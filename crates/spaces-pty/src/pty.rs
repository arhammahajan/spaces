use std::os::fd::{AsFd, AsRawFd, BorrowedFd, OwnedFd, RawFd};
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::process::{Child, Command, ExitStatus};
use std::str::FromStr;
use std::sync::Arc;

use anyhow::Context;
use nix::errno::Errno;
use nix::fcntl::{FcntlArg, FdFlag, OFlag, fcntl};
use nix::pty::{Winsize, openpty};
use nix::sys::signal::{Signal, killpg};
use nix::unistd::Pid;
use spaces_types::{PtySize, PtySpawnParams};
use thiserror::Error;

/// Structured errors returned by PTY spawn, signal, and ioctl operations.
#[derive(Debug, Error)]
pub enum PtyError {
    #[error("pty.spawn requires non-empty argv")]
    EmptyArgv,
    #[error("unsupported signal: {0}")]
    UnsupportedSignal(String),
    #[error("pgid {0} overflows i32")]
    InvalidPgid(u32),
}

/// Lock-free controller handle for resizing or signaling a spawned [`PtyChild`]
/// without contending with `wait_exit`.
#[derive(Debug, Clone)]
pub struct PtyController {
    pid: u32,
    pgid: u32,
    master: Arc<OwnedFd>,
}

impl PtyController {
    /// Returns the child process PID.
    pub fn pid(&self) -> u32 {
        self.pid
    }

    /// Returns the child process group ID (equal to `pid` after `setsid`).
    pub fn pgid(&self) -> u32 {
        self.pgid
    }

    /// Returns a cloned `Arc<OwnedFd>` for the PTY master.
    pub fn master_arc(&self) -> Arc<OwnedFd> {
        Arc::clone(&self.master)
    }

    /// Resizes the PTY window via `ioctl(TIOCSWINSZ)`.
    pub fn resize(&self, size: PtySize) -> anyhow::Result<()> {
        resize_pty(self.master.as_raw_fd(), size)
    }

    /// Sends a signal (default `SIGTERM`) to the child process group via `killpg`.
    pub fn kill(&self, signal: Option<&str>) -> anyhow::Result<()> {
        let sig = parse_signal(signal)?;
        kill_pgid(self.pgid, sig)
    }
}

/// Child process spawned inside a dedicated Unix PTY session (`setsid` + `TIOCSCTTY`).
#[derive(Debug)]
pub struct PtyChild {
    pid: u32,
    pgid: u32,
    master: Arc<OwnedFd>,
    child: Child,
    cached_exit: Option<(Option<i32>, Option<String>)>,
}

impl PtyChild {
    /// Allocates a new PTY pair and spawns `params.argv` inside a new session
    /// and process group with `params.env` injected.
    pub fn spawn(params: &PtySpawnParams) -> anyhow::Result<Self> {
        if params.argv.is_empty() {
            return Err(PtyError::EmptyArgv.into());
        }

        let winsize = to_winsize(params.size);
        let pty = openpty(&winsize, None).context("openpty failed")?;

        set_cloexec(pty.master.as_fd()).context("failed to set FD_CLOEXEC on PTY master")?;
        set_nonblock(pty.master.as_fd()).context("failed to set O_NONBLOCK on PTY master")?;
        set_cloexec(pty.slave.as_fd()).context("failed to set FD_CLOEXEC on PTY slave")?;

        let master_raw_fd = pty.master.as_raw_fd();
        let slave_raw_fd = pty.slave.as_raw_fd();

        let mut cmd = Command::new(&params.argv[0]);
        if params.argv.len() > 1 {
            cmd.args(&params.argv[1..]);
        }
        if !params.cwd.is_empty() {
            cmd.current_dir(&params.cwd);
        }
        if !params.env.contains_key("TERM") && std::env::var_os("TERM").is_none() {
            cmd.env("TERM", "xterm-256color");
        }
        cmd.envs(&params.env);

        // SAFETY: The closure runs in the forked child before `exec` and invokes
        // strictly async-signal-safe `libc` syscalls and `io::Error::last_os_error`
        // (which reads `errno` without allocating).
        unsafe {
            cmd.pre_exec(move || {
                // Reset signal mask so child inherits no blocked signals.
                let mut empty_set: libc::sigset_t = std::mem::zeroed();
                if libc::sigemptyset(&mut empty_set) != 0 {
                    return Err(std::io::Error::last_os_error());
                }
                if libc::sigprocmask(libc::SIG_SETMASK, &empty_set, std::ptr::null_mut()) != 0 {
                    return Err(std::io::Error::last_os_error());
                }

                // Reset catchable signal handlers to SIG_DFL.
                let mut sa: libc::sigaction = std::mem::zeroed();
                sa.sa_sigaction = libc::SIG_DFL;
                libc::sigemptyset(&mut sa.sa_mask);
                sa.sa_flags = 0;
                for sig in 1..32 {
                    if sig == libc::SIGKILL || sig == libc::SIGSTOP {
                        continue;
                    }
                    let _ = libc::sigaction(sig, &sa, std::ptr::null_mut());
                }

                if libc::close(master_raw_fd) != 0 {
                    return Err(std::io::Error::last_os_error());
                }

                if libc::setsid() < 0 {
                    return Err(std::io::Error::last_os_error());
                }

                if libc::ioctl(slave_raw_fd, libc::TIOCSCTTY, 0 as libc::c_int) != 0 {
                    return Err(std::io::Error::last_os_error());
                }

                // `setsid()` already assigns pgid == pid; call `setpgid(0, 0)` only
                // if not already process group leader (Linux returns EPERM on session leaders).
                if libc::getpgrp() != libc::getpid() && libc::setpgid(0, 0) != 0 {
                    return Err(std::io::Error::last_os_error());
                }

                for fd in 0..=2 {
                    if libc::dup2(slave_raw_fd, fd) < 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                    if slave_raw_fd == fd && libc::fcntl(fd, libc::F_SETFD, 0) != 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                }

                if slave_raw_fd > 2 {
                    let _ = libc::close(slave_raw_fd);
                }

                Ok(())
            });
        }

        let child = cmd
            .spawn()
            .with_context(|| format!("failed to spawn PTY command: {}", params.argv[0]))?;

        // Close parent copy of slave fd so master receives EIO when child exits.
        drop(pty.slave);

        let pid = child.id();
        let pgid = pid;
        let master = Arc::new(pty.master);

        Ok(Self {
            pid,
            pgid,
            master,
            child,
            cached_exit: None,
        })
    }

    /// Returns the child process PID.
    pub fn pid(&self) -> u32 {
        self.pid
    }

    /// Returns the child process group ID.
    pub fn pgid(&self) -> u32 {
        self.pgid
    }

    /// Returns a borrowed descriptor for the PTY master.
    pub fn master_fd(&self) -> BorrowedFd<'_> {
        self.master.as_fd()
    }

    /// Returns the raw integer fd for the PTY master.
    pub fn master_raw_fd(&self) -> RawFd {
        self.master.as_raw_fd()
    }

    /// Returns a shared `Arc<OwnedFd>` for the PTY master.
    pub fn master_arc(&self) -> Arc<OwnedFd> {
        Arc::clone(&self.master)
    }

    /// Returns a cloneable [`PtyController`] handle for resize and signal operations.
    pub fn controller(&self) -> PtyController {
        PtyController {
            pid: self.pid,
            pgid: self.pgid,
            master: Arc::clone(&self.master),
        }
    }

    /// Resizes the PTY window via `ioctl(TIOCSWINSZ)`.
    pub fn resize(&self, size: PtySize) -> anyhow::Result<()> {
        resize_pty(self.master.as_raw_fd(), size)
    }

    /// Sends a signal (default `SIGTERM`) to the child process group via `killpg`.
    pub fn kill(&self, signal: Option<&str>) -> anyhow::Result<()> {
        if self.cached_exit.is_some() {
            return Ok(());
        }
        let sig = parse_signal(signal)?;
        kill_pgid(self.pgid, sig)
    }

    /// Polls whether the child has exited without blocking.
    pub fn try_wait(&mut self) -> anyhow::Result<Option<(Option<i32>, Option<String>)>> {
        if let Some(ref cached) = self.cached_exit {
            return Ok(Some(cached.clone()));
        }
        if let Some(status) = self.child.try_wait().context("child try_wait failed")? {
            let parsed = exit_status_to_tuple(status);
            self.cached_exit = Some(parsed.clone());
            return Ok(Some(parsed));
        }
        Ok(None)
    }

    /// Blocks until the child exits and returns `(exit_code, signal_name)`.
    pub fn wait_exit(&mut self) -> anyhow::Result<(Option<i32>, Option<String>)> {
        if let Some(ref cached) = self.cached_exit {
            return Ok(cached.clone());
        }
        let status = self.child.wait().context("child wait failed")?;
        let parsed = exit_status_to_tuple(status);
        self.cached_exit = Some(parsed.clone());
        Ok(parsed)
    }
}

/// Parses an optional signal string (e.g. `"SIGTERM"`, `"TERM"`, `"KILL"`) into a [`Signal`].
pub fn parse_signal(signal: Option<&str>) -> anyhow::Result<Signal> {
    match signal {
        None => Ok(Signal::SIGTERM),
        Some(raw) => {
            let trimmed = raw.trim();
            if trimmed.is_empty() {
                return Ok(Signal::SIGTERM);
            }
            let upper = trimmed.to_ascii_uppercase();
            let normalized = if upper.starts_with("SIG") {
                upper
            } else {
                format!("SIG{upper}")
            };
            Signal::from_str(&normalized)
                .map_err(|_| PtyError::UnsupportedSignal(raw.to_owned()).into())
        }
    }
}

fn kill_pgid(pgid: u32, sig: Signal) -> anyhow::Result<()> {
    let raw_pgid = i32::try_from(pgid).map_err(|_| PtyError::InvalidPgid(pgid))?;
    match killpg(Pid::from_raw(raw_pgid), sig) {
        Ok(()) | Err(Errno::ESRCH) => Ok(()),
        Err(err) => Err(err).with_context(|| format!("killpg({pgid}, {sig}) failed")),
    }
}

fn resize_pty(master_fd: RawFd, size: PtySize) -> anyhow::Result<()> {
    let ws = libc::winsize {
        ws_row: size.rows,
        ws_col: size.cols,
        ws_xpixel: size.xpixel.unwrap_or(0),
        ws_ypixel: size.ypixel.unwrap_or(0),
    };
    let rc = unsafe { libc::ioctl(master_fd, libc::TIOCSWINSZ, &ws) };
    if rc != 0 {
        return Err(std::io::Error::last_os_error()).context("ioctl(TIOCSWINSZ) failed");
    }
    Ok(())
}

fn to_winsize(size: PtySize) -> Winsize {
    Winsize {
        ws_row: size.rows,
        ws_col: size.cols,
        ws_xpixel: size.xpixel.unwrap_or(0),
        ws_ypixel: size.ypixel.unwrap_or(0),
    }
}

fn set_cloexec(fd: BorrowedFd<'_>) -> anyhow::Result<()> {
    let flags = fcntl(fd, FcntlArg::F_GETFD)?;
    let mut fd_flags = FdFlag::from_bits_retain(flags);
    fd_flags.insert(FdFlag::FD_CLOEXEC);
    fcntl(fd, FcntlArg::F_SETFD(fd_flags))?;
    Ok(())
}

fn set_nonblock(fd: BorrowedFd<'_>) -> anyhow::Result<()> {
    let flags = fcntl(fd, FcntlArg::F_GETFL)?;
    let mut o_flags = OFlag::from_bits_retain(flags);
    o_flags.insert(OFlag::O_NONBLOCK);
    fcntl(fd, FcntlArg::F_SETFL(o_flags))?;
    Ok(())
}

fn exit_status_to_tuple(status: ExitStatus) -> (Option<i32>, Option<String>) {
    let code = status.code();
    let signal = status.signal().map(|sig_num| {
        Signal::try_from(sig_num)
            .map(|sig| sig.as_str().to_owned())
            .unwrap_or_else(|_| format!("SIG{sig_num}"))
    });
    (code, signal)
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::time::{Duration, Instant};

    use nix::unistd::{getpgid, getsid};
    use spaces_types::ThreadId;

    use super::*;

    fn base_params(argv: Vec<&str>, env: BTreeMap<String, String>) -> PtySpawnParams {
        PtySpawnParams {
            thread_id: ThreadId::from("thr_test"),
            argv: argv.into_iter().map(str::to_owned).collect(),
            cwd: "/tmp".into(),
            env,
            size: PtySize {
                cols: 80,
                rows: 24,
                xpixel: None,
                ypixel: None,
            },
        }
    }

    fn read_master_until_eof(fd: RawFd) -> String {
        let mut out = Vec::new();
        let mut buf = [0u8; 4096];
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let n = unsafe { libc::read(fd, buf.as_mut_ptr().cast(), buf.len()) };
            if n > 0 {
                out.extend_from_slice(&buf[..n as usize]);
                continue;
            }
            if n == 0 {
                break;
            }
            let err = std::io::Error::last_os_error();
            match err.raw_os_error() {
                Some(libc::EIO) => break,
                Some(libc::EINTR) => continue,
                Some(libc::EAGAIN) => {
                    if Instant::now() >= deadline {
                        panic!("timed out reading PTY master; collected={out:?}");
                    }
                    std::thread::sleep(Duration::from_millis(5));
                }
                _ => panic!("unexpected PTY read error: {err}"),
            }
        }
        String::from_utf8_lossy(&out).into_owned()
    }

    #[test]
    fn pty_echo_and_clean_exit_code() {
        let params = base_params(
            vec!["sh", "-c", "printf 'hello-pty\\n'; exit 0"],
            BTreeMap::new(),
        );
        let mut child = PtyChild::spawn(&params).expect("spawn sh");
        let output = read_master_until_eof(child.master_raw_fd());
        assert!(output.contains("hello-pty"), "output={output:?}");
        assert_eq!(child.wait_exit().expect("wait_exit"), (Some(0), None));

        let params7 = base_params(vec!["sh", "-c", "exit 7"], BTreeMap::new());
        let mut child7 = PtyChild::spawn(&params7).expect("spawn exit 7");
        let _ = read_master_until_eof(child7.master_raw_fd());
        assert_eq!(child7.wait_exit().expect("wait_exit"), (Some(7), None));
    }

    #[test]
    fn pty_creates_new_session_and_pgid_distinct_from_parent() {
        let params = base_params(vec!["sh", "-c", "sleep 5"], BTreeMap::new());
        let mut child = PtyChild::spawn(&params).expect("spawn sleep");

        let parent_pgid = getpgid(None).expect("parent pgid").as_raw() as u32;
        let child_pid = Pid::from_raw(child.pid() as i32);
        let actual_pgid = getpgid(Some(child_pid)).expect("child pgid").as_raw() as u32;
        let actual_sid = getsid(Some(child_pid)).expect("child sid").as_raw() as u32;

        assert_eq!(child.pgid(), child.pid());
        assert_eq!(actual_pgid, child.pid());
        assert_eq!(actual_sid, child.pid());
        assert_ne!(actual_pgid, parent_pgid);

        child.kill(Some("SIGKILL")).expect("kill child");
        assert_eq!(
            child.wait_exit().expect("wait_exit"),
            (None, Some("SIGKILL".into()))
        );
    }

    #[test]
    fn pty_resize_updates_winsize() {
        let params = base_params(vec!["sh", "-c", "sleep 5"], BTreeMap::new());
        let mut child = PtyChild::spawn(&params).expect("spawn sleep");

        child
            .resize(PtySize {
                cols: 132,
                rows: 48,
                xpixel: Some(1024),
                ypixel: Some(768),
            })
            .expect("resize pty");

        let mut ws: libc::winsize = unsafe { std::mem::zeroed() };
        let rc = unsafe { libc::ioctl(child.master_raw_fd(), libc::TIOCGWINSZ, &mut ws) };
        assert_eq!(rc, 0, "TIOCGWINSZ failed");
        assert_eq!(ws.ws_col, 132);
        assert_eq!(ws.ws_row, 48);
        assert_eq!(ws.ws_xpixel, 1024);
        assert_eq!(ws.ws_ypixel, 768);

        child.kill(None).expect("kill default SIGTERM");
        assert_eq!(
            child.wait_exit().expect("wait_exit"),
            (None, Some("SIGTERM".into()))
        );
    }

    #[test]
    fn pty_kill_signals_process_group() {
        let params = base_params(vec!["sh", "-c", "sleep 30"], BTreeMap::new());
        let mut child = PtyChild::spawn(&params).expect("spawn sleep");
        assert!(child.try_wait().expect("try_wait").is_none());

        child.kill(Some("TERM")).expect("kill TERM");
        let (code, sig) = child.wait_exit().expect("wait_exit");
        assert_eq!(code, None);
        assert_eq!(sig.as_deref(), Some("SIGTERM"));
    }

    #[test]
    fn pty_injects_spaces_hmac_token_env() {
        let mut env = BTreeMap::new();
        env.insert(
            "SPACES_PTY_HMAC_TOKEN".into(),
            "deadbeefcafebabe0123456789".into(),
        );
        env.insert("SPACES_PTY_ID".into(), "pty_env_test".into());
        env.insert("SPACES_THREAD_ID".into(), "thr_env_test".into());

        let params = base_params(
            vec![
                "sh",
                "-c",
                "printf '%s|%s|%s\\n' \"$SPACES_PTY_HMAC_TOKEN\" \"$SPACES_PTY_ID\" \"$SPACES_THREAD_ID\"",
            ],
            env,
        );
        let mut child = PtyChild::spawn(&params).expect("spawn env check");
        let output = read_master_until_eof(child.master_raw_fd());
        assert!(
            output.contains("deadbeefcafebabe0123456789|pty_env_test|thr_env_test"),
            "output={output:?}"
        );
        assert_eq!(child.wait_exit().expect("wait_exit"), (Some(0), None));
    }
}

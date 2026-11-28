use std::io::ErrorKind;
use std::os::fd::{AsRawFd, RawFd};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{Context, anyhow, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use spaces_types::{
    PtyAttachParams, PtyAttachResult, PtyEncoding, PtyExitNotification, PtyId, PtyOscNotification,
    PtyOutputNotification, PtySize, PtySpawnParams, RpcFrame, RpcId, RpcNotification, RpcResponse,
    ThreadId, TimestampMs, rpc_methods,
};
use tokio::io::unix::AsyncFd;
use tokio::sync::{mpsc, watch};

use crate::osc::{OscEvent, OscScanner};
use crate::pty::{PtyChild, PtyController};
use crate::ring::ReplayRing;

/// Maximum raw byte length of a single `pty.output` notification chunk (32 KiB).
pub const PTY_OUTPUT_CHUNK_BYTES: usize = 32 * 1024;

/// Active or recently exited PTY session managed by `__pty-host`.
#[derive(Debug)]
pub struct PtySessionHandle {
    pty_id: PtyId,
    thread_id: ThreadId,
    pid: u32,
    pgid: u32,
    ring: Arc<Mutex<ReplayRing>>,
    controller: PtyController,
    input_tx: mpsc::Sender<Vec<u8>>,
    exit_rx: watch::Receiver<bool>,
    exited_at: Option<Instant>,
}

impl PtySessionHandle {
    /// Spawns a child process inside a new PTY and launches the async read/write/reap tasks.
    pub fn spawn(
        pty_id: PtyId,
        params: PtySpawnParams,
        out_tx: mpsc::UnboundedSender<RpcFrame>,
        on_exit_tx: Option<mpsc::UnboundedSender<PtyId>>,
    ) -> anyhow::Result<Self> {
        let mut child = PtyChild::spawn(&params)?;
        let pid = child.pid();
        let pgid = child.pgid();
        let controller = child.controller();
        let thread_id = params.thread_id.clone();

        // SAFETY: `Arc<OwnedFd>` owns an open PTY master fd whose integer fd
        // stays valid and unchanged for the entire lifetime of `AsyncFd`.
        let async_fd = Arc::new(
            unsafe { AsyncFd::register(child.master_arc()) }
                .context("failed to register PTY master with AsyncFd")?,
        );
        let ring = Arc::new(Mutex::new(ReplayRing::new()));
        let (input_tx, mut input_rx) = mpsc::channel::<Vec<u8>>(1024);
        let (done_tx, mut done_rx) = watch::channel(false);
        let (exit_tx, exit_rx) = watch::channel(false);

        let async_fd_writer = Arc::clone(&async_fd);
        tokio::spawn(async move {
            loop {
                let data = tokio::select! {
                    biased;
                    _ = done_rx.changed() => break,
                    maybe_data = input_rx.recv() => match maybe_data {
                        Some(bytes) => bytes,
                        None => break,
                    }
                };
                let mut written = 0usize;
                while written < data.len() {
                    let mut guard = tokio::select! {
                        biased;
                        _ = done_rx.changed() => return,
                        res = async_fd_writer.writable() => match res {
                            Ok(g) => g,
                            Err(_) => return,
                        }
                    };
                    match guard.try_io(|inner| write_nonblock(inner.as_raw_fd(), &data[written..]))
                    {
                        Ok(Ok(0)) => return,
                        Ok(Ok(n)) => written += n,
                        Ok(Err(err)) if err.kind() == ErrorKind::Interrupted => continue,
                        Ok(Err(_)) => return,
                        Err(_would_block) => continue,
                    }
                }
            }
        });

        let reader_pty_id = pty_id.clone();
        let reader_ring = Arc::clone(&ring);
        tokio::spawn(async move {
            let mut scanner = OscScanner::new();
            let mut buf = vec![0u8; PTY_OUTPUT_CHUNK_BYTES];

            loop {
                let mut guard = match async_fd.readable().await {
                    Ok(g) => g,
                    Err(err) => {
                        tracing::warn!(pty_id = %reader_pty_id, error = %err, "AsyncFd readable failed");
                        break;
                    }
                };

                match guard.try_io(|inner| read_nonblock(inner.as_raw_fd(), &mut buf)) {
                    Ok(Ok(0)) => break,
                    Ok(Ok(n)) => {
                        let chunk = &buf[..n];
                        emit_live_chunk(&reader_pty_id, &reader_ring, chunk, &out_tx, &mut scanner);
                    }
                    Ok(Err(err)) if err.kind() == ErrorKind::Interrupted => continue,
                    Ok(Err(err)) if err.raw_os_error() == Some(libc::EIO) => break,
                    Ok(Err(err)) => {
                        tracing::warn!(pty_id = %reader_pty_id, error = %err, "PTY master read failed");
                        break;
                    }
                    Err(_would_block) => continue,
                }
            }

            let _ = done_tx.send(true);
            drop(async_fd);

            let wait_res = tokio::task::spawn_blocking(move || child.wait_exit()).await;
            let (exit_code, signal) = match wait_res {
                Ok(Ok((code, sig))) => (code, sig),
                Ok(Err(err)) => {
                    tracing::warn!(pty_id = %reader_pty_id, error = %err, "failed to reap PTY child");
                    (None, None)
                }
                Err(err) => {
                    tracing::warn!(pty_id = %reader_pty_id, error = %err, "PTY reaper task panicked");
                    (None, None)
                }
            };

            let _ = exit_tx.send(true);

            let exit_notif = PtyExitNotification {
                pty_id: reader_pty_id.clone(),
                exit_code,
                signal,
                ts_ms: now_ms(),
            };
            if let Ok(params) = serde_json::to_value(&exit_notif) {
                let _ = out_tx.send(RpcFrame::Notification(RpcNotification::new(
                    rpc_methods::PTY_EXIT,
                    Some(params),
                )));
            }
            if let Some(tx) = on_exit_tx {
                let _ = tx.send(reader_pty_id);
            }
        });

        Ok(Self {
            pty_id,
            thread_id,
            pid,
            pgid,
            ring,
            controller,
            input_tx,
            exit_rx,
            exited_at: None,
        })
    }

    /// Returns the session's `PtyId`.
    pub fn pty_id(&self) -> &PtyId {
        &self.pty_id
    }

    /// Returns the owning `ThreadId`.
    pub fn thread_id(&self) -> &ThreadId {
        &self.thread_id
    }

    /// Returns the child process PID.
    pub fn pid(&self) -> u32 {
        self.pid
    }

    /// Returns the child process group ID.
    pub fn pgid(&self) -> u32 {
        self.pgid
    }

    /// Returns a reference to the shared replay ring mutex.
    pub fn ring(&self) -> &Arc<Mutex<ReplayRing>> {
        &self.ring
    }

    /// Returns `true` once the child has exited and been reaped.
    pub fn is_exited(&self) -> bool {
        *self.exit_rx.borrow()
    }

    /// Marks the timestamp when this session exited (used for pruning stale exited entries).
    pub fn mark_exited_now(&mut self) {
        if self.exited_at.is_none() {
            self.exited_at = Some(Instant::now());
        }
    }

    /// Returns when this session exited, if known.
    pub fn exited_at(&self) -> Option<Instant> {
        self.exited_at
    }

    /// Queues raw input bytes to be written asynchronously to the PTY master.
    pub async fn write_input(&self, data: Vec<u8>) -> anyhow::Result<()> {
        if self.is_exited() {
            bail!("PTY session {} has already exited", self.pty_id);
        }
        self.input_tx
            .send(data)
            .await
            .map_err(|_| anyhow!("PTY input channel closed for {}", self.pty_id))
    }

    /// Resizes the PTY window via `ioctl(TIOCSWINSZ)`.
    pub fn resize(&self, size: PtySize) -> anyhow::Result<()> {
        if self.is_exited() {
            bail!("PTY session {} has already exited", self.pty_id);
        }
        self.controller.resize(size)
    }

    /// Signals the child process group (`killpg`), ignoring already-exited sessions.
    pub fn kill(&self, signal: Option<&str>) -> anyhow::Result<()> {
        if self.is_exited() {
            return Ok(());
        }
        self.controller.kill(signal)
    }

    /// Atomically snapshots the replay ring, emits `PtyAttachResult`, and if
    /// `params.request_replay` is `true`, emits `pty.output` notifications in
    /// `<= 32 KiB` chunks before releasing the ring lock so live chunks never interleave.
    pub fn attach(
        &self,
        req_id: RpcId,
        params: &PtyAttachParams,
        out_tx: &mpsc::UnboundedSender<RpcFrame>,
    ) -> anyhow::Result<()> {
        let guard = self
            .ring
            .lock()
            .map_err(|_| anyhow!("replay ring mutex poisoned"))?;
        let slice = guard.read_after(params.after_offset);
        let result = PtyAttachResult {
            pty_id: self.pty_id.clone(),
            current_offset: slice.current_offset,
            replay_truncated: slice.truncated,
        };
        let resp = RpcResponse::ok(req_id, serde_json::to_value(&result)?);
        out_tx
            .send(RpcFrame::Response(resp))
            .map_err(|_| anyhow!("RPC outbound channel closed"))?;

        if params.request_replay && !slice.bytes.is_empty() {
            let mut offset = slice
                .current_offset
                .saturating_sub(slice.bytes.len() as u64);
            for chunk in slice.bytes.chunks(PTY_OUTPUT_CHUNK_BYTES) {
                offset = offset.saturating_add(chunk.len() as u64);
                let notif = PtyOutputNotification {
                    pty_id: self.pty_id.clone(),
                    offset,
                    encoding: PtyEncoding::Base64,
                    data_b64: STANDARD.encode(chunk),
                    ts_ms: now_ms(),
                };
                out_tx
                    .send(RpcFrame::Notification(RpcNotification::new(
                        rpc_methods::PTY_OUTPUT,
                        Some(serde_json::to_value(&notif)?),
                    )))
                    .map_err(|_| anyhow!("RPC outbound channel closed"))?;
            }
        }
        Ok(())
    }

    /// Waits up to `timeout` for the child to exit and be reaped.
    pub async fn wait_exited(&self, timeout: Duration) -> bool {
        if self.is_exited() {
            return true;
        }
        let mut rx = self.exit_rx.clone();
        tokio::time::timeout(timeout, rx.wait_for(|&done| done))
            .await
            .is_ok()
    }
}

fn emit_live_chunk(
    pty_id: &PtyId,
    ring: &Arc<Mutex<ReplayRing>>,
    chunk: &[u8],
    out_tx: &mpsc::UnboundedSender<RpcFrame>,
    scanner: &mut OscScanner,
) {
    {
        let mut guard = ring.lock().expect("replay ring mutex poisoned");
        let offset = guard.push(chunk);
        let notif = PtyOutputNotification {
            pty_id: pty_id.clone(),
            offset,
            encoding: PtyEncoding::Base64,
            data_b64: STANDARD.encode(chunk),
            ts_ms: now_ms(),
        };
        if let Ok(params) = serde_json::to_value(&notif) {
            let _ = out_tx.send(RpcFrame::Notification(RpcNotification::new(
                rpc_methods::PTY_OUTPUT,
                Some(params),
            )));
        }
    }

    scanner.feed(chunk, |ev| {
        let notif = match ev {
            OscEvent::Status(status) => PtyOscNotification {
                pty_id: pty_id.clone(),
                status: Some(status),
                resume_hint: None,
                ts_ms: now_ms(),
            },
            OscEvent::ResumeHint(hint) => PtyOscNotification {
                pty_id: pty_id.clone(),
                status: None,
                resume_hint: Some(hint),
                ts_ms: now_ms(),
            },
        };
        if let Ok(params) = serde_json::to_value(&notif) {
            let _ = out_tx.send(RpcFrame::Notification(RpcNotification::new(
                rpc_methods::PTY_OSC,
                Some(params),
            )));
        }
    });
}

fn read_nonblock(fd: RawFd, buf: &mut [u8]) -> std::io::Result<usize> {
    let rc = unsafe { libc::read(fd, buf.as_mut_ptr().cast(), buf.len()) };
    if rc < 0 {
        Err(std::io::Error::last_os_error())
    } else {
        Ok(rc as usize)
    }
}

fn write_nonblock(fd: RawFd, buf: &[u8]) -> std::io::Result<usize> {
    let rc = unsafe { libc::write(fd, buf.as_ptr().cast(), buf.len()) };
    if rc < 0 {
        Err(std::io::Error::last_os_error())
    } else {
        Ok(rc as usize)
    }
}

/// Returns current Unix epoch time in milliseconds.
pub fn now_ms() -> TimestampMs {
    let ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis();
    TimestampMs(i64::try_from(ms).unwrap_or(i64::MAX))
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use spaces_types::OscState;

    use super::*;

    #[tokio::test]
    async fn session_streams_output_osc_7501_7577_and_exit() {
        let (out_tx, mut out_rx) = mpsc::unbounded_channel();
        let (exit_tx, mut exit_rx) = mpsc::unbounded_channel();

        let msg_b64 = STANDARD.encode("working hard");
        let cmd = format!(
            "printf '\\033]7501;state=working:app=claude:msg={msg_b64}\\007'; \
             printf '\\033]7577;agent=claude:session_id=s_1:sig={}\\007'; \
             printf 'hello-session\\n'; \
             exit 3",
            "b".repeat(64)
        );
        let params = PtySpawnParams {
            thread_id: ThreadId::from("thr_sess"),
            argv: vec!["sh".into(), "-c".into(), cmd],
            cwd: "/tmp".into(),
            env: BTreeMap::new(),
            size: PtySize {
                cols: 80,
                rows: 24,
                xpixel: None,
                ypixel: None,
            },
        };

        let session = PtySessionHandle::spawn(
            PtyId::from("pty_sess_1"),
            params,
            out_tx.clone(),
            Some(exit_tx),
        )
        .expect("spawn session");

        let exited_id = tokio::time::timeout(Duration::from_secs(5), exit_rx.recv())
            .await
            .expect("exit timeout")
            .expect("exit channel");
        assert_eq!(exited_id, PtyId::from("pty_sess_1"));
        assert!(session.is_exited());

        let mut saw_output = String::new();
        let mut saw_status = false;
        let mut saw_hint = false;
        let mut saw_exit = None;

        while let Ok(frame) = out_rx.try_recv() {
            if let RpcFrame::Notification(notif) = frame {
                match notif.method.as_str() {
                    rpc_methods::PTY_OUTPUT => {
                        let p: PtyOutputNotification =
                            serde_json::from_value(notif.params.unwrap()).unwrap();
                        assert!(p.offset > 0);
                        let bytes = STANDARD.decode(&p.data_b64).unwrap();
                        assert!(bytes.len() <= PTY_OUTPUT_CHUNK_BYTES);
                        saw_output.push_str(&String::from_utf8_lossy(&bytes));
                    }
                    rpc_methods::PTY_OSC => {
                        let p: PtyOscNotification =
                            serde_json::from_value(notif.params.unwrap()).unwrap();
                        if let Some(status) = p.status {
                            assert_eq!(status.state, OscState::Working);
                            assert_eq!(status.msg.as_deref(), Some("working hard"));
                            saw_status = true;
                        }
                        if let Some(hint) = p.resume_hint {
                            assert_eq!(hint.agent, "claude");
                            assert_eq!(hint.session_id, "s_1");
                            saw_hint = true;
                        }
                    }
                    rpc_methods::PTY_EXIT => {
                        let p: PtyExitNotification =
                            serde_json::from_value(notif.params.unwrap()).unwrap();
                        saw_exit = Some(p);
                    }
                    other => panic!("unexpected notification {other}"),
                }
            }
        }

        assert!(
            saw_output.contains("hello-session"),
            "output={saw_output:?}"
        );
        assert!(saw_status);
        assert!(saw_hint);
        let exit_notif = saw_exit.expect("expected pty.exit");
        assert_eq!(exit_notif.exit_code, Some(3));
        assert_eq!(exit_notif.signal, None);

        // Verify attach replays buffered bytes in <= 32 KiB chunks.
        session
            .attach(
                RpcId::Number(9),
                &PtyAttachParams {
                    pty_id: PtyId::from("pty_sess_1"),
                    after_offset: Some(0),
                    request_replay: true,
                },
                &out_tx,
            )
            .expect("attach after exit");

        let resp_frame = out_rx.try_recv().expect("attach response");
        assert!(matches!(resp_frame, RpcFrame::Response(_)));
        let replay_frame = out_rx.try_recv().expect("replay output");
        match replay_frame {
            RpcFrame::Notification(n) => {
                assert_eq!(n.method, rpc_methods::PTY_OUTPUT);
                let p: PtyOutputNotification = serde_json::from_value(n.params.unwrap()).unwrap();
                let decoded = STANDARD.decode(&p.data_b64).unwrap();
                assert!(String::from_utf8_lossy(&decoded).contains("hello-session"));
            }
            other => panic!("expected replay notification, got {other:?}"),
        }
    }

    #[tokio::test]
    async fn session_input_and_large_replay_chunking() {
        let (out_tx, mut out_rx) = mpsc::unbounded_channel();
        let params = PtySpawnParams {
            thread_id: ThreadId::from("thr_echo"),
            argv: vec!["cat".into()],
            cwd: "/tmp".into(),
            env: BTreeMap::new(),
            size: PtySize {
                cols: 80,
                rows: 24,
                xpixel: None,
                ypixel: None,
            },
        };

        let session = PtySessionHandle::spawn(PtyId::from("pty_cat"), params, out_tx.clone(), None)
            .expect("spawn cat");

        // Write > 32 KiB through ring directly or via input to verify replay chunking.
        let payload = vec![b'X'; 40_000];
        {
            let mut ring = session.ring().lock().unwrap();
            ring.push(&payload);
        }

        session
            .attach(
                RpcId::Number(1),
                &PtyAttachParams {
                    pty_id: PtyId::from("pty_cat"),
                    after_offset: Some(0),
                    request_replay: true,
                },
                &out_tx,
            )
            .expect("attach");

        let mut chunk_sizes = Vec::new();
        while let Ok(frame) = out_rx.try_recv() {
            if let RpcFrame::Notification(n) = frame
                && n.method == rpc_methods::PTY_OUTPUT
            {
                let p: PtyOutputNotification = serde_json::from_value(n.params.unwrap()).unwrap();
                let bytes = STANDARD.decode(&p.data_b64).unwrap();
                assert!(bytes.len() <= PTY_OUTPUT_CHUNK_BYTES);
                chunk_sizes.push(bytes.len());
            }
        }
        assert_eq!(
            chunk_sizes,
            vec![PTY_OUTPUT_CHUNK_BYTES, 40_000 - PTY_OUTPUT_CHUNK_BYTES]
        );

        session
            .write_input(b"ping\n".to_vec())
            .await
            .expect("write input");
        // Send Ctrl-D (EOF) or kill to exit cat cleanly.
        session.kill(Some("SIGTERM")).expect("kill cat");
        assert!(session.wait_exited(Duration::from_secs(5)).await);
    }
}

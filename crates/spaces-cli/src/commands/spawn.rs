use std::collections::BTreeMap;
use std::io::IsTerminal;
use std::os::fd::AsRawFd;
use std::path::PathBuf;

use anyhow::{Context, Result};
use clap::Args;
use serde::Serialize;
use spaces_types::rpc::rpc_methods;
use spaces_types::rpc_core::{
    PtySpawnParams, PtySpawnResult, ThreadSpawnParams, ThreadSpawnResult,
};
use spaces_types::{PtyId, Thread, ThreadDriver, ThreadId, ThreadKind};

use crate::commands::attach::{AttachOptions, attach_resolved};
use crate::rpc::RpcClient;
use crate::terminal::query_terminal_size;

#[derive(Debug, Clone, Args)]
pub struct SpawnArgs {
    /// Agent or command to spawn (e.g. claude, codex, opencode, bash)
    pub agent: String,
    /// Working directory for the spawned thread/PTY (defaults to current directory)
    #[arg(long)]
    pub cwd: Option<PathBuf>,
    /// Agent profile name
    #[arg(long)]
    pub profile: Option<String>,
    /// Space name to associate with the thread
    #[arg(long)]
    pub space: Option<String>,
    /// Optional title for the thread
    #[arg(long)]
    pub title: Option<String>,
    /// Optional model identifier
    #[arg(long)]
    pub model: Option<String>,
    /// Immediately attach to the spawned PTY after creation
    #[arg(long)]
    pub attach: bool,
    /// Arguments forwarded to the spawned command after `--`
    #[arg(last = true, allow_hyphen_values = true)]
    pub args: Vec<String>,
}

#[derive(Serialize)]
struct SpawnOutput<'a> {
    thread_id: &'a ThreadId,
    pty_id: &'a PtyId,
    pid: u32,
    pgid: u32,
    thread: &'a Thread,
}

/// Spawns a managed thread and underlying PTY, optionally attaching immediately.
pub async fn run(client: RpcClient, args: SpawnArgs, json: bool) -> Result<()> {
    let attach = args.attach;
    let (spawn_res, pty_res) = spawn_thread_and_pty(&client, args).await?;

    let summary = SpawnOutput {
        thread_id: &spawn_res.thread.id,
        pty_id: &pty_res.pty_id,
        pid: pty_res.pid,
        pgid: pty_res.pgid,
        thread: &spawn_res.thread,
    };

    if attach {
        if json {
            eprintln!("{}", serde_json::to_string(&summary)?);
        } else if std::io::stderr().is_terminal() {
            eprintln!(
                "thread_id={} pty_id={} (detach with Ctrl-] d)",
                spawn_res.thread.id, pty_res.pty_id
            );
        }
        attach_resolved(
            client,
            Some(spawn_res.thread.id),
            pty_res.pty_id,
            AttachOptions {
                takeover: false,
                read_only: false,
                no_replay: false,
            },
        )
        .await
    } else {
        if json {
            println!("{}", serde_json::to_string_pretty(&summary)?);
        } else {
            println!(
                "thread_id={} pty_id={}",
                spawn_res.thread.id, pty_res.pty_id
            );
        }
        Ok(())
    }
}

async fn spawn_thread_and_pty(
    client: &RpcClient,
    args: SpawnArgs,
) -> Result<(ThreadSpawnResult, PtySpawnResult)> {
    let cwd = resolve_cwd(args.cwd.as_deref())?;
    let cwd_str = cwd.to_string_lossy().into_owned();

    let spawn_res: ThreadSpawnResult = client
        .call(
            rpc_methods::THREAD_SPAWN,
            ThreadSpawnParams {
                agent: args.agent.clone(),
                profile: args.profile,
                driver: Some(ThreadDriver::NativePty),
                kind: Some(ThreadKind::Interactive),
                cwd: cwd_str.clone(),
                space: args.space,
                window_id: None,
                pane_id: None,
                worktree_path: None,
                vendor_session_id: None,
                title: args.title,
                model: args.model,
                initial_prompt: None,
                env: None,
            },
        )
        .await
        .context("thread.spawn RPC failed")?;

    let mut argv = Vec::with_capacity(1 + args.args.len());
    argv.push(args.agent);
    argv.extend(args.args);

    let size = query_terminal_size(std::io::stdin().as_raw_fd());
    let pty_res: PtySpawnResult = client
        .call(
            rpc_methods::PTY_SPAWN,
            PtySpawnParams {
                thread_id: spawn_res.thread.id.clone(),
                argv,
                cwd: cwd_str,
                env: BTreeMap::new(),
                size,
            },
        )
        .await
        .context("pty.spawn RPC failed")?;

    Ok((spawn_res, pty_res))
}

fn resolve_cwd(override_cwd: Option<&std::path::Path>) -> Result<PathBuf> {
    let cwd = match override_cwd {
        Some(dir) if dir.is_absolute() => dir.to_path_buf(),
        Some(dir) => std::env::current_dir()
            .context("failed to determine current working directory")?
            .join(dir),
        None => std::env::current_dir().context("failed to determine current working directory")?,
    };
    Ok(cwd)
}

#[cfg(test)]
mod tests {
    use spaces_types::rpc_core::{RpcAuthResult, RpcRequest, RpcResponse};
    use spaces_types::{BootId, Epoch, Seq, ThreadState, TimestampMs};
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    use tokio::net::UnixListener;

    use super::*;
    use crate::endpoint::DaemonEndpoint;

    #[tokio::test]
    async fn spawn_issues_thread_spawn_then_pty_spawn() {
        let dir = std::env::temp_dir().join(format!("spaces-cli-spawn-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).unwrap();

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();

            while reader.read_line(&mut line).await.unwrap() > 0 {
                let req: RpcRequest = serde_json::from_str(line.trim()).unwrap();
                let result = match req.method.as_str() {
                    rpc_methods::AUTH => serde_json::to_value(RpcAuthResult {
                        ok: true,
                        protocol_version: 1,
                        boot_id: BootId::from("boot_s"),
                        client_id: "cli-s".to_owned(),
                        current_seq: Seq(0),
                    })
                    .unwrap(),
                    rpc_methods::THREAD_SPAWN => {
                        let p: ThreadSpawnParams =
                            serde_json::from_value(req.params.unwrap()).unwrap();
                        assert_eq!(p.agent, "claude");
                        assert_eq!(p.profile.as_deref(), Some("work"));
                        assert_eq!(p.space.as_deref(), Some("dev"));
                        assert_eq!(p.cwd, "/tmp");
                        serde_json::to_value(ThreadSpawnResult {
                            thread: Thread {
                                id: ThreadId::from("thr_new"),
                                space: p.space,
                                window_id: None,
                                pane_id: None,
                                pty_id: None,
                                agent: p.agent,
                                profile: p.profile,
                                driver: ThreadDriver::NativePty,
                                kind: ThreadKind::Interactive,
                                state: ThreadState::Created,
                                cwd: p.cwd.clone(),
                                canonical_cwd: p.cwd,
                                worktree_path: None,
                                vendor_session_id: None,
                                title: p.title,
                                model: p.model,
                                epoch: Epoch(0),
                                active_turn_id: None,
                                created_at_ms: TimestampMs(1),
                                updated_at_ms: TimestampMs(1),
                            },
                            lease: None,
                        })
                        .unwrap()
                    }
                    rpc_methods::PTY_SPAWN => {
                        let p: PtySpawnParams =
                            serde_json::from_value(req.params.unwrap()).unwrap();
                        assert_eq!(p.thread_id, ThreadId::from("thr_new"));
                        assert_eq!(p.argv, vec!["claude", "--resume", "abc"]);
                        assert_eq!(p.cwd, "/tmp");
                        assert!(p.size.cols > 0 && p.size.rows > 0);
                        serde_json::to_value(PtySpawnResult {
                            pty_id: PtyId::from("pty_new"),
                            pid: 1234,
                            pgid: 1234,
                        })
                        .unwrap()
                    }
                    other => panic!("unexpected call: {other}"),
                };

                let mut raw = serde_json::to_string(&RpcResponse::ok(req.id, result)).unwrap();
                raw.push('\n');
                write_half.write_all(raw.as_bytes()).await.unwrap();
                if req.method == rpc_methods::PTY_SPAWN {
                    break;
                }
                line.clear();
            }
        });

        let endpoint = DaemonEndpoint {
            version: 1,
            protocol: "ndjson-rpc/uds".to_owned(),
            pid: 1,
            uid: 1000,
            socket: sock_path.to_string_lossy().into_owned(),
            token: "tok".to_owned(),
            boot_id: BootId::from("boot_s"),
            started_ms: TimestampMs(1),
        };
        let client = RpcClient::connect(&endpoint).await.unwrap();
        let (thread_res, pty_res) = spawn_thread_and_pty(
            &client,
            SpawnArgs {
                agent: "claude".to_owned(),
                cwd: Some(PathBuf::from("/tmp")),
                profile: Some("work".to_owned()),
                space: Some("dev".to_owned()),
                title: Some("t".to_owned()),
                model: Some("m".to_owned()),
                attach: false,
                args: vec!["--resume".to_owned(), "abc".to_owned()],
            },
        )
        .await
        .unwrap();

        assert_eq!(thread_res.thread.id, ThreadId::from("thr_new"));
        assert_eq!(pty_res.pty_id, PtyId::from("pty_new"));
        server.await.unwrap();
        std::fs::remove_dir_all(&dir).ok();
    }
}

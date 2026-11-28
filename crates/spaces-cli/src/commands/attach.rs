use std::io::{IsTerminal, Write};
use std::os::fd::{AsRawFd, RawFd};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD as BASE64_STANDARD;
use clap::Args;
use serde::Deserialize;
use serde_json::Value;
use spaces_types::rpc::rpc_methods;
use spaces_types::rpc_core::{
    LeaseClaimParams, LeaseHandoffParams, LeaseResult, LeaseStatusParams, LeaseYieldParams,
    PtyAttachParams, PtyAttachResult, PtyDetachParams, PtyEncoding, PtyExitNotification,
    PtyInputParams, PtyOutputNotification, PtyResizeParams, RpcErrorCode, ThreadGetParams,
    ThreadListParams, ThreadListResult,
};
use spaces_types::{
    Epoch, HandoffReason, LeaseOwner, LeaseOwnerKind, LeaseState, PtyId, Thread, ThreadId,
};
use tokio::io::{AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::mpsc;

use crate::commands::{default_human_owner_id, format_lease_owner, format_lease_state};
use crate::rpc::{RpcClient, RpcRemoteError};
use crate::terminal::{DetachAction, DetachEscapeParser, RawModeGuard, query_terminal_size};

const CLEANUP_TIMEOUT: Duration = Duration::from_secs(2);

#[derive(Debug, Clone, Args)]
pub struct AttachArgs {
    /// Target thread ID (e.g. thr_...) or PTY ID (pty_...)
    pub target: String,
    /// Take over an existing write lease held by another actor
    #[arg(long)]
    pub takeover: bool,
    /// Attach in read-only observer mode without claiming a write lease or forwarding input
    #[arg(long)]
    pub read_only: bool,
    /// Skip buffered PTY replay output upon attaching
    #[arg(long)]
    pub no_replay: bool,
}

#[derive(Debug, Clone, Copy)]
pub struct AttachOptions {
    pub takeover: bool,
    pub read_only: bool,
    pub no_replay: bool,
}

#[derive(Deserialize)]
#[serde(untagged)]
enum ThreadGetResponse {
    Wrapped { thread: Thread },
    Direct(Thread),
}

impl ThreadGetResponse {
    fn into_thread(self) -> Thread {
        match self {
            Self::Wrapped { thread } => thread,
            Self::Direct(thread) => thread,
        }
    }
}

/// Resolves `<target>` to a thread/PTY pair and starts an interactive attach session.
pub async fn run(client: RpcClient, args: AttachArgs) -> Result<()> {
    let (thread_id, pty_id) = resolve_target(&client, &args.target).await?;
    attach_resolved(
        client,
        thread_id,
        pty_id,
        AttachOptions {
            takeover: args.takeover,
            read_only: args.read_only,
            no_replay: args.no_replay,
        },
    )
    .await
}

async fn resolve_target(client: &RpcClient, target: &str) -> Result<(Option<ThreadId>, PtyId)> {
    if target.starts_with("pty_") {
        let pty_id = PtyId::from(target);
        let list: ThreadListResult = client
            .call(
                rpc_methods::THREAD_LIST,
                ThreadListParams {
                    include_closed: Some(true),
                    ..Default::default()
                },
            )
            .await
            .context("failed to list threads while resolving pty target")?;
        let thread_id = list
            .threads
            .into_iter()
            .find(|t| t.pty_id.as_ref() == Some(&pty_id))
            .map(|t| t.id);
        return Ok((thread_id, pty_id));
    }

    let resp: ThreadGetResponse = client
        .call(
            rpc_methods::THREAD_GET,
            ThreadGetParams {
                thread_id: ThreadId::from(target),
            },
        )
        .await
        .with_context(|| format!("failed to look up thread {target}"))?;
    let thread = resp.into_thread();
    let Some(pty_id) = thread.pty_id else {
        bail!("thread {} has no active PTY (pty_id is none)", thread.id);
    };
    Ok((Some(thread.id), pty_id))
}

/// Attaches to an already-resolved PTY (and optional owning thread) using process stdio.
pub async fn attach_resolved(
    client: RpcClient,
    thread_id: Option<ThreadId>,
    pty_id: PtyId,
    opts: AttachOptions,
) -> Result<()> {
    let stdin_fd = std::io::stdin().as_raw_fd();
    let is_tty = std::io::stdin().is_terminal();
    let stdin_rx = spawn_stdin_reader();
    let stdout = tokio::io::stdout();
    attach_with_io(
        client, thread_id, pty_id, opts, stdin_fd, is_tty, stdin_rx, stdout,
    )
    .await
}

#[allow(clippy::too_many_arguments)]
async fn attach_with_io<W: AsyncWrite + Unpin>(
    mut client: RpcClient,
    thread_id: Option<ThreadId>,
    pty_id: PtyId,
    opts: AttachOptions,
    stdin_fd: RawFd,
    is_tty: bool,
    mut stdin_rx: mpsc::UnboundedReceiver<std::io::Result<Vec<u8>>>,
    mut output: W,
) -> Result<()> {
    let (lease_epoch, acquired_lease_owner) =
        acquire_attach_lease(&client, thread_id.as_ref(), opts).await?;

    let mut notifs = client.take_notifications();
    let client = Arc::new(client);

    let _attach_res: PtyAttachResult = client
        .call(
            rpc_methods::PTY_ATTACH,
            PtyAttachParams {
                pty_id: pty_id.clone(),
                after_offset: None,
                request_replay: !opts.no_replay,
            },
        )
        .await
        .context("pty.attach RPC failed")?;

    let raw_guard = if is_tty {
        Some(RawModeGuard::enter(stdin_fd)?)
    } else {
        None
    };

    let initial_size = query_terminal_size(stdin_fd);
    let _ = client
        .notify(
            rpc_methods::PTY_RESIZE,
            PtyResizeParams {
                pty_id: pty_id.clone(),
                size: initial_size,
            },
        )
        .await;

    let mut sigwinch =
        signal(SignalKind::window_change()).context("failed to listen for SIGWINCH")?;
    let mut sigterm = signal(SignalKind::terminate()).context("failed to listen for SIGTERM")?;

    let mut stdin_open = true;
    let mut parser = DetachEscapeParser::new();

    enum ExitReason {
        UserDetach,
        PtyExited,
        Disconnected,
    }

    let exit_reason = 'attach_loop: loop {
        tokio::select! {
            maybe_notif = notifs.recv() => {
                let Some(notif) = maybe_notif else {
                    break ExitReason::Disconnected;
                };
                if notif.method == rpc_methods::PTY_OUTPUT {
                    if let Some(params) = notif.params
                        && let Ok(out) = serde_json::from_value::<PtyOutputNotification>(params)
                        && out.pty_id == pty_id
                        && out.encoding == PtyEncoding::Base64
                    {
                        let bytes = BASE64_STANDARD
                            .decode(&out.data_b64)
                            .context("invalid base64 payload in pty.output")?;
                        output.write_all(&bytes).await?;
                        output.flush().await?;
                    }
                } else if notif.method == rpc_methods::PTY_EXIT
                    && let Some(params) = notif.params
                    && let Ok(exit) = serde_json::from_value::<PtyExitNotification>(params)
                    && exit.pty_id == pty_id
                {
                    break ExitReason::PtyExited;
                }
            }
            maybe_stdin = stdin_rx.recv(), if stdin_open => {
                match maybe_stdin {
                    Some(Ok(chunk)) => {
                        for action in parser.feed(&chunk) {
                            match action {
                                DetachAction::Passthrough(bytes) => {
                                    if !opts.read_only {
                                        send_pty_input(&client, &pty_id, lease_epoch, &bytes).await?;
                                    }
                                }
                                DetachAction::Detach => {
                                    break 'attach_loop ExitReason::UserDetach;
                                }
                            }
                        }
                    }
                    Some(Err(err)) => {
                        return Err(err).context("failed to read from stdin");
                    }
                    None => {
                        if let Some(DetachAction::Passthrough(bytes)) = parser.flush()
                            && !opts.read_only
                        {
                            send_pty_input(&client, &pty_id, lease_epoch, &bytes).await?;
                        }
                        stdin_open = false;
                        if is_tty {
                            break ExitReason::UserDetach;
                        }
                    }
                }
            }
            Some(()) = sigwinch.recv() => {
                let size = query_terminal_size(stdin_fd);
                let _ = client
                    .notify(
                        rpc_methods::PTY_RESIZE,
                        PtyResizeParams {
                            pty_id: pty_id.clone(),
                            size,
                        },
                    )
                    .await;
            }
            Some(()) = sigterm.recv() => {
                break ExitReason::UserDetach;
            }
        }
    };

    drop(raw_guard);
    let _ = std::io::stdout().flush();

    match exit_reason {
        ExitReason::UserDetach => {
            let _ = tokio::time::timeout(
                CLEANUP_TIMEOUT,
                client.call::<_, Value>(
                    rpc_methods::PTY_DETACH,
                    PtyDetachParams {
                        pty_id: pty_id.clone(),
                    },
                ),
            )
            .await;

            if let (Some(tid), Some(epoch), Some(owner)) =
                (thread_id, lease_epoch, acquired_lease_owner)
            {
                let _ = tokio::time::timeout(
                    CLEANUP_TIMEOUT,
                    client.call::<_, LeaseResult>(
                        rpc_methods::LEASE_YIELD,
                        LeaseYieldParams {
                            thread_id: tid,
                            epoch,
                            owner,
                            reason: Some(HandoffReason::UserYield),
                        },
                    ),
                )
                .await;
            }

            if is_tty {
                eprintln!("\r\n[detached from {pty_id}]");
            }
            Ok(())
        }
        ExitReason::PtyExited => {
            let _ = tokio::time::timeout(
                CLEANUP_TIMEOUT,
                client.call::<_, Value>(rpc_methods::PTY_DETACH, PtyDetachParams { pty_id }),
            )
            .await;
            Ok(())
        }
        ExitReason::Disconnected => {
            bail!("daemon RPC connection closed while attached to {pty_id}");
        }
    }
}

async fn acquire_attach_lease(
    client: &RpcClient,
    thread_id: Option<&ThreadId>,
    opts: AttachOptions,
) -> Result<(Option<Epoch>, Option<LeaseOwner>)> {
    if opts.read_only {
        return Ok((None, None));
    }
    let Some(tid) = thread_id else {
        return Ok((None, None));
    };

    let human_owner = make_human_owner(client.client_id());
    let status_res = client
        .call::<_, LeaseResult>(
            rpc_methods::LEASE_STATUS,
            LeaseStatusParams {
                thread_id: tid.clone(),
            },
        )
        .await;

    let lease_result = match status_res {
        Ok(status) => match status.lease.state {
            LeaseState::Unowned => client
                .call::<_, LeaseResult>(
                    rpc_methods::LEASE_CLAIM,
                    LeaseClaimParams {
                        thread_id: tid.clone(),
                        owner: human_owner.clone(),
                        expected_epoch: Some(status.lease.epoch),
                        ttl_ms: None,
                        reason: Some(HandoffReason::UserAttach),
                    },
                )
                .await
                .context("failed to claim human lease")?,
            LeaseState::Owned
            | LeaseState::Draining
            | LeaseState::Cancelling
            | LeaseState::StoppingAndFlushing => {
                if opts.takeover {
                    let force = matches!(status.lease.state, LeaseState::Draining);
                    client
                        .call::<_, LeaseResult>(
                            rpc_methods::LEASE_HANDOFF,
                            LeaseHandoffParams {
                                thread_id: tid.clone(),
                                from_epoch: status.lease.epoch,
                                to_owner: human_owner.clone(),
                                reason: HandoffReason::UserAttach,
                                from_owner: status.lease.owner,
                                ttl_ms: None,
                                cancel_active_turn: force.then_some(true),
                            },
                        )
                        .await
                        .context("failed to take over thread lease")?
                } else {
                    let owner_desc = status
                        .lease
                        .owner
                        .as_ref()
                        .map(format_lease_owner)
                        .unwrap_or_else(|| format_lease_state(status.lease.state).to_owned());
                    bail!(
                        "thread {} is currently leased by {} (epoch {}); pass --takeover to claim control or --read-only to observe",
                        tid,
                        owner_desc,
                        status.lease.epoch.0
                    );
                }
            }
        },
        Err(err)
            if err
                .downcast_ref::<RpcRemoteError>()
                .is_some_and(|e| e.error.code == RpcErrorCode::NotFound.code()) =>
        {
            client
                .call::<_, LeaseResult>(
                    rpc_methods::LEASE_CLAIM,
                    LeaseClaimParams {
                        thread_id: tid.clone(),
                        owner: human_owner.clone(),
                        expected_epoch: None,
                        ttl_ms: None,
                        reason: Some(HandoffReason::UserAttach),
                    },
                )
                .await
                .context("failed to claim human lease")?
        }
        Err(err) => return Err(err).context("failed to query lease status"),
    };

    let owner = lease_result.lease.owner.or(Some(human_owner));
    Ok((Some(lease_result.lease.epoch), owner))
}

async fn send_pty_input(
    client: &RpcClient,
    pty_id: &PtyId,
    epoch: Option<Epoch>,
    bytes: &[u8],
) -> Result<()> {
    let data_b64 = BASE64_STANDARD.encode(bytes);
    client
        .notify(
            rpc_methods::PTY_INPUT,
            PtyInputParams {
                pty_id: pty_id.clone(),
                epoch,
                encoding: PtyEncoding::Base64,
                data_b64,
            },
        )
        .await
}

fn spawn_stdin_reader() -> mpsc::UnboundedReceiver<std::io::Result<Vec<u8>>> {
    let (tx, rx) = mpsc::unbounded_channel();
    tokio::spawn(async move {
        let mut stdin = tokio::io::stdin();
        let mut buf = [0u8; 4096];
        loop {
            match stdin.read(&mut buf).await {
                Ok(0) => break,
                Ok(n) => {
                    if tx.send(Ok(buf[..n].to_vec())).is_err() {
                        break;
                    }
                }
                Err(err) => {
                    let _ = tx.send(Err(err));
                    break;
                }
            }
        }
    });
    rx
}

fn make_human_owner(client_id: &str) -> LeaseOwner {
    LeaseOwner {
        kind: LeaseOwnerKind::Human,
        id: default_human_owner_id(),
        client_id: Some(client_id.to_owned()),
        label: Some("spaces attach".to_owned()),
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use serde_json::json;
    use spaces_types::rpc_core::{RpcAuthResult, RpcNotification, RpcRequest, RpcResponse};
    use spaces_types::{
        BootId, Lease, LeaseId, Seq, ThreadDriver, ThreadKind, ThreadState, TimestampMs,
    };
    use tokio::io::{AsyncBufReadExt, BufReader};
    use tokio::net::UnixListener;

    use super::*;
    use crate::endpoint::DaemonEndpoint;

    fn temp_dir(prefix: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "spaces-cli-attach-{prefix}-{}",
            uuid::Uuid::new_v4()
        ));
        std::fs::create_dir_all(&dir).expect("create temp dir");
        dir
    }

    fn make_thread(id: &str, pty_id: Option<&str>) -> Thread {
        Thread {
            id: ThreadId::from(id),
            space: None,
            window_id: None,
            pane_id: None,
            pty_id: pty_id.map(PtyId::from),
            agent: "claude".to_owned(),
            profile: None,
            driver: ThreadDriver::NativePty,
            kind: ThreadKind::Interactive,
            state: ThreadState::Active,
            cwd: "/tmp".to_owned(),
            canonical_cwd: "/tmp".to_owned(),
            worktree_path: None,
            vendor_session_id: None,
            title: None,
            model: None,
            epoch: Epoch(0),
            active_turn_id: None,
            created_at_ms: TimestampMs(100),
            updated_at_ms: TimestampMs(100),
        }
    }

    #[tokio::test]
    async fn attach_claims_streams_sends_input_and_yields_on_ctrl_rbracket_d() {
        let dir = temp_dir("detach");
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).expect("bind socket");

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.expect("accept");
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();
            let mut seen_methods = Vec::new();
            let mut saw_pty_input = false;
            let mut saw_yield = false;

            while reader.read_line(&mut line).await.unwrap() > 0 {
                let frame: Value = serde_json::from_str(line.trim()).unwrap();
                let method = frame["method"].as_str().unwrap().to_owned();
                seen_methods.push(method.clone());

                if let Some(id) = frame.get("id").cloned() {
                    let result = match method.as_str() {
                        rpc_methods::AUTH => serde_json::to_value(RpcAuthResult {
                            ok: true,
                            protocol_version: 1,
                            boot_id: BootId::from("boot_1"),
                            client_id: "cli-test".to_owned(),
                            current_seq: Seq(0),
                        })
                        .unwrap(),
                        rpc_methods::THREAD_GET => {
                            json!({ "thread": make_thread("thr_1", Some("pty_1")) })
                        }
                        rpc_methods::LEASE_STATUS => serde_json::to_value(LeaseResult {
                            lease: Lease {
                                id: LeaseId::from("lease_1"),
                                thread_id: ThreadId::from("thr_1"),
                                epoch: Epoch(0),
                                state: LeaseState::Unowned,
                                owner: None,
                                acquired_at_ms: None,
                                expires_at_ms: None,
                                updated_at_ms: TimestampMs(100),
                            },
                            transition: None,
                        })
                        .unwrap(),
                        rpc_methods::LEASE_CLAIM => {
                            let owner: LeaseOwner =
                                serde_json::from_value(frame["params"]["owner"].clone()).unwrap();
                            assert_eq!(owner.kind, LeaseOwnerKind::Human);
                            serde_json::to_value(LeaseResult {
                                lease: Lease {
                                    id: LeaseId::from("lease_1"),
                                    thread_id: ThreadId::from("thr_1"),
                                    epoch: Epoch(1),
                                    state: LeaseState::Owned,
                                    owner: Some(owner),
                                    acquired_at_ms: Some(TimestampMs(101)),
                                    expires_at_ms: None,
                                    updated_at_ms: TimestampMs(101),
                                },
                                transition: None,
                            })
                            .unwrap()
                        }
                        rpc_methods::PTY_ATTACH => {
                            let resp = RpcResponse::ok(
                                serde_json::from_value::<spaces_types::rpc_core::RpcId>(id)
                                    .unwrap(),
                                serde_json::to_value(PtyAttachResult {
                                    pty_id: PtyId::from("pty_1"),
                                    current_offset: 5,
                                    replay_truncated: false,
                                })
                                .unwrap(),
                            );
                            let mut raw = serde_json::to_string(&resp).unwrap();
                            raw.push('\n');
                            write_half.write_all(raw.as_bytes()).await.unwrap();

                            // Emit output chunk after attach
                            let out = RpcNotification::new(
                                rpc_methods::PTY_OUTPUT,
                                Some(
                                    serde_json::to_value(PtyOutputNotification {
                                        pty_id: PtyId::from("pty_1"),
                                        offset: 5,
                                        encoding: PtyEncoding::Base64,
                                        data_b64: BASE64_STANDARD.encode(b"hello "),
                                        ts_ms: TimestampMs(102),
                                    })
                                    .unwrap(),
                                ),
                            );
                            let mut out_raw = serde_json::to_string(&out).unwrap();
                            out_raw.push('\n');
                            write_half.write_all(out_raw.as_bytes()).await.unwrap();
                            line.clear();
                            continue;
                        }
                        rpc_methods::PTY_DETACH => json!({"ok": true}),
                        rpc_methods::LEASE_YIELD => {
                            saw_yield = true;
                            assert_eq!(frame["params"]["epoch"], 1);
                            assert_eq!(frame["params"]["reason"], "user_yield");
                            serde_json::to_value(LeaseResult {
                                lease: Lease {
                                    id: LeaseId::from("lease_1"),
                                    thread_id: ThreadId::from("thr_1"),
                                    epoch: Epoch(2),
                                    state: LeaseState::Unowned,
                                    owner: None,
                                    acquired_at_ms: None,
                                    expires_at_ms: None,
                                    updated_at_ms: TimestampMs(105),
                                },
                                transition: None,
                            })
                            .unwrap()
                        }
                        other => panic!("unexpected RPC call: {other}"),
                    };

                    let resp = RpcResponse::ok(
                        serde_json::from_value::<spaces_types::rpc_core::RpcId>(id).unwrap(),
                        result,
                    );
                    let mut raw = serde_json::to_string(&resp).unwrap();
                    raw.push('\n');
                    write_half.write_all(raw.as_bytes()).await.unwrap();
                    if saw_yield {
                        break;
                    }
                } else if method == rpc_methods::PTY_INPUT {
                    saw_pty_input = true;
                    assert_eq!(frame["params"]["epoch"], 1);
                    assert_eq!(
                        BASE64_STANDARD
                            .decode(frame["params"]["data_b64"].as_str().unwrap())
                            .unwrap(),
                        b"ping"
                    );
                }
                line.clear();
            }

            assert!(saw_pty_input);
            assert!(saw_yield);
            assert!(seen_methods.contains(&rpc_methods::PTY_DETACH.to_owned()));
        });

        let endpoint = DaemonEndpoint {
            version: 1,
            protocol: "ndjson-rpc/uds".to_owned(),
            pid: 1,
            uid: 1000,
            socket: sock_path.to_string_lossy().into_owned(),
            token: "tok".to_owned(),
            boot_id: BootId::from("boot_1"),
            started_ms: TimestampMs(1),
        };
        let client = RpcClient::connect(&endpoint).await.expect("connect");
        let (thread_id, pty_id) = resolve_target(&client, "thr_1").await.expect("resolve");
        assert_eq!(thread_id, Some(ThreadId::from("thr_1")));
        assert_eq!(pty_id, PtyId::from("pty_1"));

        let (stdin_tx, stdin_rx) = mpsc::unbounded_channel();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(20)).await;
            stdin_tx.send(Ok(b"ping".to_vec())).unwrap();
            tokio::time::sleep(Duration::from_millis(20)).await;
            stdin_tx.send(Ok(vec![0x1d])).unwrap();
            stdin_tx.send(Ok(b"d".to_vec())).unwrap();
        });

        let mut captured_output = Vec::new();
        attach_with_io(
            client,
            thread_id,
            pty_id,
            AttachOptions {
                takeover: false,
                read_only: false,
                no_replay: false,
            },
            -1,
            false,
            stdin_rx,
            &mut captured_output,
        )
        .await
        .expect("attach succeeds");

        assert_eq!(captured_output, b"hello ");
        server.await.expect("mock server");
        std::fs::remove_dir_all(&dir).ok();
    }

    #[tokio::test]
    async fn attach_requires_takeover_or_read_only_when_owned_by_other() {
        let dir = temp_dir("conflict");
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).expect("bind socket");

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.expect("accept");
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();

            while reader.read_line(&mut line).await.unwrap() > 0 {
                let req: RpcRequest = serde_json::from_str(line.trim()).unwrap();
                let result = match req.method.as_str() {
                    rpc_methods::AUTH => serde_json::to_value(RpcAuthResult {
                        ok: true,
                        protocol_version: 1,
                        boot_id: BootId::from("boot_1"),
                        client_id: "cli-test".to_owned(),
                        current_seq: Seq(0),
                    })
                    .unwrap(),
                    rpc_methods::THREAD_GET => {
                        json!({ "thread": make_thread("thr_busy", Some("pty_busy")) })
                    }
                    rpc_methods::LEASE_STATUS => serde_json::to_value(LeaseResult {
                        lease: Lease {
                            id: LeaseId::from("lease_busy"),
                            thread_id: ThreadId::from("thr_busy"),
                            epoch: Epoch(4),
                            state: LeaseState::Owned,
                            owner: Some(LeaseOwner {
                                kind: LeaseOwnerKind::Agent,
                                id: "coordinator".to_owned(),
                                client_id: None,
                                label: None,
                            }),
                            acquired_at_ms: Some(TimestampMs(100)),
                            expires_at_ms: None,
                            updated_at_ms: TimestampMs(100),
                        },
                        transition: None,
                    })
                    .unwrap(),
                    other => panic!("unexpected call: {other}"),
                };
                let mut raw = serde_json::to_string(&RpcResponse::ok(req.id, result)).unwrap();
                raw.push('\n');
                write_half.write_all(raw.as_bytes()).await.unwrap();
                if req.method == rpc_methods::LEASE_STATUS {
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
            boot_id: BootId::from("boot_1"),
            started_ms: TimestampMs(1),
        };
        let client = RpcClient::connect(&endpoint).await.expect("connect");
        let err = run(
            client,
            AttachArgs {
                target: "thr_busy".to_owned(),
                takeover: false,
                read_only: false,
                no_replay: false,
            },
        )
        .await
        .expect_err("should fail without takeover/read-only");

        let msg = err.to_string();
        assert!(msg.contains("--takeover"), "unexpected error: {msg}");
        assert!(msg.contains("--read-only"), "unexpected error: {msg}");
        assert!(msg.contains("agent:coordinator"), "unexpected error: {msg}");

        server.await.expect("server");
        std::fs::remove_dir_all(&dir).ok();
    }

    #[tokio::test]
    async fn attach_takeover_and_pty_exit_exits_cleanly() {
        let dir = temp_dir("takeover-exit");
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).expect("bind socket");

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.expect("accept");
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();
            let mut handoff_seen = false;

            while reader.read_line(&mut line).await.unwrap() > 0 {
                let frame: Value = serde_json::from_str(line.trim()).unwrap();
                let method = frame["method"].as_str().unwrap().to_owned();
                if let Some(id) = frame.get("id").cloned() {
                    let rpc_id: spaces_types::rpc_core::RpcId = serde_json::from_value(id).unwrap();
                    let result = match method.as_str() {
                        rpc_methods::AUTH => serde_json::to_value(RpcAuthResult {
                            ok: true,
                            protocol_version: 1,
                            boot_id: BootId::from("boot_1"),
                            client_id: "cli-test".to_owned(),
                            current_seq: Seq(0),
                        })
                        .unwrap(),
                        rpc_methods::THREAD_LIST => serde_json::to_value(ThreadListResult {
                            threads: vec![make_thread("thr_take", Some("pty_take"))],
                        })
                        .unwrap(),
                        rpc_methods::LEASE_STATUS => serde_json::to_value(LeaseResult {
                            lease: Lease {
                                id: LeaseId::from("lease_take"),
                                thread_id: ThreadId::from("thr_take"),
                                epoch: Epoch(2),
                                state: LeaseState::Owned,
                                owner: Some(LeaseOwner {
                                    kind: LeaseOwnerKind::Agent,
                                    id: "coordinator".to_owned(),
                                    client_id: None,
                                    label: None,
                                }),
                                acquired_at_ms: Some(TimestampMs(100)),
                                expires_at_ms: None,
                                updated_at_ms: TimestampMs(100),
                            },
                            transition: None,
                        })
                        .unwrap(),
                        rpc_methods::LEASE_HANDOFF => {
                            handoff_seen = true;
                            assert_eq!(frame["params"]["from_epoch"], 2);
                            assert_eq!(frame["params"]["reason"], "user_attach");
                            let to_owner: LeaseOwner =
                                serde_json::from_value(frame["params"]["to_owner"].clone())
                                    .unwrap();
                            assert_eq!(to_owner.kind, LeaseOwnerKind::Human);
                            serde_json::to_value(LeaseResult {
                                lease: Lease {
                                    id: LeaseId::from("lease_take"),
                                    thread_id: ThreadId::from("thr_take"),
                                    epoch: Epoch(3),
                                    state: LeaseState::Owned,
                                    owner: Some(to_owner),
                                    acquired_at_ms: Some(TimestampMs(110)),
                                    expires_at_ms: None,
                                    updated_at_ms: TimestampMs(110),
                                },
                                transition: None,
                            })
                            .unwrap()
                        }
                        rpc_methods::PTY_ATTACH => {
                            let resp = RpcResponse::ok(
                                rpc_id,
                                serde_json::to_value(PtyAttachResult {
                                    pty_id: PtyId::from("pty_take"),
                                    current_offset: 0,
                                    replay_truncated: false,
                                })
                                .unwrap(),
                            );
                            let mut raw = serde_json::to_string(&resp).unwrap();
                            raw.push('\n');
                            write_half.write_all(raw.as_bytes()).await.unwrap();

                            let exit = RpcNotification::new(
                                rpc_methods::PTY_EXIT,
                                Some(
                                    serde_json::to_value(PtyExitNotification {
                                        pty_id: PtyId::from("pty_take"),
                                        exit_code: Some(0),
                                        signal: None,
                                        ts_ms: TimestampMs(120),
                                    })
                                    .unwrap(),
                                ),
                            );
                            let mut exit_raw = serde_json::to_string(&exit).unwrap();
                            exit_raw.push('\n');
                            write_half.write_all(exit_raw.as_bytes()).await.unwrap();
                            line.clear();
                            continue;
                        }
                        rpc_methods::PTY_DETACH => {
                            let resp = RpcResponse::ok(rpc_id, json!({"ok": true}));
                            let mut raw = serde_json::to_string(&resp).unwrap();
                            raw.push('\n');
                            write_half.write_all(raw.as_bytes()).await.unwrap();
                            break;
                        }
                        other => panic!("unexpected call: {other}"),
                    };

                    let mut raw = serde_json::to_string(&RpcResponse::ok(rpc_id, result)).unwrap();
                    raw.push('\n');
                    write_half.write_all(raw.as_bytes()).await.unwrap();
                }
                line.clear();
            }
            assert!(handoff_seen);
        });

        let endpoint = DaemonEndpoint {
            version: 1,
            protocol: "ndjson-rpc/uds".to_owned(),
            pid: 1,
            uid: 1000,
            socket: sock_path.to_string_lossy().into_owned(),
            token: "tok".to_owned(),
            boot_id: BootId::from("boot_1"),
            started_ms: TimestampMs(1),
        };
        let client = RpcClient::connect(&endpoint).await.expect("connect");
        let (thread_id, pty_id) = resolve_target(&client, "pty_take").await.expect("resolve");
        assert_eq!(thread_id, Some(ThreadId::from("thr_take")));
        assert_eq!(pty_id, PtyId::from("pty_take"));

        let (_tx, stdin_rx) = mpsc::unbounded_channel();
        let mut out = Vec::new();
        attach_with_io(
            client,
            thread_id,
            pty_id,
            AttachOptions {
                takeover: true,
                read_only: false,
                no_replay: true,
            },
            -1,
            false,
            stdin_rx,
            &mut out,
        )
        .await
        .expect("attach takeover + exit succeeds");

        server.await.expect("server");
        std::fs::remove_dir_all(&dir).ok();
    }
}

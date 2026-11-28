use std::collections::HashMap;
use std::future::Future;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use anyhow::{Context, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use serde::Deserialize;
use serde_json::{Value, json};
use spaces_types::{
    PROTOCOL_VERSION, PtyAttachParams, PtyDetachParams, PtyId, PtyInputParams, PtyKillParams,
    PtyResizeParams, PtySpawnParams, PtySpawnResult, RpcAuthParams, RpcAuthResult, RpcClientKind,
    RpcError, RpcErrorCode, RpcFrame, RpcId, RpcRequest, RpcResponse, SPACES_CONFIG_DIR_NAME,
    rpc_methods,
};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;
use tokio::net::unix::{OwnedReadHalf, OwnedWriteHalf};
use tokio::sync::mpsc;
use uuid::Uuid;

use crate::pty::parse_signal;
use crate::session::PtySessionHandle;

const EXITED_SESSION_RETENTION: Duration = Duration::from_secs(60);
const SHUTDOWN_TERM_TIMEOUT: Duration = Duration::from_millis(500);

/// Endpoint metadata written by `spacesd` at `<spaces_dir>/daemon.endpoint.json`.
#[derive(Debug, Clone, Deserialize)]
pub struct DaemonEndpoint {
    pub socket: String,
    pub token: String,
    #[serde(default)]
    pub version: Option<u32>,
    #[serde(default)]
    pub protocol: Option<String>,
    #[serde(default)]
    pub boot_id: Option<String>,
}

/// Resolves `<spaces_dir | SPACES_DIR | $HOME/.spaces>`.
pub fn resolve_spaces_dir(spaces_dir: Option<PathBuf>) -> anyhow::Result<PathBuf> {
    if let Some(dir) = spaces_dir {
        return Ok(dir);
    }
    if let Some(env_dir) = std::env::var_os("SPACES_DIR") {
        let s = env_dir.to_string_lossy();
        let trimmed = s.trim();
        if !trimmed.is_empty() {
            return Ok(PathBuf::from(trimmed));
        }
    }
    let home = std::env::var_os("HOME")
        .filter(|v| !v.is_empty())
        .context("neither spaces_dir, SPACES_DIR, nor HOME is set")?;
    Ok(PathBuf::from(home).join(SPACES_CONFIG_DIR_NAME))
}

/// Reads and validates `daemon.endpoint.json` from `spaces_dir`.
pub fn read_daemon_endpoint(spaces_dir: &Path) -> anyhow::Result<DaemonEndpoint> {
    let endpoint_path = spaces_dir.join("daemon.endpoint.json");
    let raw = std::fs::read_to_string(&endpoint_path)
        .with_context(|| format!("failed to read {}", endpoint_path.display()))?;
    let endpoint: DaemonEndpoint = serde_json::from_str(&raw)
        .with_context(|| format!("failed to parse {}", endpoint_path.display()))?;
    if endpoint.socket.trim().is_empty() {
        bail!(
            "daemon endpoint socket path is empty in {}",
            endpoint_path.display()
        );
    }
    Ok(endpoint)
}

/// Connects to `spacesd` over UDS, authenticates as `RpcClientKind::PtyHost`,
/// serves `pty.*` requests until disconnect or `SIGINT`/`SIGTERM`, and cleanly
/// terminates all active child process groups on shutdown.
pub async fn run_host(spaces_dir: Option<PathBuf>) -> anyhow::Result<()> {
    let dir = resolve_spaces_dir(spaces_dir)?;
    let endpoint = read_daemon_endpoint(&dir)?;
    let stream = UnixStream::connect(&endpoint.socket)
        .await
        .with_context(|| format!("failed to connect to spacesd socket {}", endpoint.socket))?;

    run_host_on_stream(stream, &endpoint.token, shutdown_signal()).await
}

/// Connects on an already-established `UnixStream`, authenticates with `token`,
/// and runs the PTY host loop until `shutdown` resolves or the socket closes.
pub async fn run_host_on_stream<F>(
    stream: UnixStream,
    token: &str,
    shutdown: F,
) -> anyhow::Result<()>
where
    F: Future<Output = ()>,
{
    let (read_half, write_half) = stream.into_split();
    let (mut reader, mut writer) = authenticate(read_half, write_half, token).await?;

    let (out_tx, mut out_rx) = mpsc::unbounded_channel::<RpcFrame>();
    let writer_task = tokio::spawn(async move {
        let mut write_buf = String::new();
        while let Some(frame) = out_rx.recv().await {
            write_buf.clear();
            if serialize_frame_into(&frame, &mut write_buf).is_err() {
                continue;
            }
            while let Ok(next) = out_rx.try_recv() {
                let _ = serialize_frame_into(&next, &mut write_buf);
            }
            if writer.write_all(write_buf.as_bytes()).await.is_err() {
                break;
            }
            if writer.flush().await.is_err() {
                break;
            }
        }
    });

    let (exit_tx, mut exit_rx) = mpsc::unbounded_channel::<PtyId>();
    let mut sessions: HashMap<PtyId, PtySessionHandle> = HashMap::new();
    let mut line = String::new();

    tokio::pin!(shutdown);

    loop {
        line.clear();
        tokio::select! {
            _ = &mut shutdown => {
                tracing::info!("spaces-pty received shutdown signal");
                break;
            }
            Some(exited_id) = exit_rx.recv() => {
                if let Some(session) = sessions.get_mut(&exited_id) {
                    session.mark_exited_now();
                }
                prune_expired_sessions(&mut sessions);
            }
            read_res = reader.read_line(&mut line) => {
                match read_res {
                    Ok(0) => {
                        tracing::info!("spacesd UDS closed; shutting down PTY host");
                        break;
                    }
                    Ok(_) => {
                        let trimmed = line.trim();
                        if trimmed.is_empty() {
                            continue;
                        }
                        handle_incoming_line(
                            trimmed,
                            &mut sessions,
                            &out_tx,
                            &exit_tx,
                        )
                        .await;
                        prune_expired_sessions(&mut sessions);
                    }
                    Err(err) => {
                        tracing::warn!(error = %err, "error reading from spacesd UDS");
                        break;
                    }
                }
            }
        }
    }

    shutdown_sessions(&mut sessions).await;
    drop(out_tx);
    let _ = tokio::time::timeout(Duration::from_secs(1), writer_task).await;
    Ok(())
}

async fn authenticate(
    read_half: OwnedReadHalf,
    mut write_half: OwnedWriteHalf,
    token: &str,
) -> anyhow::Result<(BufReader<OwnedReadHalf>, OwnedWriteHalf)> {
    let auth_id = RpcId::String(format!("auth_{}", Uuid::new_v4()));
    let auth_params = RpcAuthParams {
        protocol_version: PROTOCOL_VERSION,
        client_kind: RpcClientKind::PtyHost,
        client_id: Some(format!("pty-host-{}", std::process::id())),
        token: Some(token.to_owned()),
        label: Some("spaces-pty".to_owned()),
    };
    let req = RpcRequest::new(
        auth_id.clone(),
        rpc_methods::AUTH,
        Some(serde_json::to_value(&auth_params)?),
    );
    let mut encoded = serde_json::to_string(&req)?;
    encoded.push('\n');
    write_half
        .write_all(encoded.as_bytes())
        .await
        .context("failed to write rpc.auth request")?;
    write_half
        .flush()
        .await
        .context("failed to flush rpc.auth request")?;

    let mut reader = BufReader::new(read_half);
    let mut line = String::new();
    loop {
        line.clear();
        let n = reader
            .read_line(&mut line)
            .await
            .context("failed to read rpc.auth response")?;
        if n == 0 {
            bail!("spacesd closed UDS before replying to rpc.auth");
        }
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        let resp: RpcResponse = serde_json::from_str(trimmed)
            .with_context(|| format!("invalid rpc.auth response JSON: {trimmed}"))?;
        if resp.id != auth_id {
            continue;
        }
        if let Some(err) = resp.error {
            bail!("rpc.auth rejected ({}): {}", err.code, err.message);
        }
        let result_val = resp.result.context("rpc.auth response missing result")?;
        let auth_res: RpcAuthResult =
            serde_json::from_value(result_val).context("invalid RpcAuthResult payload")?;
        if !auth_res.ok {
            bail!("rpc.auth returned ok=false");
        }
        return Ok((reader, write_half));
    }
}

async fn handle_incoming_line(
    line: &str,
    sessions: &mut HashMap<PtyId, PtySessionHandle>,
    out_tx: &mpsc::UnboundedSender<RpcFrame>,
    exit_tx: &mpsc::UnboundedSender<PtyId>,
) {
    let val: Value = match serde_json::from_str(line) {
        Ok(v) => v,
        Err(err) => {
            let _ = out_tx.send(RpcFrame::Response(RpcResponse::err(
                0_i64,
                RpcError::new(RpcErrorCode::ParseError, format!("Invalid JSON: {err}")),
            )));
            return;
        }
    };

    let Some(obj) = val.as_object() else {
        let _ = out_tx.send(RpcFrame::Response(RpcResponse::err(
            0_i64,
            RpcError::new(
                RpcErrorCode::InvalidRequest,
                "JSON-RPC frame must be an object",
            ),
        )));
        return;
    };

    if obj.get("jsonrpc").and_then(Value::as_str) != Some(spaces_types::JSONRPC_VERSION) {
        let id = obj
            .get("id")
            .cloned()
            .and_then(|v| serde_json::from_value::<RpcId>(v).ok())
            .unwrap_or(RpcId::Number(0));
        let _ = out_tx.send(RpcFrame::Response(RpcResponse::err(
            id,
            RpcError::new(RpcErrorCode::InvalidRequest, "Expected jsonrpc \"2.0\""),
        )));
        return;
    }

    // Ignore incoming responses or notifications without `method`.
    let Some(method_val) = obj.get("method") else {
        return;
    };
    let Some(method) = method_val.as_str() else {
        let id = obj
            .get("id")
            .cloned()
            .and_then(|v| serde_json::from_value::<RpcId>(v).ok())
            .unwrap_or(RpcId::Number(0));
        let _ = out_tx.send(RpcFrame::Response(RpcResponse::err(
            id,
            RpcError::new(RpcErrorCode::InvalidRequest, "RPC method must be a string"),
        )));
        return;
    };

    // Notifications have no `id`; do not emit responses for notifications.
    let Some(id_val) = obj.get("id").filter(|v| !v.is_null()) else {
        tracing::debug!(method, "ignoring incoming JSON-RPC notification");
        return;
    };

    let id: RpcId = match serde_json::from_value(id_val.clone()) {
        Ok(id) => id,
        Err(_) => {
            let _ = out_tx.send(RpcFrame::Response(RpcResponse::err(
                0_i64,
                RpcError::new(
                    RpcErrorCode::InvalidRequest,
                    "RPC id must be a string or integer",
                ),
            )));
            return;
        }
    };

    let params = obj.get("params").cloned();
    if let Some(resp) = dispatch_request(id, method, params, sessions, out_tx, exit_tx).await {
        let _ = out_tx.send(RpcFrame::Response(resp));
    }
}

async fn dispatch_request(
    id: RpcId,
    method: &str,
    params: Option<Value>,
    sessions: &mut HashMap<PtyId, PtySessionHandle>,
    out_tx: &mpsc::UnboundedSender<RpcFrame>,
    exit_tx: &mpsc::UnboundedSender<PtyId>,
) -> Option<RpcResponse> {
    match method {
        rpc_methods::PTY_SPAWN => {
            let mut p: PtySpawnParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            if p.argv.is_empty() {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::InvalidParams,
                        "pty.spawn requires non-empty argv",
                    ),
                ));
            }
            let pty_id = p
                .env
                .get("SPACES_PTY_ID")
                .map(|s| s.trim())
                .filter(|s| !s.is_empty())
                .map(PtyId::from)
                .unwrap_or_else(|| PtyId::from(format!("pty_{}", Uuid::new_v4())));

            if let Some(existing) = sessions.get(&pty_id)
                && !existing.is_exited()
            {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::InvalidParams,
                        format!("PTY session already active: {pty_id}"),
                    ),
                ));
            }

            p.env
                .entry("SPACES_PTY_ID".to_owned())
                .or_insert_with(|| pty_id.0.clone());
            p.env
                .entry("SPACES_THREAD_ID".to_owned())
                .or_insert_with(|| p.thread_id.0.clone());

            match PtySessionHandle::spawn(pty_id.clone(), p, out_tx.clone(), Some(exit_tx.clone()))
            {
                Ok(session) => {
                    let result = PtySpawnResult {
                        pty_id: pty_id.clone(),
                        pid: session.pid(),
                        pgid: session.pgid(),
                    };
                    sessions.insert(pty_id, session);
                    Some(RpcResponse::ok(
                        id,
                        serde_json::to_value(&result).expect("PtySpawnResult serializes"),
                    ))
                }
                Err(err) => Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InternalError, err.to_string()),
                )),
            }
        }
        rpc_methods::PTY_ATTACH => {
            let p: PtyAttachParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            let Some(session) = sessions.get(&p.pty_id) else {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::NotFound,
                        format!("PTY session not found: {}", p.pty_id),
                    ),
                ));
            };
            if let Err(err) = session.attach(id.clone(), &p, out_tx) {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InternalError, err.to_string()),
                ));
            }
            None
        }
        rpc_methods::PTY_DETACH => {
            let _p: PtyDetachParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            Some(RpcResponse::ok(id, json!({ "ok": true })))
        }
        rpc_methods::PTY_INPUT => {
            let p: PtyInputParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            let Some(session) = sessions.get(&p.pty_id) else {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::NotFound,
                        format!("PTY session not found: {}", p.pty_id),
                    ),
                ));
            };
            let decoded = match STANDARD.decode(&p.data_b64) {
                Ok(bytes) => bytes,
                Err(err) => {
                    return Some(RpcResponse::err(
                        id,
                        RpcError::new(
                            RpcErrorCode::InvalidParams,
                            format!("invalid base64 data_b64: {err}"),
                        ),
                    ));
                }
            };
            match session.write_input(decoded).await {
                Ok(()) => Some(RpcResponse::ok(id, json!({ "ok": true }))),
                Err(err) => Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InternalError, err.to_string()),
                )),
            }
        }
        rpc_methods::PTY_RESIZE => {
            let p: PtyResizeParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            let Some(session) = sessions.get(&p.pty_id) else {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::NotFound,
                        format!("PTY session not found: {}", p.pty_id),
                    ),
                ));
            };
            match session.resize(p.size) {
                Ok(()) => Some(RpcResponse::ok(id, json!({ "ok": true }))),
                Err(err) => Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InternalError, err.to_string()),
                )),
            }
        }
        rpc_methods::PTY_KILL => {
            let p: PtyKillParams = match parse_params(params) {
                Ok(v) => v,
                Err(err) => return Some(RpcResponse::err(id, err)),
            };
            if let Err(err) = parse_signal(p.signal.as_deref()) {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InvalidParams, err.to_string()),
                ));
            }
            let Some(session) = sessions.get(&p.pty_id) else {
                return Some(RpcResponse::err(
                    id,
                    RpcError::new(
                        RpcErrorCode::NotFound,
                        format!("PTY session not found: {}", p.pty_id),
                    ),
                ));
            };
            match session.kill(p.signal.as_deref()) {
                Ok(()) => Some(RpcResponse::ok(id, json!({ "ok": true }))),
                Err(err) => Some(RpcResponse::err(
                    id,
                    RpcError::new(RpcErrorCode::InternalError, err.to_string()),
                )),
            }
        }
        other => Some(RpcResponse::err(
            id,
            RpcError::new(
                RpcErrorCode::MethodNotFound,
                format!("Method not found: {other}"),
            ),
        )),
    }
}

fn parse_params<T: serde::de::DeserializeOwned>(params: Option<Value>) -> Result<T, RpcError> {
    let val =
        params.ok_or_else(|| RpcError::new(RpcErrorCode::InvalidParams, "params required"))?;
    serde_json::from_value(val).map_err(|err| {
        RpcError::new(
            RpcErrorCode::InvalidParams,
            format!("invalid params: {err}"),
        )
    })
}

fn serialize_frame_into(frame: &RpcFrame, out: &mut String) -> anyhow::Result<()> {
    let line = serde_json::to_string(frame)?;
    out.push_str(&line);
    out.push('\n');
    Ok(())
}

fn prune_expired_sessions(sessions: &mut HashMap<PtyId, PtySessionHandle>) {
    let now = Instant::now();
    sessions.retain(|_, session| match session.exited_at() {
        Some(ts) => now.saturating_duration_since(ts) < EXITED_SESSION_RETENTION,
        None => true,
    });
}

async fn shutdown_sessions(sessions: &mut HashMap<PtyId, PtySessionHandle>) {
    for session in sessions.values() {
        if !session.is_exited() {
            let _ = session.kill(Some("SIGTERM"));
        }
    }
    for session in sessions.values() {
        if !session.is_exited() && !session.wait_exited(SHUTDOWN_TERM_TIMEOUT).await {
            let _ = session.kill(Some("SIGKILL"));
            let _ = session.wait_exited(SHUTDOWN_TERM_TIMEOUT).await;
        }
    }
    sessions.clear();
}

async fn shutdown_signal() {
    use tokio::signal::unix::{SignalKind, signal};

    let mut sigint = signal(SignalKind::interrupt()).ok();
    let mut sigterm = signal(SignalKind::terminate()).ok();

    tokio::select! {
        _ = async {
            if let Some(ref mut s) = sigint {
                s.recv().await;
            } else {
                std::future::pending::<()>().await;
            }
        } => {}
        _ = async {
            if let Some(ref mut s) = sigterm {
                s.recv().await;
            } else {
                std::future::pending::<()>().await;
            }
        } => {}
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use spaces_types::{
        OscState, PtyAttachResult, PtyExitNotification, PtyOscNotification, PtyOutputNotification,
        PtySize, ThreadId,
    };
    use tokio::net::UnixListener;
    use tokio::sync::Notify;

    use super::*;

    struct MockDaemonConn {
        reader: BufReader<OwnedReadHalf>,
        writer: OwnedWriteHalf,
    }

    impl MockDaemonConn {
        async fn send_line(&mut self, value: &impl serde::Serialize) {
            let mut s = serde_json::to_string(value).unwrap();
            s.push('\n');
            self.writer.write_all(s.as_bytes()).await.unwrap();
            self.writer.flush().await.unwrap();
        }

        async fn next_frame(&mut self) -> RpcFrame {
            let mut line = String::new();
            loop {
                line.clear();
                let n = self.reader.read_line(&mut line).await.unwrap();
                assert!(n > 0, "unexpected EOF from pty host");
                let trimmed = line.trim();
                if !trimmed.is_empty() {
                    return serde_json::from_str(trimmed).unwrap();
                }
            }
        }
    }

    #[tokio::test]
    async fn host_handles_auth_spawn_attach_input_resize_kill_and_shutdown() {
        let tmp = std::env::temp_dir().join(format!("spaces-pty-host-test-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&tmp).unwrap();
        let sock_path = tmp.join("daemon.sock");
        let token = "test-secret-token";
        let endpoint_json = json!({
            "version": PROTOCOL_VERSION,
            "protocol": "ndjson-rpc/uds",
            "socket": sock_path.to_string_lossy(),
            "token": token,
        });
        std::fs::write(
            tmp.join("daemon.endpoint.json"),
            serde_json::to_string(&endpoint_json).unwrap(),
        )
        .unwrap();

        let listener = UnixListener::bind(&sock_path).unwrap();
        let dir_clone = tmp.clone();
        let host_task = tokio::spawn(async move {
            // Connect via run_host_on_stream after reading endpoint via read_daemon_endpoint
            // or directly exercise run_host by running until daemon drops socket.
            let dir = resolve_spaces_dir(Some(dir_clone)).unwrap();
            let ep = read_daemon_endpoint(&dir).unwrap();
            let stream = UnixStream::connect(&ep.socket).await.unwrap();
            let notify = std::sync::Arc::new(Notify::new());
            run_host_on_stream(stream, &ep.token, notify.notified())
                .await
                .unwrap();
        });

        let (stream, _) = listener.accept().await.unwrap();
        let (r, w) = stream.into_split();
        let mut daemon = MockDaemonConn {
            reader: BufReader::new(r),
            writer: w,
        };

        // 1. Expect rpc.auth with RpcClientKind::PtyHost
        let auth_frame = daemon.next_frame().await;
        let auth_req = match auth_frame {
            RpcFrame::Request(req) => req,
            other => panic!("expected RpcRequest for rpc.auth, got {other:?}"),
        };
        assert_eq!(auth_req.method, rpc_methods::AUTH);
        let auth_params: RpcAuthParams = serde_json::from_value(auth_req.params.unwrap()).unwrap();
        assert_eq!(auth_params.client_kind, RpcClientKind::PtyHost);
        assert_eq!(auth_params.token.as_deref(), Some(token));

        let auth_res = RpcAuthResult {
            ok: true,
            protocol_version: PROTOCOL_VERSION,
            boot_id: spaces_types::BootId::from("boot_test"),
            client_id: auth_params.client_id.unwrap(),
            current_seq: spaces_types::Seq(0),
        };
        daemon
            .send_line(&RpcResponse::ok(
                auth_req.id,
                serde_json::to_value(auth_res).unwrap(),
            ))
            .await;

        // 2. Send pty.spawn with explicit SPACES_PTY_ID
        let mut env = BTreeMap::new();
        env.insert("SPACES_PTY_ID".into(), "pty_custom_1".into());
        env.insert("SPACES_PTY_HMAC_TOKEN".into(), "hmac_abc".into());
        let spawn_params = PtySpawnParams {
            thread_id: ThreadId::from("thr_host_1"),
            argv: vec![
                "sh".into(),
                "-c".into(),
                "printf '\\033]7501;state=working:app=test\\007'; stty -echo; cat".into(),
            ],
            cwd: "/tmp".into(),
            env,
            size: PtySize {
                cols: 80,
                rows: 24,
                xpixel: None,
                ypixel: None,
            },
        };
        daemon
            .send_line(&RpcRequest::new(
                10_i64,
                rpc_methods::PTY_SPAWN,
                Some(serde_json::to_value(&spawn_params).unwrap()),
            ))
            .await;

        // Wait for spawn response and initial OSC status
        let mut spawn_res = None;
        let mut saw_osc = false;
        for _ in 0..10 {
            let frame = tokio::time::timeout(Duration::from_secs(3), daemon.next_frame())
                .await
                .expect("frame timeout");
            match frame {
                RpcFrame::Response(resp) if resp.id == RpcId::Number(10) => {
                    assert!(resp.error.is_none());
                    let parsed: PtySpawnResult =
                        serde_json::from_value(resp.result.unwrap()).unwrap();
                    assert_eq!(parsed.pty_id, PtyId::from("pty_custom_1"));
                    assert_eq!(parsed.pid, parsed.pgid);
                    spawn_res = Some(parsed);
                }
                RpcFrame::Notification(n) if n.method == rpc_methods::PTY_OSC => {
                    let p: PtyOscNotification = serde_json::from_value(n.params.unwrap()).unwrap();
                    if let Some(status) = p.status
                        && status.state == OscState::Working
                    {
                        saw_osc = true;
                    }
                }
                _ => {}
            }
            if spawn_res.is_some() && saw_osc {
                break;
            }
        }
        assert!(spawn_res.is_some());
        assert!(saw_osc);

        // 3. pty.attach + pty.detach
        daemon
            .send_line(&RpcRequest::new(
                11_i64,
                rpc_methods::PTY_ATTACH,
                Some(
                    serde_json::to_value(PtyAttachParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        after_offset: None,
                        request_replay: true,
                    })
                    .unwrap(),
                ),
            ))
            .await;

        let mut saw_attach = false;
        while !saw_attach {
            let frame = tokio::time::timeout(Duration::from_secs(3), daemon.next_frame())
                .await
                .unwrap();
            if let RpcFrame::Response(resp) = frame
                && resp.id == RpcId::Number(11)
            {
                let res: PtyAttachResult = serde_json::from_value(resp.result.unwrap()).unwrap();
                assert_eq!(res.pty_id, PtyId::from("pty_custom_1"));
                assert!(!res.replay_truncated);
                saw_attach = true;
            }
        }

        daemon
            .send_line(&RpcRequest::new(
                12_i64,
                rpc_methods::PTY_DETACH,
                Some(
                    serde_json::to_value(PtyDetachParams {
                        pty_id: PtyId::from("pty_custom_1"),
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(12)
            {
                assert_eq!(resp.result, Some(json!({ "ok": true })));
                break;
            }
        }

        // 4. pty.resize
        daemon
            .send_line(&RpcRequest::new(
                13_i64,
                rpc_methods::PTY_RESIZE,
                Some(
                    serde_json::to_value(PtyResizeParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        size: PtySize {
                            cols: 120,
                            rows: 40,
                            xpixel: None,
                            ypixel: None,
                        },
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(13)
            {
                assert_eq!(resp.result, Some(json!({ "ok": true })));
                break;
            }
        }

        // 5. pty.input -> cat echoes line
        daemon
            .send_line(&RpcRequest::new(
                14_i64,
                rpc_methods::PTY_INPUT,
                Some(
                    serde_json::to_value(PtyInputParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        epoch: None,
                        encoding: spaces_types::PtyEncoding::Base64,
                        data_b64: STANDARD.encode("hello-via-input\n"),
                    })
                    .unwrap(),
                ),
            ))
            .await;

        let mut saw_input_ack = false;
        let mut echoed = String::new();
        while !(saw_input_ack && echoed.contains("hello-via-input")) {
            let frame = tokio::time::timeout(Duration::from_secs(3), daemon.next_frame())
                .await
                .unwrap();
            match frame {
                RpcFrame::Response(resp) if resp.id == RpcId::Number(14) => {
                    assert_eq!(resp.result, Some(json!({ "ok": true })));
                    saw_input_ack = true;
                }
                RpcFrame::Notification(n) if n.method == rpc_methods::PTY_OUTPUT => {
                    let p: PtyOutputNotification =
                        serde_json::from_value(n.params.unwrap()).unwrap();
                    let bytes = STANDARD.decode(&p.data_b64).unwrap();
                    echoed.push_str(&String::from_utf8_lossy(&bytes));
                }
                _ => {}
            }
        }

        // 6. pty.kill -> child exits -> pty.exit notification
        daemon
            .send_line(&RpcRequest::new(
                15_i64,
                rpc_methods::PTY_KILL,
                Some(
                    serde_json::to_value(PtyKillParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        signal: Some("SIGTERM".into()),
                    })
                    .unwrap(),
                ),
            ))
            .await;

        let mut saw_kill_ack = false;
        let mut saw_exit = false;
        while !(saw_kill_ack && saw_exit) {
            let frame = tokio::time::timeout(Duration::from_secs(3), daemon.next_frame())
                .await
                .unwrap();
            match frame {
                RpcFrame::Response(resp) if resp.id == RpcId::Number(15) => {
                    assert_eq!(resp.result, Some(json!({ "ok": true })));
                    saw_kill_ack = true;
                }
                RpcFrame::Notification(n) if n.method == rpc_methods::PTY_EXIT => {
                    let p: PtyExitNotification = serde_json::from_value(n.params.unwrap()).unwrap();
                    assert_eq!(p.pty_id, PtyId::from("pty_custom_1"));
                    assert_eq!(p.signal.as_deref(), Some("SIGTERM"));
                    saw_exit = true;
                }
                _ => {}
            }
        }

        // 7. Error paths + request_replay=false + unknown method & NotFound check
        daemon
            .send_line(&RpcRequest::new(
                16_i64,
                rpc_methods::PTY_ATTACH,
                Some(
                    serde_json::to_value(PtyAttachParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        after_offset: None,
                        request_replay: false,
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(16)
            {
                let res: PtyAttachResult = serde_json::from_value(resp.result.unwrap()).unwrap();
                assert!(res.current_offset > 0);
                break;
            }
        }

        daemon
            .send_line(&RpcRequest::new(
                17_i64,
                rpc_methods::PTY_INPUT,
                Some(
                    serde_json::to_value(PtyInputParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        epoch: None,
                        encoding: spaces_types::PtyEncoding::Base64,
                        data_b64: "!!!not-base64!!!".into(),
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(17)
            {
                assert_eq!(resp.error.unwrap().code, RpcErrorCode::InvalidParams.code());
                break;
            }
        }

        daemon
            .send_line(&RpcRequest::new(
                18_i64,
                rpc_methods::PTY_KILL,
                Some(
                    serde_json::to_value(PtyKillParams {
                        pty_id: PtyId::from("pty_custom_1"),
                        signal: Some("SIGBOGUS".into()),
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(18)
            {
                assert_eq!(resp.error.unwrap().code, RpcErrorCode::InvalidParams.code());
                break;
            }
        }

        daemon
            .send_line(&RpcRequest::new(
                19_i64,
                rpc_methods::PTY_ATTACH,
                Some(
                    serde_json::to_value(PtyAttachParams {
                        pty_id: PtyId::from("pty_missing"),
                        after_offset: None,
                        request_replay: true,
                    })
                    .unwrap(),
                ),
            ))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(19)
            {
                assert_eq!(resp.error.unwrap().code, RpcErrorCode::NotFound.code());
                break;
            }
        }

        daemon
            .send_line(&RpcRequest::new(20_i64, "pty.does_not_exist", None))
            .await;
        loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(20)
            {
                assert_eq!(
                    resp.error.unwrap().code,
                    RpcErrorCode::MethodNotFound.code()
                );
                break;
            }
        }

        // Close daemon socket -> host exits cleanly
        drop(daemon);
        tokio::time::timeout(Duration::from_secs(3), host_task)
            .await
            .expect("host task timeout")
            .expect("host task join");

        let _ = std::fs::remove_dir_all(&tmp);
    }

    #[tokio::test]
    async fn run_host_reads_endpoint_and_exits_on_daemon_close() {
        let tmp = std::env::temp_dir().join(format!("spaces-pty-run-host-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&tmp).unwrap();
        let sock_path = tmp.join("daemon.sock");
        let token = "ep-token-123";
        std::fs::write(
            tmp.join("daemon.endpoint.json"),
            serde_json::to_string(&json!({
                "socket": sock_path.to_string_lossy(),
                "token": token,
            }))
            .unwrap(),
        )
        .unwrap();

        let listener = UnixListener::bind(&sock_path).unwrap();
        let dir_clone = tmp.clone();
        let host_task = tokio::spawn(async move { run_host(Some(dir_clone)).await });

        let (stream, _) = listener.accept().await.unwrap();
        let (r, w) = stream.into_split();
        let mut daemon = MockDaemonConn {
            reader: BufReader::new(r),
            writer: w,
        };

        let auth_req = match daemon.next_frame().await {
            RpcFrame::Request(req) => req,
            other => panic!("expected rpc.auth request, got {other:?}"),
        };
        daemon
            .send_line(&RpcResponse::ok(
                auth_req.id,
                serde_json::to_value(RpcAuthResult {
                    ok: true,
                    protocol_version: PROTOCOL_VERSION,
                    boot_id: spaces_types::BootId::from("b_run"),
                    client_id: "c_run".into(),
                    current_seq: spaces_types::Seq(0),
                })
                .unwrap(),
            ))
            .await;

        drop(daemon);
        let res = tokio::time::timeout(Duration::from_secs(3), host_task)
            .await
            .expect("run_host timeout")
            .expect("join");
        assert!(res.is_ok(), "run_host failed: {res:?}");
        let _ = std::fs::remove_dir_all(&tmp);
    }

    #[tokio::test]
    async fn host_graceful_shutdown_kills_active_child_process_groups() {
        let tmp = std::env::temp_dir().join(format!("spaces-pty-shutdown-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&tmp).unwrap();
        let sock_path = tmp.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).unwrap();
        let shutdown = std::sync::Arc::new(Notify::new());
        let shutdown_clone = std::sync::Arc::clone(&shutdown);

        let stream = UnixStream::connect(&sock_path).await.unwrap();
        let host_task = tokio::spawn(async move {
            run_host_on_stream(stream, "tok", shutdown_clone.notified())
                .await
                .unwrap();
        });

        let (stream, _) = listener.accept().await.unwrap();
        let (r, w) = stream.into_split();
        let mut daemon = MockDaemonConn {
            reader: BufReader::new(r),
            writer: w,
        };

        let auth_req = match daemon.next_frame().await {
            RpcFrame::Request(req) => req,
            other => panic!("expected auth req, got {other:?}"),
        };
        daemon
            .send_line(&RpcResponse::ok(
                auth_req.id,
                serde_json::to_value(RpcAuthResult {
                    ok: true,
                    protocol_version: PROTOCOL_VERSION,
                    boot_id: spaces_types::BootId::from("b1"),
                    client_id: "c1".into(),
                    current_seq: spaces_types::Seq(0),
                })
                .unwrap(),
            ))
            .await;

        let spawn_params = PtySpawnParams {
            thread_id: ThreadId::from("thr_sd"),
            argv: vec!["sh".into(), "-c".into(), "sleep 60".into()],
            cwd: "/tmp".into(),
            env: BTreeMap::new(),
            size: PtySize {
                cols: 80,
                rows: 24,
                xpixel: None,
                ypixel: None,
            },
        };
        daemon
            .send_line(&RpcRequest::new(
                20_i64,
                rpc_methods::PTY_SPAWN,
                Some(serde_json::to_value(&spawn_params).unwrap()),
            ))
            .await;

        let spawned: PtySpawnResult = loop {
            if let RpcFrame::Response(resp) = daemon.next_frame().await
                && resp.id == RpcId::Number(20)
            {
                break serde_json::from_value(resp.result.unwrap()).unwrap();
            }
        };
        assert!(spawned.pty_id.0.starts_with("pty_"));

        // Trigger graceful shutdown -> should kill child process group and terminate cleanly.
        shutdown.notify_one();
        tokio::time::timeout(Duration::from_secs(3), host_task)
            .await
            .expect("host shutdown timeout")
            .expect("host join");

        // Verify child process is no longer alive.
        let rc = unsafe { libc::kill(spawned.pid as i32, 0) };
        assert_eq!(rc, -1);
        assert_eq!(
            std::io::Error::last_os_error().raw_os_error(),
            Some(libc::ESRCH)
        );

        let _ = std::fs::remove_dir_all(&tmp);
    }
}

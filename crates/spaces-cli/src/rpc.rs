use std::collections::HashMap;
use std::fmt;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::{Arc, Mutex};

use anyhow::{Context, Result, anyhow, bail};
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;
use spaces_types::PROTOCOL_VERSION;
use spaces_types::rpc::rpc_methods;
use spaces_types::rpc_core::{
    RpcAuthParams, RpcAuthResult, RpcClientKind, RpcError, RpcFrame, RpcId, RpcNotification,
    RpcRequest,
};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;
use tokio::sync::{mpsc, oneshot};

use crate::endpoint::DaemonEndpoint;

/// Error returned by the remote `spacesd` JSON-RPC endpoint.
#[derive(Debug, Clone)]
pub struct RpcRemoteError {
    pub error: RpcError,
}

impl fmt::Display for RpcRemoteError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "RPC error ({}): {}", self.error.code, self.error.message)
    }
}

impl std::error::Error for RpcRemoteError {}

type PendingMap = Arc<Mutex<HashMap<RpcId, oneshot::Sender<Result<Value>>>>>;

/// Authenticated UDS NDJSON-RPC 2.0 client for `spacesd`.
pub struct RpcClient {
    next_id: AtomicI64,
    pending: PendingMap,
    write_tx: mpsc::UnboundedSender<String>,
    notif_rx: Option<mpsc::UnboundedReceiver<RpcNotification>>,
    auth: RpcAuthResult,
}

impl RpcClient {
    /// Connects to `endpoint.socket`, spawns reader/writer tasks, and authenticates via `rpc.auth`.
    pub async fn connect(endpoint: &DaemonEndpoint) -> Result<Self> {
        let stream = UnixStream::connect(&endpoint.socket)
            .await
            .with_context(|| {
                format!("failed to connect to spacesd socket at {}", endpoint.socket)
            })?;
        let (read_half, mut write_half) = stream.into_split();

        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let (write_tx, mut write_rx) = mpsc::unbounded_channel::<String>();
        let (notif_tx, notif_rx) = mpsc::unbounded_channel::<RpcNotification>();

        let reader_pending = Arc::clone(&pending);
        tokio::spawn(async move {
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();
            loop {
                line.clear();
                match reader.read_line(&mut line).await {
                    Ok(0) => {
                        fail_all_pending(&reader_pending, "daemon closed RPC connection");
                        break;
                    }
                    Ok(_) => {
                        let trimmed = line.trim();
                        if trimmed.is_empty() {
                            continue;
                        }
                        match serde_json::from_str::<RpcFrame>(trimmed) {
                            Ok(RpcFrame::Response(resp)) => {
                                let tx = {
                                    let mut guard =
                                        reader_pending.lock().expect("pending map lock poisoned");
                                    guard.remove(&resp.id)
                                };
                                if let Some(tx) = tx {
                                    let outcome = if let Some(err) = resp.error {
                                        Err(anyhow::Error::new(RpcRemoteError { error: err }))
                                    } else {
                                        Ok(resp.result.unwrap_or(Value::Null))
                                    };
                                    let _ = tx.send(outcome);
                                }
                            }
                            Ok(RpcFrame::Notification(notif)) => {
                                let _ = notif_tx.send(notif);
                            }
                            Ok(RpcFrame::Request(_)) | Err(_) => {}
                        }
                    }
                    Err(err) => {
                        fail_all_pending(&reader_pending, &format!("RPC read error: {err}"));
                        break;
                    }
                }
            }
        });

        let writer_pending = Arc::clone(&pending);
        tokio::spawn(async move {
            while let Some(frame) = write_rx.recv().await {
                if let Err(err) = write_half.write_all(frame.as_bytes()).await {
                    fail_all_pending(&writer_pending, &format!("RPC write error: {err}"));
                    break;
                }
                if let Err(err) = write_half.flush().await {
                    fail_all_pending(&writer_pending, &format!("RPC flush error: {err}"));
                    break;
                }
            }
            let _ = write_half.shutdown().await;
        });

        let mut client = Self {
            next_id: AtomicI64::new(1),
            pending,
            write_tx,
            notif_rx: Some(notif_rx),
            auth: RpcAuthResult {
                ok: false,
                protocol_version: PROTOCOL_VERSION,
                boot_id: endpoint.boot_id.clone(),
                client_id: String::new(),
                current_seq: spaces_types::Seq(0),
            },
        };

        let client_id = format!("cli-{}-{}", std::process::id(), uuid::Uuid::new_v4());
        let auth: RpcAuthResult = client
            .call(
                rpc_methods::AUTH,
                RpcAuthParams {
                    protocol_version: PROTOCOL_VERSION,
                    client_kind: RpcClientKind::Cli,
                    client_id: Some(client_id),
                    token: Some(endpoint.token.clone()),
                    label: Some("spaces-cli".to_owned()),
                },
            )
            .await
            .context("rpc.auth handshake failed")?;

        if !auth.ok {
            bail!("spacesd rejected rpc.auth handshake");
        }
        client.auth = auth;

        Ok(client)
    }

    /// Returns the authenticated `client_id` assigned during `rpc.auth`.
    pub fn client_id(&self) -> &str {
        &self.auth.client_id
    }

    /// Sends a JSON-RPC 2.0 request and awaits its typed response.
    pub async fn call<P: Serialize, R: DeserializeOwned>(
        &self,
        method: &str,
        params: P,
    ) -> Result<R> {
        let id = RpcId::Number(self.next_id.fetch_add(1, Ordering::Relaxed));
        let params_value = serialize_params(params)?;
        let request = RpcRequest::new(id.clone(), method, params_value);
        let mut line = serde_json::to_string(&request).context("failed to encode RPC request")?;
        line.push('\n');

        let (tx, rx) = oneshot::channel();
        {
            let mut guard = self.pending.lock().expect("pending map lock poisoned");
            guard.insert(id.clone(), tx);
        }

        if self.write_tx.send(line).is_err() {
            let mut guard = self.pending.lock().expect("pending map lock poisoned");
            guard.remove(&id);
            bail!("RPC connection closed while sending {method}");
        }

        let result_value = rx
            .await
            .map_err(|_| anyhow!("RPC connection closed before receiving reply for {method}"))??;

        serde_json::from_value(result_value)
            .with_context(|| format!("failed to decode response for {method}"))
    }

    /// Sends a JSON-RPC 2.0 notification without waiting for a response.
    pub async fn notify<P: Serialize>(&self, method: &str, params: P) -> Result<()> {
        let params_value = serialize_params(params)?;
        let notification = RpcNotification::new(method, params_value);
        let mut line =
            serde_json::to_string(&notification).context("failed to encode RPC notification")?;
        line.push('\n');

        self.write_tx
            .send(line)
            .map_err(|_| anyhow!("RPC connection closed while sending notification {method}"))
    }

    /// Takes ownership of the incoming notification stream (`pty.output`, `pty.exit`, `event.push`).
    pub fn take_notifications(&mut self) -> mpsc::UnboundedReceiver<RpcNotification> {
        self.notif_rx
            .take()
            .unwrap_or_else(|| mpsc::unbounded_channel().1)
    }
}

fn serialize_params<P: Serialize>(params: P) -> Result<Option<Value>> {
    let value = serde_json::to_value(params).context("failed to serialize RPC params")?;
    if value.is_null() {
        Ok(None)
    } else {
        Ok(Some(value))
    }
}

fn fail_all_pending(pending: &PendingMap, reason: &str) {
    let drained: Vec<oneshot::Sender<Result<Value>>> = {
        let mut guard = pending.lock().expect("pending map lock poisoned");
        guard.drain().map(|(_, tx)| tx).collect()
    };
    for tx in drained {
        let _ = tx.send(Err(anyhow!("{reason}")));
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use serde_json::json;
    use spaces_types::rpc_core::{RpcErrorCode, RpcResponse};
    use spaces_types::{BootId, Seq, TimestampMs};
    use tokio::net::UnixListener;

    use super::*;

    fn temp_socket_dir(prefix: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("spaces-cli-rpc-{prefix}-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).expect("create temp dir");
        dir
    }

    #[tokio::test]
    async fn authenticates_calls_notifies_and_streams_notifications() {
        let dir = temp_socket_dir("ok");
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).expect("bind mock socket");

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.expect("accept client");
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();

            // 1. Expect rpc.auth
            reader.read_line(&mut line).await.expect("read auth");
            let auth_req: RpcRequest = serde_json::from_str(line.trim()).expect("parse auth");
            assert_eq!(auth_req.method, rpc_methods::AUTH);
            let auth_params: RpcAuthParams =
                serde_json::from_value(auth_req.params.expect("auth params")).expect("decode auth");
            assert_eq!(auth_params.client_kind, RpcClientKind::Cli);
            assert_eq!(auth_params.protocol_version, PROTOCOL_VERSION);
            assert_eq!(auth_params.token.as_deref(), Some("secret-token"));

            let auth_resp = RpcResponse::ok(
                auth_req.id,
                serde_json::to_value(RpcAuthResult {
                    ok: true,
                    protocol_version: PROTOCOL_VERSION,
                    boot_id: BootId::from("boot_mock"),
                    client_id: auth_params.client_id.unwrap_or_else(|| "cli-1".to_owned()),
                    current_seq: Seq(7),
                })
                .unwrap(),
            );
            let mut raw = serde_json::to_string(&auth_resp).unwrap();
            raw.push('\n');
            write_half.write_all(raw.as_bytes()).await.unwrap();

            // 2. Push a streaming notification immediately
            let push = RpcNotification::new(
                rpc_methods::PTY_OUTPUT,
                Some(
                    json!({"pty_id": "pty_1", "offset": 0, "encoding": "base64", "data_b64": "aGk=", "ts_ms": 100}),
                ),
            );
            let mut push_raw = serde_json::to_string(&push).unwrap();
            push_raw.push('\n');
            write_half.write_all(push_raw.as_bytes()).await.unwrap();

            // 3. Handle daemon.ping call
            line.clear();
            reader.read_line(&mut line).await.expect("read ping");
            let ping_req: RpcRequest = serde_json::from_str(line.trim()).expect("parse ping");
            assert_eq!(ping_req.method, rpc_methods::DAEMON_PING);
            let ping_resp = RpcResponse::ok(
                ping_req.id,
                json!({"ok": true, "boot_id": "boot_mock", "ts_ms": 1234}),
            );
            let mut ping_raw = serde_json::to_string(&ping_resp).unwrap();
            ping_raw.push('\n');
            write_half.write_all(ping_raw.as_bytes()).await.unwrap();

            // 4. Handle failing RPC call
            line.clear();
            reader.read_line(&mut line).await.expect("read err call");
            let err_req: RpcRequest = serde_json::from_str(line.trim()).expect("parse err call");
            let err_resp = RpcResponse::err(
                err_req.id,
                RpcError::new(RpcErrorCode::NotFound, "missing thread"),
            );
            let mut err_raw = serde_json::to_string(&err_resp).unwrap();
            err_raw.push('\n');
            write_half.write_all(err_raw.as_bytes()).await.unwrap();

            // 5. Expect notify frame
            line.clear();
            reader.read_line(&mut line).await.expect("read notify");
            let notif: RpcNotification = serde_json::from_str(line.trim()).expect("parse notif");
            assert_eq!(notif.method, rpc_methods::PTY_INPUT);
            assert_eq!(notif.params.unwrap()["data_b64"], "YQ==");
        });

        let endpoint = DaemonEndpoint {
            version: PROTOCOL_VERSION,
            protocol: "ndjson-rpc/uds".to_owned(),
            pid: 99,
            uid: 1000,
            socket: sock_path.to_string_lossy().into_owned(),
            token: "secret-token".to_owned(),
            boot_id: BootId::from("boot_mock"),
            started_ms: TimestampMs(10),
        };

        let mut client = RpcClient::connect(&endpoint).await.expect("connect");
        let mut notifs = client.take_notifications();

        let notif = notifs.recv().await.expect("receive notification");
        assert_eq!(notif.method, rpc_methods::PTY_OUTPUT);

        let ping: spaces_types::DaemonPingResult = client
            .call(rpc_methods::DAEMON_PING, ())
            .await
            .expect("ping ok");
        assert!(ping.ok);
        assert_eq!(ping.boot_id, BootId::from("boot_mock"));

        let err = client
            .call::<_, Value>(rpc_methods::THREAD_GET, json!({"thread_id": "thr_missing"}))
            .await
            .expect_err("expected remote error");
        let remote = err
            .downcast_ref::<RpcRemoteError>()
            .expect("downcast RpcRemoteError");
        assert_eq!(remote.error.code, RpcErrorCode::NotFound.code());

        client
            .notify(
                rpc_methods::PTY_INPUT,
                json!({"pty_id": "pty_1", "encoding": "base64", "data_b64": "YQ=="}),
            )
            .await
            .expect("notify ok");

        server.await.expect("mock server task");
        std::fs::remove_dir_all(&dir).ok();
    }
}

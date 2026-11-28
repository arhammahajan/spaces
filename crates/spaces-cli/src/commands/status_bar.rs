use anyhow::{Context, Result};
use clap::Args;
use spaces_types::rpc::rpc_methods;
use spaces_types::{BackendStatusBarParams, BackendStatusBarResult};

use crate::rpc::RpcClient;

#[derive(Debug, Clone, Default, Args)]
pub struct StatusBarArgs {
    /// Emit plain text badges without tmux color formatting
    #[arg(long)]
    pub plain: bool,

    /// Skip periodic session autosave check
    #[arg(long)]
    pub skip_autosave: bool,
}

/// Fetches and prints the compact status-bar badge segment from `spacesd`.
pub async fn run(client: RpcClient, args: StatusBarArgs, json: bool) -> Result<()> {
    let res: BackendStatusBarResult = client
        .call(
            rpc_methods::BACKEND_STATUS_BAR_GET,
            BackendStatusBarParams {
                skip_autosave: args.skip_autosave,
            },
        )
        .await
        .context("backend.status_bar.get RPC failed")?;

    if let Some(line) = format_output(&res, args.plain, json)? {
        println!("{line}");
    }
    Ok(())
}

/// Formats a [`BackendStatusBarResult`] according to `--json` and `--plain`.
///
/// Returns `Ok(None)` when nothing should be printed (empty badge in tmux/plain mode).
pub(crate) fn format_output(
    res: &BackendStatusBarResult,
    plain: bool,
    json: bool,
) -> Result<Option<String>> {
    if json {
        return Ok(Some(
            serde_json::to_string(&res.counts).context("failed to serialize status bar counts")?,
        ));
    }

    let formatted = if plain {
        &res.formatted_plain
    } else {
        &res.formatted_tmux
    };

    if formatted.is_empty() {
        Ok(None)
    } else {
        Ok(Some(formatted.clone()))
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;
    use spaces_types::rpc_core::{RpcAuthParams, RpcAuthResult, RpcRequest, RpcResponse};
    use spaces_types::{BootId, PROTOCOL_VERSION, Seq, StatusBarCounts, TimestampMs};
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    use tokio::net::UnixListener;

    use super::*;
    use crate::endpoint::DaemonEndpoint;

    #[test]
    fn formats_json_even_when_all_zero() {
        let res = BackendStatusBarResult {
            counts: StatusBarCounts::default(),
            formatted_tmux: String::new(),
            formatted_plain: String::new(),
        };

        let out = format_output(&res, false, true)
            .expect("format json")
            .expect("json always prints");
        let parsed: serde_json::Value = serde_json::from_str(&out).expect("valid json");
        assert_eq!(
            parsed,
            json!({"needs": 0, "failed": 0, "working": 0, "done": 0, "idle": 0})
        );
    }

    #[test]
    fn formats_plain_and_tmux_outputs() {
        let res = BackendStatusBarResult {
            counts: StatusBarCounts {
                needs: 2,
                failed: 1,
                working: 3,
                done: 4,
                idle: 5,
            },
            formatted_tmux: "#[fg=#89B4FA]●2#[default] #[fg=#F38BA8]✗1#[default]".to_owned(),
            formatted_plain: "●2 ✗1".to_owned(),
        };

        assert_eq!(
            format_output(&res, false, false).expect("tmux"),
            Some("#[fg=#89B4FA]●2#[default] #[fg=#F38BA8]✗1#[default]".to_owned())
        );
        assert_eq!(
            format_output(&res, true, false).expect("plain"),
            Some("●2 ✗1".to_owned())
        );
        // --json takes precedence over --plain, matching internal/cli/statusbar.go.
        let json_out = format_output(&res, true, true)
            .expect("json")
            .expect("json present");
        let parsed: serde_json::Value = serde_json::from_str(&json_out).expect("valid json");
        assert_eq!(
            parsed,
            json!({"needs": 2, "failed": 1, "working": 3, "done": 4, "idle": 5})
        );
    }

    #[test]
    fn omits_output_when_formatted_strings_are_empty() {
        let res = BackendStatusBarResult {
            counts: StatusBarCounts {
                idle: 3,
                ..Default::default()
            },
            formatted_tmux: String::new(),
            formatted_plain: String::new(),
        };

        assert_eq!(format_output(&res, false, false).expect("tmux"), None);
        assert_eq!(format_output(&res, true, false).expect("plain"), None);
    }

    #[tokio::test]
    async fn calls_backend_status_bar_get_with_skip_autosave() {
        let dir =
            std::env::temp_dir().join(format!("spaces-cli-status-bar-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).expect("create temp dir");
        let sock_path = dir.join("daemon.sock");
        let listener = UnixListener::bind(&sock_path).expect("bind mock socket");

        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.expect("accept client");
            let (read_half, mut write_half) = stream.into_split();
            let mut reader = BufReader::new(read_half);
            let mut line = String::new();

            reader.read_line(&mut line).await.expect("read auth");
            let auth_req: RpcRequest = serde_json::from_str(line.trim()).expect("parse auth");
            let auth_params: RpcAuthParams =
                serde_json::from_value(auth_req.params.expect("auth params")).expect("decode auth");
            let auth_resp = RpcResponse::ok(
                auth_req.id,
                serde_json::to_value(RpcAuthResult {
                    ok: true,
                    protocol_version: PROTOCOL_VERSION,
                    boot_id: BootId::from("boot_sb"),
                    client_id: auth_params.client_id.unwrap_or_else(|| "cli-sb".to_owned()),
                    current_seq: Seq(1),
                })
                .unwrap(),
            );
            let mut raw = serde_json::to_string(&auth_resp).unwrap();
            raw.push('\n');
            write_half.write_all(raw.as_bytes()).await.unwrap();

            line.clear();
            reader.read_line(&mut line).await.expect("read status_bar");
            let req: RpcRequest = serde_json::from_str(line.trim()).expect("parse status_bar");
            assert_eq!(req.method, rpc_methods::BACKEND_STATUS_BAR_GET);
            let params: BackendStatusBarParams =
                serde_json::from_value(req.params.expect("status_bar params"))
                    .expect("decode params");
            assert!(params.skip_autosave);

            let resp = RpcResponse::ok(
                req.id,
                serde_json::to_value(BackendStatusBarResult {
                    counts: StatusBarCounts {
                        needs: 1,
                        failed: 0,
                        working: 0,
                        done: 0,
                        idle: 2,
                    },
                    formatted_tmux: "#[fg=#89B4FA]●1#[default]".to_owned(),
                    formatted_plain: "●1".to_owned(),
                })
                .unwrap(),
            );
            let mut resp_raw = serde_json::to_string(&resp).unwrap();
            resp_raw.push('\n');
            write_half.write_all(resp_raw.as_bytes()).await.unwrap();
        });

        let endpoint = DaemonEndpoint {
            version: PROTOCOL_VERSION,
            protocol: "ndjson-rpc/uds".to_owned(),
            pid: 100,
            uid: 1000,
            socket: sock_path.to_string_lossy().into_owned(),
            token: "tok-sb".to_owned(),
            boot_id: BootId::from("boot_sb"),
            started_ms: TimestampMs(10),
        };

        let client = RpcClient::connect(&endpoint).await.expect("connect");
        run(
            client,
            StatusBarArgs {
                plain: true,
                skip_autosave: true,
            },
            false,
        )
        .await
        .expect("run status-bar");

        server.await.expect("mock server task");
        std::fs::remove_dir_all(&dir).ok();
    }
}

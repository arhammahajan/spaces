use anyhow::{Context, Result};
use clap::{Args, ValueEnum};
use spaces_types::rpc::rpc_methods;
use spaces_types::rpc_core::{LeaseHandoffParams, LeaseResult, LeaseStatusParams};
use spaces_types::{HandoffReason, LeaseOwner, LeaseOwnerKind, ThreadId};

use crate::commands::{default_human_owner_id, format_lease_owner, format_lease_state};
use crate::rpc::RpcClient;

#[derive(Debug, Clone, Copy, PartialEq, Eq, ValueEnum)]
pub enum OwnerKindArg {
    Human,
    Agent,
    System,
}

impl From<OwnerKindArg> for LeaseOwnerKind {
    fn from(value: OwnerKindArg) -> Self {
        match value {
            OwnerKindArg::Human => Self::Human,
            OwnerKindArg::Agent => Self::Agent,
            OwnerKindArg::System => Self::System,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, ValueEnum)]
pub enum HandoffReasonArg {
    #[value(alias = "user_attach")]
    UserAttach,
    #[value(alias = "user_yield")]
    UserYield,
    #[value(alias = "agent_claim")]
    AgentClaim,
    #[value(alias = "agent_yield")]
    AgentYield,
    Disconnect,
    Timeout,
    Cancel,
}

impl From<HandoffReasonArg> for HandoffReason {
    fn from(value: HandoffReasonArg) -> Self {
        match value {
            HandoffReasonArg::UserAttach => Self::UserAttach,
            HandoffReasonArg::UserYield => Self::UserYield,
            HandoffReasonArg::AgentClaim => Self::AgentClaim,
            HandoffReasonArg::AgentYield => Self::AgentYield,
            HandoffReasonArg::Disconnect => Self::Disconnect,
            HandoffReasonArg::Timeout => Self::Timeout,
            HandoffReasonArg::Cancel => Self::Cancel,
        }
    }
}

#[derive(Debug, Clone, Args)]
pub struct HandoffArgs {
    /// Target thread ID to transfer lease ownership for
    pub thread_id: String,
    /// New lease owner kind
    #[arg(long, value_enum)]
    pub to: OwnerKindArg,
    /// Optional explicit owner ID (defaults based on `--to`)
    #[arg(long)]
    pub id: Option<String>,
    /// Cancel any active turn during handoff
    #[arg(long)]
    pub force: bool,
    /// Reason recorded in the lease transition audit log
    #[arg(long, value_enum)]
    pub reason: Option<HandoffReasonArg>,
}

/// Transfers a thread's fenced ownership lease to another actor.
pub async fn run(client: RpcClient, args: HandoffArgs, json: bool) -> Result<()> {
    let res = execute_handoff(&client, &args).await?;

    if json {
        println!("{}", serde_json::to_string_pretty(&res)?);
    } else {
        let state = format_lease_state(res.lease.state);
        let owner = res
            .lease
            .owner
            .as_ref()
            .map(format_lease_owner)
            .unwrap_or_else(|| "-".to_owned());
        println!(
            "thread={} state={} epoch={} owner={}",
            res.lease.thread_id, state, res.lease.epoch.0, owner
        );
    }

    Ok(())
}

async fn execute_handoff(client: &RpcClient, args: &HandoffArgs) -> Result<LeaseResult> {
    let thread_id = ThreadId::from(args.thread_id.as_str());
    let status: LeaseResult = client
        .call(
            rpc_methods::LEASE_STATUS,
            LeaseStatusParams {
                thread_id: thread_id.clone(),
            },
        )
        .await
        .with_context(|| format!("failed to query lease status for {thread_id}"))?;

    let to_kind: LeaseOwnerKind = args.to.into();
    let owner_id = args.id.clone().unwrap_or_else(|| default_owner_id(to_kind));
    let reason = args
        .reason
        .map(Into::into)
        .unwrap_or_else(|| default_reason(to_kind, args.force));

    client
        .call(
            rpc_methods::LEASE_HANDOFF,
            LeaseHandoffParams {
                thread_id: thread_id.clone(),
                from_epoch: status.lease.epoch,
                to_owner: LeaseOwner {
                    kind: to_kind,
                    id: owner_id,
                    client_id: Some(client.client_id().to_owned()),
                    label: Some("spaces handoff".to_owned()),
                },
                reason,
                from_owner: status.lease.owner,
                ttl_ms: None,
                cancel_active_turn: Some(args.force),
            },
        )
        .await
        .with_context(|| format!("lease.handoff failed for {thread_id}"))
}

fn default_owner_id(kind: LeaseOwnerKind) -> String {
    match kind {
        LeaseOwnerKind::Human => default_human_owner_id(),
        LeaseOwnerKind::Agent => "agent".to_owned(),
        LeaseOwnerKind::System => "system".to_owned(),
    }
}

fn default_reason(kind: LeaseOwnerKind, force: bool) -> HandoffReason {
    if force {
        return HandoffReason::Cancel;
    }
    match kind {
        LeaseOwnerKind::Human => HandoffReason::UserAttach,
        LeaseOwnerKind::Agent | LeaseOwnerKind::System => HandoffReason::AgentClaim,
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use spaces_types::rpc_core::{RpcAuthResult, RpcRequest, RpcResponse};
    use spaces_types::{BootId, Epoch, Lease, LeaseId, LeaseState, Seq, TimestampMs};
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    use tokio::net::UnixListener;

    use super::*;
    use crate::endpoint::DaemonEndpoint;

    #[tokio::test]
    async fn handoff_queries_status_and_forwards_from_epoch_and_force() {
        let dir = std::env::temp_dir().join(format!("spaces-cli-handoff-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let sock_path: PathBuf = dir.join("daemon.sock");
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
                        boot_id: BootId::from("boot_h"),
                        client_id: "cli-h".to_owned(),
                        current_seq: Seq(0),
                    })
                    .unwrap(),
                    rpc_methods::LEASE_STATUS => serde_json::to_value(LeaseResult {
                        lease: Lease {
                            id: LeaseId::from("lease_9"),
                            thread_id: ThreadId::from("thr_9"),
                            epoch: Epoch(3),
                            state: LeaseState::Owned,
                            owner: Some(LeaseOwner {
                                kind: LeaseOwnerKind::Human,
                                id: "alice".to_owned(),
                                client_id: None,
                                label: None,
                            }),
                            acquired_at_ms: Some(TimestampMs(10)),
                            expires_at_ms: None,
                            updated_at_ms: TimestampMs(10),
                        },
                        transition: None,
                    })
                    .unwrap(),
                    rpc_methods::LEASE_HANDOFF => {
                        let params: LeaseHandoffParams =
                            serde_json::from_value(req.params.unwrap()).unwrap();
                        assert_eq!(params.from_epoch, Epoch(3));
                        assert_eq!(params.to_owner.kind, LeaseOwnerKind::Agent);
                        assert_eq!(params.to_owner.id, "coordinator");
                        assert_eq!(params.cancel_active_turn, Some(true));
                        assert_eq!(params.reason, HandoffReason::Cancel);
                        serde_json::to_value(LeaseResult {
                            lease: Lease {
                                id: LeaseId::from("lease_9"),
                                thread_id: ThreadId::from("thr_9"),
                                epoch: Epoch(4),
                                state: LeaseState::Owned,
                                owner: Some(params.to_owner),
                                acquired_at_ms: Some(TimestampMs(20)),
                                expires_at_ms: None,
                                updated_at_ms: TimestampMs(20),
                            },
                            transition: None,
                        })
                        .unwrap()
                    }
                    other => panic!("unexpected call: {other}"),
                };

                let mut raw = serde_json::to_string(&RpcResponse::ok(req.id, result)).unwrap();
                raw.push('\n');
                write_half.write_all(raw.as_bytes()).await.unwrap();
                if req.method == rpc_methods::LEASE_HANDOFF {
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
            boot_id: BootId::from("boot_h"),
            started_ms: TimestampMs(1),
        };
        let client = RpcClient::connect(&endpoint).await.unwrap();
        let res = execute_handoff(
            &client,
            &HandoffArgs {
                thread_id: "thr_9".to_owned(),
                to: OwnerKindArg::Agent,
                id: Some("coordinator".to_owned()),
                force: true,
                reason: None,
            },
        )
        .await
        .unwrap();

        assert_eq!(res.lease.epoch, Epoch(4));
        assert_eq!(res.lease.owner.unwrap().id, "coordinator");
        server.await.unwrap();
        std::fs::remove_dir_all(&dir).ok();
    }
}

mod commands;
mod endpoint;
mod rpc;
mod terminal;

use std::path::PathBuf;

use anyhow::{Context, Result};
use clap::{Parser, Subcommand};
use spaces_types::rpc::rpc_methods;
use spaces_types::{DaemonPingResult, DaemonStatusResult};

use crate::commands::attach::AttachArgs;
use crate::commands::handoff::HandoffArgs;
use crate::commands::ls::LsArgs;
use crate::commands::spawn::SpawnArgs;
use crate::endpoint::{load_endpoint, resolve_spaces_dir};
use crate::rpc::RpcClient;

#[derive(Debug, Parser)]
#[command(name = "spaces", version, about = "Spaces unified control-plane CLI")]
pub struct Cli {
    /// Override the Spaces runtime directory (defaults to SPACES_DIR or ~/.spaces)
    #[arg(long, global = true, value_name = "PATH")]
    pub spaces_dir: Option<PathBuf>,

    /// Emit machine-readable JSON output
    #[arg(long, global = true)]
    pub json: bool,

    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    /// Spawn a new managed thread and PTY session
    Spawn(SpawnArgs),
    /// Attach to an existing thread or PTY session
    Attach(AttachArgs),
    /// Hand off a thread's write lease to another owner
    Handoff(HandoffArgs),
    /// List managed threads and their current lease status
    Ls(LsArgs),
    /// Ping the running spacesd daemon
    Ping,
    /// Show spacesd daemon status and counters
    Status,
    /// Internal PTY host process spawned by spacesd
    #[command(name = "__pty-host", hide = true)]
    PtyHost,
}

#[tokio::main]
async fn main() -> Result<()> {
    let cli = Cli::parse();
    run_cli(cli).await
}

async fn run_cli(cli: Cli) -> Result<()> {
    if matches!(cli.command, Command::PtyHost) {
        return spaces_pty::run_host(cli.spaces_dir).await;
    }

    let spaces_dir = resolve_spaces_dir(cli.spaces_dir.as_deref());
    let endpoint = load_endpoint(&spaces_dir)?;
    let client = RpcClient::connect(&endpoint).await?;

    match cli.command {
        Command::Spawn(args) => commands::spawn::run(client, args, cli.json).await,
        Command::Attach(args) => commands::attach::run(client, args).await,
        Command::Handoff(args) => commands::handoff::run(client, args, cli.json).await,
        Command::Ls(args) => commands::ls::run(client, args, cli.json).await,
        Command::Ping => run_ping(client, cli.json).await,
        Command::Status => run_status(client, cli.json).await,
        Command::PtyHost => unreachable!("handled above"),
    }
}

async fn run_ping(client: RpcClient, json: bool) -> Result<()> {
    let res: DaemonPingResult = client
        .call(rpc_methods::DAEMON_PING, ())
        .await
        .context("daemon.ping RPC failed")?;
    if json {
        println!("{}", serde_json::to_string_pretty(&res)?);
    } else {
        println!(
            "ok={} boot_id={} ts_ms={}",
            res.ok, res.boot_id, res.ts_ms.0
        );
    }
    Ok(())
}

async fn run_status(client: RpcClient, json: bool) -> Result<()> {
    let res: DaemonStatusResult = client
        .call(rpc_methods::DAEMON_STATUS, ())
        .await
        .context("daemon.status RPC failed")?;
    if json {
        println!("{}", serde_json::to_string_pretty(&res)?);
    } else {
        println!("boot_id: {}", res.boot_id);
        println!("protocol_version: {}", res.protocol_version);
        println!("pid: {}", res.pid);
        println!("uptime_ms: {}", res.uptime_ms);
        println!("current_seq: {}", res.current_seq.0);
        println!("active_threads: {}", res.active_threads);
        println!("active_ptys: {}", res.active_ptys);
        println!("connected_clients: {}", res.connected_clients);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::handoff::{HandoffReasonArg, OwnerKindArg};

    #[test]
    fn parses_spawn_command_with_passthrough_args() {
        let cli = Cli::try_parse_from([
            "spaces",
            "--spaces-dir",
            "/tmp/sp",
            "--json",
            "spawn",
            "claude",
            "--cwd",
            "/repo",
            "--profile",
            "work",
            "--space",
            "main",
            "--title",
            "fix bug",
            "--model",
            "claude-sonnet",
            "--attach",
            "--",
            "--resume",
            "sess_1",
        ])
        .expect("parse spawn");

        assert_eq!(cli.spaces_dir, Some(PathBuf::from("/tmp/sp")));
        assert!(cli.json);
        match cli.command {
            Command::Spawn(args) => {
                assert_eq!(args.agent, "claude");
                assert_eq!(args.cwd, Some(PathBuf::from("/repo")));
                assert_eq!(args.profile.as_deref(), Some("work"));
                assert_eq!(args.space.as_deref(), Some("main"));
                assert_eq!(args.title.as_deref(), Some("fix bug"));
                assert_eq!(args.model.as_deref(), Some("claude-sonnet"));
                assert!(args.attach);
                assert_eq!(args.args, vec!["--resume", "sess_1"]);
            }
            other => panic!("unexpected command: {other:?}"),
        }
    }

    #[test]
    fn parses_attach_variants() {
        let cli = Cli::try_parse_from(["spaces", "attach", "thr_42", "--takeover", "--no-replay"])
            .expect("parse attach takeover");
        match cli.command {
            Command::Attach(args) => {
                assert_eq!(args.target, "thr_42");
                assert!(args.takeover);
                assert!(!args.read_only);
                assert!(args.no_replay);
            }
            other => panic!("unexpected command: {other:?}"),
        }

        let ro = Cli::try_parse_from(["spaces", "attach", "pty_7", "--read-only"])
            .expect("parse attach read-only");
        match ro.command {
            Command::Attach(args) => {
                assert_eq!(args.target, "pty_7");
                assert!(!args.takeover);
                assert!(args.read_only);
                assert!(!args.no_replay);
            }
            other => panic!("unexpected command: {other:?}"),
        }
    }

    #[test]
    fn parses_handoff_subcommand() {
        let cli = Cli::try_parse_from([
            "spaces",
            "handoff",
            "thr_1",
            "--to",
            "agent",
            "--id",
            "coordinator",
            "--force",
            "--reason",
            "agent_claim",
        ])
        .expect("parse handoff");

        match cli.command {
            Command::Handoff(args) => {
                assert_eq!(args.thread_id, "thr_1");
                assert_eq!(args.to, OwnerKindArg::Agent);
                assert_eq!(args.id.as_deref(), Some("coordinator"));
                assert!(args.force);
                assert_eq!(args.reason, Some(HandoffReasonArg::AgentClaim));
            }
            other => panic!("unexpected command: {other:?}"),
        }
    }

    #[test]
    fn parses_ls_ping_status_and_pty_host() {
        let ls = Cli::try_parse_from(["spaces", "ls", "--all", "--json"]).expect("parse ls");
        assert!(ls.json);
        assert!(matches!(ls.command, Command::Ls(LsArgs { all: true })));

        let ping = Cli::try_parse_from(["spaces", "ping"]).expect("parse ping");
        assert!(matches!(ping.command, Command::Ping));

        let status = Cli::try_parse_from(["spaces", "status", "--json"]).expect("parse status");
        assert!(status.json);
        assert!(matches!(status.command, Command::Status));

        let host = Cli::try_parse_from(["spaces", "__pty-host", "--spaces-dir", "/var/sp"])
            .expect("parse __pty-host");
        assert_eq!(host.spaces_dir, Some(PathBuf::from("/var/sp")));
        assert!(matches!(host.command, Command::PtyHost));
    }
}

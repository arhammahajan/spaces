use anyhow::{Context, Result};
use clap::Args;
use serde::Serialize;
use spaces_types::rpc::{BackendSpacesListResult, rpc_methods};
use spaces_types::rpc_core::{LeaseResult, LeaseStatusParams, ThreadListParams, ThreadListResult};
use spaces_types::{Lease, LeaseState, Space, Thread, ThreadState};

use crate::commands::{format_lease_owner, format_lease_state};
use crate::rpc::RpcClient;

#[derive(Debug, Clone, Args)]
pub struct LsArgs {
    /// Include closed threads in the listing
    #[arg(long)]
    pub all: bool,
}

#[derive(Serialize)]
struct LsThreadEntry<'a> {
    thread: &'a Thread,
    #[serde(skip_serializing_if = "Option::is_none")]
    lease: Option<&'a Lease>,
}

#[derive(Serialize)]
struct LsJsonOutput<'a> {
    threads: Vec<LsThreadEntry<'a>>,
    spaces: Vec<Space>,
}

/// Lists managed threads (with lease ownership and spaces) as an aligned table or JSON.
pub async fn run(client: RpcClient, args: LsArgs, json: bool) -> Result<()> {
    let thread_res: ThreadListResult = client
        .call(
            rpc_methods::THREAD_LIST,
            ThreadListParams {
                include_closed: Some(args.all),
                ..Default::default()
            },
        )
        .await
        .context("thread.list RPC failed")?;

    let mut leases: Vec<Option<Lease>> = Vec::with_capacity(thread_res.threads.len());
    for thread in &thread_res.threads {
        let lease = client
            .call::<_, LeaseResult>(
                rpc_methods::LEASE_STATUS,
                LeaseStatusParams {
                    thread_id: thread.id.clone(),
                },
            )
            .await
            .ok()
            .map(|r| r.lease);
        leases.push(lease);
    }

    let spaces = client
        .call::<_, BackendSpacesListResult>(rpc_methods::BACKEND_SPACES_LIST, ())
        .await
        .map(|r| r.spaces)
        .unwrap_or_default();

    if json {
        let entries = thread_res
            .threads
            .iter()
            .zip(leases.iter())
            .map(|(thread, lease)| LsThreadEntry {
                thread,
                lease: lease.as_ref(),
            })
            .collect();
        let output = LsJsonOutput {
            threads: entries,
            spaces,
        };
        println!("{}", serde_json::to_string_pretty(&output)?);
        return Ok(());
    }

    if thread_res.threads.is_empty() {
        println!("No threads found.");
        if !spaces.is_empty() {
            let names: Vec<&str> = spaces.iter().map(|s| s.name.as_str()).collect();
            println!("Spaces: {}", names.join(", "));
        }
        return Ok(());
    }

    let rows: Vec<[String; 7]> = thread_res
        .threads
        .iter()
        .zip(leases.iter())
        .map(|(t, l)| {
            [
                t.id.0.clone(),
                t.agent.clone(),
                format_thread_state(t.state).to_owned(),
                format_lease(l.as_ref()),
                l.as_ref()
                    .map(|lease| lease.epoch.0)
                    .unwrap_or(t.epoch.0)
                    .to_string(),
                t.pty_id
                    .as_ref()
                    .map(|p| p.0.clone())
                    .unwrap_or_else(|| "-".to_owned()),
                t.cwd.clone(),
            ]
        })
        .collect();

    print!("{}", format_table(&rows));
    Ok(())
}

fn format_table(rows: &[[String; 7]]) -> String {
    const HEADERS: [&str; 7] = ["THREAD", "AGENT", "STATE", "LEASE", "EPOCH", "PTY", "CWD"];
    let mut widths = HEADERS.map(str::len);
    for row in rows {
        for (i, cell) in row.iter().enumerate() {
            widths[i] = widths[i].max(cell.len());
        }
    }

    let mut out = String::new();
    append_row(&mut out, &HEADERS, &widths);
    for row in rows {
        let refs = [
            row[0].as_str(),
            row[1].as_str(),
            row[2].as_str(),
            row[3].as_str(),
            row[4].as_str(),
            row[5].as_str(),
            row[6].as_str(),
        ];
        append_row(&mut out, &refs, &widths);
    }
    out
}

fn append_row(out: &mut String, cols: &[&str; 7], widths: &[usize; 7]) {
    for i in 0..cols.len() {
        if i > 0 {
            out.push_str("  ");
        }
        if i + 1 == cols.len() {
            out.push_str(cols[i]);
        } else {
            out.push_str(&format!("{:<width$}", cols[i], width = widths[i]));
        }
    }
    out.push('\n');
}

fn format_thread_state(state: ThreadState) -> &'static str {
    match state {
        ThreadState::Created => "created",
        ThreadState::Active => "active",
        ThreadState::Idle => "idle",
        ThreadState::Draining => "draining",
        ThreadState::Suspended => "suspended",
        ThreadState::Closed => "closed",
        ThreadState::Failed => "failed",
    }
}

fn format_lease(lease: Option<&Lease>) -> String {
    let Some(lease) = lease else {
        return "-".to_owned();
    };
    match lease.state {
        LeaseState::Owned => lease
            .owner
            .as_ref()
            .map(format_lease_owner)
            .unwrap_or_else(|| "owned".to_owned()),
        other => format_lease_state(other).to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn formats_aligned_table_with_expected_columns() {
        let rows = vec![
            [
                "thr_1".to_owned(),
                "claude".to_owned(),
                "active".to_owned(),
                "human:alice".to_owned(),
                "2".to_owned(),
                "pty_1".to_owned(),
                "/home/alice/repo".to_owned(),
            ],
            [
                "thr_longer_id".to_owned(),
                "codex".to_owned(),
                "idle".to_owned(),
                "unowned".to_owned(),
                "10".to_owned(),
                "-".to_owned(),
                "/tmp".to_owned(),
            ],
        ];
        let table = format_table(&rows);
        let lines: Vec<&str> = table.lines().collect();
        assert_eq!(lines.len(), 3);
        assert!(lines[0].starts_with("THREAD"));
        for col in ["AGENT", "STATE", "LEASE", "EPOCH", "PTY", "CWD"] {
            assert!(lines[0].contains(col), "missing header {col}");
        }
        assert!(lines[1].contains("human:alice"));
        assert!(lines[2].contains("thr_longer_id"));
    }
}

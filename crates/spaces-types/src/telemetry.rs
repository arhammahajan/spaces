use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::TimestampMs;
use crate::model::Status;

/// Identifies how a coding agent session was launched.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum LaunchSurface {
    #[default]
    Cli,
    App,
    Ide,
}

impl LaunchSurface {
    /// Returns the compact launch surface indicator: `""` for CLI, `"a"` for desktop app, `"i"` for IDE extension.
    pub const fn short_suffix(self) -> &'static str {
        match self {
            Self::Cli => "",
            Self::App => "a",
            Self::Ide => "i",
        }
    }
}

/// Normalized token accounting for a single agent session.
#[derive(Debug, Clone, Copy, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct TokenUsage {
    #[serde(default)]
    #[ts(type = "number")]
    pub input_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub output_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub reasoning_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub cache_read_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub cache_write_tokens: i64,
    #[serde(default)]
    pub cost_usd: f64,
    #[serde(default)]
    pub cost_priced: bool,
}

impl TokenUsage {
    /// Returns all accounted tokens across input, output, reasoning, and cache.
    pub const fn total(&self) -> i64 {
        self.input_tokens
            .saturating_add(self.output_tokens)
            .saturating_add(self.reasoning_tokens)
            .saturating_add(self.cache_read_tokens)
            .saturating_add(self.cache_write_tokens)
    }

    /// Returns non-cached-read tokens (`input + output + reasoning + cache_write`).
    pub const fn active(&self) -> i64 {
        self.input_tokens
            .saturating_add(self.output_tokens)
            .saturating_add(self.reasoning_tokens)
            .saturating_add(self.cache_write_tokens)
    }

    /// Reports whether any token or cost field is non-zero.
    pub fn has_data(&self) -> bool {
        self.input_tokens > 0
            || self.output_tokens > 0
            || self.reasoning_tokens > 0
            || self.cache_read_tokens > 0
            || self.cache_write_tokens > 0
            || self.cost_usd > 0.0
    }
}

/// Single rolling rate-limit window (e.g. 5h or 7d).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct QuotaWindow {
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub window_minutes: u32,
    #[serde(default)]
    pub used_pct: f64,
    #[serde(default)]
    #[ts(type = "number")]
    pub resets_at: i64,
}

impl QuotaWindow {
    /// Returns the remaining quota percentage clamped to `[0.0, 100.0]`.
    pub fn remaining_pct(&self) -> f64 {
        (100.0 - self.used_pct).clamp(0.0, 100.0)
    }
}

/// Account-level remaining quota for an agent provider.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct QuotaInfo {
    pub agent: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub plan_type: String,
    #[serde(default)]
    pub estimated: bool,
    #[serde(default)]
    pub primary: Option<QuotaWindow>,
    #[serde(default)]
    pub secondary: Option<QuotaWindow>,
    #[serde(default)]
    #[ts(type = "number")]
    pub updated_at: i64,
}

impl QuotaInfo {
    /// Returns the highest used percentage across available quota windows.
    pub fn max_used_pct(&self) -> f64 {
        let mut max: f64 = 0.0;
        if let Some(primary) = self.primary.as_ref() {
            max = max.max(primary.used_pct);
        }
        if let Some(secondary) = self.secondary.as_ref() {
            max = max.max(secondary.used_pct);
        }
        max
    }
}

/// Single session/rollout thread handled by a live MCP server.
#[allow(clippy::upper_case_acronyms)]
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct LiveMCPThread {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub updated_at: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub size_bytes: i64,
    #[serde(default)]
    pub active: bool,
}

/// Running agent MCP server process (e.g. `codex mcp-server`).
#[allow(clippy::upper_case_acronyms)]
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct LiveMCPServer {
    pub agent: String,
    pub pid: u32,
    #[serde(default)]
    pub ppid: u32,
    #[serde(default)]
    pub parent_cli: String,
    #[serde(default)]
    pub profile: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub mem_kb: i64,
    #[serde(default)]
    pub active_threads: u32,
    #[serde(default)]
    pub total_threads: u32,
    #[serde(default)]
    #[ts(type = "number")]
    pub last_activity_at: i64,
    #[serde(default)]
    pub threads: Vec<LiveMCPThread>,
}

/// Classifies per-session context window pressure.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum ContextFillWarning {
    #[default]
    None,
    High,
    Crit,
}

impl ContextFillWarning {
    /// Returns the compact TUI warning glyph (`""`, `"!"`, or `"⚠"`).
    pub const fn glyph(self) -> &'static str {
        match self {
            Self::None => "",
            Self::High => "!",
            Self::Crit => "⚠",
        }
    }
}

/// Summarizes a single session's context occupancy.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ContextSessionEntry {
    pub session_key: String,
    #[serde(default)]
    pub pane_id: String,
    #[serde(default)]
    pub window_id: String,
    pub project: String,
    pub agent: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub context_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub context_window: i64,
    #[serde(default)]
    pub context_pct: f64,
    #[serde(default)]
    pub warning: ContextFillWarning,
    #[serde(default)]
    pub compaction_count: u32,
}

/// Aggregate context and token rate data.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct TelemetryContext {
    #[serde(default)]
    pub tokens_per_min: f64,
    #[serde(default)]
    pub rate_history: Vec<f64>,
    #[serde(default)]
    #[ts(type = "number")]
    pub total_tokens: i64,
    #[serde(default)]
    pub total_cost_usd: f64,
    #[serde(default)]
    #[ts(type = "number")]
    pub total_mem_kb: i64,
    #[serde(default)]
    pub avg_context_pct: f64,
    #[serde(default)]
    pub active_sessions: u32,
    #[serde(default)]
    pub sessions: Vec<ContextSessionEntry>,
}

/// Listening TCP port opened by an agent child process.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct PortEntry {
    pub port: u16,
    pub pid: u32,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub project: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub pane_id: String,
    #[serde(default)]
    pub window_id: String,
    #[serde(default)]
    pub duplicate: bool,
}

/// Listening port still open after its owning agent session ended.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct OrphanPort {
    pub port: u16,
    pub pid: u32,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub project: String,
    #[serde(default)]
    pub session_id: String,
}

/// Groups monitored sessions by project with git status.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ProjectSummary {
    pub name: String,
    #[serde(default)]
    pub repo_root: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub has_git: bool,
    #[serde(default)]
    pub files_added: u32,
    #[serde(default)]
    pub files_modified: u32,
    #[serde(default)]
    pub is_clean: bool,
    #[serde(default)]
    pub session_count: u32,
    #[serde(default)]
    pub active_count: u32,
    #[serde(default)]
    pub worktree_count: u32,
}

/// Normalized per-session telemetry record.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct SessionRow {
    pub key: String,
    #[serde(default)]
    pub space: String,
    #[serde(default)]
    pub window_id: String,
    #[serde(default)]
    pub window_index: u32,
    #[serde(default)]
    pub pane_id: String,
    pub agent: String,
    #[serde(default)]
    pub launch_surface: LaunchSurface,
    #[serde(default)]
    pub pid: u32,
    #[serde(default)]
    pub agent_pid: u32,
    pub project: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub profile: String,
    #[serde(default)]
    pub config_root: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub current_task: String,
    #[serde(default)]
    pub status: Status,
    #[serde(default)]
    pub rate_limited: bool,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub effort: String,
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub usage: TokenUsage,
    #[serde(default)]
    #[ts(type = "number")]
    pub total_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub context_tokens: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub context_window: i64,
    #[serde(default)]
    pub context_pct: f64,
    #[serde(default)]
    pub warning: ContextFillWarning,
    #[serde(default)]
    pub compaction_count: u32,
    #[serde(default)]
    #[ts(type = "number")]
    pub mem_kb: i64,
    #[serde(default)]
    pub turn_count: u32,
    #[serde(default)]
    #[ts(type = "number")]
    pub avg_tokens_per_turn: i64,
    #[serde(default)]
    #[ts(type = "Array<number>")]
    pub token_history: Vec<i64>,
    #[serde(default)]
    #[ts(type = "Array<number>")]
    pub context_history: Vec<i64>,
    #[serde(default)]
    pub memory_files: u32,
    #[serde(default)]
    pub memory_lines: u32,
    #[serde(default)]
    pub ports: Vec<PortEntry>,
    #[serde(default)]
    pub started_at_ms: TimestampMs,
    #[serde(default)]
    pub updated_at_ms: TimestampMs,
}

/// Immutable telemetry snapshot powering telemetry panels and enriched tab inspector.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct TelemetrySnapshot {
    #[serde(default)]
    pub context: TelemetryContext,
    #[serde(default)]
    pub quotas: Vec<QuotaInfo>,
    #[serde(default)]
    pub projects: Vec<ProjectSummary>,
    #[serde(default)]
    pub ports: Vec<PortEntry>,
    #[serde(default)]
    pub orphan_ports: Vec<OrphanPort>,
    #[serde(default)]
    pub sessions: Vec<SessionRow>,
    #[serde(default)]
    pub mcp_servers: Vec<LiveMCPServer>,
    #[serde(default)]
    #[ts(type = "number")]
    pub updated_at: i64,
}

impl TelemetrySnapshot {
    /// Finds the normalized [`SessionRow`] associated with a pane ID.
    pub fn session_for_pane(&self, pane_id: &str) -> Option<&SessionRow> {
        if pane_id.is_empty() {
            return None;
        }
        self.sessions.iter().find(|row| row.pane_id == pane_id)
    }

    /// Returns the [`QuotaInfo`] for the given canonical agent ID, if present.
    pub fn quota_for_agent(&self, agent_id: &str) -> Option<&QuotaInfo> {
        self.quotas.iter().find(|quota| quota.agent == agent_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    fn assert_all_keys_snake_case(value: &Value) {
        match value {
            Value::Object(map) => {
                for (key, val) in map {
                    assert!(
                        key.bytes()
                            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_'),
                        "non-snake_case JSON key: {key}"
                    );
                    assert_all_keys_snake_case(val);
                }
            }
            Value::Array(items) => {
                for item in items {
                    assert_all_keys_snake_case(item);
                }
            }
            _ => {}
        }
    }

    #[test]
    fn telemetry_helpers_and_snapshot_round_trip() {
        let usage = TokenUsage {
            input_tokens: 100,
            output_tokens: 40,
            reasoning_tokens: 10,
            cache_read_tokens: 500,
            cache_write_tokens: 20,
            cost_usd: 0.12,
            cost_priced: true,
        };
        assert_eq!(usage.total(), 670);
        assert_eq!(usage.active(), 170);
        assert!(usage.has_data());

        let quota = QuotaInfo {
            agent: "claude".into(),
            source: "api".into(),
            plan_type: "max".into(),
            estimated: false,
            primary: Some(QuotaWindow {
                label: "5h".into(),
                window_minutes: 300,
                used_pct: 40.0,
                resets_at: 1_700_000_000,
            }),
            secondary: Some(QuotaWindow {
                label: "7d".into(),
                window_minutes: 10_080,
                used_pct: 75.0,
                resets_at: 1_700_500_000,
            }),
            updated_at: 1_700_000_000,
        };
        assert_eq!(quota.max_used_pct(), 75.0);
        assert_eq!(quota.primary.as_ref().unwrap().remaining_pct(), 60.0);
        assert_eq!(
            QuotaWindow {
                used_pct: 120.0,
                ..QuotaWindow::default()
            }
            .remaining_pct(),
            0.0
        );

        let snap = TelemetrySnapshot {
            context: TelemetryContext {
                tokens_per_min: 1200.5,
                rate_history: vec![100.0, 200.0],
                total_tokens: 670,
                total_cost_usd: 0.12,
                total_mem_kb: 4096,
                avg_context_pct: 55.0,
                active_sessions: 1,
                sessions: vec![ContextSessionEntry {
                    session_key: "claude:1".into(),
                    pane_id: "%5".into(),
                    window_id: "@1".into(),
                    project: "spaces".into(),
                    agent: "claude".into(),
                    context_tokens: 110_000,
                    context_window: 200_000,
                    context_pct: 55.0,
                    warning: ContextFillWarning::High,
                    compaction_count: 1,
                }],
            },
            quotas: vec![quota],
            projects: vec![ProjectSummary {
                name: "spaces".into(),
                repo_root: "/home/dev/spaces".into(),
                cwd: "/home/dev/spaces".into(),
                branch: "main".into(),
                has_git: true,
                files_added: 2,
                files_modified: 3,
                is_clean: false,
                session_count: 1,
                active_count: 1,
                worktree_count: 2,
            }],
            ports: vec![PortEntry {
                port: 3000,
                pid: 4242,
                command: "node".into(),
                project: "spaces".into(),
                session_id: "s1".into(),
                pane_id: "%5".into(),
                window_id: "@1".into(),
                duplicate: false,
            }],
            orphan_ports: vec![OrphanPort {
                port: 8080,
                pid: 9999,
                command: "python".into(),
                project: "spaces".into(),
                session_id: "old".into(),
            }],
            sessions: vec![SessionRow {
                key: "%5".into(),
                space: "spaces".into(),
                window_id: "@1".into(),
                window_index: 0,
                pane_id: "%5".into(),
                agent: "claude".into(),
                launch_surface: LaunchSurface::Ide,
                pid: 100,
                agent_pid: 101,
                project: "spaces".into(),
                cwd: "/home/dev/spaces".into(),
                session_id: "s1".into(),
                profile: "work".into(),
                config_root: "/home/.spaces".into(),
                summary: "Porting types".into(),
                current_task: "Writing telemetry.rs".into(),
                status: Status::Working,
                rate_limited: false,
                model: "claude-opus".into(),
                effort: "high".into(),
                version: "1.0.0".into(),
                usage,
                total_tokens: 670,
                context_tokens: 110_000,
                context_window: 200_000,
                context_pct: 55.0,
                warning: ContextFillWarning::High,
                compaction_count: 1,
                mem_kb: 4096,
                turn_count: 4,
                avg_tokens_per_turn: 167,
                token_history: vec![100, 200, 370],
                context_history: vec![20_000, 60_000, 110_000],
                memory_files: 2,
                memory_lines: 40,
                ports: vec![],
                started_at_ms: TimestampMs(1_700_000_000_000),
                updated_at_ms: TimestampMs(1_700_000_010_000),
            }],
            mcp_servers: vec![LiveMCPServer {
                agent: "codex".into(),
                pid: 2020,
                ppid: 1,
                parent_cli: "codex".into(),
                profile: "default".into(),
                command: "codex mcp-server".into(),
                mem_kb: 8192,
                active_threads: 1,
                total_threads: 2,
                last_activity_at: 1_700_000_005,
                threads: vec![LiveMCPThread {
                    path: "/tmp/rollout.jsonl".into(),
                    updated_at: 1_700_000_005,
                    size_bytes: 1024,
                    active: true,
                }],
            }],
            updated_at: 1_700_000_010,
        };

        assert_eq!(snap.session_for_pane("%5").unwrap().agent, "claude");
        assert!(snap.session_for_pane("").is_none());
        assert_eq!(snap.quota_for_agent("claude").unwrap().plan_type, "max");
        assert_eq!(LaunchSurface::Ide.short_suffix(), "i");
        assert_eq!(ContextFillWarning::High.glyph(), "!");

        let val = serde_json::to_value(&snap).unwrap();
        assert_all_keys_snake_case(&val);
        let rt: TelemetrySnapshot = serde_json::from_value(val).unwrap();
        assert_eq!(rt, snap);
    }
}

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::TimestampMs;

/// Attention status for a pane or space, ordered by display priority (lower = more urgent).
#[derive(
    Debug, Clone, Copy, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize, TS,
)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum Status {
    NeedsYou,
    Failed,
    Done,
    Working,
    Quiet,
    NewOutput,
    #[default]
    Idle,
}

impl Status {
    /// Returns the sort priority (lower = more urgent).
    pub const fn priority(self) -> u8 {
        match self {
            Self::NeedsYou => 0,
            Self::Failed => 1,
            Self::Done => 2,
            Self::Working => 3,
            Self::Quiet => 4,
            Self::NewOutput => 5,
            Self::Idle => 6,
        }
    }

    /// Returns the glyph for the status.
    pub const fn icon(self) -> &'static str {
        match self {
            Self::NeedsYou => "●",
            Self::Failed => "✗",
            Self::Done => "✓",
            Self::Working => "◐",
            Self::Quiet => "◌",
            Self::NewOutput => "•",
            Self::Idle => "○",
        }
    }

    /// Returns the human-readable label for the status.
    pub const fn label(self) -> &'static str {
        match self {
            Self::NeedsYou => "needs you",
            Self::Failed => "failed",
            Self::Done => "done",
            Self::Working => "working",
            Self::Quiet => "quiet",
            Self::NewOutput => "new output",
            Self::Idle => "idle",
        }
    }

    /// Reports whether the status requires immediate user attention.
    pub const fn is_attention(self) -> bool {
        matches!(self, Self::NeedsYou | Self::Failed)
    }
}

/// Filters visible panes and spaces in the navigator.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum AttentionFilter {
    #[default]
    All,
    Needs,
    Working,
    Done,
    Idle,
}

impl AttentionFilter {
    /// Returns the filter pill label.
    pub const fn label(self) -> &'static str {
        match self {
            Self::All => "all",
            Self::Needs => "blocked",
            Self::Working => "working",
            Self::Done => "done",
            Self::Idle => "idle",
        }
    }

    /// Reports whether `status` satisfies this filter.
    pub const fn matches(self, status: Status) -> bool {
        match self {
            Self::All => true,
            Self::Needs => status.is_attention(),
            Self::Working => matches!(status, Status::Working),
            Self::Done => matches!(status, Status::Done),
            Self::Idle => matches!(status, Status::Idle),
        }
    }

    /// Returns the ordered list of attention filters.
    pub const fn all() -> &'static [Self] {
        &[
            Self::All,
            Self::Needs,
            Self::Working,
            Self::Done,
            Self::Idle,
        ]
    }
}

/// Tallies panes by [`Status`].
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct Counts {
    #[serde(default)]
    pub needs: u32,
    #[serde(default)]
    pub failed: u32,
    #[serde(default)]
    pub working: u32,
    #[serde(default)]
    pub done: u32,
    #[serde(default)]
    pub quiet: u32,
    #[serde(default)]
    pub new_output: u32,
    #[serde(default)]
    pub idle: u32,
}

impl Counts {
    /// Increments the counter corresponding to `status`.
    pub const fn add(&mut self, status: Status) {
        match status {
            Status::NeedsYou => self.needs = self.needs.saturating_add(1),
            Status::Failed => self.failed = self.failed.saturating_add(1),
            Status::Done => self.done = self.done.saturating_add(1),
            Status::Working => self.working = self.working.saturating_add(1),
            Status::Quiet => self.quiet = self.quiet.saturating_add(1),
            Status::NewOutput => self.new_output = self.new_output.saturating_add(1),
            Status::Idle => self.idle = self.idle.saturating_add(1),
        }
    }

    /// Returns `needs + failed`.
    pub const fn attention_total(&self) -> u32 {
        self.needs.saturating_add(self.failed)
    }

    /// Returns the highest-priority status represented in these counts.
    pub const fn rollup(&self) -> Status {
        if self.needs > 0 {
            Status::NeedsYou
        } else if self.failed > 0 {
            Status::Failed
        } else if self.done > 0 {
            Status::Done
        } else if self.working > 0 {
            Status::Working
        } else if self.quiet > 0 {
            Status::Quiet
        } else if self.new_output > 0 {
            Status::NewOutput
        } else {
            Status::Idle
        }
    }
}

/// Token and estimated cost telemetry reported by a passive telemetry source.
#[derive(Debug, Clone, Copy, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookUsage {
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
}

impl HookUsage {
    /// Returns the sum of input, output, and reasoning tokens.
    pub const fn total_tokens(&self) -> i64 {
        self.input_tokens
            .saturating_add(self.output_tokens)
            .saturating_add(self.reasoning_tokens)
    }

    /// Reports whether any token count or cost value is populated.
    pub fn has_data(&self) -> bool {
        self.input_tokens > 0
            || self.output_tokens > 0
            || self.reasoning_tokens > 0
            || self.cache_read_tokens > 0
            || self.cache_write_tokens > 0
            || self.cost_usd > 0.0
    }
}

/// Latest tool invocation recorded in [`HookDetail`].
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookTool {
    pub name: String,
    pub input: String,
    #[serde(default)]
    pub output: String,
}

/// Subagent, background command, service, watcher, or scheduled wakeup associated with a pane.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookChild {
    pub id: String,
    #[serde(rename = "type")]
    pub child_type: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub category: String,
    pub state: String,
    #[serde(default)]
    pub outcome: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub schedule: String,
    pub since_ms: TimestampMs,
    #[serde(default)]
    pub ts_ms: TimestampMs,
    #[serde(default)]
    pub ended_ms: TimestampMs,
    pub text: String,
}

impl HookChild {
    /// Returns the lifecycle category (`"finite"`, `"service"`, `"watcher"`, or `"schedule"`).
    pub fn effective_category(&self) -> &str {
        match self.category.trim() {
            "service" | "watcher" | "schedule" | "finite" => self.category.trim(),
            _ if self.category.trim().eq_ignore_ascii_case("service") => "service",
            _ if self.category.trim().eq_ignore_ascii_case("watcher") => "watcher",
            _ if self.category.trim().eq_ignore_ascii_case("schedule") => "schedule",
            _ => "finite",
        }
    }

    /// Returns the item role (`"subagent"`, `"command"`, `"service"`, `"watcher"`, or `"schedule"`).
    pub fn effective_role(&self) -> &str {
        let trimmed = self.kind.trim();
        if !trimmed.is_empty() {
            return trimmed;
        }
        match self.effective_category() {
            "service" => "service",
            "watcher" => "watcher",
            "schedule" => "schedule",
            _ => "subagent",
        }
    }

    /// Reports whether this background item is still running.
    pub fn is_active(&self) -> bool {
        matches!(
            self.state.trim().to_ascii_lowercase().as_str(),
            "" | "working"
                | "running"
                | "pending"
                | "in_progress"
                | "started"
                | "active"
                | "scheduled"
                | "waiting"
        )
    }

    /// Reports whether this item is finite work that blocks turn completion.
    pub fn is_finite(&self) -> bool {
        self.effective_category() == "finite"
    }

    /// Returns the item's elapsed runtime in milliseconds.
    pub fn elapsed_ms(&self, now_ms: TimestampMs) -> i64 {
        if self.since_ms.0 <= 0 {
            return 0;
        }
        if !self.is_active() {
            let end = if self.ended_ms.0 > 0 {
                self.ended_ms.0
            } else {
                self.ts_ms.0
            };
            return end.saturating_sub(self.since_ms.0).max(0);
        }
        now_ms.0.saturating_sub(self.since_ms.0).max(0)
    }
}

/// Per-pane summary returned by `spaces __space-hooks`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookSummary {
    pub agent: String,
    pub app: String,
    pub state: String,
    #[serde(default)]
    pub kind: String,
    pub event: String,
    #[serde(default)]
    pub source: String,
    pub ts_ms: TimestampMs,
    pub state_since_ms: TimestampMs,
    pub turn_started_ms: TimestampMs,
    pub text: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub usage: Option<HookUsage>,
    #[serde(default)]
    pub child_count: u32,
    #[serde(default)]
    pub background: u32,
    #[serde(default)]
    pub finite_active: u32,
    #[serde(default)]
    pub service_active: u32,
    #[serde(default)]
    pub waiting_on: String,
    #[serde(default)]
    pub children: Vec<HookChild>,
    #[serde(default)]
    pub background_tasks: Vec<HookChild>,
    #[serde(default)]
    pub has_detail: bool,
}

impl HookSummary {
    /// Reports whether this hook represents an auth-required block.
    pub fn is_auth_block(&self) -> bool {
        self.state == "blocked" && self.kind == "auth"
    }

    /// Reports whether the pane has any tracked background work.
    pub fn has_background_work(&self) -> bool {
        !self.children.is_empty()
            || !self.background_tasks.is_empty()
            || self.child_count > 0
            || self.background > 0
    }

    /// Reports whether the pane has actively running finite background work or is waiting on background tasks.
    pub fn has_active_finite_work(&self) -> bool {
        if self.finite_active > 0 || !self.waiting_on.trim().is_empty() {
            return true;
        }
        self.children
            .iter()
            .chain(self.background_tasks.iter())
            .any(|item| item.is_active() && item.is_finite())
    }
}

/// Full record returned by `spaces __space-hooks --detail`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookDetail {
    #[serde(flatten)]
    pub summary: HookSummary,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub tool: Option<HookTool>,
    #[serde(default)]
    pub reply: String,
}

/// One recent completion or failure recorded across spaces.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct OutcomeEntry {
    pub session: String,
    #[serde(default)]
    pub pane: String,
    pub agent: String,
    #[serde(default)]
    pub app: String,
    #[serde(default)]
    pub profile: String,
    pub outcome: String,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub cwd: String,
    pub ts_ms: TimestampMs,
}

impl OutcomeEntry {
    /// Maps `outcome` to [`Status::Failed`] or [`Status::Done`].
    pub fn status(&self) -> Status {
        if self.outcome == "failed" {
            Status::Failed
        } else {
            Status::Done
        }
    }

    /// Returns the display label for the outcome's agent.
    pub fn display_app(&self) -> &str {
        let app = self.app.trim();
        if !app.is_empty() {
            app
        } else {
            self.agent.trim()
        }
    }
}

/// Top-level JSON snapshot returned by `spaces __space-hooks`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookSnapshot {
    pub ok: bool,
    pub version: u32,
    pub now_ms: TimestampMs,
    pub spool_pending: u32,
    #[serde(default)]
    pub panes: BTreeMap<String, BTreeMap<String, HookSummary>>,
    #[serde(default)]
    pub outcomes: Vec<OutcomeEntry>,
}

/// Top-level JSON snapshot returned by `spaces __space-hooks --detail`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HookDetailSnapshot {
    pub ok: bool,
    pub version: u32,
    #[serde(default)]
    pub record: Option<HookDetail>,
}

/// Configured profile for a launchable harness in `~/.spaces/harnesses.json`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HarnessProfile {
    pub name: String,
    #[serde(default)]
    pub display_name: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub agent: String,
}

/// Launchable agent entry from `~/.spaces/harnesses.json`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HarnessEntry {
    pub name: String,
    #[serde(default)]
    pub binary: String,
    #[serde(default)]
    pub binary_state: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub profiles: Vec<HarnessProfile>,
}

impl HarnessEntry {
    /// Reports whether this harness has a working default binary or at least one configured profile.
    pub fn is_launchable(&self) -> bool {
        if !self.profiles.is_empty() {
            return true;
        }
        let state = self.binary_state.trim().to_ascii_lowercase();
        state.is_empty() || state == "ok"
    }
}

/// JSON inventory stored at `~/.spaces/harnesses.json`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct HarnessFile {
    #[serde(default)]
    pub version: u32,
    #[serde(default)]
    pub schema_version: u32,
    #[serde(default)]
    pub generated_at_ms: TimestampMs,
    #[serde(default)]
    pub agents: Vec<HarnessEntry>,
}

/// One window inside a [`Space`].
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct Window {
    pub id: String,
    #[serde(default)]
    pub index: i32,
    pub name: String,
    #[serde(default)]
    pub active: bool,
    #[serde(default)]
    pub last: bool,
    #[serde(default)]
    pub zoomed: bool,
    #[serde(default)]
    pub bell: bool,
    #[serde(default)]
    pub activity: bool,
    #[serde(default)]
    pub silence: bool,
    #[serde(default)]
    pub width: u32,
    #[serde(default)]
    pub height: u32,
    #[serde(default)]
    pub activity_at_ms: TimestampMs,
    #[serde(default)]
    pub layout: String,
    #[serde(default)]
    pub worktree_path: String,
    #[serde(default)]
    pub worktree_name: String,
    #[serde(default)]
    pub worktree_base: String,
    #[serde(default)]
    pub worktree_managed: bool,
}

/// Aggregated CI/CD status for a PR or branch commit.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum CheckState {
    #[default]
    None,
    Success,
    Failure,
    Pending,
}

/// Aggregated CI check status and optional metadata.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct CheckSummary {
    #[serde(default)]
    pub state: CheckState,
    #[serde(default)]
    pub passed: u32,
    #[serde(default)]
    pub total: u32,
    #[serde(default)]
    pub failing_name: String,
}

/// GitHub pull request metadata for a branch.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct PRSummary {
    pub number: u32,
    #[serde(default)]
    pub title: String,
    pub state: String,
    #[serde(default)]
    pub is_draft: bool,
    #[serde(default)]
    pub url: String,
}

/// Per-directory git branch, worktree, diff stats, and GitHub PR/CI status.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct GitInfo {
    #[serde(default)]
    pub repo_key: String,
    #[serde(default)]
    pub repo_root: String,
    #[serde(default)]
    pub main_repo_root: String,
    #[serde(default)]
    pub repo_name: String,
    #[serde(default)]
    pub is_worktree: bool,
    #[serde(default)]
    pub worktree_name: String,
    #[serde(default)]
    pub managed: bool,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default)]
    pub ahead: u32,
    #[serde(default)]
    pub behind: u32,
    #[serde(default)]
    pub is_dirty: bool,
    #[serde(default)]
    pub is_rebasing: bool,
    #[serde(default)]
    pub has_conflict: bool,
    #[serde(default)]
    pub lines_added: u32,
    #[serde(default)]
    pub lines_removed: u32,
    #[serde(default)]
    pub uncommitted_added: u32,
    #[serde(default)]
    pub uncommitted_removed: u32,
    #[serde(default)]
    pub files_added: u32,
    #[serde(default)]
    pub files_modified: u32,
    #[serde(default)]
    pub pr: Option<PRSummary>,
    #[serde(default)]
    pub checks: Option<CheckSummary>,
}

impl GitInfo {
    /// Reports whether branch/PR CI check status should be rendered.
    pub fn should_show_checks(&self) -> bool {
        let Some(checks) = self.checks.as_ref() else {
            return false;
        };
        if checks.state == CheckState::None {
            return false;
        }
        if self.pr.is_some() {
            return true;
        }
        if self.branch == "main" || self.branch == "master" {
            return matches!(checks.state, CheckState::Failure | CheckState::Pending);
        }
        true
    }
}

/// One terminal pane inside a [`Space`].
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct Pane {
    pub id: String,
    #[serde(default)]
    pub numeric_id: i32,
    #[serde(default)]
    pub pane_index: u32,
    #[serde(default)]
    pub window_id: String,
    #[serde(default)]
    pub window_index: u32,
    #[serde(default)]
    pub window_name: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub conversation_title: String,
    #[serde(default)]
    pub base_conv_title: String,
    #[serde(default)]
    pub screen_title: String,
    #[serde(default)]
    pub resume_session: String,
    #[serde(default)]
    pub profile: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub start_command: String,
    #[serde(default)]
    pub foreground_cmd: String,
    #[serde(default)]
    pub launch_command: Vec<String>,
    #[serde(default)]
    pub child_process_count: u32,
    #[serde(default)]
    pub app: String,
    #[serde(default)]
    pub status: Status,
    #[serde(default)]
    pub base_status: Status,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub base_text: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub hook: Option<HookSummary>,
    #[serde(default)]
    pub git: Option<GitInfo>,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub age_ms: i64,
    #[serde(default)]
    #[ts(type = "number")]
    pub base_age_ms: i64,
    #[serde(default)]
    pub width: u32,
    #[serde(default)]
    pub height: u32,
    #[serde(default)]
    pub is_active: bool,
    #[serde(default)]
    pub window_active: bool,
    #[serde(default)]
    pub in_mode: bool,
    #[serde(default)]
    pub synchronized: bool,
    #[serde(default)]
    pub is_dead: bool,
    #[serde(default)]
    pub dead_status: i32,
    #[serde(default)]
    pub has_spaces_meta: bool,
    #[serde(default)]
    pub returned_to_shell: bool,
    #[serde(default)]
    pub pid: u32,
}

/// One workspace session containing windows and panes.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct Space {
    pub name: String,
    pub id: String,
    #[serde(default)]
    pub is_current: bool,
    #[serde(default)]
    pub windows: Vec<Window>,
    #[serde(default)]
    pub panes: Vec<Pane>,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub git: Option<GitInfo>,
    #[serde(default)]
    pub worktree_path: String,
    #[serde(default)]
    pub worktree_name: String,
    #[serde(default)]
    pub worktree_base: String,
    #[serde(default)]
    pub worktree_managed: bool,
    #[serde(default)]
    pub counts: Counts,
    #[serde(default)]
    pub status: Status,
    #[serde(default)]
    pub connected_clients: u32,
    #[serde(default)]
    pub created_at_ms: TimestampMs,
}

/// Persisted space snapshot whose session is not currently running and can be resurrected.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ExitedSpace {
    pub name: String,
    #[serde(default)]
    pub saved_at_ms: TimestampMs,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub window_count: u32,
    #[serde(default)]
    pub pane_count: u32,
    #[serde(default)]
    pub agents: Vec<String>,
}

/// Git worktree metadata for UI display, selection, and lifecycle management.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeEntry {
    pub path: String,
    pub name: String,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub head_sha: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default)]
    pub repo_key: String,
    #[serde(default)]
    pub main_repo_root: String,
    #[serde(default)]
    pub repo_name: String,
    #[serde(default)]
    pub is_main: bool,
    #[serde(default)]
    pub is_current: bool,
    #[serde(default)]
    pub is_detached: bool,
    #[serde(default)]
    pub is_locked: bool,
    #[serde(default)]
    pub is_prunable: bool,
    #[serde(default)]
    pub is_dirty: bool,
    #[serde(default)]
    pub managed: bool,
    #[serde(default)]
    pub name_source: String,
    #[serde(default)]
    pub created_at_ms: TimestampMs,
    #[serde(default)]
    pub unmerged_commits: u32,
    #[serde(default)]
    pub open_spaces: Vec<String>,
    #[serde(default)]
    pub open_window_ids: Vec<String>,
    #[serde(default)]
    pub open_window_count: u32,
    #[serde(default)]
    pub git: Option<GitInfo>,
}

/// Discovered git repository and worktree state for a directory.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeContext {
    #[serde(default)]
    pub in_repo: bool,
    #[serde(default)]
    pub repo_key: String,
    #[serde(default)]
    pub main_repo_root: String,
    #[serde(default)]
    pub repo_name: String,
    #[serde(default)]
    pub worktree_root: String,
    #[serde(default)]
    pub default_base: String,
    #[serde(default)]
    pub current_branch: String,
    #[serde(default)]
    pub current_path: String,
    #[serde(default)]
    pub has_commits: bool,
    #[serde(default)]
    pub worktrees: Vec<WorktreeEntry>,
    #[serde(default)]
    pub branches: Vec<String>,
    #[serde(default)]
    pub auto_branch: String,
    #[serde(default)]
    pub auto_dir_name: String,
    #[serde(default)]
    pub auto_source: String,
    #[serde(default)]
    pub auto_rule: String,
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
                    if key == "panes"
                        && let Value::Object(sessions) = val
                    {
                        // `HookSnapshot.panes` is keyed dynamically by session name -> pane ID.
                        for session_map in sessions.values() {
                            if let Value::Object(pane_entries) = session_map {
                                for hook_summary in pane_entries.values() {
                                    assert_all_keys_snake_case(hook_summary);
                                }
                            }
                        }
                        continue;
                    }
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
    fn status_helpers_and_serde() {
        assert_eq!(Status::NeedsYou.priority(), 0);
        assert_eq!(Status::Idle.priority(), 6);
        assert!(Status::NeedsYou.is_attention());
        assert!(Status::Failed.is_attention());
        assert!(!Status::Working.is_attention());

        let json = serde_json::to_string(&Status::NewOutput).unwrap();
        assert_eq!(json, "\"new_output\"");
        let parsed: Status = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed, Status::NewOutput);
    }

    #[test]
    fn counts_add_attention_total_and_rollup() {
        let mut counts = Counts::default();
        assert_eq!(counts.rollup(), Status::Idle);
        counts.add(Status::NewOutput);
        assert_eq!(counts.rollup(), Status::NewOutput);
        counts.add(Status::Working);
        assert_eq!(counts.rollup(), Status::Working);
        counts.add(Status::Failed);
        assert_eq!(counts.rollup(), Status::Failed);
        counts.add(Status::NeedsYou);
        assert_eq!(counts.rollup(), Status::NeedsYou);
        assert_eq!(counts.attention_total(), 2);

        let val = serde_json::to_value(counts).unwrap();
        assert_all_keys_snake_case(&val);
        let roundtrip: Counts = serde_json::from_value(val).unwrap();
        assert_eq!(roundtrip, counts);
    }

    #[test]
    fn attention_filter_round_trip() {
        for filter in AttentionFilter::all() {
            let json = serde_json::to_value(filter).unwrap();
            let decoded: AttentionFilter = serde_json::from_value(json).unwrap();
            assert_eq!(&decoded, filter);
        }
        assert!(AttentionFilter::Needs.matches(Status::Failed));
        assert!(!AttentionFilter::Needs.matches(Status::Done));
    }

    #[test]
    fn hook_detail_flattens_cleanly_and_round_trips() {
        let mut panes = BTreeMap::new();
        let mut session_panes = BTreeMap::new();
        let summary = HookSummary {
            agent: "codex".into(),
            app: "Codex".into(),
            state: "working".into(),
            kind: "".into(),
            event: "PreToolUse".into(),
            source: "hook".into(),
            ts_ms: TimestampMs(1_700_000_000_000),
            state_since_ms: TimestampMs(1_700_000_000_000),
            turn_started_ms: TimestampMs(1_700_000_000_000),
            text: "Running tests".into(),
            title: "Fix tests".into(),
            model: "gpt-5".into(),
            session_id: "sess-1".into(),
            cwd: "/tmp/repo".into(),
            usage: Some(HookUsage {
                input_tokens: 100,
                output_tokens: 50,
                reasoning_tokens: 10,
                cache_read_tokens: 20,
                cache_write_tokens: 5,
                cost_usd: 0.02,
            }),
            child_count: 1,
            background: 1,
            finite_active: 1,
            service_active: 0,
            waiting_on: "subagent".into(),
            children: vec![HookChild {
                id: "c1".into(),
                child_type: "subagent".into(),
                kind: "subagent".into(),
                category: "finite".into(),
                state: "working".into(),
                outcome: String::new(),
                command: String::new(),
                schedule: String::new(),
                since_ms: TimestampMs(1_700_000_000_000),
                ts_ms: TimestampMs(1_700_000_000_100),
                ended_ms: TimestampMs(0),
                text: "Checking".into(),
            }],
            background_tasks: vec![],
            has_detail: true,
        };
        assert!(summary.has_background_work());
        assert!(summary.has_active_finite_work());
        assert!(summary.usage.as_ref().unwrap().has_data());
        assert_eq!(summary.usage.as_ref().unwrap().total_tokens(), 160);
        assert_eq!(
            summary.children[0].elapsed_ms(TimestampMs(1_700_000_000_500)),
            500
        );

        let detail = HookDetail {
            summary: summary.clone(),
            prompt: "Run cargo test".into(),
            tool: Some(HookTool {
                name: "bash".into(),
                input: "cargo test".into(),
                output: "ok".into(),
            }),
            reply: "All passed".into(),
        };
        let detail_json = serde_json::to_value(&detail).unwrap();
        assert_eq!(detail_json["agent"], "codex");
        assert_eq!(detail_json["prompt"], "Run cargo test");
        assert!(detail_json.get("summary").is_none());
        assert_all_keys_snake_case(&detail_json);
        let detail_rt: HookDetail = serde_json::from_value(detail_json).unwrap();
        assert_eq!(detail_rt, detail);

        session_panes.insert("%1".into(), summary);
        panes.insert("dev".into(), session_panes);
        let snap = HookSnapshot {
            ok: true,
            version: 1,
            now_ms: TimestampMs(1_700_000_001_000),
            spool_pending: 0,
            panes,
            outcomes: vec![OutcomeEntry {
                session: "dev".into(),
                pane: "%1".into(),
                agent: "codex".into(),
                app: "Codex".into(),
                profile: "default".into(),
                outcome: "done".into(),
                text: "Done".into(),
                title: "Task".into(),
                model: "gpt-5".into(),
                session_id: "sess-1".into(),
                cwd: "/tmp/repo".into(),
                ts_ms: TimestampMs(1_700_000_001_000),
            }],
        };
        let snap_val = serde_json::to_value(&snap).unwrap();
        assert_all_keys_snake_case(&snap_val);
        let snap_rt: HookSnapshot = serde_json::from_value(snap_val).unwrap();
        assert_eq!(snap_rt, snap);

        let detail_snap = HookDetailSnapshot {
            ok: true,
            version: 1,
            record: Some(detail),
        };
        let detail_snap_val = serde_json::to_value(&detail_snap).unwrap();
        assert_all_keys_snake_case(&detail_snap_val);
        let detail_snap_rt: HookDetailSnapshot = serde_json::from_value(detail_snap_val).unwrap();
        assert_eq!(detail_snap_rt, detail_snap);
    }

    #[test]
    fn harness_and_space_and_worktree_round_trip() {
        let harnesses = HarnessFile {
            version: 1,
            schema_version: 2,
            generated_at_ms: TimestampMs(1_700_000_000_000),
            agents: vec![HarnessEntry {
                name: "codex".into(),
                binary: "/usr/bin/codex".into(),
                binary_state: "ok".into(),
                description: "OpenAI Codex".into(),
                profiles: vec![HarnessProfile {
                    name: "router".into(),
                    display_name: "Codex Router".into(),
                    source: "spaces".into(),
                    agent: "codex".into(),
                }],
            }],
        };
        assert!(harnesses.agents[0].is_launchable());
        let h_val = serde_json::to_value(&harnesses).unwrap();
        assert_all_keys_snake_case(&h_val);
        assert_eq!(
            h_val["agents"][0]["profiles"][0]["display_name"],
            "Codex Router"
        );
        let h_rt: HarnessFile = serde_json::from_value(h_val).unwrap();
        assert_eq!(h_rt, harnesses);

        let git = GitInfo {
            repo_key: "repo".into(),
            branch: "feat/test".into(),
            pr: Some(PRSummary {
                number: 42,
                title: "Test PR".into(),
                state: "OPEN".into(),
                is_draft: false,
                url: "https://example.com/pr/42".into(),
            }),
            checks: Some(CheckSummary {
                state: CheckState::Success,
                passed: 3,
                total: 3,
                failing_name: String::new(),
            }),
            ..GitInfo::default()
        };
        assert!(git.should_show_checks());

        let space = Space {
            name: "spaces".into(),
            id: "$1".into(),
            is_current: true,
            windows: vec![Window {
                id: "@1".into(),
                index: 0,
                name: "editor".into(),
                active: true,
                activity_at_ms: TimestampMs(1_700_000_000_000),
                ..Window::default()
            }],
            panes: vec![Pane {
                id: "%1".into(),
                numeric_id: 1,
                status: Status::Working,
                git: Some(git.clone()),
                ..Pane::default()
            }],
            git: Some(git.clone()),
            status: Status::Working,
            created_at_ms: TimestampMs(1_700_000_000_000),
            ..Space::default()
        };
        let s_val = serde_json::to_value(&space).unwrap();
        assert_all_keys_snake_case(&s_val);
        let s_rt: Space = serde_json::from_value(s_val).unwrap();
        assert_eq!(s_rt, space);

        let exited = ExitedSpace {
            name: "old".into(),
            saved_at_ms: TimestampMs(1_700_000_000_000),
            cwd: "/tmp/old".into(),
            window_count: 2,
            pane_count: 3,
            agents: vec!["claude".into()],
        };
        let e_val = serde_json::to_value(&exited).unwrap();
        assert_all_keys_snake_case(&e_val);
        let e_rt: ExitedSpace = serde_json::from_value(e_val).unwrap();
        assert_eq!(e_rt, exited);

        let wt_ctx = WorktreeContext {
            in_repo: true,
            repo_name: "spaces".into(),
            worktrees: vec![WorktreeEntry {
                path: "/tmp/wt".into(),
                name: "feat-x".into(),
                branch: "feat/x".into(),
                created_at_ms: TimestampMs(1_700_000_000_000),
                git: Some(git),
                ..WorktreeEntry::default()
            }],
            ..WorktreeContext::default()
        };
        let wt_val = serde_json::to_value(&wt_ctx).unwrap();
        assert_all_keys_snake_case(&wt_val);
        let wt_rt: WorktreeContext = serde_json::from_value(wt_val).unwrap();
        assert_eq!(wt_rt, wt_ctx);
    }
}

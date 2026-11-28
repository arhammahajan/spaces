use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::backend::{BackendVerb, BackendVerbResult};
use crate::conversations::{
    ConversationSearchHit, ConversationSearchRequest, ConversationSnapshot,
};
use crate::event::{EventCursor, EventEnvelope, EventSubscriptionFilter};
use crate::model::{HarnessEntry, HookDetail, Space, WorktreeEntry};
use crate::shared::SharedSnapshot;
use crate::telemetry::TelemetrySnapshot;

pub use crate::rpc_core::*;

/// Canonical UDS NDJSON-RPC 2.0 method names shared between Rust CLI/PTY host and TS `spacesd`.
pub mod rpc_methods {
    pub const AUTH: &str = "rpc.auth";
    pub const DAEMON_PING: &str = "daemon.ping";
    pub const DAEMON_STATUS: &str = "daemon.status";

    pub const THREAD_SPAWN: &str = "thread.spawn";
    pub const THREAD_LIST: &str = "thread.list";
    pub const THREAD_GET: &str = "thread.get";
    pub const THREAD_CLOSE: &str = "thread.close";

    pub const TURN_SEND: &str = "turn.send";
    pub const TURN_CANCEL: &str = "turn.cancel";

    pub const LEASE_CLAIM: &str = "lease.claim";
    pub const LEASE_YIELD: &str = "lease.yield";
    pub const LEASE_HANDOFF: &str = "lease.handoff";
    pub const LEASE_STATUS: &str = "lease.status";

    pub const MAILBOX_SEND: &str = "mailbox.send";
    pub const MAILBOX_LIST: &str = "mailbox.list";
    pub const MAILBOX_ACK: &str = "mailbox.ack";

    pub const EVENT_SUBSCRIBE: &str = "event.subscribe";
    pub const EVENT_UNSUBSCRIBE: &str = "event.unsubscribe";
    pub const EVENT_TAIL: &str = "event.tail";
    pub const EVENT_PUSH: &str = "event.push";

    pub const BACKEND_EXEC: &str = "backend.exec";
    pub const BACKEND_SPACES_LIST: &str = "backend.spaces.list";
    pub const BACKEND_HOOK_DETAIL_GET: &str = "backend.hook_detail.get";
    pub const BACKEND_HARNESSES_LIST: &str = "backend.harnesses.list";
    pub const BACKEND_TELEMETRY_GET: &str = "backend.telemetry.get";
    pub const BACKEND_CONVERSATIONS_LIST: &str = "backend.conversations.list";
    pub const BACKEND_CONVERSATIONS_SEARCH: &str = "backend.conversations.search";
    pub const BACKEND_SHARED_LIST: &str = "backend.shared.list";
    pub const BACKEND_WORKTREES_LIST: &str = "backend.worktrees.list";
    pub const BACKEND_STATUS_BAR_GET: &str = "backend.status_bar.get";

    pub const WORKTREE_LIST: &str = "worktree.list";
    pub const WORKTREE_CONTEXT: &str = "worktree.context";
    pub const WORKTREE_SUGGEST_NAME: &str = "worktree.suggest_name";
    pub const WORKTREE_CREATE: &str = "worktree.create";
    pub const WORKTREE_INSPECT_SAFETY: &str = "worktree.inspect_safety";
    pub const WORKTREE_REMOVE: &str = "worktree.remove";
    pub const WORKTREE_PRUNE: &str = "worktree.prune";
    pub const WORKTREE_RESOLVE: &str = "worktree.resolve";

    pub const GIT_STATUS: &str = "git.status";
    pub const GIT_DIFF: &str = "git.diff";
    pub const GIT_BRANCHES: &str = "git.branches";

    pub const FORGE_STATUS: &str = "forge.status";
    pub const FORGE_PR_LIST: &str = "forge.pr_list";

    pub const HOOK_EMIT: &str = "hook.emit";
    pub const HOOK_RECORD_OUTCOME: &str = "hook.record_outcome";
    pub const HOOK_REMOVE_OUTCOME: &str = "hook.remove_outcome";

    pub const PTY_SPAWN: &str = "pty.spawn";
    pub const PTY_ATTACH: &str = "pty.attach";
    pub const PTY_DETACH: &str = "pty.detach";
    pub const PTY_INPUT: &str = "pty.input";
    pub const PTY_OUTPUT: &str = "pty.output";
    pub const PTY_RESIZE: &str = "pty.resize";
    pub const PTY_KILL: &str = "pty.kill";
    pub const PTY_EXIT: &str = "pty.exit";
    pub const PTY_OSC: &str = "pty.osc";
}

// ---------------------------------------------------------------------------
// Event Bus RPC
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventSubscribeParams {
    pub filter: EventSubscriptionFilter,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventSubscribeResult {
    pub subscription_id: String,
    pub cursor: EventCursor,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventUnsubscribeParams {
    pub subscription_id: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventTailParams {
    pub filter: EventSubscriptionFilter,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventTailResult {
    pub events: Vec<EventEnvelope>,
    pub cursor: EventCursor,
    #[serde(default)]
    pub has_more: bool,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventPushNotification {
    pub subscription_id: String,
    pub event: EventEnvelope,
}

// ---------------------------------------------------------------------------
// Backend Verb & Query RPC
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendExecParams {
    pub verb: BackendVerb,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendExecResult {
    pub result: BackendVerbResult,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendSpacesListResult {
    pub spaces: Vec<Space>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendHookDetailParams {
    pub session: String,
    pub pane_num: u32,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendHookDetailResult {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub detail: Option<HookDetail>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendHarnessesListResult {
    pub harnesses: Vec<HarnessEntry>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendTelemetryGetParams {
    #[serde(default)]
    pub force: bool,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendTelemetryGetResult {
    pub snapshot: TelemetrySnapshot,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendConversationsListParams {
    #[serde(default)]
    pub refresh: bool,
    #[serde(default)]
    pub include_archived: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendConversationsListResult {
    pub snapshot: ConversationSnapshot,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendConversationsSearchParams {
    pub request: ConversationSearchRequest,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendConversationsSearchResult {
    pub hits: Vec<ConversationSearchHit>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendSharedListParams {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub dir: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendSharedListResult {
    pub snapshot: SharedSnapshot,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct BackendWorktreesListResult {
    pub worktrees: Vec<WorktreeEntry>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backend_exec_rpc_frame_is_single_line_ndjson() {
        let params = BackendExecParams {
            verb: BackendVerb::OpenSpace {
                name: "spaces".to_owned(),
            },
        };
        let req = RpcRequest::new(
            RpcId::Number(7),
            rpc_methods::BACKEND_EXEC,
            Some(serde_json::to_value(&params).expect("params value")),
        );
        let line = serde_json::to_string(&req).expect("serialize request");
        assert!(!line.contains('\n'));
        assert!(line.contains("\"method\":\"backend.exec\""));
        assert!(line.contains("\"verb\":\"open_space\""));

        let frame: RpcFrame = serde_json::from_str(&line).expect("decode frame");
        assert_eq!(frame, RpcFrame::Request(req));
    }
}

use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::{Epoch, PtyId, ThreadId, TimestampMs, TurnId};

/// Interaction mode of a managed thread.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum ThreadKind {
    Interactive,
    Headless,
    Hybrid,
}

/// Execution backend driving a thread.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum ThreadDriver {
    ClaudeSdk,
    CodexSdk,
    OpenCode,
    Copilot,
    Acp,
    NativePty,
}

/// Lifecycle state of a managed thread.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum ThreadState {
    Created,
    Active,
    Idle,
    Draining,
    Suspended,
    Closed,
    Failed,
}

/// Canonical managed conversation/execution thread record.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct Thread {
    pub id: ThreadId,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub space: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub window_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub pane_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub pty_id: Option<PtyId>,
    pub agent: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub profile: Option<String>,
    pub driver: ThreadDriver,
    pub kind: ThreadKind,
    pub state: ThreadState,
    pub cwd: String,
    pub canonical_cwd: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub worktree_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub vendor_session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub model: Option<String>,
    pub epoch: Epoch,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub active_turn_id: Option<TurnId>,
    pub created_at_ms: TimestampMs,
    pub updated_at_ms: TimestampMs,
}

/// Source that initiated a turn on a thread.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum TurnOrigin {
    Human,
    Coordinator,
    McpEvent,
    Mailbox,
    Schedule,
}

/// Execution state of an individual turn.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum TurnState {
    Queued,
    Running,
    WaitingPermission,
    Completed,
    Cancelled,
    Failed,
}

/// Structured failure detail for a failed turn.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct TurnError {
    pub code: String,
    pub message: String,
    pub retryable: bool,
}

/// Token and cost accounting for a turn.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct TurnUsage {
    #[ts(type = "number")]
    pub input_tokens: i64,
    #[ts(type = "number")]
    pub output_tokens: i64,
    #[ts(type = "number")]
    pub reasoning_tokens: i64,
    #[ts(type = "number")]
    pub cache_read_tokens: i64,
    #[ts(type = "number")]
    pub cache_write_tokens: i64,
    pub cost_usd: f64,
    pub cost_priced: bool,
}

/// Single execution turn within a thread.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct Turn {
    pub id: TurnId,
    pub thread_id: ThreadId,
    pub epoch: Epoch,
    pub origin: TurnOrigin,
    pub state: TurnState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub prompt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub reply_preview: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub usage: Option<TurnUsage>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub error: Option<TurnError>,
    pub started_at_ms: TimestampMs,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub ended_at_ms: Option<TimestampMs>,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn assert_ndjson_roundtrip<T>(value: &T) -> String
    where
        T: Serialize + for<'de> Deserialize<'de> + PartialEq + std::fmt::Debug,
    {
        let line = serde_json::to_string(value).expect("serialize to single-line JSON");
        assert!(!line.contains('\n'), "NDJSON line must not contain \\n");
        assert!(!line.contains('\r'), "NDJSON line must not contain \\r");

        let framed = format!("{line}\n{line}\n");
        let parsed: Vec<T> = framed
            .lines()
            .map(|l| serde_json::from_str(l).expect("deserialize NDJSON line"))
            .collect();
        assert_eq!(parsed.len(), 2);
        assert_eq!(&parsed[0], value);
        assert_eq!(&parsed[1], value);
        line
    }

    #[test]
    fn thread_and_turn_enums_snake_case() {
        assert_eq!(
            assert_ndjson_roundtrip(&ThreadDriver::ClaudeSdk),
            "\"claude_sdk\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&ThreadDriver::NativePty),
            "\"native_pty\""
        );
        assert_eq!(assert_ndjson_roundtrip(&ThreadKind::Hybrid), "\"hybrid\"");
        assert_eq!(
            assert_ndjson_roundtrip(&ThreadState::Draining),
            "\"draining\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&TurnOrigin::McpEvent),
            "\"mcp_event\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&TurnState::WaitingPermission),
            "\"waiting_permission\""
        );
    }

    #[test]
    fn thread_and_turn_roundtrip() {
        let thread = Thread {
            id: ThreadId::from("thr_100"),
            space: Some("spaces".into()),
            window_id: Some("@1".into()),
            pane_id: Some("%12".into()),
            pty_id: Some(PtyId::from("pty_100")),
            agent: "claude".into(),
            profile: None,
            driver: ThreadDriver::ClaudeSdk,
            kind: ThreadKind::Hybrid,
            state: ThreadState::Active,
            cwd: "/home/user/spaces".into(),
            canonical_cwd: "/home/user/spaces".into(),
            worktree_path: None,
            vendor_session_id: Some("v_sess_1".into()),
            title: Some("Implement types\nand RPC".into()),
            model: Some("claude-sonnet".into()),
            epoch: Epoch(1),
            active_turn_id: Some(TurnId::from("turn_1")),
            created_at_ms: TimestampMs(1_700_000_000_000),
            updated_at_ms: TimestampMs(1_700_000_000_100),
        };
        let thread_json = assert_ndjson_roundtrip(&thread);
        assert!(!thread_json.contains("worktree_path"));
        assert!(!thread_json.contains("profile"));

        let turn = Turn {
            id: TurnId::from("turn_1"),
            thread_id: thread.id.clone(),
            epoch: Epoch(1),
            origin: TurnOrigin::Human,
            state: TurnState::Completed,
            prompt: Some("Hello\nworld".into()),
            reply_preview: Some("Done".into()),
            model: Some("claude-sonnet".into()),
            usage: Some(TurnUsage {
                input_tokens: 1200,
                output_tokens: 340,
                reasoning_tokens: 100,
                cache_read_tokens: 800,
                cache_write_tokens: 50,
                cost_usd: 0.0125,
                cost_priced: true,
            }),
            error: Some(TurnError {
                code: "rate_limited".into(),
                message: "try again".into(),
                retryable: true,
            }),
            started_at_ms: TimestampMs(1_700_000_000_010),
            ended_at_ms: Some(TimestampMs(1_700_000_005_010)),
        };
        assert_ndjson_roundtrip(&turn);
    }

    #[test]
    fn thread_ts_declarations_have_no_bigint() {
        for decl in [
            ThreadKind::decl(),
            ThreadDriver::decl(),
            ThreadState::decl(),
            Thread::decl(),
            TurnOrigin::decl(),
            TurnState::decl(),
            TurnError::decl(),
            TurnUsage::decl(),
            Turn::decl(),
        ] {
            assert!(
                !decl.contains("bigint"),
                "unexpected bigint in TS decl: {decl}"
            );
        }
    }
}

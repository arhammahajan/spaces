use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::TimestampMs;

/// Unified coding-agent conversation record returned by `spaces __space-conversations`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ConversationEntry {
    pub agent: String,
    pub id: String,
    pub app: String,
    #[serde(default)]
    pub profile: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub preview: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub created_at_ms: TimestampMs,
    #[serde(default)]
    pub updated_at_ms: TimestampMs,
    #[serde(default)]
    pub message_count: u32,
    #[serde(default)]
    pub archived: bool,
    #[serde(default)]
    pub source_path: String,
    #[serde(default)]
    pub resume_target: String,
    #[serde(default)]
    pub space: String,
    #[serde(default)]
    pub last_outcome: String,
    #[serde(default)]
    pub last_opened_ms: TimestampMs,
    #[serde(default)]
    pub launch_cmd: Vec<String>,
}

impl ConversationEntry {
    /// Returns the most relevant recency timestamp, preferring the latest of
    /// `updated_at_ms` and `last_opened_ms` with `created_at_ms` fallback.
    pub fn effective_time_ms(&self) -> TimestampMs {
        let mut t = self.updated_at_ms.0.max(self.last_opened_ms.0);
        if t <= 0 {
            t = self.created_at_ms.0;
        }
        TimestampMs(t)
    }

    /// Returns `resume_target` when non-empty, otherwise `id`.
    pub fn resume_session_id(&self) -> &str {
        let target = self.resume_target.trim();
        if !target.is_empty() {
            target
        } else {
            self.id.trim()
        }
    }
}

/// Highlighted character range `[start, end)` inside [`ConversationSearchHit::snippet`].
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct SnippetSpan {
    pub start: u32,
    pub end: u32,
}

/// Search hit returned by `spaces __space-conversations --search`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ConversationSearchHit {
    pub conversation: ConversationEntry,
    pub match_kind: String,
    #[serde(default)]
    pub match_role: String,
    #[serde(default)]
    pub snippet: String,
    #[serde(default)]
    pub snippet_highlights: Vec<SnippetSpan>,
    #[serde(default)]
    pub score: f64,
}

/// Cached or refreshed conversation list response.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ConversationSnapshot {
    pub ok: bool,
    #[serde(default)]
    pub conversations: Vec<ConversationEntry>,
    #[serde(default)]
    pub synced_ms: TimestampMs,
    #[serde(default)]
    pub indexed_messages: u32,
    #[serde(default)]
    pub pending_sources: u32,
}

/// Arguments for searching unified coding-agent conversations.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ConversationSearchRequest {
    pub query: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub cwd: String,
    #[serde(default)]
    pub scope: String,
    #[serde(default)]
    pub include_archived: bool,
    #[serde(default)]
    pub limit: u32,
}

/// Search response returned by `spaces __space-conversations --search`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct ConversationSearchResponse {
    pub ok: bool,
    pub query: String,
    pub scope: String,
    #[serde(default)]
    pub hits: Vec<ConversationSearchHit>,
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
    fn conversation_types_round_trip_and_helpers() {
        let entry = ConversationEntry {
            agent: "claude".into(),
            id: "conv-123".into(),
            app: "Claude Code".into(),
            profile: "work".into(),
            title: "Port domain types".into(),
            preview: "Let's check internal/model".into(),
            cwd: "/home/dev/spaces".into(),
            branch: "feat/unified-types-rpc".into(),
            model: "claude-opus".into(),
            created_at_ms: TimestampMs(1_700_000_000_000),
            updated_at_ms: TimestampMs(1_700_000_010_000),
            message_count: 12,
            archived: false,
            source_path: "/home/dev/.spaces/claude/conv.jsonl".into(),
            resume_target: "resume-abc".into(),
            space: "spaces".into(),
            last_outcome: "done".into(),
            last_opened_ms: TimestampMs(1_700_000_020_000),
            launch_cmd: vec!["claude".into(), "--resume".into(), "resume-abc".into()],
        };
        assert_eq!(entry.effective_time_ms(), TimestampMs(1_700_000_020_000));
        assert_eq!(entry.resume_session_id(), "resume-abc");
        let fallback_entry = ConversationEntry {
            id: "raw-id".into(),
            created_at_ms: TimestampMs(1_700_000_005_000),
            ..ConversationEntry::default()
        };
        assert_eq!(
            fallback_entry.effective_time_ms(),
            TimestampMs(1_700_000_005_000)
        );
        assert_eq!(fallback_entry.resume_session_id(), "raw-id");

        let snap = ConversationSnapshot {
            ok: true,
            conversations: vec![entry.clone()],
            synced_ms: TimestampMs(1_700_000_025_000),
            indexed_messages: 42,
            pending_sources: 0,
        };
        let snap_val = serde_json::to_value(&snap).unwrap();
        assert_all_keys_snake_case(&snap_val);
        let snap_rt: ConversationSnapshot = serde_json::from_value(snap_val).unwrap();
        assert_eq!(snap_rt, snap);

        let req = ConversationSearchRequest {
            query: "domain".into(),
            agent: "claude".into(),
            cwd: "/home/dev/spaces".into(),
            scope: "all".into(),
            include_archived: false,
            limit: 50,
        };
        let req_val = serde_json::to_value(&req).unwrap();
        assert_all_keys_snake_case(&req_val);
        let req_rt: ConversationSearchRequest = serde_json::from_value(req_val).unwrap();
        assert_eq!(req_rt, req);

        let resp = ConversationSearchResponse {
            ok: true,
            query: "domain".into(),
            scope: "all".into(),
            hits: vec![ConversationSearchHit {
                conversation: entry,
                match_kind: "message".into(),
                match_role: "user".into(),
                snippet: "Port domain types".into(),
                snippet_highlights: vec![SnippetSpan { start: 5, end: 11 }],
                score: 0.95,
            }],
        };
        let resp_val = serde_json::to_value(&resp).unwrap();
        assert_all_keys_snake_case(&resp_val);
        let resp_rt: ConversationSearchResponse = serde_json::from_value(resp_val).unwrap();
        assert_eq!(resp_rt, resp);
    }
}

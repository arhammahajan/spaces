use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::{MailboxMessageId, ThreadId, TimestampMs};

/// Kind of message queued in a thread's durable mailbox.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum MailboxMessageKind {
    Prompt,
    Steer,
    Reply,
    Wakeup,
    McpEvent,
    PermissionRequest,
    PermissionDecision,
    SystemNotice,
}

/// Delivery priority assigned to a mailbox message.
#[derive(
    Debug, Clone, Copy, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize, TS,
)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum MailboxPriority {
    Low,
    #[default]
    Normal,
    High,
    Urgent,
}

/// Lifecycle status of a mailbox message.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum MailboxStatus {
    Pending,
    Delivered,
    Acknowledged,
    Expired,
    DeadLetter,
}

/// Addressing metadata identifying the sender of a mailbox message.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxAddress {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub thread_id: Option<ThreadId>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub agent: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub role: Option<String>,
}

/// Durable message addressed to a thread mailbox.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxMessage {
    pub id: MailboxMessageId,
    pub thread_id: ThreadId,
    pub from: MailboxAddress,
    pub kind: MailboxMessageKind,
    pub priority: MailboxPriority,
    pub status: MailboxStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub dedup_key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub correlation_id: Option<String>,
    #[ts(type = "unknown")]
    pub payload: serde_json::Value,
    pub created_at_ms: TimestampMs,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub delivered_at_ms: Option<TimestampMs>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub expires_at_ms: Option<TimestampMs>,
}

/// Subscription binding external event sources into a thread mailbox.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct MailboxSubscription {
    pub id: String,
    pub thread_id: ThreadId,
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional, type = "unknown")]
    pub filter: Option<serde_json::Value>,
    pub created_at_ms: TimestampMs,
}

#[cfg(test)]
mod tests {
    use serde_json::json;

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
    fn mailbox_enums_snake_case() {
        assert_eq!(
            assert_ndjson_roundtrip(&MailboxMessageKind::PermissionRequest),
            "\"permission_request\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&MailboxMessageKind::McpEvent),
            "\"mcp_event\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&MailboxPriority::Urgent),
            "\"urgent\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&MailboxStatus::DeadLetter),
            "\"dead_letter\""
        );
    }

    #[test]
    fn mailbox_message_and_subscription_roundtrip() {
        let msg = MailboxMessage {
            id: MailboxMessageId::from("msg_01"),
            thread_id: ThreadId::from("thr_01"),
            from: MailboxAddress {
                thread_id: Some(ThreadId::from("thr_coord")),
                agent: Some("claude".into()),
                role: Some("coordinator".into()),
            },
            kind: MailboxMessageKind::Steer,
            priority: MailboxPriority::High,
            status: MailboxStatus::Pending,
            dedup_key: Some("dedup-1".into()),
            correlation_id: None,
            payload: json!({
                "text": "Line 1\nLine 2",
                "nested": { "ok": true }
            }),
            created_at_ms: TimestampMs(1_700_000_000_000),
            delivered_at_ms: None,
            expires_at_ms: Some(TimestampMs(1_700_000_060_000)),
        };
        let msg_json = assert_ndjson_roundtrip(&msg);
        assert!(!msg_json.contains("correlation_id"));
        assert!(!msg_json.contains("delivered_at_ms"));

        let sub = MailboxSubscription {
            id: "sub_1".into(),
            thread_id: ThreadId::from("thr_01"),
            source: "mcp:github".into(),
            filter: Some(json!({"repo": "arhammahajan/spaces"})),
            created_at_ms: TimestampMs(1_700_000_000_500),
        };
        assert_ndjson_roundtrip(&sub);

        let sub_no_filter: MailboxSubscription = serde_json::from_str(
            r#"{"id":"sub_2","thread_id":"thr_01","source":"ci","created_at_ms":123}"#,
        )
        .unwrap();
        assert_eq!(sub_no_filter.filter, None);
        assert_ndjson_roundtrip(&sub_no_filter);
    }

    #[test]
    fn mailbox_ts_declarations_have_no_bigint_or_json_value() {
        for decl in [
            MailboxMessageKind::decl(),
            MailboxPriority::decl(),
            MailboxStatus::decl(),
            MailboxAddress::decl(),
            MailboxMessage::decl(),
            MailboxSubscription::decl(),
        ] {
            assert!(
                !decl.contains("bigint"),
                "unexpected bigint in TS decl: {decl}"
            );
            assert!(
                !decl.contains("JsonValue"),
                "unexpected JsonValue in TS decl: {decl}"
            );
        }
        assert!(MailboxMessage::decl().contains("payload: unknown"));
        assert!(MailboxSubscription::decl().contains("filter?: unknown"));
    }
}

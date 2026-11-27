use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::{BootId, Epoch, EventId, MailboxMessageId, PtyId, Seq, ThreadId, TimestampMs};
use crate::lease::{Lease, LeaseTransition};
use crate::mailbox::{MailboxMessage, MailboxStatus};
use crate::model::{OutcomeEntry, Space};
use crate::osc::{OscResumeHint, OscStatus};
use crate::thread::{Thread, Turn};

/// Cursor identifying a client's exact position in `spacesd`'s per-boot event stream.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventCursor {
    pub boot_id: BootId,
    pub seq: Seq,
}

/// Top-level envelope emitted on `spacesd`'s unified monotonic event bus.
///
/// `id` is also recorded in `seen_events` for idempotent deduplication across retries.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventEnvelope {
    pub id: EventId,
    pub boot_id: BootId,
    pub seq: Seq,
    pub ts_ms: TimestampMs,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub thread_id: Option<ThreadId>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub epoch: Option<Epoch>,
    pub payload: EventPayload,
}

/// Discriminated union of all unified event bus payloads.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(tag = "type", rename_all = "snake_case")]
#[ts(export)]
pub enum EventPayload {
    ThreadUpdated {
        thread: Thread,
    },
    TurnUpdated {
        turn: Turn,
    },
    LeaseChanged {
        lease: Lease,
        transition: LeaseTransition,
    },
    MailboxEnqueued {
        message: MailboxMessage,
    },
    MailboxUpdated {
        message_id: MailboxMessageId,
        status: MailboxStatus,
    },
    OscStatusReceived {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        pane_id: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        pty_id: Option<PtyId>,
        status: OscStatus,
    },
    OscResumeHintReceived {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        pane_id: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        pty_id: Option<PtyId>,
        hint: OscResumeHint,
    },
    PtyExited {
        pty_id: PtyId,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        exit_code: Option<i32>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        #[ts(optional)]
        signal: Option<String>,
    },
    SpaceSnapshotUpdated {
        spaces: Vec<Space>,
    },
    OutcomeRecorded {
        outcome: OutcomeEntry,
    },
    /// Sent when a client's `after_seq` cursor falls behind the bounded replay buffer
    /// or references an older `boot_id`, signaling the client to reload full state.
    GapReloadRequired {
        earliest_seq: Seq,
        latest_seq: Seq,
        reason: String,
    },
}

/// Filter passed to `event.subscribe` and `event.tail`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct EventSubscriptionFilter {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub after_seq: Option<Seq>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub boot_id: Option<BootId>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub thread_ids: Vec<ThreadId>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub types: Vec<String>,
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::osc::OscState;

    #[test]
    fn event_envelope_osc_round_trip() {
        let env = EventEnvelope {
            id: EventId::from("evt-1"),
            boot_id: BootId::from("boot-abc"),
            seq: Seq(42),
            ts_ms: TimestampMs(1_700_000_000_000),
            thread_id: Some(ThreadId::from("thr-1")),
            epoch: Some(Epoch(3)),
            payload: EventPayload::OscStatusReceived {
                pane_id: Some("%12".to_owned()),
                pty_id: Some(PtyId::from("pty-1")),
                status: OscStatus {
                    state: OscState::Working,
                    app: Some("claude".to_owned()),
                    profile: None,
                    kind: None,
                    msg: Some("thinking".to_owned()),
                    raw: None,
                },
            },
        };

        let json = serde_json::to_string(&env).expect("serialize event envelope");
        assert!(!json.contains('\n'));
        assert!(json.contains("\"type\":\"osc_status_received\""));

        let decoded: EventEnvelope = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(decoded, env);
    }

    #[test]
    fn event_gap_reload_round_trip() {
        let env = EventEnvelope {
            id: EventId::from("evt-gap"),
            boot_id: BootId::from("boot-xyz"),
            seq: Seq(1000),
            ts_ms: TimestampMs(1_700_000_001_000),
            thread_id: None,
            epoch: None,
            payload: EventPayload::GapReloadRequired {
                earliest_seq: Seq(500),
                latest_seq: Seq(1000),
                reason: "cursor fell behind replay buffer".to_owned(),
            },
        };

        let value = serde_json::to_value(&env).expect("to_value");
        assert_eq!(value["payload"]["type"], "gap_reload_required");
        assert_eq!(value["payload"]["earliest_seq"], 500);

        let decoded: EventEnvelope = serde_json::from_value(value).expect("from_value");
        assert_eq!(decoded, env);
    }
}

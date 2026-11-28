use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::{Epoch, LeaseId, ThreadId, TimestampMs};

/// Fenced ownership lease FSM states (`UNOWNED -> OWNED(epoch)`,
/// `DRAIN / CANCEL -> STOP_AND_FLUSH -> +1`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum LeaseState {
    Unowned,
    Owned,
    Draining,
    Cancelling,
    StoppingAndFlushing,
}

/// Classification of the actor holding or requesting a thread write lease.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum LeaseOwnerKind {
    Human,
    Agent,
    System,
}

/// Identity of the current or requested lease holder.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseOwner {
    pub kind: LeaseOwnerKind,
    pub id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub client_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub label: Option<String>,
}

/// Monotonically fenced write-authority token bound to a thread and epoch.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct FencingToken {
    pub thread_id: ThreadId,
    pub epoch: Epoch,
}

/// Reason recorded when transitioning or transferring a thread lease.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum HandoffReason {
    UserAttach,
    UserYield,
    AgentClaim,
    AgentYield,
    Disconnect,
    Timeout,
    Cancel,
}

/// Authoritative ownership lease record for a thread.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct Lease {
    pub id: LeaseId,
    pub thread_id: ThreadId,
    pub epoch: Epoch,
    pub state: LeaseState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub owner: Option<LeaseOwner>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub acquired_at_ms: Option<TimestampMs>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub expires_at_ms: Option<TimestampMs>,
    pub updated_at_ms: TimestampMs,
}

/// Audit record emitted for every lease FSM transition.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct LeaseTransition {
    pub thread_id: ThreadId,
    pub from_state: LeaseState,
    pub to_state: LeaseState,
    pub from_epoch: Epoch,
    pub to_epoch: Epoch,
    pub reason: HandoffReason,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub actor: Option<LeaseOwner>,
    pub ts_ms: TimestampMs,
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
    fn lease_enums_snake_case() {
        assert_eq!(
            assert_ndjson_roundtrip(&LeaseState::StoppingAndFlushing),
            "\"stopping_and_flushing\""
        );
        assert_eq!(assert_ndjson_roundtrip(&LeaseState::Unowned), "\"unowned\"");
        assert_eq!(assert_ndjson_roundtrip(&LeaseOwnerKind::Human), "\"human\"");
        assert_eq!(
            assert_ndjson_roundtrip(&HandoffReason::UserAttach),
            "\"user_attach\""
        );
        assert_eq!(
            assert_ndjson_roundtrip(&HandoffReason::AgentClaim),
            "\"agent_claim\""
        );
    }

    #[test]
    fn lease_and_transition_roundtrip() {
        let owner = LeaseOwner {
            kind: LeaseOwnerKind::Agent,
            id: "coordinator".into(),
            client_id: Some("cli_42".into()),
            label: Some("Primary\nAgent".into()),
        };

        let lease = Lease {
            id: LeaseId::from("lease_1"),
            thread_id: ThreadId::from("thr_1"),
            epoch: Epoch(3),
            state: LeaseState::Owned,
            owner: Some(owner.clone()),
            acquired_at_ms: Some(TimestampMs(1_700_000_000_000)),
            expires_at_ms: None,
            updated_at_ms: TimestampMs(1_700_000_001_000),
        };
        let lease_json = assert_ndjson_roundtrip(&lease);
        assert!(!lease_json.contains("expires_at_ms"));
        assert!(lease_json.contains(r#""epoch":3"#));

        let token = FencingToken {
            thread_id: lease.thread_id.clone(),
            epoch: lease.epoch,
        };
        assert_ndjson_roundtrip(&token);

        let transition = LeaseTransition {
            thread_id: ThreadId::from("thr_1"),
            from_state: LeaseState::StoppingAndFlushing,
            to_state: LeaseState::Owned,
            from_epoch: Epoch(3),
            to_epoch: Epoch(4),
            reason: HandoffReason::UserAttach,
            actor: Some(owner),
            ts_ms: TimestampMs(1_700_000_002_000),
        };
        let tr_json = assert_ndjson_roundtrip(&transition);
        assert!(tr_json.contains(r#""from_state":"stopping_and_flushing""#));
        assert!(tr_json.contains(r#""to_epoch":4"#));
    }

    #[test]
    fn lease_ts_declarations_have_no_bigint() {
        for decl in [
            LeaseState::decl(),
            LeaseOwnerKind::decl(),
            LeaseOwner::decl(),
            FencingToken::decl(),
            HandoffReason::decl(),
            Lease::decl(),
            LeaseTransition::decl(),
        ] {
            assert!(
                !decl.contains("bigint"),
                "unexpected bigint in TS decl: {decl}"
            );
        }
    }
}

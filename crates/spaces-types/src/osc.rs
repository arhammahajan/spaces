use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::ThreadId;

/// Normalized agent execution state carried by OSC 7501 status notifications.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum OscState {
    Working,
    Idle,
    Done,
    Blocked,
    Failed,
    Clear,
}

/// Parsed OSC 7501 program-status notification matching `internal/osc/osc.go`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct OscStatus {
    pub state: OscState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub app: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub profile: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub kind: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub msg: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub raw: Option<String>,
}

/// Parsed OSC 7577 session-resume hint emitted by agents/wrappers.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub struct OscResumeHint {
    pub agent: String,
    pub session_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub profile: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub cwd: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub resume_target: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub thread_id: Option<ThreadId>,
    /// Per-pane HMAC-SHA256 hex digest authenticating this OSC 7577 resume hint.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub signature: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub raw: Option<String>,
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
    fn osc_state_snake_case_values() {
        let cases = [
            (OscState::Working, "\"working\""),
            (OscState::Idle, "\"idle\""),
            (OscState::Done, "\"done\""),
            (OscState::Blocked, "\"blocked\""),
            (OscState::Failed, "\"failed\""),
            (OscState::Clear, "\"clear\""),
        ];
        for (state, expected) in cases {
            let json = assert_ndjson_roundtrip(&state);
            assert_eq!(json, expected);
        }
    }

    #[test]
    fn osc_status_roundtrip_and_optional_omission() {
        let minimal = OscStatus {
            state: OscState::Clear,
            app: None,
            profile: None,
            kind: None,
            msg: None,
            raw: None,
        };
        let json = assert_ndjson_roundtrip(&minimal);
        assert_eq!(json, r#"{"state":"clear"}"#);

        let full = OscStatus {
            state: OscState::Blocked,
            app: Some("claude-code".into()),
            profile: Some("work".into()),
            kind: Some("permission".into()),
            msg: Some("Approve bash:\nline two".into()),
            raw: Some("state=blocked:app=claude-code:kind=permission".into()),
        };
        let full_json = assert_ndjson_roundtrip(&full);
        assert!(full_json.contains(r#""state":"blocked""#));
        assert!(full_json.contains(r#""profile":"work""#));
    }

    #[test]
    fn osc_resume_hint_roundtrip() {
        let hint = OscResumeHint {
            agent: "codex".into(),
            session_id: "sess_abc123".into(),
            profile: Some("default".into()),
            cwd: Some("/home/user/repo".into()),
            resume_target: Some("codex resume sess_abc123".into()),
            thread_id: Some(ThreadId::from("thr_01")),
            signature: Some("deadbeef".into()),
            raw: Some("agent=codex:session_id=sess_abc123".into()),
        };
        let json = assert_ndjson_roundtrip(&hint);
        assert!(json.contains(r#""session_id":"sess_abc123""#));
        assert!(json.contains(r#""thread_id":"thr_01""#));

        let minimal: OscResumeHint =
            serde_json::from_str(r#"{"agent":"claude","session_id":"s1"}"#).unwrap();
        assert_eq!(minimal.profile, None);
        assert_eq!(minimal.thread_id, None);
        assert_ndjson_roundtrip(&minimal);
    }

    #[test]
    fn osc_ts_declarations_have_no_bigint() {
        for decl in [OscState::decl(), OscStatus::decl(), OscResumeHint::decl()] {
            assert!(
                !decl.contains("bigint"),
                "unexpected bigint in TS decl: {decl}"
            );
        }
    }
}

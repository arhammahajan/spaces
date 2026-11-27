use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::conversations::ConversationEntry;
use crate::model::{OutcomeEntry, Space};
use crate::telemetry::OrphanPort;

/// Terminal multiplexer or host driver powering backend workspace operations.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum BackendDriverKind {
    #[default]
    Tmux,
    Kitty,
    Zellij,
}

/// Context for live auto-assigned worktree branch naming.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeNamingInput {
    #[serde(default)]
    pub explicit_name: String,
    #[serde(default)]
    pub target_name: String,
    #[serde(default)]
    pub target_kind: String,
    #[serde(default)]
    pub harness_app: String,
    #[serde(default)]
    pub harness_title: String,
    #[serde(default)]
    pub harness_prompt: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub seed: i64,
}

/// Tab or space creation request with optional git worktree creation or preexisting selection.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeCreateRequest {
    #[serde(default)]
    pub session: String,
    #[serde(default)]
    pub space_name: String,
    #[serde(default)]
    pub tab_name: String,
    pub folder: String,
    #[serde(default)]
    pub use_worktree: bool,
    #[serde(default)]
    pub create_new: bool,
    #[serde(default)]
    pub existing_path: String,
    #[serde(default)]
    pub branch_name: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default)]
    #[ts(type = "number")]
    pub auto_seed: i64,
    #[serde(default)]
    pub harness_app: String,
    #[serde(default)]
    pub harness_title: String,
    #[serde(default)]
    pub harness_prompt: String,
}

/// Reports whether removing a worktree is clean or has warnings.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeRemoveSafety {
    pub can_remove_safely: bool,
    pub is_main: bool,
    pub is_dirty: bool,
    pub unmerged_commits: u32,
    #[serde(default)]
    pub branch: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default)]
    pub warnings: Vec<String>,
}

/// Auto-generated branch and directory name suggestion for a new worktree.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct WorktreeNameSuggestion {
    pub branch: String,
    pub dir_name: String,
    pub source: String,
    pub rule: String,
}

/// Mutation and action verbs executed against the backend workspace driver.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, TS)]
#[serde(tag = "verb", rename_all = "snake_case")]
#[ts(export)]
pub enum BackendVerb {
    OpenSpace {
        name: String,
    },
    FocusPane {
        session: String,
        window_id: String,
        pane_id: String,
    },
    CreateSpace {
        name: String,
        #[serde(default)]
        cwd: String,
    },
    RenameSpace {
        old_name: String,
        new_name: String,
    },
    KillSpace {
        name: String,
    },
    NewWindow {
        session: String,
        #[serde(default)]
        cwd: String,
        #[serde(default)]
        name: String,
    },
    RenameWindow {
        window_id: String,
        new_name: String,
    },
    KillWindow {
        window_id: String,
    },
    LaunchAgent {
        session: String,
        #[serde(default)]
        cwd: String,
        agent: String,
        #[serde(default)]
        profile: String,
    },
    RestoreSpace {
        name: String,
    },
    DeleteExitedSpace {
        name: String,
    },
    CapturePreviews {
        pane_ids: Vec<String>,
        lines: u32,
    },
    CreateTab {
        request: WorktreeCreateRequest,
    },
    CreateSpaceWithWorktree {
        request: WorktreeCreateRequest,
    },
    RemoveWorktree {
        path: String,
        #[serde(default)]
        force: bool,
        #[serde(default)]
        delete_branch: bool,
        #[serde(default)]
        close_windows: Vec<String>,
    },
    PruneWorktrees {
        #[serde(default)]
        spaces: Vec<Space>,
        #[serde(default)]
        remove_orphaned_merged: bool,
    },
    OpenOutcome {
        outcome: OutcomeEntry,
    },
    OpenConversation {
        conversation: ConversationEntry,
        #[serde(default)]
        force_new_tab: bool,
    },
    OpenSharedFile {
        path: String,
    },
    KillOrphanPorts {
        #[serde(default)]
        orphans: Vec<OrphanPort>,
    },
}

/// Result returned from executing a [`BackendVerb`].
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, TS)]
#[serde(tag = "kind", rename_all = "snake_case")]
#[ts(export)]
pub enum BackendVerbResult {
    Ack,
    Previews { previews: BTreeMap<String, String> },
    Pruned { summary: String },
    KilledPorts { count: u32 },
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ids::TimestampMs;
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
                    if key == "previews" {
                        // `previews` is a dynamic map keyed by pane ID (e.g. "%1").
                        if let Value::Object(inner) = val {
                            for entry in inner.values() {
                                assert_all_keys_snake_case(entry);
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
    fn backend_driver_and_worktree_structs_round_trip() {
        for kind in [
            BackendDriverKind::Tmux,
            BackendDriverKind::Kitty,
            BackendDriverKind::Zellij,
        ] {
            let val = serde_json::to_value(kind).unwrap();
            let rt: BackendDriverKind = serde_json::from_value(val).unwrap();
            assert_eq!(rt, kind);
        }

        let naming = WorktreeNamingInput {
            explicit_name: "fix-bug".into(),
            target_name: "spaces".into(),
            target_kind: "tab".into(),
            harness_app: "codex".into(),
            harness_title: "Fix bug".into(),
            harness_prompt: "Check parser".into(),
            seed: 7,
        };
        let n_val = serde_json::to_value(&naming).unwrap();
        assert_all_keys_snake_case(&n_val);
        let n_rt: WorktreeNamingInput = serde_json::from_value(n_val).unwrap();
        assert_eq!(n_rt, naming);

        let safety = WorktreeRemoveSafety {
            can_remove_safely: false,
            is_main: false,
            is_dirty: true,
            unmerged_commits: 2,
            branch: "feat/test".into(),
            base_branch: "main".into(),
            warnings: vec!["uncommitted changes".into()],
        };
        let s_val = serde_json::to_value(&safety).unwrap();
        assert_all_keys_snake_case(&s_val);
        let s_rt: WorktreeRemoveSafety = serde_json::from_value(s_val).unwrap();
        assert_eq!(s_rt, safety);

        let suggestion = WorktreeNameSuggestion {
            branch: "feat/fix-bug".into(),
            dir_name: "feat-fix-bug".into(),
            source: "auto".into(),
            rule: "prompt".into(),
        };
        let sug_val = serde_json::to_value(&suggestion).unwrap();
        assert_all_keys_snake_case(&sug_val);
        let sug_rt: WorktreeNameSuggestion = serde_json::from_value(sug_val).unwrap();
        assert_eq!(sug_rt, suggestion);
    }

    #[test]
    fn backend_verbs_and_results_round_trip() {
        let req = WorktreeCreateRequest {
            session: "spaces".into(),
            space_name: String::new(),
            tab_name: "rpc".into(),
            folder: "/tmp/repo".into(),
            use_worktree: true,
            create_new: true,
            existing_path: String::new(),
            branch_name: "feat/rpc".into(),
            base_branch: "main".into(),
            auto_seed: 1,
            harness_app: "claude".into(),
            harness_title: "Build RPC".into(),
            harness_prompt: "Implement RPC".into(),
        };

        let verbs = vec![
            BackendVerb::OpenSpace {
                name: "spaces".into(),
            },
            BackendVerb::FocusPane {
                session: "spaces".into(),
                window_id: "@1".into(),
                pane_id: "%2".into(),
            },
            BackendVerb::CreateSpace {
                name: "new".into(),
                cwd: "/tmp".into(),
            },
            BackendVerb::RenameSpace {
                old_name: "old".into(),
                new_name: "new".into(),
            },
            BackendVerb::KillSpace { name: "old".into() },
            BackendVerb::NewWindow {
                session: "spaces".into(),
                cwd: "/tmp".into(),
                name: "shell".into(),
            },
            BackendVerb::RenameWindow {
                window_id: "@1".into(),
                new_name: "editor".into(),
            },
            BackendVerb::KillWindow {
                window_id: "@1".into(),
            },
            BackendVerb::LaunchAgent {
                session: "spaces".into(),
                cwd: "/tmp".into(),
                agent: "codex".into(),
                profile: "default".into(),
            },
            BackendVerb::RestoreSpace {
                name: "exited".into(),
            },
            BackendVerb::DeleteExitedSpace {
                name: "exited".into(),
            },
            BackendVerb::CapturePreviews {
                pane_ids: vec!["%1".into(), "%2".into()],
                lines: 40,
            },
            BackendVerb::CreateTab {
                request: req.clone(),
            },
            BackendVerb::CreateSpaceWithWorktree { request: req },
            BackendVerb::RemoveWorktree {
                path: "/tmp/wt".into(),
                force: false,
                delete_branch: true,
                close_windows: vec!["@3".into()],
            },
            BackendVerb::PruneWorktrees {
                spaces: vec![Space::default()],
                remove_orphaned_merged: true,
            },
            BackendVerb::OpenOutcome {
                outcome: OutcomeEntry {
                    session: "spaces".into(),
                    agent: "claude".into(),
                    outcome: "done".into(),
                    ts_ms: TimestampMs(1_700_000_000_000),
                    ..OutcomeEntry::default()
                },
            },
            BackendVerb::OpenConversation {
                conversation: ConversationEntry {
                    agent: "codex".into(),
                    id: "c1".into(),
                    app: "Codex".into(),
                    ..ConversationEntry::default()
                },
                force_new_tab: true,
            },
            BackendVerb::OpenSharedFile {
                path: "/home/dev/.agents/mcp.json".into(),
            },
            BackendVerb::KillOrphanPorts {
                orphans: vec![OrphanPort {
                    port: 8080,
                    pid: 1234,
                    ..OrphanPort::default()
                }],
            },
        ];

        for verb in verbs {
            let val = serde_json::to_value(&verb).unwrap();
            assert!(val.get("verb").is_some(), "missing verb tag in {val}");
            assert_all_keys_snake_case(&val);
            let rt: BackendVerb = serde_json::from_value(val).unwrap();
            assert_eq!(rt, verb);
        }

        let mut previews = BTreeMap::new();
        previews.insert("%1".into(), "line1\nline2".into());

        let results = vec![
            BackendVerbResult::Ack,
            BackendVerbResult::Previews { previews },
            BackendVerbResult::Pruned {
                summary: "pruned 2 worktrees".into(),
            },
            BackendVerbResult::KilledPorts { count: 3 },
        ];

        for res in results {
            let val = serde_json::to_value(&res).unwrap();
            assert!(val.get("kind").is_some(), "missing kind tag in {val}");
            assert_all_keys_snake_case(&val);
            let rt: BackendVerbResult = serde_json::from_value(val).unwrap();
            assert_eq!(rt, res);
        }
    }
}

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use ts_rs::TS;

use crate::ids::TimestampMs;

/// Filters items in the Shared MCP & Skills view.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize, TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum SharedKind {
    #[default]
    All,
    Mcp,
    Skills,
}

impl SharedKind {
    /// Returns the short pill label for a shared filter.
    pub const fn label(self) -> &'static str {
        match self {
            Self::All => "all",
            Self::Mcp => "mcp",
            Self::Skills => "skills",
        }
    }

    /// Returns all shared filter tabs in display order.
    pub const fn all() -> &'static [Self] {
        &[Self::All, Self::Mcp, Self::Skills]
    }
}

/// One skill discovered under `<shared>/skills/<dir>/SKILL.md`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct SharedSkill {
    pub name: String,
    pub dir_name: String,
    #[serde(default)]
    pub description: String,
    pub path: String,
    pub dir_path: String,
    #[serde(default)]
    pub disable_model_invocation: bool,
    #[serde(default)]
    pub paths: Vec<String>,
    #[serde(default)]
    pub icon: String,
    #[serde(default)]
    pub color: String,
    #[serde(default)]
    pub extra_files: u32,
    #[serde(default)]
    pub has_references: bool,
    #[serde(default)]
    pub has_scripts: bool,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub mod_time_ms: TimestampMs,
}

/// Shared MCP server entry discovered under `<shared>` or aggregated from sibling `~/.spaces` harness configs.
#[allow(clippy::upper_case_acronyms)]
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct SharedMCP {
    pub name: String,
    #[serde(default)]
    pub transport: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub exposure: String,
    #[serde(default)]
    pub disabled: bool,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    #[serde(default)]
    pub source_path: String,
    #[serde(default)]
    pub source_kind: String,
    #[serde(default)]
    pub harnesses: Vec<String>,
    #[serde(default)]
    pub raw_json: String,
}

impl SharedMCP {
    /// Returns a single-line human-readable command or URL representation.
    pub fn command_line(&self) -> String {
        let cmd = self.command.trim();
        if !cmd.is_empty() {
            if self.args.is_empty() {
                return cmd.to_owned();
            }
            return format!("{cmd} {}", self.args.join(" "));
        }
        self.url.trim().to_owned()
    }
}

/// Snapshot of all shared MCP servers and skills discovered under `root_dir`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, TS)]
#[ts(export)]
pub struct SharedSnapshot {
    pub root_dir: String,
    pub skills_dir: String,
    #[serde(default)]
    pub mcps: Vec<SharedMCP>,
    #[serde(default)]
    pub skills: Vec<SharedSkill>,
    #[serde(default)]
    pub loaded_at_ms: TimestampMs,
    #[serde(default)]
    pub root_exists: bool,
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
                    if key == "env" {
                        // `env` holds dynamic environment variable names such as `API_KEY`.
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
    fn shared_kind_and_snapshot_round_trip() {
        assert_eq!(SharedKind::Mcp.label(), "mcp");
        assert_eq!(SharedKind::all().len(), 3);

        let mut env = BTreeMap::new();
        env.insert("API_KEY".into(), "secret".into());

        let mcp = SharedMCP {
            name: "filesystem".into(),
            transport: "stdio".into(),
            command: "npx".into(),
            args: vec![
                "-y".into(),
                "@modelcontextprotocol/server-filesystem".into(),
            ],
            url: String::new(),
            description: "FS server".into(),
            exposure: "shared".into(),
            disabled: false,
            env,
            source_path: "/home/dev/.agents/mcp.json".into(),
            source_kind: "shared".into(),
            harnesses: vec!["claude".into(), "codex".into()],
            raw_json: "{}".into(),
        };
        assert_eq!(
            mcp.command_line(),
            "npx -y @modelcontextprotocol/server-filesystem"
        );
        let url_mcp = SharedMCP {
            url: "https://mcp.example.com/sse".into(),
            ..SharedMCP::default()
        };
        assert_eq!(url_mcp.command_line(), "https://mcp.example.com/sse");

        let snap = SharedSnapshot {
            root_dir: "/home/dev/.agents".into(),
            skills_dir: "/home/dev/.agents/skills".into(),
            mcps: vec![mcp],
            skills: vec![SharedSkill {
                name: "rust-review".into(),
                dir_name: "rust-review".into(),
                description: "Review Rust code".into(),
                path: "/home/dev/.agents/skills/rust-review/SKILL.md".into(),
                dir_path: "/home/dev/.agents/skills/rust-review".into(),
                disable_model_invocation: false,
                paths: vec!["crates/**/*.rs".into()],
                icon: "🦀".into(),
                color: "orange".into(),
                extra_files: 1,
                has_references: true,
                has_scripts: false,
                body: "Check types carefully.".into(),
                mod_time_ms: TimestampMs(1_700_000_000_000),
            }],
            loaded_at_ms: TimestampMs(1_700_000_010_000),
            root_exists: true,
        };

        let val = serde_json::to_value(&snap).unwrap();
        assert_all_keys_snake_case(&val);
        let rt: SharedSnapshot = serde_json::from_value(val).unwrap();
        assert_eq!(rt, snap);
    }
}

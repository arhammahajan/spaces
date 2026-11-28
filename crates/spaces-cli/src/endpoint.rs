use std::ffi::OsStr;
use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize};
use spaces_types::{BootId, TimestampMs};

const ENDPOINT_FILE_NAME: &str = "daemon.endpoint.json";
const EXPECTED_PROTOCOL: &str = "ndjson-rpc/uds";

/// Connection metadata published by `spacesd` in `<spaces_dir>/daemon.endpoint.json`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct DaemonEndpoint {
    pub version: u32,
    pub protocol: String,
    pub pid: u32,
    pub uid: u32,
    pub socket: String,
    pub token: String,
    pub boot_id: BootId,
    pub started_ms: TimestampMs,
}

/// Resolves the active Spaces state directory (`--spaces-dir` -> `SPACES_DIR` -> `$HOME/.spaces`).
pub fn resolve_spaces_dir(override_dir: Option<&Path>) -> PathBuf {
    let spaces_dir_env = std::env::var_os("SPACES_DIR");
    let home_env = std::env::var_os("HOME");
    resolve_spaces_dir_from(override_dir, spaces_dir_env.as_deref(), home_env.as_deref())
}

fn resolve_spaces_dir_from(
    override_dir: Option<&Path>,
    spaces_dir_env: Option<&OsStr>,
    home_env: Option<&OsStr>,
) -> PathBuf {
    if let Some(dir) = override_dir
        && !dir.as_os_str().is_empty()
    {
        return dir.to_path_buf();
    }

    if let Some(env_val) = spaces_dir_env {
        match env_val.to_str() {
            Some(s) => {
                let trimmed = s.trim();
                if !trimmed.is_empty() {
                    return PathBuf::from(trimmed);
                }
            }
            None if !env_val.is_empty() => return PathBuf::from(env_val),
            None => {}
        }
    }

    if let Some(home) = home_env
        && !home.is_empty()
    {
        return PathBuf::from(home).join(spaces_types::SPACES_CONFIG_DIR_NAME);
    }

    PathBuf::from(spaces_types::SPACES_CONFIG_DIR_NAME)
}

/// Reads and validates `<spaces_dir>/daemon.endpoint.json`.
pub fn load_endpoint(spaces_dir: &Path) -> Result<DaemonEndpoint> {
    let endpoint_path = spaces_dir.join(ENDPOINT_FILE_NAME);
    let raw = fs::read_to_string(&endpoint_path).with_context(|| {
        format!(
            "failed to read daemon endpoint at {} (is spacesd running?)",
            endpoint_path.display()
        )
    })?;

    let endpoint: DaemonEndpoint = serde_json::from_str(&raw).with_context(|| {
        format!(
            "failed to parse daemon endpoint JSON at {}",
            endpoint_path.display()
        )
    })?;

    if endpoint.protocol != EXPECTED_PROTOCOL {
        bail!(
            "unsupported daemon protocol '{}' in {} (expected '{}')",
            endpoint.protocol,
            endpoint_path.display(),
            EXPECTED_PROTOCOL
        );
    }
    if endpoint.socket.trim().is_empty() {
        bail!(
            "daemon endpoint at {} has an empty socket path",
            endpoint_path.display()
        );
    }
    if endpoint.token.trim().is_empty() {
        bail!(
            "daemon endpoint at {} has an empty auth token",
            endpoint_path.display()
        );
    }

    Ok(endpoint)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn unique_temp_dir(prefix: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "spaces-cli-endpoint-{prefix}-{}",
            uuid::Uuid::new_v4()
        ));
        fs::create_dir_all(&dir).expect("create temp dir");
        dir
    }

    #[test]
    fn resolves_spaces_dir_by_precedence() {
        let override_path = Path::new("/custom/spaces");
        assert_eq!(
            resolve_spaces_dir_from(
                Some(override_path),
                Some(OsStr::new("/env/spaces")),
                Some(OsStr::new("/home/alice")),
            ),
            PathBuf::from("/custom/spaces")
        );

        assert_eq!(
            resolve_spaces_dir_from(
                None,
                Some(OsStr::new("  /env/spaces  ")),
                Some(OsStr::new("/home/alice")),
            ),
            PathBuf::from("/env/spaces")
        );

        assert_eq!(
            resolve_spaces_dir_from(
                None,
                Some(OsStr::new("   ")),
                Some(OsStr::new("/home/alice")),
            ),
            PathBuf::from("/home/alice/.spaces")
        );

        assert_eq!(
            resolve_spaces_dir_from(None, None, Some(OsStr::new("/home/alice"))),
            PathBuf::from("/home/alice/.spaces")
        );
    }

    #[test]
    fn loads_and_validates_daemon_endpoint_json() {
        let dir = unique_temp_dir("valid");
        let endpoint_path = dir.join("daemon.endpoint.json");
        let payload = serde_json::json!({
            "version": 1,
            "protocol": "ndjson-rpc/uds",
            "pid": 4242,
            "uid": 1000,
            "socket": dir.join("daemon.sock").to_string_lossy(),
            "token": "0123456789abcdef",
            "boot_id": "boot_test_1",
            "started_ms": 1_700_000_000_000i64,
        });
        fs::write(
            &endpoint_path,
            serde_json::to_string_pretty(&payload).unwrap(),
        )
        .expect("write endpoint");

        let loaded = load_endpoint(&dir).expect("load valid endpoint");
        assert_eq!(loaded.version, 1);
        assert_eq!(loaded.protocol, "ndjson-rpc/uds");
        assert_eq!(loaded.pid, 4242);
        assert_eq!(loaded.uid, 1000);
        assert_eq!(loaded.token, "0123456789abcdef");
        assert_eq!(loaded.boot_id, BootId::from("boot_test_1"));
        assert_eq!(loaded.started_ms, TimestampMs(1_700_000_000_000));

        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn rejects_missing_or_invalid_endpoint() {
        let dir = unique_temp_dir("invalid");
        let err = load_endpoint(&dir).expect_err("missing endpoint should fail");
        assert!(err.to_string().contains("daemon.endpoint.json"));

        fs::write(dir.join("daemon.endpoint.json"), "{not-json").expect("write malformed");
        assert!(load_endpoint(&dir).is_err());

        let bad_proto = serde_json::json!({
            "version": 1,
            "protocol": "http/tcp",
            "pid": 1,
            "uid": 1000,
            "socket": "/tmp/s.sock",
            "token": "abc",
            "boot_id": "boot_1",
            "started_ms": 123,
        });
        fs::write(
            dir.join("daemon.endpoint.json"),
            serde_json::to_string(&bad_proto).unwrap(),
        )
        .expect("write bad protocol");
        let proto_err = load_endpoint(&dir).expect_err("bad protocol should fail");
        assert!(
            proto_err
                .to_string()
                .contains("unsupported daemon protocol")
        );

        fs::remove_dir_all(&dir).ok();
    }
}

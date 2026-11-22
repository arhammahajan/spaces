package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// ShadowVar maps every configured canonical agent to the primary environment
// variable that redirects its isolated shadow home or credential store. Claude
// uses CLAUDE_CONFIG_DIR and never overrides HOME, keeping the macOS login
// keychain reachable. Only verified agents are mapped; unknown agents return false.
func ShadowVar(agent string) (string, bool) {
	switch agent {
	case "codex":
		return "CODEX_HOME", true
	case "claude":
		return "CLAUDE_CONFIG_DIR", true
	case "grok":
		return "GROK_HOME", true
	case "antigravity":
		return "ANTIGRAVITY_GEMINI_DIR", true
	case "gemini":
		return "GEMINI_CLI_HOME", true
	case "copilot":
		return "COPILOT_HOME", true
	case "deepseek":
		return "DSH_HOME", true
	case "cline":
		return "CLINE_DATA_DIR", true
	case "opencode", "kilo":
		return "XDG_DATA_HOME", true
	case "pi":
		return "PI_CODING_AGENT_DIR", true
	case "qwen":
		return "QWEN_HOME", true
	case "kimi":
		return "KIMI_CODE_HOME", true
	case "kimi-cli":
		return "KIMI_SHARE_DIR", true
	case "mcode":
		return "MINIMAX_DATA_DIR", true
	case "muse":
		return "MUSE_AUTH_PATH", true
	case "fx":
		return "FX_HOME", true
	case "vibe":
		return "VIBE_HOME", true
	default:
		return "", false
	}
}

// ShadowEnv returns the primary environment variable name and value to set for
// an agent's isolated shadow home. Most agents take the shadow directory
// directly; Muse takes the per-profile auth.json file path via MUSE_AUTH_PATH.
func ShadowEnv(agent, shadowHome string) (key, val string, ok bool) {
	v, ok := ShadowVar(agent)
	if !ok || shadowHome == "" {
		return "", "", false
	}
	if agent == "muse" {
		return v, filepath.Join(shadowHome, "auth.json"), true
	}
	return v, shadowHome, true
}

// ShadowEnvMap returns the home redirects for an isolated agent launch.
//
// antigravity (the agy CLI) ignores ANTIGRAVITY_GEMINI_DIR,
// ANTIGRAVITY_APP_DATA_DIR, and ANTIGRAVITY_CREDENTIAL_SCOPE when HOME and
// D-Bus are real, so the agy process itself requires HOME, XDG_*, and
// DBUS_SESSION_BUS_ADDRESS=disabled: for profile isolation. Resolve pairs
// those overrides with HARNESS_REAL_* + BASH_ENV so child bash shells spawned
// by agy restore the user's real HOME, XDG_*, and D-Bus session.
func ShadowEnvMap(agent, shadowHome string) map[string]string {
	k, v, ok := ShadowEnv(agent, shadowHome)
	if !ok {
		return nil
	}
	out := map[string]string{k: v}
	switch agent {
	case "antigravity":
		out["HOME"] = shadowHome
		out["XDG_CONFIG_HOME"] = filepath.Join(shadowHome, ".config")
		out["XDG_DATA_HOME"] = filepath.Join(shadowHome, ".local", "share")
		out["XDG_STATE_HOME"] = filepath.Join(shadowHome, ".local", "state")
		out["XDG_CACHE_HOME"] = filepath.Join(shadowHome, ".cache")
		out["ANTIGRAVITY_GEMINI_DIR"] = filepath.Join(shadowHome, ".gemini")
		out["GEMINI_HOME"] = out["ANTIGRAVITY_GEMINI_DIR"]
		out["DBUS_SESSION_BUS_ADDRESS"] = "disabled:"
		sum := sha256.Sum256([]byte(filepath.Clean(shadowHome)))
		out["ANTIGRAVITY_CREDENTIAL_SCOPE"] = "harness-profile-" + hex.EncodeToString(sum[:6])
	}
	return out
}

// RestoreEnvScriptName is the filename of the harness-managed BASH_ENV helper
// script under $HOME/.harness/ that restores real user HOME, XDG_*, and
// D-Bus variables for child bash shells spawned by shadow-HOME agents.
const RestoreEnvScriptName = "harness-restore-env.sh"

// RestoreEnvScriptPath returns the path to the BASH_ENV helper script under
// paths.Home/.harness/.
func RestoreEnvScriptPath(paths harnessenv.Paths) string {
	return filepath.Join(harnessenv.HarnessDir(paths), RestoreEnvScriptName)
}

const restoreEnvScriptBody = `# Managed by harness: restore real user HOME, XDG, and D-Bus for child
# bash shells spawned by shadow-HOME agents (e.g. antigravity CLI).
if [ -n "${HARNESS_REAL_HOME:-}" ]; then
    export HOME="$HARNESS_REAL_HOME"
fi
if [ -n "${HARNESS_REAL_XDG_CONFIG_HOME:-}" ]; then
    export XDG_CONFIG_HOME="$HARNESS_REAL_XDG_CONFIG_HOME"
fi
if [ -n "${HARNESS_REAL_XDG_DATA_HOME:-}" ]; then
    export XDG_DATA_HOME="$HARNESS_REAL_XDG_DATA_HOME"
fi
if [ -n "${HARNESS_REAL_XDG_STATE_HOME:-}" ]; then
    export XDG_STATE_HOME="$HARNESS_REAL_XDG_STATE_HOME"
fi
if [ -n "${HARNESS_REAL_XDG_CACHE_HOME:-}" ]; then
    export XDG_CACHE_HOME="$HARNESS_REAL_XDG_CACHE_HOME"
fi
if [ -n "${HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS:-}" ]; then
    export DBUS_SESSION_BUS_ADDRESS="$HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS"
elif [ "${DBUS_SESSION_BUS_ADDRESS:-}" = "disabled:" ]; then
    unset DBUS_SESSION_BUS_ADDRESS
fi
if [ -n "${HARNESS_PREV_BASH_ENV:-}" ]; then
    export BASH_ENV="$HARNESS_PREV_BASH_ENV"
    unset HARNESS_PREV_BASH_ENV
    if [ -r "$BASH_ENV" ]; then
        . "$BASH_ENV"
    fi
else
    unset BASH_ENV
fi
`

// EnsureRestoreEnvScript writes the BASH_ENV restore helper script atomically
// when missing or stale and returns its path.
func EnsureRestoreEnvScript(paths harnessenv.Paths) (string, error) {
	p := RestoreEnvScriptPath(paths)
	want := []byte(restoreEnvScriptBody)
	if existing, err := os.ReadFile(p); err == nil && bytes.Equal(existing, want) {
		return p, nil
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("agents: create dir for %s: %w", p, err)
	}
	tmp, err := os.CreateTemp(dir, ".harness-restore-env-*.tmp")
	if err != nil {
		return "", fmt.Errorf("agents: create temp %s: %w", p, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(want); err != nil {
		tmp.Close()
		return "", fmt.Errorf("agents: write %s: %w", p, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", fmt.Errorf("agents: chmod %s: %w", p, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agents: close %s: %w", p, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return "", fmt.Errorf("agents: publish %s: %w", p, err)
	}
	return p, nil
}

// Package agents is the agent registry and launcher for the harness CLI.
// It ports the launch behavior of the superseded harness shell script:
// per-agent binaries, router-derived environment, and argument handling.
//
// Resolve handles bare `harness <agent>` launches. ResolveProfile handles
// profile launches and additionally prepends the profile-mode PATH
// augmentation ($DataHome/fnm, $DataHome/fnm/aliases/default/bin,
// $DataHome/deepseek-harness/bin, $DataHome/pnpm/bin) required by the
// contract. The augmentation lives only in ResolveProfile so bare agent
// launches never inherit profile PATH entries; both paths are covered by
// tests.
package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// aliases maps non-canonical agent names to their canonical ids.
var aliases = map[string]string{
	"agy":       "antigravity",
	"dsh":       "deepseek",
	"kimi-code": "kimi",
}

// known lists every canonical agent id, sorted.
var known = []string{
	"antigravity",
	"claude",
	"cline",
	"codex",
	"copilot",
	"deepseek",
	"fx",
	"gemini",
	"grok",
	"kilo",
	"kimi",
	"kimi-cli",
	"mcode",
	"muse",
	"opencode",
	"pi",
	"qwen",
	"vibe",
}

// notes holds the short per-agent note shown by `spaces agents`.
var notes = map[string]string{
	"antigravity": "Antigravity CLI against the router, configured default model",
	"claude":      "Claude Code against the router (ANTHROPIC_BASE_URL)",
	"cline":       "Cline via fnm node with a router session key",
	"codex":       "Codex with Model Router provider -c overrides",
	"copilot":     "Copilot with router provider settings",
	"deepseek":    "Deepseek harness (web UI by default)",
	"fx":          "fx router binary with a router session",
	"gemini":      "Gemini CLI from a source checkout via node",
	"grok":        "Grok CLI with router model endpoints",
	"kilo":        "Kilo, plain exec",
	"kimi":        "Kimi, plain exec with its own provider settings",
	"kimi-cli":    "Kimi CLI against the router OpenAI endpoint",
	"mcode":       "mcode, plain exec with its own provider settings",
	"muse":        "Meta muse against the router key",
	"opencode":    "OpenCode with a private --standalone server per TUI",
	"pi":          "Pi, plain exec with its own provider settings",
	"qwen":        "Qwen, plain exec with its own provider settings",
	"vibe":        "Mistral vibe with a router session",
}

// Known returns the canonical agent ids, sorted.
func Known() []string {
	out := make([]string, len(known))
	copy(out, known)
	return out
}

// Normalize resolves aliases (agy, dsh, kimi-code) and reports whether
// name is a known agent.
func Normalize(name string) (string, bool) {
	if canon, ok := aliases[name]; ok {
		name = canon
	}
	for _, k := range known {
		if name == k {
			return name, true
		}
	}
	return "", false
}

// AgentInfo describes one agent for `harness agents`. The structs are
// JSON-tagged so --json output uses the same typed shape.
type AgentInfo struct {
	Name        string `json:"name"`
	Binary      string `json:"binary"`
	BinaryState string `json:"binaryState"` // "ok" or "missing"
	Note        string `json:"note"`
}

// Infos returns one AgentInfo per known agent. BinaryState is "ok" when the
// agent's default binary exists and is executable, "missing" otherwise.
func Infos(paths harnessenv.Paths) []AgentInfo {
	out := make([]AgentInfo, 0, len(known))
	for _, name := range known {
		bin, err := defaultBinary(name, paths)
		if err != nil {
			bin = ""
		}
		state := "missing"
		if isExecutable(bin) {
			state = "ok"
		}
		out = append(out, AgentInfo{
			Name:        name,
			Binary:      bin,
			BinaryState: state,
			Note:        notes[name],
		})
	}
	return out
}

// Resolved is the exact process a launch will exec: the binary, the full
// argv after it, "K=V" entries to set, directories prepended to PATH, and
// environment names to remove.
type Resolved struct {
	Binary      string
	Args        []string
	Env         []string
	PathPrepend []string
	UnsetEnv    []string
}

// CommandRunner executes external helper commands (currently fnm for
// cline). It is a package variable so tests can inject a fake; the default
// runs the real command and returns its stdout.
var CommandRunner = defaultCommandRunner

func defaultCommandRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// isExecutable reports whether path names an existing executable file, or
// resolves via PATH when path holds no slash.
func isExecutable(path string) bool {
	if path == "" {
		return false
	}
	if !strings.Contains(path, "/") {
		_, err := exec.LookPath(path)
		return err == nil
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0o111 != 0
}

// DefaultBinary returns the agent's default executable path (accepting aliases)
// without side effects or profile overrides.
func DefaultBinary(agent string, paths harnessenv.Paths) (string, error) {
	canon, ok := Normalize(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agent)
	}
	return defaultBinary(canon, paths)
}

// defaultBinary returns the agent's default executable without side effects
// (no network, no session tokens), so Infos can stay read-only. A non-empty
// LaunchSpec.Binary replaces this value at Resolve time.
func defaultBinary(agent string, paths harnessenv.Paths) (string, error) {
	home := paths.Home
	switch agent {
	case "claude", "opencode", "pi", "qwen", "kimi", "mcode", "copilot":
		return agent, nil
	case "codex", "grok", "muse":
		return filepath.Join(home, ".local", "bin", agent), nil
	case "cline":
		out, err := CommandRunner("fnm", "exec", "--using=default", "which", "node")
		if err != nil {
			return "", fmt.Errorf("cline: resolve node via fnm: %w", err)
		}
		nodeBin := strings.TrimSpace(out)
		if nodeBin == "" {
			return "", fmt.Errorf("cline: fnm returned an empty node path")
		}
		return filepath.Join(filepath.Dir(nodeBin), "cline"), nil
	case "kimi-cli":
		return filepath.Join(home, ".local", "share", "kimi-cli", "bin", "kimi-cli"), nil
	case "kilo":
		return filepath.Join(home, ".kilo", "bin", "kilo"), nil
	case "fx":
		return filepath.Join(home, ".local", "libexec", "fx", "fx-router"), nil
	case "deepseek":
		return filepath.Join(home, ".local", "share", "deepseek-harness", "bin", "dsh"), nil
	case "vibe":
		return filepath.Join(home, ".local", "share", "uv", "tools", "mistral-vibe", "bin", "vibe"), nil
	case "gemini":
		return "node", nil
	case "antigravity":
		return filepath.Join(home, ".local", "libexec", "antigravity", "agy"), nil
	}
	return "", fmt.Errorf("unknown agent: %s", agent)
}

// defaultAntigravityModel reads the configured default model from
// $Home/.gemini/antigravity-cli/settings.json, falling back to
// gemini-4-argon when the file is missing, unreadable, or has no string
// model field.
func defaultAntigravityModel(home string) string {
	const fallback = "gemini-4-argon"
	data, err := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		return fallback
	}
	var settings struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(data, &settings); err != nil || settings.Model == "" {
		return fallback
	}
	return settings.Model
}

// needsStandalone reports whether opencode should run with --standalone:
// the first arg is missing, empty, flag-like, or "mini", and none of the
// args already selects a server mode.
func needsStandalone(args []string) bool {
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	if !(first == "" || strings.HasPrefix(first, "-") || first == "mini") {
		return false
	}
	for _, a := range args {
		if a == "--standalone" || a == "--server" || strings.HasPrefix(a, "--server=") {
			return false
		}
	}
	return true
}

// hasFlag reports whether args carry a flag as a separate or inline value.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// envList flattens an env map into sorted "K=V" entries for determinism.
func envList(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// dedupeStrings drops duplicates, keeping the first occurrence.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

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
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// aliases maps non-canonical agent names to their canonical ids.
var aliases = map[string]string{
	"agy":       "antigravity",
	"dsh":       "deepseek",
	"kimi-code": "kimi",
}

// known lists every canonical agent id, sorted. antigravity-native is
// included: it is profile-only (its binary arrives via LaunchSpec.Binary),
// but Resolve accepts it, so the registry and the resolver stay consistent.
var known = []string{
	"antigravity",
	"antigravity-native",
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

// notes holds the short per-agent note shown by `harness agents`.
var notes = map[string]string{
	"antigravity":        "Antigravity CLI against the router, configured default model",
	"antigravity-native": "Profile-only: plain exec of the profile-provided native binary",
	"claude":             "Claude Code against the router (ANTHROPIC_BASE_URL)",
	"cline":              "Cline via fnm node with a router session key",
	"codex":              "Codex with Model Router provider -c overrides",
	"copilot":            "Copilot with router provider settings",
	"deepseek":           "Deepseek harness (web UI by default)",
	"fx":                 "fx router binary with a router session",
	"gemini":             "Gemini CLI from a source checkout via node",
	"grok":               "Grok CLI with router model endpoints",
	"kilo":               "Kilo, plain exec",
	"kimi":               "Kimi, plain exec with its own provider settings",
	"kimi-cli":           "Kimi CLI against the router OpenAI endpoint",
	"mcode":              "mcode, plain exec with its own provider settings",
	"muse":               "Meta muse against the router key",
	"opencode":           "OpenCode with a private --standalone server per TUI",
	"pi":                 "Pi, plain exec with its own provider settings",
	"qwen":               "Qwen, plain exec with its own provider settings",
	"vibe":               "Mistral vibe with a router session",
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

// FetchModelIDs retrieves model ids from the router's /v1/models endpoint.
// It is a package variable so tests can inject a fake; the default performs
// a real GET with a 10s timeout and tolerates both {data:[...]} and bare
// [...] response shapes.
var FetchModelIDs = defaultFetchModelIDs

func defaultFetchModelIDs(modelsURL string) ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", modelsURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var obj struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		var bare []struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(body, &bare); err2 != nil {
			return nil, err
		}
		obj.Data = bare
	}
	ids := make([]string, 0, len(obj.Data))
	for _, m := range obj.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
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
	case "antigravity-native":
		return "", nil
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

// findACPServer locates the Antigravity ACP server binary under
// $HOME/.harness/tools/antigravity-acp/linux-x64, honoring active.json's
// releaseId first (tolerant of any read/parse problem) and otherwise
// picking the newest versions/*/agy_acp_server.par by (mtime, path).
func findACPServer(paths harnessenv.Paths) (string, bool) {
	install := filepath.Join(harnessenv.HarnessDir(paths), "tools", "antigravity-acp", "linux-x64")
	if data, err := os.ReadFile(filepath.Join(install, "active.json")); err == nil {
		var active struct {
			ReleaseID string `json:"releaseId"`
		}
		if json.Unmarshal(data, &active) == nil && active.ReleaseID != "" {
			cand := filepath.Join(install, "versions", active.ReleaseID, "agy_acp_server.par")
			if isExecutable(cand) {
				return cand, true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(install, "versions"))
	if err != nil {
		return "", false
	}
	best := ""
	var bestTime time.Time
	for _, e := range entries {
		cand := filepath.Join(install, "versions", e.Name(), "agy_acp_server.par")
		st, err := os.Stat(cand)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		if best == "" || st.ModTime().After(bestTime) ||
			(st.ModTime().Equal(bestTime) && cand > best) {
			best, bestTime = cand, st.ModTime()
		}
	}
	return best, best != ""
}

// gatewayModels returns the comma-joined model ids for
// AGY_LLM_GATEWAY_MODELS: the ids from the router with the default model
// first (stable otherwise), degrading to the default model alone when the
// fetch fails.
func gatewayModels(gatewayURL, def string) string {
	ids, err := FetchModelIDs(gatewayURL + "/v1/models")
	if err != nil {
		return def
	}
	var out []string
	for _, id := range ids {
		if id == def {
			out = append(out, id)
		}
	}
	for _, id := range ids {
		if id != def {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return def
	}
	return strings.Join(out, ",")
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

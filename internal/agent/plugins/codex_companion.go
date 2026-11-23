package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	pluginsfs "github.com/arhammahajan/spaces/plugins"
)

const (
	codexCompanionSource = "codex-spaces"
	codexCompanionVer    = "1.0.0"
)

var codexCompanionCaps = []string{
	"lifecycle", "usage", "context", "tools", "files", "subagents",
	"permissions", "rate_limits", "otel", "sandbox", "errors",
}

// codexCompanion implements agent.CompanionPlugin for Codex.
type codexCompanion struct{}

func NewCodexCompanion() agent.CompanionPlugin { return codexCompanion{} }

func (codexCompanion) CompanionSpec() agent.CompanionSpec {
	return agent.CompanionSpec{
		AgentID:      "codex",
		PluginName:   "codex-spaces",
		Version:      codexCompanionVer,
		InstallDir:   ".codex/plugins/codex-spaces",
		Description:  "Codex hooks.json lifecycle + legacy notify + OTLP telemetry bridge: usage/context/rate-limit/sandbox/file telemetry over UDS HTTP.",
		Capabilities: append([]string(nil), codexCompanionCaps...),
		Runtime:      "node/esm",
	}
}

// codexEventState maps Codex native hook/legacy/OTLP event names (emitted
// by plugins/codex-spaces) to canonical (state, kind), mirroring
// stateForEvent in scripts/hook-bridge.mjs plus the OTel bridge event set.
// Explicit payload state/kind wins; this is the fallback.
func codexEventState(raw string, payload map[string]any) (state, kind string) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case lower == "permissionrequest" || lower == "permission_request":
		return "blocked", "permission"
	case lower == "sessionstart" || lower == "session_start" ||
		lower == "sessionend" || lower == "session_end":
		return "idle", ""
	case lower == "userpromptsubmit" || lower == "user_prompt_submit" ||
		lower == "pretooluse" || lower == "pre_tool_use" ||
		lower == "posttooluse" || lower == "post_tool_use":
		return "working", ""
	case lower == "stop" || lower == "agent-turn-complete" || lower == "agent_turn_complete" ||
		lower == "turn-complete" || lower == "turn_complete":
		if strings.TrimSpace(companionStr(payload, "error", "error_details", "errorDetails", "error_message", "errorMessage", "reason", "last_error", "lastError")) != "" {
			return "failed", ""
		}
		return "done", ""
	// OTLP bridge events (otel-bridge.mjs buildOtelPayload) and native
	// codex.* telemetry names: pure telemetry, never flips the pane.
	case lower == "response" || lower == "post_tool" || lower == "prompt" || lower == "tool_failure" ||
		strings.HasPrefix(lower, "codex."):
		return "working", ""
	default:
		return "working", ""
	}
}

func (codexCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	if envelope == nil {
		return nil, false
	}
	if a, ok := envelope["agent"].(string); ok && strings.TrimSpace(a) != "" &&
		!strings.EqualFold(strings.TrimSpace(a), "codex") {
		return nil, false
	}
	spec := codexCompanion{}.CompanionSpec()
	out, payload := baseCompanionEvent(spec, codexCompanionCaps, envelope)

	rawEvent := companionMergedStr(envelope, payload, "event", "hook_event_name", "hookEventName", "type")
	if rawEvent == "" {
		// Legacy notify payloads arrive as argv JSON: the bridge folds
		// {"type":"agent-turn-complete"} into a Stop.
		if t := companionMergedStr(envelope, payload, "notify_type"); t != "" {
			rawEvent = t
			if strings.Contains(strings.ToLower(t), "turn-complete") || strings.Contains(strings.ToLower(t), "turn_complete") {
				rawEvent = "agent-turn-complete"
			}
		}
	}
	if rawEvent == "" {
		rawEvent = "working"
	}
	out["event"] = strings.ToLower(strings.TrimSpace(rawEvent))

	state := normalizeCompanionState(companionMergedStr(envelope, payload, "state"))
	kind := strings.TrimSpace(companionMergedStr(envelope, payload, "kind"))
	if state == "" {
		state, kind = codexEventState(rawEvent, payload)
	}
	out["state"] = state
	if kind != "" {
		out["kind"] = kind
	} else {
		out["kind"] = nil
	}

	// Subagent routing marker from the hook bridge (agent_id /
	// parent_thread_id present on child activity).
	if v := companionMergedStr(envelope, payload, "agent_id", "agentId"); v != "" {
		out["agent_id"] = v
	}
	inferSubagent(out, payload)
	out["source"] = "plugin"
	return out, true
}

// codexResolvedHooks renders the embedded hooks.json template with the
// installed absolute plugin path so the bridge commands resolve without the
// user hand-editing <PLUGIN_DIR>.
func codexResolvedHooks(dir string) (string, error) {
	raw, err := pluginsfs.FS.ReadFile(codexCompanionSource + "/hooks.json")
	if err != nil {
		return "", fmt.Errorf("read embedded hooks.json: %w", err)
	}
	return strings.ReplaceAll(string(raw), "<PLUGIN_DIR>", dir), nil
}

func (codexCompanion) InstallCompanion(opts agent.CompanionInstallOptions) (agent.CompanionInstallResult, error) {
	spec := codexCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	prev := readInstalledVersion(dir)
	created := !dirExists(dir)
	if err := copyEmbedTree(codexCompanionSource, dir, false); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("install codex-spaces: %w", err)
	}
	// Render a resolved hooks.json inside the plugin directory. The user's
	// own ~/.codex/hooks.json and ~/.codex/config.toml are never touched:
	// DetectCompanionStatus tells them how to point at this file.
	resolved, err := codexResolvedHooks(dir)
	if err != nil {
		return agent.CompanionInstallResult{}, err
	}
	if err := writeFileAtomic(filepath.Join(dir, "hooks.json"), []byte(resolved), 0o644); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("write resolved hooks.json: %w", err)
	}
	ver := readEmbedVersion(codexCompanionSource)
	if ver == "" {
		ver = spec.Version
	}
	return installResult(dir, prev, ver, created), nil
}

func (codexCompanion) UninstallCompanion(opts agent.CompanionInstallOptions) error {
	spec := codexCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	if dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	// ~/.codex/hooks.json and config.toml are user-owned: never touched.
	return nil
}

// codexWiringNote inspects (read-only) the user's hooks/config for a
// codex-spaces reference. It never writes.
func codexWiringNote(home, dir string) string {
	if strings.TrimSpace(home) == "" {
		return "wiring unknown (no home)"
	}
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	wired := false
	if data, err := os.ReadFile(hooksPath); err == nil {
		if strings.Contains(string(data), "codex-spaces") {
			wired = true
		}
	}
	note := fmt.Sprintf("resolved hooks template at %s", filepath.Join(dir, "hooks.json"))
	if wired {
		note += "; ~/.codex/hooks.json references codex-spaces"
	} else {
		note += "; ~/.codex/hooks.json not wired (merge or copy the template; existing user hooks are never overwritten)"
	}
	return note
}

func (codexCompanion) DetectCompanionStatus(homeDir string) agent.CompanionStatus {
	home := companionHome(agent.CompanionInstallOptions{HomeDir: homeDir})
	spec := codexCompanion{}.CompanionSpec()
	dir := companionTargetDir(agent.CompanionInstallOptions{HomeDir: homeDir}, spec)
	if strings.TrimSpace(homeDir) != "" && fileExists(filepath.Join(strings.TrimSpace(homeDir), "scripts", "hook-bridge.mjs")) {
		dir = strings.TrimSpace(homeDir)
	}
	installed := fileExists(filepath.Join(dir, "scripts", "hook-bridge.mjs"))
	ver := ""
	if installed {
		ver = readInstalledVersion(dir)
		if ver == "" {
			ver = spec.Version
		}
	}
	detail := fmt.Sprintf("plugin dir %s", dir)
	wiring := codexWiringNote(home, dir)
	active := false
	if installed {
		detail += "; " + wiring
		active = strings.Contains(wiring, "references codex-spaces")
	} else {
		detail = fmt.Sprintf("not installed (expected at %s); %s", dir, wiring)
	}
	return agent.CompanionStatus{
		Installed: installed && dirExists(dir),
		Path:      dir,
		Version:   ver,
		Active:    active,
		Detail:    detail,
	}
}

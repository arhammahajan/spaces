package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	piCompanionSource = "pi-spaces"
	piCompanionVer    = "1.0.0"
)

var piCompanionCaps = []string{
	"lifecycle", "usage", "context", "tools", "files",
	"compaction", "errors", "model", "thinking", "session_tree",
}

// piCompanion implements agent.CompanionPlugin for Pi.
type piCompanion struct{}

func NewPiCompanion() agent.CompanionPlugin { return piCompanion{} }

func (piCompanion) CompanionSpec() agent.CompanionSpec {
	return agent.CompanionSpec{
		AgentID:      "pi",
		PluginName:   "pi-spaces",
		Version:      piCompanionVer,
		InstallDir:   ".pi/agent/extensions/pi-spaces",
		Description:  "Rich Pi companion extension: ExtensionAPI lifecycle, live context usage, thinking level, usage/cost, files, tools, compaction and session-tree telemetry over UDS HTTP.",
		Capabilities: append([]string(nil), piCompanionCaps...),
		Runtime:      "pi/ts",
	}
}

// piEventState maps Pi ExtensionAPI event names (emitted by
// plugins/pi-spaces) to canonical (state, kind). Explicit payload
// state/kind always wins; this is the fallback.
func piEventState(raw string) (state, kind string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "session_start", "session_shutdown":
		return "idle", ""
	case "session_switch", "session_fork", "session_tree",
		"input", "before_agent_start", "agent_start",
		"turn_start", "tool_call", "tool_execution_start",
		"tool_result", "tool_execution_end",
		"session_before_compact", "session_compact", "model_select":
		return "working", ""
	case "agent_end", "turn_end":
		return "done", ""
	default:
		return "working", ""
	}
}

func (piCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	if envelope == nil {
		return nil, false
	}
	if a, ok := envelope["agent"].(string); ok && strings.TrimSpace(a) != "" &&
		!strings.EqualFold(strings.TrimSpace(a), "pi") {
		return nil, false
	}
	spec := piCompanion{}.CompanionSpec()
	out, payload := baseCompanionEvent(spec, piCompanionCaps, envelope)

	rawEvent := companionMergedStr(envelope, payload, "event", "hook_event_name", "hookEventName", "type", "_event")
	if rawEvent == "" {
		rawEvent = "working"
	}
	out["event"] = strings.ToLower(strings.TrimSpace(rawEvent))

	state := normalizeCompanionState(companionMergedStr(envelope, payload, "state"))
	kind := strings.TrimSpace(companionMergedStr(envelope, payload, "kind"))
	if state == "" {
		state, kind = piEventState(rawEvent)
	}
	out["state"] = state
	if kind != "" {
		out["kind"] = kind
	} else {
		out["kind"] = nil
	}

	// Pi extras beyond the shared schema: the thinking level / effort the
	// model is running at. Preserved top-level so consumers can display it
	// without digging into extras.
	if thinking := companionMergedStr(envelope, payload, "thinking_level", "thinkingLevel", "thinking"); thinking != "" {
		out["thinking_level"] = truncateCompanion(thinking, companionMaxModel)
	} else if effort, ok := out["effort"].(string); ok && effort != "" {
		out["thinking_level"] = effort
	}
	out["source"] = "plugin"
	inferSubagent(out, payload)
	return out, true
}

func (piCompanion) InstallCompanion(opts agent.CompanionInstallOptions) (agent.CompanionInstallResult, error) {
	spec := piCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	prev := readInstalledVersion(dir)
	created := !dirExists(dir)
	if err := copyEmbedTree(piCompanionSource, dir, false); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("install pi-spaces: %w", err)
	}
	ver := readEmbedVersion(piCompanionSource)
	if ver == "" {
		ver = spec.Version
	}
	return installResult(dir, prev, ver, created), nil
}

func (piCompanion) UninstallCompanion(opts agent.CompanionInstallOptions) error {
	spec := piCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	if dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	return nil
}

func (piCompanion) DetectCompanionStatus(homeDir string) agent.CompanionStatus {
	spec := piCompanion{}.CompanionSpec()
	dir := companionTargetDir(agent.CompanionInstallOptions{HomeDir: homeDir}, spec)
	if strings.TrimSpace(homeDir) != "" && fileExists(filepath.Join(strings.TrimSpace(homeDir), "index.ts")) {
		dir = strings.TrimSpace(homeDir)
	}
	installed := fileExists(filepath.Join(dir, "index.ts")) || fileExists(filepath.Join(dir, "index.js"))
	ver := ""
	if installed {
		ver = readInstalledVersion(dir)
		if ver == "" {
			ver = spec.Version
		}
	}
	detail := fmt.Sprintf("extension dir %s", dir)
	if installed {
		detail += " (loaded automatically by Pi from ~/.pi/agent/extensions/)"
	} else {
		detail = fmt.Sprintf("not installed (expected at %s)", dir)
	}
	return agent.CompanionStatus{
		Installed: installed && dirExists(dir),
		Path:      dir,
		Version:   ver,
		Active:    installed && dirExists(dir),
		Detail:    detail,
	}
}

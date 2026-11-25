package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	opencodeCompanionSource = "opencode-spaces"
	opencodeCompanionVer    = "1.0.0"
)

var opencodeCompanionCaps = []string{
	"lifecycle", "usage", "context", "tools", "files", "todos",
	"subagents", "permissions", "questions", "mcp", "lsp",
	"compaction", "errors", "vcs",
}

// opencodeCompanion implements agent.CompanionPlugin for OpenCode.
type opencodeCompanion struct{}

func NewOpenCodeCompanion() agent.CompanionPlugin { return opencodeCompanion{} }

func (opencodeCompanion) CompanionSpec() agent.CompanionSpec {
	return agent.CompanionSpec{
		AgentID:      "opencode",
		PluginName:   "opencode-spaces",
		Version:      opencodeCompanionVer,
		InstallDir:   ".config/opencode/plugins/opencode-spaces",
		Description:  "Rich OpenCode companion plugin: session/usage/context/tool/file/todo/subagent/permission/question/MCP/LSP/compaction/error/VCS telemetry over UDS HTTP.",
		Capabilities: append([]string(nil), opencodeCompanionCaps...),
		Runtime:      "node/esm",
	}
}

// opencodeEventState maps OpenCode native bus/hook event names (emitted by
// plugins/opencode-spaces) to canonical (state, kind). An explicit
// state/kind in the payload always wins; this is the fallback.
func opencodeEventState(raw string) (state, kind string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "session.created", "session.updated", "session.status",
		"message.updated", "message.part.updated",
		"tool.execute.before", "tool.execute.after",
		"todo.updated", "file.edited",
		"session.diff", "chat.params", "chat.prompt",
		"lsp.client.diagnostics", "lsp.updated", "lsp.diagnostics",
		"mcp.tools.changed", "mcp.browser.open.failed", "mcp.failed", "mcp.error",
		"vcs.branch.updated", "vcs.branch", "branch.updated",
		"session.compacted", "experimental.session.compacting",
		"permission.replied", "question.replied", "question.rejected":
		return "working", ""
	case "session.idle":
		return "idle", ""
	case "session.error":
		return "failed", ""
	case "permission.asked":
		return "blocked", "permission"
	case "question.asked":
		return "blocked", "question"
	default:
		// message.part streams, diffs and unknown future bus events are
		// live telemetry: keep the pane live without forcing a state.
		return "working", ""
	}
}

func (opencodeCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	if envelope == nil {
		return nil, false
	}
	if a, ok := envelope["agent"].(string); ok && strings.TrimSpace(a) != "" &&
		!strings.EqualFold(strings.TrimSpace(a), "opencode") {
		return nil, false
	}
	spec := opencodeCompanion{}.CompanionSpec()
	out, payload := baseCompanionEvent(spec, opencodeCompanionCaps, envelope)

	rawEvent := companionMergedStr(envelope, payload, "event", "hook_event_name", "hookEventName", "type")
	if rawEvent == "" {
		rawEvent = "working"
	}
	out["event"] = strings.ToLower(strings.TrimSpace(rawEvent))
	out["raw_event"] = oneLineCompanion(envelope["event"], companionMaxModel)
	if s, _ := out["raw_event"].(string); s == "" || s == "…" {
		out["raw_event"] = oneLineCompanion(rawEvent, companionMaxModel)
	}

	// Explicit payload state/kind (tracker snapshot) wins over the event map.
	state := normalizeCompanionState(companionMergedStr(envelope, payload, "state"))
	kind := strings.TrimSpace(companionMergedStr(envelope, payload, "kind"))
	if state == "" {
		state, kind = opencodeEventState(rawEvent)
	}
	out["state"] = state
	if kind != "" {
		out["kind"] = kind
	} else {
		out["kind"] = nil
	}

	// OpenCode extras beyond the shared schema: pending questions and the
	// full subagent snapshot feed the pane children tree.
	if v := companionMerged(envelope, payload, "pending_question", "pendingQuestion"); v != nil {
		out["pending_question"] = v
	}
	if v := companionMerged(envelope, payload, "subagents"); v != nil {
		if sl, ok := v.([]any); ok {
			out["subagents"] = boundCompanionSlice(sl, 32)
		}
	}
	// Ensure the plugin source marker survives even when a stale envelope
	// omitted it.
	out["source"] = "plugin"
	inferSubagent(out, payload)
	return out, true
}

// opencodeWrapperPath is the thin entry wrapper OpenCode auto-loads from its
// plugins directory.
func opencodeWrapperPath(home string) string {
	return filepath.Join(home, ".config", "opencode", "plugins", "opencode-spaces.mjs")
}

const opencodeWrapperBody = `// Managed by tmux-spaces (` + "`tmux-spaces plugins install opencode`" + `).
// Thin entry wrapper: re-exports the opencode-spaces companion plugin so
// OpenCode auto-loads it from its plugins directory.
export { default } from "./opencode-spaces/index.mjs";
export * from "./opencode-spaces/index.mjs";
`

func (opencodeCompanion) InstallCompanion(opts agent.CompanionInstallOptions) (agent.CompanionInstallResult, error) {
	home := companionHome(opts)
	spec := opencodeCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	prev := readInstalledVersion(dir)
	created := !dirExists(dir)
	if err := copyEmbedTree(opencodeCompanionSource, dir, false); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("install opencode-spaces: %w", err)
	}
	if err := writeFileAtomic(opencodeWrapperPath(home), []byte(opencodeWrapperBody), 0o644); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("write opencode wrapper: %w", err)
	}
	ver := readEmbedVersion(opencodeCompanionSource)
	if ver == "" {
		ver = spec.Version
	}
	return installResult(dir, prev, ver, created), nil
}

func (opencodeCompanion) UninstallCompanion(opts agent.CompanionInstallOptions) error {
	home := companionHome(opts)
	spec := opencodeCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	if dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	wrapper := opencodeWrapperPath(home)
	// Only remove the wrapper when it is ours (or forced): never delete a
	// user-owned file that happens to share the name.
	if data, err := os.ReadFile(wrapper); err == nil {
		if strings.Contains(string(data), "Managed by tmux-spaces") || opts.Force {
			if err := os.Remove(wrapper); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", wrapper, err)
			}
		}
	} else if opts.Force {
		_ = os.Remove(wrapper)
	}
	return nil
}

func (opencodeCompanion) DetectCompanionStatus(homeDir string) agent.CompanionStatus {
	home := strings.TrimSpace(homeDir)
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	spec := opencodeCompanion{}.CompanionSpec()
	dir := filepath.Join(home, filepath.FromSlash(spec.InstallDir))
	if strings.TrimSpace(homeDir) != "" && filepath.IsAbs(strings.TrimSpace(homeDir)) && !dirExists(dir) {
		// Allow homeDir to point at a custom install root in tests.
		if fileExists(filepath.Join(strings.TrimSpace(homeDir), "index.mjs")) {
			dir = strings.TrimSpace(homeDir)
		}
	}
	installed := fileExists(filepath.Join(dir, "index.mjs"))
	wrapper := opencodeWrapperPath(home)
	wrapperOK := fileExists(wrapper)
	ver := ""
	if installed {
		ver = readInstalledVersion(dir)
		if ver == "" {
			ver = spec.Version
		}
	}
	detail := fmt.Sprintf("plugin dir %s; entry wrapper %s", dir, wrapper)
	switch {
	case installed && wrapperOK:
		detail += " (loaded automatically by OpenCode)"
	case installed:
		detail += " (wrapper missing: reinstall to restore auto-load)"
	default:
		detail = fmt.Sprintf("not installed (expected at %s with wrapper %s)", dir, wrapper)
	}
	return agent.CompanionStatus{
		Installed: installed && dirExists(dir),
		Path:      dir,
		Version:   ver,
		Active:    installed && wrapperOK,
		Detail:    detail,
	}
}

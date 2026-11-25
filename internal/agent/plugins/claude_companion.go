package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	claudeCompanionSource = "claude-spaces"
	claudeCompanionVer    = "1.0.0"
)

var claudeCompanionCaps = []string{
	"lifecycle", "usage", "context", "cost", "tools", "files", "todos",
	"subagents", "background_tasks", "permissions", "questions",
	"compaction", "statusline", "errors",
}

// claudeAskTools matches hook-bridge.mjs ASK_TOOLS after lowercasing and
// stripping non-letters (only letter-only entries can ever match there).
var claudeAskTools = map[string]bool{
	"askuserquestion":  true,
	"requestuserinput": true,
	"askuser":          true,
	"askquestion":      true,
}

var claudeAuthRe = regexp.MustCompile(`(?i)sign[\s_-]?in|log[\s_-]?in|login|authenticate|auth required|api key`)

// claudeCompanion implements agent.CompanionPlugin for Claude Code.
type claudeCompanion struct{}

func NewClaudeCompanion() agent.CompanionPlugin { return claudeCompanion{} }

func (claudeCompanion) CompanionSpec() agent.CompanionSpec {
	return agent.CompanionSpec{
		AgentID:      "claude",
		PluginName:   "claude-spaces",
		Version:      claudeCompanionVer,
		InstallDir:   ".claude/plugins/claude-spaces",
		Description:  "Claude Code lifecycle + statusLine bridge: all 17 lifecycle hooks and live context/cost/model metrics over UDS HTTP.",
		Capabilities: append([]string(nil), claudeCompanionCaps...),
		Runtime:      "node/esm",
	}
}

func claudeNormTool(tool string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(tool) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// claudeEventState maps Claude Code native hook/statusLine event names
// (emitted by plugins/claude-spaces) to canonical (state, kind), mirroring
// mapState in scripts/hook-bridge.mjs. Explicit payload state/kind wins.
func claudeEventState(raw string, payload map[string]any) (state, kind string) {
	tool := companionStr(payload, "tool", "tool_name", "toolName", "name")
	errText := companionStr(payload, "error", "error_details", "errorDetails", "error_message", "errorMessage", "reason", "message")
	lower := strings.ToLower(strings.TrimSpace(raw))
	// statusLine bridge.
	if lower == "statusline" || lower == "status_line" {
		return "working", ""
	}
	switch raw {
	case "SessionStart", "SessionEnd":
		return "idle", ""
	case "UserPromptSubmit":
		return "working", ""
	case "PreToolUse":
		if claudeAskTools[claudeNormTool(tool)] {
			return "blocked", "question"
		}
		return "working", ""
	case "PostToolUse":
		return "working", ""
	case "PostToolUseFailure":
		return "failed", ""
	case "PermissionRequest":
		blob := strings.ToLower(strings.Join([]string{
			companionStr(payload, "message"), companionStr(payload, "reason"), companionStr(payload, "notification_type", "notificationType"),
		}, " "))
		if claudeAuthRe.MatchString(blob) {
			return "blocked", "auth"
		}
		return "blocked", "permission"
	case "Notification":
		ntype := strings.ToLower(companionStr(payload, "notification_type", "notificationType", "type", "notification"))
		msg := strings.ToLower(companionStr(payload, "message", "body", "text", "title"))
		switch {
		case strings.Contains(ntype, "permission") || strings.Contains(ntype, "toolpermission") ||
			strings.Contains(ntype, "approval") || strings.Contains(msg, "permission") || strings.Contains(msg, "approve"):
			return "blocked", "permission"
		case strings.Contains(ntype, "elicitation") || strings.Contains(ntype, "question"):
			return "blocked", "question"
		case strings.Contains(ntype, "idle"):
			return "done", ""
		case strings.Contains(ntype, "complete"):
			return "done", ""
		case claudeAuthRe.MatchString(ntype + " " + msg):
			return "blocked", "auth"
		default:
			return "working", ""
		}
	case "Elicitation":
		return "blocked", "question"
	case "ElicitationResult":
		return "working", ""
	case "PreCompact", "PostCompact":
		return "working", ""
	case "SubagentStart", "SubagentStop", "TaskCreated", "TaskCompleted":
		return "working", ""
	case "Stop":
		if errText != "" || strings.EqualFold(strings.TrimSpace(companionStr(payload, "stop_reason", "stopReason")), "error") {
			return "failed", ""
		}
		return "done", ""
	case "StopFailure":
		return "failed", ""
	default:
		return "working", ""
	}
}

func (claudeCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	if envelope == nil {
		return nil, false
	}
	if a, ok := envelope["agent"].(string); ok && strings.TrimSpace(a) != "" &&
		!strings.EqualFold(strings.TrimSpace(a), "claude") {
		return nil, false
	}
	spec := claudeCompanion{}.CompanionSpec()
	out, payload := baseCompanionEvent(spec, claudeCompanionCaps, envelope)

	rawEvent := companionMergedStr(envelope, payload, "event", "hook_event_name", "hookEventName", "type")
	if rawEvent == "" {
		rawEvent = "working"
	}
	out["event"] = strings.ToLower(strings.TrimSpace(rawEvent))

	state := normalizeCompanionState(companionMergedStr(envelope, payload, "state"))
	kind := strings.TrimSpace(companionMergedStr(envelope, payload, "kind"))
	if state == "" {
		state, kind = claudeEventState(rawEvent, payload)
	}
	out["state"] = state
	if kind != "" {
		out["kind"] = kind
	} else {
		out["kind"] = nil
	}

	// Subagent routing markers: agent_id/agent_type ride top-level (the
	// hook bridge sets is_subagent + parent_session_id for SubagentStart/
	// SubagentStop and any event carrying an agent_id).
	if v := companionMergedStr(envelope, payload, "agent_id", "agentId"); v != "" {
		out["agent_id"] = v
	}
	if v := companionMergedStr(envelope, payload, "agent_type", "agentType", "agentName", "agent_name", "subagent_type", "subagentType"); v != "" {
		out["agent_type"] = v
	}
	if ev, _ := out["event"].(string); ev == "subagentstart" || ev == "subagentstop" {
		out["is_subagent"] = true
	}
	inferSubagent(out, payload)
	out["source"] = "plugin"
	return out, true
}

func (claudeCompanion) InstallCompanion(opts agent.CompanionInstallOptions) (agent.CompanionInstallResult, error) {
	spec := claudeCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	prev := readInstalledVersion(dir)
	created := !dirExists(dir)
	// Preserve the executable bit on scripts/*.mjs (embed strips it).
	if err := copyEmbedTree(claudeCompanionSource, dir, true); err != nil {
		return agent.CompanionInstallResult{}, fmt.Errorf("install claude-spaces: %w", err)
	}
	// Never touch ~/.claude/settings.json here: statusLine wiring stays an
	// explicit, user-owned opt-in reported by DetectCompanionStatus.
	ver := readEmbedVersion(claudeCompanionSource)
	if ver == "" {
		ver = spec.Version
	}
	return installResult(dir, prev, ver, created), nil
}

func (claudeCompanion) UninstallCompanion(opts agent.CompanionInstallOptions) error {
	spec := claudeCompanion{}.CompanionSpec()
	dir := companionTargetDir(opts, spec)
	if dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
	}
	// settings.json is user-owned: never modified, so nothing to undo.
	return nil
}

// claudeSettingsNote inspects (read-only) the user's settings for
// statusLine wiring and plugin enablement. It never writes.
func claudeSettingsNote(home string) string {
	if strings.TrimSpace(home) == "" {
		return "settings unknown (no home)"
	}
	settings := readJSONFile(filepath.Join(home, ".claude", "settings.json"))
	// statusLine may live at top level: {"statusLine": {"command": ...}}.
	wired := false
	if sl, ok := settings["statusLine"].(map[string]any); ok {
		if cmd, ok := sl["command"].(string); ok && strings.Contains(cmd, "claude-spaces") {
			wired = true
		}
	}
	enabled := "unknown"
	if m, ok := settings["enabledPlugins"].(map[string]any); ok {
		if v, present := m["claude-spaces"]; present {
			if b, ok := v.(bool); ok && !b {
				enabled = "disabled"
			} else {
				enabled = "enabled"
			}
		}
	}
	note := "plugin enablement " + enabled
	if wired {
		note += "; statusLine wired to claude-spaces"
	} else {
		note += "; statusLine not wired (optional: point statusLine.command at scripts/statusline-bridge.mjs)"
	}
	return note
}

func (claudeCompanion) DetectCompanionStatus(homeDir string) agent.CompanionStatus {
	home := companionHome(agent.CompanionInstallOptions{HomeDir: homeDir})
	spec := claudeCompanion{}.CompanionSpec()
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
	if installed {
		detail += " (hooks auto-load from hooks/hooks.json); " + claudeSettingsNote(home)
	} else {
		detail = fmt.Sprintf("not installed (expected at %s); %s", dir, claudeSettingsNote(home))
	}
	return agent.CompanionStatus{
		Installed: installed && dirExists(dir),
		Path:      dir,
		Version:   ver,
		Active:    installed && dirExists(dir),
		Detail:    detail,
	}
}

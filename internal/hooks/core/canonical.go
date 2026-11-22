package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// Aliases maps alternate binary names to their canonical agent, mirroring
// zj_hook_core.ALIASES.
var Aliases = map[string]string{
	"agy":       "antigravity",
	"dsh":       "deepseek",
	"kimi-code": "kimi",
	"cursor":    "cursor-agent",
}

// AppNames maps canonical agents to the display app name used by the
// plugin, mirroring zj_hook_core.APP_NAMES.
var AppNames = map[string]string{
	"claude":   "claude-code",
	"kimi-cli": "kimi",
	"dsh":      "deepseek",
	"dsh-tui":  "deepseek",
	"cursor":   "cursor-agent",
}

// Agents is the known-agent set, mirroring zj_hook_core.AGENTS.
var Agents = map[string]bool{
	"claude": true, "codex": true, "opencode": true, "pi": true,
	"qwen": true, "copilot": true, "kimi": true, "kimi-cli": true,
	"mcode": true, "muse": true, "vibe": true, "gemini": true,
	"cline": true, "cursor-agent": true, "grok": true, "crush": true,
	"omp": true, "kilo": true, "antigravity": true, "deepseek": true,
	"dsh-tui": true, "fx": true, "fx-router": true, "kiro": true,
	"droid": true, "amp": true,
}

var extSuffix = regexp.MustCompile(`\.(js|mjs|cjs|py|exe|cmd|bat)$`)

// envOrOS resolves the effective env mapping: an empty map falls back to
// the process environment, mirroring Python `env or os.environ`.
func envOrOS(env map[string]string) map[string]string {
	if len(env) > 0 {
		return env
	}
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// CanonicalAgent normalizes a hook argv agent hint, mirroring
// canonical_agent: ZJ_HOOK_AGENT override, Grok runner-variable override,
// basename + lowercase + extension strip, then ALIASES.
func CanonicalAgent(agent string, env map[string]string) string {
	m := envOrOS(env)
	if override := m["ZJ_HOOK_AGENT"]; override != "" {
		agent = override
	}
	// Grok reads ~/.claude/settings.json and ~/.cursor/hooks.json. Those
	// hooks execute with Grok's runner variables, so trust the runtime
	// over the argv hint. Do not use GROK_HOME: users export it globally.
	if m["GROK_SESSION_ID"] != "" || m["GROK_HOOK_EVENT"] != "" {
		return "grok"
	}
	name := strings.TrimRight(agent, "/")
	if name == "" {
		return "unknown"
	}
	name = path.Base(name)
	name = strings.ToLower(name)
	name = extSuffix.ReplaceAllString(name, "")
	if alias, ok := Aliases[name]; ok {
		name = alias
	}
	if name == "" {
		return "unknown"
	}
	return name
}

// DisplayApp maps an agent hint to the plugin-facing app name, mirroring
// display_app (which canonicalizes against the process environment).
func DisplayApp(agent string) string {
	name := CanonicalAgent(agent, nil)
	if app, ok := AppNames[name]; ok {
		return app
	}
	return name
}

// eventAliases maps normalized event names (lowercased, non-alphanumerics
// stripped) to the canonical event, mirroring _EVENT_ALIASES.
var eventAliases = map[string]string{}

func alias(canonical string, names ...string) {
	strip := regexp.MustCompile(`[^a-z0-9]`)
	for _, name := range names {
		key := strip.ReplaceAllString(strings.ToLower(name), "")
		eventAliases[key] = canonical
	}
}

func init() {
	alias("session_start", "SessionStart", "session_start", "start")
	alias("session_end", "SessionEnd", "session_end", "end")
	alias("prompt",
		"UserPromptSubmit", "userPromptSubmitted", "user_prompt_submit",
		"beforeSubmitPrompt", "BeforeAgent", "prompt", "busy",
		"sessionBusy", "session.busy", "session.execution.started")
	alias("pre_invocation", "PreInvocation", "preInvocation", "pre_invocation")
	alias("pre_tool",
		"PreToolUse", "preToolUse", "pre_tool_use", "BeforeTool",
		"beforeShellExecution", "beforeMCPExecution", "toolCall",
		"tool.call", "tool.execute.before")
	alias("post_tool",
		"PostToolUse", "postToolUse", "post_tool_use", "AfterTool",
		"PostToolBatch", "afterAgentThought", "toolResult",
		"tool.result", "tool.execute.after")
	alias("tool_failure", "PostToolUseFailure", "postToolUseFailure", "tool_failure", "ToolError")
	alias("permission",
		"PermissionRequest", "permissionRequest", "permissionRequested",
		"PermissionDenied", "permission.asked", "permission.updated")
	alias("stop",
		"Stop", "stop", "agentStop", "AfterAgent", "Interrupt",
		"StopCancelled", "SessionIdle", "turnEnd",
		"session.execution.succeeded", "session.execution.interrupted",
		"session.idle")
	alias("response", "afterAgentResponse", "AgentResponse", "agentResponse",
		"PostInvocation", "postInvocation", "post_invocation")
	alias("stop_failure",
		"StopFailure", "stopFailure", "ErrorOccurred",
		"session.execution.failed", "session.error")
	alias("subagent_start", "SubagentStart", "subagentStart", "SubagentStart")
	alias("subagent_stop", "SubagentStop", "subagentStop", "SubagentEnd", "TeammateIdle")
	alias("task_created", "TaskCreated", "taskCreated")
	alias("task_completed", "TaskCompleted", "taskCompleted", "BackgroundTaskCompleted", "backgroundTaskCompleted")
	alias("compact", "PreCompact", "PostCompact", "PreCompress", "compact")
	alias("notification", "Notification", "notification")
	alias("elicitation", "Elicitation", "question.asked")
	alias("elicitation_result", "ElicitationResult", "question.replied", "question.rejected")
	alias("working", "working", "agent.start", "permission.replied")
	alias("done", "done", "agent.end")
	alias("error", "error", "failed")
	alias("blocked", "blocked", "needs_you", "needsYou")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// CanonicalEvent maps a raw provider event name to its canonical form,
// or "" when unknown (Python: None).
func CanonicalEvent(raw string) string {
	if raw == "" {
		return ""
	}
	return eventAliases[nonAlnum.ReplaceAllString(strings.ToLower(raw), "")]
}

// nestedContainers mirrors the payload nests _first recurses into.
var nestedContainers = []string{"data", "session", "thread", "conversation", "_meta"}

// first returns the first present, non-empty value for keys, recursing
// into the nested containers — mirroring _first. A value counts as present
// when it is neither nil nor "".
func first(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := payload[key]; ok && v != nil && v != "" {
			return v
		}
	}
	for _, container := range nestedContainers {
		if nested, ok := payload[container].(map[string]any); ok {
			if v := first(nested, keys...); v != nil {
				return v
			}
		}
	}
	return nil
}

// firstStr returns the first present non-blank string for keys, mirroring
// _first_str.
func firstStr(payload map[string]any, keys ...string) string {
	if v, ok := first(payload, keys...).(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// oneLine flattens value to a single line, collapsing whitespace and
// truncating to limit characters with an ellipsis, mirroring _one_line.
// Non-strings are JSON-encoded first (compact, raw UTF-8). Character counts
// are in runes, matching Python len()/slicing on str.
func oneLine(value any, limit int, keepTail bool) string {
	var s string
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		s = v
	default:
		b, err := marshalJSON(v)
		if err != nil {
			s = fmt.Sprintf("%v", v)
		} else {
			s = string(b)
		}
	}
	s = strings.Join(strings.Fields(stripControls(s)), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if keepTail {
		return "…" + string(r[len(r)-(limit-1):])
	}
	n := limit - 1
	if n < 1 {
		n = 1
	}
	return string(r[:n]) + "…"
}

// fieldStr mirrors _field: a one-line value capped at limit, or nil when
// blank.
func fieldStr(value any, limit int, keepTail bool) any {
	if text := oneLine(value, limit, keepTail); text != "" {
		return text
	}
	return nil
}

// valueTruthy mirrors Python truthiness for the scalar/container values
// that flow through _tool_text list joins.
func valueTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		return t.String() != "" && t.String() != "0"
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// toolText extracts a human-readable fragment from a tool input payload,
// mirroring _tool_text: preferred keys first, then any string value.
func toolText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if valueTruthy(item) {
				parts = append(parts, toolText(item))
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		for _, key := range []string{
			"command", "CommandLine", "Command", "toolSummary",
			"file_path", "path", "Path", "TargetFile", "SearchPath",
			"pattern", "url", "Url", "query", "Query",
			"Role", "description", "Description", "prompt", "Prompt",
			"content", "new_string", "old_string",
		} {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		for _, candidate := range v {
			if s, ok := candidate.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// BuildPreview renders "Tool · input-fragment", mirroring build_preview.
func BuildPreview(tool string, toolInput any) string {
	tool = strings.TrimSpace(tool)
	preview := oneLine(toolText(toolInput), MaxText, false)
	if tool != "" && preview != "" {
		if len([]rune(preview)) > 120 {
			preview = string([]rune(preview)[:119]) + "…"
		}
		return tool + " · " + preview
	}
	if tool != "" {
		return tool
	}
	return preview
}

// looksAuth mirrors _looks_auth: login/auth phrasing in message-ish fields.
func looksAuth(payload map[string]any) bool {
	var sb strings.Builder
	for _, key := range []string{"message", "reason", "notification_type", "notificationType", "text"} {
		if s, ok := payload[key].(string); ok {
			sb.WriteString(s)
			sb.WriteByte(' ')
		}
	}
	text := strings.ToLower(sb.String())
	for _, needle := range []string{"sign in", "sign-in", "log in", "login", "authenticate", "auth required", "api key"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// askTools mirrors _ASK_TOOLS.
var askTools = map[string]bool{
	"askuserquestion": true, "ask_user_question": true,
	"request_user_input": true, "requestuserinput": true,
	"ask_user": true, "askuser": true,
	"ask_question": true, "askquestion": true,
}

// toolIsAsk mirrors _tool_is_ask.
func toolIsAsk(tool string) bool {
	return askTools[strings.ToLower(strings.TrimSpace(tool))]
}

// conversationTitle extracts an explicit conversation/session title or summary
// from a hook payload when present, or nil when absent.
func conversationTitle(payload map[string]any) any {
	v := first(payload,
		"conversation_title", "conversationTitle",
		"session_title", "sessionTitle",
		"session_summary", "sessionSummary",
		"custom_title", "customTitle",
		"ai_title", "aiTitle",
		"thread_title", "threadTitle",
		"chat_title", "chatTitle",
		"summary")
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return fieldStr(s, MaxText, false)
	}
	for _, container := range []string{"session", "thread", "conversation", "info"} {
		if nested, ok := payload[container].(map[string]any); ok {
			for _, key := range []string{"title", "name", "summary", "session_summary", "custom_title", "ai_title"} {
				if s, ok := nested[key].(string); ok && strings.TrimSpace(s) != "" {
					return fieldStr(s, MaxText, false)
				}
			}
		}
	}
	return nil
}

// sessionID extracts the provider session/thread/conversation id, mirroring
// _session_id (capped at MaxSession characters, nil when absent).
func sessionID(payload map[string]any) any {
	v := first(payload,
		"session_id", "sessionId", "sessionID",
		"thread_id", "threadId",
		"conversation_id", "conversationId", "id")
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return truncateRunes(strings.TrimSpace(s), MaxSession)
	}
	return nil
}

// isSubagentConversation reports whether a hook payload came from a child
// subagent conversation (e.g. Antigravity's parentConversationId /
// rootConversationId fields) rather than the pane's root conversation.
func isSubagentConversation(payload map[string]any) bool {
	if firstStr(payload, "parentConversationId", "parent_conversation_id") != "" {
		return true
	}
	rootID := firstStr(payload, "rootConversationId", "root_conversation_id")
	convID := firstStr(payload, "conversationId", "conversation_id")
	return rootID != "" && convID != "" && rootID != convID
}

// childInfo builds the subagent child record, mirroring _child_info, or nil
// when the payload carries no subagent identity.
func childInfo(agent string, payload map[string]any, event, tool, text string) map[string]any {
	subConv := isSubagentConversation(payload)
	agentID := firstStr(payload, "agent_id", "agentId", "parent_agent_id", "parentAgentId")
	agentType := firstStr(payload, "agentName", "agent_name", "agent_type", "agentType", "subagent_type", "subagentType")
	var agyPromptHint string
	if agent == "antigravity" && !subConv {
		if meta := inspectAntigravitySubagent(payload); meta.IsSubagent {
			subConv = true
			if agentType == "" && meta.AgentName != "" {
				agentType = meta.AgentName
			}
			agyPromptHint = meta.PromptHint
		}
	}
	if !subConv && event != "subagent_start" && event != "subagent_stop" && agentID == "" && agentType == "" {
		return nil
	}
	var childID string
	switch {
	case subConv:
		if sid, ok := sessionID(payload).(string); ok && sid != "" {
			childID = sid
		} else if agentID != "" {
			childID = agentID
		} else if agentType != "" {
			childID = agentType
		} else {
			childID = "child"
		}
	case agentID != "":
		childID = agentID
	case agentType != "":
		childID = agentType
	default:
		if sid, ok := sessionID(payload).(string); ok {
			childID = sid
		} else {
			childID = "child"
		}
	}
	state := "working"
	outcome := ""
	var endedMs int64
	ts := nowMs()
	if subConv {
		// Subagent conversation lifecycle (e.g. Antigravity): keep working on
		// SessionStart/PreInvocation/PostToolUse/PostInvocation, and mark
		// terminal on Stop/SessionEnd unless nested background work remains.
		if event == "stop" || event == "session_end" || event == "stop_failure" {
			keepWorking := false
			if event == "stop" {
				if known, fullyIdle := antigravityStopIdleSignal(agent, payload); known && !fullyIdle {
					keepWorking = true
				}
			}
			if !keepWorking {
				termReason := strings.ToUpper(strings.TrimSpace(firstStr(payload, "terminationReason", "termination_reason")))
				errMsg := firstStr(payload, "error", "error_message", "errorMessage")
				switch {
				case event == "stop_failure" || termReason == "ERROR" || errMsg != "":
					state = "failed"
					if errMsg != "" {
						outcome = oneLine(errMsg, 80, false)
					} else {
						outcome = "failed"
					}
				case termReason == "USER_CANCELED":
					state = "stopped"
					outcome = "stopped"
				default:
					state = "done"
					outcome = "done"
				}
				endedMs = ts
			}
		}
		// Leave text empty on tool/stop ticks so MergeEvent preserves the concise
		// Role captured from invoke_subagent; seed from lastUserInput when starting.
		childText := ""
		if event == "session_start" || event == "pre_invocation" || event == "prompt" {
			childText = oneLine(firstStr(payload, "lastUserInput", "last_user_input", "prompt", "description"), MaxText, false)
			if childText == "" && agyPromptHint != "" {
				childText = oneLine(agyPromptHint, MaxText, false)
			}
		}
		return map[string]any{
			"id":            truncateRunes(childID, MaxSession),
			"type":          truncateRunes(agentType, MaxModel),
			"kind":          BgKindSubagent,
			"category":      BgCategoryFinite,
			"state":         state,
			"outcome":       outcome,
			"text":          childText,
			"preserve_text": true,
			"since_ms":      ts,
			"ts_ms":         ts,
			"ended_ms":      endedMs,
		}
	}
	if event == "subagent_stop" {
		rawStatus := firstStr(payload, "status", "state")
		exitCode := parseOptionalInt(payload, "exit_code", "exitCode")
		errMsg := firstStr(payload, "error", "error_message", "errorMessage")
		if errMsg != "" && rawStatus == "" {
			rawStatus = "failed"
		}
		if rawStatus != "" || exitCode != nil {
			state, outcome = NormalizeBackgroundTaskState(rawStatus, exitCode)
		} else {
			state = "done"
			outcome = "done"
		}
		if state == "working" {
			state = "done"
			outcome = "done"
		}
		if errMsg != "" {
			outcome = oneLine(errMsg, 80, false)
		}
		endedMs = ts
	}
	return map[string]any{
		"id":       truncateRunes(childID, MaxSession),
		"type":     truncateRunes(agentType, MaxModel),
		"kind":     BgKindSubagent,
		"category": BgCategoryFinite,
		"state":    state,
		"outcome":  outcome,
		"text":     text,
		"since_ms": ts,
		"ts_ms":    ts,
		"ended_ms": endedMs,
	}
}

// childrenBackground counts background tasks/crons, mirroring
// _children_background.
func childrenBackground(payload map[string]any) int {
	total := 0
	for _, key := range []string{"background_tasks", "backgroundTasks", "session_crons", "sessionCrons"} {
		if list, ok := payload[key].([]any); ok {
			total += len(list)
		}
	}
	return total
}

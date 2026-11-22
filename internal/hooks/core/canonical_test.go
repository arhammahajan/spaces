package core

import (
	"strings"
	"testing"
)

func TestCanonicalAgent(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		env   map[string]string
		want  string
	}{
		{"plain", "claude", map[string]string{"x": "1"}, "claude"},
		{"case", "Claude", map[string]string{"x": "1"}, "claude"},
		{"basename", "/usr/bin/codex", map[string]string{"x": "1"}, "codex"},
		{"trailing slash", "opencode/", map[string]string{"x": "1"}, "opencode"},
		{"js ext", "claude.js", map[string]string{"x": "1"}, "claude"},
		{"mjs ext", "agent.mjs", map[string]string{"x": "1"}, "agent"},
		{"py ext", "helper.py", map[string]string{"x": "1"}, "helper"},
		{"exe ext", "tool.EXE", map[string]string{"x": "1"}, "tool"},
		{"alias agy", "agy", map[string]string{"x": "1"}, "antigravity"},
		{"alias dsh", "dsh", map[string]string{"x": "1"}, "deepseek"},
		{"alias kimi-code", "kimi-code", map[string]string{"x": "1"}, "kimi"},
		{"alias cursor", "cursor", map[string]string{"x": "1"}, "cursor-agent"},
		{"empty", "", map[string]string{"x": "1"}, "unknown"},
		{"slash only", "/", map[string]string{"x": "1"}, "unknown"},
		{"hook agent override", "claude", map[string]string{"ZJ_HOOK_AGENT": "codex"}, "codex"},
		{"grok session wins", "claude", map[string]string{"GROK_SESSION_ID": "grok-1"}, "grok"},
		{"grok event wins", "claude", map[string]string{"GROK_HOOK_EVENT": "x"}, "grok"},
		{"grok beats override", "codex", map[string]string{"ZJ_HOOK_AGENT": "pi", "GROK_SESSION_ID": "g"}, "grok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalAgent(tc.agent, tc.env); got != tc.want {
				t.Errorf("CanonicalAgent(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

func TestDisplayApp(t *testing.T) {
	cases := []struct{ agent, want string }{
		{"claude", "claude-code"},
		{"kimi-cli", "kimi"},
		{"dsh", "deepseek"},
		{"dsh-tui", "deepseek"},
		{"cursor", "cursor-agent"},
		{"codex", "codex"},
		{"opencode", "opencode"},
		{"grok", "grok"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			if got := DisplayApp(tc.agent); got != tc.want {
				t.Errorf("DisplayApp(%q) = %q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}

func TestCanonicalEvent(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"SessionStart", "session_start"},
		{"SessionEnd", "session_end"},
		{"start", "session_start"},
		{"end", "session_end"},
		{"UserPromptSubmit", "prompt"},
		{"userPromptSubmitted", "prompt"},
		{"beforeSubmitPrompt", "prompt"},
		{"BeforeAgent", "prompt"},
		{"session.execution.started", "prompt"},
		{"PreToolUse", "pre_tool"},
		{"tool.execute.before", "pre_tool"},
		{"beforeMCPExecution", "pre_tool"},
		{"PostToolUse", "post_tool"},
		{"PostToolUseFailure", "tool_failure"},
		{"ToolError", "tool_failure"},
		{"PermissionRequest", "permission"},
		{"PermissionDenied", "permission"},
		{"Stop", "stop"},
		{"AfterAgent", "stop"},
		{"session.execution.succeeded", "stop"},
		{"session.execution.failed", "stop_failure"},
		{"afterAgentResponse", "response"},
		{"SubagentStart", "subagent_start"},
		{"TeammateIdle", "subagent_stop"},
		{"PreCompact", "compact"},
		{"Notification", "notification"},
		{"Elicitation", "elicitation"},
		{"ElicitationResult", "elicitation_result"},
		{"working", "working"},
		{"agent.start", "working"},
		{"done", "done"},
		{"agent.end", "done"},
		{"error", "error"},
		{"failed", "error"},
		{"blocked", "blocked"},
		{"needs_you", "blocked"},
		{"session_idle", "stop"},
		{"SomeUnknownEvent", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := CanonicalEvent(tc.raw); got != tc.want {
				t.Errorf("CanonicalEvent(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFirstNestedLookup(t *testing.T) {
	payload := map[string]any{
		"tool_name": "",
		"data":      map[string]any{"tool_name": "Bash"},
		"session":   map[string]any{"session_id": "s1"},
	}
	if got := firstStr(payload, "tool_name"); got != "Bash" {
		t.Errorf("nested tool_name = %q", got)
	}
	if got := firstStr(payload, "session_id"); got != "s1" {
		t.Errorf("nested session_id = %q", got)
	}
	if got := first(payload, "missing"); got != nil {
		t.Errorf("missing = %v", got)
	}
	// Blank strings are skipped like Python's `value != ""`.
	if got := firstStr(map[string]any{"a": "  "}, "a"); got != "" {
		t.Errorf("blank = %q", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a  b\tc\nd", 240, false); got != "a b c d" {
		t.Errorf("squash = %q", got)
	}
	if got := oneLine("a\x00b\x7fc", 240, false); got != "a b c" {
		t.Errorf("controls = %q", got)
	}
	long := strings.Repeat("x", 300)
	got := oneLine(long, 240, false)
	if len([]rune(got)) != 240 || !strings.HasSuffix(got, "…") {
		t.Errorf("head truncate len=%d suffix=%q", len([]rune(got)), got)
	}
	got = oneLine(long, 240, true)
	if len([]rune(got)) != 240 || !strings.HasPrefix(got, "…") {
		t.Errorf("tail truncate len=%d prefix=%q", len([]rune(got)), got)
	}
	if got := oneLine(map[string]any{"a": 1}, 240, false); got != `{"a":1}` {
		t.Errorf("non-string = %q", got)
	}
	if got := oneLine(nil, 240, false); got != "" {
		t.Errorf("nil = %q", got)
	}
}

func TestBuildPreview(t *testing.T) {
	if got := BuildPreview("Bash", map[string]any{"command": "cargo test"}); got != "Bash · cargo test" {
		t.Errorf("preview = %q", got)
	}
	if got := BuildPreview("Bash", nil); got != "Bash" {
		t.Errorf("tool only = %q", got)
	}
	if got := BuildPreview("", map[string]any{"command": "ls"}); got != "ls" {
		t.Errorf("input only = %q", got)
	}
	long := strings.Repeat("y", 200)
	got := BuildPreview("Bash", map[string]any{"command": long})
	if len([]rune(got)) > len("Bash · ")+120 {
		t.Errorf("preview cap exceeded: %q", got)
	}
}

func TestLooksAuthAndAsk(t *testing.T) {
	authPayloads := []map[string]any{
		{"message": "Please sign in to continue"},
		{"notification_type": "NeedLogin"},
		{"reason": "API key expired"},
		{"text": "authenticate now"},
	}
	for i, p := range authPayloads {
		if !looksAuth(p) {
			t.Errorf("case %d not auth", i)
		}
	}
	if looksAuth(map[string]any{"message": "permission denied"}) {
		t.Errorf("permission is not auth")
	}
	for _, tool := range []string{"AskUserQuestion", "ask_user_question", "REQUEST_USER_INPUT", "askuser"} {
		if !toolIsAsk(tool) {
			t.Errorf("%q should be ask", tool)
		}
	}
	if toolIsAsk("Bash") {
		t.Errorf("Bash should not be ask")
	}
}

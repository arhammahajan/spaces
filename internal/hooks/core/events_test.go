package core

import (
	"strings"
	"testing"
)

func TestNormalizeClaudeToolAndPermission(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "s1",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "cargo test"},
		"model":           "m1",
	}, map[string]string{})
	if event == nil {
		t.Fatal("expected event, got nil")
	}
	if event["state"] != "working" {
		t.Errorf("state = %v", event["state"])
	}
	if event["text"] != "Bash · cargo test" {
		t.Errorf("text = %v", event["text"])
	}
	if event["session_id"] != "s1" {
		t.Errorf("session_id = %v", event["session_id"])
	}
	if event["model"] != "m1" {
		t.Errorf("model = %v", event["model"])
	}

	permission := NormalizeEvent("claude", "PermissionRequest", map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": "src/x.rs"},
	}, map[string]string{})
	if permission == nil {
		t.Fatal("expected permission event, got nil")
	}
	if permission["state"] != "blocked" {
		t.Errorf("state = %v", permission["state"])
	}
	if permission["kind"] != "permission" {
		t.Errorf("kind = %v", permission["kind"])
	}
	if text, _ := permission["text"].(string); !strings.Contains(text, "approve Write") {
		t.Errorf("text = %v", permission["text"])
	}
}

func TestNormalizeAskUserQuestion(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{"question": "which?"},
	}, map[string]string{})
	if event == nil {
		t.Fatal("expected event, got nil")
	}
	if event["state"] != "blocked" || event["kind"] != "question" {
		t.Errorf("state/kind = %v/%v", event["state"], event["kind"])
	}
}

func TestNormalizeProviderMatrix(t *testing.T) {
	setupRuntime(t)
	cases := []struct {
		agent   string
		event   string
		payload map[string]any
		want    string
	}{
		{"codex", "PreToolUse", map[string]any{"session_id": "c", "tool_name": "shell", "tool_input": map[string]any{"command": "ls"}}, "working"},
		{"codex", "PermissionRequest", map[string]any{"session_id": "c"}, "blocked"},
		{"gemini", "BeforeAgent", map[string]any{"session_id": "g", "prompt": "hi"}, "working"},
		{"gemini", "AfterAgent", map[string]any{"session_id": "g", "prompt_response": "done"}, "done"},
		{"gemini", "Notification", map[string]any{"notification_type": "ToolPermission"}, "blocked"},
		{"copilot", "sessionStart", map[string]any{"sessionId": "p"}, "idle"},
		{"copilot", "userPromptSubmitted", map[string]any{"sessionId": "p", "prompt": "go"}, "working"},
		{"cursor", "beforeSubmitPrompt", map[string]any{"conversation_id": "c", "prompt": "go"}, "working"},
		{"cursor", "afterAgentResponse", map[string]any{"conversation_id": "c", "text": "done"}, "working"},
		{"cursor", "stop", map[string]any{"conversation_id": "c", "status": "completed"}, "done"},
		{"kimi", "SessionStart", map[string]any{"session_id": "k"}, "idle"},
		{"opencode", "tool.execute.before", map[string]any{"sessionID": "o", "tool": "bash", "input": map[string]any{"command": "ls"}}, "working"},
		{"opencode", "session.execution.failed", map[string]any{"sessionID": "o", "error": "boom"}, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.event, func(t *testing.T) {
			event := NormalizeEvent(tc.agent, tc.event, tc.payload, map[string]string{})
			if event == nil {
				t.Fatalf("expected event, got nil")
			}
			if event["state"] != tc.want {
				t.Errorf("state = %v, want %v", event["state"], tc.want)
			}
		})
	}
}

func TestNormalizeCursorResponseKeepsReply(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("cursor", "afterAgentResponse", map[string]any{
		"conversation_id": "c",
		"text":            "finished the work",
	}, map[string]string{})
	if event == nil {
		t.Fatal("expected event, got nil")
	}
	if event["state"] != "working" {
		t.Errorf("state = %v", event["state"])
	}
	if event["reply"] != "finished the work" {
		t.Errorf("reply = %v", event["reply"])
	}
}

func TestNormalizeGrokRuntimeOverridesHint(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}, map[string]string{"GROK_SESSION_ID": "grok-1"})
	if event == nil {
		t.Fatal("expected event, got nil")
	}
	if event["agent"] != "grok" || event["app"] != "grok" {
		t.Errorf("agent/app = %v/%v", event["agent"], event["app"])
	}
}

func TestNormalizeUnknownIgnored(t *testing.T) {
	setupRuntime(t)
	if event := NormalizeEvent("claude", "SomeUnknownEvent", map[string]any{}, map[string]string{}); event != nil {
		t.Errorf("expected nil, got %v", event)
	}
	// A known event that maps to no state (plain notification) is also nil.
	if event := NormalizeEvent("claude", "Notification", map[string]any{"message": "hello"}, map[string]string{}); event != nil {
		t.Errorf("expected nil, got %v", event)
	}
}

func TestNormalizeEnvelopeShape(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}, map[string]string{})
	if event == nil {
		t.Fatal("expected event")
	}
	for _, key := range []string{
		"v", "agent", "app", "event", "raw_event", "state", "kind",
		"text", "title", "prompt", "tool", "tool_input", "tool_output", "reply",
		"model", "session_id", "cwd", "background", "child", "ts_ms", "seq",
	} {
		if _, ok := event[key]; !ok {
			t.Errorf("envelope missing key %q", key)
		}
	}
	if event["v"] != Version {
		t.Errorf("v = %v", event["v"])
	}
	if event["ts_ms"] != Now() {
		t.Errorf("ts_ms = %v, want frozen %d", event["ts_ms"], Now())
	}
}

func TestNormalizeAndMergeConversationTitle(t *testing.T) {
	setupRuntime(t)
	// Explicit conversation_title in payload wins.
	ev1 := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id":         "s-1",
		"prompt":             "First prompt text",
		"conversation_title": "Refined Session Title",
	}, map[string]string{})
	if ev1 == nil || ev1["title"] != "Refined Session Title" {
		t.Fatalf("ev1 title = %v, want Refined Session Title", ev1["title"])
	}
	// Fallback to prompt on prompt event when no explicit title field exists.
	ev2 := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "s-1",
		"prompt":     "Implement hook title extraction",
	}, map[string]string{})
	if ev2 == nil || ev2["title"] != "Implement hook title extraction" {
		t.Fatalf("ev2 title = %v, want prompt fallback", ev2["title"])
	}
	record := MergeEvent(nil, ev2, 1000)
	if record["title"] != "Implement hook title extraction" {
		t.Errorf("merged title = %v", record["title"])
	}
	// Subsequent tool event without title keeps the existing title.
	toolEv := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"session_id": "s-1",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "go test ./..."},
	}, map[string]string{})
	record = MergeEvent(record, toolEv, 2000)
	if record["title"] != "Implement hook title extraction" {
		t.Errorf("title after tool event = %v", record["title"])
	}
	// session_start with a new session_id resets the old session's title/prompt.
	newStart := NormalizeEvent("claude", "SessionStart", map[string]any{
		"session_id": "s-2",
	}, map[string]string{})
	record = MergeEvent(record, newStart, 3000)
	if record["title"] != nil || record["prompt"] != nil {
		t.Errorf("expected title and prompt reset on new session_start, got title=%v prompt=%v", record["title"], record["prompt"])
	}
}

func mergeFixture(state, event string, now int64, extra map[string]any) map[string]any {
	e := map[string]any{
		"v": 1, "agent": "claude", "app": "claude-code",
		"event": event, "state": state, "kind": nil,
		"text": event, "ts_ms": now, "seq": now,
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestMergeWorkingClearsTurnScopedBlock(t *testing.T) {
	setupRuntime(t)
	record := MergeEvent(nil, mergeFixture("blocked", "permission", 1000, map[string]any{"kind": "permission"}), 1000)
	if record["state"] != "blocked" {
		t.Fatalf("state = %v", record["state"])
	}
	record = MergeEvent(record, mergeFixture("working", "pre_tool", 2000, nil), 2000)
	if record["state"] != "working" {
		t.Errorf("state = %v", record["state"])
	}
	if record["kind"] != nil {
		t.Errorf("kind = %v", record["kind"])
	}
}

func TestMergeAuthBlockSurvivesStop(t *testing.T) {
	setupRuntime(t)
	record := MergeEvent(nil, mergeFixture("blocked", "permission", 1000, map[string]any{"kind": "auth"}), 1000)
	record = MergeEvent(record, mergeFixture("done", "stop", 2000, nil), 2000)
	if record["state"] != "blocked" || record["kind"] != "auth" {
		t.Errorf("state/kind = %v/%v", record["state"], record["kind"])
	}
	record = MergeEvent(record, mergeFixture("working", "prompt", 3000, nil), 3000)
	if record["state"] != "working" {
		t.Errorf("state = %v", record["state"])
	}
}

func TestMergeChildrenDoNotOverwriteParent(t *testing.T) {
	setupRuntime(t)
	record := MergeEvent(nil, mergeFixture("working", "pre_tool", 1000, map[string]any{"text": "Bash"}), 1000)
	child := mergeFixture("working", "pre_tool", 2000, map[string]any{"text": "child"})
	child["child"] = map[string]any{"id": "a1", "type": "Explore", "state": "working", "text": "child", "ts_ms": int64(2000)}
	record = MergeEvent(record, child, 2000)
	if record["state"] != "working" || record["text"] != "Bash" {
		t.Errorf("parent overwritten: %v/%v", record["state"], record["text"])
	}
	children, _ := record["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v", record["children"])
	}
	stop := mergeFixture("working", "subagent_stop", 3000, map[string]any{"text": "child done"})
	stop["child"] = map[string]any{"id": "a1", "type": "Explore", "state": "done", "text": "child done", "ts_ms": int64(3000)}
	record = MergeEvent(record, stop, 3000)
	children, _ = record["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v", record["children"])
	}
	if m, _ := children[0].(map[string]any); m["state"] != "done" {
		t.Errorf("child state = %v", m["state"])
	}
}

func TestMergeDuplicatesDeduped(t *testing.T) {
	setupRuntime(t)
	first := mergeFixture("working", "pre_tool", 1000, nil)
	record := MergeEvent(nil, first, 1000)
	second := mergeFixture("working", "pre_tool", 1000, nil)
	second["seq"] = int64(1001)
	merged := MergeEvent(record, second, 1200)
	children, _ := merged["children"].([]any)
	if len(children) != 0 {
		t.Errorf("children = %v", children)
	}
	last, _ := merged["last_event"].(map[string]any)
	if toInt64(last["ts_ms"]) != 1000 {
		t.Errorf("last_event ts = %v", last["ts_ms"])
	}
}

func TestPruneStoreByState(t *testing.T) {
	setupRuntime(t)
	now := Now()
	cases := []struct {
		name     string
		state    string
		kind     string
		tsAgo    int64
		sinceAgo int64
		stale    bool
	}{
		{"working fresh", "working", "", 60_000, 60_000, false},
		{"working stale", "working", "", WorkingStaleMS + 1, WorkingStaleMS + 1, true},
		{"idle stale", "idle", "", WorkingStaleMS + 1, 0, true},
		{"done fresh", "done", "", 1_000, 60_000, false},
		{"done stale", "done", "", 1_000, DoneTTLMS + 1, true},
		{"failed stale", "failed", "", 1_000, FailedTTLMS + 1, true},
		{"blocked stale", "blocked", "permission", 1_000, BlockTTLMS + 1, true},
		{"blocked fresh", "blocked", "permission", 1_000, 60_000, false},
		{"auth fresh at block ttl", "blocked", "auth", 1_000, BlockTTLMS + 1, false},
		{"auth stale", "blocked", "auth", 1_000, AuthTTLMS + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var kind any
			if tc.kind != "" {
				kind = tc.kind
			}
			record := map[string]any{
				"v": 1, "agent": "claude", "app": "claude-code",
				"state": tc.state, "kind": kind, "event": "x",
				"text": "x", "ts_ms": now - tc.tsAgo,
				"state_since_ms": now - tc.sinceAgo,
				"children":       []any{},
			}
			store := map[string]any{"v": 1, "updated_ms": now,
				"panes": map[string]any{"s": map[string]any{"1": record}}}
			pruned := PruneStore(store, now)
			panes := mustGet(t, pruned, "panes")
			_, kept := panes["s"]
			if kept == tc.stale {
				t.Errorf("stale=%v but kept=%v", tc.stale, kept)
			}
		})
	}
}

func TestCodexSubagentsAreScopedToActualLaunchesAndActiveTurns(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	merge := func(record map[string]any, raw, sid string, extra map[string]any, offset int64) map[string]any {
		t.Helper()
		payload := map[string]any{"session_id": sid}
		for key, value := range extra {
			payload[key] = value
		}
		return MergeEvent(record, NormalizeEvent("codex", raw, payload, nil), t0+offset)
	}
	record := merge(nil, "UserPromptSubmit", "parent", nil, 0)
	record = merge(record, "PreToolUse", "orphan", map[string]any{"agent_id": "orphan", "agent_type": "default"}, 2000)
	if children := record["children"].([]any); len(children) != 0 || record["session_id"] != "parent" {
		t.Fatalf("an orphan tool event changed parent attribution: children=%#v session=%v", children, record["session_id"])
	}
	worker := map[string]any{"agent_id": "child", "agent_type": "default"}
	record = merge(record, "SubagentStart", "parent", worker, 4000)
	if children := record["children"].([]any); len(children) != 1 {
		t.Fatalf("actual launch was not tracked: %#v", children)
	}
	record = merge(record, "SubagentStop", "parent", worker, 6000)
	record = merge(record, "PostToolUse", "child", worker, 8000)
	record = merge(record, "Stop", "parent", nil, 10000)
	if record["state"] != "done" {
		t.Fatalf("late child tool event kept the parent working: %v", record["state"])
	}
	record = merge(record, "UserPromptSubmit", "parent", nil, 12000)
	if children := record["children"].([]any); len(children) != 0 {
		t.Fatalf("previous turn's completed child appeared in the next turn: %#v", children)
	}
}

func TestAppendOutcomeMergesOSCAndHookWithoutDuplicates(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	store := map[string]any{"v": Version, "panes": map[string]any{}, "outcomes": []any{}}

	// 1. OSC fires first with empty session_id and empty text.
	appendOutcome(store, map[string]any{
		"session": "dev", "pane": "20", "agent": "codex", "app": "codex",
		"outcome": "done", "text": "", "title": "hello who are you",
		"cwd": "/repo", "session_id": nil, "ts_ms": t0,
	}, OutcomeDedupeMS)

	// 2. Hook Stop fires 800ms later on the same pane with rich session_id + reply + model.
	appendOutcome(store, map[string]any{
		"session": "dev", "pane": "20", "agent": "codex", "app": "codex",
		"outcome": "done", "text": "I'm Codex.", "title": "hello who are you",
		"cwd": "/repo", "model": "gpt-6-luna", "session_id": "sid-20", "ts_ms": t0 + 800,
	}, OutcomeDedupeMS)

	outcomes := store["outcomes"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 merged outcome, got %d: %#v", len(outcomes), outcomes)
	}
	got := outcomes[0].(map[string]any)
	if got["session_id"] != "sid-20" || got["text"] != "I'm Codex." || got["model"] != "gpt-6-luna" {
		t.Fatalf("expected richer hook metadata to win, got %#v", got)
	}

	// 3. Repeated unattributed OSC outcomes on the same (session, pane, agent)
	// > OutcomeDedupeMS apart collapse to a single entry instead of piling up.
	appendOutcome(store, map[string]any{
		"session": "spaces", "pane": "11", "agent": "codex", "app": "codex",
		"outcome": "done", "text": "", "title": "Diagnose launch failure",
		"cwd": "/repo/spaces", "session_id": nil, "ts_ms": t0 + 1000,
	}, OutcomeDedupeMS)
	appendOutcome(store, map[string]any{
		"session": "spaces", "pane": "11", "agent": "codex", "app": "codex",
		"outcome": "done", "text": "", "title": "Diagnose launch failure",
		"cwd": "/repo/spaces", "session_id": nil, "ts_ms": t0 + 400_000,
	}, OutcomeDedupeMS)

	outcomes = store["outcomes"].([]any)
	if len(outcomes) != 2 {
		t.Fatalf("expected 2 total outcomes (no unattributed pane duplicates), got %d: %#v", len(outcomes), outcomes)
	}

	// 4. PruneStore removes pre-existing unattributed duplicates when an
	// attributed outcome exists for the same (agent, cwd, title).
	dirty := map[string]any{
		"v": Version, "panes": map[string]any{},
		"outcomes": []any{
			map[string]any{
				"session": "spaces", "pane": "11", "agent": "codex", "app": "codex",
				"outcome": "done", "title": "Diagnose launch failure", "cwd": "/repo/spaces",
				"session_id": nil, "ts_ms": t0 + 2000,
			},
			map[string]any{
				"session": "dev", "pane": "5", "agent": "codex", "app": "codex",
				"outcome": "done", "title": "Diagnose launch failure", "cwd": "/repo/spaces",
				"session_id": "sid-diag", "ts_ms": t0 + 1000,
			},
		},
	}
	pruned := PruneStore(dirty, t0+5000)
	pOutcomes := pruned["outcomes"].([]any)
	if len(pOutcomes) != 1 || pOutcomes[0].(map[string]any)["session_id"] != "sid-diag" {
		t.Fatalf("expected PruneStore to drop unattributed duplicate and keep attributed entry, got %#v", pOutcomes)
	}
}

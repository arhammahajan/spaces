package core

import (
	"testing"
)

func TestSummaryAndDetailMergeSpoolEvents(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "cargo test"},
		"session_id": "s",
	}, map[string]string{})
	if event == nil {
		t.Fatal("nil event")
	}
	entry := map[string]any{"session": "dots", "pane": 7}
	for k, v := range event {
		entry[k] = v
	}
	if err := AppendSpool(entry); err != nil {
		t.Fatal(err)
	}
	summary := SpaceHooksSummary()
	if summary["ok"] != true || summary["version"] != Version {
		t.Errorf("summary header = %v", summary)
	}
	if summary["spool_pending"] != 1 {
		t.Errorf("spool_pending = %v", summary["spool_pending"])
	}
	pane := mustGet(t, summary, "panes", "dots", "7")
	if pane["state"] != "working" {
		t.Errorf("state = %v", pane["state"])
	}
	if pane["has_detail"] != true {
		t.Errorf("has_detail = %v", pane["has_detail"])
	}
	detail := SpaceHooksDetail("dots", 7)
	record, ok := detail["record"].(map[string]any)
	if !ok || record == nil {
		t.Fatalf("detail record = %v", detail["record"])
	}
	tool, ok := record["tool"].(map[string]any)
	if !ok {
		t.Fatalf("tool = %v", record["tool"])
	}
	if tool["name"] != "Bash" || tool["input"] != "cargo test" {
		t.Errorf("tool = %v", tool)
	}
}

func TestSummaryEmpty(t *testing.T) {
	setupRuntime(t)
	summary := SpaceHooksSummary()
	if summary["spool_pending"] != 0 {
		t.Errorf("spool_pending = %v", summary["spool_pending"])
	}
	panes, _ := summary["panes"].(map[string]any)
	if len(panes) != 0 {
		t.Errorf("panes = %v", panes)
	}
}

func TestDetailMissing(t *testing.T) {
	setupRuntime(t)
	detail := SpaceHooksDetail("nope", 1)
	if detail["ok"] != true || detail["version"] != Version {
		t.Errorf("header = %v", detail)
	}
	if detail["record"] != nil {
		t.Errorf("record = %v", detail["record"])
	}
}

func TestDetailPopsEmptyTool(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "s", "last_assistant_message": "done",
	}, map[string]string{})
	if event == nil {
		t.Fatal("nil event")
	}
	if err := MergeIntoStore("s1", 1, event); err != nil {
		t.Fatal(err)
	}
	detail := SpaceHooksDetail("s1", 1)
	record, _ := detail["record"].(map[string]any)
	if _, ok := record["tool"]; ok {
		t.Errorf("empty tool should be popped, got %v", record["tool"])
	}
	if record["child_count"] != 0 || record["has_detail"] != true {
		t.Errorf("counts = %v/%v", record["child_count"], record["has_detail"])
	}
}

func TestSummaryIncludesTitleAndResolverFallback(t *testing.T) {
	setupRuntime(t)
	defer func() { SpaceHookTitleResolver = nil }()

	SpaceHookTitleResolver = func(agent, cwd, sessionID string) string {
		if agent == "codex" && sessionID == "sess-42" {
			return "Resolved From Session Store"
		}
		return ""
	}
	event := NormalizeEvent("codex", "PreToolUse", map[string]any{
		"session_id": "sess-42",
		"cwd":        "/tmp/work",
		"tool_name":  "shell",
		"tool_input": map[string]any{"command": "ls"},
	}, map[string]string{})
	if err := MergeIntoStore("dots", 3, event); err != nil {
		t.Fatal(err)
	}
	summary := SpaceHooksSummary()
	pane := mustGet(t, summary, "panes", "dots", "3")
	if pane["title"] != "Resolved From Session Store" {
		t.Errorf("pane title = %v, want Resolved From Session Store", pane["title"])
	}
}

func TestSummaryIncludesOutcomesWithTitleResolverAndSpoolReplay(t *testing.T) {
	setupRuntime(t)
	t.Setenv("HARNESS_PROFILE", "")
	defer func() {
		SpaceHookTitleResolver = nil
		SpaceHookProfileResolver = nil
	}()

	SpaceHookTitleResolver = func(agent, cwd, sessionID string) string {
		if agent == "codex" && sessionID == "sess-done" {
			return "Add recent outcomes"
		}
		return ""
	}
	SpaceHookProfileResolver = func(agent, sessionID string) string {
		if agent == "codex" && sessionID == "sess-done" {
			return "pratham"
		}
		return ""
	}
	ev := NormalizeEvent("codex", "Stop", map[string]any{
		"session_id":             "sess-done",
		"cwd":                    "/tmp/work",
		"last_assistant_message": "All tests pass",
	}, map[string]string{})
	entry := map[string]any{"session": "beta", "pane": 9}
	for k, v := range ev {
		entry[k] = v
	}
	if err := AppendSpool(entry); err != nil {
		t.Fatal(err)
	}
	summary := SpaceHooksSummary()
	outcomes, ok := summary["outcomes"].([]any)
	if !ok || len(outcomes) != 1 {
		t.Fatalf("outcomes = %v, want 1 entry", summary["outcomes"])
	}
	o, _ := outcomes[0].(map[string]any)
	if o["session"] != "beta" || o["agent"] != "codex" || o["outcome"] != "done" || o["title"] != "Add recent outcomes" || o["profile"] != "pratham" {
		t.Errorf("unexpected outcome summary: %v", o)
	}
}

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLoadRoundTrip(t *testing.T) {
	setupRuntime(t)
	store := map[string]any{"v": 1, "updated_ms": int64(42), "panes": map[string]any{
		"dots": map[string]any{"1": map[string]any{
			"v": 1, "agent": "claude", "app": "claude-code",
			"state": "working", "kind": nil, "event": "pre_tool",
			"text": "old", "ts_ms": int64(1), "seq": int64(1),
			"children": []any{},
		}},
	}}
	if err := WriteStore(store); err != nil {
		t.Fatalf("WriteStore: %v", err)
	}
	loaded := LoadStore()
	rec := mustGet(t, loaded, "panes", "dots", "1")
	if rec["text"] != "old" {
		t.Errorf("text = %v", rec["text"])
	}
	// Store files are 0600.
	if fi, err := os.Stat(StorePath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, err = %v", fi.Mode(), err)
	}
	// Keys are sorted (sort_keys=True parity).
	raw, _ := os.ReadFile(StorePath())
	if !strings.Contains(string(raw), `"agent":"claude"`) {
		t.Errorf("unexpected store bytes: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("store is not JSON: %v", err)
	}
}

func TestWriteStoreFailure(t *testing.T) {
	setupRuntime(t)
	// Point the store at a path whose parent is a regular file: the atomic
	// write must fail instead of silently dropping data.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPACES_HOOKS_STORE", filepath.Join(blocker, "hooks.store.json"))
	if err := WriteStore(map[string]any{"v": 1}); err == nil {
		t.Errorf("expected error writing through a file parent")
	}
}

func TestLoadStoreMalformed(t *testing.T) {
	setupRuntime(t)
	for name, content := range map[string]string{
		"garbage":   "{not json",
		"wrongtype": `[1,2,3]`,
		"noshape":   `{"v":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(StorePath(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded := LoadStore()
			panes, _ := loaded["panes"].(map[string]any)
			if len(panes) != 0 {
				t.Errorf("expected empty store, got %v", loaded)
			}
		})
	}
}

func TestMergeIntoStoreWritesRecord(t *testing.T) {
	setupRuntime(t)
	event := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
	}, map[string]string{})
	if event == nil {
		t.Fatal("nil event")
	}
	if err := MergeIntoStore("dots", 7, event); err != nil {
		t.Fatalf("MergeIntoStore: %v", err)
	}
	rec := mustGet(t, LoadStore(), "panes", "dots", "7")
	if rec["state"] != "working" {
		t.Errorf("state = %v", rec["state"])
	}
	// Float pane keys from JSON round-trips stay integral.
	if err := MergeIntoStore("dots", 7.0, event); err != nil {
		t.Fatalf("MergeIntoStore float pane: %v", err)
	}
}

func TestStorePrunesStaleRecords(t *testing.T) {
	setupRuntime(t)
	old := map[string]any{
		"v": 1, "agent": "claude", "app": "claude-code",
		"state": "working", "kind": nil, "event": "pre_tool",
		"text": "old", "ts_ms": int64(1), "seq": int64(1),
		"children": []any{},
	}
	store := map[string]any{"v": 1, "updated_ms": int64(1),
		"panes": map[string]any{"dots": map[string]any{"1": old}}}
	if err := WriteStore(store); err != nil {
		t.Fatal(err)
	}
	loaded := LoadStore()
	if mustGet(t, loaded, "panes", "dots", "1")["text"] != "old" {
		t.Fatal("round trip lost record")
	}
	pruned := PruneStore(loaded, Now())
	if panes, _ := pruned["panes"].(map[string]any); len(panes) != 0 {
		t.Errorf("stale record survived: %v", panes)
	}
}

func TestApplyEventToCopySkipsKeyless(t *testing.T) {
	setupRuntime(t)
	store := map[string]any{"v": 1, "updated_ms": Now(), "panes": map[string]any{}}
	ApplyEventToCopy(store, map[string]any{"event": "x"}, Now())
	if panes, _ := store["panes"].(map[string]any); len(panes) != 0 {
		t.Errorf("keyless entry applied: %v", panes)
	}
}

func TestOutcomesPersistAcrossTurnsPaneExpiryAndDedupe(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// Turn 1: prompt -> stop (with profile and session_id).
	p1 := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-1",
		"prompt":     "Fix login flow",
		"cwd":        "/repo/app",
		"model":      "claude-opus-4",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 7, p1); err != nil {
		t.Fatal(err)
	}

	cur = base + 5_000
	s1 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-1",
		"last_assistant_message": "Fixed login flow",
		"cwd":                    "/repo/app",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 7, s1); err != nil {
		t.Fatal(err)
	}

	// Subsequent idle_prompt notification while already done must not duplicate outcome.
	cur = base + 6_000
	dupNotif := NormalizeEvent("claude", "Notification", map[string]any{
		"session_id":        "sess-1",
		"notification_type": "idle_prompt",
	}, map[string]string{})
	if err := MergeIntoStore("alpha", 7, dupNotif); err != nil {
		t.Fatal(err)
	}

	// Subagent child stop must not record a parent outcome.
	cur = base + 7_000
	childStop := NormalizeEvent("claude", "SubagentStop", map[string]any{
		"session_id":  "sess-1",
		"agent_id":    "sub-1",
		"agent_type":  "Explore",
		"description": "search repo",
	}, map[string]string{})
	if err := MergeIntoStore("alpha", 7, childStop); err != nil {
		t.Fatal(err)
	}

	// Turn 2 on a second pane/conversation fails -> both distinct conversations kept.
	cur = base + 20_000
	p2 := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-2",
		"prompt":     "Run migration",
		"cwd":        "/repo/app",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 8, p2); err != nil {
		t.Fatal(err)
	}

	cur = base + 30_000
	f2 := NormalizeEvent("claude", "StopFailure", map[string]any{
		"session_id": "sess-2",
		"error":      "migration failed",
		"cwd":        "/repo/app",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 8, f2); err != nil {
		t.Fatal(err)
	}

	// Reopening sess-1 on another turn/pane and completing again updates sess-1 in place (no duplicate).
	cur = base + 40_000
	p1b := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-1",
		"prompt":     "Fix login flow",
		"cwd":        "/repo/app",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 9, p1b); err != nil {
		t.Fatal(err)
	}
	cur = base + 45_000
	s1b := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-1",
		"last_assistant_message": "Fixed login flow v2",
		"cwd":                    "/repo/app",
	}, map[string]string{"HARNESS_PROFILE": "work"})
	if err := MergeIntoStore("alpha", 9, s1b); err != nil {
		t.Fatal(err)
	}

	// Advance past FailedTTLMS (30m) so pane records prune, but within OutcomeTTLMS (24h).
	cur = base + FailedTTLMS + 60_000
	pruned := PruneStore(LoadStore(), cur)
	if panes, _ := pruned["panes"].(map[string]any); len(panes) != 0 {
		t.Fatalf("expected stale pane to prune, got %v", panes)
	}
	outcomes, _ := pruned["outcomes"].([]any)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes count = %d, want 2 (no duplicate for sess-1): %v", len(outcomes), outcomes)
	}
	// Newest first: sess-1 updated done, then sess-2 failed.
	o0, _ := outcomes[0].(map[string]any)
	o1, _ := outcomes[1].(map[string]any)
	if o0["outcome"] != "done" || o0["session_id"] != "sess-1" || o0["text"] != "Fixed login flow v2" || o0["profile"] != "work" {
		t.Errorf("o0 = %v", o0)
	}
	if o1["outcome"] != "failed" || o1["session"] != "alpha" || o1["session_id"] != "sess-2" {
		t.Errorf("o1 = %v", o1)
	}

	// Advance past OutcomeTTLMS (24h) -> outcomes expire too.
	cur = base + OutcomeTTLMS + 60_000
	expired := PruneStore(pruned, cur)
	if rem, _ := expired["outcomes"].([]any); len(rem) != 0 {
		t.Errorf("expected expired outcomes to prune, got %v", rem)
	}
}

func TestOutcomesCappedAtMaxOutcomes(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	for i := 0; i < MaxOutcomes+5; i++ {
		cur = base + int64(i)*10_000
		ev := NormalizeEvent("codex", "Stop", map[string]any{
			"session_id":             stringKey(i),
			"last_assistant_message": "done " + stringKey(i),
		}, map[string]string{})
		if err := MergeIntoStore("work", i, ev); err != nil {
			t.Fatal(err)
		}
	}
	loaded := LoadStore()
	outcomes, _ := loaded["outcomes"].([]any)
	if len(outcomes) != MaxOutcomes {
		t.Fatalf("outcomes len = %d, want %d", len(outcomes), MaxOutcomes)
	}
	newest, _ := outcomes[0].(map[string]any)
	if newest["session_id"] != stringKey(MaxOutcomes+4) {
		t.Errorf("newest session_id = %v, want %s", newest["session_id"], stringKey(MaxOutcomes+4))
	}
}

func TestRecordOutcomePersistsAndDeduplicatesWithinWindow(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	if err := RecordOutcome(map[string]any{
		"session":    "model-router",
		"pane":       "%14",
		"agent":      "antigravity",
		"profile":    "pratham",
		"outcome":    "done",
		"text":       "finished",
		"title":      "Implement ATT-7",
		"session_id": "conv-123",
		"cwd":        "/repo",
		"ts_ms":      base,
	}); err != nil {
		t.Fatal(err)
	}
	// Reopening and closing the same conversation on a different pane replaces
	// the old entry instead of creating a duplicate, preserving rich fields on same-window repeats.
	cur = base + 3_000
	if err := RecordOutcome(map[string]any{
		"session":    "model-router",
		"pane":       "18",
		"agent":      "antigravity",
		"outcome":    "done",
		"text":       "finished",
		"session_id": "conv-123",
		"ts_ms":      cur,
	}); err != nil {
		t.Fatal(err)
	}
	// A different conversation that failed is kept separately.
	cur = base + 5_000
	if err := RecordOutcome(map[string]any{
		"session":    "model-router",
		"pane":       "19",
		"agent":      "antigravity",
		"outcome":    "error",
		"text":       "exited with status 1",
		"session_id": "conv-456",
		"ts_ms":      cur,
	}); err != nil {
		t.Fatal(err)
	}

	loaded := LoadStore()
	outcomes, _ := loaded["outcomes"].([]any)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes len = %d, want 2: %v", len(outcomes), outcomes)
	}
	o0, _ := outcomes[0].(map[string]any)
	o1, _ := outcomes[1].(map[string]any)
	if o0["outcome"] != "failed" || o0["session_id"] != "conv-456" {
		t.Errorf("o0 = %v", o0)
	}
	if o1["outcome"] != "done" || o1["profile"] != "pratham" || o1["session_id"] != "conv-123" || o1["title"] != "Implement ATT-7" {
		t.Errorf("o1 = %v", o1)
	}

	// RemoveOutcome removes conv-123 when reopened into a live tab.
	if err := RemoveOutcome(map[string]any{"agent": "antigravity", "session_id": "conv-123"}); err != nil {
		t.Fatal(err)
	}
	afterRemove, _ := LoadStore()["outcomes"].([]any)
	if len(afterRemove) != 1 {
		t.Fatalf("after RemoveOutcome len = %d, want 1: %v", len(afterRemove), afterRemove)
	}
}

func TestClearPaneAndFailedOutcomePreservedAgainstGenericFinishedExit(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// Seed a failed turn on pane 41 with model metadata.
	p := NormalizeEvent("antigravity", "PreInvocation", map[string]any{
		"session_id": "conv-fail",
		"prompt":     "Deploy release",
		"model":      "gemini-4-neon-high",
		"cwd":        "/repo",
	}, map[string]string{"HARNESS_PROFILE": "pratham"})
	if err := MergeIntoStore("model-router", 41, p); err != nil {
		t.Fatal(err)
	}
	cur = base + 5_000
	f := NormalizeEvent("antigravity", "Stop", map[string]any{
		"session_id":        "conv-fail",
		"terminationReason": "ERROR",
		"error":             "build failed",
		"model":             "gemini-4-neon-high",
		"cwd":               "/repo",
	}, map[string]string{"HARNESS_PROFILE": "pratham"})
	if err := MergeIntoStore("model-router", 41, f); err != nil {
		t.Fatal(err)
	}

	// Simulate Ctrl+D exit 2 minutes later: recordLaunchOutcome preserves failed
	// and clears the live pane entry, then async __osc-watch delivers generic
	// "done"/"finished" 100ms later.
	cur = base + 120_000
	if err := RecordOutcome(map[string]any{
		"session":    "model-router",
		"pane":       "41",
		"agent":      "antigravity",
		"profile":    "pratham",
		"outcome":    "failed",
		"text":       "failed",
		"title":      "Deploy release",
		"session_id": "conv-fail",
		"cwd":        "/repo",
		"ts_ms":      cur,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ClearPane("model-router", 41); err != nil {
		t.Fatal(err)
	}
	if st := PaneState("model-router", 41); st != "" {
		t.Fatalf("PaneState after ClearPane = %q, want empty", st)
	}

	cur = base + 120_100
	if err := RecordOutcome(map[string]any{
		"session":    "model-router",
		"pane":       "41",
		"agent":      "antigravity",
		"outcome":    "done",
		"text":       "finished",
		"session_id": "conv-fail",
		"ts_ms":      cur,
	}); err != nil {
		t.Fatal(err)
	}

	loaded := LoadStore()
	if panes, _ := loaded["panes"].(map[string]any); len(panes) != 0 {
		t.Fatalf("expected live panes map empty after ClearPane, got %v", panes)
	}
	outcomes, _ := loaded["outcomes"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes len = %d, want 1: %v", len(outcomes), outcomes)
	}
	o0, _ := outcomes[0].(map[string]any)
	if o0["outcome"] != "failed" {
		t.Fatalf("generic exit finished clobbered failed outcome: %v", o0)
	}
	if o0["model"] != "gemini-4-neon-high" || o0["profile"] != "pratham" {
		t.Errorf("expected model and profile preserved across exit outcome: %v", o0)
	}
}

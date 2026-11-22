package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOpenCodeCanonicalEventAliases(t *testing.T) {
	setupRuntime(t)
	cases := []struct {
		raw       string
		canonical string
		state     string
		kind      string
	}{
		{"session.busy", "prompt", "working", ""},
		{"permission.asked", "permission", "blocked", "permission"},
		{"permission.updated", "permission", "blocked", "permission"},
		{"question.asked", "elicitation", "blocked", "question"},
		{"question.replied", "elicitation_result", "working", ""},
		{"question.rejected", "elicitation_result", "working", ""},
		{"permission.replied", "working", "working", ""},
		{"session.error", "stop_failure", "failed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := CanonicalEvent(tc.raw); got != tc.canonical {
				t.Fatalf("CanonicalEvent(%q) = %q, want %q", tc.raw, got, tc.canonical)
			}
			ev := NormalizeEvent("opencode", tc.raw, map[string]any{"session_id": "oc-1"}, map[string]string{})
			if ev == nil {
				t.Fatalf("NormalizeEvent(%q) returned nil", tc.raw)
			}
			if ev["state"] != tc.state {
				t.Errorf("state = %v, want %q", ev["state"], tc.state)
			}
			gotKind, _ := ev["kind"].(string)
			if gotKind != tc.kind {
				t.Errorf("kind = %q, want %q", gotKind, tc.kind)
			}
		})
	}
}

func TestSourceAndUsagePreservedThroughSummaryAndDetail(t *testing.T) {
	setupRuntime(t)

	// Default source is "hook".
	ev1 := NormalizeEvent("claude", "PreToolUse", map[string]any{
		"session_id": "sess-1",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "go test"},
		"usage": map[string]any{
			"input_tokens":      1200,
			"output_tokens":     350,
			"cache_read_tokens": 800,
		},
	}, map[string]string{})
	if ev1 == nil {
		t.Fatal("ev1 is nil")
	}
	if ev1["source"] != "hook" {
		t.Fatalf("default source = %v, want hook", ev1["source"])
	}
	if err := MergeIntoStore("work", 7, ev1); err != nil {
		t.Fatalf("MergeIntoStore ev1: %v", err)
	}

	// Second event from router enriches cost and reasoning tokens while
	// preserving existing input/cache_read tokens.
	Now = func() int64 { return 1_700_000_005_000 }
	ev2 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "sess-1",
		"source":     "router",
		"usage": map[string]any{
			"output_tokens":      500,
			"reasoning_tokens":   120,
			"cache_write_tokens": 64,
			"cost_usd":           0.042,
		},
	}, map[string]string{})
	if ev2 == nil {
		t.Fatal("ev2 is nil")
	}
	if ev2["source"] != "router" {
		t.Fatalf("explicit source = %v, want router", ev2["source"])
	}
	if err := MergeIntoStore("work", 7, ev2); err != nil {
		t.Fatalf("MergeIntoStore ev2: %v", err)
	}

	// Verify typed Summary() and Detail().
	snap := Summary()
	pane, ok := snap.Panes["work"]["7"]
	if !ok {
		t.Fatalf("missing pane work/7 in Summary: %+v", snap.Panes)
	}
	if pane.Source != "router" {
		t.Errorf("Summary Source = %q, want router", pane.Source)
	}
	if pane.Usage == nil {
		t.Fatal("Summary Usage is nil")
	}
	if pane.Usage.InputTokens != 1200 || pane.Usage.OutputTokens != 500 ||
		pane.Usage.ReasoningTokens != 120 || pane.Usage.CacheReadTokens != 800 ||
		pane.Usage.CacheWriteTokens != 64 || pane.Usage.CostUSD != 0.042 {
		t.Errorf("Summary Usage = %+v", pane.Usage)
	}
	if !pane.HasDetail {
		t.Errorf("HasDetail = false, want true")
	}

	det := Detail("work", 7)
	if det.Record == nil {
		t.Fatal("Detail record is nil")
	}
	if det.Record.Source != "router" || det.Record.Usage == nil || det.Record.Usage.CostUSD != 0.042 {
		t.Errorf("Detail Source/Usage = %q / %+v", det.Record.Source, det.Record.Usage)
	}

	// Verify untyped SpaceHooksSummary() / SpaceHooksDetail().
	rawSum := SpaceHooksSummary()
	rawPane := mustGet(t, rawSum, "panes", "work", "7")
	if rawPane["source"] != "router" {
		t.Errorf("raw summary source = %v", rawPane["source"])
	}
	if _, ok := rawPane["usage"].(map[string]any); !ok {
		t.Errorf("raw summary usage = %T (%v)", rawPane["usage"], rawPane["usage"])
	}

	// Usage alone is enough to mark has_detail=true.
	Now = func() int64 { return 1_700_000_010_000 }
	usageOnly := NormalizeEvent("opencode", "session.busy", map[string]any{
		"source": "daemon_sse",
		"usage":  map[string]any{"input_tokens": 50},
	}, map[string]string{})
	if err := MergeIntoStore("work", 8, usageOnly); err != nil {
		t.Fatalf("MergeIntoStore usageOnly: %v", err)
	}
	sum8 := Summary().Panes["work"]["8"]
	if !sum8.HasDetail {
		t.Errorf("usage-only pane HasDetail = false, want true")
	}
	if sum8.Source != "daemon_sse" {
		t.Errorf("usage-only pane Source = %q, want daemon_sse", sum8.Source)
	}

	// Child event preserves source when record has no source yet.
	childEv := NormalizeEvent("opencode", "SubagentStart", map[string]any{
		"source":   "daemon_sse",
		"agent_id": "sub-1",
	}, map[string]string{})
	mergedChild := MergeEvent(nil, childEv, 2000)
	if mergedChild["source"] != "daemon_sse" {
		t.Errorf("child-initialized record source = %v, want daemon_sse", mergedChild["source"])
	}
}

func TestDaemonEventToNormalized(t *testing.T) {
	setupRuntime(t)

	// 1. Parent tool event with usage.
	toolNorm := DaemonEventToNormalized(DaemonEvent{
		Agent:      "opencode",
		Event:      "pre_tool",
		RawEvent:   "tool.execute.before",
		State:      "working",
		Title:      "Refactor telemetry",
		Prompt:     "Add daemon watch manager",
		Tool:       "bash",
		ToolInput:  "go test ./...",
		ToolOutput: "PASS",
		Reply:      "Tests passed",
		Model:      "claude-sonnet-4",
		SessionID:  "oc-sess-1",
		CWD:        "/tmp/repo",
		Profile:    "default",
		TsMs:       123456,
		Usage: &Usage{
			InputTokens:     500,
			OutputTokens:    100,
			CacheReadTokens: 250,
			CostUSD:         0.015,
		},
	})
	if toolNorm["source"] != "daemon_sse" {
		t.Errorf("default source = %v, want daemon_sse", toolNorm["source"])
	}
	if toolNorm["agent"] != "opencode" || toolNorm["app"] != "opencode" {
		t.Errorf("agent/app = %v/%v", toolNorm["agent"], toolNorm["app"])
	}
	if toolNorm["text"] != "bash · go test ./..." {
		t.Errorf("auto preview text = %v", toolNorm["text"])
	}
	if toolNorm["tool"] != "bash" || toolNorm["tool_input"] != "go test ./..." || toolNorm["tool_output"] != "PASS" {
		t.Errorf("tool fields = %v / %v / %v", toolNorm["tool"], toolNorm["tool_input"], toolNorm["tool_output"])
	}
	if toolNorm["title"] != "Refactor telemetry" || toolNorm["prompt"] != "Add daemon watch manager" || toolNorm["reply"] != "Tests passed" {
		t.Errorf("text fields = %v / %v / %v", toolNorm["title"], toolNorm["prompt"], toolNorm["reply"])
	}
	if toolNorm["model"] != "claude-sonnet-4" || toolNorm["session_id"] != "oc-sess-1" || toolNorm["cwd"] != "/tmp/repo" || toolNorm["profile"] != "default" {
		t.Errorf("meta fields = %v / %v / %v / %v", toolNorm["model"], toolNorm["session_id"], toolNorm["cwd"], toolNorm["profile"])
	}
	if toolNorm["ts_ms"] != int64(123456) {
		t.Errorf("ts_ms = %v", toolNorm["ts_ms"])
	}
	if toInt64(toolNorm["seq"]) == 0 {
		t.Errorf("seq should be non-zero")
	}
	usageMap, ok := toolNorm["usage"].(map[string]any)
	if !ok || toInt64(usageMap["input_tokens"]) != 500 || toInt64(usageMap["output_tokens"]) != 100 {
		t.Errorf("usage = %v", toolNorm["usage"])
	}

	// 2. Permission blocker deriving canonical event/state/kind from RawEvent.
	permNorm := DaemonEventToNormalized(DaemonEvent{
		Agent:    "opencode",
		RawEvent: "permission.asked",
		Text:     "approve bash · rm -rf /tmp/foo",
	})
	if permNorm["event"] != "permission" || permNorm["state"] != "blocked" || permNorm["kind"] != "permission" {
		t.Errorf("permission event/state/kind = %v/%v/%v", permNorm["event"], permNorm["state"], permNorm["kind"])
	}

	// 3. Child subagent event.
	childNorm := DaemonEventToNormalized(DaemonEvent{
		Agent:    "opencode",
		Event:    "subagent_start",
		RawEvent: "session.created",
		Child: &DaemonChildEvent{
			ID:      "child-42",
			Type:    "explore",
			State:   "working",
			Text:    "searching codebase",
			SinceMs: 1000,
			TsMs:    1000,
		},
	})
	childMap, ok := childNorm["child"].(map[string]any)
	if !ok {
		t.Fatalf("child = %T (%v)", childNorm["child"], childNorm["child"])
	}
	if childMap["id"] != "child-42" || childMap["type"] != "explore" || childMap["state"] != "working" {
		t.Errorf("child map = %v", childMap)
	}

	// Fold parent + child via MergeEvent to verify compatibility.
	rec := MergeEvent(nil, toolNorm, 1000)
	rec = MergeEvent(rec, childNorm, 2000)
	if rec["state"] != "working" || rec["source"] != "daemon_sse" {
		t.Errorf("merged state/source = %v/%v", rec["state"], rec["source"])
	}
	children, _ := rec["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children count = %d, want 1", len(children))
	}
}

func TestDaemonWatchManagerLifecycle(t *testing.T) {
	setupRuntime(t)

	var (
		mu       sync.Mutex
		panes    []DaemonPaneTarget
		started  = map[int]chan struct{}{}
		canceled = map[int]chan struct{}{}
		merged   []map[string]any
		mergedCh = make(chan map[string]any, 10)
	)

	setPanes := func(next []DaemonPaneTarget) {
		mu.Lock()
		panes = append([]DaemonPaneTarget(nil), next...)
		mu.Unlock()
	}

	lister := func(ctx context.Context) ([]DaemonPaneTarget, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]DaemonPaneTarget(nil), panes...), nil
	}

	resolver := func(agent string) DaemonWatchFn {
		if agent != "opencode" {
			return nil
		}
		return func(ctx context.Context, target DaemonPaneTarget, emit func(DaemonEvent)) error {
			mu.Lock()
			sCh := started[target.PanePID]
			cCh := canceled[target.PanePID]
			mu.Unlock()
			if sCh != nil {
				close(sCh)
			}
			emit(DaemonEvent{
				RawEvent:  "session.busy",
				Text:      "working via sse",
				SessionID: "oc-1",
				Usage:     &Usage{InputTokens: 42},
			})
			<-ctx.Done()
			if cCh != nil {
				close(cCh)
			}
			return ctx.Err()
		}
	}

	mergeFn := func(session string, pane any, event map[string]any) error {
		mu.Lock()
		merged = append(merged, map[string]any{
			"session": session,
			"pane":    pane,
			"event":   event,
		})
		mu.Unlock()
		mergedCh <- event
		return nil
	}

	started[1001] = make(chan struct{})
	canceled[1001] = make(chan struct{})
	started[2002] = make(chan struct{})
	canceled[2002] = make(chan struct{})

	// Start with one matching pane (opencode) and one non-matching pane (claude).
	setPanes([]DaemonPaneTarget{
		{Session: "main", PaneID: "%5", PaneNumericID: "5", PanePID: 1001, Agent: "opencode", CWD: "/repo", Profile: "work"},
		{Session: "main", PaneID: "%6", PaneNumericID: "6", PanePID: 1002, Agent: "claude", CWD: "/repo"},
	})

	mgr := NewDaemonWatchManager(lister, resolver, mergeFn)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		mgr.Run(ctx, 20*time.Millisecond)
	}()

	// Wait for watcher 1001 to start and emit an event.
	select {
	case <-started[1001]:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher 1001 to start")
	}
	select {
	case ev := <-mergedCh:
		if ev["agent"] != "opencode" || ev["source"] != "daemon_sse" || ev["state"] != "working" || ev["profile"] != "work" {
			t.Errorf("unexpected merged event: %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for merged event")
	}
	if got := mgr.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount = %d, want 1 (claude should be skipped)", got)
	}

	// Change PID on pane %5: old watcher (1001) must be cancelled and new watcher (2002) started.
	setPanes([]DaemonPaneTarget{
		{Session: "main", PaneID: "%5", PaneNumericID: "5", PanePID: 2002, Agent: "opencode", CWD: "/repo"},
	})
	select {
	case <-canceled[1001]:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher 1001 cancellation on PID change")
	}
	select {
	case <-started[2002]:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher 2002 to start")
	}
	select {
	case <-mergedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event from watcher 2002")
	}
	if got := mgr.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount after PID change = %d, want 1", got)
	}

	// Cancel Run context: all active watchers must be cancelled and Run must exit cleanly.
	cancel()
	select {
	case <-canceled[2002]:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher 2002 cancellation on Run stop")
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to exit")
	}
	if got := mgr.ActiveCount(); got != 0 {
		t.Errorf("ActiveCount after Run stop = %d, want 0", got)
	}

	// Verify pane exits also cancel watchers during Reconcile.
	started[3003] = make(chan struct{})
	canceled[3003] = make(chan struct{})
	setPanes([]DaemonPaneTarget{
		{Session: "main", PaneID: "%9", PanePID: 3003, Agent: "opencode"},
	})
	mgr2 := NewDaemonWatchManager(lister, resolver, mergeFn)
	rctx := context.Background()
	mgr2.Reconcile(rctx)
	<-started[3003]
	if got := mgr2.ActiveCount(); got != 1 {
		t.Errorf("mgr2 ActiveCount = %d, want 1", got)
	}
	setPanes(nil)
	mgr2.Reconcile(rctx)
	select {
	case <-canceled[3003]:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher 3003 cancellation when pane exited")
	}
	if got := mgr2.ActiveCount(); got != 0 {
		t.Errorf("mgr2 ActiveCount after exit = %d, want 0", got)
	}
}

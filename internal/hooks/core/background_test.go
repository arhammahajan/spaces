package core

import (
	"strings"
	"testing"
)

func TestClassifyBackgroundTask(t *testing.T) {
	cases := []struct {
		name       string
		rawType    string
		cmd        string
		desc       string
		persistent bool
		fromCron   bool
		wantCat    string
		wantKind   string
	}{
		// Finite work (blocks turn completion)
		{"go test", "local_bash", "go test ./...", "", false, false, BgCategoryFinite, BgKindCommand},
		{"cargo test", "shell", "cargo test --workspace", "", false, false, BgCategoryFinite, BgKindCommand},
		{"npm build", "local_bash", "cd web && npm run build", "", false, false, BgCategoryFinite, BgKindCommand},
		{"vite build", "local_bash", "vite build", "", false, false, BgCategoryFinite, BgKindCommand},
		{"vitest run", "local_bash", "npx vitest run", "", false, false, BgCategoryFinite, BgKindCommand},
		{"pytest", "bash", "pytest -q tests/", "", false, false, BgCategoryFinite, BgKindCommand},
		{"tsc check", "bash", "npx tsc --noEmit", "", false, false, BgCategoryFinite, BgKindCommand},
		{"grep -w not watch", "bash", "grep -w foo src/", "", false, false, BgCategoryFinite, BgKindCommand},
		{"subagent local", "local_agent", "", "Explore codebase", false, false, BgCategoryFinite, BgKindSubagent},
		{"subagent remote", "remote_agent", "", "Run review", false, false, BgCategoryFinite, BgKindSubagent},

		// Services (long-running, does not block completion)
		{"npm run dev", "local_bash", "cd frontend && npm run dev", "", false, false, BgCategoryService, BgKindService},
		{"pnpm dev", "local_bash", "pnpm dev", "", false, false, BgCategoryService, BgKindService},
		{"vite dev", "local_bash", "npx vite --port 5173", "", false, false, BgCategoryService, BgKindService},
		{"next dev", "local_bash", "next dev", "", false, false, BgCategoryService, BgKindService},
		{"uvicorn", "local_bash", "uvicorn app.main:app --reload", "", false, false, BgCategoryService, BgKindService},
		{"python http.server", "local_bash", "python3 -m http.server 8000", "", false, false, BgCategoryService, BgKindService},
		{"docker compose up", "local_bash", "docker compose up", "", false, false, BgCategoryService, BgKindService},
		{"docker compose detached", "local_bash", "docker compose up -d", "", false, false, BgCategoryFinite, BgKindCommand},
		{"kubectl port-forward", "local_bash", "kubectl port-forward svc/api 8080:80", "", false, false, BgCategoryService, BgKindService},
		{"persistent flag", "local_bash", "./custom-daemon", "", true, false, BgCategoryService, BgKindService},
		{"explicit service type", "service", "./worker", "", false, false, BgCategoryService, BgKindService},

		// Watchers (long-running, does not block completion)
		{"cargo watch", "local_bash", "cargo watch -x test", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"nodemon", "local_bash", "nodemon server.js", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"tsc -w", "local_bash", "npx tsc -w", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"vitest --watch", "local_bash", "vitest --watch", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"npm run watch", "local_bash", "npm run watch", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"tail -f", "local_bash", "tail -f /var/log/app.log", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"kubectl logs -f", "local_bash", "kubectl logs -f deploy/api", "", false, false, BgCategoryWatcher, BgKindWatcher},
		{"monitor type", "monitor", "poll deploy status", "", false, false, BgCategoryWatcher, BgKindWatcher},

		// Scheduled wakeups / crons (does not block completion)
		{"session cron", "", "", "check build status", false, true, BgCategorySchedule, BgKindSchedule},
		{"cron type", "cron", "", "poll PR", false, false, BgCategorySchedule, BgKindSchedule},
		{"scheduled wakeup desc", "", "", "Scheduled wakeup in 5m", false, false, BgCategorySchedule, BgKindSchedule},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, kind := ClassifyBackgroundTask(tc.rawType, tc.cmd, tc.desc, tc.persistent, tc.fromCron)
			if cat != tc.wantCat || kind != tc.wantKind {
				t.Errorf("ClassifyBackgroundTask(%q, %q, %q) = (%q, %q), want (%q, %q)",
					tc.rawType, tc.cmd, tc.desc, cat, kind, tc.wantCat, tc.wantKind)
			}
		})
	}
}

func TestStopHeldWhileFiniteBackgroundWorkRunsAndCompletesLater(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// 1. User prompt starts the turn.
	promptEv := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-bg",
		"prompt":     "Run test suite and start dev server",
		"cwd":        "/repo",
	}, map[string]string{})
	if err := MergeIntoStore("work", 4, promptEv); err != nil {
		t.Fatal(err)
	}

	// 2. Claude emits Stop while a finite test (`go test ./...`), a long-running
	// service (`npm run dev`), and a scheduled wakeup (`session_crons`) are running.
	cur = base + 5_000
	stop1 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-bg",
		"last_assistant_message": "Started tests and dev server",
		"background_tasks": []any{
			map[string]any{
				"task_id":    "t-test",
				"task_type":  "local_bash",
				"status":     "running",
				"command":    "go test ./...",
				"started_at": base + 1_000,
			},
			map[string]any{
				"task_id":    "t-dev",
				"task_type":  "local_bash",
				"status":     "running",
				"command":    "npm run dev",
				"started_at": base + 2_000,
			},
		},
		"session_crons": []any{
			map[string]any{
				"id":       "cron-1",
				"schedule": "*/5 * * * *",
				"prompt":   "Check CI status",
			},
		},
	}, map[string]string{})
	if stop1["state"] != "working" {
		t.Fatalf("stop1 normalized state = %v, want working", stop1["state"])
	}
	if txt, _ := stop1["text"].(string); !strings.Contains(txt, "go test ./...") {
		t.Fatalf("stop1 text = %q, want waiting on go test ./...", txt)
	}
	resolved1 := ResolvePaneEvent("work", 4, stop1)
	if resolved1["state"] != "working" {
		t.Fatalf("resolved1 state = %v, want working", resolved1["state"])
	}
	if err := MergeIntoStore("work", 4, stop1); err != nil {
		t.Fatal(err)
	}

	// Store keeps pane working, lists all 3 background items, and has NOT recorded a done outcome yet.
	store := LoadStore()
	rec := mustGet(t, store, "panes", "work", "4")
	if rec["state"] != "working" {
		t.Fatalf("stored state after stop1 = %v, want working", rec["state"])
	}
	if outcomes, _ := store["outcomes"].([]any); len(outcomes) != 0 {
		t.Fatalf("held Stop must not record done outcome yet, got %v", outcomes)
	}
	summary := SpaceHooksSummary()
	paneSum := mustGet(t, summary, "panes", "work", "4")
	if paneSum["state"] != "working" || paneSum["finite_active"] != 1 || paneSum["service_active"] != 2 {
		t.Fatalf("summary after stop1 = %v", paneSum)
	}
	if !strings.Contains(stringKey(paneSum["waiting_on"]), "go test ./...") {
		t.Fatalf("waiting_on = %v", paneSum["waiting_on"])
	}

	// 3. idle_prompt notification arriving while test still runs must not flip state to done.
	cur = base + 6_000
	idleNotif := NormalizeEvent("claude", "Notification", map[string]any{
		"session_id":        "sess-bg",
		"notification_type": "idle_prompt",
	}, map[string]string{})
	resolvedNotif := ResolvePaneEvent("work", 4, idleNotif)
	if resolvedNotif["state"] != "working" {
		t.Fatalf("idle_prompt while test runs: resolved state = %v, want working", resolvedNotif["state"])
	}
	if err := MergeIntoStore("work", 4, idleNotif); err != nil {
		t.Fatal(err)
	}
	if PaneState("work", 4) != "working" {
		t.Fatalf("PaneState after idle_prompt = %q, want working", PaneState("work", 4))
	}

	// 4. Test finishes; later Stop arrives with ONLY the dev server and cron remaining.
	cur = base + 15_000
	stop2 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-bg",
		"last_assistant_message": "All tests passed; dev server still running",
		"background_tasks": []any{
			map[string]any{
				"task_id":    "t-dev",
				"task_type":  "local_bash",
				"status":     "running",
				"command":    "npm run dev",
				"started_at": base + 2_000,
			},
		},
		"session_crons": []any{
			map[string]any{
				"id":       "cron-1",
				"schedule": "*/5 * * * *",
				"prompt":   "Check CI status",
			},
		},
	}, map[string]string{})
	if stop2["state"] != "done" {
		t.Fatalf("stop2 normalized state = %v, want done (only service+cron remain)", stop2["state"])
	}
	resolved2 := ResolvePaneEvent("work", 4, stop2)
	if resolved2["state"] != "done" {
		t.Fatalf("resolved2 state = %v, want done", resolved2["state"])
	}
	if err := MergeIntoStore("work", 4, stop2); err != nil {
		t.Fatal(err)
	}

	// Now pane is done, done outcome is recorded, finished test has frozen ended_ms,
	// and service + cron remain listed in background details!
	store2 := LoadStore()
	rec2 := mustGet(t, store2, "panes", "work", "4")
	if rec2["state"] != "done" {
		t.Fatalf("stored state after stop2 = %v, want done", rec2["state"])
	}
	if rec2["waiting_on"] != nil {
		t.Fatalf("waiting_on should be cleared on done, got %v", rec2["waiting_on"])
	}
	if outcomes, _ := store2["outcomes"].([]any); len(outcomes) != 1 {
		t.Fatalf("expected 1 done outcome after stop2, got %v", outcomes)
	}
	detail := SpaceHooksDetail("work", 4)
	drec, _ := detail["record"].(map[string]any)
	bgList, _ := drec["background_tasks"].([]any)
	if len(bgList) != 3 {
		t.Fatalf("expected 3 tracked background items (finished test + service + cron), got %d: %v", len(bgList), bgList)
	}
	foundFinishedTest := false
	for _, raw := range bgList {
		m, _ := raw.(map[string]any)
		if m["id"] == "t-test" {
			foundFinishedTest = true
			if m["state"] != "done" || toInt64(m["since_ms"]) != base+1_000 || toInt64(m["ended_ms"]) != cur {
				t.Errorf("t-test = %v, want state=done since=%d ended=%d", m, base+1_000, cur)
			}
		}
	}
	if !foundFinishedTest {
		t.Errorf("finished t-test missing from background_tasks: %v", bgList)
	}
	sum2 := mustGet(t, SpaceHooksSummary(), "panes", "work", "4")
	if sum2["finite_active"] != 0 || sum2["service_active"] != 2 {
		t.Errorf("summary counts after stop2: finite=%v service=%v", sum2["finite_active"], sum2["service_active"])
	}
}

func TestStopHeldWhileSubagentRunningAndCompletesAfterSubagentStop(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	promptEv := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-sub",
		"prompt":     "Investigate auth bug",
	}, map[string]string{})
	if err := MergeIntoStore("work", 9, promptEv); err != nil {
		t.Fatal(err)
	}

	// Subagent starts at t+1s.
	cur = base + 1_000
	subStart := NormalizeEvent("claude", "SubagentStart", map[string]any{
		"session_id":  "sess-sub",
		"agent_id":    "sub-42",
		"agent_type":  "Explore",
		"description": "Search auth middleware",
	}, map[string]string{})
	if err := MergeIntoStore("work", 9, subStart); err != nil {
		t.Fatal(err)
	}

	// Stop arrives at t+3s without background_tasks field while sub-42 is still running.
	cur = base + 3_000
	stop1 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-sub",
		"last_assistant_message": "Waiting for explore subagent",
	}, map[string]string{})
	resolved1 := ResolvePaneEvent("work", 9, stop1)
	if resolved1["state"] != "working" {
		t.Fatalf("ResolvePaneEvent during active subagent: state = %v, want working", resolved1["state"])
	}
	if txt, _ := resolved1["text"].(string); !strings.Contains(txt, "Explore") {
		t.Fatalf("resolved1 text = %q, want Explore mention", txt)
	}
	if err := MergeIntoStore("work", 9, resolved1); err != nil {
		t.Fatal(err)
	}
	if PaneState("work", 9) != "working" {
		t.Fatalf("store state = %q, want working", PaneState("work", 9))
	}

	// Subagent finishes at t+9s; original since_ms (t+1s) must be preserved and ended_ms frozen at t+9s.
	cur = base + 9_000
	subStop := NormalizeEvent("claude", "SubagentStop", map[string]any{
		"session_id":  "sess-sub",
		"agent_id":    "sub-42",
		"agent_type":  "Explore",
		"description": "Search auth middleware",
	}, map[string]string{})
	if err := MergeIntoStore("work", 9, subStop); err != nil {
		t.Fatal(err)
	}

	// Subsequent Stop at t+10s marks the turn done.
	cur = base + 10_000
	stop2 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-sub",
		"last_assistant_message": "Found the bug",
	}, map[string]string{})
	resolved2 := ResolvePaneEvent("work", 9, stop2)
	if resolved2["state"] != "done" {
		t.Fatalf("resolved2 after SubagentStop: state = %v, want done", resolved2["state"])
	}
	if err := MergeIntoStore("work", 9, resolved2); err != nil {
		t.Fatal(err)
	}
	rec := mustGet(t, LoadStore(), "panes", "work", "9")
	if rec["state"] != "done" {
		t.Fatalf("final state = %v, want done", rec["state"])
	}
	children, _ := rec["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v", children)
	}
	ch, _ := children[0].(map[string]any)
	if ch["state"] != "done" || toInt64(ch["since_ms"]) != base+1_000 || toInt64(ch["ended_ms"]) != base+9_000 {
		t.Errorf("child record = %v, want state=done since=%d ended=%d", ch, base+1_000, base+9_000)
	}
}

func TestStopWithAuthoritativeEmptyBackgroundClearsStaleSubagentAndDedupe(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	subStart := NormalizeEvent("claude", "SubagentStart", map[string]any{
		"session_id":  "sess-r",
		"agent_id":    "sub-1",
		"agent_type":  "Plan",
		"description": "Draft plan",
	}, map[string]string{})
	if err := MergeIntoStore("work", 5, subStart); err != nil {
		t.Fatal(err)
	}

	// Stop #1 at t+1000ms held by finite task.
	cur = base + 1_000
	stop1 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "sess-r",
		"background_tasks": []any{
			map[string]any{"task_id": "sub-1", "task_type": "local_agent", "status": "running", "description": "Draft plan"},
		},
	}, map[string]string{})
	if err := MergeIntoStore("work", 5, stop1); err != nil {
		t.Fatal(err)
	}
	if PaneState("work", 5) != "working" {
		t.Fatalf("after stop1 state = %q, want working", PaneState("work", 5))
	}

	// Stop #2 arrives only 200ms later (within DedupeMS) with authoritative empty background_tasks: []
	// (even without SubagentStop) -> must NOT be deduped and must transition cleanly to done!
	cur = base + 1_200
	stop2 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id":             "sess-r",
		"last_assistant_message": "Plan ready",
		"background_tasks":       []any{},
	}, map[string]string{})
	if err := MergeIntoStore("work", 5, stop2); err != nil {
		t.Fatal(err)
	}
	if PaneState("work", 5) != "done" {
		t.Fatalf("after rapid clearing stop2 state = %q, want done", PaneState("work", 5))
	}
}

func TestBackgroundToolLaunchesAndTaskStop(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// Bash run_in_background for a test and a dev server.
	bgTest := NormalizeEvent("claude", "PostToolUse", map[string]any{
		"session_id": "sess-tool",
		"tool_name":  "Bash",
		"tool_input": map[string]any{
			"command":           "pytest -q",
			"description":       "Run pytest",
			"run_in_background": true,
		},
		"tool_response": map[string]any{"backgroundTaskId": "bg-pytest"},
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, bgTest); err != nil {
		t.Fatal(err)
	}

	cur = base + 2_000
	bgDev := NormalizeEvent("claude", "PostToolUse", map[string]any{
		"session_id": "sess-tool",
		"tool_name":  "Bash",
		"tool_input": map[string]any{
			"command":           "npm run dev",
			"description":       "Start Vite dev server",
			"run_in_background": true,
		},
		"tool_response": map[string]any{"backgroundTaskId": "bg-vite"},
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, bgDev); err != nil {
		t.Fatal(err)
	}

	// Stop without background_tasks array -> held because bg-pytest is still finite/running!
	cur = base + 4_000
	stop1 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "sess-tool",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, stop1); err != nil {
		t.Fatal(err)
	}
	if PaneState("dev", 2) != "working" {
		t.Fatalf("expected working while bg-pytest runs, got %q", PaneState("dev", 2))
	}

	// TaskCompleted marks bg-pytest failed with exit code 1.
	cur = base + 8_000
	doneEv := NormalizeEvent("claude", "TaskCompleted", map[string]any{
		"session_id": "sess-tool",
		"task_id":    "bg-pytest",
		"status":     "failed",
		"exit_code":  1,
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, doneEv); err != nil {
		t.Fatal(err)
	}

	// Now Stop succeeds as done (only bg-vite service remains active).
	cur = base + 9_000
	stop2 := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "sess-tool",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, stop2); err != nil {
		t.Fatal(err)
	}
	if PaneState("dev", 2) != "done" {
		t.Fatalf("expected done after bg-pytest finished and only service remains, got %q", PaneState("dev", 2))
	}

	// TaskStop stops bg-vite.
	cur = base + 12_000
	killEv := NormalizeEvent("claude", "PostToolUse", map[string]any{
		"session_id": "sess-tool",
		"tool_name":  "TaskStop",
		"tool_input": map[string]any{"task_id": "bg-vite"},
	}, map[string]string{})
	if err := MergeIntoStore("dev", 2, killEv); err != nil {
		t.Fatal(err)
	}
	detail := SpaceHooksDetail("dev", 2)
	rec, _ := detail["record"].(map[string]any)
	bgList, _ := rec["background_tasks"].([]any)
	states := map[string]string{}
	outcomes := map[string]string{}
	for _, raw := range bgList {
		m, _ := raw.(map[string]any)
		states[stringKey(m["id"])] = stringKey(m["state"])
		outcomes[stringKey(m["id"])] = stringKey(m["outcome"])
	}
	if states["bg-pytest"] != "failed" || outcomes["bg-pytest"] != "exit 1" {
		t.Errorf("bg-pytest state/outcome = %q/%q, want failed/exit 1", states["bg-pytest"], outcomes["bg-pytest"])
	}
	if states["bg-vite"] != "stopped" {
		t.Errorf("bg-vite state = %q, want stopped", states["bg-vite"])
	}
}

func TestAntigravitySubagentLifecycleAndStopArbitration(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// 1. Root conversation starts and receives a prompt.
	startEv := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": "root-conv-1",
		"cwd":            "/home/u/dots",
		"modelName":      "gemini-4-neon-high",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, startEv); err != nil {
		t.Fatal(err)
	}

	cur = base + 1_000
	preInv := NormalizeEvent("antigravity", "PreInvocation", map[string]any{
		"conversationId": "root-conv-1",
		"cwd":            "/home/u/dots",
		"modelName":      "gemini-4-neon-high",
		"lastUserInput":  "Rewrite all docs using parallel subagents",
		"invocationNum":  1,
		"hookEventName":  "PreInvocation",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, preInv); err != nil {
		t.Fatal(err)
	}
	if PaneState("dots", 42) != "working" {
		t.Fatalf("after PreInvocation state = %q, want working", PaneState("dots", 42))
	}

	// 2. Race test: subagent #1's SessionStart arrives BEFORE parent's PostToolUse(invoke_subagent),
	// while subagent #2's PostToolUse(invoke_subagent) arrives BEFORE its SessionStart.
	cur = base + 2_000
	sub1Start := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId":       "sub-conv-1",
		"parentConversationId": "root-conv-1",
		"rootConversationId":   "root-conv-1",
		"agentName":            "doc-rewriter",
		"lastUserInput":        "Rewrite docs/README.md completely from scratch...",
		"hookEventName":        "SessionStart",
	}, map[string]string{})
	if sub1Start["child"] == nil {
		t.Fatalf("expected child record on subagent SessionStart, got %v", sub1Start)
	}
	if err := MergeIntoStore("dots", 42, sub1Start); err != nil {
		t.Fatal(err)
	}
	// Parent session_id and state must NOT be overwritten by subagent SessionStart.
	rec := mustGet(t, LoadStore(), "panes", "dots", "42")
	if rec["session_id"] != "root-conv-1" || rec["state"] != "working" {
		t.Fatalf("subagent SessionStart clobbered parent record: session_id=%v state=%v", rec["session_id"], rec["state"])
	}

	cur = base + 2_100
	invoke1 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-conv-1",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "call-1",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "doc-rewriter",
				"Role":     "Root README rewriter",
				"Prompt":   "Rewrite docs/README.md completely from scratch...",
			},
		},
		"result": "Created the following subagent:\n{\n  \"conversationId\":  \"sub-conv-1\",\n  \"logAbsoluteUri\":  \"file:///tmp/1\"\n}",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, invoke1); err != nil {
		t.Fatal(err)
	}

	cur = base + 2_200
	invoke2 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-conv-1",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "call-2",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "research",
				"Role":     "Zellij docs verifier",
				"Prompt":   "Verify docs/zellij.md",
			},
		},
		"result": "Created the following subagent:\n{\n  \"conversationId\":  \"sub-conv-2\"\n}",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, invoke2); err != nil {
		t.Fatal(err)
	}

	// Subagent #2 fires SessionStart AFTER invoke_subagent: Role must be preserved.
	cur = base + 2_300
	sub2Start := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId":       "sub-conv-2",
		"parentConversationId": "root-conv-1",
		"rootConversationId":   "root-conv-1",
		"agentName":            "research",
		"lastUserInput":        "Very long subagent prompt text...",
		"hookEventName":        "SessionStart",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, sub2Start); err != nil {
		t.Fatal(err)
	}

	// 3. Parent finishes its turn while both subagents run: Stop fires with protojson
	// omitting fullyIdle (false). Pane MUST stay working and list both subagents!
	cur = base + 3_000
	parentStop1 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "root-conv-1",
		"executionNum":      1,
		"terminationReason": "NO_TOOL_CALL",
		"finalModelOutput":  "Launched 2 subagents. Waiting for them to finish.",
		"hookEventName":     "Stop",
	}, map[string]string{})
	resolvedStop1 := ResolvePaneEvent("dots", 42, parentStop1)
	if resolvedStop1["state"] != "working" {
		t.Fatalf("parentStop1 resolved state = %v, want working", resolvedStop1["state"])
	}
	if err := MergeIntoStore("dots", 42, resolvedStop1); err != nil {
		t.Fatal(err)
	}
	if PaneState("dots", 42) != "working" {
		t.Fatalf("pane state while subagents run = %q, want working", PaneState("dots", 42))
	}
	store1 := LoadStore()
	if outcomes, _ := store1["outcomes"].([]any); len(outcomes) != 0 {
		t.Fatalf("premature done outcome recorded while subagents run: %v", outcomes)
	}
	sum1 := mustGet(t, SpaceHooksSummary(), "panes", "dots", "42")
	if sum1["finite_active"] != 2 || sum1["child_count"] != 2 {
		t.Fatalf("summary while subagents run = %v, want finite_active=2 child_count=2", sum1)
	}
	if w := stringKey(sum1["waiting_on"]); !strings.Contains(w, "Root README rewriter") || !strings.Contains(w, "Zellij docs verifier") {
		t.Fatalf("waiting_on = %q, want both subagent roles", w)
	}

	// 4. Even after >120s (WorkingStaleMS), pane waiting on active subagents must NOT go stale.
	cur = base + 180_000
	if PaneState("dots", 42) != "working" {
		t.Fatalf("pane waiting on subagents went stale after 3m: %q", PaneState("dots", 42))
	}

	// 5. Subagent #1 finishes (Stop with parentConversationId + fullyIdle=true).
	cur = base + 185_000
	sub1Stop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":       "sub-conv-1",
		"parentConversationId": "root-conv-1",
		"rootConversationId":   "root-conv-1",
		"agentName":            "doc-rewriter",
		"terminationReason":    "NO_TOOL_CALL",
		"fullyIdle":            true,
		"hookEventName":        "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, sub1Stop); err != nil {
		t.Fatal(err)
	}
	sum2 := mustGet(t, SpaceHooksSummary(), "panes", "dots", "42")
	if sum2["state"] != "working" || sum2["finite_active"] != 1 {
		t.Fatalf("after sub1Stop summary = %v, want state=working finite_active=1", sum2)
	}

	// 6. Subagent #2 finishes, then parent wakes up and completes with fullyIdle=true.
	cur = base + 190_000
	sub2Stop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":       "sub-conv-2",
		"parentConversationId": "root-conv-1",
		"rootConversationId":   "root-conv-1",
		"agentName":            "research",
		"terminationReason":    "NO_TOOL_CALL",
		"fullyIdle":            true,
		"hookEventName":        "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("dots", 42, sub2Stop); err != nil {
		t.Fatal(err)
	}

	cur = base + 195_000
	parentStop2 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "root-conv-1",
		"executionNum":      2,
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"finalModelOutput":  "Rewrote all docs and verified every link.",
		"hookEventName":     "Stop",
	}, map[string]string{})
	resolvedStop2 := ResolvePaneEvent("dots", 42, parentStop2)
	if resolvedStop2["state"] != "done" {
		t.Fatalf("parentStop2 resolved state = %v, want done", resolvedStop2["state"])
	}
	if err := MergeIntoStore("dots", 42, resolvedStop2); err != nil {
		t.Fatal(err)
	}
	if PaneState("dots", 42) != "done" {
		t.Fatalf("final pane state = %q, want done", PaneState("dots", 42))
	}
	finalRec := mustGet(t, LoadStore(), "panes", "dots", "42")
	if finalRec["text"] != "Rewrote all docs and verified every link." {
		t.Fatalf("final text = %v", finalRec["text"])
	}
	children, _ := finalRec["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("expected 2 finished children, got %v", children)
	}
	for _, raw := range children {
		ch, _ := raw.(map[string]any)
		if ch["state"] != "done" || toInt64(ch["ended_ms"]) <= 0 {
			t.Errorf("child not cleanly finalized: %v", ch)
		}
		if ch["id"] == "sub-conv-1" && ch["text"] != "Root README rewriter" {
			t.Errorf("sub-conv-1 text = %v, want Root README rewriter", ch["text"])
		}
		if ch["id"] == "sub-conv-2" && ch["text"] != "Zellij docs verifier" {
			t.Errorf("sub-conv-2 text = %v, want Zellij docs verifier", ch["text"])
		}
	}
	if outcomes, _ := LoadStore()["outcomes"].([]any); len(outcomes) != 1 {
		t.Fatalf("expected 1 done outcome after final stop, got %v", outcomes)
	}
}

func TestAntigravityBackgroundCommandsDaemonsAndFullyIdleReconcile(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// 1. Backgrounded finite run_command + daemon run_command + schedule.
	bgTest := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-2",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-1",
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":       "go test ./...",
				"WaitMsBeforeAsync": 500,
				"toolSummary":       "Run unit tests",
			},
		},
		"result": "Created At: 2026-11-21\nTool is running as a background task with task id: root-2/task-10\nTask Description: go test ./...",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, bgTest); err != nil {
		t.Fatal(err)
	}

	cur = base + 1_000
	bgDaemon := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-2",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-2",
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":       "./my-custom-server",
				"IsDaemon":          true,
				"WaitMsBeforeAsync": 500,
				"toolSummary":       "Start dev server",
			},
		},
		"result": "Tool is running as a background task with task id: root-2/task-11",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, bgDaemon); err != nil {
		t.Fatal(err)
	}

	// Synchronous run_command (no background task id, IsDaemon=false) must NOT add a bg task.
	cur = base + 1_500
	syncCmd := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-2",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-3",
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":       "git status",
				"WaitMsBeforeAsync": 5000,
			},
		},
		"result": "Command exited with code 0.",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, syncCmd); err != nil {
		t.Fatal(err)
	}

	// 2. Stop with omitted fullyIdle -> held working on root-2/task-10 (finite), while task-11 is service.
	cur = base + 2_000
	stop1 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "root-2",
		"terminationReason": "NO_TOOL_CALL",
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, ResolvePaneEvent("work", 7, stop1)); err != nil {
		t.Fatal(err)
	}
	sum1 := mustGet(t, SpaceHooksSummary(), "panes", "work", "7")
	if sum1["state"] != "working" || sum1["finite_active"] != 1 || sum1["service_active"] != 1 {
		t.Fatalf("sum1 = %v, want state=working finite_active=1 service_active=1", sum1)
	}

	// 3. Stop with fullyIdle=true reconciles finite task-10 to done while keeping daemon task-11 active!
	cur = base + 10_000
	stop2 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "root-2",
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, ResolvePaneEvent("work", 7, stop2)); err != nil {
		t.Fatal(err)
	}
	sum2 := mustGet(t, SpaceHooksSummary(), "panes", "work", "7")
	if sum2["state"] != "done" || sum2["finite_active"] != 0 || sum2["service_active"] != 1 {
		t.Fatalf("sum2 = %v, want state=done finite_active=0 service_active=1", sum2)
	}

	// 4. manage_task kill stops the daemon task-11.
	cur = base + 12_000
	killEv := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-2",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-4",
			"name": "manage_task",
			"args": map[string]any{
				"Action": "kill",
				"TaskId": "root-2/task-11",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, killEv); err != nil {
		t.Fatal(err)
	}
	sum3 := mustGet(t, SpaceHooksSummary(), "panes", "work", "7")
	if sum3["service_active"] != 0 {
		t.Fatalf("after manage_task kill, service_active = %v, want 0", sum3["service_active"])
	}

	// 5. Stop with terminationReason=ERROR marks pane failed.
	cur = base + 15_000
	errStop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "root-2",
		"terminationReason": "ERROR",
		"error":             "upstream timeout",
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("work", 7, ResolvePaneEvent("work", 7, errStop)); err != nil {
		t.Fatal(err)
	}
	if PaneState("work", 7) != "failed" {
		t.Fatalf("after ERROR stop state = %q, want failed", PaneState("work", 7))
	}
}

func TestAntigravitySyntheticIDAdoptionWhenPostToolResultEmpty(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// invoke_subagent PostToolUse without result text -> gets synthetic_id from toolCall.id.
	inv := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-3",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "call-synth-1",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "research",
				"Role":     "Codebase Researcher",
				"Prompt":   "Inspect hooks",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("work", 8, inv); err != nil {
		t.Fatal(err)
	}

	// Subagent fires SessionStart with real conversationId -> adopts synthetic entry cleanly.
	cur = base + 500
	subStart := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId":       "real-sub-uuid-99",
		"parentConversationId": "root-3",
		"agentName":            "research",
		"lastUserInput":        "Inspect hooks in detail",
		"hookEventName":        "SessionStart",
	}, map[string]string{})
	if err := MergeIntoStore("work", 8, subStart); err != nil {
		t.Fatal(err)
	}

	// Subagent finishes -> marks the single adopted entry done!
	cur = base + 4_000
	subStop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":       "real-sub-uuid-99",
		"parentConversationId": "root-3",
		"agentName":            "research",
		"fullyIdle":            true,
		"hookEventName":        "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("work", 8, subStop); err != nil {
		t.Fatal(err)
	}

	rec := mustGet(t, LoadStore(), "panes", "work", "8")
	children, _ := rec["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("expected 1 adopted child (no duplicate), got %d: %v", len(children), children)
	}
	ch, _ := children[0].(map[string]any)
	if ch["id"] != "real-sub-uuid-99" || ch["text"] != "Codebase Researcher" || ch["state"] != "done" {
		t.Fatalf("adopted child = %v, want id=real-sub-uuid-99 text='Codebase Researcher' state=done", ch)
	}
}

func TestActiveFiniteBackgroundWorkNeverExpiresAfterThirtyMinutes(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// Start a turn with two subagents and hold Stop while they run.
	if err := MergeIntoStore("proj", 12, NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "sess-long",
		"prompt":     "Run long multi-agent migration",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}
	cur = base + 1_000
	if err := MergeIntoStore("proj", 12, NormalizeEvent("claude", "SubagentStart", map[string]any{
		"session_id":  "sess-long",
		"agent_id":    "sub-1",
		"agent_type":  "Worker",
		"description": "Migrate backend",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}
	cur = base + 2_000
	if err := MergeIntoStore("proj", 12, NormalizeEvent("claude", "SubagentStart", map[string]any{
		"session_id":  "sess-long",
		"agent_id":    "sub-2",
		"agent_type":  "Worker",
		"description": "Migrate frontend",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}
	cur = base + 3_000
	stopHeld := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "sess-long",
	}, map[string]string{})
	if err := MergeIntoStore("proj", 12, ResolvePaneEvent("proj", 12, stopHeld)); err != nil {
		t.Fatal(err)
	}

	// Advance well past 30 minutes (3 hours of silence): record must NOT be pruned
	// and must remain working while subagents are still active.
	cur = base + 3*60*60_000
	if st := PaneState("proj", 12); st != "working" {
		t.Fatalf("after 3h with active subagents: PaneState = %q, want working", st)
	}
	sum := mustGet(t, SpaceHooksSummary(), "panes", "proj", "12")
	if sum["state"] != "working" || sum["finite_active"] != 2 {
		t.Fatalf("after 3h summary = %v, want state=working finite_active=2", sum)
	}

	// First subagent finishes at +3h10m: waiting_on updates to the remaining subagent,
	// and the pane stays working even after another 2 hours.
	cur = base + 3*60*60_000 + 10*60_000
	if err := MergeIntoStore("proj", 12, NormalizeEvent("claude", "SubagentStop", map[string]any{
		"session_id": "sess-long",
		"agent_id":   "sub-1",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}
	cur = base + 5*60*60_000 + 10*60_000
	sum2 := mustGet(t, SpaceHooksSummary(), "panes", "proj", "12")
	if sum2["state"] != "working" || sum2["finite_active"] != 1 {
		t.Fatalf("after 5h10m with 1 subagent still active: summary = %v", sum2)
	}
	if w := stringKey(sum2["waiting_on"]); strings.Contains(w, "Migrate backend") || !strings.Contains(w, "Migrate frontend") {
		t.Fatalf("waiting_on should reflect only remaining subagent, got %q", w)
	}

	// Second (last) subagent finishes at +5h15m: waiting_on clears because no finite
	// background work remains. Record stays working briefly (WorkingStaleMS) for the
	// parent to wake up, then expires normally if no follow-up parent event arrives.
	cur = base + 5*60*60_000 + 15*60_000
	if err := MergeIntoStore("proj", 12, NormalizeEvent("claude", "SubagentStop", map[string]any{
		"session_id": "sess-long",
		"agent_id":   "sub-2",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}
	sum3 := mustGet(t, SpaceHooksSummary(), "panes", "proj", "12")
	if sum3["finite_active"] != 0 {
		t.Fatalf("after all subagents stopped: finite_active = %v, want 0", sum3["finite_active"])
	}
	if _, hasWait := sum3["waiting_on"]; hasWait {
		t.Fatalf("after all subagents stopped: waiting_on should be cleared, got %v", sum3["waiting_on"])
	}
	cur += WorkingStaleMS + 1
	if st := PaneState("proj", 12); st != "" {
		t.Fatalf("after all subagents stopped and WorkingStaleMS elapsed without parent event: PaneState = %q, want pruned", st)
	}
}

func TestOnlyServicesDoNotBypassWorkingStaleMS(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	bgDev := NormalizeEvent("claude", "PostToolUse", map[string]any{
		"session_id": "sess-svc",
		"tool_name":  "Bash",
		"tool_input": map[string]any{
			"command":           "npm run dev",
			"description":       "Start dev server",
			"run_in_background": true,
		},
		"tool_response": map[string]any{"backgroundTaskId": "bg-dev"},
	}, map[string]string{})
	if err := MergeIntoStore("proj", 13, bgDev); err != nil {
		t.Fatal(err)
	}
	cur = base + WorkingStaleMS + 1
	if st := PaneState("proj", 13); st != "" {
		t.Fatalf("pane with only a service and no finite work should still expire after WorkingStaleMS, got %q", st)
	}
}

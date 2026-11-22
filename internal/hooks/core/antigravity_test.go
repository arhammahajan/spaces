package core

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityRealPayloadsWithoutParentIDOrToolOutput(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	appDir := t.TempDir()
	parentID := "02c144f6-85d2-43de-ba81-acab371b5ce6"
	sub1ID := "752cf8f5-f15d-4bcc-9d0d-0ca6c10bbe61"
	sub2ID := "963cb02b-501e-4a62-9836-cd472c2ba404"

	parentTranscript := filepath.Join(appDir, "brain", parentID, ".system_generated", "logs", "transcript.jsonl")
	sub1Transcript := filepath.Join(appDir, "brain", sub1ID, ".system_generated", "logs", "transcript.jsonl")
	sub2Transcript := filepath.Join(appDir, "brain", sub2ID, ".system_generated", "logs", "transcript.jsonl")

	writeJSONL(t, parentTranscript, []map[string]any{
		{
			"step_index": 0,
			"source":     "USER_EXPLICIT",
			"type":       "USER_INPUT",
			"content":    "<USER_REQUEST>\nPort Crush rendering system\n</USER_REQUEST>",
		},
	})

	// 1. Parent starts and receives prompt.
	ev := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": parentID,
		"transcriptPath": parentTranscript,
		"cwd":            "/home/u/dev",
		"modelName":      "gemini-4-neon-high",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	if ch, _ := ev["child"].(map[string]any); ch != nil {
		t.Fatalf("root SessionStart misclassified as child: %v", ev)
	}
	if err := MergeIntoStore("dev", 50, ev); err != nil {
		t.Fatal(err)
	}

	cur = base + 1_000
	ev = NormalizeEvent("antigravity", "PreInvocation", map[string]any{
		"conversationId": parentID,
		"transcriptPath": parentTranscript,
		"cwd":            "/home/u/dev",
		"modelName":      "gemini-4-neon-high",
		"lastUserInput":  "Port Crush rendering system using subagents",
		"invocationNum":  1,
		"hookEventName":  "PreInvocation",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, ev); err != nil {
		t.Fatal(err)
	}

	// 2. Parent invokes 2 subagents; PostToolUse omits toolOutput, so core
	// reads stepIdx from parent transcript.jsonl to get the real conversationIds.
	writeJSONL(t, parentTranscript, []map[string]any{
		{
			"step_index": 0,
			"source":     "USER_EXPLICIT",
			"type":       "USER_INPUT",
			"content":    "<USER_REQUEST>\nPort Crush rendering system\n</USER_REQUEST>",
		},
		{
			"step_index": 12,
			"source":     "MODEL",
			"type":       "GENERIC",
			"content":    "Created the following subagent:\n{\n  \"conversationId\": \"" + sub1ID + "\",\n  \"logAbsoluteUri\": \"file:///tmp/1\"\n}",
		},
		{
			"step_index": 13,
			"source":     "MODEL",
			"type":       "GENERIC",
			"content":    "Created the following subagent:\n{\n  \"conversationId\": \"" + sub2ID + "\",\n  \"logAbsoluteUri\": \"file:///tmp/2\"\n}",
		},
	})

	cur = base + 2_000
	inv1 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": parentID,
		"transcriptPath": parentTranscript,
		"stepIdx":        12,
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "toolu_1",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "self",
				"Role":     "Chat Virtualized Pipeline Porter",
				"Prompt":   "Port chat list",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, inv1); err != nil {
		t.Fatal(err)
	}

	cur = base + 2_100
	inv2 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": parentID,
		"transcriptPath": parentTranscript,
		"stepIdx":        13,
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "toolu_2",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "research",
				"Role":     "Theme & Markdown v2 Porter",
				"Prompt":   "Port theme",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, inv2); err != nil {
		t.Fatal(err)
	}

	// 3. Subagents fire SessionStart / PreInvocation / PostToolUse WITHOUT parentConversationId.
	// Their transcript step 0 has SYSTEM_MESSAGE with sender=<parentID>.
	writeJSONL(t, sub1Transcript, []map[string]any{
		{
			"step_index": 0,
			"source":     "SYSTEM",
			"type":       "SYSTEM_MESSAGE",
			"content":    "<SYSTEM_MESSAGE>\n[Message] timestamp=2026-11-21T22:37:47Z sender=" + parentID + " priority=MESSAGE_PRIORITY_HIGH content=Port chat list",
		},
	})
	writeJSONL(t, sub2Transcript, []map[string]any{
		{
			"step_index": 0,
			"source":     "SYSTEM",
			"type":       "SYSTEM_MESSAGE",
			"content":    "<SYSTEM_MESSAGE>\n[Message] timestamp=2026-11-21T22:37:47Z sender=" + parentID + " priority=MESSAGE_PRIORITY_HIGH content=Port theme",
		},
	})

	cur = base + 2_500
	sub1Start := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": sub1ID,
		"transcriptPath": sub1Transcript,
		"cwd":            "/home/u/dev",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	if ch, _ := sub1Start["child"].(map[string]any); ch == nil {
		t.Fatalf("sub1 SessionStart must be classified as child via transcript step 0: %v", sub1Start)
	}
	if err := MergeIntoStore("dev", 50, ResolvePaneEvent("dev", 50, sub1Start)); err != nil {
		t.Fatal(err)
	}

	cur = base + 2_600
	sub2Start := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": sub2ID,
		"transcriptPath": sub2Transcript,
		"cwd":            "/home/u/dev",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	if ch, _ := sub2Start["child"].(map[string]any); ch == nil {
		t.Fatalf("sub2 SessionStart must be classified as child via transcript step 0: %v", sub2Start)
	}
	if err := MergeIntoStore("dev", 50, ResolvePaneEvent("dev", 50, sub2Start)); err != nil {
		t.Fatal(err)
	}

	// Verify parent session_id and children were NOT clobbered or wiped!
	rec := mustGet(t, LoadStore(), "panes", "dev", "50")
	if rec["session_id"] != parentID {
		t.Fatalf("parent session_id clobbered: got %v, want %v", rec["session_id"], parentID)
	}

	// 4. Parent fires Stop (protojson omits fullyIdle=false).
	cur = base + 3_000
	parentStop1 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    parentID,
		"transcriptPath":    parentTranscript,
		"executionNum":      1,
		"terminationReason": "NO_TOOL_CALL",
		"hookEventName":     "Stop",
	}, map[string]string{})
	resolvedStop1 := ResolvePaneEvent("dev", 50, parentStop1)
	if err := MergeIntoStore("dev", 50, resolvedStop1); err != nil {
		t.Fatal(err)
	}

	sum := mustGet(t, SpaceHooksSummary(), "panes", "dev", "50")
	if sum["state"] != "working" || sum["finite_active"] != 2 || sum["child_count"] != 2 {
		t.Fatalf("summary while subagents run = %v, want state=working finite_active=2 child_count=2", sum)
	}
	w := stringKey(sum["waiting_on"])
	if w == "waiting on background tasks" || !strings.Contains(w, "Chat Virtualized Pipeline Porter") || !strings.Contains(w, "Theme & Markdown v2 Porter") {
		t.Fatalf("waiting_on = %q, want both subagent roles (not generic background tasks)", w)
	}

	// 5. Subagent 1 finishes with Stop(fullyIdle=true) — must NOT record a top-level outcome!
	cur = base + 10_000
	sub1Stop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    sub1ID,
		"transcriptPath":    sub1Transcript,
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, ResolvePaneEvent("dev", 50, sub1Stop)); err != nil {
		t.Fatal(err)
	}
	if outcomes, _ := LoadStore()["outcomes"].([]any); len(outcomes) != 0 {
		t.Fatalf("subagent Stop recorded bogus top-level outcome: %v", outcomes)
	}
	sum2 := mustGet(t, SpaceHooksSummary(), "panes", "dev", "50")
	if sum2["state"] != "working" || sum2["finite_active"] != 1 {
		t.Fatalf("after sub1Stop summary = %v, want working with finite_active=1", sum2)
	}
	if w2 := stringKey(sum2["waiting_on"]); !strings.Contains(w2, "Theme & Markdown v2 Porter") || strings.Contains(w2, "Chat Virtualized Pipeline Porter") {
		t.Fatalf("waiting_on after sub1Stop = %q, want only sub2", w2)
	}

	// 6. Subagent 2 finishes, then parent finishes with fullyIdle=true.
	cur = base + 15_000
	sub2Stop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    sub2ID,
		"transcriptPath":    sub2Transcript,
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, ResolvePaneEvent("dev", 50, sub2Stop)); err != nil {
		t.Fatal(err)
	}

	cur = base + 16_000
	parentStop2 := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    parentID,
		"transcriptPath":    parentTranscript,
		"executionNum":      2,
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"hookEventName":     "Stop",
	}, map[string]string{})
	if err := MergeIntoStore("dev", 50, ResolvePaneEvent("dev", 50, parentStop2)); err != nil {
		t.Fatal(err)
	}
	if PaneState("dev", 50) != "done" {
		t.Fatalf("final pane state = %q, want done", PaneState("dev", 50))
	}
	outcomes, _ := LoadStore()["outcomes"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("expected exactly 1 parent outcome, got %v", outcomes)
	}
	if out0, _ := outcomes[0].(map[string]any); out0["session_id"] != parentID {
		t.Fatalf("outcome session_id = %v, want %v", out0["session_id"], parentID)
	}
}

func TestAntigravitySubagentDetectionViaSQLiteSummariesBeforeTranscriptExists(t *testing.T) {
	setupRuntime(t)
	appDir := t.TempDir()
	dbPath := filepath.Join(appDir, "conversation_summaries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE conversation_summaries (
		conversation_id TEXT PRIMARY KEY,
		parent_conversation_id TEXT,
		nesting_depth INTEGER,
		agent_name TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO conversation_summaries VALUES ('sub-db-1', 'root-db-1', 1, 'research')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Transcript file does not exist yet (SessionStart race).
	missingTranscript := filepath.Join(appDir, "brain", "sub-db-1", ".system_generated", "logs", "transcript.jsonl")
	ev := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": "sub-db-1",
		"transcriptPath": missingTranscript,
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	child, ok := ev["child"].(map[string]any)
	if !ok || child == nil {
		t.Fatalf("expected child from SQLite conversation_summaries lookup, got %v", ev)
	}
	if child["id"] != "sub-db-1" || child["type"] != "research" {
		t.Fatalf("child = %v, want id=sub-db-1 type=research", child)
	}
}

func TestAntigravityBackgroundCommandFromTranscriptStepIdx(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	appDir := t.TempDir()
	convID := "root-bg-cmd"
	tp := filepath.Join(appDir, "brain", convID, ".system_generated", "logs", "transcript.jsonl")
	writeJSONL(t, tp, []map[string]any{
		{"step_index": 0, "source": "USER_EXPLICIT", "type": "USER_INPUT", "content": "run tests"},
		{"step_index": 5, "source": "MODEL", "type": "GENERIC", "content": "Tool is running as a background task with task id: root-bg-cmd/task-5"},
		{"step_index": 6, "source": "MODEL", "type": "GENERIC", "content": "Command exited with code 0."},
	})

	// Step 5: backgrounded run_command (PostToolUse omits toolOutput).
	ev1 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": convID,
		"transcriptPath": tp,
		"stepIdx":        5,
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-5",
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":       "go test -race ./...",
				"WaitMsBeforeAsync": 500,
				"toolSummary":       "Run race tests",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("work", 3, ev1); err != nil {
		t.Fatal(err)
	}

	// Step 6: synchronous run_command at a different stepIdx must NOT re-extract step 5's task ID.
	cur = base + 1_000
	ev2 := NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": convID,
		"transcriptPath": tp,
		"stepIdx":        6,
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "tc-6",
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":       "git status",
				"WaitMsBeforeAsync": 5000,
				"toolSummary":       "Check git status",
			},
		},
	}, map[string]string{})
	if err := MergeIntoStore("work", 3, ev2); err != nil {
		t.Fatal(err)
	}

	sum := mustGet(t, SpaceHooksSummary(), "panes", "work", "3")
	bg, _ := sum["background_tasks"].([]any)
	if len(bg) != 1 {
		t.Fatalf("expected 1 background task, got %d: %v", len(bg), bg)
	}
	if m, _ := bg[0].(map[string]any); m["id"] != "root-bg-cmd/task-5" || m["text"] != "Run race tests" {
		t.Fatalf("bg task = %v", m)
	}
}

func TestAntigravityPaneStateFallbackAdoptsSyntheticChildWhenDiskUnflushed(t *testing.T) {
	setupRuntime(t)
	base := int64(1700000000000)
	cur := base
	Now = func() int64 { return cur }

	// Parent starts and invokes subagent without any transcriptPath or DB on disk.
	if err := MergeIntoStore("work", 19, NormalizeEvent("antigravity", "PreInvocation", map[string]any{
		"conversationId": "root-race-1",
		"lastUserInput":  "Do parallel work",
		"hookEventName":  "PreInvocation",
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}

	cur = base + 1_000
	if err := MergeIntoStore("work", 19, NormalizeEvent("antigravity", "PostToolUse", map[string]any{
		"conversationId": "root-race-1",
		"hookEventName":  "PostToolUse",
		"toolCall": map[string]any{
			"id":   "call-race-1",
			"name": "invoke_subagent",
			"args": map[string]any{
				"TypeName": "self",
				"Role":     "Race Condition Tester",
			},
		},
	}, map[string]string{})); err != nil {
		t.Fatal(err)
	}

	// Subagent SessionStart arrives with NO parentConversationId, NO transcriptPath, NO DB.
	cur = base + 1_200
	rawSubStart := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": "sub-race-1",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	resolvedSubStart := ResolvePaneEvent("work", 19, rawSubStart)
	if resolvedSubStart["child"] == nil {
		t.Fatalf("ResolvePaneEvent should promote unflushed subagent SessionStart to child: %v", resolvedSubStart)
	}
	if err := MergeIntoStore("work", 19, resolvedSubStart); err != nil {
		t.Fatal(err)
	}

	// Subagent Stop arrives (still no disk metadata) -> matched by known child ID!
	cur = base + 5_000
	rawSubStop := NormalizeEvent("antigravity", "Stop", map[string]any{
		"conversationId":    "sub-race-1",
		"terminationReason": "NO_TOOL_CALL",
		"fullyIdle":         true,
		"hookEventName":     "Stop",
	}, map[string]string{})
	resolvedSubStop := ResolvePaneEvent("work", 19, rawSubStop)
	if resolvedSubStop["child"] == nil {
		t.Fatalf("ResolvePaneEvent should promote known child Stop: %v", resolvedSubStop)
	}
	if err := MergeIntoStore("work", 19, resolvedSubStop); err != nil {
		t.Fatal(err)
	}

	rec := mustGet(t, LoadStore(), "panes", "work", "19")
	if rec["session_id"] != "root-race-1" {
		t.Fatalf("parent session_id clobbered: got %v, want root-race-1", rec["session_id"])
	}
	children, _ := rec["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("expected 1 adopted child, got %v", children)
	}
	ch, _ := children[0].(map[string]any)
	if ch["id"] != "sub-race-1" || ch["text"] != "Race Condition Tester" || ch["state"] != "done" {
		t.Fatalf("adopted child = %v, want id=sub-race-1 text='Race Condition Tester' state=done", ch)
	}
}

func TestAntigravityWorktreeCWDFallback(t *testing.T) {
	setupRuntime(t)
	ev := NormalizeEvent("antigravity", "SessionStart", map[string]any{
		"conversationId": "sub-wt-1",
		"cwd":            "/home/u/.gemini/antigravity-cli/worktrees/f2efc0a0-de54-40e0-aed3-e23e0018d686/subagent-Realistic-10D-PINN-Engineer-self-4bb86977",
		"hookEventName":  "SessionStart",
	}, map[string]string{})
	child, ok := ev["child"].(map[string]any)
	if !ok || child == nil {
		t.Fatalf("expected child from worktree cwd fallback, got %v", ev)
	}
	if child["id"] != "sub-wt-1" || child["text"] != "Realistic 10D PINN Engineer" {
		t.Fatalf("child = %v, want id=sub-wt-1 text='Realistic 10D PINN Engineer'", child)
	}
}

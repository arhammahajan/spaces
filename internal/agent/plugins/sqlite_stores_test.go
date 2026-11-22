package plugins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "modernc.org/sqlite"
)

func createTestSQLiteDB(t *testing.T, dbPath string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dbPath), err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite %s: %v", dbPath, err)
	}
	defer db.Close()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func TestAntigravitySQLiteStore(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	dbPath := filepath.Join(home, ".gemini", "antigravity-cli", "conversation_summaries.db")
	createTestSQLiteDB(t, dbPath,
		`CREATE TABLE conversation_summaries (
			conversation_id TEXT PRIMARY KEY,
			title TEXT,
			preview TEXT,
			step_count INTEGER,
			last_modified_time TEXT,
			workspace_uris TEXT,
			parent_conversation_id TEXT
		)`,
		`INSERT INTO conversation_summaries VALUES (
			'conv-main-1',
			'Refactor SQLite conversation loader',
			'Please wire up native SQLite readers',
			42,
			'2026-11-22 14:30:00.123456789+00:00',
			'["file:///home/dev/spaces","file:///home/dev/other"]',
			''
		)`,
		`INSERT INTO conversation_summaries VALUES (
			'conv-preview-only',
			'',
			'Fallback title from preview text',
			7,
			'2026-11-21T09:15:00Z',
			'["https://example.com","file:///home/dev/app"]',
			NULL
		)`,
		`INSERT INTO conversation_summaries VALUES (
			'conv-subagent-1',
			'Subagent worker task',
			'Internal subagent prompt',
			12,
			'2026-11-22 15:00:00+00:00',
			'["file:///home/dev/spaces"]',
			'conv-main-1'
		)`,
	)

	// Create matching .db for conv-main-1 and an untitled orphan/subagent .db that should be skipped.
	mainConvDB := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", "conv-main-1.db")
	subagentConvDB := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", "conv-subagent-1.db")
	orphanConvDB := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", "conv-orphan-empty.db")
	writeFixture(t, mainConvDB, "SQLite format 3\x00dummy")
	writeFixture(t, subagentConvDB, "SQLite format 3\x00dummy")
	writeFixture(t, orphanConvDB, "SQLite format 3\x00dummy")

	store := NewAntigravityConversationStore(home)
	list, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(List) = %d, want 2 (subagent and orphan .db skipped): %+v", len(list), list)
	}

	if list[0].SessionID != "conv-main-1" {
		t.Fatalf("list[0].SessionID = %q, want conv-main-1", list[0].SessionID)
	}
	if list[0].Title != "Refactor SQLite conversation loader" {
		t.Errorf("list[0].Title = %q", list[0].Title)
	}
	if list[0].CWD != "/home/dev/spaces" {
		t.Errorf("list[0].CWD = %q, want /home/dev/spaces", list[0].CWD)
	}
	if list[0].MessageCount != 42 {
		t.Errorf("list[0].MessageCount = %d, want 42", list[0].MessageCount)
	}
	if list[0].SourcePath != mainConvDB {
		t.Errorf("list[0].SourcePath = %q, want %q", list[0].SourcePath, mainConvDB)
	}
	wantTs := time.Date(2026, 11, 22, 14, 30, 0, 123456789, time.UTC).UnixMilli()
	if list[0].UpdatedAtMs != wantTs || list[0].CreatedAtMs != wantTs {
		t.Errorf("list[0] timestamps = (%d, %d), want %d", list[0].CreatedAtMs, list[0].UpdatedAtMs, wantTs)
	}

	if list[1].SessionID != "conv-preview-only" {
		t.Fatalf("list[1].SessionID = %q, want conv-preview-only", list[1].SessionID)
	}
	if list[1].Title != "Fallback title from preview text" {
		t.Errorf("list[1].Title = %q", list[1].Title)
	}
	if list[1].CWD != "/home/dev/app" {
		t.Errorf("list[1].CWD = %q, want /home/dev/app", list[1].CWD)
	}
	if list[1].SourcePath != dbPath+"#conv-preview-only" {
		t.Errorf("list[1].SourcePath = %q, want %q", list[1].SourcePath, dbPath+"#conv-preview-only")
	}
}

func TestOpenCodeSQLiteStore(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	createTestSQLiteDB(t, dbPath,
		`CREATE TABLE session_v2 (
			id TEXT PRIMARY KEY,
			parent_id TEXT,
			directory TEXT,
			title TEXT,
			model TEXT,
			time_created INTEGER,
			time_updated INTEGER,
			time_archived INTEGER
		)`,
		`INSERT INTO session_v2 VALUES (
			'oc-active-1',
			NULL,
			'/work/spaces',
			'Fix tmux pane flicker',
			'{"provider":"anthropic","id":"claude-sonnet-4-5"}',
			1763700000000,
			1763700500000,
			0
		)`,
		`INSERT INTO session_v2 VALUES (
			'oc-archived-1',
			'',
			'/work/spaces',
			'Old archived session',
			'gpt-5.4',
			1763600000000,
			1763600500000,
			1763600900000
		)`,
		`INSERT INTO session_v2 VALUES (
			'oc-child-1',
			'oc-active-1',
			'/work/spaces',
			'Subagent child session',
			'{"id":"claude-haiku-4-5"}',
			1763700100000,
			1763700200000,
			0
		)`,
	)

	store := NewOpenCodeConversationStore(home)

	active, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("len(active) = %d, want 1: %+v", len(active), active)
	}
	if active[0].SessionID != "oc-active-1" ||
		active[0].Title != "Fix tmux pane flicker" ||
		active[0].Model != "claude-sonnet-4-5" ||
		active[0].CWD != "/work/spaces" ||
		active[0].Archived ||
		active[0].SourcePath != dbPath+"#oc-active-1" {
		t.Errorf("unexpected opencode active record: %+v", active[0])
	}

	all, err := store.List(ctx, agent.ConversationListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("List(IncludeArchived): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2 (child session skipped): %+v", len(all), all)
	}
	if all[1].SessionID != "oc-archived-1" || !all[1].Archived || all[1].Model != "gpt-5.4" {
		t.Errorf("unexpected opencode archived record: %+v", all[1])
	}
}

func TestKiloSQLiteStore(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	dbPath := filepath.Join(home, ".local", "share", "kilo", "kilo.db")
	createTestSQLiteDB(t, dbPath,
		`CREATE TABLE session (
			id TEXT PRIMARY KEY,
			parent_id TEXT,
			directory TEXT,
			title TEXT,
			model TEXT,
			time_created INTEGER,
			time_updated INTEGER,
			time_archived INTEGER
		)`,
		`INSERT INTO session VALUES (
			'kilo-1',
			'',
			'/work/kilo-app',
			'Implement keyboard shortcuts',
			'{"id":"claude-opus-4-5"}',
			1763710000000,
			1763710900000,
			0
		)`,
		`INSERT INTO session VALUES (
			'kilo-child',
			'kilo-1',
			'/work/kilo-app',
			'Child sub-task',
			'claude-haiku-4-5',
			1763710100000,
			1763710200000,
			0
		)`,
		`INSERT INTO session VALUES (
			'kilo-arch',
			NULL,
			'/work/kilo-app',
			'Archived kilo task',
			'gpt-5',
			1763610000000,
			1763610500000,
			1763610600000
		)`,
	)

	store := NewKiloConversationStore(home)

	active, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("len(active) = %d, want 1: %+v", len(active), active)
	}
	if active[0].SessionID != "kilo-1" ||
		active[0].Title != "Implement keyboard shortcuts" ||
		active[0].Model != "claude-opus-4-5" ||
		active[0].CWD != "/work/kilo-app" ||
		active[0].Agent != "kilo" ||
		active[0].SourcePath != dbPath+"#kilo-1" {
		t.Errorf("unexpected kilo active record: %+v", active[0])
	}

	all, err := store.List(ctx, agent.ConversationListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("List(IncludeArchived): %v", err)
	}
	if len(all) != 2 || !all[1].Archived {
		t.Fatalf("expected 2 kilo records with second archived, got %+v", all)
	}
}

func TestLargeJSONLBoundedScanAndBoilerplateFiltering(t *testing.T) {
	ctx := context.Background()

	t.Run("claude large jsonl head+tail scan with meta boilerplate and custom-title", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".claude", "projects", "-home-dev-big", "sess-big-claude.jsonl")

		var sb strings.Builder
		// Head: meta/boilerplate messages followed by real first user message
		sb.WriteString(`{"type":"user","isMeta":true,"sessionId":"sess-big-claude","cwd":"/home/dev/big","timestamp":"2026-11-20T10:00:00Z","message":{"role":"user","content":"<local-command-caveat>Caveat: the messages below were generated locally</local-command-caveat>"}}` + "\n")
		sb.WriteString(`{"type":"user","sessionId":"sess-big-claude","cwd":"/home/dev/big","timestamp":"2026-11-20T10:00:01Z","message":{"role":"user","content":"<command-name>/clear</command-name>"}}` + "\n")
		sb.WriteString(`{"type":"user","sessionId":"sess-big-claude","cwd":"/home/dev/big","timestamp":"2026-11-20T10:00:02Z","message":{"role":"user","content":"<ide_opened_file>Opened main.go</ide_opened_file>"}}` + "\n")
		sb.WriteString(`{"type":"user","sessionId":"sess-big-claude","cwd":"/home/dev/big","timestamp":"2026-11-20T10:00:05Z","message":{"role":"user","content":"Optimize the conversation scanner latency"}}` + "\n")
		sb.WriteString(`{"type":"assistant","sessionId":"sess-big-claude","cwd":"/home/dev/big","timestamp":"2026-11-20T10:00:10Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":"Analyzing scanner hot path."}}` + "\n")

		// Middle filler (> 400 KB) that should be skipped by bounded head+tail scan
		fillerPayload := strings.Repeat("x", 4096)
		for i := 0; i < 110; i++ {
			sb.WriteString(`{"type":"assistant","sessionId":"sess-big-claude","timestamp":"2026-11-20T10:05:00Z","message":{"role":"assistant","content":"MIDDLE_FILLER_TOKEN_` + fillerPayload + `"}}` + "\n")
		}

		// Tail: final assistant message + custom-title rename
		sb.WriteString(`{"type":"assistant","sessionId":"sess-big-claude","timestamp":"2026-11-20T11:00:00Z","message":{"role":"assistant","content":"Bounded scanner reduced cold scan by 20x."}}` + "\n")
		sb.WriteString(`{"type":"custom-title","customTitle":"Fast bounded JSONL scanner","sessionId":"sess-big-claude","timestamp":"2026-11-20T11:00:05Z"}` + "\n")

		content := sb.String()
		if len(content) <= 300*1024 {
			t.Fatalf("fixture size %d must be > 300 KB", len(content))
		}
		writeFixture(t, path, content)

		store := NewClaudeConversationStore(home)
		list, err := store.List(ctx, agent.ConversationListOptions{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("len(List) = %d, want 1", len(list))
		}
		rec := list[0]
		if rec.SessionID != "sess-big-claude" {
			t.Errorf("SessionID = %q, want sess-big-claude", rec.SessionID)
		}
		if rec.CWD != "/home/dev/big" {
			t.Errorf("CWD = %q, want /home/dev/big", rec.CWD)
		}
		if rec.Model != "claude-sonnet-4-5" {
			t.Errorf("Model = %q, want claude-sonnet-4-5", rec.Model)
		}
		if rec.FirstMessage != "Optimize the conversation scanner latency" {
			t.Errorf("FirstMessage = %q, want real user message (not boilerplate)", rec.FirstMessage)
		}
		if rec.Title != "Fast bounded JSONL scanner" {
			t.Errorf("Title = %q, want custom-title", rec.Title)
		}
		if rec.LastMessage != "Bounded scanner reduced cold scan by 20x." {
			t.Errorf("LastMessage = %q", rec.LastMessage)
		}
		wantCreated := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC).UnixMilli()
		wantUpdated := time.Date(2026, 11, 20, 11, 0, 5, 0, time.UTC).UnixMilli()
		if rec.CreatedAtMs != wantCreated {
			t.Errorf("CreatedAtMs = %d, want %d", rec.CreatedAtMs, wantCreated)
		}
		if rec.UpdatedAtMs != wantUpdated {
			t.Errorf("UpdatedAtMs = %d, want %d", rec.UpdatedAtMs, wantUpdated)
		}
		// Bounded scan must not have counted all 110 middle filler lines.
		if rec.MessageCount >= 110 {
			t.Errorf("MessageCount = %d, expected bounded scan to skip middle filler", rec.MessageCount)
		}
	})

	t.Run("codex boilerplate filtering skips environment_context and AGENTS.md", func(t *testing.T) {
		home := t.TempDir()
		rollout := filepath.Join(home, ".codex", "sessions", "2026", "11", "22", "rollout-2026-11-22T12-00-00-cx-bp.jsonl")

		var sb strings.Builder
		sb.WriteString(`{"timestamp":"2026-11-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-bp-1","cwd":"/repo/codex","model":"gpt-5.4"}}` + "\n")
		sb.WriteString(`{"timestamp":"2026-11-22T12:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo/codex</cwd>\n</environment_context>"}]}}` + "\n")
		sb.WriteString(`{"timestamp":"2026-11-22T12:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /repo/codex"}]}}` + "\n")
		sb.WriteString(`{"timestamp":"2026-11-22T12:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<permissions instructions>sandbox rules</permissions instructions>"}]}}` + "\n")
		sb.WriteString(`{"timestamp":"2026-11-22T12:00:04Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Add SQLite readers for OpenCode and Kilo"}]}}` + "\n")

		// Add > 350 KB filler to also exercise bounded scan on Codex rollouts
		filler := strings.Repeat("y", 4096)
		for i := 0; i < 95; i++ {
			sb.WriteString(`{"timestamp":"2026-11-22T12:05:00Z","type":"event_msg","payload":{"type":"agent_message","message":"` + filler + `"}}` + "\n")
		}
		sb.WriteString(`{"timestamp":"2026-11-22T12:30:00Z","type":"event_msg","payload":{"type":"agent_message","message":"Finished implementing SQLite readers."}}` + "\n")

		writeFixture(t, rollout, sb.String())

		store := NewCodexConversationStore(home)
		list, err := store.List(ctx, agent.ConversationListOptions{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("len(List) = %d, want 1", len(list))
		}
		rec := list[0]
		if rec.SessionID != "codex-bp-1" {
			t.Errorf("SessionID = %q, want codex-bp-1", rec.SessionID)
		}
		if rec.FirstMessage != "Add SQLite readers for OpenCode and Kilo" {
			t.Errorf("FirstMessage = %q, want real user message", rec.FirstMessage)
		}
		if rec.Title != "Add SQLite readers for OpenCode and Kilo" {
			t.Errorf("Title = %q, want derived title from real user message", rec.Title)
		}
		if rec.LastMessage != "Finished implementing SQLite readers." {
			t.Errorf("LastMessage = %q", rec.LastMessage)
		}
		wantUpdated := time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC).UnixMilli()
		if rec.UpdatedAtMs != wantUpdated {
			t.Errorf("UpdatedAtMs = %d, want %d", rec.UpdatedAtMs, wantUpdated)
		}
	})
}

func TestSQLiteFullMessageParseLogAndSearch(t *testing.T) {
	ctx := context.Background()

	// 1. Antigravity conversations_fts
	agyHome := t.TempDir()
	agyDir := filepath.Join(agyHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(agyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agyDBPath := filepath.Join(agyDir, "conversation_summaries.db")
	db, err := sql.Open("sqlite", agyDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE conversation_summaries (
			conversation_id TEXT, title TEXT, preview TEXT, step_count INTEGER,
			last_modified_time TEXT, workspace_uris TEXT, parent_conversation_id TEXT
		);
		CREATE TABLE conversations_fts (
			conversation_id TEXT, chunk_index INTEGER, last_modified_time INTEGER,
			user_body TEXT, agent_body TEXT
		);
		INSERT INTO conversation_summaries VALUES ('agy-fts-1', 'SQLite Search', 'start', 4, '2026-11-22 10:00:00+00:00', '["file:///repo"]', '');
		INSERT INTO conversations_fts VALUES ('agy-fts-1', 0, 1795255200000, 'Please optimize quantum_fluctuation_index', 'Fixed quantum_fluctuation_index in store');
	`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	agyStore := NewAntigravityConversationStore(agyHome)
	hits, err := agyStore.Search(ctx, "quantum_fluctuation_index", agent.ConversationListOptions{})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "agy-fts-1" {
		t.Fatalf("antigravity SQLite body search = (%+v, %v), want agy-fts-1", hits, err)
	}

	// 2. OpenCode session_message
	ocHome := t.TempDir()
	ocDir := filepath.Join(ocHome, ".local", "share", "opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ocDBPath := filepath.Join(ocDir, "opencode.db")
	db, err = sql.Open("sqlite", ocDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE session_v2 (
			id TEXT, parent_id TEXT, directory TEXT, title TEXT, model TEXT,
			time_created INTEGER, time_updated INTEGER, time_archived INTEGER
		);
		CREATE TABLE session_message (
			id TEXT, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, data TEXT
		);
		INSERT INTO session_v2 VALUES ('ses-oc-1', '', '/repo', 'OpenCode Chat', '{"id":"claude"}', 1795255200000, 1795255205000, 0);
		INSERT INTO session_message VALUES ('m1', 'ses-oc-1', 'user', 1, 1795255201000, '{"text":"Check opencode_deep_token_42"}');
	`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ocStore := NewOpenCodeConversationStore(ocHome)
	hits, err = ocStore.Search(ctx, "opencode_deep_token_42", agent.ConversationListOptions{})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "ses-oc-1" {
		t.Fatalf("opencode SQLite body search = (%+v, %v), want ses-oc-1", hits, err)
	}

	// 3. Kilo message + part
	kiloHome := t.TempDir()
	kiloDir := filepath.Join(kiloHome, ".local", "share", "kilo")
	if err := os.MkdirAll(kiloDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kiloDBPath := filepath.Join(kiloDir, "kilo.db")
	db, err = sql.Open("sqlite", kiloDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE session (
			id TEXT, parent_id TEXT, directory TEXT, title TEXT, model TEXT,
			time_created INTEGER, time_updated INTEGER, time_archived INTEGER
		);
		CREATE TABLE message (
			id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT
		);
		CREATE TABLE part (
			id TEXT, message_id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT
		);
		INSERT INTO session VALUES ('ses-kilo-1', '', '/repo', 'Kilo Chat', 'deepseek', 1795255200000, 1795255205000, 0);
		INSERT INTO message VALUES ('km1', 'ses-kilo-1', 1795255201000, 1795255201000, '{"role":"user","model":{"modelID":"deepseek"}}');
		INSERT INTO part VALUES ('kp1', 'km1', 'ses-kilo-1', 1795255201000, 1795255201000, '{"type":"text","text":"Debug kilo_unique_needle_99"}');
	`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	kiloStore := NewKiloConversationStore(kiloHome)
	hits, err = kiloStore.Search(ctx, "kilo_unique_needle_99", agent.ConversationListOptions{})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "ses-kilo-1" {
		t.Fatalf("kilo SQLite body search = (%+v, %v), want ses-kilo-1", hits, err)
	}
}

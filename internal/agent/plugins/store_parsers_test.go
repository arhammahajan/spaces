package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRegistryAllNineProvidersHaveStoreAndParser(t *testing.T) {
	expected := map[string]bool{
		"antigravity":  false,
		"claude":       false,
		"codex":        false,
		"cursor-agent": false,
		"deepseek":     false,
		"kilo":         false,
		"kimi":         false,
		"muse":         false,
		"opencode":     false,
	}

	for _, p := range All() {
		id := p.Spec().ID
		if _, want := expected[id]; !want {
			continue
		}
		expected[id] = true
		if p.ConversationStore() == nil {
			t.Errorf("plugin %q has nil ConversationStore()", id)
		}
		if p.LogParser() == nil {
			t.Errorf("plugin %q has nil LogParser()", id)
		}
	}

	for id, seen := range expected {
		if !seen {
			t.Errorf("expected provider plugin %q not found in All()", id)
		}
	}

	// Verify AttachProviderTelemetry does not overwrite existing custom implementations.
	customStore := NewClaudeConversationStore(t.TempDir())
	customParser := NewClaudeLogParser()
	bp := &agent.BasePlugin{
		PluginSpec:              agent.Spec{ID: "claude"},
		CustomConversationStore: customStore,
		CustomLogParser:         customParser,
	}
	AttachProviderTelemetry(bp)
	if bp.ConversationStore() != customStore {
		t.Errorf("AttachProviderTelemetry overwrote existing CustomConversationStore")
	}
	if bp.LogParser() != customParser {
		t.Errorf("AttachProviderTelemetry overwrote existing CustomLogParser")
	}
}

func TestClaudeStoreAndLogParser(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	projDir := filepath.Join(home, ".claude", "projects", "-home-dev-app")
	s1Path := filepath.Join(projDir, "sess-claude-1.jsonl")
	s2Path := filepath.Join(projDir, "sess-claude-2.jsonl")
	archivedPath := filepath.Join(projDir, "archived", "sess-claude-old.jsonl")

	writeFixture(t, s1Path, ""+
		`{"type":"summary","summary":"Fix OAuth token refresh","sessionId":"sess-claude-1","cwd":"/home/dev/app","timestamp":"2026-11-20T10:00:00Z"}`+"\n"+
		`not-valid-json`+"\n"+
		`{"type":"user","sessionId":"sess-claude-1","cwd":"/home/dev/app","timestamp":"2026-11-20T10:00:05Z","message":{"role":"user","content":"Please fix the OAuth refresh race"}}`+"\n"+
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/home/dev/app","timestamp":"2026-11-20T10:00:10Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Inspecting auth.go now."},{"type":"tool_use","name":"Read","input":{"path":"auth.go"}}]}}`+"\n"+
		`{"type":"user","sessionId":"sess-claude-1","cwd":"/home/dev/app","timestamp":"2026-11-20T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","content":"file contents here"}]}}`+"\n"+
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/home/dev/app","timestamp":"2026-11-20T10:00:20Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Added mutex guard around token refresh."}]}}`+"\n",
	)
	writeFixture(t, s2Path, ""+
		`{"type":"user","sessionId":"sess-claude-2","cwd":"/home/dev/other","timestamp":"2026-11-21T12:00:00Z","message":{"role":"user","content":"Add dark mode toggle"}}`+"\n"+
		`{"type":"assistant","sessionId":"sess-claude-2","cwd":"/home/dev/other","timestamp":"2026-11-21T12:05:00Z","message":{"role":"assistant","model":"claude-opus-4-1","content":"Implemented dark mode toggle in theme.ts."}}`+"\n",
	)
	writeFixture(t, archivedPath, ""+
		`{"type":"user","sessionId":"sess-claude-old","cwd":"/home/dev/app","archived":true,"timestamp":"2026-11-19T08:00:00Z","message":{"role":"user","content":"Archived migration task"}}`+"\n",
	)

	store := NewClaudeConversationStore(home)

	// List default (excludes archived, newest-first)
	list, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(List) = %d, want 2: %+v", len(list), list)
	}
	if list[0].SessionID != "sess-claude-2" || list[1].SessionID != "sess-claude-1" {
		t.Errorf("sort order = [%s, %s], want [sess-claude-2, sess-claude-1]", list[0].SessionID, list[1].SessionID)
	}
	if list[1].Title != "Fix OAuth token refresh" || list[1].Model != "claude-sonnet-4-5" {
		t.Errorf("unexpected metadata on sess-claude-1: %+v", list[1])
	}

	// CWD filter + IncludeArchived
	appList, err := store.List(ctx, agent.ConversationListOptions{CWD: "/home/dev/app", IncludeArchived: true})
	if err != nil {
		t.Fatalf("List CWD+Archived: %v", err)
	}
	if len(appList) != 2 {
		t.Fatalf("len(appList) = %d, want 2", len(appList))
	}
	if !appList[1].Archived {
		t.Errorf("expected second record to be archived: %+v", appList[1])
	}

	// Limit
	limited, err := store.List(ctx, agent.ConversationListOptions{Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].SessionID != "sess-claude-2" {
		t.Fatalf("Limit=1 got (%+v, %v), want [sess-claude-2]", limited, err)
	}

	// Search metadata & deep transcript (tool name "Read" / "Inspecting auth.go")
	hits, err := store.Search(ctx, "inspecting auth.go", agent.ConversationListOptions{})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "sess-claude-1" {
		t.Fatalf("Search transcript got (%+v, %v), want [sess-claude-1]", hits, err)
	}
	modelHits, err := store.Search(ctx, "OPUS-4-1", agent.ConversationListOptions{})
	if err != nil || len(modelHits) != 1 || modelHits[0].SessionID != "sess-claude-2" {
		t.Fatalf("Search model got (%+v, %v), want [sess-claude-2]", modelHits, err)
	}

	// LogParser verification (malformed line ignored, tool_use & tool_result parsed)
	entries, err := NewClaudeLogParser().ParseLog(ctx, s1Path)
	if err != nil {
		t.Fatalf("ParseLog: %v", err)
	}
	var sawToolUse, sawToolResult, sawSummary bool
	for _, e := range entries {
		if e.Event == "tool_use" && e.ToolName == "Read" {
			sawToolUse = true
		}
		if e.Event == "tool_result" && e.Role == "tool" {
			sawToolResult = true
		}
		if e.Event == "summary" && e.Text == "Fix OAuth token refresh" {
			sawSummary = true
		}
	}
	if !sawToolUse || !sawToolResult || !sawSummary {
		t.Errorf("missing expected claude log entries (toolUse=%v, toolResult=%v, summary=%v): %+v", sawToolUse, sawToolResult, sawSummary, entries)
	}
}

func TestCodexStoreAndLogParser(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	rollout1 := filepath.Join(home, ".codex", "sessions", "2026", "11", "21", "rollout-2026-11-21T10-00-00-cx1.jsonl")
	rollout2 := filepath.Join(home, ".codex", "sessions", "2026", "11", "22", "rollout-2026-11-22T11-00-00-cx2.jsonl")
	archived := filepath.Join(home, ".codex", "archived_sessions", "2026", "11", "19", "rollout-2026-11-19T09-00-00-cx0.jsonl")

	writeFixture(t, rollout1, ""+
		`{"timestamp":"2026-11-21T10:00:00Z","type":"session_meta","payload":{"id":"codex-sess-1","cwd":"/repo/alpha","model":"gpt-5.3-codex"}}`+"\n"+
		`{corrupt json`+"\n"+
		`{"timestamp":"2026-11-21T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Refactor parser pipeline"}]}}`+"\n"+
		`{"timestamp":"2026-11-21T10:00:05Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"go test ./...\"}"}}`+"\n"+
		`{"timestamp":"2026-11-21T10:00:07Z","type":"response_item","payload":{"type":"function_call_output","output":"PASS"}}`+"\n"+
		`{"timestamp":"2026-11-21T10:00:10Z","type":"event_msg","payload":{"type":"agent_message","message":"All parser tests pass."}}`+"\n",
	)
	writeFixture(t, rollout2, ""+
		`{"timestamp":"2026-11-22T11:00:00Z","type":"session_meta","payload":{"id":"codex-sess-2","cwd":"/repo/beta","model":"gpt-5.4"}}`+"\n"+
		`{"timestamp":"2026-11-22T11:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"Benchmark websocket framing"}}`+"\n",
	)
	writeFixture(t, archived, ""+
		`{"timestamp":"2026-11-19T09:00:00Z","type":"session_meta","payload":{"id":"codex-sess-old","cwd":"/repo/alpha"}}`+"\n"+
		`{"timestamp":"2026-11-19T09:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Old archived task"}}`+"\n",
	)

	store := NewCodexConversationStore(home)
	list, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].SessionID != "codex-sess-2" || list[1].SessionID != "codex-sess-1" {
		t.Fatalf("unexpected codex list: %+v", list)
	}

	withArch, err := store.List(ctx, agent.ConversationListOptions{CWD: "/repo/alpha", IncludeArchived: true})
	if err != nil || len(withArch) != 2 {
		t.Fatalf("expected 2 alpha sessions with archived, got (%+v, %v)", withArch, err)
	}

	hits, err := store.Search(ctx, "shell", agent.ConversationListOptions{})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "codex-sess-1" {
		t.Fatalf("Search tool call got (%+v, %v), want [codex-sess-1]", hits, err)
	}

	entries, err := NewCodexLogParser().ParseLog(ctx, rollout1)
	if err != nil {
		t.Fatalf("ParseLog: %v", err)
	}
	var sawFuncCall, sawOutput, sawAgentMsg bool
	for _, e := range entries {
		if e.SessionID != "codex-sess-1" {
			t.Errorf("expected propagated SessionID codex-sess-1, got %q", e.SessionID)
		}
		if e.Event == "function_call" && e.ToolName == "shell" {
			sawFuncCall = true
		}
		if e.Event == "function_call_output" && e.Text == "PASS" {
			sawOutput = true
		}
		if e.Event == "agent_message" && e.Role == "assistant" {
			sawAgentMsg = true
		}
	}
	if !sawFuncCall || !sawOutput || !sawAgentMsg {
		t.Errorf("missing codex entries (funcCall=%v, output=%v, agentMsg=%v): %+v", sawFuncCall, sawOutput, sawAgentMsg, entries)
	}
}

func TestAntigravityStoreAndLogParser(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	// 1. DB-only conversation (no JSON/JSONL metadata)
	dbOnly := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", "agy-db-only.db")
	writeFixture(t, dbOnly, "SQLite format 3\x00dummy")
	dbTime := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	if err := os.Chtimes(dbOnly, dbTime, dbTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// 2. DB + brain transcript pair (should merge into single richer record)
	dbPaired := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", "agy-paired.db")
	writeFixture(t, dbPaired, "SQLite format 3\x00dummy")
	transcriptPath := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "agy-paired", ".system_generated", "logs", "transcript.jsonl")
	writeFixture(t, transcriptPath, ""+
		`{"timestamp":"2026-11-22T09:00:00Z","session_id":"agy-paired","cwd":"/work/spaces","model":"gemini-3-pro","role":"user","event":"message","text":"Build plugin telemetry"}`+"\n"+
		`{"timestamp":"2026-11-22T09:00:05Z","session_id":"agy-paired","role":"assistant","event":"tool_call","tool_name":"write_file","text":"store_parsers.go"}`+"\n"+
		`{"timestamp":"2026-11-22T09:00:10Z","session_id":"agy-paired","role":"assistant","event":"message","text":"Implemented stores and parsers."}`+"\n",
	)

	store := NewAntigravityConversationStore(home)
	list, err := store.List(ctx, agent.ConversationListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 deduplicated antigravity sessions, got %d: %+v", len(list), list)
	}
	if list[0].SessionID != "agy-paired" || list[0].CWD != "/work/spaces" || list[0].Model != "gemini-3-pro" {
		t.Errorf("expected merged paired record first, got %+v", list[0])
	}
	if list[1].SessionID != "agy-db-only" || list[1].UpdatedAtMs != dbTime.UnixMilli() {
		t.Errorf("expected db-only fallback second with mtime %d, got %+v", dbTime.UnixMilli(), list[1])
	}

	// Search by SessionID finds db-only; search by tool name finds paired
	dbHits, err := store.Search(ctx, "agy-db-only", agent.ConversationListOptions{})
	if err != nil || len(dbHits) != 1 {
		t.Fatalf("Search db-only got (%+v, %v)", dbHits, err)
	}
	toolHits, err := store.Search(ctx, "write_file", agent.ConversationListOptions{})
	if err != nil || len(toolHits) != 1 || toolHits[0].SessionID != "agy-paired" {
		t.Fatalf("Search tool got (%+v, %v)", toolHits, err)
	}

	// ParseLog on .db returns nil, nil gracefully
	parser := NewAntigravityLogParser()
	if dbEntries, err := parser.ParseLog(ctx, dbOnly); err != nil || len(dbEntries) != 0 {
		t.Errorf("ParseLog(.db) = (%+v, %v), want (nil, nil)", dbEntries, err)
	}
	entries, err := parser.ParseLog(ctx, transcriptPath)
	if err != nil || len(entries) != 3 {
		t.Fatalf("ParseLog(transcript.jsonl) = (%d entries, %v), want 3", len(entries), err)
	}
}

func TestRemainingSixProvidersStoresAndParsers(t *testing.T) {
	ctx := context.Background()

	type providerCase struct {
		name         string
		newStore     func(string) agent.ConversationStore
		newParser    func() agent.LogParser
		activePath   string
		activeBody   string
		archivedPath string
		archivedBody string
		wantSession  string
		wantCWD      string
		wantModel    string
		wantTool     string
		searchQuery  string
	}

	cases := []providerCase{
		{
			name:         "opencode",
			newStore:     NewOpenCodeConversationStore,
			newParser:    NewOpenCodeLogParser,
			activePath:   ".local/share/opencode/storage/session/proj1/ses_oc_1.json",
			activeBody:   `{"id":"ses_oc_1","title":"OpenCode plugin audit","directory":"/work/oc","model":"claude-sonnet-4","time":{"created":1763700000000,"updated":1763700100000},"messages":[{"role":"user","text":"Audit plugin registry","timestamp":1763700001000},{"role":"assistant","tool_name":"grep","text":"searching registry","timestamp":1763700050000}]}`,
			archivedPath: ".opencode/sessions/ses_oc_arch.json",
			archivedBody: `{"id":"ses_oc_arch","title":"Old OC","directory":"/work/oc","time":{"created":1763600000000,"updated":1763600100000,"archived":1763600200000}}`,
			wantSession:  "ses_oc_1",
			wantCWD:      "/work/oc",
			wantModel:    "claude-sonnet-4",
			wantTool:     "grep",
			searchQuery:  "plugin audit",
		},
		{
			name:         "kilo",
			newStore:     NewKiloConversationStore,
			newParser:    NewKiloLogParser,
			activePath:   ".local/share/kilo/storage/session/ses_kilo_1.json",
			activeBody:   `{"id":"ses_kilo_1","title":"Kilo TUI polish","directory":"/work/kilo","model":"gpt-5","time":{"created":1763710000000,"updated":1763710200000},"messages":[{"role":"user","content":"Polish tab bar"},{"role":"assistant","content":[{"type":"tool_use","name":"edit_file","input":{"file":"ui.go"}},{"type":"text","text":"Updated tab bar."}]}]}`,
			archivedPath: ".config/kilo/sessions/ses_kilo_arch.jsonl",
			archivedBody: `{"session_id":"ses_kilo_arch","cwd":"/work/kilo","archived":true,"role":"user","text":"Archived kilo"}` + "\n",
			wantSession:  "ses_kilo_1",
			wantCWD:      "/work/kilo",
			wantModel:    "gpt-5",
			wantTool:     "edit_file",
			searchQuery:  "tab bar",
		},
		{
			name:         "kimi",
			newStore:     NewKimiConversationStore,
			newParser:    NewKimiLogParser,
			activePath:   ".kimi/sessions/kimi-1.jsonl",
			activeBody:   `{"session_id":"kimi-1","work_dir":"/work/kimi","model":"kimi-k2","timestamp":"2026-11-22T08:00:00Z","role":"user","content":"Optimize SQL query"}` + "\n" + `bad-line` + "\n" + `{"session_id":"kimi-1","timestamp":"2026-11-22T08:00:10Z","role":"assistant","content":"Checking indexes","tool_calls":[{"function":{"name":"run_sql","arguments":"EXPLAIN SELECT 1"}}]}` + "\n",
			archivedPath: ".kimi-code/sessions/kimi-old.json",
			archivedBody: `{"id":"kimi-old","cwd":"/work/kimi","archived":true,"title":"Old Kimi"}`,
			wantSession:  "kimi-1",
			wantCWD:      "/work/kimi",
			wantModel:    "kimi-k2",
			wantTool:     "run_sql",
			searchQuery:  "explain select",
		},
		{
			name:         "muse",
			newStore:     NewMuseConversationStore,
			newParser:    NewMuseLogParser,
			activePath:   ".config/muse/sessions/muse-1.jsonl",
			activeBody:   `{"session_id":"muse-1","cwd":"/work/muse","model":"claude-sonnet-4-5","timestamp":1763720000,"role":"user","text":"Fix auth middleware"}` + "\n" + `{"session_id":"muse-1","timestamp":1763720020,"role":"assistant","tool_name":"bash","text":"go test ./auth"}` + "\n",
			archivedPath: ".local/share/muse/sessions/muse-old.json",
			archivedBody: `{"id":"muse-old","cwd":"/work/muse","archived":true,"title":"Old Muse"}`,
			wantSession:  "muse-1",
			wantCWD:      "/work/muse",
			wantModel:    "claude-sonnet-4-5",
			wantTool:     "bash",
			searchQuery:  "middleware",
		},
		{
			name:         "cursor-agent",
			newStore:     NewCursorConversationStore,
			newParser:    NewCursorLogParser,
			activePath:   ".cursor/chats/ws1/chat-cur-1.json",
			activeBody:   `{"chatId":"chat-cur-1","name":"Cursor agent refactor","workspacePath":"/work/cursor","model":"cursor-fast","createdAt":"2026-11-22T06:00:00Z","updatedAt":"2026-11-22T06:05:00Z","messages":[{"role":"user","text":"Split handler module"},{"role":"assistant","toolName":"apply_patch","text":"Patched handlers.go"}]}`,
			archivedPath: ".cursor/sessions/chat-cur-old.jsonl",
			archivedBody: `{"chatId":"chat-cur-old","cwd":"/work/cursor","archived":true,"role":"user","text":"Archived cursor"}` + "\n",
			wantSession:  "chat-cur-1",
			wantCWD:      "/work/cursor",
			wantModel:    "cursor-fast",
			wantTool:     "apply_patch",
			searchQuery:  "handlers.go",
		},
		{
			name:         "deepseek",
			newStore:     NewDeepSeekConversationStore,
			newParser:    NewDeepSeekLogParser,
			activePath:   ".deepseek/sessions/ds-1.jsonl",
			activeBody:   `{"session_id":"ds-1","cwd":"/work/ds","model":"deepseek-r1","timestamp":"2026-11-22T07:00:00Z","role":"user","content":"Derive cache eviction bound"}` + "\n" + `{"session_id":"ds-1","timestamp":"2026-11-22T07:00:30Z","role":"assistant","content":"Verified LRU bound.","tool_calls":[{"function":{"name":"python_exec","arguments":"print(42)"}}]}` + "\n",
			archivedPath: ".local/share/deepseek/sessions/ds-old.json",
			archivedBody: `{"id":"ds-old","cwd":"/work/ds","archived":true,"title":"Old DS"}`,
			wantSession:  "ds-1",
			wantCWD:      "/work/ds",
			wantModel:    "deepseek-r1",
			wantTool:     "python_exec",
			searchQuery:  "eviction bound",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			activeFull := filepath.Join(home, filepath.FromSlash(tc.activePath))
			archivedFull := filepath.Join(home, filepath.FromSlash(tc.archivedPath))
			writeFixture(t, activeFull, tc.activeBody)
			writeFixture(t, archivedFull, tc.archivedBody)

			store := tc.newStore(home)
			list, err := store.List(ctx, agent.ConversationListOptions{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("len(List) = %d, want 1: %+v", len(list), list)
			}
			rec := list[0]
			if rec.SessionID != tc.wantSession || rec.CWD != tc.wantCWD || rec.Model != tc.wantModel {
				t.Errorf("got record (sid=%q cwd=%q model=%q), want (%q, %q, %q)", rec.SessionID, rec.CWD, rec.Model, tc.wantSession, tc.wantCWD, tc.wantModel)
			}

			// IncludeArchived returns both
			allList, err := store.List(ctx, agent.ConversationListOptions{CWD: tc.wantCWD, IncludeArchived: true})
			if err != nil || len(allList) != 2 {
				t.Fatalf("List(IncludeArchived) = (%d, %v), want 2", len(allList), err)
			}

			// Search finds active record
			hits, err := store.Search(ctx, tc.searchQuery, agent.ConversationListOptions{})
			if err != nil || len(hits) != 1 || hits[0].SessionID != tc.wantSession {
				t.Fatalf("Search(%q) = (%+v, %v), want [%s]", tc.searchQuery, hits, err, tc.wantSession)
			}

			// ParseLog extracts tool call and non-empty entries
			entries, err := tc.newParser().ParseLog(ctx, activeFull)
			if err != nil {
				t.Fatalf("ParseLog: %v", err)
			}
			if len(entries) < 2 {
				t.Fatalf("expected >= 2 log entries, got %d: %+v", len(entries), entries)
			}
			sawTool := false
			for _, e := range entries {
				if e.ToolName == tc.wantTool {
					sawTool = true
				}
			}
			if !sawTool {
				t.Errorf("expected tool %q in parsed entries: %+v", tc.wantTool, entries)
			}
		})
	}
}

func TestParsersRespectContextCancellationAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.jsonl")
	writeFixture(t, emptyPath, "\n   \n")

	ctx := context.Background()
	for _, p := range []agent.LogParser{
		NewClaudeLogParser(),
		NewCodexLogParser(),
		NewAntigravityLogParser(),
		NewOpenCodeLogParser(),
	} {
		entries, err := p.ParseLog(ctx, emptyPath)
		if err != nil || len(entries) != 0 {
			t.Errorf("empty file ParseLog = (%+v, %v), want (nil, nil)", entries, err)
		}

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := p.ParseLog(canceled, emptyPath); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled ParseLog err = %v, want context.Canceled", err)
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NewClaudeConversationStore(dir).List(canceled, agent.ConversationListOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled List err = %v, want context.Canceled", err)
	}
	if _, err := NewClaudeConversationStore(dir).Search(canceled, "x", agent.ConversationListOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Search err = %v, want context.Canceled", err)
	}
}

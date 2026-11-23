package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "modernc.org/sqlite"
)

type mockUsageFileCache struct {
	entries map[string]agent.CachedFileUsage
	gets    int
	puts    int
}

func newMockUsageFileCache() *mockUsageFileCache {
	return &mockUsageFileCache{
		entries: make(map[string]agent.CachedFileUsage),
	}
}

func (c *mockUsageFileCache) Get(path string) (agent.CachedFileUsage, bool) {
	c.gets++
	e, ok := c.entries[path]
	return e, ok
}

func (c *mockUsageFileCache) Put(path string, entry agent.CachedFileUsage) {
	c.puts++
	c.entries[path] = entry
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestUsageRegisterAllAttachesSevenProviders(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)

	expected := []string{"claude", "codex", "opencode", "antigravity", "copilot", "grok", "pi"}
	for _, id := range expected {
		p, ok := reg.Resolve(id)
		if !ok {
			t.Fatalf("expected plugin %q to be registered", id)
		}
		if p.UsageReader() == nil {
			t.Fatalf("expected plugin %q to have UsageReader attached", id)
		}
	}

	readers := reg.UsageReaders()
	if len(readers) < len(expected) {
		t.Fatalf("expected at least %d usage readers, got %d", len(expected), len(readers))
	}
}

func TestUsageIncrementalCacheAndRewrite(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "stream.jsonl")

	line1 := `{"type":"session_meta","timestamp":"2026-06-01T10:00:00Z","payload":{"id":"sess-1","cwd":"/work/repo"}}` + "\n"
	line2 := `{"type":"turn_context","timestamp":"2026-06-01T10:00:01Z","payload":{"model":"gpt-5.4"}}` + "\n"
	line3 := `{"type":"event_msg","timestamp":"2026-06-01T10:00:02Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":200,"output_tokens":300,"reasoning_output_tokens":50}}}}` + "\n"
	writeTestFile(t, path, line1+line2+line3)

	cache := newMockUsageFileCache()
	parseCount := 0
	runScan := func() []agent.UsageRecord {
		state := newCodexUsageScanState()
		recs, err := scanJSONLFileIncremental(ctx, path, cache, jsonlScanHooks{
			RestoreState: func(data []byte) error {
				if len(data) == 0 {
					return nil
				}
				return unmarshalCodexState(data, &state)
			},
			SaveState: func() ([]byte, error) {
				return marshalCodexState(state)
			},
			ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
				parseCount++
				rec, ok := parseCodexUsageLine(line, state)
				if !ok {
					return nil, false
				}
				return []agent.UsageRecord{rec}, false
			},
		})
		if err != nil {
			t.Fatalf("scanJSONLFileIncremental: %v", err)
		}
		return recs
	}

	// 1. Initial scan parses all 3 lines.
	recs := runScan()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record on initial scan, got %d", len(recs))
	}
	if parseCount != 3 {
		t.Fatalf("expected 3 parsed lines initially, got %d", parseCount)
	}
	if cache.puts != 1 {
		t.Fatalf("expected 1 cache put, got %d", cache.puts)
	}

	// 2. Unchanged file returns cached records with 0 lines parsed.
	parseCount = 0
	recs = runScan()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record on cache hit, got %d", len(recs))
	}
	if parseCount != 0 {
		t.Fatalf("expected 0 parsed lines on unchanged cache hit, got %d", parseCount)
	}

	// 3. Append a new token_count line; incremental resume restores model/session state
	// and parses only the 1 appended line without duplicating earlier records.
	line4 := `{"type":"event_msg","timestamp":"2026-06-01T10:00:10Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":150,"reasoning_output_tokens":25}}}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(line4); err != nil {
		t.Fatalf("append write: %v", err)
	}
	f.Close()

	parseCount = 0
	recs = runScan()
	if len(recs) != 2 {
		t.Fatalf("expected 2 records after append, got %d", len(recs))
	}
	if parseCount != 1 {
		t.Fatalf("expected only 1 parsed line on incremental resume, got %d", parseCount)
	}
	if recs[1].Model != "gpt-5.4" || recs[1].SessionID != "sess-1" {
		t.Fatalf("expected restored state model=gpt-5.4 session=sess-1, got model=%q session=%q", recs[1].Model, recs[1].SessionID)
	}

	// 4. Truncate/rewrite file triggers full reparse (even when rewritten with different content).
	rewritten := line1 + line2 + line4
	writeTestFile(t, path, rewritten)
	// Bump mtime explicitly in case filesystem has coarse mtime resolution.
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	parseCount = 0
	recs = runScan()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record after rewrite, got %d", len(recs))
	}
	if parseCount != 3 {
		t.Fatalf("expected full reparse of 3 lines after rewrite, got %d", parseCount)
	}
	if recs[0].Totals.OutputTokens != 150 {
		t.Fatalf("expected rewritten output=150, got %d", recs[0].Totals.OutputTokens)
	}
}

func marshalCodexState(s *codexUsageScanState) ([]byte, error) {
	return json.Marshal(s)
}

func unmarshalCodexState(data []byte, dst **codexUsageScanState) error {
	var s codexUsageScanState
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*dst = &s
	return nil
}

func TestUsageClaudeReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	jsonl := strings.Join([]string{
		// User line (ignored)
		`{"type":"user","timestamp":"2026-06-01T12:00:00Z","sessionId":"s-1","cwd":"/work/spaces","message":{"role":"user","content":"hi"}}`,
		// First assistant content block with tool_use + usage + costUSD + fast speed
		`{"type":"assistant","timestamp":"2026-06-01T12:00:05Z","sessionId":"s-1","requestId":"req-1","cwd":"/work/spaces","costUSD":0.042,"message":{"id":"msg-1","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"cache_read_input_tokens":500,"cache_creation_input_tokens":50,"output_tokens":200,"speed":"fast"},"content":[{"type":"text","text":"running"},{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Read"}]}}`,
		// Repeated content block for the SAME message.id + requestId -> must be deduplicated
		`{"type":"assistant","timestamp":"2026-06-01T12:00:06Z","sessionId":"s-1","requestId":"req-1","cwd":"/work/spaces","costUSD":0.042,"message":{"id":"msg-1","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"cache_read_input_tokens":500,"cache_creation_input_tokens":50,"output_tokens":200,"speed":"fast"},"content":[{"type":"text","text":"second block"}]}}`,
		// Synthetic model -> must be dropped
		`{"type":"assistant","timestamp":"2026-06-01T12:00:07Z","sessionId":"s-1","requestId":"req-2","message":{"id":"msg-2","model":"<synthetic>","usage":{"input_tokens":10,"output_tokens":10}}}`,
		// Empty model with zero tokens -> must be dropped
		`{"type":"assistant","timestamp":"2026-06-01T12:00:08Z","sessionId":"s-1","requestId":"req-3","message":{"id":"msg-3","model":"","usage":{"input_tokens":0,"output_tokens":0}}}`,
	}, "\n") + "\n"

	writeTestFile(t, filepath.Join(home, ".claude", "projects", "proj-a", "session-fallback.jsonl"), jsonl)

	recs, err := NewClaudeUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Profile: "work",
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 deduplicated record, got %d: %+v", len(recs), recs)
	}

	r := recs[0]
	if r.Provider != "claude" || r.Profile != "work" {
		t.Fatalf("unexpected provider/profile: %+v", r)
	}
	if r.Model != "claude-sonnet-4-6" || r.SessionID != "s-1" {
		t.Fatalf("unexpected model/session: %+v", r)
	}
	if r.Project != "spaces" || r.ProjectPath != "/work/spaces" {
		t.Fatalf("unexpected project: %q (%q)", r.Project, r.ProjectPath)
	}
	if r.Speed != agent.UsageSpeedFast {
		t.Fatalf("expected fast speed, got %q", r.Speed)
	}
	if r.DedupeKey != "claude:msg-1:req-1" {
		t.Fatalf("expected dedupe key claude:msg-1:req-1, got %q", r.DedupeKey)
	}
	if r.ReportedCostUSD == nil || math.Abs(*r.ReportedCostUSD-0.042) > 1e-9 {
		t.Fatalf("unexpected ReportedCostUSD: %v", r.ReportedCostUSD)
	}
	if r.Totals.UncachedInputTokens != 100 ||
		r.Totals.CachedInputTokens != 500 ||
		r.Totals.CacheCreationTokens != 50 ||
		r.Totals.OutputTokens != 200 ||
		r.Totals.ReasoningTokens != 0 {
		t.Fatalf("unexpected totals: %+v", r.Totals)
	}
	if len(r.Tools) != 2 || r.Tools[0] != "Bash" || r.Tools[1] != "Read" {
		t.Fatalf("unexpected tools: %+v", r.Tools)
	}
}

func TestUsageCodexReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	// Normal session testing normalization, duplicate suppression, speed tier, tool normalization, and total fallback.
	normalLog := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-06-01T10:00:00Z","payload":{"id":"codex-1","cwd":"/work/app"}}`,
		`{"type":"turn_context","timestamp":"2026-06-01T10:00:01Z","payload":{"model":"gpt-5.4"}}`,
		`{"type":"event_msg","timestamp":"2026-06-01T10:00:01Z","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}`,
		`{"type":"response_item","timestamp":"2026-06-01T10:00:02Z","payload":{"type":"function_call","name":"exec_command"}}`,
		`{"type":"response_item","timestamp":"2026-06-01T10:00:02Z","payload":{"type":"function_call","name":"apply_patch"}}`,
		// Turn 1: input=1000 includes cached=300 and cache_write=100 -> uncached=600; output=400 includes reasoning=150.
		`{"type":"event_msg","timestamp":"2026-06-01T10:00:05Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":100,"output_tokens":400,"reasoning_output_tokens":150},"total_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":100,"output_tokens":400,"reasoning_output_tokens":150}}}}`,
		// Exact duplicate re-emission -> must be suppressed.
		`{"type":"event_msg","timestamp":"2026-06-01T10:00:06Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":100,"output_tokens":400,"reasoning_output_tokens":150},"total_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"cache_write_input_tokens":100,"output_tokens":400,"reasoning_output_tokens":150}}}}`,
		// Turn 2: total_token_usage fallback delta (no last_token_usage).
		`{"type":"event_msg","timestamp":"2026-06-01T10:00:15Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":400,"cache_write_input_tokens":100,"output_tokens":500,"reasoning_output_tokens":200}}}}`,
	}, "\n") + "\n"

	// Forked session testing burst suppression (<1000ms from first token_count suppressed, >=1000ms emitted).
	forkLog := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-06-01T11:00:00.000Z","payload":{"id":"codex-fork","cwd":"/work/app","forked_from_id":"codex-1"}}`,
		`{"type":"turn_context","timestamp":"2026-06-01T11:00:00.010Z","payload":{"model":"gpt-5.4"}}`,
		// Copied parent event #1 (anchor at 11:00:00.020Z) -> suppressed
		`{"type":"event_msg","timestamp":"2026-06-01T11:00:00.020Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"output_tokens":100}}}}`,
		// Copied parent event #2 (300ms after anchor) -> suppressed
		`{"type":"event_msg","timestamp":"2026-06-01T11:00:00.320Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":600,"output_tokens":120}}}}`,
		// Genuine child turn (5000ms after anchor) -> emitted
		`{"type":"event_msg","timestamp":"2026-06-01T11:00:05.020Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"cached_input_tokens":50,"output_tokens":80,"reasoning_output_tokens":20}}}}`,
	}, "\n") + "\n"

	writeTestFile(t, filepath.Join(home, ".codex", "sessions", "2026", "06", "rollout-normal.jsonl"), normalLog)
	writeTestFile(t, filepath.Join(home, ".codex", "archived_sessions", "rollout-fork.jsonl"), forkLog)

	recs, err := NewCodexUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("expected 3 records (2 normal + 1 fork child), got %d: %+v", len(recs), recs)
	}

	var bySession = map[string][]agent.UsageRecord{}
	for _, r := range recs {
		bySession[r.SessionID] = append(bySession[r.SessionID], r)
	}

	norm := bySession["codex-1"]
	if len(norm) != 2 {
		t.Fatalf("expected 2 normal records, got %d", len(norm))
	}
	if norm[0].Totals.UncachedInputTokens != 600 ||
		norm[0].Totals.CachedInputTokens != 300 ||
		norm[0].Totals.CacheCreationTokens != 100 ||
		norm[0].Totals.OutputTokens != 400 ||
		norm[0].Totals.ReasoningTokens != 150 {
		t.Fatalf("unexpected codex turn 1 totals: %+v", norm[0].Totals)
	}
	if norm[0].Speed != agent.UsageSpeedFast {
		t.Fatalf("expected fast speed for priority tier, got %q", norm[0].Speed)
	}
	if len(norm[0].Tools) != 2 || norm[0].Tools[0] != "Bash" || norm[0].Tools[1] != "Edit" {
		t.Fatalf("unexpected codex tools: %+v", norm[0].Tools)
	}
	// Turn 2 delta: input=500, cached=100, cache_write=0 -> uncached=400, output=100, reasoning=50.
	if norm[1].Totals.UncachedInputTokens != 400 || norm[1].Totals.CachedInputTokens != 100 ||
		norm[1].Totals.OutputTokens != 100 || norm[1].Totals.ReasoningTokens != 50 {
		t.Fatalf("unexpected codex turn 2 delta totals: %+v", norm[1].Totals)
	}

	fork := bySession["codex-fork"]
	if len(fork) != 1 {
		t.Fatalf("expected 1 fork child record (burst suppressed), got %d", len(fork))
	}
	if fork[0].Totals.UncachedInputTokens != 150 || fork[0].Totals.OutputTokens != 80 || fork[0].Totals.ReasoningTokens != 20 {
		t.Fatalf("unexpected fork child totals: %+v", fork[0].Totals)
	}
}

func TestUsageOpenCodeReaderSQLiteAndLegacy(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	ocDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	dbPath := filepath.Join(ocDir, "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
		CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT);
		CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO session (id, directory) VALUES ('ses-1', '/work/myproj')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	msgData := `{"role":"assistant","modelID":"claude-sonnet-4-6","time":{"created":1780000000000},"tokens":{"input":300,"output":120,"reasoning":80,"cache":{"read":50,"write":25}},"cost":0.015}`
	if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, data) VALUES ('m-1', 'ses-1', 1780000000000, ?)`, msgData); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	db.Close()

	// Legacy JSON: one duplicate of m-1 (should be skipped) and one new m-2 with cost=0 (ReportedCostUSD nil).
	writeTestFile(t, filepath.Join(ocDir, "storage", "message", "ses-1", "m-1.json"), msgData)
	legacy2 := `{"id":"m-2","sessionID":"ses-1","role":"assistant","model":{"id":"gpt-5.4"},"time":{"created":1780000005000},"tokens":{"input":200,"output":60,"reasoning":40},"cost":0,"cwd":"/work/legacy"}`
	writeTestFile(t, filepath.Join(ocDir, "storage", "message", "ses-1", "m-2.json"), legacy2)

	recs, err := NewOpenCodeUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (1 sqlite + 1 unique legacy), got %d: %+v", len(recs), recs)
	}

	byKey := map[string]agent.UsageRecord{}
	for _, r := range recs {
		byKey[r.DedupeKey] = r
	}

	r1, ok := byKey["opencode:m-1"]
	if !ok {
		t.Fatalf("missing opencode:m-1")
	}
	if r1.Project != "myproj" || r1.ProjectPath != "/work/myproj" {
		t.Fatalf("expected project from session table, got %q (%q)", r1.Project, r1.ProjectPath)
	}
	// OutputTokens must be output + reasoning = 120 + 80 = 200; ReasoningTokens = 80.
	if r1.Totals.OutputTokens != 200 || r1.Totals.ReasoningTokens != 80 ||
		r1.Totals.UncachedInputTokens != 300 || r1.Totals.CachedInputTokens != 50 || r1.Totals.CacheCreationTokens != 25 {
		t.Fatalf("unexpected opencode m-1 totals: %+v", r1.Totals)
	}
	if r1.ReportedCostUSD == nil || math.Abs(*r1.ReportedCostUSD-0.015) > 1e-9 {
		t.Fatalf("unexpected m-1 cost: %v", r1.ReportedCostUSD)
	}

	r2, ok := byKey["opencode:m-2"]
	if !ok {
		t.Fatalf("missing opencode:m-2")
	}
	if r2.Totals.OutputTokens != 100 || r2.Totals.ReasoningTokens != 40 {
		t.Fatalf("unexpected opencode m-2 totals: %+v", r2.Totals)
	}
	if r2.ReportedCostUSD != nil {
		t.Fatalf("expected nil ReportedCostUSD when cost==0, got %v", *r2.ReportedCostUSD)
	}
}

func TestUsageAntigravityReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	// Session 1: heuristic estimation + tool normalization + model detection.
	transcript1 := strings.Join([]string{
		`{"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-06-01T12:00:00Z","content":"<USER_REQUEST>Use gemini-2.5-pro to fix build</USER_REQUEST>"}`,
		`{"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-06-01T12:00:04Z","thinking":"1234567890123456","content":"Done","tool_calls":[{"name":"run_command","args":{"Cwd":"\"/work/proj\""}}]}`,
	}, "\n") + "\n"
	writeTestFile(t, filepath.Join(home, ".gemini", "antigravity-cli", "brain", "sess-heur", ".system_generated", "logs", "transcript.jsonl"), transcript1)

	// Session 2: fallback to transcript_full.jsonl with explicit usage fields.
	transcript2 := `{"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-06-01T12:05:00Z","model":"gemini-3.7-flash","usage":{"input_tokens":800,"cached_input_tokens":100,"output_tokens":250,"reasoning_tokens":50}}` + "\n"
	writeTestFile(t, filepath.Join(home, ".gemini", "antigravity-cli", "brain", "sess-explicit", ".system_generated", "logs", "transcript_full.jsonl"), transcript2)

	recs, err := NewAntigravityUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 antigravity records, got %d", len(recs))
	}

	bySess := map[string]agent.UsageRecord{}
	for _, r := range recs {
		bySess[r.SessionID] = r
	}

	h := bySess["sess-heur"]
	if h.Model != "gemini-2.5-pro" || h.Project != "proj" {
		t.Fatalf("unexpected heuristic record: %+v", h)
	}
	if h.Totals.UncachedInputTokens < 100 || h.Totals.OutputTokens < 50 || h.Totals.ReasoningTokens != 4 {
		t.Fatalf("unexpected heuristic totals: %+v", h.Totals)
	}
	if len(h.Tools) != 1 || h.Tools[0] != "Bash" {
		t.Fatalf("unexpected tools: %+v", h.Tools)
	}

	ex := bySess["sess-explicit"]
	if ex.Totals.UncachedInputTokens != 800 || ex.Totals.CachedInputTokens != 100 || ex.Totals.OutputTokens != 250 || ex.Totals.ReasoningTokens != 50 {
		t.Fatalf("unexpected explicit totals: %+v", ex.Totals)
	}
}

func TestUsageCopilotReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	events := strings.Join([]string{
		`{"type":"session.start","timestamp":"2026-06-01T10:00:00Z","data":{"context":{"cwd":"/work/cop"}}}`,
		`{"type":"tool.execution_start","timestamp":"2026-06-01T10:00:02Z","data":{"toolName":"bash"}}`,
		`{"type":"session.shutdown","timestamp":"2026-06-01T10:10:00Z","data":{"modelMetrics":{"claude-sonnet-4-6":{"requests":{"count":5,"cost":3},"usage":{"inputTokens":200,"outputTokens":40,"cacheReadTokens":10,"cacheWriteTokens":5}},"gpt-5.4":{"requests":{"count":2,"cost":1},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":0,"cacheWriteTokens":0}}}}}`,
		// Second shutdown must be ignored to prevent double-counting.
		`{"type":"session.shutdown","timestamp":"2026-06-01T10:12:00Z","data":{"modelMetrics":{"gpt-5.4":{"requests":{"count":2,"cost":1},"usage":{"inputTokens":100,"outputTokens":20}}}}}`,
	}, "\n") + "\n"

	writeTestFile(t, filepath.Join(home, ".copilot", "session-state", "uuid-1", "events.jsonl"), events)

	recs, err := NewCopilotUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 copilot model records, got %d: %+v", len(recs), recs)
	}
	for _, r := range recs {
		if r.ReportedCostUSD != nil {
			t.Fatalf("copilot must ignore requests.cost premium count, got %v", *r.ReportedCostUSD)
		}
		if r.Project != "cop" {
			t.Fatalf("expected project cop, got %q", r.Project)
		}
	}
	// Tools should be attributed to exactly one record.
	toolCount := len(recs[0].Tools) + len(recs[1].Tools)
	if toolCount != 1 {
		t.Fatalf("expected tools attributed once across models, got %d", toolCount)
	}
}

func TestUsageGrokReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	updates := strings.Join([]string{
		`{"timestamp":1780000000,"params":{"sessionId":"grok-s1","_meta":{"agentTimestampMs":1780000001000},"update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"inputTokens":1000,"outputTokens":200,"cachedReadTokens":300,"cacheCreationTokens":100,"reasoningTokens":50,"costUsdTicks":250000000,"modelUsage":{"grok-code-fast-1":{"inputTokens":1000,"outputTokens":200,"cachedReadTokens":300,"cacheCreationTokens":100,"reasoningTokens":50,"costUsdTicks":250000000}}}}}}`,
		// Duplicate prompt_id -> deduplicated
		`{"timestamp":1780000000,"params":{"sessionId":"grok-s1","_meta":{"agentTimestampMs":1780000001000},"update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"inputTokens":1000,"outputTokens":200,"cachedReadTokens":300,"cacheCreationTokens":100,"reasoningTokens":50,"costUsdTicks":250000000,"modelUsage":{"grok-code-fast-1":{"inputTokens":1000,"outputTokens":200,"cachedReadTokens":300,"cacheCreationTokens":100,"reasoningTokens":50,"costUsdTicks":250000000}}}}}}`,
	}, "\n") + "\n"

	writeTestFile(t, filepath.Join(home, ".grok", "sessions", "grok-s1", "updates.jsonl"), updates)

	recs, err := NewGrokUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 grok record, got %d", len(recs))
	}
	r := recs[0]
	if r.Model != "grok-code-fast-1" || r.DedupeKey != "grok:grok-s1:p1:grok-code-fast-1" {
		t.Fatalf("unexpected grok identity: %+v", r)
	}
	if r.Totals.UncachedInputTokens != 600 || r.Totals.CachedInputTokens != 300 ||
		r.Totals.CacheCreationTokens != 100 || r.Totals.OutputTokens != 200 || r.Totals.ReasoningTokens != 50 {
		t.Fatalf("unexpected grok totals: %+v", r.Totals)
	}
	// 250000000 / 1e10 = 0.025
	if r.ReportedCostUSD == nil || math.Abs(*r.ReportedCostUSD-0.025) > 1e-9 {
		t.Fatalf("unexpected grok cost: %v", r.ReportedCostUSD)
	}
}

func TestUsagePiReader(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	lines := strings.Join([]string{
		`{"type":"session","id":"pi-sess-1","cwd":"/work/pi-repo"}`,
		`{"type":"message","id":"e1","timestamp":"2026-06-01T14:00:00Z","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-6","responseModel":"claude-sonnet-4-6-20260201","usage":{"input":400,"output":150,"cacheRead":200,"cacheWrite":50,"reasoning":30,"cost":{"total":0.012}}}}`,
		`{"type":"message","id":"e2","timestamp":"2026-06-01T14:00:05Z","message":{"role":"toolResult","usage":{"input":100,"output":20,"cost":{"total":0}}}}`,
	}, "\n") + "\n"

	writeTestFile(t, filepath.Join(home, ".pi", "agent", "sessions", "sess.jsonl"), lines)

	recs, err := NewPiUsageReader().ScanUsage(ctx, agent.UsageScanOptions{
		HomeDir: home,
		Cache:   newMockUsageFileCache(),
	})
	if err != nil {
		t.Fatalf("ScanUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 pi records, got %d: %+v", len(recs), recs)
	}

	r0 := recs[0]
	if r0.SessionID != "pi-sess-1" || r0.Project != "pi-repo" {
		t.Fatalf("unexpected pi session/project: %+v", r0)
	}
	if r0.Model != "anthropic/claude-sonnet-4-6-20260201" || r0.RateModel != "claude-sonnet-4-6-20260201" {
		t.Fatalf("unexpected pi model/rateModel: %q / %q", r0.Model, r0.RateModel)
	}
	if r0.ReportedCostUSD == nil || math.Abs(*r0.ReportedCostUSD-0.012) > 1e-9 {
		t.Fatalf("unexpected pi cost: %v", r0.ReportedCostUSD)
	}

	r1 := recs[1]
	if r1.Model != "Tools/summaries" || r1.RateModel != "" || r1.ReportedCostUSD != nil {
		t.Fatalf("unexpected pi toolResult record: %+v", r1)
	}
}

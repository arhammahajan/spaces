package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "modernc.org/sqlite"
)

func TestPluginTelemetryWiring(t *testing.T) {
	claude := NewClaude()
	if claude.SessionTelemetry() == nil {
		t.Fatal("NewClaude().SessionTelemetry() is nil")
	}
	if claude.QuotaProvider() == nil {
		t.Fatal("NewClaude().QuotaProvider() is nil")
	}

	codex := NewCodex()
	if codex.SessionTelemetry() == nil {
		t.Fatal("NewCodex().SessionTelemetry() is nil")
	}
	if codex.QuotaProvider() == nil {
		t.Fatal("NewCodex().QuotaProvider() is nil")
	}
	if codex.MCPActivityProvider() == nil {
		t.Fatal("NewCodex().MCPActivityProvider() is nil")
	}

	oc := NewOpenCode()
	if oc.SessionTelemetry() == nil {
		t.Fatal("NewOpenCode().SessionTelemetry() is nil")
	}
}

func TestClaudeIncrementalTailingAndPartialLineDeferral(t *testing.T) {
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".claude", "projects", "-tmp-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projDir, "sess-1.jsonl")

	line1 := `{"type":"user","timestamp":"2026-04-01T10:00:00Z","version":"2.1.76","gitBranch":"main","message":{"role":"user","content":"Implement telemetry sk-ant-secret123"}}` + "\n"
	line2 := `{"type":"assistant","timestamp":"2026-04-01T10:00:05Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":12000},"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/internal/agent/telemetry.go"}}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line1+line2), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newClaudeTelemetryProvider()
	q := agent.SessionMetricsQuery{
		HomeDir:   tmp,
		CWD:       "/tmp/repo",
		OpenFiles: []string{transcript},
		Cmdline:   []string{"/Users/a/Library/Application Support/Claude/claude-code/2.1.76/claude"},
	}

	m1, err := p.CollectSessionMetrics(context.Background(), q)
	if err != nil {
		t.Fatalf("first collect error: %v", err)
	}
	if m1 == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m1.TurnCount != 1 {
		t.Fatalf("TurnCount = %d, want 1", m1.TurnCount)
	}
	if m1.ContextTokens != 12100 {
		t.Fatalf("ContextTokens = %d, want 12100 (inp+cc when cr==0)", m1.ContextTokens)
	}
	if m1.Usage.InputTokens != 100 || m1.Usage.OutputTokens != 50 || m1.Usage.CacheWriteTokens != 12000 {
		t.Fatalf("unexpected usage: %+v", m1.Usage)
	}
	if m1.Summary != "Implement telemetry [REDACTED]" {
		t.Fatalf("Summary = %q, want secret redacted", m1.Summary)
	}
	if m1.CurrentTask != "Edit agent/telemetry.go" {
		t.Fatalf("CurrentTask = %q, want %q", m1.CurrentTask, "Edit agent/telemetry.go")
	}
	if m1.LaunchSurface != agent.LaunchSurfaceApp {
		t.Fatalf("LaunchSurface = %q, want app", m1.LaunchSurface)
	}
	if m1.GitBranch != "main" || m1.Version != "2.1.76" {
		t.Fatalf("branch/version = %q/%q, want main/2.1.76", m1.GitBranch, m1.Version)
	}

	offsetAfterFirst := p.cache[transcript].offset
	if offsetAfterFirst != int64(len(line1+line2)) {
		t.Fatalf("offset = %d, want %d", offsetAfterFirst, len(line1+line2))
	}

	// Append a partial trailing JSON line (no newline) — must be deferred without advancing offset.
	partialPrefix := `{"type":"assistant","timestamp":"2026-04-01T10:01:00Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":200`
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(partialPrefix); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m2, err := p.CollectSessionMetrics(context.Background(), q)
	if err != nil {
		t.Fatalf("partial collect error: %v", err)
	}
	if m2.TurnCount != 1 {
		t.Fatalf("partial line should be deferred: TurnCount = %d, want 1", m2.TurnCount)
	}
	if p.cache[transcript].offset != offsetAfterFirst {
		t.Fatalf("offset advanced on partial line: got %d, want %d", p.cache[transcript].offset, offsetAfterFirst)
	}

	// Complete the partial line with a newline — incremental tail must parse only the new turn.
	partialSuffix := `,"output_tokens":75,"cache_read_input_tokens":15000,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"done"}]}}` + "\n"
	f, err = os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(partialSuffix); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m3, err := p.CollectSessionMetrics(context.Background(), q)
	if err != nil {
		t.Fatalf("completed collect error: %v", err)
	}
	if m3.TurnCount != 2 {
		t.Fatalf("TurnCount = %d, want 2", m3.TurnCount)
	}
	if m3.Usage.InputTokens != 300 || m3.Usage.OutputTokens != 125 || m3.Usage.CacheReadTokens != 15000 {
		t.Fatalf("unexpected accumulated usage: %+v", m3.Usage)
	}
	if m3.ContextTokens != 15200 {
		t.Fatalf("ContextTokens = %d, want 15200", m3.ContextTokens)
	}
	if len(m3.TokenHistory) != 2 || len(m3.ContextHistory) != 2 {
		t.Fatalf("history lens = %d/%d, want 2/2", len(m3.TokenHistory), len(m3.ContextHistory))
	}
}

func TestClaudeCompactionDetectionSyntheticMessagesAndMemory(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "work", "proj")
	if err := os.MkdirAll(filepath.Join(cwd, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Configure 1M model in project settings.local.json.
	if err := os.WriteFile(filepath.Join(cwd, ".claude", "settings.local.json"), []byte(`{"model":"sonnet[1m]","effortLevel":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	projDir := filepath.Join(tmp, ".claude", "projects", encodeClaudeCWDPath(cwd))
	memDir := filepath.Join(projDir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("line 1\nline 2\nline 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}

	transcript := filepath.Join(projDir, "sess-compact.jsonl")
	lines := []string{
		// Synthetic user messages must be skipped for Summary.
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>ignore</local-command-caveat>"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/plugin</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":"<bash-input>ls</bash-input>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		// Real user prompt.
		`{"type":"user","message":{"role":"user","content":"Fix compaction tracker"}}`,
		// Turn 1: large context with high cache read (100k).
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":100000,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"step 1"}]}}`,
		// Turn 2 (non-trigger): moderate drop (80k, > 70% of 100.5k) — should NOT trigger compaction.
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":80000,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"step 2"}]}}`,
		// Turn 3 (non-trigger): context drops < 70%, but cache_read is still >= prev/5 (40k >= 16k) — should NOT trigger.
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":40000,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"step 3"}]}}`,
		// Turn 4 (TRIGGER): context drops to 10k (< 40.5k * 0.7 = 28.35k) AND cache_read drops to 2k (< 40k / 5 = 8k).
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":8000,"output_tokens":100,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"step 4"}]}}`,
	}
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(transcript, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newClaudeTelemetryProvider()
	m, err := p.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
		HomeDir:   tmp,
		CWD:       cwd,
		SessionID: "sess-compact",
		Cmdline:   []string{"/home/u/.vscode/extensions/anthropic.claude-code-2.1.0/claude"},
	})
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if m == nil {
		t.Fatal("expected metrics")
	}
	if m.Summary != "Fix compaction tracker" {
		t.Fatalf("Summary = %q, want %q", m.Summary, "Fix compaction tracker")
	}
	if m.CompactionCount != 1 {
		t.Fatalf("CompactionCount = %d, want 1", m.CompactionCount)
	}
	if m.MemoryFiles != 2 || m.MemoryLines != 3 {
		t.Fatalf("Memory = (%d files, %d lines), want (2, 3)", m.MemoryFiles, m.MemoryLines)
	}
	if m.ContextWindow != 1_000_000 {
		t.Fatalf("ContextWindow = %d, want 1000000 (from settings.local.json sonnet[1m])", m.ContextWindow)
	}
	if m.Effort != "high" {
		t.Fatalf("Effort = %q, want high", m.Effort)
	}
	if m.LaunchSurface != agent.LaunchSurfaceIDE {
		t.Fatalf("LaunchSurface = %q, want ide", m.LaunchSurface)
	}
}

func TestClaudeQuotaProvider(t *testing.T) {
	tmp := t.TempDir()
	claudeDefault := filepath.Join(tmp, ".claude")
	claudeAlt := filepath.Join(tmp, ".claude-work")
	if err := os.MkdirAll(claudeDefault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(claudeAlt, 0o755); err != nil {
		t.Fatal(err)
	}

	// Older file in ~/.claude/abtop-rate-limits.json
	if err := os.WriteFile(filepath.Join(claudeDefault, "abtop-rate-limits.json"), []byte(`{
		"source": "claude",
		"five_hour": {"used_percentage": 12.5, "resets_at": 1775000000},
		"seven_day": {"used_percentage": 22.0, "resets_at": 1775500000},
		"updated_at": 1774900000
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fresher file in ~/.claude-work/spaces-rate-limits.json using `pct` alias.
	if err := os.WriteFile(filepath.Join(claudeAlt, "spaces-rate-limits.json"), []byte(`{
		"source": "claude-work",
		"five_hour": {"pct": 45.0, "resets_at": 1775003600},
		"seven_day": {"pct": 60.5, "resets_at": 1775600000, "window_minutes": 10080},
		"updated_at": 1774900500
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	qp := newClaudeQuotaProvider()
	q, err := qp.FetchQuota(context.Background(), []string{tmp})
	if err != nil {
		t.Fatalf("FetchQuota error: %v", err)
	}
	if q == nil {
		t.Fatal("expected quota info")
	}
	if q.Agent != "claude" || q.UpdatedAt != 1774900500 {
		t.Fatalf("unexpected quota header: %+v", q)
	}
	if q.Primary == nil || q.Primary.UsedPct != 45.0 || q.Primary.WindowMinutes != 300 || q.Primary.Label != "5h" {
		t.Fatalf("unexpected primary window: %+v", q.Primary)
	}
	if q.Secondary == nil || q.Secondary.UsedPct != 60.5 || q.Secondary.WindowMinutes != 10080 || q.Secondary.Label != "7d" {
		t.Fatalf("unexpected secondary window: %+v", q.Secondary)
	}
}

func TestCodexRolloutParsingRateLimitCacheAndQuotaFallback(t *testing.T) {
	tmp := t.TempDir()
	rolloutDir := filepath.Join(tmp, ".codex", "sessions", "2026", "04", "01")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDir, "rollout-2026-04-01-sess-codex-1.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-04-01T12:00:00Z","payload":{"id":"sess-codex-1","cwd":"/repo/app","cli_version":"0.150.0","timestamp":"2026-04-01T12:00:00Z","git":{"branch":"feat/telemetry"}}}`,
		`{"type":"turn_context","timestamp":"2026-04-01T12:00:01Z","payload":{"model":"gpt-5-codex","effort":"medium","model_context_window":258400}}`,
		`{"type":"event_msg","timestamp":"2026-04-01T12:00:02Z","payload":{"type":"user_message","message":"Refactor parser sk-proj-topsecret"}}`,
		`{"type":"event_msg","timestamp":"2026-04-01T12:00:03Z","payload":{"type":"agent_message","message":"On it"}}`,
		`{"type":"response_item","timestamp":"2026-04-01T12:00:04Z","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}","call_id":"c1"}}`,
		`{"type":"event_msg","timestamp":"2026-04-01T12:00:05Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5000,"output_tokens":400,"cached_input_tokens":3000},"last_token_usage":{"input_tokens":1200,"output_tokens":200,"cached_input_tokens":800},"model_context_window":258400},"rate_limits":{"limit_id":"codex","primary":{"used_percent":18.5,"window_minutes":300,"resets_at":1775050000},"secondary":{"used_percent":31.0,"window_minutes":10080,"resets_at":1775600000}}}}`,
		// Model-specific rate limit should be ignored.
		`{"type":"event_msg","timestamp":"2026-04-01T12:00:06Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5000,"output_tokens":400,"cached_input_tokens":3000},"last_token_usage":{"input_tokens":1200,"output_tokens":200}},"rate_limits":{"limit_id":"codex_spark","primary":{"used_percent":0.0,"window_minutes":300,"resets_at":1775050000}}}}`,
	}
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(rollout, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &codexRateLimitStore{}
	tp := newCodexTelemetryProvider(store)
	qp := newCodexQuotaProvider(store)

	m, err := tp.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
		HomeDir: tmp,
		CWD:     "/repo/app",
	})
	if err != nil {
		t.Fatalf("CollectSessionMetrics error: %v", err)
	}
	if m == nil {
		t.Fatal("expected codex metrics")
	}
	if m.SessionID != "sess-codex-1" || m.Model != "gpt-5-codex" || m.Effort != "medium" || m.GitBranch != "feat/telemetry" {
		t.Fatalf("unexpected codex metadata: %+v", m)
	}
	if m.Usage.InputTokens != 2000 || m.Usage.OutputTokens != 400 || m.Usage.CacheReadTokens != 3000 {
		t.Fatalf("unexpected codex usage (non-cached input should be 5000-3000=2000): %+v", m.Usage)
	}
	if m.ContextTokens != 1200 || m.ContextWindow != 258400 {
		t.Fatalf("context = %d/%d, want 1200/258400", m.ContextTokens, m.ContextWindow)
	}
	if m.TurnCount != 1 {
		t.Fatalf("TurnCount = %d, want 1", m.TurnCount)
	}
	if m.Summary != "Refactor parser [REDACTED]" {
		t.Fatalf("Summary = %q", m.Summary)
	}
	if m.CurrentTask != "exec_command go test ./..." {
		t.Fatalf("CurrentTask = %q", m.CurrentTask)
	}

	// Verify atomic cache file written to ~/.cache/spaces/codex-rate-limits.json.
	cacheFile := filepath.Join(tmp, ".cache", "spaces", "codex-rate-limits.json")
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("expected codex rate-limit cache at %s: %v", cacheFile, err)
	}

	// QuotaProvider with live store returns live rate limit.
	qLive, err := qp.FetchQuota(context.Background(), []string{tmp})
	if err != nil || qLive == nil {
		t.Fatalf("FetchQuota live error: %v, q=%v", err, qLive)
	}
	if qLive.Primary == nil || qLive.Primary.UsedPct != 18.5 || qLive.Secondary == nil || qLive.Secondary.UsedPct != 31.0 {
		t.Fatalf("unexpected live codex quota: %+v", qLive)
	}

	// Fresh QuotaProvider (empty in-memory store) falls back to cache files, picking freshest (abtop vs spaces).
	abtopCache := filepath.Join(tmp, ".cache", "abtop", "codex-rate-limits.json")
	if err := os.MkdirAll(filepath.Dir(abtopCache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abtopCache, []byte(`{
		"source": "codex",
		"five_hour": {"used_percentage": 55.0, "resets_at": 1775090000, "window_minutes": 300},
		"updated_at": 1999999999
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	qpCold := newCodexQuotaProvider(&codexRateLimitStore{})
	qFallback, err := qpCold.FetchQuota(context.Background(), []string{tmp})
	if err != nil || qFallback == nil {
		t.Fatalf("FetchQuota fallback error: %v, q=%v", err, qFallback)
	}
	if qFallback.Primary == nil || qFallback.Primary.UsedPct != 55.0 {
		t.Fatalf("expected freshest abtop cache fallback (55.0%%), got %+v", qFallback.Primary)
	}
}

func TestCodexMCPActivityProvider(t *testing.T) {
	tmp := t.TempDir()
	freshRollout := filepath.Join(tmp, "rollout-fresh.jsonl")
	staleRollout := filepath.Join(tmp, "rollout-stale.jsonl")
	notRollout := filepath.Join(tmp, "notes.txt")
	for _, f := range []string{freshRollout, staleRollout, notRollout} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	staleTime := now.Add(-45 * time.Minute)
	if err := os.Chtimes(staleRollout, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	snap := &agent.ProcessSnapshot{
		Processes: map[int]agent.OSProcess{
			50:  {PID: 50, PPID: 1, Comm: "claude", Args: []string{"/usr/local/bin/claude"}},
			100: {PID: 100, PPID: 50, Comm: "codex", Args: []string{"codex", "mcp-server", "-c", "profile=qwen36-litellm"}, MemKB: 20480},
			101: {PID: 101, PPID: 999, Comm: "codex", Args: []string{"codex", "mcp-server", "--profile", "work"}, MemKB: 10240},
			// Should be excluded:
			102: {PID: 102, PPID: 1, Comm: "codex", Args: []string{"codex", "app-server"}},
			103: {PID: 103, PPID: 1, Comm: "grep", Args: []string{"grep", "codex mcp-server"}},
			104: {PID: 104, PPID: 1, Comm: "claude", Args: []string{"claude", "mcp", "serve"}},
		},
		OpenFilesFn: func(pid int) []string {
			if pid == 100 {
				return []string{freshRollout, staleRollout, notRollout}
			}
			return nil
		},
	}

	mcp := newCodexMCPActivityProvider()
	servers, err := mcp.CollectLiveMCPServers(context.Background(), snap)
	if err != nil {
		t.Fatalf("CollectLiveMCPServers error: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d MCP servers, want 2: %+v", len(servers), servers)
	}

	// Sorted by (ParentCLI, PID): "?" (pid 101) comes before "claude" (pid 100).
	if servers[0].PID != 101 || servers[0].ParentCLI != "?" || servers[0].Profile != "work" {
		t.Fatalf("unexpected server[0]: %+v", servers[0])
	}
	if servers[1].PID != 100 || servers[1].ParentCLI != "claude" || servers[1].Profile != "qwen36-litellm" {
		t.Fatalf("unexpected server[1]: %+v", servers[1])
	}
	if servers[1].ActiveThreads != 1 || servers[1].TotalThreads != 2 {
		t.Fatalf("threads = %d/%d, want 1/2", servers[1].ActiveThreads, servers[1].TotalThreads)
	}
	if servers[1].LastActivityAt == 0 {
		t.Fatalf("expected non-zero LastActivityAt")
	}
}

func TestOpenCodeSQLiteTelemetry(t *testing.T) {
	tmp := t.TempDir()
	dbDir := filepath.Join(tmp, ".local", "share", "opencode")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "opencode.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	schema := `
	CREATE TABLE session (
		id TEXT PRIMARY KEY,
		title TEXT,
		directory TEXT,
		version TEXT,
		time_created INTEGER,
		time_updated INTEGER
	);
	CREATE TABLE message (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		time_created INTEGER,
		data TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(
		`INSERT INTO session (id, title, directory, version, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)`,
		"oc-sess-1", "Debug auth ghp_secretToken99", "/repo/oc-proj", "0.9.1", 1775000000000, 1775000060000,
	); err != nil {
		t.Fatal(err)
	}

	msg1, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"providerID": "anthropic",
		"modelID":    "claude-sonnet-4-6",
		"cost":       0.015,
		"tokens": map[string]any{
			"input":     1000,
			"output":    250,
			"reasoning": 50,
			"cache": map[string]any{
				"read":  4000,
				"write": 500,
			},
		},
	})
	msg2, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"providerID": "anthropic",
		"modelID":    "claude-opus-4-6[1m]",
		"cost":       0.035,
		"tokens": map[string]any{
			"input":     1500,
			"output":    300,
			"reasoning": 100,
			"cache": map[string]any{
				"read":  6000,
				"write": 200,
			},
		},
	})
	userMsg, _ := json.Marshal(map[string]any{"role": "user", "text": "hello"})

	for i, raw := range [][]byte{userMsg, msg1, msg2} {
		if _, err := db.Exec(
			`INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`,
			"m"+string(rune('0'+i)), "oc-sess-1", 1775000001000+int64(i)*1000, string(raw),
		); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	p := newOpenCodeTelemetryProvider()
	m, err := p.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
		HomeDir: tmp,
		CWD:     "/repo/oc-proj",
	})
	if err != nil {
		t.Fatalf("CollectSessionMetrics error: %v", err)
	}
	if m == nil {
		t.Fatal("expected opencode metrics")
	}
	if m.Agent != "opencode" || m.SessionID != "oc-sess-1" {
		t.Fatalf("unexpected id/agent: %+v", m)
	}
	if m.Model != "anthropic/claude-opus-4-6[1m]" {
		t.Fatalf("Model = %q, want anthropic/claude-opus-4-6[1m]", m.Model)
	}
	if m.Summary != "Debug auth [REDACTED]" {
		t.Fatalf("Summary = %q", m.Summary)
	}
	if m.TurnCount != 2 {
		t.Fatalf("TurnCount = %d, want 2", m.TurnCount)
	}
	if m.Usage.InputTokens != 2500 || m.Usage.OutputTokens != 550 || m.Usage.ReasoningTokens != 150 ||
		m.Usage.CacheReadTokens != 10000 || m.Usage.CacheWriteTokens != 700 {
		t.Fatalf("unexpected usage: %+v", m.Usage)
	}
	if m.Usage.CostUSD < 0.049 || m.Usage.CostUSD > 0.051 {
		t.Fatalf("CostUSD = %v, want ~0.05", m.Usage.CostUSD)
	}
	if m.ContextTokens != 7500 {
		t.Fatalf("ContextTokens = %d, want 7500 (1500+6000)", m.ContextTokens)
	}
	if m.ContextWindow != 1_000_000 {
		t.Fatalf("ContextWindow = %d, want 1000000", m.ContextWindow)
	}
	if len(m.TokenHistory) != 2 {
		t.Fatalf("TokenHistory len = %d, want 2", len(m.TokenHistory))
	}
	if !m.Usage.CostPriced {
		t.Fatalf("expected CostPriced=true when OpenCode SQLite messages report explicit cost")
	}
}

func TestCodexWhamQuotaPullWindowRoutingAndThrottle(t *testing.T) {
	origEndpoint := codexWhamEndpoint
	origClient := codexWhamHTTPClient
	t.Cleanup(func() {
		codexWhamEndpoint = origEndpoint
		codexWhamHTTPClient = origClient
	})

	t.Run("headers_both_windows_and_atomic_cache", func(t *testing.T) {
		tmp := t.TempDir()
		authDir := filepath.Join(tmp, ".codex")
		if err := os.MkdirAll(authDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(`{
			"tokens": {
				"access_token": "tok-abc-123",
				"account_id": "acct-xyz-789"
			}
		}`), 0o600); err != nil {
			t.Fatal(err)
		}

		var gotAuth, gotOrig, gotAcct, gotAccept string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotOrig = r.Header.Get("originator")
			gotAcct = r.Header.Get("chatgpt-account-id")
			gotAccept = r.Header.Get("Accept")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"plan_type": "prolite",
				"rate_limit": {
					"primary_window": {
						"used_percent": 12.5,
						"limit_window_seconds": 18000,
						"reset_at": 1795500000
					},
					"secondary_window": {
						"used_percent": 44.0,
						"limit_window_seconds": 604800,
						"reset_after_seconds": 3600
					}
				}
			}`))
		}))
		defer srv.Close()

		codexWhamEndpoint = srv.URL
		codexWhamHTTPClient = srv.Client()

		qp := newCodexQuotaProvider(&codexRateLimitStore{})
		q, err := qp.FetchQuota(context.Background(), []string{tmp})
		if err != nil {
			t.Fatalf("FetchQuota error: %v", err)
		}
		if q == nil {
			t.Fatal("expected non-nil QuotaInfo")
		}
		if gotAuth != "Bearer tok-abc-123" || gotOrig != "codex_cli_rs" || gotAcct != "acct-xyz-789" || gotAccept != "application/json" {
			t.Fatalf("unexpected headers: auth=%q orig=%q acct=%q accept=%q", gotAuth, gotOrig, gotAcct, gotAccept)
		}
		if q.Source != "codex_wham" || q.PlanType != "prolite" || q.Agent != "codex" {
			t.Fatalf("unexpected quota metadata: %+v", q)
		}
		if q.Primary == nil || q.Primary.UsedPct != 12.5 || q.Primary.WindowMinutes != 300 || q.Primary.Label != "5h" || q.Primary.ResetsAt != 1795500000 {
			t.Fatalf("unexpected primary window: %+v", q.Primary)
		}
		if q.Secondary == nil || q.Secondary.UsedPct != 44.0 || q.Secondary.WindowMinutes != 10080 || q.Secondary.Label != "7d" || q.Secondary.ResetsAt <= time.Now().Unix() {
			t.Fatalf("unexpected secondary window: %+v", q.Secondary)
		}

		// Verify atomic disk cache written and per-PID temp file cleaned up.
		cachePath := filepath.Join(tmp, ".cache", "spaces", "codex-rate-limits.json")
		if _, err := os.Stat(cachePath); err != nil {
			t.Fatalf("expected cache file at %s: %v", cachePath, err)
		}
		tmpPath := fmt.Sprintf("%s.tmp.%d", cachePath, os.Getpid())
		if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
			t.Fatalf("expected temp file %s to be renamed away", tmpPath)
		}
	})

	t.Run("weekly_only_primary_window_routes_to_secondary", func(t *testing.T) {
		tmp := t.TempDir()
		authDir := filepath.Join(tmp, ".codex")
		if err := os.MkdirAll(authDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(`{
			"tokens": {
				"access_token": "tok-prolite",
				"chatgpt_account_id": "acct-prolite"
			}
		}`), 0o600); err != nil {
			t.Fatal(err)
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"plan_type": "prolite",
				"rate_limit": {
					"primary_window": {
						"used_percent": 27.5,
						"limit_window_seconds": 604800,
						"reset_at": 1795600000
					},
					"secondary_window": null
				}
			}`))
		}))
		defer srv.Close()

		codexWhamEndpoint = srv.URL
		codexWhamHTTPClient = srv.Client()

		qp := newCodexQuotaProvider(&codexRateLimitStore{})
		q, err := qp.FetchQuota(context.Background(), []string{tmp})
		if err != nil {
			t.Fatalf("FetchQuota error: %v", err)
		}
		if q == nil {
			t.Fatal("expected non-nil QuotaInfo")
		}
		if q.Primary != nil {
			t.Fatalf("expected Primary=nil for weekly-only plan, got %+v", q.Primary)
		}
		if q.Secondary == nil || q.Secondary.UsedPct != 27.5 || q.Secondary.WindowMinutes != 10080 || q.Secondary.Label != "7d" {
			t.Fatalf("expected weekly window routed to Secondary, got %+v", q.Secondary)
		}
		if q.PlanType != "prolite" || q.Source != "codex_wham" {
			t.Fatalf("unexpected plan/source: %+v", q)
		}
	})

	t.Run("negative_cache_throttle_on_401_and_500", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
			tmp := t.TempDir()
			authDir := filepath.Join(tmp, ".codex")
			if err := os.MkdirAll(authDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(`{
				"tokens": {"access_token": "tok-fail"}
			}`), 0o600); err != nil {
				t.Fatal(err)
			}

			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
			}))

			codexWhamEndpoint = srv.URL
			codexWhamHTTPClient = srv.Client()

			qp := newCodexQuotaProvider(&codexRateLimitStore{})
			q1, err := qp.FetchQuota(context.Background(), []string{tmp})
			if err != nil {
				t.Fatalf("status %d first call err: %v", status, err)
			}
			if q1 != nil {
				t.Fatalf("status %d first call expected nil quota, got %+v", status, q1)
			}
			q2, err := qp.FetchQuota(context.Background(), []string{tmp})
			if err != nil {
				t.Fatalf("status %d second call err: %v", status, err)
			}
			if q2 != nil {
				t.Fatalf("status %d second call expected nil quota, got %+v", status, q2)
			}
			srv.Close()

			if got := hits.Load(); got != 1 {
				t.Fatalf("status %d: expected 1 HTTP attempt due to 60s throttle, got %d", status, got)
			}
		}
	})
}

func TestClaudeQuotaCascadeWindowlessRFC3339AndTier2(t *testing.T) {
	t.Run("windowless_guard_and_rfc3339_resets_at", func(t *testing.T) {
		tmp := t.TempDir()
		claudeDir := filepath.Join(tmp, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Fresher file has NO windows (windowless payload) — must be ignored!
		if err := os.WriteFile(filepath.Join(claudeDir, "spaces-rate-limits.json"), []byte(`{
			"source": "claude",
			"five_hour": null,
			"seven_day": null,
			"updated_at": 1999999999
		}`), 0o600); err != nil {
			t.Fatal(err)
		}

		// Valid file has RFC3339 string for five_hour.resets_at and numeric epoch for seven_day.resets_at.
		if err := os.WriteFile(filepath.Join(claudeDir, "abtop-rate-limits.json"), []byte(`{
			"source": "claude",
			"five_hour": {"used_percentage": 33.0, "resets_at": "2026-11-23T12:00:00Z"},
			"seven_day": {"used_percentage": 18.0, "resets_at": 1795500000},
			"updated_at": 1795400000
		}`), 0o600); err != nil {
			t.Fatal(err)
		}

		qp := newClaudeQuotaProvider()
		q, err := qp.FetchQuota(context.Background(), []string{tmp})
		if err != nil {
			t.Fatalf("FetchQuota error: %v", err)
		}
		if q == nil {
			t.Fatal("expected valid quota from fallback file")
		}
		wantReset := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC).Unix()
		if q.Primary == nil || q.Primary.UsedPct != 33.0 || q.Primary.ResetsAt != wantReset {
			t.Fatalf("unexpected primary: %+v (want ResetsAt=%d)", q.Primary, wantReset)
		}
		if q.Secondary == nil || q.Secondary.UsedPct != 18.0 || q.Secondary.ResetsAt != 1795500000 {
			t.Fatalf("unexpected secondary: %+v", q.Secondary)
		}
		if q.Estimated {
			t.Fatalf("expected Tier-1 quota to have Estimated=false")
		}
	})

	t.Run("tier2_5h_utc_block_estimation_fallback", func(t *testing.T) {
		t.Setenv("SPACES_CLAUDE_5H_TOKEN_LIMIT", "100000")

		tmp := t.TempDir()
		claudeDir := filepath.Join(tmp, ".claude")
		projDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Write a windowless Tier-1 file so we verify it does not shadow Tier-2.
		if err := os.WriteFile(filepath.Join(claudeDir, "spaces-rate-limits.json"), []byte(`{
			"source": "claude",
			"updated_at": 1999999999
		}`), 0o600); err != nil {
			t.Fatal(err)
		}

		now := time.Now().UTC()
		oldTS := now.Add(-7 * time.Hour)
		firstRecentTS := now.Add(-45 * time.Minute)
		secondRecentTS := now.Add(-10 * time.Minute)

		// Old message (7h ago) should roll over; recent messages sum active tokens = (10000+2000+3000) + (5000+1000+4000) = 25000 (25% of 100000).
		// Huge cache_read_input_tokens must NOT be counted in active tokens.
		lines := fmt.Sprintf(
			`{"type":"assistant","timestamp":%q,"message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":50000,"output_tokens":10000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n"+
				`{"type":"assistant","timestamp":%q,"message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":10000,"output_tokens":2000,"cache_creation_input_tokens":3000,"cache_read_input_tokens":900000}}}`+"\n"+
				`{"type":"assistant","timestamp":%q,"message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":5000,"output_tokens":1000,"cache_creation_input_tokens":4000,"cache_read_input_tokens":800000}}}`+"\n",
			oldTS.Format(time.RFC3339),
			firstRecentTS.Format(time.RFC3339),
			secondRecentTS.Format(time.RFC3339),
		)
		if err := os.WriteFile(filepath.Join(projDir, "sess-recent.jsonl"), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}

		qp := newClaudeQuotaProvider()
		q, err := qp.FetchQuota(context.Background(), []string{tmp})
		if err != nil {
			t.Fatalf("FetchQuota error: %v", err)
		}
		if q == nil {
			t.Fatal("expected Tier-2 estimated quota")
		}
		if q.Agent != "claude" || q.Source != "estimated_5h" || !q.Estimated {
			t.Fatalf("unexpected Tier-2 metadata: %+v", q)
		}
		if q.Secondary != nil {
			t.Fatalf("expected Secondary=nil for Tier-2 estimate, got %+v", q.Secondary)
		}
		wantBlockEnd := firstRecentTS.UTC().Truncate(time.Hour).Add(5 * time.Hour).Unix()
		if q.Primary == nil || q.Primary.Label != "5h" || q.Primary.WindowMinutes != 300 || math.Abs(q.Primary.UsedPct-25.0) > 0.01 || q.Primary.ResetsAt != wantBlockEnd {
			t.Fatalf("unexpected Tier-2 primary window: %+v (want pct=25.0, ResetsAt=%d)", q.Primary, wantBlockEnd)
		}
	})

	t.Run("tier2_returns_nil_when_no_recent_messages", func(t *testing.T) {
		tmp := t.TempDir()
		projDir := filepath.Join(tmp, ".claude", "projects", "-tmp-old")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		oldPath := filepath.Join(projDir, "sess-old.jsonl")
		oldTS := time.Now().UTC().Add(-8 * time.Hour)
		line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"usage":{"input_tokens":1000,"output_tokens":500}}}`+"\n", oldTS.Format(time.RFC3339))
		if err := os.WriteFile(oldPath, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(oldPath, oldTS, oldTS); err != nil {
			t.Fatal(err)
		}

		qp := newClaudeQuotaProvider()
		q, err := qp.FetchQuota(context.Background(), []string{tmp})
		if err != nil {
			t.Fatalf("FetchQuota error: %v", err)
		}
		if q != nil {
			t.Fatalf("expected nil quota when no messages in last 5h, got %+v", q)
		}
	})
}

func TestModelPricingIntegrationClaudeCodexOpenCode(t *testing.T) {
	t.Run("claude_pricing", func(t *testing.T) {
		tmp := t.TempDir()
		projDir := filepath.Join(tmp, ".claude", "projects", "-tmp-price")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		transcript := filepath.Join(projDir, "sess-price.jsonl")
		line := `{"type":"assistant","timestamp":"2026-04-01T10:00:05Z","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":100000,"output_tokens":20000,"cache_read_input_tokens":50000,"cache_creation_input_tokens":10000}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}

		p := newClaudeTelemetryProvider()
		m, err := p.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
			HomeDir:   tmp,
			OpenFiles: []string{transcript},
		})
		if err != nil || m == nil {
			t.Fatalf("collect error=%v m=%v", err, m)
		}
		wantCost, ok := agent.EstimateCostUSD("claude-sonnet-4-6", m.Usage)
		if !ok || !m.Usage.CostPriced || math.Abs(m.Usage.CostUSD-wantCost) > 1e-9 || m.Usage.CostUSD <= 0 {
			t.Fatalf("unexpected claude cost: CostUSD=%v CostPriced=%v want=%v", m.Usage.CostUSD, m.Usage.CostPriced, wantCost)
		}
	})

	t.Run("codex_pricing", func(t *testing.T) {
		tmp := t.TempDir()
		rolloutDir := filepath.Join(tmp, ".codex", "sessions", "2026", "04", "01")
		if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
			t.Fatal(err)
		}
		rollout := filepath.Join(rolloutDir, "rollout-2026-04-01-price.jsonl")
		body := `{"type":"session_meta","timestamp":"2026-04-01T12:00:00Z","payload":{"id":"sess-price","cwd":"/repo/price"}}` + "\n" +
			`{"type":"turn_context","timestamp":"2026-04-01T12:00:01Z","payload":{"model":"gpt-5-codex"}}` + "\n" +
			`{"type":"event_msg","timestamp":"2026-04-01T12:00:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000,"output_tokens":50000,"cached_input_tokens":100000}}}}` + "\n"
		if err := os.WriteFile(rollout, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		p := newCodexTelemetryProvider(&codexRateLimitStore{})
		m, err := p.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
			HomeDir: tmp,
			CWD:     "/repo/price",
		})
		if err != nil || m == nil {
			t.Fatalf("collect error=%v m=%v", err, m)
		}
		wantCost, ok := agent.EstimateCostUSD("gpt-5-codex", m.Usage)
		if !ok || !m.Usage.CostPriced || math.Abs(m.Usage.CostUSD-wantCost) > 1e-9 || m.Usage.CostUSD <= 0 {
			t.Fatalf("unexpected codex cost: CostUSD=%v CostPriced=%v want=%v", m.Usage.CostUSD, m.Usage.CostPriced, wantCost)
		}
	})

	t.Run("opencode_pricing_fallback_when_cost_missing", func(t *testing.T) {
		tmp := t.TempDir()
		dbDir := filepath.Join(tmp, ".local", "share", "opencode")
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(dbDir, "opencode.db")
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		schema := `
		CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, directory TEXT, version TEXT, time_created INTEGER, time_updated INTEGER);
		CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT);
		`
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO session VALUES ('oc-fallback', 'Fallback', '/repo/fb', '0.9.1', 1775000000000, 1775000010000)`); err != nil {
			t.Fatal(err)
		}
		msg, _ := json.Marshal(map[string]any{
			"role":       "assistant",
			"providerID": "anthropic",
			"modelID":    "claude-sonnet-4-6",
			"tokens": map[string]any{
				"input":  100000,
				"output": 20000,
				"cache":  map[string]any{"read": 50000, "write": 10000},
			},
		})
		if _, err := db.Exec(`INSERT INTO message VALUES ('m1', 'oc-fallback', 1775000005000, ?)`, string(msg)); err != nil {
			t.Fatal(err)
		}
		db.Close()

		p := newOpenCodeTelemetryProvider()
		m, err := p.CollectSessionMetrics(context.Background(), agent.SessionMetricsQuery{
			HomeDir: tmp,
			CWD:     "/repo/fb",
		})
		if err != nil || m == nil {
			t.Fatalf("collect error=%v m=%v", err, m)
		}
		wantCost, ok := agent.EstimateCostUSD(m.Model, m.Usage)
		if !ok || !m.Usage.CostPriced || math.Abs(m.Usage.CostUSD-wantCost) > 1e-9 || m.Usage.CostUSD <= 0 {
			t.Fatalf("unexpected opencode fallback cost: CostUSD=%v CostPriced=%v want=%v", m.Usage.CostUSD, m.Usage.CostPriced, wantCost)
		}
	})
}

package model

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func setupNativeConvStoreEnv(t *testing.T) (homeDir, overlayPath string) {
	t.Helper()
	homeDir = t.TempDir()
	overlayPath = filepath.Join(t.TempDir(), "data", "conversations-overlay.json")
	cachePath := filepath.Join(t.TempDir(), "cache", "conversations-cache.json")

	oldHome := ConversationHomeDirFn
	oldOverlay := ConversationOverlayPathFn
	oldCache := ConversationCachePathFn
	ConversationHomeDirFn = func() string { return homeDir }
	ConversationOverlayPathFn = func() string { return overlayPath }
	ConversationCachePathFn = func() string { return cachePath }
	ResetConversationStoreCache()

	// Ensure no subprocess is ever spawned by native conversation store APIs.
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("unexpected subprocess execution: %s %v", name, args)
		return nil, errors.New("unexpected exec")
	})

	t.Cleanup(func() {
		ConversationHomeDirFn = oldHome
		ConversationOverlayPathFn = oldOverlay
		ConversationCachePathFn = oldCache
		ResetConversationStoreCache()
	})
	return homeDir, overlayPath
}

func writeConvTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestConversationEntryHelpers(t *testing.T) {
	c := ConversationEntry{
		Agent:        "claude",
		ID:           "sess-1",
		CWD:          "/home/u/dev/model-router/",
		CreatedAtMs:  1000,
		UpdatedAtMs:  2000,
		LastOpenedMs: 2500,
	}
	if got := c.DisplayApp(); got != "claude-code" {
		t.Errorf("DisplayApp() = %q, want claude-code", got)
	}
	if got := c.ProjectName(); got != "model-router" {
		t.Errorf("ProjectName() = %q, want model-router", got)
	}
	if got := c.EffectiveTimeMs(); got != 2500 {
		t.Errorf("EffectiveTimeMs() = %d, want 2500", got)
	}
	if got := c.ResumeSessionID(); got != "sess-1" {
		t.Errorf("ResumeSessionID() = %q, want sess-1", got)
	}
	c.ResumeTarget = "resume-99"
	if got := c.ResumeSessionID(); got != "resume-99" {
		t.Errorf("ResumeSessionID() with ResumeTarget = %q, want resume-99", got)
	}
	if c.IsEphemeralCWD() {
		t.Error("/home/u/dev/model-router should not be ephemeral")
	}

	for _, tmp := range []string{"/tmp", "/tmp/work", "/var/tmp/x", "/private/tmp/y"} {
		if !(ConversationEntry{CWD: tmp}).IsEphemeralCWD() {
			t.Errorf("%q should be ephemeral", tmp)
		}
	}
	if (ConversationEntry{CWD: "/tmp-real/proj"}).IsEphemeralCWD() {
		t.Error("/tmp-real/proj should not be ephemeral")
	}

	c2 := ConversationEntry{Agent: "codex", App: "codex", CreatedAtMs: 500}
	if got := c2.EffectiveTimeMs(); got != 500 {
		t.Errorf("EffectiveTimeMs fallback = %d, want 500", got)
	}
}

func TestFetchConversationsCachedRefreshArchivedAndProfiles(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)

	// Default home: Claude active session.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-a", "sess-claude-1.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-claude-1","cwd":"/home/user/dev/repo-a","summary":"Refactor gateway retries","timestamp":1700000001000}`,
		`{"type":"user","sessionId":"sess-claude-1","cwd":"/home/user/dev/repo-a","timestamp":1700000002000,"message":{"role":"user","model":"claude-opus-4-6","content":"investigate 429 retries"}}`,
		`{"type":"assistant","sessionId":"sess-claude-1","cwd":"/home/user/dev/repo-a","timestamp":1700000005000,"message":{"role":"assistant","model":"claude-opus-4-6","content":"added exponential backoff"}}`,
	}, "\n")+"\n")

	// Harness profile root: Claude "work" profile session.
	writeConvTestFile(t, filepath.Join(homeDir, ".harness", "profiles", "claude", "work", "home", ".claude", "projects", "repo-b", "sess-claude-work.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-claude-work","cwd":"/home/user/dev/repo-b","summary":"Work profile billing fix","timestamp":1700000010000}`,
		`{"type":"user","sessionId":"sess-claude-work","cwd":"/home/user/dev/repo-b","timestamp":1700000012000,"message":{"role":"user","content":"fix invoice rounding"}}`,
	}, "\n")+"\n")

	// Default home: Codex archived session.
	writeConvTestFile(t, filepath.Join(homeDir, ".codex", "archived_sessions", "2026", "rollout-sess-codex-arch.jsonl"), strings.Join([]string{
		`{"type":"session_meta","timestamp":1700000003000,"payload":{"id":"sess-codex-arch","cwd":"/home/user/dev/repo-c","model":"gpt-5-codex"}}`,
		`{"type":"response_item","timestamp":1700000004000,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Old archived task"}]}}`,
	}, "\n")+"\n")

	// First call populates cache; includeArchived=false filters out archived session.
	snap, err := FetchConversations(context.Background(), false, false)
	if err != nil {
		t.Fatalf("FetchConversations initial error: %v", err)
	}
	if !snap.OK || snap.SyncedMs == 0 || snap.IndexedMessages < 3 {
		t.Fatalf("unexpected snapshot metadata: %+v", snap)
	}
	if len(snap.Conversations) != 2 {
		t.Fatalf("got %d active conversations, want 2: %+v", len(snap.Conversations), snap.Conversations)
	}
	// Sorted newest first: sess-claude-work (1700000012000) then sess-claude-1 (1700000005000).
	if snap.Conversations[0].ID != "sess-claude-work" || snap.Conversations[0].Profile != "work" || snap.Conversations[0].App != "claude-code" {
		t.Errorf("conv[0] = %+v, want sess-claude-work with profile work", snap.Conversations[0])
	}
	if snap.Conversations[1].ID != "sess-claude-1" || snap.Conversations[1].Title != "Refactor gateway retries" || snap.Conversations[1].Profile != "" {
		t.Errorf("conv[1] = %+v", snap.Conversations[1])
	}

	// Write a brand-new session on disk to verify cached vs refresh behavior.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-a", "sess-claude-new.jsonl"), strings.Join([]string{
		`{"type":"user","sessionId":"sess-claude-new","cwd":"/home/user/dev/repo-a","timestamp":1700000099000,"message":{"role":"user","content":"Brand new session"}}`,
	}, "\n")+"\n")

	// Cached call (refresh=false, includeArchived=true) must NOT rescan disk,
	// so it sees the archived session from cache but NOT sess-claude-new yet.
	cachedArch, err := FetchConversations(context.Background(), false, true)
	if err != nil {
		t.Fatalf("FetchConversations cached archived error: %v", err)
	}
	if len(cachedArch.Conversations) != 3 {
		t.Fatalf("cached archived len = %d, want 3: %+v", len(cachedArch.Conversations), cachedArch.Conversations)
	}
	foundArchived := false
	for _, c := range cachedArch.Conversations {
		if c.ID == "sess-claude-new" {
			t.Errorf("cached fetch should not have rescanned disk for sess-claude-new")
		}
		if c.ID == "sess-codex-arch" && c.Archived && c.Agent == "codex" {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Errorf("expected archived codex session in cachedArch: %+v", cachedArch.Conversations)
	}

	// Refresh call rescans disk and picks up sess-claude-new at the top.
	refreshed, err := FetchConversations(context.Background(), true, true)
	if err != nil {
		t.Fatalf("FetchConversations refresh error: %v", err)
	}
	if len(refreshed.Conversations) != 4 {
		t.Fatalf("refreshed len = %d, want 4: %+v", len(refreshed.Conversations), refreshed.Conversations)
	}
	if refreshed.Conversations[0].ID != "sess-claude-new" {
		t.Errorf("newest refreshed conv = %q, want sess-claude-new", refreshed.Conversations[0].ID)
	}
}

func TestSearchConversationsNative(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)

	longUserMsg := strings.Repeat("context padding words ", 8) +
		"hit the 429 rate limit backoff when switching providers " +
		strings.Repeat("more trailing details ", 8)

	// Claude session with title, user message, and assistant-only term.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-a", "sess-claude-search.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-claude-search","cwd":"/repo/alpha","summary":"Gateway fixes","timestamp":1700000100000}`,
		`{"type":"user","sessionId":"sess-claude-search","cwd":"/repo/alpha","timestamp":1700000101000,"message":{"role":"user","content":"` + longUserMsg + `"}}`,
		`{"type":"assistant","sessionId":"sess-claude-search","cwd":"/repo/alpha","timestamp":1700000102000,"message":{"role":"assistant","content":"Implemented jittered exponentialRetryQueue in router"}}`,
	}, "\n")+"\n")

	// Codex session in a different CWD matching title.
	writeConvTestFile(t, filepath.Join(homeDir, ".codex", "sessions", "2026", "rollout-sess-codex-search.jsonl"), strings.Join([]string{
		`{"type":"session_meta","timestamp":1700000105000,"payload":{"id":"sess-codex-search","cwd":"/repo/beta","title":"Gateway fixes for codex"}}`,
		`{"type":"response_item","timestamp":1700000106000,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check codex gateway"}]}}`,
	}, "\n")+"\n")

	// Archived Claude session.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-a", "sess-claude-archived.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-claude-archived","cwd":"/repo/alpha","summary":"Archived gateway note","archived":true,"timestamp":1700000090000}`,
		`{"type":"user","sessionId":"sess-claude-archived","cwd":"/repo/alpha","timestamp":1700000091000,"message":{"role":"user","content":"old rate limit backoff discussion"}}`,
	}, "\n")+"\n")

	// 1. Empty query short-circuits cleanly.
	emptyResp, err := SearchConversations(context.Background(), ConversationSearchRequest{Query: "   "})
	if err != nil || !emptyResp.OK || len(emptyResp.Hits) != 0 {
		t.Fatalf("empty search: resp=%+v err=%v", emptyResp, err)
	}

	// 2. Scope "titles": matches title metadata, does NOT match message-only terms.
	titleResp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query: "Gateway fixes",
		Scope: "titles",
	})
	if err != nil {
		t.Fatalf("titles search error: %v", err)
	}
	if !titleResp.OK || len(titleResp.Hits) != 2 {
		t.Fatalf("titles search got %d hits, want 2: %+v", len(titleResp.Hits), titleResp.Hits)
	}
	for _, hit := range titleResp.Hits {
		if hit.MatchKind != "metadata" {
			t.Errorf("expected metadata match kind, got %+v", hit)
		}
	}
	noMsgInTitles, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query: "rate limit backoff",
		Scope: "titles",
	})
	if err != nil || len(noMsgInTitles.Hits) != 0 {
		t.Fatalf("titles scope should not match body text: resp=%+v err=%v", noMsgInTitles, err)
	}

	// 3. Scope "user": matches user message with centered snippet & exact highlight spans,
	//    and excludes assistant-only terms.
	userResp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query: "rate limit backoff",
		Agent: "claude-code", // alias should canonicalize to "claude"
		CWD:   "/repo/alpha/",
		Scope: "user",
	})
	if err != nil {
		t.Fatalf("user search error: %v", err)
	}
	if !userResp.OK || len(userResp.Hits) != 1 {
		t.Fatalf("user search got %d hits, want 1: %+v", len(userResp.Hits), userResp.Hits)
	}
	hit := userResp.Hits[0]
	if hit.Conversation.ID != "sess-claude-search" || hit.MatchKind != "message" || hit.MatchRole != "user" {
		t.Fatalf("unexpected user hit: %+v", hit)
	}
	if len(hit.SnippetHighlights) == 0 {
		t.Fatalf("expected non-empty SnippetHighlights for snippet %q", hit.Snippet)
	}
	snippetRunes := []rune(hit.Snippet)
	if len(snippetRunes) > maxConvSnippetRunes+2 {
		t.Errorf("snippet rune len = %d, want <= %d: %q", len(snippetRunes), maxConvSnippetRunes+2, hit.Snippet)
	}
	sp := hit.SnippetHighlights[0]
	if sp.Start < 0 || sp.End > len(snippetRunes) || sp.Start >= sp.End {
		t.Fatalf("invalid highlight span %+v for snippet len %d (%q)", sp, len(snippetRunes), hit.Snippet)
	}
	gotHighlight := strings.ToLower(string(snippetRunes[sp.Start:sp.End]))
	if gotHighlight != "rate limit backoff" {
		t.Errorf("highlighted text = %q, want %q (snippet=%q)", gotHighlight, "rate limit backoff", hit.Snippet)
	}

	userNoAssistant, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query: "exponentialRetryQueue",
		Scope: "user",
	})
	if err != nil || len(userNoAssistant.Hits) != 0 {
		t.Fatalf("user scope should not match assistant message: resp=%+v err=%v", userNoAssistant, err)
	}

	// 4. Scope "all" (default): matches assistant message.
	allResp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query: "exponentialRetryQueue",
	})
	if err != nil {
		t.Fatalf("all search error: %v", err)
	}
	if !allResp.OK || allResp.Scope != "all" || len(allResp.Hits) != 1 {
		t.Fatalf("all search resp = %+v", allResp)
	}
	if allResp.Hits[0].MatchKind != "message" || allResp.Hits[0].MatchRole != "assistant" {
		t.Errorf("expected assistant message hit, got %+v", allResp.Hits[0])
	}

	// 5. IncludeArchived + Limit.
	archResp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query:           "rate limit backoff",
		Scope:           "user",
		IncludeArchived: true,
	})
	if err != nil || len(archResp.Hits) != 2 {
		t.Fatalf("archived search got %d hits, want 2: err=%v hits=%+v", len(archResp.Hits), err, archResp.Hits)
	}
	limitedResp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query:           "rate limit backoff",
		Scope:           "user",
		IncludeArchived: true,
		Limit:           1,
	})
	if err != nil || len(limitedResp.Hits) != 1 {
		t.Fatalf("limited search got %d hits, want 1: err=%v", len(limitedResp.Hits), err)
	}
}

func TestRecordConversationOpenAndOverlayMerge(t *testing.T) {
	homeDir, overlayPath := setupNativeConvStoreEnv(t)

	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-a", "sess-42.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-42","cwd":"/repo","summary":"Refactor retries","timestamp":1700000100000}`,
		`{"type":"user","sessionId":"sess-42","cwd":"/repo","timestamp":1700000101000,"message":{"role":"user","content":"refactor retries"}}`,
	}, "\n")+"\n")

	// Prime cache before recording open.
	before, err := FetchConversations(context.Background(), false, false)
	if err != nil || len(before.Conversations) != 1 {
		t.Fatalf("initial fetch: err=%v convs=%+v", err, before.Conversations)
	}
	if before.Conversations[0].Space != "" || before.Conversations[0].LastOpenedMs != 0 {
		t.Fatalf("expected empty overlay initially: %+v", before.Conversations[0])
	}

	// Record open event.
	err = RecordConversationOpen(context.Background(), ConversationEntry{
		Agent:   "claude-code",
		ID:      "sess-42",
		Profile: "work",
		CWD:     "/repo",
		Title:   "Refactor retries",
	}, "alpha", 1700000999000)
	if err != nil {
		t.Fatalf("RecordConversationOpen error: %v", err)
	}

	// Verify persisted SQLite overlay DB on disk (mapped from legacy .json path to .db).
	dbPath := strings.TrimSuffix(overlayPath, ".json") + ".db"
	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat overlay db %s: %v", dbPath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("overlay db permissions = %o, want 0600", perm)
	}
	loadedOverlay := loadConversationOverlay(overlayPath)
	ov, ok := loadedOverlay["claude:sess-42"]
	if !ok {
		t.Fatalf("overlay missing claude:sess-42: %+v", loadedOverlay)
	}
	if ov.Space != "alpha" || ov.LastOpenedMs != 1700000999000 || ov.Profile != "work" {
		t.Errorf("unexpected overlay entry: %+v", ov)
	}

	// Subsequent cached fetch immediately sees merged Space & LastOpenedMs.
	cached, err := FetchConversations(context.Background(), false, false)
	if err != nil || len(cached.Conversations) != 1 {
		t.Fatalf("cached fetch after record: err=%v convs=%+v", err, cached.Conversations)
	}
	if cached.Conversations[0].Space != "alpha" || cached.Conversations[0].LastOpenedMs != 1700000999000 || cached.Conversations[0].Profile != "work" {
		t.Errorf("cached conv missing overlay fields: %+v", cached.Conversations[0])
	}

	// Full rescan after cache reset reloads overlay from disk and merges it.
	ResetConversationStoreCache()
	reloaded, err := FetchConversations(context.Background(), true, false)
	if err != nil || len(reloaded.Conversations) != 1 {
		t.Fatalf("reloaded fetch: err=%v convs=%+v", err, reloaded.Conversations)
	}
	if reloaded.Conversations[0].Space != "alpha" || reloaded.Conversations[0].LastOpenedMs != 1700000999000 {
		t.Errorf("reloaded conv missing overlay fields: %+v", reloaded.Conversations[0])
	}
}

func TestFetchConversationTitlesNative(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)

	// Older Claude conversation in /repo/shared.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "shared", "sess-old.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-old","cwd":"/repo/shared","summary":"Older shared task","timestamp":1700000100000}`,
		`{"type":"user","sessionId":"sess-old","cwd":"/repo/shared","timestamp":1700000101000,"message":{"role":"user","content":"old"}}`,
	}, "\n")+"\n")

	// Newer Claude conversation in /repo/shared.
	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "shared", "sess-new.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-new","cwd":"/repo/shared","summary":"Newest shared task","timestamp":1700000200000}`,
		`{"type":"user","sessionId":"sess-new","cwd":"/repo/shared","timestamp":1700000201000,"message":{"role":"user","content":"new"}}`,
	}, "\n")+"\n")

	// Codex conversation in /repo/codex.
	writeConvTestFile(t, filepath.Join(homeDir, ".codex", "sessions", "2026", "rollout-sess-codex-title.jsonl"), strings.Join([]string{
		`{"type":"session_meta","timestamp":1700000300000,"payload":{"id":"sess-codex-title","cwd":"/repo/codex","title":"Codex rollout title"}}`,
		`{"type":"response_item","timestamp":1700000301000,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello codex"}]}}`,
	}, "\n")+"\n")

	// Empty queries returns nil cleanly.
	emptyTitles, err := FetchConversationTitles(context.Background(), nil)
	if err != nil || emptyTitles != nil {
		t.Fatalf("empty titles: got=%v err=%v", emptyTitles, err)
	}

	titles, err := FetchConversationTitles(context.Background(), []TitleQuery{
		{Key: "pane-exact", Agent: "claude-code", CWD: "/repo/shared", SessionID: "sess-old"},
		{Key: "pane-cwd", Agent: "claude", CWD: "/repo/shared/"},
		{Key: "pane-codex", Agent: "codex", CWD: "/repo/codex"},
		{Key: "pane-missing", Agent: "claude", CWD: "/repo/does-not-exist"},
	})
	if err != nil {
		t.Fatalf("FetchConversationTitles error: %v", err)
	}
	if titles["pane-exact"] != "Older shared task" {
		t.Errorf("pane-exact = %q, want %q", titles["pane-exact"], "Older shared task")
	}
	if titles["pane-cwd"] != "Newest shared task" {
		t.Errorf("pane-cwd = %q, want %q", titles["pane-cwd"], "Newest shared task")
	}
	if titles["pane-codex"] != "Codex rollout title" {
		t.Errorf("pane-codex = %q, want %q", titles["pane-codex"], "Codex rollout title")
	}
	if _, exists := titles["pane-missing"]; exists {
		t.Errorf("pane-missing should not be present: %+v", titles)
	}
}

func TestFetchConversationTitlesLiveRefreshOverStaleDiskCache(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)

	existingPath := filepath.Join(homeDir, ".claude", "projects", "repo-live", "sess-existing.jsonl")
	writeConvTestFile(t, existingPath, strings.Join([]string{
		`{"type":"user","sessionId":"sess-existing","cwd":"/repo/live","timestamp":1700000100000,"message":{"role":"user","content":"Initial raw user prompt"}}`,
	}, "\n")+"\n")

	// Populate conversations-cache.json with the initial state (simulating an earlier mod+y open).
	if _, err := FetchConversations(context.Background(), true, false); err != nil {
		t.Fatalf("initial FetchConversations error: %v", err)
	}

	// Now simulate the agent generating a real title for sess-existing AND starting a brand-new
	// conversation sess-brand-new on disk, without reopening mod+y.
	writeConvTestFile(t, existingPath, strings.Join([]string{
		`{"type":"user","sessionId":"sess-existing","cwd":"/repo/live","timestamp":1700000100000,"message":{"role":"user","content":"Initial raw user prompt"}}`,
		`{"type":"summary","sessionId":"sess-existing","cwd":"/repo/live","summary":"Generated AI Title","timestamp":1700000105000}`,
	}, "\n")+"\n")
	newPath := filepath.Join(homeDir, ".claude", "projects", "repo-live", "sess-brand-new.jsonl")
	writeConvTestFile(t, newPath, strings.Join([]string{
		`{"type":"summary","sessionId":"sess-brand-new","cwd":"/repo/live","summary":"Brand New Live Chat","timestamp":1700000200000}`,
		`{"type":"user","sessionId":"sess-brand-new","cwd":"/repo/live","timestamp":1700000201000,"message":{"role":"user","content":"hello"}}`,
	}, "\n")+"\n")

	// Simulate a fresh popup process (empty in-memory cache, stale conversations-cache.json on disk).
	ResetConversationStoreCache()

	titles, err := FetchConversationTitles(context.Background(), []TitleQuery{
		{Key: "pane-existing", Agent: "claude-code", CWD: "/repo/live", SessionID: "sess-existing"},
		{Key: "pane-new", Agent: "claude-code", CWD: "/repo/live", SessionID: "sess-brand-new"},
	})
	if err != nil {
		t.Fatalf("FetchConversationTitles error: %v", err)
	}
	if titles["pane-existing"] != "Generated AI Title" {
		t.Errorf("pane-existing = %q, want %q", titles["pane-existing"], "Generated AI Title")
	}
	if titles["pane-new"] != "Brand New Live Chat" {
		t.Errorf("pane-new = %q, want %q", titles["pane-new"], "Brand New Live Chat")
	}
}

func TestConversationDiskCacheInstantReload(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)
	cachePath := ConversationCachePathFn()

	jsonlPath := filepath.Join(homeDir, ".claude", "projects", "repo-cache", "sess-disk-cache.jsonl")
	writeConvTestFile(t, jsonlPath, strings.Join([]string{
		`{"type":"summary","sessionId":"sess-disk-cache","cwd":"/repo/cache","summary":"Cached disk snapshot task","timestamp":1700000500000}`,
		`{"type":"user","sessionId":"sess-disk-cache","cwd":"/repo/cache","timestamp":1700000501000,"message":{"role":"user","content":"persist snapshot to disk"}}`,
	}, "\n")+"\n")

	// 1. FetchConversations(ctx, true, false) scans disk and writes conversations-cache.json.
	snap1, err := FetchConversations(context.Background(), true, false)
	if err != nil {
		t.Fatalf("FetchConversations refresh error: %v", err)
	}
	if !snap1.OK || snap1.SyncedMs == 0 || len(snap1.Conversations) != 1 {
		t.Fatalf("unexpected initial refresh snapshot: %+v", snap1)
	}
	if snap1.Conversations[0].ID != "sess-disk-cache" {
		t.Fatalf("got ID %q, want sess-disk-cache", snap1.Conversations[0].ID)
	}

	fi, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("expected disk cache file at %s: %v", cachePath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file permissions = %o, want 0600", perm)
	}

	// Record an overlay open event after the disk cache was written so we can verify
	// that reloading from disk cache merges the latest overlay.
	if err := RecordConversationOpen(context.Background(), ConversationEntry{
		Agent: "claude",
		ID:    "sess-disk-cache",
		CWD:   "/repo/cache",
		Title: "Cached disk snapshot task",
	}, "dev-space", 1700000999000); err != nil {
		t.Fatalf("RecordConversationOpen error: %v", err)
	}

	// 2. Delete the source JSONL file and reset the in-memory cache.
	if err := os.Remove(jsonlPath); err != nil {
		t.Fatalf("remove source jsonl: %v", err)
	}
	ResetConversationStoreCache()

	// 3. FetchConversations(ctx, false, false) loads instantaneously from conversations-cache.json
	//    without scanning plugins and merges the latest overlay.
	snap2, err := FetchConversations(context.Background(), false, false)
	if err != nil {
		t.Fatalf("FetchConversations disk cache reload error: %v", err)
	}
	if !snap2.OK || snap2.SyncedMs != snap1.SyncedMs || len(snap2.Conversations) != 1 {
		t.Fatalf("unexpected disk-cached snapshot: %+v (want synced_ms=%d, 1 conv)", snap2, snap1.SyncedMs)
	}
	got := snap2.Conversations[0]
	if got.ID != "sess-disk-cache" || got.Title != "Cached disk snapshot task" {
		t.Errorf("unexpected cached conversation: %+v", got)
	}
	if got.Space != "dev-space" || got.LastOpenedMs != 1700000999000 {
		t.Errorf("disk-cached conversation missing merged overlay: space=%q last_opened_ms=%d", got.Space, got.LastOpenedMs)
	}

	// Also verify FetchConversationTitles uses disk cache when in-memory cache is empty.
	ResetConversationStoreCache()
	titles, err := FetchConversationTitles(context.Background(), []TitleQuery{
		{Key: "pane-1", Agent: "claude", CWD: "/repo/cache", SessionID: "sess-disk-cache"},
	})
	if err != nil {
		t.Fatalf("FetchConversationTitles disk cache error: %v", err)
	}
	if titles["pane-1"] != "Cached disk snapshot task" {
		t.Errorf("FetchConversationTitles = %q, want %q", titles["pane-1"], "Cached disk snapshot task")
	}

	// 4. Subsequent FetchConversations(ctx, true, false) rescans disk and updates conversations-cache.json.
	snap3, err := FetchConversations(context.Background(), true, false)
	if err != nil {
		t.Fatalf("FetchConversations second refresh error: %v", err)
	}
	if !snap3.OK || len(snap3.Conversations) != 0 {
		t.Fatalf("expected 0 conversations after source deletion + refresh, got: %+v", snap3)
	}

	// Verify the disk cache itself was updated to reflect the rescan.
	ResetConversationStoreCache()
	snap4, err := FetchConversations(context.Background(), false, false)
	if err != nil {
		t.Fatalf("FetchConversations post-refresh disk cache reload error: %v", err)
	}
	if !snap4.OK || snap4.SyncedMs != snap3.SyncedMs || len(snap4.Conversations) != 0 {
		t.Fatalf("expected updated empty disk cache (synced_ms=%d), got: %+v", snap3.SyncedMs, snap4)
	}
}

func TestFetchConversationsSkipsProfileStoresSymlinkedToRealHome(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)

	// Shared Claude store in realHome.
	realClaudeProjects := filepath.Join(homeDir, ".claude", "projects")
	writeConvTestFile(t, filepath.Join(realClaudeProjects, "repo-a", "sess-shared-claude.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-shared-claude","cwd":"/repo/a","summary":"Shared claude chat","timestamp":1700000100000}`,
		`{"type":"user","sessionId":"sess-shared-claude","cwd":"/repo/a","timestamp":1700000101000,"message":{"role":"user","content":"hello shared"}}`,
	}, "\n")+"\n")

	// Profile "alpha" symlinks .claude/projects -> realHome/.claude/projects.
	alphaClaudeDir := filepath.Join(homeDir, ".harness", "profiles", "claude", "alpha", "home", ".claude")
	if err := os.MkdirAll(alphaClaudeDir, 0o755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.Symlink(realClaudeProjects, filepath.Join(alphaClaudeDir, "projects")); err != nil {
		t.Fatalf("symlink alpha projects: %v", err)
	}

	// Profile "beta" symlinks its entire home directory -> realHome.
	betaProfileDir := filepath.Join(homeDir, ".harness", "profiles", "claude", "beta")
	if err := os.MkdirAll(betaProfileDir, 0o755); err != nil {
		t.Fatalf("mkdir beta: %v", err)
	}
	if err := os.Symlink(homeDir, filepath.Join(betaProfileDir, "home")); err != nil {
		t.Fatalf("symlink beta home: %v", err)
	}

	// Profile "isolated" has its own real (non-symlinked) .claude/projects store.
	writeConvTestFile(t, filepath.Join(homeDir, ".harness", "profiles", "claude", "isolated", "home", ".claude", "projects", "repo-b", "sess-isolated.jsonl"), strings.Join([]string{
		`{"type":"summary","sessionId":"sess-isolated","cwd":"/repo/b","summary":"Isolated profile chat","timestamp":1700000200000}`,
		`{"type":"user","sessionId":"sess-isolated","cwd":"/repo/b","timestamp":1700000201000,"message":{"role":"user","content":"hello isolated"}}`,
	}, "\n")+"\n")

	// Hybrid Codex profile: sessions symlinks to realHome, archived_sessions is private.
	realCodexSessions := filepath.Join(homeDir, ".codex", "sessions")
	writeConvTestFile(t, filepath.Join(realCodexSessions, "2026", "rollout-sess-shared-codex.jsonl"), strings.Join([]string{
		`{"type":"session_meta","timestamp":1700000300000,"payload":{"id":"sess-shared-codex","cwd":"/repo/c","title":"Shared codex chat"}}`,
		`{"type":"response_item","timestamp":1700000301000,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"shared codex"}]}}`,
	}, "\n")+"\n")

	hybridCodexDir := filepath.Join(homeDir, ".harness", "profiles", "codex", "hybrid", "home", ".codex")
	if err := os.MkdirAll(hybridCodexDir, 0o755); err != nil {
		t.Fatalf("mkdir hybrid codex: %v", err)
	}
	if err := os.Symlink(realCodexSessions, filepath.Join(hybridCodexDir, "sessions")); err != nil {
		t.Fatalf("symlink hybrid sessions: %v", err)
	}
	writeConvTestFile(t, filepath.Join(hybridCodexDir, "archived_sessions", "2026", "rollout-sess-private-codex.jsonl"), strings.Join([]string{
		`{"type":"session_meta","timestamp":1700000400000,"payload":{"id":"sess-private-codex","cwd":"/repo/d","title":"Private hybrid archived"}}`,
		`{"type":"response_item","timestamp":1700000401000,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"private archived"}]}}`,
	}, "\n")+"\n")

	snap, err := FetchConversations(context.Background(), true, true)
	if err != nil {
		t.Fatalf("FetchConversations error: %v", err)
	}

	byID := make(map[string]ConversationEntry, len(snap.Conversations))
	for _, c := range snap.Conversations {
		byID[c.ID] = c
	}

	if got, ok := byID["sess-shared-claude"]; !ok {
		t.Fatalf("missing sess-shared-claude in %+v", snap.Conversations)
	} else if got.Profile != "" {
		t.Errorf("sess-shared-claude.Profile = %q, want empty (must not inherit symlinked profile alpha/beta)", got.Profile)
	}

	if got, ok := byID["sess-isolated"]; !ok {
		t.Fatalf("missing sess-isolated in %+v", snap.Conversations)
	} else if got.Profile != "isolated" {
		t.Errorf("sess-isolated.Profile = %q, want isolated", got.Profile)
	}

	if got, ok := byID["sess-shared-codex"]; !ok {
		t.Fatalf("missing sess-shared-codex in %+v", snap.Conversations)
	} else if got.Profile != "" {
		t.Errorf("sess-shared-codex.Profile = %q, want empty (shared symlinked dir in hybrid profile)", got.Profile)
	}

	if got, ok := byID["sess-private-codex"]; !ok {
		t.Fatalf("missing sess-private-codex in %+v", snap.Conversations)
	} else if got.Profile != "hybrid" {
		t.Errorf("sess-private-codex.Profile = %q, want hybrid", got.Profile)
	}

	// Verify conversations-overlay.json can now supply a profile for shared chats
	// instead of being blocked by a bogus symlinked profile tag.
	if err := RecordConversationOpen(context.Background(), ConversationEntry{
		Agent:   "claude",
		ID:      "sess-shared-claude",
		Profile: "work",
	}, "dev", 1700000900000); err != nil {
		t.Fatalf("RecordConversationOpen error: %v", err)
	}

	snap2, err := FetchConversations(context.Background(), true, true)
	if err != nil {
		t.Fatalf("FetchConversations after overlay error: %v", err)
	}
	for _, c := range snap2.Conversations {
		if c.ID == "sess-shared-claude" {
			if c.Profile != "work" {
				t.Errorf("after overlay, sess-shared-claude.Profile = %q, want work", c.Profile)
			}
			return
		}
	}
	t.Fatalf("missing sess-shared-claude after overlay refresh")
}

func TestRecordConversationLaunchAndSQLiteRoundTrip(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)
	dbOverlayPath := filepath.Join(t.TempDir(), "data", "conversations-overlay.db")
	ConversationOverlayPathFn = func() string { return dbOverlayPath }

	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-launch", "sess-launch-1.jsonl"), strings.Join([]string{
		`{"type":"user","sessionId":"sess-launch-1","cwd":"/repo/launch","timestamp":1700000100000,"message":{"role":"user","content":"start launch test"}}`,
	}, "\n")+"\n")

	// Prime cache before recording launch.
	snap1, err := FetchConversations(context.Background(), false, false)
	if err != nil || len(snap1.Conversations) != 1 {
		t.Fatalf("initial fetch: err=%v convs=%+v", err, snap1.Conversations)
	}
	if len(snap1.Conversations[0].LaunchCmd) != 0 {
		t.Fatalf("expected empty LaunchCmd initially, got %v", snap1.Conversations[0].LaunchCmd)
	}

	launchCmd := []string{"harness", "run", "--profile", "work", "claude", "--resume", "sess-launch-1"}
	if err := RecordConversationLaunch(
		context.Background(),
		"claude-code",
		"sess-launch-1",
		"work",
		"/repo/launch",
		"Launch test title",
		launchCmd,
		"space-dev",
		1700000888000,
	); err != nil {
		t.Fatalf("RecordConversationLaunch error: %v", err)
	}

	// Mutating caller slice must not affect persisted or cached LaunchCmd.
	launchCmd[0] = "mutated"

	fi, err := os.Stat(dbOverlayPath)
	if err != nil {
		t.Fatalf("expected sqlite db at %s: %v", dbOverlayPath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("overlay db permissions = %o, want 0600", perm)
	}

	wantCmd := []string{"harness", "run", "--profile", "work", "claude", "--resume", "sess-launch-1"}

	// Cached fetch sees LaunchCmd and metadata immediately.
	cached, err := FetchConversations(context.Background(), false, false)
	if err != nil || len(cached.Conversations) != 1 {
		t.Fatalf("cached fetch after launch: err=%v convs=%+v", err, cached.Conversations)
	}
	gotCached := cached.Conversations[0]
	if !reflect.DeepEqual(gotCached.LaunchCmd, wantCmd) {
		t.Errorf("cached LaunchCmd = %v, want %v", gotCached.LaunchCmd, wantCmd)
	}
	if gotCached.Profile != "work" || gotCached.Space != "space-dev" || gotCached.LastOpenedMs != 1700000888000 {
		t.Errorf("cached conv missing launch overlay fields: %+v", gotCached)
	}

	// Full rescan after cache reset reloads LaunchCmd from SQLite.
	ResetConversationStoreCache()
	reloaded, err := FetchConversations(context.Background(), true, false)
	if err != nil || len(reloaded.Conversations) != 1 {
		t.Fatalf("reloaded fetch: err=%v convs=%+v", err, reloaded.Conversations)
	}
	gotReloaded := reloaded.Conversations[0]
	if !reflect.DeepEqual(gotReloaded.LaunchCmd, wantCmd) {
		t.Errorf("reloaded LaunchCmd = %v, want %v", gotReloaded.LaunchCmd, wantCmd)
	}
	if gotReloaded.Profile != "work" || gotReloaded.Space != "space-dev" || gotReloaded.LastOpenedMs != 1700000888000 {
		t.Errorf("reloaded conv missing launch overlay fields: %+v", gotReloaded)
	}
}

func TestConversationOverlayLegacyJSONMigration(t *testing.T) {
	homeDir, _ := setupNativeConvStoreEnv(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	legacyJSONPath := filepath.Join(dataDir, "conversations-overlay.json")
	dbPath := filepath.Join(dataDir, "conversations-overlay.db")
	ConversationOverlayPathFn = func() string { return dbPath }

	legacy := conversationOverlayFile{
		Version: 1,
		Entries: map[string]conversationOverlayEntry{
			"claude:sess-migrated": {
				Agent:        "claude-code",
				SessionID:    "sess-migrated",
				Space:        "legacy-space",
				LastOpenedMs: 1700000777000,
				LastOutcome:  "ok",
				Profile:      "personal",
				CWD:          "/repo/legacy",
				Title:        "Migrated session",
				LaunchCmd:    []string{"claude", "--resume", "sess-migrated"},
			},
		},
	}
	raw, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy json: %v", err)
	}
	writeConvTestFile(t, legacyJSONPath, string(raw))

	writeConvTestFile(t, filepath.Join(homeDir, ".claude", "projects", "repo-legacy", "sess-migrated.jsonl"), strings.Join([]string{
		`{"type":"user","sessionId":"sess-migrated","cwd":"/repo/legacy","timestamp":1700000100000,"message":{"role":"user","content":"migrated"}}`,
	}, "\n")+"\n")

	snap, err := FetchConversations(context.Background(), true, false)
	if err != nil || len(snap.Conversations) != 1 {
		t.Fatalf("fetch after migration: err=%v convs=%+v", err, snap.Conversations)
	}
	got := snap.Conversations[0]
	if got.Space != "legacy-space" || got.LastOpenedMs != 1700000777000 || got.LastOutcome != "ok" || got.Profile != "personal" {
		t.Errorf("migrated fields mismatch: %+v", got)
	}
	wantCmd := []string{"claude", "--resume", "sess-migrated"}
	if !reflect.DeepEqual(got.LaunchCmd, wantCmd) {
		t.Errorf("migrated LaunchCmd = %v, want %v", got.LaunchCmd, wantCmd)
	}

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected migrated sqlite db at %s: %v", dbPath, err)
	}

	// Verify migration is one-time: adding another entry to the legacy JSON after
	// SQLite is populated must not re-import it.
	legacy.Entries["claude:sess-after"] = conversationOverlayEntry{
		Agent:     "claude",
		SessionID: "sess-after",
		Space:     "should-not-import",
	}
	raw2, _ := json.Marshal(legacy)
	writeConvTestFile(t, legacyJSONPath, string(raw2))

	overlay := loadConversationOverlay(dbPath)
	if _, exists := overlay["claude:sess-after"]; exists {
		t.Errorf("legacy JSON was re-imported after SQLite was already populated: %+v", overlay)
	}
}

func TestConversationOverlayConcurrentUpsertsPreserveFields(t *testing.T) {
	_, _ = setupNativeConvStoreEnv(t)
	dbPath := filepath.Join(t.TempDir(), "data", "conversations-overlay.db")

	const workers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workers*2)

	wantCmd := []string{"harness", "run", "--profile", "work", "claude", "--resume", "sess-concurrent"}

	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func(iter int) {
			defer wg.Done()
			err := upsertConversationOverlaySQLite(dbPath, conversationOverlayEntry{
				Agent:       "claude",
				SessionID:   "sess-concurrent",
				Profile:     "work",
				CWD:         "/repo/concurrent",
				Title:       "Concurrent chat",
				LaunchCmd:   wantCmd,
				UpdatedAtMs: int64(1700001000000 + iter),
			})
			if err != nil {
				errCh <- err
			}
		}(i)
		go func(iter int) {
			defer wg.Done()
			err := upsertConversationOverlaySQLite(dbPath, conversationOverlayEntry{
				Agent:        "claude",
				SessionID:    "sess-concurrent",
				Space:        "space-alpha",
				LastOpenedMs: int64(1700002000000 + iter),
				LastOutcome:  "opened",
				UpdatedAtMs:  int64(1700002000000 + iter),
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent upsert error: %v", err)
	}

	overlay := loadConversationOverlay(dbPath)
	ov, ok := overlay["claude:sess-concurrent"]
	if !ok {
		t.Fatalf("missing claude:sess-concurrent in %+v", overlay)
	}
	if ov.Profile != "work" {
		t.Errorf("Profile = %q, want work", ov.Profile)
	}
	if !reflect.DeepEqual(ov.LaunchCmd, wantCmd) {
		t.Errorf("LaunchCmd = %v, want %v", ov.LaunchCmd, wantCmd)
	}
	if ov.CWD != "/repo/concurrent" {
		t.Errorf("CWD = %q, want /repo/concurrent", ov.CWD)
	}
	if ov.Title != "Concurrent chat" {
		t.Errorf("Title = %q, want Concurrent chat", ov.Title)
	}
	if ov.Space != "space-alpha" {
		t.Errorf("Space = %q, want space-alpha", ov.Space)
	}
	if ov.LastOutcome != "opened" {
		t.Errorf("LastOutcome = %q, want opened", ov.LastOutcome)
	}
	wantMaxOpened := int64(1700002000000 + workers - 1)
	if ov.LastOpenedMs != wantMaxOpened {
		t.Errorf("LastOpenedMs = %d, want %d", ov.LastOpenedMs, wantMaxOpened)
	}
	if ov.UpdatedAtMs != wantMaxOpened {
		t.Errorf("UpdatedAtMs = %d, want %d", ov.UpdatedAtMs, wantMaxOpened)
	}
}

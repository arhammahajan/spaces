package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

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

func TestFetchConversationsCachedAndRefresh(t *testing.T) {
	payload := `{
		"ok": true,
		"conversations": [
			{
				"agent": "claude",
				"id": "uuid-1",
				"app": "claude-code",
				"profile": "work",
				"title": "Refactor gateway retries",
				"preview": "investigate 429 retries",
				"cwd": "/home/user/dev/model-router",
				"branch": "feat/retry",
				"model": "claude-opus-4-6",
				"created_at_ms": 1700000000000,
				"updated_at_ms": 1700000100000,
				"message_count": 42,
				"archived": false,
				"resume_target": "uuid-1",
				"space": "work",
				"last_outcome": "done",
				"last_opened_ms": 1700000100000
			}
		],
		"synced_ms": 1700000100000,
		"indexed_messages": 12400,
		"pending_sources": 2
	}`

	var calls [][]string
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(payload), nil
	})

	snap, err := FetchConversations(context.Background(), false, false)
	if err != nil {
		t.Fatalf("FetchConversations cached error: %v", err)
	}
	if !snap.OK || len(snap.Conversations) != 1 || snap.IndexedMessages != 12400 || snap.PendingSources != 2 {
		t.Fatalf("unexpected snap: %+v", snap)
	}
	if snap.Conversations[0].Title != "Refactor gateway retries" || snap.Conversations[0].Profile != "work" {
		t.Fatalf("unexpected conv: %+v", snap.Conversations[0])
	}
	if got := strings.Join(calls[0], " "); !strings.Contains(got, "__space-conversations --cached-only") || strings.Contains(got, "--include-archived") {
		t.Errorf("cached args = %q", got)
	}

	_, err = FetchConversations(context.Background(), true, true)
	if err != nil {
		t.Fatalf("FetchConversations refresh error: %v", err)
	}
	if got := strings.Join(calls[1], " "); !strings.Contains(got, "__space-conversations --refresh --include-archived") {
		t.Errorf("refresh args = %q", got)
	}
}

func TestSearchConversations(t *testing.T) {
	payload := `{
		"ok": true,
		"query": "rate limit backoff",
		"scope": "user",
		"hits": [
			{
				"conversation": {
					"agent": "claude",
					"id": "uuid-2",
					"app": "claude-code",
					"title": "Gateway fixes",
					"updated_at_ms": 1700000100000
				},
				"match_kind": "message",
				"match_role": "user",
				"snippet": "…hit the 429 rate limit backoff when switching…",
				"snippet_highlights": [{"start": 13, "end": 31}],
				"score": 12.4
			}
		]
	}`

	var gotArgs []string
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(payload), nil
	})

	resp, err := SearchConversations(context.Background(), ConversationSearchRequest{
		Query:           "rate limit backoff",
		Agent:           "claude",
		CWD:             "/repo",
		Scope:           "user",
		IncludeArchived: true,
		Limit:           25,
	})
	if err != nil {
		t.Fatalf("SearchConversations error: %v", err)
	}
	if !resp.OK || len(resp.Hits) != 1 || resp.Hits[0].MatchKind != "message" || resp.Hits[0].MatchRole != "user" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if len(resp.Hits[0].SnippetHighlights) != 1 || resp.Hits[0].SnippetHighlights[0].Start != 13 || resp.Hits[0].SnippetHighlights[0].End != 31 {
		t.Fatalf("unexpected highlights: %+v", resp.Hits[0].SnippetHighlights)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"__space-conversations", "--search", "rate limit backoff", "--agent", "claude", "--cwd", "/repo", "--scope", "user", "--include-archived", "--limit", "25"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}

	// Empty query short-circuits without spawning subprocess.
	called := false
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
	})
	emptyResp, err := SearchConversations(context.Background(), ConversationSearchRequest{Query: "   "})
	if err != nil || !emptyResp.OK || called {
		t.Fatalf("empty search: resp=%+v err=%v called=%v", emptyResp, err, called)
	}
}

func TestRecordConversationOpen(t *testing.T) {
	var gotArgs []string
	stubExec(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{"ok":true}`), nil
	})
	err := RecordConversationOpen(context.Background(), ConversationEntry{
		Agent:   "claude-code",
		ID:      "sess-42",
		Profile: "work",
		CWD:     "/repo",
		Title:   "Refactor retries",
	}, "alpha", 1700000999000)
	if err != nil {
		t.Fatalf("RecordConversationOpen error: %v", err)
	}
	if len(gotArgs) < 3 {
		t.Fatalf("unexpected args: %v", gotArgs)
	}
	rawJSON := gotArgs[len(gotArgs)-1]
	var decoded map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &decoded); err != nil {
		t.Fatalf("invalid record json %q: %v", rawJSON, err)
	}
	if decoded["agent"] != "claude" || decoded["session_id"] != "sess-42" || decoded["space"] != "alpha" || decoded["profile"] != "work" {
		t.Errorf("unexpected decoded payload: %+v", decoded)
	}
}

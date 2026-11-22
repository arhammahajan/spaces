package model

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// ConversationEntry mirrors convstore.Conversation returned by
// `spaces __space-conversations`.
type ConversationEntry struct {
	Agent        string `json:"agent"`
	ID           string `json:"id"`
	App          string `json:"app"`
	Profile      string `json:"profile,omitempty"`
	Title        string `json:"title,omitempty"`
	Preview      string `json:"preview,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Model        string `json:"model,omitempty"`
	CreatedAtMs  int64  `json:"created_at_ms,omitempty"`
	UpdatedAtMs  int64  `json:"updated_at_ms"`
	MessageCount int    `json:"message_count,omitempty"`
	Archived     bool   `json:"archived,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	ResumeTarget string `json:"resume_target,omitempty"`

	Space        string `json:"space,omitempty"`
	LastOutcome  string `json:"last_outcome,omitempty"`
	LastOpenedMs int64  `json:"last_opened_ms,omitempty"`
}

// SnippetSpan marks a highlighted rune range [Start, End) inside
// ConversationSearchHit.Snippet.
type SnippetSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ConversationSearchHit mirrors convstore.SearchHit returned by
// `spaces __space-conversations --search`.
type ConversationSearchHit struct {
	Conversation      ConversationEntry `json:"conversation"`
	MatchKind         string            `json:"match_kind"` // "message" | "metadata"
	MatchRole         string            `json:"match_role,omitempty"`
	Snippet           string            `json:"snippet,omitempty"`
	SnippetHighlights []SnippetSpan     `json:"snippet_highlights,omitempty"`
	Score             float64           `json:"score"`
}

// ConversationSnapshot mirrors convstore.ListResponse returned by
// `spaces __space-conversations --cached-only` and `--refresh`.
type ConversationSnapshot struct {
	OK              bool                `json:"ok"`
	Conversations   []ConversationEntry `json:"conversations"`
	SyncedMs        int64               `json:"synced_ms"`
	IndexedMessages int                 `json:"indexed_messages"`
	PendingSources  int                 `json:"pending_sources"`
}

// ConversationSearchRequest holds arguments for `spaces __space-conversations --search`.
type ConversationSearchRequest struct {
	Query           string
	Agent           string
	CWD             string
	Scope           string // "all" (default) | "user" | "titles"
	IncludeArchived bool
	Limit           int
}

// ConversationSearchResponse mirrors convstore.SearchResponse returned by
// `spaces __space-conversations --search`.
type ConversationSearchResponse struct {
	OK    bool                    `json:"ok"`
	Query string                  `json:"query"`
	Scope string                  `json:"scope"`
	Hits  []ConversationSearchHit `json:"hits"`
}

// CanonicalConversationAgent normalizes shorthand agent aliases to canonical IDs,
// mirroring convstore.CanonicalAgent.
func CanonicalConversationAgent(name string) string {
	return agent.DefaultRegistry().ConversationID(name)
}

// DisplayApp returns the user-facing harness label for this conversation.
func (c ConversationEntry) DisplayApp() string {
	if a := strings.TrimSpace(c.App); a != "" {
		return a
	}
	return agent.DefaultRegistry().CLIDisplayApp(c.Agent)
}

// ProjectName returns the workspace directory basename for display and search.
func (c ConversationEntry) ProjectName() string {
	cwd := strings.TrimSpace(c.CWD)
	if cwd == "" {
		return ""
	}
	clean := filepath.Clean(cwd)
	if clean == "." || clean == "/" {
		return ""
	}
	base := filepath.Base(clean)
	if base == "." || base == "/" {
		return ""
	}
	return base
}

// EffectiveTimeMs returns the most relevant recency timestamp in Unix milliseconds,
// preferring the latest of UpdatedAtMs and LastOpenedMs with CreatedAtMs fallback.
func (c ConversationEntry) EffectiveTimeMs() int64 {
	t := c.UpdatedAtMs
	if c.LastOpenedMs > t {
		t = c.LastOpenedMs
	}
	if t <= 0 {
		t = c.CreatedAtMs
	}
	return t
}

// ResumeSessionID returns ResumeTarget when set, otherwise ID.
func (c ConversationEntry) ResumeSessionID() string {
	if target := strings.TrimSpace(c.ResumeTarget); target != "" {
		return target
	}
	return strings.TrimSpace(c.ID)
}

// IsEphemeralCWD reports whether the conversation's CWD points to a temporary
// directory (e.g. /tmp, /var/tmp, /private/tmp).
func (c ConversationEntry) IsEphemeralCWD() bool {
	cleaned := filepath.Clean(strings.TrimSpace(c.CWD))
	if cleaned == "" || cleaned == "." {
		return false
	}
	for _, prefix := range []string{"/tmp", "/var/tmp", "/private/tmp"} {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return true
		}
	}
	return false
}

// FetchConversations returns conversations from the native in-process plugin
// conversation store, using the cached snapshot unless refresh is true or the
// cache has not been populated yet.
func FetchConversations(ctx context.Context, refresh, includeArchived bool) (ConversationSnapshot, error) {
	return fetchConversationsNative(ctx, refresh, includeArchived)
}

// SearchConversations searches conversations in-process across metadata and
// plugin transcript logs according to req.Scope ("all", "user", or "titles").
func SearchConversations(ctx context.Context, req ConversationSearchRequest) (ConversationSearchResponse, error) {
	return searchConversationsNative(ctx, req)
}

// RecordConversationOpen persists an open event in conversations-overlay.json
// so the native conversation store tracks space and last_opened_ms.
func RecordConversationOpen(ctx context.Context, conv ConversationEntry, space string, nowMs int64) error {
	return recordConversationOpenNative(ctx, conv, space, nowMs)
}

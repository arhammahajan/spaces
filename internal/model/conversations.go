package model

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
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
	s := strings.ToLower(strings.TrimSpace(name))
	switch s {
	case "claude-code":
		return "claude"
	case "agy", "antigravity-cli":
		return "antigravity"
	case "dsh", "dsh-tui":
		return "deepseek"
	case "kimi-code":
		return "kimi"
	case "cursor":
		return "cursor-agent"
	case "fx-router":
		return "fx"
	case "open-code":
		return "opencode"
	case "qwen-code":
		return "qwen"
	case "kilo-code":
		return "kilo"
	case "vibe-acp":
		return "vibe"
	case "grok-build":
		return "grok"
	case "muse-code", "muse-cli":
		return "muse"
	case "github-copilot", "ghcs":
		return "copilot"
	default:
		return s
	}
}

// DisplayApp returns the user-facing harness label for this conversation.
func (c ConversationEntry) DisplayApp() string {
	if a := strings.TrimSpace(c.App); a != "" {
		return a
	}
	canon := CanonicalConversationAgent(c.Agent)
	switch canon {
	case "claude":
		return "claude-code"
	case "kimi-cli":
		return "kimi"
	default:
		return canon
	}
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

// FetchConversations runs `spaces __space-conversations --cached-only|--refresh`
// and parses the ConversationSnapshot.
func FetchConversations(ctx context.Context, refresh, includeArchived bool) (ConversationSnapshot, error) {
	args := []string{"__space-conversations"}
	if refresh {
		args = append(args, "--refresh")
	} else {
		args = append(args, "--cached-only")
	}
	if includeArchived {
		args = append(args, "--include-archived")
	}
	out, err := runSpaces(ctx, args...)
	if err != nil {
		return ConversationSnapshot{}, err
	}
	var snap ConversationSnapshot
	if err := decodeJSON(out, "__space-conversations", &snap); err != nil {
		return ConversationSnapshot{}, err
	}
	return snap, nil
}

// SearchConversations runs `spaces __space-conversations --search <query> ...`
// and parses the ConversationSearchResponse.
func SearchConversations(ctx context.Context, req ConversationSearchRequest) (ConversationSearchResponse, error) {
	q := strings.TrimSpace(req.Query)
	if q == "" {
		return ConversationSearchResponse{OK: true, Scope: req.Scope}, nil
	}
	args := []string{"__space-conversations", "--search", q}
	if agent := strings.TrimSpace(req.Agent); agent != "" {
		args = append(args, "--agent", agent)
	}
	if cwd := strings.TrimSpace(req.CWD); cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if scope := strings.TrimSpace(req.Scope); scope != "" {
		args = append(args, "--scope", scope)
	}
	if req.IncludeArchived {
		args = append(args, "--include-archived")
	}
	if req.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(req.Limit))
	}
	out, err := runSpaces(ctx, args...)
	if err != nil {
		return ConversationSearchResponse{}, err
	}
	var resp ConversationSearchResponse
	if err := decodeJSON(out, "__space-conversations --search", &resp); err != nil {
		return ConversationSearchResponse{}, err
	}
	return resp, nil
}

type conversationOpenRecord struct {
	Agent        string `json:"agent"`
	SessionID    string `json:"session_id"`
	Profile      string `json:"profile,omitempty"`
	Space        string `json:"space,omitempty"`
	CWD          string `json:"cwd,omitempty"`
	Title        string `json:"title,omitempty"`
	LastOpenedMs int64  `json:"last_opened_ms,omitempty"`
}

// RecordConversationOpen sends an open event to
// `spaces __space-conversations --record <json>` so the overlay tracks space
// and last_opened_ms.
func RecordConversationOpen(ctx context.Context, conv ConversationEntry, space string, nowMs int64) error {
	agent := CanonicalConversationAgent(conv.Agent)
	if agent == "" {
		agent = CanonicalConversationAgent(conv.App)
	}
	sid := strings.TrimSpace(conv.ID)
	if sid == "" {
		sid = conv.ResumeSessionID()
	}
	rec := conversationOpenRecord{
		Agent:        agent,
		SessionID:    sid,
		Profile:      strings.TrimSpace(conv.Profile),
		Space:        strings.TrimSpace(space),
		CWD:          strings.TrimSpace(conv.CWD),
		Title:        strings.TrimSpace(conv.Title),
		LastOpenedMs: nowMs,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = runSpaces(ctx, "__space-conversations", "--record", string(raw))
	return err
}

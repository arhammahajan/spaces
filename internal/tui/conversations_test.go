package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeConversationsBackend struct {
	fakeBackend
	cachedSnap  model.ConversationSnapshot
	refreshSnap model.ConversationSnapshot
	loadErr     error
	searchHits  []model.ConversationSearchHit
	searchReqs  []model.ConversationSearchRequest
	openedConvs []struct {
		conv        model.ConversationEntry
		forceNewTab bool
	}
	openConvErr error
	loadCalls   []struct {
		refresh         bool
		includeArchived bool
	}
}

func (f *fakeConversationsBackend) LoadConversations(_ context.Context, refresh, includeArchived bool) (model.ConversationSnapshot, error) {
	f.loadCalls = append(f.loadCalls, struct {
		refresh         bool
		includeArchived bool
	}{refresh: refresh, includeArchived: includeArchived})
	if f.loadErr != nil {
		return model.ConversationSnapshot{}, f.loadErr
	}
	if refresh {
		return f.refreshSnap, nil
	}
	return f.cachedSnap, nil
}

func (f *fakeConversationsBackend) SearchConversations(_ context.Context, req model.ConversationSearchRequest) ([]model.ConversationSearchHit, error) {
	f.searchReqs = append(f.searchReqs, req)
	return f.searchHits, nil
}

func (f *fakeConversationsBackend) OpenConversation(_ context.Context, conv model.ConversationEntry, forceNewTab bool) error {
	if f.openConvErr != nil {
		return f.openConvErr
	}
	f.openedConvs = append(f.openedConvs, struct {
		conv        model.ConversationEntry
		forceNewTab bool
	}{conv: conv, forceNewTab: forceNewTab})
	return nil
}

func sampleConversations(nowMs int64) []model.ConversationEntry {
	return []model.ConversationEntry{
		{
			Agent:        "claude",
			ID:           "sess-claude-1",
			App:          "claude-code",
			Profile:      "work",
			Title:        "Refactor gateway retries",
			Preview:      "investigate 429 retries",
			CWD:          "/home/u/dev/model-router",
			Branch:       "feat/retry",
			Model:        "claude-opus-4-6",
			UpdatedAtMs:  nowMs - 60_000,
			MessageCount: 42,
			Space:        "work",
			LastOutcome:  "done",
		},
		{
			Agent:        "codex",
			ID:           "sess-codex-1",
			App:          "codex",
			Title:        "Add SQLite FTS5 indexing",
			Preview:      "create messages_fts virtual table",
			CWD:          "/home/u/dev/model-router",
			Branch:       "feat/convstore",
			Model:        "gpt-5-codex",
			UpdatedAtMs:  nowMs - 120_000,
			MessageCount: 18,
		},
		{
			Agent:        "opencode",
			ID:           "sess-oc-1",
			App:          "opencode",
			Title:        "Build billing dashboard",
			CWD:          "/home/u/dev/billing",
			UpdatedAtMs:  nowMs - 300_000,
			MessageCount: 12,
		},
		{
			Agent:       "claude",
			ID:          "sess-archived",
			App:         "claude-code",
			Title:       "Old archived experiment",
			CWD:         "/home/u/dev/model-router",
			UpdatedAtMs: nowMs - 600_000,
			Archived:    true,
		},
		{
			Agent:       "codex",
			ID:          "sess-tmp",
			App:         "codex",
			Title:       "Throwaway tmp test",
			CWD:         "/tmp/test-run-123",
			UpdatedAtMs: nowMs - 30_000,
		},
	}
}

func TestConversationsStaleWhileRevalidateAndPreservesQuery(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	convs := sampleConversations(now.UnixMilli())
	fb := &fakeConversationsBackend{
		cachedSnap: model.ConversationSnapshot{
			OK:              true,
			Conversations:   convs[:2],
			IndexedMessages: 500,
		},
		refreshSnap: model.ConversationSnapshot{
			OK:              true,
			Conversations:   convs,
			IndexedMessages: 12400,
		},
	}
	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 28})

	// Cached snapshot arrives immediately.
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: false})
	if items := m.conversationItems(); len(items) != 2 {
		t.Fatalf("after cached load got %d items, want 2", len(items))
	}

	// User starts typing before --refresh completes.
	for _, r := range "billing" {
		m = update(m, keyRunes(string(r)))
	}
	if m.query != "billing" {
		t.Fatalf("query = %q, want billing", m.query)
	}
	if items := m.conversationItems(); len(items) != 0 {
		t.Fatalf("before refresh expected 0 matches for billing, got %d", len(items))
	}

	// Async --refresh arrives; must NOT reset query and must surface the newly synced chat.
	m = update(m, conversationsMsg{snap: fb.refreshSnap, refresh: true})
	if m.query != "billing" {
		t.Fatalf("refresh clobbered query: got %q", m.query)
	}
	items := m.conversationItems()
	if len(items) != 1 || items[0].Conv.ID != "sess-oc-1" {
		t.Fatalf("after refresh got %+v, want sess-oc-1", items)
	}
}

func TestConversationsHybridFuzzyAndDebouncedFTS5MergeAndStaleDiscard(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	convs := sampleConversations(now.UnixMilli())
	bodyOnlyConv := model.ConversationEntry{
		Agent:       "gemini",
		ID:          "sess-gem-body",
		App:         "gemini",
		Title:       "Unrelated morning notes",
		CWD:         "/home/u/dev/model-router",
		UpdatedAtMs: now.UnixMilli() - 90_000,
	}
	fb := &fakeConversationsBackend{
		cachedSnap: model.ConversationSnapshot{OK: true, Conversations: convs, IndexedMessages: 5000},
		searchHits: []model.ConversationSearchHit{
			{
				Conversation:      bodyOnlyConv,
				MatchKind:         "message",
				MatchRole:         "user",
				Snippet:           "…hit the 429 rate limit backoff when switching…",
				SnippetHighlights: []model.SnippetSpan{{Start: 13, End: 31}},
				Score:             12.4,
			},
		},
	}

	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 28})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	// Type "rate limit": bumped seq, local fuzzy runs immediately (0 matches), debounce tick fires FTS5.
	var lastCmd tea.Cmd
	for _, r := range "rate limit" {
		var u tea.Model
		if r == ' ' {
			u, lastCmd = m.Update(tea.KeyMsg{Type: tea.KeySpace})
		} else {
			u, lastCmd = m.Update(keyRunes(string(r)))
		}
		m = u.(Model)
	}
	if lastCmd == nil {
		t.Fatal("expected debounce tea.Cmd on query edit")
	}
	curSeq := m.convSearchSeq

	// Stale debounce tick must be ignored.
	u, cmd := m.Update(convSearchDebounceMsg{seq: curSeq - 1})
	m = u.(Model)
	if cmd != nil {
		t.Fatal("stale debounce tick should not trigger search")
	}

	// Matching debounce tick triggers SearchConversations.
	u, cmd = m.Update(convSearchDebounceMsg{seq: curSeq})
	m = u.(Model)
	if cmd == nil {
		t.Fatal("matching debounce tick should dispatch SearchConversations cmd")
	}
	resMsg := cmd()

	// Stale search result must be discarded.
	m = update(m, convSearchResultMsg{
		seq:  curSeq - 1,
		hits: []model.ConversationSearchHit{{Conversation: model.ConversationEntry{Agent: "claude", ID: "stale-id", Title: "Stale"}}},
	})
	if len(m.convSearchHits) != 0 {
		t.Fatalf("stale search result was not discarded: %+v", m.convSearchHits)
	}

	// Matching search result merges body-only conversation + snippet sub-line.
	m = update(m, resMsg)
	items := m.conversationItems()
	if len(items) != 1 || items[0].Conv.ID != "sess-gem-body" {
		t.Fatalf("merged items = %+v, want sess-gem-body", items)
	}
	if items[0].Hit == nil || items[0].Hit.MatchRole != "user" {
		t.Fatalf("expected attached FTS5 hit with role user, got %+v", items[0].Hit)
	}

	// Render must include snippet sub-line with role and text.
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "↳ user:") || !strings.Contains(plain, "rate limit backoff") {
		t.Errorf("rendered view missing snippet sub-line:\n%s", plain)
	}
}

func TestConversationsLivePaneDetectionEnterVsCtrlO(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	convs := sampleConversations(now.UnixMilli())
	fb := &fakeConversationsBackend{
		fakeBackend: fakeBackend{
			spaces: []model.Space{
				{
					Name:      "work",
					IsCurrent: true,
					CWD:       "/home/u/dev/model-router",
					Panes: []model.Pane{
						{
							ID:            "%7",
							NumericID:     7,
							WindowID:      "@3",
							App:           "claude-code",
							Status:        model.StatusWorking,
							ResumeSession: "sess-claude-1",
							CWD:           "/home/u/dev/model-router",
						},
					},
				},
			},
		},
		cachedSnap: model.ConversationSnapshot{OK: true, Conversations: convs},
	}

	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 28})
	m = update(m, spacesMsg{spaces: fb.spaces})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	items := m.conversationItems()
	if len(items) == 0 || items[0].Conv.ID != "sess-claude-1" || items[0].Live == nil {
		t.Fatalf("expected first item sess-claude-1 to be detected as live, got %+v", items)
	}

	// View shows live working glyph + space name.
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "◐") || !strings.Contains(plain, "live·work") {
		t.Errorf("expected live pane status in view:\n%s", plain)
	}

	// Enter on live conversation focuses the live tmux pane and quits.
	m1 := update(m, keyType(tea.KeyEnter))
	if !m1.Quitting() || len(fb.focused) != 1 || fb.focused[0] != "work/@3/%7" {
		t.Fatalf("Enter on live conv: quitting=%v focused=%v", m1.Quitting(), fb.focused)
	}
	if len(fb.openedConvs) != 0 {
		t.Fatalf("Enter on live conv should not open new tab: %+v", fb.openedConvs)
	}

	// Ctrl-o on live conversation forces OpenConversation(..., true) and quits.
	m2 := update(m, keyType(tea.KeyCtrlO))
	if !m2.Quitting() || len(fb.openedConvs) != 1 || !fb.openedConvs[0].forceNewTab || fb.openedConvs[0].conv.ID != "sess-claude-1" {
		t.Fatalf("Ctrl-o on live conv: quitting=%v opened=%+v", m2.Quitting(), fb.openedConvs)
	}
}

func TestConversationsFiltersKeybindingsAndTwoStageEsc(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	convs := sampleConversations(now.UnixMilli())
	fb := &fakeConversationsBackend{
		fakeBackend: fakeBackend{
			spaces: []model.Space{
				{Name: "work", IsCurrent: true, CWD: "/home/u/dev/model-router"},
			},
		},
		cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: convs},
		refreshSnap: model.ConversationSnapshot{OK: true, Conversations: convs},
	}

	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 28})
	m = update(m, spacesMsg{spaces: fb.spaces})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	// Archived and /tmp hidden by default => 3 visible chats.
	if got := len(m.conversationItems()); got != 3 {
		t.Fatalf("default visible = %d, want 3 (excluding archived & /tmp)", got)
	}

	// Ctrl-a toggles archived & /tmp on => 5 visible chats.
	u, cmd := m.Update(keyType(tea.KeyCtrlA))
	m = u.(Model)
	if !m.convIncludeArchived || cmd == nil {
		t.Fatalf("Ctrl-a should enable archived and trigger reload: archived=%v cmd=%v", m.convIncludeArchived, cmd != nil)
	}
	if got := len(m.conversationItems()); got != 5 {
		t.Fatalf("with archived visible = %d, want 5", got)
	}
	// Toggle archived back off.
	m = update(m, keyType(tea.KeyCtrlA))
	if got := len(m.conversationItems()); got != 3 {
		t.Fatalf("archived off visible = %d, want 3", got)
	}

	// Alt-c restricts to current workspace (/home/u/dev/model-router) => excludes billing (2 visible).
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
	if !m.convWorkspaceOnly {
		t.Fatal("Alt-c should enable workspace filter")
	}
	if got := len(m.conversationItems()); got != 2 {
		t.Fatalf("workspace-only visible = %d, want 2", got)
	}
	if m.query != "" {
		t.Fatalf("Alt-c must not type 'c' into query, got %q", m.query)
	}

	// Ctrl-s cycles scope: all -> titles -> user -> all.
	m = update(m, keyType(tea.KeyCtrlS))
	if m.convScope != "titles" {
		t.Fatalf("scope after 1st Ctrl-s = %q, want titles", m.convScope)
	}
	m = update(m, keyType(tea.KeyCtrlS))
	if m.convScope != "user" {
		t.Fatalf("scope after 2nd Ctrl-s = %q, want user", m.convScope)
	}

	// Tab cycles harness pills.
	m = update(m, keyType(tea.KeyTab))
	if m.convHarnessFilter == "" {
		t.Fatal("Tab should select first harness pill")
	}
	// Shift-Tab cycles back to all.
	m = update(m, keyType(tea.KeyShiftTab))
	if m.convHarnessFilter != "" {
		t.Fatalf("Shift-Tab should return to all, got %q", m.convHarnessFilter)
	}

	// First Esc clears active filters/query without quitting.
	m = update(m, keyType(tea.KeyEsc))
	if m.Quitting() {
		t.Fatal("first Esc with active filters should clear filters, not quit")
	}
	if m.convWorkspaceOnly || m.convScope != "all" || m.convHarnessFilter != "" {
		t.Fatalf("filters not cleared on first Esc: ws=%v scope=%q harness=%q", m.convWorkspaceOnly, m.convScope, m.convHarnessFilter)
	}

	// Second Esc quits cleanly.
	m = update(m, keyType(tea.KeyEsc))
	if !m.Quitting() {
		t.Fatal("second Esc with no filters should quit")
	}
}

func TestConversationsMouseClickOpensRow(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	convs := sampleConversations(now.UnixMilli())
	fb := &fakeConversationsBackend{
		cachedSnap: model.ConversationSnapshot{OK: true, Conversations: convs},
	}
	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 28})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	layout := m.convLayout(120, 28)
	// Click row index 1 (second conversation: sess-codex-1).
	m = update(m, tea.MouseMsg{
		X:      10,
		Y:      layout.topY + 1,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if !m.Quitting() {
		t.Fatal("left-click on conversation row should open and quit")
	}
	if len(fb.openedConvs) != 1 || fb.openedConvs[0].conv.ID != "sess-codex-1" {
		t.Fatalf("openedConvs = %+v, want sess-codex-1", fb.openedConvs)
	}
}

func TestConversationsPrimaryTitleFolderAndHiddenProfile(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	conv := model.ConversationEntry{
		Agent:       "antigravity",
		ID:          "sess-agy-1",
		App:         "antigravity",
		Profile:     "secret-profile",
		Title:       "Refactor gateway retries",
		CWD:         "/home/u/dev/model-router",
		Space:       "router-space",
		UpdatedAtMs: now.UnixMilli() - 60_000,
	}
	fb := &fakeConversationsBackend{
		cachedSnap: model.ConversationSnapshot{
			OK:            true,
			Conversations: []model.ConversationEntry{conv},
		},
	}
	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "secret-profile") {
		t.Fatalf("profile information must not be displayed in popup:\n%s", plain)
	}

	// Locate the conversation row and verify Title appears before folder, space, and harness.
	var rowLine string
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Refactor gateway retries") && strings.Contains(line, "antigravity") {
			rowLine = line
			break
		}
	}
	if rowLine == "" {
		t.Fatalf("expected conversation row with title and harness in view:\n%s", plain)
	}
	titleIdx := strings.Index(rowLine, "Refactor gateway retries")
	folderIdx := strings.Index(rowLine, "~/dev/model-router")
	spaceIdx := strings.Index(rowLine, "router-space")
	harnessIdx := strings.Index(rowLine, "antigravity")
	if folderIdx < 0 {
		t.Fatalf("expected folder ~/dev/model-router on row, got: %q", rowLine)
	}
	if !(titleIdx >= 0 && titleIdx < folderIdx && folderIdx < spaceIdx && spaceIdx < harnessIdx) {
		t.Fatalf("expected order title < folder < space < harness on row, got %q (indices: title=%d folder=%d space=%d harness=%d)",
			rowLine, titleIdx, folderIdx, spaceIdx, harnessIdx)
	}

	// Opening the conversation still preserves Profile internally for harness resume.
	m = update(m, keyType(tea.KeyEnter))
	if len(fb.openedConvs) != 1 || fb.openedConvs[0].conv.Profile != "secret-profile" {
		t.Fatalf("OpenConversation must retain conv.Profile for resume, got %+v", fb.openedConvs)
	}
}

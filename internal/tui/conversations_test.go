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
	m = update(m, harnessesMsg{})
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
	m = update(m, harnessesMsg{})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	items := m.conversationItems()
	if len(items) == 0 || items[0].Conv.ID != "sess-claude-1" || items[0].Live == nil {
		t.Fatalf("expected first item sess-claude-1 to be detected as live, got %+v", items)
	}

	// A running conversation shows its space and available actions.
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "◐") || strings.Contains(plain, "live·") || strings.Contains(plain, "live in") {
		t.Errorf("unexpected live status indicator in view:\n%s", plain)
	}
	if !strings.Contains(plain, "work · claude-code") || !strings.Contains(plain, "Enter focus live pane") {
		t.Errorf("expected space name and focus action in view:\n%s", plain)
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
	m = update(m, harnessesMsg{})
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
	m = update(m, harnessesMsg{})
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
	m = update(m, harnessesMsg{})
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

func TestConversationsProfilePickerSelectAndOpen(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	conv := model.ConversationEntry{
		Agent:       "codex",
		ID:          "sess-codex-pick",
		App:         "codex",
		Title:       "Add SQLite FTS5 indexing",
		CWD:         "/home/u/dev/model-router",
		UpdatedAtMs: now.UnixMilli() - 60_000,
	}
	harnesses := []model.HarnessEntry{
		{
			Name:        "codex",
			BinaryState: "ok",
			Profiles: []model.HarnessProfile{
				{Name: "router", DisplayName: "Codex Router", Source: "store"},
				{Name: "local", DisplayName: "Local", Source: "user"},
			},
		},
	}
	fb := &fakeConversationsBackend{
		fakeBackend: fakeBackend{harness: harnesses},
		cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
	}
	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = update(m, harnessesMsg{entries: harnesses})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	// Enter opens the profile picker instead of immediately calling OpenConversation.
	m = update(m, keyType(tea.KeyEnter))
	if m.Quitting() {
		t.Fatal("Enter on unprofiled chat with multiple profiles should open picker, not quit")
	}
	if m.convPickPending == nil {
		t.Fatal("expected convPickPending to be set")
	}
	if len(fb.openedConvs) != 0 {
		t.Fatalf("OpenConversation should not have been called yet, got %+v", fb.openedConvs)
	}
	if len(m.convPickProfiles) != 3 { // Default + router + local
		t.Fatalf("convPickProfiles len = %d, want 3: %+v", len(m.convPickProfiles), m.convPickProfiles)
	}
	if m.convPickCursor != 0 {
		t.Fatalf("convPickCursor = %d, want 0 when conv.Profile is empty", m.convPickCursor)
	}

	// Render shows title, agent, profiles, and footer hint.
	plain := ansi.Strip(m.View())
	for _, want := range []string{"Choose profile", "codex", "Add SQLite FTS5 indexing", "Default", "router", "local", "enter open · esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("picker view missing %q:\n%s", want, plain)
		}
	}

	// Navigate down twice (to "local"), then up once (to "router"), then press Enter.
	m = update(m, keyType(tea.KeyDown))
	m = update(m, keyRunes("j"))
	if m.convPickCursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.convPickCursor)
	}
	m = update(m, keyRunes("k"))
	if m.convPickCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.convPickCursor)
	}

	m = update(m, keyType(tea.KeyEnter))
	if !m.Quitting() {
		t.Fatal("Enter in profile picker should open conversation and quit")
	}
	if m.convPickPending != nil {
		t.Fatal("convPickPending should be cleared after opening")
	}
	if len(fb.openedConvs) != 1 || fb.openedConvs[0].conv.ID != "sess-codex-pick" || fb.openedConvs[0].conv.Profile != "router" {
		t.Fatalf("openedConvs = %+v, want sess-codex-pick with profile router", fb.openedConvs)
	}
}

func TestConversationsProfilePickerCancelWithEscAndQ(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	conv := model.ConversationEntry{
		Agent:       "codex",
		ID:          "sess-codex-cancel",
		App:         "codex",
		Title:       "Cancelable chat",
		CWD:         "/home/u/dev/model-router",
		UpdatedAtMs: now.UnixMilli() - 60_000,
	}
	harnesses := []model.HarnessEntry{
		{
			Name:     "codex",
			Profiles: []model.HarnessProfile{{Name: "work"}, {Name: "personal"}},
		},
	}
	fb := &fakeConversationsBackend{
		fakeBackend: fakeBackend{harness: harnesses},
		cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
	}
	m := New(fb, Options{
		InitialMode: ModeConversations,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return now },
	})
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = update(m, harnessesMsg{entries: harnesses})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	// Open picker then press Esc.
	m = update(m, keyType(tea.KeyEnter))
	if m.convPickPending == nil {
		t.Fatal("expected picker to open")
	}
	m = update(m, keyType(tea.KeyEsc))
	if m.convPickPending != nil || m.Quitting() || len(fb.openedConvs) != 0 {
		t.Fatalf("Esc should cancel picker and stay in ModeConversations: pending=%v quitting=%v opened=%+v", m.convPickPending != nil, m.Quitting(), fb.openedConvs)
	}

	// Open picker again then press q.
	m = update(m, keyType(tea.KeyEnter))
	if m.convPickPending == nil {
		t.Fatal("expected picker to reopen")
	}
	m = update(m, keyRunes("q"))
	if m.convPickPending != nil || m.Quitting() || len(fb.openedConvs) != 0 {
		t.Fatalf("q should cancel picker and stay in ModeConversations: pending=%v quitting=%v opened=%+v", m.convPickPending != nil, m.Quitting(), fb.openedConvs)
	}
}

func TestConversationsProfilePickerShownWhenProfileOrLaunchCmdSet(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	harnesses := []model.HarnessEntry{
		{
			Name:     "codex",
			Profiles: []model.HarnessProfile{{Name: "work"}, {Name: "personal"}},
		},
	}

	t.Run("stored profile pre-selects profile and confirms it with cleared launch_cmd", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "codex",
			ID:          "sess-has-profile",
			App:         "codex",
			Profile:     "work",
			LaunchCmd:   []string{"harness", "codex", "-p", "work", "resume", "sess-has-profile"},
			Title:       "Has stored profile",
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		// First Enter opens the picker with "work" (index 1) pre-selected.
		m = update(m, keyType(tea.KeyEnter))
		if m.Quitting() || m.convPickPending == nil || len(fb.openedConvs) != 0 {
			t.Fatalf("expected profile picker even when profile is set: quitting=%v pending=%v opened=%+v", m.Quitting(), m.convPickPending != nil, fb.openedConvs)
		}
		if m.convPickCursor != 1 || m.convPickProfiles[m.convPickCursor].Name != "work" {
			t.Fatalf("cursor = %d (%+v), want index 1 (work)", m.convPickCursor, m.convPickProfiles)
		}

		// Second Enter immediately confirms "work" and clears LaunchCmd.
		m = update(m, keyType(tea.KeyEnter))
		if !m.Quitting() || len(fb.openedConvs) != 1 {
			t.Fatalf("expected open after confirming picker: quitting=%v opened=%+v", m.Quitting(), fb.openedConvs)
		}
		if fb.openedConvs[0].conv.Profile != "work" {
			t.Fatalf("opened Profile = %q, want work", fb.openedConvs[0].conv.Profile)
		}
		if fb.openedConvs[0].conv.LaunchCmd != nil {
			t.Fatalf("expected LaunchCmd to be cleared on picker confirm, got %v", fb.openedConvs[0].conv.LaunchCmd)
		}
	})

	t.Run("stored profile case-insensitive pre-select and switch to another profile clears launch_cmd", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "codex",
			ID:          "sess-switch-profile",
			App:         "codex",
			Profile:     "WORK",
			LaunchCmd:   []string{"harness", "codex", "-p", "work", "resume", "sess-switch-profile"},
			Title:       "Switch profile chat",
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if m.convPickPending == nil || m.convPickCursor != 1 {
			t.Fatalf("expected picker pre-selected to work (1), pending=%v cursor=%d", m.convPickPending != nil, m.convPickCursor)
		}

		// Move down to "personal" (index 2) and confirm.
		m = update(m, keyType(tea.KeyDown))
		if m.convPickCursor != 2 {
			t.Fatalf("cursor = %d, want 2 (personal)", m.convPickCursor)
		}
		m = update(m, keyType(tea.KeyEnter))
		if !m.Quitting() || len(fb.openedConvs) != 1 {
			t.Fatalf("expected open after switching profile: quitting=%v opened=%+v", m.Quitting(), fb.openedConvs)
		}
		if fb.openedConvs[0].conv.Profile != "personal" {
			t.Fatalf("opened Profile = %q, want personal", fb.openedConvs[0].conv.Profile)
		}
		if fb.openedConvs[0].conv.LaunchCmd != nil {
			t.Fatalf("expected LaunchCmd to be cleared when switching profile, got %v", fb.openedConvs[0].conv.LaunchCmd)
		}
	})

	t.Run("stored launch_cmd without profile still shows picker at cursor 0", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "codex",
			ID:          "sess-has-launch-cmd",
			App:         "codex",
			Title:       "Has stored launch_cmd",
			LaunchCmd:   []string{"codex", "resume", "sess-has-launch-cmd"},
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if m.Quitting() || m.convPickPending == nil || len(fb.openedConvs) != 0 {
			t.Fatalf("expected profile picker even when launch_cmd is set: quitting=%v pending=%v opened=%+v", m.Quitting(), m.convPickPending != nil, fb.openedConvs)
		}
		if m.convPickCursor != 0 {
			t.Fatalf("cursor = %d, want 0 when Profile is empty", m.convPickCursor)
		}

		m = update(m, keyType(tea.KeyEnter))
		if !m.Quitting() || len(fb.openedConvs) != 1 || fb.openedConvs[0].conv.Profile != "default" || fb.openedConvs[0].conv.LaunchCmd != nil {
			t.Fatalf("expected open with default profile and nil LaunchCmd, got quitting=%v opened=%+v", m.Quitting(), fb.openedConvs)
		}
	})

	t.Run("unknown stored profile falls back to cursor 0", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "codex",
			ID:          "sess-unknown-profile",
			App:         "codex",
			Profile:     "deleted-profile",
			Title:       "Unknown stored profile",
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if m.convPickPending == nil || m.convPickCursor != 0 {
			t.Fatalf("expected fallback to cursor 0 for unknown profile: pending=%v cursor=%d", m.convPickPending != nil, m.convPickCursor)
		}
	})
}

func TestConversationsProfilePickerSkippedWhenNoOrSingleProfile(t *testing.T) {
	now := time.UnixMilli(1700000100000)

	t.Run("no profiles for agent", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "pi",
			ID:          "sess-pi-1",
			App:         "pi",
			Title:       "Standalone pi chat",
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		harnesses := []model.HarnessEntry{
			{Name: "pi", BinaryState: "ok", Profiles: nil},
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if !m.Quitting() || m.convPickPending != nil || len(fb.openedConvs) != 1 {
			t.Fatalf("expected immediate open with no profiles: quitting=%v pending=%v opened=%+v", m.Quitting(), m.convPickPending != nil, fb.openedConvs)
		}
	})

	t.Run("single profile-only agent", func(t *testing.T) {
		conv := model.ConversationEntry{
			Agent:       "codex",
			ID:          "sess-single-1",
			App:         "codex",
			Title:       "Single profile chat",
			UpdatedAtMs: now.UnixMilli() - 60_000,
		}
		harnesses := []model.HarnessEntry{
			{Name: "codex", BinaryState: "missing", Profiles: []model.HarnessProfile{{Name: "only"}}},
		}
		fb := &fakeConversationsBackend{
			fakeBackend: fakeBackend{harness: harnesses},
			cachedSnap:  model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, harnessesMsg{entries: harnesses})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if !m.Quitting() || m.convPickPending != nil || len(fb.openedConvs) != 1 {
			t.Fatalf("expected immediate open with single profile: quitting=%v pending=%v opened=%+v", m.Quitting(), m.convPickPending != nil, fb.openedConvs)
		}
	})
}

func TestConversationsProfilePickerLivePaneFocusesDirectly(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	conv := model.ConversationEntry{
		Agent:       "codex",
		ID:          "sess-live-unprofiled",
		App:         "codex",
		Title:       "Live codex session",
		CWD:         "/home/u/dev/model-router",
		UpdatedAtMs: now.UnixMilli() - 60_000,
	}
	harnesses := []model.HarnessEntry{
		{Name: "codex", Profiles: []model.HarnessProfile{{Name: "work"}, {Name: "personal"}}},
	}
	fb := &fakeConversationsBackend{
		fakeBackend: fakeBackend{
			harness: harnesses,
			spaces: []model.Space{
				{
					Name:      "work",
					IsCurrent: true,
					Panes: []model.Pane{
						{
							ID:            "%9",
							NumericID:     9,
							WindowID:      "@2",
							App:           "codex",
							Status:        model.StatusWorking,
							ResumeSession: "sess-live-unprofiled",
						},
					},
				},
			},
		},
		cachedSnap: model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
	}
	m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
	m = update(m, spacesMsg{spaces: fb.spaces})
	m = update(m, harnessesMsg{entries: harnesses})
	m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

	// Enter focuses live pane directly without showing the profile picker.
	m1 := update(m, keyType(tea.KeyEnter))
	if !m1.Quitting() || m1.convPickPending != nil || len(fb.focused) != 1 || fb.focused[0] != "work/@2/%9" {
		t.Fatalf("Enter on live pane should focus directly: quitting=%v pending=%v focused=%v", m1.Quitting(), m1.convPickPending != nil, fb.focused)
	}

	// Ctrl-o forces new tab and opens the profile picker because conv has no profile.
	m2 := update(m, keyType(tea.KeyCtrlO))
	if m2.Quitting() || m2.convPickPending == nil || !m2.convPickForceNewTab {
		t.Fatalf("Ctrl-o on unprofiled live conv should open picker with forceNewTab: quitting=%v pending=%v forceNewTab=%v", m2.Quitting(), m2.convPickPending != nil, m2.convPickForceNewTab)
	}
}

func TestConversationsProfilePickerWaitsForHarnesses(t *testing.T) {
	now := time.UnixMilli(1700000100000)
	conv := model.ConversationEntry{
		Agent:       "codex",
		ID:          "sess-race",
		App:         "codex",
		Title:       "Fast Enter before harnesses load",
		UpdatedAtMs: now.UnixMilli() - 60_000,
	}

	t.Run("opens picker once harnesses arrive with multiple profiles and pre-selects stored profile", func(t *testing.T) {
		convWithProfile := conv
		convWithProfile.Profile = "work"
		harnesses := []model.HarnessEntry{
			{Name: "codex", Profiles: []model.HarnessProfile{{Name: "work"}}},
		}
		fb := &fakeConversationsBackend{
			cachedSnap: model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{convWithProfile}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		// User hits Enter before harnessesMsg arrives.
		m = update(m, keyType(tea.KeyEnter))
		if !m.convPickWaitingHarnesses || m.Quitting() {
			t.Fatalf("expected waiting for harnesses: waiting=%v quitting=%v", m.convPickWaitingHarnesses, m.Quitting())
		}

		// Harnesses arrive -> picker opens with Default + work, pre-selecting work (index 1).
		m = update(m, harnessesMsg{entries: harnesses})
		if m.convPickWaitingHarnesses || m.convPickPending == nil || len(m.convPickProfiles) != 2 {
			t.Fatalf("expected picker populated after harnessesMsg: waiting=%v pending=%v profiles=%+v", m.convPickWaitingHarnesses, m.convPickPending != nil, m.convPickProfiles)
		}
		if m.convPickCursor != 1 {
			t.Fatalf("convPickCursor = %d, want 1 (work)", m.convPickCursor)
		}
	})

	t.Run("opens directly once harnesses arrive empty or error", func(t *testing.T) {
		fb := &fakeConversationsBackend{
			cachedSnap: model.ConversationSnapshot{OK: true, Conversations: []model.ConversationEntry{conv}},
		}
		m := New(fb, Options{InitialMode: ModeConversations, HomeDir: "/home/u", Now: func() time.Time { return now }})
		m = update(m, conversationsMsg{snap: fb.cachedSnap, refresh: true})

		m = update(m, keyType(tea.KeyEnter))
		if !m.convPickWaitingHarnesses {
			t.Fatal("expected waiting for harnesses")
		}

		// Harnesses load fails (e.g. harness not installed) -> opens immediately with no toast.
		m = update(m, harnessesMsg{err: "harnesses.json not found"})
		if !m.Quitting() || m.toast != "" || len(fb.openedConvs) != 1 || fb.openedConvs[0].conv.ID != "sess-race" {
			t.Fatalf("expected immediate open without toast: quitting=%v toast=%q opened=%+v", m.Quitting(), m.toast, fb.openedConvs)
		}
	})
}

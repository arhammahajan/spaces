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

type fakeOutcomesBackend struct {
	fakeBackend
	outcomes       []model.OutcomeEntry
	openedOutcomes []model.OutcomeEntry
	openOutcomeErr error
}

func (f *fakeOutcomesBackend) LoadOutcomes(_ context.Context) ([]model.OutcomeEntry, error) {
	return f.outcomes, nil
}

func (f *fakeOutcomesBackend) OpenOutcome(_ context.Context, outcome model.OutcomeEntry) error {
	if f.openOutcomeErr != nil {
		return f.openOutcomeErr
	}
	f.openedOutcomes = append(f.openedOutcomes, outcome)
	return nil
}

func inboxTestSpaces() []model.Space {
	mk := func(name string, cur bool, panes []model.Pane) model.Space {
		var c model.Counts
		for _, p := range panes {
			c.Add(p.Status)
		}
		return model.Space{Name: name, IsCurrent: cur, Panes: panes, Counts: c, Status: c.Rollup(), CWD: "/home/u/" + name}
	}
	return []model.Space{
		mk("alpha", true, []model.Pane{
			{ID: "%1", NumericID: 1, WindowID: "@1", WindowIndex: 0, WindowName: "shell", Command: "zsh", App: "shell", Status: model.StatusIdle},
			{ID: "%2", NumericID: 2, WindowID: "@2", WindowIndex: 1, WindowName: "code", Command: "opencode", App: "opencode", Status: model.StatusWorking, Text: "refactoring router", AgeMs: 15000},
		}),
		mk("beta", false, []model.Pane{
			{ID: "%3", NumericID: 3, WindowID: "@3", WindowIndex: 0, WindowName: "auth", Command: "claude", App: "claude", Status: model.StatusNeedsYou, Text: "Allow bash?", AgeMs: 65000, Hook: &model.HookSummary{Agent: "claude", App: "claude", State: "blocked", Kind: "permission", Event: "pre_tool", Text: "Allow bash?", Model: "claude-opus-4", ChildCount: 2, HasDetail: true}},
			{ID: "%4", NumericID: 4, WindowID: "@4", WindowIndex: 1, WindowName: "editor", Command: "nvim", App: "nvim", Status: model.StatusIdle},
			{ID: "%5", NumericID: 5, WindowID: "@5", WindowIndex: 2, WindowName: "tests", Command: "codex", App: "codex", Status: model.StatusDone, Text: "all green", AgeMs: 120000},
		}),
		mk("gamma", false, []model.Pane{
			{ID: "%6", NumericID: 6, WindowID: "@6", WindowIndex: 0, WindowName: "agent", Command: "gemini", App: "gemini", Status: model.StatusIdle, AgeMs: 300000},
		}),
	}
}

func TestInboxFiltersOnlyAgentsAndSortsByUrgency(t *testing.T) {
	fb := &fakeBackend{spaces: inboxTestSpaces()}
	m := New(fb, Options{InitialMode: ModeInbox})
	m = update(m, spacesMsg{spaces: fb.spaces})

	items := m.inboxItems()
	if len(items) != 4 {
		t.Fatalf("got %d agents, want 4 (shells and nvim must be excluded)", len(items))
	}
	// Expected order: claude (NeedsYou, beta), opencode (Working, alpha), gemini (Idle, gamma), codex (Done, beta).
	wantIDs := []string{"%3", "%2", "%6", "%5"}
	for i, want := range wantIDs {
		got := m.spaces[items[i].SpaceIdx].Panes[items[i].PaneIdx].ID
		if got != want {
			t.Errorf("item[%d] = %s, want %s", i, got, want)
		}
	}

	// Verify rendered section header order: NEEDS YOU -> WORKING -> IDLE -> DONE.
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	plain := ansi.Strip(m.View())
	posNeeds := strings.Index(plain, "NEEDS YOU · 1")
	posWorking := strings.Index(plain, "WORKING · 1")
	posIdle := strings.Index(plain, "IDLE · 1")
	posDone := strings.Index(plain, "DONE · 1")
	if !(posNeeds >= 0 && posWorking > posNeeds && posIdle > posWorking && posDone > posIdle) {
		t.Errorf("unexpected section order (needs=%d working=%d idle=%d done=%d):\n%s",
			posNeeds, posWorking, posIdle, posDone, plain)
	}
}

func TestInboxSingleClickOpensAgentInOtherSpace(t *testing.T) {
	fb := &fakeBackend{spaces: inboxTestSpaces()}
	m := New(fb, Options{InitialMode: ModeInbox})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(m, spacesMsg{spaces: fb.spaces})

	layout := m.inboxLayout(120, 30)
	entries := m.buildInboxListEntries(m.inboxItems())

	// Clicking a section header does not open or quit.
	m = update(m, tea.MouseMsg{
		X:      5,
		Y:      layout.topY + 0,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if m.Quitting() || len(fb.focused) != 0 {
		t.Fatalf("clicking a section header should be ignored, got quitting=%v focused=%v", m.Quitting(), fb.focused)
	}

	// Clicking a breathing spacer row between sections is also ignored.
	// Item order: 0=%3 (NeedsYou), 1=%2 (Working), 2=%6 (Idle, gamma), 3=%5 (Done).
	spacerIdx := -1
	gammaIdx := -1
	for i, e := range entries {
		if e.isSpacer && spacerIdx < 0 {
			spacerIdx = i
		}
		if !e.isHeader && !e.isSpacer && e.itemIdx == 2 && e.subLine == 0 {
			gammaIdx = i
		}
	}
	if spacerIdx < 0 || gammaIdx < 0 {
		t.Fatalf("expected spacer (%d) and gamma (%d) entries", spacerIdx, gammaIdx)
	}
	m = update(m, tea.MouseMsg{
		X:      5,
		Y:      layout.topY + spacerIdx,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if m.Quitting() || len(fb.focused) != 0 {
		t.Fatalf("clicking a spacer row should be ignored, got quitting=%v focused=%v", m.Quitting(), fb.focused)
	}

	// Clicking gamma/@6/%6 opens that agent in its space.
	m = update(m, tea.MouseMsg{
		X:      5,
		Y:      layout.topY + gammaIdx,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if !m.Quitting() {
		t.Fatalf("expected single click on agent row to open and quit")
	}
	if len(fb.focused) != 1 || fb.focused[0] != "gamma/@6/%6" {
		t.Fatalf("focused = %v, want [gamma/@6/%%6]", fb.focused)
	}
}

func TestInboxKeyboardNavigationAndFilters(t *testing.T) {
	fb := &fakeBackend{spaces: inboxTestSpaces()}
	m := New(fb, Options{InitialMode: ModeInbox})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(m, spacesMsg{spaces: fb.spaces})

	// Filter to working ('w') => only alpha/%2.
	m = update(m, keyRunes("w"))
	if items := m.inboxItems(); len(items) != 1 {
		t.Fatalf("working filter got %d items, want 1", len(items))
	}
	// Reset to all ('a').
	m = update(m, keyRunes("a"))
	if items := m.inboxItems(); len(items) != 4 {
		t.Fatalf("all filter got %d items, want 4", len(items))
	}

	// Search by space name ("gamma").
	m = update(m, keyRunes("/"))
	for _, r := range "gamma" {
		m = update(m, keyRunes(string(r)))
	}
	if items := m.inboxItems(); len(items) != 1 {
		t.Fatalf("search 'gamma' got %d items, want 1", len(items))
	}
	// Enter opens the matched agent in gamma.
	m = update(m, keyType(tea.KeyEnter))
	if !m.Quitting() || len(fb.focused) != 1 || fb.focused[0] != "gamma/@6/%6" {
		t.Fatalf("Enter in inbox search: quitting=%v focused=%v", m.Quitting(), fb.focused)
	}
}

func TestInboxOverlaysReturnToInbox(t *testing.T) {
	fb := &fakeBackend{spaces: inboxTestSpaces()}
	m := New(fb, Options{InitialMode: ModeInbox})
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 28})
	m = update(m, spacesMsg{spaces: fb.spaces})

	// Open help and close with Esc => returns to ModeInbox.
	m = update(m, keyRunes("?"))
	if m.ModeOf() != ModeHelp {
		t.Fatalf("expected ModeHelp, got %v", m.ModeOf())
	}
	if v := m.View(); !strings.Contains(v, "Inbox · keys") {
		t.Errorf("expected inbox help text, got:\n%s", v)
	}
	m = update(m, keyType(tea.KeyEsc))
	if m.ModeOf() != ModeInbox {
		t.Fatalf("expected Esc from help to return to ModeInbox, got %v", m.ModeOf())
	}

	// Open hook detail on selected agent (beta/claude) and close with H => returns to ModeInbox.
	m = update(m, keyRunes("H"))
	if m.ModeOf() != ModeHookDetail {
		t.Fatalf("expected ModeHookDetail, got %v", m.ModeOf())
	}
	m = update(m, keyRunes("H"))
	if m.ModeOf() != ModeInbox {
		t.Fatalf("expected H from hook detail to return to ModeInbox, got %v", m.ModeOf())
	}
}

func TestInboxRendersUnifiedColumnarRowsAndActionSubline(t *testing.T) {
	spaces := inboxTestSpaces()
	spaces[1].Panes[0].CWD = "/home/u/beta"
	fb := &fakeBackend{spaces: spaces}
	m := New(fb, Options{InitialMode: ModeInbox, HomeDir: "/home/u"})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	u, cmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = u.(Model)
	if cmd != nil {
		t.Errorf("expected spacesMsg in ModeInbox not to spawn any preview capture command")
	}

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"Inbox",
		"NEEDS YOU · 1",
		"DONE · 1",
		"WORKING · 1",
		"IDLE · 1",
		"claude",
		"beta",
		"auth",
		"↳ permission: Allow bash?",
		"claude-opus-4",
		"+2 subs",
		"needs you",
		"opencode",
		"alpha",
		"code",
		"↳ refactoring router",
		"~/beta",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("inbox view missing %q:\n%s", want, plain)
		}
	}
	vLines := strings.Split(plain, "\n")
	if len(vLines) < 6 {
		t.Fatalf("unexpectedly short view:\n%s", plain)
	}
	// Line 4 is primary columnar row (status icon + title on left, space + harness + subs + age on right, no per-row status label),
	// Line 5 is 2nd metadata/action line (live action on left, model + pane + cwd on right).
	if !strings.Contains(vLines[4], "●") || !strings.Contains(vLines[4], "auth") || !strings.Contains(vLines[4], "beta") || !strings.Contains(vLines[4], "claude") || !strings.Contains(vLines[4], "+2 subs") {
		t.Errorf("expected primary row to show status icon, title, space, harness, and subs, got: %q", vLines[4])
	}
	if strings.Contains(vLines[4], "needs you") {
		t.Errorf("primary inbox row must not repeat per-listing status label, got: %q", vLines[4])
	}
	if strings.Index(vLines[4], "auth") > strings.Index(vLines[4], "claude") {
		t.Errorf("expected thread title before harness name in inbox row, got: %q", vLines[4])
	}
	if !strings.Contains(vLines[5], "↳ permission: Allow bash?") || !strings.Contains(vLines[5], "claude-opus-4") || !strings.Contains(vLines[5], "~/beta") {
		t.Errorf("expected 2nd action line to show live action, model, and cwd, got: %q", vLines[5])
	}
	// Done agent (%5 codex) must NOT have a 2nd action line.
	if strings.Contains(plain, "↳ all green") {
		t.Errorf("done agent should stay 1-line without an action subline:\n%s", plain)
	}
	if strings.Contains(plain, "│") {
		t.Errorf("inbox view should not contain a split-preview vertical divider:\n%s", plain)
	}
	if strings.Contains(plain, "nvim") {
		t.Errorf("inbox view should not include non-agent pane nvim:\n%s", plain)
	}
}

func TestPaneConversationNamePriorityOrderAndBogusTitleFiltering(t *testing.T) {
	// 1. Hooks / session store beats OSC pane title, screen reader, and window name.
	p1 := model.Pane{
		App:               "claude-code",
		Command:           "claude",
		CWD:               "/home/arhammahajan/dev/model-router",
		ConversationTitle: "Hook Session Store Title",
		Title:             "✳ OSC Title",
		ScreenTitle:       "Screen Reader Title",
		WindowName:        "Window Name",
	}
	if got := paneConversationName(p1); got != "Hook Session Store Title" {
		t.Errorf("tier 1 (hook/session store) = %q, want Hook Session Store Title", got)
	}

	// 2. Valid OSC title beats screen reader and window name when hook title is absent,
	//    and strips trailing " | <cwd_basename>".
	p2 := model.Pane{
		App:         "codex",
		Command:     "codex",
		CWD:         "/home/arhammahajan/dev/dots",
		Title:       "✳ Remove extra T3Code providers | dots",
		ScreenTitle: "Screen Reader Title",
		WindowName:  "Window Name",
	}
	if got := paneConversationName(p2); got != "Remove extra T3Code providers" {
		t.Errorf("tier 2 (cleaned OSC title) = %q, want Remove extra T3Code providers", got)
	}

	// 3. Bogus OSC titles (CWD basename, harness launch command, version banner, user@host:path)
	//    are rejected and fall through to Tier 3 (Screen Reader).
	bogusTitles := []struct {
		name  string
		app   string
		cwd   string
		title string
	}{
		{"cwd basename", "muse", "/home/arhammahajan", "arhammahajan"},
		{"nested cwd basename", "codex", "/home/arhammahajan/dev", "dev"},
		{"harness profile launch cmd", "antigravity", "/home/arhammahajan/dev/model-router", "harness agy profile saucerf166"},
		{"version banner with cwd", "fx", "/home/arhammahajan", "fx v0.0.15 | arhammahajan"},
		{"default shell host title", "claude-code", "/home/arhammahajan/dev/model-router", "arhammahajan@fedora:~/dev/model-router"},
	}
	for _, tc := range bogusTitles {
		p := model.Pane{
			App:         tc.app,
			Command:     tc.app,
			CWD:         tc.cwd,
			Title:       tc.title,
			ScreenTitle: "Screen Reader Fallback Title",
			WindowName:  "FallbackWin",
		}
		if got := paneConversationName(p); got != "Screen Reader Fallback Title" {
			t.Errorf("%s: paneConversationName(%q) = %q, want Screen Reader Fallback Title", tc.name, tc.title, got)
		}
	}

	// 4. When hooks, OSC title, and screen reader are all empty/bogus, falls back to tmux window name.
	p4 := model.Pane{
		App:        "codex",
		Command:    "codex",
		CWD:        "/home/arhammahajan/dev",
		Title:      "dev",
		WindowName: "my-feature-window",
	}
	if got := paneConversationName(p4); got != "my-feature-window" {
		t.Errorf("tier 4 (tmux window name) = %q, want my-feature-window", got)
	}
}

func TestInboxRecentOutcomesRenderFilterSearchAndClickOpensNewTab(t *testing.T) {
	fixedNow := time.UnixMilli(1700000300000)
	outcomes := []model.OutcomeEntry{
		{
			Session:   "gamma",
			Pane:      "11",
			Agent:     "claude",
			App:       "claude-code",
			Profile:   "work",
			Outcome:   "failed",
			Title:     "Fix billing migration",
			Text:      "error: migration failed",
			Model:     "claude-opus-4",
			SessionID: "sess-fail-1",
			CWD:       "/home/u/gamma",
			TsMs:      fixedNow.UnixMilli() - 120_000, // 2m ago
		},
		{
			Session:   "delta",
			Pane:      "14",
			Agent:     "codex",
			App:       "codex",
			Outcome:   "done",
			Title:     "Implement ATT-7",
			Text:      "all tests green",
			SessionID: "sess-done-2",
			CWD:       "/home/u/delta",
			TsMs:      fixedNow.UnixMilli() - 300_000, // 5m ago
		},
	}
	fb := &fakeOutcomesBackend{
		fakeBackend: fakeBackend{spaces: inboxTestSpaces()},
		outcomes:    outcomes,
	}
	m := New(fb, Options{
		InitialMode: ModeInbox,
		HomeDir:     "/home/u",
		Now:         func() time.Time { return fixedNow },
	})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(m, spacesMsg{spaces: fb.spaces, outcomes: outcomes})

	// 4 live agents + 2 persisted outcomes = 6 items, merged into the standard
	// inbox sections (NeedsYou > Failed > Working > Idle > Done):
	// 0: %3 (NeedsYou, live)
	// 1: gamma (Failed, persisted outcome)
	// 2: %2 (Working, live)
	// 3: %6 (Idle, live)
	// 4: %5 (Done, live)
	// 5: delta (Done, persisted outcome)
	items := m.inboxItems()
	if len(items) != 6 {
		t.Fatalf("got %d inbox items, want 6", len(items))
	}
	if !items[1].IsOutcome || m.outcomes[items[1].OutcomeIdx].Session != "gamma" {
		t.Fatalf("expected item[1] to be failed outcome gamma, got %+v", items[1])
	}
	if !items[5].IsOutcome || m.outcomes[items[5].OutcomeIdx].Session != "delta" {
		t.Fatalf("expected item[5] to be done outcome delta, got %+v", items[5])
	}

	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "RECENT OUTCOMES") {
		t.Errorf("inbox should merge outcomes into FAILED/DONE instead of rendering a separate RECENT OUTCOMES section:\n%s", plain)
	}
	for _, want := range []string{
		"FAILED · 1",
		"DONE · 2",
		"claude-code",
		"gamma",
		"Fix billing migration",
		"2m",
		"codex",
		"delta",
		"Implement ATT-7",
		"5m",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("inbox view missing %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Fix billing migration") {
			if strings.Contains(line, "— work") {
				t.Errorf("inbox outcome row must not show profile name: %q", line)
			}
			if strings.Contains(line, "failed") {
				t.Errorf("inbox outcome row must not repeat per-listing status: %q", line)
			}
			if strings.Index(line, "Fix billing migration") > strings.Index(line, "claude-code") {
				t.Errorf("expected thread title before harness name in outcome row: %q", line)
			}
		}
	}

	// Filter 'd' (done) keeps live done (%5) + done outcome (delta).
	m = update(m, keyRunes("d"))
	doneItems := m.inboxItems()
	if len(doneItems) != 2 || !doneItems[1].IsOutcome || m.outcomes[doneItems[1].OutcomeIdx].Session != "delta" {
		t.Fatalf("done filter items = %+v", doneItems)
	}

	// Filter 'b' (blocked/failed) keeps live needs-you (%3) + failed outcome (gamma).
	m = update(m, keyRunes("b"))
	blockedItems := m.inboxItems()
	if len(blockedItems) != 2 || !blockedItems[1].IsOutcome || m.outcomes[blockedItems[1].OutcomeIdx].Session != "gamma" {
		t.Fatalf("blocked filter items = %+v", blockedItems)
	}

	// Filter 'w' (working) excludes outcomes.
	m = update(m, keyRunes("w"))
	workingItems := m.inboxItems()
	if len(workingItems) != 1 || workingItems[0].IsOutcome {
		t.Fatalf("working filter should exclude outcomes, got %+v", workingItems)
	}

	// Back to all ('a'), search for closed workspace "delta" -> matches only the outcome.
	m = update(m, keyRunes("a"))
	m = update(m, keyRunes("/"))
	for _, r := range "delta" {
		m = update(m, keyRunes(string(r)))
	}
	searchItems := m.inboxItems()
	if len(searchItems) != 1 || !searchItems[0].IsOutcome {
		t.Fatalf("search 'delta' got %+v, want 1 outcome", searchItems)
	}
	// Detail footer for selected outcome mentions opening in a new tab.
	if detail := ansi.Strip(m.inboxDetailLine(searchItems, 120)); !strings.Contains(detail, "open in new tab") || !strings.Contains(detail, "sess-done-2") {
		t.Errorf("unexpected outcome detail line: %q", detail)
	}

	// Clear search and single-click the failed outcome row (itemIdx 1: gamma / Fix billing migration).
	m = update(m, keyType(tea.KeyEsc))
	layout := m.inboxLayout(120, 30)
	entries := m.buildInboxListEntries(m.inboxItems())
	outcomeRowY := -1
	for i, e := range entries {
		if !e.isHeader && e.itemIdx == 1 {
			outcomeRowY = layout.topY + i
			break
		}
	}
	if outcomeRowY < 0 {
		t.Fatalf("could not locate outcome entry in list")
	}
	m = update(m, tea.MouseMsg{
		X:      10,
		Y:      outcomeRowY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	if !m.Quitting() {
		t.Fatalf("expected clicking outcome to quit popup")
	}
	if len(fb.openedOutcomes) != 1 || fb.openedOutcomes[0].SessionID != "sess-fail-1" {
		t.Fatalf("openedOutcomes = %+v, want sess-fail-1", fb.openedOutcomes)
	}
	if len(fb.focused) != 0 {
		t.Fatalf("clicking closed outcome must open in new tab, not FocusPane; focused=%v", fb.focused)
	}
}

func TestInboxHidesPersistedOutcomeWhenConversationIsLive(t *testing.T) {
	spaces := inboxTestSpaces()
	// Live pane %2 (opencode, Working) is running sess-live-1.
	spaces[0].Panes[1].ResumeSession = "sess-live-1"
	outcomes := []model.OutcomeEntry{
		{
			Session:   "alpha",
			Agent:     "opencode",
			App:       "opencode",
			Outcome:   "done",
			Title:     "Refactoring router",
			SessionID: "sess-live-1",
			TsMs:      1700000000000,
		},
	}
	fb := &fakeOutcomesBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		outcomes:    outcomes,
	}
	m := New(fb, Options{InitialMode: ModeInbox})
	m = update(m, spacesMsg{spaces: spaces, outcomes: outcomes})
	// Only the 4 live panes should appear; the persisted outcome for sess-live-1 is suppressed while live.
	if items := m.inboxItems(); len(items) != 4 {
		t.Fatalf("got %d items, want 4 (live conversation must not duplicate in DONE): %+v", len(items), items)
	}

	// Same (session, pane) with matching agent is also suppressed even when the
	// live pane's ResumeSession is stale or unset.
	spaces[0].Panes[1].ResumeSession = "stale-id"
	outcomes[0].Pane = "2" // matches %2
	m = update(m, spacesMsg{spaces: spaces, outcomes: outcomes})
	if items := m.inboxItems(); len(items) != 4 {
		t.Fatalf("got %d items, want 4 (same live pane must not duplicate in DONE): %+v", len(items), items)
	}
}

func TestInboxRecentOutcomesVisibleWhenNoLiveAgents(t *testing.T) {
	fixedNow := time.UnixMilli(1700000100000)
	outcomes := []model.OutcomeEntry{
		{
			Session:   "closed-space",
			Agent:     "claude",
			App:       "claude-code",
			Outcome:   "done",
			Title:     "Refactor auth",
			SessionID: "sess-closed",
			TsMs:      fixedNow.UnixMilli() - 45_000,
		},
	}
	fb := &fakeOutcomesBackend{outcomes: outcomes}
	m := New(fb, Options{
		InitialMode: ModeInbox,
		Now:         func() time.Time { return fixedNow },
	})
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m = update(m, spacesMsg{spaces: nil, outcomes: outcomes})

	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "DONE · 1") || !strings.Contains(plain, "closed-space") || !strings.Contains(plain, "45s") {
		t.Fatalf("expected persisted done outcome in DONE section even with zero live panes:\n%s", plain)
	}
	// Pressing Enter opens the conversation in a new tab.
	m = update(m, keyType(tea.KeyEnter))
	if !m.Quitting() || len(fb.openedOutcomes) != 1 || fb.openedOutcomes[0].SessionID != "sess-closed" {
		t.Fatalf("Enter on outcome: quitting=%v opened=%+v", m.Quitting(), fb.openedOutcomes)
	}
}

func TestInboxOmitsProfileInListingAndMatchesSearch(t *testing.T) {
	spaces := inboxTestSpaces()
	spaces[0].Panes[1].App = "antigravity"
	spaces[0].Panes[1].Profile = "pratham"
	fb := &fakeBackend{spaces: spaces}
	m := New(fb, Options{InitialMode: ModeInbox, HomeDir: "/home/u"})
	m = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = update(m, spacesMsg{spaces: spaces})

	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "antigravity — pratham") {
		t.Fatalf("expected live inbox listing row to omit profile name, got:\n%s", plain)
	}
	if !strings.Contains(plain, "antigravity") {
		t.Fatalf("expected live inbox listing row to show harness name, got:\n%s", plain)
	}

	// Search by profile name still finds the live agent and detail line shows app — profile.
	m = update(m, keyRunes("/"))
	for _, r := range "pratham" {
		m = update(m, keyRunes(string(r)))
	}
	items := m.inboxItems()
	if len(items) != 1 || items[0].IsOutcome {
		t.Fatalf("profile search got %+v, want 1 live pane", items)
	}
	if detail := ansi.Strip(m.inboxDetailLine(items, 120)); !strings.Contains(detail, "antigravity — pratham") {
		t.Fatalf("expected inbox detail footer to show app — profile, got: %q", detail)
	}
}

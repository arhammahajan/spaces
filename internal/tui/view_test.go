package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

func viewModel() Model {
	fb := &fakeBackend{spaces: testSpaces()}
	m := New(fb, Options{HomeDir: "/home/u"})
	m.spaces = testSpaces()
	m.width, m.height = 100, 30
	return m
}

func TestViewHeaderPillsAndRows(t *testing.T) {
	m := viewModel()
	v := m.View()
	for _, want := range []string{"Spaces", "dots", "server", "opencode", "all", "blocked", "working", "done", "idle"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q\n%s", want, v)
		}
	}
}

func TestViewPaneRowShowsConversationAndOptionalActionSubline(t *testing.T) {
	spaces := []model.Space{
		{
			Name:      "work",
			IsCurrent: true,
			Status:    model.StatusWorking,
			Panes: []model.Pane{
				{
					ID:         "%1",
					NumericID:  1,
					WindowID:   "@1",
					WindowName: "claude",
					App:        "claude",
					Command:    "claude",
					Title:      "✳ Fix auth middleware",
					Status:     model.StatusWorking,
					Text:       "Bash · go test ./...",
					Hook: &model.HookSummary{
						Agent:      "claude",
						State:      "working",
						Text:       "Editing internal/auth.go",
						Model:      "claude-opus-4",
						ChildCount: 2,
						SessionID:  "sess-123",
					},
				},
				{
					ID:         "%2",
					NumericID:  2,
					WindowID:   "@2",
					WindowName: "refactor-router",
					App:        "codex",
					Command:    "codex",
					Title:      "user@host:/home/u/repo",
					Status:     model.StatusNeedsYou,
					Text:       "approve rm -rf",
				},
				{
					ID:         "%3",
					NumericID:  3,
					WindowID:   "@3",
					WindowName: "finished-task",
					App:        "gemini",
					Command:    "gemini",
					Status:     model.StatusDone,
					Text:       "completed task output",
				},
			},
		},
	}
	m := New(&fakeBackend{spaces: spaces}, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 120, 30
	v := m.View()
	if !strings.Contains(v, "claude") || !strings.Contains(v, "Fix auth middleware") || !strings.Contains(v, "+2 subs") || !strings.Contains(v, "claude-opus-4") || !strings.Contains(v, "working") {
		t.Fatalf("expected harness, conversation title, inline badges, and status on primary line, got:\n%s", v)
	}
	if !strings.Contains(v, "codex") || !strings.Contains(v, "refactor-router") || !strings.Contains(v, "needs you") {
		t.Fatalf("expected distinct window name fallback and status, got:\n%s", v)
	}
	// Working and blocked agent panes render an optional 2nd action line (`↳ ...`),
	// while Done/Idle panes stay a single compact line without an action subline.
	if !strings.Contains(v, "↳ Bash · go test ./...") || !strings.Contains(v, "↳ approve rm -rf") {
		t.Fatalf("expected working/blocked panes to show 2nd action line, got:\n%s", v)
	}
	if strings.Contains(v, "↳ completed task output") {
		t.Fatalf("done pane must stay 1-line without a 2nd action line, got:\n%s", v)
	}
	// Space row should not render a status icon before the space name.
	if strings.Contains(v, "▾ ◐ work") {
		t.Fatalf("space row must not show status icon before space name, got:\n%s", v)
	}
}

func TestViewPaneShowsProfileAlongsideApp(t *testing.T) {
	spaces := []model.Space{
		{
			Name:      "model-router",
			IsCurrent: true,
			Status:    model.StatusWorking,
			Panes: []model.Pane{
				{
					ID:                "%1",
					NumericID:         1,
					WindowID:          "@1",
					WindowName:        "agy",
					App:               "antigravity",
					Profile:           "pratham",
					Command:           "agy",
					ConversationTitle: "Implement ATT-7",
					Status:            model.StatusWorking,
					CWD:               "/home/u/dev/model-router",
				},
				{
					ID:                "%2",
					NumericID:         2,
					WindowID:          "@2",
					WindowName:        "claude",
					App:               "claude",
					Command:           "claude",
					ConversationTitle: "Review PR",
					Status:            model.StatusIdle,
					CWD:               "/home/u/dev/model-router",
				},
			},
		},
	}
	m := New(&fakeBackend{spaces: spaces}, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 120, 30
	// Select the profiled pane row so the detail footer renders it too.
	for i, r := range m.visibleRows() {
		if r.Kind == RowPane && r.PaneIdx == 0 {
			m.selected = i
			break
		}
	}
	v := m.View()
	if !strings.Contains(v, "antigravity — pratham") {
		t.Fatalf("expected Spaces view to show app — profile, got:\n%s", v)
	}
	if detail := m.detailLine(120); !strings.Contains(detail, "antigravity — pratham") {
		t.Fatalf("expected Spaces detail footer to show app — profile, got: %q", detail)
	}
	if strings.Contains(v, "claude —") {
		t.Fatalf("unprofiled pane must not show em dash, got:\n%s", v)
	}
}

func TestViewSmallTerminal(t *testing.T) {
	m := viewModel()
	m.width, m.height = 10, 3
	v := m.View()
	if !strings.Contains(v, "Spaces") {
		t.Fatalf("small view should contain Spaces, got %q", v)
	}
	m2 := New(&fakeBackend{}, Options{InitialMode: ModeAgents})
	m2.width, m2.height = 10, 3
	if !strings.Contains(m2.View(), "Agents") {
		t.Fatal("small agents view should contain Agents")
	}
}

func TestViewLargeTerminalDetailLine(t *testing.T) {
	m := viewModel()
	m.width, m.height = 120, 40
	v := m.View()
	// detail line for selected space contains CWD-ish content
	if !strings.Contains(v, "/home/u/dots") && !strings.Contains(v, "~/dots") {
		t.Fatalf("expected detail CWD, got:\n%s", v)
	}
	// small height hides detail line: height 10 has no CWD detail
	m.height = 10
	v2 := m.View()
	_ = v2
}

func TestViewSearchCaretAndFilter(t *testing.T) {
	m := viewModel()
	m.mode = ModeSearch
	m.query = "claude"
	m.cursor = 6
	v := m.View()
	if !strings.Contains(v, "claude") {
		t.Fatalf("expected query in view:\n%s", v)
	}
}

func TestViewDialogs(t *testing.T) {
	m := viewModel()
	m.openNew("")
	if v := m.View(); !strings.Contains(v, "New space") || !strings.Contains(v, "The folder is the starting directory") {
		t.Fatalf("new space dialog missing:\n%s", v)
	}
	m2 := viewModel()
	m2.selected = 0
	m2.requestRename()
	if v := m2.View(); !strings.Contains(v, "Rename space") {
		t.Fatalf("rename dialog missing:\n%s", v)
	}
	m3 := viewModel()
	for i, r := range m3.visibleRows() {
		if r.Kind == RowSpace && m3.spaces[r.SpaceIdx].Name == "server" {
			m3.selected = i
		}
	}
	m3.requestKill()
	if v := m3.View(); !strings.Contains(v, "Close space?") || !strings.Contains(v, "server") {
		t.Fatalf("confirm dialog missing:\n%s", v)
	}
}

func TestViewHookDetail(t *testing.T) {
	m := viewModel()
	rows := m.visibleRows()
	for i, r := range rows {
		if r.Kind == RowPane && m.spaces[r.SpaceIdx].Panes[r.PaneIdx].NumericID == 2 {
			m.selected = i
		}
	}
	u, _ := m.Update(keyRunes("H"))
	m = u.(Model)
	m.hookDetail = &model.HookDetail{
		HookSummary: model.HookSummary{Agent: "claude", App: "claude", State: "blocked", Kind: "permission", Model: "m1", SessionID: "s1", CWD: "/tmp"},
		Prompt:      "fix the failing test",
		Tool:        &model.HookTool{Name: "Bash", Input: "cargo test", Output: "ok"},
		Reply:       "looks good",
		Children:    []model.HookChild{{ID: "c1", Kind: "Explore", State: "working", Text: "searching"}},
	}
	m.hookLoading = false
	v := m.View()
	for _, want := range []string{"agent hooks", "fix the failing test", "cargo test", "Explore", "model", "session"} {
		if !strings.Contains(v, want) {
			t.Fatalf("hook view missing %q:\n%s", want, v)
		}
	}
	// scrolled view still renders
	m.hookScroll = 2
	_ = m.View()
}

func TestViewHelp(t *testing.T) {
	m := viewModel()
	m.mode = ModeHelp
	v := m.View()
	if !strings.Contains(v, "keys") {
		t.Fatalf("help missing keys:\n%s", v)
	}
}

func TestViewAgentsAndProfiles(t *testing.T) {
	fb := &fakeBackend{
		harness: []model.HarnessEntry{
			{Name: "claude", Description: "Claude Code against the router"},
			{Name: "codex", Description: "Codex", Profiles: []model.HarnessProfile{{Name: "router", DisplayName: "Codex Router", Source: "t3"}}},
		},
	}
	m := New(fb, Options{InitialMode: ModeAgents, HomeDir: "/home/u"})
	m.width, m.height = 100, 30
	u, _ := m.Update(harnessesMsg{entries: fb.harness})
	m = u.(Model)
	v := m.View()
	for _, want := range []string{"Agents", "claude", "Codex", "2 harnesses"} {
		if !strings.Contains(v, want) {
			t.Fatalf("agents view missing %q:\n%s", want, v)
		}
	}
	// drill into codex profiles
	m.harnessCursor = 1
	u, _ = m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	v = m.View()
	for _, want := range []string{"Default", "router", "t3"} {
		if !strings.Contains(v, want) {
			t.Fatalf("profiles view missing %q:\n%s", want, v)
		}
	}
	// filter profiles
	m.agentFilter = "rout"
	m.agentCur = 4
	v = m.View()
	if !strings.Contains(v, "rout") {
		t.Fatalf("filter query missing:\n%s", v)
	}
}

func TestViewAgentsEmptyAndLoading(t *testing.T) {
	m := New(&fakeBackend{}, Options{InitialMode: ModeAgents})
	m.width, m.height = 80, 20
	if v := m.View(); !strings.Contains(v, "Loading harnesses") {
		t.Fatalf("expected loading:\n%s", v)
	}
	u, _ := m.Update(harnessesMsg{})
	m = u.(Model)
	if v := m.View(); !strings.Contains(v, "No working harnesses") {
		t.Fatalf("expected empty:\n%s", v)
	}
}

func TestViewInitialLoadingStates(t *testing.T) {
	cases := []struct {
		name        string
		mode        Mode
		wantLoading string
		wantEmpty   string
	}{
		{name: "spaces", mode: ModeNavigate, wantLoading: "Loading spaces", wantEmpty: "No running spaces"},
		{name: "inbox", mode: ModeInbox, wantLoading: "Loading agents", wantEmpty: "No running agents"},
		{name: "tabs", mode: ModeTabs, wantLoading: "Loading tabs", wantEmpty: "No running workspace"},
		{name: "worktrees", mode: ModeWorktrees, wantLoading: "Loading git worktrees", wantEmpty: "No git worktrees"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&fakeBackend{}, Options{InitialMode: tc.mode})
			m.width, m.height = 100, 24
			v := m.View()
			if !strings.Contains(v, tc.wantLoading) {
				t.Fatalf("%s initial view should show %q, got:\n%s", tc.name, tc.wantLoading, v)
			}
			if strings.Contains(v, tc.wantEmpty) {
				t.Fatalf("%s initial view should not flash %q:\n%s", tc.name, tc.wantEmpty, v)
			}
			u, _ := m.Update(spacesMsg{seq: m.spacesSeq})
			m = u.(Model)
			if v2 := m.View(); !strings.Contains(v2, tc.wantEmpty) {
				t.Fatalf("%s post-load empty view should show %q, got:\n%s", tc.name, tc.wantEmpty, v2)
			}
		})
	}
}

func TestViewToastAndHints(t *testing.T) {
	m := viewModel()
	m.toast = "boom happened"
	v := m.View()
	if !strings.Contains(v, "boom happened") {
		t.Fatalf("toast missing:\n%s", v)
	}
	// narrow hints
	m.width = 50
	m.toast = ""
	if v := m.View(); !strings.Contains(v, "move") {
		t.Fatalf("hints missing:\n%s", v)
	}
}

func TestStatusColorMapping(t *testing.T) {
	cases := map[model.Status]string{
		model.StatusNeedsYou:  ColorBlue,
		model.StatusFailed:    ColorRed,
		model.StatusDone:      ColorGreen,
		model.StatusWorking:   ColorYellow,
		model.StatusQuiet:     ColorBlue,
		model.StatusNewOutput: ColorBlue,
		model.StatusIdle:      ColorOverlay0,
	}
	for s, want := range cases {
		if got := StatusColor(s); got != want {
			t.Fatalf("status %v: got %s want %s", s, got, want)
		}
	}

	if _, _, got := inboxSectionFor(model.StatusNeedsYou); got != ColorBlue {
		t.Fatalf("inboxSectionFor(StatusNeedsYou) color = %s, want %s", got, ColorBlue)
	}
	if _, _, got := inboxSectionFor(model.StatusFailed); got != ColorRed {
		t.Fatalf("inboxSectionFor(StatusFailed) color = %s, want %s", got, ColorRed)
	}

	if _, got := inboxCardActionText(model.Pane{Status: model.StatusNeedsYou, Kind: "permission", Text: "Allow bash?"}, "auth"); got != ColorBlue {
		t.Fatalf("inboxCardActionText(StatusNeedsYou) color = %s, want %s", got, ColorBlue)
	}
	if _, got := inboxCardActionText(model.Pane{Status: model.StatusFailed, Text: "exit 1"}, "auth"); got != ColorRed {
		t.Fatalf("inboxCardActionText(StatusFailed) color = %s, want %s", got, ColorRed)
	}

	if got := spaceBadges(model.Counts{Needs: 2, Failed: 1, Working: 3, Done: 4}); got != "●2 ✗1 ◐3 ✓4" {
		t.Fatalf("spaceBadges = %q, want %q", got, "●2 ✗1 ◐3 ✓4")
	}
}

func TestSpaceExistsAndValidation(t *testing.T) {
	m := viewModel()
	if !m.spaceExists("DOTS") {
		t.Fatal("case-insensitive exists failed")
	}
	if validateSpaceName("  ") == "" {
		t.Fatal("empty should fail")
	}
	if got := uniqueName("dots", m.spaceExists); got == "dots" {
		t.Fatal("unique should differ")
	}
}

func TestSpacesDeduplicatesIdenticalPaneGitAndPreservesDistinctWorktree(t *testing.T) {
	sharedGit := &model.GitInfo{
		Branch:             "feat/redesign",
		LinesAdded:         42,
		LinesRemoved:       8,
		IsDirty:            true,
		UncommittedAdded:   5,
		UncommittedRemoved: 2,
		PR:                 &model.PRSummary{Number: 42, State: "OPEN", Title: "Redesign popup"},
		Checks:             &model.CheckSummary{State: model.CheckFailure, Passed: 2, Total: 3, FailingName: "lint"},
	}
	otherGit := &model.GitInfo{
		Branch: "feat/other-wt",
		PR:     &model.PRSummary{Number: 99, State: "MERGED"},
		Checks: &model.CheckSummary{State: model.CheckSuccess},
	}
	spaces := []model.Space{
		{
			Name:      "model-router",
			IsCurrent: true,
			CWD:       "/home/u/dev/model-router",
			Git:       sharedGit,
			Status:    model.StatusWorking,
			Counts:    model.Counts{Working: 1, Done: 1},
			Panes: []model.Pane{
				{
					ID:                "%1",
					NumericID:         1,
					WindowID:          "@1",
					WindowName:        "agy",
					App:               "antigravity",
					Profile:           "pratham",
					Command:           "agy",
					ConversationTitle: "Redesign status popup",
					Status:            model.StatusWorking,
					Text:              "waiting on: go test ./...",
					CWD:               "/home/u/dev/model-router",
					Git:               sharedGit,
					AgeMs:             15_000,
					Hook: &model.HookSummary{
						Agent:        "antigravity",
						State:        "working",
						Text:         "waiting on: go test ./...",
						WaitingOn:    "waiting on: go test ./...",
						Model:        "claude-opus-4",
						FiniteActive: 1,
					},
				},
				{
					ID:                "%2",
					NumericID:         2,
					WindowID:          "@2",
					WindowName:        "codex",
					App:               "codex",
					Command:           "codex",
					ConversationTitle: "Clean up worktree",
					Status:            model.StatusDone,
					CWD:               "/home/u/dev/model-router-wt",
					Git:               otherGit,
					AgeMs:             120_000,
				},
			},
		},
	}

	m := New(&fakeBackend{spaces: spaces}, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 128, 24
	// Select pane %1 so detail inspector renders pane details.
	m.selected = 1
	plain := ansi.Strip(m.View())
	lines := strings.Split(plain, "\n")

	// Exact height compliance: never overflow popup height.
	if len(lines) != 24 {
		t.Fatalf("expected exact 24 rendered lines, got %d:\n%s", len(lines), plain)
	}

	// Space header row shows shared branch once; pane %1 must NOT duplicate it on its primary or action row.
	spaceRow := ""
	pane1Primary := ""
	pane1Action := ""
	pane2Row := ""
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "▾ model-router"):
			spaceRow = ln
		case strings.Contains(ln, "Redesign status popup"):
			pane1Primary = ln
		case strings.Contains(ln, "↳ waiting on: go test ./..."):
			pane1Action = ln
		case strings.Contains(ln, "Clean up worktree"):
			pane2Row = ln
		}
	}
	if !strings.Contains(spaceRow, "feat/redesign") || !strings.Contains(spaceRow, "#42 ● ✗ 2/3") {
		t.Fatalf("space header should display shared git/PR badges, got: %q", spaceRow)
	}
	if strings.Contains(pane1Primary, "feat/redesign") || strings.Contains(pane1Action, "feat/redesign") {
		t.Fatalf("pane sharing space git should not repeat branch on pane rows:\nprimary: %q\naction:  %q", pane1Primary, pane1Action)
	}
	if !strings.Contains(pane2Row, "feat/other-wt") || !strings.Contains(pane2Row, "#99 ◆ ✓") {
		t.Fatalf("pane in distinct worktree must still show its own git/PR badges, got: %q", pane2Row)
	}
	// Status column alignment: "working" and "done" start at the same visual column.
	workCol := strings.Index(pane1Primary, "working")
	doneCol := strings.Index(pane2Row, "done")
	if workCol < 0 || doneCol < 0 || workCol != doneCol {
		t.Fatalf("status labels should align vertically: working=%d done=%d\n%s\n%s", workCol, doneCol, pane1Primary, pane2Row)
	}
	// Detail footer should not duplicate waiting-on text.
	detail := ansi.Strip(m.detailLine(128))
	if strings.Count(detail, "waiting on: go test ./...") != 1 {
		t.Fatalf("detail inspector should show waiting-on once without duplication, got:\n%s", detail)
	}
}

func TestPopupLineCountAcrossHeights(t *testing.T) {
	for _, mode := range []Mode{ModeNavigate, ModeInbox} {
		for _, h := range []int{10, 14, 24, 32} {
			m := New(&fakeBackend{spaces: inboxTestSpaces()}, Options{InitialMode: mode, HomeDir: "/home/u"})
			m.spaces = inboxTestSpaces()
			m.width, m.height = 110, h
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			if len(lines) != h {
				t.Errorf("mode=%v height=%d: rendered %d lines, want %d", mode, h, len(lines), h)
			}
		}
	}
}

package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeTabsBackend struct {
	fakeBackend
	hookDetails     map[int]*model.HookDetail
	hookDetailCalls atomic.Int32
	hookDetailErr   error
	renamedWindows  [][2]string
	createdWindows  [][3]string
	killedWindows   []string
}

func (f *fakeTabsBackend) LoadHookDetail(_ context.Context, _ string, paneNum int) (*model.HookDetail, error) {
	f.hookDetailCalls.Add(1)
	if f.hookDetailErr != nil {
		return nil, f.hookDetailErr
	}
	if f.hookDetails != nil {
		if d, ok := f.hookDetails[paneNum]; ok {
			return d, nil
		}
	}
	return f.fakeBackend.LoadHookDetail(context.Background(), "", paneNum)
}

func (f *fakeTabsBackend) RenameWindow(_ context.Context, windowID, newName string) error {
	f.renamedWindows = append(f.renamedWindows, [2]string{windowID, newName})
	return nil
}

func (f *fakeTabsBackend) NewWindow(_ context.Context, session, cwd, name string) error {
	f.createdWindows = append(f.createdWindows, [3]string{session, cwd, name})
	return nil
}

func (f *fakeTabsBackend) KillWindow(_ context.Context, windowID string) error {
	f.killedWindows = append(f.killedWindows, windowID)
	return nil
}

func tabsTestNow() time.Time {
	return time.Unix(1_700_000_600, 0)
}

func sampleTabsSpaces() []model.Space {
	now := tabsTestNow()
	nowMs := now.UnixMilli()
	gitFeat := &model.GitInfo{
		RepoRoot:           "/home/u/dev/model-router",
		Branch:             "feat/tabs-popup",
		BaseBranch:         "main",
		Ahead:              2,
		IsDirty:            true,
		LinesAdded:         210,
		LinesRemoved:       34,
		UncommittedAdded:   18,
		UncommittedRemoved: 4,
		PR: &model.PRSummary{
			Number: 142,
			Title:  "Add rich workspace tabs popup",
			State:  "OPEN",
			URL:    "https://github.com/arhammahajan/model-router/pull/142",
		},
		Checks: &model.CheckSummary{
			State:       model.CheckFailure,
			Passed:      4,
			Total:       5,
			FailingName: "tui-width-check",
		},
	}
	gitMain := &model.GitInfo{
		RepoRoot: "/home/u/dev/model-router",
		Branch:   "main",
	}

	return []model.Space{
		{
			Name:      "model-router",
			ID:        "$0",
			IsCurrent: true,
			CWD:       "/home/u/dev/model-router",
			Git:       gitFeat,
			Windows: []model.Window{
				{ID: "@1", Index: 0, Name: "claude", Active: false, Last: true, Width: 180, Height: 48, ActivityAt: now.Add(-30 * time.Second)},
				{ID: "@2", Index: 1, Name: "editor", Active: true, Zoomed: true, Width: 180, Height: 48, ActivityAt: now.Add(-5 * time.Second)},
				{ID: "@3", Index: 2, Name: "tests", Active: false, Width: 180, Height: 48, ActivityAt: now.Add(-2 * time.Minute)},
			},
			Panes: []model.Pane{
				{
					ID: "%1", NumericID: 1, PaneIndex: 0, WindowID: "@1", WindowIndex: 0, WindowName: "claude",
					App: "claude-code", Profile: "opus", Command: "claude", PID: 4242, ChildProcessCount: 2,
					ConversationTitle: "Implement rich tabs popup",
					Status:            model.StatusNeedsYou, Kind: "permission", Text: "Allow bash `go test ./...`",
					Source: "hook", CWD: "/home/u/dev/model-router/plugins/tmux-spaces",
					Width: 90, Height: 48, IsActive: true, AgeMs: 95_000, Git: gitFeat,
					Hook: &model.HookSummary{
						Agent:         "claude",
						App:           "claude-code",
						State:         "blocked",
						Kind:          "permission",
						Event:         "pre_tool",
						TsMs:          nowMs - 95_000,
						StateSinceMs:  nowMs - 95_000,
						TurnStartedMs: nowMs - 240_000,
						Text:          "Allow bash `go test ./...`",
						Model:         "claude-opus-4",
						SessionID:     "sess-claude-01",
						WaitingOn:     "user approval for Bash tool",
						FiniteActive:  1,
						ServiceActive: 1,
						HasDetail:     true,
						Children: []model.HookChild{
							{ID: "sub-1", Kind: "research", Role: "subagent", Category: "finite", State: "working", SinceMs: nowMs - 25_000, Text: "Inspecting tmux client"},
						},
						BackgroundTasks: []model.HookChild{
							{ID: "svc-1", Role: "service", Category: "service", State: "running", SinceMs: nowMs - 600_000, Command: "vite dev --port 5173"},
						},
					},
				},
				{
					ID: "%2", NumericID: 2, PaneIndex: 1, WindowID: "@1", WindowIndex: 0, WindowName: "claude",
					App: "shell", Command: "zsh", StartCommand: "zsh -l", ForegroundCmd: "go test -v ./internal/tui/...",
					PID: 5151, ChildProcessCount: 3, Status: model.StatusIdle,
					CWD: "/home/u/dev/model-router/plugins/tmux-spaces", Width: 90, Height: 48, Git: gitFeat,
				},
				{
					ID: "%3", NumericID: 3, PaneIndex: 0, WindowID: "@2", WindowIndex: 1, WindowName: "editor",
					App: "nvim", Command: "nvim", ForegroundCmd: "nvim internal/tui/tabs.go",
					PID: 6161, ChildProcessCount: 2, Status: model.StatusIdle, Title: "tabs.go",
					CWD: "/home/u/dev/model-router", Width: 180, Height: 48, IsActive: true, WindowActive: true,
					InMode: true, Git: gitFeat,
				},
				{
					ID: "%4", NumericID: 4, PaneIndex: 0, WindowID: "@3", WindowIndex: 2, WindowName: "tests",
					App: "codex", Command: "codex", ConversationTitle: "Write tab popup tests",
					Status: model.StatusWorking, Text: "Running go test ./...",
					CWD: "/home/u/dev/model-router", Width: 180, Height: 48, IsActive: true, AgeMs: 12_000,
					Git: gitMain,
					Hook: &model.HookSummary{
						Agent: "codex", App: "codex", State: "working", Event: "tool_use",
						TsMs: nowMs - 12_000, TurnStartedMs: nowMs - 45_000, Model: "gpt-5-codex",
					},
				},
			},
		},
		{
			Name:      "other-space",
			ID:        "$1",
			IsCurrent: false,
			CWD:       "/srv/other",
			Windows: []model.Window{
				{ID: "@9", Index: 0, Name: "secret-other-tab", Active: true},
			},
			Panes: []model.Pane{
				{ID: "%9", NumericID: 9, WindowID: "@9", WindowIndex: 0, WindowName: "secret-other-tab", App: "shell", Command: "bash", Status: model.StatusIdle},
			},
		},
	}
}

func runCmdBatch(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			m = runCmdBatch(m, sub)
		}
		return m
	}
	next, nextCmd := m.Update(msg)
	m = next.(Model)
	if nextCmd != nil {
		if _, isTick := msg.(tickMsg); !isTick {
			return runCmdBatch(m, nextCmd)
		}
	}
	return m
}

func newTestTabsModel(t *testing.T, fb *fakeTabsBackend, opts Options) Model {
	t.Helper()
	opts.InitialMode = ModeTabs
	if opts.HomeDir == "" {
		opts.HomeDir = "/home/u"
	}
	if opts.Now == nil {
		opts.Now = tabsTestNow
	}
	m := New(fb, opts)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 48})
	m = next.(Model)
	next, cmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = next.(Model)
	m = runCmdBatch(m, cmd)
	return m
}

func sampleTabsBackend() *fakeTabsBackend {
	nowMs := tabsTestNow().UnixMilli()
	return &fakeTabsBackend{
		fakeBackend: fakeBackend{spaces: sampleTabsSpaces()},
		hookDetails: map[int]*model.HookDetail{
			1: {
				HookSummary: model.HookSummary{
					Agent: "claude", App: "claude-code", State: "blocked", Kind: "permission",
					Event: "pre_tool", TsMs: nowMs - 95_000, Model: "claude-opus-4", SessionID: "sess-claude-01",
					WaitingOn: "user approval for Bash tool", HasDetail: true,
				},
				Prompt: "implement the rich tab popup for mod+T with full agent telemetry",
				Tool: &model.HookTool{
					Name:   "Bash",
					Input:  "go test -race ./internal/tui/...",
					Output: "ok github.com/arhammahajan/spaces/internal/tui 0.31s",
				},
				Reply: "All unit tests passed after replacing live preview with full agent telemetry.",
			},
		},
	}
}

func TestTabsScopesToCurrentWorkspaceAndSelectsActiveTab(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	items := m.tabsItems()
	if len(items) != 3 {
		t.Fatalf("got %d tabs, want 3 from current workspace only", len(items))
	}
	if m.selected != 1 || items[m.selected].Window.ID != "@2" {
		t.Fatalf("initial selected = %d (%s), want index 1 (@2 active window)", m.selected, items[m.selected].Window.ID)
	}

	view := ansi.Strip(m.View())
	if strings.Contains(view, "secret-other-tab") {
		t.Errorf("view leaked tab from another workspace:\n%s", view)
	}
	if strings.Contains(view, "LIVE PREVIEW") {
		t.Errorf("view should not render LIVE PREVIEW:\n%s", view)
	}
	for _, want := range []string{
		"Tabs · model-router",
		"3 tabs · 4 panes",
		"1: editor",
		"zoomed",
		"180×48",
		"FOREGROUND COMMAND",
		"$ nvim internal/tui/tabs.go",
		"DETAILS",
		"pid 6161",
		"copy-mode",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}

	layout := m.tabsLayout(m.width, m.height)
	rightLines, _ := m.renderTabInspectorWithHits(m.spaces[0], items[m.selected], layout.rightWidth, layout.bodyHeight)
	if len(rightLines) == 0 {
		t.Fatalf("expected non-empty inspector lines")
	}
	topBar := ansi.Strip(rightLines[0])
	if !strings.Contains(topBar, "@2 · 180×48") {
		t.Errorf("inspector top bar missing window dimensions %q:\n%s", "@2 · 180×48", topBar)
	}
	if w := ansi.StringWidth(rightLines[0]); w != layout.rightWidth {
		t.Errorf("inspector top bar cell width = %d, want %d", w, layout.rightWidth)
	}
}

func TestTabsSessionTargetOverride(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{Target: "other-space"})

	items := m.tabsItems()
	if len(items) != 1 || items[0].Window.Name != "secret-other-tab" {
		t.Fatalf("unexpected items for --session other-space: %+v", items)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Tabs · other-space") || !strings.Contains(view, "secret-other-tab") {
		t.Fatalf("expected other-space view, got:\n%s", view)
	}
	if strings.Contains(view, "0×0") {
		t.Errorf("view should omit zero window dimensions:\n%s", view)
	}
}

func TestTabsFullAgentTelemetryMultiPaneAndScroll(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	// Move up to tab 0 ("claude" / @1), which has 2 panes (%1 agent, %2 shell).
	next, cmd := m.Update(keyType(tea.KeyUp))
	m = runCmdBatch(next.(Model), cmd)

	view := ansi.Strip(m.View())
	if strings.Contains(view, "LIVE PREVIEW") {
		t.Errorf("view should not contain LIVE PREVIEW:\n%s", view)
	}
	for _, want := range []string{
		"0: claude",
		"2 panes · @1 · 180×48",
		"%1",
		"%2",
		"←→ switch",
		"claude-code — opus",
		"claude-opus-4",
		"Implement rich tabs popup",
		"NEEDS YOU · PERMISSION",
		"Waiting on: user approval for Bash tool",
		"PROMPT",
		"implement the rich tab popup for mod+T with full agent telemetry",
		"TOOL · Bash",
		"$ go test -race ./internal/tui/...",
		"Result",
		"LATEST REPLY",
		"All unit tests passed",
		"BACKGROUND WORK · 1 active · 1 service",
		"Inspecting tmux client",
		"vite dev --port 5173",
		"GIT · model-router/plugins/tmux-spaces",
		"feat/tabs-popup → main",
		"PR #142",
		"✗ 4/5",
		"tui-width-check",
		"DETAILS",
		"sess-claude-01",
		"pre_tool",
		"pid 4242",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("full agent telemetry view missing %q:\n%s", want, view)
		}
	}

	// Verify telemetry inspector scrolls cleanly when viewport is shorter.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 130, Height: 22})
	m = next.(Model)
	topView := ansi.Strip(m.View())
	if !strings.Contains(topView, "↕ ") {
		t.Errorf("expected scroll indicator in banner on short viewport:\n%s", topView)
	}
	next, _ = m.Update(keyRunes("J"))
	m = next.(Model)
	if m.tabsInspectorScroll == 0 {
		t.Fatalf("expected 'J' to scroll telemetry inspector")
	}
	next, _ = m.Update(keyRunes("K"))
	m = next.(Model)
	if m.tabsInspectorScroll != 0 {
		t.Fatalf("expected 'K' to scroll back to 0, got %d", m.tabsInspectorScroll)
	}

	// Switch inspected pane from %1 to %2 within tab 0 -> shows Foreground Command & Details.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 48})
	m = next.(Model)
	next, cmd = m.Update(keyType(tea.KeyRight))
	m = runCmdBatch(next.(Model), cmd)

	view2 := ansi.Strip(m.View())
	for _, want := range []string{
		"FOREGROUND COMMAND · 3 child processes",
		"$ go test -v ./internal/tui/...",
		"DETAILS",
		"pid 5151",
		"zsh (from zsh -l)",
	} {
		if !strings.Contains(view2, want) {
			t.Errorf("pane %%2 process telemetry missing %q:\n%s", want, view2)
		}
	}

	// Pressing Enter focuses the exact selected window + pane (%2 in @1).
	next, _ = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("expected Enter to quit after focusing pane")
	}
	if len(fb.focused) != 1 || fb.focused[0] != "model-router/@1/%2" {
		t.Fatalf("focused = %v, want [model-router/@1/%%2]", fb.focused)
	}
}

func TestTabsPanePillClickSwitchesPane(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	// Select multi-pane tab 0 (@1, %1 and %2).
	next, cmd := m.Update(keyRunes("0"))
	m = runCmdBatch(next.(Model), cmd)

	layout := m.tabsLayout(m.width, m.height)
	_, hits := m.renderTabInspectorWithHits(m.spaces[0], m.tabsItems()[m.selected], layout.rightWidth, layout.bodyHeight)
	var secondPill *tabPaneHitRow
	for i := range hits {
		if hits[i].paneIdx == 1 {
			secondPill = &hits[i]
			break
		}
	}
	if secondPill == nil {
		t.Fatalf("expected clickable pill hit for pane index 1, got hits=%+v", hits)
	}

	clickX := layout.leftWidth + 1 + secondPill.xStart + 1
	clickY := layout.topY + secondPill.y
	next, cmd = m.Update(tea.MouseMsg{X: clickX, Y: clickY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = runCmdBatch(next.(Model), cmd)

	selPane := m.selectedTabPane(*m.selectedTab())
	if selPane == nil || selPane.ID != "%2" {
		t.Fatalf("clicking pane pill selected %+v, want %%2", selPane)
	}
}

func TestTabsDoneAgentOmitsDoneCardAndUsesFlatSections(t *testing.T) {
	spaces := sampleTabsSpaces()
	// Mark tab 2 (@3 / %4 codex) as Done with a completion message.
	spaces[0].Panes[3].Status = model.StatusDone
	spaces[0].Panes[3].Text = "turn complete"
	spaces[0].Panes[3].Hook.State = "done"
	spaces[0].Panes[3].Hook.HasDetail = true

	fb := &fakeTabsBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		hookDetails: map[int]*model.HookDetail{
			4: {
				HookSummary: model.HookSummary{Agent: "codex", App: "codex", State: "done", Model: "gpt-5-codex", HasDetail: true},
				Prompt:      "write unit tests for tabs popup",
				Tool:        &model.HookTool{Name: "Bash", Input: "go test ./..."},
				Reply:       "Added comprehensive unit tests and verified all pass.",
			},
		},
	}
	m := newTestTabsModel(t, fb, Options{})
	next, cmd := m.Update(keyRunes("2"))
	m = runCmdBatch(next.(Model), cmd)

	layout := m.tabsLayout(m.width, m.height)
	rightLines, _ := m.renderTabInspectorWithHits(m.spaces[0], m.tabsItems()[m.selected], layout.rightWidth, layout.bodyHeight)
	rhs := ansi.Strip(strings.Join(rightLines, "\n"))

	// Top-right pill shows done, so there must be no redundant DONE section or card bars.
	if !strings.Contains(rhs, "✓ done") {
		t.Errorf("expected top-right done indicator in RHS:\n%s", rhs)
	}
	if strings.Contains(rhs, "\n  DONE") {
		t.Errorf("RHS should not render a DONE block/card:\n%s", rhs)
	}
	if strings.Contains(rhs, "▌") || strings.Contains(rhs, "│ ") {
		t.Errorf("RHS should be flat with no card accent bars or quote rails:\n%s", rhs)
	}
	for _, want := range []string{
		"PROMPT",
		"write unit tests for tabs popup",
		"TOOL · Bash",
		"$ go test ./...",
		"LATEST REPLY",
		"Added comprehensive unit tests",
	} {
		if !strings.Contains(rhs, want) {
			t.Errorf("RHS missing %q:\n%s", want, rhs)
		}
	}
}

func TestTabsWaitingOnGatedByActiveOrAttentionStatus(t *testing.T) {
	tests := []struct {
		name            string
		status          model.Status
		hookState       string
		waitingOn       string
		wantPill        string
		wantShowWaiting bool
		wantHeading     string
	}{
		{
			name:            "done pane hides stale waiting_on",
			status:          model.StatusDone,
			hookState:       "done",
			waitingOn:       "stale approval from previous turn",
			wantPill:        "✓ done",
			wantShowWaiting: false,
		},
		{
			name:            "idle pane hides stale waiting_on",
			status:          model.StatusIdle,
			hookState:       "idle",
			waitingOn:       "stale subagent from previous turn",
			wantPill:        "○ idle",
			wantShowWaiting: false,
		},
		{
			name:            "working pane renders waiting_on",
			status:          model.StatusWorking,
			hookState:       "working",
			waitingOn:       "subagent: running integration tests",
			wantPill:        "◐ working",
			wantShowWaiting: true,
			wantHeading:     "WORKING",
		},
		{
			name:            "needs_you pane renders waiting_on",
			status:          model.StatusNeedsYou,
			hookState:       "blocked",
			waitingOn:       "user approval for Bash tool",
			wantPill:        "● needs you",
			wantShowWaiting: true,
			wantHeading:     "NEEDS YOU",
		},
		{
			name:            "failed pane renders waiting_on",
			status:          model.StatusFailed,
			hookState:       "error",
			waitingOn:       "retry after tool error",
			wantPill:        "✗ failed",
			wantShowWaiting: true,
			wantHeading:     "FAILED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spaces := sampleTabsSpaces()
			spaces[0].Panes[3].Status = tc.status
			spaces[0].Panes[3].Text = ""
			spaces[0].Panes[3].Hook.State = tc.hookState
			spaces[0].Panes[3].Hook.WaitingOn = tc.waitingOn
			spaces[0].Panes[3].Hook.HasDetail = true

			fb := &fakeTabsBackend{
				fakeBackend: fakeBackend{spaces: spaces},
				hookDetails: map[int]*model.HookDetail{
					4: {
						HookSummary: model.HookSummary{
							Agent:     "codex",
							App:       "codex",
							State:     tc.hookState,
							Model:     "gpt-5-codex",
							WaitingOn: tc.waitingOn,
							HasDetail: true,
						},
						Prompt: "check waiting_on rendering",
					},
				},
			}
			m := newTestTabsModel(t, fb, Options{})
			next, cmd := m.Update(keyRunes("2"))
			m = runCmdBatch(next.(Model), cmd)

			layout := m.tabsLayout(m.width, m.height)
			rightLines, _ := m.renderTabInspectorWithHits(m.spaces[0], m.tabsItems()[m.selected], layout.rightWidth, layout.bodyHeight)
			rhs := ansi.Strip(strings.Join(rightLines, "\n"))

			if !strings.Contains(rhs, tc.wantPill) {
				t.Errorf("expected top-right status pill %q in RHS:\n%s", tc.wantPill, rhs)
			}
			if tc.wantShowWaiting {
				if !strings.Contains(rhs, tc.wantHeading) {
					t.Errorf("expected status heading %q in RHS:\n%s", tc.wantHeading, rhs)
				}
				wantLine := "Waiting on: " + tc.waitingOn
				if !strings.Contains(rhs, wantLine) {
					t.Errorf("expected %q in RHS:\n%s", wantLine, rhs)
				}
			} else {
				if strings.Contains(rhs, "WAITING ON") {
					t.Errorf("RHS should not render WAITING ON section:\n%s", rhs)
				}
				if strings.Contains(rhs, "Waiting on:") || strings.Contains(rhs, tc.waitingOn) {
					t.Errorf("RHS should not render stale waiting_on %q:\n%s", tc.waitingOn, rhs)
				}
			}
		})
	}
}

func TestTabsSortingFilteringSearchAndDigitJump(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	// Press 's' to cycle sort: index -> attention (NeedsYou @1, Working @3, Idle @2).
	next, cmd := m.Update(keyRunes("s"))
	m = runCmdBatch(next.(Model), cmd)
	if m.tabsSort != TabSortAttention {
		t.Fatalf("sort = %v, want TabSortAttention", m.tabsSort)
	}
	items := m.tabsItems()
	if items[0].Window.ID != "@1" || items[1].Window.ID != "@3" || items[2].Window.ID != "@2" {
		t.Fatalf("attention sort order = [%s %s %s], want [@1 @3 @2]", items[0].Window.ID, items[1].Window.ID, items[2].Window.ID)
	}

	// Press 's' again: attention -> activity (active @2 first, then @3 with 12s hook activity, then @1 with 30s window activity).
	next, cmd = m.Update(keyRunes("s"))
	m = runCmdBatch(next.(Model), cmd)
	if m.tabsSort != TabSortActivity {
		t.Fatalf("sort = %v, want TabSortActivity", m.tabsSort)
	}
	items = m.tabsItems()
	if items[0].Window.ID != "@2" || items[1].Window.ID != "@3" || items[2].Window.ID != "@1" {
		t.Fatalf("activity sort order = [%s %s %s], want [@2 @3 @1]", items[0].Window.ID, items[1].Window.ID, items[2].Window.ID)
	}

	// Back to index sort.
	next, cmd = m.Update(keyRunes("s"))
	m = runCmdBatch(next.(Model), cmd)
	if m.tabsSort != TabSortIndex {
		t.Fatalf("sort = %v, want TabSortIndex", m.tabsSort)
	}

	// Digit jump '2' selects window index 2 (@3).
	next, cmd = m.Update(keyRunes("2"))
	m = runCmdBatch(next.(Model), cmd)
	if sel := m.selectedTab(); sel == nil || sel.Window.ID != "@3" {
		t.Fatalf("digit 2 selected %+v, want @3", sel)
	}

	// Status filter 'b' shows only blocked tab (@1).
	next, cmd = m.Update(keyRunes("b"))
	m = runCmdBatch(next.(Model), cmd)
	if got := m.tabsItems(); len(got) != 1 || got[0].Window.ID != "@1" {
		t.Fatalf("blocked filter = %+v, want [@1]", got)
	}

	// Reset filter 'a', then search by foreground cmd.
	next, cmd = m.Update(keyRunes("a"))
	m = runCmdBatch(next.(Model), cmd)
	next, _ = m.Update(keyRunes("/"))
	m = next.(Model)
	for _, r := range "nvim" {
		next, cmd = m.Update(keyRunes(string(r)))
		m = runCmdBatch(next.(Model), cmd)
	}
	if got := m.tabsItems(); len(got) != 1 || got[0].Window.ID != "@2" {
		t.Fatalf("search 'nvim' = %+v, want [@2]", got)
	}
	next, cmd = m.Update(keyType(tea.KeyEsc))
	m = runCmdBatch(next.(Model), cmd)
	if m.Quitting() || m.query != "" || m.tabsSearching {
		t.Fatalf("after Esc from search: quitting=%v query=%q searching=%v", m.Quitting(), m.query, m.tabsSearching)
	}
}

func TestTabsRenameNewAndKillFlows(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	// 1. Rename active tab (@2 "editor" -> "code"), stays in ModeTabs.
	next, _ := m.Update(keyRunes("r"))
	m = next.(Model)
	if m.ModeOf() != ModeRename || !m.renameWindow || m.renameWindowID != "@2" {
		t.Fatalf("rename state = mode:%v win:%v id:%q", m.ModeOf(), m.renameWindow, m.renameWindowID)
	}
	next, _ = m.Update(keyType(tea.KeyCtrlU))
	m = next.(Model)
	next, _ = m.Update(keyRunes("code"))
	m = next.(Model)
	next, _ = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if m.ModeOf() != ModeTabs || m.Quitting() {
		t.Fatalf("after tab rename: mode=%v quitting=%v, want ModeTabs & !quitting", m.ModeOf(), m.Quitting())
	}
	if len(fb.renamedWindows) != 1 || fb.renamedWindows[0] != [2]string{"@2", "code"} {
		t.Fatalf("renamedWindows = %v", fb.renamedWindows)
	}

	// 2. Close tab (@1 has a blocked agent -> confirm dialog warns about busy agent).
	next, cmd := m.Update(keyRunes("0"))
	m = runCmdBatch(next.(Model), cmd)
	next, _ = m.Update(keyRunes("x"))
	m = next.(Model)
	if m.ModeOf() != ModeConfirmKill || !m.confirmWindow || !m.confirmWindowBusy {
		t.Fatalf("confirm state = mode:%v win:%v busy:%v", m.ModeOf(), m.confirmWindow, m.confirmWindowBusy)
	}
	confirmView := ansi.Strip(m.View())
	if !strings.Contains(confirmView, "Close tab?") || !strings.Contains(confirmView, "working or waiting") {
		t.Errorf("confirm dialog missing busy warning:\n%s", confirmView)
	}
	next, _ = m.Update(keyRunes("y"))
	m = next.(Model)
	if m.ModeOf() != ModeTabs || len(fb.killedWindows) != 1 || fb.killedWindows[0] != "@1" {
		t.Fatalf("after kill confirm: mode=%v killed=%v", m.ModeOf(), fb.killedWindows)
	}
	if len(m.tabsItems()) != 2 {
		t.Fatalf("expected 2 remaining tabs after kill, got %d", len(m.tabsItems()))
	}

	// 3. Create new tab ('n' opens New tab dialog prefilled with CWD, Enter calls NewWindow & quits).
	next, _ = m.Update(keyRunes("n"))
	m = next.(Model)
	if m.ModeOf() != ModeNewSpace || !m.newIsTab || m.newTabSession != "model-router" {
		t.Fatalf("new tab dialog state = mode:%v isTab:%v sess:%q", m.ModeOf(), m.newIsTab, m.newTabSession)
	}
	next, _ = m.Update(keyRunes("scratch"))
	m = next.(Model)
	next, _ = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("expected creating tab to quit popup")
	}
	if len(fb.createdWindows) != 1 || fb.createdWindows[0][0] != "model-router" || fb.createdWindows[0][2] != "scratch" {
		t.Fatalf("createdWindows = %v", fb.createdWindows)
	}
}

func TestTabsOpenNewFlagFlow(t *testing.T) {
	fb := sampleTabsBackend()
	m := New(fb, Options{InitialMode: ModeTabs, OpenNew: true, HomeDir: "/home/u", Now: tabsTestNow})
	if m.ModeOf() != ModeNewSpace || !m.newIsTab || m.returnMode != ModeTabs {
		t.Fatalf("initial state = mode:%v isTab:%v return:%v", m.ModeOf(), m.newIsTab, m.returnMode)
	}

	// Typing before spacesMsg arrives must be preserved when spaces load.
	next, _ := m.Update(keyRunes("api"))
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = next.(Model)
	next, cmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = runCmdBatch(next.(Model), cmd)

	if m.newName != "api" || m.newTabSession != "model-router" || m.newCWD != "/home/u/dev/model-router" {
		t.Fatalf("prefilled new tab = name:%q sess:%q cwd:%q", m.newName, m.newTabSession, m.newCWD)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "New tab · model-router") {
		t.Errorf("new tab view missing title:\n%s", view)
	}

	// Esc exits the new-tab dialog into the Tabs popup rather than quitting.
	next, cmd = m.Update(keyType(tea.KeyEsc))
	m = runCmdBatch(next.(Model), cmd)
	if m.Quitting() || m.ModeOf() != ModeTabs {
		t.Fatalf("Esc from tabs --new: quitting=%v mode=%v, want !quitting & ModeTabs", m.Quitting(), m.ModeOf())
	}

	// Re-enter new tab flow and confirm creation.
	next, _ = m.Update(keyRunes("n"))
	m = next.(Model)
	next, _ = m.Update(keyRunes("logs"))
	m = next.(Model)
	next, _ = m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("expected Enter to quit after creating tab")
	}
	if len(fb.createdWindows) != 1 || fb.createdWindows[0] != [3]string{"model-router", "/home/u/dev/model-router", "logs"} {
		t.Fatalf("createdWindows = %v", fb.createdWindows)
	}
}

func TestTabsOverlaysExpandAndWidthInvariants(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{})

	// Select tab 0 (@1, claude) and open Hook Detail ('H'), then Esc returns to ModeTabs.
	next, cmd := m.Update(keyRunes("0"))
	m = runCmdBatch(next.(Model), cmd)
	next, cmd = m.Update(keyRunes("H"))
	m = runCmdBatch(next.(Model), cmd)
	if m.ModeOf() != ModeHookDetail {
		t.Fatalf("expected ModeHookDetail, got %v", m.ModeOf())
	}
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(Model)
	if m.ModeOf() != ModeTabs {
		t.Fatalf("Esc from ModeHookDetail = %v, want ModeTabs", m.ModeOf())
	}

	// Open Help ('?'), verify Tabs keys shown, Esc returns to ModeTabs.
	next, _ = m.Update(keyRunes("?"))
	m = next.(Model)
	if m.ModeOf() != ModeHelp || !strings.Contains(ansi.Strip(m.View()), "Tabs · keys") {
		t.Fatalf("expected Tabs help dialog, got mode=%v", m.ModeOf())
	}
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(Model)
	if m.ModeOf() != ModeTabs {
		t.Fatalf("Esc from ModeHelp = %v, want ModeTabs", m.ModeOf())
	}

	// Verify balanced split, expanded telemetry ('v'), narrow compact, and tiny viewports.
	for _, tc := range []struct {
		name string
		w, h int
		key  string
	}{
		{"wide-split", 140, 36, ""},
		{"wide-expanded-telemetry", 140, 36, "v"},
		{"wide-balanced-again", 140, 36, "v"},
		{"narrow-compact", 76, 22, ""},
		{"tiny-viewport", 14, 3, ""},
	} {
		next, _ = m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
		m = next.(Model)
		if tc.key != "" {
			next, cmd = m.Update(keyRunes(tc.key))
			m = runCmdBatch(next.(Model), cmd)
		}
		rendered := m.View()
		if tc.w >= 16 && tc.h >= 4 {
			for i, ln := range strings.Split(rendered, "\n") {
				if w := ansi.StringWidth(ln); w > tc.w {
					t.Errorf("%s line %d width %d > viewport %d: %q", tc.name, i, w, tc.w, ansi.Strip(ln))
				}
			}
		}
	}
}

func TestTabsHookDetailCachingAndRefresh(t *testing.T) {
	fb := sampleTabsBackend()
	// Make tab 0 (@1 / %1 with HasDetail=true) the active window so initial spacesMsg selects it.
	fb.spaces[0].Windows[0].Active = true
	fb.spaces[0].Windows[1].Active = false
	fb.spaces[0].Panes[0].WindowActive = true
	fb.spaces[0].Panes[2].WindowActive = false

	// 1. Initial load fetches hook detail once.
	m := newTestTabsModel(t, fb, Options{})
	if got := fb.hookDetailCalls.Load(); got != 1 {
		t.Fatalf("initial LoadHookDetail calls = %d, want 1", got)
	}
	if m.tabsHookInFlight != "" {
		t.Fatalf("tabsHookInFlight = %q after completion, want empty", m.tabsHookInFlight)
	}

	// 2. Subsequent spacesMsg ticks with unchanged Hook.TsMs do NOT trigger additional LoadHookDetail calls.
	for i := 0; i < 3; i++ {
		next, cmd := m.Update(spacesMsg{spaces: fb.spaces})
		m = runCmdBatch(next.(Model), cmd)
	}
	if got := fb.hookDetailCalls.Load(); got != 1 {
		t.Fatalf("after unchanged spacesMsg ticks, LoadHookDetail calls = %d, want 1", got)
	}

	// 3. A spacesMsg with a newer Hook.TsMs DOES refresh LoadHookDetail.
	newerTs := fb.spaces[0].Panes[0].Hook.TsMs + 5_000
	updatedSpaces := sampleTabsSpaces()
	updatedSpaces[0].Windows[0].Active = true
	updatedSpaces[0].Windows[1].Active = false
	updatedSpaces[0].Panes[0].WindowActive = true
	updatedSpaces[0].Panes[2].WindowActive = false
	updatedSpaces[0].Panes[0].Hook.TsMs = newerTs
	fb.spaces = updatedSpaces
	fb.hookDetails[1] = &model.HookDetail{
		HookSummary: model.HookSummary{
			Agent: "claude", App: "claude-code", State: "blocked", Kind: "permission",
			Event: "pre_tool", TsMs: newerTs, Model: "claude-opus-4", SessionID: "sess-claude-01",
			WaitingOn: "user approval for Bash tool", HasDetail: true,
		},
		Prompt: "updated prompt after newer hook event",
	}

	// Also verify in-flight deduplication: dispatch spacesMsg without executing cmd yet,
	// then send a second spacesMsg while the first hook detail fetch is still in flight.
	next, firstCmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = next.(Model)
	if firstCmd == nil {
		t.Fatalf("expected sync command when Hook.TsMs advanced")
	}
	if m.tabsHookInFlight == "" {
		t.Fatalf("expected tabsHookInFlight to be set while fetch is pending")
	}
	next, dupCmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = next.(Model)
	m = runCmdBatch(m, dupCmd)
	if got := fb.hookDetailCalls.Load(); got != 1 {
		t.Fatalf("duplicate in-flight spacesMsg triggered LoadHookDetail (%d calls), want 1", got)
	}

	// Now complete the original in-flight fetch.
	m = runCmdBatch(m, firstCmd)
	if got := fb.hookDetailCalls.Load(); got != 2 {
		t.Fatalf("after newer Hook.TsMs refresh, LoadHookDetail calls = %d, want 2", got)
	}
	if m.tabsHookInFlight != "" {
		t.Fatalf("tabsHookInFlight = %q after newer refresh completed, want empty", m.tabsHookInFlight)
	}
	if got := m.bgDetails[paneBgKey("model-router", "%1")]; got == nil || got.Prompt != "updated prompt after newer hook event" {
		t.Fatalf("cached detail not updated: %+v", got)
	}

	// Another unchanged tick at newerTs should remain cached (no extra call).
	next, cmd := m.Update(spacesMsg{spaces: fb.spaces})
	m = runCmdBatch(next.(Model), cmd)
	if got := fb.hookDetailCalls.Load(); got != 2 {
		t.Fatalf("after unchanged tick at newerTs, LoadHookDetail calls = %d, want 2", got)
	}

	// 4. Explicit Ctrl-r DOES force a LoadHookDetail reload (exactly once, even with spacesMsg in batch).
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = runCmdBatch(next.(Model), cmd)
	if got := fb.hookDetailCalls.Load(); got != 3 {
		t.Fatalf("after explicit Ctrl-r, LoadHookDetail calls = %d, want 3", got)
	}
	if m.tabsHookInFlight != "" {
		t.Fatalf("tabsHookInFlight = %q after Ctrl-r completed, want empty", m.tabsHookInFlight)
	}

	// 5. Error responses also clear tabsHookInFlight so future refreshes are not wedged.
	fb.hookDetailErr = errors.New("hook store busy")
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = runCmdBatch(next.(Model), cmd)
	if got := fb.hookDetailCalls.Load(); got != 4 {
		t.Fatalf("after failing Ctrl-r, LoadHookDetail calls = %d, want 4", got)
	}
	if m.tabsHookInFlight != "" {
		t.Fatalf("tabsHookInFlight = %q after error hookDetailMsg, want empty", m.tabsHookInFlight)
	}
}

func extractSpacesMsg(t *testing.T, cmd tea.Cmd) spacesMsg {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected non-nil command containing spacesMsg")
	}
	var walk func(c tea.Cmd) (spacesMsg, bool)
	walk = func(c tea.Cmd) (spacesMsg, bool) {
		if c == nil {
			return spacesMsg{}, false
		}
		msg := c()
		if sm, ok := msg.(spacesMsg); ok {
			return sm, true
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sm, ok := walk(sub); ok {
					return sm, true
				}
			}
		}
		return spacesMsg{}, false
	}
	sm, ok := walk(cmd)
	if !ok {
		t.Fatalf("command did not produce a spacesMsg")
	}
	return sm
}

func TestTabsStaleSnapshotIgnoredAfterKillAndPostKillApplied(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{RefreshInterval: time.Millisecond})

	// Start an in-flight pre-kill refresh (captures all 3 windows including @1).
	next, preKillCmd := m.Update(tickMsg(tabsTestNow()))
	m = next.(Model)
	preKillMsg := extractSpacesMsg(t, preKillCmd)
	if preKillMsg.seq == 0 || preKillMsg.seq != m.spacesSeq {
		t.Fatalf("preKillMsg.seq = %d, model spacesSeq = %d", preKillMsg.seq, m.spacesSeq)
	}

	// Select tab 0 (@1) and kill it.
	next, cmd := m.Update(keyRunes("0"))
	m = runCmdBatch(next.(Model), cmd)
	next, _ = m.Update(keyRunes("x"))
	m = next.(Model)
	next, postKillCmd := m.Update(keyRunes("y"))
	m = next.(Model)
	if len(m.tabsItems()) != 2 {
		t.Fatalf("expected 2 tabs immediately after kill, got %d", len(m.tabsItems()))
	}
	if m.spacesSeq <= preKillMsg.seq {
		t.Fatalf("expected spacesSeq (%d) to advance past preKillMsg.seq (%d)", m.spacesSeq, preKillMsg.seq)
	}

	// Stale pre-kill snapshot arriving after confirmKill must be ignored and not resurrect @1.
	next, staleCmd := m.Update(preKillMsg)
	m = next.(Model)
	if staleCmd != nil {
		t.Fatalf("expected nil cmd for ignored stale spacesMsg")
	}
	items := m.tabsItems()
	if len(items) != 2 {
		t.Fatalf("stale pre-kill snapshot resurrected deleted tab: got %d tabs (%+v)", len(items), items)
	}
	for _, it := range items {
		if it.Window.ID == "@1" {
			t.Fatalf("stale pre-kill snapshot resurrected deleted window @1")
		}
	}

	// Update backend to post-kill state (with a marker rename on @3) and deliver the post-kill snapshot.
	postSpaces := sampleTabsSpaces()
	postSpaces[0].Windows = postSpaces[0].Windows[1:]
	postSpaces[0].Windows[1].Name = "tests-post-kill"
	postSpaces[0].Panes = postSpaces[0].Panes[2:]
	postSpaces[0].Panes[1].WindowName = "tests-post-kill"
	fb.spaces = postSpaces

	postKillMsg := extractSpacesMsg(t, postKillCmd)
	if postKillMsg.seq != m.spacesSeq {
		t.Fatalf("postKillMsg.seq = %d, want %d", postKillMsg.seq, m.spacesSeq)
	}
	next, cmd = m.Update(postKillMsg)
	m = runCmdBatch(next.(Model), cmd)

	items = m.tabsItems()
	if len(items) != 2 {
		t.Fatalf("after post-kill snapshot: got %d tabs, want 2", len(items))
	}
	foundPostKill := false
	for _, it := range items {
		if it.Window.ID == "@1" {
			t.Fatalf("post-kill snapshot unexpectedly included @1")
		}
		if it.Window.ID == "@3" && it.Window.Name == "tests-post-kill" {
			foundPostKill = true
		}
	}
	if !foundPostKill {
		t.Fatalf("post-kill snapshot was not applied: items=%+v", items)
	}
}

func TestTabsStaleSnapshotIgnoredAfterRename(t *testing.T) {
	fb := sampleTabsBackend()
	m := newTestTabsModel(t, fb, Options{RefreshInterval: time.Millisecond})

	// Start an in-flight pre-rename refresh while @2 is still named "editor".
	next, preRenameCmd := m.Update(tickMsg(tabsTestNow()))
	m = next.(Model)
	preRenameMsg := extractSpacesMsg(t, preRenameCmd)

	// Rename active tab @2 ("editor" -> "code") in ModeTabs.
	next, _ = m.Update(keyRunes("r"))
	m = next.(Model)
	next, _ = m.Update(keyType(tea.KeyCtrlU))
	m = next.(Model)
	next, _ = m.Update(keyRunes("code"))
	m = next.(Model)
	next, postRenameCmd := m.Update(keyType(tea.KeyEnter))
	m = next.(Model)
	if m.ModeOf() != ModeTabs {
		t.Fatalf("mode = %v, want ModeTabs", m.ModeOf())
	}
	if m.spacesSeq <= preRenameMsg.seq {
		t.Fatalf("expected spacesSeq (%d) > preRenameMsg.seq (%d)", m.spacesSeq, preRenameMsg.seq)
	}

	// Stale pre-rename snapshot must be ignored and must not revert @2 back to "editor".
	next, staleCmd := m.Update(preRenameMsg)
	m = next.(Model)
	if staleCmd != nil {
		t.Fatalf("expected nil cmd for ignored stale pre-rename spacesMsg")
	}
	for _, w := range m.spaces[0].Windows {
		if w.ID == "@2" && w.Name != "code" {
			t.Fatalf("stale pre-rename snapshot reverted @2 name to %q, want %q", w.Name, "code")
		}
	}
	for _, p := range m.spaces[0].Panes {
		if p.WindowID == "@2" && p.WindowName != "code" {
			t.Fatalf("stale pre-rename snapshot reverted @2 pane WindowName to %q, want %q", p.WindowName, "code")
		}
	}

	// Post-rename snapshot with matching seq IS applied.
	postSpaces := sampleTabsSpaces()
	postSpaces[0].Windows[1].Name = "code"
	postSpaces[0].Panes[2].WindowName = "code"
	postSpaces[0].Windows[2].Name = "tests-after-rename"
	fb.spaces = postSpaces

	postRenameMsg := extractSpacesMsg(t, postRenameCmd)
	next, cmd := m.Update(postRenameMsg)
	m = runCmdBatch(next.(Model), cmd)
	if m.spaces[0].Windows[1].Name != "code" || m.spaces[0].Windows[2].Name != "tests-after-rename" {
		t.Fatalf("post-rename snapshot not applied: windows=%+v", m.spaces[0].Windows)
	}
}

func TestTabsOutOfOrderTickSnapshotIgnored(t *testing.T) {
	fb := sampleTabsBackend()
	m := New(fb, Options{InitialMode: ModeTabs, HomeDir: "/home/u", Now: tabsTestNow, RefreshInterval: time.Millisecond})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 48})
	m = next.(Model)

	// Init load carries seq 1 with initial window name "editor".
	initMsg := extractSpacesMsg(t, m.Init())
	if initMsg.seq != 1 {
		t.Fatalf("Init spacesMsg.seq = %d, want 1", initMsg.seq)
	}

	// Trigger tick #1 (seq 2) while backend has "editor-old".
	oldSpaces := sampleTabsSpaces()
	oldSpaces[0].Windows[1].Name = "editor-old"
	fb.spaces = oldSpaces
	next, olderTickCmd := m.Update(tickMsg(tabsTestNow()))
	m = next.(Model)
	olderTickMsg := extractSpacesMsg(t, olderTickCmd)
	if olderTickMsg.seq <= initMsg.seq {
		t.Fatalf("olderTickMsg.seq = %d, want > %d", olderTickMsg.seq, initMsg.seq)
	}

	// Trigger a newer Ctrl-r refresh (seq 3) while backend has "editor-new", and apply it first.
	newSpaces := sampleTabsSpaces()
	newSpaces[0].Windows[1].Name = "editor-new"
	fb.spaces = newSpaces
	next, newerCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = next.(Model)
	newerMsg := extractSpacesMsg(t, newerCmd)
	if newerMsg.seq <= olderTickMsg.seq {
		t.Fatalf("newerMsg.seq = %d, want > %d", newerMsg.seq, olderTickMsg.seq)
	}
	next, cmd := m.Update(newerMsg)
	m = runCmdBatch(next.(Model), cmd)
	if m.spaces[0].Windows[1].Name != "editor-new" {
		t.Fatalf("newer refresh not applied: got %q, want %q", m.spaces[0].Windows[1].Name, "editor-new")
	}

	// Delivering the older tick snapshot or Init snapshot out-of-order must be ignored.
	for _, stale := range []spacesMsg{olderTickMsg, initMsg} {
		next, staleCmd := m.Update(stale)
		m = next.(Model)
		if staleCmd != nil {
			t.Fatalf("expected nil cmd for out-of-order spacesMsg seq=%d", stale.seq)
		}
		if m.spaces[0].Windows[1].Name != "editor-new" {
			t.Fatalf("out-of-order spacesMsg seq=%d overwrote window name with %q", stale.seq, m.spaces[0].Windows[1].Name)
		}
	}
}

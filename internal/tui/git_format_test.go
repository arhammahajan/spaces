package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeGitBackend struct {
	fakeBackend
	gitResult map[string]*model.GitInfo
	prResult  map[string]*model.GitInfo
	gitCalls  int
	prCalls   int
	lastForce bool
}

func (f *fakeGitBackend) LoadGitStatus(_ context.Context, _ []string, force bool) map[string]*model.GitInfo {
	f.gitCalls++
	f.lastForce = force
	return f.gitResult
}

func (f *fakeGitBackend) LoadPRStatus(_ context.Context, _ map[string]*model.GitInfo, force bool) map[string]*model.GitInfo {
	f.prCalls++
	f.lastForce = force
	return f.prResult
}

func TestSpacesPopupRendersGitBranchDiffAndPRBadges(t *testing.T) {
	gitSpace := &model.GitInfo{
		Branch:             "feat/auth-badges",
		BaseBranch:         "main",
		LinesAdded:         42,
		LinesRemoved:       8,
		IsDirty:            true,
		UncommittedAdded:   5,
		UncommittedRemoved: 2,
		Ahead:              1,
		PR: &model.PRSummary{
			Number: 42,
			Title:  "Add git badges",
			State:  "OPEN",
		},
		Checks: &model.CheckSummary{
			State:       model.CheckFailure,
			Passed:      2,
			Total:       3,
			FailingName: "lint",
		},
	}
	gitMerged := &model.GitInfo{
		Branch: "feat/done-pr",
		PR: &model.PRSummary{
			Number: 39,
			State:  "MERGED",
		},
		Checks: &model.CheckSummary{
			State: model.CheckSuccess,
		},
	}
	spaces := []model.Space{
		{
			Name:      "router",
			IsCurrent: true,
			CWD:       "/home/u/router",
			Git:       gitSpace,
			Status:    model.StatusWorking,
			Counts:    model.Counts{Working: 1, Done: 1},
			Panes: []model.Pane{
				{
					ID:         "%1",
					NumericID:  1,
					WindowID:   "@1",
					WindowName: "claude",
					App:        "claude",
					Command:    "claude",
					Title:      "Fix auth",
					Status:     model.StatusWorking,
					Text:       "go test ./...",
					CWD:        "/home/u/router",
					Git:        gitSpace,
				},
				{
					ID:         "%2",
					NumericID:  2,
					WindowID:   "@2",
					WindowName: "codex",
					App:        "codex",
					Command:    "codex",
					Title:      "Clean up",
					Status:     model.StatusDone,
					CWD:        "/home/u/router-wt",
					Git:        gitMerged,
				},
			},
		},
	}

	m := New(&fakeBackend{spaces: spaces}, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 140, 26

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"feat/auth-badges",
		"+42 -8",
		"* +5 -2",
		"↑1",
		"#42 ● ✗ 2/3",
		"feat/done-pr",
		"#39 ◆ ✓",
		"PR #42 open",
		"CI ✗ 2/3 (lint)",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("Spaces popup view missing %q:\n%s", want, plain)
		}
	}
}

func TestInboxPopupRendersGitBranchDiffAndPRBadges(t *testing.T) {
	gitDraft := &model.GitInfo{
		Branch:             "fix/sse-drain",
		BaseBranch:         "main",
		IsDirty:            true,
		UncommittedAdded:   18,
		UncommittedRemoved: 4,
		PR: &model.PRSummary{
			Number:  77,
			Title:   "Drain SSE stream cleanly",
			State:   "OPEN",
			IsDraft: true,
		},
		Checks: &model.CheckSummary{
			State:  model.CheckPending,
			Passed: 1,
			Total:  4,
		},
	}
	spaces := []model.Space{
		{
			Name:      "model-router",
			IsCurrent: true,
			CWD:       "/home/u/model-router",
			Git:       gitDraft,
			Status:    model.StatusNeedsYou,
			Counts:    model.Counts{Needs: 1},
			Panes: []model.Pane{
				{
					ID:         "%10",
					NumericID:  10,
					WindowID:   "@1",
					WindowName: "claude",
					App:        "claude",
					Command:    "claude",
					Title:      "Drain SSE",
					Status:     model.StatusNeedsYou,
					Text:       "Allow bash?",
					CWD:        "/home/u/model-router",
					Git:        gitDraft,
					AgeMs:      30000,
				},
			},
		},
	}

	m := New(&fakeBackend{spaces: spaces}, Options{InitialMode: ModeInbox, HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 140, 24

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"fix/sse-drain",
		"* +18 -4",
		"#77 ○ ◷ 1/4",
		"PR #77 draft",
		"CI ◷ 1/4",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("Inbox popup view missing %q:\n%s", want, plain)
		}
	}
}

func TestSearchMatchesGitBranchAndPRNumberInSpacesAndInbox(t *testing.T) {
	spaces := []model.Space{
		{
			Name: "alpha",
			CWD:  "/home/u/alpha",
			Git:  &model.GitInfo{Branch: "main"},
			Panes: []model.Pane{
				{ID: "%1", NumericID: 1, WindowID: "@1", App: "claude", Command: "claude", Status: model.StatusIdle, CWD: "/home/u/alpha", Git: &model.GitInfo{Branch: "main"}},
			},
		},
		{
			Name: "beta",
			CWD:  "/home/u/beta",
			Git:  &model.GitInfo{Branch: "feat/oauth-pkce", PR: &model.PRSummary{Number: 123, State: "OPEN"}},
			Panes: []model.Pane{
				{ID: "%2", NumericID: 2, WindowID: "@2", App: "codex", Command: "codex", Status: model.StatusWorking, CWD: "/home/u/beta", Git: &model.GitInfo{Branch: "feat/oauth-pkce", PR: &model.PRSummary{Number: 123, State: "OPEN"}}},
			},
		},
	}

	// Spaces search by branch.
	m := New(&fakeBackend{spaces: spaces}, Options{})
	m.spaces = spaces
	m.query = "oauth-pkce"
	rows := m.visibleRows()
	foundBeta := false
	foundAlpha := false
	for _, r := range rows {
		if r.Kind == RowSpace {
			if m.spaces[r.SpaceIdx].Name == "beta" {
				foundBeta = true
			}
			if m.spaces[r.SpaceIdx].Name == "alpha" {
				foundAlpha = true
			}
		}
	}
	if !foundBeta || foundAlpha {
		t.Fatalf("Spaces branch search: foundBeta=%v foundAlpha=%v", foundBeta, foundAlpha)
	}

	// Inbox search by PR number (#123).
	mi := New(&fakeBackend{spaces: spaces}, Options{InitialMode: ModeInbox})
	mi.spaces = spaces
	mi.query = "#123"
	items := mi.inboxItems()
	if len(items) != 1 || mi.spaces[items[0].SpaceIdx].Panes[items[0].PaneIdx].ID != "%2" {
		t.Fatalf("Inbox PR search got %+v, want single match %%2", items)
	}
}

func TestAsyncGitAndPRFetchLifecycle(t *testing.T) {
	spaces := []model.Space{
		{
			Name: "work",
			CWD:  "/repo/work",
			Panes: []model.Pane{
				{ID: "%1", NumericID: 1, WindowID: "@1", App: "claude", Command: "claude", Status: model.StatusWorking, CWD: "/repo/work"},
			},
		},
	}
	gitOnly := map[string]*model.GitInfo{
		"/repo/work": {RepoKey: "/repo/.git", RepoRoot: "/repo/work", Branch: "feat/async", UncommittedAdded: 7},
	}
	withPR := map[string]*model.GitInfo{
		"/repo/work": {
			RepoKey:          "/repo/.git",
			RepoRoot:         "/repo/work",
			Branch:           "feat/async",
			UncommittedAdded: 7,
			PR:               &model.PRSummary{Number: 9, State: "OPEN"},
			Checks:           &model.CheckSummary{State: model.CheckSuccess},
		},
	}
	fb := &fakeGitBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		gitResult:   gitOnly,
		prResult:    withPR,
	}

	m := New(fb, Options{})
	// 1. spacesMsg triggers background LoadGitStatus.
	u, cmd := m.Update(spacesMsg{spaces: spaces})
	m = u.(Model)
	if cmd == nil {
		t.Fatal("expected spacesMsg with GitBackend to return git fetch cmd")
	}
	msg := cmd()
	// 2. gitInfoMsg populates branch/diff and triggers background LoadPRStatus.
	u, prCmd := m.Update(msg)
	m = u.(Model)
	if m.spaces[0].Git == nil || m.spaces[0].Git.Branch != "feat/async" {
		t.Fatalf("expected Space.Git populated, got %+v", m.spaces[0].Git)
	}
	if m.spaces[0].Panes[0].Git == nil || m.spaces[0].Panes[0].Git.UncommittedAdded != 7 {
		t.Fatalf("expected Pane.Git populated, got %+v", m.spaces[0].Panes[0].Git)
	}
	if prCmd == nil {
		t.Fatal("expected gitInfoMsg to trigger PR fetch cmd")
	}
	// 3. prInfoMsg attaches PR/CI badges.
	u, _ = m.Update(prCmd())
	m = u.(Model)
	if m.spaces[0].Panes[0].Git.PR == nil || m.spaces[0].Panes[0].Git.PR.Number != 9 {
		t.Fatalf("expected PR #9 attached, got %+v", m.spaces[0].Panes[0].Git)
	}
	// 4. Subsequent spacesMsg preserves cached gitInfo without flicker.
	freshSpaces := []model.Space{
		{
			Name: "work",
			CWD:  "/repo/work",
			Panes: []model.Pane{
				{ID: "%1", NumericID: 1, WindowID: "@1", App: "claude", Command: "claude", Status: model.StatusWorking, CWD: "/repo/work"},
			},
		},
	}
	u, _ = m.Update(spacesMsg{spaces: freshSpaces})
	m = u.(Model)
	if m.spaces[0].Panes[0].Git == nil || m.spaces[0].Panes[0].Git.PR == nil || m.spaces[0].Panes[0].Git.PR.Number != 9 {
		t.Fatalf("expected subsequent spacesMsg to retain gitInfo, got %+v", m.spaces[0].Panes[0].Git)
	}
	// 5. Ctrl-r triggers forced refresh.
	m.gitFetching = false
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
}

func TestMainBranchHidesPassingBranchOnlyChecks(t *testing.T) {
	mainPass := &model.GitInfo{
		Branch: "main",
		Checks: &model.CheckSummary{State: model.CheckSuccess},
	}
	if mainPass.ShouldShowChecks() {
		t.Fatal("main branch without PR should hide passing checks")
	}
	mainFail := &model.GitInfo{
		Branch: "main",
		Checks: &model.CheckSummary{State: model.CheckFailure, Passed: 1, Total: 2},
	}
	if !mainFail.ShouldShowChecks() {
		t.Fatal("main branch should still display failing checks")
	}
	mainWithPR := &model.GitInfo{
		Branch: "main",
		PR:     &model.PRSummary{Number: 5, State: "OPEN"},
		Checks: &model.CheckSummary{State: model.CheckSuccess},
	}
	if !mainWithPR.ShouldShowChecks() {
		t.Fatal("branch with PR should always display checks")
	}
}

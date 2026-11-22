package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

type fakeWorktreeBackend struct {
	fakeBackend
	inspectCtx   model.WorktreeContext
	inspectErr   error
	allWorktrees []model.WorktreeEntry
	safety       WorktreeRemoveSafety

	createdTabs    []WorktreeCreateRequest
	createdSpaces  []WorktreeCreateRequest
	createdWindows [][3]string
	killedWindows  []string
	removedPaths   []string
	removedForce   bool
	removedBranch  bool
	prunedCalls    int
	pruneSummary   string
	suggestCounter int
}

func (f *fakeWorktreeBackend) NewWindow(_ context.Context, session, cwd, name string) error {
	f.createdWindows = append(f.createdWindows, [3]string{session, cwd, name})
	return nil
}

func (f *fakeWorktreeBackend) KillWindow(_ context.Context, windowID string) error {
	f.killedWindows = append(f.killedWindows, windowID)
	return nil
}

func (f *fakeWorktreeBackend) InspectWorktrees(_ context.Context, _ string, _ WorktreeNamingInput) (model.WorktreeContext, error) {
	return f.inspectCtx, f.inspectErr
}

func (f *fakeWorktreeBackend) SuggestWorktreeName(_ context.Context, _ string, naming WorktreeNamingInput) (branch, dirName, source, rule string) {
	if strings.TrimSpace(naming.ExplicitName) != "" {
		b := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(naming.ExplicitName), " ", "-"))
		return b, strings.ReplaceAll(b, "/", "-"), "custom", "explicit"
	}
	if strings.TrimSpace(naming.TargetName) != "" {
		b := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(naming.TargetName), " ", "-"))
		return b, strings.ReplaceAll(b, "/", "-"), "from " + naming.TargetKind + " name", "target_name"
	}
	if naming.Seed == 0 && strings.TrimSpace(naming.HarnessTitle) != "" {
		b := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(naming.HarnessTitle), " ", "-"))
		return b, b, "from " + naming.HarnessApp + " title", "harness_title"
	}
	if naming.Seed > 0 {
		b := "amber-falcon-" + itoa(int(naming.Seed))
		return b, b, "auto", "fallback"
	}
	return "swift-cedar", "swift-cedar", "auto", "fallback"
}

func (f *fakeWorktreeBackend) ListAllWorktrees(_ context.Context, _ []model.Space) ([]model.WorktreeEntry, error) {
	return f.allWorktrees, nil
}

func (f *fakeWorktreeBackend) CreateTab(_ context.Context, req WorktreeCreateRequest) error {
	f.createdTabs = append(f.createdTabs, req)
	return nil
}

func (f *fakeWorktreeBackend) CreateSpaceWithWorktree(_ context.Context, req WorktreeCreateRequest) error {
	f.createdSpaces = append(f.createdSpaces, req)
	return nil
}

func (f *fakeWorktreeBackend) InspectWorktreeSafety(_ context.Context, _ string) (WorktreeRemoveSafety, error) {
	return f.safety, nil
}

func (f *fakeWorktreeBackend) RemoveWorktree(_ context.Context, path string, force, deleteBranch bool, _ []string) error {
	f.removedPaths = append(f.removedPaths, path)
	f.removedForce = force
	f.removedBranch = deleteBranch
	return nil
}

func (f *fakeWorktreeBackend) PruneWorktrees(_ context.Context, _ []model.Space, _ bool) (string, error) {
	f.prunedCalls++
	if f.pruneSummary != "" {
		return f.pruneSummary, nil
	}
	return "cleaned 1 stale record(s)", nil
}

func TestNewTabDialogExistingAndNewWorktreeAndOptionalName(t *testing.T) {
	spaces := []model.Space{
		{
			Name:      "router",
			IsCurrent: true,
			CWD:       "/home/u/dev/router",
			Panes: []model.Pane{
				{
					ID:                "%1",
					NumericID:         1,
					WindowID:          "@1",
					App:               "claude-code",
					Command:           "claude",
					ConversationTitle: "OAuth Refresh",
					IsActive:          true,
					CWD:               "/home/u/dev/router",
				},
			},
		},
	}
	wb := &fakeWorktreeBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		inspectCtx: model.WorktreeContext{
			InRepo:        true,
			RepoName:      "router",
			MainRepoRoot:  "/home/u/dev/router",
			WorktreeRoot:  "/home/u/.local/share/tmux-spaces/worktrees",
			DefaultBase:   "main",
			CurrentBranch: "feat/current",
			CurrentPath:   "/home/u/dev/router",
			HasCommits:    true,
			Worktrees: []model.WorktreeEntry{
				{Path: "/home/u/dev/router", Name: "router", Branch: "main", IsMain: true, IsCurrent: true, RepoName: "router"},
				{Path: "/home/u/.local/share/tmux-spaces/worktrees/router/feat-auth", Name: "feat-auth", Branch: "feat/auth", RepoName: "router", Managed: true, OpenWindowCount: 1},
			},
			Branches:   []string{"main", "feat/current", "feat/auth"},
			AutoBranch: "oauth-refresh",
			AutoSource: "from claude-code title",
		},
	}

	// Open unified mod+c (`tmux-spaces tabs --new`) New Tab flow inside the Tabs popup.
	m := New(wb, Options{InitialMode: ModeTabs, OpenNew: true, HomeDir: "/home/u"})
	m.width, m.height = 120, 32
	u, cmd := m.Update(spacesMsg{spaces: spaces})
	m = u.(Model)
	if cmd == nil {
		t.Fatal("expected spacesMsg in tabs --new to trigger inspectWorktreesCmd")
	}
	// Resolve inspect msg.
	m = update(m, wtInspectMsg{dir: "/home/u/dev/router", ctx: wb.inspectCtx})

	// 1. Live auto-name should come from harness conversation title when tab name is blank.
	if m.wtAutoBranch != "oauth-refresh" {
		t.Fatalf("wtAutoBranch = %q, want oauth-refresh", m.wtAutoBranch)
	}

	// 2. Toggle to New worktree with Ctrl-t, verify rendered dialog shows auto-name & path preview,
	// and blank tab name stays empty (preserving tmux automatic-rename).
	m = update(m, keyType(tea.KeyCtrlT))
	if !m.wtUseNew {
		t.Fatal("expected Ctrl-t to switch to New worktree mode")
	}
	if m.newName != "" {
		t.Fatalf("expected blank tab name to stay blank in New worktree mode, got %q", m.newName)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"New tab · router", "optional · shows active process", "[New worktree]", "oauth-refresh", "from claude-code title", "Base ref"} {
		if !strings.Contains(view, want) {
			t.Errorf("NewTab dialog missing %q:\n%s", want, view)
		}
	}

	// 3. Typing an optional tab name updates the live auto-name suggestion.
	for _, r := range "Fix Race" {
		m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.wtAutoBranch != "fix-race" || m.wtAutoSource != "from tab name" {
		t.Fatalf("after typing tab name: branch=%q source=%q", m.wtAutoBranch, m.wtAutoSource)
	}

	// 4. Clearing tab name (Ctrl-u) + Ctrl-g regenerates a fresh random auto-name.
	m = update(m, keyType(tea.KeyCtrlU))
	m = update(m, keyType(tea.KeyCtrlG))
	if m.wtAutoSeed != 1 || m.wtAutoBranch != "amber-falcon-1" {
		t.Fatalf("after Ctrl-g: seed=%d branch=%q", m.wtAutoSeed, m.wtAutoBranch)
	}

	// 5. Submit with blank tab name -> CreateTab receives empty TabName (preserves tmux automatic-rename!) and CreateNew=true.
	u, quitCmd := m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if !m.quitting || quitCmd == nil {
		t.Fatal("expected quit after NewTab submit")
	}
	if len(wb.createdTabs) != 1 {
		t.Fatalf("expected 1 CreateTab call, got %d", len(wb.createdTabs))
	}
	got := wb.createdTabs[0]
	if got.TabName != "" || !got.UseWorktree || !got.CreateNew || got.Session != "router" || got.BaseBranch != "main" {
		t.Fatalf("unexpected CreateTab req: %+v", got)
	}

	// 6. Selecting a preexisting worktree in Existing mode (0=name, 1=folder, 2=mode, 3=existing list).
	m2 := New(wb, Options{InitialMode: ModeTabs, OpenNew: true, HomeDir: "/home/u"})
	m2.width, m2.height = 120, 32
	m2 = update(m2, spacesMsg{spaces: spaces})
	m2 = update(m2, wtInspectMsg{dir: "/home/u/dev/router", ctx: wb.inspectCtx})
	// Tab to folder (1), mode (2), existing list (3), Down to second worktree (feat-auth).
	m2 = update(m2, keyType(tea.KeyTab))
	m2 = update(m2, keyType(tea.KeyTab))
	m2 = update(m2, keyType(tea.KeyTab))
	m2 = update(m2, keyType(tea.KeyDown))
	u, _ = m2.Update(keyType(tea.KeyEnter))
	m2 = u.(Model)
	if len(wb.createdTabs) != 2 {
		t.Fatalf("expected 2nd CreateTab call, got %d", len(wb.createdTabs))
	}
	got2 := wb.createdTabs[1]
	if got2.CreateNew || got2.ExistingPath != "/home/u/.local/share/tmux-spaces/worktrees/router/feat-auth" {
		t.Fatalf("unexpected existing worktree CreateTab req: %+v", got2)
	}

	// 7. Esc from `tabs --new` returns cleanly to ModeTabs instead of quitting.
	m3 := New(wb, Options{InitialMode: ModeTabs, OpenNew: true, HomeDir: "/home/u"})
	m3.width, m3.height = 120, 32
	m3 = update(m3, spacesMsg{spaces: spaces})
	m3 = update(m3, wtInspectMsg{dir: "/home/u/dev/router", ctx: wb.inspectCtx})
	m3 = update(m3, keyType(tea.KeyEsc))
	if m3.mode != ModeTabs || m3.quitting || m3.newIsTab {
		t.Fatalf("Esc in tabs --new: mode=%v quitting=%v newIsTab=%v, want ModeTabs & !quitting", m3.mode, m3.quitting, m3.newIsTab)
	}
}

func TestNewSpaceDialogWorktreeCreationAndOptionalName(t *testing.T) {
	spaces := testSpaces()
	wb := &fakeWorktreeBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		inspectCtx: model.WorktreeContext{
			InRepo:       true,
			RepoName:     "dots",
			MainRepoRoot: "/home/u/dots",
			WorktreeRoot: "/home/u/.local/share/tmux-spaces/worktrees",
			DefaultBase:  "main",
			HasCommits:   true,
			Worktrees: []model.WorktreeEntry{
				{Path: "/home/u/dots", Name: "dots", Branch: "main", IsMain: true, IsCurrent: true, RepoName: "dots"},
			},
			AutoBranch:  "swift-cedar",
			AutoDirName: "swift-cedar",
			AutoSource:  "auto",
		},
	}

	m := New(wb, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 100, 30
	_ = m.openNew("")
	m = update(m, wtInspectMsg{dir: "/home/u/dots", ctx: wb.inspectCtx})
	// Switch to New worktree with Ctrl-t; since space name was not manually edited, it tracks auto name
	// derived from the current space's active harness title ("edit"), and Ctrl-g regenerates to a fallback.
	m = update(m, keyType(tea.KeyCtrlT))
	if !m.wtUseNew || m.newName != "edit" {
		t.Fatalf("expected New worktree + harness-derived space name edit, got useNew=%v name=%q", m.wtUseNew, m.newName)
	}
	m = update(m, keyType(tea.KeyCtrlG))
	if m.newName != "amber-falcon-1" {
		t.Fatalf("expected Ctrl-g to regenerate auto space name to amber-falcon-1, got %q", m.newName)
	}
	// Clear name completely -> optional space name still submits cleanly!
	m = update(m, keyType(tea.KeyCtrlU))
	if m.newName != "" {
		t.Fatalf("expected empty newName, got %q", m.newName)
	}
	u, _ := m.Update(keyType(tea.KeyEnter))
	m = u.(Model)
	if !m.quitting || len(wb.createdSpaces) != 1 {
		t.Fatalf("expected CreateSpaceWithWorktree submit, err=%q calls=%d", m.newError, len(wb.createdSpaces))
	}
	if !wb.createdSpaces[0].CreateNew || wb.createdSpaces[0].SpaceName != "" {
		t.Fatalf("unexpected CreateSpaceWithWorktree req: %+v", wb.createdSpaces[0])
	}
}

func TestWorktreeDisplayAcrossSpacesAndInboxPopups(t *testing.T) {
	mainGit := &model.GitInfo{
		RepoKey:      "/home/u/router/.git",
		RepoRoot:     "/home/u/router",
		MainRepoRoot: "/home/u/router",
		RepoName:     "router",
		IsWorktree:   false,
		Branch:       "main",
	}
	wtGit := &model.GitInfo{
		RepoKey:          "/home/u/router/.git",
		RepoRoot:         "/home/u/.local/share/tmux-spaces/worktrees/router/feat-wt",
		MainRepoRoot:     "/home/u/router",
		RepoName:         "router",
		IsWorktree:       true,
		WorktreeName:     "feat-wt",
		Managed:          true,
		Branch:           "feat/wt",
		BaseBranch:       "main",
		IsDirty:          true,
		UncommittedAdded: 12,
	}
	spaces := []model.Space{
		{
			Name:      "router",
			IsCurrent: true,
			CWD:       "/home/u/router",
			Git:       mainGit,
			Status:    model.StatusWorking,
			Counts:    model.Counts{Working: 1},
			Panes: []model.Pane{
				{
					ID:                "%1",
					NumericID:         1,
					WindowID:          "@1",
					WindowName:        "claude",
					App:               "claude",
					Command:           "claude",
					ConversationTitle: "Build worktrees",
					Status:            model.StatusWorking,
					Text:              "running go test",
					CWD:               wtGit.RepoRoot,
					Git:               wtGit,
				},
			},
		},
	}

	// Spaces popup: shows ⎇ 1 wt on space header, ⎇ feat/wt on child pane, and wt feat-wt (router) in detail footer.
	ms := New(&fakeBackend{spaces: spaces}, Options{HomeDir: "/home/u"})
	ms.spaces = spaces
	ms.width, ms.height = 140, 26
	ms.selected = 1 // select pane row
	plainSpaces := ansi.Strip(ms.View())
	for _, want := range []string{"⎇ 1 wt", "⎇ feat/wt", "wt feat-wt (router)", "base main"} {
		if !strings.Contains(plainSpaces, want) {
			t.Errorf("Spaces popup missing worktree detail %q:\n%s", want, plainSpaces)
		}
	}

	// Spaces search matches "worktree" and worktree name.
	ms.query = "feat-wt"
	if len(ms.visibleRows()) == 0 {
		t.Fatal("expected Spaces search to match worktree name feat-wt")
	}

	// Inbox popup: shows ⎇ feat/wt and wt detail for both live pane and recent closed outcome.
	mi := New(&fakeBackend{spaces: spaces}, Options{InitialMode: ModeInbox, HomeDir: "/home/u"})
	mi.spaces = spaces
	mi.outcomes = []model.OutcomeEntry{
		{
			Session: "router",
			Agent:   "codex",
			App:     "codex",
			Outcome: "done",
			Title:   "Shipped PR",
			CWD:     wtGit.RepoRoot,
			TsMs:    1700000000000,
		},
	}
	mi.gitInfo = map[string]*model.GitInfo{wtGit.RepoRoot: wtGit}
	mi.width, mi.height = 140, 26
	plainInbox := ansi.Strip(mi.View())
	for _, want := range []string{"⎇ feat/wt", "wt feat-wt (router)", "base main"} {
		if !strings.Contains(plainInbox, want) {
			t.Errorf("Inbox popup missing worktree detail %q:\n%s", want, plainInbox)
		}
	}

	// Inbox search matches "worktree".
	mi.query = "worktree"
	if len(mi.inboxItems()) != 2 {
		t.Fatalf("expected Inbox search 'worktree' to match live pane + outcome, got %d", len(mi.inboxItems()))
	}

	// Tabs popup: shows ⎇ 1 wt in header, ⎇ feat/wt on tab card, and Worktree/Main repo rows in inspector.
	mt := New(&fakeBackend{spaces: spaces}, Options{InitialMode: ModeTabs, HomeDir: "/home/u"})
	mt.spaces = spaces
	mt.width, mt.height = 140, 36
	plainTabs := ansi.Strip(mt.View())
	for _, want := range []string{
		"Tabs · router",
		"⎇ 1 wt",
		"⎇ feat/wt",
		"⎇ feat-wt",
		"GIT · worktree · router",
		"Worktree",
		"from router · managed",
		"Main repo",
		"~/router",
		"feat/wt → main",
	} {
		if !strings.Contains(plainTabs, want) {
			t.Errorf("Tabs popup missing worktree detail %q:\n%s", want, plainTabs)
		}
	}

	// Tabs search matches "worktree", "wt", and worktree name.
	for _, q := range []string{"worktree", "wt", "feat-wt"} {
		mt.query = q
		if len(mt.tabsItems()) != 1 {
			t.Fatalf("expected Tabs search %q to match worktree tab, got %d", q, len(mt.tabsItems()))
		}
	}
}

func TestWorktreesManagerViewAndSafeCleanup(t *testing.T) {
	spaces := []model.Space{
		{
			Name:      "dev",
			IsCurrent: true,
			CWD:       "/repo",
			Panes:     []model.Pane{{ID: "%1", WindowID: "@1", CWD: "/repo"}},
		},
		{
			Name:            "feature",
			IsCurrent:       false,
			CWD:             "/wt/feat-a",
			WorktreePath:    "/wt/feat-a",
			WorktreeName:    "feat-a",
			WorktreeManaged: true,
			Panes:           []model.Pane{{ID: "%2", WindowID: "@2", CWD: "/wt/feat-a"}},
		},
	}
	wb := &fakeWorktreeBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		allWorktrees: []model.WorktreeEntry{
			{Path: "/repo", Name: "repo", Branch: "main", RepoKey: "/repo/.git", MainRepoRoot: "/repo", RepoName: "repo", IsMain: true, IsCurrent: true, OpenWindowCount: 1, OpenSpaces: []string{"dev"}},
			{Path: "/wt/feat-a", Name: "feat-a", Branch: "feat/a", BaseBranch: "main", RepoKey: "/repo/.git", MainRepoRoot: "/repo", RepoName: "repo", Managed: true, IsDirty: true, OpenWindowCount: 0},
		},
		safety: WorktreeRemoveSafety{
			IsDirty:    true,
			Branch:     "feat/a",
			BaseBranch: "main",
			Warnings:   []string{"worktree has uncommitted changes"},
		},
	}

	// 1. Open Worktrees view via W in Spaces popup.
	m := New(wb, Options{HomeDir: "/home/u"})
	m.spaces = spaces
	m.width, m.height = 120, 26
	m = update(m, keyRunes("W"))
	if m.mode != ModeWorktrees {
		t.Fatalf("expected ModeWorktrees after W, got %v", m.mode)
	}
	m = update(m, wtListMsg{entries: wb.allWorktrees})
	view := ansi.Strip(m.View())
	for _, want := range []string{"Worktrees", "feat/a", "detached", "managed"} {
		if !strings.Contains(view, want) {
			t.Errorf("Worktrees view missing %q:\n%s", want, view)
		}
	}

	// 2. Try removing dirty worktree -> refuses without force, succeeds after pressing f.
	m = update(m, keyType(tea.KeyDown)) // select feat-a
	m = update(m, keyRunes("x"))
	if m.mode != ModeConfirmRemoveWorktree {
		t.Fatalf("expected ModeConfirmRemoveWorktree, got %v", m.mode)
	}
	m = update(m, wtSafetyMsg{path: "/wt/feat-a", safety: wb.safety})
	m = update(m, keyType(tea.KeyEnter)) // dirty without force -> stays in confirm with toast
	if len(wb.removedPaths) != 0 {
		t.Fatal("should not remove dirty worktree without force toggle")
	}
	m = update(m, keyRunes("f")) // toggle force
	m = update(m, keyRunes("b")) // toggle delete branch
	m = update(m, keyType(tea.KeyEnter))
	if len(wb.removedPaths) != 1 || !wb.removedForce || !wb.removedBranch {
		t.Fatalf("expected forced remove with branch delete, got paths=%v force=%v branch=%v", wb.removedPaths, wb.removedForce, wb.removedBranch)
	}

	// 3. Prune via p.
	m = update(m, keyRunes("p"))
	if wb.prunedCalls != 1 {
		t.Fatalf("expected 1 prune call, got %d", wb.prunedCalls)
	}

	// 4. Closing a worktree-backed space offers w toggle to also remove its worktree.
	m2 := New(wb, Options{HomeDir: "/home/u"})
	m2.spaces = spaces
	m2.width, m2.height = 120, 26
	// Select "feature" space row.
	for i, r := range m2.visibleRows() {
		if r.Kind == RowSpace && m2.spaces[r.SpaceIdx].Name == "feature" {
			m2.selected = i
		}
	}
	m2 = update(m2, keyRunes("x"))
	if m2.mode != ModeConfirmKill || m2.confirmWorktreePath != "/wt/feat-a" {
		t.Fatalf("expected confirm kill with worktree path, got mode=%v wt=%q", m2.mode, m2.confirmWorktreePath)
	}
	m2 = update(m2, keyRunes("w"))
	if !m2.confirmRemoveWorktree {
		t.Fatal("expected w to toggle confirmRemoveWorktree")
	}
	m2 = update(m2, keyType(tea.KeyEnter))
	if len(wb.killed) != 1 || wb.killed[0] != "feature" {
		t.Fatalf("expected KillSpace feature, got %v", wb.killed)
	}
	if len(wb.removedPaths) != 2 || wb.removedPaths[1] != "/wt/feat-a" {
		t.Fatalf("expected worktree removal after closing space, got %v", wb.removedPaths)
	}
}

func TestTabsNewTabAndWorktreeCloseFlow(t *testing.T) {
	wtGit := &model.GitInfo{
		RepoKey:      "/home/u/router/.git",
		RepoRoot:     "/wt/feat-a",
		MainRepoRoot: "/home/u/router",
		RepoName:     "router",
		IsWorktree:   true,
		WorktreeName: "feat-a",
		Managed:      true,
		Branch:       "feat/a",
		BaseBranch:   "main",
	}
	spaces := []model.Space{
		{
			Name:      "router",
			IsCurrent: true,
			CWD:       "/home/u/router",
			Windows: []model.Window{
				{ID: "@1", Index: 0, Name: "main", Active: false},
				{ID: "@2", Index: 1, Name: "claude", Active: true, WorktreePath: "/wt/feat-a", WorktreeName: "feat-a", WorktreeBase: "main", WorktreeManaged: true},
			},
			Panes: []model.Pane{
				{ID: "%1", WindowID: "@1", WindowIndex: 0, WindowName: "main", CWD: "/home/u/router"},
				{ID: "%2", WindowID: "@2", WindowIndex: 1, WindowName: "claude", CWD: "/wt/feat-a", Git: wtGit, IsActive: true, WindowActive: true},
			},
		},
	}
	wb := &fakeWorktreeBackend{
		fakeBackend: fakeBackend{spaces: spaces},
		inspectCtx: model.WorktreeContext{
			InRepo:       true,
			RepoName:     "router",
			MainRepoRoot: "/home/u/router",
			DefaultBase:  "main",
			HasCommits:   true,
			Worktrees: []model.WorktreeEntry{
				{Path: "/home/u/router", Name: "router", Branch: "main", IsMain: true, RepoName: "router"},
				{Path: "/wt/feat-a", Name: "feat-a", Branch: "feat/a", IsCurrent: true, RepoName: "router", Managed: true},
			},
			AutoBranch: "swift-cedar",
			AutoSource: "auto",
		},
		safety: WorktreeRemoveSafety{
			CanRemoveSafely: true,
			Branch:          "feat/a",
			BaseBranch:      "main",
		},
	}

	// 1. Opening Tabs popup selects active worktree tab (@2), pressing W opens Worktrees manager and Esc returns to ModeTabs.
	m := New(wb, Options{InitialMode: ModeTabs, HomeDir: "/home/u"})
	m.width, m.height = 140, 32
	m = update(m, spacesMsg{spaces: spaces})
	m = update(m, keyRunes("W"))
	if m.mode != ModeWorktrees {
		t.Fatalf("expected W in ModeTabs to open ModeWorktrees, got %v", m.mode)
	}
	m = update(m, keyType(tea.KeyEsc))
	if m.mode != ModeTabs {
		t.Fatalf("expected Esc from ModeWorktrees to return to ModeTabs, got %v", m.mode)
	}

	// 2. Pressing x on worktree-backed tab @2 offers w toggle to remove its unshared worktree.
	m = update(m, keyRunes("x"))
	if m.mode != ModeConfirmKill || !m.confirmWindow || m.confirmWorktreePath != "/wt/feat-a" {
		t.Fatalf("expected tab confirm kill with worktree path, got mode=%v win=%v wt=%q", m.mode, m.confirmWindow, m.confirmWorktreePath)
	}
	confirmView := ansi.Strip(m.View())
	if !strings.Contains(confirmView, "Close tab?") || !strings.Contains(confirmView, "Also remove git worktree feat-a") {
		t.Errorf("tab confirm dialog missing worktree option:\n%s", confirmView)
	}
	m = update(m, keyRunes("w"))
	m = update(m, keyRunes("y"))
	if m.mode != ModeTabs {
		t.Fatalf("expected return to ModeTabs after closing tab, got %v", m.mode)
	}
	if len(wb.killedWindows) != 1 || wb.killedWindows[0] != "@2" {
		t.Fatalf("expected KillWindow @2, got %v", wb.killedWindows)
	}
	if len(wb.removedPaths) != 1 || wb.removedPaths[0] != "/wt/feat-a" {
		t.Fatalf("expected RemoveWorktree /wt/feat-a after closing tab, got %v", wb.removedPaths)
	}

	// 3. New Space (`prefix + N` or `n` inside Spaces manager) opens as centered modal and Esc returns to Spaces navigator.
	ms := New(wb, Options{OpenNew: true, HomeDir: "/home/u"})
	ms.spaces = testSpaces()
	ms.prefillNewSpace("")
	ms.wtCtx = wb.inspectCtx
	ms.width, ms.height = 120, 30
	plainSpace := ansi.Strip(ms.View())
	if !strings.Contains(plainSpace, "╭") {
		t.Errorf("New Space inside Spaces manager should render centered modal border:\n%s", plainSpace)
	}
	ms = update(ms, keyType(tea.KeyEsc))
	if ms.mode != ModeNavigate || ms.quitting {
		t.Errorf("Esc in New Space should return to Spaces manager, got mode=%v quitting=%v", ms.mode, ms.quitting)
	}
}

func TestWorktreesPopupIndentationAndMultiRepoNavigation(t *testing.T) {
	// Deliberately provide repos in non-alphabetical order (zebra before alpha)
	// and a linked worktree before main within alpha to verify canonical visual
	// order matches navigation order 1-to-1.
	entries := []model.WorktreeEntry{
		{Path: "/repos/zebra", Name: "zebra", Branch: "main", RepoKey: "/repos/zebra/.git", MainRepoRoot: "/repos/zebra", RepoName: "zebra", IsMain: true},
		{Path: "/wt/zebra/feat-z", Name: "feat-z", Branch: "feat/z", BaseBranch: "main", RepoKey: "/repos/zebra/.git", MainRepoRoot: "/repos/zebra", RepoName: "zebra", Managed: true},
		{Path: "/wt/alpha/feat-a", Name: "feat-a", Branch: "feat/a", BaseBranch: "main", RepoKey: "/repos/alpha/.git", MainRepoRoot: "/repos/alpha", RepoName: "alpha", Managed: true},
		{Path: "/repos/alpha", Name: "alpha", Branch: "main", RepoKey: "/repos/alpha/.git", MainRepoRoot: "/repos/alpha", RepoName: "alpha", IsMain: true},
	}
	wb := &fakeWorktreeBackend{
		allWorktrees: entries,
	}

	m := New(wb, Options{InitialMode: ModeWorktrees, HomeDir: "/home/u"})
	m.width, m.height = 120, 26
	m = update(m, wtListMsg{entries: entries})

	// 1. Visual order must be alpha (main, feat/a) then zebra (main, feat/z),
	// and linked worktree names must be right-offset relative to the base main row.
	plain := ansi.Strip(m.View())
	lines := strings.Split(plain, "\n")
	var alphaMainLine, alphaWTLine, zebraMainLine, zebraWTLine string
	var alphaMainIdx, alphaWTIdx, zebraMainIdx, zebraWTIdx int
	for i, line := range lines {
		switch {
		case strings.Contains(line, "● main") && alphaMainLine == "":
			alphaMainLine = line
			alphaMainIdx = i
		case strings.Contains(line, "⎇ feat/a"):
			alphaWTLine = line
			alphaWTIdx = i
		case strings.Contains(line, "● main") && alphaMainLine != "":
			zebraMainLine = line
			zebraMainIdx = i
		case strings.Contains(line, "⎇ feat/z"):
			zebraWTLine = line
			zebraWTIdx = i
		}
	}
	if alphaMainLine == "" || alphaWTLine == "" || zebraMainLine == "" || zebraWTLine == "" {
		t.Fatalf("missing expected rows in worktrees view:\n%s", plain)
	}
	if !(alphaMainIdx < alphaWTIdx && alphaWTIdx < zebraMainIdx && zebraMainIdx < zebraWTIdx) {
		t.Fatalf("expected display order alpha/main < alpha/feat-a < zebra/main < zebra/feat-z, got line indices %d, %d, %d, %d:\n%s",
			alphaMainIdx, alphaWTIdx, zebraMainIdx, zebraWTIdx, plain)
	}
	mainCol := ansi.StringWidth(alphaMainLine[:strings.Index(alphaMainLine, "main")])
	wtCol := ansi.StringWidth(alphaWTLine[:strings.Index(alphaWTLine, "feat/a")])
	if wtCol <= mainCol {
		t.Fatalf("expected linked worktree name to be right-offset from base main name (mainCol=%d, wtCol=%d):\nmain: %q\nwt:   %q",
			mainCol, wtCol, alphaMainLine, alphaWTLine)
	}
	mainIconCol := ansi.StringWidth(alphaMainLine[:strings.Index(alphaMainLine, "●")])
	wtIconCol := ansi.StringWidth(alphaWTLine[:strings.Index(alphaWTLine, "⎇")])
	if wtIconCol <= mainIconCol {
		t.Fatalf("expected linked worktree icon to be right-offset from base main icon (mainIconCol=%d, wtIconCol=%d):\nmain: %q\nwt:   %q",
			mainIconCol, wtIconCol, alphaMainLine, alphaWTLine)
	}

	// 2. Stepping Down walks strictly top-to-bottom: alpha/main -> alpha/feat-a -> zebra/main -> zebra/feat-z.
	wantOrder := []string{"/repos/alpha", "/wt/alpha/feat-a", "/repos/zebra", "/wt/zebra/feat-z"}
	for i, wantPath := range wantOrder {
		got := m.selectedWorktree()
		if got == nil || got.Path != wantPath {
			t.Fatalf("step %d: selectedWorktree = %+v, want %s", i, got, wantPath)
		}
		if i < len(wantOrder)-1 {
			m = update(m, keyType(tea.KeyDown))
		}
	}
	// Stepping Up walks strictly bottom-to-top.
	for i := len(wantOrder) - 2; i >= 0; i-- {
		m = update(m, keyType(tea.KeyUp))
		got := m.selectedWorktree()
		if got == nil || got.Path != wantOrder[i] {
			t.Fatalf("up step %d: selectedWorktree = %+v, want %s", i, got, wantOrder[i])
		}
	}

	// 3. Repo jump keys (Right/l and Left/h) and g/G work cleanly.
	m = update(m, keyType(tea.KeyRight))
	if got := m.selectedWorktree(); got == nil || got.Path != "/repos/zebra" {
		t.Fatalf("Right should jump to next repo group (/repos/zebra), got %+v", got)
	}
	m = update(m, keyType(tea.KeyLeft))
	if got := m.selectedWorktree(); got == nil || got.Path != "/repos/alpha" {
		t.Fatalf("Left should jump to prev repo group (/repos/alpha), got %+v", got)
	}
	m = update(m, keyRunes("G"))
	if got := m.selectedWorktree(); got == nil || got.Path != "/wt/zebra/feat-z" {
		t.Fatalf("G should jump to last worktree (/wt/zebra/feat-z), got %+v", got)
	}
	m = update(m, keyRunes("g"))
	if got := m.selectedWorktree(); got == nil || got.Path != "/repos/alpha" {
		t.Fatalf("g should jump to first worktree (/repos/alpha), got %+v", got)
	}

	// 4. Mouse left click selects the clicked worktree row (skipping headers/spacers).
	// Layout with height=26: topY=3 (header=0, search=1, divider=2).
	// Entries: 0=alpha header(y=3), 1=alpha main(y=4), 2=alpha feat-a(y=5), 3=spacer(y=6), 4=zebra header(y=7), 5=zebra main(y=8), 6=zebra feat-z(y=9).
	m = update(m, tea.MouseMsg{X: 10, Y: 9, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := m.selectedWorktree(); got == nil || got.Path != "/wt/zebra/feat-z" {
		t.Fatalf("mouse click on y=9 should select /wt/zebra/feat-z, got %+v", got)
	}
	// Refresh preserves selection by path.
	m = update(m, wtListMsg{entries: entries})
	if got := m.selectedWorktree(); got == nil || got.Path != "/wt/zebra/feat-z" {
		t.Fatalf("refresh should preserve selection /wt/zebra/feat-z, got %+v", got)
	}

	// 5. Opening `n` from Worktrees (opened from Tabs) and pressing Esc returns to Worktrees,
	// and a subsequent Esc returns to Tabs without looping.
	mt := New(wb, Options{InitialMode: ModeTabs, HomeDir: "/home/u"})
	mt.width, mt.height = 120, 26
	mt = update(mt, keyRunes("W"))
	mt = update(mt, wtListMsg{entries: entries})
	mt = update(mt, keyRunes("n"))
	if mt.mode != ModeNewSpace {
		t.Fatalf("expected ModeNewSpace after n in Worktrees, got %v", mt.mode)
	}
	mt = update(mt, keyType(tea.KeyEsc))
	if mt.mode != ModeWorktrees {
		t.Fatalf("expected Esc from n overlay to return to ModeWorktrees, got %v", mt.mode)
	}
	mt = update(mt, keyType(tea.KeyEsc))
	if mt.mode != ModeTabs {
		t.Fatalf("expected Esc from ModeWorktrees to return to ModeTabs, got %v", mt.mode)
	}
}

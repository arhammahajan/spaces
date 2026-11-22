package resurrect

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func TestStoreSaveLoadListExitedDeleteAndThrottle(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	st := &Store{
		Dir: t.TempDir(),
		Now: func() time.Time { return now },
	}

	s1 := SpaceSnapshot{
		Name:      "alpha",
		SavedAtMs: now.Add(-2 * time.Hour).UnixMilli(),
		CWD:       "/repo/alpha",
		Windows: []WindowSnapshot{
			{Index: 1, Name: "claude", Panes: []PaneSnapshot{{Index: 1, Agent: "claude", SessionID: "s-1"}}},
		},
	}
	s2 := SpaceSnapshot{
		Name:      "beta",
		SavedAtMs: now.Add(-10 * time.Minute).UnixMilli(),
		CWD:       "/repo/beta",
		Windows: []WindowSnapshot{
			{Index: 1, Name: "codex", Panes: []PaneSnapshot{{Index: 1, Agent: "codex"}}},
		},
	}
	if err := st.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	if err := st.Save(s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	loaded, err := st.Load("alpha")
	if err != nil || loaded.Name != "alpha" || loaded.Windows[0].Panes[0].SessionID != "s-1" {
		t.Fatalf("Load alpha = %+v, err=%v", loaded, err)
	}

	// Only non-running spaces appear in ListExited, newest first.
	exited, err := st.ListExited(map[string]bool{"alpha": true})
	if err != nil {
		t.Fatalf("ListExited: %v", err)
	}
	if len(exited) != 1 || exited[0].Name != "beta" || len(exited[0].Agents) != 1 || exited[0].Agents[0] != "codex" {
		t.Fatalf("unexpected exited: %+v", exited)
	}

	if err := st.Delete("beta"); err != nil {
		t.Fatalf("Delete beta: %v", err)
	}
	if err := st.Delete("nonexistent"); err != nil {
		t.Fatalf("Delete nonexistent should be nil, got %v", err)
	}

	// Autosave throttle.
	if !st.ShouldAutoSave() {
		t.Fatal("first ShouldAutoSave should be true")
	}
	if err := st.TouchAutoSave(); err != nil {
		t.Fatalf("TouchAutoSave: %v", err)
	}
	if st.ShouldAutoSave() {
		t.Fatal("ShouldAutoSave should be false immediately after touch")
	}
	now = now.Add(AutoSaveInterval + time.Second)
	if !st.ShouldAutoSave() {
		t.Fatal("ShouldAutoSave should be true after interval")
	}
}

func TestCaptureSpacesEnrichesAgentAndSessionID(t *testing.T) {
	origProc := ProcCommandFunc
	defer func() { ProcCommandFunc = origProc }()
	ProcCommandFunc = func(pid int) []string {
		if pid == 200 {
			return []string{"nvim", "main.go"}
		}
		return nil
	}

	snap := tmux.ServerSnapshot{
		Sessions: []tmux.Session{{Name: "work"}},
		Windows: []tmux.Window{
			{SessionName: "work", ID: "@1", Index: 1, Name: "claude", Active: true, Layout: "abcd,80x24,0,0,0"},
		},
		Panes: []tmux.Pane{
			{SessionName: "work", WindowID: "@1", WindowIndex: 1, ID: "%1", NumericID: 1, PaneIndex: 1, CurrentPath: "/repo", CurrentCommand: "claude", SpacesApp: "claude-code", ResumeSession: "sess-abc", Active: true, PID: 100},
			{SessionName: "work", WindowID: "@1", WindowIndex: 1, ID: "%2", NumericID: 2, PaneIndex: 2, CurrentPath: "/repo", CurrentCommand: "zsh", PID: 200},
		},
	}

	got := CaptureSpaces(snap, nil, time.UnixMilli(1700000000000))
	if len(got) != 1 || len(got[0].Windows) != 1 || len(got[0].Windows[0].Panes) != 2 {
		t.Fatalf("unexpected capture: %+v", got)
	}
	if got[0].Windows[0].Layout != "abcd,80x24,0,0,0" {
		t.Fatalf("layout = %q", got[0].Windows[0].Layout)
	}
	p0 := got[0].Windows[0].Panes[0]
	if p0.Agent != "claude" || p0.SessionID != "sess-abc" {
		t.Fatalf("pane 0 = %+v", p0)
	}
	p1 := got[0].Windows[0].Panes[1]
	if p1.Agent != "" || len(p1.FullCommand) != 2 || p1.FullCommand[0] != "nvim" {
		t.Fatalf("pane 1 = %+v", p1)
	}
}

type fakeRestorer struct {
	ops []string
}

func (f *fakeRestorer) NewSessionDetached(_ context.Context, name, cwd, win string, cmd ...string) error {
	f.ops = append(f.ops, "new-session:"+name+":"+cwd+":"+win+":"+strings.Join(cmd, " "))
	return nil
}
func (f *fakeRestorer) NewWindow(_ context.Context, session, cwd, name string, cmd ...string) error {
	f.ops = append(f.ops, "new-window:"+session+":"+cwd+":"+name+":"+strings.Join(cmd, " "))
	return nil
}
func (f *fakeRestorer) SplitWindow(_ context.Context, target, cwd string, cmd ...string) error {
	f.ops = append(f.ops, "split:"+target+":"+cwd+":"+strings.Join(cmd, " "))
	return nil
}
func (f *fakeRestorer) SelectLayout(_ context.Context, target, layout string) error {
	f.ops = append(f.ops, "layout:"+target+":"+layout)
	return nil
}
func (f *fakeRestorer) SelectWindow(_ context.Context, target string) error {
	f.ops = append(f.ops, "select-win:"+target)
	return nil
}
func (f *fakeRestorer) SelectPane(_ context.Context, target string) error {
	f.ops = append(f.ops, "select-pane:"+target)
	return nil
}
func (f *fakeRestorer) SwitchToSpace(_ context.Context, session string) error {
	f.ops = append(f.ops, "switch:"+session)
	return nil
}

func TestBuildPaneCommandAndRestore(t *testing.T) {
	opts := RestoreOptions{
		SelfPath:     "/bin/tmux-spaces",
		Switch:       true,
		SpacesBinary: func() []string { return []string{"spaces"} },
	}

	// 1. Agent with explicit session_id uses __resume-wrap with b64 command + session_id.
	cmd := BuildPaneCommand(PaneSnapshot{Agent: "claude", SessionID: "s-42"}, opts)
	wantB64 := base64.StdEncoding.EncodeToString([]byte("claude"))
	if strings.Join(cmd, " ") != "/bin/tmux-spaces __shim claude spaces __resume-wrap "+wantB64+" s-42" {
		t.Fatalf("agent resume cmd = %v", cmd)
	}

	// 2. Agent with profile wraps __shim-profile in __resume-wrap so it also resumes.
	profCmd := BuildPaneCommand(PaneSnapshot{Agent: "codex", Profile: "codex-work"}, opts)
	wantProfB64 := base64.StdEncoding.EncodeToString([]byte("spaces __shim-profile codex/codex-work"))
	if strings.Join(profCmd, " ") != "/bin/tmux-spaces __shim codex spaces __resume-wrap "+wantProfB64 {
		t.Fatalf("profile resume cmd = %v", profCmd)
	}

	// 2b. Harness profile launcher (e.g. `harness agy profile saucerf166`) is preserved
	// even when @spaces_profile is also captured on the pane.
	agyCmd := BuildPaneCommand(PaneSnapshot{
		Agent:       "antigravity",
		Profile:     "saucerf166",
		FullCommand: []string{"harness", "agy", "profile", "saucerf166"},
	}, opts)
	wantAgyB64 := base64.StdEncoding.EncodeToString([]byte("harness agy profile saucerf166"))
	if strings.Join(agyCmd, " ") != "/bin/tmux-spaces __shim antigravity spaces __resume-wrap "+wantAgyB64 {
		t.Fatalf("harness antigravity cmd = %v", agyCmd)
	}

	// 3. Safe TUI restarts; unsafe command does NOT restart.
	if got := BuildPaneCommand(PaneSnapshot{FullCommand: []string{"nvim", "a.go"}}, opts); len(got) != 2 || got[0] != "nvim" {
		t.Fatalf("safe TUI cmd = %v", got)
	}
	if got := BuildPaneCommand(PaneSnapshot{FullCommand: []string{"rm", "-rf", "/tmp/x"}}, opts); got != nil {
		t.Fatalf("unsafe command must not auto-restart, got %v", got)
	}

	// 4. Full Restore sequence.
	fr := &fakeRestorer{}
	snap := SpaceSnapshot{
		Name:              "work",
		CWD:               "/repo",
		ActiveWindowIndex: 2,
		Windows: []WindowSnapshot{
			{
				Index:  1,
				Name:   "agent",
				Layout: "lay1",
				Panes: []PaneSnapshot{
					{Index: 1, CWD: "/repo", Agent: "claude", SessionID: "s-42", Active: true},
					{Index: 2, CWD: "/repo/sub", FullCommand: []string{"nvim", "x.go"}},
				},
			},
			{
				Index:  2,
				Name:   "shell",
				Active: true,
				Panes:  []PaneSnapshot{{Index: 1, CWD: "/repo"}},
			},
		},
	}
	if err := Restore(context.Background(), fr, snap, opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	joined := strings.Join(fr.ops, "\n")
	for _, want := range []string{
		"new-session:work:/repo:agent:/bin/tmux-spaces __shim claude spaces __resume-wrap " + wantB64 + " s-42",
		"split:work:1:/repo/sub:nvim x.go",
		"layout:work:1:lay1",
		"select-pane:work:1.1",
		"new-window:work:/repo:shell:",
		"select-win:work:2",
		"switch:work",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in restore ops:\n%s", want, joined)
		}
	}
}

func TestCaptureAntigravitySessionFromCommandAndProc(t *testing.T) {
	origCmd := ProcCommandFunc
	origAgy := ProcAntigravitySessionFunc
	defer func() {
		ProcCommandFunc = origCmd
		ProcAntigravitySessionFunc = origAgy
	}()

	ProcCommandFunc = func(pid int) []string {
		switch pid {
		case 101:
			return []string{"harness", "agy", "profile", "saucerf166", "--conversation", "conv-cmd"}
		case 102:
			return []string{"harness", "agy", "profile", "saucerf166"}
		default:
			return nil
		}
	}
	ProcAntigravitySessionFunc = func(pid int) string {
		if pid == 102 {
			return "conv-fd"
		}
		return ""
	}

	snap := tmux.ServerSnapshot{
		Sessions: []tmux.Session{{Name: "work"}},
		Windows:  []tmux.Window{{SessionName: "work", ID: "@1", Index: 1, Name: "agy", Active: true}},
		Panes: []tmux.Pane{
			{SessionName: "work", WindowID: "@1", WindowIndex: 1, ID: "%1", NumericID: 1, PaneIndex: 1, CurrentPath: "/repo", CurrentCommand: "tmux-spaces", SpacesApp: "antigravity", SpacesTitle: "First chat", PID: 101},
			{SessionName: "work", WindowID: "@1", WindowIndex: 1, ID: "%2", NumericID: 2, PaneIndex: 2, CurrentPath: "/repo", CurrentCommand: "tmux-spaces", SpacesApp: "antigravity", SpacesTitle: "Second chat", PID: 102},
		},
	}

	got := CaptureSpaces(snap, nil, time.UnixMilli(1700000000000))
	panes := got[0].Windows[0].Panes
	if panes[0].SessionID != "conv-cmd" || panes[0].Title != "First chat" {
		t.Fatalf("pane 0 = %+v", panes[0])
	}
	if panes[1].SessionID != "conv-fd" || panes[1].Title != "Second chat" {
		t.Fatalf("pane 1 = %+v", panes[1])
	}
}

func TestRestoreMultipleAntigravityPanesDistinctConversations(t *testing.T) {
	convs := []struct {
		id    string
		title string
	}{
		{"conv-newest", "Newest chat"},
		{"conv-older", "Older chat"},
	}

	var calls []string
	opts := RestoreOptions{
		SelfPath:     "/bin/tmux-spaces",
		SpacesBinary: func() []string { return []string{"spaces"} },
		ResolveAntigravity: func(_ context.Context, cwd, profile, title string, exclude map[string]bool) (string, bool) {
			calls = append(calls, cwd+"|"+profile+"|"+title)
			if title != "" {
				for _, c := range convs {
					if !exclude[c.id] && c.title == title {
						return c.id, true
					}
				}
			}
			for _, c := range convs {
				if !exclude[c.id] {
					return c.id, true
				}
			}
			return "", true
		},
	}

	fr := &fakeRestorer{}
	snap := SpaceSnapshot{
		Name: "work",
		CWD:  "/repo",
		Windows: []WindowSnapshot{
			{
				Index: 1,
				Name:  "agy",
				Panes: []PaneSnapshot{
					// Pane 1 has explicit --conversation in FullCommand -> claims conv-older.
					{Index: 1, CWD: "/repo", Agent: "antigravity", FullCommand: []string{"harness", "agy", "profile", "saucerf166", "--conversation", "conv-older"}},
					// Pane 2 has no SessionID -> must resolve conv-newest (excluding conv-older).
					{Index: 2, CWD: "/repo", Agent: "antigravity", FullCommand: []string{"harness", "agy", "profile", "saucerf166"}},
					// Pane 3 has no SessionID and all convs are claimed -> must NOT __resume-wrap.
					{Index: 3, CWD: "/repo", Agent: "antigravity", FullCommand: []string{"harness", "agy", "profile", "saucerf166"}},
				},
			},
		},
	}

	if err := Restore(context.Background(), fr, snap, opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("resolver calls = %v, want 2", calls)
	}
	wantAgyB64 := base64.StdEncoding.EncodeToString([]byte("harness agy profile saucerf166"))
	joined := strings.Join(fr.ops, "\n")
	if !strings.Contains(joined, "split:work:1:/repo:/bin/tmux-spaces __shim antigravity spaces __resume-wrap "+wantAgyB64+" conv-newest") {
		t.Fatalf("pane 2 did not get distinct conv-newest:\n%s", joined)
	}
	if !strings.Contains(joined, "split:work:1:/repo:/bin/tmux-spaces __shim antigravity harness agy profile saucerf166") {
		t.Fatalf("pane 3 did not skip resume when all conversations claimed:\n%s", joined)
	}
	if strings.Count(joined, "__resume-wrap "+wantAgyB64+" conv-newest") != 1 {
		t.Fatalf("conv-newest resumed more than once:\n%s", joined)
	}
}

func TestRestoreWorktreeFallbackWhenRemoved(t *testing.T) {
	mainRoot := t.TempDir()
	deletedWT := filepath.Join(t.TempDir(), "removed-worktree")
	deletedSub := filepath.Join(deletedWT, "pkg", "gone")
	fr := &fakeRestorer{}
	snap := SpaceSnapshot{
		Name:         "wt-space",
		CWD:          deletedWT,
		WorktreePath: deletedWT,
		WorktreeName: "removed-worktree",
		MainRepoRoot: mainRoot,
		Windows: []WindowSnapshot{
			{
				Index:        1,
				Name:         "zsh",
				WorktreePath: deletedWT,
				Panes: []PaneSnapshot{
					{Index: 1, CWD: deletedWT},
					{Index: 2, CWD: deletedSub},
				},
			},
		},
	}
	if err := Restore(context.Background(), fr, snap, RestoreOptions{SelfPath: "/bin/tmux-spaces"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	joined := strings.Join(fr.ops, "\n")
	for _, want := range []string{
		"new-session:wt-space:" + mainRoot + ":zsh:",
		"split:wt-space:1:" + mainRoot + ":",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q when worktree was deleted, got:\n%s", want, joined)
		}
	}

	// Session-level restore (no windows) also falls back to MainRepoRoot when worktree is gone.
	frEmpty := &fakeRestorer{}
	snapEmpty := SpaceSnapshot{
		Name:         "wt-empty",
		CWD:          deletedSub,
		WorktreePath: deletedWT,
		MainRepoRoot: mainRoot,
	}
	if err := Restore(context.Background(), frEmpty, snapEmpty, RestoreOptions{SelfPath: "/bin/tmux-spaces"}); err != nil {
		t.Fatalf("Restore empty windows: %v", err)
	}
	if got := strings.Join(frEmpty.ops, "\n"); !strings.Contains(got, "new-session:wt-empty:"+mainRoot+"::") {
		t.Fatalf("expected session-level fallback to MainRepoRoot %q, got:\n%s", mainRoot, got)
	}
}

func TestRestoreWorktreeFallbackPreservesExistingWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	wtRoot := t.TempDir()
	liveSub := filepath.Join(wtRoot, "existing-sub")
	if err := os.MkdirAll(liveSub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	deletedSub1 := filepath.Join(wtRoot, "deleted-sub-1")
	deletedSub2 := filepath.Join(wtRoot, "deleted-sub-2")

	// Pane CWD is a deleted subdirectory inside an existing linked worktree:
	// must fall back to wtRoot (preserving worktree context), NOT mainRoot.
	fr := &fakeRestorer{}
	snap := SpaceSnapshot{
		Name:         "wt-space",
		CWD:          deletedSub1,
		WorktreePath: wtRoot,
		WorktreeName: "feat-worktree",
		MainRepoRoot: mainRoot,
		Windows: []WindowSnapshot{
			{
				Index:        1,
				Name:         "dev",
				WorktreePath: wtRoot,
				Panes: []PaneSnapshot{
					{Index: 1, CWD: deletedSub1},
					{Index: 2, CWD: liveSub},
					{Index: 3, CWD: deletedSub2},
				},
			},
		},
	}
	if err := Restore(context.Background(), fr, snap, RestoreOptions{SelfPath: "/bin/tmux-spaces"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	joined := strings.Join(fr.ops, "\n")
	for _, want := range []string{
		"new-session:wt-space:" + wtRoot + ":dev:",
		"split:wt-space:1:" + liveSub + ":",
		"split:wt-space:1:" + wtRoot + ":",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in restore ops, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, mainRoot) {
		t.Fatalf("must not fall back to MainRepoRoot %q when worktree %q still exists:\n%s", mainRoot, wtRoot, joined)
	}

	// Session-level restore (no windows) with deleted subdir inside existing worktree
	// also falls back to wtRoot rather than mainRoot.
	frEmpty := &fakeRestorer{}
	snapEmpty := SpaceSnapshot{
		Name:         "wt-empty",
		CWD:          deletedSub1,
		WorktreePath: wtRoot,
		MainRepoRoot: mainRoot,
	}
	if err := Restore(context.Background(), frEmpty, snapEmpty, RestoreOptions{SelfPath: "/bin/tmux-spaces"}); err != nil {
		t.Fatalf("Restore empty windows: %v", err)
	}
	if got := strings.Join(frEmpty.ops, "\n"); !strings.Contains(got, "new-session:wt-empty:"+wtRoot+"::") {
		t.Fatalf("expected session-level fallback to wtRoot %q, got:\n%s", wtRoot, got)
	}

	// Window-level WorktreePath takes precedence over deleted session WorktreePath and MainRepoRoot.
	winWT := t.TempDir()
	deletedWinSub := filepath.Join(winWT, "gone")
	deletedSessWT := filepath.Join(t.TempDir(), "gone-sess-wt")
	frWin := &fakeRestorer{}
	snapWin := SpaceSnapshot{
		Name:         "wt-mixed",
		CWD:          deletedSessWT,
		WorktreePath: deletedSessWT,
		MainRepoRoot: mainRoot,
		Windows: []WindowSnapshot{
			{
				Index:        1,
				Name:         "w1",
				WorktreePath: winWT,
				Panes:        []PaneSnapshot{{Index: 1, CWD: deletedWinSub}},
			},
			{
				Index: 2,
				Name:  "w2",
				Panes: []PaneSnapshot{{Index: 1, CWD: deletedSessWT}},
			},
		},
	}
	if err := Restore(context.Background(), frWin, snapWin, RestoreOptions{SelfPath: "/bin/tmux-spaces"}); err != nil {
		t.Fatalf("Restore mixed: %v", err)
	}
	mixedOps := strings.Join(frWin.ops, "\n")
	if !strings.Contains(mixedOps, "new-session:wt-mixed:"+winWT+":w1:") {
		t.Fatalf("expected window 1 to fall back to existing win.WorktreePath %q, got:\n%s", winWT, mixedOps)
	}
	if !strings.Contains(mixedOps, "new-window:wt-mixed:"+mainRoot+":w2:") {
		t.Fatalf("expected window 2 (deleted session worktree) to fall back to MainRepoRoot %q, got:\n%s", mainRoot, mixedOps)
	}
}

func TestResolveRestoreCWDPrecedence(t *testing.T) {
	mainRoot := t.TempDir()
	wtRoot := t.TempDir()
	liveSub := filepath.Join(wtRoot, "sub")
	if err := os.MkdirAll(liveSub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	missingDir := filepath.Join(t.TempDir(), "missing")
	missingSub := filepath.Join(wtRoot, "missing-sub")
	missingMain := filepath.Join(t.TempDir(), "missing-main")

	// Regular file (not a directory) should not be treated as a valid fallback directory.
	fileNotDir := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(fileNotDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cases := []struct {
		name     string
		cwd      string
		wtPath   string
		mainRoot string
		want     string
	}{
		{"empty cwd", "", wtRoot, mainRoot, ""},
		{"existing cwd preserved", liveSub, wtRoot, mainRoot, liveSub},
		{"missing subdir prefers existing wtPath over mainRoot", missingSub, wtRoot, mainRoot, wtRoot},
		{"missing subdir prefers existing wtPath even with empty mainRoot", missingSub, wtRoot, "", wtRoot},
		{"missing wtPath falls back to mainRoot", missingDir, missingDir, mainRoot, mainRoot},
		{"non-dir wtPath falls back to mainRoot", missingDir, fileNotDir, mainRoot, mainRoot},
		{"missing all preserves original cwd", missingSub, missingDir, missingMain, missingSub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRestoreCWD(tc.cwd, tc.wtPath, tc.mainRoot); got != tc.want {
				t.Fatalf("resolveRestoreCWD(%q, %q, %q) = %q, want %q", tc.cwd, tc.wtPath, tc.mainRoot, got, tc.want)
			}
		})
	}
}

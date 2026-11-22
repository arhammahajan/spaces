package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
	"github.com/arhammahajan/spaces/internal/worktree"
)

func initTempGitRepo(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestWorktreeBackendCreateTabSpaceAndCLI(t *testing.T) {
	repoDir := initTempGitRepo(t, "demo-repo")
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	statePath := filepath.Join(t.TempDir(), "worktrees.json")

	var tmuxCalls [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			tmuxCalls = append(tmuxCalls, append([]string(nil), args...))
			switch args[0] {
			case "display-message":
				if len(args) >= 4 && strings.Contains(args[3], "\x1f") {
					return "dev\x1f%1\n", "", 0, nil
				}
				return "dev\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fdev\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "dev\x1f@1\x1f0\x1fzsh\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return "dev\x1f@1\x1f0\x1fzsh\x1f%1\x1fzsh\x1fzsh\x1f" + repoDir + "\x1f100\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f0\n", "", 0, nil
			case "new-window":
				return "@9\n", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	client := tmux.NewClient(runner)
	client.Getenv = func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	b := newTUIBackend(client)
	b.wt = &worktree.Manager{
		Store:        &worktree.Store{Path: statePath},
		WorktreeRoot: wtRoot,
	}

	// 1. CreateTab with empty TabName + New worktree (auto-named from harness title).
	err := b.CreateTab(context.Background(), tui.WorktreeCreateRequest{
		Session:      "dev",
		TabName:      "", // optional: must omit -n so tmux auto-renames to active process!
		Folder:       repoDir,
		UseWorktree:  true,
		CreateNew:    true,
		HarnessApp:   "claude-code",
		HarnessTitle: "Fix OAuth Race Condition",
	})
	if err != nil {
		t.Fatalf("CreateTab error: %v", err)
	}

	// Verify new-window omitted -n and set @spaces_worktree_path on @9.
	var newWinArgs []string
	var setWinOptPath bool
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-window" {
			newWinArgs = call
		}
		if len(call) >= 6 && call[0] == "set-option" && call[1] == "-w" && call[3] == "@9" && call[4] == "@spaces_worktree_path" {
			setWinOptPath = true
		}
	}
	if len(newWinArgs) == 0 {
		t.Fatal("expected tmux new-window call")
	}
	for _, a := range newWinArgs {
		if a == "-n" {
			t.Fatalf("blank TabName must NOT pass -n to tmux new-window: %v", newWinArgs)
		}
	}
	if !setWinOptPath {
		t.Fatalf("expected @spaces_worktree_path set on new window @9, calls=%v", tmuxCalls)
	}

	// 2. CreateSpaceWithWorktree with empty SpaceName auto-derives space name and tags session.
	tmuxCalls = nil
	err = b.CreateSpaceWithWorktree(context.Background(), tui.WorktreeCreateRequest{
		SpaceName:   "",
		Folder:      repoDir,
		UseWorktree: true,
		CreateNew:   true,
		BranchName:  "feat/billing",
	})
	if err != nil {
		t.Fatalf("CreateSpaceWithWorktree error: %v", err)
	}
	var createdSession string
	var taggedSession bool
	for _, call := range tmuxCalls {
		if len(call) >= 4 && call[0] == "new-session" && call[2] == "-s" {
			createdSession = call[3]
		}
		if len(call) >= 5 && call[0] == "set-option" && call[1] == "-t" && call[3] == "@spaces_worktree_path" {
			taggedSession = true
		}
	}
	if createdSession != "feat-billing" {
		t.Fatalf("auto-derived space name = %q, want feat-billing", createdSession)
	}
	if !taggedSession {
		t.Fatalf("expected @spaces_worktree_path set on new session, calls=%v", tmuxCalls)
	}

	// 3. CLI `wt list`, `wt remove`, `wt prune` and `tabs --new` parsing.
	opts := parseTabsOptions([]string{"--new"})
	if opts.InitialMode != tui.ModeTabs || !opts.OpenNew {
		t.Fatalf("expected parseTabsOptions(--new) to open ModeTabs+OpenNew, got %+v", opts)
	}

	env := buildEnv(client)
	env.RunWorktrees = func(e cli.Env, args []string) int {
		return runWorktreesCLI(client, b, e, args)
	}
	var out, errBuf bytes.Buffer
	env.Stdout = &out
	env.Stderr = &errBuf
	if code := cli.Run([]string{"wt", "list", "--repo", repoDir}, env); code != cli.ExitOK {
		t.Fatalf("wt list exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "feat/billing") {
		t.Fatalf("wt list missing feat/billing:\n%s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := cli.Run([]string{"wt", "remove", "feat/billing", "--repo", repoDir, "--delete-branch", "--yes"}, env); code != cli.ExitOK {
		t.Fatalf("wt remove exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "removed") {
		t.Fatalf("unexpected wt remove output: %q", out.String())
	}
}

func TestWorktreeBackendExistingSelectionMainVsLinked(t *testing.T) {
	repoDir := initTempGitRepo(t, "main-repo")
	subDir := filepath.Join(repoDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	statePath := filepath.Join(t.TempDir(), "worktrees.json")

	var tmuxCalls [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			tmuxCalls = append(tmuxCalls, append([]string(nil), args...))
			switch args[0] {
			case "display-message":
				if len(args) >= 4 && strings.Contains(args[3], "\x1f") {
					return "dev\x1f%1\n", "", 0, nil
				}
				return "dev\n", "", 0, nil
			case "list-sessions":
				return "$0\x1fdev\x1f1\x1f1700000000\x1f1\n", "", 0, nil
			case "list-windows":
				return "dev\x1f@1\x1f0\x1fzsh\x1f1\x1f0\x1f0\x1f0\n", "", 0, nil
			case "list-panes":
				return "dev\x1f@1\x1f0\x1fzsh\x1f%1\x1fzsh\x1fzsh\x1f" + repoDir + "\x1f100\x1f1\x1f1\x1f0\x1f0\x1f0\x1f0\x1f\x1f\x1f\x1f\x1f0\n", "", 0, nil
			case "new-window":
				return "@11\n", "", 0, nil
			}
			return "", "", 0, nil
		},
	}

	client := tmux.NewClient(runner)
	client.Getenv = func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	b := newTUIBackend(client)
	b.wt = &worktree.Manager{
		Store:        &worktree.Store{Path: statePath},
		WorktreeRoot: wtRoot,
	}

	ctx := context.Background()
	created, err := b.wt.Create(ctx, worktree.CreateRequest{
		RepoDir: repoDir,
		Branch:  "feat/existing-linked",
	})
	if err != nil {
		t.Fatalf("create linked worktree: %v", err)
	}
	linkedPath := created.Worktree.Path

	// InspectWorktrees must keep the main checkout selectable alongside linked worktrees.
	wtCtx, err := b.InspectWorktrees(ctx, repoDir, tui.WorktreeNamingInput{})
	if err != nil {
		t.Fatalf("InspectWorktrees: %v", err)
	}
	foundMain, foundLinked := false, false
	for _, entry := range wtCtx.Worktrees {
		if entry.IsMain && canonicalPathClean(entry.Path) == canonicalPathClean(repoDir) {
			foundMain = true
		}
		if !entry.IsMain && canonicalPathClean(entry.Path) == canonicalPathClean(linkedPath) {
			foundLinked = true
		}
	}
	if !foundMain || !foundLinked {
		t.Fatalf("expected InspectWorktrees to include both main and linked worktrees, got %+v", wtCtx.Worktrees)
	}

	findFlagValue := func(args []string, flag string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}
	worktreeOptionCalls := func(calls [][]string) map[string]string {
		opts := map[string]string{}
		for _, call := range calls {
			if len(call) == 0 || call[0] != "set-option" {
				continue
			}
			for i := 0; i+1 < len(call); i++ {
				if strings.HasPrefix(call[i], "@spaces_worktree_") {
					opts[call[i]] = call[i+1]
				}
			}
		}
		return opts
	}

	// 1. CreateTab selecting the main checkout via ExistingPath opens in main checkout
	//    and sets NO @spaces_worktree_* options.
	tmuxCalls = nil
	if err := b.CreateTab(ctx, tui.WorktreeCreateRequest{
		Session:      "dev",
		Folder:       repoDir,
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: repoDir,
	}); err != nil {
		t.Fatalf("CreateTab(main existing): %v", err)
	}
	var gotNewWinCWD string
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-window" {
			gotNewWinCWD = findFlagValue(call, "-c")
		}
	}
	if canonicalPathClean(gotNewWinCWD) != canonicalPathClean(repoDir) {
		t.Fatalf("CreateTab(main existing) new-window -c = %q, want %q", gotNewWinCWD, repoDir)
	}
	if opts := worktreeOptionCalls(tmuxCalls); len(opts) != 0 {
		t.Fatalf("CreateTab(main existing) must not set @spaces_worktree_* options, got %v", opts)
	}

	// 1b. CreateTab selecting main checkout while staying in a subdirectory preserves subdir
	//     and still sets NO @spaces_worktree_* options.
	tmuxCalls = nil
	if err := b.CreateTab(ctx, tui.WorktreeCreateRequest{
		Session:      "dev",
		Folder:       subDir,
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: repoDir,
	}); err != nil {
		t.Fatalf("CreateTab(main existing subdir): %v", err)
	}
	gotNewWinCWD = ""
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-window" {
			gotNewWinCWD = findFlagValue(call, "-c")
		}
	}
	if canonicalPathClean(gotNewWinCWD) != canonicalPathClean(subDir) {
		t.Fatalf("CreateTab(main existing subdir) new-window -c = %q, want %q", gotNewWinCWD, subDir)
	}
	if opts := worktreeOptionCalls(tmuxCalls); len(opts) != 0 {
		t.Fatalf("CreateTab(main existing subdir) must not set @spaces_worktree_* options, got %v", opts)
	}

	// 2. CreateSpaceWithWorktree selecting the main checkout via ExistingPath opens in main
	//    checkout and sets NO @spaces_worktree_* options.
	tmuxCalls = nil
	if err := b.CreateSpaceWithWorktree(ctx, tui.WorktreeCreateRequest{
		SpaceName:    "",
		Folder:       "",
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: repoDir,
	}); err != nil {
		t.Fatalf("CreateSpaceWithWorktree(main existing): %v", err)
	}
	var gotSessName, gotSessCWD string
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-session" {
			gotSessName = findFlagValue(call, "-s")
			gotSessCWD = findFlagValue(call, "-c")
		}
	}
	if gotSessName != "main-repo" {
		t.Fatalf("CreateSpaceWithWorktree(main existing) session = %q, want main-repo", gotSessName)
	}
	if canonicalPathClean(gotSessCWD) != canonicalPathClean(repoDir) {
		t.Fatalf("CreateSpaceWithWorktree(main existing) -c = %q, want %q", gotSessCWD, repoDir)
	}
	if opts := worktreeOptionCalls(tmuxCalls); len(opts) != 0 {
		t.Fatalf("CreateSpaceWithWorktree(main existing) must not set @spaces_worktree_* options, got %v", opts)
	}

	// 3. CreateTab selecting a linked existing worktree opens in that worktree AND tags it.
	tmuxCalls = nil
	if err := b.CreateTab(ctx, tui.WorktreeCreateRequest{
		Session:      "dev",
		Folder:       repoDir,
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: linkedPath,
	}); err != nil {
		t.Fatalf("CreateTab(linked existing): %v", err)
	}
	gotNewWinCWD = ""
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-window" {
			gotNewWinCWD = findFlagValue(call, "-c")
		}
	}
	if canonicalPathClean(gotNewWinCWD) != canonicalPathClean(linkedPath) {
		t.Fatalf("CreateTab(linked existing) new-window -c = %q, want %q", gotNewWinCWD, linkedPath)
	}
	winOpts := worktreeOptionCalls(tmuxCalls)
	if canonicalPathClean(winOpts["@spaces_worktree_path"]) != canonicalPathClean(linkedPath) {
		t.Fatalf("CreateTab(linked existing) @spaces_worktree_path = %q, want %q", winOpts["@spaces_worktree_path"], linkedPath)
	}
	if winOpts["@spaces_worktree_name"] != created.Worktree.Name {
		t.Fatalf("CreateTab(linked existing) @spaces_worktree_name = %q, want %q", winOpts["@spaces_worktree_name"], created.Worktree.Name)
	}
	if winOpts["@spaces_worktree_base"] != created.Worktree.BaseBranch {
		t.Fatalf("CreateTab(linked existing) @spaces_worktree_base = %q, want %q", winOpts["@spaces_worktree_base"], created.Worktree.BaseBranch)
	}
	if winOpts["@spaces_worktree_managed"] != "1" {
		t.Fatalf("CreateTab(linked existing) @spaces_worktree_managed = %q, want 1", winOpts["@spaces_worktree_managed"])
	}

	// 4. CreateSpaceWithWorktree selecting a linked existing worktree opens in that worktree AND tags it.
	tmuxCalls = nil
	if err := b.CreateSpaceWithWorktree(ctx, tui.WorktreeCreateRequest{
		SpaceName:    "",
		Folder:       linkedPath,
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: linkedPath,
	}); err != nil {
		t.Fatalf("CreateSpaceWithWorktree(linked existing): %v", err)
	}
	gotSessName, gotSessCWD = "", ""
	for _, call := range tmuxCalls {
		if len(call) > 0 && call[0] == "new-session" {
			gotSessName = findFlagValue(call, "-s")
			gotSessCWD = findFlagValue(call, "-c")
		}
	}
	if gotSessName != created.Worktree.Name {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) session = %q, want %q", gotSessName, created.Worktree.Name)
	}
	if canonicalPathClean(gotSessCWD) != canonicalPathClean(linkedPath) {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) -c = %q, want %q", gotSessCWD, linkedPath)
	}
	sessOpts := worktreeOptionCalls(tmuxCalls)
	if canonicalPathClean(sessOpts["@spaces_worktree_path"]) != canonicalPathClean(linkedPath) {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) @spaces_worktree_path = %q, want %q", sessOpts["@spaces_worktree_path"], linkedPath)
	}
	if sessOpts["@spaces_worktree_name"] != created.Worktree.Name {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) @spaces_worktree_name = %q, want %q", sessOpts["@spaces_worktree_name"], created.Worktree.Name)
	}
	if sessOpts["@spaces_worktree_base"] != created.Worktree.BaseBranch {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) @spaces_worktree_base = %q, want %q", sessOpts["@spaces_worktree_base"], created.Worktree.BaseBranch)
	}
	if sessOpts["@spaces_worktree_managed"] != "1" {
		t.Fatalf("CreateSpaceWithWorktree(linked existing) @spaces_worktree_managed = %q, want 1", sessOpts["@spaces_worktree_managed"])
	}
}

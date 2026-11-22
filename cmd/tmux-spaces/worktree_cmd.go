package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
	"github.com/arhammahajan/spaces/internal/worktree"
)

var _ tui.WorktreeBackend = (*tuiBackend)(nil)

func toNamingContext(in tui.WorktreeNamingInput) worktree.NamingContext {
	return worktree.NamingContext{
		ExplicitName:  in.ExplicitName,
		TargetName:    in.TargetName,
		TargetKind:    in.TargetKind,
		HarnessApp:    in.HarnessApp,
		HarnessTitle:  in.HarnessTitle,
		HarnessPrompt: in.HarnessPrompt,
		Seed:          in.Seed,
	}
}

func canonicalPathClean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

func pathMatchesWorktree(cwd, wtPath string) bool {
	c := canonicalPathClean(cwd)
	w := canonicalPathClean(wtPath)
	if c == "" || w == "" {
		return false
	}
	if c == w {
		return true
	}
	return strings.HasPrefix(c, w+string(filepath.Separator))
}

func (b *tuiBackend) worktreeLinkage(spaces []model.Space, wtPath string) (openSpaces []string, openWindowIDs []string, windowCount int) {
	spaceSeen := map[string]bool{}
	winSeen := map[string]bool{}
	for _, sp := range spaces {
		for _, w := range sp.Windows {
			if w.WorktreePath != "" && pathMatchesWorktree(w.WorktreePath, wtPath) {
				if !winSeen[w.ID] {
					winSeen[w.ID] = true
					openWindowIDs = append(openWindowIDs, w.ID)
				}
				if !spaceSeen[sp.Name] {
					spaceSeen[sp.Name] = true
					openSpaces = append(openSpaces, sp.Name)
				}
			}
		}
		for _, p := range sp.Panes {
			if p.CWD != "" && pathMatchesWorktree(p.CWD, wtPath) {
				if p.WindowID != "" && !winSeen[p.WindowID] {
					winSeen[p.WindowID] = true
					openWindowIDs = append(openWindowIDs, p.WindowID)
				}
				if !spaceSeen[sp.Name] {
					spaceSeen[sp.Name] = true
					openSpaces = append(openSpaces, sp.Name)
				}
			}
		}
	}
	sort.Strings(openSpaces)
	return openSpaces, openWindowIDs, len(winSeen)
}

func (b *tuiBackend) convertWorktree(wt worktree.Worktree, currentWT string, spaces []model.Space) model.WorktreeEntry {
	openSpaces, openWinIDs, openCount := b.worktreeLinkage(spaces, wt.Path)
	var gitInfo *model.GitInfo
	if b.git != nil {
		gitInfo = b.git.Cached(wt.Path)
	}
	isCur := currentWT != "" && canonicalPathClean(wt.Path) == canonicalPathClean(currentWT)
	return model.WorktreeEntry{
		Path:            wt.Path,
		Name:            wt.Name,
		Branch:          wt.Branch,
		HeadSHA:         wt.HeadSHA,
		BaseBranch:      wt.BaseBranch,
		RepoKey:         wt.RepoKey,
		MainRepoRoot:    wt.MainRepoRoot,
		RepoName:        wt.RepoName,
		IsMain:          wt.IsMain,
		IsCurrent:       isCur,
		IsDetached:      wt.IsDetached,
		IsLocked:        wt.IsLocked,
		IsPrunable:      wt.IsPrunable,
		IsDirty:         wt.IsDirty,
		Managed:         wt.Managed,
		NameSource:      wt.NameSource,
		CreatedAtMs:     wt.CreatedAtMs,
		UnmergedCommits: wt.UnmergedCommits,
		OpenSpaces:      openSpaces,
		OpenWindowIDs:   openWinIDs,
		OpenWindowCount: openCount,
		Git:             gitInfo,
	}
}

func (b *tuiBackend) InspectWorktrees(ctx context.Context, dir string, naming tui.WorktreeNamingInput) (model.WorktreeContext, error) {
	if b.wt == nil || strings.TrimSpace(dir) == "" {
		return model.WorktreeContext{}, nil
	}
	repo, wts, err := b.wt.List(ctx, dir)
	if err != nil || repo == nil {
		return model.WorktreeContext{InRepo: false}, nil
	}
	branches, _ := b.wt.ListBranches(ctx, dir)
	_, suggestion := b.wt.SuggestForDir(ctx, dir, toNamingContext(naming))

	var spaces []model.Space
	if snap, serr := b.client.Snapshot(ctx); serr == nil {
		spaces = model.BuildSpaces(snap, b.now().UnixMilli())
	}

	entries := make([]model.WorktreeEntry, 0, len(wts))
	for _, wt := range wts {
		if wt.IsBare {
			continue
		}
		entries = append(entries, b.convertWorktree(wt, repo.CurrentWorktreePath, spaces))
	}
	wtRoot := b.wt.WorktreeRoot
	if wtRoot == "" {
		wtRoot = worktree.DefaultWorktreeRoot()
	}
	return model.WorktreeContext{
		InRepo:        true,
		RepoKey:       repo.RepoKey,
		MainRepoRoot:  repo.MainRoot,
		RepoName:      repo.RepoName,
		WorktreeRoot:  wtRoot,
		DefaultBase:   repo.DefaultBase,
		CurrentBranch: repo.CurrentBranch,
		CurrentPath:   repo.CurrentWorktreePath,
		HasCommits:    repo.HasCommits,
		Worktrees:     entries,
		Branches:      branches,
		AutoBranch:    suggestion.Branch,
		AutoDirName:   suggestion.DirName,
		AutoSource:    suggestion.Source,
		AutoRule:      suggestion.Rule,
	}, nil
}

func (b *tuiBackend) SuggestWorktreeName(ctx context.Context, dir string, naming tui.WorktreeNamingInput) (branch, dirName, source, rule string) {
	if b.wt == nil {
		res := worktree.SuggestName(toNamingContext(naming), nil)
		return res.Branch, res.DirName, res.Source, res.Rule
	}
	_, res := b.wt.SuggestForDir(ctx, dir, toNamingContext(naming))
	return res.Branch, res.DirName, res.Source, res.Rule
}

func (b *tuiBackend) ListAllWorktrees(ctx context.Context, spaces []model.Space) ([]model.WorktreeEntry, error) {
	if b.wt == nil {
		return nil, nil
	}
	if len(spaces) == 0 {
		if snap, err := b.client.Snapshot(ctx); err == nil {
			spaces = model.BuildSpaces(snap, b.now().UnixMilli())
		}
	}

	// Find current active CWD so we can mark the current worktree.
	currentCWD := ""
	for _, sp := range spaces {
		if sp.IsCurrent {
			currentCWD = sp.CWD
			for _, p := range sp.Panes {
				if p.IsActive && p.CWD != "" {
					currentCWD = p.CWD
					break
				}
			}
			break
		}
	}
	currentWT := ""
	if currentCWD != "" {
		if r, err := b.wt.DiscoverRepo(ctx, currentCWD); err == nil && r != nil {
			currentWT = r.CurrentWorktreePath
		}
	}

	// Collect candidate directories from live spaces + persisted store records.
	var candidateDirs []string
	seenDirs := map[string]bool{}
	addDir := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" || seenDirs[d] {
			return
		}
		seenDirs[d] = true
		candidateDirs = append(candidateDirs, d)
	}
	for _, sp := range spaces {
		addDir(sp.CWD)
		addDir(sp.WorktreePath)
		for _, w := range sp.Windows {
			addDir(w.WorktreePath)
		}
		for _, p := range sp.Panes {
			addDir(p.CWD)
		}
	}
	if b.wt.Store != nil {
		if recs, err := b.wt.Store.Load(); err == nil {
			for _, rec := range recs {
				addDir(rec.MainRepoRoot)
				addDir(rec.Path)
			}
		}
	}

	seenRepos := map[string]bool{}
	seenPaths := map[string]bool{}
	var out []model.WorktreeEntry
	for _, dir := range candidateDirs {
		repo, wts, err := b.wt.List(ctx, dir)
		if err != nil || repo == nil || seenRepos[repo.RepoKey] {
			continue
		}
		seenRepos[repo.RepoKey] = true
		for _, wt := range wts {
			if wt.IsBare {
				continue
			}
			cp := canonicalPathClean(wt.Path)
			if seenPaths[cp] {
				continue
			}
			seenPaths[cp] = true
			out = append(out, b.convertWorktree(wt, currentWT, spaces))
		}
	}
	return out, nil
}

func (b *tuiBackend) tagWindowWorktree(ctx context.Context, windowID, wtPath, wtName, wtBase string, managed bool) {
	if b.client == nil || strings.TrimSpace(windowID) == "" || strings.TrimSpace(wtPath) == "" {
		return
	}
	_ = b.client.SetWindowOption(ctx, windowID, "@spaces_worktree_path", wtPath)
	if wtName != "" {
		_ = b.client.SetWindowOption(ctx, windowID, "@spaces_worktree_name", wtName)
	}
	if wtBase != "" {
		_ = b.client.SetWindowOption(ctx, windowID, "@spaces_worktree_base", wtBase)
	}
	if managed {
		_ = b.client.SetWindowOption(ctx, windowID, "@spaces_worktree_managed", "1")
	}
}

func (b *tuiBackend) tagSessionWorktree(ctx context.Context, session, wtPath, wtName, wtBase string, managed bool) {
	if b.client == nil || strings.TrimSpace(session) == "" || strings.TrimSpace(wtPath) == "" {
		return
	}
	_ = b.client.SetSessionOption(ctx, session, "@spaces_worktree_path", wtPath)
	if wtName != "" {
		_ = b.client.SetSessionOption(ctx, session, "@spaces_worktree_name", wtName)
	}
	if wtBase != "" {
		_ = b.client.SetSessionOption(ctx, session, "@spaces_worktree_base", wtBase)
	}
	if managed {
		_ = b.client.SetSessionOption(ctx, session, "@spaces_worktree_managed", "1")
	}
}

func (b *tuiBackend) isMainWorktreePath(ctx context.Context, path string) bool {
	if b.wt == nil {
		return false
	}
	canonPath := canonicalPathClean(path)
	if canonPath == "" {
		return false
	}
	repo, err := b.wt.DiscoverRepo(ctx, canonPath)
	if err != nil || repo == nil {
		return false
	}
	canonMain := canonicalPathClean(repo.MainRoot)
	if canonMain == "" {
		return false
	}
	if canonPath == canonMain {
		return true
	}
	return repo.CurrentWorktreePath != "" && canonicalPathClean(repo.CurrentWorktreePath) == canonMain
}

func (b *tuiBackend) resolveWorktreeSelection(
	ctx context.Context,
	req tui.WorktreeCreateRequest,
	targetKind, targetName, originSpace string,
) (targetDir, wtPath, wtName, wtBase string, managed bool, err error) {
	targetDir = strings.TrimSpace(req.Folder)

	if req.UseWorktree && req.CreateNew && b.wt != nil {
		res, createErr := b.wt.Create(ctx, worktree.CreateRequest{
			RepoDir: targetDir,
			Branch:  strings.TrimSpace(req.BranchName),
			BaseRef: strings.TrimSpace(req.BaseBranch),
			Naming: worktree.NamingContext{
				ExplicitName:  strings.TrimSpace(req.BranchName),
				TargetName:    strings.TrimSpace(targetName),
				TargetKind:    targetKind,
				HarnessApp:    req.HarnessApp,
				HarnessTitle:  req.HarnessTitle,
				HarnessPrompt: req.HarnessPrompt,
				Seed:          req.AutoSeed,
			},
			OriginSpace: originSpace,
		})
		if createErr != nil {
			return "", "", "", "", false, createErr
		}
		return res.Worktree.Path, res.Worktree.Path, res.Worktree.Name, res.Worktree.BaseBranch, true, nil
	}

	if req.UseWorktree && strings.TrimSpace(req.ExistingPath) != "" {
		existingPath := strings.TrimSpace(req.ExistingPath)
		if targetDir == "" || !pathMatchesWorktree(targetDir, existingPath) {
			targetDir = existingPath
		}
		// Keep the main checkout selectable for opening tabs/spaces, but do not
		// emit linked-worktree metadata so tmux does not tag it with
		// @spaces_worktree_* (no linked-worktree badge or remove prompt on close).
		if b.isMainWorktreePath(ctx, existingPath) {
			return targetDir, "", "", "", false, nil
		}
		wtPath = existingPath
		wtName = filepath.Base(wtPath)
		if b.wt != nil && b.wt.Store != nil {
			if rec, ok, serr := b.wt.Store.GetByPath(wtPath); serr == nil && ok {
				if rec.Name != "" {
					wtName = rec.Name
				}
				wtBase = rec.BaseBranch
				managed = rec.Managed
				rec.LastUsedAtMs = b.now().UnixMilli()
				_ = b.wt.Store.Upsert(rec)
			}
		}
		return targetDir, wtPath, wtName, wtBase, managed, nil
	}

	return targetDir, "", "", "", false, nil
}

func (b *tuiBackend) CreateTab(ctx context.Context, req tui.WorktreeCreateRequest) error {
	session := strings.TrimSpace(req.Session)
	if session == "" {
		cur, err := b.client.CurrentSession(ctx)
		if err != nil {
			return fmt.Errorf("could not determine current space: %w", err)
		}
		session = cur
	}

	targetDir, wtPath, wtName, wtBase, managed, err := b.resolveWorktreeSelection(
		ctx, req, "tab", req.TabName, session,
	)
	if err != nil {
		return err
	}

	// Note: when req.TabName == "", NewWindowReturningID omits `-n` so tmux
	// preserves `automatic-rename on` (showing the active process name).
	winID, err := b.client.NewWindowReturningID(ctx, session, targetDir, strings.TrimSpace(req.TabName))
	if err != nil {
		return err
	}
	if wtPath != "" {
		b.tagWindowWorktree(ctx, winID, wtPath, wtName, wtBase, managed)
		if managed && b.wt != nil && b.wt.Store != nil && winID != "" {
			if rec, ok, err := b.wt.Store.GetByPath(wtPath); err == nil && ok {
				rec.OriginSpace = session
				rec.OriginWindowID = winID
				rec.LastUsedAtMs = b.now().UnixMilli()
				_ = b.wt.Store.Upsert(rec)
			}
		}
	}
	return nil
}

func (b *tuiBackend) CreateSpaceWithWorktree(ctx context.Context, req tui.WorktreeCreateRequest) error {
	targetDir, wtPath, wtName, wtBase, managed, err := b.resolveWorktreeSelection(
		ctx, req, "space", req.SpaceName, "",
	)
	if err != nil {
		return err
	}

	spaceName := strings.TrimSpace(req.SpaceName)
	if spaceName == "" {
		base := wtName
		if base == "" && targetDir != "" {
			base = filepath.Base(strings.TrimRight(targetDir, "/"))
		}
		base = strings.NewReplacer(":", "-", ".", "-", "/", "-").Replace(strings.TrimSpace(base))
		if base == "" || base == "." || base == "-" {
			base = "space"
		}
		existing := map[string]bool{}
		if sessions, err := b.client.ListSessions(ctx); err == nil {
			for _, s := range sessions {
				existing[strings.ToLower(s.Name)] = true
			}
		}
		spaceName = base
		if existing[strings.ToLower(spaceName)] {
			for i := 2; i < 1000; i++ {
				cand := fmt.Sprintf("%s-%d", base, i)
				if !existing[strings.ToLower(cand)] {
					spaceName = cand
					break
				}
			}
		}
	}

	if err := b.client.NewSession(ctx, spaceName, targetDir); err != nil {
		return err
	}
	if wtPath != "" {
		b.tagSessionWorktree(ctx, spaceName, wtPath, wtName, wtBase, managed)
		if managed && b.wt != nil && b.wt.Store != nil {
			if rec, ok, err := b.wt.Store.GetByPath(wtPath); err == nil && ok {
				rec.OriginSpace = spaceName
				rec.LastUsedAtMs = b.now().UnixMilli()
				_ = b.wt.Store.Upsert(rec)
			}
		}
	}
	return nil
}

func (b *tuiBackend) InspectWorktreeSafety(ctx context.Context, wtPath string) (tui.WorktreeRemoveSafety, error) {
	if b.wt == nil {
		return tui.WorktreeRemoveSafety{}, fmt.Errorf("worktree manager unavailable")
	}
	s, err := b.wt.InspectSafety(ctx, wtPath)
	if err != nil {
		return tui.WorktreeRemoveSafety{}, err
	}
	return tui.WorktreeRemoveSafety{
		CanRemoveSafely: s.CanRemoveSafely,
		IsMain:          s.IsMain,
		IsDirty:         s.IsDirty,
		UnmergedCommits: s.UnmergedCommits,
		Branch:          s.Branch,
		BaseBranch:      s.BaseBranch,
		Warnings:        s.Warnings,
	}, nil
}

func (b *tuiBackend) RemoveWorktree(ctx context.Context, wtPath string, force, deleteBranch bool, closeWindows []string) error {
	if b.wt == nil {
		return fmt.Errorf("worktree manager unavailable")
	}
	for _, winID := range closeWindows {
		if strings.TrimSpace(winID) != "" {
			_ = b.client.KillWindow(ctx, winID)
		}
	}
	return b.wt.Remove(ctx, wtPath, worktree.RemoveOptions{
		Force:        force,
		DeleteBranch: deleteBranch,
		ForceBranch:  force && deleteBranch,
	})
}

func (b *tuiBackend) PruneWorktrees(ctx context.Context, spaces []model.Space, removeOrphanedMerged bool) (string, error) {
	if b.wt == nil {
		return "", fmt.Errorf("worktree manager unavailable")
	}
	if len(spaces) == 0 {
		if snap, err := b.client.Snapshot(ctx); err == nil {
			spaces = model.BuildSpaces(snap, b.now().UnixMilli())
		}
	}
	var repoDirs []string
	activePaths := map[string]bool{}
	for _, sp := range spaces {
		if sp.CWD != "" {
			repoDirs = append(repoDirs, sp.CWD)
			activePaths[canonicalPathClean(sp.CWD)] = true
		}
		if sp.WorktreePath != "" {
			repoDirs = append(repoDirs, sp.WorktreePath)
			activePaths[canonicalPathClean(sp.WorktreePath)] = true
		}
		for _, w := range sp.Windows {
			if w.WorktreePath != "" {
				repoDirs = append(repoDirs, w.WorktreePath)
				activePaths[canonicalPathClean(w.WorktreePath)] = true
			}
		}
		for _, p := range sp.Panes {
			if p.CWD != "" {
				repoDirs = append(repoDirs, p.CWD)
				activePaths[canonicalPathClean(p.CWD)] = true
			}
		}
	}
	res, err := b.wt.Prune(ctx, repoDirs, removeOrphanedMerged, activePaths)
	if err != nil {
		return "", err
	}
	var parts []string
	if res.PrunedGit > 0 {
		parts = append(parts, fmt.Sprintf("pruned %d repo(s)", res.PrunedGit))
	}
	if res.PrunedRecords > 0 {
		parts = append(parts, fmt.Sprintf("cleaned %d stale record(s)", res.PrunedRecords))
	}
	if len(res.RemovedPaths) > 0 {
		parts = append(parts, fmt.Sprintf("removed %d merged worktree(s)", len(res.RemovedPaths)))
	}
	if len(parts) == 0 {
		return "All worktrees clean — nothing to prune.", nil
	}
	return strings.Join(parts, ", "), nil
}

// ---------------------------------------------------------------------------
// CLI: `tmux-spaces worktrees|wt [list|new|remove|prune]`
// ---------------------------------------------------------------------------

func runWorktreesCLI(client *tmux.Client, backend *tuiBackend, env cli.Env, args []string) int {
	if len(args) == 0 {
		opts := tui.Options{
			InitialMode: tui.ModeWorktrees,
			HomeDir:     homeDir(),
		}
		if err := tui.Run(backend, opts); err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees: %v\n", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintf(env.Stdout, `usage: tmux-spaces worktrees [subcommand] [args]

  (no args)                                open the Worktrees TUI popup
  list [--repo DIR] [--json]               list git worktrees across active spaces
  new [BRANCH] [--base REF] [--repo DIR] [--tab [NAME] | --space [NAME]] [--json]
                                           create a new git worktree (auto-named if omitted)
  remove <NAME|PATH> [--force] [--delete-branch] [--yes]
                                           safely remove a linked git worktree
  prune [--merged]                         prune stale git worktrees and registry entries
`)
		return cli.ExitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		asJSON := false
		repoDir := ""
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--json":
				asJSON = true
			case "--repo":
				if i+1 < len(rest) {
					i++
					repoDir = rest[i]
				}
			default:
				if v, ok := strings.CutPrefix(rest[i], "--repo="); ok {
					repoDir = v
				} else {
					fmt.Fprintf(env.Stderr, "tmux-spaces worktrees list: unknown arg %q\n", rest[i])
					return cli.ExitUsage
				}
			}
		}
		var entries []model.WorktreeEntry
		var err error
		if repoDir != "" {
			wtCtx, ierr := backend.InspectWorktrees(ctx, repoDir, tui.WorktreeNamingInput{})
			if ierr != nil {
				err = ierr
			} else {
				entries = wtCtx.Worktrees
			}
		} else {
			entries, err = backend.ListAllWorktrees(ctx, nil)
		}
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees list: %v\n", err)
			return cli.ExitError
		}
		if asJSON {
			raw, _ := json.Marshal(entries)
			fmt.Fprintf(env.Stdout, "%s\n", raw)
			return cli.ExitOK
		}
		if len(entries) == 0 {
			fmt.Fprintf(env.Stdout, "no worktrees found\n")
			return cli.ExitOK
		}
		fmt.Fprintf(env.Stdout, "  %-18s %-24s %-8s %-10s %s\n", "REPO", "BRANCH", "STATE", "TABS", "PATH")
		for _, wt := range entries {
			marker := " "
			if wt.IsCurrent {
				marker = "*"
			}
			state := "clean"
			if wt.IsDirty {
				state = "dirty"
			} else if wt.IsPrunable {
				state = "missing"
			}
			tabs := "detached"
			if wt.IsMain {
				tabs = "main"
				if wt.OpenWindowCount > 0 {
					tabs = fmt.Sprintf("main(%d)", wt.OpenWindowCount)
				}
			} else if wt.OpenWindowCount > 0 {
				tabs = fmt.Sprintf("%d tab(s)", wt.OpenWindowCount)
			}
			branch := wt.Branch
			if branch == "" {
				branch = "(detached)"
			}
			fmt.Fprintf(env.Stdout, "%s %-18s %-24s %-8s %-10s %s\n", marker, wt.RepoName, branch, state, tabs, wt.Path)
		}
		return cli.ExitOK

	case "new", "add":
		var branch, baseRef, repoDir, tabName, spaceName string
		openTab := false
		openSpace := false
		asJSON := false
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			switch {
			case a == "--json":
				asJSON = true
			case a == "--base" && i+1 < len(rest):
				i++
				baseRef = rest[i]
			case strings.HasPrefix(a, "--base="):
				baseRef = strings.TrimPrefix(a, "--base=")
			case a == "--repo" && i+1 < len(rest):
				i++
				repoDir = rest[i]
			case strings.HasPrefix(a, "--repo="):
				repoDir = strings.TrimPrefix(a, "--repo=")
			case a == "--tab":
				openTab = true
				if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
					i++
					tabName = rest[i]
				}
			case strings.HasPrefix(a, "--tab="):
				openTab = true
				tabName = strings.TrimPrefix(a, "--tab=")
			case a == "--space":
				openSpace = true
				if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
					i++
					spaceName = rest[i]
				}
			case strings.HasPrefix(a, "--space="):
				openSpace = true
				spaceName = strings.TrimPrefix(a, "--space=")
			case strings.HasPrefix(a, "-"):
				fmt.Fprintf(env.Stderr, "tmux-spaces worktrees new: unknown flag %q\n", a)
				return cli.ExitUsage
			default:
				if branch == "" {
					branch = a
				} else {
					fmt.Fprintf(env.Stderr, "tmux-spaces worktrees new: unexpected arg %q\n", a)
					return cli.ExitUsage
				}
			}
		}
		if repoDir == "" {
			if pwd := env.Getenv("PWD"); pwd != "" {
				repoDir = pwd
			} else if wd, err := os.Getwd(); err == nil {
				repoDir = wd
			}
		}
		if openSpace {
			if err := backend.CreateSpaceWithWorktree(ctx, tui.WorktreeCreateRequest{
				SpaceName:   spaceName,
				Folder:      repoDir,
				UseWorktree: true,
				CreateNew:   true,
				BranchName:  branch,
				BaseBranch:  baseRef,
			}); err != nil {
				fmt.Fprintf(env.Stderr, "tmux-spaces worktrees new: %v\n", err)
				return cli.ExitError
			}
			return cli.ExitOK
		}
		if openTab {
			if err := backend.CreateTab(ctx, tui.WorktreeCreateRequest{
				TabName:     tabName,
				Folder:      repoDir,
				UseWorktree: true,
				CreateNew:   true,
				BranchName:  branch,
				BaseBranch:  baseRef,
			}); err != nil {
				fmt.Fprintf(env.Stderr, "tmux-spaces worktrees new: %v\n", err)
				return cli.ExitError
			}
			return cli.ExitOK
		}
		res, err := backend.wt.Create(ctx, worktree.CreateRequest{
			RepoDir: repoDir,
			Branch:  branch,
			BaseRef: baseRef,
			Naming: worktree.NamingContext{
				ExplicitName: branch,
				TargetName:   tabName,
			},
		})
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees new: %v\n", err)
			return cli.ExitError
		}
		if asJSON {
			raw, _ := json.Marshal(res)
			fmt.Fprintf(env.Stdout, "%s\n", raw)
			return cli.ExitOK
		}
		fmt.Fprintf(env.Stdout, "%s\t%s\n", res.Worktree.Branch, res.Worktree.Path)
		return cli.ExitOK

	case "remove", "rm", "delete":
		var target, repoDir string
		force := false
		deleteBranch := false
		yes := false
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			switch {
			case a == "--force" || a == "-f":
				force = true
			case a == "--delete-branch" || a == "-b":
				deleteBranch = true
			case a == "--yes" || a == "-y":
				yes = true
			case a == "--repo" && i+1 < len(rest):
				i++
				repoDir = rest[i]
			case strings.HasPrefix(a, "--repo="):
				repoDir = strings.TrimPrefix(a, "--repo=")
			case strings.HasPrefix(a, "-"):
				fmt.Fprintf(env.Stderr, "tmux-spaces worktrees remove: unknown flag %q\n", a)
				return cli.ExitUsage
			default:
				if target == "" {
					target = a
				}
			}
		}
		if target == "" {
			fmt.Fprintf(env.Stderr, "usage: tmux-spaces worktrees remove <name-or-path> [--force] [--delete-branch] [--yes]\n")
			return cli.ExitUsage
		}
		if repoDir == "" {
			repoDir, _ = os.Getwd()
		}
		wtPath, err := backend.wt.ResolveWorktreePath(ctx, repoDir, target)
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees remove: %v\n", err)
			return cli.ExitError
		}
		safety, _ := backend.wt.InspectSafety(ctx, wtPath)
		if safety.IsMain {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees remove: refusing to remove main worktree %s\n", wtPath)
			return cli.ExitError
		}
		if !yes && !force && (safety.IsDirty || safety.UnmergedCommits > 0) {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees remove: %s has warnings (%s); pass --yes (and --force if dirty)\n",
				wtPath, strings.Join(safety.Warnings, "; "))
			return cli.ExitError
		}
		var closeWins []string
		if snap, serr := client.Snapshot(ctx); serr == nil {
			spaces := model.BuildSpaces(snap, backend.now().UnixMilli())
			_, closeWins, _ = backend.worktreeLinkage(spaces, wtPath)
		}
		if err := backend.RemoveWorktree(ctx, wtPath, force, deleteBranch, closeWins); err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees remove: %v\n", err)
			return cli.ExitError
		}
		fmt.Fprintf(env.Stdout, "removed %s\n", wtPath)
		return cli.ExitOK

	case "prune":
		removeMerged := false
		for _, a := range rest {
			switch a {
			case "--merged":
				removeMerged = true
			default:
				fmt.Fprintf(env.Stderr, "tmux-spaces worktrees prune: unknown flag %q\n", a)
				return cli.ExitUsage
			}
		}
		summary, err := backend.PruneWorktrees(ctx, nil, removeMerged)
		if err != nil {
			fmt.Fprintf(env.Stderr, "tmux-spaces worktrees prune: %v\n", err)
			return cli.ExitError
		}
		fmt.Fprintf(env.Stdout, "%s\n", summary)
		return cli.ExitOK

	default:
		fmt.Fprintf(env.Stderr, "tmux-spaces worktrees: unknown subcommand %q\n", sub)
		return cli.ExitUsage
	}
}

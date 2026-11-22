package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manager orchestrates Git worktree discovery, creation, safety inspection,
// removal, and pruning alongside persistent metadata in Store.
type Manager struct {
	Store        *Store
	WorktreeRoot string
	Runner       CommandRunner
	Now          func() time.Time
}

// DefaultWorktreeRoot resolves the default worktree storage directory:
// $TMUX_SPACES_WORKTREE_DIR, else $TMUX_SPACES_DATA_DIR/worktrees,
// else ${XDG_DATA_HOME:-~/.local/share}/tmux-spaces/worktrees.
func DefaultWorktreeRoot() string {
	if d := strings.TrimSpace(os.Getenv("TMUX_SPACES_WORKTREE_DIR")); d != "" {
		return filepath.Clean(d)
	}
	if d := strings.TrimSpace(os.Getenv("TMUX_SPACES_DATA_DIR")); d != "" {
		return filepath.Join(filepath.Clean(d), "worktrees")
	}
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = os.TempDir()
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tmux-spaces", "worktrees")
}

// NewManager constructs a Manager with default store, worktree root, and runner.
func NewManager() *Manager {
	return &Manager{
		Store:        DefaultStore(),
		WorktreeRoot: DefaultWorktreeRoot(),
		Runner:       defaultRunCommand,
		Now:          time.Now,
	}
}

func (m *Manager) runner() CommandRunner {
	if m != nil && m.Runner != nil {
		return m.Runner
	}
	return defaultRunCommand
}

func (m *Manager) store() *Store {
	if m != nil && m.Store != nil {
		return m.Store
	}
	return DefaultStore()
}

func (m *Manager) worktreeRoot() string {
	if m != nil && strings.TrimSpace(m.WorktreeRoot) != "" {
		return filepath.Clean(m.WorktreeRoot)
	}
	return DefaultWorktreeRoot()
}

func (m *Manager) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func defaultRunCommand(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, []byte, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.Bytes(), stderr.Bytes(), ee.ExitCode(), err
		}
		return stdout.Bytes(), stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) ([]byte, []byte, int, error) {
	full := make([]string, 0, len(args)+1)
	full = append(full, "--no-optional-locks")
	full = append(full, args...)
	return m.runner()(ctx, dir, nil, "git", full...)
}

// DiscoverRepo inspects dir (main checkout, subdirectory, or linked worktree)
// and returns normalized repository information.
func (m *Manager) DiscoverRepo(ctx context.Context, dir string) (*RepoInfo, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("worktree: repo directory is empty")
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("worktree: stat %s: %w", dir, err)
	}
	if !st.IsDir() {
		dir = filepath.Dir(dir)
	}
	canonDir := canonicalPath(dir)

	var topLevel, commonDir string
	out, stderr, code, err := m.git(ctx, canonDir, "rev-parse", "--show-toplevel", "--git-common-dir", "--git-dir")
	if err == nil && code == 0 {
		lines := splitLines(out)
		if len(lines) < 3 {
			return nil, fmt.Errorf("worktree: unexpected rev-parse output in %s", canonDir)
		}
		topLevel = canonicalPath(resolvePath(canonDir, lines[0]))
		commonDir = canonicalPath(resolvePath(canonDir, lines[1]))
	} else {
		// Check bare repository fallback.
		bOut, _, bCode, bErr := m.git(ctx, canonDir, "rev-parse", "--git-common-dir", "--is-bare-repository")
		if bErr != nil || bCode != 0 {
			msg := strings.TrimSpace(string(stderr))
			if msg == "" && err != nil {
				msg = err.Error()
			}
			return nil, fmt.Errorf("worktree: not a git repository (%s): %s", canonDir, msg)
		}
		bLines := splitLines(bOut)
		if len(bLines) < 2 || bLines[1] != "true" {
			return nil, fmt.Errorf("worktree: not a git repository: %s", canonDir)
		}
		commonDir = canonicalPath(resolvePath(canonDir, bLines[0]))
	}

	mainRoot := commonDir
	if filepath.Base(commonDir) == ".git" {
		mainRoot = filepath.Dir(commonDir)
	}
	repoName := filepath.Base(mainRoot)
	if repoName == "" || repoName == "." || repoName == "/" {
		repoName = filepath.Base(topLevel)
	}

	workDir := topLevel
	if workDir == "" {
		workDir = mainRoot
	}

	// Current branch (empty when detached). Works even before initial commit.
	currentBranch := ""
	if bOut, _, bCode, bErr := m.git(ctx, workDir, "symbolic-ref", "--quiet", "--short", "HEAD"); bErr == nil && bCode == 0 {
		currentBranch = strings.TrimSpace(string(bOut))
	}

	// Check whether HEAD resolves to a commit.
	hasCommits := false
	if _, _, hCode, hErr := m.git(ctx, workDir, "rev-parse", "--verify", "--quiet", "HEAD"); hErr == nil && hCode == 0 {
		hasCommits = true
	}

	defaultBase := m.discoverDefaultBase(ctx, mainRoot, currentBranch)

	return &RepoInfo{
		RepoKey:             commonDir,
		MainRoot:            mainRoot,
		RepoName:            repoName,
		CurrentWorktreePath: topLevel,
		CurrentBranch:       currentBranch,
		DefaultBase:         defaultBase,
		HasCommits:          hasCommits,
	}, nil
}

func (m *Manager) discoverDefaultBase(ctx context.Context, repoRoot, currentBranch string) string {
	// 1. origin/HEAD symbolic ref.
	if out, _, code, err := m.git(ctx, repoRoot, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && code == 0 {
		ref := strings.TrimSpace(string(out))
		if b, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok && b != "" {
			if m.refExists(ctx, repoRoot, "refs/heads/"+b) {
				return b
			}
			if m.refExists(ctx, repoRoot, "refs/remotes/origin/"+b) {
				return "origin/" + b
			}
			return b
		}
	}
	// 2. Local main or master.
	for _, cand := range []string{"main", "master"} {
		if m.refExists(ctx, repoRoot, "refs/heads/"+cand) {
			return cand
		}
	}
	// 3. Fallback to current branch.
	return currentBranch
}

func (m *Manager) refExists(ctx context.Context, repoRoot, ref string) bool {
	_, _, code, err := m.git(ctx, repoRoot, "show-ref", "--verify", "--quiet", ref)
	return err == nil && code == 0
}

// List returns the RepoInfo and all enriched Worktrees for the repository at dir.
func (m *Manager) List(ctx context.Context, dir string) (*RepoInfo, []Worktree, error) {
	repo, err := m.DiscoverRepo(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	out, stderr, code, err := m.git(ctx, repo.MainRoot, "worktree", "list", "--porcelain")
	if err != nil || code != 0 {
		return repo, nil, fmt.Errorf("worktree: list failed: %s", errDetail(stderr, err))
	}

	rawList := parseWorktreePorcelain(out)
	recByPath := map[string]Record{}
	if recs, rErr := m.store().ListByRepo(repo.RepoKey); rErr == nil {
		for _, rec := range recs {
			recByPath[canonicalPath(rec.Path)] = rec
		}
	}

	canonMain := canonicalPath(repo.MainRoot)
	result := make([]Worktree, 0, len(rawList))
	for _, wt := range rawList {
		wt.Path = canonicalPath(wt.Path)
		wt.RepoKey = repo.RepoKey
		wt.MainRepoRoot = repo.MainRoot
		wt.RepoName = repo.RepoName
		if !wt.IsBare && wt.Path == canonMain {
			wt.IsMain = true
		}

		rec, hasRec := recByPath[wt.Path]
		if hasRec {
			wt.Managed = rec.Managed
			wt.NameSource = rec.NameSource
			wt.CreatedAtMs = rec.CreatedAtMs
			if rec.Name != "" {
				wt.Name = rec.Name
			}
		}
		if wt.Name == "" {
			if wt.IsMain {
				wt.Name = repo.RepoName
			} else {
				wt.Name = filepath.Base(wt.Path)
			}
		}

		// Enrich dirty status, base branch, and unmerged commit count for accessible non-bare worktrees.
		if !wt.IsBare && !wt.IsPrunable {
			if st, statErr := os.Stat(wt.Path); statErr == nil && st.IsDir() {
				if stOut, _, stCode, stErr := m.git(ctx, wt.Path, "status", "--porcelain"); stErr == nil && stCode == 0 {
					wt.IsDirty = len(bytes.TrimSpace(stOut)) > 0
				}
				wt.BaseBranch = m.resolveWorktreeBase(ctx, repo, wt.Branch, rec.BaseBranch)
				if wt.BaseBranch != "" && wt.Branch != wt.BaseBranch {
					if cntOut, _, cntCode, cntErr := m.git(ctx, wt.Path, "rev-list", "--count", wt.BaseBranch+"..HEAD"); cntErr == nil && cntCode == 0 {
						if n, convErr := strconv.Atoi(strings.TrimSpace(string(cntOut))); convErr == nil && n > 0 {
							wt.UnmergedCommits = n
						}
					}
				}
			}
		} else {
			wt.BaseBranch = m.resolveWorktreeBase(ctx, repo, wt.Branch, rec.BaseBranch)
		}

		result = append(result, wt)
	}
	return repo, result, nil
}

func (m *Manager) resolveWorktreeBase(ctx context.Context, repo *RepoInfo, branch, recordBase string) string {
	if branch != "" {
		for _, key := range []string{"branch." + branch + ".spaces-base", "branch." + branch + ".workmux-base"} {
			if out, _, code, err := m.git(ctx, repo.MainRoot, "config", "--local", key); err == nil && code == 0 {
				if v := strings.TrimSpace(string(out)); v != "" {
					return v
				}
			}
		}
	}
	if strings.TrimSpace(recordBase) != "" {
		return strings.TrimSpace(recordBase)
	}
	return repo.DefaultBase
}

func parseWorktreePorcelain(data []byte) []Worktree {
	var out []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil && cur.Path != "" {
			out = append(out, *cur)
		}
		cur = nil
	}

	for _, rawLine := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.HeadSHA = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "bare":
			cur.IsBare = true
		case line == "detached":
			cur.IsDetached = true
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.IsLocked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			cur.IsPrunable = true
		}
	}
	flush()
	return out
}

// ListBranches lists all local branch names in the repository at dir.
func (m *Manager) ListBranches(ctx context.Context, dir string) ([]string, error) {
	out, stderr, code, err := m.git(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("worktree: list branches failed: %s", errDetail(stderr, err))
	}
	return splitLines(out), nil
}

// TakenNames builds a lowercase set of existing local branches, worktree names,
// worktree directory basenames, and directories inside the repo worktree bucket.
func (m *Manager) TakenNames(ctx context.Context, dir string) map[string]bool {
	taken := map[string]bool{}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			taken[v] = true
		}
	}

	repo, wts, err := m.List(ctx, dir)
	if err != nil || repo == nil {
		return taken
	}
	for _, wt := range wts {
		add(wt.Branch)
		if wt.Branch != "" {
			add(WorktreeDirName(wt.Branch))
		}
		add(wt.Name)
		add(filepath.Base(wt.Path))
	}
	if branches, bErr := m.ListBranches(ctx, repo.MainRoot); bErr == nil {
		for _, br := range branches {
			add(br)
			add(WorktreeDirName(br))
		}
	}
	if recs, rErr := m.store().ListByRepo(repo.RepoKey); rErr == nil {
		for _, rec := range recs {
			add(rec.Branch)
			add(rec.Name)
			add(filepath.Base(rec.Path))
		}
	}

	bucket := m.repoBucketDir(m.worktreeRoot(), repo)
	if entries, rErr := os.ReadDir(bucket); rErr == nil {
		for _, e := range entries {
			add(e.Name())
		}
	}
	return taken
}

// SuggestForDir discovers the repo at dir, fills missing RepoName/CurrentBranch
// on naming, gathers taken names, and computes a NamingResult.
func (m *Manager) SuggestForDir(ctx context.Context, dir string, naming NamingContext) (*RepoInfo, NamingResult) {
	var repo *RepoInfo
	var taken map[string]bool
	if strings.TrimSpace(dir) != "" {
		if r, err := m.DiscoverRepo(ctx, dir); err == nil && r != nil {
			repo = r
			if naming.RepoName == "" {
				naming.RepoName = r.RepoName
			}
			if naming.CurrentBranch == "" {
				naming.CurrentBranch = r.CurrentBranch
			}
			taken = m.TakenNames(ctx, dir)
		}
	}
	return repo, SuggestName(naming, taken)
}

// Create creates a new Git worktree and persists a managed Record in Store.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	repo, err := m.DiscoverRepo(ctx, req.RepoDir)
	if err != nil {
		return CreateResult{}, err
	}
	if !repo.HasCommits {
		return CreateResult{}, fmt.Errorf("worktree: repository %q has no commits yet; create an initial commit first", repo.RepoName)
	}

	_, existingWTs, err := m.List(ctx, repo.MainRoot)
	if err != nil {
		return CreateResult{}, err
	}

	root := strings.TrimSpace(req.WorktreeRoot)
	if root == "" {
		root = m.worktreeRoot()
	} else {
		root = filepath.Clean(root)
	}

	taken := m.TakenNames(ctx, repo.MainRoot)
	bucketDir := m.repoBucketDir(root, repo)
	if entries, rErr := os.ReadDir(bucketDir); rErr == nil {
		for _, e := range entries {
			taken[strings.ToLower(e.Name())] = true
		}
	}

	var namingRes NamingResult
	if explicitBranch := strings.TrimSpace(req.Branch); explicitBranch != "" {
		if err := ValidateBranchName(explicitBranch); err != nil {
			return CreateResult{}, fmt.Errorf("worktree: invalid branch name: %w", err)
		}
		namingRes = NamingResult{
			Branch:  explicitBranch,
			DirName: WorktreeDirName(explicitBranch),
			Source:  "custom",
			Rule:    "explicit",
		}
	} else {
		nctx := req.Naming
		if nctx.RepoName == "" {
			nctx.RepoName = repo.RepoName
		}
		if nctx.CurrentBranch == "" {
			nctx.CurrentBranch = repo.CurrentBranch
		}
		namingRes = SuggestName(nctx, taken)
		if err := ValidateBranchName(namingRes.Branch); err != nil {
			return CreateResult{}, fmt.Errorf("worktree: suggested branch invalid: %w", err)
		}
	}

	branch := namingRes.Branch

	// Refuse if branch is already checked out in an existing worktree.
	for _, wt := range existingWTs {
		if wt.Branch == branch {
			return CreateResult{}, fmt.Errorf("worktree: branch %q is already checked out at %s", branch, wt.Path)
		}
	}

	baseRef := strings.TrimSpace(req.BaseRef)
	if baseRef == "" {
		baseRef = repo.DefaultBase
	}
	if baseRef == "" {
		baseRef = "HEAD"
	}

	if err := os.MkdirAll(bucketDir, 0o755); err != nil {
		return CreateResult{}, fmt.Errorf("worktree: create bucket dir %s: %w", bucketDir, err)
	}

	dirName := namingRes.DirName
	if dirName == "" {
		dirName = WorktreeDirName(branch)
	}
	dstPath, finalDirName := uniqueDestinationPath(bucketDir, dirName)
	namingRes.DirName = finalDirName

	branchExists := m.refExists(ctx, repo.MainRoot, "refs/heads/"+branch)
	createdBranch := false
	var stderr []byte
	var code int
	if branchExists {
		_, stderr, code, err = m.git(ctx, repo.MainRoot, "worktree", "add", dstPath, branch)
	} else {
		createdBranch = true
		_, stderr, code, err = m.git(ctx, repo.MainRoot, "worktree", "add", "-b", branch, dstPath, baseRef)
	}
	if err != nil || code != 0 {
		return CreateResult{}, fmt.Errorf("worktree: git worktree add failed: %s", errDetail(stderr, err))
	}

	canonDst := canonicalPath(dstPath)
	headSHA := ""
	if hOut, _, hCode, hErr := m.git(ctx, canonDst, "rev-parse", "HEAD"); hErr == nil && hCode == 0 {
		headSHA = strings.TrimSpace(string(hOut))
	}

	nowMs := m.now().UnixMilli()
	rec := Record{
		Path:           canonDst,
		RepoKey:        repo.RepoKey,
		MainRepoRoot:   repo.MainRoot,
		RepoName:       repo.RepoName,
		Name:           finalDirName,
		Branch:         branch,
		BaseBranch:     baseRef,
		Managed:        true,
		NameSource:     namingRes.Source,
		CreatedAtMs:    nowMs,
		LastUsedAtMs:   nowMs,
		OriginSpace:    req.OriginSpace,
		OriginWindowID: req.OriginWindowID,
		OriginPaneID:   req.OriginPaneID,
	}
	if err := m.store().Upsert(rec); err != nil {
		if rbErr := m.rollbackCreate(ctx, repo.MainRoot, canonDst, branch, createdBranch); rbErr != nil {
			return CreateResult{}, fmt.Errorf("worktree: persist metadata: %w (rollback failed: %w)", err, rbErr)
		}
		return CreateResult{}, fmt.Errorf("worktree: persist metadata: %w", err)
	}

	// Record base branch in local git config (best-effort) after persistence succeeds.
	_, _, _, _ = m.git(ctx, repo.MainRoot, "config", "--local", "branch."+branch+".spaces-base", baseRef)

	wt := Worktree{
		Path:         canonDst,
		Name:         finalDirName,
		Branch:       branch,
		HeadSHA:      headSHA,
		BaseBranch:   baseRef,
		RepoKey:      repo.RepoKey,
		MainRepoRoot: repo.MainRoot,
		RepoName:     repo.RepoName,
		Managed:      true,
		NameSource:   namingRes.Source,
		CreatedAtMs:  nowMs,
	}

	return CreateResult{
		Worktree:      wt,
		CreatedBranch: createdBranch,
		Naming:        namingRes,
	}, nil
}

func (m *Manager) rollbackCreate(ctx context.Context, mainRoot, dstPath, branch string, deleteBranch bool) error {
	rollbackCtx := context.Background()
	if ctx != nil {
		rollbackCtx = context.WithoutCancel(ctx)
	}

	var errs []error
	if _, stderr, code, err := m.git(rollbackCtx, mainRoot, "worktree", "remove", "--force", dstPath); err != nil || code != 0 {
		errs = append(errs, fmt.Errorf("remove worktree %s: %s", dstPath, errDetail(stderr, err)))
		if _, pStderr, pCode, pErr := m.git(rollbackCtx, mainRoot, "worktree", "prune"); pErr != nil || pCode != 0 {
			errs = append(errs, fmt.Errorf("prune worktrees: %s", errDetail(pStderr, pErr)))
		}
	} else {
		m.cleanupEmptyParent(dstPath, mainRoot)
	}

	if deleteBranch && branch != "" {
		if _, stderr, code, err := m.git(rollbackCtx, mainRoot, "branch", "-D", branch); err != nil || code != 0 {
			errs = append(errs, fmt.Errorf("delete branch %q: %s", branch, errDetail(stderr, err)))
		}
	}

	return errors.Join(errs...)
}

func (m *Manager) repoBucketDir(root string, repo *RepoInfo) string {
	slug := WorktreeDirName(strings.ToLower(repo.RepoName))
	if slug == "" {
		slug = "repo"
	}
	candidate := filepath.Join(root, slug)
	if !m.bucketOwnedByOtherRepo(candidate, repo.RepoKey) {
		return candidate
	}
	sum := sha256.Sum256([]byte(repo.RepoKey))
	return filepath.Join(root, slug+"-"+hex.EncodeToString(sum[:3]))
}

func (m *Manager) bucketOwnedByOtherRepo(bucketDir, repoKey string) bool {
	canonBucket := canonicalPath(bucketDir)
	canonRepoKey := canonicalPath(repoKey)

	if recs, err := m.store().Load(); err == nil {
		for _, rec := range recs {
			if rec.RepoKey == "" || canonicalPath(rec.RepoKey) == canonRepoKey {
				continue
			}
			if canonicalPath(filepath.Dir(rec.Path)) == canonBucket {
				return true
			}
		}
	}

	entries, err := os.ReadDir(bucketDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(bucketDir, e.Name())
		out, _, code, gErr := m.git(context.Background(), sub, "rev-parse", "--git-common-dir")
		if gErr == nil && code == 0 {
			common := canonicalPath(resolvePath(sub, strings.TrimSpace(string(out))))
			if common != "" && common != canonRepoKey {
				return true
			}
		}
	}
	return false
}

func uniqueDestinationPath(bucketDir, baseName string) (string, string) {
	cand := filepath.Join(bucketDir, baseName)
	if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
		return cand, baseName
	}
	for i := 2; i <= 9999; i++ {
		name := fmt.Sprintf("%s-%d", baseName, i)
		cand = filepath.Join(bucketDir, name)
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand, name
		}
	}
	name := fmt.Sprintf("%s-%d", baseName, time.Now().Unix()%100000)
	return filepath.Join(bucketDir, name), name
}

// InspectSafety checks whether the worktree at wtPath can be safely removed.
func (m *Manager) InspectSafety(ctx context.Context, wtPath string) (RemoveSafety, error) {
	_, wt, err := m.lookupWorktree(ctx, wtPath)
	if err != nil {
		return RemoveSafety{}, err
	}

	safety := RemoveSafety{
		IsMain:          wt.IsMain,
		IsDirty:         wt.IsDirty,
		UnmergedCommits: wt.UnmergedCommits,
		Branch:          wt.Branch,
		BaseBranch:      wt.BaseBranch,
	}
	if wt.IsMain {
		safety.Warnings = append(safety.Warnings, "cannot remove main repository worktree")
	}
	if wt.IsDirty {
		safety.Warnings = append(safety.Warnings, "worktree has uncommitted changes")
	}
	if wt.UnmergedCommits > 0 {
		base := wt.BaseBranch
		if base == "" {
			base = "base branch"
		}
		safety.Warnings = append(safety.Warnings, fmt.Sprintf("branch has %d unmerged commit(s) relative to %s", wt.UnmergedCommits, base))
	}
	if wt.IsLocked {
		safety.Warnings = append(safety.Warnings, "worktree is locked")
	}
	safety.CanRemoveSafely = !wt.IsMain && !wt.IsDirty && wt.UnmergedCommits == 0 && !wt.IsLocked
	return safety, nil
}

// Remove deletes a linked worktree, optionally deletes its branch, removes
// its Store record, and removes an empty parent worktree bucket best-effort.
func (m *Manager) Remove(ctx context.Context, wtPath string, opts RemoveOptions) error {
	repo, wt, err := m.lookupWorktree(ctx, wtPath)
	if err != nil {
		return err
	}
	if wt.IsMain {
		return fmt.Errorf("worktree: refusing to remove main repository worktree %s", wt.Path)
	}

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, wt.Path)

	_, stderr, code, err := m.git(ctx, repo.MainRoot, args...)
	if err != nil || code != 0 {
		return fmt.Errorf("worktree: git worktree remove failed: %s", errDetail(stderr, err))
	}

	// Clean up store record and empty parent repo bucket directory best-effort.
	_ = m.store().DeleteByPath(wt.Path)
	m.cleanupEmptyParent(wt.Path, repo.MainRoot)

	if opts.DeleteBranch && wt.Branch != "" && wt.Branch != wt.BaseBranch && wt.Branch != repo.DefaultBase {
		flag := "-d"
		if opts.ForceBranch {
			flag = "-D"
		}
		_, bStderr, bCode, bErr := m.git(ctx, repo.MainRoot, "branch", flag, wt.Branch)
		if bErr != nil || bCode != 0 {
			return fmt.Errorf("worktree: removed worktree but failed to delete branch %q: %s", wt.Branch, errDetail(bStderr, bErr))
		}
	}
	return nil
}

func (m *Manager) cleanupEmptyParent(wtPath, mainRoot string) {
	parent := canonicalPath(filepath.Dir(wtPath))
	if parent == "" || parent == "/" || parent == "." {
		return
	}
	canonMain := canonicalPath(mainRoot)
	if parent == canonMain || parent == canonicalPath(filepath.Dir(canonMain)) {
		return
	}
	// os.Remove only succeeds if parent is an empty directory.
	_ = os.Remove(parent)
}

func (m *Manager) lookupWorktree(ctx context.Context, wtPath string) (*RepoInfo, Worktree, error) {
	wtPath = strings.TrimSpace(wtPath)
	if wtPath == "" {
		return nil, Worktree{}, errors.New("worktree: path is empty")
	}
	canonTarget := canonicalPath(wtPath)

	discoverDir := ""
	if st, err := os.Stat(canonTarget); err == nil && st.IsDir() {
		discoverDir = canonTarget
	} else if rec, ok, rErr := m.store().GetByPath(canonTarget); rErr == nil && ok && rec.MainRepoRoot != "" {
		discoverDir = rec.MainRepoRoot
	} else {
		return nil, Worktree{}, fmt.Errorf("worktree: path %s does not exist and is not tracked", wtPath)
	}

	repo, wts, err := m.List(ctx, discoverDir)
	if err != nil {
		return nil, Worktree{}, err
	}
	for _, wt := range wts {
		if canonicalPath(wt.Path) == canonTarget {
			return repo, wt, nil
		}
	}
	return repo, Worktree{}, fmt.Errorf("worktree: %s is not a registered worktree of %s", wtPath, repo.MainRoot)
}

// Prune runs `git worktree prune` per unique repo, prunes missing Store
// records, and optionally removes orphaned, clean, merged managed worktrees.
func (m *Manager) Prune(ctx context.Context, repoDirs []string, removeOrphanedMerged bool, activePaths map[string]bool) (PruneResult, error) {
	var res PruneResult

	canonActiveSet := make(map[string]bool, len(activePaths))
	canonActiveList := make([]string, 0, len(activePaths))
	for p, active := range activePaths {
		if !active {
			continue
		}
		if cp := canonicalPath(p); cp != "" && !canonActiveSet[cp] {
			canonActiveSet[cp] = true
			canonActiveList = append(canonActiveList, cp)
		}
	}
	isActiveWorktree := func(wtPath string) bool {
		canonWT := canonicalPath(wtPath)
		if canonWT == "" {
			return false
		}
		if canonActiveSet[canonWT] {
			return true
		}
		for _, ap := range canonActiveList {
			if pathWithin(ap, canonWT) {
				return true
			}
		}
		return false
	}

	reposByKey := map[string]*RepoInfo{}
	var repoOrder []*RepoInfo
	addRepo := func(dir string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		repo, err := m.DiscoverRepo(ctx, dir)
		if err != nil || repo == nil {
			return
		}
		if _, exists := reposByKey[repo.RepoKey]; exists {
			return
		}
		reposByKey[repo.RepoKey] = repo
		repoOrder = append(repoOrder, repo)
	}

	for _, dir := range repoDirs {
		addRepo(dir)
	}
	if len(repoOrder) == 0 {
		if recs, err := m.store().Load(); err == nil {
			for _, rec := range recs {
				addRepo(rec.MainRepoRoot)
			}
		}
	}

	// 1. Run `git worktree prune -v` per unique repository.
	for _, repo := range repoOrder {
		out, stderr, code, err := m.git(ctx, repo.MainRoot, "worktree", "prune", "-v")
		if err == nil && code == 0 {
			combined := string(out) + "\n" + string(stderr)
			for _, line := range strings.Split(combined, "\n") {
				t := strings.TrimSpace(line)
				if strings.HasPrefix(t, "Removing ") {
					res.PrunedGit++
				}
			}
		}
	}

	// 2. Prune Store records whose paths no longer exist on disk.
	prunedRecs, err := m.store().PruneMissing()
	if err != nil {
		return res, err
	}
	res.PrunedRecords = prunedRecs

	// 3. Optionally remove orphaned, clean, merged managed worktrees.
	if removeOrphanedMerged {
		for _, repo := range repoOrder {
			_, wts, listErr := m.List(ctx, repo.MainRoot)
			if listErr != nil {
				continue
			}
			for _, wt := range wts {
				if !wt.Managed || wt.IsMain || wt.IsBare || wt.IsLocked || wt.IsPrunable {
					continue
				}
				if isActiveWorktree(wt.Path) {
					continue
				}
				if wt.IsDirty || wt.UnmergedCommits > 0 {
					continue
				}
				if wt.BaseBranch == "" {
					continue
				}
				rmErr := m.Remove(ctx, wt.Path, RemoveOptions{
					Force:        false,
					DeleteBranch: true,
					ForceBranch:  false,
				})
				if rmErr == nil {
					res.RemovedPaths = append(res.RemovedPaths, wt.Path)
				}
			}
		}
	}

	return res, nil
}

// ResolveWorktreePath resolves a worktree path from an exact path, worktree
// name, or branch name using List and Store.
func (m *Manager) ResolveWorktreePath(ctx context.Context, repoDir, nameOrPath string) (string, error) {
	query := strings.TrimSpace(nameOrPath)
	if query == "" {
		return "", errors.New("worktree: name or path is empty")
	}
	canonQuery := canonicalPath(query)
	lowQuery := strings.ToLower(query)

	var repo *RepoInfo
	if strings.TrimSpace(repoDir) != "" {
		if r, wts, err := m.List(ctx, repoDir); err == nil {
			repo = r
			// Exact path match
			for _, wt := range wts {
				if canonicalPath(wt.Path) == canonQuery {
					return wt.Path, nil
				}
			}
			// Exact name or branch match
			for _, wt := range wts {
				if wt.Name == query || wt.Branch == query || filepath.Base(wt.Path) == query {
					return wt.Path, nil
				}
			}
			// Case-insensitive name or branch match
			for _, wt := range wts {
				if strings.ToLower(wt.Name) == lowQuery || strings.ToLower(wt.Branch) == lowQuery || strings.ToLower(filepath.Base(wt.Path)) == lowQuery {
					return wt.Path, nil
				}
			}
		}
	}

	// Fallback to Store records.
	var recs []Record
	var err error
	if repo != nil {
		recs, err = m.store().ListByRepo(repo.RepoKey)
	} else {
		recs, err = m.store().Load()
	}
	if err == nil {
		for _, rec := range recs {
			if canonicalPath(rec.Path) == canonQuery {
				return rec.Path, nil
			}
		}
		for _, rec := range recs {
			if rec.Name == query || rec.Branch == query || filepath.Base(rec.Path) == query {
				return rec.Path, nil
			}
		}
		for _, rec := range recs {
			if strings.ToLower(rec.Name) == lowQuery || strings.ToLower(rec.Branch) == lowQuery || strings.ToLower(filepath.Base(rec.Path)) == lowQuery {
				return rec.Path, nil
			}
		}
	}

	// Direct existing directory that is itself a worktree.
	if st, statErr := os.Stat(canonQuery); statErr == nil && st.IsDir() {
		if r, dErr := m.DiscoverRepo(ctx, canonQuery); dErr == nil && r != nil && r.CurrentWorktreePath != "" {
			if canonicalPath(r.CurrentWorktreePath) == canonQuery {
				return canonQuery, nil
			}
		}
	}

	return "", fmt.Errorf("worktree: could not resolve %q", query)
}

func resolvePath(base, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// pathWithin reports whether candidate equals root or is strictly inside root.
// Both arguments must be cleaned canonical paths.
func pathWithin(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	if candidate == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(candidate, prefix)
}

func splitLines(data []byte) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func errDetail(stderr []byte, err error) string {
	msg := strings.TrimSpace(string(stderr))
	if msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

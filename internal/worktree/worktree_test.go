package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCuratedWordListsUnique(t *testing.T) {
	if len(namingAdjectives) != 64 {
		t.Fatalf("expected 64 adjectives, got %d", len(namingAdjectives))
	}
	if len(namingNouns) != 64 {
		t.Fatalf("expected 64 nouns, got %d", len(namingNouns))
	}
	adjSeen := map[string]bool{}
	for _, a := range namingAdjectives {
		if a == "" || adjSeen[a] {
			t.Fatalf("duplicate or empty adjective: %q", a)
		}
		adjSeen[a] = true
	}
	nounSeen := map[string]bool{}
	for _, n := range namingNouns {
		if n == "" || nounSeen[n] {
			t.Fatalf("duplicate or empty noun: %q", n)
		}
		nounSeen[n] = true
	}
}

func TestSanitizeAndValidateBranchName(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"Feature / OAuth Login!", "feature/oauth-login"},
		{"  Fix..Double--Dots  ", "fix-double-dots"},
		{"branch.lock", "branch"},
		{"feat/sub.lock/item..bar@{1}", "feat/sub/item-bar-1"},
		{"...leading-and-trailing---", "leading-and-trailing"},
		{"///a///b///", "a/b"},
		{"@", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		got := SanitizeBranchName(tc.raw)
		if got != tc.want {
			t.Errorf("SanitizeBranchName(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if got != "" {
			if err := ValidateBranchName(got); err != nil {
				t.Errorf("ValidateBranchName(%q) unexpectedly failed: %v", got, err)
			}
		}
	}

	invalid := []string{
		"",
		"@",
		"-leading-dash",
		".leading-dot",
		"/leading-slash",
		"trailing-dot.",
		"trailing-slash/",
		"foo.lock",
		"foo/bar.lock",
		"foo/.hidden",
		"a..b",
		"a@{b}",
		"a//b",
		"has space",
		"has~tilde",
		"has^caret",
		"has:colon",
		"has?q",
		"has*star",
		"has[bracket",
		`has\slash`,
		"ctrl\x01char",
	}
	for _, br := range invalid {
		if err := ValidateBranchName(br); err == nil {
			t.Errorf("ValidateBranchName(%q) = nil, want error", br)
		}
	}

	if got := WorktreeDirName("feat/oauth/login"); got != "feat-oauth-login" {
		t.Errorf("WorktreeDirName = %q, want feat-oauth-login", got)
	}
	longName := strings.Repeat("a", 100)
	if got := WorktreeDirName(longName); len(got) > maxDirNameLength {
		t.Errorf("WorktreeDirName length = %d, want <= %d", len(got), maxDirNameLength)
	}
}

func TestSuggestNameAllTiers(t *testing.T) {
	t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")

	// Tier 1: Explicit name (never auto-suffixed or prefixed)
	t.Run("explicit", func(t *testing.T) {
		res := SuggestName(NamingContext{
			ExplicitName: "Feat / Billing Sync",
			TargetName:   "other",
			Prefix:       "dev/",
		}, map[string]bool{"feat/billing-sync": true})
		if res.Branch != "feat/billing-sync" || res.DirName != "feat-billing-sync" || res.Rule != "explicit" || res.Source != "custom" {
			t.Fatalf("unexpected Tier 1 result: %+v", res)
		}
	})

	// Tier 2: TargetName (tab & space), skipping generics and RepoName, stop-word filtering >4 words, collision avoidance
	t.Run("target_name", func(t *testing.T) {
		res := SuggestName(NamingContext{
			TargetName: "Fix the OAuth token refresh for API",
			TargetKind: "tab",
			RepoName:   "model-router",
		}, nil)
		if res.Rule != "target_name" || res.Source != "from tab name" {
			t.Fatalf("unexpected Tier 2 meta: %+v", res)
		}
		if res.Branch != "fix-oauth-token-refresh-api" {
			t.Fatalf("unexpected Tier 2 branch: %q", res.Branch)
		}

		// Collision disambiguation + prefix
		res2 := SuggestName(NamingContext{
			TargetName: "payments",
			TargetKind: "space",
			Prefix:     "wt/",
		}, map[string]bool{"wt/payments": true, "wt/payments-2": true})
		if res2.Branch != "wt/payments-3" || res2.Source != "from space name" || res2.Rule != "target_name" {
			t.Fatalf("unexpected Tier 2 collision result: %+v", res2)
		}

		// Env prefix fallback
		t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "dev-")
		resEnv := SuggestName(NamingContext{
			TargetName: "payments",
		}, nil)
		if resEnv.Branch != "dev-payments" || resEnv.Source != "from name" {
			t.Fatalf("unexpected Tier 2 env prefix result: %+v", resEnv)
		}
		t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")

		// Generic target names & repo name must be skipped
		for _, generic := range []string{"zsh", "bash", "sh", "fish", "shell", "terminal", "space", "tab", "model-router"} {
			r := SuggestName(NamingContext{
				TargetName:   generic,
				RepoName:     "model-router",
				HarnessTitle: "✳ Fix cache invalidation",
				HarnessApp:   "claude-code",
			}, nil)
			if r.Rule != "harness_title" {
				t.Errorf("TargetName %q should have been skipped, got %+v", generic, r)
			}
		}
	})

	// Tier 3: HarnessTitle with spinner/prefix/cwd cleanup, version banner & current branch skip
	t.Run("harness_title", func(t *testing.T) {
		res := SuggestName(NamingContext{
			HarnessApp:   "claude-code",
			HarnessTitle: "✳ Claude Code - Refactor the SQLite cache layer | ~/dev/model-router",
		}, nil)
		if res.Rule != "harness_title" || res.Source != "from claude title" {
			t.Fatalf("unexpected Tier 3 meta: %+v", res)
		}
		if res.Branch != "refactor-sqlite-cache-layer" {
			t.Fatalf("unexpected Tier 3 branch: %q", res.Branch)
		}

		resOC := SuggestName(NamingContext{
			HarnessApp:   "opencode",
			HarnessTitle: "⠋ OC | Add webhook retries | /tmp/repo",
		}, nil)
		if resOC.Rule != "harness_title" || resOC.Source != "from opencode title" || resOC.Branch != "add-webhook-retries" {
			t.Fatalf("unexpected OC Tier 3 result: %+v", resOC)
		}

		resEmptyApp := SuggestName(NamingContext{
			HarnessTitle: "● Build metrics dashboard",
		}, nil)
		if resEmptyApp.Rule != "harness_title" || resEmptyApp.Source != "from harness title" || resEmptyApp.Branch != "build-metrics-dashboard" {
			t.Fatalf("unexpected empty-app Tier 3 result: %+v", resEmptyApp)
		}

		// Version banners, paths, and currentBranch matches must skip to Tier 4
		for _, skipTitle := range []string{"Claude Code - v1.2.3", "/Users/dev/project", "zsh", "✳ Fix login bug"} {
			r := SuggestName(NamingContext{
				HarnessApp:    "antigravity",
				HarnessTitle:  skipTitle,
				CurrentBranch: "fix-login-bug",
				HarnessPrompt: "Implement streaming SSE endpoint for events",
			}, nil)
			if r.Rule != "harness_prompt" {
				t.Errorf("HarnessTitle %q should have fallen through to prompt, got %+v", skipTitle, r)
			}
		}
	})

	// Tier 4: HarnessPrompt with URL/markdown stripping
	t.Run("harness_prompt", func(t *testing.T) {
		res := SuggestName(NamingContext{
			HarnessApp:    "antigravity",
			HarnessPrompt: "\nPlease help fix the `jwt` refresh bug in https://example.com/issues/123.\nSecond line ignored.",
		}, nil)
		if res.Rule != "harness_prompt" || res.Source != "from agy prompt" {
			t.Fatalf("unexpected Tier 4 meta: %+v", res)
		}
		if res.Branch != "fix-jwt-refresh-bug" {
			t.Fatalf("unexpected Tier 4 branch: %q", res.Branch)
		}
	})

	// Tier 5: HarnessApp fallback + Seed regeneration skipping Title/Prompt
	t.Run("harness_agent_and_seed_regen", func(t *testing.T) {
		ctx := NamingContext{
			HarnessApp:    "cursor-agent",
			HarnessTitle:  "✳ Add billing page",
			HarnessPrompt: "Build billing checkout flow",
			Seed:          1,
		}
		r1 := SuggestName(ctx, nil)
		if r1.Rule != "harness_agent" || r1.Source != "from cursor" || !strings.HasPrefix(r1.Branch, "cursor-") {
			t.Fatalf("unexpected Tier 5 result for Seed=1: %+v", r1)
		}

		ctx.Seed = 2
		r2 := SuggestName(ctx, nil)
		if r2.Rule != "harness_agent" || r2.Branch == r1.Branch {
			t.Fatalf("Seed=2 should regenerate a distinct name, got r1=%q r2=%q", r1.Branch, r2.Branch)
		}

		// Collision avoidance on same seed
		ctx.Seed = 1
		r1Col := SuggestName(ctx, map[string]bool{r1.Branch: true})
		if r1Col.Branch == r1.Branch || !strings.HasPrefix(r1Col.Branch, "cursor-") {
			t.Fatalf("collision avoidance failed: got %+v (taken %q)", r1Col, r1.Branch)
		}
	})

	// Tier 6: Random fallback (ignores shell apps) + prefix + collision avoidance
	t.Run("fallback", func(t *testing.T) {
		r1 := SuggestName(NamingContext{
			HarnessApp: "zsh",
			Prefix:     "feat/",
			Seed:       7,
		}, nil)
		if r1.Rule != "fallback" || r1.Source != "auto" || !strings.HasPrefix(r1.Branch, "feat/") {
			t.Fatalf("unexpected Tier 6 result: %+v", r1)
		}
		if err := ValidateBranchName(r1.Branch); err != nil {
			t.Fatalf("invalid fallback branch %q: %v", r1.Branch, err)
		}

		r2 := SuggestName(NamingContext{
			HarnessApp: "zsh",
			Prefix:     "feat/",
			Seed:       7,
		}, map[string]bool{r1.Branch: true})
		if r2.Branch == r1.Branch {
			t.Fatalf("expected collision avoidance to pick a different pair, both %q", r2.Branch)
		}
	})
}

func TestStorePersistenceAndPruneMissing(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state", "worktrees.json")
	now := time.Unix(1700000000, 0)
	store := &Store{
		Path: stateFile,
		Now:  func() time.Time { return now },
	}

	// Empty load returns nil, nil
	recs, err := store.Load()
	if err != nil || len(recs) != 0 {
		t.Fatalf("initial Load = (%v, %v), want empty", recs, err)
	}

	wt1 := filepath.Join(tmp, "wt1")
	wt2 := filepath.Join(tmp, "wt2")
	if err := os.MkdirAll(wt1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt2, 0o755); err != nil {
		t.Fatal(err)
	}

	repoKey := filepath.Join(tmp, "repo", ".git")
	if err := store.Upsert(Record{
		Path:         wt1,
		RepoKey:      repoKey,
		MainRepoRoot: filepath.Join(tmp, "repo"),
		RepoName:     "repo",
		Name:         "feat-a",
		Branch:       "feat-a",
		BaseBranch:   "main",
		Managed:      true,
		NameSource:   "custom",
	}); err != nil {
		t.Fatalf("Upsert wt1: %v", err)
	}
	if err := store.Upsert(Record{
		Path:         wt2,
		RepoKey:      repoKey,
		MainRepoRoot: filepath.Join(tmp, "repo"),
		RepoName:     "repo",
		Name:         "feat-b",
		Branch:       "feat-b",
		BaseBranch:   "main",
		Managed:      true,
	}); err != nil {
		t.Fatalf("Upsert wt2: %v", err)
	}

	// Verify file permissions are 0600
	fi, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file perm = %o, want 0600", perm)
	}

	// GetByPath via symlink verifies canonical matching
	linkPath := filepath.Join(tmp, "wt1-link")
	if err := os.Symlink(wt1, linkPath); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetByPath(linkPath)
	if err != nil || !ok || got.Branch != "feat-a" || got.CreatedAtMs != now.UnixMilli() {
		t.Fatalf("GetByPath via symlink = (%+v, %v, %v)", got, ok, err)
	}

	// Upsert preserves CreatedAtMs
	later := now.Add(time.Hour)
	store.Now = func() time.Time { return later }
	if err := store.Upsert(Record{
		Path:       linkPath,
		RepoKey:    repoKey,
		Name:       "feat-a",
		Branch:     "feat-a-updated",
		Managed:    true,
		NameSource: "custom",
	}); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got2, ok, err := store.GetByPath(wt1)
	if err != nil || !ok || got2.Branch != "feat-a-updated" || got2.CreatedAtMs != now.UnixMilli() {
		t.Fatalf("Upsert did not preserve CreatedAtMs: %+v", got2)
	}

	// ListByRepo
	byRepo, err := store.ListByRepo(repoKey)
	if err != nil || len(byRepo) != 2 {
		t.Fatalf("ListByRepo = %d records, err=%v", len(byRepo), err)
	}

	// PruneMissing removes deleted directory only
	if err := os.RemoveAll(wt2); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneMissing()
	if err != nil || pruned != 1 {
		t.Fatalf("PruneMissing = (%d, %v), want (1, nil)", pruned, err)
	}
	if _, ok, _ := store.GetByPath(wt2); ok {
		t.Fatalf("wt2 should have been pruned")
	}

	// DeleteByPath
	if err := store.DeleteByPath(wt1); err != nil {
		t.Fatalf("DeleteByPath: %v", err)
	}
	remaining, err := store.Load()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("expected 0 remaining records, got %d (err=%v)", len(remaining), err)
	}
}

func TestStoreConcurrentMultiInstanceUpdates(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state", "worktrees.json")
	repoKey := filepath.Join(tmp, "repo", ".git")

	const (
		workers = 12
		perWork = 10
	)

	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Distinct Store instance per worker so intra-process s.mu is not shared;
			// correctness relies on the file lock serializing open file descriptions.
			store := &Store{Path: stateFile}
			<-start

			ephemeral := filepath.Join(tmp, fmt.Sprintf("w%02d-ephemeral", workerID))
			if err := store.Upsert(Record{
				Path:     ephemeral,
				RepoKey:  repoKey,
				RepoName: "repo",
				Name:     fmt.Sprintf("w%02d-ephemeral", workerID),
				Branch:   fmt.Sprintf("w%02d-ephemeral", workerID),
				Managed:  true,
			}); err != nil {
				errCh <- fmt.Errorf("worker %d upsert ephemeral: %w", workerID, err)
				return
			}

			for i := 0; i < perWork; i++ {
				wtPath := filepath.Join(tmp, fmt.Sprintf("w%02d-wt%02d", workerID, i))
				branch := fmt.Sprintf("feat/w%02d-%02d", workerID, i)
				if err := store.Upsert(Record{
					Path:     wtPath,
					RepoKey:  repoKey,
					RepoName: "repo",
					Name:     fmt.Sprintf("w%02d-wt%02d", workerID, i),
					Branch:   branch,
					Managed:  true,
				}); err != nil {
					errCh <- fmt.Errorf("worker %d upsert %d: %w", workerID, i, err)
					return
				}
				if i%3 == 0 {
					if _, err := store.Load(); err != nil {
						errCh <- fmt.Errorf("worker %d load: %w", workerID, err)
						return
					}
					if rec, ok, err := store.GetByPath(wtPath); err != nil || !ok || rec.Branch != branch {
						errCh <- fmt.Errorf("worker %d get %s: rec=%+v ok=%v err=%v", workerID, wtPath, rec, ok, err)
						return
					}
				}
			}

			// Update record 0 in place and delete the ephemeral record.
			firstPath := filepath.Join(tmp, fmt.Sprintf("w%02d-wt00", workerID))
			if err := store.Upsert(Record{
				Path:     firstPath,
				RepoKey:  repoKey,
				RepoName: "repo",
				Name:     fmt.Sprintf("w%02d-wt00", workerID),
				Branch:   fmt.Sprintf("feat/w%02d-00-updated", workerID),
				Managed:  true,
			}); err != nil {
				errCh <- fmt.Errorf("worker %d update first: %w", workerID, err)
				return
			}
			if err := store.DeleteByPath(ephemeral); err != nil {
				errCh <- fmt.Errorf("worker %d delete ephemeral: %w", workerID, err)
				return
			}
			if _, err := store.ListByRepo(repoKey); err != nil {
				errCh <- fmt.Errorf("worker %d list by repo: %w", workerID, err)
				return
			}
		}(w)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	store := &Store{Path: stateFile}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	wantCount := workers * perWork
	if len(recs) != wantCount {
		t.Fatalf("got %d records after concurrent multi-instance updates, want %d", len(recs), wantCount)
	}

	byPath := make(map[string]Record, len(recs))
	for _, r := range recs {
		byPath[r.Path] = r
	}
	for w := 0; w < workers; w++ {
		ephemeral := canonicalPath(filepath.Join(tmp, fmt.Sprintf("w%02d-ephemeral", w)))
		if _, exists := byPath[ephemeral]; exists {
			t.Errorf("ephemeral record %s should have been deleted", ephemeral)
		}
		for i := 0; i < perWork; i++ {
			wtPath := canonicalPath(filepath.Join(tmp, fmt.Sprintf("w%02d-wt%02d", w, i)))
			rec, ok := byPath[wtPath]
			if !ok {
				t.Errorf("missing record for %s (lost concurrent update)", wtPath)
				continue
			}
			wantBranch := fmt.Sprintf("feat/w%02d-%02d", w, i)
			if i == 0 {
				wantBranch = fmt.Sprintf("feat/w%02d-00-updated", w)
			}
			if rec.Branch != wantBranch {
				t.Errorf("record %s branch = %q, want %q", wtPath, rec.Branch, wantBranch)
			}
		}
	}

	// Verify lock file permissions are 0600.
	fi, err := os.Stat(stateFile + ".lock")
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("lock file perm = %o, want 0600", perm)
	}
}

func TestStoreSubprocessWorker(t *testing.T) {
	if os.Getenv("TMUX_SPACES_STORE_LOCK_HELPER") != "1" {
		return
	}
	stateFile := os.Getenv("TMUX_SPACES_STORE_LOCK_PATH")
	rootDir := os.Getenv("TMUX_SPACES_STORE_LOCK_ROOT")
	workerID, err := strconv.Atoi(os.Getenv("TMUX_SPACES_STORE_LOCK_WORKER"))
	if err != nil {
		t.Fatalf("parse worker id: %v", err)
	}
	count, err := strconv.Atoi(os.Getenv("TMUX_SPACES_STORE_LOCK_COUNT"))
	if err != nil {
		t.Fatalf("parse count: %v", err)
	}

	store := &Store{Path: stateFile}
	repoKey := filepath.Join(rootDir, "repo", ".git")
	tempPath := filepath.Join(rootDir, fmt.Sprintf("proc%02d-temp", workerID))
	if err := store.Upsert(Record{
		Path:     tempPath,
		RepoKey:  repoKey,
		RepoName: "repo",
		Name:     fmt.Sprintf("proc%02d-temp", workerID),
		Branch:   fmt.Sprintf("proc%02d-temp", workerID),
		Managed:  true,
	}); err != nil {
		t.Fatalf("upsert temp: %v", err)
	}

	for i := 0; i < count; i++ {
		wtPath := filepath.Join(rootDir, fmt.Sprintf("proc%02d-wt%02d", workerID, i))
		if err := store.Upsert(Record{
			Path:     wtPath,
			RepoKey:  repoKey,
			RepoName: "repo",
			Name:     fmt.Sprintf("proc%02d-wt%02d", workerID, i),
			Branch:   fmt.Sprintf("proc/%02d/%02d", workerID, i),
			Managed:  true,
		}); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		if i%4 == 0 {
			if _, err := store.Load(); err != nil {
				t.Fatalf("load %d: %v", i, err)
			}
		}
	}

	if err := store.DeleteByPath(tempPath); err != nil {
		t.Fatalf("delete temp: %v", err)
	}
}

func TestStoreConcurrentSubprocessUpdates(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "nested", "worktrees.json")

	const (
		procs   = 6
		perProc = 10
	)

	cmds := make([]*exec.Cmd, procs)
	outputs := make([]strings.Builder, procs)

	for p := 0; p < procs; p++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStoreSubprocessWorker$")
		cmd.Env = append(os.Environ(),
			"TMUX_SPACES_STORE_LOCK_HELPER=1",
			"TMUX_SPACES_STORE_LOCK_PATH="+stateFile,
			"TMUX_SPACES_STORE_LOCK_ROOT="+tmp,
			fmt.Sprintf("TMUX_SPACES_STORE_LOCK_WORKER=%d", p),
			fmt.Sprintf("TMUX_SPACES_STORE_LOCK_COUNT=%d", perProc),
		)
		cmd.Stdout = &outputs[p]
		cmd.Stderr = &outputs[p]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start subprocess %d: %v", p, err)
		}
		cmds[p] = cmd
	}

	for p, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("subprocess %d failed: %v\n%s", p, err, outputs[p].String())
		}
	}

	store := &Store{Path: stateFile}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	wantCount := procs * perProc
	if len(recs) != wantCount {
		t.Fatalf("got %d records after concurrent subprocess updates, want %d", len(recs), wantCount)
	}

	byPath := make(map[string]Record, len(recs))
	for _, r := range recs {
		byPath[r.Path] = r
	}
	for p := 0; p < procs; p++ {
		tempPath := canonicalPath(filepath.Join(tmp, fmt.Sprintf("proc%02d-temp", p)))
		if _, exists := byPath[tempPath]; exists {
			t.Errorf("temp record %s should have been deleted", tempPath)
		}
		for i := 0; i < perProc; i++ {
			wtPath := canonicalPath(filepath.Join(tmp, fmt.Sprintf("proc%02d-wt%02d", p, i)))
			rec, ok := byPath[wtPath]
			if !ok {
				t.Errorf("missing record %s (lost cross-process update)", wtPath)
				continue
			}
			wantBranch := fmt.Sprintf("proc/%02d/%02d", p, i)
			if rec.Branch != wantBranch {
				t.Errorf("record %s branch = %q, want %q", wtPath, rec.Branch, wantBranch)
			}
		}
	}
}

// --- End-to-end real Git repository tests ---

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func initTestRepo(t *testing.T, dir string, withCommit bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if withCommit {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "README.md")
		runGit(t, dir, "commit", "-m", "initial commit")
	}
	return canonicalPath(dir)
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	wtRoot := filepath.Join(base, "worktrees")
	statePath := filepath.Join(base, "worktrees.json")
	mgr := &Manager{
		Store:        &Store{Path: statePath, Now: time.Now},
		WorktreeRoot: wtRoot,
		Runner:       defaultRunCommand,
		Now:          time.Now,
	}
	return mgr, wtRoot
}

func TestManagerEndToEndCreateDiscoverListInspectRemove(t *testing.T) {
	t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")
	ctx := context.Background()
	mgr, _ := newTestManager(t)

	// 1. Unborn repo error
	unbornDir := initTestRepo(t, filepath.Join(t.TempDir(), "unborn-repo"), false)
	unbornInfo, err := mgr.DiscoverRepo(ctx, unbornDir)
	if err != nil {
		t.Fatalf("DiscoverRepo unborn: %v", err)
	}
	if unbornInfo.HasCommits {
		t.Fatalf("expected HasCommits=false for unborn repo")
	}
	if _, err := mgr.Create(ctx, CreateRequest{RepoDir: unbornDir, Branch: "feat-x"}); err == nil {
		t.Fatalf("expected error creating worktree on unborn repo")
	}

	// 2. Normal repo with initial commit
	repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "demo-app"), true)

	info, err := mgr.DiscoverRepo(ctx, repoDir)
	if err != nil {
		t.Fatalf("DiscoverRepo main: %v", err)
	}
	if !info.HasCommits || info.MainRoot != repoDir || info.RepoName != "demo-app" || info.CurrentBranch != "main" || info.DefaultBase != "main" {
		t.Fatalf("unexpected RepoInfo: %+v", info)
	}

	// 3. Create auto-named worktree from HarnessTitle
	resAuto, err := mgr.Create(ctx, CreateRequest{
		RepoDir: repoDir,
		Naming: NamingContext{
			HarnessApp:   "claude-code",
			HarnessTitle: "✳ Implement rate limiter",
		},
		OriginSpace: "dev",
	})
	if err != nil {
		t.Fatalf("Create auto: %v", err)
	}
	if !resAuto.CreatedBranch || resAuto.Worktree.Branch != "implement-rate-limiter" || !resAuto.Worktree.Managed {
		t.Fatalf("unexpected Create auto result: %+v", resAuto)
	}
	if resAuto.Naming.Rule != "harness_title" || resAuto.Naming.Source != "from claude title" {
		t.Fatalf("unexpected Create auto naming: %+v", resAuto.Naming)
	}

	// 4. DiscoverRepo from INSIDE linked worktree returns same RepoKey & MainRoot!
	subInfo, err := mgr.DiscoverRepo(ctx, resAuto.Worktree.Path)
	if err != nil {
		t.Fatalf("DiscoverRepo linked: %v", err)
	}
	if subInfo.RepoKey != info.RepoKey || subInfo.MainRoot != repoDir || subInfo.CurrentWorktreePath != resAuto.Worktree.Path || subInfo.CurrentBranch != "implement-rate-limiter" {
		t.Fatalf("unexpected linked RepoInfo: %+v", subInfo)
	}

	// 5. Nested creation from inside a linked worktree + explicit branch name
	resNested, err := mgr.Create(ctx, CreateRequest{
		RepoDir: resAuto.Worktree.Path,
		Branch:  "feat/nested-ui",
	})
	if err != nil {
		t.Fatalf("Create nested: %v", err)
	}
	if !resNested.CreatedBranch || resNested.Worktree.Branch != "feat/nested-ui" || resNested.Worktree.Name != "feat-nested-ui" {
		t.Fatalf("unexpected nested Create result: %+v", resNested)
	}

	// 6. Duplicate checked-out branch returns clear error mentioning existing worktree path
	if _, err := mgr.Create(ctx, CreateRequest{
		RepoDir: repoDir,
		Branch:  "feat/nested-ui",
	}); err == nil || !strings.Contains(err.Error(), resNested.Worktree.Path) {
		t.Fatalf("expected duplicate branch error mentioning %s, got: %v", resNested.Worktree.Path, err)
	}

	// 7. Existing unattached local branch -> CreatedBranch == false
	runGit(t, repoDir, "branch", "existing-local")
	resExisting, err := mgr.Create(ctx, CreateRequest{
		RepoDir: repoDir,
		Branch:  "existing-local",
	})
	if err != nil {
		t.Fatalf("Create existing branch: %v", err)
	}
	if resExisting.CreatedBranch {
		t.Fatalf("expected CreatedBranch=false for pre-existing local branch")
	}

	// 8. Detached worktree + dirty worktree + unmerged commit detection in List & InspectSafety
	detachedPath := filepath.Join(t.TempDir(), "detached-wt")
	runGit(t, repoDir, "worktree", "add", "--detach", detachedPath, "HEAD")

	// Make resAuto dirty
	if err := os.WriteFile(filepath.Join(resAuto.Worktree.Path, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make an unmerged commit on resNested
	if err := os.WriteFile(filepath.Join(resNested.Worktree.Path, "feature.txt"), []byte("new feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resNested.Worktree.Path, "add", "feature.txt")
	runGit(t, resNested.Worktree.Path, "commit", "-m", "add feature")

	_, wts, err := mgr.List(ctx, repoDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byPath := map[string]Worktree{}
	for _, wt := range wts {
		byPath[wt.Path] = wt
	}
	if !byPath[repoDir].IsMain {
		t.Errorf("main worktree missing IsMain=true")
	}
	if !byPath[canonicalPath(detachedPath)].IsDetached {
		t.Errorf("detached worktree missing IsDetached=true")
	}
	if !byPath[resAuto.Worktree.Path].IsDirty || !byPath[resAuto.Worktree.Path].Managed {
		t.Errorf("resAuto expected IsDirty=true, Managed=true: %+v", byPath[resAuto.Worktree.Path])
	}
	if byPath[resNested.Worktree.Path].UnmergedCommits != 1 {
		t.Errorf("resNested UnmergedCommits = %d, want 1", byPath[resNested.Worktree.Path].UnmergedCommits)
	}

	// InspectSafety checks: main, dirty, unmerged, clean
	sMain, err := mgr.InspectSafety(ctx, repoDir)
	if err != nil || sMain.CanRemoveSafely || !sMain.IsMain || len(sMain.Warnings) == 0 {
		t.Fatalf("InspectSafety main = %+v, err=%v", sMain, err)
	}
	sDirty, err := mgr.InspectSafety(ctx, resAuto.Worktree.Path)
	if err != nil || sDirty.CanRemoveSafely || !sDirty.IsDirty {
		t.Fatalf("InspectSafety dirty = %+v, err=%v", sDirty, err)
	}
	sUnmerged, err := mgr.InspectSafety(ctx, resNested.Worktree.Path)
	if err != nil || sUnmerged.CanRemoveSafely || sUnmerged.UnmergedCommits != 1 {
		t.Fatalf("InspectSafety unmerged = %+v, err=%v", sUnmerged, err)
	}
	sClean, err := mgr.InspectSafety(ctx, resExisting.Worktree.Path)
	if err != nil || !sClean.CanRemoveSafely || len(sClean.Warnings) != 0 {
		t.Fatalf("InspectSafety clean = %+v, err=%v", sClean, err)
	}

	// ResolveWorktreePath by exact path, name, and branch
	if p, err := mgr.ResolveWorktreePath(ctx, repoDir, "feat/nested-ui"); err != nil || p != resNested.Worktree.Path {
		t.Fatalf("ResolveWorktreePath by branch = (%q, %v), want %q", p, err, resNested.Worktree.Path)
	}
	if p, err := mgr.ResolveWorktreePath(ctx, repoDir, "feat-nested-ui"); err != nil || p != resNested.Worktree.Path {
		t.Fatalf("ResolveWorktreePath by name = (%q, %v), want %q", p, err, resNested.Worktree.Path)
	}

	// 9. Remove: refusal on main
	if err := mgr.Remove(ctx, repoDir, RemoveOptions{Force: true}); err == nil {
		t.Fatalf("expected error removing main worktree")
	}

	// Remove: safe refusal on dirty
	if err := mgr.Remove(ctx, resAuto.Worktree.Path, RemoveOptions{Force: false}); err == nil {
		t.Fatalf("expected safe refusal removing dirty worktree")
	}

	// Remove: force remove dirty + delete branch
	if err := mgr.Remove(ctx, resAuto.Worktree.Path, RemoveOptions{Force: true, DeleteBranch: true}); err != nil {
		t.Fatalf("force Remove dirty: %v", err)
	}
	if _, err := os.Stat(resAuto.Worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("resAuto path still exists after Remove")
	}
	if _, ok, _ := mgr.Store.GetByPath(resAuto.Worktree.Path); ok {
		t.Fatalf("resAuto store record still exists after Remove")
	}
	branches, _ := mgr.ListBranches(ctx, repoDir)
	for _, b := range branches {
		if b == "implement-rate-limiter" {
			t.Fatalf("branch implement-rate-limiter should have been deleted")
		}
	}

	// Remove unmerged with ForceBranch
	if err := mgr.Remove(ctx, resNested.Worktree.Path, RemoveOptions{Force: false, DeleteBranch: true, ForceBranch: true}); err != nil {
		t.Fatalf("Remove unmerged with ForceBranch: %v", err)
	}
	branches, _ = mgr.ListBranches(ctx, repoDir)
	for _, b := range branches {
		if b == "feat/nested-ui" {
			t.Fatalf("branch feat/nested-ui should have been force-deleted")
		}
	}
}

func TestManagerPruneStaleAndOrphaned(t *testing.T) {
	t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")
	ctx := context.Background()
	mgr, _ := newTestManager(t)
	repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "prune-app"), true)

	// 1. Stale manually deleted worktree
	staleRes, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: "stale-wt"})
	if err != nil {
		t.Fatalf("create stale-wt: %v", err)
	}
	if err := os.RemoveAll(staleRes.Worktree.Path); err != nil {
		t.Fatal(err)
	}

	// 2. Orphaned clean merged managed worktree -> SHOULD be removed (and its branch deleted)
	orphanedRes, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: "orphaned-merged"})
	if err != nil {
		t.Fatalf("create orphaned-merged: %v", err)
	}
	// Make a commit and merge it back into main so it's genuinely merged (0 unmerged commits).
	if err := os.WriteFile(filepath.Join(orphanedRes.Worktree.Path, "merged.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, orphanedRes.Worktree.Path, "add", "merged.txt")
	runGit(t, orphanedRes.Worktree.Path, "commit", "-m", "merged work")
	runGit(t, repoDir, "merge", "--ff-only", "orphaned-merged")

	// 3. Active clean merged managed worktree -> MUST be preserved
	activeRes, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: "active-wt"})
	if err != nil {
		t.Fatalf("create active-wt: %v", err)
	}

	// 4. Orphaned DIRTY managed worktree -> MUST be preserved
	dirtyRes, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: "dirty-wt"})
	if err != nil {
		t.Fatalf("create dirty-wt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirtyRes.Worktree.Path, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Orphaned UNMERGED managed worktree -> MUST be preserved
	unmergedRes, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: "unmerged-wt"})
	if err != nil {
		t.Fatalf("create unmerged-wt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unmergedRes.Worktree.Path, "wip.txt"), []byte("commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, unmergedRes.Worktree.Path, "add", "wip.txt")
	runGit(t, unmergedRes.Worktree.Path, "commit", "-m", "unmerged commit")

	// 6. Orphaned clean merged UNMANAGED worktree -> MUST be preserved
	unmanagedPath := filepath.Join(t.TempDir(), "unmanaged-wt")
	runGit(t, repoDir, "worktree", "add", "-b", "unmanaged-br", unmanagedPath, "main")

	activePaths := map[string]bool{
		activeRes.Worktree.Path: true,
	}
	pruneRes, err := mgr.Prune(ctx, []string{repoDir}, true, activePaths)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if pruneRes.PrunedGit != 1 {
		t.Errorf("PrunedGit = %d, want 1", pruneRes.PrunedGit)
	}
	if pruneRes.PrunedRecords != 1 {
		t.Errorf("PrunedRecords = %d, want 1", pruneRes.PrunedRecords)
	}
	if len(pruneRes.RemovedPaths) != 1 || pruneRes.RemovedPaths[0] != orphanedRes.Worktree.Path {
		t.Errorf("RemovedPaths = %v, want [%s]", pruneRes.RemovedPaths, orphanedRes.Worktree.Path)
	}

	// Verify orphaned-merged path & branch are gone
	if _, err := os.Stat(orphanedRes.Worktree.Path); !os.IsNotExist(err) {
		t.Errorf("orphaned-merged directory should have been removed")
	}
	branches, _ := mgr.ListBranches(ctx, repoDir)
	for _, b := range branches {
		if b == "orphaned-merged" {
			t.Errorf("branch orphaned-merged should have been deleted")
		}
	}

	// Verify active, dirty, unmerged, unmanaged, and main were all preserved!
	for label, p := range map[string]string{
		"main":      repoDir,
		"active":    activeRes.Worktree.Path,
		"dirty":     dirtyRes.Worktree.Path,
		"unmerged":  unmergedRes.Worktree.Path,
		"unmanaged": canonicalPath(unmanagedPath),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s worktree at %s should have been preserved, got stat err: %v", label, p, err)
		}
	}
}

func TestManagerPruneActiveSubdirectoryAndSiblingPrefix(t *testing.T) {
	t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")
	ctx := context.Background()
	mgr, _ := newTestManager(t)
	repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "prune-subdir-app"), true)

	createMergedWorktree := func(branch, relFile string) CreateResult {
		t.Helper()
		res, err := mgr.Create(ctx, CreateRequest{RepoDir: repoDir, Branch: branch})
		if err != nil {
			t.Fatalf("create %s: %v", branch, err)
		}
		fullPath := filepath.Join(res.Worktree.Path, relFile)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", branch, err)
		}
		if err := os.WriteFile(fullPath, []byte(branch+"\n"), 0o644); err != nil {
			t.Fatalf("write file for %s: %v", branch, err)
		}
		runGit(t, res.Worktree.Path, "add", relFile)
		runGit(t, res.Worktree.Path, "commit", "-m", "commit "+branch)
		runGit(t, repoDir, "merge", "--no-ff", "--no-edit", branch)

		safety, err := mgr.InspectSafety(ctx, res.Worktree.Path)
		if err != nil || !safety.CanRemoveSafely {
			t.Fatalf("expected %s to be clean & merged before Prune: safety=%+v err=%v", branch, safety, err)
		}
		return res
	}

	// 1. Active via a nested subdirectory inside worktree "foo" -> MUST be preserved.
	fooRes := createMergedWorktree("foo", filepath.Join("sub", "dir", "keep.txt"))
	// 2. Unreferenced sibling "foo-2" sharing string prefix with "foo" -> MUST be pruned.
	foo2Res := createMergedWorktree("foo-2", "sibling.txt")
	if !strings.HasPrefix(foo2Res.Worktree.Path, fooRes.Worktree.Path+"-") {
		t.Fatalf("expected sibling prefix layout: foo=%q foo2=%q", fooRes.Worktree.Path, foo2Res.Worktree.Path)
	}

	// 3. Unreferenced worktree "baz" whose path is a string prefix of active "baz-2" -> MUST be pruned.
	bazRes := createMergedWorktree("baz", "baz.txt")
	// 4. Active via subdirectory inside "baz-2" -> MUST be preserved (and must NOT mark "baz" active).
	baz2Res := createMergedWorktree("baz-2", filepath.Join("pkg", "work.txt"))
	if !strings.HasPrefix(baz2Res.Worktree.Path, bazRes.Worktree.Path+"-") {
		t.Fatalf("expected sibling prefix layout: baz=%q baz2=%q", bazRes.Worktree.Path, baz2Res.Worktree.Path)
	}

	// 5. Active via a symlink pointing to a subdirectory inside "bar" -> MUST be preserved.
	barRes := createMergedWorktree("bar", filepath.Join("nested", "deep", "file.txt"))
	barSubLink := filepath.Join(t.TempDir(), "bar-sub-link")
	if err := os.Symlink(filepath.Join(barRes.Worktree.Path, "nested", "deep"), barSubLink); err != nil {
		t.Fatalf("symlink bar subdir: %v", err)
	}

	activePaths := map[string]bool{
		filepath.Join(fooRes.Worktree.Path, "sub", "dir"): true,
		filepath.Join(baz2Res.Worktree.Path, "pkg"):       true,
		barSubLink: true,
	}

	pruneRes, err := mgr.Prune(ctx, []string{repoDir}, true, activePaths)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	removed := make(map[string]bool, len(pruneRes.RemovedPaths))
	for _, p := range pruneRes.RemovedPaths {
		removed[canonicalPath(p)] = true
	}
	if len(removed) != 2 || !removed[foo2Res.Worktree.Path] || !removed[bazRes.Worktree.Path] {
		t.Fatalf("RemovedPaths = %v, want [%s, %s]", pruneRes.RemovedPaths, foo2Res.Worktree.Path, bazRes.Worktree.Path)
	}

	for label, p := range map[string]string{
		"foo (active subdir)":         fooRes.Worktree.Path,
		"baz-2 (active subdir)":       baz2Res.Worktree.Path,
		"bar (active symlink subdir)": barRes.Worktree.Path,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s at %s should have been preserved, got stat err: %v", label, p, err)
		}
	}
	for label, p := range map[string]string{
		"foo-2 (unreferenced sibling)": foo2Res.Worktree.Path,
		"baz (prefix of active baz-2)": bazRes.Worktree.Path,
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s at %s should have been pruned, got stat err: %v", label, p, err)
		}
	}
}

func TestManagerRepoBucketCollisionDisambiguation(t *testing.T) {
	ctx := context.Background()
	mgr, wtRoot := newTestManager(t)

	repoA := initTestRepo(t, filepath.Join(t.TempDir(), "a", "shared-name"), true)
	repoB := initTestRepo(t, filepath.Join(t.TempDir(), "b", "shared-name"), true)

	resA, err := mgr.Create(ctx, CreateRequest{RepoDir: repoA, Branch: "feat-one"})
	if err != nil {
		t.Fatalf("Create repoA: %v", err)
	}
	resB, err := mgr.Create(ctx, CreateRequest{RepoDir: repoB, Branch: "feat-one"})
	if err != nil {
		t.Fatalf("Create repoB: %v", err)
	}

	bucketA := filepath.Dir(resA.Worktree.Path)
	bucketB := filepath.Dir(resB.Worktree.Path)
	if bucketA == bucketB {
		t.Fatalf("expected different repo buckets for two repos named shared-name, both got %s", bucketA)
	}
	if bucketA != canonicalPath(filepath.Join(wtRoot, "shared-name")) {
		t.Errorf("expected first repo bucket to be shared-name, got %s", bucketA)
	}
	if !strings.HasPrefix(filepath.Base(bucketB), "shared-name-") {
		t.Errorf("expected second repo bucket to have hash suffix, got %s", filepath.Base(bucketB))
	}
}

func TestManagerCreateRollbackOnStoreUpsertFailure(t *testing.T) {
	t.Setenv("TMUX_SPACES_WORKTREE_PREFIX", "")
	ctx := context.Background()

	t.Run("new_branch_rolled_back_and_retry_succeeds", func(t *testing.T) {
		mgr, wtRoot := newTestManager(t)
		repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "rollback-app"), true)

		// Point Store.Path to an existing directory so Upsert fails.
		badStoreDir := filepath.Join(t.TempDir(), "store-is-a-dir")
		if err := os.MkdirAll(badStoreDir, 0o755); err != nil {
			t.Fatal(err)
		}
		goodStorePath := mgr.Store.Path
		mgr.Store.Path = badStoreDir

		const branch = "feat/rollback-new"
		expectedDst := filepath.Join(wtRoot, "rollback-app", WorktreeDirName(branch))

		_, err := mgr.Create(ctx, CreateRequest{
			RepoDir: repoDir,
			Branch:  branch,
		})
		if err == nil {
			t.Fatalf("expected Create to fail when Store.Path is a directory")
		}

		// Worktree directory must not exist on disk.
		if _, statErr := os.Stat(expectedDst); !os.IsNotExist(statErr) {
			t.Fatalf("expected rolled-back worktree dir %s to not exist, stat err=%v", expectedDst, statErr)
		}

		// Worktree must not be listed in git worktree list.
		wtList := runGit(t, repoDir, "worktree", "list", "--porcelain")
		if strings.Contains(wtList, WorktreeDirName(branch)) {
			t.Fatalf("rolled-back worktree still present in git worktree list:\n%s", wtList)
		}

		// Newly created branch must have been deleted.
		branchList := runGit(t, repoDir, "branch", "--list", branch)
		if branchList != "" {
			t.Fatalf("expected newly created branch %q to be deleted, got %q", branch, branchList)
		}

		// No stale spaces-base git config should be left behind.
		cmd := exec.Command("git", "config", "--local", "--get", "branch."+branch+".spaces-base")
		cmd.Dir = repoDir
		if out, cfgErr := cmd.CombinedOutput(); cfgErr == nil {
			t.Fatalf("expected no spaces-base config after rollback, got %q", strings.TrimSpace(string(out)))
		}

		// Retrying with a working store must succeed cleanly with the same branch and directory name.
		mgr.Store.Path = goodStorePath
		res, err := mgr.Create(ctx, CreateRequest{
			RepoDir: repoDir,
			Branch:  branch,
		})
		if err != nil {
			t.Fatalf("retry Create failed: %v", err)
		}
		if !res.CreatedBranch || res.Worktree.Branch != branch || res.Worktree.Name != WorktreeDirName(branch) {
			t.Fatalf("unexpected retry Create result: %+v", res)
		}
		if res.Worktree.Path != canonicalPath(expectedDst) {
			t.Errorf("retry worktree path = %q, want %q", res.Worktree.Path, canonicalPath(expectedDst))
		}
	})

	t.Run("preexisting_branch_preserved_on_rollback", func(t *testing.T) {
		mgr, wtRoot := newTestManager(t)
		repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "rollback-existing"), true)

		const branch = "existing-branch"
		runGit(t, repoDir, "branch", branch)
		runGit(t, repoDir, "config", "--local", "branch."+branch+".spaces-base", "custom-base")

		badStoreDir := filepath.Join(t.TempDir(), "store-is-a-dir")
		if err := os.MkdirAll(badStoreDir, 0o755); err != nil {
			t.Fatal(err)
		}
		goodStorePath := mgr.Store.Path
		mgr.Store.Path = badStoreDir

		expectedDst := filepath.Join(wtRoot, "rollback-existing", WorktreeDirName(branch))

		_, err := mgr.Create(ctx, CreateRequest{
			RepoDir: repoDir,
			Branch:  branch,
		})
		if err == nil {
			t.Fatalf("expected Create to fail when Store.Path is a directory")
		}

		// Worktree directory must be removed and not listed in git worktree list.
		if _, statErr := os.Stat(expectedDst); !os.IsNotExist(statErr) {
			t.Fatalf("expected rolled-back worktree dir %s to not exist, stat err=%v", expectedDst, statErr)
		}
		wtList := runGit(t, repoDir, "worktree", "list", "--porcelain")
		if strings.Contains(wtList, WorktreeDirName(branch)) {
			t.Fatalf("rolled-back worktree still present in git worktree list:\n%s", wtList)
		}

		// Pre-existing branch and its prior spaces-base config must be preserved.
		branchList := runGit(t, repoDir, "branch", "--list", branch)
		if !strings.Contains(branchList, branch) {
			t.Fatalf("expected pre-existing branch %q to be preserved, got %q", branch, branchList)
		}
		gotBase := runGit(t, repoDir, "config", "--local", "--get", "branch."+branch+".spaces-base")
		if gotBase != "custom-base" {
			t.Fatalf("expected pre-existing spaces-base config to be preserved, got %q", gotBase)
		}

		// Retrying with a working store attaches the pre-existing branch cleanly.
		mgr.Store.Path = goodStorePath
		res, err := mgr.Create(ctx, CreateRequest{
			RepoDir: repoDir,
			Branch:  branch,
		})
		if err != nil {
			t.Fatalf("retry Create on pre-existing branch failed: %v", err)
		}
		if res.CreatedBranch || res.Worktree.Branch != branch {
			t.Fatalf("unexpected retry Create result: %+v", res)
		}
	})

	t.Run("rollback_error_included_when_rollback_also_fails", func(t *testing.T) {
		mgr, _ := newTestManager(t)
		repoDir := initTestRepo(t, filepath.Join(t.TempDir(), "rollback-err"), true)

		badStoreDir := filepath.Join(t.TempDir(), "store-is-a-dir")
		if err := os.MkdirAll(badStoreDir, 0o755); err != nil {
			t.Fatal(err)
		}
		mgr.Store.Path = badStoreDir

		baseRunner := defaultRunCommand
		mgr.Runner = func(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, []byte, int, error) {
			if name == "git" && len(args) >= 3 && args[1] == "worktree" && args[2] == "remove" {
				return nil, []byte("simulated remove failure"), 1, nil
			}
			return baseRunner(ctx, dir, stdin, name, args...)
		}

		_, err := mgr.Create(ctx, CreateRequest{
			RepoDir: repoDir,
			Branch:  "feat/rollback-fail",
		})
		if err == nil {
			t.Fatalf("expected Create to fail")
		}
		if !strings.Contains(err.Error(), "persist metadata") || !strings.Contains(err.Error(), "rollback failed") || !strings.Contains(err.Error(), "simulated remove failure") {
			t.Fatalf("expected error to wrap both persist and rollback failures, got: %v", err)
		}
	})
}

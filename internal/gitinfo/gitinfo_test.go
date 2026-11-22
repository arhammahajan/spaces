package gitinfo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
)

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestCollectGitStatusCommittedAndUncommitted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	runGitTest(t, dir, "init", "-q", "-b", "main")
	runGitTest(t, dir, "config", "user.name", "Test User")
	runGitTest(t, dir, "config", "user.email", "test@example.com")

	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "base.txt")
	runGitTest(t, dir, "commit", "-q", "-m", "init")

	// Clean on main.
	info := CollectGitStatus(context.Background(), dir, nil)
	if info == nil {
		t.Fatal("expected GitInfo for git repo")
	}
	if info.Branch != "main" || info.IsDirty || info.LinesAdded != 0 || info.LinesRemoved != 0 {
		t.Fatalf("unexpected clean main status: %+v", info)
	}

	// Feature branch with committed diff + uncommitted modified + untracked text + untracked binary.
	runGitTest(t, dir, "checkout", "-q", "-b", "feat/badges")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("line1\nchanged\nline3\nadded1\nadded2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "base.txt")
	runGitTest(t, dir, "commit", "-q", "-m", "feature commit")

	// Uncommitted change to tracked file (+1, -1) + untracked text file (3 lines) + binary file (ignored).
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("line1\nchanged2\nline3\nadded1\nadded2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, '\n', 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}

	info = CollectGitStatus(context.Background(), dir, nil)
	if info == nil {
		t.Fatal("expected GitInfo")
	}
	if info.Branch != "feat/badges" {
		t.Fatalf("Branch = %q, want feat/badges", info.Branch)
	}
	if info.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", info.BaseBranch)
	}
	// base.txt changed 1 line (-1, +1) and added 2 lines (+2) => committed +3/-1.
	if info.LinesAdded != 3 || info.LinesRemoved != 1 {
		t.Fatalf("committed diff = +%d/-%d, want +3/-1", info.LinesAdded, info.LinesRemoved)
	}
	if !info.IsDirty {
		t.Fatal("expected IsDirty=true")
	}
	// Uncommitted: base.txt (+1/-1) + untracked.txt (3 lines, no trailing newline) + blob.bin (0) => +4/-1.
	if info.UncommittedAdded != 4 || info.UncommittedRemoved != 1 {
		t.Fatalf("uncommitted diff = +%d/-%d, want +4/-1", info.UncommittedAdded, info.UncommittedRemoved)
	}

	// Non-git directory returns nil.
	nonGit := t.TempDir()
	if got := CollectGitStatus(context.Background(), nonGit, nil); got != nil {
		t.Fatalf("expected nil for non-git dir, got %+v", got)
	}
}

func TestCollectGitStatusLinkedWorktreeAndSpacesBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	mainDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainDir, "init", "-q", "-b", "main")
	runGitTest(t, mainDir, "config", "user.name", "Test User")
	runGitTest(t, mainDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(mainDir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainDir, "add", "a.txt")
	runGitTest(t, mainDir, "commit", "-q", "-m", "init")

	// Main checkout reports IsWorktree=false.
	mainInfo := CollectGitStatus(context.Background(), mainDir, nil)
	if mainInfo == nil || mainInfo.IsWorktree || mainInfo.RepoName != "myrepo" {
		t.Fatalf("unexpected main checkout GitInfo: %+v", mainInfo)
	}

	// Linked worktree reports IsWorktree=true, WorktreeName, MainRepoRoot, RepoName, and spaces-base.
	wtDir := filepath.Join(t.TempDir(), "wt-auth-fix")
	runGitTest(t, mainDir, "worktree", "add", "-q", "-b", "feat/auth-fix", wtDir, "main")
	runGitTest(t, wtDir, "config", "--local", "branch.feat/auth-fix.spaces-base", "main")

	wtInfo := CollectGitStatus(context.Background(), wtDir, nil)
	if wtInfo == nil {
		t.Fatal("expected GitInfo for linked worktree")
	}
	if !wtInfo.IsWorktree {
		t.Fatalf("expected IsWorktree=true, got %+v", wtInfo)
	}
	if wtInfo.WorktreeName != "wt-auth-fix" {
		t.Fatalf("WorktreeName = %q, want wt-auth-fix", wtInfo.WorktreeName)
	}
	if wtInfo.RepoName != "myrepo" {
		t.Fatalf("RepoName = %q, want myrepo", wtInfo.RepoName)
	}
	if wtInfo.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", wtInfo.BaseBranch)
	}
	if wtInfo.RepoKey != mainInfo.RepoKey {
		t.Fatalf("RepoKey mismatch: wt=%q main=%q", wtInfo.RepoKey, mainInfo.RepoKey)
	}
}

func TestParsePorcelainV2AheadBehindAndDetached(t *testing.T) {
	raw := strings.Join([]string{
		"# branch.oid 0123456789abcdef0123456789abcdef01234567",
		"# branch.head feat/ci",
		"# branch.upstream origin/feat/ci",
		"# branch.ab +3 -2",
		"1 .M N... 100644 100644 100644 abc def file.go",
		"",
	}, "\x00")
	obs := parsePorcelainV2([]byte(raw))
	if obs.branch != "feat/ci" || obs.ahead != 3 || obs.behind != 2 || !obs.dirty || !obs.upstream {
		t.Fatalf("unexpected obs: %+v", obs)
	}

	detached := "# branch.oid 0123456789abcdef0123456789abcdef01234567\x00# branch.head (detached)\x00"
	obs2 := parsePorcelainV2([]byte(detached))
	if obs2.branch != "" || obs2.dirty {
		t.Fatalf("unexpected detached obs: %+v", obs2)
	}
}

func TestAggregateChecks(t *testing.T) {
	// Success
	if got := AggregateChecks([]CheckRollupItem{
		{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "lint", Status: "COMPLETED", Conclusion: "SKIPPED"},
	}); got == nil || got.State != model.CheckSuccess || got.Passed != 1 || got.Total != 1 {
		t.Fatalf("success checks = %+v", got)
	}

	// Failure captures failing name and passed/total
	if got := AggregateChecks([]CheckRollupItem{
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "unit-tests", Status: "COMPLETED", Conclusion: "FAILURE"},
		{Context: "ci/deploy", State: "PENDING"},
	}); got == nil || got.State != model.CheckFailure || got.Passed != 1 || got.Total != 3 || got.FailingName != "unit-tests" {
		t.Fatalf("failure checks = %+v", got)
	}

	// Pending
	if got := AggregateChecks([]CheckRollupItem{
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Name: "lint", Status: "IN_PROGRESS"},
	}); got == nil || got.State != model.CheckPending || got.Passed != 1 || got.Total != 2 {
		t.Fatalf("pending checks = %+v", got)
	}

	// Skipped only counts as success
	if got := AggregateChecks([]CheckRollupItem{
		{Name: "docs", Status: "COMPLETED", Conclusion: "SKIPPED"},
	}); got == nil || got.State != model.CheckSuccess {
		t.Fatalf("skipped-only checks = %+v", got)
	}
}

func TestFetchRepoGitHubGraphQLAndCollectorCache(t *testing.T) {
	var ghCalls atomic.Int32
	fakeRun := func(_ context.Context, _ string, stdin []byte, name string, args ...string) ([]byte, int, error) {
		if name != "gh" {
			return nil, 1, nil
		}
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"owner":{"login":"acme"},"name":"router","url":"https://github.com/acme/router"}`), 0, nil
		}
		if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
			ghCalls.Add(1)
			var req map[string]any
			if err := json.Unmarshal(stdin, &req); err != nil {
				t.Errorf("invalid graphql stdin json: %v", err)
			}
			resp := `{
				"data": {
					"repository": {
						"pr_0": {
							"nodes": [{
								"number": 42,
								"title": "Add git badges",
								"state": "OPEN",
								"isDraft": false,
								"url": "https://github.com/acme/router/pull/42",
								"commits": {
									"nodes": [{
										"commit": {
											"statusCheckRollup": {
												"contexts": {
													"nodes": [
														{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS"},
														{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE"}
													]
												}
											}
										}
									}]
								}
							}]
						},
						"ref_0": null
					}
				}
			}`
			return []byte(resp), 0, nil
		}
		return nil, 1, nil
	}

	now := time.Now()
	c := NewCollector()
	c.run = fakeRun
	c.now = func() time.Time { return now }

	gitMap := map[string]*model.GitInfo{
		"/repo/wt1": {RepoKey: "/repo/.git", RepoRoot: "/repo/wt1", Branch: "feat/badges"},
		"/repo/wt2": {RepoKey: "/repo/.git", RepoRoot: "/repo/wt2", Branch: "feat/badges"},
	}

	out := c.FetchPR(context.Background(), gitMap, false)
	if ghCalls.Load() != 1 {
		t.Fatalf("expected 1 deduplicated GraphQL call, got %d", ghCalls.Load())
	}
	got := out["/repo/wt1"]
	if got == nil || got.PR == nil || got.PR.Number != 42 || got.PR.State != "OPEN" {
		t.Fatalf("unexpected PR: %+v", got)
	}
	if got.Checks == nil || got.Checks.State != model.CheckFailure || got.Checks.Passed != 1 || got.Checks.Total != 2 || got.Checks.FailingName != "lint" {
		t.Fatalf("unexpected Checks: %+v", got.Checks)
	}

	// Second call within TTL hits cache (no extra gh call).
	_ = c.FetchPR(context.Background(), gitMap, false)
	if ghCalls.Load() != 1 {
		t.Fatalf("expected cache hit (still 1 call), got %d", ghCalls.Load())
	}

	// Forced refresh bypasses TTL.
	_ = c.FetchPR(context.Background(), gitMap, true)
	if ghCalls.Load() != 2 {
		t.Fatalf("expected forced refresh (2 calls), got %d", ghCalls.Load())
	}
}

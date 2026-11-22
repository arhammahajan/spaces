// Package gitinfo collects local git branch/diff stats and GitHub PR/CI
// summaries asynchronously for tmux-spaces popups, inspired by raine/workmux.
package gitinfo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
)

const (
	gitCmdTimeout        = 3 * time.Second
	maxUntrackedFileSize = 2 * 1024 * 1024 // 2 MiB safety cap for untracked line counts
)

// CommandRunner executes a command in dir with optional stdin and returns
// stdout and exit code. Overridden in tests.
type CommandRunner func(ctx context.Context, dir string, stdin []byte, name string, args ...string) (stdout []byte, exitCode int, err error)

func defaultRunCommand(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, ee.ExitCode(), err
		}
		return out, -1, err
	}
	return out, 0, nil
}

// CollectGitStatus inspects a local directory for git branch, ahead/behind,
// committed diff stats (base...HEAD), uncommitted diff stats (working tree +
// untracked files), rebase state, and merge-conflict prediction.
// Returns nil if dir is not inside a git repository.
func CollectGitStatus(ctx context.Context, dir string, run CommandRunner) *model.GitInfo {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if run == nil {
		run = defaultRunCommand
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}

	gctx, cancel := context.WithTimeout(ctx, gitCmdTimeout)
	defer cancel()

	// Single rev-parse to discover worktree root, common dir (repo key), and git dir.
	out, code, err := run(gctx, dir, nil, "git", "--no-optional-locks", "rev-parse", "--show-toplevel", "--git-common-dir", "--git-dir")
	if err != nil || code != 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(lines) < 3 {
		return nil
	}
	repoRoot := resolveAbsPath(dir, strings.TrimSpace(lines[0]))
	commonDir := resolveAbsPath(dir, strings.TrimSpace(lines[1]))
	gitDir := resolveAbsPath(dir, strings.TrimSpace(lines[2]))
	if repoRoot == "" {
		return nil
	}
	canonCommon := canonicalPath(commonDir)
	canonGit := canonicalPath(gitDir)
	repoKey := canonCommon
	if repoKey == "" {
		repoKey = canonicalPath(repoRoot)
	}
	isWorktree := canonCommon != "" && canonGit != "" && canonGit != canonCommon
	mainRoot := repoRoot
	if isWorktree {
		if filepath.Base(canonCommon) == ".git" {
			mainRoot = filepath.Dir(canonCommon)
		} else {
			mainRoot = canonCommon
		}
	}
	repoName := filepath.Base(mainRoot)
	if repoName == "" || repoName == "." || repoName == "/" {
		repoName = filepath.Base(repoRoot)
	}
	wtName := ""
	if isWorktree {
		wtName = filepath.Base(canonGit)
		if wtName == "" || wtName == "." || wtName == ".git" {
			wtName = filepath.Base(repoRoot)
		}
	}

	info := &model.GitInfo{
		RepoKey:      repoKey,
		RepoRoot:     repoRoot,
		MainRepoRoot: mainRoot,
		RepoName:     repoName,
		IsWorktree:   isWorktree,
		WorktreeName: wtName,
		IsRebasing:   isRebasingDir(gitDir),
	}

	// Single porcelain v2 status gives branch, upstream, ahead/behind, and dirty state.
	stOut, stCode, stErr := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=all", "-z")
	if stErr != nil || stCode != 0 {
		return info
	}
	obs := parsePorcelainV2(stOut)
	info.Branch = obs.branch
	info.Ahead = obs.ahead
	info.Behind = obs.behind
	info.IsDirty = obs.dirty

	// Discover base branch and compute committed branch diff stats when on a feature branch.
	if info.Branch != "" {
		base := discoverBaseBranch(gctx, repoRoot, info.Branch, run)
		info.BaseBranch = base
		if base != "" && info.Branch != base {
			if diffOut, dCode, dErr := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "diff", "--no-ext-diff", "--no-textconv", "--numstat", base+"...HEAD"); dErr == nil && dCode == 0 {
				info.LinesAdded, info.LinesRemoved = parseNumstat(diffOut)
			}
			// Fast merge-conflict prediction (git >= 2.38). Ignore exit codes other than 0/1.
			if _, mCode, _ := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "merge-tree", "--write-tree", base, "HEAD"); mCode == 1 {
				info.HasConflict = true
			}
		}
	}

	// Compute uncommitted line stats (working tree diff vs HEAD + untracked text files).
	if info.IsDirty {
		if uOut, uCode, uErr := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "diff", "--no-ext-diff", "--no-textconv", "--numstat", "HEAD"); uErr == nil && uCode == 0 {
			info.UncommittedAdded, info.UncommittedRemoved = parseNumstat(uOut)
		} else if uOut, uCode, uErr := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "diff", "--no-ext-diff", "--no-textconv", "--numstat", "--cached"); uErr == nil && uCode == 0 {
			// Initial commit repo with no HEAD yet: fall back to cached diff.
			info.UncommittedAdded, info.UncommittedRemoved = parseNumstat(uOut)
		}
		if lsOut, lCode, lErr := run(gctx, repoRoot, nil, "git", "--no-optional-locks", "ls-files", "--others", "--exclude-standard", "-z"); lErr == nil && lCode == 0 {
			for _, rawName := range bytes.Split(lsOut, []byte{0}) {
				if len(rawName) == 0 {
					continue
				}
				full := filepath.Join(repoRoot, string(rawName))
				if fi, err := os.Lstat(full); err == nil {
					if fi.Mode()&os.ModeSymlink != 0 {
						info.UncommittedAdded++
					} else if fi.Mode().IsRegular() && fi.Size() <= maxUntrackedFileSize {
						if n, err := countTextLines(full); err == nil {
							info.UncommittedAdded += n
						}
					}
				}
			}
		}
	}

	return info
}

func resolveAbsPath(base, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

func canonicalPath(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

func isRebasingDir(gitDir string) bool {
	if gitDir == "" {
		return false
	}
	for _, sub := range []string{"rebase-merge", "rebase-apply"} {
		if st, err := os.Stat(filepath.Join(gitDir, sub)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

type porcelainObservation struct {
	branch   string
	ahead    int
	behind   int
	dirty    bool
	upstream bool
}

// parsePorcelainV2 parses `git status --porcelain=v2 --branch -z` output.
func parsePorcelainV2(data []byte) porcelainObservation {
	var obs porcelainObservation
	records := bytes.Split(data, []byte{0})
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) == 0 {
			continue
		}
		if bytes.HasPrefix(rec, []byte("# ")) {
			header := string(rec[2:])
			switch {
			case strings.HasPrefix(header, "branch.head "):
				name := strings.TrimSpace(strings.TrimPrefix(header, "branch.head "))
				if name != "" && name != "(detached)" {
					obs.branch = name
				}
			case strings.HasPrefix(header, "branch.upstream "):
				obs.upstream = true
			case strings.HasPrefix(header, "branch.ab "):
				val := strings.TrimSpace(strings.TrimPrefix(header, "branch.ab "))
				fields := strings.Fields(val)
				if len(fields) >= 2 {
					if a, err := strconv.Atoi(strings.TrimPrefix(fields[0], "+")); err == nil && a > 0 {
						obs.ahead = a
					}
					if b, err := strconv.Atoi(strings.TrimPrefix(fields[1], "-")); err == nil && b > 0 {
						obs.behind = b
					}
				}
			}
			continue
		}
		switch rec[0] {
		case '1', 'u', '?':
			obs.dirty = true
		case '2':
			// Renamed/copied entries are followed by a second NUL-terminated original path record.
			obs.dirty = true
			i++
		}
	}
	return obs
}

func discoverBaseBranch(ctx context.Context, repoRoot, branch string, run CommandRunner) string {
	// Respect per-branch spaces-base or workmux-base config if set.
	for _, key := range []string{"branch." + branch + ".spaces-base", "branch." + branch + ".workmux-base"} {
		if out, code, err := run(ctx, repoRoot, nil, "git", "--no-optional-locks", "config", "--local", key); err == nil && code == 0 {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	}
	// Try origin/HEAD symbolic ref.
	if out, code, err := run(ctx, repoRoot, nil, "git", "--no-optional-locks", "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil && code == 0 {
		ref := strings.TrimSpace(string(out))
		if b, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok && b != "" {
			return b
		}
	}
	// Fallback to local main or master.
	for _, cand := range []string{"main", "master"} {
		if _, code, err := run(ctx, repoRoot, nil, "git", "--no-optional-locks", "rev-parse", "--verify", "--quiet", cand); err == nil && code == 0 {
			return cand
		}
	}
	return ""
}

// parseNumstat sums added/removed lines from `git diff --numstat`.
// Binary files report "-\t-\t<path>" and are skipped.
func parseNumstat(data []byte) (added, removed int) {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{'\t'}, 3)
		if len(parts) < 2 {
			continue
		}
		if a, err := strconv.Atoi(string(parts[0])); err == nil && a > 0 {
			added += a
		}
		if r, err := strconv.Atoi(string(parts[1])); err == nil && r > 0 {
			removed += r
		}
	}
	return added, removed
}

// countTextLines counts lines in a file like git (skipping binary files that
// contain a NUL byte in the first 8 KiB).
func countTextLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var probe [8192]byte
	n, err := f.Read(probe[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if bytes.IndexByte(probe[:n], 0) >= 0 {
		return 0, nil // binary
	}

	count := bytes.Count(probe[:n], []byte{'\n'})
	last := probe[n-1]

	var buf [32 * 1024]byte
	for {
		m, rerr := f.Read(buf[:])
		if m > 0 {
			count += bytes.Count(buf[:m], []byte{'\n'})
			last = buf[m-1]
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return count, rerr
		}
	}
	if last != '\n' {
		count++
	}
	return count, nil
}

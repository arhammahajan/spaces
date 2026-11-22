// Package worktree manages Git worktrees, smart branch naming, metadata
// persistence, safety inspection, and lifecycle cleanup for tmux-spaces.
package worktree

import "context"

// CommandRunner executes a command in dir with optional stdin and returns
// stdout, stderr, exit code, and error. Overridden in tests when needed.
type CommandRunner func(ctx context.Context, dir string, stdin []byte, name string, args ...string) (stdout []byte, stderr []byte, exitCode int, err error)

// RepoInfo describes the discovered Git repository for a directory.
type RepoInfo struct {
	RepoKey             string // canonical path to git common dir
	MainRoot            string // main checkout root path; for non-bare repos filepath.Dir(commonDir)
	RepoName            string // basename of MainRoot
	CurrentWorktreePath string // canonical --show-toplevel
	CurrentBranch       string // empty when detached
	DefaultBase         string // origin/HEAD branch, else local main, else master, else CurrentBranch
	HasCommits          bool   // git rev-parse --verify HEAD succeeds
}

// Worktree describes a single Git worktree enriched with status and metadata.
type Worktree struct {
	Path            string
	Name            string
	Branch          string
	HeadSHA         string
	BaseBranch      string
	RepoKey         string
	MainRepoRoot    string
	RepoName        string
	IsMain          bool
	IsBare          bool
	IsDetached      bool
	IsLocked        bool
	IsPrunable      bool
	IsDirty         bool
	Managed         bool
	NameSource      string
	CreatedAtMs     int64
	UnmergedCommits int
}

// Record is the JSON-persisted metadata record stored in worktrees.json.
type Record struct {
	Path           string `json:"path"`
	RepoKey        string `json:"repo_key"`
	MainRepoRoot   string `json:"main_repo_root"`
	RepoName       string `json:"repo_name"`
	Name           string `json:"name"`
	Branch         string `json:"branch"`
	BaseBranch     string `json:"base_branch,omitempty"`
	Managed        bool   `json:"managed"`
	NameSource     string `json:"name_source,omitempty"`
	CreatedAtMs    int64  `json:"created_at_ms"`
	LastUsedAtMs   int64  `json:"last_used_at_ms,omitempty"`
	OriginSpace    string `json:"origin_space,omitempty"`
	OriginWindowID string `json:"origin_window_id,omitempty"`
	OriginPaneID   string `json:"origin_pane_id,omitempty"`
}

// NamingContext supplies contextual hints for SuggestName.
type NamingContext struct {
	ExplicitName   string
	TargetName     string
	TargetKind     string // "tab" or "space"
	RepoName       string
	CurrentBranch  string
	HarnessApp     string
	HarnessProfile string
	HarnessTitle   string
	HarnessPrompt  string
	Prefix         string
	Seed           int64
}

// NamingResult is the suggested branch name, directory name, and provenance.
type NamingResult struct {
	Branch  string
	DirName string
	Source  string
	Rule    string
}

// CreateRequest configures worktree creation.
type CreateRequest struct {
	RepoDir        string
	Branch         string
	BaseRef        string
	Naming         NamingContext
	OriginSpace    string
	OriginWindowID string
	OriginPaneID   string
	WorktreeRoot   string
}

// CreateResult reports the newly created worktree and naming provenance.
type CreateResult struct {
	Worktree      Worktree
	CreatedBranch bool
	Naming        NamingResult
}

// RemoveOptions controls worktree and branch deletion behavior.
type RemoveOptions struct {
	Force        bool
	DeleteBranch bool
	ForceBranch  bool
}

// RemoveSafety summarizes whether a worktree can be safely removed.
type RemoveSafety struct {
	CanRemoveSafely bool
	IsMain          bool
	IsDirty         bool
	UnmergedCommits int
	Branch          string
	BaseBranch      string
	Warnings        []string
}

// PruneResult summarizes worktree pruning and cleanup counts.
type PruneResult struct {
	PrunedGit     int
	PrunedRecords int
	RemovedPaths  []string
}

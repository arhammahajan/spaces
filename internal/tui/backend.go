package tui

import (
	"context"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/usage"
)

// Backend abstracts all I/O the TUI needs so it compiles and tests
// standalone without importing tmux/screen/cli packages.
type Backend interface {
	LoadSpaces(ctx context.Context) ([]model.Space, error)
	LoadHookDetail(ctx context.Context, session string, paneNum int) (*model.HookDetail, error)
	LoadHarnesses(ctx context.Context) ([]model.HarnessEntry, error)
	OpenSpace(ctx context.Context, name string) error
	FocusPane(ctx context.Context, session, windowID, paneID string) error
	CreateSpace(ctx context.Context, name, cwd string) error
	RenameSpace(ctx context.Context, oldName, newName string) error
	KillSpace(ctx context.Context, name string) error
	LaunchAgent(ctx context.Context, session, cwd, agent, profile string) error
}

// ResurrectBackend is an optional extension of Backend for listing, restoring,
// and deleting persisted exited space snapshots.
type ResurrectBackend interface {
	LoadExited(ctx context.Context) ([]model.ExitedSpace, error)
	RestoreSpace(ctx context.Context, name string) error
	DeleteExited(ctx context.Context, name string) error
}

// PreviewBackend is an optional extension of Backend for capturing compact
// live pane viewports for the Attention Inbox.
type PreviewBackend interface {
	CapturePreviews(ctx context.Context, paneIDs []string, lines int) map[string]string
}

// WindowRenameBackend is an optional extension of Backend for renaming a
// tmux window (tab) within a space.
type WindowRenameBackend interface {
	RenameWindow(ctx context.Context, windowID, newName string) error
}

// WindowManageBackend is an optional extension of Backend for creating and
// closing tmux windows (tabs) from the Tabs popup.
type WindowManageBackend interface {
	NewWindow(ctx context.Context, session, cwd, name string) error
	KillWindow(ctx context.Context, windowID string) error
}

// GitBackend is an optional extension of Backend for asynchronously collecting
// git branch/diff stats and GitHub PR/CI status for pane and space CWDs.
type GitBackend interface {
	LoadGitStatus(ctx context.Context, cwds []string, force bool) map[string]*model.GitInfo
	LoadPRStatus(ctx context.Context, gitByCWD map[string]*model.GitInfo, force bool) map[string]*model.GitInfo
}

// OutcomesBackend is an optional extension of Backend for loading recent
// completion/failure outcomes across spaces and opening an outcome's agent
// conversation in a new tab.
type OutcomesBackend interface {
	LoadOutcomes(ctx context.Context) ([]model.OutcomeEntry, error)
	OpenOutcome(ctx context.Context, outcome model.OutcomeEntry) error
}

// ConversationsBackend is an optional extension of Backend for listing,
// searching, and reopening unified coding-agent conversations across harnesses.
type ConversationsBackend interface {
	LoadConversations(ctx context.Context, refresh, includeArchived bool) (model.ConversationSnapshot, error)
	SearchConversations(ctx context.Context, req model.ConversationSearchRequest) ([]model.ConversationSearchHit, error)
	OpenConversation(ctx context.Context, conv model.ConversationEntry, forceNewTab bool) error
}

// SharedBackend is an optional extension of Backend for listing shared MCP
// servers and skills and opening their source files in an editor.
type SharedBackend interface {
	LoadShared(ctx context.Context, dir string) (model.SharedSnapshot, error)
	OpenSharedFile(ctx context.Context, path string) error
}

// WorktreeNamingInput provides context for live auto-assigned worktree branch naming.
type WorktreeNamingInput struct {
	ExplicitName  string
	TargetName    string
	TargetKind    string // "tab" or "space"
	HarnessApp    string
	HarnessTitle  string
	HarnessPrompt string
	Seed          int64
}

// WorktreeCreateRequest describes a tab or space creation request with optional
// git worktree creation or preexisting worktree selection.
type WorktreeCreateRequest struct {
	Session       string // target tmux session (used for CreateTab)
	SpaceName     string // optional desired space name (used for CreateSpaceWithWorktree)
	TabName       string // optional desired tab name (empty = tmux automatic process name)
	Folder        string // starting directory / repo folder
	UseWorktree   bool   // true when backed by a git worktree (new or existing)
	CreateNew     bool   // true = create new worktree; false = open ExistingPath
	ExistingPath  string // chosen existing worktree path when !CreateNew
	BranchName    string // explicit branch name (empty = auto-assigned)
	BaseBranch    string // base ref for new worktree (empty = repo default)
	AutoSeed      int64  // regeneration nonce for auto-naming
	HarnessApp    string
	HarnessTitle  string
	HarnessPrompt string
}

// WorktreeRemoveSafety reports whether removing a worktree is clean or has warnings.
type WorktreeRemoveSafety struct {
	CanRemoveSafely bool
	IsMain          bool
	IsDirty         bool
	UnmergedCommits int
	Branch          string
	BaseBranch      string
	Warnings        []string
}

// WorktreeBackend is an optional extension of Backend for git worktree
// discovery, auto-naming, creation, tab/space opening, and safe cleanup.
type WorktreeBackend interface {
	InspectWorktrees(ctx context.Context, dir string, naming WorktreeNamingInput) (model.WorktreeContext, error)
	SuggestWorktreeName(ctx context.Context, dir string, naming WorktreeNamingInput) (branch, dirName, source, rule string)
	ListAllWorktrees(ctx context.Context, spaces []model.Space) ([]model.WorktreeEntry, error)
	CreateTab(ctx context.Context, req WorktreeCreateRequest) error
	CreateSpaceWithWorktree(ctx context.Context, req WorktreeCreateRequest) error
	InspectWorktreeSafety(ctx context.Context, path string) (WorktreeRemoveSafety, error)
	RemoveWorktree(ctx context.Context, path string, force, deleteBranch bool, closeWindows []string) error
	PruneWorktrees(ctx context.Context, spaces []model.Space, removeOrphanedMerged bool) (string, error)
}

// TelemetryBackend is an optional extension of Backend for asynchronously
// collecting abtop-style session, context, quota, project, port, and MCP telemetry.
type TelemetryBackend interface {
	LoadTelemetry(ctx context.Context, spaces []model.Space, force bool) (model.TelemetrySnapshot, error)
	KillOrphanPorts(ctx context.Context, orphans []model.OrphanPort) (int, error)
}

// UsageBackend is an optional extension of Backend for loading aggregated
// multi-provider token/cost usage and burndown reports.
type UsageBackend interface {
	LoadUsage(ctx context.Context, force bool) (usage.Report, error)
}

// Mode selects which screen the TUI shows.
type Mode int

const (
	ModeNavigate Mode = iota
	ModeSearch
	ModeHelp
	ModeNewSpace
	ModeRename
	ModeConfirmKill
	ModeHookDetail
	ModeAgents
	ModeInbox
	ModeConversations
	ModeShared
	ModeTabs
	ModeWorktrees
	ModeConfirmRemoveWorktree
	ModeUsage
)

// Options configures a TUI run.
type Options struct {
	InitialMode      Mode
	InitialQuery     string
	OpenNew          bool
	OpenRenameSpace  bool
	OpenRenameWindow bool
	Target           string
	SharedDir        string
	HomeDir          string
	RefreshInterval  time.Duration
	Now              func() time.Time
}

// NowOrDefault returns the current time using Options.Now or time.Now.
func (o Options) NowOrDefault() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// RefreshIntervalOrDefault returns the polling interval, defaulting to 1.5s.
func (o Options) RefreshIntervalOrDefault() time.Duration {
	if o.RefreshInterval <= 0 {
		return 1500 * time.Millisecond
	}
	return o.RefreshInterval
}

package agent

import (
	"context"

	"github.com/arhammahajan/spaces/internal/screen"
)

// Plugin is the overarching interface implemented by every coding-agent /
// harness plugin in tmux-spaces.
//
// Shared infrastructure (tmux pane lifecycle, OSC 7501 streaming, TUI views,
// status rollups, worktree storage, resurrect layout persistence) interacts
// with agents exclusively through this interface and the Registry.
//
// Implementations typically embed BasePlugin and override only the capabilities
// that differ from defaults.
type Plugin interface {
	// Spec returns the static identity, alias, naming, and capability metadata
	// for this agent.
	Spec() Spec

	// MatchProcess reports whether the inspected OS process belongs to this agent.
	MatchProcess(proc ProcessInfo) bool

	// ScreenReader evaluates captured pane viewports and OSC titles for this agent.
	ScreenReader() ScreenReader

	// TitleExtractor extracts a conversation/session title from a captured viewport.
	TitleExtractor() TitleExtractor

	// HookPolicy governs how hooks, OSC 7501, and screen readings arbitrate state.
	HookPolicy() HookPolicy

	// ConversationStore returns an optional native conversation store reader/searcher.
	// Returning nil delegates to the shared conversation store (`spaces`).
	ConversationStore() ConversationStore

	// LogParser returns an optional native session/transcript log parser.
	// Returning nil indicates no custom log parser is provided.
	LogParser() LogParser

	// SessionResolver handles session ID extraction from argv, live process FD
	// discovery, and multi-pane restore deduplication.
	SessionResolver() SessionResolver

	// Launcher resolves the executable binary and harness-management behavior.
	Launcher() Launcher

	// ConfigProvider exposes stowed harness MCP/config paths.
	ConfigProvider() ConfigProvider
}

// Spec declares the static metadata and naming rules for an agent plugin.
type Spec struct {
	// ID is the canonical plugin identifier (e.g. "claude", "codex", "antigravity").
	ID string

	// Aliases are all alternate command names, binary names, or harness labels
	// that resolve to this plugin (e.g. ["claude-code"] for claude).
	Aliases []string

	// DisplayApp is the preferred label shown in UI/pane resolution when
	// normalizing an alias (e.g. "antigravity" for "agy", "cursor-agent" for "cursor").
	// Defaults to ID when empty.
	DisplayApp string

	// CLIDisplayApp is the canonical label written by CLI/shim/osc-watch and
	// ConversationEntry.DisplayApp (e.g. "claude-code" for claude).
	// Defaults to DisplayApp (or ID) when empty.
	CLIDisplayApp string

	// ConversationID is the canonical agent ID used in conversation stores and
	// @<agent> query filters. Defaults to ID when empty.
	ConversationID string

	// ShortName is the short prefix used in worktree auto-naming (e.g. "agy"
	// for antigravity, "cursor" for cursor-agent). Defaults to ID when empty.
	ShortName string

	// Binary is the executable name for direct (non-profile) launches
	// (e.g. "cursor-agent" for cursor). Defaults to ID when empty.
	Binary string

	// ScreenManifestID is the manifest ID in internal/screen/manifests.json
	// used by the default ManifestScreenReader (e.g. "agy", "cursor", "qodercli").
	ScreenManifestID string

	// HarnessManaged reports whether the `harness` CLI manages profiles and
	// resume flows for this agent.
	HarnessManaged bool

	// OSCAuthority reports whether live non-idle OSC 7501 / hook state takes
	// precedence over bare screen working/idle readings.
	OSCAuthority bool

	// RecognizesVersionBanner marks whether "<name> vX.Y.Z" pane titles should
	// be recognized as version banners rather than conversation titles.
	RecognizesVersionBanner bool

	// VersionBannerNames optionally overrides the names recognized in version
	// banners; when empty and RecognizesVersionBanner is true, ID and Aliases are used.
	VersionBannerNames []string

	// TitlePrefixes are harness-specific OSC title prefixes stripped during TUI
	// title rendering (e.g. "OC | ", "Claude Code - ", "Claude Code: ").
	TitlePrefixes []string

	// WorktreeTitlePrefixes are harness prefixes stripped during worktree slug
	// extraction (case-insensitive, e.g. "oc | ", "claude - ", "codex - ").
	WorktreeTitlePrefixes []string

	// BoilerplateTitles are generic placeholder titles ignored when deriving
	// worktree names (lowercase, e.g. "claude code").
	BoilerplateTitles []string

	// MatchesBareVersion reports whether this agent can run under a bare
	// version-number executable name on Linux (e.g. Claude Code's "2.1.76").
	MatchesBareVersion bool

	// WorktreeOnly marks agents known only for worktree naming (e.g. aider,
	// goose, windsurf, roo) rather than full runtime tmux-spaces management.
	WorktreeOnly bool
}

// EffectiveDisplayApp returns DisplayApp if set, else ID.
func (s Spec) EffectiveDisplayApp() string {
	if s.DisplayApp != "" {
		return s.DisplayApp
	}
	return s.ID
}

// EffectiveCLIDisplayApp returns CLIDisplayApp if set, else EffectiveDisplayApp.
func (s Spec) EffectiveCLIDisplayApp() string {
	if s.CLIDisplayApp != "" {
		return s.CLIDisplayApp
	}
	return s.EffectiveDisplayApp()
}

// EffectiveConversationID returns ConversationID if set, else ID.
func (s Spec) EffectiveConversationID() string {
	if s.ConversationID != "" {
		return s.ConversationID
	}
	return s.ID
}

// EffectiveShortName returns ShortName if set, else ID.
func (s Spec) EffectiveShortName() string {
	if s.ShortName != "" {
		return s.ShortName
	}
	return s.ID
}

// EffectiveBinary returns Binary if set, else ID.
func (s Spec) EffectiveBinary() string {
	if s.Binary != "" {
		return s.Binary
	}
	return s.ID
}

// ProcessInfo describes an OS process inspected during live agent detection.
type ProcessInfo struct {
	PID  int
	Comm string
	Args []string
}

// ScreenReader evaluates a captured pane viewport and OSC title.
type ScreenReader interface {
	// HasManifest reports whether this reader can meaningfully classify viewports.
	HasManifest() bool
	// Detect classifies the viewport state and flags.
	Detect(viewport, oscTitle string) screen.Detected
}

// TitleExtractor extracts a human-readable conversation/task title from a viewport.
type TitleExtractor interface {
	ExtractTitle(viewport string) string
}

// HookArbitrationInput carries the signals needed to evaluate agent-specific
// hook-vs-screen arbitration overrides.
type HookArbitrationInput struct {
	HookState                 string
	HookSilenceMs             int64
	HasActiveFiniteWork       bool
	HasOnlyLocalCmdBackground bool
	VisibleIdle               bool
	VisibleWorking            bool
	VisibleBlocker            bool
	ScreenState               string
}

// HookPolicy governs how hooks, OSC 7501, and screen readings interact.
type HookPolicy interface {
	// OSCAuthority reports whether live non-idle OSC 7501 / hook state wins over
	// bare screen working/idle readings.
	OSCAuthority() bool

	// ShouldYieldWorkingHookToIdle reports whether a "working" hook should yield
	// to a visibly idle viewport (e.g. missed Stop hook or stale local_bash task).
	ShouldYieldWorkingHookToIdle(in HookArbitrationInput) bool
}

// ConversationListOptions configures a native plugin ConversationStore query.
type ConversationListOptions struct {
	CWD             string
	IncludeArchived bool
	Limit           int
}

// ConversationRecord is the normalized conversation metadata produced by a
// native plugin ConversationStore.
type ConversationRecord struct {
	Agent        string
	App          string
	Profile      string
	SessionID    string
	Title        string
	CWD          string
	Model        string
	FirstMessage string
	LastMessage  string
	MessageCount int
	CreatedAtMs  int64
	UpdatedAtMs  int64
	Archived     bool
	SourcePath   string
}

// ConversationStore is an optional capability for plugins that read/search
// their agent's conversation store directly.
type ConversationStore interface {
	List(ctx context.Context, opts ConversationListOptions) ([]ConversationRecord, error)
	Search(ctx context.Context, query string, opts ConversationListOptions) ([]ConversationRecord, error)
}

// LogEntry represents a parsed event from an agent's transcript or hook log.
type LogEntry struct {
	TimestampMs int64
	Role        string
	Event       string
	Text        string
	ToolName    string
	SessionID   string
	Model       string
}

// LogParser is an optional capability for plugins that parse agent-specific
// transcript or session logs (JSONL, SQLite, JSON, etc.).
type LogParser interface {
	ParseLog(ctx context.Context, path string) ([]LogEntry, error)
}

// SessionPaneRef identifies a pane during restore-time session resolution.
type SessionPaneRef struct {
	Key         string
	Agent       string
	SessionID   string
	Profile     string
	CWD         string
	Title       string
	FullCommand []string
}

// SessionResolution holds resolved session IDs and skip-resume decisions for
// panes belonging to an agent during restore.
type SessionResolution struct {
	SessionIDs map[string]string
	SkipResume map[string]bool
}

// SessionLookupFunc resolves an unclaimed session ID for an agent in a CWD.
type SessionLookupFunc func(ctx context.Context, agent, cwd, profile, title string, exclude map[string]bool) (sessionID string, ok bool)

// SessionResolver encapsulates agent-specific session ID extraction, live
// process discovery, and multi-pane restore deduplication.
type SessionResolver interface {
	// ExtractSessionIDFromArgs extracts a session/conversation ID from argv.
	ExtractSessionIDFromArgs(argv []string) string

	// DiscoverLiveSessionID inspects a live process (e.g. /proc/<pid>/fd) to
	// find its active conversation/session ID.
	DiscoverLiveSessionID(pid int) string

	// SuppressMultiPaneCWDTitle reports whether CWD-based title fallback should
	// be suppressed when multiple live panes of this agent share the same CWD
	// without individual session IDs.
	SuppressMultiPaneCWDTitle() bool

	// ResolveRestoreSessions resolves and deduplicates session IDs across panes
	// of this agent during space restore.
	ResolveRestoreSessions(ctx context.Context, panes []SessionPaneRef, lookup SessionLookupFunc) SessionResolution
}

// Launcher encapsulates agent-specific binary resolution and harness management.
type Launcher interface {
	BinaryName() string
	IsHarnessManaged() bool
}

// MCPConfigTarget identifies a harness-specific MCP configuration file relative
// to a user's home directory.
type MCPConfigTarget struct {
	// HarnessLabel is the provenance label shown in the Shared UI (defaults to Spec.ID).
	HarnessLabel string
	// RelPath is the path relative to $HOME (e.g. ".codex/config.toml").
	RelPath string
}

// ConfigProvider exposes agent-specific configuration file locations.
type ConfigProvider interface {
	MCPConfigTargets() []MCPConfigTarget
}

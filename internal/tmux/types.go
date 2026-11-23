package tmux

import "time"

// Session represents one tmux session parsed from `tmux list-sessions`.
type Session struct {
	ID              string // e.g. "$0"
	Name            string
	Attached        int
	CreatedAt       time.Time
	Windows         int
	WorktreePath    string // #{@spaces_worktree_path}
	WorktreeName    string // #{@spaces_worktree_name}
	WorktreeBase    string // #{@spaces_worktree_base}
	WorktreeManaged bool   // #{@spaces_worktree_managed}
}

// Window represents one tmux window parsed from `tmux list-windows -a`.
type Window struct {
	SessionName     string
	ID              string // e.g. "@1"
	Index           int
	Name            string
	Active          bool
	Bell            bool
	Activity        bool
	Silence         bool
	Layout          string    // #{window_layout} for resurrect
	Last            bool      // #{window_last_flag}
	Zoomed          bool      // #{window_zoomed_flag}
	Width           int       // #{window_width}
	Height          int       // #{window_height}
	ActivityAt      time.Time // #{window_activity}
	WorktreePath    string    // #{@spaces_worktree_path}
	WorktreeName    string    // #{@spaces_worktree_name}
	WorktreeBase    string    // #{@spaces_worktree_base}
	WorktreeManaged bool      // #{@spaces_worktree_managed}
}

// Pane represents one tmux pane parsed from `tmux list-panes -a`, including
// native `@spaces_*` user options.
type Pane struct {
	SessionName    string
	WindowID       string // e.g. "@1"
	WindowIndex    int
	WindowName     string
	ID             string // e.g. "%12"
	NumericID      int    // e.g. 12
	PaneIndex      int    // #{pane_index}
	Title          string
	CurrentCommand string
	CurrentPath    string
	PID            int
	Active         bool
	WindowActive   bool
	Dead           bool
	WindowBell     bool
	WindowActivity bool
	WindowSilence  bool
	Width          int    // #{pane_width}
	Height         int    // #{pane_height}
	InMode         bool   // #{pane_in_mode}
	Synchronized   bool   // #{pane_synchronized}
	StartCommand   string // #{pane_start_command}
	DeadStatus     int    // #{pane_dead_status}

	// Native @spaces_* per-pane options set by hooks/shims/oscwatch.
	SpacesState   string
	SpacesApp     string
	SpacesKind    string
	SpacesMsg     string
	SpacesTitle   string // #{@spaces_title}
	SpacesTsMs    int64
	SpacesSource  string   // #{@spaces_source} ("osc7501", "hook", etc.)
	ResumeSession string   // #{@spaces_resume_session}
	ResumeProfile string   // #{@spaces_profile}
	LaunchCommand []string // #{@spaces_launch_cmd}
}

// ServerSnapshot holds a consistent view of sessions, windows, and panes.
type ServerSnapshot struct {
	CurrentSession string
	CurrentPaneID  string
	Sessions       []Session
	Windows        []Window
	Panes          []Pane
}

// PopupOptions configures `tmux display-popup`.
type PopupOptions struct {
	Width   string // e.g. "86%"
	Height  string // e.g. "82%"
	Title   string
	Border  string // e.g. "rounded"
	Close   bool   // -E: close when command exits
	CWD     string
	Command []string
}

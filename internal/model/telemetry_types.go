package model

import "github.com/arhammahajan/spaces/internal/agent"

// ContextFillWarning classifies per-session context window pressure.
type ContextFillWarning string

const (
	ContextWarningNone ContextFillWarning = ""
	ContextWarningHigh ContextFillWarning = "!" // >= 75%
	ContextWarningCrit ContextFillWarning = "⚠" // >= 90%
)

// ContextSessionEntry summarizes a single session's context occupancy for Panel 1 (Context).
type ContextSessionEntry struct {
	SessionKey      string             `json:"session_key"`
	PaneID          string             `json:"pane_id,omitempty"`
	WindowID        string             `json:"window_id,omitempty"`
	Project         string             `json:"project"`
	Agent           string             `json:"agent"`
	ContextTokens   int64              `json:"context_tokens"`
	ContextWindow   int64              `json:"context_window"`
	ContextPct      float64            `json:"context_pct"`
	Warning         ContextFillWarning `json:"warning,omitempty"`
	CompactionCount int                `json:"compaction_count,omitempty"`
}

// TelemetryContext holds aggregate context & token rate data for Panel 1 (Context).
type TelemetryContext struct {
	TokensPerMin   float64               `json:"tokens_per_min"`
	RateHistory    []float64             `json:"rate_history,omitempty"` // bounded recent token deltas
	TotalTokens    int64                 `json:"total_tokens"`
	ActiveSessions int                   `json:"active_sessions"`
	Sessions       []ContextSessionEntry `json:"sessions,omitempty"`
}

// PortEntry represents a listening TCP port opened by an agent child process (Panel 5).
type PortEntry struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Command   string `json:"command,omitempty"`
	Project   string `json:"project,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	PaneID    string `json:"pane_id,omitempty"`
	WindowID  string `json:"window_id,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// OrphanPort represents a listening port still open after its owning agent session ended (Panel 5).
type OrphanPort struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	Command   string `json:"command,omitempty"`
	Project   string `json:"project,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// ProjectSummary groups monitored sessions by project with git status (Panel 4).
type ProjectSummary struct {
	Name          string `json:"name"`
	RepoRoot      string `json:"repo_root,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	Branch        string `json:"branch,omitempty"`
	HasGit        bool   `json:"has_git,omitempty"`
	FilesAdded    int    `json:"files_added,omitempty"`
	FilesModified int    `json:"files_modified,omitempty"`
	IsClean       bool   `json:"is_clean,omitempty"`
	SessionCount  int    `json:"session_count"`
	ActiveCount   int    `json:"active_count,omitempty"`
}

// SessionRow is the normalized per-session telemetry record used by Panels 3 (Tokens) and 6 (Sessions),
// as well as the enriched tab inspector.
type SessionRow struct {
	Key              string              `json:"key"` // stable key (pane ID or agent:session)
	Space            string              `json:"space,omitempty"`
	WindowID         string              `json:"window_id,omitempty"`
	WindowIndex      int                 `json:"window_index,omitempty"`
	PaneID           string              `json:"pane_id,omitempty"`
	Agent            string              `json:"agent"`
	LaunchSurface    agent.LaunchSurface `json:"launch_surface,omitempty"`
	PID              int                 `json:"pid,omitempty"`
	AgentPID         int                 `json:"agent_pid,omitempty"`
	Project          string              `json:"project"`
	CWD              string              `json:"cwd,omitempty"`
	SessionID        string              `json:"session_id,omitempty"`
	Profile          string              `json:"profile,omitempty"`
	ConfigRoot       string              `json:"config_root,omitempty"`
	Summary          string              `json:"summary,omitempty"`
	CurrentTask      string              `json:"current_task,omitempty"`
	Status           Status              `json:"status"`
	RateLimited      bool                `json:"rate_limited,omitempty"`
	Model            string              `json:"model,omitempty"`
	Effort           string              `json:"effort,omitempty"`
	Version          string              `json:"version,omitempty"`
	Usage            agent.TokenUsage    `json:"usage"`
	TotalTokens      int64               `json:"total_tokens"`
	ContextTokens    int64               `json:"context_tokens,omitempty"`
	ContextWindow    int64               `json:"context_window,omitempty"`
	ContextPct       float64             `json:"context_pct,omitempty"`
	Warning          ContextFillWarning  `json:"warning,omitempty"`
	CompactionCount  int                 `json:"compaction_count,omitempty"`
	MemKB            int64               `json:"mem_kb,omitempty"`
	TurnCount        int                 `json:"turn_count,omitempty"`
	AvgTokensPerTurn int64               `json:"avg_tokens_per_turn,omitempty"`
	TokenHistory     []int64             `json:"token_history,omitempty"`
	ContextHistory   []int64             `json:"context_history,omitempty"`
	MemoryFiles      int                 `json:"memory_files,omitempty"`
	MemoryLines      int                 `json:"memory_lines,omitempty"`
	Ports            []PortEntry         `json:"ports,omitempty"`
	StartedAtMs      int64               `json:"started_at_ms,omitempty"`
	UpdatedAtMs      int64               `json:"updated_at_ms,omitempty"`
}

// TelemetrySnapshot is the immutable, UI-ready snapshot powering all 7 abtop-style
// telemetry panels and the enriched tab inspector.
type TelemetrySnapshot struct {
	Context     TelemetryContext      `json:"context"`
	Quotas      []agent.QuotaInfo     `json:"quotas,omitempty"`
	Projects    []ProjectSummary      `json:"projects,omitempty"`
	Ports       []PortEntry           `json:"ports,omitempty"`
	OrphanPorts []OrphanPort          `json:"orphan_ports,omitempty"`
	Sessions    []SessionRow          `json:"sessions,omitempty"`
	MCPServers  []agent.LiveMCPServer `json:"mcp_servers,omitempty"`
	UpdatedAt   int64                 `json:"updated_at,omitempty"` // unix seconds
}

// SessionForPane finds the normalized SessionRow associated with a tmux pane ID.
func (s TelemetrySnapshot) SessionForPane(paneID string) *SessionRow {
	if paneID == "" {
		return nil
	}
	for i := range s.Sessions {
		if s.Sessions[i].PaneID == paneID {
			return &s.Sessions[i]
		}
	}
	return nil
}

// QuotaForAgent returns the QuotaInfo for the given canonical agent ID, if present.
func (s TelemetrySnapshot) QuotaForAgent(agentID string) *agent.QuotaInfo {
	for i := range s.Quotas {
		if s.Quotas[i].Agent == agentID {
			return &s.Quotas[i]
		}
	}
	return nil
}

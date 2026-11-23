package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// LaunchSurface identifies how a coding agent session was launched.
type LaunchSurface string

const (
	LaunchSurfaceCLI LaunchSurface = "cli"
	LaunchSurfaceApp LaunchSurface = "app"
	LaunchSurfaceIDE LaunchSurface = "ide"
)

// ShortSuffix returns the compact abtop-style launch surface indicator:
// "" for CLI, "a" for desktop app, "i" for IDE extension.
func (s LaunchSurface) ShortSuffix() string {
	switch s {
	case LaunchSurfaceApp:
		return "a"
	case LaunchSurfaceIDE:
		return "i"
	default:
		return ""
	}
}

// DetectLaunchSurface classifies the launch surface from a process command line.
func DetectLaunchSurface(cmdline []string) LaunchSurface {
	if len(cmdline) == 0 {
		return LaunchSurfaceCLI
	}
	joined := strings.ReplaceAll(strings.Join(cmdline, " "), "\\", "/")
	switch {
	case strings.Contains(joined, "/Claude/claude-code/"):
		return LaunchSurfaceApp
	case strings.Contains(joined, "/extensions/anthropic.claude-code"):
		return LaunchSurfaceIDE
	default:
		return LaunchSurfaceCLI
	}
}

// TokenUsage holds normalized token accounting for a single session.
type TokenUsage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

// Total returns all accounted tokens across input, output, reasoning, and cache.
func (u TokenUsage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Active returns non-cached-read tokens (input + output + reasoning + cache_write),
// matching abtop's active_tokens() formula so token-rate graphs are not skewed by
// repeated cache reads.
func (u TokenUsage) Active() int64 {
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens + u.CacheWriteTokens
}

// HasData reports whether any token or cost field is non-zero.
func (u TokenUsage) HasData() bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 || u.ReasoningTokens > 0 ||
		u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 || u.CostUSD > 0
}

// SessionMetrics holds per-session telemetry collected by an agent plugin.
type SessionMetrics struct {
	Agent           string        `json:"agent,omitempty"`
	SessionID       string        `json:"session_id,omitempty"`
	LaunchSurface   LaunchSurface `json:"launch_surface,omitempty"`
	ConfigRoot      string        `json:"config_root,omitempty"`
	Profile         string        `json:"profile,omitempty"`
	CWD             string        `json:"cwd,omitempty"`
	GitBranch       string        `json:"git_branch,omitempty"`
	Model           string        `json:"model,omitempty"`
	Effort          string        `json:"effort,omitempty"`
	Version         string        `json:"version,omitempty"`
	Summary         string        `json:"summary,omitempty"`
	CurrentTask     string        `json:"current_task,omitempty"`
	Status          string        `json:"status,omitempty"`
	Usage           TokenUsage    `json:"usage"`
	ContextTokens   int64         `json:"context_tokens,omitempty"`
	ContextWindow   int64         `json:"context_window,omitempty"`
	CompactionCount int           `json:"compaction_count,omitempty"`
	TurnCount       int           `json:"turn_count,omitempty"`
	TokenHistory    []int64       `json:"token_history,omitempty"`
	ContextHistory  []int64       `json:"context_history,omitempty"`
	MemoryFiles     int           `json:"memory_files,omitempty"`
	MemoryLines     int           `json:"memory_lines,omitempty"`
	StartedAtMs     int64         `json:"started_at_ms,omitempty"`
	UpdatedAtMs     int64         `json:"updated_at_ms,omitempty"`
}

// SessionMetricsQuery supplies shared process and pane context to a plugin's
// SessionTelemetryProvider so plugins avoid redundant OS process scans.
type SessionMetricsQuery struct {
	PID         int
	AgentPID    int
	CWD         string
	SessionID   string
	Profile     string
	HomeDir     string
	ConfigRoots []string
	Cmdline     []string
	OpenFiles   []string
	SlowTick    bool
}

// SessionTelemetryProvider is an optional capability implemented by agent
// plugins that can extract rich per-session token, context, compaction, and
// turn metrics from native transcripts or databases.
type SessionTelemetryProvider interface {
	CollectSessionMetrics(ctx context.Context, q SessionMetricsQuery) (*SessionMetrics, error)
}

// QuotaWindow represents a single rolling rate-limit window (e.g. 5h or 7d).
type QuotaWindow struct {
	Label         string  `json:"label,omitempty"`
	WindowMinutes int     `json:"window_minutes,omitempty"`
	UsedPct       float64 `json:"used_pct"`
	ResetsAt      int64   `json:"resets_at,omitempty"` // unix seconds
}

// RemainingPct returns the remaining quota percentage clamped to [0, 100].
func (w QuotaWindow) RemainingPct() float64 {
	rem := 100.0 - w.UsedPct
	if rem < 0 {
		return 0
	}
	if rem > 100 {
		return 100
	}
	return rem
}

// FormatQuotaWindowLabel formats a window duration in minutes as a short label
// (e.g. 300 -> "5h", 10080 -> "7d", 43200 -> "30d").
func FormatQuotaWindowLabel(minutes int) string {
	if minutes <= 0 {
		return ""
	}
	if minutes%(60*24) == 0 {
		return fmt.Sprintf("%dd", minutes/(60*24))
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// QuotaInfo holds account-level remaining quota for an agent provider.
type QuotaInfo struct {
	Agent     string       `json:"agent"`
	Source    string       `json:"source,omitempty"`
	Primary   *QuotaWindow `json:"primary,omitempty"`   // typically 5h window
	Secondary *QuotaWindow `json:"secondary,omitempty"` // typically 7d (or 30d) window
	UpdatedAt int64        `json:"updated_at,omitempty"`
}

// MaxUsedPct returns the highest used percentage across available quota windows.
func (q QuotaInfo) MaxUsedPct() float64 {
	var max float64
	if q.Primary != nil && q.Primary.UsedPct > max {
		max = q.Primary.UsedPct
	}
	if q.Secondary != nil && q.Secondary.UsedPct > max {
		max = q.Secondary.UsedPct
	}
	return max
}

// QuotaProvider is an optional capability implemented by agent plugins that
// expose remaining account quota / rate-limit windows (e.g. Claude, Codex).
type QuotaProvider interface {
	FetchQuota(ctx context.Context, homeDirs []string) (*QuotaInfo, error)
}

// OSProcess holds metadata from a single-pass OS process table snapshot.
type OSProcess struct {
	PID   int
	PPID  int
	Comm  string
	Args  []string
	MemKB int64
	CWD   string
}

// ProcessSnapshot is a shared single-pass OS process snapshot passed to
// collectors and plugins to avoid repeated /proc walks.
type ProcessSnapshot struct {
	Processes map[int]OSProcess
	Children  map[int][]int
	// OpenFiles returns open file targets for pid on demand (cached per snapshot).
	OpenFilesFn func(pid int) []string
}

// OpenFiles returns the open file paths for pid, if supported by the snapshot.
func (s *ProcessSnapshot) OpenFiles(pid int) []string {
	if s == nil || s.OpenFilesFn == nil {
		return nil
	}
	return s.OpenFilesFn(pid)
}

// LiveMCPThread describes a single session/rollout thread handled by a live MCP server.
type LiveMCPThread struct {
	Path      string `json:"path,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"` // unix seconds
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Active    bool   `json:"active,omitempty"`
}

// LiveMCPServer describes a running agent MCP server process (e.g. codex mcp-server).
type LiveMCPServer struct {
	Agent          string          `json:"agent"`
	PID            int             `json:"pid"`
	PPID           int             `json:"ppid,omitempty"`
	ParentCLI      string          `json:"parent_cli,omitempty"`
	Profile        string          `json:"profile,omitempty"`
	Command        string          `json:"command,omitempty"`
	MemKB          int64           `json:"mem_kb,omitempty"`
	ActiveThreads  int             `json:"active_threads"`
	TotalThreads   int             `json:"total_threads"`
	LastActivityAt int64           `json:"last_activity_at,omitempty"` // unix seconds
	Threads        []LiveMCPThread `json:"threads,omitempty"`
}

// MCPActivityProvider is an optional capability implemented by agent plugins
// that can discover live MCP server processes and their active/total threads.
type MCPActivityProvider interface {
	CollectLiveMCPServers(ctx context.Context, snap *ProcessSnapshot) ([]LiveMCPServer, error)
}

// ContextWindowForModel returns the effective context window size in tokens.
// Matches abtop's heuristic: 1M when the model string explicitly tags "[1m]"
// or observed context exceeds 200k; otherwise 200k.
func ContextWindowForModel(transcriptModel, configuredModel string, maxContextTokens int64) int64 {
	tm := strings.ToLower(transcriptModel)
	cm := strings.ToLower(configuredModel)
	if strings.Contains(tm, "[1m]") || strings.Contains(cm, "[1m]") || maxContextTokens > 200_000 {
		return 1_000_000
	}
	return 200_000
}

var secretPrefixes = []string{
	"sk-ant-", "sk-proj-", "sk-or-", "sk_live_", "sk_test_", "rk_live_", "rk_test_",
	"ghp_", "gho_", "ghs_", "ghr_", "ghu_", "github_pat_", "glpat-",
	"xoxb-", "xoxp-", "xoxa-", "xoxr-", "AKIA", "ASIA",
}

// RedactSecrets scrubs common API key / token patterns from display strings.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, pref := range secretPrefixes {
		for {
			idx := strings.Index(out, pref)
			if idx < 0 {
				break
			}
			end := idx + len(pref)
			for end < len(out) {
				c := out[end]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
					end++
				} else {
					break
				}
			}
			if end == idx+len(pref) {
				break
			}
			out = out[:idx] + "[REDACTED]" + out[end:]
		}
	}
	return out
}

// SanitizeTerminalText strips control characters and Unicode bidi overrides
// from untrusted transcript text before rendering in the TUI.
func SanitizeTerminalText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

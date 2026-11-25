package model

import (
	"encoding/json"
	"strings"
	"time"
)

// Status represents a pane's or space's attention status, ordered by priority.
type Status int

const (
	StatusNeedsYou Status = iota
	StatusFailed
	StatusDone
	StatusWorking
	StatusQuiet
	StatusNewOutput
	StatusIdle
)

// Priority returns the sort priority (lower = more urgent).
func (s Status) Priority() int { return int(s) }

// Icon returns the glyph for the status.
func (s Status) Icon() string {
	switch s {
	case StatusNeedsYou:
		return "●"
	case StatusFailed:
		return "✗"
	case StatusDone:
		return "✓"
	case StatusWorking:
		return "◐"
	case StatusQuiet:
		return "◌"
	case StatusNewOutput:
		return "•"
	default:
		return "○"
	}
}

// Label returns the human-readable label for the status.
func (s Status) Label() string {
	switch s {
	case StatusNeedsYou:
		return "needs you"
	case StatusFailed:
		return "failed"
	case StatusDone:
		return "done"
	case StatusWorking:
		return "working"
	case StatusQuiet:
		return "quiet"
	case StatusNewOutput:
		return "new output"
	default:
		return "idle"
	}
}

// IsAttention reports whether the status requires immediate user attention.
func (s Status) IsAttention() bool {
	return s == StatusNeedsYou || s == StatusFailed
}

// AttentionFilter filters visible panes/spaces in the navigator.
type AttentionFilter int

const (
	FilterAll AttentionFilter = iota
	FilterNeeds
	FilterWorking
	FilterDone
	FilterIdle
)

// Label returns the filter pill label.
func (f AttentionFilter) Label() string {
	switch f {
	case FilterNeeds:
		return "blocked"
	case FilterWorking:
		return "working"
	case FilterDone:
		return "done"
	case FilterIdle:
		return "idle"
	default:
		return "all"
	}
}

// Matches reports whether status satisfies the filter.
func (f AttentionFilter) Matches(s Status) bool {
	switch f {
	case FilterNeeds:
		return s.IsAttention()
	case FilterWorking:
		return s == StatusWorking
	case FilterDone:
		return s == StatusDone
	case FilterIdle:
		return s == StatusIdle
	default:
		return true
	}
}

// AllFilters returns the ordered list of attention filters.
func AllFilters() []AttentionFilter {
	return []AttentionFilter{FilterAll, FilterNeeds, FilterWorking, FilterDone, FilterIdle}
}

// Counts tallies panes by Status.
type Counts struct {
	Needs     int
	Failed    int
	Working   int
	Done      int
	Quiet     int
	NewOutput int
	Idle      int
}

// Add increments the counter for s.
func (c *Counts) Add(s Status) {
	switch s {
	case StatusNeedsYou:
		c.Needs++
	case StatusFailed:
		c.Failed++
	case StatusDone:
		c.Done++
	case StatusWorking:
		c.Working++
	case StatusQuiet:
		c.Quiet++
	case StatusNewOutput:
		c.NewOutput++
	default:
		c.Idle++
	}
}

// AttentionTotal returns Needs + Failed.
func (c Counts) AttentionTotal() int { return c.Needs + c.Failed }

// Rollup returns the highest-priority status represented in Counts.
func (c Counts) Rollup() Status {
	switch {
	case c.Needs > 0:
		return StatusNeedsYou
	case c.Failed > 0:
		return StatusFailed
	case c.Done > 0:
		return StatusDone
	case c.Working > 0:
		return StatusWorking
	case c.Quiet > 0:
		return StatusQuiet
	case c.NewOutput > 0:
		return StatusNewOutput
	default:
		return StatusIdle
	}
}

// HookUsage carries token and estimated cost telemetry reported by a
// passive telemetry source (e.g. daemon_sse, router, otlp).
type HookUsage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
}

// TotalTokensOrDerived returns explicit TotalTokens or the derived sum.
func (u *HookUsage) TotalTokensOrDerived() int64 {
	if u == nil {
		return 0
	}
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens
}

// TotalTokensCount returns explicit TotalTokens or the derived sum
// (kept separate from the TotalTokens field for Go method/field rules).
func (u *HookUsage) TotalTokensCount() int64 {
	return u.TotalTokensOrDerived()
}

// HasData reports whether any token count or cost value is populated.
func (u *HookUsage) HasData() bool {
	if u == nil {
		return false
	}
	return u.InputTokens > 0 || u.OutputTokens > 0 || u.ReasoningTokens > 0 ||
		u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 || u.CostUSD > 0 || u.TotalTokens > 0
}

// HookToolExecution describes one tool call tracked by a rich companion plugin.
type HookToolExecution struct {
	CallID     string `json:"call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Input      string `json:"input,omitempty"`
	Status     string `json:"status,omitempty"`
	StartedMs  int64  `json:"started_ms,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// HookFileTouch describes one file touched by the agent.
type HookFileTouch struct {
	Path      string `json:"path,omitempty"`
	Action    string `json:"action,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	TsMs      int64  `json:"ts_ms,omitempty"`
}

// HookTodoItem describes one task-list item.
type HookTodoItem struct {
	ID       string `json:"id,omitempty"`
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// HookMCPServerStatus describes one MCP server status entry.
type HookMCPServerStatus struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Tools  int    `json:"tools,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HookLSPDiagnostics aggregates language-server diagnostics.
type HookLSPDiagnostics struct {
	Errors   int `json:"errors,omitempty"`
	Warnings int `json:"warnings,omitempty"`
	Files    int `json:"files,omitempty"`
}

// HookPendingInteraction describes a blocked permission/question awaiting input.
type HookPendingInteraction struct {
	ID          string   `json:"id,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
	TsMs        int64    `json:"ts_ms,omitempty"`
}

// HookSummary is the per-pane summary returned by `spaces __space-hooks`.
type HookSummary struct {
	Agent           string      `json:"agent"`
	App             string      `json:"app"`
	State           string      `json:"state"`
	Kind            string      `json:"kind,omitempty"`
	Event           string      `json:"event"`
	Source          string      `json:"source,omitempty"`
	TsMs            int64       `json:"ts_ms"`
	StateSinceMs    int64       `json:"state_since_ms"`
	TurnStartedMs   int64       `json:"turn_started_ms"`
	Text            string      `json:"text"`
	Title           string      `json:"title,omitempty"`
	Model           string      `json:"model,omitempty"`
	SessionID       string      `json:"session_id,omitempty"`
	CWD             string      `json:"cwd,omitempty"`
	Usage           *HookUsage  `json:"usage,omitempty"`
	ChildCount      int         `json:"child_count"`
	Background      int         `json:"background,omitempty"`
	FiniteActive    int         `json:"finite_active,omitempty"`
	ServiceActive   int         `json:"service_active,omitempty"`
	WaitingOn       string      `json:"waiting_on,omitempty"`
	Children        []HookChild `json:"children,omitempty"`
	BackgroundTasks []HookChild `json:"background_tasks,omitempty"`
	HasDetail       bool        `json:"has_detail"`
	// Rich v2 companion telemetry.
	PluginName          string                  `json:"plugin_name,omitempty"`
	PluginVersion       string                  `json:"plugin_version,omitempty"`
	PluginCapabilities  []string                `json:"plugin_capabilities,omitempty"`
	PluginTsMs          int64                   `json:"plugin_ts_ms,omitempty"`
	PID                 int                     `json:"pid,omitempty"`
	HarnessVersion      string                  `json:"harness_version,omitempty"`
	Provider            string                  `json:"provider,omitempty"`
	Effort              string                  `json:"effort,omitempty"`
	Mode                string                  `json:"mode,omitempty"`
	SandboxMode         string                  `json:"sandbox_mode,omitempty"`
	GitBranch           string                  `json:"git_branch,omitempty"`
	ContextTokens       int64                   `json:"context_tokens,omitempty"`
	ContextWindow       int64                   `json:"context_window,omitempty"`
	ContextPercent      float64                 `json:"context_percent,omitempty"`
	TurnCount           int                     `json:"turn_count,omitempty"`
	CompactionCount     int                     `json:"compaction_count,omitempty"`
	LastCompactionMs    int64                   `json:"last_compaction_ms,omitempty"`
	LastCompactionReason string                 `json:"last_compaction_reason,omitempty"`
	ToolCallCount       int                     `json:"tool_call_count,omitempty"`
	ToolErrorCount      int                     `json:"tool_error_count,omitempty"`
	RetryCount          int                     `json:"retry_count,omitempty"`
	RateLimited         bool                    `json:"rate_limited,omitempty"`
	RateLimitResetMs    int64                   `json:"rate_limit_reset_ms,omitempty"`
	RateLimitMessage    string                  `json:"rate_limit_message,omitempty"`
	ActiveTools         []HookToolExecution     `json:"active_tools,omitempty"`
	RecentTools         []HookToolExecution     `json:"recent_tools,omitempty"`
	FilesTouched        []HookFileTouch         `json:"files_touched,omitempty"`
	Todos               []HookTodoItem          `json:"todos,omitempty"`
	MCPServers          []HookMCPServerStatus   `json:"mcp_servers,omitempty"`
	LSPDiagnostics      *HookLSPDiagnostics     `json:"lsp_diagnostics,omitempty"`
	PendingPermission   *HookPendingInteraction `json:"pending_permission,omitempty"`
	LastError           string                  `json:"last_error,omitempty"`
}

// IsFreshPlugin reports whether this summary carries fresh plugin telemetry
// (nowMs - plugin_ts_ms within the working TTL for live states, or within
// the standard TTL for terminal states), so callers can skip expensive
// capture-pane scraping.
func (h *HookSummary) IsFreshPlugin(nowMs int64) bool {
	if h == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(h.Source), "plugin") {
		return false
	}
	ts := h.PluginTsMs
	if ts <= 0 {
		ts = h.TsMs
	}
	if ts <= 0 {
		return false
	}
	age := nowMs - ts
	if age < 0 {
		age = -age
	}
	const workingStaleMs = int64(120_000)
	const doneTTLMs = int64(30 * 60_000)
	const failedTTLMs = int64(30 * 60_000)
	const blockTTLMs = int64(30 * 60_000)
	const authTTLMs = int64(60 * 60_000)
	switch h.State {
	case "working", "idle", "":
		return age <= workingStaleMs
	case "blocked", "waiting":
		if h.IsAuthBlock() {
			since := h.StateSinceMs
			if since <= 0 {
				since = ts
			}
			d := nowMs - since
			if d < 0 {
				d = -d
			}
			return d <= authTTLMs
		}
		since := h.StateSinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		return d <= blockTTLMs
	case "done":
		since := h.StateSinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		return d <= doneTTLMs
	case "failed":
		since := h.StateSinceMs
		if since <= 0 {
			since = ts
		}
		d := nowMs - since
		if d < 0 {
			d = -d
		}
		return d <= failedTTLMs
	default:
		return age <= workingStaleMs
	}
}

// IsAuthBlock reports whether this hook represents an auth-required block.
func (h HookSummary) IsAuthBlock() bool {
	return h.State == "blocked" && h.Kind == "auth"
}

// HasBackgroundWork reports whether the pane has any tracked subagents,
// background commands, services, watchers, or scheduled wakeups.
func (h *HookSummary) HasBackgroundWork() bool {
	if h == nil {
		return false
	}
	return len(h.Children) > 0 || len(h.BackgroundTasks) > 0 || h.ChildCount > 0 || h.Background > 0
}

// HasActiveFiniteWork reports whether the pane has actively running finite
// background work (subagents or finite background commands) or is explicitly
// waiting on background tasks to finish. Long-running services, watchers, and
// scheduled wakeups do not count as active finite work.
func (h *HookSummary) HasActiveFiniteWork() bool {
	if h == nil {
		return false
	}
	if h.FiniteActive > 0 || strings.TrimSpace(h.WaitingOn) != "" {
		return true
	}
	for _, item := range h.AllBackgroundWork() {
		if item.IsActive() && item.IsFinite() {
			return true
		}
	}
	return false
}

// HasOnlyLocalCommandBackgroundWork reports whether the pane's active finite
// background work consists solely of local shell/bash commands (with no active
// subagents). Used to avoid pinning a pane in Working forever when a harness
// fails to emit a TaskCompleted hook for a finished local_bash command and the
// viewport is visibly idle past hookWorkingStaleMs.
func (h *HookSummary) HasOnlyLocalCommandBackgroundWork() bool {
	if h == nil || !h.HasActiveFiniteWork() {
		return false
	}
	if strings.Contains(strings.ToLower(h.WaitingOn), "subagent") {
		return false
	}
	for _, ch := range h.Children {
		if ch.IsActive() && ch.IsFinite() {
			return false
		}
	}
	hasLocalCmd := false
	for _, item := range h.BackgroundTasks {
		if !item.IsActive() || !item.IsFinite() {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(item.Role))
		typ := strings.ToLower(strings.TrimSpace(item.Kind))
		if role == "command" || typ == "local_bash" {
			hasLocalCmd = true
		} else {
			return false
		}
	}
	return hasLocalCmd
}

// AllBackgroundWork returns deduplicated subagents and background tasks ordered
// with active finite jobs first, then active services/watchers/schedules, then
// finished outcomes (newest first within each group).
func (h *HookSummary) AllBackgroundWork() []HookChild {
	if h == nil {
		return nil
	}
	return mergeHookBackgroundItems(h.Children, h.BackgroundTasks)
}

// HookTool describes the latest tool invocation in HookDetail.
type HookTool struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
}

// HookChild describes a subagent, background command, service, watcher, or
// scheduled wakeup associated with a pane.
type HookChild struct {
	ID       string `json:"id"`
	Kind     string `json:"type"`
	Role     string `json:"kind,omitempty"`     // "subagent" | "command" | "service" | "watcher" | "schedule"
	Category string `json:"category,omitempty"` // "finite" | "service" | "watcher" | "schedule"
	State    string `json:"state"`              // "working" | "done" | "failed" | "stopped"
	Outcome  string `json:"outcome,omitempty"`
	Command  string `json:"command,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	SinceMs  int64  `json:"since_ms"`
	TsMs     int64  `json:"ts_ms,omitempty"`
	EndedMs  int64  `json:"ended_ms,omitempty"`
	Text     string `json:"text"`
}

// EffectiveCategory returns the lifecycle category ("finite", "service",
// "watcher", or "schedule"), defaulting to "finite".
func (c HookChild) EffectiveCategory() string {
	switch strings.ToLower(strings.TrimSpace(c.Category)) {
	case "service", "watcher", "schedule", "finite":
		return strings.ToLower(strings.TrimSpace(c.Category))
	default:
		return "finite"
	}
}

// EffectiveRole returns the item kind ("subagent", "command", "service",
// "watcher", or "schedule"), defaulting to "subagent".
func (c HookChild) EffectiveRole() string {
	if r := strings.ToLower(strings.TrimSpace(c.Role)); r != "" {
		return r
	}
	switch c.EffectiveCategory() {
	case "service":
		return "service"
	case "watcher":
		return "watcher"
	case "schedule":
		return "schedule"
	default:
		return "subagent"
	}
}

// IsActive reports whether this background item is still running.
func (c HookChild) IsActive() bool {
	switch strings.ToLower(strings.TrimSpace(c.State)) {
	case "", "working", "running", "pending", "in_progress", "started", "active", "scheduled", "waiting":
		return true
	default:
		return false
	}
}

// IsFinite reports whether this item is finite work (blocks turn completion).
func (c HookChild) IsFinite() bool {
	return c.EffectiveCategory() == "finite"
}

// ElapsedMs returns the item's elapsed runtime in milliseconds: frozen at
// EndedMs-SinceMs (or TsMs-SinceMs) once terminal, or live nowMs-SinceMs while
// active.
func (c HookChild) ElapsedMs(nowMs int64) int64 {
	if c.SinceMs <= 0 {
		return 0
	}
	if !c.IsActive() {
		end := c.EndedMs
		if end <= 0 {
			end = c.TsMs
		}
		if end > c.SinceMs {
			return end - c.SinceMs
		}
		return 0
	}
	if nowMs > c.SinceMs {
		return nowMs - c.SinceMs
	}
	return 0
}

// HookDetail is the full record returned by `spaces __space-hooks --detail`.
type HookDetail struct {
	HookSummary
	Prompt          string                 `json:"prompt,omitempty"`
	Tool            *HookTool              `json:"tool,omitempty"`
	Reply           string                 `json:"reply,omitempty"`
	Children        []HookChild            `json:"children,omitempty"`
	BackgroundTasks []HookChild            `json:"background_tasks,omitempty"`
	Background      int                    `json:"background"`
	Extras          map[string]any         `json:"extras,omitempty"`
}

// AllBackgroundWork returns deduplicated subagents and background tasks for
// the detail view, merging both HookDetail and embedded HookSummary slices.
func (d *HookDetail) AllBackgroundWork() []HookChild {
	if d == nil {
		return nil
	}
	ch := d.Children
	if len(ch) == 0 {
		ch = d.HookSummary.Children
	}
	bg := d.BackgroundTasks
	if len(bg) == 0 {
		bg = d.HookSummary.BackgroundTasks
	}
	return mergeHookBackgroundItems(ch, bg)
}

func mergeHookBackgroundItems(children, bgTasks []HookChild) []HookChild {
	if len(children) == 0 && len(bgTasks) == 0 {
		return nil
	}
	out := make([]HookChild, 0, len(children)+len(bgTasks))
	seen := make(map[string]int, len(children)+len(bgTasks))
	add := func(item HookChild, defaultRole string) {
		if strings.TrimSpace(item.Role) == "" {
			if item.EffectiveCategory() == "finite" {
				item.Role = defaultRole
			} else {
				item.Role = item.EffectiveCategory()
			}
		}
		if strings.TrimSpace(item.Category) == "" {
			item.Category = "finite"
		}
		id := strings.TrimSpace(item.ID)
		if id != "" {
			if idx, ok := seen[id]; ok {
				// Prefer the richer/newer entry.
				if item.TsMs >= out[idx].TsMs {
					if out[idx].SinceMs > 0 && (item.SinceMs <= 0 || out[idx].SinceMs < item.SinceMs) {
						item.SinceMs = out[idx].SinceMs
					}
					out[idx] = item
				}
				return
			}
			seen[id] = len(out)
		}
		out = append(out, item)
	}
	// Add children first, then background tasks (which may carry richer command/category).
	for _, c := range children {
		add(c, "subagent")
	}
	for _, b := range bgTasks {
		add(b, "command")
	}
	// Stable group sort: active finite (0) -> active long-running (1) -> terminal outcomes (2).
	groupOf := func(c HookChild) int {
		if !c.IsActive() {
			return 2
		}
		if c.IsFinite() {
			return 0
		}
		return 1
	}
	for i := 1; i < len(out); i++ {
		cur := out[i]
		gCur := groupOf(cur)
		j := i - 1
		for j >= 0 {
			gPrev := groupOf(out[j])
			if gPrev < gCur {
				break
			}
			if gPrev == gCur && out[j].SinceMs <= cur.SinceMs {
				break
			}
			out[j+1] = out[j]
			j--
		}
		out[j+1] = cur
	}
	return out
}

// OutcomeEntry represents one recent completion or failure recorded across spaces.
type OutcomeEntry struct {
	Session   string `json:"session"`
	Pane      string `json:"pane,omitempty"`
	Agent     string `json:"agent"`
	App       string `json:"app,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Outcome   string `json:"outcome"` // "done" or "failed"
	Text      string `json:"text,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	TsMs      int64  `json:"ts_ms"`
}

// Status maps OutcomeEntry.Outcome to StatusFailed or StatusDone.
func (o OutcomeEntry) Status() Status {
	if o.Outcome == "failed" {
		return StatusFailed
	}
	return StatusDone
}

// DisplayApp returns the display label for the outcome's agent.
func (o OutcomeEntry) DisplayApp() string {
	if a := strings.TrimSpace(o.App); a != "" {
		return a
	}
	return strings.TrimSpace(o.Agent)
}

// HookSnapshot is the top-level JSON returned by `spaces __space-hooks`.
type HookSnapshot struct {
	OK           bool                              `json:"ok"`
	Version      int                               `json:"version"`
	NowMs        int64                             `json:"now_ms"`
	SpoolPending int                               `json:"spool_pending"`
	Panes        map[string]map[string]HookSummary `json:"panes"`
	Outcomes     []OutcomeEntry                    `json:"outcomes,omitempty"`
}

// HookDetailSnapshot is the top-level JSON returned by `spaces __space-hooks --detail`.
type HookDetailSnapshot struct {
	OK      bool        `json:"ok"`
	Version int         `json:"version"`
	Record  *HookDetail `json:"record"`
}

// HarnessProfile describes one configured profile for a launchable harness.
type HarnessProfile struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Source      string `json:"source,omitempty"`
	Agent       string `json:"agent,omitempty"`
}

// UnmarshalJSON accepts both camelCase (`displayName`, as written to
// ~/.harness/harnesses.json) and legacy snake_case (`display_name`).
func (p *HarnessProfile) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name              string `json:"name"`
		DisplayName       string `json:"displayName"`
		LegacyDisplayName string `json:"display_name"`
		Source            string `json:"source"`
		Agent             string `json:"agent"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Name = raw.Name
	p.DisplayName = raw.DisplayName
	if p.DisplayName == "" {
		p.DisplayName = raw.LegacyDisplayName
	}
	p.Source = raw.Source
	p.Agent = raw.Agent
	return nil
}

// HarnessEntry describes one launchable agent from ~/.harness/harnesses.json.
type HarnessEntry struct {
	Name        string           `json:"name"`
	Binary      string           `json:"binary,omitempty"`
	BinaryState string           `json:"binaryState,omitempty"`
	Description string           `json:"note,omitempty"`
	Profiles    []HarnessProfile `json:"profiles,omitempty"`
}

// UnmarshalJSON accepts both `note` (as written to ~/.harness/harnesses.json)
// and legacy `description`.
func (e *HarnessEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string           `json:"name"`
		Binary      string           `json:"binary"`
		BinaryState string           `json:"binaryState"`
		Note        string           `json:"note"`
		Description string           `json:"description"`
		Profiles    []HarnessProfile `json:"profiles"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Name = raw.Name
	e.Binary = raw.Binary
	e.BinaryState = raw.BinaryState
	e.Description = raw.Note
	if e.Description == "" {
		e.Description = raw.Description
	}
	e.Profiles = raw.Profiles
	return nil
}

// IsLaunchable reports whether this harness has a working default binary or at
// least one configured profile.
func (e HarnessEntry) IsLaunchable() bool {
	if len(e.Profiles) > 0 {
		return true
	}
	state := strings.ToLower(strings.TrimSpace(e.BinaryState))
	return state == "" || state == "ok"
}

// HarnessFile is the JSON inventory stored at ~/.harness/harnesses.json.
type HarnessFile struct {
	Version       int            `json:"version,omitempty"`
	SchemaVersion int            `json:"schema_version,omitempty"`
	GeneratedAt   int64          `json:"generatedAt,omitempty"`
	Agents        []HarnessEntry `json:"agents"`
}

// Window represents one tmux window inside a Space.
type Window struct {
	ID              string // e.g. "@1"
	Index           int
	Name            string
	Active          bool
	Last            bool
	Zoomed          bool
	Bell            bool
	Activity        bool
	Silence         bool
	Width           int
	Height          int
	ActivityAt      time.Time
	Layout          string
	WorktreePath    string
	WorktreeName    string
	WorktreeBase    string
	WorktreeManaged bool
}

// CheckState represents aggregated CI/CD status for a PR or branch commit.
type CheckState string

const (
	CheckNone    CheckState = ""
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
	CheckPending CheckState = "pending"
)

// CheckSummary holds aggregated CI check status and optional metadata.
type CheckSummary struct {
	State       CheckState `json:"state"`
	Passed      int        `json:"passed,omitempty"`
	Total       int        `json:"total,omitempty"`
	FailingName string     `json:"failing_name,omitempty"`
}

// PRSummary holds GitHub pull request metadata for a branch.
type PRSummary struct {
	Number  int    `json:"number"`
	Title   string `json:"title,omitempty"`
	State   string `json:"state"` // OPEN, MERGED, CLOSED
	IsDraft bool   `json:"is_draft,omitempty"`
	URL     string `json:"url,omitempty"`
}

// GitInfo holds per-directory git branch, worktree, diff stats, and GitHub PR/CI status.
type GitInfo struct {
	RepoKey            string        `json:"repo_key,omitempty"`
	RepoRoot           string        `json:"repo_root,omitempty"`
	MainRepoRoot       string        `json:"main_repo_root,omitempty"`
	RepoName           string        `json:"repo_name,omitempty"`
	IsWorktree         bool          `json:"is_worktree,omitempty"`
	WorktreeName       string        `json:"worktree_name,omitempty"`
	Managed            bool          `json:"managed,omitempty"`
	Branch             string        `json:"branch,omitempty"`
	BaseBranch         string        `json:"base_branch,omitempty"`
	Ahead              int           `json:"ahead,omitempty"`
	Behind             int           `json:"behind,omitempty"`
	IsDirty            bool          `json:"is_dirty,omitempty"`
	IsRebasing         bool          `json:"is_rebasing,omitempty"`
	HasConflict        bool          `json:"has_conflict,omitempty"`
	LinesAdded         int           `json:"lines_added,omitempty"`         // committed changes (base...HEAD)
	LinesRemoved       int           `json:"lines_removed,omitempty"`       // committed changes (base...HEAD)
	UncommittedAdded   int           `json:"uncommitted_added,omitempty"`   // working tree + untracked
	UncommittedRemoved int           `json:"uncommitted_removed,omitempty"` // working tree
	PR                 *PRSummary    `json:"pr,omitempty"`
	Checks             *CheckSummary `json:"checks,omitempty"`
}

// ShouldShowChecks reports whether branch/PR CI check status should be rendered.
// On default branches (main/master) without a PR, only failing or pending checks
// are shown to avoid noisy stale green badges.
func (g *GitInfo) ShouldShowChecks() bool {
	if g == nil || g.Checks == nil || g.Checks.State == CheckNone {
		return false
	}
	if g.PR != nil {
		return true
	}
	if g.Branch == "main" || g.Branch == "master" {
		return g.Checks.State == CheckFailure || g.Checks.State == CheckPending
	}
	return true
}

// Clone returns a deep copy of g safe to mutate or share across goroutines.
func (g *GitInfo) Clone() *GitInfo {
	if g == nil {
		return nil
	}
	cp := *g
	if g.PR != nil {
		pr := *g.PR
		cp.PR = &pr
	}
	if g.Checks != nil {
		ch := *g.Checks
		cp.Checks = &ch
	}
	return &cp
}

// Pane represents one tmux pane inside a Space.
type Pane struct {
	ID                string // tmux pane id, e.g. "%12"
	NumericID         int    // numeric part of ID (12) for hook store lookups
	PaneIndex         int    // #{pane_index}
	WindowID          string // e.g. "@1"
	WindowIndex       int
	WindowName        string
	Title             string
	ConversationTitle string // resolved from hooks / session store
	BaseConvTitle     string // #{@spaces_title} baseline
	ScreenTitle       string // resolved from screen reader
	ResumeSession     string // #{@spaces_resume_session}
	Profile           string // #{@spaces_profile}
	Command           string // #{pane_current_command}
	StartCommand      string // #{pane_start_command}
	ForegroundCmd     string // deepest live child cmdline from /proc (when available)
	LaunchCommand     []string
	ChildProcessCount int    // live descendant process count under PID
	App               string // resolved display app name
	Status            Status
	BaseStatus        Status
	Text              string
	BaseText          string
	Kind              string // #{@spaces_kind} (e.g. "auth", "permission")
	Source            string // #{@spaces_source} (e.g. "osc7501", "hook")
	Hook              *HookSummary
	Git               *GitInfo
	CWD               string
	AgeMs             int64
	BaseAgeMs         int64
	Width             int
	Height            int
	IsActive          bool
	WindowActive      bool
	InMode            bool
	Synchronized      bool
	IsDead            bool
	DeadStatus        int
	HasSpacesMeta     bool
	ReturnedToShell   bool
	PID               int
}

// IsAgent reports whether this pane represents a coding agent (as opposed to a
// plain shell or non-agent terminal). Used by the Attention Inbox.
func (p Pane) IsAgent() bool {
	if p.IsDead && p.Status != StatusFailed {
		return false
	}
	if p.Hook != nil {
		return true
	}
	if p.HasSpacesMeta && !isShellApp(p.App) && p.App != "" && p.App != "terminal" {
		return true
	}
	if IsKnownAgent(p.App) {
		return true
	}
	if cmd := CommandName(p.Command); cmd != "" && IsKnownAgent(cmd) {
		return true
	}
	return false
}

// Space represents one tmux session.
type Space struct {
	Name             string
	ID               string // e.g. "$0"
	IsCurrent        bool
	Windows          []Window
	Panes            []Pane
	CWD              string
	Git              *GitInfo
	WorktreePath     string
	WorktreeName     string
	WorktreeBase     string
	WorktreeManaged  bool
	Counts           Counts
	Status           Status
	ConnectedClients int
	CreatedAt        time.Time
}

// ExitedSpace represents a persisted space snapshot whose tmux session is not
// currently running and can be resurrected.
type ExitedSpace struct {
	Name        string
	SavedAt     time.Time
	CWD         string
	WindowCount int
	PaneCount   int
	Agents      []string
}

// WorktreeEntry describes a git worktree for UI display, selection, and lifecycle management.
type WorktreeEntry struct {
	Path            string
	Name            string
	Branch          string
	HeadSHA         string
	BaseBranch      string
	RepoKey         string
	MainRepoRoot    string
	RepoName        string
	IsMain          bool
	IsCurrent       bool
	IsDetached      bool
	IsLocked        bool
	IsPrunable      bool
	IsDirty         bool
	Managed         bool
	NameSource      string
	CreatedAtMs     int64
	UnmergedCommits int
	OpenSpaces      []string // live tmux space names currently using this worktree
	OpenWindowIDs   []string // live tmux window IDs currently using this worktree
	OpenWindowCount int
	Git             *GitInfo
}

// WorktreeContext holds discovered git repository and worktree state for a folder,
// used by the mod+c New Tab and New Space dialogs.
type WorktreeContext struct {
	InRepo        bool
	RepoKey       string
	MainRepoRoot  string
	RepoName      string
	WorktreeRoot  string
	DefaultBase   string
	CurrentBranch string
	CurrentPath   string
	HasCommits    bool
	Worktrees     []WorktreeEntry
	Branches      []string
	AutoBranch    string
	AutoDirName   string
	AutoSource    string
	AutoRule      string
}

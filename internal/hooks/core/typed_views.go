package core

import "strings"

// Tool describes the latest tool invocation in a PaneDetail.
type Tool struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
}

// Child describes a subagent, background command, service, watcher, or
// scheduled wakeup associated with a pane.
type Child struct {
	ID       string `json:"id"`
	Kind     string `json:"type"`
	Role     string `json:"kind,omitempty"`
	Category string `json:"category,omitempty"`
	State    string `json:"state"`
	Outcome  string `json:"outcome,omitempty"`
	Command  string `json:"command,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	SinceMs  int64  `json:"since_ms"`
	TsMs     int64  `json:"ts_ms,omitempty"`
	EndedMs  int64  `json:"ended_ms,omitempty"`
	Text     string `json:"text"`
}

// Usage describes token and cost telemetry for a pane.
type Usage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
}

// TotalTokensOrDerived returns explicit TotalTokens or the derived sum.
func (u *Usage) TotalTokensOrDerived() int64 {
	if u == nil {
		return 0
	}
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens
}

// ToolExecution describes one tool call tracked by a rich companion plugin.
type ToolExecution struct {
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     string `json:"input,omitempty"`
	Status    string `json:"status,omitempty"`
	StartedMs int64  `json:"started_ms,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// FileTouch describes one file touched by the agent.
type FileTouch struct {
	Path      string `json:"path,omitempty"`
	Action    string `json:"action,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	TsMs      int64  `json:"ts_ms,omitempty"`
}

// TodoItem describes one task-list item tracked by the agent.
type TodoItem struct {
	ID       string `json:"id,omitempty"`
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// MCPServerStatus describes one MCP server status entry.
type MCPServerStatus struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Tools  int    `json:"tools,omitempty"`
	Error  string `json:"error,omitempty"`
}

// LSPDiagnostics aggregates language-server diagnostics.
type LSPDiagnostics struct {
	Errors   int `json:"errors,omitempty"`
	Warnings int `json:"warnings,omitempty"`
	Files    int `json:"files,omitempty"`
}

// PendingInteraction describes a blocked permission/question awaiting input.
type PendingInteraction struct {
	ID          string   `json:"id,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
	TsMs        int64    `json:"ts_ms,omitempty"`
}

// PaneSummary is the typed per-pane summary view.
type PaneSummary struct {
	Agent           string  `json:"agent"`
	App             string  `json:"app"`
	Source          string  `json:"source,omitempty"`
	State           string  `json:"state"`
	Kind            string  `json:"kind,omitempty"`
	Event           string  `json:"event"`
	TsMs            int64   `json:"ts_ms"`
	StateSinceMs    int64   `json:"state_since_ms"`
	TurnStartedMs   int64   `json:"turn_started_ms"`
	Text            string  `json:"text"`
	Title           string  `json:"title,omitempty"`
	Model           string  `json:"model,omitempty"`
	SessionID       string  `json:"session_id,omitempty"`
	CWD             string  `json:"cwd,omitempty"`
	Usage           *Usage  `json:"usage,omitempty"`
	ChildCount      int     `json:"child_count"`
	Background      int     `json:"background,omitempty"`
	FiniteActive    int     `json:"finite_active,omitempty"`
	ServiceActive   int     `json:"service_active,omitempty"`
	WaitingOn       string  `json:"waiting_on,omitempty"`
	Children        []Child `json:"children,omitempty"`
	BackgroundTasks []Child `json:"background_tasks,omitempty"`
	HasDetail       bool    `json:"has_detail"`
	// Rich v2 companion telemetry.
	PluginName         string              `json:"plugin_name,omitempty"`
	PluginVersion      string              `json:"plugin_version,omitempty"`
	PluginCapabilities []string            `json:"plugin_capabilities,omitempty"`
	PluginTsMs         int64               `json:"plugin_ts_ms,omitempty"`
	PID                int                 `json:"pid,omitempty"`
	HarnessVersion     string              `json:"harness_version,omitempty"`
	Provider           string              `json:"provider,omitempty"`
	Effort             string              `json:"effort,omitempty"`
	Mode               string              `json:"mode,omitempty"`
	SandboxMode        string              `json:"sandbox_mode,omitempty"`
	GitBranch          string              `json:"git_branch,omitempty"`
	ContextTokens      int64               `json:"context_tokens,omitempty"`
	ContextWindow      int64               `json:"context_window,omitempty"`
	ContextPercent     float64             `json:"context_percent,omitempty"`
	TurnCount          int                 `json:"turn_count,omitempty"`
	CompactionCount    int                 `json:"compaction_count,omitempty"`
	LastCompactionMs   int64               `json:"last_compaction_ms,omitempty"`
	LastCompactionReason string            `json:"last_compaction_reason,omitempty"`
	ToolCallCount      int                 `json:"tool_call_count,omitempty"`
	ToolErrorCount     int                 `json:"tool_error_count,omitempty"`
	RetryCount         int                 `json:"retry_count,omitempty"`
	RateLimited        bool                `json:"rate_limited,omitempty"`
	RateLimitResetMs   int64               `json:"rate_limit_reset_ms,omitempty"`
	RateLimitMessage   string              `json:"rate_limit_message,omitempty"`
	ActiveTools        []ToolExecution     `json:"active_tools,omitempty"`
	RecentTools        []ToolExecution     `json:"recent_tools,omitempty"`
	FilesTouched       []FileTouch         `json:"files_touched,omitempty"`
	Todos              []TodoItem          `json:"todos,omitempty"`
	MCPServers         []MCPServerStatus   `json:"mcp_servers,omitempty"`
	LSPDiagnostics     *LSPDiagnostics     `json:"lsp_diagnostics,omitempty"`
	PendingPermission  *PendingInteraction `json:"pending_permission,omitempty"`
	LastError          string              `json:"last_error,omitempty"`
}

// PaneDetail is the typed per-pane detail view.
type PaneDetail struct {
	PaneSummary
	Prompt string `json:"prompt,omitempty"`
	Tool   *Tool  `json:"tool,omitempty"`
	Reply  string `json:"reply,omitempty"`
	Extras map[string]any `json:"extras,omitempty"`
}

// Outcome is one recent completion or failure entry.
type Outcome struct {
	Session   string `json:"session"`
	Pane      string `json:"pane,omitempty"`
	Agent     string `json:"agent"`
	App       string `json:"app,omitempty"`
	Profile   string `json:"profile,omitempty"`
	Outcome   string `json:"outcome"`
	Text      string `json:"text,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	TsMs      int64  `json:"ts_ms"`
}

// Snapshot is the typed top-level hook summary view.
type Snapshot struct {
	OK           bool                              `json:"ok"`
	Version      int                               `json:"version"`
	NowMs        int64                             `json:"now_ms"`
	SpoolPending int                               `json:"spool_pending"`
	Panes        map[string]map[string]PaneSummary `json:"panes"`
	Outcomes     []Outcome                         `json:"outcomes,omitempty"`
}

// DetailSnapshot is the typed top-level hook detail view.
type DetailSnapshot struct {
	OK      bool        `json:"ok"`
	Version int         `json:"version"`
	Record  *PaneDetail `json:"record"`
}

func decodeChildren(raw any) []Child {
	slice, ok := raw.([]any)
	if !ok || len(slice) == 0 {
		return nil
	}
	out := make([]Child, 0, len(slice))
	for _, item := range slice {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Child{
			ID:       stringKey(m["id"]),
			Kind:     stringKey(m["type"]),
			Role:     stringKey(m["kind"]),
			Category: stringKey(m["category"]),
			State:    stringKey(m["state"]),
			Outcome:  stringKey(m["outcome"]),
			Command:  stringKey(m["command"]),
			Schedule: stringKey(m["schedule"]),
			SinceMs:  toInt64(m["since_ms"]),
			TsMs:     toInt64(m["ts_ms"]),
			EndedMs:  toInt64(m["ended_ms"]),
			Text:     stringKey(m["text"]),
		})
	}
	return out
}

func decodeUsage(raw any) *Usage {
	m := normalizeUsage(raw)
	if m == nil {
		return nil
	}
	cost, _ := toFloat64(m["cost_usd"])
	total := toInt64(m["total_tokens"])
	if total <= 0 {
		total = toInt64(m["input_tokens"]) + toInt64(m["output_tokens"]) + toInt64(m["reasoning_tokens"])
		if total <= 0 {
			total = 0
		} else {
			// Only set derived total when some token data exists; keep zero otherwise.
		}
		var explicit int64
		_ = explicit
	}
	u := &Usage{
		InputTokens:      toInt64(m["input_tokens"]),
		OutputTokens:     toInt64(m["output_tokens"]),
		ReasoningTokens:  toInt64(m["reasoning_tokens"]),
		CacheReadTokens:  toInt64(m["cache_read_tokens"]),
		CacheWriteTokens: toInt64(m["cache_write_tokens"]),
		CostUSD:          cost,
		TotalTokens:      total,
	}
	if u.TotalTokens == 0 && toInt64(m["total_tokens"]) == 0 {
		// Preserve explicit-zero semantics: leave TotalTokens unset (omitted JSON).
		u.TotalTokens = total
		if total == 0 {
			// keep zero; HasData still works via other fields
		}
	}
	return u
}

func decodeToolExecutions(raw any) []ToolExecution {
	sl, ok := raw.([]any)
	if !ok || len(sl) == 0 {
		return nil
	}
	out := make([]ToolExecution, 0, len(sl))
	for _, item := range sl {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, ToolExecution{
			CallID:     stringKey(m["call_id"]),
			Name:       stringKey(m["name"]),
			Input:      stringKey(m["input"]),
			Status:     stringKey(m["status"]),
			StartedMs:  toInt64(m["started_ms"]),
			DurationMs: toInt64(m["duration_ms"]),
			Error:      stringKey(m["error"]),
		})
	}
	return out
}

func decodeFileTouches(raw any) []FileTouch {
	sl, ok := raw.([]any)
	if !ok || len(sl) == 0 {
		return nil
	}
	out := make([]FileTouch, 0, len(sl))
	for _, item := range sl {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, FileTouch{
			Path:      stringKey(m["path"]),
			Action:    stringKey(m["action"]),
			Additions: int(toInt64(m["additions"])),
			Deletions: int(toInt64(m["deletions"])),
			TsMs:      toInt64(m["ts_ms"]),
		})
	}
	return out
}

func decodeTodos(raw any) []TodoItem {
	sl, ok := raw.([]any)
	if !ok || len(sl) == 0 {
		return nil
	}
	out := make([]TodoItem, 0, len(sl))
	for _, item := range sl {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, TodoItem{
			ID:       stringKey(m["id"]),
			Content:  stringKey(m["content"]),
			Status:   stringKey(m["status"]),
			Priority: stringKey(m["priority"]),
		})
	}
	return out
}

func decodeMCPServers(raw any) []MCPServerStatus {
	sl, ok := raw.([]any)
	if !ok || len(sl) == 0 {
		return nil
	}
	out := make([]MCPServerStatus, 0, len(sl))
	for _, item := range sl {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		toolsVal := 0
		switch t := m["tools"].(type) {
		case []any:
			toolsVal = len(t)
		default:
			toolsVal = int(toInt64(m["tools"]))
		}
		out = append(out, MCPServerStatus{
			Name:   stringKey(m["name"]),
			Status: stringKey(m["status"]),
			Tools:  toolsVal,
			Error:  stringKey(m["error"]),
		})
	}
	return out
}

func decodeLSP(raw any) *LSPDiagnostics {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	d := &LSPDiagnostics{
		Errors:   int(toInt64(m["errors"])),
		Warnings: int(toInt64(m["warnings"])),
		Files:    int(toInt64(m["files"])),
	}
	if d.Errors == 0 && d.Warnings == 0 && d.Files == 0 {
		return nil
	}
	return d
}

func decodePendingPermission(raw any) *PendingInteraction {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	pi := &PendingInteraction{
		ID:          stringKey(m["id"]),
		Kind:        stringKey(m["kind"]),
		Tool:        stringKey(m["tool"]),
		Title:       stringKey(m["title"]),
		Description: stringKey(m["description"]),
		TsMs:        toInt64(m["ts_ms"]),
	}
	if opts, ok := m["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				pi.Options = append(pi.Options, s)
			} else if s := stringKey(o); s != "" {
				pi.Options = append(pi.Options, s)
			}
		}
	}
	if pi.ID == "" && pi.Tool == "" && pi.Title == "" && pi.Description == "" {
		return nil
	}
	return pi
}

func decodeCapabilities(raw any) []string {
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringKey(item); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

func typedSummaryRecord(record map[string]any) PaneSummary {
	m := summaryRecord(record)
	return PaneSummary{
		Agent:           stringKey(m["agent"]),
		App:             stringKey(m["app"]),
		Source:          stringKey(m["source"]),
		State:           stringKey(m["state"]),
		Kind:            stringKey(m["kind"]),
		Event:           stringKey(m["event"]),
		TsMs:            toInt64(m["ts_ms"]),
		StateSinceMs:    toInt64(m["state_since_ms"]),
		TurnStartedMs:   toInt64(m["turn_started_ms"]),
		Text:            stringKey(m["text"]),
		Title:           stringKey(m["title"]),
		Model:           stringKey(m["model"]),
		SessionID:       stringKey(m["session_id"]),
		CWD:             stringKey(m["cwd"]),
		Usage:           decodeUsage(m["usage"]),
		ChildCount:      int(toInt64(m["child_count"])),
		Background:      int(toInt64(m["background"])),
		FiniteActive:    int(toInt64(m["finite_active"])),
		ServiceActive:   int(toInt64(m["service_active"])),
		WaitingOn:       stringKey(m["waiting_on"]),
		Children:        decodeChildren(m["children"]),
		BackgroundTasks: decodeChildren(m["background_tasks"]),
		HasDetail:       truthy(m["has_detail"]),
		PluginName:         stringKey(m["plugin_name"]),
		PluginVersion:      stringKey(m["plugin_version"]),
		PluginCapabilities: decodeCapabilities(m["plugin_capabilities"]),
		PluginTsMs:         toInt64(m["plugin_ts_ms"]),
		PID:                int(toInt64(m["pid"])),
		HarnessVersion:     stringKey(m["harness_version"]),
		Provider:           stringKey(m["provider"]),
		Effort:             stringKey(m["effort"]),
		Mode:               stringKey(m["mode"]),
		SandboxMode:        stringKey(m["sandbox_mode"]),
		GitBranch:          stringKey(m["git_branch"]),
		ContextTokens:      toInt64(m["context_tokens"]),
		ContextWindow:      toInt64(m["context_window"]),
		ContextPercent:     float64From(m["context_percent"]),
		TurnCount:          int(toInt64(m["turn_count"])),
		CompactionCount:    int(toInt64(m["compaction_count"])),
		LastCompactionMs:   toInt64(m["last_compaction_ms"]),
		LastCompactionReason: stringKey(m["last_compaction_reason"]),
		ToolCallCount:      int(toInt64(m["tool_call_count"])),
		ToolErrorCount:     int(toInt64(m["tool_error_count"])),
		RetryCount:         int(toInt64(m["retry_count"])),
		RateLimited:        parseBoolField(m, "rate_limited"),
		RateLimitResetMs:   toInt64(m["rate_limit_reset_ms"]),
		RateLimitMessage:   stringKey(m["rate_limit_message"]),
		ActiveTools:        decodeToolExecutions(m["active_tools"]),
		RecentTools:        decodeToolExecutions(m["recent_tools"]),
		FilesTouched:       decodeFileTouches(m["files_touched"]),
		Todos:              decodeTodos(m["todos"]),
		MCPServers:         decodeMCPServers(m["mcp_servers"]),
		LSPDiagnostics:     decodeLSP(m["lsp_diagnostics"]),
		PendingPermission:  decodePendingPermission(m["pending_permission"]),
		LastError:          stringKey(m["last_error"]),
	}
}

func float64From(v any) float64 {
	if f, ok := toFloat64(v); ok {
		return f
	}
	return 0
}

func typedSummaryOutcome(record map[string]any) Outcome {
	m := summaryOutcome(record)
	return Outcome{
		Session:   stringKey(m["session"]),
		Pane:      stringKey(m["pane"]),
		Agent:     stringKey(m["agent"]),
		App:       stringKey(m["app"]),
		Profile:   stringKey(m["profile"]),
		Outcome:   stringKey(m["outcome"]),
		Text:      stringKey(m["text"]),
		Title:     stringKey(m["title"]),
		Model:     stringKey(m["model"]),
		SessionID: stringKey(m["session_id"]),
		CWD:       stringKey(m["cwd"]),
		TsMs:      toInt64(m["ts_ms"]),
	}
}

// Summary returns the typed hook store summary without JSON round-tripping.
func Summary() Snapshot {
	store, pending := mergedView()
	panes := make(map[string]map[string]PaneSummary)
	if storePanes, ok := store["panes"].(map[string]any); ok {
		for session, v := range storePanes {
			sessionPanes, ok := v.(map[string]any)
			if !ok {
				continue
			}
			flat := make(map[string]PaneSummary, len(sessionPanes))
			for pane, r := range sessionPanes {
				if rec, ok := r.(map[string]any); ok {
					flat[stringKey(pane)] = typedSummaryRecord(rec)
				}
			}
			panes[stringKey(session)] = flat
		}
	}
	var outcomes []Outcome
	if storeOutcomes, ok := store["outcomes"].([]any); ok && len(storeOutcomes) > 0 {
		outcomes = make([]Outcome, 0, len(storeOutcomes))
		for _, item := range storeOutcomes {
			if rec, ok := item.(map[string]any); ok {
				outcomes = append(outcomes, typedSummaryOutcome(rec))
			}
		}
	}
	return Snapshot{
		OK:           true,
		Version:      Version,
		NowMs:        nowMs(),
		SpoolPending: pending,
		Panes:        panes,
		Outcomes:     outcomes,
	}
}

// Detail returns the typed detail record for one pane without JSON round-tripping.
func Detail(session string, pane any) DetailSnapshot {
	store, _ := mergedView()
	var record map[string]any
	if storePanes, ok := store["panes"].(map[string]any); ok {
		if sessionPanes, ok := storePanes[session].(map[string]any); ok {
			if r, ok := sessionPanes[normalizePaneKey(pane)].(map[string]any); ok {
				record = r
			} else {
				record, _ = sessionPanes[stringKey(pane)].(map[string]any)
			}
		}
	}
	if record == nil {
		return DetailSnapshot{OK: true, Version: Version, Record: nil}
	}
	sum := typedSummaryRecord(record)
	det := &PaneDetail{
		PaneSummary: sum,
		Prompt:      stringKey(record["prompt"]),
		Reply:       stringKey(record["reply"]),
	}
	if ex, ok := record["extras"].(map[string]any); ok && len(ex) > 0 {
		det.Extras = ex
	}
	toolName := stringKey(record["tool"])
	toolInput := stringKey(record["tool_input"])
	toolOutput := stringKey(record["tool_output"])
	if toolName != "" || toolInput != "" {
		det.Tool = &Tool{
			Name:   toolName,
			Input:  toolInput,
			Output: toolOutput,
		}
	}
	return DetailSnapshot{
		OK:      true,
		Version: Version,
		Record:  det,
	}
}

func outcomeToMap(o Outcome) map[string]any {
	m := map[string]any{
		"session": strings.TrimSpace(o.Session),
		"agent":   strings.TrimSpace(o.Agent),
		"outcome": strings.TrimSpace(o.Outcome),
		"ts_ms":   o.TsMs,
	}
	if p := strings.TrimSpace(o.Pane); p != "" {
		m["pane"] = p
	}
	if a := strings.TrimSpace(o.App); a != "" {
		m["app"] = a
	}
	if p := strings.TrimSpace(o.Profile); p != "" {
		m["profile"] = p
	}
	if t := strings.TrimSpace(o.Text); t != "" {
		m["text"] = t
	}
	if t := strings.TrimSpace(o.Title); t != "" {
		m["title"] = t
	}
	if mod := strings.TrimSpace(o.Model); mod != "" {
		m["model"] = mod
	}
	if sid := strings.TrimSpace(o.SessionID); sid != "" {
		m["session_id"] = sid
	}
	if cwd := strings.TrimSpace(o.CWD); cwd != "" {
		m["cwd"] = cwd
	}
	return m
}

// RecordTypedOutcome persists a typed Outcome directly into hooks.store.json
// without JSON round-tripping.
func RecordTypedOutcome(o Outcome) error {
	return RecordOutcome(outcomeToMap(o))
}

// RemoveTypedOutcome removes matching outcomes from hooks.store.json without
// JSON round-tripping.
func RemoveTypedOutcome(match Outcome) error {
	return RemoveOutcome(outcomeToMap(match))
}

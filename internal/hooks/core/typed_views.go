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
}

// PaneDetail is the typed per-pane detail view.
type PaneDetail struct {
	PaneSummary
	Prompt string `json:"prompt,omitempty"`
	Tool   *Tool  `json:"tool,omitempty"`
	Reply  string `json:"reply,omitempty"`
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
	return &Usage{
		InputTokens:      toInt64(m["input_tokens"]),
		OutputTokens:     toInt64(m["output_tokens"]),
		ReasoningTokens:  toInt64(m["reasoning_tokens"]),
		CacheReadTokens:  toInt64(m["cache_read_tokens"]),
		CacheWriteTokens: toInt64(m["cache_write_tokens"]),
		CostUSD:          cost,
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
	}
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
			record, _ = sessionPanes[stringKey(pane)].(map[string]any)
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

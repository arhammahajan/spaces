package core

import (
	"strings"
)

// CompanionNormalizer translates a raw harness companion plugin envelope
// into a canonical spacecore v2 event map. Wired by cmd/tmux-spaces from
// agent plugins without import cycles. When nil (or when it returns nil),
// the built-in generic NormalizePluginEnvelope is used.
var CompanionNormalizer func(agent string, envelope map[string]any) map[string]any

// Source authority ranks: plugin (3) > daemon_sse / hook (2) > osc7501 (1) > screen (0).
func SourceRank(source string) int {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "plugin":
		return 3
	case "daemon_sse", "daemon", "hook", "router", "otlp", "":
		return 2
	case "osc7501", "osc", "osc751":
		return 1
	case "screen":
		return 0
	default:
		// Unknown telemetry sources behave like hooks (rank 2) so they merge
		// as fallback but never override a fresh plugin record.
		return 2
	}
}

// normalizePaneKey strips a leading "%" from tmux pane keys when storing,
// preserving existing behavior for plain numeric strings.
func normalizePaneKey(pane any) string {
	return strings.TrimPrefix(strings.TrimSpace(stringKey(pane)), "%")
}

// isPluginFreshRecord reports whether record carries a fresh plugin source.
func isPluginFreshRecord(record map[string]any, now int64) bool {
	if record == nil {
		return false
	}
	src, _ := record["source"].(string)
	if !strings.EqualFold(strings.TrimSpace(src), "plugin") {
		return false
	}
	ts := toInt64(record["plugin_ts_ms"])
	if ts <= 0 {
		ts = toInt64(record["ts_ms"])
	}
	if ts <= 0 {
		return false
	}
	state, _ := record["state"].(string)
	kind, _ := record["kind"].(string)
	age := now - ts
	if age < 0 {
		age = -age
	}
	switch state {
	case "working", "idle", "":
		return age <= WorkingStaleMS
	case "blocked", "waiting":
		if kind == "auth" {
			return age <= AuthTTLMS
		}
		return age <= BlockTTLMS
	case "done":
		since := toInt64(record["state_since_ms"])
		if since <= 0 {
			since = ts
		}
		d := now - since
		if d < 0 {
			d = -d
		}
		return d <= DoneTTLMS
	case "failed":
		since := toInt64(record["state_since_ms"])
		if since <= 0 {
			since = ts
		}
		d := now - since
		if d < 0 {
			d = -d
		}
		return d <= FailedTTLMS
	default:
		return age <= WorkingStaleMS
	}
}

// HasFreshPlugin reports whether (session, pane) currently holds a fresh
// plugin-sourced record, so lower-authority merges (hook, daemon_sse) and
// daemon watchers can defer to it as fallback-only.
func HasFreshPlugin(session string, pane any, now int64) bool {
	if strings.TrimSpace(session) == "" || pane == nil {
		return false
	}
	store := LoadStore()
	panes, ok := store["panes"].(map[string]any)
	if !ok {
		return false
	}
	sessionPanes, ok := panes[session].(map[string]any)
	if !ok {
		return false
	}
	key := normalizePaneKey(pane)
	var rec map[string]any
	if r, ok := sessionPanes[key].(map[string]any); ok {
		rec = r
	} else if r, ok := sessionPanes[stringKey(pane)].(map[string]any); ok {
		rec = r
	}
	return isPluginFreshRecord(rec, now)
}

// NormalizePluginEnvelope is the built-in generic v2 companion envelope
// normalizer. It extracts all standard v2 fields from envelope /
// envelope["payload"] and stamps source="plugin" plus plugin metadata.
func NormalizePluginEnvelope(agent string, envelope map[string]any) map[string]any {
	if envelope == nil {
		return nil
	}
	var payload map[string]any
	if p, ok := envelope["payload"].(map[string]any); ok {
		payload = p
	} else {
		payload = envelope
	}
	merged := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := envelope[k]; ok && v != nil && v != "" {
				return v
			}
		}
		for _, k := range keys {
			if v := first(payload, k); v != nil && v != "" {
				return v
			}
		}
		return nil
	}
	mergedStr := func(keys ...string) string {
		if v, ok := merged(keys...).(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}

	canonAgent := CanonicalAgent(agent, nil)
	if a := mergedStr("agent"); a != "" {
		// Prefer explicit envelope agent when present (canonicalized).
		if ca := CanonicalAgent(a, nil); ca != "" && ca != "unknown" {
			canonAgent = ca
		}
	}
	app := DisplayApp(canonAgent)

	rawEvent := mergedStr("event", "hook_event_name", "hookEventName", "type")
	event := CanonicalEvent(rawEvent)
	// Companion plugins may send native lifecycle names already canonical
	// (working/blocked/done/idle/error). Accept those directly.
	stateOverride := ""
	kindOverride := ""
	if event == "" {
		lower := strings.ToLower(strings.TrimSpace(rawEvent))
		switch lower {
		case "working", "blocked", "done", "error", "idle":
			event = lower
		}
	}
	var state, kind string
	if event != "" {
		if s, k, ok := EventState(event, payload); ok {
			state = s
			kind = k
		} else if event == "working" || event == "blocked" || event == "done" || event == "idle" || event == "error" {
			// Direct lifecycle event from companion.
			switch event {
			case "working":
				state = "working"
			case "blocked":
				state = "blocked"
				kind = "permission"
			case "done":
				state = "done"
			case "error":
				state = "failed"
			case "idle":
				state = "idle"
			}
		}
	}
	// Explicit state/kind in envelope win over derivation.
	if s := mergedStr("state"); s != "" {
		stateOverride = s
	}
	if k := mergedStr("kind"); k != "" {
		kindOverride = k
	}
	if stateOverride != "" {
		state = strings.ToLower(strings.TrimSpace(stateOverride))
		if state == "error" {
			state = "failed"
		}
		if state == "waiting" {
			state = "blocked"
		}
	}
	if kindOverride != "" {
		kind = kindOverride
	}
	if state == "" {
		// Default to working so rich telemetry heartbeats keep the pane live.
		state = "working"
	}
	if event == "" {
		event = strings.ToLower(strings.TrimSpace(rawEvent))
		if event == "" {
			event = state
		}
	}

	ts := toInt64(merged("ts_ms", "tsMs", "timestamp_ms", "timestampMs", "timestamp"))
	if ts <= 0 {
		ts = nowMs()
	}
	pluginTs := toInt64(merged("plugin_ts_ms", "pluginTsMs"))
	if pluginTs <= 0 {
		pluginTs = ts
	}

	pluginName := mergedStr("plugin_name", "pluginName")
	if pluginName == "" {
		if pm, ok := merged("plugin").(map[string]any); ok {
			pluginName = firstStr(pm, "name")
		}
	}
	pluginVersion := mergedStr("plugin_version", "pluginVersion")
	if pluginVersion == "" {
		if pm, ok := merged("plugin").(map[string]any); ok {
			pluginVersion = firstStr(pm, "version")
		}
	}
	var pluginCaps []any
	if pm, ok := merged("plugin").(map[string]any); ok {
		if caps, ok := pm["capabilities"].([]any); ok {
			pluginCaps = caps
		}
	}
	if pluginCaps == nil {
		if caps, ok := merged("plugin_capabilities", "pluginCapabilities").([]any); ok {
			pluginCaps = caps
		}
	}
	pidVal := toInt64(merged("pid"))
	harnessVersion := mergedStr("harness_version", "harnessVersion")

	text := mergedStr("text")
	if text == "" {
		text = oneLine(firstStr(payload, "message", "body", "text", "title"), MaxText, false)
	}
	if text == "" {
		tool := mergedStr("tool", "tool_name", "toolName")
		if tool != "" {
			text = tool
		} else {
			text = strings.ReplaceAll(event, "_", " ")
		}
	}
	text = oneLine(text, MaxText, false)

	var kindVal any
	if kind != "" {
		kindVal = kind
	}

	var modelVal any = fieldStr(mergedStr("model", "model_name", "modelName"), MaxModel, false)
	var sessionIDVal any
	if sid := mergedStr("session_id", "sessionId", "sessionID", "thread_id", "threadId", "conversation_id", "conversationId"); sid != "" {
		sessionIDVal = truncateRunes(sid, MaxSession)
	}
	var cwdVal any = fieldStr(mergedStr("cwd", "working_directory", "workingDirectory", "workspaceRoot"), MaxCWD, false)

	out := map[string]any{
		"v":           Version,
		"agent":       canonAgent,
		"app":         app,
		"source":      "plugin",
		"event":       event,
		"raw_event":   oneLine(rawEvent, MaxModel, false),
		"state":       state,
		"kind":        kindVal,
		"text":        text,
		"title":       fieldStr(merged("title", "conversation_title", "conversationTitle", "session_title", "sessionTitle"), MaxText, false),
		"prompt":      fieldStr(merged("prompt", "user_prompt", "userPrompt", "lastUserInput"), MaxField, false),
		"tool":        truncateRunes(mergedStr("tool", "tool_name", "toolName"), MaxModel),
		"tool_input":  fieldStr(toolText(merged("tool_input", "toolInput", "input", "arguments", "args")), MaxField, false),
		"tool_output": fieldStr(merged("tool_output", "toolOutput", "tool_result", "toolResult", "output", "result"), MaxOutput, true),
		"reply":       fieldStr(merged("reply", "last_assistant_message", "lastAssistantMessage", "assistant_message", "text"), MaxField, true),
		"model":       modelVal,
		"session_id":  sessionIDVal,
		"cwd":         cwdVal,
		"background":  int64(0),
		"ts_ms":       ts,
		"seq":         nowNanos(),
	}

	// Plugin metadata stamps (always present).
	out["plugin_name"] = pluginName
	out["plugin_version"] = pluginVersion
	out["plugin_ts_ms"] = pluginTs
	if pluginCaps != nil {
		out["plugin_capabilities"] = pluginCaps
	} else {
		out["plugin_capabilities"] = []any{}
	}
	if pidVal > 0 {
		out["pid"] = pidVal
	}
	if harnessVersion != "" {
		out["harness_version"] = harnessVersion
	}

	// Rich session & mode fields.
	for _, k := range []string{"provider", "effort", "mode", "sandbox_mode", "git_branch"} {
		if v := merged(k); v != nil && v != "" {
			switch k {
			case "provider", "effort", "mode", "sandbox_mode":
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					out[k] = truncateRunes(strings.TrimSpace(s), MaxModel)
				}
			case "git_branch":
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					out[k] = truncateRunes(strings.TrimSpace(s), MaxModel)
				}
			}
		}
	}

	// Usage (supports TotalTokens explicit or derived).
	if u := normalizeUsage(first(payload, "usage")); u != nil {
		out["usage"] = u
	} else if u := normalizeUsage(merged("usage")); u != nil {
		out["usage"] = u
	}

	// Rich numeric metrics.
	for _, k := range []string{"context_tokens", "context_window", "turn_count", "compaction_count", "last_compaction_ms", "tool_call_count", "tool_error_count", "retry_count", "rate_limit_reset_ms"} {
		if v := toInt64(merged(k)); v != 0 {
			out[k] = v
		} else if raw := merged(k); raw != nil {
			// Preserve explicit zero? Only set non-zero to keep records lean.
			_ = raw
		}
	}
	if f, ok := toFloat64(merged("context_percent")); ok && f != 0 {
		out["context_percent"] = f
	}
	// Auto-derive context_percent.
	if toInt64(out["context_tokens"]) > 0 && toInt64(out["context_window"]) > 0 {
		if _, has := out["context_percent"]; !has {
			out["context_percent"] = float64(toInt64(out["context_tokens"])) / float64(toInt64(out["context_window"])) * 100
		}
	}
	if s := mergedStr("last_compaction_reason"); s != "" {
		out["last_compaction_reason"] = oneLine(s, MaxField, false)
	}
	if b, ok := merged("rate_limited").(bool); ok && b {
		out["rate_limited"] = true
	} else if s := strings.ToLower(strings.TrimSpace(mergedStr("rate_limited"))); s == "true" || s == "1" {
		out["rate_limited"] = true
	}
	if s := mergedStr("rate_limit_message"); s != "" {
		out["rate_limit_message"] = oneLine(s, MaxField, false)
	}

	// Rich activity slices (bounded).
	if v := merged("active_tools"); v != nil {
		out["active_tools"] = boundSlice(toAnySlice(v), 16)
	}
	if v := merged("recent_tools"); v != nil {
		out["recent_tools"] = boundSlice(toAnySlice(v), 16)
	}
	if v := merged("files_touched"); v != nil {
		out["files_touched"] = boundSlice(toAnySlice(v), 32)
	}
	if v := merged("todos"); v != nil {
		out["todos"] = boundSlice(toAnySlice(v), 32)
	}
	if v := merged("mcp_servers"); v != nil {
		out["mcp_servers"] = boundSlice(toAnySlice(v), 32)
	}
	if v := merged("lsp_diagnostics"); v != nil {
		out["lsp_diagnostics"] = v
	}
	if v := merged("pending_permission"); v != nil {
		out["pending_permission"] = v
	}
	if s := mergedStr("last_error"); s != "" {
		out["last_error"] = oneLine(s, MaxField, false)
	}
	if v := merged("extras"); v != nil {
		if m, ok := v.(map[string]any); ok {
			out["extras"] = m
		}
	}

	// Profile passthrough.
	if p := mergedStr("profile"); p != "" {
		out["profile"] = truncateRunes(p, MaxModel)
	}

	// Subagent routing markers (preserved for mergeEventApplied).
	if b, ok := merged("is_subagent").(bool); ok && b {
		out["is_subagent"] = true
	}
	if s := mergedStr("parent_session_id", "parentSessionId", "parent_sessionId"); s != "" {
		out["parent_session_id"] = truncateRunes(s, MaxSession)
	}
	if v := merged("subagents"); v != nil {
		if sl, ok := v.([]any); ok {
			out["subagents"] = boundSlice(sl, 32)
		}
	}
	if v := merged("background_tasks", "backgroundTasks"); v != nil {
		out["background_tasks"] = v
	}

	return out
}

func toAnySlice(v any) []any {
	if sl, ok := v.([]any); ok {
		return sl
	}
	return nil
}

func boundSlice(in []any, max int) []any {
	if in == nil {
		return nil
	}
	if len(in) > max {
		return in[len(in)-max:]
	}
	return in
}

// isPluginSubagentEvent reports whether a normalized plugin event must be
// routed strictly to the pane's children list.
func isPluginSubagentEvent(event map[string]any) bool {
	if event == nil {
		return false
	}
	if parseBoolField(event, "is_subagent") {
		return true
	}
	parent := strings.TrimSpace(stringKey(event["parent_session_id"]))
	if parent != "" && parent != strings.TrimSpace(stringKey(event["session_id"])) {
		return true
	}
	return false
}

// mergePluginSubagentIntoRecord folds a subagent-scoped plugin event into
// record["children"] without touching parent-owned fields.
func mergePluginSubagentIntoRecord(record, event map[string]any, now int64) bool {
	child := pluginSubagentChild(event, now)
	if child == nil {
		return false
	}
	children, _ := record["children"].([]any)
	record["children"] = mergeChildEntry(children, child, now)
	// Reconcile full subagents snapshot when present.
	if subs, ok := event["subagents"].([]any); ok && len(subs) > 0 {
		cur, _ := record["children"].([]any)
		for _, raw := range subs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			cur = mergeChildEntry(cur, normalizePluginSubagentMap(m, now), now)
		}
		record["children"] = cur
	}
	record["ts_ms"] = now
	if seq, ok := event["seq"]; ok && seq != nil {
		record["seq"] = seq
	} else {
		record["seq"] = now
	}
	if s, _ := record["agent"].(string); s == "" {
		if a, _ := event["agent"].(string); a != "" {
			record["agent"] = a
		}
	}
	if s, _ := record["source"].(string); s == "" {
		record["source"] = "plugin"
	}
	record["last_event"] = map[string]any{
		"event": event["event"], "agent": event["agent"], "ts_ms": now,
		"child_id": stringKey(child["id"]), "child_state": stringKey(child["state"]),
	}
	return true
}

func pluginSubagentChild(event map[string]any, now int64) map[string]any {
	id := strings.TrimSpace(stringKey(event["session_id"]))
	if id == "" {
		id = strings.TrimSpace(stringKey(event["id"]))
	}
	if id == "" {
		if ch, ok := event["child"].(map[string]any); ok && ch != nil {
			id = strings.TrimSpace(stringKey(ch["id"]))
		}
	}
	if id == "" {
		id = "child"
	}
	typ := strings.TrimSpace(stringKey(event["type"]))
	if typ == "" {
		typ = strings.TrimSpace(firstStr(event, "agent_type", "agentType", "subagent_type", "subagentType"))
	}
	if typ == "" {
		if ch, ok := event["child"].(map[string]any); ok && ch != nil {
			typ = strings.TrimSpace(stringKey(ch["type"]))
		}
	}
	state := strings.TrimSpace(stringKey(event["state"]))
	if state == "" {
		state = "working"
	}
	if state == "error" {
		state = "failed"
	}
	text := oneLine(firstStr(event, "text", "description", "prompt"), MaxText, false)
	if text == "" {
		if ch, ok := event["child"].(map[string]any); ok && ch != nil {
			text = oneLine(stringKey(ch["text"]), MaxText, false)
		}
	}
	since := toInt64(event["since_ms"])
	if since <= 0 {
		since = toInt64(event["plugin_ts_ms"])
	}
	if since <= 0 {
		since = toInt64(event["ts_ms"])
	}
	if since <= 0 {
		since = now
	}
	ts := toInt64(event["ts_ms"])
	if ts <= 0 {
		ts = now
	}
	var ended int64
	if !isActiveBgState(state) {
		ended = toInt64(event["ended_ms"])
		if ended <= 0 {
			ended = now
		}
	}
	return map[string]any{
		"id":       truncateRunes(id, MaxSession),
		"type":     truncateRunes(typ, MaxModel),
		"kind":     BgKindSubagent,
		"category": BgCategoryFinite,
		"state":    state,
		"outcome":  oneLine(firstStr(event, "outcome", "error"), 80, false),
		"text":     text,
		"since_ms": since,
		"ts_ms":    ts,
		"ended_ms": ended,
	}
}

func normalizePluginSubagentMap(m map[string]any, now int64) map[string]any {
	id := strings.TrimSpace(stringKey(m["id"]))
	if id == "" {
		id = strings.TrimSpace(stringKey(m["session_id"]))
	}
	if id == "" {
		return nil
	}
	state := strings.TrimSpace(stringKey(m["state"]))
	if state == "" {
		state = "working"
	}
	since := toInt64(m["since_ms"])
	if since <= 0 {
		since = toInt64(m["ts_ms"])
	}
	if since <= 0 {
		since = now
	}
	ts := toInt64(m["ts_ms"])
	if ts <= 0 {
		ts = now
	}
	var ended int64
	if !isActiveBgState(state) {
		ended = toInt64(m["ended_ms"])
		if ended <= 0 {
			ended = now
		}
	}
	return map[string]any{
		"id":       truncateRunes(id, MaxSession),
		"type":     truncateRunes(strings.TrimSpace(stringKey(m["type"])), MaxModel),
		"kind":     BgKindSubagent,
		"category": BgCategoryFinite,
		"state":    state,
		"outcome":  oneLine(stringKey(m["outcome"]), 80, false),
		"text":     oneLine(stringKey(m["text"]), MaxText, false),
		"since_ms": since,
		"ts_ms":    ts,
		"ended_ms": ended,
	}
}

// mergePluginRichFields copies rich v2 telemetry from event onto record when present.
func mergePluginRichFields(record, event map[string]any) {
	for _, k := range []string{
		"plugin_name", "plugin_version", "plugin_capabilities", "plugin_ts_ms",
		"pid", "harness_version",
		"provider", "effort", "mode", "sandbox_mode", "git_branch",
		"context_tokens", "context_window", "context_percent",
		"turn_count", "compaction_count", "last_compaction_ms", "last_compaction_reason",
		"tool_call_count", "tool_error_count", "retry_count",
		"rate_limited", "rate_limit_reset_ms", "rate_limit_message",
		"active_tools", "recent_tools", "files_touched", "todos", "mcp_servers",
		"lsp_diagnostics", "pending_permission", "last_error", "extras",
	} {
		v, ok := event[k]
		if !ok || v == nil {
			continue
		}
		switch k {
		case "recent_tools":
			if sl, ok := v.([]any); ok {
				record[k] = boundSlice(sl, 16)
				continue
			}
		case "active_tools":
			if sl, ok := v.([]any); ok {
				record[k] = boundSlice(sl, 16)
				continue
			}
		case "files_touched", "todos", "mcp_servers":
			if sl, ok := v.([]any); ok {
				record[k] = boundSlice(sl, 32)
				continue
			}
		case "last_error":
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
				continue
			}
		}
		record[k] = v
	}
	// Auto-derive context_percent when tokens+window present.
	if toInt64(record["context_tokens"]) > 0 && toInt64(record["context_window"]) > 0 {
		if _, ok := record["context_percent"]; !ok {
			if f, ok2 := record["context_percent"].(float64); !ok2 || f == 0 {
				record["context_percent"] = float64(toInt64(record["context_tokens"])) / float64(toInt64(record["context_window"])) * 100
			}
		} else if f, ok := toFloat64(record["context_percent"]); !ok || f == 0 {
			record["context_percent"] = float64(toInt64(record["context_tokens"])) / float64(toInt64(record["context_window"])) * 100
		}
	}
	// Merge subagents snapshot into children (parent fields untouched beyond children).
	if subs, ok := event["subagents"].([]any); ok && len(subs) > 0 {
		cur, _ := record["children"].([]any)
		for _, raw := range subs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if n := normalizePluginSubagentMap(m, toInt64(event["ts_ms"])); n != nil {
				cur = mergeChildEntry(cur, n, toInt64(event["ts_ms"]))
			}
		}
		record["children"] = cur
	}
}

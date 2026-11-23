package core

import (
	"sort"
	"strings"
)

// EventState maps a canonical event plus payload to a pane state and an
// optional kind, mirroring event_state. ok is false when the event carries
// no state transition (Python: (None, None)) — NormalizeEvent then drops
// the event.
func EventState(event string, payload map[string]any) (state, kind string, ok bool) {
	switch event {
	case "session_start", "session_end":
		return "idle", "", true
	case "prompt", "pre_invocation":
		return "working", "", true
	case "pre_tool":
		toolName := firstStr(payload, "tool_name", "toolName", "name", "tool")
		if toolName == "" {
			if tc, ok := payload["toolCall"].(map[string]any); ok {
				toolName = firstStr(tc, "name")
			} else if tc, ok := payload["tool_call"].(map[string]any); ok {
				toolName = firstStr(tc, "name")
			}
		}
		if toolIsAsk(toolName) {
			return "blocked", "question", true
		}
		return "working", "", true
	case "post_tool", "tool_failure", "compact", "response":
		return "working", "", true
	case "permission":
		if looksAuth(payload) {
			return "blocked", "auth", true
		}
		return "blocked", "permission", true
	case "elicitation":
		return "blocked", "question", true
	case "elicitation_result":
		return "working", "", true
	case "stop":
		if strings.EqualFold(firstStr(payload, "terminationReason", "termination_reason"), "ERROR") ||
			firstStr(payload, "error", "error_message", "errorMessage") != "" {
			return "failed", "", true
		}
		return "done", "", true
	case "stop_failure":
		return "failed", "", true
	case "subagent_start", "subagent_stop", "task_created", "task_completed":
		return "working", "", true
	case "notification":
		notification := firstStr(payload, "notification_type", "notificationType", "type", "notification")
		lowered := strings.ToLower(notification)
		message := firstStr(payload, "message", "body", "text", "title")
		messageLower := strings.ToLower(message)
		if strings.Contains(lowered, "permission") ||
			strings.Contains(lowered, "toolpermission") ||
			strings.Contains(lowered, "approval") ||
			strings.Contains(messageLower, "permission") ||
			strings.Contains(messageLower, "approve") {
			return "blocked", "permission", true
		}
		if strings.Contains(lowered, "elicitation") || strings.Contains(lowered, "question") {
			return "blocked", "question", true
		}
		if strings.Contains(lowered, "idle") {
			// idle_prompt is a turn-end report in Claude, Qwen and Grok.
			// Treat it as done; the config's Stop/AfterAgent events
			// normally arrive first and set the same state.
			return "done", "", true
		}
		if strings.Contains(lowered, "complete") {
			return "done", "", true
		}
		if looksAuth(payload) || strings.Contains(strings.ToLower(message), "auth") {
			return "blocked", "auth", true
		}
		return "", "", false
	case "working":
		return "working", "", true
	case "blocked":
		return "blocked", "permission", true
	case "done":
		return "done", "", true
	case "error":
		return "failed", "", true
	case "idle":
		return "idle", "", true
	}
	return "", "", false
}

// NormalizeEvent canonicalizes a provider hook event into the normalized
// envelope consumed by the store, spool, publisher and plugin views,
// mirroring normalize_event. It returns nil for unknown or stateless
// events. env selects runtime overrides (Grok); an empty map reads the
// process environment.
func NormalizeEvent(agent, rawEvent string, payload map[string]any, env map[string]string) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	envMap := envOrOS(env)
	agent = CanonicalAgent(agent, envMap)
	app := DisplayApp(agent)
	raw := rawEvent
	if raw == "" {
		raw = firstStr(payload, "hook_event_name", "hookEventName", "hook_type", "hookType", "event", "type")
	}
	event := CanonicalEvent(raw)
	if event == "" {
		return nil
	}
	if agent == "codex" && (event == "subagent_start" || event == "subagent_stop") &&
		firstStr(payload, "agent_id", "agentId") == "" {
		return nil
	}
	state, kind, ok := EventState(event, payload)
	if !ok {
		return nil
	}

	tool := firstStr(payload, "tool_name", "toolName", "name", "tool")
	toolInput := first(payload, "tool_input", "toolInput", "toolArgs", "input", "arguments", "args")
	toolCallID := ""
	if tc, ok := payload["toolCall"].(map[string]any); ok {
		if tool == "" {
			tool = firstStr(tc, "name")
		}
		if toolInput == nil {
			toolInput = first(tc, "args", "arguments", "input")
		}
		toolCallID = firstStr(tc, "id")
	} else if tc, ok := payload["tool_call"].(map[string]any); ok {
		if tool == "" {
			tool = firstStr(tc, "name")
		}
		if toolInput == nil {
			toolInput = first(tc, "args", "arguments", "input")
		}
		toolCallID = firstStr(tc, "id")
	}
	toolOutput := first(payload,
		"tool_response", "toolResponse", "tool_output", "toolOutput",
		"tool_result", "toolResult", "output", "result")
	if agent == "antigravity" && event == "post_tool" && toolOutput == nil && isAntigravityBackgroundTool(tool) {
		if content := antigravityStepToolOutput(payload, tool); content != "" {
			toolOutput = content
		}
	}
	prompt := firstStr(payload,
		"prompt", "user_prompt", "userPrompt", "lastUserInput", "last_user_input",
		"initial_prompt", "initialPrompt", "user_message", "message")
	reply := firstStr(payload,
		"last_assistant_message", "lastAssistantMessage", "finalModelOutput", "final_model_output",
		"modelOutput", "model_output", "prompt_response",
		"last_message", "assistant_message", "assistant_response", "text")
	errText := firstStr(payload,
		"error", "error_details", "errorDetails", "error_message",
		"errorMessage", "reason", "message")

	preview := BuildPreview(tool, toolInput)
	text := preview
	switch event {
	case "permission":
		if preview != "" {
			text = "approve " + preview
		} else if e := oneLine(errText, MaxText, false); e != "" {
			text = e
		} else {
			text = "needs approval"
		}
	case "elicitation":
		pe := prompt
		if pe == "" {
			pe = errText
		}
		if t := oneLine(pe, MaxText, false); t != "" {
			text = "answer: " + t
		} else {
			text = "answer: needs an answer"
		}
	case "prompt", "pre_invocation":
		if t := oneLine(prompt, MaxText, false); t != "" {
			text = "prompt: " + t
		} else {
			text = "prompt: working"
		}
	case "tool_failure":
		ep := errText
		if ep == "" {
			ep = preview
		}
		if t := oneLine(ep, MaxText, false); t != "" {
			text = "tool failed: " + t
		} else {
			text = "tool failed: failed"
		}
	case "response":
		if t := oneLine(reply, MaxText, true); t != "" {
			text = t
		} else {
			text = "responding"
		}
	case "stop":
		if state == "failed" {
			if t := oneLine(errText, MaxText, false); t != "" {
				text = "error: " + t
			} else {
				text = "error: turn failed"
			}
		} else if t := oneLine(reply, MaxText, true); t != "" {
			text = t
		} else {
			text = "turn complete"
		}
	case "stop_failure":
		if t := oneLine(errText, MaxText, false); t != "" {
			text = "error: " + t
		} else {
			text = "error: turn failed"
		}
	case "session_start":
		text = "session started"
	case "session_end":
		text = "session ended"
	case "compact":
		text = "compacting context"
	case "notification":
		m := firstStr(payload, "message", "body", "text", "title")
		if m == "" {
			m = "notification"
		}
		text = oneLine(m, MaxText, false)
	case "subagent_start":
		d := firstStr(payload, "description", "prompt")
		if d == "" {
			d = tool
		}
		if d == "" {
			d = "started"
		}
		text = "subagent: " + oneLine(d, MaxText, false)
	case "subagent_stop":
		d := firstStr(payload, "description", "prompt")
		if d == "" {
			d = tool
		}
		if d == "" {
			d = "finished"
		}
		text = "subagent done: " + oneLine(d, MaxText, false)
	case "task_created":
		d := firstStr(payload, "description", "command", "title", "name")
		if d == "" {
			d = "started"
		}
		text = "background task: " + oneLine(d, MaxText, false)
	case "task_completed":
		d := firstStr(payload, "description", "command", "title", "name")
		if d == "" {
			d = "finished"
		}
		text = "background task done: " + oneLine(d, MaxText, false)
	case "idle":
		text = "idle"
	}
	if text == "" {
		text = strings.ReplaceAll(event, "_", " ")
	}

	ts := nowMs()
	child := childInfo(agent, payload, event, tool, oneLine(text, MaxText, false))
	var bgTasks []any
	var bgAuth, cronsAuth bool
	var childUpdates []any
	if child == nil {
		bgTasks, bgAuth, cronsAuth = extractBackgroundWork(payload, event, tool, toolInput, toolOutput, ts)
		childUpdates = extractChildToolUpdates(payload, event, tool, toolCallID, toolInput, toolOutput, ts)
	}
	bgCount := childrenBackground(payload)
	if len(bgTasks) > bgCount {
		active := 0
		for _, raw := range bgTasks {
			if m, ok := raw.(map[string]any); ok && isActiveBgState(stringKey(m["state"])) {
				active++
			}
		}
		if active > bgCount {
			bgCount = active
		}
	}

	// Antigravity StopHookArgs.fullyIdle: true authoritatively clears finite
	// background work; false (or omitted on a normal StopHookArgs) signals that
	// background subagents or tasks are still running.
	finiteAuth := false
	pendingBg := false
	if child == nil && event == "stop" && state == "done" {
		if known, fullyIdle := antigravityStopIdleSignal(agent, payload); known {
			if fullyIdle {
				finiteAuth = true
			} else {
				pendingBg = true
			}
		}
	}

	// Stateless ATT-5 evaluation: if Stop / idle_prompt already carries active
	// finite background work in the payload (or Antigravity reports not fully
	// idle), hold state=working right here so NormalizeEvent and MergeEvent agree.
	var waitingOn any
	if child == nil && (event == "stop" || (event == "notification" && state == "done")) && state == "done" {
		var finite []map[string]any
		for _, raw := range bgTasks {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if isActiveBgState(stringKey(m["state"])) && stringKey(m["category"]) == BgCategoryFinite {
				finite = append(finite, m)
			}
		}
		if len(finite) > 0 {
			state = "working"
			kind = ""
			w := formatWaitingText(finite)
			text = w
			waitingOn = w
		} else if pendingBg {
			state = "working"
			kind = ""
			w := "waiting on background tasks"
			text = w
			waitingOn = w
		}
	}

	var kindVal any
	if kind != "" {
		kindVal = kind
	}
	title := conversationTitle(payload)
	if title == nil && (event == "prompt" || event == "pre_invocation") && prompt != "" {
		title = fieldStr(prompt, MaxText, false)
	}
	var model any = fieldStr(firstStr(payload, "model", "model_name", "modelName"), MaxModel, false)
	var cwd any = fieldStr(firstStr(payload, "cwd", "working_directory", "workingDirectory", "workspaceRoot"), MaxCWD, false)
	profile := firstStr(payload, "profile")
	if profile == "" {
		profile = strings.TrimSpace(envMap["HARNESS_PROFILE"])
	}
	source := firstStr(payload, "source")
	if source == "" {
		source = "hook"
	}
	usage := normalizeUsage(first(payload, "usage"))
	out := map[string]any{
		"v":           Version,
		"agent":       agent,
		"app":         app,
		"source":      truncateRunes(source, MaxModel),
		"event":       event,
		"raw_event":   oneLine(raw, MaxModel, false),
		"state":       state,
		"kind":        kindVal,
		"text":        oneLine(text, MaxText, false),
		"title":       title,
		"prompt":      fieldStr(prompt, MaxField, false),
		"tool":        truncateRunes(tool, MaxModel),
		"tool_input":  fieldStr(toolText(toolInput), MaxField, false),
		"tool_output": fieldStr(toolOutput, MaxOutput, true),
		"reply":       fieldStr(reply, MaxField, true),
		"model":       model,
		"session_id":  sessionID(payload),
		"cwd":         cwd,
		"background":  bgCount,
		"child":       child,
		"ts_ms":       ts,
		"seq":         nowNanos(),
	}
	if usage != nil {
		out["usage"] = usage
	}
	if len(bgTasks) > 0 || bgAuth || cronsAuth {
		if bgTasks == nil {
			bgTasks = []any{}
		}
		out["background_tasks"] = bgTasks
	}
	if len(childUpdates) > 0 {
		out["children_updates"] = childUpdates
	}
	if bgAuth {
		out["background_authoritative"] = true
	}
	if cronsAuth {
		out["crons_authoritative"] = true
	}
	if finiteAuth {
		out["finite_authoritative"] = true
	}
	if pendingBg {
		out["pending_background"] = true
	}
	if waitingOn != nil {
		out["waiting_on"] = waitingOn
	}
	if profile != "" {
		out["profile"] = truncateRunes(profile, MaxModel)
	}
	return out
}

// normalizeUsage extracts and normalizes token/cost usage telemetry from a
// payload map or Usage struct. Returns nil when all fields are zero/absent.
func normalizeUsage(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	var inTok, outTok, reasonTok, cacheRead, cacheWrite int64
	var costUSD float64
	switch u := raw.(type) {
	case *Usage:
		if u == nil {
			return nil
		}
		inTok = u.InputTokens
		outTok = u.OutputTokens
		reasonTok = u.ReasoningTokens
		cacheRead = u.CacheReadTokens
		cacheWrite = u.CacheWriteTokens
		costUSD = u.CostUSD
	case Usage:
		inTok = u.InputTokens
		outTok = u.OutputTokens
		reasonTok = u.ReasoningTokens
		cacheRead = u.CacheReadTokens
		cacheWrite = u.CacheWriteTokens
		costUSD = u.CostUSD
	case map[string]any:
		inTok = toInt64(first(u, "input_tokens", "inputTokens"))
		outTok = toInt64(first(u, "output_tokens", "outputTokens"))
		reasonTok = toInt64(first(u, "reasoning_tokens", "reasoningTokens"))
		cacheRead = toInt64(first(u, "cache_read_tokens", "cacheReadTokens"))
		cacheWrite = toInt64(first(u, "cache_write_tokens", "cacheWriteTokens"))
		if f, ok := toFloat64(first(u, "cost_usd", "costUsd", "costUSD")); ok {
			costUSD = f
		}
	default:
		return nil
	}
	out := map[string]any{}
	if inTok > 0 {
		out["input_tokens"] = inTok
	}
	if outTok > 0 {
		out["output_tokens"] = outTok
	}
	if reasonTok > 0 {
		out["reasoning_tokens"] = reasonTok
	}
	if cacheRead > 0 {
		out["cache_read_tokens"] = cacheRead
	}
	if cacheWrite > 0 {
		out["cache_write_tokens"] = cacheWrite
	}
	if costUSD > 0 {
		out["cost_usd"] = costUSD
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeUsage merges non-zero usage fields from incoming onto existing.
func mergeUsage(existing, incoming any) map[string]any {
	inc := normalizeUsage(incoming)
	if inc == nil {
		return normalizeUsage(existing)
	}
	base := normalizeUsage(existing)
	if base == nil {
		return inc
	}
	merged := make(map[string]any, len(base)+len(inc))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range inc {
		merged[k] = v
	}
	return merged
}

// emptyRecord mirrors _empty_record.
func emptyRecord() map[string]any {
	return map[string]any{
		"v": 1, "agent": "", "app": "", "source": "", "state": "idle", "kind": nil,
		"event": "", "raw_event": "", "state_since_ms": int64(0),
		"turn_started_ms": int64(0), "ts_ms": int64(0), "text": "",
		"title": nil, "prompt": nil, "tool": "", "tool_input": nil, "tool_output": nil,
		"reply": nil, "model": nil, "session_id": nil, "cwd": nil, "usage": nil,
		"background": int64(0), "background_tasks": []any{}, "waiting_on": nil,
		"children": []any{}, "last_event": nil,
	}
}

// MergeEvent folds a normalized envelope into a pane record, mirroring
// merge_event: child updates, auth-block stickiness, turn/session
// boundaries, and DEDUPE_MS duplicate suppression. now is explicit so
// tests control staleness without sleeping.
func MergeEvent(existing, event map[string]any, now int64) map[string]any {
	record, _ := mergeEventApplied(existing, event, now)
	return record
}

func mergeEventApplied(existing, event map[string]any, now int64) (map[string]any, bool) {
	var record map[string]any
	if existing != nil {
		record = make(map[string]any, len(existing)+2)
		for k, v := range existing {
			record[k] = v
		}
	} else {
		record = emptyRecord()
	}
	if _, ok := record["children"].([]any); !ok {
		record["children"] = []any{}
	}
	if _, ok := record["background_tasks"].([]any); !ok {
		record["background_tasks"] = []any{}
	}

	event = promoteAntigravitySubagentFromRecord(existing, event, now)

	if last, ok := record["last_event"].(map[string]any); ok {
		lastEvent, _ := last["event"].(string)
		lastAgent, _ := last["agent"].(string)
		lastChildID, _ := last["child_id"].(string)
		lastChildState, _ := last["child_state"].(string)
		evEvent, _ := event["event"].(string)
		evAgent, _ := event["agent"].(string)
		evChildID := ""
		evChildState := ""
		if ch, ok := event["child"].(map[string]any); ok && ch != nil {
			evChildID, _ = ch["id"].(string)
			evChildState, _ = ch["state"].(string)
		}
		var lastTs int64
		if last["ts_ms"] != nil {
			lastTs = toInt64(last["ts_ms"])
		}
		d := now - lastTs
		if d < 0 {
			d = -d
		}
		// Never dedupe distinct subagent children, subagent state transitions,
		// child vs parent events, tool calls that register/update children or
		// background tasks, or a Stop transition that resolves a held working
		// turn or carries an authoritative background/idle snapshot.
		distinctChild := (evChildID != "" || lastChildID != "") && (evChildID != lastChildID || evChildState != lastChildState)
		hasChildUpdates := truthy(event["children_updates"]) || truthy(event["background_tasks"])
		stopStateChange := evEvent == "stop" && (truthy(record["waiting_on"]) ||
			parseBoolField(event, "background_authoritative") ||
			parseBoolField(event, "crons_authoritative") ||
			parseBoolField(event, "finite_authoritative"))
		newSession := evChildID == "" && stringKey(event["session_id"]) != "" &&
			stringKey(event["session_id"]) != stringKey(record["session_id"])
		if lastEvent == evEvent && lastAgent == evAgent && d <= DedupeMS &&
			!distinctChild && !hasChildUpdates && !stopStateChange && !newSession {
			return record, false
		}
	}

	if child, ok := event["child"].(map[string]any); ok && child != nil {
		prevActiveFinite, _ := collectBackgroundWork(record)
		children, _ := record["children"].([]any)
		if stringKey(event["agent"]) == "codex" && !codexChildEventApplies(children, child, stringKey(event["event"])) {
			return record, false
		}
		record["children"] = mergeChildEntry(children, child, now)
		childID := stringKey(child["id"])
		childState := stringKey(child["state"])
		// Also fold any background_tasks / session_crons carried on SubagentStop
		// (Claude Code includes parent-session background_tasks on SubagentStop).
		incBg, _ := event["background_tasks"].([]any)
		bgAuth := parseBoolField(event, "background_authoritative")
		cronsAuth := parseBoolField(event, "crons_authoritative")
		if len(incBg) > 0 || bgAuth || cronsAuth {
			existingBg, _ := record["background_tasks"].([]any)
			record["background_tasks"] = mergeBackgroundTasks(existingBg, incBg, bgAuth, cronsAuth, now)
		}
		if truthy(record["waiting_on"]) {
			nextActiveFinite, _ := collectBackgroundWork(record)
			if len(nextActiveFinite) > 0 {
				w := formatWaitingText(nextActiveFinite)
				if stringKey(record["text"]) == stringKey(record["waiting_on"]) {
					record["text"] = w
				}
				record["waiting_on"] = w
			} else if len(prevActiveFinite) > 0 {
				record["waiting_on"] = nil
			}
		}
		record["ts_ms"] = now
		if seq, ok := event["seq"]; ok && seq != nil {
			record["seq"] = seq
		} else {
			record["seq"] = now
		}
		record["last_event"] = map[string]any{
			"event": event["event"], "agent": event["agent"], "ts_ms": now,
			"child_id": childID, "child_state": childState,
		}
		if s, _ := record["agent"].(string); s == "" {
			if a, _ := event["agent"].(string); a != "" {
				record["agent"] = a
			} else {
				record["agent"] = ""
			}
			if a, _ := event["app"].(string); a != "" {
				record["app"] = a
			} else {
				record["app"] = ""
			}
		}
		if s, _ := record["source"].(string); s == "" {
			if src, _ := event["source"].(string); src != "" {
				record["source"] = src
			}
		}
		if event["usage"] != nil {
			if u := mergeUsage(record["usage"], event["usage"]); u != nil {
				record["usage"] = u
			}
		}
		return record, true
	}

	eventName, _ := event["event"].(string)
	state, _ := event["state"].(string)
	if state == "" {
		state, _ = record["state"].(string)
		if state == "" {
			state = "idle"
		}
	}
	kind, _ := event["kind"]
	previousState, _ := record["state"].(string)
	// An auth block survives ordinary tool/turn events; only a fresh turn
	// or a session boundary clears it.
	if record["kind"] == "auth" && state != "idle" &&
		eventName != "session_start" && eventName != "session_end" && eventName != "prompt" {
		state = "blocked"
		kind = "auth"
	}
	if state == "working" {
		kind = nil
	}

	if eventName == "prompt" || eventName == "session_start" || (eventName == "pre_invocation" && previousState != "working") {
		record["turn_started_ms"] = now
	}
	codex := stringKey(event["agent"]) == "codex"
	oldSID, newSID := stringKey(record["session_id"]), stringKey(event["session_id"])
	newCodexThread := codex && oldSID != "" && newSID != "" && oldSID != newSID
	if eventName == "session_start" || eventName == "session_end" || newCodexThread {
		record["children"] = []any{}
		record["background_tasks"] = []any{}
		record["waiting_on"] = nil
		record["reply"] = nil
		if eventName == "session_start" || newCodexThread {
			if prevSID, _ := record["session_id"].(string); prevSID != "" {
				if newSID, _ := event["session_id"].(string); newSID != "" && newSID != prevSID {
					record["title"] = nil
					record["prompt"] = nil
					record["usage"] = nil
				}
			}
		}
	}
	if eventName == "prompt" || (eventName == "pre_invocation" && previousState != "working") {
		record["reply"] = nil
		record["waiting_on"] = nil
		if codex {
			children, _ := record["children"].([]any)
			active := make([]any, 0, len(children))
			for _, raw := range children {
				if child, ok := raw.(map[string]any); ok && isActiveBgState(stringKey(child["state"])) {
					active = append(active, child)
				}
			}
			record["children"] = active
		}
	}

	// Fold parent tool-level child updates (e.g. invoke_subagent / manage_subagents).
	prevActiveFinite, _ := collectBackgroundWork(record)
	if updates, ok := event["children_updates"].([]any); ok && len(updates) > 0 {
		curChildren, _ := record["children"].([]any)
		for _, raw := range updates {
			if ch, ok := raw.(map[string]any); ok {
				curChildren = mergeChildEntry(curChildren, ch, now)
			}
		}
		record["children"] = curChildren
	}

	// Merge background tasks/services/watchers/crons before evaluating ATT-5
	// Stop completion so the state transition sees the latest snapshot.
	incBg, _ := event["background_tasks"].([]any)
	bgAuth := parseBoolField(event, "background_authoritative")
	cronsAuth := parseBoolField(event, "crons_authoritative")
	if len(incBg) > 0 || bgAuth || cronsAuth {
		existingBg, _ := record["background_tasks"].([]any)
		mergedBg := mergeBackgroundTasks(existingBg, incBg, bgAuth, cronsAuth, now)
		record["background_tasks"] = mergedBg
		if bgAuth && (eventName == "stop" || eventName == "notification") {
			existingChildren, _ := record["children"].([]any)
			record["children"] = reconcileChildrenFromBackground(existingChildren, mergedBg, now)
		}
	}
	if parseBoolField(event, "finite_authoritative") && eventName == "stop" {
		reconcileFiniteOnIdle(record, now)
	}

	// ATT-5: Accurate completion with background work.
	// Keep the pane working when Stop (or idle_prompt) arrives with active
	// finite work (tests, builds, subagents), showing what it is waiting for.
	// Let the turn finish as done when only services, watchers, or scheduled
	// wakeups remain (or no background work remains).
	waitingOverride := ""
	if (eventName == "stop" && state != "failed") || (eventName == "notification" && state == "done") {
		if !(record["kind"] == "auth" && state == "blocked") {
			activeFinite, activeServices := collectBackgroundWork(record)
			pendingBg := parseBoolField(event, "pending_background")
			if len(activeFinite) > 0 {
				state = "working"
				kind = nil
				waitingOverride = formatWaitingText(activeFinite)
				record["waiting_on"] = waitingOverride
			} else if pendingBg && len(activeServices) == 0 {
				state = "working"
				kind = nil
				waitingOverride = "waiting on background tasks"
				record["waiting_on"] = waitingOverride
			} else {
				if eventName == "stop" {
					state = "done"
				}
				record["waiting_on"] = nil
			}
		}
	} else if state != "working" {
		record["waiting_on"] = nil
	} else if truthy(record["waiting_on"]) {
		nextActiveFinite, _ := collectBackgroundWork(record)
		if len(nextActiveFinite) > 0 {
			w := formatWaitingText(nextActiveFinite)
			if stringKey(record["text"]) == stringKey(record["waiting_on"]) {
				record["text"] = w
			}
			record["waiting_on"] = w
		} else if len(prevActiveFinite) > 0 {
			record["waiting_on"] = nil
		}
	}

	if previousState != state || eventName == "session_start" {
		record["state_since_ms"] = now
	}

	record["state"] = state
	record["kind"] = kind
	for _, key := range []string{"agent", "app", "source", "event", "raw_event", "text"} {
		if v, ok := event[key].(string); ok && v != "" {
			record[key] = v
		} else if _, ok := record[key]; !ok {
			record[key] = ""
		}
	}
	if waitingOverride != "" {
		record["text"] = waitingOverride
	} else if eventName == "stop" && state == "done" && stringKey(record["text"]) == "waiting on background tasks" {
		if r := oneLine(event["reply"], MaxText, true); r != "" {
			record["text"] = r
		} else {
			record["text"] = "turn complete"
		}
	}
	record["ts_ms"] = now
	if seq, ok := event["seq"]; ok && seq != nil {
		record["seq"] = seq
	} else {
		record["seq"] = now
	}
	for _, key := range []string{
		"title", "prompt", "tool", "tool_input", "tool_output", "reply",
		"model", "profile", "session_id", "cwd", "background",
	} {
		if v, ok := event[key]; ok && v != nil {
			record[key] = v
		}
	}
	if event["usage"] != nil {
		if u := mergeUsage(record["usage"], event["usage"]); u != nil {
			record["usage"] = u
		}
	}
	// Keep integer background count synced with active tracked background tasks.
	if bgList, ok := record["background_tasks"].([]any); ok && len(bgList) > 0 {
		activeBg := 0
		for _, raw := range bgList {
			if m, ok := raw.(map[string]any); ok && isActiveBgState(stringKey(m["state"])) {
				activeBg++
			}
		}
		record["background"] = activeBg
	}
	if !truthy(record["title"]) && truthy(record["prompt"]) {
		record["title"] = fieldStr(record["prompt"], MaxText, false)
	}
	record["last_event"] = map[string]any{
		"event": event["event"], "agent": event["agent"], "ts_ms": now,
	}
	return record, true
}

func codexChildEventApplies(children []any, child map[string]any, event string) bool {
	if event == "subagent_start" {
		return true
	}
	for _, raw := range children {
		if known, ok := raw.(map[string]any); ok && stringKey(known["id"]) == stringKey(child["id"]) {
			return event == "subagent_stop" || isActiveBgState(stringKey(known["state"]))
		}
	}
	return false
}

// recordIsStale mirrors _record_is_stale.
func recordIsStale(record map[string]any, now int64) bool {
	state, _ := record["state"].(string)
	kind, _ := record["kind"].(string)
	ts := toInt64(record["ts_ms"])
	since := toInt64(record["state_since_ms"])
	if record["state_since_ms"] == nil {
		since = ts
	}
	switch state {
	case "working":
		activeFinite, _ := collectBackgroundWork(record)
		if len(activeFinite) > 0 || truthy(record["waiting_on"]) {
			return false
		}
		return now-ts > WorkingStaleMS
	case "idle":
		return now-ts > WorkingStaleMS
	case "done":
		return now-since > DoneTTLMS
	case "failed":
		return now-since > FailedTTLMS
	case "blocked":
		if kind == "auth" {
			return now-since > AuthTTLMS
		}
		return now-since > BlockTTLMS
	default:
		return now-ts > WorkingStaleMS
	}
}

// PruneStore drops stale pane records and expired outcomes, mirroring prune_store.
func PruneStore(store map[string]any, now int64) map[string]any {
	var panes map[string]any
	var rawOutcomes any
	if store != nil {
		panes, _ = store["panes"].(map[string]any)
		rawOutcomes = store["outcomes"]
	}
	cleaned := map[string]any{}
	if panes != nil {
		for session, v := range panes {
			sessionPanes, ok := v.(map[string]any)
			if !ok {
				continue
			}
			kept := map[string]any{}
			for pane, r := range sessionPanes {
				rec, ok := r.(map[string]any)
				if ok && !recordIsStale(rec, now) {
					cp := make(map[string]any, len(rec))
					for k, val := range rec {
						cp[k] = val
					}
					if stringKey(rec["agent"]) == "codex" {
						if started := toInt64(rec["turn_started_ms"]); started > 0 {
							children, _ := rec["children"].([]any)
							var current []any
							for _, raw := range children {
								child, ok := raw.(map[string]any)
								if ok && (isActiveBgState(stringKey(child["state"])) || toInt64(child["ended_ms"]) >= started) {
									current = append(current, child)
								}
							}
							cp["children"] = current
						}
					}
					kept[pane] = cp
				}
			}
			if len(kept) > 0 {
				cleaned[stringKey(session)] = kept
			}
		}
	}
	return map[string]any{
		"v":          Version,
		"updated_ms": now,
		"panes":      cleaned,
		"outcomes":   pruneOutcomes(rawOutcomes, now),
	}
}

func pruneOutcomes(raw any, now int64) []any {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return []any{}
	}
	kept := make([]map[string]any, 0, len(list))
	for _, item := range list {
		rec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		outcome, _ := rec["outcome"].(string)
		if outcome != "done" && outcome != "failed" {
			continue
		}
		ts := toInt64(rec["ts_ms"])
		if ts > 0 && now > ts && now-ts > OutcomeTTLMS {
			continue
		}
		cp := make(map[string]any, len(rec))
		for k, val := range rec {
			cp[k] = val
		}
		kept = append(kept, cp)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return toInt64(kept[i]["ts_ms"]) > toInt64(kept[j]["ts_ms"])
	})
	deduped := make([]map[string]any, 0, len(kept))
	seenConv := map[string]bool{}
	for _, m := range kept {
		if key := outcomeConversationKey(m); key != "" {
			if seenConv[key] {
				continue
			}
			seenConv[key] = true
		}
		deduped = append(deduped, m)
		if len(deduped) >= MaxOutcomes {
			break
		}
	}
	out := make([]any, len(deduped))
	for i, m := range deduped {
		out[i] = m
	}
	return out
}

func outcomeConversationKey(rec map[string]any) string {
	sid := strings.TrimSpace(stringKey(rec["session_id"]))
	if sid == "" {
		return ""
	}
	agent := strings.ToLower(strings.TrimSpace(stringKey(rec["agent"])))
	if agent == "" {
		agent = strings.ToLower(strings.TrimSpace(stringKey(rec["app"])))
	}
	return agent + "\x00" + sid
}

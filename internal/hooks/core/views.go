package core

// truthy mirrors Python truthiness for record fields.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// SpaceHookTitleResolver resolves a conversation title from a local harness
// session store when a hook record has no explicit title or prompt.
var SpaceHookTitleResolver func(agent, cwd, sessionID string) string

// SpaceHookProfileResolver infers a harness profile name from a local harness
// session store when an outcome record has a session_id but no explicit profile.
var SpaceHookProfileResolver func(agent, sessionID string) string

// summaryRecord flattens a pane record to the plugin-facing summary shape,
// mirroring _summary, with ATT-5 / ATT-9 background work metadata included.
func summaryRecord(record map[string]any) map[string]any {
	children, _ := record["children"].([]any)
	childCount := len(children)
	bgTasks, _ := record["background_tasks"].([]any)
	activeFinite, activeServices := collectBackgroundWork(record)
	bgCount := int(toInt64(record["background"]))
	if activeTotal := len(activeFinite) + len(activeServices); activeTotal > bgCount && len(bgTasks) > 0 {
		// Count active non-child background tasks.
		activeBg := 0
		for _, raw := range bgTasks {
			if m, ok := raw.(map[string]any); ok && isActiveBgState(stringKey(m["state"])) {
				activeBg++
			}
		}
		if activeBg > bgCount {
			bgCount = activeBg
		}
	}
	hasDetail := truthy(record["prompt"]) ||
		truthy(record["tool_input"]) ||
		truthy(record["tool_output"]) ||
		truthy(record["reply"]) ||
		childCount > 0 ||
		len(bgTasks) > 0 ||
		bgCount > 0
	agent, _ := record["agent"].(string)
	app, _ := record["app"].(string)
	state, _ := record["state"].(string)
	if state == "" {
		state = "idle"
	}
	event, _ := record["event"].(string)
	text, _ := record["text"].(string)
	title := record["title"]
	if !truthy(title) && SpaceHookTitleResolver != nil {
		cwd, _ := record["cwd"].(string)
		sid, _ := record["session_id"].(string)
		if agent != "" && (sid != "" || cwd != "") {
			if resolved := SpaceHookTitleResolver(agent, cwd, sid); resolved != "" {
				title = resolved
			}
		}
	}
	if !truthy(title) && truthy(record["prompt"]) {
		title = fieldStr(record["prompt"], MaxText, false)
	}
	out := map[string]any{
		"agent":           agent,
		"app":             app,
		"state":           state,
		"kind":            record["kind"],
		"event":           event,
		"ts_ms":           toInt64(record["ts_ms"]),
		"state_since_ms":  toInt64(record["state_since_ms"]),
		"turn_started_ms": toInt64(record["turn_started_ms"]),
		"text":            text,
		"title":           title,
		"model":           record["model"],
		"session_id":      record["session_id"],
		"cwd":             record["cwd"],
		"child_count":     childCount,
		"background":      bgCount,
		"finite_active":   len(activeFinite),
		"service_active":  len(activeServices),
		"has_detail":      hasDetail,
	}
	if truthy(record["waiting_on"]) {
		out["waiting_on"] = record["waiting_on"]
	}
	if len(children) > 0 {
		out["children"] = children
	}
	if len(bgTasks) > 0 {
		out["background_tasks"] = bgTasks
	}
	return out
}

// mergedView loads the store, replays fresh spool events over it in memory
// and prunes, mirroring _merged_view. It returns the view plus the number
// of pending spool events.
func mergedView() (map[string]any, int) {
	store := LoadStore()
	events := ReadSpoolEvents(MaxSpoolLines)
	now := nowMs()
	for _, entry := range events {
		ApplyEventToCopy(store, entry, now)
	}
	return PruneStore(store, now), len(events)
}

// summaryOutcome flattens one recent outcome record to the plugin-facing shape,
// resolving a conversation title from the local session store when absent.
func summaryOutcome(record map[string]any) map[string]any {
	session, _ := record["session"].(string)
	pane := stringKey(record["pane"])
	agent, _ := record["agent"].(string)
	app, _ := record["app"].(string)
	outcome, _ := record["outcome"].(string)
	text, _ := record["text"].(string)
	title := record["title"]
	if !truthy(title) && SpaceHookTitleResolver != nil {
		cwd, _ := record["cwd"].(string)
		sid, _ := record["session_id"].(string)
		if agent != "" && (sid != "" || cwd != "") {
			if resolved := SpaceHookTitleResolver(agent, cwd, sid); resolved != "" {
				title = resolved
			}
		}
	}
	profile := record["profile"]
	if !truthy(profile) && SpaceHookProfileResolver != nil {
		sid, _ := record["session_id"].(string)
		if agent != "" && sid != "" {
			if resolved := SpaceHookProfileResolver(agent, sid); resolved != "" {
				profile = resolved
			}
		}
	}
	return map[string]any{
		"session":    session,
		"pane":       pane,
		"agent":      agent,
		"app":        app,
		"profile":    profile,
		"outcome":    outcome,
		"text":       text,
		"title":      title,
		"model":      record["model"],
		"session_id": record["session_id"],
		"cwd":        record["cwd"],
		"ts_ms":      toInt64(record["ts_ms"]),
	}
}

// SpaceHooksSummary renders the plugin's space overview, mirroring
// space_hooks_summary: {ok, version, now_ms, spool_pending, panes, outcomes}
// with string pane keys.
func SpaceHooksSummary() map[string]any {
	store, pending := mergedView()
	panes := map[string]any{}
	if storePanes, ok := store["panes"].(map[string]any); ok {
		for session, v := range storePanes {
			sessionPanes, ok := v.(map[string]any)
			if !ok {
				continue
			}
			flat := map[string]any{}
			for pane, r := range sessionPanes {
				if rec, ok := r.(map[string]any); ok {
					flat[stringKey(pane)] = summaryRecord(rec)
				}
			}
			panes[stringKey(session)] = flat
		}
	}
	outcomes := []any{}
	if storeOutcomes, ok := store["outcomes"].([]any); ok {
		outcomes = make([]any, 0, len(storeOutcomes))
		for _, item := range storeOutcomes {
			if rec, ok := item.(map[string]any); ok {
				outcomes = append(outcomes, summaryOutcome(rec))
			}
		}
	}
	return map[string]any{
		"ok":            true,
		"version":       Version,
		"now_ms":        nowMs(),
		"spool_pending": pending,
		"panes":         panes,
		"outcomes":      outcomes,
	}
}

// SpaceHooksDetail renders one pane's full record, mirroring
// space_hooks_detail: the tool triple is flattened to
// {name, input, output} and child_count/has_detail are attached.
func SpaceHooksDetail(session string, pane any) map[string]any {
	store, _ := mergedView()
	var record map[string]any
	if storePanes, ok := store["panes"].(map[string]any); ok {
		if sessionPanes, ok := storePanes[session].(map[string]any); ok {
			record, _ = sessionPanes[stringKey(pane)].(map[string]any)
		}
	}
	if record == nil {
		return map[string]any{"ok": true, "version": Version, "record": nil}
	}
	detail := make(map[string]any, len(record)+2)
	for k, v := range record {
		detail[k] = v
	}
	toolName, _ := detail["tool"].(string)
	if toolName != "" || truthy(detail["tool_input"]) {
		detail["tool"] = map[string]any{
			"name":   toolName,
			"input":  valueOr(detail["tool_input"], ""),
			"output": detail["tool_output"],
		}
	} else {
		delete(detail, "tool")
	}
	children, _ := detail["children"].([]any)
	bgTasks, _ := detail["background_tasks"].([]any)
	activeFinite, activeServices := collectBackgroundWork(record)
	detail["child_count"] = len(children)
	detail["finite_active"] = len(activeFinite)
	detail["service_active"] = len(activeServices)
	detail["has_detail"] = truthy(detail["prompt"]) ||
		truthy(detail["tool_input"]) ||
		truthy(detail["tool_output"]) ||
		truthy(detail["reply"]) ||
		len(children) > 0 ||
		len(bgTasks) > 0 ||
		toInt64(detail["background"]) > 0
	return map[string]any{"ok": true, "version": Version, "record": detail}
}

func valueOr(v any, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}

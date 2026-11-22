package core

import (
	"os"
	"sort"
	"strings"
	"syscall"
)

// storeLock opens the store lock file and takes an exclusive flock,
// mirroring _store_lock. The caller must release it with unlockFile.
func storeLock() (*os.File, error) {
	if _, err := EnsureDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(StoreLockPath(), os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// unlockFile releases the flock and closes the handle, mirroring _unlock.
func unlockFile(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// LoadStore reads the store file, mirroring load_store: malformed or
// missing files degrade to an empty store.
func LoadStore() map[string]any {
	v := readJSONFile(StorePath())
	data, ok := v.(map[string]any)
	if !ok {
		return map[string]any{"v": Version, "updated_ms": nowMs(), "panes": map[string]any{}, "outcomes": []any{}}
	}
	panes, ok := data["panes"].(map[string]any)
	if !ok {
		return map[string]any{"v": Version, "updated_ms": nowMs(), "panes": map[string]any{}, "outcomes": []any{}}
	}
	out := map[string]any{}
	for session, v := range panes {
		sessionPanes, ok := v.(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{}
		for pane, r := range sessionPanes {
			if rec, ok := r.(map[string]any); ok {
				cp := make(map[string]any, len(rec))
				for k, val := range rec {
					cp[k] = val
				}
				kept[stringKey(pane)] = cp
			}
		}
		out[stringKey(session)] = kept
	}
	outcomes := []any{}
	if rawOutcomes, ok := data["outcomes"].([]any); ok {
		outcomes = make([]any, 0, len(rawOutcomes))
		for _, item := range rawOutcomes {
			if rec, ok := item.(map[string]any); ok {
				cp := make(map[string]any, len(rec))
				for k, val := range rec {
					cp[k] = val
				}
				outcomes = append(outcomes, cp)
			}
		}
	}
	return map[string]any{
		"v":          Version,
		"updated_ms": toInt64(data["updated_ms"]),
		"panes":      out,
		"outcomes":   outcomes,
	}
}

// WriteStore persists the store atomically (tmp+rename, 0600), mirroring
// write_store. Unlike append_spool, write failures propagate.
func WriteStore(store map[string]any) error {
	payload, err := marshalJSON(store)
	if err != nil {
		return err
	}
	return atomicWrite(StorePath(), payload, 0o600)
}

// MergeIntoStore folds a normalized envelope into the pane record and
// persists the pruned store under the store flock, mirroring
// merge_into_store.
func MergeIntoStore(session string, pane any, event map[string]any) error {
	now := nowMs()
	handle, err := storeLock()
	if err != nil {
		return err
	}
	defer unlockFile(handle)
	store := LoadStore()
	foldEventIntoStore(store, session, pane, event, now)
	return WriteStore(PruneStore(store, now))
}

// ApplyEventToCopy folds a spool/store entry (session+pane+envelope fields)
// into an in-memory store copy, mirroring apply_event_to_copy.
func ApplyEventToCopy(store map[string]any, entry map[string]any, now int64) {
	session, _ := entry["session"]
	pane, hasPane := entry["pane"]
	if session == nil || !hasPane || pane == nil {
		return
	}
	event := make(map[string]any, len(entry))
	for k, v := range entry {
		if k != "session" && k != "pane" {
			event[k] = v
		}
	}
	foldEventIntoStore(store, stringKey(session), pane, event, now)
}

func foldEventIntoStore(store map[string]any, session string, pane any, event map[string]any, now int64) {
	panes, ok := store["panes"].(map[string]any)
	if !ok {
		panes = map[string]any{}
		store["panes"] = panes
	}
	sessionPanes, ok := panes[session].(map[string]any)
	if !ok {
		sessionPanes = map[string]any{}
		panes[session] = sessionPanes
	}
	key := stringKey(pane)
	var previous map[string]any
	previousState := ""
	if p, ok := sessionPanes[key].(map[string]any); ok {
		previous = p
		previousState, _ = p["state"].(string)
	}
	merged, applied := mergeEventApplied(previous, event, now)
	sessionPanes[key] = merged
	if applied {
		recordOutcomeIfTerminal(store, session, key, previousState, merged, event, now)
	}
}

func recordOutcomeIfTerminal(store map[string]any, session, paneKey, previousState string, merged, event map[string]any, now int64) {
	if child, ok := event["child"].(map[string]any); ok && child != nil {
		return
	}
	newState, _ := merged["state"].(string)
	if (newState != "done" && newState != "failed") || previousState == newState {
		return
	}
	tsMs := toInt64(event["ts_ms"])
	if tsMs <= 0 {
		tsMs = now
	}
	title := merged["title"]
	if !truthy(title) && truthy(merged["prompt"]) {
		title = fieldStr(merged["prompt"], MaxText, false)
	}
	agent, _ := merged["agent"].(string)
	app, _ := merged["app"].(string)
	text, _ := merged["text"].(string)
	entry := map[string]any{
		"session":    session,
		"pane":       paneKey,
		"agent":      agent,
		"app":        app,
		"outcome":    newState,
		"text":       text,
		"title":      title,
		"model":      merged["model"],
		"profile":    merged["profile"],
		"session_id": merged["session_id"],
		"cwd":        merged["cwd"],
		"ts_ms":      tsMs,
	}
	appendOutcome(store, entry, DedupeMS)
}

// RecordOutcome persists one recent completion ("done") or failure ("failed")
// directly into hooks.store.json under the store flock. It is safe to call
// alongside the hook listener daemon and deduplicates repeated reports for the
// same (session, pane, outcome) within OutcomeDedupeMS.
func RecordOutcome(entry map[string]any) error {
	if Disabled() || entry == nil {
		return nil
	}
	outcome := strings.ToLower(strings.TrimSpace(stringKey(entry["outcome"])))
	switch outcome {
	case "done", "complete", "completed", "success":
		outcome = "done"
	case "failed", "failure", "error":
		outcome = "failed"
	default:
		return nil
	}
	session := strings.TrimSpace(stringKey(entry["session"]))
	if session == "" {
		return nil
	}
	agent := strings.TrimSpace(stringKey(entry["agent"]))
	app := strings.TrimSpace(stringKey(entry["app"]))
	if agent == "" && app == "" {
		return nil
	}
	if agent == "" {
		agent = CanonicalAgent(app, map[string]string{})
	}
	if app == "" {
		app = DisplayApp(agent)
	}
	now := nowMs()
	tsMs := toInt64(entry["ts_ms"])
	if tsMs <= 0 {
		tsMs = now
	}
	normalized := map[string]any{
		"session":    session,
		"pane":       strings.TrimPrefix(strings.TrimSpace(stringKey(entry["pane"])), "%"),
		"agent":      agent,
		"app":        app,
		"outcome":    outcome,
		"text":       oneLine(entry["text"], MaxText, false),
		"title":      fieldStr(entry["title"], MaxText, false),
		"model":      fieldStr(entry["model"], MaxModel, false),
		"profile":    fieldStr(entry["profile"], MaxModel, false),
		"session_id": fieldStr(entry["session_id"], MaxSession, false),
		"cwd":        fieldStr(entry["cwd"], MaxCWD, false),
		"ts_ms":      tsMs,
	}

	handle, err := storeLock()
	if err != nil {
		return err
	}
	defer unlockFile(handle)
	store := LoadStore()
	appendOutcome(store, normalized, OutcomeDedupeMS)
	return WriteStore(PruneStore(store, now))
}

// PaneState returns the current non-stale state recorded for (session, pane)
// in the store (e.g. "working", "done", "failed"), or "" if absent/stale.
func PaneState(session string, pane any) string {
	store, _ := mergedView()
	panes, ok := store["panes"].(map[string]any)
	if !ok {
		return ""
	}
	sessionPanes, ok := panes[session].(map[string]any)
	if !ok {
		return ""
	}
	rec, ok := sessionPanes[stringKey(pane)].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := rec["state"].(string)
	return s
}

// ClearPane removes the live pane record for (session, pane) from
// hooks.store.json under the store flock without touching persisted outcomes.
// Called when a supervised agent exits back to a shell (e.g. via Ctrl+D) so
// stale hook state never keeps the exited agent alive in Spaces or Inbox or
// leaks into the next agent launched in the same pane.
func ClearPane(session string, pane any) error {
	if Disabled() || strings.TrimSpace(session) == "" || pane == nil {
		return nil
	}
	paneKey := strings.TrimPrefix(strings.TrimSpace(stringKey(pane)), "%")
	if paneKey == "" {
		return nil
	}
	now := nowMs()
	handle, err := storeLock()
	if err != nil {
		return err
	}
	defer unlockFile(handle)
	store := LoadStore()
	panes, ok := store["panes"].(map[string]any)
	if !ok {
		return nil
	}
	sessionPanes, ok := panes[session].(map[string]any)
	if !ok {
		return nil
	}
	if _, exists := sessionPanes[paneKey]; !exists {
		return nil
	}
	delete(sessionPanes, paneKey)
	if len(sessionPanes) == 0 {
		delete(panes, session)
	}
	return WriteStore(PruneStore(store, now))
}

// RemoveOutcome deletes matching recent outcome entries from hooks.store.json
// under the store flock (for example when the user reopens a completed
// conversation into a live tab so it moves back to idle instead of lingering
// in recent outcomes).
func RemoveOutcome(match map[string]any) error {
	if Disabled() || match == nil {
		return nil
	}
	convKey := outcomeConversationKey(match)
	session := strings.TrimSpace(stringKey(match["session"]))
	paneKey := strings.TrimPrefix(strings.TrimSpace(stringKey(match["pane"])), "%")
	tsMs := toInt64(match["ts_ms"])
	if convKey == "" && session == "" {
		return nil
	}

	now := nowMs()
	handle, err := storeLock()
	if err != nil {
		return err
	}
	defer unlockFile(handle)
	store := LoadStore()
	existing, _ := store["outcomes"].([]any)
	kept := make([]any, 0, len(existing))
	for _, raw := range existing {
		rec, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if convKey != "" && outcomeConversationKey(rec) == convKey {
			continue
		}
		if convKey == "" && session != "" {
			recSession, _ := rec["session"].(string)
			recPane := strings.TrimPrefix(strings.TrimSpace(stringKey(rec["pane"])), "%")
			if recSession == session && (paneKey == "" || recPane == paneKey) && (tsMs <= 0 || toInt64(rec["ts_ms"]) == tsMs) {
				continue
			}
		}
		kept = append(kept, rec)
	}
	store["outcomes"] = kept
	return WriteStore(PruneStore(store, now))
}

func appendOutcome(store map[string]any, entry map[string]any, dedupeWindow int64) {
	session, _ := entry["session"].(string)
	paneKey := stringKey(entry["pane"])
	outcome, _ := entry["outcome"].(string)
	tsMs := toInt64(entry["ts_ms"])
	convKey := outcomeConversationKey(entry)

	existing, _ := store["outcomes"].([]any)
	items := make([]map[string]any, 0, len(existing)+1)
	for _, raw := range existing {
		rec, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// One entry per conversation: when the same (agent, session_id)
		// completes or fails again (e.g. after reopening and closing), replace
		// the older entry with the latest outcome instead of duplicating it.
		if convKey != "" && outcomeConversationKey(rec) == convKey {
			recOutcome, _ := rec["outcome"].(string)
			d := tsMs - toInt64(rec["ts_ms"])
			if d < 0 {
				d = -d
			}
			// A generic exit "finished" ("done") arriving right after a failed
			// outcome (e.g. async __osc-watch delivering attn.Finish(0) after
			// recordLaunchOutcome preserved a failed turn) must not overwrite
			// the failure back to done.
			if recOutcome == "failed" && outcome == "done" &&
				strings.EqualFold(strings.TrimSpace(stringKey(entry["text"])), "finished") &&
				d <= dedupeWindow {
				return
			}
			// Preserve richer metadata from the existing entry for the same conversation.
			if !truthy(entry["title"]) && truthy(rec["title"]) {
				entry["title"] = rec["title"]
			}
			if !truthy(entry["profile"]) && truthy(rec["profile"]) {
				entry["profile"] = rec["profile"]
			}
			if !truthy(entry["model"]) && truthy(rec["model"]) {
				entry["model"] = rec["model"]
			}
			if !truthy(entry["cwd"]) && truthy(rec["cwd"]) {
				entry["cwd"] = rec["cwd"]
			}
			continue
		}
		if recSession, _ := rec["session"].(string); recSession == session {
			if recPane := stringKey(rec["pane"]); recPane == paneKey {
				recOutcome, _ := rec["outcome"].(string)
				d := tsMs - toInt64(rec["ts_ms"])
				if d < 0 {
					d = -d
				}
				if d <= dedupeWindow {
					if recOutcome == outcome {
						return
					}
					if recOutcome == "failed" && outcome == "done" &&
						strings.EqualFold(strings.TrimSpace(stringKey(entry["text"])), "finished") {
						return
					}
				}
			}
		}
		items = append(items, rec)
	}
	items = append([]map[string]any{entry}, items...)
	sort.SliceStable(items, func(i, j int) bool {
		return toInt64(items[i]["ts_ms"]) > toInt64(items[j]["ts_ms"])
	})
	if len(items) > MaxOutcomes {
		items = items[:MaxOutcomes]
	}
	out := make([]any, len(items))
	for i, m := range items {
		out[i] = m
	}
	store["outcomes"] = out
}

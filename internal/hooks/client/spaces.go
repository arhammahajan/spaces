package client

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// SpaceHooks implements `__space-hooks [--detail SESSION PANE | --titles JSON | --record-outcome JSON | --remove-outcome JSON]`.
func SpaceHooks(args []string) int {
	if SpaceHooksSummary == nil {
		out(marshalObject([]jsonField{
			{"ok", false}, {"version", 0},
			{"panes", map[string]any{}}, {"sessions", map[string]any{}},
			{"outcomes", []any{}},
		}, false, true))
		return 0
	}
	if len(args) > 0 && args[0] == "--titles" {
		return SpaceHookTitles(args[1:])
	}
	if len(args) > 0 && args[0] == "--record-outcome" {
		return SpaceHookRecordOutcome(args[1:])
	}
	if len(args) > 0 && args[0] == "--remove-outcome" {
		return SpaceHookRemoveOutcome(args[1:])
	}
	if len(args) > 0 && args[0] == "--detail" {
		if len(args) < 3 {
			errf("usage: __space-hooks --detail SESSION PANE\n")
			return 2
		}
		var pane any = args[2]
		if detailInt, ok := paneInt(args[2]); ok {
			pane = detailInt
		}
		var detail map[string]any
		if SpaceHooksDetail != nil {
			detail = SpaceHooksDetail(args[1], pane)
		}
		if detail == nil {
			detail = map[string]any{}
		}
		out(marshalPython(detail, false))
		return 0
	}
	out(marshalPython(SpaceHooksSummary(), false))
	return 0
}

// TitleQuery is one pane query for `__space-hooks --titles`.
type TitleQuery struct {
	Key       string `json:"key"`
	Agent     string `json:"agent"`
	CWD       string `json:"cwd,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// SpaceHookTitles resolves conversation titles via DiscoverTitleFunc
// for a JSON array of TitleQuery objects passed as the first argument.
func SpaceHookTitles(args []string) int {
	titles := map[string]string{}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		var queries []TitleQuery
		if err := json.Unmarshal([]byte(args[0]), &queries); err == nil {
			type lookupKey struct {
				agent, cwd, sid string
			}
			cache := map[lookupKey]string{}
			for _, q := range queries {
				if q.Key == "" || q.Agent == "" {
					continue
				}
				lk := lookupKey{agent: q.Agent, cwd: q.CWD, sid: q.SessionID}
				t, seen := cache[lk]
				if !seen {
					t = discoverTitle(q.Agent, q.CWD, q.SessionID)
					cache[lk] = t
				}
				if t != "" {
					titles[q.Key] = t
				}
			}
		}
	}
	out(marshalPython(map[string]any{"ok": true, "titles": titles}, false))
	return 0
}

// EnrichOutcomeMap enriches a mutable outcome record map in place, filling
// missing session, pane, CWD, profile, session_id, title, agent, and app from
// tmux and local harness resolvers, and reconciling prior failure markers.
func EnrichOutcomeMap(entry map[string]any) {
	if entry == nil {
		return
	}
	session, _ := entry["session"].(string)
	paneStr := ""
	switch pv := entry["pane"].(type) {
	case string:
		paneStr = strings.TrimSpace(pv)
	case float64:
		paneStr = itoa(int(pv))
	}
	profile, _ := entry["profile"].(string)
	cwd, _ := entry["cwd"].(string)
	sid, _ := entry["session_id"].(string)
	title, _ := entry["title"].(string)

	if paneStr == "" {
		if s, p, ok := hookTarget(); ok {
			if session == "" {
				session = s
				entry["session"] = s
			}
			paneStr = p
			entry["pane"] = paneStr
		}
	}
	// Query tmux pane metadata whenever any context field is missing so
	// outcomes recorded via OSC 7501 or __shim still capture @spaces_profile,
	// @spaces_resume_session, @spaces_title, @spaces_prior_outcome, session, and pane CWD.
	priorOutcome := ""
	if paneStr != "" && TmuxFunc != nil {
		paneRef := paneStr
		if !strings.HasPrefix(paneRef, "%") {
			paneRef = "%" + paneRef
		}
		if stdout, code, err := TmuxFunc([]string{
			"display-message", "-p", "-t", paneRef,
			"#{session_name}\x1f#{pane_current_path}\x1f#{@spaces_profile}\x1f#{@spaces_resume_session}\x1f#{@spaces_title}\x1f#{@spaces_prior_outcome}",
		}, 2*time.Second); err == nil && code == 0 {
			parts := strings.Split(strings.TrimSuffix(stdout, "\n"), "\x1f")
			getPart := func(i int) string {
				if i < len(parts) {
					v := strings.TrimSpace(parts[i])
					if v != "" && v != "-" {
						return v
					}
				}
				return ""
			}
			if session == "" {
				if v := getPart(0); v != "" {
					session = v
					entry["session"] = session
				}
			}
			if strings.TrimSpace(cwd) == "" {
				if v := getPart(1); v != "" {
					cwd = v
					entry["cwd"] = cwd
				}
			}
			if strings.TrimSpace(profile) == "" {
				if v := getPart(2); v != "" {
					profile = v
					entry["profile"] = profile
				}
			}
			if strings.TrimSpace(sid) == "" {
				if v := getPart(3); v != "" {
					sid = v
					entry["session_id"] = sid
				}
			}
			if strings.TrimSpace(title) == "" {
				if v := getPart(4); v != "" {
					title = v
					entry["title"] = title
				}
			}
			priorOutcome = getPart(5)
		}
	}
	if session == "" {
		if s, p, ok := hookTarget(); ok {
			session = s
			entry["session"] = s
			if paneStr == "" {
				paneStr = p
				entry["pane"] = paneStr
			}
		}
	}
	if paneStr != "" {
		entry["pane"] = strings.TrimPrefix(paneStr, "%")
	}

	agentName, _ := entry["agent"].(string)
	app, _ := entry["app"].(string)
	if agentName == "" && app != "" {
		if norm := Normalize(app); norm != "" {
			agentName = norm
		} else {
			agentName = strings.ToLower(strings.TrimSpace(app))
		}
		entry["agent"] = agentName
	}
	if app == "" && agentName != "" {
		app = DisplayApp(agentName)
		entry["app"] = app
	}
	if strings.TrimSpace(profile) == "" {
		if envProf := strings.TrimSpace(os.Getenv("HARNESS_PROFILE")); envProf != "" {
			profile = envProf
			entry["profile"] = profile
		}
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = CurrentDirectory()
		entry["cwd"] = cwd
	}
	if strings.TrimSpace(profile) == "" && agentName != "" && strings.TrimSpace(sid) != "" {
		if inferred := DiscoverProfile(agentName, cwd, sid); inferred != "" {
			profile = inferred
			entry["profile"] = profile
		}
	}
	if strings.TrimSpace(title) == "" && agentName != "" && (sid != "" || cwd != "") {
		if t := discoverTitle(agentName, cwd, sid); t != "" {
			entry["title"] = t
		}
	}

	outcomeStr, _ := entry["outcome"].(string)
	textStr, _ := entry["text"].(string)
	if strings.EqualFold(strings.TrimSpace(outcomeStr), "done") {
		paneFailed := PaneState != nil && session != "" && paneStr != "" &&
			PaneState(session, strings.TrimPrefix(paneStr, "%")) == "failed"
		if strings.EqualFold(strings.TrimSpace(textStr), "finished") && (priorOutcome == "failed" || paneFailed) {
			// Closing a failed conversation keeps it failed instead of flipping to done.
			entry["outcome"] = "failed"
		} else if !strings.EqualFold(strings.TrimSpace(textStr), "finished") && paneStr != "" && TmuxFunc != nil {
			// A completed turn clears any prior failure marker on the pane.
			paneRef := paneStr
			if !strings.HasPrefix(paneRef, "%") {
				paneRef = "%" + paneRef
			}
			_, _, _ = TmuxFunc([]string{"set-option", "-p", "-u", "-t", paneRef, "@spaces_prior_outcome"}, 2*time.Second)
		}
	}
}

// SpaceHookRecordOutcome records one recent completion or failure outcome JSON
// object into the hook store, enriching missing session/pane/session_id/title
// from the tmux environment and DiscoverProfile/DiscoverTitleFunc.
func SpaceHookRecordOutcome(args []string) int {
	if RecordOutcome == nil || len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(args[0]), &entry); err != nil || entry == nil {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	EnrichOutcomeMap(entry)
	if err := RecordOutcome(entry); err != nil {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	out(marshalPython(map[string]any{"ok": true}, false))
	return 0
}

// SpaceHookRemoveOutcome removes a matching outcome entry from hooks.store.json
// when a user reopens that conversation into a live tab.
func SpaceHookRemoveOutcome(args []string) int {
	if RemoveOutcome == nil || len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	var match map[string]any
	if err := json.Unmarshal([]byte(args[0]), &match); err != nil || match == nil {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	if err := RemoveOutcome(match); err != nil {
		out(marshalPython(map[string]any{"ok": false}, false))
		return 0
	}
	out(marshalPython(map[string]any{"ok": true}, false))
	return 0
}

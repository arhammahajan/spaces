package model

import (
	"strconv"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// Hook staleness TTLs, matching zellij-spaces (app.rs) / spacecore:
// working heartbeats go quiet after 120s, terminal hook states linger
// 30m, auth blocks linger 60m.
const (
	hookWorkingStaleMs = 120_000
	hookDoneTTLMs      = int64(30 * 60_000)
	hookFailedTTLMs    = int64(30 * 60_000)
	hookBlockTTLMs     = int64(30 * 60_000)
	hookAuthTTLMs      = int64(60 * 60_000)
)

// doneTTLMs is how long a screen-synthesized working->idle Done is kept
// before decaying back to Idle. It clears early when the pane becomes
// active/focused.
const doneTTLMs = int64(10 * 60_000)

// ScreenReading is one pane's screen-detector result, produced by the
// screen package (Herdr manifest engine over `tmux capture-pane`).
// Readings are keyed by pane ID (e.g. "%12") in Arbitrator.Apply.
type ScreenReading struct {
	// App is the app label the viewport was evaluated for.
	App string
	// State is "idle", "working", "blocked", or "unknown".
	State           string
	VisibleIdle     bool
	VisibleBlocker  bool
	VisibleWorking  bool
	SkipStateUpdate bool
	// OSCAuthority marks panes whose live `@spaces_*` lifecycle is
	// complete; callers may set it from the screen detector. The
	// arbitrator also consults IsAuthorityApp(pane.App).
	OSCAuthority bool
	// Title is the conversation title parsed from the captured viewport.
	Title string
}

// canonicalApp normalizes an app label for comparison, mirroring
// zellij-spaces canonical_app aliases.
func canonicalApp(app string) string {
	return agent.DefaultRegistry().CanonicalOrNormalized(app)
}

// IsAuthorityApp reports whether app has a complete live lifecycle whose
// non-idle state wins over bare screen working/idle evidence.
func IsAuthorityApp(app string) bool {
	return agent.DefaultRegistry().IsOSCAuthority(app)
}

// idleLike reports whether s is an idle-family status that screen working
// evidence may promote.
func idleLike(s Status) bool {
	return s == StatusIdle || s == StatusQuiet || s == StatusNewOutput
}

// Arbitrator applies hook summaries and screen readings over freshly built
// spaces. It tracks per-pane screen-synthesized Done memory (working->idle
// transitions) with a 10-minute TTL, cleared when the pane becomes
// active/focused.
//
// Apply must be called on freshly built spaces every tick with the same
// Arbitrator: each pass recomputes from BaseStatus/BaseText/BaseAgeMs so
// repeated ticks are stable.
type Arbitrator struct {
	done      map[string]int64  // pane ID -> Done synthesis time (ms)
	prev      map[string]string // pane ID -> last observed screen state
	newlyDone []string          // pane IDs that transitioned working->done on the last Apply
}

// TakeNewlyDone returns and clears the pane IDs whose screen state just
// transitioned from working to done during the most recent Apply call.
func (a *Arbitrator) TakeNewlyDone() []string {
	if len(a.newlyDone) == 0 {
		return nil
	}
	out := append([]string(nil), a.newlyDone...)
	a.newlyDone = nil
	return out
}

// SeedAppsFromHooks fills in unresolved pane App labels from matching hook
// records before screen-reader capture runs, so panes whose process command is
// generic (e.g. "node" or "terminal") can still be evaluated by Layer 3.
func SeedAppsFromHooks(spaces []Space, hooks *HookSnapshot) {
	if hooks == nil {
		return
	}
	for si := range spaces {
		sessionHooks, ok := hooks.Panes[spaces[si].Name]
		if !ok {
			continue
		}
		for pi := range spaces[si].Panes {
			pane := &spaces[si].Panes[pi]
			if pane.ReturnedToShell || IsKnownAgent(pane.App) || isShellApp(pane.App) || IsShell(CommandName(pane.Command)) {
				continue
			}
			h, ok := sessionHooks[strconv.Itoa(pane.NumericID)]
			if !ok {
				continue
			}
			if app := strings.TrimSpace(h.App); app != "" && !isShellApp(app) {
				pane.App = app
			} else if agent := strings.TrimSpace(h.Agent); agent != "" && !isShellApp(agent) {
				pane.App = agent
			}
		}
	}
}

// NewArbitrator returns an empty Arbitrator.
func NewArbitrator() *Arbitrator {
	return &Arbitrator{
		done: make(map[string]int64),
		prev: make(map[string]string),
	}
}

// hookView decides whether a hook summary owns a pane's state, mirroring
// zellij-spaces hook_view: hooks win while fresh, hook idle never
// overrides, and a newer live base beats an older hook record.
func hookView(record *HookSummary, base Status, baseAgeMs, nowMs int64) (Status, int64, string, bool) {
	ts := record.TsMs
	since := record.StateSinceMs
	if since <= 0 {
		since = ts
	}
	stateAge := saturatingSub(nowMs, since)
	silence := saturatingSub(nowMs, ts)
	switch record.State {
	case "working":
		// Keep working indefinitely while finite background agents/tasks are
		// running; status depends on whether background work is still active
		// rather than a fixed timeout.
		if !record.HasActiveFiniteWork() && silence > hookWorkingStaleMs {
			return StatusIdle, 0, "", false
		}
	case "idle":
		if silence > hookWorkingStaleMs {
			return StatusIdle, 0, "", false
		}
	case "done":
		if stateAge > hookDoneTTLMs {
			return StatusIdle, 0, "", false
		}
	case "failed":
		if stateAge > hookFailedTTLMs {
			return StatusIdle, 0, "", false
		}
	case "blocked", "waiting":
		if record.IsAuthBlock() {
			if stateAge > hookAuthTTLMs {
				return StatusIdle, 0, "", false
			}
		} else if stateAge > hookBlockTTLMs {
			return StatusIdle, 0, "", false
		}
	}
	baseIsNewer := baseAgeMs > 0 && baseAgeMs < silence
	switch record.State {
	case "working":
		if (base == StatusDone || base == StatusFailed || base == StatusIdle) && baseIsNewer {
			return StatusIdle, 0, "", false
		}
		return StatusWorking, stateAge, record.Text, true
	case "blocked", "waiting":
		if !record.IsAuthBlock() && (base == StatusDone || base == StatusIdle) && baseIsNewer {
			return StatusIdle, 0, "", false
		}
		return StatusNeedsYou, stateAge, record.Text, true
	case "done":
		if (base == StatusWorking || base == StatusNeedsYou) && baseIsNewer {
			return StatusIdle, 0, "", false
		}
		return StatusDone, stateAge, record.Text, true
	case "failed":
		if (base == StatusWorking || base == StatusNeedsYou) && baseIsNewer {
			return StatusIdle, 0, "", false
		}
		return StatusFailed, stateAge, record.Text, true
	default:
		return StatusIdle, 0, "", false
	}
}

func saturatingSub(a, b int64) int64 {
	if a < b {
		return 0
	}
	return a - b
}

// Apply recomputes pane/space state from baselines, hooks, and screen
// readings. Precedence:
//
//	fresh hook (except hook idle, which never wins)
//	> strong VisibleBlocker (also overrides a non-blocker hook, matching
//	  zellij-spaces: a permission prompt actually visible on screen wins)
//	> live non-idle `@spaces_*` base for authority apps
//	> screen working/idle (working->idle synthesizes Done with TTL)
//	> base status
//
// After applying, Space Counts/Status and pane/space sort order are
// recomputed. hooks is keyed (session -> numeric pane ID string);
// readings is keyed by pane ID ("%12").
func (a *Arbitrator) Apply(spaces []Space, hooks *HookSnapshot, readings map[string]ScreenReading, nowMs int64) {
	if a.done == nil {
		a.done = make(map[string]int64)
	}
	if a.prev == nil {
		a.prev = make(map[string]string)
	}
	a.newlyDone = nil
	seen := make(map[string]bool, len(readings))
	for si := range spaces {
		space := &spaces[si]
		for pi := range space.Panes {
			pane := &space.Panes[pi]
			seen[pane.ID] = true

			// 1. Reset to baseline so ticks are idempotent.
			pane.Status = pane.BaseStatus
			pane.Text = pane.BaseText
			pane.AgeMs = pane.BaseAgeMs
			pane.ConversationTitle = pane.BaseConvTitle
			pane.ScreenTitle = ""
			pane.Hook = nil

			// Attach the hook summary (if any) regardless of freshness, unless the
			// pane has already returned to an interactive shell after the agent
			// exited (so a closed harness disappears immediately from the Inbox)
			// or the hook belongs to a different agent than the live process now
			// running in the pane.
			var hook *HookSummary
			if hooks != nil && !pane.ReturnedToShell {
				if sessionHooks, ok := hooks.Panes[space.Name]; ok {
					if h, ok := sessionHooks[strconv.Itoa(pane.NumericID)]; ok {
						// Ignore a hook record if the pane's live OS process
						// tree shows the pane is now just an interactive shell with
						// no live descendant agent process.
						if (isShellApp(pane.App) || IsShell(CommandName(pane.Command))) && pane.PID > 0 && ProcIsLiveFn != nil && ProcIsLiveFn(pane.PID) {
							if DetectDescendantAgentFn == nil || DetectDescendantAgentFn(pane.PID) == "" {
								goto skipHook
							}
						}
						// Ignore a stale hook record from a previous agent if a
						// different known agent is currently running in this pane.
						if IsKnownAgent(pane.App) {
							hookApp := strings.TrimSpace(h.App)
							if hookApp == "" {
								hookApp = strings.TrimSpace(h.Agent)
							}
							if hookApp != "" && canonicalApp(hookApp) != canonicalApp(pane.App) {
								goto skipHook
							}
						}
						hc := h
						hook = &hc
						pane.Hook = &hc
						if ht := strings.TrimSpace(h.Title); ht != "" {
							pane.ConversationTitle = ht
						}
						if !IsKnownAgent(pane.App) && !isShellApp(pane.App) {
							if app := strings.TrimSpace(h.App); app != "" && !isShellApp(app) {
								pane.App = app
							} else if agent := strings.TrimSpace(h.Agent); agent != "" && !isShellApp(agent) {
								pane.App = agent
							}
						}
					}
				}
			}
		skipHook:

			// 2. Screen pass over the base.
			base := pane.BaseStatus
			status := base
			clearDone := false
			reading, hasReading := readings[pane.ID]
			visibleBlocker := false
			prevNewlyDoneLen := len(a.newlyDone)
			if hasReading {
				if reading.App != "" && pane.App != "" &&
					canonicalApp(reading.App) != canonicalApp(pane.App) {
					// Stale reading evaluated for a different app; ignore.
					hasReading = false
				} else {
					visibleBlocker = reading.VisibleBlocker
					status, clearDone = a.screenStatus(pane, reading, base, nowMs)
					if st := strings.TrimSpace(reading.Title); st != "" {
						pane.ScreenTitle = st
					}
				}
			}
			if hasReading {
				// Track the last observed state so a later idle viewport
				// can recognize a finished working turn.
				switch reading.State {
				case "working", "idle", "blocked":
					a.prev[pane.ID] = reading.State
				}
			}

			// 3. Done-memory pass (screen-synthesized working->idle, 10m TTL).
			if doneAt, ok := a.done[pane.ID]; ok {
				expired := nowMs >= doneAt && nowMs-doneAt > doneTTLMs
				if clearDone || expired {
					delete(a.done, pane.ID)
					if status == StatusDone {
						status = StatusIdle
					}
				} else if status == StatusIdle {
					status = StatusDone
				}
				if status == StatusDone {
					pane.AgeMs = saturatingSub(nowMs, doneAt)
				}
			}

			// 4. Fresh hooks win over screen evidence, with one exception:
			// a permission/question prompt actually visible on screen.
			// Also yield when a stale working hook is superseded by a newer
			// screen-completed turn or contradicted by a visibly idle viewport
			// (e.g. Antigravity after a missed Stop or finished local_bash task).
			if hook != nil {
				hookSilence := saturatingSub(nowMs, hook.TsMs)
				screenFinishedNewerTurn := len(a.newlyDone) > prevNewlyDoneLen &&
					status == StatusDone &&
					pane.BaseAgeMs > 0 && pane.BaseAgeMs < hookSilence
				var policy agent.HookPolicy = agent.BaseHookPolicy{StaleLocalCmdMs: hookWorkingStaleMs}
				if p, ok := agent.DefaultRegistry().ResolveRuntime(pane.App); ok {
					policy = p.HookPolicy()
				}
				screenState := ""
				if hasReading {
					screenState = reading.State
				}
				yieldWorkingToIdle := policy.ShouldYieldWorkingHookToIdle(agent.HookArbitrationInput{
					HookState:                 hook.State,
					HookSilenceMs:             hookSilence,
					HasActiveFiniteWork:       hook.HasActiveFiniteWork(),
					HasOnlyLocalCmdBackground: hook.HasOnlyLocalCommandBackgroundWork(),
					VisibleIdle:               hasReading && reading.VisibleIdle,
					VisibleWorking:            hasReading && reading.VisibleWorking,
					VisibleBlocker:            hasReading && reading.VisibleBlocker,
					ScreenState:               screenState,
				})

				if !(hook.State == "working" && screenFinishedNewerTurn) && !yieldWorkingToIdle {
					if hs, age, text, fresh := hookView(hook, base, pane.BaseAgeMs, nowMs); fresh {
						status = hs
						pane.AgeMs = age
						if strings.TrimSpace(text) != "" {
							pane.Text = text
						}
						if visibleBlocker && status != StatusNeedsYou {
							status = StatusNeedsYou
						}
						if len(a.newlyDone) > prevNewlyDoneLen {
							a.newlyDone = a.newlyDone[:prevNewlyDoneLen]
						}
						if status != StatusDone {
							delete(a.done, pane.ID)
						}
					}
				}
			}

			pane.Status = status
		}
		var counts Counts
		for i := range space.Panes {
			counts.Add(space.Panes[i].Status)
		}
		space.Counts = counts
		space.Status = counts.Rollup()
		sortPanes(space.Panes)
	}
	// Prune memory for panes that no longer exist.
	for id := range a.done {
		if !seen[id] {
			delete(a.done, id)
		}
	}
	for id := range a.prev {
		if !seen[id] {
			delete(a.prev, id)
		}
	}
	SortSpaces(spaces)
}

// screenStatus folds one screen reading over the base status and reports
// the resulting status plus whether Done memory should be cleared.
func (a *Arbitrator) screenStatus(pane *Pane, reading ScreenReading, base Status, nowMs int64) (Status, bool) {
	if reading.SkipStateUpdate {
		return base, false
	}
	if base == StatusFailed {
		return StatusFailed, true
	}
	authorityActive := (reading.OSCAuthority || IsAuthorityApp(pane.App)) && !idleLike(base)
	if reading.VisibleBlocker || (reading.State == "blocked" && !authorityActive) {
		return StatusNeedsYou, true
	}
	if base == StatusDone {
		// A remembered/native Done stays until a new live turn or a
		// visible blocker; an idle viewport must not erase it.
		if reading.State == "working" && reading.VisibleWorking {
			return StatusWorking, true
		}
		return StatusDone, true
	}
	if authorityActive {
		// A complete live lifecycle owns working/idle.
		return base, false
	}
	switch reading.State {
	case "working":
		if reading.VisibleWorking {
			return StatusWorking, true
		}
		return base, false
	case "idle", "unknown":
		// When working itself came from a persisted screen reading
		// (@spaces_source=screen), the working banner/spinner disappearing is
		// enough to mark the turn done even for manifests (such as agy) that
		// define no explicit visible_idle rule and report "unknown" once idle.
		persistedScreenWorking := base == StatusWorking && strings.EqualFold(strings.TrimSpace(pane.Source), "screen")
		if reading.State == "unknown" {
			if !persistedScreenWorking {
				return base, false
			}
		} else if !reading.VisibleIdle && !persistedScreenWorking {
			// No visible idle prompt: keep the live base.
			return base, false
		}
		wasWorking := base == StatusWorking || base == StatusNeedsYou ||
			a.prev[pane.ID] == "working"
		_, hasDone := a.done[pane.ID]
		switch {
		case wasWorking:
			a.done[pane.ID] = nowMs
			a.newlyDone = append(a.newlyDone, pane.ID)
			return StatusDone, false
		case hasDone:
			return StatusDone, false
		default:
			return StatusIdle, false
		}
	default:
		return base, false
	}
}

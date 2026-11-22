package model

import (
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent/plugins"
	"github.com/arhammahajan/spaces/internal/tmux"
)

func arbSpace(name, app string, status Status, active bool) Space {
	p := Pane{
		ID: "%%1", NumericID: 1,
		WindowIndex: 0, WindowName: "editor",
		App: app, Status: status, BaseStatus: status,
		Text: "base", BaseText: "base",
		IsActive: active,
	}
	var counts Counts
	counts.Add(status)
	return Space{
		Name: name, ID: "$0", IsCurrent: name == "dots",
		Panes: []Pane{p}, Counts: counts, Status: counts.Rollup(),
	}
}

func hookSnap(session, id, state, text string, ts, since int64) *HookSnapshot {
	return &HookSnapshot{
		OK: true, Version: 1, NowMs: ts,
		Panes: map[string]map[string]HookSummary{
			session: {
				id: {
					Agent: "claude", App: "claude-code", State: state,
					Event: "pre_tool", TsMs: ts, StateSinceMs: since, Text: text,
				},
			},
		},
	}
}

func applyOne(t *testing.T, arb *Arbitrator, space Space, hooks *HookSnapshot, readings map[string]ScreenReading, nowMs int64) Space {
	t.Helper()
	spaces := []Space{space}
	arb.Apply(spaces, hooks, readings, nowMs)
	return spaces[0]
}

func TestFreshHookWorkingWinsOverIdleBase(t *testing.T) {
	arb := NewArbitrator()
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusIdle, false),
		hookSnap("dots", "1", "working", "Bash x test", 9_000, 9_000), nil, 10_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
	if got.Panes[0].Text != "Bash x test" {
		t.Errorf("text = %q", got.Panes[0].Text)
	}
	if got.Panes[0].AgeMs != 1_000 {
		t.Errorf("age = %d want 1000", got.Panes[0].AgeMs)
	}
	if got.Status != StatusWorking {
		t.Errorf("space status = %v", got.Status)
	}
	if got.Panes[0].Hook == nil || got.Panes[0].Hook.State != "working" {
		t.Error("hook summary should be attached")
	}
}

func TestStaleHookFallsBackToBase(t *testing.T) {
	arb := NewArbitrator()
	// working hook silent for >120s.
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusIdle, false),
		hookSnap("dots", "1", "working", "old", 0, 0), nil, 200_000)
	if got.Panes[0].Status != StatusIdle {
		t.Errorf("got %v want idle", got.Panes[0].Status)
	}
	// done hook older than 30m.
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false),
		hookSnap("dots", "1", "done", "old", 0, 0), nil, 31*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
	// failed hook older than 30m.
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false),
		hookSnap("dots", "1", "failed", "old", 0, 0), nil, 31*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
	// non-auth blocked hook older than 30m.
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false),
		hookSnap("dots", "1", "blocked", "old", 0, 0), nil, 31*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
	// auth block survives 30m but not 60m.
	auth := hookSnap("dots", "1", "blocked", "auth?", 0, 0)
	auth.Panes["dots"]["1"] = HookSummary{
		State: "blocked", Kind: "auth", TsMs: 0, StateSinceMs: 0, Text: "auth?",
	}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), auth, nil, 31*60_000)
	if got.Panes[0].Status != StatusNeedsYou {
		t.Errorf("auth block at 31m: got %v want needs-you", got.Panes[0].Status)
	}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), auth, nil, 61*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("auth block at 61m: got %v want working", got.Panes[0].Status)
	}
}

func TestHookDonePersistsThirtyMinutes(t *testing.T) {
	arb := NewArbitrator()
	hooks := hookSnap("dots", "1", "done", "turn complete", 9_000, 9_000)
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), hooks, nil, 10_000)
	if got.Panes[0].Status != StatusDone {
		t.Fatalf("got %v want done", got.Panes[0].Status)
	}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), hooks, nil, 29*60_000)
	if got.Panes[0].Status != StatusDone {
		t.Errorf("at 29m: got %v want done", got.Panes[0].Status)
	}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), hooks, nil, 9_000+31*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("at 31m: got %v want working", got.Panes[0].Status)
	}
}

func TestHookIdleNeverOverrides(t *testing.T) {
	arb := NewArbitrator()
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false),
		hookSnap("dots", "1", "idle", "idle", 9_000, 9_000), nil, 10_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
}

func TestNewerBaseBeatsOldHook(t *testing.T) {
	arb := NewArbitrator()
	space := arbSpace("dots", "claude-code", StatusDone, false)
	space.Panes[0].BaseAgeMs = 500
	space.Panes[0].AgeMs = 500
	got := applyOne(t, arb, space,
		hookSnap("dots", "1", "working", "Bash old", 9_000, 9_000), nil, 10_000)
	if got.Panes[0].Status != StatusDone {
		t.Errorf("got %v want done", got.Panes[0].Status)
	}
}

func TestVisibleBlockerOverridesHookWorking(t *testing.T) {
	arb := NewArbitrator()
	readings := map[string]ScreenReading{
		"%%1": {App: "claude-code", State: "blocked", VisibleBlocker: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusIdle, false),
		hookSnap("dots", "1", "working", "Bash x test", 9_000, 9_000), readings, 10_000)
	if got.Panes[0].Status != StatusNeedsYou {
		t.Errorf("got %v want needs-you", got.Panes[0].Status)
	}
}

func TestAuthorityKeepsLiveBaseOverScreen(t *testing.T) {
	arb := NewArbitrator()
	// claude-code is an authority app: idle viewport must not downgrade working.
	readings := map[string]ScreenReading{
		"%%1": {App: "claude-code", State: "idle", VisibleIdle: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("authority working: got %v want working", got.Panes[0].Status)
	}
	// ... but a strong blocker still wins.
	readings["%%1"] = ScreenReading{App: "claude-code", State: "blocked", VisibleBlocker: true}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusWorking, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusNeedsYou {
		t.Errorf("authority blocker: got %v want needs-you", got.Panes[0].Status)
	}
	// Non-authority apps follow screen working evidence.
	plain := map[string]ScreenReading{
		"%%1": {App: "gemini", State: "working", VisibleWorking: true},
	}
	got = applyOne(t, arb, arbSpace("dots", "gemini", StatusIdle, false), nil, plain, 1_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("non-authority working: got %v want working", got.Panes[0].Status)
	}
	// Unknown screens leave the base alone.
	unknown := map[string]ScreenReading{
		"%%1": {App: "gemini", State: "unknown"},
	}
	got = applyOne(t, arb, arbSpace("dots", "gemini", StatusIdle, false), nil, unknown, 1_000)
	if got.Panes[0].Status != StatusIdle {
		t.Errorf("unknown: got %v want idle", got.Panes[0].Status)
	}
	// skip_state_update leaves the base alone.
	skip := map[string]ScreenReading{
		"%%1": {App: "gemini", State: "working", VisibleWorking: true, SkipStateUpdate: true},
	}
	got = applyOne(t, arb, arbSpace("dots", "gemini", StatusIdle, false), nil, skip, 1_000)
	if got.Panes[0].Status != StatusIdle {
		t.Errorf("skip: got %v want idle", got.Panes[0].Status)
	}
}

func TestNativeDoneSurvivesIdleViewport(t *testing.T) {
	arb := NewArbitrator()
	readings := map[string]ScreenReading{
		"%%1": {App: "claude-code", State: "idle", VisibleIdle: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusDone, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusDone {
		t.Errorf("got %v want done", got.Panes[0].Status)
	}
	// ... but a new visible working turn promotes it.
	readings["%%1"] = ScreenReading{App: "claude-code", State: "working", VisibleWorking: true}
	got = applyOne(t, arb, arbSpace("dots", "claude-code", StatusDone, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working", got.Panes[0].Status)
	}
}

func TestScreenFailedBaseSticks(t *testing.T) {
	arb := NewArbitrator()
	readings := map[string]ScreenReading{
		"%%1": {App: "gemini", State: "working", VisibleWorking: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "gemini", StatusFailed, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusFailed {
		t.Errorf("got %v want failed", got.Panes[0].Status)
	}
}

func TestWorkingToIdleSynthesizesDoneUntilFocused(t *testing.T) {
	arb := NewArbitrator()
	// Tick 1: screen working observed (non-authority app).
	working := map[string]ScreenReading{
		"%%1": {App: "qwen", State: "working", VisibleWorking: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "qwen", StatusIdle, false), nil, working, 1_000)
	if got.Panes[0].Status != StatusWorking {
		t.Fatalf("tick1: got %v want working", got.Panes[0].Status)
	}
	// Tick 2: idle viewport after working -> Done (pane not focused).
	idle := map[string]ScreenReading{
		"%%1": {App: "qwen", State: "idle", VisibleIdle: true},
	}
	next := arbSpace("dots", "qwen", StatusIdle, false)
	spaces := []Space{next}
	arb.Apply(spaces, nil, idle, 2_000)
	if spaces[0].Panes[0].Status != StatusDone {
		t.Fatalf("tick2: got %v want done", spaces[0].Panes[0].Status)
	}
	// Tick 3: repeat is stable (idempotent recompute from base).
	again := arbSpace("dots", "qwen", StatusIdle, false)
	spaces = []Space{again}
	arb.Apply(spaces, nil, idle, 3_000)
	if spaces[0].Panes[0].Status != StatusDone {
		t.Fatalf("tick3: got %v want done", spaces[0].Panes[0].Status)
	}
	// Done memory stays Done even when the pane is active (clears on a new working turn or 10m TTL).
	focused := arbSpace("dots", "qwen", StatusIdle, true)
	spaces = []Space{focused}
	arb.Apply(spaces, nil, idle, 4_000)
	if spaces[0].Panes[0].Status != StatusDone {
		t.Fatalf("focused: got %v want done (kept for 10m TTL)", spaces[0].Panes[0].Status)
	}
	// Starting a new working turn clears Done memory -> Working.
	spaces = []Space{arbSpace("dots", "qwen", StatusIdle, true)}
	arb.Apply(spaces, nil, working, 5_000)
	if spaces[0].Panes[0].Status != StatusWorking {
		t.Fatalf("new turn: got %v want working", spaces[0].Panes[0].Status)
	}
}

func TestDoneMemoryExpiresAfterTenMinutes(t *testing.T) {
	arb := NewArbitrator()
	working := map[string]ScreenReading{"%%1": {App: "qwen", State: "working", VisibleWorking: true}}
	idle := map[string]ScreenReading{"%%1": {App: "qwen", State: "idle", VisibleIdle: true}}
	spaces := []Space{arbSpace("dots", "qwen", StatusIdle, false)}
	arb.Apply(spaces, nil, working, 1_000)
	spaces = []Space{arbSpace("dots", "qwen", StatusIdle, false)}
	arb.Apply(spaces, nil, idle, 2_000)
	if spaces[0].Panes[0].Status != StatusDone {
		t.Fatalf("got %v want done", spaces[0].Panes[0].Status)
	}
	spaces = []Space{arbSpace("dots", "qwen", StatusIdle, false)}
	arb.Apply(spaces, nil, idle, 2_000+doneTTLMs+1)
	if spaces[0].Panes[0].Status != StatusIdle {
		t.Errorf("expired: got %v want idle", spaces[0].Panes[0].Status)
	}
}

func TestStaleAppReadingIgnored(t *testing.T) {
	arb := NewArbitrator()
	readings := map[string]ScreenReading{
		"%%1": {App: "gemini", State: "working", VisibleWorking: true},
	}
	got := applyOne(t, arb, arbSpace("dots", "claude-code", StatusIdle, false), nil, readings, 1_000)
	if got.Panes[0].Status != StatusIdle {
		t.Errorf("mismatched app reading: got %v want idle", got.Panes[0].Status)
	}
}

func TestApplyRecomputesSortAndCounts(t *testing.T) {
	arb := NewArbitrator()
	space := Space{
		Name: "dots", ID: "$0", IsCurrent: true,
		Panes: []Pane{
			{ID: "%%2", NumericID: 2, WindowIndex: 1, App: "shell", Status: StatusIdle, BaseStatus: StatusIdle},
			{ID: "%%1", NumericID: 1, WindowIndex: 0, App: "shell", Status: StatusIdle, BaseStatus: StatusIdle},
		},
	}
	hooks := hookSnap("dots", "2", "blocked", "approve?", 9_000, 9_000)
	spaces := []Space{space, arbSpace("zzz", "shell", StatusIdle, false)}
	arb.Apply(spaces, hooks, nil, 10_000)
	// dots sorts... both spaces present; dots has NeedsYou so after current-first it stays first.
	if spaces[0].Name != "dots" {
		t.Fatalf("first space = %s", spaces[0].Name)
	}
	if spaces[0].Status != StatusNeedsYou || spaces[0].Counts.Needs != 1 {
		t.Errorf("dots = %+v/%+v", spaces[0].Status, spaces[0].Counts)
	}
	if spaces[0].Panes[0].ID != "%%2" {
		t.Errorf("urgent pane should sort first, got %s", spaces[0].Panes[0].ID)
	}
}

func TestApplyWithRealBuildSnapshot(t *testing.T) {
	snap := tmux.ServerSnapshot{
		CurrentSession: "dots",
		Sessions:       []tmux.Session{{ID: "$0", Name: "dots"}},
		Windows:        []tmux.Window{{SessionName: "dots", ID: "@1", Index: 0, Name: "w"}},
		Panes: []tmux.Pane{{
			SessionName: "dots", WindowID: "@1", WindowIndex: 0, WindowName: "w",
			ID: "%5", NumericID: 5, CurrentCommand: "bash", SpacesState: "working",
		}},
	}
	spaces := BuildSpaces(snap, 10_000)
	arb := NewArbitrator()
	arb.Apply(spaces, hookSnap("dots", "5", "done", "all set", 9_500, 9_500), nil, 10_000)
	if spaces[0].Panes[0].Status != StatusDone {
		t.Errorf("got %v want done", spaces[0].Panes[0].Status)
	}
	// Idempotent: second apply with same inputs is stable.
	spaces2 := BuildSpaces(snap, 10_000)
	arb.Apply(spaces2, hookSnap("dots", "5", "done", "all set", 9_500, 9_500), nil, 10_000)
	if spaces2[0].Panes[0].Status != spaces[0].Panes[0].Status ||
		spaces2[0].Panes[0].Text != spaces[0].Panes[0].Text {
		t.Error("repeated Apply is not stable")
	}
	// Nil hooks/readings keep the base.
	spaces3 := BuildSpaces(snap, 10_000)
	NewArbitrator().Apply(spaces3, nil, nil, 10_000)
	if spaces3[0].Panes[0].Status != StatusWorking {
		t.Errorf("nil inputs: got %v want working", spaces3[0].Panes[0].Status)
	}
	if !strings.Contains(spaces3[0].Panes[0].App, "shell") {
		t.Errorf("app = %q want shell", spaces3[0].Panes[0].App)
	}
}

func TestNewerOSCWorkingBeatsOlderHookDone(t *testing.T) {
	arb := NewArbitrator()
	space := arbSpace("dots", "claude-code", StatusWorking, false)
	space.Panes[0].BaseAgeMs = 500
	space.Panes[0].AgeMs = 500
	// Hook said "done" 5s ago, OSC 7501 said "working" 500ms ago.
	if got := applyOne(t, arb, space,
		hookSnap("dots", "1", "done", "old turn", 5_000, 5_000), nil, 10_000); got.Panes[0].Status != StatusWorking {
		t.Errorf("got %v want working (newer OSC should beat older hook done)", got.Panes[0].Status)
	}
}

func TestExitedAgentReturnedToShellDoesNotStayInInboxViaHookStore(t *testing.T) {
	origDetect := DetectDescendantAgentFn
	defer func() { DetectDescendantAgentFn = origDetect }()
	DetectDescendantAgentFn = func(int) string { return "" }

	snap := tmux.ServerSnapshot{
		CurrentSession: "dots",
		Sessions:       []tmux.Session{{ID: "$0", Name: "dots"}},
		Windows:        []tmux.Window{{SessionName: "dots", ID: "@1", Index: 0, Name: "w"}},
		Panes: []tmux.Pane{{
			SessionName: "dots", WindowID: "@1", WindowIndex: 0, WindowName: "w",
			ID: "%5", NumericID: 5, CurrentCommand: "zsh", PID: 100,
			SpacesState: "working", SpacesApp: "claude-code", SpacesSource: "hook",
		}},
	}
	spaces := BuildSpaces(snap, 10_000)
	arb := NewArbitrator()
	// Even though the hook store still has a fresh "working" record for pane 5,
	// the agent exited back to zsh so Arbitrator must not re-attach the hook.
	arb.Apply(spaces, hookSnap("dots", "5", "working", "Bash · cargo test", 9_500, 9_500), nil, 10_000)
	p := spaces[0].Panes[0]
	if p.App != "shell" || p.Hook != nil || p.IsAgent() {
		t.Errorf("exited agent pane should immediately be non-agent shell, got app=%q hook=%+v isAgent=%v", p.App, p.Hook, p.IsAgent())
	}
}

func TestSeedAppsFromHooksAndApplyDoNotReviveShellPaneAfterCtrlD(t *testing.T) {
	origDetect := DetectDescendantAgentFn
	origLive := ProcIsLiveFn
	defer func() {
		DetectDescendantAgentFn = origDetect
		ProcIsLiveFn = origLive
	}()
	DetectDescendantAgentFn = func(int) string { return "" }
	ProcIsLiveFn = func(pid int) bool { return pid == 438039 }

	hooks := &HookSnapshot{
		OK: true, Version: 1, NowMs: 10_000,
		Panes: map[string]map[string]HookSummary{
			"model-router": {
				"41": {
					Agent: "antigravity", App: "antigravity", State: "done",
					Event: "stop", TsMs: 9_000, StateSinceMs: 9_000,
					Text: "turn complete", Title: "Rewrite Readme", SessionID: "conv-41",
				},
			},
		},
	}

	// Case 1: Immediately after Ctrl+D back to zsh with stale @spaces_app/@spaces_state.
	snap1 := tmux.ServerSnapshot{
		CurrentSession: "model-router",
		Sessions:       []tmux.Session{{ID: "$0", Name: "model-router"}},
		Windows:        []tmux.Window{{SessionName: "model-router", ID: "@1", Index: 0, Name: "zsh"}},
		Panes: []tmux.Pane{{
			SessionName: "model-router", WindowID: "@1", WindowIndex: 0, WindowName: "zsh",
			ID: "%41", NumericID: 41, CurrentCommand: "zsh", PID: 438039,
			SpacesState: "done", SpacesApp: "antigravity", SpacesSource: "osc7501",
			SpacesMsg: "finished", SpacesTsMs: 9_500, ResumeSession: "conv-41",
		}},
	}
	spaces1 := BuildSpaces(snap1, 10_000)
	SeedAppsFromHooks(spaces1, hooks)
	NewArbitrator().Apply(spaces1, hooks, nil, 10_000)
	p1 := spaces1[0].Panes[0]
	if !p1.ReturnedToShell || p1.App != "shell" || p1.Hook != nil || p1.IsAgent() {
		t.Fatalf("after Ctrl+D (stale @spaces_*): want ReturnedToShell=true shell non-agent, got returned=%v app=%q hook=%+v isAgent=%v status=%v",
			p1.ReturnedToShell, p1.App, p1.Hook, p1.IsAgent(), p1.Status)
	}

	// Case 2: Subsequent tick after @spaces_* has been cleared (e.g. via status clear or sync cleanup),
	// while the 30m hook record is still present in hooks.store.json.
	snap2 := tmux.ServerSnapshot{
		CurrentSession: "model-router",
		Sessions:       []tmux.Session{{ID: "$0", Name: "model-router"}},
		Windows:        []tmux.Window{{SessionName: "model-router", ID: "@1", Index: 0, Name: "zsh"}},
		Panes: []tmux.Pane{{
			SessionName: "model-router", WindowID: "@1", WindowIndex: 0, WindowName: "zsh",
			ID: "%41", NumericID: 41, CurrentCommand: "zsh", PID: 438039,
		}},
	}
	spaces2 := BuildSpaces(snap2, 10_000)
	SeedAppsFromHooks(spaces2, hooks)
	NewArbitrator().Apply(spaces2, hooks, nil, 10_000)
	p2 := spaces2[0].Panes[0]
	if p2.App != "shell" || p2.Hook != nil || p2.IsAgent() {
		t.Fatalf("after @spaces_* cleared: want shell non-agent, got app=%q hook=%+v isAgent=%v status=%v",
			p2.App, p2.Hook, p2.IsAgent(), p2.Status)
	}
}

func TestActiveBackgroundWorkStaysWorkingPastThirtyMinutes(t *testing.T) {
	arb := NewArbitrator()
	startMs := int64(1_700_000_000_000)
	twoHoursLater := startMs + 2*60*60_000

	// Pane has an active finite subagent running for 2 hours without new hook events.
	hooks := &HookSnapshot{
		OK: true, Version: 1, NowMs: twoHoursLater,
		Panes: map[string]map[string]HookSummary{
			"proj": {
				"1": {
					Agent:        "claude",
					App:          "claude-code",
					State:        "working",
					Event:        "stop",
					TsMs:         startMs,
					StateSinceMs: startMs,
					Text:         "waiting on subagent: Worker · Migrate backend",
					WaitingOn:    "waiting on subagent: Worker · Migrate backend",
					FiniteActive: 1,
					ChildCount:   1,
					Children: []HookChild{
						{
							ID:       "sub-1",
							Kind:     "Worker",
							Role:     "subagent",
							Category: "finite",
							State:    "working",
							SinceMs:  startMs,
							TsMs:     startMs,
							Text:     "Migrate backend",
						},
					},
				},
			},
		},
	}

	got := applyOne(t, arb, arbSpace("proj", "claude-code", StatusIdle, false), hooks, nil, twoHoursLater)
	if got.Panes[0].Status != StatusWorking {
		t.Fatalf("at +2h with active subagent: got %v want StatusWorking", got.Panes[0].Status)
	}

	// Even if only Children has an active finite subagent (FiniteActive/WaitingOn omitted),
	// it stays working indefinitely until the child is no longer active.
	hooks.Panes["proj"]["1"] = HookSummary{
		Agent:        "claude",
		App:          "claude-code",
		State:        "working",
		Event:        "post_tool",
		TsMs:         startMs,
		StateSinceMs: startMs,
		Text:         "running subagent",
		Children: []HookChild{
			{ID: "sub-1", Role: "subagent", Category: "finite", State: "working", SinceMs: startMs, TsMs: startMs},
		},
	}
	got = applyOne(t, arb, arbSpace("proj", "claude-code", StatusIdle, false), hooks, nil, startMs+6*60*60_000)
	if got.Panes[0].Status != StatusWorking {
		t.Fatalf("at +6h with active child: got %v want StatusWorking", got.Panes[0].Status)
	}

	// Once the subagent finishes (state=done, FiniteActive=0, WaitingOn=""),
	// normal working staleness applies and the pane is no longer forced working.
	hooks.Panes["proj"]["1"] = HookSummary{
		Agent:        "claude",
		App:          "claude-code",
		State:        "working",
		Event:        "subagent_stop",
		TsMs:         startMs,
		StateSinceMs: startMs,
		Text:         "subagent done",
		FiniteActive: 0,
		Children: []HookChild{
			{ID: "sub-1", Role: "subagent", Category: "finite", State: "done", SinceMs: startMs, TsMs: startMs + 1000, EndedMs: startMs + 1000},
		},
	}
	got = applyOne(t, arb, arbSpace("proj", "claude-code", StatusIdle, false), hooks, nil, startMs+hookWorkingStaleMs+1)
	if got.Panes[0].Status != StatusIdle {
		t.Fatalf("after subagent finished and WorkingStaleMs elapsed: got %v want StatusIdle", got.Panes[0].Status)
	}

	// Long-running services alone do NOT keep a silent working hook alive past WorkingStaleMs.
	hooks.Panes["proj"]["1"] = HookSummary{
		Agent:         "claude",
		App:           "claude-code",
		State:         "working",
		Event:         "post_tool",
		TsMs:          startMs,
		StateSinceMs:  startMs,
		ServiceActive: 1,
		BackgroundTasks: []HookChild{
			{ID: "svc-1", Role: "service", Category: "service", State: "working", SinceMs: startMs, TsMs: startMs},
		},
	}
	got = applyOne(t, arb, arbSpace("proj", "claude-code", StatusIdle, false), hooks, nil, startMs+hookWorkingStaleMs+1)
	if got.Panes[0].Status != StatusIdle {
		t.Fatalf("with only a service active past WorkingStaleMs: got %v want StatusIdle", got.Panes[0].Status)
	}
}

func TestExpiredBaseWorkingWithNewerTmuxTsDoesNotOverrideActiveBackgroundHook(t *testing.T) {
	startMs := int64(1_700_000_000_000)
	laterMs := startMs + 45*60_000

	// Simulate async __osc-watch writing @spaces_ts 5ms AFTER hook.TsMs.
	// When @spaces_state="working" expires in BuildSpaces, BaseAgeMs must reset to 0
	// so hookView does not mistake the expired fallback idle for a newer idle event.
	snap := tmux.ServerSnapshot{
		CurrentSession: "proj",
		Sessions:       []tmux.Session{{ID: "$0", Name: "proj"}},
		Windows:        []tmux.Window{{SessionName: "proj", ID: "@1", Index: 0, Name: "agent"}},
		Panes: []tmux.Pane{{
			SessionName: "proj", WindowID: "@1", WindowIndex: 0, WindowName: "agent",
			ID: "%5", NumericID: 5, CurrentCommand: "antigravity",
			SpacesState: "working", SpacesApp: "antigravity", SpacesSource: "osc7501",
			SpacesTsMs: startMs + 5,
		}},
	}
	spaces := BuildSpaces(snap, laterMs)
	if spaces[0].Panes[0].BaseStatus != StatusIdle || spaces[0].Panes[0].BaseAgeMs != 0 {
		t.Fatalf("expired base should reset to Idle with BaseAgeMs=0, got status=%v age=%d",
			spaces[0].Panes[0].BaseStatus, spaces[0].Panes[0].BaseAgeMs)
	}

	hooks := &HookSnapshot{
		OK: true, Version: 1, NowMs: laterMs,
		Panes: map[string]map[string]HookSummary{
			"proj": {
				"5": {
					Agent:        "antigravity",
					App:          "antigravity",
					State:        "working",
					Event:        "stop",
					TsMs:         startMs,
					StateSinceMs: startMs,
					Text:         "waiting on subagent: Codebase Researcher",
					WaitingOn:    "waiting on subagent: Codebase Researcher",
					FiniteActive: 1,
				},
			},
		},
	}
	arb := NewArbitrator()
	arb.Apply(spaces, hooks, nil, laterMs)
	if spaces[0].Panes[0].Status != StatusWorking {
		t.Fatalf("at +45m with active background work: got %v want StatusWorking", spaces[0].Panes[0].Status)
	}
}

func TestFreshWorkingHookSuppressesSpuriousScreenNewlyDone(t *testing.T) {
	arb := NewArbitrator()
	t0 := int64(1_700_000_000_000)

	// Tick 1: Antigravity viewport shows working.
	workingReading := map[string]ScreenReading{
		"%%1": {App: "antigravity", State: "working", VisibleWorking: true},
	}
	hooks := &HookSnapshot{
		OK: true, Version: 1, NowMs: t0,
		Panes: map[string]map[string]HookSummary{
			"proj": {
				"1": {
					Agent: "antigravity", App: "antigravity", State: "working",
					Event: "stop", TsMs: t0, StateSinceMs: t0,
					WaitingOn: "waiting on subagent: Docs", FiniteActive: 1,
				},
			},
		},
	}
	s1 := []Space{arbSpace("proj", "antigravity", StatusWorking, false)}
	s1[0].Panes[0].Source = "screen"
	arb.Apply(s1, hooks, workingReading, t0)

	// Tick 2: Parent viewport clears to unknown/idle while subagent still runs.
	// Hook keeps pane StatusWorking and must NOT trigger screen-synthesized newlyDone.
	idleReading := map[string]ScreenReading{
		"%%1": {App: "antigravity", State: "unknown"},
	}
	s2 := []Space{arbSpace("proj", "antigravity", StatusWorking, false)}
	s2[0].Panes[0].Source = "screen"
	arb.Apply(s2, hooks, idleReading, t0+5_000)
	if s2[0].Panes[0].Status != StatusWorking {
		t.Fatalf("tick 2 status = %v, want StatusWorking", s2[0].Panes[0].Status)
	}
	if newlyDone := arb.TakeNewlyDone(); len(newlyDone) != 0 {
		t.Fatalf("fresh working hook must suppress spurious screen newlyDone, got %v", newlyDone)
	}
}

func TestAntigravityClearsStaleWorkingHooksWhenVisiblyIdle(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	idleReading := map[string]ScreenReading{
		"%%1": {App: "antigravity", State: "idle", VisibleIdle: true},
	}

	// Case 1: Stale local_bash background task (finite_active=1, >hookWorkingStaleMs,
	// no active subagents) while viewport is visibly idle -> must NOT stay working forever.
	arb1 := NewArbitrator()
	hooksLocalCmd := &HookSnapshot{
		OK: true, Version: 1, NowMs: t0 + hookWorkingStaleMs + 60_000,
		Panes: map[string]map[string]HookSummary{
			"dots": {
				"1": {
					Agent:        "antigravity",
					App:          "antigravity",
					State:        "working",
					Event:        "post_tool",
					TsMs:         t0,
					StateSinceMs: t0,
					FiniteActive: 1,
					WaitingOn:    "waiting on: for l in ...",
					BackgroundTasks: []HookChild{
						{ID: "conv/task-115", Role: "command", Kind: "local_bash", Category: "finite", State: "working", SinceMs: t0, TsMs: t0},
					},
				},
			},
		},
	}
	got1 := applyOne(t, arb1, arbSpace("dots", "antigravity", StatusIdle, false), hooksLocalCmd, idleReading, t0+hookWorkingStaleMs+60_000)
	if got1.Panes[0].Status != StatusIdle {
		t.Fatalf("stale local_bash hook with visibly idle viewport: got %v, want StatusIdle", got1.Panes[0].Status)
	}

	// Case 2: Newer screen turn completes after an older working hook -> transitions to Done
	// and preserves newlyDone instead of letting the older hook overwrite it back to Working.
	arb2 := NewArbitrator()
	sp2 := arbSpace("dots", "antigravity", StatusWorking, false)
	sp2.Panes[0].Source = "screen"
	sp2.Panes[0].BaseAgeMs = 4_000 // screen saw working 4s ago (much newer than t0)
	got2 := applyOne(t, arb2, sp2, hooksLocalCmd, idleReading, t0+60_000)
	if got2.Panes[0].Status != StatusDone {
		t.Fatalf("newer screen turn finished over older working hook: got %v, want StatusDone", got2.Panes[0].Status)
	}
	if newlyDone := arb2.TakeNewlyDone(); len(newlyDone) != 1 || newlyDone[0] != "%%1" {
		t.Fatalf("expected newlyDone=[%%%%1], got %v", newlyDone)
	}

	// Case 3: Antigravity missed Stop hook (working, no finite work, >10s silent) while
	// viewport is visibly idle -> transitions to Done instead of staying Working for 2m.
	arb3 := NewArbitrator()
	hooksMissedStop := &HookSnapshot{
		OK: true, Version: 1, NowMs: t0 + plugins.AntigravityHookIdleGraceMs + 1_000,
		Panes: map[string]map[string]HookSummary{
			"dots": {
				"1": {
					Agent:        "antigravity",
					App:          "antigravity",
					State:        "working",
					Event:        "post_tool",
					TsMs:         t0,
					StateSinceMs: t0,
				},
			},
		},
	}
	sp3 := arbSpace("dots", "antigravity", StatusWorking, false)
	sp3.Panes[0].Source = "osc7501"
	got3 := applyOne(t, arb3, sp3, hooksMissedStop, idleReading, t0+plugins.AntigravityHookIdleGraceMs+1_000)
	if got3.Panes[0].Status != StatusDone {
		t.Fatalf("antigravity missed Stop hook with visibly idle viewport: got %v, want StatusDone", got3.Panes[0].Status)
	}

	// Case 4: Active subagent (NOT local_bash) still stays StatusWorking even with VisibleIdle!
	arb4 := NewArbitrator()
	hooksSubagent := &HookSnapshot{
		OK: true, Version: 1, NowMs: t0 + hookWorkingStaleMs + 60_000,
		Panes: map[string]map[string]HookSummary{
			"dots": {
				"1": {
					Agent:        "antigravity",
					App:          "antigravity",
					State:        "working",
					Event:        "stop",
					TsMs:         t0,
					StateSinceMs: t0,
					FiniteActive: 1,
					WaitingOn:    "waiting on subagent: Codebase Researcher",
				},
			},
		},
	}
	got4 := applyOne(t, arb4, arbSpace("dots", "antigravity", StatusIdle, false), hooksSubagent, idleReading, t0+hookWorkingStaleMs+60_000)
	if got4.Panes[0].Status != StatusWorking {
		t.Fatalf("active subagent must stay StatusWorking even with idle parent prompt, got %v", got4.Panes[0].Status)
	}
}

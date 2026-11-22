package model

import (
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func testSnapshot() tmux.ServerSnapshot {
	now := time.Unix(1_700_000_000, 0)
	return tmux.ServerSnapshot{
		CurrentSession: "dots",
		CurrentPaneID:  "%1",
		Sessions: []tmux.Session{
			{ID: "$0", Name: "dots", Attached: 1, CreatedAt: now},
			{ID: "$1", Name: "server", Attached: 0, CreatedAt: now},
		},
		Windows: []tmux.Window{
			{SessionName: "dots", ID: "@1", Index: 0, Name: "editor"},
			{SessionName: "dots", ID: "@2", Index: 1, Name: "shell"},
			{SessionName: "server", ID: "@3", Index: 0, Name: "logs"},
		},
		Panes: []tmux.Pane{
			{
				SessionName: "dots", WindowID: "@1", WindowIndex: 0, WindowName: "editor",
				ID: "%1", NumericID: 1, Title: "nvim", CurrentCommand: "nvim",
				CurrentPath: "/home/me/dev/dots", PID: 100, Active: true, WindowActive: true,
				SpacesState: "working", SpacesApp: "opencode", SpacesMsg: "editing auth", SpacesTsMs: 1_000,
			},
			{
				SessionName: "dots", WindowID: "@2", WindowIndex: 1, WindowName: "shell",
				ID: "%2", NumericID: 2, Title: "bash", CurrentCommand: "bash",
				CurrentPath: "/home/me/dev/dots", PID: 101,
				WindowBell: true,
			},
			{
				SessionName: "server", WindowID: "@3", WindowIndex: 0, WindowName: "logs",
				ID: "%3", NumericID: 3, Title: "tail", CurrentCommand: "tail",
				CurrentPath: "/var/log", PID: 102, WindowSilence: true,
			},
		},
	}
}

func TestBuildSpacesStateMapping(t *testing.T) {
	cases := map[string]Status{
		"needs_you": StatusNeedsYou, "blocked": StatusNeedsYou,
		"failed": StatusFailed, "error": StatusFailed,
		"done":    StatusDone,
		"working": StatusWorking, "busy": StatusWorking,
		"quiet":      StatusQuiet,
		"new_output": StatusNewOutput,
		"idle":       StatusIdle, "": StatusIdle,
		"bogsy": StatusIdle,
	}
	for state, want := range cases {
		snap := tmux.ServerSnapshot{
			CurrentSession: "s",
			Sessions:       []tmux.Session{{ID: "$0", Name: "s"}},
			Windows:        []tmux.Window{{SessionName: "s", ID: "@1", Index: 0, Name: "w"}},
			Panes: []tmux.Pane{{
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%7", NumericID: 7, CurrentCommand: "top", SpacesState: state,
			}},
		}
		spaces := BuildSpaces(snap, 5_000)
		if len(spaces) != 1 || len(spaces[0].Panes) != 1 {
			t.Fatalf("state %q: expected one space with one pane", state)
		}
		if got := spaces[0].Panes[0].Status; got != want {
			t.Errorf("state %q: got %v want %v", state, got, want)
		}
		if got := spaces[0].Panes[0].BaseStatus; got != want {
			t.Errorf("state %q: base got %v want %v", state, got, want)
		}
	}
}

func TestBuildSpacesNativeFallback(t *testing.T) {
	mk := func(active, bell, activity, silence bool) Status {
		snap := tmux.ServerSnapshot{
			CurrentSession: "s",
			Sessions:       []tmux.Session{{ID: "$0", Name: "s"}},
			Windows:        []tmux.Window{{SessionName: "s", ID: "@1", Index: 0, Name: "w"}},
			Panes: []tmux.Pane{{
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%7", NumericID: 7, CurrentCommand: "top",
				Active: active, WindowBell: bell, WindowActivity: activity, WindowSilence: silence,
			}},
		}
		return BuildSpaces(snap, 5_000)[0].Panes[0].Status
	}
	if got := mk(false, true, false, false); got != StatusNeedsYou {
		t.Errorf("inactive bell: got %v want needs-you", got)
	}
	if got := mk(true, true, false, false); got != StatusIdle {
		t.Errorf("active bell: got %v want idle", got)
	}
	if got := mk(false, false, true, false); got != StatusNewOutput {
		t.Errorf("inactive activity: got %v want new-output", got)
	}
	if got := mk(true, false, true, false); got != StatusIdle {
		t.Errorf("active activity: got %v want idle", got)
	}
	if got := mk(true, false, false, true); got != StatusQuiet {
		t.Errorf("silence: got %v want quiet", got)
	}
}

func TestBuildSpacesAppResolution(t *testing.T) {
	mk := func(spacesApp, cmd string) string {
		snap := tmux.ServerSnapshot{
			Sessions: []tmux.Session{{ID: "$0", Name: "s"}},
			Windows:  []tmux.Window{{SessionName: "s", ID: "@1", Index: 0, Name: "w"}},
			Panes: []tmux.Pane{{
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%7", NumericID: 7, SpacesApp: spacesApp, CurrentCommand: cmd,
			}},
		}
		return BuildSpaces(snap, 0)[0].Panes[0].App
	}
	if got := mk("claude-code", "bash"); got != "claude-code" {
		t.Errorf("spaces_app wins: got %q", got)
	}
	if got := mk("bash", "nvim"); got != "shell" {
		t.Errorf("shell spaces_app normalized: got %q", got)
	}
	for _, sh := range []string{"bash", "zsh", "fish", "sh", "nu", "/bin/bash -l"} {
		if got := mk("", sh); got != "shell" {
			t.Errorf("command %q: got %q want shell", sh, got)
		}
	}
	if got := mk("", "nvim"); got != "nvim" {
		t.Errorf("command passthrough: got %q", got)
	}
	if got := mk("", "/usr/local/bin/pi --arg"); got != "pi" {
		t.Errorf("command basename: got %q", got)
	}
	if got := mk("", ""); got != "terminal" {
		t.Errorf("empty command: got %q want terminal", got)
	}
}

func TestBuildSpacesRollupCWDCurrentAndSort(t *testing.T) {
	spaces := BuildSpaces(testSnapshot(), 5_000)
	if len(spaces) != 2 {
		t.Fatalf("got %d spaces, want 2", len(spaces))
	}
	// Current space sorts first even though server also has status.
	if spaces[0].Name != "dots" || !spaces[0].IsCurrent {
		t.Errorf("first space = %+v, want current dots", spaces[0].Name)
	}
	dots := spaces[0]
	if dots.Status != StatusNeedsYou {
		t.Errorf("dots status = %v, want needs-you (bell pane)", dots.Status)
	}
	if dots.Counts.Working != 1 || dots.Counts.Needs != 1 {
		t.Errorf("dots counts = %+v, want 1 working + 1 needs", dots.Counts)
	}
	if dots.CWD != "/home/me/dev/dots" {
		t.Errorf("dots cwd = %q", dots.CWD)
	}
	// Panes sorted by urgency: needs-you pane first despite higher window index.
	if dots.Panes[0].ID != "%2" || dots.Panes[1].ID != "%1" {
		t.Errorf("pane order = %q,%q want %%2,%%1", dots.Panes[0].ID, dots.Panes[1].ID)
	}
	if dots.Panes[0].AgeMs != 0 {
		t.Errorf("bell pane age = %d, want 0 (no @spaces_ts)", dots.Panes[0].AgeMs)
	}
	if dots.Panes[1].AgeMs != 4_000 || dots.Panes[1].BaseAgeMs != 4_000 {
		t.Errorf("working pane age = %d, want 4000", dots.Panes[1].AgeMs)
	}
	if dots.Panes[1].Text != "editing auth" || dots.Panes[1].BaseText != "editing auth" {
		t.Errorf("working pane text = %q", dots.Panes[1].Text)
	}
	server := spaces[1]
	if server.IsCurrent || server.Status != StatusQuiet {
		t.Errorf("server = current:%v status:%v", server.IsCurrent, server.Status)
	}
	if server.CWD != "/var/log" {
		t.Errorf("server cwd = %q (first pane with cwd)", server.CWD)
	}
	if len(server.Windows) != 1 || server.Windows[0].Name != "logs" {
		t.Errorf("server windows = %+v", server.Windows)
	}
}

func TestBuildSpacesIdempotentBaseline(t *testing.T) {
	snap := testSnapshot()
	a := BuildSpaces(snap, 5_000)
	b := BuildSpaces(snap, 5_000)
	if len(a) != len(b) {
		t.Fatal("space count differs across builds")
	}
	for i := range a {
		if a[i].Status != b[i].Status || a[i].Counts != b[i].Counts {
			t.Errorf("space %s differs across builds", a[i].Name)
		}
		for j := range a[i].Panes {
			if a[i].Panes[j] != b[i].Panes[j] {
				t.Errorf("pane %s differs across builds", a[i].Panes[j].ID)
			}
		}
	}
}

func TestBuildSpacesStaleBaseStatusExpires(t *testing.T) {
	nowMs := int64(10_000_000_000)
	mk := func(state, kind string, ageMs int64) Status {
		snap := tmux.ServerSnapshot{
			Sessions: []tmux.Session{{ID: "$0", Name: "s"}},
			Windows:  []tmux.Window{{SessionName: "s", ID: "@1", Index: 0, Name: "w"}},
			Panes: []tmux.Pane{{
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%7", NumericID: 7, CurrentCommand: "claude",
				SpacesState: state, SpacesApp: "claude-code", SpacesKind: kind,
				SpacesTsMs: nowMs - ageMs,
			}},
		}
		return BuildSpaces(snap, nowMs)[0].Panes[0].Status
	}
	if got := mk("working", "", hookWorkingStaleMs-1); got != StatusWorking {
		t.Errorf("fresh working: got %v want working", got)
	}
	if got := mk("working", "", hookWorkingStaleMs+1); got != StatusIdle {
		t.Errorf("stale working: got %v want idle", got)
	}
	if got := mk("blocked", "permission", hookBlockTTLMs+1); got != StatusIdle {
		t.Errorf("stale permission block: got %v want idle", got)
	}
	if got := mk("blocked", "auth", hookBlockTTLMs+1); got != StatusNeedsYou {
		t.Errorf("auth block within 60m: got %v want needs-you", got)
	}
	if got := mk("blocked", "auth", hookAuthTTLMs+1); got != StatusIdle {
		t.Errorf("stale auth block: got %v want idle", got)
	}
	if got := mk("done", "", hookDoneTTLMs+1); got != StatusIdle {
		t.Errorf("stale done: got %v want idle", got)
	}
}

func TestBuildSpacesShellReturnAndVersionedAgentDetection(t *testing.T) {
	origDetect := DetectDescendantAgentFn
	defer func() { DetectDescendantAgentFn = origDetect }()
	DetectDescendantAgentFn = func(pid int) string {
		if pid == 500 {
			return "claude"
		}
		return ""
	}

	snap := tmux.ServerSnapshot{
		Sessions: []tmux.Session{{ID: "$0", Name: "s"}},
		Windows:  []tmux.Window{{SessionName: "s", ID: "@1", Index: 0, Name: "w"}},
		Panes: []tmux.Pane{
			{
				// Exited codex back to zsh -> stale @spaces_app/@spaces_state cleared.
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%1", NumericID: 1, CurrentCommand: "zsh", PID: 100,
				SpacesState: "working", SpacesApp: "codex", SpacesSource: "osc7501",
			},
			{
				// Versioned binary name without @spaces_app -> normalized to "muse".
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%2", NumericID: 2, CurrentCommand: "muse-bin-1.4.24-R9131.1", PID: 200,
			},
			{
				// Runtime/version command ("2.1.76") resolved via descendant process tree.
				SessionName: "s", WindowID: "@1", WindowIndex: 0, WindowName: "w",
				ID: "%3", NumericID: 3, CurrentCommand: "2.1.76", PID: 500,
			},
		},
	}
	panes := BuildSpaces(snap, 10_000)[0].Panes
	byID := map[string]Pane{}
	for _, p := range panes {
		byID[p.ID] = p
	}
	if p := byID["%1"]; p.App != "shell" || p.Status != StatusIdle || p.IsAgent() {
		t.Errorf("returned shell pane = %+v, want shell/idle/non-agent", p)
	}
	if p := byID["%2"]; p.App != "muse" || !p.IsAgent() {
		t.Errorf("versioned muse pane = %+v, want app=muse/agent", p)
	}
	if p := byID["%3"]; p.App != "claude" || !p.IsAgent() {
		t.Errorf("descendant claude pane = %+v, want app=claude/agent", p)
	}
}

func TestBuildSpacesLiveDescendantAgentOverridesStaleSpacesApp(t *testing.T) {
	origDetect := DetectDescendantAgentFn
	defer func() { DetectDescendantAgentFn = origDetect }()
	DetectDescendantAgentFn = func(pid int) string {
		if pid == 700 {
			return "agy"
		}
		return ""
	}

	// Pane %31 previously ran claude-code (leaving @spaces_app=claude-code),
	// and is now running `harness agy profile saucerf166` (CurrentCommand="harness",
	// descendant binary="agy").
	snap := tmux.ServerSnapshot{
		Sessions: []tmux.Session{{ID: "$0", Name: "model-router"}},
		Windows:  []tmux.Window{{SessionName: "model-router", ID: "@1", Index: 0, Name: "w"}},
		Panes: []tmux.Pane{{
			SessionName: "model-router", WindowID: "@1", WindowIndex: 0, WindowName: "w",
			ID: "%31", NumericID: 31, CurrentCommand: "harness", PID: 700,
			SpacesApp: "claude-code", SpacesState: "done", SpacesMsg: "old claude msg", SpacesSource: "hook",
		}},
	}
	p := BuildSpaces(snap, 10_000)[0].Panes[0]
	if p.App != "antigravity" {
		t.Errorf("p.App = %q, want antigravity (live descendant agent must override stale @spaces_app)", p.App)
	}
	if p.Status != StatusIdle || p.Text != "" {
		t.Errorf("stale @spaces_state/@spaces_msg from previous agent should be ignored, got status=%v text=%q", p.Status, p.Text)
	}
}

func TestBuildSpacesWindowAndPaneExtendedMetadata(t *testing.T) {
	origInspect := InspectPaneProcessFn
	defer func() { InspectPaneProcessFn = origInspect }()
	InspectPaneProcessFn = func(pid int) (string, int) {
		if pid == 9001 {
			return "go test -v ./internal/tui/...", 3
		}
		return "", 0
	}

	actAt := time.Unix(1_700_000_500, 0)
	snap := tmux.ServerSnapshot{
		CurrentSession: "model-router",
		Sessions:       []tmux.Session{{ID: "$0", Name: "model-router"}},
		Windows: []tmux.Window{{
			SessionName: "model-router",
			ID:          "@4",
			Index:       2,
			Name:        "tests",
			Active:      true,
			Zoomed:      true,
			Width:       180,
			Height:      48,
			ActivityAt:  actAt,
			Layout:      "bb62,180x48,0,0,4",
		}},
		Panes: []tmux.Pane{{
			SessionName:    "model-router",
			WindowID:       "@4",
			WindowIndex:    2,
			WindowName:     "tests",
			ID:             "%9",
			NumericID:      9,
			PaneIndex:      1,
			CurrentCommand: "go",
			CurrentPath:    "/home/u/dev/model-router",
			PID:            9001,
			Active:         true,
			WindowActive:   true,
			Width:          90,
			Height:         48,
			InMode:         true,
			Synchronized:   true,
			StartCommand:   "zsh",
		}},
	}

	spaces := BuildSpaces(snap, 1_700_000_600_000)
	if len(spaces) != 1 || len(spaces[0].Windows) != 1 || len(spaces[0].Panes) != 1 {
		t.Fatalf("unexpected spaces: %+v", spaces)
	}
	w := spaces[0].Windows[0]
	if !w.Zoomed || w.Width != 180 || w.Height != 48 || !w.ActivityAt.Equal(actAt) || w.Layout != "bb62,180x48,0,0,4" {
		t.Errorf("window metadata = %+v", w)
	}
	p := spaces[0].Panes[0]
	if p.PaneIndex != 1 || p.Width != 90 || p.Height != 48 || !p.WindowActive || !p.InMode || !p.Synchronized {
		t.Errorf("pane layout metadata = %+v", p)
	}
	if p.StartCommand != "zsh" || p.ForegroundCmd != "go test -v ./internal/tui/..." || p.ChildProcessCount != 3 {
		t.Errorf("pane process metadata = %+v", p)
	}
}

func TestBuildPaneFallsBackToDescendantProfile(t *testing.T) {
	origProf := DetectDescendantProfileFn
	origInspect := InspectPaneProcessFn
	t.Cleanup(func() {
		DetectDescendantProfileFn = origProf
		InspectPaneProcessFn = origInspect
	})
	InspectPaneProcessFn = func(int) (string, int) { return "", 0 }
	DetectDescendantProfileFn = func(pid int) string {
		if pid == 4242 {
			return "saucerf166"
		}
		return ""
	}

	snap := tmux.ServerSnapshot{
		Sessions: []tmux.Session{{Name: "dev", ID: "$1"}},
		Panes: []tmux.Pane{
			{
				SessionName:    "dev",
				ID:             "%1",
				CurrentCommand: "agy",
				PID:            4242,
				// ResumeProfile intentionally empty (@spaces_profile unset).
			},
			{
				SessionName:    "dev",
				ID:             "%2",
				CurrentCommand: "agy",
				PID:            4242,
				ResumeProfile:  "pratham", // @spaces_profile takes precedence.
			},
			{
				SessionName:    "dev",
				ID:             "%3",
				CurrentCommand: "zsh",
				PID:            4242, // non-agent pane should not query profile.
			},
		},
	}

	spaces := BuildSpaces(snap, 1_700_000_000_000)
	if len(spaces) != 1 || len(spaces[0].Panes) != 3 {
		t.Fatalf("unexpected spaces: %+v", spaces)
	}
	byID := map[string]Pane{}
	for _, p := range spaces[0].Panes {
		byID[p.ID] = p
	}
	if byID["%1"].Profile != "saucerf166" {
		t.Errorf("pane %%1 Profile = %q, want saucerf166", byID["%1"].Profile)
	}
	if byID["%2"].Profile != "pratham" {
		t.Errorf("pane %%2 Profile = %q, want pratham (@spaces_profile must win)", byID["%2"].Profile)
	}
	if byID["%3"].Profile != "" {
		t.Errorf("pane %%3 (non-agent) Profile = %q, want empty", byID["%3"].Profile)
	}
}

func TestExtractProfileFromProcData(t *testing.T) {
	// 1. cmdline argv wins over environ.
	got := extractProfileFromProcData(
		[]byte("harness\x00agy\x00profile\x00pratham\x00"),
		[]byte("PATH=/usr/bin\x00HARNESS_PROFILE=other\x00"),
	)
	if got != "pratham" {
		t.Errorf("cmdline precedence = %q, want pratham", got)
	}

	// 2. Falls back to HARNESS_PROFILE in environ when cmdline has no profile.
	got = extractProfileFromProcData(
		[]byte("/usr/bin/agy\x00--conversation\x00abc\x00"),
		[]byte("HOME=/tmp\x00HARNESS_PROFILE=saucerf166\x00TERM=xterm\x00"),
	)
	if got != "saucerf166" {
		t.Errorf("environ fallback = %q, want saucerf166", got)
	}

	// 3. Empty when neither has a profile.
	got = extractProfileFromProcData(
		[]byte("claude\x00--resume\x00123\x00"),
		[]byte("HOME=/tmp\x00"),
	)
	if got != "" {
		t.Errorf("empty case = %q, want empty", got)
	}
}

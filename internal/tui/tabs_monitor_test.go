package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/model"
)

type fakeTelemetryTabsBackend struct {
	fakeTabsBackend
	snap          model.TelemetrySnapshot
	loadCalls     atomic.Int32
	lastForce     atomic.Bool
	killCalls     atomic.Int32
	killedOrphans []model.OrphanPort
	killReturn    int
	killErr       error
	focusedPanes  [][3]string
}

func (f *fakeTelemetryTabsBackend) FocusPane(_ context.Context, session, windowID, paneID string) error {
	f.focusedPanes = append(f.focusedPanes, [3]string{session, windowID, paneID})
	return nil
}

func (f *fakeTelemetryTabsBackend) LoadTelemetry(_ context.Context, _ []model.Space, force bool) (model.TelemetrySnapshot, error) {
	f.loadCalls.Add(1)
	f.lastForce.Store(force)
	return f.snap, nil
}

func (f *fakeTelemetryTabsBackend) KillOrphanPorts(_ context.Context, orphans []model.OrphanPort) (int, error) {
	f.killCalls.Add(1)
	f.killedOrphans = append([]model.OrphanPort(nil), orphans...)
	if f.killErr != nil {
		return 0, f.killErr
	}
	if f.killReturn > 0 {
		return f.killReturn, nil
	}
	return len(orphans), nil
}

func sampleTelemetrySnapshot(now time.Time) model.TelemetrySnapshot {
	nowUnix := now.Unix()
	return model.TelemetrySnapshot{
		UpdatedAt: nowUnix,
		Context: model.TelemetryContext{
			TokensPerMin:   12400,
			RateHistory:    []float64{1200, 3400, 5600, 9200, 12400, 8100, 10500, 12400},
			TotalTokens:    1_250_000,
			ActiveSessions: 2,
			Sessions: []model.ContextSessionEntry{
				{
					SessionKey:      "%1",
					PaneID:          "%1",
					WindowID:        "@1",
					Project:         "model-router",
					Agent:           "claude",
					ContextTokens:   184_000,
					ContextWindow:   200_000,
					ContextPct:      92.0,
					Warning:         model.ContextWarningCrit,
					CompactionCount: 2,
				},
				{
					SessionKey:      "%4",
					PaneID:          "%4",
					WindowID:        "@3",
					Project:         "spaces",
					Agent:           "codex",
					ContextTokens:   780_000,
					ContextWindow:   1_000_000,
					ContextPct:      78.0,
					Warning:         model.ContextWarningHigh,
					CompactionCount: 0,
				},
			},
		},
		Quotas: []agent.QuotaInfo{
			{
				Agent:     "claude",
				Source:    "oauth",
				UpdatedAt: nowUnix - 60, // fresh (<= 600s)
				Primary: &agent.QuotaWindow{
					Label:         "5h",
					WindowMinutes: 300,
					UsedPct:       18.0,
					ResetsAt:      nowUnix + 2*3600 + 14*60,
				},
				Secondary: &agent.QuotaWindow{
					Label:         "7d",
					WindowMinutes: 10080,
					UsedPct:       42.0,
					ResetsAt:      nowUnix + 3*86400 + 4*3600,
				},
			},
			{
				Agent:     "codex",
				Source:    "local",
				UpdatedAt: nowUnix - 120, // fresh
				Primary: &agent.QuotaWindow{
					Label:         "5h",
					WindowMinutes: 300,
					UsedPct:       5.0,
					ResetsAt:      nowUnix + 4*3600 + 30*60,
				},
				Secondary: &agent.QuotaWindow{
					Label:         "7d",
					WindowMinutes: 10080,
					UsedPct:       12.0,
					ResetsAt:      nowUnix + 5*86400 + 2*3600,
				},
			},
		},
		Projects: []model.ProjectSummary{
			{
				Name:          "model-router",
				Branch:        "feat/tabs-popup",
				HasGit:        true,
				FilesAdded:    3,
				FilesModified: 7,
				SessionCount:  1,
				ActiveCount:   1,
			},
			{
				Name:         "spaces",
				Branch:       "main",
				HasGit:       true,
				IsClean:      true,
				SessionCount: 1,
				ActiveCount:  1,
			},
		},
		Ports: []model.PortEntry{
			{Port: 3000, PID: 8100, Command: "node", Project: "model-router", SessionID: "sess-claude-01", PaneID: "%1", Duplicate: false},
			{Port: 5173, PID: 8200, Command: "vite", Project: "model-router", SessionID: "sess-claude-01", PaneID: "%1", Duplicate: true},
		},
		OrphanPorts: []model.OrphanPort{
			{Port: 8080, PID: 9999, Command: "python", Project: "old-app", SessionID: "dead-sess"},
		},
		Sessions: []model.SessionRow{
			{
				Key:              "%1",
				Space:            "model-router",
				WindowID:         "@1",
				WindowIndex:      0,
				PaneID:           "%1",
				Agent:            "claude",
				LaunchSurface:    agent.LaunchSurfaceIDE,
				PID:              4242,
				Project:          "model-router",
				SessionID:        "sess-claude-01",
				Profile:          "opus",
				Summary:          "Implement rich tabs popup",
				CurrentTask:      "Running go test ./internal/tui/...",
				Status:           model.StatusNeedsYou,
				Model:            "claude-opus-4-6",
				Usage:            agent.TokenUsage{InputTokens: 420_000, OutputTokens: 85_000, CacheReadTokens: 310_000, CacheWriteTokens: 45_000},
				TotalTokens:      860_000,
				ContextTokens:    184_000,
				ContextWindow:    200_000,
				ContextPct:       92.0,
				Warning:          model.ContextWarningCrit,
				CompactionCount:  2,
				MemKB:            262_144, // 256M
				TurnCount:        14,
				AvgTokensPerTurn: 61_428,
				TokenHistory:     []int64{12000, 28000, 45000, 62000, 81000, 54000, 92000, 73000},
				MemoryFiles:      2,
				MemoryLines:      148,
				Ports: []model.PortEntry{
					{Port: 3000, PID: 8100, Project: "model-router", SessionID: "sess-claude-01", PaneID: "%1"},
					{Port: 5173, PID: 8200, Project: "model-router", SessionID: "sess-claude-01", PaneID: "%1", Duplicate: true},
				},
			},
			{
				Key:              "%4",
				Space:            "model-router",
				WindowID:         "@3",
				WindowIndex:      2,
				PaneID:           "%4",
				Agent:            "codex",
				LaunchSurface:    agent.LaunchSurfaceCLI,
				PID:              7373,
				Project:          "spaces",
				SessionID:        "codex-99887766",
				Profile:          "default",
				Summary:          "Write tab popup tests",
				CurrentTask:      "Verifying narrow layout",
				Status:           model.StatusWorking,
				RateLimited:      true,
				Model:            "gpt-5-codex",
				Usage:            agent.TokenUsage{InputTokens: 210_000, OutputTokens: 40_000, CacheReadTokens: 120_000, CacheWriteTokens: 20_000},
				TotalTokens:      390_000,
				ContextTokens:    780_000,
				ContextWindow:    1_000_000,
				ContextPct:       78.0,
				Warning:          model.ContextWarningHigh,
				CompactionCount:  0,
				MemKB:            1_572_864, // 1.5G
				TurnCount:        8,
				AvgTokensPerTurn: 48_750,
				TokenHistory:     []int64{10000, 15000, 22000, 48000, 60000, 52000},
			},
		},
		MCPServers: []agent.LiveMCPServer{
			{
				Agent:          "codex",
				PID:            9100,
				ParentCLI:      "claude",
				Profile:        "work-prof",
				ActiveThreads:  2,
				TotalThreads:   5,
				LastActivityAt: nowUnix - 45,
			},
		},
	}
}

func TestTelemetryWidgets(t *testing.T) {
	// fmtTokens
	if got := fmtTokens(1_200_000); got != "1.2M" {
		t.Fatalf("fmtTokens(1.2M) = %q", got)
	}
	if got := fmtTokens(2_000_000); got != "2M" {
		t.Fatalf("fmtTokens(2M) = %q", got)
	}
	if got := fmtTokens(42_500); got != "42.5k" {
		t.Fatalf("fmtTokens(42.5k) = %q", got)
	}
	if got := fmtTokens(200_000); got != "200k" {
		t.Fatalf("fmtTokens(200k) = %q", got)
	}
	if got := fmtTokens(850); got != "850" {
		t.Fatalf("fmtTokens(850) = %q", got)
	}

	// fmtMemKB
	if got := fmtMemKB(0); got != "—" {
		t.Fatalf("fmtMemKB(0) = %q", got)
	}
	if got := fmtMemKB(131_072); got != "128M" {
		t.Fatalf("fmtMemKB(131072) = %q", got)
	}
	if got := fmtMemKB(1_572_864); got != "1.5G" {
		t.Fatalf("fmtMemKB(1572864) = %q", got)
	}

	// fmtCostUSD
	if got := fmtCostUSD(0, false); got != "—" {
		t.Fatalf("fmtCostUSD(0, false) = %q, want —", got)
	}
	if got := fmtCostUSD(0, true); got != "$0.00" {
		t.Fatalf("fmtCostUSD(0, true) = %q, want $0.00", got)
	}
	if got := fmtCostUSD(0.08, true); got != "$0.08" {
		t.Fatalf("fmtCostUSD(0.08, true) = %q, want $0.08", got)
	}
	if got := fmtCostUSD(1.24, true); got != "$1.24" {
		t.Fatalf("fmtCostUSD(1.24, true) = %q, want $1.24", got)
	}

	// fmtCountdown
	now := int64(1_700_000_000)
	if got := fmtCountdown(now-10, now); got != "" {
		t.Fatalf("fmtCountdown past = %q, want empty", got)
	}
	if got := fmtCountdown(now+45, now); got != "in 45s" {
		t.Fatalf("fmtCountdown 45s = %q", got)
	}
	if got := fmtCountdown(now+2*3600+14*60, now); got != "in 2h 14m" {
		t.Fatalf("fmtCountdown 2h14m = %q", got)
	}
	if got := fmtCountdown(now+3*86400+4*3600, now); got != "in 3d 4h" {
		t.Fatalf("fmtCountdown 3d4h = %q", got)
	}

	// fmtRelativeAge
	if got := fmtRelativeAge(0, now); got != "—" {
		t.Fatalf("fmtRelativeAge(0) = %q", got)
	}
	if got := fmtRelativeAge(now-45, now); got != "45s ago" {
		t.Fatalf("fmtRelativeAge(45s) = %q", got)
	}
	if got := fmtRelativeAge(now-3*3600, now); got != "3h ago" {
		t.Fatalf("fmtRelativeAge(3h) = %q", got)
	}

	// shortenModel
	if got := shortenModel("claude-opus-4-6", 200_000); got != "opus4.6" {
		t.Fatalf("shortenModel opus = %q", got)
	}
	if got := shortenModel("claude-opus-4-6", 1_000_000); got != "opus4.6[1m]" {
		t.Fatalf("shortenModel opus 1m = %q", got)
	}
	if got := shortenModel("claude-sonnet-4-6[1m]", 200_000); got != "sonnet4.6[1m]" {
		t.Fatalf("shortenModel sonnet [1m] = %q", got)
	}
	if got := shortenModel("", 0); got != "-" {
		t.Fatalf("shortenModel empty = %q", got)
	}

	// agentBadge
	if got := agentBadge("claude", agent.LaunchSurfaceCLI); got != "*CC" {
		t.Fatalf("agentBadge claude cli = %q", got)
	}
	if got := agentBadge("claude", agent.LaunchSurfaceApp); got != "*CCa" {
		t.Fatalf("agentBadge claude app = %q", got)
	}
	if got := agentBadge("claude", agent.LaunchSurfaceIDE); got != "*CCi" {
		t.Fatalf("agentBadge claude ide = %q", got)
	}
	if got := agentBadge("codex", agent.LaunchSurfaceCLI); got != ">CD" {
		t.Fatalf("agentBadge codex = %q", got)
	}
	if got := agentBadge("opencode", agent.LaunchSurfaceCLI); got != "#OC" {
		t.Fatalf("agentBadge opencode = %q", got)
	}

	// meterBar & remainingBar width
	mb := meterBar(65, 10, ColorGreen, ColorYellow, ColorRed)
	if w := ansi.StringWidth(mb); w != 10 {
		t.Fatalf("meterBar width = %d, want 10", w)
	}
	if plain := ansi.Strip(mb); plain != strings.Repeat("■", 10) {
		t.Fatalf("meterBar plain = %q", plain)
	}
	rb := remainingBar(30, 8)
	if w := ansi.StringWidth(rb); w != 8 {
		t.Fatalf("remainingBar width = %d, want 8", w)
	}

	// brailleSparkline
	sp := brailleSparkline([]float64{0.0, 0.25, 0.5, 0.75, 1.0, 1.0}, 6, ColorGreen, ColorYellow, ColorRed)
	if w := ansi.StringWidth(sp); w != 6 {
		t.Fatalf("brailleSparkline width = %d, want 6", w)
	}
	if !strings.Contains(ansi.Strip(sp), "⣿") {
		t.Fatalf("expected peak braille glyph ⣿ in %q", ansi.Strip(sp))
	}

	// brailleGraphMultirow
	rows := brailleGraphMultirow([]float64{0.0, 0.2, 0.5, 0.8, 1.0, 1.0}, 8, 3, ColorGreen, ColorYellow, ColorRed)
	if len(rows) != 3 {
		t.Fatalf("brailleGraphMultirow rows = %d, want 3", len(rows))
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != 8 {
			t.Fatalf("brailleGraphMultirow row %d width = %d, want 8", i, w)
		}
	}
	if !strings.Contains(ansi.Strip(rows[0]), "⣿") {
		t.Fatalf("expected top row peak braille ⣿ in %q", ansi.Strip(rows[0]))
	}
}

func newTelemetryTestTabsModel(t *testing.T, fb *fakeTelemetryTabsBackend, width, height int) Model {
	t.Helper()
	fb.spaces = sampleTabsSpaces()
	fb.snap = sampleTelemetrySnapshot(tabsTestNow())
	m := New(fb, Options{
		InitialMode: ModeTabs,
		HomeDir:     "/home/u",
		Now:         tabsTestNow,
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(Model)
	m = runCmdBatch(m, m.loadSpacesCmd())
	return m
}

func TestTabsMonitorDesktopAndNarrowRendering(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)
	if !m.telemetryLoaded {
		t.Fatalf("expected telemetryLoaded after spacesMsg in ModeTabs")
	}

	// Toggle into monitor mode with 'm'.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)
	if !m.tabsMonitorMode {
		t.Fatalf("expected tabsMonitorMode=true after pressing 'm'")
	}

	desktopView := ansi.Strip(m.View())
	for _, want := range []string{
		"¹context",
		"12.4k/min",
		"92%⚠",
		"200k C2",
		"²quota",
		"CLAUDE",
		"CODEX",
		"in 2h 14m",
		"in 3d 4h",
		"³tokens",
		"860k",
		"Turns: 14",
		"⁴projects",
		"feat/tabs-popup",
		"+3",
		"~7",
		"clean",
		"⁵ports",
		":3000",
		":5173",
		":8080",
		"⚠orphan",
		"X kill orphans",
		"⁶sessions",
		"*CCi",
		"opus4.6",
		"RateLimit",
		"└─ Running go test",
		"256M",
		"⁷mcp servers",
		"work",
		"2/5",
		"45s ago",
	} {
		if !strings.Contains(desktopView, want) {
			t.Errorf("desktop monitor missing %q\n--- VIEW ---\n%s", want, desktopView)
		}
	}

	// Narrow mode (80x28): cycles Work -> Usage -> System via right arrow / 'l'.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 28})
	m = next.(Model)

	workView := ansi.Strip(m.View())
	if !strings.Contains(workView, "[Work]") || !strings.Contains(workView, "¹context") || !strings.Contains(workView, "⁶sessions") {
		t.Errorf("narrow Work tab missing expected panels:\n%s", workView)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(Model)
	usageView := ansi.Strip(m.View())
	if !strings.Contains(usageView, "[Usage]") || !strings.Contains(usageView, "²quota") || !strings.Contains(usageView, "³tokens") {
		t.Errorf("narrow Usage tab missing expected panels:\n%s", usageView)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = next.(Model)
	sysView := ansi.Strip(m.View())
	if !strings.Contains(sysView, "[System]") || !strings.Contains(sysView, "⁴projects") || !strings.Contains(sysView, "⁵ports") || !strings.Contains(sysView, "⁷mcp servers") {
		t.Errorf("narrow System tab missing expected panels:\n%s", sysView)
	}
}

func TestTabsMonitorInteractions(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)

	// Enter monitor mode.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)

	// Panel toggle 1-7: hide panel 2 (quota) and panel 7 (mcp servers).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	m = next.(Model)
	viewHidden := ansi.Strip(m.View())
	if strings.Contains(viewHidden, "²quota") {
		t.Errorf("expected panel 2 (quota) to be hidden after pressing '2'")
	}
	if strings.Contains(viewHidden, "⁷mcp servers") {
		t.Errorf("expected panel 7 (mcp servers) to be hidden after pressing '7'")
	}
	// Restore panel 2.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = next.(Model)
	if !strings.Contains(ansi.Strip(m.View()), "²quota") {
		t.Errorf("expected panel 2 (quota) to reappear after toggling '2' again")
	}

	// Session navigation j/k updates Panel 3 Tokens.
	if !strings.Contains(ansi.Strip(m.View()), "860k") {
		t.Fatalf("expected initial selected session tokens (860k)")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)
	if m.tabsMonitorSelected != 1 {
		t.Fatalf("tabsMonitorSelected = %d, want 1", m.tabsMonitorSelected)
	}
	if !strings.Contains(ansi.Strip(m.View()), "390k") {
		t.Errorf("expected Panel 3 Tokens to update to 390k after pressing 'j'")
	}

	// Shift+X kills orphan ports and triggers forced telemetry reload.
	prevLoads := fb.loadCalls.Load()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = next.(Model)
	if cmd == nil {
		t.Fatalf("expected killOrphanPortsCmd on Shift+X")
	}
	m = runCmdBatch(m, cmd)
	if fb.killCalls.Load() != 1 || len(fb.killedOrphans) != 1 || fb.killedOrphans[0].Port != 8080 {
		t.Fatalf("unexpected KillOrphanPorts calls=%d orphans=%+v", fb.killCalls.Load(), fb.killedOrphans)
	}
	if !strings.Contains(m.toast, "Killed 1 orphan port") {
		t.Errorf("expected orphan port kill toast, got %q", m.toast)
	}
	if fb.loadCalls.Load() <= prevLoads || !fb.lastForce.Load() {
		t.Errorf("expected forced telemetry reload after killing orphans")
	}

	// Enter focuses selected session's pane (%4 in @3) and quits.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.Quitting() {
		t.Fatalf("expected Quitting() after Enter on monitor session")
	}
	if len(fb.focusedPanes) != 1 || fb.focusedPanes[0] != [3]string{"model-router", "@3", "%4"} {
		t.Fatalf("focusedPanes = %+v, want [model-router @3 %%4]", fb.focusedPanes)
	}

	// Esc exits monitor mode back to tabs list without quitting.
	m2 := newTelemetryTestTabsModel(t, fb, 140, 42)
	next, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m2 = next.(Model)
	next, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 = next.(Model)
	if m2.tabsMonitorMode || m2.Quitting() {
		t.Fatalf("Esc in monitor mode should return to tabs list without quitting")
	}
}

func TestTabsEnrichedHeaderAndInspector(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 150, 42)

	// Select tab 0 (@1, pane %1 which matches SessionRow %1).
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	m = next.(Model)

	view := ansi.Strip(m.View())
	for _, want := range []string{
		// Enriched header pills
		"12.4k/m",
		"CC 82%",
		"CD 95%",
		"⚠ 2 ports",
		// Hint bar
		"monitor",
		// Enriched inspector Context & Tokens card
		"CONTEXT & TOKENS",
		"200k · C2 · MEM 148/200",
		"92%⚠",
		"860k",
		"14 turns",
		"420k",
		"85k",
		"310k",
		"45k",
		"tok/t",
		":3000",
		":5173 ⚠",
		// Details grid runtime additions
		"mem 256M",
		"ide",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("enriched tabs popup missing %q\n--- VIEW ---\n%s", want, view)
		}
	}
}

func TestTabsMonitorLiveFilterAndJump(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)

	// Enter monitor mode.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)

	// Hints bar shows filter & sort hints.
	initialView := ansi.Strip(m.View())
	for _, want := range []string{"/ filter", "s sort"} {
		if !strings.Contains(initialView, want) {
			t.Errorf("monitor hints missing %q\n%s", want, initialView)
		}
	}

	// Press '/' to start live filter and type "codexx", then Backspace -> "codex".
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = next.(Model)
	if !m.tabsMonitorFiltering {
		t.Fatalf("expected tabsMonitorFiltering=true after '/'")
	}
	for _, r := range "codexx" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.tabsMonitorFilter != "codex" {
		t.Fatalf("tabsMonitorFilter = %q, want %q", m.tabsMonitorFilter, "codex")
	}

	// Press Enter to commit filter (exits typing mode, keeps filter active).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.tabsMonitorFiltering {
		t.Fatalf("expected tabsMonitorFiltering=false after Enter")
	}
	if m.tabsMonitorFilter != "codex" {
		t.Fatalf("expected filter to remain %q after Enter, got %q", "codex", m.tabsMonitorFilter)
	}

	// Panel 6 header shows active filter, Panel 6 & Panel 3 reflect only the filtered codex session (390k).
	filteredView := ansi.Strip(m.View())
	if !strings.Contains(filteredView, "/ codex") {
		t.Errorf("expected active filter '/ codex' in view:\n%s", filteredView)
	}
	if !strings.Contains(filteredView, "390k") {
		t.Errorf("expected Panel 3 Tokens to update to filtered session (390k):\n%s", filteredView)
	}
	if strings.Contains(filteredView, "Implement rich tabs popup") {
		t.Errorf("expected non-matching claude session to be filtered out of Panel 6:\n%s", filteredView)
	}

	// Enter jumps to the filtered session's pane (%4 in @3), not the unfiltered index 0 (%1).
	mJump := m
	next, _ = mJump.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mJump = next.(Model)
	if !mJump.Quitting() {
		t.Fatalf("expected Quitting() after Enter on filtered session")
	}
	if len(fb.focusedPanes) != 1 || fb.focusedPanes[0] != [3]string{"model-router", "@3", "%4"} {
		t.Fatalf("focusedPanes = %+v, want [model-router @3 %%4]", fb.focusedPanes)
	}

	// Esc first clears active filter without exiting monitor mode; second Esc exits monitor mode.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.tabsMonitorFilter != "" || !m.tabsMonitorMode {
		t.Fatalf("first Esc should clear filter and stay in monitor mode: filter=%q mode=%v", m.tabsMonitorFilter, m.tabsMonitorMode)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.tabsMonitorMode {
		t.Fatalf("second Esc should exit monitor mode")
	}
}

func TestTabsMonitorSortCycle(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)

	// Replace sessions with 3 rows whose highest values differ for Context, Tokens, Memory, and Cost.
	m.telemetry.Sessions = []model.SessionRow{
		{
			Key:         "%1",
			Space:       "sp",
			WindowID:    "@1",
			PaneID:      "%1",
			Agent:       "claude",
			Project:     "alpha",
			Summary:     "alpha-task",
			Status:      model.StatusWorking,
			ContextPct:  40.0,
			TotalTokens: 100_000,
			MemKB:       512_000,
			Usage:       agent.TokenUsage{CostUSD: 4.50, CostPriced: true},
		},
		{
			Key:         "%2",
			Space:       "sp",
			WindowID:    "@2",
			PaneID:      "%2",
			Agent:       "codex",
			Project:     "beta",
			Summary:     "beta-task",
			Status:      model.StatusWorking,
			ContextPct:  95.0,
			TotalTokens: 200_000,
			MemKB:       256_000,
			Usage:       agent.TokenUsage{CostUSD: 1.10, CostPriced: true},
		},
		{
			Key:         "%3",
			Space:       "sp",
			WindowID:    "@3",
			PaneID:      "%3",
			Agent:       "opencode",
			Project:     "gamma",
			Summary:     "gamma-task",
			Status:      model.StatusWorking,
			ContextPct:  60.0,
			TotalTokens: 900_000,
			MemKB:       2_048_000,
			Usage:       agent.TokenUsage{CostUSD: 2.25, CostPriced: true},
		},
	}

	// Enter monitor mode.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)

	expectOrder := func(wantMode monitorSortMode, wantLabel string, wantFirstKey string) {
		t.Helper()
		if m.tabsMonitorSort != wantMode {
			t.Fatalf("tabsMonitorSort = %v, want %v", m.tabsMonitorSort, wantMode)
		}
		got := m.monitorSessions()
		if len(got) != 3 || got[0].Key != wantFirstKey {
			t.Fatalf("sort=%s first session = %q, want %q", wantLabel, got[0].Key, wantFirstKey)
		}
		if wantMode != monitorSortDefault {
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "sort:"+wantLabel) {
				t.Errorf("expected 'sort:%s' indicator in monitor view:\n%s", wantLabel, view)
			}
		}
	}

	// default -> alpha (%1)
	expectOrder(monitorSortDefault, "default", "%1")

	// s -> context (beta 95%)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	expectOrder(monitorSortContext, "context", "%2")

	// s -> tokens (gamma 900k)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	expectOrder(monitorSortTokens, "tokens", "%3")

	// s -> memory (gamma 2G, then alpha 512M, then beta 256M)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	expectOrder(monitorSortMemory, "memory", "%3")
	if got := m.monitorSessions(); got[1].Key != "%1" || got[2].Key != "%2" {
		t.Fatalf("memory sort tail = [%s, %s], want [%%1, %%2]", got[1].Key, got[2].Key)
	}

	// s -> cost (alpha $4.50)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	expectOrder(monitorSortCost, "cost", "%1")

	// s -> back to default
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(Model)
	expectOrder(monitorSortDefault, "default", "%1")
}

func TestTabsMonitorQuotaFreshnessAndPlanBadge(t *testing.T) {
	now := tabsTestNow()
	nowUnix := now.Unix()

	// Case 1: Claude Last-Known (2h old) + Codex with PlanType ("plus")
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)
	m.telemetry.Quotas = []agent.QuotaInfo{
		{
			Agent:     "claude",
			Source:    "statusline",
			UpdatedAt: nowUnix - 2*3600, // 2h old -> Last-Known (<= 24h)
			Primary: &agent.QuotaWindow{
				Label:    "5h",
				UsedPct:  35.0,
				ResetsAt: nowUnix + 2*3600 + 10*60,
			},
		},
		{
			Agent:     "codex",
			Source:    "local",
			PlanType:  "plus",
			UpdatedAt: nowUnix - 60, // fresh
			Primary: &agent.QuotaWindow{
				Label:    "5h",
				UsedPct:  20.0,
				ResetsAt: nowUnix + 3*3600,
			},
		},
	}

	panel := ansi.Strip(strings.Join(m.renderPanelQuota(60, 9), "\n"))
	for _, want := range []string{
		"CLAUDE ~last",
		"65%",
		"in 2h 10m",
		"CODEX (plus)",
		"80%",
		"in 3h 0m",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("quota panel missing %q:\n%s", want, panel)
		}
	}

	// Case 2: Estimated Claude (~est) + Stale Codex (>10m hides countdown)
	m.telemetry.Quotas = []agent.QuotaInfo{
		{
			Agent:     "claude",
			Source:    "estimated",
			Estimated: true,
			UpdatedAt: nowUnix - 1800,
			Primary: &agent.QuotaWindow{
				Label:    "5h",
				UsedPct:  40.0,
				ResetsAt: nowUnix + 4*3600,
			},
		},
		{
			Agent:     "codex",
			Source:    "local",
			PlanType:  "pro",
			UpdatedAt: nowUnix - 1200, // 20m old -> stale for codex
			Primary: &agent.QuotaWindow{
				Label:    "5h",
				UsedPct:  10.0,
				ResetsAt: nowUnix + 5*3600,
			},
		},
	}
	panel2 := ansi.Strip(strings.Join(m.renderPanelQuota(60, 9), "\n"))
	for _, want := range []string{"CLAUDE ~est", "60%", "in 4h 0m", "CODEX (pro)"} {
		if !strings.Contains(panel2, want) {
			t.Errorf("quota panel (estimated/stale) missing %q:\n%s", want, panel2)
		}
	}
	if strings.Contains(panel2, "in 5h 0m") {
		t.Errorf("stale codex quota should hide reset countdown:\n%s", panel2)
	}

	// Case 3: Claude > 24h old is stale and hides countdown.
	m.telemetry.Quotas = []agent.QuotaInfo{
		{
			Agent:     "claude",
			Source:    "statusline",
			UpdatedAt: nowUnix - 25*3600,
			Primary: &agent.QuotaWindow{
				Label:    "5h",
				UsedPct:  30.0,
				ResetsAt: nowUnix + 3600,
			},
		},
	}
	panel3 := ansi.Strip(strings.Join(m.renderPanelQuota(60, 9), "\n"))
	if strings.Contains(panel3, "~last") || strings.Contains(panel3, "in 1h 0m") {
		t.Errorf("claude quota >24h should be stale (no ~last or countdown):\n%s", panel3)
	}
}

func TestTabsMonitorCostAggregatesAndWorktreeBadge(t *testing.T) {
	fb := &fakeTelemetryTabsBackend{}
	m := newTelemetryTestTabsModel(t, fb, 140, 42)

	// Populate cost, aggregate memory, avg context %, and worktree count.
	m.telemetry.Context.TotalCostUSD = 1.89
	m.telemetry.Context.TotalMemKB = 1_835_008 // ~1.8G
	m.telemetry.Context.AvgContextPct = 85.0
	m.telemetry.Projects[0].WorktreeCount = 2
	m.telemetry.Sessions[0].Usage.CostUSD = 1.24
	m.telemetry.Sessions[0].Usage.CostPriced = true
	m.telemetry.Sessions[1].Usage.CostUSD = 0.65
	m.telemetry.Sessions[1].Usage.CostPriced = true

	// Check enriched inspector first (before entering monitor mode).
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	m = next.(Model)
	inspView := ansi.Strip(m.View())
	if !strings.Contains(inspView, "Cost") || !strings.Contains(inspView, "$1.24") {
		t.Errorf("enriched inspector missing Cost $1.24:\n%s", inspView)
	}

	// Enter monitor mode and check Panel 1, Panel 3, Panel 4, Panel 6.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)
	monView := ansi.Strip(m.View())
	for _, want := range []string{
		"$1.89",       // Panel 1 TotalCostUSD
		"Σ1.8G",       // Panel 1 TotalMemKB
		"avg 85%",     // Panel 1 AvgContextPct
		"Cost: $1.24", // Panel 3 Tokens Cost
		"⎇ 2",         // Panel 4 Projects WorktreeCount badge
		"Cost",        // Panel 6 Cost header
		"$1.24",       // Panel 6 session 0 cost
		"$0.65",       // Panel 6 session 1 cost
	} {
		if !strings.Contains(monView, want) {
			t.Errorf("monitor view missing %q:\n%s", want, monView)
		}
	}

	// Also verify compact Panel 1 strip renders cost, memory, and avg context %.
	compactCtx := ansi.Strip(strings.Join(m.renderPanelContext(120, 3), "\n"))
	for _, want := range []string{"$1.89", "Σ1.8G", "avg 85%"} {
		if !strings.Contains(compactCtx, want) {
			t.Errorf("compact Panel 1 missing %q:\n%s", want, compactCtx)
		}
	}
}

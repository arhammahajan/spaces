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

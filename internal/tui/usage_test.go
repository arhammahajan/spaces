package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/cli"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/usage"
)

type fakeUsageBackend struct {
	report     usage.Report
	err        error
	loadCalls  int
	forceCalls int
}

func (f *fakeUsageBackend) LoadSpaces(context.Context) ([]model.Space, error) { return nil, nil }
func (f *fakeUsageBackend) LoadHookDetail(context.Context, string, int) (*model.HookDetail, error) {
	return nil, nil
}
func (f *fakeUsageBackend) LoadHarnesses(context.Context) ([]model.HarnessEntry, error) {
	return nil, nil
}
func (f *fakeUsageBackend) OpenSpace(context.Context, string) error              { return nil }
func (f *fakeUsageBackend) FocusPane(context.Context, string, string, string) error { return nil }
func (f *fakeUsageBackend) CreateSpace(context.Context, string, string) error    { return nil }
func (f *fakeUsageBackend) RenameSpace(context.Context, string, string) error    { return nil }
func (f *fakeUsageBackend) KillSpace(context.Context, string) error              { return nil }
func (f *fakeUsageBackend) LaunchAgent(context.Context, string, string, string, string) error {
	return nil
}

func (f *fakeUsageBackend) LoadUsage(_ context.Context, force bool) (usage.Report, error) {
	f.loadCalls++
	if force {
		f.forceCalls++
	}
	if f.err != nil {
		return usage.Report{}, f.err
	}
	return f.report, nil
}

func sampleUsageNow() time.Time {
	return time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
}

func sampleUsageReport() usage.Report {
	now := sampleUsageNow()
	daily := make([]usage.DailyBucket, 0, 14)
	for i := 13; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		claudeTok := int64(500_000 + (14-i)*100_000)
		codexTok := int64(300_000 + (14-i)*50_000)
		claudeCost := 1.25 + float64(14-i)*0.25
		codexCost := 0.85 + float64(14-i)*0.15
		daily = append(daily, usage.DailyBucket{
			Date: d,
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: claudeTok/4 + codexTok/4,
				CachedInputTokens:   claudeTok/2 + codexTok/2,
				CacheCreationTokens: claudeTok / 8,
				OutputTokens:        claudeTok/8 + codexTok/4,
			},
			CostUSD:         claudeCost + codexCost,
			CacheSavingsUSD: 1.40,
			CategoryCost: usage.CategoryCost{
				Input:      0.80,
				CacheRead:  0.30,
				CacheWrite: 0.20,
				Output:     claudeCost + codexCost - 1.30,
			},
			Records:  12,
			Sessions: 3,
			ByProvider: map[string]usage.SliceMetric{
				"claude": {Key: "claude", Tokens: claudeTok, CostUSD: claudeCost, Records: 7},
				"codex":  {Key: "codex", Tokens: codexTok, CostUSD: codexCost, Records: 5},
			},
			ByModel: map[string]usage.SliceMetric{
				"claude-sonnet-4": {Key: "claude-sonnet-4", Tokens: claudeTok, CostUSD: claudeCost, Records: 7},
				"gpt-5-codex":     {Key: "gpt-5-codex", Tokens: codexTok, CostUSD: codexCost, Records: 5},
			},
			ByProject: map[string]usage.SliceMetric{
				"spaces": {Key: "spaces", Tokens: claudeTok + codexTok, CostUSD: claudeCost + codexCost, Records: 12},
			},
		})
	}

	return usage.Report{
		GeneratedAtMs: now.UnixMilli(),
		Totals: agent.UsageTokenTotals{
			UncachedInputTokens: 4_500_000,
			CachedInputTokens:   12_000_000,
			CacheCreationTokens: 1_500_000,
			OutputTokens:        3_000_000,
		},
		CostUSD:         72.45,
		CacheSavingsUSD: 19.60,
		CacheHitRate:    0.667,
		CostPerMTokens:  3.45,
		Records:         168,
		Sessions:        24,
		ActiveDays:      14,
		CurrentStreak:   9,
		LongestStreak:   14,
		PeakDayDate:     now.Format("2006-01-02"),
		PeakDayTokens:   2_900_000,
		PeakDayCostUSD:  7.70,
		Daily:           daily,
		Providers: []usage.ProviderSummary{
			{
				Provider:     "claude",
				Totals:       agent.UsageTokenTotals{UncachedInputTokens: 2_500_000, CachedInputTokens: 8_000_000, OutputTokens: 1_800_000},
				CostUSD:      45.20,
				CostShare:    0.624,
				TokenShare:   0.586,
				CacheHitRate: 0.72,
				Sessions:     15,
			},
			{
				Provider:     "codex",
				Totals:       agent.UsageTokenTotals{UncachedInputTokens: 2_000_000, CachedInputTokens: 4_000_000, OutputTokens: 1_200_000},
				CostUSD:      27.25,
				CostShare:    0.376,
				TokenShare:   0.414,
				CacheHitRate: 0.57,
				Sessions:     9,
			},
		},
		Models: []usage.ModelSummary{
			{
				Model:           "claude-sonnet-4",
				Provider:        "claude",
				Totals:          agent.UsageTokenTotals{UncachedInputTokens: 2_500_000, CachedInputTokens: 8_000_000, OutputTokens: 1_800_000},
				CostUSD:         45.20,
				CacheSavingsUSD: 12.10,
				CategoryCost:    usage.CategoryCost{Input: 10.0, CacheRead: 3.2, CacheWrite: 2.0, Output: 30.0},
				CostShare:       0.624,
				CacheHitRate:    0.72,
				CostPerMTokens:  3.67,
			},
			{
				Model:           "gpt-5-codex",
				Provider:        "codex",
				Totals:          agent.UsageTokenTotals{UncachedInputTokens: 2_000_000, CachedInputTokens: 4_000_000, OutputTokens: 1_200_000},
				CostUSD:         27.25,
				CacheSavingsUSD: 7.50,
				CategoryCost:    usage.CategoryCost{Input: 7.25, CacheRead: 2.0, Output: 18.0},
				CostShare:       0.376,
				CacheHitRate:    0.57,
				CostPerMTokens:  3.78,
			},
		},
		SessionsList: []usage.SessionSummary{
			{
				SessionID:   "sess-1",
				Provider:    "claude",
				Project:     "spaces",
				Models:      []string{"claude-sonnet-4"},
				Totals:      agent.UsageTokenTotals{UncachedInputTokens: 400_000, CachedInputTokens: 1_200_000, OutputTokens: 300_000},
				CostUSD:     14.80,
				Records:     28,
				StartedAtMs: now.Add(-2 * time.Hour).UnixMilli(),
				UpdatedAtMs: now.Add(-15 * time.Minute).UnixMilli(),
				DurationMs:  (105 * time.Minute).Milliseconds(),
			},
			{
				SessionID:   "sess-2",
				Provider:    "codex",
				Project:     "codex-cli",
				Models:      []string{"gpt-5-codex"},
				Totals:      agent.UsageTokenTotals{UncachedInputTokens: 900_000, CachedInputTokens: 1_800_000, OutputTokens: 500_000},
				CostUSD:     9.50,
				Records:     19,
				StartedAtMs: now.Add(-4 * time.Hour).UnixMilli(),
				UpdatedAtMs: now.Add(-5 * time.Minute).UnixMilli(),
				DurationMs:  (45 * time.Minute).Milliseconds(),
			},
		},
		Tools: []usage.ToolSummary{
			{Name: "Bash", Count: 142},
			{Name: "Edit", Count: 89},
			{Name: "Read", Count: 210},
		},
		Burndown: usage.BurndownSummary{
			Quotas: []agent.QuotaInfo{
				{
					Agent:    "claude",
					PlanType: "Max",
					Primary: &agent.QuotaWindow{
						Label:         "5h",
						WindowMinutes: 300,
						UsedPct:       45,
						ResetsAt:      now.Add(2 * time.Hour).Unix(),
					},
					Secondary: &agent.QuotaWindow{
						Label:         "7d",
						WindowMinutes: 10080,
						UsedPct:       28,
						ResetsAt:      now.Add(72 * time.Hour).Unix(),
					},
				},
				{
					Agent:     "codex",
					PlanType:  "Pro",
					Estimated: true,
					Primary: &agent.QuotaWindow{
						Label:         "5h",
						WindowMinutes: 300,
						UsedPct:       85,
						ResetsAt:      now.Add(90 * time.Minute).Unix(),
					},
				},
			},
			ActiveBlocks: []usage.ActiveBlock{
				{
					Provider:    "gemini",
					TotalTokens: 420_000,
					LimitTokens: 1_000_000,
					UsedPct:     42,
					ResetsAt:    now.Add(3 * time.Hour).Unix(),
					Active:      true,
				},
			},
			Projection: usage.PlanProjection{
				Plan:            usage.PlanConfig{Name: "Pro", MonthlyUSD: 100, ResetDay: 1},
				PeriodStart:     "2026-11-01",
				PeriodEnd:       "2026-11-30",
				SpentUSD:        72.45,
				PercentUsed:     72.45,
				MedianDailyUSD:  4.90,
				Projected30dUSD: 147.00,
				Status:          usage.PlanStatusNear,
			},
			FiveHourHistory: []usage.FiveHourBucket{
				{
					Label:   "10:00 – 15:00",
					Totals:  agent.UsageTokenTotals{UncachedInputTokens: 300_000, CachedInputTokens: 900_000, OutputTokens: 200_000},
					CostUSD: 6.40,
					Records: 18,
					Current: true,
					ByProvider: map[string]usage.SliceMetric{
						"claude": {Key: "claude", Tokens: 1_000_000, CostUSD: 4.40},
						"codex":  {Key: "codex", Tokens: 400_000, CostUSD: 2.00},
					},
					ByModel: map[string]usage.SliceMetric{
						"claude-sonnet-4": {Key: "claude-sonnet-4", Tokens: 1_000_000, CostUSD: 4.40},
					},
				},
				{
					Label:   "05:00 – 10:00",
					Totals:  agent.UsageTokenTotals{UncachedInputTokens: 150_000, CachedInputTokens: 400_000, OutputTokens: 100_000},
					CostUSD: 2.80,
					Records: 9,
					ByProvider: map[string]usage.SliceMetric{
						"codex": {Key: "codex", Tokens: 650_000, CostUSD: 2.80},
					},
				},
			},
		},
	}
}

func newLoadedUsageModel(t *testing.T, fb *fakeUsageBackend, w, h int) Model {
	t.Helper()
	m := New(fb, Options{
		InitialMode: ModeUsage,
		Now:         sampleUsageNow,
	})
	m.width = w
	m.height = h
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil cmd for ModeUsage")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		cur := m
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			next, _ := cur.Update(sub())
			cur = next.(Model)
		}
		return cur
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func sendKey(m Model, k tea.KeyMsg) (Model, tea.Cmd) {
	next, cmd := m.Update(k)
	return next.(Model), cmd
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestUsageRenderAllTabsWideAndNarrow(t *testing.T) {
	sizes := [][2]int{{140, 40}, {72, 22}}
	for _, sz := range sizes {
		fb := &fakeUsageBackend{report: sampleUsageReport()}
		m := newLoadedUsageModel(t, fb, sz[0], sz[1])

		expectedSnippets := []string{
			"Token activity",          // Tab 0: Overview
			"claude",                  // Tab 1: Usage
			"Live Rate-Limit Windows", // Tab 2: Plan
			"claude-sonnet-4",         // Tab 3: Models
			"Showing 1–2 of 2",        // Tab 4: Sessions
		}

		for tab := 0; tab < 5; tab++ {
			m, _ = sendKey(m, runeKey(string(rune('1'+tab))))
			if m.usageTab != tab {
				t.Fatalf("size %dx%d: expected tab %d, got %d", sz[0], sz[1], tab, m.usageTab)
			}
			view := m.View()
			plain := ansi.Strip(view)
			lines := strings.Split(view, "\n")
			if len(lines) != sz[1] {
				t.Errorf("size %dx%d tab %d: expected %d lines, got %d", sz[0], sz[1], tab, sz[1], len(lines))
			}
			if !strings.Contains(plain, "Usage · $72.45") {
				t.Errorf("size %dx%d tab %d: missing title in view", sz[0], sz[1], tab)
			}
			if !strings.Contains(plain, expectedSnippets[tab]) {
				t.Errorf("size %dx%d tab %d: expected %q in view:\n%s", sz[0], sz[1], tab, expectedSnippets[tab], plain)
			}
		}
	}
}

func TestUsageKeyboardNavigationAndInteractions(t *testing.T) {
	fb := &fakeUsageBackend{report: sampleUsageReport()}
	m := newLoadedUsageModel(t, fb, 140, 40)

	// Tab / Shift+Tab cycling
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.usageTab != 1 {
		t.Fatalf("Tab expected tab 1, got %d", m.usageTab)
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.usageTab != 0 {
		t.Fatalf("Shift+Tab expected tab 0, got %d", m.usageTab)
	}

	// Overview: g cycles Daily -> Weekly -> Cumulative
	if m.usageOverviewAgg != usageAggDaily {
		t.Fatalf("expected Daily initially")
	}
	m, _ = sendKey(m, runeKey("g"))
	if m.usageOverviewAgg != usageAggWeekly {
		t.Fatalf("expected Weekly after g")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Each column = 1 week") {
		t.Errorf("expected weekly caption in view")
	}
	m, _ = sendKey(m, runeKey("g"))
	if m.usageOverviewAgg != usageAggCumulative {
		t.Fatalf("expected Cumulative after second g")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Running total") {
		t.Errorf("expected cumulative caption in view")
	}

	// Range cycling: r (default 30d -> 90d -> all -> 7d)
	m, _ = sendKey(m, runeKey("r"))
	if m.usageRange != usageRange90d {
		t.Fatalf("expected 90d, got %v", m.usageRange)
	}
	m, _ = sendKey(m, runeKey("r"))
	if m.usageRange != usageRangeAll {
		t.Fatalf("expected all, got %v", m.usageRange)
	}
	m, _ = sendKey(m, runeKey("r"))
	if m.usageRange != usageRange7d {
		t.Fatalf("expected 7d, got %v", m.usageRange)
	}

	// Usage tab: g (group), c (cost/tokens), ←/→ (day), Enter (expand)
	m, _ = sendKey(m, runeKey("2"))
	prevDay := m.usageDayIdx
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.usageDayIdx != prevDay-1 {
		t.Fatalf("expected day idx %d, got %d", prevDay-1, m.usageDayIdx)
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.usageDayIdx != prevDay {
		t.Fatalf("expected day idx %d, got %d", prevDay, m.usageDayIdx)
	}
	m, _ = sendKey(m, runeKey("g"))
	if m.usageGroupBy != usageGroupModel {
		t.Fatalf("expected Model grouping, got %v", m.usageGroupBy)
	}
	m, _ = sendKey(m, runeKey("c"))
	if m.usageUseCost {
		t.Fatalf("expected usageUseCost=false after c")
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.usageExpanded {
		t.Fatalf("expected usageExpanded=true after Enter")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Cache Read:") {
		t.Errorf("expected expanded breakdown details on Usage tab")
	}

	// Esc collapses expanded details without quitting
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.usageExpanded || m.quitting {
		t.Fatalf("expected Esc to collapse expanded details, not quit")
	}

	// Plan tab: ←/→ toggles 5h/Weekly, ↑/↓ selects 5h bucket, Enter expands
	m, _ = sendKey(m, runeKey("3"))
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.usagePlanWindow != 1 {
		t.Fatalf("expected Weekly window selected")
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.usageRowSel != 1 {
		t.Fatalf("expected row 1 selected on Plan tab")
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.usageExpanded {
		t.Fatalf("expected Plan 5h bucket expanded")
	}

	// Models tab: ↑/↓ + Enter expands category cost
	m, _ = sendKey(m, runeKey("4"))
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.View()), "Cache Read") {
		t.Errorf("expected inline category cost breakdown on Models tab")
	}

	// Sessions tab: s cycles sort Cost -> Tokens -> Recent
	m, _ = sendKey(m, runeKey("5"))
	if m.usageFilteredSessions()[0].SessionID != "sess-1" {
		t.Fatalf("expected sess-1 first by Cost")
	}
	m, _ = sendKey(m, runeKey("s"))
	if m.usageSessionSort != usageSortTokens {
		t.Fatalf("expected Tokens sort")
	}
	if m.usageFilteredSessions()[0].SessionID != "sess-2" {
		t.Fatalf("expected sess-2 first by Tokens")
	}
	m, _ = sendKey(m, runeKey("s"))
	if m.usageSessionSort != usageSortRecent {
		t.Fatalf("expected Recent sort")
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.View()), "Models:") {
		t.Errorf("expected inline session details after Enter")
	}

	// Provider filter: p cycles All -> claude -> codex -> All
	m, _ = sendKey(m, runeKey("p"))
	if m.usageProvider != "claude" {
		t.Fatalf("expected provider claude, got %q", m.usageProvider)
	}
	m, _ = sendKey(m, runeKey("p"))
	if m.usageProvider != "codex" {
		t.Fatalf("expected provider codex, got %q", m.usageProvider)
	}
	m, _ = sendKey(m, runeKey("p"))
	if m.usageProvider != "" {
		t.Fatalf("expected provider cleared, got %q", m.usageProvider)
	}

	// Dashboard grid: z opens, Tab moves focus, Enter zooms
	m, _ = sendKey(m, runeKey("z"))
	if !m.usageDashboard {
		t.Fatalf("expected dashboard open")
	}
	dashView := ansi.Strip(m.View())
	if !strings.Contains(dashView, "1 Overview") || !strings.Contains(dashView, "4 Models") {
		t.Errorf("expected dashboard cards in view:\n%s", dashView)
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.usageDashFocus != 1 {
		t.Fatalf("expected dash focus 1, got %d", m.usageDashFocus)
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.usageDashboard || m.usageTab != 1 {
		t.Fatalf("expected Enter to zoom into tab 1, dashboard=%v tab=%d", m.usageDashboard, m.usageTab)
	}

	// Help overlay: ? opens, Esc closes
	m, _ = sendKey(m, runeKey("?"))
	if !m.usageHelp {
		t.Fatalf("expected help overlay open")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Usage Shortcuts") {
		t.Errorf("expected help dialog content")
	}
	m, _ = sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.usageHelp || m.quitting {
		t.Fatalf("expected Esc to close help without quitting")
	}

	// Force refresh: R
	beforeForce := fb.forceCalls
	var cmd tea.Cmd
	m, cmd = sendKey(m, runeKey("R"))
	if cmd == nil {
		t.Fatalf("expected refresh cmd on R")
	}
	msg := cmd()
	m, _ = func() (Model, tea.Cmd) {
		n, c := m.Update(msg)
		return n.(Model), c
	}()
	if fb.forceCalls != beforeForce+1 {
		t.Fatalf("expected force refresh call to increment from %d to %d, got %d", beforeForce, beforeForce+1, fb.forceCalls)
	}

	// Esc at top level quits
	m, cmd = sendKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.quitting || cmd == nil {
		t.Fatalf("expected Esc to quit at top level")
	}
}

func TestUsageLoadingEmptyAndErrorStates(t *testing.T) {
	// Loading state (before usageMsg arrives)
	fb := &fakeUsageBackend{report: sampleUsageReport()}
	m := New(fb, Options{InitialMode: ModeUsage, Now: sampleUsageNow})
	m.width, m.height = 100, 30
	if !strings.Contains(ansi.Strip(m.View()), "Loading usage data…") {
		t.Errorf("expected loading state in view")
	}

	// Empty state
	emptyFB := &fakeUsageBackend{report: usage.Report{}}
	mEmpty := newLoadedUsageModel(t, emptyFB, 100, 30)
	if !strings.Contains(ansi.Strip(mEmpty.View()), "No usage data found.") {
		t.Errorf("expected empty state in view")
	}

	// Error state
	errFB := &fakeUsageBackend{err: errors.New("disk read failed")}
	mErr := newLoadedUsageModel(t, errFB, 100, 30)
	plain := ansi.Strip(mErr.View())
	if !strings.Contains(plain, "Couldn't load usage: disk read failed") || !strings.Contains(plain, "Press R to retry.") {
		t.Errorf("expected error state in view:\n%s", plain)
	}
}

func TestUsageMouseTabClickAndChartPrimitives(t *testing.T) {
	fb := &fakeUsageBackend{report: sampleUsageReport()}
	m := newLoadedUsageModel(t, fb, 120, 32)

	// Click on the 3rd tab ("3 Plan") on row y=1
	_, hits := usageTabBarLayout(usageTabNames, m.usageTab, 118)
	if len(hits) < 3 {
		t.Fatalf("expected at least 3 tab hits, got %d", len(hits))
	}
	clickX := hits[2].Start + 2 + 1 // +1 for horizontal pad
	next, _ := m.Update(tea.MouseMsg{X: clickX, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(Model)
	if m.usageTab != 2 {
		t.Fatalf("expected click to select tab 2 (Plan), got %d", m.usageTab)
	}

	// Overflow chevrons on very narrow tab bar
	narrowTabs := ansi.Strip(renderUsageTabBar(usageTabNames, 2, 26))
	if !strings.Contains(narrowTabs, "‹") || !strings.Contains(narrowTabs, "›") {
		t.Errorf("expected overflow chevrons on narrow tab bar, got %q", narrowTabs)
	}

	// Quota bar segments
	bar := ansi.Strip(renderCodexQuotaBar(45, 20))
	if !strings.HasPrefix(bar, "[") || !strings.HasSuffix(bar, "]") || len([]rune(bar)) != 22 {
		t.Errorf("unexpected quota bar %q", bar)
	}

	// Formatting helpers
	if got := formatCompactTokens(1_500_000); got != "1.5M" {
		t.Errorf("formatCompactTokens(1.5M) = %q", got)
	}
	if got := formatUSD(12.345); got != "$12.35" {
		t.Errorf("formatUSD(12.345) = %q", got)
	}
	if got := formatPct(55.4, 0); got != "55%" {
		t.Errorf("formatPct(55.4, 0) = %q", got)
	}
	if got := formatDurationShort((2*time.Hour + 15*time.Minute).Milliseconds()); got != "2h15m" {
		t.Errorf("formatDurationShort = %q", got)
	}
}

func TestCLIUsageDispatch(t *testing.T) {
	called := false
	var out, errBuf bytes.Buffer
	env := cli.Env{
		Stdout: &out,
		Stderr: &errBuf,
		RunUsage: func(_ cli.Env, args []string) int {
			called = true
			return cli.ExitOK
		},
	}
	code := cli.Run([]string{"usage"}, env)
	if code != cli.ExitOK || !called {
		t.Fatalf("cli.Run(usage) code=%d called=%v stderr=%q", code, called, errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = cli.Run([]string{"usage", "--help"}, env)
	if code != cli.ExitOK || !strings.Contains(out.String(), "usage: tmux-spaces usage") {
		t.Fatalf("cli.Run(usage --help) code=%d out=%q", code, out.String())
	}
}

type fakeUsageLimitsBackend struct {
	*fakeUsageBackend
	limits           usage.LimitsSnapshot
	limitsErr        error
	limitsCalls      int
	limitsForceCalls int
}

func (f *fakeUsageLimitsBackend) LoadUsageLimits(_ context.Context, force bool) (usage.LimitsSnapshot, error) {
	f.limitsCalls++
	if force {
		f.limitsForceCalls++
	}
	if f.limitsErr != nil {
		return usage.LimitsSnapshot{}, f.limitsErr
	}
	return f.limits, nil
}

func sampleLimitsSnapshot(now time.Time) usage.LimitsSnapshot {
	return usage.LimitsSnapshot{
		Version:     1,
		CheckedAtMs: now.UnixMilli(),
		Accounts: []usage.LimitAccount{
			{
				IdentityKey: "claude:default",
				Provider:    "claude",
				Profiles:    []string{"default"},
				Email:       "arham@example.com",
				Plan:        "Max",
				CheckedAt:   now.Format(time.RFC3339),
				ResetCredits: &agent.ResetCredits{
					AvailableCount: 2,
					NextExpiresAt:  now.AddDate(0, 1, 0).Format(time.RFC3339),
				},
				Windows: []agent.UsageLimitWindow{
					{
						ID:                 "session",
						Kind:               agent.LimitWindowSession,
						Label:              "Session",
						UsedPercent:        45,
						ResetsAt:           now.Add(2*time.Hour + 15*time.Minute).Format(time.RFC3339),
						WindowDurationMins: 300,
					},
					{
						ID:                 "weekly",
						Kind:               agent.LimitWindowWeekly,
						Label:              "Weekly",
						UsedPercent:        20,
						ResetsAt:           now.Add(48 * time.Hour).Format(time.RFC3339),
						WindowDurationMins: 10080,
					},
				},
			},
			{
				IdentityKey: "codex:default",
				Provider:    "codex",
				Profiles:    []string{"default"},
				Email:       "codex@example.com",
				Plan:        "Pro",
				CheckedAt:   now.Format(time.RFC3339),
				Stale:       true,
				Windows: []agent.UsageLimitWindow{
					{
						ID:                 "session",
						Kind:               agent.LimitWindowSession,
						Label:              "Session",
						UsedPercent:        85,
						ResetsAt:           now.Add(3 * time.Hour).Format(time.RFC3339),
						WindowDurationMins: 300,
					},
					{
						ID:                 "weekly",
						Kind:               agent.LimitWindowWeekly,
						Label:              "Weekly",
						UsedPercent:        50,
						ResetsAt:           now.Add(84 * time.Hour).Format(time.RFC3339),
						WindowDurationMins: 10080,
					},
				},
			},
			{
				IdentityKey: "antigravity:pratham",
				Provider:    "antigravity",
				Profiles:    []string{"pratham"},
				Email:       "pratham@example.com",
				Plan:        "Pro",
				CheckedAt:   now.Format(time.RFC3339),
				Windows: []agent.UsageLimitWindow{
					{
						ID:                 "gemini-5h",
						Kind:               agent.LimitWindowSession,
						Label:              "Gemini · 5-hour",
						UsedPercent:        25,
						ResetsAt:           now.Add(4 * time.Hour).Format(time.RFC3339),
						WindowDurationMins: 300,
					},
				},
			},
			{
				IdentityKey: "antigravity:saucerf166",
				Provider:    "antigravity",
				Profiles:    []string{"saucerf166"},
				Email:       "saucerf166@example.com",
				Plan:        "Go",
				CheckedAt:   now.Format(time.RFC3339),
				Windows: []agent.UsageLimitWindow{
					{
						ID:                 "claude-gpt-weekly",
						Kind:               agent.LimitWindowWeekly,
						Label:              "Claude & GPT · Weekly",
						UsedPercent:        60,
						ResetsAt:           now.Add(96 * time.Hour).Format(time.RFC3339),
						WindowDurationMins: 10080,
					},
				},
			},
		},
		Notices: []usage.LimitNotice{
			{
				Provider: "claude",
				Profile:  "work",
				Message:  "Could not read limits.",
			},
		},
	}
}

func runTeaCmd(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		cur := m
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			next, _ := cur.Update(sub())
			cur = next.(Model)
		}
		return cur
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestUsagePlanTabPerProfileLimitsAndResetCredits(t *testing.T) {
	now := sampleUsageNow()
	sizes := [][2]int{{140, 40}, {72, 22}}

	for _, sz := range sizes {
		fb := &fakeUsageLimitsBackend{
			fakeUsageBackend: &fakeUsageBackend{report: sampleUsageReport()},
			limits:           sampleLimitsSnapshot(now),
		}
		m := New(fb, Options{
			InitialMode: ModeUsage,
			Now:         func() time.Time { return now },
		})
		m.width = sz[0]
		m.height = sz[1]
		m = runTeaCmd(m, m.Init())

		// Switch to Plan tab (3)
		m, _ = sendKey(m, runeKey("3"))
		if m.usageTab != usageTabPlan {
			t.Fatalf("size %dx%d: expected usageTabPlan, got %d", sz[0], sz[1], m.usageTab)
		}

		// Render full Plan tab lines (and check View height)
		view := m.View()
		if lines := strings.Split(view, "\n"); len(lines) != sz[1] {
			t.Errorf("size %dx%d: expected %d lines, got %d", sz[0], sz[1], sz[1], len(lines))
		}

		// Check all rendered Plan tab lines (including scrolled content at narrow height)
		fullPlan := ansi.Strip(strings.Join(m.renderUsagePlanTab(sz[0]-2), "\n"))
		checks := []string{
			"Plan & Rate Limits",
			"Updated 14:30",
			"Claude",
			"Codex",
			"Antigravity",
			"pratham",
			"saucerf166",
			"Max",
			"Pro",
			"stale",
			"✦ 2 reset credits available",
			"(exp Dec 23)",
			"▲ ahead",
			"▼ under",
			"● on",
			"claude/work: Could not read limits.",
		}
		for _, want := range checks {
			if !strings.Contains(fullPlan, want) {
				t.Errorf("size %dx%d: expected %q in Plan tab:\n%s", sz[0], sz[1], want, fullPlan)
			}
		}

		// Check Dashboard Card #3 ("3 Plan") includes per-profile summary & reset credits
		dashCard := ansi.Strip(strings.Join(m.renderUsagePlanDashboardCard(50), "\n"))
		if !strings.Contains(dashCard, "claude/default") || !strings.Contains(dashCard, "✦ 2 reset credits available") {
			t.Errorf("size %dx%d: expected per-profile summary and reset credits in dashboard card:\n%s", sz[0], sz[1], dashCard)
		}
	}
}

func TestUsagePlanTabAutoRefreshOnSwitch(t *testing.T) {
	curNow := sampleUsageNow()
	fb := &fakeUsageLimitsBackend{
		fakeUsageBackend: &fakeUsageBackend{report: sampleUsageReport()},
		limits:           sampleLimitsSnapshot(curNow),
	}

	m := New(fb, Options{
		InitialMode: ModeUsage,
		Now:         func() time.Time { return curNow },
	})
	m.width = 140
	m.height = 40

	if !m.usageLimitsRefreshing {
		t.Fatalf("expected usageLimitsRefreshing=true immediately after New()")
	}

	// Init runs cache load + background live refresh
	m = runTeaCmd(m, m.Init())
	if m.usageLimitsRefreshing {
		t.Fatalf("expected usageLimitsRefreshing=false after Init refresh completes")
	}
	if fb.limitsCalls != 2 || fb.limitsForceCalls != 1 {
		t.Fatalf("expected 2 limits calls (1 cache, 1 force) from Init, got calls=%d force=%d", fb.limitsCalls, fb.limitsForceCalls)
	}

	// Switching to Plan tab within 15s cooldown should NOT trigger another refresh
	var cmd tea.Cmd
	m, cmd = sendKey(m, runeKey("3"))
	if cmd != nil {
		t.Fatalf("expected no refresh cmd within 15s cooldown")
	}
	if m.usageLimitsRefreshing {
		t.Fatalf("expected usageLimitsRefreshing=false within cooldown")
	}

	// Advance clock past 15s cooldown, switch away and back via Tab
	curNow = curNow.Add(16 * time.Second)
	m, _ = sendKey(m, runeKey("2"))
	m, cmd = sendKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.usageTab != usageTabPlan {
		t.Fatalf("expected tab to be usageTabPlan, got %d", m.usageTab)
	}
	if cmd == nil {
		t.Fatalf("expected auto-refresh cmd when switching to Plan tab after 15s cooldown")
	}
	if !m.usageLimitsRefreshing {
		t.Fatalf("expected usageLimitsRefreshing=true while refresh is in flight")
	}

	// Switching away and back while already refreshing must suppress duplicate refresh
	m, _ = sendKey(m, runeKey("1"))
	var dupCmd tea.Cmd
	m, dupCmd = sendKey(m, runeKey("3"))
	if dupCmd != nil {
		t.Fatalf("expected duplicate refresh suppressed while usageLimitsRefreshing=true")
	}

	// Complete the in-flight refresh
	m = runTeaCmd(m, cmd)
	if m.usageLimitsRefreshing {
		t.Fatalf("expected usageLimitsRefreshing=false after refresh completes")
	}
	if fb.limitsForceCalls != 2 {
		t.Fatalf("expected 2 force calls after auto-refresh, got %d", fb.limitsForceCalls)
	}

	// Manual force refresh (R) should refresh both usage report and usage limits
	beforeUsageForce := fb.forceCalls
	beforeLimitsForce := fb.limitsForceCalls
	m, cmd = sendKey(m, runeKey("R"))
	if cmd == nil {
		t.Fatalf("expected refresh cmd on R")
	}
	m = runTeaCmd(m, cmd)
	if fb.forceCalls != beforeUsageForce+1 {
		t.Fatalf("expected usage forceCalls=%d, got %d", beforeUsageForce+1, fb.forceCalls)
	}
	if fb.limitsForceCalls != beforeLimitsForce+1 {
		t.Fatalf("expected limits forceCalls=%d, got %d", beforeLimitsForce+1, fb.limitsForceCalls)
	}
}

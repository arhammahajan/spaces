package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/usage"
)

var usageTabNames = []string{"Overview", "Plan", "Models", "Sessions"}

const (
	usageTabOverview = 0
	usageTabPlan     = 1
	usageTabModels   = 2
	usageTabSessions = 3
)

type usageMsg struct {
	report  usage.Report
	refresh bool
	err     string
}

type usageLimitsMsg struct {
	snapshot usage.LimitsSnapshot
	refresh  bool
	err      string
}

const planLimitsRefreshCooldown = 15 * time.Second

func (m Model) loadUsageCmd(force bool) tea.Cmd {
	ub, ok := m.backend.(UsageBackend)
	if !ok {
		return func() tea.Msg {
			return usageMsg{refresh: force, err: "usage backend not configured"}
		}
	}
	return func() tea.Msg {
		rep, err := ub.LoadUsage(context.Background(), force)
		if err != nil {
			return usageMsg{refresh: force, err: err.Error()}
		}
		return usageMsg{report: rep, refresh: force}
	}
}

func (m Model) loadUsageLimitsCmd(force bool) tea.Cmd {
	ulb, ok := m.backend.(UsageLimitsBackend)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		snap, err := ulb.LoadUsageLimits(context.Background(), force)
		if err != nil {
			return usageLimitsMsg{refresh: force, err: err.Error()}
		}
		return usageLimitsMsg{snapshot: snap, refresh: force}
	}
}

func (m Model) handleUsageMsg(msg usageMsg) (tea.Model, tea.Cmd) {
	if msg.refresh {
		m.usageRefreshing = false
	}
	if msg.err != "" {
		if !m.usageLoaded || msg.refresh {
			m.usageLoading = false
			m.usageErr = msg.err
		}
		return m, nil
	}
	hasData := msg.report.Records > 0 || len(msg.report.Daily) > 0 || len(msg.report.Burndown.Quotas) > 0 || len(msg.report.Burndown.Limits.Accounts) > 0 || len(msg.report.SessionsList) > 0 || len(msg.report.Models) > 0
	if !msg.refresh && !hasData && m.usageRefreshing {
		// Initial DB cache was empty; keep showing Loading… until the
		// background refresh scan finishes so we don't flash an empty state.
		return m, nil
	}
	m.usageLoading = false
	m.usageErr = ""
	m.usageReport = msg.report
	m.usageLoaded = true
	if m.usageDayIdx < 0 {
		days := m.usageFilteredDaily()
		if len(days) > 0 {
			m.usageDayIdx = len(days) - 1
		}
	}
	m.clampUsageSelection()
	return m, nil
}

func (m Model) handleUsageLimitsMsg(msg usageLimitsMsg) (tea.Model, tea.Cmd) {
	if msg.refresh {
		m.usageLimitsRefreshing = false
	}
	if msg.err != "" {
		m.usageLimitsErr = msg.err
		return m, nil
	}
	if !msg.refresh && len(msg.snapshot.Accounts) == 0 && len(msg.snapshot.Notices) == 0 && m.usageLimitsRefreshing {
		// Initial cache was empty; keep refreshing silently without clobbering existing data.
		return m, nil
	}
	m.usageLimitsErr = ""
	m.usageLimits = msg.snapshot
	m.usageLimitsLoaded = true
	if msg.refresh {
		m.usageLimitsRefreshedAt = m.opts.NowOrDefault()
	}
	return m, nil
}

func (m *Model) maybeRefreshPlanLimits() tea.Cmd {
	if m.usageTab != usageTabPlan || m.usageLimitsRefreshing {
		return nil
	}
	if _, ok := m.backend.(UsageLimitsBackend); !ok {
		return nil
	}
	now := m.opts.NowOrDefault()
	if !m.usageLimitsRefreshedAt.IsZero() && now.Sub(m.usageLimitsRefreshedAt) < planLimitsRefreshCooldown {
		return nil
	}
	m.usageLimitsRefreshing = true
	return m.loadUsageLimitsCmd(true)
}

func (m Model) effectiveUsageLimits() usage.LimitsSnapshot {
	if m.usageLimitsLoaded && (len(m.usageLimits.Accounts) > 0 || len(m.usageLimits.Notices) > 0) {
		return m.usageLimits
	}
	return m.usageReport.Burndown.Limits
}

// ---------------------------------------------------------------------------
// Filtered views over m.usageReport
// ---------------------------------------------------------------------------

func (m Model) usageProviderList() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range m.usageReport.Providers {
		if p.Provider == "" {
			continue
		}
		if _, ok := seen[p.Provider]; !ok {
			seen[p.Provider] = struct{}{}
			out = append(out, p.Provider)
		}
	}
	for _, acc := range m.effectiveUsageLimits().Accounts {
		if acc.Provider == "" {
			continue
		}
		if _, ok := seen[acc.Provider]; !ok {
			seen[acc.Provider] = struct{}{}
			out = append(out, acc.Provider)
		}
	}
	return out
}

func (m Model) usageFilteredDaily() []usage.DailyBucket {
	src := m.usageReport.Daily
	if len(src) == 0 {
		return nil
	}
	now := m.opts.NowOrDefault()
	cutoff := ""
	if d := m.usageRange.days(); d > 0 {
		cutoff = now.AddDate(0, 0, -(d - 1)).Format("2006-01-02")
	}

	out := make([]usage.DailyBucket, 0, len(src))
	for _, b := range src {
		if cutoff != "" && b.Date < cutoff {
			continue
		}
		if m.usageProvider != "" {
			pm, ok := b.ByProvider[m.usageProvider]
			if !ok {
				continue
			}
			filtered := usage.DailyBucket{
				Date:       b.Date,
				CostUSD:    pm.CostUSD,
				Records:    pm.Records,
				ByProvider: map[string]usage.SliceMetric{m.usageProvider: pm},
				ByModel:    b.ByModel,
				ByProject:  b.ByProject,
			}
			filtered.Totals.UncachedInputTokens = pm.Tokens
			out = append(out, filtered)
		} else {
			out = append(out, b)
		}
	}
	return out
}

func (m Model) usageFilteredModels() []usage.ModelSummary {
	var out []usage.ModelSummary
	for _, ms := range m.usageReport.Models {
		if m.usageProvider != "" && ms.Provider != m.usageProvider {
			continue
		}
		out = append(out, ms)
	}
	return out
}

func (m Model) usageFilteredSessions() []usage.SessionSummary {
	var out []usage.SessionSummary
	for _, s := range m.usageReport.SessionsList {
		if m.usageProvider != "" && s.Provider != m.usageProvider {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		switch m.usageSessionSort {
		case usageSortTokens:
			ti, tj := out[i].Totals.Total(), out[j].Totals.Total()
			if ti != tj {
				return ti > tj
			}
		case usageSortRecent:
			if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
				return out[i].UpdatedAtMs > out[j].UpdatedAtMs
			}
		default:
			if out[i].CostUSD != out[j].CostUSD {
				return out[i].CostUSD > out[j].CostUSD
			}
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func (m Model) usageFilteredFiveHour() []usage.FiveHourBucket {
	var out []usage.FiveHourBucket
	for _, b := range m.usageReport.Burndown.FiveHourHistory {
		if m.usageProvider != "" {
			if _, ok := b.ByProvider[m.usageProvider]; !ok && b.Provider != m.usageProvider {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

func (m Model) usageFilteredQuotas() []agent.QuotaInfo {
	var out []agent.QuotaInfo
	for _, q := range m.usageReport.Burndown.Quotas {
		if m.usageProvider != "" && !strings.EqualFold(q.Agent, m.usageProvider) {
			continue
		}
		out = append(out, q)
	}
	return out
}

func mergeSliceMetricMap(dst, src map[string]usage.SliceMetric) {
	for k, v := range src {
		cur := dst[k]
		cur.Key = k
		cur.Tokens += v.Tokens
		cur.CostUSD += v.CostUSD
		cur.Records += v.Records
		dst[k] = cur
	}
}

func addDailyBucket(dst *usage.DailyBucket, src usage.DailyBucket) {
	dst.Totals.UncachedInputTokens += src.Totals.UncachedInputTokens
	dst.Totals.CachedInputTokens += src.Totals.CachedInputTokens
	dst.Totals.CacheCreationTokens += src.Totals.CacheCreationTokens
	dst.Totals.OutputTokens += src.Totals.OutputTokens
	dst.CostUSD += src.CostUSD
	dst.CacheSavingsUSD += src.CacheSavingsUSD
	dst.CategoryCost.Input += src.CategoryCost.Input
	dst.CategoryCost.CacheRead += src.CategoryCost.CacheRead
	dst.CategoryCost.CacheWrite += src.CategoryCost.CacheWrite
	dst.CategoryCost.Output += src.CategoryCost.Output
	dst.Records += src.Records
	dst.Sessions += src.Sessions
	if dst.ByProvider == nil {
		dst.ByProvider = map[string]usage.SliceMetric{}
	}
	if dst.ByModel == nil {
		dst.ByModel = map[string]usage.SliceMetric{}
	}
	if dst.ByProject == nil {
		dst.ByProject = map[string]usage.SliceMetric{}
	}
	mergeSliceMetricMap(dst.ByProvider, src.ByProvider)
	mergeSliceMetricMap(dst.ByModel, src.ByModel)
	mergeSliceMetricMap(dst.ByProject, src.ByProject)
}

func cloneDailyBucket(src usage.DailyBucket) usage.DailyBucket {
	var out usage.DailyBucket
	out.Date = src.Date
	addDailyBucket(&out, src)
	return out
}

func aggregateUsageBuckets(days []usage.DailyBucket, mode usageOverviewAgg) []usage.DailyBucket {
	if len(days) == 0 || mode == usageAggDaily {
		return days
	}
	switch mode {
	case usageAggWeekly:
		var out []usage.DailyBucket
		byWeek := map[string]int{}
		for _, d := range days {
			weekKey := d.Date
			if t, err := time.Parse("2006-01-02", d.Date); err == nil {
				sun := t.AddDate(0, 0, -int(t.Weekday()))
				weekKey = sun.Format("2006-01-02")
			}
			idx, ok := byWeek[weekKey]
			if !ok {
				idx = len(out)
				byWeek[weekKey] = idx
				out = append(out, usage.DailyBucket{Date: weekKey})
			}
			addDailyBucket(&out[idx], d)
		}
		return out
	case usageAggCumulative:
		out := make([]usage.DailyBucket, 0, len(days))
		var running usage.DailyBucket
		for _, d := range days {
			running.Date = d.Date
			addDailyBucket(&running, d)
			out = append(out, cloneDailyBucket(running))
		}
		return out
	default:
		return days
	}
}

func (m Model) usageChartBuckets() []usage.DailyBucket {
	return aggregateUsageBuckets(m.usageFilteredDaily(), m.usageOverviewAgg)
}

func (m *Model) clampUsageSelection() {
	buckets := m.usageChartBuckets()
	if len(buckets) == 0 {
		m.usageDayIdx = 0
	} else if m.usageDayIdx < 0 || m.usageDayIdx >= len(buckets) {
		m.usageDayIdx = len(buckets) - 1
	}

	var count int
	switch m.usageTab {
	case usageTabPlan:
		count = len(m.usageFilteredFiveHour())
	case usageTabModels:
		count = len(m.usageFilteredModels())
	case usageTabSessions:
		count = len(m.usageFilteredSessions())
	}
	if count == 0 {
		m.usageRowSel = 0
	} else {
		if m.usageRowSel < 0 {
			m.usageRowSel = 0
		}
		if m.usageRowSel >= count {
			m.usageRowSel = count - 1
		}
	}
	if m.usageScroll < 0 {
		m.usageScroll = 0
	}
}

// ---------------------------------------------------------------------------
// Key & Mouse Handling
// ---------------------------------------------------------------------------

func (m Model) handleUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := runeString(msg)

	if msg.Type == tea.KeyCtrlC || r == "q" {
		m.quitting = true
		return m, tea.Quit
	}
	if r == "?" {
		m.usageHelp = !m.usageHelp
		return m, nil
	}
	if msg.Type == tea.KeyEsc {
		if m.usageHelp {
			m.usageHelp = false
			return m, nil
		}
		if m.usageExpanded {
			m.usageExpanded = false
			return m, nil
		}
		if m.usageDashboard {
			m.usageDashboard = false
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	}
	if m.usageHelp {
		return m, nil
	}

	switch msg.Type {
	case tea.KeyTab:
		if m.usageDashboard {
			m.usageDashFocus = (m.usageDashFocus + 1) % 4
			return m, nil
		}
		cmd := m.switchUsageTab((m.usageTab + 1) % len(usageTabNames))
		return m, cmd
	case tea.KeyShiftTab:
		if m.usageDashboard {
			m.usageDashFocus = (m.usageDashFocus + 3) % 4
			return m, nil
		}
		cmd := m.switchUsageTab((m.usageTab + len(usageTabNames) - 1) % len(usageTabNames))
		return m, cmd
	case tea.KeyEnter:
		if m.usageDashboard {
			m.usageDashboard = false
			cmd := m.switchUsageTab(m.usageDashFocus)
			return m, cmd
		}
		m.usageExpanded = !m.usageExpanded
		return m, nil
	case tea.KeyLeft:
		m.usageHandleLeftRight(-1)
		return m, nil
	case tea.KeyRight:
		m.usageHandleLeftRight(1)
		return m, nil
	case tea.KeyUp:
		m.usageHandleUpDown(-1)
		return m, nil
	case tea.KeyDown:
		m.usageHandleUpDown(1)
		return m, nil
	case tea.KeyPgUp:
		m.usageHandlePage(-1)
		return m, nil
	case tea.KeyPgDown:
		m.usageHandlePage(1)
		return m, nil
	case tea.KeyHome:
		m.usageHandleHomeEnd(false)
		return m, nil
	case tea.KeyEnd:
		m.usageHandleHomeEnd(true)
		return m, nil
	}

	switch r {
	case "1", "2", "3", "4":
		idx := int(r[0] - '1')
		m.usageDashboard = false
		cmd := m.switchUsageTab(idx)
		return m, cmd
	case "z":
		if m.usageDashboard {
			m.usageDashboard = false
			cmd := m.switchUsageTab(m.usageDashFocus)
			return m, cmd
		} else {
			m.usageDashboard = true
			if m.usageTab < 4 {
				m.usageDashFocus = m.usageTab
			} else {
				m.usageDashFocus = 0
			}
			m.usageScroll = 0
		}
		return m, nil
	case "r":
		m.usageRange = m.usageRange.next()
		m.usageDayIdx = -1
		m.clampUsageSelection()
		return m, nil
	case "R":
		m.usageRefreshing = true
		if !m.usageLoaded {
			m.usageLoading = true
		}
		m.usageErr = ""
		cmds := []tea.Cmd{m.loadUsageCmd(true)}
		if _, ok := m.backend.(UsageLimitsBackend); ok {
			m.usageLimitsRefreshing = true
			m.usageLimitsErr = ""
			cmds = append(cmds, m.loadUsageLimitsCmd(true))
		}
		if len(cmds) == 1 {
			return m, cmds[0]
		}
		return m, tea.Batch(cmds...)
	case "p":
		provs := m.usageProviderList()
		if len(provs) == 0 {
			m.usageProvider = ""
		} else if m.usageProvider == "" {
			m.usageProvider = provs[0]
		} else {
			nextIdx := -1
			for i, p := range provs {
				if p == m.usageProvider {
					nextIdx = i + 1
					break
				}
			}
			if nextIdx < 0 || nextIdx >= len(provs) {
				m.usageProvider = ""
			} else {
				m.usageProvider = provs[nextIdx]
			}
		}
		m.usageDayIdx = -1
		m.usageRowSel = 0
		m.usageExpanded = false
		m.clampUsageSelection()
		return m, nil
	case "g":
		if m.usageTab == usageTabOverview {
			m.usageOverviewAgg = m.usageOverviewAgg.next()
			m.usageDayIdx = -1
			m.clampUsageSelection()
		}
		return m, nil
	case "b":
		if m.usageTab == usageTabOverview {
			m.usageGroupBy = m.usageGroupBy.next()
		}
		return m, nil
	case "c":
		if m.usageTab == usageTabOverview {
			m.usageUseCost = !m.usageUseCost
		}
		return m, nil
	case "s":
		if m.usageTab == usageTabSessions {
			m.usageSessionSort = m.usageSessionSort.next()
			m.usageRowSel = 0
			m.usageExpanded = false
		}
		return m, nil
	case "h":
		m.usageHandleLeftRight(-1)
		return m, nil
	case "l":
		m.usageHandleLeftRight(1)
		return m, nil
	case "k":
		m.usageHandleUpDown(-1)
		return m, nil
	case "j":
		m.usageHandleUpDown(1)
		return m, nil
	}
	return m, nil
}

func (m *Model) switchUsageTab(tab int) tea.Cmd {
	if tab < 0 || tab >= len(usageTabNames) {
		return nil
	}
	m.usageTab = tab
	m.usageRowSel = 0
	m.usageExpanded = false
	m.usageScroll = 0
	m.usageDayIdx = -1
	m.clampUsageSelection()
	return m.maybeRefreshPlanLimits()
}

func (m *Model) usageHandleLeftRight(delta int) {
	if m.usageDashboard {
		m.usageDashFocus = (m.usageDashFocus + delta + 4) % 4
		return
	}
	switch m.usageTab {
	case usageTabOverview:
		buckets := m.usageChartBuckets()
		if len(buckets) == 0 {
			return
		}
		m.usageDayIdx += delta
		if m.usageDayIdx < 0 {
			m.usageDayIdx = 0
		}
		if m.usageDayIdx >= len(buckets) {
			m.usageDayIdx = len(buckets) - 1
		}
	}
}

func (m Model) usageViewportMetrics() (innerW, bodyH int) {
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h <= 0 {
		h = 24
	}
	pad := 0
	if w > 2 {
		pad = 1
	}
	innerW = w - 2*pad
	if innerW < 10 {
		innerW = w
	}
	ctrlLines := m.renderUsageControls(innerW)
	// title (1) + tab bar (1) + controls (len) + top rule (1) + footer rule (1) + footer (2)
	chromeH := 1 + 1 + len(ctrlLines) + 1 + 1 + 2
	bodyH = h - chromeH
	if bodyH < 1 {
		bodyH = 1
	}
	return innerW, bodyH
}

func (m Model) usageMaxScroll() int {
	innerW, bodyH := m.usageViewportMetrics()
	var raw []string
	switch m.usageTab {
	case usageTabOverview:
		raw = m.renderUsageOverview(innerW, bodyH)
	case usageTabPlan:
		raw = m.renderUsagePlanTab(innerW)
	case usageTabModels:
		raw = m.renderUsageModelsTab(innerW)
	case usageTabSessions:
		return 0
	default:
		return 0
	}
	if len(raw) <= bodyH {
		return 0
	}
	return len(raw) - bodyH
}

func (m *Model) scrollUsageBy(delta int) {
	maxScroll := m.usageMaxScroll()
	m.usageScroll += delta
	if m.usageScroll < 0 {
		m.usageScroll = 0
	}
	if m.usageScroll > maxScroll {
		m.usageScroll = maxScroll
	}
}

func (m *Model) ensureUsageModelRowVisible() {
	_, bodyH := m.usageViewportMetrics()
	// Models tab has 2 header lines before row 0.
	rowTop := 2 + m.usageRowSel
	rowBot := rowTop
	if m.usageExpanded {
		rowBot = rowTop + 1
	}
	if m.usageRowSel == 0 {
		m.usageScroll = 0
		return
	}
	if rowTop < m.usageScroll {
		m.usageScroll = rowTop
	} else if rowBot >= m.usageScroll+bodyH {
		m.usageScroll = rowBot - bodyH + 1
	}
	maxScroll := m.usageMaxScroll()
	if m.usageScroll < 0 {
		m.usageScroll = 0
	}
	if m.usageScroll > maxScroll {
		m.usageScroll = maxScroll
	}
}

func (m *Model) usageHandleUpDown(delta int) {
	if m.usageDashboard {
		step := 1
		if m.width >= 104 {
			step = 2
		}
		m.usageDashFocus = (m.usageDashFocus + delta*step + 4) % 4
		return
	}
	switch m.usageTab {
	case usageTabPlan:
		maxScroll := m.usageMaxScroll()
		if maxScroll > 0 {
			if delta > 0 {
				if m.usageScroll < maxScroll {
					m.scrollUsageBy(delta)
					return
				}
			} else if delta < 0 {
				if m.usageRowSel > 0 {
					m.usageRowSel += delta
					m.clampUsageSelection()
					return
				}
				m.scrollUsageBy(delta)
				return
			}
		}
		m.usageRowSel += delta
		m.clampUsageSelection()
	case usageTabModels:
		m.usageRowSel += delta
		m.clampUsageSelection()
		m.ensureUsageModelRowVisible()
	case usageTabSessions:
		m.usageRowSel += delta
		m.usageScroll = 0
		m.clampUsageSelection()
	default:
		m.scrollUsageBy(delta)
	}
}

func (m *Model) usageHandlePage(dir int) {
	if m.usageDashboard {
		return
	}
	_, bodyH := m.usageViewportMetrics()
	step := bodyH / 2
	if step < 1 {
		step = 1
	}
	switch m.usageTab {
	case usageTabModels:
		m.usageRowSel += dir * step
		m.clampUsageSelection()
		m.ensureUsageModelRowVisible()
	case usageTabSessions:
		m.usageRowSel += dir * step
		m.usageScroll = 0
		m.clampUsageSelection()
	default:
		m.scrollUsageBy(dir * step)
	}
}

func (m *Model) usageHandleHomeEnd(toEnd bool) {
	if m.usageDashboard {
		return
	}
	switch m.usageTab {
	case usageTabModels:
		if toEnd {
			m.usageRowSel = len(m.usageFilteredModels()) - 1
		} else {
			m.usageRowSel = 0
		}
		m.clampUsageSelection()
		m.ensureUsageModelRowVisible()
	case usageTabSessions:
		if toEnd {
			m.usageRowSel = len(m.usageFilteredSessions()) - 1
		} else {
			m.usageRowSel = 0
		}
		m.usageScroll = 0
		m.clampUsageSelection()
	default:
		if toEnd {
			m.usageScroll = m.usageMaxScroll()
		} else {
			m.usageScroll = 0
		}
	}
}

func (m Model) handleUsageWheel(delta int) (tea.Model, tea.Cmd) {
	if m.usageHelp {
		return m, nil
	}
	m.usageHandleUpDown(delta)
	return m, nil
}

func (m Model) handleUsageLeftClick(x, y int) (tea.Model, tea.Cmd) {
	if m.usageHelp {
		m.usageHelp = false
		return m, nil
	}
	// Tab bar is always on row y == 1, inset by pad.
	if y == 1 {
		w := m.width
		if w <= 0 {
			w = 80
		}
		pad := 0
		if w > 2 {
			pad = 1
		}
		innerW := w - 2*pad
		_, hits := usageTabBarLayout(usageTabNames, m.usageTab, innerW)
		relX := x - pad
		for _, h := range hits {
			if relX >= h.Start && relX < h.End {
				m.usageDashboard = false
				cmd := m.switchUsageTab(h.Index)
				return m, cmd
			}
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// 6-Zone Codex AnalyticsView Renderer
// ---------------------------------------------------------------------------

func (m Model) renderUsage(width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	pad := 0
	if width > 2 {
		pad = 1
	}
	innerW := width - 2*pad
	if innerW < 10 {
		innerW = width
		pad = 0
	}
	inset := func(line string) string {
		if pad == 0 {
			return fitCellWidth(line, width)
		}
		return fitCellWidth(" "+fitCellWidth(line, innerW), width)
	}

	// Zone 1: Title banner (1 row, full-width ColorMantle surface)
	titleLine := m.renderUsageTitle(innerW, width, pad)
	// Zone 2: Filled Tab Bar (1 row, always at y == 1 for mouse hits)
	tabLine := renderUsageTabBar(usageTabNames, m.usageTab, innerW)
	// Zone 3: Controls Bar (1-2 rows)
	ctrlLines := m.renderUsageControls(innerW)
	// Zone 4: Horizontal Rule separating chrome from body (1 row)
	ruleLine := styleDivider.Render(strings.Repeat("─", innerW))
	// Zone 6: Footer Rule (1 row) + Footer Hints (2 fixed rows)
	foot1, foot2 := m.renderUsageFooter(innerW)

	chromeH := 1 + 1 + len(ctrlLines) + 1 + 1 + 2
	bodyH := height - chromeH
	if bodyH < 1 {
		bodyH = 1
	}

	// Zone 5: Body
	var bodyLines []string
	switch {
	case m.usageHelp:
		dialog := m.renderUsageHelpDialog(innerW)
		placed := lipgloss.Place(innerW, bodyH, lipgloss.Center, lipgloss.Center, dialog)
		bodyLines = strings.Split(placed, "\n")
	case m.usageLoading && !m.usageLoaded:
		bodyLines = m.renderUsageCenteredState(innerW, bodyH, styleAccent.Render("Loading usage data…"))
	case m.usageErr != "" && !m.usageLoaded:
		msg := styleError.Render("Couldn't load usage: "+m.usageErr) + "\n" + styleMuted.Render("Press R to retry.")
		bodyLines = m.renderUsageCenteredState(innerW, bodyH, msg)
	case !m.usageLoaded || m.usageIsEmpty():
		bodyLines = m.renderUsageCenteredState(innerW, bodyH, styleMuted.Render("No usage data found."))
	case m.usageDashboard:
		bodyLines = m.renderUsageDashboard(innerW, bodyH)
	default:
		var raw []string
		switch m.usageTab {
		case usageTabOverview:
			raw = m.renderUsageOverview(innerW, bodyH)
		case usageTabPlan:
			raw = m.renderUsagePlanTab(innerW)
		case usageTabModels:
			raw = m.renderUsageModelsTab(innerW)
		case usageTabSessions:
			raw = m.renderUsageSessionsTab(innerW, bodyH)
		}
		bodyLines = sliceUsageBody(raw, m.usageScroll, bodyH, innerW)
	}

	var out strings.Builder
	out.WriteString(titleLine)
	out.WriteByte('\n')
	out.WriteString(inset(tabLine))
	out.WriteByte('\n')
	for _, cl := range ctrlLines {
		out.WriteString(inset(cl))
		out.WriteByte('\n')
	}
	out.WriteString(inset(ruleLine))
	out.WriteByte('\n')
	for i := 0; i < bodyH; i++ {
		if i < len(bodyLines) {
			out.WriteString(inset(bodyLines[i]))
		} else {
			out.WriteString(fitCellWidth("", width))
		}
		out.WriteByte('\n')
	}
	out.WriteString(inset(ruleLine))
	out.WriteByte('\n')
	out.WriteString(inset(foot1))
	out.WriteByte('\n')
	out.WriteString(inset(foot2))
	return out.String()
}

func (m Model) usageIsEmpty() bool {
	r := m.usageReport
	limits := m.effectiveUsageLimits()
	return r.Records == 0 && len(r.Daily) == 0 && len(r.Burndown.Quotas) == 0 && len(limits.Accounts) == 0 && len(r.SessionsList) == 0 && len(r.Models) == 0
}

func (m Model) renderUsageCenteredState(width, height int, content string) []string {
	placed := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
	return strings.Split(placed, "\n")
}

func sliceUsageBody(lines []string, scroll, bodyH, width int) []string {
	if len(lines) <= bodyH {
		out := make([]string, bodyH)
		for i := 0; i < bodyH; i++ {
			if i < len(lines) {
				out[i] = fitCellWidth(lines[i], width)
			} else {
				out[i] = fitCellWidth("", width)
			}
		}
		return out
	}
	maxScroll := len(lines) - bodyH
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	out := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		out[i] = fitCellWidth(lines[scroll+i], width)
	}
	return out
}

func (m Model) renderUsageTitle(innerW, fullW, pad int) string {
	bg := lipgloss.Color(ColorMantle)
	boldText := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorText)).Bold(true)
	mutedText := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorOverlay0))
	accentText := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	padStr := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", pad))

	left := boldText.Render("Usage · " + formatUSD(m.usageReport.CostUSD))
	meta := fmt.Sprintf(" · %d providers · %d sessions", len(m.usageReport.Providers), m.usageReport.Sessions)
	left += mutedText.Render(meta)

	var right string
	if m.usageLoading && !m.usageLoaded {
		right = accentText.Render("Loading…")
	} else if m.usageRefreshing && m.usageLoaded {
		right = accentText.Render("Syncing…")
	} else if m.usageReport.GeneratedAtMs > 0 {
		t := time.UnixMilli(m.usageReport.GeneratedAtMs).In(m.opts.NowOrDefault().Location())
		right = mutedText.Render("Updated · " + t.Format("15:04"))
	} else {
		right = mutedText.Render("Updated · —")
	}

	leftW := ansi.StringWidth(left)
	rightW := ansi.StringWidth(right)
	var content string
	if leftW+rightW+2 <= innerW {
		gap := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", innerW-leftW-rightW))
		content = left + gap + right
	} else {
		content = fitCellWidthWithBG(left, innerW, ColorMantle)
	}
	if pad > 0 {
		return fitCellWidthWithBG(padStr+content+padStr, fullW, ColorMantle)
	}
	return fitCellWidthWithBG(content, fullW, ColorMantle)
}

// renderUsageSectionRule draws a clean titled hairline divider:
// "Title  ──────────────────────  Right"
func renderUsageSectionRule(title, right string, width int) string {
	titleSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	leftPart := titleSt.Render(title)
	lw := ansi.StringWidth(leftPart)

	if right == "" {
		rem := width - lw - 2
		if rem >= 3 {
			return fitCellWidth(leftPart+"  "+styleDivider.Render(strings.Repeat("─", rem)), width)
		}
		return fitCellWidth(leftPart, width)
	}

	rw := ansi.StringWidth(right)
	rem := width - lw - rw - 4
	if rem >= 3 {
		return fitCellWidth(leftPart+"  "+styleDivider.Render(strings.Repeat("─", rem))+"  "+right, width)
	}
	if lw+rw+2 <= width {
		return leftPart + strings.Repeat(" ", width-lw-rw) + right
	}
	return fitCellWidth(leftPart, width)
}

type usageKPIItem struct {
	label string
	value string
	color string
}

// renderUsageKPIStrip renders evenly distributed KPI columns separated by subtle vertical hairlines.
func renderUsageKPIStrip(items []usageKPIItem, width int) []string {
	if len(items) == 0 {
		return nil
	}
	cols := 1
	switch {
	case len(items) >= 5 && width >= 88:
		cols = 5
	case len(items) == 4 && width >= 68:
		cols = 4
	case width >= 54:
		cols = minInt(len(items), 3)
	case width >= 36:
		cols = minInt(len(items), 2)
	}

	var lines []string
	vSep := styleDivider.Render("│")
	for start := 0; start < len(items); start += cols {
		end := start + cols
		if end > len(items) {
			end = len(items)
		}
		rowItems := items[start:end]
		n := len(rowItems)
		sepTotal := (n - 1) * 3 // " │ " between columns
		avail := width - sepTotal
		if avail < n*10 {
			avail = n * 10
		}
		colW := avail / n

		var valRow, lblRow strings.Builder
		for i, k := range rowItems {
			cw := colW
			if i == n-1 {
				cw = avail - colW*(n-1)
			}
			valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(k.color)).Bold(true)
			valCell := fitCellWidth(" "+valSt.Render(k.value), cw)
			lblCell := fitCellWidth(" "+styleMuted.Render(k.label), cw)
			if i > 0 {
				valRow.WriteString(" " + vSep + " ")
				lblRow.WriteString(" " + vSep + " ")
			}
			valRow.WriteString(valCell)
			lblRow.WriteString(lblCell)
		}
		lines = append(lines, fitCellWidth(valRow.String(), width))
		lines = append(lines, fitCellWidth(lblRow.String(), width))
		if end < len(items) {
			lines = append(lines, "")
		}
	}
	return lines
}

func (m Model) renderUsageControls(width int) []string {
	sep := styleMuted.Render(" · ")
	keySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))

	var segs []string
	if m.usageDashboard {
		segs = append(segs, styleAccent.Render("Dashboard")+sep+styleMuted.Render("enter focus"))
	}

	// Range selector: r 7d/30d/90d/all
	ranges := []usageRange{usageRange7d, usageRange30d, usageRange90d, usageRangeAll}
	rParts := make([]string, len(ranges))
	for i, rg := range ranges {
		if rg == m.usageRange {
			rParts[i] = styleAccent.Render(rg.label())
		} else {
			rParts[i] = styleMuted.Render(rg.label())
		}
	}
	segs = append(segs, keySt.Render("r ")+strings.Join(rParts, styleMuted.Render("/")))

	// Contextual controls per tab
	if !m.usageDashboard {
		switch m.usageTab {
		case usageTabOverview:
			aggs := []usageOverviewAgg{usageAggDaily, usageAggWeekly, usageAggCumulative}
			parts := make([]string, len(aggs))
			for i, a := range aggs {
				if a == m.usageOverviewAgg {
					parts[i] = styleAccent.Render(a.label())
				} else {
					parts[i] = styleMuted.Render(a.label())
				}
			}
			segs = append(segs, keySt.Render("g ")+strings.Join(parts, styleMuted.Render("/")))

			groups := []usageGroupBy{usageGroupProvider, usageGroupModel, usageGroupProject, usageGroupTokenType}
			gParts := make([]string, len(groups))
			for i, g := range groups {
				if g == m.usageGroupBy {
					gParts[i] = styleAccent.Render(g.label())
				} else {
					gParts[i] = styleMuted.Render(g.label())
				}
			}
			segs = append(segs, keySt.Render("b ")+strings.Join(gParts, styleMuted.Render("/")))

			var costPart, tokPart string
			if m.usageUseCost {
				costPart = styleAccent.Render("Cost")
				tokPart = styleMuted.Render("Tokens")
			} else {
				costPart = styleMuted.Render("Cost")
				tokPart = styleAccent.Render("Tokens")
			}
			segs = append(segs, keySt.Render("c ")+costPart+styleMuted.Render("/")+tokPart)
		case usageTabSessions:
			sorts := []usageSessionSort{usageSortCost, usageSortTokens, usageSortRecent}
			parts := make([]string, len(sorts))
			for i, s := range sorts {
				if s == m.usageSessionSort {
					parts[i] = styleAccent.Render(s.label())
				} else {
					parts[i] = styleMuted.Render(s.label())
				}
			}
			segs = append(segs, keySt.Render("s sort: ")+strings.Join(parts, styleMuted.Render("/")))
		}
	}

	// Provider filter
	if m.usageProvider == "" {
		segs = append(segs, keySt.Render("p ")+styleMuted.Render("All"))
	} else {
		segs = append(segs, keySt.Render("p ")+styleAccent.Render(m.usageProvider))
	}

	line := strings.Join(segs, sep)
	if ansi.StringWidth(line) <= width || len(segs) <= 1 {
		return []string{fitCellWidth(line, width)}
	}
	// Wrap cleanly onto 2 lines when narrow.
	mid := (len(segs) + 1) / 2
	line1 := strings.Join(segs[:mid], sep)
	line2 := strings.Join(segs[mid:], sep)
	return []string{fitCellWidth(line1, width), fitCellWidth(line2, width)}
}

func (m Model) renderUsageFooter(width int) (string, string) {
	var ctxPairs [][2]string
	if m.usageDashboard {
		ctxPairs = [][2]string{
			{"tab/arrows", "focus card"},
			{"enter/z", "zoom tab"},
			{"r", "range"},
			{"esc", "exit dashboard"},
		}
	} else {
		switch m.usageTab {
		case usageTabOverview:
			ctxPairs = [][2]string{
				{"↑/↓", "scroll"},
				{"←/→", "day"},
				{"enter", "details"},
				{"r", "range"},
				{"g", "mode"},
				{"b", "group"},
				{"c", "cost/tokens"},
				{"esc", "close"},
			}
		case usageTabPlan:
			ctxPairs = [][2]string{
				{"↑/↓", "5h period"},
				{"enter", "details"},
				{"p", "provider"},
				{"esc", "back"},
			}
		case usageTabModels:
			ctxPairs = [][2]string{
				{"↑/↓", "select"},
				{"enter", "breakdown"},
				{"p", "provider"},
				{"esc", "back"},
			}
		case usageTabSessions:
			ctxPairs = [][2]string{
				{"↑/↓", "select"},
				{"enter", "details"},
				{"s", "sort"},
				{"p", "provider"},
				{"esc", "back"},
			}
		}
	}
	globalPairs := [][2]string{
		{"tab/1-4", "report"},
		{"z", "dashboard"},
		{"p", "provider"},
		{"?", "help"},
		{"R", "refresh"},
		{"q", "close"},
	}
	return renderHintsBar(ctxPairs, width), renderHintsBar(globalPairs, width)
}

// ---------------------------------------------------------------------------
// Tab 0 — Overview
// ---------------------------------------------------------------------------

func (m Model) renderUsageOverview(width, bodyH int) []string {
	r := m.usageReport
	days := m.usageFilteredDaily()

	// Compute range-scoped spend & tokens when filtered.
	totalSpend := r.CostUSD
	totalTokens := r.Totals.Total()
	if m.usageProvider != "" || m.usageRange != usageRangeAll {
		totalSpend = 0
		totalTokens = 0
		for _, d := range days {
			totalSpend += d.CostUSD
			totalTokens += d.Totals.Total()
		}
	}

	var lines []string

	// Responsive 5-stat KPI strip
	kpis := []usageKPIItem{
		{"Total spend", formatUSD(totalSpend), ColorGreen},
		{"Total tokens", formatCompactTokens(totalTokens), ColorBlue},
		{"Cache hit", formatPct(r.CacheHitRate*100, 1), ColorTeal},
		{"Current streak", fmt.Sprintf("%dd", r.CurrentStreak), ColorLavender},
		{"30d projected", formatUSD(r.Burndown.Projection.Projected30dUSD), ColorYellow},
	}
	lines = append(lines, renderUsageKPIStrip(kpis, width)...)
	lines = append(lines, "")

	// Stacked usage bar chart & selected-bucket breakdown (Daily / Weekly / Cumulative via g)
	if len(days) > 0 {
		lines = append(lines, m.renderUsageDailySection(width, bodyH)...)
		lines = append(lines, "")
	}

	// 52-week Token activity heatmap matrix (always stays a heatmap matrix)
	lines = append(lines, renderUsageSectionRule(
		"Token activity",
		styleMuted.Render("Last 12 months"),
		width,
	))
	heatLines := renderActivityHeatmap(r.Daily, usageAggDaily, false, width, m.opts.NowOrDefault())
	lines = append(lines, heatLines...)
	lines = append(lines, "")

	// 2-column summary section (>=76 cols, stacked below)
	if width >= 76 {
		leftW := (width - 3) / 2
		rightW := width - leftW - 3
		leftCol := m.renderOverviewInsights(r, leftW)
		rightCol := m.renderOverviewTopProvidersTools(r, rightW)
		vSep := styleDivider.Render("│")

		maxRows := len(leftCol)
		if len(rightCol) > maxRows {
			maxRows = len(rightCol)
		}
		for i := 0; i < maxRows; i++ {
			l, rr := "", ""
			if i < len(leftCol) {
				l = leftCol[i]
			}
			if i < len(rightCol) {
				rr = rightCol[i]
			}
			lines = append(lines, fitCellWidth(fitCellWidth(l, leftW)+" "+vSep+" "+fitCellWidth(rr, rightW), width))
		}
	} else {
		lines = append(lines, m.renderOverviewInsights(r, width)...)
		lines = append(lines, "")
		lines = append(lines, m.renderOverviewTopProvidersTools(r, width)...)
	}

	return lines
}

func (m Model) renderOverviewInsights(r usage.Report, width int) []string {
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	peakDesc := "—"
	if r.PeakDayDate != "" {
		peakDesc = fmt.Sprintf("%s (%s)", shortDateLabel(r.PeakDayDate), formatCompactTokens(r.PeakDayTokens))
	}
	rows := [][2]string{
		{"Cache savings", formatUSD(r.CacheSavingsUSD)},
		{"Cost / 1M tokens", formatUSD(r.CostPerMTokens)},
		{"7d median burn", formatUSD(r.Burndown.Projection.MedianDailyUSD) + "/day"},
		{"Peak day", peakDesc},
		{"Longest streak", fmt.Sprintf("%dd", r.LongestStreak)},
		{"Active days", fmt.Sprintf("%d", r.ActiveDays)},
		{"Sessions", fmt.Sprintf("%d", r.Sessions)},
	}
	out := []string{renderUsageSectionRule("Activity insights", "", width)}
	for _, kv := range rows {
		out = append(out, alignHeaderRight(" "+styleMuted.Render(kv[0]), valSt.Render(kv[1])+" ", width))
	}
	return out
}

func (m Model) renderOverviewTopProvidersTools(r usage.Report, width int) []string {
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	out := []string{renderUsageSectionRule("Top providers & tools", "", width)}

	provLimit := len(r.Providers)
	if provLimit > 4 {
		provLimit = 4
	}
	for i := 0; i < provLimit; i++ {
		p := r.Providers[i]
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[i%len(usageSeriesPalette)])).Render("●")
		left := fmt.Sprintf(" %s %s", dot, truncate(p.Provider, maxInt(10, width-20)))
		right := fmt.Sprintf("%s %4s ", valSt.Render(formatUSD(p.CostUSD)), styleMuted.Render(formatPct(p.CostShare*100, 0)))
		out = append(out, alignHeaderRight(left, right, width))
	}
	if len(r.Tools) > 0 {
		out = append(out, fitCellWidth(" "+styleSubtext.Render("Top tools"), width))
		toolLimit := len(r.Tools)
		if toolLimit > 4 {
			toolLimit = 4
		}
		for i := 0; i < toolLimit; i++ {
			t := r.Tools[i]
			left := "   " + truncate(t.Name, maxInt(10, width-16))
			right := styleMuted.Render(fmt.Sprintf("%d runs", t.Count)) + " "
			out = append(out, alignHeaderRight(left, right, width))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Daily Usage Section (Stacked Daily Bar Chart + Day Breakdown in Overview)
// ---------------------------------------------------------------------------

func (m Model) renderUsageDailySection(width, bodyH int) []string {
	rawDays := m.usageFilteredDaily()
	if len(rawDays) == 0 {
		return nil
	}
	buckets := aggregateUsageBuckets(rawDays, m.usageOverviewAgg)
	if len(buckets) == 0 {
		return nil
	}
	selIdx := m.usageDayIdx
	if selIdx < 0 || selIdx >= len(buckets) {
		selIdx = len(buckets) - 1
	}

	var rangeCost float64
	var rangeTok int64
	for _, d := range rawDays {
		rangeCost += d.CostUSD
		rangeTok += d.Totals.Total()
	}

	dateRange := shortDateLabel(rawDays[0].Date)
	if len(rawDays) > 1 {
		dateRange += " – " + shortDateLabel(rawDays[len(rawDays)-1].Date)
	}
	headline := renderUsageSectionRule(
		fmt.Sprintf("%s usage · %s · %s tokens", m.usageOverviewAgg.label(), formatUSD(rangeCost), formatCompactTokens(rangeTok)),
		styleMuted.Render(dateRange),
		width,
	)

	sideBySide := width >= 104
	panelW := 38
	if width >= 130 {
		panelW = 42
	}
	chartW := width
	chartH := 10
	if sideBySide {
		chartW = width - panelW - 3
		chartH = 11
	}
	if bodyH > 0 && chartH > bodyH-5 {
		chartH = bodyH - 5
	}
	if chartH < 6 {
		chartH = 6
	}

	chartLines, series := renderStackedBarChart(buckets, selIdx, m.usageGroupBy, m.usageUseCost, chartW, chartH)
	breakdownW := width
	if sideBySide {
		breakdownW = panelW
	}
	breakdownLines := m.renderUsageDayBreakdown(buckets[selIdx], series, breakdownW)

	var lines []string
	lines = append(lines, headline)
	lines = append(lines, "")

	if sideBySide {
		vSep := styleDivider.Render("│")
		maxRows := len(chartLines)
		if len(breakdownLines) > maxRows {
			maxRows = len(breakdownLines)
		}
		for i := 0; i < maxRows; i++ {
			l, rr := "", ""
			if i < len(chartLines) {
				l = chartLines[i]
			}
			if i < len(breakdownLines) {
				rr = breakdownLines[i]
			}
			lines = append(lines, fitCellWidth(fitCellWidth(l, chartW)+" "+vSep+" "+fitCellWidth(rr, panelW), width))
		}
	} else {
		lines = append(lines, chartLines...)
		lines = append(lines, "")
		lines = append(lines, breakdownLines...)
	}

	return lines
}

func (m Model) renderUsageDayBreakdown(day usage.DailyBucket, series []usageSeriesItem, width int) []string {
	meta := styleMuted.Render(fmt.Sprintf("%s · %s", formatUSD(day.CostUSD), formatCompactTokens(day.Totals.Total())))
	lines := []string{renderUsageSectionRule(shortDateLabel(day.Date), meta, width)}

	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	maxLbl := width - 18
	if maxLbl < 10 {
		maxLbl = 10
	}
	for _, s := range series {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color)).Render("●")
		var val string
		if m.usageUseCost {
			val = formatUSD(s.CostUSD)
		} else {
			val = formatCompactTokens(s.Tokens)
		}
		left := fmt.Sprintf(" %s %s", dot, truncate(s.Label, maxLbl))
		right := fmt.Sprintf("%s  %s ", styleMuted.Render(fmt.Sprintf("%4s", formatPct(s.Share, 0))), valSt.Render(val))
		lines = append(lines, alignHeaderRight(left, right, width))
	}

	if m.usageExpanded {
		lines = append(lines, styleDivider.Render(strings.Repeat("─", width)))
		lines = append(lines, fitCellWidth(" "+styleMuted.Render(fmt.Sprintf("Input: %s · Cache Read: %s",
			formatCompactTokens(day.Totals.UncachedInputTokens),
			formatCompactTokens(day.Totals.CachedInputTokens),
		)), width))
		lines = append(lines, fitCellWidth(" "+styleMuted.Render(fmt.Sprintf("Cache Write: %s · Output: %s",
			formatCompactTokens(day.Totals.CacheCreationTokens),
			formatCompactTokens(day.Totals.OutputTokens),
		)), width))
		lines = append(lines, fitCellWidth(" "+styleMuted.Render(fmt.Sprintf("Records: %d · Sessions: %d · Saved: %s",
			day.Records, day.Sessions, formatUSD(day.CacheSavingsUSD),
		)), width))
	}
	return lines
}

// ---------------------------------------------------------------------------
// Tab 2 — Plan & Burndown
// ---------------------------------------------------------------------------

var limitProviderOrder = []string{
	"claude",
	"codex",
	"antigravity",
	"opencode",
	"cursor-agent",
	"grok",
	"muse",
}

func (m Model) usageFilteredLimitAccounts() []usage.LimitAccount {
	limits := m.effectiveUsageLimits()
	var out []usage.LimitAccount
	for _, acc := range limits.Accounts {
		if m.usageProvider != "" && !strings.EqualFold(acc.Provider, m.usageProvider) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

func (m Model) usageFilteredLimitNotices() []usage.LimitNotice {
	limits := m.effectiveUsageLimits()
	var out []usage.LimitNotice
	for _, n := range limits.Notices {
		if m.usageProvider != "" && !strings.EqualFold(n.Provider, m.usageProvider) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func alignHeaderRight(left, right string, width int) string {
	if right == "" {
		return fitCellWidth(left, width)
	}
	lw := ansi.StringWidth(left)
	rw := ansi.StringWidth(right)
	if lw+rw+2 <= width {
		return left + strings.Repeat(" ", width-lw-rw) + right
	}
	return fitCellWidth(left, width)
}

func (m Model) renderUsagePlanTab(width int) []string {
	now := m.opts.NowOrDefault()
	limits := m.effectiveUsageLimits()
	accounts := m.usageFilteredLimitAccounts()
	notices := m.usageFilteredLimitNotices()
	quotas := m.usageFilteredQuotas()
	proj := m.usageReport.Burndown.Projection
	history := m.usageFilteredFiveHour()

	var lines []string

	// Right-aligned live limits refresh status
	var statusRight string
	switch {
	case m.usageLimitsRefreshing:
		statusRight = styleAccent.Render("Refreshing…")
	case limits.CheckedAtMs > 0:
		t := time.UnixMilli(limits.CheckedAtMs).In(now.Location())
		statusRight = styleMuted.Render("Updated " + t.Format("15:04"))
	case m.usageLimitsErr != "":
		statusRight = styleError.Render("Refresh failed")
	}

	// 1. Plan & Rate Limits (per-profile & per-harness) with legacy fallback
	if len(accounts) > 0 {
		lines = append(lines, renderUsageSectionRule("Plan & Rate Limits", statusRight, width))

		// Group accounts by provider in canonical order.
		byProv := map[string][]usage.LimitAccount{}
		for _, acc := range accounts {
			key := strings.ToLower(strings.TrimSpace(acc.Provider))
			if key == "" {
				key = "other"
			}
			byProv[key] = append(byProv[key], acc)
		}
		var provOrder []string
		seenProv := map[string]bool{}
		for _, p := range limitProviderOrder {
			if len(byProv[p]) > 0 {
				provOrder = append(provOrder, p)
				seenProv[p] = true
			}
		}
		var extraProvs []string
		for p := range byProv {
			if !seenProv[p] {
				extraProvs = append(extraProvs, p)
			}
		}
		sort.Strings(extraProvs)
		provOrder = append(provOrder, extraProvs...)

		provHeaderSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0)).Bold(true)
		for _, prov := range provOrder {
			group := byProv[prov]
			unit := "account"
			if len(group) != 1 {
				unit = "accounts"
			}
			subHdr := " " + styleAccent.Render("▸") + " " + provHeaderSt.Render(limitProviderDisplayName(prov)) +
				" " + styleMuted.Render(fmt.Sprintf("(%d %s)", len(group), unit))
			lines = append(lines, fitCellWidth(subHdr, width))

			// Dynamically span across as many columns as fit cleanly for multi-profile
			// providers, balancing rows (e.g. 4 accounts -> 2x2 instead of 3+1).
			const minCardW = 66
			const sepW = 3 // " │ "
			cols := (width + sepW) / (minCardW + sepW)
			if cols < 1 {
				cols = 1
			}
			if cols > len(group) {
				cols = len(group)
			}
			if cols > 1 && len(group) > cols {
				rows := (len(group) + cols - 1) / cols
				cols = (len(group) + rows - 1) / rows
			}

			if cols >= 2 {
				vSep := styleDivider.Render("│")
				totalSep := (cols - 1) * sepW
				baseW := (width - totalSep) / cols
				for i := 0; i < len(group); i += cols {
					end := i + cols
					if end > len(group) {
						end = len(group)
					}
					rowCount := end - i
					rowCards := make([][]string, rowCount)
					colWidths := make([]int, cols)
					maxR := 0
					for c := 0; c < cols; c++ {
						cw := baseW
						if c == cols-1 {
							cw = width - totalSep - baseW*(cols-1)
						}
						colWidths[c] = cw
						if c < rowCount {
							rowCards[c] = m.renderLimitAccountCard(group[i+c], now, cw, true)
							if len(rowCards[c]) > maxR {
								maxR = len(rowCards[c])
							}
						}
					}
					for r := 0; r < maxR; r++ {
						var rb strings.Builder
						for c := 0; c < rowCount; c++ {
							if c > 0 {
								rb.WriteString(" " + vSep + " ")
							}
							cell := ""
							if r < len(rowCards[c]) {
								cell = rowCards[c][r]
							}
							rb.WriteString(fitCellWidth(cell, colWidths[c]))
						}
						lines = append(lines, fitCellWidth(rb.String(), width))
					}
					lines = append(lines, "")
				}
			} else {
				for _, acc := range group {
					lines = append(lines, m.renderLimitAccountCard(acc, now, width, false)...)
					lines = append(lines, "")
				}
			}
		}
	} else {
		// Graceful fallback to legacy Quotas + ActiveBlocks when no LimitsSnapshot accounts are present.
		covered := map[string]bool{}
		var cards [][]string
		for _, q := range quotas {
			covered[strings.ToLower(q.Agent)] = true
			cards = append(cards, m.renderQuotaCard(q, now))
		}
		for _, blk := range m.usageReport.Burndown.ActiveBlocks {
			if m.usageProvider != "" && !strings.EqualFold(blk.Provider, m.usageProvider) {
				continue
			}
			if covered[strings.ToLower(blk.Provider)] {
				continue
			}
			cards = append(cards, m.renderActiveBlockCard(blk, now))
		}

		if len(cards) > 0 {
			lines = append(lines, renderUsageSectionRule("Live Rate-Limit Windows", statusRight, width))
			if width >= 140 && len(cards) >= 2 {
				halfW := (width - 3) / 2
				rightW := width - halfW - 3
				vSep := styleDivider.Render("│")
				for i := 0; i < len(cards); i += 2 {
					left := cards[i]
					var right []string
					if i+1 < len(cards) {
						right = cards[i+1]
					}
					maxR := len(left)
					if len(right) > maxR {
						maxR = len(right)
					}
					for r := 0; r < maxR; r++ {
						l, rr := "", ""
						if r < len(left) {
							l = left[r]
						}
						if r < len(right) {
							rr = right[r]
						}
						lines = append(lines, fitCellWidth(fitCellWidth(l, halfW)+" "+vSep+" "+fitCellWidth(rr, rightW), width))
					}
					lines = append(lines, "")
				}
			} else {
				for _, c := range cards {
					lines = append(lines, c...)
					lines = append(lines, "")
				}
			}
		} else {
			lines = append(lines, renderUsageSectionRule("Plan & Rate Limits", statusRight, width))
			if m.usageLimitsRefreshing {
				lines = append(lines, styleMuted.Render("  Refreshing plan limits…"))
			} else {
				lines = append(lines, styleMuted.Render("  No live rate-limit windows reported."))
			}
			lines = append(lines, "")
		}
	}

	// Render any probe/status notices compactly below the limit cards.
	if len(notices) > 0 {
		warnSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow))
		for _, n := range notices {
			src := n.Provider
			if n.Profile != "" {
				src += "/" + n.Profile
			}
			msg := fmt.Sprintf("  %s %s", warnSt.Render("⚠"), styleMuted.Render(src+": "+n.Message))
			lines = append(lines, fitCellWidth(msg, width))
		}
		lines = append(lines, "")
	}

	// 2. Monthly Burndown & Projection
	period := "Current period"
	if proj.PeriodStart != "" && proj.PeriodEnd != "" {
		period = proj.PeriodStart + " – " + proj.PeriodEnd
	}
	lines = append(lines, renderUsageSectionRule("Monthly Burndown & Projection", styleMuted.Render(period), width))
	lines = append(lines, renderUsageKPIStrip([]usageKPIItem{
		{"Spent", formatUSD(proj.SpentUSD), ColorGreen},
		{"7d median burn", formatUSD(proj.MedianDailyUSD) + "/day", ColorBlue},
		{"30d projected", formatUSD(proj.Projected30dUSD), ColorYellow},
		{"Cache savings", formatUSD(m.usageReport.CacheSavingsUSD), ColorTeal},
	}, width)...)

	if proj.Plan.MonthlyUSD > 0 {
		badge := renderPlanStatusBadge(proj.Status)
		bar := renderCodexQuotaBar(proj.PercentUsed, 20)
		lines = append(lines, fitCellWidth(fmt.Sprintf("  %s  %s %s of %s %s",
			styleMuted.Render("Budget"), bar, formatPct(proj.PercentUsed, 0), formatUSD(proj.Plan.MonthlyUSD), badge,
		), width))
	}
	lines = append(lines, "")

	// 3. Recent 5-hour Periods
	lines = append(lines, renderUsageSectionRule("Recent 5-hour Periods", "", width))
	if len(history) == 0 {
		lines = append(lines, styleMuted.Render("  No 5-hour history recorded."))
		return lines
	}
	for i, b := range history {
		marker := "  "
		if i == m.usageRowSel {
			marker = styleAccent.Render("› ")
		}
		curr := " "
		if b.Current {
			curr = styleAccent.Render("*")
		}
		row := fmt.Sprintf("%s%s %-20s %10s  %10s  %s",
			marker,
			curr,
			truncate(b.Label, 20),
			formatCompactTokens(b.Totals.Total()),
			formatUSD(b.CostUSD),
			styleMuted.Render(fmt.Sprintf("%d reqs", b.Records)),
		)
		if i == m.usageRowSel {
			lines = append(lines, fitCellWidthWithBG(styleSelection.Render(row), width, ColorSurface0))
		} else {
			lines = append(lines, fitCellWidth(row, width))
		}
		if i == m.usageRowSel && m.usageExpanded {
			for k, pm := range b.ByProvider {
				lines = append(lines, fitCellWidth(fmt.Sprintf("      provider %-14s %10s  %s",
					truncate(k, 14), formatCompactTokens(pm.Tokens), formatUSD(pm.CostUSD)), width))
			}
			for k, mm := range b.ByModel {
				lines = append(lines, fitCellWidth(fmt.Sprintf("      model    %-14s %10s  %s",
					truncate(k, 14), formatCompactTokens(mm.Tokens), formatUSD(mm.CostUSD)), width))
			}
		}
	}
	return lines
}

func (m Model) renderLimitAccountCard(acc usage.LimitAccount, now time.Time, cardW int, inGrid bool) []string {
	profileLabel := strings.Join(acc.Profiles, ", ")
	if profileLabel == "" {
		profileLabel = acc.DisplayName
	}
	if profileLabel == "" {
		profileLabel = "default"
	}

	profSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	planSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal)).Bold(true)
	staleSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Bold(true)
	creditSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Bold(true)

	headPrefix := "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1)).Render("▎") + " "
	bodyPrefix := "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface0)).Render("▎") + " "
	prefixW := 4
	if inGrid {
		headPrefix = " "
		bodyPrefix = " "
		prefixW = 1
	}

	hdrParts := []string{profSt.Render(profileLabel)}
	if acc.Plan != "" {
		hdrParts = append(hdrParts, planSt.Render(acc.Plan))
	}
	if acc.Stale {
		hdrParts = append(hdrParts, staleSt.Render("stale"))
	}
	hdrLeft := headPrefix + strings.Join(hdrParts, " · ")
	var hdrLine string
	if acc.Email != "" {
		remW := cardW - ansi.StringWidth(hdrLeft) - 3
		if remW >= 8 {
			hdrRight := styleMuted.Render(truncate(acc.Email, remW)) + " "
			if inGrid {
				hdrLine = alignHeaderRight(hdrLeft, hdrRight, cardW)
			} else {
				hdrLine = fitCellWidth(hdrLeft+"  "+hdrRight, cardW)
			}
		} else {
			hdrLine = fitCellWidth(hdrLeft, cardW)
		}
	} else {
		hdrLine = fitCellWidth(hdrLeft, cardW)
	}
	out := []string{hdrLine}

	// Reset credits highlight line
	if acc.ResetCredits != nil && acc.ResetCredits.AvailableCount > 0 {
		noun := "reset credits"
		if acc.ResetCredits.AvailableCount == 1 {
			noun = "reset credit"
		}
		creditLine := fmt.Sprintf("%s%s", bodyPrefix, creditSt.Render(fmt.Sprintf("✦ %d %s available", acc.ResetCredits.AvailableCount, noun)))
		if acc.ResetCredits.NextExpiresAt != "" {
			exp := formatLimitExpiryDate(acc.ResetCredits.NextExpiresAt, now)
			if exp != "" {
				creditLine += " " + styleMuted.Render("(exp "+exp+")")
			}
		}
		out = append(out, fitCellWidth(creditLine, cardW))
	}

	// Unavailable message when no windows are reported
	if len(acc.Windows) == 0 {
		msg := "No windows reported"
		if acc.Unavailable != nil && acc.Unavailable.Message != "" {
			msg = acc.Unavailable.Message
		} else if acc.Unavailable != nil && acc.Unavailable.Reason != "" {
			msg = acc.Unavailable.Reason
		}
		out = append(out, fitCellWidth(bodyPrefix+styleMuted.Render(msg), cardW))
		return out
	}

	// Measure actual window labels and size labelW + barSegs dynamically to fit
	// cardW cleanly without clipping labels, pace tags, or reset countdowns.
	compact := cardW < 92
	colSep := "  "
	resetReserve := 16
	if compact {
		colSep = " "
		resetReserve = 9
	}

	maxLbl := 0
	for _, w := range acc.Windows {
		lbl := strings.TrimSpace(w.Label)
		if lbl == "" {
			lbl = w.Kind
		}
		if lbl == "" {
			lbl = w.ID
		}
		if n := len([]rune(lbl)); n > maxLbl {
			maxLbl = n
		}
	}
	if maxLbl < 14 {
		maxLbl = 14
	}
	if maxLbl > 26 {
		maxLbl = 26
	}

	// Fixed row width excluding labelW and barSegs:
	// prefixW + 1 (space) + 2 ("[]") + 10 (" 100% left") + len(colSep) + 7 (pace) + len(colSep) + resetReserve + 1 (right pad)
	fixedOther := prefixW + 1 + 2 + 10 + len(colSep) + 7 + len(colSep) + resetReserve + 1
	avail := cardW - fixedOther
	if avail < 22 {
		avail = 22
	}

	barSegs := 12
	switch {
	case avail >= 42:
		barSegs = 20
	case avail >= 36:
		barSegs = 16
	case avail >= 28:
		barSegs = 12
	default:
		barSegs = 10
	}

	labelW := maxLbl
	if labelW > avail-barSegs {
		labelW = avail - barSegs
	}
	if labelW < 12 {
		labelW = 12
	}
	// If labels are short, let the bar expand up to 20 segments to use extra width.
	if extra := avail - labelW; extra > barSegs {
		barSegs = extra
		if barSegs > 20 {
			barSegs = 20
		}
	}

	for _, w := range acc.Windows {
		lblText := strings.TrimSpace(w.Label)
		if lblText == "" {
			lblText = w.Kind
		}
		if lblText == "" {
			lblText = w.ID
		}
		paddedLbl := styleSubtext.Render(fmt.Sprintf("%-*s", labelW, truncate(lblText, labelW)))

		bar := renderCodexQuotaBar(w.UsedPercent, barSegs)
		rem := 100.0 - w.UsedPercent
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}

		row := fmt.Sprintf("%s%s %s %4s left", bodyPrefix, paddedLbl, bar, formatPct(rem, 0))
		pace, hasPace := computeLimitPace(w, now)
		resetStr := formatLimitResetCountdown(w.ResetsAt, now, compact)
		if hasPace {
			row += colSep + renderLimitPaceTag(pace, w.UsedPercent)
		} else if resetStr != "" {
			row += colSep + renderLimitPaceTag("", 0)
		}
		if resetStr != "" {
			row += colSep + styleMuted.Render(resetStr)
		}
		out = append(out, fitCellWidth(row, cardW))
	}

	return out
}

func (m Model) renderUsagePlanDashboardCard(width int) []string {
	accounts := m.usageFilteredLimitAccounts()
	if len(accounts) == 0 {
		return m.renderUsagePlanTab(width)
	}

	var lines []string
	var totalCredits int
	profSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	planSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal))
	creditSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Bold(true)

	shown := 0
	for _, acc := range accounts {
		if acc.ResetCredits != nil && acc.ResetCredits.AvailableCount > 0 {
			totalCredits += acc.ResetCredits.AvailableCount
		}
		if shown >= 4 || len(acc.Windows) == 0 {
			continue
		}
		w := acc.Windows[0]
		rem := 100.0 - w.UsedPercent
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}
		prof := acc.Provider
		if len(acc.Profiles) > 0 {
			prof += "/" + acc.Profiles[0]
		}
		bar := renderCodexQuotaBar(w.UsedPercent, 10)
		planPart := ""
		if acc.Plan != "" {
			planPart = " " + planSt.Render(acc.Plan)
		}
		row := fmt.Sprintf("%-18s %s %s left%s",
			profSt.Render(truncate(prof, 18)),
			bar,
			formatPct(rem, 0),
			planPart,
		)
		lines = append(lines, fitCellWidth(row, width))
		shown++
	}

	if totalCredits > 0 {
		noun := "reset credits"
		if totalCredits == 1 {
			noun = "reset credit"
		}
		lines = append(lines, fitCellWidth(creditSt.Render(fmt.Sprintf("✦ %d %s available", totalCredits, noun)), width))
	}

	proj := m.usageReport.Burndown.Projection
	if proj.Projected30dUSD > 0 || proj.SpentUSD > 0 {
		lines = append(lines, fitCellWidth(
			styleMuted.Render(fmt.Sprintf("30d proj: %s · spent: %s", formatUSD(proj.Projected30dUSD), formatUSD(proj.SpentUSD))),
			width,
		))
	}
	return lines
}

func (m Model) renderQuotaCard(q agent.QuotaInfo, now time.Time) []string {
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true).Render(q.Agent)
	if q.PlanType != "" {
		title += " " + styleMuted.Render("("+q.PlanType+")")
	}
	if q.Estimated {
		title += " " + styleMuted.Render("(est)")
	}
	out := []string{"  " + title}

	formatRow := func(label string, w *agent.QuotaWindow, highlight bool) string {
		if w == nil {
			return ""
		}
		bar := renderCodexQuotaBar(w.UsedPct, 20)
		rem := w.RemainingPct()
		lbl := fmt.Sprintf("%-13s", label+":")
		if highlight {
			lbl = styleAccent.Render(lbl)
		} else {
			lbl = styleMuted.Render(lbl)
		}
		resetStr := ""
		if w.ResetsAt > 0 {
			resetStr = styleMuted.Render(fmt.Sprintf(" (resets %s)", formatResetTime(w.ResetsAt, now)))
		}
		return fmt.Sprintf("  %s %s %s left%s", lbl, bar, formatPct(rem, 0), resetStr)
	}

	if q.Primary != nil {
		out = append(out, formatRow("5h limit", q.Primary, false))
	}
	if q.Secondary != nil {
		out = append(out, formatRow("Weekly limit", q.Secondary, false))
	}
	return out
}

func (m Model) renderActiveBlockCard(blk usage.ActiveBlock, now time.Time) []string {
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true).Render(blk.Provider) +
		" " + styleMuted.Render("(local 5h)")
	bar := renderCodexQuotaBar(blk.UsedPct, 20)
	rem := 100.0 - blk.UsedPct
	if rem < 0 {
		rem = 0
	}
	resetStr := ""
	if blk.ResetsAt > 0 {
		resetStr = styleMuted.Render(fmt.Sprintf(" (resets %s)", formatResetTime(blk.ResetsAt, now)))
	}
	return []string{
		"  " + title,
		fmt.Sprintf("  %-13s %s %s left%s", styleMuted.Render("5h block:"), bar, formatPct(rem, 0), resetStr),
	}
}

func renderPlanStatusBadge(status usage.PlanStatus) string {
	switch status {
	case usage.PlanStatusUnder:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Bold(true).Render("[Under]")
	case usage.PlanStatusNear:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Bold(true).Render("[Near]")
	case usage.PlanStatusOver:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRed)).Bold(true).Render("[Over]")
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Tab 3 — Models
// ---------------------------------------------------------------------------

func (m Model) renderUsageModelsTab(width int) []string {
	models := m.usageFilteredModels()
	if len(models) == 0 {
		return []string{styleMuted.Render("No model usage recorded.")}
	}

	provW := 12
	// Fixed columns: 2 (indent) + provW (12) + 8 + 8 + 8 + 9 + 6 + 6 spaces + 1 right margin = 60
	modelW := width - 60
	if modelW < 16 {
		modelW = 16
	}
	if modelW > 80 {
		modelW = 80
	}
	hdr := fmt.Sprintf("  %-*s %-*s %8s %8s %8s %9s %6s",
		modelW, "Model", provW, "Provider", "Tokens", "CacheHit", "$/1M", "Cost", "Share")
	lines := []string{
		fitCellWidth(styleMuted.Render(hdr), width),
		fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width),
	}

	for i, ms := range models {
		row := fmt.Sprintf("  %-*s %-*s %8s %8s %8s %9s %6s",
			modelW,
			truncate(ms.Model, modelW),
			provW,
			truncate(ms.Provider, provW),
			formatCompactTokens(ms.Totals.Total()),
			formatPct(ms.CacheHitRate*100, 1),
			formatUSD(ms.CostPerMTokens),
			formatUSD(ms.CostUSD),
			formatPct(ms.CostShare*100, 1),
		)
		if i == m.usageRowSel {
			lines = append(lines, fitCellWidthWithBG(styleSelection.Render(row), width, ColorSurface0))
		} else {
			lines = append(lines, fitCellWidth(row, width))
		}

		if i == m.usageRowSel && m.usageExpanded {
			detail := fmt.Sprintf("    Input %s · Cache Read %s · Cache Write %s · Output %s · Savings %s",
				formatUSD(ms.CategoryCost.Input),
				formatUSD(ms.CategoryCost.CacheRead),
				formatUSD(ms.CategoryCost.CacheWrite),
				formatUSD(ms.CategoryCost.Output),
				formatUSD(ms.CacheSavingsUSD),
			)
			lines = append(lines, fitCellWidth(styleMuted.Render(detail), width))
		}
	}
	return lines
}

// ---------------------------------------------------------------------------
// Tab 4 — Sessions
// ---------------------------------------------------------------------------

func (m Model) renderUsageSessionsTab(width, bodyH int) []string {
	sessions := m.usageFilteredSessions()
	if len(sessions) == 0 {
		return []string{styleMuted.Render("No sessions recorded.")}
	}

	now := m.opts.NowOrDefault()
	provW := 12
	// Fixed columns: 2 (indent) + provW (12) + 8 (Tokens) + 9 (Cost) + 10 (When) + 4 spaces + 1 right margin = 46
	nameW := width - 46
	if nameW < 18 {
		nameW = 18
	}
	if nameW > 96 {
		nameW = 96
	}
	hdr := fmt.Sprintf("  %-*s %-*s %8s %9s %10s",
		nameW, "Session / Project", provW, "Provider", "Tokens", "Cost", "When")
	lines := []string{
		fitCellWidth(styleMuted.Render(hdr), width),
		fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width),
	}

	visRows := bodyH - 4
	if visRows < 2 {
		visRows = 2
	}
	start := windowStart(m.usageRowSel, len(sessions), visRows)
	end := start + visRows
	if end > len(sessions) {
		end = len(sessions)
	}

	for i := start; i < end; i++ {
		s := sessions[i]
		label := s.Project
		if label == "" {
			label = s.SessionID
		}
		when := "—"
		if s.UpdatedAtMs > 0 {
			ageMs := now.UnixMilli() - s.UpdatedAtMs
			if ageMs < 0 {
				ageMs = 0
			}
			when = formatDurationShort(ageMs) + " ago"
		}
		row := fmt.Sprintf("  %-*s %-*s %8s %9s %10s",
			nameW,
			truncate(label, nameW),
			provW,
			truncate(s.Provider, provW),
			formatCompactTokens(s.Totals.Total()),
			formatUSD(s.CostUSD),
			when,
		)
		if i == m.usageRowSel {
			lines = append(lines, fitCellWidthWithBG(styleSelection.Render(row), width, ColorSurface0))
		} else {
			lines = append(lines, fitCellWidth(row, width))
		}
		if i == m.usageRowSel && m.usageExpanded {
			modelsStr := strings.Join(s.Models, ", ")
			if modelsStr == "" {
				modelsStr = "—"
			}
			detail := fmt.Sprintf("    Models: %s · In %s / Cache %s / Out %s · Dur %s",
				modelsStr,
				formatCompactTokens(s.Totals.UncachedInputTokens),
				formatCompactTokens(s.Totals.CachedInputTokens),
				formatCompactTokens(s.Totals.OutputTokens),
				formatDurationShort(s.DurationMs),
			)
			lines = append(lines, fitCellWidth(styleMuted.Render(detail), width))
		}
	}

	lines = append(lines, fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width))
	footer := fmt.Sprintf("  Showing %d–%d of %d sessions", start+1, end, len(sessions))
	lines = append(lines, fitCellWidth(styleMuted.Render(footer), width))
	return lines
}

// ---------------------------------------------------------------------------
// Dashboard Grid (z toggle)
// ---------------------------------------------------------------------------

func (m Model) renderUsageDashboard(width, bodyH int) []string {
	twoCol := width >= 104
	cardTitles := [4]string{"1 Overview", "2 Plan", "3 Models", "4 Sessions"}

	cardW := width
	cardH := (bodyH / 4)
	if twoCol {
		cardW = (width - 2) / 2
		cardH = bodyH / 2
	}
	if cardH < 5 {
		cardH = 5
	}
	innerCardW := cardW - 4
	if innerCardW < 10 {
		innerCardW = 10
	}
	innerCardH := cardH - 2
	if innerCardH < 2 {
		innerCardH = 2
	}

	buildCard := func(idx int, content []string) []string {
		focused := m.usageDashFocus == idx
		borderColor := ColorSurface1
		prefix := "  "
		titleSt := styleMuted
		if focused {
			borderColor = ColorLavender
			prefix = "▸ "
			titleSt = styleAccent
		}
		bSt := lipgloss.NewStyle().Foreground(lipgloss.Color(borderColor))

		title := prefix + cardTitles[idx]
		topFill := cardW - 4 - ansi.StringWidth(title)
		if topFill < 1 {
			topFill = 1
		}
		top := bSt.Render("╭─ ") + titleSt.Render(title) + bSt.Render(" "+strings.Repeat("─", topFill)+"╮")
		bot := bSt.Render("╰" + strings.Repeat("─", maxInt(1, cardW-2)) + "╯")

		out := make([]string, 0, cardH)
		out = append(out, fitCellWidth(top, cardW))
		for r := 0; r < innerCardH; r++ {
			line := ""
			if r < len(content) {
				line = content[r]
			}
			row := bSt.Render("│") + " " + fitCellWidth(line, innerCardW) + " " + bSt.Render("│")
			out = append(out, fitCellWidth(row, cardW))
		}
		out = append(out, fitCellWidth(bot, cardW))
		return out
	}

	// Build concise content for each of the 4 cards.
	ov := m.renderUsageOverview(innerCardW, innerCardH)
	pl := m.renderUsagePlanDashboardCard(innerCardW)
	mo := m.renderUsageModelsTab(innerCardW)
	se := m.renderUsageSessionsTab(innerCardW, innerCardH)

	c0 := buildCard(0, ov)
	c1 := buildCard(1, pl)
	c2 := buildCard(2, mo)
	c3 := buildCard(3, se)

	var lines []string
	if twoCol {
		for r := 0; r < len(c0); r++ {
			lines = append(lines, fitCellWidth(c0[r]+"  "+c1[r], width))
		}
		for r := 0; r < len(c2); r++ {
			lines = append(lines, fitCellWidth(c2[r]+"  "+c3[r], width))
		}
	} else {
		lines = append(lines, c0...)
		lines = append(lines, c1...)
		lines = append(lines, c2...)
		lines = append(lines, c3...)
	}
	return sliceUsageBody(lines, 0, bodyH, width)
}

// ---------------------------------------------------------------------------
// Help Overlay (? toggle)
// ---------------------------------------------------------------------------

func (m Model) renderUsageHelpDialog(width int) string {
	maxW := width - 4
	if maxW > 64 {
		maxW = 64
	}
	if maxW < 28 {
		maxW = 28
	}
	rows := [][2]string{
		{"1–4 / Tab", "Switch report tab"},
		{"z", "Toggle 2×2 dashboard grid"},
		{"r", "Cycle range (7d / 30d / 90d / all)"},
		{"p", "Filter by provider"},
		{"g", "Cycle activity mode (Daily / Weekly / Cum)"},
		{"b", "Cycle daily breakdown grouping (Overview)"},
		{"c", "Toggle Cost / Tokens (Overview)"},
		{"s", "Cycle session sort (Cost / Tokens / Recent)"},
		{"← / →", "Select day on daily usage chart (Overview)"},
		{"↑ / ↓", "Move selection or scroll"},
		{"Enter", "Expand details / zoom dashboard card"},
		{"R / Ctrl-r", "Force refresh usage data"},
		{"?", "Toggle this help dialog"},
		{"Esc / q", "Back / close popup"},
	}
	var b strings.Builder
	b.WriteString(styleAccent.Render("Usage Shortcuts"))
	b.WriteString("\n\n")
	for _, kv := range rows {
		b.WriteString(fmt.Sprintf("%-13s %s\n", styleAccent.Render(kv[0]), styleSubtext.Render(kv[1])))
	}
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("Press ? or Esc to close"))
	return styleDialogBox.Width(maxW).Render(b.String())
}

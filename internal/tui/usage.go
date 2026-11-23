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

var usageTabNames = []string{"Overview", "Usage", "Plan", "Models", "Sessions"}

const (
	usageTabOverview = 0
	usageTabUsage    = 1
	usageTabPlan     = 2
	usageTabModels   = 3
	usageTabSessions = 4
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

func (m *Model) clampUsageSelection() {
	days := m.usageFilteredDaily()
	if len(days) == 0 {
		m.usageDayIdx = 0
	} else if m.usageDayIdx < 0 || m.usageDayIdx >= len(days) {
		m.usageDayIdx = len(days) - 1
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
		if m.usageTab != usageTabOverview {
			m.usageExpanded = !m.usageExpanded
		}
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
	}

	switch r {
	case "1", "2", "3", "4", "5":
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
		switch m.usageTab {
		case usageTabOverview:
			m.usageOverviewAgg = m.usageOverviewAgg.next()
		case usageTabUsage:
			m.usageGroupBy = m.usageGroupBy.next()
		}
		return m, nil
	case "c":
		if m.usageTab == usageTabUsage {
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
	case usageTabUsage:
		days := m.usageFilteredDaily()
		if len(days) == 0 {
			return
		}
		m.usageDayIdx += delta
		if m.usageDayIdx < 0 {
			m.usageDayIdx = 0
		}
		if m.usageDayIdx >= len(days) {
			m.usageDayIdx = len(days) - 1
		}
	case usageTabPlan:
		m.usagePlanWindow = (m.usagePlanWindow + 1) % 2
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
	case usageTabPlan, usageTabModels, usageTabSessions:
		m.usageRowSel += delta
		m.clampUsageSelection()
	default:
		m.usageScroll += delta
		if m.usageScroll < 0 {
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

	// Zone 1: Title (1 row)
	titleLine := m.renderUsageTitle(innerW)
	// Zone 2: Filled Tab Bar (1 row)
	tabLine := renderUsageTabBar(usageTabNames, m.usageTab, innerW)
	// Zone 3: Horizontal Rule (1 row)
	ruleLine := styleDivider.Render(strings.Repeat("─", innerW))
	// Zone 4: Controls Bar (1-2 rows)
	ctrlLines := m.renderUsageControls(innerW)
	// Zone 6: Footer Hints (2 fixed rows)
	foot1, foot2 := m.renderUsageFooter(innerW)

	chromeH := 1 + 1 + 1 + len(ctrlLines) + 2
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
			raw = m.renderUsageOverview(innerW)
		case usageTabUsage:
			raw = m.renderUsageDailyTab(innerW, bodyH)
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
	out.WriteString(inset(titleLine))
	out.WriteByte('\n')
	out.WriteString(inset(tabLine))
	out.WriteByte('\n')
	out.WriteString(inset(ruleLine))
	out.WriteByte('\n')
	for _, cl := range ctrlLines {
		out.WriteString(inset(cl))
		out.WriteByte('\n')
	}
	for i := 0; i < bodyH; i++ {
		if i < len(bodyLines) {
			out.WriteString(inset(bodyLines[i]))
		} else {
			out.WriteString(fitCellWidth("", width))
		}
		out.WriteByte('\n')
	}
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

func (m Model) renderUsageTitle(width int) string {
	boldText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	left := boldText.Render("Usage · " + formatUSD(m.usageReport.CostUSD))
	meta := fmt.Sprintf(" · %d providers · %d sessions", len(m.usageReport.Providers), m.usageReport.Sessions)
	left += styleMuted.Render(meta)

	var right string
	if m.usageLoading && !m.usageLoaded {
		right = styleAccent.Render("Loading…")
	} else if m.usageRefreshing && m.usageLoaded {
		right = styleAccent.Render("Syncing…")
	} else if m.usageReport.GeneratedAtMs > 0 {
		t := time.UnixMilli(m.usageReport.GeneratedAtMs).In(m.opts.NowOrDefault().Location())
		right = styleMuted.Render("Updated · " + t.Format("15:04"))
	} else {
		right = styleMuted.Render("Updated · —")
	}

	leftW := ansi.StringWidth(left)
	rightW := ansi.StringWidth(right)
	if leftW+rightW+2 <= width {
		return left + strings.Repeat(" ", width-leftW-rightW) + right
	}
	return fitCellWidth(left, width)
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
		case usageTabUsage:
			groups := []usageGroupBy{usageGroupProvider, usageGroupModel, usageGroupProject, usageGroupTokenType}
			parts := make([]string, len(groups))
			for i, g := range groups {
				if g == m.usageGroupBy {
					parts[i] = styleAccent.Render(g.label())
				} else {
					parts[i] = styleMuted.Render(g.label())
				}
			}
			segs = append(segs, keySt.Render("g ")+strings.Join(parts, styleMuted.Render("/")))
			var costPart, tokPart string
			if m.usageUseCost {
				costPart = styleAccent.Render("Cost")
				tokPart = styleMuted.Render("Tokens")
			} else {
				costPart = styleMuted.Render("Cost")
				tokPart = styleAccent.Render("Tokens")
			}
			segs = append(segs, keySt.Render("c ")+costPart+styleMuted.Render("/")+tokPart)
		case usageTabPlan:
			w5 := styleMuted.Render("5h")
			ww := styleMuted.Render("Weekly")
			if m.usagePlanWindow == 0 {
				w5 = styleAccent.Render("5h")
			} else {
				ww = styleAccent.Render("Weekly")
			}
			segs = append(segs, keySt.Render("←/→ ")+w5+styleMuted.Render(" / ")+ww)
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
				{"r", "range"},
				{"g", "daily/weekly/cum"},
				{"esc", "close"},
			}
		case usageTabUsage:
			ctxPairs = [][2]string{
				{"←/→", "day"},
				{"enter", "details"},
				{"r", "range"},
				{"g", "group"},
				{"c", "cost/tokens"},
				{"esc", "back"},
			}
		case usageTabPlan:
			ctxPairs = [][2]string{
				{"←/→", "5h/weekly"},
				{"↑/↓", "5h period"},
				{"enter", "details"},
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
		{"tab/1-5", "report"},
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

func (m Model) renderUsageOverview(width int) []string {
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
	type kpi struct {
		label string
		value string
		color string
	}
	kpis := []kpi{
		{"Total spend", formatUSD(totalSpend), ColorGreen},
		{"Total tokens", formatCompactTokens(totalTokens), ColorBlue},
		{"Cache hit", formatPct(r.CacheHitRate*100, 1), ColorTeal},
		{"Current streak", fmt.Sprintf("%dd", r.CurrentStreak), ColorLavender},
		{"30d projected", formatUSD(r.Burndown.Projection.Projected30dUSD), ColorYellow},
	}
	cols := 1
	switch {
	case width >= 90:
		cols = 5
	case width >= 54:
		cols = 3
	case width >= 36:
		cols = 2
	}
	colW := width / cols
	if colW < 12 {
		colW = 12
	}
	for start := 0; start < len(kpis); start += cols {
		end := start + cols
		if end > len(kpis) {
			end = len(kpis)
		}
		var lblRow, valRow strings.Builder
		for i := start; i < end; i++ {
			k := kpis[i]
			valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(k.color)).Bold(true)
			lblRow.WriteString(fitCellWidth(styleMuted.Render(k.label), colW))
			valRow.WriteString(fitCellWidth(valSt.Render(k.value), colW))
		}
		lines = append(lines, fitCellWidth(lblRow.String(), width))
		lines = append(lines, fitCellWidth(valRow.String(), width))
	}
	lines = append(lines, "")

	// 52-week Token activity chart
	secTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	lines = append(lines, fitCellWidth(
		secTitle.Render("Token activity")+" "+styleMuted.Render("Last 12 months · "+m.usageOverviewAgg.label()),
		width,
	))
	heatLines := renderActivityHeatmap(r.Daily, m.usageOverviewAgg, width, m.opts.NowOrDefault())
	lines = append(lines, heatLines...)
	lines = append(lines, "")

	// Bottom 2-column section (>=76 cols, stacked below)
	leftCol := m.renderOverviewInsights(r)
	rightCol := m.renderOverviewTopProvidersTools(r)

	if width >= 76 {
		halfW := (width - 2) / 2
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
			lines = append(lines, fitCellWidth(fitCellWidth(l, halfW)+"  "+fitCellWidth(rr, width-halfW-2), width))
		}
	} else {
		lines = append(lines, leftCol...)
		lines = append(lines, "")
		lines = append(lines, rightCol...)
	}

	return lines
}

func (m Model) renderOverviewInsights(r usage.Report) []string {
	titleSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
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
	out := []string{titleSt.Render("Activity insights")}
	for _, kv := range rows {
		out = append(out, fmt.Sprintf("%-18s %s", styleMuted.Render(kv[0]), valSt.Render(kv[1])))
	}
	return out
}

func (m Model) renderOverviewTopProvidersTools(r usage.Report) []string {
	titleSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	out := []string{titleSt.Render("Top providers & tools")}

	provLimit := len(r.Providers)
	if provLimit > 4 {
		provLimit = 4
	}
	for i := 0; i < provLimit; i++ {
		p := r.Providers[i]
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(usageSeriesPalette[i%len(usageSeriesPalette)])).Render("●")
		out = append(out, fmt.Sprintf("%s %-14s %s %s",
			dot,
			truncate(p.Provider, 14),
			valSt.Render(formatUSD(p.CostUSD)),
			styleMuted.Render(formatPct(p.CostShare*100, 0)),
		))
	}
	if len(r.Tools) > 0 {
		out = append(out, styleMuted.Render("Top tools:"))
		toolLimit := len(r.Tools)
		if toolLimit > 4 {
			toolLimit = 4
		}
		for i := 0; i < toolLimit; i++ {
			t := r.Tools[i]
			out = append(out, fmt.Sprintf("  %-16s %s", truncate(t.Name, 16), styleMuted.Render(fmt.Sprintf("%d runs", t.Count))))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tab 1 — Usage (Stacked Daily Bar Chart + Breakdown)
// ---------------------------------------------------------------------------

func (m Model) renderUsageDailyTab(width, bodyH int) []string {
	days := m.usageFilteredDaily()
	if len(days) == 0 {
		return []string{styleMuted.Render("No daily usage data in selected range.")}
	}
	selIdx := m.usageDayIdx
	if selIdx < 0 || selIdx >= len(days) {
		selIdx = len(days) - 1
	}

	var rangeCost float64
	var rangeTok int64
	for _, d := range days {
		rangeCost += d.CostUSD
		rangeTok += d.Totals.Total()
	}

	boldText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	dateRange := shortDateLabel(days[0].Date)
	if len(days) > 1 {
		dateRange += " – " + shortDateLabel(days[len(days)-1].Date)
	}
	headline := boldText.Render(fmt.Sprintf("%s · %s tokens", formatUSD(rangeCost), formatCompactTokens(rangeTok))) +
		"  " + styleMuted.Render(dateRange)

	sideBySide := width >= 130
	chartW := width
	chartH := bodyH - 9
	if sideBySide {
		chartW = width - 46
		chartH = bodyH - 3
	}
	if chartH < 8 {
		chartH = 8
	}
	if chartH > 16 {
		chartH = 16
	}

	chartLines, series := renderStackedBarChart(days, selIdx, m.usageGroupBy, m.usageUseCost, chartW, chartH)
	breakdownLines := m.renderUsageDayBreakdown(days[selIdx], series, 44)

	var lines []string
	lines = append(lines, fitCellWidth(headline, width))
	lines = append(lines, "")

	if sideBySide {
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
			lines = append(lines, fitCellWidth(fitCellWidth(l, chartW)+"  "+fitCellWidth(rr, 44), width))
		}
	} else {
		lines = append(lines, chartLines...)
		lines = append(lines, "")
		lines = append(lines, breakdownLines...)
	}

	return lines
}

func (m Model) renderUsageDayBreakdown(day usage.DailyBucket, series []usageSeriesItem, width int) []string {
	boldText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	header := boldText.Render(shortDateLabel(day.Date)) +
		styleMuted.Render(fmt.Sprintf(" · %s · %s tokens", formatUSD(day.CostUSD), formatCompactTokens(day.Totals.Total())))
	lines := []string{fitCellWidth(header, width)}

	for _, s := range series {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(s.Color)).Render("●")
		var val string
		if m.usageUseCost {
			val = formatUSD(s.CostUSD)
		} else {
			val = formatCompactTokens(s.Tokens)
		}
		row := fmt.Sprintf("%s %-16s %5s  %s", dot, truncate(s.Label, 16), styleMuted.Render(formatPct(s.Share, 0)), val)
		lines = append(lines, fitCellWidth(row, width))
	}

	if m.usageExpanded {
		lines = append(lines, styleDivider.Render(strings.Repeat("─", minInt(width, 38))))
		lines = append(lines, styleMuted.Render(fmt.Sprintf("Input: %s · Cache Read: %s",
			formatCompactTokens(day.Totals.UncachedInputTokens),
			formatCompactTokens(day.Totals.CachedInputTokens),
		)))
		lines = append(lines, styleMuted.Render(fmt.Sprintf("Cache Write: %s · Output: %s",
			formatCompactTokens(day.Totals.CacheCreationTokens),
			formatCompactTokens(day.Totals.OutputTokens),
		)))
		lines = append(lines, styleMuted.Render(fmt.Sprintf("Records: %d · Sessions: %d · Saved: %s",
			day.Records, day.Sessions, formatUSD(day.CacheSavingsUSD),
		)))
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

	secTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
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
		lines = append(lines, alignHeaderRight(secTitle.Render("Plan & Rate Limits"), statusRight, width))

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
			subHdr := "  " + provHeaderSt.Render(limitProviderDisplayName(prov)) +
				" " + styleMuted.Render(fmt.Sprintf("(%d %s)", len(group), unit))
			lines = append(lines, fitCellWidth(subHdr, width))

			if width >= 104 && len(group) >= 2 {
				halfW := (width - 2) / 2
				for i := 0; i < len(group); i += 2 {
					left := m.renderLimitAccountCard(group[i], now, halfW)
					var right []string
					if i+1 < len(group) {
						right = m.renderLimitAccountCard(group[i+1], now, width-halfW-2)
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
						lines = append(lines, fitCellWidth(fitCellWidth(l, halfW)+"  "+fitCellWidth(rr, width-halfW-2), width))
					}
					lines = append(lines, "")
				}
			} else {
				for _, acc := range group {
					lines = append(lines, m.renderLimitAccountCard(acc, now, width)...)
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
			lines = append(lines, alignHeaderRight(secTitle.Render("Live Rate-Limit Windows"), statusRight, width))
			if width >= 100 && len(cards) >= 2 {
				halfW := (width - 2) / 2
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
						lines = append(lines, fitCellWidth(fitCellWidth(l, halfW)+"  "+fitCellWidth(rr, width-halfW-2), width))
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
			lines = append(lines, alignHeaderRight(secTitle.Render("Plan & Rate Limits"), statusRight, width))
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
	lines = append(lines, fitCellWidth(secTitle.Render("Monthly Burndown & Projection"), width))
	period := "Current period"
	if proj.PeriodStart != "" && proj.PeriodEnd != "" {
		period = proj.PeriodStart + " – " + proj.PeriodEnd
	}
	lines = append(lines, fitCellWidth(fmt.Sprintf("  %-18s %s", styleMuted.Render("Billing period"), period), width))
	lines = append(lines, fitCellWidth(fmt.Sprintf("  %-18s %s   %-16s %s/day",
		styleMuted.Render("Spent"), formatUSD(proj.SpentUSD),
		styleMuted.Render("7d median burn"), formatUSD(proj.MedianDailyUSD),
	), width))
	lines = append(lines, fitCellWidth(fmt.Sprintf("  %-18s %s   %-16s %s",
		styleMuted.Render("30d projected"), formatUSD(proj.Projected30dUSD),
		styleMuted.Render("Cache savings"), formatUSD(m.usageReport.CacheSavingsUSD),
	), width))

	if proj.Plan.MonthlyUSD > 0 {
		badge := renderPlanStatusBadge(proj.Status)
		bar := renderCodexQuotaBar(proj.PercentUsed, 20)
		lines = append(lines, fitCellWidth(fmt.Sprintf("  %-18s %s %s of %s %s",
			styleMuted.Render("Budget"), bar, formatPct(proj.PercentUsed, 0), formatUSD(proj.Plan.MonthlyUSD), badge,
		), width))
	}
	lines = append(lines, "")

	// 3. Recent 5-hour Periods
	lines = append(lines, fitCellWidth(secTitle.Render("Recent 5-hour Periods"), width))
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
		row := fmt.Sprintf("%s%s %-18s %8s  %8s  %s",
			marker,
			curr,
			truncate(b.Label, 18),
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
				lines = append(lines, fitCellWidth(fmt.Sprintf("      provider %-14s %8s  %s",
					truncate(k, 14), formatCompactTokens(pm.Tokens), formatUSD(pm.CostUSD)), width))
			}
			for k, mm := range b.ByModel {
				lines = append(lines, fitCellWidth(fmt.Sprintf("      model    %-14s %8s  %s",
					truncate(k, 14), formatCompactTokens(mm.Tokens), formatUSD(mm.CostUSD)), width))
			}
		}
	}
	return lines
}

func (m Model) renderLimitAccountCard(acc usage.LimitAccount, now time.Time, cardW int) []string {
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

	hdrParts := []string{profSt.Render(profileLabel)}
	if acc.Plan != "" {
		hdrParts = append(hdrParts, planSt.Render(acc.Plan))
	}
	if acc.Stale {
		hdrParts = append(hdrParts, staleSt.Render("stale"))
	}
	hdr := "    " + strings.Join(hdrParts, " ")
	if acc.Email != "" {
		remW := cardW - ansi.StringWidth(hdr) - 1
		if remW >= 8 {
			hdr += " " + styleMuted.Render(truncate(acc.Email, remW))
		}
	}
	out := []string{fitCellWidth(hdr, cardW)}

	// Reset credits highlight line
	if acc.ResetCredits != nil && acc.ResetCredits.AvailableCount > 0 {
		noun := "reset credits"
		if acc.ResetCredits.AvailableCount == 1 {
			noun = "reset credit"
		}
		creditLine := fmt.Sprintf("    %s", creditSt.Render(fmt.Sprintf("✦ %d %s available", acc.ResetCredits.AvailableCount, noun)))
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
		out = append(out, fitCellWidth("    "+styleMuted.Render(msg), cardW))
		return out
	}

	// Responsive window row column widths
	labelW := 18
	barSegs := 20
	compact := false
	switch {
	case cardW < 58:
		labelW = 12
		barSegs = 12
		compact = true
	case cardW < 86:
		labelW = 14
		barSegs = 20
		compact = true
	}

	for _, w := range acc.Windows {
		lblText := strings.TrimSpace(w.Label)
		if lblText == "" {
			lblText = w.Kind
		}
		if lblText == "" {
			lblText = w.ID
		}
		paddedLbl := fmt.Sprintf("%-*s", labelW, truncate(lblText, labelW))
		highlight := (m.usagePlanWindow == 0 && w.Kind == agent.LimitWindowSession) ||
			(m.usagePlanWindow == 1 && w.Kind == agent.LimitWindowWeekly)
		if highlight {
			paddedLbl = styleAccent.Render(paddedLbl)
		} else {
			paddedLbl = styleMuted.Render(paddedLbl)
		}

		bar := renderCodexQuotaBar(w.UsedPercent, barSegs)
		rem := 100.0 - w.UsedPercent
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}

		row := fmt.Sprintf("    %s %s %4s left", paddedLbl, bar, formatPct(rem, 0))
		if pace, ok := computeLimitPace(w, now); ok {
			row += " " + renderLimitPaceTag(pace, w.UsedPercent)
		}
		if resetStr := formatLimitResetCountdown(w.ResetsAt, now, compact); resetStr != "" {
			row += " " + styleMuted.Render(resetStr)
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
		out = append(out, formatRow("5h limit", q.Primary, m.usagePlanWindow == 0))
	}
	if q.Secondary != nil {
		out = append(out, formatRow("Weekly limit", q.Secondary, m.usagePlanWindow == 1))
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

	modelW := 24
	if width < 80 {
		modelW = 16
	}
	hdr := fmt.Sprintf("  %-*s %-10s %8s %8s %8s %9s %6s",
		modelW, "Model", "Provider", "Tokens", "CacheHit", "$/1M", "Cost", "Share")
	lines := []string{
		fitCellWidth(styleMuted.Render(hdr), width),
		fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width),
	}

	for i, ms := range models {
		row := fmt.Sprintf("  %-*s %-10s %8s %8s %8s %9s %6s",
			modelW,
			truncate(ms.Model, modelW),
			truncate(ms.Provider, 10),
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
	nameW := 28
	if width < 80 {
		nameW = 18
	}
	hdr := fmt.Sprintf("  %-*s %-10s %8s %9s %8s",
		nameW, "Session / Project", "Provider", "Tokens", "Cost", "When")
	lines := []string{
		fitCellWidth(styleMuted.Render(hdr), width),
		fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width),
	}

	visRows := bodyH - 3
	if visRows < 3 {
		visRows = 3
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
		row := fmt.Sprintf("  %-*s %-10s %8s %9s %8s",
			nameW,
			truncate(label, nameW),
			truncate(s.Provider, 10),
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

	footer := fmt.Sprintf("  Showing %d–%d of %d sessions", start+1, end, len(sessions))
	lines = append(lines, fitCellWidth(styleMuted.Render(footer), width))
	return lines
}

// ---------------------------------------------------------------------------
// Dashboard Grid (z toggle)
// ---------------------------------------------------------------------------

func (m Model) renderUsageDashboard(width, bodyH int) []string {
	twoCol := width >= 104
	cardTitles := [4]string{"1 Overview", "2 Usage", "3 Plan", "4 Models"}

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
	ov := m.renderUsageOverview(innerCardW)
	us := m.renderUsageDailyTab(innerCardW, innerCardH)
	pl := m.renderUsagePlanDashboardCard(innerCardW)
	mo := m.renderUsageModelsTab(innerCardW)

	c0 := buildCard(0, ov)
	c1 := buildCard(1, us)
	c2 := buildCard(2, pl)
	c3 := buildCard(3, mo)

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
		{"1–5 / Tab", "Switch report tab"},
		{"z", "Toggle 2×2 dashboard grid"},
		{"r", "Cycle range (7d / 30d / 90d / all)"},
		{"p", "Filter by provider"},
		{"g", "Cycle overview mode or Usage grouping"},
		{"c", "Toggle Cost / Tokens (Usage tab)"},
		{"s", "Cycle session sort (Cost / Tokens / Recent)"},
		{"← / →", "Select day (Usage) or 5h/Weekly (Plan)"},
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

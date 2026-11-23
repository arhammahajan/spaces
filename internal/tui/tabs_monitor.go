package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/model"
)

const (
	monitorDesktopWidth     = 100
	monitorQuotaStaleS      = 600
	monitorClaudeLastKnownS = 86400
)

type monitorSortMode int

const (
	monitorSortDefault monitorSortMode = iota
	monitorSortContext
	monitorSortTokens
	monitorSortMemory
	monitorSortCost
)

func (s monitorSortMode) next() monitorSortMode {
	return (s + 1) % 5
}

func (s monitorSortMode) label() string {
	switch s {
	case monitorSortContext:
		return "context"
	case monitorSortTokens:
		return "tokens"
	case monitorSortMemory:
		return "memory"
	case monitorSortCost:
		return "cost"
	default:
		return "default"
	}
}

func sessionMatchesMonitorFilter(s model.SessionRow, queryLower string) bool {
	if queryLower == "" {
		return true
	}
	for _, field := range []string{
		s.Project,
		s.Agent,
		s.Model,
		s.Summary,
		s.CurrentTask,
		s.SessionID,
		s.Profile,
	} {
		if field != "" && strings.Contains(strings.ToLower(field), queryLower) {
			return true
		}
	}
	return false
}

func (m Model) monitorSessions() []model.SessionRow {
	src := m.telemetry.Sessions
	if len(src) == 0 {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(m.tabsMonitorFilter))
	var out []model.SessionRow
	if q == "" {
		if m.tabsMonitorSort == monitorSortDefault {
			return src
		}
		out = make([]model.SessionRow, len(src))
		copy(out, src)
	} else {
		out = make([]model.SessionRow, 0, len(src))
		for _, s := range src {
			if sessionMatchesMonitorFilter(s, q) {
				out = append(out, s)
			}
		}
	}

	switch m.tabsMonitorSort {
	case monitorSortContext:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].ContextPct > out[j].ContextPct
		})
	case monitorSortTokens:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].TotalTokens > out[j].TotalTokens
		})
	case monitorSortMemory:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].MemKB > out[j].MemKB
		})
	case monitorSortCost:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Usage.CostUSD > out[j].Usage.CostUSD
		})
	}
	return out
}

func (m *Model) clampMonitorSelection() {
	n := len(m.monitorSessions())
	if n == 0 {
		m.tabsMonitorSelected = 0
		return
	}
	if m.tabsMonitorSelected < 0 {
		m.tabsMonitorSelected = 0
	}
	if m.tabsMonitorSelected >= n {
		m.tabsMonitorSelected = n - 1
	}
}

func (m Model) monitorPanelVisible(panel int) bool {
	if m.tabsMonitorHidden == nil {
		return true
	}
	return !m.tabsMonitorHidden[panel]
}

func (m *Model) toggleMonitorPanel(panel int) {
	if m.tabsMonitorHidden == nil {
		m.tabsMonitorHidden = map[int]bool{}
	}
	m.tabsMonitorHidden[panel] = !m.tabsMonitorHidden[panel]
}

func (m *Model) moveMonitorSelection(delta int) {
	n := len(m.monitorSessions())
	if n == 0 {
		m.tabsMonitorSelected = 0
		return
	}
	m.tabsMonitorSelected += delta
	if m.tabsMonitorSelected < 0 {
		m.tabsMonitorSelected = 0
	}
	if m.tabsMonitorSelected >= n {
		m.tabsMonitorSelected = n - 1
	}
}

func (m *Model) activateMonitorSession() {
	sessions := m.monitorSessions()
	if m.tabsMonitorSelected < 0 || m.tabsMonitorSelected >= len(sessions) {
		return
	}
	sess := sessions[m.tabsMonitorSelected]
	spaceName := strings.TrimSpace(sess.Space)
	winID := strings.TrimSpace(sess.WindowID)
	paneID := strings.TrimSpace(sess.PaneID)

	// Fall back to resolving Space/WindowID from loaded spaces by PaneID.
	if (spaceName == "" || winID == "") && paneID != "" {
		for _, sp := range m.spaces {
			for _, p := range sp.Panes {
				if p.ID == paneID {
					if spaceName == "" {
						spaceName = sp.Name
					}
					if winID == "" {
						winID = p.WindowID
					}
					break
				}
			}
		}
	}
	if spaceName == "" && paneID == "" {
		m.toast = "Session is not attached to a tmux pane."
		return
	}
	if err := m.backend.FocusPane(context.Background(), spaceName, winID, paneID); err != nil {
		m.toast = err.Error()
		return
	}
	m.quitting = true
}

func (m Model) handleTabsMonitor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.tabsMonitorFiltering {
		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEsc:
			m.tabsMonitorFilter = ""
			m.tabsMonitorFiltering = false
			m.tabsMonitorSelected = 0
			m.clampMonitorSelection()
			return m, nil
		case tea.KeyEnter:
			m.tabsMonitorFiltering = false
			m.clampMonitorSelection()
			return m, nil
		case tea.KeyBackspace:
			runes := []rune(m.tabsMonitorFilter)
			if len(runes) > 0 {
				m.tabsMonitorFilter = string(runes[:len(runes)-1])
				m.tabsMonitorSelected = 0
				m.clampMonitorSelection()
			}
			return m, nil
		case tea.KeySpace:
			m.tabsMonitorFilter += " "
			m.tabsMonitorSelected = 0
			m.clampMonitorSelection()
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP:
			m.moveMonitorSelection(-1)
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.moveMonitorSelection(1)
			return m, nil
		case tea.KeyRunes:
			if len(msg.Runes) > 0 {
				m.tabsMonitorFilter += string(msg.Runes)
				m.tabsMonitorSelected = 0
				m.clampMonitorSelection()
			}
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.tabsMonitorFilter != "" {
			m.tabsMonitorFilter = ""
			m.tabsMonitorSelected = 0
			m.clampMonitorSelection()
			return m, nil
		}
		m.tabsMonitorMode = false
		return m, nil
	case tea.KeyEnter:
		m.activateMonitorSession()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveMonitorSelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveMonitorSelection(1)
		return m, nil
	case tea.KeyLeft:
		if m.width < monitorDesktopWidth {
			m.tabsMonitorTab = (m.tabsMonitorTab + 2) % 3
		}
		return m, nil
	case tea.KeyRight:
		if m.width < monitorDesktopWidth {
			m.tabsMonitorTab = (m.tabsMonitorTab + 1) % 3
		}
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		ch := string(msg.Runes)
		switch ch {
		case "/":
			m.tabsMonitorFiltering = true
			return m, nil
		case "s":
			m.tabsMonitorSort = m.tabsMonitorSort.next()
			m.tabsMonitorSelected = 0
			m.clampMonitorSelection()
			return m, nil
		case "m":
			m.tabsMonitorMode = false
			return m, nil
		case "q":
			m.quitting = true
			return m, tea.Quit
		case "k":
			m.moveMonitorSelection(-1)
			return m, nil
		case "j":
			m.moveMonitorSelection(1)
			return m, nil
		case "h":
			if m.width < monitorDesktopWidth {
				m.tabsMonitorTab = (m.tabsMonitorTab + 2) % 3
			}
			return m, nil
		case "l":
			if m.width < monitorDesktopWidth {
				m.tabsMonitorTab = (m.tabsMonitorTab + 1) % 3
			}
			return m, nil
		case "X":
			if len(m.telemetry.OrphanPorts) > 0 {
				if cmd := m.killOrphanPortsCmd(m.telemetry.OrphanPorts); cmd != nil {
					return m, cmd
				}
			}
			return m, nil
		case "?":
			m.returnMode = ModeTabs
			m.mode = ModeHelp
			return m, nil
		default:
			if len(ch) == 1 && ch[0] >= '1' && ch[0] <= '7' {
				m.toggleMonitorPanel(int(ch[0] - '0'))
				return m, nil
			}
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Top-level monitor dashboard rendering
// ---------------------------------------------------------------------------

func (m Model) renderTabsMonitor(width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	reserved := 2 // top header + bottom hints
	if m.toast != "" {
		reserved++
	}
	bodyH := height - reserved
	if bodyH < 3 {
		bodyH = 3
	}

	var bodyLines []string
	if width >= monitorDesktopWidth {
		bodyLines = m.renderMonitorDesktop(width, bodyH)
	} else {
		bodyLines = m.renderMonitorNarrow(width, bodyH)
	}

	var b strings.Builder
	b.WriteString(m.tabsHeaderLine(width))
	b.WriteString("\n")
	for i := 0; i < bodyH; i++ {
		if i < len(bodyLines) {
			b.WriteString(bodyLines[i])
		} else {
			b.WriteString(fitCellWidth("", width))
		}
		b.WriteString("\n")
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.tabsMonitorHintsLine(width))
	return b.String()
}

func (m Model) tabsMonitorHintsLine(width int) string {
	if m.tabsMonitorFiltering {
		pairs := [][2]string{
			{"/", m.tabsMonitorFilter + "█"},
			{"Enter", "apply"},
			{"Esc", "clear"},
		}
		return renderHintsBar(pairs, width)
	}
	sortHint := "sort"
	if m.tabsMonitorSort != monitorSortDefault {
		sortHint = "sort:" + m.tabsMonitorSort.label()
	}
	pairs := [][2]string{
		{"↑↓", "session"},
		{"Enter", "focus"},
		{"/", "filter"},
		{"s", sortHint},
		{"1-7", "panels"},
	}
	if width < monitorDesktopWidth {
		pairs = append(pairs, [2]string{"←→", "tab"})
	}
	if len(m.telemetry.OrphanPorts) > 0 {
		pairs = append(pairs, [2]string{"X", "kill orphans"})
	}
	escLabel := "tabs"
	if m.tabsMonitorFilter != "" {
		escLabel = "clear filter"
	}
	pairs = append(pairs,
		[2]string{"m/Esc", escLabel},
		[2]string{"q", "quit"},
	)
	return renderHintsBar(pairs, width)
}

func (m Model) renderMonitorDesktop(width, height int) []string {
	showCtx := m.monitorPanelVisible(1)
	var midPanels []int
	for _, id := range []int{2, 3, 4, 5, 7} {
		if m.monitorPanelVisible(id) {
			midPanels = append(midPanels, id)
		}
	}
	showMid := len(midPanels) > 0
	showSess := m.monitorPanelVisible(6)

	if !showCtx && !showMid && !showSess {
		return renderMonitorEmptyCanvas("All monitor panels hidden. Press 1-7 to toggle panels.", width, height)
	}

	ctxH, midH, sessH := allocateDesktopBands(height, len(m.telemetry.Sessions), showCtx, showMid, showSess)

	var out []string
	if ctxH > 0 {
		out = append(out, m.renderPanelContext(width, ctxH)...)
	}
	if midH > 0 {
		n := len(midPanels)
		widths := make([]int, n)
		baseW := width / n
		rem := width % n
		boxes := make([][]string, n)
		for i, id := range midPanels {
			w := baseW
			if i == n-1 {
				w += rem
			}
			widths[i] = w
			boxes[i] = m.renderMonitorPanelByID(id, w, midH)
		}
		out = append(out, hstackBoxes(boxes, widths, midH)...)
	}
	if sessH > 0 {
		out = append(out, m.renderPanelSessions(width, sessH)...)
	}
	return padOrTrimLines(out, width, height)
}

func allocateDesktopBands(totalH, numSessions int, showCtx, showMid, showSess bool) (ctxH, midH, sessH int) {
	bands := 0
	if showCtx {
		bands++
	}
	if showMid {
		bands++
	}
	if showSess {
		bands++
	}
	if bands == 0 {
		return 0, 0, 0
	}
	if bands == 1 {
		if showCtx {
			return totalH, 0, 0
		}
		if showMid {
			return 0, totalH, 0
		}
		return 0, 0, totalH
	}

	// Ideal context height: 5..8 when room allows, else 3 (compact 1-line strip).
	ctxIdeal := numSessions + 4
	if ctxIdeal < 5 {
		ctxIdeal = 5
	}
	if ctxIdeal > 8 {
		ctxIdeal = 8
	}
	midIdeal := 9
	if totalH < 28 {
		midIdeal = 8
	}
	if totalH < 24 {
		midIdeal = 6
	}

	if showCtx && showMid && showSess {
		switch {
		case totalH >= 24:
			ctxH = ctxIdeal
			midH = midIdeal
			sessH = totalH - ctxH - midH
			if sessH < 8 {
				deficit := 8 - sessH
				trimCtx := minInt(deficit, ctxH-5)
				ctxH -= trimCtx
				deficit -= trimCtx
				trimMid := minInt(deficit, midH-6)
				midH -= trimMid
				sessH = totalH - ctxH - midH
			}
		case totalH >= 15:
			ctxH = 3 // compact 1-line strip
			midH = 6
			sessH = totalH - ctxH - midH
		default:
			ctxH = 3
			midH = maxInt(3, (totalH-ctxH)/2)
			sessH = maxInt(3, totalH-ctxH-midH)
		}
		return ctxH, midH, sessH
	}

	// Two bands visible.
	if showCtx && showSess {
		ctxH = minInt(ctxIdeal, maxInt(3, totalH/3))
		sessH = totalH - ctxH
		return ctxH, 0, sessH
	}
	if showMid && showSess {
		midH = minInt(midIdeal, maxInt(6, totalH/3))
		sessH = totalH - midH
		return 0, midH, sessH
	}
	// showCtx && showMid
	ctxH = minInt(ctxIdeal, maxInt(3, totalH/2))
	midH = totalH - ctxH
	return ctxH, midH, 0
}

func (m Model) renderMonitorNarrow(width, height int) []string {
	tabBarH := 1
	contentH := height - tabBarH
	if contentH < 3 {
		contentH = 3
	}

	tab := m.tabsMonitorTab
	if tab < 0 || tab > 2 {
		tab = 0
	}

	var panels []int
	switch tab {
	case 0: // Work
		for _, id := range []int{1, 6} {
			if m.monitorPanelVisible(id) {
				panels = append(panels, id)
			}
		}
	case 1: // Usage
		for _, id := range []int{2, 3} {
			if m.monitorPanelVisible(id) {
				panels = append(panels, id)
			}
		}
	case 2: // System
		for _, id := range []int{4, 5, 7} {
			if m.monitorPanelVisible(id) {
				panels = append(panels, id)
			}
		}
	}

	var lines []string
	if len(panels) == 0 {
		lines = renderMonitorEmptyCanvas("Panels in this tab are hidden. Press 1-7 to show.", width, contentH)
	} else {
		heights := splitVerticalHeights(contentH, panels)
		for i, id := range panels {
			lines = append(lines, m.renderMonitorPanelByID(id, width, heights[i])...)
		}
		lines = padOrTrimLines(lines, width, contentH)
	}

	lines = append(lines, renderMonitorNarrowTabBar(tab, width))
	return padOrTrimLines(lines, width, height)
}

func splitVerticalHeights(totalH int, panels []int) []int {
	n := len(panels)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	if n == 1 {
		out[0] = totalH
		return out
	}
	// Special case: Work tab (Context + Sessions) gives Context ~35% (max 8) and Sessions the rest.
	if n == 2 && panels[0] == 1 && panels[1] == 6 {
		ctxH := totalH * 35 / 100
		if ctxH < 5 {
			ctxH = 5
		}
		if ctxH > 8 {
			ctxH = 8
		}
		if ctxH > totalH-4 {
			ctxH = maxInt(3, totalH/2)
		}
		out[0] = ctxH
		out[1] = totalH - ctxH
		return out
	}
	base := totalH / n
	rem := totalH % n
	for i := 0; i < n; i++ {
		out[i] = base
		if i == n-1 {
			out[i] += rem
		}
	}
	return out
}

func renderMonitorNarrowTabBar(activeTab, width int) string {
	labels := [3]string{"Work", "Usage", "System"}
	activeSt := lipgloss.NewStyle().Background(lipgloss.Color(ColorSurface0)).Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	inactiveSt := lipgloss.NewStyle().Background(lipgloss.Color(ColorMantle)).Foreground(lipgloss.Color(ColorOverlay0))
	bgSt := lipgloss.NewStyle().Background(lipgloss.Color(ColorMantle))

	var b strings.Builder
	b.WriteString(bgSt.Render(" "))
	for i, lbl := range labels {
		if i > 0 {
			b.WriteString(bgSt.Render("  "))
		}
		if i == activeTab {
			b.WriteString(activeSt.Render(" [" + lbl + "] "))
		} else {
			b.WriteString(inactiveSt.Render("  " + lbl + "  "))
		}
	}
	return fitCellWidthWithBG(b.String(), width, ColorMantle)
}

func (m Model) renderMonitorPanelByID(id, width, height int) []string {
	switch id {
	case 1:
		return m.renderPanelContext(width, height)
	case 2:
		return m.renderPanelQuota(width, height)
	case 3:
		return m.renderPanelTokens(width, height)
	case 4:
		return m.renderPanelProjects(width, height)
	case 5:
		return m.renderPanelPorts(width, height)
	case 6:
		return m.renderPanelSessions(width, height)
	case 7:
		return m.renderPanelMCP(width, height)
	default:
		return renderMonitorBox("", "panel", ColorSurface1, nil, width, height)
	}
}

// ---------------------------------------------------------------------------
// Panel 1: Context (token rate braille graph + per-session context bars)
// ---------------------------------------------------------------------------

func (m Model) renderPanelContext(width, height int) []string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 4 || innerH < 1 {
		return renderMonitorBox("¹", "context", ColorTeal, nil, width, height)
	}

	ctx := m.telemetry.Context
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	stGreen := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen))
	stBold := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)

	// Compact 1-line strip when height is tight.
	if innerH <= 1 {
		rateHex := lerpHexColor(50, ColorGreen, ColorYellow, ColorRed)
		line := stMuted.Render(" Rate ") +
			lipgloss.NewStyle().Foreground(lipgloss.Color(rateHex)).Bold(true).Render(fmtTokens(int64(ctx.TokensPerMin))+"/min") +
			stMuted.Render("  Total ") +
			stText.Render(fmtTokens(ctx.TotalTokens))
		if ctx.TotalCostUSD > 0 {
			line += stMuted.Render("  ") + lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(fmtCostUSD(ctx.TotalCostUSD, true))
		}
		if ctx.TotalMemKB > 0 {
			line += stMuted.Render("  Σ") + stText.Render(fmtMemKB(ctx.TotalMemKB))
		}
		if ctx.AvgContextPct > 0 {
			avgHex := lerpHexColor(ctx.AvgContextPct, ColorGreen, ColorYellow, ColorRed)
			line += stMuted.Render("  avg ") + lipgloss.NewStyle().Foreground(lipgloss.Color(avgHex)).Render(fmt.Sprintf("%.0f%%", ctx.AvgContextPct))
		}
		line += stMuted.Render("  ") + stGreen.Render(fmt.Sprintf("%d active", ctx.ActiveSessions))
		return renderMonitorBox("¹", "context", ColorTeal, []string{line}, width, height)
	}

	// Split horizontally: left 62% activity graph, right 38% context bars.
	leftW := innerW * 62 / 100
	if leftW < 18 {
		leftW = innerW
	}
	rightW := innerW - leftW - 1
	if rightW < 24 {
		// Too narrow to split: render graph full-width.
		leftLines := renderContextGraphColumn(ctx, innerW, innerH)
		return renderMonitorBox("¹", "context", ColorTeal, leftLines, width, height)
	}

	leftLines := renderContextGraphColumn(ctx, leftW, innerH)
	rightLines := renderContextTableColumn(ctx, m.telemetry.Sessions, rightW, innerH, stBold, stMuted)

	divSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface0))
	combined := make([]string, innerH)
	for i := 0; i < innerH; i++ {
		l := ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		r := ""
		if i < len(rightLines) {
			r = rightLines[i]
		}
		combined[i] = fitCellWidth(l, leftW) + divSt.Render("│") + fitCellWidth(r, rightW)
	}
	return renderMonitorBox("¹", "context", ColorTeal, combined, width, height)
}

func renderContextGraphColumn(ctx model.TelemetryContext, width, height int) []string {
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))

	var lines []string
	// Header line: token rate
	ratePct := 30.0
	if len(ctx.RateHistory) > 0 {
		maxR := 1.0
		for _, v := range ctx.RateHistory {
			if v > maxR {
				maxR = v
			}
		}
		ratePct = (ctx.RateHistory[len(ctx.RateHistory)-1] / maxR) * 100.0
	}
	rateColor := lerpHexColor(ratePct, ColorGreen, ColorYellow, ColorRed)
	lines = append(lines, stMuted.Render(" Token rate  ")+
		lipgloss.NewStyle().Foreground(lipgloss.Color(rateColor)).Bold(true).Render(fmtTokens(int64(ctx.TokensPerMin))+"/min"))

	// Multi-row braille area graph
	graphH := height - 2
	if graphH < 1 {
		graphH = 1
	}
	graphW := width - 2
	if graphW < 4 {
		graphW = 4
	}
	graphRows := brailleGraphMultirow(ctx.RateHistory, graphW, graphH, ColorGreen, ColorYellow, ColorRed)
	for _, r := range graphRows {
		lines = append(lines, " "+r)
	}

	// Summary footer
	if len(lines) < height {
		footer := " " + stText.Render(fmtTokens(ctx.TotalTokens)) + stMuted.Render(" total")
		if ctx.TotalCostUSD > 0 {
			footer += stMuted.Render("  ") + lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(fmtCostUSD(ctx.TotalCostUSD, true))
		}
		if ctx.TotalMemKB > 0 {
			footer += stMuted.Render("  Σ") + stText.Render(fmtMemKB(ctx.TotalMemKB))
		}
		if ctx.AvgContextPct > 0 {
			avgHex := lerpHexColor(ctx.AvgContextPct, ColorGreen, ColorYellow, ColorRed)
			footer += stMuted.Render("  avg ") + lipgloss.NewStyle().Foreground(lipgloss.Color(avgHex)).Render(fmt.Sprintf("%.0f%%", ctx.AvgContextPct))
		}
		lines = append(lines, footer)
	}
	return lines
}

func renderContextTableColumn(ctx model.TelemetryContext, sessions []model.SessionRow, width, height int, stBold, stMuted lipgloss.Style) []string {
	entries := ctx.Sessions
	if len(entries) == 0 && len(sessions) > 0 {
		for _, s := range sessions {
			entries = append(entries, model.ContextSessionEntry{
				SessionKey:      s.Key,
				PaneID:          s.PaneID,
				Project:         s.Project,
				Agent:           s.Agent,
				ContextTokens:   s.ContextTokens,
				ContextWindow:   s.ContextWindow,
				ContextPct:      s.ContextPct,
				Warning:         s.Warning,
				CompactionCount: s.CompactionCount,
			})
		}
	}

	projW := 10
	winW := 9
	pctW := 6 // " 100%⚠"
	barW := width - projW - winW - pctW - 4
	if barW < 4 {
		barW = 4
	}
	if barW > 18 {
		barW = 18
	}

	var lines []string
	hdr := fmt.Sprintf(" %-*s %-*s %s", projW, "Project", barW+pctW, "Context", "Window")
	lines = append(lines, stBold.Render(truncate(hdr, width)))

	if len(entries) == 0 {
		lines = append(lines, stMuted.Render(" No active sessions"))
		return lines
	}

	stTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender))
	for _, e := range entries {
		if len(lines) >= height {
			break
		}
		warn := string(e.Warning)
		if warn == "" {
			if e.ContextPct >= 90 {
				warn = "⚠"
			} else if e.ContextPct >= 75 {
				warn = "!"
			}
		}
		pctHex := lerpHexColor(e.ContextPct, ColorGreen, ColorYellow, ColorRed)
		pctSt := lipgloss.NewStyle().Foreground(lipgloss.Color(pctHex))
		if warn != "" {
			pctSt = pctSt.Bold(true)
		}

		winInfo := ""
		if e.ContextWindow > 0 && e.CompactionCount > 0 {
			winInfo = fmt.Sprintf("%s C%d", fmtTokens(e.ContextWindow), e.CompactionCount)
		} else if e.ContextWindow > 0 {
			winInfo = fmtTokens(e.ContextWindow)
		} else if e.CompactionCount > 0 {
			winInfo = fmt.Sprintf("C%d", e.CompactionCount)
		}

		row := " " +
			stTitle.Render(fmt.Sprintf("%-*s", projW, truncate(e.Project, projW))) + " " +
			meterBar(e.ContextPct, barW, ColorGreen, ColorYellow, ColorRed) +
			pctSt.Render(fmt.Sprintf(" %3.0f%%%s", e.ContextPct, warn))
		if winInfo != "" {
			row += " " + stMuted.Render(truncate(winInfo, winW))
		}
		lines = append(lines, row)
	}
	return lines
}

// ---------------------------------------------------------------------------
// Panel 2: Quota (Claude & Codex side-by-side remaining quota)
// ---------------------------------------------------------------------------

func (m Model) renderPanelQuota(width, height int) []string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 8 || innerH < 1 {
		return renderMonitorBox("²", "quota", ColorLavender, nil, width, height)
	}

	nowUnix := m.opts.NowOrDefault().Unix()
	colW := innerW / 2
	rightW := innerW - colW
	contentH := innerH
	showBottom := innerH >= 6
	if showBottom {
		contentH = innerH - 1
	}

	leftCol := renderQuotaSourceCol("CLAUDE", m.telemetry.QuotaForAgent("claude"), "run claude", colW, contentH, nowUnix)
	rightCol := renderQuotaSourceCol("CODEX", m.telemetry.QuotaForAgent("codex"), "run codex", rightW, contentH, nowUnix)

	lines := make([]string, 0, innerH)
	for i := 0; i < contentH; i++ {
		l, r := "", ""
		if i < len(leftCol) {
			l = leftCol[i]
		}
		if i < len(rightCol) {
			r = rightCol[i]
		}
		lines = append(lines, fitCellWidth(l, colW)+fitCellWidth(r, rightW))
	}
	if showBottom {
		stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
		stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
		lines = append(lines, " "+stText.Render("Total "+fmtTokens(m.telemetry.Context.TotalTokens))+
			stMuted.Render("  "+fmtTokens(int64(m.telemetry.Context.TokensPerMin))+"/min"))
	}
	return renderMonitorBox("²", "quota", ColorLavender, lines, width, height)
}

func renderQuotaSourceCol(name string, q *agent.QuotaInfo, emptyHint string, width, height int, nowUnix int64) []string {
	stTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stSub := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))

	if q == nil || (q.Primary == nil && q.Secondary == nil) {
		return []string{
			stTitle.Render(" " + name),
			stMuted.Render("  — no data"),
			stMuted.Render("  " + emptyHint),
		}
	}

	age := int64(0)
	if q.UpdatedAt > 0 && nowUnix > q.UpdatedAt {
		age = nowUnix - q.UpdatedAt
	}
	isClaude := strings.EqualFold(q.Agent, "claude") || strings.EqualFold(name, "CLAUDE")
	isStale := false
	isLastKnown := false
	if !q.Estimated && q.UpdatedAt > 0 {
		if isClaude {
			if age > monitorClaudeLastKnownS {
				isStale = true
			} else if age > monitorQuotaStaleS {
				isLastKnown = true
			}
		} else if age > monitorQuotaStaleS {
			isStale = true
		}
	}

	hdrStyle := stTitle
	if isStale {
		hdrStyle = stMuted.Bold(true)
	}

	barW := width - 10
	if barW < 3 {
		barW = 3
	}
	if barW > 10 {
		barW = 10
	}

	var lines []string
	hdrText := " " + name
	if plan := strings.TrimSpace(q.PlanType); plan != "" {
		hdrText += " (" + plan + ")"
	}
	hdrLine := hdrStyle.Render(hdrText)
	if q.Estimated {
		hdrLine += stMuted.Render(" ~est")
	} else if isLastKnown {
		hdrLine += stMuted.Render(" ~last")
	}
	lines = append(lines, hdrLine)

	renderWindow := func(w *agent.QuotaWindow, fallbackLbl string) {
		if w == nil || len(lines) >= height {
			return
		}
		lbl := strings.TrimSpace(w.Label)
		if lbl == "" && w.WindowMinutes > 0 {
			lbl = agent.FormatQuotaWindowLabel(w.WindowMinutes)
		}
		if lbl == "" {
			lbl = fallbackLbl
		}
		rem := w.RemainingPct()
		pctHex := lerpHexColor(w.UsedPct, ColorGreen, ColorYellow, ColorRed)
		pctSt := lipgloss.NewStyle().Foreground(lipgloss.Color(pctHex))
		if isStale {
			pctSt = stMuted
		}
		lines = append(lines, stSub.Render(fmt.Sprintf(" %-2s ", truncate(lbl, 3)))+
			remainingBar(w.UsedPct, barW)+
			pctSt.Render(fmt.Sprintf(" %3.0f%%", rem)))

		if len(lines) < height {
			cd := ""
			if !isStale && w.ResetsAt > 0 {
				cd = fmtCountdown(w.ResetsAt, nowUnix)
			}
			if cd != "" {
				lines = append(lines, stMuted.Render("   "+cd))
			} else {
				lines = append(lines, "")
			}
		}
	}

	renderWindow(q.Primary, "5h")
	renderWindow(q.Secondary, "7d")
	return lines
}

// ---------------------------------------------------------------------------
// Panel 3: Tokens (selected session token breakdown + per-turn sparkline)
// ---------------------------------------------------------------------------

func (m Model) renderPanelTokens(width, height int) []string {
	innerW := width - 2
	innerH := height - 2

	sessions := m.monitorSessions()
	var sel *model.SessionRow
	if m.tabsMonitorSelected >= 0 && m.tabsMonitorSelected < len(sessions) {
		s := sessions[m.tabsMonitorSelected]
		sel = &s
	}

	title := "tokens"
	if sel != nil {
		sid := sel.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		if sel.Project != "" && sid != "" {
			title = fmt.Sprintf("tokens (%s/%s)", truncate(sel.Project, 10), sid)
		} else if sel.Project != "" {
			title = fmt.Sprintf("tokens (%s)", truncate(sel.Project, 14))
		}
	}

	if innerW < 8 || innerH < 1 {
		return renderMonitorBox("³", title, ColorBlue, nil, width, height)
	}

	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	stTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)

	if sel == nil {
		return renderMonitorBox("³", title, ColorBlue, []string{stMuted.Render(" No session selected")}, width, height)
	}

	totalIn := sel.Usage.InputTokens
	totalOut := sel.Usage.OutputTokens
	cacheR := sel.Usage.CacheReadTokens
	cacheW := sel.Usage.CacheWriteTokens
	total := sel.TotalTokens
	if total <= 0 {
		total = sel.Usage.Total()
	}
	turns := sel.TurnCount
	avg := sel.AvgTokensPerTurn
	if avg <= 0 && turns > 0 {
		avg = total / int64(turns)
	}

	pctOf := func(v int64) float64 {
		if total <= 0 {
			return 0
		}
		return float64(v) / float64(total) * 100.0
	}

	barW := innerW - 18
	if barW < 4 {
		barW = 4
	}
	if barW > 14 {
		barW = 14
	}

	hasCost := sel.Usage.CostPriced || sel.Usage.CostUSD > 0
	costStr := fmtCostUSD(sel.Usage.CostUSD, sel.Usage.CostPriced)
	stCost := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow))

	var lines []string
	if innerH < 7 {
		totLine := stMuted.Render(" Total: ") + stTitle.Render(fmtTokens(total)) +
			stMuted.Render(fmt.Sprintf("  %dt %s/t", turns, fmtTokens(avg)))
		if hasCost {
			totLine += stMuted.Render("  ") + stCost.Render(costStr)
		}
		lines = append(lines, totLine)
	} else {
		totLine := stMuted.Render(" Total: ") + stTitle.Render(fmtTokens(total))
		if hasCost && innerW < 36 {
			totLine += stMuted.Render("  Cost: ") + stCost.Render(costStr)
		}
		lines = append(lines, totLine)
	}
	lines = append(lines, stMuted.Render(" Input : ")+meterBar(pctOf(totalIn), barW, ColorTeal, ColorBlue, ColorLavender)+
		lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal)).Render(" "+fmtTokens(totalIn)))
	lines = append(lines, stMuted.Render(" Output: ")+meterBar(pctOf(totalOut), barW, ColorYellow, ColorRed, ColorRed)+
		lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(" "+fmtTokens(totalOut)))
	lines = append(lines, stMuted.Render(" CacheR: ")+meterBar(pctOf(cacheR), barW, ColorBlue, ColorMauve, ColorMauve)+
		lipgloss.NewStyle().Foreground(lipgloss.Color(ColorMauve)).Render(" "+fmtTokens(cacheR)))
	lines = append(lines, stMuted.Render(" CacheW: ")+meterBar(pctOf(cacheW), barW, ColorBlue, ColorMauve, ColorMauve)+
		lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBlue)).Render(" "+fmtTokens(cacheW)))

	// Per-turn sparkline
	sparkW := innerW - 10
	if sparkW < 4 {
		sparkW = 4
	}
	if sparkW > 20 {
		sparkW = 20
	}
	hist := make([]float64, len(sel.TokenHistory))
	for i, v := range sel.TokenHistory {
		hist[i] = float64(v)
	}
	lines = append(lines, " "+brailleSparkline(hist, sparkW, ColorGreen, ColorYellow, ColorRed)+stMuted.Render(" tok/t"))

	if innerH >= 7 {
		turnLine := stMuted.Render(" Turns: ") + stText.Render(fmt.Sprintf("%d", turns)) +
			stMuted.Render("  Avg: ") + stText.Render(fmtTokens(avg)+"/t")
		if hasCost && innerW >= 36 {
			turnLine += stMuted.Render("  Cost: ") + stCost.Render(costStr)
		}
		lines = append(lines, turnLine)
	}

	return renderMonitorBox("³", title, ColorBlue, lines, width, height)
}

// ---------------------------------------------------------------------------
// Panel 4: Projects (project name, git branch, +added ~modified or clean)
// ---------------------------------------------------------------------------

func (m Model) renderPanelProjects(width, height int) []string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 8 || innerH < 1 {
		return renderMonitorBox("⁴", "projects", ColorBlue, nil, width, height)
	}

	stTitle := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stGreen := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen))
	stYellow := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow))
	stMauve := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorMauve))

	if len(m.telemetry.Projects) == 0 {
		return renderMonitorBox("⁴", "projects", ColorBlue, []string{stMuted.Render(" No active projects")}, width, height)
	}

	var lines []string
	for _, proj := range m.telemetry.Projects {
		if len(lines) >= innerH {
			break
		}
		wtBadge := ""
		if proj.WorktreeCount > 1 {
			wtBadge = fmt.Sprintf(" ⎇ %d", proj.WorktreeCount)
		}
		nameBudget := innerW - 2 - ansi.StringWidth(wtBadge)
		if nameBudget < 4 {
			nameBudget = 4
		}
		projLine := stTitle.Render(" " + truncate(proj.Name, nameBudget))
		if wtBadge != "" {
			projLine += stMauve.Render(wtBadge)
		}
		lines = append(lines, projLine)
		if len(lines) >= innerH {
			break
		}
		var br strings.Builder
		br.WriteString("   ")
		if !proj.HasGit || strings.TrimSpace(proj.Branch) == "" {
			br.WriteString(stMuted.Render("no git"))
		} else {
			var statParts []string
			if proj.FilesAdded > 0 || proj.FilesModified > 0 {
				if proj.FilesAdded > 0 {
					statParts = append(statParts, stGreen.Render(fmt.Sprintf("+%d", proj.FilesAdded)))
				}
				if proj.FilesModified > 0 {
					statParts = append(statParts, stYellow.Render(fmt.Sprintf("~%d", proj.FilesModified)))
				}
			} else if proj.IsClean {
				statParts = append(statParts, stGreen.Render("clean"))
			}
			statStr := strings.Join(statParts, " ")
			statW := ansi.StringWidth(statStr)
			branchBudget := innerW - 3
			if statW > 0 {
				branchBudget -= statW + 1
			}
			if branchBudget < 6 {
				branchBudget = 6
			}
			br.WriteString(stText.Render(truncate(proj.Branch, branchBudget)))
			if statStr != "" {
				br.WriteString(" " + statStr)
			}
		}
		lines = append(lines, br.String())
	}
	return renderMonitorBox("⁴", "projects", ColorBlue, lines, width, height)
}

// ---------------------------------------------------------------------------
// Panel 5: Ports (listening ports, duplicate ⚠ warning, orphan ports + X hint)
// ---------------------------------------------------------------------------

func (m Model) renderPanelPorts(width, height int) []string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 8 || innerH < 1 {
		return renderMonitorBox("⁵", "ports", ColorMauve, nil, width, height)
	}

	stBold := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	stGreen := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen))
	stRed := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRed)).Bold(true)
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))

	portCounts := map[int]int{}
	for _, p := range m.telemetry.Ports {
		portCounts[p.Port]++
	}

	var lines []string
	lines = append(lines, stBold.Render(" Port   Session"))

	for _, p := range m.telemetry.Ports {
		dup := p.Duplicate || portCounts[p.Port] > 1
		portSt := stGreen
		warn := ""
		if dup {
			portSt = stRed
			warn = " ⚠"
		}
		sid := p.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		lbl := strings.TrimSpace(p.Project + " " + sid)
		lines = append(lines, portSt.Render(fmt.Sprintf(" :%-5d", p.Port))+" "+stText.Render(lbl)+stRed.Render(warn))
	}

	for _, op := range m.telemetry.OrphanPorts {
		lbl := strings.TrimSpace(op.Project)
		if lbl != "" {
			lbl += " "
		}
		lines = append(lines, stRed.Render(fmt.Sprintf(" :%-5d %s⚠orphan", op.Port, lbl)))
	}

	if len(m.telemetry.Ports) == 0 && len(m.telemetry.OrphanPorts) == 0 {
		lines = append(lines, stMuted.Render(" No open ports"))
	}

	if len(m.telemetry.OrphanPorts) > 0 {
		hint := stMuted.Render(" X kill orphans")
		if len(lines) < innerH {
			lines = append(lines, hint)
		} else if innerH >= 2 {
			lines[innerH-1] = hint
		}
	}

	return renderMonitorBox("⁵", "ports", ColorMauve, lines, width, height)
}

// ---------------------------------------------------------------------------
// Panel 6: Sessions (responsive table of monitored sessions)
// ---------------------------------------------------------------------------

func (m Model) renderPanelSessions(width, height int) []string {
	title := "sessions"
	if m.tabsMonitorFiltering {
		title += "  / " + m.tabsMonitorFilter + "█"
	} else if m.tabsMonitorFilter != "" {
		title += "  / " + m.tabsMonitorFilter
	}
	if m.tabsMonitorSort != monitorSortDefault {
		title += "  sort:" + m.tabsMonitorSort.label()
	}

	innerW := width - 2
	innerH := height - 2
	if innerW < 12 || innerH < 1 {
		return renderMonitorBox("⁶", title, ColorLavender, nil, width, height)
	}

	stBold := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))

	showPID := innerW >= 120
	showSID := innerW >= 76
	showCfg := innerW >= 100
	showModel := innerW >= 90
	showTokens := innerW >= 86
	showCost := width >= 104
	showMem := innerW >= 110
	showTurn := innerW >= 110

	projW := 10
	if innerW >= 120 {
		projW = 14
	} else if innerW < 80 {
		projW = 8
	}
	sidW := 8
	if innerW < 110 {
		sidW = 5
	}
	cfgW := 10
	if innerW >= 110 {
		cfgW = 12
	}
	statusW := 9
	if innerW < 100 {
		statusW = 7
	}
	modelW := 12
	if innerW < 110 {
		modelW = 10
	}
	ctxW := 5
	tokW := 6
	costW := 7
	memW := 6
	turnW := 4
	pidW := 6

	fixedW := 1 + 1 + 4 + 1 + projW + 1 + statusW + 1 + ctxW // marker + AI + Project + Status + Ctx
	if showPID {
		fixedW += pidW + 1
	}
	if showSID {
		fixedW += sidW + 1
	}
	if showCfg {
		fixedW += cfgW + 1
	}
	if showModel {
		fixedW += modelW + 1
	}
	if showTokens {
		fixedW += tokW + 1
	}
	if showCost {
		fixedW += costW + 1
	}
	if showMem {
		fixedW += memW + 1
	}
	if showTurn {
		fixedW += turnW + 1
	}
	summaryW := innerW - fixedW - 2
	if summaryW < 8 {
		summaryW = 8
	}

	// Build header row.
	var hdr strings.Builder
	hdr.WriteString("  ")
	hdr.WriteString(fmt.Sprintf("%-4s ", "AI"))
	if showPID {
		hdr.WriteString(fmt.Sprintf("%-*s ", pidW, "PID"))
	}
	hdr.WriteString(fmt.Sprintf("%-*s ", projW, "Project"))
	if showSID {
		hdr.WriteString(fmt.Sprintf("%-*s ", sidW, "Session"))
	}
	if showCfg {
		hdr.WriteString(fmt.Sprintf("%-*s ", cfgW, "Config"))
	}
	hdr.WriteString(fmt.Sprintf("%-*s ", summaryW, "Summary"))
	hdr.WriteString(fmt.Sprintf("%-*s ", statusW, "Status"))
	if showModel {
		hdr.WriteString(fmt.Sprintf("%-*s ", modelW, "Model"))
	}
	hdr.WriteString(fmt.Sprintf("%*s", ctxW, "Ctx"))
	if showTokens {
		hdr.WriteString(fmt.Sprintf(" %*s", tokW, "Tokens"))
	}
	if showCost {
		hdr.WriteString(fmt.Sprintf(" %*s", costW, "Cost"))
	}
	if showMem {
		hdr.WriteString(fmt.Sprintf(" %*s", memW, "Mem"))
	}
	if showTurn {
		hdr.WriteString(fmt.Sprintf(" %*s", turnW, "Turn"))
	}

	lines := []string{stBold.Render(truncate(hdr.String(), innerW))}

	sessions := m.monitorSessions()
	if len(sessions) == 0 {
		if len(m.telemetry.Sessions) > 0 && m.tabsMonitorFilter != "" {
			lines = append(lines, stMuted.Render(" No sessions match / "+m.tabsMonitorFilter))
		} else {
			lines = append(lines, stMuted.Render(" No monitored agent sessions."))
		}
		return renderMonitorBox("⁶", title, ColorLavender, lines, width, height)
	}

	// Calculate summary column x-offset for current-task sub-rows.
	summaryIndent := 2 + 5 + projW + 1
	if showPID {
		summaryIndent += pidW + 1
	}
	if showSID {
		summaryIndent += sidW + 1
	}
	if showCfg {
		summaryIndent += cfgW + 1
	}

	type sessRowLine struct {
		sessIdx int
		text    string
	}
	var allRows []sessRowLine

	for i, s := range sessions {
		selected := i == m.tabsMonitorSelected
		bg := ColorBase
		if selected {
			bg = ColorSurface0
		}
		st := func(fg string, bold bool) lipgloss.Style {
			stl := lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
			if bold {
				stl = stl.Bold(true)
			}
			return stl
		}

		marker := " "
		if selected {
			marker = "►"
		}
		badge := agentBadge(s.Agent, s.LaunchSurface)
		badgeCol := agentBadgeColor(s.Agent)

		statusLbl := s.Status.Label()
		statusCol := StatusColor(s.Status)
		if s.RateLimited {
			statusLbl = "RateLimit"
			statusCol = ColorRed
		}

		sid := s.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		cfg := s.Profile
		if cfg == "" {
			cfg = s.ConfigRoot
		}
		mdl := shortenModel(s.Model, s.ContextWindow)
		ctxHex := lerpHexColor(s.ContextPct, ColorGreen, ColorYellow, ColorRed)

		var row strings.Builder
		row.WriteString(st(ColorLavender, true).Render(marker + " "))
		row.WriteString(st(badgeCol, selected).Render(fmt.Sprintf("%-4s", truncate(badge, 4))))
		row.WriteString(st(bg, false).Render(" "))
		if showPID {
			pidStr := "—"
			if s.PID > 0 {
				pidStr = fmt.Sprintf("%d", s.PID)
			}
			row.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%-*s", pidW, truncate(pidStr, pidW))))
			row.WriteString(st(bg, false).Render(" "))
		}
		row.WriteString(st(ColorLavender, selected).Render(fmt.Sprintf("%-*s", projW, truncate(s.Project, projW))))
		row.WriteString(st(bg, false).Render(" "))
		if showSID {
			row.WriteString(st(ColorSubtext0, false).Render(fmt.Sprintf("%-*s", sidW, truncate(sid, sidW))))
			row.WriteString(st(bg, false).Render(" "))
		}
		if showCfg {
			row.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%-*s", cfgW, truncate(cfg, cfgW))))
			row.WriteString(st(bg, false).Render(" "))
		}
		sum := s.Summary
		if sum == "" {
			sum = "—"
		}
		row.WriteString(st(ColorText, selected).Render(fmt.Sprintf("%-*s", summaryW, truncate(sum, summaryW))))
		row.WriteString(st(bg, false).Render(" "))
		row.WriteString(st(statusCol, s.RateLimited || s.Status.IsAttention()).Render(fmt.Sprintf("%-*s", statusW, truncate(statusLbl, statusW))))
		row.WriteString(st(bg, false).Render(" "))
		if showModel {
			row.WriteString(st(ColorSubtext0, false).Render(fmt.Sprintf("%-*s", modelW, truncate(mdl, modelW))))
			row.WriteString(st(bg, false).Render(" "))
		}
		row.WriteString(st(ctxHex, s.ContextPct >= 75).Render(fmt.Sprintf("%*s", ctxW, fmt.Sprintf("%.0f%%", s.ContextPct))))
		if showTokens {
			row.WriteString(st(bg, false).Render(" "))
			row.WriteString(st(ColorText, false).Render(fmt.Sprintf("%*s", tokW, fmtTokens(s.TotalTokens))))
		}
		if showCost {
			row.WriteString(st(bg, false).Render(" "))
			costFg := ColorOverlay0
			if s.Usage.CostPriced || s.Usage.CostUSD > 0 {
				costFg = ColorYellow
			}
			row.WriteString(st(costFg, false).Render(fmt.Sprintf("%*s", costW, fmtCostUSD(s.Usage.CostUSD, s.Usage.CostPriced))))
		}
		if showMem {
			row.WriteString(st(bg, false).Render(" "))
			row.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%*s", memW, fmtMemKB(s.MemKB))))
		}
		if showTurn {
			row.WriteString(st(bg, false).Render(" "))
			row.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%*d", turnW, s.TurnCount)))
		}

		renderedRow := row.String()
		if selected {
			renderedRow = fitCellWidthWithBG(renderedRow, innerW, bg)
		} else {
			renderedRow = fitCellWidth(renderedRow, innerW)
		}
		allRows = append(allRows, sessRowLine{sessIdx: i, text: renderedRow})

		if task := strings.TrimSpace(s.CurrentTask); task != "" {
			indent := summaryIndent
			if indent > innerW-12 {
				indent = 4
			}
			taskText := strings.Repeat(" ", indent) +
				lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1)).Render("└─ ") +
				lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0)).Render(truncate(task, maxInt(8, innerW-indent-3)))
			allRows = append(allRows, sessRowLine{sessIdx: i, text: fitCellWidth(taskText, innerW)})
		}
	}

	// Scroll to keep selected session visible.
	availRows := innerH - 1
	selRowIdx := 0
	for idx, r := range allRows {
		if r.sessIdx == m.tabsMonitorSelected {
			selRowIdx = idx
			break
		}
	}
	start := windowStart(selRowIdx, len(allRows), availRows)
	for i := 0; i < availRows && start+i < len(allRows); i++ {
		lines = append(lines, allRows[start+i].text)
	}
	return renderMonitorBox("⁶", title, ColorLavender, lines, width, height)
}

// ---------------------------------------------------------------------------
// Panel 7: MCP Servers (Codex MCP servers, parent CLI, profile, active/total)
// ---------------------------------------------------------------------------

func (m Model) renderPanelMCP(width, height int) []string {
	innerW := width - 2
	innerH := height - 2
	if innerW < 8 || innerH < 1 {
		return renderMonitorBox("⁷", "mcp servers", ColorMauve, nil, width, height)
	}

	stBold := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)
	stText := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	stSub := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	stMuted := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	stGreen := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorGreen)).Bold(true)
	stBlue := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBlue))

	parentW := 6
	actW := 4
	ageW := 7
	profW := innerW - parentW - actW - ageW - 4
	if profW < 5 {
		profW = 5
	}
	if profW > 12 {
		profW = 12
	}

	hdr := fmt.Sprintf(" %-*s %-*s %-*s %s", parentW, "Parent", profW, "Profile", actW, "A/T", "Last")
	lines := []string{stBold.Render(truncate(hdr, innerW))}
	if len(m.telemetry.MCPServers) == 0 {
		lines = append(lines, stMuted.Render(" No MCP servers"))
		return renderMonitorBox("⁷", "mcp servers", ColorMauve, lines, width, height)
	}

	nowUnix := m.opts.NowOrDefault().Unix()
	for _, srv := range m.telemetry.MCPServers {
		if len(lines) >= innerH {
			break
		}
		parent := srv.ParentCLI
		if parent == "" {
			parent = srv.Agent
		}
		profile := srv.Profile
		if profile == "" {
			profile = "default"
		}
		cntSt := stMuted
		if srv.ActiveThreads > 0 {
			cntSt = stGreen
		} else if srv.TotalThreads > 0 {
			cntSt = stBlue
		}
		age := fmtRelativeAge(srv.LastActivityAt, nowUnix)
		cntStr := fmt.Sprintf("%d/%d", srv.ActiveThreads, srv.TotalThreads)
		row := " " +
			stText.Render(fmt.Sprintf("%-*s ", parentW, truncate(parent, parentW))) +
			stSub.Render(fmt.Sprintf("%-*s ", profW, truncate(profile, profW))) +
			cntSt.Render(fmt.Sprintf("%-*s ", actW, cntStr)) +
			stMuted.Render(age)
		lines = append(lines, row)
	}
	return renderMonitorBox("⁷", "mcp servers", ColorMauve, lines, width, height)
}

// ---------------------------------------------------------------------------
// Box drawing & layout helpers
// ---------------------------------------------------------------------------

// renderMonitorBox draws a btop-style rounded border with notch title:
//
//	╭─┐¹context┌─────────╮
//	│ ...                │
//	╰────────────────────╯
func renderMonitorBox(numSup, title, borderHex string, content []string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if width < 4 {
		out := make([]string, height)
		for i := range out {
			out[i] = fitCellWidth("", width)
		}
		return out
	}

	bSt := lipgloss.NewStyle().Foreground(lipgloss.Color(borderHex))
	numSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	ttlSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText)).Bold(true)

	// Top border with notch title.
	maxTitleW := width - 7 // ╭─┐ + num + ┌ + ╮
	if maxTitleW < 1 {
		maxTitleW = 1
	}
	shortTitle := truncate(title, maxTitleW)
	notch := bSt.Render("┐") + numSt.Render(numSup) + ttlSt.Render(shortTitle) + bSt.Render("┌")
	notchW := ansi.StringWidth(notch)
	rightDash := width - 3 - notchW // ╭─ (2) + ╮ (1)
	if rightDash < 0 {
		rightDash = 0
	}
	topLine := fitCellWidth(bSt.Render("╭─")+notch+bSt.Render(strings.Repeat("─", rightDash)+"╮"), width)
	if height == 1 {
		return []string{topLine}
	}

	bottomLine := fitCellWidth(bSt.Render("╰"+strings.Repeat("─", width-2)+"╯"), width)
	if height == 2 {
		return []string{topLine, bottomLine}
	}

	innerW := width - 2
	innerH := height - 2
	out := make([]string, 0, height)
	out = append(out, topLine)
	side := bSt.Render("│")
	for i := 0; i < innerH; i++ {
		ln := ""
		if i < len(content) {
			ln = content[i]
		}
		out = append(out, side+fitCellWidth(ln, innerW)+side)
	}
	out = append(out, bottomLine)
	return out
}

func hstackBoxes(boxes [][]string, widths []int, height int) []string {
	out := make([]string, height)
	for row := 0; row < height; row++ {
		var b strings.Builder
		for i, box := range boxes {
			if row < len(box) {
				b.WriteString(box[row])
			} else {
				b.WriteString(fitCellWidth("", widths[i]))
			}
		}
		out[row] = b.String()
	}
	return out
}

func renderMonitorEmptyCanvas(msg string, width, height int) []string {
	out := make([]string, height)
	mid := height / 2
	for i := 0; i < height; i++ {
		if i == mid {
			out[i] = fitCellWidth(styleMuted.Render("  "+truncate(msg, maxInt(4, width-4))), width)
		} else {
			out[i] = fitCellWidth("", width)
		}
	}
	return out
}

func padOrTrimLines(lines []string, width, height int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, fitCellWidth("", width))
	}
	return lines
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "github.com/arhammahajan/spaces/internal/agent/plugins"
	"github.com/arhammahajan/spaces/internal/model"
)

// Run constructs and runs the popup program.
func Run(backend Backend, opts Options) error {
	m := New(backend, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		// Pre-window-size render: still show useful content for tests.
		return m.renderFull(80, 24)
	}
	return m.renderFull(m.width, m.height)
}

const statusColWidth = 9

func searchLineY() int   { return 1 }
func listTop() int       { return 2 }
func agentsListTop() int { return 2 }

type navigatorLayout struct {
	topY         int
	bodyHeight   int
	showDividers bool
	detailLines  int
}

func (m Model) navigatorLayout(width, height int) navigatorLayout {
	_ = width
	if height <= 0 {
		height = 24
	}
	showDividers := height >= 12
	topY := 2 // header + search
	if showDividers {
		topY = 3 // + top hairline divider
	}
	detailLines := 0
	if height >= 16 {
		detailLines = 2
	} else if height >= 12 {
		detailLines = 1
	}
	reserved := topY + 1 // + hints line
	if detailLines > 0 {
		if showDividers {
			reserved++ // + bottom hairline divider
		}
		reserved += detailLines
	}
	if m.toast != "" {
		reserved++
	}
	bodyHeight := height - reserved
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	return navigatorLayout{
		topY:         topY,
		bodyHeight:   bodyHeight,
		showDividers: showDividers,
		detailLines:  detailLines,
	}
}

func (m Model) listHeight() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	reserved := 3 // header + search + hints
	if h >= 16 {
		reserved = 4 // + detail line
	}
	if m.toast != "" {
		reserved++
	}
	lh := h - reserved
	if lh < 1 {
		lh = 1
	}
	return lh
}

func windowStart(selected, total, height int) int {
	if height <= 0 || total <= height {
		return 0
	}
	if selected < 0 {
		selected = 0
	}
	start := selected - height/2
	if start < 0 {
		start = 0
	}
	if start > total-height {
		start = total - height
	}
	return start
}

func renderDividerLine(width int) string {
	if width <= 0 {
		return ""
	}
	return fitCellWidth(styleDivider.Render(strings.Repeat("─", width)), width)
}

func renderSectionRule(leftStyled string, width int) string {
	if width <= 0 {
		return ""
	}
	leftW := ansi.StringWidth(leftStyled)
	if leftW+3 >= width {
		return fitCellWidth(leftStyled, width)
	}
	ruleLen := width - leftW - 2
	if ruleLen < 1 {
		return fitCellWidth(leftStyled, width)
	}
	line := leftStyled + " " + styleDivider.Render(strings.Repeat("─", ruleLen)) + " "
	return fitCellWidth(line, width)
}

func renderFilterPills(active model.AttentionFilter, bg string) string {
	activeSt := lipgloss.NewStyle().
		Background(lipgloss.Color(ColorSurface0)).
		Foreground(lipgloss.Color(ColorLavender)).
		Bold(true)
	inactiveSt := lipgloss.NewStyle().
		Background(lipgloss.Color(bg)).
		Foreground(lipgloss.Color(ColorOverlay0))
	sep := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Render(" ")

	pills := make([]string, 0, len(model.AllFilters()))
	for _, f := range model.AllFilters() {
		label := f.Label()
		if f == active {
			pills = append(pills, activeSt.Render("["+label+"]"))
		} else {
			pills = append(pills, inactiveSt.Render(label))
		}
	}
	return strings.Join(pills, sep)
}

func renderHeaderBar(title string, summarySegs []string, active model.AttentionFilter, width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	dot := st(ColorSurface1, false).Render(" · ")

	var lb strings.Builder
	lb.WriteString(st(ColorText, true).Render(" " + title))
	if len(summarySegs) > 0 {
		lb.WriteString(st(bg, false).Render("   "))
		lb.WriteString(strings.Join(summarySegs, dot))
	}
	leftStyled := lb.String()
	rightStyled := renderFilterPills(active, bg) + st(bg, false).Render(" ")

	leftW := ansi.StringWidth(leftStyled)
	rightW := ansi.StringWidth(rightStyled)
	var line string
	if leftW+rightW+2 <= width {
		line = leftStyled + st(bg, false).Render(strings.Repeat(" ", width-leftW-rightW)) + rightStyled
	} else if width > rightW+8 {
		truncLeft := ansi.Truncate(leftStyled, width-rightW-2, "…")
		rem := width - ansi.StringWidth(truncLeft) - rightW
		if rem < 1 {
			rem = 1
		}
		line = truncLeft + st(bg, false).Render(strings.Repeat(" ", rem)) + rightStyled
	} else {
		line = ansi.Truncate(leftStyled, width, "…")
	}
	return fitCellWidthWithBG(line, width, bg)
}

func renderSearchBar(query string, active bool, placeholder, matchText string, width int) string {
	slashSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	querySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	caretSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	phSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	matchSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))

	rightStyled := ""
	rightW := 0
	if matchText != "" {
		rightStyled = matchSt.Render(matchText + " ")
		rightW = ansi.StringWidth(rightStyled)
	}

	maxLeft := width
	if rightW > 0 {
		maxLeft = width - rightW - 2
	}
	if maxLeft < 8 {
		maxLeft = 8
	}

	var lb strings.Builder
	lb.WriteString(slashSt.Render(" / "))
	if query == "" && !active {
		lb.WriteString(phSt.Render(truncate(placeholder, maxLeft-4)))
	} else {
		lb.WriteString(querySt.Render(truncate(query, maxLeft-5)))
		if active {
			lb.WriteString(caretSt.Render("_"))
		}
	}
	leftStyled := lb.String()
	leftW := ansi.StringWidth(leftStyled)

	if rightW > 0 && leftW+rightW+2 <= width {
		return fitCellWidth(leftStyled+strings.Repeat(" ", width-leftW-rightW)+rightStyled, width)
	}
	return fitCellWidth(leftStyled, width)
}

func renderHintsBar(pairs [][2]string, width int) string {
	keySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	descSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	sepSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1))

	var b strings.Builder
	b.WriteString(" ")
	used := 1
	for i, kv := range pairs {
		seg := keySt.Render(kv[0])
		if kv[1] != "" {
			seg += descSt.Render(" " + kv[1])
		}
		sep := ""
		if i > 0 {
			sep = sepSt.Render(" · ")
		}
		need := ansi.StringWidth(sep) + ansi.StringWidth(seg)
		if i > 0 && used+need > width-1 {
			break
		}
		b.WriteString(sep)
		b.WriteString(seg)
		used += need
	}
	return fitCellWidth(b.String(), width)
}

func (m Model) renderFull(width, height int) string {
	if m.mode == ModeRename && m.renameStandalone && height <= 3 {
		return m.renameBottomBar(width)
	}
	if width < 16 || height < 4 {
		switch m.mode {
		case ModeAgents:
			return styleHeader.Render("Agents")
		case ModeInbox:
			return styleHeader.Render("Inbox")
		case ModeConversations:
			return styleHeader.Render("Conversations")
		case ModeShared:
			return styleHeader.Render("Shared")
		case ModeTabs:
			return styleHeader.Render("Tabs")
		case ModeWorktrees:
			return styleHeader.Render("Worktrees")
		case ModeUsage:
			return styleHeader.Render("Usage")
		default:
			return styleHeader.Render("Spaces")
		}
	}
	// Modal overlays render centered inside the popup viewport so they never
	// overflow off the bottom of the tmux display-popup.
	switch m.mode {
	case ModeNewSpace:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.newSpaceDialogWithWorktree(width))
	case ModeRename:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.renameDialog(width))
	case ModeConfirmKill:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.confirmDialog(width))
	case ModeConfirmRemoveWorktree:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.renderConfirmRemoveWorktree(width))
	case ModeHelp:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.helpDialog(width))
	case ModeHookDetail:
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, m.hookDialog(width, height))
	case ModeAgents:
		return m.renderAgents(width, height)
	case ModeInbox:
		return m.renderInbox(width, height)
	case ModeConversations:
		return m.renderConversations(width, height)
	case ModeShared:
		return m.renderShared(width, height)
	case ModeTabs:
		return m.renderTabs(width, height)
	case ModeWorktrees:
		return m.renderWorktrees(width, height)
	case ModeUsage:
		return m.renderUsage(width, height)
	}
	return m.renderNavigator(width, height)
}

// ---------------------------------------------------------------------------
// navigator
// ---------------------------------------------------------------------------

func (m Model) renderNavigator(width, height int) string {
	layout := m.navigatorLayout(width, height)
	rows := m.visibleRows()

	var b strings.Builder
	b.WriteString(m.headerLine(width))
	b.WriteString("\n")
	b.WriteString(m.searchLine(width))
	b.WriteString("\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	lh := layout.bodyHeight
	if len(rows) == 0 {
		msg := "No spaces match this filter."
		if len(m.spaces) == 0 {
			if m.spacesLoading {
				msg = "Loading spaces…"
			} else {
				msg = "No running spaces. Press n to create one."
			}
		}
		b.WriteString(fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width))
		b.WriteString("\n")
		for i := 1; i < lh; i++ {
			b.WriteString(fitCellWidth("", width))
			b.WriteString("\n")
		}
	} else {
		entries := m.buildNavigatorEntries(rows)
		selEntry := navigatorSelectedEntryIdx(entries, m.selected)
		start := windowStart(selEntry, len(entries), lh)
		for i := 0; i < lh; i++ {
			idx := start + i
			if idx >= len(entries) {
				b.WriteString(fitCellWidth("", width))
				b.WriteString("\n")
				continue
			}
			e := entries[idx]
			if e.isSpacer {
				b.WriteString(fitCellWidth("", width))
				b.WriteString("\n")
				continue
			}
			r := rows[e.rowIdx]
			selected := e.rowIdx == m.selected
			if e.subLine == 1 && r.Kind == RowPane {
				sp := m.spaces[r.SpaceIdx]
				p := sp.Panes[r.PaneIdx]
				b.WriteString(m.renderUnifiedPaneActionRow(sp, p, selected, false, false, width))
			} else if e.subLine == 2 && r.Kind == RowPane {
				sp := m.spaces[r.SpaceIdx]
				p := sp.Panes[r.PaneIdx]
				items := m.paneBackgroundItems(sp.Name, p)
				if e.bgIdx >= 0 && e.bgIdx < len(items) {
					b.WriteString(m.renderInlineBackgroundRow(items[e.bgIdx], e.bgLast, selected, false, width))
				} else {
					b.WriteString(fitCellWidth("", width))
				}
			} else {
				b.WriteString(m.rowLineWithStyle(r, selected, false, width))
			}
			b.WriteString("\n")
		}
	}
	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width))
			b.WriteString("\n")
		}
		for _, dl := range m.renderNavigatorDetailLines(width, layout.detailLines) {
			b.WriteString(dl)
			b.WriteString("\n")
		}
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.hintsLine(width))
	return b.String()
}

func (m Model) headerLine(width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	needs := 0
	failed := 0
	working := 0
	done := 0
	for _, sp := range m.spaces {
		needs += sp.Counts.Needs
		failed += sp.Counts.Failed
		working += sp.Counts.Working
		done += sp.Counts.Done
	}
	noun := "spaces"
	if len(m.spaces) == 1 {
		noun = "space"
	}
	segs := []string{st(ColorSubtext0, false).Render(fmt.Sprintf("%d %s", len(m.spaces), noun))}
	if needs > 0 {
		segs = append(segs, st(ColorBlue, true).Render(fmt.Sprintf("● %d needs you", needs)))
	}
	if failed > 0 {
		segs = append(segs, st(ColorRed, true).Render(fmt.Sprintf("✗ %d failed", failed)))
	}
	if working > 0 {
		segs = append(segs, st(ColorYellow, false).Render(fmt.Sprintf("◐ %d working", working)))
	}
	if done > 0 {
		segs = append(segs, st(ColorGreen, false).Render(fmt.Sprintf("✓ %d done", done)))
	}
	return renderHeaderBar("Spaces", segs, m.filter, width)
}

func (m Model) searchLine(width int) string {
	matchText := ""
	if strings.TrimSpace(m.query) != "" {
		rows := m.visibleRows()
		matches := 0
		for _, r := range rows {
			if r.Kind == RowPane || r.Kind == RowExited {
				matches++
			}
		}
		noun := "matches"
		if matches == 1 {
			noun = "match"
		}
		matchText = fmt.Sprintf("%d %s", matches, noun)
	}
	return renderSearchBar(m.query, m.mode == ModeSearch, "Search spaces, agents, branches, paths…", matchText, width)
}

type navigatorListEntry struct {
	rowIdx   int
	subLine  int // 0 = primary row, 1 = optional 2nd action line, 2 = expanded inline background item
	bgIdx    int
	bgLast   bool
	altBG    bool
	isSpacer bool
}

func (m Model) buildNavigatorEntries(rows []Row) []navigatorListEntry {
	if len(rows) == 0 {
		return nil
	}
	entries := make([]navigatorListEntry, 0, len(rows)*2)
	for i, r := range rows {
		switch r.Kind {
		case RowSpace:
			if len(entries) > 0 {
				entries = append(entries, navigatorListEntry{isSpacer: true})
			}
			entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 0})
		case RowPane:
			entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 0})
			sp := m.spaces[r.SpaceIdx]
			p := sp.Panes[r.PaneIdx]
			if paneHasActiveActionLine(sp, p, false, m.opts.HomeDir) {
				entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 1})
			}
			if m.expandedBg != nil && m.expandedBg[paneBgKey(sp.Name, p.ID)] {
				bgItems := m.paneBackgroundItems(sp.Name, p)
				for bi := range bgItems {
					entries = append(entries, navigatorListEntry{
						rowIdx:  i,
						subLine: 2,
						bgIdx:   bi,
						bgLast:  bi == len(bgItems)-1,
					})
				}
			}
		case RowExitedHeader:
			if len(entries) > 0 {
				entries = append(entries, navigatorListEntry{isSpacer: true})
			}
			entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 0})
		case RowExited:
			entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 0})
		default:
			if len(entries) > 0 && r.Kind == RowCreate {
				entries = append(entries, navigatorListEntry{isSpacer: true})
			}
			entries = append(entries, navigatorListEntry{rowIdx: i, subLine: 0})
		}
	}
	return entries
}

func navigatorSelectedEntryIdx(entries []navigatorListEntry, selected int) int {
	for i, e := range entries {
		if !e.isSpacer && e.rowIdx == selected && e.subLine == 0 {
			return i
		}
	}
	return 0
}

func (m Model) rowLine(r Row, selected bool, width int) string {
	return m.rowLineWithStyle(r, selected, false, width)
}

func (m Model) rowLineWithStyle(r Row, selected, altBG bool, width int) string {
	if r.Kind == RowPane {
		sp := m.spaces[r.SpaceIdx]
		p := sp.Panes[r.PaneIdx]
		return m.renderUnifiedPanePrimaryRow(sp, p, selected, altBG, false, width)
	}
	if r.Kind == RowExitedHeader {
		hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0)).Bold(true).Render(
			fmt.Sprintf("  EXITED · %d", len(m.exited)),
		)
		return renderSectionRule(hdr, width)
	}

	bg := ColorBase
	if r.Kind == RowSpace {
		bg = ColorMantle
	}
	if selected {
		bg = ColorSurface0
	}
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	prefix := "  "
	if selected {
		prefix = "▌ "
	}
	innerW := width - 3 // 2-char prefix + 1-char right padding
	if innerW < 10 {
		innerW = 10
	}

	var leftStyled, rightStyled string
	var leftW, rightW int

	switch r.Kind {
	case RowSpace:
		sp := m.spaces[r.SpaceIdx]
		chev := "▾"
		if m.collapsed[sp.Name] {
			chev = "▸"
		}
		var lb strings.Builder
		lb.WriteString(st(ColorOverlay0, false).Render(chev + " "))
		lb.WriteString(st(ColorText, true).Render(sp.Name))
		if sp.IsCurrent {
			lb.WriteString(st(bg, false).Render("  "))
			lb.WriteString(st(ColorLavender, false).Render("current"))
		}
		if wtCount := spaceLinkedWorktreeCount(sp); wtCount > 0 {
			noun := "wt"
			if wtCount > 1 {
				noun = "wts"
			}
			lb.WriteString(st(bg, false).Render("  "))
			lb.WriteString(st(ColorMauve, false).Render(fmt.Sprintf("⎇ %d %s", wtCount, noun)))
		}

		wCount := windowCount(sp)
		pCount := len(sp.Panes)
		var meta string
		if width >= 96 {
			pNoun := "panes"
			if pCount == 1 {
				pNoun = "pane"
			}
			meta = fmt.Sprintf("%d win · %d %s", wCount, pCount, pNoun)
		} else {
			meta = fmt.Sprintf("%dw · %dp", wCount, pCount)
		}
		badges := renderSpaceBadges(sp.Counts, bg, st)
		var baseRB strings.Builder
		if badges != "" {
			baseRB.WriteString(badges)
			baseRB.WriteString(st(bg, false).Render("  "))
		}
		baseRB.WriteString(st(ColorOverlay0, false).Render(meta))
		baseRight := baseRB.String()

		baseLeft := lb.String()
		baseLeftW := ansi.StringWidth(baseLeft)
		baseRightW := ansi.StringWidth(baseRight)
		avail := innerW - baseLeftW - baseRightW - 2

		branchBadge := renderGitBranchBadge(sp.Git, 22, st)
		diffBadge := renderGitDiffBadge(sp.Git, bg, st)
		prBadge := renderGitPRBadge(sp.Git, bg, st)

		var includePR, includeDiff, includeBranch bool
		used := 0
		if prBadge != "" {
			if w := ansi.StringWidth(prBadge) + 2; used+w <= avail {
				includePR = true
				used += w
			}
		}
		if diffBadge != "" {
			if w := ansi.StringWidth(diffBadge) + 2; used+w <= avail {
				includeDiff = true
				used += w
			}
		}
		if branchBadge != "" {
			if w := ansi.StringWidth(branchBadge) + 3; used+w <= avail {
				includeBranch = true
				used += w
			}
		}

		// Group all git context together cleanly right after the space name.
		if includeBranch || includeDiff || includePR {
			lb.WriteString(st(bg, false).Render("   "))
			firstGit := true
			addGit := func(s string) {
				if !firstGit {
					lb.WriteString(st(bg, false).Render("  "))
				}
				lb.WriteString(s)
				firstGit = false
			}
			if includeBranch {
				addGit(branchBadge)
			}
			if includeDiff {
				addGit(diffBadge)
			}
			if includePR {
				addGit(prBadge)
			}
		}
		leftStyled = lb.String()
		leftW = ansi.StringWidth(leftStyled)
		rightStyled = baseRight
		rightW = ansi.StringWidth(rightStyled)

	case RowExited:
		ex := m.exited[r.ExitedIdx]
		age := ""
		if !ex.SavedAt.IsZero() {
			if d := time.Since(ex.SavedAt).Milliseconds(); d > 0 {
				age = formatAge(d) + " ago"
			}
		}
		if age != "" {
			rightStyled = st(ColorOverlay0, false).Render(age)
			rightW = ansi.StringWidth(rightStyled)
		}
		maxLeft := innerW - rightW - 2
		if maxLeft < 10 {
			maxLeft = 10
		}
		var lb strings.Builder
		lb.WriteString(st(ColorOverlay0, false).Render("  ○  "))
		lb.WriteString(st(ColorSubtext0, selected).Render(ex.Name))
		if len(ex.Agents) > 0 {
			agents := strings.Join(ex.Agents, ", ")
			rem := maxLeft - 5 - ansi.StringWidth(ex.Name) - 2
			if rem > 4 {
				lb.WriteString(st(bg, false).Render("  "))
				lb.WriteString(st(ColorOverlay0, false).Render(ansi.Truncate(agents, rem, "…")))
			}
		}
		leftStyled = lb.String()
		leftW = ansi.StringWidth(leftStyled)

	case RowCreate:
		leftStyled = st(ColorGreen, true).Render(fmt.Sprintf("  + Create space %q", r.CreateName))
		leftW = ansi.StringWidth(leftStyled)
	}

	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	if rightW > 0 && leftW+rightW+2 <= innerW {
		out.WriteString(leftStyled)
		out.WriteString(st(bg, false).Render(strings.Repeat(" ", innerW-leftW-rightW)))
		out.WriteString(rightStyled)
	} else if rightW > 0 && innerW > rightW+4 {
		truncLeft := ansi.Truncate(leftStyled, innerW-rightW-2, "…")
		out.WriteString(truncLeft)
		rem := innerW - ansi.StringWidth(truncLeft) - rightW
		if rem < 1 {
			rem = 1
		}
		out.WriteString(st(bg, false).Render(strings.Repeat(" ", rem)))
		out.WriteString(rightStyled)
	} else {
		out.WriteString(ansi.Truncate(leftStyled, innerW, "…"))
	}

	if selected || bg != ColorBase {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

// paneShowsGit reports whether a pane should display git badges directly on its
// row. In Inbox (showSpaceCol == true) every pane shows its own git context; in
// Spaces, child panes omit redundant git badges that are already displayed
// identically on their parent Space header row.
func paneShowsGit(sp model.Space, p model.Pane, showSpaceCol bool) bool {
	if p.Git == nil {
		return false
	}
	if showSpaceCol {
		return true
	}
	return !gitInfoEquivalent(sp.Git, p.Git)
}

// paneHasActiveActionLine reports whether an agent pane is actively working,
// blocked, or failed and has a live action/blocker/metadata line to display on
// the optional 2nd card line.
func paneHasActiveActionLine(sp model.Space, p model.Pane, showSpaceCol bool, homeDir string) bool {
	if !p.IsAgent() {
		return false
	}
	if p.Status != model.StatusNeedsYou && p.Status != model.StatusFailed && p.Status != model.StatusWorking {
		return false
	}
	primary := paneConversationName(p)
	if primary == "" {
		primary = p.WindowName
	}
	action, _ := inboxCardActionText(p, primary)
	if action != "" || shortenPath(p.CWD, homeDir) != "" || paneShowsGit(sp, p, showSpaceCol) {
		return true
	}
	return p.Hook != nil && strings.TrimSpace(p.Hook.Model) != ""
}

// formatAppLabel formats the coding harness name alongside its profile using
// the same em-dash convention as the tmux pane top border (`app — profile`).
func formatAppLabel(app, profile string) string {
	app = strings.TrimSpace(app)
	profile = strings.TrimSpace(profile)
	if app != "" && profile != "" {
		return app + " — " + profile
	}
	if app != "" {
		return app
	}
	return profile
}

// renderStyledAppCol renders `<app>` (and optional ` — <profile>` with subtle
// mauve profile accent), truncated to maxW and right-padded to colW.
func renderStyledAppCol(app, profile string, colW, maxW int, selected bool, bg string, st cellStyleFn) string {
	label := truncate(formatAppLabel(app, profile), maxW)
	appFg := ColorSubtext0
	if selected {
		appFg = ColorText
	}
	var b strings.Builder
	if idx := strings.Index(label, " — "); idx >= 0 {
		left := label[:idx]
		right := label[idx+len(" — "):]
		b.WriteString(st(appFg, selected).Render(left))
		b.WriteString(st(ColorOverlay0, false).Render(" — "))
		b.WriteString(st(ColorMauve, selected).Render(right))
	} else {
		b.WriteString(st(appFg, selected).Render(label))
	}
	if w := ansi.StringWidth(label); w < colW {
		b.WriteString(st(bg, false).Render(strings.Repeat(" ", colW-w)))
	}
	return b.String()
}

// maxAppLabelWidth returns the maximum visual width for an `<app> — <profile>`
// column at the given popup width.
func maxAppLabelWidth(width int) int {
	switch {
	case width >= 92:
		return 24
	case width >= 68:
		return 20
	default:
		return 14
	}
}

// appColumnWidth computes a tight, consistent column width for the app (and
// optional profile) column across visible rows so Spaces and Inbox columns stay
// vertically aligned without excessive empty padding. In Inbox mode
// (showSpaceCol == true), profiles are omitted and the baseline width is
// len("antigravity") (11) so all harness labels align cleanly.
func (m Model) appColumnWidth(showSpaceCol bool, maxW int) int {
	colW := 10
	if showSpaceCol {
		colW = len("antigravity")
	}
	consider := func(app, profile string) {
		if showSpaceCol {
			profile = ""
		}
		if w := ansi.StringWidth(truncate(formatAppLabel(app, profile), maxW)); w > colW {
			colW = w
		}
	}
	for _, sp := range m.spaces {
		for _, p := range sp.Panes {
			if showSpaceCol && !p.IsAgent() {
				continue
			}
			consider(p.App, p.Profile)
		}
	}
	if showSpaceCol {
		for _, o := range m.outcomes {
			baseApp := o.DisplayApp()
			if baseApp == "" {
				baseApp = "agent"
			}
			consider(baseApp, o.Profile)
		}
	}
	if colW > maxW {
		return maxW
	}
	return colW
}

// spaceColumnWidth computes a tight, consistent column width for space names in
// the Attention Inbox so short workspace names do not leave excessive whitespace.
func (m Model) spaceColumnWidth(maxW int) int {
	colW := 8
	for _, sp := range m.spaces {
		if w := ansi.StringWidth(truncate(sp.Name, maxW)); w > colW {
			colW = w
		}
	}
	for _, o := range m.outcomes {
		if s := strings.TrimSpace(o.Session); s != "" {
			if w := ansi.StringWidth(truncate(s, maxW)); w > colW {
				colW = w
			}
		}
	}
	if colW > maxW {
		return maxW
	}
	return colW
}

// renderStatusTail renders the fixed-width right-aligned status + age tail so
// every row aligns along the same vertical columns.
func renderStatusTail(status model.Status, ageStr string, isOutcome bool, bg string, st cellStyleFn) string {
	var b strings.Builder
	if isOutcome {
		b.WriteString(st(ColorOverlay0, false).Render("↺ "))
	}
	label := status.Label()
	if w := ansi.StringWidth(label); w < statusColWidth {
		label += strings.Repeat(" ", statusColWidth-w)
	}
	b.WriteString(st(StatusColor(status), status.IsAttention()).Render(label))
	if ageStr != "" {
		b.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf(" %3s", ageStr)))
	} else {
		b.WriteString(st(bg, false).Render("    "))
	}
	return b.String()
}

// renderInboxAgeTail renders the right-aligned age tail for Inbox rows without
// repeating the section status label.
func renderInboxAgeTail(ageStr string, isOutcome bool, bg string, st cellStyleFn) string {
	var b strings.Builder
	if isOutcome {
		b.WriteString(st(ColorOverlay0, false).Render("↺ "))
	} else {
		b.WriteString(st(bg, false).Render("  "))
	}
	if ageStr != "" {
		b.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%3s", ageStr)))
	} else {
		b.WriteString(st(bg, false).Render("   "))
	}
	return b.String()
}

// renderUnifiedPanePrimaryRow renders Line 1 of a pane row shared by Spaces and Inbox:
//
//	Spaces: Left  `▌   <icon>  <App [— Profile]>  <Conversation Title>`
//	        Right `[1-line only: git/model]  [▸ bg badge]  <status (fixed)> <age (fixed)>`
//	Inbox:  Left  `▌   <icon>  <Conversation Title>`
//	        Right `<Space>  <App>  [1-line only: git/model]  [▸ bg badge]  <age (fixed)>`
//
// When a pane has an active 2nd line (Working / NeedsYou / Failed), git and
// model move cleanly onto Line 2 so Line 1 stays calm and titles never crush.
func (m Model) renderUnifiedPanePrimaryRow(sp model.Space, p model.Pane, selected, altBG, showSpaceCol bool, width int) string {
	_ = altBG
	bg := ColorBase
	if selected {
		bg = ColorSurface0
	}
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	prefix := "  "
	if selected {
		prefix = "▌ "
	}
	innerW := width - 3 // 2-char prefix + 1-char right padding
	if innerW < 10 {
		innerW = 10
	}

	conv := paneConversationName(p)
	if showSpaceCol && conv == "" {
		conv = inboxAgentTitle(p, m.opts.HomeDir)
	}
	maxAppW := maxAppLabelWidth(width)
	appColW := m.appColumnWidth(showSpaceCol, maxAppW)
	spaceColW := 0
	if showSpaceCol {
		spaceColW = m.spaceColumnWidth(14)
	}
	hasActionLine := paneHasActiveActionLine(sp, p, showSpaceCol, m.opts.HomeDir)

	// Mandatory fixed-width right tail: status + age (Spaces) or age only (Inbox).
	ageStr := ""
	if p.AgeMs > 0 {
		ageStr = formatAge(p.AgeMs)
	}
	var tailStyled string
	if showSpaceCol {
		tailStyled = renderInboxAgeTail(ageStr, false, bg, st)
	} else {
		tailStyled = renderStatusTail(p.Status, ageStr, false, bg, st)
	}
	tailW := ansi.StringWidth(tailStyled)

	// In Inbox mode, Space + Harness sit on the right side (in fixed columns)
	// while the thread title takes the primary left column.
	var inboxMetaStyled string
	inboxMetaW := 0
	if showSpaceCol {
		spaceCol := truncate(sp.Name, spaceColW)
		if sw := ansi.StringWidth(spaceCol); sw < spaceColW {
			spaceCol += strings.Repeat(" ", spaceColW-sw)
		}
		spaceFg := ColorSubtext0
		if sp.IsCurrent {
			spaceFg = ColorLavender
		}
		var mb strings.Builder
		mb.WriteString(st(spaceFg, selected || sp.IsCurrent).Render(spaceCol))
		mb.WriteString(st(bg, false).Render("  "))
		mb.WriteString(renderStyledAppCol(p.App, "", appColW, maxAppW, selected, bg, st))
		inboxMetaStyled = mb.String()
		inboxMetaW = ansi.StringWidth(inboxMetaStyled) + 2
	}

	// Minimum left width: "  ●  " (5) + primary content.
	const indentAndIconW = 5
	minLeftW := indentAndIconW
	if !showSpaceCol {
		minLeftW += appColW
	}
	if conv != "" {
		cw := ansi.StringWidth(conv)
		minTitle := 22
		if width < 90 {
			minTitle = 16
		}
		if cw > minTitle {
			cw = minTitle
		}
		if showSpaceCol {
			minLeftW += cw
		} else {
			minLeftW += 2 + cw
		}
	}
	availRight := innerW - minLeftW - inboxMetaW - tailW - 2
	if availRight < 0 {
		availRight = 0
	}

	// Optional right-aligned badges in display order (left-to-right):
	// 0: branch, 1: diff, 2: pr/ci, 3: model, 4: bg disclosure badge.
	// Active 2-line panes defer git & model to Line 2 so Line 1 remains uncluttered.
	var slots [5]string
	if !hasActionLine {
		if paneShowsGit(sp, p, showSpaceCol) {
			slots[0] = renderGitBranchBadge(p.Git, 16, st)
			slots[1] = renderGitDiffBadge(p.Git, bg, st)
			slots[2] = renderGitPRBadge(p.Git, bg, st)
		}
		if p.Hook != nil && strings.TrimSpace(p.Hook.Model) != "" && innerW >= 72 {
			slots[3] = st(ColorSubtext0, false).Render(truncate(strings.TrimSpace(p.Hook.Model), 20))
		}
	}
	if p.Hook != nil && innerW >= 54 {
		slots[4] = m.formatBackgroundBadge(sp, p, st)
	}

	// Priority: background toggle > PR/CI > diff > branch > model.
	var included [5]bool
	usedRight := 0
	for _, idx := range []int{4, 2, 1, 0, 3} {
		if slots[idx] == "" {
			continue
		}
		w := ansi.StringWidth(slots[idx]) + 2
		if usedRight+w <= availRight {
			included[idx] = true
			usedRight += w
		}
	}

	var rb strings.Builder
	gap := st(bg, false).Render("  ")
	for i := 0; i < len(slots); i++ {
		if included[i] {
			rb.WriteString(slots[i])
			rb.WriteString(gap)
		}
	}
	if showSpaceCol && inboxMetaStyled != "" {
		rb.WriteString(inboxMetaStyled)
		rb.WriteString(gap)
	}
	rb.WriteString(tailStyled)
	rightStyled := rb.String()
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW - rightW - 2
	if maxLeft < 10 {
		maxLeft = 10
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(st(StatusColor(p.Status), p.Status != model.StatusIdle).Render(p.Status.Icon()))
	lb.WriteString(st(bg, false).Render("  "))

	if showSpaceCol {
		titleBudget := maxLeft - indentAndIconW
		if conv != "" && titleBudget > 3 {
			titleBold := selected || p.Status.IsAttention()
			lb.WriteString(st(ColorText, titleBold).Render(ansi.Truncate(conv, titleBudget, "…")))
		}
	} else if conv != "" {
		lb.WriteString(renderStyledAppCol(p.App, p.Profile, appColW, maxAppW, selected, bg, st))
		usedW := indentAndIconW + appColW
		titleBudget := maxLeft - usedW - 2
		if titleBudget > 3 {
			lb.WriteString(st(bg, false).Render("  "))
			titleBold := selected || p.Status.IsAttention()
			lb.WriteString(st(ColorText, titleBold).Render(ansi.Truncate(conv, titleBudget, "…")))
		}
	} else {
		lb.WriteString(renderStyledAppCol(p.App, p.Profile, 0, maxLeft-indentAndIconW, selected, bg, st))
	}
	leftStyled := lb.String()
	leftW := ansi.StringWidth(leftStyled)

	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	if rightW > 0 && leftW+rightW+2 <= innerW {
		out.WriteString(leftStyled)
		out.WriteString(st(bg, false).Render(strings.Repeat(" ", innerW-leftW-rightW)))
		out.WriteString(rightStyled)
	} else if rightW > 0 && innerW > rightW+4 {
		truncLeft := ansi.Truncate(leftStyled, innerW-rightW-2, "…")
		out.WriteString(truncLeft)
		rem := innerW - ansi.StringWidth(truncLeft) - rightW
		if rem < 1 {
			rem = 1
		}
		out.WriteString(st(bg, false).Render(strings.Repeat(" ", rem)))
		out.WriteString(rightStyled)
	} else {
		out.WriteString(ansi.Truncate(leftStyled, innerW, "…"))
	}

	if selected {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

// renderUnifiedPaneActionRow renders the optional 2nd line of an active/blocked
// agent card in both the Spaces popup and the Inbox popup:
//
//	Left:  `▌      ↳ <live action/tool/prompt>` (or `<window> (<pane_id>)` when idle of action text)
//	Right: `[git branch · diff · pr/ci] · [model] · [loc] · [shortened CWD]`
func (m Model) renderUnifiedPaneActionRow(sp model.Space, p model.Pane, selected, altBG, showSpaceCol bool, width int) string {
	_ = altBG
	primaryTitle := paneConversationName(p)
	if primaryTitle == "" {
		primaryTitle = inboxAgentTitle(p, m.opts.HomeDir)
	}

	bg := ColorBase
	if selected {
		bg = ColorSurface0
	}
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	prefix := "  "
	if selected {
		prefix = "▌ "
	}
	innerW := width - 3 // 2-char prefix + 1-char right padding
	if innerW < 10 {
		innerW = 10
	}

	actionText, actionColor := inboxCardActionText(p, primaryTitle)

	win := strings.TrimSpace(p.WindowName)
	var loc string
	if win != "" && !strings.EqualFold(win, p.App) && !strings.EqualFold(win, primaryTitle) && !isGenericShellName(win) {
		loc = truncate(win, 16) + " (" + p.ID + ")"
	} else if p.ID != "" {
		loc = p.ID
	}

	const leftPrefixW = 7 // "     ↳ "
	leftMinText := 0
	if actionText != "" {
		aw := ansi.StringWidth(actionText)
		if aw > 26 {
			aw = 26
		}
		leftMinText = aw
	} else if loc != "" {
		leftMinText = ansi.StringWidth(loc)
	}
	availRight := innerW - leftPrefixW - leftMinText - 3
	if availRight < 0 {
		availRight = 0
	}

	// Right-side contextual metadata slots (display order left-to-right):
	// 0: git branch, 1: git diff, 2: git pr/ci, 3: model, 4: loc, 5: cwd.
	var slots [6]string
	if paneShowsGit(sp, p, showSpaceCol) {
		slots[0] = renderGitBranchBadge(p.Git, 18, st)
		slots[1] = renderGitDiffBadge(p.Git, bg, st)
		slots[2] = renderGitPRBadge(p.Git, bg, st)
	}
	if p.Hook != nil && strings.TrimSpace(p.Hook.Model) != "" {
		slots[3] = st(ColorSubtext0, false).Render(truncate(strings.TrimSpace(p.Hook.Model), 20))
	}
	if actionText != "" && loc != "" {
		slots[4] = st(ColorOverlay0, false).Render(loc)
	}
	if cwdStr := shortenPath(p.CWD, m.opts.HomeDir); cwdStr != "" && innerW >= 54 {
		maxCWD := innerW / 3
		if maxCWD > 28 {
			maxCWD = 28
		}
		if maxCWD < 14 {
			maxCWD = 14
		}
		slots[5] = st(ColorOverlay0, false).Render(truncate(cwdStr, maxCWD))
	}

	// Select right metadata by priority: PR/CI > diff > branch > model > cwd > loc.
	var included [6]bool
	usedRight := 0
	countRight := 0
	for _, idx := range []int{2, 1, 0, 3, 5, 4} {
		if slots[idx] == "" {
			continue
		}
		sepW := 0
		if countRight > 0 {
			// Git slots (0,1,2) separate with 2 spaces; distinct metadata groups separate with " · ".
			sepW = 3
		}
		w := ansi.StringWidth(slots[idx]) + sepW
		if usedRight+w <= availRight {
			included[idx] = true
			usedRight += w
			countRight++
		}
	}

	dotSep := st(ColorSurface1, false).Render(" · ")
	gitGap := st(bg, false).Render("  ")
	var rb strings.Builder
	// Group git badges together as one cluster, then model / loc / cwd separated by " · ".
	var gitParts []string
	for i := 0; i <= 2; i++ {
		if included[i] {
			gitParts = append(gitParts, slots[i])
		}
	}
	var groups []string
	if len(gitParts) > 0 {
		groups = append(groups, strings.Join(gitParts, gitGap))
	}
	for i := 3; i <= 5; i++ {
		if included[i] {
			groups = append(groups, slots[i])
		}
	}
	if len(groups) > 0 {
		rb.WriteString(strings.Join(groups, dotSep))
	}
	rightStyled := rb.String()
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW
	if rightW > 0 {
		maxLeft = innerW - rightW - 2
	}
	if maxLeft < 10 {
		maxLeft = 10
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("     "))
	lb.WriteString(st(ColorSurface1, false).Render("↳ "))
	rem := maxLeft - leftPrefixW
	if rem > 4 {
		if actionText != "" {
			lb.WriteString(st(actionColor, p.Status.IsAttention()).Render(ansi.Truncate(actionText, rem, "…")))
		} else if loc != "" {
			lb.WriteString(st(ColorOverlay0, false).Render(ansi.Truncate(loc, rem, "…")))
		}
	}

	leftStyled := ansi.Truncate(lb.String(), maxLeft, "…")
	leftW := ansi.StringWidth(leftStyled)

	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	if rightW > 0 && leftW+rightW+2 <= innerW {
		out.WriteString(leftStyled)
		out.WriteString(st(bg, false).Render(strings.Repeat(" ", innerW-leftW-rightW)))
		out.WriteString(rightStyled)
	} else {
		out.WriteString(ansi.Truncate(leftStyled, innerW, "…"))
	}

	if selected {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

func spaceBadges(c model.Counts) string {
	var parts []string
	if c.Needs > 0 {
		parts = append(parts, fmt.Sprintf("●%d", c.Needs))
	}
	if c.Failed > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", c.Failed))
	}
	if c.Working > 0 {
		parts = append(parts, fmt.Sprintf("◐%d", c.Working))
	}
	if c.Done > 0 {
		parts = append(parts, fmt.Sprintf("✓%d", c.Done))
	}
	return strings.Join(parts, " ")
}

func renderSpaceBadges(c model.Counts, bg string, st cellStyleFn) string {
	var parts []string
	if c.Needs > 0 {
		parts = append(parts, st(ColorBlue, true).Render(fmt.Sprintf("●%d", c.Needs)))
	}
	if c.Failed > 0 {
		parts = append(parts, st(ColorRed, true).Render(fmt.Sprintf("✗%d", c.Failed)))
	}
	if c.Working > 0 {
		parts = append(parts, st(ColorYellow, true).Render(fmt.Sprintf("◐%d", c.Working)))
	}
	if c.Done > 0 {
		parts = append(parts, st(ColorGreen, true).Render(fmt.Sprintf("✓%d", c.Done)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, st(bg, false).Render(" "))
}

func windowCount(sp model.Space) int {
	if len(sp.Windows) > 0 {
		return len(sp.Windows)
	}
	seen := map[string]bool{}
	for _, p := range sp.Panes {
		seen[p.WindowID] = true
	}
	if len(seen) == 0 {
		return 1
	}
	return len(seen)
}

// paneConversationName returns the name of the open harness conversation for a
// pane row following the 4-tier priority order:
//  1. Hooks / local harness session store (p.ConversationTitle / p.Hook.Title)
//  2. Cleaned pane OSC title (p.Title)
//  3. Screen-reader viewport title (p.ScreenTitle)
//  4. Distinct tmux window name (p.WindowName)
//
// If none of those resolve, it falls back to the hook/resume session ID.
// It never returns the live action/tool text.
func paneConversationName(p model.Pane) string {
	// 1. Hooks / local session store.
	if title := cleanHookOrScreenTitle(p.ConversationTitle, p.App, p.Command, p.CWD); title != "" {
		return title
	}
	if p.Hook != nil {
		if title := cleanHookOrScreenTitle(p.Hook.Title, p.App, p.Command, p.CWD); title != "" {
			return title
		}
	}
	// 2. Pane OSC title.
	if title := cleanConversationTitle(p.Title, p.App, p.Command, p.CWD); title != "" {
		return title
	}
	// 3. Screen reader title.
	if title := cleanHookOrScreenTitle(p.ScreenTitle, p.App, p.Command, p.CWD); title != "" {
		return title
	}
	// 4. Distinct tmux window name.
	win := strings.TrimSpace(p.WindowName)
	if win != "" &&
		!strings.EqualFold(win, p.App) &&
		!strings.EqualFold(win, p.Command) &&
		!isPathOrCWDBasename(win, p.CWD) &&
		!isHarnessLaunchCommand(win) &&
		!isGenericShellName(win) {
		return win
	}
	// Final fallback: session ID.
	if p.Hook != nil {
		if sid := strings.TrimSpace(p.Hook.SessionID); sid != "" {
			return sid
		}
	}
	if sid := strings.TrimSpace(p.ResumeSession); sid != "" {
		return sid
	}
	return ""
}

func cleanHookOrScreenTitle(raw, app, cmd, cwd string) string {
	t := stripTitleDecorations(raw)
	if t == "" {
		return ""
	}
	if strings.EqualFold(t, app) || strings.EqualFold(t, cmd) || isGenericShellName(t) {
		return ""
	}
	if isHarnessLaunchCommand(t) || isVersionBanner(t, app, cmd) {
		return ""
	}
	return t
}

func cleanConversationTitle(raw, app, cmd, cwd string) string {
	t := stripTitleDecorations(raw)
	if t == "" {
		return ""
	}
	// Strip trailing " | <cwd_basename>" suffix appended by some harnesses
	// (e.g. "Remove extra T3Code providers | dots" or "fx v0.0.15 | arhammahajan").
	if pipe := strings.LastIndex(t, " | "); pipe > 0 {
		right := strings.TrimSpace(t[pipe+3:])
		if isPathOrCWDBasename(right, cwd) {
			t = strings.TrimSpace(t[:pipe])
		}
	}
	if t == "" {
		return ""
	}
	if strings.EqualFold(t, app) || strings.EqualFold(t, cmd) || isGenericShellName(t) {
		return ""
	}
	if isPathOrCWDBasename(t, cwd) || isHarnessLaunchCommand(t) || isVersionBanner(t, app, cmd) {
		return ""
	}
	// Ignore default shell titles like "user@host:/path" or bare paths.
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~/") || strings.EqualFold(t, "~") {
		return ""
	}
	if at := strings.IndexByte(t, '@'); at > 0 {
		if colon := strings.IndexByte(t[at:], ':'); colon > 1 && !strings.ContainsAny(t[:at+colon], " \t") {
			return ""
		}
	}
	return t
}

func stripTitleDecorations(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		return ""
	}
	// Strip leading spinner / status glyphs harnesses prepend to OSC titles.
	for {
		trimmed := strings.TrimLeft(t, "✳⠐⠂⠁⠈⠠⠄⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏●◐✓✗○◌• ")
		if trimmed == t {
			break
		}
		t = trimmed
	}
	for _, prefix := range agent.DefaultRegistry().AllTitlePrefixes() {
		if len(t) > len(prefix) && strings.EqualFold(t[:len(prefix)], prefix) {
			t = strings.TrimSpace(t[len(prefix):])
		}
	}
	return strings.TrimSpace(t)
}

func isPathOrCWDBasename(s, cwd string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || s == "~" {
		return true
	}
	cwd = strings.TrimRight(strings.TrimSpace(cwd), "/")
	if cwd == "" {
		return false
	}
	if strings.EqualFold(s, cwd) {
		return true
	}
	base := cwd
	if slash := strings.LastIndexByte(cwd, '/'); slash >= 0 {
		base = cwd[slash+1:]
	}
	return base != "" && strings.EqualFold(s, base)
}

func isHarnessLaunchCommand(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{
		"harness ",
		"tmux-spaces ",
		"zellij-spaces ",
		"spaces ",
		"zj ",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isVersionBanner(s, app, cmd string) bool {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) != 2 {
		return false
	}
	first, second := strings.ToLower(fields[0]), strings.ToLower(fields[1])
	if !strings.HasPrefix(second, "v") || len(second) < 2 || second[1] < '0' || second[1] > '9' {
		return false
	}
	if strings.EqualFold(first, app) || strings.EqualFold(first, cmd) {
		return true
	}
	return agent.DefaultRegistry().IsVersionBannerName(first)
}

func isGenericShellName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sh", "bash", "zsh", "fish", "dash", "nu", "ksh", "tmux", "shell", "terminal":
		return true
	}
	return false
}

func renderPaneDetailLines(sp model.Space, p model.Pane, homeDir string, width, maxLines int) []string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	sep := st(ColorSurface1, false).Render(" · ")

	win := p.WindowName
	if win == "" {
		win = p.WindowID
	}
	loc := win
	if p.ID != "" {
		if loc != "" {
			loc += " (" + p.ID + ")"
		} else {
			loc = p.ID
		}
	}

	// Line 1: identity + status + runtime metadata.
	var l1 []string
	idPart := st(ColorLavender, true).Render(sp.Name)
	if loc != "" {
		idPart += st(ColorSurface1, false).Render(" › ") + st(ColorSubtext0, false).Render(loc)
	}
	l1 = append(l1, idPart)
	if appLabel := formatAppLabel(p.App, p.Profile); appLabel != "" {
		l1 = append(l1, renderStyledAppCol(p.App, p.Profile, 0, 32, false, bg, st))
	}
	statusPart := st(StatusColor(p.Status), p.Status.IsAttention()).Render(p.Status.Icon() + " " + p.Status.Label())
	if p.AgeMs > 0 {
		statusPart += st(ColorOverlay0, false).Render(" " + formatAge(p.AgeMs))
	}
	l1 = append(l1, statusPart)
	if p.Hook != nil && strings.TrimSpace(p.Hook.Model) != "" {
		l1 = append(l1, st(ColorSubtext0, false).Render("model "+strings.TrimSpace(p.Hook.Model)))
	}
	if cwd := shortenPath(p.CWD, homeDir); cwd != "" {
		l1 = append(l1, st(ColorOverlay0, false).Render(cwd))
	}
	if p.Hook != nil && strings.TrimSpace(p.Hook.SessionID) != "" {
		l1 = append(l1, st(ColorOverlay0, false).Render("session "+strings.TrimSpace(p.Hook.SessionID)))
	}

	// Line 2: git / PR / CI + background work + deduplicated live action/text.
	var l2 []string
	if gitStyled := renderGitDetailStyled(p.Git, bg, st); gitStyled != "" {
		l2 = append(l2, gitStyled)
	}
	text := strings.TrimSpace(p.Text)
	if text == "" && p.Hook != nil {
		text = strings.TrimSpace(p.Hook.Text)
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" && p.Hook != nil {
		kind = strings.TrimSpace(p.Hook.Kind)
	}
	waitingOn := ""
	if p.Hook != nil {
		waitingOn = strings.TrimSpace(p.Hook.WaitingOn)
		if bgSum := formatDetailBackgroundSummary(p.Hook); bgSum != "" {
			// Avoid duplicating waitingOn when text already equals waitingOn.
			if waitingOn != "" && strings.EqualFold(text, waitingOn) {
				text = ""
			}
			l2 = append(l2, st(ColorBlue, false).Render(bgSum))
		}
	}
	if kind != "" && text != "" {
		l2 = append(l2, st(StatusColor(p.Status), p.Status.IsAttention()).Render(kind+": "+text))
	} else if kind != "" {
		l2 = append(l2, st(StatusColor(p.Status), p.Status.IsAttention()).Render(kind))
	} else if text != "" {
		l2 = append(l2, st(ColorText, false).Render(text))
	}

	padFooter := func(s string) string {
		line := st(bg, false).Render(" ") + s
		if width > 2 && ansi.StringWidth(line) > width-1 {
			line = ansi.Truncate(line, width-1, "…")
		}
		return fitCellWidthWithBG(line, width, bg)
	}

	if maxLines <= 1 {
		combined := append(l1, l2...)
		return []string{padFooter(strings.Join(combined, sep))}
	}
	if len(l2) == 0 {
		l2 = append(l2, st(ColorOverlay0, false).Render("Enter focus pane · H hook details · e expand background"))
	}
	return []string{
		padFooter(strings.Join(l1, sep)),
		padFooter(strings.Join(l2, sep)),
	}
}

func (m Model) renderNavigatorDetailLines(width, maxLines int) []string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	sep := st(ColorSurface1, false).Render(" · ")
	padLine := func(s string) string {
		line := st(bg, false).Render(" ") + s
		if width > 2 && ansi.StringWidth(line) > width-1 {
			line = ansi.Truncate(line, width-1, "…")
		}
		return fitCellWidthWithBG(line, width, bg)
	}

	if r := m.selectedRow(); r != nil {
		switch r.Kind {
		case RowSpace:
			sp := m.spaces[r.SpaceIdx]
			cwd := sp.CWD
			if cwd == "" {
				cwd = "-"
			}
			var l1 []string
			l1 = append(l1, st(ColorText, true).Render(sp.Name))
			if sp.IsCurrent {
				l1 = append(l1, st(ColorLavender, false).Render("current space"))
			}
			l1 = append(l1, st(ColorSubtext0, false).Render(shortenPath(cwd, m.opts.HomeDir)))
			l1 = append(l1, st(ColorOverlay0, false).Render(fmt.Sprintf("%d windows · %d panes", windowCount(sp), len(sp.Panes))))
			if badges := renderSpaceBadges(sp.Counts, bg, st); badges != "" {
				l1 = append(l1, badges)
			} else {
				l1 = append(l1, st(StatusColor(sp.Status), false).Render(sp.Status.Label()))
			}
			gitStyled := renderGitDetailStyled(sp.Git, bg, st)
			if maxLines <= 1 {
				if gitStyled != "" {
					l1 = append(l1, gitStyled)
				}
				return []string{padLine(strings.Join(l1, sep))}
			}
			l2 := gitStyled
			if l2 == "" {
				l2 = st(ColorOverlay0, false).Render("Enter switch space · Space fold · n new · r rename · x close")
			}
			return []string{padLine(strings.Join(l1, sep)), padLine(l2)}

		case RowPane:
			sp := m.spaces[r.SpaceIdx]
			p := sp.Panes[r.PaneIdx]
			return renderPaneDetailLines(sp, p, m.opts.HomeDir, width, maxLines)

		case RowExited:
			if r.ExitedIdx < len(m.exited) {
				ex := m.exited[r.ExitedIdx]
				cwd := ex.CWD
				if cwd == "" {
					cwd = "-"
				}
				var l1 []string
				l1 = append(l1, st(ColorText, true).Render(ex.Name))
				l1 = append(l1, st(ColorSubtext0, false).Render(shortenPath(cwd, m.opts.HomeDir)))
				l1 = append(l1, st(ColorOverlay0, false).Render(fmt.Sprintf("%d windows · %d panes", ex.WindowCount, ex.PaneCount)))
				if len(ex.Agents) > 0 {
					l1 = append(l1, st(ColorSubtext0, false).Render("agents: "+strings.Join(ex.Agents, ", ")))
				}
				hint := st(ColorOverlay0, false).Render("Enter restores saved space · x deletes snapshot")
				if maxLines <= 1 {
					l1 = append(l1, hint)
					return []string{padLine(strings.Join(l1, sep))}
				}
				return []string{padLine(strings.Join(l1, sep)), padLine(hint)}
			}

		case RowCreate:
			l1 := st(ColorGreen, true).Render("Create space "+r.CreateName) + sep + st(ColorOverlay0, false).Render("Enter opens the New Space dialog")
			if maxLines <= 1 {
				return []string{padLine(l1)}
			}
			return []string{padLine(l1), padLine(st(ColorOverlay0, false).Render("Esc clears search"))}
		}
	}

	empty := st(ColorOverlay0, false).Render("No spaces yet. Press n to create one.")
	if maxLines <= 1 {
		return []string{padLine(empty)}
	}
	return []string{padLine(empty), padLine("")}
}

func (m Model) detailLine(width int) string {
	return strings.Join(m.renderNavigatorDetailLines(width, 2), "\n")
}

func (m Model) hintsLine(width int) string {
	switch m.mode {
	case ModeSearch:
		return renderHintsBar([][2]string{
			{"type", "filter"},
			{"↑↓", "move"},
			{"Enter", "open"},
			{"Esc", "clear"},
		}, width)
	case ModeHelp:
		return renderHintsBar([][2]string{{"Esc", "close help"}}, width)
	case ModeHookDetail:
		return renderHintsBar([][2]string{
			{"↑↓", "scroll"},
			{"PgUp/PgDn", "page"},
			{"H/Esc", "close"},
		}, width)
	case ModeNewSpace, ModeRename:
		return renderHintsBar([][2]string{
			{"Tab", "next field"},
			{"Enter", "save"},
			{"Esc", "cancel"},
		}, width)
	case ModeConfirmKill:
		return renderHintsBar([][2]string{
			{"Enter", "confirm"},
			{"Esc", "cancel"},
		}, width)
	default:
		if width < 72 {
			return renderHintsBar([][2]string{
				{"↑↓", "move"},
				{"Enter", "open"},
				{"/", "search"},
				{"n", "new"},
				{"Esc", "close"},
			}, width)
		}
		return renderHintsBar([][2]string{
			{"↑↓", "move"},
			{"Enter", "open"},
			{"/", "search"},
			{"n", "new"},
			{"W", "worktrees"},
			{"r", "rename"},
			{"x", "close"},
			{"Space", "fold"},
			{"e", "expand"},
			{"H", "hooks"},
			{"?", "keys"},
			{"Esc", "close"},
		}, width)
	}
}

// --- dialogs ---

func (m Model) classicNewSpaceDialog(width int) string {
	var b strings.Builder
	if m.newIsTab {
		title := "New tab"
		if m.newTabSession != "" {
			title = "New tab · " + m.newTabSession
		}
		b.WriteString(styleAccent.Render(title))
	} else {
		b.WriteString(styleAccent.Render("New space"))
	}
	b.WriteString("\n")
	nameCaret := ""
	cwdCaret := ""
	if m.newField == 0 {
		nameCaret = "_"
	} else {
		cwdCaret = "_"
	}
	b.WriteString(fmt.Sprintf("Name: %s%s\n", m.newName, nameCaret))
	b.WriteString(fmt.Sprintf("Folder: %s%s\n", m.newCWD, cwdCaret))
	if m.newError != "" {
		b.WriteString(styleError.Render(m.newError))
		b.WriteString("\n")
	} else if m.newIsTab {
		b.WriteString(styleMuted.Render("Leave name blank to use the default tab name."))
		b.WriteString("\n")
	} else {
		b.WriteString(styleMuted.Render("The folder is the starting directory."))
		b.WriteString("\n")
	}
	b.WriteString(styleMuted.Render("Tab next · Enter create · Esc cancel"))
	return styleDialogBox.Render(b.String())
}

func (m Model) renameBottomBar(width int) string {
	if width <= 0 {
		width = 80
	}
	bg := lipgloss.Color(ColorMantle)
	labelStyle := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	textStyle := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorText))
	hintStyle := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorOverlay0))
	errStyle := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color(ColorRed)).Bold(true)
	padStyle := lipgloss.NewStyle().Background(bg)

	label := " Rename space: "
	if m.renameWindow {
		label = " Rename tab: "
	}
	left := labelStyle.Render(label) + textStyle.Render(m.renameText+"_")
	if m.renameError != "" {
		left += errStyle.Render("  " + m.renameError)
	}
	right := hintStyle.Render("Enter save · Esc cancel ")
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	if leftW+rightW+1 <= width {
		gap := padStyle.Render(strings.Repeat(" ", width-leftW-rightW))
		return left + gap + right
	}
	if leftW < width {
		return left + padStyle.Render(strings.Repeat(" ", width-leftW))
	}
	return left
}

func (m Model) renameDialog(width int) string {
	var b strings.Builder
	title := "Rename space"
	if m.renameWindow {
		title = "Rename tab"
	}
	b.WriteString(styleAccent.Render(title))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Name: %s_\n", m.renameText))
	if m.renameError != "" {
		b.WriteString(styleError.Render(m.renameError))
		b.WriteString("\n")
	}
	b.WriteString(styleMuted.Render("Enter save · Esc cancel"))
	return styleDialogBox.Render(b.String())
}

func (m Model) appendConfirmWorktreeOption(b *strings.Builder) bool {
	if m.confirmWorktreePath == "" {
		return false
	}
	box := "[ ]"
	if m.confirmRemoveWorktree {
		box = "[x]"
	}
	wtLabel := m.confirmWorktreeName
	if m.confirmWorktreeBranch != "" && m.confirmWorktreeBranch != wtLabel {
		wtLabel += " (" + m.confirmWorktreeBranch + ")"
	}
	b.WriteString(fmt.Sprintf("\n  %s Also remove git worktree %s (w)\n", box, wtLabel))
	if m.confirmWorktreeSafety.IsDirty {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render("    ⚠ Worktree has uncommitted changes (will be kept safe)") + "\n")
	} else if m.confirmWorktreeSafety.UnmergedCommits > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(
			fmt.Sprintf("    ⚠ %d unmerged commit(s) (branch will be preserved)", m.confirmWorktreeSafety.UnmergedCommits),
		) + "\n")
	}
	b.WriteString("\n" + styleMuted.Render("y/Enter confirm · w toggle worktree · n/Esc cancel"))
	return true
}

func (m Model) confirmDialog(width int) string {
	var b strings.Builder
	if m.confirmWindow {
		b.WriteString(styleError.Render("Close tab?"))
		b.WriteString("\n")
		pnoun := "panes"
		if m.confirmPanes == 1 {
			pnoun = "pane"
		}
		b.WriteString(fmt.Sprintf("Close tab %q (%s)? Its %d %s will stop.\n", m.confirmWindowName, m.confirmWindowID, m.confirmPanes, pnoun))
		if m.confirmWindowBusy {
			b.WriteString(styleError.Render("Warning: an agent in this tab is currently working or waiting for you.") + "\n")
		}
		if m.confirmWindowLast {
			b.WriteString(styleError.Render("Warning: this is the only tab in "+m.confirmSpace+"; closing it will close the space.") + "\n")
		}
		if m.appendConfirmWorktreeOption(&b) {
			return styleDialogBox.Render(b.String())
		}
		b.WriteString(styleMuted.Render("y/Enter confirm · n/Esc cancel"))
		return styleDialogBox.Render(b.String())
	}
	if m.confirmExited {
		b.WriteString(styleError.Render("Delete saved space?"))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("Delete saved snapshot %q? It will no longer appear under Exited.\n", m.confirmSpace))
		b.WriteString(styleMuted.Render("y/Enter confirm · n/Esc cancel"))
		return styleDialogBox.Render(b.String())
	}
	b.WriteString(styleError.Render("Close space?"))
	b.WriteString("\n")
	wnoun := "windows"
	if m.confirmWindows == 1 {
		wnoun = "window"
	}
	pnoun := "panes"
	if m.confirmPanes == 1 {
		pnoun = "pane"
	}
	b.WriteString(fmt.Sprintf("Close %q? Its %d %s and %d %s will stop. This cannot be undone.\n", m.confirmSpace, m.confirmWindows, wnoun, m.confirmPanes, pnoun))
	if m.appendConfirmWorktreeOption(&b) {
		return styleDialogBox.Render(b.String())
	}
	b.WriteString(styleMuted.Render("y/Enter confirm · n/Esc cancel"))
	return styleDialogBox.Render(b.String())
}

func (m Model) helpDialog(width int) string {
	sectionSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	keySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	descSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	kv := func(k, d string) string {
		return "  " + keySt.Render(fmt.Sprintf("%-14s", k)) + " " + descSt.Render(d)
	}

	if m.returnMode == ModeTabs {
		lines := []string{
			styleAccent.Render("Tabs · keys"),
			"",
			sectionSt.Render("Navigate"),
			kv("↑ ↓ / j k", "move between tabs in current workspace"),
			kv("← → / h l", "cycle split panes inside selected tab"),
			kv("0 … 9", "jump to tab by tmux window index"),
			kv("Enter / 2xClick", "focus selected tab & pane"),
			kv("/", "filter by index, name, agent, title, cmd, branch, worktree, path"),
			kv("H", "full scrollable hook detail overlay"),
			kv("Esc / q", "close tabs popup"),
			"",
			sectionSt.Render("Manage & View"),
			kv("n / r / x", "new · rename · close tab"),
			kv("W", "manage git worktrees"),
			kv("s", "cycle sort order (index · attention · activity)"),
			kv("J K / [ ]", "scroll full telemetry inspector"),
			kv("v", "expand or balance telemetry panel width"),
			"",
			sectionSt.Render("Filter"),
			kv("Tab / S-Tab", "cycle status filter"),
			kv("b / w / d / i", "blocked · working · done · idle"),
			kv("a", "show all tabs"),
			kv("Ctrl-r", "force refresh tabs, git & agent telemetry"),
			"",
			styleMuted.Render("? / Esc close"),
		}
		return styleDialogBox.Render(strings.Join(lines, "\n"))
	}
	if m.returnMode == ModeInbox {
		lines := []string{
			styleAccent.Render("Inbox · keys"),
			"",
			sectionSt.Render("Navigate"),
			kv("↑ ↓ / j k", "move between agents"),
			kv("Click / Enter", "open agent pane or reopen recent outcome in new tab"),
			kv("1 … 9", "select agent"),
			kv("/", "filter by agent, space, title, status"),
			kv("e / Space / →", "expand or collapse background work"),
			kv("H", "hook details for selected agent"),
			kv("Esc / q", "close inbox"),
			"",
			sectionSt.Render("Filter"),
			kv("Tab", "cycle status filter"),
			kv("b / w / d / i", "blocked · working · done · idle"),
			kv("a", "show all agents"),
			kv("Ctrl-r", "refresh"),
			"",
			styleMuted.Render("? / Esc close"),
		}
		return styleDialogBox.Render(strings.Join(lines, "\n"))
	}
	lines := []string{
		styleAccent.Render("Spaces · keys"),
		"",
		sectionSt.Render("Navigate"),
		kv("↑ ↓ / j k", "move between rows"),
		kv("← → / h l", "previous / next space"),
		kv("Enter", "open the selected space or pane"),
		kv("/", "search names, agents, branches, and paths"),
		kv("e / Space", "expand/collapse background work (or fold space)"),
		kv("H", "hook details for the selected pane"),
		kv("Esc", "close this navigator"),
		"",
		sectionSt.Render("Spaces"),
		kv("n / r / x", "new · rename · close space"),
		kv("W", "manage git worktrees"),
		kv("1 … 9", "select a space"),
		"",
		sectionSt.Render("Filter"),
		kv("b / w / d / i", "blocked · working · done · idle"),
		kv("a", "show everything"),
		kv("Ctrl-r", "refresh"),
		"",
		styleMuted.Render("? / Esc close"),
	}
	return styleDialogBox.Render(strings.Join(lines, "\n"))
}

func (m Model) hookDialog(width, height int) string {
	maxContentW := width - 8
	if maxContentW < 36 {
		maxContentW = 36
	}
	if maxContentW > 96 {
		maxContentW = 96
	}
	labelSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	sectionSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))

	var b strings.Builder
	b.WriteString(styleAccent.Render("Spaces · agent hooks"))
	b.WriteString("\n")
	if m.hookLoading && m.hookDetail == nil {
		b.WriteString(styleMuted.Render("loading hook detail…"))
		return styleDialogBox.Render(b.String())
	}
	if m.hookDetail == nil {
		// Fall back to summary info from the pane.
		if p := m.selectedPane(); p != nil && p.Hook != nil {
			h := p.Hook
			b.WriteString(valSt.Render(fmt.Sprintf("%s · %s · %s", h.App, h.State, h.Event)) + "\n")
		} else {
			b.WriteString(styleMuted.Render("No detail record for this pane.") + "\n")
		}
		b.WriteString("\n" + styleMuted.Render("↑↓ scroll · PgUp/PgDn page · H / Esc close"))
		return styleDialogBox.Render(b.String())
	}
	d := m.hookDetail
	header := d.Agent
	if header == "" {
		header = d.App
	}
	metaLine := fmt.Sprintf("%s · %s", header, d.State)
	if d.Kind != "" {
		metaLine += " · " + d.Kind
	}
	var lines []string
	lines = append(lines, styleAccent.Render("Spaces · agent hooks"))
	lines = append(lines, valSt.Render(truncate(metaLine, maxContentW)))
	if d.Model != "" {
		lines = append(lines, labelSt.Render("model    ")+valSt.Render(truncate(d.Model, maxContentW-9)))
	}
	if d.SessionID != "" {
		lines = append(lines, labelSt.Render("session  ")+valSt.Render(truncate(d.SessionID, maxContentW-9)))
	}
	if d.CWD != "" {
		lines = append(lines, labelSt.Render("cwd      ")+valSt.Render(truncate(d.CWD, maxContentW-9)))
	}
	lines = append(lines, "")

	addBody := func(raw string) {
		for _, ln := range strings.Split(raw, "\n") {
			lines = append(lines, valSt.Render(truncate(ln, maxContentW)))
		}
	}
	if strings.TrimSpace(d.Prompt) != "" {
		lines = append(lines, sectionSt.Render("prompt"))
		addBody(d.Prompt)
		lines = append(lines, "")
	}
	if d.Tool != nil {
		lines = append(lines, sectionSt.Render("tool · "+d.Tool.Name))
		if strings.TrimSpace(d.Tool.Input) != "" {
			addBody(d.Tool.Input)
		}
		if strings.TrimSpace(d.Tool.Output) != "" {
			lines = append(lines, labelSt.Render("result"))
			addBody(d.Tool.Output)
		}
		lines = append(lines, "")
	}
	if strings.TrimSpace(d.Reply) != "" {
		lines = append(lines, sectionSt.Render("reply"))
		addBody(d.Reply)
		lines = append(lines, "")
	}
	for _, bgLine := range renderHookDetailBackground(d, m.opts.NowOrDefault().UnixMilli()) {
		if bgLine == "" {
			lines = append(lines, "")
		} else if !strings.HasPrefix(bgLine, "· ") && !strings.HasPrefix(bgLine, "  ") {
			lines = append(lines, sectionSt.Render(truncate(bgLine, maxContentW)))
		} else {
			lines = append(lines, valSt.Render(truncate(bgLine, maxContentW)))
		}
	}

	visible := 12
	if height > 10 {
		visible = height - 8
		if visible < 4 {
			visible = 4
		}
		if visible > 30 {
			visible = 30
		}
	}
	start := m.hookScroll
	if start < 0 {
		start = 0
	}
	if start > len(lines)-1 {
		start = len(lines) - 1
		if start < 0 {
			start = 0
		}
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}
	shown := strings.Join(lines[start:end], "\n")
	return styleDialogBox.Render(shown + "\n" + styleMuted.Render("↑↓ scroll · PgUp/PgDn page · H / Esc close"))
}

// ---------------------------------------------------------------------------
// agents selector
// ---------------------------------------------------------------------------

func (m Model) renderAgents(width, height int) string {
	var b strings.Builder
	title := "Agents"
	if m.profileOf != nil && *m.profileOf < len(m.harnesses) {
		title = " " + m.harnesses[*m.profileOf].Name + " profiles "
	}
	count := ""
	if m.agentsLoading && len(m.harnesses) == 0 {
		count = "loading…"
	} else if m.profileOf != nil {
		n := len(m.profileRows())
		profs := 0
		for _, r := range m.profileRows() {
			if !r.isDefault {
				profs++
			}
		}
		_ = n
		noun := "profiles"
		if profs == 1 {
			noun = "profile"
		}
		count = fmt.Sprintf("%d %s", profs, noun)
	} else {
		noun := "harnesses"
		if len(m.harnesses) == 1 {
			noun = "harness"
		}
		count = fmt.Sprintf("%d %s", len(m.harnesses), noun)
	}
	b.WriteString(styleHeader.Width(width).Render(title + "  " + count))
	b.WriteString("\n")
	b.WriteString(styleSubtext.Render(truncate("/ "+m.agentFilter+"_", width)))
	b.WriteString("\n")
	lh := m.listHeight()
	if m.agentsLoading && len(m.harnesses) == 0 {
		b.WriteString(styleMuted.Render("  Loading harnesses…"))
		b.WriteString("\n")
		for i := 1; i < lh; i++ {
			b.WriteString("\n")
		}
	} else if m.profileOf != nil {
		rows := m.profileRows()
		if len(rows) == 0 {
			b.WriteString(styleMuted.Render("  No profiles match this filter."))
			b.WriteString("\n")
			for i := 1; i < lh; i++ {
				b.WriteString("\n")
			}
		} else {
			start := windowStart(m.harnessCursor, len(rows), lh)
			for i := 0; i < lh; i++ {
				idx := start + i
				if idx >= len(rows) {
					b.WriteString("\n")
					continue
				}
				r := rows[idx]
				var name, note string
				if r.isDefault {
					name = "Default"
					agent := m.harnesses[*m.profileOf].Name
					note = "plain " + agent + " launch"
				} else {
					p := m.harnesses[*m.profileOf].Profiles[r.profIdx]
					name = p.Name
					if p.DisplayName != "" && p.DisplayName != p.Name {
						note = p.DisplayName
					}
					if p.Source != "" {
						if note != "" {
							note += " · " + p.Source
						} else {
							note = p.Source
						}
					}
				}
				line := "  " + name + "  " + note
				line = truncate(line, width-1)
				if idx == m.harnessCursor {
					b.WriteString(styleSelection.Width(width).Render("▌" + line))
				} else {
					b.WriteString(" " + line)
				}
				b.WriteString("\n")
			}
		}
	} else {
		vis := m.filteredHarnessIdx()
		if len(vis) == 0 {
			msg := "No working harnesses found."
			if strings.TrimSpace(m.agentFilter) != "" {
				msg = "No harnesses match this filter."
			}
			b.WriteString(styleMuted.Render("  " + msg))
			b.WriteString("\n")
			for i := 1; i < lh; i++ {
				b.WriteString("\n")
			}
		} else {
			start := windowStart(m.harnessCursor, len(vis), lh)
			for i := 0; i < lh; i++ {
				idx := start + i
				if idx >= len(vis) {
					b.WriteString("\n")
					continue
				}
				h := m.harnesses[vis[idx]]
				label := h.Name
				if len(h.Profiles) > 0 {
					label += " ›"
				}
				line := "  " + label + "  " + h.Description
				line = truncate(line, width-1)
				if idx == m.harnessCursor {
					b.WriteString(styleSelection.Width(width).Render("▌" + line))
				} else {
					b.WriteString(" " + line)
				}
				b.WriteString("\n")
			}
		}
	}
	if height >= 16 || height <= 0 {
		b.WriteString(m.agentsDetailLine(width))
		b.WriteString("\n")
	}
	if m.toast != "" {
		b.WriteString(styleError.Render("! " + truncate(m.toast, width-2)))
		b.WriteString("\n")
	}
	back := "close"
	if m.profileOf != nil {
		back = "back"
	}
	b.WriteString(styleMuted.Render(truncate("↑↓ move · type filter · Enter open · Ctrl-r refresh · Esc "+back, width)))
	return b.String()
}

func (m Model) agentsDetailLine(width int) string {
	if m.profileOf != nil {
		rows := m.profileRows()
		if m.harnessCursor >= 0 && m.harnessCursor < len(rows) {
			r := rows[m.harnessCursor]
			if r.isDefault {
				return styleFooter.Width(width).Render("Default  plain launch")
			}
			p := m.harnesses[*m.profileOf].Profiles[r.profIdx]
			parts := []string{p.Name}
			if p.DisplayName != "" && p.DisplayName != p.Name {
				parts = append(parts, p.DisplayName)
			}
			if p.Source != "" {
				parts = append(parts, p.Source)
			}
			return styleFooter.Width(width).Render(truncate(strings.Join(parts, "  "), width))
		}
		return styleFooter.Width(width).Render("Type to filter the profile list.")
	}
	vis := m.filteredHarnessIdx()
	if m.harnessCursor >= 0 && m.harnessCursor < len(vis) {
		h := m.harnesses[vis[m.harnessCursor]]
		return styleFooter.Width(width).Render(truncate(h.Name+"  "+h.Description, width))
	}
	return styleFooter.Width(width).Render("Type to filter the harness list.")
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	if n <= 1 {
		return string(rs[:n])
	}
	return string(rs[:n-1]) + "…"
}

func truncateApp(s string) string { return truncate(s, 18) }

func shortenPath(p, home string) string {
	if p == "" {
		return ""
	}
	if home != "" && strings.HasPrefix(p, home) {
		rest := strings.TrimPrefix(p, home)
		if rest == "" {
			return "~"
		}
		return "~" + rest
	}
	return p
}

func formatAge(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

// Internal message for ModeShared.
type sharedMsg struct {
	snap model.SharedSnapshot
	err  string
}

type sharedItem struct {
	IsMCP          bool
	MCP            model.SharedMCP
	Skill          model.SharedSkill
	FuzzyScore     int
	NameHighlights []int
}

func (it sharedItem) Key() string {
	if it.IsMCP {
		return "mcp:" + it.MCP.Name
	}
	return "skill:" + it.Skill.DirName
}

func (it sharedItem) Name() string {
	if it.IsMCP {
		return it.MCP.Name
	}
	return it.Skill.Name
}

func (it sharedItem) SourcePath() string {
	if it.IsMCP {
		return it.MCP.SourcePath
	}
	return it.Skill.Path
}

type sharedLayout struct {
	topY         int
	bodyHeight   int
	showDividers bool
	detailLines  int
}

type sharedListEntry struct {
	isHeader bool
	isSpacer bool
	icon     string
	title    string
	color    string
	count    int
	itemIdx  int
}

func (m Model) loadSharedCmd() tea.Cmd {
	dir := m.opts.SharedDir
	home := m.opts.HomeDir
	if sb, ok := m.backend.(SharedBackend); ok {
		return func() tea.Msg {
			snap, err := sb.LoadShared(context.Background(), dir)
			if err != nil {
				return sharedMsg{err: err.Error()}
			}
			return sharedMsg{snap: snap}
		}
	}
	return func() tea.Msg {
		snap, err := model.LoadShared(dir, home)
		if err != nil {
			return sharedMsg{err: err.Error()}
		}
		return sharedMsg{snap: snap}
	}
}

func (m Model) sharedItems() []sharedItem {
	tokens := strings.Fields(strings.TrimSpace(m.query))
	hasQuery := len(tokens) > 0

	var mcps []sharedItem
	if m.sharedFilter == model.SharedFilterAll || m.sharedFilter == model.SharedFilterMCP {
		for _, srv := range m.sharedSnap.MCPs {
			if !hasQuery {
				mcps = append(mcps, sharedItem{IsMCP: true, MCP: srv})
				continue
			}
			score, hl, ok := model.FuzzyScoreSharedMCP(srv, tokens)
			if !ok {
				continue
			}
			mcps = append(mcps, sharedItem{
				IsMCP:          true,
				MCP:            srv,
				FuzzyScore:     score,
				NameHighlights: hl,
			})
		}
		if hasQuery {
			sort.SliceStable(mcps, func(i, j int) bool {
				if mcps[i].FuzzyScore != mcps[j].FuzzyScore {
					return mcps[i].FuzzyScore > mcps[j].FuzzyScore
				}
				return strings.ToLower(mcps[i].MCP.Name) < strings.ToLower(mcps[j].MCP.Name)
			})
		}
	}

	var skills []sharedItem
	if m.sharedFilter == model.SharedFilterAll || m.sharedFilter == model.SharedFilterSkills {
		for _, sk := range m.sharedSnap.Skills {
			if !hasQuery {
				skills = append(skills, sharedItem{Skill: sk})
				continue
			}
			score, hl, ok := model.FuzzyScoreSharedSkill(sk, tokens)
			if !ok {
				continue
			}
			skills = append(skills, sharedItem{
				Skill:          sk,
				FuzzyScore:     score,
				NameHighlights: hl,
			})
		}
		if hasQuery {
			sort.SliceStable(skills, func(i, j int) bool {
				if skills[i].FuzzyScore != skills[j].FuzzyScore {
					return skills[i].FuzzyScore > skills[j].FuzzyScore
				}
				return strings.ToLower(skills[i].Skill.Name) < strings.ToLower(skills[j].Skill.Name)
			})
		}
	}

	items := make([]sharedItem, 0, len(mcps)+len(skills))
	items = append(items, mcps...)
	items = append(items, skills...)
	return items
}

func buildSharedListEntries(items []sharedItem) []sharedListEntry {
	if len(items) == 0 {
		return nil
	}
	mcpCount := 0
	skillCount := 0
	for _, it := range items {
		if it.IsMCP {
			mcpCount++
		} else {
			skillCount++
		}
	}

	entries := make([]sharedListEntry, 0, len(items)+4)
	if mcpCount > 0 {
		entries = append(entries, sharedListEntry{
			isHeader: true,
			icon:     "⚡",
			title:    "MCP SERVERS",
			color:    ColorTeal,
			count:    mcpCount,
		})
		for i, it := range items {
			if it.IsMCP {
				entries = append(entries, sharedListEntry{itemIdx: i})
			}
		}
	}
	if skillCount > 0 {
		if len(entries) > 0 {
			entries = append(entries, sharedListEntry{isSpacer: true})
		}
		entries = append(entries, sharedListEntry{
			isHeader: true,
			icon:     "✦",
			title:    "SKILLS",
			color:    ColorLavender,
			count:    skillCount,
		})
		for i, it := range items {
			if !it.IsMCP {
				entries = append(entries, sharedListEntry{itemIdx: i})
			}
		}
	}
	return entries
}

func sharedSelectedEntryIdx(entries []sharedListEntry, selected int) int {
	for i, e := range entries {
		if !e.isHeader && !e.isSpacer && e.itemIdx == selected {
			return i
		}
	}
	return 0
}

func (m Model) selectedSharedKey() string {
	items := m.sharedItems()
	if m.selected < 0 || m.selected >= len(items) {
		return ""
	}
	return items[m.selected].Key()
}

func (m *Model) restoreSharedSelection(prevKey string) {
	items := m.sharedItems()
	if len(items) == 0 {
		m.selected = 0
		return
	}
	if prevKey != "" {
		for i, it := range items {
			if it.Key() == prevKey {
				m.selected = i
				return
			}
		}
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(items) {
		m.selected = len(items) - 1
	}
}

func (m *Model) moveSharedSelection(delta int) {
	items := m.sharedItems()
	if len(items) == 0 {
		m.selected = 0
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(items) {
		m.selected = len(items) - 1
	}
}

func (m Model) hasActiveSharedFilter() bool {
	return strings.TrimSpace(m.query) != "" || m.sharedFilter != model.SharedFilterAll
}

func (m *Model) openSelectedSharedFile() {
	items := m.sharedItems()
	if m.selected < 0 || m.selected >= len(items) {
		return
	}
	p := strings.TrimSpace(items[m.selected].SourcePath())
	if p == "" {
		m.toast = "No source file for selected item."
		return
	}
	sb, ok := m.backend.(SharedBackend)
	if !ok {
		m.toast = "Open in editor not supported."
		return
	}
	if err := sb.OpenSharedFile(context.Background(), p); err != nil {
		m.toast = err.Error()
		return
	}
	m.quitting = true
}

// ---------------------------------------------------------------------------
// update handlers
// ---------------------------------------------------------------------------

func (m Model) handleSharedMsg(msg sharedMsg) (tea.Model, tea.Cmd) {
	m.sharedLoading = false
	if msg.err != "" {
		m.toast = msg.err
		return m, nil
	}
	prevKey := m.selectedSharedKey()
	m.sharedSnap = msg.snap
	m.toast = ""
	m.restoreSharedSelection(prevKey)
	return m, nil
}

// ---------------------------------------------------------------------------
// key + mouse handling (search-first, with detail inspector)
// ---------------------------------------------------------------------------

func (m Model) handleShared(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sharedInspecting {
		return m.handleSharedInspectorKey(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.hasActiveSharedFilter() {
			m.query = ""
			m.cursor = 0
			m.sharedFilter = model.SharedFilterAll
			m.toast = ""
			m.selected = 0
			m.clampSelection()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		items := m.sharedItems()
		if m.selected >= 0 && m.selected < len(items) {
			m.sharedInspecting = true
			m.sharedDetailScroll = 0
		}
		return m, nil
	case tea.KeyCtrlO:
		m.openSelectedSharedFile()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveSharedSelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveSharedSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveSharedSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveSharedSelection(10)
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		all := model.AllSharedFilters()
		idx := 0
		for i, f := range all {
			if f == m.sharedFilter {
				idx = i
				break
			}
		}
		if msg.Type == tea.KeyShiftTab {
			idx = (idx + len(all) - 1) % len(all)
		} else {
			idx = (idx + 1) % len(all)
		}
		m.sharedFilter = all[idx]
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyRight:
		if m.cursor < len([]rune(m.query)) {
			m.cursor++
		}
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		m.cursor = 0
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE:
		m.cursor = len([]rune(m.query))
		return m, nil
	case tea.KeyBackspace:
		m.deleteRuneBefore()
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyDelete:
		m.deleteRuneAt()
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyCtrlU:
		m.query = ""
		m.cursor = 0
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyCtrlW:
		m.deleteWord()
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeySpace:
		m.insertRune(' ')
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyRunes:
		if msg.Alt || len(msg.Runes) == 0 {
			return m, nil
		}
		for _, r := range msg.Runes {
			m.insertRune(r)
		}
		m.selected = 0
		m.clampSelection()
		return m, nil
	}
	return m, nil
}

func (m Model) handleSharedInspectorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyEnter:
		m.sharedInspecting = false
		m.sharedDetailScroll = 0
		return m, nil
	case tea.KeyCtrlO:
		m.openSelectedSharedFile()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		if m.sharedDetailScroll > 0 {
			m.sharedDetailScroll--
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.sharedDetailScroll++
		return m, nil
	case tea.KeyPgUp:
		m.sharedDetailScroll -= 12
		if m.sharedDetailScroll < 0 {
			m.sharedDetailScroll = 0
		}
		return m, nil
	case tea.KeyPgDown:
		m.sharedDetailScroll += 12
		return m, nil
	case tea.KeyHome:
		m.sharedDetailScroll = 0
		return m, nil
	case tea.KeyEnd:
		m.sharedDetailScroll = 1 << 30
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch string(msg.Runes) {
		case "q":
			m.sharedInspecting = false
			m.sharedDetailScroll = 0
		case "k":
			if m.sharedDetailScroll > 0 {
				m.sharedDetailScroll--
			}
		case "j":
			m.sharedDetailScroll++
		case "g":
			m.sharedDetailScroll = 0
		case "G":
			m.sharedDetailScroll = 1 << 30
		case "e":
			m.openSelectedSharedFile()
			if m.quitting {
				return m, tea.Quit
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleSharedLeftClick(x, y int) (tea.Model, tea.Cmd) {
	_ = x
	if m.sharedInspecting {
		return m, nil
	}
	items := m.sharedItems()
	if len(items) == 0 {
		return m, nil
	}
	layout := m.sharedLayout(m.width, m.height)
	if y < layout.topY || y >= layout.topY+layout.bodyHeight {
		return m, nil
	}
	entries := buildSharedListEntries(items)
	selEntry := sharedSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), layout.bodyHeight)
	pos := start + (y - layout.topY)
	if pos < 0 || pos >= len(entries) || entries[pos].isHeader || entries[pos].isSpacer {
		return m, nil
	}
	idx := entries[pos].itemIdx
	now := time.Now()
	if m.selected == idx && m.lastClickRow == idx && now.Sub(m.lastClickAt) < 500*time.Millisecond {
		m.sharedInspecting = true
		m.sharedDetailScroll = 0
		return m, nil
	}
	m.selected = idx
	m.lastClickRow = idx
	m.lastClickAt = now
	return m, nil
}

// ---------------------------------------------------------------------------
// layout + rendering
// ---------------------------------------------------------------------------

func (m Model) sharedLayout(width, height int) sharedLayout {
	_ = width
	if height <= 0 {
		height = 24
	}
	showDividers := height >= 12
	topY := 2 // header + search bar
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
	return sharedLayout{
		topY:         topY,
		bodyHeight:   bodyHeight,
		showDividers: showDividers,
		detailLines:  detailLines,
	}
}

func (m Model) renderShared(width, height int) string {
	if m.sharedInspecting {
		return m.renderSharedInspector(width, height)
	}
	layout := m.sharedLayout(width, height)
	items := m.sharedItems()

	var b strings.Builder
	b.WriteString(m.sharedHeaderLine(width))
	b.WriteString("\n")
	b.WriteString(m.sharedSearchLine(items, width))
	b.WriteString("\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	lines := m.renderSharedList(items, width, layout.bodyHeight)
	for i := 0; i < layout.bodyHeight; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width))
			b.WriteString("\n")
		}
		for _, dl := range m.renderSharedDetailLines(items, width, layout.detailLines) {
			b.WriteString(dl)
			b.WriteString("\n")
		}
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.sharedHintsLine(width))
	return b.String()
}

func (m Model) sharedHeaderLine(width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	dot := st(ColorSurface1, false).Render(" · ")

	var segs []string
	if m.sharedLoading && len(m.sharedSnap.MCPs) == 0 && len(m.sharedSnap.Skills) == 0 {
		segs = append(segs, st(ColorYellow, false).Render("loading…"))
	} else {
		mcpN := len(m.sharedSnap.MCPs)
		skillN := len(m.sharedSnap.Skills)
		mcpColor := ColorOverlay0
		if mcpN > 0 {
			mcpColor = ColorTeal
		}
		segs = append(segs, st(mcpColor, mcpN > 0).Render(fmt.Sprintf("⚡ %d mcp", mcpN)))
		skillNoun := "skills"
		if skillN == 1 {
			skillNoun = "skill"
		}
		segs = append(segs, st(ColorSubtext0, false).Render(fmt.Sprintf("✦ %d %s", skillN, skillNoun)))
		if root := shortenPath(m.sharedSnap.RootDir, m.opts.HomeDir); root != "" && width >= 92 {
			segs = append(segs, st(ColorOverlay0, false).Render(truncate(root, 34)))
		}
	}

	var lb strings.Builder
	lb.WriteString(st(ColorText, true).Render(" Shared MCP & Skills"))
	if len(segs) > 0 {
		lb.WriteString(st(bg, false).Render("   "))
		lb.WriteString(strings.Join(segs, dot))
	}
	leftStyled := lb.String()
	rightStyled := renderSharedFilterPills(m.sharedFilter, bg) + st(bg, false).Render(" ")

	leftW := ansi.StringWidth(leftStyled)
	rightW := ansi.StringWidth(rightStyled)
	var line string
	if leftW+rightW+2 <= width {
		line = leftStyled + st(bg, false).Render(strings.Repeat(" ", width-leftW-rightW)) + rightStyled
	} else if width > rightW+10 {
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

func renderSharedFilterPills(active model.SharedKind, bg string) string {
	activeSt := lipgloss.NewStyle().
		Background(lipgloss.Color(ColorSurface0)).
		Foreground(lipgloss.Color(ColorLavender)).
		Bold(true)
	inactiveSt := lipgloss.NewStyle().
		Background(lipgloss.Color(bg)).
		Foreground(lipgloss.Color(ColorOverlay0))
	sep := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Render(" ")

	all := model.AllSharedFilters()
	pills := make([]string, 0, len(all))
	for _, f := range all {
		label := f.Label()
		if f == active {
			pills = append(pills, activeSt.Render("["+label+"]"))
		} else {
			pills = append(pills, inactiveSt.Render(label))
		}
	}
	return strings.Join(pills, sep)
}

func (m Model) sharedSearchLine(items []sharedItem, width int) string {
	slashSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	querySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	caretSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	phSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	matchSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))

	rightStyled := ""
	rightW := 0
	if m.hasActiveSharedFilter() {
		noun := "matches"
		if len(items) == 1 {
			noun = "match"
		}
		rightStyled = matchSt.Render(fmt.Sprintf("%d %s ", len(items), noun))
		rightW = ansi.StringWidth(rightStyled)
	}

	maxLeft := width
	if rightW > 0 {
		maxLeft = width - rightW - 2
	}
	if maxLeft < 10 {
		maxLeft = 10
	}

	var lb strings.Builder
	lb.WriteString(slashSt.Render(" / "))
	if m.query == "" {
		lb.WriteString(caretSt.Render("_"))
		rem := maxLeft - 5
		if rem > 6 {
			lb.WriteString(phSt.Render(" " + truncate("Search shared MCP servers and skills…", rem-1)))
		}
	} else {
		lb.WriteString(querySt.Render(truncate(m.query, maxLeft-5)))
		lb.WriteString(caretSt.Render("_"))
	}
	leftStyled := lb.String()
	leftW := ansi.StringWidth(leftStyled)

	if rightW > 0 && leftW+rightW+2 <= width {
		return fitCellWidth(leftStyled+strings.Repeat(" ", width-leftW-rightW)+rightStyled, width)
	}
	return fitCellWidth(leftStyled, width)
}

func (m Model) renderSharedList(items []sharedItem, width, height int) []string {
	lines := make([]string, height)
	if len(items) == 0 {
		msg := "No shared MCP servers or skills match this query."
		switch {
		case m.sharedLoading:
			msg = "Loading shared MCP servers and skills…"
		case !m.sharedSnap.RootExists && m.sharedSnap.RootDir != "":
			msg = "Directory not found: " + shortenPath(m.sharedSnap.RootDir, m.opts.HomeDir)
		case len(m.sharedSnap.MCPs) == 0 && len(m.sharedSnap.Skills) == 0:
			msg = "No shared MCP servers or skills found in " + shortenPath(m.sharedSnap.RootDir, m.opts.HomeDir)
		case m.sharedFilter == model.SharedFilterMCP && len(m.sharedSnap.MCPs) == 0:
			msg = "No shared MCP servers found."
		case m.sharedFilter == model.SharedFilterSkills && len(m.sharedSnap.Skills) == 0:
			msg = "No shared skills found."
		}
		lines[0] = fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width)
		for i := 1; i < height; i++ {
			lines[i] = fitCellWidth("", width)
		}
		return lines
	}

	entries := buildSharedListEntries(items)
	selEntry := sharedSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), height)

	for i := 0; i < height; i++ {
		idx := start + i
		if idx >= len(entries) {
			lines[i] = fitCellWidth("", width)
			continue
		}
		e := entries[idx]
		if e.isSpacer {
			lines[i] = fitCellWidth("", width)
			continue
		}
		if e.isHeader {
			hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(e.color)).Bold(true).Render(
				fmt.Sprintf("  %s %s · %d", e.icon, e.title, e.count),
			)
			lines[i] = renderSectionRule(hdr, width)
			continue
		}
		it := items[e.itemIdx]
		selected := e.itemIdx == m.selected
		if it.IsMCP {
			lines[i] = m.renderSharedMCPRow(it, selected, width)
		} else {
			lines[i] = m.renderSharedSkillRow(it, selected, width)
		}
	}
	return lines
}

func (m Model) renderSharedMCPRow(it sharedItem, selected bool, width int) string {
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
	innerW := width - 3
	if innerW < 10 {
		innerW = 10
	}

	srv := it.MCP
	var rightParts []string
	if srv.Disabled {
		rightParts = append(rightParts, st(ColorRed, false).Render("disabled"))
	}
	if srv.Transport != "" {
		rightParts = append(rightParts, st(ColorTeal, false).Render(srv.Transport))
	}
	if srv.SourceKind == "shared" {
		rightParts = append(rightParts, st(ColorGreen, false).Render("shared"))
	} else if len(srv.Harnesses) > 0 {
		noun := "harnesses"
		if len(srv.Harnesses) == 1 {
			noun = "harness"
		}
		rightParts = append(rightParts, st(ColorOverlay0, false).Render(fmt.Sprintf("%d %s", len(srv.Harnesses), noun)))
	}
	sep := st(ColorSurface1, false).Render(" · ")
	rightStyled := strings.Join(rightParts, sep)
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW - rightW - 2
	if maxLeft < 10 {
		maxLeft = 10
	}

	nameColW := 24
	if width >= 110 {
		nameColW = 30
	} else if width < 76 {
		nameColW = 18
	}
	if nameColW > maxLeft-6 {
		nameColW = maxLeft - 6
	}
	if nameColW < 8 {
		nameColW = 8
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("  "))
	iconColor := ColorTeal
	if srv.Disabled {
		iconColor = ColorOverlay0
	}
	lb.WriteString(st(iconColor, true).Render("⚡"))
	lb.WriteString(st(bg, false).Render(" "))

	nameFg := ColorText
	if srv.Disabled {
		nameFg = ColorOverlay0
	}
	if len(it.NameHighlights) > 0 {
		spans := positionsToSpans(it.NameHighlights)
		renderedName := renderHighlightedText(srv.Name, spans, nameColW, st(nameFg, selected), st(ColorLavender, true))
		lb.WriteString(renderedName)
		if w := ansi.StringWidth(renderedName); w < nameColW {
			lb.WriteString(st(bg, false).Render(strings.Repeat(" ", nameColW-w)))
		}
	} else {
		truncName := ansi.Truncate(srv.Name, nameColW, "…")
		lb.WriteString(st(nameFg, selected).Render(truncName))
		if w := ansi.StringWidth(truncName); w < nameColW {
			lb.WriteString(st(bg, false).Render(strings.Repeat(" ", nameColW-w)))
		}
	}

	summary := srv.Description
	if summary == "" {
		summary = shortenPath(srv.CommandLine(), m.opts.HomeDir)
	}
	rem := maxLeft - 5 - nameColW - 2
	if summary != "" && rem > 6 {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorSubtext0, false).Render(ansi.Truncate(summary, rem, "…")))
	}

	return composeSharedRow(prefix, lb.String(), rightStyled, innerW, width, selected, bg, st)
}

func (m Model) renderSharedSkillRow(it sharedItem, selected bool, width int) string {
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
	innerW := width - 3
	if innerW < 10 {
		innerW = 10
	}

	sk := it.Skill
	var rightParts []string
	if sk.HasReferences {
		rightParts = append(rightParts, st(ColorBlue, false).Render("refs"))
	}
	if sk.HasScripts {
		rightParts = append(rightParts, st(ColorYellow, false).Render("scripts"))
	}
	if sk.DisableModelInvocation {
		rightParts = append(rightParts, st(ColorOverlay0, false).Render("slash"))
	} else {
		rightParts = append(rightParts, st(ColorGreen, false).Render("auto"))
	}
	sep := st(ColorSurface1, false).Render(" · ")
	rightStyled := strings.Join(rightParts, sep)
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW - rightW - 2
	if maxLeft < 10 {
		maxLeft = 10
	}

	nameColW := 28
	if width >= 115 {
		nameColW = 36
	} else if width < 80 {
		nameColW = 20
	}
	if nameColW > maxLeft-6 {
		nameColW = maxLeft - 6
	}
	if nameColW < 8 {
		nameColW = 8
	}

	icon := "✦"
	iconColor := ColorGreen
	if sk.DisableModelInvocation {
		icon = "/"
		iconColor = ColorMauve
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(st(iconColor, true).Render(icon))
	lb.WriteString(st(bg, false).Render("  "))

	if len(it.NameHighlights) > 0 {
		spans := positionsToSpans(it.NameHighlights)
		renderedName := renderHighlightedText(sk.Name, spans, nameColW, st(ColorText, selected), st(ColorLavender, true))
		lb.WriteString(renderedName)
		if w := ansi.StringWidth(renderedName); w < nameColW {
			lb.WriteString(st(bg, false).Render(strings.Repeat(" ", nameColW-w)))
		}
	} else {
		truncName := ansi.Truncate(sk.Name, nameColW, "…")
		lb.WriteString(st(ColorText, selected).Render(truncName))
		if w := ansi.StringWidth(truncName); w < nameColW {
			lb.WriteString(st(bg, false).Render(strings.Repeat(" ", nameColW-w)))
		}
	}

	rem := maxLeft - 5 - nameColW - 2
	if sk.Description != "" && rem > 6 {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorSubtext0, false).Render(ansi.Truncate(sk.Description, rem, "…")))
	}

	return composeSharedRow(prefix, lb.String(), rightStyled, innerW, width, selected, bg, st)
}

func composeSharedRow(prefix, leftStyled, rightStyled string, innerW, width int, selected bool, bg string, st func(string, bool) lipgloss.Style) string {
	leftW := ansi.StringWidth(leftStyled)
	rightW := ansi.StringWidth(rightStyled)

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

func (m Model) renderSharedDetailLines(items []sharedItem, width, maxLines int) []string {
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

	if len(items) == 0 || m.selected < 0 || m.selected >= len(items) {
		empty := st(ColorOverlay0, false).Render("Type to fuzzy-search shared MCP servers and skills. Tab filters by type.")
		if maxLines <= 1 {
			return []string{padLine(empty)}
		}
		return []string{padLine(empty), padLine("")}
	}

	it := items[m.selected]
	if it.IsMCP {
		srv := it.MCP
		var l1 []string
		l1 = append(l1, st(ColorTeal, true).Render("⚡ "+srv.Name))
		if srv.Transport != "" {
			l1 = append(l1, st(ColorSubtext0, false).Render(srv.Transport))
		}
		if srv.Exposure != "" {
			l1 = append(l1, st(ColorMauve, false).Render(srv.Exposure))
		}
		if srv.SourceKind == "shared" {
			l1 = append(l1, st(ColorGreen, false).Render("shared"))
		} else if len(srv.Harnesses) > 0 {
			l1 = append(l1, st(ColorSubtext0, false).Render(fmt.Sprintf("%d harnesses: %s", len(srv.Harnesses), truncate(strings.Join(srv.Harnesses, ", "), 44))))
		}
		if p := shortenPath(srv.SourcePath, m.opts.HomeDir); p != "" {
			l1 = append(l1, st(ColorOverlay0, false).Render(p))
		}

		var l2 []string
		if cmd := shortenPath(srv.CommandLine(), m.opts.HomeDir); cmd != "" {
			l2 = append(l2, st(ColorText, false).Render(cmd))
		}
		if srv.Description != "" {
			l2 = append(l2, st(ColorSubtext0, false).Render(srv.Description))
		}
		if maxLines <= 1 {
			return []string{padLine(strings.Join(append(l1, l2...), sep))}
		}
		return []string{padLine(strings.Join(l1, sep)), padLine(strings.Join(l2, sep))}
	}

	sk := it.Skill
	var l1 []string
	l1 = append(l1, st(ColorLavender, true).Render(sk.Name))
	if sk.DisableModelInvocation {
		l1 = append(l1, st(ColorMauve, false).Render("/"+sk.Name+" (manual)"))
	} else {
		l1 = append(l1, st(ColorGreen, false).Render("auto-invocable"))
	}
	if sk.ExtraFiles > 0 {
		noun := "files"
		if sk.ExtraFiles == 1 {
			noun = "file"
		}
		l1 = append(l1, st(ColorSubtext0, false).Render(fmt.Sprintf("+%d %s", sk.ExtraFiles, noun)))
	}
	if len(sk.Paths) > 0 {
		l1 = append(l1, st(ColorTeal, false).Render("paths: "+strings.Join(sk.Paths, ", ")))
	}
	if p := shortenPath(sk.Path, m.opts.HomeDir); p != "" {
		l1 = append(l1, st(ColorOverlay0, false).Render(p))
	}

	var l2 []string
	if sk.Description != "" {
		l2 = append(l2, st(ColorText, false).Render(sk.Description))
	} else {
		l2 = append(l2, st(ColorOverlay0, false).Render("Enter inspect SKILL.md · Ctrl-o open in editor"))
	}
	if maxLines <= 1 {
		return []string{padLine(strings.Join(append(l1, l2...), sep))}
	}
	return []string{padLine(strings.Join(l1, sep)), padLine(strings.Join(l2, sep))}
}

func (m Model) sharedHintsLine(width int) string {
	if width < 72 {
		return renderHintsBar([][2]string{
			{"↑↓", "move"},
			{"Enter", "inspect"},
			{"Tab", "filter"},
			{"Esc", "close"},
		}, width)
	}
	return renderHintsBar([][2]string{
		{"↑↓", "move"},
		{"Enter", "inspect"},
		{"Ctrl-o", "edit"},
		{"Tab", "filter"},
		{"Ctrl-r", "refresh"},
		{"Esc", "clear/close"},
	}, width)
}

// ---------------------------------------------------------------------------
// Detail inspector (scrollable view of SKILL.md or MCP config)
// ---------------------------------------------------------------------------

func (m Model) renderSharedInspector(width, height int) string {
	if height <= 0 {
		height = 24
	}
	items := m.sharedItems()
	if m.selected < 0 || m.selected >= len(items) {
		m.sharedInspecting = false
		return m.renderShared(width, height)
	}
	it := items[m.selected]

	contentW := width - 4
	if contentW < 20 {
		contentW = 20
	}
	docLines := m.buildSharedInspectorLines(it, contentW)

	showDividers := height >= 10
	reserved := 2 // header + hints
	if showDividers {
		reserved += 2 // top + bottom dividers
	}
	if m.toast != "" {
		reserved++
	}
	bodyH := height - reserved
	if bodyH < 1 {
		bodyH = 1
	}

	maxScroll := len(docLines) - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.sharedDetailScroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	var b strings.Builder
	b.WriteString(m.sharedInspectorHeader(it, scroll, len(docLines), bodyH, width))
	b.WriteString("\n")
	if showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	for i := 0; i < bodyH; i++ {
		idx := scroll + i
		if idx < len(docLines) {
			b.WriteString(fitCellWidth("  "+docLines[idx], width))
		} else {
			b.WriteString(fitCellWidth("", width))
		}
		b.WriteString("\n")
	}

	if showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(renderHintsBar([][2]string{
		{"↑↓/jk", "scroll"},
		{"PgUp/PgDn", "page"},
		{"g/G", "top/bottom"},
		{"e/Ctrl-o", "edit"},
		{"Esc/q", "back"},
	}, width))
	return b.String()
}

func (m Model) sharedInspectorHeader(it sharedItem, scroll, total, bodyH, width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	dot := st(ColorSurface1, false).Render(" · ")

	var left string
	if it.IsMCP {
		left = st(ColorTeal, true).Render(" ⚡ "+it.MCP.Name) + dot + st(ColorSubtext0, false).Render("MCP server")
	} else {
		left = st(ColorLavender, true).Render(" ✦ "+it.Skill.Name) + dot + st(ColorSubtext0, false).Render("Skill")
	}

	var right string
	if total > bodyH && total > 0 {
		end := scroll + bodyH
		if end > total {
			end = total
		}
		right = st(ColorOverlay0, false).Render(fmt.Sprintf("%d–%d / %d ", scroll+1, end, total))
	} else if p := filepath.Base(it.SourcePath()); p != "" {
		right = st(ColorOverlay0, false).Render(p + " ")
	}

	leftW := ansi.StringWidth(left)
	rightW := ansi.StringWidth(right)
	var line string
	if rightW > 0 && leftW+rightW+2 <= width {
		line = left + st(bg, false).Render(strings.Repeat(" ", width-leftW-rightW)) + right
	} else {
		line = ansi.Truncate(left, width, "…")
	}
	return fitCellWidthWithBG(line, width, bg)
}

func (m Model) buildSharedInspectorLines(it sharedItem, maxW int) []string {
	labelSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	sectionSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	subHeadSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorBlue)).Bold(true)
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	subSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	codeSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal))

	kv := func(k, v string) string {
		return labelSt.Render(fmt.Sprintf("%-11s", k)) + valSt.Render(truncate(v, maxW-11))
	}

	var lines []string
	if it.IsMCP {
		srv := it.MCP
		lines = append(lines, kv("name", srv.Name))
		if srv.Transport != "" {
			lines = append(lines, kv("transport", srv.Transport))
		}
		status := "enabled"
		if srv.Disabled {
			status = "disabled"
		}
		lines = append(lines, kv("status", status))
		if srv.Exposure != "" {
			lines = append(lines, kv("exposure", srv.Exposure))
		}
		if srv.SourcePath != "" {
			lines = append(lines, kv("config", shortenPath(srv.SourcePath, m.opts.HomeDir)))
		}
		if srv.Command != "" {
			lines = append(lines, kv("command", shortenPath(srv.Command, m.opts.HomeDir)))
		}
		if len(srv.Args) > 0 {
			lines = append(lines, kv("args", strings.Join(srv.Args, " ")))
		}
		if srv.URL != "" {
			lines = append(lines, kv("url", srv.URL))
		}
		if srv.Description != "" {
			lines = append(lines, "")
			lines = append(lines, sectionSt.Render("Description"))
			for _, wl := range wrapWords(srv.Description, maxW) {
				lines = append(lines, valSt.Render(wl))
			}
		}
		if len(srv.Harnesses) > 0 {
			lines = append(lines, "")
			lines = append(lines, sectionSt.Render(fmt.Sprintf("Configured in %d harnesses", len(srv.Harnesses))))
			for _, wl := range wrapWords(strings.Join(srv.Harnesses, ", "), maxW) {
				lines = append(lines, subSt.Render(wl))
			}
		}
		if len(srv.Env) > 0 {
			lines = append(lines, "")
			lines = append(lines, sectionSt.Render("Environment"))
			keys := make([]string, 0, len(srv.Env))
			for k := range srv.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				lines = append(lines, codeSt.Render(truncate(k+"="+srv.Env[k], maxW)))
			}
		}
		if srv.RawJSON != "" {
			lines = append(lines, "")
			lines = append(lines, sectionSt.Render("Definition"))
			for _, rawLn := range strings.Split(srv.RawJSON, "\n") {
				lines = append(lines, codeSt.Render(truncate(rawLn, maxW)))
			}
		}
		return lines
	}

	sk := it.Skill
	lines = append(lines, kv("name", sk.Name))
	if sk.DirName != "" && sk.DirName != sk.Name {
		lines = append(lines, kv("directory", sk.DirName))
	}
	inv := "auto-invocable"
	if sk.DisableModelInvocation {
		inv = "manual (/" + sk.Name + ")"
	}
	lines = append(lines, kv("invocation", inv))
	if len(sk.Paths) > 0 {
		lines = append(lines, kv("paths", strings.Join(sk.Paths, ", ")))
	}
	if sk.ExtraFiles > 0 {
		var parts []string
		if sk.HasReferences {
			parts = append(parts, "references/")
		}
		if sk.HasScripts {
			parts = append(parts, "scripts/")
		}
		parts = append(parts, fmt.Sprintf("%d extra files", sk.ExtraFiles))
		lines = append(lines, kv("assets", strings.Join(parts, " · ")))
	}
	if sk.Path != "" {
		lines = append(lines, kv("file", shortenPath(sk.Path, m.opts.HomeDir)))
	}
	if sk.Description != "" {
		lines = append(lines, "")
		lines = append(lines, sectionSt.Render("Description"))
		for _, wl := range wrapWords(sk.Description, maxW) {
			lines = append(lines, valSt.Render(wl))
		}
	}
	if sk.Body != "" {
		lines = append(lines, "")
		lines = append(lines, sectionSt.Render("SKILL.md"))
		inCode := false
		for _, rawLn := range strings.Split(sk.Body, "\n") {
			trimmed := strings.TrimSpace(rawLn)
			if strings.HasPrefix(trimmed, "```") {
				inCode = !inCode
				lines = append(lines, labelSt.Render(truncate(rawLn, maxW)))
				continue
			}
			if inCode {
				lines = append(lines, codeSt.Render(truncate(rawLn, maxW)))
				continue
			}
			if trimmed == "" {
				lines = append(lines, "")
				continue
			}
			if strings.HasPrefix(trimmed, "# ") {
				lines = append(lines, sectionSt.Render(truncate(strings.TrimPrefix(trimmed, "# "), maxW)))
				continue
			}
			if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
				lines = append(lines, subHeadSt.Render(truncate(strings.TrimLeft(trimmed, "# "), maxW)))
				continue
			}
			for _, wl := range wrapWords(rawLn, maxW) {
				lines = append(lines, valSt.Render(wl))
			}
		}
	}
	return lines
}

func wrapWords(s string, maxW int) []string {
	if maxW <= 8 || ansi.StringWidth(s) <= maxW {
		return []string{truncate(s, maxW)}
	}
	leadLen := 0
	for leadLen < len(s) && s[leadLen] == ' ' {
		leadLen++
	}
	indent := s[:leadLen]
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	cur := indent + words[0]
	for _, w := range words[1:] {
		if ansi.StringWidth(cur)+1+ansi.StringWidth(w) <= maxW {
			cur += " " + w
		} else {
			out = append(out, truncate(cur, maxW))
			cur = indent + w
		}
	}
	out = append(out, truncate(cur, maxW))
	return out
}

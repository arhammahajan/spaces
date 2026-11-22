package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

// InboxItem identifies either a live agent pane or a recent outcome entry
// across all spaces in the Attention Inbox.
type InboxItem struct {
	IsOutcome  bool
	SpaceIdx   int
	PaneIdx    int
	OutcomeIdx int
}

type inboxLayout struct {
	topY         int // first Y coordinate of the list body
	bodyHeight   int // number of rows in the list body
	showDividers bool
	detailLines  int
}

type inboxListEntry struct {
	isHeader bool
	isSpacer bool
	icon     string
	title    string
	color    string
	count    int
	itemIdx  int
	subLine  int // 0 = primary line, 1 = optional action line, 2 = expanded inline background item
	bgIdx    int
	bgLast   bool
}

func inboxSectionFor(s model.Status) (icon, title, color string) {
	switch s {
	case model.StatusNeedsYou:
		return "●", "NEEDS YOU", ColorBlue
	case model.StatusFailed:
		return "✗", "FAILED", ColorRed
	case model.StatusDone:
		return "✓", "DONE", ColorGreen
	case model.StatusWorking:
		return "◐", "WORKING", ColorYellow
	default:
		return "○", "IDLE", ColorOverlay0
	}
}

func (m Model) inboxItemStatus(item InboxItem) model.Status {
	if item.IsOutcome {
		return m.outcomes[item.OutcomeIdx].Status()
	}
	return m.spaces[item.SpaceIdx].Panes[item.PaneIdx].Status
}

func (m Model) buildInboxListEntries(items []InboxItem) []inboxListEntry {
	if len(items) == 0 {
		return nil
	}
	entries := make([]inboxListEntry, 0, len(items)*2+10)
	for i := 0; i < len(items); {
		icon, title, color := inboxSectionFor(m.inboxItemStatus(items[i]))
		j := i + 1
		for j < len(items) {
			_, t2, _ := inboxSectionFor(m.inboxItemStatus(items[j]))
			if t2 != title {
				break
			}
			j++
		}
		if len(entries) > 0 {
			entries = append(entries, inboxListEntry{isSpacer: true})
		}
		entries = append(entries, inboxListEntry{
			isHeader: true,
			icon:     icon,
			title:    title,
			color:    color,
			count:    j - i,
		})
		for k := i; k < j; k++ {
			entries = append(entries, inboxListEntry{isHeader: false, itemIdx: k, subLine: 0})
			if !items[k].IsOutcome {
				sp := m.spaces[items[k].SpaceIdx]
				pk := sp.Panes[items[k].PaneIdx]
				if paneHasActiveActionLine(sp, pk, true, m.opts.HomeDir) {
					entries = append(entries, inboxListEntry{isHeader: false, itemIdx: k, subLine: 1})
				}
				if m.expandedBg != nil && m.expandedBg[paneBgKey(sp.Name, pk.ID)] {
					bgItems := m.paneBackgroundItems(sp.Name, pk)
					for bi := range bgItems {
						entries = append(entries, inboxListEntry{
							isHeader: false,
							itemIdx:  k,
							subLine:  2,
							bgIdx:    bi,
							bgLast:   bi == len(bgItems)-1,
						})
					}
				}
			}
		}
		i = j
	}
	return entries
}

func inboxSelectedEntryIdx(entries []inboxListEntry, selected int) int {
	for i, e := range entries {
		if !e.isHeader && !e.isSpacer && e.itemIdx == selected && e.subLine == 0 {
			return i
		}
	}
	return 0
}

func (m Model) isInboxContext() bool {
	return m.mode == ModeInbox || m.returnMode == ModeInbox
}

func (m Model) exitOverlayMode() Mode {
	if m.returnMode == ModeInbox || m.returnMode == ModeTabs || m.returnMode == ModeWorktrees {
		return m.returnMode
	}
	return ModeNavigate
}

// inboxLiveConversationSets indexes all currently running agent panes across
// spaces so closed/persisted outcomes for a conversation that is currently
// live (idle, working, blocked, done, or failed) are never duplicated.
func (m *Model) inboxLiveConversationSets() (sessionIDs map[string]bool, paneApps map[string]string) {
	sessionIDs = make(map[string]bool)
	paneApps = make(map[string]string)
	for _, sp := range m.spaces {
		for _, p := range sp.Panes {
			if !p.IsAgent() {
				continue
			}
			if sid := strings.TrimSpace(p.ResumeSession); sid != "" {
				sessionIDs[sid] = true
			}
			if p.Hook != nil {
				if sid := strings.TrimSpace(p.Hook.SessionID); sid != "" {
					sessionIDs[sid] = true
				}
			}
			if p.NumericID >= 0 {
				paneApps[sp.Name+"\x00"+strconv.Itoa(p.NumericID)] = strings.TrimSpace(p.App)
			}
		}
	}
	return sessionIDs, paneApps
}

func outcomeIsLive(o model.OutcomeEntry, liveSessionIDs map[string]bool, livePaneApps map[string]string) bool {
	if sid := strings.TrimSpace(o.SessionID); sid != "" && liveSessionIDs[sid] {
		return true
	}
	if o.Session != "" && o.Pane != "" {
		key := o.Session + "\x00" + strings.TrimPrefix(strings.TrimSpace(o.Pane), "%")
		if liveApp, ok := livePaneApps[key]; ok {
			oApp := o.DisplayApp()
			if liveApp == "" || oApp == "" ||
				strings.EqualFold(liveApp, oApp) ||
				strings.EqualFold(liveApp, strings.TrimSpace(o.Agent)) {
				return true
			}
		}
	}
	return false
}

// inboxStatusPriority defines the Attention Inbox section ordering:
// NeedsYou > Failed > Working > Idle (Quiet / NewOutput / Idle) > Done.
func inboxStatusPriority(s model.Status) int {
	switch s {
	case model.StatusNeedsYou:
		return 0
	case model.StatusFailed:
		return 1
	case model.StatusWorking:
		return 2
	case model.StatusQuiet:
		return 3
	case model.StatusNewOutput:
		return 4
	case model.StatusIdle:
		return 5
	case model.StatusDone:
		return 6
	default:
		return 5
	}
}

// inboxItems returns all agent panes and non-live persisted outcomes across
// all spaces, filtered by status and search query, and sorted by inbox section
// order (NeedsYou > Failed > Working > Quiet > NewOutput > Idle > Done),
// merging persisted failed/done conversations right into FAILED and DONE.
func (m *Model) inboxItems() []InboxItem {
	q := strings.ToLower(strings.TrimSpace(m.query))
	liveSIDs, livePanes := m.inboxLiveConversationSets()
	var items []InboxItem
	for si, sp := range m.spaces {
		for pi, p := range sp.Panes {
			if !p.IsAgent() {
				continue
			}
			if !m.filter.Matches(p.Status) {
				continue
			}
			if q != "" && !inboxMatchesQuery(sp, p, q) {
				continue
			}
			items = append(items, InboxItem{SpaceIdx: si, PaneIdx: pi})
		}
	}
	for oi, o := range m.outcomes {
		if outcomeIsLive(o, liveSIDs, livePanes) {
			continue
		}
		if !m.filter.Matches(o.Status()) {
			continue
		}
		if q != "" && !outcomeMatchesQuery(o, m.gitInfo[o.CWD], q) {
			continue
		}
		items = append(items, InboxItem{IsOutcome: true, OutcomeIdx: oi})
	}
	sort.SliceStable(items, func(i, j int) bool {
		stI, stJ := m.inboxItemStatus(items[i]), m.inboxItemStatus(items[j])
		if inboxStatusPriority(stI) != inboxStatusPriority(stJ) {
			return inboxStatusPriority(stI) < inboxStatusPriority(stJ)
		}
		if items[i].IsOutcome != items[j].IsOutcome {
			return !items[i].IsOutcome
		}
		if items[i].IsOutcome {
			return m.outcomes[items[i].OutcomeIdx].TsMs > m.outcomes[items[j].OutcomeIdx].TsMs
		}
		si, sj := m.spaces[items[i].SpaceIdx], m.spaces[items[j].SpaceIdx]
		pi, pj := si.Panes[items[i].PaneIdx], sj.Panes[items[j].PaneIdx]
		if strings.ToLower(si.Name) != strings.ToLower(sj.Name) {
			return strings.ToLower(si.Name) < strings.ToLower(sj.Name)
		}
		if pi.WindowIndex != pj.WindowIndex {
			return pi.WindowIndex < pj.WindowIndex
		}
		return pi.NumericID < pj.NumericID
	})
	return items
}

func outcomeMatchesQuery(o model.OutcomeEntry, g *model.GitInfo, q string) bool {
	if strings.Contains(strings.ToLower(o.Session), q) ||
		strings.Contains(strings.ToLower(o.Agent), q) ||
		strings.Contains(strings.ToLower(o.App), q) ||
		strings.Contains(strings.ToLower(o.Profile), q) ||
		strings.Contains(strings.ToLower(o.Outcome), q) ||
		strings.Contains(strings.ToLower(o.Status().Label()), q) ||
		strings.Contains(strings.ToLower(o.Title), q) ||
		strings.Contains(strings.ToLower(o.Text), q) ||
		strings.Contains(strings.ToLower(o.Model), q) ||
		strings.Contains(strings.ToLower(o.CWD), q) ||
		strings.Contains(strings.ToLower(o.SessionID), q) {
		return true
	}
	if g != nil {
		if g.Branch != "" && strings.Contains(strings.ToLower(g.Branch), q) {
			return true
		}
		if g.IsWorktree {
			if q == "worktree" || q == "wt" ||
				(g.WorktreeName != "" && strings.Contains(strings.ToLower(g.WorktreeName), q)) ||
				(g.RepoName != "" && strings.Contains(strings.ToLower(g.RepoName), q)) {
				return true
			}
		}
	}
	return false
}

func inboxMatchesQuery(sp model.Space, p model.Pane, q string) bool {
	if strings.Contains(strings.ToLower(p.App), q) ||
		strings.Contains(strings.ToLower(p.Profile), q) ||
		strings.Contains(strings.ToLower(sp.Name), q) ||
		strings.Contains(strings.ToLower(p.ConversationTitle), q) ||
		strings.Contains(strings.ToLower(p.ScreenTitle), q) ||
		strings.Contains(strings.ToLower(p.Title), q) ||
		strings.Contains(strings.ToLower(p.WindowName), q) ||
		strings.Contains(strings.ToLower(p.Text), q) ||
		strings.Contains(strings.ToLower(p.CWD), q) ||
		strings.Contains(strings.ToLower(p.Status.Label()), q) {
		return true
	}
	if p.Hook != nil {
		if strings.Contains(strings.ToLower(p.Hook.Title), q) ||
			strings.Contains(strings.ToLower(p.Hook.Text), q) ||
			strings.Contains(strings.ToLower(p.Hook.Event), q) ||
			strings.Contains(strings.ToLower(p.Hook.Model), q) ||
			strings.Contains(strings.ToLower(p.Hook.WaitingOn), q) {
			return true
		}
		for _, bg := range p.Hook.AllBackgroundWork() {
			if strings.Contains(strings.ToLower(bg.Kind), q) ||
				strings.Contains(strings.ToLower(bg.Role), q) ||
				strings.Contains(strings.ToLower(bg.Category), q) ||
				strings.Contains(strings.ToLower(bg.Command), q) ||
				strings.Contains(strings.ToLower(bg.Text), q) ||
				strings.Contains(strings.ToLower(bg.Outcome), q) {
				return true
			}
		}
	}
	if p.Git != nil {
		if p.Git.Branch != "" && strings.Contains(strings.ToLower(p.Git.Branch), q) {
			return true
		}
		if p.Git.IsWorktree {
			if q == "worktree" || q == "wt" ||
				(p.Git.WorktreeName != "" && strings.Contains(strings.ToLower(p.Git.WorktreeName), q)) ||
				(p.Git.RepoName != "" && strings.Contains(strings.ToLower(p.Git.RepoName), q)) {
				return true
			}
		}
		if p.Git.PR != nil {
			if p.Git.PR.Number > 0 && strings.Contains(fmt.Sprintf("#%d", p.Git.PR.Number), q) {
				return true
			}
			if p.Git.PR.Title != "" && strings.Contains(strings.ToLower(p.Git.PR.Title), q) {
				return true
			}
		}
	}
	if sp.Git != nil {
		if sp.Git.Branch != "" && strings.Contains(strings.ToLower(sp.Git.Branch), q) {
			return true
		}
		if sp.Git.IsWorktree && (q == "worktree" || q == "wt" ||
			(sp.Git.WorktreeName != "" && strings.Contains(strings.ToLower(sp.Git.WorktreeName), q))) {
			return true
		}
	}
	return false
}

func (m Model) inboxLayout(width, height int) inboxLayout {
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
	return inboxLayout{
		topY:         topY,
		bodyHeight:   bodyHeight,
		showDividers: showDividers,
		detailLines:  detailLines,
	}
}

func (m *Model) activateInboxItem(idx int) {
	items := m.inboxItems()
	if idx < 0 || idx >= len(items) {
		return
	}
	item := items[idx]
	if item.IsOutcome {
		if item.OutcomeIdx < 0 || item.OutcomeIdx >= len(m.outcomes) {
			return
		}
		ob, ok := m.backend.(OutcomesBackend)
		if !ok {
			m.toast = "Cannot open outcome conversation."
			return
		}
		if err := ob.OpenOutcome(context.Background(), m.outcomes[item.OutcomeIdx]); err != nil {
			m.toast = err.Error()
			return
		}
		m.quitting = true
		return
	}
	sp := m.spaces[item.SpaceIdx]
	p := sp.Panes[item.PaneIdx]
	if err := m.backend.FocusPane(context.Background(), sp.Name, p.WindowID, p.ID); err != nil {
		m.toast = err.Error()
		return
	}
	m.quitting = true
}

func (m *Model) moveInboxSelection(delta int) {
	items := m.inboxItems()
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

// ---------------------------------------------------------------------------
// key + mouse handling
// ---------------------------------------------------------------------------

func (m Model) handleInbox(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inboxSearching {
		return m.handleInboxSearch(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		if strings.TrimSpace(m.query) != "" {
			m.query = ""
			m.cursor = 0
			m.selected = 0
			m.clampSelection()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		m.activateInboxItem(m.selected)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp:
		m.moveInboxSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveInboxSelection(1)
		return m, nil
	case tea.KeyRight:
		t := true
		cmd := m.toggleExpandSelectedPane(&t)
		return m, cmd
	case tea.KeyLeft:
		f := false
		cmd := m.toggleExpandSelectedPane(&f)
		return m, cmd
	case tea.KeyPgUp:
		m.moveInboxSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveInboxSelection(10)
		return m, nil
	case tea.KeyHome:
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyEnd:
		items := m.inboxItems()
		m.selected = len(items) - 1
		m.clampSelection()
		return m, nil
	case tea.KeyTab:
		m.filter = nextFilter(m.filter, false)
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeySpace:
		cmd := m.toggleExpandSelectedPane(nil)
		return m, cmd
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		ch := string(msg.Runes)
		switch ch {
		case "k":
			m.moveInboxSelection(-1)
		case "j":
			m.moveInboxSelection(1)
		case "q":
			if strings.TrimSpace(m.query) != "" {
				m.query = ""
				m.cursor = 0
				m.selected = 0
				m.clampSelection()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "/":
			m.inboxSearching = true
		case "?":
			m.returnMode = ModeInbox
			m.mode = ModeHelp
		case "H":
			cmd := m.openHookDetail()
			return m, cmd
		case "e", " ":
			cmd := m.toggleExpandSelectedPane(nil)
			return m, cmd
		case "l":
			t := true
			cmd := m.toggleExpandSelectedPane(&t)
			return m, cmd
		case "h":
			f := false
			cmd := m.toggleExpandSelectedPane(&f)
			return m, cmd
		case "b":
			m.setFilter(model.FilterNeeds)
		case "w":
			m.setFilter(model.FilterWorking)
		case "d":
			m.setFilter(model.FilterDone)
		case "i":
			m.setFilter(model.FilterIdle)
		case "a":
			m.filter = model.FilterAll
			m.selected = 0
			m.clampSelection()
		case "g":
			m.selected = 0
			m.clampSelection()
		case "G":
			items := m.inboxItems()
			m.selected = len(items) - 1
			m.clampSelection()
		default:
			if len(ch) == 1 && ch[0] >= '1' && ch[0] <= '9' {
				idx := int(ch[0] - '1')
				items := m.inboxItems()
				if idx < len(items) {
					m.selected = idx
				}
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleInboxSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if strings.TrimSpace(m.query) != "" {
			m.query = ""
			m.cursor = 0
			m.selected = 0
			m.inboxSearching = false
			m.clampSelection()
			return m, nil
		}
		m.inboxSearching = false
		return m, nil
	case tea.KeyEnter:
		m.inboxSearching = false
		m.activateInboxItem(m.selected)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp:
		m.moveInboxSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveInboxSelection(1)
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
		for _, r := range msg.Runes {
			m.insertRune(r)
		}
		m.selected = 0
		m.clampSelection()
		return m, nil
	}
	return m, nil
}

func (m Model) handleInboxLeftClick(x, y int) (tea.Model, tea.Cmd) {
	_ = x
	if y == searchLineY() {
		m.inboxSearching = true
		return m, nil
	}
	items := m.inboxItems()
	if len(items) == 0 {
		return m, nil
	}
	layout := m.inboxLayout(m.width, m.height)
	if y < layout.topY || y >= layout.topY+layout.bodyHeight {
		return m, nil
	}
	entries := m.buildInboxListEntries(items)
	selEntry := inboxSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), layout.bodyHeight)
	pos := start + (y - layout.topY)
	if pos < 0 || pos >= len(entries) || entries[pos].isHeader || entries[pos].isSpacer {
		return m, nil
	}
	idx := entries[pos].itemIdx
	m.selected = idx
	m.activateInboxItem(idx)
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

func (m Model) renderInbox(width, height int) string {
	layout := m.inboxLayout(width, height)
	items := m.inboxItems()

	var b strings.Builder
	b.WriteString(m.inboxHeaderLine(width))
	b.WriteString("\n")
	b.WriteString(m.inboxSearchLine(width))
	b.WriteString("\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	lines := m.renderInboxList(items, width, layout.bodyHeight)
	for i := 0; i < layout.bodyHeight; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width))
			b.WriteString("\n")
		}
		for _, dl := range m.renderInboxDetailLines(items, width, layout.detailLines) {
			b.WriteString(dl)
			b.WriteString("\n")
		}
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.inboxHintsLine(width))
	return b.String()
}

func (m Model) renderInboxList(items []InboxItem, width, height int) []string {
	lines := make([]string, height)
	if len(items) == 0 {
		msg := "No active agents match this filter."
		if m.spacesLoading && len(m.spaces) == 0 && len(m.outcomes) == 0 {
			msg = "Loading agents…"
		} else if m.totalAgentCount() == 0 && len(m.outcomes) == 0 {
			msg = "No running agents found."
		}
		lines[0] = fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width)
		for i := 1; i < height; i++ {
			lines[i] = fitCellWidth("", width)
		}
		return lines
	}

	entries := m.buildInboxListEntries(items)
	selEntry := inboxSelectedEntryIdx(entries, m.selected)
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
		selected := e.itemIdx == m.selected
		item := items[e.itemIdx]
		if item.IsOutcome {
			lines[i] = m.renderInboxOutcomeRow(m.outcomes[item.OutcomeIdx], selected, false, width)
			continue
		}
		sp := m.spaces[item.SpaceIdx]
		p := sp.Panes[item.PaneIdx]
		switch e.subLine {
		case 0:
			lines[i] = m.renderUnifiedPanePrimaryRow(sp, p, selected, false, true, width)
		case 1:
			lines[i] = m.renderUnifiedPaneActionRow(sp, p, selected, false, true, width)
		case 2:
			bgItems := m.paneBackgroundItems(sp.Name, p)
			if e.bgIdx >= 0 && e.bgIdx < len(bgItems) {
				lines[i] = m.renderInlineBackgroundRow(bgItems[e.bgIdx], e.bgLast, selected, false, width)
			} else {
				lines[i] = fitCellWidth("", width)
			}
		}
	}
	return lines
}

// renderInboxOutcomeRow renders one recent completion/failure entry aligned
// with the live agent columns:
//
//	Left:  `▌   <icon>  <Agent [— Profile]>  <Workspace>  <Title or summary>`
//	Right: `[model]  ↺ <status (fixed)> <relative time>`
func (m Model) renderInboxOutcomeRow(o model.OutcomeEntry, selected, altBG bool, width int) string {
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
	innerW := width - 3
	if innerW < 10 {
		innerW = 10
	}

	status := o.Status()
	now := m.opts.NowOrDefault()
	timeStr := formatOutcomeTime(o.TsMs, now)

	tailStyled := renderStatusTail(status, timeStr, true, bg, st)
	tailW := ansi.StringWidth(tailStyled)

	var rb strings.Builder
	usedRight := tailW
	if g := m.gitInfo[o.CWD]; g != nil && innerW >= 72 {
		if brBadge := renderGitBranchBadge(g, 18, st); brBadge != "" {
			if w := ansi.StringWidth(brBadge) + 2; usedRight+w+42 <= innerW {
				rb.WriteString(brBadge)
				rb.WriteString(st(bg, false).Render("  "))
				usedRight += w
			}
		}
	}
	if mdl := strings.TrimSpace(o.Model); mdl != "" && innerW >= 78 {
		modelBadge := st(ColorSubtext0, false).Render(truncate(mdl, 20))
		if w := ansi.StringWidth(modelBadge) + 2; usedRight+w+40 <= innerW {
			rb.WriteString(modelBadge)
			rb.WriteString(st(bg, false).Render("  "))
		}
	}
	rb.WriteString(tailStyled)
	rightStyled := rb.String()
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW - rightW - 2
	if maxLeft < 10 {
		maxLeft = 10
	}

	baseApp := o.DisplayApp()
	if baseApp == "" {
		baseApp = "agent"
	}
	maxAppW := maxAppLabelWidth(width)
	appColW := m.appColumnWidth(true, maxAppW)
	spaceColW := m.spaceColumnWidth(14)

	spaceName := strings.TrimSpace(o.Session)
	if spaceName == "" {
		spaceName = "-"
	}
	spaceCol := truncate(spaceName, spaceColW)
	if sw := ansi.StringWidth(spaceCol); sw < spaceColW {
		spaceCol += strings.Repeat(" ", spaceColW-sw)
	}
	isCurrentSpace := false
	for _, sp := range m.spaces {
		if sp.IsCurrent && sp.Name == spaceName {
			isCurrentSpace = true
			break
		}
	}
	spaceFg := ColorSubtext0
	if isCurrentSpace {
		spaceFg = ColorLavender
	}

	summary := strings.TrimSpace(o.Title)
	if summary == "" {
		summary = strings.TrimSpace(o.Text)
	}
	if summary == "" {
		summary = shortenPath(o.CWD, m.opts.HomeDir)
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(st(StatusColor(status), true).Render(status.Icon()))
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(renderStyledAppCol(baseApp, o.Profile, appColW, maxAppW, selected, bg, st))
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(st(spaceFg, selected || isCurrentSpace).Render(spaceCol))
	usedW := 5 + appColW + 2 + spaceColW
	titleBudget := maxLeft - usedW - 2
	if summary != "" && titleBudget > 3 {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorText, selected || status.IsAttention()).Render(ansi.Truncate(summary, titleBudget, "…")))
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

func formatOutcomeTime(tsMs int64, now time.Time) string {
	if tsMs <= 0 {
		return ""
	}
	diffMs := now.UnixMilli() - tsMs
	if diffMs < 1000 {
		return "now"
	}
	return formatAge(diffMs)
}

func formatOutcomeDetailTime(tsMs int64, now time.Time) string {
	if tsMs <= 0 {
		return ""
	}
	clock := time.UnixMilli(tsMs).Local().Format("15:04")
	rel := formatOutcomeTime(tsMs, now)
	if rel == "" {
		return clock
	}
	return clock + " (" + rel + ")"
}

func inboxCardActionText(p model.Pane, primaryTitle string) (text, color string) {
	rawText := strings.TrimSpace(p.Text)
	if rawText == "" && p.Hook != nil {
		rawText = strings.TrimSpace(p.Hook.Text)
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" && p.Hook != nil {
		kind = strings.TrimSpace(p.Hook.Kind)
	}
	event := ""
	if p.Hook != nil {
		event = strings.TrimSpace(p.Hook.Event)
	}

	color = ColorSubtext0
	switch p.Status {
	case model.StatusNeedsYou:
		color = ColorBlue
	case model.StatusFailed:
		color = ColorRed
	case model.StatusWorking:
		color = ColorYellow
	case model.StatusDone:
		color = ColorGreen
	}

	if kind != "" && rawText != "" && !strings.EqualFold(rawText, primaryTitle) {
		return kind + ": " + rawText, color
	}
	if kind != "" {
		return kind, color
	}
	if rawText != "" && !strings.EqualFold(rawText, primaryTitle) {
		return rawText, color
	}
	if event != "" && !strings.EqualFold(event, primaryTitle) {
		return event, ColorOverlay0
	}
	return "", ColorOverlay0
}

func inboxAgentTitle(p model.Pane, homeDir string) string {
	if t := paneConversationName(p); t != "" {
		return t
	}
	if t := strings.TrimSpace(p.Text); t != "" {
		return t
	}
	if p.WindowName != "" {
		return p.WindowName
	}
	if p.App != "" {
		return p.App
	}
	return shortenPath(p.CWD, homeDir)
}

func (m Model) renderInboxDetailLines(items []InboxItem, width, maxLines int) []string {
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
		label := "No active agents."
		if m.spacesLoading && len(m.spaces) == 0 && len(m.outcomes) == 0 {
			label = "Loading agents…"
		}
		empty := st(ColorOverlay0, false).Render(label)
		if maxLines <= 1 {
			return []string{padLine(empty)}
		}
		return []string{padLine(empty), padLine("")}
	}
	item := items[m.selected]
	if item.IsOutcome {
		o := m.outcomes[item.OutcomeIdx]
		status := o.Status()
		var l1 []string
		if s := strings.TrimSpace(o.Session); s != "" {
			l1 = append(l1, st(ColorLavender, true).Render(s))
		}
		if agent := formatAppLabel(o.DisplayApp(), o.Profile); agent != "" {
			l1 = append(l1, renderStyledAppCol(o.DisplayApp(), o.Profile, 0, 32, false, bg, st))
		}
		l1 = append(l1, st(StatusColor(status), status.IsAttention()).Render(status.Icon()+" "+status.Label()))
		if mdl := strings.TrimSpace(o.Model); mdl != "" {
			l1 = append(l1, st(ColorSubtext0, false).Render("model "+mdl))
		}
		if cwd := shortenPath(o.CWD, m.opts.HomeDir); cwd != "" {
			l1 = append(l1, st(ColorOverlay0, false).Render(cwd))
		}
		if sid := strings.TrimSpace(o.SessionID); sid != "" {
			l1 = append(l1, st(ColorOverlay0, false).Render("session "+sid))
		}

		var l2 []string
		if g := m.gitInfo[o.CWD]; g != nil {
			if gs := renderGitDetailStyled(g, bg, st); gs != "" {
				l2 = append(l2, gs)
			}
		}
		if txt := strings.TrimSpace(o.Text); txt != "" {
			l2 = append(l2, st(ColorText, false).Render(txt))
		}
		if ts := formatOutcomeDetailTime(o.TsMs, m.opts.NowOrDefault()); ts != "" {
			l2 = append(l2, st(ColorOverlay0, false).Render(ts))
		}
		l2 = append(l2, st(ColorLavender, false).Render("Enter/click: open in new tab"))

		if maxLines <= 1 {
			return []string{padLine(strings.Join(append(l1, l2...), sep))}
		}
		return []string{padLine(strings.Join(l1, sep)), padLine(strings.Join(l2, sep))}
	}
	sp := m.spaces[item.SpaceIdx]
	p := sp.Panes[item.PaneIdx]
	return renderPaneDetailLines(sp, p, m.opts.HomeDir, width, maxLines)
}

func (m Model) inboxDetailLine(items []InboxItem, width int) string {
	return strings.Join(m.renderInboxDetailLines(items, width, 2), "\n")
}

func (m Model) totalAgentCount() int {
	n := 0
	for _, sp := range m.spaces {
		for _, p := range sp.Panes {
			if p.IsAgent() {
				n++
			}
		}
	}
	return n
}

func (m Model) inboxHeaderLine(width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	var counts model.Counts
	total := 0
	liveSIDs, livePanes := m.inboxLiveConversationSets()
	for _, sp := range m.spaces {
		for _, p := range sp.Panes {
			if !p.IsAgent() {
				continue
			}
			total++
			counts.Add(p.Status)
		}
	}
	for _, o := range m.outcomes {
		if outcomeIsLive(o, liveSIDs, livePanes) {
			continue
		}
		total++
		counts.Add(o.Status())
	}
	noun := "agents"
	if total == 1 {
		noun = "agent"
	}
	segs := []string{st(ColorSubtext0, false).Render(fmt.Sprintf("%d %s", total, noun))}
	if counts.Needs > 0 {
		segs = append(segs, st(ColorBlue, true).Render(fmt.Sprintf("● %d needs you", counts.Needs)))
	}
	if counts.Failed > 0 {
		segs = append(segs, st(ColorRed, true).Render(fmt.Sprintf("✗ %d failed", counts.Failed)))
	}
	if counts.Working > 0 {
		segs = append(segs, st(ColorYellow, false).Render(fmt.Sprintf("◐ %d working", counts.Working)))
	}
	if counts.Done > 0 {
		segs = append(segs, st(ColorGreen, false).Render(fmt.Sprintf("✓ %d done", counts.Done)))
	}
	return renderHeaderBar("Inbox", segs, m.filter, width)
}

func (m Model) inboxSearchLine(width int) string {
	matchText := ""
	if strings.TrimSpace(m.query) != "" {
		n := len(m.inboxItems())
		noun := "matches"
		if n == 1 {
			noun = "match"
		}
		matchText = fmt.Sprintf("%d %s", n, noun)
	}
	return renderSearchBar(m.query, m.inboxSearching, "Filter agents by name, space, branch, title, status…", matchText, width)
}

// fitCellWidth guarantees s renders as a single terminal line of exact
// visual cell width, preserving ANSI SGR sequences and resetting at the end
// so styles never bleed across columns or lines.
func fitCellWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	w := ansi.StringWidth(s)
	if w > width {
		s = ansi.Truncate(s, width, "")
		w = ansi.StringWidth(s)
	}
	if w < width {
		return s + "\x1b[0m" + strings.Repeat(" ", width-w)
	}
	return s + "\x1b[0m"
}

func fitCellWidthWithBG(s string, width int, bgHex string) string {
	if width <= 0 {
		return ""
	}
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	w := ansi.StringWidth(s)
	if w > width {
		s = ansi.Truncate(s, width, "")
		w = ansi.StringWidth(s)
	}
	if w < width {
		pad := lipgloss.NewStyle().Background(lipgloss.Color(bgHex)).Render(strings.Repeat(" ", width-w))
		return s + pad + "\x1b[0m"
	}
	return s + "\x1b[0m"
}

func (m Model) inboxHintsLine(width int) string {
	if m.inboxSearching {
		return renderHintsBar([][2]string{
			{"type", "filter"},
			{"↑↓", "select"},
			{"Enter", "open"},
			{"Esc", "done"},
		}, width)
	}
	if width < 72 {
		return renderHintsBar([][2]string{
			{"↑↓", "move"},
			{"Enter", "open"},
			{"/", "filter"},
			{"Esc", "close"},
		}, width)
	}
	return renderHintsBar([][2]string{
		{"↑↓", "move"},
		{"Enter", "open"},
		{"/", "filter"},
		{"b/w/d/i/a", "status"},
		{"e", "expand"},
		{"H", "hooks"},
		{"?", "keys"},
		{"Esc", "close"},
	}, width)
}

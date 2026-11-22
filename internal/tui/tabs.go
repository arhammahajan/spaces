package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

// TabSortMode controls how tabs are ordered in ModeTabs.
type TabSortMode int

const (
	TabSortIndex TabSortMode = iota
	TabSortAttention
	TabSortActivity
)

// Label returns the short pill label for the sort mode.
func (s TabSortMode) Label() string {
	switch s {
	case TabSortAttention:
		return "attention"
	case TabSortActivity:
		return "activity"
	default:
		return "index"
	}
}

func (s TabSortMode) next() TabSortMode {
	switch s {
	case TabSortIndex:
		return TabSortAttention
	case TabSortAttention:
		return TabSortActivity
	default:
		return TabSortIndex
	}
}

// TabItem aggregates one tmux window (tab) and all of its panes in the current
// workspace, along with rollup status, counts, background work, and git context.
type TabItem struct {
	SpaceIdx         int
	Window           model.Window
	Panes            []model.Pane
	Counts           model.Counts
	Status           model.Status
	PrimaryPaneIdx   int
	ActivePaneIdx    int
	AgentCount       int
	BgActiveCount    int
	BgServiceCount   int
	BgTotalCount     int
	HasDifferentCWDs bool
	CWD              string
	Git              *model.GitInfo
	LatestActivityMs int64
}

type tabsLayout struct {
	topY         int
	bodyHeight   int
	split        bool
	leftWidth    int
	rightWidth   int
	showDividers bool
	detailLines  int
}

type tabCardEntry struct {
	tabIdx  int
	subLine int // 0 = primary, 1 = meta/git, 2 = live action/cmd
	spacer  bool
}

type tabPaneHitRow struct {
	y       int
	xStart  int
	xEnd    int
	paneIdx int
}

func (m Model) isTabsContext() bool {
	return m.mode == ModeTabs || m.returnMode == ModeTabs
}

// tabsSpace resolves the target workspace for ModeTabs: explicit --session
// Target if set, otherwise the current tmux session, otherwise the first space.
func (m Model) tabsSpace() (int, *model.Space) {
	if len(m.spaces) == 0 {
		return -1, nil
	}
	if target := strings.TrimSpace(m.opts.Target); target != "" {
		for i := range m.spaces {
			if m.spaces[i].Name == target {
				return i, &m.spaces[i]
			}
		}
		for i := range m.spaces {
			if strings.EqualFold(m.spaces[i].Name, target) {
				return i, &m.spaces[i]
			}
		}
		return -1, nil
	}
	for i := range m.spaces {
		if m.spaces[i].IsCurrent {
			return i, &m.spaces[i]
		}
	}
	return 0, &m.spaces[0]
}

// allWorkspaceTabs builds the complete unfiltered slice of TabItems for the
// target workspace.
func (m Model) allWorkspaceTabs() (int, *model.Space, []TabItem) {
	si, sp := m.tabsSpace()
	if sp == nil {
		return -1, nil, nil
	}
	nowMs := m.opts.NowOrDefault().UnixMilli()

	// Group panes by WindowID (falling back to WindowIndex key when ID is absent).
	panesByWin := make(map[string][]model.Pane)
	winKey := func(id string, idx int) string {
		if strings.TrimSpace(id) != "" {
			return id
		}
		return fmt.Sprintf("idx:%d", idx)
	}
	for _, p := range sp.Panes {
		k := winKey(p.WindowID, p.WindowIndex)
		panesByWin[k] = append(panesByWin[k], p)
	}

	windows := sp.Windows
	if len(windows) == 0 && len(sp.Panes) > 0 {
		// Synthesize windows from panes when a snapshot/test omitted Windows.
		seen := make(map[string]bool)
		for _, p := range sp.Panes {
			k := winKey(p.WindowID, p.WindowIndex)
			if seen[k] {
				continue
			}
			seen[k] = true
			windows = append(windows, model.Window{
				ID:     p.WindowID,
				Index:  p.WindowIndex,
				Name:   p.WindowName,
				Active: p.WindowActive,
			})
		}
		sort.SliceStable(windows, func(i, j int) bool {
			return windows[i].Index < windows[j].Index
		})
	}

	tabs := make([]TabItem, 0, len(windows))
	for _, w := range windows {
		k := winKey(w.ID, w.Index)
		panes := append([]model.Pane(nil), panesByWin[k]...)
		sort.SliceStable(panes, func(i, j int) bool {
			if panes[i].PaneIndex != panes[j].PaneIndex {
				return panes[i].PaneIndex < panes[j].PaneIndex
			}
			return panes[i].NumericID < panes[j].NumericID
		})

		tab := TabItem{
			SpaceIdx: si,
			Window:   w,
			Panes:    panes,
		}
		if !w.ActivityAt.IsZero() {
			tab.LatestActivityMs = w.ActivityAt.UnixMilli()
		}

		activeIdx := -1
		primaryIdx := -1
		primaryScore := 1 << 30
		firstCWD := ""

		for pi, p := range panes {
			tab.Counts.Add(p.Status)
			if p.IsAgent() {
				tab.AgentCount++
			}
			if p.IsActive && activeIdx < 0 {
				activeIdx = pi
			}
			if cwd := strings.TrimSpace(p.CWD); cwd != "" {
				if firstCWD == "" {
					firstCWD = cwd
				} else if cwd != firstCWD {
					tab.HasDifferentCWDs = true
				}
			}

			// Aggregate background work.
			bgItems := m.paneBackgroundItems(sp.Name, p)
			if len(bgItems) > 0 {
				tab.BgTotalCount += len(bgItems)
				for _, item := range bgItems {
					if !item.IsActive() {
						continue
					}
					if item.IsFinite() {
						tab.BgActiveCount++
					} else {
						tab.BgServiceCount++
					}
				}
			} else if p.Hook != nil {
				tab.BgActiveCount += p.Hook.FiniteActive
				if p.Hook.FiniteActive == 0 {
					tab.BgActiveCount += p.Hook.ChildCount + p.Hook.Background
				}
				tab.BgServiceCount += p.Hook.ServiceActive
				tab.BgTotalCount += p.Hook.ChildCount + p.Hook.Background + p.Hook.ServiceActive
			}

			// Track latest activity timestamp across panes.
			if p.Hook != nil && p.Hook.TsMs > tab.LatestActivityMs {
				tab.LatestActivityMs = p.Hook.TsMs
			}
			if p.AgeMs > 0 && nowMs > p.AgeMs {
				if ts := nowMs - p.AgeMs; ts > tab.LatestActivityMs {
					tab.LatestActivityMs = ts
				}
			}

			// Pick primary pane: urgent agent > active agent > active pane > any agent > first pane.
			score := inboxStatusPriority(p.Status) * 10
			if !p.IsAgent() {
				score += 50
			}
			if !p.IsActive {
				score += 5
			}
			if score < primaryScore {
				primaryScore = score
				primaryIdx = pi
			}
		}

		if activeIdx < 0 {
			activeIdx = 0
		}
		if primaryIdx < 0 {
			primaryIdx = activeIdx
		}
		tab.ActivePaneIdx = activeIdx
		tab.PrimaryPaneIdx = primaryIdx
		tab.Status = tab.Counts.Rollup()

		if len(panes) > 0 {
			pp := panes[primaryIdx]
			tab.CWD = pp.CWD
			tab.Git = pp.Git
			if tab.CWD == "" {
				tab.CWD = panes[activeIdx].CWD
			}
			if tab.Git == nil {
				tab.Git = panes[activeIdx].Git
			}
			if tab.Git == nil {
				for _, p := range panes {
					if p.Git != nil {
						tab.Git = p.Git
						break
					}
				}
			}
		}
		if tab.CWD == "" {
			tab.CWD = sp.CWD
		}
		if tab.Git == nil {
			tab.Git = sp.Git
		}

		tabs = append(tabs, tab)
	}
	return si, sp, tabs
}

// tabsItems returns the filtered and sorted TabItems for the current workspace.
func (m Model) tabsItems() []TabItem {
	_, sp, all := m.allWorkspaceTabs()
	if sp == nil || len(all) == 0 {
		return nil
	}
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(m.query)))
	var out []TabItem
	for _, tab := range all {
		if !tabMatchesStatusFilter(tab, m.filter) {
			continue
		}
		if len(tokens) > 0 && !tabMatchesQuery(tab, tokens) {
			continue
		}
		out = append(out, tab)
	}

	switch m.tabsSort {
	case TabSortAttention:
		sort.SliceStable(out, func(i, j int) bool {
			pi, pj := inboxStatusPriority(out[i].Status), inboxStatusPriority(out[j].Status)
			if pi != pj {
				return pi < pj
			}
			if out[i].Window.Active != out[j].Window.Active {
				return out[i].Window.Active
			}
			return out[i].Window.Index < out[j].Window.Index
		})
	case TabSortActivity:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Window.Active != out[j].Window.Active {
				return out[i].Window.Active
			}
			if out[i].LatestActivityMs != out[j].LatestActivityMs {
				return out[i].LatestActivityMs > out[j].LatestActivityMs
			}
			return out[i].Window.Index < out[j].Window.Index
		})
	default:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Window.Index != out[j].Window.Index {
				return out[i].Window.Index < out[j].Window.Index
			}
			return out[i].Window.ID < out[j].Window.ID
		})
	}
	return out
}

func tabMatchesStatusFilter(tab TabItem, f model.AttentionFilter) bool {
	if f == model.FilterAll {
		return true
	}
	if f.Matches(tab.Status) {
		return true
	}
	for _, p := range tab.Panes {
		if f.Matches(p.Status) {
			return true
		}
	}
	return false
}

func tabMatchesQuery(tab TabItem, tokens []string) bool {
	for _, tok := range tokens {
		if !tabMatchesToken(tab, tok) {
			return false
		}
	}
	return true
}

func tabMatchesToken(tab TabItem, tok string) bool {
	idxStr := strconv.Itoa(tab.Window.Index)
	if tok == idxStr || tok == idxStr+":" || tok == "#"+idxStr {
		return true
	}
	if strings.Contains(strings.ToLower(tab.Window.Name), tok) ||
		strings.Contains(strings.ToLower(tab.Window.ID), tok) ||
		strings.Contains(strings.ToLower(tab.Window.WorktreeName), tok) ||
		strings.Contains(strings.ToLower(tab.Window.WorktreePath), tok) ||
		strings.Contains(strings.ToLower(tab.CWD), tok) ||
		strings.Contains(strings.ToLower(tab.Status.Label()), tok) {
		return true
	}
	if (tab.Window.WorktreePath != "" || tab.Window.WorktreeName != "") && (tok == "wt" || strings.HasPrefix("worktree", tok)) {
		return true
	}
	if tab.Window.Active && strings.HasPrefix("active", tok) {
		return true
	}
	if tab.Window.Zoomed && strings.HasPrefix("zoomed", tok) {
		return true
	}
	if tab.Window.Last && tok == "last" {
		return true
	}
	if tab.Window.Bell && tok == "bell" {
		return true
	}
	if gitMatchesToken(tab.Git, tok) {
		return true
	}
	for _, p := range tab.Panes {
		if strings.Contains(strings.ToLower(p.ID), tok) ||
			strings.Contains(strings.ToLower(p.App), tok) ||
			strings.Contains(strings.ToLower(p.Profile), tok) ||
			strings.Contains(strings.ToLower(p.ConversationTitle), tok) ||
			strings.Contains(strings.ToLower(p.ScreenTitle), tok) ||
			strings.Contains(strings.ToLower(p.Title), tok) ||
			strings.Contains(strings.ToLower(p.Command), tok) ||
			strings.Contains(strings.ToLower(p.StartCommand), tok) ||
			strings.Contains(strings.ToLower(p.ForegroundCmd), tok) ||
			strings.Contains(strings.ToLower(p.Text), tok) ||
			strings.Contains(strings.ToLower(p.Kind), tok) ||
			strings.Contains(strings.ToLower(p.CWD), tok) ||
			strings.Contains(strings.ToLower(p.Status.Label()), tok) ||
			gitMatchesToken(p.Git, tok) {
			return true
		}
		if p.Hook != nil {
			if strings.Contains(strings.ToLower(p.Hook.Title), tok) ||
				strings.Contains(strings.ToLower(p.Hook.Text), tok) ||
				strings.Contains(strings.ToLower(p.Hook.Model), tok) ||
				strings.Contains(strings.ToLower(p.Hook.Event), tok) ||
				strings.Contains(strings.ToLower(p.Hook.WaitingOn), tok) ||
				strings.Contains(strings.ToLower(p.Hook.SessionID), tok) {
				return true
			}
			for _, bg := range p.Hook.AllBackgroundWork() {
				if strings.Contains(strings.ToLower(bg.Kind), tok) ||
					strings.Contains(strings.ToLower(bg.Role), tok) ||
					strings.Contains(strings.ToLower(bg.Category), tok) ||
					strings.Contains(strings.ToLower(bg.Command), tok) ||
					strings.Contains(strings.ToLower(bg.Text), tok) ||
					strings.Contains(strings.ToLower(bg.Outcome), tok) {
					return true
				}
			}
		}
	}
	return false
}

func gitMatchesToken(g *model.GitInfo, tok string) bool {
	if g == nil {
		return false
	}
	if g.Branch != "" && strings.Contains(strings.ToLower(g.Branch), tok) {
		return true
	}
	if g.BaseBranch != "" && strings.Contains(strings.ToLower(g.BaseBranch), tok) {
		return true
	}
	if g.WorktreeName != "" && strings.Contains(strings.ToLower(g.WorktreeName), tok) {
		return true
	}
	if g.RepoName != "" && strings.Contains(strings.ToLower(g.RepoName), tok) {
		return true
	}
	if g.IsWorktree && (tok == "wt" || strings.HasPrefix("worktree", tok)) {
		return true
	}
	if g.PR != nil {
		if g.PR.Number > 0 && strings.Contains(fmt.Sprintf("#%d", g.PR.Number), tok) {
			return true
		}
		if g.PR.Title != "" && strings.Contains(strings.ToLower(g.PR.Title), tok) {
			return true
		}
		if g.PR.State != "" && strings.Contains(strings.ToLower(g.PR.State), tok) {
			return true
		}
	}
	if g.Checks != nil && g.Checks.FailingName != "" && strings.Contains(strings.ToLower(g.Checks.FailingName), tok) {
		return true
	}
	return false
}

func (m Model) selectedTab() *TabItem {
	items := m.tabsItems()
	if m.selected < 0 || m.selected >= len(items) {
		return nil
	}
	t := items[m.selected]
	return &t
}

func (m Model) selectedTabPaneIdx(tab TabItem) int {
	if len(tab.Panes) == 0 {
		return -1
	}
	if m.tabsPaneSel != nil {
		if idx, ok := m.tabsPaneSel[tab.Window.ID]; ok && idx >= 0 && idx < len(tab.Panes) {
			return idx
		}
	}
	if tab.PrimaryPaneIdx >= 0 && tab.PrimaryPaneIdx < len(tab.Panes) {
		return tab.PrimaryPaneIdx
	}
	return 0
}

func (m Model) selectedTabPane(tab TabItem) *model.Pane {
	idx := m.selectedTabPaneIdx(tab)
	if idx < 0 || idx >= len(tab.Panes) {
		return nil
	}
	p := tab.Panes[idx]
	return &p
}

func (m *Model) selectInitialActiveTab() {
	if m.tabsInitSelected {
		return
	}
	items := m.tabsItems()
	if len(items) == 0 {
		return
	}
	m.tabsInitSelected = true
	if strings.TrimSpace(m.query) != "" {
		return
	}
	for i, t := range items {
		if t.Window.Active {
			m.selected = i
			return
		}
	}
}

func (m *Model) moveTabsSelection(delta int) {
	items := m.tabsItems()
	if len(items) == 0 {
		m.selected = 0
		m.tabsInspectorScroll = 0
		return
	}
	prev := m.selected
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(items) {
		m.selected = len(items) - 1
	}
	if m.selected != prev {
		m.tabsInspectorScroll = 0
	}
}

func (m *Model) cycleTabPaneSelection(delta int) {
	tab := m.selectedTab()
	if tab == nil || len(tab.Panes) <= 1 {
		return
	}
	if m.tabsPaneSel == nil {
		m.tabsPaneSel = map[string]int{}
	}
	cur := m.selectedTabPaneIdx(*tab)
	next := (cur + delta + len(tab.Panes)) % len(tab.Panes)
	m.tabsPaneSel[tab.Window.ID] = next
	m.tabsInspectorScroll = 0
}

func (m *Model) scrollTabsInspector(delta int) {
	m.tabsInspectorScroll += delta
	if m.tabsInspectorScroll < 0 {
		m.tabsInspectorScroll = 0
	}
}

func (m *Model) selectTabByDigit(digit int) bool {
	items := m.tabsItems()
	if len(items) == 0 {
		return false
	}
	// First match exact tmux window index (0..9).
	for i, t := range items {
		if t.Window.Index == digit {
			if m.selected != i {
				m.tabsInspectorScroll = 0
			}
			m.selected = i
			return true
		}
	}
	// Fallback to 1-based list position for digits 1..9.
	if digit >= 1 && digit-1 < len(items) {
		if m.selected != digit-1 {
			m.tabsInspectorScroll = 0
		}
		m.selected = digit - 1
		return true
	}
	return false
}

func (m *Model) activateTabItem(idx int) {
	items := m.tabsItems()
	if idx < 0 || idx >= len(items) {
		return
	}
	tab := items[idx]
	if tab.SpaceIdx < 0 || tab.SpaceIdx >= len(m.spaces) {
		return
	}
	sp := m.spaces[tab.SpaceIdx]
	paneID := ""
	if p := m.selectedTabPane(tab); p != nil {
		paneID = p.ID
	}
	if err := m.backend.FocusPane(context.Background(), sp.Name, tab.Window.ID, paneID); err != nil {
		m.toast = err.Error()
		return
	}
	m.quitting = true
}

func (m *Model) requestTabRename() {
	tab := m.selectedTab()
	if tab == nil {
		m.toast = "Select a tab to rename."
		return
	}
	m.returnMode = ModeTabs
	m.renameWindow = true
	m.renameStandalone = false
	m.renameWindowID = tab.Window.ID
	m.renameOld = tab.Window.Name
	m.renameText = tab.Window.Name
	m.renameCur = len([]rune(tab.Window.Name))
	m.renameError = ""
	m.mode = ModeRename
}

func (m *Model) prefillNewTab() tea.Cmd {
	_, sp := m.tabsSpace()
	if sp == nil {
		if m.newCWD == "" && m.opts.HomeDir != "" {
			m.newCWD = m.opts.HomeDir
			m.newCWDCur = len([]rune(m.newCWD))
		}
		return m.initNewSpaceWorktrees()
	}
	m.newTabSession = sp.Name
	if m.newCWD == "" {
		cwd := sp.CWD
		if tab := m.selectedTab(); tab != nil && tab.CWD != "" {
			cwd = tab.CWD
		}
		if cwd == "" {
			cwd = m.opts.HomeDir
		}
		m.newCWD = cwd
		m.newCWDCur = len([]rune(cwd))
	}
	return m.initNewSpaceWorktrees()
}

func (m *Model) requestNewTab() tea.Cmd {
	_, sp := m.tabsSpace()
	if sp == nil {
		m.toast = "No active workspace found."
		return nil
	}
	cwd := sp.CWD
	if tab := m.selectedTab(); tab != nil && tab.CWD != "" {
		cwd = tab.CWD
	}
	if cwd == "" {
		cwd = m.opts.HomeDir
	}
	m.returnMode = ModeTabs
	m.newIsTab = true
	m.newTabSession = sp.Name
	m.newName = ""
	m.newNameCur = 0
	m.newNameEdited = false
	m.newCWD = cwd
	m.newCWDCur = len([]rune(cwd))
	m.newField = 0
	m.newError = ""
	m.mode = ModeNewSpace
	return m.initNewSpaceWorktrees()
}

func (m *Model) requestTabKill() tea.Cmd {
	m.confirmWorktreePath = ""
	m.confirmWorktreeName = ""
	m.confirmWorktreeBranch = ""
	m.confirmWorktreeManaged = false
	m.confirmRemoveWorktree = false
	m.confirmWorktreeSafety = WorktreeRemoveSafety{}

	_, sp, all := m.allWorkspaceTabs()
	tab := m.selectedTab()
	if sp == nil || tab == nil {
		return nil
	}
	busy := tab.Counts.Working > 0 || tab.Counts.Needs > 0
	m.returnMode = ModeTabs
	m.confirmWindow = true
	m.confirmWindowID = tab.Window.ID
	m.confirmWindowName = tab.Window.Name
	m.confirmSpace = sp.Name
	m.confirmPanes = len(tab.Panes)
	m.confirmWindowBusy = busy
	m.confirmWindowLast = len(all) <= 1
	m.confirmExited = false
	m.mode = ModeConfirmKill

	// Detect if this tab is backed by a linked git worktree not shared by any other open tab.
	wtPath := tab.Window.WorktreePath
	wtName := tab.Window.WorktreeName
	wtBranch := ""
	wtManaged := tab.Window.WorktreeManaged
	if tab.Git != nil && tab.Git.IsWorktree {
		if wtPath == "" {
			wtPath = tab.Git.RepoRoot
		}
		if wtName == "" {
			wtName = tab.Git.WorktreeName
		}
		wtBranch = tab.Git.Branch
		if tab.Git.Managed {
			wtManaged = true
		}
	}
	if wtPath != "" && !m.worktreeUsedByOtherWindow(tab.Window.ID, wtPath) {
		m.confirmWorktreePath = wtPath
		m.confirmWorktreeName = wtName
		if m.confirmWorktreeName == "" {
			m.confirmWorktreeName = filepath.Base(wtPath)
		}
		m.confirmWorktreeBranch = wtBranch
		m.confirmWorktreeManaged = wtManaged
		return m.inspectWorktreeSafetyCmd(wtPath)
	}
	return nil
}

func tabsHookKey(session string, paneNum int) string {
	return session + "\x00" + strconv.Itoa(paneNum)
}

func hookDetailStale(summary *model.HookSummary, cached *model.HookDetail) bool {
	if summary == nil || cached == nil {
		return true
	}
	if summary.TsMs > 0 && cached.TsMs > 0 {
		return summary.TsMs > cached.TsMs
	}
	return summary.Event != cached.Event || summary.State != cached.State
}

// syncTabsInspectorCmd fetches full hook detail asynchronously for the
// currently selected tab's active pane when available.
func (m *Model) syncTabsInspectorCmd(force bool) tea.Cmd {
	if m.mode != ModeTabs {
		return nil
	}
	tab := m.selectedTab()
	if tab == nil || tab.SpaceIdx < 0 || tab.SpaceIdx >= len(m.spaces) {
		return nil
	}
	sp := m.spaces[tab.SpaceIdx]
	p := m.selectedTabPane(*tab)
	if p == nil {
		return nil
	}
	if p.Hook != nil && p.Hook.HasDetail && p.NumericID >= 0 {
		inFlightKey := tabsHookKey(sp.Name, p.NumericID)
		if !force && m.tabsHookInFlight == inFlightKey {
			return nil
		}
		key := paneBgKey(sp.Name, p.ID)
		var cached *model.HookDetail
		if m.bgDetails != nil {
			cached = m.bgDetails[key]
		}
		if force || cached == nil || hookDetailStale(p.Hook, cached) {
			m.tabsHookInFlight = inFlightKey
			return m.loadHookDetailCmd(sp.Name, p.NumericID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// key + mouse handling
// ---------------------------------------------------------------------------

func (m Model) handleTabs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.tabsSearching {
		return m.handleTabsSearch(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if strings.TrimSpace(m.query) != "" {
			m.query = ""
			m.cursor = 0
			m.selected = 0
			m.tabsInspectorScroll = 0
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		m.activateTabItem(m.selected)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveTabsSelection(-1)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveTabsSelection(1)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyLeft:
		m.cycleTabPaneSelection(-1)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyRight:
		m.cycleTabPaneSelection(1)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyCtrlD:
		m.scrollTabsInspector(6)
		return m, nil
	case tea.KeyCtrlU:
		m.scrollTabsInspector(-6)
		return m, nil
	case tea.KeyPgUp:
		m.moveTabsSelection(-8)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyPgDown:
		m.moveTabsSelection(8)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyHome:
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyEnd:
		items := m.tabsItems()
		m.selected = len(items) - 1
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyTab:
		m.filter = nextFilter(m.filter, false)
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyShiftTab:
		m.filter = nextFilter(m.filter, true)
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		ch := string(msg.Runes)
		switch ch {
		case "k":
			m.moveTabsSelection(-1)
			return m, m.syncTabsInspectorCmd(false)
		case "j":
			m.moveTabsSelection(1)
			return m, m.syncTabsInspectorCmd(false)
		case "J", "]":
			m.scrollTabsInspector(3)
			return m, nil
		case "K", "[":
			m.scrollTabsInspector(-3)
			return m, nil
		case "h":
			m.cycleTabPaneSelection(-1)
			return m, m.syncTabsInspectorCmd(false)
		case "l":
			m.cycleTabPaneSelection(1)
			return m, m.syncTabsInspectorCmd(false)
		case "q":
			if strings.TrimSpace(m.query) != "" {
				m.query = ""
				m.cursor = 0
				m.selected = 0
				m.tabsInspectorScroll = 0
				m.clampSelection()
				return m, m.syncTabsInspectorCmd(false)
			}
			m.quitting = true
			return m, tea.Quit
		case "/":
			m.tabsSearching = true
			return m, nil
		case "?":
			m.returnMode = ModeTabs
			m.mode = ModeHelp
			return m, nil
		case "H":
			cmd := m.openHookDetail()
			return m, cmd
		case "n":
			return m, m.requestNewTab()
		case "W":
			return m, m.openWorktreesView()
		case "r":
			m.requestTabRename()
			return m, nil
		case "x":
			return m, m.requestTabKill()
		case "s":
			m.tabsSort = m.tabsSort.next()
			m.tabsInspectorScroll = 0
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		case "v":
			m.tabsTelemetryExpanded = !m.tabsTelemetryExpanded
			return m, nil
		case "b":
			m.setFilter(model.FilterNeeds)
			m.tabsInspectorScroll = 0
			return m, m.syncTabsInspectorCmd(false)
		case "w":
			m.setFilter(model.FilterWorking)
			m.tabsInspectorScroll = 0
			return m, m.syncTabsInspectorCmd(false)
		case "d":
			m.setFilter(model.FilterDone)
			m.tabsInspectorScroll = 0
			return m, m.syncTabsInspectorCmd(false)
		case "i":
			m.setFilter(model.FilterIdle)
			m.tabsInspectorScroll = 0
			return m, m.syncTabsInspectorCmd(false)
		case "a":
			m.filter = model.FilterAll
			m.selected = 0
			m.tabsInspectorScroll = 0
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		case "g":
			m.selected = 0
			m.tabsInspectorScroll = 0
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		case "G":
			items := m.tabsItems()
			m.selected = len(items) - 1
			m.tabsInspectorScroll = 0
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		default:
			if len(ch) == 1 && ch[0] >= '0' && ch[0] <= '9' {
				if m.selectTabByDigit(int(ch[0] - '0')) {
					return m, m.syncTabsInspectorCmd(false)
				}
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleTabsSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if strings.TrimSpace(m.query) != "" {
			m.query = ""
			m.cursor = 0
			m.selected = 0
			m.tabsInspectorScroll = 0
			m.tabsSearching = false
			m.clampSelection()
			return m, m.syncTabsInspectorCmd(false)
		}
		m.tabsSearching = false
		return m, nil
	case tea.KeyEnter:
		m.tabsSearching = false
		m.activateTabItem(m.selected)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveTabsSelection(-1)
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveTabsSelection(1)
		return m, m.syncTabsInspectorCmd(false)
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
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyDelete:
		m.deleteRuneAt()
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyCtrlU:
		m.query = ""
		m.cursor = 0
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyCtrlW:
		m.deleteWord()
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeySpace:
		m.insertRune(' ')
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			m.insertRune(r)
		}
		m.selected = 0
		m.tabsInspectorScroll = 0
		m.clampSelection()
		return m, m.syncTabsInspectorCmd(false)
	}
	return m, nil
}

func (m Model) handleTabsWheel(x, delta int) (tea.Model, tea.Cmd) {
	layout := m.tabsLayout(m.width, m.height)
	if layout.split && x > layout.leftWidth {
		m.scrollTabsInspector(delta * 2)
		return m, nil
	}
	m.moveTabsSelection(delta)
	return m, m.syncTabsInspectorCmd(false)
}

func (m Model) handleTabsLeftClick(x, y int) (tea.Model, tea.Cmd) {
	if y == searchLineY() {
		m.tabsSearching = true
		return m, nil
	}
	items := m.tabsItems()
	if len(items) == 0 {
		return m, nil
	}
	layout := m.tabsLayout(m.width, m.height)
	if y < layout.topY || y >= layout.topY+layout.bodyHeight {
		return m, nil
	}
	// Click inside right inspector panel: allow clicking a pane pill in multi-pane tabs.
	if layout.split && x > layout.leftWidth {
		tab := m.selectedTab()
		if tab != nil && len(tab.Panes) > 1 && tab.SpaceIdx >= 0 && tab.SpaceIdx < len(m.spaces) {
			_, hits := m.renderTabInspectorWithHits(m.spaces[tab.SpaceIdx], *tab, layout.rightWidth, layout.bodyHeight)
			relX := x - (layout.leftWidth + 1)
			relY := y - layout.topY
			for _, h := range hits {
				if h.y == relY && relX >= h.xStart && relX < h.xEnd && h.paneIdx >= 0 && h.paneIdx < len(tab.Panes) {
					if m.tabsPaneSel == nil {
						m.tabsPaneSel = map[string]int{}
					}
					prev := m.selectedTabPaneIdx(*tab)
					m.tabsPaneSel[tab.Window.ID] = h.paneIdx
					if prev != h.paneIdx {
						m.tabsInspectorScroll = 0
					}
					now := time.Now()
					if prev == h.paneIdx && m.lastClickRow == -(h.paneIdx+1) && now.Sub(m.lastClickAt) < 500*time.Millisecond {
						m.activateTabItem(m.selected)
						if m.quitting {
							return m, tea.Quit
						}
						return m, nil
					}
					m.lastClickRow = -(h.paneIdx + 1)
					m.lastClickAt = now
					return m, m.syncTabsInspectorCmd(false)
				}
			}
		}
		return m, nil
	}

	entries := m.buildTabCardEntries(items)
	selEntry := tabsSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), layout.bodyHeight)
	pos := start + (y - layout.topY)
	if pos < 0 || pos >= len(entries) || entries[pos].spacer {
		return m, nil
	}
	idx := entries[pos].tabIdx
	now := time.Now()
	if m.selected == idx && m.lastClickRow == idx && now.Sub(m.lastClickAt) < 500*time.Millisecond {
		m.activateTabItem(idx)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.selected != idx {
		m.tabsInspectorScroll = 0
	}
	m.selected = idx
	m.lastClickRow = idx
	m.lastClickAt = now
	return m, m.syncTabsInspectorCmd(false)
}

// ---------------------------------------------------------------------------
// layout + rendering
// ---------------------------------------------------------------------------

func (m Model) tabsLayout(width, height int) tabsLayout {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	showDividers := height >= 12
	topY := 2 // header + search
	if showDividers {
		topY = 3 // + top hairline divider
	}
	split := width >= 90
	detailLines := 0
	if !split {
		if height >= 16 {
			detailLines = 2
		} else if height >= 12 {
			detailLines = 1
		}
	}
	reserved := topY + 1 // + hints line
	if detailLines > 0 {
		if showDividers {
			reserved++
		}
		reserved += detailLines
	} else if showDividers {
		reserved++ // bottom hairline divider separating body from hints
	}
	if m.toast != "" {
		reserved++
	}
	bodyHeight := height - reserved
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	leftW := width
	rightW := 0
	if split {
		if m.tabsTelemetryExpanded {
			leftW = width * 30 / 100
			if leftW < 28 {
				leftW = 28
			}
			if leftW > 42 {
				leftW = 42
			}
		} else {
			leftW = width * 42 / 100
			if leftW < 36 {
				leftW = 36
			}
			if leftW > 58 {
				leftW = 58
			}
		}
		rightW = width - leftW - 1 // 1 cell for vertical hairline divider
		if rightW < 34 {
			split = false
			leftW = width
			rightW = 0
		}
	}
	return tabsLayout{
		topY:         topY,
		bodyHeight:   bodyHeight,
		split:        split,
		leftWidth:    leftW,
		rightWidth:   rightW,
		showDividers: showDividers,
		detailLines:  detailLines,
	}
}

func (m Model) renderTabs(width, height int) string {
	layout := m.tabsLayout(width, height)
	items := m.tabsItems()
	_, sp := m.tabsSpace()

	var b strings.Builder
	b.WriteString(m.tabsHeaderLine(width))
	b.WriteString("\n")
	b.WriteString(m.tabsSearchLine(items, width))
	b.WriteString("\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	if layout.split {
		leftLines := m.renderTabsList(items, layout.leftWidth, layout.bodyHeight)
		var rightLines []string
		if sp != nil && len(items) > 0 && m.selected >= 0 && m.selected < len(items) {
			rightLines, _ = m.renderTabInspectorWithHits(*sp, items[m.selected], layout.rightWidth, layout.bodyHeight)
		} else {
			rightLines = m.renderTabInspectorEmpty(layout.rightWidth, layout.bodyHeight)
		}
		vDiv := styleDivider.Render("│")
		for i := 0; i < layout.bodyHeight; i++ {
			b.WriteString(leftLines[i])
			b.WriteString(vDiv)
			b.WriteString(rightLines[i])
			b.WriteString("\n")
		}
	} else {
		lines := m.renderTabsList(items, width, layout.bodyHeight)
		for i := 0; i < layout.bodyHeight; i++ {
			b.WriteString(lines[i])
			b.WriteString("\n")
		}
	}

	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width))
			b.WriteString("\n")
		}
		for _, dl := range m.renderTabFooterLines(sp, items, width, layout.detailLines) {
			b.WriteString(dl)
			b.WriteString("\n")
		}
	} else if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.tabsHintsLine(width))
	return b.String()
}

func (m Model) tabsHeaderLine(width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	_, sp, all := m.allWorkspaceTabs()
	title := "Tabs"
	if sp != nil && sp.Name != "" {
		title = "Tabs · " + sp.Name
	}

	var counts model.Counts
	paneTotal := 0
	seenWT := map[string]bool{}
	for _, tab := range all {
		paneTotal += len(tab.Panes)
		if p := strings.TrimSpace(tab.Window.WorktreePath); p != "" {
			seenWT[filepath.Clean(p)] = true
		} else if tab.Git != nil && tab.Git.IsWorktree && tab.Git.RepoRoot != "" {
			seenWT[filepath.Clean(tab.Git.RepoRoot)] = true
		}
		for _, p := range tab.Panes {
			counts.Add(p.Status)
			if p.Git != nil && p.Git.IsWorktree && p.Git.RepoRoot != "" {
				seenWT[filepath.Clean(p.Git.RepoRoot)] = true
			}
		}
	}

	tabNoun := "tabs"
	if len(all) == 1 {
		tabNoun = "tab"
	}
	paneNoun := "panes"
	if paneTotal == 1 {
		paneNoun = "pane"
	}
	var segs []string
	if paneTotal > 0 && paneTotal != len(all) {
		segs = append(segs, st(ColorSubtext0, false).Render(fmt.Sprintf("%d %s · %d %s", len(all), tabNoun, paneTotal, paneNoun)))
	} else {
		segs = append(segs, st(ColorSubtext0, false).Render(fmt.Sprintf("%d %s", len(all), tabNoun)))
	}
	if len(seenWT) > 0 {
		wtNoun := "wt"
		if len(seenWT) > 1 {
			wtNoun = "wts"
		}
		segs = append(segs, st(ColorMauve, false).Render(fmt.Sprintf("⎇ %d %s", len(seenWT), wtNoun)))
	}
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
	if m.tabsSort != TabSortIndex {
		segs = append(segs, st(ColorMauve, false).Render("sort:"+m.tabsSort.Label()))
	}
	return renderHeaderBar(title, segs, m.filter, width)
}

func (m Model) tabsSearchLine(items []TabItem, width int) string {
	var rightParts []string
	if strings.TrimSpace(m.query) != "" || m.filter != model.FilterAll {
		noun := "tabs"
		if len(items) == 1 {
			noun = "tab"
		}
		rightParts = append(rightParts, fmt.Sprintf("%d %s", len(items), noun))
	}
	rightParts = append(rightParts, "sort:"+m.tabsSort.Label())
	matchText := strings.Join(rightParts, " · ")
	return renderSearchBar(m.query, m.tabsSearching, "Filter tabs by index, name, agent, title, cmd, branch, worktree, path…", matchText, width)
}

func (m Model) buildTabCardEntries(items []TabItem) []tabCardEntry {
	if len(items) == 0 {
		return nil
	}
	entries := make([]tabCardEntry, 0, len(items)*3)
	for i, tab := range items {
		if i > 0 {
			entries = append(entries, tabCardEntry{spacer: true})
		}
		entries = append(entries, tabCardEntry{tabIdx: i, subLine: 0})
		entries = append(entries, tabCardEntry{tabIdx: i, subLine: 1})
		if tabCardActionLine(tab, m.opts.HomeDir) != "" {
			entries = append(entries, tabCardEntry{tabIdx: i, subLine: 2})
		}
	}
	return entries
}

func tabsSelectedEntryIdx(entries []tabCardEntry, selected int) int {
	for i, e := range entries {
		if !e.spacer && e.tabIdx == selected && e.subLine == 0 {
			return i
		}
	}
	return 0
}

func (m Model) renderTabsList(items []TabItem, width, height int) []string {
	lines := make([]string, height)
	if len(items) == 0 {
		msg := "No tabs match this filter."
		if m.spacesLoading && len(m.spaces) == 0 {
			msg = "Loading tabs…"
		} else if _, sp := m.tabsSpace(); sp == nil {
			msg = "No running workspace found."
		} else if _, _, all := m.allWorkspaceTabs(); len(all) == 0 {
			msg = "No open tabs in this workspace."
		}
		lines[0] = fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width)
		for i := 1; i < height; i++ {
			lines[i] = fitCellWidth("", width)
		}
		return lines
	}

	entries := m.buildTabCardEntries(items)
	selEntry := tabsSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), height)

	for i := 0; i < height; i++ {
		idx := start + i
		if idx >= len(entries) {
			lines[i] = fitCellWidth("", width)
			continue
		}
		e := entries[idx]
		if e.spacer {
			lines[i] = fitCellWidth("", width)
			continue
		}
		tab := items[e.tabIdx]
		selected := e.tabIdx == m.selected
		switch e.subLine {
		case 0:
			lines[i] = m.renderTabCardPrimary(tab, selected, width)
		case 1:
			lines[i] = m.renderTabCardMeta(tab, selected, width)
		case 2:
			lines[i] = m.renderTabCardAction(tab, selected, width)
		}
	}
	return lines
}

// renderTabCardPrimary renders Line 1 of a tab card in the left list:
//
//	Left:  `▌ 0:* ● tabName — Conversation Title [Z]`
//	Right: `[2p] <status> <age>`
func (m Model) renderTabCardPrimary(tab TabItem, selected bool, width int) string {
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

	var p *model.Pane
	if len(tab.Panes) > 0 {
		p = m.selectedTabPane(tab)
	}
	ageStr := tabDisplayAge(tab, p, m.opts.NowOrDefault())

	var rb strings.Builder
	if len(tab.Panes) > 1 {
		rb.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf("%dp", len(tab.Panes))))
		rb.WriteString(st(bg, false).Render(" "))
	}
	if width >= 46 {
		lbl := tab.Status.Label()
		if tab.Status == model.StatusNeedsYou && width < 52 {
			lbl = "needs"
		}
		rb.WriteString(st(StatusColor(tab.Status), tab.Status.IsAttention()).Render(lbl))
		if ageStr != "" {
			rb.WriteString(st(ColorOverlay0, false).Render(" " + ageStr))
		}
	} else if ageStr != "" {
		rb.WriteString(st(ColorOverlay0, false).Render(ageStr))
	}
	rightStyled := rb.String()
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW
	if rightW > 0 {
		maxLeft = innerW - rightW - 2
	}
	if maxLeft < 8 {
		maxLeft = 8
	}

	var lb strings.Builder
	idxFg := ColorOverlay0
	if tab.Window.Active {
		idxFg = ColorLavender
	}
	lb.WriteString(st(idxFg, tab.Window.Active || selected).Render(fmt.Sprintf("%d:", tab.Window.Index)))
	switch {
	case tab.Window.Active:
		lb.WriteString(st(ColorLavender, true).Render("*"))
	case tab.Window.Last:
		lb.WriteString(st(ColorOverlay0, false).Render("-"))
	default:
		lb.WriteString(st(bg, false).Render(" "))
	}
	lb.WriteString(st(bg, false).Render(" "))
	lb.WriteString(st(StatusColor(tab.Status), tab.Status != model.StatusIdle).Render(tab.Status.Icon()))
	lb.WriteString(st(bg, false).Render(" "))

	tabTitle := tabPrimaryTitle(tab, p)
	titleFg := ColorText
	if !selected && tab.Status == model.StatusIdle && !tab.Window.Active {
		titleFg = ColorSubtext0
	}
	titleBold := selected || tab.Window.Active || tab.Status.IsAttention()

	used := ansi.StringWidth(lb.String())
	flagsStr := tabInlineFlags(tab.Window)
	flagsW := 0
	if flagsStr != "" {
		flagsW = ansi.StringWidth(flagsStr) + 1
	}
	titleBudget := maxLeft - used - flagsW
	if titleBudget < 4 {
		titleBudget = maxLeft - used
		flagsStr = ""
	}
	if titleBudget > 0 {
		lb.WriteString(st(titleFg, titleBold).Render(ansi.Truncate(tabTitle, titleBudget, "…")))
	}
	if flagsStr != "" {
		lb.WriteString(st(bg, false).Render(" "))
		lb.WriteString(st(ColorMauve, false).Render(flagsStr))
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

// renderTabCardMeta renders Line 2 of a tab card: app/profile, background work
// badge, git branch/diff/PR/CI, and shortened path.
func (m Model) renderTabCardMeta(tab TabItem, selected bool, width int) string {
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

	p := m.selectedTabPane(tab)
	dot := st(ColorSurface1, false).Render(" · ")
	var parts []string

	if p != nil {
		appLabel := formatAppLabel(p.App, p.Profile)
		if appLabel != "" {
			parts = append(parts, renderStyledAppCol(p.App, p.Profile, 0, 22, selected, bg, st))
		}
	}
	if len(tab.Panes) > 1 && tab.AgentCount > 1 {
		parts = append(parts, st(ColorBlue, false).Render(fmt.Sprintf("%d agents", tab.AgentCount)))
	}
	if tab.BgActiveCount > 0 || tab.BgServiceCount > 0 {
		var bgParts []string
		if tab.BgActiveCount > 0 {
			bgParts = append(bgParts, fmt.Sprintf("+%d bg", tab.BgActiveCount))
		}
		if tab.BgServiceCount > 0 {
			bgParts = append(bgParts, fmt.Sprintf("%d svc", tab.BgServiceCount))
		}
		parts = append(parts, st(ColorBlue, true).Render(strings.Join(bgParts, " ")))
	}

	if tab.Git != nil {
		var gitParts []string
		if br := renderGitBranchBadge(tab.Git, 18, st); br != "" {
			gitParts = append(gitParts, br)
		}
		if diff := renderGitDiffBadge(tab.Git, bg, st); diff != "" {
			gitParts = append(gitParts, diff)
		}
		if pr := renderGitPRBadge(tab.Git, bg, st); pr != "" {
			gitParts = append(gitParts, pr)
		}
		if len(gitParts) > 0 {
			parts = append(parts, strings.Join(gitParts, st(bg, false).Render(" ")))
		}
	} else if wtName := strings.TrimSpace(tab.Window.WorktreeName); wtName != "" {
		parts = append(parts, st(ColorMauve, true).Render("⎇ ")+st(ColorTeal, false).Render(truncate(wtName, 18)))
	}

	if cwd := shortenPath(tab.CWD, m.opts.HomeDir); cwd != "" && innerW >= 46 {
		parts = append(parts, st(ColorOverlay0, false).Render(truncate(cwd, 24)))
	}

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("     "))
	budget := innerW - 5
	used := 0
	for i, part := range parts {
		pw := ansi.StringWidth(part)
		sepW := 0
		if i > 0 {
			sepW = 3
		}
		if i > 0 && used+sepW+pw > budget {
			break
		}
		if i > 0 {
			lb.WriteString(dot)
			used += sepW
		}
		if pw > budget-used {
			part = ansi.Truncate(part, budget-used, "…")
			pw = ansi.StringWidth(part)
		}
		lb.WriteString(part)
		used += pw
	}

	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	out.WriteString(ansi.Truncate(lb.String(), innerW, "…"))
	if selected {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

// renderTabCardAction renders Line 3 of a tab card when the tab has live
// agent action/blocker text or a distinct foreground command.
func (m Model) renderTabCardAction(tab TabItem, selected bool, width int) string {
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

	text, color := tabCardActionWithColor(tab, m.opts.HomeDir)
	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	out.WriteString(st(bg, false).Render("     "))
	out.WriteString(st(ColorSurface1, false).Render("↳ "))
	rem := innerW - 7
	if rem > 3 && text != "" {
		out.WriteString(st(color, tab.Status.IsAttention()).Render(ansi.Truncate(text, rem, "…")))
	}
	if selected {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

func tabCardActionLine(tab TabItem, homeDir string) string {
	txt, _ := tabCardActionWithColor(tab, homeDir)
	return txt
}

func tabCardActionWithColor(tab TabItem, homeDir string) (string, string) {
	if len(tab.Panes) == 0 {
		return "", ColorOverlay0
	}
	p := tab.Panes[tab.PrimaryPaneIdx]
	primary := tabPrimaryTitle(tab, &p)
	if p.IsAgent() {
		if txt, col := inboxCardActionText(p, primary); txt != "" {
			return txt, col
		}
		if p.Hook != nil && strings.TrimSpace(p.Hook.WaitingOn) != "" {
			return p.Hook.WaitingOn, ColorBlue
		}
	}
	if fg := strings.TrimSpace(p.ForegroundCmd); fg != "" && !strings.EqualFold(fg, p.App) && !strings.EqualFold(fg, tab.Window.Name) {
		return fg, ColorSubtext0
	}
	if p.IsDead {
		return fmt.Sprintf("pane exited (status %d)", p.DeadStatus), ColorRed
	}
	_ = homeDir
	return "", ColorOverlay0
}

func tabPrimaryTitle(tab TabItem, p *model.Pane) string {
	winName := strings.TrimSpace(tab.Window.Name)
	conv := ""
	if p != nil {
		conv = paneConversationName(*p)
		if conv == "" && !p.IsAgent() {
			if t := cleanConversationTitle(p.Title, p.App, p.Command, p.CWD); t != "" {
				conv = t
			}
		}
	}
	switch {
	case winName != "" && conv != "" && !strings.EqualFold(winName, conv):
		if p != nil && (strings.EqualFold(winName, p.App) || strings.EqualFold(winName, p.Profile) || isGenericShellName(winName)) {
			return conv
		}
		return winName + " — " + conv
	case conv != "":
		return conv
	case winName != "":
		return winName
	case p != nil && p.App != "":
		return p.App
	default:
		return tab.Window.ID
	}
}

func tabInlineFlags(w model.Window) string {
	var flags []string
	if w.Zoomed {
		flags = append(flags, "Z")
	}
	if w.Bell {
		flags = append(flags, "!")
	}
	if w.Activity && !w.Active {
		flags = append(flags, "#")
	}
	if w.Silence {
		flags = append(flags, "~")
	}
	if len(flags) == 0 {
		return ""
	}
	return "[" + strings.Join(flags, "") + "]"
}

func tabDisplayAge(tab TabItem, p *model.Pane, now time.Time) string {
	if p != nil && p.AgeMs > 0 {
		return formatAge(p.AgeMs)
	}
	if tab.LatestActivityMs > 0 {
		if d := now.UnixMilli() - tab.LatestActivityMs; d > 0 {
			return formatAge(d)
		}
	}
	return ""
}

// wrapTextLines splits raw text by newlines and wraps long lines cleanly at
// word boundaries to maxWidth cells, capping at maxLines with an ellipsis.
func wrapTextLines(raw string, maxWidth, maxLines int) []string {
	if maxWidth < 8 {
		maxWidth = 8
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	paragraphs := strings.Split(strings.TrimSpace(raw), "\n")
	var lines []string
	truncated := false

	for _, para := range paragraphs {
		para = strings.TrimRight(para, " \t")
		if para == "" {
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			continue
		}
		cur := ""
		for _, w := range words {
			if cur == "" {
				if ansi.StringWidth(w) > maxWidth {
					cur = truncate(w, maxWidth)
				} else {
					cur = w
				}
				continue
			}
			cand := cur + " " + w
			if ansi.StringWidth(cand) <= maxWidth {
				cur = cand
			} else {
				lines = append(lines, cur)
				if maxLines > 0 && len(lines) >= maxLines {
					truncated = true
					cur = ""
					break
				}
				if ansi.StringWidth(w) > maxWidth {
					cur = truncate(w, maxWidth)
				} else {
					cur = w
				}
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
		if maxLines > 0 && len(lines) >= maxLines {
			if len(lines) > maxLines {
				lines = lines[:maxLines]
				truncated = true
			}
			break
		}
	}
	if truncated && len(lines) > 0 {
		last := lines[len(lines)-1]
		if !strings.HasSuffix(last, "…") {
			lines[len(lines)-1] = truncate(last, maxWidth-1) + "…"
		}
	}
	return lines
}

func (m Model) renderTabFooterLines(sp *model.Space, items []TabItem, width, maxLines int) []string {
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

	if sp == nil || len(items) == 0 || m.selected < 0 || m.selected >= len(items) {
		label := "No tabs available."
		if m.spacesLoading && len(m.spaces) == 0 {
			label = "Loading tabs…"
		}
		empty := st(ColorOverlay0, false).Render(label)
		if maxLines <= 1 {
			return []string{padLine(empty)}
		}
		return []string{padLine(empty), padLine("")}
	}
	tab := items[m.selected]
	p := m.selectedTabPane(tab)
	if p != nil {
		return renderPaneDetailLines(*sp, *p, m.opts.HomeDir, width, maxLines)
	}
	l1 := st(ColorLavender, true).Render(fmt.Sprintf("%d: %s", tab.Window.Index, tab.Window.Name))
	if cwd := shortenPath(tab.CWD, m.opts.HomeDir); cwd != "" {
		l1 += sep + st(ColorOverlay0, false).Render(cwd)
	}
	if maxLines <= 1 {
		return []string{padLine(l1)}
	}
	return []string{padLine(l1), padLine(st(ColorOverlay0, false).Render("Enter focus · n new · r rename · x close"))}
}

func (m Model) tabsHintsLine(width int) string {
	if m.tabsSearching {
		return renderHintsBar([][2]string{
			{"type", "filter"},
			{"↑↓", "select"},
			{"Enter", "focus"},
			{"Esc", "done"},
		}, width)
	}
	if width < 78 {
		return renderHintsBar([][2]string{
			{"↑↓", "tab"},
			{"←→", "pane"},
			{"Enter", "focus"},
			{"/", "filter"},
			{"n/r/x", "manage"},
			{"?", "keys"},
			{"Esc", "close"},
		}, width)
	}
	return renderHintsBar([][2]string{
		{"↑↓", "tab"},
		{"←→", "pane"},
		{"J/K", "scroll"},
		{"Enter", "focus"},
		{"/", "filter"},
		{"s", "sort"},
		{"v", "expand"},
		{"n", "new"},
		{"W", "worktrees"},
		{"r", "rename"},
		{"x", "close"},
		{"H", "hooks"},
		{"?", "keys"},
		{"Esc", "close"},
	}, width)
}

func relativeRepoLabel(repoRoot, cwd, homeDir string) string {
	repoRoot = filepath.Clean(strings.TrimSpace(repoRoot))
	if repoRoot == "" || repoRoot == "." {
		return ""
	}
	base := filepath.Base(repoRoot)
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	if cwd != "" && cwd != repoRoot {
		if rel, err := filepath.Rel(repoRoot, cwd); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return base + "/" + rel
		}
	}
	if base != "" && base != "/" {
		return base
	}
	return shortenPath(repoRoot, homeDir)
}

func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

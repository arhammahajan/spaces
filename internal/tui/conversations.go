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

const convSearchDebounce = 80 * time.Millisecond

// Internal messages for ModeConversations.
type (
	conversationsMsg struct {
		snap    model.ConversationSnapshot
		refresh bool
		err     string
	}
	convSearchDebounceMsg struct {
		seq int
	}
	convSearchResultMsg struct {
		seq   int
		query string
		hits  []model.ConversationSearchHit
		err   string
	}
)

type convLivePane struct {
	SpaceIdx int
	PaneIdx  int
	Space    string
	WindowID string
	PaneID   string
	Status   model.Status
}

type convItem struct {
	Conv            model.ConversationEntry
	Live            *convLivePane
	FuzzyScore      int
	TitleHighlights []int
	Hit             *model.ConversationSearchHit
}

type convLayout struct {
	topY         int
	bodyHeight   int
	showDividers bool
	detailLines  int
}

type convListEntry struct {
	itemIdx int
	subLine int // 0 = primary row, 1 = optional FTS5 snippet line
}

func convKey(agent, id string) string {
	return model.CanonicalConversationAgent(agent) + "\x00" + strings.TrimSpace(id)
}

func (m Model) loadConversationsCmd(refresh, includeArchived bool) tea.Cmd {
	cb, ok := m.backend.(ConversationsBackend)
	if !ok {
		return func() tea.Msg {
			return conversationsMsg{refresh: refresh, err: "Conversations backend not available."}
		}
	}
	return func() tea.Msg {
		snap, err := cb.LoadConversations(context.Background(), refresh, includeArchived)
		if err != nil {
			return conversationsMsg{refresh: refresh, err: err.Error()}
		}
		return conversationsMsg{snap: snap, refresh: refresh}
	}
}

func convDebounceCmd(seq int) tea.Cmd {
	return tea.Tick(convSearchDebounce, func(time.Time) tea.Msg {
		return convSearchDebounceMsg{seq: seq}
	})
}

func (m Model) searchConversationsCmd(seq int, req model.ConversationSearchRequest) tea.Cmd {
	cb, ok := m.backend.(ConversationsBackend)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		hits, err := cb.SearchConversations(context.Background(), req)
		if err != nil {
			return convSearchResultMsg{seq: seq, query: req.Query, err: err.Error()}
		}
		return convSearchResultMsg{seq: seq, query: req.Query, hits: hits}
	}
}

func (m *Model) scheduleConvSearch() tea.Cmd {
	m.convSearchSeq++
	seq := m.convSearchSeq
	m.convSearchHits = nil
	m.convSearchHitOrder = nil
	m.convLastSearchQuery = ""
	_, tokens := model.ParseConversationQuery(m.query)
	if len(tokens) == 0 || m.effectiveConvScope() == "titles" {
		m.convSearchInFlight = false
		return nil
	}
	return convDebounceCmd(seq)
}

func (m Model) effectiveConvScope() string {
	switch m.convScope {
	case "titles", "user":
		return m.convScope
	default:
		return "all"
	}
}

func (m Model) currentWorkspaceCWD() string {
	cur := m.currentSpace()
	if cur == nil {
		return ""
	}
	if cwd := strings.TrimSpace(cur.CWD); cwd != "" {
		return filepath.Clean(cwd)
	}
	for _, p := range cur.Panes {
		if p.IsActive && strings.TrimSpace(p.CWD) != "" {
			return filepath.Clean(strings.TrimSpace(p.CWD))
		}
	}
	for _, p := range cur.Panes {
		if strings.TrimSpace(p.CWD) != "" {
			return filepath.Clean(strings.TrimSpace(p.CWD))
		}
	}
	return ""
}

func (m Model) liveConversationPanes() map[string]convLivePane {
	live := make(map[string]convLivePane)
	for si, sp := range m.spaces {
		for pi, p := range sp.Panes {
			if p.IsDead || !p.IsAgent() {
				continue
			}
			var sids []string
			if s := strings.TrimSpace(p.ResumeSession); s != "" {
				sids = append(sids, s)
			}
			if p.Hook != nil {
				if s := strings.TrimSpace(p.Hook.SessionID); s != "" {
					sids = append(sids, s)
				}
			}
			if len(sids) == 0 {
				continue
			}
			var agents []string
			if a := model.CanonicalConversationAgent(p.App); a != "" {
				agents = append(agents, a)
			}
			if p.Hook != nil {
				if a := model.CanonicalConversationAgent(p.Hook.Agent); a != "" {
					agents = append(agents, a)
				}
				if a := model.CanonicalConversationAgent(p.Hook.App); a != "" {
					agents = append(agents, a)
				}
			}
			entry := convLivePane{
				SpaceIdx: si,
				PaneIdx:  pi,
				Space:    sp.Name,
				WindowID: p.WindowID,
				PaneID:   p.ID,
				Status:   p.Status,
			}
			for _, a := range agents {
				for _, sid := range sids {
					key := convKey(a, sid)
					if existing, ok := live[key]; !ok || entry.Status.Priority() < existing.Status.Priority() {
						live[key] = entry
					}
				}
			}
		}
	}
	return live
}

func lookupLivePane(live map[string]convLivePane, c model.ConversationEntry) *convLivePane {
	if len(live) == 0 {
		return nil
	}
	agent := model.CanonicalConversationAgent(c.Agent)
	if agent == "" {
		agent = model.CanonicalConversationAgent(c.App)
	}
	for _, sid := range []string{strings.TrimSpace(c.ID), c.ResumeSessionID()} {
		if sid == "" {
			continue
		}
		if lp, ok := live[convKey(agent, sid)]; ok {
			cp := lp
			return &cp
		}
	}
	return nil
}

// conversationHarnessPills returns "" ("all") followed by present canonical
// agent IDs sorted by count descending, then recency descending, then name.
func (m Model) conversationHarnessPills() []string {
	type stats struct {
		count  int
		latest int64
	}
	byAgent := make(map[string]*stats)
	for _, c := range m.conversations {
		if !m.convIncludeArchived && (c.Archived || c.IsEphemeralCWD()) {
			continue
		}
		a := model.CanonicalConversationAgent(c.Agent)
		if a == "" {
			a = model.CanonicalConversationAgent(c.App)
		}
		if a == "" {
			continue
		}
		st, ok := byAgent[a]
		if !ok {
			st = &stats{}
			byAgent[a] = st
		}
		st.count++
		if ts := c.EffectiveTimeMs(); ts > st.latest {
			st.latest = ts
		}
	}
	agents := make([]string, 0, len(byAgent))
	for a := range byAgent {
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool {
		si, sj := byAgent[agents[i]], byAgent[agents[j]]
		if si.count != sj.count {
			return si.count > sj.count
		}
		if si.latest != sj.latest {
			return si.latest > sj.latest
		}
		return agents[i] < agents[j]
	})
	pills := make([]string, 0, len(agents)+1)
	pills = append(pills, "")
	pills = append(pills, agents...)
	return pills
}

func cwdMatchesWorkspace(convCWD, wsCWD string) bool {
	if wsCWD == "" {
		return true
	}
	cc := strings.TrimSpace(convCWD)
	if cc == "" {
		return false
	}
	cc = filepath.Clean(cc)
	ws := filepath.Clean(wsCWD)
	return cc == ws || strings.HasPrefix(cc, ws+"/") || strings.HasPrefix(ws, cc+"/")
}

func (m Model) conversationItems() []convItem {
	inlineAgent, tokens := model.ParseConversationQuery(m.query)
	activeAgent := m.convHarnessFilter
	if inlineAgent != "" {
		activeAgent = inlineAgent
	}
	wsCWD := ""
	if m.convWorkspaceOnly {
		wsCWD = m.currentWorkspaceCWD()
	}
	scope := m.effectiveConvScope()
	nowMs := m.opts.NowOrDefault().UnixMilli()
	liveMap := m.liveConversationPanes()

	passesFilters := func(c model.ConversationEntry) bool {
		if !m.convIncludeArchived && (c.Archived || c.IsEphemeralCWD()) {
			return false
		}
		if activeAgent != "" {
			ca := model.CanonicalConversationAgent(c.Agent)
			capp := model.CanonicalConversationAgent(c.App)
			if ca != activeAgent && capp != activeAgent {
				return false
			}
		}
		if m.convWorkspaceOnly && wsCWD != "" && !cwdMatchesWorkspace(c.CWD, wsCWD) {
			return false
		}
		return true
	}

	if len(tokens) == 0 {
		items := make([]convItem, 0, len(m.conversations))
		for _, c := range m.conversations {
			if !passesFilters(c) {
				continue
			}
			items = append(items, convItem{
				Conv: c,
				Live: lookupLivePane(liveMap, c),
			})
		}
		sort.SliceStable(items, func(i, j int) bool {
			ti, tj := items[i].Conv.EffectiveTimeMs(), items[j].Conv.EffectiveTimeMs()
			if ti != tj {
				return ti > tj
			}
			li, lj := items[i].Live != nil, items[j].Live != nil
			if li != lj {
				return li
			}
			return items[i].Conv.ID < items[j].Conv.ID
		})
		return items
	}

	items := make([]convItem, 0, len(m.conversations))
	seen := make(map[string]int, len(m.conversations))

	// Local instant fzf-style fuzzy matching.
	for _, c := range m.conversations {
		if !passesFilters(c) {
			continue
		}
		score, hl, ok := model.FuzzyScoreConversation(c, tokens, nowMs)
		if !ok {
			continue
		}
		key := convKey(c.Agent, c.ID)
		var hitPtr *model.ConversationSearchHit
		if scope != "titles" && m.convSearchHits != nil {
			if h, exists := m.convSearchHits[key]; exists {
				hc := h
				hitPtr = &hc
			}
		}
		seen[key] = len(items)
		items = append(items, convItem{
			Conv:            c,
			Live:            lookupLivePane(liveMap, c),
			FuzzyScore:      score,
			TitleHighlights: hl,
			Hit:             hitPtr,
		})
	}

	// Merge FTS5 full-message search hits seamlessly (body-only matches appear
	// even when title/metadata didn't fuzzy-match).
	if scope != "titles" {
		for i := range m.convSearchHitOrder {
			h := m.convSearchHitOrder[i]
			if !passesFilters(h.Conversation) {
				continue
			}
			key := convKey(h.Conversation.Agent, h.Conversation.ID)
			if idx, exists := seen[key]; exists {
				if items[idx].Hit == nil {
					hc := h
					items[idx].Hit = &hc
				}
				continue
			}
			hc := h
			seen[key] = len(items)
			items = append(items, convItem{
				Conv: h.Conversation,
				Live: lookupLivePane(liveMap, h.Conversation),
				Hit:  &hc,
			})
		}
	}

	rankScore := func(it convItem) float64 {
		s := float64(it.FuzzyScore)
		if it.Hit != nil {
			boost := 120.0 + it.Hit.Score*10.0
			if boost > 600.0 {
				boost = 600.0
			}
			if boost < 60.0 {
				boost = 60.0
			}
			s += boost
		}
		return s
	}

	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := rankScore(items[i]), rankScore(items[j])
		if ri != rj {
			return ri > rj
		}
		ti, tj := items[i].Conv.EffectiveTimeMs(), items[j].Conv.EffectiveTimeMs()
		if ti != tj {
			return ti > tj
		}
		return items[i].Conv.ID < items[j].Conv.ID
	})
	return items
}

func (m Model) selectedConvKey() string {
	items := m.conversationItems()
	if m.selected < 0 || m.selected >= len(items) {
		return ""
	}
	c := items[m.selected].Conv
	return convKey(c.Agent, c.ID)
}

func (m *Model) restoreConvSelection(prevKey string) {
	items := m.conversationItems()
	if len(items) == 0 {
		m.selected = 0
		return
	}
	if prevKey != "" {
		for i, it := range items {
			if convKey(it.Conv.Agent, it.Conv.ID) == prevKey {
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

func (m *Model) moveConvSelection(delta int) {
	items := m.conversationItems()
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

func (m *Model) activateConversation(idx int, forceNewTab bool) {
	items := m.conversationItems()
	if idx < 0 || idx >= len(items) {
		return
	}
	item := items[idx]
	ctx := context.Background()
	if !forceNewTab && item.Live != nil {
		if err := m.backend.FocusPane(ctx, item.Live.Space, item.Live.WindowID, item.Live.PaneID); err != nil {
			m.toast = err.Error()
			return
		}
		m.quitting = true
		return
	}
	cb, ok := m.backend.(ConversationsBackend)
	if !ok {
		m.toast = "Cannot open conversation: backend unsupported."
		return
	}
	if err := cb.OpenConversation(ctx, item.Conv, forceNewTab); err != nil {
		m.toast = err.Error()
		return
	}
	m.quitting = true
}

func (m Model) hasActiveConvFilter() bool {
	return m.query != "" ||
		m.convHarnessFilter != "" ||
		m.convWorkspaceOnly ||
		m.effectiveConvScope() != "all" ||
		m.convIncludeArchived
}

// ---------------------------------------------------------------------------
// update handlers
// ---------------------------------------------------------------------------

func (m Model) handleConversationsMsg(msg conversationsMsg) (tea.Model, tea.Cmd) {
	m.convLoading = false
	if msg.refresh {
		m.convRefreshing = false
	}
	if msg.err != "" {
		if msg.refresh || !m.convRefreshing {
			m.toast = msg.err
		}
		return m, nil
	}
	prevKey := m.selectedConvKey()
	// Avoid clobbering fresh --refresh results with a late --cached-only reply.
	if msg.refresh || len(m.conversations) == 0 {
		m.conversations = msg.snap.Conversations
		m.convSyncedMs = msg.snap.SyncedMs
		m.convIndexedMessages = msg.snap.IndexedMessages
		m.convPendingSources = msg.snap.PendingSources
	}
	m.toast = ""
	m.restoreConvSelection(prevKey)
	return m, nil
}

func (m Model) handleConvSearchDebounce(msg convSearchDebounceMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.convSearchSeq {
		return m, nil
	}
	inlineAgent, tokens := model.ParseConversationQuery(m.query)
	ftsQuery := strings.Join(tokens, " ")
	scope := m.effectiveConvScope()
	if ftsQuery == "" || scope == "titles" {
		m.convSearchInFlight = false
		return m, nil
	}
	agent := m.convHarnessFilter
	if inlineAgent != "" {
		agent = inlineAgent
	}
	cwd := ""
	if m.convWorkspaceOnly {
		cwd = m.currentWorkspaceCWD()
	}
	req := model.ConversationSearchRequest{
		Query:           ftsQuery,
		Agent:           agent,
		CWD:             cwd,
		Scope:           scope,
		IncludeArchived: m.convIncludeArchived,
		Limit:           50,
	}
	cmd := m.searchConversationsCmd(msg.seq, req)
	if cmd != nil {
		m.convSearchInFlight = true
	}
	return m, cmd
}

func (m Model) handleConvSearchResult(msg convSearchResultMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.convSearchSeq {
		// Discard stale search result.
		return m, nil
	}
	m.convSearchInFlight = false
	if msg.err != "" {
		return m, nil
	}
	prevKey := m.selectedConvKey()
	hitMap := make(map[string]model.ConversationSearchHit, len(msg.hits))
	for _, h := range msg.hits {
		key := convKey(h.Conversation.Agent, h.Conversation.ID)
		if _, exists := hitMap[key]; !exists {
			hitMap[key] = h
		}
	}
	m.convSearchHits = hitMap
	m.convSearchHitOrder = msg.hits
	m.convLastSearchQuery = msg.query
	m.restoreConvSelection(prevKey)
	return m, nil
}

// ---------------------------------------------------------------------------
// key + mouse handling (search-first)
// ---------------------------------------------------------------------------

func (m Model) handleConversations(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Alt-c toggles current workspace/CWD scope vs all workspaces.
	if (msg.Alt && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && (msg.Runes[0] == 'c' || msg.Runes[0] == 'C')) ||
		msg.String() == "alt+c" {
		if !m.convWorkspaceOnly && m.currentWorkspaceCWD() == "" {
			m.toast = "No workspace folder for current space."
			return m, nil
		}
		m.toast = ""
		m.convWorkspaceOnly = !m.convWorkspaceOnly
		m.selected = 0
		m.convSearchHits = nil
		m.convSearchHitOrder = nil
		m.clampSelection()
		return m, m.scheduleConvSearch()
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if m.hasActiveConvFilter() {
			wasArchived := m.convIncludeArchived
			m.query = ""
			m.cursor = 0
			m.convHarnessFilter = ""
			m.convWorkspaceOnly = false
			m.convScope = "all"
			m.convIncludeArchived = false
			m.convSearchSeq++
			m.convSearchInFlight = false
			m.convSearchHits = nil
			m.convSearchHitOrder = nil
			m.convLastSearchQuery = ""
			m.toast = ""
			m.selected = 0
			m.clampSelection()
			if wasArchived {
				return m, m.loadConversationsCmd(false, false)
			}
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		m.activateConversation(m.selected, false)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyCtrlO:
		m.activateConversation(m.selected, true)
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveConvSelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveConvSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveConvSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveConvSelection(10)
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		pills := m.conversationHarnessPills()
		if len(pills) > 1 {
			idx := 0
			for i, p := range pills {
				if p == m.convHarnessFilter {
					idx = i
					break
				}
			}
			if msg.Type == tea.KeyShiftTab {
				idx = (idx + len(pills) - 1) % len(pills)
			} else {
				idx = (idx + 1) % len(pills)
			}
			m.convHarnessFilter = pills[idx]
			m.selected = 0
			m.convSearchHits = nil
			m.convSearchHitOrder = nil
			m.clampSelection()
			return m, m.scheduleConvSearch()
		}
		return m, nil
	case tea.KeyCtrlS:
		switch m.effectiveConvScope() {
		case "all":
			m.convScope = "titles"
		case "titles":
			m.convScope = "user"
		default:
			m.convScope = "all"
		}
		m.selected = 0
		m.convSearchHits = nil
		m.convSearchHitOrder = nil
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeyCtrlA:
		m.convIncludeArchived = !m.convIncludeArchived
		m.selected = 0
		m.convSearchHits = nil
		m.convSearchHitOrder = nil
		m.clampSelection()
		var cmds []tea.Cmd
		if m.convIncludeArchived {
			m.convRefreshing = true
			cmds = append(cmds, m.loadConversationsCmd(false, true), m.loadConversationsCmd(true, true))
		}
		if sc := m.scheduleConvSearch(); sc != nil {
			cmds = append(cmds, sc)
		}
		return m, tea.Batch(cmds...)
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
	case tea.KeyHome:
		m.cursor = 0
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE:
		m.cursor = len([]rune(m.query))
		return m, nil
	case tea.KeyBackspace:
		m.deleteRuneBefore()
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeyDelete:
		m.deleteRuneAt()
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeyCtrlU:
		m.query = ""
		m.cursor = 0
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeyCtrlW:
		m.deleteWord()
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeySpace:
		m.insertRune(' ')
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	case tea.KeyRunes:
		if msg.Alt || len(msg.Runes) == 0 {
			return m, nil
		}
		for _, r := range msg.Runes {
			m.insertRune(r)
		}
		m.selected = 0
		m.clampSelection()
		return m, m.scheduleConvSearch()
	}
	return m, nil
}

func (m Model) handleConvLeftClick(x, y int) (tea.Model, tea.Cmd) {
	_ = x
	items := m.conversationItems()
	if len(items) == 0 {
		return m, nil
	}
	layout := m.convLayout(m.width, m.height)
	if y < layout.topY || y >= layout.topY+layout.bodyHeight {
		return m, nil
	}
	entries := buildConvListEntries(items)
	selEntry := convSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), layout.bodyHeight)
	pos := start + (y - layout.topY)
	if pos < 0 || pos >= len(entries) {
		return m, nil
	}
	idx := entries[pos].itemIdx
	m.selected = idx
	m.activateConversation(idx, false)
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// layout + rendering
// ---------------------------------------------------------------------------

func (m Model) convLayout(width, height int) convLayout {
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
	return convLayout{
		topY:         topY,
		bodyHeight:   bodyHeight,
		showDividers: showDividers,
		detailLines:  detailLines,
	}
}

func buildConvListEntries(items []convItem) []convListEntry {
	if len(items) == 0 {
		return nil
	}
	entries := make([]convListEntry, 0, len(items)*2)
	for i, it := range items {
		entries = append(entries, convListEntry{itemIdx: i, subLine: 0})
		if it.Hit != nil && strings.TrimSpace(it.Hit.Snippet) != "" {
			entries = append(entries, convListEntry{itemIdx: i, subLine: 1})
		}
	}
	return entries
}

func convSelectedEntryIdx(entries []convListEntry, selected int) int {
	for i, e := range entries {
		if e.itemIdx == selected && e.subLine == 0 {
			return i
		}
	}
	return 0
}

func (m Model) renderConversations(width, height int) string {
	layout := m.convLayout(width, height)
	items := m.conversationItems()

	var b strings.Builder
	b.WriteString(m.convHeaderLine(width))
	b.WriteString("\n")
	b.WriteString(m.convSearchLine(items, width))
	b.WriteString("\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width))
		b.WriteString("\n")
	}

	lines := m.renderConvList(items, width, layout.bodyHeight)
	for i := 0; i < layout.bodyHeight; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width))
			b.WriteString("\n")
		}
		for _, dl := range m.renderConvDetailLines(items, width, layout.detailLines) {
			b.WriteString(dl)
			b.WriteString("\n")
		}
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width))
		b.WriteString("\n")
	}
	b.WriteString(m.convHintsLine(width))
	return b.String()
}

func formatCompactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (m Model) convHeaderLine(width int) string {
	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	dot := st(ColorSurface1, false).Render(" · ")

	visibleTotal := 0
	for _, c := range m.conversations {
		if !m.convIncludeArchived && (c.Archived || c.IsEphemeralCWD()) {
			continue
		}
		visibleTotal++
	}
	pills := m.conversationHarnessPills()
	harnessCount := len(pills) - 1
	if harnessCount < 0 {
		harnessCount = 0
	}

	var segs []string
	if m.convLoading && visibleTotal == 0 {
		segs = append(segs, st(ColorYellow, false).Render("loading…"))
	} else {
		noun := "chats"
		if visibleTotal == 1 {
			noun = "chat"
		}
		segs = append(segs, st(ColorSubtext0, false).Render(fmt.Sprintf("%d %s", visibleTotal, noun)))
		if harnessCount > 0 {
			hNoun := "harnesses"
			if harnessCount == 1 {
				hNoun = "harness"
			}
			segs = append(segs, st(ColorOverlay0, false).Render(fmt.Sprintf("%d %s", harnessCount, hNoun)))
		}
		switch {
		case m.convRefreshing:
			segs = append(segs, st(ColorYellow, false).Render("syncing…"))
		case m.convPendingSources > 0:
			segs = append(segs, st(ColorYellow, false).Render(fmt.Sprintf("indexing (%d pending)", m.convPendingSources)))
		case m.convIndexedMessages > 0:
			segs = append(segs, st(ColorGreen, false).Render(formatCompactCount(m.convIndexedMessages)+" msgs indexed"))
		}
	}

	var lb strings.Builder
	lb.WriteString(st(ColorText, true).Render(" Conversations"))
	if len(segs) > 0 {
		lb.WriteString(st(bg, false).Render("   "))
		lb.WriteString(strings.Join(segs, dot))
	}
	leftStyled := lb.String()

	inlineAgent, _ := model.ParseConversationQuery(m.query)
	activePill := m.convHarnessFilter
	if inlineAgent != "" {
		activePill = inlineAgent
	}
	rightStyled := renderConvHarnessPills(pills, activePill, bg) + st(bg, false).Render(" ")

	leftW := ansi.StringWidth(leftStyled)
	rightW := ansi.StringWidth(rightStyled)
	var line string
	if leftW+rightW+2 <= width {
		line = leftStyled + st(bg, false).Render(strings.Repeat(" ", width-leftW-rightW)) + rightStyled
	} else if width > rightW+12 {
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

func renderConvHarnessPills(pills []string, active, bg string) string {
	activeSt := lipgloss.NewStyle().
		Background(lipgloss.Color(ColorSurface0)).
		Foreground(lipgloss.Color(ColorLavender)).
		Bold(true)
	inactiveSt := lipgloss.NewStyle().
		Background(lipgloss.Color(bg)).
		Foreground(lipgloss.Color(ColorOverlay0))
	sep := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Render(" ")

	// Show "all" plus top 5 harnesses; if the active filter is beyond top 5, include it too.
	const maxPills = 6
	shown := pills
	if len(shown) > maxPills {
		shown = append([]string(nil), pills[:maxPills]...)
		found := false
		for _, p := range shown {
			if p == active {
				found = true
				break
			}
		}
		if !found && active != "" {
			shown[maxPills-1] = active
		}
	}

	rendered := make([]string, 0, len(shown))
	for _, p := range shown {
		label := p
		if label == "" {
			label = "all"
		}
		if p == active {
			rendered = append(rendered, activeSt.Render("["+label+"]"))
		} else {
			rendered = append(rendered, inactiveSt.Render(label))
		}
	}
	return strings.Join(rendered, sep)
}

func (m Model) convSearchLine(items []convItem, width int) string {
	slashSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	querySt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	caretSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	phSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))

	scope := m.effectiveConvScope()
	var rightParts []string
	if m.convWorkspaceOnly {
		wsName := filepath.Base(m.currentWorkspaceCWD())
		if wsName == "" || wsName == "." || wsName == "/" {
			wsName = "workspace"
		}
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(ColorTeal)).Render("cwd:"+truncate(wsName, 16)))
	}
	if m.convIncludeArchived {
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(ColorMauve)).Render("+archived"))
	}
	scopeColor := ColorOverlay0
	if scope != "all" {
		scopeColor = ColorLavender
	}
	rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(scopeColor)).Render("scope:"+scope))

	if m.convSearchInFlight {
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render("searching…"))
	} else {
		noun := "matches"
		if len(items) == 1 {
			noun = "match"
		}
		if strings.TrimSpace(m.query) != "" || m.convHarnessFilter != "" || m.convWorkspaceOnly {
			rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0)).Render(fmt.Sprintf("%d %s", len(items), noun)))
		}
	}

	sep := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1)).Render(" · ")
	rightStyled := ""
	rightW := 0
	if len(rightParts) > 0 {
		rightStyled = strings.Join(rightParts, sep) + " "
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
			lb.WriteString(phSt.Render(" " + truncate("Search titles, messages, paths… (@agent)", rem-1)))
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

func (m Model) renderConvList(items []convItem, width, height int) []string {
	lines := make([]string, height)
	if len(items) == 0 {
		msg := "No conversations match this query."
		if m.convLoading {
			msg = "Loading conversations…"
		} else if len(m.conversations) == 0 {
			msg = "No conversations indexed yet. Press Ctrl-r to sync."
		}
		lines[0] = fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width)
		for i := 1; i < height; i++ {
			lines[i] = fitCellWidth("", width)
		}
		return lines
	}

	entries := buildConvListEntries(items)
	selEntry := convSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), height)

	for i := 0; i < height; i++ {
		idx := start + i
		if idx >= len(entries) {
			lines[i] = fitCellWidth("", width)
			continue
		}
		e := entries[idx]
		selected := e.itemIdx == m.selected
		it := items[e.itemIdx]
		if e.subLine == 1 {
			lines[i] = m.renderConvSnippetRow(it, selected, width)
		} else {
			lines[i] = m.renderConvPrimaryRow(it, selected, width)
		}
	}
	return lines
}

func formatConvFolder(cwd, homeDir string, width int) string {
	short := shortenPath(strings.TrimSpace(cwd), homeDir)
	if short == "" {
		return ""
	}
	maxW := 28
	switch {
	case width >= 120:
		maxW = 32
	case width >= 96:
		maxW = 24
	case width >= 76:
		maxW = 18
	default:
		maxW = 14
	}
	if ansi.StringWidth(short) <= maxW {
		return short
	}
	// Keep the tail segments of deep paths (e.g. "…/dev/model-router") so the
	// actual folder name stays legible.
	parts := strings.Split(short, "/")
	if len(parts) >= 2 {
		tail := "…/" + parts[len(parts)-2] + "/" + parts[len(parts)-1]
		if ansi.StringWidth(tail) <= maxW {
			return tail
		}
		last := parts[len(parts)-1]
		if last != "" {
			return truncate(last, maxW)
		}
	}
	return truncate(short, maxW)
}

func (m Model) renderConvPrimaryRow(it convItem, selected bool, width int) string {
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

	// Status / live icon.
	icon := "·"
	iconColor := ColorOverlay0
	iconBold := false
	if it.Live != nil {
		icon = it.Live.Status.Icon()
		iconColor = StatusColor(it.Live.Status)
		if it.Live.Status == model.StatusIdle {
			iconColor = ColorTeal
		}
		iconBold = true
	} else {
		switch strings.ToLower(strings.TrimSpace(it.Conv.LastOutcome)) {
		case "done":
			icon = "✓"
			iconColor = ColorGreen
		case "failed", "error":
			icon = "✗"
			iconColor = ColorRed
		}
	}

	// Secondary metadata on the right: <folder> · <space> · <harness>   <age>
	// Profile information is intentionally omitted from display (retained on Conv for resume).
	now := m.opts.NowOrDefault()
	ageStr := formatOutcomeTime(it.Conv.EffectiveTimeMs(), now)
	if ageStr == "" {
		ageStr = "   "
	} else {
		ageStr = fmt.Sprintf("%3s", ageStr)
	}
	ageStyled := st(ColorOverlay0, false).Render(ageStr)

	baseApp := it.Conv.DisplayApp()
	if baseApp == "" {
		baseApp = "agent"
	}
	harnessBadge := st(ColorOverlay0, false).Render(truncate(baseApp, 14))

	var spaceBadge string
	if it.Live != nil {
		if it.Live.Status == model.StatusNeedsYou {
			spaceBadge = st(ColorLavender, true).Render("● " + truncate(it.Live.Space, 14))
		} else {
			spaceBadge = st(ColorTeal, selected).Render("live·" + truncate(it.Live.Space, 12))
		}
	} else if sp := strings.TrimSpace(it.Conv.Space); sp != "" {
		spaceBadge = st(ColorOverlay0, false).Render(truncate(sp, 14))
	}

	var folderBadge string
	if folder := formatConvFolder(it.Conv.CWD, m.opts.HomeDir, width); folder != "" {
		folderBadge = st(ColorSubtext0, false).Render(folder)
	}

	// Reserve generous room for the primary conversation title on the left.
	const indentAndIconW = 5 // "  ●  "
	minTitle := 28
	if width < 90 {
		minTitle = 20
	}
	if width < 68 {
		minTitle = 14
	}
	metaSep := st(ColorSurface1, false).Render(" · ")
	metaSepW := ansi.StringWidth(metaSep)
	availMeta := innerW - (indentAndIconW + minTitle + 2) - ansi.StringWidth(ageStyled) - 2
	if availMeta < 0 {
		availMeta = 0
	}

	// Select secondary segments within budget (priority: harness, space, folder),
	// then render in reading order: folder · space · harness.
	usedMeta := 0
	canAdd := func(seg string) bool {
		if seg == "" {
			return false
		}
		need := ansi.StringWidth(seg)
		if usedMeta > 0 {
			need += metaSepW
		}
		if usedMeta+need <= availMeta {
			usedMeta += need
			return true
		}
		return false
	}
	showHarness := canAdd(harnessBadge)
	showSpace := canAdd(spaceBadge)
	showFolder := canAdd(folderBadge)

	var metaParts []string
	if showFolder {
		metaParts = append(metaParts, folderBadge)
	}
	if showSpace {
		metaParts = append(metaParts, spaceBadge)
	}
	if showHarness {
		metaParts = append(metaParts, harnessBadge)
	}

	var rightStyled string
	if len(metaParts) > 0 {
		rightStyled = strings.Join(metaParts, metaSep) + st(bg, false).Render("  ") + ageStyled
	} else {
		rightStyled = ageStyled
	}
	rightW := ansi.StringWidth(rightStyled)

	maxLeft := innerW - rightW - 2
	if maxLeft < 10 {
		maxLeft = 10
	}

	// Primary text: conversation title (never substitute the folder/username as a fake title).
	title := strings.TrimSpace(it.Conv.Title)
	hasTitle := title != ""
	if !hasTitle {
		if prev := strings.TrimSpace(it.Conv.Preview); prev != "" {
			title = singleLineText(prev)
		} else {
			title = "(untitled)"
		}
	}
	title = singleLineText(title)

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("  "))
	lb.WriteString(st(iconColor, iconBold).Render(icon))
	lb.WriteString(st(bg, false).Render("  "))

	titleBudget := maxLeft - indentAndIconW
	if title != "" && titleBudget > 3 {
		titleFg := ColorText
		if !hasTitle {
			titleFg = ColorSubtext0
		}
		if hasTitle && len(it.TitleHighlights) > 0 {
			spans := positionsToSpans(it.TitleHighlights)
			lb.WriteString(renderHighlightedText(title, spans, titleBudget, st(titleFg, selected), st(ColorLavender, true)))
		} else {
			lb.WriteString(st(titleFg, selected).Render(ansi.Truncate(title, titleBudget, "…")))
		}
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

func (m Model) renderConvSnippetRow(it convItem, selected bool, width int) string {
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

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("     "))
	lb.WriteString(st(ColorSurface1, false).Render("↳ "))
	used := 7

	role := strings.TrimSpace(it.Hit.MatchRole)
	if role != "" {
		roleColor := ColorSubtext0
		switch strings.ToLower(role) {
		case "user":
			roleColor = ColorBlue
		case "assistant":
			roleColor = ColorMauve
		}
		roleTag := role + ": "
		lb.WriteString(st(roleColor, false).Render(roleTag))
		used += ansi.StringWidth(roleTag)
	}

	rem := innerW - used
	if rem > 4 {
		snippet := singleLinePreserveLength(it.Hit.Snippet)
		lb.WriteString(renderHighlightedText(
			snippet,
			it.Hit.SnippetHighlights,
			rem,
			st(ColorSubtext0, false),
			st(ColorLavender, true),
		))
	}

	var out strings.Builder
	if selected {
		out.WriteString(st(ColorLavender, true).Render(prefix))
	} else {
		out.WriteString(st(bg, false).Render(prefix))
	}
	out.WriteString(lb.String())

	if selected {
		return fitCellWidthWithBG(out.String(), width, bg)
	}
	return fitCellWidth(out.String(), width)
}

func (m Model) renderConvDetailLines(items []convItem, width, maxLines int) []string {
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
		empty := st(ColorOverlay0, false).Render("Type to fuzzy-search titles and message history across coding harnesses.")
		if maxLines <= 1 {
			return []string{padLine(empty)}
		}
		return []string{padLine(empty), padLine("")}
	}

	it := items[m.selected]
	c := it.Conv

	// Line 1: Title · Folder · Branch · Space/Live · Harness (no profile) · Model · Messages
	var l1 []string
	if title := singleLineText(strings.TrimSpace(c.Title)); title != "" {
		l1 = append(l1, st(ColorText, true).Render(truncate(title, 48)))
	}
	if cwd := shortenPath(c.CWD, m.opts.HomeDir); cwd != "" {
		l1 = append(l1, st(ColorSubtext0, false).Render(cwd))
	}
	if br := strings.TrimSpace(c.Branch); br != "" {
		l1 = append(l1, st(ColorTeal, false).Render("⎇ "+br))
	}
	if it.Live != nil {
		l1 = append(l1, st(StatusColor(it.Live.Status), it.Live.Status.IsAttention()).Render(
			it.Live.Status.Icon()+" live in "+it.Live.Space,
		))
	} else if sp := strings.TrimSpace(c.Space); sp != "" {
		l1 = append(l1, st(ColorLavender, false).Render("space "+sp))
	}
	baseApp := c.DisplayApp()
	if baseApp == "" {
		baseApp = "agent"
	}
	l1 = append(l1, st(ColorOverlay0, false).Render(baseApp))
	if mdl := strings.TrimSpace(c.Model); mdl != "" {
		l1 = append(l1, st(ColorOverlay0, false).Render(mdl))
	}
	if c.MessageCount > 0 {
		l1 = append(l1, st(ColorOverlay0, false).Render(fmt.Sprintf("%d msgs", c.MessageCount)))
	}

	// Line 2: Preview / snippet · Timestamp · Action hint
	var l2 []string
	if it.Hit != nil && strings.TrimSpace(it.Hit.Snippet) != "" {
		l2 = append(l2, st(ColorText, false).Render(singleLineText(it.Hit.Snippet)))
	} else if prev := strings.TrimSpace(c.Preview); prev != "" {
		l2 = append(l2, st(ColorText, false).Render(singleLineText(prev)))
	}
	if ts := formatOutcomeDetailTime(c.EffectiveTimeMs(), m.opts.NowOrDefault()); ts != "" {
		l2 = append(l2, st(ColorOverlay0, false).Render(ts))
	}
	if it.Live != nil {
		l2 = append(l2, st(ColorLavender, false).Render("Enter focus live pane · Ctrl-o new tab"))
	} else {
		l2 = append(l2, st(ColorLavender, false).Render("Enter resume in new tab"))
	}

	if maxLines <= 1 {
		return []string{padLine(strings.Join(append(l1, l2...), sep))}
	}
	return []string{padLine(strings.Join(l1, sep)), padLine(strings.Join(l2, sep))}
}

func (m Model) convHintsLine(width int) string {
	if width < 76 {
		return renderHintsBar([][2]string{
			{"↑↓", "move"},
			{"Enter", "open"},
			{"Tab", "harness"},
			{"Ctrl-s", "scope"},
			{"Esc", "clear/close"},
		}, width)
	}
	return renderHintsBar([][2]string{
		{"↑↓", "move"},
		{"Enter", "open"},
		{"Ctrl-o", "new tab"},
		{"Tab", "harness"},
		{"Alt-c", "workspace"},
		{"Ctrl-s", "scope"},
		{"Ctrl-a", "archived"},
		{"Ctrl-r", "refresh"},
		{"Esc", "clear/close"},
	}, width)
}

// ---------------------------------------------------------------------------
// text + highlight helpers
// ---------------------------------------------------------------------------

func singleLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// singleLinePreserveLength replaces line breaks and tabs with single spaces
// without changing rune count so SnippetSpan rune offsets stay aligned.
func singleLinePreserveLength(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

func positionsToSpans(positions []int) []model.SnippetSpan {
	if len(positions) == 0 {
		return nil
	}
	spans := make([]model.SnippetSpan, 0, len(positions))
	start := positions[0]
	end := start + 1
	for _, p := range positions[1:] {
		if p == end {
			end = p + 1
		} else if p > end {
			spans = append(spans, model.SnippetSpan{Start: start, End: end})
			start = p
			end = p + 1
		}
	}
	spans = append(spans, model.SnippetSpan{Start: start, End: end})
	return spans
}

// renderHighlightedText renders text truncated to maxW display cells, applying
// hlSt to rune indices covered by spans and baseSt to all other runes.
func renderHighlightedText(text string, spans []model.SnippetSpan, maxW int, baseSt, hlSt lipgloss.Style) string {
	if maxW <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	hlMask := make([]bool, len(runes))
	for _, sp := range spans {
		s := sp.Start
		e := sp.End
		if s < 0 {
			s = 0
		}
		if e > len(runes) {
			e = len(runes)
		}
		for i := s; i < e; i++ {
			hlMask[i] = true
		}
	}

	// Determine how many runes fit within maxW cells (leaving 1 cell for "…" if truncated).
	totalW := ansi.StringWidth(text)
	truncated := totalW > maxW
	limitW := maxW
	if truncated && maxW > 1 {
		limitW = maxW - 1
	}

	var b strings.Builder
	usedW := 0
	var runBuf strings.Builder
	runHL := false
	hasRun := false
	flush := func() {
		if !hasRun || runBuf.Len() == 0 {
			return
		}
		if runHL {
			b.WriteString(hlSt.Render(runBuf.String()))
		} else {
			b.WriteString(baseSt.Render(runBuf.String()))
		}
		runBuf.Reset()
		hasRun = false
	}

	for i, r := range runes {
		rw := ansi.StringWidth(string(r))
		if usedW+rw > limitW {
			break
		}
		if hasRun && hlMask[i] != runHL {
			flush()
		}
		runHL = hlMask[i]
		hasRun = true
		runBuf.WriteRune(r)
		usedW += rw
	}
	flush()
	if truncated && maxW > 1 {
		b.WriteString(baseSt.Render("…"))
	}
	return b.String()
}

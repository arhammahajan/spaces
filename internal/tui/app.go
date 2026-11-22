package tui

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arhammahajan/spaces/internal/model"
)

// RowKind identifies a visible navigator row.
type RowKind int

const (
	RowSpace RowKind = iota
	RowPane
	RowExitedHeader
	RowExited
	RowCreate
)

// Row is one selectable line in the navigator tree.
type Row struct {
	Kind       RowKind
	SpaceIdx   int
	PaneIdx    int
	ExitedIdx  int
	CreateName string
}

// Internal messages.
type (
	tickMsg   time.Time
	spacesMsg struct {
		seq      uint64
		spaces   []model.Space
		exited   []model.ExitedSpace
		outcomes []model.OutcomeEntry
		err      string
	}
	hookDetailMsg struct {
		session string
		paneNum int
		detail  *model.HookDetail
		err     string
	}
	harnessesMsg struct {
		entries []model.HarnessEntry
		err     string
	}
	gitInfoMsg struct {
		info  map[string]*model.GitInfo
		force bool
	}
	prInfoMsg struct {
		info map[string]*model.GitInfo
	}
)

// Model is the Bubbletea model for the tmux-spaces popup.
type Model struct {
	backend Backend
	opts    Options

	mode   Mode
	width  int
	height int

	spaces        []model.Space
	exited        []model.ExitedSpace
	outcomes      []model.OutcomeEntry
	spacesSeq     uint64
	spacesLoading bool
	filter    model.AttentionFilter
	query     string
	cursor    int
	selected  int

	collapsed  map[string]bool
	expandedBg map[string]bool
	bgDetails  map[string]*model.HookDetail
	toast      string

	newName       string
	newNameCur    int
	newNameEdited bool
	newCWD        string
	newCWDCur     int
	newField      int // 0 = name, 1 = cwd, 2 = worktree mode, 3 = list/branch, 4 = base
	newError      string
	newIsTab      bool
	newTabSession string

	wtCtx           model.WorktreeContext
	wtLoading       bool
	wtUseNew        bool
	wtExistingIdx   int
	wtBranch        string
	wtBranchCur     int
	wtBaseIdx       int
	wtAutoSeed      int64
	wtAutoBranch    string
	wtAutoDirName   string
	wtAutoSource    string
	wtHarnessApp    string
	wtHarnessTitle  string
	wtHarnessPrompt string

	worktrees            []model.WorktreeEntry
	worktreesLoading     bool
	worktreesStandalone  bool
	worktreesReturnMode  Mode
	wtSelected           int
	wtQuery              string
	wtQueryCur           int
	wtSearching          bool
	wtRemoveTarget       model.WorktreeEntry
	wtRemoveSafety       WorktreeRemoveSafety
	wtRemoveForce        bool
	wtRemoveDeleteBranch bool
	renameOld            string
	renameText           string
	renameCur            int
	renameError          string
	renameWindow         bool
	renameWindowID       string
	renameStandalone     bool

	confirmSpace           string
	confirmWindows         int
	confirmPanes           int
	confirmExited          bool
	confirmWindow          bool
	confirmWindowID        string
	confirmWindowName      string
	confirmWindowBusy      bool
	confirmWindowLast      bool
	confirmWorktreePath    string
	confirmWorktreeName    string
	confirmWorktreeBranch  string
	confirmWorktreeManaged bool
	confirmRemoveWorktree  bool
	confirmWorktreeSafety  WorktreeRemoveSafety

	hookSession string
	hookPaneNum int
	hookDetail  *model.HookDetail
	hookLoading bool
	hookScroll  int

	harnesses     []model.HarnessEntry
	harnessCursor int
	agentFilter   string
	agentCur      int
	profileOf     *int
	agentsLoading bool

	lastClickRow int
	lastClickAt  time.Time

	previews       map[string]string
	gitInfo        map[string]*model.GitInfo
	gitFetching    bool
	prFetching     bool
	inboxSearching bool
	inboxTick      int
	returnMode     Mode

	conversations       []model.ConversationEntry
	convSyncedMs        int64
	convIndexedMessages int
	convPendingSources  int
	convLoading         bool
	convRefreshing      bool
	convHarnessFilter   string
	convWorkspaceOnly   bool
	convScope           string
	convIncludeArchived bool
	convSearchSeq       int
	convSearchInFlight  bool
	convSearchHits           map[string]model.ConversationSearchHit
	convSearchHitOrder       []model.ConversationSearchHit
	convLastSearchQuery      string
	convPickPending          *model.ConversationEntry
	convPickForceNewTab      bool
	convPickProfiles         []model.HarnessProfile
	convPickCursor           int
	convPickWaitingHarnesses bool

	sharedSnap         model.SharedSnapshot
	sharedLoading      bool
	sharedFilter       model.SharedKind
	sharedInspecting   bool
	sharedDetailScroll int

	tabsSearching         bool
	tabsSort              TabSortMode
	tabsPaneSel           map[string]int
	tabsTelemetryExpanded bool
	tabsInspectorScroll   int
	tabsInitSelected      bool
	tabsHookInFlight      string

	pendingTarget string
	quitting      bool
}

// New creates a Model. Exported for tests and the Run helper.
func New(backend Backend, opts Options) Model {
	m := Model{
		backend:        backend,
		opts:           opts,
		mode:           opts.InitialMode,
		spacesSeq:      1,
		filter:         model.FilterAll,
		query:          opts.InitialQuery,
		cursor:         len([]rune(opts.InitialQuery)),
		collapsed:      map[string]bool{},
		expandedBg:     map[string]bool{},
		bgDetails:      map[string]*model.HookDetail{},
		previews:       map[string]string{},
		gitInfo:        map[string]*model.GitInfo{},
		tabsPaneSel:    map[string]int{},
		convScope:      "all",
		convSearchHits: map[string]model.ConversationSearchHit{},
		lastClickRow:   -1,
		pendingTarget:  strings.TrimSpace(opts.Target),
	}
	if m.mode == ModeTabs {
		m.pendingTarget = ""
	}
	if opts.InitialQuery != "" && m.mode == ModeNavigate {
		m.mode = ModeSearch
	}
	if opts.OpenNew {
		if m.mode == ModeTabs {
			m.returnMode = ModeTabs
			m.newIsTab = true
		}
		m.mode = ModeNewSpace
	}
	if m.mode == ModeWorktrees {
		m.worktreesStandalone = true
		m.worktreesLoading = true
	}
	if opts.OpenRenameWindow || opts.OpenRenameSpace {
		m.mode = ModeRename
		m.renameStandalone = true
		m.renameWindow = opts.OpenRenameWindow
	}
	if m.mode == ModeAgents {
		m.agentsLoading = true
		m.agentFilter = opts.InitialQuery
		m.agentCur = len([]rune(m.agentFilter))
	}
	if m.mode == ModeConversations {
		m.convLoading = true
		m.convRefreshing = true
		m.agentsLoading = true
		if _, tokens := model.ParseConversationQuery(opts.InitialQuery); len(tokens) > 0 {
			m.convSearchSeq = 1
		}
	}
	if m.mode == ModeShared {
		m.sharedLoading = true
	}
	if m.mode == ModeNavigate || m.mode == ModeSearch || m.mode == ModeInbox || m.mode == ModeTabs || m.mode == ModeWorktrees {
		m.spacesLoading = true
	}
	return m
}

// Quitting reports whether the model requested exit.
func (m Model) Quitting() bool { return m.quitting }

// ModeOf reports the current mode (for tests/views).
func (m Model) ModeOf() Mode { return m.mode }

// Init loads initial data and starts the refresh tick.
func (m Model) Init() tea.Cmd {
	if m.mode == ModeAgents {
		// Load spaces once so launchAgent knows the current space and CWD,
		// without starting a polling tick (Agents view stays static).
		return tea.Batch(m.loadHarnessesCmd(), m.loadSpacesCmd())
	}
	if m.mode == ModeConversations {
		cmds := []tea.Cmd{
			m.loadConversationsCmd(false, m.convIncludeArchived),
			m.loadConversationsCmd(true, m.convIncludeArchived),
			m.loadSpacesCmd(),
			m.loadHarnessesCmd(),
		}
		if m.convSearchSeq > 0 {
			cmds = append(cmds, convDebounceCmd(m.convSearchSeq))
		}
		return tea.Batch(cmds...)
	}
	if m.mode == ModeShared {
		return m.loadSharedCmd()
	}
	if m.mode == ModeRename && m.renameStandalone {
		return m.loadSpacesCmd()
	}
	if m.mode == ModeWorktrees {
		return m.loadSpacesCmd()
	}
	return tea.Batch(m.tickCmd(), m.loadSpacesCmd())
}

func (m Model) tickCmd() tea.Cmd {
	interval := m.opts.RefreshIntervalOrDefault()
	return tea.Tick(interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) loadSpacesCmd() tea.Cmd {
	return m.loadSpacesCmdWithSeq(m.spacesSeq)
}

func (m *Model) nextLoadSpacesCmd() tea.Cmd {
	m.spacesSeq++
	return m.loadSpacesCmdWithSeq(m.spacesSeq)
}

func (m Model) loadSpacesCmdWithSeq(seq uint64) tea.Cmd {
	backend := m.backend
	wantExited := m.mode == ModeNavigate || m.mode == ModeSearch
	wantOutcomes := m.mode == ModeInbox
	return func() tea.Msg {
		ctx := context.Background()
		spaces, err := backend.LoadSpaces(ctx)
		if err != nil {
			return spacesMsg{seq: seq, err: err.Error()}
		}
		var exited []model.ExitedSpace
		if wantExited {
			if rb, ok := backend.(ResurrectBackend); ok {
				if list, eErr := rb.LoadExited(ctx); eErr == nil {
					exited = list
				}
			}
		}
		var outcomes []model.OutcomeEntry
		if wantOutcomes {
			if ob, ok := backend.(OutcomesBackend); ok {
				if list, oErr := ob.LoadOutcomes(ctx); oErr == nil {
					outcomes = list
				}
			}
		}
		return spacesMsg{seq: seq, spaces: spaces, exited: exited, outcomes: outcomes}
	}
}

func (m Model) loadHookDetailCmd(session string, paneNum int) tea.Cmd {
	return func() tea.Msg {
		d, err := m.backend.LoadHookDetail(context.Background(), session, paneNum)
		if err != nil {
			return hookDetailMsg{session: session, paneNum: paneNum, err: err.Error()}
		}
		return hookDetailMsg{session: session, paneNum: paneNum, detail: d}
	}
}

func (m Model) loadHarnessesCmd() tea.Cmd {
	return func() tea.Msg {
		entries, err := m.backend.LoadHarnesses(context.Background())
		if err != nil {
			return harnessesMsg{err: err.Error()}
		}
		return harnessesMsg{entries: entries}
	}
}

func (m Model) loadGitStatusCmd(cwds []string, force bool) tea.Cmd {
	gb, ok := m.backend.(GitBackend)
	if !ok || len(cwds) == 0 {
		return nil
	}
	return func() tea.Msg {
		info := gb.LoadGitStatus(context.Background(), cwds, force)
		return gitInfoMsg{info: info, force: force}
	}
}

func (m Model) loadPRStatusCmd(gitByCWD map[string]*model.GitInfo, force bool) tea.Cmd {
	gb, ok := m.backend.(GitBackend)
	if !ok || len(gitByCWD) == 0 {
		return nil
	}
	return func() tea.Msg {
		info := gb.LoadPRStatus(context.Background(), gitByCWD, force)
		return prInfoMsg{info: info}
	}
}

func (m *Model) collectCWDs() []string {
	seen := make(map[string]struct{})
	var cwds []string
	add := func(cwd string) {
		cwd = strings.TrimSpace(cwd)
		if cwd == "" {
			return
		}
		if _, ok := seen[cwd]; ok {
			return
		}
		seen[cwd] = struct{}{}
		cwds = append(cwds, cwd)
	}
	for _, sp := range m.spaces {
		add(sp.CWD)
		for _, p := range sp.Panes {
			add(p.CWD)
		}
	}
	for _, o := range m.outcomes {
		add(o.CWD)
	}
	return cwds
}

func (m *Model) applyGitInfo() {
	if len(m.gitInfo) == 0 {
		return
	}
	for si := range m.spaces {
		if sp := &m.spaces[si]; sp.CWD != "" {
			if g, ok := m.gitInfo[sp.CWD]; ok && g != nil {
				sp.Git = g
			}
		}
		for pi := range m.spaces[si].Panes {
			p := &m.spaces[si].Panes[pi]
			if p.CWD != "" {
				if g, ok := m.gitInfo[p.CWD]; ok && g != nil {
					p.Git = g
				}
			}
		}
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampSelection()
		return m, nil
	case tickMsg:
		if m.mode == ModeAgents || m.mode == ModeConversations || m.mode == ModeShared || m.mode == ModeWorktrees || m.mode == ModeConfirmRemoveWorktree {
			// Static in modal/secondary modes: no polling flicker.
			return m, nil
		}
		return m, tea.Batch(m.nextLoadSpacesCmd(), m.tickCmd())
	case wtInspectMsg:
		return m.handleWtInspectMsg(msg)
	case wtPromptDetailMsg:
		return m.handleWtPromptDetailMsg(msg)
	case wtListMsg:
		return m.handleWtListMsg(msg)
	case wtSafetyMsg:
		return m.handleWtSafetyMsg(msg)
	case sharedMsg:
		return m.handleSharedMsg(msg)
	case conversationsMsg:
		return m.handleConversationsMsg(msg)
	case convSearchDebounceMsg:
		return m.handleConvSearchDebounce(msg)
	case convSearchResultMsg:
		return m.handleConvSearchResult(msg)
	case spacesMsg:
		if msg.seq > 0 && msg.seq < m.spacesSeq {
			return m, nil
		}
		m.spacesLoading = false
		if msg.err != "" {
			if m.mode == ModeWorktrees && len(m.worktrees) == 0 {
				m.worktreesLoading = false
			}
			if m.mode != ModeConversations {
				m.toast = msg.err
			}
			return m, nil
		}
		m.spaces = msg.spaces
		m.exited = msg.exited
		m.outcomes = msg.outcomes
		m.applyGitInfo()
		if m.mode == ModeTabs || (m.mode == ModeNewSpace && m.newIsTab) {
			m.selectInitialActiveTab()
		}
		m.clampSelection()
		var cmds []tea.Cmd
		if m.mode == ModeNewSpace && !m.newIsTab && m.newName == "" && m.newCWD == "" {
			m.prefillNewSpace("")
			if cmd := m.initNewSpaceWorktrees(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.mode == ModeNewSpace && m.newIsTab && m.newTabSession == "" {
			if cmd := m.prefillNewTab(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.mode == ModeWorktrees && len(m.worktrees) == 0 {
			if cmd := m.loadAllWorktreesCmd(); cmd != nil {
				m.worktreesLoading = true
				cmds = append(cmds, cmd)
			} else {
				m.worktreesLoading = false
			}
		}
		if m.mode == ModeRename && m.renameStandalone && m.renameOld == "" {
			m.prefillStandaloneRename()
		}
		if m.pendingTarget != "" && m.mode != ModeAgents && m.mode != ModeInbox && m.mode != ModeConversations && m.mode != ModeTabs {
			target := m.pendingTarget
			m.pendingTarget = ""
			if m.spaceExists(target) {
				if err := m.backend.OpenSpace(context.Background(), target); err != nil {
					m.toast = err.Error()
					return m, nil
				}
				m.quitting = true
				return m, tea.Quit
			}
			m.query = target
			m.cursor = len([]rune(target))
			m.mode = ModeSearch
			m.selected = 0
			m.clampSelection()
		}
		if !m.gitFetching && m.mode != ModeAgents && m.mode != ModeRename && m.mode != ModeConversations && m.mode != ModeWorktrees {
			if cwds := m.collectCWDs(); len(cwds) > 0 {
				if cmd := m.loadGitStatusCmd(cwds, false); cmd != nil {
					m.gitFetching = true
					cmds = append(cmds, cmd)
				}
			}
		}
		if m.mode == ModeTabs {
			if cmd := m.syncTabsInspectorCmd(false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil
	case gitInfoMsg:
		m.gitFetching = false
		if m.gitInfo == nil {
			m.gitInfo = make(map[string]*model.GitInfo)
		}
		for cwd, g := range msg.info {
			m.gitInfo[cwd] = g
		}
		m.applyGitInfo()
		if !m.prFetching && len(msg.info) > 0 {
			if cmd := m.loadPRStatusCmd(msg.info, msg.force); cmd != nil {
				m.prFetching = true
				return m, cmd
			}
		}
		return m, nil
	case prInfoMsg:
		m.prFetching = false
		if m.gitInfo == nil {
			m.gitInfo = make(map[string]*model.GitInfo)
		}
		for cwd, g := range msg.info {
			m.gitInfo[cwd] = g
		}
		m.applyGitInfo()
		return m, nil
	case hookDetailMsg:
		if m.tabsHookInFlight == tabsHookKey(msg.session, msg.paneNum) {
			m.tabsHookInFlight = ""
		}
		if msg.detail != nil && msg.err == "" {
			for _, sp := range m.spaces {
				if sp.Name != msg.session {
					continue
				}
				for _, p := range sp.Panes {
					if p.NumericID == msg.paneNum {
						if m.bgDetails == nil {
							m.bgDetails = map[string]*model.HookDetail{}
						}
						m.bgDetails[paneBgKey(sp.Name, p.ID)] = msg.detail
						break
					}
				}
			}
		}
		if msg.session == m.hookSession && msg.paneNum == m.hookPaneNum {
			m.hookLoading = false
			if msg.err != "" {
				m.toast = msg.err
			} else {
				m.hookDetail = msg.detail
			}
		}
		return m, nil
	case harnessesMsg:
		m.agentsLoading = false
		if msg.err != "" {
			if m.mode != ModeConversations {
				m.toast = "Could not list harnesses: " + msg.err
			}
			m.harnesses = nil
		} else {
			m.harnesses = msg.entries
			if len(m.harnesses) == 0 && m.mode != ModeConversations {
				m.toast = "No working harnesses found."
			}
		}
		m.harnessCursor = 0
		m.profileOf = nil
		if m.mode == ModeConversations && m.convPickWaitingHarnesses {
			return m.finishConvPickAfterHarnesses()
		}
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// rows + filtering
// ---------------------------------------------------------------------------

func (m *Model) visibleRows() []Row {
	q := strings.TrimSpace(m.query)
	type matchedPane struct {
		idx   int
		score int
	}
	type matchedSpace struct {
		idx   int
		score int
		panes []matchedPane
	}
	var spaces []matchedSpace
	for si, sp := range m.spaces {
		spaceScore, spaceOK := 0, true
		if q != "" {
			spaceScore, spaceOK = model.SpaceScore(sp, q)
			if !spaceOK {
				continue
			}
		}
		// Check if the space itself (name/cwd/branch) matched directly so we know
		// whether to keep all panes under it or only matching panes.
		ql := strings.ToLower(q)
		selfMatch := q == "" || strings.Contains(strings.ToLower(sp.Name), ql) ||
			strings.Contains(strings.ToLower(sp.CWD), ql) ||
			(sp.Git != nil && sp.Git.Branch != "" && strings.Contains(strings.ToLower(sp.Git.Branch), ql))
		var matched []matchedPane
		for idx, p := range sp.Panes {
			if !m.filter.Matches(p.Status) {
				continue
			}
			if q != "" {
				ps, ok := model.PaneScore(p, q)
				if !ok && !selfMatch {
					continue
				}
				if !ok {
					ps = 100
				}
				matched = append(matched, matchedPane{idx: idx, score: ps})
			} else {
				matched = append(matched, matchedPane{idx: idx})
			}
		}
		if m.filter != model.FilterAll && len(matched) == 0 {
			continue
		}
		if q != "" {
			sort.SliceStable(matched, func(i, j int) bool {
				return matched[i].score < matched[j].score
			})
		}
		spaces = append(spaces, matchedSpace{idx: si, score: spaceScore, panes: matched})
	}
	if q != "" {
		sort.SliceStable(spaces, func(i, j int) bool {
			return spaces[i].score < spaces[j].score
		})
	}
	var rows []Row
	for _, ms := range spaces {
		sp := m.spaces[ms.idx]
		rows = append(rows, Row{Kind: RowSpace, SpaceIdx: ms.idx})
		show := true
		if q == "" && m.filter == model.FilterAll && m.collapsed[sp.Name] {
			show = false
		}
		if show {
			for _, mp := range ms.panes {
				rows = append(rows, Row{Kind: RowPane, SpaceIdx: ms.idx, PaneIdx: mp.idx})
			}
		}
	}
	if m.filter == model.FilterAll && len(m.exited) > 0 {
		ql := strings.ToLower(q)
		var matchedExited []int
		for ei, ex := range m.exited {
			if q == "" ||
				strings.Contains(strings.ToLower(ex.Name), ql) ||
				strings.Contains(strings.ToLower(ex.CWD), ql) ||
				strings.Contains(strings.ToLower(strings.Join(ex.Agents, " ")), ql) {
				matchedExited = append(matchedExited, ei)
			}
		}
		if len(matchedExited) > 0 {
			rows = append(rows, Row{Kind: RowExitedHeader})
			for _, ei := range matchedExited {
				rows = append(rows, Row{Kind: RowExited, ExitedIdx: ei})
			}
		}
	}
	if q != "" {
		if err := validateSpaceName(q); err == "" && !m.spaceExists(q) {
			rows = append(rows, Row{Kind: RowCreate, CreateName: q})
		}
	}
	return rows
}

func (m *Model) clampSelection() {
	n := 0
	switch m.mode {
	case ModeInbox:
		n = len(m.inboxItems())
	case ModeConversations:
		n = len(m.conversationItems())
	case ModeShared:
		n = len(m.sharedItems())
	case ModeTabs:
		n = len(m.tabsItems())
	case ModeWorktrees:
		m.clampWorktreeSelection()
		return
	default:
		n = len(m.visibleRows())
	}
	if n == 0 {
		m.selected = 0
		return
	}
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= n {
		m.selected = n - 1
	}
}

func (m *Model) selectedRow() *Row {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return nil
	}
	r := rows[m.selected]
	return &r
}

func (m *Model) selectedSpaceName() string {
	if m.isTabsContext() {
		if _, sp := m.tabsSpace(); sp != nil {
			return sp.Name
		}
		return ""
	}
	if m.isInboxContext() {
		items := m.inboxItems()
		if m.selected >= 0 && m.selected < len(items) {
			item := items[m.selected]
			if item.IsOutcome {
				if item.OutcomeIdx >= 0 && item.OutcomeIdx < len(m.outcomes) {
					return m.outcomes[item.OutcomeIdx].Session
				}
				return ""
			}
			return m.spaces[item.SpaceIdx].Name
		}
		return ""
	}
	r := m.selectedRow()
	if r == nil {
		return ""
	}
	switch r.Kind {
	case RowSpace:
		if r.SpaceIdx < len(m.spaces) {
			return m.spaces[r.SpaceIdx].Name
		}
	case RowPane:
		if r.SpaceIdx < len(m.spaces) {
			return m.spaces[r.SpaceIdx].Name
		}
	}
	return ""
}

func (m *Model) selectedPane() *model.Pane {
	if m.isTabsContext() {
		tab := m.selectedTab()
		if tab == nil {
			return nil
		}
		return m.selectedTabPane(*tab)
	}
	if m.isInboxContext() {
		items := m.inboxItems()
		if m.selected < 0 || m.selected >= len(items) {
			return nil
		}
		item := items[m.selected]
		if item.IsOutcome {
			return nil
		}
		return &m.spaces[item.SpaceIdx].Panes[item.PaneIdx]
	}
	r := m.selectedRow()
	if r == nil || r.Kind != RowPane {
		return nil
	}
	if r.SpaceIdx >= len(m.spaces) {
		return nil
	}
	sp := &m.spaces[r.SpaceIdx]
	if r.PaneIdx >= len(sp.Panes) {
		return nil
	}
	return &sp.Panes[r.PaneIdx]
}

func (m *Model) currentSpace() *model.Space {
	for i := range m.spaces {
		if m.spaces[i].IsCurrent {
			return &m.spaces[i]
		}
	}
	if name := m.opts.Target; name != "" {
		for i := range m.spaces {
			if m.spaces[i].Name == name {
				return &m.spaces[i]
			}
		}
	}
	return nil
}

func (m *Model) moveSelection(delta int) {
	rows := m.visibleRows()
	if len(rows) == 0 {
		return
	}
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(rows) {
		m.selected = len(rows) - 1
	}
	// Skip non-selectable section headers.
	if rows[m.selected].Kind == RowExitedHeader {
		step := 1
		if delta < 0 {
			step = -1
		}
		next := m.selected + step
		if next >= 0 && next < len(rows) && rows[next].Kind != RowExitedHeader {
			m.selected = next
		} else if alt := m.selected - step; alt >= 0 && alt < len(rows) && rows[alt].Kind != RowExitedHeader {
			m.selected = alt
		}
	}
}

func (m *Model) jumpSpace(dir int) {
	rows := m.visibleRows()
	var spaceRows []int
	for i, r := range rows {
		if r.Kind == RowSpace {
			spaceRows = append(spaceRows, i)
		}
	}
	if len(spaceRows) == 0 {
		return
	}
	cur := 0
	best := 0
	for i, ri := range spaceRows {
		if ri <= m.selected {
			best = i
		}
		_ = cur
	}
	_ = best
	// find current space position
	pos := 0
	for i, ri := range spaceRows {
		if ri == m.selected {
			pos = i
			break
		}
		if ri < m.selected {
			pos = i
		}
	}
	// If selected is a pane, attribute to its space row above.
	for i, ri := range spaceRows {
		if ri <= m.selected {
			pos = i
		}
	}
	next := pos + dir
	if next < 0 {
		next = 0
	}
	if next >= len(spaceRows) {
		next = len(spaceRows) - 1
	}
	m.selected = spaceRows[next]
}

func (m *Model) selectSpaceNumber(n int) {
	rows := m.visibleRows()
	count := 0
	for i, r := range rows {
		if r.Kind == RowSpace {
			count++
			if count == n {
				m.selected = i
				return
			}
		}
	}
}

func (m *Model) setFilter(f model.AttentionFilter) {
	if m.filter == f {
		m.filter = model.FilterAll
	} else {
		m.filter = f
	}
	m.selected = 0
	m.clampSelection()
}

func (m *Model) spaceExists(name string) bool {
	for _, sp := range m.spaces {
		if strings.EqualFold(sp.Name, name) {
			return true
		}
	}
	return false
}

func validateSpaceName(name string) string {
	t := strings.TrimSpace(name)
	if t == "" {
		return "Name cannot be empty."
	}
	for _, r := range t {
		if r < 32 {
			return "Name cannot contain control characters."
		}
	}
	return ""
}

func uniqueName(base string, exists func(string) bool) string {
	if !exists(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := base + "-" + itoa(i)
		if !exists(cand) {
			return cand
		}
	}
	return base + "-new"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// dialogs
// ---------------------------------------------------------------------------

func (m *Model) prefillNewSpace(prefill string) {
	cwd := ""
	if cur := m.currentSpace(); cur != nil && cur.CWD != "" {
		cwd = cur.CWD
	} else if m.opts.HomeDir != "" {
		cwd = m.opts.HomeDir
	}
	base := strings.TrimSpace(prefill)
	if base == "" {
		if cwd != "" {
			base = path.Base(strings.TrimRight(cwd, "/"))
			if base == "" || base == "/" || base == "." {
				base = "space"
			}
		} else {
			base = "space"
		}
	}
	if errStr := validateSpaceName(base); errStr != "" {
		base = "space"
	}
	name := uniqueName(base, m.spaceExists)
	m.newName = name
	m.newNameCur = len([]rune(name))
	m.newNameEdited = strings.TrimSpace(prefill) != ""
	m.newCWD = cwd
	m.newCWDCur = len([]rune(cwd))
	m.newField = 0
	m.newError = ""
}

func (m *Model) openNew(prefill string) tea.Cmd {
	m.returnMode = ModeNavigate
	m.newIsTab = false
	m.newTabSession = ""
	m.prefillNewSpace(prefill)
	m.mode = ModeNewSpace
	return m.initNewSpaceWorktrees()
}

func (m *Model) requestRename() {
	name := m.selectedSpaceName()
	if name == "" {
		m.toast = "Select a running space to rename."
		return
	}
	m.renameWindow = false
	m.renameWindowID = ""
	m.renameStandalone = false
	m.renameOld = name
	m.renameText = name
	m.renameCur = len([]rune(name))
	m.renameError = ""
	m.mode = ModeRename
}

func (m *Model) prefillStandaloneRename() {
	cur := m.currentSpace()
	if cur == nil && len(m.spaces) > 0 {
		cur = &m.spaces[0]
	}
	if cur == nil {
		return
	}
	if m.renameWindow {
		var winName, winID string
		for _, w := range cur.Windows {
			if w.Active {
				winName = w.Name
				winID = w.ID
				break
			}
		}
		if winID == "" && len(cur.Windows) > 0 {
			winName = cur.Windows[0].Name
			winID = cur.Windows[0].ID
		}
		m.renameWindowID = winID
		m.renameOld = winName
		m.renameText = winName
		m.renameCur = len([]rune(winName))
		m.renameError = ""
		return
	}
	m.renameOld = cur.Name
	m.renameText = cur.Name
	m.renameCur = len([]rune(cur.Name))
	m.renameError = ""
}

func (m *Model) requestKill() tea.Cmd {
	m.confirmWorktreePath = ""
	m.confirmWorktreeName = ""
	m.confirmWorktreeBranch = ""
	m.confirmWorktreeManaged = false
	m.confirmRemoveWorktree = false
	m.confirmWorktreeSafety = WorktreeRemoveSafety{}
	if r := m.selectedRow(); r != nil && r.Kind == RowExited && r.ExitedIdx < len(m.exited) {
		ex := m.exited[r.ExitedIdx]
		m.confirmSpace = ex.Name
		m.confirmWindows = ex.WindowCount
		m.confirmPanes = ex.PaneCount
		m.confirmExited = true
		m.mode = ModeConfirmKill
		return nil
	}
	name := m.selectedSpaceName()
	if name == "" {
		return nil
	}
	for _, sp := range m.spaces {
		if sp.Name == name {
			if sp.IsCurrent {
				m.toast = "This is the current space. Switch away before closing it."
				return nil
			}
			m.confirmSpace = name
			m.confirmWindows = len(sp.Windows)
			if m.confirmWindows == 0 && len(sp.Panes) > 0 {
				seen := map[string]bool{}
				for _, p := range sp.Panes {
					seen[p.WindowID] = true
				}
				m.confirmWindows = len(seen)
			}
			m.confirmPanes = len(sp.Panes)
			m.confirmExited = false
			m.mode = ModeConfirmKill

			// Detect if this space is backed by a linked worktree not shared by another running space.
			wtPath := sp.WorktreePath
			wtName := sp.WorktreeName
			wtBranch := ""
			wtManaged := sp.WorktreeManaged
			if sp.Git != nil && sp.Git.IsWorktree {
				if wtPath == "" {
					wtPath = sp.Git.RepoRoot
				}
				if wtName == "" {
					wtName = sp.Git.WorktreeName
				}
				wtBranch = sp.Git.Branch
				if sp.Git.Managed {
					wtManaged = true
				}
			}
			if wtPath != "" && !m.worktreeUsedByOtherSpace(sp.Name, wtPath) {
				m.confirmWorktreePath = wtPath
				m.confirmWorktreeName = wtName
				if m.confirmWorktreeName == "" {
					m.confirmWorktreeName = path.Base(wtPath)
				}
				m.confirmWorktreeBranch = wtBranch
				m.confirmWorktreeManaged = wtManaged
				return m.inspectWorktreeSafetyCmd(wtPath)
			}
			return nil
		}
	}
	return nil
}

func (m *Model) worktreeUsedByOtherSpace(excludeSpace, wtPath string) bool {
	for _, sp := range m.spaces {
		if sp.Name == excludeSpace {
			continue
		}
		if sameCleanPath(sp.WorktreePath, wtPath) || sameCleanPath(sp.CWD, wtPath) || isSubPath(sp.CWD, wtPath) {
			return true
		}
		for _, p := range sp.Panes {
			if sameCleanPath(p.CWD, wtPath) || isSubPath(p.CWD, wtPath) {
				return true
			}
		}
	}
	return false
}

func (m *Model) worktreeUsedByOtherWindow(excludeWinID, wtPath string) bool {
	for _, sp := range m.spaces {
		for _, w := range sp.Windows {
			if w.ID != "" && w.ID == excludeWinID {
				continue
			}
			if sameCleanPath(w.WorktreePath, wtPath) || isSubPath(w.WorktreePath, wtPath) {
				return true
			}
		}
		for _, p := range sp.Panes {
			if p.WindowID != "" && p.WindowID == excludeWinID {
				continue
			}
			if sameCleanPath(p.CWD, wtPath) || isSubPath(p.CWD, wtPath) {
				return true
			}
		}
	}
	return false
}

func (m *Model) toggleExpandSelectedPane(forceExpand *bool) tea.Cmd {
	p := m.selectedPane()
	if p == nil {
		if forceExpand == nil {
			m.toast = "Select an agent pane with background work."
		}
		return nil
	}
	space := m.selectedSpaceName()
	if !m.paneCanExpandBackground(space, *p) {
		if forceExpand == nil {
			m.toast = "No background work for this pane."
		}
		return nil
	}
	if m.expandedBg == nil {
		m.expandedBg = map[string]bool{}
	}
	key := paneBgKey(space, p.ID)
	want := !m.expandedBg[key]
	if forceExpand != nil {
		want = *forceExpand
	}
	if want {
		m.expandedBg[key] = true
		if len(m.paneBackgroundItems(space, *p)) == 0 && p.Hook != nil && p.Hook.HasDetail {
			return m.loadHookDetailCmd(space, p.NumericID)
		}
	} else {
		delete(m.expandedBg, key)
	}
	return nil
}

func (m *Model) openHookDetail() tea.Cmd {
	p := m.selectedPane()
	if p == nil {
		// Allow hook detail on a space row if it has exactly one pane? No:
		// require a pane selection.
		m.toast = "Select an agent pane with hook data."
		return nil
	}
	space := m.selectedSpaceName()
	m.hookSession = space
	m.hookPaneNum = p.NumericID
	m.hookDetail = nil
	m.hookScroll = 0
	hasDetail := p.Hook != nil && p.Hook.HasDetail
	m.hookLoading = hasDetail
	m.returnMode = m.mode
	m.mode = ModeHookDetail
	if hasDetail {
		return m.loadHookDetailCmd(space, p.NumericID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// agents selector
// ---------------------------------------------------------------------------

func (m *Model) filteredHarnessIdx() []int {
	q := strings.ToLower(strings.TrimSpace(m.agentFilter))
	var out []int
	for i, h := range m.harnesses {
		if q == "" || strings.Contains(strings.ToLower(h.Name), q) ||
			strings.Contains(strings.ToLower(h.Description), q) {
			out = append(out, i)
		}
	}
	return out
}

type profileRow struct {
	isDefault bool
	profIdx   int
}

func (m *Model) profileRows() []profileRow {
	if m.profileOf == nil || *m.profileOf >= len(m.harnesses) {
		return nil
	}
	agent := m.harnesses[*m.profileOf]
	q := strings.ToLower(strings.TrimSpace(m.agentFilter))
	var rows []profileRow
	if strings.ToLower(strings.TrimSpace(agent.BinaryState)) != "missing" &&
		(q == "" || strings.Contains("default", q) || strings.Contains(strings.ToLower(agent.Name), q)) {
		rows = append(rows, profileRow{isDefault: true})
	}
	for i, p := range agent.Profiles {
		if q == "" || strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.DisplayName), q) ||
			strings.Contains(strings.ToLower(p.Source), q) {
			rows = append(rows, profileRow{profIdx: i})
		}
	}
	return rows
}

func (m *Model) agentsVisibleCount() int {
	if m.profileOf != nil {
		return len(m.profileRows())
	}
	return len(m.filteredHarnessIdx())
}

func (m *Model) clampHarnessCursor() {
	n := m.agentsVisibleCount()
	if n == 0 {
		m.harnessCursor = 0
		return
	}
	if m.harnessCursor < 0 {
		m.harnessCursor = 0
	}
	if m.harnessCursor >= n {
		m.harnessCursor = n - 1
	}
}

func (m *Model) activateAgentRow() {
	if m.profileOf != nil {
		rows := m.profileRows()
		if m.harnessCursor < 0 || m.harnessCursor >= len(rows) {
			m.toast = "No profiles available."
			return
		}
		agent := m.harnesses[*m.profileOf]
		row := rows[m.harnessCursor]
		profile := ""
		if !row.isDefault {
			if row.profIdx < 0 || row.profIdx >= len(agent.Profiles) {
				m.toast = "No profiles available."
				return
			}
			profile = agent.Profiles[row.profIdx].Name
		}
		m.launchAgent(agent.Name, profile)
		return
	}
	vis := m.filteredHarnessIdx()
	if m.harnessCursor < 0 || m.harnessCursor >= len(vis) {
		m.toast = "No harnesses available."
		return
	}
	agent := m.harnesses[vis[m.harnessCursor]]
	if len(agent.Profiles) == 0 {
		m.launchAgent(agent.Name, "")
		return
	}
	idx := vis[m.harnessCursor]
	cp := idx
	m.profileOf = &cp
	m.harnessCursor = 0
	m.agentFilter = ""
	m.agentCur = 0
}

func (m *Model) launchAgent(agent, profile string) {
	session := ""
	cwd := m.opts.HomeDir
	if cur := m.currentSpace(); cur != nil {
		session = cur.Name
		if cur.CWD != "" {
			cwd = cur.CWD
		}
	}
	if session == "" && m.opts.Target != "" {
		session = m.opts.Target
	}
	if session == "" && len(m.spaces) > 0 {
		session = m.spaces[0].Name
		if m.spaces[0].CWD != "" {
			cwd = m.spaces[0].CWD
		}
	}
	ctx := context.Background()
	if err := m.backend.LaunchAgent(ctx, session, cwd, agent, profile); err != nil {
		m.toast = "Could not open " + agent + ": " + err.Error()
		m.agentsLoading = false
		return
	}
	m.quitting = true
}

// ---------------------------------------------------------------------------
// actions
// ---------------------------------------------------------------------------

func (m *Model) activateSelected() tea.Cmd {
	r := m.selectedRow()
	if r == nil {
		return nil
	}
	ctx := context.Background()
	switch r.Kind {
	case RowSpace:
		name := m.spaces[r.SpaceIdx].Name
		if err := m.backend.OpenSpace(ctx, name); err != nil {
			m.toast = err.Error()
			return nil
		}
		m.quitting = true
	case RowPane:
		sp := m.spaces[r.SpaceIdx]
		p := sp.Panes[r.PaneIdx]
		if err := m.backend.FocusPane(ctx, sp.Name, p.WindowID, p.ID); err != nil {
			m.toast = err.Error()
			return nil
		}
		m.quitting = true
	case RowExited:
		if r.ExitedIdx >= len(m.exited) {
			return nil
		}
		rb, ok := m.backend.(ResurrectBackend)
		if !ok {
			m.toast = "Restore is not available."
			return nil
		}
		name := m.exited[r.ExitedIdx].Name
		if err := rb.RestoreSpace(ctx, name); err != nil {
			m.toast = "Restore failed: " + err.Error()
			return nil
		}
		m.quitting = true
	case RowCreate:
		return m.openNew(r.CreateName)
	}
	return nil
}

// ---------------------------------------------------------------------------
// key handling
// ---------------------------------------------------------------------------

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global Ctrl-r refresh (navigator modes + agents + inbox + conversations + shared).
	if msg.Type == tea.KeyCtrlR {
		if m.mode == ModeAgents {
			m.agentsLoading = true
			return m, m.loadHarnessesCmd()
		}
		if m.mode == ModeConversations {
			if m.convPickPending != nil {
				return m, nil
			}
			m.convRefreshing = true
			return m, tea.Batch(m.loadConversationsCmd(true, m.convIncludeArchived), m.nextLoadSpacesCmd(), m.loadHarnessesCmd())
		}
		if m.mode == ModeShared {
			m.sharedLoading = true
			return m, m.loadSharedCmd()
		}
		if m.mode == ModeWorktrees {
			m.worktreesLoading = true
			return m, tea.Batch(m.nextLoadSpacesCmd(), m.loadAllWorktreesCmd())
		}
		if m.mode == ModeNavigate || m.mode == ModeSearch || m.mode == ModeHelp || m.mode == ModeInbox || m.mode == ModeTabs {
			cmds := []tea.Cmd{m.nextLoadSpacesCmd()}
			if !m.gitFetching {
				if cwds := m.collectCWDs(); len(cwds) > 0 {
					if gcmd := m.loadGitStatusCmd(cwds, true); gcmd != nil {
						m.gitFetching = true
						cmds = append(cmds, gcmd)
					}
				}
			}
			if m.mode == ModeTabs {
				if tcmd := m.syncTabsInspectorCmd(true); tcmd != nil {
					cmds = append(cmds, tcmd)
				}
			}
			return m, tea.Batch(cmds...)
		}
		return m, nil
	}
	switch m.mode {
	case ModeNavigate:
		return m.handleNavigate(msg)
	case ModeSearch:
		return m.handleSearch(msg)
	case ModeHelp:
		if msg.Type == tea.KeyEsc || msg.Type == tea.KeyRunes && runeString(msg) == "?" {
			m.mode = m.exitOverlayMode()
			return m, nil
		}
		return m, nil
	case ModeNewSpace:
		return m.handleNewSpace(msg)
	case ModeRename:
		return m.handleRename(msg)
	case ModeConfirmKill:
		return m.handleConfirm(msg)
	case ModeConfirmRemoveWorktree:
		return m.handleConfirmRemoveWorktree(msg)
	case ModeHookDetail:
		return m.handleHookDetail(msg)
	case ModeAgents:
		return m.handleAgents(msg)
	case ModeInbox:
		return m.handleInbox(msg)
	case ModeConversations:
		return m.handleConversations(msg)
	case ModeShared:
		return m.handleShared(msg)
	case ModeTabs:
		return m.handleTabs(msg)
	case ModeWorktrees:
		return m.handleWorktrees(msg)
	}
	return m, nil
}

func runeString(msg tea.KeyMsg) string {
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		return string(msg.Runes)
	}
	return ""
}

func (m Model) handleNavigate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Printable / control dispatch.
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
		cmd := m.activateSelected()
		if m.quitting {
			return m, tea.Quit
		}
		return m, cmd
	case tea.KeyUp:
		m.moveSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSelection(1)
		return m, nil
	case tea.KeyLeft:
		m.jumpSpace(-1)
		return m, nil
	case tea.KeyRight:
		m.jumpSpace(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveSelection(10)
		return m, nil
	case tea.KeyHome:
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeyEnd:
		rows := m.visibleRows()
		m.selected = len(rows) - 1
		m.clampSelection()
		return m, nil
	case tea.KeyTab:
		m.filter = nextFilter(m.filter, false)
		m.selected = 0
		m.clampSelection()
		return m, nil
	case tea.KeySpace:
		return m, m.toggleSpaceOrPaneExpand()
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		ch := string(msg.Runes)
		switch ch {
		case "k":
			m.moveSelection(-1)
		case "j":
			m.moveSelection(1)
		case "h":
			m.jumpSpace(-1)
		case "l":
			m.jumpSpace(1)
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
			m.mode = ModeSearch
		case "?":
			m.returnMode = ModeNavigate
			m.mode = ModeHelp
		case "H":
			cmd := m.openHookDetail()
			return m, cmd
		case "e":
			cmd := m.toggleExpandSelectedPane(nil)
			return m, cmd
		case "n":
			return m, m.openNew("")
		case "W":
			return m, m.openWorktreesView()
		case "r":
			m.requestRename()
		case "x":
			return m, m.requestKill()
		case " ":
			return m, m.toggleSpaceOrPaneExpand()
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
			rows := m.visibleRows()
			m.selected = len(rows) - 1
			m.clampSelection()
		default:
			if len(ch) == 1 && ch[0] >= '1' && ch[0] <= '9' {
				m.selectSpaceNumber(int(ch[0] - '0'))
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) toggleSpaceOrPaneExpand() tea.Cmd {
	if p := m.selectedPane(); p != nil {
		space := m.selectedSpaceName()
		if m.paneCanExpandBackground(space, *p) {
			return m.toggleExpandSelectedPane(nil)
		}
	}
	name := m.selectedSpaceName()
	if name != "" {
		if m.collapsed[name] {
			delete(m.collapsed, name)
		} else {
			m.collapsed[name] = true
		}
		// Keep selection on the space row.
		rows := m.visibleRows()
		for i, r := range rows {
			if r.Kind == RowSpace && m.spaces[r.SpaceIdx].Name == name {
				m.selected = i
				break
			}
		}
	}
	return nil
}

func (m Model) handleSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if strings.TrimSpace(m.query) != "" {
			m.query = ""
			m.cursor = 0
			m.selected = 0
			m.mode = ModeNavigate
			m.clampSelection()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		m.mode = ModeNavigate
		cmd := m.activateSelected()
		if m.quitting {
			return m, tea.Quit
		}
		return m, cmd
	case tea.KeyUp:
		m.moveSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveSelection(10)
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
	case tea.KeyHome:
		m.cursor = 0
		return m, nil
	case tea.KeyEnd:
		m.cursor = len([]rune(m.query))
		return m, nil
	case tea.KeyBackspace:
		m.deleteRuneBefore()
		m.afterQueryChange()
		return m, nil
	case tea.KeyDelete:
		m.deleteRuneAt()
		m.afterQueryChange()
		return m, nil
	case tea.KeyCtrlU:
		m.query = ""
		m.cursor = 0
		m.afterQueryChange()
		return m, nil
	case tea.KeyCtrlW:
		m.deleteWord()
		m.afterQueryChange()
		return m, nil
	case tea.KeyCtrlA:
		m.cursor = 0
		return m, nil
	case tea.KeyCtrlE:
		m.cursor = len([]rune(m.query))
		return m, nil
	case tea.KeySpace:
		m.insertRune(' ')
		m.afterQueryChange()
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			m.insertRune(r)
		}
		m.afterQueryChange()
		return m, nil
	}
	return m, nil
}

func (m *Model) afterQueryChange() {
	m.mode = ModeSearch
	m.selected = 0
	m.clampSelection()
}

func (m *Model) insertRune(r rune) {
	rs := []rune(m.query)
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > len(rs) {
		m.cursor = len(rs)
	}
	rs = append(rs[:m.cursor], append([]rune{r}, rs[m.cursor:]...)...)
	m.query = string(rs)
	m.cursor++
}

func (m *Model) deleteRuneBefore() {
	rs := []rune(m.query)
	if m.cursor <= 0 || len(rs) == 0 {
		return
	}
	m.cursor--
	m.query = string(append(rs[:m.cursor], rs[m.cursor+1:]...))
}

func (m *Model) deleteRuneAt() {
	rs := []rune(m.query)
	if m.cursor < 0 || m.cursor >= len(rs) {
		return
	}
	m.query = string(append(rs[:m.cursor], rs[m.cursor+1:]...))
}

func (m *Model) deleteWord() {
	rs := []rune(m.query)
	for m.cursor > 0 && isSpace(rs[m.cursor-1]) {
		m.deleteRuneBefore()
		rs = []rune(m.query)
	}
	for m.cursor > 0 && !isSpace(rs[m.cursor-1]) {
		m.deleteRuneBefore()
		rs = []rune(m.query)
	}
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

func nextFilter(f model.AttentionFilter, backwards bool) model.AttentionFilter {
	all := model.AllFilters()
	idx := 0
	for i, v := range all {
		if v == f {
			idx = i
			break
		}
	}
	if backwards {
		return all[(idx+len(all)-1)%len(all)]
	}
	return all[(idx+1)%len(all)]
}

// --- New Space dialog ---

func (m Model) handleNewSpace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.handleNewSpaceWithWorktree(msg)
}

// --- Rename dialog ---

func (m Model) handleRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if m.renameStandalone {
			m.quitting = true
			return m, tea.Quit
		}
		m.mode = m.exitOverlayMode()
		return m, nil
	case tea.KeyEnter:
		name := strings.TrimSpace(m.renameText)
		if m.renameWindow {
			if name == "" {
				m.renameError = "Tab name cannot be empty."
				return m, nil
			}
			if name == m.renameOld {
				if m.renameStandalone {
					m.quitting = true
					return m, tea.Quit
				}
				m.mode = m.exitOverlayMode()
				return m, nil
			}
			wb, ok := m.backend.(WindowRenameBackend)
			if !ok {
				m.renameError = "Window rename is not supported."
				return m, nil
			}
			if err := wb.RenameWindow(context.Background(), m.renameWindowID, name); err != nil {
				m.renameError = err.Error()
				return m, nil
			}
			if m.returnMode == ModeTabs {
				for si := range m.spaces {
					for wi := range m.spaces[si].Windows {
						if m.spaces[si].Windows[wi].ID == m.renameWindowID {
							m.spaces[si].Windows[wi].Name = name
						}
					}
					for pi := range m.spaces[si].Panes {
						if m.spaces[si].Panes[pi].WindowID == m.renameWindowID {
							m.spaces[si].Panes[pi].WindowName = name
						}
					}
				}
				m.mode = ModeTabs
				return m, m.nextLoadSpacesCmd()
			}
			m.mode = ModeNavigate
			m.quitting = true
			return m, tea.Quit
		}
		if errStr := validateSpaceName(name); errStr != "" {
			m.renameError = errStr
			return m, nil
		}
		name = sanitizeName(name)
		if name == m.renameOld {
			if m.renameStandalone {
				m.quitting = true
				return m, tea.Quit
			}
			m.mode = ModeNavigate
			return m, nil
		}
		for _, sp := range m.spaces {
			if !strings.EqualFold(sp.Name, m.renameOld) && strings.EqualFold(sp.Name, name) {
				m.renameError = "A space named '" + name + "' already exists."
				return m, nil
			}
		}
		if err := m.backend.RenameSpace(context.Background(), m.renameOld, name); err != nil {
			m.renameError = err.Error()
			return m, nil
		}
		m.mode = ModeNavigate
		m.quitting = true
		return m, tea.Quit
	case tea.KeyBackspace, tea.KeyDelete, tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd, tea.KeyCtrlU, tea.KeyCtrlW, tea.KeyCtrlA, tea.KeyCtrlE, tea.KeyRunes, tea.KeySpace:
		m.renameError = ""
		m.renameText, m.renameCur = editText(m.renameText, m.renameCur, msg)
		return m, nil
	}
	return m, nil
}

// --- Confirm kill ---

func (m Model) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.confirmWindow = false
		m.confirmWorktreePath = ""
		m.confirmRemoveWorktree = false
		m.mode = m.exitOverlayMode()
		return m, nil
	case tea.KeyEnter:
		return m.confirmKill()
	case tea.KeySpace:
		if !m.confirmExited && m.confirmWorktreePath != "" {
			m.confirmRemoveWorktree = !m.confirmRemoveWorktree
		}
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch string(msg.Runes) {
		case "y", "Y":
			return m.confirmKill()
		case "n", "N":
			m.confirmWindow = false
			m.confirmWorktreePath = ""
			m.confirmRemoveWorktree = false
			m.mode = m.exitOverlayMode()
			return m, nil
		case "w", "W":
			if !m.confirmExited && m.confirmWorktreePath != "" {
				m.confirmRemoveWorktree = !m.confirmRemoveWorktree
			}
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

func (m Model) confirmKill() (tea.Model, tea.Cmd) {
	if m.confirmWindow {
		wb, ok := m.backend.(WindowManageBackend)
		if !ok {
			m.toast = "Closing tabs is not supported."
			m.confirmWindow = false
			m.mode = m.exitOverlayMode()
			return m, nil
		}
		winID := m.confirmWindowID
		if err := wb.KillWindow(context.Background(), winID); err != nil {
			m.toast = err.Error()
			m.confirmWindow = false
			m.confirmWorktreePath = ""
			m.confirmRemoveWorktree = false
			m.mode = m.exitOverlayMode()
			return m, nil
		}
		if m.confirmRemoveWorktree && m.confirmWorktreePath != "" {
			if wtb, ok := m.backend.(WorktreeBackend); ok {
				deleteBranch := m.confirmWorktreeManaged && m.confirmWorktreeSafety.UnmergedCommits == 0
				if err := wtb.RemoveWorktree(context.Background(), m.confirmWorktreePath, false, deleteBranch, nil); err != nil {
					m.toast = "Tab closed, but worktree kept: " + err.Error()
				}
			}
		}
		for si := range m.spaces {
			if m.spaces[si].Name != m.confirmSpace {
				continue
			}
			var keptWins []model.Window
			for _, w := range m.spaces[si].Windows {
				if w.ID != winID {
					keptWins = append(keptWins, w)
				}
			}
			m.spaces[si].Windows = keptWins
			var keptPanes []model.Pane
			for _, p := range m.spaces[si].Panes {
				if p.WindowID != winID {
					keptPanes = append(keptPanes, p)
				}
			}
			m.spaces[si].Panes = keptPanes
		}
		delete(m.tabsPaneSel, winID)
		m.confirmWindow = false
		m.confirmWorktreePath = ""
		m.confirmRemoveWorktree = false
		m.mode = ModeTabs
		m.clampSelection()
		return m, tea.Batch(m.nextLoadSpacesCmd(), m.syncTabsInspectorCmd(false))
	}
	if m.confirmExited {
		if rb, ok := m.backend.(ResurrectBackend); ok {
			if err := rb.DeleteExited(context.Background(), m.confirmSpace); err != nil {
				m.toast = err.Error()
				m.mode = ModeNavigate
				return m, nil
			}
		}
		var kept []model.ExitedSpace
		for _, ex := range m.exited {
			if ex.Name != m.confirmSpace {
				kept = append(kept, ex)
			}
		}
		m.exited = kept
		m.spacesSeq++
		m.confirmExited = false
		m.mode = ModeNavigate
		m.clampSelection()
		return m, nil
	}
	for _, sp := range m.spaces {
		if sp.Name == m.confirmSpace && sp.IsCurrent {
			m.toast = "This is the current space. Switch away before closing it."
			m.mode = ModeNavigate
			return m, nil
		}
	}
	if err := m.backend.KillSpace(context.Background(), m.confirmSpace); err != nil {
		m.toast = err.Error()
		m.mode = ModeNavigate
		return m, nil
	}
	if m.confirmRemoveWorktree && m.confirmWorktreePath != "" {
		if wb, ok := m.backend.(WorktreeBackend); ok {
			deleteBranch := m.confirmWorktreeManaged && m.confirmWorktreeSafety.UnmergedCommits == 0
			if err := wb.RemoveWorktree(context.Background(), m.confirmWorktreePath, false, deleteBranch, nil); err != nil {
				m.toast = "Space closed, but worktree kept: " + err.Error()
			}
		}
	}
	// Remove locally and stay open.
	var kept []model.Space
	for _, sp := range m.spaces {
		if sp.Name != m.confirmSpace {
			kept = append(kept, sp)
		}
	}
	m.spaces = kept
	m.spacesSeq++
	m.selected = 0
	m.clampSelection()
	m.mode = ModeNavigate
	return m, nil
}

// --- Hook detail ---

func (m Model) handleHookDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = m.exitOverlayMode()
		return m, nil
	case tea.KeyUp:
		if m.hookScroll > 0 {
			m.hookScroll--
		}
		return m, nil
	case tea.KeyDown:
		m.hookScroll++
		return m, nil
	case tea.KeyPgUp:
		m.hookScroll -= 10
		if m.hookScroll < 0 {
			m.hookScroll = 0
		}
		return m, nil
	case tea.KeyPgDown:
		m.hookScroll += 10
		return m, nil
	case tea.KeyHome:
		m.hookScroll = 0
		return m, nil
	case tea.KeyEnd:
		m.hookScroll = 1 << 30
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch string(msg.Runes) {
		case "q", "H":
			m.mode = m.exitOverlayMode()
			return m, nil
		case "k":
			if m.hookScroll > 0 {
				m.hookScroll--
			}
			return m, nil
		case "j":
			m.hookScroll++
			return m, nil
		case "g":
			m.hookScroll = 0
			return m, nil
		case "G":
			m.hookScroll = 1 << 30
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// --- Agents ---

func (m Model) handleAgents(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.agentsLoading {
		if msg.Type == tea.KeyEsc {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		if m.profileOf != nil {
			m.profileOf = nil
			m.harnessCursor = 0
			m.agentFilter = ""
			m.agentCur = 0
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		m.activateAgentRow()
		if m.quitting {
			return m, tea.Quit
		}
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyUp:
		m.harnessCursor--
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyDown:
		m.harnessCursor++
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyPgUp:
		m.harnessCursor -= 10
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyPgDown:
		m.harnessCursor += 10
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyCtrlN:
		m.harnessCursor++
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyCtrlP:
		m.harnessCursor--
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyBackspace:
		m.deleteAgentRuneBefore()
		m.harnessCursor = 0
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyDelete:
		m.deleteAgentRuneAt()
		m.harnessCursor = 0
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyLeft:
		if m.agentCur > 0 {
			m.agentCur--
		}
		return m, nil
	case tea.KeyRight:
		if m.agentCur < len([]rune(m.agentFilter)) {
			m.agentCur++
		}
		return m, nil
	case tea.KeyHome:
		m.agentCur = 0
		return m, nil
	case tea.KeyEnd:
		m.agentCur = len([]rune(m.agentFilter))
		return m, nil
	case tea.KeyCtrlU:
		m.agentFilter = ""
		m.agentCur = 0
		m.harnessCursor = 0
		return m, nil
	case tea.KeyCtrlW:
		m.deleteAgentWord()
		m.harnessCursor = 0
		return m, nil
	case tea.KeyCtrlA:
		m.agentCur = 0
		return m, nil
	case tea.KeyCtrlE:
		m.agentCur = len([]rune(m.agentFilter))
		return m, nil
	case tea.KeySpace:
		rs := []rune(m.agentFilter)
		if m.agentCur < 0 {
			m.agentCur = 0
		}
		if m.agentCur > len(rs) {
			m.agentCur = len(rs)
		}
		rs = append(rs[:m.agentCur], append([]rune{' '}, rs[m.agentCur:]...)...)
		m.agentFilter = string(rs)
		m.agentCur++
		m.harnessCursor = 0
		m.clampHarnessCursor()
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			rs := []rune(m.agentFilter)
			if m.agentCur < 0 {
				m.agentCur = 0
			}
			if m.agentCur > len(rs) {
				m.agentCur = len(rs)
			}
			rs = append(rs[:m.agentCur], append([]rune{r}, rs[m.agentCur:]...)...)
			m.agentFilter = string(rs)
			m.agentCur++
		}
		m.harnessCursor = 0
		m.clampHarnessCursor()
		return m, nil
	}
	return m, nil
}

func (m *Model) deleteAgentRuneBefore() {
	rs := []rune(m.agentFilter)
	if m.agentCur <= 0 || len(rs) == 0 {
		return
	}
	m.agentCur--
	m.agentFilter = string(append(rs[:m.agentCur], rs[m.agentCur+1:]...))
}

func (m *Model) deleteAgentRuneAt() {
	rs := []rune(m.agentFilter)
	if m.agentCur < 0 || m.agentCur >= len(rs) {
		return
	}
	m.agentFilter = string(append(rs[:m.agentCur], rs[m.agentCur+1:]...))
}

func (m *Model) deleteAgentWord() {
	for {
		rs := []rune(m.agentFilter)
		if m.agentCur <= 0 || len(rs) == 0 {
			return
		}
		if !isSpace(rs[m.agentCur-1]) {
			break
		}
		m.deleteAgentRuneBefore()
	}
	for {
		rs := []rune(m.agentFilter)
		if m.agentCur <= 0 || len(rs) == 0 {
			return
		}
		if isSpace(rs[m.agentCur-1]) {
			break
		}
		m.deleteAgentRuneBefore()
	}
}

// --- Mouse ---

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.mode == ModeAgents {
			m.harnessCursor--
			m.clampHarnessCursor()
			return m, nil
		}
		if m.mode == ModeInbox {
			m.moveInboxSelection(-1)
			return m, nil
		}
		if m.mode == ModeConversations {
			if m.convPickPending != nil {
				m.moveConvPickCursor(-1)
				return m, nil
			}
			m.moveConvSelection(-1)
			return m, nil
		}
		if m.mode == ModeShared {
			if m.sharedInspecting {
				if m.sharedDetailScroll > 0 {
					m.sharedDetailScroll--
				}
			} else {
				m.moveSharedSelection(-1)
			}
			return m, nil
		}
		if m.mode == ModeTabs {
			return m.handleTabsWheel(msg.X, -1)
		}
		if m.mode == ModeHookDetail {
			if m.hookScroll > 0 {
				m.hookScroll--
			}
			return m, nil
		}
		if m.mode == ModeWorktrees {
			m.wtSelected--
			m.clampWorktreeSelection()
			return m, nil
		}
		m.moveSelection(-1)
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.mode == ModeAgents {
			m.harnessCursor++
			m.clampHarnessCursor()
			return m, nil
		}
		if m.mode == ModeInbox {
			m.moveInboxSelection(1)
			return m, nil
		}
		if m.mode == ModeConversations {
			if m.convPickPending != nil {
				m.moveConvPickCursor(1)
				return m, nil
			}
			m.moveConvSelection(1)
			return m, nil
		}
		if m.mode == ModeShared {
			if m.sharedInspecting {
				m.sharedDetailScroll++
			} else {
				m.moveSharedSelection(1)
			}
			return m, nil
		}
		if m.mode == ModeTabs {
			return m.handleTabsWheel(msg.X, 1)
		}
		if m.mode == ModeHookDetail {
			m.hookScroll++
			return m, nil
		}
		if m.mode == ModeWorktrees {
			m.wtSelected++
			m.clampWorktreeSelection()
			return m, nil
		}
		m.moveSelection(1)
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.handleLeftClick(msg.X, msg.Y)
	}
	return m, nil
}

func (m Model) handleLeftClick(x, y int) (tea.Model, tea.Cmd) {
	if m.mode == ModeInbox {
		return m.handleInboxLeftClick(x, y)
	}
	if m.mode == ModeConversations {
		return m.handleConvLeftClick(x, y)
	}
	if m.mode == ModeShared {
		return m.handleSharedLeftClick(x, y)
	}
	if m.mode == ModeTabs {
		return m.handleTabsLeftClick(x, y)
	}
	if m.mode == ModeWorktrees {
		return m.handleWorktreesLeftClick(x, y)
	}
	if m.mode == ModeAgents {
		idx := y - agentsListTop()
		if idx < 0 {
			return m, nil
		}
		start := windowStart(m.harnessCursor, m.agentsVisibleCount(), m.listHeight())
		pos := start + idx
		if pos < 0 || pos >= m.agentsVisibleCount() {
			return m, nil
		}
		now := time.Now()
		if m.lastClickRow == pos && now.Sub(m.lastClickAt) < 500*time.Millisecond {
			m.harnessCursor = pos
			m.activateAgentRow()
			if m.quitting {
				return m, tea.Quit
			}
			return m, nil
		}
		m.lastClickRow = pos
		m.lastClickAt = now
		m.harnessCursor = pos
		return m, nil
	}
	// Ignore clicks while overlays are up (except navigate/search).
	switch m.mode {
	case ModeNewSpace, ModeRename, ModeConfirmKill, ModeConfirmRemoveWorktree, ModeHelp, ModeHookDetail, ModeWorktrees:
		return m, nil
	}
	if y == searchLineY() {
		m.mode = ModeSearch
		return m, nil
	}
	layout := m.navigatorLayout(m.width, m.height)
	top := layout.topY
	lh := layout.bodyHeight
	if y < top || y >= top+lh {
		return m, nil
	}
	rows := m.visibleRows()
	if len(rows) == 0 {
		return m, nil
	}
	entries := m.buildNavigatorEntries(rows)
	selEntry := navigatorSelectedEntryIdx(entries, m.selected)
	start := windowStart(selEntry, len(entries), lh)
	entryPos := start + (y - top)
	if entryPos < 0 || entryPos >= len(entries) || entries[entryPos].isSpacer {
		return m, nil
	}
	pos := entries[entryPos].rowIdx
	if pos < 0 || pos >= len(rows) || rows[pos].Kind == RowExitedHeader {
		return m, nil
	}
	now := time.Now()
	if rows[pos].Kind == RowSpace && m.lastClickRow == pos && now.Sub(m.lastClickAt) < 500*time.Millisecond {
		m.selected = pos
		m.activateSelected()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.selected == pos {
		// Second single click on the already-selected row opens it.
		// Double-click also opens via the time check above for spaces;
		// for panes open on repeat click too.
		if m.lastClickRow == pos && now.Sub(m.lastClickAt) < 500*time.Millisecond {
			m.activateSelected()
			if m.quitting {
				return m, tea.Quit
			}
			return m, nil
		}
		m.lastClickRow = pos
		m.lastClickAt = now
		return m, nil
	}
	m.selected = pos
	m.lastClickRow = pos
	m.lastClickAt = now
	return m, nil
}

// ---------------------------------------------------------------------------
// text editing helper
// ---------------------------------------------------------------------------

func editText(value string, cursor int, msg tea.KeyMsg) (string, int) {
	rs := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(rs) {
		cursor = len(rs)
	}
	switch msg.Type {
	case tea.KeyBackspace:
		if cursor <= 0 {
			return value, cursor
		}
		cursor--
		return string(append(rs[:cursor], rs[cursor+1:]...)), cursor
	case tea.KeyDelete:
		if cursor >= len(rs) {
			return value, cursor
		}
		return string(append(rs[:cursor], rs[cursor+1:]...)), cursor
	case tea.KeyLeft:
		if cursor > 0 {
			cursor--
		}
		return value, cursor
	case tea.KeyRight:
		if cursor < len(rs) {
			cursor++
		}
		return value, cursor
	case tea.KeyHome:
		return value, 0
	case tea.KeyEnd:
		return value, len(rs)
	case tea.KeyCtrlU:
		return "", 0
	case tea.KeyCtrlW:
		for cursor > 0 && isSpace(rs[cursor-1]) {
			cursor--
			rs = append(rs[:cursor], rs[cursor+1:]...)
		}
		for cursor > 0 && !isSpace(rs[cursor-1]) {
			cursor--
			rs = append(rs[:cursor], rs[cursor+1:]...)
		}
		return string(rs), cursor
	case tea.KeyCtrlA:
		return value, 0
	case tea.KeyCtrlE:
		return value, len(rs)
	case tea.KeySpace:
		rs = append(rs[:cursor], append([]rune{' '}, rs[cursor:]...)...)
		cursor++
		return string(rs), cursor
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			rs = append(rs[:cursor], append([]rune{r}, rs[cursor:]...)...)
			cursor++
		}
		return string(rs), cursor
	}
	return value, cursor
}

func sanitizeName(s string) string {
	return strings.TrimSpace(s)
}

// Sort spaces for display: current first, then attention priority, then name.
func sortSpaces(spaces []model.Space) {
	sort.SliceStable(spaces, func(i, j int) bool {
		if spaces[i].IsCurrent != spaces[j].IsCurrent {
			return spaces[i].IsCurrent
		}
		if spaces[i].Status.Priority() != spaces[j].Status.Priority() {
			return spaces[i].Status.Priority() < spaces[j].Status.Priority()
		}
		return strings.ToLower(spaces[i].Name) < strings.ToLower(spaces[j].Name)
	})
}

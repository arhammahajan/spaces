package tui

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

// Internal worktree TUI messages.
type (
	wtInspectMsg struct {
		dir string
		ctx model.WorktreeContext
		err string
	}
	wtListMsg struct {
		entries []model.WorktreeEntry
		err     string
	}
	wtSafetyMsg struct {
		path   string
		safety WorktreeRemoveSafety
		err    string
	}
	wtPromptDetailMsg struct {
		prompt string
	}
)

// ---------------------------------------------------------------------------
// async commands
// ---------------------------------------------------------------------------

func (m Model) inspectWorktreesCmd(dir string) tea.Cmd {
	wb, ok := m.backend.(WorktreeBackend)
	if !ok || strings.TrimSpace(dir) == "" {
		return nil
	}
	naming := m.currentNamingInput()
	return func() tea.Msg {
		wtCtx, err := wb.InspectWorktrees(context.Background(), dir, naming)
		if err != nil {
			return wtInspectMsg{dir: dir, err: err.Error()}
		}
		return wtInspectMsg{dir: dir, ctx: wtCtx}
	}
}

func (m Model) loadAllWorktreesCmd() tea.Cmd {
	wb, ok := m.backend.(WorktreeBackend)
	if !ok {
		return nil
	}
	spaces := append([]model.Space(nil), m.spaces...)
	return func() tea.Msg {
		entries, err := wb.ListAllWorktrees(context.Background(), spaces)
		if err != nil {
			return wtListMsg{err: err.Error()}
		}
		return wtListMsg{entries: entries}
	}
}

func (m Model) inspectWorktreeSafetyCmd(wtPath string) tea.Cmd {
	wb, ok := m.backend.(WorktreeBackend)
	if !ok || strings.TrimSpace(wtPath) == "" {
		return nil
	}
	return func() tea.Msg {
		safety, err := wb.InspectWorktreeSafety(context.Background(), wtPath)
		if err != nil {
			return wtSafetyMsg{path: wtPath, err: err.Error()}
		}
		return wtSafetyMsg{path: wtPath, safety: safety}
	}
}

func (m Model) loadActivePromptCmd(session string, paneNum int) tea.Cmd {
	if session == "" || paneNum < 0 {
		return nil
	}
	return func() tea.Msg {
		d, err := m.backend.LoadHookDetail(context.Background(), session, paneNum)
		if err != nil || d == nil {
			return wtPromptDetailMsg{}
		}
		return wtPromptDetailMsg{prompt: strings.TrimSpace(d.Prompt)}
	}
}

// ---------------------------------------------------------------------------
// context & auto-naming helpers
// ---------------------------------------------------------------------------

func (m *Model) captureActiveHarnessContext(sp *model.Space, preferredPane *model.Pane) (session, cwd, app, title string, hookPaneNum int, hasHookDetail bool) {
	hookPaneNum = -1
	if sp == nil {
		return "", m.opts.HomeDir, "", "", -1, false
	}
	session = sp.Name
	cwd = sp.CWD
	// Prefer an explicitly selected pane (e.g. from ModeTabs), then the active pane
	// in the active window, then any active pane, then any agent pane.
	var activePane *model.Pane
	var agentPane *model.Pane
	if preferredPane != nil {
		activePane = preferredPane
	}
	if activePane == nil {
		activeWinID := ""
		for _, w := range sp.Windows {
			if w.Active {
				activeWinID = w.ID
				break
			}
		}
		for i := range sp.Panes {
			p := &sp.Panes[i]
			if p.IsActive && (activeWinID == "" || p.WindowID == activeWinID) {
				activePane = p
				break
			}
		}
	}
	if activePane == nil {
		for i := range sp.Panes {
			if sp.Panes[i].IsActive {
				activePane = &sp.Panes[i]
				break
			}
		}
	}
	if activePane != nil && activePane.CWD != "" {
		cwd = activePane.CWD
	}
	if activePane != nil && activePane.IsAgent() {
		agentPane = activePane
	} else {
		for i := range sp.Panes {
			if sp.Panes[i].IsAgent() {
				agentPane = &sp.Panes[i]
				break
			}
		}
	}
	if agentPane != nil {
		app = agentPane.App
		title = paneConversationName(*agentPane)
		if agentPane.Hook != nil && agentPane.Hook.HasDetail {
			hookPaneNum = agentPane.NumericID
			hasHookDetail = true
		}
	}
	if cwd == "" {
		cwd = m.opts.HomeDir
	}
	return session, cwd, app, title, hookPaneNum, hasHookDetail
}

func (m *Model) currentNamingInput() WorktreeNamingInput {
	targetKind := "space"
	targetName := strings.TrimSpace(m.newName)
	if m.newIsTab {
		targetKind = "tab"
	} else if !m.newNameEdited {
		// Don't feed an auto-prefilled folder name back into branch auto-naming
		// when the user hasn't typed an explicit space name.
		targetName = ""
	}
	return WorktreeNamingInput{
		ExplicitName:  strings.TrimSpace(m.wtBranch),
		TargetName:    targetName,
		TargetKind:    targetKind,
		HarnessApp:    m.wtHarnessApp,
		HarnessTitle:  m.wtHarnessTitle,
		HarnessPrompt: m.wtHarnessPrompt,
		Seed:          m.wtAutoSeed,
	}
}

func (m *Model) refreshWorktreeSuggestion() {
	if !m.wtCtx.InRepo {
		m.wtAutoBranch = ""
		m.wtAutoDirName = ""
		m.wtAutoSource = ""
		return
	}
	wb, ok := m.backend.(WorktreeBackend)
	if !ok {
		return
	}
	dir := m.activeWorktreeDir()
	br, dirName, src, _ := wb.SuggestWorktreeName(context.Background(), dir, m.currentNamingInput())
	m.wtAutoBranch = br
	m.wtAutoDirName = dirName
	m.wtAutoSource = src
}

func (m *Model) activeWorktreeDir() string {
	return strings.TrimSpace(m.newCWD)
}

func (m *Model) wtBaseOptions() []string {
	var opts []string
	seen := map[string]bool{}
	add := func(b string) {
		b = strings.TrimSpace(b)
		if b == "" || seen[b] {
			return
		}
		seen[b] = true
		opts = append(opts, b)
	}
	add(m.wtCtx.DefaultBase)
	add(m.wtCtx.CurrentBranch)
	for _, br := range m.wtCtx.Branches {
		add(br)
	}
	if len(opts) == 0 {
		opts = append(opts, "HEAD")
	}
	return opts
}

func (m *Model) selectedBaseRef() string {
	opts := m.wtBaseOptions()
	if m.wtBaseIdx < 0 || m.wtBaseIdx >= len(opts) {
		return opts[0]
	}
	return opts[m.wtBaseIdx]
}

func (m *Model) selectedExistingWorktree() *model.WorktreeEntry {
	if len(m.wtCtx.Worktrees) == 0 {
		return nil
	}
	idx := m.wtExistingIdx
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.wtCtx.Worktrees) {
		idx = len(m.wtCtx.Worktrees) - 1
	}
	return &m.wtCtx.Worktrees[idx]
}

// ---------------------------------------------------------------------------
// lifecycle entrypoints
// ---------------------------------------------------------------------------

func (m *Model) openNewTabForDir(session, dir string) tea.Cmd {
	m.returnMode = m.mode
	m.newIsTab = true
	m.newTabSession = session
	if m.newTabSession == "" {
		if _, sp := m.tabsSpace(); sp != nil {
			m.newTabSession = sp.Name
		} else if cur := m.currentSpace(); cur != nil {
			m.newTabSession = cur.Name
		}
	}
	m.newName = ""
	m.newNameCur = 0
	m.newNameEdited = false
	if dir == "" {
		if cur := m.currentSpace(); cur != nil && cur.CWD != "" {
			dir = cur.CWD
		} else {
			dir = m.opts.HomeDir
		}
	}
	m.newCWD = dir
	m.newCWDCur = len([]rune(dir))
	m.newField = 0
	m.newError = ""
	m.mode = ModeNewSpace
	cmd := m.initNewSpaceWorktrees()
	m.wtUseNew = true
	return cmd
}

func (m *Model) initNewSpaceWorktrees() tea.Cmd {
	var sp *model.Space
	var preferredPane *model.Pane
	if m.newIsTab {
		_, sp = m.tabsSpace()
		if tab := m.selectedTab(); tab != nil {
			preferredPane = m.selectedTabPane(*tab)
		}
	} else {
		sp = m.currentSpace()
	}
	if sp == nil && len(m.spaces) > 0 {
		sp = &m.spaces[0]
	}
	_, _, app, title, hookPaneNum, hasDetail := m.captureActiveHarnessContext(sp, preferredPane)
	m.wtHarnessApp = app
	m.wtHarnessTitle = title
	m.wtHarnessPrompt = ""
	m.wtUseNew = false
	m.wtExistingIdx = 0
	m.wtBranch = ""
	m.wtBranchCur = 0
	m.wtBaseIdx = 0
	m.wtAutoSeed = 0
	m.wtCtx = model.WorktreeContext{}
	m.wtLoading = false

	var cmds []tea.Cmd
	if _, ok := m.backend.(WorktreeBackend); ok && strings.TrimSpace(m.newCWD) != "" {
		m.wtLoading = true
		cmds = append(cmds, m.inspectWorktreesCmd(strings.TrimSpace(m.newCWD)))
	}
	if hasDetail && title == "" && sp != nil && hookPaneNum >= 0 {
		cmds = append(cmds, m.loadActivePromptCmd(sp.Name, hookPaneNum))
	}
	return tea.Batch(cmds...)
}

func (m *Model) openWorktreesView() tea.Cmd {
	if _, ok := m.backend.(WorktreeBackend); !ok {
		m.toast = "Worktree management is not available."
		return nil
	}
	m.returnMode = m.mode
	m.worktreesReturnMode = m.mode
	m.worktreesStandalone = false
	m.mode = ModeWorktrees
	m.wtSelected = 0
	m.wtQuery = ""
	m.wtQueryCur = 0
	m.wtSearching = false
	m.worktreesLoading = true
	return m.loadAllWorktreesCmd()
}

// ---------------------------------------------------------------------------
// message handlers
// ---------------------------------------------------------------------------

func (m Model) handleWtInspectMsg(msg wtInspectMsg) (tea.Model, tea.Cmd) {
	// Drop stale responses if user changed the folder in the meantime.
	if msg.dir != "" && strings.TrimSpace(msg.dir) != m.activeWorktreeDir() {
		return m, nil
	}
	m.wtLoading = false
	if msg.err != "" {
		m.wtCtx = model.WorktreeContext{}
		return m, nil
	}
	m.wtCtx = msg.ctx
	m.wtAutoBranch = msg.ctx.AutoBranch
	m.wtAutoDirName = msg.ctx.AutoDirName
	m.wtAutoSource = msg.ctx.AutoSource
	m.wtExistingIdx = 0
	for i, wt := range msg.ctx.Worktrees {
		if wt.IsCurrent {
			m.wtExistingIdx = i
			break
		}
	}
	m.wtBaseIdx = 0
	m.refreshWorktreeSuggestion()
	return m, nil
}

func (m Model) handleWtPromptDetailMsg(msg wtPromptDetailMsg) (tea.Model, tea.Cmd) {
	if msg.prompt != "" && m.wtHarnessPrompt == "" {
		m.wtHarnessPrompt = msg.prompt
		if m.mode == ModeNewSpace {
			m.refreshWorktreeSuggestion()
		}
	}
	return m, nil
}

func (m Model) handleWtListMsg(msg wtListMsg) (tea.Model, tea.Cmd) {
	m.worktreesLoading = false
	if msg.err != "" {
		m.toast = msg.err
		return m, nil
	}
	prevPath := ""
	if cur := m.selectedWorktree(); cur != nil {
		prevPath = cur.Path
	}
	m.worktrees = msg.entries
	if prevPath != "" {
		vis := m.filteredWorktrees()
		for pos, idx := range vis {
			if sameCleanPath(m.worktrees[idx].Path, prevPath) {
				m.wtSelected = pos
				return m, nil
			}
		}
	}
	m.clampWorktreeSelection()
	return m, nil
}

func (m Model) handleWtSafetyMsg(msg wtSafetyMsg) (tea.Model, tea.Cmd) {
	if msg.err != "" {
		return m, nil
	}
	if m.mode == ModeConfirmKill && m.confirmWorktreePath == msg.path {
		m.confirmWorktreeSafety = msg.safety
	}
	if m.mode == ModeConfirmRemoveWorktree && m.wtRemoveTarget.Path == msg.path {
		m.wtRemoveSafety = msg.safety
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Worktree form sub-renderers (used by unified New Tab / New Space dialog)
// ---------------------------------------------------------------------------

func (m Model) renderExistingWorktreePicker(innerW int, focused bool, maxShow int) string {
	var b strings.Builder
	mutedSt := lipgloss.NewStyle().Background(lipgloss.Color(ColorBase)).Foreground(lipgloss.Color(ColorOverlay0))
	if len(m.wtCtx.Worktrees) == 0 {
		b.WriteString(mutedSt.Render("  No worktrees found.") + "\n")
		return b.String()
	}
	if maxShow < 1 {
		maxShow = 1
	}
	if len(m.wtCtx.Worktrees) > maxShow && maxShow > 1 {
		// Reserve 1 line for the "(i/n worktrees)" counter.
		maxShow--
	}
	start := windowStart(m.wtExistingIdx, len(m.wtCtx.Worktrees), maxShow)
	end := start + maxShow
	if end > len(m.wtCtx.Worktrees) {
		end = len(m.wtCtx.Worktrees)
	}
	for i := start; i < end; i++ {
		wt := m.wtCtx.Worktrees[i]
		sel := i == m.wtExistingIdx
		bg := ColorBase
		if sel && focused {
			bg = ColorSurface0
		}
		st := func(fg string, bold bool) lipgloss.Style {
			s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
			if bold {
				s = s.Bold(true)
			}
			return s
		}
		prefix := "  "
		if sel {
			if focused {
				prefix = "▌ "
			} else {
				prefix = "▸ "
			}
		}
		icon := "⎇ "
		iconColor := ColorMauve
		if wt.IsMain {
			icon = "● "
			iconColor = ColorBlue
		}
		label := wt.Branch
		if label == "" {
			label = wt.Name
		}
		if label == "" {
			label = filepath.Base(wt.Path)
		}
		var tags []string
		if wt.IsCurrent {
			tags = append(tags, "current")
		}
		if wt.IsMain {
			tags = append(tags, "main")
		}
		if wt.IsDirty {
			tags = append(tags, "*")
		}
		if wt.OpenWindowCount > 0 {
			noun := "tabs"
			if wt.OpenWindowCount == 1 {
				noun = "tab"
			}
			tags = append(tags, fmt.Sprintf("%d %s", wt.OpenWindowCount, noun))
		}
		var line strings.Builder
		line.WriteString(st(ColorLavender, sel).Render(prefix))
		line.WriteString(st(iconColor, sel).Render(icon))
		line.WriteString(st(ColorText, sel).Render(truncate(label, 24)))
		if len(tags) > 0 {
			line.WriteString(st(ColorOverlay0, false).Render("  " + strings.Join(tags, " · ")))
		}
		shortP := shortenPath(wt.Path, m.opts.HomeDir)
		rem := innerW - ansi.StringWidth(line.String()) - 2
		if rem > 10 {
			line.WriteString(st(bg, false).Render("  "))
			line.WriteString(st(ColorOverlay0, false).Render(truncate(shortP, rem)))
		}
		b.WriteString(fitCellWidthWithBG(line.String(), innerW, bg) + "\n")
	}
	if len(m.wtCtx.Worktrees) > maxShow {
		b.WriteString(mutedSt.Render(fmt.Sprintf("  (%d/%d worktrees)", m.wtExistingIdx+1, len(m.wtCtx.Worktrees))) + "\n")
	}
	return b.String()
}

func (m Model) renderNewWorktreeFields(innerW int, branchFocused, baseFocused bool) string {
	bg := ColorBase
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	labelSt := st(ColorSubtext0, false)
	activeLabelSt := st(ColorLavender, true)
	valSt := st(ColorText, false)
	mutedSt := st(ColorOverlay0, false)
	tealSt := st(ColorTeal, false)
	indent := st(bg, false).Render(strings.Repeat(" ", 11))

	brLbl := labelSt.Render(fmt.Sprintf("%-11s", "Branch"))
	brCaret := ""
	if branchFocused {
		brLbl = activeLabelSt.Render(fmt.Sprintf("%-11s", "Branch"))
		brCaret = "_"
	}
	baseLbl := labelSt.Render(fmt.Sprintf("%-11s", "Base ref"))
	if baseFocused {
		baseLbl = activeLabelSt.Render(fmt.Sprintf("%-11s", "Base ref"))
	}

	var b strings.Builder
	if strings.TrimSpace(m.wtBranch) == "" {
		autoName := m.wtAutoBranch
		if autoName == "" {
			autoName = "auto"
		}
		b.WriteString(brLbl + valSt.Render(brCaret) + mutedSt.Render(" auto: ") + tealSt.Render(truncate(autoName, innerW-24)) + "\n")
		if m.wtAutoSource != "" {
			b.WriteString(indent + mutedSt.Render("↳ "+m.wtAutoSource+" · Ctrl-g regenerate") + "\n")
		}
	} else {
		b.WriteString(brLbl + valSt.Render(m.wtBranch+brCaret) + "\n")
		if m.wtAutoBranch != "" && m.wtAutoBranch != strings.TrimSpace(m.wtBranch) {
			b.WriteString(indent + mutedSt.Render("↳ branch: "+m.wtAutoBranch) + "\n")
		}
	}

	opts := m.wtBaseOptions()
	curBase := m.selectedBaseRef()
	baseHint := ""
	if len(opts) > 1 {
		baseHint = mutedSt.Render("  (←→ cycle)")
	}
	b.WriteString(baseLbl + tealSt.Render(curBase) + baseHint + "\n")
	if m.wtCtx.WorktreeRoot != "" && m.wtCtx.RepoName != "" {
		dirName := m.wtAutoDirName
		if dirName == "" {
			dirName = strings.ReplaceAll(m.wtAutoBranch, "/", "-")
		}
		if dirName != "" {
			previewPath := filepath.Join(m.wtCtx.WorktreeRoot, m.wtCtx.RepoName, dirName)
			b.WriteString(labelSt.Render(fmt.Sprintf("%-11s", "Path")) + mutedSt.Render(truncate(shortenPath(previewPath, m.opts.HomeDir), innerW-12)) + "\n")
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Enhanced ModeNewSpace worktree handling
// ---------------------------------------------------------------------------

func (m *Model) newSpaceMaxField() int {
	if _, ok := m.backend.(WorktreeBackend); !ok || !m.wtCtx.InRepo {
		return 1 // classic 0=name, 1=cwd
	}
	if m.wtUseNew {
		return 4 // 0=name, 1=cwd, 2=mode, 3=branch, 4=base
	}
	if len(m.wtCtx.Worktrees) > 0 {
		return 3 // 0=name, 1=cwd, 2=mode, 3=existing list
	}
	return 2
}

func (m *Model) syncAutoNewSpaceName() {
	if m.newIsTab || m.newNameEdited || !m.wtCtx.InRepo {
		return
	}
	var candidate string
	if m.wtUseNew {
		candidate = m.wtAutoDirName
		if candidate == "" {
			candidate = m.wtAutoBranch
		}
	} else if wt := m.selectedExistingWorktree(); wt != nil {
		if wt.IsMain {
			candidate = m.wtCtx.RepoName
		} else if wt.Name != "" {
			candidate = wt.Name
		} else {
			candidate = wt.Branch
		}
	}
	candidate = strings.ReplaceAll(strings.TrimSpace(candidate), "/", "-")
	if candidate == "" {
		return
	}
	name := uniqueName(candidate, m.spaceExists)
	m.newName = name
	m.newNameCur = len([]rune(name))
}

func (m Model) handleNewSpaceWithWorktree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	maxField := m.newSpaceMaxField()
	if m.newField > maxField {
		m.newField = maxField
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.newIsTab = false
		m.mode = m.exitOverlayMode()
		m.newError = ""
		m.clampSelection()
		if m.mode == ModeTabs {
			return m, m.syncTabsInspectorCmd(false)
		}
		return m, nil

	case tea.KeyCtrlT:
		if m.wtCtx.InRepo {
			m.wtUseNew = !m.wtUseNew
			m.newError = ""
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
			if m.newField > m.newSpaceMaxField() {
				m.newField = m.newSpaceMaxField()
			}
		}
		return m, nil

	case tea.KeyCtrlG:
		if m.wtCtx.InRepo {
			m.wtAutoSeed++
			m.wtBranch = ""
			m.wtBranchCur = 0
			m.wtUseNew = true
			m.newError = ""
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
		}
		return m, nil

	case tea.KeyEnter:
		return m.submitNewSpace()

	case tea.KeyTab:
		m.newField = (m.newField + 1) % (maxField + 1)
		m.newError = ""
		return m, nil

	case tea.KeyShiftTab:
		m.newField = (m.newField + maxField) % (maxField + 1)
		m.newError = ""
		return m, nil

	case tea.KeyUp:
		if m.newField == 3 && !m.wtUseNew && m.wtCtx.InRepo && len(m.wtCtx.Worktrees) > 0 {
			if m.wtExistingIdx > 0 {
				m.wtExistingIdx--
				m.syncAutoNewSpaceName()
				return m, nil
			}
			m.newField = 2
			return m, nil
		}
		if maxField == 1 {
			if m.newField == 0 {
				m.newField = 1
			} else {
				m.newField = 0
			}
		} else if m.newField > 0 {
			m.newField--
		}
		m.newError = ""
		return m, nil

	case tea.KeyDown:
		if m.newField == 3 && !m.wtUseNew && m.wtCtx.InRepo && len(m.wtCtx.Worktrees) > 0 {
			if m.wtExistingIdx < len(m.wtCtx.Worktrees)-1 {
				m.wtExistingIdx++
				m.syncAutoNewSpaceName()
			}
			return m, nil
		}
		if maxField == 1 {
			if m.newField == 0 {
				m.newField = 1
			} else {
				m.newField = 0
			}
		} else if m.newField < maxField {
			m.newField++
		}
		m.newError = ""
		return m, nil

	case tea.KeyLeft:
		if m.newField == 2 && m.wtCtx.InRepo {
			m.wtUseNew = false
			m.newError = ""
			m.syncAutoNewSpaceName()
			return m, nil
		}
		if m.newField == 4 && m.wtCtx.InRepo && m.wtUseNew {
			opts := m.wtBaseOptions()
			m.wtBaseIdx = (m.wtBaseIdx + len(opts) - 1) % len(opts)
			return m, nil
		}

	case tea.KeyRight:
		if m.newField == 2 && m.wtCtx.InRepo {
			m.wtUseNew = true
			m.newError = ""
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
			return m, nil
		}
		if m.newField == 4 && m.wtCtx.InRepo && m.wtUseNew {
			opts := m.wtBaseOptions()
			m.wtBaseIdx = (m.wtBaseIdx + 1) % len(opts)
			return m, nil
		}

	case tea.KeySpace:
		if m.newField == 2 && m.wtCtx.InRepo {
			m.wtUseNew = !m.wtUseNew
			m.newError = ""
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
			return m, nil
		}
		if m.newField == 4 && m.wtCtx.InRepo && m.wtUseNew {
			opts := m.wtBaseOptions()
			m.wtBaseIdx = (m.wtBaseIdx + 1) % len(opts)
			return m, nil
		}
	}

	if msg.Alt && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && (msg.Runes[0] == 'w' || msg.Runes[0] == 'W') {
		if m.wtCtx.InRepo {
			m.wtUseNew = !m.wtUseNew
			m.newError = ""
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
		}
		return m, nil
	}

	switch m.newField {
	case 0:
		if isTextEditKey(msg) {
			m.newError = ""
			m.newName, m.newNameCur = editText(m.newName, m.newNameCur, msg)
			m.newNameEdited = strings.TrimSpace(m.newName) != ""
			m.refreshWorktreeSuggestion()
		}
	case 1:
		if isTextEditKey(msg) {
			m.newError = ""
			oldCWD := m.newCWD
			m.newCWD, m.newCWDCur = editText(m.newCWD, m.newCWDCur, msg)
			if m.newCWD != oldCWD {
				if _, ok := m.backend.(WorktreeBackend); ok && strings.TrimSpace(m.newCWD) != "" {
					m.wtLoading = true
					return m, m.inspectWorktreesCmd(strings.TrimSpace(m.newCWD))
				}
			}
		}
	case 3:
		if m.wtCtx.InRepo && m.wtUseNew && isTextEditKey(msg) {
			m.newError = ""
			m.wtBranch, m.wtBranchCur = editText(m.wtBranch, m.wtBranchCur, msg)
			m.refreshWorktreeSuggestion()
			m.syncAutoNewSpaceName()
		} else if m.wtCtx.InRepo && !m.wtUseNew && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'j':
				if m.wtExistingIdx < len(m.wtCtx.Worktrees)-1 {
					m.wtExistingIdx++
					m.syncAutoNewSpaceName()
				}
			case 'k':
				if m.wtExistingIdx > 0 {
					m.wtExistingIdx--
					m.syncAutoNewSpaceName()
				}
			}
		}
	}
	return m, nil
}

func (m Model) submitNewSpace() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.newName)
	cwd := strings.TrimSpace(m.newCWD)

	if m.newIsTab {
		for _, r := range name {
			if r < 32 {
				m.newError = "Tab name cannot contain control characters."
				return m, nil
			}
		}
		session := strings.TrimSpace(m.newTabSession)
		if session == "" {
			if _, sp := m.tabsSpace(); sp != nil {
				session = sp.Name
			} else if cur := m.currentSpace(); cur != nil {
				session = cur.Name
			}
		}
		wb, hasWB := m.backend.(WorktreeBackend)
		if hasWB && m.wtCtx.InRepo {
			req := WorktreeCreateRequest{
				Session:       session,
				TabName:       name, // empty preserves tmux automatic-rename!
				Folder:        cwd,
				UseWorktree:   true,
				CreateNew:     m.wtUseNew,
				BranchName:    strings.TrimSpace(m.wtBranch),
				BaseBranch:    m.selectedBaseRef(),
				AutoSeed:      m.wtAutoSeed,
				HarnessApp:    m.wtHarnessApp,
				HarnessTitle:  m.wtHarnessTitle,
				HarnessPrompt: m.wtHarnessPrompt,
			}
			if !m.wtUseNew {
				if wt := m.selectedExistingWorktree(); wt != nil {
					req.ExistingPath = wt.Path
					// Keep exact pane CWD when staying in the current worktree (e.g. a subdirectory).
					if !wt.IsCurrent || cwd == "" {
						req.Folder = wt.Path
					}
				}
			}
			if err := wb.CreateTab(context.Background(), req); err != nil {
				m.newError = err.Error()
				return m, nil
			}
			m.newIsTab = false
			m.mode = m.exitOverlayMode()
			m.quitting = true
			return m, tea.Quit
		}
		wmb, ok := m.backend.(WindowManageBackend)
		if !ok {
			m.newError = "Creating tabs is not supported."
			return m, nil
		}
		if err := wmb.NewWindow(context.Background(), session, cwd, name); err != nil {
			m.newError = err.Error()
			return m, nil
		}
		m.newIsTab = false
		m.mode = m.exitOverlayMode()
		m.quitting = true
		return m, tea.Quit
	}

	wb, hasWB := m.backend.(WorktreeBackend)
	if hasWB && m.wtCtx.InRepo {
		if name != "" {
			if errStr := validateSpaceName(name); errStr != "" {
				m.newError = errStr
				return m, nil
			}
			name = sanitizeName(name)
			if m.spaceExists(name) {
				m.newError = "A space named '" + name + "' already exists."
				return m, nil
			}
		}
		req := WorktreeCreateRequest{
			SpaceName:     name, // empty = backend derives from worktree/folder & uniquifies
			Folder:        cwd,
			UseWorktree:   true,
			CreateNew:     m.wtUseNew,
			BranchName:    strings.TrimSpace(m.wtBranch),
			BaseBranch:    m.selectedBaseRef(),
			AutoSeed:      m.wtAutoSeed,
			HarnessApp:    m.wtHarnessApp,
			HarnessTitle:  m.wtHarnessTitle,
			HarnessPrompt: m.wtHarnessPrompt,
		}
		if !m.wtUseNew {
			if wt := m.selectedExistingWorktree(); wt != nil {
				req.ExistingPath = wt.Path
				if !wt.IsCurrent || cwd == "" {
					req.Folder = wt.Path
				}
			}
		}
		if err := wb.CreateSpaceWithWorktree(context.Background(), req); err != nil {
			m.newError = err.Error()
			return m, nil
		}
		m.mode = ModeNavigate
		m.quitting = true
		return m, tea.Quit
	}

	// Classic non-worktree fallback.
	if errStr := validateSpaceName(name); errStr != "" {
		m.newError = errStr
		return m, nil
	}
	name = sanitizeName(name)
	if m.spaceExists(name) {
		m.newError = "A space named '" + name + "' already exists."
		return m, nil
	}
	if err := m.backend.CreateSpace(context.Background(), name, cwd); err != nil {
		m.newError = err.Error()
		return m, nil
	}
	m.mode = ModeNavigate
	m.quitting = true
	return m, tea.Quit
}

func (m Model) newSpaceDialogContent(innerW, maxListRows int) (body, hints string) {
	if maxListRows < 2 {
		maxListRows = 2
	}
	bg := ColorBase
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	titleSt := st(ColorLavender, true)
	labelSt := st(ColorSubtext0, false)
	activeLabelSt := st(ColorLavender, true)
	valSt := st(ColorText, false)
	mutedSt := st(ColorOverlay0, false)
	tealSt := st(ColorTeal, false)
	mauveSt := st(ColorMauve, true)
	pillActive := lipgloss.NewStyle().Background(lipgloss.Color(ColorSurface0)).Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	pillInactive := st(ColorOverlay0, false)
	sp := st(bg, false).Render

	lbl := func(field int, text string) string {
		if m.newField == field {
			return activeLabelSt.Render(fmt.Sprintf("%-11s", text))
		}
		return labelSt.Render(fmt.Sprintf("%-11s", text))
	}

	var b strings.Builder
	hdr := "New space"
	if m.newIsTab {
		hdr = "New tab"
		if m.newTabSession != "" {
			hdr += " · " + m.newTabSession
		}
	}
	b.WriteString(titleSt.Render(hdr))
	if m.wtCtx.InRepo && m.wtCtx.RepoName != "" {
		b.WriteString(sp("   ") + mauveSt.Render("⎇ ") + tealSt.Render(m.wtCtx.RepoName))
	}
	b.WriteString("\n\n")

	nameCaret, cwdCaret := "", ""
	if m.newField == 0 {
		nameCaret = "_"
	} else if m.newField == 1 {
		cwdCaret = "_"
	}
	nameLabel := "Name"
	nameEmptyHint := " (optional · auto from worktree)"
	if m.newIsTab {
		nameLabel = "Tab name"
		nameEmptyHint = " (optional · shows active process)"
	}
	if strings.TrimSpace(m.newName) == "" {
		b.WriteString(lbl(0, nameLabel) + valSt.Render(nameCaret) + mutedSt.Render(nameEmptyHint) + "\n")
	} else {
		b.WriteString(lbl(0, nameLabel) + valSt.Render(m.newName+nameCaret) + "\n")
	}
	b.WriteString(lbl(1, "Folder") + valSt.Render(truncate(m.newCWD, innerW-13)+cwdCaret) + "\n")

	if m.wtLoading {
		b.WriteString("\n" + mutedSt.Render("Inspecting git worktrees…") + "\n")
	} else if m.wtCtx.InRepo {
		b.WriteString("\n")
		existingPill := pillInactive.Render(" Existing ")
		newPill := pillInactive.Render(" New worktree ")
		if m.wtUseNew {
			newPill = pillActive.Render("[New worktree]")
		} else {
			existingPill = pillActive.Render("[Existing]")
		}
		b.WriteString(lbl(2, "Worktree") + existingPill + sp("  ") + newPill + mutedSt.Render("   (←→ / Ctrl-t)") + "\n")

		if !m.wtUseNew {
			b.WriteString(m.renderExistingWorktreePicker(innerW, m.newField == 3, maxListRows))
		} else {
			b.WriteString(m.renderNewWorktreeFields(innerW, m.newField == 3, m.newField == 4))
		}
	} else {
		if m.newIsTab {
			b.WriteString("\n" + mutedSt.Render("Not a git repository — opens a standard tab here.") + "\n")
		} else {
			b.WriteString("\n" + mutedSt.Render("The folder is the starting directory.") + "\n")
		}
	}

	if m.newError != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(ColorRed)).Bold(true).Render("! "+truncate(m.newError, innerW-2)) + "\n")
	}
	action := "create"
	if m.newIsTab && !m.wtUseNew {
		action = "open"
	}
	if m.wtCtx.InRepo && m.wtUseNew {
		hints = "Tab field · ←→ mode/base · Ctrl-g new auto-name · Enter create · Esc cancel"
	} else if m.wtCtx.InRepo {
		hints = "Tab field · ←→ mode · ↑↓ pick worktree · Ctrl-t toggle · Enter " + action + " · Esc cancel"
	} else {
		hints = "Tab next · Enter " + action + " · Esc cancel"
	}
	return b.String(), hints
}

func (m Model) newSpaceDialogWithWorktree(width int) string {
	if _, ok := m.backend.(WorktreeBackend); !ok || (!m.wtCtx.InRepo && !m.wtLoading) {
		// Keep compact classic dialog when outside git repos or without WorktreeBackend.
		return m.classicNewSpaceDialog(width)
	}
	boxW := width - 6
	if boxW > 78 {
		boxW = 78
	}
	if boxW < 44 {
		boxW = 44
	}
	innerW := boxW - 4
	body, hints := m.newSpaceDialogContent(innerW, 6)
	return styleDialogBox.Width(boxW).Render(body + "\n" + styleMuted.Render(hints))
}

// ---------------------------------------------------------------------------
// ModeWorktrees (Worktrees manager & cleanup view)
// ---------------------------------------------------------------------------

type wtRowEntry struct {
	isHeader bool
	isSpacer bool
	repoName string
	repoRoot string
	count    int
	itemIdx  int
}

type wtRepoGroup struct {
	key      string
	repoName string
	repoRoot string
	indices  []int // indices into m.worktrees in display order
}

func (m *Model) groupedFilteredWorktrees() []wtRepoGroup {
	q := strings.ToLower(strings.TrimSpace(m.wtQuery))
	var groups []*wtRepoGroup
	byKey := map[string]*wtRepoGroup{}
	for i, wt := range m.worktrees {
		if q != "" &&
			!strings.Contains(strings.ToLower(wt.RepoName), q) &&
			!strings.Contains(strings.ToLower(wt.Name), q) &&
			!strings.Contains(strings.ToLower(wt.Branch), q) &&
			!strings.Contains(strings.ToLower(wt.Path), q) &&
			!strings.Contains(strings.ToLower(strings.Join(wt.OpenSpaces, " ")), q) &&
			!(q == "managed" && wt.Managed) &&
			!(q == "detached" && wt.OpenWindowCount == 0 && !wt.IsMain) &&
			!(q == "dirty" && wt.IsDirty) {
			continue
		}
		k := wt.RepoKey
		if k == "" {
			k = wt.MainRepoRoot
		}
		name := wt.RepoName
		if name == "" {
			name = path.Base(wt.MainRepoRoot)
		}
		g := byKey[k]
		if g == nil {
			g = &wtRepoGroup{key: k, repoName: name, repoRoot: wt.MainRepoRoot}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.indices = append(g.indices, i)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		ni := strings.ToLower(groups[i].repoName)
		nj := strings.ToLower(groups[j].repoName)
		if ni != nj {
			return ni < nj
		}
		return groups[i].key < groups[j].key
	})
	out := make([]wtRepoGroup, 0, len(groups))
	for _, g := range groups {
		sort.SliceStable(g.indices, func(i, j int) bool {
			wi := m.worktrees[g.indices[i]]
			wj := m.worktrees[g.indices[j]]
			if wi.IsMain != wj.IsMain {
				return wi.IsMain
			}
			return false
		})
		out = append(out, *g)
	}
	return out
}

func (m *Model) filteredWorktrees() []int {
	groups := m.groupedFilteredWorktrees()
	var out []int
	for _, g := range groups {
		out = append(out, g.indices...)
	}
	return out
}

func (m *Model) clampWorktreeSelection() {
	n := len(m.filteredWorktrees())
	if n == 0 {
		m.wtSelected = 0
		return
	}
	if m.wtSelected < 0 {
		m.wtSelected = 0
	}
	if m.wtSelected >= n {
		m.wtSelected = n - 1
	}
}

func (m *Model) moveWorktreeSelection(delta int) {
	m.wtSelected += delta
	m.clampWorktreeSelection()
}

func (m *Model) jumpWorktreeRepo(dir int) {
	groups := m.groupedFilteredWorktrees()
	if len(groups) == 0 {
		m.wtSelected = 0
		return
	}
	starts := make([]int, len(groups))
	pos := 0
	for i, g := range groups {
		starts[i] = pos
		pos += len(g.indices)
	}
	cur := 0
	for i, s := range starts {
		if s <= m.wtSelected {
			cur = i
		}
	}
	next := cur + dir
	if next < 0 {
		next = 0
	}
	if next >= len(starts) {
		next = len(starts) - 1
	}
	m.wtSelected = starts[next]
}

func (m *Model) selectedWorktree() *model.WorktreeEntry {
	vis := m.filteredWorktrees()
	if m.wtSelected < 0 || m.wtSelected >= len(vis) {
		return nil
	}
	return &m.worktrees[vis[m.wtSelected]]
}

func (m Model) exitWorktreesMode() Mode {
	if m.worktreesReturnMode == ModeInbox || m.worktreesReturnMode == ModeTabs {
		return m.worktreesReturnMode
	}
	return ModeNavigate
}

func (m Model) handleWorktrees(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.wtSearching {
		return m.handleWorktreesSearch(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		if strings.TrimSpace(m.wtQuery) != "" {
			m.wtQuery = ""
			m.wtQueryCur = 0
			m.wtSelected = 0
			m.clampWorktreeSelection()
			return m, nil
		}
		if m.worktreesStandalone {
			m.quitting = true
			return m, tea.Quit
		}
		m.mode = m.exitWorktreesMode()
		return m, nil
	case tea.KeyEnter:
		return m.activateSelectedWorktree()
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveWorktreeSelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveWorktreeSelection(1)
		return m, nil
	case tea.KeyLeft:
		m.jumpWorktreeRepo(-1)
		return m, nil
	case tea.KeyRight:
		m.jumpWorktreeRepo(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveWorktreeSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveWorktreeSelection(10)
		return m, nil
	case tea.KeyHome:
		m.wtSelected = 0
		m.clampWorktreeSelection()
		return m, nil
	case tea.KeyEnd:
		m.wtSelected = len(m.filteredWorktrees()) - 1
		m.clampWorktreeSelection()
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch string(msg.Runes) {
		case "k":
			m.moveWorktreeSelection(-1)
		case "j":
			m.moveWorktreeSelection(1)
		case "h":
			m.jumpWorktreeRepo(-1)
		case "l":
			m.jumpWorktreeRepo(1)
		case "g":
			m.wtSelected = 0
			m.clampWorktreeSelection()
		case "G":
			m.wtSelected = len(m.filteredWorktrees()) - 1
			m.clampWorktreeSelection()
		case "/":
			m.wtSearching = true
		case "q":
			if strings.TrimSpace(m.wtQuery) != "" {
				m.wtQuery = ""
				m.wtQueryCur = 0
				m.wtSelected = 0
				m.clampWorktreeSelection()
				return m, nil
			}
			if m.worktreesStandalone {
				m.quitting = true
				return m, tea.Quit
			}
			m.mode = m.exitWorktreesMode()
		case "n":
			dir := ""
			if wt := m.selectedWorktree(); wt != nil {
				dir = wt.Path
			}
			curSession := ""
			if cur := m.currentSpace(); cur != nil {
				curSession = cur.Name
			}
			cmd := m.openNewTabForDir(curSession, dir)
			m.wtUseNew = true
			return m, cmd
		case "x", "d":
			return m.requestRemoveSelectedWorktree()
		case "p":
			wb, ok := m.backend.(WorktreeBackend)
			if !ok {
				return m, nil
			}
			summary, err := wb.PruneWorktrees(context.Background(), m.spaces, true)
			if err != nil {
				m.toast = err.Error()
				return m, nil
			}
			m.toast = summary
			m.worktreesLoading = true
			return m, m.loadAllWorktreesCmd()
		}
	}
	return m, nil
}

func (m Model) handleWorktreesSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEsc:
		m.wtSearching = false
		return m, nil
	case tea.KeyEnter:
		m.wtSearching = false
		return m.activateSelectedWorktree()
	case tea.KeyUp, tea.KeyCtrlP:
		m.moveWorktreeSelection(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.moveWorktreeSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.moveWorktreeSelection(-10)
		return m, nil
	case tea.KeyPgDown:
		m.moveWorktreeSelection(10)
		return m, nil
	default:
		if isTextEditKey(msg) {
			m.wtQuery, m.wtQueryCur = editText(m.wtQuery, m.wtQueryCur, msg)
			m.wtSelected = 0
			m.clampWorktreeSelection()
		}
	}
	return m, nil
}

func (m Model) handleWorktreesLeftClick(x, y int) (tea.Model, tea.Cmd) {
	_ = x
	if y == searchLineY() {
		m.wtSearching = true
		return m, nil
	}
	groups := m.groupedFilteredWorktrees()
	entries := buildWorktreeListEntriesFromGroups(groups)
	if len(entries) == 0 {
		return m, nil
	}
	layout := m.navigatorLayout(m.width, m.height)
	if y < layout.topY || y >= layout.topY+layout.bodyHeight {
		return m, nil
	}
	selEntry := worktreeSelectedEntryIdx(entries, m.wtSelected)
	start := windowStart(selEntry, len(entries), layout.bodyHeight)
	pos := start + (y - layout.topY)
	if pos < 0 || pos >= len(entries) || entries[pos].isHeader || entries[pos].isSpacer {
		return m, nil
	}
	idx := entries[pos].itemIdx
	m.wtSearching = false
	now := time.Now()
	if m.wtSelected == idx && m.lastClickRow == idx && now.Sub(m.lastClickAt) < 500*time.Millisecond {
		return m.activateSelectedWorktree()
	}
	m.wtSelected = idx
	m.lastClickRow = idx
	m.lastClickAt = now
	return m, nil
}

func (m Model) paneBelongsToWorktree(paneCWD, wtPath string) bool {
	if !sameCleanPath(paneCWD, wtPath) && !isSubPath(paneCWD, wtPath) {
		return false
	}
	wtClean := filepath.Clean(strings.TrimSpace(wtPath))
	for _, other := range m.worktrees {
		otherClean := filepath.Clean(strings.TrimSpace(other.Path))
		if otherClean == "" || otherClean == wtClean {
			continue
		}
		if len(otherClean) > len(wtClean) && isSubPath(otherClean, wtClean) && (sameCleanPath(paneCWD, otherClean) || isSubPath(paneCWD, otherClean)) {
			return false
		}
	}
	return true
}

func (m Model) activateSelectedWorktree() (tea.Model, tea.Cmd) {
	wt := m.selectedWorktree()
	if wt == nil {
		return m, nil
	}
	// If a live pane/window is already open in this worktree, focus it directly!
	for _, sp := range m.spaces {
		for _, p := range sp.Panes {
			if m.paneBelongsToWorktree(p.CWD, wt.Path) {
				if err := m.backend.FocusPane(context.Background(), sp.Name, p.WindowID, p.ID); err != nil {
					m.toast = err.Error()
					return m, nil
				}
				m.quitting = true
				return m, tea.Quit
			}
		}
	}
	// Otherwise open a new tab in the current space backed by this worktree!
	wb, ok := m.backend.(WorktreeBackend)
	if !ok {
		m.toast = "Cannot open worktree tab."
		return m, nil
	}
	session := ""
	if cur := m.currentSpace(); cur != nil {
		session = cur.Name
	} else if len(m.spaces) > 0 {
		session = m.spaces[0].Name
	}
	err := wb.CreateTab(context.Background(), WorktreeCreateRequest{
		Session:      session,
		TabName:      "", // keep tmux automatic-rename
		Folder:       wt.Path,
		UseWorktree:  true,
		CreateNew:    false,
		ExistingPath: wt.Path,
	})
	if err != nil {
		m.toast = err.Error()
		return m, nil
	}
	m.quitting = true
	return m, tea.Quit
}

func (m Model) requestRemoveSelectedWorktree() (tea.Model, tea.Cmd) {
	wt := m.selectedWorktree()
	if wt == nil {
		return m, nil
	}
	if wt.IsMain {
		m.toast = "Cannot remove the main repository worktree."
		return m, nil
	}
	m.wtRemoveTarget = *wt
	m.wtRemoveSafety = WorktreeRemoveSafety{
		CanRemoveSafely: !wt.IsDirty && wt.UnmergedCommits == 0,
		IsDirty:         wt.IsDirty,
		UnmergedCommits: wt.UnmergedCommits,
		Branch:          wt.Branch,
		BaseBranch:      wt.BaseBranch,
	}
	m.wtRemoveForce = false
	m.wtRemoveDeleteBranch = wt.Managed && !wt.IsDirty && wt.UnmergedCommits == 0 && wt.Branch != "" && wt.Branch != wt.BaseBranch
	m.mode = ModeConfirmRemoveWorktree
	return m, m.inspectWorktreeSafetyCmd(wt.Path)
}

func (m Model) handleConfirmRemoveWorktree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = ModeWorktrees
		return m, nil
	case tea.KeyEnter:
		return m.executeRemoveWorktree()
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch string(msg.Runes) {
		case "y", "Y":
			return m.executeRemoveWorktree()
		case "n", "N":
			m.mode = ModeWorktrees
			return m, nil
		case "f", "F":
			m.wtRemoveForce = !m.wtRemoveForce
			return m, nil
		case "b", "B":
			if m.wtRemoveTarget.Branch != "" && m.wtRemoveTarget.Branch != m.wtRemoveTarget.BaseBranch {
				m.wtRemoveDeleteBranch = !m.wtRemoveDeleteBranch
			}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) executeRemoveWorktree() (tea.Model, tea.Cmd) {
	wb, ok := m.backend.(WorktreeBackend)
	if !ok {
		m.mode = ModeWorktrees
		return m, nil
	}
	if m.wtRemoveSafety.IsDirty && !m.wtRemoveForce {
		m.toast = "Worktree has uncommitted changes. Press f to toggle force remove."
		return m, nil
	}
	if err := wb.RemoveWorktree(context.Background(), m.wtRemoveTarget.Path, m.wtRemoveForce, m.wtRemoveDeleteBranch, m.wtRemoveTarget.OpenWindowIDs); err != nil {
		m.toast = err.Error()
		m.mode = ModeWorktrees
		return m, nil
	}
	m.toast = fmt.Sprintf("Removed worktree %s", m.wtRemoveTarget.Name)
	m.mode = ModeWorktrees
	m.worktreesLoading = true
	return m, tea.Batch(m.loadAllWorktreesCmd(), m.loadSpacesCmd())
}

func (m Model) renderConfirmRemoveWorktree(width int) string {
	boxW := width - 8
	if boxW > 72 {
		boxW = 72
	}
	if boxW < 44 {
		boxW = 44
	}
	wt := m.wtRemoveTarget
	var b strings.Builder
	b.WriteString(styleError.Render("Remove git worktree?") + "\n\n")
	b.WriteString(fmt.Sprintf("Worktree: %s (%s)\n", wt.Name, wt.Branch))
	b.WriteString(styleMuted.Render("Path: "+shortenPath(wt.Path, m.opts.HomeDir)) + "\n")
	if wt.OpenWindowCount > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(
			fmt.Sprintf("⚠ %d open tmux tab(s) in %s will also be closed.\n", wt.OpenWindowCount, strings.Join(wt.OpenSpaces, ", ")),
		))
	}
	for _, w := range m.wtRemoveSafety.Warnings {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render("⚠ "+w) + "\n")
	}
	if len(m.wtRemoveSafety.Warnings) == 0 {
		if m.wtRemoveSafety.IsDirty {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render("⚠ Uncommitted changes in working tree") + "\n")
		}
		if m.wtRemoveSafety.UnmergedCommits > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Render(
				fmt.Sprintf("⚠ %d commit(s) not merged into %s", m.wtRemoveSafety.UnmergedCommits, m.wtRemoveSafety.BaseBranch),
			) + "\n")
		}
	}
	b.WriteString("\n")
	forceBox := "[ ]"
	if m.wtRemoveForce {
		forceBox = "[x]"
	}
	branchBox := "[ ]"
	if m.wtRemoveDeleteBranch {
		branchBox = "[x]"
	}
	if m.wtRemoveSafety.IsDirty || m.wtRemoveForce {
		b.WriteString(fmt.Sprintf("  %s Force remove dirty worktree (f)\n", forceBox))
	}
	if wt.Branch != "" && wt.Branch != wt.BaseBranch {
		b.WriteString(fmt.Sprintf("  %s Also delete local branch %q (b)\n", branchBox, wt.Branch))
	}
	b.WriteString("\n" + styleMuted.Render("y/Enter remove · f force · b branch · n/Esc cancel"))
	return styleDialogBox.Width(boxW).Render(b.String())
}

func buildWorktreeListEntriesFromGroups(groups []wtRepoGroup) []wtRowEntry {
	if len(groups) == 0 {
		return nil
	}
	var entries []wtRowEntry
	itemPos := 0
	for gi, g := range groups {
		if gi > 0 {
			entries = append(entries, wtRowEntry{isSpacer: true})
		}
		entries = append(entries, wtRowEntry{
			isHeader: true,
			repoName: g.repoName,
			repoRoot: g.repoRoot,
			count:    len(g.indices),
		})
		for range g.indices {
			entries = append(entries, wtRowEntry{itemIdx: itemPos})
			itemPos++
		}
	}
	return entries
}

func worktreeSelectedEntryIdx(entries []wtRowEntry, selected int) int {
	for i, e := range entries {
		if !e.isHeader && !e.isSpacer && e.itemIdx == selected {
			return i
		}
	}
	return 0
}

func (m Model) renderWorktrees(width, height int) string {
	layout := m.navigatorLayout(width, height)
	groups := m.groupedFilteredWorktrees()
	var vis []int
	for _, g := range groups {
		vis = append(vis, g.indices...)
	}

	bg := ColorMantle
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	managed, detached := 0, 0
	for _, wt := range m.worktrees {
		if wt.Managed {
			managed++
		}
		if !wt.IsMain && wt.OpenWindowCount == 0 {
			detached++
		}
	}
	segs := []string{st(ColorSubtext0, false).Render(fmt.Sprintf("%d worktrees", len(m.worktrees)))}
	if managed > 0 {
		segs = append(segs, st(ColorMauve, false).Render(fmt.Sprintf("%d managed", managed)))
	}
	if detached > 0 {
		segs = append(segs, st(ColorYellow, false).Render(fmt.Sprintf("%d detached", detached)))
	}

	var b strings.Builder
	b.WriteString(renderHeaderBar("Worktrees", segs, model.FilterAll, width) + "\n")
	matchText := ""
	if strings.TrimSpace(m.wtQuery) != "" {
		matchText = fmt.Sprintf("%d matches", len(vis))
	}
	b.WriteString(renderSearchBar(m.wtQuery, m.wtSearching, "Filter worktrees by repo, branch, name, path, managed, detached…", matchText, width) + "\n")
	if layout.showDividers {
		b.WriteString(renderDividerLine(width) + "\n")
	}

	lh := layout.bodyHeight
	if m.worktreesLoading && len(m.worktrees) == 0 {
		b.WriteString(fitCellWidth(styleMuted.Render("  Loading git worktrees…"), width) + "\n")
		for i := 1; i < lh; i++ {
			b.WriteString(fitCellWidth("", width) + "\n")
		}
	} else if len(vis) == 0 {
		msg := "No git worktrees found across active spaces."
		if strings.TrimSpace(m.wtQuery) != "" {
			msg = "No worktrees match this filter."
		}
		b.WriteString(fitCellWidth(styleMuted.Render("  "+truncate(msg, width-2)), width) + "\n")
		for i := 1; i < lh; i++ {
			b.WriteString(fitCellWidth("", width) + "\n")
		}
	} else {
		entries := buildWorktreeListEntriesFromGroups(groups)
		selEntry := worktreeSelectedEntryIdx(entries, m.wtSelected)
		start := windowStart(selEntry, len(entries), lh)
		for i := 0; i < lh; i++ {
			idx := start + i
			if idx >= len(entries) {
				b.WriteString(fitCellWidth("", width) + "\n")
				continue
			}
			e := entries[idx]
			if e.isSpacer {
				b.WriteString(fitCellWidth("", width) + "\n")
				continue
			}
			if e.isHeader {
				hdr := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true).Render(
					fmt.Sprintf("  %s · %d", e.repoName, e.count),
				)
				b.WriteString(renderSectionRule(hdr, width) + "\n")
				continue
			}
			wt := m.worktrees[vis[e.itemIdx]]
			b.WriteString(m.renderWorktreeRow(wt, e.itemIdx == m.wtSelected, width) + "\n")
		}
	}

	if layout.detailLines > 0 {
		if layout.showDividers {
			b.WriteString(renderDividerLine(width) + "\n")
		}
		for _, dl := range m.renderWorktreeDetailLines(width, layout.detailLines) {
			b.WriteString(dl + "\n")
		}
	}
	if m.toast != "" {
		b.WriteString(fitCellWidth(styleError.Render(" ! "+truncate(m.toast, width-3)), width) + "\n")
	}
	b.WriteString(renderHintsBar([][2]string{
		{"↑↓", "move"},
		{"Enter", "open/focus"},
		{"n", "new"},
		{"x", "remove"},
		{"p", "prune"},
		{"/", "filter"},
		{"Ctrl-r", "refresh"},
		{"Esc", "back"},
	}, width))
	return b.String()
}

func (m Model) renderWorktreeRow(wt model.WorktreeEntry, selected bool, width int) string {
	bg := ColorBase
	if selected {
		bg = ColorSurface0
	}
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg))
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

	var rightParts []string
	if wt.Git != nil {
		if diff := renderGitDiffBadge(wt.Git, bg, st); diff != "" {
			rightParts = append(rightParts, diff)
		}
		if pr := renderGitPRBadge(wt.Git, bg, st); pr != "" {
			rightParts = append(rightParts, pr)
		}
	} else if wt.IsDirty {
		rightParts = append(rightParts, st(ColorYellow, true).Render("*"))
	}
	if wt.OpenWindowCount > 0 {
		noun := "tabs"
		if wt.OpenWindowCount == 1 {
			noun = "tab"
		}
		loc := fmt.Sprintf("%d %s", wt.OpenWindowCount, noun)
		if len(wt.OpenSpaces) > 0 {
			loc += " · " + truncate(strings.Join(wt.OpenSpaces, ","), 18)
		}
		rightParts = append(rightParts, st(ColorGreen, false).Render(loc))
	} else if !wt.IsMain {
		rightParts = append(rightParts, st(ColorOverlay0, false).Render("detached"))
	}
	if wt.Managed {
		rightParts = append(rightParts, st(ColorMauve, false).Render("managed"))
	}
	rightStyled := strings.Join(rightParts, st(bg, false).Render("  "))
	rightW := ansi.StringWidth(rightStyled)

	icon := "⎇ "
	iconColor := ColorMauve
	if wt.IsMain {
		icon = "● "
		iconColor = ColorBlue
	} else if wt.IsPrunable {
		icon = "! "
		iconColor = ColorRed
	}
	branch := wt.Branch
	if branch == "" {
		if wt.IsDetached {
			branch = "(detached)"
		} else {
			branch = wt.Name
		}
	}
	indent := "  "
	if !wt.IsMain {
		indent = "    "
	}
	var lb strings.Builder
	lb.WriteString(st(bg, false).Render(indent))
	lb.WriteString(st(iconColor, true).Render(icon))
	lb.WriteString(st(ColorTeal, selected).Render(truncate(branch, 28)))
	if wt.IsMain {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorBlue, false).Render("main"))
	} else if wt.Name != "" && wt.Name != branch && wt.Name != strings.ReplaceAll(branch, "/", "-") {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorSubtext0, false).Render("[" + truncate(wt.Name, 18) + "]"))
	}
	if wt.IsCurrent {
		lb.WriteString(st(bg, false).Render("  "))
		lb.WriteString(st(ColorLavender, false).Render("current"))
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

func (m Model) renderWorktreeDetailLines(width, maxLines int) []string {
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
	wt := m.selectedWorktree()
	if wt == nil {
		return []string{padLine(st(ColorOverlay0, false).Render("Press n to create a new worktree or p to prune.")), padLine("")}[:maxLines]
	}

	var l1 []string
	l1 = append(l1, st(ColorText, true).Render(wt.RepoName))
	if wt.Branch != "" {
		l1 = append(l1, st(ColorTeal, false).Render("⎇ "+wt.Branch))
	}
	if wt.BaseBranch != "" && wt.BaseBranch != wt.Branch {
		l1 = append(l1, st(ColorSubtext0, false).Render("base "+wt.BaseBranch))
	}
	if wt.UnmergedCommits > 0 {
		l1 = append(l1, st(ColorYellow, false).Render(fmt.Sprintf("%d unmerged", wt.UnmergedCommits)))
	}
	l1 = append(l1, st(ColorOverlay0, false).Render(shortenPath(wt.Path, m.opts.HomeDir)))

	var l2 []string
	if wt.Git != nil {
		if gs := renderGitDetailStyled(wt.Git, bg, st); gs != "" {
			l2 = append(l2, gs)
		}
	}
	if wt.NameSource != "" {
		l2 = append(l2, st(ColorOverlay0, false).Render("named: "+wt.NameSource))
	}
	if wt.OpenWindowCount > 0 {
		l2 = append(l2, st(ColorGreen, false).Render("Enter focuses open tab"))
	} else {
		l2 = append(l2, st(ColorLavender, false).Render("Enter opens new tab here"))
	}
	if maxLines <= 1 {
		return []string{padLine(strings.Join(append(l1, l2...), sep))}
	}
	return []string{padLine(strings.Join(l1, sep)), padLine(strings.Join(l2, sep))}
}

// ---------------------------------------------------------------------------
// small path / key helpers
// ---------------------------------------------------------------------------

func isTextEditKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete, tea.KeyLeft, tea.KeyRight,
		tea.KeyHome, tea.KeyEnd, tea.KeyCtrlU, tea.KeyCtrlW, tea.KeyCtrlA,
		tea.KeyCtrlE, tea.KeyRunes, tea.KeySpace:
		return true
	}
	return false
}

func sameCleanPath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	return a != "" && a != "." && a == b
}

func isSubPath(child, parent string) bool {
	child = filepath.Clean(strings.TrimSpace(child))
	parent = filepath.Clean(strings.TrimSpace(parent))
	if child == "" || parent == "" || parent == "." || parent == "/" {
		return false
	}
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// spaceLinkedWorktreeCount counts distinct linked git worktrees among a space's panes
// that differ from the space's own primary GitInfo root.
func spaceLinkedWorktreeCount(sp model.Space) int {
	seen := map[string]bool{}
	spaceRoot := ""
	if sp.Git != nil {
		spaceRoot = sp.Git.RepoRoot
		if sp.Git.IsWorktree && sp.Git.RepoRoot != "" {
			seen[sp.Git.RepoRoot] = true
		}
	}
	for _, p := range sp.Panes {
		if p.Git != nil && p.Git.IsWorktree && p.Git.RepoRoot != "" {
			seen[p.Git.RepoRoot] = true
		}
	}
	// If only the space's own worktree is present, the branch badge on the space row already shows it.
	if len(seen) == 1 && spaceRoot != "" && seen[spaceRoot] {
		return 0
	}
	return len(seen)
}

package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

// ---------------------------------------------------------------------------
// right-hand rich tab inspector + full agent/process telemetry
// ---------------------------------------------------------------------------

type inspectorKV struct {
	label string
	value string
	color string
	bold  bool
}

func (m Model) renderTabInspectorEmpty(width, height int) []string {
	lines := make([]string, height)
	if height > 1 {
		lines[0] = fitCellWidth("", width)
		lines[1] = fitCellWidth(styleMuted.Render("  Select a tab to inspect its panes, git status, and full telemetry."), width)
		for i := 2; i < height; i++ {
			lines[i] = fitCellWidth("", width)
		}
		return lines
	}
	if height == 1 {
		lines[0] = fitCellWidth(styleMuted.Render("  Select a tab to inspect."), width)
	}
	return lines
}

func (m Model) renderTabInspectorWithHits(sp model.Space, tab TabItem, width, height int) ([]string, []tabPaneHitRow) {
	if height <= 0 {
		return nil, nil
	}
	stBase := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	stMantle := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Background(lipgloss.Color(ColorMantle)).Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	selPaneIdx := m.selectedTabPaneIdx(tab)
	var selPane *model.Pane
	if selPaneIdx >= 0 && selPaneIdx < len(tab.Panes) {
		p := tab.Panes[selPaneIdx]
		selPane = &p
	}

	var body []string
	var rawHits []tabPaneHitRow
	addBlank := func() {
		if len(body) > 0 {
			body = append(body, fitCellWidth("", width))
		}
	}

	// 1. Hero Header: primary title + right-aligned status pill + clean subtitle.
	body = append(body, fitCellWidth("", width))
	body = append(body, m.renderInspectorHero(sp, tab, selPane, width)...)

	// 2. Multi-pane Pill Switcher (only shown when tab has 2+ panes).
	if len(tab.Panes) > 1 {
		body = append(body, fitCellWidth("", width))
		pillLines, pillHits := m.renderPanePillStrip(tab.Panes, selPaneIdx, width, len(body))
		body = append(body, pillLines...)
		rawHits = append(rawHits, pillHits...)
	}

	// Subtle inset hairline separating hero header from telemetry zones.
	body = append(body, renderInsetHairline(width))
	body = append(body, fitCellWidth("", width))

	// 3. Telemetry Body: Agent Telemetry OR Process Telemetry.
	var sessTelemetry *model.SessionRow
	if selPane != nil {
		if selPane.IsAgent() {
			body = append(body, m.renderAgentTelemetrySections(sp, *selPane, width)...)
		} else {
			body = append(body, m.renderProcessCommandSection(*selPane, width)...)
		}
		if sess := m.telemetry.SessionForPane(selPane.ID); sess != nil {
			sessTelemetry = sess
			addBlank()
			body = append(body, m.renderInspectorContextTokensSection(*sess, width)...)
		}
	}

	// 4. Git, Worktree & Pull Request Card.
	var activeGit *model.GitInfo
	if selPane != nil && selPane.Git != nil {
		activeGit = selPane.Git
	} else {
		activeGit = tab.Git
	}
	if activeGit != nil || strings.TrimSpace(tab.Window.WorktreePath) != "" || strings.TrimSpace(tab.Window.WorktreeName) != "" {
		addBlank()
		relCWD := tab.CWD
		if selPane != nil && selPane.CWD != "" {
			relCWD = selPane.CWD
		}
		body = append(body, m.renderInspectorGitSection(activeGit, tab.Window, relCWD, width)...)
	}

	// 5. Compact 2-column Details Grid at the bottom (all technical/runtime metadata).
	if selPane != nil {
		details := m.renderInspectorDetailsSection(sp, tab, *selPane, sessTelemetry, width)
		if len(details) > 0 {
			addBlank()
			body = append(body, details...)
		}
	}

	// Scroll viewport below the pinned 1-line top bar.
	viewH := height - 1
	if viewH < 0 {
		viewH = 0
	}
	scroll := m.tabsInspectorScroll
	maxScroll := len(body) - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}

	// Pinned Top Bar (Mantle): tab index/name on left, compact flags + scroll indicator on right.
	bannerLeft := stMantle(ColorLavender, true).Render(fmt.Sprintf("  %d:", tab.Window.Index)) +
		stMantle(ColorText, true).Render(" "+tab.Window.Name)

	mantleDot := stMantle(ColorSurface1, false).Render(" · ")
	var metaRight []string
	if tab.Window.Active {
		metaRight = append(metaRight, stMantle(ColorGreen, true).Render("active"))
	} else if tab.Window.Last {
		metaRight = append(metaRight, stMantle(ColorSubtext0, false).Render("last"))
	}
	if tab.Window.Zoomed {
		metaRight = append(metaRight, stMantle(ColorMauve, true).Render("zoomed"))
	}
	if wtLbl := inspectorWorktreeLabel(activeGit, tab.Window); wtLbl != "" {
		metaRight = append(metaRight, stMantle(ColorMauve, true).Render("⎇ ")+stMantle(ColorTeal, false).Render(truncate(wtLbl, 18)))
	}
	if len(tab.Panes) > 1 {
		metaRight = append(metaRight, stMantle(ColorSubtext0, false).Render(fmt.Sprintf("%d panes", len(tab.Panes))))
	}
	if tab.Window.ID != "" {
		metaRight = append(metaRight, stMantle(ColorOverlay0, false).Render(tab.Window.ID))
	}
	if tab.Window.Width > 0 && tab.Window.Height > 0 {
		metaRight = append(metaRight, stMantle(ColorOverlay0, false).Render(fmt.Sprintf("%d×%d", tab.Window.Width, tab.Window.Height)))
	}
	if !tab.Window.ActivityAt.IsZero() {
		if d := m.opts.NowOrDefault().Sub(tab.Window.ActivityAt).Milliseconds(); d > 0 {
			metaRight = append(metaRight, stMantle(ColorOverlay0, false).Render(formatAge(d)+" ago"))
		}
	}
	if maxScroll > 0 && viewH > 0 {
		endLine := scroll + viewH
		if endLine > len(body) {
			endLine = len(body)
		}
		metaRight = append(metaRight, stMantle(ColorLavender, false).Render(fmt.Sprintf("↕ %d-%d/%d", scroll+1, endLine, len(body))))
	}
	bannerRight := ""
	if len(metaRight) > 0 {
		bannerRight = strings.Join(metaRight, mantleDot) + stMantle(ColorMantle, false).Render("  ")
	}
	bannerLine := joinBannerLeftRight(bannerLeft, bannerRight, width, ColorMantle)

	out := make([]string, 0, height)
	out = append(out, bannerLine)
	for i := 0; i < viewH; i++ {
		idx := scroll + i
		if idx < len(body) {
			out = append(out, body[idx])
		} else {
			out = append(out, fitCellWidth("", width))
		}
	}

	var hits []tabPaneHitRow
	for _, h := range rawHits {
		visY := h.y - scroll + 1 // +1 for pinned top bar
		if visY >= 1 && visY < height {
			h.y = visY
			hits = append(hits, h)
		}
	}
	_ = stBase
	return out, hits
}

// renderInspectorHero renders the clean 2-line hero header at the top of the
// RHS inspector: bold title + right-aligned status pill, and a calm subtitle.
func (m Model) renderInspectorHero(sp model.Space, tab TabItem, p *model.Pane, width int) []string {
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	dot := st(ColorSurface1, false).Render(" · ")
	innerW := width - 4
	if innerW < 12 {
		innerW = 12
	}

	// Status pill on the right.
	status := tab.Status
	ageStr := ""
	if p != nil {
		status = p.Status
		if p.AgeMs > 0 {
			ageStr = formatAge(p.AgeMs)
		}
	}
	pillText := status.Icon() + " " + status.Label()
	if ageStr != "" {
		pillText += " · " + ageStr
	}
	pillStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(ColorSurface0)).
		Foreground(lipgloss.Color(StatusColor(status))).
		Bold(status != model.StatusIdle)
	pill := pillStyle.Render(" " + pillText + " ")
	pillW := ansi.StringWidth(pill)

	// Title on the left: prefer conversation title for agents, pane/window title otherwise.
	title := strings.TrimSpace(tab.Window.Name)
	if p != nil {
		if conv := paneConversationName(*p); conv != "" {
			title = conv
		} else if t := cleanConversationTitle(p.Title, p.App, p.Command, p.CWD); t != "" {
			title = t
		} else if p.App != "" && isGenericShellName(title) {
			title = p.App
		}
	}
	if title == "" {
		title = tab.Window.ID
	}

	titleBudget := innerW - pillW - 2
	if titleBudget < 10 {
		titleBudget = innerW
	}
	titleStyled := st(ColorText, true).Render(truncate(title, titleBudget))
	titleW := ansi.StringWidth(titleStyled)

	var line1 string
	if titleW+pillW+2 <= innerW {
		gap := strings.Repeat(" ", innerW-titleW-pillW)
		line1 = fitCellWidth("  "+titleStyled+gap+pill, width)
	} else {
		line1 = fitCellWidth("  "+titleStyled, width)
	}

	// Subtitle line: calm metadata identity.
	var subParts []string
	if p != nil {
		if lbl := formatAppLabel(p.App, p.Profile); lbl != "" {
			subParts = append(subParts, renderStyledAppCol(p.App, p.Profile, 0, 28, false, ColorBase, st))
		}
		if p.IsAgent() {
			mdl := resolvePaneModel(sp, *p, m.bgDetails)
			if mdl != "" {
				subParts = append(subParts, st(ColorSubtext0, false).Render(truncate(mdl, 26)))
			}
			nowMs := m.opts.NowOrDefault().UnixMilli()
			if turnStart := resolveTurnStartedMs(sp, *p, m.bgDetails); turnStart > 0 && nowMs > turnStart {
				subParts = append(subParts, st(ColorYellow, false).Render("turn "+formatAge(nowMs-turnStart)))
			}
		} else {
			if cwd := shortenPath(p.CWD, m.opts.HomeDir); cwd != "" {
				subParts = append(subParts, st(ColorOverlay0, false).Render(truncate(cwd, maxInt(16, innerW-24))))
			}
		}
		if len(tab.Panes) == 1 && p.ID != "" {
			subParts = append(subParts, st(ColorOverlay0, false).Render(p.ID))
		}
		if p.InMode {
			subParts = append(subParts, st(ColorYellow, false).Render("copy-mode"))
		}
		if p.Synchronized {
			subParts = append(subParts, st(ColorTeal, false).Render("sync-panes"))
		}
		if p.IsDead {
			subParts = append(subParts, st(ColorRed, true).Render(fmt.Sprintf("exited (%d)", p.DeadStatus)))
		}
	}
	if len(subParts) == 0 {
		return []string{line1}
	}
	line2 := fitCellWidth("  "+ansi.Truncate(strings.Join(subParts, dot), innerW, "…"), width)
	return []string{line1, line2}
}

// renderPanePillStrip renders a clean horizontal clickable pane selector strip
// for multi-pane tabs, wrapping neatly when there are many panes.
func (m Model) renderPanePillStrip(panes []model.Pane, selIdx, width, baseY int) ([]string, []tabPaneHitRow) {
	innerW := width - 4
	if innerW < 16 {
		innerW = 16
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0)).Render("←→ switch")
	hintW := ansi.StringWidth(hint)

	type pillItem struct {
		paneIdx int
		styled  string
		w       int
	}
	pills := make([]pillItem, 0, len(panes))
	for i, p := range panes {
		selected := i == selIdx
		bg := ColorMantle
		fg := ColorSubtext0
		if selected {
			bg = ColorSurface0
			fg = ColorText
		}
		st := func(c string, bold bool) lipgloss.Style {
			s := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(c))
			if bold {
				s = s.Bold(true)
			}
			return s
		}
		label := formatAppLabel(p.App, p.Profile)
		if label == "" {
			label = p.Command
		}
		maxLbl := 24
		if len(panes) > 3 {
			maxLbl = 16
		}
		label = truncate(label, maxLbl)
		var pb strings.Builder
		pb.WriteString(st(bg, false).Render(" "))
		pb.WriteString(st(StatusColor(p.Status), selected || p.Status.IsAttention()).Render(p.Status.Icon()))
		pb.WriteString(st(bg, false).Render(" "))
		pb.WriteString(st(ColorOverlay0, false).Render(p.ID))
		if label != "" {
			pb.WriteString(st(bg, false).Render(" "))
			pb.WriteString(st(fg, selected).Render(label))
		}
		pb.WriteString(st(bg, false).Render(" "))
		s := pb.String()
		pills = append(pills, pillItem{paneIdx: i, styled: s, w: ansi.StringWidth(s)})
	}

	var rows [][]pillItem
	curRow := []pillItem{}
	curW := 0
	for _, pi := range pills {
		need := pi.w
		if len(curRow) > 0 {
			need += 1 // 1-space gap between pills
		}
		maxRowW := innerW
		if len(rows) == 0 && innerW > hintW+24 {
			maxRowW = innerW - hintW - 2
		}
		if len(curRow) > 0 && curW+need > maxRowW {
			rows = append(rows, curRow)
			curRow = []pillItem{pi}
			curW = pi.w
		} else {
			curRow = append(curRow, pi)
			curW += need
		}
	}
	if len(curRow) > 0 {
		rows = append(rows, curRow)
	}

	var out []string
	var hits []tabPaneHitRow
	for ri, row := range rows {
		var b strings.Builder
		b.WriteString("  ")
		x := 2
		for pi, item := range row {
			if pi > 0 {
				b.WriteString(" ")
				x++
			}
			hits = append(hits, tabPaneHitRow{
				y:       baseY + ri,
				xStart:  x,
				xEnd:    x + item.w,
				paneIdx: item.paneIdx,
			})
			b.WriteString(item.styled)
			x += item.w
		}
		if ri == 0 && x+2+hintW <= width-2 {
			b.WriteString(strings.Repeat(" ", width-2-x-hintW))
			b.WriteString(hint)
		}
		out = append(out, fitCellWidth(b.String(), width))
	}
	return out, hits
}

// renderAgentTelemetrySections renders the full agent telemetry in a clean,
// flat layout (no shaded cards or quote rails): active attention/waiting notice
// (never for done/idle) -> prompt -> tool (input + result) -> latest reply ->
// aligned background work list.
func (m Model) renderAgentTelemetrySections(sp model.Space, p model.Pane, width int) []string {
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	nowMs := m.opts.NowOrDefault().UnixMilli()

	var detail *model.HookDetail
	if m.bgDetails != nil {
		detail = m.bgDetails[paneBgKey(sp.Name, p.ID)]
	}
	bgItems := m.paneBackgroundItems(sp.Name, p)

	waitingOn := ""
	if p.Status.IsAttention() || p.Status == model.StatusWorking {
		if p.Hook != nil {
			waitingOn = strings.TrimSpace(p.Hook.WaitingOn)
		}
		if waitingOn == "" && detail != nil {
			waitingOn = strings.TrimSpace(detail.WaitingOn)
		}
	}
	statusMsg := strings.TrimSpace(p.Text)
	if statusMsg == "" && p.Hook != nil {
		statusMsg = strings.TrimSpace(p.Hook.Text)
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" && p.Hook != nil {
		kind = strings.TrimSpace(p.Hook.Kind)
	}

	var out []string
	addBlank := func() {
		if len(out) > 0 {
			out = append(out, fitCellWidth("", width))
		}
	}

	wrapW := maxInt(18, width-4)

	// A. Attention / Waiting / Live Working notice (only rendered for attention or
	// active working states; idle/done/quiet panes rely on the top-right status pill).
	showStatusBlock := p.Status.IsAttention() || (p.Status == model.StatusWorking && (waitingOn != "" || statusMsg != ""))
	if showStatusBlock {
		accentColor := StatusColor(p.Status)
		if waitingOn != "" && !p.Status.IsAttention() {
			accentColor = ColorBlue
		}
		heading := strings.ToUpper(p.Status.Label())
		sub := strings.ToUpper(kind)
		out = append(out, renderSectionLabel(heading, sub, accentColor, width))

		if waitingOn != "" {
			for i, ln := range wrapTextLines(waitingOn, maxInt(12, wrapW-12), 4) {
				if i == 0 {
					out = append(out, fitCellWidth("  "+st(ColorBlue, true).Render("Waiting on: ")+st(ColorText, false).Render(ln), width))
				} else {
					out = append(out, fitCellWidth("              "+st(ColorText, false).Render(ln), width))
				}
			}
		}
		if statusMsg != "" && !strings.EqualFold(statusMsg, waitingOn) {
			for _, ln := range wrapTextLines(statusMsg, wrapW, 6) {
				out = append(out, fitCellWidth("  "+st(ColorText, false).Render(ln), width))
			}
		}
	}

	// B. Prompt section (flat).
	if detail != nil && strings.TrimSpace(detail.Prompt) != "" {
		addBlank()
		out = append(out, renderSectionLabel("PROMPT", "", ColorLavender, width))
		out = append(out, renderFlatTextBlock(wrapTextLines(detail.Prompt, wrapW, 10), ColorText, width)...)
	}

	// C. Tool Invocation section (flat: input + result).
	if detail != nil && detail.Tool != nil && strings.TrimSpace(detail.Tool.Name) != "" {
		addBlank()
		out = append(out, renderSectionLabel("TOOL", detail.Tool.Name, ColorYellow, width))
		if inp := strings.TrimSpace(detail.Tool.Input); inp != "" {
			isBash := strings.EqualFold(detail.Tool.Name, "bash")
			lineW := wrapW
			if isBash {
				lineW = maxInt(14, wrapW-2)
			}
			for i, ln := range wrapTextLines(inp, lineW, 10) {
				if isBash {
					if i == 0 {
						out = append(out, fitCellWidth("  "+st(ColorYellow, false).Render("$ ")+st(ColorText, false).Render(ln), width))
					} else {
						out = append(out, fitCellWidth("    "+st(ColorText, false).Render(ln), width))
					}
				} else {
					out = append(out, fitCellWidth("  "+st(ColorText, false).Render(ln), width))
				}
			}
		}
		if res := strings.TrimSpace(detail.Tool.Output); res != "" {
			out = append(out, fitCellWidth("  "+st(ColorOverlay0, true).Render("Result"), width))
			out = append(out, renderFlatTextBlock(wrapTextLines(res, wrapW, 10), ColorSubtext0, width)...)
		}
	}

	// D. Latest Reply section (flat).
	if detail != nil && strings.TrimSpace(detail.Reply) != "" {
		addBlank()
		out = append(out, renderSectionLabel("LATEST REPLY", "", ColorGreen, width))
		out = append(out, renderFlatTextBlock(wrapTextLines(detail.Reply, wrapW, 12), ColorText, width)...)
	}

	// E. Background Work section (aligned columns for subagents, finite jobs, services/watchers/schedules).
	if len(bgItems) > 0 {
		addBlank()
		activeFinite, activeSvc := 0, 0
		for _, it := range bgItems {
			if !it.IsActive() {
				continue
			}
			if it.IsFinite() {
				activeFinite++
			} else {
				activeSvc++
			}
		}
		var sub []string
		if activeFinite > 0 {
			sub = append(sub, fmt.Sprintf("%d active", activeFinite))
		}
		if activeSvc > 0 {
			noun := "service"
			if activeSvc != 1 {
				noun = "services"
			}
			sub = append(sub, fmt.Sprintf("%d %s", activeSvc, noun))
		}
		if len(sub) == 0 {
			sub = append(sub, fmt.Sprintf("%d total", len(bgItems)))
		}
		out = append(out, renderSectionLabel("BACKGROUND WORK", strings.Join(sub, " · "), ColorBlue, width))
		out = append(out, renderAlignedBGList(bgItems, nowMs, width)...)
	} else if p.Hook != nil && (p.Hook.ChildCount > 0 || p.Hook.Background > 0 || p.Hook.ServiceActive > 0) {
		addBlank()
		out = append(out, renderSectionLabel("BACKGROUND WORK", formatDetailBackgroundSummary(p.Hook), ColorBlue, width))
	}

	// Loading / empty telemetry notice.
	if detail == nil && p.Hook != nil && p.Hook.HasDetail {
		addBlank()
		out = append(out, fitCellWidth("  "+st(ColorYellow, false).Render("Loading full hook telemetry…"), width))
	} else if len(out) == 0 {
		out = append(out, fitCellWidth("  "+st(ColorOverlay0, false).Render("No hook events recorded for this agent yet."), width))
	}

	return out
}

// renderProcessCommandSection renders the flat foreground command section for
// non-agent panes (editors, shells, servers, test runners).
func (m Model) renderProcessCommandSection(p model.Pane, width int) []string {
	var out []string
	fg := strings.TrimSpace(p.ForegroundCmd)
	if fg == "" {
		fg = strings.TrimSpace(p.Command)
	}
	if fg == "" {
		return out
	}

	sub := ""
	if p.ChildProcessCount > 0 {
		noun := "child process"
		if p.ChildProcessCount != 1 {
			noun = "child processes"
		}
		sub = fmt.Sprintf("%d %s", p.ChildProcessCount, noun)
	}
	out = append(out, renderSectionLabel("FOREGROUND COMMAND", sub, ColorLavender, width))

	pfxSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	txtSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	wrapW := maxInt(14, width-6)
	for i, ln := range wrapTextLines(fg, wrapW, 6) {
		if i == 0 {
			out = append(out, fitCellWidth("  "+pfxSt.Render("$ ")+txtSt.Render(ln), width))
		} else {
			out = append(out, fitCellWidth("    "+txtSt.Render(ln), width))
		}
	}
	return out
}

func renderAlignedBGList(items []model.HookChild, nowMs int64, width int) []string {
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	innerW := width - 4
	if innerW < 20 {
		innerW = 20
	}
	const tagColW = 10
	var out []string

	for _, item := range items {
		icon, iconColor, stateLabel, stateColor := bgItemPresentation(item)
		tag := truncate(bgItemTag(item), tagColW)
		elapsed := formatBgElapsed(item.ElapsedMs(nowMs))

		var rb strings.Builder
		rb.WriteString(st(stateColor, item.State == "failed").Render(stateLabel))
		if elapsed != "" {
			rb.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf(" %4s", elapsed)))
		}
		rightStyled := rb.String()
		rightW := ansi.StringWidth(rightStyled)

		var lb strings.Builder
		lb.WriteString(st(iconColor, true).Render(icon))
		lb.WriteString("  ")
		tagPad := tag
		if tw := ansi.StringWidth(tagPad); tw < tagColW {
			tagPad += strings.Repeat(" ", tagColW-tw)
		}
		lb.WriteString(st(bgTagColor(item), true).Render(tagPad))

		desc := collapseWhitespace(bgItemDisplayText(item))
		maxLeft := innerW - rightW - 2
		if desc != "" {
			rem := maxLeft - ansi.StringWidth(lb.String()) - 2
			if rem > 6 {
				lb.WriteString("  ")
				descFg := ColorText
				if !item.IsActive() {
					descFg = ColorSubtext0
				}
				lb.WriteString(st(descFg, false).Render(truncate(desc, rem)))
			}
		}
		leftStyled := ansi.Truncate(lb.String(), maxInt(12, maxLeft), "…")
		gap := innerW - ansi.StringWidth(leftStyled) - rightW
		if gap < 1 {
			gap = 1
		}
		out = append(out, fitCellWidth("  "+leftStyled+strings.Repeat(" ", gap)+rightStyled, width))

		cmd := strings.TrimSpace(item.Command)
		if cmd != "" && !strings.EqualFold(cmd, desc) {
			cmdPrefix := strings.Repeat(" ", 2+3+tagColW+2)
			rem := width - len(cmdPrefix) - 4
			if rem > 8 {
				out = append(out, fitCellWidth(cmdPrefix+st(ColorOverlay0, false).Render("$ ")+st(ColorSubtext0, false).Render(truncate(cmd, rem)), width))
			}
		}
	}
	return out
}

func inspectorWorktreeLabel(g *model.GitInfo, w model.Window) string {
	if g != nil && g.IsWorktree {
		if s := strings.TrimSpace(g.WorktreeName); s != "" {
			return s
		}
		if s := strings.TrimSpace(g.Branch); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(w.WorktreeName); s != "" {
		return s
	}
	if p := strings.TrimSpace(w.WorktreePath); p != "" {
		return filepath.Base(p)
	}
	return ""
}

// renderInspectorGitSection renders aligned Worktree / Main repo / Branch / PR / CI rows.
func (m Model) renderInspectorGitSection(g *model.GitInfo, w model.Window, cwd string, width int) []string {
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}
	isWT := (g != nil && g.IsWorktree) || strings.TrimSpace(w.WorktreePath) != "" || strings.TrimSpace(w.WorktreeName) != ""
	subtitle := ""
	if g != nil {
		subtitle = relativeRepoLabel(g.RepoRoot, cwd, m.opts.HomeDir)
		if isWT {
			repo := strings.TrimSpace(g.RepoName)
			if repo != "" {
				subtitle = "worktree · " + repo
			} else if subtitle != "" {
				subtitle = "worktree · " + subtitle
			} else {
				subtitle = "worktree"
			}
		}
	} else if isWT {
		subtitle = "worktree"
	}

	var out []string
	out = append(out, renderSectionLabel("GIT", subtitle, ColorTeal, width))

	const labelW = 9
	valW := maxInt(14, width-4-labelW-2)
	addRow := func(label, styledVal string) {
		lbl := st(ColorOverlay0, false).Render(fmt.Sprintf("%-*s", labelW, label))
		out = append(out, fitCellWidth("  "+lbl+"  "+ansi.Truncate(styledVal, valW, "…"), width))
	}

	// Worktree + Main repo rows when backed by a linked worktree.
	if isWT {
		wtName := inspectorWorktreeLabel(g, w)
		managed := (g != nil && g.Managed) || w.WorktreeManaged
		repoName := ""
		if g != nil {
			repoName = strings.TrimSpace(g.RepoName)
		}
		if wtName != "" {
			wtVal := st(ColorMauve, true).Render("⎇ ") + st(ColorText, true).Render(truncate(wtName, 26))
			var meta []string
			if repoName != "" {
				meta = append(meta, "from "+repoName)
			}
			if managed {
				meta = append(meta, "managed")
			}
			if len(meta) > 0 {
				wtVal += "  " + st(ColorOverlay0, false).Render("("+strings.Join(meta, " · ")+")")
			}
			addRow("Worktree", wtVal)
		}
		if g != nil && strings.TrimSpace(g.MainRepoRoot) != "" {
			addRow("Main repo", st(ColorSubtext0, false).Render(shortenPath(g.MainRepoRoot, m.opts.HomeDir)))
		} else if wtPath := strings.TrimSpace(w.WorktreePath); wtPath != "" {
			addRow("Path", st(ColorSubtext0, false).Render(shortenPath(wtPath, m.opts.HomeDir)))
		}
		if g == nil && strings.TrimSpace(w.WorktreeBase) != "" {
			addRow("Base", st(ColorOverlay0, false).Render(w.WorktreeBase))
		}
	}

	if g == nil {
		return out
	}

	// Branch + diff + ahead/behind + flags.
	var branchParts []string
	if br := renderGitBranchBadge(g, 28, st); br != "" {
		if g.BaseBranch != "" && g.BaseBranch != g.Branch {
			br += st(ColorOverlay0, false).Render(" → " + truncate(g.BaseBranch, 18))
		}
		branchParts = append(branchParts, br)
	}
	if diff := renderGitDiffBadge(g, ColorBase, st); diff != "" {
		branchParts = append(branchParts, diff)
	}
	if g.HasConflict {
		branchParts = append(branchParts, st(ColorRed, true).Render("conflict"))
	}
	if g.IsRebasing {
		branchParts = append(branchParts, st(ColorYellow, true).Render("rebasing"))
	}
	if len(branchParts) > 0 {
		addRow("Branch", strings.Join(branchParts, "   "))
	}

	// Pull Request row.
	if pr := g.PR; pr != nil && pr.Number > 0 {
		icon, color := prStateIconColor(pr)
		state := strings.ToLower(pr.State)
		if pr.IsDraft {
			state = "draft"
		}
		prVal := st(color, true).Render(icon + " " + state)
		if t := strings.TrimSpace(pr.Title); t != "" {
			prVal += "  " + st(ColorText, false).Render(truncate(t, maxInt(14, valW-14)))
		}
		addRow(fmt.Sprintf("PR #%d", pr.Number), prVal)
	}

	// CI Checks row.
	if g.ShouldShowChecks() {
		ch := g.Checks
		var ciVal string
		switch ch.State {
		case model.CheckSuccess:
			s := "✓ passing"
			if ch.Total > 0 {
				s = fmt.Sprintf("✓ %d/%d passing", ch.Passed, ch.Total)
			}
			ciVal = st(ColorGreen, true).Render(s)
		case model.CheckFailure:
			s := "✗ failing"
			if ch.Total > 0 {
				s = fmt.Sprintf("✗ %d/%d", ch.Passed, ch.Total)
			}
			ciVal = st(ColorRed, true).Render(s)
			if ch.FailingName != "" {
				ciVal += "  " + st(ColorSubtext0, false).Render(truncate(ch.FailingName, maxInt(12, valW-14)))
			}
		case model.CheckPending:
			s := "◷ pending"
			if ch.Total > 0 {
				s = fmt.Sprintf("◷ %d/%d pending", ch.Passed, ch.Total)
			}
			ciVal = st(ColorYellow, false).Render(s)
		}
		if ciVal != "" {
			addRow("Checks", ciVal)
		}
	}

	return out
}

// renderInspectorContextTokensSection renders the abtop-style Context & Tokens
// telemetry card inside the tab inspector when a matching SessionRow is available.
func (m Model) renderInspectorContextTokensSection(sess model.SessionRow, width int) []string {
	st := func(fg string, bold bool) lipgloss.Style {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
		if bold {
			s = s.Bold(true)
		}
		return s
	}

	var subParts []string
	if sess.ContextWindow > 0 {
		subParts = append(subParts, fmtTokens(sess.ContextWindow))
	}
	if sess.CompactionCount > 0 {
		subParts = append(subParts, fmt.Sprintf("C%d", sess.CompactionCount))
	}
	if sess.MemoryLines > 0 {
		subParts = append(subParts, fmt.Sprintf("MEM %d/200", sess.MemoryLines))
	}

	var out []string
	out = append(out, renderSectionLabel("CONTEXT & TOKENS", strings.Join(subParts, " · "), ColorTeal, width))

	const labelW = 9
	innerW := width - 4
	if innerW < 20 {
		innerW = 20
	}
	valW := maxInt(14, innerW-labelW-2)
	addRow := func(label, styledVal string) {
		lbl := st(ColorOverlay0, false).Render(fmt.Sprintf("%-*s", labelW, label))
		out = append(out, fitCellWidth("  "+lbl+"  "+ansi.Truncate(styledVal, valW, "…"), width))
	}

	// Context bar row.
	ctxBarW := maxInt(6, minInt(18, valW-16))
	warn := string(sess.Warning)
	if warn == "" {
		if sess.ContextPct >= 90 {
			warn = "⚠"
		} else if sess.ContextPct >= 75 {
			warn = "!"
		}
	}
	pctHex := lerpHexColor(sess.ContextPct, ColorGreen, ColorYellow, ColorRed)
	ctxMeta := fmt.Sprintf(" %.0f%%%s", sess.ContextPct, warn)
	if sess.ContextTokens > 0 && sess.ContextWindow > 0 {
		ctxMeta += fmt.Sprintf(" (%s/%s)", fmtTokens(sess.ContextTokens), fmtTokens(sess.ContextWindow))
	}
	addRow("Context", meterBar(sess.ContextPct, ctxBarW, ColorGreen, ColorYellow, ColorRed)+
		st(pctHex, warn != "").Render(ctxMeta))

	// Token breakdown rows.
	total := sess.TotalTokens
	if total <= 0 {
		total = sess.Usage.Total()
	}
	pctOf := func(v int64) float64 {
		if total <= 0 {
			return 0
		}
		return float64(v) / float64(total) * 100.0
	}
	tokBarW := maxInt(5, minInt(14, valW-10))

	avg := sess.AvgTokensPerTurn
	if avg <= 0 && sess.TurnCount > 0 {
		avg = total / int64(sess.TurnCount)
	}
	totVal := st(ColorText, true).Render(fmtTokens(total))
	if sess.TurnCount > 0 {
		totVal += st(ColorOverlay0, false).Render(fmt.Sprintf("  ·  %d turns  ·  %s/t", sess.TurnCount, fmtTokens(avg)))
	}
	addRow("Total", totVal)
	if sess.Usage.CostPriced || sess.Usage.CostUSD > 0 {
		addRow("Cost", st(ColorYellow, true).Render(fmtCostUSD(sess.Usage.CostUSD, sess.Usage.CostPriced)))
	}

	if sess.Usage.InputTokens > 0 || sess.Usage.OutputTokens > 0 || sess.Usage.CacheReadTokens > 0 || sess.Usage.CacheWriteTokens > 0 {
		addRow("Input", meterBar(pctOf(sess.Usage.InputTokens), tokBarW, ColorTeal, ColorBlue, ColorLavender)+
			st(ColorTeal, false).Render(" "+fmtTokens(sess.Usage.InputTokens)))
		addRow("Output", meterBar(pctOf(sess.Usage.OutputTokens), tokBarW, ColorYellow, ColorRed, ColorRed)+
			st(ColorYellow, false).Render(" "+fmtTokens(sess.Usage.OutputTokens)))
		addRow("Cache R", meterBar(pctOf(sess.Usage.CacheReadTokens), tokBarW, ColorBlue, ColorMauve, ColorMauve)+
			st(ColorMauve, false).Render(" "+fmtTokens(sess.Usage.CacheReadTokens)))
		addRow("Cache W", meterBar(pctOf(sess.Usage.CacheWriteTokens), tokBarW, ColorBlue, ColorMauve, ColorMauve)+
			st(ColorBlue, false).Render(" "+fmtTokens(sess.Usage.CacheWriteTokens)))
	}

	// Per-turn braille sparkline.
	if len(sess.TokenHistory) > 0 {
		hist := make([]float64, len(sess.TokenHistory))
		for i, v := range sess.TokenHistory {
			hist[i] = float64(v)
		}
		sparkW := maxInt(6, minInt(22, valW-8))
		addRow("Per-turn", brailleSparkline(hist, sparkW, ColorGreen, ColorYellow, ColorRed)+
			st(ColorOverlay0, false).Render(" tok/t"))
	}

	// Listening child ports.
	if len(sess.Ports) > 0 {
		var pParts []string
		for _, pt := range sess.Ports {
			if pt.Duplicate {
				pParts = append(pParts, st(ColorRed, true).Render(fmt.Sprintf(":%d ⚠", pt.Port)))
			} else {
				pParts = append(pParts, st(ColorGreen, false).Render(fmt.Sprintf(":%d", pt.Port)))
			}
		}
		addRow("Ports", strings.Join(pParts, "  "))
	}

	return out
}

// renderInspectorDetailsSection renders a compact 2-column (or 1-column when
// narrow) runtime & session metadata grid at the bottom of the inspector.
func (m Model) renderInspectorDetailsSection(sp model.Space, tab TabItem, p model.Pane, sess *model.SessionRow, width int) []string {
	now := m.opts.NowOrDefault()
	nowMs := now.UnixMilli()
	var items []inspectorKV

	if cwd := shortenPath(p.CWD, m.opts.HomeDir); cwd != "" {
		items = append(items, inspectorKV{label: "Dir", value: cwd, color: ColorSubtext0})
	}

	if p.IsAgent() {
		var detail *model.HookDetail
		if m.bgDetails != nil {
			detail = m.bgDetails[paneBgKey(sp.Name, p.ID)]
		}

		// Timing summary.
		var tParts []string
		var turnStart, stateSince, lastTs int64
		if p.Hook != nil {
			turnStart, stateSince, lastTs = p.Hook.TurnStartedMs, p.Hook.StateSinceMs, p.Hook.TsMs
		}
		if detail != nil {
			if turnStart == 0 {
				turnStart = detail.TurnStartedMs
			}
			if stateSince == 0 {
				stateSince = detail.StateSinceMs
			}
			if lastTs == 0 {
				lastTs = detail.TsMs
			}
		}
		if turnStart > 0 && nowMs > turnStart {
			tParts = append(tParts, "turn "+formatAge(nowMs-turnStart))
		}
		if stateSince > 0 && nowMs > stateSince {
			tParts = append(tParts, "state "+formatAge(nowMs-stateSince))
		} else if p.AgeMs > 0 {
			tParts = append(tParts, "age "+formatAge(p.AgeMs))
		}
		if lastTs > 0 {
			tParts = append(tParts, formatOutcomeDetailTime(lastTs, now))
		}
		if len(tParts) > 0 {
			items = append(items, inspectorKV{label: "Timing", value: strings.Join(tParts, " · "), color: ColorSubtext0})
		}

		// Runtime summary (PID, children, geometry).
		var rParts []string
		if p.PID > 0 {
			rParts = append(rParts, fmt.Sprintf("pid %d", p.PID))
		}
		if p.ChildProcessCount > 0 {
			noun := "child"
			if p.ChildProcessCount != 1 {
				noun = "children"
			}
			rParts = append(rParts, fmt.Sprintf("%d %s", p.ChildProcessCount, noun))
		}
		if p.Width > 0 && p.Height > 0 {
			rParts = append(rParts, fmt.Sprintf("%d×%d", p.Width, p.Height))
		}
		if sess != nil {
			if sess.MemKB > 0 {
				rParts = append(rParts, "mem "+fmtMemKB(sess.MemKB))
			}
			if sess.LaunchSurface != "" {
				rParts = append(rParts, string(sess.LaunchSurface))
			}
		}
		if len(rParts) > 0 {
			items = append(items, inspectorKV{label: "Runtime", value: strings.Join(rParts, " · "), color: ColorOverlay0})
		}

		// Token / cost usage summary (from passive telemetry such as daemon_sse, router, or OTLP).
		if usageStr := formatHookUsage(resolveHookUsage(p, detail)); usageStr != "" {
			items = append(items, inspectorKV{label: "Usage", value: usageStr, color: ColorSubtext0})
		}

		// Session + hook event + source.
		var sParts []string
		sid := strings.TrimSpace(p.ResumeSession)
		if p.Hook != nil && strings.TrimSpace(p.Hook.SessionID) != "" {
			sid = strings.TrimSpace(p.Hook.SessionID)
		}
		if detail != nil && strings.TrimSpace(detail.SessionID) != "" {
			sid = strings.TrimSpace(detail.SessionID)
		}
		if sid != "" {
			sParts = append(sParts, sid)
		}
		event := ""
		if p.Hook != nil {
			event = strings.TrimSpace(p.Hook.Event)
		}
		if event == "" && detail != nil {
			event = strings.TrimSpace(detail.Event)
		}
		if event != "" {
			sParts = append(sParts, event)
		}
		src := strings.TrimSpace(p.Source)
		if p.Hook != nil {
			if hs := strings.TrimSpace(p.Hook.Source); hs != "" && (src == "" || strings.EqualFold(src, "hook")) {
				src = hs
			}
		}
		if detail != nil {
			if ds := strings.TrimSpace(detail.Source); ds != "" && (src == "" || strings.EqualFold(src, "hook")) {
				src = ds
			}
		}
		if src != "" {
			sParts = append(sParts, src)
		}
		if len(sParts) > 0 {
			items = append(items, inspectorKV{label: "Session", value: strings.Join(sParts, " · "), color: ColorOverlay0})
		}
	} else {
		if p.Command != "" {
			prog := p.Command
			if p.StartCommand != "" && !strings.EqualFold(p.StartCommand, p.Command) {
				prog += " (from " + p.StartCommand + ")"
			}
			items = append(items, inspectorKV{label: "Process", value: prog, color: ColorSubtext0})
		}
		var rParts []string
		if p.PID > 0 {
			rParts = append(rParts, fmt.Sprintf("pid %d", p.PID))
		}
		if p.ChildProcessCount > 0 {
			noun := "child"
			if p.ChildProcessCount != 1 {
				noun = "children"
			}
			rParts = append(rParts, fmt.Sprintf("%d %s", p.ChildProcessCount, noun))
		}
		if p.Width > 0 && p.Height > 0 {
			rParts = append(rParts, fmt.Sprintf("%d×%d", p.Width, p.Height))
		}
		if sess != nil {
			if sess.MemKB > 0 {
				rParts = append(rParts, "mem "+fmtMemKB(sess.MemKB))
			}
			if sess.LaunchSurface != "" {
				rParts = append(rParts, string(sess.LaunchSurface))
			}
		}
		if len(rParts) > 0 {
			items = append(items, inspectorKV{label: "Runtime", value: strings.Join(rParts, " · "), color: ColorOverlay0})
		}
		var flags []string
		if p.IsActive {
			flags = append(flags, "active")
		}
		if p.InMode {
			flags = append(flags, "copy-mode")
		}
		if p.Synchronized {
			flags = append(flags, "sync-panes")
		}
		if p.IsDead {
			flags = append(flags, fmt.Sprintf("exited (%d)", p.DeadStatus))
		}
		if tab.Window.Zoomed {
			flags = append(flags, "zoomed")
		}
		if tab.Window.Bell {
			flags = append(flags, "bell")
		}
		if tab.Window.Silence {
			flags = append(flags, "silence")
		}
		if len(flags) > 0 {
			c := ColorOverlay0
			if p.IsDead {
				c = ColorRed
			} else if p.InMode {
				c = ColorYellow
			}
			items = append(items, inspectorKV{label: "Flags", value: strings.Join(flags, " · "), color: c})
		}
		if !tab.Window.ActivityAt.IsZero() {
			items = append(items, inspectorKV{label: "Activity", value: formatOutcomeDetailTime(tab.Window.ActivityAt.UnixMilli(), now), color: ColorOverlay0})
		}
		if t := strings.TrimSpace(p.Title); t != "" && !strings.EqualFold(t, p.App) && !strings.EqualFold(t, tab.Window.Name) {
			items = append(items, inspectorKV{label: "Title", value: t, color: ColorSubtext0})
		}
	}

	if len(items) == 0 {
		return nil
	}

	var out []string
	out = append(out, renderSectionLabel("DETAILS", "", ColorOverlay0, width))
	out = append(out, renderKVGrid(items, width)...)
	return out
}

// ---------------------------------------------------------------------------
// clean UI primitives for the RHS inspector
// ---------------------------------------------------------------------------

func renderInsetHairline(width int) string {
	innerW := width - 4
	if innerW < 4 {
		return renderDividerLine(width)
	}
	return fitCellWidth("  "+styleDivider.Render(strings.Repeat("─", innerW)), width)
}

func renderSectionLabel(title, subtitle, accentColor string, width int) string {
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor)).Bold(true).Render(title))
	if strings.TrimSpace(subtitle) != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSurface1)).Render(" · "))
		rem := width - ansi.StringWidth(b.String()) - 2
		if rem > 4 {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0)).Render(truncate(subtitle, rem)))
		}
	}
	return fitCellWidth(b.String(), width)
}

// renderFlatTextBlock renders wrapped text lines cleanly with consistent
// 2-cell left/right margins and no card background or vertical rails.
func renderFlatTextBlock(lines []string, textColor string, width int) []string {
	textSt := lipgloss.NewStyle().Foreground(lipgloss.Color(textColor))
	maxTextW := maxInt(8, width-4)
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = fitCellWidth("  "+textSt.Render(truncate(ln, maxTextW)), width)
	}
	return out
}

// renderKVGrid formats key-value pairs in a clean aligned table matching the
// GIT section's label column (or 2 columns only on ultra-wide panels >= 112).
func renderKVGrid(items []inspectorKV, width int) []string {
	labelSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	const labelW = 9
	innerW := width - 4
	if innerW < 18 {
		innerW = 18
	}

	renderCell := func(kv inspectorKV, cellW int) string {
		valW := cellW - labelW - 2
		if valW < 6 {
			valW = 6
		}
		valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(kv.color))
		if kv.bold {
			valSt = valSt.Bold(true)
		}
		s := labelSt.Render(fmt.Sprintf("%-*s", labelW, kv.label)) + "  " + valSt.Render(truncate(kv.value, valW))
		if w := ansi.StringWidth(s); w < cellW {
			s += strings.Repeat(" ", cellW-w)
		}
		return ansi.Truncate(s, cellW, "…")
	}

	var out []string
	if width >= 112 && len(items) >= 2 {
		colGap := 3
		colW := (innerW - colGap) / 2
		for i := 0; i < len(items); i += 2 {
			left := renderCell(items[i], colW)
			if i+1 < len(items) {
				right := renderCell(items[i+1], innerW-colW-colGap)
				out = append(out, fitCellWidth("  "+left+strings.Repeat(" ", colGap)+right, width))
			} else {
				out = append(out, fitCellWidth("  "+renderCell(items[i], innerW), width))
			}
		}
		return out
	}

	for _, kv := range items {
		out = append(out, fitCellWidth("  "+renderCell(kv, innerW), width))
	}
	return out
}

func joinBannerLeftRight(leftStyled, rightStyled string, width int, bg string) string {
	if rightStyled == "" {
		return fitCellWidthWithBG(leftStyled, width, bg)
	}
	padSt := lipgloss.NewStyle().Background(lipgloss.Color(bg))
	leftW := ansi.StringWidth(leftStyled)
	rightW := ansi.StringWidth(rightStyled)
	var line string
	if leftW+rightW+2 <= width {
		line = leftStyled + padSt.Render(strings.Repeat(" ", width-leftW-rightW)) + rightStyled
	} else if width > rightW+8 {
		truncLeft := ansi.Truncate(leftStyled, width-rightW-2, "…")
		rem := width - ansi.StringWidth(truncLeft) - rightW
		if rem < 1 {
			rem = 1
		}
		line = truncLeft + padSt.Render(strings.Repeat(" ", rem)) + rightStyled
	} else {
		line = ansi.Truncate(leftStyled, width, "…")
	}
	return fitCellWidthWithBG(line, width, bg)
}

func resolvePaneModel(sp model.Space, p model.Pane, bgDetails map[string]*model.HookDetail) string {
	if bgDetails != nil {
		if d := bgDetails[paneBgKey(sp.Name, p.ID)]; d != nil && strings.TrimSpace(d.Model) != "" {
			return strings.TrimSpace(d.Model)
		}
	}
	if p.Hook != nil && strings.TrimSpace(p.Hook.Model) != "" {
		return strings.TrimSpace(p.Hook.Model)
	}
	return ""
}

func resolveTurnStartedMs(sp model.Space, p model.Pane, bgDetails map[string]*model.HookDetail) int64 {
	if p.Hook != nil && p.Hook.TurnStartedMs > 0 {
		return p.Hook.TurnStartedMs
	}
	if bgDetails != nil {
		if d := bgDetails[paneBgKey(sp.Name, p.ID)]; d != nil && d.TurnStartedMs > 0 {
			return d.TurnStartedMs
		}
	}
	return 0
}

func resolveHookUsage(p model.Pane, detail *model.HookDetail) *model.HookUsage {
	if p.Hook != nil && p.Hook.Usage != nil && p.Hook.Usage.HasData() {
		return p.Hook.Usage
	}
	if detail != nil && detail.Usage != nil && detail.Usage.HasData() {
		return detail.Usage
	}
	return nil
}

func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		s := fmt.Sprintf("%.1f", float64(n)/1_000_000)
		return strings.TrimSuffix(s, ".0") + "M"
	case n >= 100_000:
		return fmt.Sprintf("%dk", (n+500)/1_000)
	case n >= 1_000:
		s := fmt.Sprintf("%.1f", float64(n)/1_000)
		return strings.TrimSuffix(s, ".0") + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatCostUSD(cost float64) string {
	switch {
	case cost <= 0:
		return ""
	case cost < 0.01:
		return fmt.Sprintf("$%.4f", cost)
	default:
		return fmt.Sprintf("$%.2f", cost)
	}
}

func formatHookUsage(u *model.HookUsage) string {
	if u == nil || !u.HasData() {
		return ""
	}
	var parts []string
	if u.InputTokens > 0 {
		parts = append(parts, formatTokenCount(u.InputTokens)+" in")
	}
	if u.OutputTokens > 0 {
		parts = append(parts, formatTokenCount(u.OutputTokens)+" out")
	}
	if u.ReasoningTokens > 0 {
		parts = append(parts, formatTokenCount(u.ReasoningTokens)+" reason")
	}
	if u.CacheReadTokens > 0 {
		parts = append(parts, formatTokenCount(u.CacheReadTokens)+" cached")
	}
	if u.CacheWriteTokens > 0 && u.CacheReadTokens == 0 {
		parts = append(parts, formatTokenCount(u.CacheWriteTokens)+" cache-w")
	}
	if cost := formatCostUSD(u.CostUSD); cost != "" {
		parts = append(parts, cost)
	}
	return strings.Join(parts, " · ")
}

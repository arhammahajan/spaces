package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/arhammahajan/spaces/internal/model"
)

func paneBgKey(spaceName, paneID string) string {
	return spaceName + "\x00" + paneID
}

// paneBackgroundItems returns the ordered background work items (subagents,
// finite commands, services, watchers, scheduled wakeups) for a pane.
func (m Model) paneBackgroundItems(spaceName string, p model.Pane) []model.HookChild {
	if p.Hook != nil {
		if items := p.Hook.AllBackgroundWork(); len(items) > 0 {
			return items
		}
	}
	if m.bgDetails != nil {
		if d, ok := m.bgDetails[paneBgKey(spaceName, p.ID)]; ok && d != nil {
			return d.AllBackgroundWork()
		}
	}
	return nil
}

// paneCanExpandBackground reports whether the pane has subagents or background
// commands/services that can be expanded inline.
func (m Model) paneCanExpandBackground(spaceName string, p model.Pane) bool {
	if len(m.paneBackgroundItems(spaceName, p)) > 0 {
		return true
	}
	return p.Hook != nil && p.Hook.HasBackgroundWork()
}

// formatBackgroundBadge renders the compact inline badge for an agent row's
// subagents and background work, showing an expand/collapse chevron when items
// are expandable and distinguishing finite jobs from long-running services.
func (m Model) formatBackgroundBadge(sp model.Space, p model.Pane, st func(string, bool) lipgloss.Style) string {
	if p.Hook == nil {
		return ""
	}
	items := m.paneBackgroundItems(sp.Name, p)
	expanded := m.expandedBg != nil && m.expandedBg[paneBgKey(sp.Name, p.ID)]

	if len(items) == 0 {
		// Backward-compatible fallback when only aggregate counts exist.
		if p.Hook.ChildCount <= 0 && p.Hook.Background <= 0 {
			return ""
		}
		var parts []string
		if p.Hook.ChildCount > 0 {
			noun := "sub"
			if p.Hook.ChildCount > 1 {
				noun = "subs"
			}
			parts = append(parts, fmt.Sprintf("+%d %s", p.Hook.ChildCount, noun))
		}
		if p.Hook.Background > 0 {
			parts = append(parts, fmt.Sprintf("+%d bg", p.Hook.Background))
		}
		return st(ColorBlue, true).Render(strings.Join(parts, " · "))
	}

	subCount := 0
	finiteBg := 0
	serviceCount := 0
	for _, item := range items {
		switch {
		case item.EffectiveCategory() != "finite":
			if item.IsActive() {
				serviceCount++
			}
		case item.EffectiveRole() == "subagent":
			subCount++
		default:
			finiteBg++
		}
	}
	// If all non-finite items are terminal (none active) and no finite items exist,
	// still surface the total count so the user can expand to inspect outcomes.
	if subCount == 0 && finiteBg == 0 && serviceCount == 0 {
		finiteBg = len(items)
	}

	var parts []string
	if subCount > 0 {
		noun := "sub"
		if subCount > 1 {
			noun = "subs"
		}
		parts = append(parts, fmt.Sprintf("+%d %s", subCount, noun))
	}
	if finiteBg > 0 {
		parts = append(parts, fmt.Sprintf("+%d bg", finiteBg))
	}
	if serviceCount > 0 {
		noun := "svc"
		if serviceCount > 1 {
			noun = "svcs"
		}
		parts = append(parts, fmt.Sprintf("%d %s", serviceCount, noun))
	}

	chev := "▸ "
	if expanded {
		chev = "▾ "
	}
	color := ColorBlue
	if subCount == 0 && finiteBg == 0 && serviceCount > 0 {
		color = ColorTeal
	}
	return st(color, true).Render(chev + strings.Join(parts, " · "))
}

// renderInlineBackgroundRow renders one expanded inline child/background task
// line directly underneath its parent agent row (used in both Spaces and Inbox).
func (m Model) renderInlineBackgroundRow(item model.HookChild, isLast, selected, altBG bool, width int) string {
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

	icon, iconColor, stateLabel, stateColor := bgItemPresentation(item)
	elapsed := formatBgElapsed(item.ElapsedMs(m.opts.NowOrDefault().UnixMilli()))

	var rb strings.Builder
	statePadded := stateLabel
	if w := ansi.StringWidth(statePadded); w < statusColWidth {
		statePadded += strings.Repeat(" ", statusColWidth-w)
	}
	rb.WriteString(st(stateColor, item.State == "failed").Render(statePadded))
	if elapsed != "" {
		rb.WriteString(st(ColorOverlay0, false).Render(fmt.Sprintf(" %3s", elapsed)))
	} else {
		rb.WriteString(st(bg, false).Render("    "))
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

	connector := "├─ "
	if isLast {
		connector = "└─ "
	}
	tag := bgItemTag(item)
	desc := bgItemDisplayText(item)

	var lb strings.Builder
	lb.WriteString(st(bg, false).Render("     "))
	lb.WriteString(st(ColorSurface1, false).Render(connector))
	lb.WriteString(st(iconColor, true).Render(icon + " "))
	if tag != "" {
		tagCol := tag
		if tw := ansi.StringWidth(tagCol); tw < 8 {
			tagCol += strings.Repeat(" ", 8-tw)
		}
		lb.WriteString(st(bgTagColor(item), false).Render(tagCol))
	}
	if desc != "" {
		used := ansi.StringWidth(lb.String())
		rem := maxLeft - used - 2
		if rem > 4 {
			if tag != "" {
				lb.WriteString(st(bg, false).Render("  "))
			}
			descColor := ColorText
			if !item.IsActive() {
				descColor = ColorSubtext0
			}
			lb.WriteString(st(descColor, false).Render(ansi.Truncate(desc, rem, "…")))
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

func bgItemPresentation(item model.HookChild) (icon, iconColor, stateLabel, stateColor string) {
	cat := item.EffectiveCategory()
	switch strings.ToLower(strings.TrimSpace(item.State)) {
	case "done", "completed", "complete", "succeeded", "success":
		label := "done"
		if o := strings.TrimSpace(item.Outcome); o != "" && !strings.EqualFold(o, "done") {
			label = "done · " + truncate(o, 14)
		}
		return "✓", ColorGreen, label, ColorGreen
	case "failed", "failure", "error":
		label := "failed"
		if o := strings.TrimSpace(item.Outcome); o != "" && !strings.EqualFold(o, "failed") {
			label = "failed · " + truncate(o, 14)
		}
		return "✗", ColorRed, label, ColorRed
	case "stopped", "cancelled", "canceled", "killed":
		return "■", ColorOverlay0, "stopped", ColorOverlay0
	default:
		switch cat {
		case "service":
			return "▸", ColorTeal, "service", ColorTeal
		case "watcher":
			return "◉", ColorBlue, "watcher", ColorBlue
		case "schedule":
			return "◷", ColorMauve, "scheduled", ColorMauve
		default:
			return "◐", ColorYellow, "running", ColorYellow
		}
	}
}

func bgTagColor(item model.HookChild) string {
	switch item.EffectiveCategory() {
	case "service":
		return ColorTeal
	case "watcher":
		return ColorBlue
	case "schedule":
		return ColorMauve
	default:
		if item.EffectiveRole() == "subagent" {
			return ColorLavender
		}
		return ColorSubtext0
	}
}

func bgItemTag(item model.HookChild) string {
	switch item.EffectiveCategory() {
	case "service":
		return "svc"
	case "watcher":
		return "watch"
	case "schedule":
		if sched := strings.TrimSpace(item.Schedule); sched != "" {
			return "cron " + truncate(sched, 18)
		}
		return "cron"
	default:
		if item.EffectiveRole() == "subagent" {
			if k := strings.TrimSpace(item.Kind); k != "" && !strings.EqualFold(k, "subagent") && !strings.EqualFold(k, "local_agent") {
				return truncate(k, 16)
			}
			return "subagent"
		}
		return "bg"
	}
}

func bgItemDisplayText(item model.HookChild) string {
	text := strings.TrimSpace(item.Text)
	text = strings.TrimPrefix(text, "subagent: ")
	text = strings.TrimPrefix(text, "subagent done: ")
	text = strings.TrimPrefix(text, "background task: ")
	text = strings.TrimPrefix(text, "background task done: ")
	if text != "" {
		return text
	}
	if cmd := strings.TrimSpace(item.Command); cmd != "" {
		return cmd
	}
	if id := strings.TrimSpace(item.ID); id != "" {
		return id
	}
	return ""
}

func formatBgElapsed(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return "<1s"
	}
	return formatAge(ms)
}

// renderHookDetailBackground renders the ATT-5 / ATT-9 background work sections
// inside the `H` hook detail overlay.
func renderHookDetailBackground(d *model.HookDetail, nowMs int64) []string {
	if d == nil {
		return nil
	}
	var lines []string
	if w := strings.TrimSpace(d.WaitingOn); w != "" {
		lines = append(lines, "waiting on", "  "+w, "")
	}
	items := d.AllBackgroundWork()
	var finiteItems, serviceItems []model.HookChild
	for _, item := range items {
		if item.IsFinite() {
			finiteItems = append(finiteItems, item)
		} else {
			serviceItems = append(serviceItems, item)
		}
	}
	if len(finiteItems) > 0 {
		hasCommand := false
		for _, it := range finiteItems {
			if it.EffectiveRole() != "subagent" {
				hasCommand = true
				break
			}
		}
		heading := fmt.Sprintf("subagents (%d)", len(finiteItems))
		if hasCommand {
			heading = fmt.Sprintf("subagents & finite jobs (%d)", len(finiteItems))
		}
		lines = append(lines, heading)
		for _, c := range finiteItems {
			icon, _, stLabel, _ := bgItemPresentation(c)
			elapsed := formatBgElapsed(c.ElapsedMs(nowMs))
			parts := []string{icon, bgItemTag(c), stLabel}
			if elapsed != "" {
				parts = append(parts, elapsed)
			}
			if c.ID != "" {
				parts = append(parts, c.ID)
			}
			line := "· " + strings.Join(parts, " ")
			if desc := bgItemDisplayText(c); desc != "" {
				line += "  " + desc
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}
	if len(serviceItems) > 0 {
		lines = append(lines, fmt.Sprintf("services, watchers & schedules (%d)", len(serviceItems)))
		for _, c := range serviceItems {
			icon, _, stLabel, _ := bgItemPresentation(c)
			elapsed := formatBgElapsed(c.ElapsedMs(nowMs))
			parts := []string{icon, bgItemTag(c), stLabel}
			if elapsed != "" {
				parts = append(parts, elapsed)
			}
			line := "· " + strings.Join(parts, " ")
			if desc := bgItemDisplayText(c); desc != "" {
				line += "  " + desc
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}
	if len(items) == 0 && d.Background > 0 {
		lines = append(lines, fmt.Sprintf("background tasks: %d", d.Background))
	}
	return lines
}

func formatDetailBackgroundSummary(h *model.HookSummary) string {
	if h == nil {
		return ""
	}
	var parts []string
	if w := strings.TrimSpace(h.WaitingOn); w != "" {
		parts = append(parts, w)
	}
	if h.FiniteActive > 0 {
		noun := "job"
		if h.FiniteActive > 1 {
			noun = "jobs"
		}
		parts = append(parts, fmt.Sprintf("%d active %s", h.FiniteActive, noun))
	} else if h.ChildCount > 0 {
		noun := "sub"
		if h.ChildCount > 1 {
			noun = "subs"
		}
		parts = append(parts, fmt.Sprintf("+%d %s", h.ChildCount, noun))
	}
	if h.ServiceActive > 0 {
		noun := "svc"
		if h.ServiceActive > 1 {
			noun = "svcs"
		}
		parts = append(parts, fmt.Sprintf("%d %s", h.ServiceActive, noun))
	}
	return strings.Join(parts, " · ")
}

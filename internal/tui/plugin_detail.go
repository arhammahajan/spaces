package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/arhammahajan/spaces/internal/model"
)

// hookInlinePillText returns a compact unobtrusive pill for pane rows when
// rich companion telemetry is present. It returns ("", "") when nothing
// worth surfacing exists so existing rows stay byte-identical.
// Priority: rate-limited ("429") > context percent ("ctx NN%") > cost ("$X.XX").
func hookInlinePillText(h *model.HookSummary) (string, string) {
	if h == nil {
		return "", ""
	}
	if h.RateLimited {
		return "429", ColorRed
	}
	if h.ContextPercent > 0 {
		pct := int(h.ContextPercent + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 999 {
			pct = 999
		}
		color := ColorOverlay0
		switch {
		case h.ContextPercent >= 90:
			color = ColorRed
		case h.ContextPercent >= 75:
			color = ColorYellow
		}
		return fmt.Sprintf("ctx %d%%", pct), color
	}
	if h.Usage != nil && h.Usage.CostUSD > 0 {
		if cost := formatCostUSD(h.Usage.CostUSD); cost != "" {
			return cost, ColorOverlay0
		}
	}
	return "", ""
}

// formatHookInlinePill renders the pill with the row's style helper. It
// returns "" when there is nothing to show.
func formatHookInlinePill(h *model.HookSummary, st func(string, bool) lipgloss.Style) string {
	text, color := hookInlinePillText(h)
	if text == "" {
		return ""
	}
	bold := color == ColorRed
	return st(color, bold).Render(text)
}

// formatPluginUsage renders token usage with explicit cache_r/cache_w parts
// plus cost, e.g. "12.4k in · 3.1k out · 1.2k reason · 8.2k cache_r · 2k cache_w · $0.04".
func formatPluginUsage(u *model.HookUsage) string {
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
		parts = append(parts, formatTokenCount(u.CacheReadTokens)+" cache_r")
	}
	if u.CacheWriteTokens > 0 {
		parts = append(parts, formatTokenCount(u.CacheWriteTokens)+" cache_w")
	}
	if cost := formatCostUSD(u.CostUSD); cost != "" {
		parts = append(parts, cost)
	}
	return strings.Join(parts, " · ")
}

// contextPercentFor derives a display percent from explicit ContextPercent or
// from ContextTokens/ContextWindow when only raw counts are present.
func contextPercentFor(h *model.HookSummary) float64 {
	if h == nil {
		return 0
	}
	if h.ContextPercent > 0 {
		return h.ContextPercent
	}
	if h.ContextTokens > 0 && h.ContextWindow > 0 {
		return float64(h.ContextTokens) / float64(h.ContextWindow) * 100
	}
	return 0
}

// hookPluginHeaderLines renders provenance, session context, context/usage,
// rate-limit/error banners, and pending permission. All sections are omitted
// when empty so non-plugin fallback panes stay clean.
func hookPluginHeaderLines(d *model.HookDetail, nowMs int64, maxW int) []string {
	if d == nil {
		return nil
	}
	h := &d.HookSummary
	labelSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorOverlay0))
	sectionSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	errSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRed)).Bold(true)
	warnSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorYellow)).Bold(true)
	dimSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))

	var lines []string

	// Source & plugin provenance.
	hasProv := strings.TrimSpace(h.Source) != "" || strings.TrimSpace(h.PluginName) != "" ||
		strings.TrimSpace(h.HarnessVersion) != "" || h.PID != 0
	if hasProv {
		lines = append(lines, sectionSt.Render(truncate("source & plugin", maxW)))
		if s := strings.TrimSpace(h.Source); s != "" {
			lines = append(lines, labelSt.Render("source   ")+valSt.Render(truncate(s, maxW-9)))
		}
		if n := strings.TrimSpace(h.PluginName); n != "" {
			v := n
			if ver := strings.TrimSpace(h.PluginVersion); ver != "" {
				v += " v" + ver
			}
			lines = append(lines, labelSt.Render("plugin   ")+valSt.Render(truncate(v, maxW-9)))
		}
		if hv := strings.TrimSpace(h.HarnessVersion); hv != "" {
			lines = append(lines, labelSt.Render("harness  ")+valSt.Render(truncate(hv, maxW-9)))
		}
		if h.PID != 0 {
			lines = append(lines, labelSt.Render("pid      ")+valSt.Render(truncate(fmt.Sprintf("%d", h.PID), maxW-9)))
		}
		lines = append(lines, "")
	}

	// Model & session context (model/session/cwd already shown in the dialog
	// header, so only supplement with fields that add information).
	var ctxRows []string
	addRow := func(label, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		ctxRows = append(ctxRows, labelSt.Render(label)+valSt.Render(truncate(strings.TrimSpace(v), maxW-9)))
	}
	addRow("provider ", h.Provider)
	addRow("effort   ", h.Effort)
	addRow("mode     ", h.Mode)
	addRow("sandbox  ", h.SandboxMode)
	addRow("branch   ", h.GitBranch)
	if len(ctxRows) > 0 {
		lines = append(lines, sectionSt.Render(truncate("model & session", maxW)))
		lines = append(lines, ctxRows...)
		lines = append(lines, "")
	}

	// Context window & usage.
	pct := contextPercentFor(h)
	hasCtx := h.ContextTokens > 0 || h.ContextWindow > 0 || pct > 0
	usageStr := formatPluginUsage(h.Usage)
	var counterParts []string
	if h.TurnCount > 0 {
		noun := "turns"
		if h.TurnCount == 1 {
			noun = "turn"
		}
		counterParts = append(counterParts, fmt.Sprintf("%d %s", h.TurnCount, noun))
	}
	if h.CompactionCount > 0 {
		s := fmt.Sprintf("%d compactions", h.CompactionCount)
		if h.CompactionCount == 1 {
			s = "1 compaction"
		}
		if r := strings.TrimSpace(h.LastCompactionReason); r != "" {
			s += " (last: " + r + ")"
		}
		counterParts = append(counterParts, s)
	}
	if h.ToolCallCount > 0 {
		s := fmt.Sprintf("%d tools", h.ToolCallCount)
		if h.ToolCallCount == 1 {
			s = "1 tool"
		}
		if h.ToolErrorCount > 0 {
			s += fmt.Sprintf(" (%d errors)", h.ToolErrorCount)
		}
		counterParts = append(counterParts, s)
	} else if h.ToolErrorCount > 0 {
		counterParts = append(counterParts, fmt.Sprintf("%d tool errors", h.ToolErrorCount))
	}
	if h.RetryCount > 0 {
		noun := "retries"
		if h.RetryCount == 1 {
			noun = "retry"
		}
		counterParts = append(counterParts, fmt.Sprintf("%d %s", h.RetryCount, noun))
	}
	if hasCtx || usageStr != "" || len(counterParts) > 0 {
		lines = append(lines, sectionSt.Render(truncate("context & usage", maxW)))
		if hasCtx {
			var ctxLine string
			switch {
			case h.ContextTokens > 0 && h.ContextWindow > 0:
				ctxLine = fmt.Sprintf("context: %s / %s (%d%%)",
					formatTokenCount(h.ContextTokens), formatTokenCount(h.ContextWindow), int(pct+0.5))
			case h.ContextWindow > 0:
				ctxLine = fmt.Sprintf("context window: %s (%d%%)", formatTokenCount(h.ContextWindow), int(pct+0.5))
			case h.ContextTokens > 0:
				ctxLine = fmt.Sprintf("context: %s (%d%%)", formatTokenCount(h.ContextTokens), int(pct+0.5))
			default:
				ctxLine = fmt.Sprintf("context: %d%%", int(pct+0.5))
			}
			var styled string
			switch {
			case pct >= 90:
				styled = errSt.Render(truncate(ctxLine, maxW))
			case pct >= 75:
				styled = warnSt.Render(truncate(ctxLine, maxW))
			default:
				styled = valSt.Render(truncate(ctxLine, maxW))
			}
			lines = append(lines, styled)
		}
		if usageStr != "" {
			lines = append(lines, valSt.Render(truncate(usageStr, maxW)))
		}
		if len(counterParts) > 0 {
			lines = append(lines, dimSt.Render(truncate(strings.Join(counterParts, " · "), maxW)))
		}
		lines = append(lines, "")
	}

	// Rate limit / error banners.
	if h.RateLimited {
		msg := "rate limited"
		if m := strings.TrimSpace(h.RateLimitMessage); m != "" {
			msg = "rate limited: " + m
		}
		if h.RateLimitResetMs > nowMs {
			msg += fmt.Sprintf(" (resets in %s)", formatAge(h.RateLimitResetMs-nowMs))
		}
		lines = append(lines, warnSt.Render(truncate("⚠ "+msg, maxW)))
	}
	if e := strings.TrimSpace(h.LastError); e != "" {
		lines = append(lines, errSt.Render(truncate("✗ "+e, maxW)))
	}
	if h.RateLimited || strings.TrimSpace(h.LastError) != "" {
		lines = append(lines, "")
	}

	// Pending permission / question.
	if p := h.PendingPermission; p != nil && (strings.TrimSpace(p.Kind) != "" ||
		strings.TrimSpace(p.Tool) != "" || strings.TrimSpace(p.Title) != "" ||
		strings.TrimSpace(p.Description) != "" || len(p.Options) > 0) {
		head := "pending permission"
		if k := strings.TrimSpace(p.Kind); k != "" {
			head += " · " + k
		}
		if t := strings.TrimSpace(p.Tool); t != "" {
			head += " · " + t
		}
		lines = append(lines, sectionSt.Render(truncate(head, maxW)))
		if t := strings.TrimSpace(p.Title); t != "" {
			lines = append(lines, valSt.Render(truncate(t, maxW)))
		}
		if desc := strings.TrimSpace(p.Description); desc != "" {
			for _, ln := range strings.Split(desc, "\n") {
				lines = append(lines, dimSt.Render(truncate(strings.TrimSpace(ln), maxW)))
			}
		}
		if len(p.Options) > 0 {
			var opts []string
			for _, o := range p.Options {
				if strings.TrimSpace(o) != "" {
					opts = append(opts, strings.TrimSpace(o))
				}
			}
			if len(opts) > 0 {
				lines = append(lines, labelSt.Render("options  ")+valSt.Render(truncate(strings.Join(opts, " / "), maxW-9)))
			}
		}
		lines = append(lines, "")
	}

	// Trim trailing blank line.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// hookPluginBodyLines renders active/recent tools, files touched, todos, and
// MCP/LSP sections. Bounded and omitted when empty.
func hookPluginBodyLines(d *model.HookDetail, nowMs int64, maxW int) []string {
	if d == nil {
		return nil
	}
	h := &d.HookSummary
	sectionSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorLavender)).Bold(true)
	valSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorText))
	dimSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorSubtext0))
	errSt := lipgloss.NewStyle().Foreground(lipgloss.Color(ColorRed)).Bold(true)

	var lines []string

	// Active tools (live elapsed).
	if len(h.ActiveTools) > 0 {
		n := len(h.ActiveTools)
		if n > 8 {
			n = 8
		}
		lines = append(lines, sectionSt.Render(truncate(fmt.Sprintf("active tools (%d)", len(h.ActiveTools)), maxW)))
		for _, t := range h.ActiveTools[:n] {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = "tool"
			}
			elapsed := ""
			if t.StartedMs > 0 && nowMs > t.StartedMs {
				if e := formatBgElapsed(nowMs - t.StartedMs); e != "" {
					elapsed = " " + e
				}
			} else if t.DurationMs > 0 {
				if e := formatBgElapsed(t.DurationMs); e != "" {
					elapsed = " " + e
				}
			}
			line := "· " + name + elapsed
			if st := strings.TrimSpace(t.Status); st != "" && !strings.EqualFold(st, "running") {
				line += " " + st
			}
			if inp := strings.TrimSpace(t.Input); inp != "" {
				one := strings.Join(strings.Fields(inp), " ")
				line += "  " + one
			}
			lines = append(lines, valSt.Render(truncate(line, maxW)))
		}
		lines = append(lines, "")
	}

	// Recent tools (bounded to 8, errors highlighted).
	if len(h.RecentTools) > 0 {
		n := len(h.RecentTools)
		if n > 8 {
			n = 8
		}
		lines = append(lines, sectionSt.Render(truncate(fmt.Sprintf("recent tools (%d)", len(h.RecentTools)), maxW)))
		for _, t := range h.RecentTools[:n] {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = "tool"
			}
			line := "· " + name
			if st := strings.TrimSpace(t.Status); st != "" {
				line += " " + st
			}
			if t.DurationMs > 0 {
				if e := formatBgElapsed(t.DurationMs); e != "" {
					line += " " + e
				}
			}
			if e := strings.TrimSpace(t.Error); e != "" {
				line += " ✗ " + e
			}
			styled := truncate(line, maxW)
			if strings.TrimSpace(t.Error) != "" || strings.EqualFold(strings.TrimSpace(t.Status), "failed") ||
				strings.EqualFold(strings.TrimSpace(t.Status), "error") {
				lines = append(lines, errSt.Render(styled))
			} else {
				lines = append(lines, dimSt.Render(styled))
			}
		}
		lines = append(lines, "")
	}

	// Files touched (bounded to 8).
	if len(h.FilesTouched) > 0 {
		n := len(h.FilesTouched)
		if n > 8 {
			n = 8
		}
		lines = append(lines, sectionSt.Render(truncate(fmt.Sprintf("files touched (%d)", len(h.FilesTouched)), maxW)))
		for _, f := range h.FilesTouched[:n] {
			path := strings.TrimSpace(f.Path)
			if path == "" {
				continue
			}
			action := strings.TrimSpace(f.Action)
			if action == "" {
				action = "edit"
			}
			line := "· " + action + " " + path
			if f.Additions > 0 || f.Deletions > 0 {
				line += fmt.Sprintf(" (+%d/-%d)", f.Additions, f.Deletions)
			}
			lines = append(lines, valSt.Render(truncate(line, maxW)))
		}
		lines = append(lines, "")
	}

	// Todos / plan.
	if len(h.Todos) > 0 {
		done := 0
		for _, td := range h.Todos {
			s := strings.ToLower(strings.TrimSpace(td.Status))
			if s == "completed" || s == "done" || s == "success" {
				done++
			}
		}
		lines = append(lines, sectionSt.Render(truncate(fmt.Sprintf("todos %d/%d done", done, len(h.Todos)), maxW)))
		shown := 0
		for _, td := range h.Todos {
			s := strings.ToLower(strings.TrimSpace(td.Status))
			active := s == "in_progress" || s == "in-progress" || s == "active" || s == "working" || s == "started"
			if !active {
				continue
			}
			content := strings.TrimSpace(td.Content)
			if content == "" {
				content = strings.TrimSpace(td.ID)
			}
			if content == "" {
				continue
			}
			lines = append(lines, valSt.Render(truncate("· "+content+" ["+strings.TrimSpace(td.Status)+"]", maxW)))
			shown++
			if shown >= 5 {
				break
			}
		}
		// If nothing is actively in progress, show up to 5 pending items for context.
		if shown == 0 {
			for _, td := range h.Todos {
				s := strings.ToLower(strings.TrimSpace(td.Status))
				if s == "completed" || s == "done" || s == "success" {
					continue
				}
				content := strings.TrimSpace(td.Content)
				if content == "" {
					continue
				}
				lines = append(lines, dimSt.Render(truncate("· "+content+" ["+strings.TrimSpace(td.Status)+"]", maxW)))
				shown++
				if shown >= 5 {
					break
				}
			}
		}
		lines = append(lines, "")
	}

	// MCP & LSP.
	if len(h.MCPServers) > 0 {
		ok, bad := 0, 0
		for _, s := range h.MCPServers {
			st := strings.ToLower(strings.TrimSpace(s.Status))
			if st == "" || st == "connected" || st == "ok" || st == "running" {
				ok++
			} else {
				bad++
			}
		}
		head := fmt.Sprintf("mcp servers (%d)", len(h.MCPServers))
		if bad > 0 {
			head += fmt.Sprintf(" · %d connected · %d errored", ok, bad)
		}
		lines = append(lines, sectionSt.Render(truncate(head, maxW)))
		for _, s := range h.MCPServers {
			name := strings.TrimSpace(s.Name)
			if name == "" {
				name = "server"
			}
			line := "· " + name + " " + strings.TrimSpace(s.Status)
			if s.Tools > 0 {
				line += fmt.Sprintf(" (%d tools)", s.Tools)
			}
			if e := strings.TrimSpace(s.Error); e != "" {
				line += " ✗ " + e
			}
			st := strings.ToLower(strings.TrimSpace(s.Status))
			if strings.TrimSpace(s.Error) != "" || st == "error" || st == "failed" || st == "disconnected" {
				lines = append(lines, errSt.Render(truncate(line, maxW)))
			} else {
				lines = append(lines, dimSt.Render(truncate(line, maxW)))
			}
		}
		lines = append(lines, "")
	}
	if l := h.LSPDiagnostics; l != nil && (l.Errors > 0 || l.Warnings > 0) {
		parts := []string{}
		if l.Errors > 0 {
			noun := "errors"
			if l.Errors == 1 {
				noun = "error"
			}
			parts = append(parts, fmt.Sprintf("%d %s", l.Errors, noun))
		}
		if l.Warnings > 0 {
			noun := "warnings"
			if l.Warnings == 1 {
				noun = "warning"
			}
			parts = append(parts, fmt.Sprintf("%d %s", l.Warnings, noun))
		}
		suffix := ""
		if l.Files > 0 {
			suffix = fmt.Sprintf(" in %d files", l.Files)
		}
		line := "lsp: " + strings.Join(parts, ", ") + suffix
		if l.Errors > 0 {
			lines = append(lines, errSt.Render(truncate(line, maxW)))
		} else {
			lines = append(lines, sectionSt.Render(truncate("lsp", maxW)))
			lines = append(lines, valSt.Render(truncate(line, maxW)))
		}
		lines = append(lines, "")
	}

	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

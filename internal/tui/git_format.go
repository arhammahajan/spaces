package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/arhammahajan/spaces/internal/model"
)

type cellStyleFn func(fg string, bold bool) lipgloss.Style

// renderGitBranchBadge formats the git branch name (with optional worktree
// indicator glyph and rebase marker).
func renderGitBranchBadge(g *model.GitInfo, maxLen int, st cellStyleFn) string {
	if g == nil {
		return ""
	}
	br := strings.TrimSpace(g.Branch)
	wt := strings.TrimSpace(g.WorktreeName)
	if br == "" && !g.IsRebasing && (!g.IsWorktree || wt == "") {
		return ""
	}
	var b strings.Builder
	if g.IsRebasing {
		b.WriteString(st(ColorYellow, true).Render("R"))
		if br != "" || (g.IsWorktree && wt != "") {
			b.WriteString(st(ColorYellow, false).Render(" "))
		}
	}
	if g.IsWorktree {
		b.WriteString(st(ColorMauve, true).Render("⎇ "))
		label := br
		if label == "" {
			label = wt
		}
		if label != "" {
			avail := maxLen - 2
			if avail < 6 {
				avail = 6
			}
			b.WriteString(st(ColorTeal, false).Render(truncate(label, avail)))
		}
		return b.String()
	}
	if br != "" {
		b.WriteString(st(ColorTeal, false).Render(truncate(br, maxLen)))
	}
	return b.String()
}

// renderGitDiffBadge formats committed diff stats (base...HEAD), uncommitted
// working-tree stats (* +X -Y), conflict marker (!), and ahead/behind counts,
// mirroring workmux's visual conventions.
func renderGitDiffBadge(g *model.GitInfo, bg string, st cellStyleFn) string {
	if g == nil {
		return ""
	}
	hasUncommitted := g.UncommittedAdded > 0 || g.UncommittedRemoved > 0 || g.IsDirty
	hasCommitted := g.LinesAdded > 0 || g.LinesRemoved > 0

	var parts []string
	space := st(bg, false).Render(" ")

	if hasCommitted {
		// Dim committed totals slightly when uncommitted changes are also present,
		// matching workmux (historical vs active work).
		var cb strings.Builder
		if g.LinesAdded > 0 {
			s := st(ColorGreen, false)
			if hasUncommitted {
				s = s.Faint(true)
			}
			cb.WriteString(s.Render(fmt.Sprintf("+%d", g.LinesAdded)))
		}
		if g.LinesRemoved > 0 {
			if cb.Len() > 0 {
				cb.WriteString(space)
			}
			s := st(ColorRed, false)
			if hasUncommitted {
				s = s.Faint(true)
			}
			cb.WriteString(s.Render(fmt.Sprintf("-%d", g.LinesRemoved)))
		}
		if cb.Len() > 0 {
			parts = append(parts, cb.String())
		}
	}

	if hasUncommitted {
		var ub strings.Builder
		ub.WriteString(st(ColorYellow, true).Render("*"))
		if g.UncommittedAdded > 0 {
			ub.WriteString(space)
			ub.WriteString(st(ColorGreen, true).Render(fmt.Sprintf("+%d", g.UncommittedAdded)))
		}
		if g.UncommittedRemoved > 0 {
			ub.WriteString(space)
			ub.WriteString(st(ColorRed, true).Render(fmt.Sprintf("-%d", g.UncommittedRemoved)))
		}
		parts = append(parts, ub.String())
	}

	if g.HasConflict {
		parts = append(parts, st(ColorRed, true).Render("!"))
	}
	if g.Ahead > 0 {
		parts = append(parts, st(ColorBlue, false).Render(fmt.Sprintf("↑%d", g.Ahead)))
	}
	if g.Behind > 0 {
		parts = append(parts, st(ColorYellow, false).Render(fmt.Sprintf("↓%d", g.Behind)))
	}

	return strings.Join(parts, space)
}

// renderGitPRBadge formats GitHub PR number + state glyph and CI check badge,
// mirroring workmux's pr_status formatting with safe Unicode glyphs.
func renderGitPRBadge(g *model.GitInfo, bg string, st cellStyleFn) string {
	if g == nil {
		return ""
	}
	showChecks := g.ShouldShowChecks()
	if g.PR == nil && !showChecks {
		return ""
	}

	space := st(bg, false).Render(" ")
	var parts []string

	if pr := g.PR; pr != nil && pr.Number > 0 {
		icon, color := prStateIconColor(pr)
		parts = append(parts, st(color, false).Render(fmt.Sprintf("#%d %s", pr.Number, icon)))
	}

	if showChecks {
		ch := g.Checks
		switch ch.State {
		case model.CheckSuccess:
			parts = append(parts, st(ColorGreen, true).Render("✓"))
		case model.CheckFailure:
			lbl := "✗"
			if ch.Total > 0 {
				lbl = fmt.Sprintf("✗ %d/%d", ch.Passed, ch.Total)
			}
			parts = append(parts, st(ColorRed, true).Render(lbl))
		case model.CheckPending:
			lbl := "◷"
			if ch.Total > 0 {
				lbl = fmt.Sprintf("◷ %d/%d", ch.Passed, ch.Total)
			}
			parts = append(parts, st(ColorYellow, false).Render(lbl))
		}
	}

	return strings.Join(parts, space)
}

func prStateIconColor(pr *model.PRSummary) (icon, color string) {
	if pr.IsDraft {
		return "○", ColorOverlay0
	}
	switch strings.ToUpper(strings.TrimSpace(pr.State)) {
	case "OPEN":
		return "●", ColorGreen
	case "MERGED":
		return "◆", ColorMauve
	case "CLOSED":
		return "×", ColorRed
	default:
		return "?", ColorOverlay0
	}
}

// gitInfoEquivalent reports whether two GitInfo records display the exact same
// inline branch, diff, PR, and CI status. Used by the Spaces popup so child
// panes sharing the parent space's repo/branch do not spam duplicate git badges
// on every row while separate worktrees/repos still display theirs.
func gitInfoEquivalent(a, b *model.GitInfo) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Branch != b.Branch ||
		a.IsWorktree != b.IsWorktree ||
		a.WorktreeName != b.WorktreeName ||
		a.RepoRoot != b.RepoRoot ||
		a.IsRebasing != b.IsRebasing ||
		a.HasConflict != b.HasConflict ||
		a.IsDirty != b.IsDirty ||
		a.LinesAdded != b.LinesAdded ||
		a.LinesRemoved != b.LinesRemoved ||
		a.UncommittedAdded != b.UncommittedAdded ||
		a.UncommittedRemoved != b.UncommittedRemoved ||
		a.Ahead != b.Ahead ||
		a.Behind != b.Behind {
		return false
	}
	if (a.PR == nil) != (b.PR == nil) {
		return false
	}
	if a.PR != nil {
		if a.PR.Number != b.PR.Number || a.PR.State != b.PR.State || a.PR.IsDraft != b.PR.IsDraft {
			return false
		}
	}
	aShow, bShow := a.ShouldShowChecks(), b.ShouldShowChecks()
	if aShow != bShow {
		return false
	}
	if aShow {
		if a.Checks.State != b.Checks.State || a.Checks.Passed != b.Checks.Passed || a.Checks.Total != b.Checks.Total {
			return false
		}
	}
	return true
}

// renderGitDetailStyled builds a styled git/worktree/PR/CI summary for the bottom
// inspector footer in both the Spaces and Inbox popups.
func renderGitDetailStyled(g *model.GitInfo, bg string, st cellStyleFn) string {
	if g == nil {
		return ""
	}
	var parts []string
	sep := st(ColorSurface1, false).Render(" · ")

	if g.IsWorktree {
		wtLabel := strings.TrimSpace(g.WorktreeName)
		if wtLabel == "" {
			wtLabel = strings.TrimSpace(g.Branch)
		}
		if wtLabel != "" {
			wtPart := st(ColorMauve, true).Render("wt " + truncate(wtLabel, 24))
			if repo := strings.TrimSpace(g.RepoName); repo != "" {
				wtPart += st(ColorOverlay0, false).Render(" (" + truncate(repo, 18) + ")")
			}
			parts = append(parts, wtPart)
		}
	}
	if br := renderGitBranchBadge(g, 32, st); br != "" {
		parts = append(parts, br)
	}
	if g.IsWorktree && strings.TrimSpace(g.BaseBranch) != "" && g.BaseBranch != g.Branch {
		parts = append(parts, st(ColorOverlay0, false).Render("base "+truncate(g.BaseBranch, 18)))
	}
	if diff := renderGitDiffBadge(g, bg, st); diff != "" {
		parts = append(parts, diff)
	}
	if g.PR != nil && g.PR.Number > 0 {
		_, color := prStateIconColor(g.PR)
		state := strings.ToLower(g.PR.State)
		if g.PR.IsDraft {
			state = "draft"
		}
		prStr := st(color, false).Render(fmt.Sprintf("PR #%d %s", g.PR.Number, state))
		if t := strings.TrimSpace(g.PR.Title); t != "" {
			prStr += st(ColorSubtext0, false).Render(" " + truncate(t, 34))
		}
		parts = append(parts, prStr)
	}
	if g.ShouldShowChecks() {
		ch := g.Checks
		switch ch.State {
		case model.CheckSuccess:
			parts = append(parts, st(ColorGreen, true).Render("CI ✓"))
		case model.CheckFailure:
			s := "CI ✗"
			if ch.Total > 0 {
				s += fmt.Sprintf(" %d/%d", ch.Passed, ch.Total)
			}
			if ch.FailingName != "" {
				s += " (" + truncate(ch.FailingName, 24) + ")"
			}
			parts = append(parts, st(ColorRed, true).Render(s))
		case model.CheckPending:
			s := "CI ◷"
			if ch.Total > 0 {
				s += fmt.Sprintf(" %d/%d", ch.Passed, ch.Total)
			}
			parts = append(parts, st(ColorYellow, false).Render(s))
		}
	}
	return strings.Join(parts, sep)
}

// formatGitDetailSummary builds a concise plain-text git/worktree/PR/CI summary for the
// bottom detail footer in both the Spaces and Inbox popups.
func formatGitDetailSummary(g *model.GitInfo) string {
	if g == nil {
		return ""
	}
	var parts []string
	if g.IsWorktree {
		wtLabel := strings.TrimSpace(g.WorktreeName)
		if wtLabel == "" {
			wtLabel = strings.TrimSpace(g.Branch)
		}
		if wtLabel != "" {
			s := "wt " + wtLabel
			if repo := strings.TrimSpace(g.RepoName); repo != "" {
				s += " (" + repo + ")"
			}
			parts = append(parts, s)
		}
	}
	if g.Branch != "" {
		b := g.Branch
		if g.IsWorktree {
			b = "⎇ " + b
		}
		if g.IsRebasing {
			b = "R " + b
		}
		parts = append(parts, b)
	}
	var diffParts []string
	if g.LinesAdded > 0 || g.LinesRemoved > 0 {
		if g.LinesAdded > 0 {
			diffParts = append(diffParts, fmt.Sprintf("+%d", g.LinesAdded))
		}
		if g.LinesRemoved > 0 {
			diffParts = append(diffParts, fmt.Sprintf("-%d", g.LinesRemoved))
		}
	}
	if g.UncommittedAdded > 0 || g.UncommittedRemoved > 0 || g.IsDirty {
		u := "*"
		if g.UncommittedAdded > 0 {
			u += fmt.Sprintf(" +%d", g.UncommittedAdded)
		}
		if g.UncommittedRemoved > 0 {
			u += fmt.Sprintf(" -%d", g.UncommittedRemoved)
		}
		diffParts = append(diffParts, u)
	}
	if g.Ahead > 0 {
		diffParts = append(diffParts, fmt.Sprintf("↑%d", g.Ahead))
	}
	if g.Behind > 0 {
		diffParts = append(diffParts, fmt.Sprintf("↓%d", g.Behind))
	}
	if len(diffParts) > 0 {
		parts = append(parts, strings.Join(diffParts, " "))
	}
	if g.PR != nil && g.PR.Number > 0 {
		state := strings.ToLower(g.PR.State)
		if g.PR.IsDraft {
			state = "draft"
		}
		prStr := fmt.Sprintf("PR #%d %s", g.PR.Number, state)
		if strings.TrimSpace(g.PR.Title) != "" {
			prStr += " " + truncate(strings.TrimSpace(g.PR.Title), 32)
		}
		parts = append(parts, prStr)
	}
	if g.ShouldShowChecks() {
		ch := g.Checks
		switch ch.State {
		case model.CheckSuccess:
			parts = append(parts, "CI ✓")
		case model.CheckFailure:
			s := "CI ✗"
			if ch.Total > 0 {
				s += fmt.Sprintf(" %d/%d", ch.Passed, ch.Total)
			}
			if ch.FailingName != "" {
				s += " (" + truncate(ch.FailingName, 24) + ")"
			}
			parts = append(parts, s)
		case model.CheckPending:
			s := "CI ◷"
			if ch.Total > 0 {
				s += fmt.Sprintf(" %d/%d", ch.Passed, ch.Total)
			}
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "  ")
}

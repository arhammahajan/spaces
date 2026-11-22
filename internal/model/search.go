package model

import (
	"fmt"
	"sort"
	"strings"
)

// SpaceScore ranks a space against a query, lower is better.
// It mirrors zellij-spaces ranking: exact name (0) < name prefix (5) <
// name substring (10) < cwd substring (20) < git branch/PR (22) < best pane score.
// The second return value is false when nothing matches.
func SpaceScore(space Space, query string) (int, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0, true
	}
	name := strings.ToLower(space.Name)
	switch {
	case name == q:
		return 0, true
	case strings.HasPrefix(name, q):
		return 5, true
	case strings.Contains(name, q):
		return 10, true
	}
	if cwd := strings.ToLower(space.CWD); cwd != "" && strings.Contains(cwd, q) {
		return 20, true
	}
	if gitMatchesQuery(space.Git, q) {
		return 22, true
	}
	best := -1
	for i := range space.Panes {
		if score, ok := PaneScore(space.Panes[i], q); ok {
			if best == -1 || score < best {
				best = score
			}
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// PaneScore ranks a pane against a query, lower is better.
// It mirrors zellij-spaces ranking: exact app (20) < app prefix (25) <
// exact window name (30) < window prefix (35) < title (40) < git branch (42) <
// app substring (45) < window substring (50) < text (55) < git PR (56) <
// hook identity (58) < cwd (60) < status label (65).
// The second return value is false when nothing matches.
func PaneScore(pane Pane, query string) (int, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0, true
	}
	app := strings.ToLower(pane.App)
	profile := strings.ToLower(pane.Profile)
	title := strings.ToLower(pane.Title)
	convTitle := strings.ToLower(pane.ConversationTitle)
	screenTitle := strings.ToLower(pane.ScreenTitle)
	tab := strings.ToLower(pane.WindowName)
	text := strings.ToLower(pane.Text)
	cwd := strings.ToLower(pane.CWD)
	if app == q {
		return 20, true
	}
	if app != "" && strings.HasPrefix(app, q) {
		return 25, true
	}
	if tab == q {
		return 30, true
	}
	if tab != "" && strings.HasPrefix(tab, q) {
		return 35, true
	}
	if (convTitle != "" && strings.Contains(convTitle, q)) ||
		(title != "" && strings.Contains(title, q)) ||
		(screenTitle != "" && strings.Contains(screenTitle, q)) {
		return 40, true
	}
	if pane.Git != nil {
		if pane.Git.Branch != "" && strings.Contains(strings.ToLower(pane.Git.Branch), q) {
			return 42, true
		}
		if pane.Git.IsWorktree {
			if q == "worktree" || q == "wt" ||
				(pane.Git.WorktreeName != "" && strings.Contains(strings.ToLower(pane.Git.WorktreeName), q)) ||
				(pane.Git.RepoName != "" && strings.Contains(strings.ToLower(pane.Git.RepoName), q)) {
				return 43, true
			}
		}
	}
	if app != "" && strings.Contains(app, q) {
		return 45, true
	}
	if profile != "" && strings.Contains(profile, q) {
		return 46, true
	}
	if tab != "" && strings.Contains(tab, q) {
		return 50, true
	}
	if text != "" && strings.Contains(text, q) {
		return 55, true
	}
	if gitPRMatchesQuery(pane.Git, q) {
		return 56, true
	}
	if hookText(pane.Hook) != "" && strings.Contains(hookText(pane.Hook), q) {
		return 58, true
	}
	if cwd != "" && strings.Contains(cwd, q) {
		return 60, true
	}
	if strings.Contains(strings.ToLower(pane.Status.Label()), q) {
		return 65, true
	}
	return 0, false
}

func gitMatchesQuery(g *GitInfo, q string) bool {
	if g == nil {
		return false
	}
	if g.Branch != "" && strings.Contains(strings.ToLower(g.Branch), q) {
		return true
	}
	if g.IsWorktree {
		if q == "worktree" || q == "wt" {
			return true
		}
		if g.WorktreeName != "" && strings.Contains(strings.ToLower(g.WorktreeName), q) {
			return true
		}
		if g.RepoName != "" && strings.Contains(strings.ToLower(g.RepoName), q) {
			return true
		}
	}
	return gitPRMatchesQuery(g, q)
}

func gitPRMatchesQuery(g *GitInfo, q string) bool {
	if g == nil || g.PR == nil {
		return false
	}
	if g.PR.Number > 0 && strings.Contains(fmt.Sprintf("#%d", g.PR.Number), q) {
		return true
	}
	if g.PR.Title != "" && strings.Contains(strings.ToLower(g.PR.Title), q) {
		return true
	}
	return false
}

// hookText renders the searchable hook identity: title, model, session, event,
// kind, waiting_on, and background work descriptions/commands.
func hookText(hook *HookSummary) string {
	if hook == nil {
		return ""
	}
	parts := []string{
		hook.Title, hook.Model, hook.SessionID, hook.Event, hook.Kind, hook.WaitingOn,
	}
	for _, item := range hook.AllBackgroundWork() {
		parts = append(parts, item.Kind, item.Role, item.Category, item.Command, item.Text, item.State, item.Outcome)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// SortSpaces orders spaces: current first, then attention urgency, then
// case-insensitive name.
func SortSpaces(spaces []Space) {
	sort.SliceStable(spaces, func(i, j int) bool {
		if spaces[i].IsCurrent != spaces[j].IsCurrent {
			return spaces[i].IsCurrent
		}
		if spaces[i].Status.Priority() != spaces[j].Status.Priority() {
			return spaces[i].Status.Priority() < spaces[j].Status.Priority()
		}
		ni, nj := strings.ToLower(spaces[i].Name), strings.ToLower(spaces[j].Name)
		if ni != nj {
			return ni < nj
		}
		return spaces[i].Name < spaces[j].Name
	})
}

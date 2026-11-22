package model

import (
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/tmux"
)

// BuildSpaces maps a tmux ServerSnapshot into sorted Spaces.
//
// Pane status comes from the per-pane `@spaces_state` option when set; when
// empty it falls back to tmux native window flags (bell/activity/silence).
// Pane identity, hook-store lookups, and text/age baselines mirror the
// zellij-spaces model so the arbitrator can recompute idempotently.
func BuildSpaces(snap tmux.ServerSnapshot, nowMs int64) []Space {
	windowsBySession := make(map[string][]tmux.Window)
	for _, w := range snap.Windows {
		windowsBySession[w.SessionName] = append(windowsBySession[w.SessionName], w)
	}
	panesBySession := make(map[string][]tmux.Pane)
	for _, p := range snap.Panes {
		panesBySession[p.SessionName] = append(panesBySession[p.SessionName], p)
	}

	spaces := make([]Space, 0, len(snap.Sessions))
	for _, s := range snap.Sessions {
		space := Space{
			Name:             s.Name,
			ID:               s.ID,
			IsCurrent:        s.Name == snap.CurrentSession,
			ConnectedClients: s.Attached,
			CreatedAt:        s.CreatedAt,
			WorktreePath:     s.WorktreePath,
			WorktreeName:     s.WorktreeName,
			WorktreeBase:     s.WorktreeBase,
			WorktreeManaged:  s.WorktreeManaged,
		}
		for _, w := range windowsBySession[s.Name] {
			space.Windows = append(space.Windows, Window{
				ID:              w.ID,
				Index:           w.Index,
				Name:            w.Name,
				Active:          w.Active,
				Last:            w.Last,
				Zoomed:          w.Zoomed,
				Bell:            w.Bell,
				Activity:        w.Activity,
				Silence:         w.Silence,
				Width:           w.Width,
				Height:          w.Height,
				ActivityAt:      w.ActivityAt,
				Layout:          w.Layout,
				WorktreePath:    w.WorktreePath,
				WorktreeName:    w.WorktreeName,
				WorktreeBase:    w.WorktreeBase,
				WorktreeManaged: w.WorktreeManaged,
			})
		}
		sort.SliceStable(space.Windows, func(i, j int) bool {
			return space.Windows[i].Index < space.Windows[j].Index
		})
		for _, p := range panesBySession[s.Name] {
			space.Panes = append(space.Panes, buildPane(p, nowMs))
		}
		sortPanes(space.Panes)
		for _, p := range space.Panes {
			space.Counts.Add(p.Status)
		}
		space.Status = space.Counts.Rollup()
		space.CWD = spaceCWD(space.Panes)
		spaces = append(spaces, space)
	}
	SortSpaces(spaces)
	return spaces
}

// DetectDescendantAgentFn inspects descendant processes of a pane's PID to
// identify harnesses running under generic runtimes (node, bun, python, etc.)
// or versioned binaries (e.g. "2.1.76"). Overridden in tests.
var DetectDescendantAgentFn = detectDescendantAgent

// InspectPaneProcessFn inspects live descendant processes of a pane's PID to
// capture a readable foreground command line and descendant process count.
// Overridden in tests.
var InspectPaneProcessFn = inspectPaneProcess

// buildPane maps one tmux pane to a model Pane, seeding Base* baselines.
func buildPane(p tmux.Pane, nowMs int64) Pane {
	returned := shellReturnedFromAgent(p)
	var (
		status        Status
		ageMs         int64
		text          string
		kind          string
		source        string
		convTitle     string
		resumeSession string
		profile       string
		hasMeta       bool
		descAgent     string
	)
	cmd := CommandName(p.CurrentCommand)
	if !returned && p.PID > 0 && isRuntimeOrVersionCommand(cmd) && DetectDescendantAgentFn != nil {
		descAgent = DetectDescendantAgentFn(p.PID)
	}
	staleOtherAgentMeta := false
	if descAgent != "" && strings.TrimSpace(p.SpacesApp) != "" {
		if canonicalApp(p.SpacesApp) != canonicalApp(descAgent) {
			staleOtherAgentMeta = true
		}
	}
	if returned || staleOtherAgentMeta {
		status = nativeFallbackStatus(p)
	} else {
		status = mapSpacesState(p.SpacesState)
		if strings.TrimSpace(p.SpacesState) == "" {
			status = nativeFallbackStatus(p)
		}
		if p.SpacesTsMs > 0 && nowMs >= p.SpacesTsMs {
			ageMs = nowMs - p.SpacesTsMs
		}
		text = p.SpacesMsg
		kind = p.SpacesKind
		source = p.SpacesSource
		convTitle = strings.TrimSpace(p.SpacesTitle)
		resumeSession = strings.TrimSpace(p.ResumeSession)
		profile = strings.TrimSpace(p.ResumeProfile)
		hasMeta = strings.TrimSpace(p.SpacesApp) != "" || strings.TrimSpace(p.SpacesState) != ""
		if strings.TrimSpace(p.SpacesState) != "" && isStaleBaseStatus(status, kind, source, ageMs) {
			status = nativeFallbackStatus(p)
			text = ""
			kind = ""
			source = ""
			ageMs = 0
		}
	}
	var fgCmd string
	var childProcs int
	if !p.Dead && p.PID > 0 && InspectPaneProcessFn != nil {
		fgCmd, childProcs = InspectPaneProcessFn(p.PID)
	}
	pane := Pane{
		ID:                p.ID,
		NumericID:         p.NumericID,
		PaneIndex:         p.PaneIndex,
		WindowID:          p.WindowID,
		WindowIndex:       p.WindowIndex,
		WindowName:        p.WindowName,
		Title:             p.Title,
		ConversationTitle: convTitle,
		BaseConvTitle:     convTitle,
		ResumeSession:     resumeSession,
		Profile:           profile,
		Command:           p.CurrentCommand,
		StartCommand:      p.StartCommand,
		ForegroundCmd:     fgCmd,
		ChildProcessCount: childProcs,
		App:               resolveApp(p, returned, descAgent),
		Status:            status,
		BaseStatus:        status,
		Text:              text,
		BaseText:          text,
		Kind:              kind,
		Source:            source,
		CWD:               p.CurrentPath,
		AgeMs:             ageMs,
		BaseAgeMs:         ageMs,
		Width:             p.Width,
		Height:            p.Height,
		IsActive:          p.Active,
		WindowActive:      p.WindowActive,
		InMode:            p.InMode,
		Synchronized:      p.Synchronized,
		IsDead:            p.Dead,
		DeadStatus:        p.DeadStatus,
		HasSpacesMeta:     hasMeta,
		ReturnedToShell:   returned,
		PID:               p.PID,
	}
	return pane
}

// isStaleBaseStatus reports whether a timestamped @spaces_state has exceeded
// its freshness TTL (matching hook TTLs, or doneTTLMs for screen-synthesized
// done), allowing hooks or the screen reader to take over instead of pinning
// stale states forever.
func isStaleBaseStatus(status Status, kind, source string, ageMs int64) bool {
	if ageMs <= 0 {
		return false
	}
	switch status {
	case StatusWorking:
		return ageMs > hookWorkingStaleMs
	case StatusDone:
		if strings.EqualFold(strings.TrimSpace(source), "screen") {
			return ageMs > doneTTLMs
		}
		return ageMs > hookDoneTTLMs
	case StatusFailed:
		return ageMs > hookFailedTTLMs
	case StatusNeedsYou:
		if strings.EqualFold(strings.TrimSpace(kind), "auth") {
			return ageMs > hookAuthTTLMs
		}
		return ageMs > hookBlockTTLMs
	default:
		return false
	}
}

// ProcIsLiveFn reports whether a pane's PID exists in the OS process table so
// buildPane can check for live descendant processes when the current command is
// a generic runtime wrapper (such as `node`) without breaking unit tests that
// pass synthetic PIDs.
var ProcIsLiveFn = procIsLive

// shellReturnedFromAgent reports whether a pane previously ran an agent (which
// left @spaces_app/@spaces_state behind) but has since exited back to an
// interactive shell or generic runtime wrapper with no live agent process.
func shellReturnedFromAgent(p tmux.Pane) bool {
	cmd := CommandName(p.CurrentCommand)
	app := strings.TrimSpace(p.SpacesApp)
	if app == "" || isShellApp(app) {
		return false
	}
	hasLiveDescendantAgent := func() bool {
		return p.PID > 0 && DetectDescendantAgentFn != nil && DetectDescendantAgentFn(p.PID) != ""
	}
	if IsShell(cmd) {
		src := strings.ToLower(strings.TrimSpace(p.SpacesSource))
		if src == "osc7501" || src == "hook" {
			return !hasLiveDescendantAgent()
		}
		if p.PID > 0 && IsKnownAgent(app) {
			return !hasLiveDescendantAgent()
		}
		return false
	}
	// If the foreground command is a generic runtime or wrapper (e.g. "node",
	// "harness") rather than the agent binary itself, and the pane's PID is live
	// in /proc with no live descendant agent process, the agent has exited.
	if isRuntimeOrVersionCommand(cmd) && !IsKnownAgent(cmd) && p.PID > 0 && ProcIsLiveFn != nil && ProcIsLiveFn(p.PID) {
		if !hasLiveDescendantAgent() {
			return true
		}
	}
	return false
}

// isRuntimeOrVersionCommand reports whether cmd is a generic runtime wrapper
// or bare version string (such as Claude Code's "2.1.76" binary on Linux)
// whose real harness name should be resolved from the pane's process tree.
func isRuntimeOrVersionCommand(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	switch c {
	case "node", "nodejs", "bun", "deno", "python", "python3", "uv", "ruby",
		"npx", "pnpm", "yarn", "harness", "spaces", "tmux-spaces":
		return true
	}
	return agent.DefaultRegistry().MatchesBareVersionCommand(c)
}

// mapSpacesState maps a `@spaces_state` value to a Status.
// Unknown values degrade to Idle.
func mapSpacesState(state string) Status {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "needs_you", "needs-you", "needsyou", "blocked":
		return StatusNeedsYou
	case "failed", "failure", "error":
		return StatusFailed
	case "done", "complete", "completed", "success":
		return StatusDone
	case "working", "busy", "running":
		return StatusWorking
	case "quiet":
		return StatusQuiet
	case "new_output", "new-output", "activity":
		return StatusNewOutput
	case "idle", "":
		return StatusIdle
	default:
		return StatusIdle
	}
}

// nativeFallbackStatus derives a Status from tmux window flags when no
// `@spaces_state` is set. Bell/activity only count while the pane is not
// active, mirroring "unseen alert" semantics.
func nativeFallbackStatus(p tmux.Pane) Status {
	if p.WindowBell && !p.Active {
		return StatusNeedsYou
	}
	if p.WindowActivity && !p.Active {
		return StatusNewOutput
	}
	if p.WindowSilence {
		return StatusQuiet
	}
	return StatusIdle
}

// resolveApp resolves the display app name:
//  1. When the foreground command is a generic runtime or launcher wrapper
//     (such as `harness`, `node`, `python`, or a versioned binary like `2.1.76`),
//     a live descendant agent process in the pane's process tree is authoritative
//     over stale `@spaces_app` options left by a previous agent in the same pane.
//  2. Otherwise explicit `@spaces_app` wins (unless the pane returned to a shell
//     after an agent exited), followed by the pane's current command, then "terminal".
func resolveApp(p tmux.Pane, shellReturned bool, descAgent string) string {
	cmd := CommandName(p.CurrentCommand)
	if !shellReturned && descAgent != "" {
		if app := strings.TrimSpace(p.SpacesApp); app != "" &&
			canonicalApp(app) == canonicalApp(descAgent) {
			if strings.EqualFold(app, NormalizeAgentCommand(app)) {
				return app
			}
		}
		return displayAgentApp(descAgent)
	}
	if !shellReturned {
		if app := strings.TrimSpace(p.SpacesApp); app != "" {
			if IsShell(app) {
				return "shell"
			}
			if norm := NormalizeAgentCommand(app); IsKnownAgent(norm) {
				// Preserve canonical labels like "claude-code", but collapse
				// versioned binary names like "muse-bin-1.4.24-R9131.1" -> "muse".
				if strings.EqualFold(app, norm) {
					return app
				}
				return norm
			}
			return app
		}
	}
	if cmd != "" {
		if IsShell(cmd) {
			return "shell"
		}
		if norm := NormalizeAgentCommand(cmd); IsKnownAgent(norm) {
			return displayAgentApp(norm)
		}
		if descAgent != "" {
			return displayAgentApp(descAgent)
		}
		return cmd
	}
	return "terminal"
}

func displayAgentApp(app string) string {
	return agent.DefaultRegistry().DisplayApp(app)
}

// spaceCWD prefers the active pane's CWD, then the first pane with one.
func spaceCWD(panes []Pane) string {
	for _, p := range panes {
		if p.IsActive && p.CWD != "" {
			return p.CWD
		}
	}
	for _, p := range panes {
		if p.CWD != "" {
			return p.CWD
		}
	}
	return ""
}

// sortPanes orders panes by status urgency, window index, then numeric ID.
func sortPanes(panes []Pane) {
	sort.SliceStable(panes, func(i, j int) bool {
		if panes[i].Status.Priority() != panes[j].Status.Priority() {
			return panes[i].Status.Priority() < panes[j].Status.Priority()
		}
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		return panes[i].NumericID < panes[j].NumericID
	})
}

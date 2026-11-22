package resurrect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// TmuxRestorer is the minimal tmux mutation surface needed to recreate a
// SpaceSnapshot. *tmux.Client implements it directly.
type TmuxRestorer interface {
	NewSessionDetached(ctx context.Context, name, cwd, windowName string, command ...string) error
	NewWindow(ctx context.Context, session, cwd, name string, command ...string) error
	SplitWindow(ctx context.Context, target, cwd string, command ...string) error
	SelectLayout(ctx context.Context, target, layout string) error
	SelectWindow(ctx context.Context, target string) error
	SelectPane(ctx context.Context, target string) error
	SwitchToSpace(ctx context.Context, session string) error
}

// tmuxRichRestorer is an optional extension of TmuxRestorer (implemented by
// *tmux.Client) that returns exact tmux window/pane IDs and supports restoring
// initial window geometry, worktree options, and pane zoom state without
// depending on tmux's base-index or pane-base-index settings.
type tmuxRichRestorer interface {
	NewSessionDetachedWithID(ctx context.Context, name, cwd, windowName string, width, height int, command ...string) (winID, paneID string, err error)
	NewWindowDetachedWithID(ctx context.Context, session, cwd, name string, command ...string) (winID, paneID string, err error)
	SplitWindowWithID(ctx context.Context, target, cwd string, command ...string) (paneID string, err error)
	SetSessionOption(ctx context.Context, session, option, value string) error
	SetWindowOption(ctx context.Context, windowID, option, value string) error
	ResizePaneZoom(ctx context.Context, target string) error
}

// RestoreOptions configures how a SpaceSnapshot is recreated.
type RestoreOptions struct {
	// SelfPath is the path to tmux-spaces (used to wrap resumed agents in __shim).
	SelfPath string
	// Switch switches/attaches the client to the restored space when true.
	Switch bool
	// HarnessBinary is the optional path/name of the `harness` CLI used to
	// launch profiled agents (defaults to "harness").
	HarnessBinary string
	// SpacesBinary is deprecated and unused; kept for backwards compatibility.
	SpacesBinary func() []string
	// ResolveSession resolves an unclaimed session/conversation ID for
	// (agent, cwd, profile, title) while excluding already-claimed IDs.
	// Returns (id, ok) where ok=false means resolution was unavailable.
	// Defaults to querying native plugin conversation stores via
	// agent.DefaultRegistry().FindUnclaimedConversation.
	ResolveSession agent.SessionLookupFunc
	// ResolveAntigravity resolves an unclaimed Antigravity conversation ID for
	// (cwd, profile, title) while excluding already-claimed IDs. Kept for
	// backwards compatibility when ResolveSession is nil.
	ResolveAntigravity func(ctx context.Context, cwd, profile, title string, exclude map[string]bool) (string, bool)
}

// safeRestoreCommands is the allowlist of non-agent interactive TUIs safe to
// restart automatically on restore (mirroring tmux-resurrect's default list).
// Arbitrary shell commands are NEVER re-executed automatically for safety.
var safeRestoreCommands = map[string]bool{
	"nvim":    true,
	"vim":     true,
	"vi":      true,
	"nano":    true,
	"emacs":   true,
	"lazygit": true,
	"tig":     true,
	"htop":    true,
	"btop":    true,
	"top":     true,
	"ranger":  true,
	"yazi":    true,
	"k9s":     true,
	"man":     true,
	"less":    true,
}

// BuildPaneCommand decides what command (if any) to run when recreating pane p:
//   - AI agent -> `tmux-spaces __shim [--profile <profile>] <agent> <cmd...> [native resume args...]`
//   - Safe interactive TUI (nvim, lazygit, btop, ...) -> restarts the command in CWD.
//   - Plain shell or arbitrary command -> returns nil (clean login shell in CWD).
func BuildPaneCommand(p PaneSnapshot, opts RestoreOptions) []string {
	return buildPaneCommand(p, opts, false)
}

func appendResumeArgs(cmd []string, agentName, sessionID string, skipResume bool) []string {
	out := append([]string(nil), cmd...)
	sid := strings.TrimSpace(sessionID)
	if skipResume || sid == "" {
		return out
	}
	if plugin, ok := agent.DefaultRegistry().ResolveRuntime(agentName); ok {
		if plugin.SessionResolver().ExtractSessionIDFromArgs(out) != "" {
			return out
		}
	}
	resumeArgs := agent.DefaultRegistry().ResumeArgs(agentName, sid)
	if len(resumeArgs) == 0 {
		return out
	}
	return append(out, resumeArgs...)
}

func buildPaneCommand(p PaneSnapshot, opts RestoreOptions, skipResume bool) []string {
	self := opts.SelfPath
	if self == "" {
		self = "tmux-spaces"
	}

	if p.Agent != "" {
		var resumeArgs []string
		if !skipResume && strings.TrimSpace(p.SessionID) != "" {
			resumeArgs = agent.DefaultRegistry().ResumeArgs(p.Agent, p.SessionID)
		}
		harnessBin := opts.HarnessBinary
		if harnessBin == "" {
			harnessBin = "harness"
		}

		// Case 1: Harness launcher (e.g. `harness agy profile saucerf166`).
		if len(p.FullCommand) > 0 && isHarnessLauncher(p.FullCommand) {
			cmdWithResume := appendResumeArgs(p.FullCommand, p.Agent, p.SessionID, skipResume)
			cmd := []string{self, "__shim"}
			if p.Profile != "" {
				cmd = append(cmd, "--profile", p.Profile)
			}
			cmd = append(cmd, p.Agent)
			return append(cmd, cmdWithResume...)
		}

		// Case 2: Profiled agent pane.
		if p.Profile != "" {
			cmd := []string{self, "__shim", "--profile", p.Profile, p.Agent, harnessBin, p.Agent, "profile", p.Profile}
			return append(cmd, resumeArgs...)
		}

		// Case 3: Direct agent invocation captured in FullCommand.
		if len(p.FullCommand) > 0 && CanonicalAgent(p.FullCommand[0]) == p.Agent {
			cmdWithResume := appendResumeArgs(p.FullCommand, p.Agent, p.SessionID, skipResume)
			return append([]string{self, "__shim", p.Agent}, cmdWithResume...)
		}

		// Case 4: Fallback to plugin binary name.
		bin := agent.DefaultRegistry().BinaryName(p.Agent)
		if bin == "" {
			bin = p.Agent
		}
		cmd := []string{self, "__shim", p.Agent, bin}
		return append(cmd, resumeArgs...)
	}

	// Non-agent panes: only restore allowlisted interactive TUIs.
	if len(p.FullCommand) > 0 {
		base := strings.ToLower(filepath.Base(p.FullCommand[0]))
		if safeRestoreCommands[base] {
			return append([]string(nil), p.FullCommand...)
		}
	}
	return nil
}

func isHarnessLauncher(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	return filepath.Base(argv[0]) == "harness" && argv[1] != "spaces"
}

// resolveRestoreCWD resolves a pane or space CWD for resurrection when the
// saved directory no longer exists on disk. It prefers the linked worktree root
// (wtPath) when that directory still exists so worktree context is preserved,
// falling back to mainRoot only when the worktree root itself is gone, and
// otherwise returning cwd unchanged.
func resolveRestoreCWD(cwd, wtPath, mainRoot string) string {
	if cwd == "" {
		return cwd
	}
	if st, err := os.Stat(cwd); err == nil && st.IsDir() {
		return cwd
	}
	if wtPath != "" {
		if st, err := os.Stat(wtPath); err == nil && st.IsDir() {
			return wtPath
		}
	}
	if mainRoot != "" {
		if st, err := os.Stat(mainRoot); err == nil && st.IsDir() {
			return mainRoot
		}
	}
	return cwd
}

// Restore recreates a saved SpaceSnapshot on the tmux server, applying window
// layouts, CWDs, active pane/window selection, worktree metadata, zoom state,
// and AI agent conversation resume.
func Restore(ctx context.Context, r TmuxRestorer, snap SpaceSnapshot, opts RestoreOptions) error {
	if strings.TrimSpace(snap.Name) == "" {
		return fmt.Errorf("resurrect: snapshot has empty name")
	}
	rich, hasRich := r.(tmuxRichRestorer)
	if len(snap.Windows) == 0 {
		cwd := resolveRestoreCWD(snap.CWD, snap.WorktreePath, snap.MainRepoRoot)
		if err := r.NewSessionDetached(ctx, snap.Name, cwd, ""); err != nil {
			return err
		}
		if hasRich {
			restoreSessionWorktreeOptions(ctx, rich, snap)
		}
		if opts.Switch {
			return r.SwitchToSpace(ctx, snap.Name)
		}
		return nil
	}

	assigned, skipResume := resolveAgentSessions(ctx, snap, opts)

	activeWinTarget := ""
	for wi, win := range snap.Windows {
		winTarget := fmt.Sprintf("%s:%d", snap.Name, wi+1)
		if win.Index > 0 {
			winTarget = fmt.Sprintf("%s:%d", snap.Name, win.Index)
		}
		isActiveWin := win.Active || (snap.ActiveWindowIndex > 0 && win.Index == snap.ActiveWindowIndex)

		var firstPane PaneSnapshot
		if len(win.Panes) > 0 {
			firstPane = win.Panes[0]
		} else {
			firstPane = PaneSnapshot{CWD: snap.CWD}
		}
		firstKey := paneResolveKey(wi, 0)
		if sid := assigned[firstKey]; sid != "" {
			firstPane.SessionID = sid
		}
		winWT := win.WorktreePath
		if winWT == "" {
			winWT = snap.WorktreePath
		}
		firstCWD := firstPane.CWD
		if firstCWD == "" {
			firstCWD = snap.CWD
		}
		firstCWD = resolveRestoreCWD(firstCWD, winWT, snap.MainRepoRoot)
		firstCmd := buildPaneCommand(firstPane, opts, skipResume[firstKey])

		var createdPaneIDs []string
		if hasRich {
			var (
				winID  string
				paneID string
				err    error
			)
			if wi == 0 {
				winID, paneID, err = rich.NewSessionDetachedWithID(ctx, snap.Name, firstCWD, win.Name, win.Width, win.Height, firstCmd...)
				if err != nil {
					return fmt.Errorf("resurrect: create session %s: %w", snap.Name, err)
				}
				restoreSessionWorktreeOptions(ctx, rich, snap)
			} else {
				winID, paneID, err = rich.NewWindowDetachedWithID(ctx, snap.Name, firstCWD, win.Name, firstCmd...)
				if err != nil {
					return fmt.Errorf("resurrect: create window %s:%s: %w", snap.Name, win.Name, err)
				}
			}
			if winID != "" {
				winTarget = winID
			}
			restoreWindowWorktreeOptions(ctx, rich, winTarget, win)
			createdPaneIDs = append(createdPaneIDs, paneID)
		} else {
			if wi == 0 {
				if err := r.NewSessionDetached(ctx, snap.Name, firstCWD, win.Name, firstCmd...); err != nil {
					return fmt.Errorf("resurrect: create session %s: %w", snap.Name, err)
				}
			} else {
				if err := r.NewWindow(ctx, snap.Name, firstCWD, win.Name, firstCmd...); err != nil {
					return fmt.Errorf("resurrect: create window %s:%s: %w", snap.Name, win.Name, err)
				}
			}
		}

		if isActiveWin {
			activeWinTarget = winTarget
		}

		// Recreate additional panes in this window.
		lastPaneID := ""
		if len(createdPaneIDs) > 0 {
			lastPaneID = createdPaneIDs[0]
		}
		for pi := 1; pi < len(win.Panes); pi++ {
			p := win.Panes[pi]
			pKey := paneResolveKey(wi, pi)
			if sid := assigned[pKey]; sid != "" {
				p.SessionID = sid
			}
			pCWD := p.CWD
			if pCWD == "" {
				pCWD = firstCWD
			}
			pCWD = resolveRestoreCWD(pCWD, winWT, snap.MainRepoRoot)
			pCmd := buildPaneCommand(p, opts, skipResume[pKey])
			if hasRich {
				splitTarget := winTarget
				if lastPaneID != "" {
					splitTarget = lastPaneID
				}
				paneID, err := rich.SplitWindowWithID(ctx, splitTarget, pCWD, pCmd...)
				if err != nil {
					return fmt.Errorf("resurrect: split pane %d in %s: %w", pi, winTarget, err)
				}
				createdPaneIDs = append(createdPaneIDs, paneID)
				if paneID != "" {
					lastPaneID = paneID
				}
				// Rebalance intermediate splits when recreating 3+ panes so
				// subsequent splits never fail with "no space for new pane"
				// before the final saved layout is applied.
				if len(win.Panes) > 2 && pi < len(win.Panes)-1 {
					_ = r.SelectLayout(ctx, winTarget, "tiled")
				}
			} else {
				if err := r.SplitWindow(ctx, winTarget, pCWD, pCmd...); err != nil {
					return fmt.Errorf("resurrect: split pane %d in %s: %w", pi, winTarget, err)
				}
			}
		}

		// Apply saved tmux layout string so split proportions match.
		if win.Layout != "" {
			_ = r.SelectLayout(ctx, winTarget, win.Layout)
		}

		// Restore active pane within this window.
		activePaneTarget := ""
		for pi, p := range win.Panes {
			if p.Active {
				if hasRich && pi < len(createdPaneIDs) && createdPaneIDs[pi] != "" {
					activePaneTarget = createdPaneIDs[pi]
				} else {
					paneIdx := pi + 1
					if p.Index > 0 {
						paneIdx = p.Index
					}
					activePaneTarget = fmt.Sprintf("%s.%d", winTarget, paneIdx)
				}
				_ = r.SelectPane(ctx, activePaneTarget)
				break
			}
		}
		if hasRich && win.Zoomed {
			zoomTarget := activePaneTarget
			if zoomTarget == "" {
				zoomTarget = winTarget
			}
			_ = rich.ResizePaneZoom(ctx, zoomTarget)
		}
	}

	if activeWinTarget != "" {
		_ = r.SelectWindow(ctx, activeWinTarget)
	}
	if opts.Switch {
		return r.SwitchToSpace(ctx, snap.Name)
	}
	return nil
}

func restoreSessionWorktreeOptions(ctx context.Context, r tmuxRichRestorer, snap SpaceSnapshot) {
	if snap.WorktreePath != "" {
		_ = r.SetSessionOption(ctx, snap.Name, "@spaces_worktree_path", snap.WorktreePath)
	}
	if snap.WorktreeName != "" {
		_ = r.SetSessionOption(ctx, snap.Name, "@spaces_worktree_name", snap.WorktreeName)
	}
	if snap.WorktreeBase != "" {
		_ = r.SetSessionOption(ctx, snap.Name, "@spaces_worktree_base", snap.WorktreeBase)
	}
}

func restoreWindowWorktreeOptions(ctx context.Context, r tmuxRichRestorer, winTarget string, win WindowSnapshot) {
	if win.WorktreePath != "" {
		_ = r.SetWindowOption(ctx, winTarget, "@spaces_worktree_path", win.WorktreePath)
	}
	if win.WorktreeName != "" {
		_ = r.SetWindowOption(ctx, winTarget, "@spaces_worktree_name", win.WorktreeName)
	}
	if win.WorktreeBase != "" {
		_ = r.SetWindowOption(ctx, winTarget, "@spaces_worktree_base", win.WorktreeBase)
	}
}

func paneResolveKey(wi, pi int) string {
	return fmt.Sprintf("%d:%d", wi, pi)
}

// resolveAgentSessions delegates restore-time session resolution and
// deduplication to each agent plugin's SessionResolver.
func resolveAgentSessions(ctx context.Context, snap SpaceSnapshot, opts RestoreOptions) (map[string]string, map[string]bool) {
	assigned := map[string]string{}
	skipResume := map[string]bool{}

	byAgent := map[string][]agent.SessionPaneRef{}
	var agentOrder []string
	for wi, win := range snap.Windows {
		for pi, p := range win.Panes {
			if p.Agent == "" {
				continue
			}
			if _, seen := byAgent[p.Agent]; !seen {
				agentOrder = append(agentOrder, p.Agent)
			}
			cwd := p.CWD
			if cwd == "" {
				cwd = snap.CWD
			}
			prof := p.Profile
			if prof == "" {
				prof = agent.HarnessProfileFromArgv(p.FullCommand)
			}
			byAgent[p.Agent] = append(byAgent[p.Agent], agent.SessionPaneRef{
				Key:         paneResolveKey(wi, pi),
				Agent:       p.Agent,
				SessionID:   p.SessionID,
				Profile:     prof,
				CWD:         cwd,
				Title:       p.Title,
				FullCommand: p.FullCommand,
			})
		}
	}

	if len(agentOrder) == 0 {
		return assigned, skipResume
	}

	lookup := func(ctx context.Context, agentID, cwd, profile, title string, exclude map[string]bool) (string, bool) {
		if opts.ResolveSession != nil {
			return opts.ResolveSession(ctx, agentID, cwd, profile, title, exclude)
		}
		if agentID == "antigravity" && opts.ResolveAntigravity != nil {
			return opts.ResolveAntigravity(ctx, cwd, profile, title, exclude)
		}
		return defaultResolveAgentSession(ctx, agentID, cwd, profile, title, exclude)
	}

	for _, agentID := range agentOrder {
		plugin, ok := agent.DefaultRegistry().ResolveRuntime(agentID)
		if !ok {
			continue
		}
		res := plugin.SessionResolver().ResolveRestoreSessions(ctx, byAgent[agentID], lookup)
		for k, v := range res.SessionIDs {
			assigned[k] = v
		}
		for k, v := range res.SkipResume {
			if v {
				skipResume[k] = true
			}
		}
	}
	return assigned, skipResume
}

func defaultResolveAgentSession(ctx context.Context, agentID, cwd, profile, title string, exclude map[string]bool) (string, bool) {
	return agent.DefaultRegistry().FindUnclaimedConversation(ctx, "", agentID, cwd, profile, title, exclude)
}

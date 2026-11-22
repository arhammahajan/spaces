package resurrect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/model"
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

// RestoreOptions configures how a SpaceSnapshot is recreated.
type RestoreOptions struct {
	// SelfPath is the path to tmux-spaces (used to wrap resumed agents in __shim).
	SelfPath string
	// Switch switches/attaches the client to the restored space when true.
	Switch bool
	// SpacesBinary returns the argv prefix for zj helpers (defaults to model.SpacesBinary).
	SpacesBinary func() []string
	// ResolveAntigravity resolves an unclaimed Antigravity conversation ID for
	// (cwd, profile, title) while excluding already-claimed IDs. Returns (id, ok)
	// where ok=false means resolution was unavailable. Defaults to invoking
	// `spaces resume-command antigravity ...`.
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
//   - AI agent with profile -> `tmux-spaces __shim <agent> spaces __shim-profile <agent>/<profile>`
//   - AI agent (with or without explicit session ID) ->
//     `tmux-spaces __shim <agent> spaces __resume-wrap <b64(cmd)> [sessionID]`
//     so spaceresume attaches the exact conversation (or discovers the latest in CWD).
//   - Safe interactive TUI (nvim, lazygit, btop, ...) -> restarts the command in CWD.
//   - Plain shell or arbitrary command -> returns nil (clean login shell in CWD).
func BuildPaneCommand(p PaneSnapshot, opts RestoreOptions) []string {
	return buildPaneCommand(p, opts, false)
}

func buildPaneCommand(p PaneSnapshot, opts RestoreOptions, skipResume bool) []string {
	self := opts.SelfPath
	if self == "" {
		self = "tmux-spaces"
	}
	zjFn := opts.SpacesBinary
	if zjFn == nil {
		zjFn = model.SpacesBinary
	}

	if p.Agent != "" {
		zj := zjFn()
		if skipResume {
			if len(p.FullCommand) > 0 && isHarnessLauncher(p.FullCommand) {
				return append([]string{self, "__shim", p.Agent}, p.FullCommand...)
			}
			if p.Profile != "" {
				cmd := append([]string{self, "__shim", "--profile", p.Profile, p.Agent}, zj...)
				return append(cmd, "__shim-profile", p.Agent+"/"+p.Profile)
			}
			if len(p.FullCommand) > 0 && CanonicalAgent(p.FullCommand[0]) == p.Agent {
				return append([]string{self, "__shim", p.Agent}, p.FullCommand...)
			}
			return []string{self, "__shim", p.Agent, p.Agent}
		}
		baseCmd := p.Agent
		if len(p.FullCommand) > 0 && isHarnessLauncher(p.FullCommand) {
			baseCmd = shlexJoin(p.FullCommand)
		} else if p.Profile != "" {
			shimProfile := append(append([]string(nil), zj...), "__shim-profile", p.Agent+"/"+p.Profile)
			baseCmd = shlexJoin(shimProfile)
		} else if len(p.FullCommand) > 0 && CanonicalAgent(p.FullCommand[0]) == p.Agent {
			baseCmd = shlexJoin(p.FullCommand)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(baseCmd))
		cmd := append([]string{self, "__shim", p.Agent}, zj...)
		cmd = append(cmd, "__resume-wrap", encoded)
		if p.SessionID != "" {
			cmd = append(cmd, p.SessionID)
		}
		return cmd
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
// layouts, CWDs, active pane/window selection, and AI agent conversation resume.
func Restore(ctx context.Context, r TmuxRestorer, snap SpaceSnapshot, opts RestoreOptions) error {
	if strings.TrimSpace(snap.Name) == "" {
		return fmt.Errorf("resurrect: snapshot has empty name")
	}
	if len(snap.Windows) == 0 {
		cwd := resolveRestoreCWD(snap.CWD, snap.WorktreePath, snap.MainRepoRoot)
		if err := r.NewSessionDetached(ctx, snap.Name, cwd, ""); err != nil {
			return err
		}
		if opts.Switch {
			return r.SwitchToSpace(ctx, snap.Name)
		}
		return nil
	}

	assigned, skipResume := resolveAntigravitySessions(ctx, snap, opts)

	activeWinTarget := ""
	for wi, win := range snap.Windows {
		winTarget := fmt.Sprintf("%s:%d", snap.Name, wi+1)
		if win.Index > 0 {
			winTarget = fmt.Sprintf("%s:%d", snap.Name, win.Index)
		}
		if win.Active || (snap.ActiveWindowIndex > 0 && win.Index == snap.ActiveWindowIndex) {
			activeWinTarget = winTarget
		}

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

		if wi == 0 {
			if err := r.NewSessionDetached(ctx, snap.Name, firstCWD, win.Name, firstCmd...); err != nil {
				return fmt.Errorf("resurrect: create session %s: %w", snap.Name, err)
			}
		} else {
			if err := r.NewWindow(ctx, snap.Name, firstCWD, win.Name, firstCmd...); err != nil {
				return fmt.Errorf("resurrect: create window %s:%s: %w", snap.Name, win.Name, err)
			}
		}

		// Recreate additional panes in this window.
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
			if err := r.SplitWindow(ctx, winTarget, pCWD, pCmd...); err != nil {
				return fmt.Errorf("resurrect: split pane %d in %s: %w", pi, winTarget, err)
			}
		}

		// Apply saved tmux layout string so split proportions match.
		if win.Layout != "" {
			_ = r.SelectLayout(ctx, winTarget, win.Layout)
		}

		// Restore active pane within this window.
		for pi, p := range win.Panes {
			if p.Active {
				paneIdx := pi + 1
				if p.Index > 0 {
					paneIdx = p.Index
				}
				_ = r.SelectPane(ctx, fmt.Sprintf("%s.%d", winTarget, paneIdx))
				break
			}
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

func shlexJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a == "" {
			quoted[i] = "''"
			continue
		}
		safe := true
		for _, r := range a {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '@') {
				safe = false
				break
			}
		}
		if safe {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(quoted, " ")
}

func paneResolveKey(wi, pi int) string {
	return fmt.Sprintf("%d:%d", wi, pi)
}

// resolveAntigravitySessions ensures multiple Antigravity panes in the same
// SpaceSnapshot never resume the same conversation_id. Panes that already carry
// a SessionID (or `--conversation <id>` in FullCommand) claim those IDs first;
// remaining Antigravity panes resolve distinct unclaimed conversations via
// `spaces resume-command` (matching saved Title when available), or skip resume
// when all matching conversations are already claimed.
func resolveAntigravitySessions(ctx context.Context, snap SpaceSnapshot, opts RestoreOptions) (map[string]string, map[string]bool) {
	assigned := map[string]string{}
	skipResume := map[string]bool{}
	claimed := map[string]bool{}

	type pendingPane struct {
		key     string
		cwd     string
		profile string
		title   string
	}
	var pending []pendingPane

	for wi, win := range snap.Windows {
		for pi, p := range win.Panes {
			if p.Agent != "antigravity" {
				continue
			}
			sid := strings.TrimSpace(p.SessionID)
			if sid == "" {
				sid = extractSessionIDFromCommand("antigravity", p.FullCommand)
			}
			if sid != "" {
				claimed[sid] = true
				continue
			}
			cwd := p.CWD
			if cwd == "" {
				cwd = snap.CWD
			}
			prof := p.Profile
			if prof == "" {
				prof = harnessProfileFromArgv(p.FullCommand)
			}
			pending = append(pending, pendingPane{
				key:     paneResolveKey(wi, pi),
				cwd:     cwd,
				profile: prof,
				title:   p.Title,
			})
		}
	}

	if len(pending) == 0 {
		return assigned, skipResume
	}

	resolver := opts.ResolveAntigravity
	if resolver == nil {
		resolver = func(ctx context.Context, cwd, profile, title string, exclude map[string]bool) (string, bool) {
			return defaultResolveAntigravity(ctx, opts.SpacesBinary, cwd, profile, title, exclude)
		}
	}

	for _, p := range pending {
		sid, ok := resolver(ctx, p.cwd, p.profile, p.title, claimed)
		if sid != "" {
			assigned[p.key] = sid
			claimed[sid] = true
		} else if ok && len(claimed) > 0 {
			skipResume[p.key] = true
		}
	}
	return assigned, skipResume
}

func defaultResolveAntigravity(ctx context.Context, zjFn func() []string, cwd, profile, title string, exclude map[string]bool) (string, bool) {
	if zjFn == nil {
		zjFn = model.SpacesBinary
	}
	zj := zjFn()
	if len(zj) == 0 {
		return "", false
	}
	args := append([]string(nil), zj[1:]...)
	args = append(args, "resume-command", "antigravity", cwd)
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if title != "" {
		args = append(args, "--title", title)
	}
	if len(exclude) > 0 {
		ids := make([]string, 0, len(exclude))
		for id := range exclude {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		args = append(args, "--exclude", strings.Join(ids, ","))
	}
	out, err := exec.CommandContext(ctx, zj[0], args...).Output()
	if err != nil {
		return "", false
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", false
	}
	return res.SessionID, true
}

func harnessProfileFromArgv(argv []string) string {
	if len(argv) < 3 || filepath.Base(argv[0]) != "harness" {
		return ""
	}
	switch {
	case argv[1] == "profile" && !strings.HasPrefix(argv[2], "-"):
		return argv[2]
	case len(argv) >= 4 && argv[2] == "profile" && !strings.HasPrefix(argv[3], "-"):
		return argv[3]
	case len(argv) >= 4 && argv[1] == "spaces" && argv[2] == "__shim-profile":
		if _, prof, ok := strings.Cut(argv[3], "/"); ok {
			return prof
		}
		return argv[3]
	}
	return ""
}

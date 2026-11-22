package resurrect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/agent/plugins"
	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/tmux"
)

// ProcCommandFunc inspects the active foreground/child command line for a
// pane's root PID. Tests may stub it; production reads /proc on Linux.
var ProcCommandFunc = readChildCmdline

// ProcAntigravitySessionFunc inspects open file descriptors of a pane's
// descendant processes to find an active Antigravity conversation ID.
// Tests may stub it; production delegates to the Antigravity plugin's
// SessionResolver.
var ProcAntigravitySessionFunc = defaultProcAntigravitySession

func defaultProcAntigravitySession(pid int) string {
	if p, ok := agent.DefaultRegistry().ResolveRuntime("antigravity"); ok {
		return p.SessionResolver().DiscoverLiveSessionID(pid)
	}
	if plugins.AntigravityProcSessionFunc != nil {
		return plugins.AntigravityProcSessionFunc(pid)
	}
	return ""
}

// CanonicalAgent returns the canonical AI agent id if name identifies a known
// coding harness, or "" otherwise.
func CanonicalAgent(name string) string {
	return agent.DefaultRegistry().CanonicalID(name)
}

// CaptureSpaces converts a live tmux ServerSnapshot plus optional hook store
// summary into per-space SpaceSnapshots ready for persistence.
func CaptureSpaces(snap tmux.ServerSnapshot, hooks *model.HookSnapshot, now time.Time) []SpaceSnapshot {
	winBySession := map[string][]tmux.Window{}
	for _, w := range snap.Windows {
		winBySession[w.SessionName] = append(winBySession[w.SessionName], w)
	}
	panesByWin := map[string][]tmux.Pane{}
	for _, p := range snap.Panes {
		key := p.SessionName + "\x00" + p.WindowID
		panesByWin[key] = append(panesByWin[key], p)
	}

	nowMs := now.UnixMilli()
	out := make([]SpaceSnapshot, 0, len(snap.Sessions))
	for _, sess := range snap.Sessions {
		wins := append([]tmux.Window(nil), winBySession[sess.Name]...)
		sort.Slice(wins, func(i, j int) bool { return wins[i].Index < wins[j].Index })

		spaceSnap := SpaceSnapshot{
			Version:      SnapshotVersion,
			Name:         sess.Name,
			SavedAtMs:    nowMs,
			WorktreePath: sess.WorktreePath,
			WorktreeName: sess.WorktreeName,
			WorktreeBase: sess.WorktreeBase,
		}
		var firstCWD string
		for _, w := range wins {
			if w.Active {
				spaceSnap.ActiveWindowIndex = w.Index
			}
			rawPanes := append([]tmux.Pane(nil), panesByWin[sess.Name+"\x00"+w.ID]...)
			sort.Slice(rawPanes, func(i, j int) bool {
				if rawPanes[i].PaneIndex != rawPanes[j].PaneIndex {
					return rawPanes[i].PaneIndex < rawPanes[j].PaneIndex
				}
				return rawPanes[i].NumericID < rawPanes[j].NumericID
			})
			ws := WindowSnapshot{
				Index:        w.Index,
				Name:         w.Name,
				Layout:       w.Layout,
				Active:       w.Active,
				Zoomed:       w.Zoomed,
				Width:        w.Width,
				Height:       w.Height,
				WorktreePath: w.WorktreePath,
				WorktreeName: w.WorktreeName,
				WorktreeBase: w.WorktreeBase,
				Panes:        make([]PaneSnapshot, 0, len(rawPanes)),
			}
			for _, p := range rawPanes {
				if p.CurrentPath != "" {
					if firstCWD == "" {
						firstCWD = p.CurrentPath
					}
					if p.Active && w.Active {
						spaceSnap.CWD = p.CurrentPath
					}
				}
				var hookRec *model.HookSummary
				if hooks != nil {
					if sp, ok := hooks.Panes[sess.Name]; ok {
						if h, ok := sp[strconv.Itoa(p.NumericID)]; ok {
							hc := h
							hookRec = &hc
						}
					}
				}
				ps := capturePane(p, hookRec)
				ws.Panes = append(ws.Panes, ps)
			}
			spaceSnap.Windows = append(spaceSnap.Windows, ws)
		}
		if spaceSnap.CWD == "" {
			spaceSnap.CWD = firstCWD
		}
		out = append(out, spaceSnap)
	}
	return out
}

// CaptureOpenState records the ordered list of currently running spaces and
// the last active space from a live tmux ServerSnapshot.
func CaptureOpenState(snap tmux.ServerSnapshot, now time.Time) OpenState {
	sessions := append([]tmux.Session(nil), snap.Sessions...)
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
		}
		if sessions[i].ID != sessions[j].ID {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].Name < sessions[j].Name
	})

	spaces := make([]string, 0, len(sessions))
	inSet := make(map[string]bool, len(sessions))
	firstAttached := ""
	for _, s := range sessions {
		if strings.TrimSpace(s.Name) == "" {
			continue
		}
		spaces = append(spaces, s.Name)
		inSet[s.Name] = true
		if firstAttached == "" && s.Attached > 0 {
			firstAttached = s.Name
		}
	}

	active := strings.TrimSpace(snap.CurrentSession)
	if active == "" || !inSet[active] {
		active = firstAttached
	}
	if active == "" && len(spaces) > 0 {
		active = spaces[0]
	}
	return OpenState{
		Version:       SnapshotVersion,
		SavedAtMs:     now.UnixMilli(),
		ActiveSession: active,
		Spaces:        spaces,
	}
}

func capturePane(p tmux.Pane, hook *model.HookSummary) PaneSnapshot {
	title := p.Title
	if t := strings.TrimSpace(p.SpacesTitle); t != "" {
		title = t
	}
	ps := PaneSnapshot{
		Index:   p.PaneIndex,
		ID:      p.ID,
		CWD:     p.CurrentPath,
		Command: p.CurrentCommand,
		App:     p.SpacesApp,
		Title:   title,
		Active:  p.Active,
		Profile: p.ResumeProfile,
	}

	if ProcCommandFunc != nil && p.PID > 0 {
		if argv := ProcCommandFunc(p.PID); len(argv) > 0 {
			ps.FullCommand = argv
		}
	}

	// Determine canonical AI agent from highest-confidence source first.
	for _, cand := range []string{
		p.SpacesApp,
		func() string {
			if hook != nil {
				if hook.Agent != "" {
					return hook.Agent
				}
				return hook.App
			}
			return ""
		}(),
		func() string {
			if len(ps.FullCommand) > 0 {
				return ps.FullCommand[0]
			}
			return ""
		}(),
		p.CurrentCommand,
	} {
		if a := CanonicalAgent(cand); a != "" {
			ps.Agent = a
			break
		}
	}

	// Capture conversation session ID from @spaces_resume_session, hook store,
	// explicit CLI resume flags, or live process discovery via the agent plugin.
	if p.ResumeSession != "" {
		ps.SessionID = p.ResumeSession
	} else if hook != nil && hook.SessionID != "" {
		ps.SessionID = hook.SessionID
	} else if sid := extractSessionIDFromCommand(ps.Agent, ps.FullCommand); sid != "" {
		ps.SessionID = sid
	} else if ps.Agent == "antigravity" && ProcAntigravitySessionFunc != nil && p.PID > 0 {
		ps.SessionID = ProcAntigravitySessionFunc(p.PID)
	} else if ps.Agent != "" && ps.Agent != "antigravity" && p.PID > 0 {
		if plugin, ok := agent.DefaultRegistry().ResolveRuntime(ps.Agent); ok {
			ps.SessionID = plugin.SessionResolver().DiscoverLiveSessionID(p.PID)
		}
	}

	if ps.App == "" && ps.Agent != "" {
		ps.App = ps.Agent
	}
	return ps
}

func extractSessionIDFromCommand(agentName string, argv []string) string {
	if agentName == "" || len(argv) == 0 {
		return ""
	}
	if p, ok := agent.DefaultRegistry().ResolveRuntime(agentName); ok {
		return p.SessionResolver().ExtractSessionIDFromArgs(argv)
	}
	return agent.ExtractSessionIDFromArgs(argv, false)
}

// readChildCmdline walks /proc/<pid>/task/*/children to find the deepest
// non-shim child command line running inside a tmux pane on Linux.
func readChildCmdline(pid int) []string {
	cur := pid
	var best []string
	for depth := 0; depth < 6; depth++ {
		child := firstChildPID(cur)
		if child <= 0 || child == cur {
			break
		}
		argv := readProcCmdline(child)
		if len(argv) > 0 {
			base := filepath.Base(argv[0])
			// Keep user-facing `harness <agent> ...` / `harness profile ...`
			// launchers intact so profile/shadow-home setup is preserved on
			// restore, while descending past internal wrappers.
			best = argv
			if base == "harness" && (len(argv) < 2 || argv[1] != "spaces") {
				return argv
			}
			if CanonicalAgent(base) != "" {
				return argv
			}
		}
		cur = child
	}
	return best
}

func firstChildPID(pid int) int {
	pattern := fmt.Sprintf("/proc/%d/task/*/children", pid)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(f); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func readProcCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

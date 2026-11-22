package resurrect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
	"github.com/arhammahajan/spaces/internal/tmux"
)

// ProcCommandFunc inspects the active foreground/child command line for a
// pane's root PID. Tests may stub it; production reads /proc on Linux.
var ProcCommandFunc = readChildCmdline

// ProcAntigravitySessionFunc inspects open file descriptors of a pane's
// descendant processes to find an active Antigravity conversation ID.
// Tests may stub it; production reads /proc/<pid>/fd on Linux.
var ProcAntigravitySessionFunc = readChildAntigravitySession

// knownAgents is the canonical set of AI coding harnesses eligible for
// conversation resurrection via spaceresume.
var knownAgents = map[string]string{
	"claude":       "claude",
	"claude-code":  "claude",
	"codex":        "codex",
	"opencode":     "opencode",
	"pi":           "pi",
	"qwen":         "qwen",
	"copilot":      "copilot",
	"kimi":         "kimi",
	"kimi-cli":     "kimi",
	"kimi-code":    "kimi",
	"mcode":        "mcode",
	"muse":         "muse",
	"vibe":         "vibe",
	"vibe-acp":     "vibe",
	"gemini":       "gemini",
	"cline":        "cline",
	"cursor":       "cursor-agent",
	"cursor-agent": "cursor-agent",
	"grok":         "grok",
	"crush":        "crush",
	"omp":          "omp",
	"kilo":         "kilo",
	"antigravity":  "antigravity",
	"agy":          "antigravity",
	"deepseek":     "deepseek",
	"dsh":          "deepseek",
	"dsh-tui":      "deepseek",
	"fx":           "fx",
	"fx-router":    "fx",
}

// CanonicalAgent returns the canonical AI agent id if name identifies a known
// coding harness, or "" otherwise.
func CanonicalAgent(name string) string {
	n := strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if canon, ok := knownAgents[n]; ok {
		return canon
	}
	return ""
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
	// explicit CLI resume flags, or (for Antigravity) open process FDs.
	if p.ResumeSession != "" {
		ps.SessionID = p.ResumeSession
	} else if hook != nil && hook.SessionID != "" {
		ps.SessionID = hook.SessionID
	} else if sid := extractSessionIDFromCommand(ps.Agent, ps.FullCommand); sid != "" {
		ps.SessionID = sid
	} else if ps.Agent == "antigravity" && ProcAntigravitySessionFunc != nil && p.PID > 0 {
		ps.SessionID = ProcAntigravitySessionFunc(p.PID)
	}

	if ps.App == "" && ps.Agent != "" {
		ps.App = ps.Agent
	}
	return ps
}

func extractSessionIDFromCommand(agent string, argv []string) string {
	if agent == "" || len(argv) == 0 {
		return ""
	}
	valueFlags := []string{"--conversation", "--resume", "--session", "--id"}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		for _, flag := range valueFlags {
			if tok == flag && i+1 < len(argv) {
				v := strings.TrimSpace(argv[i+1])
				if v != "" && !strings.HasPrefix(v, "-") && v != "latest" {
					return v
				}
			}
			if strings.HasPrefix(tok, flag+"=") {
				v := strings.TrimSpace(strings.TrimPrefix(tok, flag+"="))
				if v != "" && v != "latest" {
					return v
				}
			}
		}
		if (agent == "codex" || agent == "muse") && tok == "resume" && i+1 < len(argv) {
			v := strings.TrimSpace(argv[i+1])
			if v != "" && !strings.HasPrefix(v, "-") && v != "--last" {
				return v
			}
		}
	}
	return ""
}

// readChildAntigravitySession walks the pane's descendant processes on Linux
// and inspects /proc/<pid>/fd/* for open Antigravity conversation DB or
// presence lock files (`.../antigravity-cli/conversations/<id>.db` or
// `.../antigravity-cli/presence/<id>.lock`).
func readChildAntigravitySession(pid int) string {
	if pid <= 1 {
		return ""
	}
	visited := map[int]bool{pid: true}
	frontier := []int{pid}
	var lockFallback string
	for depth := 0; depth < 6 && len(frontier) > 0; depth++ {
		var next []int
		for _, cur := range frontier {
			if id, isConv := antigravitySessionFromFDs(cur); id != "" {
				if isConv {
					return id
				}
				if lockFallback == "" {
					lockFallback = id
				}
			}
			for _, child := range allChildPIDs(cur) {
				if !visited[child] {
					visited[child] = true
					next = append(next, child)
				}
			}
		}
		frontier = next
	}
	return lockFallback
}

func antigravitySessionFromFDs(pid int) (string, bool) {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return "", false
	}
	var lockID string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil || target == "" {
			continue
		}
		if idx := strings.Index(target, "/antigravity-cli/conversations/"); idx >= 0 {
			rest := target[idx+len("/antigravity-cli/conversations/"):]
			for _, suf := range []string{".db-wal", ".db-shm", ".db"} {
				if strings.HasSuffix(rest, suf) {
					id := strings.TrimSuffix(rest, suf)
					if id != "" && !strings.Contains(id, "/") {
						return id, true
					}
				}
			}
		}
		if lockID == "" {
			if idx := strings.Index(target, "/antigravity-cli/presence/"); idx >= 0 {
				rest := target[idx+len("/antigravity-cli/presence/"):]
				if strings.HasSuffix(rest, ".lock") {
					id := strings.TrimSuffix(rest, ".lock")
					if id != "" && !strings.Contains(id, "/") {
						lockID = id
					}
				}
			}
		}
	}
	if lockID != "" {
		return lockID, false
	}
	return "", false
}

func allChildPIDs(pid int) []int {
	pattern := fmt.Sprintf("/proc/%d/task/*/children", pid)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var out []int
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(f); err == nil && n > 1 {
				out = append(out, n)
			}
		}
	}
	return out
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

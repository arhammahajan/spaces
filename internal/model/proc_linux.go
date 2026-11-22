//go:build linux

package model

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

// procIsLive reports whether /proc appears mounted and rootPID is an existing
// process in /proc. When tests pass synthetic PIDs (like 100) that don't exist,
// procIsLive returns false so tests don't falsely conclude a synthetic PID has
// no children unless they stub DetectDescendantAgentFn.
func procIsLive(rootPID int) bool {
	if rootPID <= 1 {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(rootPID)))
	return err == nil
}

// detectDescendantAgent walks direct and descendant child processes of rootPID
// via /proc/<pid>/task/*/children (up to 4 levels deep) and returns the first
// known coding-agent name found in a child's comm or cmdline argv.
func detectDescendantAgent(rootPID int) string {
	if rootPID <= 1 {
		return ""
	}
	visited := map[int]bool{rootPID: true}
	frontier := []int{rootPID}
	for depth := 0; depth < 4 && len(frontier) > 0; depth++ {
		var next []int
		for _, pid := range frontier {
			children := procChildren(pid)
			for _, child := range children {
				if visited[child] {
					continue
				}
				visited[child] = true
				if agent := agentFromProc(child); agent != "" {
					return agent
				}
				next = append(next, child)
			}
		}
		frontier = next
	}
	return ""
}

func procChildren(pid int) []int {
	pattern := filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "children")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	var out []int
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			if child, err := strconv.Atoi(field); err == nil && child > 1 {
				out = append(out, child)
			}
		}
	}
	return out
}

func agentFromProc(pid int) string {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	if comm, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		if n := NormalizeAgentCommand(strings.TrimSpace(string(comm))); IsKnownAgent(n) {
			return n
		}
	}
	raw, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	parts := bytes.Split(raw, []byte{0})
	// Check argv[0], then script/entrypoint arguments (e.g. `node /path/to/claude`,
	// `harness muse`, `python -m kimi_cli`). Skip flags starting with '-'.
	checked := 0
	for _, part := range parts {
		arg := strings.TrimSpace(string(part))
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		if n := NormalizeAgentCommand(arg); IsKnownAgent(n) {
			return n
		}
		// Also check parent directory segments for versioned binaries like
		// `~/.local/share/claude/versions/2.1.76`.
		if strings.Contains(arg, "/") {
			for _, seg := range strings.Split(arg, "/") {
				if seg == "" {
					continue
				}
				if n := NormalizeAgentCommand(seg); IsKnownAgent(n) {
					return n
				}
			}
		}
		checked++
		if checked >= 4 {
			break
		}
	}
	return ""
}

// detectDescendantProfile inspects rootPID and its descendant processes (up to
// 4 levels deep) via /proc/<pid>/cmdline and /proc/<pid>/environ to recover
// the active harness profile name when @spaces_profile is unset.
func detectDescendantProfile(rootPID int) string {
	if rootPID <= 1 {
		return ""
	}
	if prof := profileFromProc(rootPID); prof != "" {
		return prof
	}
	visited := map[int]bool{rootPID: true}
	frontier := []int{rootPID}
	const maxNodes = 64
	seen := 1
	for depth := 0; depth < 4 && len(frontier) > 0 && seen < maxNodes; depth++ {
		var next []int
		for _, pid := range frontier {
			for _, child := range procChildren(pid) {
				if child <= 1 || visited[child] {
					continue
				}
				visited[child] = true
				seen++
				if prof := profileFromProc(child); prof != "" {
					return prof
				}
				if seen >= maxNodes {
					break
				}
				next = append(next, child)
			}
			if seen >= maxNodes {
				break
			}
		}
		frontier = next
	}
	return ""
}

func profileFromProc(pid int) string {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	cmdline, _ := os.ReadFile(filepath.Join(base, "cmdline"))
	environ, _ := os.ReadFile(filepath.Join(base, "environ"))
	return extractProfileFromProcData(cmdline, environ)
}

// extractProfileFromProcData parses a NUL-delimited /proc/<pid>/cmdline and
// /proc/<pid>/environ pair, returning a harness profile from argv first, then
// HARNESS_PROFILE from the environment.
func extractProfileFromProcData(cmdline, environ []byte) string {
	if len(cmdline) > 0 {
		rawParts := bytes.Split(cmdline, []byte{0})
		argv := make([]string, 0, len(rawParts))
		for _, p := range rawParts {
			if s := strings.TrimSpace(string(p)); s != "" {
				argv = append(argv, s)
			}
		}
		if prof := strings.TrimSpace(agent.HarnessProfileFromArgv(argv)); prof != "" {
			return prof
		}
	}
	if len(environ) > 0 {
		for _, entry := range bytes.Split(environ, []byte{0}) {
			if bytes.HasPrefix(entry, []byte("HARNESS_PROFILE=")) {
				if prof := strings.TrimSpace(string(entry[len("HARNESS_PROFILE="):])); prof != "" {
					return prof
				}
			}
		}
	}
	return ""
}

type procStat struct {
	pgrp  int
	tpgid int
}

// parseProcStat extracts pgrp (field 5) and tpgid (field 8) from the contents
// of /proc/<pid>/stat. Because comm (field 2) is enclosed in parentheses and
// may contain spaces or parentheses itself, we split after the LAST ')'.
func parseProcStat(raw string) (procStat, bool) {
	idx := strings.LastIndexByte(raw, ')')
	if idx < 0 || idx+1 >= len(raw) {
		return procStat{}, false
	}
	// Fields after ")":
	//   0: state, 1: ppid, 2: pgrp, 3: session, 4: tty_nr, 5: tpgid, ...
	fields := strings.Fields(raw[idx+1:])
	if len(fields) <= 5 {
		return procStat{}, false
	}
	pgrp, err1 := strconv.Atoi(fields[2])
	tpgid, err2 := strconv.Atoi(fields[5])
	if err1 != nil || err2 != nil {
		return procStat{}, false
	}
	return procStat{pgrp: pgrp, tpgid: tpgid}, true
}

func readProcStat(pid int) (procStat, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil || len(raw) == 0 {
		return procStat{}, false
	}
	return parseProcStat(string(raw))
}

func isForegroundProc(st procStat, paneTPGID int, inFgSubtree bool) bool {
	if st.pgrp <= 0 || st.tpgid <= 0 || st.pgrp != st.tpgid {
		return false
	}
	if paneTPGID <= 0 {
		return true
	}
	return st.tpgid == paneTPGID || inFgSubtree
}

type procCandidate struct {
	pid        int
	cmd        string
	foreground bool
	isLeaf     bool
	depth      int
}

// candidateBetter reports whether a strictly outranks b:
//  1. belongs to the pane's active foreground process group
//  2. leaf process (no non-helper children in the traversal)
//  3. deeper depth in the process tree
//  4. higher PID (deterministic tie-breaker independent of /proc order)
func candidateBetter(a, b procCandidate) bool {
	if a.foreground != b.foreground {
		return a.foreground
	}
	if a.isLeaf != b.isLeaf {
		return a.isLeaf
	}
	if a.depth != b.depth {
		return a.depth > b.depth
	}
	return a.pid > b.pid
}

func selectBestProcCandidate(candidates []procCandidate) (procCandidate, bool) {
	if len(candidates) == 0 {
		return procCandidate{}, false
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if candidateBetter(c, best) {
			best = c
		}
	}
	return best, true
}

type procInspector struct {
	children func(pid int) []int
	cmdline  func(pid int) string
	stat     func(pid int) (procStat, bool)
}

type procWalkNode struct {
	pid             int
	depth           int
	ancestorCandIdx int
	inFgSubtree     bool
}

// inspectPaneProcess inspects live descendant processes of rootPID via /proc
// and returns a compact readable foreground command line plus the total count
// of live descendant processes. Safe no-op for synthetic/non-existent PIDs.
func inspectPaneProcess(rootPID int) (string, int) {
	if !procIsLive(rootPID) {
		return "", 0
	}
	return inspectPaneProcessWith(rootPID, procInspector{
		children: procChildren,
		cmdline:  readableProcCmdline,
		stat:     readProcStat,
	})
}

func inspectPaneProcessWith(rootPID int, insp procInspector) (string, int) {
	if rootPID <= 1 {
		return "", 0
	}
	paneTPGID := 0
	if insp.stat != nil {
		if rootStat, ok := insp.stat(rootPID); ok && rootStat.tpgid > 0 {
			paneTPGID = rootStat.tpgid
		}
	}

	visited := map[int]bool{rootPID: true}
	frontier := []procWalkNode{{pid: rootPID, depth: 0, ancestorCandIdx: -1}}
	var candidates []procCandidate
	count := 0
	const (
		maxDepth = 4
		maxNodes = 32
	)

	for len(frontier) > 0 && count < maxNodes {
		var next []procWalkNode
		for _, parent := range frontier {
			if parent.depth >= maxDepth {
				continue
			}
			children := insp.children(parent.pid)
			for _, child := range children {
				if child <= 1 || visited[child] {
					continue
				}
				visited[child] = true
				count++

				fg := false
				if insp.stat != nil {
					if st, ok := insp.stat(child); ok {
						fg = isForegroundProc(st, paneTPGID, parent.inFgSubtree)
					}
				}

				childDepth := parent.depth + 1
				nextAncestorIdx := parent.ancestorCandIdx
				if cmd := insp.cmdline(child); cmd != "" && !isInternalHelperCmd(cmd) {
					if parent.ancestorCandIdx >= 0 {
						candidates[parent.ancestorCandIdx].isLeaf = false
					}
					nextAncestorIdx = len(candidates)
					candidates = append(candidates, procCandidate{
						pid:        child,
						cmd:        cmd,
						foreground: fg,
						isLeaf:     true,
						depth:      childDepth,
					})
				}

				if count >= maxNodes {
					break
				}
				if childDepth < maxDepth {
					next = append(next, procWalkNode{
						pid:             child,
						depth:           childDepth,
						ancestorCandIdx: nextAncestorIdx,
						inFgSubtree:     parent.inFgSubtree || fg,
					})
				}
			}
			if count >= maxNodes {
				break
			}
		}
		frontier = next
	}

	if best, ok := selectBestProcCandidate(candidates); ok {
		return best.cmd, count
	}

	if cmd := insp.cmdline(rootPID); cmd != "" && !isInternalHelperCmd(cmd) {
		// Skip bare login shells like "-zsh" or "bash" with no interesting args.
		fields := strings.Fields(cmd)
		if len(fields) > 1 || (len(fields) == 1 && !IsShell(strings.TrimLeft(fields[0], "-"))) {
			return cmd, count
		}
	}
	return "", count
}

func isInternalHelperCmd(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	return strings.Contains(lower, "__osc-watch") || strings.Contains(lower, "__shim")
}

func readableProcCmdline(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	parts := bytes.Split(raw, []byte{0})
	var args []string
	for _, p := range parts {
		s := strings.TrimSpace(string(p))
		if s == "" {
			continue
		}
		if len(args) == 0 {
			// Shorten full executable path to basename for compact display.
			if strings.Contains(s, "/") {
				s = filepath.Base(s)
			}
		}
		args = append(args, s)
		if len(args) >= 8 {
			break
		}
	}
	if len(args) == 0 {
		return ""
	}
	joined := strings.Join(args, " ")
	rs := []rune(joined)
	if len(rs) > 160 {
		return string(rs[:159]) + "…"
	}
	return joined
}

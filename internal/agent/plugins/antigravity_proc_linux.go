//go:build linux

package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// discoverAntigravitySessionFromProc walks the descendant process tree of pid
// (depth <= 6) inspecting /proc/<pid>/fd symlinks for open Antigravity
// conversation databases (`.../antigravity-cli/conversations/<id>.db*`) or
// presence locks (`.../antigravity-cli/presence/<id>.lock`).
func discoverAntigravitySessionFromProc(pid int) string {
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
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, tok := range strings.Fields(string(raw)) {
			if c, err := strconv.Atoi(tok); err == nil && c > 1 {
				out = append(out, c)
			}
		}
	}
	return out
}

//go:build linux

package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arhammahajan/spaces/internal/agent"
)

func defaultPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// discoverOpenCodeEndpoint walks target.PanePID and its descendant processes
// (depth <= 6) looking for an OpenCode server endpoint via:
//  1. /proc/<pid>/cmdline (--server or --port/+--hostname)
//  2. /proc/<pid>/environ (OPENCODE_SERVER_URL=)
//  3. Listening loopback TCP sockets owned by OpenCode processes in the subtree
//     (/proc/<pid>/fd socket:[<inode>] matched against /proc/net/tcp and /proc/net/tcp6).
func discoverOpenCodeEndpoint(ctx context.Context, target agent.DaemonWatchTarget) (string, bool) {
	if target.PanePID <= 1 {
		return "", false
	}

	visited := map[int]bool{target.PanePID: true}
	frontier := []int{target.PanePID}
	var opencodePIDs []int

	for depth := 0; depth < 6 && len(frontier) > 0; depth++ {
		if ctx != nil && ctx.Err() != nil {
			return "", false
		}
		var next []int
		for _, pid := range frontier {
			argv := readProcCmdline(pid)
			comm := readProcComm(pid)
			isOC := isOpenCodeProc(comm, argv)
			if isOC {
				opencodePIDs = append(opencodePIDs, pid)
			}

			// 1. Check cmdline flags on OpenCode processes (or explicit --server).
			if isOC {
				if ep, ok := extractOpenCodeEndpointFromCmdline(argv); ok {
					return ep, true
				}
			}

			// 2. Check OPENCODE_SERVER_URL in environ on any process in the subtree.
			if envBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil && len(envBytes) > 0 {
				if ep, ok := extractOpenCodeEndpointFromEnv(envBytes); ok {
					return ep, true
				}
			}

			for _, child := range allChildPIDs(pid) {
				if !visited[child] {
					visited[child] = true
					next = append(next, child)
				}
			}
		}
		frontier = next
	}

	if len(opencodePIDs) == 0 {
		return "", false
	}

	// 3. Match socket inodes of OpenCode processes against /proc/net/tcp{,6}.
	listening := make(map[string]string)
	if raw4, err := os.ReadFile("/proc/net/tcp"); err == nil {
		for inode, ep := range parseProcNetTCP(raw4, false) {
			listening[inode] = ep
		}
	}
	if raw6, err := os.ReadFile("/proc/net/tcp6"); err == nil {
		for inode, ep := range parseProcNetTCP(raw6, true) {
			if _, exists := listening[inode]; !exists {
				listening[inode] = ep
			}
		}
	}
	if len(listening) == 0 {
		return "", false
	}

	for _, pid := range opencodePIDs {
		for _, inode := range readProcSocketInodes(pid) {
			if ep, ok := listening[inode]; ok {
				return ep, true
			}
		}
	}

	return "", false
}

func readProcCmdline(pid int) []string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

func readProcComm(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func isOpenCodeProc(comm string, argv []string) bool {
	p := NewOpenCode()
	return p.MatchProcess(agent.ProcessInfo{
		Comm: comm,
		Args: argv,
	})
}

func readProcSocketInodes(pid int) []string {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}
	var inodes []string
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if inode != "" {
				inodes = append(inodes, inode)
			}
		}
	}
	return inodes
}

//go:build linux

package model

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/arhammahajan/spaces/internal/agent"
)

// captureProcessSnapshot performs a single pass over /proc/<pid> to collect
// process metadata, parent-child links, RSS memory, and (when scanPorts is true)
// listening TCP sockets from /proc/net/tcp{,6}.
func captureProcessSnapshot(scanPorts bool) *procTableSnapshot {
	procs := make(map[int]agent.OSProcess)
	children := make(map[int][]int)
	pageSize := int64(os.Getpagesize())
	if pageSize <= 0 {
		pageSize = 4096
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return newEmptyProcTableSnapshot()
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		base := filepath.Join("/proc", entry.Name())
		statRaw, err := os.ReadFile(filepath.Join(base, "stat"))
		if err != nil || len(statRaw) == 0 {
			continue
		}
		comm, ppid, rssPages, ok := parseTelemetryProcStat(string(statRaw))
		if !ok {
			continue
		}

		cmdRaw, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil || len(cmdRaw) == 0 {
			continue // kernel thread or exited process
		}
		args := parseProcCmdlineArgs(cmdRaw)
		if len(args) == 0 {
			continue
		}

		cwd, _ := os.Readlink(filepath.Join(base, "cwd"))
		memKB := (rssPages * pageSize) / 1024
		if memKB < 0 {
			memKB = 0
		}

		procs[pid] = agent.OSProcess{
			PID:   pid,
			PPID:  ppid,
			Comm:  comm,
			Args:  args,
			MemKB: memKB,
			CWD:   cwd,
		}
		children[ppid] = append(children[ppid], pid)
	}

	var fdMu sync.Mutex
	fdCache := make(map[int][]string)
	openFilesFn := func(pid int) []string {
		if pid <= 0 {
			return nil
		}
		fdMu.Lock()
		if cached, ok := fdCache[pid]; ok {
			fdMu.Unlock()
			return cached
		}
		fdMu.Unlock()

		targets := readProcOpenFiles(pid)

		fdMu.Lock()
		fdCache[pid] = targets
		fdMu.Unlock()
		return targets
	}

	portsByPID := make(map[int][]int)
	if scanPorts {
		inodeToPort := make(map[uint64]int)
		for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
				parseProcNetTCP(string(raw), inodeToPort)
			}
		}
		if len(inodeToPort) > 0 {
			for pid := range procs {
				for _, target := range openFilesFn(pid) {
					inode, ok := parseSocketInode(target)
					if !ok {
						continue
					}
					if port, found := inodeToPort[inode]; found {
						if !slices.Contains(portsByPID[pid], port) {
							portsByPID[pid] = append(portsByPID[pid], port)
						}
					}
				}
			}
		}
	}

	return &procTableSnapshot{
		ProcessSnapshot: &agent.ProcessSnapshot{
			Processes:   procs,
			Children:    children,
			OpenFilesFn: openFilesFn,
		},
		PortsByPID: portsByPID,
	}
}

func readProcOpenFiles(pid int) []string {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	targets := make([]string, 0, len(entries))
	for _, entry := range entries {
		if target, err := os.Readlink(filepath.Join(fdDir, entry.Name())); err == nil && target != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

// killOrphanPort safely terminates an orphaned listening child process with
// SIGTERM after re-verifying /proc/<pid>/cmdline still matches expectedCmd
// to prevent PID-reuse kills.
func killOrphanPort(pid int, expectedCmd string) error {
	if pid <= 1 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	expected := strings.TrimSpace(expectedCmd)
	if expected == "" {
		return fmt.Errorf("refusing to kill pid %d without expected command", pid)
	}
	cmdRaw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return fmt.Errorf("read cmdline for pid %d: %w", pid, err)
	}
	actual := strings.Join(parseProcCmdlineArgs(cmdRaw), " ")
	if actual != expected {
		return fmt.Errorf("pid %d command changed (pid reuse guard): got %q, want %q", pid, actual, expected)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("sigterm pid %d: %w", pid, err)
	}
	return nil
}

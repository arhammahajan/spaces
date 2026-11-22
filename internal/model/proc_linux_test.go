//go:build linux

package model

import (
	"os"
	"strconv"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantOK    bool
		wantPgrp  int
		wantTpgid int
	}{
		{
			name:      "standard foreground process",
			raw:       "1234 (claude) S 1000 1234 1000 34816 1234 4194304 10 0\n",
			wantOK:    true,
			wantPgrp:  1234,
			wantTpgid: 1234,
		},
		{
			name:      "background process",
			raw:       "1235 (node) S 1000 1235 1000 34816 1234 4194304",
			wantOK:    true,
			wantPgrp:  1235,
			wantTpgid: 1234,
		},
		{
			name:      "detached process with negative tpgid",
			raw:       "2000 (daemon) S 1 2000 2000 0 -1 4194304",
			wantOK:    true,
			wantPgrp:  2000,
			wantTpgid: -1,
		},
		{
			name:      "comm containing spaces",
			raw:       "4321 (Web Content) S 100 200 100 34816 200 0",
			wantOK:    true,
			wantPgrp:  200,
			wantTpgid: 200,
		},
		{
			name:      "comm containing nested parentheses",
			raw:       "5678 (foo (bar)) S 100 250 100 34816 300 0",
			wantOK:    true,
			wantPgrp:  250,
			wantTpgid: 300,
		},
		{
			name:      "comm containing spaces and multiple closing parens",
			raw:       "9999 (my) weird (proc)) R 10 42 10 0 -1",
			wantOK:    true,
			wantPgrp:  42,
			wantTpgid: -1,
		},
		{
			name:   "empty input",
			raw:    "",
			wantOK: false,
		},
		{
			name:   "missing closing paren",
			raw:    "1234 (broken S 100 200 100 0 200",
			wantOK: false,
		},
		{
			name:   "truncated fields after paren",
			raw:    "1234 (bash) S 100 200 100 0",
			wantOK: false,
		},
		{
			name:   "non-numeric pgrp",
			raw:    "1234 (bash) S 100 nope 100 0 200",
			wantOK: false,
		},
		{
			name:   "non-numeric tpgid",
			raw:    "1234 (bash) S 100 200 100 0 nope",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := parseProcStat(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("parseProcStat(%q) ok = %v, want %v", tc.raw, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if st.pgrp != tc.wantPgrp || st.tpgid != tc.wantTpgid {
				t.Fatalf("parseProcStat(%q) = %+v, want pgrp=%d tpgid=%d", tc.raw, st, tc.wantPgrp, tc.wantTpgid)
			}
		})
	}
}

func TestReadProcStatSelf(t *testing.T) {
	st, ok := readProcStat(os.Getpid())
	if !ok {
		t.Fatalf("readProcStat(self=%d) failed on linux", os.Getpid())
	}
	if st.pgrp <= 0 {
		t.Errorf("readProcStat(self) pgrp = %d, want > 0", st.pgrp)
	}
}

func TestSelectBestProcCandidate(t *testing.T) {
	if _, ok := selectBestProcCandidate(nil); ok {
		t.Fatalf("expected false for empty candidates")
	}

	// Foreground shallow non-leaf beats deep background leaf.
	fgNonLeaf := procCandidate{pid: 200, cmd: "claude", foreground: true, isLeaf: false, depth: 1}
	bgDeepLeaf := procCandidate{pid: 900, cmd: "esbuild", foreground: false, isLeaf: true, depth: 4}
	for _, list := range [][]procCandidate{{fgNonLeaf, bgDeepLeaf}, {bgDeepLeaf, fgNonLeaf}} {
		got, ok := selectBestProcCandidate(list)
		if !ok || got.pid != fgNonLeaf.pid {
			t.Errorf("fg vs bg (%+v): got %+v, want pid %d", list, got, fgNonLeaf.pid)
		}
	}

	// Foreground leaf beats foreground non-leaf even if non-leaf has higher PID or depth.
	fgLeafShallow := procCandidate{pid: 210, cmd: "go test", foreground: true, isLeaf: true, depth: 1}
	fgNonLeafDeep := procCandidate{pid: 500, cmd: "wrapper", foreground: true, isLeaf: false, depth: 3}
	for _, list := range [][]procCandidate{{fgLeafShallow, fgNonLeafDeep}, {fgNonLeafDeep, fgLeafShallow}} {
		got, ok := selectBestProcCandidate(list)
		if !ok || got.pid != fgLeafShallow.pid {
			t.Errorf("leaf vs non-leaf (%+v): got %+v, want pid %d", list, got, fgLeafShallow.pid)
		}
	}

	// Deeper foreground leaf beats shallower foreground leaf.
	fgLeafD1 := procCandidate{pid: 800, cmd: "shallow", foreground: true, isLeaf: true, depth: 1}
	fgLeafD2 := procCandidate{pid: 150, cmd: "deeper", foreground: true, isLeaf: true, depth: 2}
	for _, list := range [][]procCandidate{{fgLeafD1, fgLeafD2}, {fgLeafD2, fgLeafD1}} {
		got, ok := selectBestProcCandidate(list)
		if !ok || got.pid != fgLeafD2.pid {
			t.Errorf("depth (%+v): got %+v, want pid %d", list, got, fgLeafD2.pid)
		}
	}

	// Higher PID breaks equal (foreground, isLeaf, depth) ties deterministically.
	sibA := procCandidate{pid: 301, cmd: "grep foo", foreground: true, isLeaf: true, depth: 1}
	sibB := procCandidate{pid: 302, cmd: "less", foreground: true, isLeaf: true, depth: 1}
	for _, list := range [][]procCandidate{{sibA, sibB}, {sibB, sibA}} {
		got, ok := selectBestProcCandidate(list)
		if !ok || got.pid != sibB.pid {
			t.Errorf("pid tie-break (%+v): got %+v, want pid %d", list, got, sibB.pid)
		}
	}
}

type fakeProcTree struct {
	children map[int][]int
	cmdlines map[int]string
	stats    map[int]procStat
}

func (f fakeProcTree) inspector() procInspector {
	return procInspector{
		children: func(pid int) []int { return f.children[pid] },
		cmdline:  func(pid int) string { return f.cmdlines[pid] },
		stat: func(pid int) (procStat, bool) {
			st, ok := f.stats[pid]
			return st, ok
		},
	}
}

func TestInspectPaneProcessForegroundBeatsBackgroundRegardlessOfOrder(t *testing.T) {
	// Pane shell (100, tpgid=200) has:
	//   - foreground agent (200: claude --resume, pgrp=200, tpgid=200)
	//   - background dev server tree (300 -> 301 -> 302, pgrp=300, tpgid=200)
	//   - detached daemon (400, pgrp=400, tpgid=-1)
	//   - background process with its own nested PTY (500, pgrp=500, tpgid=500)
	orders := [][]int{
		{200, 300, 400, 500},
		{500, 400, 300, 200},
		{300, 200, 500, 400},
	}
	for _, rootChildren := range orders {
		tree := fakeProcTree{
			children: map[int][]int{
				100: rootChildren,
				300: {301},
				301: {302},
			},
			cmdlines: map[int]string{
				100: "-zsh",
				200: "claude --resume",
				300: "npm run dev",
				301: "vite --port 5173",
				302: "esbuild --service",
				400: "postgres -D /tmp/db",
				500: "script -q /dev/null",
			},
			stats: map[int]procStat{
				100: {pgrp: 100, tpgid: 200},
				200: {pgrp: 200, tpgid: 200},
				300: {pgrp: 300, tpgid: 200},
				301: {pgrp: 300, tpgid: 200},
				302: {pgrp: 300, tpgid: 200},
				400: {pgrp: 400, tpgid: -1},
				500: {pgrp: 500, tpgid: 500},
			},
		}
		cmd, count := inspectPaneProcessWith(100, tree.inspector())
		if cmd != "claude --resume" || count != 6 {
			t.Errorf("order %v: got (%q, %d), want (%q, 6)", rootChildren, cmd, count, "claude --resume")
		}
	}
}

func TestInspectPaneProcessDeeperLeafBeatsParentWrapperRegardlessOfOrder(t *testing.T) {
	// Pane shell (100, tpgid=200) runs:
	//   - foreground wrapper (200: harness claude) -> node (201) -> go test (190, lower PID!)
	//   - foreground sibling leaf at depth 1 (250: tail -f log, higher PID!)
	orders := [][]int{
		{200, 250},
		{250, 200},
	}
	for _, rootChildren := range orders {
		tree := fakeProcTree{
			children: map[int][]int{
				100: rootChildren,
				200: {201},
				201: {190},
			},
			cmdlines: map[int]string{
				100: "bash",
				200: "harness claude",
				201: "node /opt/claude/cli.js",
				190: "go test -race ./internal/model/...",
				250: "tail -f /tmp/app.log",
			},
			stats: map[int]procStat{
				100: {pgrp: 100, tpgid: 200},
				200: {pgrp: 200, tpgid: 200},
				201: {pgrp: 200, tpgid: 200},
				190: {pgrp: 200, tpgid: 200},
				250: {pgrp: 200, tpgid: 200},
			},
		}
		cmd, count := inspectPaneProcessWith(100, tree.inspector())
		if cmd != "go test -race ./internal/model/..." || count != 4 {
			t.Errorf("order %v: got (%q, %d), want (%q, 4)", rootChildren, cmd, count, "go test -race ./internal/model/...")
		}
	}
}

func TestInspectPaneProcessSkipsInternalHelpers(t *testing.T) {
	// root (100) -> __shim (200, helper) -> claude (201) -> __osc-watch (202, helper)
	// Also sibling foreground leaf (150: cat) at depth 1: claude (depth 2) must remain
	// a leaf despite its __osc-watch helper child, so claude beats cat and __osc-watch.
	tree := fakeProcTree{
		children: map[int][]int{
			100: {200, 150},
			200: {201},
			201: {202},
		},
		cmdlines: map[int]string{
			100: "zsh",
			200: "tmux-spaces __shim claude claude",
			201: "claude --model opus",
			202: "tmux-spaces __osc-watch %7",
			150: "cat",
		},
		stats: map[int]procStat{
			100: {pgrp: 100, tpgid: 200},
			200: {pgrp: 200, tpgid: 200},
			201: {pgrp: 200, tpgid: 200},
			202: {pgrp: 200, tpgid: 200},
			150: {pgrp: 200, tpgid: 200},
		},
	}
	cmd, count := inspectPaneProcessWith(100, tree.inspector())
	if cmd != "claude --model opus" || count != 4 {
		t.Fatalf("got (%q, %d), want (%q, 4)", cmd, count, "claude --model opus")
	}

	// Only helper child under bare login shell -> empty cmd.
	bareShell := fakeProcTree{
		children: map[int][]int{100: {202}},
		cmdlines: map[int]string{
			100: "-zsh",
			202: "tmux-spaces __osc-watch %7",
		},
		stats: map[int]procStat{
			100: {pgrp: 100, tpgid: 100},
			202: {pgrp: 100, tpgid: 100},
		},
	}
	cmd, count = inspectPaneProcessWith(100, bareShell.inspector())
	if cmd != "" || count != 1 {
		t.Errorf("bare shell with helper: got (%q, %d), want (\"\", 1)", cmd, count)
	}

	// Only helper child under non-shell root -> falls back to root cmdline.
	editorRoot := fakeProcTree{
		children: map[int][]int{100: {202}},
		cmdlines: map[int]string{
			100: "nvim internal/model/proc_linux.go",
			202: "tmux-spaces __osc-watch %7",
		},
		stats: map[int]procStat{
			100: {pgrp: 100, tpgid: 100},
			202: {pgrp: 100, tpgid: 100},
		},
	}
	cmd, count = inspectPaneProcessWith(100, editorRoot.inspector())
	if cmd != "nvim internal/model/proc_linux.go" || count != 1 {
		t.Errorf("editor root with helper: got (%q, %d), want (nvim..., 1)", cmd, count)
	}
}

func TestInspectPaneProcessBoundsAndDeterministicTieBreak(t *testing.T) {
	// Equal depth/leaf/foreground siblings: highest PID wins regardless of child order.
	for _, order := range [][]int{{201, 202}, {202, 201}} {
		tree := fakeProcTree{
			children: map[int][]int{100: order},
			cmdlines: map[int]string{
				100: "bash",
				201: "grep foo",
				202: "less -R",
			},
			stats: map[int]procStat{
				100: {pgrp: 100, tpgid: 201},
				201: {pgrp: 201, tpgid: 201},
				202: {pgrp: 201, tpgid: 201},
			},
		}
		cmd, count := inspectPaneProcessWith(100, tree.inspector())
		if cmd != "less -R" || count != 2 {
			t.Errorf("tie-break order %v: got (%q, %d), want (\"less -R\", 2)", order, cmd, count)
		}
	}

	// Depth limit <= 4: depth 5 descendant is not traversed.
	deepTree := fakeProcTree{
		children: map[int][]int{
			100: {101},
			101: {102},
			102: {103},
			103: {104},
			104: {105},
		},
		cmdlines: map[int]string{
			100: "bash",
			101: "d1",
			102: "d2",
			103: "d3",
			104: "d4",
			105: "d5",
		},
		stats: map[int]procStat{
			100: {pgrp: 100, tpgid: 101},
			101: {pgrp: 101, tpgid: 101},
			102: {pgrp: 101, tpgid: 101},
			103: {pgrp: 101, tpgid: 101},
			104: {pgrp: 101, tpgid: 101},
			105: {pgrp: 101, tpgid: 101},
		},
	}
	cmd, count := inspectPaneProcessWith(100, deepTree.inspector())
	if cmd != "d4" || count != 4 {
		t.Errorf("depth bound: got (%q, %d), want (\"d4\", 4)", cmd, count)
	}

	// Node limit <= 32.
	wideChildren := make([]int, 40)
	cmdlines := map[int]string{100: "bash"}
	stats := map[int]procStat{100: {pgrp: 100, tpgid: 200}}
	for i := 0; i < 40; i++ {
		pid := 200 + i
		wideChildren[i] = pid
		cmdlines[pid] = "worker-" + strconv.Itoa(i)
		stats[pid] = procStat{pgrp: 200, tpgid: 200}
	}
	wideTree := fakeProcTree{
		children: map[int][]int{100: wideChildren},
		cmdlines: cmdlines,
		stats:    stats,
	}
	cmd, count = inspectPaneProcessWith(100, wideTree.inspector())
	if count != 32 || cmd != "worker-31" {
		t.Errorf("node cap: got (%q, %d), want (\"worker-31\", 32)", cmd, count)
	}
}

package model

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

// fakeTelemetryPlugin is a lightweight test plugin implementing optional
// telemetry capabilities (SessionTelemetryProvider, QuotaProvider, MCPActivityProvider).
type fakeTelemetryPlugin struct {
	agent.BasePlugin
	sessionFn func(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error)
	quotaFn   func(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error)
	mcpFn     func(ctx context.Context, snap *agent.ProcessSnapshot) ([]agent.LiveMCPServer, error)
}

func (f *fakeTelemetryPlugin) SessionTelemetry() agent.SessionTelemetryProvider {
	if f.sessionFn == nil {
		return nil
	}
	return fakeSessionProvider{fn: f.sessionFn}
}

func (f *fakeTelemetryPlugin) QuotaProvider() agent.QuotaProvider {
	if f.quotaFn == nil {
		return nil
	}
	return fakeQuotaProvider{fn: f.quotaFn}
}

func (f *fakeTelemetryPlugin) MCPActivityProvider() agent.MCPActivityProvider {
	if f.mcpFn == nil {
		return nil
	}
	return fakeMCPProvider{fn: f.mcpFn}
}

type fakeSessionProvider struct {
	fn func(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error)
}

func (p fakeSessionProvider) CollectSessionMetrics(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
	return p.fn(ctx, q)
}

type fakeQuotaProvider struct {
	fn func(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error)
}

func (p fakeQuotaProvider) FetchQuota(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error) {
	return p.fn(ctx, homeDirs)
}

type fakeMCPProvider struct {
	fn func(ctx context.Context, snap *agent.ProcessSnapshot) ([]agent.LiveMCPServer, error)
}

func (p fakeMCPProvider) CollectLiveMCPServers(ctx context.Context, snap *agent.ProcessSnapshot) ([]agent.LiveMCPServer, error) {
	return p.fn(ctx, snap)
}

func withStubProcHooks(
	t *testing.T,
	capture func(scanPorts bool) *procTableSnapshot,
	kill func(pid int, expectedCmd string) error,
) {
	t.Helper()
	origCapture := captureProcSnapshotFn
	origKill := killOrphanPortFn
	if capture != nil {
		captureProcSnapshotFn = capture
	}
	if kill != nil {
		killOrphanPortFn = kill
	}
	t.Cleanup(func() {
		captureProcSnapshotFn = origCapture
		killOrphanPortFn = origKill
	})
}

func TestTelemetryTokenRateDeltasAndCap(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	var activeTokens int64 = 10_000
	claude := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
		sessionFn: func(_ context.Context, _ agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
			return &agent.SessionMetrics{
				Agent:     "claude",
				SessionID: "sess-1",
				Usage: agent.TokenUsage{
					InputTokens:     activeTokens,
					CacheReadTokens: 50_000, // cache reads must not skew active token rate
				},
			}, nil
		},
	}
	reg := agent.NewRegistry(claude)
	col := NewTelemetryCollector(reg)

	now := time.Unix(1_700_000_000, 0)
	col.nowFn = func() time.Time { return now }

	spaces := []Space{{
		Name: "work",
		Panes: []Pane{{
			ID:     "%1",
			App:    "claude",
			Status: StatusWorking,
			PID:    100,
			Hook:   &HookSummary{Agent: "claude", SessionID: "sess-1"},
		}},
	}}

	// Tick 1: first appearance initializes prev = cur -> 0 delta (no startup spike).
	snap1 := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if snap1.Context.TokensPerMin != 0 {
		t.Fatalf("tick 1 TokensPerMin = %v, want 0", snap1.Context.TokensPerMin)
	}
	if len(snap1.Context.RateHistory) != 1 || snap1.Context.RateHistory[0] != 0 {
		t.Fatalf("tick 1 RateHistory = %v, want [0]", snap1.Context.RateHistory)
	}

	// Tick 2 (+10s): +1500 active tokens.
	now = now.Add(10 * time.Second)
	activeTokens += 1500
	snap2 := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if snap2.Context.TokensPerMin != 1500 {
		t.Fatalf("tick 2 TokensPerMin = %v, want 1500", snap2.Context.TokensPerMin)
	}

	// Tick 3 (+20s): +500 active tokens -> rolling 60s sum = 2000.
	now = now.Add(20 * time.Second)
	activeTokens += 500
	snap3 := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if snap3.Context.TokensPerMin != 2000 {
		t.Fatalf("tick 3 TokensPerMin = %v, want 2000", snap3.Context.TokensPerMin)
	}

	// Tick 4 (+55s from tick 3 -> tick 2 is now 75s old and drops out, tick 3 is 55s old and stays).
	now = now.Add(55 * time.Second)
	activeTokens += 300
	snap4 := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if snap4.Context.TokensPerMin != 800 {
		t.Fatalf("tick 4 TokensPerMin = %v, want 800 (500+300)", snap4.Context.TokensPerMin)
	}

	// Verify RateHistory is capped at 200 entries.
	for i := 0; i < 250; i++ {
		now = now.Add(time.Second)
		activeTokens += 10
		col.Collect(context.Background(), spaces, t.TempDir(), false)
	}
	snapFinal := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if len(snapFinal.Context.RateHistory) != maxRateHistoryLen {
		t.Fatalf("RateHistory len = %d, want %d", len(snapFinal.Context.RateHistory), maxRateHistoryLen)
	}
}

func TestTelemetryContextWarningsAndHistoryBounds(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	longHist := make([]int64, 80)
	for i := range longHist {
		longHist[i] = int64(i + 1)
	}

	metricsBySID := map[string]*agent.SessionMetrics{
		"s-ok": {
			SessionID:      "s-ok",
			ContextTokens:  100_000,
			ContextWindow:  200_000, // 50%
			TurnCount:      4,
			Usage:          agent.TokenUsage{InputTokens: 4000, OutputTokens: 2000},
			TokenHistory:   longHist,
			ContextHistory: longHist,
		},
		"s-high": {
			SessionID:     "s-high",
			ContextTokens: 150_000,
			ContextWindow: 200_000, // 75%
		},
		"s-crit": {
			SessionID:     "s-crit",
			ContextTokens: 180_000,
			ContextWindow: 200_000, // 90%
		},
	}

	claude := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
		sessionFn: func(_ context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
			return metricsBySID[q.SessionID], nil
		},
	}
	col := NewTelemetryCollector(agent.NewRegistry(claude))
	spaces := []Space{{
		Name: "dev",
		Panes: []Pane{
			{ID: "%1", App: "claude", Hook: &HookSummary{Agent: "claude", SessionID: "s-ok"}},
			{ID: "%2", App: "claude", Hook: &HookSummary{Agent: "claude", SessionID: "s-high"}},
			{ID: "%3", App: "claude", Hook: &HookSummary{Agent: "claude", SessionID: "s-crit"}},
		},
	}}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if len(snap.Sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(snap.Sessions))
	}

	sOK := snap.SessionForPane("%1")
	if sOK.ContextPct != 50 || sOK.Warning != ContextWarningNone {
		t.Fatalf("s-ok = %.1f%% %q, want 50%% none", sOK.ContextPct, sOK.Warning)
	}
	if sOK.TotalTokens != 6000 || sOK.AvgTokensPerTurn != 1500 {
		t.Fatalf("s-ok tokens=%d avg=%d, want 6000/1500", sOK.TotalTokens, sOK.AvgTokensPerTurn)
	}
	if len(sOK.TokenHistory) != maxSessionHistLen || sOK.TokenHistory[0] != 17 || sOK.TokenHistory[63] != 80 {
		t.Fatalf("s-ok TokenHistory bounded incorrectly: len=%d first=%d last=%d",
			len(sOK.TokenHistory), sOK.TokenHistory[0], sOK.TokenHistory[len(sOK.TokenHistory)-1])
	}
	if len(sOK.ContextHistory) != maxSessionHistLen {
		t.Fatalf("s-ok ContextHistory len = %d, want %d", len(sOK.ContextHistory), maxSessionHistLen)
	}

	sHigh := snap.SessionForPane("%2")
	if sHigh.ContextPct != 75 || sHigh.Warning != ContextWarningHigh {
		t.Fatalf("s-high = %.1f%% %q, want 75%% %q", sHigh.ContextPct, sHigh.Warning, ContextWarningHigh)
	}

	sCrit := snap.SessionForPane("%3")
	if sCrit.ContextPct != 90 || sCrit.Warning != ContextWarningCrit {
		t.Fatalf("s-crit = %.1f%% %q, want 90%% %q", sCrit.ContextPct, sCrit.Warning, ContextWarningCrit)
	}
}

func TestTelemetryDuplicatePortsAndOrphanLifecycle(t *testing.T) {
	currentSnap := &procTableSnapshot{
		ProcessSnapshot: &agent.ProcessSnapshot{
			Processes: map[int]agent.OSProcess{
				100: {PID: 100, PPID: 1, Comm: "zsh", Args: []string{"zsh"}, MemKB: 1000},
				101: {PID: 101, PPID: 100, Comm: "claude", Args: []string{"claude"}, MemKB: 5000},
				102: {PID: 102, PPID: 101, Comm: "node", Args: []string{"node", "server.js"}, MemKB: 2000},
				200: {PID: 200, PPID: 1, Comm: "zsh", Args: []string{"zsh"}, MemKB: 1000},
				201: {PID: 201, PPID: 200, Comm: "node", Args: []string{"node", "dup.js"}, MemKB: 2000},
			},
			Children: map[int][]int{
				100: {101},
				101: {102},
				200: {201},
			},
		},
		PortsByPID: map[int][]int{
			102: {3000, 5173},
			201: {3000},
		},
	}

	var killedPIDs []int
	var killedCmds []string
	withStubProcHooks(t,
		func(_ bool) *procTableSnapshot { return currentSnap },
		func(pid int, expectedCmd string) error {
			killedPIDs = append(killedPIDs, pid)
			killedCmds = append(killedCmds, expectedCmd)
			return nil
		},
	)

	reg := agent.NewRegistry(&fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
	})
	col := NewTelemetryCollector(reg)

	spaces := []Space{{
		Name: "app",
		Panes: []Pane{
			{ID: "%1", App: "claude", PID: 100, CWD: "/repo/a", Hook: &HookSummary{Agent: "claude", SessionID: "s1"}},
			{ID: "%2", App: "claude", PID: 200, CWD: "/repo/b", Hook: &HookSummary{Agent: "claude", SessionID: "s2"}},
		},
	}}

	// Tick 1: both sessions live -> port 3000 duplicated, 5173 unique, no orphans.
	snap1 := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if len(snap1.Ports) != 3 {
		t.Fatalf("got %d ports, want 3: %+v", len(snap1.Ports), snap1.Ports)
	}
	if snap1.Ports[0].Port != 3000 || !snap1.Ports[0].Duplicate || snap1.Ports[1].Port != 3000 || !snap1.Ports[1].Duplicate {
		t.Fatalf("expected port 3000 entries marked Duplicate: %+v", snap1.Ports)
	}
	if snap1.Ports[2].Port != 5173 || snap1.Ports[2].Duplicate {
		t.Fatalf("expected port 5173 non-duplicate: %+v", snap1.Ports[2])
	}
	if len(snap1.OrphanPorts) != 0 {
		t.Fatalf("expected 0 orphans while live, got %+v", snap1.OrphanPorts)
	}
	if s1 := snap1.SessionForPane("%1"); s1 == nil || s1.MemKB != 8000 {
		t.Fatalf("expected s1 MemKB=8000 (1000+5000+2000), got %+v", s1)
	}

	// Session %1 ends, but child PID 102 is still alive and listening on 3000 & 5173.
	spacesAfter := []Space{{
		Name: "app",
		Panes: []Pane{
			{ID: "%2", App: "claude", PID: 200, CWD: "/repo/b", Hook: &HookSummary{Agent: "claude", SessionID: "s2"}},
		},
	}}
	snap2 := col.Collect(context.Background(), spacesAfter, t.TempDir(), true)
	if len(snap2.OrphanPorts) != 2 {
		t.Fatalf("expected 2 orphan ports (3000, 5173), got %+v", snap2.OrphanPorts)
	}
	if snap2.OrphanPorts[0].Port != 3000 || snap2.OrphanPorts[1].Port != 5173 || snap2.OrphanPorts[0].PID != 102 {
		t.Fatalf("unexpected orphan ports: %+v", snap2.OrphanPorts)
	}

	// Safe KillOrphanPorts kills PID 102 with expected command.
	killed, err := col.KillOrphanPorts(context.Background(), []OrphanPort{snap2.OrphanPorts[1]})
	if err != nil || killed != 1 {
		t.Fatalf("KillOrphanPorts = (%d, %v), want (1, nil)", killed, err)
	}
	if len(killedPIDs) != 1 || killedPIDs[0] != 102 || killedCmds[0] != "node server.js" {
		t.Fatalf("unexpected kill args: pids=%v cmds=%v", killedPIDs, killedCmds)
	}

	// Port 3000 on PID 102 closes -> pruned on next Collect.
	delete(currentSnap.PortsByPID, 102)
	snap3 := col.Collect(context.Background(), spacesAfter, t.TempDir(), true)
	if len(snap3.OrphanPorts) != 0 {
		t.Fatalf("expected orphans pruned after port closes, got %+v", snap3.OrphanPorts)
	}
}

func TestTelemetryRateLimitPromotionAndQuotaOrder(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	opencode := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "opencode"}},
		quotaFn: func(_ context.Context, _ []string) (*agent.QuotaInfo, error) {
			return &agent.QuotaInfo{Agent: "opencode", Primary: &agent.QuotaWindow{UsedPct: 40}}, nil
		},
	}
	codex := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "codex"}},
		quotaFn: func(_ context.Context, _ []string) (*agent.QuotaInfo, error) {
			return &agent.QuotaInfo{Agent: "codex", Primary: &agent.QuotaWindow{UsedPct: 89.5}}, nil
		},
	}
	claude := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
		quotaFn: func(_ context.Context, _ []string) (*agent.QuotaInfo, error) {
			return &agent.QuotaInfo{Agent: "claude", Primary: &agent.QuotaWindow{UsedPct: 95.0}}, nil
		},
	}

	// Register in reverse order to verify deterministic sorting (claude, codex, opencode).
	col := NewTelemetryCollector(agent.NewRegistry(opencode, codex, claude))
	spaces := []Space{{
		Name: "main",
		Panes: []Pane{
			{ID: "%1", App: "claude", Status: StatusIdle, Hook: &HookSummary{Agent: "claude", SessionID: "c-idle"}},
			{ID: "%2", App: "claude", Status: StatusWorking, Hook: &HookSummary{Agent: "claude", SessionID: "c-work"}},
			{ID: "%3", App: "codex", Status: StatusIdle, Hook: &HookSummary{Agent: "codex", SessionID: "x-idle"}},
		},
	}}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if len(snap.Quotas) != 3 || snap.Quotas[0].Agent != "claude" || snap.Quotas[1].Agent != "codex" || snap.Quotas[2].Agent != "opencode" {
		t.Fatalf("unexpected quota ordering: %+v", snap.Quotas)
	}
	if !snap.SessionForPane("%1").RateLimited {
		t.Fatal("expected idle claude session to be marked RateLimited")
	}
	if snap.SessionForPane("%2").RateLimited {
		t.Fatal("expected working claude session NOT to be marked RateLimited")
	}
	if snap.SessionForPane("%3").RateLimited {
		t.Fatal("expected codex session (89.5% <= 90%) NOT to be marked RateLimited")
	}
}

func TestTelemetryProjectGrouping(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	claude := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
		sessionFn: func(_ context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
			if q.SessionID == "s-fallback" {
				return &agent.SessionMetrics{GitBranch: "feat/from-metrics"}, nil
			}
			return &agent.SessionMetrics{}, nil
		},
	}
	col := NewTelemetryCollector(agent.NewRegistry(claude))
	spaces := []Space{{
		Name: "dev",
		Panes: []Pane{
			{
				ID: "%1", App: "claude", Status: StatusWorking, CWD: "/work/clean-repo/sub",
				Git:  &GitInfo{RepoRoot: "/work/clean-repo", RepoName: "clean-repo", Branch: "main", IsDirty: false},
				Hook: &HookSummary{Agent: "claude", SessionID: "s1"},
			},
			{
				ID: "%2", App: "claude", Status: StatusIdle, CWD: "/work/clean-repo",
				Git:  &GitInfo{RepoRoot: "/work/clean-repo", RepoName: "clean-repo", Branch: "main", IsDirty: false},
				Hook: &HookSummary{Agent: "claude", SessionID: "s2"},
			},
			{
				ID: "%3", App: "claude", Status: StatusWorking, CWD: "/work/dirty-repo",
				Git:  &GitInfo{RepoRoot: "/work/dirty-repo", RepoName: "dirty-repo", Branch: "feat/ui", IsDirty: true, FilesAdded: 2, FilesModified: 3},
				Hook: &HookSummary{Agent: "claude", SessionID: "s3"},
			},
			{
				ID: "%4", App: "claude", Status: StatusIdle, CWD: "/work/no-git-pane",
				Hook: &HookSummary{Agent: "claude", SessionID: "s-fallback"},
			},
		},
	}}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if len(snap.Projects) != 3 {
		t.Fatalf("got %d projects, want 3: %+v", len(snap.Projects), snap.Projects)
	}

	pClean := snap.Projects[0]
	if pClean.Name != "clean-repo" || !pClean.HasGit || !pClean.IsClean || pClean.SessionCount != 2 || pClean.ActiveCount != 1 {
		t.Fatalf("unexpected clean project: %+v", pClean)
	}
	pDirty := snap.Projects[1]
	if pDirty.Name != "dirty-repo" || !pDirty.HasGit || pDirty.IsClean || pDirty.FilesAdded != 2 || pDirty.FilesModified != 3 || pDirty.SessionCount != 1 || pDirty.ActiveCount != 1 {
		t.Fatalf("unexpected dirty project: %+v", pDirty)
	}
	pFallback := snap.Projects[2]
	if pFallback.Name != "no-git-pane" || pFallback.Branch != "feat/from-metrics" || !pFallback.HasGit || !pFallback.IsClean {
		t.Fatalf("unexpected fallback-branch project: %+v", pFallback)
	}
}

func TestTelemetrySlowTickThrottlingAndPIDChangeRefresh(t *testing.T) {
	var (
		portScans  atomic.Int32
		quotaCalls atomic.Int32
		mcpCalls   atomic.Int32
	)
	withStubProcHooks(t, func(scanPorts bool) *procTableSnapshot {
		if scanPorts {
			portScans.Add(1)
		}
		return newEmptyProcTableSnapshot()
	}, nil)

	codex := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "codex"}},
		quotaFn: func(_ context.Context, _ []string) (*agent.QuotaInfo, error) {
			quotaCalls.Add(1)
			return &agent.QuotaInfo{Agent: "codex", Primary: &agent.QuotaWindow{UsedPct: 12}}, nil
		},
		mcpFn: func(_ context.Context, _ *agent.ProcessSnapshot) ([]agent.LiveMCPServer, error) {
			mcpCalls.Add(1)
			return []agent.LiveMCPServer{
				{Agent: "codex", PID: 500, LastActivityAt: 100},
				{Agent: "codex", PID: 400, LastActivityAt: 200},
				{Agent: "codex", PID: 300, LastActivityAt: 200},
			}, nil
		},
	}

	col := NewTelemetryCollector(agent.NewRegistry(codex))
	spaces := []Space{{
		Name:  "s",
		Panes: []Pane{{ID: "%1", App: "codex", PID: 1000, Hook: &HookSummary{Agent: "codex", SessionID: "s1"}}},
	}}

	// Tick 1: slow tick runs.
	snap1 := col.Collect(context.Background(), spaces, t.TempDir(), false)
	if portScans.Load() != 1 || quotaCalls.Load() != 1 || mcpCalls.Load() != 1 {
		t.Fatalf("tick 1 counts = (%d, %d, %d), want (1, 1, 1)", portScans.Load(), quotaCalls.Load(), mcpCalls.Load())
	}
	// Verify MCP sorting: LastActivityAt desc, then PID asc.
	if len(snap1.MCPServers) != 3 || snap1.MCPServers[0].PID != 300 || snap1.MCPServers[1].PID != 400 || snap1.MCPServers[2].PID != 500 {
		t.Fatalf("unexpected MCP server order: %+v", snap1.MCPServers)
	}

	// Ticks 2..5: fast ticks reuse caches.
	for tick := 2; tick <= 5; tick++ {
		snap := col.Collect(context.Background(), spaces, t.TempDir(), false)
		if portScans.Load() != 1 || quotaCalls.Load() != 1 || mcpCalls.Load() != 1 {
			t.Fatalf("tick %d unexpectedly ran slow poll: (%d, %d, %d)", tick, portScans.Load(), quotaCalls.Load(), mcpCalls.Load())
		}
		if len(snap.Quotas) != 1 || len(snap.MCPServers) != 3 {
			t.Fatalf("tick %d did not preserve cached quotas/mcp", tick)
		}
	}

	// Tick 6: 5th tick after tick 1 -> slow poll runs again.
	col.Collect(context.Background(), spaces, t.TempDir(), false)
	if portScans.Load() != 2 || quotaCalls.Load() != 2 || mcpCalls.Load() != 2 {
		t.Fatalf("tick 6 counts = (%d, %d, %d), want (2, 2, 2)", portScans.Load(), quotaCalls.Load(), mcpCalls.Load())
	}

	// PID set change on fast tick -> triggers slow poll immediately.
	spaces[0].Panes[0].PID = 2000
	col.Collect(context.Background(), spaces, t.TempDir(), false)
	if portScans.Load() != 3 || quotaCalls.Load() != 3 || mcpCalls.Load() != 3 {
		t.Fatalf("PID change counts = (%d, %d, %d), want (3, 3, 3)", portScans.Load(), quotaCalls.Load(), mcpCalls.Load())
	}

	// Explicit force=true on fast tick -> triggers slow poll immediately.
	col.Collect(context.Background(), spaces, t.TempDir(), true)
	if portScans.Load() != 4 || quotaCalls.Load() != 4 || mcpCalls.Load() != 4 {
		t.Fatalf("force counts = (%d, %d, %d), want (4, 4, 4)", portScans.Load(), quotaCalls.Load(), mcpCalls.Load())
	}
}

func TestParseProcNetTCPAndSocketInode(t *testing.T) {
	raw := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 123456 1 00000000 100 0 0 10 0
   1: 00000000:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 789012 1 00000000 100 0 0 10 0
   2: 0100007F:1F90 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 999999 1 00000000 100 0 0 10 0
`
	inodes := make(map[uint64]int)
	parseProcNetTCP(raw, inodes)

	if len(inodes) != 2 {
		t.Fatalf("got %d listening inodes, want 2: %+v", len(inodes), inodes)
	}
	if inodes[123456] != 3000 { // 0x0BB8 = 3000
		t.Fatalf("inode 123456 port = %d, want 3000", inodes[123456])
	}
	if inodes[789012] != 5173 { // 0x1435 = 5173
		t.Fatalf("inode 789012 port = %d, want 5173", inodes[789012])
	}
	if _, exists := inodes[999999]; exists {
		t.Fatal("ESTABLISHED socket (state 01) should not be included")
	}

	if inode, ok := parseSocketInode("socket:[123456]"); !ok || inode != 123456 {
		t.Fatalf("parseSocketInode = (%d, %v), want (123456, true)", inode, ok)
	}
	if _, ok := parseSocketInode("/tmp/file.jsonl"); ok {
		t.Fatal("expected non-socket target to return false")
	}
}

func TestParseTelemetryProcStatWithSpacesAndParens(t *testing.T) {
	// comm contains both spaces and nested parentheses: "(Web Content (1))"
	// Fields after ')':
	// 0:S 1:4242 2:100 3:100 4:0 5:-1 6:4194304 7:10 8:0 9:0 10:0 11:5 12:2 13:0 14:0 15:20 16:0 17:4 18:0 19:9000 20:1048576 21:2048
	line := "9876 (Web Content (1)) S 4242 100 100 0 -1 4194304 10 0 0 0 5 2 0 0 20 0 4 0 9000 1048576 2048 18446744073709551615"
	comm, ppid, rss, ok := parseTelemetryProcStat(line)
	if !ok {
		t.Fatal("expected parseTelemetryProcStat to succeed")
	}
	if comm != "Web Content (1)" {
		t.Fatalf("comm = %q, want %q", comm, "Web Content (1)")
	}
	if ppid != 4242 {
		t.Fatalf("ppid = %d, want 4242", ppid)
	}
	if rss != 2048 {
		t.Fatalf("rss = %d, want 2048", rss)
	}

	if _, _, _, ok := parseTelemetryProcStat("malformed"); ok {
		t.Fatal("expected malformed stat line to fail")
	}
}

func TestTelemetryWorktreeAwareProjectGrouping(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	reg := agent.NewRegistry(&fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
	})
	col := NewTelemetryCollector(reg)

	spaces := []Space{
		{
			Name: "tab-1",
			Panes: []Pane{
				// Repo 1, worktree A (clean)
				{
					ID: "%1", App: "claude", Status: StatusWorking, CWD: "/work/spaces",
					Git: &GitInfo{
						RepoKey:      "github.com/org/spaces",
						MainRepoRoot: "/work/spaces",
						RepoRoot:     "/work/spaces",
						RepoName:     "spaces",
						Branch:       "main",
						IsDirty:      false,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s1"},
				},
				// Repo 1, worktree B (dirty)
				{
					ID: "%2", App: "claude", Status: StatusWorking, CWD: "/work/spaces-wt-ui/sub",
					Git: &GitInfo{
						RepoKey:       "github.com/org/spaces",
						MainRepoRoot:  "/work/spaces",
						RepoRoot:      "/work/spaces-wt-ui",
						RepoName:      "spaces",
						IsWorktree:    true,
						Branch:        "feat/ui",
						IsDirty:       true,
						FilesAdded:    2,
						FilesModified: 3,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s2"},
				},
				// Repo 1, second pane in same worktree B (must not double-count worktree or files)
				{
					ID: "%3", App: "claude", Status: StatusIdle, CWD: "/work/spaces-wt-ui",
					Git: &GitInfo{
						RepoKey:       "github.com/org/spaces",
						MainRepoRoot:  "/work/spaces",
						RepoRoot:      "/work/spaces-wt-ui",
						RepoName:      "spaces",
						IsWorktree:    true,
						Branch:        "feat/ui",
						IsDirty:       true,
						FilesAdded:    2,
						FilesModified: 3,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s3"},
				},
			},
		},
		{
			Name: "tab-2",
			Panes: []Pane{
				// Repo 2: grouped by MainRepoRoot (no RepoKey, no RepoName -> basename of MainRepoRoot), both worktrees clean
				{
					ID: "%4", App: "claude", Status: StatusIdle, CWD: "/work/clean-multi",
					Git: &GitInfo{
						MainRepoRoot: "/work/clean-multi",
						RepoRoot:     "/work/clean-multi",
						Branch:       "main",
						IsDirty:      false,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s4"},
				},
				{
					ID: "%5", App: "claude", Status: StatusIdle, CWD: "/work/clean-multi-wt2",
					Git: &GitInfo{
						MainRepoRoot: "/work/clean-multi",
						RepoRoot:     "/work/clean-multi-wt2",
						IsWorktree:   true,
						Branch:       "feat/a",
						IsDirty:      false,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s5"},
				},
				// Repo 3: two dirty worktrees -> file counts aggregated across worktrees
				{
					ID: "%6", App: "claude", Status: StatusWorking, CWD: "/work/agg-repo",
					Git: &GitInfo{
						RepoKey:       "github.com/org/agg-repo",
						MainRepoRoot:  "/work/agg-repo",
						RepoRoot:      "/work/agg-repo",
						RepoName:      "agg-repo",
						Branch:        "main",
						IsDirty:       true,
						FilesAdded:    1,
						FilesModified: 4,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s6"},
				},
				{
					ID: "%7", App: "claude", Status: StatusIdle, CWD: "/work/agg-repo-wt2",
					Git: &GitInfo{
						RepoKey:       "github.com/org/agg-repo",
						MainRepoRoot:  "/work/agg-repo",
						RepoRoot:      "/work/agg-repo-wt2",
						RepoName:      "agg-repo",
						IsWorktree:    true,
						Branch:        "feat/b",
						IsDirty:       true,
						FilesAdded:    2,
						FilesModified: 1,
					},
					Hook: &HookSummary{Agent: "claude", SessionID: "s7"},
				},
			},
		},
	}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if len(snap.Projects) != 3 {
		t.Fatalf("got %d projects, want 3: %+v", len(snap.Projects), snap.Projects)
	}

	p1 := snap.Projects[0]
	if p1.Name != "spaces" || p1.RepoRoot != "/work/spaces" || p1.WorktreeCount != 2 ||
		p1.SessionCount != 3 || p1.ActiveCount != 2 ||
		p1.FilesAdded != 2 || p1.FilesModified != 3 || p1.IsClean || !p1.HasGit {
		t.Fatalf("unexpected project 1: %+v", p1)
	}

	p2 := snap.Projects[1]
	if p2.Name != "clean-multi" || p2.RepoRoot != "/work/clean-multi" || p2.WorktreeCount != 2 ||
		p2.SessionCount != 2 || p2.ActiveCount != 0 ||
		p2.FilesAdded != 0 || p2.FilesModified != 0 || !p2.IsClean || !p2.HasGit {
		t.Fatalf("unexpected project 2: %+v", p2)
	}

	p3 := snap.Projects[2]
	if p3.Name != "agg-repo" || p3.WorktreeCount != 2 ||
		p3.SessionCount != 2 || p3.ActiveCount != 1 ||
		p3.FilesAdded != 3 || p3.FilesModified != 5 || p3.IsClean {
		t.Fatalf("unexpected project 3: %+v", p3)
	}
}

func TestTelemetryCostPricingFallback(t *testing.T) {
	withStubProcHooks(t, func(_ bool) *procTableSnapshot {
		return newEmptyProcTableSnapshot()
	}, nil)

	// Plugin with NO SessionTelemetryProvider (hook-only sessions).
	reg := agent.NewRegistry(&fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
	})
	col := NewTelemetryCollector(reg)

	spaces := []Space{{
		Name: "cost",
		Panes: []Pane{
			{
				ID: "%1", App: "claude", Status: StatusWorking,
				Hook: &HookSummary{
					Agent:     "claude",
					SessionID: "hook-known-model",
					Model:     "claude-sonnet-4-6",
					Usage: &HookUsage{
						InputTokens:  1_000_000, // $3.00
						OutputTokens: 100_000,   // $1.50
					},
				},
			},
			{
				ID: "%2", App: "claude", Status: StatusIdle,
				Hook: &HookSummary{
					Agent:     "claude",
					SessionID: "hook-unknown-model",
					Model:     "unknown-local-model",
					Usage: &HookUsage{
						InputTokens:  500_000,
						OutputTokens: 50_000,
					},
				},
			},
			{
				ID: "%3", App: "claude", Status: StatusIdle,
				Hook: &HookSummary{
					Agent:     "claude",
					SessionID: "hook-explicit-cost",
					Model:     "unknown-local-model",
					Usage: &HookUsage{
						InputTokens: 1000,
						CostUSD:     0.75,
					},
				},
			},
		},
	}}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)

	s1 := snap.SessionForPane("%1")
	if s1 == nil {
		t.Fatal("missing session %1")
	}
	if !s1.Usage.CostPriced {
		t.Fatalf("expected known hook model to be CostPriced=true, got %+v", s1.Usage)
	}
	if math.Abs(s1.Usage.CostUSD-4.50) > 1e-9 {
		t.Fatalf("s1 CostUSD = %v, want 4.50", s1.Usage.CostUSD)
	}

	s2 := snap.SessionForPane("%2")
	if s2 == nil {
		t.Fatal("missing session %2")
	}
	if s2.Usage.CostPriced || s2.Usage.CostUSD != 0 {
		t.Fatalf("expected unpriced model to keep CostPriced=false and CostUSD=0, got %+v", s2.Usage)
	}

	s3 := snap.SessionForPane("%3")
	if s3 == nil {
		t.Fatal("missing session %3")
	}
	if !s3.Usage.CostPriced || math.Abs(s3.Usage.CostUSD-0.75) > 1e-9 {
		t.Fatalf("expected explicit hook CostUSD=0.75 to set CostPriced=true, got %+v", s3.Usage)
	}
}

func TestTelemetryContextAggregates(t *testing.T) {
	procSnap := &procTableSnapshot{
		ProcessSnapshot: &agent.ProcessSnapshot{
			Processes: map[int]agent.OSProcess{
				100: {PID: 100, PPID: 1, Comm: "claude", Args: []string{"claude"}, MemKB: 12000},
				200: {PID: 200, PPID: 1, Comm: "claude", Args: []string{"claude"}, MemKB: 8000},
				300: {PID: 300, PPID: 1, Comm: "claude", Args: []string{"claude"}, MemKB: 5000},
			},
			Children: map[int][]int{},
		},
		PortsByPID: map[int][]int{},
	}
	withStubProcHooks(t, func(_ bool) *procTableSnapshot { return procSnap }, nil)

	metricsBySID := map[string]*agent.SessionMetrics{
		"s1": {
			SessionID:     "s1",
			Model:         "claude-sonnet-4-6",
			ContextTokens: 100_000,
			ContextWindow: 200_000, // 50%
			Usage:         agent.TokenUsage{InputTokens: 1000, CostUSD: 1.25, CostPriced: true},
		},
		"s2": {
			SessionID:     "s2",
			Model:         "claude-sonnet-4-6",
			ContextTokens: 150_000,
			ContextWindow: 200_000, // 75%
			Usage:         agent.TokenUsage{InputTokens: 2000, CostUSD: 2.50, CostPriced: true},
		},
		"s3": {
			SessionID: "s3",
			// No model, no context window, no cost -> excluded from AvgContextPct denominator.
		},
	}

	claude := &fakeTelemetryPlugin{
		BasePlugin: agent.BasePlugin{PluginSpec: agent.Spec{ID: "claude"}},
		sessionFn: func(_ context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
			return metricsBySID[q.SessionID], nil
		},
	}
	col := NewTelemetryCollector(agent.NewRegistry(claude))

	spaces := []Space{{
		Name: "agg",
		Panes: []Pane{
			{ID: "%1", App: "claude", PID: 100, Hook: &HookSummary{Agent: "claude", SessionID: "s1"}},
			{ID: "%2", App: "claude", PID: 200, Hook: &HookSummary{Agent: "claude", SessionID: "s2"}},
			{ID: "%3", App: "claude", PID: 300, Hook: &HookSummary{Agent: "claude", SessionID: "s3"}},
		},
	}}

	snap := col.Collect(context.Background(), spaces, t.TempDir(), true)
	if math.Abs(snap.Context.TotalCostUSD-3.75) > 1e-9 {
		t.Fatalf("TotalCostUSD = %v, want 3.75", snap.Context.TotalCostUSD)
	}
	if snap.Context.TotalMemKB != 25000 {
		t.Fatalf("TotalMemKB = %d, want 25000", snap.Context.TotalMemKB)
	}
	if math.Abs(snap.Context.AvgContextPct-62.5) > 1e-9 {
		t.Fatalf("AvgContextPct = %v, want 62.5", snap.Context.AvgContextPct)
	}

	// Empty spaces -> all aggregates zero.
	emptySnap := col.Collect(context.Background(), nil, t.TempDir(), true)
	if emptySnap.Context.TotalCostUSD != 0 || emptySnap.Context.TotalMemKB != 0 || emptySnap.Context.AvgContextPct != 0 {
		t.Fatalf("expected zero aggregates for empty spaces, got %+v", emptySnap.Context)
	}
}

package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	slowPollInterval    = 5
	maxRateHistoryLen   = 200
	maxSessionHistLen   = 64
	rateLimitedQuotaPct = 90.0
	contextWarnHighPct  = 75.0
	contextWarnCritPct  = 90.0
	rateWindowDuration  = 60 * time.Second
)

// procTableSnapshot combines the shared agent.ProcessSnapshot with listening
// TCP ports discovered during a single-pass OS scan.
type procTableSnapshot struct {
	*agent.ProcessSnapshot
	PortsByPID map[int][]int
}

func newEmptyProcTableSnapshot() *procTableSnapshot {
	return &procTableSnapshot{
		ProcessSnapshot: &agent.ProcessSnapshot{
			Processes: make(map[int]agent.OSProcess),
			Children:  make(map[int][]int),
		},
		PortsByPID: make(map[int][]int),
	}
}

// Package-level hooks for deterministic unit tests without touching /proc.
var (
	captureProcSnapshotFn = captureProcessSnapshot
	killOrphanPortFn      = killOrphanPort
)

type descendantPort struct {
	PID     int
	Port    int
	Command string
}

type descendantInfo struct {
	TotalMemKB   int64
	AgentPID     int
	AgentCmdline []string
	AgentName    string
	Ports        []descendantPort
	AllPIDs      []int
}

// descendantTree walks rootPID and all its descendants via DFS (with a visited
// cycle guard), summing RSS memory, locating the best matching coding-agent
// process, and collecting all listening TCP ports owned by the tree.
func descendantTree(rootPID int, snap *procTableSnapshot, reg *agent.Registry) descendantInfo {
	var info descendantInfo
	if rootPID <= 0 || snap == nil || snap.ProcessSnapshot == nil {
		return info
	}

	visited := make(map[int]bool)
	stack := []int{rootPID}
	type portKey struct {
		pid  int
		port int
	}
	seenPorts := make(map[portKey]bool)

	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid <= 0 || visited[pid] {
			continue
		}
		visited[pid] = true
		info.AllPIDs = append(info.AllPIDs, pid)

		if proc, ok := snap.Processes[pid]; ok {
			info.TotalMemKB += proc.MemKB

			// Detect agent process (prefer descendant over pane root shell).
			if reg != nil {
				if matched := reg.DetectProcess(agent.ProcessInfo{
					PID:  proc.PID,
					Comm: proc.Comm,
					Args: proc.Args,
				}); matched != "" {
					if info.AgentPID == 0 || (info.AgentPID == rootPID && pid != rootPID) {
						info.AgentPID = pid
						info.AgentCmdline = proc.Args
						info.AgentName = matched
					}
				}
			}

			if ports := snap.PortsByPID[pid]; len(ports) > 0 {
				cmd := procCommandString(proc)
				for _, port := range ports {
					pk := portKey{pid: pid, port: port}
					if seenPorts[pk] {
						continue
					}
					seenPorts[pk] = true
					info.Ports = append(info.Ports, descendantPort{
						PID:     pid,
						Port:    port,
						Command: cmd,
					})
				}
			}
		}

		for _, child := range snap.Children[pid] {
			if !visited[child] {
				stack = append(stack, child)
			}
		}
	}

	if info.AgentPID == 0 {
		if rootProc, ok := snap.Processes[rootPID]; ok {
			info.AgentPID = rootPID
			info.AgentCmdline = rootProc.Args
		}
	}

	return info
}

func procCommandString(p agent.OSProcess) string {
	if len(p.Args) > 0 {
		return strings.Join(p.Args, " ")
	}
	return strings.TrimSpace(p.Comm)
}

type rateSample struct {
	ts    time.Time
	delta float64
}

type trackedPortKey struct {
	pid  int
	port int
}

type trackedPortChild struct {
	Port      int
	Command   string
	Project   string
	SessionID string
}

// TelemetryCollector aggregates shared OS process/port telemetry, per-agent
// session metrics, account quotas, project summaries, and live MCP servers
// into an immutable TelemetrySnapshot. It is safe for concurrent use.
type TelemetryCollector struct {
	mu                  sync.Mutex
	reg                 *agent.Registry
	nowFn               func() time.Time
	tickCount           int
	cachedPorts         map[int][]int
	cachedPortPIDs      []int
	cachedQuotas        []agent.QuotaInfo
	cachedMCPServers    []agent.LiveMCPServer
	prevActiveTokens    map[string]int64
	rateSamples         []rateSample
	trackedPortChildren map[trackedPortKey]trackedPortChild
}

// NewTelemetryCollector creates a TelemetryCollector backed by reg (or
// agent.DefaultRegistry() when reg is nil).
func NewTelemetryCollector(reg *agent.Registry) *TelemetryCollector {
	if reg == nil {
		reg = agent.DefaultRegistry()
	}
	return &TelemetryCollector{
		reg:                 reg,
		nowFn:               time.Now,
		tickCount:           slowPollInterval, // run slow poll on tick 1
		cachedPorts:         make(map[int][]int),
		prevActiveTokens:    make(map[string]int64),
		trackedPortChildren: make(map[trackedPortKey]trackedPortChild),
	}
}

// Collect gathers a fresh TelemetrySnapshot for the given spaces.
// Expensive scans (listening ports, quota providers, MCP activity providers)
// are throttled to every slowPollInterval ticks unless force is true or the
// sorted set of live pane/agent PIDs changed.
func (c *TelemetryCollector) Collect(ctx context.Context, spaces []Space, homeDir string, force bool) TelemetrySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.nowFn()
	scheduledSlow := force || c.tickCount >= slowPollInterval
	if scheduledSlow {
		c.tickCount = 1
	} else {
		c.tickCount++
	}

	procSnap := captureProcSnapshotFn(scheduledSlow)
	if procSnap == nil {
		procSnap = newEmptyProcTableSnapshot()
	}
	if procSnap.ProcessSnapshot == nil {
		procSnap.ProcessSnapshot = &agent.ProcessSnapshot{
			Processes: make(map[int]agent.OSProcess),
			Children:  make(map[int][]int),
		}
	}
	if procSnap.PortsByPID == nil {
		procSnap.PortsByPID = make(map[int][]int)
	}

	currentPIDs := collectLivePIDSet(spaces, procSnap, c.reg)
	pidsChanged := !slices.Equal(currentPIDs, c.cachedPortPIDs)
	slowTick := scheduledSlow || pidsChanged

	if slowTick {
		if !scheduledSlow {
			// PID set changed on a fast tick: perform a fresh port scan now.
			rescanned := captureProcSnapshotFn(true)
			if rescanned != nil {
				procSnap = rescanned
				if procSnap.ProcessSnapshot == nil {
					procSnap.ProcessSnapshot = &agent.ProcessSnapshot{
						Processes: make(map[int]agent.OSProcess),
						Children:  make(map[int][]int),
					}
				}
				if procSnap.PortsByPID == nil {
					procSnap.PortsByPID = make(map[int][]int)
				}
			}
			c.tickCount = 1
		}
		c.cachedPorts = clonePortsMap(procSnap.PortsByPID)
		c.cachedPortPIDs = currentPIDs
	} else {
		procSnap.PortsByPID = clonePortsMap(c.cachedPorts)
	}

	realHome := agent.RealHomeDir(homeDir)
	profileRoots := c.reg.DiscoverProfileRoots(realHome)

	// 1. Collect normalized SessionRows, Ports, and Project groupings.
	var (
		sessions      []SessionRow
		allPorts      []PortEntry
		liveChildPIDs = make(map[int]bool)
		projectOrder  []string
		projectsByKey = make(map[string]*projectAccum)
	)

	for _, sp := range spaces {
		for _, pane := range sp.Panes {
			isAgentPane := pane.IsAgent() || pane.ResumeSession != ""
			if !isAgentPane {
				if pane.Git != nil {
					addProjectPane(projectsByKey, &projectOrder, sp, pane, "", false, false)
				}
				continue
			}

			desc := descendantTree(pane.PID, procSnap, c.reg)
			for _, pid := range desc.AllPIDs {
				liveChildPIDs[pid] = true
			}

			plugin, canonicalAgent := c.resolvePanePlugin(pane, desc)
			effectiveCWD := firstNonEmpty(pane.CWD, hookCWD(pane.Hook), sp.CWD)
			sessionID := firstNonEmpty(hookSessionID(pane.Hook), pane.ResumeSession)
			agentPID := desc.AgentPID
			if agentPID == 0 {
				agentPID = pane.PID
			}

			var metrics *agent.SessionMetrics
			if plugin != nil {
				if provider := plugin.SessionTelemetry(); provider != nil {
					var openFiles []string
					if agentPID > 0 {
						openFiles = procSnap.OpenFiles(agentPID)
					}
					q := agent.SessionMetricsQuery{
						PID:         pane.PID,
						AgentPID:    agentPID,
						CWD:         effectiveCWD,
						SessionID:   sessionID,
						Profile:     pane.Profile,
						HomeDir:     realHome,
						ConfigRoots: configRootsForAgent(realHome, profileRoots, canonicalAgent),
						Cmdline:     desc.AgentCmdline,
						OpenFiles:   openFiles,
						SlowTick:    slowTick,
					}
					if m, err := provider.CollectSessionMetrics(ctx, q); err == nil && m != nil {
						metrics = m
					}
				}
			}

			row := buildSessionRow(sp, pane, canonicalAgent, agentPID, effectiveCWD, sessionID, desc, metrics)
			sessions = append(sessions, row)

			for _, pe := range row.Ports {
				allPorts = append(allPorts, pe)
				c.trackedPortChildren[trackedPortKey{pid: pe.PID, port: pe.Port}] = trackedPortChild{
					Port:      pe.Port,
					Command:   pe.Command,
					Project:   row.Project,
					SessionID: row.SessionID,
				}
			}

			metricsBranch := ""
			if metrics != nil {
				metricsBranch = strings.TrimSpace(metrics.GitBranch)
			}
			addProjectPane(projectsByKey, &projectOrder, sp, pane, metricsBranch, true, row.Status == StatusWorking)
		}
	}

	// 2. Ports sorting, duplicate port detection, and orphan port tracking (Panel 5).
	sort.Slice(allPorts, func(i, j int) bool {
		if allPorts[i].Port != allPorts[j].Port {
			return allPorts[i].Port < allPorts[j].Port
		}
		if allPorts[i].PID != allPorts[j].PID {
			return allPorts[i].PID < allPorts[j].PID
		}
		return allPorts[i].PaneID < allPorts[j].PaneID
	})
	portCounts := make(map[int]int, len(allPorts))
	for _, pe := range allPorts {
		portCounts[pe.Port]++
	}
	for i := range allPorts {
		if portCounts[allPorts[i].Port] > 1 {
			allPorts[i].Duplicate = true
		}
	}
	for si := range sessions {
		for pi := range sessions[si].Ports {
			if portCounts[sessions[si].Ports[pi].Port] > 1 {
				sessions[si].Ports[pi].Duplicate = true
			}
		}
	}

	var orphanPorts []OrphanPort
	for key, tracked := range c.trackedPortChildren {
		if liveChildPIDs[key.pid] {
			continue
		}
		_, stillAlive := procSnap.Processes[key.pid]
		stillListening := slices.Contains(procSnap.PortsByPID[key.pid], key.port)
		if stillAlive && stillListening {
			orphanPorts = append(orphanPorts, OrphanPort{
				Port:      tracked.Port,
				PID:       key.pid,
				Command:   tracked.Command,
				Project:   tracked.Project,
				SessionID: tracked.SessionID,
			})
		} else {
			delete(c.trackedPortChildren, key)
		}
	}
	sort.Slice(orphanPorts, func(i, j int) bool {
		if orphanPorts[i].Port != orphanPorts[j].Port {
			return orphanPorts[i].Port < orphanPorts[j].Port
		}
		return orphanPorts[i].PID < orphanPorts[j].PID
	})

	// 3. Quotas & rate-limit promotion (Panel 2).
	if slowTick {
		c.cachedQuotas = c.collectQuotas(ctx, realHome, profileRoots)
	}
	quotas := cloneQuotas(c.cachedQuotas)
	promoteRateLimitedSessions(sessions, quotas)

	// 4. Token rate history & rolling /min (Panel 1).
	var tickDelta float64
	for _, s := range sessions {
		cur := s.Usage.Active()
		if prev, seen := c.prevActiveTokens[s.Key]; seen && cur > prev {
			tickDelta += float64(cur - prev)
		}
		c.prevActiveTokens[s.Key] = cur
	}
	c.rateSamples = append(c.rateSamples, rateSample{ts: now, delta: tickDelta})
	if len(c.rateSamples) > maxRateHistoryLen {
		c.rateSamples = c.rateSamples[len(c.rateSamples)-maxRateHistoryLen:]
	}

	rateHistory := make([]float64, len(c.rateSamples))
	var tokensPerMin float64
	for i, sample := range c.rateSamples {
		rateHistory[i] = sample.delta
		if now.Sub(sample.ts) <= rateWindowDuration {
			tokensPerMin += sample.delta
		}
	}

	var (
		totalTokens     int64
		totalCostUSD    float64
		totalMemKB      int64
		contextPctSum   float64
		contextPctCount int
	)
	ctxSessions := make([]ContextSessionEntry, 0, len(sessions))
	for _, s := range sessions {
		totalTokens += s.TotalTokens
		totalCostUSD += s.Usage.CostUSD
		totalMemKB += s.MemKB
		if s.ContextWindow > 0 {
			contextPctSum += s.ContextPct
			contextPctCount++
		}
		ctxSessions = append(ctxSessions, ContextSessionEntry{
			SessionKey:      s.Key,
			PaneID:          s.PaneID,
			WindowID:        s.WindowID,
			Project:         s.Project,
			Agent:           s.Agent,
			ContextTokens:   s.ContextTokens,
			ContextWindow:   s.ContextWindow,
			ContextPct:      s.ContextPct,
			Warning:         s.Warning,
			CompactionCount: s.CompactionCount,
		})
	}
	var avgContextPct float64
	if contextPctCount > 0 {
		avgContextPct = contextPctSum / float64(contextPctCount)
	}

	// 5. Projects summary (Panel 4).
	projects := make([]ProjectSummary, 0, len(projectOrder))
	for _, key := range projectOrder {
		acc := projectsByKey[key]
		filesAdded := 0
		filesModified := 0
		anyDirty := false
		for _, wt := range acc.worktrees {
			filesAdded += wt.filesAdded
			filesModified += wt.filesModified
			if wt.isDirty {
				anyDirty = true
			}
		}
		isClean := acc.HasGit && !anyDirty && filesAdded == 0 && filesModified == 0
		projects = append(projects, ProjectSummary{
			Name:          acc.Name,
			RepoRoot:      acc.RepoRoot,
			CWD:           acc.CWD,
			Branch:        acc.Branch,
			HasGit:        acc.HasGit,
			FilesAdded:    filesAdded,
			FilesModified: filesModified,
			IsClean:       isClean,
			SessionCount:  acc.SessionCount,
			ActiveCount:   acc.ActiveCount,
			WorktreeCount: len(acc.worktrees),
		})
	}

	// 6. MCP Servers (Panel 7).
	if slowTick {
		c.cachedMCPServers = c.collectMCPServers(ctx, procSnap.ProcessSnapshot)
	}
	mcpServers := cloneMCPServers(c.cachedMCPServers)

	return TelemetrySnapshot{
		Context: TelemetryContext{
			TokensPerMin:   tokensPerMin,
			RateHistory:    rateHistory,
			TotalTokens:    totalTokens,
			TotalCostUSD:   totalCostUSD,
			TotalMemKB:     totalMemKB,
			AvgContextPct:  avgContextPct,
			ActiveSessions: len(sessions),
			Sessions:       ctxSessions,
		},
		Quotas:      quotas,
		Projects:    projects,
		Ports:       allPorts,
		OrphanPorts: orphanPorts,
		Sessions:    sessions,
		MCPServers:  mcpServers,
		UpdatedAt:   now.Unix(),
	}
}

// KillOrphanPorts performs a fresh port scan, re-verifies each orphan is still
// alive and listening on the expected port, and sends SIGTERM via
// killOrphanPortFn (which also validates cmdline to prevent PID-reuse kills).
func (c *TelemetryCollector) KillOrphanPorts(_ context.Context, orphans []OrphanPort) (int, error) {
	if len(orphans) == 0 {
		return 0, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	fresh := captureProcSnapshotFn(true)
	if fresh == nil || fresh.ProcessSnapshot == nil {
		return 0, nil
	}

	var (
		killed int
		errs   []error
	)
	for _, orphan := range orphans {
		if orphan.PID <= 1 {
			continue
		}
		if _, alive := fresh.Processes[orphan.PID]; !alive {
			delete(c.trackedPortChildren, trackedPortKey{pid: orphan.PID, port: orphan.Port})
			continue
		}
		if !slices.Contains(fresh.PortsByPID[orphan.PID], orphan.Port) {
			delete(c.trackedPortChildren, trackedPortKey{pid: orphan.PID, port: orphan.Port})
			continue
		}
		if err := killOrphanPortFn(orphan.PID, orphan.Command); err != nil {
			errs = append(errs, err)
			continue
		}
		killed++
		delete(c.trackedPortChildren, trackedPortKey{pid: orphan.PID, port: orphan.Port})
	}
	return killed, errors.Join(errs...)
}

func (c *TelemetryCollector) resolvePanePlugin(pane Pane, desc descendantInfo) (agent.Plugin, string) {
	candidates := []string{
		pane.App,
		hookAgent(pane.Hook),
		hookApp(pane.Hook),
		CommandName(pane.Command),
		desc.AgentName,
	}
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		if p, ok := c.reg.Resolve(cand); ok && p != nil {
			return p, p.Spec().ID
		}
	}
	fallback := firstNonEmpty(pane.App, hookAgent(pane.Hook), CommandName(pane.Command), desc.AgentName)
	if norm := NormalizeAgentCommand(fallback); norm != "" {
		return nil, norm
	}
	return nil, strings.TrimSpace(fallback)
}

func buildSessionRow(
	sp Space,
	pane Pane,
	canonicalAgent string,
	agentPID int,
	effectiveCWD string,
	sessionID string,
	desc descendantInfo,
	metrics *agent.SessionMetrics,
) SessionRow {
	agentName := canonicalAgent
	if agentName == "" && metrics != nil {
		agentName = strings.TrimSpace(metrics.Agent)
	}
	if metrics != nil && strings.TrimSpace(metrics.SessionID) != "" {
		sessionID = strings.TrimSpace(metrics.SessionID)
	}
	if metrics != nil && strings.TrimSpace(metrics.CWD) != "" && effectiveCWD == "" {
		effectiveCWD = strings.TrimSpace(metrics.CWD)
	}

	project := deriveProjectName(pane, effectiveCWD, sp.Name)
	key := sessionRowKey(agentName, sessionID, pane)

	launchSurface := agent.DetectLaunchSurface(desc.AgentCmdline)
	if metrics != nil && metrics.LaunchSurface != "" {
		launchSurface = metrics.LaunchSurface
	}

	profile := strings.TrimSpace(pane.Profile)
	if profile == "" && metrics != nil {
		profile = strings.TrimSpace(metrics.Profile)
	}

	var (
		configRoot      string
		modelName       string
		effort          string
		version         string
		summary         string
		currentTask     string
		usage           agent.TokenUsage
		contextTokens   int64
		contextWindow   int64
		compactionCount int
		turnCount       int
		tokenHistory    []int64
		contextHistory  []int64
		memoryFiles     int
		memoryLines     int
		startedAtMs     int64
		updatedAtMs     int64
	)

	if metrics != nil {
		configRoot = strings.TrimSpace(metrics.ConfigRoot)
		modelName = strings.TrimSpace(metrics.Model)
		effort = strings.TrimSpace(metrics.Effort)
		version = strings.TrimSpace(metrics.Version)
		summary = strings.TrimSpace(metrics.Summary)
		currentTask = strings.TrimSpace(metrics.CurrentTask)
		if metrics.Usage.HasData() {
			usage = metrics.Usage
		}
		contextTokens = metrics.ContextTokens
		contextWindow = metrics.ContextWindow
		compactionCount = metrics.CompactionCount
		turnCount = metrics.TurnCount
		tokenHistory = tailInt64(metrics.TokenHistory, maxSessionHistLen)
		contextHistory = tailInt64(metrics.ContextHistory, maxSessionHistLen)
		memoryFiles = metrics.MemoryFiles
		memoryLines = metrics.MemoryLines
		startedAtMs = metrics.StartedAtMs
		updatedAtMs = metrics.UpdatedAtMs
	}

	if !usage.HasData() && pane.Hook != nil && pane.Hook.Usage.HasData() {
		usage = agent.TokenUsage{
			InputTokens:      pane.Hook.Usage.InputTokens,
			OutputTokens:     pane.Hook.Usage.OutputTokens,
			ReasoningTokens:  pane.Hook.Usage.ReasoningTokens,
			CacheReadTokens:  pane.Hook.Usage.CacheReadTokens,
			CacheWriteTokens: pane.Hook.Usage.CacheWriteTokens,
			CostUSD:          pane.Hook.Usage.CostUSD,
		}
	}

	if modelName == "" && pane.Hook != nil {
		modelName = strings.TrimSpace(pane.Hook.Model)
	}
	if !usage.CostPriced && usage.CostUSD > 0 {
		usage.CostPriced = true
	}
	if !usage.CostPriced && modelName != "" && usage.HasData() {
		if cost, ok := agent.EstimateCostUSD(modelName, usage); ok {
			usage.CostUSD = cost
			usage.CostPriced = true
		}
	}
	if summary == "" {
		summary = firstNonEmpty(pane.ConversationTitle, hookTitle(pane.Hook), pane.ScreenTitle, pane.Title)
	}
	if currentTask == "" {
		currentTask = firstNonEmpty(pane.Text, telemetryHookText(pane.Hook))
	}
	summary = agent.SanitizeTerminalText(agent.RedactSecrets(summary))
	currentTask = agent.SanitizeTerminalText(agent.RedactSecrets(currentTask))

	if startedAtMs == 0 && pane.Hook != nil && pane.Hook.TurnStartedMs > 0 {
		startedAtMs = pane.Hook.TurnStartedMs
	}
	if updatedAtMs == 0 && pane.Hook != nil && pane.Hook.TsMs > 0 {
		updatedAtMs = pane.Hook.TsMs
	}

	totalTokens := usage.Total()
	if contextWindow <= 0 && (contextTokens > 0 || modelName != "") {
		contextWindow = agent.ContextWindowForModel(modelName, "", contextTokens)
	}

	var contextPct float64
	if contextWindow > 0 && contextTokens > 0 {
		contextPct = (float64(contextTokens) * 100.0) / float64(contextWindow)
	}
	warning := classifyContextWarning(contextPct)

	var avgTokensPerTurn int64
	if turnCount > 0 {
		avgTokensPerTurn = totalTokens / int64(turnCount)
	}

	var ports []PortEntry
	if len(desc.Ports) > 0 {
		ports = make([]PortEntry, 0, len(desc.Ports))
		for _, dp := range desc.Ports {
			ports = append(ports, PortEntry{
				Port:      dp.Port,
				PID:       dp.PID,
				Command:   dp.Command,
				Project:   project,
				SessionID: sessionID,
				PaneID:    pane.ID,
				WindowID:  pane.WindowID,
			})
		}
		sort.Slice(ports, func(i, j int) bool {
			if ports[i].Port != ports[j].Port {
				return ports[i].Port < ports[j].Port
			}
			return ports[i].PID < ports[j].PID
		})
	}

	return SessionRow{
		Key:              key,
		Space:            sp.Name,
		WindowID:         pane.WindowID,
		WindowIndex:      pane.WindowIndex,
		PaneID:           pane.ID,
		Agent:            agentName,
		LaunchSurface:    launchSurface,
		PID:              pane.PID,
		AgentPID:         agentPID,
		Project:          project,
		CWD:              effectiveCWD,
		SessionID:        sessionID,
		Profile:          profile,
		ConfigRoot:       configRoot,
		Summary:          summary,
		CurrentTask:      currentTask,
		Status:           pane.Status,
		Model:            modelName,
		Effort:           effort,
		Version:          version,
		Usage:            usage,
		TotalTokens:      totalTokens,
		ContextTokens:    contextTokens,
		ContextWindow:    contextWindow,
		ContextPct:       contextPct,
		Warning:          warning,
		CompactionCount:  compactionCount,
		MemKB:            desc.TotalMemKB,
		TurnCount:        turnCount,
		AvgTokensPerTurn: avgTokensPerTurn,
		TokenHistory:     tokenHistory,
		ContextHistory:   contextHistory,
		MemoryFiles:      memoryFiles,
		MemoryLines:      memoryLines,
		Ports:            ports,
		StartedAtMs:      startedAtMs,
		UpdatedAtMs:      updatedAtMs,
	}
}

func classifyContextWarning(pct float64) ContextFillWarning {
	switch {
	case pct >= contextWarnCritPct:
		return ContextWarningCrit
	case pct >= contextWarnHighPct:
		return ContextWarningHigh
	default:
		return ContextWarningNone
	}
}

func (c *TelemetryCollector) collectQuotas(ctx context.Context, realHome string, profileRoots []agent.ProfileRoot) []agent.QuotaInfo {
	providers := c.reg.QuotaProviders()
	if len(providers) == 0 {
		return nil
	}
	var out []agent.QuotaInfo
	for _, p := range providers {
		qp := p.QuotaProvider()
		if qp == nil {
			continue
		}
		agentID := p.Spec().ID
		homeDirs := configRootsForAgent(realHome, profileRoots, agentID)
		info, err := qp.FetchQuota(ctx, homeDirs)
		if err != nil || info == nil {
			continue
		}
		cp := cloneQuotaInfo(*info)
		if strings.TrimSpace(cp.Agent) == "" {
			cp.Agent = agentID
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := quotaSortRank(out[i].Agent), quotaSortRank(out[j].Agent)
		if ri != rj {
			return ri < rj
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

func quotaSortRank(agentID string) int {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "claude":
		return 0
	case "codex":
		return 1
	default:
		return 2
	}
}

func promoteRateLimitedSessions(sessions []SessionRow, quotas []agent.QuotaInfo) {
	if len(quotas) == 0 || len(sessions) == 0 {
		return
	}
	saturated := make(map[string]bool)
	for _, q := range quotas {
		if q.MaxUsedPct() > rateLimitedQuotaPct {
			saturated[strings.ToLower(strings.TrimSpace(q.Agent))] = true
		}
	}
	if len(saturated) == 0 {
		return
	}
	for i := range sessions {
		if sessions[i].Status == StatusWorking || sessions[i].Status == StatusFailed {
			continue
		}
		if saturated[strings.ToLower(strings.TrimSpace(sessions[i].Agent))] {
			sessions[i].RateLimited = true
		}
	}
}

func (c *TelemetryCollector) collectMCPServers(ctx context.Context, snap *agent.ProcessSnapshot) []agent.LiveMCPServer {
	providers := c.reg.MCPActivityProviders()
	if len(providers) == 0 {
		return nil
	}
	var out []agent.LiveMCPServer
	for _, p := range providers {
		mp := p.MCPActivityProvider()
		if mp == nil {
			continue
		}
		servers, err := mp.CollectLiveMCPServers(ctx, snap)
		if err != nil || len(servers) == 0 {
			continue
		}
		out = append(out, servers...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt != out[j].LastActivityAt {
			return out[i].LastActivityAt > out[j].LastActivityAt
		}
		return out[i].PID < out[j].PID
	})
	return out
}

type worktreeStats struct {
	filesAdded    int
	filesModified int
	isDirty       bool
}

type projectAccum struct {
	Name         string
	hasRepoName  bool
	RepoRoot     string
	CWD          string
	Branch       string
	HasGit       bool
	SessionCount int
	ActiveCount  int
	worktrees    map[string]*worktreeStats
}

func cleanNonEmptyPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func addProjectPane(
	byKey map[string]*projectAccum,
	order *[]string,
	sp Space,
	pane Pane,
	metricsBranch string,
	isSession bool,
	isActive bool,
) {
	cwd := cleanNonEmptyPath(firstNonEmpty(pane.CWD, hookCWD(pane.Hook), sp.CWD))
	var repoKey, mainRepoRoot, repoRoot, repoName string
	if pane.Git != nil {
		repoKey = strings.TrimSpace(pane.Git.RepoKey)
		mainRepoRoot = cleanNonEmptyPath(pane.Git.MainRepoRoot)
		repoRoot = cleanNonEmptyPath(pane.Git.RepoRoot)
		repoName = strings.TrimSpace(pane.Git.RepoName)
	}
	name := deriveProjectName(pane, cwd, sp.Name)
	key := firstNonEmpty(repoKey, mainRepoRoot, repoRoot, cwd, name)
	if key == "" {
		return
	}

	canonicalRoot := firstNonEmpty(mainRepoRoot, repoRoot)

	acc, exists := byKey[key]
	if !exists {
		acc = &projectAccum{
			Name:        name,
			hasRepoName: repoName != "",
			RepoRoot:    canonicalRoot,
			CWD:         cwd,
			worktrees:   make(map[string]*worktreeStats),
		}
		byKey[key] = acc
		*order = append(*order, key)
	}

	if repoName != "" && !acc.hasRepoName {
		acc.Name = repoName
		acc.hasRepoName = true
	} else if acc.Name == "" {
		acc.Name = name
	}
	if mainRepoRoot != "" {
		acc.RepoRoot = mainRepoRoot
	} else if acc.RepoRoot == "" && repoRoot != "" {
		acc.RepoRoot = repoRoot
	}
	if acc.CWD == "" && cwd != "" {
		acc.CWD = cwd
	}

	wtKey := firstNonEmpty(repoRoot, cwd, key)
	wt, wtExists := acc.worktrees[wtKey]
	if !wtExists {
		wt = &worktreeStats{}
		acc.worktrees[wtKey] = wt
	}

	if pane.Git != nil {
		acc.HasGit = true
		if pane.Git.Branch != "" && (acc.Branch == "" || !pane.Git.IsWorktree) {
			acc.Branch = pane.Git.Branch
		}
		if pane.Git.IsDirty {
			wt.isDirty = true
		}
		if pane.Git.FilesAdded > wt.filesAdded {
			wt.filesAdded = pane.Git.FilesAdded
		}
		if pane.Git.FilesModified > wt.filesModified {
			wt.filesModified = pane.Git.FilesModified
		}
	} else if acc.Branch == "" && metricsBranch != "" {
		acc.Branch = metricsBranch
		acc.HasGit = true
	}

	if isSession {
		acc.SessionCount++
		if isActive {
			acc.ActiveCount++
		}
	}
}

func collectLivePIDSet(spaces []Space, snap *procTableSnapshot, reg *agent.Registry) []int {
	seen := make(map[int]bool)
	for _, sp := range spaces {
		for _, pane := range sp.Panes {
			if pane.PID <= 0 {
				continue
			}
			seen[pane.PID] = true
			if snap != nil && snap.ProcessSnapshot != nil {
				desc := descendantTree(pane.PID, snap, reg)
				if desc.AgentPID > 0 {
					seen[desc.AgentPID] = true
				}
			}
		}
	}
	out := make([]int, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

func configRootsForAgent(realHome string, profileRoots []agent.ProfileRoot, agentID string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	add(realHome)
	for _, pr := range profileRoots {
		if agentID == "" || strings.EqualFold(pr.Agent, agentID) {
			add(pr.HomeDir)
		}
	}
	return out
}

func sessionRowKey(agentName, sessionID string, pane Pane) string {
	if sessionID != "" {
		if agentName != "" {
			return agentName + ":" + sessionID
		}
		return sessionID
	}
	if pane.ID != "" {
		return pane.ID
	}
	return fmt.Sprintf("%s:%d", agentName, pane.PID)
}

func deriveProjectName(pane Pane, cwd, spaceName string) string {
	if pane.Git != nil && strings.TrimSpace(pane.Git.RepoName) != "" {
		return strings.TrimSpace(pane.Git.RepoName)
	}
	if pane.Git != nil && strings.TrimSpace(pane.Git.MainRepoRoot) != "" {
		if base := filepath.Base(pane.Git.MainRepoRoot); base != "" && base != "." && base != "/" {
			return base
		}
	}
	if pane.Git != nil && strings.TrimSpace(pane.Git.RepoRoot) != "" {
		if base := filepath.Base(pane.Git.RepoRoot); base != "" && base != "." && base != "/" {
			return base
		}
	}
	if cwd != "" {
		if base := filepath.Base(cwd); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return strings.TrimSpace(spaceName)
}

func tailInt64(in []int64, maxLen int) []int64 {
	if len(in) == 0 || maxLen <= 0 {
		return nil
	}
	if len(in) > maxLen {
		in = in[len(in)-maxLen:]
	}
	out := make([]int64, len(in))
	copy(out, in)
	return out
}

func clonePortsMap(in map[int][]int) map[int][]int {
	if len(in) == 0 {
		return make(map[int][]int)
	}
	out := make(map[int][]int, len(in))
	for pid, ports := range in {
		cp := make([]int, len(ports))
		copy(cp, ports)
		out[pid] = cp
	}
	return out
}

func cloneQuotaInfo(q agent.QuotaInfo) agent.QuotaInfo {
	cp := q
	if q.Primary != nil {
		w := *q.Primary
		cp.Primary = &w
	}
	if q.Secondary != nil {
		w := *q.Secondary
		cp.Secondary = &w
	}
	return cp
}

func cloneQuotas(in []agent.QuotaInfo) []agent.QuotaInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.QuotaInfo, len(in))
	for i, q := range in {
		out[i] = cloneQuotaInfo(q)
	}
	return out
}

func cloneMCPServers(in []agent.LiveMCPServer) []agent.LiveMCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.LiveMCPServer, len(in))
	for i, s := range in {
		cp := s
		if len(s.Threads) > 0 {
			cp.Threads = make([]agent.LiveMCPThread, len(s.Threads))
			copy(cp.Threads, s.Threads)
		}
		out[i] = cp
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func hookAgent(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.Agent
}

func hookApp(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.App
}

func hookSessionID(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.SessionID
}

func hookCWD(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.CWD
}

func hookTitle(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.Title
}

func telemetryHookText(h *HookSummary) string {
	if h == nil {
		return ""
	}
	return h.Text
}

// parseTelemetryProcStat parses /proc/<pid>/stat, correctly handling comm names
// containing spaces or parentheses by splitting on the first '(' and last ')'.
// Returns comm, ppid, rss (in pages), and ok.
func parseTelemetryProcStat(raw string) (comm string, ppid int, rssPages int64, ok bool) {
	firstParen := strings.IndexByte(raw, '(')
	lastParen := strings.LastIndexByte(raw, ')')
	if firstParen < 0 || lastParen <= firstParen || lastParen+1 >= len(raw) {
		return "", 0, 0, false
	}
	comm = raw[firstParen+1 : lastParen]
	fields := strings.Fields(raw[lastParen+1:])
	// After ")": [0]=state, [1]=ppid, ..., [21]=rss
	if len(fields) < 22 {
		return "", 0, 0, false
	}
	ppidVal, err1 := strconv.Atoi(fields[1])
	rssVal, err2 := strconv.ParseInt(fields[21], 10, 64)
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return comm, ppidVal, rssVal, true
}

// parseProcCmdlineArgs splits NUL-delimited /proc/<pid>/cmdline bytes into argv.
func parseProcCmdlineArgs(raw []byte) []string {
	parts := bytes.Split(raw, []byte{0})
	args := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(string(p)); s != "" {
			args = append(args, s)
		}
	}
	return args
}

// parseProcNetTCP parses /proc/net/tcp or /proc/net/tcp6 content and populates
// inodeToPort for sockets in LISTEN state ("0A").
func parseProcNetTCP(data string, inodeToPort map[uint64]int) {
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || !strings.EqualFold(fields[3], "0A") {
			continue
		}
		colon := strings.LastIndexByte(fields[1], ':')
		if colon < 0 || colon+1 >= len(fields[1]) {
			continue
		}
		portVal, err := strconv.ParseUint(fields[1][colon+1:], 16, 16)
		if err != nil || portVal == 0 {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		inodeToPort[inode] = int(portVal)
	}
}

// parseSocketInode extracts the numeric inode from a /proc/<pid>/fd symlink
// target of the form "socket:[12345]".
func parseSocketInode(target string) (uint64, bool) {
	s, ok := strings.CutPrefix(target, "socket:[")
	if !ok {
		return 0, false
	}
	s, ok = strings.CutSuffix(s, "]")
	if !ok || s == "" {
		return 0, false
	}
	inode, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return inode, true
}

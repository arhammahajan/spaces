package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const codexMCPActiveMtimeSecs = 30 * 60 // 30 minutes (1800s)

type codexRateLimitStore struct {
	mu     sync.RWMutex
	latest *agent.QuotaInfo
}

func (s *codexRateLimitStore) get() *agent.QuotaInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneQuotaInfo(s.latest)
}

func (s *codexRateLimitStore) update(q *agent.QuotaInfo, homeDir string) bool {
	if q == nil {
		return false
	}
	s.mu.Lock()
	if s.latest != nil && q.UpdatedAt <= s.latest.UpdatedAt {
		s.mu.Unlock()
		return false
	}
	s.latest = cloneQuotaInfo(q)
	s.mu.Unlock()

	writeCodexRateLimitCache(q, homeDir)
	return true
}

func cloneQuotaInfo(q *agent.QuotaInfo) *agent.QuotaInfo {
	if q == nil {
		return nil
	}
	cp := *q
	if q.Primary != nil {
		w := *q.Primary
		cp.Primary = &w
	}
	if q.Secondary != nil {
		w := *q.Secondary
		cp.Secondary = &w
	}
	return &cp
}

type codexRolloutCacheEntry struct {
	dev               uint64
	ino               uint64
	offset            int64
	sessionID         string
	cwd               string
	gitBranch         string
	version           string
	model             string
	effort            string
	contextWindow     int64
	usage             agent.TokenUsage
	lastContextTokens int64
	maxContextTokens  int64
	prevContextTokens int64
	prevCacheRead     int64
	compactionCount   int
	turnCount         int
	tokenHistory      []int64
	contextHistory    []int64
	summary           string
	currentTask       string
	startedAtMs       int64
	updatedAtMs       int64
	rateLimit         *agent.QuotaInfo
}

type codexTelemetryProvider struct {
	mu        sync.Mutex
	cache     map[string]*codexRolloutCacheEntry
	rateStore *codexRateLimitStore
}

func newCodexTelemetryProvider(store *codexRateLimitStore) *codexTelemetryProvider {
	if store == nil {
		store = &codexRateLimitStore{}
	}
	return &codexTelemetryProvider{
		cache:     make(map[string]*codexRolloutCacheEntry),
		rateStore: store,
	}
}

func (p *codexTelemetryProvider) CollectSessionMetrics(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	homeDir := strings.TrimSpace(q.HomeDir)
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}

	rolloutPath, configRoot := locateCodexRollout(q, homeDir)
	if rolloutPath == "" || isSymlinkPath(rolloutPath) {
		return nil, nil
	}

	p.mu.Lock()
	entry, newRL, ok := p.updateRolloutCacheLocked(rolloutPath)
	if !ok {
		p.mu.Unlock()
		return nil, nil
	}
	sessionID := entry.sessionID
	cwd := entry.cwd
	gitBranch := entry.gitBranch
	version := entry.version
	model := entry.model
	effort := entry.effort
	ctxWindow := entry.contextWindow
	usage := entry.usage
	lastCtx := entry.lastContextTokens
	maxCtx := entry.maxContextTokens
	compactions := entry.compactionCount
	turns := entry.turnCount
	tokHist := append([]int64(nil), entry.tokenHistory...)
	ctxHist := append([]int64(nil), entry.contextHistory...)
	summary := entry.summary
	currentTask := entry.currentTask
	startedAtMs := entry.startedAtMs
	updatedAtMs := entry.updatedAtMs
	p.mu.Unlock()

	if newRL != nil {
		p.rateStore.update(newRL, homeDir)
	}

	if sessionID == "" {
		sessionID = q.SessionID
	}
	if cwd == "" {
		cwd = q.CWD
	}
	if ctxWindow <= 0 {
		ctxWindow = agent.ContextWindowForModel(model, "", maxCtx)
	}

	return &agent.SessionMetrics{
		Agent:           "codex",
		SessionID:       sessionID,
		LaunchSurface:   agent.DetectLaunchSurface(q.Cmdline),
		ConfigRoot:      configRoot,
		Profile:         q.Profile,
		CWD:             cwd,
		GitBranch:       gitBranch,
		Model:           model,
		Effort:          effort,
		Version:         version,
		Summary:         summary,
		CurrentTask:     currentTask,
		Usage:           usage,
		ContextTokens:   lastCtx,
		ContextWindow:   ctxWindow,
		CompactionCount: compactions,
		TurnCount:       turns,
		TokenHistory:    tokHist,
		ContextHistory:  ctxHist,
		StartedAtMs:     startedAtMs,
		UpdatedAtMs:     updatedAtMs,
	}, nil
}

func (p *codexTelemetryProvider) updateRolloutCacheLocked(path string) (*codexRolloutCacheEntry, *agent.QuotaInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return nil, nil, false
	}

	dev, ino := telemetryFileIdentity(info)
	fileSize := info.Size()
	mtimeMs := info.ModTime().UnixMilli()

	entry, exists := p.cache[path]
	if !exists || entry.dev != dev || entry.ino != ino || fileSize < entry.offset {
		entry = &codexRolloutCacheEntry{
			dev: dev,
			ino: ino,
		}
		p.cache[path] = entry
	}

	if fileSize == entry.offset {
		if entry.updatedAtMs == 0 {
			entry.updatedAtMs = mtimeMs
		}
		return entry, nil, true
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()

	if entry.offset > 0 {
		if _, err := f.Seek(entry.offset, io.SeekStart); err != nil {
			return nil, nil, false
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	bytesRead := entry.offset
	var latestRL *agent.QuotaInfo

	for {
		line, n, hasNewline, hitCap, err := readBoundedLine(reader, telemetryMaxLineBytes)
		if n == 0 && err != nil {
			break
		}
		if hitCap && !hasNewline {
			bytesRead = fileSize
			break
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if hasNewline {
				bytesRead += int64(n)
			}
			if err != nil {
				break
			}
			continue
		}

		var raw map[string]any
		if jErr := json.Unmarshal([]byte(trimmed), &raw); jErr != nil {
			if !hasNewline {
				break
			}
			bytesRead += int64(n)
			if err != nil {
				break
			}
			continue
		}

		bytesRead += int64(n)
		if rl := parseCodexRolloutEntry(raw, entry); rl != nil {
			if latestRL == nil || rl.UpdatedAt >= latestRL.UpdatedAt {
				latestRL = rl
			}
		}

		if err != nil {
			break
		}
	}

	entry.offset = bytesRead
	if mtimeMs > entry.updatedAtMs {
		entry.updatedAtMs = mtimeMs
	}
	if latestRL != nil {
		entry.rateLimit = cloneQuotaInfo(latestRL)
	}
	return entry, latestRL, true
}

func parseCodexRolloutEntry(val map[string]any, entry *codexRolloutCacheEntry) *agent.QuotaInfo {
	tsMs := parseRFC3339Ms(strField(val, "timestamp"))
	if tsMs > 0 {
		if entry.startedAtMs == 0 || tsMs < entry.startedAtMs {
			entry.startedAtMs = tsMs
		}
		if tsMs > entry.updatedAtMs {
			entry.updatedAtMs = tsMs
		}
	}

	payload, _ := val["payload"].(map[string]any)
	if payload == nil {
		return nil
	}

	switch strField(val, "type") {
	case "session_meta":
		if id := strField(payload, "id"); id != "" {
			entry.sessionID = id
		}
		if cwd := strField(payload, "cwd"); cwd != "" {
			entry.cwd = cwd
		}
		if ver := strField(payload, "cli_version"); ver != "" {
			entry.version = ver
		}
		if ts := parseRFC3339Ms(strField(payload, "timestamp")); ts > 0 {
			entry.startedAtMs = ts
		}
		if git, ok := payload["git"].(map[string]any); ok {
			if branch := strField(git, "branch"); branch != "" {
				entry.gitBranch = branch
			}
		}

	case "turn_context":
		if m := strField(payload, "model"); m != "" {
			entry.model = m
		}
		if e := strField(payload, "effort"); e != "" {
			entry.effort = e
		}
		if cw := int64Field(payload, "model_context_window"); cw > 0 {
			entry.contextWindow = cw
		}

	case "event_msg":
		switch strField(payload, "type") {
		case "task_started":
			if cw := int64Field(payload, "model_context_window"); cw > 0 {
				entry.contextWindow = cw
			}
			if m := strField(payload, "model"); m != "" {
				entry.model = m
			}

		case "user_message":
			if msg := strField(payload, "message"); msg != "" {
				cleaned := sanitizeTelemetryText(msg, 120)
				if cleaned != "" {
					if entry.summary == "" {
						entry.summary = cleaned
					}
					entry.currentTask = cleaned
				}
			}

		case "agent_message":
			entry.turnCount++

		case "item_completed":
			parseCodexItemCompleted(payload, entry)

		case "token_count":
			if info, ok := payload["info"].(map[string]any); ok {
				if total, ok := info["total_token_usage"].(map[string]any); ok {
					inp := int64Field(total, "input_tokens")
					out := int64Field(total, "output_tokens")
					reasoning := int64Field(total, "reasoning_output_tokens")
					cache := int64Field(total, "cached_input_tokens")
					if cache == 0 {
						cache = int64Field(total, "cache_read_input_tokens")
					}
					nonCached := inp - cache
					if nonCached < 0 {
						nonCached = 0
					}
					entry.usage.InputTokens = nonCached
					entry.usage.OutputTokens = out
					entry.usage.ReasoningTokens = reasoning
					entry.usage.CacheReadTokens = cache
				}

				if last, ok := info["last_token_usage"].(map[string]any); ok {
					inp := int64Field(last, "input_tokens")
					out := int64Field(last, "output_tokens")
					cr := int64Field(last, "cached_input_tokens")
					if cr == 0 {
						cr = int64Field(last, "cache_read_input_tokens")
					}

					if entry.prevContextTokens > 0 && inp > 0 && inp < entry.prevContextTokens*7/10 {
						if entry.prevCacheRead > 1000 {
							if cr < entry.prevCacheRead/5 {
								entry.compactionCount++
							}
						} else if entry.prevContextTokens > 10000 {
							entry.compactionCount++
						}
					}

					entry.lastContextTokens = inp
					if inp > entry.maxContextTokens {
						entry.maxContextTokens = inp
					}
					entry.prevContextTokens = inp
					entry.prevCacheRead = cr

					entry.tokenHistory = appendCappedInt64(entry.tokenHistory, inp+out, telemetryMaxHistory)
					entry.contextHistory = appendCappedInt64(entry.contextHistory, inp, telemetryMaxHistory)
				}

				if cw := int64Field(info, "model_context_window"); cw > 0 {
					entry.contextWindow = cw
				}
			}

			if rl, ok := payload["rate_limits"].(map[string]any); ok {
				return parseCodexRateLimitPayload(rl, tsMs)
			}
		}

	case "response_item":
		itemType := strField(payload, "type")
		if itemType == "function_call" || itemType == "custom_tool_call" {
			if name := strField(payload, "name"); name != "" {
				arg := ""
				if itemType == "function_call" {
					arg = parseCodexFunctionCallArg(strField(payload, "arguments"))
				}
				task := name
				if arg != "" {
					task = name + " " + arg
				}
				entry.currentTask = sanitizeTelemetryText(task, 120)
			}
		}
	}

	return nil
}

func parseCodexItemCompleted(payload map[string]any, entry *codexRolloutCacheEntry) {
	item, ok := payload["item"].(map[string]any)
	if !ok {
		return
	}
	switch strField(item, "type") {
	case "UserMessage":
		text := concatCodexItemText(item["content"])
		cleaned := sanitizeTelemetryText(text, 120)
		if cleaned != "" {
			if entry.summary == "" {
				entry.summary = cleaned
			}
			entry.currentTask = cleaned
		}
	case "AgentMessage":
		entry.turnCount++
	case "CommandExecution":
		name := "exec"
		arg := ""
		if arr, ok := item["parsed_cmd"].([]any); ok && len(arr) > 0 {
			if first, ok := arr[0].(map[string]any); ok {
				switch strField(first, "type") {
				case "read", "write", "search":
					name = strField(first, "type")
				}
				arg = shortenTelemetryPath(strField(first, "path"))
			}
		}
		task := name
		if arg != "" {
			task = name + " " + arg
		}
		entry.currentTask = sanitizeTelemetryText(task, 120)
	case "FileChange":
		if changes, ok := item["changes"].(map[string]any); ok {
			for p := range changes {
				entry.currentTask = sanitizeTelemetryText("edit "+shortenTelemetryPath(p), 120)
				break
			}
		}
	}
}

func concatCodexItemText(content any) string {
	arr, ok := content.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, p := range arr {
		if obj, ok := p.(map[string]any); ok {
			if t := strField(obj, "text"); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

func parseCodexFunctionCallArg(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(arguments), &doc); err != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path"} {
		if raw := strField(doc, k); raw != "" {
			return shortenTelemetryPath(raw)
		}
	}
	for _, k := range []string{"cmd", "command", "chars", "target", "session_id"} {
		if raw := strField(doc, k); raw != "" {
			return truncateRunes(raw, 60)
		}
	}
	return ""
}

func parseCodexRateLimitPayload(rl map[string]any, tsMs int64) *agent.QuotaInfo {
	limitID := strField(rl, "limit_id")
	if limitID != "" && limitID != "codex" {
		return nil
	}

	q := &agent.QuotaInfo{
		Agent:     "codex",
		Source:    "codex",
		UpdatedAt: tsMs / 1000,
	}
	if q.UpdatedAt == 0 {
		q.UpdatedAt = time.Now().Unix()
	}

	for _, slot := range []string{"primary", "secondary"} {
		w, ok := rl[slot].(map[string]any)
		if !ok {
			continue
		}
		pct, hasPct := float64Field(w, "used_percent")
		if !hasPct {
			pct, hasPct = float64Field(w, "used_percentage")
		}
		if !hasPct {
			continue
		}
		mins := int(int64Field(w, "window_minutes"))
		resetsAt := int64Field(w, "resets_at")
		qw := &agent.QuotaWindow{
			Label:         agent.FormatQuotaWindowLabel(mins),
			WindowMinutes: mins,
			UsedPct:       pct,
			ResetsAt:      resetsAt,
		}
		if mins > 0 && mins <= 300 {
			q.Primary = qw
		} else {
			q.Secondary = qw
		}
	}

	if q.Primary == nil && q.Secondary == nil {
		return nil
	}
	return q
}

func locateCodexRollout(q agent.SessionMetricsQuery, homeDir string) (rolloutPath, configRoot string) {
	// 1. Check OpenFiles for rollout-*.jsonl.
	for _, f := range q.OpenFiles {
		if isCodexRolloutPath(f) && !isSymlinkPath(f) {
			return f, codexConfigRootFromRollout(f)
		}
	}

	var sessionsDirs []string
	seen := make(map[string]struct{})
	addRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		sd := filepath.Join(filepath.Clean(root), "sessions")
		if _, ok := seen[sd]; ok {
			return
		}
		if st, err := os.Stat(sd); err == nil && st.IsDir() {
			seen[sd] = struct{}{}
			sessionsDirs = append(sessionsDirs, sd)
		}
	}
	for _, r := range q.ConfigRoots {
		addRoot(r)
	}
	if homeDir != "" {
		addRoot(filepath.Join(homeDir, ".codex"))
	}

	if len(sessionsDirs) == 0 {
		return "", ""
	}

	// Collect candidate rollout files sorted by mtime desc.
	type candidate struct {
		path    string
		cfgRoot string
		modTime time.Time
	}
	var cands []candidate
	for _, sd := range sessionsDirs {
		cfgRoot := filepath.Dir(sd)
		_ = filepath.WalkDir(sd, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !isCodexRolloutPath(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			cands = append(cands, candidate{path: path, cfgRoot: cfgRoot, modTime: info.ModTime()})
			return nil
		})
	}

	sort.Slice(cands, func(i, j int) bool {
		return cands[i].modTime.After(cands[j].modTime)
	})

	// 2. Match by SessionID (filename substring or session_meta.payload.id).
	if sid := strings.TrimSpace(q.SessionID); sid != "" {
		for _, c := range cands {
			if strings.Contains(filepath.Base(c.path), sid) {
				return c.path, c.cfgRoot
			}
		}
		for _, c := range cands {
			metaSID, _ := peekCodexSessionMeta(c.path)
			if metaSID == sid {
				return c.path, c.cfgRoot
			}
		}
	}

	// 3. Match newest rollout with matching CWD.
	if cwd := strings.TrimSpace(q.CWD); cwd != "" {
		cleanCWD := filepath.Clean(cwd)
		for _, c := range cands {
			_, metaCWD := peekCodexSessionMeta(c.path)
			if metaCWD != "" && filepath.Clean(metaCWD) == cleanCWD {
				return c.path, c.cfgRoot
			}
		}
	}

	return "", ""
}

func peekCodexSessionMeta(path string) (sessionID, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 16*1024)
	for i := 0; i < 8; i++ {
		line, n, _, hitCap, err := readBoundedLine(r, 256*1024)
		if n == 0 && err != nil {
			break
		}
		if hitCap {
			break
		}
		var doc map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &doc) == nil && strField(doc, "type") == "session_meta" {
			if payload, ok := doc["payload"].(map[string]any); ok {
				return strField(payload, "id"), strField(payload, "cwd")
			}
		}
		if err != nil {
			break
		}
	}
	return "", ""
}

func isCodexRolloutPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl")
}

func codexConfigRootFromRollout(p string) string {
	slashed := filepath.ToSlash(p)
	if idx := strings.Index(slashed, "/sessions/"); idx > 0 {
		return filepath.FromSlash(slashed[:idx])
	}
	return ""
}

func writeCodexRateLimitCache(q *agent.QuotaInfo, homeDir string) {
	if q == nil {
		return
	}
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	if homeDir == "" {
		return
	}
	cachePath := filepath.Join(homeDir, ".cache", "spaces", "codex-rate-limits.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return
	}

	payload := rateLimitFileJSON{
		Source:    "codex",
		UpdatedAt: q.UpdatedAt,
	}
	if q.Primary != nil {
		pct := q.Primary.UsedPct
		payload.FiveHour = &rateLimitWindowJSON{
			UsedPercentage: &pct,
			ResetsAt:       q.Primary.ResetsAt,
			WindowMinutes:  q.Primary.WindowMinutes,
		}
	}
	if q.Secondary != nil {
		pct := q.Secondary.UsedPct
		payload.SevenDay = &rateLimitWindowJSON{
			UsedPercentage: &pct,
			ResetsAt:       q.Secondary.ResetsAt,
			WindowMinutes:  q.Secondary.WindowMinutes,
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmpPath, cachePath)
}

// codexQuotaProvider returns the freshest Codex QuotaInfo from in-memory live
// rate limits, ~/.cache/spaces/codex-rate-limits.json, or ~/.cache/abtop/codex-rate-limits.json.
type codexQuotaProvider struct {
	rateStore *codexRateLimitStore
}

func newCodexQuotaProvider(store *codexRateLimitStore) *codexQuotaProvider {
	if store == nil {
		store = &codexRateLimitStore{}
	}
	return &codexQuotaProvider{rateStore: store}
}

func (p *codexQuotaProvider) FetchQuota(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var best *agent.QuotaInfo
	var bestFreshness int64

	if live := p.rateStore.get(); live != nil {
		best = live
		bestFreshness = live.UpdatedAt
	}

	homes := homeDirs
	if len(homes) == 0 {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			homes = []string{h}
		}
	}

	for _, h := range homes {
		for _, sub := range []string{
			filepath.Join(h, ".cache", "spaces", "codex-rate-limits.json"),
			filepath.Join(h, ".cache", "abtop", "codex-rate-limits.json"),
		} {
			info, freshness := readRateLimitQuotaFile(sub, "codex")
			if info == nil {
				continue
			}
			if best == nil || freshness > bestFreshness {
				best = info
				bestFreshness = freshness
			}
		}
	}
	return best, nil
}

// codexMCPActivityProvider discovers live `codex mcp-server` processes and their open rollout threads.
type codexMCPActivityProvider struct{}

func newCodexMCPActivityProvider() *codexMCPActivityProvider {
	return &codexMCPActivityProvider{}
}

func (p *codexMCPActivityProvider) CollectLiveMCPServers(ctx context.Context, snap *agent.ProcessSnapshot) ([]agent.LiveMCPServer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snap == nil || len(snap.Processes) == 0 {
		return nil, nil
	}

	now := time.Now()
	var servers []agent.LiveMCPServer

	for _, proc := range snap.Processes {
		if !isCodexMCPServerProcess(proc) {
			continue
		}

		parentCLI := resolveMCPParentCLI(proc.PPID, snap.Processes)
		profile := parseCodexMCPProfile(proc.Args)
		openFiles := snap.OpenFiles(proc.PID)

		seenRollout := make(map[string]struct{})
		var threads []agent.LiveMCPThread
		var activeCount int
		var lastActivity int64

		for _, f := range openFiles {
			if !isCodexRolloutPath(f) {
				continue
			}
			if _, dup := seenRollout[f]; dup {
				continue
			}
			seenRollout[f] = struct{}{}

			var updatedAt int64
			var sizeBytes int64
			var active bool
			if st, err := os.Stat(f); err == nil {
				mod := st.ModTime()
				updatedAt = mod.Unix()
				sizeBytes = st.Size()
				age := now.Sub(mod)
				if age >= 0 && int64(age.Seconds()) < codexMCPActiveMtimeSecs {
					active = true
					activeCount++
				}
				if updatedAt > lastActivity {
					lastActivity = updatedAt
				}
			}

			threads = append(threads, agent.LiveMCPThread{
				Path:      f,
				UpdatedAt: updatedAt,
				SizeBytes: sizeBytes,
				Active:    active,
			})
		}

		sort.Slice(threads, func(i, j int) bool {
			return threads[i].UpdatedAt > threads[j].UpdatedAt
		})

		servers = append(servers, agent.LiveMCPServer{
			Agent:          "codex",
			PID:            proc.PID,
			PPID:           proc.PPID,
			ParentCLI:      parentCLI,
			Profile:        profile,
			Command:        strings.Join(proc.Args, " "),
			MemKB:          proc.MemKB,
			ActiveThreads:  activeCount,
			TotalThreads:   len(threads),
			LastActivityAt: lastActivity,
			Threads:        threads,
		})
	}

	sort.Slice(servers, func(i, j int) bool {
		if servers[i].ParentCLI != servers[j].ParentCLI {
			return servers[i].ParentCLI < servers[j].ParentCLI
		}
		return servers[i].PID < servers[j].PID
	})

	return servers, nil
}

func isCodexMCPServerProcess(proc agent.OSProcess) bool {
	joined := strings.Join(proc.Args, " ")
	low := strings.ToLower(joined)
	if strings.Contains(low, "grep") || strings.Contains(low, "app-server") {
		return false
	}
	if !strings.Contains(low, "mcp-server") {
		return false
	}
	if agent.NormalizeCommand(proc.Comm) == "codex" {
		return true
	}
	for _, arg := range proc.Args {
		if agent.NormalizeCommand(arg) == "codex" {
			return true
		}
	}
	return false
}

func resolveMCPParentCLI(ppid int, procs map[int]agent.OSProcess) string {
	known := []string{"claude", "codex", "opencode", "cursor", "kilo", "kimi", "muse", "antigravity"}
	cur := ppid
	for depth := 0; depth < 4 && cur > 1; depth++ {
		p, ok := procs[cur]
		if !ok {
			break
		}
		commNorm := agent.NormalizeCommand(p.Comm)
		for _, k := range known {
			if commNorm == k {
				return k
			}
		}
		for _, arg := range p.Args {
			argNorm := agent.NormalizeCommand(arg)
			for _, k := range known {
				if argNorm == k {
					return k
				}
			}
		}
		if p.PPID == cur {
			break
		}
		cur = p.PPID
	}
	return "?"
}

func parseCodexMCPProfile(args []string) string {
	for i := 0; i < len(args); i++ {
		tok := strings.TrimSpace(args[i])
		if tok == "--profile" && i+1 < len(args) {
			val := strings.Trim(strings.TrimSpace(args[i+1]), `"'`)
			if val != "" {
				return val
			}
		}
		if strings.HasPrefix(tok, "--profile=") {
			val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(tok, "--profile=")), `"'`)
			if val != "" {
				return val
			}
		}
		if tok == "-c" && i+1 < len(args) {
			next := strings.TrimSpace(args[i+1])
			if strings.HasPrefix(next, "profile=") {
				val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(next, "profile=")), `"'`)
				if val != "" {
					return val
				}
			}
		}
		if idx := strings.Index(tok, "profile="); idx >= 0 {
			tail := tok[idx+len("profile="):]
			if sp := strings.IndexAny(tail, " \t"); sp >= 0 {
				tail = tail[:sp]
			}
			val := strings.Trim(tail, `"'`)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

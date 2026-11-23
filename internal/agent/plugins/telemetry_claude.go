package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	telemetryMaxLineBytes       = 10 * 1024 * 1024 // 10 MiB
	telemetryMaxHistory         = 64
	claudeTier2MaxFiles         = 32
	claudeTier2DefaultLimit     = 2_500_000
	claudeTier2BlockWindowHours = 5
)

type claudeTranscriptCacheEntry struct {
	dev               uint64
	ino               uint64
	offset            int64
	usage             agent.TokenUsage
	lastContextTokens int64
	maxContextTokens  int64
	prevContextTokens int64
	prevCacheRead     int64
	compactionCount   int
	turnCount         int
	tokenHistory      []int64
	contextHistory    []int64
	model             string
	version           string
	gitBranch         string
	summary           string
	currentTask       string
	startedAtMs       int64
	updatedAtMs       int64
	configRoot        string
}

type claudeTelemetryProvider struct {
	mu    sync.Mutex
	cache map[string]*claudeTranscriptCacheEntry
}

func newClaudeTelemetryProvider() *claudeTelemetryProvider {
	return &claudeTelemetryProvider{
		cache: make(map[string]*claudeTranscriptCacheEntry),
	}
}

type claudeSessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
}

func (p *claudeTelemetryProvider) CollectSessionMetrics(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	homeDir := strings.TrimSpace(q.HomeDir)
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}

	transcriptPath, sessionID, cwd, configRoot, sessionStartedAt := locateClaudeTranscript(q, homeDir)
	if transcriptPath == "" {
		return nil, nil
	}

	if isSymlinkPath(transcriptPath) {
		return nil, nil
	}

	p.mu.Lock()
	entry, ok := p.updateTranscriptCacheLocked(transcriptPath, configRoot)
	if !ok {
		p.mu.Unlock()
		return nil, nil
	}
	// Snapshot fields under lock.
	usage := entry.usage
	lastCtx := entry.lastContextTokens
	maxCtx := entry.maxContextTokens
	compactions := entry.compactionCount
	turns := entry.turnCount
	tokHist := append([]int64(nil), entry.tokenHistory...)
	ctxHist := append([]int64(nil), entry.contextHistory...)
	model := entry.model
	version := entry.version
	gitBranch := entry.gitBranch
	summary := entry.summary
	currentTask := entry.currentTask
	startedAtMs := entry.startedAtMs
	updatedAtMs := entry.updatedAtMs
	resolvedConfigRoot := entry.configRoot
	p.mu.Unlock()

	if sessionStartedAt > 0 && (startedAtMs == 0 || sessionStartedAt < startedAtMs) {
		startedAtMs = sessionStartedAt
	}
	if cwd == "" {
		cwd = q.CWD
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	}

	configuredModel := readClaudeConfiguredModel(cwd, resolvedConfigRoot, homeDir)
	effort := readClaudeEffortLevel(cwd, resolvedConfigRoot, homeDir)
	contextWindow := agent.ContextWindowForModel(model, configuredModel, maxCtx)

	projectDir := filepath.Dir(transcriptPath)
	memFiles, memLines := readClaudeMemoryStatus(filepath.Join(projectDir, "memory"))

	m := &agent.SessionMetrics{
		Agent:           "claude",
		SessionID:       sessionID,
		LaunchSurface:   agent.DetectLaunchSurface(q.Cmdline),
		ConfigRoot:      resolvedConfigRoot,
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
		ContextWindow:   contextWindow,
		CompactionCount: compactions,
		TurnCount:       turns,
		TokenHistory:    tokHist,
		ContextHistory:  ctxHist,
		MemoryFiles:     memFiles,
		MemoryLines:     memLines,
		StartedAtMs:     startedAtMs,
		UpdatedAtMs:     updatedAtMs,
	}
	if cost, ok := agent.EstimateCostUSD(m.Model, m.Usage); ok {
		m.Usage.CostUSD = cost
		m.Usage.CostPriced = true
	}
	return m, nil
}

func (p *claudeTelemetryProvider) updateTranscriptCacheLocked(path, configRoot string) (*claudeTranscriptCacheEntry, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return nil, false
	}

	dev, ino := telemetryFileIdentity(info)
	fileSize := info.Size()
	mtimeMs := info.ModTime().UnixMilli()

	entry, exists := p.cache[path]
	if !exists || entry.dev != dev || entry.ino != ino || fileSize < entry.offset {
		entry = &claudeTranscriptCacheEntry{
			dev:        dev,
			ino:        ino,
			configRoot: configRoot,
		}
		p.cache[path] = entry
	}
	if configRoot != "" {
		entry.configRoot = configRoot
	}

	if fileSize == entry.offset {
		if entry.updatedAtMs == 0 {
			entry.updatedAtMs = mtimeMs
		}
		return entry, true
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	if entry.offset > 0 {
		if _, err := f.Seek(entry.offset, io.SeekStart); err != nil {
			return nil, false
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	bytesRead := entry.offset

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
				// Partial trailing line: defer until newline completes.
				break
			}
			// Complete line with invalid JSON: skip and advance.
			bytesRead += int64(n)
			if err != nil {
				break
			}
			continue
		}

		bytesRead += int64(n)
		parseClaudeTranscriptEntry(raw, entry)

		if err != nil {
			break
		}
	}

	entry.offset = bytesRead
	if mtimeMs > entry.updatedAtMs {
		entry.updatedAtMs = mtimeMs
	}
	return entry, true
}

func parseClaudeTranscriptEntry(val map[string]any, entry *claudeTranscriptCacheEntry) {
	tsMs := parseRFC3339Ms(strField(val, "timestamp"))
	if tsMs > 0 {
		if entry.startedAtMs == 0 || tsMs < entry.startedAtMs {
			entry.startedAtMs = tsMs
		}
		if tsMs > entry.updatedAtMs {
			entry.updatedAtMs = tsMs
		}
	}

	if v := strField(val, "version"); v != "" {
		entry.version = v
	}
	if b := strField(val, "gitBranch"); b != "" {
		entry.gitBranch = b
	}

	switch strField(val, "type") {
	case "summary":
		if sum := sanitizeTelemetryText(strField(val, "summary"), 160); sum != "" {
			entry.summary = sum
		}

	case "assistant":
		entry.turnCount++
		entry.currentTask = ""

		msg, ok := val["message"].(map[string]any)
		if !ok {
			return
		}
		if m := strField(msg, "model"); m != "" {
			entry.model = m
		}
		if usage, ok := msg["usage"].(map[string]any); ok {
			inp := int64Field(usage, "input_tokens")
			out := int64Field(usage, "output_tokens")
			cr := int64Field(usage, "cache_read_input_tokens")
			cc := int64Field(usage, "cache_creation_input_tokens")

			entry.usage.InputTokens += inp
			entry.usage.OutputTokens += out
			entry.usage.CacheReadTokens += cr
			entry.usage.CacheWriteTokens += cc

			var curCtx int64
			if cr == 0 && cc > 0 {
				curCtx = inp + cc
			} else {
				curCtx = inp + cr
			}

			if entry.prevContextTokens > 0 &&
				curCtx < entry.prevContextTokens*7/10 &&
				entry.prevCacheRead > 1000 &&
				cr < entry.prevCacheRead/5 {
				entry.compactionCount++
			}

			entry.lastContextTokens = curCtx
			if curCtx > entry.maxContextTokens {
				entry.maxContextTokens = curCtx
			}
			entry.prevContextTokens = curCtx
			entry.prevCacheRead = cr

			entry.tokenHistory = appendCappedInt64(entry.tokenHistory, inp+out+cr+cc, telemetryMaxHistory)
			entry.contextHistory = appendCappedInt64(entry.contextHistory, curCtx, telemetryMaxHistory)
		}

		if content, ok := msg["content"].([]any); ok {
			for _, item := range content {
				block, ok := item.(map[string]any)
				if !ok || strField(block, "type") != "tool_use" {
					continue
				}
				toolName := strField(block, "name")
				if toolName == "" {
					toolName = "?"
				}
				arg := extractClaudeToolArg(block)
				task := toolName
				if arg != "" {
					task = toolName + " " + arg
				}
				entry.currentTask = sanitizeTelemetryText(task, 120)
			}
		}

	case "user":
		if isClaudeSyntheticUserMsg(val) {
			return
		}
		msg, ok := val["message"].(map[string]any)
		if !ok {
			return
		}
		prompt := extractClaudePromptText(msg)
		if prompt == "" {
			return
		}
		if entry.summary == "" {
			entry.summary = sanitizeTelemetryText(prompt, 120)
		}
		entry.currentTask = sanitizeTelemetryText(prompt, 120)
	}
}

func isClaudeSyntheticUserMsg(entry map[string]any) bool {
	if isMeta, ok := entry["isMeta"].(bool); ok && isMeta {
		return true
	}
	msg, ok := entry["message"].(map[string]any)
	if !ok {
		return false
	}
	switch c := msg["content"].(type) {
	case []any:
		if len(c) == 0 {
			return false
		}
		for _, item := range c {
			block, ok := item.(map[string]any)
			if !ok || strField(block, "type") != "tool_result" {
				return false
			}
		}
		return true
	case string:
		t := strings.TrimLeft(c, " \t\r\n")
		for _, prefix := range []string{
			"<local-command-stdout>",
			"<local-command-stderr>",
			"<local-command-caveat>",
			"<command-name>",
			"<bash-input>",
			"<bash-stdout>",
			"<bash-stderr>",
		} {
			if strings.HasPrefix(t, prefix) {
				return true
			}
		}
	}
	return false
}

func extractClaudePromptText(msg map[string]any) string {
	var raw string
	switch c := msg["content"].(type) {
	case string:
		raw = c
	case []any:
		for _, item := range c {
			block, ok := item.(map[string]any)
			if ok && strField(block, "type") == "text" {
				if t := strField(block, "text"); t != "" {
					raw = t
					break
				}
			}
		}
	}
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	var kept []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") {
			continue
		}
		kept = append(kept, t)
	}
	joined := strings.Join(kept, " ")
	for {
		start := strings.Index(joined, "[Image")
		if start < 0 {
			break
		}
		end := strings.IndexByte(joined[start:], ']')
		if end < 0 {
			break
		}
		joined = joined[:start] + strings.TrimLeft(joined[start+end+1:], " ")
	}
	joined = strings.TrimSpace(joined)
	if strings.Contains(joined, "You are a conversation title generator") {
		return ""
	}
	return joined
}

func extractClaudeToolArg(toolUse map[string]any) string {
	input, ok := toolUse["input"].(map[string]any)
	if !ok {
		return ""
	}
	if fp := strField(input, "file_path"); fp != "" {
		return shortenTelemetryPath(fp)
	}
	if cmd := strField(input, "command"); cmd != "" {
		if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
			cmd = cmd[:idx]
		}
		return truncateRunes(cmd, 40)
	}
	if pat := strField(input, "pattern"); pat != "" {
		return truncateRunes(pat, 40)
	}
	return ""
}

func locateClaudeTranscript(q agent.SessionMetricsQuery, homeDir string) (transcriptPath, sessionID, cwd, configRoot string, startedAtMs int64) {
	// 1. Check OpenFiles for *.jsonl under a .claude*/projects/ directory.
	for _, f := range q.OpenFiles {
		slashed := filepath.ToSlash(f)
		if !strings.HasSuffix(slashed, ".jsonl") {
			continue
		}
		idx := strings.Index(slashed, "/projects/")
		if idx < 0 {
			continue
		}
		baseRoot := filepath.FromSlash(slashed[:idx])
		baseName := filepath.Base(baseRoot)
		if baseName != ".claude" && !strings.HasPrefix(baseName, ".claude-") {
			continue
		}
		// Skip subagent transcripts nested under <sid>/subagents/.
		if strings.Contains(slashed[idx:], "/subagents/") {
			continue
		}
		if !isSymlinkPath(f) {
			return f, strings.TrimSuffix(filepath.Base(f), ".jsonl"), q.CWD, baseRoot, 0
		}
	}

	configDirs := collectClaudeConfigDirs(q.ConfigRoots, homeDir)

	// 2. Check <config_dir>/sessions/{pid}.json.
	candidatePIDs := make([]int, 0, 2)
	if q.AgentPID > 0 {
		candidatePIDs = append(candidatePIDs, q.AgentPID)
	}
	if q.PID > 0 && q.PID != q.AgentPID {
		candidatePIDs = append(candidatePIDs, q.PID)
	}
	for _, pid := range candidatePIDs {
		for _, cfgDir := range configDirs {
			sfPath := filepath.Join(cfgDir, "sessions", strconv.Itoa(pid)+".json")
			if isSymlinkPath(sfPath) {
				continue
			}
			data, err := os.ReadFile(sfPath)
			if err != nil {
				continue
			}
			var sf claudeSessionFile
			if err := json.Unmarshal(data, &sf); err != nil || strings.TrimSpace(sf.SessionID) == "" {
				continue
			}
			projDir := resolveClaudeProjectDir(filepath.Join(cfgDir, "projects"), sf.CWD, sf.SessionID)
			if projDir == "" {
				continue
			}
			sid := sf.SessionID
			if liveSID := findClaudeLiveSessionID(projDir, sf.StartedAt); liveSID != "" {
				sid = liveSID
			}
			tp := filepath.Join(projDir, sid+".jsonl")
			if !isSymlinkPath(tp) {
				return tp, sid, sf.CWD, cfgDir, sf.StartedAt
			}
		}
	}

	// 3. Fallback: q.SessionID or q.CWD encoded project dir across config dirs.
	if sid := strings.TrimSpace(q.SessionID); sid != "" {
		for _, cfgDir := range configDirs {
			projDir := resolveClaudeProjectDir(filepath.Join(cfgDir, "projects"), q.CWD, sid)
			if projDir == "" {
				continue
			}
			tp := filepath.Join(projDir, sid+".jsonl")
			if !isSymlinkPath(tp) {
				return tp, sid, q.CWD, cfgDir, 0
			}
		}
	}

	if cwdClean := strings.TrimSpace(q.CWD); cwdClean != "" {
		encoded := encodeClaudeCWDPath(cwdClean)
		var bestPath, bestCfg string
		var bestMod time.Time
		for _, cfgDir := range configDirs {
			projDir := filepath.Join(cfgDir, "projects", encoded)
			entries, err := os.ReadDir(projDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.ModTime().After(bestMod) {
					bestMod = info.ModTime()
					bestPath = filepath.Join(projDir, e.Name())
					bestCfg = cfgDir
				}
			}
		}
		if bestPath != "" {
			return bestPath, strings.TrimSuffix(filepath.Base(bestPath), ".jsonl"), cwdClean, bestCfg, 0
		}
	}

	return "", "", "", "", 0
}

func collectClaudeConfigDirs(extraRoots []string, homeDir string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		if _, ok := seen[clean]; ok {
			return
		}
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}

	for _, r := range extraRoots {
		add(r)
	}
	if envDir := os.Getenv("CLAUDE_CONFIG_DIR"); envDir != "" {
		add(envDir)
	}
	if homeDir != "" {
		add(filepath.Join(homeDir, ".claude"))
		if entries, err := os.ReadDir(homeDir); err == nil {
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".claude-") && e.IsDir() {
					add(filepath.Join(homeDir, name))
				}
			}
		}
	}
	return out
}

func encodeClaudeCWDPath(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch r {
		case '/', '\\', ':', '_', '.':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func resolveClaudeProjectDir(projectsDir, cwd, originalSID string) string {
	jsonlName := originalSID + ".jsonl"
	if cwd != "" {
		primary := filepath.Join(projectsDir, encodeClaudeCWDPath(cwd))
		if originalSID != "" && !isSymlinkPath(filepath.Join(primary, jsonlName)) {
			return primary
		}
		if info, err := os.Stat(primary); err == nil && info.IsDir() && originalSID == "" {
			return primary
		}
	}

	if originalSID != "" {
		if entries, err := os.ReadDir(projectsDir); err == nil {
			for _, e := range entries {
				if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
					continue
				}
				candDir := filepath.Join(projectsDir, e.Name())
				if !isSymlinkPath(filepath.Join(candDir, jsonlName)) {
					return candDir
				}
			}
		}
	}

	if cwd != "" {
		primary := filepath.Join(projectsDir, encodeClaudeCWDPath(cwd))
		if info, err := os.Stat(primary); err == nil && info.IsDir() {
			return primary
		}
	}
	return ""
}

func findClaudeLiveSessionID(projectDir string, startedAtMs int64) string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	var minMtime time.Time
	if startedAtMs > 5000 {
		minMtime = time.UnixMilli(startedAtMs - 5000)
	}
	var bestSID string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		if !minMtime.IsZero() && mod.Before(minMtime) {
			continue
		}
		if mod.After(bestMod) {
			bestMod = mod
			bestSID = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return bestSID
}

func claudeSettingsCandidates(cwd, configRoot, homeDir string) []string {
	var paths []string
	seen := make(map[string]struct{})
	add := func(p string) {
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	if cwd != "" {
		add(filepath.Join(cwd, ".claude", "settings.local.json"))
		add(filepath.Join(cwd, ".claude", "settings.json"))
	}
	if configRoot != "" {
		add(filepath.Join(configRoot, "settings.local.json"))
		add(filepath.Join(configRoot, "settings.json"))
	}
	if homeDir != "" {
		add(filepath.Join(homeDir, ".claude", "settings.local.json"))
		add(filepath.Join(homeDir, ".claude", "settings.json"))
	}
	return paths
}

func readClaudeConfiguredModel(cwd, configRoot, homeDir string) string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CODE_MODEL")); v != "" {
		return v
	}
	for _, p := range claudeSettingsCandidates(cwd, configRoot, homeDir) {
		if m := readJSONStringField(p, "model"); m != "" {
			return m
		}
	}
	return ""
}

func readClaudeEffortLevel(cwd, configRoot, homeDir string) string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CODE_EFFORT_LEVEL")); v != "" {
		return v
	}
	for _, p := range claudeSettingsCandidates(cwd, configRoot, homeDir) {
		if lvl := readJSONStringField(p, "effortLevel"); lvl != "" {
			return lvl
		}
	}
	return ""
}

func readJSONStringField(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(strField(doc, key))
}

func readClaudeMemoryStatus(memoryDir string) (files, lines int) {
	entries, err := os.ReadDir(memoryDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files++
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(memoryDir, "MEMORY.md")); err == nil && len(data) > 0 {
		s := strings.TrimSuffix(string(data), "\n")
		if s != "" {
			lines = strings.Count(s, "\n") + 1
		}
	}
	return files, lines
}

// claudeQuotaProvider reads Claude rate-limit JSON files written by abtop or spaces hooks.
type claudeQuotaProvider struct{}

func newClaudeQuotaProvider() *claudeQuotaProvider {
	return &claudeQuotaProvider{}
}

// flexibleUnixSec unmarshals either numeric Unix epoch seconds or RFC3339 strings.
type flexibleUnixSec int64

func (f *flexibleUnixSec) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexibleUnixSec(i)
		return nil
	}
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexibleUnixSec(int64(fl))
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		str = strings.TrimSpace(str)
		if str == "" {
			*f = 0
			return nil
		}
		if i, err := strconv.ParseInt(str, 10, 64); err == nil {
			*f = flexibleUnixSec(i)
			return nil
		}
		if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
			*f = flexibleUnixSec(t.Unix())
			return nil
		}
		if t, err := time.Parse(time.RFC3339, str); err == nil {
			*f = flexibleUnixSec(t.Unix())
			return nil
		}
	}
	*f = 0
	return nil
}

type rateLimitWindowJSON struct {
	UsedPercentage *float64        `json:"used_percentage"`
	Pct            *float64        `json:"pct"`
	ResetsAt       flexibleUnixSec `json:"resets_at"`
	WindowMinutes  int             `json:"window_minutes"`
}

type rateLimitFileJSON struct {
	Source    string               `json:"source"`
	PlanType  string               `json:"plan_type,omitempty"`
	FiveHour  *rateLimitWindowJSON `json:"five_hour"`
	SevenDay  *rateLimitWindowJSON `json:"seven_day"`
	UpdatedAt flexibleUnixSec      `json:"updated_at"`
}

func (p *claudeQuotaProvider) FetchQuota(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dirs := collectClaudeQuotaDirs(homeDirs)
	var best *agent.QuotaInfo
	var bestFreshness int64

	for _, dir := range dirs {
		for _, fname := range []string{"spaces-rate-limits.json", "abtop-rate-limits.json"} {
			path := filepath.Join(dir, fname)
			info, freshness := readRateLimitQuotaFile(path, "claude")
			if info == nil {
				continue
			}
			if best == nil || freshness > bestFreshness {
				best = info
				bestFreshness = freshness
			}
		}
	}
	if best != nil {
		return best, nil
	}

	// Tier-2 fallback: estimate 5h UTC block usage from recent local Claude JSONL transcripts.
	return estimateClaudeTier2Quota(ctx, dirs, time.Now().UTC()), nil
}

type claudeTier2Call struct {
	ts           time.Time
	activeTokens int64
}

func claudeTier2TokenLimit() int64 {
	if raw := strings.TrimSpace(os.Getenv("SPACES_CLAUDE_5H_TOKEN_LIMIT")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			return v
		}
	}
	return claudeTier2DefaultLimit
}

func estimateClaudeTier2Quota(ctx context.Context, configDirs []string, now time.Time) *agent.QuotaInfo {
	if len(configDirs) == 0 || ctx.Err() != nil {
		return nil
	}

	cutoff := now.Add(-claudeTier2BlockWindowHours * time.Hour)
	type candFile struct {
		path    string
		modTime time.Time
	}
	var files []candFile

	for _, cfgDir := range configDirs {
		projectsDir := filepath.Join(cfgDir, "projects")
		projEntries, err := os.ReadDir(projectsDir)
		if err != nil {
			continue
		}
		for _, pe := range projEntries {
			if pe.Type()&os.ModeSymlink != 0 || !pe.IsDir() {
				continue
			}
			pDir := filepath.Join(projectsDir, pe.Name())
			entries, err := os.ReadDir(pDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.ModTime().Before(cutoff) {
					continue
				}
				files = append(files, candFile{
					path:    filepath.Join(pDir, e.Name()),
					modTime: info.ModTime(),
				})
			}
		}
	}

	if len(files) == 0 {
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	if len(files) > claudeTier2MaxFiles {
		files = files[:claudeTier2MaxFiles]
	}

	var calls []claudeTier2Call
	for _, cf := range files {
		if ctx.Err() != nil {
			return nil
		}
		calls = append(calls, scanClaudeTier2Calls(cf.path)...)
	}
	if len(calls) == 0 {
		return nil
	}

	blockStart, totalTokens, ok := resolveActiveClaude5hBlock(calls, now)
	if !ok {
		return nil
	}

	limit := claudeTier2TokenLimit()
	usedPct := (float64(totalTokens) / float64(limit)) * 100.0
	if usedPct < 0 {
		usedPct = 0
	}
	if usedPct > 100 {
		usedPct = 100
	}

	blockEnd := blockStart.Add(claudeTier2BlockWindowHours * time.Hour)
	return &agent.QuotaInfo{
		Agent:     "claude",
		Source:    "estimated_5h",
		Estimated: true,
		Primary: &agent.QuotaWindow{
			Label:         "5h",
			WindowMinutes: 300,
			UsedPct:       usedPct,
			ResetsAt:      blockEnd.Unix(),
		},
		UpdatedAt: now.Unix(),
	}
}

func scanClaudeTier2Calls(path string) []claudeTier2Call {
	if isSymlinkPath(path) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []claudeTier2Call
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, n, _, hitCap, err := readBoundedLine(reader, telemetryMaxLineBytes)
		if n == 0 && err != nil {
			break
		}
		if hitCap {
			if err != nil {
				break
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, `"usage"`) {
			if err != nil {
				break
			}
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(trimmed), &raw) == nil && strField(raw, "type") == "assistant" {
			tsMs := parseRFC3339Ms(strField(raw, "timestamp"))
			if tsMs > 0 {
				if msg, ok := raw["message"].(map[string]any); ok {
					if usage, ok := msg["usage"].(map[string]any); ok {
						active := int64Field(usage, "input_tokens") +
							int64Field(usage, "output_tokens") +
							int64Field(usage, "cache_creation_input_tokens")
						if active > 0 {
							out = append(out, claudeTier2Call{
								ts:           time.UnixMilli(tsMs).UTC(),
								activeTokens: active,
							})
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out
}

// resolveActiveClaude5hBlock implements the 5h UTC rolling block calculation:
// block start is floored to the UTC hour of the first message in the block,
// and rolls over when a new message is >= 5h after block start or >= 5h after
// the previous message. Returns (blockStart, totalActiveTokens, active).
func resolveActiveClaude5hBlock(calls []claudeTier2Call, now time.Time) (time.Time, int64, bool) {
	if len(calls) == 0 {
		return time.Time{}, 0, false
	}
	sorted := make([]claudeTier2Call, len(calls))
	copy(sorted, calls)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ts.Before(sorted[j].ts)
	})

	windowDur := claudeTier2BlockWindowHours * time.Hour
	var (
		hasBlock    bool
		blockStart  time.Time
		lastEntry   time.Time
		totalTokens int64
	)

	for _, c := range sorted {
		if !hasBlock {
			hasBlock = true
			blockStart = c.ts.UTC().Truncate(time.Hour)
			lastEntry = c.ts
			totalTokens = c.activeTokens
			continue
		}
		rollsOver := c.ts.Sub(blockStart) >= windowDur
		stale := c.ts.Sub(lastEntry) >= windowDur
		if rollsOver || stale {
			blockStart = c.ts.UTC().Truncate(time.Hour)
			lastEntry = c.ts
			totalTokens = c.activeTokens
		} else {
			lastEntry = c.ts
			totalTokens += c.activeTokens
		}
	}

	if !hasBlock {
		return time.Time{}, 0, false
	}
	blockEnd := blockStart.Add(windowDur)
	nowUTC := now.UTC()
	if nowUTC.Before(blockStart) || nowUTC.After(blockEnd) || nowUTC.Sub(lastEntry) >= windowDur {
		return time.Time{}, 0, false
	}
	return blockStart, totalTokens, true
}

func collectClaudeQuotaDirs(homeDirs []string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" {
			return
		}
		clean := filepath.Clean(d)
		if _, ok := seen[clean]; ok {
			return
		}
		if st, err := os.Stat(clean); err == nil && st.IsDir() {
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}

	homes := homeDirs
	if len(homes) == 0 {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			homes = []string{h}
		}
	}
	for _, h := range homes {
		add(filepath.Join(h, ".claude"))
		if entries, err := os.ReadDir(h); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".claude-") && e.IsDir() {
					add(filepath.Join(h, e.Name()))
				}
			}
		}
	}
	if envDir := os.Getenv("CLAUDE_CONFIG_DIR"); envDir != "" {
		add(envDir)
	}
	return out
}

func readRateLimitQuotaFile(path, agentID string) (*agent.QuotaInfo, int64) {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.IsDir() {
		return nil, 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	var rf rateLimitFileJSON
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, 0
	}
	if rf.FiveHour == nil && rf.SevenDay == nil {
		return nil, 0
	}

	q := &agent.QuotaInfo{
		Agent:     agentID,
		Source:    rf.Source,
		PlanType:  rf.PlanType,
		UpdatedAt: int64(rf.UpdatedAt),
	}
	if q.Source == "" {
		q.Source = agentID
	}
	if rf.FiveHour != nil {
		pct, ok := rf.FiveHour.usedPct()
		if ok {
			mins := rf.FiveHour.WindowMinutes
			if mins <= 0 {
				mins = 300
			}
			q.Primary = &agent.QuotaWindow{
				Label:         agent.FormatQuotaWindowLabel(mins),
				WindowMinutes: mins,
				UsedPct:       pct,
				ResetsAt:      int64(rf.FiveHour.ResetsAt),
			}
		}
	}
	if rf.SevenDay != nil {
		pct, ok := rf.SevenDay.usedPct()
		if ok {
			mins := rf.SevenDay.WindowMinutes
			if mins <= 0 {
				mins = 10080
			}
			q.Secondary = &agent.QuotaWindow{
				Label:         agent.FormatQuotaWindowLabel(mins),
				WindowMinutes: mins,
				UsedPct:       pct,
				ResetsAt:      int64(rf.SevenDay.ResetsAt),
			}
		}
	}
	if q.Primary == nil && q.Secondary == nil {
		return nil, 0
	}

	freshness := int64(rf.UpdatedAt)
	if freshness == 0 {
		freshness = st.ModTime().Unix()
	}
	return q, freshness
}

func (w *rateLimitWindowJSON) usedPct() (float64, bool) {
	if w == nil {
		return 0, false
	}
	if w.UsedPercentage != nil {
		return *w.UsedPercentage, true
	}
	if w.Pct != nil {
		return *w.Pct, true
	}
	return 0, false
}

// Shared telemetry helpers used across Claude, Codex, and OpenCode providers.

func isSymlinkPath(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeSymlink != 0
}

// readBoundedLine reads up to maxBytes+1 bytes stopping at '\n' or EOF.
// Returns (line, bytesRead, hasNewline, hitCap, err).
func readBoundedLine(r *bufio.Reader, maxBytes int) (string, int, bool, bool, error) {
	var buf []byte
	for {
		frag, err := r.ReadSlice('\n')
		buf = append(buf, frag...)
		if err == nil {
			if len(buf) > maxBytes {
				return "", len(buf), true, true, nil
			}
			return string(buf), len(buf), true, false, nil
		}
		if err == bufio.ErrBufferFull {
			if len(buf) > maxBytes {
				return "", len(buf), false, true, nil
			}
			continue
		}
		// EOF or other error.
		if len(buf) > maxBytes {
			return "", len(buf), false, true, err
		}
		hasNL := len(buf) > 0 && buf[len(buf)-1] == '\n'
		return string(buf), len(buf), hasNL, false, err
	}
}

func appendCappedInt64(s []int64, v int64, capMax int) []int64 {
	s = append(s, v)
	if len(s) > capMax {
		s = append([]int64(nil), s[len(s)-capMax:]...)
	}
	return s
}

func sanitizeTelemetryText(s string, maxRunes int) string {
	safe := agent.SanitizeTerminalText(s)
	redacted := agent.RedactSecrets(safe)
	return truncateRunes(redacted, maxRunes)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}

func shortenTelemetryPath(path string) string {
	slashed := filepath.ToSlash(path)
	parts := strings.Split(slashed, "/")
	if len(parts) <= 2 {
		return path
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func parseRFC3339Ms(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}

func int64Field(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
	}
	return 0
}

func float64Field(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

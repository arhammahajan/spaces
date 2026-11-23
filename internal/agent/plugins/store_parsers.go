package plugins

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	maxScannerTokenSize = 10 * 1024 * 1024 // 10 MiB for large tool output lines
	maxDerivedTitleLen  = 120
	listHeadScanBytes   = 256 * 1024 // 256 KiB head window for fast List() metadata scan
	listTailScanBytes   = 128 * 1024 // 128 KiB tail window for fast List() metadata scan
)

// AttachProviderTelemetry attaches native ConversationStore, LogParser,
// UsageReader, and UsageLimitProvider implementations to p when p is a
// *agent.BasePlugin for a supported provider plugin and those capabilities are
// not already set.
func AttachProviderTelemetry(p agent.Plugin) {
	bp, ok := p.(*agent.BasePlugin)
	if !ok || bp == nil {
		return
	}
	AttachProviderStores(bp)
	AttachProviderUsageReaders(bp)
	AttachProviderUsageLimits(bp)
}

// AttachProviderStores wires native ConversationStore and LogParser
// implementations onto a *agent.BasePlugin for any of the 9 supported
// providers if CustomConversationStore / CustomLogParser are nil.
func AttachProviderStores(bp *agent.BasePlugin) {
	if bp == nil {
		return
	}
	var (
		store  agent.ConversationStore
		parser agent.LogParser
	)
	switch bp.PluginSpec.ID {
	case "antigravity":
		store = NewAntigravityConversationStore("")
		parser = NewAntigravityLogParser()
	case "claude":
		store = NewClaudeConversationStore("")
		parser = NewClaudeLogParser()
	case "codex":
		store = NewCodexConversationStore("")
		parser = NewCodexLogParser()
	case "cursor-agent":
		store = NewCursorConversationStore("")
		parser = NewCursorLogParser()
	case "deepseek":
		store = NewDeepSeekConversationStore("")
		parser = NewDeepSeekLogParser()
	case "kilo":
		store = NewKiloConversationStore("")
		parser = NewKiloLogParser()
	case "kimi":
		store = NewKimiConversationStore("")
		parser = NewKimiLogParser()
	case "muse":
		store = NewMuseConversationStore("")
		parser = NewMuseLogParser()
	case "opencode":
		store = NewOpenCodeConversationStore("")
		parser = NewOpenCodeLogParser()
	default:
		return
	}
	if bp.CustomConversationStore == nil {
		bp.CustomConversationStore = store
	}
	if bp.CustomLogParser == nil {
		bp.CustomLogParser = parser
	}
}

// Per-provider ConversationStore and LogParser constructors.
// Passing an empty homeDir defaults to os.UserHomeDir() at query time.

// NewAntigravityConversationStore returns a native ConversationStore for Antigravity CLI.
func NewAntigravityConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:         "antigravity",
			appName:         "antigravity",
			relDirs:         []string{".gemini/antigravity-cli/conversations", ".gemini/antigravity-cli/brain"},
			sqliteRelPaths:  []string{".gemini/antigravity-cli/conversation_summaries.db"},
			allowDBFallback: true,
			fileFilter:      antigravityFileFilter,
			parser:          NewAntigravityLogParser(),
			sqliteLoader:    loadAntigravitySQLite,
		},
	}
}

// NewAntigravityLogParser returns a native LogParser for Antigravity transcripts.
func NewAntigravityLogParser() agent.LogParser {
	return structuredLogParser{provider: "antigravity", allowDBNoop: true}
}

// NewClaudeConversationStore returns a native ConversationStore for Claude Code.
func NewClaudeConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:    "claude",
			appName:    "claude",
			relDirs:    []string{".claude/projects"},
			fileFilter: jsonlOnlyFilter,
			parser:     NewClaudeLogParser(),
		},
	}
}

// NewClaudeLogParser returns a native LogParser for Claude Code JSONL transcripts.
func NewClaudeLogParser() agent.LogParser {
	return claudeLogParser{}
}

// NewCodexConversationStore returns a native ConversationStore for OpenAI Codex CLI.
func NewCodexConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:    "codex",
			appName:    "codex",
			relDirs:    []string{".codex/sessions", ".codex/archived_sessions"},
			fileFilter: codexRolloutFilter,
			parser:     NewCodexLogParser(),
		},
	}
}

// NewCodexLogParser returns a native LogParser for Codex rollout JSONL logs.
func NewCodexLogParser() agent.LogParser {
	return codexLogParser{}
}

// NewCursorConversationStore returns a native ConversationStore for Cursor Agent CLI.
func NewCursorConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:    "cursor-agent",
			appName:    "cursor-agent",
			relDirs:    []string{".cursor/chats", ".cursor/sessions"},
			fileFilter: jsonOrJSONLFilter,
			parser:     NewCursorLogParser(),
		},
	}
}

// NewCursorLogParser returns a native LogParser for Cursor Agent session logs.
func NewCursorLogParser() agent.LogParser {
	return structuredLogParser{provider: "cursor-agent"}
}

// NewDeepSeekConversationStore returns a native ConversationStore for DeepSeek Harness.
func NewDeepSeekConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:    "deepseek",
			appName:    "deepseek",
			relDirs:    []string{".deepseek/sessions", ".local/share/deepseek/sessions"},
			fileFilter: jsonOrJSONLFilter,
			parser:     NewDeepSeekLogParser(),
		},
	}
}

// NewDeepSeekLogParser returns a native LogParser for DeepSeek session logs.
func NewDeepSeekLogParser() agent.LogParser {
	return structuredLogParser{provider: "deepseek"}
}

// NewKiloConversationStore returns a native ConversationStore for Kilo Code.
func NewKiloConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:        "kilo",
			appName:        "kilo",
			relDirs:        []string{".local/share/kilo/storage/session", ".config/kilo/sessions"},
			sqliteRelPaths: []string{".local/share/kilo/kilo.db"},
			fileFilter:     jsonOrJSONLFilter,
			parser:         NewKiloLogParser(),
			sqliteLoader:   loadKiloSQLite,
		},
	}
}

// NewKiloLogParser returns a native LogParser for Kilo Code session logs.
func NewKiloLogParser() agent.LogParser {
	return structuredLogParser{provider: "kilo"}
}

// NewKimiConversationStore returns a native ConversationStore for Kimi CLI / Kimi Code.
func NewKimiConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:    "kimi",
			appName:    "kimi",
			relDirs:    []string{".kimi/sessions", ".kimi-code/sessions"},
			fileFilter: jsonOrJSONLFilter,
			parser:     NewKimiLogParser(),
		},
	}
}

// NewKimiLogParser returns a native LogParser for Kimi session logs.
func NewKimiLogParser() agent.LogParser {
	return structuredLogParser{provider: "kimi"}
}

// NewMuseConversationStore returns a native ConversationStore for Muse CLI.
func NewMuseConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:        "muse",
			appName:        "muse",
			relDirs:        []string{".config/muse/sessions", ".local/share/muse/sessions"},
			sqliteRelPaths: []string{".local/share/muse/session-index.db"},
			fileFilter:     jsonOrJSONLFilter,
			parser:         NewMuseLogParser(),
			sqliteLoader:   loadMuseSQLite,
		},
	}
}

// NewMuseLogParser returns a native LogParser for Muse session logs.
func NewMuseLogParser() agent.LogParser {
	return structuredLogParser{provider: "muse"}
}

// NewOpenCodeConversationStore returns a native ConversationStore for OpenCode.
func NewOpenCodeConversationStore(homeDir string) agent.ConversationStore {
	return &fileConversationStore{
		homeDir: homeDir,
		cfg: providerStoreConfig{
			agentID:        "opencode",
			appName:        "opencode",
			relDirs:        []string{".local/share/opencode/storage/session", ".opencode/sessions"},
			sqliteRelPaths: []string{".local/share/opencode/opencode.db"},
			fileFilter:     jsonOrJSONLFilter,
			parser:         NewOpenCodeLogParser(),
			sqliteLoader:   loadOpenCodeSQLite,
		},
	}
}

// NewOpenCodeLogParser returns a native LogParser for OpenCode session logs.
func NewOpenCodeLogParser() agent.LogParser {
	return structuredLogParser{provider: "opencode"}
}

// -----------------------------------------------------------------------------
// Provider store configuration and file filters
// -----------------------------------------------------------------------------

type providerStoreConfig struct {
	agentID         string
	appName         string
	relDirs         []string
	sqliteRelPaths  []string
	allowDBFallback bool
	fileFilter      func(name string) bool
	parser          agent.LogParser
	sqliteLoader    func(ctx context.Context, home string) []agent.ConversationRecord
}

type boundedLogParser interface {
	parseLogBounded(ctx context.Context, path string, headBytes, tailBytes int64) ([]agent.LogEntry, error)
}

func jsonlOnlyFilter(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".jsonl")
}

func jsonOrJSONLFilter(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".json")
}

func codexRolloutFilter(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "rollout-") && strings.HasSuffix(lower, ".jsonl")
}

func antigravityFileFilter(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jsonl") ||
		strings.HasSuffix(lower, ".json") ||
		strings.HasSuffix(lower, ".db")
}

// -----------------------------------------------------------------------------
// Shared filesystem-backed ConversationStore
// -----------------------------------------------------------------------------

type fileConversationStore struct {
	homeDir string
	cfg     providerStoreConfig
}

func (s *fileConversationStore) resolveHome(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	if strings.TrimSpace(s.homeDir) != "" {
		return s.homeDir, nil
	}
	return os.UserHomeDir()
}

// StorePaths returns the candidate filesystem paths under homeDir that this
// store inspects (conversation directories and SQLite databases).
func (s *fileConversationStore) StorePaths(homeDir string) []string {
	home, err := s.resolveHome(homeDir)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	out := make([]string, 0, len(s.cfg.relDirs)+len(s.cfg.sqliteRelPaths))
	for _, rel := range s.cfg.relDirs {
		out = append(out, filepath.Join(home, filepath.FromSlash(rel)))
	}
	for _, rel := range s.cfg.sqliteRelPaths {
		out = append(out, filepath.Join(home, filepath.FromSlash(rel)))
	}
	return out
}

func (s *fileConversationStore) List(ctx context.Context, opts agent.ConversationListOptions) ([]agent.ConversationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := s.resolveHome(opts.HomeDir)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	bySession := make(map[string]agent.ConversationRecord)
	hasSQLiteRecords := false
	if s.cfg.sqliteLoader != nil {
		sqliteRecords := s.cfg.sqliteLoader(ctx, home)
		if len(sqliteRecords) > 0 {
			hasSQLiteRecords = true
			for _, rec := range sqliteRecords {
				key := rec.SessionID
				if key == "" {
					key = rec.SourcePath
				}
				if existing, exists := bySession[key]; exists {
					bySession[key] = mergeConversationRecords(existing, rec)
				} else {
					bySession[key] = rec
				}
			}
		}
	}

	if (s.cfg.agentID == "antigravity" || s.cfg.agentID == "muse") && hasSQLiteRecords {
		// SQLite index is authoritative for this home (avoids untitled subagents
		// and internal artifacts).
		return s.finalizeRecords(bySession, opts), nil
	}

	var codexTitles map[string]string
	if s.cfg.agentID == "codex" {
		codexTitles = loadCodexSessionIndex(home)
	}
	var kimiIdx kimiSessionIndex
	if s.cfg.agentID == "kimi" {
		kimiIdx = loadKimiIndexes(home)
	}

	for _, relDir := range s.cfg.relDirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root := filepath.Join(home, relDir)
		info, statErr := os.Stat(root)
		if statErr != nil || !info.IsDir() {
			continue
		}

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".index-cache", "messages", "steps":
					return fs.SkipDir
				}
				return nil
			}
			if s.cfg.agentID == "kimi" && strings.EqualFold(d.Name(), "state.json") {
				return nil
			}
			if s.cfg.fileFilter != nil && !s.cfg.fileFilter(d.Name()) {
				return nil
			}

			rec, ok := s.loadRecord(ctx, root, path, d)
			if !ok {
				return nil
			}
			if s.cfg.agentID == "codex" && len(codexTitles) > 0 {
				if idxTitle := strings.TrimSpace(codexTitles[rec.SessionID]); idxTitle != "" {
					rec.Title = cleanSingleLine(idxTitle, maxDerivedTitleLen)
				}
			}
			if s.cfg.agentID == "kimi" {
				if kimiIdx.deleted[rec.SessionID] {
					return nil
				}
				if rec.CWD == "" {
					if wd := kimiIdx.workDirBySession[rec.SessionID]; wd != "" {
						rec.CWD = wd
					} else if rel, err := filepath.Rel(root, path); err == nil {
						parts := strings.Split(filepath.ToSlash(rel), "/")
						if len(parts) >= 2 {
							if wd := kimiIdx.workDirByHash[strings.ToLower(parts[0])]; wd != "" {
								rec.CWD = wd
							}
						}
					}
				}
				if rec.MessageCount == 0 && strings.TrimSpace(rec.Title) == "" {
					return nil
				}
			}
			key := rec.SessionID
			if key == "" {
				key = rec.SourcePath
			}
			if existing, exists := bySession[key]; exists {
				bySession[key] = mergeConversationRecords(existing, rec)
			} else {
				bySession[key] = rec
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}

	return s.finalizeRecords(bySession, opts), nil
}

func (s *fileConversationStore) finalizeRecords(bySession map[string]agent.ConversationRecord, opts agent.ConversationListOptions) []agent.ConversationRecord {
	wantCWD := normalizeCWD(opts.CWD)
	wantProfile := strings.TrimSpace(opts.Profile)
	records := make([]agent.ConversationRecord, 0, len(bySession))
	for _, rec := range bySession {
		if wantProfile != "" && strings.TrimSpace(rec.Profile) == "" {
			rec.Profile = wantProfile
		}
		if !opts.IncludeArchived && rec.Archived {
			continue
		}
		if wantCWD != "" && normalizeCWD(rec.CWD) != wantCWD {
			continue
		}
		records = append(records, rec)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAtMs != records[j].UpdatedAtMs {
			return records[i].UpdatedAtMs > records[j].UpdatedAtMs
		}
		if records[i].CreatedAtMs != records[j].CreatedAtMs {
			return records[i].CreatedAtMs > records[j].CreatedAtMs
		}
		return records[i].SessionID < records[j].SessionID
	})

	if opts.Limit > 0 && len(records) > opts.Limit {
		records = records[:opts.Limit]
	}
	return records
}

func (s *fileConversationStore) Search(ctx context.Context, query string, opts agent.ConversationListOptions) ([]agent.ConversationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return s.List(ctx, opts)
	}

	listOpts := opts
	listOpts.Limit = 0
	candidates, err := s.List(ctx, listOpts)
	if err != nil {
		return nil, err
	}

	var matched []agent.ConversationRecord
	for _, rec := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if recordMatchesQuery(rec, q) {
			matched = append(matched, rec)
		} else if s.cfg.parser != nil && rec.SourcePath != "" {
			entries, parseErr := s.cfg.parser.ParseLog(ctx, rec.SourcePath)
			if parseErr == nil && entriesMatchQuery(entries, q) {
				matched = append(matched, rec)
			}
		}
		if opts.Limit > 0 && len(matched) >= opts.Limit {
			break
		}
	}
	return matched, nil
}

func (s *fileConversationStore) loadRecord(ctx context.Context, root, path string, d fs.DirEntry) (agent.ConversationRecord, bool) {
	info, err := d.Info()
	var mtimeMs int64
	if err == nil && info != nil {
		mtimeMs = info.ModTime().UnixMilli()
	}

	rec := agent.ConversationRecord{
		Agent:       s.cfg.agentID,
		App:         s.cfg.appName,
		SessionID:   inferSessionIDFromPath(s.cfg.agentID, root, path),
		CreatedAtMs: mtimeMs,
		UpdatedAtMs: mtimeMs,
		Archived:    pathLooksArchived(path),
		SourcePath:  path,
	}

	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".db") {
		if !s.cfg.allowDBFallback {
			return agent.ConversationRecord{}, false
		}
		return rec, rec.SessionID != ""
	}

	meta := extractFileMetadata(ctx, s.cfg.agentID, path)
	if meta.isSubagent {
		return agent.ConversationRecord{}, false
	}
	if meta.sessionID != "" {
		rec.SessionID = meta.sessionID
	}
	if meta.title != "" {
		rec.Title = meta.title
	}
	if meta.cwd != "" {
		rec.CWD = meta.cwd
	}
	if meta.model != "" {
		rec.Model = meta.model
	}
	if meta.profile != "" {
		rec.Profile = meta.profile
	}
	if meta.archived {
		rec.Archived = true
	}
	if meta.createdAtMs > 0 {
		rec.CreatedAtMs = meta.createdAtMs
	}
	if meta.updatedAtMs > 0 {
		rec.UpdatedAtMs = meta.updatedAtMs
	}

	if s.cfg.parser != nil {
		var (
			entries  []agent.LogEntry
			parseErr error
		)
		if strings.HasSuffix(lower, ".jsonl") && info != nil && info.Size() > listHeadScanBytes {
			if bp, ok := s.cfg.parser.(boundedLogParser); ok {
				entries, parseErr = bp.parseLogBounded(ctx, path, listHeadScanBytes, listTailScanBytes)
			} else {
				entries, parseErr = s.cfg.parser.ParseLog(ctx, path)
			}
		} else {
			entries, parseErr = s.cfg.parser.ParseLog(ctx, path)
		}
		if parseErr == nil && len(entries) > 0 {
			var firstUser, firstAny, lastAny string
			var minTs, maxTs int64
			msgCount := 0
			for _, e := range entries {
				if rec.SessionID == "" && e.SessionID != "" {
					rec.SessionID = e.SessionID
				}
				if rec.Model == "" && e.Model != "" {
					rec.Model = e.Model
				}
				if e.TimestampMs > 0 {
					if minTs == 0 || e.TimestampMs < minTs {
						minTs = e.TimestampMs
					}
					if e.TimestampMs > maxTs {
						maxTs = e.TimestampMs
					}
				}
				if e.Event == "custom-title" && strings.TrimSpace(e.Text) != "" {
					rec.Title = cleanSingleLine(e.Text, maxDerivedTitleLen)
					continue
				}
				if e.Event == "summary" && rec.Title == "" && strings.TrimSpace(e.Text) != "" {
					rec.Title = cleanSingleLine(e.Text, maxDerivedTitleLen)
					continue
				}
				if e.Event == "meta" {
					continue
				}
				text := strings.TrimSpace(e.Text)
				role := strings.ToLower(strings.TrimSpace(e.Role))
				if role == "user" || role == "assistant" {
					msgCount++
					if text != "" && !isBoilerplateUserMessage(text) {
						if role == "user" && firstUser == "" {
							firstUser = text
						}
						if firstAny == "" {
							firstAny = text
						}
						lastAny = text
					}
				}
			}
			if firstUser != "" {
				rec.FirstMessage = firstUser
			} else {
				rec.FirstMessage = firstAny
			}
			rec.LastMessage = lastAny
			rec.MessageCount = msgCount
			if minTs > 0 && meta.createdAtMs == 0 {
				rec.CreatedAtMs = minTs
			}
			if maxTs > 0 && meta.updatedAtMs == 0 {
				rec.UpdatedAtMs = maxTs
			}
		}
	}

	if rec.Title == "" && rec.FirstMessage != "" {
		rec.Title = cleanSingleLine(rec.FirstMessage, maxDerivedTitleLen)
	}
	if rec.UpdatedAtMs == 0 {
		rec.UpdatedAtMs = mtimeMs
	}
	if rec.CreatedAtMs == 0 {
		rec.CreatedAtMs = rec.UpdatedAtMs
	}
	return rec, rec.SessionID != "" || rec.MessageCount > 0 || rec.Title != ""
}

func mergeConversationRecords(a, b agent.ConversationRecord) agent.ConversationRecord {
	// Prefer non-.db record with richer content as base.
	aIsDB := strings.HasSuffix(strings.ToLower(a.SourcePath), ".db")
	bIsDB := strings.HasSuffix(strings.ToLower(b.SourcePath), ".db")
	primary, secondary := a, b
	if (aIsDB && !bIsDB) ||
		(aIsDB == bIsDB && b.MessageCount > a.MessageCount) ||
		(aIsDB == bIsDB && b.MessageCount == a.MessageCount && b.Title != "" && a.Title == "") {
		primary, secondary = b, a
	}

	if primary.Title == "" {
		primary.Title = secondary.Title
	}
	if primary.CWD == "" {
		primary.CWD = secondary.CWD
	}
	if primary.Model == "" {
		primary.Model = secondary.Model
	}
	if primary.Profile == "" {
		primary.Profile = secondary.Profile
	}
	if primary.FirstMessage == "" {
		primary.FirstMessage = secondary.FirstMessage
	}
	if primary.LastMessage == "" {
		primary.LastMessage = secondary.LastMessage
	}
	if secondary.MessageCount > primary.MessageCount {
		primary.MessageCount = secondary.MessageCount
	}
	if secondary.UpdatedAtMs > primary.UpdatedAtMs {
		primary.UpdatedAtMs = secondary.UpdatedAtMs
	}
	if secondary.CreatedAtMs > 0 && (primary.CreatedAtMs == 0 || secondary.CreatedAtMs < primary.CreatedAtMs) {
		primary.CreatedAtMs = secondary.CreatedAtMs
	}
	primary.Archived = primary.Archived || secondary.Archived
	return primary
}

func recordMatchesQuery(rec agent.ConversationRecord, q string) bool {
	for _, field := range []string{
		rec.Title,
		rec.FirstMessage,
		rec.LastMessage,
		rec.CWD,
		rec.SessionID,
		rec.Model,
	} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

func entriesMatchQuery(entries []agent.LogEntry, q string) bool {
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Text), q) ||
			strings.Contains(strings.ToLower(e.ToolName), q) ||
			strings.Contains(strings.ToLower(e.Model), q) ||
			strings.Contains(strings.ToLower(e.SessionID), q) {
			return true
		}
	}
	return false
}

func normalizeCWD(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func pathLooksArchived(path string) bool {
	slashed := "/" + strings.ToLower(filepath.ToSlash(path)) + "/"
	return strings.Contains(slashed, "/archived/") || strings.Contains(slashed, "/archived_sessions/")
}

func inferSessionIDFromPath(agentID, root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err == nil {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		// Antigravity brain layout: brain/<session-id>/.system_generated/logs/transcript.jsonl
		if agentID == "antigravity" && strings.HasSuffix(filepath.ToSlash(root), "/brain") && len(parts) >= 2 {
			if seg := strings.TrimSpace(parts[0]); seg != "" && seg != "." {
				return seg
			}
		}
		// Kimi layouts:
		//  - ~/.kimi-code/sessions/wd_<name>_<hash>/session_<uuid>/agents/main/wire.jsonl
		//  - ~/.kimi/sessions/<cwd-md5-hash>/<uuid>/wire.jsonl (or context.jsonl)
		if agentID == "kimi" {
			for _, seg := range parts {
				if strings.HasPrefix(seg, "session_") && len(seg) > len("session_") {
					return seg
				}
			}
			if len(parts) >= 3 && parts[1] != "." && parts[1] != "" {
				return parts[1]
			}
			if len(parts) == 2 && parts[0] != "." && parts[0] != "" {
				return parts[0]
			}
		}
		// Muse layout: sessions/YYYY/MM/DD/<session-uuid>/session.jsonl
		if agentID == "muse" && len(parts) >= 2 {
			parent := strings.TrimSpace(parts[len(parts)-2])
			if parent != "" && parent != "." {
				return parent
			}
		}
		// Generic nested session dir like sessions/<session-id>/session.json or transcript.jsonl
		baseNoExt := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		switch strings.ToLower(baseNoExt) {
		case "transcript", "transcript_full", "session", "metadata", "wire", "context", "messages", "chat":
			if len(parts) >= 2 && parts[len(parts)-2] != "." && parts[len(parts)-2] != "" {
				return parts[len(parts)-2]
			}
		}
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func cleanSingleLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// -----------------------------------------------------------------------------
// Metadata extraction from JSON / JSONL files
// -----------------------------------------------------------------------------

type fileMetadata struct {
	sessionID   string
	title       string
	cwd         string
	model       string
	profile     string
	archived    bool
	isSubagent  bool
	createdAtMs int64
	updatedAtMs int64
}

func loadCodexSessionIndex(home string) map[string]string {
	idxPath := filepath.Join(home, ".codex", "session_index.jsonl")
	f, err := os.Open(idxPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	titles := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 32*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			Title      string `json:"title"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		id := strings.TrimSpace(rec.ID)
		name := firstNonEmpty(rec.ThreadName, rec.Title)
		if id != "" && name != "" {
			titles[id] = name
		}
	}
	return titles
}

type kimiSessionIndex struct {
	workDirBySession map[string]string
	workDirByHash    map[string]string
	deleted          map[string]bool
}

func loadKimiIndexes(home string) kimiSessionIndex {
	idx := kimiSessionIndex{
		workDirBySession: make(map[string]string),
		workDirByHash:    make(map[string]string),
		deleted:          make(map[string]bool),
	}

	// 1. ~/.kimi-code/session_index.jsonl (sessionId -> workDir, deleted tombstones)
	if f, err := os.Open(filepath.Join(home, ".kimi-code", "session_index.jsonl")); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 32*1024), 1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var rec struct {
				SessionID string `json:"sessionId"`
				WorkDir   string `json:"workDir"`
				Deleted   bool   `json:"deleted"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}
			sid := strings.TrimSpace(rec.SessionID)
			if sid == "" {
				continue
			}
			if rec.Deleted {
				idx.deleted[sid] = true
			} else {
				delete(idx.deleted, sid)
				if wd := strings.TrimSpace(rec.WorkDir); wd != "" {
					idx.workDirBySession[sid] = wd
				}
			}
		}
	}

	// 2. ~/.kimi/kimi.json (work_dirs[].path -> md5(path) used as ~/.kimi/sessions/<hash>)
	if data, err := os.ReadFile(filepath.Join(home, ".kimi", "kimi.json")); err == nil {
		var doc struct {
			WorkDirs []struct {
				Path string `json:"path"`
			} `json:"work_dirs"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(data), &doc); err == nil {
			for _, wd := range doc.WorkDirs {
				p := strings.TrimSpace(wd.Path)
				if p == "" {
					continue
				}
				sum := md5.Sum([]byte(p))
				idx.workDirByHash[hex.EncodeToString(sum[:])] = p
			}
		}
	}

	return idx
}

func extractFileMetadata(ctx context.Context, provider, path string) fileMetadata {
	var meta fileMetadata
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		data, err := os.ReadFile(path)
		if err != nil {
			return meta
		}
		var doc map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(data), &doc); err != nil {
			return meta
		}
		applyMetaObject(&meta, doc)
		return meta
	}

	_ = scanJSONLBounded(ctx, path, listHeadScanBytes, listTailScanBytes, func(line []byte) error {
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			return nil
		}
		// Codex embeds session_meta / turn_context inside payload.
		// NOTE: turn_context carries `"summary": "none"` (reasoning summary mode),
		// which must NOT be mistaken for a conversation title.
		if provider == "codex" {
			typ := strField(obj, "type")
			payload, _ := obj["payload"].(map[string]any)
			switch typ {
			case "session_meta":
				if payload != nil {
					if strField(payload, "parent_thread_id") != "" {
						meta.isSubagent = true
					}
					if meta.sessionID == "" {
						meta.sessionID = firstNonEmpty(strField(payload, "id"), strField(payload, "session_id"))
					}
					if meta.title == "" {
						meta.title = firstNonEmpty(strField(payload, "thread_name"), strField(payload, "title"))
					}
					if meta.cwd == "" {
						meta.cwd = strField(payload, "cwd")
					}
					if meta.model == "" {
						meta.model = strField(payload, "model")
					}
					if ts := parseTimestampMs(payload["timestamp"]); ts > 0 && (meta.createdAtMs == 0 || ts < meta.createdAtMs) {
						meta.createdAtMs = ts
					}
				}
			case "turn_context":
				if payload != nil {
					if meta.cwd == "" {
						meta.cwd = strField(payload, "cwd")
					}
					if meta.model == "" {
						meta.model = strField(payload, "model")
					}
				}
			}
			return nil
		}
		// Muse JSONL lines carry a per-event `"id"` (record UUID) and a nested
		// `"stream": {"kind": "session", "id": "<session-uuid>"}`. Extract stream.id
		// for sessionID and never treat the per-event `"id"` as the session ID.
		if provider == "muse" {
			if stream, ok := obj["stream"].(map[string]any); ok {
				if sid := strField(stream, "id"); sid != "" {
					meta.sessionID = sid
				}
				// Apply non-ID metadata from obj and payload.record while preserving stream.id
				savedSID := meta.sessionID
				applyMetaObject(&meta, obj)
				if payload, ok := obj["payload"].(map[string]any); ok {
					if recObj, ok := payload["record"].(map[string]any); ok {
						applyMetaObject(&meta, recObj)
					}
				}
				if savedSID != "" {
					meta.sessionID = savedSID
				}
				return nil
			}
			// Flat JSONL format (no stream envelope): apply standard metadata
			applyMetaObject(&meta, obj)
			return nil
		}
		applyMetaObject(&meta, obj)
		// Claude embeds model inside message
		if msg, ok := obj["message"].(map[string]any); ok {
			if meta.model == "" {
				meta.model = strField(msg, "model")
			}
		}
		return nil
	})
	return meta
}

func applyMetaObject(meta *fileMetadata, obj map[string]any) {
	if meta.sessionID == "" {
		meta.sessionID = firstNonEmpty(
			strField(obj, "sessionId"),
			strField(obj, "session_id"),
			strField(obj, "conversationId"),
			strField(obj, "conversation_id"),
			strField(obj, "chatId"),
			strField(obj, "chat_id"),
			strField(obj, "id"),
		)
	}
	if ct := firstNonEmpty(strField(obj, "customTitle"), strField(obj, "custom_title")); ct != "" {
		meta.title = cleanSingleLine(ct, maxDerivedTitleLen)
	} else if meta.title == "" {
		meta.title = firstNonEmpty(
			strField(obj, "title"),
			strField(obj, "summary"),
			strField(obj, "name"),
		)
	}
	if meta.cwd == "" {
		meta.cwd = firstNonEmpty(
			strField(obj, "cwd"),
			strField(obj, "directory"),
			strField(obj, "work_dir"),
			strField(obj, "workDir"),
			strField(obj, "workspace"),
			strField(obj, "workspacePath"),
			strField(obj, "project_dir"),
		)
	}
	if meta.model == "" {
		meta.model = firstNonEmpty(
			strField(obj, "model"),
			strField(obj, "model_id"),
			strField(obj, "modelId"),
		)
	}
	if meta.profile == "" {
		meta.profile = strField(obj, "profile")
	}
	if boolField(obj, "archived") || boolField(obj, "isArchived") || boolField(obj, "is_archived") {
		meta.archived = true
	}
	if ts := firstTimestamp(obj, "createdAt", "created_at", "created", "started_at", "startedAt"); ts > 0 {
		if meta.createdAtMs == 0 || ts < meta.createdAtMs {
			meta.createdAtMs = ts
		}
	}
	if ts := firstTimestamp(obj, "updatedAt", "updated_at", "updated", "last_activity", "lastActivity"); ts > 0 {
		if ts > meta.updatedAtMs {
			meta.updatedAtMs = ts
		}
	}
	if timeObj, ok := obj["time"].(map[string]any); ok {
		if ts := parseTimestampMs(timeObj["created"]); ts > 0 && (meta.createdAtMs == 0 || ts < meta.createdAtMs) {
			meta.createdAtMs = ts
		}
		if ts := parseTimestampMs(timeObj["updated"]); ts > 0 && ts > meta.updatedAtMs {
			meta.updatedAtMs = ts
		}
		if parseTimestampMs(timeObj["archived"]) > 0 || boolField(timeObj, "archived") {
			meta.archived = true
		}
	}
}

// -----------------------------------------------------------------------------
// Claude Code LogParser
// -----------------------------------------------------------------------------

type claudeLogParser struct{}

func (p claudeLogParser) ParseLog(ctx context.Context, path string) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONL(ctx, path, func(line []byte) error {
		parseClaudeLine(line, &entries, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (p claudeLogParser) parseLogBounded(ctx context.Context, path string, headBytes, tailBytes int64) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONLBounded(ctx, path, headBytes, tailBytes, func(line []byte) error {
		parseClaudeLine(line, &entries, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func parseClaudeLine(line []byte, entries *[]agent.LogEntry, sessionID, lastModel *string) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return // ignore malformed lines gracefully
	}

	if sid := firstNonEmpty(strField(obj, "sessionId"), strField(obj, "session_id")); sid != "" {
		*sessionID = sid
	}
	ts := firstTimestamp(obj, "timestamp", "ts", "created_at", "createdAt")
	typ := strField(obj, "type")
	role := strField(obj, "role")
	model := strField(obj, "model")

	var content any = obj["content"]
	if msg, ok := obj["message"].(map[string]any); ok {
		if r := strField(msg, "role"); r != "" {
			role = r
		}
		if m := strField(msg, "model"); m != "" {
			model = m
		}
		if c, exists := msg["content"]; exists {
			content = c
		}
	}
	if model != "" {
		*lastModel = model
	}
	effModel := firstNonEmpty(model, *lastModel)

	if typ == "custom-title" {
		title := firstNonEmpty(strField(obj, "customTitle"), strField(obj, "custom_title"), strField(obj, "title"), strField(obj, "text"))
		if title != "" {
			*entries = append(*entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "system",
				Event:       "custom-title",
				Text:        title,
				SessionID:   *sessionID,
				Model:       effModel,
			})
		}
		return
	}

	if typ == "summary" {
		summary := firstNonEmpty(strField(obj, "summary"), strField(obj, "text"))
		if summary != "" {
			*entries = append(*entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "system",
				Event:       "summary",
				Text:        summary,
				SessionID:   *sessionID,
				Model:       effModel,
			})
		}
		return
	}

	if role == "" && (typ == "user" || typ == "assistant" || typ == "system") {
		role = typ
	}

	text, toolCalls, toolResults := extractContentParts(content)
	if text == "" {
		text = firstNonEmpty(strField(obj, "text"), strField(obj, "summary"))
	}

	event := typ
	if boolField(obj, "isMeta") {
		event = "meta"
	} else if event == "" {
		event = "message"
	}

	if text != "" {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        role,
			Event:       event,
			Text:        text,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
	for _, tc := range toolCalls {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        firstNonEmpty(role, "assistant"),
			Event:       "tool_use",
			Text:        tc.text,
			ToolName:    tc.name,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
	for _, tr := range toolResults {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        "tool",
			Event:       "tool_result",
			Text:        tr.text,
			ToolName:    tr.name,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
}

// -----------------------------------------------------------------------------
// Codex Rollout LogParser
// -----------------------------------------------------------------------------

type codexLogParser struct{}

func (p codexLogParser) ParseLog(ctx context.Context, path string) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONL(ctx, path, func(line []byte) error {
		parseCodexLine(line, &entries, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (p codexLogParser) parseLogBounded(ctx context.Context, path string, headBytes, tailBytes int64) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONLBounded(ctx, path, headBytes, tailBytes, func(line []byte) error {
		parseCodexLine(line, &entries, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func parseCodexLine(line []byte, entries *[]agent.LogEntry, sessionID, lastModel *string) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return
	}

	ts := firstTimestamp(obj, "timestamp", "ts")
	typ := strField(obj, "type")
	payload, _ := obj["payload"].(map[string]any)

	switch typ {
	case "session_meta":
		if payload != nil {
			if id := firstNonEmpty(strField(payload, "id"), strField(payload, "session_id")); id != "" {
				*sessionID = id
			}
			if m := strField(payload, "model"); m != "" {
				*lastModel = m
			}
			if ts == 0 {
				ts = parseTimestampMs(payload["timestamp"])
			}
		}
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        "system",
			Event:       "session_meta",
			Text:        strField(payload, "cwd"),
			SessionID:   *sessionID,
			Model:       *lastModel,
		})

	case "turn_context":
		if payload != nil {
			if m := strField(payload, "model"); m != "" {
				*lastModel = m
			}
		}

	case "response_item":
		if payload == nil {
			return
		}
		pType := strField(payload, "type")
		switch pType {
		case "message":
			role := strField(payload, "role")
			text, toolCalls, toolResults := extractContentParts(payload["content"])
			if text != "" {
				*entries = append(*entries, agent.LogEntry{
					TimestampMs: ts,
					Role:        role,
					Event:       "message",
					Text:        text,
					SessionID:   *sessionID,
					Model:       *lastModel,
				})
			}
			for _, tc := range toolCalls {
				*entries = append(*entries, agent.LogEntry{
					TimestampMs: ts,
					Role:        firstNonEmpty(role, "assistant"),
					Event:       "tool_call",
					Text:        tc.text,
					ToolName:    tc.name,
					SessionID:   *sessionID,
					Model:       *lastModel,
				})
			}
			for _, tr := range toolResults {
				*entries = append(*entries, agent.LogEntry{
					TimestampMs: ts,
					Role:        "tool",
					Event:       "tool_result",
					Text:        tr.text,
					ToolName:    tr.name,
					SessionID:   *sessionID,
					Model:       *lastModel,
				})
			}

		case "function_call", "custom_tool_call":
			name := firstNonEmpty(strField(payload, "name"), strField(payload, "tool_name"))
			argsText := stringifyAny(firstNonNil(payload["arguments"], payload["input"]))
			*entries = append(*entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "assistant",
				Event:       pType,
				Text:        argsText,
				ToolName:    name,
				SessionID:   *sessionID,
				Model:       *lastModel,
			})

		case "function_call_output", "custom_tool_call_output":
			outText := stringifyAny(payload["output"])
			*entries = append(*entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "tool",
				Event:       pType,
				Text:        outText,
				ToolName:    strField(payload, "name"),
				SessionID:   *sessionID,
				Model:       *lastModel,
			})

		case "reasoning":
			text, _, _ := extractContentParts(payload["summary"])
			if text != "" {
				*entries = append(*entries, agent.LogEntry{
					TimestampMs: ts,
					Role:        "assistant",
					Event:       "reasoning",
					Text:        text,
					SessionID:   *sessionID,
					Model:       *lastModel,
				})
			}
		}

	case "event_msg":
		if payload == nil {
			return
		}
		evType := strField(payload, "type")
		text := firstNonEmpty(strField(payload, "message"), strField(payload, "text"), strField(payload, "title"))
		role := ""
		switch evType {
		case "user_message":
			role = "user"
		case "agent_message", "agent_reasoning":
			role = "assistant"
		default:
			role = "system"
		}
		if text != "" || evType != "" {
			*entries = append(*entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        role,
				Event:       firstNonEmpty(evType, "event_msg"),
				Text:        text,
				SessionID:   *sessionID,
				Model:       *lastModel,
			})
		}

	default:
		// Fallback for generic/flat entries inside codex logs
		appendStructuredEntry(entries, obj, sessionID, lastModel)
	}
}

// -----------------------------------------------------------------------------
// Structured JSON / JSONL LogParser (Antigravity, OpenCode, Kilo, Kimi, Muse,
// Cursor Agent, DeepSeek)
// -----------------------------------------------------------------------------

type structuredLogParser struct {
	provider    string
	allowDBNoop bool
}

func (p structuredLogParser) ParseLog(ctx context.Context, path string) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".db") || strings.Contains(lower, ".db#") {
		return parseSQLiteLog(ctx, p.provider, path)
	}

	if strings.HasSuffix(lower, ".json") {
		return parseJSONDocumentLog(ctx, path)
	}

	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONL(ctx, path, func(line []byte) error {
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			return nil // ignore malformed lines gracefully
		}
		appendStructuredEntry(&entries, obj, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (p structuredLogParser) parseLogBounded(ctx context.Context, path string, headBytes, tailBytes int64) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".db") || strings.Contains(lower, ".db#") || strings.HasSuffix(lower, ".json") {
		return p.ParseLog(ctx, path)
	}
	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)
	err := scanJSONLBounded(ctx, path, headBytes, tailBytes, func(line []byte) error {
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			return nil // ignore malformed lines gracefully
		}
		appendStructuredEntry(&entries, obj, &sessionID, &lastModel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func parseJSONDocumentLog(ctx context.Context, path string) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}

	var (
		entries   []agent.LogEntry
		sessionID string
		lastModel string
	)

	// Top-level JSON array of entries/messages
	if trimmed[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, nil
		}
		for _, item := range arr {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			appendStructuredEntry(&entries, item, &sessionID, &lastModel)
		}
		return entries, nil
	}

	// Top-level JSON object (e.g., OpenCode / Kilo / Cursor / Kimi / Muse / DeepSeek / Antigravity session JSON)
	var doc map[string]any
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return nil, nil
	}
	sessionID = firstNonEmpty(
		strField(doc, "sessionId"),
		strField(doc, "session_id"),
		strField(doc, "conversationId"),
		strField(doc, "conversation_id"),
		strField(doc, "chatId"),
		strField(doc, "chat_id"),
		strField(doc, "id"),
	)
	lastModel = firstNonEmpty(strField(doc, "model"), strField(doc, "model_id"), strField(doc, "modelId"))

	for _, key := range []string{"messages", "entries", "events", "turns", "history", "conversation"} {
		rawList, ok := doc[key].([]any)
		if !ok {
			continue
		}
		for _, rawItem := range rawList {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if item, ok := rawItem.(map[string]any); ok {
				appendStructuredEntry(&entries, item, &sessionID, &lastModel)
			}
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}

	// If the JSON object itself is a single log/session metadata record with no nested messages array
	if title := firstNonEmpty(strField(doc, "title"), strField(doc, "summary"), strField(doc, "text")); title != "" {
		ts := firstTimestamp(doc, "updatedAt", "updated_at", "timestamp", "createdAt", "created_at")
		if ts == 0 {
			if timeObj, ok := doc["time"].(map[string]any); ok {
				ts = firstTimestamp(timeObj, "updated", "created")
			}
		}
		entries = append(entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        firstNonEmpty(strField(doc, "role"), "system"),
			Event:       firstNonEmpty(strField(doc, "event"), strField(doc, "type"), "session"),
			Text:        title,
			SessionID:   sessionID,
			Model:       lastModel,
		})
	}
	return entries, nil
}

func appendStructuredEntry(entries *[]agent.LogEntry, obj map[string]any, sessionID, lastModel *string) {
	if sid := firstNonEmpty(
		strField(obj, "sessionId"),
		strField(obj, "session_id"),
		strField(obj, "conversationId"),
		strField(obj, "conversation_id"),
		strField(obj, "chatId"),
	); sid != "" {
		*sessionID = sid
	}
	if stream, ok := obj["stream"].(map[string]any); ok {
		if sid := strField(stream, "id"); sid != "" {
			*sessionID = sid
		}
	}

	// Muse session.jsonl envelope: payload.kind == "run" with payload.event.prompt
	if payload, ok := obj["payload"].(map[string]any); ok {
		if recObj, ok := payload["record"].(map[string]any); ok {
			if m := firstNonEmpty(strField(recObj, "model_id"), strField(recObj, "model")); m != "" {
				*lastModel = m
			}
		}
		if strField(payload, "kind") == "run" {
			if evObj, ok := payload["event"].(map[string]any); ok {
				if prompt := strings.TrimSpace(strField(evObj, "prompt")); prompt != "" {
					ts := firstTimestamp(obj, "recorded_at", "timestamp", "ts")
					*entries = append(*entries, agent.LogEntry{
						TimestampMs: ts,
						Role:        "user",
						Event:       "run_started",
						Text:        prompt,
						SessionID:   *sessionID,
						Model:       *lastModel,
					})
					return
				}
			}
		}
	}

	model := firstNonEmpty(strField(obj, "model"), strField(obj, "model_id"), strField(obj, "modelId"))
	role := firstNonEmpty(strField(obj, "role"), strField(obj, "sender"), strField(obj, "author"))
	event := firstNonEmpty(strField(obj, "event"), strField(obj, "type"))
	toolName := firstNonEmpty(strField(obj, "toolName"), strField(obj, "tool_name"), strField(obj, "tool"))

	ts := firstTimestamp(obj, "timestamp", "timestamp_ms", "timestampMs", "ts", "created_at", "createdAt", "time")
	if ts == 0 {
		if timeObj, ok := obj["time"].(map[string]any); ok {
			ts = firstTimestamp(timeObj, "created", "updated", "start")
		}
	}

	var content any = firstNonNil(obj["content"], obj["parts"], obj["text"], obj["message"])
	if msgObj, ok := obj["message"].(map[string]any); ok {
		if role == "" {
			role = strField(msgObj, "role")
		}
		if model == "" {
			model = strField(msgObj, "model")
		}
		if c := firstNonNil(msgObj["content"], msgObj["parts"], msgObj["text"]); c != nil {
			content = c
		}
	}

	if model != "" {
		*lastModel = model
	}
	effModel := firstNonEmpty(model, *lastModel)

	text, toolCalls, toolResults := extractContentParts(content)
	if text == "" {
		text = firstNonEmpty(strField(obj, "text"), strField(obj, "reasoning_content"), strField(obj, "summary"))
	}
	if rawTC := firstNonNil(obj["tool_calls"], obj["toolCalls"]); rawTC != nil {
		toolCalls = append(toolCalls, extractOpenAIToolCalls(rawTC)...)
	}

	if role == "" {
		switch strings.ToLower(event) {
		case "user", "user_message":
			role = "user"
		case "assistant", "agent", "agent_message", "model":
			role = "assistant"
		case "tool", "tool_result":
			role = "tool"
		}
	}
	if event == "" {
		if toolName != "" {
			event = "tool_call"
		} else {
			event = "message"
		}
	}

	if text != "" || toolName != "" {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        role,
			Event:       event,
			Text:        text,
			ToolName:    toolName,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
	for _, tc := range toolCalls {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        firstNonEmpty(role, "assistant"),
			Event:       "tool_call",
			Text:        tc.text,
			ToolName:    tc.name,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
	for _, tr := range toolResults {
		*entries = append(*entries, agent.LogEntry{
			TimestampMs: ts,
			Role:        "tool",
			Event:       "tool_result",
			Text:        tr.text,
			ToolName:    tr.name,
			SessionID:   *sessionID,
			Model:       effModel,
		})
	}
}

// -----------------------------------------------------------------------------
// Low-level JSONL scanning & value extraction helpers
// -----------------------------------------------------------------------------

func scanJSONL(ctx context.Context, path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenSize)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// scanJSONLBounded reads only the first headBytes and last tailBytes of a JSONL
// file larger than headBytes, trimming partial lines at cut boundaries so List()
// avoids reading multi-megabyte transcripts in full.
func scanJSONLBounded(ctx context.Context, path string, headBytes, tailBytes int64, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size <= headBytes || headBytes <= 0 {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), maxScannerTokenSize)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			if err := fn(line); err != nil {
				return err
			}
		}
		return scanner.Err()
	}

	headBuf := make([]byte, headBytes)
	n, err := io.ReadFull(f, headBuf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	headBuf = headBuf[:n]

	var headEnd int64
	if lastNL := bytes.LastIndexByte(headBuf, '\n'); lastNL >= 0 {
		headBuf = headBuf[:lastNL]
		headEnd = int64(lastNL) + 1
	} else {
		headBuf = nil
		headEnd = int64(n)
	}

	emitLines := func(buf []byte) error {
		for len(buf) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			var line []byte
			if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
				line = buf[:idx]
				buf = buf[idx+1:]
			} else {
				line = buf
				buf = nil
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			if err := fn(line); err != nil {
				return err
			}
		}
		return nil
	}

	if err := emitLines(headBuf); err != nil {
		return err
	}

	if tailBytes <= 0 || headEnd >= size {
		return nil
	}

	tailStart := size - tailBytes
	trimmedStart := true
	if tailStart <= headEnd {
		tailStart = headEnd
		trimmedStart = false
	}

	tailLen := size - tailStart
	if tailLen <= 0 {
		return nil
	}
	tailBuf := make([]byte, tailLen)
	tn, err := f.ReadAt(tailBuf, tailStart)
	if err != nil && err != io.EOF {
		return err
	}
	tailBuf = tailBuf[:tn]
	if trimmedStart {
		if firstNL := bytes.IndexByte(tailBuf, '\n'); firstNL >= 0 {
			tailBuf = tailBuf[firstNL+1:]
		} else {
			tailBuf = nil
		}
	}
	return emitLines(tailBuf)
}

func isBoilerplateUserMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		// Claude Code meta / command / IDE wrappers
		"<local-command-caveat>",
		"<command-name>",
		"<command-message>",
		"<system-reminder>",
		"<ide_opened_file>",
		"<ide_selection>",
		"[request interrupted",
		// Codex system / context wrappers
		"<environment_context>",
		"# agents.md",
		"<instructions>",
		"<permissions instructions>",
		"<user_instructions>",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

type toolCallInfo struct {
	name string
	text string
}

func extractContentParts(v any) (text string, toolCalls []toolCallInfo, toolResults []toolCallInfo) {
	switch val := v.(type) {
	case nil:
		return "", nil, nil
	case string:
		return strings.TrimSpace(val), nil, nil
	case []any:
		var parts []string
		for _, item := range val {
			switch block := item.(type) {
			case string:
				if s := strings.TrimSpace(block); s != "" {
					parts = append(parts, s)
				}
			case map[string]any:
				bType := strings.ToLower(strField(block, "type"))
				switch bType {
				case "tool_use", "tool_call", "function_call", "tool-invocation":
					name := firstNonEmpty(strField(block, "name"), strField(block, "toolName"), strField(block, "tool"))
					args := stringifyAny(firstNonNil(block["input"], block["arguments"], block["args"]))
					toolCalls = append(toolCalls, toolCallInfo{name: name, text: args})
				case "tool_result", "function_call_output", "tool-result":
					name := firstNonEmpty(strField(block, "name"), strField(block, "toolName"), strField(block, "tool"))
					out := stringifyAny(firstNonNil(block["content"], block["output"], block["result"], block["text"]))
					toolResults = append(toolResults, toolCallInfo{name: name, text: out})
				case "thinking", "reasoning":
					// Skip internal thinking if regular text is present; otherwise ignore
				default:
					if s := firstNonEmpty(strField(block, "text"), strField(block, "content"), strField(block, "value")); s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.Join(parts, "\n"), toolCalls, toolResults
	case map[string]any:
		if s := firstNonEmpty(strField(val, "text"), strField(val, "content"), strField(val, "message")); s != "" {
			return s, nil, nil
		}
		return "", nil, nil
	default:
		return "", nil, nil
	}
}

func extractOpenAIToolCalls(v any) []toolCallInfo {
	rawList, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []toolCallInfo
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := firstNonEmpty(strField(m, "name"), strField(m, "tool_name"))
		args := stringifyAny(firstNonNil(m["arguments"], m["input"]))
		if fn, ok := m["function"].(map[string]any); ok {
			if name == "" {
				name = strField(fn, "name")
			}
			if args == "" {
				args = stringifyAny(fn["arguments"])
			}
		}
		if name != "" || args != "" {
			out = append(out, toolCallInfo{name: name, text: args})
		}
	}
	return out
}

func stringifyAny(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(val)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key].(bool)
	return ok && v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func firstTimestamp(m map[string]any, keys ...string) int64 {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		if v, exists := m[k]; exists {
			if ts := parseTimestampMs(v); ts > 0 {
				return ts
			}
		}
	}
	return 0
}

func parseTimestampMs(v any) int64 {
	switch val := v.(type) {
	case float64:
		return normalizeNumericTimestamp(val)
	case int64:
		return normalizeNumericTimestamp(float64(val))
	case int:
		return normalizeNumericTimestamp(float64(val))
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return normalizeNumericTimestamp(f)
		}
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return normalizeNumericTimestamp(f)
		}
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UnixMilli()
			}
		}
	}
	return 0
}

func normalizeNumericTimestamp(n float64) int64 {
	if n <= 0 {
		return 0
	}
	switch {
	case n >= 1e17: // nanoseconds
		return int64(n / 1e6)
	case n >= 1e14: // microseconds
		return int64(n / 1e3)
	case n >= 1e11: // milliseconds
		return int64(n)
	case n >= 1e9: // seconds
		return int64(n * 1000)
	default:
		return int64(n)
	}
}

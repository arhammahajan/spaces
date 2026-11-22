package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	maxConvScanWorkers      = 8
	maxConvSnippetRunes     = 140
	maxConvTitleRunes       = 120
	maxConvPreviewRunes     = 200
	currentConvCacheVersion = 3
)

// ConversationHomeDirFn resolves the real user home directory used when
// scanning native agent conversation stores. Tests may override it.
var ConversationHomeDirFn = func() string {
	return agent.RealHomeDir("")
}

// ConversationOverlayPathFn resolves the path to conversations-overlay.db.
// Tests may override it (including with a legacy .json path).
var ConversationOverlayPathFn = defaultConversationOverlayPath

// ConversationCachePathFn resolves the path to conversations-cache.json.
// Tests may override it.
var ConversationCachePathFn = defaultConversationCachePath

func defaultConversationOverlayPath() string {
	if override := strings.TrimSpace(os.Getenv("TMUX_SPACES_CONVERSATION_OVERLAY")); override != "" {
		return override
	}
	xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if xdg == "" {
		home := strings.TrimSpace(ConversationHomeDirFn())
		if home == "" {
			home = agent.RealHomeDir("")
		}
		if home == "" {
			return ""
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdg, "tmux-spaces", "conversations-overlay.db")
}

func defaultConversationCachePath() string {
	if override := strings.TrimSpace(os.Getenv("TMUX_SPACES_CONVERSATION_CACHE")); override != "" {
		return override
	}
	xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if xdg == "" {
		home := strings.TrimSpace(ConversationHomeDirFn())
		if home == "" {
			home = agent.RealHomeDir("")
		}
		if home == "" {
			return ""
		}
		xdg = filepath.Join(home, ".cache")
	}
	return filepath.Join(xdg, "tmux-spaces", "conversations-cache.json")
}

type conversationOverlayEntry struct {
	Agent        string   `json:"agent"`
	SessionID    string   `json:"session_id"`
	Space        string   `json:"space,omitempty"`
	LastOpenedMs int64    `json:"last_opened_ms,omitempty"`
	LastOutcome  string   `json:"last_outcome,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	Title        string   `json:"title,omitempty"`
	LaunchCmd    []string `json:"launch_cmd,omitempty"`
	UpdatedAtMs  int64    `json:"updated_at_ms,omitempty"`
}

type conversationOverlayFile struct {
	Version int                                 `json:"version"`
	Entries map[string]conversationOverlayEntry `json:"entries"`
}

type cachedConvSnapshot struct {
	Version         int                 `json:"version"`
	SyncedMs        int64               `json:"synced_ms"`
	IndexedMessages int                 `json:"indexed_messages"`
	Conversations   []ConversationEntry `json:"conversations"`
}

type conversationCacheState struct {
	mu              sync.RWMutex
	loaded          bool
	entries         []ConversationEntry
	syncedMs        int64
	indexedMessages int
}

var (
	convCache   conversationCacheState
	overlayMu   sync.Mutex
	diskCacheMu sync.Mutex
)

// ResetConversationStoreCache clears the in-memory conversation snapshot cache.
// Intended for tests.
func ResetConversationStoreCache() {
	convCache.mu.Lock()
	defer convCache.mu.Unlock()
	convCache.loaded = false
	convCache.entries = nil
	convCache.syncedMs = 0
	convCache.indexedMessages = 0
}

func loadConversationDiskCache() (cachedConvSnapshot, bool) {
	path := strings.TrimSpace(ConversationCachePathFn())
	if path == "" {
		return cachedConvSnapshot{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return cachedConvSnapshot{}, false
	}
	var snap cachedConvSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return cachedConvSnapshot{}, false
	}
	if snap.Version != currentConvCacheVersion {
		return cachedConvSnapshot{}, false
	}
	if len(snap.Conversations) == 0 && snap.SyncedMs <= 0 {
		return cachedConvSnapshot{}, false
	}
	return snap, true
}

func saveConversationDiskCache(snap cachedConvSnapshot) error {
	path := strings.TrimSpace(ConversationCachePathFn())
	if path == "" {
		return nil
	}
	if snap.Version == 0 {
		snap.Version = currentConvCacheVersion
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	diskCacheMu.Lock()
	defer diskCacheMu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".conversations-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func mergeConversationOverlay(entries []ConversationEntry) []ConversationEntry {
	overlayMu.Lock()
	overlay := loadConversationOverlay(ConversationOverlayPathFn())
	overlayMu.Unlock()

	out := make([]ConversationEntry, len(entries))
	copy(out, entries)
	if len(overlay) > 0 {
		for i := range out {
			if ov, ok := overlay[overlayKey(out[i].Agent, out[i].ID)]; ok {
				applyOverlayToEntry(&out[i], ov)
			}
		}
	}
	sortConversationEntries(out)
	return out
}

func overlayKey(canonAgent, sessionID string) string {
	return canonAgent + ":" + sessionID
}

func loadConversationOverlay(path string) map[string]conversationOverlayEntry {
	return loadConversationOverlaySQLite(path)
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func applyOverlayToEntry(entry *ConversationEntry, ov conversationOverlayEntry) {
	if ov.Space != "" {
		entry.Space = ov.Space
	}
	if ov.LastOpenedMs > entry.LastOpenedMs {
		entry.LastOpenedMs = ov.LastOpenedMs
	}
	if ov.LastOutcome != "" {
		entry.LastOutcome = ov.LastOutcome
	}
	if strings.TrimSpace(entry.Profile) == "" && ov.Profile != "" {
		entry.Profile = ov.Profile
	}
	if strings.TrimSpace(entry.CWD) == "" && ov.CWD != "" {
		entry.CWD = ov.CWD
	}
	if strings.TrimSpace(entry.Title) == "" && ov.Title != "" {
		entry.Title = ov.Title
	}
	if len(ov.LaunchCmd) > 0 {
		entry.LaunchCmd = cloneStringSlice(ov.LaunchCmd)
	}
}

type convScanJob struct {
	plugin          agent.Plugin
	store           agent.ConversationStore
	homeDir         string
	resolvedHomeDir string
	profile         string
}

type storePathReporter interface {
	StorePaths(homeDir string) []string
}

type convDedupKey struct {
	agent string
	id    string
}

func resolveRealPath(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(clean)
}

func isPathWithin(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// pathPointsIntoRealHome reports whether path (which may carry a "#id"
// SQLite fragment suffix) resolves via symlinks to a location inside
// resolvedRealHome and outside resolvedProfileHome.
func pathPointsIntoRealHome(path, resolvedProfileHome, resolvedRealHome string) bool {
	if resolvedRealHome == "" || resolvedProfileHome == "" {
		return false
	}
	clean := strings.TrimSpace(path)
	if clean == "" {
		return false
	}
	if idx := strings.IndexByte(clean, '#'); idx >= 0 {
		clean = clean[:idx]
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return false
	}
	resolved = filepath.Clean(resolved)
	return isPathWithin(resolved, resolvedRealHome) && !isPathWithin(resolved, resolvedProfileHome)
}

// profileStorePointsToRealHome reports whether profileHome itself resolves to
// resolvedRealHome, or all existing store paths under profileHome resolve via
// symlinks into resolvedRealHome (outside profileHome).
func profileStorePointsToRealHome(store agent.ConversationStore, profileHome, resolvedRealHome string) bool {
	if resolvedRealHome == "" || strings.TrimSpace(profileHome) == "" {
		return false
	}
	resolvedProfile, err := filepath.EvalSymlinks(profileHome)
	if err != nil {
		return false
	}
	resolvedProfile = filepath.Clean(resolvedProfile)
	if resolvedProfile == resolvedRealHome {
		return true
	}
	reporter, ok := store.(storePathReporter)
	if !ok {
		return false
	}
	paths := reporter.StorePaths(profileHome)
	if len(paths) == 0 {
		return false
	}
	existing := 0
	for _, p := range paths {
		if _, statErr := os.Stat(p); statErr != nil {
			continue
		}
		existing++
		if !pathPointsIntoRealHome(p, resolvedProfile, resolvedRealHome) {
			return false
		}
	}
	return existing > 0
}

func scanAllConversations(ctx context.Context) ([]ConversationEntry, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	reg := agent.DefaultRegistry()
	realHome := strings.TrimSpace(ConversationHomeDirFn())
	resolvedRealHome := resolveRealPath(realHome)

	var jobs []convScanJob
	for _, p := range reg.RuntimePlugins() {
		store := p.ConversationStore()
		if store == nil {
			continue
		}
		if realHome != "" {
			jobs = append(jobs, convScanJob{
				plugin:          p,
				store:           store,
				homeDir:         realHome,
				resolvedHomeDir: resolvedRealHome,
				profile:         "",
			})
		}
	}

	if realHome != "" {
		for _, pr := range reg.DiscoverProfileRoots(realHome) {
			p, ok := reg.ResolveRuntime(pr.Agent)
			if !ok {
				continue
			}
			store := p.ConversationStore()
			if store == nil {
				continue
			}
			if profileStorePointsToRealHome(store, pr.HomeDir, resolvedRealHome) {
				continue
			}
			jobs = append(jobs, convScanJob{
				plugin:          p,
				store:           store,
				homeDir:         pr.HomeDir,
				resolvedHomeDir: resolveRealPath(pr.HomeDir),
				profile:         pr.Profile,
			})
		}
	}

	results := make([][]ConversationEntry, len(jobs))
	sem := make(chan struct{}, maxConvScanWorkers)
	var wg sync.WaitGroup

	for i, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, j convScanJob) {
			defer wg.Done()
			defer func() { <-sem }()

			recs, err := j.store.List(ctx, agent.ConversationListOptions{
				HomeDir:         j.homeDir,
				Profile:         j.profile,
				IncludeArchived: true,
			})
			if err != nil || len(recs) == 0 {
				return
			}
			mapped := make([]ConversationEntry, 0, len(recs))
			for _, rec := range recs {
				fallbackProfile := j.profile
				if fallbackProfile != "" && pathPointsIntoRealHome(rec.SourcePath, j.resolvedHomeDir, resolvedRealHome) {
					fallbackProfile = ""
					if strings.EqualFold(strings.TrimSpace(rec.Profile), j.profile) {
						rec.Profile = ""
					}
				}
				mapped = append(mapped, mapRecordToEntry(reg, j.plugin, fallbackProfile, rec))
			}
			results[idx] = mapped
		}(i, job)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	byKey := make(map[convDedupKey]ConversationEntry)
	for _, batch := range results {
		for _, entry := range batch {
			idKey := strings.TrimSpace(entry.ID)
			if idKey == "" {
				idKey = strings.TrimSpace(entry.SourcePath)
			}
			if idKey == "" {
				continue
			}
			k := convDedupKey{agent: entry.Agent, id: idKey}
			if existing, exists := byKey[k]; exists {
				byKey[k] = mergeConversationEntries(existing, entry)
			} else {
				byKey[k] = entry
			}
		}
	}

	overlayMu.Lock()
	overlay := loadConversationOverlay(ConversationOverlayPathFn())
	overlayMu.Unlock()

	entries := make([]ConversationEntry, 0, len(byKey))
	indexedMessages := 0
	for _, entry := range byKey {
		if ov, ok := overlay[overlayKey(entry.Agent, entry.ID)]; ok {
			applyOverlayToEntry(&entry, ov)
		}
		indexedMessages += entry.MessageCount
		entries = append(entries, entry)
	}

	sortConversationEntries(entries)
	return entries, indexedMessages, nil
}

func mapRecordToEntry(reg *agent.Registry, p agent.Plugin, fallbackProfile string, rec agent.ConversationRecord) ConversationEntry {
	rawAgent := strings.TrimSpace(rec.Agent)
	if rawAgent == "" && p != nil {
		rawAgent = p.Spec().ID
	}
	canonAgent := reg.ConversationID(rawAgent)
	app := reg.CLIDisplayApp(rawAgent)
	if app == "" {
		app = strings.TrimSpace(rec.App)
	}
	profile := strings.TrimSpace(rec.Profile)
	if profile == "" {
		profile = strings.TrimSpace(fallbackProfile)
	}
	title := strings.TrimSpace(rec.Title)
	if title == "" {
		title = truncateSingleLine(rec.FirstMessage, maxConvTitleRunes)
	}
	preview := truncateSingleLine(rec.LastMessage, maxConvPreviewRunes)
	if preview == "" {
		preview = truncateSingleLine(rec.FirstMessage, maxConvPreviewRunes)
	}

	return ConversationEntry{
		Agent:        canonAgent,
		ID:           strings.TrimSpace(rec.SessionID),
		App:          app,
		Profile:      profile,
		Title:        title,
		Preview:      preview,
		CWD:          strings.TrimSpace(rec.CWD),
		Model:        strings.TrimSpace(rec.Model),
		CreatedAtMs:  rec.CreatedAtMs,
		UpdatedAtMs:  rec.UpdatedAtMs,
		MessageCount: rec.MessageCount,
		Archived:     rec.Archived,
		SourcePath:   strings.TrimSpace(rec.SourcePath),
	}
}

func mergeConversationEntries(a, b ConversationEntry) ConversationEntry {
	winner, loser := a, b
	if b.UpdatedAtMs > a.UpdatedAtMs ||
		(b.UpdatedAtMs == a.UpdatedAtMs && b.MessageCount > a.MessageCount) {
		winner, loser = b, a
	}
	if winner.Profile == "" {
		winner.Profile = loser.Profile
	}
	if winner.Title == "" {
		winner.Title = loser.Title
	}
	if winner.Preview == "" {
		winner.Preview = loser.Preview
	}
	if winner.CWD == "" {
		winner.CWD = loser.CWD
	}
	if winner.Branch == "" {
		winner.Branch = loser.Branch
	}
	if winner.Model == "" {
		winner.Model = loser.Model
	}
	if winner.App == "" {
		winner.App = loser.App
	}
	if winner.SourcePath == "" {
		winner.SourcePath = loser.SourcePath
	}
	if winner.ResumeTarget == "" {
		winner.ResumeTarget = loser.ResumeTarget
	}
	if len(winner.LaunchCmd) == 0 && len(loser.LaunchCmd) > 0 {
		winner.LaunchCmd = cloneStringSlice(loser.LaunchCmd)
	}
	if loser.CreatedAtMs > 0 && (winner.CreatedAtMs == 0 || loser.CreatedAtMs < winner.CreatedAtMs) {
		winner.CreatedAtMs = loser.CreatedAtMs
	}
	if loser.MessageCount > winner.MessageCount {
		winner.MessageCount = loser.MessageCount
	}
	return winner
}

func sortConversationEntries(entries []ConversationEntry) {
	sort.Slice(entries, func(i, j int) bool {
		ei, ej := entries[i].EffectiveTimeMs(), entries[j].EffectiveTimeMs()
		if ei != ej {
			return ei > ej
		}
		if entries[i].UpdatedAtMs != entries[j].UpdatedAtMs {
			return entries[i].UpdatedAtMs > entries[j].UpdatedAtMs
		}
		if entries[i].CreatedAtMs != entries[j].CreatedAtMs {
			return entries[i].CreatedAtMs > entries[j].CreatedAtMs
		}
		if entries[i].Agent != entries[j].Agent {
			return entries[i].Agent < entries[j].Agent
		}
		return entries[i].ID < entries[j].ID
	})
}

func filterArchivedEntries(entries []ConversationEntry, includeArchived bool) []ConversationEntry {
	out := make([]ConversationEntry, 0, len(entries))
	for _, e := range entries {
		if !includeArchived && e.Archived {
			continue
		}
		out = append(out, e)
	}
	return out
}

func fetchConversationsNative(ctx context.Context, refresh, includeArchived bool) (ConversationSnapshot, error) {
	if !refresh {
		convCache.mu.RLock()
		if convCache.loaded {
			filtered := filterArchivedEntries(convCache.entries, includeArchived)
			snap := ConversationSnapshot{
				OK:              true,
				Conversations:   filtered,
				SyncedMs:        convCache.syncedMs,
				IndexedMessages: convCache.indexedMessages,
				PendingSources:  0,
			}
			convCache.mu.RUnlock()
			return snap, nil
		}
		convCache.mu.RUnlock()

		if diskSnap, ok := loadConversationDiskCache(); ok {
			entries := mergeConversationOverlay(diskSnap.Conversations)
			cacheCopy := append([]ConversationEntry(nil), entries...)

			convCache.mu.Lock()
			convCache.loaded = true
			convCache.entries = cacheCopy
			convCache.syncedMs = diskSnap.SyncedMs
			convCache.indexedMessages = diskSnap.IndexedMessages
			convCache.mu.Unlock()

			return ConversationSnapshot{
				OK:              true,
				Conversations:   filterArchivedEntries(entries, includeArchived),
				SyncedMs:        diskSnap.SyncedMs,
				IndexedMessages: diskSnap.IndexedMessages,
				PendingSources:  0,
			}, nil
		}
	}

	entries, indexedMessages, err := scanAllConversations(ctx)
	if err != nil {
		return ConversationSnapshot{}, err
	}
	nowMs := time.Now().UnixMilli()
	cacheCopy := append([]ConversationEntry(nil), entries...)

	convCache.mu.Lock()
	convCache.loaded = true
	convCache.entries = cacheCopy
	convCache.syncedMs = nowMs
	convCache.indexedMessages = indexedMessages
	convCache.mu.Unlock()

	_ = saveConversationDiskCache(cachedConvSnapshot{
		Version:         currentConvCacheVersion,
		SyncedMs:        nowMs,
		IndexedMessages: indexedMessages,
		Conversations:   entries,
	})

	return ConversationSnapshot{
		OK:              true,
		Conversations:   filterArchivedEntries(entries, includeArchived),
		SyncedMs:        nowMs,
		IndexedMessages: indexedMessages,
		PendingSources:  0,
	}, nil
}

func applyOverlayToConvCache(canonAgent, sid string, ov conversationOverlayEntry) {
	convCache.mu.Lock()
	if convCache.loaded {
		updated := false
		for i := range convCache.entries {
			if convCache.entries[i].Agent == canonAgent && convCache.entries[i].ID == sid {
				applyOverlayToEntry(&convCache.entries[i], ov)
				updated = true
			}
		}
		if updated {
			sortConversationEntries(convCache.entries)
		}
	}
	convCache.mu.Unlock()
}

func recordConversationOpenNative(ctx context.Context, conv ConversationEntry, space string, nowMs int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonAgent := CanonicalConversationAgent(conv.Agent)
	if canonAgent == "" {
		canonAgent = CanonicalConversationAgent(conv.App)
	}
	sid := strings.TrimSpace(conv.ID)
	if sid == "" {
		sid = conv.ResumeSessionID()
	}
	if canonAgent == "" || sid == "" {
		return nil
	}

	ov := conversationOverlayEntry{
		Agent:        canonAgent,
		SessionID:    sid,
		Space:        strings.TrimSpace(space),
		LastOpenedMs: nowMs,
		LastOutcome:  strings.TrimSpace(conv.LastOutcome),
		Profile:      strings.TrimSpace(conv.Profile),
		CWD:          strings.TrimSpace(conv.CWD),
		Title:        strings.TrimSpace(conv.Title),
		LaunchCmd:    cloneStringSlice(conv.LaunchCmd),
		UpdatedAtMs:  nowMs,
	}

	overlayPath := ConversationOverlayPathFn()
	overlayMu.Lock()
	err := upsertConversationOverlaySQLite(overlayPath, ov)
	overlayMu.Unlock()
	if err != nil {
		return err
	}

	// Keep the in-memory cache immediately consistent with the new overlay.
	applyOverlayToConvCache(canonAgent, sid, ov)
	return nil
}

func recordConversationLaunchNative(ctx context.Context, agentName, sessionID, profile, cwd, title string, launchCmd []string, space string, nowMs int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonAgent := CanonicalConversationAgent(agentName)
	sid := strings.TrimSpace(sessionID)
	if canonAgent == "" || sid == "" {
		return nil
	}

	updatedAt := nowMs
	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}

	ov := conversationOverlayEntry{
		Agent:        canonAgent,
		SessionID:    sid,
		Space:        strings.TrimSpace(space),
		LastOpenedMs: nowMs,
		Profile:      strings.TrimSpace(profile),
		CWD:          strings.TrimSpace(cwd),
		Title:        strings.TrimSpace(title),
		LaunchCmd:    cloneStringSlice(launchCmd),
		UpdatedAtMs:  updatedAt,
	}

	overlayPath := ConversationOverlayPathFn()
	overlayMu.Lock()
	err := upsertConversationOverlaySQLite(overlayPath, ov)
	overlayMu.Unlock()
	if err != nil {
		return err
	}

	// Keep the in-memory cache immediately consistent with the new overlay.
	applyOverlayToConvCache(canonAgent, sid, ov)
	return nil
}

func normalizeSearchScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "titles":
		return "titles"
	case "user":
		return "user"
	default:
		return "all"
	}
}

func searchConversationsNative(ctx context.Context, req ConversationSearchRequest) (ConversationSearchResponse, error) {
	scope := normalizeSearchScope(req.Scope)
	q := strings.TrimSpace(req.Query)
	if q == "" {
		return ConversationSearchResponse{OK: true, Query: "", Scope: scope}, nil
	}
	if err := ctx.Err(); err != nil {
		return ConversationSearchResponse{}, err
	}

	snap, err := fetchConversationsNative(ctx, false, req.IncludeArchived)
	if err != nil {
		return ConversationSearchResponse{}, err
	}

	wantAgent := ""
	if strings.TrimSpace(req.Agent) != "" {
		wantAgent = CanonicalConversationAgent(req.Agent)
	}
	wantCWD := ""
	if strings.TrimSpace(req.CWD) != "" {
		wantCWD = filepath.Clean(strings.TrimSpace(req.CWD))
	}

	candidates := make([]ConversationEntry, 0, len(snap.Conversations))
	for _, entry := range snap.Conversations {
		if wantAgent != "" && CanonicalConversationAgent(entry.Agent) != wantAgent {
			continue
		}
		if wantCWD != "" {
			if strings.TrimSpace(entry.CWD) == "" || filepath.Clean(strings.TrimSpace(entry.CWD)) != wantCWD {
				continue
			}
		}
		candidates = append(candidates, entry)
	}

	reg := agent.DefaultRegistry()
	hitsByIdx := make([]*ConversationSearchHit, len(candidates))

	if scope == "titles" {
		for i, entry := range candidates {
			if hit, ok := matchConversationMetadata(entry, q); ok {
				h := hit
				hitsByIdx[i] = &h
			}
		}
	} else {
		sem := make(chan struct{}, maxConvScanWorkers)
		var wg sync.WaitGroup
		for i, entry := range candidates {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int, e ConversationEntry) {
				defer wg.Done()
				defer func() { <-sem }()

				best, hasBest := matchConversationMetadata(e, q)
				if msgHit, ok := matchConversationMessages(ctx, reg, e, q, scope); ok {
					if !hasBest || msgHit.Score > best.Score {
						best = msgHit
						hasBest = true
					}
				}
				if hasBest {
					h := best
					hitsByIdx[idx] = &h
				}
			}(i, entry)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return ConversationSearchResponse{}, err
		}
	}

	hits := make([]ConversationSearchHit, 0, len(candidates))
	for _, h := range hitsByIdx {
		if h != nil {
			hits = append(hits, *h)
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		ti, tj := hits[i].Conversation.EffectiveTimeMs(), hits[j].Conversation.EffectiveTimeMs()
		if ti != tj {
			return ti > tj
		}
		return hits[i].Conversation.ID < hits[j].Conversation.ID
	})

	if req.Limit > 0 && len(hits) > req.Limit {
		hits = hits[:req.Limit]
	}

	return ConversationSearchResponse{
		OK:    true,
		Query: q,
		Scope: scope,
		Hits:  hits,
	}, nil
}

func matchConversationMetadata(entry ConversationEntry, query string) (ConversationSearchHit, bool) {
	type fieldSpec struct {
		text      string
		baseScore float64
	}
	fields := []fieldSpec{
		{text: entry.Title, baseScore: 100.0},
		{text: entry.ID, baseScore: 75.0},
		{text: entry.CWD, baseScore: 70.0},
		{text: entry.Model, baseScore: 65.0},
		{text: entry.Profile, baseScore: 65.0},
	}

	var best ConversationSearchHit
	found := false
	for _, f := range fields {
		if strings.TrimSpace(f.text) == "" {
			continue
		}
		ok, exact, snippet, spans := matchAndBuildSnippet(f.text, query, maxConvSnippetRunes)
		if !ok {
			continue
		}
		score := f.baseScore + recencyBonus(entry.EffectiveTimeMs())
		if exact {
			score += 10.0
		}
		if !found || score > best.Score {
			best = ConversationSearchHit{
				Conversation:      entry,
				MatchKind:         "metadata",
				Snippet:           snippet,
				SnippetHighlights: spans,
				Score:             score,
			}
			found = true
		}
	}
	return best, found
}

func matchConversationMessages(
	ctx context.Context,
	reg *agent.Registry,
	entry ConversationEntry,
	query, scope string,
) (ConversationSearchHit, bool) {
	if strings.TrimSpace(entry.SourcePath) == "" {
		return ConversationSearchHit{}, false
	}
	p, ok := reg.ResolveRuntime(entry.Agent)
	if !ok || p.LogParser() == nil {
		return ConversationSearchHit{}, false
	}
	logEntries, err := p.LogParser().ParseLog(ctx, entry.SourcePath)
	if err != nil || len(logEntries) == 0 {
		return ConversationSearchHit{}, false
	}

	var best ConversationSearchHit
	found := false
	for _, le := range logEntries {
		if ctx.Err() != nil {
			return ConversationSearchHit{}, false
		}
		role := strings.ToLower(strings.TrimSpace(le.Role))
		if scope == "user" && role != "user" {
			continue
		}
		if strings.TrimSpace(le.Text) == "" {
			continue
		}
		matched, exact, snippet, spans := matchAndBuildSnippet(le.Text, query, maxConvSnippetRunes)
		if !matched {
			continue
		}
		base := 50.0
		if role == "user" {
			base = 80.0
		}
		score := base + recencyBonus(entry.EffectiveTimeMs())
		if exact {
			score += 10.0
		}
		if !found || score > best.Score {
			best = ConversationSearchHit{
				Conversation:      entry,
				MatchKind:         "message",
				MatchRole:         role,
				Snippet:           snippet,
				SnippetHighlights: spans,
				Score:             score,
			}
			found = true
		}
	}
	return best, found
}

// recencyBonus returns a small tie-breaker in [0, 1) that favors more recent
// conversations without overpowering match-kind priority.
func recencyBonus(tsMs int64) float64 {
	if tsMs <= 0 {
		return 0
	}
	// Map Unix milliseconds smoothly into (0, 1).
	const ref = 2e12 // ~2033 in ms
	b := float64(tsMs) / ref
	if b >= 0.99 {
		return 0.99
	}
	return b
}

func fetchConversationTitlesSnapshot(ctx context.Context, queries []TitleQuery) (TitleSnapshot, error) {
	if len(queries) == 0 {
		return TitleSnapshot{OK: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return TitleSnapshot{}, err
	}

	// Checks in-memory cache first, then disk cache, before falling back to a full scan.
	snap, err := fetchConversationsNative(ctx, false, true)
	if err != nil {
		return TitleSnapshot{}, err
	}

	byAgentSession := make(map[string]string, len(snap.Conversations))
	bySessionOnly := make(map[string]string, len(snap.Conversations))
	for _, entry := range snap.Conversations {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		sid := strings.TrimSpace(entry.ID)
		if sid == "" {
			continue
		}
		k := overlayKey(entry.Agent, sid)
		if _, exists := byAgentSession[k]; !exists {
			byAgentSession[k] = title
		}
		if _, exists := bySessionOnly[sid]; !exists {
			bySessionOnly[sid] = title
		}
	}

	titles := make(map[string]string, len(queries))
	for _, q := range queries {
		key := strings.TrimSpace(q.Key)
		if key == "" {
			continue
		}
		canonAgent := CanonicalConversationAgent(q.Agent)
		sid := strings.TrimSpace(q.SessionID)
		if sid != "" {
			if canonAgent != "" {
				if title, ok := byAgentSession[overlayKey(canonAgent, sid)]; ok {
					titles[key] = title
					continue
				}
			}
			if title, ok := bySessionOnly[sid]; ok {
				titles[key] = title
			}
			continue
		}

		wantCWD := strings.TrimSpace(q.CWD)
		if wantCWD == "" {
			continue
		}
		cleanCWD := filepath.Clean(wantCWD)

		// snap.Conversations is sorted newest-first; prefer non-archived, fallback to archived.
		var archivedFallback string
		for _, entry := range snap.Conversations {
			if canonAgent != "" && entry.Agent != canonAgent {
				continue
			}
			if strings.TrimSpace(entry.CWD) == "" || filepath.Clean(strings.TrimSpace(entry.CWD)) != cleanCWD {
				continue
			}
			title := strings.TrimSpace(entry.Title)
			if title == "" {
				continue
			}
			if !entry.Archived {
				titles[key] = title
				archivedFallback = ""
				break
			}
			if archivedFallback == "" {
				archivedFallback = title
			}
		}
		if _, ok := titles[key]; !ok && archivedFallback != "" {
			titles[key] = archivedFallback
		}
	}

	return TitleSnapshot{
		OK:     true,
		Titles: titles,
	}, nil
}

// -----------------------------------------------------------------------------
// Snippet & rune-accurate highlight helpers
// -----------------------------------------------------------------------------

func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func truncateSingleLine(s string, maxRunes int) string {
	norm := collapseWhitespace(s)
	if norm == "" || maxRunes <= 0 {
		return norm
	}
	runes := []rune(norm)
	if len(runes) <= maxRunes {
		return norm
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

func toLowerRunes(runes []rune) []rune {
	out := make([]rune, len(runes))
	for i, r := range runes {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func indexRunesFrom(haystack, needle []rune, from int) int {
	nLen := len(needle)
	if nLen == 0 || from < 0 || from+nLen > len(haystack) {
		return -1
	}
	limit := len(haystack) - nLen
	first := needle[0]
	for i := from; i <= limit; i++ {
		if haystack[i] != first {
			continue
		}
		match := true
		for j := 1; j < nLen; j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func matchAndBuildSnippet(text, query string, maxRunes int) (bool, bool, string, []SnippetSpan) {
	normText := collapseWhitespace(text)
	normQuery := collapseWhitespace(query)
	if normText == "" || normQuery == "" {
		return false, false, "", nil
	}

	textRunes := []rune(normText)
	lowerText := toLowerRunes(textRunes)
	queryRunes := toLowerRunes([]rune(normQuery))

	if pos := indexRunesFrom(lowerText, queryRunes, 0); pos >= 0 {
		snippet, spans := buildCenteredSnippet(textRunes, lowerText, [][]rune{queryRunes}, pos, len(queryRunes), maxRunes)
		return true, true, snippet, spans
	}

	// Fallback: all whitespace-separated tokens must be present.
	rawTokens := strings.Fields(normQuery)
	if len(rawTokens) < 2 {
		return false, false, "", nil
	}
	tokens := make([][]rune, 0, len(rawTokens))
	firstPos := -1
	firstLen := 0
	for _, tok := range rawTokens {
		tr := toLowerRunes([]rune(tok))
		if len(tr) == 0 {
			continue
		}
		p := indexRunesFrom(lowerText, tr, 0)
		if p < 0 {
			return false, false, "", nil
		}
		if firstPos < 0 || p < firstPos {
			firstPos = p
			firstLen = len(tr)
		}
		tokens = append(tokens, tr)
	}
	if firstPos < 0 {
		return false, false, "", nil
	}
	snippet, spans := buildCenteredSnippet(textRunes, lowerText, tokens, firstPos, firstLen, maxRunes)
	return true, false, snippet, spans
}

func buildCenteredSnippet(
	textRunes, lowerText []rune,
	needles [][]rune,
	matchStart, matchLen, maxRunes int,
) (string, []SnippetSpan) {
	total := len(textRunes)
	if maxRunes <= 0 {
		maxRunes = maxConvSnippetRunes
	}

	winStart := 0
	winEnd := total
	if total > maxRunes {
		center := matchStart + matchLen/2
		winStart = center - maxRunes/2
		if winStart < 0 {
			winStart = 0
		}
		winEnd = winStart + maxRunes
		if winEnd > total {
			winEnd = total
			winStart = total - maxRunes
			if winStart < 0 {
				winStart = 0
			}
		}
	}

	hasLeading := winStart > 0
	hasTrailing := winEnd < total

	// Adjust slice slightly so the final snippet (including ellipses) stays near maxRunes.
	var sb strings.Builder
	offset := 0
	if hasLeading {
		sb.WriteRune('…')
		offset = 1
	}
	windowRunes := textRunes[winStart:winEnd]
	windowLower := lowerText[winStart:winEnd]
	sb.WriteString(string(windowRunes))
	if hasTrailing {
		sb.WriteRune('…')
	}

	var spans []SnippetSpan
	for _, needle := range needles {
		if len(needle) == 0 {
			continue
		}
		from := 0
		for {
			pos := indexRunesFrom(windowLower, needle, from)
			if pos < 0 {
				break
			}
			spans = append(spans, SnippetSpan{
				Start: offset + pos,
				End:   offset + pos + len(needle),
			})
			from = pos + len(needle)
		}
	}

	spans = mergeSnippetSpans(spans)
	return sb.String(), spans
}

func mergeSnippetSpans(spans []SnippetSpan) []SnippetSpan {
	if len(spans) <= 1 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End < spans[j].End
	})
	merged := make([]SnippetSpan, 0, len(spans))
	cur := spans[0]
	for _, s := range spans[1:] {
		if s.Start <= cur.End {
			if s.End > cur.End {
				cur.End = s.End
			}
		} else {
			merged = append(merged, cur)
			cur = s
		}
	}
	merged = append(merged, cur)
	return merged
}

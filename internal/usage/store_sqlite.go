package usage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/arhammahajan/spaces/internal/agent"
)

const usageDBSchema = `
CREATE TABLE IF NOT EXISTS usage_records (
	record_key TEXT PRIMARY KEY,
	provider TEXT NOT NULL,
	profile TEXT NOT NULL DEFAULT '',
	timestamp_ms INTEGER NOT NULL,
	day TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	rate_model TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	project TEXT NOT NULL DEFAULT '',
	project_path TEXT NOT NULL DEFAULT '',
	uncached_input INTEGER NOT NULL DEFAULT 0,
	cached_input INTEGER NOT NULL DEFAULT 0,
	cache_creation INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	reported_cost_usd REAL,
	cost_usd REAL NOT NULL DEFAULT 0,
	cache_savings_usd REAL NOT NULL DEFAULT 0,
	cost_input REAL NOT NULL DEFAULT 0,
	cost_cache_read REAL NOT NULL DEFAULT 0,
	cost_cache_write REAL NOT NULL DEFAULT 0,
	cost_output REAL NOT NULL DEFAULT 0,
	cost_unsplit REAL NOT NULL DEFAULT 0,
	cost_source TEXT NOT NULL DEFAULT 'unpriced',
	speed TEXT NOT NULL DEFAULT 'standard',
	tools_json TEXT NOT NULL DEFAULT '',
	source_path TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usage_records_ts ON usage_records(timestamp_ms);
CREATE INDEX IF NOT EXISTS idx_usage_records_day_prov ON usage_records(day, provider);
CREATE INDEX IF NOT EXISTS idx_usage_records_prov_model ON usage_records(provider, model);
CREATE INDEX IF NOT EXISTS idx_usage_records_session ON usage_records(session_id);

CREATE TABLE IF NOT EXISTS usage_files (
	path TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	mtime_ns INTEGER NOT NULL,
	resume_offset INTEGER NOT NULL,
	guard_len INTEGER NOT NULL,
	guard_hash TEXT NOT NULL,
	state_json BLOB,
	records_json BLOB,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_files_updated_at ON usage_files(updated_at);

CREATE TABLE IF NOT EXISTS usage_meta (
	key TEXT PRIMARY KEY,
	value BLOB NOT NULL,
	updated_at_ms INTEGER NOT NULL
);
`

const usageRecordUpsertSQL = `
INSERT INTO usage_records (
	record_key, provider, profile, timestamp_ms, day, model, rate_model,
	session_id, project, project_path,
	uncached_input, cached_input, cache_creation, output_tokens, reasoning_tokens,
	reported_cost_usd, cost_usd, cache_savings_usd,
	cost_input, cost_cache_read, cost_cache_write, cost_output, cost_unsplit,
	cost_source, speed, tools_json, source_path
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(record_key) DO UPDATE SET
	provider = excluded.provider,
	profile = CASE WHEN excluded.profile != '' THEN excluded.profile ELSE usage_records.profile END,
	timestamp_ms = excluded.timestamp_ms,
	day = excluded.day,
	model = CASE WHEN excluded.model != '' THEN excluded.model ELSE usage_records.model END,
	rate_model = CASE WHEN excluded.rate_model != '' THEN excluded.rate_model ELSE usage_records.rate_model END,
	session_id = CASE WHEN excluded.session_id != '' THEN excluded.session_id ELSE usage_records.session_id END,
	project = CASE WHEN excluded.project != '' THEN excluded.project ELSE usage_records.project END,
	project_path = CASE WHEN excluded.project_path != '' THEN excluded.project_path ELSE usage_records.project_path END,
	uncached_input = excluded.uncached_input,
	cached_input = excluded.cached_input,
	cache_creation = excluded.cache_creation,
	output_tokens = excluded.output_tokens,
	reasoning_tokens = excluded.reasoning_tokens,
	reported_cost_usd = excluded.reported_cost_usd,
	cost_usd = excluded.cost_usd,
	cache_savings_usd = excluded.cache_savings_usd,
	cost_input = excluded.cost_input,
	cost_cache_read = excluded.cost_cache_read,
	cost_cache_write = excluded.cost_cache_write,
	cost_output = excluded.cost_output,
	cost_unsplit = excluded.cost_unsplit,
	cost_source = excluded.cost_source,
	speed = excluded.speed,
	tools_json = CASE WHEN excluded.tools_json != '' THEN excluded.tools_json ELSE usage_records.tools_json END,
	source_path = CASE WHEN excluded.source_path != '' THEN excluded.source_path ELSE usage_records.source_path END
`

// DefaultUsageDBPath resolves the persistent SQLite usage database path,
// matching convstore's XDG data directory convention:
//   $TMUX_SPACES_USAGE_DB > $XDG_DATA_HOME/tmux-spaces/usage.db > <home>/.local/share/tmux-spaces/usage.db
func DefaultUsageDBPath(homeDir string) string {
	if override := strings.TrimSpace(os.Getenv("TMUX_SPACES_USAGE_DB")); override != "" {
		return override
	}
	if strings.TrimSpace(homeDir) == "" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
			return filepath.Join(xdg, "tmux-spaces", "usage.db")
		}
	}
	base := agent.RealHomeDir(homeDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, ".local", "share", "tmux-spaces", "usage.db")
}

// Store is the persistent SQLite usage database holding normalized usage
// records, incremental file scan offsets, and cached report snapshots for
// sub-millisecond reads and fast SQL aggregation.
type Store struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
}

var _ agent.UsageFileCache = (*Store)(nil)

// OpenStore opens (or creates) the persistent SQLite usage database at dbPath.
// When dbPath is empty, DefaultUsageDBPath("") is used.
func OpenStore(dbPath string) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" {
		dbPath = DefaultUsageDBPath("")
	}
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("usage db path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	_, statErr := os.Stat(dbPath)
	existed := statErr == nil

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA user_version = 1;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	if _, err := db.Exec(usageDBSchema); err != nil {
		_ = db.Close()
		return nil, err
	}

	if !existed {
		_ = os.Chmod(dbPath, 0o600)
	}

	return &Store{path: dbPath, db: db}, nil
}

// Close closes the underlying SQLite database connection.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// ---------------------------------------------------------------------------
// agent.UsageFileCache implementation (backed by usage_files table)
// ---------------------------------------------------------------------------

// Get retrieves the cached incremental scan state for path.
func (s *Store) Get(path string) (agent.CachedFileUsage, bool) {
	if s == nil || path == "" {
		return agent.CachedFileUsage{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return agent.CachedFileUsage{}, false
	}

	var (
		entry       agent.CachedFileUsage
		guardHashS  string
		stateJSON   []byte
		recordsJSON []byte
	)
	err := s.db.QueryRow(
		`SELECT size, mtime_ns, resume_offset, guard_len, guard_hash, state_json, records_json
		 FROM usage_files WHERE path = ?`,
		path,
	).Scan(
		&entry.Size,
		&entry.MtimeNs,
		&entry.ResumeOffset,
		&entry.GuardLen,
		&guardHashS,
		&stateJSON,
		&recordsJSON,
	)
	if err != nil {
		return agent.CachedFileUsage{}, false
	}

	if guardHashS != "" {
		if h, parseErr := strconv.ParseUint(guardHashS, 10, 64); parseErr == nil {
			entry.GuardHash = h
		} else if h, hexErr := strconv.ParseUint(guardHashS, 16, 64); hexErr == nil {
			entry.GuardHash = h
		}
	}
	if len(stateJSON) > 0 {
		entry.StateJSON = stateJSON
	}
	if len(recordsJSON) > 0 {
		var recs []agent.UsageRecord
		if unmarshalErr := json.Unmarshal(recordsJSON, &recs); unmarshalErr != nil {
			return agent.CachedFileUsage{}, false
		}
		entry.Records = recs
	}
	return entry, true
}

// Put stores or updates the cached incremental scan state for path.
func (s *Store) Put(path string, entry agent.CachedFileUsage) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return
	}

	var recordsJSON []byte
	if len(entry.Records) > 0 {
		b, err := json.Marshal(entry.Records)
		if err != nil {
			return
		}
		recordsJSON = b
	}

	guardHashS := strconv.FormatUint(entry.GuardHash, 10)
	nowUnix := time.Now().Unix()

	_, _ = s.db.Exec(
		`INSERT INTO usage_files
			(path, size, mtime_ns, resume_offset, guard_len, guard_hash, state_json, records_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
			size = excluded.size,
			mtime_ns = excluded.mtime_ns,
			resume_offset = excluded.resume_offset,
			guard_len = excluded.guard_len,
			guard_hash = excluded.guard_hash,
			state_json = excluded.state_json,
			records_json = excluded.records_json,
			updated_at = excluded.updated_at`,
		path,
		entry.Size,
		entry.MtimeNs,
		entry.ResumeOffset,
		entry.GuardLen,
		guardHashS,
		entry.StateJSON,
		recordsJSON,
		nowUnix,
	)
}

// PruneFilesOlderThan deletes stale file-scan cache entries older than cutoffUnix.
// Note: usage_records are never pruned here so historical usage persists even
// after source transcripts are deleted.
func (s *Store) PruneFilesOlderThan(cutoffUnix int64) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM usage_files WHERE updated_at < ?`, cutoffUnix)
	return err
}

// ---------------------------------------------------------------------------
// Persistent usage_records upsert & SQL queries
// ---------------------------------------------------------------------------

func deterministicRecordKey(rec agent.UsageRecord) string {
	if k := strings.TrimSpace(rec.DedupeKey); k != "" {
		return k
	}
	raw := fmt.Sprintf("%s|%s|%s|%d|%s|%d|%d|%d|%d|%d|%s",
		strings.TrimSpace(rec.Provider),
		strings.TrimSpace(rec.Profile),
		strings.TrimSpace(rec.SessionID),
		rec.TimestampMs,
		strings.TrimSpace(rec.Model),
		rec.Totals.UncachedInputTokens,
		rec.Totals.CachedInputTokens,
		rec.Totals.CacheCreationTokens,
		rec.Totals.OutputTokens,
		rec.Totals.ReasoningTokens,
		strings.TrimSpace(rec.SourcePath),
	)
	return fmt.Sprintf("auto:%016x", agent.FNV1a64([]byte(raw)))
}

// UpsertRecords prices and persists normalized usage records into usage_records
// in a single SQLite transaction. Returns the number of valid records upserted.
func (s *Store) UpsertRecords(records []agent.UsageRecord, table *RateTable, loc *time.Location) (int, error) {
	if s == nil || len(records) == 0 {
		return 0, nil
	}
	if loc == nil {
		loc = time.Local
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, fmt.Errorf("usage store is closed")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(usageRecordUpsertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	seen := make(map[string]struct{}, len(records))
	written := 0
	for _, rec := range records {
		if rec.TimestampMs <= 0 {
			continue
		}
		if !rec.Totals.HasData() && rec.ReportedCostUSD == nil {
			continue
		}
		key := deterministicRecordKey(rec)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		priced := PriceRecord(rec, table)
		provider := strings.TrimSpace(rec.Provider)
		if provider == "" {
			provider = "unknown"
		}
		model := strings.TrimSpace(rec.Model)
		if model == "" {
			model = "unknown"
		}
		project := strings.TrimSpace(rec.Project)
		projectPath := strings.TrimSpace(rec.ProjectPath)
		if project == "" && projectPath != "" {
			project = agent.ProjectNameFromPath(projectPath)
		}
		speed := strings.TrimSpace(rec.Speed)
		if speed == "" {
			speed = agent.UsageSpeedStandard
		}
		day := time.UnixMilli(rec.TimestampMs).In(loc).Format("2006-01-02")

		var reportedVal any
		if rec.ReportedCostUSD != nil && !math.IsNaN(*rec.ReportedCostUSD) && !math.IsInf(*rec.ReportedCostUSD, 0) {
			reportedVal = *rec.ReportedCostUSD
		}

		toolsJSON := ""
		if len(rec.Tools) > 0 {
			if b, merr := json.Marshal(rec.Tools); merr == nil {
				toolsJSON = string(b)
			}
		}

		if _, err := stmt.Exec(
			key,
			provider,
			strings.TrimSpace(rec.Profile),
			rec.TimestampMs,
			day,
			model,
			strings.TrimSpace(rec.RateModel),
			strings.TrimSpace(rec.SessionID),
			project,
			projectPath,
			rec.Totals.UncachedInputTokens,
			rec.Totals.CachedInputTokens,
			rec.Totals.CacheCreationTokens,
			rec.Totals.OutputTokens,
			rec.Totals.ReasoningTokens,
			reportedVal,
			priced.CostUSD,
			priced.CacheSavingsUSD,
			priced.CategoryCost.Input,
			priced.CategoryCost.CacheRead,
			priced.CategoryCost.CacheWrite,
			priced.CategoryCost.Output,
			priced.CategoryCost.Unsplit,
			string(priced.CostSource),
			speed,
			toolsJSON,
			strings.TrimSpace(rec.SourcePath),
		); err != nil {
			return written, err
		}
		written++
	}

	if err := tx.Commit(); err != nil {
		return written, err
	}
	tx = nil
	return written, nil
}

// RecordCount returns the total number of persisted records in usage_records.
func (s *Store) RecordCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&count); err != nil {
		return 0
	}
	return count
}

// QueryRecentRecords returns normalized UsageRecords with timestamp_ms >= sinceMs,
// ordered ascending by timestamp_ms (uses idx_usage_records_ts).
func (s *Store) QueryRecentRecords(sinceMs int64) ([]agent.UsageRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT record_key, provider, profile, timestamp_ms, model, rate_model,
		       session_id, project, project_path,
		       uncached_input, cached_input, cache_creation, output_tokens, reasoning_tokens,
		       reported_cost_usd, speed, tools_json, source_path
		FROM usage_records
		WHERE timestamp_ms >= ?
		ORDER BY timestamp_ms ASC`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []agent.UsageRecord
	for rows.Next() {
		var (
			rec         agent.UsageRecord
			reported    sql.NullFloat64
			toolsJSON   string
		)
		if err := rows.Scan(
			&rec.DedupeKey,
			&rec.Provider,
			&rec.Profile,
			&rec.TimestampMs,
			&rec.Model,
			&rec.RateModel,
			&rec.SessionID,
			&rec.Project,
			&rec.ProjectPath,
			&rec.Totals.UncachedInputTokens,
			&rec.Totals.CachedInputTokens,
			&rec.Totals.CacheCreationTokens,
			&rec.Totals.OutputTokens,
			&rec.Totals.ReasoningTokens,
			&reported,
			&rec.Speed,
			&toolsJSON,
			&rec.SourcePath,
		); err != nil {
			return nil, err
		}
		if reported.Valid {
			v := reported.Float64
			rec.ReportedCostUSD = &v
		}
		if toolsJSON != "" {
			var tools []string
			if json.Unmarshal([]byte(toolsJSON), &tools) == nil {
				rec.Tools = tools
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// QueryDaily queries daily usage rollups directly from usage_records via SQL,
// optionally filtered by [sinceDay, untilDay] and provider.
func (s *Store) QueryDaily(sinceDay, untilDay, provider string) ([]DailyBucket, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, nil
	}

	var conds []string
	var args []any
	if strings.TrimSpace(sinceDay) != "" {
		conds = append(conds, "day >= ?")
		args = append(args, strings.TrimSpace(sinceDay))
	}
	if strings.TrimSpace(untilDay) != "" {
		conds = append(conds, "day <= ?")
		args = append(args, strings.TrimSpace(untilDay))
	}
	if strings.TrimSpace(provider) != "" {
		conds = append(conds, "provider = ?")
		args = append(args, strings.TrimSpace(provider))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows, err := s.db.Query(`
		SELECT day,
		       SUM(uncached_input), SUM(cached_input), SUM(cache_creation),
		       SUM(output_tokens), SUM(reasoning_tokens),
		       SUM(cost_usd), SUM(cache_savings_usd),
		       SUM(cost_input), SUM(cost_cache_read), SUM(cost_cache_write), SUM(cost_output), SUM(cost_unsplit),
		       COUNT(*),
		       SUM(CASE WHEN cost_source = 'unpriced' THEN 1 ELSE 0 END),
		       COUNT(DISTINCT CASE WHEN session_id != '' THEN provider || ':' || session_id END)
		FROM usage_records
		`+where+`
		GROUP BY day
		ORDER BY day ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDay := make(map[string]int)
	var out []DailyBucket
	for rows.Next() {
		var b DailyBucket
		if err := rows.Scan(
			&b.Date,
			&b.Totals.UncachedInputTokens,
			&b.Totals.CachedInputTokens,
			&b.Totals.CacheCreationTokens,
			&b.Totals.OutputTokens,
			&b.Totals.ReasoningTokens,
			&b.CostUSD,
			&b.CacheSavingsUSD,
			&b.CategoryCost.Input,
			&b.CategoryCost.CacheRead,
			&b.CategoryCost.CacheWrite,
			&b.CategoryCost.Output,
			&b.CategoryCost.Unsplit,
			&b.Records,
			&b.UnpricedRecords,
			&b.Sessions,
		); err != nil {
			return nil, err
		}
		b.CostUSD = roundUSD(b.CostUSD)
		b.CacheSavingsUSD = roundUSD(b.CacheSavingsUSD)
		b.CategoryCost = roundCategoryCost(b.CategoryCost)
		b.ByProvider = make(map[string]SliceMetric)
		b.ByModel = make(map[string]SliceMetric)
		b.ByProject = make(map[string]SliceMetric)
		byDay[b.Date] = len(out)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	// Populate ByProvider, ByModel, ByProject slices via indexed GROUP BY queries.
	fillSlice := func(col string, assign func(idx int, key string, m SliceMetric)) error {
		q := fmt.Sprintf(`
			SELECT day, %s,
			       SUM(uncached_input + cached_input + cache_creation + output_tokens),
			       SUM(cost_usd),
			       COUNT(*)
			FROM usage_records
			%s
			GROUP BY day, %s`, col, where, col)
		r, qerr := s.db.Query(q, args...)
		if qerr != nil {
			return qerr
		}
		defer r.Close()
		for r.Next() {
			var day, key string
			var tokens int64
			var cost float64
			var count int
			if serr := r.Scan(&day, &key, &tokens, &cost, &count); serr != nil {
				return serr
			}
			if key == "" {
				continue
			}
			if idx, ok := byDay[day]; ok {
				assign(idx, key, SliceMetric{
					Key:     key,
					Tokens:  tokens,
					CostUSD: roundUSD(cost),
					Records: count,
				})
			}
		}
		return r.Err()
	}

	if err := fillSlice("provider", func(idx int, key string, m SliceMetric) { out[idx].ByProvider[key] = m }); err != nil {
		return nil, err
	}
	if err := fillSlice("model", func(idx int, key string, m SliceMetric) { out[idx].ByModel[key] = m }); err != nil {
		return nil, err
	}
	if err := fillSlice("project", func(idx int, key string, m SliceMetric) { out[idx].ByProject[key] = m }); err != nil {
		return nil, err
	}

	return out, nil
}

// BuildReport constructs a complete usage Report directly from SQLite indexed
// queries without re-parsing transcripts or re-pricing historical records.
func (s *Store) BuildReport(now time.Time, loc *time.Location) (Report, error) {
	if s == nil {
		return Report{}, nil
	}
	if loc == nil {
		loc = time.Local
	}

	daily, err := s.QueryDaily("", "", "")
	if err != nil {
		return Report{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return Report{}, fmt.Errorf("usage store is closed")
	}

	rep := Report{
		GeneratedAtMs: now.UnixMilli(),
		Daily:         daily,
	}

	// Grand totals + cost source counts.
	var (
		reportedCount int
		distinctSess  int
	)
	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(uncached_input), 0),
		       COALESCE(SUM(cached_input), 0),
		       COALESCE(SUM(cache_creation), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(reasoning_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(cache_savings_usd), 0),
		       COALESCE(SUM(cost_input), 0),
		       COALESCE(SUM(cost_cache_read), 0),
		       COALESCE(SUM(cost_cache_write), 0),
		       COALESCE(SUM(cost_output), 0),
		       COALESCE(SUM(cost_unsplit), 0),
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN cost_source = 'unpriced' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cost_source = 'providerReported' THEN 1 ELSE 0 END), 0),
		       COUNT(DISTINCT CASE WHEN session_id != '' THEN provider || ':' || session_id END)
		FROM usage_records`).Scan(
		&rep.Totals.UncachedInputTokens,
		&rep.Totals.CachedInputTokens,
		&rep.Totals.CacheCreationTokens,
		&rep.Totals.OutputTokens,
		&rep.Totals.ReasoningTokens,
		&rep.CostUSD,
		&rep.CacheSavingsUSD,
		&rep.CategoryCost.Input,
		&rep.CategoryCost.CacheRead,
		&rep.CategoryCost.CacheWrite,
		&rep.CategoryCost.Output,
		&rep.CategoryCost.Unsplit,
		&rep.Records,
		&rep.UnpricedRecords,
		&reportedCount,
		&distinctSess,
	)
	if err != nil {
		return Report{}, err
	}
	rep.CostUSD = roundUSD(rep.CostUSD)
	rep.CacheSavingsUSD = roundUSD(rep.CacheSavingsUSD)
	rep.CategoryCost = roundCategoryCost(rep.CategoryCost)
	rep.Sessions = distinctSess
	rep.CostSource = aggregateCostSource(rep.Records, rep.UnpricedRecords, reportedCount)
	if inTot := rep.Totals.InputTotal(); inTot > 0 {
		rep.CacheHitRate = float64(rep.Totals.CachedInputTokens) / float64(inTot)
	}
	if pricedTokens := pricedTokenEstimate(rep.Totals.Total(), rep.Records, rep.UnpricedRecords); pricedTokens > 0 && rep.CostUSD > 0 {
		rep.CostPerMTokens = (rep.CostUSD / float64(pricedTokens)) * 1_000_000.0
	}

	// Streaks & peak day from daily buckets.
	rep.ActiveDays = len(daily)
	for _, d := range daily {
		toks := d.Totals.Total()
		if toks > rep.PeakDayTokens || (toks == rep.PeakDayTokens && d.CostUSD > rep.PeakDayCostUSD) {
			rep.PeakDayDate = d.Date
			rep.PeakDayTokens = toks
			rep.PeakDayCostUSD = d.CostUSD
		}
	}
	rep.CurrentStreak, rep.LongestStreak = computeStreaks(daily, now.In(loc))

	// Hourly buckets (last 24h).
	hourNow := now.In(loc).Truncate(time.Hour)
	hourStart := hourNow.Add(-23 * time.Hour)
	hourly := make([]HourlyBucket, 24)
	for i := 0; i < 24; i++ {
		t := hourStart.Add(time.Duration(i) * time.Hour)
		hourly[i] = HourlyBucket{
			HourStartMs: t.UnixMilli(),
			Label:       t.Format("15:00"),
		}
	}
	hRows, err := s.db.Query(`
		SELECT timestamp_ms, uncached_input, cached_input, cache_creation, output_tokens, reasoning_tokens, cost_usd
		FROM usage_records
		WHERE timestamp_ms >= ?`, hourStart.UnixMilli())
	if err != nil {
		return Report{}, err
	}
	for hRows.Next() {
		var tsMs, u, c, cw, o, r int64
		var cost float64
		if serr := hRows.Scan(&tsMs, &u, &c, &cw, &o, &r, &cost); serr != nil {
			_ = hRows.Close()
			return Report{}, serr
		}
		idx := int((tsMs - hourStart.UnixMilli()) / int64(time.Hour/time.Millisecond))
		if idx >= 0 && idx < 24 {
			hourly[idx].Totals.UncachedInputTokens += u
			hourly[idx].Totals.CachedInputTokens += c
			hourly[idx].Totals.CacheCreationTokens += cw
			hourly[idx].Totals.OutputTokens += o
			hourly[idx].Totals.ReasoningTokens += r
			hourly[idx].CostUSD += cost
			hourly[idx].Records++
		}
	}
	_ = hRows.Close()
	for i := range hourly {
		hourly[i].CostUSD = roundUSD(hourly[i].CostUSD)
	}
	rep.Hourly = hourly

	// Providers summary.
	grandTokens := rep.Totals.Total()
	pRows, err := s.db.Query(`
		SELECT provider,
		       SUM(uncached_input), SUM(cached_input), SUM(cache_creation),
		       SUM(output_tokens), SUM(reasoning_tokens),
		       SUM(cost_usd), SUM(cache_savings_usd),
		       SUM(cost_input), SUM(cost_cache_read), SUM(cost_cache_write), SUM(cost_output), SUM(cost_unsplit),
		       COUNT(*),
		       SUM(CASE WHEN cost_source = 'unpriced' THEN 1 ELSE 0 END),
		       COUNT(DISTINCT CASE WHEN session_id != '' THEN session_id END)
		FROM usage_records
		GROUP BY provider`)
	if err != nil {
		return Report{}, err
	}
	provIdx := make(map[string]int)
	for pRows.Next() {
		var ps ProviderSummary
		if serr := pRows.Scan(
			&ps.Provider,
			&ps.Totals.UncachedInputTokens, &ps.Totals.CachedInputTokens, &ps.Totals.CacheCreationTokens,
			&ps.Totals.OutputTokens, &ps.Totals.ReasoningTokens,
			&ps.CostUSD, &ps.CacheSavingsUSD,
			&ps.CategoryCost.Input, &ps.CategoryCost.CacheRead, &ps.CategoryCost.CacheWrite,
			&ps.CategoryCost.Output, &ps.CategoryCost.Unsplit,
			&ps.Records, &ps.UnpricedRecords, &ps.Sessions,
		); serr != nil {
			_ = pRows.Close()
			return Report{}, serr
		}
		ps.CostUSD = roundUSD(ps.CostUSD)
		ps.CacheSavingsUSD = roundUSD(ps.CacheSavingsUSD)
		ps.CategoryCost = roundCategoryCost(ps.CategoryCost)
		if rep.CostUSD > 0 {
			ps.CostShare = ps.CostUSD / rep.CostUSD
		}
		if grandTokens > 0 {
			ps.TokenShare = float64(ps.Totals.Total()) / float64(grandTokens)
		}
		if inTot := ps.Totals.InputTotal(); inTot > 0 {
			ps.CacheHitRate = float64(ps.Totals.CachedInputTokens) / float64(inTot)
		}
		provIdx[ps.Provider] = len(rep.Providers)
		rep.Providers = append(rep.Providers, ps)
	}
	_ = pRows.Close()

	// Populate distinct models per provider.
	pmRows, err := s.db.Query(`SELECT DISTINCT provider, model FROM usage_records WHERE model != '' ORDER BY provider, model`)
	if err == nil {
		for pmRows.Next() {
			var prov, mod string
			if pmRows.Scan(&prov, &mod) == nil {
				if idx, ok := provIdx[prov]; ok {
					rep.Providers[idx].Models = append(rep.Providers[idx].Models, mod)
				}
			}
		}
		_ = pmRows.Close()
	}
	sort.Slice(rep.Providers, func(i, j int) bool {
		if rep.Providers[i].CostUSD != rep.Providers[j].CostUSD {
			return rep.Providers[i].CostUSD > rep.Providers[j].CostUSD
		}
		if rep.Providers[i].Totals.Total() != rep.Providers[j].Totals.Total() {
			return rep.Providers[i].Totals.Total() > rep.Providers[j].Totals.Total()
		}
		return rep.Providers[i].Provider < rep.Providers[j].Provider
	})

	// Models summary.
	mRows, err := s.db.Query(`
		SELECT model, provider,
		       SUM(uncached_input), SUM(cached_input), SUM(cache_creation),
		       SUM(output_tokens), SUM(reasoning_tokens),
		       SUM(cost_usd), SUM(cache_savings_usd),
		       SUM(cost_input), SUM(cost_cache_read), SUM(cost_cache_write), SUM(cost_output), SUM(cost_unsplit),
		       COUNT(*),
		       SUM(CASE WHEN cost_source = 'unpriced' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN cost_source = 'providerReported' THEN 1 ELSE 0 END),
		       COUNT(DISTINCT CASE WHEN session_id != '' THEN provider || ':' || session_id END)
		FROM usage_records
		GROUP BY model, provider`)
	if err != nil {
		return Report{}, err
	}
	for mRows.Next() {
		var ms ModelSummary
		var mReported int
		if serr := mRows.Scan(
			&ms.Model, &ms.Provider,
			&ms.Totals.UncachedInputTokens, &ms.Totals.CachedInputTokens, &ms.Totals.CacheCreationTokens,
			&ms.Totals.OutputTokens, &ms.Totals.ReasoningTokens,
			&ms.CostUSD, &ms.CacheSavingsUSD,
			&ms.CategoryCost.Input, &ms.CategoryCost.CacheRead, &ms.CategoryCost.CacheWrite,
			&ms.CategoryCost.Output, &ms.CategoryCost.Unsplit,
			&ms.Records, &ms.UnpricedRecords, &mReported, &ms.Sessions,
		); serr != nil {
			_ = mRows.Close()
			return Report{}, serr
		}
		ms.CostUSD = roundUSD(ms.CostUSD)
		ms.CacheSavingsUSD = roundUSD(ms.CacheSavingsUSD)
		ms.CategoryCost = roundCategoryCost(ms.CategoryCost)
		ms.CostSource = aggregateCostSource(ms.Records, ms.UnpricedRecords, mReported)
		if rep.CostUSD > 0 {
			ms.CostShare = ms.CostUSD / rep.CostUSD
		}
		if grandTokens > 0 {
			ms.TokenShare = float64(ms.Totals.Total()) / float64(grandTokens)
		}
		if inTot := ms.Totals.InputTotal(); inTot > 0 {
			ms.CacheHitRate = float64(ms.Totals.CachedInputTokens) / float64(inTot)
		}
		if pt := pricedTokenEstimate(ms.Totals.Total(), ms.Records, ms.UnpricedRecords); pt > 0 && ms.CostUSD > 0 {
			ms.CostPerMTokens = (ms.CostUSD / float64(pt)) * 1_000_000.0
		}
		rep.Models = append(rep.Models, ms)
	}
	_ = mRows.Close()
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].CostUSD != rep.Models[j].CostUSD {
			return rep.Models[i].CostUSD > rep.Models[j].CostUSD
		}
		if rep.Models[i].Totals.Total() != rep.Models[j].Totals.Total() {
			return rep.Models[i].Totals.Total() > rep.Models[j].Totals.Total()
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})

	// Projects summary.
	projRows, err := s.db.Query(`
		SELECT project, MAX(project_path),
		       SUM(uncached_input), SUM(cached_input), SUM(cache_creation),
		       SUM(output_tokens), SUM(reasoning_tokens),
		       SUM(cost_usd),
		       COUNT(*),
		       COUNT(DISTINCT CASE WHEN session_id != '' THEN provider || ':' || session_id END)
		FROM usage_records
		WHERE project != ''
		GROUP BY project`)
	if err != nil {
		return Report{}, err
	}
	projIdx := make(map[string]int)
	for projRows.Next() {
		var pr ProjectSummary
		if serr := projRows.Scan(
			&pr.Project, &pr.Path,
			&pr.Totals.UncachedInputTokens, &pr.Totals.CachedInputTokens, &pr.Totals.CacheCreationTokens,
			&pr.Totals.OutputTokens, &pr.Totals.ReasoningTokens,
			&pr.CostUSD, &pr.Records, &pr.Sessions,
		); serr != nil {
			_ = projRows.Close()
			return Report{}, serr
		}
		pr.CostUSD = roundUSD(pr.CostUSD)
		projIdx[pr.Project] = len(rep.Projects)
		rep.Projects = append(rep.Projects, pr)
	}
	_ = projRows.Close()
	if ppRows, perr := s.db.Query(`SELECT DISTINCT project, provider FROM usage_records WHERE project != '' ORDER BY project, provider`); perr == nil {
		for ppRows.Next() {
			var proj, prov string
			if ppRows.Scan(&proj, &prov) == nil {
				if idx, ok := projIdx[proj]; ok {
					rep.Projects[idx].Providers = append(rep.Projects[idx].Providers, prov)
				}
			}
		}
		_ = ppRows.Close()
	}
	sort.Slice(rep.Projects, func(i, j int) bool {
		if rep.Projects[i].CostUSD != rep.Projects[j].CostUSD {
			return rep.Projects[i].CostUSD > rep.Projects[j].CostUSD
		}
		if rep.Projects[i].Totals.Total() != rep.Projects[j].Totals.Total() {
			return rep.Projects[i].Totals.Total() > rep.Projects[j].Totals.Total()
		}
		return rep.Projects[i].Project < rep.Projects[j].Project
	})

	// Sessions summary.
	sRows, err := s.db.Query(`
		SELECT session_id, provider, MAX(profile), MAX(project), MAX(project_path),
		       SUM(uncached_input), SUM(cached_input), SUM(cache_creation),
		       SUM(output_tokens), SUM(reasoning_tokens),
		       SUM(cost_usd),
		       COUNT(*),
		       SUM(CASE WHEN cost_source = 'unpriced' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN cost_source = 'providerReported' THEN 1 ELSE 0 END),
		       MIN(timestamp_ms), MAX(timestamp_ms)
		FROM usage_records
		WHERE session_id != ''
		GROUP BY session_id, provider
		ORDER BY MAX(timestamp_ms) DESC`)
	if err != nil {
		return Report{}, err
	}
	sessIdx := make(map[string]int)
	for sRows.Next() {
		var ss SessionSummary
		var sUnpriced, sReported int
		if serr := sRows.Scan(
			&ss.SessionID, &ss.Provider, &ss.Profile, &ss.Project, &ss.ProjectPath,
			&ss.Totals.UncachedInputTokens, &ss.Totals.CachedInputTokens, &ss.Totals.CacheCreationTokens,
			&ss.Totals.OutputTokens, &ss.Totals.ReasoningTokens,
			&ss.CostUSD, &ss.Records, &sUnpriced, &sReported,
			&ss.StartedAtMs, &ss.UpdatedAtMs,
		); serr != nil {
			_ = sRows.Close()
			return Report{}, serr
		}
		ss.CostUSD = roundUSD(ss.CostUSD)
		ss.CostSource = aggregateCostSource(ss.Records, sUnpriced, sReported)
		if ss.UpdatedAtMs > ss.StartedAtMs {
			ss.DurationMs = ss.UpdatedAtMs - ss.StartedAtMs
		}
		sessIdx[ss.Provider+"\x00"+ss.SessionID] = len(rep.SessionsList)
		rep.SessionsList = append(rep.SessionsList, ss)
	}
	_ = sRows.Close()
	if smRows, serr := s.db.Query(`SELECT DISTINCT provider, session_id, model FROM usage_records WHERE session_id != '' AND model != '' ORDER BY model`); serr == nil {
		for smRows.Next() {
			var prov, sid, mod string
			if smRows.Scan(&prov, &sid, &mod) == nil {
				if idx, ok := sessIdx[prov+"\x00"+sid]; ok {
					rep.SessionsList[idx].Models = append(rep.SessionsList[idx].Models, mod)
				}
			}
		}
		_ = smRows.Close()
	}

	// Tools summary (top 25).
	toolCounts := make(map[string]int)
	if tRows, terr := s.db.Query(`SELECT tools_json FROM usage_records WHERE tools_json != ''`); terr == nil {
		for tRows.Next() {
			var raw string
			if tRows.Scan(&raw) == nil && raw != "" {
				var tools []string
				if json.Unmarshal([]byte(raw), &tools) == nil {
					for _, t := range tools {
						t = strings.TrimSpace(t)
						if t != "" {
							toolCounts[t]++
						}
					}
				}
			}
		}
		_ = tRows.Close()
	}
	if len(toolCounts) > 0 {
		tools := make([]ToolSummary, 0, len(toolCounts))
		for name, count := range toolCounts {
			tools = append(tools, ToolSummary{Name: name, Count: count})
		}
		sort.Slice(tools, func(i, j int) bool {
			if tools[i].Count != tools[j].Count {
				return tools[i].Count > tools[j].Count
			}
			return tools[i].Name < tools[j].Name
		})
		if len(tools) > 25 {
			tools = tools[:25]
		}
		rep.Tools = tools
	}

	return rep, nil
}

// ---------------------------------------------------------------------------
// Persisted Report snapshot for instant popup open
// ---------------------------------------------------------------------------

const latestSnapshotMetaKey = "latest_report_v1"

// SaveSnapshot persists report into usage_meta so subsequent popup opens load
// immediately from SQLite before background refresh completes.
func (s *Store) SaveSnapshot(report Report) error {
	if s == nil {
		return nil
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	_, err = s.db.Exec(`
		INSERT INTO usage_meta (key, value, updated_at_ms)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at_ms = excluded.updated_at_ms`,
		latestSnapshotMetaKey, data, time.Now().UnixMilli(),
	)
	return err
}

// LoadSnapshot loads the last persisted Report snapshot from usage_meta.
func (s *Store) LoadSnapshot() (Report, bool) {
	if s == nil {
		return Report{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return Report{}, false
	}
	var raw []byte
	if err := s.db.QueryRow(`SELECT value FROM usage_meta WHERE key = ?`, latestSnapshotMetaKey).Scan(&raw); err != nil || len(raw) == 0 {
		return Report{}, false
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return Report{}, false
	}
	return rep, true
}

func roundUSD(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

func roundCategoryCost(c CategoryCost) CategoryCost {
	return CategoryCost{
		Input:      roundUSD(c.Input),
		CacheRead:  roundUSD(c.CacheRead),
		CacheWrite: roundUSD(c.CacheWrite),
		Output:     roundUSD(c.Output),
		Unsplit:    roundUSD(c.Unsplit),
	}
}

func aggregateCostSource(total, unpriced, reported int) CostSource {
	if total <= 0 || unpriced >= total {
		return CostSourceUnpriced
	}
	if reported > 0 && reported == total-unpriced && unpriced == 0 {
		return CostSourceProviderReported
	}
	return CostSourceModelPriced
}

func pricedTokenEstimate(totalTokens int64, totalRecords, unpricedRecords int) int64 {
	if totalTokens <= 0 || totalRecords <= 0 || unpricedRecords >= totalRecords {
		return 0
	}
	if unpricedRecords <= 0 {
		return totalTokens
	}
	unpricedTokens := int64(math.Round(float64(totalTokens) * float64(unpricedRecords) / float64(totalRecords)))
	if unpricedTokens >= totalTokens {
		return 0
	}
	return totalTokens - unpricedTokens
}

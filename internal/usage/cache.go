package usage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	// DefaultCacheRetentionDays is the default retention window for cached file scan entries.
	DefaultCacheRetentionDays = 92
)

// DefaultScanCachePath returns the default SQLite scan cache path under homeDir.
func DefaultScanCachePath(homeDir string) string {
	base := agent.RealHomeDir(homeDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, ".cache", "tmux-spaces", "usage-scan-cache.db")
}

// SQLiteFileCache implements agent.UsageFileCache backed by modernc.org/sqlite.
// All methods are thread-safe and degrade gracefully to no-ops if the database
// cannot be opened or encounters I/O errors.
type SQLiteFileCache struct {
	mu sync.Mutex
	db *sql.DB
}

var _ agent.UsageFileCache = (*SQLiteFileCache)(nil)

// OpenSQLiteFileCache opens (or creates) the SQLite file usage cache at cachePath.
// If cachePath is empty, DefaultScanCachePath("") is used. If the database cannot
// be opened or initialized, a non-nil no-op cache is returned so usage scans
// never fail due to cache I/O.
func OpenSQLiteFileCache(cachePath string) *SQLiteFileCache {
	if cachePath == "" {
		cachePath = DefaultScanCachePath("")
	}
	if cachePath == "" {
		return &SQLiteFileCache{}
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return &SQLiteFileCache{}
	}

	db, err := sql.Open("sqlite", cachePath)
	if err != nil {
		return &SQLiteFileCache{}
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA user_version = 1;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return &SQLiteFileCache{}
		}
	}

	const schema = `
CREATE TABLE IF NOT EXISTS file_usage_cache (
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
CREATE INDEX IF NOT EXISTS idx_file_usage_cache_updated_at ON file_usage_cache(updated_at);
`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return &SQLiteFileCache{}
	}

	return &SQLiteFileCache{db: db}
}

// Get retrieves the cached scan state for path.
func (c *SQLiteFileCache) Get(path string) (agent.CachedFileUsage, bool) {
	if c == nil {
		return agent.CachedFileUsage{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil || path == "" {
		return agent.CachedFileUsage{}, false
	}

	var (
		entry       agent.CachedFileUsage
		guardHashS  string
		stateJSON   []byte
		recordsJSON []byte
	)
	err := c.db.QueryRow(
		`SELECT size, mtime_ns, resume_offset, guard_len, guard_hash, state_json, records_json
		 FROM file_usage_cache WHERE path = ?`,
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

// Put stores or replaces the cached scan state for path.
func (c *SQLiteFileCache) Put(path string, entry agent.CachedFileUsage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil || path == "" {
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

	_, _ = c.db.Exec(
		`INSERT INTO file_usage_cache
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

// PruneOlderThan removes cache entries whose updated_at timestamp is strictly
// older than cutoffUnix (unix seconds).
func (c *SQLiteFileCache) PruneOlderThan(cutoffUnix int64) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	_, err := c.db.Exec(`DELETE FROM file_usage_cache WHERE updated_at < ?`, cutoffUnix)
	return err
}

// Close closes the underlying SQLite database connection.
func (c *SQLiteFileCache) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}

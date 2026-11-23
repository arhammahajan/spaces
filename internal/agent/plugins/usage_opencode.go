package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "modernc.org/sqlite"
)

var openCodeDBNamePattern = regexp.MustCompile(`^opencode(?:-[a-zA-Z0-9_-]+)?\.db$`)

type openCodeUsageReader struct{}

// NewOpenCodeUsageReader returns an agent.UsageReader for OpenCode SQLite
// databases and legacy JSON message storage.
func NewOpenCodeUsageReader() agent.UsageReader {
	return openCodeUsageReader{}
}

func (openCodeUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := resolveOpenCodeUsageDirs(home, opts.IsRealHome)
	seenDedupe := make(map[string]struct{})
	var all []agent.UsageRecord

	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, statErr := os.Stat(root)
		if statErr != nil || !info.IsDir() {
			continue
		}

		// 1. Scan opencode*.db SQLite databases (opencode.db first).
		entries, readErr := os.ReadDir(root)
		if readErr == nil {
			var dbNames []string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if openCodeDBNamePattern.MatchString(e.Name()) {
					dbNames = append(dbNames, e.Name())
				}
			}
			sort.Slice(dbNames, func(i, j int) bool {
				if dbNames[i] == "opencode.db" {
					return true
				}
				if dbNames[j] == "opencode.db" {
					return false
				}
				return dbNames[i] < dbNames[j]
			})
			for _, name := range dbNames {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				dbPath := filepath.Join(root, name)
				recs := scanOpenCodeSQLiteUsage(ctx, dbPath, opts, seenDedupe)
				all = append(all, recs...)
			}
		}

		// 2. Scan legacy <root>/storage/message/**/*.json files.
		legacyRoot := filepath.Join(root, "storage", "message")
		if linfo, lerr := os.Stat(legacyRoot); lerr == nil && linfo.IsDir() {
			walkErr := filepath.WalkDir(legacyRoot, func(path string, d fs.DirEntry, err error) error {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if err != nil || d.IsDir() {
					return nil
				}
				if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
					return nil
				}
				id := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
				if id != "" {
					if _, exists := seenDedupe["opencode:"+id]; exists {
						return nil
					}
				}
				if opts.SinceMs > 0 {
					if fi, ferr := d.Info(); ferr == nil && fi.ModTime().UnixMilli() < opts.SinceMs {
						return nil
					}
				}
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					return nil
				}
				rec, ok := parseOpenCodeUsageMessage(data, openCodeMessageFallback{
					id: id,
				})
				if !ok {
					return nil
				}
				all = append(all, finalizeUsageRecords([]agent.UsageRecord{rec}, opts, path, seenDedupe)...)
				return nil
			})
			if walkErr != nil {
				return nil, walkErr
			}
		}
	}

	return all, nil
}

func resolveOpenCodeUsageDirs(home string, isRealHome bool) []string {
	var dirs []string
	if isRealHome {
		if raw := strings.TrimSpace(os.Getenv("OPENCODE_DATA_DIR")); raw != "" {
			for _, part := range strings.Split(raw, ",") {
				if p := expandUsageHomePath(part, home); p != "" {
					dirs = append(dirs, p)
				}
			}
		}
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" && filepath.IsAbs(xdg) {
			dirs = append(dirs, filepath.Join(xdg, "opencode"))
		}
	}
	dirs = append(dirs, filepath.Join(home, ".local", "share", "opencode"))
	return dedupeUsageDirs(dirs)
}

func openOpenCodeUsageSQLite(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, os.ErrNotExist
	}
	slashed := filepath.ToSlash(path)
	escaped := strings.ReplaceAll(strings.ReplaceAll(slashed, "?", "%3F"), "#", "%23")
	uri := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(100)&_pragma=query_only(1)", escaped)
	return sql.Open("sqlite", uri)
}

func scanOpenCodeSQLiteUsage(
	ctx context.Context,
	dbPath string,
	opts agent.UsageScanOptions,
	seenDedupe map[string]struct{},
) []agent.UsageRecord {
	db, err := openOpenCodeUsageSQLite(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	tables := listSQLiteTables(ctx, db)
	if len(tables) == 0 {
		return nil
	}

	sessionDirs := loadOpenCodeSessionDirs(ctx, db, tables)
	var out []agent.UsageRecord

	for _, table := range []string{"message", "session_message"} {
		if !tables[table] {
			continue
		}
		cols := listSQLiteColumns(ctx, db, table)
		if !cols["data"] {
			continue
		}

		idExpr := "''"
		if cols["id"] {
			idExpr = "id"
		}
		sessionExpr := "''"
		if cols["session_id"] {
			sessionExpr = "session_id"
		}
		createdExpr := "NULL"
		if cols["time_created"] {
			createdExpr = "time_created"
		}

		var where []string
		var args []any
		if table == "session_message" && cols["type"] {
			where = append(where, "type = 'assistant'")
		}
		if cols["time_created"] && opts.SinceMs > 0 {
			where = append(where, "time_created >= ?")
			args = append(args, opts.SinceMs)
		}

		query := fmt.Sprintf("SELECT %s, %s, data, %s FROM %s", idExpr, sessionExpr, createdExpr, table)
		if len(where) > 0 {
			query += " WHERE " + strings.Join(where, " AND ")
		}

		rows, qerr := db.QueryContext(ctx, query, args...)
		if qerr != nil {
			continue
		}
		for rows.Next() {
			if ctx.Err() != nil {
				break
			}
			var (
				idNull      sql.NullString
				sessionNull sql.NullString
				dataNull    sql.NullString
				createdRaw  any
			)
			if serr := rows.Scan(&idNull, &sessionNull, &dataNull, &createdRaw); serr != nil {
				continue
			}
			var fallbackTs int64
			if ts, ok := parseUsageTimestampMs(createdRaw); ok {
				fallbackTs = ts
			}
			sid := strings.TrimSpace(sessionNull.String)
			rec, ok := parseOpenCodeUsageMessage([]byte(dataNull.String), openCodeMessageFallback{
				id:          strings.TrimSpace(idNull.String),
				sessionID:   sid,
				timestampMs: fallbackTs,
				directory:   sessionDirs[sid],
			})
			if !ok {
				continue
			}
			out = append(out, finalizeUsageRecords([]agent.UsageRecord{rec}, opts, dbPath, seenDedupe)...)
		}
		rows.Close()
	}

	return out
}

func listSQLiteTables(ctx context.Context, db *sql.DB) map[string]bool {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			tables[name] = true
		}
	}
	return tables
}

func listSQLiteColumns(ctx context.Context, db *sql.DB, table string) map[string]bool {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid      int
			name     string
			colType  string
			notNull  int
			dflt     any
			pk       int
		)
		if rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk) == nil {
			cols[name] = true
		}
	}
	return cols
}

func loadOpenCodeSessionDirs(ctx context.Context, db *sql.DB, tables map[string]bool) map[string]string {
	dirs := make(map[string]string)
	for _, table := range []string{"session", "session_v2"} {
		if !tables[table] {
			continue
		}
		cols := listSQLiteColumns(ctx, db, table)
		if !cols["id"] {
			continue
		}
		dirCol := ""
		switch {
		case cols["directory"]:
			dirCol = "directory"
		case cols["cwd"]:
			dirCol = "cwd"
		case cols["project_dir"]:
			dirCol = "project_dir"
		default:
			continue
		}
		rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT id, %s FROM %s", dirCol, table))
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, dir sql.NullString
			if rows.Scan(&id, &dir) == nil {
				sid := strings.TrimSpace(id.String)
				d := strings.TrimSpace(dir.String)
				if sid != "" && d != "" {
					if _, exists := dirs[sid]; !exists {
						dirs[sid] = d
					}
				}
			}
		}
		rows.Close()
	}
	return dirs
}

type openCodeMessageFallback struct {
	id          string
	sessionID   string
	timestampMs int64
	directory   string
}

type openCodeMessageJSON struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	ModelID   string `json:"modelID"`
	Model     *struct {
		ID      string `json:"id"`
		ModelID string `json:"modelID"`
	} `json:"model"`
	Time *struct {
		Created any `json:"created"`
	} `json:"time"`
	Tokens *struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		Reasoning float64 `json:"reasoning"`
		Cache     *struct {
			Read  float64 `json:"read"`
			Write float64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Cost *float64 `json:"cost"`
	Path *struct {
		CWD  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	CWD       string `json:"cwd"`
	Directory string `json:"directory"`
}

func parseOpenCodeUsageMessage(raw []byte, fb openCodeMessageFallback) (agent.UsageRecord, bool) {
	var msg openCodeMessageJSON
	if err := json.Unmarshal(raw, &msg); err != nil {
		return agent.UsageRecord{}, false
	}
	if msg.Role != "" && msg.Role != "assistant" {
		return agent.UsageRecord{}, false
	}
	if msg.Tokens == nil {
		return agent.UsageRecord{}, false
	}

	var model string
	if msg.Model != nil {
		model = strings.TrimSpace(msg.Model.ID)
		if model == "" {
			model = strings.TrimSpace(msg.Model.ModelID)
		}
	}
	if model == "" {
		model = strings.TrimSpace(msg.ModelID)
	}
	if model == "" {
		return agent.UsageRecord{}, false
	}

	var tsMs int64
	if msg.Time != nil {
		if parsed, ok := parseUsageTimestampMs(msg.Time.Created); ok {
			tsMs = parsed
		}
	}
	if tsMs == 0 {
		tsMs = fb.timestampMs
	}
	if tsMs <= 0 {
		return agent.UsageRecord{}, false
	}

	var cacheRead, cacheWrite int64
	if msg.Tokens.Cache != nil {
		cacheRead = usageTokenInt64(msg.Tokens.Cache.Read)
		cacheWrite = usageTokenInt64(msg.Tokens.Cache.Write)
	}
	reasoning := usageTokenInt64(msg.Tokens.Reasoning)
	output := usageTokenInt64(msg.Tokens.Output) + reasoning

	totals := agent.UsageTokenTotals{
		UncachedInputTokens: usageTokenInt64(msg.Tokens.Input),
		CachedInputTokens:   cacheRead,
		CacheCreationTokens: cacheWrite,
		OutputTokens:        output,
		ReasoningTokens:     reasoning,
	}
	if !totals.HasData() {
		return agent.UsageRecord{}, false
	}

	id := fb.id
	if id == "" {
		id = strings.TrimSpace(msg.ID)
	}
	var dedupeKey string
	if id != "" {
		dedupeKey = "opencode:" + id
	}

	sessionID := fb.sessionID
	if sessionID == "" {
		sessionID = strings.TrimSpace(msg.SessionID)
	}

	dir := fb.directory
	if dir == "" {
		dir = strings.TrimSpace(msg.Directory)
	}
	if dir == "" {
		dir = strings.TrimSpace(msg.CWD)
	}
	if dir == "" && msg.Path != nil {
		dir = strings.TrimSpace(msg.Path.CWD)
		if dir == "" {
			dir = strings.TrimSpace(msg.Path.Root)
		}
	}

	return agent.UsageRecord{
		Provider:        "opencode",
		TimestampMs:     tsMs,
		Model:           model,
		SessionID:       sessionID,
		Project:         agent.ProjectNameFromPath(dir),
		ProjectPath:     dir,
		Totals:          totals,
		ReportedCostUSD: usageFloatPtrIfPos(msg.Cost),
		Speed:           agent.UsageSpeedStandard,
		DedupeKey:       dedupeKey,
	}, true
}

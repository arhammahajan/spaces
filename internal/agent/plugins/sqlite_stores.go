package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	_ "modernc.org/sqlite"
)

func openReadOnlySQLite(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, os.ErrNotExist
	}
	slashed := filepath.ToSlash(path)
	escaped := strings.ReplaceAll(strings.ReplaceAll(slashed, "?", "%3F"), "#", "%23")
	uri := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=2000", escaped)
	return sql.Open("sqlite", uri)
}

// loadAntigravitySQLite reads titled conversation summaries from Antigravity's
// conversation_summaries.db, skipping subagent rows.
func loadAntigravitySQLite(ctx context.Context, home string) []agent.ConversationRecord {
	if ctx.Err() != nil {
		return nil
	}
	dbPath := filepath.Join(home, ".gemini", "antigravity-cli", "conversation_summaries.db")
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT conversation_id, title, preview, step_count, last_modified_time, workspace_uris, parent_conversation_id
		FROM conversation_summaries
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	var records []agent.ConversationRecord
	for rows.Next() {
		if ctx.Err() != nil {
			break
		}
		var (
			convID        sql.NullString
			titleNull     sql.NullString
			previewNull   sql.NullString
			stepCount     sql.NullInt64
			lastModRaw    any
			workspaceURIs sql.NullString
			parentID      sql.NullString
		)
		if err := rows.Scan(&convID, &titleNull, &previewNull, &stepCount, &lastModRaw, &workspaceURIs, &parentID); err != nil {
			continue
		}
		if strings.TrimSpace(parentID.String) != "" {
			continue
		}
		id := strings.TrimSpace(convID.String)
		if id == "" {
			continue
		}

		preview := strings.TrimSpace(previewNull.String)
		title := cleanSingleLine(titleNull.String, maxDerivedTitleLen)
		if title == "" && preview != "" {
			title = cleanSingleLine(preview, maxDerivedTitleLen)
		}

		tsMs := parseSQLiteTimestampMs(lastModRaw)
		cwd := extractWorkspaceURICWD(workspaceURIs.String)

		sourcePath := filepath.Join(convDir, id+".db")
		if _, err := os.Stat(sourcePath); err != nil {
			sourcePath = dbPath
		}

		msgCount := 0
		if stepCount.Valid && stepCount.Int64 > 0 {
			msgCount = int(stepCount.Int64)
		}

		records = append(records, agent.ConversationRecord{
			Agent:        "antigravity",
			App:          "antigravity",
			SessionID:    id,
			Title:        title,
			CWD:          cwd,
			FirstMessage: preview,
			LastMessage:  preview,
			MessageCount: msgCount,
			CreatedAtMs:  tsMs,
			UpdatedAtMs:  tsMs,
			SourcePath:   sourcePath,
		})
	}
	return records
}

// loadOpenCodeSQLite reads sessions from OpenCode's SQLite database
// (<home>/.local/share/opencode/opencode.db), supporting both session_v2 and session tables.
func loadOpenCodeSQLite(ctx context.Context, home string) []agent.ConversationRecord {
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	return loadSessionTableSQLite(ctx, dbPath, "opencode", "opencode", []string{"session_v2", "session"})
}

// loadKiloSQLite reads sessions from Kilo's SQLite database
// (<home>/.local/share/kilo/kilo.db).
func loadKiloSQLite(ctx context.Context, home string) []agent.ConversationRecord {
	dbPath := filepath.Join(home, ".local", "share", "kilo", "kilo.db")
	return loadSessionTableSQLite(ctx, dbPath, "kilo", "kilo", []string{"session"})
}

func loadSessionTableSQLite(ctx context.Context, dbPath, agentID, appName string, candidateTables []string) []agent.ConversationRecord {
	if ctx.Err() != nil {
		return nil
	}
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	tableName := ""
	for _, tbl := range candidateTables {
		if sqliteTableExists(ctx, db, tbl) {
			tableName = tbl
			break
		}
	}
	if tableName == "" {
		return nil
	}

	cols := sqliteTableColumns(ctx, db, tableName)
	colExpr := func(name string) string {
		if cols[strings.ToLower(name)] {
			return name
		}
		return "NULL"
	}

	query := fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, %s, %s FROM %s",
		colExpr("id"),
		colExpr("parent_id"),
		colExpr("directory"),
		colExpr("title"),
		colExpr("model"),
		colExpr("time_created"),
		colExpr("time_updated"),
		colExpr("time_archived"),
		tableName,
	)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var records []agent.ConversationRecord
	for rows.Next() {
		if ctx.Err() != nil {
			break
		}
		var (
			idNull       sql.NullString
			parentNull   sql.NullString
			dirNull      sql.NullString
			titleNull    sql.NullString
			modelNull    sql.NullString
			createdRaw   any
			updatedRaw   any
			archivedRaw  any
		)
		if err := rows.Scan(&idNull, &parentNull, &dirNull, &titleNull, &modelNull, &createdRaw, &updatedRaw, &archivedRaw); err != nil {
			continue
		}
		if strings.TrimSpace(parentNull.String) != "" {
			continue
		}
		id := strings.TrimSpace(idNull.String)
		if id == "" {
			continue
		}

		createdMs := parseSQLiteTimestampMs(createdRaw)
		updatedMs := parseSQLiteTimestampMs(updatedRaw)
		archivedMs := parseSQLiteTimestampMs(archivedRaw)
		if updatedMs == 0 {
			updatedMs = createdMs
		}
		if createdMs == 0 {
			createdMs = updatedMs
		}

		records = append(records, agent.ConversationRecord{
			Agent:       agentID,
			App:         appName,
			SessionID:   id,
			Title:       cleanSingleLine(titleNull.String, maxDerivedTitleLen),
			CWD:         strings.TrimSpace(dirNull.String),
			Model:       parseSQLiteModel(modelNull.String),
			CreatedAtMs: createdMs,
			UpdatedAtMs: updatedMs,
			Archived:    archivedMs > 0,
			SourcePath:  dbPath,
		})
	}
	return records
}

func sqliteTableExists(ctx context.Context, db *sql.DB, tableName string) bool {
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableName).Scan(&name)
	return err == nil && name != ""
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, tableName string) map[string]bool {
	cols := make(map[string]bool)
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err == nil {
			cols[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	return cols
}

func extractWorkspaceURICWD(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var uris []string
	if err := json.Unmarshal([]byte(raw), &uris); err != nil {
		return ""
	}
	for _, u := range uris {
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "file://") {
			continue
		}
		p := strings.TrimPrefix(u, "file://")
		if unescaped, err := url.PathUnescape(p); err == nil {
			p = unescaped
		}
		p = strings.TrimSpace(p)
		if p != "" {
			return p
		}
	}
	return ""
}

func parseSQLiteModel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(s), &obj); err == nil {
			return firstNonEmpty(
				strField(obj, "id"),
				strField(obj, "modelID"),
				strField(obj, "model_id"),
				strField(obj, "name"),
			)
		}
	}
	return s
}

func parseSQLiteTimestampMs(v any) int64 {
	switch val := v.(type) {
	case nil:
		return 0
	case time.Time:
		if val.IsZero() {
			return 0
		}
		return val.UnixMilli()
	case []byte:
		return parseSQLiteTimestampMs(string(val))
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0
		}
		if ts := parseTimestampMs(s); ts > 0 {
			return ts
		}
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05Z07:00",
			"2006-01-02 15:04:05.999999999",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UnixMilli()
			}
		}
		return 0
	default:
		return parseTimestampMs(val)
	}
}

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
	uri := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(500)&_pragma=query_only(1)", escaped)
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
			sourcePath = dbPath + "#" + id
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

// loadMuseSQLite reads indexed sessions from Muse's SQLite database
// (<home>/.local/share/muse/session-index.db), skipping subagent rows.
func loadMuseSQLite(ctx context.Context, home string) []agent.ConversationRecord {
	if ctx.Err() != nil {
		return nil
	}
	dbPath := filepath.Join(home, ".local", "share", "muse", "session-index.db")
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	if !sqliteTableExists(ctx, db, "sessions") {
		return nil
	}
	cols := sqliteTableColumns(ctx, db, "sessions")
	colExpr := func(name string) string {
		if cols[strings.ToLower(name)] {
			return name
		}
		return "NULL"
	}

	query := fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s FROM sessions",
		colExpr("session_id"),
		colExpr("workspace_root"),
		colExpr("title"),
		colExpr("effective_long_title"),
		colExpr("first_user_prompt"),
		colExpr("model_id"),
		colExpr("created_at_us"),
		colExpr("updated_at_us"),
		colExpr("prompt_count"),
		colExpr("session_log_path"),
		colExpr("msp_parent_session_id"),
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
			sidNull     sql.NullString
			cwdNull     sql.NullString
			titleNull   sql.NullString
			longNull    sql.NullString
			promptNull  sql.NullString
			modelNull   sql.NullString
			createdRaw  any
			updatedRaw  any
			promptCount sql.NullInt64
			logPathNull sql.NullString
			parentNull  sql.NullString
		)
		if err := rows.Scan(
			&sidNull, &cwdNull, &titleNull, &longNull, &promptNull,
			&modelNull, &createdRaw, &updatedRaw, &promptCount,
			&logPathNull, &parentNull,
		); err != nil {
			continue
		}
		if strings.TrimSpace(parentNull.String) != "" {
			continue
		}
		id := strings.TrimSpace(sidNull.String)
		if id == "" {
			continue
		}

		firstPrompt := strings.TrimSpace(promptNull.String)
		title := cleanSingleLine(firstNonEmpty(titleNull.String, longNull.String, firstPrompt), maxDerivedTitleLen)
		msgCount := 0
		if promptCount.Valid && promptCount.Int64 > 0 {
			msgCount = int(promptCount.Int64)
		}
		if isPlaceholderSessionTitle(title) && msgCount == 0 && firstPrompt == "" {
			continue
		}
		createdMs := parseSQLiteTimestampMs(createdRaw)
		updatedMs := parseSQLiteTimestampMs(updatedRaw)
		if updatedMs == 0 {
			updatedMs = createdMs
		}
		if createdMs == 0 {
			createdMs = updatedMs
		}

		srcPath := strings.TrimSpace(logPathNull.String)
		if srcPath == "" {
			srcPath = dbPath + "#" + id
		}

		records = append(records, agent.ConversationRecord{
			Agent:        "muse",
			App:          "muse",
			SessionID:    id,
			Title:        title,
			CWD:          strings.TrimSpace(cwdNull.String),
			Model:        strings.TrimSpace(modelNull.String),
			FirstMessage: firstPrompt,
			LastMessage:  firstPrompt,
			MessageCount: msgCount,
			CreatedAtMs:  createdMs,
			UpdatedAtMs:  updatedMs,
			SourcePath:   srcPath,
		})
	}
	return records
}

type sqliteSessionSummary struct {
	firstUser string
	lastText  string
	msgCount  int
	model     string
}

func isPlaceholderSessionTitle(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" || low == "new session" || low == "untitled" {
		return true
	}
	return strings.HasPrefix(low, "new session - ")
}

func loadSQLiteSessionSummaries(ctx context.Context, db *sql.DB) map[string]sqliteSessionSummary {
	out := make(map[string]sqliteSessionSummary)
	if sqliteTableExists(ctx, db, "message") && sqliteTableExists(ctx, db, "part") {
		rows, err := db.QueryContext(ctx, `
			SELECT m.session_id, m.data, p.data
			FROM message m
			LEFT JOIN part p ON p.message_id = m.id
			ORDER BY m.session_id ASC, m.time_created ASC, p.time_created ASC
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				if ctx.Err() != nil {
					break
				}
				var (
					sidNull  sql.NullString
					msgData  sql.NullString
					partData sql.NullString
				)
				if err := rows.Scan(&sidNull, &msgData, &partData); err != nil {
					continue
				}
				sid := strings.TrimSpace(sidNull.String)
				if sid == "" {
					continue
				}
				role, modelName := extractSQLiteRoleAndModel(msgData.String)
				text := extractSQLiteMessageText(partData.String)
				if text == "" {
					text = extractSQLiteMessageText(msgData.String)
				}
				sum := out[sid]
				if sum.model == "" && modelName != "" {
					sum.model = modelName
				}
				if text != "" {
					sum.msgCount++
					if role == "user" && sum.firstUser == "" && !isBoilerplateUserMessage(text) {
						sum.firstUser = text
					}
					sum.lastText = text
				}
				out[sid] = sum
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	if sqliteTableExists(ctx, db, "session_message") {
		// Count messages per session without reading large assistant/tool JSON blobs.
		if countRows, err := db.QueryContext(ctx, `
			SELECT session_id, COUNT(*)
			FROM session_message
			WHERE lower(type) IN ('user', 'assistant')
			GROUP BY session_id
		`); err == nil {
			defer countRows.Close()
			for countRows.Next() {
				if ctx.Err() != nil {
					break
				}
				var (
					sidNull sql.NullString
					cnt     int
				)
				if err := countRows.Scan(&sidNull, &cnt); err != nil {
					continue
				}
				sid := strings.TrimSpace(sidNull.String)
				if sid == "" {
					continue
				}
				sum := out[sid]
				sum.msgCount = cnt
				out[sid] = sum
			}
		}

		// Read only user messages to extract first/last prompt text (avoids scanning
		// hundreds of MB of assistant tool outputs on every popup refresh).
		rows, err := db.QueryContext(ctx, `
			SELECT session_id, data
			FROM session_message
			WHERE lower(type) = 'user'
			ORDER BY session_id ASC, seq ASC, time_created ASC
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				if ctx.Err() != nil {
					break
				}
				var (
					sidNull sql.NullString
					dataStr sql.NullString
				)
				if err := rows.Scan(&sidNull, &dataStr); err != nil {
					continue
				}
				sid := strings.TrimSpace(sidNull.String)
				if sid == "" {
					continue
				}
				text := extractSQLiteMessageText(dataStr.String)
				if text == "" {
					continue
				}
				sum := out[sid]
				if sum.msgCount == 0 {
					sum.msgCount = 1
				}
				if sum.firstUser == "" && !isBoilerplateUserMessage(text) {
					sum.firstUser = text
				}
				sum.lastText = text
				out[sid] = sum
			}
		}
	}
	return out
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

	summaries := loadSQLiteSessionSummaries(ctx, db)

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
			idNull      sql.NullString
			parentNull  sql.NullString
			dirNull     sql.NullString
			titleNull   sql.NullString
			modelNull   sql.NullString
			createdRaw  any
			updatedRaw  any
			archivedRaw any
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

		sum := summaries[id]
		title := cleanSingleLine(titleNull.String, maxDerivedTitleLen)
		if isPlaceholderSessionTitle(title) {
			if sum.firstUser != "" {
				title = cleanSingleLine(sum.firstUser, maxDerivedTitleLen)
			} else if sum.lastText != "" {
				title = cleanSingleLine(sum.lastText, maxDerivedTitleLen)
			} else if sum.msgCount == 0 {
				// Skip empty abandoned placeholder sessions ("New session - ...").
				continue
			} else {
				title = ""
			}
		}

		modelName := parseSQLiteModel(modelNull.String)
		if modelName == "" {
			modelName = sum.model
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
			Agent:        agentID,
			App:          appName,
			SessionID:    id,
			Title:        title,
			CWD:          strings.TrimSpace(dirNull.String),
			Model:        modelName,
			FirstMessage: sum.firstUser,
			LastMessage:  sum.lastText,
			MessageCount: sum.msgCount,
			CreatedAtMs:  createdMs,
			UpdatedAtMs:  updatedMs,
			Archived:     archivedMs > 0,
			SourcePath:   dbPath + "#" + id,
		})
	}
	return records
}

func sqliteTableExists(ctx context.Context, db *sql.DB, tableName string) bool {
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableName).Scan(&name)
	return err == nil && name != ""
}

// parseSQLiteLog extracts message-level LogEntries from SQLite-backed stores
// for Antigravity (conversations_fts), OpenCode (session_message), and Kilo
// (message + part / session_message).
func parseSQLiteLog(ctx context.Context, provider, path string) ([]agent.LogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	basePath, sessionID, _ := strings.Cut(path, "#")
	if _, err := os.Stat(basePath); err != nil {
		return nil, err
	}

	switch provider {
	case "antigravity":
		return parseAntigravitySQLiteLog(ctx, basePath, sessionID)
	case "opencode":
		return parseOpenCodeSQLiteLog(ctx, basePath, sessionID)
	case "kilo":
		return parseKiloSQLiteLog(ctx, basePath, sessionID)
	default:
		return nil, nil
	}
}

func parseAntigravitySQLiteLog(ctx context.Context, basePath, sessionID string) ([]agent.LogEntry, error) {
	summariesPath := basePath
	if filepath.Base(basePath) != "conversation_summaries.db" {
		if sessionID == "" {
			sessionID = strings.TrimSuffix(filepath.Base(basePath), filepath.Ext(basePath))
		}
		summariesPath = filepath.Join(filepath.Dir(filepath.Dir(basePath)), "conversation_summaries.db")
	}
	if sessionID == "" {
		return nil, nil
	}
	db, err := openReadOnlySQLite(summariesPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	if !sqliteTableExists(ctx, db, "conversations_fts") {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT user_body, agent_body, last_modified_time
		FROM conversations_fts
		WHERE conversation_id = ?
		ORDER BY chunk_index ASC
	`, sessionID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var entries []agent.LogEntry
	for rows.Next() {
		if ctx.Err() != nil {
			break
		}
		var (
			userBody  sql.NullString
			agentBody sql.NullString
			lastMod   any
		)
		if err := rows.Scan(&userBody, &agentBody, &lastMod); err != nil {
			continue
		}
		ts := parseSQLiteTimestampMs(lastMod)
		if u := strings.TrimSpace(userBody.String); u != "" {
			entries = append(entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "user",
				Event:       "message",
				Text:        u,
				SessionID:   sessionID,
			})
		}
		if a := strings.TrimSpace(agentBody.String); a != "" {
			entries = append(entries, agent.LogEntry{
				TimestampMs: ts,
				Role:        "assistant",
				Event:       "message",
				Text:        a,
				SessionID:   sessionID,
			})
		}
	}
	return entries, nil
}

func parseOpenCodeSQLiteLog(ctx context.Context, dbPath, sessionID string) ([]agent.LogEntry, error) {
	if sessionID == "" {
		return nil, nil
	}
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()
	return querySessionMessageTable(ctx, db, sessionID)
}

func parseKiloSQLiteLog(ctx context.Context, dbPath, sessionID string) ([]agent.LogEntry, error) {
	if sessionID == "" {
		return nil, nil
	}
	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	if sqliteTableExists(ctx, db, "message") && sqliteTableExists(ctx, db, "part") {
		rows, err := db.QueryContext(ctx, `
			SELECT m.time_created, m.data, p.data
			FROM message m
			LEFT JOIN part p ON p.message_id = m.id
			WHERE m.session_id = ?
			ORDER BY m.time_created ASC, p.time_created ASC
		`, sessionID)
		if err == nil {
			defer rows.Close()
			var entries []agent.LogEntry
			for rows.Next() {
				if ctx.Err() != nil {
					break
				}
				var (
					createdRaw any
					msgData    sql.NullString
					partData   sql.NullString
				)
				if err := rows.Scan(&createdRaw, &msgData, &partData); err != nil {
					continue
				}
				role, modelName := extractSQLiteRoleAndModel(msgData.String)
				text := extractSQLiteMessageText(partData.String)
				if text == "" {
					text = extractSQLiteMessageText(msgData.String)
				}
				if text == "" {
					continue
				}
				entries = append(entries, agent.LogEntry{
					TimestampMs: parseSQLiteTimestampMs(createdRaw),
					Role:        role,
					Event:       "message",
					Text:        text,
					SessionID:   sessionID,
					Model:       modelName,
				})
			}
			if len(entries) > 0 {
				return entries, nil
			}
		}
	}

	return querySessionMessageTable(ctx, db, sessionID)
}

func querySessionMessageTable(ctx context.Context, db *sql.DB, sessionID string) ([]agent.LogEntry, error) {
	if !sqliteTableExists(ctx, db, "session_message") {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT type, time_created, data
		FROM session_message
		WHERE session_id = ?
		ORDER BY seq ASC, time_created ASC
	`, sessionID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var entries []agent.LogEntry
	for rows.Next() {
		if ctx.Err() != nil {
			break
		}
		var (
			typ        sql.NullString
			createdRaw any
			dataStr    sql.NullString
		)
		if err := rows.Scan(&typ, &createdRaw, &dataStr); err != nil {
			continue
		}
		text := extractSQLiteMessageText(dataStr.String)
		if text == "" {
			continue
		}
		role := "assistant"
		if strings.EqualFold(strings.TrimSpace(typ.String), "user") {
			role = "user"
		}
		entries = append(entries, agent.LogEntry{
			TimestampMs: parseSQLiteTimestampMs(createdRaw),
			Role:        role,
			Event:       "message",
			Text:        text,
			SessionID:   sessionID,
		})
	}
	return entries, nil
}

func extractSQLiteRoleAndModel(raw string) (string, string) {
	role := "assistant"
	modelName := ""
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return role, modelName
	}
	if r := strings.ToLower(strings.TrimSpace(strField(doc, "role"))); r != "" {
		role = r
	}
	if m, ok := doc["model"].(map[string]any); ok {
		modelName = firstNonEmpty(strField(m, "modelID"), strField(m, "id"))
	} else if m := strField(doc, "model"); m != "" {
		modelName = m
	}
	return role, modelName
}

func extractSQLiteMessageText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return ""
	}
	if t := strings.TrimSpace(strField(doc, "text")); t != "" {
		return t
	}
	if content := firstNonNil(doc["content"], doc["parts"]); content != nil {
		text, _, _ := extractContentParts(content)
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
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

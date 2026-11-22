package model

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const conversationOverlaySchema = `
CREATE TABLE IF NOT EXISTS conversation_overlay (
  agent TEXT NOT NULL,
  session_id TEXT NOT NULL,
  space TEXT NOT NULL DEFAULT '',
  last_opened_ms INTEGER NOT NULL DEFAULT 0,
  last_outcome TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  launch_cmd_json TEXT NOT NULL DEFAULT '',
  updated_at_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (agent, session_id)
);
`

const conversationOverlayUpsertSQL = `
INSERT INTO conversation_overlay (
  agent, session_id, space, last_opened_ms, last_outcome,
  profile, cwd, title, launch_cmd_json, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent, session_id) DO UPDATE SET
  space = CASE WHEN excluded.space != '' THEN excluded.space ELSE conversation_overlay.space END,
  last_opened_ms = MAX(conversation_overlay.last_opened_ms, excluded.last_opened_ms),
  last_outcome = CASE WHEN excluded.last_outcome != '' THEN excluded.last_outcome ELSE conversation_overlay.last_outcome END,
  profile = CASE WHEN excluded.profile != '' THEN excluded.profile ELSE conversation_overlay.profile END,
  cwd = CASE WHEN excluded.cwd != '' THEN excluded.cwd ELSE conversation_overlay.cwd END,
  title = CASE WHEN excluded.title != '' THEN excluded.title ELSE conversation_overlay.title END,
  launch_cmd_json = CASE WHEN excluded.launch_cmd_json != '' THEN excluded.launch_cmd_json ELSE conversation_overlay.launch_cmd_json END,
  updated_at_ms = MAX(conversation_overlay.updated_at_ms, excluded.updated_at_ms)
`

func resolveConversationOverlayPaths(path string) (dbPath, legacyJSONPath string) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", ""
	}
	if strings.HasSuffix(p, ".json") {
		return strings.TrimSuffix(p, ".json") + ".db", p
	}
	return p, filepath.Join(filepath.Dir(p), "conversations-overlay.json")
}

func openConversationOverlayDB(path string) (*sql.DB, error) {
	dbPath, legacyJSONPath := resolveConversationOverlayPaths(path)
	if dbPath == "" {
		return nil, nil
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

	if _, err := db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(conversationOverlaySchema); err != nil {
		_ = db.Close()
		return nil, err
	}

	if !existed {
		_ = os.Chmod(dbPath, 0o600)
	}

	if err := migrateLegacyOverlayJSONIfNeeded(db, legacyJSONPath); err != nil {
		// Best-effort migration: do not fail opening the DB on corrupt legacy JSON.
		_ = err
	}

	return db, nil
}

func migrateLegacyOverlayJSONIfNeeded(db *sql.DB, legacyJSONPath string) error {
	if db == nil || strings.TrimSpace(legacyJSONPath) == "" {
		return nil
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_overlay`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	raw, err := os.ReadFile(legacyJSONPath)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	var file conversationOverlayFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return err
	}
	if len(file.Entries) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(conversationOverlayUpsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()

	nowMs := time.Now().UnixMilli()
	for k, v := range file.Entries {
		canon := CanonicalConversationAgent(v.Agent)
		sid := strings.TrimSpace(v.SessionID)
		if canon == "" || sid == "" {
			if parts := strings.SplitN(k, ":", 2); len(parts) == 2 {
				if canon == "" {
					canon = CanonicalConversationAgent(parts[0])
				}
				if sid == "" {
					sid = strings.TrimSpace(parts[1])
				}
			}
		}
		if canon == "" || sid == "" {
			continue
		}

		updatedAt := v.UpdatedAtMs
		if updatedAt <= 0 {
			updatedAt = v.LastOpenedMs
		}
		if updatedAt <= 0 {
			updatedAt = nowMs
		}

		if _, err := stmt.Exec(
			canon,
			sid,
			strings.TrimSpace(v.Space),
			v.LastOpenedMs,
			strings.TrimSpace(v.LastOutcome),
			strings.TrimSpace(v.Profile),
			strings.TrimSpace(v.CWD),
			strings.TrimSpace(v.Title),
			encodeLaunchCmdJSON(v.LaunchCmd),
			updatedAt,
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func encodeLaunchCmdJSON(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeLaunchCmdJSON(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var cmd []string
	if err := json.Unmarshal([]byte(trimmed), &cmd); err != nil || len(cmd) == 0 {
		return nil
	}
	return cmd
}

func loadConversationOverlaySQLite(path string) map[string]conversationOverlayEntry {
	out := make(map[string]conversationOverlayEntry)
	dbPath, legacyJSONPath := resolveConversationOverlayPaths(path)
	if dbPath == "" {
		return out
	}

	// Avoid creating an empty DB file on read when neither the DB nor a legacy
	// JSON overlay file exists yet.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if legacyJSONPath == "" {
			return out
		}
		info, legErr := os.Stat(legacyJSONPath)
		if legErr != nil || info.IsDir() || info.Size() == 0 {
			return out
		}
	}

	db, err := openConversationOverlayDB(path)
	if err != nil || db == nil {
		return out
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT agent, session_id, space, last_opened_ms, last_outcome,
		       profile, cwd, title, launch_cmd_json, updated_at_ms
		FROM conversation_overlay
	`)
	if err != nil {
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ov            conversationOverlayEntry
			launchCmdJSON string
		)
		if err := rows.Scan(
			&ov.Agent,
			&ov.SessionID,
			&ov.Space,
			&ov.LastOpenedMs,
			&ov.LastOutcome,
			&ov.Profile,
			&ov.CWD,
			&ov.Title,
			&launchCmdJSON,
			&ov.UpdatedAtMs,
		); err != nil {
			continue
		}
		canon := CanonicalConversationAgent(ov.Agent)
		sid := strings.TrimSpace(ov.SessionID)
		if canon == "" || sid == "" {
			continue
		}
		ov.Agent = canon
		ov.SessionID = sid
		ov.LaunchCmd = decodeLaunchCmdJSON(launchCmdJSON)
		out[overlayKey(canon, sid)] = ov
	}

	return out
}

func upsertConversationOverlaySQLite(path string, ov conversationOverlayEntry) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	canon := CanonicalConversationAgent(ov.Agent)
	sid := strings.TrimSpace(ov.SessionID)
	if canon == "" || sid == "" {
		return nil
	}

	db, err := openConversationOverlayDB(path)
	if err != nil || db == nil {
		return err
	}
	defer db.Close()

	updatedAt := ov.UpdatedAtMs
	if updatedAt <= 0 {
		updatedAt = ov.LastOpenedMs
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}

	_, err = db.Exec(
		conversationOverlayUpsertSQL,
		canon,
		sid,
		strings.TrimSpace(ov.Space),
		ov.LastOpenedMs,
		strings.TrimSpace(ov.LastOutcome),
		strings.TrimSpace(ov.Profile),
		strings.TrimSpace(ov.CWD),
		strings.TrimSpace(ov.Title),
		encodeLaunchCmdJSON(ov.LaunchCmd),
		updatedAt,
	)
	return err
}

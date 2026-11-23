package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

type opencodeTelemetryProvider struct{}

func newOpenCodeTelemetryProvider() *opencodeTelemetryProvider {
	return &opencodeTelemetryProvider{}
}

func (p *opencodeTelemetryProvider) CollectSessionMetrics(ctx context.Context, q agent.SessionMetricsQuery) (*agent.SessionMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dbPath := resolveOpenCodeDBPath(q)
	if dbPath == "" || isSymlinkPath(dbPath) {
		return nil, nil
	}

	db, err := openReadOnlySQLite(dbPath)
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	sessionTable := ""
	for _, cand := range []string{"session_v2", "session"} {
		if sqliteTableExists(ctx, db, cand) {
			sessionTable = cand
			break
		}
	}
	if sessionTable == "" {
		return nil, nil
	}

	cols := sqliteTableColumns(ctx, db, sessionTable)
	colExpr := func(name string) string {
		if cols[strings.ToLower(name)] {
			return name
		}
		return "NULL"
	}

	var (
		query string
		args  []any
	)
	selectClause := fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, %s FROM %s",
		colExpr("id"),
		colExpr("title"),
		colExpr("directory"),
		colExpr("version"),
		colExpr("model"),
		colExpr("time_created"),
		colExpr("time_updated"),
		sessionTable,
	)

	if sid := strings.TrimSpace(q.SessionID); sid != "" {
		query = selectClause + " WHERE id = ? LIMIT 1"
		args = []any{sid}
	} else if cwd := strings.TrimSpace(q.CWD); cwd != "" {
		orderCol := "time_updated"
		if !cols["time_updated"] {
			orderCol = "time_created"
		}
		query = selectClause + fmt.Sprintf(" WHERE directory = ? ORDER BY %s DESC LIMIT 1", orderCol)
		args = []any{cwd}
	} else {
		return nil, nil
	}

	var (
		idNull      sql.NullString
		titleNull   sql.NullString
		dirNull     sql.NullString
		versionNull sql.NullString
		modelNull   sql.NullString
		createdRaw  any
		updatedRaw  any
	)
	if err := db.QueryRowContext(ctx, query, args...).Scan(
		&idNull, &titleNull, &dirNull, &versionNull, &modelNull, &createdRaw, &updatedRaw,
	); err != nil {
		return nil, nil
	}

	sessionID := strings.TrimSpace(idNull.String)
	if sessionID == "" {
		return nil, nil
	}

	metrics := &agent.SessionMetrics{
		Agent:         "opencode",
		SessionID:     sessionID,
		LaunchSurface: agent.DetectLaunchSurface(q.Cmdline),
		ConfigRoot:    filepath.Dir(dbPath),
		Profile:       q.Profile,
		CWD:           strings.TrimSpace(dirNull.String),
		Version:       sanitizeTelemetryText(versionNull.String, 64),
		Model:         parseSQLiteModel(modelNull.String),
		Summary:       sanitizeTelemetryText(titleNull.String, 160),
		StartedAtMs:   parseSQLiteTimestampMs(createdRaw),
		UpdatedAtMs:   parseSQLiteTimestampMs(updatedRaw),
	}

	var maxContextTokens int64
	if sqliteTableExists(ctx, db, "message") {
		rows, err := db.QueryContext(ctx, `
			SELECT data
			FROM message
			WHERE session_id = ?
			ORDER BY time_created ASC
		`, sessionID)
		if err == nil {
			defer rows.Close()
			var latestModel, latestProvider string
			for rows.Next() {
				if ctx.Err() != nil {
					break
				}
				var dataNull sql.NullString
				if err := rows.Scan(&dataNull); err != nil {
					continue
				}
				var doc map[string]any
				if err := json.Unmarshal([]byte(dataNull.String), &doc); err != nil {
					continue
				}
				if !strings.EqualFold(strField(doc, "role"), "assistant") {
					continue
				}

				metrics.TurnCount++

				if m := strField(doc, "modelID"); m != "" {
					latestModel = m
				} else if mObj, ok := doc["model"].(map[string]any); ok {
					if m := firstNonEmpty(strField(mObj, "modelID"), strField(mObj, "id")); m != "" {
						latestModel = m
					}
					if prov := strField(mObj, "providerID"); prov != "" {
						latestProvider = prov
					}
				}
				if prov := strField(doc, "providerID"); prov != "" {
					latestProvider = prov
				}

				if cost, ok := float64Field(doc, "cost"); ok && cost > 0 {
					metrics.Usage.CostUSD += cost
				}

				if toks, ok := doc["tokens"].(map[string]any); ok {
					inp := int64Field(toks, "input")
					out := int64Field(toks, "output")
					reasoning := int64Field(toks, "reasoning")
					var cacheRead, cacheWrite int64
					if cache, ok := toks["cache"].(map[string]any); ok {
						cacheRead = int64Field(cache, "read")
						cacheWrite = int64Field(cache, "write")
					}

					metrics.Usage.InputTokens += inp
					metrics.Usage.OutputTokens += out
					metrics.Usage.ReasoningTokens += reasoning
					metrics.Usage.CacheReadTokens += cacheRead
					metrics.Usage.CacheWriteTokens += cacheWrite

					turnTotal := inp + out + reasoning + cacheRead + cacheWrite
					if turnTotal > 0 {
						metrics.TokenHistory = appendCappedInt64(metrics.TokenHistory, turnTotal, telemetryMaxHistory)
					}

					turnCtx := inp + cacheRead
					if turnCtx == 0 && cacheWrite > 0 {
						turnCtx = inp + cacheWrite
					}
					if turnCtx == 0 {
						turnCtx = inp + out
					}
					if turnCtx > 0 {
						metrics.ContextTokens = turnCtx
						if turnCtx > maxContextTokens {
							maxContextTokens = turnCtx
						}
						metrics.ContextHistory = appendCappedInt64(metrics.ContextHistory, turnCtx, telemetryMaxHistory)
					}
				}
			}
			if latestProvider != "" && latestModel != "" {
				metrics.Model = latestProvider + "/" + latestModel
			} else if latestModel != "" {
				metrics.Model = latestModel
			}
		}
	}

	if metrics.ContextTokens == 0 && (metrics.Usage.InputTokens+metrics.Usage.OutputTokens) > 0 {
		metrics.ContextTokens = metrics.Usage.InputTokens + metrics.Usage.OutputTokens
	}
	metrics.ContextWindow = agent.ContextWindowForModel(metrics.Model, "", maxContextTokens)

	return metrics, nil
}

func resolveOpenCodeDBPath(q agent.SessionMetricsQuery) string {
	var candidates []string
	if h := strings.TrimSpace(q.HomeDir); h != "" {
		candidates = append(candidates, filepath.Join(h, ".local", "share", "opencode", "opencode.db"))
	}
	for _, r := range q.ConfigRoots {
		r = strings.TrimSpace(r)
		if r != "" {
			candidates = append(candidates, filepath.Join(r, "opencode.db"))
		}
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "opencode", "opencode.db"))
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		candidates = append(candidates, filepath.Join(h, ".local", "share", "opencode", "opencode.db"))
	}

	for _, c := range candidates {
		if info, err := os.Lstat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

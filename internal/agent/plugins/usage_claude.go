package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const claudeSyntheticModel = "<synthetic>"

type claudeUsageReader struct{}

// NewClaudeUsageReader returns an agent.UsageReader for Claude Code JSONL transcripts.
func NewClaudeUsageReader() agent.UsageReader {
	return claudeUsageReader{}
}

func (claudeUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{filepath.Join(home, ".claude", "projects")}
	if opts.IsRealHome {
		if cfgDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); cfgDir != "" {
			roots = append(roots, filepath.Join(expandUsageHomePath(cfgDir, home), "projects"))
		}
	}
	roots = dedupeUsageDirs(roots)

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
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
				return nil
			}
			if opts.SinceMs > 0 {
				if fi, ferr := d.Info(); ferr == nil && usageFileTooOld(fi, opts.SinceMs) {
					return nil
				}
			}

			fileStem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			recs, scanErr := scanJSONLFileIncremental(ctx, path, opts.Cache, jsonlScanHooks{
				ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
					rec, ok := parseClaudeUsageLine(line, fileStem)
					if !ok {
						return nil, false
					}
					return []agent.UsageRecord{rec}, false
				},
			})
			if scanErr != nil {
				return nil
			}
			all = append(all, finalizeUsageRecords(recs, opts, path, seenDedupe)...)
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}

	return all, nil
}

type claudeTranscriptLine struct {
	Type      string               `json:"type"`
	Timestamp any                  `json:"timestamp"`
	RequestID string               `json:"requestId"`
	SessionID string               `json:"sessionId"`
	CWD       string               `json:"cwd"`
	CostUSD   *float64             `json:"costUSD"`
	Message   *claudeUsageMessage  `json:"message"`
}

type claudeUsageMessage struct {
	ID      string           `json:"id"`
	Model   string           `json:"model"`
	Usage   *claudeLineUsage `json:"usage"`
	Content json.RawMessage  `json:"content"`
}

type claudeLineUsage struct {
	InputTokens              float64 `json:"input_tokens"`
	CacheReadInputTokens     float64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens float64 `json:"cache_creation_input_tokens"`
	OutputTokens             float64 `json:"output_tokens"`
	Speed                    string  `json:"speed"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func parseClaudeUsageLine(line []byte, fallbackSessionID string) (agent.UsageRecord, bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return agent.UsageRecord{}, false
	}

	var raw claudeTranscriptLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return agent.UsageRecord{}, false
	}
	if raw.Type != "assistant" || raw.Message == nil || raw.Message.Usage == nil {
		return agent.UsageRecord{}, false
	}

	model := strings.TrimSpace(raw.Message.Model)
	if model == claudeSyntheticModel {
		return agent.UsageRecord{}, false
	}

	tsMs, ok := parseUsageTimestampMs(raw.Timestamp)
	if !ok {
		return agent.UsageRecord{}, false
	}

	u := raw.Message.Usage
	totals := agent.UsageTokenTotals{
		UncachedInputTokens: usageTokenInt64(u.InputTokens),
		CachedInputTokens:   usageTokenInt64(u.CacheReadInputTokens),
		CacheCreationTokens: usageTokenInt64(u.CacheCreationInputTokens),
		OutputTokens:        usageTokenInt64(u.OutputTokens),
		ReasoningTokens:     0,
	}

	if model == "" && !totals.HasData() {
		return agent.UsageRecord{}, false
	}

	reportedCost := usageFloatPtrIfNonNeg(raw.CostUSD)
	if !totals.HasData() && reportedCost == nil {
		return agent.UsageRecord{}, false
	}

	speed := agent.UsageSpeedStandard
	if u.Speed == "fast" {
		speed = agent.UsageSpeedFast
	}

	var dedupeKey string
	msgID := strings.TrimSpace(raw.Message.ID)
	reqID := strings.TrimSpace(raw.RequestID)
	if msgID != "" && reqID != "" {
		dedupeKey = "claude:" + msgID + ":" + reqID
	}

	sessionID := strings.TrimSpace(raw.SessionID)
	if sessionID == "" {
		sessionID = fallbackSessionID
	}

	cwd := strings.TrimSpace(raw.CWD)
	tools := extractClaudeToolNames(raw.Message.Content)

	return agent.UsageRecord{
		Provider:        "claude",
		TimestampMs:     tsMs,
		Model:           model,
		SessionID:       sessionID,
		Project:         agent.ProjectNameFromPath(cwd),
		ProjectPath:     cwd,
		Totals:          totals,
		ReportedCostUSD: reportedCost,
		Speed:           speed,
		DedupeKey:       dedupeKey,
		Tools:           tools,
	}, true
}

func extractClaudeToolNames(rawContent json.RawMessage) []string {
	trimmed := bytes.TrimSpace(rawContent)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var blocks []claudeContentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil
	}
	var tools []string
	for _, b := range blocks {
		if b.Type == "tool_use" {
			if name := strings.TrimSpace(b.Name); name != "" {
				tools = append(tools, name)
			}
		}
	}
	return tools
}

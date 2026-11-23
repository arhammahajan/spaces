package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

type copilotUsageReader struct{}

// NewCopilotUsageReader returns an agent.UsageReader for GitHub Copilot CLI
// session-state event streams.
func NewCopilotUsageReader() agent.UsageReader {
	return copilotUsageReader{}
}

func (copilotUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{filepath.Join(home, ".copilot", "session-state")}
	if opts.IsRealHome {
		if copilotHome := strings.TrimSpace(os.Getenv("COPILOT_HOME")); copilotHome != "" {
			roots = append(roots, filepath.Join(expandUsageHomePath(copilotHome, home), "session-state"))
		}
	}
	roots = dedupeUsageDirs(roots)

	seenDedupe := make(map[string]struct{})
	var all []agent.UsageRecord

	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() {
				continue
			}
			sessionUUID := entry.Name()
			eventsPath := filepath.Join(root, sessionUUID, "events.jsonl")
			fi, serr := os.Stat(eventsPath)
			if serr != nil || fi.IsDir() {
				continue
			}
			if opts.SinceMs > 0 && usageFileTooOld(fi, opts.SinceMs) {
				continue
			}

			state := &copilotUsageScanState{}
			recs, scanErr := scanJSONLFileIncremental(ctx, eventsPath, opts.Cache, jsonlScanHooks{
				RestoreState: func(data []byte) error {
					if len(data) == 0 {
						return nil
					}
					var restored copilotUsageScanState
					if err := json.Unmarshal(data, &restored); err != nil {
						return err
					}
					state = &restored
					return nil
				},
				SaveState: func() ([]byte, error) {
					return json.Marshal(state)
				},
				ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
					return parseCopilotUsageLine(line, sessionUUID, state)
				},
			})
			if scanErr != nil {
				continue
			}
			all = append(all, finalizeUsageRecords(recs, opts, eventsPath, seenDedupe)...)
		}
	}

	return all, nil
}

type copilotUsageScanState struct {
	CWD             string   `json:"cwd,omitempty"`
	Tools           []string `json:"tools,omitempty"`
	EmittedShutdown bool     `json:"emitted_shutdown,omitempty"`
}

type copilotEventLine struct {
	Type      string          `json:"type"`
	Timestamp any             `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type copilotStartData struct {
	Context *struct {
		CWD string `json:"cwd"`
	} `json:"context"`
}

type copilotToolStartData struct {
	ToolName string `json:"toolName"`
}

type copilotShutdownData struct {
	ModelMetrics map[string]struct {
		Usage *struct {
			InputTokens      float64 `json:"inputTokens"`
			OutputTokens     float64 `json:"outputTokens"`
			CacheReadTokens  float64 `json:"cacheReadTokens"`
			CacheWriteTokens float64 `json:"cacheWriteTokens"`
		} `json:"usage"`
	} `json:"modelMetrics"`
}

func parseCopilotUsageLine(
	line []byte,
	sessionUUID string,
	state *copilotUsageScanState,
) ([]agent.UsageRecord, bool) {
	if state.EmittedShutdown {
		return nil, true
	}

	var ev copilotEventLine
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, false
	}

	switch ev.Type {
	case "session.start":
		var data copilotStartData
		if json.Unmarshal(ev.Data, &data) == nil && data.Context != nil {
			if cwd := strings.TrimSpace(data.Context.CWD); cwd != "" {
				state.CWD = cwd
			}
		}
		return nil, false

	case "tool.execution_start":
		var data copilotToolStartData
		if json.Unmarshal(ev.Data, &data) == nil {
			if name := strings.TrimSpace(data.ToolName); name != "" {
				state.Tools = append(state.Tools, name)
			}
		}
		return nil, false

	case "session.shutdown":
		tsMs, ok := parseUsageTimestampMs(ev.Timestamp)
		if !ok {
			return nil, false
		}
		var data copilotShutdownData
		if err := json.Unmarshal(ev.Data, &data); err != nil || len(data.ModelMetrics) == 0 {
			return nil, false
		}

		models := make([]string, 0, len(data.ModelMetrics))
		for m := range data.ModelMetrics {
			if strings.TrimSpace(m) != "" {
				models = append(models, m)
			}
		}
		sort.Strings(models)

		var records []agent.UsageRecord
		first := true
		for _, model := range models {
			metric := data.ModelMetrics[model]
			if metric.Usage == nil {
				continue
			}
			totals := agent.UsageTokenTotals{
				UncachedInputTokens: usageTokenInt64(metric.Usage.InputTokens),
				CachedInputTokens:   usageTokenInt64(metric.Usage.CacheReadTokens),
				CacheCreationTokens: usageTokenInt64(metric.Usage.CacheWriteTokens),
				OutputTokens:        usageTokenInt64(metric.Usage.OutputTokens),
				ReasoningTokens:     0,
			}
			if !totals.HasData() {
				continue
			}

			var tools []string
			if first && len(state.Tools) > 0 {
				tools = append([]string(nil), state.Tools...)
			}
			first = false

			records = append(records, agent.UsageRecord{
				Provider:        "copilot",
				TimestampMs:     tsMs,
				Model:           model,
				SessionID:       sessionUUID,
				Project:         agent.ProjectNameFromPath(state.CWD),
				ProjectPath:     state.CWD,
				Totals:          totals,
				ReportedCostUSD: nil, // requests.cost is premium request count, not USD
				Speed:           agent.UsageSpeedStandard,
				DedupeKey:       "copilot:" + sessionUUID + ":" + model,
				Tools:           tools,
			})
		}

		if len(records) > 0 {
			state.EmittedShutdown = true
			state.Tools = nil
			return records, true
		}
	}

	return nil, false
}

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const grokCostTicksPerDollar = 1e10

type grokUsageReader struct{}

// NewGrokUsageReader returns an agent.UsageReader for Grok CLI session updates.
func NewGrokUsageReader() agent.UsageReader {
	return grokUsageReader{}
}

func (grokUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{filepath.Join(home, ".grok", "sessions")}
	if opts.IsRealHome {
		if grokHome := strings.TrimSpace(os.Getenv("GROK_HOME")); grokHome != "" {
			roots = append(roots, filepath.Join(expandUsageHomePath(grokHome, home), "sessions"))
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
			if d.Name() != "updates.jsonl" {
				return nil
			}
			if opts.SinceMs > 0 {
				if fi, ferr := d.Info(); ferr == nil && usageFileTooOld(fi, opts.SinceMs) {
					return nil
				}
			}

			fallbackSessionID := filepath.Base(filepath.Dir(path))
			recs, scanErr := scanJSONLFileIncremental(ctx, path, opts.Cache, jsonlScanHooks{
				ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
					return parseGrokUsageLine(line, fallbackSessionID), false
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

type grokUpdateLine struct {
	Timestamp any `json:"timestamp"`
	Params    *struct {
		SessionID string `json:"sessionId"`
		Meta      *struct {
			AgentTimestampMs any `json:"agentTimestampMs"`
		} `json:"_meta"`
		Update *struct {
			SessionUpdate string          `json:"sessionUpdate"`
			PromptID      string          `json:"prompt_id"`
			Usage         *grokUsageBlock `json:"usage"`
		} `json:"update"`
	} `json:"params"`
}

type grokUsageTotals struct {
	InputTokens         float64  `json:"inputTokens"`
	OutputTokens        float64  `json:"outputTokens"`
	CachedReadTokens    float64  `json:"cachedReadTokens"`
	CacheCreationTokens float64  `json:"cacheCreationTokens"`
	ReasoningTokens     float64  `json:"reasoningTokens"`
	CostUsdTicks        *float64 `json:"costUsdTicks"`
}

type grokUsageBlock struct {
	grokUsageTotals
	ModelUsage map[string]grokUsageTotals `json:"modelUsage"`
}

func grokTicksToUSD(ticks *float64) *float64 {
	if ticks == nil || math.IsNaN(*ticks) || math.IsInf(*ticks, 0) || *ticks < 0 {
		return nil
	}
	usd := *ticks / grokCostTicksPerDollar
	return &usd
}

func grokTotalsToUsage(t grokUsageTotals) agent.UsageTokenTotals {
	cached := usageTokenInt64(t.CachedReadTokens)
	cacheCreate := usageTokenInt64(t.CacheCreationTokens)
	uncached := maxInt64(0, usageTokenInt64(t.InputTokens)-cached-cacheCreate)
	output := usageTokenInt64(t.OutputTokens)
	reasoning := usageTokenInt64(t.ReasoningTokens)
	if reasoning > output {
		reasoning = output
	}
	return agent.UsageTokenTotals{
		UncachedInputTokens: uncached,
		CachedInputTokens:   cached,
		CacheCreationTokens: cacheCreate,
		OutputTokens:        output,
		ReasoningTokens:     reasoning,
	}
}

func parseGrokUsageLine(line []byte, fallbackSessionID string) []agent.UsageRecord {
	if !bytes.Contains(line, []byte(`"turn_completed"`)) {
		return nil
	}

	var raw grokUpdateLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	if raw.Params == nil || raw.Params.Update == nil || raw.Params.Update.Usage == nil {
		return nil
	}
	upd := raw.Params.Update
	if upd.SessionUpdate != "turn_completed" {
		return nil
	}

	var tsMs int64
	if raw.Params.Meta != nil && raw.Params.Meta.AgentTimestampMs != nil {
		if parsed, ok := parseUsageTimestampMs(raw.Params.Meta.AgentTimestampMs); ok {
			tsMs = parsed
		}
	}
	if tsMs == 0 && raw.Timestamp != nil {
		if parsed, ok := parseUsageTimestampMs(raw.Timestamp); ok {
			tsMs = parsed
		}
	}
	if tsMs <= 0 {
		return nil
	}

	sessionID := strings.TrimSpace(raw.Params.SessionID)
	if sessionID == "" {
		sessionID = fallbackSessionID
	}
	promptID := strings.TrimSpace(upd.PromptID)
	usage := upd.Usage

	if len(usage.ModelUsage) == 0 {
		totals := grokTotalsToUsage(usage.grokUsageTotals)
		if !totals.HasData() {
			return nil
		}
		var dedupeKey string
		if promptID != "" {
			dedupeKey = "grok:" + sessionID + ":" + promptID + ":grok"
		}
		return []agent.UsageRecord{{
			Provider:        "grok",
			TimestampMs:     tsMs,
			Model:           "grok",
			SessionID:       sessionID,
			Totals:          totals,
			ReportedCostUSD: grokTicksToUSD(usage.CostUsdTicks),
			Speed:           agent.UsageSpeedStandard,
			DedupeKey:       dedupeKey,
		}}
	}

	models := make([]string, 0, len(usage.ModelUsage))
	for m := range usage.ModelUsage {
		if strings.TrimSpace(m) != "" {
			models = append(models, m)
		}
	}
	sort.Strings(models)

	topLevelCost := grokTicksToUSD(usage.CostUsdTicks)
	var usedTickedCost float64
	var untickedTokens int64
	for _, m := range models {
		entry := usage.ModelUsage[m]
		tok := grokTotalsToUsage(entry).Total()
		if tok == 0 {
			continue
		}
		if c := grokTicksToUSD(entry.CostUsdTicks); c != nil {
			usedTickedCost += *c
		} else {
			untickedTokens += tok
		}
	}
	var remainingCost *float64
	if topLevelCost != nil {
		rem := math.Max(0, *topLevelCost-usedTickedCost)
		remainingCost = &rem
	}

	var results []agent.UsageRecord
	for _, m := range models {
		entry := usage.ModelUsage[m]
		totals := grokTotalsToUsage(entry)
		if !totals.HasData() {
			continue
		}
		cost := grokTicksToUSD(entry.CostUsdTicks)
		if cost == nil && remainingCost != nil && untickedTokens > 0 {
			share := *remainingCost * (float64(totals.Total()) / float64(untickedTokens))
			cost = &share
		}
		var dedupeKey string
		if promptID != "" {
			dedupeKey = "grok:" + sessionID + ":" + promptID + ":" + m
		}
		results = append(results, agent.UsageRecord{
			Provider:        "grok",
			TimestampMs:     tsMs,
			Model:           m,
			SessionID:       sessionID,
			Totals:          totals,
			ReportedCostUSD: cost,
			Speed:           agent.UsageSpeedStandard,
			DedupeKey:       dedupeKey,
		})
	}
	return results
}

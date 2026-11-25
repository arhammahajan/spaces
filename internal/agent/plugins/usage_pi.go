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

type piUsageReader struct{}

// NewPiUsageReader returns an agent.UsageReader for Pi coding-agent JSONL sessions.
func NewPiUsageReader() agent.UsageReader {
	return piUsageReader{}
}

func (piUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{filepath.Join(home, ".pi", "agent", "sessions")}
	if opts.IsRealHome {
		if sessionDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); sessionDir != "" {
			roots = append(roots, expandUsageHomePath(sessionDir, home))
		} else if agentDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); agentDir != "" {
			roots = append(roots, filepath.Join(expandUsageHomePath(agentDir, home), "sessions"))
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

			state := &piUsageScanState{}
			recs, scanErr := scanJSONLFileIncremental(ctx, path, opts.Cache, jsonlScanHooks{
				RestoreState: func(data []byte) error {
					if len(data) == 0 {
						return nil
					}
					var restored piUsageScanState
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
					rec, ok := parsePiUsageLine(line, state)
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

type piUsageScanState struct {
	SessionID string `json:"session_id,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

type piLineUsage struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Reasoning  float64 `json:"reasoning"`
	Cost       *struct {
		Total *float64 `json:"total"`
	} `json:"cost"`
}

type piSessionLine struct {
	Type      string       `json:"type"`
	ID        string       `json:"id"`
	Timestamp string       `json:"timestamp"`
	CWD       string       `json:"cwd"`
	Provider  string       `json:"provider"`
	Model     string       `json:"model"`
	Usage     *piLineUsage `json:"usage"`
	Message   *struct {
		Role          string       `json:"role"`
		Provider      string       `json:"provider"`
		Model         string       `json:"model"`
		ResponseModel string       `json:"responseModel"`
		Usage         *piLineUsage `json:"usage"`
	} `json:"message"`
}

func parsePiUsageLine(line []byte, state *piUsageScanState) (agent.UsageRecord, bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) && !bytes.Contains(line, []byte(`"session"`)) {
		return agent.UsageRecord{}, false
	}

	var entry piSessionLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return agent.UsageRecord{}, false
	}

	if entry.Type == "session" {
		if id := strings.TrimSpace(entry.ID); id != "" {
			state.SessionID = id
		}
		if cwd := strings.TrimSpace(entry.CWD); cwd != "" {
			state.CWD = cwd
		}
		return agent.UsageRecord{}, false
	}

	var (
		usage     *piLineUsage
		provider  string
		rateModel string
	)

	switch entry.Type {
	case "message":
		if entry.Message == nil || entry.Message.Usage == nil {
			return agent.UsageRecord{}, false
		}
		switch entry.Message.Role {
		case "assistant":
			usage = entry.Message.Usage
			provider = strings.TrimSpace(entry.Message.Provider)
			rateModel = strings.TrimSpace(entry.Message.ResponseModel)
			if rateModel == "" {
				rateModel = strings.TrimSpace(entry.Message.Model)
			}
		case "toolResult":
			usage = entry.Message.Usage
		default:
			return agent.UsageRecord{}, false
		}
	case "usage":
		if entry.Usage == nil {
			return agent.UsageRecord{}, false
		}
		usage = entry.Usage
		provider = strings.TrimSpace(entry.Provider)
		rateModel = strings.TrimSpace(entry.Model)
	case "compaction", "branch_summary":
		if entry.Usage == nil {
			return agent.UsageRecord{}, false
		}
		usage = entry.Usage
	default:
		return agent.UsageRecord{}, false
	}

	tsMs, ok := parseUsageTimestampStringMs(entry.Timestamp)
	if !ok {
		return agent.UsageRecord{}, false
	}

	model := "Tools/summaries"
	if provider != "" && rateModel != "" {
		model = provider + "/" + rateModel
	} else if rateModel != "" {
		model = rateModel
	}

	output := usageTokenInt64(usage.Output)
	reasoning := usageTokenInt64(usage.Reasoning)
	if reasoning > output {
		reasoning = output
	}

	totals := agent.UsageTokenTotals{
		UncachedInputTokens: usageTokenInt64(usage.Input),
		CachedInputTokens:   usageTokenInt64(usage.CacheRead),
		CacheCreationTokens: usageTokenInt64(usage.CacheWrite),
		OutputTokens:        output,
		ReasoningTokens:     reasoning,
	}

	var reportedCost *float64
	if usage.Cost != nil {
		reportedCost = usageFloatPtrIfPos(usage.Cost.Total)
	}
	if !totals.HasData() && reportedCost == nil {
		return agent.UsageRecord{}, false
	}

	var dedupeKey string
	if id := strings.TrimSpace(entry.ID); id != "" {
		dedupeKey = "pi:" + id + ":" + entry.Timestamp + ":" + model
	}

	return agent.UsageRecord{
		Provider:        "pi",
		TimestampMs:     tsMs,
		Model:           model,
		RateModel:       rateModel,
		SessionID:       state.SessionID,
		Project:         agent.ProjectNameFromPath(state.CWD),
		ProjectPath:     state.CWD,
		Totals:          totals,
		ReportedCostUSD: reportedCost,
		Speed:           agent.UsageSpeedStandard,
		DedupeKey:       dedupeKey,
	}, true
}

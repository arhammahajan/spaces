package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const codexForkBurstWindowMs = 1000

type codexUsageReader struct{}

// NewCodexUsageReader returns an agent.UsageReader for OpenAI Codex rollout JSONL logs.
func NewCodexUsageReader() agent.UsageReader {
	return codexUsageReader{}
}

func (codexUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
	}
	if opts.IsRealHome {
		if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
			expanded := expandUsageHomePath(codexHome, home)
			roots = append(roots,
				filepath.Join(expanded, "sessions"),
				filepath.Join(expanded, "archived_sessions"),
			)
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

			state := newCodexUsageScanState()
			recs, scanErr := scanJSONLFileIncremental(ctx, path, opts.Cache, jsonlScanHooks{
				RestoreState: func(data []byte) error {
					if len(data) == 0 {
						return nil
					}
					var restored codexUsageScanState
					if err := json.Unmarshal(data, &restored); err != nil {
						return err
					}
					if restored.Speed == "" {
						restored.Speed = agent.UsageSpeedStandard
					}
					state = &restored
					return nil
				},
				SaveState: func() ([]byte, error) {
					return json.Marshal(state)
				},
				ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
					rec, ok := parseCodexUsageLine(line, state)
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

type codexTokenSnapshot struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

func (s codexTokenSnapshot) signature() string {
	return fmt.Sprintf("%d:%d:%d:%d:%d",
		s.InputTokens, s.CachedInputTokens, s.CacheWriteInputTokens, s.OutputTokens, s.ReasoningOutputTokens)
}

type codexUsageScanState struct {
	SessionID             string              `json:"session_id,omitempty"`
	CWD                   string              `json:"cwd,omitempty"`
	Model                 string              `json:"model,omitempty"`
	Speed                 string              `json:"speed,omitempty"`
	SawSessionMeta        bool                `json:"saw_session_meta,omitempty"`
	IsFork                bool                `json:"is_fork,omitempty"`
	SuppressingForkCopies bool                `json:"suppressing_fork_copies,omitempty"`
	HasForkAnchor         bool                `json:"has_fork_anchor,omitempty"`
	ForkAnchorMs          int64               `json:"fork_anchor_ms,omitempty"`
	LastUsageSignature    string              `json:"last_usage_signature,omitempty"`
	PrevTotal             *codexTokenSnapshot `json:"prev_total,omitempty"`
	PendingTools          []string            `json:"pending_tools,omitempty"`
}

func newCodexUsageScanState() *codexUsageScanState {
	return &codexUsageScanState{
		Speed: agent.UsageSpeedStandard,
	}
}

type codexRolloutLine struct {
	Type      string          `json:"type"`
	Timestamp any             `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	Model          string `json:"model"`
	Name           string `json:"name"`
	ForkedFromID   string `json:"forked_from_id"`
	ServiceTier    string `json:"service_tier"`
	ThreadSettings *struct {
		ServiceTier string `json:"service_tier"`
	} `json:"thread_settings"`
	Source *struct {
		Subagent *struct {
			ThreadSpawn *struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	} `json:"source"`
	Info *struct {
		LastTokenUsage  *codexRawTokenUsage `json:"last_token_usage"`
		TotalTokenUsage *codexRawTokenUsage `json:"total_token_usage"`
	} `json:"info"`
}

type codexRawTokenUsage struct {
	InputTokens           float64 `json:"input_tokens"`
	CachedInputTokens     float64 `json:"cached_input_tokens"`
	CacheWriteInputTokens float64 `json:"cache_write_input_tokens"`
	OutputTokens          float64 `json:"output_tokens"`
	ReasoningOutputTokens float64 `json:"reasoning_output_tokens"`
}

func (r *codexRawTokenUsage) toSnapshot() codexTokenSnapshot {
	return codexTokenSnapshot{
		InputTokens:           usageTokenInt64(r.InputTokens),
		CachedInputTokens:     usageTokenInt64(r.CachedInputTokens),
		CacheWriteInputTokens: usageTokenInt64(r.CacheWriteInputTokens),
		OutputTokens:          usageTokenInt64(r.OutputTokens),
		ReasoningOutputTokens: usageTokenInt64(r.ReasoningOutputTokens),
	}
}

func parseCodexUsageLine(line []byte, state *codexUsageScanState) (agent.UsageRecord, bool) {
	if !bytes.Contains(line, []byte(`"token_count"`)) &&
		!bytes.Contains(line, []byte(`"turn_context"`)) &&
		!bytes.Contains(line, []byte(`"thread_settings_applied"`)) &&
		!bytes.Contains(line, []byte(`"service_tier"`)) &&
		!bytes.Contains(line, []byte(`"session_meta"`)) &&
		!bytes.Contains(line, []byte(`"function_call"`)) {
		return agent.UsageRecord{}, false
	}

	var rec codexRolloutLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return agent.UsageRecord{}, false
	}
	if len(rec.Payload) == 0 || rec.Payload[0] != '{' {
		return agent.UsageRecord{}, false
	}
	var payload codexPayload
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return agent.UsageRecord{}, false
	}

	switch rec.Type {
	case "session_meta":
		if state.SawSessionMeta {
			return agent.UsageRecord{}, false
		}
		state.SawSessionMeta = true
		if id := strings.TrimSpace(payload.ID); id != "" {
			state.SessionID = id
		} else if sid := strings.TrimSpace(payload.SessionID); sid != "" {
			state.SessionID = sid
		}
		if cwd := strings.TrimSpace(payload.CWD); cwd != "" {
			state.CWD = cwd
		}
		if model := strings.TrimSpace(payload.Model); model != "" {
			state.Model = model
		}
		if isCodexForkMeta(&payload) {
			state.IsFork = true
			state.SuppressingForkCopies = true
		}
		return agent.UsageRecord{}, false

	case "turn_context":
		if model := strings.TrimSpace(payload.Model); model != "" {
			state.Model = model
		}
		if state.CWD == "" {
			if cwd := strings.TrimSpace(payload.CWD); cwd != "" {
				state.CWD = cwd
			}
		}
		if tier := strings.TrimSpace(payload.ServiceTier); tier != "" {
			state.Speed = normalizeCodexSpeed(tier)
		}
		return agent.UsageRecord{}, false

	case "response_item":
		if payload.Type == "function_call" {
			if tool := normalizeCodexToolName(payload.Name); tool != "" {
				state.PendingTools = append(state.PendingTools, tool)
			}
		}
		return agent.UsageRecord{}, false
	}

	if payload.Type == "thread_settings_applied" || rec.Type == "thread_settings_applied" {
		tier := payload.ServiceTier
		if payload.ThreadSettings != nil && payload.ThreadSettings.ServiceTier != "" {
			tier = payload.ThreadSettings.ServiceTier
		}
		state.Speed = normalizeCodexSpeed(tier)
		return agent.UsageRecord{}, false
	}

	if payload.Type != "token_count" || payload.Info == nil {
		return agent.UsageRecord{}, false
	}

	tsMs, ok := parseUsageTimestampMs(rec.Timestamp)
	if !ok || state.Model == "" {
		return agent.UsageRecord{}, false
	}

	var (
		snap     codexTokenSnapshot
		hasUsage bool
	)

	var curTotal *codexTokenSnapshot
	if payload.Info.TotalTokenUsage != nil {
		t := payload.Info.TotalTokenUsage.toSnapshot()
		curTotal = &t
	}

	if payload.Info.LastTokenUsage != nil {
		lastSnap := payload.Info.LastTokenUsage.toSnapshot()
		sig := lastSnap.signature()
		if sig == state.LastUsageSignature {
			return agent.UsageRecord{}, false
		}
		if curTotal != nil && state.PrevTotal != nil && *curTotal == *state.PrevTotal {
			return agent.UsageRecord{}, false
		}
		state.LastUsageSignature = sig
		if curTotal != nil {
			cp := *curTotal
			state.PrevTotal = &cp
		}
		snap = lastSnap
		hasUsage = true
	} else if curTotal != nil {
		if state.PrevTotal != nil && *curTotal == *state.PrevTotal {
			return agent.UsageRecord{}, false
		}
		if state.PrevTotal != nil {
			snap = codexTokenSnapshot{
				InputTokens:           maxInt64(0, curTotal.InputTokens-state.PrevTotal.InputTokens),
				CachedInputTokens:     maxInt64(0, curTotal.CachedInputTokens-state.PrevTotal.CachedInputTokens),
				CacheWriteInputTokens: maxInt64(0, curTotal.CacheWriteInputTokens-state.PrevTotal.CacheWriteInputTokens),
				OutputTokens:          maxInt64(0, curTotal.OutputTokens-state.PrevTotal.OutputTokens),
				ReasoningOutputTokens: maxInt64(0, curTotal.ReasoningOutputTokens-state.PrevTotal.ReasoningOutputTokens),
			}
		} else {
			snap = *curTotal
		}
		cp := *curTotal
		state.PrevTotal = &cp
		hasUsage = true
	}

	if !hasUsage {
		return agent.UsageRecord{}, false
	}

	if state.IsFork && state.SuppressingForkCopies {
		if !state.HasForkAnchor {
			state.HasForkAnchor = true
			state.ForkAnchorMs = tsMs
			return agent.UsageRecord{}, false
		}
		if tsMs-state.ForkAnchorMs < codexForkBurstWindowMs {
			return agent.UsageRecord{}, false
		}
		state.SuppressingForkCopies = false
	}

	uncached := maxInt64(0, snap.InputTokens-snap.CachedInputTokens-snap.CacheWriteInputTokens)
	output := snap.OutputTokens
	reasoning := snap.ReasoningOutputTokens
	if reasoning > output {
		reasoning = output
	}

	totals := agent.UsageTokenTotals{
		UncachedInputTokens: uncached,
		CachedInputTokens:   snap.CachedInputTokens,
		CacheCreationTokens: snap.CacheWriteInputTokens,
		OutputTokens:        output,
		ReasoningTokens:     reasoning,
	}
	if !totals.HasData() {
		return agent.UsageRecord{}, false
	}

	var tools []string
	if len(state.PendingTools) > 0 {
		tools = append([]string(nil), state.PendingTools...)
		state.PendingTools = nil
	}

	speed := state.Speed
	if speed == "" {
		speed = agent.UsageSpeedStandard
	}

	return agent.UsageRecord{
		Provider:    "codex",
		TimestampMs: tsMs,
		Model:       state.Model,
		SessionID:   state.SessionID,
		Project:     agent.ProjectNameFromPath(state.CWD),
		ProjectPath: state.CWD,
		Totals:      totals,
		Speed:       speed,
		Tools:       tools,
	}, true
}

func isCodexForkMeta(p *codexPayload) bool {
	if strings.TrimSpace(p.ForkedFromID) != "" {
		return true
	}
	if p.Source != nil &&
		p.Source.Subagent != nil &&
		p.Source.Subagent.ThreadSpawn != nil &&
		strings.TrimSpace(p.Source.Subagent.ThreadSpawn.ParentThreadID) != "" {
		return true
	}
	return false
}

func normalizeCodexSpeed(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "priority", "fast":
		return agent.UsageSpeedFast
	case "ultrafast":
		return agent.UsageSpeedUltrafast
	default:
		return agent.UsageSpeedStandard
	}
}

func normalizeCodexToolName(raw string) string {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "exec_command", "shell":
		return "Bash"
	case "apply_patch":
		return "Edit"
	default:
		return raw
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	antigravityDefaultModel = "gemini-3.7-flash"
	antigravityMinInputTok  = 100
	antigravityMinOutputTok = 50
)

type antigravityUsageReader struct{}

// NewAntigravityUsageReader returns an agent.UsageReader for Google Antigravity
// CLI transcripts under .gemini/antigravity-cli/brain.
func NewAntigravityUsageReader() agent.UsageReader {
	return antigravityUsageReader{}
}

func (antigravityUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := resolveUsageHome(opts)
	if err != nil || strings.TrimSpace(home) == "" {
		return nil, err
	}

	roots := []string{filepath.Join(home, ".gemini", "antigravity-cli", "brain")}
	if opts.IsRealHome {
		if agyHome := strings.TrimSpace(os.Getenv("ANTIGRAVITY_HOME")); agyHome != "" {
			roots = append(roots, filepath.Join(expandUsageHomePath(agyHome, home), "brain"))
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
			sessionID := entry.Name()
			logsDir := filepath.Join(root, sessionID, ".system_generated", "logs")
			transcriptPath := filepath.Join(logsDir, "transcript.jsonl")
			fi, serr := os.Stat(transcriptPath)
			if serr != nil || fi.IsDir() {
				transcriptPath = filepath.Join(logsDir, "transcript_full.jsonl")
				fi, serr = os.Stat(transcriptPath)
				if serr != nil || fi.IsDir() {
					continue
				}
			}
			if opts.SinceMs > 0 && usageFileTooOld(fi, opts.SinceMs) {
				continue
			}

			state := newAntigravityUsageScanState()
			recs, scanErr := scanJSONLFileIncremental(ctx, transcriptPath, opts.Cache, jsonlScanHooks{
				RestoreState: func(data []byte) error {
					if len(data) == 0 {
						return nil
					}
					var restored antigravityUsageScanState
					if err := json.Unmarshal(data, &restored); err != nil {
						return err
					}
					if restored.CurrentModel == "" {
						restored.CurrentModel = antigravityDefaultModel
					}
					state = &restored
					return nil
				},
				SaveState: func() ([]byte, error) {
					return json.Marshal(state)
				},
				ParseLine: func(line []byte) ([]agent.UsageRecord, bool) {
					rec, ok := parseAntigravityUsageLine(line, sessionID, state)
					if !ok {
						return nil, false
					}
					return []agent.UsageRecord{rec}, false
				},
			})
			if scanErr != nil {
				continue
			}
			all = append(all, finalizeUsageRecords(recs, opts, transcriptPath, seenDedupe)...)
		}
	}

	return all, nil
}

type antigravityUsageScanState struct {
	CurrentUserMessage string `json:"current_user_message,omitempty"`
	CurrentModel       string `json:"current_model,omitempty"`
	CurrentProjectPath string `json:"current_project_path,omitempty"`
	ToolOutputsLen     int    `json:"tool_outputs_len,omitempty"`
}

func newAntigravityUsageScanState() *antigravityUsageScanState {
	return &antigravityUsageScanState{
		CurrentModel: antigravityDefaultModel,
	}
}

type antigravityTranscriptLine struct {
	Source    string                     `json:"source"`
	Type      string                     `json:"type"`
	CreatedAt any                        `json:"created_at"`
	Timestamp any                        `json:"timestamp"`
	Model     string                     `json:"model"`
	Content   string                     `json:"content"`
	Thinking  string                     `json:"thinking"`
	ToolCalls []antigravityToolCallEntry `json:"tool_calls"`
	Usage     *antigravityExplicitUsage  `json:"usage"`
	CostUSD   *float64                   `json:"cost_usd"`
}

type antigravityToolCallEntry struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type antigravityExplicitUsage struct {
	InputTokens           float64 `json:"input_tokens"`
	PromptTokens          float64 `json:"prompt_tokens"`
	OutputTokens          float64 `json:"output_tokens"`
	CompletionTokens      float64 `json:"completion_tokens"`
	CachedInputTokens     float64 `json:"cached_input_tokens"`
	CacheReadInputTokens  float64 `json:"cache_read_input_tokens"`
	CacheCreationTokens   float64 `json:"cache_creation_input_tokens"`
	ReasoningTokens       float64 `json:"reasoning_tokens"`
	ThinkingTokens        float64 `json:"thinking_tokens"`
}

func parseAntigravityUsageLine(
	line []byte,
	sessionID string,
	state *antigravityUsageScanState,
) (agent.UsageRecord, bool) {
	var entry antigravityTranscriptLine
	if err := json.Unmarshal(line, &entry); err != nil {
		return agent.UsageRecord{}, false
	}

	if entry.Source == "USER_EXPLICIT" || entry.Type == "USER_INPUT" || entry.Source == "USER" {
		if entry.Content != "" {
			if detected := detectAntigravityModel(entry.Content); detected != "" {
				state.CurrentModel = detected
			}
			state.CurrentUserMessage = extractAntigravityUserRequest(entry.Content)
			state.ToolOutputsLen = 0
		}
		return agent.UsageRecord{}, false
	}

	if entry.Source == "MODEL" && entry.Type == "GENERIC" {
		state.ToolOutputsLen += len(entry.Content)
		return agent.UsageRecord{}, false
	}

	isModelResponse := (entry.Source == "MODEL" && (entry.Type == "PLANNER_RESPONSE" ||
		len(entry.ToolCalls) > 0 ||
		entry.Thinking != "" ||
		entry.Usage != nil)) || entry.Usage != nil
	if !isModelResponse {
		return agent.UsageRecord{}, false
	}

	tsRaw := entry.CreatedAt
	if tsRaw == nil {
		tsRaw = entry.Timestamp
	}
	tsMs, ok := parseUsageTimestampMs(tsRaw)
	if !ok {
		return agent.UsageRecord{}, false
	}

	var tools []string
	for _, tc := range entry.ToolCalls {
		rawName := strings.TrimSpace(tc.Name)
		if rawName == "" {
			continue
		}
		tools = append(tools, normalizeAntigravityTool(rawName))
		if cwd := extractAntigravityCWD(tc.Args); cwd != "" {
			state.CurrentProjectPath = cwd
		}
	}

	model := strings.TrimSpace(entry.Model)
	if model == "" {
		model = state.CurrentModel
	}
	if model == "" {
		model = antigravityDefaultModel
	}

	var totals agent.UsageTokenTotals
	if entry.Usage != nil {
		u := entry.Usage
		inTok := usageTokenInt64(u.InputTokens)
		if inTok == 0 {
			inTok = usageTokenInt64(u.PromptTokens)
		}
		outTok := usageTokenInt64(u.OutputTokens)
		if outTok == 0 {
			outTok = usageTokenInt64(u.CompletionTokens)
		}
		cachedTok := usageTokenInt64(u.CachedInputTokens)
		if cachedTok == 0 {
			cachedTok = usageTokenInt64(u.CacheReadInputTokens)
		}
		cacheCreate := usageTokenInt64(u.CacheCreationTokens)
		reasoningTok := usageTokenInt64(u.ReasoningTokens)
		if reasoningTok == 0 {
			reasoningTok = usageTokenInt64(u.ThinkingTokens)
		}
		if reasoningTok > outTok {
			outTok += reasoningTok
		}
		totals = agent.UsageTokenTotals{
			UncachedInputTokens: inTok,
			CachedInputTokens:   cachedTok,
			CacheCreationTokens: cacheCreate,
			OutputTokens:        outTok,
			ReasoningTokens:     reasoningTok,
		}
	}

	if !totals.HasData() {
		reasoningTok := int64(len(entry.Thinking) / 4)
		toolCallsLen := 0
		if len(entry.ToolCalls) > 0 {
			if b, err := json.Marshal(entry.ToolCalls); err == nil {
				toolCallsLen = len(b)
			}
		}
		inTok := maxInt64(antigravityMinInputTok, int64((len(state.CurrentUserMessage)+state.ToolOutputsLen)/4))
		contentOut := maxInt64(antigravityMinOutputTok, int64((len(entry.Content)+toolCallsLen)/4))
		totals = agent.UsageTokenTotals{
			UncachedInputTokens: inTok,
			OutputTokens:        contentOut + reasoningTok,
			ReasoningTokens:     reasoningTok,
		}
	}

	state.ToolOutputsLen = 0

	projectPath := state.CurrentProjectPath
	project := agent.ProjectNameFromPath(projectPath)
	if project == "" && sessionID != "" {
		project = "brain-" + sessionID
	}

	return agent.UsageRecord{
		Provider:        "antigravity",
		TimestampMs:     tsMs,
		Model:           model,
		RateModel:       antigravityRateModel(model),
		SessionID:       sessionID,
		Project:         project,
		ProjectPath:     projectPath,
		Totals:          totals,
		ReportedCostUSD: usageFloatPtrIfNonNeg(entry.CostUSD),
		Speed:           agent.UsageSpeedStandard,
		Tools:           tools,
	}, true
}

// antigravityRateModel maps Antigravity effort/routing/orchestrator variants
// onto their base pricing model ID (matching t3code's antigravityRateModel).
func antigravityRateModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	switch m {
	case "gemini-3-flash-agent", "gemini-3-flash-agent-a", "gemini-3-flash-agent-b", "gemini-3-flash-a", "gemini-3-flash-b":
		return "gemini-3.5-flash"
	case "gemini-3.7-flash-safety-le":
		return "gemini-3.7-flash"
	case "gemini-pro-orchestrator-neon":
		return "gemini-4-neon"
	case "gemini-pro-orchestrator-g1-pro":
		return "gemini-4-argon"
	}
	base := m
	for _, suffix := range []string{"-extra-low", "-low", "-medium", "-high", "-tiered", "-control", "-thinking", "-agent", "-orchestrator"} {
		if strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
			break
		}
	}
	if base == "gemini-3-flash" {
		return "gemini-3-flash-preview"
	}
	return base
}

func extractAntigravityUserRequest(raw string) string {
	const startTag = "<USER_REQUEST>"
	const endTag = "</USER_REQUEST>"
	if start := strings.Index(raw, startTag); start >= 0 {
		after := raw[start+len(startTag):]
		if end := strings.Index(after, endTag); end >= 0 {
			return strings.TrimSpace(after[:end])
		}
	}
	return strings.TrimSpace(raw)
}

func detectAntigravityModel(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "gemini-3.7-flash"), strings.Contains(lower, "gemini 3.7 flash"), strings.Contains(lower, "3.7-flash"):
		return "gemini-3.7-flash"
	case strings.Contains(lower, "gemini-2.5-pro"), strings.Contains(lower, "gemini 2.5 pro"), strings.Contains(lower, "2.5-pro"):
		return "gemini-2.5-pro"
	case strings.Contains(lower, "gemini-2.5-flash"), strings.Contains(lower, "gemini 2.5 flash"), strings.Contains(lower, "2.5-flash"):
		return "gemini-2.5-flash"
	default:
		return ""
	}
}

func normalizeAntigravityTool(raw string) string {
	switch raw {
	case "run_command":
		return "Bash"
	case "view_file", "read_url_content", "read_browser_page":
		return "Read"
	case "write_to_file", "replace_file_content", "notebook_edit":
		return "Edit"
	case "list_dir", "find_by_name", "grep_search", "search_web":
		return "Glob"
	case "send_message":
		return "Agent"
	default:
		return raw
	}
}

func extractAntigravityCWD(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"Cwd", "cwd", "directory", "path"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
		if s != "" {
			return s
		}
	}
	return ""
}

package core

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	agyTranscriptFirstLineLimit = 64 * 1024
	agyTranscriptTailLimit      = 64 * 1024
)

var (
	agySystemSenderRe  = regexp.MustCompile(`\[Message\][^\n]*\bsender=([^\s\]]+)`)
	agySystemContentRe = regexp.MustCompile(`\[Message\][^\n]*\bcontent=(.+)`)
	agyWorktreeSubRe   = regexp.MustCompile(`[/\\]worktrees[/\\]([0-9a-fA-F-]{8,})[/\\]subagent-([^/\\]+)`)
)

type antigravitySubagentMeta struct {
	IsSubagent bool
	ParentID   string
	AgentName  string
	PromptHint string
}

func isAntigravityBackgroundTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "invoke_subagent", "run_command", "run_workflow", "schedule":
		return true
	default:
		return false
	}
}

func antigravityTranscriptPath(payload map[string]any) string {
	if p := firstStr(payload, "transcriptPath", "transcript_path"); p != "" {
		return p
	}
	if dir := firstStr(payload, "artifactDirectoryPath", "artifact_directory_path"); dir != "" {
		return filepath.Join(dir, ".system_generated", "logs", "transcript.jsonl")
	}
	return ""
}

// antigravityAppDataDir derives the Antigravity appDataDir (containing
// conversation_summaries.db) from transcriptPath or artifactDirectoryPath.
func antigravityAppDataDir(payload map[string]any) string {
	for _, raw := range []string{
		firstStr(payload, "artifactDirectoryPath", "artifact_directory_path"),
		firstStr(payload, "transcriptPath", "transcript_path"),
	} {
		if raw == "" {
			continue
		}
		cleaned := filepath.Clean(raw)
		needle := string(filepath.Separator) + "brain" + string(filepath.Separator)
		if idx := strings.LastIndex(cleaned, needle); idx > 0 {
			return cleaned[:idx]
		}
	}
	return ""
}

// inspectAntigravitySubagent determines whether an Antigravity hook payload
// belongs to a subagent conversation when Antigravity omits parentConversationId
// from the hook JSON. It checks, in order:
//  1. worktree cwd pattern (.../worktrees/<parent>/subagent-<label>)
//  2. step 0 of transcriptPath (SYSTEM_MESSAGE with sender=<parent> vs USER_INPUT)
//  3. conversation_summaries.db (parent_conversation_id / nesting_depth / agent_name)
func inspectAntigravitySubagent(payload map[string]any) antigravitySubagentMeta {
	var meta antigravitySubagentMeta
	if payload == nil {
		return meta
	}

	// 1. Zero-I/O cwd worktree check (subagents launched with Workspace: branch/share).
	if cwd := firstStr(payload, "cwd", "working_directory", "workingDirectory", "workspaceRoot"); cwd != "" {
		if m := agyWorktreeSubRe.FindStringSubmatch(cwd); len(m) == 3 {
			meta.IsSubagent = true
			meta.ParentID = strings.TrimSpace(m[1])
			meta.PromptHint = cleanWorktreeSubagentLabel(m[2])
		}
	}

	// 2. Step 0 of transcript.jsonl: root conversations start with USER_INPUT;
	// subagents start with a SYSTEM_MESSAGE carrying sender=<parentConversationId>.
	rootConfirmed := false
	if tp := antigravityTranscriptPath(payload); tp != "" {
		isSub, isRoot, parentID, hint := readAntigravityTranscriptStep0(tp)
		if isRoot && !meta.IsSubagent {
			rootConfirmed = true
		}
		if isSub {
			meta.IsSubagent = true
			if parentID != "" {
				meta.ParentID = parentID
			}
			if meta.PromptHint == "" && hint != "" {
				meta.PromptHint = hint
			}
		}
	}

	if rootConfirmed {
		return meta
	}

	// 3. Fallback / enrichment via conversation_summaries.db (handles SessionStart
	// before transcript.jsonl is created, and fills agent_name like "self"/"research").
	convID := firstStr(payload, "conversationId", "conversation_id", "session_id", "sessionId")
	appDir := antigravityAppDataDir(payload)
	if convID != "" && appDir != "" {
		dbPath := filepath.Join(appDir, "conversation_summaries.db")
		if parentID, nestingDepth, agentName, ok := lookupAntigravitySummaryParent(dbPath, convID); ok {
			if parentID != "" || nestingDepth > 0 || agentName != "" {
				meta.IsSubagent = true
				if parentID != "" && meta.ParentID == "" {
					meta.ParentID = parentID
				}
				if agentName != "" && meta.AgentName == "" {
					meta.AgentName = agentName
				}
			}
		}
	}

	return meta
}

func cleanWorktreeSubagentLabel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "-")
	// Strip trailing 8-char hex hash if present (e.g. "My-Role-self-4bb86977").
	if len(parts) > 1 && isHexToken(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	// Strip trailing agent type token like "self" or "research" if followed by role words.
	if len(parts) > 1 {
		last := strings.ToLower(parts[len(parts)-1])
		if last == "self" || last == "research" || last == "general" {
			parts = parts[:len(parts)-1]
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func isHexToken(s string) bool {
	if len(s) < 6 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func readAntigravityTranscriptStep0(path string) (isSubagent, isRoot bool, parentID, hint string) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, "", ""
	}
	defer f.Close()

	r := bufio.NewReaderSize(io.LimitReader(f, agyTranscriptFirstLineLimit), 8192)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return false, false, "", ""
	}
	var step struct {
		StepIndex int    `json:"step_index"`
		Source    string `json:"source"`
		Type      string `json:"type"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(line, &step); err != nil {
		return false, false, "", ""
	}
	if step.StepIndex != 0 {
		return false, false, "", ""
	}
	src := strings.ToUpper(strings.TrimSpace(step.Source))
	typ := strings.ToUpper(strings.TrimSpace(step.Type))
	if src == "USER_EXPLICIT" || typ == "USER_INPUT" {
		return false, true, "", ""
	}
	if src == "SYSTEM" || typ == "SYSTEM_MESSAGE" {
		if m := agySystemSenderRe.FindStringSubmatch(step.Content); len(m) == 2 {
			parent := strings.TrimSpace(m[1])
			var contentHint string
			if cm := agySystemContentRe.FindStringSubmatch(step.Content); len(cm) == 2 {
				contentHint = strings.TrimSpace(cm[1])
			}
			return true, false, parent, contentHint
		}
	}
	return false, false, "", ""
}

func lookupAntigravitySummaryParent(dbPath, convID string) (parentID string, nestingDepth int, agentName string, ok bool) {
	if _, err := os.Stat(dbPath); err != nil {
		return "", 0, "", false
	}
	u := url.URL{Scheme: "file", Path: dbPath}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(200)")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", 0, "", false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var pID, aName sql.NullString
	var depth sql.NullInt64
	err = db.QueryRowContext(
		ctx,
		`SELECT COALESCE(parent_conversation_id, ''), COALESCE(nesting_depth, 0), COALESCE(agent_name, '') FROM conversation_summaries WHERE conversation_id = ? LIMIT 1`,
		convID,
	).Scan(&pID, &depth, &aName)
	if err != nil {
		return "", 0, "", false
	}
	return strings.TrimSpace(pID.String), int(depth.Int64), strings.TrimSpace(aName.String), true
}

// antigravityStepToolOutput reads the tool result content for an Antigravity
// PostToolUse event from transcriptPath when PostToolUse omits toolOutput.
func antigravityStepToolOutput(payload map[string]any, tool string) string {
	tp := antigravityTranscriptPath(payload)
	if tp == "" {
		return ""
	}
	stepIdx := parseOptionalInt(payload, "stepIdx", "step_idx", "stepIndex", "step_index")
	f, err := os.Open(tp)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 {
		return ""
	}
	start := fi.Size() - agyTranscriptTailLimit
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, agyTranscriptTailLimit+1024))
	if err != nil || len(raw) == 0 {
		return ""
	}
	text := string(raw)
	// If we seeked mid-line, drop the partial first line.
	if start > 0 {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			text = text[nl+1:]
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	type transcriptStep struct {
		StepIndex int    `json:"step_index"`
		Type      string `json:"type"`
		Content   string `json:"content"`
	}
	if stepIdx != nil {
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var st transcriptStep
			if err := json.Unmarshal([]byte(line), &st); err != nil {
				continue
			}
			if st.StepIndex == *stepIdx {
				return st.Content
			}
		}
		return ""
	}
	// Fallback when stepIdx is absent: inspect only the most recent GENERIC step.
	toolLower := strings.ToLower(strings.TrimSpace(tool))
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-4; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var st transcriptStep
		if err := json.Unmarshal([]byte(line), &st); err != nil {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(st.Type)) != "GENERIC" || st.Content == "" {
			continue
		}
		if toolLower == "invoke_subagent" && strings.Contains(st.Content, "conversationId") {
			return st.Content
		}
		if (toolLower == "run_command" || toolLower == "run_workflow" || toolLower == "schedule") &&
			agyBgTaskIDRe.MatchString(st.Content) {
			return st.Content
		}
	}
	return ""
}

// promoteAntigravitySubagentFromRecord is a pane-state safety net for
// Antigravity subagent events whose parentConversationId could not be detected
// from the payload or disk yet (e.g. a subagent SessionStart firing before
// transcript.jsonl or conversation_summaries.db flushes).
func promoteAntigravitySubagentFromRecord(existing, event map[string]any, now int64) map[string]any {
	if existing == nil || event == nil {
		return event
	}
	if child, ok := event["child"].(map[string]any); ok && child != nil {
		return event
	}
	if stringKey(event["agent"]) != "antigravity" || stringKey(existing["agent"]) != "antigravity" {
		return event
	}
	evSID := strings.TrimSpace(stringKey(event["session_id"]))
	recSID := strings.TrimSpace(stringKey(existing["session_id"]))
	if evSID == "" || recSID == "" || evSID == recSID {
		return event
	}

	knownChild := false
	hasSyntheticActive := false
	if children, ok := existing["children"].([]any); ok {
		for _, raw := range children {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if stringKey(m["id"]) == evSID {
				knownChild = true
				break
			}
			if parseBoolField(m, "synthetic_id") && isActiveBgState(stringKey(m["state"])) {
				hasSyntheticActive = true
			}
		}
	}
	evName := stringKey(event["event"])
	// Only infer a new subagent from a working parent when the parent has a
	// pending synthetic invoke_subagent entry or is actively waiting on
	// background tasks (and not on an explicit user prompt).
	inferredSubagent := !knownChild &&
		evName != "prompt" &&
		stringKey(existing["state"]) == "working" &&
		(hasSyntheticActive || truthy(existing["waiting_on"]))

	if !knownChild && !inferredSubagent {
		return event
	}

	if now <= 0 {
		now = toInt64(event["ts_ms"])
		if now <= 0 {
			now = nowMs()
		}
	}
	childState := "working"
	outcome := ""
	var endedMs int64
	if evName == "stop" || evName == "session_end" || evName == "stop_failure" {
		if !(evName == "stop" && parseBoolField(event, "pending_background")) {
			if evName == "stop_failure" || stringKey(event["state"]) == "failed" {
				childState = "failed"
				outcome = "failed"
			} else {
				childState = "done"
				outcome = "done"
			}
			endedMs = now
		}
	}

	out := make(map[string]any, len(event)+1)
	for k, v := range event {
		switch k {
		case "background_tasks", "children_updates", "background_authoritative",
			"crons_authoritative", "finite_authoritative", "pending_background", "waiting_on":
			continue
		}
		out[k] = v
	}
	out["child"] = map[string]any{
		"id":            truncateRunes(evSID, MaxSession),
		"type":          "",
		"kind":          BgKindSubagent,
		"category":      BgCategoryFinite,
		"state":         childState,
		"outcome":       outcome,
		"text":          "",
		"preserve_text": true,
		"since_ms":      now,
		"ts_ms":         now,
		"ended_ms":      endedMs,
	}
	return out
}

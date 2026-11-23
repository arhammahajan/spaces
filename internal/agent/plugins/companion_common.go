package plugins

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arhammahajan/spaces/internal/agent"
	pluginsfs "github.com/arhammahajan/spaces/plugins"
)

// Shared helpers for the agent-specific CompanionPlugin implementations
// (opencode/pi/claude/codex). This file is deliberately free of any import
// of internal/hooks/core: normalized events are plain map[string]any values
// following the canonical v2 schema so cmd/tmux-spaces can wire them as
// hookcore.CompanionNormalizer without an import cycle.

// Truncation budgets mirror the JS/TS bridges (MAX_TEXT/MAX_FIELD/MAX_OUTPUT)
// and the spacecore v2 field limits.
const (
	companionMaxText    = 240
	companionMaxField   = 4096
	companionMaxOutput  = 2048
	companionMaxModel   = 200
	companionMaxSession = 200
	companionMaxCWD     = 1000
)

// ---- envelope field extraction ----

// companionPayload returns envelope["payload"] when it is a map, else the
// envelope itself (bridges always send {v,agent,session,pane,plugin,event,
// ts_ms,pid,harness_version,payload}).
func companionPayload(envelope map[string]any) map[string]any {
	if envelope == nil {
		return map[string]any{}
	}
	if p, ok := envelope["payload"].(map[string]any); ok && p != nil {
		return p
	}
	return envelope
}

func companionStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// companionMergedStr looks keys up in the envelope first, then the payload.
func companionMergedStr(env, payload map[string]any, keys ...string) string {
	if s := companionStr(env, keys...); s != "" {
		return s
	}
	return companionStr(payload, keys...)
}

func companionMerged(env, payload map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := env[k]; ok && v != nil && v != "" {
			return v
		}
	}
	for _, k := range keys {
		if v, ok := payload[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

func companionMergedMap(env, payload map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if m, ok := env[k].(map[string]any); ok && m != nil {
			return m
		}
	}
	for _, k := range keys {
		if m, ok := payload[k].(map[string]any); ok && m != nil {
			return m
		}
	}
	return nil
}

func companionMergedSlice(env, payload map[string]any, keys ...string) []any {
	for _, k := range keys {
		if sl, ok := env[k].([]any); ok && sl != nil {
			return sl
		}
	}
	for _, k := range keys {
		if sl, ok := payload[k].([]any); ok && sl != nil {
			return sl
		}
	}
	return nil
}

func companionInt64(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		if n != n || n > 9e18 || n < -9e18 {
			return 0
		}
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

func companionFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if n != n {
			return 0, false
		}
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
		return 0, false
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func companionBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		s := strings.ToLower(strings.TrimSpace(b))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return b != 0
	default:
		return false
	}
}

func companionNowMs() int64 { return time.Now().UnixMilli() }

// oneLineCompanion stringifies v (JSON for non-strings), squashes
// whitespace and truncates to limit runes with an ellipsis suffix.
func oneLineCompanion(v any, limit int) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		s = t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			s = fmt.Sprint(t)
		} else {
			s = string(b)
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

// boundCompanionSlice caps a slice at max entries (keeps the tail).
func boundCompanionSlice(in []any, max int) []any {
	if in == nil {
		return nil
	}
	if len(in) > max {
		return in[len(in)-max:]
	}
	return in
}

// normalizeCompanionUsage extracts the canonical usage map
// (input/output/reasoning/cache tokens + cost_usd), accepting both
// snake_case and camelCase keys. Returns nil when empty.
func normalizeCompanionUsage(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	get := func(keys ...string) int64 {
		for _, k := range keys {
			if n := companionInt64(m[k]); n != 0 {
				return n
			}
		}
		return 0
	}
	in := get("input_tokens", "inputTokens", "input")
	out := get("output_tokens", "outputTokens", "output")
	reason := get("reasoning_tokens", "reasoningTokens", "reasoning")
	cacheRead := get("cache_read_tokens", "cacheReadTokens", "cache_read_input_tokens", "cached_tokens", "cachedTokens")
	cacheWrite := get("cache_write_tokens", "cacheWriteTokens", "cache_creation_input_tokens")
	var cost float64
	for _, k := range []string{"cost_usd", "costUsd", "costUSD", "cost"} {
		if f, ok := companionFloat(m[k]); ok && f > 0 {
			cost = f
			break
		}
	}
	var total int64
	for _, k := range []string{"total_tokens", "totalTokens", "total"} {
		if n := companionInt64(m[k]); n != 0 {
			total = n
			break
		}
	}
	if in == 0 && out == 0 && reason == 0 && cacheRead == 0 && cacheWrite == 0 && cost == 0 && total == 0 {
		return nil
	}
	res := map[string]any{"input_tokens": in, "output_tokens": out}
	if reason != 0 {
		res["reasoning_tokens"] = reason
	}
	if cacheRead != 0 {
		res["cache_read_tokens"] = cacheRead
	}
	if cacheWrite != 0 {
		res["cache_write_tokens"] = cacheWrite
	}
	if total != 0 {
		res["total_tokens"] = total
	}
	if cost > 0 {
		res["cost_usd"] = cost
	}
	return res
}

// toolTextCompanion renders a compact one-line fragment of a tool input,
// mirroring the bridges' toolTextFragment helpers.
func toolTextCompanion(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return oneLineCompanion(s, companionMaxField)
	}
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"command", "CommandLine", "Command", "file_path", "path", "pattern", "url", "query", "description", "prompt", "content"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return oneLineCompanion(strings.TrimSpace(s), companionMaxField)
			}
		}
	}
	return oneLineCompanion(v, companionMaxField)
}

// normalizeCompanionState canonicalizes an explicit state string from a
// payload ("error"->"failed", "waiting"->"blocked").
func normalizeCompanionState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return "failed"
	case "waiting":
		return "blocked"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// baseCompanionEvent builds the canonical v2 event shell shared by every
// agent normalizer: plugin stamps, identity/session fields, usage, numeric
// metrics, bounded activity slices and subagent routing markers. Callers
// set event/state/kind/text specifics on top.
func baseCompanionEvent(spec agent.CompanionSpec, capabilities []string, envelope map[string]any) (map[string]any, map[string]any) {
	payload := companionPayload(envelope)
	if envelope == nil {
		envelope = map[string]any{}
	}

	rawEvent := companionMergedStr(envelope, payload, "event", "hook_event_name", "hookEventName", "type")

	ts := companionInt64(companionMerged(envelope, payload, "ts_ms", "tsMs", "timestamp_ms", "timestampMs", "timestamp"))
	if ts <= 0 {
		ts = companionNowMs()
	}
	pluginTs := companionInt64(companionMerged(envelope, payload, "plugin_ts_ms", "pluginTsMs"))
	if pluginTs <= 0 {
		pluginTs = ts
	}

	pluginName := companionMergedStr(envelope, payload, "plugin_name", "pluginName")
	if pluginName == "" {
		if pm := companionMergedMap(envelope, payload, "plugin"); pm != nil {
			pluginName = companionStr(pm, "name")
		}
	}
	if pluginName == "" {
		pluginName = spec.PluginName
	}
	pluginVersion := companionMergedStr(envelope, payload, "plugin_version", "pluginVersion")
	if pluginVersion == "" {
		if pm := companionMergedMap(envelope, payload, "plugin"); pm != nil {
			pluginVersion = companionStr(pm, "version")
		}
	}
	if pluginVersion == "" {
		pluginVersion = spec.Version
	}
	var pluginCaps []any
	if pm := companionMergedMap(envelope, payload, "plugin"); pm != nil {
		if caps, ok := pm["capabilities"].([]any); ok {
			pluginCaps = caps
		}
	}
	if pluginCaps == nil {
		pluginCaps = make([]any, 0, len(capabilities))
		for _, c := range capabilities {
			pluginCaps = append(pluginCaps, c)
		}
	}

	text := companionMergedStr(envelope, payload, "text")
	if text == "" {
		text = oneLineCompanion(companionMerged(envelope, payload, "message", "body", "title"), companionMaxText)
	}
	if text == "" {
		if tool := companionMergedStr(envelope, payload, "tool", "tool_name", "toolName"); tool != "" {
			text = tool
		} else {
			text = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(rawEvent)), "_", " ")
			if text == "" {
				text = "working"
			}
		}
	}

	var sessionID any
	if sid := companionMergedStr(envelope, payload, "session_id", "sessionId", "sessionID", "thread_id", "threadId", "conversation_id", "conversationId"); sid != "" {
		sessionID = sid
	}
	var cwd any
	if c := companionMergedStr(envelope, payload, "cwd", "working_directory", "workingDirectory", "workspaceRoot"); c != "" {
		cwd = c
	}

	out := map[string]any{
		"v":           2,
		"agent":       spec.AgentID,
		"app":         spec.AgentID,
		"source":      "plugin",
		"event":       strings.ToLower(strings.TrimSpace(rawEvent)),
		"raw_event":   oneLineCompanion(rawEvent, companionMaxModel),
		"state":       "working",
		"kind":        nil,
		"text":        oneLineCompanion(text, companionMaxText),
		"title":       strOrNil(oneLineCompanion(companionMerged(envelope, payload, "title", "conversation_title", "conversationTitle", "session_title", "sessionTitle"), companionMaxText)),
		"prompt":      strOrNil(oneLineCompanion(companionMerged(envelope, payload, "prompt", "user_prompt", "userPrompt", "lastUserInput"), companionMaxField)),
		"tool":        truncateCompanion(companionMergedStr(envelope, payload, "tool", "tool_name", "toolName"), companionMaxModel),
		"tool_input":  strOrNil(toolTextCompanion(companionMerged(envelope, payload, "tool_input", "toolInput", "input", "arguments", "args"))),
		"tool_output": strOrNil(oneLineCompanion(companionMerged(envelope, payload, "tool_output", "toolOutput", "tool_result", "toolResult", "output", "result"), companionMaxOutput)),
		"reply":       strOrNil(oneLineCompanion(companionMerged(envelope, payload, "reply", "last_assistant_message", "lastAssistantMessage", "assistant_message"), companionMaxField)),
		"model":       strOrNil(truncateCompanion(companionMergedStr(envelope, payload, "model", "model_name", "modelName"), companionMaxModel)),
		"session_id":  sessionID,
		"cwd":         cwd,
		"background":  int64(0),
		"ts_ms":       ts,
		"seq":         time.Now().UnixNano(),
	}

	out["plugin_name"] = pluginName
	out["plugin_version"] = pluginVersion
	out["plugin_ts_ms"] = pluginTs
	out["plugin_capabilities"] = pluginCaps
	if pid := companionInt64(companionMerged(envelope, payload, "pid")); pid > 0 {
		out["pid"] = pid
	}
	if hv := companionMergedStr(envelope, payload, "harness_version", "harnessVersion"); hv != "" {
		out["harness_version"] = hv
	}

	for _, k := range []string{"provider", "effort", "mode", "sandbox_mode", "git_branch"} {
		if s := companionMergedStr(envelope, payload, k); s != "" {
			out[k] = truncateCompanion(s, companionMaxModel)
		}
	}
	if p := companionMergedStr(envelope, payload, "profile"); p != "" {
		out["profile"] = truncateCompanion(p, companionMaxModel)
	}

	if u := normalizeCompanionUsage(payload["usage"]); u != nil {
		out["usage"] = u
	} else if u := normalizeCompanionUsage(envelope["usage"]); u != nil {
		out["usage"] = u
	}
	if u, ok := out["usage"].(map[string]any); ok {
		if cost, ok := u["cost_usd"].(float64); ok && cost > 0 {
			out["cost_usd"] = cost
		}
	}

	for _, k := range []string{"context_tokens", "context_window", "turn_count", "compaction_count", "last_compaction_ms", "tool_call_count", "tool_error_count", "retry_count", "rate_limit_reset_ms"} {
		if n := companionInt64(companionMerged(envelope, payload, k)); n != 0 {
			out[k] = n
		}
	}
	if f, ok := companionFloat(companionMerged(envelope, payload, "context_percent", "contextPercent")); ok && f != 0 {
		out["context_percent"] = f
	}
	if companionInt64(out["context_tokens"]) > 0 && companionInt64(out["context_window"]) > 0 {
		if _, has := out["context_percent"]; !has {
			out["context_percent"] = float64(companionInt64(out["context_tokens"])) / float64(companionInt64(out["context_window"])) * 100
		}
	}
	if s := companionMergedStr(envelope, payload, "last_compaction_reason"); s != "" {
		out["last_compaction_reason"] = oneLineCompanion(s, companionMaxField)
	}
	if companionBool(companionMerged(envelope, payload, "rate_limited", "rateLimited")) {
		out["rate_limited"] = true
	}
	if s := companionMergedStr(envelope, payload, "rate_limit_message", "rateLimitMessage"); s != "" {
		out["rate_limit_message"] = oneLineCompanion(s, companionMaxField)
	}

	if sl := companionMergedSlice(envelope, payload, "active_tools", "activeTools"); sl != nil {
		out["active_tools"] = boundCompanionSlice(sl, 16)
	}
	if sl := companionMergedSlice(envelope, payload, "recent_tools", "recentTools"); sl != nil {
		out["recent_tools"] = boundCompanionSlice(sl, 16)
	}
	if sl := companionMergedSlice(envelope, payload, "files_touched", "filesTouched"); sl != nil {
		out["files_touched"] = boundCompanionSlice(sl, 32)
	}
	if sl := companionMergedSlice(envelope, payload, "todos"); sl != nil {
		out["todos"] = boundCompanionSlice(sl, 32)
	}
	if sl := companionMergedSlice(envelope, payload, "mcp_servers", "mcpServers"); sl != nil {
		out["mcp_servers"] = boundCompanionSlice(sl, 32)
	}
	if v := companionMerged(envelope, payload, "lsp_diagnostics", "lspDiagnostics"); v != nil {
		out["lsp_diagnostics"] = v
	}
	if v := companionMerged(envelope, payload, "pending_permission", "pendingPermission"); v != nil {
		out["pending_permission"] = v
	}
	if s := companionMergedStr(envelope, payload, "last_error", "lastError"); s != "" {
		out["last_error"] = oneLineCompanion(s, companionMaxField)
	}
	if m := companionMergedMap(envelope, payload, "extras"); m != nil {
		out["extras"] = m
	}

	if companionBool(companionMerged(envelope, payload, "is_subagent", "isSubagent")) {
		out["is_subagent"] = true
	}
	if s := companionMergedStr(envelope, payload, "parent_session_id", "parentSessionId", "parent_sessionId"); s != "" {
		out["parent_session_id"] = truncateCompanion(s, companionMaxSession)
	}
	if sl := companionMergedSlice(envelope, payload, "subagents"); sl != nil {
		out["subagents"] = boundCompanionSlice(sl, 32)
	}
	if v := companionMerged(envelope, payload, "background_tasks", "backgroundTasks"); v != nil {
		out["background_tasks"] = v
	}

	return out, payload
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truncateCompanion(s string, limit int) string {
	if s == "" {
		return ""
	}
	if limit > 0 && utf8.RuneCountInString(s) > limit {
		runes := []rune(s)
		return string(runes[:limit-1]) + "…"
	}
	return s
}

// ---- install/uninstall/detect helpers ----

// companionHome resolves the home directory for install/uninstall/status:
// explicit HomeDir wins, else os.UserHomeDir, else $HOME.
func companionHome(opts agent.CompanionInstallOptions) string {
	if h := strings.TrimSpace(opts.HomeDir); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return os.Getenv("HOME")
}

// companionTargetDir resolves the plugin install directory: explicit
// InstallDir wins, else <home>/<spec.InstallDir>.
func companionTargetDir(opts agent.CompanionInstallOptions, spec agent.CompanionSpec) string {
	if d := strings.TrimSpace(opts.InstallDir); d != "" {
		return d
	}
	return filepath.Join(companionHome(opts), filepath.FromSlash(spec.InstallDir))
}

// copyEmbedTree copies an embedded source tree (forward-slash rooted at
// srcRoot inside pluginsfs.FS) into dstDir. When markExec is true, files
// under scripts/ ending in .mjs get the owner/group/other execute bits
// (embed strips them) so hook bridges stay directly runnable.
func copyEmbedTree(srcRoot, dstDir string, markExec bool) error {
	return fs.WalkDir(pluginsfs.FS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, srcRoot), "/")
		target := dstDir
		if rel != "" {
			target = filepath.Join(dstDir, filepath.FromSlash(rel))
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := pluginsfs.FS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if markExec && strings.HasSuffix(p, ".mjs") && strings.Contains("/"+p+"/", "/scripts/") {
			perm = 0o755
		}
		if err := os.WriteFile(target, data, perm); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		if markExec && perm == 0o755 {
			_ = os.Chmod(target, perm)
		}
		return nil
	})
}

// readEmbedVersion reads the version field from an embedded package.json.
func readEmbedVersion(srcRoot string) string {
	data, err := pluginsfs.FS.ReadFile(srcRoot + "/package.json")
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if v, ok := m["version"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// readInstalledVersion reads the version field from an installed
// package.json; empty when missing or unparsable.
func readInstalledVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if v, ok := m["version"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// writeFileAtomic writes data to path via temp-file + rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// readJSONFile reads a JSON object file; missing/unparsable files yield an
// empty map and no error (callers must never fail hard on user config).
func readJSONFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// inferSubagent marks out["is_subagent"] when the payload carries child
// routing signals even if the explicit flag is absent, mirroring the JS/TS
// bridges (agent_id presence, or a parent session id that differs from the
// event's own session id).
func inferSubagent(out, payload map[string]any) {
	if companionBool(out["is_subagent"]) {
		return
	}
	if s := companionStr(payload, "agent_id", "agentId"); s != "" {
		out["is_subagent"] = true
		return
	}
	parent := companionStr(payload,
		"parent_session_id", "parentSessionId",
		"parent_thread_id", "parentThreadId",
		"parent_conversation_id", "parentConversationId",
		"root_conversation_id", "rootConversationId")
	if parent == "" {
		return
	}
	if sid := companionStr(payload, "session_id", "sessionId", "sessionID", "thread_id", "threadId", "conversation_id", "conversationId"); sid != "" && parent == sid {
		// Same-id parent echoes (e.g. Claude SubagentStart) still mark the
		// event as subagent-scoped when an agent identity is present; the
		// per-agent normalizer handles those via agent_id/agent_type.
		return
	}
	out["is_subagent"] = true
}

// marking Updated when the directory is new or the version changed.
func installResult(dir, prevVersion, newVersion string, created bool) agent.CompanionInstallResult {
	if newVersion == "" {
		newVersion = prevVersion
	}
	return agent.CompanionInstallResult{
		Path:    dir,
		Version: newVersion,
		Updated: created || prevVersion == "" || (newVersion != "" && prevVersion != newVersion),
	}
}

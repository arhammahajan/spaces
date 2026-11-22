package core

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Background task categories used by ATT-5 (completion arbitration) and ATT-9
// (inline background work display).
const (
	BgCategoryFinite   = "finite"
	BgCategoryService  = "service"
	BgCategoryWatcher  = "watcher"
	BgCategorySchedule = "schedule"
)

// Background task kinds describing what is running.
const (
	BgKindSubagent = "subagent"
	BgKindCommand  = "command"
	BgKindService  = "service"
	BgKindWatcher  = "watcher"
	BgKindSchedule = "schedule"
)

// ClassifyBackgroundTask classifies a background task into a lifecycle category
// (finite vs service/watcher/schedule) and display kind.
//
// Finite work (subagents, tests, builds, scripts) keeps a pane working on Stop
// until it completes. Services, watchers, and scheduled wakeups are tracked in
// background details without blocking turn completion.
func ClassifyBackgroundTask(rawType, command, description string, persistent, fromCron bool) (category, kind string) {
	t := strings.ToLower(strings.TrimSpace(rawType))
	if fromCron {
		return BgCategorySchedule, BgKindSchedule
	}
	switch t {
	case "cron", "schedule", "scheduled", "wakeup", "timer", "loop",
		"session_cron", "remote_cron", "scheduled_wakeup", "cron_job":
		return BgCategorySchedule, BgKindSchedule
	case "subagent", "agent", "local_agent", "remote_agent",
		"in_process_teammate", "teammate", "task":
		return BgCategoryFinite, BgKindSubagent
	case "watcher", "watch", "monitor", "stream", "tail":
		return BgCategoryWatcher, BgKindWatcher
	case "service", "server", "daemon", "proxy", "tunnel", "database", "db":
		return BgCategoryService, BgKindService
	}
	if persistent {
		return BgCategoryService, BgKindService
	}

	// Inspect command first, then description when no command was provided.
	if cmd := strings.TrimSpace(command); cmd != "" {
		if cat, knd, matched := classifyCommand(cmd); matched {
			return cat, knd
		}
	}
	if desc := strings.TrimSpace(description); desc != "" {
		if cat, knd, matched := classifyDescription(desc); matched {
			return cat, knd
		}
	}
	return BgCategoryFinite, BgKindCommand
}

// classifyCommand checks shell command segments for known watcher or
// long-running service invocations. Returns matched=false for finite commands.
func classifyCommand(command string) (category, kind string, matched bool) {
	segments := splitShellSegments(command)
	// Evaluate segments from last to first (so `cd app && npm run dev` matches
	// `npm run dev`, while `npm run build && echo done` stays finite).
	for i := len(segments) - 1; i >= 0; i-- {
		tokens := strings.Fields(segments[i])
		tokens = stripLeadingShellWrappers(tokens)
		if len(tokens) == 0 {
			continue
		}
		if cat, knd, ok := classifyTokens(tokens); ok {
			return cat, knd, true
		}
	}
	return "", "", false
}

func splitShellSegments(cmd string) []string {
	var parts []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			cur.WriteByte(c)
			continue
		}
		if inDouble {
			if c == '\\' && i+1 < len(cmd) {
				cur.WriteByte(c)
				cur.WriteByte(cmd[i+1])
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			cur.WriteByte(c)
		case '"':
			inDouble = true
			cur.WriteByte(c)
		case ';', '\n':
			if s := strings.TrimSpace(cur.String()); s != "" {
				parts = append(parts, s)
			}
			cur.Reset()
		case '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				if s := strings.TrimSpace(cur.String()); s != "" {
					parts = append(parts, s)
				}
				cur.Reset()
				i++
			} else {
				cur.WriteByte(c)
			}
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				if s := strings.TrimSpace(cur.String()); s != "" {
					parts = append(parts, s)
				}
				cur.Reset()
				i++
			} else {
				// Pipe segment (e.g. `tail -f app.log | grep ERR`)
				if s := strings.TrimSpace(cur.String()); s != "" {
					parts = append(parts, s)
				}
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		parts = append(parts, s)
	}
	return parts
}

func stripLeadingShellWrappers(tokens []string) []string {
	for len(tokens) > 0 {
		tok := tokens[0]
		if strings.Contains(tok, "=") && !strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "/") {
			tokens = tokens[1:]
			continue
		}
		base := commandBase(tok)
		switch base {
		case "env", "nohup", "exec", "sudo", "time", "nice", "ionice", "stdbuf":
			tokens = tokens[1:]
			// Skip wrapper flags like `stdbuf -oL`, `sudo -E`, `nice -n 5`.
			for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
				flag := tokens[0]
				tokens = tokens[1:]
				if (flag == "-n" || flag == "-u" || flag == "-C") && len(tokens) > 0 {
					tokens = tokens[1:]
				}
			}
			continue
		case "npx", "bunx", "pnpx", "uvx":
			tokens = tokens[1:]
			for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
				tokens = tokens[1:]
			}
			continue
		}
		break
	}
	return tokens
}

func commandBase(tok string) string {
	tok = strings.Trim(tok, `"'`)
	if idx := strings.LastIndexByte(tok, '/'); idx >= 0 {
		tok = tok[idx+1:]
	}
	return strings.ToLower(tok)
}

func hasFlag(tokens []string, flags ...string) bool {
	for _, tok := range tokens[1:] {
		l := strings.ToLower(tok)
		for _, f := range flags {
			if l == f || strings.HasPrefix(l, f+"=") {
				return true
			}
		}
	}
	return false
}

func hasArg(tokens []string, wants ...string) bool {
	for _, tok := range tokens[1:] {
		l := strings.ToLower(tok)
		for _, w := range wants {
			if l == w {
				return true
			}
		}
	}
	return false
}

func firstSubcommand(tokens []string) string {
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return strings.ToLower(tok)
	}
	return ""
}

func secondSubcommand(tokens []string) string {
	seen := 0
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		seen++
		if seen == 2 {
			return strings.ToLower(tok)
		}
	}
	return ""
}

func classifyTokens(tokens []string) (category, kind string, matched bool) {
	base := commandBase(tokens[0])
	sub := firstSubcommand(tokens)
	sub2 := secondSubcommand(tokens)

	// 1. Dedicated watcher / file-monitor binaries.
	switch base {
	case "nodemon", "watchexec", "entr", "fswatch", "chokidar", "inotifywait",
		"cargo-watch", "air", "reflex", "bacon", "pytest-watch", "ptw", "watch":
		return BgCategoryWatcher, BgKindWatcher, true
	}

	// 2. Streaming log followers.
	if base == "tail" && hasFlag(tokens, "-f", "-F", "--follow") {
		return BgCategoryWatcher, BgKindWatcher, true
	}
	if (base == "journalctl" || base == "dmesg") && hasFlag(tokens, "-f", "--follow", "-w", "--follow") {
		return BgCategoryWatcher, BgKindWatcher, true
	}
	if (base == "docker" || base == "podman" || base == "kubectl") && sub == "logs" && hasFlag(tokens, "-f", "--follow") {
		return BgCategoryWatcher, BgKindWatcher, true
	}

	// 3. Explicit --watch / watch subcommand on build/test tools.
	if hasFlag(tokens, "--watch", "--watch-all", "--watchall") {
		return BgCategoryWatcher, BgKindWatcher, true
	}
	if base == "cargo" && sub == "watch" {
		return BgCategoryWatcher, BgKindWatcher, true
	}
	switch base {
	case "tsc", "rollup", "esbuild", "webpack", "sass", "tailwindcss",
		"jest", "vitest", "mocha", "bun", "node":
		if hasFlag(tokens, "-w") {
			return BgCategoryWatcher, BgKindWatcher, true
		}
	}

	// 4. Package manager scripts (npm/pnpm/yarn/bun/make/just).
	switch base {
	case "npm", "pnpm", "yarn", "bun":
		script := sub
		if sub == "run" {
			script = sub2
		}
		switch script {
		case "watch", "test:watch", "build:watch", "dev:watch", "typecheck:watch":
			return BgCategoryWatcher, BgKindWatcher, true
		case "dev", "start", "serve", "server", "preview", "storybook", "develop":
			return BgCategoryService, BgKindService, true
		}
		if strings.HasSuffix(script, ":watch") || strings.HasPrefix(script, "watch:") {
			return BgCategoryWatcher, BgKindWatcher, true
		}
		if strings.HasSuffix(script, ":dev") || strings.HasSuffix(script, ":serve") {
			return BgCategoryService, BgKindService, true
		}
	case "make", "just", "task":
		switch sub {
		case "watch":
			return BgCategoryWatcher, BgKindWatcher, true
		case "dev", "serve", "server", "run-server", "storybook":
			return BgCategoryService, BgKindService, true
		}
	}

	// 5. Dev servers, web servers, databases, proxies, and tunnels.
	switch base {
	case "vite":
		if sub != "build" && sub != "optimize" {
			return BgCategoryService, BgKindService, true
		}
	case "webpack-dev-server", "http-server", "live-server", "storybook", "start-storybook",
		"uvicorn", "gunicorn", "hypercorn", "daphne",
		"nginx", "redis-server", "postgres", "mysqld", "mongod", "memcached",
		"ngrok", "localtunnel", "lt", "mailhog", "minio":
		return BgCategoryService, BgKindService, true
	case "next", "nuxt", "astro", "remix", "svelte-kit":
		if sub == "dev" || sub == "start" || sub == "preview" || sub == "" {
			return BgCategoryService, BgKindService, true
		}
	case "webpack":
		if sub == "serve" {
			return BgCategoryService, BgKindService, true
		}
	case "parcel":
		if sub != "build" {
			return BgCategoryService, BgKindService, true
		}
	case "hugo", "jekyll", "ng", "gatsby", "caddy", "mkdocs", "docsify":
		if sub == "server" || sub == "serve" || sub == "develop" || sub == "run" {
			return BgCategoryService, BgKindService, true
		}
	case "flask":
		if sub == "run" {
			return BgCategoryService, BgKindService, true
		}
	case "rails":
		if sub == "s" || sub == "server" {
			return BgCategoryService, BgKindService, true
		}
	case "bundle":
		if sub == "exec" && (sub2 == "puma" || sub2 == "rails" && hasArg(tokens, "s", "server")) {
			return BgCategoryService, BgKindService, true
		}
	case "php":
		if sub == "artisan" && sub2 == "serve" {
			return BgCategoryService, BgKindService, true
		}
		if hasFlag(tokens, "-s") {
			return BgCategoryService, BgKindService, true
		}
	case "mix":
		if sub == "phx.server" {
			return BgCategoryService, BgKindService, true
		}
	case "python", "python3", "uv":
		if hasArg(tokens, "runserver", "http.server", "uvicorn", "gunicorn", "hypercorn", "flask") &&
			!hasArg(tokens, "pytest", "unittest", "mypy", "ruff") {
			if hasArg(tokens, "runserver", "http.server", "uvicorn", "gunicorn", "hypercorn") ||
				(hasArg(tokens, "flask") && hasArg(tokens, "run")) {
				return BgCategoryService, BgKindService, true
			}
		}
	case "docker", "podman":
		if sub == "compose" && sub2 == "up" && !hasFlag(tokens, "-d", "--detach") {
			return BgCategoryService, BgKindService, true
		}
	case "docker-compose":
		if sub == "up" && !hasFlag(tokens, "-d", "--detach") {
			return BgCategoryService, BgKindService, true
		}
	case "kubectl":
		if sub == "port-forward" || sub == "proxy" {
			return BgCategoryService, BgKindService, true
		}
	case "cloudflared":
		if sub == "tunnel" {
			return BgCategoryService, BgKindService, true
		}
	case "ssh":
		if hasFlag(tokens, "-l", "-r", "-d") {
			return BgCategoryService, BgKindService, true
		}
	case "go":
		if sub == "run" && hasArg(tokens, "serve", "server") {
			return BgCategoryService, BgKindService, true
		}
	}

	return "", "", false
}

func classifyDescription(desc string) (category, kind string, matched bool) {
	lower := strings.ToLower(desc)
	for _, needle := range []string{"scheduled wakeup", "cron job", "recurring loop", "periodic wakeup"} {
		if strings.Contains(lower, needle) {
			return BgCategorySchedule, BgKindSchedule, true
		}
	}
	for _, needle := range []string{"file watcher", "watch mode", "watching ", "tail logs", "log monitor"} {
		if strings.Contains(lower, needle) {
			return BgCategoryWatcher, BgKindWatcher, true
		}
	}
	for _, needle := range []string{"dev server", "web server", "http server", "api server", "background service", "port forward", "port-forward"} {
		if strings.Contains(lower, needle) {
			return BgCategoryService, BgKindService, true
		}
	}
	return "", "", false
}

// NormalizeBackgroundTaskState maps raw provider task states and optional exit
// codes onto canonical states ("working", "done", "failed", "stopped") and an
// optional short outcome string.
func NormalizeBackgroundTaskState(rawState string, exitCode *int) (state, outcome string) {
	s := strings.ToLower(strings.TrimSpace(rawState))
	switch s {
	case "", "running", "working", "pending", "in_progress", "in-progress",
		"started", "queued", "active", "scheduled", "waiting":
		if exitCode != nil {
			if *exitCode == 0 {
				return "done", "exit 0"
			}
			return "failed", fmt.Sprintf("exit %d", *exitCode)
		}
		return "working", ""
	case "done", "completed", "complete", "succeeded", "success", "finished", "ok", "passed":
		if exitCode != nil && *exitCode != 0 {
			return "failed", fmt.Sprintf("exit %d", *exitCode)
		}
		if exitCode != nil {
			return "done", "exit 0"
		}
		return "done", "done"
	case "failed", "failure", "error", "errored", "crashed":
		if exitCode != nil {
			return "failed", fmt.Sprintf("exit %d", *exitCode)
		}
		return "failed", "failed"
	case "stopped", "cancelled", "canceled", "killed", "aborted", "terminated":
		return "stopped", s
	case "exited":
		if exitCode != nil && *exitCode != 0 {
			return "failed", fmt.Sprintf("exit %d", *exitCode)
		}
		return "done", "exit 0"
	default:
		if exitCode != nil {
			if *exitCode == 0 {
				return "done", "exit 0"
			}
			return "failed", fmt.Sprintf("exit %d", *exitCode)
		}
		return "working", ""
	}
}

func isActiveBgState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "working", "running", "pending", "in_progress", "started", "active", "scheduled", "waiting":
		return true
	default:
		return false
	}
}

func parseBoolField(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch b := v.(type) {
			case bool:
				if b {
					return true
				}
			case string:
				l := strings.ToLower(strings.TrimSpace(b))
				if l == "true" || l == "1" || l == "yes" {
					return true
				}
			case float64:
				if b != 0 {
					return true
				}
			case int:
				if b != 0 {
					return true
				}
			}
		}
	}
	return false
}

func parseOptionalInt(m map[string]any, keys ...string) *int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch n := v.(type) {
		case int:
			cp := n
			return &cp
		case int64:
			cp := int(n)
			return &cp
		case float64:
			cp := int(n)
			return &cp
		case json.Number:
			if i, err := n.Int64(); err == nil {
				cp := int(i)
				return &cp
			}
		}
	}
	return nil
}

func parseTimestampMs(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return normalizeEpochMs(int64(t))
	case int64:
		return normalizeEpochMs(t)
	case float64:
		return normalizeEpochMs(int64(t))
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return normalizeEpochMs(i)
		}
		if f, err := t.Float64(); err == nil {
			return normalizeEpochMs(int64(f))
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed.UnixMilli()
			}
		}
	}
	return 0
}

func normalizeEpochMs(v int64) int64 {
	if v <= 0 {
		return 0
	}
	switch {
	case v > 1e17: // nanoseconds
		return v / 1e6
	case v > 1e14: // microseconds
		return v / 1e3
	case v > 1e11: // milliseconds
		return v
	case v > 1e9: // seconds
		return v * 1000
	default:
		return v
	}
}

func shortHashID(prefix, seed string) string {
	sum := sha1.Sum([]byte(seed))
	return prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

// payloadHasKey checks whether any of keys exist directly in payload or its
// nested containers (even when the value is an empty slice).
func payloadHasKey(payload map[string]any, keys ...string) (any, bool) {
	if payload == nil {
		return nil, false
	}
	for _, k := range keys {
		if v, ok := payload[k]; ok && v != nil {
			return v, true
		}
	}
	for _, container := range nestedContainers {
		if nested, ok := payload[container].(map[string]any); ok {
			if v, ok := payloadHasKey(nested, keys...); ok {
				return v, true
			}
		}
	}
	return nil, false
}

func parseBackgroundTaskItem(raw any, fromCron bool, now int64) map[string]any {
	switch item := raw.(type) {
	case string:
		text := oneLine(item, MaxText, false)
		if text == "" {
			return nil
		}
		cat, knd := ClassifyBackgroundTask("", text, text, false, fromCron)
		prefix := "bg"
		if fromCron {
			prefix = "cron"
		}
		return map[string]any{
			"id":       shortHashID(prefix, text),
			"type":     knd,
			"kind":     knd,
			"category": cat,
			"state":    "working",
			"outcome":  "",
			"text":     text,
			"command":  text,
			"schedule": "",
			"since_ms": now,
			"ts_ms":    now,
			"ended_ms": int64(0),
		}
	case map[string]any:
		id := firstStr(item, "task_id", "taskId", "id", "shell_id", "shellId", "agent_id", "agentId", "cron_id", "cronId", "job_id", "jobId")
		rawType := firstStr(item, "task_type", "taskType", "type", "kind", "agent_type", "agentType")
		command := firstStr(item, "command", "cmd")
		desc := firstStr(item, "description", "title", "name", "label", "summary", "prompt", "text")
		schedule := firstStr(item, "cron", "schedule", "expression", "interval")
		persistent := parseBoolField(item, "persistent", "long_running", "longRunning", "is_service", "isService", "daemon")

		cat, knd := ClassifyBackgroundTask(rawType, command, desc, persistent, fromCron)
		rawState := firstStr(item, "status", "state")
		exitCode := parseOptionalInt(item, "exit_code", "exitCode")
		state, outcome := NormalizeBackgroundTaskState(rawState, exitCode)
		if explicitOutcome := firstStr(item, "outcome", "error", "reason"); explicitOutcome != "" && state != "working" {
			outcome = oneLine(explicitOutcome, 80, false)
		}

		text := desc
		if text == "" {
			text = command
		}
		if text == "" && schedule != "" {
			text = "scheduled (" + schedule + ")"
		}
		if text == "" {
			text = rawType
		}
		if text == "" {
			text = id
		}
		text = oneLine(text, MaxText, false)
		commandLine := oneLine(command, MaxText, false)

		if id == "" {
			seed := rawType + "|" + commandLine + "|" + text + "|" + schedule
			prefix := "bg"
			if fromCron || cat == BgCategorySchedule {
				prefix = "cron"
			}
			id = shortHashID(prefix, seed)
		}
		sinceMs := parseTimestampMs(first(item, "since_ms", "sinceMs", "started_at", "startedAt", "start_time", "startTime", "created_at", "createdAt"))
		if sinceMs <= 0 {
			sinceMs = now
		}
		endedMs := parseTimestampMs(first(item, "ended_ms", "endedMs", "completed_at", "completedAt", "ended_at", "endedAt", "finished_at", "finishedAt"))
		if !isActiveBgState(state) && endedMs <= 0 {
			endedMs = now
		}
		if rawType == "" {
			rawType = knd
		}
		return map[string]any{
			"id":       truncateRunes(id, MaxSession),
			"type":     truncateRunes(rawType, MaxModel),
			"kind":     knd,
			"category": cat,
			"state":    state,
			"outcome":  outcome,
			"text":     text,
			"command":  commandLine,
			"schedule": truncateRunes(schedule, 64),
			"since_ms": sinceMs,
			"ts_ms":    now,
			"ended_ms": endedMs,
		}
	default:
		return nil
	}
}

// extractBackgroundWork extracts background tasks, services, watchers, and
// scheduled wakeups from hook payloads (Stop/SubagentStop arrays, task_completed
// events, and background tool invocations like Bash run_in_background, Monitor,
// CronCreate, TaskStop/KillShell).
func extractBackgroundWork(payload map[string]any, event, tool string, toolInput, toolOutput any, now int64) (tasks []any, bgAuthoritative bool, cronsAuthoritative bool) {
	addTask := func(task map[string]any) {
		if task == nil {
			return
		}
		tasks = append(tasks, task)
	}

	if rawList, ok := payloadHasKey(payload, "background_tasks", "backgroundTasks"); ok {
		if list, isSlice := rawList.([]any); isSlice {
			bgAuthoritative = true
			for _, item := range list {
				addTask(parseBackgroundTaskItem(item, false, now))
			}
		}
	}
	if rawCrons, ok := payloadHasKey(payload, "session_crons", "sessionCrons", "scheduled_tasks", "scheduledTasks", "crons"); ok {
		if list, isSlice := rawCrons.([]any); isSlice {
			cronsAuthoritative = true
			for _, item := range list {
				addTask(parseBackgroundTaskItem(item, true, now))
			}
		}
	}

	// Explicit task_completed / task_created hooks.
	if event == "task_completed" || event == "task_created" {
		m := map[string]any{}
		for k, v := range payload {
			m[k] = v
		}
		if event == "task_completed" && firstStr(m, "status", "state") == "" {
			if firstStr(m, "error", "error_message") != "" {
				m["status"] = "failed"
			} else {
				m["status"] = "done"
			}
		}
		if event == "task_created" && firstStr(m, "status", "state") == "" {
			m["status"] = "running"
		}
		if parsed := parseBackgroundTaskItem(m, false, now); parsed != nil {
			addTask(parsed)
		}
	}

	// Tool-level background launches and stops (Bash run_in_background, Monitor,
	// CronCreate, ScheduleWakeup, TaskStop, KillShell).
	if event == "pre_tool" || event == "post_tool" || event == "tool_failure" {
		inMap, _ := toolInput.(map[string]any)
		outMap, _ := toolOutput.(map[string]any)
		toolLower := strings.ToLower(strings.TrimSpace(tool))

		switch toolLower {
		case "bash", "shell", "run_command":
			bgTaskID := extractAntigravityTaskID(toolOutput)
			isDaemon := inMap != nil && parseBoolField(inMap, "IsDaemon", "is_daemon", "isDaemon")
			runBg := inMap != nil && parseBoolField(inMap, "run_in_background", "runInBackground", "background")
			if inMap != nil && (runBg || isDaemon || bgTaskID != "") {
				cmd := firstStr(inMap, "command", "CommandLine", "Command", "cmd")
				desc := firstStr(inMap, "description", "toolSummary", "Description", "label", "name")
				id := bgTaskID
				if id == "" && outMap != nil {
					id = firstStr(outMap, "backgroundTaskId", "background_task_id", "task_id", "taskId", "shell_id", "shellId", "id")
				}
				if id == "" {
					id = firstStr(inMap, "task_id", "taskId", "shell_id", "shellId", "id")
				}
				if id == "" && (cmd != "" || desc != "") {
					id = shortHashID("bg", cmd+"|"+desc)
				}
				if id != "" {
					cat, knd := ClassifyBackgroundTask("local_bash", cmd, desc, isDaemon, false)
					state := "working"
					outcome := ""
					var endedMs int64
					if event == "tool_failure" || firstStr(payload, "error", "error_message", "errorMessage") != "" {
						state = "failed"
						outcome = "failed"
						endedMs = now
					}
					text := desc
					if text == "" {
						text = cmd
					}
					addTask(map[string]any{
						"id":       truncateRunes(id, MaxSession),
						"type":     "local_bash",
						"kind":     knd,
						"category": cat,
						"state":    state,
						"outcome":  outcome,
						"text":     oneLine(text, MaxText, false),
						"command":  oneLine(cmd, MaxText, false),
						"schedule": "",
						"since_ms": now,
						"ts_ms":    now,
						"ended_ms": endedMs,
					})
				}
			}
		case "run_workflow":
			if bgTaskID := extractAntigravityTaskID(toolOutput); bgTaskID != "" {
				desc := ""
				if inMap != nil {
					desc = firstStr(inMap, "Description", "description", "ScriptPath", "script_path")
				}
				if desc == "" {
					desc = "workflow"
				}
				addTask(map[string]any{
					"id":       truncateRunes(bgTaskID, MaxSession),
					"type":     "workflow",
					"kind":     BgKindCommand,
					"category": BgCategoryFinite,
					"state":    "working",
					"outcome":  "",
					"text":     oneLine(desc, MaxText, false),
					"command":  "",
					"schedule": "",
					"since_ms": now,
					"ts_ms":    now,
					"ended_ms": int64(0),
				})
			}
		case "monitor":
			if inMap != nil {
				cmd := firstStr(inMap, "command", "cmd")
				desc := firstStr(inMap, "description", "name")
				id := ""
				if outMap != nil {
					id = firstStr(outMap, "task_id", "taskId", "monitor_id", "monitorId", "id")
				}
				if id == "" && (cmd != "" || desc != "") {
					id = shortHashID("mon", cmd+"|"+desc)
				}
				if id != "" {
					text := desc
					if text == "" {
						text = cmd
					}
					addTask(map[string]any{
						"id":       truncateRunes(id, MaxSession),
						"type":     "monitor",
						"kind":     BgKindWatcher,
						"category": BgCategoryWatcher,
						"state":    "working",
						"outcome":  "",
						"text":     oneLine(text, MaxText, false),
						"command":  oneLine(cmd, MaxText, false),
						"schedule": "",
						"since_ms": now,
						"ts_ms":    now,
						"ended_ms": int64(0),
					})
				}
			}
		case "croncreate", "cron_create", "schedulewakeup", "schedule_wakeup", "schedule":
			if inMap != nil {
				sched := firstStr(inMap, "cron", "cron_expression", "CronExpression", "schedule", "interval")
				if sched == "" {
					if dur := parseOptionalInt(inMap, "DurationSeconds", "duration_seconds", "durationSeconds"); dur != nil && *dur > 0 {
						sched = fmt.Sprintf("in %ds", *dur)
					}
				}
				prompt := firstStr(inMap, "prompt", "Prompt", "description", "command")
				id := extractAntigravityTaskID(toolOutput)
				if id == "" && outMap != nil {
					id = firstStr(outMap, "id", "job_id", "cron_id", "task_id", "TaskId")
				}
				if id == "" && (sched != "" || prompt != "") {
					id = shortHashID("cron", sched+"|"+prompt)
				}
				if id != "" {
					text := prompt
					if text == "" {
						text = sched
					}
					addTask(map[string]any{
						"id":       truncateRunes(id, MaxSession),
						"type":     "cron",
						"kind":     BgKindSchedule,
						"category": BgCategorySchedule,
						"state":    "working",
						"outcome":  "",
						"text":     oneLine(text, MaxText, false),
						"command":  "",
						"schedule": truncateRunes(sched, 64),
						"since_ms": now,
						"ts_ms":    now,
						"ended_ms": int64(0),
					})
				}
			}
		case "manage_task":
			if inMap != nil && strings.EqualFold(firstStr(inMap, "Action", "action"), "kill") {
				if id := firstStr(inMap, "TaskId", "task_id", "taskId", "id"); id != "" {
					addTask(map[string]any{
						"id":       truncateRunes(id, MaxSession),
						"state":    "stopped",
						"outcome":  "stopped",
						"ts_ms":    now,
						"ended_ms": now,
					})
				}
			}
		case "taskstop", "task_stop", "killshell", "kill_shell", "crondelete", "cron_delete":
			if inMap != nil {
				id := firstStr(inMap, "task_id", "taskId", "shell_id", "shellId", "id", "cron_id", "job_id")
				if id != "" {
					addTask(map[string]any{
						"id":       truncateRunes(id, MaxSession),
						"state":    "stopped",
						"outcome":  "stopped",
						"ts_ms":    now,
						"ended_ms": now,
					})
				}
			}
		}
	}

	return tasks, bgAuthoritative, cronsAuthoritative
}

func mergeBackgroundTasks(existing []any, incoming []any, bgAuthoritative, cronsAuthoritative bool, now int64) []any {
	items := make([]map[string]any, 0, len(existing)+len(incoming))
	byID := make(map[string]int, len(existing))
	for _, raw := range existing {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cp := make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
		id, _ := cp["id"].(string)
		if id != "" {
			byID[id] = len(items)
		}
		items = append(items, cp)
	}

	seenBgIDs := map[string]bool{}
	seenCronIDs := map[string]bool{}
	for _, raw := range incoming {
		inc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := inc["id"].(string)
		cat, _ := inc["category"].(string)
		if id != "" {
			if cat == BgCategorySchedule {
				seenCronIDs[id] = true
			} else {
				seenBgIDs[id] = true
			}
		}
		if id != "" {
			if idx, exists := byID[id]; exists {
				prev := items[idx]
				prevSince := toInt64(prev["since_ms"])
				prevState, _ := prev["state"].(string)
				merged := make(map[string]any, len(prev)+len(inc))
				for k, v := range prev {
					merged[k] = v
				}
				for k, v := range inc {
					// Do not overwrite rich metadata with empty patch fields (e.g. TaskStop).
					if s, ok := v.(string); ok && s == "" && (k == "type" || k == "kind" || k == "category" || k == "text" || k == "command" || k == "schedule") {
						continue
					}
					merged[k] = v
				}
				if prevSince > 0 {
					merged["since_ms"] = prevSince
				}
				newState, _ := merged["state"].(string)
				if !isActiveBgState(newState) {
					if toInt64(merged["ended_ms"]) <= 0 {
						if prevEnded := toInt64(prev["ended_ms"]); prevEnded > 0 && !isActiveBgState(prevState) {
							merged["ended_ms"] = prevEnded
						} else {
							merged["ended_ms"] = now
						}
					}
				} else {
					merged["ended_ms"] = int64(0)
				}
				items[idx] = merged
				continue
			}
		}
		// Skip stop-only patch if we never tracked the ID.
		if _, hasText := inc["text"]; !hasText && !isActiveBgState(stringKey(inc["state"])) {
			continue
		}
		cp := make(map[string]any, len(inc))
		for k, v := range inc {
			cp[k] = v
		}
		if toInt64(cp["since_ms"]) <= 0 {
			cp["since_ms"] = now
		}
		if id != "" {
			byID[id] = len(items)
		}
		items = append(items, cp)
	}

	// Reconcile authoritative Stop snapshots: any previously-active task not in
	// the authoritative snapshot has finished.
	if bgAuthoritative {
		for _, item := range items {
			cat, _ := item["category"].(string)
			if cat == BgCategorySchedule {
				continue
			}
			id, _ := item["id"].(string)
			state, _ := item["state"].(string)
			if isActiveBgState(state) && !seenBgIDs[id] {
				item["state"] = "done"
				if strings.TrimSpace(stringKey(item["outcome"])) == "" {
					item["outcome"] = "done"
				}
				if toInt64(item["ended_ms"]) <= 0 {
					item["ended_ms"] = now
				}
				item["ts_ms"] = now
			}
		}
	}
	if cronsAuthoritative {
		for _, item := range items {
			cat, _ := item["category"].(string)
			if cat != BgCategorySchedule {
				continue
			}
			id, _ := item["id"].(string)
			state, _ := item["state"].(string)
			if isActiveBgState(state) && !seenCronIDs[id] {
				item["state"] = "stopped"
				if strings.TrimSpace(stringKey(item["outcome"])) == "" {
					item["outcome"] = "stopped"
				}
				if toInt64(item["ended_ms"]) <= 0 {
					item["ended_ms"] = now
				}
				item["ts_ms"] = now
			}
		}
	}

	if len(items) > MaxChildren {
		items = items[len(items)-MaxChildren:]
	}
	out := make([]any, len(items))
	for i, m := range items {
		out[i] = m
	}
	return out
}

// reconcileChildrenFromBackground marks previously-working subagent children as
// done when an authoritative background_tasks snapshot on Stop no longer lists
// them (preventing a missed SubagentStop from wedging a pane in working).
func reconcileChildrenFromBackground(children []any, bgTasks []any, now int64) []any {
	activeBgIDs := map[string]bool{}
	for _, raw := range bgTasks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if isActiveBgState(stringKey(m["state"])) {
			if id := stringKey(m["id"]); id != "" {
				activeBgIDs[id] = true
			}
		}
	}
	out := make([]any, 0, len(children))
	for _, raw := range children {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cp := make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
		id := stringKey(cp["id"])
		if isActiveBgState(stringKey(cp["state"])) && !activeBgIDs[id] {
			cp["state"] = "done"
			if strings.TrimSpace(stringKey(cp["outcome"])) == "" {
				cp["outcome"] = "done"
			}
			if toInt64(cp["ended_ms"]) <= 0 {
				cp["ended_ms"] = now
			}
			cp["ts_ms"] = now
		}
		out = append(out, cp)
	}
	return out
}

// collectBackgroundWork returns deduplicated active finite work and active
// long-running work (services, watchers, schedules) for a pane record.
func collectBackgroundWork(record map[string]any) (activeFinite []map[string]any, activeServices []map[string]any) {
	seen := map[string]bool{}
	consider := func(raw any, defaultCategory, defaultKind string) {
		m, ok := raw.(map[string]any)
		if !ok {
			return
		}
		id := stringKey(m["id"])
		if id != "" {
			if seen[id] {
				return
			}
			seen[id] = true
		}
		if !isActiveBgState(stringKey(m["state"])) {
			return
		}
		cat := strings.TrimSpace(stringKey(m["category"]))
		if cat == "" {
			cat = defaultCategory
		}
		if cat == BgCategoryFinite {
			activeFinite = append(activeFinite, m)
		} else {
			activeServices = append(activeServices, m)
		}
	}
	// Background tasks take precedence when an agent ID appears in both.
	if bg, ok := record["background_tasks"].([]any); ok {
		for _, item := range bg {
			consider(item, BgCategoryFinite, BgKindCommand)
		}
	}
	if ch, ok := record["children"].([]any); ok {
		for _, item := range ch {
			consider(item, BgCategoryFinite, BgKindSubagent)
		}
	}
	return activeFinite, activeServices
}

func bgItemSummaryLabel(m map[string]any) string {
	cmd := strings.TrimSpace(stringKey(m["command"]))
	text := strings.TrimSpace(stringKey(m["text"]))
	typ := strings.TrimSpace(stringKey(m["type"]))
	kind := strings.TrimSpace(stringKey(m["kind"]))
	// Strip redundant "subagent: " prefix when building concise waiting text.
	cleanText := strings.TrimPrefix(text, "subagent: ")
	cleanText = strings.TrimPrefix(cleanText, "subagent done: ")
	if kind == BgKindSubagent || typ == "local_agent" || typ == "subagent" || typ == "Explore" {
		if typ != "" && typ != "subagent" && typ != "local_agent" && cleanText != "" && !strings.EqualFold(typ, cleanText) {
			return typ + " · " + cleanText
		}
		if cleanText != "" {
			return cleanText
		}
		if typ != "" {
			return typ
		}
		return "subagent"
	}
	if cmd != "" {
		return cmd
	}
	if cleanText != "" {
		return cleanText
	}
	if typ != "" {
		return typ
	}
	return "background task"
}

// formatWaitingText renders a concise human-readable description of the active
// finite work that is keeping a pane in `working` after a Stop hook.
func formatWaitingText(finiteItems []map[string]any) string {
	if len(finiteItems) == 0 {
		return ""
	}
	if len(finiteItems) == 1 {
		m := finiteItems[0]
		kind := strings.TrimSpace(stringKey(m["kind"]))
		label := bgItemSummaryLabel(m)
		if kind == BgKindSubagent {
			return oneLine("waiting on subagent: "+label, MaxText, false)
		}
		return oneLine("waiting on: "+label, MaxText, false)
	}
	labels := make([]string, 0, len(finiteItems))
	for _, m := range finiteItems {
		labels = append(labels, oneLine(bgItemSummaryLabel(m), 48, false))
	}
	sort.Strings(labels)
	return oneLine(fmt.Sprintf("waiting on %d tasks: %s", len(finiteItems), strings.Join(labels, ", ")), MaxText, false)
}

// ResolvePaneEvent evaluates a normalized event against the pane's current
// stored state (store + pending spool) using the exact same ATT-5 background
// completion rules as MergeEvent. HookClient calls this before emitting tmux
// pane options / OSC 7501 so the hook store and tmux status always agree.
func ResolvePaneEvent(session string, pane any, event map[string]any) map[string]any {
	if event == nil {
		return nil
	}
	if child, ok := event["child"].(map[string]any); ok && child != nil {
		return event
	}
	eventName, _ := event["event"].(string)
	state, _ := event["state"].(string)
	isAgy := stringKey(event["agent"]) == "antigravity"
	if !isAgy && eventName != "stop" && !(eventName == "notification" && state == "done") {
		return event
	}

	var existing map[string]any
	if session != "" && pane != nil {
		store, _ := mergedView()
		if panes, ok := store["panes"].(map[string]any); ok {
			if sp, ok := panes[session].(map[string]any); ok {
				existing, _ = sp[stringKey(pane)].(map[string]any)
			}
		}
	}
	now := toInt64(event["ts_ms"])
	if now <= 0 {
		now = nowMs()
	}
	if isAgy {
		promoted := promoteAntigravitySubagentFromRecord(existing, event, now)
		if child, ok := promoted["child"].(map[string]any); ok && child != nil {
			return promoted
		}
		if eventName != "stop" && !(eventName == "notification" && state == "done") {
			return event
		}
	}
	// Run merge logic on a non-deduped copy to obtain the exact resolved state.
	simExisting := existing
	if existing != nil {
		simExisting = make(map[string]any, len(existing))
		for k, v := range existing {
			if k == "last_event" {
				continue
			}
			simExisting[k] = v
		}
	}
	merged, _ := mergeEventApplied(simExisting, event, now)
	resolved := make(map[string]any, len(event)+2)
	for k, v := range event {
		resolved[k] = v
	}
	if st, _ := merged["state"].(string); st != "" {
		resolved["state"] = st
	}
	if txt, _ := merged["text"].(string); txt != "" {
		resolved["text"] = txt
	}
	if wo, ok := merged["waiting_on"]; ok {
		resolved["waiting_on"] = wo
	}
	return resolved
}

var (
	agySubagentConvIDRe = regexp.MustCompile(`(?i)"(?:conversationId|conversation_id)"\s*:\s*"([^"]+)"`)
	agyBgTaskIDRe       = regexp.MustCompile(`(?i)background task with task id:\s*([^\s"'` + "`" + `]+)`)
)

func extractAntigravityTaskID(toolOutput any) string {
	switch v := toolOutput.(type) {
	case string:
		if m := agyBgTaskIDRe.FindStringSubmatch(v); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	case map[string]any:
		if id := firstStr(v, "taskId", "task_id", "TaskId", "backgroundTaskId", "background_task_id"); id != "" {
			return id
		}
		for _, k := range []string{"output", "content", "text", "result", "message"} {
			if s, ok := v[k].(string); ok {
				if m := agyBgTaskIDRe.FindStringSubmatch(s); len(m) == 2 {
					return strings.TrimSpace(m[1])
				}
			}
		}
	}
	return ""
}

func extractSubagentConversationID(toolOutput any) string {
	switch v := toolOutput.(type) {
	case string:
		if m := agySubagentConvIDRe.FindStringSubmatch(v); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	case map[string]any:
		if id := firstStr(v, "conversationId", "conversation_id", "agentId", "agent_id"); id != "" {
			return id
		}
		for _, k := range []string{"output", "content", "text", "result", "message"} {
			if s, ok := v[k].(string); ok {
				if m := agySubagentConvIDRe.FindStringSubmatch(s); len(m) == 2 {
					return strings.TrimSpace(m[1])
				}
			}
		}
	}
	return ""
}

// antigravityStopIdleSignal reads Antigravity's StopHookArgs.fullyIdle signal.
// Because protojson omits default false booleans, a real Antigravity StopHookArgs
// payload (carrying terminationReason/executionNum/continuationActive/hookEventName=Stop)
// for a normal stop without fullyIdle=true indicates background tasks or subagents
// are still running.
func antigravityStopIdleSignal(agent string, payload map[string]any) (known bool, fullyIdle bool) {
	if v, ok := payloadHasKey(payload, "fullyIdle", "fully_idle"); ok {
		switch b := v.(type) {
		case bool:
			return true, b
		case string:
			l := strings.ToLower(strings.TrimSpace(b))
			if l == "true" || l == "1" {
				return true, true
			}
			if l == "false" || l == "0" {
				return true, false
			}
		}
	}
	if agent != "antigravity" {
		return false, false
	}
	termReason := strings.ToUpper(strings.TrimSpace(firstStr(payload, "terminationReason", "termination_reason")))
	if termReason == "ERROR" || termReason == "USER_CANCELED" {
		return false, false
	}
	_, hasExec := payloadHasKey(payload, "executionNum", "execution_num")
	_, hasCont := payloadHasKey(payload, "continuationActive", "continuation_active")
	hookEv := firstStr(payload, "hookEventName", "hook_event_name")
	if termReason != "" || hasExec || hasCont || strings.EqualFold(hookEv, "Stop") {
		return true, false
	}
	return false, false
}

// extractChildToolUpdates extracts subagent launch/kill updates from parent
// tool invocations (Antigravity's invoke_subagent and manage_subagents).
func extractChildToolUpdates(payload map[string]any, event, tool, toolCallID string, toolInput, toolOutput any, now int64) []any {
	if event != "post_tool" && event != "tool_failure" {
		return nil
	}
	inMap, _ := toolInput.(map[string]any)
	if inMap == nil {
		return nil
	}
	toolLower := strings.ToLower(strings.TrimSpace(tool))
	switch toolLower {
	case "invoke_subagent":
		typeName := firstStr(inMap, "TypeName", "type_name", "typeName", "agent_type", "agentType")
		role := firstStr(inMap, "Role", "role", "description", "Description")
		prompt := firstStr(inMap, "Prompt", "prompt")
		id := extractSubagentConversationID(toolOutput)
		syntheticID := false
		if id == "" {
			syntheticID = true
			id = strings.TrimSpace(toolCallID)
		}
		if id == "" {
			id = shortHashID("sub", typeName+"|"+role+"|"+prompt)
		}
		text := role
		if text == "" {
			text = prompt
		}
		if text == "" {
			text = typeName
		}
		state := "working"
		outcome := ""
		var endedMs int64
		errMsg := firstStr(payload, "error", "error_message", "errorMessage")
		if event == "tool_failure" || errMsg != "" {
			state = "failed"
			if errMsg != "" {
				outcome = oneLine(errMsg, 80, false)
			} else {
				outcome = "failed"
			}
			endedMs = now
		}
		child := map[string]any{
			"id":       truncateRunes(id, MaxSession),
			"type":     truncateRunes(typeName, MaxModel),
			"kind":     BgKindSubagent,
			"category": BgCategoryFinite,
			"state":    state,
			"outcome":  outcome,
			"text":     oneLine(text, MaxText, false),
			"since_ms": now,
			"ts_ms":    now,
			"ended_ms": endedMs,
		}
		if syntheticID {
			child["synthetic_id"] = true
		}
		return []any{child}
	case "manage_subagents":
		action := strings.ToLower(strings.TrimSpace(firstStr(inMap, "Action", "action")))
		switch action {
		case "kill_all":
			return []any{map[string]any{
				"kill_all": true,
				"state":    "stopped",
				"outcome":  "stopped",
				"ts_ms":    now,
				"ended_ms": now,
			}}
		case "kill":
			rawIDs, _ := inMap["ConversationIds"].([]any)
			if len(rawIDs) == 0 {
				rawIDs, _ = inMap["conversation_ids"].([]any)
			}
			var updates []any
			for _, raw := range rawIDs {
				if id, ok := raw.(string); ok && strings.TrimSpace(id) != "" {
					updates = append(updates, map[string]any{
						"id":       truncateRunes(strings.TrimSpace(id), MaxSession),
						"state":    "stopped",
						"outcome":  "stopped",
						"ts_ms":    now,
						"ended_ms": now,
					})
				}
			}
			return updates
		}
	}
	return nil
}

// mergeChildEntry folds one subagent child update (or kill_all marker) into
// record["children"], preserving original since_ms and non-empty metadata.
func mergeChildEntry(children []any, child map[string]any, now int64) []any {
	items := make([]any, 0, len(children)+1)
	for _, item := range children {
		if m, ok := item.(map[string]any); ok {
			cp := make(map[string]any, len(m))
			for k, v := range m {
				cp[k] = v
			}
			items = append(items, cp)
		}
	}
	if parseBoolField(child, "kill_all") {
		for _, item := range items {
			m := item.(map[string]any)
			if isActiveBgState(stringKey(m["state"])) {
				m["state"] = "stopped"
				m["outcome"] = "stopped"
				m["ts_ms"] = now
				if toInt64(m["ended_ms"]) <= 0 {
					m["ended_ms"] = now
				}
			}
		}
		return items
	}
	childID := stringKey(child["id"])
	preserveText := parseBoolField(child, "preserve_text")
	incomingSynthetic := parseBoolField(child, "synthetic_id")
	matchIdx := -1
	for i, item := range items {
		m := item.(map[string]any)
		if stringKey(m["id"]) == childID {
			matchIdx = i
			break
		}
	}
	// If a real subagent conversation event arrives (with conversationId) and
	// invoke_subagent previously recorded a synthetic ID (empty toolOutput),
	// adopt that synthetic entry so its Role is preserved and there is no duplicate.
	if matchIdx < 0 && !incomingSynthetic && preserveText && childID != "" {
		inType := strings.ToLower(strings.TrimSpace(stringKey(child["type"])))
		fallbackIdx := -1
		for i, item := range items {
			m := item.(map[string]any)
			if !parseBoolField(m, "synthetic_id") || !isActiveBgState(stringKey(m["state"])) {
				continue
			}
			mType := strings.ToLower(strings.TrimSpace(stringKey(m["type"])))
			if inType != "" && mType != "" && inType == mType {
				matchIdx = i
				break
			}
			if fallbackIdx < 0 {
				fallbackIdx = i
			}
		}
		if matchIdx < 0 {
			matchIdx = fallbackIdx
		}
	}
	replaced := false
	if matchIdx >= 0 {
		m := items[matchIdx].(map[string]any)
		merged := make(map[string]any, len(m)+len(child))
		for k, v := range m {
			merged[k] = v
		}
		for k, v := range child {
			if k == "preserve_text" {
				continue
			}
			if s, ok := v.(string); ok {
				if s == "" && (k == "type" || k == "kind" || k == "category" || k == "text" || k == "command" || k == "schedule") {
					continue
				}
				if k == "text" && preserveText && strings.TrimSpace(stringKey(m["text"])) != "" {
					continue
				}
				if k == "type" && preserveText && strings.TrimSpace(stringKey(m["type"])) != "" {
					continue
				}
			}
			merged[k] = v
		}
		if !incomingSynthetic {
			delete(merged, "synthetic_id")
		}
		if s := toInt64(m["since_ms"]); s > 0 {
			merged["since_ms"] = s
		} else if child["since_ms"] != nil {
			merged["since_ms"] = child["since_ms"]
		}
		if !isActiveBgState(stringKey(merged["state"])) {
			if toInt64(merged["ended_ms"]) <= 0 {
				merged["ended_ms"] = now
			}
		} else {
			merged["ended_ms"] = int64(0)
		}
		items[matchIdx] = merged
		replaced = true
	}
	if !replaced {
		// Skip stop-only patch (e.g. manage_subagents kill) if we never tracked the ID.
		if _, hasKind := child["kind"]; !hasKind && !isActiveBgState(stringKey(child["state"])) {
			return items
		}
		cp := make(map[string]any, len(child))
		for k, v := range child {
			if k == "preserve_text" {
				continue
			}
			cp[k] = v
		}
		if toInt64(cp["since_ms"]) <= 0 {
			cp["since_ms"] = now
		}
		if !isActiveBgState(stringKey(cp["state"])) && toInt64(cp["ended_ms"]) <= 0 {
			cp["ended_ms"] = now
		}
		items = append(items, cp)
	}
	if len(items) > MaxChildren {
		items = items[len(items)-MaxChildren:]
	}
	return items
}

// reconcileFiniteOnIdle marks active finite children and background tasks as
// done when an authoritative idle signal (such as Antigravity Stop with
// fullyIdle=true) confirms all finite background work has completed, while
// leaving long-running services, watchers, and schedules intact.
func reconcileFiniteOnIdle(record map[string]any, now int64) {
	if children, ok := record["children"].([]any); ok && len(children) > 0 {
		out := make([]any, 0, len(children))
		for _, raw := range children {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			cp := make(map[string]any, len(m))
			for k, v := range m {
				cp[k] = v
			}
			if isActiveBgState(stringKey(cp["state"])) {
				cp["state"] = "done"
				if strings.TrimSpace(stringKey(cp["outcome"])) == "" {
					cp["outcome"] = "done"
				}
				if toInt64(cp["ended_ms"]) <= 0 {
					cp["ended_ms"] = now
				}
				cp["ts_ms"] = now
			}
			out = append(out, cp)
		}
		record["children"] = out
	}
	if bgTasks, ok := record["background_tasks"].([]any); ok && len(bgTasks) > 0 {
		out := make([]any, 0, len(bgTasks))
		for _, raw := range bgTasks {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			cp := make(map[string]any, len(m))
			for k, v := range m {
				cp[k] = v
			}
			cat := strings.TrimSpace(stringKey(cp["category"]))
			if (cat == "" || cat == BgCategoryFinite) && isActiveBgState(stringKey(cp["state"])) {
				cp["state"] = "done"
				if strings.TrimSpace(stringKey(cp["outcome"])) == "" {
					cp["outcome"] = "done"
				}
				if toInt64(cp["ended_ms"]) <= 0 {
					cp["ended_ms"] = now
				}
				cp["ts_ms"] = now
			}
			out = append(out, cp)
		}
		record["background_tasks"] = out
	}
}

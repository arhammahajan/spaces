# Harness Companion Plugins Architecture

`tmux-spaces` supports **rich in-process companion plugins** for coding harnesses alongside its existing external fallback mechanisms (hooks, daemon SSE watchers, OSC 7501, transcript/SQLite readers, and `capture-pane` screen rules).

## Architectural Decision: Design 1 vs. Design 2

Two candidate designs were evaluated for companion plugins:

1. **Design 1 (Chosen): Agent-specific Spaces plugins + rich harness extractors + shared Spaces normalization/aggregation.**
   - Each harness companion plugin (`plugins/<agent>-spaces/`) runs inside or directly alongside the harness and acts as a **rich extractor**—subscribing to every lifecycle hook, event bus message, status pipe, and OTel feed the harness exposes and pushing structured `v2` envelopes over the local `spacecore` Unix domain socket (`POST /v1/plugin/<agent>`).
   - Each agent's Go plugin inside `spaces` (`internal/agent/plugins/<agent>_companion.go` implementing `agent.CompanionPlugin`) owns agent-specific normalization (`NormalizeCompanionEvent`), installation/uninstallation (`InstallCompanion` / `UninstallCompanion`), and health diagnostics (`DetectCompanionStatus`).
   - Shared `spaces` infrastructure (`internal/hooks/core`, `internal/model`, `internal/cli`, `internal/tui`) owns canonical state merging, ATT-5 background-work classification, subagent isolation, tiered source precedence, TTL/staleness handling, and TUI/CLI display.

2. **Design 2 (Rejected): Full normalization inside each harness plugin.**
   - Would duplicate state-machine rules, ATT-5 finite-vs-service background work logic, auth-block detection, context percentage math, tool preview formatting, and subagent isolation across four separate JS/TS/shell codebases.
   - Any bug fix or schema evolution in `spaces` would require updating and re-installing every harness plugin.
   - Breaks the existing `internal/agent` plugin boundary documented in [agent-plugins.md](agent-plugins.md).

## Shared Wire Contract (`v2`)

Companion plugins push HTTP/1.1 `POST /v1/plugin/<agent>` requests over the `spacecore` Unix domain socket discovered from `<runtimeDir>/hooks.endpoint.json` (`$SPACES_HOOKS_RUNTIME`, else `$XDG_RUNTIME_DIR/spaces`, else `/tmp/spaces-<uid>`):

- **Auth:** `X-ZJ-Token: <token>` header + Linux `SO_PEERCRED` same-UID check.
- **Fail-open & non-blocking:** Short socket timeout (50–100 ms), async fire-and-forget dispatch, never blocks or mutates harness execution, silent no-op when `$TMUX_PANE` is unset.
- **Exact `(tmux_session, tmux_pane) <-> session_id` binding:** Each plugin inherits `$TMUX_PANE` and resolves the tmux session once (cached in-process).
- **Rich `v2` telemetry fields:**
  - Plugin metadata: `plugin_name`, `plugin_version`, `plugin_capabilities`, `plugin_ts_ms`, `pid`, `harness_version`
  - Session & mode: `session_id`, `parent_session_id`, `is_subagent`, `title`, `cwd`, `profile`, `provider`, `model`, `effort`, `mode`, `sandbox_mode`, `git_branch`
  - Live metrics: `usage` (input, output, reasoning, cache read/write, total, cost USD), `context_tokens`, `context_window`, `context_percent`, `turn_count`, `compaction_count`, `last_compaction_ms`, `last_compaction_reason`, `tool_call_count`, `tool_error_count`, `retry_count`, `rate_limited`, `rate_limit_reset_ms`, `rate_limit_message`
  - Rich activity & artifacts: `active_tools`, `recent_tools`, `files_touched`, `todos`, `mcp_servers`, `lsp_diagnostics`, `pending_permission`, `subagents` / `children`, `background_tasks`, `last_error`, `extras`

## Tiered Source Precedence & Fallback

All existing fallback paths remain intact. Sources are layered by authority:

1. **`plugin` (highest authority):** Fresh companion plugin pushes win over hooks, daemon SSE, OSC 7501, and screen rules. While fresh for a pane, `DaemonWatchManager` skips redundant `daemon_sse` overwrites and the TUI skips expensive `capture-pane` screen scraping for that pane.
2. **`daemon_sse` / `hook`:** Existing structured event feeds take over automatically if a companion plugin is not installed or goes stale.
3. **`osc7501` / `screen`:** Zero-config terminal and viewport fallback for uninstrumented harnesses.

### Subagent Isolation

Child/subagent events tagged with `is_subagent: true` or `parent_session_id != ""` are routed strictly to the pane's `children` tree and never overwrite the parent session's `session_id`, `model`, `context_tokens`, `context_window`, `title`, or `usage`.

## Supported Harnesses (Initial 4)

| Harness | Companion Plugin | Rich Signals Extracted |
| --- | --- | --- |
| **OpenCode** | `plugins/opencode-spaces` | Bus events (`session.*`, `message.*`, `message.part.*`, `permission.*`, `question.*`, `todo.updated`, `file.edited`, `lsp.*`, `mcp.*`, `vcs.branch.updated`), `tool.execute.before/after`, `chat.params`, `experimental.session.compacting`, child session isolation, token/cost/context/diff tracking |
| **Pi** | `plugins/pi-spaces` | `ExtensionAPI` events (`session_start/switch/fork/tree/shutdown`, `input`, `agent_start/end`, `turn_start/end`, `tool_call/result`, `session_before_compact/compact`, `model_select`), live `ctx.getContextUsage()`, thinking level, cumulative usage/cost, edited files |
| **Claude Code** | `plugins/claude-spaces` | Distributable `.claude-plugin/plugin.json` + `hooks/hooks.json` covering all lifecycle hooks + `statusLine` live JSON bridge (`context_window`, `cost`, `model`, `workspace`, `version`) + subagent/background task tracking + `TodoWrite`/`Edit`/`Write` extraction + transcript tail fallback |
| **Codex** | `plugins/codex-spaces` | Modern `hooks.json` lifecycle hooks + legacy `notify` fallback + OpenTelemetry OTLP JSON bridge (`/v1/logs`, `/v1/metrics`, `/v1/traces`) + `apply_patch` file extraction + rollout JSONL token/context/rate-limit extraction |

## CLI & Diagnostics

- `tmux-spaces plugins list` — List supported companion plugins and their capabilities.
- `tmux-spaces plugins status [--json]` — Show installation and live freshness status per harness.
- `tmux-spaces plugins install <agent|all>` — Install or update companion plugin(s) into the harness's native plugin/config directory.
- `tmux-spaces plugins uninstall <agent|all>` — Cleanly remove installed companion plugin(s).
- `tmux-spaces doctor` / `tmux-spaces harness-doctor` — Report active companion plugins vs. fallback mode.

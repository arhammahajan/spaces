# codex-spaces — Codex CLI companion plugin for tmux-spaces

RICH bridge between the OpenAI Codex CLI and the tmux-spaces `spacecore`
hook listener. It combines three telemetry paths into one wire format:

1. **Native `hooks.json` lifecycle hooks** (`scripts/hook-bridge.mjs`) —
   `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
   `PermissionRequest`, `Stop`, `SessionEnd` with stdin JSON payloads.
2. **Legacy `notify` fallback** (same script) — `config.toml`
   `notify = [...]` argv JSON payloads such as `agent-turn-complete`.
3. **OTLP telemetry bridge** (`scripts/otel-bridge.mjs`) — translates
   Codex `[otel]` OTLP HTTP/JSON `/v1/logs`, `/v1/metrics`, `/v1/traces`
   exports (conversation starts, API requests, SSE events, tool
   decisions/results, user prompts, token usage, 429 rate limits, model,
   reasoning effort, sandbox policy) into `v2` envelopes.

All paths POST JSON to `POST /v1/plugin/codex` on the spacecore listener
(Unix socket, `X-ZJ-Token` header) with a 100 ms fail-open budget, and
always exit 0 so telemetry can never block a Codex tool call or turn.

Zero runtime dependencies (Node ≥ 18 stdlib only).

## Wire contract

```
POST /v1/plugin/codex HTTP/1.1
Content-Type: application/json
Content-Length: <n>
X-ZJ-Token: <token from hooks.endpoint.json>
```

```json
{
  "v": 2,
  "agent": "codex",
  "session": "<tmux_session_name>",
  "pane": "<$TMUX_PANE>",
  "plugin": {
    "name": "codex-spaces",
    "version": "1.0.0",
    "capabilities": ["lifecycle", "usage", "context", "tools", "files", "subagents", "permissions", "rate_limits", "otel", "sandbox", "errors"]
  },
  "event": "<codex_native_event_name>",
  "ts_ms": 1700000000000,
  "pid": 12345,
  "harness_version": "<if_known>",
  "payload": { "...": "... see scripts/hook-bridge.mjs buildPayload ..." }
}
```

## Install

### 1. Native hooks (recommended)

```sh
# copy the template and point it at this directory
cp plugins/codex-spaces/hooks.json ~/.codex/hooks.json
# then replace every <PLUGIN_DIR> with the absolute plugin path, e.g.:
sed -i "s|<PLUGIN_DIR>|$HOME/tmux-spaces/plugins/codex-spaces|g" ~/.codex/hooks.json
```

### 2. Legacy `notify` fallback (older Codex)

In `~/.codex/config.toml`:

```toml
notify = ["node", "<PLUGIN_DIR>/scripts/hook-bridge.mjs", "notify", "{\"type\":\"agent-turn-complete\"}"]
```

Codex appends/passes the turn JSON via argv; the bridge detects argv JSON
payloads with `type: agent-turn-complete` and reports them as `Stop`.

### 3. Optional OTLP telemetry

In `~/.codex/config.toml`:

```toml
[otel]
# Point Codex's OTLP exporter at a local forwarder that pipes each
# export body into otel-bridge.mjs, e.g.:
# exporter_otlp_endpoint = "http://127.0.0.1:4318"
```

and forward each export body on stdin:

```sh
curl -s http://127.0.0.1:4318/v1/logs -d @export.json |
  node plugins/codex-spaces/scripts/otel-bridge.mjs /v1/logs
```

The bridge aggregates token usage, tool decisions/results, durations,
status codes (including 429 rate-limit detection), model, reasoning effort
and sandbox policy into one `v2` envelope per export.

## Environment

| Variable | Meaning |
| --- | --- |
| `TMUX_PANE` | Pane id; when unset the bridge is a silent no-op unless `SPACES_PLUGIN_ALLOW_NO_TMUX=1`. |
| `SPACES_PLUGIN_ALLOW_NO_TMUX=1` | Allow delivery outside tmux (tests, daemons). |
| `SPACES_HOOKS_RUNTIME` | Runtime dir override holding `hooks.endpoint.json`. |
| `XDG_RUNTIME_DIR` | Else `$XDG_RUNTIME_DIR/spaces`. |
| `SPACES_TMUX_SESSION` | Override for the cached `tmux display-message -p '#S'` session resolution (tests/embedding). |

Endpoint discovery mirrors `internal/hooks/core`: read
`$RUNTIME/hooks.endpoint.json` (`socket`, `tcp_port` fallback, `token`),
POST over UDS HTTP/1.1 with a 100 ms timeout, `X-ZJ-Token` header.

## Safety & design notes

- Every entry point exits 0 on all paths (missing/slow socket, bad JSON,
  missing rollout file, unset `$TMUX_PANE`).
- Subagent isolation: payloads carrying `agent_id` / `parent_thread_id`
  set `is_subagent: true` so child activity never overwrites the parent
  thread's `session_id`, `model` or `context_tokens`.
- `files_touched` is extracted from `apply_patch` patch bodies
  (`*** Update File:` / `*** Add File:` / `*** Delete File:`) plus standard
  file-argument keys (`file_path`, `path`, …).
- Rollout enrichment: when the hook payload carries `transcript_path` /
  `rollout_path` (Codex rollout JSONL), the bridge tail-reads the last
  128 KB to extract `info.total_token_usage` / `last_token_usage`,
  `model_context_window`, `rate_limits.primary` / `secondary` and
  compaction markers.

## Tests

```sh
cd plugins/codex-spaces
node --test
```

The suite (`test/plugin.test.mjs`) spins up a real mock Unix-domain-socket
HTTP server and covers modern stdin payloads for all seven events,
`apply_patch` file extraction, rollout JSONL token/rate-limit extraction,
legacy `notify` argv JSON, OTLP `/v1/logs` + `/v1/metrics` parsing
(including 429 detection), subagent isolation, fail-open on missing/slow
sockets, and the silent no-op when `$TMUX_PANE` is unset.

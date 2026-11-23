# pi-spaces — RICH Pi Coding Agent companion extension

Native TypeScript Pi extension for `tmux-spaces` (Design 1: rich harness-side
extractor). It reads **everything** Pi's `ExtensionAPI` exposes and pushes
structured `v2` envelopes to `spacecore` over UDS HTTP (`POST /v1/plugin/pi`).

Zero runtime dependencies. Works under Pi (jiti/tsx/esbuild) and under
Node 24's native type stripping (`node --test` runs directly, no build step).

## Install

Pi loads extensions from `~/.pi/agent/extensions/pi-spaces/` or
`.pi/extensions/`. Symlink or copy this directory there:

```sh
mkdir -p ~/.pi/agent/extensions
ln -s /path/to/tmux-spaces/plugins/pi-spaces ~/.pi/agent/extensions/pi-spaces
```

No install step, no bundling, no dependencies.

## Files

| File | Role |
| ---- | ---- |
| `index.ts` | Extension entrypoint (`export default function (pi: ExtensionAPI)`). Subscribes to every Pi lifecycle event, drives the tracker, emits envelopes. |
| `tracker.ts` | Rich in-process state tracker (sessions, turns, usage/cost, context window, compactions, thinking level, branch/tree, files touched, tools, errors). Pure state machine, no I/O. |
| `transport.ts` | Fail-open UDS HTTP/1.1 client (`node:http`/`node:fs`/`node:child_process`), 100 ms timeout, endpoint discovery, `X-ZJ-Token`, cached tmux session resolution, silent no-op without `$TMUX_PANE`. |
| `test/extension.test.mjs` | `node:test` suite against a real mock UDS HTTP server. |
| `package.json` | Zero deps, ESM, `"test": "node --test"`. |

## Events observed

Session: `session_start`, `session_switch`, `session_fork`, `session_tree`,
`session_shutdown` (session ID/file/name/branch, `ctx.cwd`, `ctx.model`,
`pi.getThinkingLevel()`, `pi.getActiveTools()`, branch-scan reconstruction of
token/cost totals, turn count, compaction count and title).

Agent & turns: `input` / `before_agent_start`, `agent_start`, `agent_end`,
`turn_start`, `turn_end` (prompt seeding, `working`/`blocked`/`done`/`failed`
states, assistant `usage`, live `ctx.getContextUsage()`).

Tools: `tool_call` / `tool_execution_start`, `tool_result` /
`tool_execution_end` (active-tool tracking, ask/question detection,
`write`/`edit`/`read` file-path extraction, durations, `recent_tools`,
`tool_call_count` / `tool_error_count`, `files_touched`).

Compaction & model: `session_before_compact`, `session_compact`,
`model_select` (counter, timestamp, reason, post-compaction context,
provider/model/window/thinking updates).

All subscriptions are defensive (`pi.on` / `ctx.sessionManager` / `ctx.model`
/ `ctx.getContextUsage` / `pi.getThinkingLevel` / `pi.getActiveTools` are all
optional) and every handler is wrapped in try/catch. The extension is
observe-only: it never returns `{ block: true }` and never mutates prompts or
tool args.

## Wire contract

`POST /v1/plugin/pi` over the runtime Unix socket with `Content-Type:
application/json`, `Content-Length` and `X-ZJ-Token`:

```json
{
  "v": 2,
  "agent": "pi",
  "session": "<tmux_session_name>",
  "pane": "<$TMUX_PANE>",
  "plugin": {
    "name": "pi-spaces",
    "version": "1.0.0",
    "capabilities": ["lifecycle", "usage", "context", "tools", "files", "compaction", "errors", "model", "thinking", "session_tree"]
  },
  "event": "<pi_native_event_name>",
  "ts_ms": 1700000000000,
  "pid": 12345,
  "harness_version": "<if_known>",
  "payload": { "...tracker state...", "raw_event": { "..." } }
}
```

Endpoint discovery mirrors the hooks core: `$SPACES_HOOKS_RUNTIME` (with
`${VAR}` expansion) → `$XDG_RUNTIME_DIR/spaces` → `/tmp/spaces-<uid>`, reading
`hooks.endpoint.json` (`{ socket, token }`).

Environment:

| Var | Meaning |
| --- | ------- |
| `TMUX_PANE` | Pane attribution. Unset → silent no-op. |
| `SPACES_PLUGIN_ALLOW_NO_TMUX=1` | Allow sending without `$TMUX_PANE` (tests). |
| `SPACES_HOOKS_RUNTIME` / `XDG_RUNTIME_DIR` | Runtime dir discovery. |
| `SPACES_SESSION` / `TMUX_SESSION_NAME` | Session override (tests / exotic setups). |
| `SPACES_HOOKS_DISABLE=1` | Global kill switch (never sends). |
| `SPACES_HARNESS_VERSION` | Fallback `harness_version` when Pi exposes none. |

## Test

```sh
cd plugins/pi-spaces
node --test
```

Covers the full lifecycle, live context extraction, cumulative usage/cost,
`files_touched`, `tool_error_count`, error → `failed`, missing/slow socket
fail-open, and the `$TMUX_PANE` silent no-op — all against a real mock UDS
HTTP server.

# opencode-spaces

Rich harness-side companion plugin for [tmux-spaces](https://github.com/arhammahajan/spaces):
it reads **everything OpenCode exposes** (bus events + interception hooks) and
pushes structured **v2 envelopes** to `spacecore` over UDS HTTP
(`POST /v1/plugin/opencode`).

## Install

Drop-in (single folder, zero runtime dependencies):

```sh
mkdir -p ~/.config/opencode/plugins
cp -r plugins/opencode-spaces ~/.config/opencode/plugins/opencode-spaces
```

or as an npm dependency:

```sh
npm i opencode-spaces
```

then import `./index.mjs` from your OpenCode config. The default export is the
OpenCode plugin function:

```js
export default async ({ project, client, $, directory, worktree }) => ({ ... });
```

Requires Node.js >= 18. No `npm install` needed — there are no dependencies.

## How it works

- `index.mjs` — plugin entry: wires every OpenCode hook, builds v2 envelopes.
- `tracker.mjs` — in-process state: sessions, subagents, tools (in-flight +
  recent with durations), token/cost usage, context %, files touched, todos,
  MCP servers, LSP diagnostics, pending permissions/questions, compaction and
  retry/rate-limit state. Subagent sessions (detected via `parentID`) are
  tracked separately and **never** overwrite the root session's
  `session_id`, `model`, `context_tokens`, or `usage`; root payloads carry a
  `subagents` summary array.
- `transport.mjs` — fail-open UDS HTTP/1.1 client: 100ms timeout,
  fire-and-forget, never throws.

Hook coverage:

| Source | Events |
| --- | --- |
| `event` bus | `session.created/updated/deleted/status/idle/error/compacted/diff`, `message.updated`, `message.part.updated` (tool/text/reasoning/step-finish/compaction/subtask), `permission.asked/updated/replied`, `question.asked/replied/rejected`, `todo.updated`, `file.edited`, `lsp.client.diagnostics`/`lsp.updated`, `mcp.tools.changed`/`mcp.browser.open.failed`, `vcs.branch.updated` |
| interception | `tool.execute.before/after` (in-flight tracking, duration, error counts), `chat.params` (exact model/provider/variant/context limit), `experimental.session.compacting` (reason) |

High-frequency streaming `message.part.updated` text deltas are coalesced to at
most one push per 250ms; lifecycle/tool/permission/error/idle events are always
sent immediately. Every handler is wrapped in try/catch and observes only —
the plugin never mutates OpenCode inputs and never returns control-flow values.

## Wire contract

`POST /v1/plugin/opencode` with `Content-Type: application/json`,
`Content-Length`, and `X-ZJ-Token`:

```json
{
  "v": 2,
  "agent": "opencode",
  "session": "<tmux_session_name>",
  "pane": "<$TMUX_PANE>",
  "plugin": {
    "name": "opencode-spaces",
    "version": "1.0.0",
    "capabilities": ["lifecycle", "usage", "context", "tools", "files", "todos", "subagents", "permissions", "questions", "mcp", "lsp", "compaction", "errors", "vcs"]
  },
  "event": "<native_event_name>",
  "ts_ms": 1700000000000,
  "pid": 12345,
  "harness_version": "<if_known>",
  "payload": { "...": "... see index.mjs buildEnvelope / tracker.mjs snapshot ..." }
}
```

## Configuration (environment)

| Variable | Purpose | Default |
| --- | --- | --- |
| `TMUX_PANE` | pane identity; when unset the plugin silently no-ops | — |
| `SPACES_PLUGIN_ALLOW_NO_TMUX=1` | opt in to emitting without tmux (tests/headless) | off |
| `SPACES_HOOKS_RUNTIME` | dir (or direct `.sock` path) containing `hooks.sock`/`spacecore.sock`/`spaces.sock` | — |
| `SPACES_HOOKS_SOCKET` | explicit socket path override | — |
| `XDG_RUNTIME_DIR` | `$XDG_RUNTIME_DIR/spaces` is probed | — |
| `/tmp/spaces-<uid>` | final discovery fallback | — |
| `SPACES_HOOKS_TOKEN` (`SPACES_TOKEN`, `ZJ_TOKEN` fallbacks) | `X-ZJ-Token` header value | `""` |
| `SPACES_HOOKS_TIMEOUT_MS` | POST timeout | `100` |
| `SPACES_TMUX_SESSION` | override tmux session resolution (tests) | — |

## Test

```sh
cd plugins/opencode-spaces
node --test
```

The suite spins up a real mock Unix domain socket HTTP server and verifies the
v2 envelopes and headers end to end: lifecycle, tokens/cost/context, tool
timing, permissions, questions, diffs, todos, LSP, VCS, compaction, retry /
rate-limit, subagent isolation, text throttling, missing-socket and hanging-
socket fail-open, and the no-`TMUX_PANE` silent no-op.

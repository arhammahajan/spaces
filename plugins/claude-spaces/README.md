# claude-spaces

A distributable [Claude Code plugin](https://code.claude.com/docs/en/plugins.md)
that bridges Claude Code lifecycle hooks and the `statusLine` live metrics
snapshot into [tmux-spaces](https://github.com/arhammahajan/spaces) `spacecore`
over Unix-domain-socket HTTP.

Design: comprehensive lifecycle hooks + `statusLine` live metrics bridge +
rich JSONL/transcript extraction, pushing shared **v2 envelopes** to
`POST /v1/plugin/claude`.

Zero runtime dependencies. Node.js >= 18 (builtins only).

## Layout

```
plugins/claude-spaces/
  .claude-plugin/plugin.json   # plugin manifest (no hooks key — hooks live in hooks/hooks.json)
  hooks/hooks.json             # wiring for all 17 Claude Code lifecycle hooks
  package.json                 # ESM, zero deps, "test": "node --test"
  scripts/transport.mjs        # fail-open UDS HTTP/1.1 client (100ms budget) + endpoint discovery
  scripts/hook-bridge.mjs      # hook stdin JSON -> v2 envelope -> POST /v1/plugin/claude
  scripts/statusline-bridge.mjs# statusLine snapshot -> v2 metrics envelope + compact status line
  test/plugin.test.mjs         # comprehensive node:test suite (mock UDS server)
```

## Install

```bash
# from the tmux-spaces checkout
/plugin marketplace add ./plugins/claude-spaces   # or copy the directory to your plugin dir
/plugin install claude-spaces
```

Then point the plugin at the running listener (automatic when you work
inside tmux — the listener endpoint and `$TMUX_PANE` are discovered):

```bash
export SPACES_HOOKS_RUNTIME="${XDG_RUNTIME_DIR:-/tmp}/spaces"  # default discovery; usually unset
```

### statusLine

Claude Code reads `statusLine` from settings, so add the bridge there to
get live model/usage/cost/context metrics:

```json
{
  "statusLine": {
    "type": "command",
    "command": "node \"/path/to/claude-spaces/scripts/statusline-bridge.mjs\""
  }
}
```

To keep your existing status line and still send telemetry, set a
passthrough instead — the snapshot is piped to your command and its stdout
is shown:

```bash
export SPACES_CLAUDE_STATUSLINE_PASSTHROUGH="my-statusline --fast"
```

## Wire contract (`POST /v1/plugin/claude`)

Headers: `Content-Type: application/json`, correct `Content-Length`, and
`X-ZJ-Token: <token from hooks.endpoint.json>`.

```json
{
  "v": 2,
  "agent": "claude",
  "session": "<tmux session name>",
  "pane": "<$TMUX_PANE>",
  "plugin": {
    "name": "claude-spaces",
    "version": "1.0.0",
    "capabilities": ["lifecycle", "usage", "context", "cost", "tools", "files", "todos", "subagents", "background_tasks", "permissions", "questions", "compaction", "statusline", "errors"]
  },
  "event": "<hook event name or \"statusline\">",
  "ts_ms": 1700000000000,
  "pid": 12345,
  "harness_version": "<Claude Code version from statusline>",
  "payload": { "...": "..." }
}
```

The payload carries `session_id`, `cwd`, `model`, `mode`, `state`, `kind`,
`text`, `prompt`, `reply`, `tool` / `tool_call_id` / `tool_input` /
`tool_output`, `usage` (`input_tokens`, `output_tokens`,
`cache_read_tokens`, `cache_write_tokens`, `cost_usd`), `context_tokens` /
`context_window` / `context_percent`, `compaction_count`, `files_touched`,
`todos`, `background_tasks`, `pending_permission`, `last_error`, `extras`,
and the untouched `raw_event`.

## Behavior notes

- **Always exit 0.** `hook-bridge.mjs` never prints to stdout and never
  exits non-zero, so telemetry can never block tool execution.
  `statusline-bridge.mjs` prints exactly one status line.
- **Fail-open.** Missing endpoint, bad token, dial errors, slow listener
  (>100ms budget), unreadable transcripts, and malformed stdin all degrade
  to silent no-ops (diagnostics, if any, go to stderr — never to the
  statusline stdout).
- **Silent without tmux.** When `$TMUX_PANE` is unset nothing is sent;
  set `SPACES_PLUGIN_ALLOW_NO_TMUX=1` to send anyway (pane `no-tmux`),
  e.g. for tests.
- **Subagent isolation.** When `agent_id`/`agent_type` is present (or the
  event is `SubagentStart`/`SubagentStop`), the payload is marked
  `is_subagent` with `parent_session_id`/`agent_id`/`agent_type`, and the
  parent's `model`/`context_tokens` are left unset so subagent traffic
  never clobbers the parent session record.
- **Transcript fallback.** On `Stop`/`PostCompact`/`SessionEnd` the bridge
  tail-reads `transcript_path` (last 256 KiB, bounded and synchronous) to
  recover cumulative token usage, model, turn count, and compaction count
  for turns where no statusline snapshot fired yet.
- **Endpoint discovery:** `$SPACES_HOOKS_RUNTIME` →
  `$XDG_RUNTIME_DIR/spaces` → `<tmpdir>/spaces-<uid>`, reading
  `hooks.endpoint.json` (`version: 1`, `socket`, `token`).
- **Session resolution:** `$SPACES_PLUGIN_SESSION` override, else
  `tmux display-message -p '#S'` (cached per process).

## Test

```bash
cd plugins/claude-spaces
node --test
```

The suite validates `plugin.json` / `hooks/hooks.json` schema, every hook
event mapping, `files_touched`/`todos` extraction, transcript JSONL usage
parsing, full `statusLine` snapshot parsing (model, context window, cost,
workspace, version), passthrough delegation, and all fail-open guarantees
against a real mock UDS HTTP server.

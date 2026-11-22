# tmux-spaces

Native tmux port of `plugins/zellij-spaces`: spaces are tmux sessions,
windows are tmux windows, and agent attention state lives in native
per-pane tmux options (`@spaces_*`). No Zellij required at runtime.

The Zellij plugin (`plugins/zellij-spaces`) and the `spaces` helpers keep
working untouched — this plugin only adds a tmux path.

## Install

### TPM (recommended)

Add to `~/.tmux.conf`:

```tmux
set -g @plugin 'tmux-plugins/tpm'
set -g @plugin 'tmux-spaces'   # path to this directory
run '~/.tmux/plugins/tpm/tpm'
```

Then `prefix + I` to install. The plugin script (`tmux-spaces.tmux`)
finds the `tmux-spaces` binary next to itself, on `PATH`, or builds it
with `go` when available.

### Manual

```sh
cd plugins/tmux-spaces
./build.sh            # runs tests, writes ./tmux-spaces
```

Then source the plugin from `~/.tmux.conf`:

```tmux
run-shell /path/to/plugins/tmux-spaces/tmux-spaces.tmux
```

## Keybindings

All keys are bound in the `prefix` table.

| Key (default)   | Command              | Action                        |
| --------------- | -------------------- | ----------------------------- |
| `prefix + s`    | `tmux-spaces popup`  | Navigator (spaces/panes/TUI)  |
| `prefix + i`    | `tmux-spaces inbox`  | Attention Inbox (agents + live previews across spaces) |
| `prefix + y`    | `tmux-spaces conversations` | Conversations (fuzzy + full-message search & resume across harnesses) |
| `prefix + u`    | `tmux-spaces shared` | Shared MCP servers & skills browser (`~/.agents` + stowed harness configs) |
| `prefix + T`    | `tmux-spaces tabs`   | Tabs inspector (all tabs in current workspace with full pane, git/worktree/PR & agent telemetry) |
| `prefix + c`    | `tmux-spaces tabs --new` | Create a new tab inside the Tabs popup (optional name + existing/new git worktree) |
| `prefix + a`    | `tmux-spaces agents` | Agents selector TUI           |
| `prefix + N`    | `tmux-spaces popup --new` | Create a new space (with optional git worktree) |
| `prefix + W`    | `tmux-spaces worktrees` | Git worktrees manager & cleanup TUI |

## tmux options

The plugin places tmux's status bar, including the window tabs, at the bottom.
To move it to the top, set `status-position top` after loading the plugin.

Set these before the `run-shell` line (shown with defaults):

```tmux
set -g @spaces-key 's'               # Navigator key
set -g @spaces-inbox-key 'i'         # Attention Inbox key
set -g @spaces-conversations-key 'y' # Conversations search/resume key
set -g @spaces-shared-key 'u'        # Shared MCP & skills browser key
set -g @spaces-shared-dir ''         # Override shared harnesses dir (default ~/.agents)
set -g @spaces-tabs-key 'T'          # Current-workspace Tabs inspector key
set -g @spaces-new-tab-key 'c'       # New-tab key (opens Tabs popup in new-tab flow; set 'off' to keep tmux default)
set -g @spaces-agents-key 'a'        # Agents selector key
set -g @spaces-new-key 'N'           # New-space key
set -g @spaces-worktrees-key 'W'     # Worktrees manager key
set -g @spaces-popup-width '86%'     # display-popup width
set -g @spaces-popup-height '82%'    # display-popup height
set -g @spaces-border 'rounded'      # display-popup border style
set -g @spaces-pane-border 'on'      # show agent (and `— <profile>`) on top pane border
set -g @spaces-detach-on-destroy 'off' # switch to most recent open space instead of detaching when closing a space's last pane/tab
set -g @spaces-auto-restore 'on'     # auto-restore last-open (non-exited) spaces on fresh tmux start
```

## Git worktree-backed spaces & tabs

- **`prefix + c` / `n` in Tabs (New tab):** Opens the Tabs popup's New Tab dialog inside the current workspace with an optional tab name (leave empty to preserve tmux's `automatic-rename` showing the active process) and lets you pick an **Existing** worktree or create a **New worktree** (`Esc` returns to the Tabs popup).
- **`prefix + N` / `n` in Spaces (New space):** Includes the same Existing / New worktree picker when the chosen folder is inside a git repository, with optional Space name (auto-derived from the worktree when omitted).
- **Auto-assigned worktree names:** When the Branch field is left empty, names are assigned in priority order: explicit branch → tab/space name → active coding harness conversation title → active harness prompt → `<agent>-<adj>-<noun>` → collision-free `<adj>-<noun>` fallback. Press `Ctrl-g` to regenerate, or `Ctrl-t` / `Alt-w` to toggle Existing vs New worktree.
- **Display & management:** Linked worktrees display `⎇ <branch>` badges, multi-worktree indicators (`⎇ N wt`), and worktree details (`wt <name> (<repo>) · base <base>`, `Worktree`, and `Main repo` rows) across the **Spaces**, **Tabs**, and **Inbox** popups. Press `W` in Spaces or Tabs (or `prefix + W`) for the Worktrees manager (`Enter` to jump/open, `x` for safe removal with dirty/unmerged checks, `p` to prune stale & merged orphaned worktrees), or toggle `w` when closing a worktree-backed space or tab.
- **Storage overrides:** Managed worktrees default to `${XDG_DATA_HOME:-~/.local/share}/tmux-spaces/worktrees/<repo>/<name>` and metadata to `worktrees.json` (overridable via `TMUX_SPACES_WORKTREE_DIR`, `TMUX_SPACES_WORKTREE_STATE`, and `TMUX_SPACES_WORKTREE_PREFIX`).

## Status bar integration

Put the placeholder where you want the attention badge:

```tmux
set -g status-right '#{spaces_status} %H:%M '
```

On load the plugin rewrites `#{spaces_status}` to
`#(tmux-spaces status-bar)`. With no placeholder anywhere, the badge is
appended to `status-right` once. Reloading is idempotent.

The badge shows hot counts with Catppuccin Mocha colors
(`●2 ◐1 ✓3`) and prints nothing when every pane is idle, so the
status line stays clean. Manual equivalent:

```tmux
set -g status-right '#(tmux-spaces status-bar) %H:%M '
```

## Hook setup

Agent hooks work with zero tmux-specific configuration. The existing
hook client (`internal/harness/spaceresume`, driven by `zj hook` /
`harness` hooks) detects the tmux environment automatically:

- When `ZELLIJ_SESSION_NAME` is set, everything behaves exactly as
  before (OSC 7501 to the pane tty, Zellij session ids).
- When it is empty but `TMUX` + `TMUX_PANE` are set, the hook resolves
  the tmux session via `tmux display-message` and publishes to the hook
  store with the numeric pane id, and `EmitProgramStatus` projects onto
  native per-pane options instead of writing OSC bytes to `/dev/tty`:
  - `@spaces_state` — `working`, `idle`, `done`, `blocked`, `error`, ...
  - `@spaces_app` — agent app label (`claude-code`, `codex`, ...)
  - `@spaces_profile` — harness CLI profile name when opened through `harness`
  - `@spaces_kind` — block kind (unset when empty)
  - `@spaces_msg` — detail message (unset when empty)
  - `@spaces_ts` — Unix milliseconds of the last update

So configuring hooks for your agent (as documented for `spaces`) is all it
takes; inside tmux the states land on the pane options above, which the
badge, `panes`, and `wait` commands read.

## CLI reference

```sh
tmux-spaces status-bar [--plain] [--json]
```

Badge segment for the status line. Styled with `#[fg=...]` colors by
default, `--plain` for `●2 ◐1 ✓3`, `--json` for counts. Empty output
when all panes are idle.

```sh
tmux-spaces space [list|current|new|switch|rename|close] ...
tmux-spaces spaces ...              # alias
```

- `space list` — table of sessions with window and needs/failed/work/done counts
- `space current` — print the current session name
- `space new <name> [cwd]` — create a session
- `space switch|open <name>` — switch to a session
- `space rename <new> [old]` — rename (defaults to current)
- `space close|kill [name] [--yes]` — kill (prompts on a tty, refuses without `--yes` otherwise)

```sh
tmux-spaces panes [--session S] [--json]
```

List panes with status, app, window, pane id, title, and CWD.

```sh
tmux-spaces status <idle|working|done|blocked|error|clear> [--app A] [--kind K] [--msg M] [--pane %N]
```

Set `@spaces_*` on the current (or target) pane. `clear` unsets all
five options. `--kind`/`--msg` with an empty value unset their option.
(`failed` is accepted as an alias of `error`.)

```sh
tmux-spaces prompt <target> <text...>
tmux-spaces read <target> [--lines N]
```

Type literal text + Enter into a pane (`send-keys -l`), or capture a
pane viewport (`capture-pane`).

```sh
tmux-spaces wait <target> [--until done|failed|needs_you|idle|working|any] [--timeout 60s]
```

Poll a pane (or session rollup) until the condition holds. `any` matches
`needs_you`, `failed`, or `done`. Timeout accepts Go durations or bare
seconds.

```sh
tmux-spaces next
```

Jump to the hottest attention pane (`NeedsYou` > `Failed` > `Done`)
across all sessions.

```sh
tmux-spaces agent <name> [--profile P] [--session S] [--cwd D] [-- args...]
```

Open an agent in a new window under `tmux-spaces __shim`. With
`--profile`, the pane runs `harness spaces __shim-profile <agent>/<profile> ...`
wrapped by `__shim` so status reporting is identical.

```sh
tmux-spaces __shim [--hold] <app> <bin> [args...]
```

Pane-side wrapper: primes `@spaces_state` through `working`/`idle`,
runs the child, records `done`/`failed` on exit, and propagates the
exit code. `--hold` drops into `$SHELL -l` afterwards.

```sh
tmux-spaces doctor [--json]
```

Check the tmux binary, server, session context, `spaces`/`harness`
availability, and hook listener health. Exits non-zero when tmux or the
server is missing.

```sh
tmux-spaces popup [--new]
tmux-spaces worktrees|wt [list|new|remove|prune] ...
tmux-spaces inbox
tmux-spaces conversations [--query Q]
tmux-spaces chats [--query Q]       # alias
tmux-spaces shared [--dir DIR] [--query Q]
tmux-spaces skills|mcp [--dir DIR] [--query Q] # aliases
tmux-spaces tabs [--new] [--session SPACE] [--query Q]
tmux-spaces tab [--new] [--session SPACE] [--query Q]  # alias
tmux-spaces agents
```

Open the Navigator / Attention Inbox / Conversations / Shared MCP & Skills / Tabs / Agents TUI (wired by the full binary).
In the Tabs popup (`prefix + T`), inspect every open tab in the current workspace in a split
master-detail view with multi-pane breakdown, live foreground process command lines, full git
branch/diff/PR/CI details, and full scrollable agent telemetry (state/event/model/turn timings,
session ID, waiting-on callouts, wrapped prompt, tool call input & result, latest reply, and
complete subagents/background jobs/services trees; `↑↓`/`jk` switch tabs, `←→`/`hl` cycle split
panes inside a tab, `J`/`K` or `[`/`]` scroll telemetry, `v` expand telemetry width, `0`–`9` jump
to window index, `Enter` focus pane, `/` filter, `Tab` or `b`/`w`/`d`/`i`/`a` status filters, `s`
cycle sort order `index`/`attention`/`activity`, `n` new tab, `r` rename tab, `x` close tab, `H`
full hook overlay).
In the Conversations popup (`prefix + y`), start typing immediately for instant fuzzy filtering
plus debounced full-message search across all coding harnesses (use `@<agent>` inline filters,
`Tab`/`Shift-Tab` to cycle harnesses, `Alt-c` for current workspace, `Ctrl-s` to cycle search
scope, `Ctrl-a` to include archived/`/tmp` chats, `Enter` to focus a live pane or resume in a new
tab, and `Ctrl-o` to always open in a new tab).
In the Shared MCP & Skills popup (`prefix + u`), start typing to fuzzy-filter shared MCP servers
and skills from `~/.agents` and stowed harness configs (`Tab`/`Shift-Tab` cycles `all` / `mcp` / `skills`,
`Enter` opens a scrollable detail inspector, `Ctrl-o` opens the source file in your editor, and
`Ctrl-r` reloads from disk).

## Adding new coding agents

Agent-specific behavior (identity/aliases, screen reading, title extraction, hook arbitration policy,
conversation store/log parsing hooks, live session discovery, launch/resume metadata, and MCP config
paths) is encapsulated behind the overarching `agent.Plugin` interface in `internal/agent` with
per-agent plugins in `internal/agent/plugins`. See [docs/agent-plugins.md](docs/agent-plugins.md)
for the architecture overview and a quick-start guide to adding a new coding agent.

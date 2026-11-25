// Package pluginsfs embeds the harness companion plugin sources shipped
// under plugins/*-spaces so `tmux-spaces plugins install` works from a
// standalone binary without needing the repo checkout.
//
// The `all:` prefix keeps dot-directories (e.g.
// claude-spaces/.claude-plugin) in the embedded tree. Go code installs the
// files anywhere on disk; nothing here is ever executed in-process.
package pluginsfs

import "embed"

// FS holds the full source trees of every companion plugin:
// opencode-spaces, pi-spaces, claude-spaces and codex-spaces.
//
//go:embed all:opencode-spaces all:pi-spaces all:claude-spaces all:codex-spaces
var FS embed.FS

# Coding Agent Plugin Architecture

`tmux-spaces` separates **shared tmux/UI/lifecycle infrastructure** from **agent-specific behavior** behind one overarching plugin interface in [`internal/agent`](../internal/agent/plugin.go) and per-agent implementations in [`internal/agent/plugins`](../internal/agent/plugins/register.go).

## Shared vs. Agent-Specific Boundaries

- **Shared (agent-agnostic):** tmux client & pane options (`@spaces_*`), OSC 7501 streaming parser (`internal/osc`), generic manifest screen-rule engine (`internal/screen`), status rollups & TTLs, TUI views (`internal/tui`), git/PR telemetry (`internal/gitinfo`), worktree manager/store (`internal/worktree`), resurrect layout store (`internal/resurrect`), fuzzy search, shared skills parser, and generic JSON/TOML MCP parsers.
- **Agent-specific (via `agent.Plugin`):**
  - **Identity & Naming (`Spec`):** canonical ID, aliases, UI/CLI display labels, conversation ID, worktree short prefix, versioned/bare-version binary matching, OSC title prefixes, version banner names, and boilerplate titles.
  - **Screen Reading (`ScreenReader`):** declarative manifest rules (`internal/screen/manifests.json`) or custom Go viewport classifiers.
  - **Title Extraction (`TitleExtractor`):** agent-specific footer, tab-bar, status-line, or prompt parsers.
  - **Hooks & Arbitration (`HookPolicy`):** OSC 7501 authority and custom hook-vs-screen idle-yield rules (e.g., Antigravity's 10s idle grace for missed `Stop` hooks).
  - **Conversation Store & Log Parsing (`ConversationStore`, `LogParser`):** optional native conversation store readers/searchers and transcript/session log parsers (returning `nil` falls back to the shared `spaces` store).
  - **Session Resolution (`SessionResolver`):** CLI argv session ID extraction (flags or positional `resume <id>`), live process FD session discovery, multi-pane CWD title suppression, and restore-time session deduplication.
  - **Launching (`Launcher`):** direct executable binary name and `harness` CLI management flag.
  - **Configuration (`ConfigProvider`):** stowed `$HOME` MCP configuration paths.

## Adding a New Agent Plugin

1. Create a new file in `internal/agent/plugins/<name>.go` embedding `*agent.BasePlugin`:

```go
package plugins

import "github.com/arhammahajan/spaces/internal/agent"

func NewMyAgent() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "my-agent",
			Aliases:                 []string{"mya", "my-agent-cli"},
			ScreenManifestID:        "my-agent", // optional entry in internal/screen/manifests.json
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"MYA | "},
			WorktreeTitlePrefixes:   []string{"mya | ", "my-agent - "},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "my-agent", RelPath: ".my-agent/mcp.json"},
		},
		// Override only the capabilities that differ from defaults:
		// CustomScreenReader:      ...,
		// CustomTitleExtractor:    ...,
		// CustomHookPolicy:        ...,
		// CustomConversationStore: ...,
		// CustomLogParser:         ...,
		// CustomSessionResolver:   ...,
		// CustomLauncher:          ...,
		// CustomConfigProvider:    ...,
	}
}
```

2. Register `NewMyAgent()` in `All()` inside [`internal/agent/plugins/register.go`](../internal/agent/plugins/register.go).
3. Add a unit test in [`internal/agent/plugins/plugins_test.go`](../internal/agent/plugins/plugins_test.go) and run `go test ./...`.

Once registered, process detection, alias normalization, screen reading, title extraction, hook arbitration, conversation filtering, MCP discovery, CLI launching, resurrect capture/restore, TUI title cleanup, and worktree auto-naming all pick up the new agent automatically with zero edits to other packages.

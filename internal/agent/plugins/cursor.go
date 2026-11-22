package plugins

import "github.com/arhammahajan/spaces/internal/agent"

// NewCursor returns the coding-agent plugin for Cursor Agent CLI.
func NewCursor() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "cursor-agent",
			Aliases:                 []string{"cursor", "cursor_agent", "cursor-cli"},
			DisplayApp:              "cursor-agent",
			CLIDisplayApp:           "cursor-agent",
			ConversationID:          "cursor-agent",
			ShortName:               "cursor",
			Binary:                  "cursor-agent",
			ScreenManifestID:        "cursor",
			HarnessManaged:          false,
			OSCAuthority:            false,
			RecognizesVersionBanner: true,
			WorktreeTitlePrefixes:   []string{"cursor - ", "cursor: ", "cursor | "},
		},
	}
}

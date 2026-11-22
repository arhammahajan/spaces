package plugins

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

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
			TitlePrefixes:           []string{"Cursor - ", "Cursor: ", "Cursor Agent - "},
			WorktreeTitlePrefixes:   []string{"cursor - ", "cursor: ", "cursor | "},
			BoilerplateTitles:       []string{"cursor", "cursor agent"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "cursor", RelPath: ".cursor/mcp.json"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(extractCursorTitle),
	}
}

// extractCursorTitle parses Cursor Agent CLI session headers or prompt lines,
// falling back to generic prompt extraction.
func extractCursorTitle(viewport string) string {
	top := screen.TopNonEmptyLines(viewport, 4)
	for _, raw := range screen.SplitLines(top) {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		for _, prefix := range []string{"cursor agent · ", "cursor · ", "session: "} {
			if strings.HasPrefix(lower, prefix) {
				if t := screen.CleanViewportTitle(line[len(prefix):]); t != "" {
					return t
				}
			}
		}
	}
	for _, marker := range []string{"›", "❯", ">"} {
		if t := screen.ExtractPromptMarkerLine(viewport, marker); t != "" {
			return t
		}
	}
	return screen.ExtractGenericTitle(viewport)
}

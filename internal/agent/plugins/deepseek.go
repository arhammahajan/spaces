package plugins

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

// NewDeepSeek returns the coding-agent plugin for DeepSeek Harness (`dsh` / `dsh-tui`).
func NewDeepSeek() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "deepseek",
			Aliases:                 []string{"dsh", "dsh-tui"},
			ResumeFlag:              "--session",
			DisplayApp:              "deepseek",
			CLIDisplayApp:           "deepseek",
			ConversationID:          "deepseek",
			ShortName:               "deepseek",
			ScreenManifestID:        "deepseek",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"DeepSeek - ", "DeepSeek: "},
			WorktreeTitlePrefixes: []string{
				"deepseek - ", "deepseek: ", "deepseek | ",
				"dsh - ", "dsh: ", "dsh | ",
			},
			BoilerplateTitles: []string{"deepseek", "deepseek harness"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "deepseek", RelPath: ".deepseek/mcp.json"},
			{HarnessLabel: "deepseek", RelPath: ".config/deepseek/mcp.json"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(extractDeepSeekTitle),
	}
}

// extractDeepSeekTitle parses DeepSeek Harness (`dsh-tui`) header/session lines
// or prompt lines, falling back to generic prompt extraction.
func extractDeepSeekTitle(viewport string) string {
	top := screen.TopNonEmptyLines(viewport, 4)
	for _, raw := range screen.SplitLines(top) {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		for _, prefix := range []string{"deepseek · ", "dsh · ", "dsh-tui · ", "session: "} {
			if strings.HasPrefix(lower, prefix) {
				if t := screen.CleanViewportTitle(line[len(prefix):]); t != "" {
					return t
				}
			}
		}
	}
	for _, marker := range []string{"❯", ">"} {
		if t := screen.ExtractPromptMarkerLine(viewport, marker); t != "" {
			return t
		}
	}
	return screen.ExtractGenericTitle(viewport)
}

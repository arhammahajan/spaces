package plugins

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

// NewKimi returns the coding-agent plugin for Kimi CLI / Kimi Code.
func NewKimi() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "kimi",
			Aliases:                 []string{"kimi-cli", "kimi-code", "kimi code"},
			DisplayApp:              "kimi",
			CLIDisplayApp:           "kimi",
			ConversationID:          "kimi",
			ShortName:               "kimi",
			ResumeFlag:              "--session",
			ScreenManifestID:        "kimi",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"Kimi - ", "Kimi: ", "Kimi Code - "},
			WorktreeTitlePrefixes: []string{
				"kimi - ", "kimi: ", "kimi | ",
				"kimi code - ", "kimi code: ", "kimi code | ",
			},
			BoilerplateTitles: []string{"kimi", "kimi cli", "kimi code"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "kimi-cli", RelPath: ".kimi/mcp.json"},
			{HarnessLabel: "kimi-cli", RelPath: ".kimi/config.toml"},
			{HarnessLabel: "kimi-code", RelPath: ".kimi-code/mcp.json"},
			{HarnessLabel: "kimi-code", RelPath: ".kimi-code/config.toml"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(extractKimiTitle),
	}
}

// extractKimiTitle parses Kimi CLI status headers or prompt lines (`❯` / `>`),
// falling back to generic prompt extraction.
func extractKimiTitle(viewport string) string {
	top := screen.TopNonEmptyLines(viewport, 4)
	for _, raw := range screen.SplitLines(top) {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		for _, prefix := range []string{"kimi code · ", "kimi · ", "session: "} {
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

package plugins

import (
	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

// NewClaude returns the coding-agent plugin for Claude Code.
func NewClaude() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "claude",
			Aliases:                 []string{"claude-code", "claude_code", "claudecode"},
			DisplayApp:              "claude",
			CLIDisplayApp:           "claude-code",
			ConversationID:          "claude",
			ShortName:               "claude",
			ResumeFlag:              "--resume",
			ScreenManifestID:        "claude",
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			MatchesBareVersion:      true,
			TitlePrefixes:           []string{"Claude Code - ", "Claude Code: ", "Claude - ", "Claude: "},
			WorktreeTitlePrefixes: []string{
				"claude code - ", "claude code: ", "claude code | ",
				"claude - ", "claude: ", "claude | ",
			},
			BoilerplateTitles: []string{"claude code", "claude"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "claude", RelPath: ".claude.json"},
			{HarnessLabel: "claude", RelPath: ".claude/mcp.json"},
			{HarnessLabel: "claude", RelPath: ".claude/settings.json"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(func(viewport string) string {
			return screen.ExtractPromptMarkerLine(viewport, "❯")
		}),
		CustomCompanionPlugin: NewClaudeCompanion(),
	}
}

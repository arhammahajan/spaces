package plugins

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

// NewCodex returns the coding-agent plugin for OpenAI Codex CLI.
func NewCodex() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "codex",
			Aliases:                 []string{"openai-codex"},
			ResumeSubcommand:        "resume",
			ScreenManifestID:        "codex",
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"Codex - ", "Codex: "},
			WorktreeTitlePrefixes:   []string{"codex - ", "codex: ", "codex | "},
			BoilerplateTitles:       []string{"codex", "openai codex"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "codex", RelPath: ".codex/config.toml"},
		},
		CustomTitleExtractor:  agent.TitleExtractorFunc(extractCodexTitle),
		CustomSessionResolver: agent.BaseSessionResolver{PositionalResume: true, UniqueExplicitSessions: true},
	}
}

// extractCodexTitle parses Codex footer lines (`<model> <effort> · <cwd> · <Title>`)
// or the latest user prompt line (`› <prompt>`).
func extractCodexTitle(viewport string) string {
	bottom := screen.BottomNonEmptyLines(viewport, 6)
	for _, raw := range screen.SplitLines(bottom) {
		line := strings.TrimSpace(raw)
		if !strings.Contains(line, " · ") {
			continue
		}
		if strings.Contains(line, "limit:") || strings.Contains(line, "resets at") || strings.Contains(line, "warnings") {
			continue
		}
		parts := strings.Split(line, " · ")
		if len(parts) >= 3 && screen.IsPathSegment(parts[1]) {
			if t := screen.CleanViewportTitle(strings.Join(parts[2:], " · ")); t != "" {
				return t
			}
		}
	}
	if t := screen.ExtractPromptMarkerLine(viewport, "›"); t != "" {
		return t
	}
	return ""
}

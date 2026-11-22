package plugins

import (
	"regexp"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

var museTokenMeterRe = regexp.MustCompile(`\s+\d+(?:\.\d+)?[kKmM]?/\d+(?:\.\d+)?[kKmM]?(?:\s*\(\d+(?:\.\d+)?%\))?\s*$`)

// NewMuse returns the coding-agent plugin for Muse CLI.
func NewMuse() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "muse",
			Aliases:                 []string{"muse-code", "muse-cli"},
			ScreenManifestID:        "muse",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"Muse - ", "Muse: "},
			WorktreeTitlePrefixes:   []string{"muse - ", "muse: ", "muse | "},
			BoilerplateTitles:       []string{"muse", "muse cli"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "muse", RelPath: ".config/muse/settings.json"},
		},
		CustomTitleExtractor:  agent.TitleExtractorFunc(extractMuseTitle),
		CustomSessionResolver: agent.BaseSessionResolver{PositionalResume: true},
	}
}

// extractMuseTitle parses Muse footer lines (`<model> · <effort> · <cwd> · <Title> [tokens]`)
// or user prompt lines (`❯ <prompt>`).
func extractMuseTitle(viewport string) string {
	bottom := screen.BottomNonEmptyLines(viewport, 5)
	for _, raw := range screen.SplitLines(bottom) {
		line := strings.TrimSpace(raw)
		parts := strings.Split(line, " · ")
		if len(parts) >= 4 && screen.IsEffortSegment(parts[1]) && screen.IsPathSegment(parts[2]) {
			rawTitle := museTokenMeterRe.ReplaceAllString(strings.Join(parts[3:], " · "), "")
			if t := screen.CleanViewportTitle(rawTitle); t != "" {
				return t
			}
		}
	}
	if t := screen.ExtractPromptMarkerLine(viewport, "❯"); t != "" {
		return t
	}
	return ""
}

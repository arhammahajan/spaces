package plugins

import (
	"regexp"
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

var (
	opencodeTabLeadRe = regexp.MustCompile(`^[‹<]\d+\s+`)
	multiSpaceRe      = regexp.MustCompile(`\s{2,}`)
)

// NewOpenCode returns the coding-agent plugin for OpenCode.
func NewOpenCode() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "opencode",
			Aliases:                 []string{"open-code", "herdr:opencode", "oc"},
			ResumeFlag:              "--session",
			ScreenManifestID:        "opencode",
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
			TitlePrefixes:           []string{"OC | ", "OpenCode - ", "OpenCode: "},
			WorktreeTitlePrefixes: []string{
				"oc | ", "oc: ", "oc - ",
				"opencode | ", "opencode - ", "opencode: ",
			},
			BoilerplateTitles: []string{"opencode", "open code"},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "opencode", RelPath: ".config/opencode/opencode.json"},
		},
		CustomTitleExtractor:  agent.TitleExtractorFunc(extractOpencodeTitle),
		CustomDaemonWatcher:   NewOpenCodeDaemonWatcher(),
		CustomCompanionPlugin: NewOpenCodeCompanion(),
	}
}

// extractOpencodeTitle parses OpenCode/Kilo top tab bars (`‹3  Tab1  ActiveTitle + ...`)
// or boxed user prompts (`┃  <prompt>`).
func extractOpencodeTitle(viewport string) string {
	top := screen.TopNonEmptyLines(viewport, 4)
	for _, raw := range screen.SplitLines(top) {
		line := strings.TrimSpace(raw)
		if opencodeTabLeadRe.MatchString(line) && strings.Contains(line, "+") {
			rest := opencodeTabLeadRe.ReplaceAllString(line, "")
			segs := multiSpaceRe.Split(rest, -1)
			plusIdx := -1
			for i, seg := range segs {
				s := strings.TrimSpace(seg)
				if s == "+" || strings.HasSuffix(s, " +") {
					plusIdx = i
					break
				}
			}
			if plusIdx >= 0 {
				if s := strings.TrimSpace(segs[plusIdx]); strings.HasSuffix(s, " +") {
					if t := screen.CleanViewportTitle(strings.TrimSuffix(s, " +")); t != "" {
						return t
					}
				}
				for i := plusIdx - 1; i >= 0; i-- {
					if t := screen.CleanViewportTitle(segs[i]); t != "" {
						return t
					}
				}
			}
		}
	}
	// Fallback: boxed user prompt block (`┃  <prompt>`)
	for _, raw := range screen.SplitLines(viewport) {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "┃") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "┃"))
			if rest == "" || strings.Contains(rest, " · ") {
				continue
			}
			if t := screen.CleanViewportTitle(rest); t != "" {
				return t
			}
		}
	}
	return ""
}

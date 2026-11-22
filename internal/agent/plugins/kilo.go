package plugins

import "github.com/arhammahajan/spaces/internal/agent"

// NewKilo returns the coding-agent plugin for Kilo Code (shares OpenCode's TUI tab bar).
func NewKilo() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "kilo",
			Aliases:                 []string{"kilo-code", "kilo code", "herdr:kilo"},
			ScreenManifestID:        "kilo",
			HarnessManaged:          true,
			OSCAuthority:            false,
			RecognizesVersionBanner: true,
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "kilo", RelPath: ".config/kilo/kilo.json"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(extractOpencodeTitle),
	}
}

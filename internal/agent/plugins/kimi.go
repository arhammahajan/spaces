package plugins

import "github.com/arhammahajan/spaces/internal/agent"

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
			ScreenManifestID:        "kimi",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "kimi-cli", RelPath: ".kimi/mcp.json"},
			{HarnessLabel: "kimi-cli", RelPath: ".kimi/config.toml"},
			{HarnessLabel: "kimi-code", RelPath: ".kimi-code/mcp.json"},
			{HarnessLabel: "kimi-code", RelPath: ".kimi-code/config.toml"},
		},
	}
}

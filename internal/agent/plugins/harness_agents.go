package plugins

import "github.com/arhammahajan/spaces/internal/agent"

// harnessManagedPlugins returns the remaining harness-managed and OSC-authority
// coding-agent plugins.
func harnessManagedPlugins() []agent.Plugin {
	return []agent.Plugin{
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "cline",
				ScreenManifestID:        "cline",
				OSCAuthority:            false,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "cline", RelPath: ".cline/cline_mcp_settings.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "copilot",
				Aliases:                 []string{"github-copilot", "ghcs"},
				ScreenManifestID:        "copilot",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "copilot", RelPath: ".copilot/mcp-config.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "fx",
				Aliases:                 []string{"fx-router"},
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "fx", RelPath: ".fx/mcp.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "gemini",
				Aliases:                 []string{"gemini-cli"},
				ScreenManifestID:        "gemini",
				OSCAuthority:            false,
				RecognizesVersionBanner: true,
				WorktreeTitlePrefixes:   []string{"gemini - ", "gemini: ", "gemini | "},
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "gemini", RelPath: ".gemini/settings.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "grok",
				Aliases:                 []string{"grok-build"},
				ScreenManifestID:        "grok",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "grok", RelPath: ".grok/config.toml"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "mcode",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "mcode", RelPath: ".minimax/mcp.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "pi",
				Aliases:                 []string{"herdr:pi"},
				ScreenManifestID:        "pi",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "pi", RelPath: ".pi/agent/mcp.json"},
			},
			CustomCompanionPlugin: NewPiCompanion(),
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "qwen",
				Aliases:                 []string{"qwen-code", "qwen code"},
				ScreenManifestID:        "qwen",
				OSCAuthority:            false,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "qwen", RelPath: ".qwen/settings.json"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "vibe",
				Aliases:                 []string{"vibe-acp"},
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
			MCPTargets: []agent.MCPConfigTarget{
				{HarnessLabel: "vibe", RelPath: ".vibe/config.toml"},
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "crush",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "omp",
				OSCAuthority:            true,
				RecognizesVersionBanner: true,
			},
		},
	}
}

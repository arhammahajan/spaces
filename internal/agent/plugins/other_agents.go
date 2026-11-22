package plugins

import "github.com/arhammahajan/spaces/internal/agent"

// otherRuntimePlugins returns manifest-backed coding-agent plugins that use
// default BasePlugin capabilities.
func otherRuntimePlugins() []agent.Plugin {
	return []agent.Plugin{
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "amp",
				Aliases:          []string{"amp-local"},
				ScreenManifestID: "amp",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "devin",
				Aliases:          []string{"devin-cli", "devin cli"},
				ScreenManifestID: "devin",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:                      "droid",
				ScreenManifestID:        "droid",
				RecognizesVersionBanner: true,
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "hermes",
				Aliases:          []string{"hermes-agent"},
				ScreenManifestID: "hermes",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "kiro",
				Aliases:          []string{"kiro-cli"},
				ScreenManifestID: "kiro",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "letta",
				Aliases:          []string{"letta-code", "letta code"},
				ScreenManifestID: "letta",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "maki",
				ScreenManifestID: "maki",
			},
		},
		&agent.BasePlugin{
			PluginSpec: agent.Spec{
				ID:               "qodercli",
				Aliases:          []string{"qoderclicn", "qoder", "qodercn"},
				ScreenManifestID: "qodercli",
			},
		},
	}
}

// worktreeOnlyPlugins returns additional coding agents recognized for worktree
// auto-naming (e.g. `<agent>-<adj>-<noun>`) that are not full runtime
// tmux-spaces agents.
func worktreeOnlyPlugins() []agent.Plugin {
	return []agent.Plugin{
		&agent.BasePlugin{PluginSpec: agent.Spec{ID: "aider", WorktreeOnly: true}},
		&agent.BasePlugin{PluginSpec: agent.Spec{ID: "goose", WorktreeOnly: true}},
		&agent.BasePlugin{PluginSpec: agent.Spec{ID: "windsurf", WorktreeOnly: true}},
		&agent.BasePlugin{PluginSpec: agent.Spec{ID: "roo", Aliases: []string{"roo-code"}, WorktreeOnly: true}},
	}
}

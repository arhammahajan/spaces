package plugins

import "github.com/arhammahajan/spaces/internal/agent"

// NewDeepSeek returns the coding-agent plugin for DeepSeek Harness (`dsh` / `dsh-tui`).
func NewDeepSeek() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "deepseek",
			Aliases:                 []string{"dsh", "dsh-tui"},
			DisplayApp:              "deepseek",
			CLIDisplayApp:           "deepseek",
			ConversationID:          "deepseek",
			ShortName:               "deepseek",
			HarnessManaged:          true,
			OSCAuthority:            true,
			RecognizesVersionBanner: true,
		},
	}
}

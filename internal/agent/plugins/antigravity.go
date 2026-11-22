package plugins

import (
	"strings"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

// AntigravityHookIdleGraceMs is the silence threshold (10s) after which a
// working Antigravity hook yields to a visibly idle viewport (handles missed
// Stop hooks).
const AntigravityHookIdleGraceMs int64 = 10_000

// AntigravityProcSessionFunc inspects open file descriptors of a pane's
// process tree to discover the live Antigravity conversation ID. Overridable
// in tests.
var AntigravityProcSessionFunc = discoverAntigravitySessionFromProc

// NewAntigravity returns the coding-agent plugin for Antigravity CLI (`agy`).
func NewAntigravity() agent.Plugin {
	return &agent.BasePlugin{
		PluginSpec: agent.Spec{
			ID:                      "antigravity",
			Aliases:                 []string{"agy", "antigravity-cli"},
			DisplayApp:              "antigravity",
			CLIDisplayApp:           "antigravity",
			ConversationID:          "antigravity",
			ShortName:               "agy",
			ScreenManifestID:        "agy",
			HarnessManaged:          true,
			OSCAuthority:            false,
			RecognizesVersionBanner: true,
			WorktreeTitlePrefixes:   []string{"antigravity - ", "antigravity: ", "antigravity | "},
		},
		MCPTargets: []agent.MCPConfigTarget{
			{HarnessLabel: "antigravity", RelPath: ".gemini/config/mcp_config.json"},
			{HarnessLabel: "antigravity", RelPath: ".gemini/antigravity-cli/settings.json"},
		},
		CustomTitleExtractor: agent.TitleExtractorFunc(extractAntigravityTitle),
		CustomHookPolicy:     antigravityHookPolicy{},
		CustomSessionResolver: antigravitySessionResolver{
			BaseSessionResolver: agent.BaseSessionResolver{AgentID: "antigravity"},
		},
	}
}

// extractAntigravityTitle parses Antigravity CLI viewports for the conversation
// prompt (`> <prompt>` with up to 3 indented continuation lines).
func extractAntigravityTitle(viewport string) string {
	return screen.ExtractMultilinePrompt(viewport, ">")
}

type antigravityHookPolicy struct{}

func (antigravityHookPolicy) OSCAuthority() bool {
	return false
}

func (antigravityHookPolicy) ShouldYieldWorkingHookToIdle(in agent.HookArbitrationInput) bool {
	if (agent.BaseHookPolicy{Authority: false, StaleLocalCmdMs: agent.DefaultStaleLocalCmdMs}).ShouldYieldWorkingHookToIdle(in) {
		return true
	}
	return in.HookState == "working" &&
		!in.HasActiveFiniteWork &&
		in.HookSilenceMs >= AntigravityHookIdleGraceMs &&
		in.VisibleIdle
}

type antigravitySessionResolver struct {
	agent.BaseSessionResolver
}

func (antigravitySessionResolver) DiscoverLiveSessionID(pid int) string {
	if AntigravityProcSessionFunc == nil || pid <= 0 {
		return ""
	}
	return strings.TrimSpace(AntigravityProcSessionFunc(pid))
}

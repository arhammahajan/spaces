package plugins

import (
	"context"
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
		CustomTitleExtractor:  agent.TitleExtractorFunc(extractAntigravityTitle),
		CustomHookPolicy:      antigravityHookPolicy{},
		CustomSessionResolver: antigravitySessionResolver{},
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

func (antigravitySessionResolver) SuppressMultiPaneCWDTitle() bool {
	return true
}

// ResolveRestoreSessions deduplicates Antigravity conversations across
// multiple restored panes in the same space so no two panes resume the same
// conversation ID.
func (r antigravitySessionResolver) ResolveRestoreSessions(
	ctx context.Context,
	panes []agent.SessionPaneRef,
	lookup agent.SessionLookupFunc,
) agent.SessionResolution {
	res := agent.SessionResolution{
		SessionIDs: map[string]string{},
		SkipResume: map[string]bool{},
	}
	if len(panes) == 0 {
		return res
	}

	claimed := map[string]bool{}
	var pending []agent.SessionPaneRef
	for _, p := range panes {
		sid := strings.TrimSpace(p.SessionID)
		if sid == "" {
			sid = r.ExtractSessionIDFromArgs(p.FullCommand)
		}
		if sid != "" {
			claimed[sid] = true
			continue
		}
		if p.Profile == "" {
			p.Profile = agent.HarnessProfileFromArgv(p.FullCommand)
		}
		pending = append(pending, p)
	}
	if len(pending) == 0 || lookup == nil {
		return res
	}

	for _, p := range pending {
		sid, ok := lookup(ctx, "antigravity", p.CWD, p.Profile, p.Title, claimed)
		if sid != "" {
			res.SessionIDs[p.Key] = sid
			claimed[sid] = true
		} else if ok && len(claimed) > 0 {
			res.SkipResume[p.Key] = true
		}
	}
	return res
}

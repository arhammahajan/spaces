package plugins

import (
	"context"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/screen"
)

func TestBuiltinPluginsRegistrationAndLookups(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)

	// Runtime agents
	expectedCanonical := map[string]string{
		"claude":          "claude",
		"claude-code":     "claude",
		"agy":             "antigravity",
		"antigravity":     "antigravity",
		"antigravity-cli": "antigravity",
		"cursor":          "cursor-agent",
		"cursor-agent":    "cursor-agent",
		"dsh":             "deepseek",
		"dsh-tui":         "deepseek",
		"deepseek":        "deepseek",
		"kimi":            "kimi",
		"kimi-cli":        "kimi",
		"kimi-code":       "kimi",
		"open-code":       "opencode",
		"herdr:opencode":  "opencode",
		"muse-bin-1.4.2":  "muse",
		"grok-build":      "grok",
		"github-copilot":  "copilot",
		"ghcs":            "copilot",
		"fx-router":       "fx",
		"vibe-acp":        "vibe",
		"qoder":           "qodercli",
	}
	for alias, want := range expectedCanonical {
		if got := reg.CanonicalID(alias); got != want {
			t.Errorf("CanonicalID(%q) = %q, want %q", alias, got, want)
		}
		if !reg.IsKnown(alias) {
			t.Errorf("IsKnown(%q) = false, want true", alias)
		}
	}

	// WorktreeOnly agents must NOT be reported as runtime agents, but MUST resolve ShortName.
	for _, wtOnly := range []string{"aider", "goose", "windsurf", "roo", "roo-code"} {
		if reg.IsKnown(wtOnly) {
			t.Errorf("IsKnown(%q) = true for WorktreeOnly agent, want false", wtOnly)
		}
		if short, ok := reg.ShortName(wtOnly); !ok || short == "" {
			t.Errorf("ShortName(%q) = (%q, %v), want non-empty", wtOnly, short, ok)
		}
	}

	// Specific naming conventions
	if got := reg.CLIDisplayApp("claude"); got != "claude-code" {
		t.Errorf("CLIDisplayApp(claude) = %q, want claude-code", got)
	}
	if got := reg.BinaryName("cursor"); got != "cursor-agent" {
		t.Errorf("BinaryName(cursor) = %q, want cursor-agent", got)
	}
	if short, ok := reg.ShortName("antigravity"); !ok || short != "agy" {
		t.Errorf("ShortName(antigravity) = (%q, %v), want (agy, true)", short, ok)
	}
	if short, ok := reg.ShortName("cursor-agent"); !ok || short != "cursor" {
		t.Errorf("ShortName(cursor-agent) = (%q, %v), want (cursor, true)", short, ok)
	}
	if !reg.MatchesBareVersionCommand("2.1.76") {
		t.Error("MatchesBareVersionCommand(2.1.76) = false, want true")
	}

	// MCP targets count: 22 stowed targets across built-in plugins
	targets := reg.AllMCPConfigTargets()
	if len(targets) != 22 {
		t.Errorf("len(AllMCPConfigTargets()) = %d, want 22", len(targets))
	}
}

func TestAntigravityPluginHookPolicyAndRestoreDedup(t *testing.T) {
	p := NewAntigravity()

	// HookPolicy: 10s idle grace when visibly idle without finite work
	hp := p.HookPolicy()
	if hp.OSCAuthority() {
		t.Error("Antigravity OSCAuthority = true, want false")
	}
	if !hp.ShouldYieldWorkingHookToIdle(agent.HookArbitrationInput{
		HookState:     "working",
		HookSilenceMs: 10_000,
		VisibleIdle:   true,
	}) {
		t.Error("expected Antigravity working hook to yield after 10s when visibly idle")
	}
	if hp.ShouldYieldWorkingHookToIdle(agent.HookArbitrationInput{
		HookState:           "working",
		HookSilenceMs:       15_000,
		HasActiveFiniteWork: true,
		VisibleIdle:         true,
	}) {
		t.Error("expected Antigravity working hook NOT to yield when active finite work exists")
	}

	// SessionResolver: multi-pane deduplication
	sr := p.SessionResolver()
	if !sr.SuppressMultiPaneCWDTitle() {
		t.Error("Antigravity SuppressMultiPaneCWDTitle = false, want true")
	}
	panes := []agent.SessionPaneRef{
		{Key: "0:0", Agent: "antigravity", SessionID: "s1", CWD: "/repo"},
		{Key: "0:1", Agent: "antigravity", CWD: "/repo"},
		{Key: "0:2", Agent: "antigravity", CWD: "/repo"},
	}
	calls := 0
	res := sr.ResolveRestoreSessions(context.Background(), panes, func(_ context.Context, _, _, _, _ string, exclude map[string]bool) (string, bool) {
		calls++
		if !exclude["s1"] {
			t.Errorf("expected s1 to be excluded, got %v", exclude)
		}
		if !exclude["s2"] {
			return "s2", true
		}
		return "", true
	})
	if calls != 2 {
		t.Errorf("lookup calls = %d, want 2", calls)
	}
	if res.SessionIDs["0:1"] != "s2" {
		t.Errorf("pane 0:1 session = %q, want s2", res.SessionIDs["0:1"])
	}
	if !res.SkipResume["0:2"] {
		t.Error("expected pane 0:2 to set SkipResume=true when no unclaimed session remains")
	}
}

func TestBuiltinScreenDetectionAndTitleExtraction(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)

	// Codex title extraction
	codexViewport := "gpt-5 high · ~/dev/spaces · Refactor agent plugins\n"
	if got := reg.ExtractTitle("codex", codexViewport); got != "Refactor agent plugins" {
		t.Errorf("codex ExtractTitle = %q, want Refactor agent plugins", got)
	}

	// Muse title extraction (strips token meter)
	museViewport := "claude-sonnet · high · ~/dev/spaces · Fix auth bug 12.5k/200k (6%)\n"
	if got := reg.ExtractTitle("muse", museViewport); got != "Fix auth bug" {
		t.Errorf("muse ExtractTitle = %q, want Fix auth bug", got)
	}

	// OpenCode tab-bar title extraction
	ocViewport := "‹3  OldTab  Plugin Refactor +  \n"
	if got := reg.ExtractTitle("opencode", ocViewport); got != "Plugin Refactor" {
		t.Errorf("opencode ExtractTitle = %q, want Plugin Refactor", got)
	}

	// Antigravity multiline prompt extraction
	agyViewport := "> design the agent\n  plugin interface\n● working\n"
	if got := reg.ExtractTitle("antigravity", agyViewport); got != "design the agent plugin interface" {
		t.Errorf("antigravity ExtractTitle = %q, want 'design the agent plugin interface'", got)
	}

	// Claude manifest detection
	if !reg.HasScreenManifest("claude-code") {
		t.Error("HasScreenManifest(claude-code) = false, want true")
	}
	claudeWorking := "❯ refactor plugin\n✽ Working…\n───────────────\n❯\n───────────────\n  esc to interrupt\n"
	det := reg.DetectScreen("claude-code", claudeWorking, "")
	if det.State != screen.StateWorking || !det.VisibleWorking || det.Title != "refactor plugin" {
		t.Errorf("expected claude working screen with title, got %+v", det)
	}
}

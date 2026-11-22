package screen

import (
	"strings"
	"sync"
)

// State is the screen-inferred agent state.
type State int

const (
	// StateIdle means the viewport shows an idle prompt.
	StateIdle State = iota
	// StateWorking means the viewport shows active work.
	StateWorking
	// StateBlocked means the viewport shows a blocker (permission prompt, etc).
	StateBlocked
	// StateUnknown means no rule matched or the app has no manifest.
	StateUnknown
)

// String returns the lowercase state name.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateWorking:
		return "working"
	case StateBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// Detected is the outcome of evaluating one viewport.
type Detected struct {
	State           State
	VisibleIdle     bool
	VisibleBlocker  bool
	VisibleWorking  bool
	SkipStateUpdate bool
	// MatchedRule is the id of the winning rule, or "" when none matched.
	MatchedRule string
	// Title is the conversation/session title parsed from agent-specific
	// header/status lines in the captured viewport, or "" when none was found.
	Title string
}

// unknownDetected builds a no-evidence Unknown result.
func unknownDetected(rule string) Detected {
	return Detected{
		State:           StateUnknown,
		VisibleIdle:     false,
		VisibleBlocker:  false,
		VisibleWorking:  false,
		SkipStateUpdate: false,
		MatchedRule:     rule,
	}
}

// HasEvidence reports whether the detection carries a visible signal.
func (d Detected) HasEvidence() bool {
	return d.VisibleIdle || d.VisibleBlocker || d.VisibleWorking
}

// oscAuthorityApps are apps whose OSC lifecycle is complete enough that a
// live non-idle OSC state must win over a screen working/idle result.
// Blockers still override (handled by the caller, as in app.rs).
var oscAuthorityApps = []string{
	"claude",
	"codex",
	"opencode",
	"pi",
	"crush",
	"grok",
	"omp",
	"copilot",
	"kimi",
	"muse",
	"mcode",
	"fx",
	"fx-router",
	"vibe",
	"vibe-acp",
	"dsh-tui",
	"deepseek",
}

var (
	authorityOnce sync.Once
	authoritySet  map[string]struct{}
)

func authority() map[string]struct{} {
	authorityOnce.Do(func() {
		authoritySet = make(map[string]struct{}, len(oscAuthorityApps))
		for _, app := range oscAuthorityApps {
			authoritySet[app] = struct{}{}
		}
	})
	return authoritySet
}

// CanonicalApp returns the canonical manifest id where one exists, else the
// normalized app label. A few harness labels do not have manifests but still
// name the same agent.
func CanonicalApp(app string) string {
	normalized := normalizeApp(app)
	if id := engine().manifestID(normalized); id != "" {
		return id
	}
	switch normalized {
	case "dsh", "dsh-tui":
		return "deepseek"
	case "fx-router":
		return "fx"
	case "vibe-acp":
		return "vibe"
	case "kimi-cli", "kimi-code":
		return "kimi"
	case "claude", "claude-code":
		return "claude"
	default:
		return normalized
	}
}

// AgentIsKnown reports whether a bundled manifest can evaluate this app.
func AgentIsKnown(app string) bool {
	if engine().manifest(normalizeApp(app)) != nil {
		return true
	}
	return engine().manifest(CanonicalApp(app)) != nil
}

// OSCAuthority reports whether a live non-idle OSC state should win over
// screen working/idle for this app.
func OSCAuthority(app string) bool {
	canonical := CanonicalApp(app)
	set := authority()
	if _, ok := set[canonical]; ok {
		return true
	}
	// Aliases that do not have a manifest still appear as raw labels.
	_, ok := set[normalizeApp(app)]
	return ok
}

// HasManifest reports whether a bundled manifest exists for id (or alias).
func HasManifest(id string) bool {
	return engine().manifest(normalizeApp(id)) != nil
}

// DetectManifest evaluates only the manifest rules for manifestID (without
// running ExtractTitle). Used by agent plugins that pair manifest screen
// detection with their own TitleExtractor.
func DetectManifest(manifestID, viewport, oscTitle string) Detected {
	if strings.TrimSpace(viewport) == "" {
		return unknownDetected("")
	}
	return engine().detect(normalizeApp(manifestID), viewport, oscTitle, "")
}

// Detect evaluates the viewport for an app. oscTitle is the pane title (the
// OSC 0/2 title); oscProgress is unavailable to tmux, pass "".
func Detect(app, screen, oscTitle string) Detected {
	if strings.TrimSpace(screen) == "" {
		return unknownDetected("")
	}
	norm := normalizeApp(app)
	if engine().manifest(norm) == nil {
		norm = CanonicalApp(app)
	}
	det := engine().detect(norm, screen, oscTitle, "")
	det.Title = ExtractTitle(norm, screen)
	return det
}

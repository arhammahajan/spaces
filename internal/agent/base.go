package agent

import (
	"context"
	"path/filepath"
	"strings"

	harnessagents "github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/screen"
)

// DefaultStaleLocalCmdMs is the default silence threshold (120s) after which a
// working hook with only local_bash background work yields to a visibly idle screen.
const DefaultStaleLocalCmdMs int64 = 120_000

// BasePlugin provides default implementations for every method of Plugin.
// Concrete agent plugins embed BasePlugin and populate only the fields or
// custom capability implementations they need.
type BasePlugin struct {
	PluginSpec              Spec
	MCPTargets              []MCPConfigTarget
	CustomProcessMatcher    func(ProcessInfo) bool
	CustomScreenReader      ScreenReader
	CustomTitleExtractor    TitleExtractor
	CustomHookPolicy        HookPolicy
	CustomConversationStore ConversationStore
	CustomLogParser         LogParser
	CustomSessionResolver   SessionResolver
	CustomLauncher          Launcher
	CustomConfigProvider    ConfigProvider
}

func (b *BasePlugin) Spec() Spec {
	return b.PluginSpec
}

func (b *BasePlugin) MatchProcess(proc ProcessInfo) bool {
	if b.CustomProcessMatcher != nil {
		return b.CustomProcessMatcher(proc)
	}
	return defaultMatchProcess(b.PluginSpec, proc)
}

func (b *BasePlugin) ScreenReader() ScreenReader {
	if b.CustomScreenReader != nil {
		return b.CustomScreenReader
	}
	return ManifestScreenReader{ManifestID: b.PluginSpec.ScreenManifestID}
}

func (b *BasePlugin) TitleExtractor() TitleExtractor {
	if b.CustomTitleExtractor != nil {
		return b.CustomTitleExtractor
	}
	return GenericTitleExtractor{}
}

func (b *BasePlugin) HookPolicy() HookPolicy {
	if b.CustomHookPolicy != nil {
		return b.CustomHookPolicy
	}
	return BaseHookPolicy{
		Authority:       b.PluginSpec.OSCAuthority,
		StaleLocalCmdMs: DefaultStaleLocalCmdMs,
	}
}

func (b *BasePlugin) ConversationStore() ConversationStore {
	return b.CustomConversationStore
}

func (b *BasePlugin) LogParser() LogParser {
	return b.CustomLogParser
}

func (b *BasePlugin) SessionResolver() SessionResolver {
	if b.CustomSessionResolver != nil {
		return b.CustomSessionResolver
	}
	return BaseSessionResolver{
		AgentID:                       b.PluginSpec.ID,
		DisableMultiPaneTitleSuppress: b.PluginSpec.WorktreeOnly,
		DisableRestoreDedup:           b.PluginSpec.WorktreeOnly,
	}
}

func (b *BasePlugin) Launcher() Launcher {
	if b.CustomLauncher != nil {
		return b.CustomLauncher
	}
	return BaseLauncher{Spec: b.PluginSpec}
}

func (b *BasePlugin) ConfigProvider() ConfigProvider {
	if b.CustomConfigProvider != nil {
		return b.CustomConfigProvider
	}
	return BaseConfigProvider{
		DefaultLabel: b.PluginSpec.ID,
		Targets:      b.MCPTargets,
	}
}

// ManifestScreenReader evaluates pane viewports using a bundled manifest in
// internal/screen/manifests.json.
type ManifestScreenReader struct {
	ManifestID string
}

func (m ManifestScreenReader) HasManifest() bool {
	if strings.TrimSpace(m.ManifestID) == "" {
		return false
	}
	return screen.HasManifest(m.ManifestID)
}

func (m ManifestScreenReader) Detect(viewport, oscTitle string) screen.Detected {
	if strings.TrimSpace(m.ManifestID) == "" {
		return screen.Detected{State: screen.StateUnknown}
	}
	return screen.DetectManifest(m.ManifestID, viewport, oscTitle)
}

// GenericTitleExtractor uses common prompt markers (❯, ›, >) to extract titles.
type GenericTitleExtractor struct{}

func (GenericTitleExtractor) ExtractTitle(viewport string) string {
	return screen.ExtractGenericTitle(viewport)
}

// TitleExtractorFunc adapts a function to TitleExtractor.
type TitleExtractorFunc func(viewport string) string

func (f TitleExtractorFunc) ExtractTitle(viewport string) string {
	if f == nil {
		return ""
	}
	return f(viewport)
}

// BaseHookPolicy provides the shared default arbitration policy:
//   - OSCAuthority from Spec
//   - Yields a stale (>120s) working hook when only local_bash background work
//     remains and the viewport is visibly idle.
type BaseHookPolicy struct {
	Authority       bool
	StaleLocalCmdMs int64
}

func (p BaseHookPolicy) OSCAuthority() bool {
	return p.Authority
}

func (p BaseHookPolicy) ShouldYieldWorkingHookToIdle(in HookArbitrationInput) bool {
	threshold := p.StaleLocalCmdMs
	if threshold <= 0 {
		threshold = DefaultStaleLocalCmdMs
	}
	return in.HookState == "working" &&
		in.HookSilenceMs > threshold &&
		in.HasOnlyLocalCmdBackground &&
		in.VisibleIdle
}

// BaseSessionResolver provides standard CLI flag session ID extraction
// (--conversation, --resume, --session, --id), optional positional
// `resume <id>` extraction (used by Codex and Muse), multi-pane CWD title
// fallback suppression, and multi-pane restore session deduplication.
type BaseSessionResolver struct {
	AgentID                       string
	PositionalResume              bool
	DisableMultiPaneTitleSuppress bool
	DisableRestoreDedup           bool
}

func (r BaseSessionResolver) ExtractSessionIDFromArgs(argv []string) string {
	return ExtractSessionIDFromArgs(argv, r.PositionalResume)
}

func (BaseSessionResolver) DiscoverLiveSessionID(int) string {
	return ""
}

func (r BaseSessionResolver) SuppressMultiPaneCWDTitle() bool {
	return !r.DisableMultiPaneTitleSuppress
}

// ResolveRestoreSessions deduplicates agent conversations across multiple
// restored panes in the same space so no two panes resume the same session ID.
func (r BaseSessionResolver) ResolveRestoreSessions(
	ctx context.Context,
	panes []SessionPaneRef,
	lookup SessionLookupFunc,
) SessionResolution {
	if r.DisableRestoreDedup {
		return SessionResolution{}
	}
	res := SessionResolution{
		SessionIDs: map[string]string{},
		SkipResume: map[string]bool{},
	}
	if len(panes) == 0 {
		return res
	}

	claimed := map[string]bool{}
	var pending []SessionPaneRef
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
			p.Profile = HarnessProfileFromArgv(p.FullCommand)
		}
		pending = append(pending, p)
	}
	if len(pending) == 0 || lookup == nil {
		return res
	}

	for _, p := range pending {
		agentID := strings.TrimSpace(r.AgentID)
		if agentID == "" {
			agentID = strings.TrimSpace(p.Agent)
		}
		if agentID == "" {
			agentID = strings.TrimSpace(panes[0].Agent)
		}
		sid, ok := lookup(ctx, agentID, p.CWD, p.Profile, p.Title, claimed)
		if sid != "" {
			res.SessionIDs[p.Key] = sid
			claimed[sid] = true
		} else if ok && len(claimed) > 0 {
			res.SkipResume[p.Key] = true
		}
	}
	return res
}

// BaseLauncher provides standard binary and harness-management resolution.
type BaseLauncher struct {
	Spec Spec
}

func (l BaseLauncher) BinaryName() string {
	return l.Spec.EffectiveBinary()
}

func (l BaseLauncher) IsHarnessManaged() bool {
	if l.Spec.HarnessManaged {
		return true
	}
	_, ok := harnessagents.Normalize(l.Spec.ID)
	return ok
}

func (l BaseLauncher) ResumeArgs(sessionID string) []string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	if l.Spec.ResumeSubcommand != "" {
		return []string{l.Spec.ResumeSubcommand, sid}
	}
	if l.Spec.ResumeFlag != "" {
		return []string{l.Spec.ResumeFlag, sid}
	}
	return nil
}

// BaseConfigProvider returns static MCP config targets declared by a plugin.
type BaseConfigProvider struct {
	DefaultLabel string
	Targets      []MCPConfigTarget
}

func (c BaseConfigProvider) MCPConfigTargets() []MCPConfigTarget {
	if len(c.Targets) == 0 {
		return nil
	}
	out := make([]MCPConfigTarget, len(c.Targets))
	for i, t := range c.Targets {
		if t.HarnessLabel == "" {
			t.HarnessLabel = c.DefaultLabel
		}
		out[i] = t
	}
	return out
}

// NormalizeCommand extracts a canonical lowercase command name from a raw
// binary path or wrapper name:
//   - strips leading "herdr:" prefix
//   - strips directory prefixes
//   - strips executable/script extensions (.exe, .cmd, .bat, .ps1, .js, .mjs, .cjs)
//   - collapses versioned binaries like "muse-bin-1.4.24-R9131.1" -> "muse"
//     and "claude-2.1.76" -> "claude"
func NormalizeCommand(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "herdr:")
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".js", ".mjs", ".cjs"} {
		if stripped, ok := strings.CutSuffix(s, ext); ok {
			s = stripped
			break
		}
	}
	if s == "" {
		return ""
	}
	if prefix, _, ok := strings.Cut(s, "-bin-"); ok && prefix != "" {
		return prefix
	}
	if idx := strings.IndexByte(s, '-'); idx > 0 && idx+1 < len(s) {
		if c := s[idx+1]; c >= '0' && c <= '9' {
			return s[:idx]
		}
	}
	return s
}

// IsBareVersionString reports whether cmd looks like a bare version string
// (e.g. "2.1.76", used by Claude Code's versioned binary on Linux).
func IsBareVersionString(cmd string) bool {
	s := strings.TrimSpace(cmd)
	if len(s) == 0 {
		return false
	}
	return s[0] >= '0' && s[0] <= '9' && strings.Contains(s, ".")
}

// ExtractSessionIDFromArgs scans argv for standard session flags
// (--conversation, --resume, --session, --id) and optionally positional
// `resume <id>` arguments.
func ExtractSessionIDFromArgs(argv []string, positionalResume bool) string {
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		for _, flag := range []string{"--conversation", "--resume", "--session", "--id"} {
			if tok == flag && i+1 < len(argv) {
				val := strings.TrimSpace(argv[i+1])
				if val != "" && !strings.HasPrefix(val, "-") && val != "latest" {
					return val
				}
			}
			if strings.HasPrefix(tok, flag+"=") {
				val := strings.TrimSpace(strings.TrimPrefix(tok, flag+"="))
				if val != "" && val != "latest" {
					return val
				}
			}
		}
		if positionalResume && tok == "resume" && i+1 < len(argv) {
			val := strings.TrimSpace(argv[i+1])
			if val != "" && !strings.HasPrefix(val, "-") && val != "--last" {
				return val
			}
		}
	}
	return ""
}

// HarnessProfileFromArgv extracts the profile name from a `spaces`,
// `tmux-spaces`, or legacy `harness` launcher command line (e.g.
// `spaces profile work`, `spaces antigravity profile work`,
// `tmux-spaces __shim --profile work claude`, or
// `harness spaces __shim-profile antigravity/work`).
func HarnessProfileFromArgv(argv []string) string {
	if len(argv) < 2 {
		return ""
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	if base != "harness" && base != "spaces" && base != "tmux-spaces" {
		return ""
	}
	for i := 1; i < len(argv); i++ {
		tok := argv[i]
		if (tok == "profile" || tok == "--profile") && i+1 < len(argv) {
			prof := strings.TrimSpace(argv[i+1])
			if prof != "" && !strings.HasPrefix(prof, "-") {
				return prof
			}
		}
		if strings.HasPrefix(tok, "--profile=") {
			prof := strings.TrimSpace(strings.TrimPrefix(tok, "--profile="))
			if prof != "" {
				return prof
			}
		}
		if tok == "__shim-profile" && i+1 < len(argv) {
			if _, prof, ok := strings.Cut(argv[i+1], "/"); ok {
				return strings.TrimSpace(prof)
			}
		}
	}
	return ""
}

func defaultMatchProcess(spec Spec, proc ProcessInfo) bool {
	if spec.WorktreeOnly {
		return false
	}
	matchesName := func(raw string) bool {
		norm := NormalizeCommand(raw)
		if norm == "" {
			return false
		}
		if norm == strings.ToLower(spec.ID) {
			return true
		}
		for _, a := range spec.Aliases {
			if norm == strings.ToLower(a) {
				return true
			}
		}
		return false
	}
	if matchesName(proc.Comm) {
		return true
	}
	checked := 0
	for _, arg := range proc.Args {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		checked++
		if checked > 4 {
			break
		}
		if matchesName(arg) {
			return true
		}
		if strings.Contains(arg, "/") {
			for _, seg := range strings.Split(arg, "/") {
				if matchesName(seg) {
					return true
				}
			}
		}
	}
	return false
}

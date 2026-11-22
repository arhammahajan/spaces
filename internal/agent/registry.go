package agent

import (
	"sort"
	"strings"
	"sync"

	"github.com/arhammahajan/spaces/internal/screen"
)

// Registry holds all registered coding-agent plugins and provides fast,
// consistent lookup and normalization across all tmux-spaces subsystems.
type Registry struct {
	mu      sync.RWMutex
	ordered []Plugin
	byID    map[string]Plugin
	byKey   map[string]Plugin // normalized ID + all aliases -> Plugin
}

var (
	defaultRegOnce sync.Once
	defaultReg     *Registry
)

// DefaultRegistry returns the process-wide plugin registry.
func DefaultRegistry() *Registry {
	defaultRegOnce.Do(func() {
		defaultReg = NewRegistry()
	})
	return defaultReg
}

// Register registers a plugin into the process-wide DefaultRegistry.
func Register(p Plugin) {
	DefaultRegistry().Register(p)
}

// NewRegistry creates an isolated Registry populated with plugins.
func NewRegistry(plugins ...Plugin) *Registry {
	r := &Registry{
		byID:  make(map[string]Plugin),
		byKey: make(map[string]Plugin),
	}
	for _, p := range plugins {
		r.Register(p)
	}
	return r
}

// Register adds or replaces a plugin in the registry, indexing its ID,
// aliases, DisplayApp, CLIDisplayApp, ConversationID, Binary, and ScreenManifestID.
func (r *Registry) Register(p Plugin) {
	if p == nil {
		return
	}
	spec := p.Spec()
	id := strings.ToLower(strings.TrimSpace(spec.ID))
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byID[id]; !exists {
		r.ordered = append(r.ordered, p)
	} else {
		for i, existing := range r.ordered {
			if strings.ToLower(strings.TrimSpace(existing.Spec().ID)) == id {
				r.ordered[i] = p
				break
			}
		}
	}
	r.byID[id] = p

	keys := []string{
		id,
		spec.DisplayApp,
		spec.CLIDisplayApp,
		spec.ConversationID,
		spec.Binary,
		spec.ScreenManifestID,
	}
	keys = append(keys, spec.Aliases...)
	for _, k := range keys {
		nk := strings.ToLower(strings.TrimSpace(k))
		if nk == "" {
			continue
		}
		// Never let a WorktreeOnly plugin overwrite a runtime plugin's alias.
		if existing, ok := r.byKey[nk]; ok && !existing.Spec().WorktreeOnly && spec.WorktreeOnly {
			continue
		}
		r.byKey[nk] = p
		// Also index normalized command form (e.g. stripping herdr:).
		if norm := NormalizeCommand(nk); norm != "" && norm != nk {
			if existing, ok := r.byKey[norm]; !ok || existing.Spec().WorktreeOnly || !spec.WorktreeOnly {
				r.byKey[norm] = p
			}
		}
	}
}

// Resolve looks up a plugin by raw name, alias, binary path, or versioned
// command string. Returns (nil, false) when unknown.
func (r *Registry) Resolve(name string) (Plugin, bool) {
	raw := strings.ToLower(strings.TrimSpace(name))
	if raw == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	if p, ok := r.byKey[raw]; ok {
		return p, true
	}
	norm := NormalizeCommand(raw)
	if norm != "" {
		if p, ok := r.byKey[norm]; ok {
			return p, true
		}
	}
	return nil, false
}

// ResolveRuntime resolves only full runtime plugins (excluding WorktreeOnly plugins).
func (r *Registry) ResolveRuntime(name string) (Plugin, bool) {
	p, ok := r.Resolve(name)
	if !ok || p.Spec().WorktreeOnly {
		return nil, false
	}
	return p, true
}

// All returns all registered plugins in registration order.
func (r *Registry) All() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// RuntimePlugins returns all registered non-WorktreeOnly plugins.
func (r *Registry) RuntimePlugins() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.ordered))
	for _, p := range r.ordered {
		if !p.Spec().WorktreeOnly {
			out = append(out, p)
		}
	}
	return out
}

// IsKnown reports whether name resolves to a registered runtime agent plugin.
func (r *Registry) IsKnown(name string) bool {
	_, ok := r.ResolveRuntime(name)
	return ok
}

// CanonicalID returns the canonical runtime plugin ID for name, or "" if unknown.
func (r *Registry) CanonicalID(name string) string {
	p, ok := r.ResolveRuntime(name)
	if !ok {
		return ""
	}
	return p.Spec().ID
}

// CanonicalOrNormalized returns the canonical runtime plugin ID if known,
// otherwise the normalized lowercase command name.
func (r *Registry) CanonicalOrNormalized(name string) string {
	if p, ok := r.ResolveRuntime(name); ok {
		return p.Spec().ID
	}
	return NormalizeCommand(name)
}

// DisplayApp returns the preferred UI display label for name, preserving unknown
// names as-is.
func (r *Registry) DisplayApp(name string) string {
	if p, ok := r.ResolveRuntime(name); ok {
		return p.Spec().EffectiveDisplayApp()
	}
	return strings.TrimSpace(name)
}

// CLIDisplayApp returns the canonical label used by CLI/shim/osc-watch and
// ConversationEntry.DisplayApp (e.g. "claude" -> "claude-code", "agy" -> "antigravity").
func (r *Registry) CLIDisplayApp(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if p, ok := r.ResolveRuntime(trimmed); ok {
		return p.Spec().EffectiveCLIDisplayApp()
	}
	return trimmed
}

// ConversationID returns the canonical conversation-store agent ID for name.
// Unknown names are returned lowercased and trimmed.
func (r *Registry) ConversationID(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	if low == "" {
		return ""
	}
	if p, ok := r.ResolveRuntime(low); ok {
		return p.Spec().EffectiveConversationID()
	}
	return low
}

// ShortName returns the worktree short prefix for any known coding agent
// (including WorktreeOnly agents like aider, goose, windsurf, roo).
func (r *Registry) ShortName(name string) (string, bool) {
	p, ok := r.Resolve(name)
	if !ok {
		return "", false
	}
	return p.Spec().EffectiveShortName(), true
}

// BinaryName returns the executable binary name for direct launches (e.g.
// "cursor" -> "cursor-agent"). Unknown names are returned trimmed as-is.
func (r *Registry) BinaryName(name string) string {
	if p, ok := r.ResolveRuntime(name); ok {
		return p.Launcher().BinaryName()
	}
	return strings.TrimSpace(name)
}

// IsOSCAuthority reports whether live non-idle OSC 7501 / hook state wins over
// bare screen working/idle readings for name.
func (r *Registry) IsOSCAuthority(name string) bool {
	p, ok := r.ResolveRuntime(name)
	if !ok {
		return false
	}
	return p.HookPolicy().OSCAuthority()
}

// IsHarnessManaged reports whether the `harness` CLI manages profiles/resume
// for name.
func (r *Registry) IsHarnessManaged(name string) bool {
	p, ok := r.ResolveRuntime(name)
	if !ok {
		return false
	}
	return p.Launcher().IsHarnessManaged()
}

// IsVersionBannerName reports whether word is a known agent name that appears
// in "<agent> v1.2.3" version banners.
func (r *Registry) IsVersionBannerName(word string) bool {
	low := strings.ToLower(strings.TrimSpace(word))
	if low == "" {
		return false
	}
	p, ok := r.ResolveRuntime(low)
	if !ok {
		return false
	}
	spec := p.Spec()
	if !spec.RecognizesVersionBanner {
		return false
	}
	if len(spec.VersionBannerNames) == 0 {
		return true
	}
	for _, n := range spec.VersionBannerNames {
		if strings.ToLower(n) == low {
			return true
		}
	}
	return false
}

// MatchesBareVersionCommand reports whether any registered runtime plugin runs
// under a bare version string (e.g. "2.1.76" for Claude Code) and cmd matches.
func (r *Registry) MatchesBareVersionCommand(cmd string) bool {
	if !IsBareVersionString(cmd) {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.ordered {
		if !p.Spec().WorktreeOnly && p.Spec().MatchesBareVersion {
			return true
		}
	}
	return false
}

// HasScreenManifest reports whether app resolves to a plugin whose ScreenReader
// has a manifest / active detection rules.
func (r *Registry) HasScreenManifest(app string) bool {
	p, ok := r.ResolveRuntime(app)
	if !ok {
		return false
	}
	return p.ScreenReader().HasManifest()
}

// DetectScreen evaluates a captured pane viewport and OSC title using the
// plugin's ScreenReader and TitleExtractor.
func (r *Registry) DetectScreen(app, viewport, oscTitle string) screen.Detected {
	if strings.TrimSpace(viewport) == "" {
		return screen.Detected{State: screen.StateUnknown}
	}
	p, ok := r.ResolveRuntime(app)
	if !ok {
		return screen.Detected{State: screen.StateUnknown}
	}
	det := p.ScreenReader().Detect(viewport, oscTitle)
	det.Title = p.TitleExtractor().ExtractTitle(viewport)
	return det
}

// ExtractTitle extracts a conversation/session title from viewport using the
// resolved plugin's TitleExtractor, falling back to generic prompt extraction.
func (r *Registry) ExtractTitle(app, viewport string) string {
	if strings.TrimSpace(viewport) == "" {
		return ""
	}
	if p, ok := r.ResolveRuntime(app); ok {
		if t := p.TitleExtractor().ExtractTitle(viewport); t != "" {
			return t
		}
	}
	return screen.ExtractGenericTitle(viewport)
}

// AllTitlePrefixes returns all TUI OSC title prefixes declared across plugins,
// ordered longest-first to avoid partial prefix matches.
func (r *Registry) AllTitlePrefixes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	var out []string
	for _, p := range r.ordered {
		for _, pref := range p.Spec().TitlePrefixes {
			if pref == "" {
				continue
			}
			if _, ok := seen[pref]; !ok {
				seen[pref] = struct{}{}
				out = append(out, pref)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// AllWorktreeTitlePrefixes returns all lowercase worktree title prefixes across
// plugins, ordered longest-first.
func (r *Registry) AllWorktreeTitlePrefixes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	var out []string
	for _, p := range r.ordered {
		for _, pref := range p.Spec().WorktreeTitlePrefixes {
			low := strings.ToLower(pref)
			if low == "" {
				continue
			}
			if _, ok := seen[low]; !ok {
				seen[low] = struct{}{}
				out = append(out, low)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// AllBoilerplateTitles returns the set of lowercase boilerplate titles declared
// by plugins.
func (r *Registry) AllBoilerplateTitles() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool)
	for _, p := range r.ordered {
		for _, t := range p.Spec().BoilerplateTitles {
			if low := strings.ToLower(strings.TrimSpace(t)); low != "" {
				out[low] = true
			}
		}
	}
	return out
}

// AllMCPConfigTargets returns all stowed harness MCP config targets declared
// across all registered plugins.
func (r *Registry) AllMCPConfigTargets() []MCPConfigTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []MCPConfigTarget
	for _, p := range r.ordered {
		out = append(out, p.ConfigProvider().MCPConfigTargets()...)
	}
	return out
}

// DetectProcess inspects proc against all registered runtime plugins and
// returns the matching plugin's canonical ID (or "" if none matches).
func (r *Registry) DetectProcess(proc ProcessInfo) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.ordered {
		if p.Spec().WorktreeOnly {
			continue
		}
		if p.MatchProcess(proc) {
			return p.Spec().ID
		}
	}
	return ""
}

// Package profiles implements the harness CLI profile store, shadow-home
// templates and seeding, and native-antigravity credential resolution.
//
// Profiles live in their own store file, $HOME/.harness/profiles.json,
// with camelCase keys and a top-level version of 1. Every agent implicitly
// has a built-in unmodified "default" profile; user-created profiles live in
// the own store and isolate their state under $HOME/.harness.
package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// profileNameRe validates own-store profile names (T3-ish slug rules): a
// leading letter followed by up to 63 letters, digits, underscores, or
// dashes. All existing lowercase names remain valid.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// safeCredentialRe validates native-antigravity credential names. Only safe
// names map to a credential file under $ConfigHome/ai/antigravity.
var safeCredentialRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// knownAgents is the frozen agent list from the harness CLI contract. Child C
// owns the live registry; profiles validates against this list so the two
// packages build independently.
var knownAgents = []string{
	"claude",
	"codex",
	"grok",
	"cline",
	"opencode",
	"pi",
	"qwen",
	"copilot",
	"kimi",
	"kimi-cli",
	"mcode",
	"muse",
	"antigravity",
	"fx",
	"deepseek",
	"kilo",
	"vibe",
	"gemini",
}

// KnownAgents returns the frozen canonical agent ids, sorted.
func KnownAgents() []string {
	out := append([]string(nil), knownAgents...)
	sort.Strings(out)
	return out
}

// isKnownAgent reports whether name is a canonical agent id.
func isKnownAgent(name string) bool {
	for _, a := range knownAgents {
		if a == name {
			return true
		}
	}
	return false
}

// Profile is one named launcher profile in the own store.
type Profile struct {
	Agent       string            `json:"agent"`
	DisplayName string            `json:"displayName"`
	Enabled     bool              `json:"enabled"`
	BinaryPath  string            `json:"binaryPath"`
	HomePath    string            `json:"homePath"`
	ShadowHome  string            `json:"shadowHomePath"`
	SeedFrom    string            `json:"seedFrom"`
	Env         map[string]string `json:"env"`
	PrependArgs []string          `json:"prependArgs"`
	Router      bool              `json:"router"`
	Overlay     *bool             `json:"overlay"`
}

// UnmarshalJSON decodes a Profile, defaulting Router and Enabled to true
// when their keys are absent. Explicit false is respected per the contract.
func (p *Profile) UnmarshalJSON(data []byte) error {
	type plain Profile // new type: same fields, no methods (no recursion)
	var aux struct {
		plain
		Router  *bool `json:"router"`
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*p = Profile(aux.plain)
	if aux.Router == nil {
		p.Router = true
	} else {
		p.Router = *aux.Router
	}
	if aux.Enabled == nil {
		p.Enabled = true
	} else {
		p.Enabled = *aux.Enabled
	}
	return nil
}

// NamedProfile pairs a store key with its Profile.
type NamedProfile struct {
	Name    string  `json:"name"`
	Profile Profile `json:"profile"`
}

// CreateOptions controls profile creation side effects.
type CreateOptions struct {
	// SeedRoot overrides the dotfiles root used for ShadowHome seeding.
	SeedRoot string
	// NoSeed skips ShadowHome seeding even when SeedFrom is set.
	NoSeed bool
}

// DefaultProfileName is the reserved name of each agent's built-in unmodified
// profile.
const DefaultProfileName = "default"

// DefaultShadowHomeTemplate is the preconfigured default path template used
// when creating a profile without an explicit --shadow-home. It can be
// overridden by HARNESS_SHADOW_HOME_TEMPLATE or the top-level
// shadowHomeTemplate field in profiles.json.
const DefaultShadowHomeTemplate = "{home}/.harness/profiles/{agent}/{name}/home"

// DefaultTemplateDir returns the harness-owned directory whose contents are
// automatically seeded into newly created profiles for agent when no explicit
// --seed-from is provided: $HOME/.harness/templates/<agent>.
func DefaultTemplateDir(paths harnessenv.Paths, agent string) string {
	return filepath.Join(harnessenv.HarnessDir(paths), "templates", agent)
}

// dotsAgentDir maps a canonical agent id to its shared config subdirectory
// under the dotfiles root (e.g. ~/dev/dots/<dir>).
func dotsAgentDir(agent string) string {
	switch agent {
	case "kimi":
		return "kimi-code"
	default:
		return mapWantAgent(agent)
	}
}

// DefaultSharedConfigHome returns the default central shared-config home for
// agent under the dotfiles root ($HARNESS_DOTFILES or ~/dev/dots).
func DefaultSharedConfigHome(agent string, paths harnessenv.Paths) string {
	root, _ := harnessenv.DotfilesRoot(paths)
	return filepath.Join(root, dotsAgentDir(agent))
}

// ExpandShadowHomeTemplate expands a shadow-home path template for agent and
// profile name, supporting leading "~/" and {home}, {data_home}, {config_home},
// {state_home}, {cache_home}, {agent}, and {name} placeholders.
func ExpandShadowHomeTemplate(tmpl, agent, name string, paths harnessenv.Paths) string {
	if tmpl == "" {
		tmpl = DefaultShadowHomeTemplate
	}
	replacer := strings.NewReplacer(
		"{home}", paths.Home,
		"{data_home}", paths.DataHome,
		"{config_home}", paths.ConfigHome,
		"{state_home}", paths.StateHome,
		"{cache_home}", paths.CacheHome,
		"{agent}", agent,
		"{name}", name,
	)
	expanded := expandTildePath(replacer.Replace(tmpl), paths.Home)
	return filepath.Clean(expanded)
}

// Store is the loaded own-store file plus its location.
type Store struct {
	paths              harnessenv.Paths
	path               string
	shadowHomeTemplate string
	defaults           map[string]Profile
	profiles           map[profileKey]Profile
}

// ProfilesDir returns the root directory holding per-profile directories:
// $HOME/.harness/profiles.
func ProfilesDir(paths harnessenv.Paths) string {
	return filepath.Join(harnessenv.HarnessDir(paths), "profiles")
}

// ProfileFilePath returns the JSON config file path for a profile (including
// each agent's "default" profile):
// $HOME/.harness/profiles/<agent>/<name>/profile.json.
func ProfileFilePath(paths harnessenv.Paths, agent, name string) string {
	return filepath.Join(ProfilesDir(paths), mapWantAgent(agent), name, "profile.json")
}

// StorePath returns the legacy/global store file location for paths
// ($HOME/.harness/profiles.json). Individual profiles are stored in
// ProfileFilePath ($HOME/.harness/profiles/<agent>/<name>/profile.json).
func StorePath(paths harnessenv.Paths) string {
	return filepath.Join(harnessenv.HarnessDir(paths), "profiles.json")
}

// Path returns the file this store loads from and saves to.
func (s *Store) Path() string { return s.path }

// ShadowHomeTemplate returns the effective shadow-home path template for this
// store: HARNESS_SHADOW_HOME_TEMPLATE if set, else the store file's configured
// shadowHomeTemplate, else DefaultShadowHomeTemplate.
func (s *Store) ShadowHomeTemplate() string {
	if envTmpl := strings.TrimSpace(os.Getenv("HARNESS_SHADOW_HOME_TEMPLATE")); envTmpl != "" {
		return envTmpl
	}
	if strings.TrimSpace(s.shadowHomeTemplate) != "" {
		return s.shadowHomeTemplate
	}
	return DefaultShadowHomeTemplate
}

// defaultShadowHome renders the default shadow home path for (agent, name).
func (s *Store) defaultShadowHome(agent, name string) string {
	return ExpandShadowHomeTemplate(s.ShadowHomeTemplate(), agent, name, s.paths)
}

// storeFile is the on-disk schema: camelCase keys, top-level version 1.
type storeFile struct {
	Version            int                `json:"version"`
	ShadowHomeTemplate string             `json:"shadowHomeTemplate,omitempty"`
	Defaults           map[string]Profile `json:"defaults,omitempty"`
	Profiles           map[string]Profile `json:"profiles,omitempty"`
}

// Open loads all per-profile JSON files from
// $HOME/.harness/profiles/<agent>/<name>/profile.json (including each
// agent's "default" profile) and merges any legacy entries from
// $HOME/.harness/profiles.json. Missing files mean an empty store.
func Open(paths harnessenv.Paths) (*Store, error) {
	path := StorePath(paths)
	s := &Store{
		paths:    paths,
		path:     path,
		defaults: map[string]Profile{},
		profiles: map[profileKey]Profile{},
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(strings.TrimSpace(string(data))) > 0 {
			var sf storeFile
			if err := json.Unmarshal(data, &sf); err != nil {
				return nil, fmt.Errorf("profiles: parse %s: %w", path, err)
			}
			s.shadowHomeTemplate = sf.ShadowHomeTemplate
			for agent, p := range sf.Defaults {
				p.Agent = agent
				p.Env = nonNilMap(p.Env)
				p.PrependArgs = nonNilSlice(p.PrependArgs)
				s.defaults[agent] = p
			}
			for name, p := range sf.Profiles {
				p.Env = nonNilMap(p.Env)
				p.PrependArgs = nonNilSlice(p.PrependArgs)
				s.profiles[newProfileKey(p.Agent, name)] = p
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Scan $HOME/.harness/profiles/<agent>/<name>/profile.json.
	// Per-profile JSON files are the canonical source of truth and win over
	// any legacy profiles.json entries.
	profilesRoot := ProfilesDir(paths)
	agentDirs, err := os.ReadDir(profilesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	for _, agentEntry := range agentDirs {
		if !agentEntry.IsDir() {
			continue
		}
		agentName := agentEntry.Name()
		profDirs, err := os.ReadDir(filepath.Join(profilesRoot, agentName))
		if err != nil {
			continue
		}
		for _, profEntry := range profDirs {
			if !profEntry.IsDir() {
				continue
			}
			profName := profEntry.Name()
			pFile := filepath.Join(profilesRoot, agentName, profName, "profile.json")
			data, err := os.ReadFile(pFile)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			var p Profile
			if err := json.Unmarshal(data, &p); err != nil {
				return nil, fmt.Errorf("profiles: parse %s: %w", pFile, err)
			}
			// The directory identifies the owning agent. Keep a profile-only
			// driver name when it belongs to that same agent.
			if p.Agent == "" || mapWantAgent(p.Agent) != mapWantAgent(agentName) {
				p.Agent = agentName
			}
			p.Env = nonNilMap(p.Env)
			p.PrependArgs = nonNilSlice(p.PrependArgs)
			if profName == DefaultProfileName {
				s.defaults[mapWantAgent(p.Agent)] = p
			} else {
				s.profiles[newProfileKey(p.Agent, profName)] = p
			}
		}
	}
	return s, nil
}

type profileKey struct {
	agent string
	name  string
}

func newProfileKey(agent, name string) profileKey {
	return profileKey{agent: mapWantAgent(agent), name: name}
}

func (key profileKey) String() string {
	return key.agent + "/" + key.name
}

func (s *Store) matchingKeys(ref string) []profileKey {
	if agent, name, qualified := strings.Cut(ref, "/"); qualified {
		key := newProfileKey(agent, name)
		if _, ok := s.profiles[key]; ok {
			return []profileKey{key}
		}
		return nil
	}
	var keys []profileKey
	for key := range s.profiles {
		if key.name == ref {
			keys = append(keys, key)
		}
	}
	return keys
}

func ambiguousProfile(name string, keys []profileKey) error {
	refs := make([]string, 0, len(keys))
	for _, key := range keys {
		refs = append(refs, key.String())
	}
	sort.Strings(refs)
	return fmt.Errorf("profile %q is ambiguous; use %s", name, strings.Join(refs, " or "))
}

func (s *Store) selectKey(ref string) (profileKey, error) {
	keys := s.profileCandidates(ref)
	switch len(keys) {
	case 0:
		return profileKey{}, fmt.Errorf("unknown profile: %s", ref)
	case 1:
		if _, ok := s.profiles[keys[0]]; !ok {
			return profileKey{}, fmt.Errorf("unknown stored profile: %s", ref)
		}
		return keys[0], nil
	default:
		return profileKey{}, ambiguousProfile(ref, keys)
	}
}

// List returns custom profiles sorted by name, then by agent.
func (s *Store) List() []NamedProfile {
	out := make([]NamedProfile, 0, len(s.profiles))
	for key, p := range s.profiles {
		out = append(out, NamedProfile{Name: key.name, Profile: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Profile.Agent < out[j].Profile.Agent
	})
	return out
}

// Get returns a custom profile by qualified reference or unique name.
func (s *Store) Get(ref string) (NamedProfile, bool) {
	key, err := s.selectKey(ref)
	if err != nil {
		return NamedProfile{}, false
	}
	return NamedProfile{Name: key.name, Profile: s.profiles[key]}, true
}

// Create validates and inserts a profile, creating its shadow home (from the
// preconfigured default template when ShadowHome is empty) and seeding it from
// either --seed-from or the agent's default template directory, then persists
// the store.
func (s *Store) Create(name string, p Profile, opts CreateOptions) error {
	if name == DefaultProfileName {
		return fmt.Errorf("profile name %q is reserved for each agent's built-in default profile", DefaultProfileName)
	}
	if !profileNameRe.MatchString(name) {
		return fmt.Errorf("invalid profile name: %s (must match ^[a-zA-Z][a-zA-Z0-9_-]{0,63}$)", name)
	}
	if !isKnownAgent(p.Agent) {
		return fmt.Errorf("unknown agent: %s", p.Agent)
	}
	key := newProfileKey(p.Agent, name)
	if _, dup := s.profiles[key]; dup {
		return fmt.Errorf("profile already exists: %s", key)
	}
	if err := validateSeedFrom(p.SeedFrom); err != nil {
		return err
	}
	shadow := p.ShadowHome
	if shadow == "" {
		shadow = s.defaultShadowHome(p.Agent, name)
	} else {
		shadow = expandTildePath(shadow, s.paths.Home)
	}
	if p.BinaryPath != "" {
		p.BinaryPath = expandTildePath(p.BinaryPath, s.paths.Home)
	}
	if p.HomePath != "" {
		p.HomePath = expandTildePath(p.HomePath, s.paths.Home)
	} else if defShared := DefaultSharedConfigHome(p.Agent, s.paths); defShared != "" {
		p.HomePath = defShared
	}
	if p.Overlay == nil {
		t := true
		p.Overlay = &t
	}
	if err := os.MkdirAll(shadow, 0o700); err != nil {
		return fmt.Errorf("profiles: create shadow home: %w", err)
	}
	if !opts.NoSeed {
		if p.SeedFrom != "" {
			root := opts.SeedRoot
			if root == "" {
				root, _ = harnessenv.DotfilesRoot(s.paths)
			}
			if err := seedShadowHome(root, p.SeedFrom, shadow); err != nil {
				return err
			}
		} else {
			if err := seedDir(DefaultTemplateDir(s.paths, p.Agent), shadow); err != nil {
				return err
			}
		}
	}
	sharedHome := p.HomePath
	if sharedHome == "" || sharedHome == DefaultSharedConfigHome(p.Agent, s.paths) {
		sharedHome = DefaultSharedHome(p.Agent, s.paths)
	}
	if SupportsOverlay(p.Agent) {
		if overlayEnabled(p) {
			if err := MaterializeShadowHomeWithPaths(p.Agent, shadow, sharedHome, s.paths); err != nil {
				return err
			}
		} else {
			if err := MaterializeBaseConfigWithPaths(p.Agent, shadow, sharedHome, s.paths); err != nil {
				return err
			}
		}
	}
	p.ShadowHome = shadow
	p.Env = nonNilMap(p.Env)
	p.PrependArgs = nonNilSlice(p.PrependArgs)
	s.profiles[key] = p
	return s.Save()
}

// Remove deletes a profile. With purge, the shadow home is deleted too, but
// only when it lives under $HOME/.harness/profiles.
func (s *Store) Remove(name string, purge bool) error {
	if name == DefaultProfileName {
		return fmt.Errorf("cannot remove built-in %q profile", DefaultProfileName)
	}
	key, err := s.selectKey(name)
	if err != nil {
		return err
	}
	p := s.profiles[key]
	base := ProfilesDir(s.paths)
	pFile := ProfileFilePath(s.paths, key.agent, key.name)
	if err := os.Remove(pFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("profiles: remove profile json: %w", err)
	}
	if purge && p.ShadowHome != "" {
		if underDir(base, p.ShadowHome) {
			if err := os.RemoveAll(p.ShadowHome); err != nil {
				return fmt.Errorf("profiles: purge shadow home: %w", err)
			}
			// Clean empty ancestor directories up to (not including) base so
			// purge leaves no empty husk under the profiles base.
			for parent := filepath.Dir(p.ShadowHome); underDir(base, parent); parent = filepath.Dir(parent) {
				if err := os.Remove(parent); err != nil {
					break
				}
			}
		}
	}
	for parent := filepath.Dir(pFile); underDir(base, parent); parent = filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			break
		}
	}
	delete(s.profiles, key)
	return s.Save()
}

// EditPatch describes a partial profile update. Nil pointers leave the
// field untouched; ClearEnv/SetEnv/UnsetEnv update Env; PrependArgs handling
// depends on SetPrependArgs/ClearPrependArgs (see Store.Edit).
type EditPatch struct {
	DisplayName      *string
	BinaryPath       *string
	ClearBinary      bool
	HomePath         *string
	ClearHomePath    bool
	ShadowHome       *string
	ClearShadowHome  bool
	SeedFrom         *string
	SetEnv           map[string]string
	UnsetEnv         []string
	ClearEnv         bool
	PrependArgs      []string
	SetPrependArgs   bool
	ClearPrependArgs bool
	Router           *bool
	Enabled          *bool
	Overlay          *bool
}

// builtinDefaultProfile returns the unmodified built-in default profile for
// a canonical agent.
func builtinDefaultProfile(agent string) Profile {
	return Profile{
		Agent:       agent,
		DisplayName: "Default",
		Enabled:     true,
		Env:         map[string]string{},
		PrependArgs: []string{},
		Router:      true,
	}
}

// GetDefault returns the effective default profile for canonical agent and
// reports whether it has a stored override.
func (s *Store) GetDefault(agent string) (Profile, bool) {
	want := mapWantAgent(agent)
	if p, ok := s.defaults[want]; ok {
		p.Agent = want
		p.Env = copyMap(nonNilMap(p.Env))
		p.PrependArgs = copySlice(nonNilSlice(p.PrependArgs))
		return p, true
	}
	return builtinDefaultProfile(want), false
}

// EditDefault applies a partial update to an agent's built-in "default"
// profile and persists it in its per-agent profile file.
func (s *Store) EditDefault(agent string, patch EditPatch) error {
	want := mapWantAgent(agent)
	if !isKnownAgent(want) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	cur, _ := s.GetDefault(want)
	updated, err := applyEditPatch(cur, patch, s.paths)
	if err != nil {
		return err
	}
	updated.Agent = want
	s.defaults[want] = updated
	return s.Save()
}

// EnsureDefaults ensures every known agent has a default profile JSON file at
// $HOME/.harness/profiles/<agent>/default/profile.json with suitable
// defaults, without overwriting any existing default profile files.
func (s *Store) EnsureDefaults(defaultBinaryFn func(agent string) string) error {
	for _, agent := range KnownAgents() {
		if _, ok := s.defaults[agent]; ok {
			continue
		}
		p := builtinDefaultProfile(agent)
		if defaultBinaryFn != nil {
			p.BinaryPath = defaultBinaryFn(agent)
		}
		p.HomePath = DefaultSharedConfigHome(agent, s.paths)
		t := true
		p.Overlay = &t
		s.defaults[agent] = p
	}
	return s.Save()
}

// ResetDefault removes any stored default profile JSON for an agent,
// restoring the built-in unmodified default.
func (s *Store) ResetDefault(agent string) error {
	want := mapWantAgent(agent)
	if !isKnownAgent(want) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	pFile := ProfileFilePath(s.paths, want, DefaultProfileName)
	if err := os.Remove(pFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("profiles: remove default profile json: %w", err)
	}
	base := ProfilesDir(s.paths)
	for parent := filepath.Dir(pFile); underDir(base, parent); parent = filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			break
		}
	}
	delete(s.defaults, want)
	return s.Save()
}

// SetEnabled toggles Enabled on an existing store profile and persists
// atomically.
func (s *Store) SetEnabled(name string, enabled bool) error {
	if name == DefaultProfileName {
		return fmt.Errorf("cannot disable built-in %q profile; use \"harness profile edit default --agent <agent>\" to customize it", DefaultProfileName)
	}
	key, err := s.selectKey(name)
	if err != nil {
		return err
	}
	p := s.profiles[key]
	p.Enabled = enabled
	s.profiles[key] = p
	return s.Save()
}

// Edit applies a partial update to an existing profile, creating the new
// ShadowHome directory (with overlay materialization when explicitly enabled)
// when ShadowHome is set to a non-empty path, then persists atomically.
func (s *Store) Edit(name string, patch EditPatch) error {
	if name == DefaultProfileName {
		return fmt.Errorf("to edit a default profile, specify --agent <agent> (Store.EditDefault)")
	}
	if agent, profile, qualified := strings.Cut(name, "/"); qualified && profile == DefaultProfileName {
		return s.EditDefault(agent, patch)
	}
	key, err := s.selectKey(name)
	if err != nil {
		return err
	}
	p := s.profiles[key]
	updated, err := applyEditPatch(p, patch, s.paths)
	if err != nil {
		return err
	}
	s.profiles[key] = updated
	return s.Save()
}

func applyEditPatch(p Profile, patch EditPatch, paths harnessenv.Paths) (Profile, error) {
	if patch.DisplayName != nil {
		p.DisplayName = *patch.DisplayName
	}
	if patch.ClearBinary {
		p.BinaryPath = ""
	} else if patch.BinaryPath != nil {
		p.BinaryPath = expandTildePath(*patch.BinaryPath, paths.Home)
	}
	if patch.ClearHomePath {
		p.HomePath = ""
	} else if patch.HomePath != nil {
		p.HomePath = expandTildePath(*patch.HomePath, paths.Home)
	}
	if patch.SeedFrom != nil {
		if err := validateSeedFrom(*patch.SeedFrom); err != nil {
			return Profile{}, err
		}
		p.SeedFrom = *patch.SeedFrom
	}
	if patch.Overlay != nil {
		v := *patch.Overlay
		p.Overlay = &v
	}
	if patch.ClearShadowHome {
		p.ShadowHome = ""
	} else if patch.ShadowHome != nil {
		shadow := expandTildePath(*patch.ShadowHome, paths.Home)
		if shadow != "" {
			if err := os.MkdirAll(shadow, 0o700); err != nil {
				return Profile{}, fmt.Errorf("profiles: create shadow home: %w", err)
			}
		}
		p.ShadowHome = shadow
	}
	if p.ShadowHome != "" && (patch.ShadowHome != nil || patch.HomePath != nil || patch.Overlay != nil) {
		sharedHome := p.HomePath
		if sharedHome == "" || sharedHome == DefaultSharedConfigHome(p.Agent, paths) {
			sharedHome = DefaultSharedHome(p.Agent, paths)
		}
		if SupportsOverlay(p.Agent) {
			if overlayEnabled(p) {
				if err := MaterializeShadowHomeWithPaths(p.Agent, p.ShadowHome, sharedHome, paths); err != nil {
					return Profile{}, err
				}
			} else {
				if err := MaterializeBaseConfigWithPaths(p.Agent, p.ShadowHome, sharedHome, paths); err != nil {
					return Profile{}, err
				}
			}
		}
	}
	if patch.ClearEnv {
		p.Env = map[string]string{}
	}
	if patch.SetEnv != nil {
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		for k, v := range patch.SetEnv {
			p.Env[k] = v
		}
	}
	if len(patch.UnsetEnv) > 0 {
		for _, k := range patch.UnsetEnv {
			delete(p.Env, k)
		}
	}
	switch {
	case patch.ClearPrependArgs:
		p.PrependArgs = []string{}
		if len(patch.PrependArgs) > 0 {
			// --clear-args combined with --arg replaces rather than clears.
			p.PrependArgs = append([]string{}, patch.PrependArgs...)
		}
	case patch.SetPrependArgs:
		p.PrependArgs = append([]string{}, patch.PrependArgs...)
	case len(patch.PrependArgs) > 0:
		p.PrependArgs = append(append([]string{}, p.PrependArgs...), patch.PrependArgs...)
	}
	if patch.Router != nil {
		p.Router = *patch.Router
	}
	if patch.Enabled != nil {
		p.Enabled = *patch.Enabled
	}
	p.Env = nonNilMap(p.Env)
	p.PrependArgs = nonNilSlice(p.PrependArgs)
	return p, nil
}

// Clone duplicates an existing profile under a new name with a fresh managed
// shadow home. DisplayName is left empty so the clone displays as dst. The
// source shadow home and any live auth.json/shadow state are never copied;
// the clone is seeded from dotfiles and codex-overlay-materialized like a
// fresh profile.
func (s *Store) Clone(src, dst string, opts CreateOptions) error {
	if dst == DefaultProfileName {
		return fmt.Errorf("profile name %q is reserved for each agent's built-in default profile", DefaultProfileName)
	}
	srcKey, err := s.selectKey(src)
	if err != nil {
		return err
	}
	sp := s.profiles[srcKey]
	if !profileNameRe.MatchString(dst) {
		return fmt.Errorf("invalid profile name: %s (must match ^[a-zA-Z][a-zA-Z0-9_-]{0,63}$)", dst)
	}
	dstKey := newProfileKey(srcKey.agent, dst)
	if _, dup := s.profiles[dstKey]; dup {
		return fmt.Errorf("profile already exists: %s", dstKey)
	}
	shadow := s.defaultShadowHome(sp.Agent, dst)
	if err := os.MkdirAll(shadow, 0o700); err != nil {
		return fmt.Errorf("profiles: create shadow home: %w", err)
	}
	if !opts.NoSeed {
		if sp.SeedFrom != "" {
			root := opts.SeedRoot
			if root == "" {
				root, _ = harnessenv.DotfilesRoot(s.paths)
			}
			if err := seedShadowHome(root, sp.SeedFrom, shadow); err != nil {
				return err
			}
		} else {
			if err := seedDir(DefaultTemplateDir(s.paths, sp.Agent), shadow); err != nil {
				return err
			}
		}
	}
	overlay := sp.Overlay
	sharedHome := sp.HomePath
	if sharedHome == "" || sharedHome == DefaultSharedConfigHome(sp.Agent, s.paths) {
		sharedHome = DefaultSharedHome(sp.Agent, s.paths)
	}
	if SupportsOverlay(sp.Agent) {
		if overlayEnabled(sp) {
			if err := MaterializeShadowHomeWithPaths(sp.Agent, shadow, sharedHome, s.paths); err != nil {
				return err
			}
		} else {
			if err := MaterializeBaseConfigWithPaths(sp.Agent, shadow, sharedHome, s.paths); err != nil {
				return err
			}
		}
	}
	var overlayCopy *bool
	if overlay != nil {
		v := *overlay
		overlayCopy = &v
	}
	np := Profile{
		Agent:       sp.Agent,
		DisplayName: "",
		Enabled:     sp.Enabled,
		ShadowHome:  shadow,
		SeedFrom:    sp.SeedFrom,
		Env:         copyMap(sp.Env),
		PrependArgs: append([]string{}, sp.PrependArgs...),
		Router:      sp.Router,
		BinaryPath:  sp.BinaryPath,
		HomePath:    sp.HomePath,
		Overlay:     overlayCopy,
	}
	np.Env = nonNilMap(np.Env)
	np.PrependArgs = nonNilSlice(np.PrependArgs)
	s.profiles[dstKey] = np
	return s.Save()
}

// Rename moves a profile key, relocating its managed shadow home (only when
// it lives under $DataHome/harness/profiles) to the new canonical location.
// External custom ShadowHome paths are left untouched on disk.
func (s *Store) Rename(oldName, newName string) error {
	if oldName == DefaultProfileName {
		return fmt.Errorf("cannot rename built-in %q profile", DefaultProfileName)
	}
	if newName == DefaultProfileName {
		return fmt.Errorf("profile name %q is reserved for each agent's built-in default profile", DefaultProfileName)
	}
	oldKey, err := s.selectKey(oldName)
	if err != nil {
		return err
	}
	if oldKey.name == newName {
		return nil
	}
	p := s.profiles[oldKey]
	if !profileNameRe.MatchString(newName) {
		return fmt.Errorf("invalid profile name: %s (must match ^[a-zA-Z][a-zA-Z0-9_-]{0,63}$)", newName)
	}
	newKey := newProfileKey(oldKey.agent, newName)
	if _, dup := s.profiles[newKey]; dup {
		return fmt.Errorf("profile already exists: %s", newKey)
	}
	if p.ShadowHome != "" {
		base := filepath.Join(harnessenv.HarnessDir(s.paths), "profiles")
		if underDir(base, p.ShadowHome) {
			newShadow := s.defaultShadowHome(p.Agent, newName)
			if p.ShadowHome != newShadow {
				if _, err := os.Stat(newShadow); err == nil {
					return fmt.Errorf("profiles: rename target shadow already exists: %s", newShadow)
				}
				if err := os.MkdirAll(filepath.Dir(newShadow), 0o755); err != nil {
					return fmt.Errorf("profiles: rename shadow home: %w", err)
				}
				if err := os.Rename(p.ShadowHome, newShadow); err != nil {
					return fmt.Errorf("profiles: rename shadow home: %w", err)
				}
				for parent := filepath.Dir(p.ShadowHome); underDir(base, parent); parent = filepath.Dir(parent) {
					if err := os.Remove(parent); err != nil {
						break
					}
				}
				p.ShadowHome = newShadow
			}
		}
	}
	oldFile := ProfileFilePath(s.paths, oldKey.agent, oldKey.name)
	if err := os.Remove(oldFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("profiles: remove old profile json: %w", err)
	}
	base := ProfilesDir(s.paths)
	for parent := filepath.Dir(oldFile); underDir(base, parent); parent = filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			break
		}
	}
	delete(s.profiles, oldKey)
	p.Env = nonNilMap(p.Env)
	p.PrependArgs = nonNilSlice(p.PrependArgs)
	s.profiles[newKey] = p
	return s.Save()
}

// Save writes each profile (both custom profiles and per-agent default
// profiles) atomically to its own JSON file at
// $HOME/.harness/profiles/<agent>/<name>/profile.json with mode 0600.
// Legacy profile entries are migrated into those files; profiles.json retains
// the shared shadow-home template.
func (s *Store) Save() error {
	for agent, p := range s.defaults {
		p.Agent = agent
		if err := writeProfileFile(ProfileFilePath(s.paths, agent, DefaultProfileName), p); err != nil {
			return err
		}
	}
	for key, p := range s.profiles {
		if err := writeProfileFile(ProfileFilePath(s.paths, key.agent, key.name), p); err != nil {
			return err
		}
	}
	_, legacyExists := os.Stat(s.path)
	if s.shadowHomeTemplate == "" && os.IsNotExist(legacyExists) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("profiles: create store dir: %w", err)
	}
	// Once migrated, keep only shared metadata here. The per-agent files
	// can represent duplicate names and are already authoritative.
	sf := storeFile{Version: 1, ShadowHomeTemplate: s.shadowHomeTemplate}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles: encode store: %w", err)
	}
	data = append(data, '\n')
	return writeAtomicFile(s.path, data, 0o600)
}

func writeProfileFile(path string, p Profile) error {
	p.Env = nonNilMap(p.Env)
	p.PrependArgs = nonNilSlice(p.PrependArgs)
	if p.Overlay == nil {
		t := true
		p.Overlay = &t
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles: encode %s: %w", path, err)
	}
	data = append(data, '\n')
	return writeAtomicFile(path, data, 0o600)
}

func writeAtomicFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("profiles: create dir for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*.tmp")
	if err != nil {
		return fmt.Errorf("profiles: write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("profiles: write %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("profiles: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("profiles: write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("profiles: write %s: %w", path, err)
	}
	return nil
}

// validateSeedFrom rejects absolute paths and parent-directory escapes.
func validateSeedFrom(seedFrom string) error {
	if seedFrom == "" {
		return nil
	}
	if filepath.IsAbs(seedFrom) {
		return fmt.Errorf("invalid seedFrom: %s (must be a relative path)", seedFrom)
	}
	for _, seg := range strings.Split(filepath.ToSlash(seedFrom), "/") {
		if seg == ".." {
			return fmt.Errorf("invalid seedFrom: %s (must be a relative path)", seedFrom)
		}
	}
	return nil
}

// seedShadowHome copies regular files from <root>/<seedFrom> into dst,
// skipping *.bak files and any .git or node_modules subtree. A missing
// source tree is not an error.
func seedShadowHome(root, seedFrom, dst string) error {
	return seedDir(filepath.Join(root, seedFrom), dst)
}

// seedDir copies regular files and preserves symlinks from src into dst,
// skipping *.bak files and any .git or node_modules subtree. A missing source
// directory is not an error.
func seedDir(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil || !st.IsDir() {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if seg == ".git" || seg == "node_modules" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(d.Name(), ".bak") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(linkTarget, target)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

// copyFile copies a regular file, preserving its permission bits.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// underDir reports whether target is a proper subdirectory of base.
func underDir(base, target string) bool {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Profile resolution, listing, built-in default profiles, and
// native-antigravity credential lookup.
//
// Every agent starts with an implicit unmodified "default" profile
// (SourceDefault). User-added profiles live in the own store
// (SourceStore), and native antigravity credentials are discovered separately
// (SourceNativeAntigravity). All storage and resolution is completely
// independent of T3 Code.
package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// Source labels used by Resolution and ListEntry.
const (
	SourceDefault           = "default"
	SourceStore             = "store"
	SourceNativeAntigravity = "native-antigravity"
)

// NativeAntigravityLabel is the generic display label for native profiles.
// Credential contents are never exposed.
const NativeAntigravityLabel = "Native Antigravity"

// specialNativeNames require a native credential file; a missing file is a
// dedicated login-hint error instead of a generic unknown-profile error.
var specialNativeNames = map[string]bool{
	"antigravity":              true,
	"antigravity_mahajanarham": true,
}

// NativeCredentialDir returns the native-antigravity credential directory:
// $HOME/.harness/credentials/antigravity.
func NativeCredentialDir(paths harnessenv.Paths) string {
	return filepath.Join(harnessenv.HarnessDir(paths), "credentials", "antigravity")
}

// Resolution is the outcome of resolving a profile name.
type Resolution struct {
	Source            string
	Name              string
	Profile           Profile
	Customized        bool // true when a stored default overrides the built-in profile
	NativeAntigravity bool // set when Source == "native-antigravity"
}

// ListEntry is one row of the profile-list overview. It carries names,
// labels, and sources only — never secrets.
type ListEntry struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Source      string `json:"source"`
	Agent       string `json:"agent,omitempty"`
}

// DefaultResolution builds the built-in unmodified "default" profile
// resolution for a canonical agent.
func DefaultResolution(agent string) Resolution {
	return Resolution{
		Source:  SourceDefault,
		Name:    DefaultProfileName,
		Profile: builtinDefaultProfile(mapWantAgent(agent)),
	}
}

// Resolve accepts an agent/name reference or a name unique across agents.
func Resolve(ref string, paths harnessenv.Paths) (Resolution, error) {
	store, err := Open(paths)
	if err != nil {
		return Resolution{}, err
	}
	return store.resolve(ref)
}

func (s *Store) resolve(ref string) (Resolution, error) {
	agent, name, qualified := strings.Cut(ref, "/")
	if !qualified {
		name = ref
	}
	if name == DefaultProfileName {
		if !qualified {
			return Resolution{}, fmt.Errorf("profile %q requires an agent; use agent/default", name)
		}
		agent = mapWantAgent(agent)
		if !isKnownAgent(agent) {
			return Resolution{}, fmt.Errorf("unknown agent: %s", agent)
		}
		p, customized := s.GetDefault(agent)
		return Resolution{Source: SourceDefault, Name: name, Profile: p, Customized: customized}, nil
	}
	keys := s.profileCandidates(ref)
	if len(keys) > 1 {
		return Resolution{}, ambiguousProfile(name, keys)
	}
	if len(keys) == 1 {
		key := keys[0]
		if p, ok := s.profiles[key]; ok {
			if !p.Enabled {
				return Resolution{}, fmt.Errorf("profile disabled: %s", key)
			}
			return Resolution{Source: SourceStore, Name: key.name, Profile: p}, nil
		}
		return nativeResolution(key.name, s.paths), nil
	}
	if qualified {
		keys := s.profileCandidates(name)
		if len(keys) == 1 && keys[0].agent != mapWantAgent(agent) {
			return Resolution{}, fmt.Errorf("profile %q belongs to agent %s, not %s", name, keys[0].agent, agent)
		}
	}
	if (!qualified || mapWantAgent(agent) == "antigravity") && specialNativeNames[name] {
		return Resolution{}, fmt.Errorf("no native credential for profile: %s (create an antigravity profile with: harness profile create %s --agent antigravity)", name, name)
	}
	available := availableNames(s.paths, s)
	if len(available) == 0 {
		return Resolution{}, fmt.Errorf("unknown profile: %s", ref)
	}
	return Resolution{}, fmt.Errorf("unknown profile: %s (available: %s)", ref, strings.Join(available, ", "))
}

func (s *Store) profileCandidates(ref string) []profileKey {
	keys := s.matchingKeys(ref)
	agent, name, qualified := strings.Cut(ref, "/")
	if qualified && mapWantAgent(agent) != "antigravity" {
		return keys
	}
	if !qualified {
		name = ref
	}
	nativeKey := newProfileKey("antigravity", name)
	if _, stored := s.profiles[nativeKey]; !stored && name != DefaultProfileName &&
		hasNativeCredential(s.paths, name) {
		keys = append(keys, nativeKey)
	}
	return keys
}

// ResolveForAgent resolves a name within the specified agent.
func ResolveForAgent(agent, name string, paths harnessenv.Paths) (Resolution, error) {
	return Resolve(newProfileKey(agent, name).String(), paths)
}

// LaunchSpecFrom converts a Resolution into a launch instruction for the
// agents package.
func LaunchSpecFrom(r Resolution) harnessenv.LaunchSpec {
	return LaunchSpecFromPaths(r, harnessenv.DefaultPaths())
}

// ListAll returns built-in defaults, stored profiles, and native credentials.
func ListAll(paths harnessenv.Paths) ([]ListEntry, error) {
	store, err := Open(paths)
	if err != nil {
		return nil, err
	}
	return store.listEntries(""), nil
}

// ListForAgent returns the default and profiles belonging to the given agent.
func ListForAgent(agent string, paths harnessenv.Paths) ([]ListEntry, error) {
	store, err := Open(paths)
	if err != nil {
		return nil, err
	}
	return store.listEntries(mapWantAgent(agent)), nil
}

func (s *Store) listEntries(agent string) []ListEntry {
	var out []ListEntry
	for _, known := range KnownAgents() {
		if agent == "" || agent == known {
			profile, _ := s.defaults[known]
			display := profile.DisplayName
			if display == "" {
				display = builtinDefaultProfile(known).DisplayName
			}
			out = append(out, ListEntry{Name: DefaultProfileName, DisplayName: display, Source: SourceDefault, Agent: known})
		}
	}
	for _, np := range s.List() {
		key := newProfileKey(np.Profile.Agent, np.Name)
		if agent != "" && agent != key.agent {
			continue
		}
		display := np.Profile.DisplayName
		if display == "" {
			display = np.Name
		}
		out = append(out, ListEntry{Name: key.name, DisplayName: display, Source: SourceStore, Agent: key.agent})
	}
	if agent == "" || agent == "antigravity" {
		for _, name := range listNativeCredentials(s.paths) {
			if _, stored := s.profiles[newProfileKey("antigravity", name)]; stored || name == DefaultProfileName {
				continue
			}
			out = append(out, ListEntry{Name: name, DisplayName: NativeAntigravityLabel, Source: SourceNativeAntigravity, Agent: "antigravity"})
		}
	}
	return out
}

// FormatPlain renders entries as tab-separated NAME and AGENT lines (with
// DisplayName as a third column when it differs from Name).
func FormatPlain(entries []ListEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name)
		b.WriteByte('\t')
		b.WriteString(e.Agent)
		if e.DisplayName != "" && e.DisplayName != e.Name {
			b.WriteByte('\t')
			b.WriteString(e.DisplayName)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// availableNames lists every resolvable custom/native name, sorted, for error messages.
func availableNames(paths harnessenv.Paths, store *Store) []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if store != nil {
		for _, np := range store.List() {
			add(np.Name)
		}
	}
	for _, n := range listNativeCredentials(paths) {
		add(n)
	}
	sort.Strings(names)
	return names
}

// nativeResolution builds a Resolution for a valid native credential.
func nativeResolution(name string, paths harnessenv.Paths) Resolution {
	return Resolution{
		Source: SourceNativeAntigravity,
		Name:   name,
		Profile: Profile{
			Agent:       "antigravity-native",
			DisplayName: NativeAntigravityLabel,
			Enabled:     true,
			ShadowHome:  filepath.Join(harnessenv.HarnessDir(paths), "antigravity-native", name),
		},
		NativeAntigravity: true,
	}
}

// expandTildePath expands a leading "~" (alone or followed by a slash) to home.
func expandTildePath(value, home string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value
}

// hasNativeCredential reports whether a valid credential file exists for
// name. Only the presence of token.refresh_token is checked; the file is
// never logged and parse failures count as absent.
func hasNativeCredential(paths harnessenv.Paths, name string) bool {
	if name == "." || name == ".." || !safeCredentialRe.MatchString(name) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(NativeCredentialDir(paths), name+".json"))
	if err != nil {
		return false
	}
	var cred struct {
		Token *struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return false
	}
	return cred.Token != nil && cred.Token.RefreshToken != ""
}

// listNativeCredentials returns the sorted names with valid credential
// files, restricted to safe names.
func listNativeCredentials(paths harnessenv.Paths) []string {
	entries, err := os.ReadDir(NativeCredentialDir(paths))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if hasNativeCredential(paths, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copySlice(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

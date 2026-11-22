// Profile resolution, listing, and built-in default profiles.
//
// Every agent starts with an implicit unmodified "default" profile
// (SourceDefault). User-added profiles live in the own store (SourceStore).
package profiles

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// Source labels used by Resolution and ListEntry.
const (
	SourceDefault = "default"
	SourceStore   = "store"
)

// Resolution is the outcome of resolving a profile name.
type Resolution struct {
	Source     string
	Name       string
	Profile    Profile
	Customized bool // true when a stored default overrides the built-in profile
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
		Profile: builtinDefaultProfile(agent),
	}
}

// Resolve accepts an agent/name reference or a name unique across agents.
func Resolve(ref string, paths harnessenv.Paths) (Resolution, error) {
	store, err := Open(paths)
	if err != nil {
		return Resolution{}, err
	}
	return store.Resolve(ref)
}

// Resolve resolves an agent/name reference or a unique name from an already-open Store.
func (s *Store) Resolve(ref string) (Resolution, error) {
	return s.resolve(ref)
}

// ResolveForAgent resolves a profile name within the specified agent from an already-open Store.
func (s *Store) ResolveForAgent(agent, name string) (Resolution, error) {
	return s.resolve(newProfileKey(agent, name).String())
}

// ListAll returns built-in defaults and stored profiles from an already-open Store.
func (s *Store) ListAll() []ListEntry {
	return s.listEntries("")
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
		if !isKnownAgent(agent) {
			return Resolution{}, fmt.Errorf("unknown agent: %s", agent)
		}
		p, customized := s.GetDefault(agent)
		return Resolution{Source: SourceDefault, Name: name, Profile: p, Customized: customized}, nil
	}
	keys := s.matchingKeys(ref)
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
	}
	if qualified {
		keys := s.matchingKeys(name)
		if len(keys) == 1 && keys[0].agent != agent {
			return Resolution{}, fmt.Errorf("profile %q belongs to agent %s, not %s", name, keys[0].agent, agent)
		}
	}
	available := availableNames(s)
	if len(available) == 0 {
		return Resolution{}, fmt.Errorf("unknown profile: %s", ref)
	}
	return Resolution{}, fmt.Errorf("unknown profile: %s (available: %s)", ref, strings.Join(available, ", "))
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

// ListAll returns built-in defaults and stored profiles.
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
	return store.listEntries(agent), nil
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

// availableNames lists every resolvable custom name, sorted, for error messages.
func availableNames(store *Store) []string {
	seen := map[string]bool{}
	var names []string
	if store != nil {
		for _, np := range store.List() {
			if !seen[np.Name] {
				seen[np.Name] = true
				names = append(names, np.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// expandTildePath expands leading "~" / "~/" and "${HOME}" / "$HOME" references to home.
func expandTildePath(value, home string) string {
	return harnessenv.ExpandHome(value, home)
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

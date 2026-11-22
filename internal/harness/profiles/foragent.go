// ForAgent and paths-aware launch-spec helpers.

package profiles

import (
	"path/filepath"
	"sort"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// ForAgent returns every active profile belonging to the canonical agent:
// always starting with the built-in unmodified "default" profile, followed
// by all enabled/resolvable custom profiles sorted by Name.
//
// Thus an agent with no user-created profiles has len == 1 (just "default"),
// and adding a custom profile yields len == 2 ("default" + the custom profile).
func ForAgent(agent string, paths harnessenv.Paths) ([]Resolution, error) {
	if !isKnownAgent(agent) {
		return nil, nil
	}
	store, err := Open(paths)
	if err != nil {
		return nil, err
	}
	var out []Resolution
	for _, entry := range store.listEntries(agent) {
		res, err := store.resolve(newProfileKey(entry.Agent, entry.Name).String())
		if err == nil {
			out = append(out, res)
		}
	}
	custom := out[1:]
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })
	return out, nil
}

// LaunchSpecFromPaths converts a Resolution into a launch instruction using
// the given paths for the dotfiles root. It is the paths-aware core of
// LaunchSpecFrom; prefer it in new code so tests with custom harnessenv.Paths
// (or XDG_CONFIG_HOME/HOME overrides) resolve correctly.
func LaunchSpecFromPaths(r Resolution, paths harnessenv.Paths) harnessenv.LaunchSpec {
	p := r.Profile
	if r.Source == SourceDefault && !r.Customized {
		return harnessenv.LaunchSpec{Agent: p.Agent, DisplayName: p.DisplayName}
	}
	homePath := expandTildePath(p.HomePath, paths.Home)
	if homePath == DefaultSharedConfigHome(p.Agent, paths) || homePath == DefaultSharedHome(p.Agent, paths) {
		homePath = ""
	}
	shadow := expandTildePath(p.ShadowHome, paths.Home)
	if shadow == "" && homePath == "" && p.Agent == "antigravity" &&
		(r.Source == SourceStore || !p.Router) {
		shadow = filepath.Join(ProfilesDir(paths), p.Agent, r.Name, "home")
	}
	return harnessenv.LaunchSpec{
		Agent:       p.Agent,
		DisplayName: p.DisplayName,
		Env:         copyMap(p.Env),
		PrependArgs: copySlice(p.PrependArgs),
		Binary:      expandTildePath(p.BinaryPath, paths.Home),
		HomePath:    homePath,
		ShadowHome:  shadow,
		NoRouter:    !p.Router,
	}
}

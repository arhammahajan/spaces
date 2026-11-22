package profiles

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func forAgentPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("HARNESS_DOTFILES", filepath.Join(tmp, "dots-missing"))
	t.Setenv("HARNESS_SHADOW_HOME_TEMPLATE", "")
	return harnessenv.DefaultPaths()
}

func mustStoreProfile(t *testing.T, paths harnessenv.Paths, name, agent string, enabled bool) {
	t.Helper()
	s, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(name, Profile{Agent: agent, Enabled: enabled, Router: true}, CreateOptions{NoSeed: true}); err != nil {
		t.Fatalf("Create(%s) = %v", name, err)
	}
}

func forAgentNames(res []Resolution) []string {
	names := make([]string, 0, len(res))
	for _, r := range res {
		names = append(names, r.Name)
	}
	return names
}

func TestForAgentDefaultOnly(t *testing.T) {
	paths := forAgentPaths(t)
	res, err := ForAgent("pi", paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != DefaultProfileName || res[0].Source != SourceDefault {
		t.Fatalf("empty store = %+v, want [default]", res)
	}
	if res[0].Profile.Agent != "pi" {
		t.Fatalf("agent = %q, want pi", res[0].Profile.Agent)
	}
}

func TestForAgentSingleCustomYieldsTwo(t *testing.T) {
	paths := forAgentPaths(t)
	mustStoreProfile(t, paths, "solo", "pi", true)
	res, err := ForAgent("pi", paths)
	if err != nil {
		t.Fatal(err)
	}
	got := forAgentNames(res)
	want := []string{"default", "solo"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ForAgent(pi) = %v, want %v", got, want)
	}
	if res[0].Source != SourceDefault || res[1].Source != SourceStore {
		t.Fatalf("sources = [%s, %s], want [default, store]", res[0].Source, res[1].Source)
	}
}

func TestForAgentMultipleSortedAfterDefault(t *testing.T) {
	paths := forAgentPaths(t)
	mustStoreProfile(t, paths, "zeta", "pi", true)
	mustStoreProfile(t, paths, "alpha", "pi", true)
	mustStoreProfile(t, paths, "mid", "pi", true)
	res, err := ForAgent("pi", paths)
	if err != nil {
		t.Fatal(err)
	}
	got := forAgentNames(res)
	want := []string{"default", "alpha", "mid", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ForAgent(pi) = %v, want sorted %v", got, want)
	}
}

func TestForAgentFiltersByAgent(t *testing.T) {
	paths := forAgentPaths(t)
	mustStoreProfile(t, paths, "p-one", "pi", true)
	mustStoreProfile(t, paths, "c-one", "codex", true)
	res, err := ForAgent("pi", paths)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(forAgentNames(res), ",") != "default,p-one" {
		t.Fatalf("ForAgent(pi) = %v, want [default p-one]", forAgentNames(res))
	}
	res, err = ForAgent("codex", paths)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(forAgentNames(res), ",") != "default,c-one" {
		t.Fatalf("ForAgent(codex) = %v, want [default c-one]", forAgentNames(res))
	}
}

func TestForAgentDisabledExcluded(t *testing.T) {
	paths := forAgentPaths(t)
	mustStoreProfile(t, paths, "live", "pi", true)
	mustStoreProfile(t, paths, "dead", "pi", false)
	res, err := ForAgent("pi", paths)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(forAgentNames(res), ",") != "default,live" {
		t.Fatalf("disabled store profile must be excluded, got %v", forAgentNames(res))
	}
}

func TestLaunchSpecFromPathsUsesPaths(t *testing.T) {
	t.Run("default spec is unmodified", func(t *testing.T) {
		paths := forAgentPaths(t)
		r := DefaultResolution("codex")
		spec := LaunchSpecFromPaths(r, paths)
		if spec.Agent != "codex" || spec.ShadowHome != "" || spec.Binary != "" || len(spec.Env) != 0 || len(spec.PrependArgs) != 0 {
			t.Fatalf("default spec must be unmodified, got %+v", spec)
		}
	})

	t.Run("store spec carries profile fields", func(t *testing.T) {
		paths := forAgentPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "w", Profile{Agent: "codex", DisplayName: "W", Enabled: true,
			Env: map[string]string{"A": "b"}, PrependArgs: []string{"--z"}, Router: true},
			CreateOptions{NoSeed: true})
		r, err := Resolve("w", paths)
		if err != nil {
			t.Fatal(err)
		}
		spec := LaunchSpecFromPaths(r, paths)
		if spec.Agent != "codex" || spec.Env["A"] != "b" || len(spec.PrependArgs) != 1 {
			t.Fatalf("store spec wrong: %+v", spec)
		}
	})
}

func TestLaunchSpecFromBackwardsCompat(t *testing.T) {
	paths := forAgentPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "w", Profile{Agent: "pi", DisplayName: "W", Enabled: true, Router: true},
		CreateOptions{NoSeed: true})
	r, err := Resolve("w", paths)
	if err != nil {
		t.Fatal(err)
	}
	a := LaunchSpecFrom(r)
	b := LaunchSpecFromPaths(r, paths)
	if a.Agent != b.Agent || a.DisplayName != b.DisplayName {
		t.Fatalf("LaunchSpecFrom = %+v, LaunchSpecFromPaths = %+v, want equal", a, b)
	}
}

func TestForAgentUnknownAgentEmpty(t *testing.T) {
	paths := forAgentPaths(t)
	mustStoreProfile(t, paths, "solo", "pi", true)
	res, err := ForAgent("bogus-agent", paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("unknown agent = %v, want none", forAgentNames(res))
	}
}

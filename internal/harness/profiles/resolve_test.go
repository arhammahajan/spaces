package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveIndependentOfT3Settings(t *testing.T) {
	paths := testPaths(t)
	// Even if a T3 settings file exists in $HOME/.t3/userdata/settings.json,
	// harness profiles must ignore it completely.
	t3Dir := filepath.Join(paths.Home, ".t3", "userdata")
	if err := os.MkdirAll(t3Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"providerInstances":{"codex":{"displayName":"T3 Codex"},"grok_router":{"driver":"grok"}}}`
	if err := os.WriteFile(filepath.Join(t3Dir, "settings.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("codex", paths); err == nil {
		t.Fatal("T3 settings must not resolve as a harness profile")
	}
	if _, err := Resolve("grok_router", paths); err == nil {
		t.Fatal("T3 settings must not resolve as a harness profile")
	}
	entries, err := ListAll(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(KnownAgents()) {
		t.Fatalf("ListAll with only T3 settings = %d entries, want only built-in defaults", len(entries))
	}
	for _, entry := range entries {
		if entry.Source != SourceDefault {
			t.Fatalf("unexpected profile from T3 settings: %+v", entry)
		}
	}
	res, err := ForAgent("codex", paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != DefaultProfileName || res[0].Source != SourceDefault {
		t.Fatalf("ForAgent(codex) = %+v, want only built-in default", res)
	}
}

func TestLegacyProfilesMigrateWithScopedNames(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(filepath.Dir(StorePath(paths)), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"shadowHomeTemplate":"{home}/agents/{agent}/{name}","profiles":{"work":{"agent":"codex","displayName":"Legacy Codex"}}}`
	if err := os.WriteFile(StorePath(paths), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, store, "work", Profile{Agent: "claude", Enabled: true}, CreateOptions{NoSeed: true})
	legacyProfile, err := Resolve("codex/work", paths)
	if err != nil || legacyProfile.Profile.DisplayName != "Legacy Codex" {
		t.Fatalf("legacy profile = (%+v, %v)", legacyProfile, err)
	}
	if _, err := Resolve("claude/work", paths); err != nil {
		t.Fatal(err)
	}
	store, err = Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ShadowHomeTemplate(); got != "{home}/agents/{agent}/{name}" {
		t.Fatalf("template = %q after migration", got)
	}
	if err := store.Remove("codex/work", false); err != nil {
		t.Fatal(err)
	}
	if res, err := Resolve("work", paths); err != nil || res.Profile.Agent != "claude" {
		t.Fatalf("remaining work profile = (%+v, %v)", res, err)
	}
}

func TestResolveForAgentDefaultAndCustom(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "work", Profile{Agent: "claude", Enabled: true, Router: true}, CreateOptions{NoSeed: true})

	// "default" always resolves for any agent.
	r, err := ResolveForAgent("claude", DefaultProfileName, paths)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != SourceDefault || r.Name != DefaultProfileName || r.Profile.Agent != "claude" {
		t.Fatalf("default resolution = %+v", r)
	}
	// Custom profile resolves for its own agent and fails for another agent.
	r, err = ResolveForAgent("claude", "work", paths)
	if err != nil || r.Source != SourceStore {
		t.Fatalf("custom resolution = (%+v, %v)", r, err)
	}
	if _, err := ResolveForAgent("codex", "work", paths); err == nil || !strings.Contains(err.Error(), "belongs to agent claude") {
		t.Fatalf("wrong agent err = %v", err)
	}
}

func TestDefaultProfileNameReserved(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	if err := s.Create(DefaultProfileName, Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Create(default) err = %v, want reserved error", err)
	}
	mustCreate(t, s, "custom", Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
	if err := s.Clone("custom", DefaultProfileName, CreateOptions{NoSeed: true}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Clone(_, default) err = %v, want reserved error", err)
	}
	if err := s.Rename("custom", DefaultProfileName); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("Rename(_, default) err = %v, want reserved error", err)
	}
	if err := s.Rename(DefaultProfileName, "other"); err == nil {
		t.Fatal("Rename(default, _) must fail")
	}
	if err := s.Remove(DefaultProfileName, false); err == nil {
		t.Fatal("Remove(default) must fail")
	}
	if err := s.Edit(DefaultProfileName, EditPatch{}); err == nil {
		t.Fatal("Edit(default) must fail")
	}
	if err := s.SetEnabled(DefaultProfileName, false); err == nil {
		t.Fatal("SetEnabled(default) must fail")
	}
}

func TestDefaultShadowHomeTemplateAndAutoSeed(t *testing.T) {
	t.Run("default template uses agent and name", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_SHADOW_HOME_TEMPLATE", "")
		s, _ := Open(paths)
		mustCreate(t, s, "work", Profile{Agent: "claude", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		np, ok := s.Get("work")
		if !ok {
			t.Fatal("profile missing")
		}
		want := filepath.Join(paths.Home, ".harness", "profiles", "claude", "work", "home")
		if np.Profile.ShadowHome != want {
			t.Fatalf("ShadowHome = %q, want %q", np.Profile.ShadowHome, want)
		}
		if st, err := os.Stat(want); err != nil || !st.IsDir() {
			t.Fatalf("shadow home dir missing: %v", err)
		}
	})

	t.Run("env template override", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_SHADOW_HOME_TEMPLATE", "~/custom-shadows/{agent}/{name}")
		s, _ := Open(paths)
		mustCreate(t, s, "work", Profile{Agent: "codex", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		np, _ := s.Get("work")
		want := filepath.Join(paths.Home, "custom-shadows", "codex", "work")
		if np.Profile.ShadowHome != want {
			t.Fatalf("ShadowHome = %q, want %q", np.Profile.ShadowHome, want)
		}
	})

	t.Run("store shadowHomeTemplate persisted and honored", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_SHADOW_HOME_TEMPLATE", "")
		if err := os.MkdirAll(filepath.Dir(StorePath(paths)), 0o755); err != nil {
			t.Fatal(err)
		}
		doc := `{"version":1,"shadowHomeTemplate":"{state_home}/harness-homes/{agent}-{name}","profiles":{}}`
		if err := os.WriteFile(StorePath(paths), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(paths)
		if err != nil {
			t.Fatal(err)
		}
		mustCreate(t, s, "alt", Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		np, _ := s.Get("alt")
		want := filepath.Join(paths.StateHome, "harness-homes", "pi-alt")
		if np.Profile.ShadowHome != want {
			t.Fatalf("ShadowHome = %q, want %q", np.Profile.ShadowHome, want)
		}
		// Template survives Save/Open round-trip.
		s2, err := Open(paths)
		if err != nil {
			t.Fatal(err)
		}
		if s2.ShadowHomeTemplate() != "{state_home}/harness-homes/{agent}-{name}" {
			t.Fatalf("ShadowHomeTemplate lost on save: %q", s2.ShadowHomeTemplate())
		}
	})

	t.Run("auto-seeds from default agent template dir without inheriting default home", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("HARNESS_SHADOW_HOME_TEMPLATE", "")
		// Plant default agent home state that must NOT bleed into a new profile.
		defaultHome := filepath.Join(paths.Home, ".claude")
		if err := os.MkdirAll(defaultHome, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(defaultHome, "settings.json"), []byte(`{"default":"yes"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		// Plant starting template with skills symlink, MCP config, and hooks.
		sharedSkills := filepath.Join(paths.Home, "shared-skills")
		if err := os.MkdirAll(sharedSkills, 0o755); err != nil {
			t.Fatal(err)
		}
		tmplDir := DefaultTemplateDir(paths, "claude")
		if err := os.MkdirAll(filepath.Join(tmplDir, "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sharedSkills, filepath.Join(tmplDir, "skills")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmplDir, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmplDir, "hooks", "notify.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		s, _ := Open(paths)
		off := false
		mustCreate(t, s, "work", Profile{Agent: "claude", Enabled: true, Overlay: &off}, CreateOptions{})
		np, _ := s.Get("work")
		if _, err := os.Lstat(filepath.Join(np.Profile.ShadowHome, "settings.json")); !os.IsNotExist(err) {
			t.Fatalf("default home settings.json must not be overlaid into new profile (err=%v)", err)
		}
		gotLink, err := os.Readlink(filepath.Join(np.Profile.ShadowHome, "skills"))
		if err != nil || gotLink != sharedSkills {
			t.Fatalf("template skills symlink = (%q, %v), want %q", gotLink, err, sharedSkills)
		}
		data, err := os.ReadFile(filepath.Join(np.Profile.ShadowHome, "mcp.json"))
		if err != nil || string(data) != `{"mcpServers":{}}` {
			t.Fatalf("template mcp.json = (%q, %v)", string(data), err)
		}
		if _, err := os.Stat(filepath.Join(np.Profile.ShadowHome, "hooks", "notify.json")); err != nil {
			t.Fatalf("template hooks not seeded: %v", err)
		}
	})
}

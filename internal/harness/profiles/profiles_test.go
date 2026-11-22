package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// testPaths points every home-derived location at temp dirs and isolates the
// dotfiles root so no test touches the real home.
func testPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("HARNESS_DOTFILES", filepath.Join(tmp, "dots-missing"))
	return harnessenv.DefaultPaths()
}

func mustCreate(t *testing.T, s *Store, name string, p Profile, opts CreateOptions) {
	t.Helper()
	if err := s.Create(name, p, opts); err != nil {
		t.Fatalf("Create(%s) = %v", name, err)
	}
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s, err := Open(testPaths(t))
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("expected empty store, got %v", s.List())
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get on empty store must report missing")
	}
}

func TestCreateAndRoundTrip(t *testing.T) {
	paths := testPaths(t)
	s, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	in := Profile{
		Agent:       "codex",
		DisplayName: "Codex (Model Router)",
		Enabled:     true,
		SeedFrom:    "",
		Env:         map[string]string{"HARNESS_EXTRA": "1"},
		PrependArgs: []string{"--x", "1"},
		Router:      true,
	}
	mustCreate(t, s, "codex_router", in, CreateOptions{NoSeed: true})

	// In-memory readback.
	np, ok := s.Get("codex_router")
	if !ok {
		t.Fatal("Get(codex_router) missing after Create")
	}
	if np.Profile.Agent != "codex" || np.Profile.DisplayName != "Codex (Model Router)" {
		t.Fatalf("unexpected profile: %+v", np.Profile)
	}
	wantShadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "codex_router", "home")
	if np.Profile.ShadowHome != wantShadow {
		t.Fatalf("ShadowHome = %q, want %q", np.Profile.ShadowHome, wantShadow)
	}
	if st, err := os.Stat(wantShadow); err != nil || !st.IsDir() {
		t.Fatalf("shadow home must exist: %v", err)
	}

	// On-disk per-profile JSON: camelCase keys.
	raw, err := os.ReadFile(ProfileFilePath(paths, "codex", "codex_router"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"displayName"`, `"binaryPath"`, `"homePath"`, `"shadowHomePath"`, `"seedFrom"`, `"prependArgs"`, `"router"`, `"overlay"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("profile JSON missing %s:\n%s", key, raw)
		}
	}

	// Reopen from disk.
	s2, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	np2, ok := s2.Get("codex_router")
	if !ok {
		t.Fatal("Get after reopen missing")
	}
	if np2.Profile.Env["HARNESS_EXTRA"] != "1" || len(np2.Profile.PrependArgs) != 2 || !np2.Profile.Router {
		t.Fatalf("round trip mismatch: %+v", np2.Profile)
	}
	if got := len(s2.List()); got != 1 {
		t.Fatalf("List len = %d, want 1", got)
	}
}

func TestSaveMode0600(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "codex", Profile{Agent: "codex", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
	info, err := os.Stat(ProfileFilePath(paths, "codex", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("profile JSON mode = %o, want 600", perm)
	}
}

func TestListSorted(t *testing.T) {
	s, _ := Open(testPaths(t))
	mustCreate(t, s, "bprof", Profile{Agent: "pi", Router: true}, CreateOptions{NoSeed: true})
	mustCreate(t, s, "aprof", Profile{Agent: "pi", Router: true}, CreateOptions{NoSeed: true})
	list := s.List()
	if len(list) != 2 || list[0].Name != "aprof" || list[1].Name != "bprof" {
		t.Fatalf("unsorted list: %+v", list)
	}
}

func TestCreateValidation(t *testing.T) {
	badNames := []string{"", "a b", "-x", "1abc", "a/b", "a.B", "_lead", strings.Repeat("a", 65)}
	for _, name := range badNames {
		s, _ := Open(testPaths(t))
		err := s.Create(name, Profile{Agent: "codex", Router: true}, CreateOptions{NoSeed: true})
		if err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Errorf("Create(%q) = %v, want invalid-name error", name, err)
		}
	}
	// T3-style slugs allow a leading letter of either case.
	for _, name := range []string{"Abc", "UPPER", "Codex1", "a-B_c9"} {
		s, _ := Open(testPaths(t))
		if err := s.Create(name, Profile{Agent: "codex", Router: true}, CreateOptions{NoSeed: true}); err != nil {
			t.Errorf("Create(%q) = %v, want success", name, err)
		}
	}
	badAgents := []string{"", "Codex", "bogus", "codex-router"}
	for _, agent := range badAgents {
		s, _ := Open(testPaths(t))
		err := s.Create("goodname", Profile{Agent: agent, Router: true}, CreateOptions{NoSeed: true})
		if err == nil || !strings.Contains(err.Error(), "unknown agent") {
			t.Errorf("Create(agent=%q) = %v, want unknown-agent error", agent, err)
		}
	}

	// Every frozen agent is accepted.
	for _, agent := range KnownAgents() {
		s, _ := Open(testPaths(t))
		if err := s.Create("p1", Profile{Agent: agent, Router: true}, CreateOptions{NoSeed: true}); err != nil {
			t.Errorf("Create(agent=%q) = %v, want success", agent, err)
		}
	}

	// Duplicates rejected.
	s, _ := Open(testPaths(t))
	mustCreate(t, s, "dup", Profile{Agent: "codex", Router: true}, CreateOptions{NoSeed: true})
	if err := s.Create("dup", Profile{Agent: "codex", Router: true}, CreateOptions{NoSeed: true}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate Create = %v, want already-exists error", err)
	}

	// Bad seedFrom rejected before any side effect.
	s, _ = Open(testPaths(t))
	for _, seed := range []string{"/abs/path", "../escape"} {
		err := s.Create("badseed", Profile{Agent: "codex", SeedFrom: seed, Router: true}, CreateOptions{})
		if err == nil || !strings.Contains(err.Error(), "invalid seedFrom") {
			t.Errorf("Create(seedFrom=%q) = %v, want invalid-seedFrom error", seed, err)
		}
	}
	if _, ok := s.Get("badseed"); ok {
		t.Error("failed Create must not insert the profile")
	}
}

func TestRouterDefaultsTrueWhenAbsent(t *testing.T) {
	paths := testPaths(t)
	dir := filepath.Dir(StorePath(paths))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"profiles":{"plain":{"agent":"pi","displayName":"P","enabled":true,"shadowHomePath":"/tmp/x","seedFrom":"","env":{},"prependArgs":[]}}}`
	if err := os.WriteFile(StorePath(paths), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	np, ok := s.Get("plain")
	if !ok {
		t.Fatal("profile missing")
	}
	if !np.Profile.Router {
		t.Error("Router must default to true when the key is absent")
	}
}

func writeDotsTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"codex/config.json":               `{"a":1}`,
		"codex/sub/deep.txt":              "deep",
		"codex/sub/.git/nested":           "skip",
		"codex/old.bak":                   "skip",
		"codex/.git/HEAD":                 "skip",
		"codex/.git/objects/x":            "skip",
		"codex/node_modules/pkg/index.js": "skip",
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("config.json", filepath.Join(root, "codex", "link")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSeedsShadowHome(t *testing.T) {
	paths := testPaths(t)
	dots := t.TempDir()
	writeDotsTree(t, dots)

	s, _ := Open(paths)
	mustCreate(t, s, "seeded", Profile{Agent: "codex", Enabled: true, SeedFrom: "codex", Router: true},
		CreateOptions{SeedRoot: dots})
	shadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "seeded", "home")

	for _, rel := range []string{"config.json", "sub/deep.txt"} {
		data, err := os.ReadFile(filepath.Join(shadow, rel))
		if err != nil {
			t.Errorf("seeded file %s missing: %v", rel, err)
		} else if len(data) == 0 {
			t.Errorf("seeded file %s empty", rel)
		}
	}
	for _, rel := range []string{
		"old.bak", ".git/HEAD", ".git/objects/x", "sub/.git/nested",
		"node_modules/pkg/index.js",
	} {
		if _, err := os.Lstat(filepath.Join(shadow, rel)); !os.IsNotExist(err) {
			t.Errorf("seeded tree must not contain %s", rel)
		}
	}
	// Symlinks in templates (e.g. shared skills/hooks/mcps) are preserved.
	linkTarget, err := os.Readlink(filepath.Join(shadow, "link"))
	if err != nil {
		t.Fatalf("seeded symlink missing: %v", err)
	}
	if linkTarget != "config.json" {
		t.Errorf("seeded symlink target = %q, want %q", linkTarget, "config.json")
	}
}

func TestCreateNoSeedAndMissingRoot(t *testing.T) {
	paths := testPaths(t)
	dots := t.TempDir()
	writeDotsTree(t, dots)

	s, _ := Open(paths)
	off := false
	mustCreate(t, s, "noseed", Profile{Agent: "codex", Enabled: true, SeedFrom: "codex", Router: true, Overlay: &off},
		CreateOptions{SeedRoot: dots, NoSeed: true})
	shadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "noseed", "home")
	entries, err := os.ReadDir(shadow)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("NoSeed with overlay disabled must leave shadow home empty, got %v", entries)
	}

	// Missing dotfiles root (default HARNESS_DOTFILES points nowhere): skip.
	s2, _ := Open(paths)
	mustCreate(t, s2, "noroot", Profile{Agent: "codex", Enabled: true, SeedFrom: "codex", Router: true},
		CreateOptions{})
	if _, ok := s2.Get("noroot"); !ok {
		t.Fatal("create with missing dots root must still succeed")
	}
}

func TestRemove(t *testing.T) {
	t.Run("purge deletes shadow under base", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "gone", Profile{Agent: "codex", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		shadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "gone", "home")
		if err := os.WriteFile(filepath.Join(shadow, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := s.Remove("gone", true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(shadow); !os.IsNotExist(err) {
			t.Error("purge must delete the shadow home")
		}
		if _, ok := s.Get("gone"); ok {
			t.Error("profile must be gone from the store")
		}
	})

	t.Run("without purge keeps shadow", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "kept", Profile{Agent: "codex", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		shadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "kept", "home")
		if err := s.Remove("kept", false); err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(shadow); err != nil || !st.IsDir() {
			t.Error("non-purge remove must keep the shadow home")
		}
	})

	t.Run("purge spares shadow outside base", func(t *testing.T) {
		paths := testPaths(t)
		outside := filepath.Join(t.TempDir(), "outside-home")
		s, _ := Open(paths)
		mustCreate(t, s, "ext", Profile{Agent: "codex", Enabled: true, ShadowHome: outside, Router: true},
			CreateOptions{NoSeed: true})
		if err := s.Remove("ext", true); err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(outside); err != nil || !st.IsDir() {
			t.Error("purge must not delete a shadow home outside the base dir")
		}
	})

	t.Run("unknown profile", func(t *testing.T) {
		s, _ := Open(testPaths(t))
		if err := s.Remove("nope", true); err == nil || !strings.Contains(err.Error(), "unknown profile") {
			t.Errorf("Remove(missing) = %v, want unknown-profile error", err)
		}
	})
}

func TestCorruptStoreErrors(t *testing.T) {
	paths := testPaths(t)
	dir := filepath.Dir(StorePath(paths))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StorePath(paths), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(paths); err == nil {
		t.Error("Open must fail on corrupt JSON")
	}
}

func TestStoreJSONRoundTripPreservesRouterFalse(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "plain", Profile{Agent: "pi", Enabled: true, Router: false}, CreateOptions{NoSeed: true})
	s2, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	np, _ := s2.Get("plain")
	if np.Profile.Router {
		t.Error("explicit router:false must survive a round trip")
	}
	raw, err := os.ReadFile(ProfileFilePath(paths, "pi", "plain"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["router"] != false {
		t.Error("stored JSON must carry router:false explicitly")
	}
}

func TestDefaultProfileJSONOverridesAndReset(t *testing.T) {
	paths := testPaths(t)
	s, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureDefaults(func(agent string) string { return "/default/bin/" + agent }); err != nil {
		t.Fatal(err)
	}
	// Every known agent should be seeded in defaults with suitable defaults.
	s2, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	p, customized := s2.GetDefault("claude")
	if !customized {
		t.Fatal("expected claude default to be persisted by EnsureDefaults")
	}
	wantShared := DefaultSharedConfigHome("claude", paths)
	if p.BinaryPath != "/default/bin/claude" || p.HomePath != wantShared || !p.Router {
		t.Fatalf("unexpected seeded default: %+v (want homePath %q)", p, wantShared)
	}
	// Simulate user editing ~/.harness/profiles.json directly.
	customHome := filepath.Join(paths.Home, "custom-claude")
	customBin := "/custom/claude"
	noRouter := false
	if err := s2.EditDefault("claude", EditPatch{
		BinaryPath:  &customBin,
		HomePath:    &customHome,
		SetEnv:      map[string]string{"MY_FLAG": "1"},
		PrependArgs: []string{"--verbose"},
		Router:      &noRouter,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := ResolveForAgent("claude", DefaultProfileName, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Customized || res.Source != SourceDefault {
		t.Fatalf("ResolveForAgent default = %+v", res)
	}
	spec := LaunchSpecFromPaths(res, paths)
	if spec.Binary != customBin || spec.HomePath != customHome || !spec.NoRouter || spec.Env["MY_FLAG"] != "1" || len(spec.PrependArgs) != 1 || spec.PrependArgs[0] != "--verbose" {
		t.Fatalf("LaunchSpecFromPaths = %+v", spec)
	}
	// ResetDefault restores unmodified built-in default.
	if err := s2.ResetDefault("claude"); err != nil {
		t.Fatal(err)
	}
	res2, err := ResolveForAgent("claude", DefaultProfileName, paths)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Customized {
		t.Fatalf("expected unmodified default after reset, got %+v", res2)
	}
}

package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

func TestSetEnabled(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "p1", Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
	if err := s.SetEnabled("p1", false); err != nil {
		t.Fatal(err)
	}
	np, _ := s.Get("p1")
	if np.Profile.Enabled {
		t.Fatal("SetEnabled(false) did not disable")
	}
	if _, err := Resolve("p1", paths); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled profile must fail Resolve, got %v", err)
	}
	if err := s.SetEnabled("p1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("p1", paths); err != nil {
		t.Fatalf("re-enabled profile must resolve: %v", err)
	}
	if err := s.SetEnabled("nope", true); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("SetEnabled(missing) = %v, want unknown-profile", err)
	}
	// persists
	s2, _ := Open(paths)
	np2, _ := s2.Get("p1")
	if !np2.Profile.Enabled {
		t.Fatal("SetEnabled(true) did not persist")
	}
}

func TestEditBasics(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "e1", Profile{Agent: "codex", Enabled: true, Router: true, Env: map[string]string{"A": "1", "B": "2"}, PrependArgs: []string{"--x"}}, CreateOptions{NoSeed: true})

	sharedHome := filepath.Join(t.TempDir(), "shared-home")
	patch := EditPatch{
		DisplayName: strptr("Pretty"),
		BinaryPath:  strptr("/bin/custom"),
		HomePath:    strptr(sharedHome),
		SetEnv:      map[string]string{"B": "22", "C": "3"},
		UnsetEnv:    []string{"A"},
		PrependArgs: []string{"--y"},
		Router:      boolptr(false),
	}
	if err := s.Edit("e1", patch); err != nil {
		t.Fatal(err)
	}
	np, _ := s.Get("e1")
	if np.Profile.DisplayName != "Pretty" || np.Profile.BinaryPath != "/bin/custom" || np.Profile.HomePath != sharedHome {
		t.Fatalf("scalar edit failed: %+v", np.Profile)
	}
	if np.Profile.Env["B"] != "22" || np.Profile.Env["C"] != "3" {
		t.Fatalf("SetEnv failed: %v", np.Profile.Env)
	}
	if _, ok := np.Profile.Env["A"]; ok {
		t.Fatalf("UnsetEnv failed: %v", np.Profile.Env)
	}
	// append semantics when SetPrependArgs is false
	if len(np.Profile.PrependArgs) != 2 || np.Profile.PrependArgs[0] != "--x" || np.Profile.PrependArgs[1] != "--y" {
		t.Fatalf("PrependArgs append failed: %v", np.Profile.PrependArgs)
	}
	if np.Profile.Router {
		t.Fatal("Router edit failed")
	}

	// replace
	if err := s.Edit("e1", EditPatch{PrependArgs: []string{"--new"}, SetPrependArgs: true}); err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("e1")
	if len(np.Profile.PrependArgs) != 1 || np.Profile.PrependArgs[0] != "--new" {
		t.Fatalf("SetPrependArgs replace failed: %v", np.Profile.PrependArgs)
	}
	// clear
	if err := s.Edit("e1", EditPatch{ClearPrependArgs: true}); err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("e1")
	if len(np.Profile.PrependArgs) != 0 {
		t.Fatalf("ClearPrependArgs failed: %v", np.Profile.PrependArgs)
	}
	// clear+args replaces
	if err := s.Edit("e1", EditPatch{ClearPrependArgs: true, PrependArgs: []string{"--z"}}); err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("e1")
	if len(np.Profile.PrependArgs) != 1 || np.Profile.PrependArgs[0] != "--z" {
		t.Fatalf("clear+args replace failed: %v", np.Profile.PrependArgs)
	}

	// overlay + enabled toggles
	if err := s.Edit("e1", EditPatch{Overlay: boolptr(false), Enabled: boolptr(false)}); err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("e1")
	if np.Profile.Overlay == nil || *np.Profile.Overlay {
		t.Fatal("Overlay edit failed")
	}
	if np.Profile.Enabled {
		t.Fatal("Enabled edit failed")
	}

	// unknown + bad seedFrom
	if err := s.Edit("nope", EditPatch{}); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("Edit(missing) = %v", err)
	}
	if err := s.Edit("e1", EditPatch{SeedFrom: strptr("/abs")}); err == nil || !strings.Contains(err.Error(), "invalid seedFrom") {
		t.Fatalf("Edit bad seedFrom = %v", err)
	}
}

func TestEditShadowHome(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "sh", Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true})

	custom := filepath.Join(t.TempDir(), "custom-home")
	if err := s.Edit("sh", EditPatch{ShadowHome: strptr(custom)}); err != nil {
		t.Fatal(err)
	}
	np, _ := s.Get("sh")
	if np.Profile.ShadowHome != custom {
		t.Fatalf("ShadowHome = %q", np.Profile.ShadowHome)
	}
	if st, err := os.Stat(custom); err != nil || !st.IsDir() {
		t.Fatalf("custom shadow dir must be created: %v", err)
	}
	// clear
	if err := s.Edit("sh", EditPatch{ClearShadowHome: true}); err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("sh")
	if np.Profile.ShadowHome != "" {
		t.Fatalf("ClearShadowHome failed: %q", np.Profile.ShadowHome)
	}
}

func TestEditShadowHomeCodexOverlay(t *testing.T) {
	paths := testPaths(t)
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "config.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(paths)
	mustCreate(t, s, "cx", Profile{Agent: "codex", Enabled: true, Router: true, HomePath: shared, Overlay: boolptr(true)}, CreateOptions{NoSeed: true})
	custom := filepath.Join(t.TempDir(), "cx-shadow")
	if err := s.Edit("cx", EditPatch{ShadowHome: strptr(custom)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(custom, "config.toml")); err != nil {
		t.Fatalf("overlay must link shared entry: %v", err)
	}
}

func TestClone(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "src", Profile{
		Agent: "codex", DisplayName: "Src Display", Enabled: true, Router: false,
		BinaryPath: "/bin/x", HomePath: "/shared", SeedFrom: "codex",
		Env: map[string]string{"K": "v"}, PrependArgs: []string{"--a"},
		Overlay: boolptr(false),
	}, CreateOptions{NoSeed: true})
	// plant live state in src shadow that must NOT be copied
	srcNP, _ := s.Get("src")
	if err := os.WriteFile(filepath.Join(srcNP.Profile.ShadowHome, "auth.json"), []byte(`{"live":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcNP.Profile.ShadowHome, "marker"), []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Clone("src", "dst", CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}
	dstNP, ok := s.Get("dst")
	if !ok {
		t.Fatal("clone missing")
	}
	if dstNP.Profile.DisplayName != "" {
		t.Fatalf("clone DisplayName must be empty, got %q", dstNP.Profile.DisplayName)
	}
	if dstNP.Profile.Agent != "codex" || !dstNP.Profile.Enabled || dstNP.Profile.Router {
		t.Fatalf("clone scalar copy wrong: %+v", dstNP.Profile)
	}
	if dstNP.Profile.BinaryPath != "/bin/x" || dstNP.Profile.HomePath != "/shared" || dstNP.Profile.SeedFrom != "codex" {
		t.Fatalf("clone paths copy wrong: %+v", dstNP.Profile)
	}
	if dstNP.Profile.Env["K"] != "v" || len(dstNP.Profile.PrependArgs) != 1 {
		t.Fatalf("clone env/args wrong: %+v", dstNP.Profile)
	}
	if dstNP.Profile.Overlay == nil || *dstNP.Profile.Overlay {
		t.Fatal("clone overlay must preserve false")
	}
	wantShadow := filepath.Join(paths.Home, ".harness", "profiles", "codex", "dst", "home")
	if dstNP.Profile.ShadowHome != wantShadow {
		t.Fatalf("clone shadow = %q, want %q", dstNP.Profile.ShadowHome, wantShadow)
	}
	if dstNP.Profile.ShadowHome == srcNP.Profile.ShadowHome {
		t.Fatal("clone must not reuse src shadow")
	}
	if _, err := os.Stat(filepath.Join(wantShadow, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("clone must not copy live auth.json")
	}
	if _, err := os.Stat(filepath.Join(wantShadow, "marker")); !os.IsNotExist(err) {
		t.Fatal("clone must not copy live shadow state")
	}
	// deep copy: mutating dst env must not affect src
	dstNP.Profile.Env["K"] = "changed"
	srcNP2, _ := s.Get("src")
	if srcNP2.Profile.Env["K"] != "v" {
		t.Fatal("clone Env must be a deep copy")
	}

	// errors
	if err := s.Clone("nope", "x2", CreateOptions{}); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("Clone(missing src) = %v", err)
	}
	if err := s.Clone("src", "dst", CreateOptions{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Clone(dup) = %v", err)
	}
	if err := s.Clone("src", "bad name", CreateOptions{}); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
		t.Fatalf("Clone(bad dst) = %v", err)
	}
}

func TestCloneSeedsAndOverlay(t *testing.T) {
	paths := testPaths(t)
	dots := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dots, "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dots, "codex", "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "skills-link"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(paths)
	mustCreate(t, s, "src", Profile{Agent: "codex", Enabled: true, Router: true, SeedFrom: "codex", HomePath: shared, Overlay: boolptr(true)}, CreateOptions{NoSeed: true})
	if err := s.Clone("src", "dst", CreateOptions{SeedRoot: dots}); err != nil {
		t.Fatal(err)
	}
	dst, _ := s.Get("dst")
	if _, err := os.Stat(filepath.Join(dst.Profile.ShadowHome, "seed.txt")); err != nil {
		t.Fatalf("clone must seed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst.Profile.ShadowHome, "skills-link")); err != nil {
		t.Fatalf("clone must materialize overlay: %v", err)
	}
}

func TestRename(t *testing.T) {
	t.Run("managed moves", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "old", Profile{Agent: "pi", Enabled: true, Router: true}, CreateOptions{NoSeed: true})
		oldNP, _ := s.Get("old")
		marker := filepath.Join(oldNP.Profile.ShadowHome, "marker")
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := s.Rename("old", "new"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Get("old"); ok {
			t.Fatal("old key must be gone")
		}
		np, ok := s.Get("new")
		if !ok {
			t.Fatal("new key missing")
		}
		want := filepath.Join(paths.Home, ".harness", "profiles", "pi", "new", "home")
		if np.Profile.ShadowHome != want {
			t.Fatalf("shadow = %q, want %q", np.Profile.ShadowHome, want)
		}
		if _, err := os.Stat(filepath.Join(want, "marker")); err != nil {
			t.Fatalf("marker must move: %v", err)
		}
		if _, err := os.Stat(oldNP.Profile.ShadowHome); !os.IsNotExist(err) {
			t.Fatalf("old shadow must be gone: %v", err)
		}
		// persists
		s2, _ := Open(paths)
		np2, ok := s2.Get("new")
		if !ok || np2.Profile.ShadowHome != want {
			t.Fatalf("rename must persist: %+v", np2)
		}
	})

	t.Run("external untouched", func(t *testing.T) {
		paths := testPaths(t)
		outside := filepath.Join(t.TempDir(), "outside")
		s, _ := Open(paths)
		mustCreate(t, s, "ext", Profile{Agent: "pi", Enabled: true, Router: true, ShadowHome: outside}, CreateOptions{NoSeed: true})
		if err := s.Rename("ext", "ext2"); err != nil {
			t.Fatal(err)
		}
		np, _ := s.Get("ext2")
		if np.Profile.ShadowHome != outside {
			t.Fatalf("external shadow must be untouched: %q", np.Profile.ShadowHome)
		}
		if st, err := os.Stat(outside); err != nil || !st.IsDir() {
			t.Fatalf("external dir must remain: %v", err)
		}
	})

	t.Run("errors and noop", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "a", Profile{Agent: "pi", Router: true}, CreateOptions{NoSeed: true})
		mustCreate(t, s, "b", Profile{Agent: "pi", Router: true}, CreateOptions{NoSeed: true})
		if err := s.Rename("a", "a"); err != nil {
			t.Fatalf("self rename must be no-op: %v", err)
		}
		if err := s.Rename("nope", "x"); err == nil || !strings.Contains(err.Error(), "unknown profile") {
			t.Fatalf("Rename(missing) = %v", err)
		}
		if err := s.Rename("a", "b"); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("Rename(dup) = %v", err)
		}
		if err := s.Rename("a", "bad name"); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Fatalf("Rename(bad) = %v", err)
		}
	})
}

func TestMaterializeForLaunch(t *testing.T) {
	t.Run("codex enabled", func(t *testing.T) {
		paths := testPaths(t)
		shared := filepath.Join(t.TempDir(), "shared")
		if err := os.MkdirAll(shared, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shared, "cfg"), []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
		shadow := filepath.Join(t.TempDir(), "shadow")
		r := Resolution{Name: "c", Source: SourceStore, Profile: Profile{Agent: "codex", ShadowHome: shadow, HomePath: shared, Overlay: boolptr(true)}}
		if err := MaterializeForLaunch(r, paths); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(shadow, "cfg")); err != nil {
			t.Fatalf("overlay missing: %v", err)
		}
		// idempotent
		if err := MaterializeForLaunch(r, paths); err != nil {
			t.Fatalf("second materialize: %v", err)
		}
	})

	t.Run("codex default shared home", func(t *testing.T) {
		paths := testPaths(t)
		shared := DefaultCodexSharedHome(paths)
		if err := os.MkdirAll(shared, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shared, "d"), []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
		shadow := filepath.Join(t.TempDir(), "shadow2")
		r := Resolution{Name: "c", Source: SourceStore, Profile: Profile{Agent: "codex", ShadowHome: shadow, Overlay: boolptr(true)}}
		if err := MaterializeForLaunch(r, paths); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(shadow, "d")); err != nil {
			t.Fatalf("default shared overlay missing: %v", err)
		}
	})

	t.Run("noops", func(t *testing.T) {
		paths := testPaths(t)
		// empty shadow
		if err := MaterializeForLaunch(Resolution{Profile: Profile{Agent: "codex"}}, paths); err != nil {
			t.Fatalf("empty shadow must be no-op: %v", err)
		}
		// overlay disabled
		shadow := filepath.Join(t.TempDir(), "noshadow")
		r := Resolution{Profile: Profile{Agent: "codex", ShadowHome: shadow, Overlay: boolptr(false)}}
		if err := MaterializeForLaunch(r, paths); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(shadow); !os.IsNotExist(err) {
			t.Fatal("disabled overlay must not create shadow dir")
		}
		// overlay unset defaults to enabled
		shadowDefault := filepath.Join(t.TempDir(), "shadow-default")
		rDefault := Resolution{Profile: Profile{Agent: "codex", ShadowHome: shadowDefault}}
		if err := MaterializeForLaunch(rDefault, paths); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(shadowDefault, "sessions")); err != nil {
			t.Fatalf("default (unset) overlay must share sessions: %v", err)
		}
	})
}

func TestListEntryAgent(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	mustCreate(t, s, "mine", Profile{Agent: "codex", Enabled: true, Router: true}, CreateOptions{NoSeed: true})

	entries, err := ListAll(paths)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ListEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if byName["mine"].Agent != "codex" {
		t.Fatalf("store agent = %q, want codex", byName["mine"].Agent)
	}
	// JSON carries agent
	raw, _ := json.Marshal(entries)
	if !strings.Contains(string(raw), `"agent"`) {
		t.Fatalf("list JSON must carry agent: %s", raw)
	}
	// plain format unchanged
	plain := FormatPlain([]ListEntry{
		{Name: "mine", DisplayName: "mine", Agent: "codex", Source: SourceStore},
		{Name: "mywork", DisplayName: "My Work", Agent: "antigravity", Source: SourceStore},
	})
	if plain != "mine\tcodex\nmywork\tantigravity\tMy Work\n" {
		t.Fatalf("FormatPlain = %q", plain)
	}
}

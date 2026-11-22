package spacelaunch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// testPaths isolates HOME/XDG so store, T3 settings and cache all land in
// temp dirs. T3 settings and native credentials are absent: only the own
// store contributes profiles.
func testPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, ".local", "share"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	return harnessenv.DefaultPaths()
}

func TestRefreshBuildsAgentsWithStoreProfiles(t *testing.T) {
	paths := testPaths(t)
	homeBin := filepath.Join(paths.Home, ".local", "bin")
	if err := os.MkdirAll(homeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// opencode resolves to a bare name (PATH lookup); codex resolves to
	// $HOME/.local/bin/codex.
	binDir := t.TempDir()
	fakeBin(t, binDir, "opencode")
	fakeBin(t, homeBin, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	store, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	prof := profiles.Profile{Agent: "codex", DisplayName: "Codex Router", Enabled: true}
	if err := store.Create("codex_router", prof, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}

	if err := Refresh(paths); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	raw, err := os.ReadFile(CachePath(paths))
	if err != nil {
		t.Fatal(err)
	}
	var file CacheFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("cache is not JSON: %v", err)
	}
	if file.Version != cacheVersion {
		t.Errorf("version = %d, want %d", file.Version, cacheVersion)
	}
	if file.GeneratedAt <= 0 {
		t.Errorf("missing generatedAt")
	}
	byName := map[string]CachedAgent{}
	for _, a := range file.Agents {
		byName[a.Name] = a
	}
	codex, ok := byName["codex"]
	if !ok {
		t.Fatalf("codex missing from cache agents")
	}
	if codex.BinaryState != "ok" {
		t.Errorf("codex binary_state = %q, want ok", codex.BinaryState)
	}
	if len(codex.Profiles) != 1 || codex.Profiles[0].Name != "codex_router" {
		t.Fatalf("codex profiles = %+v, want [codex_router]", codex.Profiles)
	}
	if codex.Profiles[0].Agent != "codex" || codex.Profiles[0].Source != "store" {
		t.Errorf("profile mapping = %+v", codex.Profiles[0])
	}
	if open, ok := byName["opencode"]; !ok || len(open.Profiles) != 0 {
		t.Errorf("opencode should have no profiles, got %+v", open)
	}
	// The file must never contain explicit nulls: some consumers only
	// tolerate missing fields.
	raw, err = os.ReadFile(CachePath(paths))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, a := range decoded["agents"].([]interface{}) {
		entry := a.(map[string]interface{})
		if entry["profiles"] == nil {
			t.Errorf("agent %s has null profiles; emit []", entry["name"])
		}
	}
	// Disabled store profiles never reach the cache.
	store2, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	off := profiles.Profile{Agent: "codex", Enabled: false}
	if err := store2.Create("codex_off", off, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(paths); err != nil {
		t.Fatal(err)
	}
	file, err = Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ProfilesFor(file, "codex")
	for _, p := range got {
		if p.Name == "codex_off" {
			t.Errorf("disabled profile leaked into cache")
		}
	}
}

func TestEnsureFreshRefreshesStaleCaches(t *testing.T) {
	paths := testPaths(t)
	if _, err := EnsureFresh(paths); err != nil {
		t.Fatalf("initial EnsureFresh: %v", err)
	}
	path := CachePath(paths)
	stale := time.Now().Add(-cacheTTL - time.Minute)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(`{"version":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Wrong version counts as stale too.
	file, err := EnsureFresh(paths)
	if err != nil {
		t.Fatalf("EnsureFresh on stale cache: %v", err)
	}
	if file.Version != cacheVersion {
		t.Errorf("stale cache was not rebuilt")
	}
	if string(before) == "" {
		t.Errorf("expected a previous cache body")
	}
}

func TestEnsureFreshFailsWithoutCache(t *testing.T) {
	testPaths(t)
	// Break profile listing by making the store file unparsable AND the
	// cache unwritable: point XDG_CONFIG_HOME at a file.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocker)
	t.Setenv("XDG_CONFIG_HOME", blocker)
	bad := harnessenv.DefaultPaths()
	if _, err := EnsureFresh(bad); err == nil {
		t.Errorf("expected an error when the cache cannot be built")
	}
}

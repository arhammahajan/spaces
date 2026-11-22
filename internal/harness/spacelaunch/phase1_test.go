package spacelaunch

import (
	"path/filepath"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

// TestResolveProfileStorePathsAware verifies the pane-side profile path
// resolves store profiles with the process paths and forwards caller args.
func TestResolveProfileStorePathsAware(t *testing.T) {
	paths := testPaths(t)
	headlessAccess(t)
	store, err := profiles.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create("codex_work", profiles.Profile{
		Agent:       "codex",
		DisplayName: "Codex Work",
		Enabled:     true,
		Router:      true,
	}, profiles.CreateOptions{NoSeed: true}); err != nil {
		t.Fatal(err)
	}
	resolution, resolved, err := resolveProfile("codex_work", []string{"--extra"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Source != profiles.SourceStore || resolution.Profile.Agent != "codex" {
		t.Fatalf("resolution wrong: %+v", resolution)
	}
	if want := filepath.Join(paths.Home, ".local", "bin", "codex"); resolved.Binary != want {
		t.Fatalf("Binary = %q, want paths-aware %q", resolved.Binary, want)
	}
	if len(resolved.Args) == 0 || resolved.Args[len(resolved.Args)-1] != "--extra" {
		t.Fatalf("caller args must flow through, args = %v", resolved.Args)
	}
}

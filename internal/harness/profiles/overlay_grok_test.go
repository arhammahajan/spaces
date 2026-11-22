package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func TestMaterializeForLaunchGrokDefaultSharesSessions(t *testing.T) {
	tmp := t.TempDir()
	paths := harnessenv.Paths{
		Home:       filepath.Join(tmp, "home"),
		ConfigHome: filepath.Join(tmp, "config"),
		DataHome:   filepath.Join(tmp, "data"),
		StateHome:  filepath.Join(tmp, "state"),
		CacheHome:  filepath.Join(tmp, "cache"),
	}
	sharedSessions := filepath.Join(paths.Home, ".grok", "sessions")
	if err := os.MkdirAll(sharedSessions, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeForLaunch(DefaultResolution("grok"), paths); err != nil {
		t.Fatalf("MaterializeForLaunch(grok default) = %v", err)
	}

	link := filepath.Join(harnessenv.HarnessDir(paths), "grok", "model-router", "sessions")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected grok router home sessions symlink at %s: %v", link, err)
	}
	if filepath.Clean(target) != filepath.Clean(sharedSessions) {
		t.Fatalf("sessions symlink target = %q, want %q", target, sharedSessions)
	}
}

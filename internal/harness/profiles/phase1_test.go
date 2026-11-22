package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func TestEnabledDefaultsTrueWhenAbsent(t *testing.T) {
	paths := testPaths(t)
	dir := filepath.Dir(StorePath(paths))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"profiles":{"plain":{"agent":"pi","displayName":"P","shadowHomePath":"/tmp/x","seedFrom":"","router":true,"env":{},"prependArgs":[]}}}`
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
	if !np.Profile.Enabled {
		t.Error("Enabled must default to true when the key is absent")
	}
	if !np.Profile.Router {
		t.Error("Router must still default to true")
	}

	// Explicit false is respected.
	doc = `{"version":1,"profiles":{"off":{"agent":"pi","displayName":"P","enabled":false,"router":true,"env":{},"prependArgs":[]}}}`
	if err := os.WriteFile(StorePath(paths), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	np, _ = s.Get("off")
	if np.Profile.Enabled {
		t.Error("explicit enabled:false must be respected")
	}
	if _, err := Resolve("off", paths); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled store profile must error, got %v", err)
	}
}

func TestProfileNewFieldsRoundTrip(t *testing.T) {
	paths := testPaths(t)
	s, _ := Open(paths)
	on := true
	sharedCodex := filepath.Join(t.TempDir(), "shared-codex")
	in := Profile{
		Agent: "codex", DisplayName: "C", Enabled: true, Router: true,
		BinaryPath: "/custom/codex", HomePath: sharedCodex, Overlay: &on,
	}
	mustCreate(t, s, "rt", in, CreateOptions{NoSeed: true})
	s2, err := Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	np, ok := s2.Get("rt")
	if !ok {
		t.Fatal("missing after round trip")
	}
	if np.Profile.BinaryPath != "/custom/codex" || np.Profile.HomePath != sharedCodex {
		t.Fatalf("new fields lost: %+v", np.Profile)
	}
	if np.Profile.Overlay == nil || !*np.Profile.Overlay {
		t.Fatalf("overlay flag lost: %+v", np.Profile.Overlay)
	}
	raw, _ := os.ReadFile(ProfileFilePath(paths, "codex", "rt"))
	for _, key := range []string{`"binaryPath"`, `"homePath"`, `"overlay"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("profile JSON missing %s", key)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["agent"] != "codex" || decoded["router"] != true {
		t.Errorf("core keys changed: %v", decoded)
	}
}

func mustResolveProfile(t *testing.T, paths harnessenv.Paths, name string) Resolution {
	t.Helper()
	r, err := Resolve(name, paths)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", name, err)
	}
	return r
}

func TestLaunchSpecShadowPropagation(t *testing.T) {
	t.Run("store shadow into agents.Resolve", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "w", Profile{Agent: "claude", DisplayName: "W", Enabled: true, Router: true},
			CreateOptions{NoSeed: true})
		r := mustResolveProfile(t, paths, "w")
		spec := LaunchSpecFromPaths(r, paths)
		if spec.ShadowHome == "" || spec.Agent != "claude" {
			t.Fatalf("store ShadowHome missing: %+v", spec)
		}
		resolved, err := agents.Resolve(spec, paths, harnessenv.RouterSettings{URL: "http://127.0.0.1:4000", APIKey: "k"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, kv := range resolved.Env {
			if kv == "CLAUDE_CONFIG_DIR="+spec.ShadowHome {
				found = true
			}
			if strings.HasPrefix(kv, "HOME=") {
				t.Fatalf("shadow must not set HOME: %q", kv)
			}
		}
		if !found {
			t.Fatalf("CLAUDE_CONFIG_DIR not propagated: %v", resolved.Env)
		}
	})
	t.Run("store tilde shadow into agents.Resolve", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "cx", Profile{Agent: "codex", DisplayName: "CX", Enabled: true,
			Router: true, ShadowHome: "~/.codex-sh"}, CreateOptions{NoSeed: true})
		spec := LaunchSpecFromPaths(mustResolveProfile(t, paths, "cx"), paths)
		resolved, err := agents.Resolve(spec, paths, harnessenv.RouterSettings{URL: "http://127.0.0.1:4000", APIKey: "k"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "CODEX_HOME=" + filepath.Join(paths.Home, ".codex-sh")
		found := false
		for _, kv := range resolved.Env {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("want %q in %v", want, resolved.Env)
		}
	})
	t.Run("store BinaryPath forwarded", func(t *testing.T) {
		paths := testPaths(t)
		s, _ := Open(paths)
		mustCreate(t, s, "b", Profile{Agent: "pi", DisplayName: "B", Enabled: true,
			Router: true, BinaryPath: "/custom/pi"}, CreateOptions{NoSeed: true})
		spec := LaunchSpecFromPaths(mustResolveProfile(t, paths, "b"), paths)
		if spec.Binary != "/custom/pi" {
			t.Fatalf("Binary = %q", spec.Binary)
		}
	})
}

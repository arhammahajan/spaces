package harnessenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathsFallbacks(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	p := DefaultPaths()
	if p.Home != "/home/testuser" {
		t.Fatalf("Home = %q", p.Home)
	}
	if p.ConfigHome != "/home/testuser/.config" {
		t.Fatalf("ConfigHome = %q", p.ConfigHome)
	}
	if p.DataHome != "/home/testuser/.local/share" {
		t.Fatalf("DataHome = %q", p.DataHome)
	}
	if p.StateHome != "/home/testuser/.local/state" {
		t.Fatalf("StateHome = %q", p.StateHome)
	}
}

func TestDefaultPathsHonorsAbsoluteXDG(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
	t.Setenv("XDG_DATA_HOME", "relative/is-ignored")
	p := DefaultPaths()
	if p.ConfigHome != "/xdg/cfg" {
		t.Fatalf("ConfigHome = %q", p.ConfigHome)
	}
	if p.DataHome != "/home/testuser/.local/share" {
		t.Fatalf("relative XDG_DATA_HOME must fall back, got %q", p.DataHome)
	}
}

func TestRouterEnvDefaults(t *testing.T) {
	t.Setenv("MODEL_ROUTER_URL", "")
	t.Setenv("MODEL_ROUTER_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := RouterEnv(DefaultPaths())
	if s.URL != "http://127.0.0.1:4000" || s.APIKey != "unused" {
		t.Fatalf("unexpected defaults: %+v", s)
	}
}

func TestRouterEnvFileAndOverrides(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := "export MODEL_ROUTER_URL=\"${MODEL_ROUTER_URL:-http://127.0.0.1:4100}\"\n" +
		"export MODEL_ROUTER_API_KEY=\"${MODEL_ROUTER_API_KEY:-filekey}\"\n"
	if err := os.WriteFile(filepath.Join(cfg, "ai", "model-router.env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("file fills defaults", func(t *testing.T) {
		t.Setenv("MODEL_ROUTER_URL", "")
		t.Setenv("MODEL_ROUTER_API_KEY", "")
		t.Setenv("XDG_CONFIG_HOME", cfg)
		s := RouterEnv(DefaultPaths())
		if s.URL != "http://127.0.0.1:4100" || s.APIKey != "filekey" {
			t.Fatalf("unexpected file values: %+v", s)
		}
	})
	t.Run("process env wins", func(t *testing.T) {
		t.Setenv("MODEL_ROUTER_URL", "http://router.example:4000/")
		t.Setenv("MODEL_ROUTER_API_KEY", "overridden")
		t.Setenv("XDG_CONFIG_HOME", cfg)
		s := RouterEnv(DefaultPaths())
		if s.URL != "http://router.example:4000" {
			t.Fatalf("trailing slash must be trimmed, got %q", s.URL)
		}
		if s.APIKey != "overridden" {
			t.Fatalf("APIKey = %q", s.APIKey)
		}
	})
}

func TestParseEnvFile(t *testing.T) {
	data := "# comment\n" +
		"export A=\"${B:-fallback}\" \n" +
		"B='literal $A'\n" +
		"C=bare-${A}\n" +
		"garbage line without equals\n" +
		"1BAD=x\n"
	got := ParseEnvFile(data, nil)
	want := map[string]string{
		"A": "fallback",
		"B": "literal $A",
		"C": "bare-fallback",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["1BAD"]; ok {
		t.Error("invalid name must be skipped")
	}
}

func TestParseEnvFileLaterLinesSeeEarlierValues(t *testing.T) {
	got := ParseEnvFile("X=first\nY=\"prefix-${X}\"\n", nil)
	if got["Y"] != "prefix-first" {
		t.Fatalf("Y = %q, want %q", got["Y"], "prefix-first")
	}
}

func TestNewRouterSessionShape(t *testing.T) {
	s := NewRouterSession()
	if !strings.HasPrefix(s, "router-session-") {
		t.Fatalf("session %q lacks prefix", s)
	}
	uuid := strings.TrimPrefix(s, "router-session-")
	parts := strings.Split(uuid, "-")
	if len(parts) != 5 {
		t.Fatalf("uuid parts = %v", parts)
	}
	if len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("uuid segment lengths wrong: %v", parts)
	}
	if parts[2][0] != '4' {
		t.Fatalf("uuid version nibble = %q, want 4", parts[2][0])
	}
	if NewRouterSession() == s {
		t.Fatal("sessions must be unique")
	}
}

func TestDotfilesRootOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_DOTFILES", "/nonexistent-dots")
	root, ok := DotfilesRoot(DefaultPaths())
	if root != "/nonexistent-dots" || ok {
		t.Fatalf("root=%q ok=%v", root, ok)
	}
	tmp := t.TempDir()
	t.Setenv("HARNESS_DOTFILES", tmp)
	root, ok = DotfilesRoot(DefaultPaths())
	if root != tmp || !ok {
		t.Fatalf("root=%q ok=%v", root, ok)
	}
	home := t.TempDir()
	dotsDir := filepath.Join(home, "custom-dots")
	if err := os.MkdirAll(dotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("HARNESS_DOTFILES", "${HOME}/custom-dots")
	root, ok = DotfilesRoot(DefaultPaths())
	if root != dotsDir || !ok {
		t.Fatalf("root=%q ok=%v, want %q true", root, ok, dotsDir)
	}
}

func TestExpandHome(t *testing.T) {
	home := "/Users/tester"
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"~", home},
		{"~/dev/dots", filepath.Join(home, "dev", "dots")},
		{"$HOME", home},
		{"$HOME/dev/dots", filepath.Join(home, "dev", "dots")},
		{"${HOME}", home},
		{"${HOME}/.local/bin/harness", filepath.Join(home, ".local", "bin", "harness")},
		{"${HOME}/a:${HOME}/b", home + "/a:" + home + "/b"},
		{"/opt/bin/tool", "/opt/bin/tool"},
		{"$HOME_OTHER/x", "$HOME_OTHER/x"},
	}
	for _, tc := range cases {
		if got := ExpandHome(tc.in, home); got != tc.want {
			t.Errorf("ExpandHome(%q, %q) = %q, want %q", tc.in, home, got, tc.want)
		}
	}
	if got := ExpandHome("${HOME}/dev", ""); got != "${HOME}/dev" {
		t.Errorf("ExpandHome with empty home = %q, want unchanged", got)
	}
}

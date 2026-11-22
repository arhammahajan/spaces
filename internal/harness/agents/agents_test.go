package agents

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// testPaths points HOME and all XDG dirs at temp dirs so no test touches
// the real home.
func testPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("HARNESS_DOTFILES", "")
	return harnessenv.DefaultPaths()
}

func testRouter() harnessenv.RouterSettings {
	// Trailing slash: Resolve must trim it like the old script's ${%/}.
	return harnessenv.RouterSettings{URL: "http://127.0.0.1:4000/", APIKey: "testkey"}
}

// stubRunner replaces CommandRunner for the test, restoring the default.
func stubRunner(t *testing.T, fn func(name string, args ...string) (string, error)) {
	t.Helper()
	old := CommandRunner
	CommandRunner = fn
	t.Cleanup(func() { CommandRunner = old })
}

// stubFetch replaces FetchModelIDs for the test, restoring the default.
func stubFetch(t *testing.T, fn func(modelsURL string) ([]string, error)) {
	t.Helper()
	old := FetchModelIDs
	FetchModelIDs = fn
	t.Cleanup(func() { FetchModelIDs = old })
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		m[k] = v
	}
	return m
}

func TestKnownSortedAndComplete(t *testing.T) {
	got := Known()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Known() not sorted: %v", got)
	}
	want := []string{
		"antigravity", "antigravity-native",
		"claude", "cline", "codex", "copilot", "deepseek", "fx",
		"gemini", "grok", "kilo", "kimi", "kimi-cli", "mcode",
		"muse", "opencode", "pi", "qwen", "vibe",
	}
	if len(got) != len(want) {
		t.Fatalf("Known() = %v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Known() = %v, want %v", got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"claude", "claude", true},
		{"codex", "codex", true},
		{"antigravity-native", "antigravity-native", true},
		{"agy", "antigravity", true},
		{"dsh", "deepseek", true},
		{"kimi-code", "kimi", true},
		{"nope", "", false},
		{"", "", false},
		{"profile", "", false},
		{"AGY", "", false},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Normalize(%q) = (%q, %v), want (%q, %v)",
				c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestInfosStates(t *testing.T) {
	paths := testPaths(t)
	// Fakes: PATH binary (claude), absolute binary (codex), fnm node for
	// cline, ACP server install.
	bindir := t.TempDir()
	writeExec(t, filepath.Join(bindir, "claude"))
	t.Setenv("PATH", bindir)
	writeExec(t, filepath.Join(paths.Home, ".local", "bin", "codex"))
	fnmdir := t.TempDir()
	writeExec(t, filepath.Join(fnmdir, "node"))
	writeExec(t, filepath.Join(fnmdir, "cline"))
	stubRunner(t, func(name string, args ...string) (string, error) {
		return filepath.Join(fnmdir, "node") + "\n", nil
	})

	infos := Infos(paths)
	if len(infos) != len(Known()) {
		t.Fatalf("Infos returned %d entries, want %d", len(infos), len(Known()))
	}
	byName := map[string]AgentInfo{}
	for _, info := range infos {
		byName[info.Name] = info
		if info.Note == "" {
			t.Errorf("agent %s has no note", info.Name)
		}
	}
	if byName["claude"].BinaryState != "ok" || byName["claude"].Binary != "claude" {
		t.Errorf("claude info = %+v", byName["claude"])
	}
	if byName["codex"].BinaryState != "ok" {
		t.Errorf("codex info = %+v", byName["codex"])
	}
	if byName["cline"].BinaryState != "ok" {
		t.Errorf("cline info = %+v (want fnm-resolved binary)", byName["cline"])
	}
	// Nothing installed for these: absolute paths must report missing.
	for _, name := range []string{"grok", "muse", "deepseek", "antigravity-native"} {
		if byName[name].BinaryState != "missing" {
			t.Errorf("%s info = %+v, want missing", name, byName[name])
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		name string
		r    Resolved
		want string
	}{
		{
			"bare tokens pass through",
			Resolved{Binary: "codex", Args: []string{"-c", `model="gpt-6-luna"`}},
			`codex -c 'model="gpt-6-luna"'`,
		},
		{
			"spaces force quotes",
			Resolved{Binary: "/bin/x", Args: []string{"hello world"}},
			`/bin/x 'hello world'`,
		},
		{
			"single quotes escaped",
			Resolved{Binary: "opencode", Args: []string{"it's"}},
			`opencode 'it'"'"'s'`,
		},
		{
			"empty string quoted",
			Resolved{Binary: "dsh", Args: []string{""}},
			`dsh ''`,
		},
		{
			"special chars quoted",
			Resolved{Binary: "node", Args: []string{"a;b", "$HOME", "a*b"}},
			`node 'a;b' '$HOME' 'a*b'`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Describe(c.r); got != c.want {
				t.Fatalf("Describe = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNeedsStandalone(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"-x"}, true},
		{[]string{"--foo", "bar"}, true},
		{[]string{"mini"}, true},
		{[]string{"mini", "extra"}, true},
		{[]string{""}, true},
		{[]string{"run"}, false},
		{[]string{"mini", "--standalone"}, false},
		{[]string{"-x", "--server"}, false},
		{[]string{"--server=1234"}, false},
		{[]string{"--standalone"}, false},
	}
	for _, c := range cases {
		if got := needsStandalone(c.args); got != c.want {
			t.Errorf("needsStandalone(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

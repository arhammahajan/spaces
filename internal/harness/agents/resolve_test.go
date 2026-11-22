package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func mustResolve(t *testing.T, spec harnessenv.LaunchSpec, paths harnessenv.Paths, args []string) Resolved {
	t.Helper()
	r, err := Resolve(spec, paths, testRouter(), args)
	if err != nil {
		t.Fatalf("Resolve(%+v, %v) error: %v", spec, args, err)
	}
	if len(r.UnsetEnv) == 0 || r.UnsetEnv[0] != "HARNESS_AGENT" {
		t.Fatalf("UnsetEnv = %v, want HARNESS_AGENT first", r.UnsetEnv)
	}
	allowed := map[string]bool{
		"HARNESS_AGENT":                  true,
		"GOOGLE_API_KEY":                 true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
		"GOOGLE_CLOUD_PROJECT":           true,
		"GEMINI_API_KEY":                 true,
		"GOOGLE_GEMINI_BASE_URL":         true,
		"ANTIGRAVITY_APP_DATA_DIR":       true,
		"ANTIGRAVITY_CONFIG_REL_DIR":     true,
	}
	seen := map[string]bool{}
	for _, name := range r.UnsetEnv {
		if !allowed[name] {
			t.Fatalf("UnsetEnv = %v, unexpected entry %q", r.UnsetEnv, name)
		}
		if seen[name] {
			t.Fatalf("UnsetEnv = %v, duplicate entry %q", r.UnsetEnv, name)
		}
		seen[name] = true
	}
	return r
}

func TestResolveUsageErrors(t *testing.T) {
	paths := testPaths(t)
	if _, err := Resolve(harnessenv.LaunchSpec{}, paths, testRouter(), nil); err == nil {
		t.Error("empty agent: want error")
	}
	if _, err := Resolve(harnessenv.LaunchSpec{Agent: "nope"}, paths, testRouter(), nil); err == nil {
		t.Error("unknown agent: want error")
	} else if !strings.Contains(err.Error(), "unknown agent: nope") {
		t.Errorf("unexpected error: %v", err)
	}
	// Aliases resolve before execution.
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "agy"}, paths, nil)
	if !strings.HasSuffix(r.Binary, "/agy") {
		t.Errorf("agy binary = %q", r.Binary)
	}
}

func TestResolveClaude(t *testing.T) {
	paths := testPaths(t)
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "claude"}, paths, []string{"--foo"})
	if r.Binary != "claude" {
		t.Errorf("binary = %q", r.Binary)
	}
	m := envMap(t, r.Env)
	if m["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:4000" || m["ANTHROPIC_AUTH_TOKEN"] != "testkey" {
		t.Errorf("env = %v", m)
	}
	if len(r.Args) != 1 || r.Args[0] != "--foo" {
		t.Errorf("args = %v", r.Args)
	}
	if len(r.PathPrepend) != 0 {
		t.Errorf("bare launch PathPrepend = %v", r.PathPrepend)
	}
}

func TestResolveCodex(t *testing.T) {
	paths := testPaths(t)
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "codex"}, paths, []string{"run"})
	wantBin := filepath.Join(paths.Home, ".local", "bin", "codex")
	if r.Binary != wantBin {
		t.Fatalf("binary = %q, want %q", r.Binary, wantBin)
	}
	want := []string{
		"-c", `model_provider="model_router"`,
		"-c", `model="gpt-6-luna"`,
		"-c", `service_tier="default"`,
		"-c", `model_providers.model_router.name="Model Router"`,
		"-c", `model_providers.model_router.base_url="http://127.0.0.1:4000/v1"`,
		"-c", `model_providers.model_router.env_key="MODEL_ROUTER_API_KEY"`,
		"-c", `model_providers.model_router.wire_api="responses"`,
		"-c", `model_providers.model_router.requires_openai_auth=false`,
		"-c", `model_providers.model_router.supports_websockets=false`,
		"run",
	}
	if strings.Join(r.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q, want %q", r.Args, want)
	}
	if len(r.Env) != 0 {
		t.Errorf("codex env = %v, want empty (auth rides on env_key)", r.Env)
	}
}

func TestResolveGrok(t *testing.T) {
	paths := testPaths(t)
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "grok"}, paths, nil)
	if r.Binary != filepath.Join(paths.Home, ".local", "bin", "grok") {
		t.Errorf("binary = %q", r.Binary)
	}
	m := envMap(t, r.Env)
	if m["GROK_HOME"] != filepath.Join(paths.Home, ".harness", "grok", "model-router") {
		t.Errorf("GROK_HOME = %q", m["GROK_HOME"])
	}
	if m["GROK_MODELS_BASE_URL"] != "http://127.0.0.1:4000/v1" {
		t.Errorf("GROK_MODELS_BASE_URL = %q", m["GROK_MODELS_BASE_URL"])
	}
	if m["GROK_MODELS_LIST_URL"] != "http://127.0.0.1:4000/v1/models" {
		t.Errorf("GROK_MODELS_LIST_URL = %q", m["GROK_MODELS_LIST_URL"])
	}
	if m["XAI_API_KEY"] != "testkey" {
		t.Errorf("XAI_API_KEY = %q", m["XAI_API_KEY"])
	}
	if !strings.HasPrefix(m["GROK_MODEL_ROUTER_SESSION"], "router-session-") {
		t.Errorf("session = %q", m["GROK_MODEL_ROUTER_SESSION"])
	}
}

func TestResolveCline(t *testing.T) {
	paths := testPaths(t)
	stubRunner(t, func(name string, args ...string) (string, error) {
		if name != "fnm" || strings.Join(args, " ") != "exec --using=default which node" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return "/fnm/aliases/default/bin/node\n", nil
	})
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "cline"}, paths, []string{"extra"})
	if r.Binary != filepath.Join("/fnm", "aliases", "default", "bin", "cline") {
		t.Errorf("binary = %q", r.Binary)
	}
	if len(r.Args) != 3 || r.Args[0] != "--key" || r.Args[2] != "extra" {
		t.Errorf("args = %v", r.Args)
	}
	if !strings.HasPrefix(r.Args[1], "router-session-") {
		t.Errorf("session = %q", r.Args[1])
	}
}

func TestResolveClineRunnerError(t *testing.T) {
	paths := testPaths(t)
	stubRunner(t, func(string, ...string) (string, error) {
		return "", errors.New("fnm not found")
	})
	if _, err := Resolve(harnessenv.LaunchSpec{Agent: "cline"}, paths, testRouter(), nil); err == nil {
		t.Error("want error when fnm fails")
	}
}

func TestResolveOpencode(t *testing.T) {
	paths := testPaths(t)
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"empty", nil, []string{"--standalone"}},
		{"flag first", []string{"--foo"}, []string{"--standalone", "--foo"}},
		{"mini", []string{"mini"}, []string{"--standalone", "mini"}},
		{"explicit standalone", []string{"--standalone"}, []string{"--standalone"}},
		{"server", []string{"--server"}, []string{"--server"}},
		{"server value", []string{"--server=9999"}, []string{"--server=9999"}},
		{"subcommand", []string{"run", "--foo"}, []string{"run", "--foo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mustResolve(t, harnessenv.LaunchSpec{Agent: "opencode"}, paths, c.args)
			if r.Binary != "opencode" {
				t.Fatalf("binary = %q", r.Binary)
			}
			if strings.Join(r.Args, "\x00") != strings.Join(c.want, "\x00") {
				t.Fatalf("args = %v, want %v", r.Args, c.want)
			}
		})
	}
}

func TestResolvePlainExec(t *testing.T) {
	paths := testPaths(t)
	for _, name := range []string{"pi", "qwen", "kimi", "mcode"} {
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: name}, paths, []string{"a"})
		if r.Binary != name {
			t.Errorf("%s binary = %q", name, r.Binary)
		}
		if len(r.Env) != 0 {
			t.Errorf("%s env = %v, want empty", name, r.Env)
		}
		if strings.Join(r.Args, ",") != "a" {
			t.Errorf("%s args = %v", name, r.Args)
		}
	}
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "kilo"}, paths, []string{"a"})
	if want := filepath.Join(paths.Home, ".kilo", "bin", "kilo"); r.Binary != want {
		t.Errorf("kilo binary = %q, want %q", r.Binary, want)
	}
	if len(r.Env) != 0 {
		t.Errorf("kilo env = %v, want empty", r.Env)
	}
}

func TestResolveCopilot(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("COPILOT_MODEL", "")
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "copilot"}, paths, nil)
	m := envMap(t, r.Env)
	if m["COPILOT_MODEL"] != "gpt-6-luna" {
		t.Errorf("default COPILOT_MODEL = %q", m["COPILOT_MODEL"])
	}
	if m["COPILOT_PROVIDER_BASE_URL"] != "http://127.0.0.1:4000/v1" ||
		m["COPILOT_PROVIDER_API_KEY"] != "testkey" ||
		m["COPILOT_PROVIDER_TYPE"] != "openai" ||
		m["COPILOT_PROVIDER_WIRE_API"] != "responses" ||
		m["COPILOT_PROVIDER_TRANSPORT"] != "http" {
		t.Errorf("provider env = %v", m)
	}

	t.Setenv("COPILOT_MODEL", "gpt-5")
	r = mustResolve(t, harnessenv.LaunchSpec{Agent: "copilot"}, paths, nil)
	if envMap(t, r.Env)["COPILOT_MODEL"] != "gpt-5" {
		t.Errorf("existing COPILOT_MODEL not preserved: %v", r.Env)
	}
}

func TestResolveKimiCliMuseFx(t *testing.T) {
	paths := testPaths(t)
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "kimi-cli"}, paths, nil)
	m := envMap(t, r.Env)
	if m["OPENAI_BASE_URL"] != "http://127.0.0.1:4000/v1" {
		t.Errorf("OPENAI_BASE_URL = %q", m["OPENAI_BASE_URL"])
	}
	if !strings.HasPrefix(m["OPENAI_API_KEY"], "router-session-") {
		t.Errorf("OPENAI_API_KEY = %q", m["OPENAI_API_KEY"])
	}
	if want := filepath.Join(paths.Home, ".local", "share", "kimi-cli", "bin", "kimi-cli"); r.Binary != want {
		t.Errorf("kimi-cli binary = %q", r.Binary)
	}

	r = mustResolve(t, harnessenv.LaunchSpec{Agent: "muse"}, paths, nil)
	if envMap(t, r.Env)["META_API_KEY"] != "testkey" {
		t.Errorf("muse env = %v", r.Env)
	}

	r = mustResolve(t, harnessenv.LaunchSpec{Agent: "fx"}, paths, nil)
	m = envMap(t, r.Env)
	if !strings.HasPrefix(m["FX_MODEL_ROUTER_SESSION"], "router-session-") {
		t.Errorf("fx env = %v", r.Env)
	}
	if want := filepath.Join(paths.Home, ".local", "libexec", "fx", "fx-router"); r.Binary != want {
		t.Errorf("fx binary = %q", r.Binary)
	}
}

func TestResolveDeepseek(t *testing.T) {
	t.Run("default web arg", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("PATH", t.TempDir()) // no node on PATH
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "deepseek"}, paths, nil)
		if strings.Join(r.Args, ",") != "web" {
			t.Fatalf("args = %v, want [web]", r.Args)
		}
		want := filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin")
		if len(r.PathPrepend) != 1 || r.PathPrepend[0] != want {
			t.Fatalf("PathPrepend = %v, want [%s]", r.PathPrepend, want)
		}
	})
	t.Run("caller args kept", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("PATH", t.TempDir())
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "deepseek"}, paths, []string{"chat"})
		if strings.Join(r.Args, ",") != "chat" {
			t.Fatalf("args = %v", r.Args)
		}
	})
	t.Run("node present means no prepend", func(t *testing.T) {
		paths := testPaths(t)
		bindir := t.TempDir()
		writeExec(t, filepath.Join(bindir, "node"))
		t.Setenv("PATH", bindir)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "deepseek"}, paths, nil)
		if len(r.PathPrepend) != 0 {
			t.Fatalf("PathPrepend = %v, want empty", r.PathPrepend)
		}
	})
}

func TestResolveVibe(t *testing.T) {
	paths := testPaths(t)
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "vibe"}, paths, nil)
	want := filepath.Join(paths.Home, ".local", "share", "uv", "tools", "mistral-vibe", "bin", "vibe")
	if r.Binary != want {
		t.Errorf("vibe binary = %q", r.Binary)
	}
	if !strings.HasPrefix(envMap(t, r.Env)["VIBE_ROUTER_SESSION"], "router-session-") {
		t.Errorf("vibe env = %v", r.Env)
	}
}

func TestResolveGemini(t *testing.T) {
	setup := func(t *testing.T) (harnessenv.Paths, string) {
		paths := testPaths(t)
		repo := filepath.Join(paths.Home, "dev", "gemini-cli")
		entry := filepath.Join(repo, "packages", "cli", "dist", "index.js")
		if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry, []byte("console.log(1)"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GEMINI_CLI_REPO", "")
		return paths, entry
	}
	t.Run("missing dist errors", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("GEMINI_CLI_REPO", "")
		_, err := Resolve(harnessenv.LaunchSpec{Agent: "gemini"}, paths, testRouter(), nil)
		if err == nil {
			t.Fatal("want error when dist entry is missing")
		}
		want := "build Gemini CLI in " + filepath.Join(paths.Home, "dev", "gemini-cli") + " first"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want substring %q", err, want)
		}
	})
	t.Run("node entry with env", func(t *testing.T) {
		paths, entry := setup(t)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "gemini"}, paths, []string{"--x"})
		if r.Binary != "node" {
			t.Fatalf("binary = %q", r.Binary)
		}
		if len(r.Args) != 2 || r.Args[0] != entry || r.Args[1] != "--x" {
			t.Fatalf("args = %v", r.Args)
		}
		m := envMap(t, r.Env)
		if m["GOOGLE_GEMINI_BASE_URL"] != "http://127.0.0.1:4000" || m["GEMINI_MODEL_ROUTER"] != "1" {
			t.Errorf("env = %v", m)
		}
		if !strings.HasPrefix(m["GEMINI_API_KEY"], "router-session-") {
			t.Errorf("GEMINI_API_KEY = %q", m["GEMINI_API_KEY"])
		}
		if len(r.PathPrepend) != 0 {
			t.Errorf("PathPrepend = %v without fnm node", r.PathPrepend)
		}
	})
	t.Run("fnm node prepended and repo overridden", func(t *testing.T) {
		paths, _ := setup(t)
		fnmBin := filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin")
		writeExec(t, filepath.Join(fnmBin, "node"))
		alt := t.TempDir()
		altEntry := filepath.Join(alt, "packages", "cli", "dist", "index.js")
		if err := os.MkdirAll(filepath.Dir(altEntry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(altEntry, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GEMINI_CLI_REPO", alt)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "gemini"}, paths, nil)
		if len(r.PathPrepend) != 1 || r.PathPrepend[0] != fnmBin {
			t.Fatalf("PathPrepend = %v", r.PathPrepend)
		}
		if r.Args[0] != altEntry {
			t.Fatalf("args = %v", r.Args)
		}
	})
}

func TestResolveAntigravity(t *testing.T) {
	writeSettings := func(t *testing.T, home, body string) {
		t.Helper()
		dir := filepath.Join(home, ".gemini", "antigravity-cli")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("default from settings", func(t *testing.T) {
		paths := testPaths(t)
		writeSettings(t, paths.Home, `{"model":"gemini-3-pro"}`)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, paths, []string{"run"})
		if strings.Join(r.Args, " ") != `--model gemini-3-pro --settings {"modelProvider":"gemini"} run` {
			t.Fatalf("args = %v", r.Args)
		}
		m := envMap(t, r.Env)
		if m["GOOGLE_GEMINI_BASE_URL"] != "http://127.0.0.1:4000" {
			t.Errorf("env = %v", m)
		}
		if !strings.HasPrefix(m["GEMINI_API_KEY"], "router-session-") {
			t.Errorf("env = %v", m)
		}
	})
	t.Run("unreadable settings fall back", func(t *testing.T) {
		paths := testPaths(t) // no settings file
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, paths, nil)
		if strings.Join(r.Args, " ") != `--model gemini-4-argon --settings {"modelProvider":"gemini"}` {
			t.Fatalf("args = %v", r.Args)
		}
	})
	t.Run("broken settings fall back", func(t *testing.T) {
		paths := testPaths(t)
		writeSettings(t, paths.Home, `not json`)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, paths, nil)
		if strings.Join(r.Args, " ") != `--model gemini-4-argon --settings {"modelProvider":"gemini"}` {
			t.Fatalf("args = %v", r.Args)
		}
	})
	t.Run("explicit model wins", func(t *testing.T) {
		paths := testPaths(t)
		for _, args := range [][]string{{"--model", "x"}, {"--model=x"}, {"run", "--model=x"}} {
			r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, paths, args)
			want := append([]string{"--settings", `{"modelProvider":"gemini"}`}, args...)
			if strings.Join(r.Args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("args = %v, want %v", r.Args, want)
			}
		}
	})
	t.Run("explicit settings win", func(t *testing.T) {
		args := []string{"--model=x", "--settings", "/configured.json"}
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, testPaths(t), args)
		if strings.Join(r.Args, "\x00") != strings.Join(args, "\x00") {
			t.Fatalf("args = %v, want %v", r.Args, args)
		}
	})
	t.Run("no router skips default model and router env", func(t *testing.T) {
		paths := testPaths(t)
		writeSettings(t, paths.Home, `{"model":"gemini-3-pro"}`)
		r, err := Resolve(harnessenv.LaunchSpec{Agent: "antigravity", NoRouter: true}, paths, testRouter(), []string{"run"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(r.Args, " ") != "run" {
			t.Fatalf("args = %v, want [run]", r.Args)
		}
		m := envMap(t, r.Env)
		if _, ok := m["GOOGLE_GEMINI_BASE_URL"]; ok {
			t.Errorf("NoRouter must not set GOOGLE_GEMINI_BASE_URL: %v", m)
		}
	})
}

func TestResolveBinaryOverrideRescuesDefault(t *testing.T) {
	paths := testPaths(t)
	stubRunner(t, func(string, ...string) (string, error) {
		return "", errors.New("fnm not found")
	})
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "cline", Binary: "/custom/cline"}, paths, nil)
	if r.Binary != "/custom/cline" {
		t.Fatalf("binary = %q", r.Binary)
	}
}

func TestResolveSpecLayering(t *testing.T) {
	paths := testPaths(t)
	spec := harnessenv.LaunchSpec{
		Agent:       "claude",
		Binary:      "/custom/claude",
		PrependArgs: []string{"--pre"},
		Env:         map[string]string{"ANTHROPIC_BASE_URL": "http://custom/", "EXTRA": "1"},
	}
	r := mustResolve(t, spec, paths, []string{"--post"})
	if r.Binary != "/custom/claude" {
		t.Errorf("binary = %q", r.Binary)
	}
	if strings.Join(r.Args, ",") != "--pre,--post" {
		t.Errorf("args = %v", r.Args)
	}
	m := envMap(t, r.Env)
	if m["ANTHROPIC_BASE_URL"] != "http://custom/" || m["EXTRA"] != "1" || m["ANTHROPIC_AUTH_TOKEN"] != "testkey" {
		t.Errorf("env = %v", m)
	}
}

func TestResolveProfileAugmentation(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("PATH", t.TempDir()) // deepseek without node exercises the dedupe path
	bare, err := Resolve(harnessenv.LaunchSpec{Agent: "deepseek"}, paths, testRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.PathPrepend) != 1 {
		t.Fatalf("bare PathPrepend = %v", bare.PathPrepend)
	}
	prof, err := ResolveProfile(harnessenv.LaunchSpec{Agent: "deepseek"}, paths, testRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(paths.DataHome, "fnm", "aliases", "default", "bin"),
		filepath.Join(paths.DataHome, "fnm"),
		filepath.Join(paths.DataHome, "deepseek-harness", "bin"),
		filepath.Join(paths.DataHome, "pnpm", "bin"),
	}
	if strings.Join(prof.PathPrepend, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("profile PathPrepend = %v, want %v", prof.PathPrepend, want)
	}
	// Bare claude stays clean while the profile gains all four entries.
	bare, err = Resolve(harnessenv.LaunchSpec{Agent: "claude"}, paths, testRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.PathPrepend) != 0 {
		t.Fatalf("bare claude PathPrepend = %v", bare.PathPrepend)
	}
	prof, err = ResolveProfile(harnessenv.LaunchSpec{Agent: "claude"}, paths, testRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prof.PathPrepend) != 4 {
		t.Fatalf("profile claude PathPrepend = %v", prof.PathPrepend)
	}
}

func TestBuildChildEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "KEEP=1", "DROP=old", "OVERRIDE=old"}
	r := Resolved{
		Env:         []string{"OVERRIDE=new", "ADDED=yes"},
		UnsetEnv:    []string{"DROP"},
		PathPrepend: []string{"/opt/bin", "/local/bin"},
	}
	got := envMap(t, BuildChildEnv(base, r))
	if got["KEEP"] != "1" || got["OVERRIDE"] != "new" || got["ADDED"] != "yes" {
		t.Fatalf("unexpected env: %v", got)
	}
	if _, ok := got["DROP"]; ok {
		t.Fatalf("DROP should be unset: %v", got)
	}
	if got["PATH"] != "/opt/bin:/local/bin:/usr/bin" {
		t.Fatalf("PATH = %q, want /opt/bin:/local/bin:/usr/bin", got["PATH"])
	}
}

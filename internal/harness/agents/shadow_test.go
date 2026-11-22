package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func TestShadowVarMapping(t *testing.T) {
	for agent, want := range map[string]string{
		"codex":              "CODEX_HOME",
		"claude":             "CLAUDE_CONFIG_DIR",
		"grok":               "GROK_HOME",
		"antigravity":        "ANTIGRAVITY_GEMINI_DIR",
		"gemini":             "GEMINI_CLI_HOME",
		"copilot":            "COPILOT_HOME",
		"deepseek":           "DSH_HOME",
		"cline":              "CLINE_DATA_DIR",
		"opencode":           "XDG_DATA_HOME",
		"kilo":               "XDG_DATA_HOME",
		"pi":                 "PI_CODING_AGENT_DIR",
		"qwen":               "QWEN_HOME",
		"kimi":               "KIMI_CODE_HOME",
		"kimi-cli":           "KIMI_SHARE_DIR",
		"mcode":              "MINIMAX_DATA_DIR",
		"muse":               "MUSE_AUTH_PATH",
		"fx":                 "FX_HOME",
		"vibe":               "VIBE_HOME",
		"antigravity-native": "",
		"bogus":              "",
	} {
		got, ok := ShadowVar(agent)
		if want == "" {
			if ok {
				t.Errorf("ShadowVar(%q) = %q, want no mapping", agent, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ShadowVar(%q) = %q, %v; want %q", agent, got, ok, want)
		}
	}
	// Verify every known agent except antigravity-native has a ShadowVar mapping.
	for _, agent := range Known() {
		if agent == "antigravity-native" {
			continue
		}
		if _, ok := ShadowVar(agent); !ok {
			t.Errorf("known agent %q is missing a ShadowVar mapping", agent)
		}
	}
	// Muse maps ShadowEnv to <shadowHome>/auth.json.
	k, v, ok := ShadowEnv("muse", "/sh/muse")
	if !ok || k != "MUSE_AUTH_PATH" || v != "/sh/muse/auth.json" {
		t.Errorf("ShadowEnv(muse) = (%q, %q, %v), want (MUSE_AUTH_PATH, /sh/muse/auth.json, true)", k, v, ok)
	}
}

func unsetSet(r Resolved) map[string]bool {
	out := map[string]bool{}
	for _, name := range r.UnsetEnv {
		out[name] = true
	}
	return out
}

func TestShadowHomeIsolation(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "codex-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "codex", ShadowHome: shadow}, paths, nil)
		if envMap(t, r.Env)["CODEX_HOME"] != shadow {
			t.Fatalf("CODEX_HOME = %v, want shadow", r.Env)
		}
	})
	t.Run("claude keeps HOME intact", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "claude-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "claude", ShadowHome: shadow}, paths, nil)
		m := envMap(t, r.Env)
		if m["CLAUDE_CONFIG_DIR"] != shadow {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q, want shadow", m["CLAUDE_CONFIG_DIR"])
		}
		for _, kv := range r.Env {
			if strings.HasPrefix(kv, "HOME=") {
				t.Fatalf("claude shadow must never override HOME: %q", kv)
			}
		}
	})
	t.Run("grok overrides default", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "grok-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "grok", ShadowHome: shadow}, paths, nil)
		if envMap(t, r.Env)["GROK_HOME"] != shadow {
			t.Fatalf("GROK_HOME = %v, want shadow", r.Env)
		}
	})
	t.Run("antigravity isolates HOME, gemini dir, dbus, credential scope, and wires BASH_ENV restore", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
		t.Setenv("BASH_ENV", "")
		shadow := filepath.Join(t.TempDir(), "agy-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity", ShadowHome: shadow}, paths, nil)
		m := envMap(t, r.Env)
		if m["HOME"] != shadow {
			t.Fatalf("HOME = %q, want %q", m["HOME"], shadow)
		}
		if m["XDG_CONFIG_HOME"] != filepath.Join(shadow, ".config") {
			t.Fatalf("XDG_CONFIG_HOME = %q, want %q", m["XDG_CONFIG_HOME"], filepath.Join(shadow, ".config"))
		}
		if m["ANTIGRAVITY_GEMINI_DIR"] != filepath.Join(shadow, ".gemini") {
			t.Fatalf("ANTIGRAVITY_GEMINI_DIR = %q, want %q", m["ANTIGRAVITY_GEMINI_DIR"], filepath.Join(shadow, ".gemini"))
		}
		if m["DBUS_SESSION_BUS_ADDRESS"] != "disabled:" {
			t.Fatalf("DBUS_SESSION_BUS_ADDRESS = %q, want disabled:", m["DBUS_SESSION_BUS_ADDRESS"])
		}
		if !strings.HasPrefix(m["ANTIGRAVITY_CREDENTIAL_SCOPE"], "harness-profile-") {
			t.Fatalf("ANTIGRAVITY_CREDENTIAL_SCOPE = %q, want harness-profile-*", m["ANTIGRAVITY_CREDENTIAL_SCOPE"])
		}
		if m["HARNESS_REAL_HOME"] != paths.Home || m["HARNESS_REAL_XDG_CONFIG_HOME"] != paths.ConfigHome {
			t.Fatalf("HARNESS_REAL_* not populated: %v", m)
		}
		if m["HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS"] != "unix:path=/run/user/1000/bus" {
			t.Fatalf("HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS = %q", m["HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS"])
		}
		if m["BASH_ENV"] != RestoreEnvScriptPath(paths) {
			t.Fatalf("BASH_ENV = %q, want %q", m["BASH_ENV"], RestoreEnvScriptPath(paths))
		}
	})
	t.Run("unmapped agent ignores shadow", func(t *testing.T) {
		paths := testPaths(t)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity-native", Binary: "/bin/native.sh", ShadowHome: "/sh/native"}, paths, nil)
		for _, kv := range r.Env {
			if strings.Contains(kv, "/sh/native") {
				t.Fatalf("unmapped agent must ignore ShadowHome: %q", kv)
			}
		}
	})
	t.Run("explicit env wins over shadow", func(t *testing.T) {
		paths := testPaths(t)
		r := mustResolve(t, harnessenv.LaunchSpec{
			Agent:      "codex",
			ShadowHome: "/sh/codex",
			Env:        map[string]string{"CODEX_HOME": "/explicit"},
		}, paths, nil)
		if envMap(t, r.Env)["CODEX_HOME"] != "/explicit" {
			t.Fatalf("explicit Env must win, got %v", r.Env)
		}
	})
}

func TestAntigravityAmbientScrub(t *testing.T) {
	scrub := []string{"GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GEMINI_API_KEY"}
	t.Run("native scrubs all four", func(t *testing.T) {
		paths := testPaths(t)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity-native", Binary: "/s/native.sh"}, paths, nil)
		unset := unsetSet(r)
		for _, k := range scrub {
			if !unset[k] {
				t.Errorf("UnsetEnv %v must scrub %s", r.UnsetEnv, k)
			}
		}
	})
	t.Run("router GEMINI_API_KEY preserved", func(t *testing.T) {
		paths := testPaths(t)
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity"}, paths, nil)
		unset := unsetSet(r)
		if unset["GEMINI_API_KEY"] {
			t.Errorf("launch-set GEMINI_API_KEY must not be scrubbed: %v", r.UnsetEnv)
		}
		for _, k := range scrub[:3] {
			if !unset[k] {
				t.Errorf("UnsetEnv %v must scrub %s", r.UnsetEnv, k)
			}
		}
	})
	t.Run("spec env overrides survive scrub", func(t *testing.T) {
		paths := testPaths(t)
		r := mustResolve(t, harnessenv.LaunchSpec{
			Agent:  "antigravity-native",
			Binary: "/s/native.sh",
			Env: map[string]string{
				"GOOGLE_API_KEY":       "user-key",
				"GEMINI_API_KEY":       "user-gemini",
				"GOOGLE_CLOUD_PROJECT": "proj",
			},
		}, paths, nil)
		unset := unsetSet(r)
		m := envMap(t, r.Env)
		for k, want := range map[string]string{
			"GOOGLE_API_KEY": "user-key", "GEMINI_API_KEY": "user-gemini", "GOOGLE_CLOUD_PROJECT": "proj",
		} {
			if m[k] != want {
				t.Errorf("Env[%s] = %q, want %q", k, m[k], want)
			}
			if unset[k] {
				t.Errorf("spec-provided %s must not be in UnsetEnv", k)
			}
		}
		if !unset["GOOGLE_APPLICATION_CREDENTIALS"] {
			t.Errorf("unprovided GOOGLE_APPLICATION_CREDENTIALS must still be scrubbed: %v", r.UnsetEnv)
		}
	})
	t.Run("non-antigravity untouched", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("GOOGLE_API_KEY", "ambient")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "codex"}, paths, nil)
		if unsetSet(r)["GOOGLE_API_KEY"] {
			t.Errorf("codex must not scrub ambient keys: %v", r.UnsetEnv)
		}
	})
}

func TestAntigravityModelReadsRealHome(t *testing.T) {
	paths := testPaths(t)
	// Real-home settings declare model-real.
	dir := filepath.Join(paths.Home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"model-real"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// An isolated GEMINI_HOME with a decoy model must not affect lookup.
	shadow := t.TempDir()
	sdir := filepath.Join(shadow, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "settings.json"), []byte(`{"model":"model-decoy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity", ShadowHome: shadow}, paths, nil)
	if strings.Join(r.Args, " ") != `--model model-real --settings {"modelProvider":"gemini"}` {
		t.Fatalf("model must come from real home, args = %v", r.Args)
	}
	if envMap(t, r.Env)["ANTIGRAVITY_GEMINI_DIR"] != filepath.Join(shadow, ".gemini") {
		t.Fatalf("ANTIGRAVITY_GEMINI_DIR must still isolate to shadow: %v", r.Env)
	}
}

func TestNoRouterCredentialIsolation(t *testing.T) {
	t.Run("claude no-router scrubs router env and uses CLAUDE_CONFIG_DIR", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "claude-work")
		r, err := Resolve(harnessenv.LaunchSpec{
			Agent:      "claude",
			ShadowHome: shadow,
			NoRouter:   true,
		}, paths, testRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := envMap(t, r.Env)
		if m["CLAUDE_CONFIG_DIR"] != shadow {
			t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", m["CLAUDE_CONFIG_DIR"], shadow)
		}
		if _, ok := m["ANTHROPIC_BASE_URL"]; ok {
			t.Fatalf("no-router claude must not set ANTHROPIC_BASE_URL: %v", m)
		}
		if _, ok := m["ANTHROPIC_AUTH_TOKEN"]; ok {
			t.Fatalf("no-router claude must not set ANTHROPIC_AUTH_TOKEN: %v", m)
		}
		u := unsetSet(r)
		for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
			if !u[k] {
				t.Errorf("no-router claude must unset ambient %s, got %v", k, r.UnsetEnv)
			}
		}
	})

	t.Run("codex no-router skips model_router -c flags and uses CODEX_HOME", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "codex-work")
		r, err := Resolve(harnessenv.LaunchSpec{
			Agent:      "codex",
			ShadowHome: shadow,
			NoRouter:   true,
		}, paths, testRouter(), []string{"--ask"})
		if err != nil {
			t.Fatal(err)
		}
		if envMap(t, r.Env)["CODEX_HOME"] != shadow {
			t.Fatalf("CODEX_HOME = %q, want %q", envMap(t, r.Env)["CODEX_HOME"], shadow)
		}
		if strings.Join(r.Args, " ") != "--ask" {
			t.Fatalf("no-router codex must not inject -c router flags, got %v", r.Args)
		}
		u := unsetSet(r)
		if !u["OPENAI_API_KEY"] || !u["OPENAI_BASE_URL"] {
			t.Errorf("no-router codex must unset ambient OPENAI_* keys, got %v", r.UnsetEnv)
		}
	})

	t.Run("grok no-router uses GROK_HOME and scrubs XAI_API_KEY", func(t *testing.T) {
		paths := testPaths(t)
		shadow := filepath.Join(t.TempDir(), "grok-work")
		r, err := Resolve(harnessenv.LaunchSpec{
			Agent:      "grok",
			ShadowHome: shadow,
			NoRouter:   true,
		}, paths, testRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		m := envMap(t, r.Env)
		if m["GROK_HOME"] != shadow {
			t.Fatalf("GROK_HOME = %q, want %q", m["GROK_HOME"], shadow)
		}
		if _, ok := m["XAI_API_KEY"]; ok {
			t.Fatalf("no-router grok must not set XAI_API_KEY: %v", m)
		}
		if !unsetSet(r)["XAI_API_KEY"] {
			t.Errorf("no-router grok must unset ambient XAI_API_KEY, got %v", r.UnsetEnv)
		}
	})

	t.Run("explicit spec.Env wins over no-router scrub", func(t *testing.T) {
		paths := testPaths(t)
		r, err := Resolve(harnessenv.LaunchSpec{
			Agent:    "claude",
			NoRouter: true,
			Env:      map[string]string{"ANTHROPIC_API_KEY": "sk-explicit"},
		}, paths, testRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if envMap(t, r.Env)["ANTHROPIC_API_KEY"] != "sk-explicit" {
			t.Fatalf("explicit ANTHROPIC_API_KEY lost: %v", r.Env)
		}
		if unsetSet(r)["ANTHROPIC_API_KEY"] {
			t.Fatalf("explicit ANTHROPIC_API_KEY must not be unset: %v", r.UnsetEnv)
		}
	})
}

func TestAntigravityProfileOwnsCredentialEnvironment(t *testing.T) {
	paths := testPaths(t)
	t.Run("antigravity", func(t *testing.T) {
		shadow := filepath.Join(t.TempDir(), "private")
		r, err := Resolve(harnessenv.LaunchSpec{
			Agent: "antigravity", Binary: "/fake/agy", HomePath: shadow, NoRouter: true,
			Env: map[string]string{
				"HOME": "/shared", "XDG_CONFIG_HOME": "/shared", "XDG_DATA_HOME": "/shared",
				"DBUS_SESSION_BUS_ADDRESS": "unix:path=/shared/bus",
				"ANTIGRAVITY_GEMINI_DIR":   "/shared", "ANTIGRAVITY_CONFIG_REL_DIR": "/shared",
				"GEMINI_HOME": "/shared",
				"BROWSER":     "my-browser",
			},
		}, paths, testRouter(), nil)
		if err != nil {
			t.Fatal(err)
		}
		env := envMap(t, r.Env)
		for key, want := range map[string]string{
			"HOME": shadow, "XDG_CONFIG_HOME": filepath.Join(shadow, ".config"),
			"XDG_DATA_HOME":            filepath.Join(shadow, ".local", "share"),
			"DBUS_SESSION_BUS_ADDRESS": "disabled:", "BROWSER": "my-browser",
		} {
			if env[key] != want {
				t.Errorf("%s = %q, want %q", key, env[key], want)
			}
		}
		if env["ANTIGRAVITY_CONFIG_REL_DIR"] != "" || !unsetSet(r)["ANTIGRAVITY_CONFIG_REL_DIR"] {
			t.Error("the profile must drop external Antigravity config redirects")
		}
	})
}

func TestAntigravityBashEnvRestoresRealEnv(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found on PATH")
	}
	t.Run("restores real HOME, XDG, and D-Bus and does not clobber nested overrides", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
		t.Setenv("BASH_ENV", "")
		script, err := EnsureRestoreEnvScript(paths)
		if err != nil {
			t.Fatalf("EnsureRestoreEnvScript: %v", err)
		}
		shadow := filepath.Join(t.TempDir(), "agy-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity", ShadowHome: shadow}, paths, nil)
		if envMap(t, r.Env)["BASH_ENV"] != script {
			t.Fatalf("BASH_ENV = %q, want %q", envMap(t, r.Env)["BASH_ENV"], script)
		}

		cmd := exec.Command("bash", "-c", `printf '%s|%s|%s|%s|%s|%s|%s\n' "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" "$XDG_CACHE_HOME" "$DBUS_SESSION_BUS_ADDRESS" "${BASH_ENV:-}"; HOME=/tmp/custom-nested bash -c 'printf "%s\n" "$HOME"'`)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, r.Env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash failed: %v\n%s", err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != 2 {
			t.Fatalf("unexpected output lines: %q", string(out))
		}
		want := strings.Join([]string{
			paths.Home,
			paths.ConfigHome,
			paths.DataHome,
			paths.StateHome,
			paths.CacheHome,
			"unix:path=/run/user/1000/bus",
			"",
		}, "|")
		if lines[0] != want {
			t.Fatalf("restored env = %q, want %q", lines[0], want)
		}
		if lines[1] != "/tmp/custom-nested" {
			t.Fatalf("nested bash HOME = %q, want /tmp/custom-nested", lines[1])
		}
	})

	t.Run("unsets disabled D-Bus when originally unset and chains prior BASH_ENV", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		t.Setenv("HARNESS_REAL_DBUS_SESSION_BUS_ADDRESS", "")
		prior := filepath.Join(t.TempDir(), "prior.sh")
		if err := os.WriteFile(prior, []byte("export PRIOR_MARKER=ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BASH_ENV", prior)
		if _, err := EnsureRestoreEnvScript(paths); err != nil {
			t.Fatalf("EnsureRestoreEnvScript: %v", err)
		}
		shadow := filepath.Join(t.TempDir(), "agy-shadow")
		r := mustResolve(t, harnessenv.LaunchSpec{Agent: "antigravity", ShadowHome: shadow}, paths, nil)

		cmd := exec.Command("bash", "-c", `printf '%s|%s|%s\n' "${DBUS_SESSION_BUS_ADDRESS+set}" "${PRIOR_MARKER:-}" "${BASH_ENV:-}"`)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, r.Env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash failed: %v\n%s", err, out)
		}
		got := strings.TrimSpace(string(out))
		want := "|ok|" + prior
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

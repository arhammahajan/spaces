package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/agents"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/routerctl"
)

// testPaths builds a fully temporary home and XDG layout. HOME and
// XDG_CONFIG_HOME are repointed so even the config loader's default
// credential lookup stays inside the temp home, and the hooks/runtime
// environment variables are cleared so nothing leaks from the developer
// environment.
func testPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HARNESS_ROUTER_CONFIG", "")
	t.Setenv("SPACES_HOOKS_RUNTIME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	return harnessenv.Paths{
		Home:       home,
		ConfigHome: filepath.Join(home, ".config"),
		DataHome:   filepath.Join(home, ".local", "share"),
		StateHome:  filepath.Join(home, ".local", "state"),
		CacheHome:  filepath.Join(home, ".cache"),
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// routerStatusServer is a canned healthy-ish router for the status
// probe: livez/readyz honor the codes, /v1/models lists two models.
func routerStatusServer(t *testing.T, livezCode, readyCode int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"model-router-go","version":"9.9.9"}`))
	})
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(livezCode)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(readyCode)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a-model"},{"id":"b-model"}]}`))
	})
	mux.HandleFunc("/diagnostics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	return httptest.NewServer(mux)
}

// routerOptions builds routerctl.Options against the temp paths with an
// Exec hook that fails, so managed-mode detection never touches the real
// systemctl.
func routerOptions(paths harnessenv.Paths, url string) routerctl.Options {
	return routerctl.Options{
		Paths:     paths,
		Router:    harnessenv.RouterSettings{URL: url, APIKey: "test-key"},
		Systemctl: "systemctl",
		Exec: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("systemctl unavailable in tests")
		},
		PidPath:     filepath.Join(paths.StateHome, "harness", "router.pid"),
		HTTPTimeout: 2 * time.Second,
		Kill:        syscall.Kill,
	}
}

// validRouterConfigTOML is a minimal valid router config; the upstream
// key comes from the environment like the routerctl fixtures do.
const validRouterConfigTOML = `
[server]
auth_mode = "none"
allow_dynamic_routes = false

[retry]
max_attempts = 1

[providers.up]
dialect = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "DOCTOR_TEST_UP_KEY"

[[models]]
name = "claude-haiku"
provider = "up"
model = "claude-haiku-4-5"
`

// installAllAgents fakes every default agent binary under the temp home
// and PATH so the agents check reports a fully installed registry.
// antigravity-native has no default binary by design and is skipped by
// the check itself.
func installAllAgents(t *testing.T, paths harnessenv.Paths) {
	t.Helper()
	bindir := t.TempDir()
	for _, name := range []string{"claude", "opencode", "pi", "qwen", "kimi", "mcode", "copilot", "node"} {
		writeExec(t, filepath.Join(bindir, name))
	}
	t.Setenv("PATH", bindir)
	home := paths.Home
	for _, name := range []string{"codex", "grok", "muse"} {
		writeExec(t, filepath.Join(home, ".local", "bin", name))
	}
	writeExec(t, filepath.Join(home, ".local", "share", "kimi-cli", "bin", "kimi-cli"))
	writeExec(t, filepath.Join(home, ".kilo", "bin", "kilo"))
	writeExec(t, filepath.Join(home, ".local", "libexec", "fx", "fx-router"))
	writeExec(t, filepath.Join(home, ".local", "share", "deepseek-harness", "bin", "dsh"))
	writeExec(t, filepath.Join(home, ".local", "share", "uv", "tools", "mistral-vibe", "bin", "vibe"))
	writeExec(t, filepath.Join(home, ".local", "libexec", "antigravity", "agy"))
	// cline resolves its node through the fnm seam.
	fnmdir := t.TempDir()
	writeExec(t, filepath.Join(fnmdir, "node"))
	writeExec(t, filepath.Join(fnmdir, "cline"))
	old := agents.CommandRunner
	agents.CommandRunner = func(name string, args ...string) (string, error) {
		return filepath.Join(fnmdir, "node") + "\n", nil
	}
	t.Cleanup(func() { agents.CommandRunner = old })
}

// healthySpacesRuntime writes an endpoint naming a live unix socket plus a
// spool of the given line count, and returns the socket path.
func healthySpacesRuntime(t *testing.T, dir string, spoolLines int) string {
	t.Helper()
	sock := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sock)
	})
	endpoint := fmt.Sprintf(`{"version":1,"protocol":"http/uds","pid":%d,"uid":%d,"socket":%q,"tcp_port":null,"token":"x","started_ms":1}`,
		os.Getpid(), os.Getuid(), sock)
	writeFile(t, filepath.Join(dir, "hooks.endpoint.json"), endpoint)
	var spool strings.Builder
	for i := 0; i < spoolLines; i++ {
		fmt.Fprintf(&spool, `{"seq":%d,"agent":"claude","event":"stop"}`+"\n", i)
	}
	writeFile(t, filepath.Join(dir, "hooks-spool.jsonl"), spool.String())
	return sock
}

func byName(checks []Check) map[string]Check {
	out := make(map[string]Check, len(checks))
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

func TestRunHealthyStack(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("DOCTOR_TEST_UP_KEY", "dummy")
	srv := routerStatusServer(t, http.StatusOK, http.StatusOK)
	defer srv.Close()
	installAllAgents(t, paths)

	cfg := filepath.Join(paths.ConfigHome, "model-router", "router-go.toml")
	writeFile(t, cfg, validRouterConfigTOML)
	writeFile(t, filepath.Join(paths.Home, ".harness", "profiles.json"),
		`{"version":1,"profiles":{"work":{"agent":"claude","displayName":"Work"},"play":{"agent":"codex","displayName":"Play"}}}`)

	runtime := t.TempDir()
	sock := healthySpacesRuntime(t, runtime, 2)
	writeFile(t, filepath.Join(runtime, "hooks.server.lock"), fmt.Sprintf("%d\n", os.Getpid()))

	target := filepath.Join(paths.Home, "bin", "harness-real")
	writeExec(t, target)
	for _, name := range entryNames {
		symlink(t, target, filepath.Join(paths.Home, ".local", "bin", name))
	}
	writeFile(t, filepath.Join(paths.Home, ".local", "bin", "tmux-spaces"), "wasm-bytes")
	writeFile(t, filepath.Join(paths.ConfigHome, "tmux", "tmux.conf"), "keybinds {}")

	checks := Run(CheckOptions{
		Paths:         paths,
		Router:        routerOptions(paths, srv.URL),
		RouterConfig:  cfg,
		SpacesRuntime: runtime,
	})
	wantNames := []string{"router", "router-config", "profiles",
		"agents", "spaces-runtime", "spaces-listener", "entries", "tmux"}
	if len(checks) != len(wantNames) {
		t.Fatalf("Run returned %d checks, want %d", len(checks), len(wantNames))
	}
	for i, want := range wantNames {
		if checks[i].Name != want {
			t.Errorf("check %d name = %q, want %q", i, checks[i].Name, want)
		}
		if checks[i].State != StateOK {
			t.Errorf("check %s = %q (%s), want ok", checks[i].Name, checks[i].State, checks[i].Detail)
		}
		if strings.TrimSpace(checks[i].Detail) == "" {
			t.Errorf("check %s has an empty detail", checks[i].Name)
		}
	}
	if detail := checks[0].Detail; !strings.Contains(detail, "live and ready; 2 models") ||
		!strings.Contains(detail, "version 9.9.9") {
		t.Errorf("router detail = %q", detail)
	}
	if detail := checks[1].Detail; !strings.Contains(detail, "valid config at "+cfg) {
		t.Errorf("router-config detail = %q", detail)
	}
	if detail := checks[2].Detail; detail != "2 profiles" {
		t.Errorf("profiles detail = %q", detail)
	}
	if detail := checks[3].Detail; detail != "18/18 installed" {
		t.Errorf("agents detail = %q", detail)
	}
	if detail := checks[4].Detail; !strings.Contains(detail, "socket "+sock) ||
		!strings.Contains(detail, "2 spooled events") {
		t.Errorf("spaces-runtime detail = %q", detail)
	}
	if detail := checks[5].Detail; detail != fmt.Sprintf("listener pid %d alive", os.Getpid()) {
		t.Errorf("spaces-listener detail = %q", detail)
	}
	if detail := checks[6].Detail; detail != "spaces -> "+target {
		t.Errorf("entries detail = %q", detail)
	}
	s := Summarize(checks)
	if s.OK != 8 || s.Warn != 0 || s.Fail != 0 {
		t.Fatalf("summary = %+v, want 8 ok", s)
	}
	if out := Render(checks); !strings.HasSuffix(out, "8 ok, 0 warn, 0 fail\n") {
		t.Errorf("render summary line missing: %q", out)
	}
}

// TestRunDegradedStack pins the absent-piece state of every check: an
// empty temp home with nothing installed.
func TestRunDegradedStack(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("PATH", t.TempDir())
	opts := CheckOptions{
		Paths:  paths,
		Router: routerOptions(paths, "http://127.0.0.1:1"), // nothing listens
		// A config path that does not exist.
		RouterConfig:  filepath.Join(paths.ConfigHome, "router.toml"),
		SpacesRuntime: t.TempDir(),
	}
	checks := Run(opts)
	if len(checks) != 8 {
		t.Fatalf("Run returned %d checks, want 8", len(checks))
	}
	want := map[string]string{
		"router":          StateFail, // not live
		"router-config":   StateFail, // read config: no such file
		"profiles":        StateOK,   // empty store is healthy
		"agents":          StateWarn, // nothing installed
		"spaces-runtime":  StateWarn, // no endpoint
		"spaces-listener": StateWarn, // no lock
		"entries":         StateFail, // both missing
		"tmux":            StateFail, // both missing
	}
	got := byName(checks)
	for name, state := range want {
		if got[name].State != state {
			t.Errorf("check %s = %q (%s), want %q", name, got[name].State, got[name].Detail, state)
		}
	}
	if detail := got["router"].Detail; !strings.Contains(detail, "not live") {
		t.Errorf("router detail = %q", detail)
	}
	if detail := got["router-config"].Detail; !strings.Contains(detail, "read config") {
		t.Errorf("router-config detail = %q", detail)
	}
	if detail := got["profiles"].Detail; detail != "0 profiles" {
		t.Errorf("profiles detail = %q", detail)
	}
	if detail := got["agents"].Detail; !strings.HasPrefix(detail, "missing: ") ||
		!strings.Contains(detail, "claude") || strings.Contains(detail, "antigravity-native") {
		t.Errorf("agents detail = %q", detail)
	}
	if detail := got["entries"].Detail; detail != "spaces missing" {
		t.Errorf("entries detail = %q", detail)
	}
	s := Summarize(checks)
	if s.OK != 1 || s.Warn != 3 || s.Fail != 4 {
		t.Fatalf("summary = %+v, want 1 ok, 3 warn, 4 fail", s)
	}
	if out := Render(checks); !strings.HasSuffix(out, "1 ok, 3 warn, 4 fail\n") {
		t.Errorf("render summary line wrong: %q", out)
	}
}

// TestRunPanicBecomesFail proves the never-panic guarantee end to end:
// a signal seam that panics turns its row into a fail while every other
// check still runs.
func TestRunPanicBecomesFail(t *testing.T) {
	paths := testPaths(t)
	runtime := t.TempDir()
	writeFile(t, filepath.Join(runtime, "hooks.server.lock"), fmt.Sprintf("%d\n", os.Getpid()))
	opts := CheckOptions{
		Paths:         paths,
		SpacesRuntime: runtime,
		Kill:          func(int, syscall.Signal) error { panic("kill exploded") },
	}
	checks := Run(opts)
	if len(checks) != 8 {
		t.Fatalf("Run returned %d checks, want 8", len(checks))
	}
	row := byName(checks)["spaces-listener"]
	if row.State != StateFail || !strings.Contains(row.Detail, "check panicked") ||
		!strings.Contains(row.Detail, "kill exploded") {
		t.Errorf("listener row = %+v, want a fail carrying the panic", row)
	}
	for _, c := range checks {
		switch c.State {
		case StateOK, StateWarn, StateFail:
		default:
			t.Errorf("check %s has unknown state %q", c.Name, c.State)
		}
	}
}

func TestSafeStepRecoversPanic(t *testing.T) {
	state, detail := safeStep("boom", func(CheckOptions) (string, string) {
		panic("kaboom")
	}, CheckOptions{})
	if state != StateFail {
		t.Errorf("state = %q, want fail", state)
	}
	if !strings.Contains(detail, "check panicked") || !strings.Contains(detail, "kaboom") {
		t.Errorf("detail = %q", detail)
	}
}

func TestCheckRouter(t *testing.T) {
	paths := testPaths(t)
	t.Run("ok with version and models", func(t *testing.T) {
		srv := routerStatusServer(t, http.StatusOK, http.StatusOK)
		defer srv.Close()
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, srv.URL)})
		if state != StateOK {
			t.Fatalf("state = %q (%s)", state, detail)
		}
		if !strings.Contains(detail, "live and ready; 2 models") ||
			!strings.Contains(detail, "version 9.9.9") {
			t.Errorf("detail = %q", detail)
		}
	})
	t.Run("warn when not ready", func(t *testing.T) {
		srv := routerStatusServer(t, http.StatusOK, http.StatusServiceUnavailable)
		defer srv.Close()
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, srv.URL)})
		if state != StateWarn || !strings.Contains(detail, "not ready") {
			t.Errorf("(%q, %q), want warn/not ready", state, detail)
		}
	})
	t.Run("fail when not live", func(t *testing.T) {
		srv := routerStatusServer(t, http.StatusServiceUnavailable, http.StatusOK)
		defer srv.Close()
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, srv.URL)})
		if state != StateFail || !strings.Contains(detail, "not live") {
			t.Errorf("(%q, %q), want fail/not live", state, detail)
		}
	})
	t.Run("fail when unreachable", func(t *testing.T) {
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, "http://127.0.0.1:1")})
		if state != StateFail || !strings.Contains(detail, "not live") {
			t.Errorf("(%q, %q), want fail/not live", state, detail)
		}
	})
	t.Run("fail with error detail when no url", func(t *testing.T) {
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, "")})
		if state != StateFail || !strings.Contains(detail, "no router URL configured") {
			t.Errorf("(%q, %q), want fail/no router URL configured", state, detail)
		}
	})
	t.Run("fail with error detail when malformed url", func(t *testing.T) {
		state, detail := checkRouter(CheckOptions{Router: routerOptions(paths, "://bad")})
		if state != StateFail || !strings.Contains(detail, "invalid router URL") {
			t.Errorf("(%q, %q), want fail/invalid router URL", state, detail)
		}
	})
}

func TestCheckRouterConfig(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("DOCTOR_TEST_UP_KEY", "dummy")
	dir := t.TempDir()
	good := filepath.Join(dir, "router.toml")
	writeFile(t, good, validRouterConfigTOML)
	bad := filepath.Join(dir, "bad.toml")
	writeFile(t, bad, "[server\n")
	missing := filepath.Join(dir, "missing.toml")

	t.Run("injected path", func(t *testing.T) {
		state, detail := checkRouterConfig(CheckOptions{RouterConfig: good})
		if state != StateOK || detail != "valid config at "+good {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("invalid toml fails", func(t *testing.T) {
		state, detail := checkRouterConfig(CheckOptions{RouterConfig: bad})
		if state != StateFail || detail == "" {
			t.Errorf("(%q, %q), want fail with the error as detail", state, detail)
		}
	})
	t.Run("missing file fails", func(t *testing.T) {
		state, detail := checkRouterConfig(CheckOptions{RouterConfig: missing})
		if state != StateFail || !strings.Contains(detail, "read config") {
			t.Errorf("(%q, %q), want fail/read config", state, detail)
		}
	})
	t.Run("env detection", func(t *testing.T) {
		t.Setenv("HARNESS_ROUTER_CONFIG", good)
		state, detail := checkRouterConfig(CheckOptions{Paths: paths})
		if state != StateOK || detail != "valid config at "+good {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("config home detection", func(t *testing.T) {
		detected := filepath.Join(paths.ConfigHome, "model-router", "router-go.toml")
		writeFile(t, detected, validRouterConfigTOML)
		state, detail := checkRouterConfig(CheckOptions{Paths: paths})
		if state != StateOK || detail != "valid config at "+detected {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
}

func TestRouterConfigPathResolution(t *testing.T) {
	testPaths(t) // clears HARNESS_ROUTER_CONFIG
	if got := (CheckOptions{RouterConfig: "injected"}).routerConfigPath(); got != "injected" {
		t.Errorf("injected = %q", got)
	}
	t.Setenv("HARNESS_ROUTER_CONFIG", "/env/router.toml")
	if got := (CheckOptions{}).routerConfigPath(); got != "/env/router.toml" {
		t.Errorf("env = %q", got)
	}
	t.Setenv("HARNESS_ROUTER_CONFIG", "")
	if got := (CheckOptions{}).routerConfigPath(); got != "router.toml" {
		t.Errorf("fallback = %q, want the literal router.toml", got)
	}
}

func TestCheckProfiles(t *testing.T) {
	paths := testPaths(t)
	store := filepath.Join(paths.Home, ".harness", "profiles.json")
	t.Run("count", func(t *testing.T) {
		writeFile(t, store, `{"version":1,"profiles":{"a":{"agent":"claude"},"b":{"agent":"codex"}}}`)
		state, detail := checkProfiles(CheckOptions{Paths: paths})
		if state != StateOK || detail != "2 profiles" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("absent store is ok", func(t *testing.T) {
		_ = os.Remove(store)
		state, detail := checkProfiles(CheckOptions{Paths: paths})
		if state != StateOK || detail != "0 profiles" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("malformed store fails", func(t *testing.T) {
		writeFile(t, store, "{not json")
		state, detail := checkProfiles(CheckOptions{Paths: paths})
		if state != StateFail || !strings.Contains(detail, "parse") {
			t.Errorf("(%q, %q), want fail/parse", state, detail)
		}
	})
}

func TestCheckAgents(t *testing.T) {
	t.Run("all installed", func(t *testing.T) {
		paths := testPaths(t)
		installAllAgents(t, paths)
		state, detail := checkAgents(CheckOptions{Paths: paths})
		if state != StateOK || detail != "18/18 installed" {
			t.Errorf("(%q, %q), want ok/18 installed", state, detail)
		}
	})
	t.Run("missing ones listed", func(t *testing.T) {
		paths := testPaths(t)
		t.Setenv("PATH", t.TempDir()) // no PATH agents either
		state, detail := checkAgents(CheckOptions{Paths: paths})
		if state != StateWarn {
			t.Fatalf("state = %q (%s)", state, detail)
		}
		if !strings.HasPrefix(detail, "missing: ") {
			t.Errorf("detail = %q", detail)
		}
		if !strings.Contains(detail, "claude") || !strings.Contains(detail, "codex") {
			t.Errorf("detail = %q, want the missing binaries listed", detail)
		}
		if strings.Contains(detail, "antigravity-native") {
			t.Errorf("detail = %q, antigravity-native must not be listed", detail)
		}
	})
	t.Run("partial install", func(t *testing.T) {
		paths := testPaths(t)
		bindir := t.TempDir()
		writeExec(t, filepath.Join(bindir, "claude"))
		t.Setenv("PATH", bindir)
		writeExec(t, filepath.Join(paths.Home, ".local", "bin", "codex"))
		state, detail := checkAgents(CheckOptions{Paths: paths})
		if state != StateWarn || !strings.HasPrefix(detail, "missing: ") {
			t.Fatalf("(%q, %q), want warn with a missing list", state, detail)
		}
		// Neither installed agent may be listed as missing.
		if strings.Contains(detail, "claude") || strings.Contains(detail, "codex") {
			t.Errorf("detail = %q lists an installed agent", detail)
		}
	})
}

func TestCheckSpacesRuntime(t *testing.T) {
	paths := testPaths(t)
	t.Run("healthy with spool", func(t *testing.T) {
		runtime := t.TempDir()
		sock := healthySpacesRuntime(t, runtime, 2)
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateOK {
			t.Fatalf("state = %q (%s)", state, detail)
		}
		if !strings.Contains(detail, "socket "+sock) || !strings.Contains(detail, "2 spooled events") {
			t.Errorf("detail = %q", detail)
		}
	})
	t.Run("absent endpoint warns", func(t *testing.T) {
		runtime := t.TempDir()
		writeFile(t, filepath.Join(runtime, "hooks-spool.jsonl"), `{"a":1}`+"\n")
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateWarn || !strings.Contains(detail, "no endpoint in "+runtime) ||
			!strings.Contains(detail, "1 spooled events") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing runtime dir warns", func(t *testing.T) {
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: filepath.Join(t.TempDir(), "absent")})
		if state != StateWarn || !strings.Contains(detail, "no endpoint in") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("malformed endpoint fails", func(t *testing.T) {
		runtime := t.TempDir()
		writeFile(t, filepath.Join(runtime, "hooks.endpoint.json"), "{not json")
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateFail || !strings.Contains(detail, "malformed endpoint") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("endpoint without socket fails", func(t *testing.T) {
		runtime := t.TempDir()
		writeFile(t, filepath.Join(runtime, "hooks.endpoint.json"), `{"version":1}`)
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateFail || !strings.Contains(detail, "malformed endpoint") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing socket fails", func(t *testing.T) {
		runtime := t.TempDir()
		gone := filepath.Join(runtime, "gone.sock")
		writeFile(t, filepath.Join(runtime, "hooks.endpoint.json"),
			fmt.Sprintf(`{"version":1,"socket":%q}`, gone))
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateFail || !strings.Contains(detail, "socket "+gone+" missing") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("blank spool lines are not counted", func(t *testing.T) {
		runtime := t.TempDir()
		sock := healthySpacesRuntime(t, runtime, 0)
		writeFile(t, filepath.Join(runtime, "hooks-spool.jsonl"), "\n{\"a\":1}\n\n\n{\"b\":2}\n\n")
		state, detail := checkSpacesRuntime(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateOK || !strings.Contains(detail, "2 spooled events") {
			t.Errorf("(%q, %q), want 2 spooled events for %s", state, detail, sock)
		}
	})
}

func TestCheckListener(t *testing.T) {
	paths := testPaths(t)
	lock := func(t *testing.T, runtime, body string) {
		t.Helper()
		writeFile(t, filepath.Join(runtime, "hooks.server.lock"), body)
	}
	t.Run("alive pid", func(t *testing.T) {
		runtime := t.TempDir()
		lock(t, runtime, fmt.Sprintf("%d\n", os.Getpid()))
		state, detail := checkListener(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateOK || detail != fmt.Sprintf("listener pid %d alive", os.Getpid()) {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("dead pid", func(t *testing.T) {
		runtime := t.TempDir()
		// Beyond any allocatable pid, so the real syscall returns ESRCH.
		lock(t, runtime, "33554432\n")
		state, detail := checkListener(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateFail || !strings.Contains(detail, "pid 33554432 not running") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("absent lock warns", func(t *testing.T) {
		runtime := t.TempDir()
		state, detail := checkListener(CheckOptions{Paths: paths, SpacesRuntime: runtime})
		if state != StateWarn || !strings.Contains(detail, "no listener lock") {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("unparseable lock warns", func(t *testing.T) {
		for _, body := range []string{"", "flock-held\n", "123abc", "-5\n", "0\n"} {
			runtime := t.TempDir()
			lock(t, runtime, body)
			state, detail := checkListener(CheckOptions{Paths: paths, SpacesRuntime: runtime})
			if state != StateWarn || !strings.Contains(detail, "no parseable pid") {
				t.Errorf("lock %q: (%q, %q), want warn/no parseable pid", body, state, detail)
			}
		}
	})
	t.Run("eperm counts as alive", func(t *testing.T) {
		runtime := t.TempDir()
		lock(t, runtime, "4242\n")
		opts := CheckOptions{Paths: paths, SpacesRuntime: runtime,
			Kill: func(int, syscall.Signal) error { return syscall.EPERM }}
		state, detail := checkListener(opts)
		if state != StateOK || detail != "listener pid 4242 alive" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("esrch counts as dead", func(t *testing.T) {
		runtime := t.TempDir()
		lock(t, runtime, "4242\n")
		opts := CheckOptions{Paths: paths, SpacesRuntime: runtime,
			Kill: func(int, syscall.Signal) error { return syscall.ESRCH }}
		state, detail := checkListener(opts)
		if state != StateFail || detail != "listener pid 4242 not running" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
}

func TestCheckEntries(t *testing.T) {
	// Each subtest gets a fresh home so leftovers never leak.
	t.Run("healthy symlinks", func(t *testing.T) {
		paths := testPaths(t)
		target := filepath.Join(paths.Home, "bin", "tmux-spaces")
		writeExec(t, target)
		symlink(t, target, filepath.Join(paths.Home, ".local", "bin", "spaces"))
		state, detail := checkEntries(CheckOptions{Paths: paths})
		if state != StateOK || detail != "spaces -> "+target {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing", func(t *testing.T) {
		paths := testPaths(t)
		state, detail := checkEntries(CheckOptions{Paths: paths})
		if state != StateFail || detail != "spaces missing" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("dangling", func(t *testing.T) {
		paths := testPaths(t)
		gone := filepath.Join(paths.Home, "bin", "gone")
		symlink(t, gone, filepath.Join(paths.Home, ".local", "bin", "spaces"))
		state, detail := checkEntries(CheckOptions{Paths: paths})
		if state != StateFail || !strings.Contains(detail, "spaces dangles to "+gone) {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("regular files still resolve", func(t *testing.T) {
		paths := testPaths(t)
		binDir := filepath.Join(paths.Home, ".local", "bin")
		writeExec(t, filepath.Join(binDir, "spaces"))
		state, detail := checkEntries(CheckOptions{Paths: paths})
		if state != StateOK || detail != "spaces (regular file)" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
}

func TestCheckTmux(t *testing.T) {
	// Each subtest gets a fresh home so leftovers never leak.
	t.Run("present", func(t *testing.T) {
		paths := testPaths(t)
		writeFile(t, filepath.Join(paths.Home, ".local", "bin", "tmux-spaces"), "wasm")
		writeFile(t, filepath.Join(paths.ConfigHome, "tmux", "tmux.conf"), "keybinds {}")
		state, detail := checkTmux(CheckOptions{Paths: paths})
		if state != StateOK || detail != "plugin and config present" {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing plugin", func(t *testing.T) {
		paths := testPaths(t)
		plugin := filepath.Join(paths.Home, ".local", "bin", "tmux-spaces")
		writeFile(t, filepath.Join(paths.ConfigHome, "tmux", "tmux.conf"), "keybinds {}")
		state, detail := checkTmux(CheckOptions{Paths: paths})
		if state != StateFail || !strings.Contains(detail, "plugin "+plugin) {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing config", func(t *testing.T) {
		paths := testPaths(t)
		config := filepath.Join(paths.ConfigHome, "tmux", "tmux.conf")
		writeFile(t, filepath.Join(paths.Home, ".local", "bin", "tmux-spaces"), "wasm")
		state, detail := checkTmux(CheckOptions{Paths: paths})
		if state != StateFail || !strings.Contains(detail, "config "+config) {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
	t.Run("missing both", func(t *testing.T) {
		paths := testPaths(t)
		plugin := filepath.Join(paths.Home, ".local", "bin", "tmux-spaces")
		config := filepath.Join(paths.ConfigHome, "tmux", "tmux.conf")
		state, detail := checkTmux(CheckOptions{Paths: paths})
		if state != StateFail ||
			!strings.Contains(detail, "plugin "+plugin) || !strings.Contains(detail, "config "+config) {
			t.Errorf("(%q, %q)", state, detail)
		}
	})
}

func TestSpacesRuntimeDirResolution(t *testing.T) {
	testPaths(t) // clears SPACES_HOOKS_RUNTIME and XDG_RUNTIME_DIR
	t.Run("injected wins", func(t *testing.T) {
		t.Setenv("SPACES_HOOKS_RUNTIME", "/env-hooks")
		if got := (CheckOptions{SpacesRuntime: "/injected"}).spacesRuntimeDir(); got != "/injected" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("SPACES_HOOKS_RUNTIME with expansion", func(t *testing.T) {
		t.Setenv("HOOKDIR", "/hooks")
		t.Setenv("SPACES_HOOKS_RUNTIME", "$HOOKDIR/spaces")
		if got := (CheckOptions{}).spacesRuntimeDir(); got != "/hooks/spaces" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("XDG_RUNTIME_DIR fallback", func(t *testing.T) {
		t.Setenv("SPACES_HOOKS_RUNTIME", "")
		t.Setenv("XDG_RUNTIME_DIR", "/xdg")
		if got := (CheckOptions{}).spacesRuntimeDir(); got != "/xdg/spaces" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tmp fallback", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		if got := (CheckOptions{}).spacesRuntimeDir(); got != "/tmp/spaces" {
			t.Errorf("got %q", got)
		}
	})
}

func TestParsePid(t *testing.T) {
	cases := []struct {
		in  string
		pid int
		ok  bool
	}{
		{"12345\n", 12345, true},
		{"  12345  ", 12345, true},
		{"1", 1, true},
		{"", 0, false},
		{"123abc", 0, false},
		{"-5", 0, false},
		{"0", 0, false},
		{"pid 42", 0, false},
	}
	for _, c := range cases {
		pid, ok := parsePid(c.in)
		if pid != c.pid || ok != c.ok {
			t.Errorf("parsePid(%q) = (%d, %v), want (%d, %v)", c.in, pid, ok, c.pid, c.ok)
		}
	}
}

func TestSpoolCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks-spool.jsonl")
	writeFile(t, path, "a\nb\n")
	if got := spoolCount(path); got != 2 {
		t.Errorf("spoolCount = %d, want 2", got)
	}
	writeFile(t, path, "a\n\nb\n\n")
	if got := spoolCount(path); got != 2 {
		t.Errorf("spoolCount blanks = %d, want 2", got)
	}
	if got := spoolCount(filepath.Join(dir, "absent.jsonl")); got != 0 {
		t.Errorf("spoolCount absent = %d, want 0", got)
	}
}

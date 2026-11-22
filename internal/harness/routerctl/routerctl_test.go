package routerctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

func testPaths(t *testing.T) harnessenv.Paths {
	t.Helper()
	home := t.TempDir()
	return harnessenv.Paths{
		Home:       home,
		ConfigHome: filepath.Join(home, ".config"),
		DataHome:   filepath.Join(home, ".local", "share"),
		StateHome:  filepath.Join(home, ".local", "state"),
		CacheHome:  filepath.Join(home, ".cache"),
	}
}

func testRouter(url string) harnessenv.RouterSettings {
	return harnessenv.RouterSettings{URL: url, APIKey: "test-key"}
}

// execFake is an injectable Exec hook with scripted outputs per command.
type execFake struct {
	calls [][]string
	// handler returns output/error for the invoked command.
	handler func(name string, args []string) ([]byte, error)
}

func (f *execFake) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.handler(name, args)
}

func (f *execFake) argStrings() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// systemdStub returns a handler that answers unit-existence probes.
// listOK controls list-unit-files output/exit, enabledOK controls
// is-enabled exit; start/stop/restart always succeed.
func systemdStub(listOutput string, listErr, enabledErr error) func(string, []string) ([]byte, error) {
	return func(_ string, args []string) ([]byte, error) {
		for i, a := range args {
			switch a {
			case "list-unit-files":
				_ = i
				if listErr != nil {
					return nil, listErr
				}
				return []byte(listOutput), nil
			case "is-enabled":
				if enabledErr != nil {
					return nil, enabledErr
				}
				return []byte("enabled\n"), nil
			case "start", "stop", "restart":
				return []byte{}, nil
			}
		}
		return nil, errors.New("unexpected systemctl invocation: " + strings.Join(args, " "))
	}
}

func failingExec(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, errors.New("systemctl: command not found")
}

func TestDetectDefaults(t *testing.T) {
	p := testPaths(t)
	opts := Detect(p, testRouter("http://127.0.0.1:4000"))
	if opts.Unit != "model-router.service" {
		t.Fatalf("unit = %q", opts.Unit)
	}
	if want := filepath.Join(p.StateHome, "harness", "router.pid"); opts.PidPath != want {
		t.Fatalf("pid = %q want %q", opts.PidPath, want)
	}
	if opts.HTTPTimeout != 5*time.Second {
		t.Fatalf("timeout = %v", opts.HTTPTimeout)
	}
	if opts.Systemctl == "" {
		t.Fatalf("systemctl = %q", opts.Systemctl)
	}
}

func TestDetectRouterBinOrder(t *testing.T) {
	newBin := func(dir, name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	now := time.Now()

	t.Run("env wins", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_BIN", "/env/bin")
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterBin != "/env/bin" {
			t.Fatalf("bin = %q", opts.RouterBin)
		}
	})

	t.Run("newest libexec wins over dev and PATH", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_BIN", "")
		old := newBin(filepath.Join(p.Home, ".local", "libexec", "model-router"), "model-router-go-old", now.Add(-time.Hour))
		newest := newBin(filepath.Join(p.Home, ".local", "libexec", "model-router"), "model-router-go-new", now)
		newBin(filepath.Join(p.Home, "dev", "model-router"), "model-router-go", now.Add(time.Hour))
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterBin != newest {
			t.Fatalf("bin = %q want %q (old %q)", opts.RouterBin, newest, old)
		}
	})

	t.Run("dev fallback", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_BIN", "")
		dev := newBin(filepath.Join(p.Home, "dev", "model-router"), "model-router-go", now)
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		t.Setenv("PATH", t.TempDir())
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterBin != dev {
			t.Fatalf("bin = %q want %q", opts.RouterBin, dev)
		}
	})

	t.Run("PATH fallback", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_BIN", "")
		bindir := t.TempDir()
		want := newBin(bindir, "model-router-go", now)
		t.Setenv("PATH", bindir)
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterBin != want {
			t.Fatalf("bin = %q want %q", opts.RouterBin, want)
		}
	})

	t.Run("literal fallback", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_BIN", "")
		t.Setenv("PATH", t.TempDir())
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterBin != "model-router-go" {
			t.Fatalf("bin = %q", opts.RouterBin)
		}
	})
}

func TestDetectRouterConfigOrder(t *testing.T) {
	write := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# cfg"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("env wins", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_CONFIG", "/env/router.toml")
		t.Setenv("HARNESS_ROUTER_BIN", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterConfig != "/env/router.toml" {
			t.Fatalf("config = %q", opts.RouterConfig)
		}
	})

	t.Run("config home wins", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		t.Setenv("HARNESS_ROUTER_BIN", "")
		want := filepath.Join(p.ConfigHome, "model-router", "router-go.toml")
		write(want)
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterConfig != want {
			t.Fatalf("config = %q want %q", opts.RouterConfig, want)
		}
	})

	t.Run("relative fallback", func(t *testing.T) {
		p := testPaths(t)
		t.Setenv("HARNESS_ROUTER_CONFIG", "")
		t.Setenv("HARNESS_ROUTER_BIN", "")
		opts := Detect(p, testRouter("http://x"))
		if opts.RouterConfig != "router.toml" {
			t.Fatalf("config = %q", opts.RouterConfig)
		}
	})
}

func TestSystemdSelection(t *testing.T) {
	cases := []struct {
		name      string
		listOut   string
		listErr   error
		enableErr error
		want      bool
	}{
		{"list-unit-files names unit", "model-router.service enabled\n", nil, errors.New("disabled"), true},
		{"is-enabled succeeds", "UNIT FILE STATE\n", nil, nil, true},
		{"neither succeeds", "UNIT FILE STATE\n", nil, errors.New("disabled"), false},
		{"list fails, is-enabled succeeds", "", errors.New("nope"), nil, true},
		{"both fail", "", errors.New("nope"), errors.New("nope"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &execFake{handler: systemdStub(tc.listOut, tc.listErr, tc.enableErr)}
			opts := Options{
				Paths:     testPaths(t),
				Router:    testRouter("http://127.0.0.1:1"),
				Unit:      "model-router.service",
				Systemctl: "systemctl",
				Exec:      f.exec,
			}
			if got := systemdManaged(context.Background(), opts); got != tc.want {
				t.Fatalf("systemdManaged = %v want %v", got, tc.want)
			}
		})
	}
}

func directOpts(p harnessenv.Paths, url string) Options {
	return Options{
		Paths:        p,
		Router:       testRouter(url),
		Unit:         "model-router.service",
		Systemctl:    "systemctl",
		Exec:         failingExec,
		PidPath:      filepath.Join(p.StateHome, "harness", "router.pid"),
		HTTPTimeout:  2 * time.Second,
		Kill:         syscall.Kill,
		RouterConfig: filepath.Join(p.ConfigHome, "router.toml"),
	}
}

// signalRecorder fakes Kill with an in-memory liveness flag.
type signalRecorder struct {
	alive   bool
	signals []syscall.Signal
}

func (r *signalRecorder) kill(_ int, sig syscall.Signal) error {
	if sig == syscall.Signal(0) {
		if r.alive {
			return nil
		}
		return errors.New("no such process")
	}
	r.signals = append(r.signals, sig)
	if sig == syscall.SIGTERM || sig == syscall.SIGKILL {
		r.alive = false
	}
	return nil
}

func writePid(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statusServer(t *testing.T, readyCode, diagCode int) (*httptest.Server, *map[string]string) {
	t.Helper()
	seen := &map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"model-router-go","version":"9.9.9"}`))
	})
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(readyCode)
		_, _ = w.Write([]byte(`{"status":"x"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		(*seen)["auth"] = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"b-model","object":"model","owned_by":"model-router","metadata":{"router":{"x":1}}},{"id":"a-model","object":"model","owned_by":"other"}]}`))
	})
	mux.HandleFunc("/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		(*seen)["diag-auth"] = r.Header.Get("Authorization")
		w.WriteHeader(diagCode)
		if diagCode == 200 {
			_, _ = w.Write([]byte(`{"uptime_s":3}`))
		} else {
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}
	})
	srv := httptest.NewServer(mux)
	return srv, seen
}

func statusOpts(p harnessenv.Paths, url string, exec func(context.Context, string, ...string) ([]byte, error)) Options {
	opts := directOpts(p, url)
	opts.Exec = exec
	return opts
}

func TestFetchStatusHealthy(t *testing.T) {
	p := testPaths(t)
	srv, seen := statusServer(t, 200, 200)
	defer srv.Close()
	st, err := FetchStatus(context.Background(), statusOpts(p, srv.URL, failingExec))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if st.Managed != "none" {
		t.Fatalf("managed = %q", st.Managed)
	}
	if st.URL != srv.URL {
		t.Fatalf("url = %q", st.URL)
	}
	if !st.Livez || !st.Ready {
		t.Fatalf("live=%v ready=%v", st.Livez, st.Ready)
	}
	if st.Version != "9.9.9" {
		t.Fatalf("version = %q", st.Version)
	}
	if st.Models != 2 {
		t.Fatalf("models = %d", st.Models)
	}
	if fmt.Sprint(st.ModelIDs) != "[a-model b-model]" {
		t.Fatalf("ids = %v", st.ModelIDs)
	}
	if string(st.Diagnostics) != `{"uptime_s":3}` {
		t.Fatalf("diag = %s", st.Diagnostics)
	}
	if st.DiagnosticsNote != "" {
		t.Fatalf("note = %q", st.DiagnosticsNote)
	}
	if (*seen)["auth"] != "Bearer test-key" {
		t.Fatalf("models auth = %q", (*seen)["auth"])
	}
	if (*seen)["diag-auth"] != "Bearer test-key" {
		t.Fatalf("diag auth = %q", (*seen)["diag-auth"])
	}
}

func TestFetchStatusDegraded(t *testing.T) {
	p := testPaths(t)
	srv, _ := statusServer(t, 503, 403)
	defer srv.Close()
	st, err := FetchStatus(context.Background(), statusOpts(p, srv.URL, failingExec))
	if err != nil {
		t.Fatalf("degraded status must not error: %v", err)
	}
	if st.Ready {
		t.Fatal("ready must be false on 503")
	}
	if !st.Livez {
		t.Fatal("live must be true")
	}
	if st.Diagnostics != nil {
		t.Fatalf("diag must be nil, got %s", st.Diagnostics)
	}
	if st.DiagnosticsNote != "403 diagnostics_disabled" {
		t.Fatalf("note = %q", st.DiagnosticsNote)
	}
	if st.Models != 2 || len(st.ModelIDs) != 2 {
		t.Fatalf("models degraded: %+v", st)
	}
}

func TestFetchStatusDown(t *testing.T) {
	p := testPaths(t)
	// Nothing listens here: every probe degrades, no hard error.
	st, err := FetchStatus(context.Background(), statusOpts(p, "http://127.0.0.1:1", failingExec))
	if err != nil {
		t.Fatalf("down router must not error: %v", err)
	}
	if st.Livez || st.Ready || st.Version != "" || st.Models != 0 {
		t.Fatalf("down status not empty: %+v", st)
	}
	if st.DiagnosticsNote == "" {
		t.Fatal("expected a diagnostics note when unreachable")
	}
}

func TestFetchStatusManagedModes(t *testing.T) {
	p := testPaths(t)
	srv, _ := statusServer(t, 200, 403)
	defer srv.Close()

	f := &execFake{handler: systemdStub("model-router.service enabled\n", nil, nil)}
	st, err := FetchStatus(context.Background(), statusOpts(p, srv.URL, f.exec))
	if err != nil {
		t.Fatal(err)
	}
	if st.Managed != "systemd" {
		t.Fatalf("managed = %q", st.Managed)
	}

	rec := &signalRecorder{alive: true}
	opts := statusOpts(p, srv.URL, failingExec)
	opts.Kill = rec.kill
	writePid(t, opts.PidPath, 4242)
	st, err = FetchStatus(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if st.Managed != "direct" {
		t.Fatalf("managed = %q", st.Managed)
	}
}

func TestListModelsShapes(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  []ModelInfo
		isErr bool
	}{
		{
			"data envelope",
			`{"object":"list","data":[{"id":"m1","owned_by":"o1","metadata":{"router":{}}}]}`,
			[]ModelInfo{{ID: "m1", OwnedBy: "o1", Router: true}},
			false,
		},
		{
			"bare array",
			`[{"id":"m2","owned_by":"o2"}]`,
			[]ModelInfo{{ID: "m2", OwnedBy: "o2"}},
			false,
		},
		{
			"non-object metadata is not router",
			`{"data":[{"id":"m3","metadata":"str"}]}`,
			[]ModelInfo{{ID: "m3"}},
			false,
		},
		{
			"empty ids skipped",
			`{"data":[{"id":"","owned_by":"x"},{"id":"m4"}]}`,
			[]ModelInfo{{ID: "m4"}},
			false,
		},
		{"invalid json", `{{{`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					w.WriteHeader(404)
					return
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			opts := statusOpts(testPaths(t), srv.URL, failingExec)
			got, err := ListModels(context.Background(), opts)
			if tc.isErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestListModelsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	opts := statusOpts(testPaths(t), srv.URL, failingExec)
	if _, err := ListModels(context.Background(), opts); err == nil {
		t.Fatal("expected status error")
	}
}

func TestListModelsNoKeySendsNoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	p := testPaths(t)
	opts := statusOpts(p, srv.URL, failingExec)
	opts.Router = harnessenv.RouterSettings{URL: srv.URL, APIKey: ""}
	if _, err := ListModels(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("auth = %q, want empty", gotAuth)
	}
}

func TestValidateConfigWithOptions(t *testing.T) {
	// Keep default credential loading off the real home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TEST_UP_KEY_M2", "dummy")
	dir := t.TempDir()
	good := filepath.Join(dir, "router.toml")
	body := `
[server]
auth_mode = "none"
allow_dynamic_routes = false

[retry]
max_attempts = 1

[providers.up]
dialect = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "TEST_UP_KEY_M2"

[[models]]
name = "claude-haiku"
provider = "up"
model = "claude-haiku-4-5"
`
	if err := os.WriteFile(good, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{}
	if err := ValidateConfigWithOptions(context.Background(), opts, good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigWithOptions(context.Background(), opts, bad); err == nil {
		t.Fatal("expected error for invalid config")
	}
	if err := ValidateConfigWithOptions(context.Background(), opts, filepath.Join(dir, "missing.toml")); err == nil {
		t.Fatal("expected error for missing config")
	}
}

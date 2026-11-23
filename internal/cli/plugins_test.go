package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/agent"
)

type fakeTestCompanion struct {
	spec         agent.CompanionSpec
	installed    bool
	path         string
	version      string
	detail       string
	installRes   agent.CompanionInstallResult
	installErr   error
	uninstallErr error
	installedArg *agent.CompanionInstallOptions
}

func (f *fakeTestCompanion) CompanionSpec() agent.CompanionSpec { return f.spec }
func (f *fakeTestCompanion) NormalizeCompanionEvent(envelope map[string]any) (map[string]any, bool) {
	return map[string]any{"source": "plugin"}, true
}
func (f *fakeTestCompanion) DetectCompanionStatus(_ string) agent.CompanionStatus {
	return agent.CompanionStatus{Installed: f.installed, Path: f.path, Version: f.version, Detail: f.detail}
}
func (f *fakeTestCompanion) InstallCompanion(opts agent.CompanionInstallOptions) (agent.CompanionInstallResult, error) {
	f.installedArg = &opts
	return f.installRes, f.installErr
}
func (f *fakeTestCompanion) UninstallCompanion(opts agent.CompanionInstallOptions) error {
	f.installedArg = &opts
	return f.uninstallErr
}

var testCompanions = map[string]*fakeTestCompanion{}

func registerTestCompanions(t *testing.T) {
	t.Helper()
	defs := map[string]*fakeTestCompanion{
		"plugtest-alpha": {
			spec:       agent.CompanionSpec{AgentID: "plugtest-alpha", PluginName: "alpha-spaces", Version: "1.2.3", Runtime: "node", Capabilities: []string{"lifecycle", "usage"}},
			installed:  true,
			path:       "/home/u/.test/alpha",
			version:    "1.2.3",
			installRes: agent.CompanionInstallResult{Path: "/home/u/.test/alpha", Version: "1.2.3"},
		},
		"plugtest-beta": {
			spec:      agent.CompanionSpec{AgentID: "plugtest-beta", PluginName: "beta-spaces", Version: "0.1.0", Runtime: "binary", Capabilities: []string{"lifecycle"}},
			installed: false,
			path:      "",
			version:   "0.1.0",
		},
		"plugtest-gamma": {
			spec:       agent.CompanionSpec{AgentID: "plugtest-gamma", PluginName: "gamma-spaces", Version: "2.0.0", Runtime: "node"},
			installed:  true,
			path:       "/home/u/.test/gamma",
			version:    "2.0.0",
			installRes: agent.CompanionInstallResult{Path: "/home/u/.test/gamma", Version: "2.0.0", Updated: true},
		},
	}
	for id, cp := range defs {
		testCompanions[id] = cp
		agent.DefaultRegistry().Register(&agent.BasePlugin{
			PluginSpec:            agent.Spec{ID: id},
			CustomCompanionPlugin: cp,
		})
	}
}

func pluginsTestEnv() Env {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)
	env.Getenv = func(k string) string {
		if k == "HOME" {
			return "/home/u"
		}
		return ""
	}
	return env
}

func TestPluginsList(t *testing.T) {
	registerTestCompanions(t)
	env := pluginsTestEnv()
	if code := Run([]string{"plugins"}, env); code != ExitOK {
		t.Fatalf("plugins bare = %d: %s", code, stderrOf(t, env))
	}
	got := stdoutOf(t, env)
	for _, want := range []string{"plugtest-alpha", "alpha-spaces", "1.2.3"} {
		if !strings.Contains(got, want) {
			t.Errorf("list missing %q in:\n%s", want, got)
		}
	}

	env2 := pluginsTestEnv()
	if code := Run([]string{"plugins", "list", "--json"}, env2); code != ExitOK {
		t.Fatalf("list --json = %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdoutOf(t, env2))), &rows); err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]map[string]any{}
	for _, r := range rows {
		if a, _ := r["agent"].(string); a != "" {
			byAgent[a] = r
		}
	}
	alpha, ok := byAgent["plugtest-alpha"]
	if !ok {
		t.Fatalf("alpha missing in %v", byAgent)
	}
	if alpha["plugin"] != "alpha-spaces" || alpha["installed"] != true || alpha["runtime"] != "node" {
		t.Errorf("alpha = %v", alpha)
	}
	beta, ok := byAgent["plugtest-beta"]
	if !ok {
		t.Fatalf("beta missing in %v", byAgent)
	}
	if beta["installed"] != false {
		t.Errorf("beta installed = %v, want false", beta["installed"])
	}
}

func TestPluginsStatus(t *testing.T) {
	registerTestCompanions(t)
	env := pluginsTestEnv()
	env.PluginLive = func() map[string]PluginLiveState {
		return map[string]PluginLiveState{
			"plugtest-alpha": {Fresh: 2, Total: 2},
			"plugtest-gamma": {Fresh: 0, Total: 1},
		}
	}
	if code := Run([]string{"plugins", "status"}, env); code != ExitOK {
		t.Fatalf("status = %d: %s", code, stderrOf(t, env))
	}
	got := stdoutOf(t, env)
	for _, want := range []string{"plugtest-alpha", "active", "plugtest-gamma", "stale", "plugtest-beta", "fallback"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q in:\n%s", want, got)
		}
	}

	env2 := pluginsTestEnv()
	env2.PluginLive = func() map[string]PluginLiveState {
		return map[string]PluginLiveState{"plugtest-alpha": {Fresh: 1, Total: 1}}
	}
	if code := Run([]string{"plugin", "status", "--json"}, env2); code != ExitOK {
		t.Fatalf("alias status --json = %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdoutOf(t, env2))), &rows); err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]map[string]any{}
	for _, r := range rows {
		if a, _ := r["agent"].(string); a != "" {
			byAgent[a] = r
		}
	}
	if byAgent["plugtest-alpha"]["state"] != "active" {
		t.Errorf("alpha state = %v", byAgent["plugtest-alpha"])
	}
	if byAgent["plugtest-gamma"]["state"] != "installed" {
		t.Errorf("gamma state = %v, want installed (no live records)", byAgent["plugtest-gamma"])
	}
	if byAgent["plugtest-beta"]["state"] != "fallback" {
		t.Errorf("beta state = %v, want fallback", byAgent["plugtest-beta"])
	}
}

func TestPluginsInstallUninstall(t *testing.T) {
	registerTestCompanions(t)
	alpha := testCompanions["plugtest-alpha"]

	env := pluginsTestEnv()
	if code := Run([]string{"plugins", "install", "plugtest-alpha"}, env); code != ExitOK {
		t.Fatalf("install = %d: %s", code, stderrOf(t, env))
	}
	if got := stdoutOf(t, env); !strings.Contains(got, "plugtest-alpha") || !strings.Contains(got, "installed") {
		t.Errorf("install out = %q", got)
	}
	if alpha.installedArg == nil || alpha.installedArg.HomeDir != "/home/u" || alpha.installedArg.Force {
		t.Errorf("install opts = %+v", alpha.installedArg)
	}

	env2 := pluginsTestEnv()
	if code := Run([]string{"plugins", "install", "plugtest-gamma", "--force"}, env2); code != ExitOK {
		t.Fatalf("install --force = %d", code)
	}
	if got := stdoutOf(t, env2); !strings.Contains(got, "updated") {
		t.Errorf("updated install out = %q", got)
	}
	if !testCompanions["plugtest-gamma"].installedArg.Force {
		t.Errorf("force flag not passed through")
	}

	env3 := pluginsTestEnv()
	if code := Run([]string{"plugins", "install", "all", "--home", t.TempDir()}, env3); code != ExitOK {
		t.Fatalf("install all = %d: %s", code, stderrOf(t, env3))
	}

	env4 := pluginsTestEnv()
	if code := Run([]string{"plugins", "uninstall", "plugtest-alpha"}, env4); code != ExitOK {
		t.Fatalf("uninstall = %d: %s", code, stderrOf(t, env4))
	}
	if got := stdoutOf(t, env4); !strings.Contains(got, "uninstalled") {
		t.Errorf("uninstall out = %q", got)
	}

	env5 := pluginsTestEnv()
	if code := Run([]string{"plugins", "install", "nope"}, env5); code != ExitUsage {
		t.Errorf("unknown agent = %d, want usage", code)
	}
	if code := Run([]string{"plugins", "install"}, pluginsTestEnv()); code != ExitUsage {
		t.Errorf("missing target should be usage")
	}

	alpha.installErr = errors.New("disk full")
	defer func() { alpha.installErr = nil }()
	env6 := pluginsTestEnv()
	if code := Run([]string{"plugins", "install", "plugtest-alpha"}, env6); code != ExitError {
		t.Errorf("failing install = %d, want error", code)
	}
}

func TestPluginsHelpAndUnknown(t *testing.T) {
	env := pluginsTestEnv()
	if code := Run([]string{"plugins", "--help"}, env); code != ExitOK {
		t.Errorf("plugins --help = %d", code)
	}
	if code := Run([]string{"plugins", "bogus"}, env); code != ExitUsage {
		t.Errorf("bogus subcommand = %d, want usage", code)
	}
	if code := Run([]string{"help", "plugins"}, env); code != ExitOK {
		t.Errorf("help plugins = %d", code)
	}
}

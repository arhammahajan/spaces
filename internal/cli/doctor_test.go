package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func doctorEnv(fc *FakeClient) Env {
	env := testEnvWithGetter(fc, func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1/default,2,0"
		}
		return ""
	})
	env.LookPath = func(file string) (string, error) {
		switch file {
		case "tmux":
			return "/usr/bin/tmux", nil
		case "spaces":
			return "/usr/bin/zj", nil
		default:
			return "", errTestBoom
		}
	}
	env.HookHealth = func() (string, error) { return "3 panes tracked", nil }
	return env
}

func TestDoctorHealthy(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := doctorEnv(fc)
	if code := Run([]string{"doctor"}, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	got := stdoutOf(t, env)
	for _, want := range []string{"tmux:", "server:", "hooks:", "3 panes tracked"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestDoctorJSON(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := doctorEnv(fc)
	if code := Run([]string{"doctor", "--json"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	var report struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdoutOf(t, env))), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.Checks) != 6 {
		t.Errorf("report = %+v", report)
	}
}

func TestDoctorUnhealthy(t *testing.T) {
	// Missing tmux binary fails the check.
	fc := &FakeClient{Up: true}
	env := doctorEnv(fc)
	env.LookPath = func(string) (string, error) { return "", errTestBoom }
	if code := Run([]string{"doctor"}, env); code != ExitError {
		t.Errorf("missing tmux = %d", code)
	}
	// Server down fails the check.
	fc2 := &FakeClient{Up: false}
	env2 := doctorEnv(fc2)
	if code := Run([]string{"doctor", "--json"}, env2); code != ExitError {
		t.Errorf("server down = %d", code)
	}
	// Hook error surfaces but does not fail the gate.
	fc3 := &FakeClient{Up: true}
	env3 := doctorEnv(fc3)
	env3.HookHealth = func() (string, error) { return "", errTestBoom }
	if code := Run([]string{"doctor"}, env3); code != ExitOK {
		t.Errorf("hook error should not gate = %d", code)
	}
	if got := stdoutOf(t, env3); !strings.Contains(got, "hooks:") {
		t.Errorf("hooks row = %q", got)
	}
	// Nil hook seam reports unknown.
	env4 := doctorEnv(&FakeClient{Up: true})
	env4.HookHealth = nil
	if code := Run([]string{"doctor"}, env4); code != ExitOK {
		t.Errorf("nil hook seam = %d", code)
	}
	if code := Run([]string{"doctor", "extra"}, testEnv(&FakeClient{})); code != ExitUsage {
		t.Errorf("extra args = %d", code)
	}
}

func TestPopupAgentsWiring(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)
	if code := Run([]string{"popup"}, env); code != ExitError {
		t.Errorf("unwired popup = %d", code)
	}
	called := false
	env2 := testEnv(fc)
	env2.RunPopup = func(Env, []string) int {
		called = true
		return 42
	}
	if code := Run([]string{"popup", "--new"}, env2); code != 42 || !called {
		t.Errorf("popup passthrough = %d %v", code, called)
	}
	if code := Run([]string{"agents"}, testEnv(fc)); code != ExitError {
		t.Errorf("unwired agents = %d", code)
	}
	env3 := testEnv(fc)
	env3.RunAgents = func(Env, []string) int { return 7 }
	if code := Run([]string{"agents"}, env3); code != 7 {
		t.Errorf("agents passthrough = %d", code)
	}
	if code := Run([]string{"popup", "extra"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("popup extra = %d", code)
	}
}

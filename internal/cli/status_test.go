package cli

import (
	"testing"
)

func statusEnv(fc *FakeClient, tmuxPane string) Env {
	return testEnvWithGetter(fc, func(k string) string {
		if k == "TMUX_PANE" {
			return tmuxPane
		}
		return ""
	})
}

func findSet(calls []paneOptCall, option string) (string, bool) {
	for _, c := range calls {
		if c.Option == option {
			return c.Value, true
		}
	}
	return "", false
}

func TestStatusSet(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := statusEnv(fc, "%12")
	args := []string{"status", "working", "--app", "claude-code", "--profile", "work", "--kind", "auth", "--msg", "hello"}
	if code := Run(args, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	got := map[string]string{}
	for _, c := range fc.SetCalls {
		got[c.Option] = c.Value
		if c.Pane != "%12" {
			t.Errorf("wrong pane %q", c.Pane)
		}
	}
	for opt, want := range map[string]string{
		"@spaces_state":   "working",
		"@spaces_app":     "claude-code",
		"@spaces_profile": "work",
		"@spaces_kind":    "auth",
		"@spaces_msg":     "hello",
	} {
		if got[opt] != want {
			t.Errorf("%s = %q want %q (all: %v)", opt, got[opt], want, got)
		}
	}
	if _, ok := got["@spaces_ts"]; !ok {
		t.Errorf("missing @spaces_ts in %v", got)
	}
}

func TestStatusFailedAliasAndUnset(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := statusEnv(fc, "%3")
	if code := Run([]string{"status", "failed", "--kind", "", "--msg", ""}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if v, ok := findSet(fc.SetCalls, "@spaces_state"); !ok || v != "error" {
		t.Errorf("failed alias = %v %v", v, ok)
	}
	for _, opt := range []string{"@spaces_kind", "@spaces_msg"} {
		found := false
		for _, c := range fc.UnsetCalls {
			if c.Option == opt {
				found = true
			}
		}
		if !found {
			t.Errorf("expected unset %s: %+v", opt, fc.UnsetCalls)
		}
	}
}

func TestStatusClear(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := statusEnv(fc, "%3")
	if code := Run([]string{"status", "clear"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.SetCalls) != 0 {
		t.Errorf("clear should not set: %v", fc.SetCalls)
	}
	if len(fc.UnsetCalls) != 11 {
		t.Errorf("clear unsets = %v", fc.UnsetCalls)
	}
}

func TestStatusErrors(t *testing.T) {
	fc := &FakeClient{Up: true}
	if code := Run([]string{"status", "bogus"}, statusEnv(fc, "%1")); code != ExitUsage {
		t.Errorf("bad state = %d", code)
	}
	if code := Run([]string{"status", "working"}, statusEnv(fc, "")); code != ExitError {
		t.Errorf("missing pane = %d", code)
	}
	if code := Run([]string{"status"}, statusEnv(fc, "%1")); code != ExitUsage {
		t.Errorf("missing state = %d", code)
	}
	fc.SetErr = errTestBoom
	if code := Run([]string{"status", "idle"}, statusEnv(fc, "%1")); code != ExitError {
		t.Errorf("set error = %d", code)
	}
	fc2 := &FakeClient{Up: true, UnsetErr: errTestBoom}
	if code := Run([]string{"status", "clear", "--pane", "%9"}, testEnv(fc2)); code != ExitError {
		t.Errorf("unset error = %d", code)
	}
}

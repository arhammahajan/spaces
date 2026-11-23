package cli

import (
	"strings"
	"testing"
)

func agentEnv(fc *FakeClient, bins ...string) Env {
	env := testEnvWithGetter(fc, func(k string) string {
		if k == "PWD" {
			return "/tmp"
		}
		return ""
	})
	env.LookPath = func(file string) (string, error) {
		for _, b := range bins {
			if file == b {
				return "/bin/" + file, nil
			}
		}
		return "", errTestBoom
	}
	return env
}

func TestAgentPlain(t *testing.T) {
	fc := &FakeClient{Up: true, Cur: "s1", CurOK: true}
	dir := t.TempDir()
	env := agentEnv(fc, "codex")
	env.Getenv = func(k string) string {
		if k == "PWD" {
			return dir
		}
		return ""
	}
	args := []string{"agent", "codex", "--", "--model", "x"}
	if code := Run(args, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if len(fc.NewWindows) != 1 {
		t.Fatalf("windows = %+v", fc.NewWindows)
	}
	w := fc.NewWindows[0]
	if w.Session != "s1" || w.CWD != dir || w.Label != "codex" {
		t.Errorf("window = %+v", w)
	}
	want := []string{"tmux-spaces", "__shim", "codex", "codex", "--model", "x"}
	if strings.Join(w.Cmd, " ") != strings.Join(want, " ") {
		t.Errorf("cmd = %v want %v", w.Cmd, want)
	}
}

func TestAgentCursorMapping(t *testing.T) {
	fc := &FakeClient{Up: true, Cur: "s1", CurOK: true}
	env := agentEnv(fc, "cursor-agent")
	env.Getenv = func(string) string { return t.TempDir() }
	if code := Run([]string{"agent", "cursor"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.NewWindows) != 1 {
		t.Fatalf("windows = %+v", fc.NewWindows)
	}
	if got := strings.Join(fc.NewWindows[0].Cmd, " "); !strings.Contains(got, "cursor-agent cursor-agent") {
		t.Errorf("cmd = %q", got)
	}
}

func TestAgentProfile(t *testing.T) {
	fc := &FakeClient{Up: true, Cur: "s1", CurOK: true}
	env := agentEnv(fc)
	env.Getenv = func(string) string { return t.TempDir() }
	args := []string{"agent", "codex", "--profile", "codex_router", "--session", "s2", "--", "--extra", "1"}
	if code := Run(args, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if len(fc.NewWindows) != 1 {
		t.Fatalf("windows = %+v", fc.NewWindows)
	}
	w := fc.NewWindows[0]
	if w.Session != "s2" || w.Label != "codex_router" {
		t.Errorf("window = %+v", w)
	}
	want := "tmux-spaces __shim --profile codex_router codex tmux-spaces codex profile codex_router --extra 1"
	if strings.Join(w.Cmd, " ") != want {
		t.Errorf("cmd = %q want %q", strings.Join(w.Cmd, " "), want)
	}
}

func TestAgentErrors(t *testing.T) {
	fc := &FakeClient{Up: true, Cur: "s1", CurOK: true}
	env := agentEnv(fc)
	env.Getenv = func(string) string { return t.TempDir() }
	if code := Run([]string{"agent", "codex"}, env); code != ExitError {
		t.Errorf("missing binary = %d", code)
	}
	if code := Run([]string{"agent"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing name = %d", code)
	}
	fc2 := &FakeClient{Up: true}
	env2 := agentEnv(fc2, "codex")
	env2.Getenv = func(string) string { return t.TempDir() }
	if code := Run([]string{"agent", "codex"}, env2); code != ExitError {
		t.Errorf("no session = %d", code)
	}
	env3 := agentEnv(&FakeClient{Up: true, Cur: "s1", CurOK: true}, "codex")
	env3.Getenv = func(string) string { return "" }
	if code := Run([]string{"agent", "codex", "--cwd", "/nonexistent-xyz"}, env3); code != ExitError {
		t.Errorf("bad cwd = %d", code)
	}
	fc5 := &FakeClient{Up: true, Cur: "s1", CurOK: true, NewWinErr: errTestBoom}
	env5 := agentEnv(fc5, "codex")
	env5.Getenv = func(string) string { return t.TempDir() }
	if code := Run([]string{"agent", "codex"}, env5); code != ExitError {
		t.Errorf("new window error = %d", code)
	}
}

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/tmux"
)

// advancingEnv returns an env whose clock advances one second per Sleep,
// so wait tests terminate deterministically.
func advancingEnv(fc *FakeClient, snaps []tmux.ServerSnapshot) Env {
	env := testEnv(fc)
	now := time.Unix(1700000000, 0)
	calls := 0
	env.Now = func() time.Time { return now }
	env.Sleep = func(time.Duration) {
		calls++
		now = now.Add(time.Second)
		if calls < len(snaps) {
			fc.Snap = snaps[calls]
		}
	}
	return env
}

func TestWaitImmediate(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%5", 5, "done", "pi"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"wait", "%5", "--until", "done"}, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if got := strings.TrimSpace(stdoutOf(t, env)); got != "done" {
		t.Errorf("wait printed %q", got)
	}
	// Numeric id without % also resolves.
	env2 := testEnv(fc)
	if code := Run([]string{"wait", "5", "--until", "done"}, env2); code != ExitOK {
		t.Fatalf("numeric = %d", code)
	}
}

func TestWaitTransitions(t *testing.T) {
	fc := &FakeClient{Up: true}
	working := tmux.ServerSnapshot{Panes: []tmux.Pane{makePane("s", "w", "%5", 5, "working", "pi")}}
	needs := tmux.ServerSnapshot{Panes: []tmux.Pane{makePane("s", "w", "%5", 5, "blocked", "pi")}}
	fc.Snap = working
	env := advancingEnv(fc, []tmux.ServerSnapshot{working, needs})
	if code := Run([]string{"wait", "%5", "--until", "needs_you", "--timeout", "30s"}, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if got := strings.TrimSpace(stdoutOf(t, env)); got != "needs" {
		t.Errorf("wait printed %q", got)
	}
}

func TestWaitTimeout(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{makePane("s", "w", "%5", 5, "working", "pi")}}
	env := advancingEnv(fc, nil)
	// Clock advances 1s per poll; timeout 2s lapses quickly.
	if code := Run([]string{"wait", "%5", "--until", "done", "--timeout", "2s"}, env); code != ExitError {
		t.Fatalf("code = %d", code)
	}
	if got := stderrOf(t, env); !strings.Contains(got, "timed out") || !strings.Contains(got, "working") {
		t.Errorf("stderr = %q", got)
	}
}

func TestWaitAnyAndSession(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%5", 5, "error", "pi"),
		makePane("s", "w", "%6", 6, "idle", ""),
	}}
	env := testEnv(fc)
	if code := Run([]string{"wait", "s", "--until", "any"}, env); code != ExitOK {
		t.Fatalf("session any = %d", code)
	}
	env2 := testEnv(fc)
	if code := Run([]string{"wait", "s", "--until", "failed"}, env2); code != ExitOK {
		t.Fatalf("session failed = %d", code)
	}
	if code := Run([]string{"wait", "%9", "--timeout", "1"}, testEnv(fc)); code != ExitError {
		t.Errorf("missing pane should time out")
	}
}

func TestWaitUsage(t *testing.T) {
	fc := &FakeClient{Up: true}
	if code := Run([]string{"wait", "%1", "--until", "bogus"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("bad state = %d", code)
	}
	if code := Run([]string{"wait", "%1", "--timeout", "bogus"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("bad timeout = %d", code)
	}
	if code := Run([]string{"wait"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing target = %d", code)
	}
	fc.SnapErr = errTestBoom
	if code := Run([]string{"wait", "%1", "--timeout", "1s"}, testEnv(fc)); code != ExitError {
		t.Errorf("snapshot error = %d", code)
	}
}

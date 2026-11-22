package cli

import (
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func TestNextPicksHottest(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("b", "w", "%9", 9, "done", "pi"),
		makePane("a", "w", "%3", 3, "error", "codex"),
		makePane("a", "w", "%4", 4, "blocked", "claude-code"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"next"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	// NeedsYou (%4) beats Failed (%3) beats Done (%9).
	if len(fc.Switched) != 1 || fc.Switched[0] != "a:@1.%4" {
		t.Errorf("switched = %v", fc.Switched)
	}
	if got := strings.TrimSpace(stdoutOf(t, env)); got != "a:@1.%4" {
		t.Errorf("printed = %q", got)
	}
}

func TestNextFailedOverDone(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%9", 9, "done", "pi"),
		makePane("s", "w", "%3", 3, "failed", "codex"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"next"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.Switched) != 1 || fc.Switched[0] != "s:@1.%3" {
		t.Errorf("switched = %v", fc.Switched)
	}
}

func TestNextNone(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%1", 1, "idle", ""),
		makePane("s", "w", "%2", 2, "working", "pi"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"next"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.Switched) != 0 {
		t.Errorf("switched = %v", fc.Switched)
	}
	if got := stderrOf(t, env); !strings.Contains(got, "no attention panes") {
		t.Errorf("stderr = %q", got)
	}
}

func TestNextErrors(t *testing.T) {
	if code := Run([]string{"next", "extra"}, testEnv(&FakeClient{})); code != ExitUsage {
		t.Errorf("extra args = %d", code)
	}
	fc := &FakeClient{Up: true, SnapErr: errTestBoom}
	if code := Run([]string{"next"}, testEnv(fc)); code != ExitError {
		t.Errorf("snapshot error = %d", code)
	}
	fc2 := &FakeClient{Up: true, SwitchErr: errTestBoom}
	fc2.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{makePane("s", "w", "%1", 1, "done", "pi")}}
	if code := Run([]string{"next"}, testEnv(fc2)); code != ExitError {
		t.Errorf("switch error = %d", code)
	}
}

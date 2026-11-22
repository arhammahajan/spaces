package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func serverSnapshotForStatus() tmux.ServerSnapshot {
	return tmux.ServerSnapshot{
		CurrentSession: "s1",
		Panes: []tmux.Pane{
			makePane("s1", "w", "%1", 1, "blocked", "claude-code"),
			makePane("s1", "w", "%2", 2, "blocked", "codex"),
			makePane("s1", "w", "%3", 3, "working", "pi"),
			makePane("s1", "w", "%4", 4, "done", "gemini"),
			makePane("s1", "w", "%5", 5, "done", "gemini"),
			makePane("s1", "w", "%6", 6, "done", "gemini"),
			makePane("s1", "w", "%7", 7, "idle", ""),
		},
	}
}

func TestStatusBarStyled(t *testing.T) {
	fc := &FakeClient{Up: true, Snap: serverSnapshotForStatus()}
	env := testEnv(fc)
	if code := Run([]string{"status-bar"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	got := stdoutOf(t, env)
	for _, want := range []string{
		"#[fg=#89B4FA]●2#[default]",
		"#[fg=#F9E2AF]◐1#[default]",
		"#[fg=#A6E3A1]✓3#[default]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "✗") {
		t.Errorf("zero failed bucket should be omitted: %q", got)
	}
}

func TestStatusBarPlain(t *testing.T) {
	fc := &FakeClient{Up: true, Snap: serverSnapshotForStatus()}
	env := testEnv(fc)
	if code := Run([]string{"status-bar", "--plain"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	got := strings.TrimSpace(stdoutOf(t, env))
	if got != "●2 ◐1 ✓3" {
		t.Errorf("plain = %q", got)
	}
}

func TestStatusBarFailed(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%1", 1, "error", "pi"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"status-bar", "--plain"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := strings.TrimSpace(stdoutOf(t, env)); got != "✗1" {
		t.Errorf("plain = %q", got)
	}
	envStyled := testEnv(fc)
	if code := Run([]string{"status-bar"}, envStyled); code != ExitOK {
		t.Fatalf("styled code = %d", code)
	}
	if got := strings.TrimSpace(stdoutOf(t, envStyled)); got != "#[fg=#F38BA8]✗1#[default]" {
		t.Errorf("styled = %q, want #[fg=#F38BA8]✗1#[default]", got)
	}
}

func TestStatusBarJSON(t *testing.T) {
	fc := &FakeClient{Up: true, Snap: serverSnapshotForStatus()}
	env := testEnv(fc)
	if code := Run([]string{"status-bar", "--json"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	var c statusCounts
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdoutOf(t, env))), &c); err != nil {
		t.Fatal(err)
	}
	if c.Needs != 2 || c.Working != 1 || c.Done != 3 || c.Idle != 1 || c.Failed != 0 {
		t.Errorf("counts = %+v", c)
	}
}

func TestStatusBarEmptyWhenIdle(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s", "w", "%1", 1, "idle", ""),
		makePane("s", "w", "%2", 2, "", ""),
	}}
	env := testEnv(fc)
	if code := Run([]string{"status-bar"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := stdoutOf(t, env); got != "" {
		t.Errorf("idle output = %q", got)
	}
}

func TestStatusBarError(t *testing.T) {
	fc := &FakeClient{SnapErr: errTestBoom}
	env := testEnv(fc)
	if code := Run([]string{"status-bar"}, env); code != ExitError {
		t.Errorf("code = %d", code)
	}
	if got := stdoutOf(t, env); got != "" {
		t.Errorf("stdout on error = %q", got)
	}
}

package cli

import (
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func spaceTestClient() *FakeClient {
	fc := &FakeClient{
		Up:    true,
		Cur:   "alpha",
		CurOK: true,
		Sessions: []tmux.Session{
			{Name: "alpha"}, {Name: "beta"},
		},
		Windows: map[string][]tmux.Window{
			"alpha": {{SessionName: "alpha", ID: "@1"}, {SessionName: "alpha", ID: "@2"}},
			"beta":  {{SessionName: "beta", ID: "@1"}},
		},
		Panes: map[string][]tmux.Pane{
			"alpha": {
				makePane("alpha", "w", "%1", 1, "blocked", "claude-code"),
				makePane("alpha", "w", "%2", 2, "working", "codex"),
			},
			"beta": {
				makePane("beta", "w", "%3", 3, "done", "pi"),
				makePane("beta", "w", "%4", 4, "error", "gemini"),
			},
		},
	}
	return fc
}

func TestSpaceList(t *testing.T) {
	fc := spaceTestClient()
	env := testEnv(fc)
	if code := Run([]string{"space", "list"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	got := stdoutOf(t, env)
	for _, want := range []string{"SPACE", "alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Current session gets the marker.
	lines := strings.Split(got, "\n")
	found := false
	for _, l := range lines {
		if strings.Contains(l, "alpha") && strings.HasPrefix(l, "*") {
			found = true
		}
	}
	if !found {
		t.Errorf("current marker missing in:\n%s", got)
	}
	// Alias works.
	env2 := testEnv(fc)
	if code := Run([]string{"spaces"}, env2); code != ExitOK {
		t.Fatalf("spaces alias = %d", code)
	}
}

func TestSpaceListSkipsFailingSessions(t *testing.T) {
	fc := spaceTestClient()
	fc.WinErr = errTestBoom
	env := testEnv(fc)
	// ListWindows fails for every session: table still prints the header.
	if code := Run([]string{"space", "list"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := stdoutOf(t, env); !strings.Contains(got, "SPACE") {
		t.Errorf("header missing: %q", got)
	}
}

func TestSpaceCurrent(t *testing.T) {
	fc := spaceTestClient()
	env := testEnv(fc)
	if code := Run([]string{"space", "current"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := strings.TrimSpace(stdoutOf(t, env)); got != "alpha" {
		t.Errorf("current = %q", got)
	}
	fc.CurOK = false
	env2 := testEnv(fc)
	if code := Run([]string{"space", "current"}, env2); code != ExitError {
		t.Errorf("no current = %d", code)
	}
}

func TestSpaceNew(t *testing.T) {
	fc := spaceTestClient()
	dir := t.TempDir()
	env := testEnvWithGetter(fc, func(k string) string {
		if k == "PWD" {
			return dir
		}
		return ""
	})
	if code := Run([]string{"space", "new", "gamma"}, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if len(fc.NewSessions) != 1 || fc.NewSessions[0].Name != "gamma" || fc.NewSessions[0].CWD != dir {
		t.Errorf("new = %+v", fc.NewSessions)
	}
	// Duplicate rejected.
	env2 := testEnv(fc)
	if code := Run([]string{"space", "new", "alpha", dir}, env2); code != ExitError {
		t.Errorf("duplicate = %d", code)
	}
	// Bad name and bad dir rejected.
	env3 := testEnv(fc)
	if code := Run([]string{"space", "new", "a:b", dir}, env3); code != ExitUsage {
		t.Errorf("bad name = %d", code)
	}
	env4 := testEnv(fc)
	if code := Run([]string{"space", "new", "delta", "/nonexistent-dir-xyz"}, env4); code != ExitError {
		t.Errorf("bad dir = %d", code)
	}
	if code := Run([]string{"space", "new"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing name should be usage")
	}
}

func TestSpaceSwitch(t *testing.T) {
	fc := spaceTestClient()
	env := testEnv(fc)
	if code := Run([]string{"space", "switch", "beta"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.Switched) != 1 || fc.Switched[0] != "beta" {
		t.Errorf("switched = %v", fc.Switched)
	}
	env2 := testEnv(fc)
	if code := Run([]string{"space", "open", "nope"}, env2); code != ExitError {
		t.Errorf("unknown = %d", code)
	}
	if code := Run([]string{"space", "switch"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing name should be usage")
	}
}

func TestSpaceRename(t *testing.T) {
	fc := spaceTestClient()
	env := testEnv(fc)
	if code := Run([]string{"space", "rename", "ALPHA"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.Renamed) != 1 || fc.Renamed[0] != (renameCall{"alpha", "ALPHA"}) {
		t.Errorf("renamed = %+v", fc.Renamed)
	}
	env2 := testEnv(fc)
	if code := Run([]string{"space", "rename", "BETA", "beta"}, env2); code != ExitOK {
		t.Fatalf("explicit = %d", code)
	}
	if code := Run([]string{"space", "rename", "a:b"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("bad name should be usage")
	}
	if code := Run([]string{"space", "rename"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing name should be usage")
	}
}

func TestSpaceClose(t *testing.T) {
	fc := spaceTestClient()
	// Simulate the kill taking effect: beta disappears afterwards.
	fc.Sessions = []tmux.Session{{Name: "alpha"}}
	env := testEnv(fc)
	if code := Run([]string{"space", "close", "beta", "--yes"}, env); code != ExitOK {
		t.Fatalf("code = %d: %s", code, stderrOf(t, env))
	}
	if len(fc.Killed) != 1 || fc.Killed[0] != "beta" {
		t.Errorf("killed = %v", fc.Killed)
	}
	// Refuses without --yes on non-terminal stdin.
	fc2 := spaceTestClient()
	if code := Run([]string{"space", "close", "beta"}, testEnv(fc2)); code != ExitError {
		t.Errorf("no --yes = %d", code)
	}
	if len(fc2.Killed) != 0 {
		t.Errorf("should not kill: %v", fc2.Killed)
	}
	// Detects a failed kill (session still listed).
	fc3 := spaceTestClient()
	env3 := testEnv(fc3)
	if code := Run([]string{"space", "close", "beta", "-y"}, env3); code != ExitError {
		t.Errorf("failed close = %d", code)
	}
	// Unknown subcommand.
	if code := Run([]string{"space", "bogus"}, testEnv(spaceTestClient())); code != ExitUsage {
		t.Errorf("bogus sub = %d", code)
	}
	// kill alias with current default.
	fc4 := spaceTestClient()
	fc4.Sessions = []tmux.Session{{Name: "beta"}}
	if code := Run([]string{"space", "kill", "--yes"}, testEnv(fc4)); code != ExitOK {
		t.Errorf("kill current = %d", code)
	}
	if len(fc4.Killed) != 1 || fc4.Killed[0] != "alpha" {
		t.Errorf("kill current killed = %v", fc4.Killed)
	}
}

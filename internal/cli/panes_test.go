package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
)

func TestPanesTable(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{
		Panes: []tmux.Pane{
			makePane("s1", "editor", "%12", 12, "blocked", "claude-code"),
			makePane("s2", "term", "%3", 3, "idle", ""),
		},
	}
	env := testEnv(fc)
	if code := Run([]string{"panes"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	got := stdoutOf(t, env)
	for _, want := range []string{"s1", "s2", "%12", "needs", "claude-code", "editor"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPanesSessionFilter(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Panes = map[string][]tmux.Pane{
		"s9": {makePane("s9", "w", "%7", 7, "done", "pi")},
	}
	env := testEnv(fc)
	if code := Run([]string{"panes", "--session", "s9"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := stdoutOf(t, env); !strings.Contains(got, "%7") || !strings.Contains(got, "done") {
		t.Errorf("filtered = %q", got)
	}
	fc.PanesErr = errTestBoom
	if code := Run([]string{"panes", "--session", "s9"}, testEnv(fc)); code != ExitError {
		t.Errorf("list error should fail")
	}
}

func TestPanesJSON(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = tmux.ServerSnapshot{Panes: []tmux.Pane{
		makePane("s1", "w", "%12", 12, "error", "gemini"),
	}}
	env := testEnv(fc)
	if code := Run([]string{"panes", "--json"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdoutOf(t, env))), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["pane"] != "%12" || rows[0]["status"] != "failed" || rows[0]["app"] != "gemini" {
		t.Errorf("rows = %v", rows)
	}
}

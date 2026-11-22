package main

import (
	"context"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/tmux"
	"github.com/arhammahajan/spaces/internal/tui"
)

func TestParseTabsOptions(t *testing.T) {
	opts := parseTabsOptions([]string{"--session", "model-router", "--query", "claude"})
	if opts.InitialMode != tui.ModeTabs || opts.Target != "model-router" || opts.InitialQuery != "claude" {
		t.Fatalf("unexpected opts: %+v", opts)
	}
	opts2 := parseTabsOptions([]string{"--session=work", "--query=tests"})
	if opts2.InitialMode != tui.ModeTabs || opts2.Target != "work" || opts2.InitialQuery != "tests" {
		t.Fatalf("unexpected opts2: %+v", opts2)
	}
	opts3 := parseTabsOptions([]string{"-s", "dev", "-q", "auth", "--new"})
	if opts3.InitialMode != tui.ModeTabs || opts3.Target != "dev" || opts3.InitialQuery != "auth" || !opts3.OpenNew {
		t.Fatalf("unexpected opts3: %+v", opts3)
	}
	opts4 := parseTabsOptions([]string{"-q=auth"})
	if opts4.InitialMode != tui.ModeTabs || opts4.Target != "" || opts4.InitialQuery != "auth" || opts4.OpenNew {
		t.Fatalf("unexpected opts4: %+v", opts4)
	}
	opts5 := parseTabsOptions([]string{"-s=dev"})
	if opts5.InitialMode != tui.ModeTabs || opts5.Target != "dev" || opts5.InitialQuery != "" || opts5.OpenNew {
		t.Fatalf("unexpected opts5: %+v", opts5)
	}
	opts6 := parseTabsOptions([]string{"-s=dev", "-q=auth", "--new"})
	if opts6.InitialMode != tui.ModeTabs || opts6.Target != "dev" || opts6.InitialQuery != "auth" || !opts6.OpenNew {
		t.Fatalf("unexpected opts6: %+v", opts6)
	}
}

func TestTUIBackendWindowManageAndPreview(t *testing.T) {
	var calls [][]string
	runner := &fakeRunner{
		handler: func(args []string) (string, string, int, error) {
			calls = append(calls, append([]string(nil), args...))
			switch args[0] {
			case "capture-pane":
				return "\x1b[32mPASS\x1b[0m ok pkg/tabs\n", "", 0, nil
			default:
				return "", "", 0, nil
			}
		},
	}
	client := tmux.NewClient(runner)
	b := newTUIBackend(client)

	if err := b.NewWindow(context.Background(), "work", "/home/u/proj", "tests"); err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if err := b.KillWindow(context.Background(), "@7"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	previews := b.CapturePreviews(context.Background(), []string{"%11"}, 0)
	if got := previews["%11"]; !strings.Contains(got, "PASS") {
		t.Fatalf("CapturePreviews = %q, want PASS", got)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 tmux calls, got %d: %v", len(calls), calls)
	}
	if got := strings.Join(calls[0], " "); got != "new-window -t work: -c /home/u/proj -n tests" {
		t.Errorf("new-window call = %q", got)
	}
	if got := strings.Join(calls[1], " "); got != "kill-window -t @7" {
		t.Errorf("kill-window call = %q", got)
	}
	if got := strings.Join(calls[2], " "); got != "capture-pane -p -e -N -t %11" {
		t.Errorf("capture-pane call = %q", got)
	}
}

package agents

import (
	"errors"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// resetAccessSeams restores the injectable picker seams after a test.
func resetAccessSeams(t *testing.T) {
	t.Helper()
	accessIn = strings.NewReader("")
	accessOut = &strings.Builder{}
	stdinIsTerminal = func() bool { return true }
	t.Setenv("HARNESS_AGENT", "")
	t.Setenv("HARNESS_ACCESS", "")
}

func TestApplyAccessLevelSkipsForT3(t *testing.T) {
	resetAccessSeams(t)
	t.Setenv("HARNESS_AGENT", "claude")
	// Even an explicit level must not inject flags into a T3 invocation.
	t.Setenv("HARNESS_ACCESS", "full")
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("T3 invocation must be untouched: %v", err)
	}
	if len(spec.PrependArgs) != 0 {
		t.Fatalf("T3 invocation got injected args: %v", spec.PrependArgs)
	}
}

func TestApplyAccessLevelSkipsWithoutLevels(t *testing.T) {
	resetAccessSeams(t)
	spec := harnessenv.LaunchSpec{Agent: "pi"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("agents without levels must pass through: %v", err)
	}
	if len(spec.PrependArgs) != 0 {
		t.Fatalf("pi got injected args: %v", spec.PrependArgs)
	}
}

func TestApplyAccessLevelEnvPreselect(t *testing.T) {
	resetAccessSeams(t)
	stdinIsTerminal = func() bool { return false } // preselect works headless
	t.Setenv("HARNESS_ACCESS", "full")
	spec := harnessenv.LaunchSpec{Agent: "claude", PrependArgs: []string{"--session-preexisting"}}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("preselect failed: %v", err)
	}
	want := []string{"--dangerously-skip-permissions", "--session-preexisting"}
	if strings.Join(spec.PrependArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("spec.PrependArgs = %v, want %v", spec.PrependArgs, want)
	}
}

func TestApplyAccessLevelUnknownEnvPreselect(t *testing.T) {
	resetAccessSeams(t)
	t.Setenv("HARNESS_ACCESS", "bogus")
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	err := ApplyAccessLevel(&spec)
	if err == nil {
		t.Fatal("unknown level must be a usage error")
	}
	if !strings.Contains(err.Error(), "available: read-only, default, auto, full") {
		t.Fatalf("error must list available levels: %v", err)
	}
}

func TestApplyAccessLevelSkipsHeadless(t *testing.T) {
	resetAccessSeams(t)
	stdinIsTerminal = func() bool { return false }
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("headless launch must pass through: %v", err)
	}
	if len(spec.PrependArgs) != 0 {
		t.Fatalf("headless launch got injected args: %v", spec.PrependArgs)
	}
}

func TestPromptSelectsDefaultOnEnter(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("\n")
	spec := harnessenv.LaunchSpec{Agent: "codex"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("Enter must pick the default level: %v", err)
	}
	if len(spec.PrependArgs) != 0 {
		t.Fatalf("default level must add no flags, got %v", spec.PrependArgs)
	}
	out := accessOut.(*strings.Builder).String()
	if !strings.Contains(out, "Access level for codex:") {
		t.Fatalf("prompt missing header: %q", out)
	}
	if !strings.Contains(out, "> 2) Default") {
		t.Fatalf("default level must be marked, got %q", out)
	}
	if !strings.Contains(out, "4) Full access") {
		t.Fatalf("list must show all levels, got %q", out)
	}
}

func TestPromptNumberSelection(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("4\n")
	spec := harnessenv.LaunchSpec{Agent: "codex"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	want := []string{"--dangerously-bypass-approvals-and-sandbox"}
	if strings.Join(spec.PrependArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("spec.PrependArgs = %v, want %v", spec.PrependArgs, want)
	}
}

func TestPromptIDAndRetry(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("bogus\nread-only\n")
	spec := harnessenv.LaunchSpec{Agent: "codex"}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("retry then valid ID must succeed: %v", err)
	}
	want := []string{"--sandbox", "read-only"}
	if strings.Join(spec.PrependArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("spec.PrependArgs = %v, want %v", spec.PrependArgs, want)
	}
	out := accessOut.(*strings.Builder).String()
	if !strings.Contains(out, `Invalid choice "bogus"`) {
		t.Fatalf("invalid input must re-prompt, got %q", out)
	}
}

func TestPromptCancel(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("q\n")
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	err := ApplyAccessLevel(&spec)
	if !errors.Is(err, ErrAccessCancelled) {
		t.Fatalf("q must cancel, got %v", err)
	}
}

func TestPromptEOFCancels(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("")
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	err := ApplyAccessLevel(&spec)
	if !errors.Is(err, ErrAccessCancelled) {
		t.Fatalf("EOF must cancel instead of guessing, got %v", err)
	}
}

func TestPromptLayeringKeepsProfileArgs(t *testing.T) {
	resetAccessSeams(t)
	accessIn = strings.NewReader("full\n")
	spec := harnessenv.LaunchSpec{
		Agent:       "kimi",
		PrependArgs: []string{"--profile", "dsh-tui"},
		Env:         map[string]string{"K": "V"},
	}
	if err := ApplyAccessLevel(&spec); err != nil {
		t.Fatalf("selection failed: %v", err)
	}
	want := []string{"--auto", "--profile", "dsh-tui"}
	if strings.Join(spec.PrependArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("spec.PrependArgs = %v, want %v", spec.PrependArgs, want)
	}
	if spec.Env["K"] != "V" {
		t.Fatal("selection must not clobber existing env")
	}
	// The chosen level must exist in the agent's table.
	if _, ok := FindAccessLevel("kimi", "full"); !ok {
		t.Fatal("kimi full level must exist")
	}
}

func TestPreselectNeverPrompts(t *testing.T) {
	resetAccessSeams(t)
	// A terminal is attached and no HARNESS_ACCESS is set: preselect must
	// stay silent and leave the spec untouched (describe and other
	// non-launch resolutions rely on this).
	spec := harnessenv.LaunchSpec{Agent: "claude"}
	if err := PreselectAccessLevel(&spec); err != nil {
		t.Fatalf("preselect failed: %v", err)
	}
	if out := accessOut.(*strings.Builder).String(); out != "" {
		t.Fatalf("preselect must not prompt, got %q", out)
	}
	if len(spec.PrependArgs) != 0 {
		t.Fatalf("preselect must not inject without HARNESS_ACCESS: %v", spec.PrependArgs)
	}
}

func TestPreselectUnknownLevelIsUsageError(t *testing.T) {
	resetAccessSeams(t)
	t.Setenv("HARNESS_ACCESS", "bogus")
	if err := PreselectAccessLevel(&harnessenv.LaunchSpec{Agent: "claude"}); err == nil {
		t.Fatal("unknown HARNESS_ACCESS must fail preselect too")
	}
}

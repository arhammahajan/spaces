package cli

import (
	"strings"
	"testing"
)

func shimEnv(fc *FakeClient, childCode int) Env {
	env := testEnvWithGetter(fc, func(k string) string {
		if k == "TMUX_PANE" {
			return "%12"
		}
		return ""
	})
	var ran []string
	_ = ran
	env.RunChild = func(bin string, args []string) int {
		return childCode
	}
	return env
}

func TestShimSuccess(t *testing.T) {
	fc := &FakeClient{Up: true}
	var ranBin string
	var ranArgs []string
	env := shimEnv(fc, 0)
	env.RunChild = func(bin string, args []string) int {
		ranBin, ranArgs = bin, args
		return 0
	}
	if code := Run([]string{"__shim", "claude", "/bin/claude", "-p", "hi"}, env); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if ranBin != "/bin/claude" || strings.Join(ranArgs, " ") != "-p hi" {
		t.Errorf("child = %s %v", ranBin, ranArgs)
	}
	states := []string{}
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_state" {
			states = append(states, c.Value)
		}
	}
	want := []string{"working", "idle", "done"}
	if strings.Join(states, ",") != strings.Join(want, ",") {
		t.Errorf("states = %v want %v", states, want)
	}
	// App is canonicalized.
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_app" && c.Value != "claude-code" {
			t.Errorf("app = %q", c.Value)
		}
	}
	// Done carries the finished message.
	found := false
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_msg" && c.Value == "finished" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing finished msg: %+v", fc.SetCalls)
	}
}

func TestShimFailure(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := shimEnv(fc, 3)
	if code := Run([]string{"__shim", "pi", "/bin/pi"}, env); code != 3 {
		t.Fatalf("code = %d (want child code 3)", code)
	}
	states := []string{}
	msgs := []string{}
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_state" {
			states = append(states, c.Value)
		}
		if c.Option == "@spaces_msg" {
			msgs = append(msgs, c.Value)
		}
	}
	if last := states[len(states)-1]; last != "failed" {
		t.Errorf("states = %v", states)
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "3") {
			found = true
		}
	}
	if !found {
		t.Errorf("msgs = %v", msgs)
	}
}

func TestShimHold(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := shimEnv(fc, 2)
	shelled := false
	env.RunShell = func() int {
		shelled = true
		return 7
	}
	if code := Run([]string{"__shim", "--hold", "pi", "/bin/pi"}, env); code != 7 {
		t.Fatalf("code = %d (want shell code 7)", code)
	}
	if !shelled {
		t.Errorf("hold shell did not run")
	}
}

func TestShimNoPane(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc) // TMUX_PANE empty
	ran := false
	env.RunChild = func(string, []string) int {
		ran = true
		return 0
	}
	if code := Run([]string{"__shim", "pi", "/bin/pi"}, env); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !ran {
		t.Errorf("child did not run")
	}
	if len(fc.SetCalls) != 0 {
		t.Errorf("no pane: should not set options: %v", fc.SetCalls)
	}
}

func TestShimUsage(t *testing.T) {
	fc := &FakeClient{Up: true}
	if code := Run([]string{"__shim", "onlyapp"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing bin = %d", code)
	}
	if code := Run([]string{"__shim"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing all = %d", code)
	}
	if code := Run([]string{"__shim", "--bogus", "app", "bin"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("unknown flag = %d", code)
	}
	if code := Run([]string{"__shim", "--help"}, testEnv(fc)); code != ExitOK {
		t.Errorf("help = %d", code)
	}
}

func TestShimProfileFlag(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := shimEnv(fc, 0)
	if code := Run([]string{"__shim", "--profile", "pratham", "antigravity", "/bin/agy"}, env); code != 0 {
		t.Fatalf("code = %d", code)
	}
	sawSet := false
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_profile" && c.Value == "pratham" {
			sawSet = true
		}
	}
	if !sawSet {
		t.Fatalf("expected @spaces_profile=pratham to be set: %+v", fc.SetCalls)
	}
	sawUnset := false
	for _, c := range fc.UnsetCalls {
		if c.Option == "@spaces_profile" {
			sawUnset = true
		}
	}
	if !sawUnset {
		t.Fatalf("expected @spaces_profile to be unset on exit: %+v", fc.UnsetCalls)
	}
}

func TestShimRecordsOutcomeOnExit(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := shimEnv(fc, 0)
	var recorded []OutcomeRecord
	env.RecordOutcome = func(rec OutcomeRecord) {
		recorded = append(recorded, rec)
	}
	if code := Run([]string{"__shim", "--profile", "pratham", "antigravity", "/bin/agy"}, env); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded = %+v, want 1", recorded)
	}
	if recorded[0].App != "antigravity" || recorded[0].Profile != "pratham" || recorded[0].Outcome != "done" || recorded[0].Pane != "%12" {
		t.Errorf("unexpected outcome record: %+v", recorded[0])
	}
}

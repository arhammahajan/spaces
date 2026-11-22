package cli

import (
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"
)

func TestOSCWatchProjectsAndClears(t *testing.T) {
	fc := &FakeClient{Up: true}
	msg := base64.StdEncoding.EncodeToString([]byte("Running go test"))
	stream := "hello\x1b]7501;state=working:app=claude:msg=" + msg + "\x1b\\" +
		"\x1b]7501;state=blocked:app=claude:kind=permission\x07"

	now := time.UnixMilli(1700000000000)
	env := testEnv(fc)
	env.Stdin = strings.NewReader(stream)
	env.Now = func() time.Time {
		now = now.Add(200 * time.Millisecond)
		return now
	}

	if code := Run([]string{"__osc-watch", "%7"}, env); code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", code, stderrOf(t, env))
	}

	last := map[string]string{}
	for _, c := range fc.SetCalls {
		if c.Pane != "%7" {
			t.Fatalf("unexpected pane %q", c.Pane)
		}
		last[c.Option] = c.Value
	}
	if last["@spaces_state"] != "blocked" {
		t.Fatalf("@spaces_state = %q, want blocked", last["@spaces_state"])
	}
	if last["@spaces_app"] != "claude-code" {
		t.Fatalf("@spaces_app = %q, want claude-code", last["@spaces_app"])
	}
	if last["@spaces_kind"] != "permission" {
		t.Fatalf("@spaces_kind = %q, want permission", last["@spaces_kind"])
	}
	if last["@spaces_source"] != "osc7501" {
		t.Fatalf("@spaces_source = %q, want osc7501", last["@spaces_source"])
	}
	unsetMsg := false
	for _, c := range fc.UnsetCalls {
		if c.Pane == "%7" && c.Option == "@spaces_msg" {
			unsetMsg = true
		}
	}
	if !unsetMsg {
		t.Fatalf("expected @spaces_msg to be unset on second record; UnsetCalls=%+v", fc.UnsetCalls)
	}

	// Clear resets all @spaces_* options.
	fc.SetCalls = nil
	fc.UnsetCalls = nil
	env2 := testEnv(fc)
	env2.Stdin = strings.NewReader("\x1b]7501;state=clear\x1b\\")
	if code := Run([]string{"__osc-watch", "%7"}, env2); code != ExitOK {
		t.Fatalf("clear code = %d", code)
	}
	if len(fc.SetCalls) != 0 {
		t.Fatalf("clear should not set options, got %+v", fc.SetCalls)
	}
	if len(fc.UnsetCalls) != 12 {
		t.Fatalf("clear should unset 12 options, got %d: %+v", len(fc.UnsetCalls), fc.UnsetCalls)
	}
}

type syncFakeClient struct {
	*FakeClient
	applied chan struct{}
}

func (s *syncFakeClient) SetPaneOption(pane, option, value string) error {
	err := s.FakeClient.SetPaneOption(pane, option, value)
	if option == "@spaces_source" {
		select {
		case s.applied <- struct{}{}:
		default:
		}
	}
	return err
}

func TestOSCWatchProfileLifecycle(t *testing.T) {
	fc := &FakeClient{Up: true}
	sc := &syncFakeClient{FakeClient: fc, applied: make(chan struct{}, 4)}
	pr, pw := io.Pipe()
	now := time.UnixMilli(1700000000000)
	env := testEnv(fc)
	env.Client = sc
	env.Stdin = pr
	env.Now = func() time.Time {
		now = now.Add(200 * time.Millisecond)
		return now
	}

	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"__osc-watch", "%9"}, env)
	}()

	writeAndWait := func(rec string) {
		t.Helper()
		if _, err := io.WriteString(pw, rec); err != nil {
			t.Fatalf("write %q: %v", rec, err)
		}
		select {
		case <-sc.applied:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for OSC record %q to apply", rec)
		}
	}

	writeAndWait("\x1b]7501;state=working:app=agy:profile=pratham\x1b\\")
	writeAndWait("\x1b]7501;state=idle:app=agy\x1b\\")
	writeAndWait("\x1b]7501;state=working:app=claude\x1b\\")
	_ = pw.Close()

	if code := <-done; code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", code, stderrOf(t, env))
	}
	sawSet := false
	for _, c := range fc.SetCalls {
		if c.Option == "@spaces_profile" && c.Value == "pratham" {
			sawSet = true
		}
	}
	if !sawSet {
		t.Fatalf("expected @spaces_profile=pratham to be set, SetCalls=%+v", fc.SetCalls)
	}
	// Switching to a different app without a profile clears @spaces_profile once.
	unsetCount := 0
	for _, c := range fc.UnsetCalls {
		if c.Option == "@spaces_profile" {
			unsetCount++
		}
	}
	if unsetCount != 1 {
		t.Fatalf("expected @spaces_profile to be unset once on app change, got %d (UnsetCalls=%+v)", unsetCount, fc.UnsetCalls)
	}
}

func TestOSCWatchRequiresPane(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)
	env.Getenv = func(string) string { return "" }
	if code := Run([]string{"__osc-watch"}, env); code != ExitUsage {
		t.Fatalf("code = %d, want ExitUsage", code)
	}
}

func TestOSCWatchRecordsTerminalOutcomes(t *testing.T) {
	fc := &FakeClient{Up: true}
	sc := &syncFakeClient{FakeClient: fc, applied: make(chan struct{}, 16)}
	pr, pw := io.Pipe()

	env := testEnv(fc)
	env.Client = sc
	env.Stdin = pr
	var recorded []OutcomeRecord
	env.RecordOutcome = func(rec OutcomeRecord) {
		recorded = append(recorded, rec)
	}

	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"__osc-watch", "%14"}, env)
	}()

	writeAndWait := func(rec string) {
		t.Helper()
		if _, err := io.WriteString(pw, rec); err != nil {
			t.Fatalf("write %q: %v", rec, err)
		}
		select {
		case <-sc.applied:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for OSC record %q", rec)
		}
	}

	writeAndWait("\x1b]7501;state=working:app=agy:profile=pratham\x1b\\")
	writeAndWait("\x1b]7501;state=done:app=agy:profile=pratham\x1b\\")
	writeAndWait("\x1b]7501;state=error:app=agy:profile=pratham\x1b\\")
	_ = pw.Close()

	if code := <-done; code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded = %+v, want 2 outcomes (done + failed)", recorded)
	}
	if recorded[0].Outcome != "done" || recorded[0].App != "antigravity" || recorded[0].Profile != "pratham" {
		t.Errorf("recorded[0] = %+v", recorded[0])
	}
	if recorded[1].Outcome != "failed" || recorded[1].Pane != "%14" {
		t.Errorf("recorded[1] = %+v", recorded[1])
	}
}

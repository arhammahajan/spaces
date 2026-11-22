package cli

import (
	"testing"
)

func TestPrompt(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)
	if code := Run([]string{"prompt", "%12", "hello", "world"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if len(fc.Sent) != 1 || fc.Sent[0].Target != "%12" || fc.Sent[0].Text != "hello world" {
		t.Errorf("sent = %+v", fc.Sent)
	}
	if code := Run([]string{"prompt", "%12"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing text = %d", code)
	}
	if code := Run([]string{"prompt"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing target = %d", code)
	}
	// Dashes pass through as message text.
	fc2 := &FakeClient{Up: true}
	if code := Run([]string{"prompt", "%1", "--help"}, testEnv(fc2)); code != ExitOK {
		t.Fatalf("dashed text = %d", code)
	}
	if len(fc2.Sent) != 1 || fc2.Sent[0].Text != "--help" {
		t.Errorf("sent = %+v", fc2.Sent)
	}
	if code := Run([]string{"prompt", "--help"}, testEnv(fc)); code != ExitOK {
		t.Errorf("prompt help = %d", code)
	}
	fc.SendErr = errTestBoom
	if code := Run([]string{"prompt", "%1", "x"}, testEnv(fc)); code != ExitError {
		t.Errorf("send error = %d", code)
	}
}

func TestRead(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Capture = map[string]string{"%12": "line1\nline2"}
	env := testEnv(fc)
	if code := Run([]string{"read", "%12"}, env); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if got := stdoutOf(t, env); got != "line1\nline2\n" {
		t.Errorf("read = %q", got)
	}
	// Empty capture prints nothing.
	env2 := testEnv(fc)
	if code := Run([]string{"read", "%9"}, env2); code != ExitOK {
		t.Fatalf("empty = %d", code)
	}
	if got := stdoutOf(t, env2); got != "" {
		t.Errorf("empty read = %q", got)
	}
	if code := Run([]string{"read", "%12", "--lines", "0"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("zero lines should be usage")
	}
	fc.CaptureErr = errTestBoom
	if code := Run([]string{"read", "%12"}, testEnv(fc)); code != ExitError {
		t.Errorf("capture error = %d", code)
	}
	if code := Run([]string{"read"}, testEnv(fc)); code != ExitUsage {
		t.Errorf("missing target = %d", code)
	}
}

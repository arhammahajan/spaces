package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/arhammahajan/spaces/internal/osc"
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

func TestShimEmitsOSCLifecycle(t *testing.T) {
	var tty bytes.Buffer
	oldTty := osc.TtyWriter
	osc.TtyWriter = &tty
	t.Cleanup(func() { osc.TtyWriter = oldTty })

	fc := &FakeClient{Up: true}
	env := testEnvWithGetter(fc, func(k string) string {
		switch k {
		case "TMUX":
			return "/tmp/tmux-1000/default,42,0"
		case "TMUX_PANE":
			return "%12"
		}
		return ""
	})
	env.RunChild = func(string, []string) int { return 0 }

	if code := Run([]string{"__shim", "--profile", "pratham", "claude", "/bin/claude"}, env); code != 0 {
		t.Fatalf("code = %d", code)
	}
	want := "\x1b]7501;state=working:app=claude-code:profile=pratham\x1b\\" +
		"\x1b]7501;state=idle:app=claude-code:profile=pratham\x1b\\" +
		"\x1b]7501;state=done:app=claude-code:profile=pratham:msg=ZmluaXNoZWQ=\x1b\\"
	if got := tty.String(); got != want {
		t.Fatalf("osc tty = %q, want %q", got, want)
	}

	tty.Reset()
	env.RunChild = func(string, []string) int { return 3 }
	if code := Run([]string{"__shim", "agy", "/bin/agy"}, env); code != 3 {
		t.Fatalf("code = %d", code)
	}
	wantErr := "\x1b]7501;state=working:app=antigravity\x1b\\" +
		"\x1b]7501;state=idle:app=antigravity\x1b\\" +
		"\x1b]7501;state=error:app=antigravity:msg=" +
		base64.StdEncoding.EncodeToString([]byte("exited with status 3")) + "\x1b\\"
	if got := tty.String(); got != wantErr {
		t.Fatalf("osc tty (error) = %q, want %q", got, wantErr)
	}
}

func TestShimSetsLaunchCmdAndStripsResumeArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "claude strips --resume id",
			args: []string{"__shim", "--profile", "work", "claude", "/usr/local/bin/harness", "claude", "profile", "work", "--model", "opus", "--resume", "old-sess-1"},
			want: []string{"/usr/local/bin/harness", "claude", "profile", "work", "--model", "opus"},
		},
		{
			name: "claude strips --resume=id",
			args: []string{"__shim", "claude", "/bin/claude", "--resume=old-sess-2", "-p", "hi"},
			want: []string{"/bin/claude", "-p", "hi"},
		},
		{
			name: "antigravity strips --conversation id",
			args: []string{"__shim", "--profile", "saucer", "antigravity", "/usr/local/bin/harness", "antigravity", "profile", "saucer", "--conversation", "conv-99"},
			want: []string{"/usr/local/bin/harness", "antigravity", "profile", "saucer"},
		},
		{
			name: "codex strips resume subcommand and id",
			args: []string{"__shim", "--profile", "dev", "codex", "/usr/local/bin/harness", "codex", "profile", "dev", "resume", "codex-sess-7"},
			want: []string{"/usr/local/bin/harness", "codex", "profile", "dev"},
		},
		{
			name: "plain command without resume flags preserved",
			args: []string{"__shim", "claude", "/bin/claude", "-p", "hello"},
			want: []string{"/bin/claude", "-p", "hello"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &FakeClient{Up: true}
			env := shimEnv(fc, 0)
			if code := Run(tc.args, env); code != 0 {
				t.Fatalf("code = %d", code)
			}
			raw, ok := findSet(fc.SetCalls, "@spaces_launch_cmd")
			if !ok {
				t.Fatalf("expected @spaces_launch_cmd to be set: %+v", fc.SetCalls)
			}
			var got []string
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal @spaces_launch_cmd %q: %v", raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("@spaces_launch_cmd = %v, want %v", got, tc.want)
			}
		})
	}
}

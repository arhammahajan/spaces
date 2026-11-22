package client

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// saveTmuxTestSeams snapshots the seams this file's tests mutate.
func saveTmuxTestSeams(t *testing.T) {
	t.Helper()
	snap := saveSeams()
	t.Cleanup(snap.restore)
}

// clearSpaceEnv empties every session/pane locator so each test starts
// from a known state regardless of the ambient environment.
func clearSpaceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("ZELLIJ_PANE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HARNESS_PROFILE", "")
}

// tmuxCall records one TmuxFunc invocation.
type tmuxCall struct {
	args []string
}

func stubTmux(calls *[]tmuxCall, out string, code int, err error) func([]string, time.Duration) (string, int, error) {
	return func(args []string, _ time.Duration) (string, int, error) {
		*calls = append(*calls, tmuxCall{args: append([]string{}, args...)})
		return out, code, err
	}
}

func callArgs(c tmuxCall) string { return strings.Join(c.args, " ") }

func TestTmuxBinary(t *testing.T) {
	clearSpaceEnv(t)
	t.Setenv("TMUX_BIN", "")
	if got := TmuxBinary(); got != "tmux" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("TMUX_BIN", "/opt/bin/tmux")
	if got := TmuxBinary(); got != "/opt/bin/tmux" {
		t.Errorf("override = %q", got)
	}
}

func TestHookTargetZellijIntact(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	TmuxFunc = stubTmux(&[]tmuxCall{}, "nope\n", 0, nil)

	// Zellij path wins even when tmux env is present.
	t.Setenv("ZELLIJ_SESSION_NAME", "zsess")
	t.Setenv("ZELLIJ_PANE_ID", "terminal_7")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%12")
	if s, p, ok := HookTarget(); !ok || s != "zsess" || p != "7" {
		t.Errorf("zellij = %q %q %v", s, p, ok)
	}

	// Session without a pane id stays silent.
	t.Setenv("ZELLIJ_PANE_ID", "terminal_")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("pane-less session should be silent")
	}

	// No session anywhere stays silent.
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("empty env should be silent without tmux display-message")
	}
}

func TestHookTargetTmux(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)

	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "mysess\n", 0, nil)
	s, p, ok := HookTarget()
	if !ok || s != "mysess" || p != "12" {
		t.Fatalf("tmux = %q %q %v", s, p, ok)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	want := "display-message -p -t %12 #{session_name}"
	if got := callArgs(calls[0]); got != want {
		t.Errorf("call = %q want %q", got, want)
	}

	// Missing TMUX_PANE stays silent without calling tmux.
	calls = nil
	t.Setenv("TMUX_PANE", "")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("missing TMUX_PANE should be silent")
	}
	if len(calls) != 0 {
		t.Errorf("unexpected calls = %v", calls)
	}

	// Missing TMUX stays silent.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "%3")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("missing TMUX should be silent")
	}

	// Unparseable pane id stays silent.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "bogus")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("bogus TMUX_PANE should be silent")
	}

	// tmux errors stay silent.
	TmuxFunc = stubTmux(&calls, "", 1, nil)
	t.Setenv("TMUX_PANE", "%4")
	if _, _, ok := HookTarget(); ok {
		t.Errorf("tmux nonzero exit should be silent")
	}
	TmuxFunc = stubTmux(&calls, "   \n", 0, nil)
	if _, _, ok := HookTarget(); ok {
		t.Errorf("blank session should be silent")
	}

	// Nil TmuxFunc stays silent instead of panicking.
	TmuxFunc = nil
	if _, _, ok := HookTarget(); ok {
		t.Errorf("nil TmuxFunc should be silent")
	}
}

func TestTmuxPaneOptionHelpers(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%8")

	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "hello\n", 0, nil)

	SetTmuxPaneOption("", "@spaces_foo", "bar")
	UnsetTmuxPaneOption("%9", "@spaces_foo")
	got := GetTmuxPaneOption("", "@spaces_foo")
	if got != "hello" {
		t.Errorf("GetTmuxPaneOption = %q, want hello", got)
	}
	want := []string{
		"set-option -p -t %8 @spaces_foo bar",
		"set-option -p -u -t %9 @spaces_foo",
		"display-message -p -t %8 #{@spaces_foo}",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if callArgs(calls[i]) != want[i] {
			t.Errorf("call %d = %q want %q", i, callArgs(calls[i]), want[i])
		}
	}
}

func TestEmitProgramStatusTmux(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	NowFunc = func() time.Time { return time.UnixMilli(1700000001234) }

	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }

	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "", 0, nil)
	EmitProgramStatus("blocked", "claude-code", "need input", "auth")

	var got []string
	for _, c := range calls {
		got = append(got, callArgs(c))
	}
	want := []string{
		"set-option -p -t %12 @spaces_state blocked",
		"set-option -p -t %12 @spaces_app claude-code",
		"set-option -p -t %12 @spaces_kind auth",
		"set-option -p -t %12 @spaces_msg need input",
		"set-option -p -t %12 @spaces_ts 1700000001234",
		"set-option -p -t %12 @spaces_source hook",
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q want %q", i, got[i], want[i])
		}
	}
	// Dual-layer: OSC 7501 is also emitted to the pane's tty for __osc-watch.
	wantOSC := "\x1b]7501;state=blocked:app=claude-code:kind=auth:msg=" + b64Encode("need input") + "\x1b\\"
	if content := readFile(t, tty.Name()); content != wantOSC {
		t.Errorf("tty got %q, want %q", content, wantOSC)
	}

	// Empty kind/msg unset their options instead of writing blanks; HARNESS_PROFILE sets @spaces_profile.
	t.Setenv("HARNESS_PROFILE", "pratham")
	calls = nil
	EmitProgramStatus("idle", "zj", "", "")
	got = nil
	for _, c := range calls {
		got = append(got, callArgs(c))
	}
	want = []string{
		"set-option -p -t %12 @spaces_state idle",
		"set-option -p -t %12 @spaces_app zj",
		"set-option -p -t %12 @spaces_profile pratham",
		"set-option -p -u -t %12 @spaces_kind",
		"set-option -p -u -t %12 @spaces_msg",
		"set-option -p -t %12 @spaces_ts 1700000001234",
		"set-option -p -t %12 @spaces_source hook",
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q want %q", i, got[i], want[i])
		}
	}
}

func TestEmitProgramStatusZellijRegardlessOfTmux(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "zsess")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")

	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "", 0, nil)

	EmitProgramStatus("working", "claude-code", "", "")
	if len(calls) != 0 {
		t.Errorf("zellij path must not call tmux: %v", calls)
	}
	if got := readFile(t, tty.Name()); got != "\x1b]7501;state=working:app=claude-code\x1b\\" {
		t.Errorf("osc = %q", got)
	}
}

func TestEmitAgentAndHookStatusTmux(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%9")
	NowFunc = func() time.Time { return time.UnixMilli(1700000000000) }
	TtyPathsFunc = func() []string { return nil }
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "", 0, nil)

	EmitAgentEvent("claude", "start")
	foundWorking, foundIdle := false, false
	for _, c := range calls {
		switch callArgs(c) {
		case "set-option -p -t %9 @spaces_state working":
			foundWorking = true
		case "set-option -p -t %9 @spaces_state idle":
			foundIdle = true
		}
	}
	if !foundWorking || !foundIdle {
		t.Errorf("prime calls = %v", calls)
	}

	// Hook status with blocked/auth projects kind through tmux.
	calls = nil
	EmitHookStatus(map[string]any{"state": "blocked", "kind": "auth", "app": "pi"})
	joined := []string{}
	for _, c := range calls {
		joined = append(joined, callArgs(c))
	}
	for _, want := range []string{
		"set-option -p -t %9 @spaces_state blocked",
		"set-option -p -t %9 @spaces_kind auth",
	} {
		found := false
		for _, g := range joined {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %v", want, joined)
		}
	}

	// Failed maps to error, like the OSC path.
	calls = nil
	EmitHookStatus(map[string]any{"state": "failed", "app": "pi"})
	joined = nil
	for _, c := range calls {
		joined = append(joined, callArgs(c))
	}
	found := false
	for _, g := range joined {
		if g == "set-option -p -t %9 @spaces_state error" {
			found = true
		}
	}
	if !found {
		t.Errorf("failed mapping = %v", joined)
	}
}

func TestHookClientTmuxTransport(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	AncestorsFunc = func() [][]string { return nil }
	TtyPathsFunc = func() []string { return nil }

	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "tmuxsess\n", 0, nil)

	HookDisabled = func() bool { return false }
	NormalizeEvent = func(agent, event string, payload map[string]any, env map[string]string) map[string]any {
		return map[string]any{"event": "prompt", "state": "working", "app": "claude-code"}
	}
	var published map[string]any
	Publish = func(e map[string]any) bool { published = e; return true }

	StdinValue = strings.NewReader(`{"session_id":"abc"}`)
	if code := HookClient("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if published["session"] != "tmuxsess" || published["pane"] != "12" {
		t.Errorf("envelope = %v", published)
	}
	sawDisplay, sawState, sawResume := false, false, false
	for _, c := range calls {
		a := callArgs(c)
		if strings.HasPrefix(a, "display-message") {
			sawDisplay = true
		}
		if a == "set-option -p -t %12 @spaces_state working" {
			sawState = true
		}
		if a == "set-option -p -t %12 @spaces_resume_session abc" {
			sawResume = true
		}
	}
	if !sawDisplay || !sawState || !sawResume {
		t.Errorf("calls = %v", calls)
	}
}

func TestHookClientTmuxDegraded(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%5")
	AncestorsFunc = func() [][]string { return nil }
	HookDisabled, NormalizeEvent, Publish, AppendSpool, EnsureServer = nil, nil, nil, nil, nil
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "s\n", 0, nil)
	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }

	StdinValue = strings.NewReader(`{"session_id":"x"}`)
	if code := Hook("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	// Dual-layer degraded path emits OSC 7501 to tty AND projects tmux options.
	wantOSC := "\x1b]7501;state=working:app=claude-code\x1b\\"
	if got := readFile(t, tty.Name()); got != wantOSC {
		t.Errorf("tty got %q, want %q", got, wantOSC)
	}
	found := false
	for _, c := range calls {
		if callArgs(c) == "set-option -p -t %5 @spaces_state working" {
			found = true
		}
	}
	if !found {
		t.Errorf("calls = %v", calls)
	}
}

func TestHookClientStopBackgroundDecisionMatchesTmuxAndStore(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	AncestorsFunc = func() [][]string { return nil }
	TtyPathsFunc = func() []string { return nil }
	NowFunc = func() time.Time { return time.UnixMilli(1700000000000) }
	HookDisabled = func() bool { return false }

	// 1. Subagent child map must never overwrite the pane's tmux status.
	var calls []tmuxCall
	TmuxFunc = stubTmux(&calls, "work\n", 0, nil)
	EmitHookStatus(map[string]any{
		"event": "subagent_start",
		"state": "working",
		"app":   "claude-code",
		"child": map[string]any{"id": "sub-1", "type": "Explore", "state": "working"},
	})
	if len(calls) != 0 {
		t.Fatalf("child map event must not emit tmux status, got %v", calls)
	}

	// 2. Stop held by ResolvePaneEvent emits @spaces_state=working with waiting message
	//    to tmux AND publishes the exact same working state to the hook store.
	calls = nil
	var published map[string]any
	Publish = func(e map[string]any) bool { published = e; return true }
	NormalizeEvent = func(agent, event string, payload map[string]any, env map[string]string) map[string]any {
		return map[string]any{"event": "stop", "state": "done", "app": "claude-code", "text": "turn complete"}
	}
	ResolvePaneEvent = func(session, pane string, event map[string]any) map[string]any {
		cp := map[string]any{}
		for k, v := range event {
			cp[k] = v
		}
		cp["state"] = "working"
		cp["text"] = "waiting on: go test ./..."
		cp["waiting_on"] = "waiting on: go test ./..."
		return cp
	}
	StdinValue = strings.NewReader(`{"session_id":"s-1"}`)
	if code := HookClient("claude", "Stop"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	sawWorking, sawWaitingMsg, sawUnsetPrior := false, false, false
	for _, c := range calls {
		a := callArgs(c)
		if a == "set-option -p -t %12 @spaces_state working" {
			sawWorking = true
		}
		if a == "set-option -p -t %12 @spaces_msg waiting on: go test ./..." {
			sawWaitingMsg = true
		}
		if strings.Contains(a, "@spaces_prior_outcome") {
			sawUnsetPrior = true
		}
	}
	if !sawWorking || !sawWaitingMsg {
		t.Fatalf("expected tmux working + waiting msg, got %v", calls)
	}
	if sawUnsetPrior {
		t.Fatalf("held Stop must not clear @spaces_prior_outcome: %v", calls)
	}
	normPub, _ := published["normalized"].(map[string]any)
	if normPub["state"] != "working" || normPub["text"] != "waiting on: go test ./..." {
		t.Fatalf("published normalized = %v, want working + waiting msg", normPub)
	}

	// 3. Later Stop with no finite work emits @spaces_state=done and clears @spaces_prior_outcome.
	calls = nil
	ResolvePaneEvent = func(session, pane string, event map[string]any) map[string]any {
		return event
	}
	StdinValue = strings.NewReader(`{"session_id":"s-1"}`)
	if code := HookClient("claude", "Stop"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	sawDone := false
	sawUnsetPrior = false
	for _, c := range calls {
		a := callArgs(c)
		if a == "set-option -p -t %12 @spaces_state done" {
			sawDone = true
		}
		if a == "set-option -p -u -t %12 @spaces_prior_outcome" {
			sawUnsetPrior = true
		}
	}
	if !sawDone || !sawUnsetPrior {
		t.Fatalf("expected tmux done + unset prior outcome, got %v", calls)
	}
}

func TestRecordResumeTargetRecordsLaunchCmd(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	NowFunc = func() time.Time { return time.UnixMilli(1700000123456) }

	var calls []tmuxCall
	TmuxFunc = func(args []string, _ time.Duration) (string, int, error) {
		calls = append(calls, tmuxCall{args: append([]string{}, args...)})
		if len(args) > 0 && args[0] == "display-message" {
			return "alpha\x1f/repo/work\x1fwork\x1f[\"/usr/local/bin/harness\",\"claude\",\"profile\",\"work\"]\n", 0, nil
		}
		return "", 0, nil
	}

	var gotAgent, gotSID, gotProfile, gotCWD, gotSpace string
	var gotCmd []string
	var gotNowMs int64
	RecordConversationLaunchFunc = func(_ context.Context, agent, sessionID, profile, cwd, title string, launchCmd []string, space string, nowMs int64) error {
		gotAgent, gotSID, gotProfile, gotCWD, gotSpace = agent, sessionID, profile, cwd, space
		gotCmd = append([]string(nil), launchCmd...)
		gotNowMs = nowMs
		return nil
	}

	RecordResumeTarget("claude", map[string]any{"session_id": "sess-xyz"})

	wantCmd := []string{"/usr/local/bin/harness", "claude", "profile", "work"}
	if gotAgent != "claude" || gotSID != "sess-xyz" || gotProfile != "work" ||
		gotCWD != "/repo/work" || gotSpace != "alpha" || gotNowMs != 1700000123456 ||
		!reflect.DeepEqual(gotCmd, wantCmd) {
		t.Fatalf("recorded launch = (%q, %q, %q, %q, %q, %v, %d)",
			gotAgent, gotSID, gotProfile, gotCWD, gotSpace, gotCmd, gotNowMs)
	}
}

func TestRecordResumeTargetRecordsProfileWithoutLaunchCmd(t *testing.T) {
	saveTmuxTestSeams(t)
	clearSpaceEnv(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	t.Setenv("TMUX_PANE", "%12")
	NowFunc = func() time.Time { return time.UnixMilli(1700000123456) }

	type rec struct {
		agent, sid, profile, cwd, space string
		cmd                             []string
		nowMs                           int64
	}
	var recorded []rec
	RecordConversationLaunchFunc = func(_ context.Context, agent, sessionID, profile, cwd, _ string, launchCmd []string, space string, nowMs int64) error {
		recorded = append(recorded, rec{
			agent: agent, sid: sessionID, profile: profile, cwd: cwd, space: space,
			cmd: append([]string(nil), launchCmd...), nowMs: nowMs,
		})
		return nil
	}

	// 1. @spaces_profile set, @spaces_launch_cmd empty -> still records profile.
	TmuxFunc = func(args []string, _ time.Duration) (string, int, error) {
		if len(args) > 0 && args[0] == "display-message" {
			return "alpha\x1f/repo/work\x1fpratham\x1f\n", 0, nil
		}
		return "", 0, nil
	}
	RecordResumeTarget("antigravity", map[string]any{"session_id": "sess-1"})
	if len(recorded) != 1 || recorded[0].agent != "antigravity" || recorded[0].sid != "sess-1" ||
		recorded[0].profile != "pratham" || recorded[0].cwd != "/repo/work" || recorded[0].space != "alpha" ||
		len(recorded[0].cmd) != 0 {
		t.Fatalf("case 1 recorded = %+v", recorded)
	}

	// 2. Both tmux options empty, HARNESS_PROFILE env set -> still records profile.
	recorded = nil
	t.Setenv("HARNESS_PROFILE", "env-prof")
	TmuxFunc = func(args []string, _ time.Duration) (string, int, error) {
		if len(args) > 0 && args[0] == "display-message" {
			return "alpha\x1f/repo/work\x1f\x1f\n", 0, nil
		}
		return "", 0, nil
	}
	RecordResumeTarget("claude", map[string]any{"session_id": "sess-2"})
	if len(recorded) != 1 || recorded[0].profile != "env-prof" || len(recorded[0].cmd) != 0 {
		t.Fatalf("case 2 recorded = %+v", recorded)
	}

	// 3. @spaces_profile and HARNESS_PROFILE empty, @spaces_launch_cmd has profile -> extracts profile.
	recorded = nil
	t.Setenv("HARNESS_PROFILE", "")
	TmuxFunc = func(args []string, _ time.Duration) (string, int, error) {
		if len(args) > 0 && args[0] == "display-message" {
			return "alpha\x1f/repo/work\x1f\x1f[\"/usr/local/bin/harness\",\"agy\",\"profile\",\"from-cmd\"]\n", 0, nil
		}
		return "", 0, nil
	}
	RecordResumeTarget("antigravity", map[string]any{"session_id": "sess-3"})
	wantCmd := []string{"/usr/local/bin/harness", "agy", "profile", "from-cmd"}
	if len(recorded) != 1 || recorded[0].profile != "from-cmd" || !reflect.DeepEqual(recorded[0].cmd, wantCmd) {
		t.Fatalf("case 3 recorded = %+v", recorded)
	}

	// 4. Both launchCmd and profile empty -> does not record.
	recorded = nil
	TmuxFunc = func(args []string, _ time.Duration) (string, int, error) {
		if len(args) > 0 && args[0] == "display-message" {
			return "alpha\x1f/repo/work\x1f\x1f\n", 0, nil
		}
		return "", 0, nil
	}
	RecordResumeTarget("claude", map[string]any{"session_id": "sess-4"})
	if len(recorded) != 0 {
		t.Fatalf("case 4 expected no record when both launchCmd and profile are empty, got %+v", recorded)
	}
}

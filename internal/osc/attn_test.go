package osc

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/arhammahajan/spaces/internal/agent/plugins"
)

// fixture captures both record targets and restores them on cleanup,
// so tests never touch the real /dev/tty.
type fixture struct {
	tty bytes.Buffer
	out bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	oldTty, oldOut := TtyWriter, Out
	TtyWriter, Out = &f.tty, &f.out
	t.Cleanup(func() { TtyWriter, Out = oldTty, oldOut })
	return f
}

// failingWriter rejects every write, standing in for an unusable tty.
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, os.ErrInvalid
}

func TestCanonicalApp(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude", "claude-code"},
		{"Claude", "claude-code"},
		{"kimi-cli", "kimi-cli"},
		{"dsh", "deepseek"},
		{"dsh-tui", "deepseek"},
		{"cursor", "cursor-agent"},
		{"agy", "antigravity"},
		{"opencode", "opencode"},
		{"codex", "codex"},
		{"claude-code", "claude-code"}, // already canonical stays put
		{"  MyCustomAgent  ", "mycustomagent"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CanonicalApp(c.in); got != c.want {
			t.Errorf("CanonicalApp(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEmitStateAndAppOnly(t *testing.T) {
	f := newFixture(t)
	Emit("zj", "working", "", "")
	want := "\x1b]7501;state=working:app=zj\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
	if f.out.Len() != 0 {
		t.Errorf("stdout fallback = %q, want empty", f.out.String())
	}
}

func TestEmitWithProfile(t *testing.T) {
	f := newFixture(t)
	EmitWithProfile("antigravity", "pratham", "blocked", "need input", "permission")
	want := "\x1b]7501;state=blocked:app=antigravity:profile=pratham:kind=permission:msg=" +
		base64.StdEncoding.EncodeToString([]byte("need input")) + "\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
	// Verify round-trip with Scanner/Watch.
	var recs []Record
	if err := Watch(bytes.NewBufferString(f.tty.String()), func(r Record) {
		recs = append(recs, r)
	}); err != nil {
		t.Fatalf("Watch error: %v", err)
	}
	if len(recs) != 1 || recs[0].State != "blocked" || recs[0].App != "antigravity" || recs[0].Profile != "pratham" || recs[0].Kind != "permission" || recs[0].Msg != "need input" {
		t.Fatalf("round-trip parsed = %+v", recs)
	}
}

func TestWrapTmuxPassthroughRoundTrip(t *testing.T) {
	seq := FormatRecord("working", "claude-code", "work", "", "running tests")
	wrapped := WrapTmuxPassthrough(seq)
	wantPrefix := "\x1bPtmux;\x1b\x1b]7501;"
	wantSuffix := "\x1b\x1b\\\x1b\\"
	if !strings.HasPrefix(wrapped, wantPrefix) || !strings.HasSuffix(wrapped, wantSuffix) {
		t.Fatalf("wrapped = %q", wrapped)
	}
	var recs []Record
	if err := Watch(bytes.NewBufferString(wrapped), func(r Record) {
		recs = append(recs, r)
	}); err != nil {
		t.Fatalf("Watch error: %v", err)
	}
	if len(recs) != 1 || recs[0].State != "working" || recs[0].App != "claude-code" || recs[0].Profile != "work" || recs[0].Msg != "running tests" {
		t.Fatalf("parsed = %+v", recs)
	}
	if WrapTmuxPassthrough("") != "" {
		t.Errorf("WrapTmuxPassthrough(\"\") = %q, want empty", WrapTmuxPassthrough(""))
	}
}

func TestEmitKindBeforeMsg(t *testing.T) {
	f := newFixture(t)
	Emit("opencode", "blocked", "need input", "permission")
	want := "\x1b]7501;state=blocked:app=opencode:kind=permission:msg=" +
		base64.StdEncoding.EncodeToString([]byte("need input")) + "\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

func TestEmitBase64Msg(t *testing.T) {
	f := newFixture(t)
	Emit("zj", "done", "finished", "")
	// Hardcoded StdEncoding of "finished" to pin the bytes exactly.
	want := "\x1b]7501;state=done:app=zj:msg=ZmluaXNoZWQ=\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
	f.tty.Reset()
	Emit("zj", "error", "exited with status 3", "")
	want = "\x1b]7501;state=error:app=zj:msg=" +
		base64.StdEncoding.EncodeToString([]byte("exited with status 3")) + "\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

func TestEmitKindWithoutMsg(t *testing.T) {
	f := newFixture(t)
	Emit("pi", "blocked", "", "auth")
	want := "\x1b]7501;state=blocked:app=pi:kind=auth\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

func TestEmitFallbackOnWriteFailure(t *testing.T) {
	f := newFixture(t)
	TtyWriter = failingWriter{}
	Emit("zj", "idle", "", "")
	want := "\x1b]7501;state=idle:app=zj\x1b\\"
	if got := f.out.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if f.tty.Len() != 0 {
		t.Errorf("tty = %q, want empty", f.tty.String())
	}
}

func TestEmitNilTtyWriter(t *testing.T) {
	f := newFixture(t)
	TtyWriter = nil
	Emit("zj", "working", "", "")
	want := "\x1b]7501;state=working:app=zj\x1b\\"
	if got := f.out.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestDefaultTtyDeviceWritesDevice(t *testing.T) {
	f := newFixture(t)
	dev := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath, oldEmit := TtyPath, TmuxEmitFunc
	TtyPath = dev
	TmuxEmitFunc = nil
	t.Cleanup(func() { TtyPath, TmuxEmitFunc = oldPath, oldEmit })
	TtyWriter = ttyDevice{}
	Emit("zj", "working", "", "")
	data, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "\x1b]7501;state=working:app=zj\x1b\\" {
		t.Errorf("device = %q", got)
	}
	if f.out.Len() != 0 {
		t.Errorf("stdout fallback = %q, want empty", f.out.String())
	}
}

func TestDefaultTtyDeviceFallback(t *testing.T) {
	f := newFixture(t)
	oldPath, oldEmit := TtyPath, TmuxEmitFunc
	TtyPath = filepath.Join(t.TempDir(), "missing-tty")
	TmuxEmitFunc = nil
	t.Cleanup(func() { TtyPath, TmuxEmitFunc = oldPath, oldEmit })
	TtyWriter = ttyDevice{}
	Emit("zj", "idle", "", "")
	want := "\x1b]7501;state=idle:app=zj\x1b\\"
	if got := f.out.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestEnabledGating(t *testing.T) {
	newFixture(t) // buffer tty: a writable record target

	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	if Enabled() {
		t.Error("Enabled() outside Zellij and tmux = true, want false")
	}

	// Inside tmux without TMUX_PANE is not a targetable pane.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,42,0")
	if Enabled() {
		t.Error("Enabled() with TMUX but no TMUX_PANE = true, want false")
	}

	// Inside tmux with TMUX + TMUX_PANE enables OSC 7501 attention reporting.
	t.Setenv("TMUX_PANE", "%12")
	if !Enabled() {
		t.Error("Enabled() inside tmux = false, want true")
	}
	for _, v := range []string{"0", "false"} {
		t.Setenv("HARNESS_ATTN", v)
		if Enabled() {
			t.Errorf("Enabled() inside tmux with HARNESS_ATTN=%q = true, want false", v)
		}
	}
	t.Setenv("HARNESS_ATTN", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	t.Setenv("ZELLIJ_SESSION_NAME", "main")
	if !Enabled() {
		t.Error("Enabled() inside Zellij = false, want true")
	}

	// Present-but-empty HARNESS_ATTN means enabled.
	t.Setenv("HARNESS_ATTN", "")
	if !Enabled() {
		t.Error("Enabled() with empty HARNESS_ATTN = false, want true")
	}

	for _, v := range []string{"0", "false"} {
		t.Setenv("HARNESS_ATTN", v)
		if Enabled() {
			t.Errorf("Enabled() with HARNESS_ATTN=%q = true, want false", v)
		}
	}

	t.Setenv("HARNESS_ATTN", "1")
	if !Enabled() {
		t.Error("Enabled() with HARNESS_ATTN=1 = false, want true")
	}

	// T3 invocations still report: their panes are real.
	t.Setenv("HARNESS_AGENT", "t3-run")
	if !Enabled() {
		t.Error("Enabled() with HARNESS_AGENT set = false, want true")
	}

	// An unusable record target disables reporting.
	t.Setenv("HARNESS_ATTN", "")
	TtyWriter = failingWriter{}
	if Enabled() {
		t.Error("Enabled() with unwritable target = true, want false")
	}

	// A nil target is not writable either.
	TtyWriter = nil
	if Enabled() {
		t.Error("Enabled() with nil TtyWriter = true, want false")
	}
}

func TestEnabledProbesDefaultDevice(t *testing.T) {
	newFixture(t)
	t.Setenv("ZELLIJ_SESSION_NAME", "main")
	t.Setenv("HARNESS_ATTN", "")
	oldPath, oldTty := TtyPath, TtyWriter
	t.Cleanup(func() { TtyPath, TtyWriter = oldPath, oldTty })

	dev := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	TtyPath, TtyWriter = dev, ttyDevice{}
	if !Enabled() {
		t.Error("Enabled() with writable device = false, want true")
	}

	TtyPath = filepath.Join(t.TempDir(), "absent-tty")
	if Enabled() {
		t.Error("Enabled() with unopenable device = true, want false")
	}
}

func TestBeginAndPrime(t *testing.T) {
	f := newFixture(t)
	Begin("claude-code")
	want := "\x1b]7501;state=working:app=claude-code\x1b\\" +
		"\x1b]7501;state=idle:app=claude-code\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("Begin tty = %q, want %q", got, want)
	}
	if f.out.Len() != 0 {
		t.Errorf("stdout fallback = %q, want empty", f.out.String())
	}

	f.tty.Reset()
	Prime("claude-code")
	if got := f.tty.String(); got != want {
		t.Errorf("Prime tty = %q, want %q", got, want)
	}
}

func TestFinishExitZero(t *testing.T) {
	f := newFixture(t)
	Finish("claude-code", 0)
	want := "\x1b]7501;state=done:app=claude-code:msg=ZmluaXNoZWQ=\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

func TestFinishNonZero(t *testing.T) {
	f := newFixture(t)
	Finish("kimi", 3)
	want := "\x1b]7501;state=error:app=kimi:msg=" +
		base64.StdEncoding.EncodeToString([]byte("exited with status 3")) + "\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

func TestFinishFormatsCodeAsGiven(t *testing.T) {
	f := newFixture(t)
	Finish("zj", 137)
	want := "\x1b]7501;state=error:app=zj:msg=" +
		base64.StdEncoding.EncodeToString([]byte("exited with status 137")) + "\x1b\\"
	if got := f.tty.String(); got != want {
		t.Errorf("tty = %q, want %q", got, want)
	}
}

// fakeChild writes an executable shell script under dir and returns its path.
func fakeChild(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func oscRecords(s string) []string {
	var recs []string
	for _, part := range strings.Split(s, "\x1b\\") {
		if part != "" {
			recs = append(recs, part+"\x1b\\")
		}
	}
	return recs
}

func assertRecords(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWrapExitZero(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := fakeChild(t, dir, "agent", `printf '%s\n' "$*" >> "`+log+`"; exit 0`)
	code := Wrap("claude-code", bin, []string{"--flag", "x"}, nil)
	if code != 0 {
		t.Fatalf("Wrap exit = %d, want 0", code)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=claude-code\x1b\\",
		"\x1b]7501;state=idle:app=claude-code\x1b\\",
		"\x1b]7501;state=done:app=claude-code:msg=ZmluaXNoZWQ=\x1b\\",
	})
	if f.out.Len() != 0 {
		t.Errorf("stdout fallback = %q, want empty", f.out.String())
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "--flag x\n" {
		t.Errorf("child argv = %q, want %q", got, "--flag x\n")
	}
}

func TestWrapExitThree(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	bin := fakeChild(t, dir, "agent", "exit 3")
	code := Wrap("kimi", bin, nil, nil)
	if code != 3 {
		t.Fatalf("Wrap exit = %d, want 3", code)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=kimi\x1b\\",
		"\x1b]7501;state=idle:app=kimi\x1b\\",
		"\x1b]7501;state=error:app=kimi:msg=" +
			base64.StdEncoding.EncodeToString([]byte("exited with status 3")) + "\x1b\\",
	})
}

func TestWrapSpawnFailure(t *testing.T) {
	f := newFixture(t)
	bin := filepath.Join(t.TempDir(), "missing-agent")
	code := Wrap("deepseek", bin, nil, nil)
	if code != 127 {
		t.Fatalf("Wrap exit = %d, want 127", code)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=deepseek\x1b\\",
		"\x1b]7501;state=idle:app=deepseek\x1b\\",
		"\x1b]7501;state=error:app=deepseek:msg=" +
			base64.StdEncoding.EncodeToString([]byte("exited with status 127")) + "\x1b\\",
	})
}

func TestWrapSignalDeath(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	bin := fakeChild(t, dir, "agent", "kill -9 $$")
	code := Wrap("zj", bin, nil, nil)
	if code != 137 { // 128 + SIGKILL, shell-style
		t.Fatalf("Wrap exit = %d, want 137", code)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=zj\x1b\\",
		"\x1b]7501;state=idle:app=zj\x1b\\",
		"\x1b]7501;state=error:app=zj:msg=" +
			base64.StdEncoding.EncodeToString([]byte("exited with status 137")) + "\x1b\\",
	})
}

func TestWrapEnvAppendWins(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "env.log")
	bin := fakeChild(t, dir, "envchild",
		`printf '%s\n' "$ATTN_ADDED" >> "`+log+`"; printf '%s\n' "$ATTN_EXISTING" >> "`+log+`"`)
	t.Setenv("ATTN_EXISTING", "base")
	envAppend := []string{"ATTN_ADDED=added", "ATTN_EXISTING=overridden"}
	code := Wrap("opencode", bin, nil, envAppend)
	if code != 0 {
		t.Fatalf("Wrap exit = %d, want 0", code)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "added\noverridden\n"; got != want {
		t.Errorf("child env = %q, want %q", got, want)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=opencode\x1b\\",
		"\x1b]7501;state=idle:app=opencode\x1b\\",
		"\x1b]7501;state=done:app=opencode:msg=ZmluaXNoZWQ=\x1b\\",
	})
}

func TestChildEnv(t *testing.T) {
	t.Setenv("ATTN_CHILD_BASE", "keep")
	t.Setenv("ATTN_CHILD_BOTH", "old")
	envAppend := []string{"ATTN_CHILD_NEW=new", "ATTN_CHILD_BOTH=new", "malformed"}
	env := childEnv(envAppend)
	seen := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			if kv != "malformed" {
				t.Errorf("unexpected malformed entry %q", kv)
			}
			continue
		}
		if prev, dup := seen[k]; dup {
			t.Errorf("duplicate key %q (%q and %q)", k, prev, v)
			continue
		}
		seen[k] = v
	}
	if seen["ATTN_CHILD_BASE"] != "keep" {
		t.Errorf("base entry lost: %q", seen["ATTN_CHILD_BASE"])
	}
	if seen["ATTN_CHILD_NEW"] != "new" {
		t.Errorf("appended entry = %q, want new", seen["ATTN_CHILD_NEW"])
	}
	if seen["ATTN_CHILD_BOTH"] != "new" {
		t.Errorf("shadowed entry = %q, want new (envAppend must win)", seen["ATTN_CHILD_BOTH"])
	}
	if want := len(os.Environ()) - 1 + len(envAppend); len(env) != want {
		t.Errorf("env length = %d, want %d", len(env), want)
	}
}

func TestWrapWithProfileSetsAndClearsTmuxProfile(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldTty, oldOut, oldPath := TtyWriter, Out, TtyPath
	oldEmit, oldProfile := TmuxEmitFunc, TmuxProfileFunc
	t.Cleanup(func() {
		TtyWriter, Out, TtyPath = oldTty, oldOut, oldPath
		TmuxEmitFunc, TmuxProfileFunc = oldEmit, oldProfile
	})
	TtyPath = dev
	TtyWriter = ttyDevice{}

	var events []string
	TmuxEmitFunc = func(app, state, msg, kind string) {
		events = append(events, "emit:"+state+":"+app)
	}
	TmuxProfileFunc = func(profile string) {
		events = append(events, "profile:"+profile)
	}

	dir := t.TempDir()
	bin := fakeChild(t, dir, "agent", "exit 0")
	if code := WrapWithProfile("antigravity", "pratham", bin, nil, nil); code != 0 {
		t.Fatalf("WrapWithProfile exit = %d, want 0", code)
	}
	want := []string{
		"profile:pratham",
		"emit:working:antigravity",
		"emit:idle:antigravity",
		"emit:done:antigravity",
		"profile:",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestWrapWithProfileEmitsProfileRecords(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	bin := fakeChild(t, dir, "agent", "exit 0")
	if code := WrapWithProfile("antigravity", "pratham", bin, nil, nil); code != 0 {
		t.Fatalf("WrapWithProfile exit = %d, want 0", code)
	}
	assertRecords(t, oscRecords(f.tty.String()), []string{
		"\x1b]7501;state=working:app=antigravity:profile=pratham\x1b\\",
		"\x1b]7501;state=idle:app=antigravity:profile=pratham\x1b\\",
		"\x1b]7501;state=done:app=antigravity:profile=pratham:msg=ZmluaXNoZWQ=\x1b\\",
	})
}

package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeResult is one scripted Runner response.
type fakeResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// fakeRunner records calls and serves scripted results keyed by the joined
// argv, falling back to handler or an empty success.
type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	results map[string]fakeResult
	handler func(args []string) (string, string, int, error)
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{results: map[string]fakeResult{}}
}

func (f *fakeRunner) key(args []string) string {
	return strings.Join(args, "\x00")
}

// stub registers a scripted response for an exact argv.
func (f *fakeRunner) stub(args []string, res fakeResult) {
	f.results[f.key(args)] = res
}

// stubOutput registers a successful stdout response for an exact argv.
func (f *fakeRunner) stubOutput(args []string, stdout string) {
	f.stub(args, fakeResult{stdout: stdout})
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	res, ok := f.results[f.key(args)]
	handler := f.handler
	f.mu.Unlock()
	if ok {
		return res.stdout, res.stderr, res.exitCode, res.err
	}
	if handler != nil {
		return handler(args)
	}
	return "", "", 0, nil
}

func (f *fakeRunner) argvs() []string {
	var out []string
	for _, call := range f.calls {
		out = append(out, strings.Join(call, " "))
	}
	return out
}

func testClient(fake *fakeRunner, env map[string]string) *Client {
	return &Client{
		Runner: fake,
		Getenv: func(key string) string {
			return env[key]
		},
	}
}

func ctx() context.Context {
	return context.Background()
}

func TestExecRunnerHonorsTMUXBIN(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	content := "#!/bin/sh\nprintf 'out:%s\\n' \"$*\"\nprintf 'err-output\\n' >&2\nexit 3\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_BIN", script)

	r := NewExecRunner()
	if r.Bin != script {
		t.Fatalf("Bin = %q, want %q", r.Bin, script)
	}
	stdout, stderr, code, err := r.Run(ctx(), "list-sessions", "-F", "a b")
	if err == nil {
		t.Fatal("expected error for exit 3")
	}
	if code != 3 {
		t.Fatalf("exitCode = %d, want 3", code)
	}
	if !strings.Contains(stdout, "out:list-sessions -F a b") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "err-output") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestExecRunnerSuccessAndDefault(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "tmux-ok")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_BIN", script)
	stdout, _, code, err := ExecRunner{}.Run(ctx(), "has-session")
	if err != nil || code != 0 || strings.TrimSpace(stdout) != "hello" {
		t.Fatalf("got %q, %d, %v", stdout, code, err)
	}

	t.Setenv("TMUX_BIN", "")
	if got := NewExecRunner().Bin; got != "tmux" {
		t.Fatalf("default Bin = %q, want tmux", got)
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	r := ExecRunner{Bin: filepath.Join(t.TempDir(), "does-not-exist")}
	_, _, code, err := r.Run(ctx(), "has-session")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if code != -1 {
		t.Fatalf("exitCode = %d, want -1", code)
	}
}

func TestListSessions(t *testing.T) {
	fake := newFakeRunner()
	fake.handler = func(args []string) (string, string, int, error) {
		if args[0] != "list-sessions" || args[1] != "-F" {
			return "", "", 1, errors.New("unexpected call")
		}
		if !strings.Contains(args[2], "\x1f") {
			return "", "", 1, errors.New("format must use \\x1f separator")
		}
		var b strings.Builder
		b.WriteString("$0\x1fmain project\x1f2\x1f1700000000\x1f3\n")
		b.WriteString("$7\x1fscratch\x1f0\x1f1700000100\x1f1\n")
		return b.String(), "", 0, nil
	}
	c := testClient(fake, nil)
	sessions, err := c.ListSessions(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	if sessions[0].ID != "$0" || sessions[0].Name != "main project" || sessions[0].Attached != 2 || sessions[0].Windows != 3 {
		t.Fatalf("session[0] = %+v", sessions[0])
	}
	if sessions[0].CreatedAt.Unix() != 1700000000 {
		t.Fatalf("CreatedAt = %v", sessions[0].CreatedAt)
	}
	if sessions[1].Attached != 0 {
		t.Fatalf("session[1] = %+v", sessions[1])
	}
}

func TestListSessionsNoServer(t *testing.T) {
	fake := newFakeRunner()
	fake.stub([]string{"list-sessions", "-F", sessionListFormat}, fakeResult{
		stderr:   "no server running on /tmp/tmux-1000/default",
		exitCode: 1,
		err:      errors.New("exit status 1"),
	})
	c := testClient(fake, nil)
	_, err := c.ListSessions(ctx())
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
}

func TestListWindows(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"list-windows", "-a", "-F", windowListFormat},
		"dev\x1f@1\x1f0\x1feditor tab\x1f1\x1f0\x1f1\x1f0\n"+
			"dev\x1f@2\x1f1\x1fserver\x1f0\x1f1\x1f0\x1f1\n")
	c := testClient(fake, nil)
	windows, err := c.ListWindows(ctx())
	if err != nil {
		t.Fatal(err)
	}
	want := []Window{
		{SessionName: "dev", ID: "@1", Index: 0, Name: "editor tab", Active: true, Activity: true},
		{SessionName: "dev", ID: "@2", Index: 1, Name: "server", Bell: true, Silence: true},
	}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("got %+v, want %+v", windows, want)
	}
}

func TestListPanes(t *testing.T) {
	fake := newFakeRunner()
	fields := []string{
		"dev sess", "@1", "0", "my window", "%12", "pane title with spaces\tand tab",
		"node", "/home/u/my dir", "4242", "1", "1", "0", "0", "1", "0",
		"working", "claude", "edit", "Writing code", "1700000000123",
	}
	fake.stubOutput([]string{"list-panes", "-a", "-F", paneFormat},
		strings.Join(fields, "\x1f")+"\n"+
			strings.Join([]string{
				"dev sess", "@1", "1", "other", "%13", "", "bash", "/tmp", "4243",
				"0", "0", "0", "0", "0", "0", "", "-", "", "", "",
			}, "\x1f")+"\n")
	c := testClient(fake, nil)
	panes, err := c.ListPanes(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 2 {
		t.Fatalf("got %d panes", len(panes))
	}
	p := panes[0]
	if p.SessionName != "dev sess" || p.WindowID != "@1" || p.WindowIndex != 0 || p.WindowName != "my window" {
		t.Fatalf("identity = %+v", p)
	}
	if p.ID != "%12" || p.NumericID != 12 {
		t.Fatalf("id = %q/%d", p.ID, p.NumericID)
	}
	if p.Title != "pane title with spaces\tand tab" {
		t.Fatalf("title = %q", p.Title)
	}
	if p.CurrentCommand != "node" || p.CurrentPath != "/home/u/my dir" || p.PID != 4242 {
		t.Fatalf("proc = %+v", p)
	}
	if !p.Active || !p.WindowActive || p.Dead {
		t.Fatalf("flags = %+v", p)
	}
	if !p.WindowActivity || p.WindowBell || p.WindowSilence {
		t.Fatalf("window flags = %+v", p)
	}
	if p.SpacesState != "working" || p.SpacesApp != "claude" || p.SpacesKind != "edit" ||
		p.SpacesMsg != "Writing code" || p.SpacesTsMs != 1700000000123 {
		t.Fatalf("spaces = %+v", p)
	}
	q := panes[1]
	if q.SpacesState != "" || q.SpacesApp != "" || q.SpacesKind != "" || q.SpacesMsg != "" || q.SpacesTsMs != 0 {
		t.Fatalf("empty spaces fields should normalize, got %+v", q)
	}
	if q.NumericID != 13 || q.Active {
		t.Fatalf("q = %+v", q)
	}
}

func TestListPanesSkipsMalformed(t *testing.T) {
	fake := newFakeRunner()
	good := strings.Join([]string{
		"s", "@1", "0", "w", "%1", "t", "sh", "/tmp", "1",
		"1", "1", "0", "0", "0", "0", "", "", "", "", "",
	}, "\x1f")
	fake.stubOutput([]string{"list-panes", "-a", "-F", paneFormat}, "short\x1fline\n"+good+"\n")
	c := testClient(fake, nil)
	panes, err := c.ListPanes(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 || panes[0].ID != "%1" {
		t.Fatalf("got %+v", panes)
	}
}

func TestSnapshot(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"list-sessions", "-F", sessionListFormat}, "$0\x1fdev\x1f1\x1f1700000000\x1f2\n")
	fake.stubOutput([]string{"list-windows", "-a", "-F", windowListFormat}, "dev\x1f@1\x1f0\x1fw\x1f1\x1f0\x1f0\x1f0\n")
	fake.stubOutput([]string{"list-panes", "-a", "-F", paneFormat}, strings.Join([]string{
		"dev", "@1", "0", "w", "%3", "t", "sh", "/tmp", "9",
		"1", "1", "0", "0", "0", "0", "idle", "bash", "", "", "5",
	}, "\x1f")+"\n")
	fake.stubOutput([]string{"display-message", "-p", "-F", "#{session_name}\x1f#{pane_id}"}, "dev\x1f%3\n")

	c := testClient(fake, map[string]string{"TMUX_PANE": "%99"})
	snap, err := c.Snapshot(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Sessions) != 1 || len(snap.Windows) != 1 || len(snap.Panes) != 1 {
		t.Fatalf("snap = %+v", snap)
	}
	if snap.CurrentSession != "dev" || snap.CurrentPaneID != "%3" {
		t.Fatalf("current = %q/%q", snap.CurrentSession, snap.CurrentPaneID)
	}
}

func TestSnapshotFallsBackToEnvPane(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"list-sessions", "-F", sessionListFormat}, "")
	fake.stubOutput([]string{"list-windows", "-a", "-F", windowListFormat}, "")
	fake.stubOutput([]string{"list-panes", "-a", "-F", paneFormat}, "")
	fake.stub([]string{"display-message", "-p", "-F", "#{session_name}\x1f#{pane_id}"}, fakeResult{
		stderr: "no current session", exitCode: 1, err: errors.New("exit status 1"),
	})
	c := testClient(fake, map[string]string{"TMUX_PANE": "%7"})
	snap, err := c.Snapshot(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if snap.CurrentPaneID != "%7" || snap.CurrentSession != "" {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestSnapshotNoServer(t *testing.T) {
	fake := newFakeRunner()
	fake.stub([]string{"list-sessions", "-F", sessionListFormat}, fakeResult{
		stderr: "no server running", exitCode: 1, err: errors.New("exit status 1"),
	})
	c := testClient(fake, nil)
	snap, err := c.Snapshot(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Sessions) != 0 || len(snap.Windows) != 0 || len(snap.Panes) != 0 {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestSnapshotPropagatesRealErrors(t *testing.T) {
	fake := newFakeRunner()
	fake.stub([]string{"list-sessions", "-F", sessionListFormat}, fakeResult{
		stderr: "boom", exitCode: 1, err: errors.New("exit status 1"),
	})
	c := testClient(fake, nil)
	if _, err := c.Snapshot(ctx()); err == nil || errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v", err)
	}
}

func TestCapturePane(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"capture-pane", "-p", "-t", "%12", "-S", "-50"}, "line1\nline2\n")
	c := testClient(fake, nil)
	out, err := c.CapturePane(ctx(), "%12", 50)
	if err != nil || out != "line1\nline2\n" {
		t.Fatalf("got %q, %v", out, err)
	}

	// Non-positive lines select the 200-line default.
	fake.stubOutput([]string{"capture-pane", "-p", "-t", "%12", "-S", "-200"}, "x\n")
	if _, err := c.CapturePane(ctx(), "%12", 0); err != nil {
		t.Fatal(err)
	}

	fake.stub([]string{"capture-pane", "-p", "-t", "%99", "-S", "-10"}, fakeResult{
		stderr: "can't find pane: %99", exitCode: 1, err: errors.New("exit status 1"),
	})
	if _, err := c.CapturePane(ctx(), "%99", 10); err == nil {
		t.Fatal("expected error")
	}
}

func TestSwitchToSpace(t *testing.T) {
	inside := testClient(newFakeRunner(), map[string]string{"TMUX": "/tmp/tmux-1000/default,123,0"})
	if err := inside.SwitchToSpace(ctx(), "dev"); err != nil {
		t.Fatal(err)
	}
	if got := inside.Runner.(*fakeRunner).argvs(); !reflect.DeepEqual(got, []string{"switch-client -t dev"}) {
		t.Fatalf("calls = %v", got)
	}

	fake := newFakeRunner()
	outside := testClient(fake, nil)
	if err := outside.SwitchToSpace(ctx(), "dev"); err != nil {
		t.Fatal(err)
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, []string{"attach-session -t dev"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestFocusPaneSameSession(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"display-message", "-p", "#{session_name}"}, "dev\n")
	c := testClient(fake, map[string]string{"TMUX": "x"})
	if err := c.FocusPane(ctx(), "dev", "@1", "%3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"display-message -p #{session_name}",
		"select-window -t @1",
		"select-pane -t %3",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestFocusPaneSwitchesSession(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"display-message", "-p", "#{session_name}"}, "other\n")
	c := testClient(fake, map[string]string{"TMUX": "x"})
	if err := c.FocusPane(ctx(), "dev", "@1", "%3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"display-message -p #{session_name}",
		"switch-client -t dev",
		"select-window -t @1",
		"select-pane -t %3",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestFocusPaneAttachesOutsideTmux(t *testing.T) {
	fake := newFakeRunner()
	fake.stub([]string{"display-message", "-p", "#{session_name}"}, fakeResult{
		stderr: "no current session", exitCode: 1, err: errors.New("exit status 1"),
	})
	c := testClient(fake, nil)
	if err := c.FocusPane(ctx(), "dev", "", "%3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"display-message -p #{session_name}",
		"attach-session -t dev",
		"select-pane -t %3",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestNewRenameKillSession(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, map[string]string{"TMUX": "x"})
	if err := c.NewSession(ctx(), "work", "/home/u/proj"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameSession(ctx(), "work", "play"); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameWindow(ctx(), "@1", "editor"); err != nil {
		t.Fatal(err)
	}
	if err := c.KillSession(ctx(), "play"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"new-session -d -s work -c /home/u/proj",
		"switch-client -t work",
		"rename-session -t work play",
		"rename-window -t @1 -- editor",
		"kill-session -t play",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}

	fake2 := newFakeRunner()
	c2 := testClient(fake2, nil)
	if err := c2.NewSession(ctx(), "work", ""); err != nil {
		t.Fatal(err)
	}
	want2 := []string{"new-session -d -s work", "attach-session -t work"}
	if got := fake2.argvs(); !reflect.DeepEqual(got, want2) {
		t.Fatalf("calls = %v", got)
	}
}

func TestNewWindow(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, nil)
	if err := c.NewWindow(ctx(), "dev", "/tmp", "agent", "tmux-spaces", "__shim", "run"); err != nil {
		t.Fatal(err)
	}
	if err := c.NewWindow(ctx(), "", "", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"new-window -t dev: -c /tmp -n agent -- tmux-spaces __shim run",
		"new-window",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestPaneOptions(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, nil)
	if err := c.SetPaneOption(ctx(), "%1", "spaces_state", "working"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPaneOption(ctx(), "", "@spaces_app", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := c.UnsetPaneOption(ctx(), "%1", "spaces_msg"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"set-option -p -t %1 @spaces_state working",
		"set-option -p @spaces_app claude",
		"set-option -p -t %1 -u @spaces_msg",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestSetSpacesStatus(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, nil)
	if err := c.SetSpacesStatus(ctx(), "%2", "working", "claude", "edit", "Writing code", 42); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"set-option -p -t %2 @spaces_state working",
		"set-option -p -t %2 @spaces_app claude",
		"set-option -p -t %2 @spaces_kind edit",
		"set-option -p -t %2 @spaces_msg Writing code",
		"set-option -p -t %2 @spaces_ts 42",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}

	fake2 := newFakeRunner()
	c2 := testClient(fake2, nil)
	for _, state := range []string{"clear", ""} {
		if err := c2.SetSpacesStatus(ctx(), "%2", state, "x", "y", "z", 1); err != nil {
			t.Fatal(err)
		}
	}
	var wantClear []string
	for _, state := range []string{"clear", ""} {
		_ = state
		for _, opt := range spacesOptions {
			wantClear = append(wantClear, "set-option -p -t %2 -u "+opt)
		}
	}
	if got := fake2.argvs(); !reflect.DeepEqual(got, wantClear) {
		t.Fatalf("calls = %v", got)
	}
}

func TestSetSpacesStatusErrorStopsEarly(t *testing.T) {
	fake := newFakeRunner()
	fake.handler = func(args []string) (string, string, int, error) {
		if len(args) > 0 && args[len(args)-2] == "@spaces_app" {
			return "", "boom", 1, errors.New("exit status 1")
		}
		return "", "", 0, nil
	}
	c := testClient(fake, nil)
	if err := c.SetSpacesStatus(ctx(), "%2", "working", "a", "k", "m", 1); err == nil {
		t.Fatal("expected error")
	}
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %v", fake.argvs())
	}
}

func TestSendKeys(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, nil)
	if err := c.SendKeys(ctx(), "%1", "hello Enter world", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SendKeys(ctx(), "%1", "go", true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"send-keys -t %1 -l hello Enter world",
		"send-keys -t %1 -l go",
		"send-keys -t %1 Enter",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}
}

func TestDisplayPopup(t *testing.T) {
	fake := newFakeRunner()
	c := testClient(fake, nil)
	err := c.DisplayPopup(ctx(), PopupOptions{
		Width: "86%", Height: "82%", Title: " spaces ", Border: "rounded",
		Close: true, CWD: "/tmp", Command: []string{"tmux-spaces", "popup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"display-popup -E -w 86% -h 82% -T  spaces  -b rounded -d /tmp tmux-spaces popup",
	}
	if got := fake.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v", got)
	}

	fake2 := newFakeRunner()
	if err := testClient(fake2, nil).DisplayPopup(ctx(), PopupOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := fake2.argvs(); !reflect.DeepEqual(got, []string{"display-popup"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestEnvHelpers(t *testing.T) {
	c := testClient(newFakeRunner(), map[string]string{"TMUX": "sock,1,0", "TMUX_PANE": "%12"})
	if !c.InsideTmux() {
		t.Fatal("InsideTmux = false")
	}
	if got := c.CurrentPaneID(); got != "%12" {
		t.Fatalf("CurrentPaneID = %q", got)
	}
	if got := c.ParseNumericPaneID("%12"); got != 12 {
		t.Fatalf("ParseNumericPaneID = %d", got)
	}
	outside := testClient(newFakeRunner(), nil)
	if outside.InsideTmux() || outside.CurrentPaneID() != "" {
		t.Fatal("expected outside-tmux env")
	}
}

func TestParseNumericPaneID(t *testing.T) {
	cases := map[string]int{
		"%0": 0, "%12": 12, "12": 12, " %5 ": 5,
		"": -1, "%": -1, "%x": -1, "%-1": -1, "%%": -1, "%1.2": -1,
	}
	for in, want := range cases {
		if got := ParseNumericPaneID(in); got != want {
			t.Fatalf("ParseNumericPaneID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCurrentSession(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"display-message", "-p", "#{session_name}"}, "  dev\n")
	c := testClient(fake, nil)
	name, err := c.CurrentSession(ctx())
	if err != nil || name != "dev" {
		t.Fatalf("got %q, %v", name, err)
	}

	fake2 := newFakeRunner()
	fake2.stubOutput([]string{"display-message", "-p", "#{session_name}"}, "\n")
	if _, err := testClient(fake2, nil).CurrentSession(ctx()); !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
}

func TestServerRunning(t *testing.T) {
	ok := newFakeRunner()
	ok.stub([]string{"has-session"}, fakeResult{exitCode: 0})
	if !testClient(ok, nil).ServerRunning(ctx()) {
		t.Fatal("expected true")
	}
	down := newFakeRunner()
	down.stub([]string{"has-session"}, fakeResult{
		stderr: "no server running", exitCode: 1, err: errors.New("exit status 1"),
	})
	if testClient(down, nil).ServerRunning(ctx()) {
		t.Fatal("expected false")
	}
}

func TestCommandErrorIncludesStderr(t *testing.T) {
	err := commandError([]string{"kill-session", "-t", "x"}, "can't find session: x", 1, errors.New("exit status 1"))
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "can't find session: x") {
		t.Fatalf("err = %v", err)
	}
	plain := commandError([]string{"x"}, "", 1, errors.New("boom"))
	if errors.Is(plain, ErrNoServer) || !strings.Contains(plain.Error(), "boom") {
		t.Fatalf("err = %v", plain)
	}
}

func TestEndToEndWithStubScript(t *testing.T) {
	// Exercise the full stack (Client -> ExecRunner -> stub tmux script).
	dir := t.TempDir()
	script := filepath.Join(dir, "tmux")
	body := `#!/bin/sh
US=$(printf '\037')
if [ "$1" = "list-panes" ]; then
  printf '%s\n' "dev${US}@1${US}0${US}w${US}%8${US}hi there${US}claude${US}/home/u${US}100${US}1${US}1${US}0${US}0${US}0${US}0${US}working${US}claude${US}edit${US}msg${US}9"
elif [ "$1" = "capture-pane" ]; then
  printf 'screen text\n'
fi
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_BIN", script)
	c := DefaultClient()
	panes, err := c.ListPanes(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 || panes[0].Title != "hi there" || panes[0].SpacesApp != "claude" {
		t.Fatalf("panes = %+v", panes)
	}
	out, err := c.CapturePane(ctx(), "%8", 10)
	if err != nil || out != "screen text\n" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestListWindowsExtendedFieldsAndKillWindow(t *testing.T) {
	fake := newFakeRunner()
	fake.stubOutput([]string{"list-windows", "-a", "-F", windowListFormat},
		strings.Join([]string{
			"dev", "@1", "0", "agent", "1", "0", "1", "0", "bb62,180x48,0,0,1",
			"0", "1", "180", "48", "1700000123",
		}, "\x1f")+"\n"+
			strings.Join([]string{
				"dev", "@2", "1", "tests", "0", "0", "0", "0", "abcd,180x48,0,0,2",
				"1", "0", "160", "40", "1700000000",
			}, "\x1f")+"\n")
	c := testClient(fake, nil)
	wins, err := c.ListWindows(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 2 {
		t.Fatalf("got %d windows", len(wins))
	}
	if !wins[0].Active || !wins[0].Zoomed || wins[0].Last || wins[0].Width != 180 || wins[0].Height != 48 || wins[0].ActivityAt.Unix() != 1700000123 {
		t.Fatalf("wins[0] = %+v", wins[0])
	}
	if wins[1].Active || !wins[1].Last || wins[1].Zoomed || wins[1].Width != 160 || wins[1].Height != 40 || wins[1].ActivityAt.Unix() != 1700000000 {
		t.Fatalf("wins[1] = %+v", wins[1])
	}

	if err := c.KillWindow(ctx(), "@2"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	gotArgvs := fake.argvs()
	if len(gotArgvs) == 0 || gotArgvs[len(gotArgvs)-1] != "kill-window -t @2" {
		t.Fatalf("last argv = %v, want kill-window -t @2", gotArgvs)
	}
}

func TestListPanesExtendedFields(t *testing.T) {
	fake := newFakeRunner()
	fields := []string{
		"dev", "@1", "0", "agent", "%12", "pane title",
		"node", "/home/u/proj", "4242", "1", "1", "0", "0", "0", "0",
		"working", "claude", "edit", "Writing code", "1700000000123",
		"2", "sess-abc", "opus", "hook", "Fix auth",
		"120", "34", "1", "1", "claude --resume sess-abc", "0",
	}
	fake.stubOutput([]string{"list-panes", "-a", "-F", paneFormat}, strings.Join(fields, "\x1f")+"\n")
	c := testClient(fake, nil)
	panes, err := c.ListPanes(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 {
		t.Fatalf("got %d panes", len(panes))
	}
	p := panes[0]
	if p.Width != 120 || p.Height != 34 || !p.InMode || !p.Synchronized || p.StartCommand != "claude --resume sess-abc" || p.DeadStatus != 0 {
		t.Fatalf("extended pane fields = %+v", p)
	}
}

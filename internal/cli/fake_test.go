package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/tmux"
)

type paneOptCall struct {
	Pane   string
	Option string
	Value  string
}

type sendCall struct {
	Target string
	Text   string
}

type newSessCall struct {
	Name string
	CWD  string
}

type renameCall struct {
	Old string
	New string
}

type newWinCall struct {
	Session string
	CWD     string
	Label   string
	Cmd     []string
}

// FakeClient is an in-memory TmuxClient for tests.
type FakeClient struct {
	Bin      string
	Up       bool
	Cur      string
	CurOK    bool
	Snap     tmux.ServerSnapshot
	SnapErr  error
	Sessions []tmux.Session
	SessErr  error
	Windows  map[string][]tmux.Window
	WinErr   error
	Panes    map[string][]tmux.Pane
	PanesErr error

	SetErr     error
	UnsetErr   error
	SendErr    error
	Capture    map[string]string
	CaptureErr error
	SwitchErr  error
	NewSessErr error
	RenameErr  error
	KillErr    error
	NewWinErr  error

	SetCalls    []paneOptCall
	UnsetCalls  []paneOptCall
	Sent        []sendCall
	Switched    []string
	NewSessions []newSessCall
	Renamed     []renameCall
	Killed      []string
	NewWindows  []newWinCall
}

func (f *FakeClient) BinaryPath() string                     { return f.Bin }
func (f *FakeClient) ServerRunning() bool                    { return f.Up }
func (f *FakeClient) CurrentSession() (string, bool)         { return f.Cur, f.CurOK }
func (f *FakeClient) Snapshot() (tmux.ServerSnapshot, error) { return f.Snap, f.SnapErr }
func (f *FakeClient) ListSessions() ([]tmux.Session, error)  { return f.Sessions, f.SessErr }
func (f *FakeClient) ListWindows(session string) ([]tmux.Window, error) {
	return f.Windows[session], f.WinErr
}
func (f *FakeClient) ListPanes(session string) ([]tmux.Pane, error) {
	return f.Panes[session], f.PanesErr
}
func (f *FakeClient) SetPaneOption(pane, option, value string) error {
	f.SetCalls = append(f.SetCalls, paneOptCall{pane, option, value})
	return f.SetErr
}
func (f *FakeClient) UnsetPaneOption(pane, option string) error {
	f.UnsetCalls = append(f.UnsetCalls, paneOptCall{Pane: pane, Option: option})
	return f.UnsetErr
}
func (f *FakeClient) SendKeys(target, text string) error {
	f.Sent = append(f.Sent, sendCall{target, text})
	return f.SendErr
}
func (f *FakeClient) CapturePane(target string, lines int) (string, error) {
	if f.CaptureErr != nil {
		return "", f.CaptureErr
	}
	return f.Capture[target], nil
}
func (f *FakeClient) SwitchTo(target string) error {
	f.Switched = append(f.Switched, target)
	return f.SwitchErr
}
func (f *FakeClient) NewSession(name, cwd string) error {
	f.NewSessions = append(f.NewSessions, newSessCall{name, cwd})
	return f.NewSessErr
}
func (f *FakeClient) RenameSession(oldName, newName string) error {
	f.Renamed = append(f.Renamed, renameCall{oldName, newName})
	return f.RenameErr
}
func (f *FakeClient) KillSession(name string) error {
	f.Killed = append(f.Killed, name)
	return f.KillErr
}
func (f *FakeClient) NewWindow(session, cwd, label string, cmd []string) error {
	f.NewWindows = append(f.NewWindows, newWinCall{session, cwd, label, append([]string{}, cmd...)})
	return f.NewWinErr
}

// testEnv builds an Env with buffering stdio, a frozen clock, and stubbed
// lookups around client. A nil client leaves Env.Client unset.
func testEnv(client *FakeClient) Env {
	env := Env{
		Stdout:   &bytes.Buffer{},
		Stderr:   &bytes.Buffer{},
		Stdin:    strings.NewReader(""),
		Now:      func() time.Time { return time.Unix(1700000000, 0) },
		Sleep:    func(time.Duration) {},
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Self:     "tmux-spaces",
	}
	if client != nil {
		env.Client = client
	}
	return env
}

func testEnvWithGetter(client *FakeClient, get func(string) string) Env {
	e := testEnv(client)
	e.Getenv = get
	return e
}

func stdoutOf(t *testing.T, e Env) string {
	t.Helper()
	buf, ok := e.Stdout.(*bytes.Buffer)
	if !ok {
		t.Fatal("stdout is not a buffer")
	}
	return buf.String()
}

func stderrOf(t *testing.T, e Env) string {
	t.Helper()
	buf, ok := e.Stderr.(*bytes.Buffer)
	if !ok {
		t.Fatal("stderr is not a buffer")
	}
	return buf.String()
}

var errTestBoom = errors.New("boom")

func makePane(session, window, id string, num int, state, app string) tmux.Pane {
	return tmux.Pane{
		SessionName: session,
		WindowID:    "@1",
		WindowIndex: 0,
		WindowName:  window,
		ID:          id,
		NumericID:   num,
		Title:       "pane-" + id,
		CurrentPath: "/work/" + session,
		SpacesState: state,
		SpacesApp:   app,
	}
}

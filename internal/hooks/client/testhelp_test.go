package client

import (
	"io"
	"os"
	"testing"
	"time"
)

// captureOutput redirects Stdout/Stderr to temp files for one test.
func captureOutput(t *testing.T) (outPath, errPath string) {
	t.Helper()
	outFile, err := os.CreateTemp("", "hookclient-out-*")
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.CreateTemp("", "hookclient-err-*")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := Stdout, Stderr
	Stdout, Stderr = outFile, errFile
	t.Cleanup(func() {
		Stdout, Stderr = oldOut, oldErr
		outFile.Close()
		errFile.Close()
		_ = os.Remove(outFile.Name())
		_ = os.Remove(errFile.Name())
	})
	return outFile.Name(), errFile.Name()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func resetFile(t *testing.T, f *os.File, path string) {
	t.Helper()
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
}

// seamSnapshot records every injectable seam so tests can restore them.
type seamSnapshot struct {
	runBin        func(string, []string, string, time.Duration) (string, int, error)
	tmux          func([]string, time.Duration) (string, int, error)
	look          func(string) (string, error)
	anc           func() [][]string
	tty           func() []string
	hd            func() bool
	ne            func(string, string, map[string]any, map[string]string) map[string]any
	rpe           func(string, string, map[string]any) map[string]any
	pub           func(map[string]any) bool
	spool         func(map[string]any) error
	ensure        func(string) bool
	summary       func() map[string]any
	detail        func(string, any) map[string]any
	recordOutcome func(map[string]any) error
	removeOutcome func(map[string]any) error
	paneState     func(string, any) string
	status        func() int
	stop          func() int
	start         func(string) int
	daemon        func() int
	serve         func(bool, *int) int
	discoverTitle func(string, string, string) string
	stdin         interface{ Read([]byte) (int, error) }
	now           func() time.Time
}

func saveSeams() seamSnapshot {
	return seamSnapshot{
		RunBinFunc, TmuxFunc, LookPathFunc,
		AncestorsFunc, TtyPathsFunc,
		HookDisabled, NormalizeEvent, ResolvePaneEvent, Publish, AppendSpool,
		EnsureServer, SpaceHooksSummary, SpaceHooksDetail,
		RecordOutcome, RemoveOutcome, PaneState,
		HooksStatus, HooksStop, HooksStart, Daemonize, Serve,
		DiscoverTitleFunc,
		StdinValue, NowFunc,
	}
}

func (s seamSnapshot) restore() {
	RunBinFunc, TmuxFunc, LookPathFunc = s.runBin, s.tmux, s.look
	AncestorsFunc, TtyPathsFunc = s.anc, s.tty
	HookDisabled, NormalizeEvent, ResolvePaneEvent, Publish, AppendSpool = s.hd, s.ne, s.rpe, s.pub, s.spool
	EnsureServer, SpaceHooksSummary, SpaceHooksDetail = s.ensure, s.summary, s.detail
	RecordOutcome, RemoveOutcome, PaneState = s.recordOutcome, s.removeOutcome, s.paneState
	HooksStatus, HooksStop, HooksStart, Daemonize, Serve = s.status, s.stop, s.start, s.daemon, s.serve
	DiscoverTitleFunc = s.discoverTitle
	StdinValue, NowFunc = s.stdin, s.now
}

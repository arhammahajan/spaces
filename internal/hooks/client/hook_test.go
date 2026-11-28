package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEmitProgramStatusFormat(t *testing.T) {
	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	snap := saveSeams()
	defer snap.restore()
	TtyPathsFunc = func() []string { return []string{"/nonexistent-spaces-tty", tty.Name()} }
	EmitProgramStatus("working", "claude-code", "hi", "kind-x")
	got := readFile(t, tty.Name())
	want := "\x1b]7501;state=working:app=claude-code:kind=kind-x:msg=aGk=\x1b\\"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// No kind when empty, no msg when empty.
	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	EmitProgramStatus("idle", "zj", "", "")
	if got := readFile(t, tty.Name()); got != "\x1b]7501;state=idle:app=zj\x1b\\" {
		t.Errorf("got %q", got)
	}
	// Base64 of multibyte message.
	EmitProgramStatus("done", "codex", "héllo", "")
	if got := readFile(t, tty.Name()); !strings.Contains(got, ":msg=") {
		t.Errorf("got %q", got)
	}
}

func TestEmitHookStatusMapping(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	readTTY := func() string { return readFile(t, tty.Name()) }

	// Child events never change pane state.
	EmitHookStatus(map[string]any{"child": true, "state": "working", "app": "x"})
	if got := readTTY(); got != "" {
		t.Errorf("child emitted %q", got)
	}
	// session_start primes working then idle; file truncates to idle.
	EmitHookStatus(map[string]any{"event": "session_start", "app": "claude-code"})
	if got := readTTY(); got != "\x1b]7501;state=idle:app=claude-code\x1b\\" {
		t.Errorf("prime = %q", got)
	}
	// session_end emits idle with text.
	EmitHookStatus(map[string]any{"event": "session_end", "app": "codex", "text": "bye"})
	want := "\x1b]7501;state=idle:app=codex:msg=Ynll\x1b\\"
	if got := readTTY(); got != want {
		t.Errorf("end = %q want %q", got, want)
	}
	// Blocked carries kind.
	EmitHookStatus(map[string]any{"state": "blocked", "kind": "auth", "app": "pi"})
	if got := readTTY(); got != "\x1b]7501;state=blocked:app=pi:kind=auth\x1b\\" {
		t.Errorf("blocked = %q", got)
	}
	// Unknown state emits nothing.
	if err := os.Truncate(tty.Name(), 0); err != nil {
		t.Fatal(err)
	}
	EmitHookStatus(map[string]any{"state": "mystery", "app": "pi"})
	if got := readTTY(); got != "" {
		t.Errorf("mystery emitted %q", got)
	}
	// Failed maps to error.
	EmitHookStatus(map[string]any{"state": "failed", "app": "pi"})
	if got := readTTY(); got != "\x1b]7501;state=error:app=pi\x1b\\" {
		t.Errorf("failed = %q", got)
	}
	// Missing app defaults to zj.
	EmitHookStatus(map[string]any{"state": "working"})
	if got := readTTY(); got != "\x1b]7501;state=working:app=zj\x1b\\" {
		t.Errorf("default app = %q", got)
	}
}

func TestReadHookPayload(t *testing.T) {
	r, w, _ := os.Pipe()
	snap := saveSeams()
	defer snap.restore()
	StdinValue = r
	go func() {
		_, _ = w.WriteString(`{"session_id":"s-1","cwd":"/w"}`)
		_ = w.Close()
	}()
	got := ReadHookPayload(2)
	if got["session_id"] != "s-1" {
		t.Errorf("payload = %v", got)
	}
	if id := ExtractSessionID(got); id != "s-1" {
		t.Errorf("extract = %q", id)
	}
	// Non-dict JSON degrades to empty.
	r2, w2, _ := os.Pipe()
	StdinValue = r2
	go func() {
		_, _ = w2.WriteString(`[1,2]`)
		_ = w2.Close()
	}()
	if got := ReadHookPayload(2); len(got) != 0 {
		t.Errorf("list payload = %v", got)
	}
	// Timeout on silent pipe.
	r3, w3, _ := os.Pipe() // never written, never closed
	defer w3.Close()
	StdinValue = r3
	if got := ReadHookPayload(0.05); len(got) != 0 {
		t.Errorf("timeout payload = %v", got)
	}
	_ = r3.Close()
}

func TestExtractSessionIDNested(t *testing.T) {
	p := map[string]any{"session": map[string]any{"threadId": "t-9"}}
	if got := ExtractSessionID(p); got != "t-9" {
		t.Errorf("got %q", got)
	}
	p = map[string]any{"id": "plain-id"}
	if got := ExtractSessionID(p); got != "plain-id" {
		t.Errorf("got %q", got)
	}
	if got := ExtractSessionID(map[string]any{}); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestHookDegraded(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	// Empty agent is a no-op.
	if code := Hook("", "prompt"); code != 0 {
		t.Fatalf("empty agent code = %d", code)
	}
	// All seams nil: degraded behavior.
	HookDisabled, NormalizeEvent, Publish, AppendSpool, EnsureServer = nil, nil, nil, nil, nil
	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	AncestorsFunc = func() [][]string { return nil }
	t.Setenv("ZELLIJ_SESSION_NAME", "s")
	t.Setenv("ZELLIJ_PANE_ID", "terminal_7")
	StdinValue = strings.NewReader(`{"session_id":"x"}`)
	if code := Hook("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := readFile(t, tty.Name()); got != "\x1b]7501;state=working:app=claude-code\x1b\\" {
		t.Errorf("osc = %q", got)
	}
	// Outside zellij/tmux: silent, no OSC.
	t.Setenv("ZELLIJ_SESSION_NAME", "")
	if err := os.Truncate(tty.Name(), 0); err != nil {
		t.Fatal(err)
	}
	StdinValue = strings.NewReader(`{}`)
	if code := Hook("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := readFile(t, tty.Name()); got != "" {
		t.Errorf("outside zellij emitted %q", got)
	}
	// Non-interactive ancestor: silent.
	t.Setenv("ZELLIJ_SESSION_NAME", "s")
	AncestorsFunc = func() [][]string { return [][]string{{"claude", "-p", "q"}} }
	if code := Hook("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := readFile(t, tty.Name()); got != "" {
		t.Errorf("headless emitted %q", got)
	}
}

func TestHookNormalizedTransport(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	t.Setenv("SPACES_DIR", t.TempDir())
	tty, _ := os.CreateTemp("", "tty-*")
	tty.Close()
	defer os.Remove(tty.Name())
	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	AncestorsFunc = func() [][]string { return nil }
	t.Setenv("ZELLIJ_SESSION_NAME", "s1")
	t.Setenv("ZELLIJ_PANE_ID", "5")
	var published map[string]any
	Publish = func(e map[string]any) bool { published = e; return true }
	HookDisabled = func() bool { return false }
	NormalizeEvent = func(agent, event string, payload map[string]any, env map[string]string) map[string]any {
		return map[string]any{"event": "prompt", "state": "working", "app": "claude-code"}
	}
	DiscoverTitleFunc = func(agent, cwd, sessionID string) string {
		if agent == "claude" && sessionID == "abc" {
			return "Fix auth bug"
		}
		return ""
	}
	StdinValue = strings.NewReader(`{"session_id":"abc"}`)
	if code := HookClient("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if published["session"] != "s1" || published["pane"] != "5" {
		t.Errorf("envelope = %v", published)
	}
	if norm, _ := published["normalized"].(map[string]any); norm["title"] != "Fix auth bug" {
		t.Errorf("title not enriched: %v", norm)
	}
	if got := readFile(t, tty.Name()); got != "\x1b]7501;state=working:app=claude-code\x1b\\" {
		t.Errorf("osc = %q", got)
	}
	// When spacesd is offline and Publish fails, fail silently without spooling or spawning legacy hook-server.
	var spooled map[string]any
	var ensured string
	Publish = func(map[string]any) bool { return false }
	AppendSpool = func(e map[string]any) error { spooled = e; return nil }
	EnsureServer = func(h string) bool { ensured = h; return true }
	StdinValue = strings.NewReader(`{"session_id":"abc"}`)
	if code := SessionHook("claude", "prompt"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if spooled != nil || ensured != "" {
		t.Errorf("expected no legacy spool/spawn when spacesd offline: spool=%v ensured=%q", spooled, ensured)
	}
}

func TestHooksDegradedAndUsage(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	HooksStatus, HooksStop, HooksStart, Daemonize, Serve = nil, nil, nil, nil, nil
	_, errPath := captureOutput(t)
	if code := Hooks(nil); code != 2 {
		t.Errorf("code = %d", code)
	}
	if got := readFile(t, errPath); !strings.Contains(got, "hook core unavailable") {
		t.Errorf("stderr = %q", got)
	}
	if code := HookServer(nil); code != 2 {
		t.Errorf("server code = %d", code)
	}
	if code := SpaceHooks(nil); code != 0 {
		t.Errorf("space hooks code = %d", code)
	}
}

func TestHooksDispatch(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	HooksStatus = func() int { return 3 }
	HooksStop = func() int { return 4 }
	HooksStart = func(string) int { return 5 }
	Serve = func(bool, *int) int { return 6 }
	_, errPath := captureOutput(t)
	if code := Hooks(nil); code != 3 {
		t.Errorf("status = %d", code)
	}
	if code := Hooks([]string{"stop"}); code != 4 {
		t.Errorf("stop = %d", code)
	}
	if code := Hooks([]string{"restart"}); code != 5 {
		t.Errorf("restart = %d", code)
	}
	if code := Hooks([]string{"serve"}); code != 6 {
		t.Errorf("serve = %d", code)
	}
	if code := Hooks([]string{"bogus"}); code != 2 {
		t.Errorf("bogus = %d", code)
	}
	if got := readFile(t, errPath); !strings.Contains(got, "usage:") {
		t.Errorf("stderr = %q", got)
	}
	Daemonize = func() int { return 7 }
	if code := HookServer([]string{"--daemon"}); code != 7 {
		t.Errorf("daemon = %d", code)
	}
	var gotPort *int
	Serve = func(_ bool, p *int) int { gotPort = p; return 0 }
	if code := HookServer([]string{"--tcp", "8099"}); code != 0 || gotPort == nil || *gotPort != 8099 {
		t.Errorf("tcp = %d port = %v", code, gotPort)
	}
}

func TestSpaceHooksDispatch(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()
	outPath, errPath := captureOutput(t)
	SpaceHooksSummary = func() map[string]any {
		return map[string]any{"ok": true, "version": 1, "panes": map[string]any{}}
	}
	SpaceHooksDetail = func(s string, p any) map[string]any {
		return map[string]any{"session": s, "pane": p}
	}
	if code := SpaceHooks(nil); code != 0 {
		t.Fatalf("code = %d", code)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, outPath))), &summary); err != nil {
		t.Fatal(err)
	}
	if summary["ok"] != true {
		t.Errorf("summary = %v", summary)
	}
	resetFile(t, Stdout, outPath)
	if code := SpaceHooks([]string{"--detail", "s1", "3"}); code != 0 {
		t.Fatalf("detail code = %d", code)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(readFile(t, outPath))), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["pane"] != float64(3) {
		t.Errorf("pane = %v", detail)
	}
	if code := SpaceHooks([]string{"--detail", "only"}); code != 2 {
		t.Errorf("short detail = %d", code)
	}
	if got := readFile(t, errPath); !strings.Contains(got, "usage: __space-hooks") {
		t.Errorf("stderr = %q", got)
	}
}

package client

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEndpointFile(t *testing.T, dir string, ep DaemonEndpoint) {
	t.Helper()
	raw, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("marshal endpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, daemonEndpointFileName), raw, 0o600); err != nil {
		t.Fatalf("write endpoint: %v", err)
	}
}

func startMockDaemonUDS(t *testing.T, sockPath string, expectedToken string, authOK bool) <-chan []udsRPCRequest {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	})

	received := make(chan []udsRPCRequest, 1)
	go func() {
		defer close(received)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

		reader := bufio.NewReader(conn)
		var frames []udsRPCRequest

		// 1. rpc.auth
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var authReq udsRPCRequest
		if err := json.Unmarshal(line, &authReq); err != nil {
			return
		}
		frames = append(frames, authReq)

		var authResp map[string]any
		if !authOK {
			authResp = map[string]any{
				"jsonrpc": "2.0",
				"id":      authReq.ID,
				"error":   map[string]any{"code": -32001, "message": "unauthorized"},
			}
		} else {
			authResp = map[string]any{
				"jsonrpc": "2.0",
				"id":      authReq.ID,
				"result": map[string]any{
					"ok":               true,
					"protocol_version": 1,
					"boot_id":          "boot_test",
					"client_id":        "go-hook-client",
					"current_seq":      1,
				},
			}
		}
		raw, _ := json.Marshal(authResp)
		_, _ = conn.Write(append(raw, '\n'))
		if !authOK {
			received <- frames
			return
		}

		// 2. hook.emit
		line, err = reader.ReadBytes('\n')
		if err != nil {
			received <- frames
			return
		}
		var emitReq udsRPCRequest
		if err := json.Unmarshal(line, &emitReq); err != nil {
			received <- frames
			return
		}
		frames = append(frames, emitReq)

		emitResp := map[string]any{
			"jsonrpc": "2.0",
			"id":      emitReq.ID,
			"result":  map[string]any{"ok": true, "state": "working"},
		}
		raw, _ = json.Marshal(emitResp)
		_, _ = conn.Write(append(raw, '\n'))
		received <- frames
	}()
	return received
}

func TestLoadDaemonEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPACES_DIR", dir)

	// Missing endpoint file fails.
	if _, err := LoadDaemonEndpoint(); err == nil {
		t.Fatal("expected error for missing daemon.endpoint.json")
	}

	// Valid endpoint loads cleanly with SPACES_DIR override.
	want := DaemonEndpoint{
		Version:   1,
		Protocol:  "ndjson-rpc/uds",
		PID:       4242,
		UID:       1000,
		Socket:    filepath.Join(dir, "daemon.sock"),
		Token:     "tok-secret-123",
		BootID:    "boot_1",
		StartedMs: 1700000000000,
	}
	writeEndpointFile(t, dir, want)
	got, err := LoadDaemonEndpoint()
	if err != nil {
		t.Fatalf("LoadDaemonEndpoint() error = %v", err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}

	// Invalid protocol fails.
	badProto := want
	badProto.Protocol = "http/tcp"
	writeEndpointFile(t, dir, badProto)
	if _, err := LoadDaemonEndpoint(); err == nil {
		t.Fatal("expected error for invalid protocol")
	}

	// Empty token fails.
	badTok := want
	badTok.Token = "   "
	writeEndpointFile(t, dir, badTok)
	if _, err := LoadDaemonEndpoint(); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestEmitHookToDaemonUDSExchange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPACES_DIR", dir)
	sockPath := filepath.Join(dir, "daemon.sock")

	received := startMockDaemonUDS(t, sockPath, "tok-abc", true)
	writeEndpointFile(t, dir, DaemonEndpoint{
		Version:   1,
		Protocol:  "ndjson-rpc/uds",
		PID:       1234,
		UID:       1000,
		Socket:    sockPath,
		Token:     "tok-abc",
		BootID:    "boot_test",
		StartedMs: 1700000000000,
	})

	ok := EmitHookToDaemon(HookEmitParams{
		Agent:     "claude",
		Event:     "prompt",
		Session:   "dev",
		Pane:      "3",
		PaneRef:   "%3",
		CWD:       "/repo",
		SessionID: "sess_99",
		Profile:   "work",
		Title:     "Fix tests",
		Payload:   map[string]any{"session_id": "sess_99"},
		TsMs:      1700000001234,
	})
	if !ok {
		t.Fatal("expected EmitHookToDaemon to return true")
	}

	frames := <-received
	if len(frames) != 2 {
		t.Fatalf("expected 2 RPC frames, got %d", len(frames))
	}

	if frames[0].JSONRPC != "2.0" || frames[0].Method != "rpc.auth" {
		t.Fatalf("unexpected auth frame: %+v", frames[0])
	}
	authParams, _ := frames[0].Params.(map[string]any)
	if authParams["protocol_version"] != float64(1) ||
		authParams["client_kind"] != "hook_client" ||
		authParams["client_id"] != "go-hook-client" ||
		authParams["token"] != "tok-abc" {
		t.Fatalf("unexpected rpc.auth params: %+v", authParams)
	}

	if frames[1].JSONRPC != "2.0" || frames[1].Method != "hook.emit" {
		t.Fatalf("unexpected emit frame: %+v", frames[1])
	}
	emitParams, _ := frames[1].Params.(map[string]any)
	if emitParams["agent"] != "claude" ||
		emitParams["event"] != "prompt" ||
		emitParams["session"] != "dev" ||
		emitParams["pane"] != "3" ||
		emitParams["pane_ref"] != "%3" ||
		emitParams["session_id"] != "sess_99" ||
		emitParams["profile"] != "work" ||
		emitParams["title"] != "Fix tests" {
		t.Fatalf("unexpected hook.emit params: %+v", emitParams)
	}
}

func TestHookClientEmitsToSpacesdUDSFirst(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()

	dir := t.TempDir()
	t.Setenv("SPACES_DIR", dir)
	t.Setenv("ZELLIJ_SESSION_NAME", "s1")
	t.Setenv("ZELLIJ_PANE_ID", "terminal_7")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("HARNESS_PROFILE", "work")

	sockPath := filepath.Join(dir, "daemon.sock")
	received := startMockDaemonUDS(t, sockPath, "tok-live", true)
	writeEndpointFile(t, dir, DaemonEndpoint{
		Version:   1,
		Protocol:  "ndjson-rpc/uds",
		PID:       555,
		UID:       1000,
		Socket:    sockPath,
		Token:     "tok-live",
		BootID:    "boot_live",
		StartedMs: 1700000000000,
	})

	tty, err := os.CreateTemp("", "tty-*")
	if err != nil {
		t.Fatal(err)
	}
	tty.Close()
	defer os.Remove(tty.Name())

	TtyPathsFunc = func() []string { return []string{tty.Name()} }
	AncestorsFunc = func() [][]string { return nil }
	HookDisabled = func() bool { return false }
	NormalizeEvent = func(agent, event string, payload map[string]any, env map[string]string) map[string]any {
		return map[string]any{
			"event": "prompt",
			"state": "working",
			"app":   "claude-code",
			"cwd":   "/repo/app",
		}
	}
	legacyPublishCalled := false
	spoolCalled := false
	ensureCalled := false
	Publish = func(map[string]any) bool { legacyPublishCalled = true; return true }
	AppendSpool = func(map[string]any) error { spoolCalled = true; return nil }
	EnsureServer = func(string) bool { ensureCalled = true; return true }
	StdinValue = strings.NewReader(`{"session_id":"sid-7"}`)

	if code := HookClient("claude", "prompt"); code != 0 {
		t.Fatalf("HookClient code = %d, want 0", code)
	}

	frames := <-received
	if len(frames) != 2 || frames[1].Method != "hook.emit" {
		t.Fatalf("expected rpc.auth + hook.emit on UDS, got %+v", frames)
	}
	if legacyPublishCalled || spoolCalled || ensureCalled {
		t.Errorf("legacy fallback should not run on UDS success: publish=%v spool=%v ensure=%v",
			legacyPublishCalled, spoolCalled, ensureCalled)
	}
	if got := readFile(t, tty.Name()); got != "\x1b]7501;state=working:app=claude-code\x1b\\" {
		t.Errorf("local OSC degraded unexpectedly: %q", got)
	}
}

func TestEmitHookToDaemonSilentFallback(t *testing.T) {
	snap := saveSeams()
	defer snap.restore()

	// 1. Missing endpoint file -> fast false.
	dir := t.TempDir()
	t.Setenv("SPACES_DIR", dir)
	start := time.Now()
	if EmitHookToDaemon(HookEmitParams{Agent: "claude", Event: "prompt"}) {
		t.Fatal("expected false when endpoint file is missing")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("missing endpoint took too long: %v", elapsed)
	}

	// 2. Unreachable socket -> fast false.
	writeEndpointFile(t, dir, DaemonEndpoint{
		Version:   1,
		Protocol:  "ndjson-rpc/uds",
		PID:       99999,
		UID:       1000,
		Socket:    filepath.Join(dir, "nonexistent.sock"),
		Token:     "tok",
		BootID:    "boot_x",
		StartedMs: 1,
	})
	start = time.Now()
	if EmitHookToDaemon(HookEmitParams{Agent: "claude", Event: "prompt"}) {
		t.Fatal("expected false when socket is unreachable")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("unreachable socket took too long: %v", elapsed)
	}

	// 3. Auth rejected -> fast false.
	sockPath := filepath.Join(dir, "reject.sock")
	_ = startMockDaemonUDS(t, sockPath, "expected", false)
	writeEndpointFile(t, dir, DaemonEndpoint{
		Version:   1,
		Protocol:  "ndjson-rpc/uds",
		PID:       999,
		UID:       1000,
		Socket:    sockPath,
		Token:     "wrong",
		BootID:    "boot_x",
		StartedMs: 1,
	})
	if EmitHookToDaemon(HookEmitParams{Agent: "claude", Event: "prompt"}) {
		t.Fatal("expected false when rpc.auth is rejected")
	}

	// 4. HookClient exits 0 silently without spooling or spawning legacy hook-server.
	t.Setenv("SPACES_DIR", t.TempDir())
	t.Setenv("ZELLIJ_SESSION_NAME", "s1")
	t.Setenv("ZELLIJ_PANE_ID", "9")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	AncestorsFunc = func() [][]string { return nil }
	TtyPathsFunc = func() []string { return nil }
	HookDisabled = nil
	NormalizeEvent = nil
	spoolCalled := false
	ensureCalled := false
	AppendSpool = func(map[string]any) error { spoolCalled = true; return nil }
	EnsureServer = func(string) bool { ensureCalled = true; return true }
	StdinValue = strings.NewReader(`{"session_id":"s-off"}`)

	if code := HookClient("claude", "stop"); code != 0 {
		t.Fatalf("HookClient code = %d, want 0", code)
	}
	if spoolCalled || ensureCalled {
		t.Fatalf("expected no spool/spawn on offline daemon: spool=%v ensure=%v", spoolCalled, ensureCalled)
	}
}

package core

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func captureStdout(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return code, string(out)
}

// testServer spins up the real handler on a loopback TCP socket backed by
// the temp runtime.
func testServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	h := &hookHandler{token: token}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func postJSON(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-ZJ-Token", token)
	}
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp
}

func TestHookRoutes(t *testing.T) {
	setupRuntime(t)
	const token = "test-token"
	srv := testServer(t, token)

	t.Run("health", func(t *testing.T) {
		for _, p := range []string{"/health", "/v1/health"} {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(body), `"version":1`) {
				t.Errorf("GET %s = %d %s", p, resp.StatusCode, body)
			}
		}
	})

	t.Run("get unknown is 404", func(t *testing.T) {
		for _, p := range []string{"/nope", "/v1/hooks", "/v1/hook"} {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
			}
		}
	})

	t.Run("post needs token", func(t *testing.T) {
		if resp := postJSON(t, srv.URL+"/v1/hook", "", "{}"); resp.StatusCode != 401 {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		if resp := postJSON(t, srv.URL+"/v1/hook", "wrong", "{}"); resp.StatusCode != 401 {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("post bad json is 400", func(t *testing.T) {
		if resp := postJSON(t, srv.URL+"/v1/hook", token, "{nope"); resp.StatusCode != 400 {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		if resp := postJSON(t, srv.URL+"/v1/hook", token, "[1,2]"); resp.StatusCode != 400 {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("post missing session is 400", func(t *testing.T) {
		if resp := postJSON(t, srv.URL+"/v1/hook", token, "{}"); resp.StatusCode != 400 {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("post unknown event acked 204", func(t *testing.T) {
		body := `{"session":"s","pane":1,"agent":"claude","event":"Nope"}`
		if resp := postJSON(t, srv.URL+"/v1/hook", token, body); resp.StatusCode != 204 {
			t.Errorf("status = %d, want 204", resp.StatusCode)
		}
	})

	t.Run("post normalized merges 204", func(t *testing.T) {
		body := `{"session":"s","pane":2,"agent":"claude","normalized":` +
			`{"v":1,"agent":"claude","app":"claude-code","event":"pre_tool","state":"working",` +
			`"text":"Bash","ts_ms":1700000000000,"seq":1}}`
		if resp := postJSON(t, srv.URL+"/v1/hook", token, body); resp.StatusCode != 204 {
			t.Errorf("status = %d, want 204", resp.StatusCode)
		}
		if mustGet(t, LoadStore(), "panes", "s", "2")["state"] != "working" {
			t.Errorf("store not merged")
		}
	})

	t.Run("post raw payload normalizes", func(t *testing.T) {
		body := `{"session":"s","pane":3,"agent":"claude","event":"PreToolUse",` +
			`"payload":{"tool_name":"Bash","tool_input":{"command":"ls"}}}`
		if resp := postJSON(t, srv.URL+"/v1/hook", token, body); resp.StatusCode != 204 {
			t.Errorf("status = %d, want 204", resp.StatusCode)
		}
		if mustGet(t, LoadStore(), "panes", "s", "3")["text"] != "Bash · ls" {
			t.Errorf("payload not normalized")
		}
	})

	t.Run("post path agent", func(t *testing.T) {
		body := `{"session":"s","pane":4,"event":"Stop","payload":{"session_id":"x"}}`
		if resp := postJSON(t, srv.URL+"/v1/hook/codex", token, body); resp.StatusCode != 204 {
			t.Errorf("status = %d, want 204", resp.StatusCode)
		}
		if mustGet(t, LoadStore(), "panes", "s", "4")["agent"] != "codex" {
			t.Errorf("path agent not used")
		}
	})

	t.Run("post oversized is 413", func(t *testing.T) {
		// Declare a huge Content-Length but send headers only: the
		// server answers 413 from the header, like the Python handler.
		// (A full-body POST would race a connection reset, in Go and in
		// Python alike, since neither reads the over-limit body.)
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "POST /v1/hook HTTP/1.1\r\nHost: x\r\nX-ZJ-Token: %s\r\n"+
			"Content-Type: application/json\r\nContent-Length: %d\r\n"+
			"Connection: close\r\n\r\n", token, MaxBody+1)
		buf := make([]byte, 64)
		n, _ := io.ReadFull(conn, buf)
		if !strings.Contains(string(buf[:n]), "413") {
			t.Errorf("response = %q, want 413", buf[:n])
		}
	})

	t.Run("post chunked is 411", func(t *testing.T) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "POST /v1/hook HTTP/1.1\r\nHost: x\r\nX-ZJ-Token: %s\r\n"+
			"Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n"+
			"Connection: close\r\n\r\n2\r\n{}\r\n0\r\n\r\n", token)
		buf := make([]byte, 64)
		n, _ := io.ReadFull(conn, buf)
		if !strings.Contains(string(buf[:n]), "411") {
			t.Errorf("response = %q, want 411", buf[:n])
		}
	})
}

func TestPublishNoEndpoint(t *testing.T) {
	setupRuntime(t)
	if Publish(map[string]any{"session": "s"}) {
		t.Errorf("publish without endpoint should be false")
	}
}

func TestPublishDisabled(t *testing.T) {
	setupRuntime(t)
	t.Setenv("SPACES_HOOKS_DISABLE", "1")
	if Publish(map[string]any{"session": "s"}) {
		t.Errorf("publish when disabled should be false")
	}
}

func TestEnsureServerNoDaemon(t *testing.T) {
	setupRuntime(t)
	if err := EnsureServer("/bin/true"); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if _, err := os.Stat(StartstampPath()); !os.IsNotExist(err) {
		t.Errorf("no-daemon run must not touch the startstamp")
	}
}

func TestEnsureServerDisabled(t *testing.T) {
	setupRuntime(t)
	t.Setenv("SPACES_HOOKS_DISABLE", "yes")
	if err := EnsureServer("/bin/true"); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
}

func TestEnsureServerThrottle(t *testing.T) {
	setupRuntime(t)
	t.Setenv("SPACES_HOOKS_NO_DAEMON", "")
	marker := filepath.Join(t.TempDir(), "spawns")
	script := filepath.Join(t.TempDir(), "helper.sh")
	scriptBody := "#!/bin/sh\necho spawned >> " + marker + "\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServer(script); err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := EnsureServer(script); err != nil {
		t.Fatalf("second: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("helper never ran: %v", err)
	}
	if lines := strings.Count(string(data), "spawned"); lines != 1 {
		t.Errorf("spawned %d times, want 1 (throttled)", lines)
	}
	// After removing the stamp a new spawn is attempted.
	_ = os.Remove(StartstampPath())
	if err := EnsureServer(script); err != nil {
		t.Fatalf("third: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	data, _ = os.ReadFile(marker)
	if lines := strings.Count(string(data), "spawned"); lines != 2 {
		t.Errorf("spawned %d times, want 2", lines)
	}
}

func TestHooksStatusOutput(t *testing.T) {
	setupRuntime(t)
	code, out := captureStdout(t, HooksStatus)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{
		"server:  not running",
		"store:   0 pane(s)", "spool:   0 pending",
		"runtime: " + RuntimeDir(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"socket:", "tcp:"} {
		if strings.Contains(out, absent) {
			t.Errorf("status should omit %q without endpoint:\n%s", absent, out)
		}
	}
	t.Setenv("SPACES_HOOKS_DISABLE", "1")
	_, out = captureStdout(t, HooksStatus)
	if !strings.Contains(out, "hooks:   disabled (SPACES_HOOKS_DISABLE)") {
		t.Errorf("disabled line missing:\n%s", out)
	}
}

func TestHooksStatusRunning(t *testing.T) {
	setupRuntime(t)
	sock := SocketPath()
	if _, err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	endpoint := map[string]any{
		"version": 1, "protocol": "http/uds", "pid": os.Getpid(),
		"uid": os.Getuid(), "socket": sock, "tcp_port": 1234,
		"token": "x", "started_ms": Now(),
	}
	payload, _ := marshalJSON(endpoint)
	if err := atomicWrite(EndpointPath(), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	_, out := captureStdout(t, HooksStatus)
	for _, want := range []string{
		fmt.Sprintf("server:  running (pid %d)", os.Getpid()),
		"socket:  " + sock,
		"tcp:     127.0.0.1:1234",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestHooksStopWithoutEndpoint(t *testing.T) {
	setupRuntime(t)
	if code := HooksStop(); code != 0 {
		t.Errorf("code = %d", code)
	}
}

func TestHooksStartFailed(t *testing.T) {
	setupRuntime(t)
	code, out := captureStdout(t, func() int { return HooksStart("/bin/true") })
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(out, "zj hooks: failed to start") {
		t.Errorf("output = %q", out)
	}
}

func TestHooksStartAlreadyRunning(t *testing.T) {
	setupRuntime(t)
	sock := SocketPath()
	if _, err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	endpoint := map[string]any{
		"version": 1, "protocol": "http/uds", "pid": os.Getpid(),
		"uid": os.Getuid(), "socket": sock, "tcp_port": nil,
		"token": "x", "started_ms": Now(),
	}
	payload, _ := marshalJSON(endpoint)
	if err := atomicWrite(EndpointPath(), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := captureStdout(t, func() int { return HooksStart("/bin/true") })
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(out, "zj hooks: already running") {
		t.Errorf("output = %q", out)
	}
}

// TestServeChild is the in-test-binary listener used by
// TestServerPublishAndSpoolDrain, mirroring the Python test's
// `python -c ... serve(...)` subprocess. The parent passes its frozen
// clock through ZJCORE_TEST_NOW_MS so TTL decisions match.
func TestServeChild(t *testing.T) {
	if os.Getenv("ZJCORE_TEST_SERVE_CHILD") != "1" {
		return
	}
	if ms, err := strconv.ParseInt(os.Getenv("ZJCORE_TEST_NOW_MS"), 10, 64); err == nil {
		Now = func() int64 { return ms }
	}
	os.Exit(Serve(true, -1))
}

func waitEndpoint(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		endpoint := ReadEndpoint()
		if endpoint != nil {
			if sock, ok := endpoint["socket"].(string); ok && SocketReady(sock) {
				return endpoint
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("endpoint never became ready")
	return nil
}

func TestServerPublishAndSpoolDrain(t *testing.T) {
	setupRuntime(t)
	// Spool one event before the server starts; startup must drain it.
	pre := NormalizeEvent("claude", "Stop", map[string]any{
		"session_id": "s", "last_assistant_message": "done",
	}, map[string]string{})
	if pre == nil {
		t.Fatal("nil event")
	}
	spooled := map[string]any{"session": "probe", "pane": 7}
	for k, v := range pre {
		spooled[k] = v
	}
	if err := AppendSpool(spooled); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestServeChild")
	cmd.Env = append(os.Environ(),
		"ZJCORE_TEST_SERVE_CHILD=1",
		fmt.Sprintf("ZJCORE_TEST_NOW_MS=%d", Now()),
	)
	var childStderr bytes.Buffer
	cmd.Stderr = &childStderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait(); close(childDone) }()
	t.Cleanup(func() {
		select {
		case <-childDone:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("child did not exit; stderr: %s", childStderr.String())
		}
	})

	endpoint := waitEndpoint(t, 10*time.Second)
	time.Sleep(500 * time.Millisecond) // let the startup drain settle

	// A second owner must exit immediately without disturbing the server.
	before, _ := marshalJSON(ReadEndpoint())
	if code := Serve(true, 0); code != 0 {
		t.Errorf("second Serve = %d, want 0", code)
	}
	after, _ := marshalJSON(ReadEndpoint())
	if !bytes.Equal(before, after) {
		t.Errorf("second owner changed the endpoint")
	}

	summary := SpaceHooksSummary()
	pane := mustGet(t, summary, "panes", "probe", "7")
	if pane["state"] != "done" {
		t.Errorf("drained state = %v, want done", pane["state"])
	}

	// Publish a fresh event over the UDS.
	fresh := NormalizeEvent("claude", "UserPromptSubmit", map[string]any{
		"session_id": "s", "prompt": "again",
	}, map[string]string{})
	if fresh == nil {
		t.Fatal("nil fresh event")
	}
	if !Publish(map[string]any{
		"session": "probe", "pane": 7, "agent": "claude",
		"event": "UserPromptSubmit", "normalized": fresh,
	}) {
		t.Errorf("publish over UDS failed")
	}
	if got := mustGet(t, SpaceHooksSummary(), "panes", "probe", "7")["state"]; got != "working" {
		t.Errorf("state after publish = %v, want working", got)
	}

	// TCP serves the same routes; the token is still required.
	port := toInt64(endpoint["tcp_port"])
	if port <= 0 {
		t.Fatalf("no tcp port in endpoint: %v", endpoint)
	}
	tcpURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	token, _ := endpoint["token"].(string)
	normalizedStop := NormalizeEvent("claude", "Stop", map[string]any{"session_id": "t"}, map[string]string{})
	stopBody, _ := marshalJSON(map[string]any{
		"session": "probe", "pane": 8, "agent": "claude", "normalized": normalizedStop,
	})
	req, _ := http.NewRequest("POST", tcpURL+"/v1/hook", bytes.NewReader(stopBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ZJ-Token", token)
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tcp post: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("tcp status = %d, want 204", resp.StatusCode)
	}
	bad, _ := http.NewRequest("POST", tcpURL+"/v1/hook", strings.NewReader("{}"))
	bad.Header.Set("Content-Type", "application/json")
	bad.Close = true
	badResp, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatalf("tcp bad post: %v", err)
	}
	_, _ = io.ReadAll(badResp.Body)
	_ = badResp.Body.Close()
	if badResp.StatusCode != 401 {
		t.Errorf("tcp unauthenticated status = %d, want 401", badResp.StatusCode)
	}

	// HooksStop terminates the child and cleans up.
	if code := HooksStop(); code != 0 {
		t.Errorf("HooksStop = %d", code)
	}
	select {
	case err := <-childDone:
		if err != nil {
			t.Errorf("child exit: %v (stderr: %s)", err, childStderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not stop")
	}
	if _, err := os.Stat(EndpointPath()); !os.IsNotExist(err) {
		t.Errorf("endpoint not cleaned up")
	}
}

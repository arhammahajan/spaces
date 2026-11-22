package core

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// publishTimeout mirrors the 0.2s hook-client timeout: dial plus each
// socket phase.
const publishTimeout = 200 * time.Millisecond

// ReadEndpoint returns the decoded endpoint document, or nil when it is
// missing, malformed or from another version — mirroring read_endpoint.
func ReadEndpoint() map[string]any {
	v := readJSONFile(EndpointPath())
	data, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if toInt64(data["version"]) != Version {
		return nil
	}
	return data
}

// SocketReady probes a unix socket path, mirroring _socket_ready.
func SocketReady(path string) bool {
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// postHook POSTs body to /v1/hook on one transport, returning true on any
// 2xx status. Token newlines are rejected to keep the framed request
// well-formed.
func postHook(network, addr, token string, body []byte) bool {
	if strings.ContainsAny(token, "\r\n") {
		return false
	}
	conn, err := net.DialTimeout(network, addr, publishTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(publishTimeout))
	req := fmt.Sprintf("POST /v1/hook HTTP/1.1\r\nHost: localhost\r\n"+
		"Content-Type: application/json\r\nX-ZJ-Token: %s\r\n"+
		"Content-Length: %d\r\nConnection: close\r\n\r\n",
		token, len(body))
	if _, err := conn.Write([]byte(req)); err != nil {
		return false
	}
	if _, err := conn.Write(body); err != nil {
		return false
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Publish POSTs an envelope to the listener over the unix socket recorded
// in hooks.endpoint.json (falling back to loopback TCP when the endpoint
// advertises a port), mirroring publish.
func Publish(envelope map[string]any) bool {
	if Disabled() {
		return false
	}
	endpoint := ReadEndpoint()
	if endpoint == nil {
		return false
	}
	body := marshalOrderedMap(envelope, spoolKeyOrder)
	token, _ := endpoint["token"].(string)
	if socketFile, ok := endpoint["socket"].(string); ok && socketFile != "" {
		if postHook("unix", socketFile, token, body) {
			return true
		}
	}
	if port := toInt64(endpoint["tcp_port"]); port > 0 {
		if postHook("tcp", fmt.Sprintf("127.0.0.1:%d", port), token, body) {
			return true
		}
	}
	return false
}

// EnsureServer lazily starts the listener without waiting, at most once
// per StartThrottleS, mirroring ensure_server. helperPath is the harness
// binary, relaunched as `helperPath hook-server --daemon`.
func EnsureServer(helperPath string) error {
	if Disabled() || noDaemon() {
		return nil
	}
	if endpoint := ReadEndpoint(); endpoint != nil {
		if socketFile, ok := endpoint["socket"].(string); ok && socketFile != "" {
			if SocketReady(socketFile) {
				return nil
			}
		}
	}
	if _, err := EnsureDir(); err != nil {
		return err
	}
	stamp := StartstampPath()
	if fi, err := os.Stat(stamp); err == nil {
		if time.Since(fi.ModTime()) < StartThrottleS*time.Second {
			return nil
		}
	}
	// Best-effort touch; like the Python, a failed touch still proceeds
	// to the spawn attempt.
	if f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		logMsg(fmt.Sprintf("could not start hook server: %v", err))
		return nil
	}
	defer devnull.Close()
	cmd := exec.Command(helperPath, "hook-server", "--daemon")
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logMsg(fmt.Sprintf("could not start hook server: %v", err))
		return nil
	}
	logMsg(fmt.Sprintf("spawned hook server pid=%d", cmd.Process.Pid))
	return nil
}

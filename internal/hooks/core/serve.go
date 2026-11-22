package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// connKey carries the accepted connection into the request context so the
// handler can apply the UDS same-user check.
type connKey struct{}

// hookHandler serves the listener routes, mirroring _HookHandler. Only the
// exact routes/methods/status codes of the Python do_GET/do_POST exist:
// GET /health and /v1/health, POST /v1/hook[/...]. Anything else is an
// error; the Python sends 501 with an HTML body for unknown methods, which
// no caller depends on, so this port answers JSON uniformly.
type hookHandler struct {
	token string
}

func (h *hookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if r.URL.Path == "/health" || r.URL.Path == "/v1/health" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
	case "POST":
		h.handleHook(w, r)
	default:
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false})
	}
}

func writeJSON(w http.ResponseWriter, status int, v map[string]any) {
	body, err := marshalJSON(v)
	if err != nil {
		body = []byte(`{"ok":false}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// authorized mirrors _HookHandler._authorized: the token must match, and on
// UDS the peer must additionally be the same uid (Linux SO_PEERCRED).
func (h *hookHandler) authorized(r *http.Request) bool {
	if r.Header.Get("X-ZJ-Token") != h.token {
		return false
	}
	if conn, ok := r.Context().Value(connKey{}).(net.Conn); ok {
		if _, isUnix := conn.(*net.UnixConn); isUnix {
			if uid, ok := connUID(conn); ok && uid != os.Getuid() {
				return false
			}
		}
	}
	return true
}

func (h *hookHandler) handleHook(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	var pathAgent *string
	if p != "/v1/hook" {
		if !strings.HasPrefix(p, "/v1/hook/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
			return
		}
		rest := p[len("/v1/hook/"):]
		pathAgent = &rest
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	// net/http reports -1 when no Content-Length was sent (or chunked);
	// the Python answers 411 Length Required in exactly that case.
	if r.ContentLength < 0 {
		writeJSON(w, http.StatusLengthRequired, map[string]any{"ok": false})
		return
	}
	if r.ContentLength > MaxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || int64(len(raw)) > MaxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false})
		return
	}
	v, err := decodeJSON(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	envelope, ok := v.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	// Never let a bad payload kill the listener.
	accepted := false
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				logMsg(fmt.Sprintf("hook handler error: %v", rec))
			}
		}()
		accepted = acceptEnvelope(envelope, pathAgent, r)
	}()
	if accepted {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
}

// acceptEnvelope merges one hook envelope into the store, mirroring
// _accept_envelope. Unknown (stateless) events are acknowledged but
// ignored.
func acceptEnvelope(envelope map[string]any, pathAgent *string, r *http.Request) bool {
	session, _ := envelope["session"].(string)
	if session == "" {
		session = r.Header.Get("X-ZJ-Session")
	}
	if session == "" {
		session = r.URL.Query().Get("session")
	}
	var pane any
	if p, ok := envelope["pane"]; ok && p != nil {
		pane = p
	} else if h := r.Header.Get("X-ZJ-Pane"); h != "" {
		pane = h
	} else if q := r.URL.Query().Get("pane"); q != "" {
		pane = q
	}
	if session == "" || pane == nil {
		return false
	}
	var agent string
	if pathAgent != nil && *pathAgent != "" {
		agent = *pathAgent
	} else if a, ok := envelope["agent"].(string); ok && a != "" {
		agent = a
	} else {
		agent = "unknown"
	}
	if normalized, ok := envelope["normalized"].(map[string]any); ok {
		event := make(map[string]any, len(normalized))
		for k, v := range normalized {
			event[k] = v
		}
		if err := MergeIntoStore(session, pane, event); err != nil {
			logMsg(fmt.Sprintf("hook handler error: %v", err))
			return false
		}
		return true
	}
	payload, present := envelope["payload"]
	var pmap map[string]any
	if !present {
		// The envelope itself doubles as the payload (mirrors
		// `payload = envelope.get("payload", envelope)`).
		pmap = envelope
	} else {
		var isMap bool
		pmap, isMap = payload.(map[string]any)
		if !isMap {
			return false
		}
	}
	rawEvent, _ := envelope["event"].(string)
	event := NormalizeEvent(agent, rawEvent, pmap, nil)
	if event == nil {
		return true // acknowledged but ignored
	}
	if err := MergeIntoStore(session, pane, event); err != nil {
		logMsg(fmt.Sprintf("hook handler error: %v", err))
		return false
	}
	return true
}

// randomHex returns n random bytes as hex, mirroring secrets.token_hex(n).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// removeStale drops a dead socket/endpoint pair, mirroring _remove_stale: a
// live pid keeps everything in place.
func removeStale(socket string) {
	if endpoint := ReadEndpoint(); endpoint != nil {
		if pid := toInt64(endpoint["pid"]); pid > 0 {
			if err := syscall.Kill(int(pid), 0); err == nil {
				return
			} else if err != syscall.ESRCH {
				return
			}
		}
	}
	_ = os.Remove(socket)
	_ = os.Remove(EndpointPath())
}

// writeEndpoint records the listener endpoint atomically (0600), mirroring
// _write_endpoint.
func writeEndpoint(token string, tcpPort *int) {
	var port any
	if tcpPort != nil {
		port = *tcpPort
	}
	endpoint := map[string]any{
		"version":    Version,
		"protocol":   "http/uds",
		"pid":        os.Getpid(),
		"uid":        os.Getuid(),
		"socket":     SocketPath(),
		"tcp_port":   port,
		"token":      token,
		"started_ms": nowMs(),
	}
	_ = atomicWrite(EndpointPath(), marshalOrderedMap(endpoint, endpointKeyOrder), 0o600)
}

// Serve runs the hook listener, mirroring serve: it takes the server lock
// (a second owner exits 0 immediately), drains the spool, and serves the
// unix socket plus optional loopback TCP until SIGTERM/SIGINT. tcpPort > 0
// binds that port; tcpPort < 0 binds an ephemeral port (used by tests);
// tcpPort == 0 disables TCP, mirroring Python `if tcp_port:`.
func Serve(foreground bool, tcpPort int) int {
	if _, err := EnsureDir(); err != nil {
		return 1
	}
	lock, err := os.OpenFile(LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 1
	}
	held := true
	defer func() {
		if held {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
		_ = lock.Close()
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		held = false
		return 0
	}
	token, err := randomHex(16)
	if err != nil {
		return 1
	}
	removeStale(SocketPath())
	unixListener, err := net.Listen("unix", SocketPath())
	if err != nil {
		logMsg(fmt.Sprintf("could not bind unix socket: %v", err))
		return 1
	}
	_ = os.Chmod(SocketPath(), 0o600)
	defer unixListener.Close()

	var tcpListener net.Listener
	var boundPort *int
	if tcpPort != 0 {
		port := tcpPort
		if port < 0 {
			port = 0
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			logMsg(fmt.Sprintf("could not bind TCP listener: %v", err))
		} else {
			defer l.Close()
			tcpListener = l
			if addr, ok := l.Addr().(*net.TCPAddr); ok {
				p := addr.Port
				boundPort = &p
			}
		}
	}
	writeEndpoint(token, boundPort)

	_ = DrainSpool()

	handler := &hookHandler{token: token}
	withConn := func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connKey{}, c)
	}
	unixServer := &http.Server{Handler: handler, ConnContext: withConn}
	go func() { _ = unixServer.Serve(unixListener) }()
	var tcpServer *http.Server
	if tcpListener != nil {
		tcpServer = &http.Server{Handler: handler, ConnContext: withConn}
		go func() { _ = tcpServer.Serve(tcpListener) }()
	}
	if !foreground {
		logMsg(fmt.Sprintf("listener started pid=%d socket=%s", os.Getpid(), SocketPath()))
	}

	stopOnce := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopOnce:
				return
			case <-ticker.C:
				_ = DrainSpool()
			}
		}
	}()

	var watchCancel context.CancelFunc
	var watchDone chan struct{}
	if DaemonWatchPaneLister != nil && DaemonWatchResolver != nil {
		var watchCtx context.Context
		watchCtx, watchCancel = context.WithCancel(context.Background())
		watchDone = make(chan struct{})
		mgr := NewDaemonWatchManager(DaemonWatchPaneLister, DaemonWatchResolver, MergeIntoStore)
		go func() {
			defer close(watchDone)
			mgr.Run(watchCtx, 2*time.Second)
		}()
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	signal.Stop(sigCh)
	close(stopOnce)
	if watchCancel != nil {
		watchCancel()
		select {
		case <-watchDone:
		case <-time.After(3 * time.Second):
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = unixServer.Shutdown(ctx)
	if tcpServer != nil {
		_ = tcpServer.Shutdown(ctx)
	}
	_ = os.Remove(SocketPath())
	_ = os.Remove(EndpointPath())
	return 0
}

// Daemonize detaches a listener child, mirroring daemonize. Go cannot fork,
// so this re-executes the current binary with the same arguments in a new
// session (stdio on the hooks log / /dev/null); the re-executed child sees
// the marker env var and serves in the foreground slot instead of
// re-daemonizing.
func Daemonize() int {
	const marker = "_ZJ_HOOKS_DAEMON_CHILD"
	if os.Getenv(marker) == "1" {
		return Serve(false, 0)
	}
	if _, err := EnsureDir(); err != nil {
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	logFile, err := os.OpenFile(LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logFile, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return 1
		}
	}
	defer logFile.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 1
	}
	defer devnull.Close()
	env := append(append([]string{}, os.Environ()...), marker+"=1")
	_, err = syscall.ForkExec(self, os.Args, &syscall.ProcAttr{
		Dir:   "/",
		Files: []uintptr{devnull.Fd(), logFile.Fd(), logFile.Fd()},
		Sys:   &syscall.SysProcAttr{Setsid: true},
		Env:   env,
	})
	if err != nil {
		logMsg(fmt.Sprintf("could not daemonize hook server: %v", err))
		return 1
	}
	return 0
}

// HooksStatus prints listener/store/spool status, mirroring hooks_status.
func HooksStatus() int {
	endpoint := ReadEndpoint()
	if endpoint != nil {
		pid := toInt64(endpoint["pid"])
		alive := false
		if pid > 0 {
			alive = syscall.Kill(int(pid), 0) == nil
		}
		socketFile, _ := endpoint["socket"].(string)
		ready := socketFile != "" && SocketReady(socketFile)
		status := "stale"
		if alive && ready {
			status = "running"
		}
		fmt.Printf("server:  %s (pid %d)\n", status, pid)
		fmt.Printf("socket:  %s\n", socketFile)
		if port := toInt64(endpoint["tcp_port"]); port > 0 {
			fmt.Printf("tcp:     127.0.0.1:%d\n", port)
		} else {
			fmt.Printf("tcp:     disabled\n")
		}
	} else {
		fmt.Printf("server:  not running\n")
	}
	store := LoadStore()
	panes := 0
	if storePanes, ok := store["panes"].(map[string]any); ok {
		for _, v := range storePanes {
			if sessionPanes, ok := v.(map[string]any); ok {
				panes += len(sessionPanes)
			}
		}
	}
	fmt.Printf("store:   %d pane(s) (%s)\n", panes, StorePath())
	spool := ReadSpoolEvents(MaxSpoolLines)
	fmt.Printf("spool:   %d pending event(s) (%s)\n", len(spool), SpoolPath())
	fmt.Printf("runtime: %s\n", RuntimeDir())
	if Disabled() {
		fmt.Printf("hooks:   disabled (SPACES_HOOKS_DISABLE)\n")
	}
	return 0
}

// HooksStop terminates the listener and removes its socket/endpoint,
// mirroring hooks_stop.
func HooksStop() int {
	endpoint := ReadEndpoint()
	if endpoint == nil {
		return 0
	}
	if pid := toInt64(endpoint["pid"]); pid > 0 {
		_ = syscall.Kill(int(pid), syscall.SIGTERM)
		for i := 0; i < 20; i++ {
			if err := syscall.Kill(int(pid), 0); err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = os.Remove(EndpointPath())
	_ = os.Remove(SocketPath())
	fmt.Printf("zj hooks: stopped\n")
	return 0
}

// HooksStart launches `helperPath hook-server --daemon` and polls for a
// ready endpoint, mirroring hooks_start.
func HooksStart(helperPath string) int {
	if endpoint := ReadEndpoint(); endpoint != nil {
		if socketFile, ok := endpoint["socket"].(string); ok && socketFile != "" {
			if SocketReady(socketFile) {
				fmt.Printf("zj hooks: already running\n")
				return 0
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helperPath, "hook-server", "--daemon")
	_ = cmd.Run()
	for i := 0; i < 40; i++ {
		if endpoint := ReadEndpoint(); endpoint != nil {
			if socketFile, ok := endpoint["socket"].(string); ok && socketFile != "" {
				if SocketReady(socketFile) {
					fmt.Printf("zj hooks: started\n")
					return 0
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("zj hooks: failed to start\n")
	return 1
}

package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	defaultOpenCodeBackoffInitial   = 250 * time.Millisecond
	defaultOpenCodeBackoffMax       = 5 * time.Second
	defaultOpenCodePIDCheckInterval = 1 * time.Second
	maxSSELineBytes                 = 4 * 1024 * 1024 // 4 MiB
)

// ErrSSELineTooLong is returned by ParseSSEStream when a single SSE line
// exceeds maxSSELineBytes (4 MiB).
var ErrSSELineTooLong = errors.New("sse line exceeds maximum length")

// ParseSSEStream reads a W3C Server-Sent Events stream from r and invokes
// onEvent for each dispatched event (blank-line terminated). It supports
// event:, multi-line data:, id:, retry:, comment lines (:), LF and CRLF line
// endings, and lines up to 4 MiB.
func ParseSSEStream(r io.Reader, onEvent func(eventName, data string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)

	var (
		eventName string
		dataLines []string
		hasData   bool
	)

	dispatch := func() error {
		if !hasData {
			eventName = ""
			dataLines = dataLines[:0]
			return nil
		}
		ev := eventName
		payload := strings.Join(dataLines, "\n")
		eventName = ""
		dataLines = dataLines[:0]
		hasData = false
		if onEvent != nil {
			return onEvent(ev, payload)
		}
		return nil
	}

	for {
		line, eof, err := readSSELine(br, maxSSELineBytes)
		if err != nil {
			return err
		}

		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if eof {
				return nil
			}
			continue
		}

		// Comment line.
		if line[0] == ':' {
			if eof {
				return nil
			}
			continue
		}

		field := line
		value := ""
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			field = line[:idx]
			value = line[idx+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}

		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
		case "id", "retry":
			// Valid SSE fields; no client-side state required for OpenCode SSE.
		}

		if eof {
			return nil
		}
	}
}

func readSSELine(br *bufio.Reader, maxBytes int) (string, bool, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 {
			if len(buf)+len(frag) > maxBytes {
				return "", false, ErrSSELineTooLong
			}
			buf = append(buf, frag...)
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			line := trimSSELineEnding(buf)
			return string(line), true, nil
		}
		return "", false, err
	}
	line := trimSSELineEnding(buf)
	return string(line), false, nil
}

func trimSSELineEnding(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

// OpenCodeDaemonWatcher implements agent.DaemonWatcher for OpenCode's local
// HTTP SSE server.
type OpenCodeDaemonWatcher struct {
	HTTPClient       *http.Client
	BackoffInitial   time.Duration
	BackoffMax       time.Duration
	PIDCheckInterval time.Duration
	PIDAliveFn       func(pid int) bool
	DiscoverFn       func(ctx context.Context, target agent.DaemonWatchTarget) (string, bool)
}

// NewOpenCodeDaemonWatcher constructs an OpenCodeDaemonWatcher with default
// reconnect backoff (250ms -> 5s) and PID liveness polling (1s).
func NewOpenCodeDaemonWatcher() *OpenCodeDaemonWatcher {
	return &OpenCodeDaemonWatcher{
		BackoffInitial:   defaultOpenCodeBackoffInitial,
		BackoffMax:       defaultOpenCodeBackoffMax,
		PIDCheckInterval: defaultOpenCodePIDCheckInterval,
	}
}

func (w *OpenCodeDaemonWatcher) httpClient() *http.Client {
	if w != nil && w.HTTPClient != nil {
		return w.HTTPClient
	}
	return &http.Client{}
}

func (w *OpenCodeDaemonWatcher) backoffInitial() time.Duration {
	if w != nil && w.BackoffInitial > 0 {
		return w.BackoffInitial
	}
	return defaultOpenCodeBackoffInitial
}

func (w *OpenCodeDaemonWatcher) backoffMax() time.Duration {
	if w != nil && w.BackoffMax > 0 {
		return w.BackoffMax
	}
	return defaultOpenCodeBackoffMax
}

func (w *OpenCodeDaemonWatcher) pidCheckInterval() time.Duration {
	if w != nil && w.PIDCheckInterval > 0 {
		return w.PIDCheckInterval
	}
	return defaultOpenCodePIDCheckInterval
}

func (w *OpenCodeDaemonWatcher) isPIDAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	if w != nil && w.PIDAliveFn != nil {
		return w.PIDAliveFn(pid)
	}
	return defaultPIDAlive(pid)
}

// DiscoverEndpoint returns target.Endpoint if configured, otherwise inspects
// the pane process tree on supported platforms.
func (w *OpenCodeDaemonWatcher) DiscoverEndpoint(ctx context.Context, target agent.DaemonWatchTarget) (string, bool) {
	if ep := normalizeOpenCodeEndpoint(target.Endpoint); ep != "" {
		return ep, true
	}
	if w != nil && w.DiscoverFn != nil {
		ep, ok := w.DiscoverFn(ctx, target)
		if !ok {
			return "", false
		}
		if norm := normalizeOpenCodeEndpoint(ep); norm != "" {
			return norm, true
		}
		return "", false
	}
	return discoverOpenCodeEndpoint(ctx, target)
}

// Watch discovers the OpenCode local server endpoint, subscribes to its SSE
// stream (<baseURL>/event with 404 fallback to <baseURL>/global/event), emits
// normalized agent.DaemonEvent records, reconnects with exponential backoff on
// transient disconnects, and exits cleanly when ctx is cancelled or the pane
// PID exits.
func (w *OpenCodeDaemonWatcher) Watch(ctx context.Context, target agent.DaemonWatchTarget, emit agent.DaemonEventSink) error {
	if target.PanePID > 0 && !w.isPIDAlive(target.PanePID) {
		return nil
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var pidExited atomic.Bool
	if target.PanePID > 0 {
		interval := w.pidCheckInterval()
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-watchCtx.Done():
					return
				case <-ticker.C:
					if !w.isPIDAlive(target.PanePID) {
						pidExited.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}

	mapper := NewOpenCodeEventMapper(target)
	backoff := w.backoffInitial()
	maxBackoff := w.backoffMax()
	if maxBackoff < backoff {
		maxBackoff = backoff
	}

	sleepBackoff := func() bool {
		t := time.NewTimer(backoff)
		defer t.Stop()
		select {
		case <-watchCtx.Done():
			return false
		case <-t.C:
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		return true
	}

	for {
		if watchCtx.Err() != nil {
			if pidExited.Load() {
				return nil
			}
			return ctx.Err()
		}

		endpoint, ok := w.DiscoverEndpoint(watchCtx, target)
		if !ok || endpoint == "" {
			if !sleepBackoff() {
				if pidExited.Load() {
					return nil
				}
				return ctx.Err()
			}
			continue
		}

		connected, err := w.streamOnce(watchCtx, endpoint, mapper, emit)
		if connected {
			backoff = w.backoffInitial()
		}
		if watchCtx.Err() != nil {
			if pidExited.Load() {
				return nil
			}
			return ctx.Err()
		}
		_ = err
		if !sleepBackoff() {
			if pidExited.Load() {
				return nil
			}
			return ctx.Err()
		}
	}
}

func (w *OpenCodeDaemonWatcher) streamOnce(
	ctx context.Context,
	baseURL string,
	mapper *OpenCodeEventMapper,
	emit agent.DaemonEventSink,
) (connected bool, err error) {
	base := strings.TrimRight(baseURL, "/")
	resp, err := w.openSSE(ctx, base+"/event")
	if err != nil {
		return false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		resp, err = w.openSSE(ctx, base+"/global/event")
		if err != nil {
			return false, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("opencode sse status %d", resp.StatusCode)
	}

	err = ParseSSEStream(resp.Body, func(eventName, data string) error {
		events := mapper.Map(eventName, data)
		if emit != nil {
			for _, ev := range events {
				emit(ev)
			}
		}
		return nil
	})
	return true, err
}

func (w *OpenCodeDaemonWatcher) openSSE(ctx context.Context, streamURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	return w.httpClient().Do(req)
}

// --- Pure discovery parser helpers (platform-independent for testability) ---

func normalizeOpenCodeEndpoint(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
}

// extractOpenCodeEndpointFromCmdline extracts an OpenCode server endpoint from
// argv flags (`--server <url>`, `--server=<url>`, or `--port <port>` with
// optional `--hostname <host>`).
func extractOpenCodeEndpointFromCmdline(argv []string) (string, bool) {
	var serverURL, port, hostname string
	for i := 0; i < len(argv); i++ {
		tok := strings.TrimSpace(argv[i])
		if tok == "" {
			continue
		}
		switch {
		case tok == "--server" && i+1 < len(argv):
			serverURL = strings.TrimSpace(argv[i+1])
			i++
		case strings.HasPrefix(tok, "--server="):
			serverURL = strings.TrimSpace(strings.TrimPrefix(tok, "--server="))
		case (tok == "--port" || tok == "-p") && i+1 < len(argv):
			port = strings.TrimSpace(argv[i+1])
			i++
		case strings.HasPrefix(tok, "--port="):
			port = strings.TrimSpace(strings.TrimPrefix(tok, "--port="))
		case tok == "--hostname" && i+1 < len(argv):
			hostname = strings.TrimSpace(argv[i+1])
			i++
		case strings.HasPrefix(tok, "--hostname="):
			hostname = strings.TrimSpace(strings.TrimPrefix(tok, "--hostname="))
		}
	}

	if serverURL != "" {
		if norm := normalizeOpenCodeEndpoint(serverURL); norm != "" {
			return norm, true
		}
	}
	if port != "" {
		p, err := strconv.Atoi(port)
		if err == nil && p > 0 && p <= 65535 {
			host := hostname
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
				host = "[" + host + "]"
			}
			return fmt.Sprintf("http://%s:%d", host, p), true
		}
	}
	return "", false
}

// extractOpenCodeEndpointFromEnv extracts OPENCODE_SERVER_URL from a
// NUL-separated /proc/<pid>/environ buffer.
func extractOpenCodeEndpointFromEnv(environ []byte) (string, bool) {
	for _, entry := range bytes.Split(environ, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if bytes.HasPrefix(entry, []byte("OPENCODE_SERVER_URL=")) {
			val := string(bytes.TrimPrefix(entry, []byte("OPENCODE_SERVER_URL=")))
			if norm := normalizeOpenCodeEndpoint(val); norm != "" {
				return norm, true
			}
		}
	}
	return "", false
}

// parseProcNetTCP parses /proc/net/tcp (ipv6=false) or /proc/net/tcp6
// (ipv6=true) and returns a map from socket inode string to loopback HTTP
// endpoint URL for all listening (state 0A) loopback/wildcard sockets.
func parseProcNetTCP(data []byte, ipv6 bool) map[string]string {
	out := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		// Columns: sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode ...
		if len(fields) < 10 {
			continue
		}
		if fields[0] == "sl" {
			continue
		}
		// State 0A == TCP_LISTEN
		if !strings.EqualFold(fields[3], "0A") {
			continue
		}
		addrHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		portVal, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || portVal == 0 {
			continue
		}
		inode := fields[9]
		if inode == "" || inode == "0" {
			continue
		}

		host, ok := loopbackHostFromProcHex(addrHex, ipv6)
		if !ok {
			continue
		}
		out[inode] = fmt.Sprintf("http://%s:%d", host, portVal)
	}
	return out
}

func loopbackHostFromProcHex(addrHex string, ipv6 bool) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(addrHex))
	if !ipv6 {
		if len(upper) != 8 {
			return "", false
		}
		// Little-endian IPv4: 127.0.0.1 -> 0100007F, 0.0.0.0 -> 00000000
		if upper == "0100007F" || upper == "00000000" {
			return "127.0.0.1", true
		}
		// Also accept any 127.x.x.x loopback address (LSB byte == 0x7F in LE).
		b, err := hex.DecodeString(upper)
		if err == nil && len(b) == 4 && b[3] == 0x7f {
			return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0]), true
		}
		return "", false
	}

	if len(upper) != 32 {
		return "", false
	}
	switch upper {
	case "00000000000000000000000000000000": // ::
		return "127.0.0.1", true
	case "00000000000000000000000001000000", // ::1 (Linux /proc/net/tcp6 32-bit LE words)
		"00000000000000000000000000000001": // ::1 (big-endian)
		return "[::1]", true
	case "0000000000000000FFFF00000100007F": // ::ffff:127.0.0.1
		return "127.0.0.1", true
	}
	return "", false
}

// Package core is a Go port of zj_hook_core.py from zellij-spaces.
//
// It owns the parts that both the hook client and the background listener
// need: provider event canonicalization and payload normalization, merging
// an event into a pane record, the per-pane store and the append-only
// spool, HTTP-over-Unix-socket publication and the loopback listener, and
// the __space-hooks summary/detail views consumed by the plugin.
//
// Nothing here executes input from a payload. The listener only parses
// JSON, merges a record and writes files.
//
// Time source: the Now variable (Unix milliseconds) backs every freshness
// decision; tests override it so TTL tests never sleep.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Version matches zj_hook_core.VERSION; it is written into the store and
// the endpoint file and checked when the endpoint is read.
const Version = 1

// Freshness / pruning windows (milliseconds).
const (
	WorkingStaleMS = 120_000
	DoneTTLMS      = 30 * 60_000
	FailedTTLMS    = 30 * 60_000
	BlockTTLMS     = 30 * 60_000
	AuthTTLMS      = 60 * 60_000
	SpoolTTLMS     = 60 * 60_000
	OutcomeTTLMS   = 24 * 60 * 60_000
)

// Field / file bounds.
const (
	MaxText         = 240
	MaxField        = 4096
	MaxOutput       = 2048
	MaxModel        = 200
	MaxSession      = 200
	MaxCWD          = 1000
	MaxChildren     = 32
	MaxOutcomes     = 20
	MaxEventBytes   = 16 * 1024
	MaxSpoolBytes   = 1024 * 1024
	MaxSpoolLines   = 2048
	MaxBody         = 512 * 1024
	DedupeMS        = 500
	OutcomeDedupeMS = 15_000
	// StartThrottleS bounds ensure_server relaunches: at most one spawn
	// attempt per window.
	StartThrottleS = 10
)

// Now returns the current time in Unix milliseconds. Override it in tests
// to control TTL, pruning and staleness decisions without sleeping.
var Now = func() int64 { return time.Now().UnixMilli() }

// nowNanos backs the envelope "seq" field (Python: time.time_ns()).
var nowNanos = func() int64 { return time.Now().UnixNano() }

func nowMs() int64 { return Now() }

// envTruthy reports whether key holds a truthy switch value, mirroring
// `os.environ.get(key, "").lower() in {"1", "true", "yes"}`.
func envTruthy(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// Disabled reports whether hooks are globally disabled via SPACES_HOOKS_DISABLE.
func Disabled() bool { return envTruthy("SPACES_HOOKS_DISABLE") }

// noDaemon reports whether background server launches are suppressed
// (used by tests and by the server child itself).
func noDaemon() bool { return envTruthy("SPACES_HOOKS_NO_DAEMON") }

// RuntimeDir resolves the hooks runtime directory: SPACES_HOOKS_RUNTIME first,
// then ${XDG_RUNTIME_DIR}/zj, then /tmp/zj-<uid> — mirroring the Python and
// the systemd unit (which sets SPACES_HOOKS_RUNTIME=%t/zj).
func RuntimeDir() string {
	if configured := os.Getenv("SPACES_HOOKS_RUNTIME"); configured != "" {
		return os.ExpandEnv(configured)
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "spaces")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("spaces-%d", os.Getuid()))
}

// StorePath resolves the store file: SPACES_HOOKS_STORE wins, else the runtime dir.
func StorePath() string {
	if configured := os.Getenv("SPACES_HOOKS_STORE"); configured != "" {
		return os.ExpandEnv(configured)
	}
	return filepath.Join(RuntimeDir(), "hooks.store.json")
}

// EndpointPath is the listener endpoint file (socket path + token).
func EndpointPath() string { return filepath.Join(RuntimeDir(), "hooks.endpoint.json") }

// SocketPath is the listener unix socket.
func SocketPath() string { return filepath.Join(RuntimeDir(), "h.sock") }

// SpoolPath is the append-only spool of undrained hook events.
func SpoolPath() string { return filepath.Join(RuntimeDir(), "hooks-spool.jsonl") }

// LockPath is the server-ownership lock. Store writes use their own lock.
func LockPath() string { return filepath.Join(RuntimeDir(), "hooks.server.lock") }

// StoreLockPath serializes store writers.
func StoreLockPath() string { return filepath.Join(RuntimeDir(), "hooks.store.lock") }

// StartstampPath throttles ensure_server relaunches.
func StartstampPath() string { return filepath.Join(RuntimeDir(), "hooks.startstamp") }

// LogPath is the listener log file.
func LogPath() string { return filepath.Join(RuntimeDir(), "hooks.log") }

// EnsureDir creates the runtime dir with mode 0700 (best effort chmod, like
// the Python which swallows chmod failures).
func EnsureDir() (string, error) {
	dir := RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	return dir, nil
}

// atomicWrite writes data to path atomically (temp file in the same
// directory, fsync, chmod, rename), mirroring _atomic_write.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if serr != nil {
		_ = os.Remove(tmp)
		return serr
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// marshalJSON encodes v as compact JSON without HTML escaping, matching
// Python json.dumps(ensure_ascii=False, separators=(",", ":")) byte for
// byte on all inputs both encoders accept (raw UTF-8, no <>& escapes).
// encoding/json sorts map keys, matching sort_keys=True where used.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// spoolKeyOrder reproduces Python dict insertion order for hook envelopes
// ({"session": ..., "pane": ..., **normalized}), so spool and publish bytes
// match the Python, which dumps those without sort_keys. (The store file
// uses sort_keys=True and keeps the plain sorted marshalJSON.)
var spoolKeyOrder = []string{
	"session", "pane",
	"v", "agent", "app", "event", "raw_event", "state", "kind",
	"text", "title", "prompt", "tool", "tool_input", "tool_output", "reply",
	"model", "profile", "session_id", "cwd", "background", "background_tasks",
	"background_authoritative", "crons_authoritative", "waiting_on", "child",
	"ts_ms", "seq",
	"normalized", "payload",
}

// childKeyOrder reproduces _child_info insertion order.
var childKeyOrder = []string{"id", "type", "kind", "category", "state", "outcome", "text", "command", "schedule", "since_ms", "ts_ms", "ended_ms"}

// endpointKeyOrder reproduces _write_endpoint insertion order.
var endpointKeyOrder = []string{
	"version", "protocol", "pid", "uid", "socket", "tcp_port", "token", "started_ms",
}

// marshalOrderedMap encodes a map with preferred keys first (in order),
// remaining keys sorted — mirroring Python insertion order for the known
// envelope shapes. Unknown keys land in a sorted tail.
func marshalOrderedMap(m map[string]any, first []string) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	wrote := false
	writeKV := func(k string, v any) {
		if wrote {
			buf.WriteByte(',')
		}
		wrote = true
		kb, err := marshalJSON(k)
		if err != nil {
			kb = []byte(`""`)
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(valueOrderedJSON(v, k))
	}
	seen := map[string]bool{}
	for _, k := range first {
		if v, ok := m[k]; ok {
			seen[k] = true
			writeKV(k, v)
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		writeKV(k, m[k])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// valueOrderedJSON encodes a value, recursing into nested envelope/child
// maps with their insertion orders.
func valueOrderedJSON(v any, parentKey string) []byte {
	if m, ok := v.(map[string]any); ok {
		if m == nil {
			return []byte("null")
		}
		switch parentKey {
		case "child":
			return marshalOrderedMap(m, childKeyOrder)
		case "normalized", "payload":
			return marshalOrderedMap(m, spoolKeyOrder)
		default:
			return marshalOrderedMap(m, nil)
		}
	}
	b, err := marshalJSON(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// decodeJSON decodes data preserving number literals (json.Number),
// so store/spool round-trips keep integer values byte-identical.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// readJSONFile returns the decoded JSON document at path, or nil on any
// I/O or parse error (mirrors _read_json).
func readJSONFile(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := decodeJSON(data)
	if err != nil {
		return nil
	}
	return v
}

// toInt64 converts JSON-decoded numbers (json.Number, float64, ints) to
// int64, mirroring Python int(...) on record timestamps.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
		return 0
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	default:
		return 0
	}
}

// toFloat64 converts JSON-decoded numbers for TTL comparisons.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// stringKey converts a session/pane JSON value to its map key, mirroring
// Python str(...). Integer-valued floats render without a fraction.
func stringKey(v any) string {
	switch k := v.(type) {
	case nil:
		return ""
	case string:
		return k
	case json.Number:
		return k.String()
	case float64:
		if k == float64(int64(k)) {
			return fmt.Sprintf("%d", int64(k))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%v", k), "0"), ".")
	case bool:
		if k {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", k)
	}
}

// truncateRunes cuts s to at most n characters (Python slices by code
// point, not by byte).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// stripControls replaces C0 controls and DEL with spaces (the _CONTROL
// regex), leaving \t\n\r (0x09, 0x0a, 0x0d) for the whitespace squash.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x08 || r == 0x0b || r == 0x0c || (r >= 0x0e && r <= 0x1f) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// logMsg appends a timestamped line to the hooks log, mirroring _log.
func logMsg(message string) {
	if _, err := EnsureDir(); err != nil {
		return
	}
	path := LogPath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02T15:04:05"), message)
	_ = os.Chmod(path, 0o600)
}

package core

import (
	"os"
	"strings"
	"syscall"
)

// spoolLine serializes a spool entry to one JSON line, mirroring
// _spool_line: key order follows Python dict insertion order, and
// oversized entries (>24KiB) have their big text fields truncated before
// a final byte cut.
func spoolLine(entry map[string]any) []byte {
	payload := marshalOrderedMap(entry, spoolKeyOrder)
	data := append(append([]byte{}, payload...), '\n')
	if len(data) <= 24*1024 {
		return data
	}
	trimmed := make(map[string]any, len(entry))
	for k, v := range entry {
		trimmed[k] = v
	}
	for _, tc := range []struct {
		key      string
		limit    int
		keepTail bool
	}{
		{"prompt", 1024, false},
		{"tool_input", 1024, false},
		{"tool_output", 512, true},
		{"reply", 1024, true},
		{"text", MaxText, false},
	} {
		if s, ok := trimmed[tc.key].(string); ok {
			trimmed[tc.key] = oneLine(s, tc.limit, tc.keepTail)
		}
	}
	payload = marshalOrderedMap(trimmed, spoolKeyOrder)
	// Like Python `payload[:24*1024]`, the cut counts characters.
	if r := []rune(string(payload)); len(r) > 24*1024 {
		payload = []byte(string(r[:24*1024]))
	}
	return append(payload, '\n')
}

// AppendSpool appends an entry to the spool file under an flock, trimming
// the spool when it exceeds MaxSpoolBytes, mirroring append_spool. I/O
// failures are swallowed (best-effort telemetry), exactly like the Python;
// only directory setup errors propagate.
func AppendSpool(entry map[string]any) error {
	if Disabled() {
		return nil
	}
	if _, err := EnsureDir(); err != nil {
		return err
	}
	path := SpoolPath()
	fd, err := syscall.Open(path, syscall.O_APPEND|syscall.O_CREAT|syscall.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	func() {
		defer syscall.Close(fd)
		if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
			return
		}
		defer syscall.Flock(fd, syscall.LOCK_UN)
		_, _ = syscall.Write(fd, spoolLine(entry))
	}()
	if fi, err := os.Stat(path); err == nil && fi.Size() > MaxSpoolBytes {
		trimSpool(path)
	}
	return nil
}

// trimSpool keeps the last MaxSpoolLines lines, mirroring _trim_spool.
func trimSpool(path string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Split after newlines so kept lines stay newline-terminated.
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > MaxSpoolLines {
		lines = lines[len(lines)-MaxSpoolLines:]
	}
	joined := strings.Join(lines, "")
	if err := f.Truncate(0); err != nil {
		return
	}
	if _, err := f.Seek(0, 0); err != nil {
		return
	}
	_, _ = f.WriteString(joined)
	_ = f.Sync()
}

// ReadSpoolEvents returns up to limit fresh spool entries (newest last),
// mirroring read_spool_events: unparsable lines are skipped and entries
// older than SPOOL_TTL_MS are dropped.
func ReadSpoolEvents(limit int) []map[string]any {
	data, err := os.ReadFile(SpoolPath())
	if err != nil {
		return []map[string]any{}
	}
	// readlines() semantics: a trailing newline does not add an element.
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	cutoff := nowMs() - SpoolTTLMS
	events := []map[string]any{}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		v, err := decodeJSON([]byte(line))
		if err != nil {
			continue
		}
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if ts, ok := toFloat64(entry["ts_ms"]); ok && ts < float64(cutoff) {
			continue
		}
		events = append(events, entry)
	}
	return events
}

// DrainSpool replays spool entries into the store and truncates the spool,
// mirroring drain_spool. It returns the number of entries applied; entries
// for missing session/pane still count, exactly like the Python.
func DrainSpool() int {
	path := SpoolPath()
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return 0
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	storeLockHandle, err := storeLock()
	if err != nil {
		return 0
	}
	defer unlockFile(storeLockHandle)
	store := LoadStore()
	cutoff := nowMs() - SpoolTTLMS
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		v, err := decodeJSON([]byte(line))
		if err != nil {
			continue
		}
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if ts, ok := toFloat64(entry["ts_ms"]); ok && ts < float64(cutoff) {
			continue
		}
		ApplyEventToCopy(store, entry, nowMs())
		count++
	}
	_ = WriteStore(PruneStore(store, nowMs()))
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_ = f.Sync()
	return count
}

package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func spoolEntry(t *testing.T, session string, pane int, ts int64, extra map[string]any) map[string]any {
	t.Helper()
	e := map[string]any{
		"session": session, "pane": pane,
		"v": 1, "agent": "claude", "app": "claude-code",
		"event": "pre_tool", "state": "working", "kind": nil,
		"text": "Bash", "ts_ms": ts, "seq": ts,
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestSpoolKeyOrderMatchesPython(t *testing.T) {
	setupRuntime(t)
	// Python dumps spool lines in dict insertion order (no sort_keys);
	// entries are {session, pane, **envelope}.
	line := string(spoolLine(map[string]any{
		"pane": 7, "session": "dots", "state": "working",
		"v": 1, "text": "Bash", "child": nil, "ts_ms": int64(5),
	}))
	want := `{"session":"dots","pane":7,"v":1,"state":"working","text":"Bash","child":null,"ts_ms":5}` + "\n"
	if line != want {
		t.Errorf("got  %q\nwant %q", line, want)
	}
}

func TestAppendReadRoundTrip(t *testing.T) {
	setupRuntime(t)
	if err := AppendSpool(spoolEntry(t, "dots", 7, Now(), nil)); err != nil {
		t.Fatalf("AppendSpool: %v", err)
	}
	events := ReadSpoolEvents(MaxSpoolLines)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0]["session"] != "dots" {
		t.Errorf("session = %v", events[0]["session"])
	}
}

func TestSpoolTTLSkipped(t *testing.T) {
	setupRuntime(t)
	now := Now()
	_ = AppendSpool(spoolEntry(t, "fresh", 1, now, nil))
	_ = AppendSpool(spoolEntry(t, "stale", 2, now-SpoolTTLMS-1, nil))
	noTs := spoolEntry(t, "nots", 3, now, nil)
	delete(noTs, "ts_ms")
	_ = AppendSpool(noTs)
	events := ReadSpoolEvents(MaxSpoolLines)
	got := map[string]bool{}
	for _, e := range events {
		got[e["session"].(string)] = true
	}
	if !got["fresh"] || !got["nots"] || got["stale"] {
		t.Errorf("ttl filter wrong: %v", got)
	}
}

func TestSpoolTrimsToMaxLines(t *testing.T) {
	setupRuntime(t)
	// Build a >1MB spool directly so the next append triggers the trim.
	var sb strings.Builder
	line := fmt.Sprintf(`{"session":"s","pane":1,"event":"x","state":"working","ts_ms":%d,"pad":"%s"}`+"\n",
		Now(), strings.Repeat("p", 400))
	for sb.Len() <= MaxSpoolBytes {
		sb.WriteString(line)
	}
	if err := os.WriteFile(SpoolPath(), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(strings.Split(strings.TrimRight(sb.String(), "\n"), "\n"))
	if before <= MaxSpoolLines {
		t.Fatalf("fixture too small: %d lines", before)
	}
	if err := AppendSpool(spoolEntry(t, "tail", 9, Now(), nil)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SpoolPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != MaxSpoolLines {
		t.Errorf("lines = %d, want %d", len(lines), MaxSpoolLines)
	}
	if !strings.Contains(lines[len(lines)-1], `"tail"`) {
		t.Errorf("newest entry lost in trim")
	}
	if n := len(ReadSpoolEvents(MaxSpoolLines)); n != MaxSpoolLines {
		t.Errorf("read events = %d", n)
	}
}

func TestSpoolOversizedEntryTrimmed(t *testing.T) {
	setupRuntime(t)
	big := strings.Repeat("z", 100*1024)
	entry := spoolEntry(t, "big", 1, Now(), map[string]any{
		"prompt": big, "tool_input": big, "tool_output": big, "reply": big, "text": big,
	})
	line := spoolLine(entry)
	if len(line) > 24*1024+1 {
		t.Errorf("spool line = %d bytes, want <= %d", len(line), 24*1024+1)
	}
	if err := AppendSpool(entry); err != nil {
		t.Fatal(err)
	}
	events := ReadSpoolEvents(MaxSpoolLines)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
}

func TestDrainSpoolReplaysAndTruncates(t *testing.T) {
	setupRuntime(t)
	now := Now()
	_ = AppendSpool(spoolEntry(t, "probe", 7, now, nil))
	_ = AppendSpool(spoolEntry(t, "old", 8, now-SpoolTTLMS-1, nil))
	f, err := os.OpenFile(SpoolPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("not json\n")
	_ = f.Close()

	count := DrainSpool()
	if count != 1 {
		t.Errorf("count = %d, want 1 (stale + garbage skipped)", count)
	}
	rec := mustGet(t, LoadStore(), "panes", "probe", "7")
	if rec["state"] != "working" {
		t.Errorf("state = %v", rec["state"])
	}
	data, _ := os.ReadFile(SpoolPath())
	if len(data) != 0 {
		t.Errorf("spool not truncated: %q", data)
	}
}

func TestDrainSpoolMissingFile(t *testing.T) {
	setupRuntime(t)
	if count := DrainSpool(); count != 0 {
		t.Errorf("count = %d", count)
	}
}

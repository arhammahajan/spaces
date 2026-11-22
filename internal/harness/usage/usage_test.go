// Usage CLI tests: tolerant JSONL parsing, aggregation math across day
// boundaries, the humanizer, --days filtering, rendering and typed JSON.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func entry(ts float64, model string, status int, in, out, cached int64) Entry {
	return Entry{TS: ts, Model: model, Provider: "p", Ingress: "openai-chat",
		Upstream: "openai-chat", Status: status, InputTokens: in, OutputTokens: out,
		CachedTokens: cached, DurationMS: 5, Stream: false}
}

// Fixed clock: 2026-11-19 12:00:00 UTC.
var now = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func tsFor(y int, m time.Month, d, h, min int) float64 {
	return float64(time.Date(y, m, d, h, min, 0, 0, time.UTC).Unix())
}

func TestLoadTolerance(t *testing.T) {
	p := writeLog(t, strings.Join([]string{
		`{"ts":1.5,"model":"m","provider":"p","ingress":"openai-chat","upstream":"anthropic","status":200,"input_tokens":7,"output_tokens":3,"cached_tokens":2,"duration_ms":42,"stream":true}`,
		``,                // blank line skipped
		`{"ts": broken`,   // malformed line skipped
		`not json at all`, // malformed line skipped
		`{"ts":2.0,"model":"m2","provider":"p","ingress":"openai-chat","upstream":"openai-chat","status":500,"input_tokens":0,"output_tokens":0,"cached_tokens":0,"duration_ms":1,"stream":false}`,
		`{"ts":3.5,"model":"m3","provider":"p","ingress":"gemini","upstream":"gemini","status":200,"input_tokens":9,"output_tokens":8,"cached_tokens":0,"duration_ms":2,"stream":false}`, // truncated final line: no trailing newline
	}, "\n"))
	entries, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 parsed entries, got %d", len(entries))
	}
	if entries[0].Model != "m" || entries[0].Upstream != "anthropic" || !entries[0].Stream {
		t.Fatalf("entry 0 wrong: %+v", entries[0])
	}
	if entries[2].Model != "m3" || entries[2].InputTokens != 9 {
		t.Fatalf("truncated final line must parse: %+v", entries[2])
	}
}

func TestLoadEmptyAndMissing(t *testing.T) {
	entries, err := Load(writeLog(t, ""))
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty file must load cleanly: %v %d", err, len(entries))
	}
	entries, err = Load(writeLog(t, "\n\n"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("blank-only file must load cleanly: %v %d", err, len(entries))
	}
	_, err = Load(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err == nil {
		t.Fatal("missing file must be an error")
	}
}

func TestAggregateMathAcrossDayBoundaries(t *testing.T) {
	day1 := tsFor(2026, 11, 18, 23, 59) // yesterday 23:59 UTC
	day2 := tsFor(2026, 11, 19, 0, 1)   // today 00:01 UTC
	entries := []Entry{
		entry(day1, "m-a", 200, 10, 5, 2),
		entry(day1+30, "m-a", 429, 1, 0, 0),
		entry(day2, "m-b", 200, 7, 3, 0),
		entry(day2+30, "m-b", 500, 1, 1, 0),
		entry(tsFor(2026, 11, 19, 11, 0), "m-b", 200, 2, 2, 1),
	}
	s := Aggregate(entries, now, 0) // no window
	if s.Total.Requests != 5 || s.Total.Errors != 2 {
		t.Fatalf("total requests/errors wrong: %+v", s.Total)
	}
	if s.Total.InputTokens != 21 || s.Total.OutputTokens != 11 || s.Total.CachedTokens != 3 {
		t.Fatalf("total tokens wrong: %+v", s.Total)
	}
	if len(s.Days) != 2 || s.Days[0].Day != "2026-11-18" || s.Days[1].Day != "2026-11-19" {
		t.Fatalf("day grouping wrong: %+v", s.Days)
	}
	// 23:59 and 00:01 land on different days.
	if s.Days[0].Requests != 2 || s.Days[0].Errors != 1 {
		t.Fatalf("day 1 rollup wrong: %+v", s.Days[0])
	}
	if s.Days[1].Requests != 3 || s.Days[1].Errors != 1 || s.Days[1].InputTokens != 10 {
		t.Fatalf("day 2 rollup wrong: %+v", s.Days[1])
	}
	if len(s.Models) != 2 || s.Models[0].Model != "m-a" || s.Models[1].Model != "m-b" {
		t.Fatalf("model grouping wrong: %+v", s.Models)
	}
	if s.Models[0].Requests != 2 || s.Models[1].InputTokens != 10 || s.Models[1].CachedTokens != 1 {
		t.Fatalf("model rollups wrong: %+v", s.Models)
	}
}

func TestAggregateDaysWindow(t *testing.T) {
	entries := []Entry{
		entry(tsFor(2026, 11, 17, 6, 0), "m", 200, 100, 10, 0), // 2 days ago
		entry(tsFor(2026, 11, 18, 6, 0), "m", 200, 20, 2, 0),   // yesterday
		entry(tsFor(2026, 11, 19, 6, 0), "m", 200, 3, 1, 0),    // today
	}
	// days=2 keeps yesterday and today only.
	s := Aggregate(entries, now, 2)
	if s.Total.Requests != 2 || s.Total.InputTokens != 23 {
		t.Fatalf("day window wrong: %+v", s.Total)
	}
	if len(s.Days) != 2 || s.Days[0].Day != "2026-11-18" {
		t.Fatalf("window days wrong: %+v", s.Days)
	}
	// days=1 keeps today only.
	s = Aggregate(entries, now, 1)
	if s.Total.Requests != 1 || s.Total.InputTokens != 3 {
		t.Fatalf("days=1 wrong: %+v", s.Total)
	}
	// days=3 reaches two days ago as well.
	s = Aggregate(entries, now, 3)
	if s.Total.Requests != 3 {
		t.Fatalf("days=3 wrong: %+v", s.Total)
	}
}

func TestAggregateEmpty(t *testing.T) {
	s := Aggregate(nil, now, 7)
	if s.Total.Requests != 0 || len(s.Days) != 0 || len(s.Models) != 0 {
		t.Fatalf("empty aggregate wrong: %+v", s)
	}
	table := RenderTable(s, "day")
	if !strings.Contains(table, "TOTAL") || !strings.Contains(table, "DAY") {
		t.Fatalf("empty table must still render: %q", table)
	}
}

func TestHumanize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"}, {1, "1"}, {999, "999"},
		{1000, "1K"}, {1234, "1.2K"}, {999999, "1000K"},
		{1200000, "1.2M"}, {2000000, "2M"}, {340000, "340K"},
		{2500000000, "2.5B"}, {-5, "0"},
	}
	for _, tc := range cases {
		if got := Humanize(tc.n); got != tc.want {
			t.Fatalf("Humanize(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestRenderTableHumanized(t *testing.T) {
	entries := []Entry{
		entry(tsFor(2026, 11, 19, 6, 0), "gpt-4o", 200, 1200000, 340000, 12000),
		entry(tsFor(2026, 11, 19, 7, 0), "gpt-4o", 500, 0, 0, 0),
		entry(tsFor(2026, 11, 18, 6, 0), "claude", 200, 999, 5, 0),
	}
	s := Aggregate(entries, now, 0)
	byDay := RenderTable(s, "day")
	for _, want := range []string{
		"DAY", "2026-11-18", "2026-11-19", "TOTAL", "1.2M", "340K", "12K",
	} {
		if !strings.Contains(byDay, want) {
			t.Fatalf("day table missing %q:\n%s", want, byDay)
		}
	}
	if !strings.Contains(byDay, "REQS") || !strings.Contains(byDay, "ERRS") {
		t.Fatalf("table must carry count columns:\n%s", byDay)
	}
	byModel := RenderTable(s, "model")
	for _, want := range []string{"MODEL", "claude", "gpt-4o", "999", "1.2M"} {
		if !strings.Contains(byModel, want) {
			t.Fatalf("model table missing %q:\n%s", want, byModel)
		}
	}
	// Column alignment: every line has the same visible width.
	widths := map[int]bool{}
	for _, line := range strings.Split(strings.TrimRight(byDay, "\n"), "\n") {
		widths[len(line)] = true
	}
	if len(widths) != 1 {
		t.Fatalf("table columns must align:\n%s", byDay)
	}
}

func TestRenderJSONTypedShape(t *testing.T) {
	entries := []Entry{
		entry(tsFor(2026, 11, 19, 6, 0), "m", 200, 7, 3, 2),
		entry(tsFor(2026, 11, 19, 7, 0), "m", 404, 0, 0, 0),
	}
	s := Aggregate(entries, now, 0)
	out := RenderJSON(s)
	var back struct {
		Days []struct {
			Day string `json:"day"`
			Rollup
		} `json:"days"`
		Models []struct {
			Model string `json:"model"`
			Rollup
		} `json:"models"`
		Total Rollup `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("json output must be typed and parseable: %v\n%s", err, out)
	}
	if len(back.Days) != 1 || back.Days[0].Day != "2026-11-19" {
		t.Fatalf("json days wrong: %s", out)
	}
	if back.Days[0].Requests != 2 || back.Days[0].Errors != 1 || back.Days[0].CachedTokens != 2 {
		t.Fatalf("json day rollup wrong: %s", out)
	}
	if back.Total.Requests != 2 || back.Models[0].Model != "m" {
		t.Fatalf("json totals wrong: %s", out)
	}
}

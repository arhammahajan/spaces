// Package usage aggregates the router's accounting JSONL (the opt-in
// [accounting] records) into per-day and per-model rollups for
// `harness usage`.
//
// Load is tolerant: blank and malformed lines are skipped and a
// truncated final line parses like any other. Aggregation counts
// requests, errors (status >= 400) and input/output/cached token sums,
// grouped by UTC day and by model. Rendering covers the human table
// (1.2M-style token humanization) and the typed --json output.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one accounting record as written by the router's
// [accounting] JSONL. Unknown members are ignored, so new record fields
// stay loadable.
type Entry struct {
	TS           float64 `json:"ts"`
	Model        string  `json:"model"`
	Provider     string  `json:"provider"`
	Ingress      string  `json:"ingress"`
	Upstream     string  `json:"upstream"`
	Status       int     `json:"status"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	DurationMS   int64   `json:"duration_ms"`
	Stream       bool    `json:"stream"`
}

// Rollup is the counted aggregate over a set of entries.
type Rollup struct {
	Requests     int64 `json:"requests"`
	Errors       int64 `json:"errors"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
}

func (r *Rollup) add(e Entry) {
	r.Requests++
	if e.Status >= 400 {
		r.Errors++
	}
	r.InputTokens += e.InputTokens
	r.OutputTokens += e.OutputTokens
	r.CachedTokens += e.CachedTokens
}

// DayRollup aggregates one UTC calendar day (YYYY-MM-DD).
type DayRollup struct {
	Day string `json:"day"`
	Rollup
}

// ModelRollup aggregates one public model name.
type ModelRollup struct {
	Model string `json:"model"`
	Rollup
}

// Summary carries both groupings plus the window total.
type Summary struct {
	Days   []DayRollup   `json:"days"`
	Models []ModelRollup `json:"models"`
	Total  Rollup        `json:"total"`
}

// Load parses an accounting JSONL file. Blank and malformed lines are
// skipped, and a final line without its trailing newline parses like any
// other. A missing or unreadable file is an error; an empty file is not.
func Load(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read usage log: %w", err)
	}
	var out []Entry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Aggregate rolls entries up per UTC day and per model. days > 0 keeps
// only entries whose UTC day falls inside the last `days` days counting
// today as day 1; days <= 0 keeps everything. Days render ascending;
// models sort by name.
func Aggregate(entries []Entry, now time.Time, days int) Summary {
	var s Summary
	dayIdx := map[string]int{}
	modelIdx := map[string]int{}
	cutoff := ""
	if days > 0 {
		cutoff = now.UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	}
	for _, e := range entries {
		day := dayOf(e.TS)
		if cutoff != "" && day < cutoff {
			continue
		}
		s.Total.add(e)
		if i, ok := dayIdx[day]; ok {
			s.Days[i].add(e)
		} else {
			dayIdx[day] = len(s.Days)
			d := DayRollup{Day: day}
			d.add(e)
			s.Days = append(s.Days, d)
		}
		if i, ok := modelIdx[e.Model]; ok {
			s.Models[i].add(e)
		} else {
			modelIdx[e.Model] = len(s.Models)
			m := ModelRollup{Model: e.Model}
			m.add(e)
			s.Models = append(s.Models, m)
		}
	}
	sort.Slice(s.Days, func(i, j int) bool { return s.Days[i].Day < s.Days[j].Day })
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].Model < s.Models[j].Model })
	return s
}

func dayOf(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("2006-01-02")
}

// RenderTable renders the summary as a human table. by selects the
// grouping: "day" (the default for anything else) or "model". Token
// columns use 1.2M-style humanization and a TOTAL row closes the table.
func RenderTable(s Summary, by string) string {
	var b strings.Builder
	head := func(first string) {
		b.WriteString(first + "\tREQS\tERRS\tINPUT\tOUTPUT\tCACHED\n")
	}
	row := func(first string, r Rollup) {
		b.WriteString(first + "\t" +
			strconv.FormatInt(r.Requests, 10) + "\t" +
			strconv.FormatInt(r.Errors, 10) + "\t" +
			Humanize(r.InputTokens) + "\t" +
			Humanize(r.OutputTokens) + "\t" +
			Humanize(r.CachedTokens) + "\n")
	}
	if by == "model" {
		head("MODEL")
		for _, m := range s.Models {
			row(m.Model, m.Rollup)
		}
	} else {
		head("DAY")
		for _, d := range s.Days {
			row(d.Day, d.Rollup)
		}
	}
	row("TOTAL", s.Total)
	return alignColumns(b.String())
}

// alignColumns pads each tab-separated column to its widest cell with
// two spaces between columns; the label column stays left-aligned and
// the numeric columns right-align by padding on the left.
func alignColumns(table string) string {
	lines := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
	widths := make([]int, 6)
	for _, line := range lines {
		for i, cell := range strings.Split(line, "\t") {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	for _, line := range lines {
		cells := strings.Split(line, "\t")
		for i, cell := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			pad := widths[i] - len(cell)
			if i == 0 {
				b.WriteString(cell + strings.Repeat(" ", pad))
			} else {
				b.WriteString(strings.Repeat(" ", pad) + cell)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RenderJSON renders the typed --json output with two-space indentation.
// A Summary always marshals, so the fallback is unreachable.
func RenderJSON(s Summary) string {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Humanize renders a token count in 1.2M style: below a thousand the
// exact number, then one decimal K/M/B with a trailing ".0" trimmed.
func Humanize(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1000*1000:
		return scaled(n, 1000, "K")
	case n < 1000*1000*1000:
		return scaled(n, 1000*1000, "M")
	default:
		return scaled(n, 1000*1000*1000, "B")
	}
}

func scaled(n int64, unit int64, suffix string) string {
	v := float64(n) / float64(unit)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + suffix
}

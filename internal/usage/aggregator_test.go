package usage

import (
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestAggregateDedupeFilterBucketsAndStreaks(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 4, 15, 14, 30, 0, 0, loc)

	ts := func(day, hour, min int) int64 {
		return time.Date(2026, 4, day, hour, min, 0, 0, loc).UnixMilli()
	}

	reportedCost := 5.0
	records := []agent.UsageRecord{
		// Valid record on April 13 (part of 3-day streak: 13, 14, 15)
		{
			Provider:    "claude",
			TimestampMs: ts(13, 10, 0),
			Model:       "claude-sonnet-4-6",
			SessionID:   "s1",
			Project:     "spaces",
			ProjectPath: "/dev/spaces",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 100_000,
				CachedInputTokens:   300_000,
				OutputTokens:        50_000,
				ReasoningTokens:     10_000,
			},
			DedupeKey: "dup-1",
			Tools:     []string{"bash", "read_file", "bash"},
		},
		// Duplicate of dup-1 (must be ignored)
		{
			Provider:    "claude",
			TimestampMs: ts(13, 10, 1),
			Model:       "claude-sonnet-4-6",
			SessionID:   "s1",
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 999_999},
			DedupeKey:   "dup-1",
		},
		// Invalid: zero timestamp (filtered)
		{
			Provider:    "claude",
			TimestampMs: 0,
			Model:       "claude-sonnet-4-6",
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 1000},
		},
		// Invalid: no token data and no reported cost (filtered)
		{
			Provider:    "claude",
			TimestampMs: ts(14, 10, 0),
			Model:       "claude-sonnet-4-6",
		},
		// Valid record on April 14 (within last 24h at 16:15 -> hourly slot)
		{
			Provider:    "codex",
			TimestampMs: ts(14, 16, 15),
			Model:       "gpt-5.4",
			SessionID:   "s2",
			ProjectPath: "/dev/other-repo",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 200_000,
				OutputTokens:        100_000,
			},
			Tools: []string{"read_file"},
		},
		// Valid record on April 15 (same session s1 updated later, plus reported cost)
		{
			Provider:        "claude",
			TimestampMs:     ts(15, 14, 10),
			Model:           "claude-opus-4-6",
			SessionID:       "s1",
			Project:         "spaces",
			ProjectPath:     "/dev/spaces",
			Totals:          agent.UsageTokenTotals{UncachedInputTokens: 50_000, OutputTokens: 20_000},
			ReportedCostUSD: &reportedCost,
			Tools:           []string{"bash"},
		},
		// Older isolated day (April 10) to test streak break
		{
			Provider:    "gemini",
			TimestampMs: ts(10, 9, 0),
			Model:       "unknown-model",
			SessionID:   "s3",
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 10_000},
		},
	}

	rep := Aggregate(records, nil, now, loc)

	if rep.Records != 4 {
		t.Fatalf("Records = %d, want 4", rep.Records)
	}
	if rep.UnpricedRecords != 1 {
		t.Errorf("UnpricedRecords = %d, want 1", rep.UnpricedRecords)
	}
	if rep.ActiveDays != 4 {
		t.Errorf("ActiveDays = %d, want 4", rep.ActiveDays)
	}
	if rep.CurrentStreak != 3 {
		t.Errorf("CurrentStreak = %d, want 3 (April 13, 14, 15)", rep.CurrentStreak)
	}
	if rep.LongestStreak != 3 {
		t.Errorf("LongestStreak = %d, want 3", rep.LongestStreak)
	}
	if rep.PeakDayDate != "2026-04-13" {
		t.Errorf("PeakDayDate = %q, want 2026-04-13", rep.PeakDayDate)
	}
	if rep.Sessions != 3 {
		t.Errorf("Sessions = %d, want 3", rep.Sessions)
	}

	// Hourly should always have 24 slots ending at 14:00.
	if len(rep.Hourly) != 24 {
		t.Fatalf("len(Hourly) = %d, want 24", len(rep.Hourly))
	}
	if rep.Hourly[23].Label != "14:00" || rep.Hourly[23].Records != 1 {
		t.Errorf("Hourly[23] = %+v, want label 14:00 with 1 record", rep.Hourly[23])
	}
	if rep.Hourly[1].Label != "16:00" || rep.Hourly[1].Records != 1 {
		t.Errorf("Hourly[1] = %+v, want label 16:00 with 1 record", rep.Hourly[1])
	}

	// SessionsList sorted descending by UpdatedAtMs, s1 should be first with 2 deduplicated models.
	if len(rep.SessionsList) != 3 {
		t.Fatalf("len(SessionsList) = %d, want 3", len(rep.SessionsList))
	}
	if rep.SessionsList[0].SessionID != "s1" {
		t.Errorf("newest session = %q, want s1", rep.SessionsList[0].SessionID)
	}
	if len(rep.SessionsList[0].Models) != 2 {
		t.Errorf("s1 models = %v, want 2 deduplicated models", rep.SessionsList[0].Models)
	}
	if rep.SessionsList[0].DurationMs != ts(15, 14, 10)-ts(13, 10, 0) {
		t.Errorf("s1 DurationMs = %d, want %d", rep.SessionsList[0].DurationMs, ts(15, 14, 10)-ts(13, 10, 0))
	}

	// Tools sorted descending by count.
	if len(rep.Tools) != 2 || rep.Tools[0].Name != "bash" || rep.Tools[0].Count != 3 {
		t.Errorf("unexpected Tools: %+v", rep.Tools)
	}

	// Projects derived from ProjectPath when Project omitted.
	if len(rep.Projects) != 2 {
		t.Fatalf("len(Projects) = %d, want 2", len(rep.Projects))
	}
	foundOther := false
	for _, p := range rep.Projects {
		if p.Project == "other-repo" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Errorf("expected derived project name other-repo in %+v", rep.Projects)
	}
}

func TestAggregateStreakEndingYesterday(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 4, 15, 9, 0, 0, 0, loc)
	recs := []agent.UsageRecord{
		{Provider: "claude", TimestampMs: time.Date(2026, 4, 13, 12, 0, 0, 0, loc).UnixMilli(), Model: "claude-sonnet-4-6", Totals: agent.UsageTokenTotals{UncachedInputTokens: 100}},
		{Provider: "claude", TimestampMs: time.Date(2026, 4, 14, 12, 0, 0, 0, loc).UnixMilli(), Model: "claude-sonnet-4-6", Totals: agent.UsageTokenTotals{UncachedInputTokens: 100}},
	}
	rep := Aggregate(recs, nil, now, loc)
	if rep.CurrentStreak != 2 || rep.LongestStreak != 2 {
		t.Errorf("got current=%d longest=%d, want 2/2", rep.CurrentStreak, rep.LongestStreak)
	}
}

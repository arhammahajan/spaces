package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestDetectActiveBlocks(t *testing.T) {
	now := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)

	t.Setenv("SPACES_CLAUDE_5H_TOKEN_LIMIT", "1000000")

	records := []agent.UsageRecord{
		// Old block (>5h ago) rolled over into current block
		{
			Provider:    "claude",
			TimestampMs: now.Add(-8 * time.Hour).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 200_000},
		},
		// Active block started 2h ago (floored to 12:00 UTC)
		{
			Provider:    "claude",
			TimestampMs: time.Date(2026, 4, 15, 12, 15, 0, 0, time.UTC).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 300_000, OutputTokens: 200_000},
		},
		{
			Provider:    "claude",
			TimestampMs: time.Date(2026, 4, 15, 14, 10, 0, 0, time.UTC).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 100_000},
		},
		// Expired codex block (started 7h ago, no recent activity)
		{
			Provider:    "codex",
			TimestampMs: now.Add(-7 * time.Hour).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 500_000},
		},
	}

	blocks := DetectActiveBlocks(records, now)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1 (got %+v)", len(blocks), blocks)
	}
	b := blocks[0]
	if b.Provider != "claude" || !b.Active {
		t.Fatalf("unexpected block: %+v", b)
	}
	wantStart := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	if b.StartMs != wantStart || b.EndMs != wantStart+fiveHourMs {
		t.Errorf("block window = [%d, %d], want [%d, %d]", b.StartMs, b.EndMs, wantStart, wantStart+fiveHourMs)
	}
	if b.TotalTokens != 600_000 {
		t.Errorf("TotalTokens = %d, want 600000", b.TotalTokens)
	}
	if b.LimitTokens != 1_000_000 {
		t.Errorf("LimitTokens = %d, want 1000000", b.LimitTokens)
	}
	if !almostEqual(b.UsedPct, 60.0, 1e-6) {
		t.Errorf("UsedPct = %f, want 60.0", b.UsedPct)
	}
}

func TestBuildFiveHourHistory(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 4, 15, 14, 30, 0, 0, loc)

	records := []agent.UsageRecord{
		{
			Provider:    "claude",
			Model:       "claude-sonnet-4-6",
			TimestampMs: now.Add(-10 * time.Minute).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 10_000},
		},
		{
			Provider:    "codex",
			Model:       "gpt-5.4",
			TimestampMs: now.Add(-6 * time.Hour).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 20_000},
		},
		// Older than 7 days -> excluded
		{
			Provider:    "claude",
			Model:       "claude-sonnet-4-6",
			TimestampMs: now.Add(-8 * 24 * time.Hour).UnixMilli(),
			Totals:      agent.UsageTokenTotals{UncachedInputTokens: 50_000},
		},
	}

	hist := BuildFiveHourHistory(records, nil, now, loc)
	if len(hist) != 2 {
		t.Fatalf("len(hist) = %d, want 2", len(hist))
	}
	if !hist[0].Current {
		t.Errorf("expected newest bucket to be Current=true, got %+v", hist[0])
	}
	if hist[1].Current {
		t.Errorf("expected older bucket to be Current=false")
	}
	if hist[0].StartMs <= hist[1].StartMs {
		t.Errorf("expected newest-first ordering")
	}
	if hist[0].ByProvider["claude"].Tokens != 10_000 {
		t.Errorf("unexpected ByProvider in current bucket: %+v", hist[0].ByProvider)
	}
}

func TestLoadPlanConfig(t *testing.T) {
	home := t.TempDir()

	// Missing file returns default.
	def := LoadPlanConfig(home)
	if def.Name != "Monthly" || def.MonthlyUSD != 0 || def.ResetDay != 1 {
		t.Fatalf("unexpected default config: %+v", def)
	}

	// Custom TOML with out-of-range reset_day gets clamped to 28.
	cfgPath := filepath.Join(home, ".config", "tmux-spaces", "usage.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `
name = "Claude Max"
monthly_usd = 200.0
provider = "claude"
reset_day = 31
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadPlanConfig(home)
	if cfg.Name != "Claude Max" || cfg.MonthlyUSD != 200.0 || cfg.Provider != "claude" || cfg.ResetDay != 28 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
}

func TestComputePlanProjection(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, loc)

	// Trailing 7 days: April 9..15 with one huge spike ($100) on April 15.
	// Spends: [10, 10, 10, 10, 10, 10, 100] -> median = 10, total = 160.
	daily := []DailyBucket{
		{Date: "2026-04-01", CostUSD: 20, Totals: agent.UsageTokenTotals{UncachedInputTokens: 100_000}}, // prior history before 7d cutoff
		{Date: "2026-04-09", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-10", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-11", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-12", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-13", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-14", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 50_000}},
		{Date: "2026-04-15", CostUSD: 100, Totals: agent.UsageTokenTotals{UncachedInputTokens: 500_000}},
	}

	plan := PlanConfig{
		Name:       "Pro",
		MonthlyUSD: 200.0,
		ResetDay:   1,
	}

	proj := ComputePlanProjection(daily, plan, now, loc)
	if proj.PeriodStart != "2026-04-01" || proj.PeriodEnd != "2026-04-30" {
		t.Errorf("period = [%s, %s], want [2026-04-01, 2026-04-30]", proj.PeriodStart, proj.PeriodEnd)
	}
	if !almostEqual(proj.SpentUSD, 180.0, 1e-9) {
		t.Errorf("SpentUSD = %f, want 180.0", proj.SpentUSD)
	}
	if !almostEqual(proj.PercentUsed, 0.9, 1e-9) {
		t.Errorf("PercentUsed = %f, want 0.9", proj.PercentUsed)
	}
	if proj.Status != PlanStatusNear {
		t.Errorf("Status = %s, want near", proj.Status)
	}
	if !almostEqual(proj.MedianDailyUSD, 10.0, 1e-9) {
		t.Errorf("MedianDailyUSD = %f, want 10.0", proj.MedianDailyUSD)
	}
	if !almostEqual(proj.AvgDailyUSD, 160.0/7.0, 1e-9) {
		t.Errorf("AvgDailyUSD = %f, want %f", proj.AvgDailyUSD, 160.0/7.0)
	}
	if proj.RemainingDays != 15 {
		t.Errorf("RemainingDays = %d, want 15", proj.RemainingDays)
	}
	if !almostEqual(proj.ProjectedPeriodUSD, 180.0+10.0*15.0, 1e-9) {
		t.Errorf("ProjectedPeriodUSD = %f, want 330.0", proj.ProjectedPeriodUSD)
	}
	if !proj.CompleteCost {
		t.Errorf("expected CompleteCost=true")
	}

	// Short history without prior days before 7d cutoff uses clamped span_days.
	shortDaily := []DailyBucket{
		{Date: "2026-04-14", CostUSD: 10, Totals: agent.UsageTokenTotals{UncachedInputTokens: 1000}, UnpricedRecords: 1},
		{Date: "2026-04-15", CostUSD: 20, Totals: agent.UsageTokenTotals{UncachedInputTokens: 2000}},
	}
	shortProj := ComputePlanProjection(shortDaily, PlanConfig{MonthlyUSD: 0, ResetDay: 1}, now, loc)
	if shortProj.Status != PlanStatusNone {
		t.Errorf("expected PlanStatusNone when MonthlyUSD=0, got %s", shortProj.Status)
	}
	if !almostEqual(shortProj.AvgDailyUSD, 15.0, 1e-9) {
		t.Errorf("expected AvgDailyUSD = 30/2 = 15.0, got %f", shortProj.AvgDailyUSD)
	}
	if shortProj.CompleteCost {
		t.Errorf("expected CompleteCost=false when an unpriced record exists in trailing 7d")
	}
}

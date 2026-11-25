package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	fiveHourMs             = int64(5 * time.Hour / time.Millisecond)
	tenHourMs              = int64(10 * time.Hour / time.Millisecond)
	oneHourMs              = int64(time.Hour / time.Millisecond)
	default5hTokenLimit    = int64(2_500_000)
	maxFiveHourBuckets     = 24
	fiveHourHistoryLookDay = 7
)

// DetectActiveBlocks groups records per provider from the last 10 hours into
// 5-hour rolling blocks starting at floor_to_hour(first.Timestamp), rolling over
// when ts - block.Start > 5h or ts - block.Last >= 5h. A block is active iff
// now > block.Start && now <= block.Start+5h && now - block.Last < 5h.
func DetectActiveBlocks(records []agent.UsageRecord, now time.Time) []ActiveBlock {
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	cutoffMs := nowMs - tenHourMs

	seenDedupe := make(map[string]struct{}, len(records))
	byProvider := make(map[string][]agent.UsageRecord)
	for _, rec := range records {
		if rec.DedupeKey != "" {
			if _, dup := seenDedupe[rec.DedupeKey]; dup {
				continue
			}
			seenDedupe[rec.DedupeKey] = struct{}{}
		}
		if rec.TimestampMs <= 0 || rec.TimestampMs < cutoffMs {
			continue
		}
		if rec.Totals.Total() <= 0 {
			continue
		}
		prov := strings.TrimSpace(rec.Provider)
		if prov == "" {
			prov = "unknown"
		}
		byProvider[prov] = append(byProvider[prov], rec)
	}

	if len(byProvider) == 0 {
		return nil
	}

	var active []ActiveBlock
	for prov, recs := range byProvider {
		sort.Slice(recs, func(i, j int) bool {
			return recs[i].TimestampMs < recs[j].TimestampMs
		})

		var cur *ActiveBlock
		for _, rec := range recs {
			ts := rec.TimestampMs
			if cur == nil || (ts-cur.StartMs) > fiveHourMs || (ts-cur.LastEntryMs) >= fiveHourMs {
				startMs := floorToHourMs(ts)
				cur = &ActiveBlock{
					Provider:    prov,
					StartMs:     startMs,
					EndMs:       startMs + fiveHourMs,
					LastEntryMs: ts,
					ResetsAt:    (startMs + fiveHourMs) / 1000,
				}
			}
			cur.TotalTokens += rec.Totals.Total()
			if ts > cur.LastEntryMs {
				cur.LastEntryMs = ts
			}
		}

		if cur == nil {
			continue
		}
		if nowMs > cur.StartMs && nowMs <= cur.EndMs && (nowMs-cur.LastEntryMs) < fiveHourMs {
			limit := fiveHourTokenLimit(prov)
			cur.LimitTokens = limit
			if limit > 0 {
				pct := (float64(cur.TotalTokens) / float64(limit)) * 100.0
				if pct < 0 {
					pct = 0
				}
				if pct > 100 {
					pct = 100
				}
				cur.UsedPct = pct
			}
			cur.Active = true
			active = append(active, *cur)
		}
	}

	sort.Slice(active, func(i, j int) bool {
		return active[i].Provider < active[j].Provider
	})
	return active
}

func floorToHourMs(tsMs int64) int64 {
	if tsMs >= 0 {
		return tsMs - (tsMs % oneHourMs)
	}
	return time.UnixMilli(tsMs).UTC().Truncate(time.Hour).UnixMilli()
}

func fiveHourTokenLimit(provider string) int64 {
	if strings.EqualFold(strings.TrimSpace(provider), "claude") {
		if raw := strings.TrimSpace(os.Getenv("SPACES_CLAUDE_5H_TOKEN_LIMIT")); raw != "" {
			if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return default5hTokenLimit
}

// BuildFiveHourHistory groups records from the last 7 days into 5-hour blocks
// (newest first, up to 24 buckets), marking the active current block and
// populating ByProvider and ByModel.
func BuildFiveHourHistory(records []agent.UsageRecord, table *RateTable, now time.Time, loc *time.Location) []FiveHourBucket {
	if loc == nil {
		loc = time.Local
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	cutoffMs := now.Add(-fiveHourHistoryLookDay * 24 * time.Hour).UnixMilli()
	currentSlot := nowMs / fiveHourMs

	seenDedupe := make(map[string]struct{}, len(records))
	buckets := make(map[int64]*FiveHourBucket)

	for _, rec := range records {
		if rec.DedupeKey != "" {
			if _, dup := seenDedupe[rec.DedupeKey]; dup {
				continue
			}
			seenDedupe[rec.DedupeKey] = struct{}{}
		}
		if rec.TimestampMs <= 0 || rec.TimestampMs < cutoffMs {
			continue
		}
		if !rec.Totals.HasData() && rec.ReportedCostUSD == nil {
			continue
		}

		slot := rec.TimestampMs / fiveHourMs
		b, ok := buckets[slot]
		if !ok {
			startMs := slot * fiveHourMs
			endMs := startMs + fiveHourMs
			startLocal := time.UnixMilli(startMs).In(loc)
			endLocal := time.UnixMilli(endMs).In(loc)
			b = &FiveHourBucket{
				StartMs:    startMs,
				EndMs:      endMs,
				Label:      fmt.Sprintf("%s %s-%s", startLocal.Format("Jan 02"), startLocal.Format("15:04"), endLocal.Format("15:04")),
				Current:    slot == currentSlot,
				ByProvider: make(map[string]SliceMetric),
				ByModel:    make(map[string]SliceMetric),
			}
			buckets[slot] = b
		}

		priced := PriceRecord(rec, table)
		recTokens := rec.Totals.Total()

		prov := strings.TrimSpace(rec.Provider)
		if prov == "" {
			prov = "unknown"
		}
		model := strings.TrimSpace(rec.Model)
		if model == "" {
			model = "unknown"
		}

		b.Totals.Add(rec.Totals)
		b.CostUSD += priced.CostUSD
		b.Records++
		addSliceMetric(b.ByProvider, prov, recTokens, priced.CostUSD)
		addSliceMetric(b.ByModel, model, recTokens, priced.CostUSD)
	}

	if len(buckets) == 0 {
		return nil
	}

	out := make([]FiveHourBucket, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartMs > out[j].StartMs
	})
	if len(out) > maxFiveHourBuckets {
		out = out[:maxFiveHourBuckets]
	}
	return out
}

// LoadPlanConfig reads optional <homeDir>/.config/tmux-spaces/usage.toml.
// Returns default PlanConfig{Name: "Monthly", MonthlyUSD: 0, ResetDay: 1} when
// absent or unreadable.
func LoadPlanConfig(homeDir string) PlanConfig {
	def := PlanConfig{
		Name:       "Monthly",
		MonthlyUSD: 0,
		ResetDay:   1,
	}
	base := agent.RealHomeDir(homeDir)
	if base == "" {
		return def
	}
	path := filepath.Join(base, ".config", "tmux-spaces", "usage.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}

	var cfg PlanConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return def
	}
	return normalizePlanConfig(cfg)
}

func normalizePlanConfig(cfg PlanConfig) PlanConfig {
	cfg.Name = strings.TrimSpace(cfg.Name)
	if cfg.Name == "" {
		cfg.Name = "Monthly"
	}
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	if math.IsNaN(cfg.MonthlyUSD) || math.IsInf(cfg.MonthlyUSD, 0) || cfg.MonthlyUSD < 0 {
		cfg.MonthlyUSD = 0
	}
	if cfg.ResetDay < 1 {
		cfg.ResetDay = 1
	} else if cfg.ResetDay > 28 {
		cfg.ResetDay = 28
	}
	return cfg
}

// ComputePlanProjection calculates billing-period spend, trailing-7d median and
// average run rates, period projection, 30-day projections, and budget status.
func ComputePlanProjection(daily []DailyBucket, plan PlanConfig, now time.Time, loc *time.Location) PlanProjection {
	if loc == nil {
		loc = time.Local
	}
	if now.IsZero() {
		now = time.Now()
	}
	plan = normalizePlanConfig(plan)

	nowLocal := now.In(loc)
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.UTC)

	// Compute billing period [periodStart, periodEnd] from plan.ResetDay.
	var periodStart, nextStart time.Time
	if today.Day() >= plan.ResetDay {
		periodStart = time.Date(today.Year(), today.Month(), plan.ResetDay, 0, 0, 0, 0, time.UTC)
		nextStart = periodStart.AddDate(0, 1, 0)
	} else {
		nextStart = time.Date(today.Year(), today.Month(), plan.ResetDay, 0, 0, 0, 0, time.UTC)
		periodStart = nextStart.AddDate(0, -1, 0)
	}
	periodEnd := nextStart.AddDate(0, 0, -1)

	periodStartStr := periodStart.Format("2006-01-02")
	periodEndStr := periodEnd.Format("2006-01-02")
	todayStr := today.Format("2006-01-02")

	// Index daily buckets by date string.
	byDate := make(map[string]DailyBucket, len(daily))
	var earliestDate string
	for _, b := range daily {
		byDate[b.Date] = b
		if b.Date <= todayStr && (earliestDate == "" || b.Date < earliestDate) {
			earliestDate = b.Date
		}
	}

	filterProvider := strings.ToLower(strings.TrimSpace(plan.Provider))
	allProviders := filterProvider == "" || filterProvider == "all"

	bucketSpendTokens := func(b DailyBucket) (float64, int64) {
		if allProviders {
			return b.CostUSD, b.Totals.Total()
		}
		for k, v := range b.ByProvider {
			if strings.EqualFold(k, filterProvider) {
				return v.CostUSD, v.Tokens
			}
		}
		return 0, 0
	}

	// Sum SpentUSD within [periodStartStr, periodEndStr].
	var spentUSD float64
	for _, b := range daily {
		if b.Date >= periodStartStr && b.Date <= periodEndStr {
			cost, _ := bucketSpendTokens(b)
			spentUSD += cost
		}
	}

	// Trailing 7 calendar days: [today-6 .. today].
	cutoff7 := today.AddDate(0, 0, -6)
	cutoff7Str := cutoff7.Format("2006-01-02")

	var dailySpends [7]float64
	var trail7USD float64
	var trail7Tokens int64
	completeCost := true

	for i := 0; i < 7; i++ {
		dStr := cutoff7.AddDate(0, 0, i).Format("2006-01-02")
		if b, ok := byDate[dStr]; ok {
			cost, toks := bucketSpendTokens(b)
			dailySpends[i] = cost
			trail7USD += cost
			trail7Tokens += toks
			if b.UnpricedRecords > 0 {
				completeCost = false
			}
		}
	}

	// Median of the 7 daily spends (index 3 of sorted 7-element slice).
	sortedSpends := dailySpends
	sort.Float64s(sortedSpends[:])
	medianDailyUSD := sortedSpends[3]

	// Denominator for trailing-7d average: 7 if history exists before cutoff7Str,
	// else clamped span_days in [1..7].
	denom := 7
	hasPriorHistory := false
	for _, b := range daily {
		if b.Date < cutoff7Str {
			hasPriorHistory = true
			break
		}
	}
	if !hasPriorHistory && earliestDate != "" {
		if earliestTime, err := time.Parse("2006-01-02", earliestDate); err == nil {
			spanDays := daysBetween(earliestTime, today) + 1
			if spanDays < 1 {
				spanDays = 1
			}
			if spanDays > 7 {
				spanDays = 7
			}
			denom = spanDays
		}
	}

	avgDailyUSD := trail7USD / float64(denom)
	avgDailyTokens := trail7Tokens / int64(denom)

	remainingDays := daysBetween(today, periodEnd)
	if remainingDays < 0 {
		remainingDays = 0
	}

	projectedPeriodUSD := spentUSD + medianDailyUSD*float64(remainingDays)
	projected30dUSD := avgDailyUSD * 30.0
	projected30dTokens := avgDailyTokens * 30

	var percentUsed float64
	status := PlanStatusNone
	if plan.MonthlyUSD > 0 {
		percentUsed = spentUSD / plan.MonthlyUSD
		switch {
		case percentUsed > 1.0:
			status = PlanStatusOver
		case percentUsed >= 0.8:
			status = PlanStatusNear
		default:
			status = PlanStatusUnder
		}
	}

	return PlanProjection{
		Plan:               plan,
		PeriodStart:        periodStartStr,
		PeriodEnd:          periodEndStr,
		SpentUSD:           spentUSD,
		PercentUsed:        percentUsed,
		MedianDailyUSD:     medianDailyUSD,
		AvgDailyUSD:        avgDailyUSD,
		AvgDailyTokens:     avgDailyTokens,
		RemainingDays:      remainingDays,
		ProjectedPeriodUSD: projectedPeriodUSD,
		Projected30dUSD:    projected30dUSD,
		Projected30dTokens: projected30dTokens,
		CompleteCost:       completeCost,
		Status:             status,
	}
}

func daysBetween(a, b time.Time) int {
	aUTC := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bUTC := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(bUTC.Sub(aUTC) / (24 * time.Hour))
}

package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const maxToolSummaries = 25

// Aggregate deduplicates, filters, prices, and buckets raw usage records into
// a complete Report snapshot.
func Aggregate(records []agent.UsageRecord, table *RateTable, now time.Time, loc *time.Location) Report {
	if loc == nil {
		loc = time.Local
	}
	if now.IsZero() {
		now = time.Now()
	}

	nowLocal := now.In(loc)
	report := Report{
		GeneratedAtMs: now.UnixMilli(),
		CostSource:    CostSourceUnpriced,
	}

	// Build 24 hourly slots ending at nowLocal's current hour.
	endHour := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), nowLocal.Hour(), 0, 0, 0, loc)
	hourly := make([]HourlyBucket, 24)
	for i := 0; i < 24; i++ {
		slotStart := endHour.Add(time.Duration(i-23) * time.Hour)
		hourly[i] = HourlyBucket{
			HourStartMs: slotStart.UnixMilli(),
			Label:       slotStart.Format("15:04"),
		}
	}
	firstHourMs := hourly[0].HourStartMs
	lastHourEndMs := hourly[23].HourStartMs + int64(time.Hour/time.Millisecond)

	type dayAcc struct {
		bucket   DailyBucket
		sessions map[string]struct{}
	}
	type providerAcc struct {
		summary  ProviderSummary
		sessions map[string]struct{}
		models   map[string]struct{}
	}
	type modelAcc struct {
		summary          ModelSummary
		sessions         map[string]struct{}
		providerCounts   map[string]int
		hasReported      bool
		hasModelPriced   bool
	}
	type projectAcc struct {
		summary   ProjectSummary
		sessions  map[string]struct{}
		providers map[string]struct{}
	}
	type sessionAcc struct {
		summary        SessionSummary
		models         map[string]struct{}
		hasReported    bool
		hasModelPriced bool
	}

	dailyMap := make(map[string]*dayAcc)
	providerMap := make(map[string]*providerAcc)
	modelMap := make(map[string]*modelAcc)
	projectMap := make(map[string]*projectAcc)
	sessionMap := make(map[string]*sessionAcc)
	toolCounts := make(map[string]int)

	seenDedupe := make(map[string]struct{}, len(records))
	var globalHasReported, globalHasModelPriced bool

	for _, rec := range records {
		if rec.DedupeKey != "" {
			if _, dup := seenDedupe[rec.DedupeKey]; dup {
				continue
			}
			seenDedupe[rec.DedupeKey] = struct{}{}
		}
		if rec.TimestampMs <= 0 {
			continue
		}
		if !rec.Totals.HasData() && rec.ReportedCostUSD == nil {
			continue
		}

		priced := PriceRecord(rec, table)
		recTokens := rec.Totals.Total()

		provider := strings.TrimSpace(rec.Provider)
		if provider == "" {
			provider = "unknown"
		}
		model := strings.TrimSpace(rec.Model)
		if model == "" {
			model = "unknown"
		}
		project := strings.TrimSpace(rec.Project)
		projectPath := strings.TrimSpace(rec.ProjectPath)
		if project == "" && projectPath != "" {
			project = agent.ProjectNameFromPath(projectPath)
		}
		sessionID := strings.TrimSpace(rec.SessionID)
		sessionKey := ""
		if sessionID != "" {
			sessionKey = provider + "\x00" + sessionID
		}

		// Grand totals
		report.Totals.Add(rec.Totals)
		report.CostUSD += priced.CostUSD
		report.CacheSavingsUSD += priced.CacheSavingsUSD
		report.CategoryCost.Add(priced.CategoryCost)
		report.Records++
		switch priced.CostSource {
		case CostSourceProviderReported:
			globalHasReported = true
		case CostSourceModelPriced:
			globalHasModelPriced = true
		default:
			report.UnpricedRecords++
		}

		// Daily bucket
		dateKey := time.UnixMilli(rec.TimestampMs).In(loc).Format("2006-01-02")
		da, ok := dailyMap[dateKey]
		if !ok {
			da = &dayAcc{
				bucket: DailyBucket{
					Date:       dateKey,
					ByProvider: make(map[string]SliceMetric),
					ByModel:    make(map[string]SliceMetric),
					ByProject:  make(map[string]SliceMetric),
				},
				sessions: make(map[string]struct{}),
			}
			dailyMap[dateKey] = da
		}
		da.bucket.Totals.Add(rec.Totals)
		da.bucket.CostUSD += priced.CostUSD
		da.bucket.CacheSavingsUSD += priced.CacheSavingsUSD
		da.bucket.CategoryCost.Add(priced.CategoryCost)
		da.bucket.Records++
		if priced.CostSource == CostSourceUnpriced {
			da.bucket.UnpricedRecords++
		}
		if sessionKey != "" {
			da.sessions[sessionKey] = struct{}{}
		}
		addSliceMetric(da.bucket.ByProvider, provider, recTokens, priced.CostUSD)
		addSliceMetric(da.bucket.ByModel, model, recTokens, priced.CostUSD)
		if project != "" {
			addSliceMetric(da.bucket.ByProject, project, recTokens, priced.CostUSD)
		}

		// Hourly bucket (last 24h)
		if rec.TimestampMs >= firstHourMs && rec.TimestampMs < lastHourEndMs {
			idx := int((rec.TimestampMs - firstHourMs) / int64(time.Hour/time.Millisecond))
			// Guard against DST transitions by verifying bounds.
			if idx >= 0 && idx < len(hourly) &&
				rec.TimestampMs >= hourly[idx].HourStartMs &&
				rec.TimestampMs < hourly[idx].HourStartMs+int64(time.Hour/time.Millisecond) {
				hourly[idx].Totals.Add(rec.Totals)
				hourly[idx].CostUSD += priced.CostUSD
				hourly[idx].Records++
			} else {
				for i := range hourly {
					if rec.TimestampMs >= hourly[i].HourStartMs &&
						rec.TimestampMs < hourly[i].HourStartMs+int64(time.Hour/time.Millisecond) {
						hourly[i].Totals.Add(rec.Totals)
						hourly[i].CostUSD += priced.CostUSD
						hourly[i].Records++
						break
					}
				}
			}
		}

		// Provider summary
		pa, ok := providerMap[provider]
		if !ok {
			pa = &providerAcc{
				summary:  ProviderSummary{Provider: provider},
				sessions: make(map[string]struct{}),
				models:   make(map[string]struct{}),
			}
			providerMap[provider] = pa
		}
		pa.summary.Totals.Add(rec.Totals)
		pa.summary.CostUSD += priced.CostUSD
		pa.summary.CacheSavingsUSD += priced.CacheSavingsUSD
		pa.summary.CategoryCost.Add(priced.CategoryCost)
		pa.summary.Records++
		if priced.CostSource == CostSourceUnpriced {
			pa.summary.UnpricedRecords++
		}
		if sessionKey != "" {
			pa.sessions[sessionKey] = struct{}{}
		}
		if model != "" {
			pa.models[model] = struct{}{}
		}

		// Model summary
		ma, ok := modelMap[model]
		if !ok {
			ma = &modelAcc{
				summary:        ModelSummary{Model: model},
				sessions:       make(map[string]struct{}),
				providerCounts: make(map[string]int),
			}
			modelMap[model] = ma
		}
		ma.summary.Totals.Add(rec.Totals)
		ma.summary.CostUSD += priced.CostUSD
		ma.summary.CacheSavingsUSD += priced.CacheSavingsUSD
		ma.summary.CategoryCost.Add(priced.CategoryCost)
		ma.summary.Records++
		ma.providerCounts[provider]++
		if sessionKey != "" {
			ma.sessions[sessionKey] = struct{}{}
		}
		switch priced.CostSource {
		case CostSourceProviderReported:
			ma.hasReported = true
		case CostSourceModelPriced:
			ma.hasModelPriced = true
		default:
			ma.summary.UnpricedRecords++
		}

		// Project summary
		if project != "" {
			projKey := project
			if projectPath != "" {
				projKey = projectPath
			}
			pra, ok := projectMap[projKey]
			if !ok {
				pra = &projectAcc{
					summary: ProjectSummary{
						Project: project,
						Path:    projectPath,
					},
					sessions:  make(map[string]struct{}),
					providers: make(map[string]struct{}),
				}
				projectMap[projKey] = pra
			}
			pra.summary.Totals.Add(rec.Totals)
			pra.summary.CostUSD += priced.CostUSD
			pra.summary.Records++
			if sessionKey != "" {
				pra.sessions[sessionKey] = struct{}{}
			}
			pra.providers[provider] = struct{}{}
		}

		// Session summary
		if sessionKey != "" {
			sa, ok := sessionMap[sessionKey]
			if !ok {
				sa = &sessionAcc{
					summary: SessionSummary{
						SessionID:   sessionID,
						Provider:    provider,
						Profile:     strings.TrimSpace(rec.Profile),
						Project:     project,
						ProjectPath: projectPath,
						StartedAtMs: rec.TimestampMs,
						UpdatedAtMs: rec.TimestampMs,
					},
					models: make(map[string]struct{}),
				}
				sessionMap[sessionKey] = sa
			} else {
				if sa.summary.Profile == "" && strings.TrimSpace(rec.Profile) != "" {
					sa.summary.Profile = strings.TrimSpace(rec.Profile)
				}
				if sa.summary.Project == "" && project != "" {
					sa.summary.Project = project
				}
				if sa.summary.ProjectPath == "" && projectPath != "" {
					sa.summary.ProjectPath = projectPath
				}
				if rec.TimestampMs < sa.summary.StartedAtMs {
					sa.summary.StartedAtMs = rec.TimestampMs
				}
				if rec.TimestampMs > sa.summary.UpdatedAtMs {
					sa.summary.UpdatedAtMs = rec.TimestampMs
				}
			}
			sa.summary.Totals.Add(rec.Totals)
			sa.summary.CostUSD += priced.CostUSD
			sa.summary.Records++
			if model != "" && model != "unknown" {
				sa.models[model] = struct{}{}
			}
			switch priced.CostSource {
			case CostSourceProviderReported:
				sa.hasReported = true
			case CostSourceModelPriced:
				sa.hasModelPriced = true
			}
		}

		// Tools
		for _, tool := range rec.Tools {
			tName := strings.TrimSpace(tool)
			if tName != "" {
				toolCounts[tName]++
			}
		}
	}

	// Finalize grand totals
	grandTotalTokens := report.Totals.Total()
	if inTot := report.Totals.InputTotal(); inTot > 0 {
		report.CacheHitRate = float64(report.Totals.CachedInputTokens) / float64(inTot)
	}
	if grandTotalTokens > 0 {
		report.CostPerMTokens = report.CostUSD / (float64(grandTotalTokens) / 1_000_000.0)
	}
	report.CostSource = combineCostSource(globalHasReported, globalHasModelPriced)
	report.Sessions = len(sessionMap)
	report.Hourly = hourly

	// Finalize Daily (sorted ascending by Date)
	if len(dailyMap) > 0 {
		daily := make([]DailyBucket, 0, len(dailyMap))
		for _, da := range dailyMap {
			da.bucket.Sessions = len(da.sessions)
			if len(da.bucket.ByProject) == 0 {
				da.bucket.ByProject = nil
			}
			daily = append(daily, da.bucket)
		}
		sort.Slice(daily, func(i, j int) bool {
			return daily[i].Date < daily[j].Date
		})
		report.Daily = daily
		report.ActiveDays = len(daily)

		// Streaks & Peak Day
		report.CurrentStreak, report.LongestStreak = computeStreaks(daily, nowLocal)
		for _, d := range daily {
			dt := d.Totals.Total()
			if report.PeakDayDate == "" ||
				dt > report.PeakDayTokens ||
				(dt == report.PeakDayTokens && d.CostUSD > report.PeakDayCostUSD) ||
				(dt == report.PeakDayTokens && d.CostUSD == report.PeakDayCostUSD && d.Date > report.PeakDayDate) {
				report.PeakDayDate = d.Date
				report.PeakDayTokens = dt
				report.PeakDayCostUSD = d.CostUSD
			}
		}
	}

	// Finalize Providers (sorted descending by CostUSD, tie-break Totals.Total())
	if len(providerMap) > 0 {
		providers := make([]ProviderSummary, 0, len(providerMap))
		for _, pa := range providerMap {
			s := pa.summary
			s.Sessions = len(pa.sessions)
			s.Models = sortedKeys(pa.models)
			if report.CostUSD > 0 {
				s.CostShare = s.CostUSD / report.CostUSD
			}
			if grandTotalTokens > 0 {
				s.TokenShare = float64(s.Totals.Total()) / float64(grandTotalTokens)
			}
			if inTot := s.Totals.InputTotal(); inTot > 0 {
				s.CacheHitRate = float64(s.Totals.CachedInputTokens) / float64(inTot)
			}
			providers = append(providers, s)
		}
		sort.Slice(providers, func(i, j int) bool {
			if providers[i].CostUSD != providers[j].CostUSD {
				return providers[i].CostUSD > providers[j].CostUSD
			}
			ti, tj := providers[i].Totals.Total(), providers[j].Totals.Total()
			if ti != tj {
				return ti > tj
			}
			return providers[i].Provider < providers[j].Provider
		})
		report.Providers = providers
	}

	// Finalize Models (sorted descending by CostUSD, tie-break Totals.Total())
	if len(modelMap) > 0 {
		models := make([]ModelSummary, 0, len(modelMap))
		for _, ma := range modelMap {
			s := ma.summary
			s.Sessions = len(ma.sessions)
			s.Provider = dominantKey(ma.providerCounts)
			s.CostSource = combineCostSource(ma.hasReported, ma.hasModelPriced)
			if report.CostUSD > 0 {
				s.CostShare = s.CostUSD / report.CostUSD
			}
			tot := s.Totals.Total()
			if grandTotalTokens > 0 {
				s.TokenShare = float64(tot) / float64(grandTotalTokens)
			}
			if inTot := s.Totals.InputTotal(); inTot > 0 {
				s.CacheHitRate = float64(s.Totals.CachedInputTokens) / float64(inTot)
			}
			if tot > 0 {
				s.CostPerMTokens = s.CostUSD / (float64(tot) / 1_000_000.0)
			}
			models = append(models, s)
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].CostUSD != models[j].CostUSD {
				return models[i].CostUSD > models[j].CostUSD
			}
			ti, tj := models[i].Totals.Total(), models[j].Totals.Total()
			if ti != tj {
				return ti > tj
			}
			return models[i].Model < models[j].Model
		})
		report.Models = models
	}

	// Finalize Projects (sorted descending by CostUSD, tie-break Totals.Total())
	if len(projectMap) > 0 {
		projects := make([]ProjectSummary, 0, len(projectMap))
		for _, pra := range projectMap {
			s := pra.summary
			s.Sessions = len(pra.sessions)
			s.Providers = sortedKeys(pra.providers)
			projects = append(projects, s)
		}
		sort.Slice(projects, func(i, j int) bool {
			if projects[i].CostUSD != projects[j].CostUSD {
				return projects[i].CostUSD > projects[j].CostUSD
			}
			ti, tj := projects[i].Totals.Total(), projects[j].Totals.Total()
			if ti != tj {
				return ti > tj
			}
			if projects[i].Project != projects[j].Project {
				return projects[i].Project < projects[j].Project
			}
			return projects[i].Path < projects[j].Path
		})
		report.Projects = projects
	}

	// Finalize SessionsList (sorted descending by UpdatedAtMs)
	if len(sessionMap) > 0 {
		sessions := make([]SessionSummary, 0, len(sessionMap))
		for _, sa := range sessionMap {
			s := sa.summary
			s.Models = sortedKeys(sa.models)
			s.CostSource = combineCostSource(sa.hasReported, sa.hasModelPriced)
			if s.UpdatedAtMs > s.StartedAtMs {
				s.DurationMs = s.UpdatedAtMs - s.StartedAtMs
			}
			sessions = append(sessions, s)
		}
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].UpdatedAtMs != sessions[j].UpdatedAtMs {
				return sessions[i].UpdatedAtMs > sessions[j].UpdatedAtMs
			}
			if sessions[i].CostUSD != sessions[j].CostUSD {
				return sessions[i].CostUSD > sessions[j].CostUSD
			}
			return sessions[i].SessionID < sessions[j].SessionID
		})
		report.SessionsList = sessions
	}

	// Finalize Tools (sorted descending by Count, top 25)
	if len(toolCounts) > 0 {
		tools := make([]ToolSummary, 0, len(toolCounts))
		for name, count := range toolCounts {
			tools = append(tools, ToolSummary{Name: name, Count: count})
		}
		sort.Slice(tools, func(i, j int) bool {
			if tools[i].Count != tools[j].Count {
				return tools[i].Count > tools[j].Count
			}
			return tools[i].Name < tools[j].Name
		})
		if len(tools) > maxToolSummaries {
			tools = tools[:maxToolSummaries]
		}
		report.Tools = tools
	}

	return report
}

func addSliceMetric(m map[string]SliceMetric, key string, tokens int64, costUSD float64) {
	cur := m[key]
	cur.Key = key
	cur.Tokens += tokens
	cur.CostUSD += costUSD
	cur.Records++
	m[key] = cur
}

func combineCostSource(hasReported, hasModelPriced bool) CostSource {
	switch {
	case hasReported && !hasModelPriced:
		return CostSourceProviderReported
	case hasModelPriced:
		return CostSourceModelPriced
	default:
		return CostSourceUnpriced
	}
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dominantKey(counts map[string]int) string {
	bestKey := ""
	bestCount := -1
	for k, c := range counts {
		if c > bestCount || (c == bestCount && k < bestKey) {
			bestKey = k
			bestCount = c
		}
	}
	return bestKey
}

func computeStreaks(daily []DailyBucket, nowLocal time.Time) (currentStreak, longestStreak int) {
	if len(daily) == 0 {
		return 0, 0
	}

	dateSet := make(map[string]struct{}, len(daily))
	var parsedDates []time.Time
	for _, d := range daily {
		t, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			continue
		}
		dateSet[d.Date] = struct{}{}
		parsedDates = append(parsedDates, t)
	}
	if len(parsedDates) == 0 {
		return 0, 0
	}

	// Longest streak across sorted ascending dates.
	run := 1
	longestStreak = 1
	for i := 1; i < len(parsedDates); i++ {
		if parsedDates[i-1].AddDate(0, 0, 1).Equal(parsedDates[i]) {
			run++
		} else {
			run = 1
		}
		if run > longestStreak {
			longestStreak = run
		}
	}

	// Current streak ending today or yesterday.
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.UTC)
	anchor := today
	if _, ok := dateSet[anchor.Format("2006-01-02")]; !ok {
		yesterday := today.AddDate(0, 0, -1)
		if _, okY := dateSet[yesterday.Format("2006-01-02")]; !okY {
			return 0, longestStreak
		}
		anchor = yesterday
	}

	for {
		if _, ok := dateSet[anchor.Format("2006-01-02")]; !ok {
			break
		}
		currentStreak++
		anchor = anchor.AddDate(0, 0, -1)
	}
	return currentStreak, longestStreak
}

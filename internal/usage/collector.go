package usage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	defaultCollectorTTL = 30 * time.Second
	maxScanConcurrency  = 6
	quotaFetchTimeout   = 5 * time.Second
)

type inflightCollect struct {
	done   chan struct{}
	report Report
	err    error
}

// Collector orchestrates persistent SQLite usage storage, incremental provider
// scanning, pricing, aggregation, and quota/burndown collection with instant
// DB-backed reads, 30-second in-memory caching, and concurrent call coalescing.
type Collector struct {
	opts CollectOptions
	ttl  time.Duration

	mu       sync.Mutex
	hasCache bool
	cached   Report
	cachedAt time.Time
	inflight *inflightCollect
}

// NewCollector creates a Collector rooted at homeDir using DefaultRegistry and
// standard persistent DB / cache paths.
func NewCollector(homeDir string) *Collector {
	return NewCollectorWithOptions(CollectOptions{HomeDir: homeDir})
}

// NewCollectorWithOptions creates a Collector with explicit CollectOptions.
func NewCollectorWithOptions(opts CollectOptions) *Collector {
	return &Collector{
		opts: opts,
		ttl:  defaultCollectorTTL,
	}
}

// Collect returns a usage Report.
//
// When force is false:
//  1. Serves the in-memory cached snapshot if younger than 30s.
//  2. Otherwise reads the persisted snapshot (or SQL-aggregated report) directly
//     from the persistent SQLite usage database (`usage.db`) for sub-5ms opens.
//  3. Falls back to a full scan only when the database has no persisted data yet.
//
// When force is true:
//
//	Runs an incremental scan across all registered UsageReaders, upserts new
//	records into `usage.db`, builds the report from SQLite, refreshes live
//	quotas/burndown, and persists the updated snapshot.
//
// Concurrent full-scan calls coalesce onto a single run.
func (c *Collector) Collect(ctx context.Context, force bool) (Report, error) {
	nowFn := c.opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	c.mu.Lock()
	if !force && c.hasCache && nowFn().Sub(c.cachedAt) < c.ttl {
		rep := c.cached
		c.mu.Unlock()
		return rep, nil
	}
	hadCache := c.hasCache
	opts := c.opts
	c.mu.Unlock()

	// Fast path for !force on a fresh Collector (e.g. brand-new popup process):
	// serve directly from the persistent SQLite DB if it already has a snapshot
	// or persisted usage_records (matches convstore's instant-open + background
	// refresh UX). If this Collector already had an in-memory cache whose TTL
	// expired, proceed to rescan.
	if !force && !hadCache {
		if rep, ok := loadCachedReportFromDB(opts, nowFn()); ok {
			c.mu.Lock()
			c.cached = rep
			c.cachedAt = nowFn()
			c.hasCache = true
			c.mu.Unlock()
			return rep, nil
		}
	}

	c.mu.Lock()
	if c.inflight != nil {
		flight := c.inflight
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.report, flight.err
		case <-ctx.Done():
			return Report{}, ctx.Err()
		}
	}

	flight := &inflightCollect{done: make(chan struct{})}
	c.inflight = flight
	opts = c.opts
	opts.Force = force
	c.mu.Unlock()

	rep, err := CollectWithOptions(ctx, opts)

	c.mu.Lock()
	if err == nil {
		c.cached = rep
		c.cachedAt = nowFn()
		c.hasCache = true
	}
	flight.report = rep
	flight.err = err
	c.inflight = nil
	close(flight.done)
	c.mu.Unlock()

	return rep, err
}

func resolveUsageDBPath(opts CollectOptions, homeDir string) string {
	if strings.TrimSpace(opts.DBPath) != "" {
		return strings.TrimSpace(opts.DBPath)
	}
	if homeDir != "" {
		return DefaultUsageDBPath(homeDir)
	}
	return DefaultUsageDBPath("")
}

func loadCachedReportFromDB(opts CollectOptions, now time.Time) (Report, bool) {
	homeDir := agent.RealHomeDir(opts.HomeDir)
	dbPath := resolveUsageDBPath(opts, homeDir)
	if dbPath == "" {
		return Report{}, false
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		return Report{}, false
	}
	defer func() { _ = store.Close() }()

	if snap, ok := store.LoadSnapshot(); ok {
		return snap, true
	}
	if store.RecordCount() == 0 {
		return Report{}, false
	}

	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}
	rep, err := store.BuildReport(now, loc)
	if err != nil {
		return Report{}, false
	}
	rep.HomeDir = homeDir

	var plan PlanConfig
	if opts.PlanConfig != nil {
		plan = normalizePlanConfig(*opts.PlanConfig)
	} else {
		plan = LoadPlanConfig(homeDir)
	}
	recentRecs, _ := store.QueryRecentRecords(now.AddDate(0, 0, -7).UnixMilli())
	rep.Burndown = BurndownSummary{
		ActiveBlocks:    DetectActiveBlocks(recentRecs, now),
		Projection:      ComputePlanProjection(rep.Daily, plan, now, loc),
		FiveHourHistory: BuildFiveHourHistory(recentRecs, nil, now, loc),
	}
	return rep, true
}

// CollectWithOptions performs an incremental usage scan across all registered
// UsageReaders (real home + discovered harness profile roots), upserts records
// into the persistent SQLite usage database (`usage.db`), fetches live
// QuotaProviders in parallel, queries the aggregated Report from SQLite, and
// persists the updated snapshot.
func CollectWithOptions(ctx context.Context, opts CollectOptions) (Report, error) {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}

	homeDir := agent.RealHomeDir(opts.HomeDir)
	reg := opts.Registry
	if reg == nil {
		reg = agent.DefaultRegistry()
	}

	// Open the persistent SQLite usage database (which also stores incremental
	// file-scan resume state). Fall back to SQLiteFileCache only if Store open fails.
	var (
		store     *Store
		fileCache agent.UsageFileCache
		closeFn   func()
	)
	dbPath := resolveUsageDBPath(opts, homeDir)
	if dbPath != "" {
		if st, err := OpenStore(dbPath); err == nil {
			store = st
			fileCache = st
			_ = st.PruneFilesOlderThan(now.AddDate(0, 0, -DefaultCacheRetentionDays).Unix())
			closeFn = func() { _ = st.Close() }
		}
	}
	if fileCache == nil {
		cachePath := opts.CachePath
		if cachePath == "" && homeDir != "" {
			cachePath = DefaultScanCachePath(homeDir)
		}
		cache := OpenSQLiteFileCache(cachePath)
		_ = cache.PruneOlderThan(now.AddDate(0, 0, -DefaultCacheRetentionDays).Unix())
		fileCache = cache
		closeFn = func() { _ = cache.Close() }
	}
	if closeFn != nil {
		defer closeFn()
	}

	ratesPath := opts.RatesPath
	if ratesPath == "" && homeDir != "" {
		ratesPath = DefaultRatesCachePath(homeDir)
	}

	profileRoots := reg.DiscoverProfileRoots(homeDir)

	var (
		wg       sync.WaitGroup
		recMu    sync.Mutex
		warnMu   sync.Mutex
		quotaMu  sync.Mutex
		records  []agent.UsageRecord
		warnings []string
		quotas   []agent.QuotaInfo
		table    *RateTable
	)

	addWarning := func(msg string) {
		if msg == "" {
			return
		}
		warnMu.Lock()
		warnings = append(warnings, msg)
		warnMu.Unlock()
	}

	// Fetch/load rate table in parallel with scanning and quota queries.
	wg.Add(1)
	go func() {
		defer wg.Done()
		table = LoadOrFetchRateTable(ctx, ratesPath, opts.DisableRemote)
	}()

	// Build scan jobs for all UsageReaders across realHome + matching profile roots.
	type scanJob struct {
		plugin     agent.Plugin
		homeDir    string
		profile    string
		isRealHome bool
	}
	var jobs []scanJob
	for _, p := range reg.UsageReaders() {
		agentID := p.Spec().ID
		if homeDir != "" {
			jobs = append(jobs, scanJob{
				plugin:     p,
				homeDir:    homeDir,
				profile:    "",
				isRealHome: true,
			})
		}
		for _, pr := range profileRoots {
			if pr.Agent == agentID && pr.HomeDir != "" && pr.HomeDir != homeDir {
				jobs = append(jobs, scanJob{
					plugin:     p,
					homeDir:    pr.HomeDir,
					profile:    pr.Profile,
					isRealHome: false,
				})
			}
		}
	}

	// Run scan jobs with bounded concurrency of 6.
	sem := make(chan struct{}, maxScanConcurrency)
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			reader := job.plugin.UsageReader()
			if reader == nil {
				return
			}
			recs, err := reader.ScanUsage(ctx, agent.UsageScanOptions{
				HomeDir:    job.homeDir,
				Profile:    job.profile,
				IsRealHome: job.isRealHome,
				Cache:      fileCache,
			})
			if err != nil {
				label := job.plugin.Spec().ID
				if job.profile != "" {
					label += "/" + job.profile
				}
				addWarning(fmt.Sprintf("usage scan %s: %v", label, err))
				return
			}
			if len(recs) == 0 {
				return
			}
			agentID := job.plugin.Spec().ID
			for i := range recs {
				if strings.TrimSpace(recs[i].Provider) == "" {
					recs[i].Provider = agentID
				}
				if strings.TrimSpace(recs[i].Profile) == "" && job.profile != "" {
					recs[i].Profile = job.profile
				}
			}
			recMu.Lock()
			records = append(records, recs...)
			recMu.Unlock()
		}()
	}

	// Fetch live account quotas in parallel with a bounded 5s timeout.
	quotaCtx, quotaCancel := context.WithTimeout(ctx, quotaFetchTimeout)
	defer quotaCancel()

	for _, p := range reg.QuotaProviders() {
		p := p
		agentID := p.Spec().ID
		var homeDirs []string
		if homeDir != "" {
			homeDirs = append(homeDirs, homeDir)
		}
		for _, pr := range profileRoots {
			if pr.Agent == agentID && pr.HomeDir != "" && pr.HomeDir != homeDir {
				homeDirs = append(homeDirs, pr.HomeDir)
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			qp := p.QuotaProvider()
			if qp == nil {
				return
			}
			info, err := qp.FetchQuota(quotaCtx, homeDirs)
			if err != nil {
				addWarning(fmt.Sprintf("quota %s: %v", agentID, err))
				return
			}
			if info == nil {
				return
			}
			q := *info
			if strings.TrimSpace(q.Agent) == "" {
				q.Agent = agentID
			}
			quotaMu.Lock()
			quotas = append(quotas, q)
			quotaMu.Unlock()
		}()
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	sort.Slice(quotas, func(i, j int) bool {
		return quotas[i].Agent < quotas[j].Agent
	})
	sort.Strings(warnings)

	var (
		report     Report
		recentRecs []agent.UsageRecord
	)
	if store != nil {
		if _, err := store.UpsertRecords(records, table, loc); err != nil {
			addWarning(fmt.Sprintf("usage db upsert: %v", err))
			report = Aggregate(records, table, now, loc)
			recentRecs = records
		} else if dbRep, err := store.BuildReport(now, loc); err != nil {
			addWarning(fmt.Sprintf("usage db query: %v", err))
			report = Aggregate(records, table, now, loc)
			recentRecs = records
		} else {
			report = dbRep
			if rr, rerr := store.QueryRecentRecords(now.AddDate(0, 0, -7).UnixMilli()); rerr == nil {
				recentRecs = rr
			} else {
				recentRecs = records
			}
		}
	} else {
		report = Aggregate(records, table, now, loc)
		recentRecs = records
	}

	report.HomeDir = homeDir
	report.Warnings = warnings

	var plan PlanConfig
	if opts.PlanConfig != nil {
		plan = normalizePlanConfig(*opts.PlanConfig)
	} else {
		plan = LoadPlanConfig(homeDir)
	}

	report.Burndown = BurndownSummary{
		Quotas:          quotas,
		ActiveBlocks:    DetectActiveBlocks(recentRecs, now),
		Projection:      ComputePlanProjection(report.Daily, plan, now, loc),
		FiveHourHistory: BuildFiveHourHistory(recentRecs, table, now, loc),
	}

	if store != nil {
		_ = store.SaveSnapshot(report)
	}

	return report, nil
}

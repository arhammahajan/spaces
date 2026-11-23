package usage

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

type fakeUsageReader struct {
	scanFn func(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error)
}

func (r *fakeUsageReader) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	return r.scanFn(ctx, opts)
}

type fakeQuotaProvider struct {
	fetchFn func(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error)
}

func (q *fakeQuotaProvider) FetchQuota(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error) {
	return q.fetchFn(ctx, homeDirs)
}

func TestCollectWithOptionsAndProfileRoots(t *testing.T) {
	home := t.TempDir()
	// Create a shadow profile root for claude under .harness/profiles/claude/work/home.
	profileHome := filepath.Join(home, ".harness", "profiles", "claude", "work", "home")
	if err := os.MkdirAll(profileHome, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	var seenHomes []string
	var mu sync.Mutex

	claudePlugin := &agent.BasePlugin{
		PluginSpec: agent.Spec{ID: "claude"},
		CustomUsageReader: &fakeUsageReader{
			scanFn: func(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
				mu.Lock()
				seenHomes = append(seenHomes, opts.HomeDir)
				mu.Unlock()
				return []agent.UsageRecord{
					{
						TimestampMs: now.Add(-30 * time.Minute).UnixMilli(),
						Model:       "claude-sonnet-4-6",
						SessionID:   "sess-" + opts.Profile,
						Totals:      agent.UsageTokenTotals{UncachedInputTokens: 100_000, OutputTokens: 50_000},
					},
				}, nil
			},
		},
		CustomQuotaProvider: &fakeQuotaProvider{
			fetchFn: func(ctx context.Context, homeDirs []string) (*agent.QuotaInfo, error) {
				if len(homeDirs) != 2 {
					t.Errorf("expected 2 homeDirs for quota, got %v", homeDirs)
				}
				return &agent.QuotaInfo{
					PlanType: "max",
					Primary:  &agent.QuotaWindow{Label: "5h", UsedPct: 42.5},
				}, nil
			},
		},
	}

	reg := agent.NewRegistry(claudePlugin)

	rep, err := CollectWithOptions(context.Background(), CollectOptions{
		HomeDir:       home,
		Now:           func() time.Time { return now },
		Location:      time.UTC,
		Registry:      reg,
		CachePath:     filepath.Join(home, "cache.db"),
		RatesPath:     filepath.Join(home, "rates.json"),
		DisableRemote: true,
	})
	if err != nil {
		t.Fatalf("CollectWithOptions failed: %v", err)
	}
	if len(seenHomes) != 2 {
		t.Errorf("expected 2 scan calls (real home + work profile), got %d: %v", len(seenHomes), seenHomes)
	}
	if rep.Records != 2 {
		t.Errorf("Records = %d, want 2", rep.Records)
	}
	if len(rep.Burndown.Quotas) != 1 || rep.Burndown.Quotas[0].Agent != "claude" || rep.Burndown.Quotas[0].Primary.UsedPct != 42.5 {
		t.Errorf("unexpected quotas: %+v", rep.Burndown.Quotas)
	}
	if len(rep.Burndown.ActiveBlocks) != 1 {
		t.Errorf("expected 1 active block, got %+v", rep.Burndown.ActiveBlocks)
	}
	if len(rep.Burndown.FiveHourHistory) != 1 {
		t.Errorf("expected 1 five-hour history bucket, got %+v", rep.Burndown.FiveHourHistory)
	}
}

func TestCollectorCachingAndCoalescing(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	var scans atomic.Int32
	gate := make(chan struct{})

	p := &agent.BasePlugin{
		PluginSpec: agent.Spec{ID: "claude"},
		CustomUsageReader: &fakeUsageReader{
			scanFn: func(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
				if scans.Add(1) == 1 {
					<-gate
				}
				return []agent.UsageRecord{
					{
						Provider:    "claude",
						TimestampMs: now.UnixMilli(),
						Model:       "claude-sonnet-4-6",
						Totals:      agent.UsageTokenTotals{UncachedInputTokens: 1000},
					},
				}, nil
			},
		},
	}

	reg := agent.NewRegistry(p)
	curNow := now
	var clockMu sync.Mutex
	col := NewCollectorWithOptions(CollectOptions{
		HomeDir:       home,
		Now:           func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return curNow },
		Location:      time.UTC,
		Registry:      reg,
		CachePath:     filepath.Join(home, "cache.db"),
		RatesPath:     filepath.Join(home, "rates.json"),
		DisableRemote: true,
	})

	// Launch 5 concurrent Collect calls; all should coalesce into 1 scan.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep, err := col.Collect(context.Background(), false)
			if err != nil || rep.Records != 1 {
				t.Errorf("unexpected result: records=%d err=%v", rep.Records, err)
			}
		}()
	}
	time.Sleep(25 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := scans.Load(); got != 1 {
		t.Fatalf("expected 1 coalesced scan, got %d", got)
	}

	// Within 30s TTL, cached result is served without re-scanning.
	if _, err := col.Collect(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("expected cached hit (1 scan), got %d", got)
	}

	// Force=true bypasses TTL and triggers a new scan.
	if _, err := col.Collect(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("expected force scan (2 scans), got %d", got)
	}

	// Advancing clock past 30s expires TTL.
	clockMu.Lock()
	curNow = curNow.Add(31 * time.Second)
	clockMu.Unlock()
	if _, err := col.Collect(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 3 {
		t.Fatalf("expected TTL-expired scan (3 scans), got %d", got)
	}
}

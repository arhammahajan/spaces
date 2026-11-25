package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestStoreUpsertQueryAndPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	cost1 := 1.25
	recs := []agent.UsageRecord{
		{
			Provider:    "claude",
			Profile:     "work",
			TimestampMs: now.Add(-2 * time.Hour).UnixMilli(),
			Model:       "claude-sonnet-4-6",
			SessionID:   "sess-1",
			Project:     "spaces",
			ProjectPath: "/home/dev/spaces",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 10000,
				CachedInputTokens:   50000,
				CacheCreationTokens: 2000,
				OutputTokens:        4000,
			},
			ReportedCostUSD: &cost1,
			DedupeKey:       "claude:msg1:req1",
			Tools:           []string{"Bash", "Edit"},
		},
		{
			// Duplicate DedupeKey should be idempotent.
			Provider:    "claude",
			TimestampMs: now.Add(-2 * time.Hour).UnixMilli(),
			Model:       "claude-sonnet-4-6",
			SessionID:   "sess-1",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 10000,
				OutputTokens:        4000,
			},
			DedupeKey: "claude:msg1:req1",
		},
		{
			Provider:    "codex",
			TimestampMs: now.AddDate(0, 0, -1).UnixMilli(),
			Model:       "gpt-5-codex",
			SessionID:   "sess-2",
			Project:     "api",
			ProjectPath: "/home/dev/api",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 20000,
				CachedInputTokens:   10000,
				OutputTokens:        5000,
				ReasoningTokens:     1000,
			},
			DedupeKey: "codex:turn1",
			Tools:     []string{"Bash"},
		},
	}

	written, err := store.UpsertRecords(recs, nil, time.UTC)
	if err != nil {
		t.Fatalf("UpsertRecords: %v", err)
	}
	if written != 2 {
		t.Fatalf("expected 2 deduplicated records written, got %d", written)
	}
	if got := store.RecordCount(); got != 2 {
		t.Fatalf("RecordCount: got %d, want 2", got)
	}

	// Re-open DB from disk to verify true persistence.
	_ = store.Close()
	store, err = OpenStore(dbPath)
	if err != nil {
		t.Fatalf("re-OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	daily, err := store.QueryDaily("", "", "")
	if err != nil {
		t.Fatalf("QueryDaily: %v", err)
	}
	if len(daily) != 2 {
		t.Fatalf("expected 2 daily buckets, got %d", len(daily))
	}

	claudeDaily, err := store.QueryDaily("", "", "claude")
	if err != nil {
		t.Fatalf("QueryDaily(claude): %v", err)
	}
	if len(claudeDaily) != 1 || claudeDaily[0].Records != 1 {
		t.Fatalf("expected 1 claude daily bucket, got %+v", claudeDaily)
	}

	rep, err := store.BuildReport(now, time.UTC)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	if rep.Records != 2 || rep.Sessions != 2 || len(rep.Providers) != 2 || len(rep.Models) != 2 {
		t.Fatalf("unexpected BuildReport summary: records=%d sessions=%d providers=%d models=%d",
			rep.Records, rep.Sessions, len(rep.Providers), len(rep.Models))
	}
	if len(rep.Tools) != 2 || rep.Tools[0].Name != "Bash" || rep.Tools[0].Count != 2 {
		t.Fatalf("unexpected tools rollup: %+v", rep.Tools)
	}

	// Snapshot save & reload.
	if err := store.SaveSnapshot(rep); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	loaded, ok := store.LoadSnapshot()
	if !ok {
		t.Fatalf("LoadSnapshot returned ok=false")
	}
	if loaded.Records != rep.Records || loaded.CostUSD != rep.CostUSD {
		t.Fatalf("snapshot mismatch: got %+v, want %+v", loaded, rep)
	}
}

func TestCollectorPersistsAcrossTranscriptDeletionAndInstantLoad(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

	scanCalls := 0
	var currentRecords []agent.UsageRecord
	currentRecords = []agent.UsageRecord{
		{
			Provider:    "claude",
			TimestampMs: now.Add(-1 * time.Hour).UnixMilli(),
			Model:       "claude-sonnet-4-6",
			SessionID:   "s1",
			Project:     "spaces",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 5000,
				OutputTokens:        2000,
			},
			DedupeKey: "claude:m1:r1",
		},
	}

	reg := agent.NewRegistry(&agent.BasePlugin{
		PluginSpec: agent.Spec{ID: "claude"},
		CustomUsageReader: fakeUsageReaderFunc(func(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
			scanCalls++
			return currentRecords, nil
		}),
	})

	opts := CollectOptions{
		HomeDir:       dir,
		DBPath:        dbPath,
		Registry:      reg,
		Location:      time.UTC,
		Now:           func() time.Time { return now },
		DisableRemote: true,
	}

	// 1. Initial forced scan populates usage.db.
	c1 := NewCollectorWithOptions(opts)
	rep1, err := c1.Collect(context.Background(), true)
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if rep1.Records != 1 || scanCalls != 1 {
		t.Fatalf("expected 1 record & 1 scan, got records=%d scans=%d", rep1.Records, scanCalls)
	}

	// 2. Fresh Collector (simulating a brand-new tmux popup process!) with force=false
	//    must load instantaneously from usage.db WITHOUT scanning transcripts.
	c2 := NewCollectorWithOptions(opts)
	rep2, err := c2.Collect(context.Background(), false)
	if err != nil {
		t.Fatalf("instant Collect(false): %v", err)
	}
	if scanCalls != 1 {
		t.Fatalf("expected 0 additional scans on cached DB load, got total scans=%d", scanCalls)
	}
	if rep2.Records != 1 || rep2.Sessions != 1 {
		t.Fatalf("expected persisted report from DB, got %+v", rep2)
	}

	// 3. Even if the source transcript disappears (reader returns empty), force=true
	//    preserves historical records already persisted in usage.db!
	currentRecords = nil
	rep3, err := c2.Collect(context.Background(), true)
	if err != nil {
		t.Fatalf("Collect after transcript deletion: %v", err)
	}
	if rep3.Records != 1 {
		t.Fatalf("expected historical record to persist in usage.db, got records=%d", rep3.Records)
	}
}

type fakeUsageReaderFunc func(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error)

func (f fakeUsageReaderFunc) ScanUsage(ctx context.Context, opts agent.UsageScanOptions) ([]agent.UsageRecord, error) {
	return f(ctx, opts)
}

package usage

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func TestSQLiteFileCacheRoundTripAndPrune(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage-scan-cache.db")

	cache := OpenSQLiteFileCache(dbPath)
	defer func() { _ = cache.Close() }()

	if _, ok := cache.Get("/nonexistent"); ok {
		t.Fatal("expected miss on empty cache")
	}

	reported := 0.42
	want := agent.CachedFileUsage{
		Size:         4096,
		MtimeNs:      1700000000123456789,
		ResumeOffset: 2048,
		GuardLen:     512,
		GuardHash:    uint64(1)<<63 | 0xdeadbeef, // high-bit uint64 to verify no int64 overflow
		StateJSON:    []byte(`{"cursor":42}`),
		Records: []agent.UsageRecord{
			{
				Provider:        "claude",
				TimestampMs:     1700000000000,
				Model:           "claude-sonnet-4-6",
				SessionID:       "sess-1",
				Totals:          agent.UsageTokenTotals{UncachedInputTokens: 100, OutputTokens: 50},
				ReportedCostUSD: &reported,
			},
		},
	}

	cache.Put("/tmp/transcript.jsonl", want)

	got, ok := cache.Get("/tmp/transcript.jsonl")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Size != want.Size || got.MtimeNs != want.MtimeNs || got.ResumeOffset != want.ResumeOffset ||
		got.GuardLen != want.GuardLen || got.GuardHash != want.GuardHash {
		t.Fatalf("metadata mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
	if string(got.StateJSON) != string(want.StateJSON) {
		t.Errorf("StateJSON = %q, want %q", string(got.StateJSON), string(want.StateJSON))
	}
	if len(got.Records) != 1 || got.Records[0].SessionID != "sess-1" || got.Records[0].ReportedCostUSD == nil || *got.Records[0].ReportedCostUSD != 0.42 {
		t.Errorf("Records mismatch: %+v", got.Records)
	}

	// Prune with cutoff in the past keeps the entry.
	if err := cache.PruneOlderThan(time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatalf("PruneOlderThan failed: %v", err)
	}
	if _, ok := cache.Get("/tmp/transcript.jsonl"); !ok {
		t.Fatal("expected entry to survive past cutoff")
	}

	// Prune with cutoff in the future removes the entry.
	if err := cache.PruneOlderThan(time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("PruneOlderThan failed: %v", err)
	}
	if _, ok := cache.Get("/tmp/transcript.jsonl"); ok {
		t.Fatal("expected entry to be pruned")
	}
}

func TestSQLiteFileCacheConcurrency(t *testing.T) {
	dir := t.TempDir()
	cache := OpenSQLiteFileCache(filepath.Join(dir, "concurrent.db"))
	defer func() { _ = cache.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("/tmp/file-%d.jsonl", i)
			cache.Put(key, agent.CachedFileUsage{
				Size:      int64(i * 100),
				GuardHash: uint64(i),
			})
			got, ok := cache.Get(key)
			if !ok || got.Size != int64(i*100) {
				t.Errorf("concurrent get failed for %s: %+v, %v", key, got, ok)
			}
		}()
	}
	wg.Wait()
}

func TestSQLiteFileCacheGracefulNoOpFallback(t *testing.T) {
	// Passing a directory path as DB file causes open/pragma to fail gracefully.
	dir := t.TempDir()
	cache := OpenSQLiteFileCache(dir)
	if cache == nil {
		t.Fatal("expected non-nil no-op cache")
	}
	cache.Put("/tmp/a", agent.CachedFileUsage{Size: 10})
	if _, ok := cache.Get("/tmp/a"); ok {
		t.Fatal("expected miss on no-op cache")
	}
	if err := cache.PruneOlderThan(time.Now().Unix()); err != nil {
		t.Errorf("expected nil error from no-op PruneOlderThan, got %v", err)
	}
	if err := cache.Close(); err != nil {
		t.Errorf("expected nil error from no-op Close, got %v", err)
	}
}

package usage

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestParseLiteLLMRates(t *testing.T) {
	raw := []byte(`{
		"sample_spec": {
			"input_cost_per_token": 0.0
		},
		"missing_output": {
			"input_cost_per_token": 0.000001
		},
		"anthropic/claude-sonnet-4-6": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"input_cost_per_token_priority": 0.000006,
			"output_cost_per_token_priority": 0.000030,
			"input_cost_per_token_ultrafast": 0.000009,
			"output_cost_per_token_ultrafast": 0.000045
		},
		"vertex_ai/claude-sonnet-4-6": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"input_cost_per_token_priority": 0.000006,
			"output_cost_per_token_priority": 0.000030,
			"input_cost_per_token_ultrafast": 0.000009,
			"output_cost_per_token_ultrafast": 0.000045
		},
		"openai/ambig-model": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002
		},
		"azure/ambig-model": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000010
		},
		"fast-override-model": {
			"input_cost_per_token": 0.000002,
			"output_cost_per_token": 0.000008,
			"provider_specific_entry": {
				"fast": {
					"input_cost_per_token": 0.000004,
					"output_cost_per_token": 0.000016
				}
			}
		}
	}`)

	table := ParseLiteLLMRates(raw)
	if table == nil {
		t.Fatal("expected non-nil RateTable")
	}
	if _, ok := table.Lookup("missing_output"); ok {
		t.Error("expected missing_output to be dropped")
	}
	if _, ok := table.Lookup("sample_spec"); ok {
		t.Error("expected sample_spec to be dropped")
	}

	// Unambiguous bare alias should resolve because anthropic/ and vertex_ai/ agree.
	sonnet, ok := table.Lookup("claude-sonnet-4-6")
	if !ok {
		t.Fatal("expected unambiguous bare alias claude-sonnet-4-6 to resolve")
	}
	if !almostEqual(sonnet.Standard.Input, 3e-6, 1e-12) || !almostEqual(sonnet.Standard.CacheRead, 3e-7, 1e-12) {
		t.Errorf("unexpected standard sonnet rates: %+v", sonnet.Standard)
	}
	if sonnet.Fast == nil || !almostEqual(sonnet.Fast.Input, 6e-6, 1e-12) || !almostEqual(sonnet.Fast.CacheRead, 6e-6, 1e-12) {
		t.Errorf("unexpected fast sonnet rates (cache should default to fast input): %+v", sonnet.Fast)
	}
	if sonnet.Ultrafast == nil || !almostEqual(sonnet.Ultrafast.Output, 45e-6, 1e-12) {
		t.Errorf("unexpected ultrafast sonnet rates: %+v", sonnet.Ultrafast)
	}

	// Ambiguous bare alias must NOT resolve.
	if _, ok := table.Lookup("ambig-model"); ok {
		t.Error("expected ambiguous bare alias ambig-model to NOT resolve")
	}
	// Exact provider/model still resolves.
	if e, ok := table.Lookup("openai/ambig-model"); !ok || !almostEqual(e.Standard.Input, 1e-6, 1e-12) {
		t.Errorf("expected openai/ambig-model to resolve, got %+v, %v", e, ok)
	}
	// Missing cache read/write defaults to input rate.
	fastMod, ok := table.Lookup("fast-override-model")
	if !ok {
		t.Fatal("expected fast-override-model to resolve")
	}
	if !almostEqual(fastMod.Standard.CacheRead, 2e-6, 1e-12) || !almostEqual(fastMod.Standard.CacheWrite, 2e-6, 1e-12) {
		t.Errorf("expected missing cache rates to default to input rate, got %+v", fastMod.Standard)
	}
	if fastMod.Fast == nil || !almostEqual(fastMod.Fast.Input, 4e-6, 1e-12) {
		t.Errorf("expected provider_specific_entry.fast to parse, got %+v", fastMod.Fast)
	}
}

func TestPriceRecord(t *testing.T) {
	table := ParseLiteLLMRates([]byte(`{
		"custom-model": {
			"input_cost_per_token": 0.000010,
			"output_cost_per_token": 0.000030,
			"cache_read_input_token_cost": 0.000001,
			"cache_creation_input_token_cost": 0.000012,
			"input_cost_per_token_priority": 0.000020,
			"output_cost_per_token_priority": 0.000060,
			"cache_read_input_token_cost_priority": 0.000002,
			"cache_creation_input_token_cost_priority": 0.000024
		}
	}`))

	t.Run("synthetic and alias models rejected", func(t *testing.T) {
		for _, bad := range []string{"<synthetic>", "synthetic", "opus", "sonnet", "haiku", "fable", "OPUS", "anthropic/haiku"} {
			pr := PriceRecord(agent.UsageRecord{
				Model:  bad,
				Totals: agent.UsageTokenTotals{UncachedInputTokens: 1000, OutputTokens: 500},
			}, table)
			if pr.CostSource != CostSourceUnpriced || pr.CostUSD != 0 {
				t.Errorf("model %q: expected unpriced/$0, got %s / %f", bad, pr.CostSource, pr.CostUSD)
			}
		}
	})

	t.Run("model priced without double counting reasoning tokens", func(t *testing.T) {
		pr := PriceRecord(agent.UsageRecord{
			Model: "custom-model",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 100_000, // $1.00
				CachedInputTokens:   200_000, // $0.20 (savings = 200k * (10-1)/1M = $1.80)
				CacheCreationTokens: 50_000,  // $0.60
				OutputTokens:        100_000, // $3.00
				ReasoningTokens:     40_000,  // subset of OutputTokens - MUST NOT be added
			},
		}, table)
		if pr.CostSource != CostSourceModelPriced {
			t.Fatalf("expected modelPriced, got %s", pr.CostSource)
		}
		wantCost := 1.00 + 0.20 + 0.60 + 3.00
		if !almostEqual(pr.CostUSD, wantCost, 1e-9) {
			t.Errorf("CostUSD = %f, want %f", pr.CostUSD, wantCost)
		}
		if !almostEqual(pr.CacheSavingsUSD, 1.80, 1e-9) {
			t.Errorf("CacheSavingsUSD = %f, want 1.80", pr.CacheSavingsUSD)
		}
		if !almostEqual(pr.CategoryCost.Output, 3.00, 1e-9) {
			t.Errorf("CategoryCost.Output = %f, want 3.00", pr.CategoryCost.Output)
		}
	})

	t.Run("fast speed tier and RateModel precedence", func(t *testing.T) {
		pr := PriceRecord(agent.UsageRecord{
			Model:     "display-alias",
			RateModel: "custom-model",
			Speed:     agent.UsageSpeedFast,
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 100_000, // $2.00
				OutputTokens:        100_000, // $6.00
			},
		}, table)
		if pr.CostSource != CostSourceModelPriced {
			t.Fatalf("expected modelPriced, got %s", pr.CostSource)
		}
		if !almostEqual(pr.CostUSD, 8.00, 1e-9) {
			t.Errorf("CostUSD = %f, want 8.00", pr.CostUSD)
		}
	})

	t.Run("provider reported scales category cost proportionally", func(t *testing.T) {
		reported := 2.40 // half of listCost 4.80
		pr := PriceRecord(agent.UsageRecord{
			Model:           "custom-model",
			ReportedCostUSD: &reported,
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 100_000, // list $1.00 -> scaled $0.50
				CachedInputTokens:   200_000, // list $0.20 -> scaled $0.10
				CacheCreationTokens: 50_000,  // list $0.60 -> scaled $0.30
				OutputTokens:        100_000, // list $3.00 -> scaled $1.50
			},
		}, table)
		if pr.CostSource != CostSourceProviderReported {
			t.Fatalf("expected providerReported, got %s", pr.CostSource)
		}
		if !almostEqual(pr.CostUSD, 2.40, 1e-9) {
			t.Errorf("CostUSD = %f, want 2.40", pr.CostUSD)
		}
		sum := pr.CategoryCost.Input + pr.CategoryCost.CacheRead + pr.CategoryCost.CacheWrite + pr.CategoryCost.Output
		if !almostEqual(sum, 2.40, 1e-9) || !almostEqual(pr.CategoryCost.Output, 1.50, 1e-9) {
			t.Errorf("unexpected scaled categories: %+v (sum=%f)", pr.CategoryCost, sum)
		}
	})

	t.Run("provider reported without rate puts cost in Unsplit", func(t *testing.T) {
		reported := 1.25
		pr := PriceRecord(agent.UsageRecord{
			Model:           "unknown-exotic-model",
			ReportedCostUSD: &reported,
			Totals:          agent.UsageTokenTotals{UncachedInputTokens: 1000},
		}, table)
		if pr.CostSource != CostSourceProviderReported {
			t.Fatalf("expected providerReported, got %s", pr.CostSource)
		}
		if !almostEqual(pr.CategoryCost.Unsplit, 1.25, 1e-9) {
			t.Errorf("expected Unsplit=1.25, got %+v", pr.CategoryCost)
		}
	})

	t.Run("fallback to static agent.LookupModelRates", func(t *testing.T) {
		pr := PriceRecord(agent.UsageRecord{
			Model: "claude-sonnet-4-6",
			Totals: agent.UsageTokenTotals{
				UncachedInputTokens: 1_000_000, // $3.00
				OutputTokens:        1_000_000, // $15.00
			},
		}, nil)
		if pr.CostSource != CostSourceModelPriced || !almostEqual(pr.CostUSD, 18.0, 1e-9) {
			t.Errorf("expected static fallback $18.00, got %s / %f", pr.CostSource, pr.CostUSD)
		}
	})
}

func TestLoadOrFetchRateTable(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "rates.json")

	// DisableRemote with missing cache -> nil.
	if tbl := LoadOrFetchRateTable(context.Background(), cachePath, true); tbl != nil {
		t.Fatalf("expected nil table when cache absent and disableRemote=true")
	}

	// Write fresh cache and verify DisableRemote reads it.
	sample := []byte(`{"test-model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	if err := os.WriteFile(cachePath, sample, 0o644); err != nil {
		t.Fatal(err)
	}
	tbl := LoadOrFetchRateTable(context.Background(), cachePath, true)
	if tbl == nil {
		t.Fatal("expected cached table to load")
	}
	if _, ok := tbl.Lookup("test-model"); !ok {
		t.Fatal("expected test-model in cached table")
	}

	// Stale cache + local httptest server refreshes atomically.
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(cachePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"fresh-model":{"input_cost_per_token":0.000005,"output_cost_per_token":0.000010}}`))
	}))
	defer srv.Close()

	refreshed := loadOrFetchRateTableFromURL(context.Background(), cachePath, srv.URL, false)
	if refreshed == nil {
		t.Fatal("expected refreshed table")
	}
	if _, ok := refreshed.Lookup("fresh-model"); !ok {
		t.Fatal("expected fresh-model after refresh")
	}

	// On server failure with stale cache, gracefully fall back to stale cache.
	if err := os.Chtimes(cachePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	fallback := loadOrFetchRateTableFromURL(context.Background(), cachePath, errSrv.URL, false)
	if fallback == nil {
		t.Fatal("expected fallback to stale cache on HTTP 500")
	}
	if _, ok := fallback.Lookup("fresh-model"); !ok {
		t.Fatal("expected stale cached fresh-model on fallback")
	}
}

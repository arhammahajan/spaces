package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	litellmRatesURL   = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	ratesCacheTTL     = 24 * time.Hour
	ratesFetchTimeout = 5 * time.Second
)

// TokenRates holds per-token USD pricing for a single serving tier.
type TokenRates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

func (r TokenRates) equal(other TokenRates) bool {
	return floatEqual(r.Input, other.Input) &&
		floatEqual(r.Output, other.Output) &&
		floatEqual(r.CacheRead, other.CacheRead) &&
		floatEqual(r.CacheWrite, other.CacheWrite)
}

// ModelRateEntry holds per-token USD rates for a model across speed tiers.
type ModelRateEntry struct {
	Standard  TokenRates
	Fast      *TokenRates
	Ultrafast *TokenRates
}

func (e ModelRateEntry) equal(other ModelRateEntry) bool {
	if !e.Standard.equal(other.Standard) {
		return false
	}
	if !ptrRatesEqual(e.Fast, other.Fast) {
		return false
	}
	return ptrRatesEqual(e.Ultrafast, other.Ultrafast)
}

func ptrRatesEqual(a, b *TokenRates) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.equal(*b)
}

func floatEqual(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= 1e-15 {
		return true
	}
	maxVal := math.Max(math.Abs(a), math.Abs(b))
	return diff <= maxVal*1e-9
}

// RateTable maps normalized model names and unambiguous bare-model aliases
// to per-token USD rates.
type RateTable struct {
	entries map[string]ModelRateEntry
	aliases map[string]ModelRateEntry
}

// Len returns the number of primary model entries in the table.
func (t *RateTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries)
}

// Lookup resolves a model name against exact entries, unambiguous bare aliases,
// and normalized variants (stripping [1m], trailing -YYYYMMDD, provider prefixes,
// and Claude dot/dash version forms).
func (t *RateTable) Lookup(model string) (ModelRateEntry, bool) {
	if t == nil {
		return ModelRateEntry{}, false
	}
	raw := strings.ToLower(strings.TrimSpace(model))
	if raw == "" {
		return ModelRateEntry{}, false
	}

	candidates := buildRateLookupCandidates(raw)
	for _, c := range candidates {
		if e, ok := t.entries[c]; ok {
			return e, true
		}
	}
	for _, c := range candidates {
		if e, ok := t.aliases[c]; ok {
			return e, true
		}
	}
	return ModelRateEntry{}, false
}

func buildRateLookupCandidates(raw string) []string {
	seen := make(map[string]struct{}, 8)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}

	add(raw)
	cleaned := strings.TrimSpace(strings.ReplaceAll(raw, "[1m]", ""))
	add(cleaned)

	stripped := stripDateSuffix(cleaned)
	add(stripped)

	if idx := strings.LastIndex(cleaned, "/"); idx >= 0 && idx+1 < len(cleaned) {
		bare := cleaned[idx+1:]
		add(bare)
		add(stripDateSuffix(bare))
	}

	// Add Claude dot/dash variants for each candidate collected so far.
	base := append([]string(nil), out...)
	for _, c := range base {
		if strings.HasPrefix(c, "claude-") {
			add(strings.ReplaceAll(c, ".", "-"))
		}
		if idx := strings.LastIndex(c, "/"); idx >= 0 && strings.HasPrefix(c[idx+1:], "claude-") {
			add(c[:idx+1] + strings.ReplaceAll(c[idx+1:], ".", "-"))
		}
	}
	return out
}

func stripDateSuffix(s string) string {
	if len(s) > 9 && s[len(s)-9] == '-' {
		allDigits := true
		for _, c := range s[len(s)-8:] {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return s[:len(s)-9]
		}
	}
	return s
}

type litellmRawEntry struct {
	InputCostPerToken               *float64 `json:"input_cost_per_token"`
	OutputCostPerToken              *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost         *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost     *float64 `json:"cache_creation_input_token_cost"`
	InputCostPerTokenPriority       *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerTokenPriority      *float64 `json:"output_cost_per_token_priority"`
	CacheReadInputTokenCostPriority *float64 `json:"cache_read_input_token_cost_priority"`
	CacheCreationCostPriority       *float64 `json:"cache_creation_input_token_cost_priority"`
	InputCostPerTokenUltrafast      *float64 `json:"input_cost_per_token_ultrafast"`
	OutputCostPerTokenUltrafast     *float64 `json:"output_cost_per_token_ultrafast"`
	CacheReadCostUltrafast          *float64 `json:"cache_read_input_token_cost_ultrafast"`
	CacheCreationCostUltrafast      *float64 `json:"cache_creation_input_token_cost_ultrafast"`
	ProviderSpecificEntry           *struct {
		Fast      *litellmTierOverride `json:"fast"`
		Ultrafast *litellmTierOverride `json:"ultrafast"`
	} `json:"provider_specific_entry"`
}

type litellmTierOverride struct {
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

func validRate(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	v := *p
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

func buildTierRates(inP, outP, cacheReadP, cacheWriteP *float64) *TokenRates {
	inRate, okIn := validRate(inP)
	outRate, okOut := validRate(outP)
	if !okIn || !okOut {
		return nil
	}
	cr, okCR := validRate(cacheReadP)
	if !okCR {
		cr = inRate
	}
	cw, okCW := validRate(cacheWriteP)
	if !okCW {
		cw = inRate
	}
	return &TokenRates{
		Input:      inRate,
		Output:     outRate,
		CacheRead:  cr,
		CacheWrite: cw,
	}
}

// ParseLiteLLMRates parses LiteLLM's model_prices_and_context_window.json format
// into a RateTable. Entries missing input_cost_per_token or output_cost_per_token
// are dropped. Missing cache read/write rates default to the input rate.
// Unambiguous bare-model aliases are built when all provider/model entries
// sharing a bare name agree on their rates.
func ParseLiteLLMRates(data []byte) *RateTable {
	if len(data) == 0 {
		return nil
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return nil
	}

	entries := make(map[string]ModelRateEntry, len(rawMap))
	for k, rawVal := range rawMap {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || key == "sample_spec" {
			continue
		}
		var item litellmRawEntry
		if err := json.Unmarshal(rawVal, &item); err != nil {
			continue
		}
		std := buildTierRates(
			item.InputCostPerToken,
			item.OutputCostPerToken,
			item.CacheReadInputTokenCost,
			item.CacheCreationInputTokenCost,
		)
		if std == nil {
			continue
		}

		entry := ModelRateEntry{Standard: *std}

		// Fast tier: *_priority fields take precedence, fallback to provider_specific_entry.fast.
		if fast := buildTierRates(
			item.InputCostPerTokenPriority,
			item.OutputCostPerTokenPriority,
			item.CacheReadInputTokenCostPriority,
			item.CacheCreationCostPriority,
		); fast != nil {
			entry.Fast = fast
		} else if item.ProviderSpecificEntry != nil && item.ProviderSpecificEntry.Fast != nil {
			f := item.ProviderSpecificEntry.Fast
			entry.Fast = buildTierRates(f.InputCostPerToken, f.OutputCostPerToken, f.CacheReadInputTokenCost, f.CacheCreationInputTokenCost)
		}

		// Ultrafast tier: *_ultrafast fields, fallback to provider_specific_entry.ultrafast.
		if uf := buildTierRates(
			item.InputCostPerTokenUltrafast,
			item.OutputCostPerTokenUltrafast,
			item.CacheReadCostUltrafast,
			item.CacheCreationCostUltrafast,
		); uf != nil {
			entry.Ultrafast = uf
		} else if item.ProviderSpecificEntry != nil && item.ProviderSpecificEntry.Ultrafast != nil {
			u := item.ProviderSpecificEntry.Ultrafast
			entry.Ultrafast = buildTierRates(u.InputCostPerToken, u.OutputCostPerToken, u.CacheReadInputTokenCost, u.CacheCreationInputTokenCost)
		}

		entries[key] = entry
	}

	if len(entries) == 0 {
		return nil
	}

	// Build unambiguous bare-model aliases from provider/model keys.
	type aliasCandidate struct {
		entry     ModelRateEntry
		ambiguous bool
	}
	candidates := make(map[string]*aliasCandidate)
	for key, entry := range entries {
		idx := strings.LastIndex(key, "/")
		if idx < 0 || idx+1 >= len(key) {
			continue
		}
		bare := key[idx+1:]
		if _, exists := entries[bare]; exists {
			continue
		}
		if cur, ok := candidates[bare]; !ok {
			candidates[bare] = &aliasCandidate{entry: entry}
		} else if !cur.ambiguous && !cur.entry.equal(entry) {
			cur.ambiguous = true
		}
	}

	aliases := make(map[string]ModelRateEntry)
	for bare, c := range candidates {
		if !c.ambiguous {
			aliases[bare] = c.entry
		}
	}

	return &RateTable{
		entries: entries,
		aliases: aliases,
	}
}

// DefaultRatesCachePath returns the default disk cache path for LiteLLM model rates.
func DefaultRatesCachePath(homeDir string) string {
	base := agent.RealHomeDir(homeDir)
	if base == "" {
		return ""
	}
	return filepath.Join(base, ".cache", "tmux-spaces", "usage-model-rates.json")
}

// LoadOrFetchRateTable reads the cached LiteLLM rate table from cachePath.
// If fresh (<24h) or disableRemote is true, it returns the parsed cache (or nil
// if absent). Otherwise it performs a bounded 5s HTTP GET, atomically caches on
// success, and falls back to any stale disk cache (or nil) on error.
func LoadOrFetchRateTable(ctx context.Context, cachePath string, disableRemote bool) *RateTable {
	return loadOrFetchRateTableFromURL(ctx, cachePath, litellmRatesURL, disableRemote)
}

func loadOrFetchRateTableFromURL(ctx context.Context, cachePath, url string, disableRemote bool) *RateTable {
	if cachePath == "" {
		cachePath = DefaultRatesCachePath("")
	}

	var cachedTable *RateTable
	var cacheFresh bool
	if cachePath != "" {
		if info, err := os.Stat(cachePath); err == nil && !info.IsDir() {
			if data, readErr := os.ReadFile(cachePath); readErr == nil {
				cachedTable = ParseLiteLLMRates(data)
				if cachedTable != nil && time.Since(info.ModTime()) < ratesCacheTTL {
					cacheFresh = true
				}
			}
		}
	}

	if cacheFresh || disableRemote {
		return cachedTable
	}

	fetchCtx, cancel := context.WithTimeout(ctx, ratesFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return cachedTable
	}
	client := &http.Client{Timeout: ratesFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return cachedTable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cachedTable
	}
	// Cap body at 32 MiB to guard against runaway responses.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return cachedTable
	}
	fetched := ParseLiteLLMRates(body)
	if fetched == nil {
		return cachedTable
	}

	if cachePath != "" {
		_ = writeFileAtomic(cachePath, body, 0o644)
	}
	return fetched
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-rates-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp rates cache: %w", err)
	}
	return nil
}

// PricedRecord pairs a UsageRecord with its computed USD cost, cache savings,
// category breakdown, and cost provenance.
type PricedRecord struct {
	Record          agent.UsageRecord
	CostUSD         float64
	CacheSavingsUSD float64
	CategoryCost    CategoryCost
	CostSource      CostSource
}

var unpriceableModels = map[string]struct{}{
	"<synthetic>": {},
	"synthetic":   {},
	"opus":        {},
	"sonnet":      {},
	"haiku":       {},
	"fable":       {},
}

func isUnpriceableModel(model string) bool {
	norm := strings.ToLower(strings.TrimSpace(model))
	if norm == "" {
		return true
	}
	if _, bad := unpriceableModels[norm]; bad {
		return true
	}
	if idx := strings.LastIndex(norm, "/"); idx >= 0 && idx+1 < len(norm) {
		if _, bad := unpriceableModels[norm[idx+1:]]; bad {
			return true
		}
	}
	return false
}

func lookupTokenRates(model, speed string, table *RateTable) (TokenRates, bool) {
	if isUnpriceableModel(model) {
		return TokenRates{}, false
	}
	if table != nil {
		if entry, ok := table.Lookup(model); ok {
			switch strings.ToLower(strings.TrimSpace(speed)) {
			case agent.UsageSpeedFast:
				if entry.Fast != nil {
					return *entry.Fast, true
				}
			case agent.UsageSpeedUltrafast:
				if entry.Ultrafast != nil {
					return *entry.Ultrafast, true
				}
			}
			return entry.Standard, true
		}
	}
	if staticRates, ok := agent.LookupModelRates(model); ok {
		return TokenRates{
			Input:      staticRates.InputPerM / 1_000_000.0,
			Output:     staticRates.OutputPerM / 1_000_000.0,
			CacheRead:  staticRates.CacheReadPerM / 1_000_000.0,
			CacheWrite: staticRates.CacheWritePerM / 1_000_000.0,
		}, true
	}
	return TokenRates{}, false
}

// PriceRecord computes the USD cost, cache savings, and category breakdown for
// a single UsageRecord using RateTable with fallback to agent.LookupModelRates.
// OutputTokens already includes ReasoningTokens and is never double-counted.
func PriceRecord(rec agent.UsageRecord, table *RateTable) PricedRecord {
	out := PricedRecord{Record: rec}

	lookupModel := strings.TrimSpace(rec.RateModel)
	if lookupModel == "" {
		lookupModel = strings.TrimSpace(rec.Model)
	}

	rates, rateFound := lookupTokenRates(lookupModel, rec.Speed, table)

	var listCat CategoryCost
	var listCostUSD float64
	if rateFound {
		// OutputTokens already includes ReasoningTokens; never double-count.
		listCat = CategoryCost{
			Input:      float64(rec.Totals.UncachedInputTokens) * rates.Input,
			CacheRead:  float64(rec.Totals.CachedInputTokens) * rates.CacheRead,
			CacheWrite: float64(rec.Totals.CacheCreationTokens) * rates.CacheWrite,
			Output:     float64(rec.Totals.OutputTokens) * rates.Output,
		}
		listCostUSD = listCat.Input + listCat.CacheRead + listCat.CacheWrite + listCat.Output

		if rec.Totals.CachedInputTokens > 0 && rates.Input > rates.CacheRead {
			savings := float64(rec.Totals.CachedInputTokens) * (rates.Input - rates.CacheRead)
			if !math.IsNaN(savings) && !math.IsInf(savings, 0) && savings > 0 {
				out.CacheSavingsUSD = savings
			}
		}
	}

	if rec.ReportedCostUSD != nil && !math.IsNaN(*rec.ReportedCostUSD) && !math.IsInf(*rec.ReportedCostUSD, 0) {
		reported := *rec.ReportedCostUSD
		out.CostUSD = reported
		out.CostSource = CostSourceProviderReported
		if rateFound && listCostUSD > 0 {
			scale := reported / listCostUSD
			out.CategoryCost = CategoryCost{
				Input:      listCat.Input * scale,
				CacheRead:  listCat.CacheRead * scale,
				CacheWrite: listCat.CacheWrite * scale,
				Output:     listCat.Output * scale,
			}
		} else {
			out.CategoryCost = CategoryCost{Unsplit: reported}
		}
		return out
	}

	if rateFound {
		out.CostUSD = listCostUSD
		out.CategoryCost = listCat
		out.CostSource = CostSourceModelPriced
		return out
	}

	out.CostUSD = 0
	out.CostSource = CostSourceUnpriced
	return out
}

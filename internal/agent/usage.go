package agent

import (
	"context"
	"path/filepath"
	"strings"
)

// UsageSpeed classifies provider serving speed tiers.
const (
	UsageSpeedStandard  = "standard"
	UsageSpeedFast      = "fast"
	UsageSpeedUltrafast = "ultrafast"
)

// UsageTokenTotals holds normalized, non-overlapping token counters for a
// usage record or aggregated bucket (matching t3code's token semantics):
//   - UncachedInputTokens, CachedInputTokens, and CacheCreationTokens are disjoint.
//   - OutputTokens INCLUDES ReasoningTokens; ReasoningTokens is a subset of
//     OutputTokens and must never be added on top of OutputTokens.
type UsageTokenTotals struct {
	UncachedInputTokens int64 `json:"uncached_input_tokens,omitempty"`
	CachedInputTokens   int64 `json:"cached_input_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	OutputTokens        int64 `json:"output_tokens,omitempty"`
	ReasoningTokens     int64 `json:"reasoning_tokens,omitempty"`
}

// Total returns all tokens without double-counting ReasoningTokens.
func (t UsageTokenTotals) Total() int64 {
	return t.UncachedInputTokens + t.CachedInputTokens + t.CacheCreationTokens + t.OutputTokens
}

// InputTotal returns total input tokens across uncached, cache-read, and cache-write.
func (t UsageTokenTotals) InputTotal() int64 {
	return t.UncachedInputTokens + t.CachedInputTokens + t.CacheCreationTokens
}

// Add accumulates other into t.
func (t *UsageTokenTotals) Add(other UsageTokenTotals) {
	t.UncachedInputTokens += other.UncachedInputTokens
	t.CachedInputTokens += other.CachedInputTokens
	t.CacheCreationTokens += other.CacheCreationTokens
	t.OutputTokens += other.OutputTokens
	t.ReasoningTokens += other.ReasoningTokens
}

// HasData reports whether any token counter is non-zero.
func (t UsageTokenTotals) HasData() bool {
	return t.UncachedInputTokens > 0 || t.CachedInputTokens > 0 ||
		t.CacheCreationTokens > 0 || t.OutputTokens > 0 || t.ReasoningTokens > 0
}

// UsageRecord is a single normalized usage event produced by a provider plugin's
// UsageReader (combining t3code's UsageRecord and agents-in-a-box's ProviderCall).
type UsageRecord struct {
	Provider        string           `json:"provider"`
	Profile         string           `json:"profile,omitempty"`
	TimestampMs     int64            `json:"timestamp_ms"`
	Model           string           `json:"model,omitempty"`
	RateModel       string           `json:"rate_model,omitempty"`
	SessionID       string           `json:"session_id,omitempty"`
	Project         string           `json:"project,omitempty"`
	ProjectPath     string           `json:"project_path,omitempty"`
	Totals          UsageTokenTotals `json:"totals"`
	ReportedCostUSD *float64         `json:"reported_cost_usd,omitempty"`
	Speed           string           `json:"speed,omitempty"`
	DedupeKey       string           `json:"dedupe_key,omitempty"`
	Tools           []string         `json:"tools,omitempty"`
	SourcePath      string           `json:"source_path,omitempty"`
}

// CachedFileUsage stores incremental scan state for a single transcript/log file.
type CachedFileUsage struct {
	Size         int64         `json:"size"`
	MtimeNs      int64         `json:"mtime_ns"`
	ResumeOffset int64         `json:"resume_offset"`
	GuardLen     int           `json:"guard_len"`
	GuardHash    uint64        `json:"guard_hash"`
	StateJSON    []byte        `json:"state_json,omitempty"`
	Records      []UsageRecord `json:"records,omitempty"`
}

// UsageFileCache is an optional persistent file-level scan cache passed by the
// shared usage engine into provider UsageReaders so unchanged or append-only
// transcripts avoid full reparsing.
type UsageFileCache interface {
	Get(path string) (CachedFileUsage, bool)
	Put(path string, entry CachedFileUsage)
}

// UsageScanOptions configures a provider UsageReader scan.
type UsageScanOptions struct {
	HomeDir    string
	Profile    string
	SinceMs    int64
	IsRealHome bool
	Cache      UsageFileCache
}

// UsageReader is an optional capability implemented by agent plugins that can
// scan historical token and cost usage from their native transcripts, logs, or
// local databases.
type UsageReader interface {
	ScanUsage(ctx context.Context, opts UsageScanOptions) ([]UsageRecord, error)
}

// FNV1a64 computes the 64-bit FNV-1a hash of b (used for incremental file
// guard hashes and deterministic record IDs).
func FNV1a64(b []byte) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for _, c := range b {
		h ^= uint64(c)
		h *= prime64
	}
	return h
}

// ProjectNameFromPath derives a short display project name from a working
// directory path.
func ProjectNameFromPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	cleaned := filepath.Clean(p)
	base := filepath.Base(cleaned)
	if base == "" || base == "." || base == "/" {
		return cleaned
	}
	return base
}

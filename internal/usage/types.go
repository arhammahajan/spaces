// Package usage implements the shared historical token/cost aggregation,
// dynamic/static model pricing, incremental scan caching, and burndown /
// rate-limit projection engine above provider plugins.
package usage

import (
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

// CostSource classifies how a bucket's USD cost was determined.
type CostSource string

const (
	CostSourceProviderReported CostSource = "providerReported"
	CostSourceModelPriced      CostSource = "modelPriced"
	CostSourceUnpriced         CostSource = "unpriced"
)

// CategoryCost splits USD spend across token billing categories.
type CategoryCost struct {
	Input      float64 `json:"input,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	Output     float64 `json:"output,omitempty"`
	Unsplit    float64 `json:"unsplit,omitempty"`
}

// Add accumulates other into c.
func (c *CategoryCost) Add(other CategoryCost) {
	c.Input += other.Input
	c.CacheRead += other.CacheRead
	c.CacheWrite += other.CacheWrite
	c.Output += other.Output
	c.Unsplit += other.Unsplit
}

// SliceMetric holds a compact token/cost pair for a sub-dimension within a bucket.
type SliceMetric struct {
	Key     string  `json:"key"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Records int     `json:"records"`
}

// DailyBucket aggregates usage for a single local calendar date (YYYY-MM-DD).
type DailyBucket struct {
	Date            string                 `json:"date"`
	Totals          agent.UsageTokenTotals `json:"totals"`
	CostUSD         float64                `json:"cost_usd"`
	CacheSavingsUSD float64                `json:"cache_savings_usd"`
	CategoryCost    CategoryCost           `json:"category_cost"`
	Records         int                    `json:"records"`
	UnpricedRecords int                    `json:"unpriced_records"`
	Sessions        int                    `json:"sessions"`
	ByProvider      map[string]SliceMetric `json:"by_provider,omitempty"`
	ByModel         map[string]SliceMetric `json:"by_model,omitempty"`
	ByProject       map[string]SliceMetric `json:"by_project,omitempty"`
}

// HourlyBucket aggregates usage for a single 1-hour slot (last 24h).
type HourlyBucket struct {
	HourStartMs int64                  `json:"hour_start_ms"`
	Label       string                 `json:"label"`
	Totals      agent.UsageTokenTotals `json:"totals"`
	CostUSD     float64                `json:"cost_usd"`
	Records     int                    `json:"records"`
}

// ProviderSummary aggregates totals for one coding-agent provider.
type ProviderSummary struct {
	Provider        string                 `json:"provider"`
	Totals          agent.UsageTokenTotals `json:"totals"`
	CostUSD         float64                `json:"cost_usd"`
	CacheSavingsUSD float64                `json:"cache_savings_usd"`
	CategoryCost    CategoryCost           `json:"category_cost"`
	CostShare       float64                `json:"cost_share"`
	TokenShare      float64                `json:"token_share"`
	CacheHitRate    float64                `json:"cache_hit_rate"`
	Sessions        int                    `json:"sessions"`
	Records         int                    `json:"records"`
	UnpricedRecords int                    `json:"unpriced_records"`
	Models          []string               `json:"models,omitempty"`
}

// ModelSummary aggregates totals for one model ID.
type ModelSummary struct {
	Model           string                 `json:"model"`
	Provider        string                 `json:"provider,omitempty"`
	Totals          agent.UsageTokenTotals `json:"totals"`
	CostUSD         float64                `json:"cost_usd"`
	CacheSavingsUSD float64                `json:"cache_savings_usd"`
	CategoryCost    CategoryCost           `json:"category_cost"`
	CostShare       float64                `json:"cost_share"`
	TokenShare      float64                `json:"token_share"`
	CacheHitRate    float64                `json:"cache_hit_rate"`
	CostPerMTokens  float64                `json:"cost_per_m_tokens"`
	Sessions        int                    `json:"sessions"`
	Records         int                    `json:"records"`
	UnpricedRecords int                    `json:"unpriced_records"`
	CostSource      CostSource             `json:"cost_source"`
}

// ProjectSummary aggregates totals for one workspace/project directory.
type ProjectSummary struct {
	Project   string                 `json:"project"`
	Path      string                 `json:"path,omitempty"`
	Totals    agent.UsageTokenTotals `json:"totals"`
	CostUSD   float64                `json:"cost_usd"`
	Sessions  int                    `json:"sessions"`
	Records   int                    `json:"records"`
	Providers []string               `json:"providers,omitempty"`
}

// SessionSummary aggregates totals for one conversation/session.
type SessionSummary struct {
	SessionID   string                 `json:"session_id"`
	Provider    string                 `json:"provider"`
	Profile     string                 `json:"profile,omitempty"`
	Project     string                 `json:"project,omitempty"`
	ProjectPath string                 `json:"project_path,omitempty"`
	Models      []string               `json:"models,omitempty"`
	Totals      agent.UsageTokenTotals `json:"totals"`
	CostUSD     float64                `json:"cost_usd"`
	Records     int                    `json:"records"`
	StartedAtMs int64                  `json:"started_at_ms"`
	UpdatedAtMs int64                  `json:"updated_at_ms"`
	DurationMs  int64                  `json:"duration_ms"`
	CostSource  CostSource             `json:"cost_source"`
}

// ToolSummary counts invocations of a tool across sessions.
type ToolSummary struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ActiveBlock represents a locally aggregated 5-hour rolling token block
// (adapted from agents-in-a-box's Tier-2 live_window block detector).
type ActiveBlock struct {
	Provider    string  `json:"provider"`
	StartMs     int64   `json:"start_ms"`
	EndMs       int64   `json:"end_ms"`
	LastEntryMs int64   `json:"last_entry_ms"`
	TotalTokens int64   `json:"total_tokens"`
	LimitTokens int64   `json:"limit_tokens"`
	UsedPct     float64 `json:"used_pct"`
	ResetsAt    int64   `json:"resets_at"` // unix seconds
	Active      bool    `json:"active"`
}

// FiveHourBucket aggregates usage within a 5-hour rolling period for Plan history.
type FiveHourBucket struct {
	StartMs    int64                  `json:"start_ms"`
	EndMs      int64                  `json:"end_ms"`
	Label      string                 `json:"label"`
	Provider   string                 `json:"provider,omitempty"`
	Totals     agent.UsageTokenTotals `json:"totals"`
	CostUSD    float64                `json:"cost_usd"`
	Records    int                    `json:"records"`
	Current    bool                   `json:"current"`
	ByProvider map[string]SliceMetric `json:"by_provider,omitempty"`
	ByModel    map[string]SliceMetric `json:"by_model,omitempty"`
}

// PlanConfig configures optional monthly budget / plan burndown tracking.
type PlanConfig struct {
	Name       string  `toml:"name" json:"name"`
	MonthlyUSD float64 `toml:"monthly_usd" json:"monthly_usd"`
	Provider   string  `toml:"provider" json:"provider"` // "" or "all" for all providers
	ResetDay   int     `toml:"reset_day" json:"reset_day"`
}

// PlanStatus summarizes budget pace relative to MonthlyUSD.
type PlanStatus string

const (
	PlanStatusNone  PlanStatus = "none"
	PlanStatusUnder PlanStatus = "under"
	PlanStatusNear  PlanStatus = "near"
	PlanStatusOver  PlanStatus = "over"
)

// PlanProjection holds burndown metrics and 30-day linear / median projections.
type PlanProjection struct {
	Plan               PlanConfig `json:"plan"`
	PeriodStart        string     `json:"period_start"` // YYYY-MM-DD
	PeriodEnd          string     `json:"period_end"`   // YYYY-MM-DD
	SpentUSD           float64    `json:"spent_usd"`
	PercentUsed        float64    `json:"percent_used"`
	MedianDailyUSD     float64    `json:"median_daily_usd"`
	AvgDailyUSD        float64    `json:"avg_daily_usd"`
	AvgDailyTokens     int64      `json:"avg_daily_tokens"`
	RemainingDays      int        `json:"remaining_days"`
	ProjectedPeriodUSD float64    `json:"projected_period_usd"`
	Projected30dUSD    float64    `json:"projected_30d_usd"`
	Projected30dTokens int64      `json:"projected_30d_tokens"`
	CompleteCost       bool       `json:"complete_cost"`
	Status             PlanStatus `json:"status"`
}

// BurndownSummary combines live provider quotas, local 5h blocks, 5h history,
// and monthly burndown projections.
type BurndownSummary struct {
	Quotas          []agent.QuotaInfo `json:"quotas,omitempty"`
	ActiveBlocks    []ActiveBlock     `json:"active_blocks,omitempty"`
	Projection      PlanProjection    `json:"projection"`
	FiveHourHistory []FiveHourBucket  `json:"five_hour_history,omitempty"`
}

// CollectOptions configures a usage collection run.
type CollectOptions struct {
	HomeDir       string
	Force         bool
	Now           func() time.Time
	Registry      *agent.Registry
	Location      *time.Location
	CachePath     string
	RatesPath     string
	PlanConfig    *PlanConfig
	DisableRemote bool
}

// Report is the complete aggregated usage and burndown snapshot rendered by
// the Usage popup TUI.
type Report struct {
	GeneratedAtMs   int64                  `json:"generated_at_ms"`
	HomeDir         string                 `json:"home_dir,omitempty"`
	Totals          agent.UsageTokenTotals `json:"totals"`
	CostUSD         float64                `json:"cost_usd"`
	CacheSavingsUSD float64                `json:"cache_savings_usd"`
	CategoryCost    CategoryCost           `json:"category_cost"`
	CacheHitRate    float64                `json:"cache_hit_rate"`
	CostPerMTokens  float64                `json:"cost_per_m_tokens"`
	CostSource      CostSource             `json:"cost_source"`
	Records         int                    `json:"records"`
	UnpricedRecords int                    `json:"unpriced_records"`
	Sessions        int                    `json:"sessions"`
	ActiveDays      int                    `json:"active_days"`
	CurrentStreak   int                    `json:"current_streak"`
	LongestStreak   int                    `json:"longest_streak"`
	PeakDayDate     string                 `json:"peak_day_date,omitempty"`
	PeakDayTokens   int64                  `json:"peak_day_tokens"`
	PeakDayCostUSD  float64                `json:"peak_day_cost_usd"`
	Daily           []DailyBucket          `json:"daily,omitempty"`
	Hourly          []HourlyBucket         `json:"hourly,omitempty"`
	Providers       []ProviderSummary      `json:"providers,omitempty"`
	Models          []ModelSummary         `json:"models,omitempty"`
	Projects        []ProjectSummary       `json:"projects,omitempty"`
	SessionsList    []SessionSummary       `json:"sessions_list,omitempty"`
	Tools           []ToolSummary          `json:"tools,omitempty"`
	Burndown        BurndownSummary        `json:"burndown"`
	Warnings        []string               `json:"warnings,omitempty"`
}

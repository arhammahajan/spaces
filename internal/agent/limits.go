package agent

import "context"

// UsageLimitWindowKind classifies a provider rate-limit window cadence.
const (
	LimitWindowSession = "session"
	LimitWindowWeekly  = "weekly"
	LimitWindowMonthly = "monthly"
	LimitWindowOther   = "other"
)

// UsageLimitUnavailableReason distinguishes permanent unsupported accounts
// (e.g. API key auth) from transient probe failures.
const (
	LimitUnavailableUnsupported = "unsupported"
	LimitUnavailableProbeFailed = "probeFailed"
)

// UsageLimitWindow represents a single normalized provider rate-limit window
// (matching t3code's ServerProviderUsageWindow).
type UsageLimitWindow struct {
	ID                 string  `json:"id"`
	Kind               string  `json:"kind"` // "session", "weekly", "monthly", "other"
	Label              string  `json:"label"`
	UsedPercent        float64 `json:"used_percent"` // [0, 100]
	ResetsAt           string  `json:"resets_at,omitempty"`
	WindowDurationMins int     `json:"window_duration_mins,omitempty"`
}

// ResetCredits describes available rate-limit reset credits on an account
// (e.g. Claude cedar_ember grants, Codex reset credits, Antigravity credits).
type ResetCredits struct {
	AvailableCount int    `json:"available_count"`
	NextExpiresAt  string `json:"next_expires_at,omitempty"`
	NextCreditID   string `json:"next_credit_id,omitempty"`
}

// UsageLimitUnavailable explains why a provider/profile could not report windows.
type UsageLimitUnavailable struct {
	Reason  string `json:"reason"` // "unsupported" or "probeFailed"
	Message string `json:"message,omitempty"`
}

// UsageLimitQuery supplies profile and environment context to a provider's
// UsageLimitProvider so each profile's credentials are read in isolation.
type UsageLimitQuery struct {
	Profile     string            `json:"profile"`
	DisplayName string            `json:"display_name,omitempty"`
	HomeDir     string            `json:"home_dir"`
	RealHomeDir string            `json:"real_home_dir"`
	IsDefault   bool              `json:"is_default"`
	Env         map[string]string `json:"env,omitempty"`
	BinaryPath  string            `json:"binary_path,omitempty"`
}

// ProviderUsageLimits is the raw per-profile limit snapshot returned by a
// provider plugin before shared normalization and identity-key merging.
type ProviderUsageLimits struct {
	Provider              string                 `json:"provider"`
	Profile               string                 `json:"profile"`
	DisplayName           string                 `json:"display_name,omitempty"`
	Email                 string                 `json:"email,omitempty"`
	Plan                  string                 `json:"plan,omitempty"`
	WorkspaceID           string                 `json:"workspace_id,omitempty"`
	CredentialFingerprint string                 `json:"credential_fingerprint,omitempty"`
	IdentityKey           string                 `json:"identity_key,omitempty"`
	CheckedAt             string                 `json:"checked_at"`
	Windows               []UsageLimitWindow     `json:"windows"`
	ResetCredits          *ResetCredits          `json:"reset_credits,omitempty"`
	Unavailable           *UsageLimitUnavailable `json:"unavailable,omitempty"`
	Source                string                 `json:"source,omitempty"`
}

// UsageLimitProvider is an optional capability implemented by agent plugins
// that can query live per-profile plan/rate-limit windows and reset credits
// over their native SDK, CLI, or API.
type UsageLimitProvider interface {
	FetchUsageLimits(ctx context.Context, q UsageLimitQuery) (*ProviderUsageLimits, error)
}

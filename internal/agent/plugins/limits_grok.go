package plugins

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var grokBillingAPIURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

const (
	grokPrimaryAuthKey   = "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828"
	grokSecondaryAuthKey = "https://accounts.x.ai/sign-in"
)

type grokUsageLimitProvider struct{}

// NewGrokUsageLimitProvider returns a UsageLimitProvider for Grok Build.
func NewGrokUsageLimitProvider() agent.UsageLimitProvider {
	return &grokUsageLimitProvider{}
}

type grokAuthEntry struct {
	Key      string `json:"key"`
	AuthMode string `json:"auth_mode"`
	Email    string `json:"email"`
}

type grokBillingResponse struct {
	Config *struct {
		CreditUsagePercent *float64 `json:"creditUsagePercent"`
		CurrentPeriod      *struct {
			Type string `json:"type"`
			End  string `json:"end"`
		} `json:"currentPeriod"`
	} `json:"config"`
}

func (p *grokUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	if limitEnv(q, "XAI_API_KEY") != "" {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableUnsupported, "Grok usage limits are not available with XAI_API_KEY.", now), nil
	}

	entry, err := readGrokAuthEntry(q)
	if err != nil {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Grok credentials.", now), nil
	}
	if entry == nil || strings.EqualFold(strings.TrimSpace(entry.AuthMode), "api_key") || strings.TrimSpace(entry.Key) == "" {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableUnsupported, "Grok subscription credentials not found.", now), nil
	}

	key := strings.TrimSpace(entry.Key)
	email := strings.TrimSpace(entry.Email)

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, grokBillingAPIURL, nil)
	if err != nil {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Could not build Grok billing request.", now), nil
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := client.Do(req)
	if err != nil {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Grok could not read usage limits.", now), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Grok could not read usage limits.", now), nil
	}

	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Grok could not read usage limits.", now), nil
	}

	var payload grokBillingResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return unavailableLimits("grok", q.Profile, agent.LimitUnavailableProbeFailed, "Grok returned invalid billing payload.", now), nil
	}

	windows := parseGrokBillingWindows(&payload)
	return &agent.ProviderUsageLimits{
		Provider:              "grok",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Email:                 email,
		CredentialFingerprint: sha256Hex("grok\x00" + key),
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		Source:                "api",
	}, nil
}

func readGrokAuthEntry(q agent.UsageLimitQuery) (*grokAuthEntry, error) {
	creds := make(map[string]grokAuthEntry)
	if raw := limitEnv(q, "GROK_AUTH"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			return nil, err
		}
	} else {
		grokHome := limitEnv(q, "GROK_HOME")
		if grokHome == "" {
			homeDir := limitHomeDir(q)
			if homeDir != "" {
				grokHome = filepath.Join(homeDir, ".grok")
			}
		}
		if grokHome == "" {
			return nil, nil
		}
		exists, err := readJSONFile(filepath.Join(grokHome, "auth.json"), &creds)
		if !exists {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}

	if entry, ok := creds[grokPrimaryAuthKey]; ok {
		return &entry, nil
	}
	if entry, ok := creds[grokSecondaryAuthKey]; ok {
		return &entry, nil
	}
	return nil, nil
}

func parseGrokBillingWindows(payload *grokBillingResponse) []agent.UsageLimitWindow {
	if payload.Config == nil || payload.Config.CreditUsagePercent == nil {
		return []agent.UsageLimitWindow{}
	}
	pct := *payload.Config.CreditUsagePercent
	if math.IsNaN(pct) || math.IsInf(pct, 0) {
		return []agent.UsageLimitWindow{}
	}

	kind := agent.LimitWindowOther
	label := "Subscription"
	var resetsAt string
	if payload.Config.CurrentPeriod != nil {
		pt := strings.TrimPrefix(strings.TrimSpace(payload.Config.CurrentPeriod.Type), "USAGE_PERIOD_TYPE_")
		switch pt {
		case "WEEKLY":
			kind = agent.LimitWindowWeekly
			label = "Weekly"
		case "MONTHLY":
			kind = agent.LimitWindowMonthly
			label = "Monthly"
		}
		resetsAt = formatISOTime(payload.Config.CurrentPeriod.End)
	}

	return []agent.UsageLimitWindow{
		{
			ID:          "subscription",
			Kind:        kind,
			Label:       label,
			UsedPercent: clampPercent(pct),
			ResetsAt:    resetsAt,
		},
	}
}

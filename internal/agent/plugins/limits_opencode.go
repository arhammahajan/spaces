package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var openCodeUsageAPIURL = "https://opencode.ai/zen/go/v1/usage"

type openCodeUsageLimitProvider struct{}

// NewOpenCodeUsageLimitProvider returns a UsageLimitProvider for OpenCode Go.
func NewOpenCodeUsageLimitProvider() agent.UsageLimitProvider {
	return &openCodeUsageLimitProvider{}
}

type openCodeAuthFile struct {
	OpenCodeGo *struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	} `json:"opencode-go"`
}

type openCodeUsageWindow struct {
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resetsAt"`
}

type openCodeUsageResponse struct {
	Usage *struct {
		Rolling *openCodeUsageWindow `json:"rolling"`
		Weekly  *openCodeUsageWindow `json:"weekly"`
		Monthly *openCodeUsageWindow `json:"monthly"`
	} `json:"usage"`
}

func (p *openCodeUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	apiKey, err := resolveOpenCodeAPIKey(q)
	if err != nil {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read OpenCode credentials.", now), nil
	}
	if apiKey == "" {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableUnsupported, "OpenCode Go credentials not found.", now), nil
	}

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeUsageAPIURL, nil)
	if err != nil {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "Could not build OpenCode usage request.", now), nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "OpenCode Go could not read usage.", now), nil
	}
	defer resp.Body.Close()

	// A valid Zen key can exist without a Go subscription.
	if resp.StatusCode == http.StatusForbidden {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableUnsupported, "OpenCode Go subscription is not active.", now), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "OpenCode Go could not read usage.", now), nil
	}

	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "OpenCode Go could not read usage.", now), nil
	}

	var payload openCodeUsageResponse
	if err := json.Unmarshal(body, &payload); err != nil || payload.Usage == nil {
		return unavailableLimits("opencode", q.Profile, agent.LimitUnavailableProbeFailed, "OpenCode Go returned invalid usage payload.", now), nil
	}

	windows := make([]agent.UsageLimitWindow, 0, 3)
	if payload.Usage.Rolling != nil && payload.Usage.Rolling.Percent != nil {
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "go_rolling",
			Kind:               agent.LimitWindowSession,
			Label:              "Go · Session",
			UsedPercent:        clampPercent(*payload.Usage.Rolling.Percent),
			ResetsAt:           formatISOTime(payload.Usage.Rolling.ResetsAt),
			WindowDurationMins: sessionWindowMins,
		})
	}
	if payload.Usage.Weekly != nil && payload.Usage.Weekly.Percent != nil {
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "go_weekly",
			Kind:               agent.LimitWindowWeekly,
			Label:              "Go · Weekly",
			UsedPercent:        clampPercent(*payload.Usage.Weekly.Percent),
			ResetsAt:           formatISOTime(payload.Usage.Weekly.ResetsAt),
			WindowDurationMins: weeklyWindowMins,
		})
	}
	if payload.Usage.Monthly != nil && payload.Usage.Monthly.Percent != nil {
		windows = append(windows, agent.UsageLimitWindow{
			ID:          "go_monthly",
			Kind:        agent.LimitWindowMonthly,
			Label:       "Go · Monthly",
			UsedPercent: clampPercent(*payload.Usage.Monthly.Percent),
			ResetsAt:    formatISOTime(payload.Usage.Monthly.ResetsAt),
		})
	}

	return &agent.ProviderUsageLimits{
		Provider:              "opencode",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Plan:                  "Go",
		CredentialFingerprint: sha256Hex("opencode-go\x00" + apiKey),
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		Source:                "api",
	}, nil
}

func resolveOpenCodeAPIKey(q agent.UsageLimitQuery) (string, error) {
	var auth openCodeAuthFile
	if raw := limitEnv(q, "OPENCODE_AUTH_CONTENT"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &auth); err != nil {
			return "", err
		}
	} else {
		dataHome := limitEnv(q, "XDG_DATA_HOME")
		if dataHome == "" {
			homeDir := limitHomeDir(q)
			if homeDir != "" {
				dataHome = filepath.Join(homeDir, ".local", "share")
			}
		}
		if dataHome != "" {
			if _, err := readJSONFile(filepath.Join(dataHome, "opencode", "auth.json"), &auth); err != nil {
				return "", err
			}
		}
	}
	if auth.OpenCodeGo != nil && auth.OpenCodeGo.Type == "api" && strings.TrimSpace(auth.OpenCodeGo.Key) != "" {
		return strings.TrimSpace(auth.OpenCodeGo.Key), nil
	}
	return limitEnv(q, "OPENCODE_API_KEY"), nil
}

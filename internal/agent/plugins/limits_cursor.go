package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var cursorDefaultAPIEndpoint = "https://api2.cursor.sh"

type cursorUsageLimitProvider struct{}

// NewCursorUsageLimitProvider returns a UsageLimitProvider for Cursor.
func NewCursorUsageLimitProvider() agent.UsageLimitProvider {
	return &cursorUsageLimitProvider{}
}

type cursorAuthFile struct {
	AccessToken string `json:"accessToken"`
	Email       string `json:"email"`
}

type cursorUsageResponse struct {
	BillingCycleEnd json.RawMessage `json:"billingCycleEnd"`
	PlanUsage       *struct {
		TotalPercentUsed *float64 `json:"totalPercentUsed"`
		AutoPercentUsed  *float64 `json:"autoPercentUsed"`
		APIPercentUsed   *float64 `json:"apiPercentUsed"`
	} `json:"planUsage"`
}

func (p *cursorUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	token := limitEnv(q, "CURSOR_AUTH_TOKEN")
	if token == "" && limitEnv(q, "CURSOR_API_KEY") != "" {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableUnsupported, "Cursor usage limits require a CLI login or CURSOR_AUTH_TOKEN.", now), nil
	}

	var email string
	if token == "" {
		homeDir := limitHomeDir(q)
		var candidates []string
		if xdg := limitEnv(q, "XDG_CONFIG_HOME"); xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "cursor", "auth.json"))
		} else if homeDir != "" {
			candidates = append(candidates, filepath.Join(homeDir, ".config", "cursor", "auth.json"))
		}
		if homeDir != "" {
			candidates = append(candidates, filepath.Join(homeDir, ".cursor", "auth.json"))
		}
		for _, path := range candidates {
			var creds cursorAuthFile
			exists, err := readJSONFile(path, &creds)
			if !exists || err != nil {
				continue
			}
			if t := strings.TrimSpace(creds.AccessToken); t != "" {
				token = t
				email = strings.TrimSpace(creds.Email)
				break
			}
		}
	}

	if token == "" {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableUnsupported, "Cursor credentials not found.", now), nil
	}

	endpoint := limitEnv(q, "CURSOR_API_ENDPOINT")
	if endpoint == "" {
		endpoint = cursorDefaultAPIEndpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", bytes.NewReader([]byte("{}")))
	if err != nil {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableProbeFailed, "Could not build Cursor usage request.", now), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("connect-protocol-version", "1")
	req.Header.Set("x-cursor-client-type", "cli")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableProbeFailed, "Cursor could not read usage limits.", now), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableProbeFailed, "Cursor could not read usage limits.", now), nil
	}

	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableProbeFailed, "Cursor could not read usage limits.", now), nil
	}

	var payload cursorUsageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableProbeFailed, "Cursor returned invalid usage payload.", now), nil
	}

	resetsAt := parseCursorBillingCycleEnd(payload.BillingCycleEnd)
	windows := make([]agent.UsageLimitWindow, 0, 3)
	if payload.PlanUsage != nil {
		defs := []struct {
			id    string
			label string
			val   *float64
		}{
			{id: "totalPercentUsed", label: "Overall", val: payload.PlanUsage.TotalPercentUsed},
			{id: "autoPercentUsed", label: "Cursor Models", val: payload.PlanUsage.AutoPercentUsed},
			{id: "apiPercentUsed", label: "Other Models", val: payload.PlanUsage.APIPercentUsed},
		}
		for _, d := range defs {
			if d.val == nil || math.IsNaN(*d.val) || math.IsInf(*d.val, 0) {
				continue
			}
			windows = append(windows, agent.UsageLimitWindow{
				ID:          d.id,
				Kind:        agent.LimitWindowMonthly,
				Label:       d.label,
				UsedPercent: clampPercent(*d.val),
				ResetsAt:    resetsAt,
			})
		}
	}

	if len(windows) == 0 {
		return unavailableLimits("cursor-agent", q.Profile, agent.LimitUnavailableUnsupported, "Cursor returned no plan usage windows.", now), nil
	}

	return &agent.ProviderUsageLimits{
		Provider:              "cursor-agent",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Email:                 email,
		CredentialFingerprint: sha256Hex("cursor\x00" + token),
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		Source:                "api",
	}, nil
}

func parseCursorBillingCycleEnd(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil && num > 0 {
		return formatEpochMillis(num)
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		str = strings.TrimSpace(str)
		if str == "" {
			return ""
		}
		if parsed, err := strconv.ParseFloat(str, 64); err == nil && parsed > 0 {
			return formatEpochMillis(parsed)
		}
		return formatISOTime(str)
	}
	return ""
}

package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var codexUsageAPIURL = "https://chatgpt.com/backend-api/wham/usage"

type codexUsageLimitProvider struct{}

// NewCodexUsageLimitProvider returns a UsageLimitProvider for OpenAI Codex.
func NewCodexUsageLimitProvider() agent.UsageLimitProvider {
	return &codexUsageLimitProvider{}
}

type codexLimitAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   *struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type codexWhamWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
	ResetAt            *float64 `json:"reset_at"`
	ResetsAt           *float64 `json:"resets_at"`
	ResetAfterSeconds  *float64 `json:"reset_after_seconds"`
}

type codexResetCreditItem struct {
	Status       string   `json:"status"`
	ExpiresAt    *float64 `json:"expires_at"`
	ExpiresAtCml *float64 `json:"expiresAt"`
}

type codexResetCreditsPayload struct {
	AvailableCount    *int                   `json:"available_count"`
	AvailableCountCml *int                   `json:"availableCount"`
	Credits           []codexResetCreditItem `json:"credits"`
}

type codexWhamUsageResponse struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		PrimaryWindow   *codexWhamWindow `json:"primary_window"`
		SecondaryWindow *codexWhamWindow `json:"secondary_window"`
	} `json:"rate_limit"`
	ResetCredits    *codexResetCreditsPayload `json:"reset_credits"`
	ResetCreditsCml *codexResetCreditsPayload `json:"resetCredits"`
	Credits         *codexResetCreditsPayload `json:"credits"`
}

func (p *codexUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	homeDir := limitHomeDir(q)
	codexHome := limitEnv(q, "CODEX_HOME")
	if codexHome == "" && homeDir != "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}
	if codexHome == "" {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableUnsupported, "Codex home directory not found.", now), nil
	}

	var auth codexLimitAuthFile
	exists, err := readJSONFile(filepath.Join(codexHome, "auth.json"), &auth)
	if !exists {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableUnsupported, "Codex auth.json not found.", now), nil
	}
	if err != nil {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Codex auth.json.", now), nil
	}

	if strings.EqualFold(strings.TrimSpace(auth.AuthMode), "api_key") || auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableUnsupported, "Codex usage limits require ChatGPT OAuth authentication.", now), nil
	}

	accessToken := strings.TrimSpace(auth.Tokens.AccessToken)
	accountID := strings.TrimSpace(auth.Tokens.AccountID)
	email, jwtPlan, jwtAccountID := decodeCodexIDTokenClaims(auth.Tokens.IDToken)
	if accountID == "" {
		accountID = jwtAccountID
	}

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageAPIURL, nil)
	if err != nil {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Could not build Codex usage request.", now), nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	req.Header.Set("originator", "codex_cli_rs")

	resp, err := client.Do(req)
	if err != nil {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Codex usage limits.", now), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Codex usage endpoint returned an error.", now), nil
	}

	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Codex usage response.", now), nil
	}

	var payload codexWhamUsageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return unavailableLimits("codex", q.Profile, agent.LimitUnavailableProbeFailed, "Could not decode Codex usage response.", now), nil
	}

	plan := strings.TrimSpace(payload.PlanType)
	if plan == "" {
		plan = jwtPlan
	}

	windows := parseCodexWhamWindows(&payload, plan, now)
	resetCredits := parseCodexResetCredits(&payload)

	return &agent.ProviderUsageLimits{
		Provider:              "codex",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Email:                 email,
		Plan:                  plan,
		WorkspaceID:           accountID,
		CredentialFingerprint: sha256Hex("codex\x00" + accountID + "\x00" + accessToken),
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		ResetCredits:          resetCredits,
		Source:                "api",
	}, nil
}

func decodeCodexIDTokenClaims(idToken string) (email, plan, accountID string) {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return "", "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  *struct {
			PlanType  string `json:"chatgpt_plan_type"`
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", "", ""
	}
	email = strings.TrimSpace(claims.Email)
	if claims.Auth != nil {
		plan = strings.TrimSpace(claims.Auth.PlanType)
		accountID = strings.TrimSpace(claims.Auth.AccountID)
	}
	return email, plan, accountID
}

func parseCodexWhamWindows(payload *codexWhamUsageResponse, plan string, now time.Time) []agent.UsageLimitWindow {
	if payload.RateLimit == nil {
		return []agent.UsageLimitWindow{}
	}
	isMonthlyPlan := strings.EqualFold(plan, "free") || strings.EqualFold(plan, "go")
	primaryFallbackMins := sessionWindowMins
	if isMonthlyPlan {
		primaryFallbackMins = monthlyWindowMins
	}

	slots := []struct {
		id           string
		w            *codexWhamWindow
		fallbackMins int
	}{
		{id: "primary", w: payload.RateLimit.PrimaryWindow, fallbackMins: primaryFallbackMins},
		{id: "secondary", w: payload.RateLimit.SecondaryWindow, fallbackMins: weeklyWindowMins},
	}

	windows := make([]agent.UsageLimitWindow, 0, 2)
	for _, slot := range slots {
		if slot.w == nil || slot.w.UsedPercent == nil {
			continue
		}
		if math.IsNaN(*slot.w.UsedPercent) || math.IsInf(*slot.w.UsedPercent, 0) {
			continue
		}
		mins := slot.fallbackMins
		if slot.w.LimitWindowSeconds != nil && *slot.w.LimitWindowSeconds > 0 {
			mins = int(*slot.w.LimitWindowSeconds / 60)
		}
		kind := codexKindForDuration(mins)
		var resetsAt string
		switch {
		case slot.w.ResetAt != nil && *slot.w.ResetAt > 0:
			resetsAt = formatEpochSeconds(*slot.w.ResetAt)
		case slot.w.ResetsAt != nil && *slot.w.ResetsAt > 0:
			resetsAt = formatEpochSeconds(*slot.w.ResetsAt)
		case slot.w.ResetAfterSeconds != nil && *slot.w.ResetAfterSeconds > 0:
			resetsAt = now.Add(time.Duration(*slot.w.ResetAfterSeconds * float64(time.Second))).UTC().Format(time.RFC3339)
		}
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 slot.id,
			Kind:               kind,
			Label:              codexLabelForKind(kind),
			UsedPercent:        clampPercent(*slot.w.UsedPercent),
			ResetsAt:           resetsAt,
			WindowDurationMins: mins,
		})
	}
	return windows
}

func codexKindForDuration(mins int) string {
	if mins >= monthlyWindowMins {
		return agent.LimitWindowMonthly
	}
	if mins >= weeklyWindowMins {
		return agent.LimitWindowWeekly
	}
	return agent.LimitWindowSession
}

func codexLabelForKind(kind string) string {
	switch kind {
	case agent.LimitWindowSession:
		return "Session"
	case agent.LimitWindowWeekly:
		return "Weekly"
	default:
		return "Monthly"
	}
}

func parseCodexResetCredits(payload *codexWhamUsageResponse) *agent.ResetCredits {
	for _, blk := range []*codexResetCreditsPayload{payload.ResetCredits, payload.ResetCreditsCml, payload.Credits} {
		if blk == nil {
			continue
		}
		var count *int
		if blk.AvailableCount != nil {
			count = blk.AvailableCount
		} else if blk.AvailableCountCml != nil {
			count = blk.AvailableCountCml
		}
		if count == nil {
			continue
		}
		avail := *count
		if avail < 0 {
			avail = 0
		}
		var earliest float64
		for _, c := range blk.Credits {
			if c.Status != "available" {
				continue
			}
			var exp *float64
			if c.ExpiresAt != nil {
				exp = c.ExpiresAt
			} else if c.ExpiresAtCml != nil {
				exp = c.ExpiresAtCml
			}
			if exp == nil || math.IsNaN(*exp) || math.IsInf(*exp, 0) || *exp <= 0 {
				continue
			}
			if earliest == 0 || *exp < earliest {
				earliest = *exp
			}
		}
		return &agent.ResetCredits{
			AvailableCount: avail,
			NextExpiresAt:  formatEpochSeconds(earliest),
		}
	}
	return nil
}

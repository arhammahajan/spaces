package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var (
	claudeUsageAPIURL = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"
	claudeGrantIDRe   = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	claudeNonAlnumRe  = regexp.MustCompile(`[^a-z0-9]+`)
)

type claudeUsageLimitProvider struct{}

// NewClaudeUsageLimitProvider returns a UsageLimitProvider for Claude Code.
func NewClaudeUsageLimitProvider() agent.UsageLimitProvider {
	return &claudeUsageLimitProvider{}
}

type claudeCredentialsFile struct {
	ClaudeAiOauth *struct {
		AccessToken      string `json:"accessToken"`
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

type claudeAccountConfigFile struct {
	OAuthAccount *struct {
		EmailAddress     string `json:"emailAddress"`
		OrganizationUUID string `json:"organizationUuid"`
		BillingType      string `json:"billingType"`
	} `json:"oauthAccount"`
}

type claudeUsageWindowPayload struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeModelScopedPayload struct {
	DisplayName string   `json:"display_name"`
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeCedarEmberGrant struct {
	ID         string `json:"id"`
	ResetsLeft *int   `json:"resets_left"`
	EndsAt     string `json:"ends_at"`
	Paused     bool   `json:"paused"`
	UsableNow  bool   `json:"usable_now"`
}

type claudeCedarEmberPayload struct {
	Eligible    bool                    `json:"eligible"`
	Grants      []claudeCedarEmberGrant `json:"grants"`
	NextGrantID string                  `json:"next_grant_id"`
}

type claudeUsageResponse struct {
	FiveHour    *claudeUsageWindowPayload  `json:"five_hour"`
	SevenDay    *claudeUsageWindowPayload  `json:"seven_day"`
	ModelScoped []claudeModelScopedPayload `json:"model_scoped"`
	RateLimits  *struct {
		FiveHour    *claudeUsageWindowPayload  `json:"five_hour"`
		SevenDay    *claudeUsageWindowPayload  `json:"seven_day"`
		ModelScoped []claudeModelScopedPayload `json:"model_scoped"`
	} `json:"rate_limits"`
	CedarEmber *claudeCedarEmberPayload `json:"cedar_ember"`
}

func (p *claudeUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	homeDir := limitHomeDir(q)
	configDir := limitEnv(q, "CLAUDE_CONFIG_DIR")
	if configDir == "" && homeDir != "" {
		configDir = filepath.Join(homeDir, ".claude")
	}
	if configDir == "" {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableUnsupported, "Claude configuration directory not found.", now), nil
	}

	var creds claudeCredentialsFile
	_, _ = readJSONFile(filepath.Join(configDir, ".credentials.json"), &creds)

	var acctCfg claudeAccountConfigFile
	foundAcct, _ := readJSONFile(filepath.Join(configDir, ".claude.json"), &acctCfg)
	if (!foundAcct || acctCfg.OAuthAccount == nil) && q.IsDefault && homeDir != "" {
		_, _ = readJSONFile(filepath.Join(homeDir, ".claude.json"), &acctCfg)
	}

	var (
		accessToken string
		plan        string
		email       string
		workspaceID string
	)
	if creds.ClaudeAiOauth != nil {
		accessToken = strings.TrimSpace(creds.ClaudeAiOauth.AccessToken)
		plan = strings.TrimSpace(creds.ClaudeAiOauth.SubscriptionType)
	}
	if acctCfg.OAuthAccount != nil {
		email = strings.TrimSpace(acctCfg.OAuthAccount.EmailAddress)
		workspaceID = strings.TrimSpace(acctCfg.OAuthAccount.OrganizationUUID)
		if plan == "" {
			plan = strings.TrimSpace(acctCfg.OAuthAccount.BillingType)
		}
	}

	if accessToken == "" {
		if local := readClaudeLocalRateLimits(configDir, q, email, plan, workspaceID, now); local != nil {
			return local, nil
		}
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableUnsupported, "Claude OAuth credentials not found.", now), nil
	}

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageAPIURL, nil)
	if err != nil {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableProbeFailed, "Could not build Claude usage request.", now), nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")

	resp, err := client.Do(req)
	if err != nil {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Claude usage limits.", now), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableProbeFailed, "Claude usage endpoint returned an error.", now), nil
	}

	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableProbeFailed, "Could not read Claude usage response.", now), nil
	}

	var payload claudeUsageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return unavailableLimits("claude", q.Profile, agent.LimitUnavailableProbeFailed, "Could not decode Claude usage response.", now), nil
	}

	windows := parseClaudeUsageWindows(&payload)
	resetCredits := parseClaudeCedarEmber(payload.CedarEmber, now)

	return &agent.ProviderUsageLimits{
		Provider:              "claude",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Email:                 email,
		Plan:                  plan,
		WorkspaceID:           workspaceID,
		CredentialFingerprint: sha256Hex("claude\x00" + accessToken),
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		ResetCredits:          resetCredits,
		Source:                "api",
	}, nil
}

func parseClaudeUsageWindows(payload *claudeUsageResponse) []agent.UsageLimitWindow {
	fiveHour := payload.FiveHour
	sevenDay := payload.SevenDay
	modelScoped := payload.ModelScoped
	if payload.RateLimits != nil {
		if fiveHour == nil {
			fiveHour = payload.RateLimits.FiveHour
		}
		if sevenDay == nil {
			sevenDay = payload.RateLimits.SevenDay
		}
		if len(modelScoped) == 0 {
			modelScoped = payload.RateLimits.ModelScoped
		}
	}

	windows := make([]agent.UsageLimitWindow, 0, 2+len(modelScoped))
	if fiveHour != nil && fiveHour.Utilization != nil {
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "five_hour",
			Kind:               agent.LimitWindowSession,
			Label:              "Session",
			UsedPercent:        clampPercent(*fiveHour.Utilization),
			ResetsAt:           formatISOTime(fiveHour.ResetsAt),
			WindowDurationMins: sessionWindowMins,
		})
	}
	if sevenDay != nil && sevenDay.Utilization != nil {
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "seven_day",
			Kind:               agent.LimitWindowWeekly,
			Label:              "Weekly",
			UsedPercent:        clampPercent(*sevenDay.Utilization),
			ResetsAt:           formatISOTime(sevenDay.ResetsAt),
			WindowDurationMins: weeklyWindowMins,
		})
	}
	for _, entry := range modelScoped {
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" || entry.Utilization == nil {
			continue
		}
		slug := claudeNonAlnumRe.ReplaceAllString(strings.ToLower(name), "_")
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "seven_day_" + slug,
			Kind:               agent.LimitWindowWeekly,
			Label:              "Weekly · " + name,
			UsedPercent:        clampPercent(*entry.Utilization),
			ResetsAt:           formatISOTime(entry.ResetsAt),
			WindowDurationMins: weeklyWindowMins,
		})
	}
	return windows
}

func parseClaudeCedarEmber(ce *claudeCedarEmberPayload, now time.Time) *agent.ResetCredits {
	if ce == nil || !ce.Eligible {
		return nil
	}
	live := make([]claudeCedarEmberGrant, 0, len(ce.Grants))
	for _, g := range ce.Grants {
		if !claudeGrantIDRe.MatchString(g.ID) {
			continue
		}
		if g.ResetsLeft == nil || *g.ResetsLeft < 0 {
			continue
		}
		if g.Paused || !g.UsableNow {
			continue
		}
		if strings.TrimSpace(g.EndsAt) != "" {
			t, ok := parseISOTime(g.EndsAt)
			if !ok || !t.After(now) {
				continue
			}
		}
		live = append(live, g)
	}

	var next *claudeCedarEmberGrant
	for i := range live {
		if live[i].ID == ce.NextGrantID {
			next = &live[i]
			break
		}
	}
	if next == nil {
		return &agent.ResetCredits{AvailableCount: 0}
	}
	total := 0
	for _, g := range live {
		total += *g.ResetsLeft
	}
	return &agent.ResetCredits{
		AvailableCount: total,
		NextExpiresAt:  formatISOTime(next.EndsAt),
		NextCreditID:   next.ID,
	}
}

func readClaudeLocalRateLimits(configDir string, q agent.UsageLimitQuery, email, plan, workspaceID string, now time.Time) *agent.ProviderUsageLimits {
	for _, name := range []string{"spaces-rate-limits.json", "abtop-rate-limits.json"} {
		path := filepath.Join(configDir, name)
		var payload claudeUsageResponse
		exists, err := readJSONFile(path, &payload)
		if !exists || err != nil {
			continue
		}
		windows := parseClaudeUsageWindows(&payload)
		if len(windows) == 0 {
			continue
		}
		return &agent.ProviderUsageLimits{
			Provider:     "claude",
			Profile:      q.Profile,
			DisplayName:  q.DisplayName,
			Email:        email,
			Plan:         plan,
			WorkspaceID:  workspaceID,
			CheckedAt:    now.Format(time.RFC3339),
			Windows:      windows,
			ResetCredits: parseClaudeCedarEmber(payload.CedarEmber, now),
			Source:       "local-state",
		}
	}
	return nil
}

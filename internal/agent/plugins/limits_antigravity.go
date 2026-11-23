package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

var (
	antigravityEndpoints = []string{
		"https://daily-cloudcode-pa.googleapis.com",
		"https://cloudcode-pa.googleapis.com",
	}
	antigravityTokenURL    = "https://oauth2.googleapis.com/token"
	antigravityUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	antigravityCLIRefresh  = refreshAntigravityTokenViaCLI
)

type antigravityUsageLimitProvider struct{}

// NewAntigravityUsageLimitProvider returns a UsageLimitProvider for Google Antigravity.
func NewAntigravityUsageLimitProvider() agent.UsageLimitProvider {
	return &antigravityUsageLimitProvider{}
}

type antigravityOAuthTokenFile struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
		ExpiresIn    int64  `json:"expires_in"`
	} `json:"token"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ProjectID    string `json:"project_id"`
	AuthMethod   string `json:"auth_method"`
	IDTokenEmail string `json:"id_token_email"`
	Email        string `json:"email"`
}

type antigravityACPTokenFile struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	ProjectID    string `json:"project_id"`
}

type antigravityResolvedCreds struct {
	accessToken string
	projectID   string
	email       string
	fallbackKey string
}

type antigravityQuota struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

type antigravityBucket struct {
	BucketID          string   `json:"bucketId"`
	Window            string   `json:"window"`
	DisplayName       string   `json:"displayName"`
	Disabled          bool     `json:"disabled"`
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

type antigravityCreditsBlock struct {
	AvailableCount *int   `json:"availableCount"`
	AvailableSnake *int   `json:"available_count"`
	NextExpiresAt  string `json:"nextExpiresAt"`
	NextExpiresSnk string `json:"next_expires_at"`
	NextCreditID   string `json:"nextCreditId"`
	NextCreditSnk  string `json:"next_credit_id"`
}

type antigravitySummaryResponse struct {
	Groups []struct {
		DisplayName string              `json:"displayName"`
		Buckets     []antigravityBucket `json:"buckets"`
	} `json:"groups"`
	Buckets         []antigravityBucket      `json:"buckets"`
	ResetCredits    *antigravityCreditsBlock `json:"resetCredits"`
	ResetCreditsSnk *antigravityCreditsBlock `json:"reset_credits"`
	Credits         *antigravityCreditsBlock `json:"credits"`
	AvailableResets *int                     `json:"availableResets"`
	AvailableRstSnk *int                     `json:"available_resets"`
}

type antigravityModelsResponse struct {
	Models map[string]struct {
		ModelProvider   string            `json:"modelProvider"`
		QuotaInfo       *antigravityQuota `json:"quotaInfo"`
		WeeklyQuotaInfo *antigravityQuota `json:"weeklyQuotaInfo"`
	} `json:"models"`
}

func (p *antigravityUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	homeDir := limitHomeDir(q)
	if homeDir == "" {
		return unavailableLimits("antigravity", q.Profile, agent.LimitUnavailableUnsupported, "Sign in with Google to see Antigravity limits.", now), nil
	}

	client := newLimitHTTPClient(defaultLimitHTTPTimeout)
	creds, unavail := resolveAntigravityCredentials(ctx, client, q, homeDir, now)
	if unavail != nil {
		return unavailableLimits("antigravity", q.Profile, unavail.Reason, unavail.Message, now), nil
	}

	ua := fmt.Sprintf("antigravity/hub/1.0.0 (aidev_client; os_type=%s; arch=%s; cl=963137146)", runtime.GOOS, runtime.GOARCH)
	bodyMap := map[string]any{}
	if creds.projectID != "" {
		bodyMap["project"] = creds.projectID
	}
	reqBody, _ := json.Marshal(bodyMap)

	// Query userinfo and retrieveUserQuotaSummary concurrently.
	var (
		userID    string
		userEmail string
		uiWG      sync.WaitGroup
	)
	uiWG.Add(1)
	go func() {
		defer uiWG.Done()
		userID, userEmail = fetchAntigravityUserInfo(ctx, client, creds.accessToken)
	}()

	windows, resetCredits := fetchAntigravitySummary(ctx, client, creds.accessToken, ua, reqBody)
	hasGemini := false
	hasThirdParty := false
	for _, w := range windows {
		low := strings.ToLower(w.ID)
		if strings.HasPrefix(low, "gemini-") {
			hasGemini = true
		}
		if strings.HasPrefix(low, "3p-") || strings.HasPrefix(low, "claude-") || strings.HasPrefix(low, "gpt-") {
			hasThirdParty = true
		}
	}
	if len(windows) == 0 || !hasGemini {
		modelWins := fetchAntigravityModels(ctx, client, creds.accessToken, ua, reqBody)
		if len(modelWins) > 0 {
			merged := make(map[string]agent.UsageLimitWindow, len(windows)+len(modelWins))
			for _, w := range windows {
				merged[w.ID] = w
			}
			for _, w := range modelWins {
				low := strings.ToLower(w.ID)
				if hasThirdParty && (strings.HasPrefix(low, "claude-") || strings.HasPrefix(low, "gpt-")) {
					continue
				}
				if prev, ok := merged[w.ID]; !ok || w.UsedPercent > prev.UsedPercent {
					merged[w.ID] = w
				}
			}
			windows = sortedLimitWindows(merged)
		}
	}
	uiWG.Wait()

	if len(windows) == 0 {
		return unavailableLimits("antigravity", q.Profile, agent.LimitUnavailableProbeFailed, "Antigravity did not return usable quota information.", now), nil
	}

	email := strings.TrimSpace(creds.email)
	if email == "" {
		email = strings.TrimSpace(userEmail)
	}
	fingerprint := creds.fallbackKey
	if userID != "" {
		fingerprint = sha256Hex("google-account\x00" + userID)
	}

	return &agent.ProviderUsageLimits{
		Provider:              "antigravity",
		Profile:               q.Profile,
		DisplayName:           q.DisplayName,
		Email:                 email,
		CredentialFingerprint: fingerprint,
		CheckedAt:             now.Format(time.RFC3339),
		Windows:               windows,
		ResetCredits:          resetCredits,
		Source:                "api",
	}, nil
}

func resolveAntigravityCredentials(ctx context.Context, client *http.Client, q agent.UsageLimitQuery, homeDir string, now time.Time) (*antigravityResolvedCreds, *agent.UsageLimitUnavailable) {
	primaryPath := filepath.Join(homeDir, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	var primary antigravityOAuthTokenFile
	primaryExists, primaryErr := readJSONFile(primaryPath, &primary)
	primaryExpired := false
	if primaryExists && primaryErr == nil {
		accessToken := strings.TrimSpace(primary.Token.AccessToken)
		email := strings.TrimSpace(primary.IDTokenEmail)
		if email == "" {
			email = strings.TrimSpace(primary.Email)
		}
		expired := false
		if primary.Token.Expiry != "" {
			if exp, ok := parseISOTime(primary.Token.Expiry); ok && !exp.After(now.Add(60*time.Second)) {
				expired = true
			}
		}
		if accessToken != "" && !expired {
			return &antigravityResolvedCreds{
				accessToken: accessToken,
				projectID:   strings.TrimSpace(primary.ProjectID),
				email:       email,
				fallbackKey: sha256Hex("antigravity\x00" + accessToken),
			}, nil
		}
		if strings.TrimSpace(primary.ClientID) != "" &&
			strings.TrimSpace(primary.ClientSecret) != "" &&
			strings.TrimSpace(primary.Token.RefreshToken) != "" {
			refreshed, err := exchangeAntigravityRefreshToken(ctx, client, primary.ClientID, primary.ClientSecret, primary.Token.RefreshToken)
			if err != nil {
				return nil, &agent.UsageLimitUnavailable{
					Reason:  agent.LimitUnavailableProbeFailed,
					Message: "Could not refresh Antigravity OAuth token.",
				}
			}
			return &antigravityResolvedCreds{
				accessToken: refreshed,
				projectID:   strings.TrimSpace(primary.ProjectID),
				email:       email,
				fallbackKey: sha256Hex("antigravity\x00" + refreshed),
			}, nil
		}
		// Native antigravity-cli tokens omit client_id/client_secret; ask the
		// profile's agy binary (with DBUS_SESSION_BUS_ADDRESS=disabled:) to
		// refresh the file in place when a refresh_token is present.
		if expired && strings.TrimSpace(primary.Token.RefreshToken) != "" {
			primaryExpired = true
			if bin := resolveAntigravityBinary(q); bin != "" && antigravityCLIRefresh != nil {
				if err := antigravityCLIRefresh(ctx, bin, homeDir); err == nil {
					var reloaded antigravityOAuthTokenFile
					if ok, rerr := readJSONFile(primaryPath, &reloaded); ok && rerr == nil {
						newTok := strings.TrimSpace(reloaded.Token.AccessToken)
						stillExpired := false
						if reloaded.Token.Expiry != "" {
							if exp, ok := parseISOTime(reloaded.Token.Expiry); ok && !exp.After(now.Add(60*time.Second)) {
								stillExpired = true
							}
						}
						if newTok != "" && !stillExpired {
							newEmail := strings.TrimSpace(reloaded.IDTokenEmail)
							if newEmail == "" {
								newEmail = email
							}
							return &antigravityResolvedCreds{
								accessToken: newTok,
								projectID:   strings.TrimSpace(reloaded.ProjectID),
								email:       newEmail,
								fallbackKey: sha256Hex("antigravity\x00" + newTok),
							}, nil
						}
					}
				}
			}
		}
	}

	secondaryPath := filepath.Join(homeDir, "antigravity-acp", "acp_token.json")
	var secondary antigravityACPTokenFile
	secondaryExists, secondaryErr := readJSONFile(secondaryPath, &secondary)
	if secondaryExists && secondaryErr == nil &&
		strings.TrimSpace(secondary.ClientID) != "" &&
		strings.TrimSpace(secondary.ClientSecret) != "" &&
		strings.TrimSpace(secondary.RefreshToken) != "" {
		refreshed, err := exchangeAntigravityRefreshToken(ctx, client, secondary.ClientID, secondary.ClientSecret, secondary.RefreshToken)
		if err != nil {
			return nil, &agent.UsageLimitUnavailable{
				Reason:  agent.LimitUnavailableProbeFailed,
				Message: "Could not refresh Antigravity OAuth token.",
			}
		}
		return &antigravityResolvedCreds{
			accessToken: refreshed,
			projectID:   strings.TrimSpace(secondary.ProjectID),
			fallbackKey: sha256Hex("antigravity\x00" + refreshed),
		}, nil
	}

	if primaryExpired {
		return nil, &agent.UsageLimitUnavailable{
			Reason:  agent.LimitUnavailableProbeFailed,
			Message: "Antigravity OAuth token expired; open profile to refresh.",
		}
	}

	if primaryExists && primaryErr != nil || secondaryExists && secondaryErr != nil {
		return nil, &agent.UsageLimitUnavailable{
			Reason:  agent.LimitUnavailableProbeFailed,
			Message: "Could not read Antigravity token file.",
		}
	}

	return nil, &agent.UsageLimitUnavailable{
		Reason:  agent.LimitUnavailableUnsupported,
		Message: "Sign in with Google to see Antigravity limits.",
	}
}

func resolveAntigravityBinary(q agent.UsageLimitQuery) string {
	if p := strings.TrimSpace(q.BinaryPath); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	if realHome := strings.TrimSpace(q.RealHomeDir); realHome != "" {
		cand := filepath.Join(realHome, ".local", "libexec", "antigravity", "agy")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath("agy"); err == nil && p != "" {
		return p
	}
	return ""
}

func refreshAntigravityTokenViaCLI(ctx context.Context, binPath, homeDir string) error {
	runCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binPath, "models")
	baseEnv := os.Environ()
	env := make([]string, 0, len(baseEnv)+3)
	for _, kv := range baseEnv {
		if k, _, ok := strings.Cut(kv, "="); ok {
			switch k {
			case "HOME", "XDG_CONFIG_HOME", "DBUS_SESSION_BUS_ADDRESS":
				continue
			}
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+filepath.Join(homeDir, ".config"),
		"DBUS_SESSION_BUS_ADDRESS=disabled:",
	)
	cmd.Env = env
	return cmd.Run()
}

func exchangeAntigravityRefreshToken(ctx context.Context, client *http.Client, clientID, clientSecret, refreshToken string) (string, error) {
	form := url.Values{
		"client_id":     {strings.TrimSpace(clientID)},
		"client_secret": {strings.TrimSpace(clientSecret)},
		"refresh_token": {strings.TrimSpace(refreshToken)},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh status %d", resp.StatusCode)
	}
	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return "", err
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	token := strings.TrimSpace(parsed.AccessToken)
	if token == "" {
		return "", fmt.Errorf("empty access_token in refresh response")
	}
	return token, nil
}

func fetchAntigravityUserInfo(ctx context.Context, client *http.Client, accessToken string) (string, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, antigravityUserInfoURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", ""
	}
	body, err := readHTTPBodyLimited(resp.Body)
	if err != nil {
		return "", ""
	}
	var info struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", ""
	}
	return strings.TrimSpace(info.ID), strings.TrimSpace(info.Email)
}

func fetchAntigravitySummary(ctx context.Context, client *http.Client, accessToken, ua string, reqBody []byte) ([]agent.UsageLimitWindow, *agent.ResetCredits) {
	type result struct {
		windows []agent.UsageLimitWindow
		credits *agent.ResetCredits
	}
	endpoints := antigravityEndpoints
	results := make([]result, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(idx int, endpoint string) {
			defer wg.Done()
			endpoint = strings.TrimRight(endpoint, "/")
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1internal:retrieveUserQuotaSummary", bytes.NewReader(reqBody))
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("User-Agent", ua)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return
			}
			body, err := readHTTPBodyLimited(resp.Body)
			if err != nil {
				return
			}
			var summary antigravitySummaryResponse
			if err := json.Unmarshal(body, &summary); err != nil {
				return
			}
			results[idx] = result{
				windows: parseAntigravitySummaryWindows(&summary),
				credits: parseAntigravityResetCredits(&summary),
			}
		}(i, ep)
	}
	wg.Wait()

	merged := make(map[string]agent.UsageLimitWindow)
	var credits *agent.ResetCredits
	for _, r := range results {
		for _, w := range r.windows {
			if prev, ok := merged[w.ID]; !ok || w.UsedPercent > prev.UsedPercent {
				merged[w.ID] = w
			}
		}
		if r.credits != nil {
			if credits == nil || r.credits.AvailableCount > credits.AvailableCount {
				credits = r.credits
			}
		}
	}
	return sortedLimitWindows(merged), credits
}

func parseAntigravitySummaryWindows(summary *antigravitySummaryResponse) []agent.UsageLimitWindow {
	type bucketGroup struct {
		displayName string
		buckets     []antigravityBucket
	}
	var groups []bucketGroup
	hasGroupBuckets := false
	for _, g := range summary.Groups {
		if len(g.Buckets) > 0 {
			hasGroupBuckets = true
			break
		}
	}
	if hasGroupBuckets {
		for _, g := range summary.Groups {
			groups = append(groups, bucketGroup{displayName: g.DisplayName, buckets: g.Buckets})
		}
	} else if len(summary.Buckets) > 0 {
		groups = append(groups, bucketGroup{buckets: summary.Buckets})
	}

	byID := make(map[string]agent.UsageLimitWindow)
	for _, group := range groups {
		for _, b := range group.buckets {
			if b.Disabled || strings.TrimSpace(b.BucketID) == "" || b.RemainingFraction == nil {
				continue
			}
			if math.IsNaN(*b.RemainingFraction) || math.IsInf(*b.RemainingFraction, 0) {
				continue
			}
			family := strings.TrimSpace(group.displayName)
			switch {
			case strings.HasPrefix(b.BucketID, "gemini-"):
				family = "Gemini"
			case strings.HasPrefix(b.BucketID, "3p-"):
				family = "Claude & GPT (shared)"
			case family == "":
				family = b.BucketID
			}
			var (
				kind       = agent.LimitWindowOther
				duration   int
				windowName = strings.TrimSpace(b.DisplayName)
			)
			switch b.Window {
			case "weekly":
				kind = agent.LimitWindowWeekly
				duration = weeklyWindowMins
				windowName = "Weekly"
			case "5h":
				kind = agent.LimitWindowSession
				duration = sessionWindowMins
				windowName = "5-hour"
			default:
				if windowName == "" {
					windowName = "Quota"
				}
			}
			w := agent.UsageLimitWindow{
				ID:                 b.BucketID,
				Kind:               kind,
				Label:              fmt.Sprintf("%s · %s", family, windowName),
				UsedPercent:        (1 - clampFraction(*b.RemainingFraction)) * 100,
				ResetsAt:           formatISOTime(b.ResetTime),
				WindowDurationMins: duration,
			}
			byID[w.ID] = w
		}
	}
	return sortedLimitWindows(byID)
}

func parseAntigravityResetCredits(summary *antigravitySummaryResponse) *agent.ResetCredits {
	for _, blk := range []*antigravityCreditsBlock{summary.ResetCredits, summary.ResetCreditsSnk, summary.Credits} {
		if blk == nil {
			continue
		}
		var count *int
		if blk.AvailableCount != nil {
			count = blk.AvailableCount
		} else if blk.AvailableSnake != nil {
			count = blk.AvailableSnake
		}
		if count == nil {
			continue
		}
		avail := *count
		if avail < 0 {
			avail = 0
		}
		nextExp := blk.NextExpiresAt
		if nextExp == "" {
			nextExp = blk.NextExpiresSnk
		}
		nextID := strings.TrimSpace(blk.NextCreditID)
		if nextID == "" {
			nextID = strings.TrimSpace(blk.NextCreditSnk)
		}
		return &agent.ResetCredits{
			AvailableCount: avail,
			NextExpiresAt:  formatISOTime(nextExp),
			NextCreditID:   nextID,
		}
	}
	if summary.AvailableResets != nil {
		avail := *summary.AvailableResets
		if avail < 0 {
			avail = 0
		}
		return &agent.ResetCredits{AvailableCount: avail}
	}
	if summary.AvailableRstSnk != nil {
		avail := *summary.AvailableRstSnk
		if avail < 0 {
			avail = 0
		}
		return &agent.ResetCredits{AvailableCount: avail}
	}
	return nil
}

func fetchAntigravityModels(ctx context.Context, client *http.Client, accessToken, ua string, reqBody []byte) []agent.UsageLimitWindow {
	endpoints := antigravityEndpoints
	results := make([][]agent.UsageLimitWindow, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(idx int, endpoint string) {
			defer wg.Done()
			endpoint = strings.TrimRight(endpoint, "/")
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1internal:fetchAvailableModels", bytes.NewReader(reqBody))
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("User-Agent", ua)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return
			}
			body, err := readHTTPBodyLimited(resp.Body)
			if err != nil {
				return
			}
			var models antigravityModelsResponse
			if err := json.Unmarshal(body, &models); err != nil {
				return
			}
			results[idx] = parseAntigravityModelWindows(&models)
		}(i, ep)
	}
	wg.Wait()

	merged := make(map[string]agent.UsageLimitWindow)
	for _, list := range results {
		for _, w := range list {
			if prev, ok := merged[w.ID]; !ok || w.UsedPercent > prev.UsedPercent {
				merged[w.ID] = w
			}
		}
	}
	return sortedLimitWindows(merged)
}

func parseAntigravityModelWindows(models *antigravityModelsResponse) []agent.UsageLimitWindow {
	byID := make(map[string]agent.UsageLimitWindow)
	for modelID, m := range models.Models {
		var family string
		switch {
		case m.ModelProvider == "MODEL_PROVIDER_GOOGLE" || strings.HasPrefix(modelID, "gemini-"):
			family = "Gemini"
		case m.ModelProvider == "MODEL_PROVIDER_ANTHROPIC" || strings.HasPrefix(modelID, "claude-"):
			family = "Claude"
		case m.ModelProvider == "MODEL_PROVIDER_OPENAI" || strings.HasPrefix(modelID, "gpt-"):
			family = "GPT"
		default:
			continue
		}
		pairs := []struct {
			suffix string
			quota  *antigravityQuota
		}{
			{suffix: "quota", quota: m.QuotaInfo},
			{suffix: "weekly", quota: m.WeeklyQuotaInfo},
		}
		for _, pair := range pairs {
			if pair.quota == nil || pair.quota.RemainingFraction == nil {
				continue
			}
			if math.IsNaN(*pair.quota.RemainingFraction) || math.IsInf(*pair.quota.RemainingFraction, 0) {
				continue
			}
			id := strings.ToLower(family) + "-" + pair.suffix
			kind := agent.LimitWindowOther
			label := family + " · Reported quota"
			duration := 0
			if pair.suffix == "weekly" {
				kind = agent.LimitWindowWeekly
				label = family + " · Weekly"
				duration = weeklyWindowMins
			}
			w := agent.UsageLimitWindow{
				ID:                 id,
				Kind:               kind,
				Label:              label,
				UsedPercent:        (1 - clampFraction(*pair.quota.RemainingFraction)) * 100,
				ResetsAt:           formatISOTime(pair.quota.ResetTime),
				WindowDurationMins: duration,
			}
			if prev, ok := byID[id]; !ok || w.UsedPercent > prev.UsedPercent {
				byID[id] = w
			}
		}
	}
	return sortedLimitWindows(byID)
}

func sortedLimitWindows(byID map[string]agent.UsageLimitWindow) []agent.UsageLimitWindow {
	out := make([]agent.UsageLimitWindow, 0, len(byID))
	for _, w := range byID {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

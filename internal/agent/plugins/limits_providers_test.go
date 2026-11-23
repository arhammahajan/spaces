package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

func writeLimitTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLimitsRegistryExactSevenProviders(t *testing.T) {
	reg := agent.NewRegistry()
	RegisterAll(reg)
	providers := reg.UsageLimitProviders()

	got := make([]string, 0, len(providers))
	for _, p := range providers {
		got = append(got, p.Spec().ID)
	}
	sort.Strings(got)

	want := []string{
		"antigravity",
		"claude",
		"codex",
		"cursor-agent",
		"grok",
		"muse",
		"opencode",
	}
	if len(got) != len(want) {
		t.Fatalf("UsageLimitProviders() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UsageLimitProviders()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestLimitsEnvIsolation(t *testing.T) {
	t.Setenv("SPACES_TEST_LIMIT_KEY", "host-secret")

	named := agent.UsageLimitQuery{Profile: "work", IsDefault: false}
	if v := limitEnv(named, "SPACES_TEST_LIMIT_KEY"); v != "" {
		t.Fatalf("expected empty env for named profile, got %q", v)
	}

	namedWithOverride := agent.UsageLimitQuery{
		Profile:   "work",
		IsDefault: false,
		Env:       map[string]string{"SPACES_TEST_LIMIT_KEY": "profile-secret"},
	}
	if v := limitEnv(namedWithOverride, "SPACES_TEST_LIMIT_KEY"); v != "profile-secret" {
		t.Fatalf("expected profile-secret, got %q", v)
	}

	def := agent.UsageLimitQuery{Profile: "default", IsDefault: true}
	if v := limitEnv(def, "SPACES_TEST_LIMIT_KEY"); v != "host-secret" {
		t.Fatalf("expected host-secret for default profile, got %q", v)
	}
}

func TestLimitsAntigravityDirectTokenAndSummaryMerge(t *testing.T) {
	ep1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:retrieveUserQuotaSummary" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer direct-token-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "antigravity/hub/1.0.0 ") {
			http.Error(w, "bad ua", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{
					"displayName": "Gemini",
					"buckets": []map[string]any{
						{
							"bucketId":          "gemini-5h",
							"window":            "5h",
							"remainingFraction": 0.70,
							"resetTime":         "2026-12-01T15:00:00Z",
						},
						{
							"bucketId":          "gemini-disabled",
							"window":            "5h",
							"remainingFraction": 0.10,
							"disabled":          true,
						},
					},
				},
			},
			"resetCredits": map[string]any{
				"availableCount": 2,
				"nextExpiresAt":  "2026-12-10T00:00:00Z",
				"nextCreditId":   "ag-credit-1",
			},
		})
	}))
	defer ep1.Close()

	ep2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:retrieveUserQuotaSummary" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"groups": []map[string]any{
				{
					"displayName": "Gemini",
					"buckets": []map[string]any{
						{
							"bucketId":          "gemini-5h",
							"window":            "5h",
							"remainingFraction": 0.40, // 60% used > 30% used from ep1
							"resetTime":         "2026-12-01T15:00:00Z",
						},
					},
				},
				{
					"displayName": "3P",
					"buckets": []map[string]any{
						{
							"bucketId":          "3p-weekly",
							"window":            "weekly",
							"remainingFraction": 0.25,
							"resetTime":         "2026-12-07T00:00:00Z",
						},
					},
				},
			},
		})
	}))
	defer ep2.Close()

	userInfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "google-uid-42",
			"email": "user@example.com",
		})
	}))
	defer userInfoSrv.Close()

	origEndpoints := antigravityEndpoints
	origUserInfo := antigravityUserInfoURL
	antigravityEndpoints = []string{ep1.URL, ep2.URL}
	antigravityUserInfoURL = userInfoSrv.URL
	t.Cleanup(func() {
		antigravityEndpoints = origEndpoints
		antigravityUserInfoURL = origUserInfo
	})

	home := t.TempDir()
	expiry := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	writeLimitTestFile(t, filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"), `{
		"token": { "access_token": "direct-token-123", "expiry": "`+expiry+`" },
		"id_token_email": "ag@example.com"
	}`)

	p := NewAntigravityUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Email != "ag@example.com" {
		t.Fatalf("Email = %q, want ag@example.com", got.Email)
	}
	wantFP := sha256Hex("google-account\x00google-uid-42")
	if got.CredentialFingerprint != wantFP {
		t.Fatalf("CredentialFingerprint = %q, want %q", got.CredentialFingerprint, wantFP)
	}
	if got.ResetCredits == nil || got.ResetCredits.AvailableCount != 2 || got.ResetCredits.NextCreditID != "ag-credit-1" {
		t.Fatalf("ResetCredits = %+v", got.ResetCredits)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2: %+v", len(got.Windows), got.Windows)
	}
	// Sorted by ID: 3p-weekly, gemini-5h
	if got.Windows[0].ID != "3p-weekly" || got.Windows[0].Kind != agent.LimitWindowWeekly || got.Windows[0].Label != "Claude & GPT (shared) · Weekly" || got.Windows[0].UsedPercent != 75 {
		t.Fatalf("window[0] = %+v", got.Windows[0])
	}
	if got.Windows[1].ID != "gemini-5h" || got.Windows[1].Kind != agent.LimitWindowSession || got.Windows[1].Label != "Gemini · 5-hour" || int(got.Windows[1].UsedPercent+0.5) != 60 {
		t.Fatalf("window[1] = %+v", got.Windows[1])
	}
}

func TestLimitsAntigravityACPRefreshAndModelsFallback(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "ref-xyz" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-token-999",
			"expires_in":   3600,
		})
	}))
	defer tokenSrv.Close()

	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer refreshed-token-999" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []any{}})
		case "/v1internal:fetchAvailableModels":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-2.5-pro": map[string]any{
						"modelProvider": "MODEL_PROVIDER_GOOGLE",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.2,
							"resetTime":         "2026-12-01T10:00:00Z",
						},
						"weeklyQuotaInfo": map[string]any{
							"remainingFraction": 0.5,
							"resetTime":         "2026-12-07T00:00:00Z",
						},
					},
					"claude-sonnet-4": map[string]any{
						"modelProvider": "MODEL_PROVIDER_ANTHROPIC",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.9,
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ep.Close()

	userInfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "uid-acp", "email": "acp@example.com"})
	}))
	defer userInfoSrv.Close()

	origEndpoints := antigravityEndpoints
	origTokenURL := antigravityTokenURL
	origUserInfo := antigravityUserInfoURL
	antigravityEndpoints = []string{ep.URL}
	antigravityTokenURL = tokenSrv.URL
	antigravityUserInfoURL = userInfoSrv.URL
	t.Cleanup(func() {
		antigravityEndpoints = origEndpoints
		antigravityTokenURL = origTokenURL
		antigravityUserInfoURL = origUserInfo
	})

	home := t.TempDir()
	writeLimitTestFile(t, filepath.Join(home, "antigravity-acp", "acp_token.json"), `{
		"client_id": "cid",
		"client_secret": "csec",
		"refresh_token": "ref-xyz",
		"project_id": "proj-1"
	}`)

	p := NewAntigravityUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "work",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Email != "acp@example.com" {
		t.Fatalf("Email = %q, want acp@example.com", got.Email)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("len(Windows) = %d, want 3: %+v", len(got.Windows), got.Windows)
	}
	if got.Windows[0].ID != "claude-quota" || got.Windows[1].ID != "gemini-quota" || got.Windows[2].ID != "gemini-weekly" {
		t.Fatalf("unexpected model fallback windows: %+v", got.Windows)
	}
}

func TestLimitsClaudeOAuthAndCedarEmber(t *testing.T) {
	futureEnd := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	pastEnd := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer claude-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			http.Error(w, "missing beta header", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"five_hour": map[string]any{
				"utilization": 42.5,
				"resets_at":   "2026-12-01T14:00:00Z",
			},
			"seven_day": map[string]any{
				"utilization": 65.0,
				"resets_at":   "2026-12-07T00:00:00Z",
			},
			"model_scoped": []map[string]any{
				{
					"display_name": "Sonnet 4.5",
					"utilization":  18.0,
					"resets_at":    "2026-12-07T00:00:00Z",
				},
			},
			"cedar_ember": map[string]any{
				"eligible":      true,
				"next_grant_id": "grant_a",
				"grants": []map[string]any{
					{"id": "grant_a", "resets_left": 2, "ends_at": futureEnd, "paused": false, "usable_now": true},
					{"id": "grant_b", "resets_left": 1, "ends_at": futureEnd, "paused": false, "usable_now": true},
					{"id": "grant_paused", "resets_left": 5, "ends_at": futureEnd, "paused": true, "usable_now": true},
					{"id": "grant_expired", "resets_left": 3, "ends_at": pastEnd, "paused": false, "usable_now": true},
				},
			},
		})
	}))
	defer srv.Close()

	origURL := claudeUsageAPIURL
	claudeUsageAPIURL = srv.URL
	t.Cleanup(func() { claudeUsageAPIURL = origURL })

	home := t.TempDir()
	cfgDir := filepath.Join(home, ".claude")
	writeLimitTestFile(t, filepath.Join(cfgDir, ".credentials.json"), `{
		"claudeAiOauth": { "accessToken": "claude-tok", "subscriptionType": "max" }
	}`)
	writeLimitTestFile(t, filepath.Join(cfgDir, ".claude.json"), `{
		"oauthAccount": { "emailAddress": "claude@example.com", "organizationUuid": "org-123" }
	}`)

	p := NewClaudeUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Email != "claude@example.com" || got.WorkspaceID != "org-123" || got.Plan != "max" {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("len(Windows) = %d, want 3: %+v", len(got.Windows), got.Windows)
	}
	if got.Windows[0].ID != "five_hour" || got.Windows[0].UsedPercent != 42.5 {
		t.Fatalf("window[0] = %+v", got.Windows[0])
	}
	if got.Windows[2].ID != "seven_day_sonnet_4_5" || got.Windows[2].Label != "Weekly · Sonnet 4.5" {
		t.Fatalf("window[2] = %+v", got.Windows[2])
	}
	if got.ResetCredits == nil || got.ResetCredits.AvailableCount != 3 || got.ResetCredits.NextCreditID != "grant_a" || got.ResetCredits.NextExpiresAt == "" {
		t.Fatalf("ResetCredits = %+v", got.ResetCredits)
	}
}

func TestLimitsCodexWhamJWTAndResetCredits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer codex-access" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("ChatGPT-Account-Id") != "acct-777" || r.Header.Get("originator") != "codex_cli_rs" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "pro",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":         35.0,
					"limit_window_seconds": 18000,
					"reset_at":             1796130000,
				},
				"secondary_window": map[string]any{
					"used_percent":         80.0,
					"limit_window_seconds": 604800,
					"reset_at":             1796600000,
				},
			},
			"reset_credits": map[string]any{
				"available_count": 2,
				"credits": []map[string]any{
					{"status": "used", "expires_at": 1796000000},
					{"status": "available", "expires_at": 1797000000},
					{"status": "available", "expires_at": 1796500000},
				},
			},
		})
	}))
	defer srv.Close()

	origURL := codexUsageAPIURL
	codexUsageAPIURL = srv.URL
	t.Cleanup(func() { codexUsageAPIURL = origURL })

	jwtHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	jwtPayload := base64.RawURLEncoding.EncodeToString([]byte(`{
		"email": "codex@example.com",
		"https://api.openai.com/auth": { "chatgpt_plan_type": "plus" }
	}`))
	fakeJWT := jwtHeader + "." + jwtPayload + ".sig"

	home := t.TempDir()
	writeLimitTestFile(t, filepath.Join(home, ".codex", "auth.json"), `{
		"auth_mode": "chatgpt",
		"tokens": {
			"access_token": "codex-access",
			"account_id": "acct-777",
			"id_token": "`+fakeJWT+`"
		}
	}`)

	p := NewCodexUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Email != "codex@example.com" || got.Plan != "pro" || got.WorkspaceID != "acct-777" {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2", len(got.Windows))
	}
	if got.Windows[0].Kind != agent.LimitWindowSession || got.Windows[0].WindowDurationMins != 300 {
		t.Fatalf("window[0] = %+v", got.Windows[0])
	}
	if got.Windows[1].Kind != agent.LimitWindowWeekly || got.Windows[1].WindowDurationMins != 10080 {
		t.Fatalf("window[1] = %+v", got.Windows[1])
	}
	wantExp := formatEpochSeconds(1796500000)
	if got.ResetCredits == nil || got.ResetCredits.AvailableCount != 2 || got.ResetCredits.NextExpiresAt != wantExp {
		t.Fatalf("ResetCredits = %+v, want count=2 exp=%s", got.ResetCredits, wantExp)
	}
}

func TestLimitsOpenCodeUsageAnd403Unsupported(t *testing.T) {
	statusCode := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oc-go-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if statusCode != http.StatusOK {
			w.WriteHeader(statusCode)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]any{
				"rolling": map[string]any{"percent": 20, "resetsAt": "2026-12-01T12:00:00Z"},
				"weekly":  map[string]any{"percent": 45, "resetsAt": "2026-12-07T00:00:00Z"},
				"monthly": map[string]any{"percent": 60, "resetsAt": "2027-01-01T00:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	origURL := openCodeUsageAPIURL
	openCodeUsageAPIURL = srv.URL
	t.Cleanup(func() { openCodeUsageAPIURL = origURL })

	home := t.TempDir()
	writeLimitTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{
		"opencode-go": { "type": "api", "key": "oc-go-key" }
	}`)

	p := NewOpenCodeUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil || got.Plan != "Go" || len(got.Windows) != 3 {
		t.Fatalf("unexpected opencode result: %+v", got)
	}
	if got.Windows[0].ID != "go_rolling" || got.Windows[1].ID != "go_weekly" || got.Windows[2].ID != "go_monthly" {
		t.Fatalf("unexpected opencode windows: %+v", got.Windows)
	}
	if got.CredentialFingerprint != sha256Hex("opencode-go\x00oc-go-key") {
		t.Fatalf("unexpected fingerprint: %q", got.CredentialFingerprint)
	}

	statusCode = http.StatusForbidden
	unavail, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits 403 error: %v", err)
	}
	if unavail.Unavailable == nil || unavail.Unavailable.Reason != agent.LimitUnavailableUnsupported {
		t.Fatalf("expected unsupported on 403, got: %+v", unavail.Unavailable)
	}
}

func TestLimitsCursorUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer cursor-tok" ||
			r.Header.Get("connect-protocol-version") != "1" ||
			r.Header.Get("x-cursor-client-type") != "cli" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"billingCycleEnd": 1798761600000,
			"planUsage": map[string]any{
				"totalPercentUsed": 52.5,
				"autoPercentUsed":  40.0,
				"apiPercentUsed":   70.0,
			},
		})
	}))
	defer srv.Close()

	home := t.TempDir()
	writeLimitTestFile(t, filepath.Join(home, ".config", "cursor", "auth.json"), `{
		"accessToken": "cursor-tok",
		"email": "cursor@example.com"
	}`)

	p := NewCursorUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
		Env:     map[string]string{"CURSOR_API_ENDPOINT": srv.URL},
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Email != "cursor@example.com" || len(got.Windows) != 3 {
		t.Fatalf("unexpected cursor result: %+v", got)
	}
	if got.Windows[0].ID != "totalPercentUsed" || got.Windows[0].Label != "Overall" ||
		got.Windows[1].ID != "autoPercentUsed" || got.Windows[1].Label != "Cursor Models" ||
		got.Windows[2].ID != "apiPercentUsed" || got.Windows[2].Label != "Other Models" {
		t.Fatalf("unexpected cursor windows: %+v", got.Windows)
	}
	if got.Windows[0].ResetsAt == "" {
		t.Fatalf("expected non-empty ResetsAt from billingCycleEnd")
	}
}

func TestLimitsGrokBillingAndXAIKeyUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer grok-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{
				"creditUsagePercent": 33.3,
				"currentPeriod": map[string]any{
					"type": "USAGE_PERIOD_TYPE_WEEKLY",
					"end":  "2026-12-08T00:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	origURL := grokBillingAPIURL
	grokBillingAPIURL = srv.URL
	t.Cleanup(func() { grokBillingAPIURL = origURL })

	home := t.TempDir()
	writeLimitTestFile(t, filepath.Join(home, ".grok", "auth.json"), `{
		"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
			"key": "grok-tok",
			"auth_mode": "oauth",
			"email": "grok@example.com"
		}
	}`)

	p := NewGrokUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil || got.Email != "grok@example.com" || len(got.Windows) != 1 {
		t.Fatalf("unexpected grok result: %+v", got)
	}
	if got.Windows[0].ID != "subscription" || got.Windows[0].Kind != agent.LimitWindowWeekly || got.Windows[0].Label != "Weekly" {
		t.Fatalf("unexpected grok window: %+v", got.Windows[0])
	}

	unavail, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
		Env:     map[string]string{"XAI_API_KEY": "xai-key"},
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if unavail.Unavailable == nil || unavail.Unavailable.Reason != agent.LimitUnavailableUnsupported {
		t.Fatalf("expected unsupported with XAI_API_KEY, got %+v", unavail.Unavailable)
	}
}

func TestLimitsMuseLocalState(t *testing.T) {
	home := t.TempDir()
	sessionLog := filepath.Join(home, ".local", "share", "muse", "sessions", "s1", "session.jsonl")
	writeLimitTestFile(t, sessionLog, strings.Join([]string{
		`{"method":"usage/changed","params":{"observedAtMs":1795000000000,"tier":"pro","window":{"usedPercent":10,"windowDurationMins":300,"resetsAtMs":1795018000000},"weekly":{"usedPercent":20,"resetsAtMs":1795600000000}}}`,
		`{"method":"usage/changed","params":{"observedAtMs":1795005000000,"tier":"max","window":{"usedPercent":55,"windowDurationMins":300,"resetsAtMs":1795023000000},"weekly":{"usedPercent":72,"resetsAtMs":1795600000000}}}`,
	}, "\n")+"\n")

	p := NewMuseUsageLimitProvider()
	got, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: home,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits error: %v", err)
	}
	if got.Unavailable != nil {
		t.Fatalf("unexpected unavailable: %+v", got.Unavailable)
	}
	if got.Plan != "max" || got.Source != "local-state" || len(got.Windows) != 2 {
		t.Fatalf("unexpected muse result: %+v", got)
	}
	if got.Windows[0].ID != "window" || got.Windows[0].Kind != agent.LimitWindowSession || got.Windows[0].UsedPercent != 55 || got.Windows[0].WindowDurationMins != 300 {
		t.Fatalf("muse window[0] = %+v", got.Windows[0])
	}
	if got.Windows[1].ID != "weekly" || got.Windows[1].Kind != agent.LimitWindowWeekly || got.Windows[1].UsedPercent != 72 {
		t.Fatalf("muse window[1] = %+v", got.Windows[1])
	}

	emptyHome := t.TempDir()
	unavail, err := p.FetchUsageLimits(context.Background(), agent.UsageLimitQuery{
		Profile: "default",
		HomeDir: emptyHome,
	})
	if err != nil {
		t.Fatalf("FetchUsageLimits empty error: %v", err)
	}
	if unavail.Unavailable == nil || unavail.Unavailable.Reason != agent.LimitUnavailableUnsupported {
		t.Fatalf("expected unsupported for empty muse home, got %+v", unavail.Unavailable)
	}
}

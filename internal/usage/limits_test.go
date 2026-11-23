package usage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

type fakeUsageLimitProvider struct {
	fetchFn func(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error)
}

func (f *fakeUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	return f.fetchFn(ctx, q)
}

func TestLimitClampPercent(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-10, 0},
		{0, 0},
		{42.5, 42.5},
		{100, 100},
		{145.2, 100},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{math.Inf(-1), 0},
	}
	for _, tc := range cases {
		if got := ClampPercent(tc.in); got != tc.want {
			t.Errorf("ClampPercent(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLimitNormalizeWindows(t *testing.T) {
	t.Run("dedup_clamp_timestamp_and_default_sort", func(t *testing.T) {
		in := []agent.UsageLimitWindow{
			{ID: "monthly_cap", Kind: agent.LimitWindowMonthly, Label: "Monthly", UsedPercent: 120, ResetsAt: "not-a-timestamp"},
			{ID: "seven_day", Kind: agent.LimitWindowWeekly, Label: "Weekly", UsedPercent: 30, ResetsAt: "2026-04-20T00:00:00Z"},
			{ID: "seven_day", Kind: agent.LimitWindowWeekly, Label: "Weekly", UsedPercent: 55.5, ResetsAt: "2026-04-20T00:00:00Z"},
			{ID: "five_hour", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: -5, ResetsAt: "2026-04-15T19:00:00.500Z"},
			{ID: "extra", Kind: "unknown_kind", Label: "Extra", UsedPercent: math.NaN()},
		}
		got := NormalizeWindows("claude", in)
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4: %+v", len(got), got)
		}
		wantIDs := []string{"five_hour", "seven_day", "monthly_cap", "extra"}
		for i, id := range wantIDs {
			if got[i].ID != id {
				t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, id)
			}
		}
		if got[0].UsedPercent != 0 || got[0].ResetsAt != "2026-04-15T19:00:00.500Z" {
			t.Errorf("five_hour unexpected: %+v", got[0])
		}
		if got[1].UsedPercent != 55.5 {
			t.Errorf("seven_day UsedPercent = %v, want 55.5 (higher duplicate)", got[1].UsedPercent)
		}
		if got[2].UsedPercent != 100 || got[2].ResetsAt != "" {
			t.Errorf("monthly_cap should clamp to 100 and drop invalid ResetsAt: %+v", got[2])
		}
		if got[3].Kind != agent.LimitWindowOther || got[3].UsedPercent != 0 {
			t.Errorf("extra unexpected: %+v", got[3])
		}
	})

	t.Run("antigravity_family_ordering", func(t *testing.T) {
		in := []agent.UsageLimitWindow{
			{ID: "claude-weekly", Kind: agent.LimitWindowWeekly, UsedPercent: 40},
			{ID: "claude-5h", Kind: agent.LimitWindowSession, UsedPercent: 20},
			{ID: "gemini-pro-weekly", Kind: agent.LimitWindowWeekly, UsedPercent: 50},
			{ID: "gemini-flash-5h", Kind: agent.LimitWindowSession, UsedPercent: 10},
			{ID: "gemini-pro-5h", Kind: agent.LimitWindowSession, UsedPercent: 30},
		}
		got := NormalizeWindows("antigravity", in)
		wantIDs := []string{
			"gemini-flash-5h",
			"gemini-pro-5h",
			"gemini-pro-weekly",
			"claude-5h",
			"claude-weekly",
		}
		for i, id := range wantIDs {
			if got[i].ID != id {
				t.Errorf("antigravity[%d] = %q, want %q (all=%+v)", i, got[i].ID, id, got)
			}
		}
	})

	t.Run("cursor_agent_hides_total_when_auto_and_api_present", func(t *testing.T) {
		in := []agent.UsageLimitWindow{
			{ID: "apiPercentUsed", Kind: agent.LimitWindowMonthly, UsedPercent: 45},
			{ID: "totalPercentUsed", Kind: agent.LimitWindowMonthly, UsedPercent: 60},
			{ID: "autoPercentUsed", Kind: agent.LimitWindowMonthly, UsedPercent: 25},
		}
		got := NormalizeWindows("cursor-agent", in)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2: %+v", len(got), got)
		}
		if got[0].ID != "autoPercentUsed" || got[1].ID != "apiPercentUsed" {
			t.Errorf("unexpected cursor order: %+v", got)
		}

		// When only one pool is present, totalPercentUsed stays and sorts first.
		partial := NormalizeWindows("cursor-agent", []agent.UsageLimitWindow{
			{ID: "apiPercentUsed", Kind: agent.LimitWindowMonthly, UsedPercent: 45},
			{ID: "totalPercentUsed", Kind: agent.LimitWindowMonthly, UsedPercent: 60},
		})
		if len(partial) != 2 || partial[0].ID != "totalPercentUsed" || partial[1].ID != "apiPercentUsed" {
			t.Errorf("unexpected partial cursor windows: %+v", partial)
		}
	})
}

func TestLimitResolveIdentityKeys(t *testing.T) {
	raws := []*agent.ProviderUsageLimits{
		// 0: email + workspace (single workspace for alice -> entry 1 should inherit ws-1)
		{Provider: "claude", Profile: "default", Email: " Alice@Example.COM ", WorkspaceID: "ws-1"},
		// 1: same email without workspace -> inherits ws-1
		{Provider: "claude", Profile: "work", Email: "alice@example.com"},
		// 2 & 3: bob has two distinct workspaces
		{Provider: "codex", Profile: "p1", Email: "bob@example.com", WorkspaceID: "ws-a"},
		{Provider: "codex", Profile: "p2", Email: "bob@example.com", WorkspaceID: "ws-b"},
		// 4: bob without workspace -> ambiguous (2 workspaces), falls back to baseKey
		{Provider: "codex", Profile: "p3", Email: "bob@example.com"},
		// 5: credential fingerprint fallback
		{Provider: "grok", Profile: "default", CredentialFingerprint: "sha256:abcd"},
		// 6: profile fallback
		{Provider: "muse", Profile: "custom"},
	}

	keys := ResolveIdentityKeys(raws)
	want := map[int]string{
		0: "claude:alice@example.com:ws-1",
		1: "claude:alice@example.com:ws-1",
		2: "codex:bob@example.com:ws-a",
		3: "codex:bob@example.com:ws-b",
		4: "codex:bob@example.com",
		5: "grok:credential:sha256:abcd",
		6: "muse:profile:custom",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("ResolveIdentityKeys:\ngot:  %#v\nwant: %#v", keys, want)
	}
}

func TestLimitNormalizeAndMergeLimits(t *testing.T) {
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	previous := &LimitsSnapshot{
		Version:     LimitsCacheVersion,
		CheckedAtMs: now.Add(-10 * time.Minute).UnixMilli(),
		Accounts: []LimitAccount{
			{
				IdentityKey: "grok:user@example.com",
				Provider:    "grok",
				Profiles:    []string{"default"},
				DisplayName: "Grok Pro",
				Email:       "user@example.com",
				CheckedAt:   now.Add(-10 * time.Minute).Format(time.RFC3339),
				Windows: []agent.UsageLimitWindow{
					{ID: "session", Kind: agent.LimitWindowSession, Label: "Session", UsedPercent: 35},
				},
			},
		},
	}

	raws := []*agent.ProviderUsageLimits{
		// Two claude profiles sharing the same identity key:
		// "work" has older windows + reset credits; "default" has fresher windows but no credits.
		{
			Provider:    "claude",
			Profile:     "work",
			DisplayName: "Alice Work",
			Email:       "alice@example.com",
			Plan:        "Max",
			WorkspaceID: "ws-1",
			CheckedAt:   now.Add(-2 * time.Minute).Format(time.RFC3339),
			Windows: []agent.UsageLimitWindow{
				{ID: "five_hour", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: 20},
			},
			ResetCredits: &agent.ResetCredits{AvailableCount: 2, NextCreditID: "cred-1"},
		},
		{
			Provider:  "claude",
			Profile:   "default",
			Email:     "ALICE@example.com",
			CheckedAt: now.Add(-30 * time.Second).Format(time.RFC3339),
			Windows: []agent.UsageLimitWindow{
				{ID: "five_hour", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: 45},
				{ID: "seven_day", Kind: agent.LimitWindowWeekly, Label: "Weekly", UsedPercent: 15},
			},
		},
		// Unsupported account -> silently skipped (no account, no notice).
		{
			Provider: "opencode",
			Profile:  "default",
			Unavailable: &agent.UsageLimitUnavailable{
				Reason:  agent.LimitUnavailableUnsupported,
				Message: "API key auth has no subscription limits",
			},
		},
		// Transient probe failure for grok -> notice + preserved previous bars marked Stale.
		{
			Provider: "grok",
			Profile:  "default",
			Email:    "user@example.com",
			Unavailable: &agent.UsageLimitUnavailable{
				Reason:  agent.LimitUnavailableProbeFailed,
				Message: "upstream 503",
			},
		},
		// Codex with exhausted weekly window -> session window suppressed to 100% and aligned to weekly reset.
		{
			Provider:  "codex",
			Profile:   "default",
			Email:     "dev@example.com",
			CheckedAt: now.Format(time.RFC3339),
			Windows: []agent.UsageLimitWindow{
				{
					ID:                 "primary",
					Kind:               agent.LimitWindowSession,
					Label:              "5h",
					UsedPercent:        25,
					ResetsAt:           now.Add(2 * time.Hour).Format(time.RFC3339),
					WindowDurationMins: 300,
				},
				{
					ID:                 "secondary",
					Kind:               agent.LimitWindowWeekly,
					Label:              "Weekly",
					UsedPercent:        100,
					ResetsAt:           now.Add(48 * time.Hour).Format(time.RFC3339),
					WindowDurationMins: 7 * 24 * 60,
				},
			},
		},
	}

	snap := NormalizeAndMergeLimits(raws, previous, now)

	if snap.Version != LimitsCacheVersion {
		t.Errorf("Version = %d, want %d", snap.Version, LimitsCacheVersion)
	}
	// Expect 3 accounts ordered by SupportedLimitProviders: claude, codex, grok.
	if len(snap.Accounts) != 3 {
		t.Fatalf("len(Accounts) = %d, want 3: %+v", len(snap.Accounts), snap.Accounts)
	}
	if snap.Accounts[0].Provider != "claude" || snap.Accounts[1].Provider != "codex" || snap.Accounts[2].Provider != "grok" {
		t.Fatalf("unexpected provider order: %s, %s, %s", snap.Accounts[0].Provider, snap.Accounts[1].Provider, snap.Accounts[2].Provider)
	}

	// Verify merged claude account.
	cl := snap.Accounts[0]
	if !reflect.DeepEqual(cl.Profiles, []string{"default", "work"}) {
		t.Errorf("claude Profiles = %v, want [default work]", cl.Profiles)
	}
	if cl.DisplayName != "Alice Work" || cl.Plan != "Max" || cl.WorkspaceID != "ws-1" {
		t.Errorf("claude metadata not merged: %+v", cl)
	}
	if len(cl.Windows) != 2 || cl.Windows[0].UsedPercent != 45 {
		t.Errorf("claude fresher windows not kept: %+v", cl.Windows)
	}
	if cl.ResetCredits == nil || cl.ResetCredits.AvailableCount != 2 || cl.ResetCredits.NextCreditID != "cred-1" {
		t.Errorf("claude reset credits not preserved: %+v", cl.ResetCredits)
	}

	// Verify codex weekly-exhausted session suppression.
	cx := snap.Accounts[1]
	if len(cx.Windows) != 2 {
		t.Fatalf("codex windows = %+v", cx.Windows)
	}
	if cx.Windows[0].Kind != agent.LimitWindowSession || cx.Windows[0].UsedPercent != 100 {
		t.Errorf("codex session not suppressed to 100%%: %+v", cx.Windows[0])
	}
	wantWeeklyReset := now.Add(48 * time.Hour).Format(time.RFC3339)
	if cx.Windows[0].ResetsAt != wantWeeklyReset {
		t.Errorf("codex session ResetsAt = %q, want %q", cx.Windows[0].ResetsAt, wantWeeklyReset)
	}
	if cx.Windows[0].WindowDurationMins != 0 {
		t.Errorf("codex suppressed session WindowDurationMins = %d, want 0", cx.Windows[0].WindowDurationMins)
	}

	// Verify grok stale fallback & notice (and opencode unsupported silently skipped).
	gk := snap.Accounts[2]
	if !gk.Stale {
		t.Errorf("grok account should be marked Stale=true: %+v", gk)
	}
	if len(gk.Windows) != 1 || gk.Windows[0].UsedPercent != 35 {
		t.Errorf("grok last-good windows not preserved: %+v", gk.Windows)
	}
	if len(snap.Notices) != 1 {
		t.Fatalf("Notices = %+v, want 1 (grok only)", snap.Notices)
	}
	if snap.Notices[0].Provider != "grok" || snap.Notices[0].Message != "upstream 503" {
		t.Errorf("unexpected notice: %+v", snap.Notices[0])
	}
}

func TestLimitClaudeWeeklySuppression(t *testing.T) {
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	weeklyReset := now.Add(72 * time.Hour).Format(time.RFC3339)
	snap := NormalizeAndMergeLimits([]*agent.ProviderUsageLimits{
		{
			Provider:  "claude",
			Profile:   "default",
			CheckedAt: now.Format(time.RFC3339),
			Windows: []agent.UsageLimitWindow{
				{ID: "five_hour", Kind: agent.LimitWindowSession, UsedPercent: 10, ResetsAt: now.Add(3 * time.Hour).Format(time.RFC3339), WindowDurationMins: 300},
				{ID: "seven_day", Kind: agent.LimitWindowWeekly, UsedPercent: 100, ResetsAt: weeklyReset, WindowDurationMins: 7 * 24 * 60},
			},
		},
	}, nil, now)

	if len(snap.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(snap.Accounts))
	}
	session := snap.Accounts[0].Windows[0]
	if session.UsedPercent != 100 || session.ResetsAt != weeklyReset || session.WindowDurationMins != 0 {
		t.Errorf("claude session suppression failed: %+v", session)
	}
}

func TestLimitPaceAndFormatResetsIn(t *testing.T) {
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	// 100-minute window, resets in 50 minutes => 50% elapsed, 50% remaining.
	base := agent.UsageLimitWindow{
		ID:                 "five_hour",
		Kind:               agent.LimitWindowSession,
		ResetsAt:           now.Add(50 * time.Minute).Format(time.RFC3339),
		WindowDurationMins: 100,
	}

	if got := RemainingPercent(agent.UsageLimitWindow{UsedPercent: 34.4}); got != 66 {
		t.Errorf("RemainingPercent(34.4) = %d, want 66", got)
	}

	share, ok := ElapsedShare(base, now)
	if !ok || math.Abs(share-0.5) > 1e-6 {
		t.Errorf("ElapsedShare = (%v, %v), want (0.5, true)", share, ok)
	}
	paceRem, ok := PaceRemainingPercent(base, now)
	if !ok || paceRem != 50 {
		t.Errorf("PaceRemainingPercent = (%d, %v), want (50, true)", paceRem, ok)
	}

	// Pace thresholds: gap = used - elapsed*100 (50).
	aheadW := base
	aheadW.UsedPercent = 60 // gap = +10 > 5
	if p, ok := ComputeWindowPace(aheadW, now); !ok || p != LimitPaceAhead {
		t.Errorf("ahead pace = (%q, %v), want (%q, true)", p, ok, LimitPaceAhead)
	}

	onW := base
	onW.UsedPercent = 54 // gap = +4 in [-5, 5]
	if p, ok := ComputeWindowPace(onW, now); !ok || p != LimitPaceOn {
		t.Errorf("on pace = (%q, %v), want (%q, true)", p, ok, LimitPaceOn)
	}

	underW := base
	underW.UsedPercent = 40 // gap = -10 < -5
	if p, ok := ComputeWindowPace(underW, now); !ok || p != LimitPaceUnder {
		t.Errorf("under pace = (%q, %v), want (%q, true)", p, ok, LimitPaceUnder)
	}

	if _, ok := ComputeWindowPace(agent.UsageLimitWindow{UsedPercent: 50}, now); ok {
		t.Errorf("expected ok=false without ResetsAt/WindowDurationMins")
	}

	// FormatResetsIn cases.
	formatCases := []struct {
		resetsAt string
		want     string
	}{
		{"", ""},
		{"invalid", ""},
		{now.Add(-5 * time.Minute).Format(time.RFC3339), "resets now"},
		{now.Format(time.RFC3339), "resets now"},
		{now.Add(30 * time.Second).Format(time.RFC3339), "resets <1m"},
		{now.Add(42 * time.Minute).Format(time.RFC3339), "resets in 42m"},
		{now.Add(3*time.Hour + 15*time.Minute).Format(time.RFC3339), "resets in 3h 15m"},
		{now.Add(2*24*time.Hour + 5*time.Hour + 20*time.Minute).Format(time.RFC3339), "resets in 2d 5h"},
	}
	for _, tc := range formatCases {
		got := FormatResetsIn(agent.UsageLimitWindow{ResetsAt: tc.resetsAt}, now)
		if got != tc.want {
			t.Errorf("FormatResetsIn(%q) = %q, want %q", tc.resetsAt, got, tc.want)
		}
	}
}

func TestLimitCacheRoundTrip(t *testing.T) {
	home := t.TempDir()

	// Unwraps shadow HOME back to realHome/.harness/usage-limits-cache.json.
	t.Setenv(limitsCacheEnvVar, "")
	shadowHome := filepath.Join(home, ".harness", "profiles", "claude", "work", "home")
	gotPath := DefaultLimitsCachePath(shadowHome)
	wantPath := filepath.Join(home, ".harness", "usage-limits-cache.json")
	if gotPath != wantPath {
		t.Errorf("DefaultLimitsCachePath(shadow) = %q, want %q", gotPath, wantPath)
	}

	// Env override wins.
	overridePath := filepath.Join(home, "custom-limits.json")
	t.Setenv(limitsCacheEnvVar, overridePath)
	if got := DefaultLimitsCachePath(home); got != overridePath {
		t.Errorf("DefaultLimitsCachePath with env = %q, want %q", got, overridePath)
	}

	snap := LimitsSnapshot{
		Version:     LimitsCacheVersion,
		CheckedAtMs: 1776261600000,
		Accounts: []LimitAccount{
			{
				IdentityKey: "claude:alice@example.com",
				Provider:    "claude",
				Profiles:    []string{"default"},
				CheckedAt:   "2026-04-15T14:00:00Z",
				Windows: []agent.UsageLimitWindow{
					{ID: "five_hour", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: 42},
				},
			},
		},
	}

	if err := SaveLimitsCache(wantPath, snap); err != nil {
		t.Fatalf("SaveLimitsCache: %v", err)
	}

	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("Stat cache file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file perms = %o, want 0600", perm)
	}

	loaded, ok := LoadLimitsCache(wantPath)
	if !ok {
		t.Fatalf("LoadLimitsCache returned ok=false")
	}
	if !reflect.DeepEqual(loaded, snap) {
		t.Errorf("loaded snapshot mismatch:\ngot:  %+v\nwant: %+v", loaded, snap)
	}

	// Wrong version is rejected.
	badPath := filepath.Join(home, "bad-version.json")
	if err := os.WriteFile(badPath, []byte(`{"version":99,"checked_at_ms":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadLimitsCache(badPath); ok {
		t.Errorf("expected ok=false for unsupported cache version")
	}
}

func TestLimitCollectorFanOutCachingAndCoalescing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(limitsCacheEnvVar, "")

	// 1. Customized default profile for claude.
	writeProfileJSON(t, filepath.Join(home, ".harness", "profiles", "claude", "default", "profile.json"), map[string]any{
		"agent":       "claude",
		"displayName": "Personal Claude",
		"enabled":     true,
		"binaryPath":  "~/bin/claude-custom",
		"env":         map[string]string{"CLAUDE_ENV": "default-val"},
	})
	// 2. Enabled named profile "work" for claude with custom shadow home.
	workShadow := filepath.Join(home, "custom-work-home")
	if err := os.MkdirAll(workShadow, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfileJSON(t, filepath.Join(home, ".harness", "profiles", "claude", "work", "profile.json"), map[string]any{
		"agent":          "claude",
		"displayName":    "Work Claude",
		"enabled":        true,
		"shadowHomePath": workShadow,
		"binaryPath":     "/usr/local/bin/claude-work",
		"env":            map[string]string{"CLAUDE_ENV": "work-val"},
	})
	// 3. Disabled named profile "disabled" for claude (must NOT be probed).
	disabledHome := filepath.Join(home, ".harness", "profiles", "claude", "disabled", "home")
	if err := os.MkdirAll(disabledHome, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfileJSON(t, filepath.Join(home, ".harness", "profiles", "claude", "disabled", "profile.json"), map[string]any{
		"agent":          "claude",
		"displayName":    "Disabled Claude",
		"enabled":        false,
		"shadowHomePath": disabledHome,
	})
	// 4. Legacy directory profile "legacy" for claude (no profile.json).
	legacyHome := filepath.Join(home, ".harness", "profiles", "claude", "legacy", "home")
	if err := os.MkdirAll(legacyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	var (
		mu          sync.Mutex
		seenQueries = map[string]agent.UsageLimitQuery{}
		probeCalls  atomic.Int32
		failCodex   atomic.Bool
		gate        chan struct{}
	)

	claudePlugin := &agent.BasePlugin{
		PluginSpec: agent.Spec{ID: "claude"},
		CustomUsageLimitProvider: &fakeUsageLimitProvider{
			fetchFn: func(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
				probeCalls.Add(1)
				if gate != nil {
					<-gate
				}
				mu.Lock()
				seenQueries["claude/"+q.Profile] = q
				mu.Unlock()
				return &agent.ProviderUsageLimits{
					Provider:    "claude",
					Profile:     q.Profile,
					DisplayName: q.DisplayName,
					Email:       q.Profile + "@example.com",
					CheckedAt:   now.Format(time.RFC3339),
					Windows: []agent.UsageLimitWindow{
						{ID: "five_hour", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: 30},
					},
				}, nil
			},
		},
	}

	codexPlugin := &agent.BasePlugin{
		PluginSpec: agent.Spec{ID: "codex"},
		CustomUsageLimitProvider: &fakeUsageLimitProvider{
			fetchFn: func(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
				probeCalls.Add(1)
				if failCodex.Load() {
					return nil, errors.New("codex timeout")
				}
				mu.Lock()
				seenQueries["codex/"+q.Profile] = q
				mu.Unlock()
				return &agent.ProviderUsageLimits{
					Provider:  "codex",
					Profile:   q.Profile,
					Email:     "codex@example.com",
					CheckedAt: now.Format(time.RFC3339),
					Windows: []agent.UsageLimitWindow{
						{ID: "primary", Kind: agent.LimitWindowSession, Label: "5h", UsedPercent: 50},
					},
				}, nil
			},
		},
	}

	reg := agent.NewRegistry(claudePlugin, codexPlugin)
	col := NewLimitsCollectorWithOptions(LimitsCollectOptions{
		HomeDir:  home,
		Registry: reg,
		Now:      func() time.Time { return now },
	})

	// First Collect(ctx, false) with no cache yet -> probes live and writes ~/.harness/usage-limits-cache.json.
	snap, err := col.Collect(context.Background(), false)
	if err != nil {
		t.Fatalf("initial Collect failed: %v", err)
	}
	if len(snap.Accounts) != 4 {
		t.Fatalf("expected 4 accounts (claude default/work/legacy + codex default), got %d: %+v", len(snap.Accounts), snap.Accounts)
	}

	// Verify profile query details and that disabled profile was skipped.
	if _, found := seenQueries["claude/disabled"]; found {
		t.Errorf("disabled profile should not have been probed")
	}
	if q := seenQueries["claude/default"]; !q.IsDefault || q.HomeDir != home || q.DisplayName != "Personal Claude" || q.Env["CLAUDE_ENV"] != "default-val" || q.BinaryPath != filepath.Join(home, "bin", "claude-custom") {
		t.Errorf("unexpected claude/default query: %+v", q)
	}
	if q := seenQueries["claude/work"]; q.IsDefault || q.HomeDir != workShadow || q.DisplayName != "Work Claude" || q.Env["CLAUDE_ENV"] != "work-val" || q.BinaryPath != "/usr/local/bin/claude-work" {
		t.Errorf("unexpected claude/work query: %+v", q)
	}
	if q := seenQueries["claude/legacy"]; q.IsDefault || q.HomeDir != legacyHome {
		t.Errorf("unexpected claude/legacy query: %+v", q)
	}

	// Verify cache file was written to <home>/.harness/usage-limits-cache.json.
	cachePath := filepath.Join(home, ".harness", "usage-limits-cache.json")
	if _, ok := LoadLimitsCache(cachePath); !ok {
		t.Fatalf("expected valid cache file at %s", cachePath)
	}

	// Brand-new collector with !force serves immediately from disk cache without probing.
	callsBefore := probeCalls.Load()
	freshCol := NewLimitsCollectorWithOptions(LimitsCollectOptions{
		HomeDir:  home,
		Registry: reg,
		Now:      func() time.Time { return now },
	})
	cachedSnap, err := freshCol.Collect(context.Background(), false)
	if err != nil {
		t.Fatalf("cached Collect failed: %v", err)
	}
	if probeCalls.Load() != callsBefore {
		t.Errorf("expected 0 live probes on !force cache hit, got %d new probes", probeCalls.Load()-callsBefore)
	}
	if len(cachedSnap.Accounts) != 4 {
		t.Errorf("cached snapshot accounts = %d, want 4", len(cachedSnap.Accounts))
	}

	// Concurrent force calls coalesce onto a single live probe run.
	gate = make(chan struct{})
	beforeCoalesce := probeCalls.Load()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := freshCol.Collect(context.Background(), true); err != nil {
				t.Errorf("coalesced Collect error: %v", err)
			}
		}()
	}
	time.Sleep(25 * time.Millisecond)
	close(gate)
	wg.Wait()

	// 4 targets probed once across 5 concurrent callers.
	if delta := probeCalls.Load() - beforeCoalesce; delta != 4 {
		t.Errorf("expected 4 coalesced probe calls (1 run), got %d", delta)
	}

	// Subsequent force probe with codex failing preserves codex last-good bars as Stale=true.
	gate = nil
	failCodex.Store(true)
	staleSnap, err := freshCol.Collect(context.Background(), true)
	if err != nil {
		t.Fatalf("stale-fallback Collect failed: %v", err)
	}
	var foundNotice bool
	for _, n := range staleSnap.Notices {
		if n.Provider == "codex" && n.Message == "codex timeout" {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Errorf("expected codex timeout notice, got %+v", staleSnap.Notices)
	}
	var codexAcc *LimitAccount
	for i := range staleSnap.Accounts {
		if staleSnap.Accounts[i].Provider == "codex" {
			codexAcc = &staleSnap.Accounts[i]
		}
	}
	if codexAcc == nil || !codexAcc.Stale || len(codexAcc.Windows) != 1 || codexAcc.Windows[0].UsedPercent != 50 {
		t.Errorf("expected preserved stale codex account with 50%% used, got %+v", codexAcc)
	}
}

func writeProfileJSON(t *testing.T, path string, data map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

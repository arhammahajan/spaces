package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

const (
	defaultLimitHTTPTimeout = 10 * time.Second
	maxLimitResponseBytes   = 2 * 1024 * 1024 // 2 MiB
	sessionWindowMins       = 5 * 60
	weeklyWindowMins        = 7 * 24 * 60
	monthlyWindowMins       = 30 * 24 * 60
)

// AttachProviderUsageLimits attaches native agent.UsageLimitProvider
// implementations onto bp for the 7 supported providers when
// CustomUsageLimitProvider is nil.
func AttachProviderUsageLimits(bp *agent.BasePlugin) {
	if bp == nil || bp.CustomUsageLimitProvider != nil {
		return
	}
	switch bp.PluginSpec.ID {
	case "claude":
		bp.CustomUsageLimitProvider = NewClaudeUsageLimitProvider()
	case "codex":
		bp.CustomUsageLimitProvider = NewCodexUsageLimitProvider()
	case "opencode":
		bp.CustomUsageLimitProvider = NewOpenCodeUsageLimitProvider()
	case "cursor-agent":
		bp.CustomUsageLimitProvider = NewCursorUsageLimitProvider()
	case "grok":
		bp.CustomUsageLimitProvider = NewGrokUsageLimitProvider()
	case "antigravity":
		bp.CustomUsageLimitProvider = NewAntigravityUsageLimitProvider()
	case "muse":
		bp.CustomUsageLimitProvider = NewMuseUsageLimitProvider()
	}
}

// limitEnv returns the value of key from q.Env when present, falling back to
// os.Getenv(key) ONLY when q.IsDefault is true so named shadow profiles remain
// strictly isolated from host environment variables.
func limitEnv(q agent.UsageLimitQuery, key string) string {
	if q.Env != nil {
		if val, ok := q.Env[key]; ok {
			return strings.TrimSpace(val)
		}
	}
	if q.IsDefault {
		return strings.TrimSpace(os.Getenv(key))
	}
	return ""
}

// limitHomeDir resolves the profile's home directory without leaking the host
// home into named shadow profiles.
func limitHomeDir(q agent.UsageLimitQuery) string {
	if h := strings.TrimSpace(q.HomeDir); h != "" {
		return h
	}
	if q.IsDefault {
		if h := strings.TrimSpace(q.RealHomeDir); h != "" {
			return h
		}
		if h, err := os.UserHomeDir(); err == nil {
			return strings.TrimSpace(h)
		}
	}
	return ""
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newLimitHTTPClient returns an HTTP client configured with a bounded timeout
// and automatic redirects disabled so bearer tokens are never forwarded across
// redirects.
func newLimitHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultLimitHTTPTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func unavailableLimits(provider, profile, reason, msg string, now time.Time) *agent.ProviderUsageLimits {
	return &agent.ProviderUsageLimits{
		Provider:  provider,
		Profile:   profile,
		CheckedAt: now.UTC().Format(time.RFC3339),
		Windows:   []agent.UsageLimitWindow{},
		Unavailable: &agent.UsageLimitUnavailable{
			Reason:  reason,
			Message: msg,
		},
	}
}

func clampPercent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func clampFraction(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func parseISOTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func formatISOTime(raw string) string {
	t, ok := parseISOTime(raw)
	if !ok {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatEpochSeconds(sec float64) string {
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 {
		return ""
	}
	return time.Unix(0, int64(sec*float64(time.Second))).UTC().Format(time.RFC3339)
}

func formatEpochMillis(ms float64) string {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 {
		return ""
	}
	return time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
}

func readJSONFile(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return true, nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return true, err
	}
	return true, nil
}

func readHTTPBodyLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxLimitResponseBytes))
}

package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

type museUsageLimitProvider struct{}

// NewMuseUsageLimitProvider returns a UsageLimitProvider for Muse Code.
func NewMuseUsageLimitProvider() agent.UsageLimitProvider {
	return &museUsageLimitProvider{}
}

type museSubscriptionUsage struct {
	ObservedAtMs float64 `json:"observedAtMs"`
	Tier         string  `json:"tier"`
	Window       *struct {
		UsedPercent        *float64 `json:"usedPercent"`
		WindowDurationMins *float64 `json:"windowDurationMins"`
		ResetsAtMs         *float64 `json:"resetsAtMs"`
	} `json:"window"`
	Weekly *struct {
		UsedPercent *float64 `json:"usedPercent"`
		ResetsAtMs  *float64 `json:"resetsAtMs"`
	} `json:"weekly"`
}

func (p *museUsageLimitProvider) FetchUsageLimits(ctx context.Context, q agent.UsageLimitQuery) (*agent.ProviderUsageLimits, error) {
	now := time.Now().UTC()
	homeDir := limitHomeDir(q)
	if homeDir == "" {
		return unavailableLimits("muse", q.Profile, agent.LimitUnavailableUnsupported, "No Muse subscription usage state found.", now), nil
	}

	roots := []string{
		filepath.Join(homeDir, ".config", "muse"),
		filepath.Join(homeDir, ".local", "share", "muse"),
	}

	var (
		best   *museSubscriptionUsage
		bestTs float64
	)

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil || d.IsDir() {
				return nil
			}
			lower := strings.ToLower(d.Name())
			if !strings.HasSuffix(lower, ".json") && !strings.HasSuffix(lower, ".jsonl") {
				return nil
			}
			var fallbackTs float64
			if fi, err := d.Info(); err == nil && fi != nil {
				fallbackTs = float64(fi.ModTime().UnixMilli())
			}
			found := scanMuseUsageFile(path, lower)
			if found != nil {
				ts := found.ObservedAtMs
				if ts <= 0 {
					ts = fallbackTs
				}
				if best == nil || ts >= bestTs {
					best = found
					bestTs = ts
				}
			}
			return nil
		})
	}

	if best == nil {
		return unavailableLimits("muse", q.Profile, agent.LimitUnavailableUnsupported, "No Muse subscription usage state found.", now), nil
	}

	windows := make([]agent.UsageLimitWindow, 0, 2)
	if best.Window != nil && best.Window.UsedPercent != nil && !math.IsNaN(*best.Window.UsedPercent) && !math.IsInf(*best.Window.UsedPercent, 0) {
		var dur int
		if best.Window.WindowDurationMins != nil && *best.Window.WindowDurationMins > 0 {
			dur = int(math.Round(*best.Window.WindowDurationMins))
		}
		var resetsAt string
		if best.Window.ResetsAtMs != nil {
			resetsAt = formatEpochMillis(*best.Window.ResetsAtMs)
		}
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "window",
			Kind:               agent.LimitWindowSession,
			Label:              "Session",
			UsedPercent:        clampPercent(*best.Window.UsedPercent),
			ResetsAt:           resetsAt,
			WindowDurationMins: dur,
		})
	}
	if best.Weekly != nil && best.Weekly.UsedPercent != nil && !math.IsNaN(*best.Weekly.UsedPercent) && !math.IsInf(*best.Weekly.UsedPercent, 0) {
		var resetsAt string
		if best.Weekly.ResetsAtMs != nil {
			resetsAt = formatEpochMillis(*best.Weekly.ResetsAtMs)
		}
		windows = append(windows, agent.UsageLimitWindow{
			ID:                 "weekly",
			Kind:               agent.LimitWindowWeekly,
			Label:              "Weekly",
			UsedPercent:        clampPercent(*best.Weekly.UsedPercent),
			ResetsAt:           resetsAt,
			WindowDurationMins: weeklyWindowMins,
		})
	}

	if len(windows) == 0 {
		return unavailableLimits("muse", q.Profile, agent.LimitUnavailableUnsupported, "No Muse subscription usage state found.", now), nil
	}

	return &agent.ProviderUsageLimits{
		Provider:    "muse",
		Profile:     q.Profile,
		DisplayName: q.DisplayName,
		Plan:        strings.TrimSpace(best.Tier),
		CheckedAt:   now.Format(time.RFC3339),
		Windows:     windows,
		Source:      "local-state",
	}, nil
}

func scanMuseUsageFile(path, lowerName string) *museSubscriptionUsage {
	if strings.HasSuffix(lowerName, ".json") {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return extractMuseSubscriptionUsage(data)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var best *museSubscriptionUsage
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !strings.Contains(string(line), "usage/changed") && !strings.Contains(string(line), "subscriptionUsage") {
			continue
		}
		if candidate := extractMuseSubscriptionUsage(line); candidate != nil {
			if best == nil || candidate.ObservedAtMs >= best.ObservedAtMs {
				best = candidate
			}
		}
	}
	return best
}

func extractMuseSubscriptionUsage(raw []byte) *museSubscriptionUsage {
	var envelope struct {
		Method            string                 `json:"method"`
		Type              string                 `json:"type"`
		Event             string                 `json:"event"`
		Params            *museSubscriptionUsage `json:"params"`
		Data              *museSubscriptionUsage `json:"data"`
		SubscriptionUsage *museSubscriptionUsage `json:"subscriptionUsage"`
		museSubscriptionUsage
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	if envelope.SubscriptionUsage != nil && isValidMuseUsage(envelope.SubscriptionUsage) {
		return envelope.SubscriptionUsage
	}
	if (envelope.Method == "usage/changed" || envelope.Type == "usage/changed" || envelope.Event == "usage/changed") {
		if envelope.Params != nil && isValidMuseUsage(envelope.Params) {
			return envelope.Params
		}
		if envelope.Data != nil && isValidMuseUsage(envelope.Data) {
			return envelope.Data
		}
	}
	if isValidMuseUsage(&envelope.museSubscriptionUsage) {
		return &envelope.museSubscriptionUsage
	}
	return nil
}

func isValidMuseUsage(u *museSubscriptionUsage) bool {
	if u == nil {
		return false
	}
	hasWindow := u.Window != nil && u.Window.UsedPercent != nil
	hasWeekly := u.Weekly != nil && u.Weekly.UsedPercent != nil
	return hasWindow || hasWeekly
}

package usage

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

// SupportedLimitProviders lists the 7 agent providers that participate in
// overarching per-profile usage limit collection, in canonical display order.
var SupportedLimitProviders = []string{
	"claude",
	"codex",
	"antigravity",
	"opencode",
	"cursor-agent",
	"grok",
	"muse",
}

var antigravitySuffixRe = regexp.MustCompile(`-(5h|weekly|quota)$`)

func supportedLimitProviderOrder(provider string) int {
	p := strings.ToLower(strings.TrimSpace(provider))
	for i, name := range SupportedLimitProviders {
		if name == p {
			return i
		}
	}
	return len(SupportedLimitProviders)
}

func windowKindOrder(kind string) int {
	switch kind {
	case agent.LimitWindowSession:
		return 0
	case agent.LimitWindowWeekly:
		return 1
	case agent.LimitWindowMonthly:
		return 2
	case agent.LimitWindowOther:
		return 3
	default:
		return 3
	}
}

func normalizeWindowKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case agent.LimitWindowSession:
		return agent.LimitWindowSession
	case agent.LimitWindowWeekly:
		return agent.LimitWindowWeekly
	case agent.LimitWindowMonthly:
		return agent.LimitWindowMonthly
	default:
		return agent.LimitWindowOther
	}
}

func cursorWindowRank(id string) int {
	switch id {
	case "totalPercentUsed":
		return 0
	case "autoPercentUsed":
		return 1
	case "apiPercentUsed":
		return 2
	default:
		return 3
	}
}

func parseLimitTime(ts string) (time.Time, bool) {
	s := strings.TrimSpace(ts)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ClampPercent clamps finite values to [0, 100] and returns 0 for NaN or Inf.
func ClampPercent(v float64) float64 {
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

// NormalizeWindows deduplicates windows by ID (keeping the higher UsedPercent),
// clamps UsedPercent to [0, 100], drops unparseable ResetsAt timestamps, and
// sorts windows using provider-specific ordering matching t3code.
func NormalizeWindows(provider string, windows []agent.UsageLimitWindow) []agent.UsageLimitWindow {
	if len(windows) == 0 {
		return []agent.UsageLimitWindow{}
	}

	byID := make(map[string]agent.UsageLimitWindow, len(windows))
	for _, raw := range windows {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			continue
		}
		w := agent.UsageLimitWindow{
			ID:                 id,
			Kind:               normalizeWindowKind(raw.Kind),
			Label:              strings.TrimSpace(raw.Label),
			UsedPercent:        ClampPercent(raw.UsedPercent),
			WindowDurationMins: raw.WindowDurationMins,
		}
		if w.WindowDurationMins < 0 {
			w.WindowDurationMins = 0
		}
		if _, ok := parseLimitTime(raw.ResetsAt); ok {
			w.ResetsAt = strings.TrimSpace(raw.ResetsAt)
		}

		if existing, exists := byID[id]; exists {
			if w.UsedPercent > existing.UsedPercent {
				if w.ResetsAt == "" {
					w.ResetsAt = existing.ResetsAt
				}
				if w.WindowDurationMins == 0 {
					w.WindowDurationMins = existing.WindowDurationMins
				}
				if w.Label == "" {
					w.Label = existing.Label
				}
				byID[id] = w
			} else {
				if existing.ResetsAt == "" && w.ResetsAt != "" {
					existing.ResetsAt = w.ResetsAt
				}
				if existing.WindowDurationMins == 0 && w.WindowDurationMins > 0 {
					existing.WindowDurationMins = w.WindowDurationMins
				}
				if existing.Label == "" && w.Label != "" {
					existing.Label = w.Label
				}
				byID[id] = existing
			}
		} else {
			byID[id] = w
		}
	}

	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "cursor-agent" || prov == "cursor" {
		_, hasAuto := byID["autoPercentUsed"]
		_, hasAPI := byID["apiPercentUsed"]
		if hasAuto && hasAPI {
			delete(byID, "totalPercentUsed")
		}
	}

	out := make([]agent.UsageLimitWindow, 0, len(byID))
	for _, w := range byID {
		out = append(out, w)
	}

	switch prov {
	case "antigravity":
		sort.Slice(out, func(i, j int) bool {
			li, lj := out[i], out[j]
			gi := strings.HasPrefix(li.ID, "gemini-")
			gj := strings.HasPrefix(lj.ID, "gemini-")
			if gi != gj {
				return gi
			}
			fi := antigravitySuffixRe.ReplaceAllString(li.ID, "")
			fj := antigravitySuffixRe.ReplaceAllString(lj.ID, "")
			if fi != fj {
				return fi < fj
			}
			if ki, kj := windowKindOrder(li.Kind), windowKindOrder(lj.Kind); ki != kj {
				return ki < kj
			}
			return li.ID < lj.ID
		})
	case "cursor-agent", "cursor":
		sort.Slice(out, func(i, j int) bool {
			li, lj := out[i], out[j]
			if ri, rj := cursorWindowRank(li.ID), cursorWindowRank(lj.ID); ri != rj {
				return ri < rj
			}
			if ki, kj := windowKindOrder(li.Kind), windowKindOrder(lj.Kind); ki != kj {
				return ki < kj
			}
			return li.ID < lj.ID
		})
	default:
		sort.Slice(out, func(i, j int) bool {
			li, lj := out[i], out[j]
			if ki, kj := windowKindOrder(li.Kind), windowKindOrder(lj.Kind); ki != kj {
				return ki < kj
			}
			return li.ID < lj.ID
		})
	}

	return out
}

func baseIdentityKey(r *agent.ProviderUsageLimits) string {
	if r == nil {
		return ""
	}
	prov := strings.ToLower(strings.TrimSpace(r.Provider))
	if email := strings.ToLower(strings.TrimSpace(r.Email)); email != "" {
		return prov + ":" + email
	}
	if fp := strings.TrimSpace(r.CredentialFingerprint); fp != "" {
		return prov + ":credential:" + fp
	}
	return ""
}

// ResolveIdentityKeys resolves a stable deduplication key for each entry in
// raws using t3code's two-pass accountKeyResolver semantics.
func ResolveIdentityKeys(raws []*agent.ProviderUsageLimits) map[int]string {
	knownWorkspaces := make(map[string]map[string]struct{})
	for _, r := range raws {
		if r == nil {
			continue
		}
		base := baseIdentityKey(r)
		ws := strings.TrimSpace(r.WorkspaceID)
		if base != "" && ws != "" {
			if knownWorkspaces[base] == nil {
				knownWorkspaces[base] = make(map[string]struct{})
			}
			knownWorkspaces[base][ws] = struct{}{}
		}
	}

	keys := make(map[int]string, len(raws))
	for i, r := range raws {
		if r == nil {
			continue
		}
		prov := strings.ToLower(strings.TrimSpace(r.Provider))
		base := baseIdentityKey(r)
		if base == "" {
			prof := strings.TrimSpace(r.Profile)
			if prof == "" {
				prof = "default"
			}
			keys[i] = prov + ":profile:" + prof
			continue
		}
		if ws := strings.TrimSpace(r.WorkspaceID); ws != "" {
			keys[i] = base + ":" + ws
			continue
		}
		if known := knownWorkspaces[base]; len(known) == 1 {
			for onlyWS := range known {
				keys[i] = base + ":" + onlyWS
			}
			continue
		}
		keys[i] = base
	}

	return keys
}

type mergedAccountState struct {
	account          LimitAccount
	checkedAt        time.Time
	creditsCheckedAt time.Time
	profiles         map[string]struct{}
}

// NormalizeAndMergeLimits normalizes raw per-profile provider limit snapshots,
// merges profiles sharing the same identity key, preserves last-good bars as
// Stale on transient probe failures, suppresses weekly-blocked session windows,
// and returns a deterministically sorted LimitsSnapshot.
func NormalizeAndMergeLimits(raws []*agent.ProviderUsageLimits, previous *LimitsSnapshot, now time.Time) LimitsSnapshot {
	if now.IsZero() {
		now = time.Now()
	}

	prevByKey := make(map[string]LimitAccount)
	prevByProviderProfile := make(map[string]LimitAccount)
	if previous != nil {
		for _, acc := range previous.Accounts {
			if acc.IdentityKey == "" || acc.Unavailable != nil || len(acc.Windows) == 0 {
				continue
			}
			prevByKey[acc.IdentityKey] = acc
			accProv := strings.ToLower(strings.TrimSpace(acc.Provider))
			for _, p := range acc.Profiles {
				if p = strings.TrimSpace(p); p != "" {
					prevByProviderProfile[accProv+"\x00"+p] = acc
				}
			}
		}
	}

	keys := ResolveIdentityKeys(raws)
	live := make(map[string]*mergedAccountState)
	staleFallbacks := make(map[string]*mergedAccountState)

	var notices []LimitNotice
	seenNotices := make(map[LimitNotice]struct{})
	addNotice := func(n LimitNotice) {
		if _, dup := seenNotices[n]; dup {
			return
		}
		seenNotices[n] = struct{}{}
		notices = append(notices, n)
	}

	for i, r := range raws {
		if r == nil {
			continue
		}
		prov := strings.ToLower(strings.TrimSpace(r.Provider))
		prof := strings.TrimSpace(r.Profile)
		if prof == "" {
			prof = "default"
		}
		idKey := keys[i]

		if r.Unavailable != nil {
			if r.Unavailable.Reason == agent.LimitUnavailableUnsupported {
				// Silent skip: API key / unconfigured accounts don't clutter notices.
				continue
			}
			msg := strings.TrimSpace(r.Unavailable.Message)
			if msg == "" {
				msg = "Could not read limits."
			}
			addNotice(LimitNotice{
				Provider: prov,
				Profile:  prof,
				Message:  msg,
			})
			prev, ok := prevByKey[idKey]
			if !ok && idKey == prov+":profile:"+prof {
				prev, ok = prevByProviderProfile[prov+"\x00"+prof]
			}
			if ok {
				prevKey := prev.IdentityKey
				st, exists := staleFallbacks[prevKey]
				if !exists {
					cloned := cloneLimitAccount(prev)
					cloned.Stale = true
					cloned.Unavailable = nil
					cloned.Windows = NormalizeWindows(cloned.Provider, cloned.Windows)
					pset := make(map[string]struct{}, len(cloned.Profiles)+1)
					for _, p := range cloned.Profiles {
						if p != "" {
							pset[p] = struct{}{}
						}
					}
					pset[prof] = struct{}{}
					staleFallbacks[prevKey] = &mergedAccountState{
						account:  cloned,
						profiles: pset,
					}
				} else {
					st.profiles[prof] = struct{}{}
				}
			}
			continue
		}

		wins := NormalizeWindows(prov, r.Windows)
		if len(wins) == 0 && r.ResetCredits == nil {
			continue
		}

		checkedAtTime, ok := parseLimitTime(r.CheckedAt)
		checkedAtStr := strings.TrimSpace(r.CheckedAt)
		if !ok {
			checkedAtTime = now.UTC()
			checkedAtStr = checkedAtTime.Format(time.RFC3339)
		}

		st, exists := live[idKey]
		if !exists {
			acc := LimitAccount{
				IdentityKey: idKey,
				Provider:    prov,
				DisplayName: strings.TrimSpace(r.DisplayName),
				Email:       strings.TrimSpace(r.Email),
				Plan:        strings.TrimSpace(r.Plan),
				WorkspaceID: strings.TrimSpace(r.WorkspaceID),
				CheckedAt:   checkedAtStr,
				Windows:     wins,
				Source:      strings.TrimSpace(r.Source),
				Stale:       false,
			}
			var creditsAt time.Time
			if r.ResetCredits != nil {
				rc := *r.ResetCredits
				acc.ResetCredits = &rc
				creditsAt = checkedAtTime
			}
			live[idKey] = &mergedAccountState{
				account:          acc,
				checkedAt:        checkedAtTime,
				creditsCheckedAt: creditsAt,
				profiles:         map[string]struct{}{prof: {}},
			}
			continue
		}

		st.profiles[prof] = struct{}{}
		if checkedAtTime.After(st.checkedAt) {
			st.checkedAt = checkedAtTime
			st.account.CheckedAt = checkedAtStr
			st.account.Windows = wins
			if src := strings.TrimSpace(r.Source); src != "" {
				st.account.Source = src
			}
		}
		if st.account.DisplayName == "" {
			st.account.DisplayName = strings.TrimSpace(r.DisplayName)
		}
		if st.account.Email == "" {
			st.account.Email = strings.TrimSpace(r.Email)
		}
		if st.account.Plan == "" {
			st.account.Plan = strings.TrimSpace(r.Plan)
		}
		if st.account.WorkspaceID == "" {
			st.account.WorkspaceID = strings.TrimSpace(r.WorkspaceID)
		}
		if st.account.Source == "" {
			st.account.Source = strings.TrimSpace(r.Source)
		}
		if r.ResetCredits != nil {
			if st.account.ResetCredits == nil || checkedAtTime.After(st.creditsCheckedAt) {
				rc := *r.ResetCredits
				st.account.ResetCredits = &rc
				st.creditsCheckedAt = checkedAtTime
			}
		}
	}

	accounts := make([]LimitAccount, 0, len(live)+len(staleFallbacks))
	for _, st := range live {
		st.account.Profiles = sortedProfileKeys(st.profiles)
		applyWeeklyExhaustedSuppression(&st.account, now)
		accounts = append(accounts, st.account)
	}
	for idKey, st := range staleFallbacks {
		if _, hasLive := live[idKey]; hasLive {
			continue
		}
		st.account.Profiles = sortedProfileKeys(st.profiles)
		applyWeeklyExhaustedSuppression(&st.account, now)
		accounts = append(accounts, st.account)
	}

	sort.Slice(accounts, func(i, j int) bool {
		ai, aj := accounts[i], accounts[j]
		oi, oj := supportedLimitProviderOrder(ai.Provider), supportedLimitProviderOrder(aj.Provider)
		if oi != oj {
			return oi < oj
		}
		if ai.Provider != aj.Provider {
			return ai.Provider < aj.Provider
		}
		ni, nj := accountSortName(ai), accountSortName(aj)
		if ni != nj {
			return ni < nj
		}
		return ai.IdentityKey < aj.IdentityKey
	})

	sort.Slice(notices, func(i, j int) bool {
		if notices[i].Provider != notices[j].Provider {
			return notices[i].Provider < notices[j].Provider
		}
		if notices[i].Profile != notices[j].Profile {
			return notices[i].Profile < notices[j].Profile
		}
		return notices[i].Message < notices[j].Message
	})

	return LimitsSnapshot{
		Version:     LimitsCacheVersion,
		CheckedAtMs: now.UnixMilli(),
		Accounts:    accounts,
		Notices:     notices,
	}
}

func cloneLimitAccount(src LimitAccount) LimitAccount {
	dst := src
	if len(src.Profiles) > 0 {
		dst.Profiles = append([]string(nil), src.Profiles...)
	}
	if len(src.Windows) > 0 {
		dst.Windows = append([]agent.UsageLimitWindow(nil), src.Windows...)
	}
	if src.ResetCredits != nil {
		rc := *src.ResetCredits
		dst.ResetCredits = &rc
	}
	if src.Unavailable != nil {
		u := *src.Unavailable
		dst.Unavailable = &u
	}
	return dst
}

func sortedProfileKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func accountSortName(a LimitAccount) string {
	if s := strings.TrimSpace(a.DisplayName); s != "" {
		return strings.ToLower(s)
	}
	if s := strings.TrimSpace(a.Email); s != "" {
		return strings.ToLower(s)
	}
	return strings.ToLower(a.IdentityKey)
}

func applyWeeklyExhaustedSuppression(acc *LimitAccount, now time.Time) {
	if acc == nil || len(acc.Windows) == 0 {
		return
	}
	var spentWeekly *agent.UsageLimitWindow
	for i := range acc.Windows {
		w := &acc.Windows[i]
		if w.UsedPercent < 100 {
			continue
		}
		switch acc.Provider {
		case "claude":
			if w.ID == "seven_day" {
				spentWeekly = w
			}
		case "codex":
			if w.Kind == agent.LimitWindowWeekly {
				spentWeekly = w
			}
		}
		if spentWeekly != nil {
			break
		}
	}
	if spentWeekly == nil {
		return
	}

	cutoff := now
	if checked, ok := parseLimitTime(acc.CheckedAt); ok && checked.After(cutoff) {
		cutoff = checked
	}
	weeklyReset, hasWeekly := parseLimitTime(spentWeekly.ResetsAt)

	for i := range acc.Windows {
		w := &acc.Windows[i]
		if w.Kind != agent.LimitWindowSession {
			continue
		}
		origUsed := w.UsedPercent
		sessionReset, hasSession := parseLimitTime(w.ResetsAt)

		var nextReset string
		if hasWeekly && hasSession && weeklyReset.After(cutoff) && sessionReset.After(cutoff) {
			if !sessionReset.After(weeklyReset) {
				nextReset = spentWeekly.ResetsAt
			} else if origUsed >= 100 {
				nextReset = w.ResetsAt
			} else {
				nextReset = spentWeekly.ResetsAt
			}
		} else if hasWeekly && !hasSession && weeklyReset.After(cutoff) {
			nextReset = spentWeekly.ResetsAt
		}

		w.UsedPercent = 100
		w.ResetsAt = nextReset
		w.WindowDurationMins = 0
	}
}

// RemainingPercent returns the rounded remaining percentage [0, 100] for w.
func RemainingPercent(w agent.UsageLimitWindow) int {
	return int(math.Round(100 - ClampPercent(w.UsedPercent)))
}

// ElapsedShare returns the fraction [0, 1] of w's window duration that has
// elapsed as of now. Returns (0, false) if ResetsAt is invalid or
// WindowDurationMins <= 0.
func ElapsedShare(w agent.UsageLimitWindow, now time.Time) (float64, bool) {
	resetsAt, ok := parseLimitTime(w.ResetsAt)
	if !ok || w.WindowDurationMins <= 0 {
		return 0, false
	}
	length := time.Duration(w.WindowDurationMins) * time.Minute
	if length <= 0 {
		return 0, false
	}
	elapsed := float64(length-resetsAt.Sub(now)) / float64(length)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > 1 {
		elapsed = 1
	}
	return elapsed, true
}

// PaceRemainingPercent returns the rounded percentage of window time remaining
// (matching remaining quota bars). Returns (0, false) when elapsed share cannot
// be determined.
func PaceRemainingPercent(w agent.UsageLimitWindow, now time.Time) (int, bool) {
	elapsed, ok := ElapsedShare(w, now)
	if !ok {
		return 0, false
	}
	return int(math.Round((1 - elapsed) * 100)), true
}

// ComputeWindowPace classifies usage pace relative to elapsed window time:
// gap = usedPercent - elapsed*100; > 5 => ahead, < -5 => under, else on.
func ComputeWindowPace(w agent.UsageLimitWindow, now time.Time) (LimitPace, bool) {
	elapsed, ok := ElapsedShare(w, now)
	if !ok {
		return "", false
	}
	gap := ClampPercent(w.UsedPercent) - elapsed*100
	if gap > 5 {
		return LimitPaceAhead, true
	}
	if gap < -5 {
		return LimitPaceUnder, true
	}
	return LimitPaceOn, true
}

// FormatResetsIn formats the time until w resets relative to now.
func FormatResetsIn(w agent.UsageLimitWindow, now time.Time) string {
	resetsAt, ok := parseLimitTime(w.ResetsAt)
	if !ok {
		return ""
	}
	rem := resetsAt.Sub(now)
	if rem <= 0 {
		return "resets now"
	}
	if rem < time.Minute {
		return "resets <1m"
	}
	days := int(rem / (24 * time.Hour))
	hours := int((rem % (24 * time.Hour)) / time.Hour)
	mins := int((rem % time.Hour) / time.Minute)
	if days > 0 {
		return fmt.Sprintf("resets in %dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("resets in %dh %dm", hours, mins)
	}
	return fmt.Sprintf("resets in %dm", mins)
}

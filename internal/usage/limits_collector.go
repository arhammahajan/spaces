package usage

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
	"github.com/arhammahajan/spaces/internal/harness/profiles"
)

const (
	maxLimitProbeConcurrency = 8
	limitProbeTimeout        = 12 * time.Second
)

// LimitsCollectOptions configures a LimitsCollector.
type LimitsCollectOptions struct {
	HomeDir   string
	CachePath string
	Registry  *agent.Registry
	Now       func() time.Time
}

type inflightLimitsCollect struct {
	done chan struct{}
	snap LimitsSnapshot
	err  error
}

// LimitsCollector orchestrates per-profile usage-limit probing across all
// supported providers, with persistent ~/.harness caching and concurrent call
// coalescing.
type LimitsCollector struct {
	opts LimitsCollectOptions

	mu       sync.Mutex
	hasCache bool
	cached   LimitsSnapshot
	inflight *inflightLimitsCollect
}

// NewLimitsCollector creates a LimitsCollector rooted at homeDir using
// DefaultRegistry and the standard ~/.harness/usage-limits-cache.json path.
func NewLimitsCollector(homeDir string) *LimitsCollector {
	return NewLimitsCollectorWithOptions(LimitsCollectOptions{HomeDir: homeDir})
}

// NewLimitsCollectorWithOptions creates a LimitsCollector with explicit options.
func NewLimitsCollectorWithOptions(opts LimitsCollectOptions) *LimitsCollector {
	return &LimitsCollector{opts: opts}
}

func (c *LimitsCollector) resolveCachePath() string {
	if p := strings.TrimSpace(c.opts.CachePath); p != "" {
		return p
	}
	return DefaultLimitsCachePath(c.opts.HomeDir)
}

func isNonEmptyLimitsSnapshot(snap LimitsSnapshot) bool {
	return len(snap.Accounts) > 0 || len(snap.Notices) > 0
}

// Collect returns a normalized LimitsSnapshot.
//
// When force is false, Collect immediately returns the in-memory cached
// snapshot or the persisted ~/.harness/usage-limits-cache.json snapshot (if
// present and non-empty), falling through to a live probe only when no cache
// exists yet.
//
// When force is true (or no cache exists), Collect coalesces concurrent calls,
// fans out live probes across all enabled profiles for the 7 supported
// providers, normalizes and merges results with stale-fallback preservation,
// and atomically persists the snapshot to ~/.harness/usage-limits-cache.json.
func (c *LimitsCollector) Collect(ctx context.Context, force bool) (LimitsSnapshot, error) {
	cachePath := c.resolveCachePath()

	if !force {
		c.mu.Lock()
		if c.hasCache {
			snap := c.cached
			c.mu.Unlock()
			return snap, nil
		}
		c.mu.Unlock()

		if snap, ok := LoadLimitsCache(cachePath); ok && isNonEmptyLimitsSnapshot(snap) {
			c.mu.Lock()
			c.cached = snap
			c.hasCache = true
			c.mu.Unlock()
			return snap, nil
		}
	}

	c.mu.Lock()
	if c.inflight != nil {
		flight := c.inflight
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.snap, flight.err
		case <-ctx.Done():
			return LimitsSnapshot{}, ctx.Err()
		}
	}

	flight := &inflightLimitsCollect{done: make(chan struct{})}
	c.inflight = flight
	var prevPtr *LimitsSnapshot
	if c.hasCache {
		cp := c.cached
		prevPtr = &cp
	}
	c.mu.Unlock()

	if prevPtr == nil {
		if diskSnap, ok := LoadLimitsCache(cachePath); ok {
			prevPtr = &diskSnap
		}
	}

	snap, err := c.probeLive(ctx, prevPtr, cachePath)

	c.mu.Lock()
	if err == nil {
		c.cached = snap
		c.hasCache = true
	}
	flight.snap = snap
	flight.err = err
	c.inflight = nil
	close(flight.done)
	c.mu.Unlock()

	return snap, err
}

type limitProbeTarget struct {
	providerID string
	provider   agent.UsageLimitProvider
	query      agent.UsageLimitQuery
}

func (c *LimitsCollector) probeLive(ctx context.Context, previous *LimitsSnapshot, cachePath string) (LimitsSnapshot, error) {
	nowFn := c.opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	realHome := agent.RealHomeDir(c.opts.HomeDir)
	reg := c.opts.Registry
	if reg == nil {
		reg = agent.DefaultRegistry()
	}

	targets := buildLimitProbeTargets(reg, realHome)
	if len(targets) == 0 {
		snap := NormalizeAndMergeLimits(nil, previous, now)
		if cachePath != "" {
			_ = SaveLimitsCache(cachePath, snap)
		}
		return snap, nil
	}

	raws := make([]*agent.ProviderUsageLimits, len(targets))
	sem := make(chan struct{}, maxLimitProbeConcurrency)
	var wg sync.WaitGroup

	for idx, target := range targets {
		idx := idx
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			probeCtx, cancel := context.WithTimeout(ctx, limitProbeTimeout)
			defer cancel()

			res, err := target.provider.FetchUsageLimits(probeCtx, target.query)
			if err != nil {
				raws[idx] = &agent.ProviderUsageLimits{
					Provider:    target.providerID,
					Profile:     target.query.Profile,
					DisplayName: target.query.DisplayName,
					CheckedAt:   now.UTC().Format(time.RFC3339),
					Unavailable: &agent.UsageLimitUnavailable{
						Reason:  agent.LimitUnavailableProbeFailed,
						Message: err.Error(),
					},
				}
				return
			}
			if res == nil {
				return
			}
			cp := *res
			if strings.TrimSpace(cp.Provider) == "" {
				cp.Provider = target.providerID
			}
			if strings.TrimSpace(cp.Profile) == "" {
				cp.Profile = target.query.Profile
			}
			if strings.TrimSpace(cp.DisplayName) == "" {
				cp.DisplayName = target.query.DisplayName
			}
			if strings.TrimSpace(cp.CheckedAt) == "" {
				cp.CheckedAt = now.UTC().Format(time.RFC3339)
			}
			raws[idx] = &cp
		}()
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return LimitsSnapshot{}, err
	}

	snap := NormalizeAndMergeLimits(raws, previous, now)
	if cachePath != "" {
		_ = SaveLimitsCache(cachePath, snap)
	}
	return snap, nil
}

func buildLimitProbeTargets(reg *agent.Registry, realHome string) []limitProbeTarget {
	var (
		store           *profiles.Store
		storeNamed      []profiles.NamedProfile
		discoveredRoots []agent.ProfileRoot
	)
	if realHome != "" {
		paths := harnessenv.Paths{
			Home:       realHome,
			ConfigHome: filepath.Join(realHome, ".config"),
			DataHome:   filepath.Join(realHome, ".local", "share"),
			StateHome:  filepath.Join(realHome, ".local", "state"),
			CacheHome:  filepath.Join(realHome, ".cache"),
		}
		if st, err := profiles.Open(paths); err == nil {
			store = st
			storeNamed = st.List()
		}
		discoveredRoots = reg.DiscoverProfileRoots(realHome)
	}

	var targets []limitProbeTarget
	for _, providerID := range SupportedLimitProviders {
		p, ok := reg.ResolveRuntime(providerID)
		if !ok || p == nil {
			continue
		}
		ulp := p.UsageLimitProvider()
		if ulp == nil {
			continue
		}

		// 1. Always include the "default" profile, enriched with any customized
		// default profile settings from profiles.Open(paths).
		defaultQuery := agent.UsageLimitQuery{
			Profile:     profiles.DefaultProfileName,
			DisplayName: "Default",
			HomeDir:     realHome,
			RealHomeDir: realHome,
			IsDefault:   true,
		}
		if store != nil {
			if defProf, customized := store.GetDefault(providerID); customized {
				if dn := strings.TrimSpace(defProf.DisplayName); dn != "" {
					defaultQuery.DisplayName = dn
				}
				if len(defProf.Env) > 0 {
					defaultQuery.Env = cloneStringMap(defProf.Env)
				}
				if bp := strings.TrimSpace(defProf.BinaryPath); bp != "" {
					defaultQuery.BinaryPath = harnessenv.ExpandHome(bp, realHome)
				}
			}
		}
		targets = append(targets, limitProbeTarget{
			providerID: providerID,
			provider:   ulp,
			query:      defaultQuery,
		})

		seen := map[string]bool{profiles.DefaultProfileName: true}

		// 2. Include every enabled named profile from profiles.Open(paths).
		for _, np := range storeNamed {
			aid := reg.CanonicalID(np.Profile.Agent)
			if aid == "" {
				aid = strings.ToLower(strings.TrimSpace(np.Profile.Agent))
			}
			if aid != providerID {
				continue
			}
			seen[np.Name] = true
			if !np.Profile.Enabled {
				continue
			}
			homeDir := strings.TrimSpace(np.Profile.ShadowHome)
			if homeDir == "" {
				homeDir = filepath.Join(realHome, ".harness", "profiles", np.Profile.Agent, np.Name, "home")
			} else {
				homeDir = harnessenv.ExpandHome(homeDir, realHome)
			}
			targets = append(targets, limitProbeTarget{
				providerID: providerID,
				provider:   ulp,
				query: agent.UsageLimitQuery{
					Profile:     np.Name,
					DisplayName: strings.TrimSpace(np.Profile.DisplayName),
					HomeDir:     homeDir,
					RealHomeDir: realHome,
					IsDefault:   false,
					Env:         cloneStringMap(np.Profile.Env),
					BinaryPath:  harnessenv.ExpandHome(strings.TrimSpace(np.Profile.BinaryPath), realHome),
				},
			})
		}

		// 3. Include any legacy/fixture profile directory under
		// <realHome>/.harness/profiles/<agent>/<name>/home not already listed.
		for _, pr := range discoveredRoots {
			if pr.Agent != providerID || seen[pr.Profile] {
				continue
			}
			seen[pr.Profile] = true
			targets = append(targets, limitProbeTarget{
				providerID: providerID,
				provider:   ulp,
				query: agent.UsageLimitQuery{
					Profile:     pr.Profile,
					HomeDir:     pr.HomeDir,
					RealHomeDir: realHome,
					IsDefault:   false,
				},
			})
		}
	}

	return targets
}

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

package gitinfo

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
)

const (
	defaultGitTTL     = 5 * time.Second
	defaultPRTTL      = 30 * time.Second
	defaultRepoErrTTL = 60 * time.Second
	maxFetchWorkers   = 4
)

type cachedGitEntry struct {
	info      *model.GitInfo
	fetchedAt time.Time
}

type cachedRepoIdentity struct {
	id        repoIdentity
	ok        bool
	fetchedAt time.Time
}

type cachedPREntry struct {
	gh        BranchGitHub
	fetchedAt time.Time
}

type repoBranchKey struct {
	repoKey string
	branch  string
}

// Collector caches local git status and GitHub PR/CI summaries across ticks
// so popups open instantaneously and refresh in the background without blocking.
type Collector struct {
	run        CommandRunner
	now        func() time.Time
	gitTTL     time.Duration
	prTTL      time.Duration
	repoErrTTL time.Duration

	mu       sync.Mutex
	gitCache map[string]cachedGitEntry
	repoIDs  map[string]cachedRepoIdentity
	prCache  map[repoBranchKey]cachedPREntry
}

// NewCollector creates a Collector with sensible default TTLs.
func NewCollector() *Collector {
	return &Collector{
		run:        defaultRunCommand,
		now:        time.Now,
		gitTTL:     defaultGitTTL,
		prTTL:      defaultPRTTL,
		repoErrTTL: defaultRepoErrTTL,
		gitCache:   make(map[string]cachedGitEntry),
		repoIDs:    make(map[string]cachedRepoIdentity),
		prCache:    make(map[repoBranchKey]cachedPREntry),
	}
}

// Cached returns a deep copy of the cached GitInfo (with any cached PR/CI
// merged in) for cwd, or nil if none is cached yet. Never blocks on I/O.
func (c *Collector) Cached(cwd string) *model.GitInfo {
	if c == nil || strings.TrimSpace(cwd) == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.gitCache[cwd]
	if !ok || entry.info == nil {
		return nil
	}
	return c.mergeCachedPRLocked(entry.info)
}

// AttachCached populates Space.Git and Pane.Git from the in-memory cache
// without running any subprocesses.
func (c *Collector) AttachCached(spaces []model.Space) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for si := range spaces {
		if sp := &spaces[si]; sp.CWD != "" {
			if e, ok := c.gitCache[sp.CWD]; ok && e.info != nil {
				sp.Git = c.mergeCachedPRLocked(e.info)
			}
		}
		for pi := range spaces[si].Panes {
			p := &spaces[si].Panes[pi]
			if p.CWD != "" {
				if e, ok := c.gitCache[p.CWD]; ok && e.info != nil {
					p.Git = c.mergeCachedPRLocked(e.info)
				}
			}
		}
	}
}

// FetchGit collects fresh local git status for any unique CWD whose cached
// entry is missing or stale (or all CWDs when force is true). It merges any
// already-cached PR/CI summaries and returns the resulting CWD -> GitInfo map.
func (c *Collector) FetchGit(ctx context.Context, cwds []string, force bool) map[string]*model.GitInfo {
	unique := dedupeStrings(cwds)
	if len(unique) == 0 {
		return nil
	}

	now := c.now()
	var toFetch []string
	result := make(map[string]*model.GitInfo, len(unique))

	c.mu.Lock()
	for _, dir := range unique {
		if !force {
			if e, ok := c.gitCache[dir]; ok && now.Sub(e.fetchedAt) < c.gitTTL {
				if e.info != nil {
					result[dir] = c.mergeCachedPRLocked(e.info)
				}
				continue
			}
		}
		toFetch = append(toFetch, dir)
	}
	c.mu.Unlock()

	if len(toFetch) == 0 {
		return result
	}

	fetched := make(map[string]*model.GitInfo, len(toFetch))
	var fmu sync.Mutex
	workers := len(toFetch)
	if workers > maxFetchWorkers {
		workers = maxFetchWorkers
	}
	jobs := make(chan string, len(toFetch))
	for _, dir := range toFetch {
		jobs <- dir
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range jobs {
				info := CollectGitStatus(ctx, dir, c.run)
				fmu.Lock()
				fetched[dir] = info
				fmu.Unlock()
			}
		}()
	}
	wg.Wait()

	doneAt := c.now()
	c.mu.Lock()
	for dir, info := range fetched {
		c.gitCache[dir] = cachedGitEntry{info: info, fetchedAt: doneAt}
		if info != nil {
			result[dir] = c.mergeCachedPRLocked(info)
		}
	}
	c.mu.Unlock()
	return result
}

// FetchPR queries GitHub PR and CI check status for all repos/branches present
// in gitByCWD whose PR cache entry is missing or stale (or all when force is
// true). Returns a fresh CWD -> GitInfo map with PR/CI attached.
func (c *Collector) FetchPR(ctx context.Context, gitByCWD map[string]*model.GitInfo, force bool) map[string]*model.GitInfo {
	if len(gitByCWD) == 0 {
		return nil
	}

	type repoBatch struct {
		repoKey  string
		repoRoot string
		branches map[string]struct{}
	}

	now := c.now()
	batches := make(map[string]*repoBatch)

	c.mu.Lock()
	for _, info := range gitByCWD {
		if info == nil || info.RepoKey == "" || info.RepoRoot == "" || info.Branch == "" {
			continue
		}
		key := repoBranchKey{repoKey: info.RepoKey, branch: info.Branch}
		if !force {
			if e, ok := c.prCache[key]; ok && now.Sub(e.fetchedAt) < c.prTTL {
				continue
			}
		}
		if rid, ok := c.repoIDs[info.RepoKey]; ok && !rid.ok && !force && now.Sub(rid.fetchedAt) < c.repoErrTTL {
			continue
		}
		b := batches[info.RepoKey]
		if b == nil {
			b = &repoBatch{
				repoKey:  info.RepoKey,
				repoRoot: info.RepoRoot,
				branches: make(map[string]struct{}),
			}
			batches[info.RepoKey] = b
		}
		b.branches[info.Branch] = struct{}{}
	}
	c.mu.Unlock()

	if len(batches) > 0 {
		list := make([]*repoBatch, 0, len(batches))
		for _, b := range batches {
			list = append(list, b)
		}
		workers := len(list)
		if workers > maxFetchWorkers {
			workers = maxFetchWorkers
		}
		jobs := make(chan *repoBatch, len(list))
		for _, b := range list {
			jobs <- b
		}
		close(jobs)

		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for batch := range jobs {
					c.fetchRepoBatch(ctx, batch.repoKey, batch.repoRoot, sortedKeys(batch.branches), force)
				}
			}()
		}
		wg.Wait()
	}

	out := make(map[string]*model.GitInfo, len(gitByCWD))
	c.mu.Lock()
	for cwd, info := range gitByCWD {
		if info != nil {
			out[cwd] = c.mergeCachedPRLocked(info)
		}
	}
	c.mu.Unlock()
	return out
}

func (c *Collector) fetchRepoBatch(ctx context.Context, repoKey, repoRoot string, branches []string, force bool) {
	if len(branches) == 0 {
		return
	}
	c.mu.Lock()
	rid, hasRID := c.repoIDs[repoKey]
	c.mu.Unlock()

	if !hasRID || (!rid.ok && force) || (!rid.ok && c.now().Sub(rid.fetchedAt) >= c.repoErrTTL) {
		id, err := resolveRepoIdentity(ctx, repoRoot, c.run)
		rid = cachedRepoIdentity{id: id, ok: err == nil, fetchedAt: c.now()}
		c.mu.Lock()
		c.repoIDs[repoKey] = rid
		c.mu.Unlock()
	}
	if !rid.ok {
		return
	}

	summaries, err := FetchRepoGitHub(ctx, repoRoot, rid.id, branches, c.run)
	if err != nil {
		return
	}
	doneAt := c.now()
	c.mu.Lock()
	for _, br := range branches {
		c.prCache[repoBranchKey{repoKey: repoKey, branch: br}] = cachedPREntry{
			gh:        summaries[br],
			fetchedAt: doneAt,
		}
	}
	c.mu.Unlock()
}

func (c *Collector) mergeCachedPRLocked(base *model.GitInfo) *model.GitInfo {
	cp := base.Clone()
	if cp == nil || cp.RepoKey == "" || cp.Branch == "" {
		return cp
	}
	if e, ok := c.prCache[repoBranchKey{repoKey: cp.RepoKey, branch: cp.Branch}]; ok {
		if e.gh.PR != nil {
			pr := *e.gh.PR
			cp.PR = &pr
		}
		if e.gh.Checks != nil {
			ch := *e.gh.Checks
			cp.Checks = &ch
		}
	}
	return cp
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

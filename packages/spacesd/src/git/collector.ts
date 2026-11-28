import type {
  CheckSummary,
  GitInfo,
  PRSummary,
  Space,
} from "@spaces/types";
import { CliGitDriver, type LocalGitDriver } from "./driver";

export const DEFAULT_GIT_TTL_MS = 5_000;
export const DEFAULT_PR_TTL_MS = 30_000;
export const DEFAULT_REPO_ERR_TTL_MS = 60_000;
export const MAX_FETCH_WORKERS = 4;

export interface ForgeLookupResult {
  pr?: PRSummary | null;
  checks?: CheckSummary | null;
}

export type ForgeLookupFn = (
  cwd: string,
  branch: string,
) => Promise<ForgeLookupResult | null>;

export interface GitCollectorOptions {
  driver?: LocalGitDriver;
  now?: () => number;
  gitTtlMs?: number;
  prTtlMs?: number;
  repoErrTtlMs?: number;
  maxWorkers?: number;
  forgeLookup?: ForgeLookupFn;
}

interface CachedGitEntry {
  info: GitInfo | null;
  fetchedAt: number;
}

interface CachedPREntry {
  pr: PRSummary | null;
  checks: CheckSummary | null;
  fetchedAt: number;
}

function prCacheKey(repoKey: string, branch: string): string {
  return `${repoKey}\0${branch}`;
}

function cloneGitInfo(info: GitInfo): GitInfo {
  return {
    ...info,
    pr: info.pr ? { ...info.pr } : null,
    checks: info.checks ? { ...info.checks } : null,
  };
}

function dedupeStrings(items: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of items) {
    const trimmed = item.trim();
    if (!trimmed || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    out.push(trimmed);
  }
  return out;
}

async function runPool<T>(
  items: readonly T[],
  maxWorkers: number,
  worker: (item: T) => Promise<void>,
): Promise<void> {
  if (items.length === 0) {
    return;
  }
  const concurrency = Math.max(1, Math.min(maxWorkers, items.length));
  let cursor = 0;

  const runners: Array<Promise<void>> = [];
  for (let i = 0; i < concurrency; i += 1) {
    runners.push(
      (async () => {
        while (true) {
          const idx = cursor;
          cursor += 1;
          if (idx >= items.length) {
            return;
          }
          await worker(items[idx]!);
        }
      })(),
    );
  }
  await Promise.all(runners);
}

/**
 * Caches local git status and forge PR/CI summaries across ticks so UI
 * snapshots open instantaneously and refresh in the background without blocking.
 */
export class GitCollector {
  private readonly driver: LocalGitDriver;
  private readonly now: () => number;
  private readonly gitTtlMs: number;
  private readonly prTtlMs: number;
  private readonly repoErrTtlMs: number;
  private readonly maxWorkers: number;
  private readonly defaultForgeLookup?: ForgeLookupFn;

  private readonly gitCache = new Map<string, CachedGitEntry>();
  private readonly prCache = new Map<string, CachedPREntry>();
  private readonly repoErrCache = new Map<string, number>();

  constructor(opts: GitCollectorOptions = {}) {
    this.driver = opts.driver ?? new CliGitDriver();
    this.now = opts.now ?? Date.now;
    this.gitTtlMs = opts.gitTtlMs ?? DEFAULT_GIT_TTL_MS;
    this.prTtlMs = opts.prTtlMs ?? DEFAULT_PR_TTL_MS;
    this.repoErrTtlMs = opts.repoErrTtlMs ?? DEFAULT_REPO_ERR_TTL_MS;
    this.maxWorkers = opts.maxWorkers ?? MAX_FETCH_WORKERS;
    this.defaultForgeLookup = opts.forgeLookup;
  }

  /**
   * Returns a deep copy of the cached GitInfo (with any cached PR/CI merged in)
   * for `cwd`, or `null` if none is cached yet. Never blocks on I/O.
   */
  getCached(cwd: string): GitInfo | null {
    const key = cwd.trim();
    if (!key) {
      return null;
    }
    const entry = this.gitCache.get(key);
    if (!entry || !entry.info) {
      return null;
    }
    return this.mergeCachedPR(entry.info);
  }

  /**
   * Populates `space.git` and `pane.git` from the in-memory cache without
   * running any subprocesses.
   */
  attachCached(spaces: Space[]): void {
    for (const sp of spaces) {
      if (sp.cwd) {
        const cached = this.getCached(sp.cwd);
        if (cached) {
          sp.git = cached;
        }
      }
      for (const pane of sp.panes) {
        if (pane.cwd) {
          const cached = this.getCached(pane.cwd);
          if (cached) {
            pane.git = cached;
          }
        }
      }
    }
  }

  /**
   * Clears cached entries for a specific `cwd` or the entire collector cache.
   */
  invalidate(cwd?: string): void {
    if (cwd !== undefined) {
      this.gitCache.delete(cwd.trim());
      return;
    }
    this.gitCache.clear();
    this.prCache.clear();
    this.repoErrCache.clear();
  }

  /**
   * Collects fresh local git status for any unique CWD whose cached entry is
   * missing or stale (or all CWDs when `force` is true). Merges any cached
   * PR/CI summaries and returns the resulting CWD -> GitInfo map.
   */
  async fetchGit(
    cwds: readonly string[],
    force = false,
  ): Promise<Map<string, GitInfo>> {
    const unique = dedupeStrings(cwds);
    const result = new Map<string, GitInfo>();
    if (unique.length === 0) {
      return result;
    }

    const nowMs = this.now();
    const toFetch: string[] = [];

    for (const dir of unique) {
      if (!force) {
        const entry = this.gitCache.get(dir);
        if (entry && nowMs - entry.fetchedAt < this.gitTtlMs) {
          if (entry.info) {
            result.set(dir, this.mergeCachedPR(entry.info));
          }
          continue;
        }
      }
      toFetch.push(dir);
    }

    if (toFetch.length === 0) {
      return result;
    }

    const fetched = new Map<string, GitInfo | null>();
    await runPool(toFetch, this.maxWorkers, async (dir) => {
      try {
        const info = await this.driver.getStatus(dir);
        fetched.set(dir, info);
      } catch {
        fetched.set(dir, null);
      }
    });

    const doneAt = this.now();
    for (const [dir, info] of fetched) {
      this.gitCache.set(dir, { info, fetchedAt: doneAt });
      if (info) {
        result.set(dir, this.mergeCachedPR(info));
      }
    }

    return result;
  }

  /**
   * Queries forge PR/CI status for all repos/branches present in `gitByCwd`
   * whose PR cache entry is missing or stale (or all when `force` is true).
   * Accepts an optional `forgeLookup` callback (falling back to the constructor
   * option). Returns a fresh CWD -> GitInfo map with PR/CI attached.
   */
  async fetchForge(
    gitByCwd: ReadonlyMap<string, GitInfo> | Record<string, GitInfo>,
    force = false,
    forgeLookup?: ForgeLookupFn,
  ): Promise<Map<string, GitInfo>> {
    const entries: Array<[string, GitInfo]> =
      gitByCwd instanceof Map
        ? Array.from(gitByCwd.entries())
        : Object.entries(gitByCwd);

    const out = new Map<string, GitInfo>();
    if (entries.length === 0) {
      return out;
    }

    const lookup = forgeLookup ?? this.defaultForgeLookup;
    if (lookup) {
      const nowMs = this.now();
      const pending = new Map<
        string,
        { repoKey: string; repoRoot: string; branch: string }
      >();

      for (const [, info] of entries) {
        if (!info || !info.repo_key || !info.repo_root || !info.branch) {
          continue;
        }
        const key = prCacheKey(info.repo_key, info.branch);
        if (!force) {
          const cached = this.prCache.get(key);
          if (cached && nowMs - cached.fetchedAt < this.prTtlMs) {
            continue;
          }
          const errAt = this.repoErrCache.get(info.repo_key);
          if (errAt !== undefined && nowMs - errAt < this.repoErrTtlMs) {
            continue;
          }
        }
        if (!pending.has(key)) {
          pending.set(key, {
            repoKey: info.repo_key,
            repoRoot: info.repo_root,
            branch: info.branch,
          });
        }
      }

      const jobs = Array.from(pending.values()).sort((a, b) => {
        const rk = a.repoKey.localeCompare(b.repoKey);
        return rk !== 0 ? rk : a.branch.localeCompare(b.branch);
      });

      await runPool(jobs, this.maxWorkers, async (job) => {
        try {
          const res = await lookup(job.repoRoot, job.branch);
          this.repoErrCache.delete(job.repoKey);
          this.prCache.set(prCacheKey(job.repoKey, job.branch), {
            pr: res?.pr ? { ...res.pr } : null,
            checks: res?.checks ? { ...res.checks } : null,
            fetchedAt: this.now(),
          });
        } catch {
          this.repoErrCache.set(job.repoKey, this.now());
        }
      });
    }

    for (const [cwd, info] of entries) {
      if (info) {
        out.set(cwd, this.mergeCachedPR(info));
      }
    }
    return out;
  }

  private mergeCachedPR(base: GitInfo): GitInfo {
    const cp = cloneGitInfo(base);
    if (!cp.repo_key || !cp.branch) {
      return cp;
    }
    const cached = this.prCache.get(prCacheKey(cp.repo_key, cp.branch));
    if (cached) {
      if (cached.pr) {
        cp.pr = { ...cached.pr };
      }
      if (cached.checks) {
        cp.checks = { ...cached.checks };
      }
    }
    return cp;
  }
}

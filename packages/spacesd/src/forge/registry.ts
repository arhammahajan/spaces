import type { ForgeKind, ForgeRepoRef } from "@spaces/types";
import { ForgejoProvider, type ReadFileFn } from "./forgejo";
import { GitHubProvider } from "./github";
import { GitLabProvider } from "./gitlab";
import {
  RequestPolicy,
  TokenCache,
  type ExecCliFn,
  type FetchFn,
} from "./http";
import type { ForgeProvider, GitRemoteHost } from "./types";

const SCP_WITH_USER = /^[a-zA-Z0-9._-]+@([^:/]+):(.+)$/u;
// Userless SCP like `github.com:owner/repo.git`, excluding single-letter Windows drive paths (`C:\foo`).
const SCP_USERLESS = /^([a-zA-Z0-9.-]{2,}):([^/\\].*)$/u;
const REFINE_TIMEOUT_MS = 5_000;

function stripGitSuffix(path: string): string {
  const cleaned = path.replace(/^\/+|\/+$/gu, "");
  return cleaned.endsWith(".git") ? cleaned.slice(0, -4) : cleaned;
}

/**
 * Parses SCP (`git@host:owner/repo.git`), `https://`, `http://`, and `ssh://` git remotes.
 * - Preserves port in `host` for HTTP/HTTPS remotes; strips port for SSH remotes.
 * - Strips `.git` suffix and leading/trailing slashes.
 * - Preserves nested GitLab groups (`a/b/c`) and Forgejo subpath mounts (`git/owner/repo`).
 */
export function parseGitRemote(remoteUrl: string): GitRemoteHost | null {
  const trimmed = remoteUrl.trim();
  if (trimmed.length === 0) return null;

  if (/^(?:https?|ssh):\/\//iu.test(trimmed)) {
    let url: URL;
    try {
      url = new URL(trimmed);
    } catch {
      return null;
    }
    const isSsh = url.protocol.toLowerCase() === "ssh:";
    const hostname = url.hostname.toLowerCase();
    const host = (isSsh ? hostname : url.host).toLowerCase();
    const path = stripGitSuffix(url.pathname);
    if (!host || !hostname || !path) return null;
    return { remoteUrl: trimmed, host, hostname, path };
  }

  const scpMatch = SCP_WITH_USER.exec(trimmed) ?? SCP_USERLESS.exec(trimmed);
  if (scpMatch?.[1] && scpMatch[2]) {
    const rawHost = scpMatch[1].toLowerCase();
    const path = stripGitSuffix(scpMatch[2]);
    if (!rawHost || !path) return null;
    const hostname = rawHost.replace(/:\d+$/u, "");
    return {
      remoteUrl: trimmed,
      host: hostname,
      hostname,
      path,
    };
  }

  return null;
}

export interface ForgeRegistryOptions {
  readonly refineTimeoutMs?: number;
}

interface RefinementCacheEntry {
  readonly repo: ForgeRepoRef | null;
}

export class ForgeRegistry {
  private readonly providers: Array<ForgeProvider> = [];
  private readonly byKind = new Map<ForgeKind, ForgeProvider>();
  private readonly refinementCache = new Map<string, RefinementCacheEntry>();
  private readonly refineTimeoutMs: number;

  constructor(opts: ForgeRegistryOptions = {}) {
    this.refineTimeoutMs = opts.refineTimeoutMs ?? REFINE_TIMEOUT_MS;
  }

  register(provider: ForgeProvider): this {
    if (this.byKind.has(provider.kind)) {
      throw new Error(`Forge provider '${provider.kind}' is already registered.`);
    }
    this.providers.push(provider);
    this.byKind.set(provider.kind, provider);
    return this;
  }

  get(kind: ForgeKind): ForgeProvider | undefined {
    return this.byKind.get(kind);
  }

  list(): ReadonlyArray<ForgeProvider> {
    return [...this.providers];
  }

  clearCache(): void {
    this.refinementCache.clear();
  }

  /**
   * Phase 1: synchronous static recognition across registered providers in order.
   */
  recognizeRemoteSync(
    remoteUrl: string,
  ): { readonly provider: ForgeProvider; readonly repo: ForgeRepoRef } | null {
    const parsed = parseGitRemote(remoteUrl);
    if (!parsed) return null;
    for (const provider of this.providers) {
      const repo = provider.definition.recognizeRemote(parsed);
      if (repo !== null) {
        return { provider, repo };
      }
    }
    return null;
  }

  /**
   * Two-phase repository resolution:
   * - Phase 1: synchronous static `recognizeRemote` across registered providers.
   * - Phase 2: parallel async `refineUnknownRemote` probes with timeout;
   *   caches negative `"unknown"` ONLY when all probes definitively settled without error/timeout.
   */
  async resolveRepo(
    cwd: string,
    remoteUrl: string,
    signal?: AbortSignal,
  ): Promise<ForgeRepoRef | null> {
    const parsed = parseGitRemote(remoteUrl);
    if (!parsed) return null;

    // Phase 1: static recognition
    for (const provider of this.providers) {
      const repo = provider.definition.recognizeRemote(parsed);
      if (repo !== null) {
        return repo;
      }
    }

    // Phase 2: async refinement
    const cacheKey = `${parsed.host}\0${parsed.path}`;
    const cached = this.refinementCache.get(cacheKey);
    if (cached !== undefined) {
      return cached.repo;
    }

    const refiners = this.providers.filter(
      (p): p is ForgeProvider & {
        readonly refineUnknownRemote: NonNullable<ForgeProvider["refineUnknownRemote"]>;
      } => typeof p.refineUnknownRemote === "function",
    );

    if (refiners.length === 0) {
      this.refinementCache.set(cacheKey, { repo: null });
      return null;
    }

    const controller = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort(new Error(`refineUnknownRemote timed out after ${this.refineTimeoutMs}ms`));
    }, this.refineTimeoutMs);

    const onCallerAbort = () => controller.abort(signal?.reason);
    if (signal) {
      if (signal.aborted) {
        clearTimeout(timer);
        return null;
      }
      signal.addEventListener("abort", onCallerAbort, { once: true });
    }

    try {
      const settled = await Promise.allSettled(
        refiners.map((provider) =>
          provider.refineUnknownRemote({
            cwd,
            remote: parsed,
            signal: controller.signal,
          }),
        ),
      );

      let matched: ForgeRepoRef | null = null;
      let allDefinitivelySettled = !timedOut && !signal?.aborted;

      for (const outcome of settled) {
        if (outcome.status === "fulfilled") {
          if (outcome.value !== null && matched === null) {
            matched = outcome.value;
          }
        } else {
          allDefinitivelySettled = false;
        }
      }

      if (matched !== null) {
        this.refinementCache.set(cacheKey, { repo: matched });
        return matched;
      }

      if (allDefinitivelySettled) {
        this.refinementCache.set(cacheKey, { repo: null });
      }
      return null;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onCallerAbort);
    }
  }
}

export interface DefaultForgeRegistryOptions {
  readonly policy?: RequestPolicy;
  readonly tokenCache?: TokenCache;
  readonly fetchFn?: FetchFn;
  readonly execCliFn?: ExecCliFn;
  readonly readFileFn?: ReadFileFn;
  readonly nowFn?: () => number;
  readonly env?: Record<string, string | undefined>;
  readonly tokens?: Partial<Record<ForgeKind, Record<string, string>>>;
  readonly platform?: string;
  readonly home?: string;
  readonly refineTimeoutMs?: number;
}

export function createDefaultForgeRegistry(
  opts: DefaultForgeRegistryOptions = {},
): ForgeRegistry {
  const policy =
    opts.policy ??
    new RequestPolicy({
      fetchFn: opts.fetchFn,
      nowFn: opts.nowFn,
    });
  const tokenCache =
    opts.tokenCache ??
    new TokenCache({
      nowFn: opts.nowFn,
    });

  const registry = new ForgeRegistry({
    refineTimeoutMs: opts.refineTimeoutMs,
  });

  registry.register(
    new GitHubProvider({
      policy,
      tokenCache,
      execCliFn: opts.execCliFn,
      env: opts.env,
      tokens: opts.tokens?.github,
    }),
  );

  registry.register(
    new GitLabProvider({
      policy,
      tokenCache,
      execCliFn: opts.execCliFn,
      env: opts.env,
      tokens: opts.tokens?.gitlab,
    }),
  );

  registry.register(
    new ForgejoProvider({
      policy,
      tokenCache,
      execCliFn: opts.execCliFn,
      readFileFn: opts.readFileFn,
      env: opts.env,
      tokens: opts.tokens?.forgejo,
      platform: opts.platform,
      home: opts.home,
    }),
  );

  return registry;
}

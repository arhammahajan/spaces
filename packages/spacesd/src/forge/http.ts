import { createHash } from "node:crypto";
import type { ForgeKind } from "@spaces/types";
import { ForgeError } from "./types";

export type FetchFn = (
  input: string | URL | Request,
  init?: RequestInit,
) => Promise<Response>;

export interface ExecCliInput {
  readonly command: string;
  readonly args: ReadonlyArray<string>;
  readonly cwd?: string;
  readonly env?: Record<string, string | undefined>;
  readonly timeoutMs?: number;
  readonly signal?: AbortSignal;
}

export interface ExecCliResult {
  readonly stdout: string;
  readonly stderr: string;
  readonly exitCode: number;
  readonly errorCode?: string;
}

export type ExecCliFn = (input: ExecCliInput) => Promise<ExecCliResult>;

export async function defaultExecCli(input: ExecCliInput): Promise<ExecCliResult> {
  const timeoutMs = input.timeoutMs ?? 10_000;
  const mergedEnv: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v !== undefined) {
      mergedEnv[k] = v;
    }
  }
  if (input.env) {
    for (const [k, v] of Object.entries(input.env)) {
      if (v === undefined) {
        delete mergedEnv[k];
      } else {
        mergedEnv[k] = v;
      }
    }
  }

  let proc: ReturnType<typeof Bun.spawn>;
  try {
    proc = Bun.spawn([input.command, ...input.args], {
      cwd: input.cwd,
      env: mergedEnv,
      stdout: "pipe",
      stderr: "pipe",
    });
  } catch (error) {
    const code =
      error && typeof error === "object" && "code" in error && typeof error.code === "string"
        ? error.code
        : "ENOENT";
    return {
      stdout: "",
      stderr: error instanceof Error ? error.message : String(error),
      exitCode: 127,
      errorCode: code,
    };
  }

  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    try {
      proc.kill();
    } catch {
      // ignore
    }
  }, timeoutMs);

  const onAbort = () => {
    try {
      proc.kill();
    } catch {
      // ignore
    }
  };
  input.signal?.addEventListener("abort", onAbort, { once: true });

  try {
    const [stdout, stderr, exitCode] = await Promise.all([
      proc.stdout && typeof proc.stdout !== "number"
        ? new Response(proc.stdout).text()
        : Promise.resolve(""),
      proc.stderr && typeof proc.stderr !== "number"
        ? new Response(proc.stderr).text()
        : Promise.resolve(""),
      proc.exited,
    ]);
    return {
      stdout,
      stderr: timedOut ? `${stderr}\nCommand timed out after ${timeoutMs}ms`.trim() : stderr,
      exitCode: timedOut ? 124 : exitCode,
      ...(timedOut ? { errorCode: "ETIMEDOUT" } : {}),
    };
  } finally {
    clearTimeout(timer);
    input.signal?.removeEventListener("abort", onAbort);
  }
}

const DNS_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u;
const IPV6_LITERAL = /^\[[0-9a-f:.]+\]$/iu;

/**
 * Strictly validates a host (`hostname` or `hostname:port` or `[ipv6]:port`) before
 * interpolating it into any API URL.
 */
export function validateHost(host: string, provider: ForgeKind = "unknown"): string {
  const trimmed = host.trim().toLowerCase();
  if (
    trimmed.length === 0 ||
    trimmed.length > 255 ||
    /[\s/?#@\\]/u.test(trimmed) ||
    /[\u0000-\u001f\u007f]/u.test(trimmed)
  ) {
    throw new ForgeError({
      reason: "failed",
      provider,
      message: `Invalid forge host: "${host}"`,
    });
  }

  let hostname = trimmed;
  let portStr: string | undefined;

  if (trimmed.startsWith("[")) {
    const closeIdx = trimmed.indexOf("]");
    if (closeIdx < 0) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Invalid IPv6 forge host: "${host}"`,
      });
    }
    hostname = trimmed.slice(0, closeIdx + 1);
    const rest = trimmed.slice(closeIdx + 1);
    if (rest.length > 0) {
      if (!rest.startsWith(":")) {
        throw new ForgeError({
          reason: "failed",
          provider,
          message: `Invalid IPv6 forge host port: "${host}"`,
        });
      }
      portStr = rest.slice(1);
    }
    if (!IPV6_LITERAL.test(hostname)) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Invalid IPv6 forge host: "${host}"`,
      });
    }
  } else {
    const colonIdx = trimmed.indexOf(":");
    if (colonIdx >= 0) {
      if (trimmed.indexOf(":", colonIdx + 1) >= 0) {
        throw new ForgeError({
          reason: "failed",
          provider,
          message: `Invalid forge host: "${host}"`,
        });
      }
      hostname = trimmed.slice(0, colonIdx);
      portStr = trimmed.slice(colonIdx + 1);
    }

    if (hostname.length === 0 || hostname.includes("..")) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Invalid forge hostname: "${host}"`,
      });
    }

    const labels = hostname.split(".");
    if (!labels.every((label) => DNS_LABEL.test(label))) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Invalid forge hostname: "${host}"`,
      });
    }
  }

  if (portStr !== undefined) {
    if (!/^\d{1,5}$/u.test(portStr)) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Invalid forge port: "${host}"`,
      });
    }
    const port = Number(portStr);
    if (!Number.isSafeInteger(port) || port < 1 || port > 65535) {
      throw new ForgeError({
        reason: "failed",
        provider,
        message: `Out-of-range forge port: "${host}"`,
      });
    }
  }

  return portStr !== undefined ? `${hostname}:${portStr}` : hostname;
}

export function sha256Hex(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

export interface TokenCacheEntry {
  readonly token: string | null;
  readonly fingerprint: string | null;
}

interface InternalTokenEntry extends TokenCacheEntry {
  readonly expiresAt: number;
}

export interface TokenCacheOptions {
  readonly positiveTtlMs?: number;
  readonly negativeTtlMs?: number;
  readonly nowFn?: () => number;
}

const DEFAULT_POSITIVE_TOKEN_TTL_MS = 5 * 60 * 1_000;
const DEFAULT_NEGATIVE_TOKEN_TTL_MS = 10 * 1_000;

export class TokenCache {
  private readonly entries = new Map<string, InternalTokenEntry>();
  private readonly positiveTtlMs: number;
  private readonly negativeTtlMs: number;
  private readonly nowFn: () => number;

  constructor(opts: TokenCacheOptions = {}) {
    this.positiveTtlMs = opts.positiveTtlMs ?? DEFAULT_POSITIVE_TOKEN_TTL_MS;
    this.negativeTtlMs = opts.negativeTtlMs ?? DEFAULT_NEGATIVE_TOKEN_TTL_MS;
    this.nowFn = opts.nowFn ?? (() => Date.now());
  }

  get(key: string): TokenCacheEntry | undefined {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    if (entry.expiresAt <= this.nowFn()) {
      this.entries.delete(key);
      return undefined;
    }
    return { token: entry.token, fingerprint: entry.fingerprint };
  }

  set(key: string, token: string | null): TokenCacheEntry {
    const normalized = token !== null && token.trim().length > 0 ? token.trim() : null;
    const fingerprint = normalized !== null ? sha256Hex(normalized) : null;
    const ttl = normalized !== null ? this.positiveTtlMs : this.negativeTtlMs;
    this.entries.set(key, {
      token: normalized,
      fingerprint,
      expiresAt: this.nowFn() + ttl,
    });
    return { token: normalized, fingerprint };
  }

  async getOrLoad(
    key: string,
    loader: () => Promise<string | null>,
  ): Promise<TokenCacheEntry> {
    const cached = this.get(key);
    if (cached !== undefined) {
      return cached;
    }
    const token = await loader();
    return this.set(key, token);
  }

  invalidate(key: string): void {
    this.entries.delete(key);
  }

  clear(): void {
    this.entries.clear();
  }
}

class HostSemaphore {
  private readonly maxConcurrent: number;
  private readonly active = new Map<string, number>();
  private readonly queues = new Map<string, Array<() => void>>();

  constructor(maxConcurrent: number) {
    this.maxConcurrent = Math.max(1, maxConcurrent);
  }

  async acquire(host: string): Promise<void> {
    const current = this.active.get(host) ?? 0;
    if (current < this.maxConcurrent) {
      this.active.set(host, current + 1);
      return;
    }
    await new Promise<void>((resolve) => {
      const queue = this.queues.get(host) ?? [];
      queue.push(resolve);
      this.queues.set(host, queue);
    });
  }

  release(host: string): void {
    const queue = this.queues.get(host);
    if (queue && queue.length > 0) {
      const next = queue.shift()!;
      if (queue.length === 0) {
        this.queues.delete(host);
      }
      next();
      return;
    }
    const current = this.active.get(host) ?? 0;
    if (current <= 1) {
      this.active.delete(host);
    } else {
      this.active.set(host, current - 1);
    }
  }
}

interface RateLimitState {
  attempt: number;
  retryAtMs: number;
}

const FALLBACK_COOLDOWN_MS = 30_000;
const MAX_FALLBACK_COOLDOWN_MS = 15 * 60 * 1_000;
const MAX_REDIRECTS = 5;
const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const RATE_LIMIT_BODY_PATTERN =
  /rate\s*limit|abuse\s*detection|secondary\s*rate\s*limit|too\s*many\s*requests/iu;

export function parseRetryAtMs(headers: Headers, nowMs: number): number | undefined {
  const retryAfter = headers.get("retry-after")?.trim();
  if (retryAfter) {
    if (/^\d+$/u.test(retryAfter)) {
      const seconds = Number(retryAfter);
      const retryAt = nowMs + seconds * 1_000;
      if (Number.isSafeInteger(retryAt)) {
        return retryAt;
      }
    } else {
      const parsed = Date.parse(retryAfter);
      if (Number.isFinite(parsed) && parsed > nowMs) {
        return parsed;
      }
    }
  }

  const rateLimitReset = headers.get("x-ratelimit-reset")?.trim();
  if (rateLimitReset && /^\d+$/u.test(rateLimitReset)) {
    const epochSeconds = Number(rateLimitReset);
    const retryAt = epochSeconds * 1_000;
    if (Number.isSafeInteger(retryAt) && retryAt > nowMs) {
      return retryAt;
    }
  }

  return undefined;
}

export interface RequestPolicyOptions {
  readonly maxConcurrentPerHost?: number;
  readonly fetchFn?: FetchFn;
  readonly nowFn?: () => number;
}

export interface PolicyFetchInput {
  readonly provider: ForgeKind;
  readonly host: string;
  readonly url: string;
  readonly init?: RequestInit;
  readonly credentialScope?: string;
}

export class RequestPolicy {
  private readonly semaphore: HostSemaphore;
  private readonly fetchFn: FetchFn;
  private readonly nowFn: () => number;
  private readonly rateLimits = new Map<string, RateLimitState>();

  constructor(opts: RequestPolicyOptions = {}) {
    this.semaphore = new HostSemaphore(opts.maxConcurrentPerHost ?? 8);
    this.fetchFn = opts.fetchFn ?? ((input, init) => fetch(input, init));
    this.nowFn = opts.nowFn ?? (() => Date.now());
  }

  private rateLimitKey(provider: ForgeKind, host: string, credentialScope = ""): string {
    return `${provider}\0${host.trim().toLowerCase()}\0${credentialScope}`;
  }

  checkRateLimit(provider: ForgeKind, host: string, credentialScope = ""): void {
    const key = this.rateLimitKey(provider, host, credentialScope);
    const state = this.rateLimits.get(key);
    if (state && state.retryAtMs > this.nowFn()) {
      throw new ForgeError({
        reason: "rate-limited",
        provider,
        retryAtMs: state.retryAtMs,
        message: `${provider} requests to ${host} are rate-limited until ${new Date(state.retryAtMs).toISOString()}.`,
      });
    }
  }

  recordRateLimit(
    provider: ForgeKind,
    host: string,
    retryAtMs?: number,
    credentialScope = "",
  ): number {
    const now = this.nowFn();
    const key = this.rateLimitKey(provider, host, credentialScope);
    const previous = this.rateLimits.get(key);
    const attempt = (previous?.attempt ?? 0) + 1;
    const fallbackMs = Math.min(
      FALLBACK_COOLDOWN_MS * 2 ** Math.max(0, attempt - 1),
      MAX_FALLBACK_COOLDOWN_MS,
    );
    const proposed = retryAtMs !== undefined && retryAtMs > now ? retryAtMs : now + fallbackMs;
    const effective =
      previous && previous.retryAtMs > now ? Math.max(previous.retryAtMs, proposed) : proposed;
    this.rateLimits.set(key, { attempt, retryAtMs: effective });
    return effective;
  }

  recordSuccess(provider: ForgeKind, host: string, credentialScope = ""): void {
    const key = this.rateLimitKey(provider, host, credentialScope);
    const previous = this.rateLimits.get(key);
    if (previous && previous.retryAtMs <= this.nowFn()) {
      this.rateLimits.delete(key);
    }
  }

  /**
   * Executes an HTTP request under per-host concurrency limiting, rate-limit tracking,
   * and cross-origin redirect credential stripping.
   */
  async fetch(input: PolicyFetchInput): Promise<Response> {
    const validatedHost = validateHost(input.host, input.provider);
    const scope = input.credentialScope ?? "";
    this.checkRateLimit(input.provider, validatedHost, scope);

    await this.semaphore.acquire(validatedHost);
    try {
      // Re-check after waiting for a concurrency slot in case another request triggered a pause.
      this.checkRateLimit(input.provider, validatedHost, scope);

      let currentUrl = input.url;
      let currentInit: RequestInit = {
        ...input.init,
        redirect: "manual",
      };

      for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
        const currentOrigin = new URL(currentUrl).origin;
        const response = await this.fetchFn(currentUrl, currentInit);

        if (REDIRECT_STATUSES.has(response.status)) {
          const location = response.headers.get("location");
          if (!location || hop === MAX_REDIRECTS) {
            return response;
          }
          const nextUrl = new URL(location, currentUrl);
          const headers = new Headers(currentInit.headers);
          if (nextUrl.origin !== currentOrigin) {
            headers.delete("authorization");
            headers.delete("private-token");
          }
          currentUrl = nextUrl.toString();
          currentInit = {
            ...currentInit,
            headers,
            redirect: "manual",
            ...(response.status === 303 ? { method: "GET", body: undefined } : {}),
          };
          continue;
        }

        if (response.status === 429) {
          const retryAtMs = this.recordRateLimit(
            input.provider,
            validatedHost,
            parseRetryAtMs(response.headers, this.nowFn()),
            scope,
          );
          throw new ForgeError({
            reason: "rate-limited",
            provider: input.provider,
            statusCode: 429,
            retryAtMs,
            message: `${input.provider} API rate limit exceeded on ${validatedHost}.`,
          });
        }

        if (response.status === 403) {
          const remaining = response.headers.get("x-ratelimit-remaining")?.trim();
          let isRateLimit = remaining === "0";
          if (!isRateLimit) {
            const bodyText = await response.clone().text().catch(() => "");
            isRateLimit = RATE_LIMIT_BODY_PATTERN.test(bodyText);
          }
          if (isRateLimit) {
            const retryAtMs = this.recordRateLimit(
              input.provider,
              validatedHost,
              parseRetryAtMs(response.headers, this.nowFn()),
              scope,
            );
            throw new ForgeError({
              reason: "rate-limited",
              provider: input.provider,
              statusCode: 403,
              retryAtMs,
              message: `${input.provider} API rate limit exceeded on ${validatedHost}.`,
            });
          }
        }

        if (response.ok) {
          this.recordSuccess(input.provider, validatedHost, scope);
        }
        return response;
      }

      throw new ForgeError({
        reason: "failed",
        provider: input.provider,
        message: `Too many redirects fetching ${input.url}`,
      });
    } finally {
      this.semaphore.release(validatedHost);
    }
  }

  /**
   * Inspects GraphQL errors for `RATE_LIMITED` and pauses the host if detected.
   */
  handleGraphQLErrors(
    provider: ForgeKind,
    host: string,
    errors: ReadonlyArray<{ readonly type?: string; readonly code?: string; readonly message?: string }>,
    headers?: Headers,
    credentialScope = "",
  ): void {
    const rateLimited = errors.some(
      (err) =>
        err.type?.toUpperCase() === "RATE_LIMITED" ||
        err.code?.toUpperCase() === "RATE_LIMITED" ||
        (err.message ? RATE_LIMIT_BODY_PATTERN.test(err.message) : false),
    );
    if (rateLimited) {
      const validatedHost = validateHost(host, provider);
      const retryAtMs = this.recordRateLimit(
        provider,
        validatedHost,
        headers ? parseRetryAtMs(headers, this.nowFn()) : undefined,
        credentialScope,
      );
      throw new ForgeError({
        reason: "rate-limited",
        provider,
        retryAtMs,
        message: `${provider} GraphQL rate limit exceeded on ${validatedHost}.`,
      });
    }
  }
}

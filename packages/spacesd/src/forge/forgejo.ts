import { readFile } from "node:fs/promises";
import { homedir } from "node:os";
import * as NodePath from "node:path";
import type {
  ChangeRequestSummary,
  CheckRunDetail,
  CheckState,
  ForgeRepoRef,
} from "@spaces/types";
import {
  defaultExecCli,
  RequestPolicy,
  TokenCache,
  validateHost,
  type ExecCliFn,
} from "./http";
import {
  aggregateCheckRuns,
  dedupeCheckRuns,
  ForgeError,
  type ForgeCapabilities,
  type ForgeDefinition,
  type ForgeProvider,
  type GitRemoteHost,
  type ListChangeRequestsInput,
  type ParsedChangeRequestUrl,
  type TimestampedCheckRun,
} from "./types";

const CHANGE_REQUEST_PATH = /^\/([^/]+(?:\/[^/]+)+)\/pulls\/(\d+)(?:\/|$)/u;

export function isForgejoHost(hostname: string): boolean {
  const host = hostname.toLowerCase();
  return (
    host === "codeberg.org" ||
    host.split(".").includes("forgejo") ||
    host.split(".").includes("gitea")
  );
}

/**
 * Splits a Forgejo remote path into optional subpath mount + `owner/repo`.
 * Example: `git/alice/demo` -> `{ mountPath: "git", repository: "alice/demo" }`.
 */
export function splitForgejoMountAndRepo(
  remotePath: string,
  knownMountPath?: string,
): { readonly mountPath: string; readonly repository: string } | null {
  const cleanPath = remotePath.replace(/^\/+|\/+$/gu, "");
  const segments = cleanPath.split("/").filter(Boolean);
  if (segments.length < 2) return null;

  if (knownMountPath !== undefined) {
    const cleanMount = knownMountPath.replace(/^\/+|\/+$/gu, "");
    if (cleanMount.length === 0) {
      return segments.length === 2
        ? { mountPath: "", repository: segments.join("/") }
        : {
            mountPath: segments.slice(0, -2).join("/"),
            repository: segments.slice(-2).join("/"),
          };
    }
    const lowerPath = cleanPath.toLowerCase();
    const lowerMount = cleanMount.toLowerCase();
    if (!lowerPath.startsWith(`${lowerMount}/`)) return null;
    const remainder = cleanPath.slice(cleanMount.length + 1);
    const repoSegments = remainder.split("/").filter(Boolean);
    if (repoSegments.length !== 2) return null;
    return { mountPath: cleanMount, repository: repoSegments.join("/") };
  }

  return {
    mountPath: segments.slice(0, -2).join("/"),
    repository: segments.slice(-2).join("/"),
  };
}

export function forgejoKeysPaths(input: {
  readonly platform?: string;
  readonly home?: string;
  readonly dataHome?: string;
  readonly appData?: string;
}): ReadonlyArray<string> {
  const platform = input.platform ?? process.platform;
  const home = input.home ?? homedir();
  if (platform === "darwin") {
    return ["forgejo-cli", "Cyborus"].map((org) =>
      NodePath.join(
        home,
        "Library",
        "Application Support",
        `${org}.forgejo-cli`,
        "keys.json",
      ),
    );
  }
  if (platform === "win32") {
    return ["forgejo-cli", "Cyborus"].map((org) =>
      NodePath.win32.join(
        input.appData || NodePath.win32.join(home, "AppData", "Roaming"),
        org,
        "forgejo-cli",
        "data",
        "keys.json",
      ),
    );
  }
  const baseDataDir =
    input.dataHome && NodePath.isAbsolute(input.dataHome)
      ? input.dataHome
      : NodePath.join(home, ".local", "share");
  return [NodePath.join(baseDataDir, "forgejo-cli", "keys.json")];
}

export interface ForgejoKeysFile {
  readonly hosts: Record<string, { readonly type?: string; readonly token: string }>;
  readonly aliases?: Record<string, string>;
}

export interface TeaLoginEntry {
  readonly name: string;
  readonly url: string;
  readonly ssh_host?: string;
  readonly user?: string;
  readonly default?: string | boolean;
  readonly token?: string;
}

export function parseForgejoKeysJson(raw: string): ForgejoKeysFile | null {
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    const obj = parsed as Record<string, unknown>;
    if (!obj.hosts || typeof obj.hosts !== "object") return null;
    const hosts: Record<string, { type?: string; token: string }> = {};
    for (const [k, v] of Object.entries(obj.hosts as Record<string, unknown>)) {
      if (v && typeof v === "object" && typeof (v as Record<string, unknown>).token === "string") {
        const entry = v as { type?: string; token: string };
        hosts[k.toLowerCase()] = { type: entry.type, token: entry.token };
      }
    }
    const aliases: Record<string, string> = {};
    if (obj.aliases && typeof obj.aliases === "object") {
      for (const [k, v] of Object.entries(obj.aliases as Record<string, unknown>)) {
        if (typeof v === "string") {
          aliases[k.toLowerCase()] = v.toLowerCase();
        }
      }
    }
    return { hosts, aliases };
  } catch {
    return null;
  }
}

export function parseTeaLoginsJson(raw: string): ReadonlyArray<TeaLoginEntry> {
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    const results: Array<TeaLoginEntry> = [];
    for (const item of parsed) {
      if (
        item &&
        typeof item === "object" &&
        typeof (item as Record<string, unknown>).url === "string"
      ) {
        const rec = item as Record<string, unknown>;
        results.push({
          name: typeof rec.name === "string" ? rec.name : "",
          url: rec.url as string,
          ...(typeof rec.ssh_host === "string" ? { ssh_host: rec.ssh_host } : {}),
          ...(typeof rec.user === "string" ? { user: rec.user } : {}),
          ...(typeof rec.default === "string" || typeof rec.default === "boolean"
            ? { default: rec.default }
            : {}),
          ...(typeof rec.token === "string" ? { token: rec.token } : {}),
        });
      }
    }
    return results;
  } catch {
    return [];
  }
}

export const forgejoDefinition: ForgeDefinition = {
  kind: "forgejo",
  label: "Forgejo",
  publicHost: "codeberg.org",
  terminology: {
    shortLabel: "PR",
    shortLabelWithArticle: "a PR",
    singular: "pull request",
    plural: "pull requests",
  },
  recognizeRemote(remote) {
    if (!isForgejoHost(remote.hostname)) return null;
    const split = splitForgejoMountAndRepo(remote.path);
    if (!split) return null;
    const origin = /^https?:/iu.test(remote.remoteUrl)
      ? new URL(remote.remoteUrl).origin
      : `https://${remote.host}`;
    const baseUrl = split.mountPath ? `${origin}/${split.mountPath}` : origin;
    return {
      kind: "forgejo",
      host: remote.host,
      repository: split.repository,
      remote_url: remote.remoteUrl,
      base_url: baseUrl,
    };
  },
  parseChangeRequestUrl(url: URL): ParsedChangeRequestUrl | null {
    if (url.protocol !== "https:" && url.protocol !== "http:") return null;
    const match = CHANGE_REQUEST_PATH.exec(url.pathname);
    const repository = match?.[1];
    const number = Number(match?.[2]);
    if (!repository || !Number.isSafeInteger(number) || number < 1) return null;
    return {
      host: url.hostname.toLowerCase(),
      repository: repository.toLowerCase(),
      number,
      repositoryUrl: `${url.origin}/${repository}`,
      authority: url.host.toLowerCase(),
    };
  },
  changeRequestHeadRef(number: number): string {
    return `refs/pull/${number}/head`;
  },
};

export const forgejoCapabilities: ForgeCapabilities = {
  diff: true,
  comment: true,
  actions: ["merge", "close", "reopen", "update-branch"],
  mergeMethods: ["merge", "squash", "rebase"],
  review: true,
  search: false,
};

export type ReadFileFn = (path: string) => Promise<string>;

export interface ForgejoProviderOptions {
  readonly policy?: RequestPolicy;
  readonly tokenCache?: TokenCache;
  readonly execCliFn?: ExecCliFn;
  readonly readFileFn?: ReadFileFn;
  readonly env?: Record<string, string | undefined>;
  readonly tokens?: Record<string, string>;
  readonly platform?: string;
  readonly home?: string;
}

function forgejoApiBase(host: string, baseUrl?: string): string {
  if (baseUrl) {
    const parsed = new URL(baseUrl);
    validateHost(parsed.host, "forgejo");
    return `${parsed.origin}${parsed.pathname.replace(/\/+$/u, "")}/api/v1`;
  }
  const validated = validateHost(host, "forgejo");
  return `https://${validated}/api/v1`;
}

function mapForgejoCommitStatus(state?: string | null): CheckState {
  const norm = (state ?? "").trim().toLowerCase();
  switch (norm) {
    case "success":
      return "success";
    case "failure":
    case "error":
    case "warning":
      return "failure";
    case "pending":
      return "pending";
    default:
      return "none";
  }
}

function normalizeForgejoPrState(state: string, merged?: boolean): string {
  if (merged) return "MERGED";
  const lower = state.trim().toLowerCase();
  if (lower === "open") return "OPEN";
  if (lower === "closed") return "CLOSED";
  return state.toUpperCase();
}

function parseTimestampMs(value?: string | null): number | undefined {
  if (!value) return undefined;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : undefined;
}

interface RawForgejoPR {
  readonly number: number;
  readonly title: string;
  readonly state: string;
  readonly draft?: boolean;
  readonly merged?: boolean;
  readonly html_url: string;
  readonly updated_at?: string | null;
  readonly user?: { readonly login?: string; readonly username?: string } | null;
  readonly head?: { readonly ref?: string; readonly sha?: string } | null;
  readonly base?: { readonly ref?: string } | null;
}

function normalizeFjHostKey(rawKey: string): { host: string; mountPath: string; origin?: string } {
  const trimmed = rawKey.trim().toLowerCase().replace(/\/+$/u, "");
  if (/^https?:\/\//iu.test(trimmed)) {
    try {
      const parsed = new URL(trimmed);
      return {
        host: parsed.host.toLowerCase(),
        mountPath: parsed.pathname.replace(/^\/+|\/+$/gu, ""),
        origin: parsed.origin,
      };
    } catch {
      // fall through
    }
  }
  const slashIdx = trimmed.indexOf("/");
  if (slashIdx >= 0) {
    return {
      host: trimmed.slice(0, slashIdx),
      mountPath: trimmed.slice(slashIdx + 1).replace(/^\/+|\/+$/gu, ""),
    };
  }
  return { host: trimmed, mountPath: "" };
}

export class ForgejoProvider implements ForgeProvider {
  readonly kind = "forgejo" as const;
  readonly definition = forgejoDefinition;
  readonly capabilities = forgejoCapabilities;

  private readonly policy: RequestPolicy;
  private readonly tokenCache: TokenCache;
  private readonly execCliFn: ExecCliFn;
  private readonly readFileFn: ReadFileFn;
  private readonly env: Record<string, string | undefined>;
  private readonly explicitTokens: Map<string, string>;
  private readonly platform?: string;
  private readonly home?: string;

  constructor(opts: ForgejoProviderOptions = {}) {
    this.policy = opts.policy ?? new RequestPolicy();
    this.tokenCache = opts.tokenCache ?? new TokenCache();
    this.execCliFn = opts.execCliFn ?? defaultExecCli;
    this.readFileFn = opts.readFileFn ?? ((p) => readFile(p, "utf8"));
    this.env = opts.env ?? process.env;
    this.platform = opts.platform;
    this.home = opts.home;
    this.explicitTokens = new Map();
    if (opts.tokens) {
      for (const [k, v] of Object.entries(opts.tokens)) {
        if (v.trim().length > 0) {
          this.explicitTokens.set(k.trim().toLowerCase(), v.trim());
        }
      }
    }
  }

  private async loadFjKeys(): Promise<ForgejoKeysFile | null> {
    const paths = forgejoKeysPaths({
      platform: this.platform,
      home: this.home,
      dataHome: this.env.XDG_DATA_HOME,
      appData: this.env.APPDATA,
    });
    for (const filePath of paths) {
      try {
        const content = await this.readFileFn(filePath);
        const parsed = parseForgejoKeysJson(content);
        if (parsed) return parsed;
      } catch {
        // try next path
      }
    }
    return null;
  }

  private async loadTeaLogins(
    cwd?: string,
    signal?: AbortSignal,
  ): Promise<ReadonlyArray<TeaLoginEntry>> {
    const res = await this.execCliFn({
      command: "tea",
      args: ["login", "list", "--output", "json"],
      cwd,
      timeoutMs: 4_500,
      signal,
    });
    if (res.exitCode !== 0) {
      return [];
    }
    return parseTeaLoginsJson(res.stdout);
  }

  /**
   * Probes `fj` `keys.json` and `tea login list --output json` to recognize self-hosted
   * Forgejo/Gitea servers on arbitrary hostnames, ports, and subpath mounts.
   */
  async refineUnknownRemote(input: {
    readonly cwd: string;
    readonly remote: GitRemoteHost;
    readonly signal?: AbortSignal;
  }): Promise<ForgeRepoRef | null> {
    const isHttpRemote = /^https?:/iu.test(input.remote.remoteUrl);
    const remoteOrigin = isHttpRemote
      ? new URL(input.remote.remoteUrl).origin
      : `https://${input.remote.host}`;

    // 1. Check fj keys.json
    const keys = await this.loadFjKeys();
    if (keys) {
      for (const hostKey of Object.keys(keys.hosts)) {
        const parsed = normalizeFjHostKey(hostKey);
        const hostMatches = isHttpRemote
          ? parsed.host === input.remote.host
          : parsed.host === input.remote.host ||
            parsed.host.replace(/:\d+$/u, "") === input.remote.hostname;
        if (!hostMatches) continue;
        const split = splitForgejoMountAndRepo(input.remote.path, parsed.mountPath);
        if (!split) continue;
        const origin = parsed.origin ?? (isHttpRemote ? remoteOrigin : `https://${parsed.host}`);
        const baseUrl = split.mountPath ? `${origin}/${split.mountPath}` : origin;
        return {
          kind: "forgejo",
          host: parsed.host,
          repository: split.repository,
          remote_url: input.remote.remoteUrl,
          base_url: baseUrl,
        };
      }
    }

    // 2. Check tea login list --output json
    const logins = await this.loadTeaLogins(input.cwd, input.signal);
    for (const login of logins) {
      let loginUrl: URL;
      try {
        loginUrl = new URL(login.url);
      } catch {
        continue;
      }
      const loginHost = loginUrl.host.toLowerCase();
      const loginHostname = loginUrl.hostname.toLowerCase();
      const loginMount = loginUrl.pathname.replace(/^\/+|\/+$/gu, "");
      const sshHost = login.ssh_host?.toLowerCase();

      const hostMatches = isHttpRemote
        ? loginHost === input.remote.host
        : sshHost === input.remote.host ||
          sshHost === input.remote.hostname ||
          loginHostname === input.remote.hostname;
      if (!hostMatches) continue;

      const split = splitForgejoMountAndRepo(input.remote.path, loginMount);
      if (!split) continue;

      const baseUrl = split.mountPath
        ? `${loginUrl.origin}/${split.mountPath}`
        : loginUrl.origin;
      return {
        kind: "forgejo",
        host: loginHost,
        repository: split.repository,
        remote_url: input.remote.remoteUrl,
        base_url: baseUrl,
      };
    }

    return null;
  }

  /**
   * Host-scoped token resolution:
   * 1. Explicit config for exact host/hostname
   * 2. Host-scoped env vars (`CODEBERG_TOKEN` only for `codeberg.org`;
   *    `FORGEJO_TOKEN` only when `FORGEJO_HOST` matches; `GITEA_TOKEN` only when `GITEA_HOST` matches)
   * 3. `fj` `keys.json` matching host (or alias)
   * 4. `tea login list --output json` matching host (if token present)
   */
  async resolveToken(
    host: string,
    cwd?: string,
    signal?: AbortSignal,
  ): Promise<{ readonly token: string | null; readonly fingerprint: string | null }> {
    const validatedHost = validateHost(host, "forgejo");
    const hostname = validatedHost.replace(/:\d+$/u, "");

    const explicit =
      this.explicitTokens.get(validatedHost) ?? this.explicitTokens.get(hostname);
    if (explicit) {
      return this.tokenCache.getOrLoad(`forgejo:${validatedHost}:explicit`, async () => explicit);
    }

    if (hostname === "codeberg.org" && this.env.CODEBERG_TOKEN?.trim()) {
      const envToken = this.env.CODEBERG_TOKEN.trim();
      return this.tokenCache.getOrLoad(`forgejo:${validatedHost}:codeberg-env`, async () => envToken);
    }

    const forgejoEnvHost = this.env.FORGEJO_HOST?.trim().toLowerCase();
    if (
      forgejoEnvHost &&
      (forgejoEnvHost === validatedHost || forgejoEnvHost === hostname) &&
      this.env.FORGEJO_TOKEN?.trim()
    ) {
      const envToken = this.env.FORGEJO_TOKEN.trim();
      return this.tokenCache.getOrLoad(`forgejo:${validatedHost}:forgejo-env`, async () => envToken);
    }

    const giteaEnvHost = this.env.GITEA_HOST?.trim().toLowerCase();
    if (
      giteaEnvHost &&
      (giteaEnvHost === validatedHost || giteaEnvHost === hostname) &&
      this.env.GITEA_TOKEN?.trim()
    ) {
      const envToken = this.env.GITEA_TOKEN.trim();
      return this.tokenCache.getOrLoad(`forgejo:${validatedHost}:gitea-env`, async () => envToken);
    }

    return this.tokenCache.getOrLoad(`forgejo:${validatedHost}:discovery`, async () => {
      const keys = await this.loadFjKeys();
      if (keys) {
        const direct = keys.hosts[validatedHost] ?? keys.hosts[hostname];
        if (direct?.token) {
          return direct.token;
        }
        // Match URL-formatted keys (e.g., https://example.com:3000/git)
        for (const [k, v] of Object.entries(keys.hosts)) {
          const parsed = normalizeFjHostKey(k);
          if (parsed.host === validatedHost || parsed.host === hostname) {
            return v.token;
          }
        }
        // Match aliases
        if (keys.aliases) {
          const aliasTarget = keys.aliases[validatedHost] ?? keys.aliases[hostname];
          if (aliasTarget && keys.hosts[aliasTarget]?.token) {
            return keys.hosts[aliasTarget]!.token;
          }
        }
      }

      const logins = await this.loadTeaLogins(cwd, signal);
      for (const login of logins) {
        if (!login.token) continue;
        try {
          const parsed = new URL(login.url);
          if (
            parsed.host.toLowerCase() === validatedHost ||
            parsed.hostname.toLowerCase() === hostname
          ) {
            return login.token;
          }
        } catch {
          // ignore malformed url
        }
      }

      return null;
    });
  }

  private async requireToken(
    host: string,
    cwd?: string,
    signal?: AbortSignal,
  ): Promise<{ readonly token: string; readonly fingerprint: string }> {
    const entry = await this.resolveToken(host, cwd, signal);
    if (!entry.token || !entry.fingerprint) {
      throw new ForgeError({
        reason: "unauthenticated",
        provider: "forgejo",
        message: `No Forgejo/Gitea credentials found for ${host}. Configure 'fj' keys.json, 'tea login', or a host token.`,
      });
    }
    return { token: entry.token, fingerprint: entry.fingerprint };
  }

  private buildHeaders(token: string): Record<string, string> {
    return {
      Accept: "application/json",
      Authorization: `token ${token}`,
      "User-Agent": "spacesd",
    };
  }

  private mapHttpError(response: Response, host: string, detail: string): ForgeError {
    if (response.status === 401) {
      return new ForgeError({
        reason: "unauthenticated",
        provider: "forgejo",
        statusCode: 401,
        message: `Forgejo authentication failed on ${host}: ${detail}`,
      });
    }
    if (response.status === 403) {
      return new ForgeError({
        reason: "forbidden",
        provider: "forgejo",
        statusCode: 403,
        message: `Forgejo access forbidden on ${host}: ${detail}`,
      });
    }
    if (response.status === 404) {
      return new ForgeError({
        reason: "not-found",
        provider: "forgejo",
        statusCode: 404,
        message: `Forgejo resource not found on ${host}: ${detail}`,
      });
    }
    return new ForgeError({
      reason: "failed",
      provider: "forgejo",
      statusCode: response.status,
      message: `Forgejo API error (${response.status}) on ${host}: ${detail}`,
    });
  }

  private mapPrSummary(
    repo: ForgeRepoRef,
    pr: RawForgejoPR,
    checks: Array<CheckRunDetail> = [],
  ): ChangeRequestSummary {
    const summary = aggregateCheckRuns(checks);
    const updatedAtMs = parseTimestampMs(pr.updated_at);
    const author = pr.user?.login ?? pr.user?.username;
    return {
      provider: "forgejo",
      host: repo.host,
      repository: repo.repository,
      number: pr.number,
      title: pr.title,
      state: normalizeForgejoPrState(pr.state, pr.merged),
      is_draft: Boolean(pr.draft),
      head_branch: pr.head?.ref ?? "",
      ...(pr.base?.ref ? { base_branch: pr.base.ref } : {}),
      url: pr.html_url,
      ...(author ? { author } : {}),
      ...(summary ? { checks_state: summary.state } : {}),
      checks,
      ...(updatedAtMs !== undefined ? { updated_at_ms: updatedAtMs } : {}),
    };
  }

  async getViewer(input: {
    readonly cwd?: string;
    readonly host: string;
    readonly baseUrl?: string;
    readonly signal?: AbortSignal;
  }): Promise<string> {
    const { token, fingerprint } = await this.requireToken(input.host, input.cwd, input.signal);
    const apiBase = forgejoApiBase(input.host, input.baseUrl);
    const response = await this.policy.fetch({
      provider: "forgejo",
      host: input.host,
      url: `${apiBase}/user`,
      credentialScope: fingerprint,
      init: {
        method: "GET",
        headers: this.buildHeaders(token),
        signal: input.signal,
      },
    });
    if (!response.ok) {
      throw this.mapHttpError(response, input.host, await response.text().catch(() => ""));
    }
    const data = (await response.json()) as { login?: string; username?: string };
    const login = data.login ?? data.username;
    if (!login) {
      throw new ForgeError({
        reason: "failed",
        provider: "forgejo",
        message: `Forgejo /user response missing login on ${input.host}`,
      });
    }
    return login;
  }

  async listChangeRequests(
    input: ListChangeRequestsInput,
  ): Promise<Array<ChangeRequestSummary>> {
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );
    const apiBase = forgejoApiBase(input.repo.host, input.repo.base_url);
    const [owner, name] = splitOwnerRepo(input.repo.repository);
    const restState =
      input.state === "open"
        ? "open"
        : input.state === "closed" || input.state === "merged"
          ? "closed"
          : "all";
    const limit = Math.min(Math.max(input.limit ?? 30, 1), 50);
    const params = new URLSearchParams({
      state: restState,
      limit: String(limit),
    });

    const response = await this.policy.fetch({
      provider: "forgejo",
      host: input.repo.host,
      url: `${apiBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/pulls?${params.toString()}`,
      credentialScope: fingerprint,
      init: {
        method: "GET",
        headers: this.buildHeaders(token),
        signal: input.signal,
      },
    });
    if (!response.ok) {
      throw this.mapHttpError(response, input.repo.host, await response.text().catch(() => ""));
    }

    const rawPrs = (await response.json()) as Array<RawForgejoPR>;
    const mapped: Array<ChangeRequestSummary> = [];
    for (const pr of rawPrs) {
      if (input.branch && pr.head?.ref !== input.branch) continue;
      const state = normalizeForgejoPrState(pr.state, pr.merged);
      if (input.state === "merged" && state !== "MERGED") continue;
      if (input.state === "closed" && state !== "CLOSED") continue;
      mapped.push(this.mapPrSummary(input.repo, pr));
    }
    return mapped;
  }

  async getChangeRequestForBranch(input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branch: string;
    readonly signal?: AbortSignal;
  }): Promise<ChangeRequestSummary | null> {
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );
    const apiBase = forgejoApiBase(input.repo.host, input.repo.base_url);
    const [owner, name] = splitOwnerRepo(input.repo.repository);

    const response = await this.policy.fetch({
      provider: "forgejo",
      host: input.repo.host,
      url: `${apiBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/pulls?state=all&limit=50`,
      credentialScope: fingerprint,
      init: {
        method: "GET",
        headers: this.buildHeaders(token),
        signal: input.signal,
      },
    });
    if (!response.ok) {
      throw this.mapHttpError(response, input.repo.host, await response.text().catch(() => ""));
    }

    const rawPrs = (await response.json()) as Array<RawForgejoPR>;
    const matched = rawPrs.find((pr) => pr.head?.ref === input.branch);
    if (!matched) return null;

    let checks: Array<CheckRunDetail> = [];
    if (matched.head?.sha) {
      try {
        checks = await this.getChecks({
          cwd: input.cwd,
          repo: input.repo,
          ref: matched.head.sha,
          signal: input.signal,
        });
      } catch {
        // Statuses may be absent or disabled.
      }
    }

    return this.mapPrSummary(input.repo, matched, checks);
  }

  async getChecks(input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly ref: string;
    readonly signal?: AbortSignal;
  }): Promise<Array<CheckRunDetail>> {
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );
    const apiBase = forgejoApiBase(input.repo.host, input.repo.base_url);
    const [owner, name] = splitOwnerRepo(input.repo.repository);
    const encodedRef = encodeURIComponent(input.ref);

    const response = await this.policy.fetch({
      provider: "forgejo",
      host: input.repo.host,
      url: `${apiBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/statuses/${encodedRef}`,
      credentialScope: fingerprint,
      init: {
        method: "GET",
        headers: this.buildHeaders(token),
        signal: input.signal,
      },
    });
    if (!response.ok) {
      throw this.mapHttpError(response, input.repo.host, await response.text().catch(() => ""));
    }

    const statuses = (await response.json()) as Array<{
      context: string;
      status: string;
      description?: string | null;
      target_url?: string | null;
      updated_at?: string | null;
      created_at?: string | null;
    }>;

    const entries: Array<TimestampedCheckRun> = statuses.map((st, idx) => ({
      check: {
        name: st.context,
        status: mapForgejoCommitStatus(st.status),
        ...(st.description ? { description: st.description } : {}),
        ...(st.target_url ? { url: st.target_url } : {}),
      },
      at: st.updated_at ?? st.created_at ?? null,
      sequence: idx + 1,
    }));

    return dedupeCheckRuns(entries);
  }
}

function splitOwnerRepo(repository: string): [string, string] {
  const parts = repository.split("/").filter(Boolean);
  if (parts.length !== 2) {
    throw new ForgeError({
      reason: "failed",
      provider: "forgejo",
      message: `Invalid forgejo repository path: "${repository}"`,
    });
  }
  return [parts[0]!, parts[1]!];
}

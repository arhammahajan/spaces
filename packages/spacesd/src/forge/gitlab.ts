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

const CHANGE_REQUEST_PATH = /^\/([^/]+(?:\/[^/]+)+)\/-\/merge_requests\/(\d+)(?:\/|$)/u;
const HOST_LINE_PATTERN = /^(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?|\[[a-f0-9:.]+\])(?::\d+)?$/iu;
const LOGGED_IN_PATTERN = /Logged in to .+? as\s+([^\s(]+)/iu;

export function isGitLabHost(hostname: string): boolean {
  const host = hostname.toLowerCase();
  return host === "gitlab.com" || host.split(".").includes("gitlab");
}

function isNestedGitLabRepoPath(path: string): boolean {
  const segments = path.split("/").filter(Boolean);
  return (
    segments.length >= 2 &&
    segments.every((s) => s !== "." && s !== ".." && !/\s/u.test(s))
  );
}

export interface GitLabAuthStatusHost {
  readonly host: string;
  readonly account: string | null;
}

export function parseGitLabAuthStatusHosts(text: string): ReadonlyArray<GitLabAuthStatusHost> {
  const hosts: Array<GitLabAuthStatusHost> = [];
  let currentHost: string | null = null;
  let currentLines: Array<string> = [];

  const flush = () => {
    if (currentHost === null) return;
    const account = LOGGED_IN_PATTERN.exec(currentLines.join("\n"))?.[1]?.trim() || null;
    hosts.push({ host: currentHost, account });
    currentHost = null;
    currentLines = [];
  };

  for (const rawLine of text.split(/\r?\n/u)) {
    const line = rawLine.trim();
    if (line.length === 0) continue;
    const isHostLine =
      rawLine.length === rawLine.trimStart().length && HOST_LINE_PATTERN.test(line);
    if (isHostLine) {
      flush();
      currentHost = line.toLowerCase();
      continue;
    }
    if (currentHost !== null) {
      currentLines.push(line);
    }
  }

  flush();
  return hosts;
}

export const gitlabDefinition: ForgeDefinition = {
  kind: "gitlab",
  label: "GitLab",
  publicHost: "gitlab.com",
  terminology: {
    shortLabel: "MR",
    shortLabelWithArticle: "an MR",
    singular: "merge request",
    plural: "merge requests",
  },
  recognizeRemote(remote) {
    if (!isGitLabHost(remote.hostname)) return null;
    if (!isNestedGitLabRepoPath(remote.path)) return null;
    const baseUrl = /^https?:/iu.test(remote.remoteUrl)
      ? new URL(remote.remoteUrl).origin
      : `https://${remote.host}`;
    return {
      kind: "gitlab",
      host: remote.host,
      repository: remote.path,
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
      ...(url.port ? { authority: url.host.toLowerCase() } : {}),
    };
  },
  changeRequestHeadRef(number: number): string {
    return `refs/merge-requests/${number}/head`;
  },
};

export const gitlabCapabilities: ForgeCapabilities = {
  diff: true,
  comment: true,
  actions: [
    "merge",
    "ready",
    "draft",
    "close",
    "reopen",
    "delete-branch",
    "update-branch",
  ],
  mergeMethods: ["merge", "squash", "rebase"],
  review: true,
  search: true,
};

export interface GitLabProviderOptions {
  readonly policy?: RequestPolicy;
  readonly tokenCache?: TokenCache;
  readonly execCliFn?: ExecCliFn;
  readonly env?: Record<string, string | undefined>;
  readonly tokens?: Record<string, string>;
}

function gitlabApiBase(host: string, baseUrl?: string): string {
  if (baseUrl) {
    const parsed = new URL(baseUrl);
    validateHost(parsed.host, "gitlab");
    return `${parsed.origin}${parsed.pathname.replace(/\/+$/u, "")}/api/v4`;
  }
  const validated = validateHost(host, "gitlab");
  return `https://${validated}/api/v4`;
}

function mapGitLabStatus(status?: string | null): CheckState {
  const norm = (status ?? "").trim().toLowerCase();
  switch (norm) {
    case "success":
      return "success";
    case "failed":
    case "canceled":
    case "cancelled":
      return "failure";
    case "running":
    case "pending":
    case "created":
    case "waiting_for_resource":
    case "preparing":
    case "scheduled":
      return "pending";
    case "skipped":
    case "manual":
    default:
      return "none";
  }
}

function normalizeGitLabMrState(state: string): string {
  const lower = state.trim().toLowerCase();
  if (lower === "opened" || lower === "open") return "OPEN";
  if (lower === "merged") return "MERGED";
  if (lower === "closed") return "CLOSED";
  return state.toUpperCase();
}

function parseTimestampMs(value?: string | null): number | undefined {
  if (!value) return undefined;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : undefined;
}

interface RawGitLabMR {
  readonly iid: number;
  readonly title: string;
  readonly state: string;
  readonly draft?: boolean;
  readonly work_in_progress?: boolean;
  readonly source_branch: string;
  readonly target_branch?: string;
  readonly web_url: string;
  readonly updated_at?: string | null;
  readonly sha?: string | null;
  readonly author?: { readonly username?: string } | null;
  readonly head_pipeline?: {
    readonly id?: number;
    readonly status?: string;
    readonly web_url?: string;
  } | null;
}

export class GitLabProvider implements ForgeProvider {
  readonly kind = "gitlab" as const;
  readonly definition = gitlabDefinition;
  readonly capabilities = gitlabCapabilities;

  private readonly policy: RequestPolicy;
  private readonly tokenCache: TokenCache;
  private readonly execCliFn: ExecCliFn;
  private readonly env: Record<string, string | undefined>;
  private readonly explicitTokens: Map<string, string>;

  constructor(opts: GitLabProviderOptions = {}) {
    this.policy = opts.policy ?? new RequestPolicy();
    this.tokenCache = opts.tokenCache ?? new TokenCache();
    this.execCliFn = opts.execCliFn ?? defaultExecCli;
    this.env = opts.env ?? process.env;
    this.explicitTokens = new Map();
    if (opts.tokens) {
      for (const [k, v] of Object.entries(opts.tokens)) {
        if (v.trim().length > 0) {
          this.explicitTokens.set(k.trim().toLowerCase(), v.trim());
        }
      }
    }
  }

  /**
   * Probes `glab auth status` to recognize self-managed GitLab instances on arbitrary hostnames.
   */
  async refineUnknownRemote(input: {
    readonly cwd: string;
    readonly remote: GitRemoteHost;
    readonly signal?: AbortSignal;
  }): Promise<ForgeRepoRef | null> {
    if (!isNestedGitLabRepoPath(input.remote.path)) {
      return null;
    }
    const res = await this.execCliFn({
      command: "glab",
      args: ["auth", "status"],
      cwd: input.cwd,
      env: { NO_COLOR: "1" },
      timeoutMs: 4_500,
      signal: input.signal,
    });
    const combined = `${res.stdout}\n${res.stderr}`;
    const hosts = parseGitLabAuthStatusHosts(combined);
    const matched = hosts.find(
      (h) =>
        h.account !== null &&
        (h.host === input.remote.host || h.host === input.remote.hostname),
    );
    if (!matched) {
      return null;
    }
    const baseUrl = /^https?:/iu.test(input.remote.remoteUrl)
      ? new URL(input.remote.remoteUrl).origin
      : `https://${input.remote.host}`;
    return {
      kind: "gitlab",
      host: input.remote.host,
      repository: input.remote.path,
      remote_url: input.remote.remoteUrl,
      base_url: baseUrl,
    };
  }

  /**
   * Host-scoped token resolution:
   * 1. Explicit config for exact host/hostname
   * 2. `GITLAB_TOKEN` / `GITLAB_ACCESS_TOKEN` / `OAUTH_TOKEN` ONLY when `host === (GITLAB_HOST || "gitlab.com")`
   * 3. `glab config get token --host <host>` with GitLab token env vars cleared
   */
  async resolveToken(
    host: string,
    cwd?: string,
    signal?: AbortSignal,
  ): Promise<{ readonly token: string | null; readonly fingerprint: string | null }> {
    const validatedHost = validateHost(host, "gitlab");
    const hostname = validatedHost.replace(/:\d+$/u, "");

    const explicit =
      this.explicitTokens.get(validatedHost) ?? this.explicitTokens.get(hostname);
    if (explicit) {
      return this.tokenCache.getOrLoad(`gitlab:${validatedHost}:explicit`, async () => explicit);
    }

    const configuredEnvHost =
      this.env.GITLAB_HOST?.trim().toLowerCase() || "gitlab.com";
    if (validatedHost === configuredEnvHost || hostname === configuredEnvHost) {
      const envToken =
        this.env.GITLAB_TOKEN?.trim() ||
        this.env.GITLAB_ACCESS_TOKEN?.trim() ||
        this.env.OAUTH_TOKEN?.trim();
      if (envToken) {
        return this.tokenCache.getOrLoad(`gitlab:${validatedHost}:env`, async () => envToken);
      }
    }

    return this.tokenCache.getOrLoad(`gitlab:${validatedHost}:cli`, async () => {
      const res = await this.execCliFn({
        command: "glab",
        args: ["config", "get", "token", "--host", validatedHost],
        cwd,
        env: {
          GITLAB_TOKEN: undefined,
          GITLAB_ACCESS_TOKEN: undefined,
          OAUTH_TOKEN: undefined,
          GITLAB_HOST: undefined,
          NO_COLOR: "1",
        },
        timeoutMs: 5_000,
        signal,
      });
      if (res.exitCode !== 0) {
        return null;
      }
      const trimmed = res.stdout.trim();
      return trimmed.length > 0 ? trimmed : null;
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
        provider: "gitlab",
        message: `No GitLab credentials found for ${host}. Run 'glab auth login --hostname ${host}' or configure a host token.`,
      });
    }
    return { token: entry.token, fingerprint: entry.fingerprint };
  }

  private buildHeaders(token: string): Record<string, string> {
    return {
      Accept: "application/json",
      "PRIVATE-TOKEN": token,
      "User-Agent": "spacesd",
    };
  }

  private mapHttpError(response: Response, host: string, detail: string): ForgeError {
    if (response.status === 401) {
      return new ForgeError({
        reason: "unauthenticated",
        provider: "gitlab",
        statusCode: 401,
        message: `GitLab authentication failed on ${host}: ${detail}`,
      });
    }
    if (response.status === 403) {
      return new ForgeError({
        reason: "forbidden",
        provider: "gitlab",
        statusCode: 403,
        message: `GitLab access forbidden on ${host}: ${detail}`,
      });
    }
    if (response.status === 404) {
      return new ForgeError({
        reason: "not-found",
        provider: "gitlab",
        statusCode: 404,
        message: `GitLab resource not found on ${host}: ${detail}`,
      });
    }
    return new ForgeError({
      reason: "failed",
      provider: "gitlab",
      statusCode: response.status,
      message: `GitLab API error (${response.status}) on ${host}: ${detail}`,
    });
  }

  private mapMrSummary(
    repo: ForgeRepoRef,
    mr: RawGitLabMR,
    checks: Array<CheckRunDetail> = [],
  ): ChangeRequestSummary {
    let effectiveChecks = checks;
    if (effectiveChecks.length === 0 && mr.head_pipeline?.status) {
      const pipelineState = mapGitLabStatus(mr.head_pipeline.status);
      effectiveChecks = [
        {
          name: "pipeline",
          status: pipelineState,
          ...(mr.head_pipeline.web_url ? { url: mr.head_pipeline.web_url } : {}),
        },
      ];
    }
    const summary = aggregateCheckRuns(effectiveChecks);
    const updatedAtMs = parseTimestampMs(mr.updated_at);
    return {
      provider: "gitlab",
      host: repo.host,
      repository: repo.repository,
      number: mr.iid,
      title: mr.title,
      state: normalizeGitLabMrState(mr.state),
      is_draft: Boolean(mr.draft || mr.work_in_progress),
      head_branch: mr.source_branch,
      ...(mr.target_branch ? { base_branch: mr.target_branch } : {}),
      url: mr.web_url,
      ...(mr.author?.username ? { author: mr.author.username } : {}),
      ...(summary ? { checks_state: summary.state } : {}),
      checks: effectiveChecks,
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
    const apiBase = gitlabApiBase(input.host, input.baseUrl);
    const response = await this.policy.fetch({
      provider: "gitlab",
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
    const data = (await response.json()) as { username?: string };
    if (!data.username) {
      throw new ForgeError({
        reason: "failed",
        provider: "gitlab",
        message: `GitLab /user response missing username on ${input.host}`,
      });
    }
    return data.username;
  }

  async listChangeRequests(
    input: ListChangeRequestsInput,
  ): Promise<Array<ChangeRequestSummary>> {
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );
    const apiBase = gitlabApiBase(input.repo.host, input.repo.base_url);
    const encodedProject = encodeURIComponent(input.repo.repository);
    const params = new URLSearchParams({
      per_page: String(Math.min(Math.max(input.limit ?? 30, 1), 100)),
      order_by: "updated_at",
      sort: "desc",
    });
    if (input.state === "open") {
      params.set("state", "opened");
    } else if (input.state === "closed" || input.state === "merged") {
      params.set("state", input.state);
    } else {
      params.set("state", "all");
    }
    if (input.branch) {
      params.set("source_branch", input.branch);
    }

    const response = await this.policy.fetch({
      provider: "gitlab",
      host: input.repo.host,
      url: `${apiBase}/projects/${encodedProject}/merge_requests?${params.toString()}`,
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

    const rawMrs = (await response.json()) as Array<RawGitLabMR>;
    return rawMrs.map((mr) => this.mapMrSummary(input.repo, mr));
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
    const apiBase = gitlabApiBase(input.repo.host, input.repo.base_url);
    const encodedProject = encodeURIComponent(input.repo.repository);
    const params = new URLSearchParams({
      source_branch: input.branch,
      state: "all",
      per_page: "5",
      order_by: "created_at",
      sort: "desc",
    });

    const response = await this.policy.fetch({
      provider: "gitlab",
      host: input.repo.host,
      url: `${apiBase}/projects/${encodedProject}/merge_requests?${params.toString()}`,
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
    const rawMrs = (await response.json()) as Array<RawGitLabMR>;
    const mr = rawMrs[0];
    if (!mr) return null;

    let checks: Array<CheckRunDetail> = [];
    if (mr.sha) {
      try {
        checks = await this.getChecks({
          cwd: input.cwd,
          repo: input.repo,
          ref: mr.sha,
          signal: input.signal,
        });
      } catch {
        // Fall back to head_pipeline if commit statuses are unavailable.
      }
    }

    return this.mapMrSummary(input.repo, mr, checks);
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
    const apiBase = gitlabApiBase(input.repo.host, input.repo.base_url);
    const encodedProject = encodeURIComponent(input.repo.repository);
    const encodedRef = encodeURIComponent(input.ref);

    const response = await this.policy.fetch({
      provider: "gitlab",
      host: input.repo.host,
      url: `${apiBase}/projects/${encodedProject}/repository/commits/${encodedRef}/statuses?per_page=100`,
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
      name: string;
      status: string;
      description?: string | null;
      target_url?: string | null;
      finished_at?: string | null;
      created_at?: string | null;
    }>;

    const entries: Array<TimestampedCheckRun> = statuses.map((st, idx) => ({
      check: {
        name: st.name,
        status: mapGitLabStatus(st.status),
        ...(st.description ? { description: st.description } : {}),
        ...(st.target_url ? { url: st.target_url } : {}),
      },
      at: st.finished_at ?? st.created_at ?? null,
      sequence: idx + 1,
    }));

    return dedupeCheckRuns(entries);
  }
}

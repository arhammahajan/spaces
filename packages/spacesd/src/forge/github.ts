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
  type BranchChangeRequestResult,
  type ForgeCapabilities,
  type ForgeDefinition,
  type ForgeProvider,
  type ListChangeRequestsInput,
  type ParsedChangeRequestUrl,
  type TimestampedCheckRun,
} from "./types";

const MAX_BATCH_BRANCHES = 32;
const CHANGE_REQUEST_PATH = /^\/([^/]+\/[^/]+)\/pull\/(\d+)(?:\/|$)/u;

export function isGitHubHost(hostname: string): boolean {
  const host = hostname.toLowerCase();
  return (
    host === "github.com" ||
    host.endsWith(".ghe.com") ||
    host.split(".").includes("github")
  );
}

function isTwoSegmentRepoPath(path: string): boolean {
  const parts = path.split("/").filter(Boolean);
  return (
    parts.length === 2 &&
    parts.every((s) => /^[\w.-]+$/u.test(s) && s !== "." && s !== "..")
  );
}

export const githubDefinition: ForgeDefinition = {
  kind: "github",
  label: "GitHub",
  publicHost: "github.com",
  terminology: {
    shortLabel: "PR",
    shortLabelWithArticle: "a PR",
    singular: "pull request",
    plural: "pull requests",
  },
  recognizeRemote(remote) {
    if (!isGitHubHost(remote.hostname)) return null;
    if (!isTwoSegmentRepoPath(remote.path)) return null;
    const baseUrl = /^https?:/iu.test(remote.remoteUrl)
      ? new URL(remote.remoteUrl).origin
      : `https://${remote.host}`;
    return {
      kind: "github",
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
    return `refs/pull/${number}/head`;
  },
};

export const githubCapabilities: ForgeCapabilities = {
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

export interface GitHubProviderOptions {
  readonly policy?: RequestPolicy;
  readonly tokenCache?: TokenCache;
  readonly execCliFn?: ExecCliFn;
  readonly env?: Record<string, string | undefined>;
  readonly tokens?: Record<string, string>;
}

function githubApiBase(host: string): { restBase: string; graphqlUrl: string } {
  const validated = validateHost(host, "github");
  const hostname = validated.replace(/:\d+$/u, "");
  if (hostname === "github.com") {
    return {
      restBase: "https://api.github.com",
      graphqlUrl: "https://api.github.com/graphql",
    };
  }
  if (hostname.endsWith(".ghe.com")) {
    return {
      restBase: `https://api.${validated}`,
      graphqlUrl: `https://api.${validated}/graphql`,
    };
  }
  return {
    restBase: `https://${validated}/api/v3`,
    graphqlUrl: `https://${validated}/api/graphql`,
  };
}

function mapGitHubCheckRunStatus(
  status?: string | null,
  conclusion?: string | null,
): CheckState {
  const normStatus = (status ?? "").trim().toUpperCase();
  const normConclusion = (conclusion ?? "").trim().toUpperCase();

  if (normConclusion === "SUCCESS" || (normConclusion === "" && normStatus === "SUCCESS")) {
    return "success";
  }
  if (
    normConclusion === "FAILURE" ||
    normConclusion === "CANCELLED" ||
    normConclusion === "TIMED_OUT" ||
    normConclusion === "STARTUP_FAILURE" ||
    normConclusion === "ACTION_REQUIRED" ||
    (normConclusion === "" && (normStatus === "FAILURE" || normStatus === "ERROR"))
  ) {
    return "failure";
  }
  if (normConclusion === "NEUTRAL" || normConclusion === "SKIPPED") {
    return "none";
  }
  if (
    normStatus === "IN_PROGRESS" ||
    normStatus === "QUEUED" ||
    normStatus === "PENDING" ||
    normStatus === "REQUESTED" ||
    normStatus === "WAITING" ||
    normStatus === "EXPECTED"
  ) {
    return "pending";
  }
  return "none";
}

interface GqlCheckNode {
  readonly __typename?: string;
  readonly name?: string;
  readonly status?: string;
  readonly conclusion?: string | null;
  readonly detailsUrl?: string | null;
  readonly completedAt?: string | null;
  readonly startedAt?: string | null;
  readonly checkSuite?: { readonly workflowRun?: { readonly workflow?: { readonly name?: string } } | null } | null;
  readonly context?: string;
  readonly state?: string;
  readonly description?: string | null;
  readonly targetUrl?: string | null;
  readonly createdAt?: string | null;
}

function normalizeGqlChecks(nodes: ReadonlyArray<GqlCheckNode | null> | undefined): Array<CheckRunDetail> {
  if (!nodes || nodes.length === 0) return [];
  const entries: Array<TimestampedCheckRun> = [];
  let seq = 0;
  for (const node of nodes) {
    if (!node) continue;
    seq++;
    if (node.__typename === "StatusContext" || (!node.name && node.context)) {
      const name = (node.context ?? "").trim();
      if (!name) continue;
      entries.push({
        check: {
          name,
          status: mapGitHubCheckRunStatus(node.state, null),
          ...(node.description ? { description: node.description } : {}),
          ...(node.targetUrl ? { url: node.targetUrl } : {}),
        },
        at: node.createdAt ?? null,
        sequence: seq,
      });
    } else {
      const name = (node.name ?? "").trim();
      if (!name) continue;
      entries.push({
        check: {
          name,
          status: mapGitHubCheckRunStatus(node.status, node.conclusion),
          ...(node.detailsUrl ? { url: node.detailsUrl } : {}),
        },
        workflowName: node.checkSuite?.workflowRun?.workflow?.name ?? null,
        at: node.completedAt ?? node.startedAt ?? null,
        sequence: seq,
      });
    }
  }
  return dedupeCheckRuns(entries);
}

function normalizeGitHubPrState(state: string, mergedAt?: string | null): string {
  const upper = state.trim().toUpperCase();
  if (upper === "MERGED" || (upper === "CLOSED" && mergedAt)) {
    return "MERGED";
  }
  if (upper === "OPEN") return "OPEN";
  if (upper === "CLOSED") return "CLOSED";
  return upper;
}

function parseTimestampMs(value?: string | null): number | undefined {
  if (!value) return undefined;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : undefined;
}

export class GitHubProvider implements ForgeProvider {
  readonly kind = "github" as const;
  readonly definition = githubDefinition;
  readonly capabilities = githubCapabilities;

  private readonly policy: RequestPolicy;
  private readonly tokenCache: TokenCache;
  private readonly execCliFn: ExecCliFn;
  private readonly env: Record<string, string | undefined>;
  private readonly explicitTokens: Map<string, string>;

  constructor(opts: GitHubProviderOptions = {}) {
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
   * Host-scoped token resolution:
   * 1. Explicit config for exact host/hostname
   * 2. `GH_TOKEN`/`GITHUB_TOKEN` ONLY for `github.com` & `*.ghe.com`;
   *    `GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN` ONLY when `GH_HOST === host`
   * 3. `gh auth token --hostname <host>` with enterprise/token env vars cleared & `GH_PROMPT_DISABLED=1`
   */
  async resolveToken(
    host: string,
    cwd?: string,
    signal?: AbortSignal,
  ): Promise<{ readonly token: string | null; readonly fingerprint: string | null }> {
    const validatedHost = validateHost(host, "github");
    const hostname = validatedHost.replace(/:\d+$/u, "");

    const explicit =
      this.explicitTokens.get(validatedHost) ?? this.explicitTokens.get(hostname);
    if (explicit) {
      return this.tokenCache.getOrLoad(`github:${validatedHost}:explicit`, async () => explicit);
    }

    const isPublicOrGheCloud = hostname === "github.com" || hostname.endsWith(".ghe.com");
    if (isPublicOrGheCloud) {
      const envToken = this.env.GH_TOKEN?.trim() || this.env.GITHUB_TOKEN?.trim();
      if (envToken) {
        return this.tokenCache.getOrLoad(`github:${validatedHost}:env`, async () => envToken);
      }
    } else {
      const ghHost = this.env.GH_HOST?.trim().toLowerCase();
      if (ghHost && (ghHost === validatedHost || ghHost === hostname)) {
        const enterpriseToken =
          this.env.GH_ENTERPRISE_TOKEN?.trim() ||
          this.env.GITHUB_ENTERPRISE_TOKEN?.trim();
        if (enterpriseToken) {
          return this.tokenCache.getOrLoad(
            `github:${validatedHost}:enterprise-env`,
            async () => enterpriseToken,
          );
        }
      }
    }

    return this.tokenCache.getOrLoad(`github:${validatedHost}:cli`, async () => {
      const res = await this.execCliFn({
        command: "gh",
        args: ["auth", "token", "--hostname", hostname],
        cwd,
        env: {
          GH_TOKEN: undefined,
          GITHUB_TOKEN: undefined,
          GH_ENTERPRISE_TOKEN: undefined,
          GITHUB_ENTERPRISE_TOKEN: undefined,
          GH_HOST: undefined,
          GH_PROMPT_DISABLED: "1",
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
        provider: "github",
        message: `No GitHub credentials found for ${host}. Run 'gh auth login --hostname ${host}' or configure a host token.`,
      });
    }
    return { token: entry.token, fingerprint: entry.fingerprint };
  }

  private buildHeaders(token: string): Record<string, string> {
    return {
      Accept: "application/vnd.github+json",
      Authorization: `Bearer ${token}`,
      "X-GitHub-Api-Version": "2022-11-28",
      "User-Agent": "spacesd",
    };
  }

  private mapHttpError(response: Response, host: string, detail: string): ForgeError {
    if (response.status === 401) {
      return new ForgeError({
        reason: "unauthenticated",
        provider: "github",
        statusCode: 401,
        message: `GitHub authentication failed on ${host}: ${detail}`,
      });
    }
    if (response.status === 403) {
      return new ForgeError({
        reason: "forbidden",
        provider: "github",
        statusCode: 403,
        message: `GitHub access forbidden on ${host}: ${detail}`,
      });
    }
    if (response.status === 404) {
      return new ForgeError({
        reason: "not-found",
        provider: "github",
        statusCode: 404,
        message: `GitHub resource not found on ${host}: ${detail}`,
      });
    }
    return new ForgeError({
      reason: "failed",
      provider: "github",
      statusCode: response.status,
      message: `GitHub API error (${response.status}) on ${host}: ${detail}`,
    });
  }

  async getViewer(input: {
    readonly cwd?: string;
    readonly host: string;
    readonly baseUrl?: string;
    readonly signal?: AbortSignal;
  }): Promise<string> {
    const { token, fingerprint } = await this.requireToken(input.host, input.cwd, input.signal);
    const { restBase } = githubApiBase(input.host);
    const response = await this.policy.fetch({
      provider: "github",
      host: input.host,
      url: `${restBase}/user`,
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
    const data = (await response.json()) as { login?: string };
    if (!data.login) {
      throw new ForgeError({
        reason: "failed",
        provider: "github",
        message: `GitHub /user response missing login on ${input.host}`,
      });
    }
    return data.login;
  }

  async listChangeRequests(
    input: ListChangeRequestsInput,
  ): Promise<Array<ChangeRequestSummary>> {
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );
    const { restBase } = githubApiBase(input.repo.host);
    const [owner, name] = splitOwnerRepo(input.repo.repository, "github");
    const restState =
      input.state === "open"
        ? "open"
        : input.state === "closed" || input.state === "merged"
          ? "closed"
          : "all";
    const limit = Math.min(Math.max(input.limit ?? 30, 1), 100);
    const params = new URLSearchParams({
      state: restState,
      per_page: String(limit),
      sort: "updated",
      direction: "desc",
    });
    if (input.branch) {
      params.set("head", `${owner}:${input.branch}`);
    }

    const response = await this.policy.fetch({
      provider: "github",
      host: input.repo.host,
      url: `${restBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/pulls?${params.toString()}`,
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

    const rawPrs = (await response.json()) as Array<{
      number: number;
      title: string;
      state: string;
      draft?: boolean;
      merged_at?: string | null;
      html_url: string;
      updated_at?: string | null;
      user?: { login?: string } | null;
      head?: { ref?: string } | null;
      base?: { ref?: string } | null;
    }>;

    const mapped: Array<ChangeRequestSummary> = [];
    for (const pr of rawPrs) {
      const normalizedState = normalizeGitHubPrState(pr.state, pr.merged_at);
      if (input.state === "merged" && normalizedState !== "MERGED") continue;
      if (input.state === "closed" && normalizedState !== "CLOSED") continue;
      mapped.push({
        provider: "github",
        host: input.repo.host,
        repository: input.repo.repository,
        number: pr.number,
        title: pr.title,
        state: normalizedState,
        is_draft: Boolean(pr.draft),
        head_branch: pr.head?.ref ?? "",
        ...(pr.base?.ref ? { base_branch: pr.base.ref } : {}),
        url: pr.html_url,
        ...(pr.user?.login ? { author: pr.user.login } : {}),
        checks: [],
        ...(parseTimestampMs(pr.updated_at) !== undefined
          ? { updated_at_ms: parseTimestampMs(pr.updated_at) }
          : {}),
      });
    }
    return mapped;
  }

  async getChangeRequestForBranch(input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branch: string;
    readonly signal?: AbortSignal;
  }): Promise<ChangeRequestSummary | null> {
    const batch = await this.getChangeRequestsForBranches({
      cwd: input.cwd,
      repo: input.repo,
      branches: [input.branch],
      signal: input.signal,
    });
    return batch[input.branch]?.changeRequest ?? null;
  }

  /**
   * Batched GraphQL query (up to 32 branches per query matching `internal/gitinfo/github.go`)
   * with automatic REST `/repos/{owner}/{repo}/pulls` fallback on non-auth/non-rate-limit GraphQL errors.
   */
  async getChangeRequestsForBranches(input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branches: ReadonlyArray<string>;
    readonly signal?: AbortSignal;
  }): Promise<Record<string, BranchChangeRequestResult>> {
    if (input.branches.length === 0) {
      return {};
    }
    const { token, fingerprint } = await this.requireToken(
      input.repo.host,
      input.cwd,
      input.signal,
    );

    try {
      return await this.fetchBranchesGraphQL(
        input.repo,
        input.branches,
        token,
        fingerprint,
        input.signal,
      );
    } catch (error) {
      if (
        error instanceof ForgeError &&
        (error.reason === "rate-limited" ||
          error.reason === "unauthenticated" ||
          error.reason === "forbidden")
      ) {
        throw error;
      }
      return await this.fetchBranchesRESTFallback(input);
    }
  }

  private async fetchBranchesGraphQL(
    repo: ForgeRepoRef,
    branches: ReadonlyArray<string>,
    token: string,
    fingerprint: string,
    signal?: AbortSignal,
  ): Promise<Record<string, BranchChangeRequestResult>> {
    const [owner, name] = splitOwnerRepo(repo.repository, "github");
    const { graphqlUrl } = githubApiBase(repo.host);
    const result: Record<string, BranchChangeRequestResult> = {};

    for (let start = 0; start < branches.length; start += MAX_BATCH_BRANCHES) {
      const chunk = branches.slice(start, start + MAX_BATCH_BRANCHES);
      const decls: Array<string> = ["$owner: String!", "$name: String!"];
      const variables: Record<string, string> = { owner, name };
      const fields: Array<string> = [];

      for (let i = 0; i < chunk.length; i++) {
        const branch = chunk[i]!;
        const headVar = `head_${i}`;
        const refVar = `ref_${i}`;
        decls.push(`$${headVar}: String!`, `$${refVar}: String!`);
        variables[headVar] = branch;
        variables[refVar] = `refs/heads/${branch}`;
        fields.push(
          `    pr_${i}: pullRequests(headRefName: $${headVar}, first: 1, states: [OPEN, MERGED, CLOSED], orderBy: {field: CREATED_AT, direction: DESC}) {\n` +
            `      nodes {\n` +
            `        number title state isDraft url updatedAt baseRefName\n` +
            `        author { login }\n` +
            `        commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) {\n` +
            `          nodes {\n` +
            `            __typename\n` +
            `            ... on CheckRun { name status conclusion detailsUrl completedAt startedAt }\n` +
            `            ... on StatusContext { context state description targetUrl createdAt }\n` +
            `          }\n` +
            `        } } } } }\n` +
            `      }\n` +
            `    }\n` +
            `    ref_${i}: ref(qualifiedName: $${refVar}) {\n` +
            `      target { ... on Commit { statusCheckRollup { contexts(first: 100) {\n` +
            `        nodes {\n` +
            `          __typename\n` +
            `          ... on CheckRun { name status conclusion detailsUrl completedAt startedAt }\n` +
            `          ... on StatusContext { context state description targetUrl createdAt }\n` +
            `        }\n` +
            `      } } } }\n` +
            `    }`,
        );
      }

      const query = `query(${decls.join(", ")}) {\n  repository(owner: $owner, name: $name) {\n${fields.join("\n")}\n  }\n}`;
      const response = await this.policy.fetch({
        provider: "github",
        host: repo.host,
        url: graphqlUrl,
        credentialScope: fingerprint,
        init: {
          method: "POST",
          headers: {
            ...this.buildHeaders(token),
            "Content-Type": "application/json",
          },
          body: JSON.stringify({ query, variables }),
          signal,
        },
      });

      if (!response.ok) {
        throw this.mapHttpError(response, repo.host, await response.text().catch(() => ""));
      }

      const payload = (await response.json()) as {
        data?: { repository?: Record<string, unknown> | null } | null;
        errors?: Array<{ type?: string; code?: string; message?: string }>;
      };

      if (payload.errors && payload.errors.length > 0) {
        this.policy.handleGraphQLErrors(
          "github",
          repo.host,
          payload.errors,
          response.headers,
          fingerprint,
        );
        if (!payload.data?.repository) {
          throw new ForgeError({
            reason: "failed",
            provider: "github",
            message: `GitHub GraphQL error: ${payload.errors[0]?.message ?? "unknown error"}`,
          });
        }
      }

      const repoData = payload.data?.repository;
      if (!repoData) {
        continue;
      }

      for (let i = 0; i < chunk.length; i++) {
        const branch = chunk[i]!;
        const prConn = repoData[`pr_${i}`] as
          | {
              nodes?: Array<{
                number: number;
                title: string;
                state: string;
                isDraft?: boolean;
                url: string;
                updatedAt?: string | null;
                baseRefName?: string | null;
                author?: { login?: string } | null;
                commits?: {
                  nodes?: Array<{
                    commit?: {
                      statusCheckRollup?: {
                        contexts?: { nodes?: Array<GqlCheckNode | null> };
                      } | null;
                    };
                  }>;
                };
              }>;
            }
          | null
          | undefined;

        const refObj = repoData[`ref_${i}`] as
          | {
              target?: {
                statusCheckRollup?: {
                  contexts?: { nodes?: Array<GqlCheckNode | null> };
                } | null;
              } | null;
            }
          | null
          | undefined;

        let checkRuns: Array<CheckRunDetail> = [];
        let changeRequest: ChangeRequestSummary | undefined;

        const prNode = prConn?.nodes?.[0];
        if (prNode) {
          const prCheckNodes =
            prNode.commits?.nodes?.[0]?.commit?.statusCheckRollup?.contexts?.nodes;
          checkRuns = normalizeGqlChecks(prCheckNodes);
        }
        if (checkRuns.length === 0) {
          const refCheckNodes = refObj?.target?.statusCheckRollup?.contexts?.nodes;
          checkRuns = normalizeGqlChecks(refCheckNodes);
        }

        const checksSummary = aggregateCheckRuns(checkRuns);
        if (prNode) {
          const updatedAtMs = parseTimestampMs(prNode.updatedAt);
          changeRequest = {
            provider: "github",
            host: repo.host,
            repository: repo.repository,
            number: prNode.number,
            title: prNode.title,
            state: normalizeGitHubPrState(prNode.state),
            is_draft: Boolean(prNode.isDraft),
            head_branch: branch,
            ...(prNode.baseRefName ? { base_branch: prNode.baseRefName } : {}),
            url: prNode.url,
            ...(prNode.author?.login ? { author: prNode.author.login } : {}),
            ...(checksSummary ? { checks_state: checksSummary.state } : {}),
            checks: checkRuns,
            ...(updatedAtMs !== undefined ? { updated_at_ms: updatedAtMs } : {}),
          };
        }

        result[branch] = {
          ...(changeRequest ? { changeRequest } : {}),
          ...(checksSummary ? { checks: checksSummary } : {}),
          ...(checkRuns.length > 0 ? { checkRuns } : {}),
        };
      }
    }

    return result;
  }

  private async fetchBranchesRESTFallback(input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branches: ReadonlyArray<string>;
    readonly signal?: AbortSignal;
  }): Promise<Record<string, BranchChangeRequestResult>> {
    const wanted = new Set(input.branches);
    const prs = await this.listChangeRequests({
      cwd: input.cwd,
      repo: input.repo,
      state: "all",
      limit: 100,
      signal: input.signal,
    });
    const result: Record<string, BranchChangeRequestResult> = {};
    for (const pr of prs) {
      if (!wanted.has(pr.head_branch)) continue;
      if (result[pr.head_branch]) continue;
      result[pr.head_branch] = { changeRequest: pr };
    }
    return result;
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
    const { restBase } = githubApiBase(input.repo.host);
    const [owner, name] = splitOwnerRepo(input.repo.repository, "github");
    const encodedRef = encodeURIComponent(input.ref);

    const [checkRunsResp, statusResp] = await Promise.all([
      this.policy.fetch({
        provider: "github",
        host: input.repo.host,
        url: `${restBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/commits/${encodedRef}/check-runs?per_page=100`,
        credentialScope: fingerprint,
        init: { method: "GET", headers: this.buildHeaders(token), signal: input.signal },
      }),
      this.policy.fetch({
        provider: "github",
        host: input.repo.host,
        url: `${restBase}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/commits/${encodedRef}/status`,
        credentialScope: fingerprint,
        init: { method: "GET", headers: this.buildHeaders(token), signal: input.signal },
      }),
    ]);

    if (!checkRunsResp.ok) {
      throw this.mapHttpError(
        checkRunsResp,
        input.repo.host,
        await checkRunsResp.text().catch(() => ""),
      );
    }

    const entries: Array<TimestampedCheckRun> = [];
    let seq = 0;

    const checkRunsData = (await checkRunsResp.json()) as {
      check_runs?: Array<{
        name: string;
        status?: string;
        conclusion?: string | null;
        html_url?: string | null;
        completed_at?: string | null;
        started_at?: string | null;
      }>;
    };
    for (const run of checkRunsData.check_runs ?? []) {
      seq++;
      entries.push({
        check: {
          name: run.name,
          status: mapGitHubCheckRunStatus(run.status, run.conclusion),
          ...(run.html_url ? { url: run.html_url } : {}),
        },
        at: run.completed_at ?? run.started_at ?? null,
        sequence: seq,
      });
    }

    if (statusResp.ok) {
      const statusData = (await statusResp.json()) as {
        statuses?: Array<{
          context: string;
          state: string;
          description?: string | null;
          target_url?: string | null;
          updated_at?: string | null;
          created_at?: string | null;
        }>;
      };
      for (const st of statusData.statuses ?? []) {
        seq++;
        entries.push({
          check: {
            name: st.context,
            status: mapGitHubCheckRunStatus(st.state, null),
            ...(st.description ? { description: st.description } : {}),
            ...(st.target_url ? { url: st.target_url } : {}),
          },
          at: st.updated_at ?? st.created_at ?? null,
          sequence: seq,
        });
      }
    }

    return dedupeCheckRuns(entries);
  }
}

function splitOwnerRepo(repository: string, provider: "github"): [string, string] {
  const parts = repository.split("/").filter(Boolean);
  if (parts.length !== 2) {
    throw new ForgeError({
      reason: "failed",
      provider,
      message: `Invalid ${provider} repository path: "${repository}"`,
    });
  }
  return [parts[0]!, parts[1]!];
}

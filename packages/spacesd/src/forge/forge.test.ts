import { describe, expect, test } from "bun:test";
import type { CheckRunDetail, IssueLink } from "@spaces/types";
import { IssueTrackerRegistry } from "../issues/registry";
import {
  isStandaloneIssueTracker,
  type IssueTrackerProvider,
  type StandaloneIssueTrackerProvider,
} from "../issues/types";
import {
  ForgejoProvider,
  forgejoKeysPaths,
  parseForgejoKeysJson,
  parseTeaLoginsJson,
} from "./forgejo";
import { GitHubProvider } from "./github";
import { GitLabProvider, parseGitLabAuthStatusHosts } from "./gitlab";
import {
  RequestPolicy,
  TokenCache,
  sha256Hex,
  validateHost,
  type ExecCliInput,
  type ExecCliResult,
  type FetchFn,
} from "./http";
import {
  ForgeRegistry,
  createDefaultForgeRegistry,
  parseGitRemote,
} from "./registry";
import {
  ForgeError,
  aggregateCheckRuns,
  dedupeCheckRuns,
  type ForgeProvider,
} from "./types";

function jsonResponse(
  body: unknown,
  status = 200,
  headers: Record<string, string> = {},
): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "content-type": "application/json",
      ...headers,
    },
  });
}

describe("parseGitRemote", () => {
  test("parses SCP, HTTPS, SSH, enterprise, nested GitLab groups, and Forgejo ports/subpaths", () => {
    expect(parseGitRemote("git@github.com:org/repo.git")).toEqual({
      remoteUrl: "git@github.com:org/repo.git",
      host: "github.com",
      hostname: "github.com",
      path: "org/repo",
    });

    expect(parseGitRemote("github.com:org/repo.git")).toEqual({
      remoteUrl: "github.com:org/repo.git",
      host: "github.com",
      hostname: "github.com",
      path: "org/repo",
    });

    expect(parseGitRemote("https://acme.ghe.com/team/service.git")).toEqual({
      remoteUrl: "https://acme.ghe.com/team/service.git",
      host: "acme.ghe.com",
      hostname: "acme.ghe.com",
      path: "team/service",
    });

    expect(parseGitRemote("git@gitlab.com:org/platform/sub/api.git")).toEqual({
      remoteUrl: "git@gitlab.com:org/platform/sub/api.git",
      host: "gitlab.com",
      hostname: "gitlab.com",
      path: "org/platform/sub/api",
    });

    expect(parseGitRemote("http://git.local:3000/alice/demo.git")).toEqual({
      remoteUrl: "http://git.local:3000/alice/demo.git",
      host: "git.local:3000",
      hostname: "git.local",
      path: "alice/demo",
    });

    expect(parseGitRemote("https://forgejo.example.com/git/alice/demo.git")).toEqual({
      remoteUrl: "https://forgejo.example.com/git/alice/demo.git",
      host: "forgejo.example.com",
      hostname: "forgejo.example.com",
      path: "git/alice/demo",
    });

    // SSH port must not leak into web host identity
    expect(parseGitRemote("ssh://git@codeberg.org:2222/alice/demo.git")).toEqual({
      remoteUrl: "ssh://git@codeberg.org:2222/alice/demo.git",
      host: "codeberg.org",
      hostname: "codeberg.org",
      path: "alice/demo",
    });

    expect(parseGitRemote("/local/path/to/repo")).toBeNull();
    expect(parseGitRemote("C:\\Users\\dev\\repo")).toBeNull();
    expect(parseGitRemote("")).toBeNull();
  });
});

describe("validateHost, TokenCache, and RequestPolicy", () => {
  test("validates hostnames strictly", () => {
    expect(validateHost("GitHub.COM")).toBe("github.com");
    expect(validateHost("git.local:3000")).toBe("git.local:3000");
    expect(validateHost("[::1]:3000")).toBe("[::1]:3000");

    expect(() => validateHost("github.com/evil")).toThrow(ForgeError);
    expect(() => validateHost("user@github.com")).toThrow(ForgeError);
    expect(() => validateHost("github.com?x=1")).toThrow(ForgeError);
    expect(() => validateHost("bad..host.com")).toThrow(ForgeError);
    expect(() => validateHost("git.local:99999")).toThrow(ForgeError);
    expect(() => validateHost("")).toThrow(ForgeError);
  });

  test("TokenCache enforces positive (~5m) and negative (~10s) TTLs with sha256 fingerprint", async () => {
    let now = 1_000_000;
    const cache = new TokenCache({ nowFn: () => now });

    let loads = 0;
    const first = await cache.getOrLoad("k1", async () => {
      loads++;
      return "secret-token-1";
    });
    expect(first.token).toBe("secret-token-1");
    expect(first.fingerprint).toBe(sha256Hex("secret-token-1"));
    expect(loads).toBe(1);

    // Still cached at +4m59s
    now += 299_000;
    await cache.getOrLoad("k1", async () => {
      loads++;
      return "secret-token-2";
    });
    expect(loads).toBe(1);

    // Expired at +5m01s
    now += 2_000;
    const refreshed = await cache.getOrLoad("k1", async () => {
      loads++;
      return "secret-token-2";
    });
    expect(refreshed.token).toBe("secret-token-2");
    expect(loads).toBe(2);

    // Negative cache (~10s)
    let negLoads = 0;
    const neg1 = await cache.getOrLoad("missing", async () => {
      negLoads++;
      return null;
    });
    expect(neg1).toEqual({ token: null, fingerprint: null });
    now += 9_000;
    await cache.getOrLoad("missing", async () => {
      negLoads++;
      return "later-token";
    });
    expect(negLoads).toBe(1);
    now += 2_000;
    const neg2 = await cache.getOrLoad("missing", async () => {
      negLoads++;
      return "later-token";
    });
    expect(neg2.token).toBe("later-token");
    expect(negLoads).toBe(2);
  });

  test("RequestPolicy strips Authorization and PRIVATE-TOKEN on cross-origin redirects", async () => {
    const seenHeaders: Array<{ url: string; auth: string | null; privateToken: string | null }> = [];
    const fetchFn: FetchFn = async (input, init) => {
      const url = String(input);
      const headers = new Headers(init?.headers);
      seenHeaders.push({
        url,
        auth: headers.get("authorization"),
        privateToken: headers.get("private-token"),
      });
      if (url === "https://api.github.com/start") {
        return new Response(null, {
          status: 302,
          headers: { location: "https://api.github.com/same-origin" },
        });
      }
      if (url === "https://api.github.com/same-origin") {
        return new Response(null, {
          status: 302,
          headers: { location: "https://evil.example.net/leaked" },
        });
      }
      return jsonResponse({ ok: true });
    };

    const policy = new RequestPolicy({ fetchFn });
    const res = await policy.fetch({
      provider: "github",
      host: "github.com",
      url: "https://api.github.com/start",
      init: {
        headers: {
          Authorization: "Bearer super-secret",
          "PRIVATE-TOKEN": "gl-secret",
        },
      },
    });
    expect(res.ok).toBe(true);
    expect(seenHeaders).toEqual([
      {
        url: "https://api.github.com/start",
        auth: "Bearer super-secret",
        privateToken: "gl-secret",
      },
      {
        url: "https://api.github.com/same-origin",
        auth: "Bearer super-secret",
        privateToken: "gl-secret",
      },
      {
        url: "https://evil.example.net/leaked",
        auth: null,
        privateToken: null,
      },
    ]);
  });

  test("RequestPolicy enforces rate-limit backoff for 429 and 403 x-ratelimit-remaining: 0", async () => {
    let now = 2_000_000;
    let calls = 0;
    const fetchFn: FetchFn = async () => {
      calls++;
      if (calls === 1) {
        return new Response("Too Many Requests", {
          status: 429,
          headers: { "retry-after": "60" },
        });
      }
      if (calls === 2) {
        return new Response("API rate limit exceeded", {
          status: 403,
          headers: {
            "x-ratelimit-remaining": "0",
            "x-ratelimit-reset": String(Math.floor((now + 120_000) / 1_000)),
          },
        });
      }
      return jsonResponse({ ok: true });
    };

    const policy = new RequestPolicy({ fetchFn, nowFn: () => now });

    // 1st call triggers 429 -> pauses for 60s
    await expect(
      policy.fetch({ provider: "github", host: "github.com", url: "https://api.github.com/user" }),
    ).rejects.toMatchObject({ reason: "rate-limited", retryAtMs: now + 60_000 });
    expect(calls).toBe(1);

    // Immediate retry is rejected locally without hitting fetchFn
    await expect(
      policy.fetch({ provider: "github", host: "github.com", url: "https://api.github.com/user" }),
    ).rejects.toMatchObject({ reason: "rate-limited", retryAtMs: now + 60_000 });
    expect(calls).toBe(1);

    // Advance past 60s -> hits 403 rate limit -> pauses 120s
    now += 61_000;
    await expect(
      policy.fetch({ provider: "github", host: "github.com", url: "https://api.github.com/user" }),
    ).rejects.toMatchObject({ reason: "rate-limited" });
    expect(calls).toBe(2);

    // Advance past 120s -> succeeds
    now += 121_000;
    const okRes = await policy.fetch({
      provider: "github",
      host: "github.com",
      url: "https://api.github.com/user",
    });
    expect(okRes.ok).toBe(true);
    expect(calls).toBe(3);
  });

  test("RequestPolicy enforces per-host concurrency cap", async () => {
    let inFlight = 0;
    let maxObserved = 0;
    const fetchFn: FetchFn = async () => {
      inFlight++;
      maxObserved = Math.max(maxObserved, inFlight);
      await new Promise((r) => setTimeout(r, 15));
      inFlight--;
      return jsonResponse({ ok: true });
    };

    const policy = new RequestPolicy({ fetchFn, maxConcurrentPerHost: 3 });
    await Promise.all(
      Array.from({ length: 10 }, () =>
        policy.fetch({
          provider: "github",
          host: "github.com",
          url: "https://api.github.com/user",
        }),
      ),
    );
    expect(maxObserved).toBe(3);
  });
});

describe("aggregateCheckRuns & dedupeCheckRuns", () => {
  test("dedupes reruns and applies failure > pending > success precedence", () => {
    const deduped = dedupeCheckRuns([
      {
        check: { name: "test", status: "failure" },
        workflowName: "CI",
        at: "2026-01-01T00:00:00Z",
      },
      {
        check: { name: "test", status: "success" },
        workflowName: "CI",
        at: "2026-01-01T00:05:00Z",
      },
      {
        check: { name: "lint", status: "pending" },
        workflowName: "CI",
        at: "2026-01-01T00:02:00Z",
      },
    ]);
    expect(deduped).toEqual([
      { name: "test", status: "success" },
      { name: "lint", status: "pending" },
    ]);

    expect(aggregateCheckRuns(deduped)).toEqual({
      state: "pending",
      passed: 1,
      total: 2,
      failing_name: "",
    });

    const failing: Array<CheckRunDetail> = [
      { name: "build", status: "success" },
      { name: "unit", status: "failure" },
      { name: "e2e", status: "pending" },
    ];
    expect(aggregateCheckRuns(failing)).toEqual({
      state: "failure",
      passed: 1,
      total: 3,
      failing_name: "unit",
    });

    // Rerun passed after earlier failure
    expect(
      aggregateCheckRuns([
        { name: "unit", status: "failure" },
        { name: "unit", status: "success" },
      ]),
    ).toEqual({
      state: "success",
      passed: 1,
      total: 1,
      failing_name: "",
    });

    expect(aggregateCheckRuns([])).toBeUndefined();
  });
});

describe("Host-scoped token isolation (zero cross-host leakage)", () => {
  test("GitHub isolates GH_TOKEN to github.com/*.ghe.com and GH_ENTERPRISE_TOKEN to matching GH_HOST", async () => {
    const cliCalls: Array<ExecCliInput> = [];
    const execCliFn = async (input: ExecCliInput): Promise<ExecCliResult> => {
      cliCalls.push(input);
      if (input.args.includes("github.other.internal")) {
        return { stdout: "cli-other-token\n", stderr: "", exitCode: 0 };
      }
      return { stdout: "", stderr: "not logged in", exitCode: 1 };
    };

    const provider = new GitHubProvider({
      env: {
        GH_TOKEN: "public-gh-token",
        GH_HOST: "github.corp.internal",
        GH_ENTERPRISE_TOKEN: "corp-ent-token",
      },
      execCliFn,
    });

    expect((await provider.resolveToken("github.com")).token).toBe("public-gh-token");
    expect((await provider.resolveToken("acme.ghe.com")).token).toBe("public-gh-token");
    expect((await provider.resolveToken("github.corp.internal")).token).toBe("corp-ent-token");

    // Unmatched enterprise host MUST NOT receive GH_TOKEN or GH_ENTERPRISE_TOKEN;
    // falls back to `gh auth token --hostname` with token env vars cleared and GH_PROMPT_DISABLED=1.
    expect((await provider.resolveToken("github.other.internal")).token).toBe("cli-other-token");
    expect(cliCalls).toHaveLength(1);
    expect(cliCalls[0]?.command).toBe("gh");
    expect(cliCalls[0]?.args).toEqual(["auth", "token", "--hostname", "github.other.internal"]);
    expect(cliCalls[0]?.env).toMatchObject({
      GH_TOKEN: undefined,
      GITHUB_TOKEN: undefined,
      GH_ENTERPRISE_TOKEN: undefined,
      GITHUB_ENTERPRISE_TOKEN: undefined,
      GH_HOST: undefined,
      GH_PROMPT_DISABLED: "1",
    });
  });

  test("GitLab isolates GITLAB_TOKEN to matching GITLAB_HOST (default gitlab.com)", async () => {
    const cliCalls: Array<ExecCliInput> = [];
    const provider = new GitLabProvider({
      env: {
        GITLAB_TOKEN: "gl-public-token",
      },
      execCliFn: async (input) => {
        cliCalls.push(input);
        return { stdout: "", stderr: "no token", exitCode: 1 };
      },
    });

    expect((await provider.resolveToken("gitlab.com")).token).toBe("gl-public-token");
    expect((await provider.resolveToken("gitlab.corp.internal")).token).toBeNull();
    expect(cliCalls[0]?.command).toBe("glab");
    expect(cliCalls[0]?.args).toEqual(["config", "get", "token", "--host", "gitlab.corp.internal"]);
    expect(cliCalls[0]?.env).toMatchObject({
      GITLAB_TOKEN: undefined,
      GITLAB_ACCESS_TOKEN: undefined,
      OAUTH_TOKEN: undefined,
      GITLAB_HOST: undefined,
    });
  });

  test("Forgejo isolates CODEBERG_TOKEN, FORGEJO_TOKEN, and GITEA_TOKEN by host", async () => {
    const provider = new ForgejoProvider({
      env: {
        CODEBERG_TOKEN: "cb-secret",
        FORGEJO_HOST: "forgejo.corp.local:3000",
        FORGEJO_TOKEN: "fj-corp-secret",
        GITEA_HOST: "gitea.internal",
        GITEA_TOKEN: "gt-internal-secret",
      },
      readFileFn: async () => {
        throw new Error("ENOENT");
      },
      execCliFn: async () => ({ stdout: "[]", stderr: "", exitCode: 0 }),
    });

    expect((await provider.resolveToken("codeberg.org")).token).toBe("cb-secret");
    expect((await provider.resolveToken("forgejo.corp.local:3000")).token).toBe("fj-corp-secret");
    expect((await provider.resolveToken("gitea.internal")).token).toBe("gt-internal-secret");
    // Unmatched host gets null (zero leakage!)
    expect((await provider.resolveToken("forgejo.other.org")).token).toBeNull();
  });
});

describe("GitHubProvider", () => {
  test("batches >32 branches into multiple GraphQL queries and aggregates CheckRun + StatusContext", async () => {
    const graphqlRequests: Array<{ query: string; variables: Record<string, string> }> = [];
    const fetchFn: FetchFn = async (input, init) => {
      expect(String(input)).toBe("https://api.github.com/graphql");
      const body = JSON.parse(String(init?.body)) as {
        query: string;
        variables: Record<string, string>;
      };
      graphqlRequests.push(body);

      const repoData: Record<string, unknown> = {};
      for (const [k, branch] of Object.entries(body.variables)) {
        if (!k.startsWith("head_")) continue;
        const idx = k.slice(5);
        if (branch === "feat-0") {
          repoData[`pr_${idx}`] = {
            nodes: [
              {
                number: 101,
                title: "Add forge layer",
                state: "OPEN",
                isDraft: false,
                url: "https://github.com/acme/spaces/pull/101",
                updatedAt: "2026-11-28T10:00:00Z",
                baseRefName: "main",
                author: { login: "alice" },
                commits: {
                  nodes: [
                    {
                      commit: {
                        statusCheckRollup: {
                          contexts: {
                            nodes: [
                              {
                                __typename: "CheckRun",
                                name: "test",
                                status: "COMPLETED",
                                conclusion: "FAILURE",
                                completedAt: "2026-11-28T09:50:00Z",
                              },
                              {
                                __typename: "CheckRun",
                                name: "test",
                                status: "COMPLETED",
                                conclusion: "SUCCESS",
                                completedAt: "2026-11-28T09:59:00Z",
                              },
                              {
                                __typename: "StatusContext",
                                context: "ci/deploy-preview",
                                state: "PENDING",
                                description: "Deploying",
                                createdAt: "2026-11-28T09:58:00Z",
                              },
                            ],
                          },
                        },
                      },
                    },
                  ],
                },
              },
            ],
          };
        } else {
          repoData[`pr_${idx}`] = { nodes: [] };
        }
        repoData[`ref_${idx}`] = null;
      }

      return jsonResponse({ data: { repository: repoData } });
    };

    const policy = new RequestPolicy({ fetchFn });
    const provider = new GitHubProvider({
      policy,
      tokens: { "github.com": "ghp_test" },
    });

    const branches = Array.from({ length: 35 }, (_, i) => `feat-${i}`);
    const result = await provider.getChangeRequestsForBranches({
      repo: {
        kind: "github",
        host: "github.com",
        repository: "acme/spaces",
        remote_url: "git@github.com:acme/spaces.git",
      },
      branches,
    });

    expect(graphqlRequests).toHaveLength(2); // 32 + 3
    expect(result["feat-0"]?.changeRequest?.number).toBe(101);
    expect(result["feat-0"]?.changeRequest?.checks_state).toBe("pending");
    expect(result["feat-0"]?.checks).toEqual({
      state: "pending",
      passed: 1,
      total: 2,
      failing_name: "",
    });
  });

  test("falls back to REST /pulls when GraphQL fails and propagates GraphQL RATE_LIMITED", async () => {
    let mode: "fallback" | "ratelimit" = "fallback";
    const urls: Array<string> = [];
    const fetchFn: FetchFn = async (input) => {
      const url = String(input);
      urls.push(url);
      if (url.endsWith("/graphql")) {
        if (mode === "ratelimit") {
          return jsonResponse({
            errors: [{ type: "RATE_LIMITED", message: "API rate limit exceeded" }],
          });
        }
        return jsonResponse({
          errors: [{ message: "Something went wrong on GraphQL" }],
        });
      }
      return jsonResponse([
        {
          number: 42,
          title: "Fallback PR",
          state: "open",
          draft: true,
          html_url: "https://github.com/acme/spaces/pull/42",
          user: { login: "bob" },
          head: { ref: "fix-bug" },
          base: { ref: "main" },
        },
      ]);
    };

    const provider = new GitHubProvider({
      policy: new RequestPolicy({ fetchFn }),
      tokens: { "github.com": "ghp_test" },
    });

    const repo = {
      kind: "github" as const,
      host: "github.com",
      repository: "acme/spaces",
      remote_url: "https://github.com/acme/spaces.git",
    };

    const fallbackRes = await provider.getChangeRequestForBranch({
      repo,
      branch: "fix-bug",
    });
    expect(fallbackRes?.number).toBe(42);
    expect(fallbackRes?.is_draft).toBe(true);
    expect(urls.some((u) => u.includes("/repos/acme/spaces/pulls"))).toBe(true);

    mode = "ratelimit";
    await expect(
      provider.getChangeRequestForBranch({ repo, branch: "fix-bug" }),
    ).rejects.toMatchObject({ reason: "rate-limited", provider: "github" });
  });
});

describe("GitLabProvider", () => {
  test("supports nested groups, MR checks, and glab auth status refinement", async () => {
    const requestedUrls: Array<string> = [];
    const fetchFn: FetchFn = async (input, init) => {
      const url = String(input);
      requestedUrls.push(url);
      expect(new Headers(init?.headers).get("private-token")).toBe("glpat-nested");
      if (url.includes("/merge_requests")) {
        return jsonResponse([
          {
            iid: 19,
            title: "Nested group MR",
            state: "opened",
            draft: false,
            source_branch: "feat/nested",
            target_branch: "main",
            web_url: "https://gitlab.corp.io/org/team/sub/repo/-/merge_requests/19",
            sha: "abc123",
            author: { username: "carol" },
            head_pipeline: { status: "running" },
          },
        ]);
      }
      if (url.includes("/commits/abc123/statuses")) {
        return jsonResponse([
          { name: "rspec", status: "success", finished_at: "2026-11-28T10:00:00Z" },
          { name: "rubocop", status: "failed", finished_at: "2026-11-28T10:01:00Z" },
        ]);
      }
      return jsonResponse({ username: "carol" });
    };

    const provider = new GitLabProvider({
      policy: new RequestPolicy({ fetchFn }),
      tokens: { "gitlab.corp.io": "glpat-nested" },
      execCliFn: async (input) => {
        if (input.command === "glab" && input.args[0] === "auth") {
          return {
            stdout:
              "git.selfhosted.internal\n  ✓ Logged in to git.selfhosted.internal as carol (oauth_token)\n",
            stderr: "",
            exitCode: 0,
          };
        }
        return { stdout: "", stderr: "", exitCode: 1 };
      },
    });

    const repo = {
      kind: "gitlab" as const,
      host: "gitlab.corp.io",
      repository: "org/team/sub/repo",
      remote_url: "git@gitlab.corp.io:org/team/sub/repo.git",
      base_url: "https://gitlab.corp.io",
    };

    const mr = await provider.getChangeRequestForBranch({
      repo,
      branch: "feat/nested",
    });
    expect(mr?.number).toBe(19);
    expect(mr?.state).toBe("OPEN");
    expect(mr?.checks_state).toBe("failure");
    expect(requestedUrls[0]).toContain("/api/v4/projects/org%2Fteam%2Fsub%2Frepo/merge_requests");

    // glab auth status parsing & unknown remote refinement
    const parsedHosts = parseGitLabAuthStatusHosts(
      "git.selfhosted.internal\n  ✓ Logged in to git.selfhosted.internal as carol\n",
    );
    expect(parsedHosts).toEqual([{ host: "git.selfhosted.internal", account: "carol" }]);

    const refined = await provider.refineUnknownRemote({
      cwd: "/tmp/repo",
      remote: parseGitRemote("git@git.selfhosted.internal:org/sub/app.git")!,
    });
    expect(refined).toEqual({
      kind: "gitlab",
      host: "git.selfhosted.internal",
      repository: "org/sub/app",
      remote_url: "git@git.selfhosted.internal:org/sub/app.git",
      base_url: "https://git.selfhosted.internal",
    });
  });
});

describe("ForgejoProvider", () => {
  test("discovers fj keys.json, preserves ports/subpaths, and fetches PRs + commit statuses", async () => {
    expect(forgejoKeysPaths({ platform: "linux", home: "/home/dev" })).toEqual([
      "/home/dev/.local/share/forgejo-cli/keys.json",
    ]);
    expect(forgejoKeysPaths({ platform: "darwin", home: "/Users/dev" })).toEqual([
      "/Users/dev/Library/Application Support/forgejo-cli.forgejo-cli/keys.json",
      "/Users/dev/Library/Application Support/Cyborus.forgejo-cli/keys.json",
    ]);

    const keysJson = JSON.stringify({
      hosts: {
        "https://git.internal:3000/git": { type: "Application", token: "fj-subpath-token" },
        "codeberg.org": { type: "Application", token: "fj-codeberg-token" },
      },
    });
    expect(parseForgejoKeysJson(keysJson)?.hosts["codeberg.org"]?.token).toBe("fj-codeberg-token");

    const seenUrls: Array<string> = [];
    const fetchFn: FetchFn = async (input, init) => {
      const url = String(input);
      seenUrls.push(url);
      expect(new Headers(init?.headers).get("authorization")).toBe("token fj-subpath-token");
      if (url.includes("/pulls")) {
        return jsonResponse([
          {
            number: 7,
            title: "Subpath Forgejo PR",
            state: "open",
            draft: false,
            merged: false,
            html_url: "https://git.internal:3000/git/alice/demo/pulls/7",
            user: { login: "alice" },
            head: { ref: "feat/subpath", sha: "deadbeef" },
            base: { ref: "main" },
          },
        ]);
      }
      if (url.includes("/statuses/deadbeef")) {
        return jsonResponse([
          { context: "woodpecker/test", status: "success", updated_at: "2026-11-28T10:00:00Z" },
        ]);
      }
      return jsonResponse({ login: "alice" });
    };

    const provider = new ForgejoProvider({
      policy: new RequestPolicy({ fetchFn }),
      platform: "linux",
      home: "/home/dev",
      env: {},
      readFileFn: async (p) => {
        if (p === "/home/dev/.local/share/forgejo-cli/keys.json") return keysJson;
        throw new Error("ENOENT");
      },
      execCliFn: async () => ({ stdout: "[]", stderr: "", exitCode: 0 }),
    });

    const refined = await provider.refineUnknownRemote({
      cwd: "/tmp/demo",
      remote: parseGitRemote("https://git.internal:3000/git/alice/demo.git")!,
    });
    expect(refined).toEqual({
      kind: "forgejo",
      host: "git.internal:3000",
      repository: "alice/demo",
      remote_url: "https://git.internal:3000/git/alice/demo.git",
      base_url: "https://git.internal:3000/git",
    });

    const pr = await provider.getChangeRequestForBranch({
      repo: refined!,
      branch: "feat/subpath",
    });
    expect(pr?.number).toBe(7);
    expect(pr?.checks_state).toBe("success");
    expect(seenUrls[0]).toBe(
      "https://git.internal:3000/git/api/v1/repos/alice/demo/pulls?state=all&limit=50",
    );
    expect(seenUrls[1]).toBe(
      "https://git.internal:3000/git/api/v1/repos/alice/demo/statuses/deadbeef",
    );
  });

  test("refines unknown remote via tea login list --output json", async () => {
    const teaJson = JSON.stringify([
      {
        name: "local-gitea",
        url: "http://code.corp:8080/forge",
        ssh_host: "code.corp",
        user: "dev",
        default: "true",
      },
    ]);
    expect(parseTeaLoginsJson(teaJson)).toHaveLength(1);

    const provider = new ForgejoProvider({
      env: {},
      readFileFn: async () => {
        throw new Error("ENOENT");
      },
      execCliFn: async (input) => {
        if (input.command === "tea") {
          return { stdout: teaJson, stderr: "", exitCode: 0 };
        }
        return { stdout: "", stderr: "", exitCode: 1 };
      },
    });

    const refined = await provider.refineUnknownRemote({
      cwd: "/tmp/repo",
      remote: parseGitRemote("http://code.corp:8080/forge/bob/app.git")!,
    });
    expect(refined).toEqual({
      kind: "forgejo",
      host: "code.corp:8080",
      repository: "bob/app",
      remote_url: "http://code.corp:8080/forge/bob/app.git",
      base_url: "http://code.corp:8080/forge",
    });
  });
});

describe("ForgeRegistry two-phase resolution & negative caching", () => {
  test("recognizes GitHub, GitLab, and Forgejo statically in Phase 1", () => {
    const registry = createDefaultForgeRegistry({ env: {} });

    expect(registry.recognizeRemoteSync("git@github.com:org/repo.git")?.repo.kind).toBe("github");
    expect(registry.recognizeRemoteSync("https://gitlab.com/a/b/c.git")?.repo.kind).toBe("gitlab");
    expect(registry.recognizeRemoteSync("https://codeberg.org/alice/demo.git")?.repo.kind).toBe(
      "forgejo",
    );
    expect(
      registry.recognizeRemoteSync("https://forgejo.example.com/git/alice/demo.git")?.repo,
    ).toEqual({
      kind: "forgejo",
      host: "forgejo.example.com",
      repository: "alice/demo",
      remote_url: "https://forgejo.example.com/git/alice/demo.git",
      base_url: "https://forgejo.example.com/git",
    });
  });

  test("caches negative unknown only when all Phase 2 providers definitively settled", async () => {
    let probeCalls = 0;
    let shouldFail = true;

    const flakyProvider: ForgeProvider = {
      kind: "gitlab",
      definition: {
        kind: "gitlab",
        label: "Flaky",
        publicHost: null,
        terminology: {
          shortLabel: "MR",
          shortLabelWithArticle: "an MR",
          singular: "merge request",
          plural: "merge requests",
        },
        recognizeRemote: () => null,
        parseChangeRequestUrl: () => null,
      },
      capabilities: {
        diff: false,
        comment: false,
        actions: [],
        mergeMethods: [],
        review: false,
        search: false,
      },
      refineUnknownRemote: async () => {
        probeCalls++;
        if (shouldFail) {
          throw new Error("transient CLI error");
        }
        return null;
      },
      getViewer: async () => "user",
      listChangeRequests: async () => [],
      getChangeRequestForBranch: async () => null,
    };

    const registry = new ForgeRegistry();
    registry.register(flakyProvider);

    // 1st call fails -> must NOT cache negative result
    expect(await registry.resolveRepo("/tmp", "https://unknown.example.com/a/b.git")).toBeNull();
    expect(probeCalls).toBe(1);

    // 2nd call succeeds with null -> definitively settled -> cached!
    shouldFail = false;
    expect(await registry.resolveRepo("/tmp", "https://unknown.example.com/a/b.git")).toBeNull();
    expect(probeCalls).toBe(2);

    // 3rd call hits negative cache -> probeCalls stays 2
    expect(await registry.resolveRepo("/tmp", "https://unknown.example.com/a/b.git")).toBeNull();
    expect(probeCalls).toBe(2);
  });
});

describe("IssueTrackerRegistry", () => {
  test("registers standalone and embedded issue trackers, parses URLs, and probes credentials", async () => {
    const linearStub: StandaloneIssueTrackerProvider = {
      kind: "linear",
      standalone: {
        label: "Linear",
        publicHost: "linear.app",
        credentialKind: "api-key",
        tokenHint: "lin_api_...",
      },
      capabilities: {
        create: true,
        comment: true,
        edit: true,
        labels: true,
        assignees: true,
        setState: true,
        search: true,
        reactions: false,
        templates: false,
      },
      getViewer: async () => "alice@acme.dev",
      listIssues: async () => ({ items: [], nextCursor: null }),
      getIssue: async (target) => ({
        tracker: "linear",
        host: target.host,
        scope: target.scope,
        key: target.key,
        display_key: target.key,
        title: "Sample issue",
        state: { category: "open", name: "In Progress" },
        url: `https://linear.app/acme/issue/${target.key}`,
        labels: ["backend"],
        body: "Details",
        states: [
          { category: "open", name: "Todo" },
          { category: "open", name: "In Progress" },
          { category: "closed", name: "Done", close_reason: "completed" },
        ],
        comments: [],
        commentsCursor: null,
      }),
      parseIssueUrl: (url): (IssueLink & { workspace?: string }) | null => {
        if (url.hostname.toLowerCase() !== "linear.app") return null;
        const m = /^\/([^/]+)\/issue\/([A-Z]+-\d+)(?:\/|$)/u.exec(url.pathname);
        if (!m?.[1] || !m[2]) return null;
        const team = m[2].split("-")[0]!;
        return {
          tracker: "linear",
          host: "linear.app",
          scope: team,
          key: m[2],
          url: url.toString(),
          workspace: m[1],
        };
      },
      probeCredential: async (token) => ({
        account: token === "lin_api_ok" ? "alice@acme.dev" : "unknown",
        workspace: "acme",
        scopes: [{ scope: "ENG", name: "Engineering", key: "ENG" }],
      }),
    };

    const githubIssuesStub: IssueTrackerProvider = {
      kind: "github",
      capabilities: {
        create: true,
        comment: true,
        edit: true,
        labels: true,
        assignees: true,
        setState: true,
        search: true,
        reactions: true,
        templates: true,
      },
      getViewer: async () => "alice",
      listIssues: async () => ({ items: [], nextCursor: null }),
      getIssue: async (target) => ({
        tracker: "github",
        host: target.host,
        scope: target.scope,
        key: target.key,
        display_key: `#${target.key}`,
        title: "GH issue",
        state: { category: "open", name: "Open" },
        url: `https://${target.host}/${target.scope}/issues/${target.key}`,
        labels: [],
        body: "",
        states: [
          { category: "open", name: "Open" },
          { category: "closed", name: "Closed" },
        ],
        comments: [],
        commentsCursor: null,
      }),
    };

    const registry = new IssueTrackerRegistry();
    registry.register(linearStub).register(githubIssuesStub);

    expect(registry.list()).toHaveLength(2);
    expect(registry.listStandalone().map((p) => p.kind)).toEqual(["linear"]);
    expect(isStandaloneIssueTracker(linearStub)).toBe(true);
    expect(isStandaloneIssueTracker(githubIssuesStub)).toBe(false);

    const parsed = registry.parseIssueUrl("https://linear.app/acme/issue/ENG-123/add-forge");
    expect(parsed?.provider.kind).toBe("linear");
    expect(parsed?.link).toMatchObject({
      tracker: "linear",
      host: "linear.app",
      scope: "ENG",
      key: "ENG-123",
    });

    const probe = await linearStub.probeCredential("lin_api_ok");
    expect(probe).toEqual({
      account: "alice@acme.dev",
      workspace: "acme",
      scopes: [{ scope: "ENG", name: "Engineering", key: "ENG" }],
    });

    const ghProvider = new GitHubProvider();
    expect(registry.resolveForForge(ghProvider)?.kind).toBe("github");
  });
});

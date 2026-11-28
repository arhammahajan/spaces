import type {
  ChangeRequestSummary,
  CheckRunDetail,
  CheckState,
  CheckSummary,
  ForgeKind,
  ForgeRepoRef,
} from "@spaces/types";
import type { IssueTrackerProvider } from "../issues/types";

export interface GitRemoteHost {
  readonly remoteUrl: string;
  /** Lower-case host; keeps port for HTTP/HTTPS remotes, omits port for SSH remotes. */
  readonly host: string;
  /** Lower-case hostname without port. */
  readonly hostname: string;
  /** Repository path without leading/trailing slashes or `.git` suffix. */
  readonly path: string;
}

export interface ChangeRequestTerminology {
  readonly shortLabel: string;
  readonly shortLabelWithArticle: string;
  readonly singular: string;
  readonly plural: string;
}

export interface ParsedChangeRequestUrl {
  readonly host: string;
  readonly repository: string;
  readonly number: number;
  readonly repositoryUrl: string;
  readonly authority?: string;
}

/**
 * Pure metadata and static URL/remote parsing for a git forge.
 */
export interface ForgeDefinition {
  readonly kind: ForgeKind;
  readonly label: string;
  readonly publicHost: string | null;
  readonly terminology: ChangeRequestTerminology;
  readonly recognizeRemote: (remote: GitRemoteHost) => ForgeRepoRef | null;
  readonly parseChangeRequestUrl: (url: URL) => ParsedChangeRequestUrl | null;
  readonly changeRequestHeadRef?: (number: number) => string;
}

export type ForgeMergeMethod = "merge" | "squash" | "rebase";

export interface ForgeCapabilities {
  readonly diff: boolean;
  readonly comment: boolean;
  readonly actions: ReadonlyArray<string>;
  readonly mergeMethods: ReadonlyArray<ForgeMergeMethod>;
  readonly review: boolean;
  readonly search: boolean;
}

export type ForgeErrorReason =
  | "missing-tool"
  | "unauthenticated"
  | "forbidden"
  | "rate-limited"
  | "not-found"
  | "failed";

export class ForgeError extends Error {
  readonly reason: ForgeErrorReason;
  readonly provider: ForgeKind;
  readonly retryAtMs?: number;
  readonly statusCode?: number;

  constructor(opts: {
    reason: ForgeErrorReason;
    provider: ForgeKind;
    message: string;
    retryAtMs?: number;
    statusCode?: number;
    cause?: unknown;
  }) {
    super(opts.message, { cause: opts.cause });
    this.name = "ForgeError";
    this.reason = opts.reason;
    this.provider = opts.provider;
    this.retryAtMs = opts.retryAtMs;
    this.statusCode = opts.statusCode;
  }
}

export interface BranchChangeRequestResult {
  readonly changeRequest?: ChangeRequestSummary;
  readonly checks?: CheckSummary;
  readonly checkRuns?: ReadonlyArray<CheckRunDetail>;
}

export interface ListChangeRequestsInput {
  readonly cwd?: string;
  readonly repo: ForgeRepoRef;
  readonly state?: "open" | "closed" | "merged" | "all";
  readonly limit?: number;
  readonly branch?: string;
  readonly signal?: AbortSignal;
}

export interface ForgeProvider {
  readonly kind: ForgeKind;
  readonly definition: ForgeDefinition;
  readonly capabilities: ForgeCapabilities;
  readonly issues?: IssueTrackerProvider;

  readonly refineUnknownRemote?: (input: {
    readonly cwd: string;
    readonly remote: GitRemoteHost;
    readonly signal?: AbortSignal;
  }) => Promise<ForgeRepoRef | null>;

  readonly getViewer: (input: {
    readonly cwd?: string;
    readonly host: string;
    readonly baseUrl?: string;
    readonly signal?: AbortSignal;
  }) => Promise<string>;

  readonly listChangeRequests: (
    input: ListChangeRequestsInput,
  ) => Promise<Array<ChangeRequestSummary>>;

  readonly getChangeRequestForBranch: (input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branch: string;
    readonly signal?: AbortSignal;
  }) => Promise<ChangeRequestSummary | null>;

  readonly getChangeRequestsForBranches?: (input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly branches: ReadonlyArray<string>;
    readonly signal?: AbortSignal;
  }) => Promise<Record<string, BranchChangeRequestResult>>;

  readonly getChecks?: (input: {
    readonly cwd?: string;
    readonly repo: ForgeRepoRef;
    readonly ref: string;
    readonly signal?: AbortSignal;
  }) => Promise<Array<CheckRunDetail>>;
}

export interface TimestampedCheckRun {
  readonly check: CheckRunDetail;
  readonly workflowName?: string | null;
  readonly at?: string | null;
  readonly sequence?: number;
}

function isAtLeastAsNew(
  candidate: string | null | undefined,
  kept: string | null | undefined,
): boolean {
  if (!candidate) return !kept;
  if (!kept) return true;
  const candidateTime = Date.parse(candidate);
  const keptTime = Date.parse(kept);
  if (Number.isNaN(candidateTime) || Number.isNaN(keptTime)) {
    return candidate >= kept;
  }
  if (candidateTime !== keptTime) {
    return candidateTime > keptTime;
  }
  return candidate >= kept;
}

/**
 * Deduplicates reruns of checks by `(workflowName, name)`, keeping the newest entry
 * and sorting failing checks first.
 */
export function dedupeCheckRuns(
  entries: ReadonlyArray<TimestampedCheckRun>,
): Array<CheckRunDetail> {
  const newestByKey = new Map<string, TimestampedCheckRun>();

  for (const entry of entries) {
    const key = JSON.stringify([entry.workflowName ?? "", entry.check.name]);
    const kept = newestByKey.get(key);
    if (kept !== undefined) {
      if (!isAtLeastAsNew(entry.at, kept.at)) {
        continue;
      }
      if (
        entry.sequence !== undefined &&
        kept.sequence !== undefined &&
        entry.sequence < kept.sequence &&
        isAtLeastAsNew(kept.at, entry.at)
      ) {
        continue;
      }
    }
    newestByKey.set(key, entry);
  }

  const survivors = [...newestByKey.values()];
  const countsByName = new Map<string, number>();
  for (const entry of survivors) {
    countsByName.set(entry.check.name, (countsByName.get(entry.check.name) ?? 0) + 1);
  }

  return survivors
    .map((entry) => {
      const workflowName = entry.workflowName ?? "";
      return workflowName.length > 0 && (countsByName.get(entry.check.name) ?? 0) > 1
        ? { ...entry.check, name: `${workflowName} / ${entry.check.name}` }
        : entry.check;
    })
    .sort((a, b) => Number(b.status === "failure") - Number(a.status === "failure"));
}

/**
 * Aggregates check runs into a single `CheckSummary`, deduplicating reruns by name
 * (keeping the latest occurrence) and applying failure > pending > success precedence.
 */
export function aggregateCheckRuns(
  checks: ReadonlyArray<CheckRunDetail>,
): CheckSummary | undefined {
  if (checks.length === 0) {
    return undefined;
  }

  // Dedupe reruns by check name, keeping the latest occurrence.
  const latestByName = new Map<string, CheckRunDetail>();
  let unnamedIndex = 0;
  for (const check of checks) {
    const trimmedName = check.name.trim();
    const key = trimmedName.length > 0 ? trimmedName : `__unnamed_${unnamedIndex++}`;
    latestByName.set(key, check);
  }

  let passed = 0;
  let failed = 0;
  let pending = 0;
  let skipped = 0;
  let failingName = "";

  for (const check of latestByName.values()) {
    switch (check.status) {
      case "success":
        passed++;
        break;
      case "failure":
        failed++;
        if (failingName === "" && check.name.trim() !== "") {
          failingName = check.name.trim();
        }
        break;
      case "pending":
        pending++;
        break;
      case "none":
        skipped++;
        break;
    }
  }

  const total = passed + failed + pending;
  if (total === 0) {
    if (skipped > 0) {
      return { state: "success", passed: 0, total: 0, failing_name: "" };
    }
    return undefined;
  }

  if (failed > 0) {
    return {
      state: "failure",
      passed,
      total,
      failing_name: failingName,
    };
  }

  if (pending > 0) {
    return {
      state: "pending",
      passed,
      total,
      failing_name: "",
    };
  }

  return {
    state: "success",
    passed,
    total,
    failing_name: "",
  };
}

export function rollupCheckState(
  checks: ReadonlyArray<CheckRunDetail>,
): CheckState | undefined {
  return aggregateCheckRuns(checks)?.state;
}

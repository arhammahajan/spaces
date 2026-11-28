import type {
  IssueLink,
  IssueState,
  IssueSummary,
  IssueTrackerKind,
} from "@spaces/types";

export type IssueTrackerErrorReason =
  | "missing-tool"
  | "unauthenticated"
  | "forbidden"
  | "rate-limited"
  | "not-found"
  | "failed";

export class IssueTrackerError extends Error {
  readonly tracker: IssueTrackerKind;
  readonly reason: IssueTrackerErrorReason;
  readonly retryAtMs?: number;
  readonly statusCode?: number;

  constructor(opts: {
    tracker: IssueTrackerKind;
    reason: IssueTrackerErrorReason;
    message: string;
    retryAtMs?: number;
    statusCode?: number;
    cause?: unknown;
  }) {
    super(opts.message, { cause: opts.cause });
    this.name = "IssueTrackerError";
    this.tracker = opts.tracker;
    this.reason = opts.reason;
    this.retryAtMs = opts.retryAtMs;
    this.statusCode = opts.statusCode;
  }
}

export interface IssueCapabilities {
  readonly create: boolean;
  readonly comment: boolean;
  readonly edit: boolean;
  readonly labels: boolean;
  readonly assignees: boolean;
  readonly setState: boolean;
  readonly search: boolean;
  readonly reactions: boolean;
  readonly templates: boolean;
}

/**
 * Addressing seam for issue trackers: `(host, scope, key)`.
 * - `host`: where the tracker lives (`github.com`, `linear.app`, `acme.atlassian.net`).
 * - `scope`: container within the host (`owner/repo`, Linear team key, Jira project key).
 * - `key`: issue identifier within the scope (`"123"`, `"ENG-42"`).
 */
export interface IssueScopeTarget {
  readonly cwd?: string;
  readonly host: string;
  readonly scope: string;
}

export interface IssueTarget extends IssueScopeTarget {
  readonly key: string;
}

export interface IssueListInput extends IssueScopeTarget {
  readonly state?: "open" | "closed" | "all";
  readonly limit?: number;
  readonly query?: string;
  readonly labels?: ReadonlyArray<string>;
  readonly assignee?: string;
  readonly cursor?: string;
  readonly signal?: AbortSignal;
}

export interface IssuePage {
  readonly items: ReadonlyArray<IssueSummary>;
  readonly nextCursor: string | null;
}

export interface IssueComment {
  readonly id: string;
  readonly author?: string;
  readonly body: string;
  readonly createdAtMs?: number;
  readonly url?: string;
}

export interface IssueDetail extends IssueSummary {
  readonly body: string;
  readonly states: ReadonlyArray<IssueState>;
  readonly comments: ReadonlyArray<IssueComment>;
  readonly commentsCursor: string | null;
}

export interface IssueTrackerScope {
  readonly scope: string;
  readonly name: string;
  readonly key?: string;
}

export interface IssueTrackerProvider {
  readonly kind: IssueTrackerKind;
  readonly capabilities: IssueCapabilities;

  readonly getViewer: (input: {
    readonly cwd?: string;
    readonly host: string;
    readonly signal?: AbortSignal;
  }) => Promise<string>;

  readonly listIssues: (input: IssueListInput) => Promise<IssuePage>;

  readonly getIssue: (
    input: IssueTarget & { readonly signal?: AbortSignal },
  ) => Promise<IssueDetail>;

  readonly parseIssueUrl?: (url: URL) => IssueLink | null;

  readonly createIssue?: (
    input: IssueScopeTarget & {
      readonly title: string;
      readonly body?: string;
      readonly labels?: ReadonlyArray<string>;
      readonly assignees?: ReadonlyArray<string>;
      readonly signal?: AbortSignal;
    },
  ) => Promise<IssueSummary>;

  readonly commentIssue?: (
    input: IssueTarget & {
      readonly body: string;
      readonly signal?: AbortSignal;
    },
  ) => Promise<void>;

  readonly setIssueState?: (
    input: IssueTarget & {
      readonly state: string;
      readonly signal?: AbortSignal;
    },
  ) => Promise<void>;
}

export interface StandaloneIssueTrackerDescriptor {
  readonly label: string;
  readonly publicHost: string;
  readonly credentialKind: "api-key" | "oauth";
  readonly tokenHint?: string;
}

export interface ProbeCredentialResult {
  readonly account: string;
  readonly workspace?: string;
  readonly scopes: ReadonlyArray<IssueTrackerScope>;
}

/**
 * Standalone issue tracker reached via its own credential (e.g. Linear, Jira)
 * rather than a git checkout's forge credentials.
 */
export interface StandaloneIssueTrackerProvider extends IssueTrackerProvider {
  readonly standalone: StandaloneIssueTrackerDescriptor;
  readonly parseIssueUrl: (
    url: URL,
  ) => (IssueLink & { readonly workspace?: string }) | null;
  readonly probeCredential: (
    token: string,
    signal?: AbortSignal,
  ) => Promise<ProbeCredentialResult>;
}

export function isStandaloneIssueTracker(
  provider: IssueTrackerProvider,
): provider is StandaloneIssueTrackerProvider {
  return "standalone" in provider;
}

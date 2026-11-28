import type { TurnUsage } from "@spaces/types";
import type { VendorSessionLookup } from "../resurrect/capture";

export type AccessLevel = "read-only" | "default" | "auto" | "full";

export interface HarnessProfileConfig {
  agent: string;
  name: string;
  displayName?: string;
  enabled: boolean;
  binaryPath?: string;
  homePath?: string;
  shadowHomePath?: string;
  env: Record<string, string>;
  prependArgs: string[];
  router: boolean;
  overlay?: boolean;
}

export interface InteractiveLaunchRequest {
  cwd: string;
  profile?: string;
  sessionId?: string;
  accessLevel?: AccessLevel;
  model?: string;
  effort?: string;
  extraArgs?: string[];
  noRouter?: boolean;
  homeDir?: string;
  spacesDir?: string;
  env?: Record<string, string | undefined>;
}

export interface InteractiveLaunchSpec {
  argv: string[];
  env: Record<string, string>;
  unsetEnv: string[];
  cwd: string;
  shadowHome?: string;
  configDir?: string;
}

export interface HeadlessStartRequest {
  threadId: string;
  cwd: string;
  profile?: string;
  sessionId?: string;
  accessLevel?: AccessLevel;
  model?: string;
  effort?: string;
  extraArgs?: string[];
  noRouter?: boolean;
  homeDir?: string;
  spacesDir?: string;
  env?: Record<string, string | undefined>;
}

export type HeadlessTurnEvent =
  | { type: "session_init"; sessionId: string; model?: string }
  | { type: "text_delta"; text: string }
  | { type: "tool_use"; id?: string; name: string; input?: unknown }
  | { type: "tool_result"; id?: string; output?: string; isError?: boolean }
  | { type: "permission_request"; id?: string; tool: string; description?: string }
  | {
      type: "completed";
      sessionId?: string;
      replyPreview?: string;
      model?: string;
      usage?: TurnUsage;
    }
  | { type: "failed"; code: string; message: string; retryable: boolean };

export interface HeadlessTurnResult {
  sessionId?: string;
  replyPreview?: string;
  model?: string;
  usage?: TurnUsage;
}

export interface HeadlessSession {
  readonly agent: string;
  readonly threadId: string;
  getSessionId(): string | undefined;
  isRunning(): boolean;
  sendTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult>;
  cancel(reason?: string): Promise<void>;
  stopAndFlush(): Promise<{ sessionId?: string; usage?: TurnUsage }>;
}

export interface DeltaCursor {
  sessionPath: string;
  byteOffset: number;
  partialLine?: string;
}

export interface ParsedDiskTurn {
  role: "user" | "assistant";
  text: string;
  timestampMs?: number;
  model?: string;
  stopReason?: string;
  usage?: TurnUsage;
}

export interface DeltaReadResult {
  nextCursor: DeltaCursor;
  turns: ParsedDiskTurn[];
  title?: string;
  model?: string;
  usage?: TurnUsage;
}

export interface StopAndFlushOptions {
  sessionId?: string;
  cwd: string;
  profile?: string;
  configDir?: string;
  cursor?: DeltaCursor;
  headlessSession?: HeadlessSession | null;
  stopInteractive?: () => Promise<void> | void;
}

export interface HarnessPluginSpec {
  id: string;
  aliases: string[];
  displayApp: string;
  cliDisplayApp: string;
  binary: string;
  screenManifestId: string;
  oscAuthority: boolean;
  resumeFlag?: string;
  resumeSubcommand?: string;
  positionalResume: boolean;
  uniqueExplicitSessions: boolean;
  titlePrefixes: string[];
  mcpTargets: string[];
}

/**
 * Unified TypeScript HarnessPlugin contract merging `internal/agent` and
 * `internal/harness` plus the agent-specific resume/discovery seams removed
 * from shared `resurrect/`.
 */
export interface HarnessPlugin {
  readonly spec: HarnessPluginSpec;

  resumeArgs(sessionId: string): string[];
  discoverLiveSessionId?(pid: number): string;
  resolveVendorSession?: VendorSessionLookup;

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec;
  startHeadless(req: HeadlessStartRequest): HeadlessSession;
  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null>;
  readDelta(
    sessionId: string,
    cwd: string,
    cursor?: DeltaCursor,
    configDir?: string,
  ): DeltaReadResult;
}

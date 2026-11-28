import type { TurnUsage } from "@spaces/types";
import type {
  HeadlessSession,
  HeadlessTurnEvent,
  HeadlessTurnResult,
  InteractiveLaunchSpec,
} from "../plugins/types";

/**
 * Minimal child process handle matching `Bun.spawn(..., { stdin: "pipe", stdout: "pipe", stderr: "pipe" })`
 * while remaining straightforward to mock in unit tests.
 */
export interface SpawnedProcess {
  stdin: {
    write(data: string | Uint8Array): void | number | Promise<number>;
    end(): void | number | Promise<void> | Promise<number>;
  };
  stdout: ReadableStream<Uint8Array>;
  stderr?: ReadableStream<Uint8Array>;
  exited: Promise<number>;
  kill(signal?: NodeJS.Signals | number): void;
}

/**
 * Process spawner abstraction injected into `ClaudeHeadlessExecutor`.
 */
export type ClaudeSpawnFn = (
  argv: string[],
  opts: { cwd: string; env: Record<string, string | undefined> },
) => SpawnedProcess;

/**
 * Options for `buildClaudeHeadlessArgv`.
 */
export interface BuildClaudeHeadlessArgvOptions {
  sessionId?: string;
  resume?: boolean;
  permissionPromptsHost?: boolean;
}

/**
 * Options for constructing `ClaudeHeadlessExecutor`.
 */
export interface ClaudeHeadlessExecutorOptions {
  threadId: string;
  launch: InteractiveLaunchSpec;
  sessionId?: string;
  resume?: boolean;
  permissionPromptsHost?: boolean;
  spawn?: ClaudeSpawnFn;
  cancelGraceMs?: number;
}

/**
 * Structured error thrown when a Claude headless turn fails, cancels, or aborts.
 */
export class ClaudeHeadlessError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable = false) {
    super(message);
    this.name = "ClaudeHeadlessError";
    this.code = code;
    this.retryable = retryable;
  }
}

const DEFAULT_CANCEL_GRACE_MS = 500;
const STDERR_TAIL_MAX_CHARS = 4096;
const EXIT_STDOUT_DRAIN_WAIT_MS = 20;

const CORE_VALUE_FLAGS = new Set(["--input-format", "--output-format"]);
const CORE_BOOL_FLAGS = new Set([
  "-p",
  "--print",
  "--verbose",
  "--include-partial-messages",
  "--replay-user-messages",
]);
const SESSION_VALUE_FLAGS = new Set(["-r", "--resume", "--session-id"]);

/**
 * Default process spawner backed by `Bun.spawn`.
 */
export function defaultClaudeSpawn(
  argv: string[],
  opts: { cwd: string; env: Record<string, string | undefined> },
): SpawnedProcess {
  return Bun.spawn(argv, {
    cwd: opts.cwd,
    env: opts.env,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  });
}

function matchesFlag(token: string, flag: string): boolean {
  return token === flag || token.startsWith(`${flag}=`);
}

function isFlagInSet(token: string, flags: ReadonlySet<string>): string | undefined {
  for (const flag of flags) {
    if (matchesFlag(token, flag)) {
      return flag;
    }
  }
  return undefined;
}

function consumeFlagPair(
  args: string[],
  index: number,
  token: string,
  flag: string,
  allowBareOptional = false,
): { nextIndex: number; value?: string } {
  if (token.startsWith(`${flag}=`)) {
    const val = token.slice(flag.length + 1);
    return { nextIndex: index, value: val };
  }
  const next = args[index + 1];
  if (next !== undefined && (!allowBareOptional || !next.startsWith("-"))) {
    return { nextIndex: index + 1, value: next };
  }
  return { nextIndex: index, value: undefined };
}

/**
 * Transforms an interactive launch `argv` into a bidirectional `stream-json`
 * Claude Code headless invocation while deduplicating conflicting flags.
 */
export function buildClaudeHeadlessArgv(
  launchArgv: string[],
  opts: BuildClaudeHeadlessArgvOptions = {},
): string[] {
  const binary = launchArgv[0]?.trim() || "claude";
  const rest = launchArgv.slice(1);
  const explicitSessionId = opts.sessionId?.trim();
  const hasExplicitSession = Boolean(explicitSessionId);

  const forwardedFlags: string[] = [];
  let preservedResumePair: string[] | undefined;
  let preservedSessionIdPair: string[] | undefined;

  for (let i = 0; i < rest.length; i += 1) {
    const tok = rest[i]!;
    if (tok === "--") {
      forwardedFlags.push(...rest.slice(i));
      break;
    }

    if (isFlagInSet(tok, CORE_BOOL_FLAGS)) {
      continue;
    }

    const coreValFlag = isFlagInSet(tok, CORE_VALUE_FLAGS);
    if (coreValFlag) {
      const consumed = consumeFlagPair(rest, i, tok, coreValFlag);
      i = consumed.nextIndex;
      continue;
    }

    if (opts.permissionPromptsHost && matchesFlag(tok, "--permission-prompts")) {
      const consumed = consumeFlagPair(rest, i, tok, "--permission-prompts");
      i = consumed.nextIndex;
      continue;
    }

    if (hasExplicitSession && (matchesFlag(tok, "-c") || matchesFlag(tok, "--continue"))) {
      continue;
    }

    const sessionFlag = isFlagInSet(tok, SESSION_VALUE_FLAGS);
    if (sessionFlag) {
      const isResume = sessionFlag === "-r" || sessionFlag === "--resume";
      const consumed = consumeFlagPair(rest, i, tok, sessionFlag, isResume);
      i = consumed.nextIndex;
      if (!hasExplicitSession) {
        if (isResume) {
          preservedResumePair =
            consumed.value !== undefined ? ["--resume", consumed.value] : [tok];
        } else {
          preservedSessionIdPair =
            consumed.value !== undefined ? ["--session-id", consumed.value] : [tok];
        }
      }
      continue;
    }

    forwardedFlags.push(tok);
  }

  if (hasExplicitSession && explicitSessionId) {
    forwardedFlags.push(opts.resume ? "--resume" : "--session-id", explicitSessionId);
  } else {
    if (preservedResumePair) {
      forwardedFlags.push(...preservedResumePair);
    }
    if (preservedSessionIdPair) {
      forwardedFlags.push(...preservedSessionIdPair);
    }
  }

  if (opts.permissionPromptsHost) {
    forwardedFlags.push("--permission-prompts", "host");
  }

  return [
    binary,
    "-p",
    "--input-format",
    "stream-json",
    "--output-format",
    "stream-json",
    "--verbose",
    "--include-partial-messages",
    "--replay-user-messages",
    ...forwardedFlags,
  ];
}

interface PendingToolUse {
  id?: string;
  name: string;
  input?: unknown;
  partialJson: string;
}

interface ActiveTurn {
  onEvent?: (event: HeadlessTurnEvent) => void;
  resolve: (result: HeadlessTurnResult) => void;
  reject: (error: Error) => void;
  settled: boolean;
  cancelling: boolean;
  sessionId?: string;
  model?: string;
  accumulatedText: string;
  currentStreamedText: string;
  pendingTools: Map<number, PendingToolUse>;
  emittedToolIds: Set<string>;
  currentMessageUsage?: TurnUsage;
  fallbackUsage?: TurnUsage;
}

interface QueuedTurn {
  prompt: string;
  onEvent?: (event: HeadlessTurnEvent) => void;
  resolve: (result: HeadlessTurnResult) => void;
  reject: (error: Error) => void;
}

interface ProcessHandle {
  proc: SpawnedProcess;
  exited: boolean;
  exitCode?: number;
  exitPromise: Promise<number>;
  stdoutDone: Promise<void>;
  stdoutReader?: ReadableStreamDefaultReader<Uint8Array>;
  stderrDone: Promise<void>;
  stderrReader?: ReadableStreamDefaultReader<Uint8Array>;
  stderrTail: string;
  lastStreamError?: { code?: string; message: string; retryable?: boolean };
  cancelled: boolean;
  terminatePromise?: Promise<void>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim().length > 0 ? value : undefined;
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function firstFiniteNumber(...values: unknown[]): number | undefined {
  for (const val of values) {
    const num = finiteNumber(val);
    if (num !== undefined) {
      return num;
    }
  }
  return undefined;
}

function roundUsd(amount: number): number {
  return Math.round(amount * 1e12) / 1e12;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function extractSessionIdFromArgv(argv: string[]): string | undefined {
  for (let i = 1; i < argv.length; i += 1) {
    const tok = argv[i]!;
    for (const flag of ["--resume", "-r", "--session-id"]) {
      if (tok === flag && i + 1 < argv.length) {
        const val = argv[i + 1]!.trim();
        if (val && !val.startsWith("-")) {
          return val;
        }
      }
      const prefix = `${flag}=`;
      if (tok.startsWith(prefix)) {
        const val = tok.slice(prefix.length).trim();
        if (val) {
          return val;
        }
      }
    }
  }
  return undefined;
}

function hasOnlySessionIdFlag(argv: string[]): boolean {
  let sawSessionId = false;
  let sawResume = false;
  for (let i = 1; i < argv.length; i += 1) {
    const tok = argv[i]!;
    if (matchesFlag(tok, "--session-id")) {
      sawSessionId = true;
    } else if (matchesFlag(tok, "--resume") || matchesFlag(tok, "-r")) {
      sawResume = true;
    }
  }
  return sawSessionId && !sawResume;
}

function buildProcessEnv(launch: InteractiveLaunchSpec): Record<string, string | undefined> {
  const env: Record<string, string | undefined> = {
    ...process.env,
    ...launch.env,
  };
  for (const key of launch.unsetEnv) {
    delete env[key];
  }
  return env;
}

function extractTextFromContent(content: unknown): string | undefined {
  if (typeof content === "string") {
    return content;
  }
  if (!Array.isArray(content)) {
    return undefined;
  }
  const parts: string[] = [];
  for (const item of content) {
    if (typeof item === "string") {
      parts.push(item);
      continue;
    }
    if (isRecord(item) && typeof item.text === "string") {
      parts.push(item.text);
    }
  }
  return parts.length > 0 ? parts.join("\n") : undefined;
}

function parseUsageObject(
  usageRaw: unknown,
  topLevelObj?: Record<string, unknown>,
): TurnUsage | undefined {
  const u = isRecord(usageRaw) ? usageRaw : undefined;
  const rawCost = firstFiniteNumber(
    topLevelObj?.total_cost_usd,
    topLevelObj?.cost_usd,
    topLevelObj?.costUSD,
    u?.cost_usd,
    u?.costUsd,
    u?.costUSD,
  );

  if (!u && rawCost === undefined) {
    return undefined;
  }

  const inputTokens =
    firstFiniteNumber(u?.input_tokens, u?.inputTokens) ?? 0;
  const outputTokens =
    firstFiniteNumber(u?.output_tokens, u?.outputTokens) ?? 0;
  const reasoningTokens =
    firstFiniteNumber(
      u?.reasoning_tokens,
      u?.reasoningTokens,
      u?.thinking_tokens,
      u?.thinkingTokens,
    ) ?? 0;
  const cacheReadTokens =
    firstFiniteNumber(
      u?.cache_read_tokens,
      u?.cacheReadTokens,
      u?.cache_read_input_tokens,
      u?.cacheReadInputTokens,
    ) ?? 0;
  const cacheWriteTokens =
    firstFiniteNumber(
      u?.cache_write_tokens,
      u?.cacheWriteTokens,
      u?.cache_creation_input_tokens,
      u?.cacheCreationInputTokens,
    ) ?? 0;

  const explicitCostPriced =
    typeof topLevelObj?.cost_priced === "boolean"
      ? topLevelObj.cost_priced
      : typeof u?.cost_priced === "boolean"
        ? u.cost_priced
        : undefined;
  const costUsd = rawCost ?? 0;
  const costPriced =
    explicitCostPriced !== undefined
      ? explicitCostPriced
      : rawCost !== undefined && rawCost > 0;

  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    reasoning_tokens: reasoningTokens,
    cache_read_tokens: cacheReadTokens,
    cache_write_tokens: cacheWriteTokens,
    cost_usd: roundUsd(costUsd),
    cost_priced: costPriced,
  };
}

function parseModelUsageMap(
  modelUsageRaw: unknown,
  topLevelObj: Record<string, unknown>,
): { usage?: TurnUsage; model?: string } {
  if (!isRecord(modelUsageRaw)) {
    return {};
  }
  const entries = Object.entries(modelUsageRaw).filter(([, val]) => isRecord(val));
  if (entries.length === 0) {
    return {};
  }

  let acc: TurnUsage | undefined;
  let primaryModel: string | undefined;
  for (const [modelName, rawStats] of entries) {
    if (!primaryModel && modelName.trim().length > 0) {
      primaryModel = modelName;
    }
    const parsed = parseUsageObject(rawStats);
    if (parsed) {
      acc = addTurnUsage(acc, parsed);
    }
  }

  const topCost = firstFiniteNumber(
    topLevelObj.total_cost_usd,
    topLevelObj.cost_usd,
    topLevelObj.costUSD,
  );
  if (acc && topCost !== undefined && acc.cost_usd === 0) {
    acc = {
      ...acc,
      cost_usd: roundUsd(topCost),
      cost_priced: acc.cost_priced || topCost > 0,
    };
  }

  return { usage: acc, model: primaryModel };
}

function addTurnUsage(
  acc: TurnUsage | undefined,
  next: TurnUsage | undefined,
): TurnUsage | undefined {
  if (!next) {
    return acc;
  }
  if (!acc) {
    return { ...next };
  }
  const nextHasTokens =
    next.input_tokens > 0 ||
    next.output_tokens > 0 ||
    next.reasoning_tokens > 0 ||
    next.cache_read_tokens > 0 ||
    next.cache_write_tokens > 0;
  const costPriced = next.cost_priced
    ? acc.cost_priced || acc.cost_usd === 0
    : nextHasTokens
      ? false
      : acc.cost_priced;

  return {
    input_tokens: acc.input_tokens + next.input_tokens,
    output_tokens: acc.output_tokens + next.output_tokens,
    reasoning_tokens: acc.reasoning_tokens + next.reasoning_tokens,
    cache_read_tokens: acc.cache_read_tokens + next.cache_read_tokens,
    cache_write_tokens: acc.cache_write_tokens + next.cache_write_tokens,
    cost_usd: roundUsd(acc.cost_usd + next.cost_usd),
    cost_priced: costPriced,
  };
}

function inferRetryable(code: string, message: string): boolean {
  const haystack = `${code} ${message}`.toLowerCase();
  return (
    haystack.includes("rate_limit") ||
    haystack.includes("overloaded") ||
    haystack.includes("timeout") ||
    haystack.includes("temporarily")
  );
}

function extractResultError(obj: Record<string, unknown>): {
  code: string;
  message: string;
  retryable: boolean;
} {
  const errObj = isRecord(obj.error) ? obj.error : undefined;
  const rawSubtype = nonEmptyString(obj.subtype);
  const code =
    nonEmptyString(obj.code) ??
    nonEmptyString(obj.error_code) ??
    nonEmptyString(errObj?.code) ??
    (rawSubtype && rawSubtype !== "success" ? rawSubtype : undefined) ??
    "turn_error";

  let errorsMessage: string | undefined;
  if (Array.isArray(obj.errors)) {
    const msgs = obj.errors
      .map((item) => {
        if (typeof item === "string") return item.trim();
        if (isRecord(item)) return nonEmptyString(item.message) ?? "";
        return "";
      })
      .filter((s) => s.length > 0);
    if (msgs.length > 0) {
      errorsMessage = msgs.join("; ");
    }
  }

  const message =
    nonEmptyString(obj.message) ??
    (typeof obj.error === "string" ? nonEmptyString(obj.error) : nonEmptyString(errObj?.message)) ??
    errorsMessage ??
    nonEmptyString(obj.result) ??
    "Claude turn failed";

  const retryable =
    typeof obj.retryable === "boolean"
      ? obj.retryable
      : typeof errObj?.retryable === "boolean"
        ? errObj.retryable
        : inferRetryable(code, message);

  return { code, message, retryable };
}

/**
 * Bidirectional `stream-json` headless executor for Claude Code CLI implementing
 * the unified `HeadlessSession` contract.
 */
export class ClaudeHeadlessExecutor implements HeadlessSession {
  readonly agent = "claude";
  readonly threadId: string;

  private readonly launch: InteractiveLaunchSpec;
  private readonly explicitResume?: boolean;
  private readonly permissionPromptsHost?: boolean;
  private readonly spawnFn: ClaudeSpawnFn;
  private readonly cancelGraceMs: number;

  private sessionId?: string;
  private lastModel?: string;
  private cumulativeUsage?: TurnUsage;

  private activeProc: ProcessHandle | null = null;
  private activeTurn: ActiveTurn | null = null;
  private readonly pendingQueue: QueuedTurn[] = [];
  private pumping = false;
  private stopped = false;
  private stopPromise: Promise<{ sessionId?: string; usage?: TurnUsage }> | null = null;

  constructor(options: ClaudeHeadlessExecutorOptions) {
    this.threadId = options.threadId;
    this.launch = options.launch;
    this.sessionId =
      nonEmptyString(options.sessionId) ?? extractSessionIdFromArgv(options.launch.argv);
    this.explicitResume = options.resume;
    this.permissionPromptsHost = options.permissionPromptsHost;
    this.spawnFn = options.spawn ?? defaultClaudeSpawn;
    this.cancelGraceMs = Math.max(1, options.cancelGraceMs ?? DEFAULT_CANCEL_GRACE_MS);
  }

  getSessionId(): string | undefined {
    return this.sessionId;
  }

  isRunning(): boolean {
    return this.activeProc !== null && !this.activeProc.exited && !this.stopped;
  }

  /**
   * Queues a prompt turn onto the bidirectional `stream-json` session,
   * serializing concurrent calls and streaming `HeadlessTurnEvent` updates.
   */
  sendTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    if (this.stopped) {
      return Promise.reject(
        new ClaudeHeadlessError("session_stopped", "Claude headless session has been stopped"),
      );
    }
    return new Promise<HeadlessTurnResult>((resolve, reject) => {
      this.pendingQueue.push({ prompt, onEvent, resolve, reject });
      if (!this.pumping) {
        this.pumping = true;
        queueMicrotask(() => {
          void this.pumpTurns();
        });
      }
    });
  }

  /**
   * Interrupts the running child process with `SIGINT`, falling back to `SIGTERM`
   * after `cancelGraceMs` if it remains alive, and rejects any in-flight turn.
   */
  async cancel(reason?: string): Promise<void> {
    const message = nonEmptyString(reason) ? `Turn cancelled: ${reason}` : "Turn cancelled";
    const active = this.activeTurn;
    if (active) {
      active.cancelling = true;
      this.activeTurn = null;
    }
    const queued = this.pendingQueue.splice(0);

    const handle = this.activeProc;
    if (handle && !handle.exited) {
      handle.cancelled = true;
      await this.terminateHandle(handle, true);
    }
    if (handle && this.activeProc === handle) {
      this.activeProc = null;
    }

    if (active) {
      this.settleTurnFailure(active, "cancelled", message, false);
    }
    for (const item of queued) {
      item.reject(new ClaudeHeadlessError("cancelled", message, false));
    }
  }

  /**
   * Closes `stdin` to allow graceful shutdown, escalates to `SIGINT`/`SIGTERM`
   * if the process ignores EOF beyond `cancelGraceMs`, and returns final
   * `sessionId` plus cumulative `TurnUsage`.
   */
  async stopAndFlush(): Promise<{ sessionId?: string; usage?: TurnUsage }> {
    if (this.stopPromise) {
      return this.stopPromise;
    }
    this.stopped = true;
    const queued = this.pendingQueue.splice(0);

    this.stopPromise = (async () => {
      const handle = this.activeProc;
      if (handle && !handle.exited) {
        try {
          await Promise.resolve(handle.proc.stdin.end());
        } catch {
          // Ignore stdin close errors if child pipe already broke.
        }

        const exitedCleanly = await this.waitForExit(handle, this.cancelGraceMs);
        if (!exitedCleanly && !handle.exited) {
          handle.cancelled = true;
          await this.terminateHandle(handle, true);
        }
      }

      if (handle) {
        await Promise.race([handle.stdoutDone, sleep(EXIT_STDOUT_DRAIN_WAIT_MS)]);
        this.cancelReaders(handle);
        if (this.activeProc === handle) {
          this.activeProc = null;
        }
      }

      const active = this.activeTurn;
      if (active && !active.settled) {
        this.settleTurnFailure(
          active,
          "session_stopped",
          "Claude headless session stopped before turn completed",
          false,
        );
      }
      for (const item of queued) {
        item.reject(
          new ClaudeHeadlessError(
            "session_stopped",
            "Claude headless session stopped before queued turn started",
            false,
          ),
        );
      }

      return {
        sessionId: this.sessionId,
        usage: this.cumulativeUsage ? { ...this.cumulativeUsage } : undefined,
      };
    })();

    return this.stopPromise;
  }

  private async pumpTurns(): Promise<void> {
    try {
      while (this.pendingQueue.length > 0) {
        const next = this.pendingQueue.shift()!;
        if (this.stopped) {
          next.reject(
            new ClaudeHeadlessError("session_stopped", "Claude headless session has been stopped"),
          );
          continue;
        }
        try {
          const result = await this.executeSingleTurn(next.prompt, next.onEvent);
          next.resolve(result);
        } catch (err) {
          next.reject(err instanceof Error ? err : new Error(String(err)));
        }
      }
    } finally {
      this.pumping = false;
    }
  }

  private rejectQueuedTurns(code: string, message: string, retryable: boolean): void {
    while (this.pendingQueue.length > 0) {
      const queued = this.pendingQueue.shift()!;
      queued.reject(new ClaudeHeadlessError(code, message, retryable));
    }
  }

  private ensureProcess(): ProcessHandle {
    if (this.activeProc && !this.activeProc.exited) {
      return this.activeProc;
    }

    const resume =
      this.explicitResume !== undefined
        ? this.explicitResume
        : Boolean(this.sessionId) && !hasOnlySessionIdFlag(this.launch.argv);

    const argv = buildClaudeHeadlessArgv(this.launch.argv, {
      sessionId: this.sessionId,
      resume,
      permissionPromptsHost: this.permissionPromptsHost,
    });
    const cwd = this.launch.cwd || process.cwd();
    const env = buildProcessEnv(this.launch);
    const proc = this.spawnFn(argv, { cwd, env });

    const handle: ProcessHandle = {
      proc,
      exited: false,
      exitPromise: proc.exited.then(
        (code) => code,
        () => -1,
      ),
      stdoutDone: Promise.resolve(),
      stderrDone: Promise.resolve(),
      stderrTail: "",
      cancelled: false,
    };

    handle.stdoutDone = this.readStdoutLoop(handle);
    if (proc.stderr) {
      handle.stderrDone = this.readStderrLoop(handle);
    }

    void handle.exitPromise.then((code) => this.handleProcessExit(handle, code));
    this.activeProc = handle;
    return handle;
  }

  private async executeSingleTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    let handle: ProcessHandle;
    try {
      handle = this.ensureProcess();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const failure = new ClaudeHeadlessError("spawn_failed", message, false);
      onEvent?.({ type: "failed", code: failure.code, message: failure.message, retryable: false });
      throw failure;
    }

    return new Promise<HeadlessTurnResult>((resolve, reject) => {
      const turn: ActiveTurn = {
        onEvent,
        resolve,
        reject,
        settled: false,
        cancelling: false,
        sessionId: this.sessionId,
        model: this.lastModel,
        accumulatedText: "",
        currentStreamedText: "",
        pendingTools: new Map(),
        emittedToolIds: new Set(),
      };
      this.activeTurn = turn;

      const payload =
        JSON.stringify({
          type: "user",
          message: {
            role: "user",
            content: [{ type: "text", text: prompt }],
          },
        }) + "\n";

      try {
        const writeResult = handle.proc.stdin.write(payload);
        if (writeResult && typeof (writeResult as Promise<number>).then === "function") {
          void (writeResult as Promise<number>).catch((err) => {
            const msg = err instanceof Error ? err.message : String(err);
            this.settleTurnFailure(turn, "stdin_write_error", msg, false);
          });
        }
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        this.settleTurnFailure(turn, "stdin_write_error", msg, false);
      }
    });
  }

  private async readStdoutLoop(handle: ProcessHandle): Promise<void> {
    const reader = handle.proc.stdout.getReader();
    handle.stdoutReader = reader;
    const decoder = new TextDecoder("utf-8");
    let lineBuffer = "";

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        if (value) {
          lineBuffer += decoder.decode(value, { stream: true });
          const lines = lineBuffer.split("\n");
          lineBuffer = lines.pop() ?? "";
          for (const rawLine of lines) {
            this.processStdoutLine(handle, rawLine);
          }
        }
      }
      lineBuffer += decoder.decode();
      if (lineBuffer.trim().length > 0) {
        this.processStdoutLine(handle, lineBuffer);
      }
    } catch {
      // Stream cancelled or errored; process exit handler settles any in-flight turn.
    } finally {
      try {
        reader.releaseLock();
      } catch {
        // Ignore already released lock.
      }
    }
  }

  private async readStderrLoop(handle: ProcessHandle): Promise<void> {
    if (!handle.proc.stderr) {
      return;
    }
    const reader = handle.proc.stderr.getReader();
    handle.stderrReader = reader;
    const decoder = new TextDecoder("utf-8");

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        if (value) {
          const text = decoder.decode(value, { stream: true });
          handle.stderrTail = (handle.stderrTail + text).slice(-STDERR_TAIL_MAX_CHARS);
        }
      }
      const rest = decoder.decode();
      if (rest) {
        handle.stderrTail = (handle.stderrTail + rest).slice(-STDERR_TAIL_MAX_CHARS);
      }
    } catch {
      // Ignore stderr read/cancellation errors.
    } finally {
      try {
        reader.releaseLock();
      } catch {
        // Ignore already released lock.
      }
    }
  }

  private async handleProcessExit(handle: ProcessHandle, exitCode: number): Promise<void> {
    handle.exited = true;
    handle.exitCode = exitCode;

    // Give an in-memory or OS pipe a brief window to deliver any buffered lines
    // (such as a final "result" line) before declaring the stream dead.
    await Promise.race([handle.stdoutDone, sleep(EXIT_STDOUT_DRAIN_WAIT_MS)]);
    this.cancelReaders(handle);

    if (this.activeProc === handle) {
      this.activeProc = null;
    }

    const turn = this.activeTurn;
    if (!turn || turn.settled || turn.cancelling || this.stopped) {
      return;
    }

    if (handle.cancelled) {
      this.settleTurnFailure(turn, "cancelled", "Turn cancelled", false);
      return;
    }

    if (handle.lastStreamError) {
      const err = handle.lastStreamError;
      this.settleTurnFailure(
        turn,
        err.code ?? "process_exit",
        err.message,
        err.retryable ?? false,
      );
      this.rejectQueuedTurns(
        err.code ?? "process_exit",
        err.message,
        err.retryable ?? false,
      );
      return;
    }

    const stderrDetail = handle.stderrTail.trim();
    const message =
      exitCode === 0
        ? "Claude process exited before completing turn"
        : stderrDetail
          ? `Claude process exited with code ${exitCode}: ${stderrDetail}`
          : `Claude process exited with code ${exitCode}`;

    this.settleTurnFailure(turn, "process_exit", message, false);
    this.rejectQueuedTurns("process_exit", message, false);
  }

  private processStdoutLine(handle: ProcessHandle, rawLine: string): void {
    const trimmed = rawLine.replace(/\r$/, "").trim();
    if (!trimmed) {
      return;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      return;
    }
    if (!isRecord(parsed)) {
      return;
    }

    const envelopeSessionId = nonEmptyString(parsed.session_id) ?? nonEmptyString(parsed.sessionId);
    if (envelopeSessionId) {
      this.sessionId = envelopeSessionId;
    }

    const turn = this.activeTurn;
    if (!turn || turn.settled || turn.cancelling) {
      return;
    }

    if (envelopeSessionId) {
      turn.sessionId = envelopeSessionId;
    }

    const type = nonEmptyString(parsed.type);
    switch (type) {
      case "system": {
        this.handleSystemMessage(turn, parsed);
        break;
      }
      case "stream_event": {
        this.handleStreamEvent(turn, parsed);
        break;
      }
      case "assistant": {
        this.handleAssistantMessage(turn, parsed);
        break;
      }
      case "user": {
        this.handleUserMessage(turn, parsed);
        break;
      }
      case "tool_result": {
        this.emitToolResult(turn, parsed);
        break;
      }
      case "permission_request": {
        this.emitPermissionRequest(turn, parsed);
        break;
      }
      case "control_request": {
        this.handleControlRequest(turn, parsed);
        break;
      }
      case "error": {
        const extracted = extractResultError(parsed);
        handle.lastStreamError = extracted;
        break;
      }
      case "result": {
        this.handleResultMessage(turn, parsed);
        break;
      }
      default:
        break;
    }
  }

  private handleSystemMessage(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const subtype = nonEmptyString(obj.subtype);
    if (subtype !== "init") {
      return;
    }
    const sessionId =
      nonEmptyString(obj.session_id) ??
      nonEmptyString(obj.sessionId) ??
      this.sessionId ??
      "";
    const model = nonEmptyString(obj.model);
    if (sessionId) {
      this.sessionId = sessionId;
      turn.sessionId = sessionId;
    }
    if (model) {
      this.lastModel = model;
      turn.model = model;
    }
    turn.onEvent?.({
      type: "session_init",
      sessionId,
      ...(model ? { model } : {}),
    });
  }

  private handleStreamEvent(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const ev = isRecord(obj.event) ? obj.event : obj;
    const evType = nonEmptyString(ev.type);

    switch (evType) {
      case "message_start": {
        turn.currentStreamedText = "";
        turn.currentMessageUsage = undefined;
        if (isRecord(ev.message)) {
          const model = nonEmptyString(ev.message.model);
          if (model) {
            this.lastModel = model;
            turn.model = model;
          }
          const msgUsage = parseUsageObject(ev.message.usage);
          if (msgUsage) {
            turn.currentMessageUsage = msgUsage;
          }
        }
        break;
      }

      case "content_block_start": {
        const index = finiteNumber(ev.index) ?? 0;
        const block = isRecord(ev.content_block) ? ev.content_block : undefined;
        if (!block) {
          break;
        }
        const blockType = nonEmptyString(block.type);
        if (blockType === "text" && typeof block.text === "string" && block.text.length > 0) {
          turn.currentStreamedText += block.text;
          turn.accumulatedText += block.text;
          turn.onEvent?.({ type: "text_delta", text: block.text });
        } else if (blockType === "tool_use") {
          const name = nonEmptyString(block.name) ?? "unknown";
          const id = nonEmptyString(block.id);
          turn.pendingTools.set(index, {
            id,
            name,
            input: block.input,
            partialJson: "",
          });
        } else if (blockType === "tool_result") {
          this.emitToolResult(turn, block);
        } else if (blockType === "permission_request") {
          this.emitPermissionRequest(turn, block);
        }
        break;
      }

      case "content_block_delta": {
        const index = finiteNumber(ev.index) ?? 0;
        const delta = isRecord(ev.delta) ? ev.delta : undefined;
        if (!delta) {
          break;
        }
        const deltaType = nonEmptyString(delta.type);
        if (deltaType === "text_delta" && typeof delta.text === "string" && delta.text.length > 0) {
          turn.currentStreamedText += delta.text;
          turn.accumulatedText += delta.text;
          turn.onEvent?.({ type: "text_delta", text: delta.text });
        } else if (deltaType === "input_json_delta" && typeof delta.partial_json === "string") {
          const pending = turn.pendingTools.get(index);
          if (pending) {
            pending.partialJson += delta.partial_json;
          }
        }
        break;
      }

      case "content_block_stop": {
        const index = finiteNumber(ev.index) ?? 0;
        this.flushPendingToolByIndex(turn, index);
        break;
      }

      case "message_delta": {
        const deltaUsage = parseUsageObject(ev.usage);
        if (deltaUsage) {
          const prev = turn.currentMessageUsage;
          turn.currentMessageUsage = prev
            ? {
                input_tokens: deltaUsage.input_tokens || prev.input_tokens,
                output_tokens: deltaUsage.output_tokens || prev.output_tokens,
                reasoning_tokens: deltaUsage.reasoning_tokens || prev.reasoning_tokens,
                cache_read_tokens: deltaUsage.cache_read_tokens || prev.cache_read_tokens,
                cache_write_tokens: deltaUsage.cache_write_tokens || prev.cache_write_tokens,
                cost_usd: deltaUsage.cost_usd || prev.cost_usd,
                cost_priced: deltaUsage.cost_priced || prev.cost_priced,
              }
            : deltaUsage;
        }
        break;
      }

      case "message_stop": {
        this.flushAllPendingTools(turn);
        if (turn.currentMessageUsage) {
          turn.fallbackUsage = addTurnUsage(turn.fallbackUsage, turn.currentMessageUsage);
          turn.currentMessageUsage = undefined;
        }
        break;
      }

      default: {
        // Support flat synthetic stream_event blocks if supplied directly.
        if (isRecord(ev.delta)) {
          const deltaType = nonEmptyString(ev.delta.type);
          if (
            deltaType === "text_delta" &&
            typeof ev.delta.text === "string" &&
            ev.delta.text.length > 0
          ) {
            turn.currentStreamedText += ev.delta.text;
            turn.accumulatedText += ev.delta.text;
            turn.onEvent?.({ type: "text_delta", text: ev.delta.text });
          }
        } else if (isRecord(ev.content_block)) {
          const block = ev.content_block;
          if (nonEmptyString(block.type) === "tool_use") {
            this.emitToolUse(turn, block);
          } else if (nonEmptyString(block.type) === "tool_result") {
            this.emitToolResult(turn, block);
          } else if (nonEmptyString(block.type) === "permission_request") {
            this.emitPermissionRequest(turn, block);
          }
        }
        break;
      }
    }
  }

  private handleAssistantMessage(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const msg = isRecord(obj.message) ? obj.message : obj;
    const model = nonEmptyString(msg.model) ?? nonEmptyString(obj.model);
    if (model) {
      this.lastModel = model;
      turn.model = model;
    }

    const content = msg.content;
    const hadStreamedText = turn.currentStreamedText.length > 0;

    if (typeof content === "string" && content.length > 0) {
      if (!hadStreamedText) {
        turn.accumulatedText += content;
        turn.onEvent?.({ type: "text_delta", text: content });
      }
    } else if (Array.isArray(content)) {
      for (const block of content) {
        if (!isRecord(block)) {
          continue;
        }
        const blockType = nonEmptyString(block.type);
        if (blockType === "text" && typeof block.text === "string" && block.text.length > 0) {
          if (!hadStreamedText) {
            turn.accumulatedText += block.text;
            turn.onEvent?.({ type: "text_delta", text: block.text });
          }
        } else if (blockType === "tool_use") {
          const id = nonEmptyString(block.id);
          if (id) {
            this.deletePendingToolById(turn, id);
          }
          if (!id || !turn.emittedToolIds.has(id)) {
            this.emitToolUse(turn, block);
          }
        } else if (blockType === "tool_result") {
          this.emitToolResult(turn, block);
        } else if (blockType === "permission_request") {
          this.emitPermissionRequest(turn, block);
        }
      }
    }

    // Reset per-message streamed flag so subsequent assistant steps in the same
    // turn (after tool_result) emit text if they don't use stream_event deltas.
    turn.currentStreamedText = "";
    this.flushAllPendingTools(turn);

    if (isRecord(msg.usage)) {
      const msgUsage = parseUsageObject(msg.usage);
      if (msgUsage) {
        turn.currentMessageUsage = undefined;
        turn.fallbackUsage = addTurnUsage(turn.fallbackUsage, msgUsage);
      }
    } else if (turn.currentMessageUsage) {
      turn.fallbackUsage = addTurnUsage(turn.fallbackUsage, turn.currentMessageUsage);
      turn.currentMessageUsage = undefined;
    }
  }

  private handleUserMessage(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const msg = isRecord(obj.message) ? obj.message : obj;
    const content = msg.content;
    if (!Array.isArray(content)) {
      return;
    }
    for (const block of content) {
      if (!isRecord(block)) {
        continue;
      }
      if (nonEmptyString(block.type) === "tool_result") {
        this.emitToolResult(turn, block);
      }
    }
  }

  private handleControlRequest(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const req = isRecord(obj.request) ? obj.request : undefined;
    if (!req) {
      return;
    }
    if (nonEmptyString(req.subtype) === "can_use_tool") {
      const id = nonEmptyString(obj.request_id) ?? nonEmptyString(obj.id);
      const tool = nonEmptyString(req.tool_name) ?? nonEmptyString(req.tool) ?? "unknown";
      const description =
        nonEmptyString(req.decision_reason) ??
        nonEmptyString(req.description) ??
        (req.input !== undefined ? JSON.stringify(req.input) : undefined);
      turn.onEvent?.({
        type: "permission_request",
        ...(id ? { id } : {}),
        tool,
        ...(description ? { description } : {}),
      });
    }
  }

  private handleResultMessage(turn: ActiveTurn, obj: Record<string, unknown>): void {
    this.flushAllPendingTools(turn);

    const sessionId =
      nonEmptyString(obj.session_id) ??
      nonEmptyString(obj.sessionId) ??
      turn.sessionId ??
      this.sessionId;
    if (sessionId) {
      this.sessionId = sessionId;
      turn.sessionId = sessionId;
    }

    const fromModelUsage = parseModelUsageMap(obj.modelUsage, obj);
    const model =
      nonEmptyString(obj.model) ??
      turn.model ??
      fromModelUsage.model ??
      this.lastModel;
    if (model) {
      this.lastModel = model;
    }

    let usage = parseUsageObject(obj.usage, obj) ?? fromModelUsage.usage;
    const accumulatedFallback = addTurnUsage(turn.fallbackUsage, turn.currentMessageUsage);
    turn.currentMessageUsage = undefined;
    if (!usage && accumulatedFallback) {
      const topCost = firstFiniteNumber(obj.total_cost_usd, obj.cost_usd, obj.costUSD);
      usage =
        topCost !== undefined
          ? {
              ...accumulatedFallback,
              cost_usd: roundUsd(topCost),
              cost_priced: accumulatedFallback.cost_priced || topCost > 0,
            }
          : accumulatedFallback;
    }

    if (usage) {
      this.cumulativeUsage = addTurnUsage(this.cumulativeUsage, usage);
    }

    if (obj.is_error === true) {
      const err = extractResultError(obj);
      this.settleTurnFailure(turn, err.code, err.message, err.retryable);
      return;
    }

    const replyPreview =
      typeof obj.result === "string" && obj.result.length > 0
        ? obj.result
        : turn.accumulatedText.length > 0
          ? turn.accumulatedText
          : undefined;

    const result: HeadlessTurnResult = {
      ...(sessionId ? { sessionId } : {}),
      ...(replyPreview !== undefined ? { replyPreview } : {}),
      ...(model ? { model } : {}),
      ...(usage ? { usage } : {}),
    };
    this.settleTurnSuccess(turn, result);
  }

  private flushPendingToolByIndex(turn: ActiveTurn, index: number): void {
    const pending = turn.pendingTools.get(index);
    if (!pending) {
      return;
    }
    turn.pendingTools.delete(index);

    let input = pending.input;
    const rawJson = pending.partialJson.trim();
    if (rawJson.length > 0) {
      try {
        input = JSON.parse(rawJson);
      } catch {
        // Keep initial input or raw partial string if malformed.
        if (input === undefined || (isRecord(input) && Object.keys(input).length === 0)) {
          input = rawJson;
        }
      }
    }

    if (pending.id && turn.emittedToolIds.has(pending.id)) {
      return;
    }
    if (pending.id) {
      turn.emittedToolIds.add(pending.id);
    }
    turn.onEvent?.({
      type: "tool_use",
      ...(pending.id ? { id: pending.id } : {}),
      name: pending.name,
      ...(input !== undefined ? { input } : {}),
    });
  }

  private flushAllPendingTools(turn: ActiveTurn): void {
    const indices = Array.from(turn.pendingTools.keys()).sort((a, b) => a - b);
    for (const index of indices) {
      this.flushPendingToolByIndex(turn, index);
    }
  }

  private deletePendingToolById(turn: ActiveTurn, id: string): void {
    for (const [index, pending] of turn.pendingTools.entries()) {
      if (pending.id === id) {
        turn.pendingTools.delete(index);
      }
    }
  }

  private emitToolUse(turn: ActiveTurn, block: Record<string, unknown>): void {
    const id = nonEmptyString(block.id);
    if (id && turn.emittedToolIds.has(id)) {
      return;
    }
    if (id) {
      turn.emittedToolIds.add(id);
    }
    const name = nonEmptyString(block.name) ?? "unknown";
    turn.onEvent?.({
      type: "tool_use",
      ...(id ? { id } : {}),
      name,
      ...(block.input !== undefined ? { input: block.input } : {}),
    });
  }

  private emitToolResult(turn: ActiveTurn, block: Record<string, unknown>): void {
    const id = nonEmptyString(block.tool_use_id) ?? nonEmptyString(block.id);
    const output =
      typeof block.output === "string"
        ? block.output
        : extractTextFromContent(block.content);
    const isError =
      typeof block.is_error === "boolean"
        ? block.is_error
        : typeof block.isError === "boolean"
          ? block.isError
          : undefined;

    turn.onEvent?.({
      type: "tool_result",
      ...(id ? { id } : {}),
      ...(output !== undefined ? { output } : {}),
      ...(isError !== undefined ? { isError } : {}),
    });
  }

  private emitPermissionRequest(turn: ActiveTurn, block: Record<string, unknown>): void {
    const id = nonEmptyString(block.id) ?? nonEmptyString(block.request_id);
    const tool =
      nonEmptyString(block.tool) ??
      nonEmptyString(block.tool_name) ??
      nonEmptyString(block.name) ??
      "unknown";
    const description =
      nonEmptyString(block.description) ??
      nonEmptyString(block.message) ??
      nonEmptyString(block.reason);

    turn.onEvent?.({
      type: "permission_request",
      ...(id ? { id } : {}),
      tool,
      ...(description ? { description } : {}),
    });
  }

  private settleTurnSuccess(turn: ActiveTurn, result: HeadlessTurnResult): void {
    if (turn.settled) {
      return;
    }
    turn.settled = true;
    if (this.activeTurn === turn) {
      this.activeTurn = null;
    }
    turn.onEvent?.({
      type: "completed",
      ...(result.sessionId ? { sessionId: result.sessionId } : {}),
      ...(result.replyPreview !== undefined ? { replyPreview: result.replyPreview } : {}),
      ...(result.model ? { model: result.model } : {}),
      ...(result.usage ? { usage: result.usage } : {}),
    });
    turn.resolve(result);
  }

  private settleTurnFailure(
    turn: ActiveTurn,
    code: string,
    message: string,
    retryable: boolean,
  ): void {
    if (turn.settled) {
      return;
    }
    turn.settled = true;
    if (this.activeTurn === turn) {
      this.activeTurn = null;
    }
    turn.onEvent?.({
      type: "failed",
      code,
      message,
      retryable,
    });
    turn.reject(new ClaudeHeadlessError(code, message, retryable));
  }

  private async waitForExit(handle: ProcessHandle, timeoutMs: number): Promise<boolean> {
    if (handle.exited) {
      return true;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const exited = await Promise.race([
        handle.exitPromise.then(() => true),
        new Promise<boolean>((resolve) => {
          timer = setTimeout(() => resolve(false), timeoutMs);
        }),
      ]);
      return exited;
    } finally {
      if (timer !== undefined) {
        clearTimeout(timer);
      }
    }
  }

  private terminateHandle(
    handle: ProcessHandle,
    cancelReadersAfterward: boolean,
  ): Promise<void> {
    if (handle.terminatePromise) {
      return handle.terminatePromise;
    }
    handle.terminatePromise = (async () => {
      if (!handle.exited) {
        try {
          handle.proc.kill("SIGINT");
        } catch {
          // Ignore kill failures on already-gone process.
        }
        const exitedOnInt = await this.waitForExit(handle, this.cancelGraceMs);
        if (!exitedOnInt && !handle.exited) {
          try {
            handle.proc.kill("SIGTERM");
          } catch {
            // Ignore kill failures.
          }
          await this.waitForExit(handle, Math.min(this.cancelGraceMs, 50));
        }
      }
      if (cancelReadersAfterward) {
        this.cancelReaders(handle);
      }
    })();
    return handle.terminatePromise;
  }

  private cancelReaders(handle: ProcessHandle): void {
    if (handle.stdoutReader) {
      void handle.stdoutReader.cancel().catch(() => {});
    }
    if (handle.stderrReader) {
      void handle.stderrReader.cancel().catch(() => {});
    }
  }
}

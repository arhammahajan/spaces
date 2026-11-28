import type { TurnUsage } from "@spaces/types";
import type {
  HeadlessSession,
  HeadlessTurnEvent,
  HeadlessTurnResult,
  InteractiveLaunchSpec,
} from "../plugins/types";
import type { SpawnedProcess } from "./claude";

export type { SpawnedProcess } from "./claude";

/**
 * Process spawner abstraction injected into `CodexHeadlessExecutor`.
 */
export type CodexSpawnFn = (
  argv: string[],
  opts: { cwd: string; env: Record<string, string | undefined> },
) => SpawnedProcess;

/**
 * Options for `buildCodexHeadlessArgv`.
 */
export interface BuildCodexHeadlessArgvOptions {
  sessionId?: string;
  prompt: string;
}

/**
 * Options for constructing `CodexHeadlessExecutor`.
 */
export interface CodexHeadlessExecutorOptions {
  threadId: string;
  launch: InteractiveLaunchSpec;
  sessionId?: string;
  spawn?: CodexSpawnFn;
  cancelGraceMs?: number;
}

/**
 * Structured error thrown when a Codex headless turn fails, cancels, or aborts.
 */
export class CodexHeadlessError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable = false) {
    super(message);
    this.name = "CodexHeadlessError";
    this.code = code;
    this.retryable = retryable;
  }
}

const DEFAULT_CANCEL_GRACE_MS = 500;
const STDERR_TAIL_MAX_CHARS = 4096;
const EXIT_STDOUT_DRAIN_WAIT_MS = 20;

/**
 * Default process spawner backed by `Bun.spawn`.
 */
export function defaultCodexSpawn(
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim().length > 0 ? value.trim() : undefined;
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
    if (tok === "resume" && i + 1 < argv.length) {
      const val = argv[i + 1]!.trim();
      if (val && !val.startsWith("-") && val !== "--last") {
        return val;
      }
    }
  }
  return undefined;
}

/**
 * Transforms an interactive Codex launch `argv` into a `codex exec --json`
 * (or `codex exec resume <sessionId> --json`) invocation while stripping
 * duplicate `exec`, `resume <id>`, and `--json` tokens.
 */
export function buildCodexHeadlessArgv(
  launchArgv: string[],
  opts: BuildCodexHeadlessArgvOptions,
): string[] {
  const binary = launchArgv[0]?.trim() || "codex";
  const rest = launchArgv.slice(1);
  const explicitSessionId = nonEmptyString(opts.sessionId);

  const forwardedFlags: string[] = [];
  let preservedResumeId: string | undefined;

  for (let i = 0; i < rest.length; i += 1) {
    const tok = rest[i]!;
    if (tok === "--") {
      forwardedFlags.push(...rest.slice(i));
      break;
    }
    if (tok === "exec" || tok === "--json" || tok === "--experimental-json") {
      continue;
    }
    if (tok === "resume") {
      const next = rest[i + 1];
      if (next !== undefined && !next.startsWith("-")) {
        preservedResumeId = next.trim();
        i += 1;
      }
      continue;
    }
    forwardedFlags.push(tok);
  }

  const effectiveSessionId = explicitSessionId ?? preservedResumeId;
  if (effectiveSessionId) {
    return [
      binary,
      "exec",
      "resume",
      effectiveSessionId,
      "--json",
      ...forwardedFlags,
      opts.prompt,
    ];
  }

  return [binary, "exec", "--json", ...forwardedFlags, opts.prompt];
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
    if (isRecord(item)) {
      const blockType = nonEmptyString(item.type)?.toLowerCase() ?? "";
      if (
        blockType === "" ||
        blockType === "text" ||
        blockType === "output_text" ||
        blockType === "input_text"
      ) {
        if (typeof item.text === "string") {
          parts.push(item.text);
        } else if (typeof item.content === "string") {
          parts.push(item.content);
        }
      }
    }
  }
  return parts.length > 0 ? parts.join("\n") : undefined;
}

function parseCodexUsage(usageRaw: unknown, topLevel?: Record<string, unknown>): TurnUsage | undefined {
  const u = isRecord(usageRaw) ? usageRaw : undefined;
  const rawCost = firstFiniteNumber(
    topLevel?.cost_usd,
    topLevel?.total_cost_usd,
    u?.cost_usd,
    u?.costUsd,
    u?.costUSD,
  );
  if (!u && rawCost === undefined) {
    return undefined;
  }

  const inputTokens = firstFiniteNumber(u?.input_tokens, u?.inputTokens) ?? 0;
  const outputTokens = firstFiniteNumber(u?.output_tokens, u?.outputTokens) ?? 0;
  const reasoningTokens =
    firstFiniteNumber(
      u?.reasoning_output_tokens,
      u?.reasoning_tokens,
      u?.reasoningTokens,
    ) ?? 0;
  const cacheReadTokens =
    firstFiniteNumber(
      u?.cached_input_tokens,
      u?.cache_read_tokens,
      u?.cache_read_input_tokens,
      u?.cacheReadTokens,
    ) ?? 0;
  const cacheWriteTokens =
    firstFiniteNumber(
      u?.cache_write_tokens,
      u?.cache_creation_input_tokens,
      u?.cacheWriteTokens,
    ) ?? 0;

  const costUsd = rawCost ?? 0;
  const explicitPriced =
    typeof u?.cost_priced === "boolean"
      ? u.cost_priced
      : typeof topLevel?.cost_priced === "boolean"
        ? topLevel.cost_priced
        : undefined;
  const costPriced = explicitPriced !== undefined ? explicitPriced : costUsd > 0;

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
  return {
    input_tokens: acc.input_tokens + next.input_tokens,
    output_tokens: acc.output_tokens + next.output_tokens,
    reasoning_tokens: acc.reasoning_tokens + next.reasoning_tokens,
    cache_read_tokens: acc.cache_read_tokens + next.cache_read_tokens,
    cache_write_tokens: acc.cache_write_tokens + next.cache_write_tokens,
    cost_usd: roundUsd(acc.cost_usd + next.cost_usd),
    cost_priced: acc.cost_priced || next.cost_priced || acc.cost_usd + next.cost_usd > 0,
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

function extractCodexError(obj: Record<string, unknown>): {
  code: string;
  message: string;
  retryable: boolean;
} {
  const errObj = isRecord(obj.error) ? obj.error : undefined;
  const code =
    nonEmptyString(obj.code) ??
    nonEmptyString(errObj?.code) ??
    (nonEmptyString(obj.type) === "turn.failed" ? "turn_failed" : "codex_error");
  const message =
    nonEmptyString(errObj?.message) ??
    (typeof obj.error === "string" ? nonEmptyString(obj.error) : undefined) ??
    nonEmptyString(obj.message) ??
    "Codex turn failed";
  const retryable =
    typeof obj.retryable === "boolean"
      ? obj.retryable
      : typeof errObj?.retryable === "boolean"
        ? errObj.retryable
        : inferRetryable(code, message);
  return { code, message, retryable };
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
  usage?: TurnUsage;
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
  cancelled: boolean;
  terminatePromise?: Promise<void>;
}

/**
 * Headless executor for OpenAI Codex CLI (`codex exec --json` /
 * `codex exec resume <sessionId> --json`) implementing `HeadlessSession`.
 */
export class CodexHeadlessExecutor implements HeadlessSession {
  readonly agent = "codex";
  readonly threadId: string;

  private readonly launch: InteractiveLaunchSpec;
  private readonly spawnFn: CodexSpawnFn;
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

  constructor(options: CodexHeadlessExecutorOptions) {
    this.threadId = options.threadId;
    this.launch = options.launch;
    this.sessionId =
      nonEmptyString(options.sessionId) ?? extractSessionIdFromArgv(options.launch.argv);
    this.spawnFn = options.spawn ?? defaultCodexSpawn;
    this.cancelGraceMs = Math.max(1, options.cancelGraceMs ?? DEFAULT_CANCEL_GRACE_MS);
  }

  getSessionId(): string | undefined {
    return this.sessionId;
  }

  isRunning(): boolean {
    return this.activeProc !== null && !this.activeProc.exited && !this.stopped;
  }

  /**
   * Queues a prompt turn onto `codex exec --json` (or `codex exec resume <id> --json`
   * when a session ID is known), serializing concurrent calls and streaming
   * `HeadlessTurnEvent` updates.
   */
  sendTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    if (this.stopped) {
      return Promise.reject(
        new CodexHeadlessError("session_stopped", "Codex headless session has been stopped"),
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
   * Interrupts the active `codex exec` process with `SIGINT`, falling back to
   * `SIGTERM` after `cancelGraceMs`, and rejects any in-flight or queued turns.
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
      item.reject(new CodexHeadlessError("cancelled", message, false));
    }
  }

  /**
   * Stops any active `codex exec` child process, rejects pending turns, and
   * returns the latest `sessionId` plus cumulative `TurnUsage`.
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
          "Codex headless session stopped before turn completed",
          false,
        );
      }
      for (const item of queued) {
        item.reject(
          new CodexHeadlessError(
            "session_stopped",
            "Codex headless session stopped before queued turn started",
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
            new CodexHeadlessError("session_stopped", "Codex headless session has been stopped"),
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

  private executeSingleTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    const argv = buildCodexHeadlessArgv(this.launch.argv, {
      sessionId: this.sessionId,
      prompt,
    });
    const cwd = this.launch.cwd || process.cwd();
    const env = buildProcessEnv(this.launch);

    let proc: SpawnedProcess;
    try {
      proc = this.spawnFn(argv, { cwd, env });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const failure = new CodexHeadlessError("spawn_failed", message, false);
      onEvent?.({ type: "failed", code: failure.code, message: failure.message, retryable: false });
      return Promise.reject(failure);
    }

    try {
      void Promise.resolve(proc.stdin.end()).catch(() => {});
    } catch {
      // Ignore stdin close error.
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
      };
      this.activeTurn = turn;

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
      this.activeProc = handle;

      handle.stdoutDone = this.readStdoutLoop(handle, turn);
      if (proc.stderr) {
        handle.stderrDone = this.readStderrLoop(handle);
      }

      void handle.exitPromise.then((code) => this.handleProcessExit(handle, turn, code));
    });
  }

  private async readStdoutLoop(handle: ProcessHandle, turn: ActiveTurn): Promise<void> {
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
            this.processStdoutLine(turn, rawLine);
          }
        }
      }
      lineBuffer += decoder.decode();
      if (lineBuffer.trim().length > 0) {
        this.processStdoutLine(turn, lineBuffer);
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

  private async handleProcessExit(
    handle: ProcessHandle,
    turn: ActiveTurn,
    exitCode: number,
  ): Promise<void> {
    handle.exited = true;
    handle.exitCode = exitCode;

    await Promise.race([handle.stdoutDone, sleep(EXIT_STDOUT_DRAIN_WAIT_MS)]);
    this.cancelReaders(handle);

    if (this.activeProc === handle) {
      this.activeProc = null;
    }

    if (turn.settled || turn.cancelling || this.stopped) {
      return;
    }

    if (handle.cancelled) {
      this.settleTurnFailure(turn, "cancelled", "Turn cancelled", false);
      return;
    }

    if (exitCode === 0 && turn.accumulatedText.length > 0) {
      this.settleTurnSuccess(turn, {
        ...(turn.sessionId ? { sessionId: turn.sessionId } : {}),
        replyPreview: turn.accumulatedText,
        ...(turn.model ? { model: turn.model } : {}),
        ...(turn.usage ? { usage: turn.usage } : {}),
      });
      return;
    }

    const stderrDetail = handle.stderrTail.trim();
    const message =
      exitCode === 0
        ? "Codex process exited before completing turn"
        : stderrDetail
          ? `Codex process exited with code ${exitCode}: ${stderrDetail}`
          : `Codex process exited with code ${exitCode}`;

    this.settleTurnFailure(turn, "process_exit", message, false);
  }

  private processStdoutLine(turn: ActiveTurn, rawLine: string): void {
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

    const threadId =
      nonEmptyString(parsed.thread_id) ??
      nonEmptyString(parsed.session_id) ??
      nonEmptyString(parsed.sessionId);
    if (threadId) {
      this.sessionId = threadId;
      turn.sessionId = threadId;
    }

    const model = nonEmptyString(parsed.model);
    if (model) {
      this.lastModel = model;
      turn.model = model;
    }

    if (turn.settled || turn.cancelling) {
      return;
    }

    const type = nonEmptyString(parsed.type);
    switch (type) {
      case "thread.started": {
        const sid = threadId ?? this.sessionId ?? "";
        turn.onEvent?.({
          type: "session_init",
          sessionId: sid,
          ...(turn.model ? { model: turn.model } : {}),
        });
        break;
      }

      case "item.completed": {
        this.handleItemCompleted(turn, parsed);
        break;
      }

      case "agent_message": {
        const text =
          (typeof parsed.text === "string" ? parsed.text : undefined) ??
          (typeof parsed.message === "string" ? parsed.message : undefined) ??
          extractTextFromContent(parsed.content);
        if (text && text.length > 0) {
          this.appendAgentText(turn, text);
        }
        break;
      }

      case "turn.completed": {
        const usage = parseCodexUsage(parsed.usage, parsed);
        if (usage) {
          turn.usage = usage;
          this.cumulativeUsage = addTurnUsage(this.cumulativeUsage, usage);
        }
        this.settleTurnSuccess(turn, {
          ...(turn.sessionId ? { sessionId: turn.sessionId } : {}),
          ...(turn.accumulatedText.length > 0 ? { replyPreview: turn.accumulatedText } : {}),
          ...(turn.model ? { model: turn.model } : {}),
          ...(turn.usage ? { usage: turn.usage } : {}),
        });
        break;
      }

      case "turn.failed":
      case "error": {
        const extracted = extractCodexError(parsed);
        this.settleTurnFailure(turn, extracted.code, extracted.message, extracted.retryable);
        break;
      }

      default:
        break;
    }
  }

  private handleItemCompleted(turn: ActiveTurn, obj: Record<string, unknown>): void {
    const item = isRecord(obj.item) ? obj.item : undefined;
    if (!item) {
      return;
    }

    const itemModel = nonEmptyString(item.model);
    if (itemModel) {
      this.lastModel = itemModel;
      turn.model = itemModel;
    }

    const itemType = nonEmptyString(item.type);
    if (itemType === "agent_message" || itemType === "message") {
      if (itemType === "message") {
        const role = nonEmptyString(item.role)?.toLowerCase();
        if (role && role !== "assistant") {
          return;
        }
      }
      const text =
        (typeof item.text === "string" ? item.text : undefined) ??
        (typeof item.message === "string" ? item.message : undefined) ??
        extractTextFromContent(item.content);
      if (text && text.length > 0) {
        this.appendAgentText(turn, text);
      }
      return;
    }

    if (itemType === "command_execution" || itemType === "mcp_tool_call" || itemType === "tool_use") {
      const id = nonEmptyString(item.id);
      const name =
        nonEmptyString(item.tool) ??
        nonEmptyString(item.name) ??
        (itemType === "command_execution" ? "shell" : itemType);
      const input =
        item.arguments ??
        item.input ??
        (typeof item.command === "string" ? { command: item.command } : undefined);
      turn.onEvent?.({
        type: "tool_use",
        ...(id ? { id } : {}),
        name,
        ...(input !== undefined ? { input } : {}),
      });

      const output =
        typeof item.aggregated_output === "string"
          ? item.aggregated_output
          : typeof item.output === "string"
            ? item.output
            : undefined;
      const isError =
        typeof item.exit_code === "number"
          ? item.exit_code !== 0
          : item.status === "failed"
            ? true
            : undefined;
      if (output !== undefined || isError !== undefined) {
        turn.onEvent?.({
          type: "tool_result",
          ...(id ? { id } : {}),
          ...(output !== undefined ? { output } : {}),
          ...(isError !== undefined ? { isError } : {}),
        });
      }
    }
  }

  private appendAgentText(turn: ActiveTurn, text: string): void {
    turn.accumulatedText = turn.accumulatedText ? `${turn.accumulatedText}\n${text}` : text;
    turn.onEvent?.({ type: "text_delta", text });
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
    turn.reject(new CodexHeadlessError(code, message, retryable));
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

import type { TurnUsage } from "@spaces/types";
import type {
  HeadlessSession,
  HeadlessTurnEvent,
  HeadlessTurnResult,
  InteractiveLaunchSpec,
} from "../plugins/types";

const DEFAULT_CANCEL_GRACE_MS = 500;
const STDERR_TAIL_MAX_CHARS = 4096;

/**
 * Minimal child process handle matching `Bun.spawn(..., { stdout: "pipe", stderr: "pipe" })`
 * while remaining straightforward to mock in unit tests.
 */
export interface GeminiSpawnedProcess {
  stdout: ReadableStream<Uint8Array>;
  stderr?: ReadableStream<Uint8Array>;
  exited: Promise<number>;
  kill(signal?: NodeJS.Signals | number): void;
}

/**
 * Process spawner abstraction injected into `GeminiHeadlessExecutor`.
 */
export type GeminiSpawnFn = (
  argv: string[],
  opts: { cwd: string; env: Record<string, string | undefined> },
) => GeminiSpawnedProcess;

export interface GeminiHeadlessExecutorOptions {
  threadId: string;
  launch: InteractiveLaunchSpec;
  sessionId?: string;
  spawn?: GeminiSpawnFn;
  cancelGraceMs?: number;
}

export class GeminiHeadlessError extends Error {
  readonly code: string;
  readonly retryable: boolean;

  constructor(code: string, message: string, retryable = false) {
    super(message);
    this.name = "GeminiHeadlessError";
    this.code = code;
    this.retryable = retryable;
  }
}

export function defaultGeminiSpawn(
  argv: string[],
  opts: { cwd: string; env: Record<string, string | undefined> },
): GeminiSpawnedProcess {
  return Bun.spawn(argv, {
    cwd: opts.cwd,
    env: opts.env,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  });
}

function isRecord(val: unknown): val is Record<string, unknown> {
  return typeof val === "object" && val !== null && !Array.isArray(val);
}

function strField(obj: Record<string, unknown> | undefined, ...keys: string[]): string {
  if (!obj) return "";
  for (const key of keys) {
    const val = obj[key];
    if (typeof val === "string" && val.trim() !== "") {
      return val.trim();
    }
  }
  return "";
}

function finiteNumber(val: unknown): number | undefined {
  if (typeof val === "number" && Number.isFinite(val)) {
    return val;
  }
  if (typeof val === "string" && val.trim() !== "") {
    const parsed = Number(val);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return undefined;
}

function extractText(raw: unknown): string {
  if (typeof raw === "string") {
    return raw;
  }
  if (Array.isArray(raw)) {
    const parts: string[] = [];
    for (const item of raw) {
      if (typeof item === "string") {
        parts.push(item);
      } else if (isRecord(item)) {
        if (typeof item.text === "string") {
          parts.push(item.text);
        } else if (typeof item.content === "string") {
          parts.push(item.content);
        }
      }
    }
    return parts.join("");
  }
  if (isRecord(raw)) {
    if (typeof raw.text === "string") return raw.text;
    if (typeof raw.content === "string") return raw.content;
  }
  return "";
}

function parseGeminiUsage(raw: unknown): TurnUsage | undefined {
  if (!isRecord(raw)) {
    return undefined;
  }

  const inputTokens =
    finiteNumber(raw.input_tokens) ??
    finiteNumber(raw.inputTokens) ??
    finiteNumber(raw.prompt_tokens) ??
    finiteNumber(raw.input) ??
    0;
  const outputTokens =
    finiteNumber(raw.output_tokens) ??
    finiteNumber(raw.outputTokens) ??
    finiteNumber(raw.completion_tokens) ??
    finiteNumber(raw.candidates_tokens) ??
    0;
  const reasoningTokens =
    finiteNumber(raw.reasoning_tokens) ??
    finiteNumber(raw.reasoningTokens) ??
    finiteNumber(raw.thoughts_tokens) ??
    0;
  const cacheReadTokens =
    finiteNumber(raw.cache_read_tokens) ??
    finiteNumber(raw.cacheReadTokens) ??
    finiteNumber(raw.cached_tokens) ??
    finiteNumber(raw.cached) ??
    0;
  const cacheWriteTokens =
    finiteNumber(raw.cache_write_tokens) ??
    finiteNumber(raw.cacheWriteTokens) ??
    0;
  const costUsd =
    finiteNumber(raw.cost_usd) ??
    finiteNumber(raw.costUsd) ??
    finiteNumber(raw.total_cost_usd) ??
    0;
  const costPriced =
    typeof raw.cost_priced === "boolean"
      ? raw.cost_priced
      : costUsd > 0;

  const hasAny =
    inputTokens > 0 ||
    outputTokens > 0 ||
    reasoningTokens > 0 ||
    cacheReadTokens > 0 ||
    cacheWriteTokens > 0 ||
    costUsd > 0;

  if (!hasAny) {
    return undefined;
  }

  return {
    input_tokens: Math.trunc(inputTokens),
    output_tokens: Math.trunc(outputTokens),
    reasoning_tokens: Math.trunc(reasoningTokens),
    cache_read_tokens: Math.trunc(cacheReadTokens),
    cache_write_tokens: Math.trunc(cacheWriteTokens),
    cost_usd: costUsd,
    cost_priced: costPriced,
  };
}

function mergeLaunchEnv(
  launch: InteractiveLaunchSpec,
): Record<string, string | undefined> {
  const env: Record<string, string | undefined> = { ...process.env };
  for (const key of launch.unsetEnv) {
    delete env[key];
  }
  for (const [key, val] of Object.entries(launch.env)) {
    env[key] = val;
  }
  return env;
}

const VALUE_FLAGS_TO_STRIP = new Set([
  "-p",
  "--prompt",
  "--output-format",
  "-r",
  "--resume",
]);

function matchesValueFlag(tok: string): string | undefined {
  for (const flag of VALUE_FLAGS_TO_STRIP) {
    if (tok === flag || tok.startsWith(`${flag}=`)) {
      return flag;
    }
  }
  return undefined;
}

/**
 * Builds the headless CLI argv for `gemini -p <prompt> --output-format stream-json`,
 * preserving base binary / node script and non-conflicting launch flags.
 */
export function buildGeminiHeadlessArgv(
  launchArgv: readonly string[],
  prompt: string,
  sessionId?: string,
): string[] {
  const base = launchArgv.length > 0 ? [...launchArgv] : ["gemini"];
  const prefixCount =
    base.length >= 2 &&
    (base[0] === "node" || base[0]?.endsWith("/node")) &&
    !base[1]!.startsWith("-")
      ? 2
      : 1;

  const head = base.slice(0, prefixCount);
  const rest = base.slice(prefixCount);
  const forwarded: string[] = [];

  for (let i = 0; i < rest.length; i += 1) {
    const tok = rest[i]!;
    const flag = matchesValueFlag(tok);
    if (flag) {
      if (tok === flag && i + 1 < rest.length) {
        i += 1;
      }
      continue;
    }
    forwarded.push(tok);
  }

  if (sessionId?.trim()) {
    forwarded.push("--resume", sessionId.trim());
  }

  forwarded.push("-p", prompt, "--output-format", "stream-json");
  return [...head, ...forwarded];
}

async function readStreamTextTail(
  stream: ReadableStream<Uint8Array> | undefined,
  maxChars: number,
): Promise<string> {
  if (!stream) return "";
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (value) {
        buf += decoder.decode(value, { stream: true });
        if (buf.length > maxChars * 2) {
          buf = buf.slice(-maxChars);
        }
      }
    }
    buf += decoder.decode();
  } catch {
    // Ignore stderr read errors.
  } finally {
    reader.releaseLock();
  }
  return buf.length > maxChars ? buf.slice(-maxChars) : buf;
}

/**
 * Headless executor for Gemini CLI over `gemini -p <prompt> --output-format stream-json`.
 */
export class GeminiHeadlessExecutor implements HeadlessSession {
  readonly agent = "gemini";
  readonly threadId: string;

  private readonly launch: InteractiveLaunchSpec;
  private readonly spawnFn: GeminiSpawnFn;
  private readonly cancelGraceMs: number;

  private sessionId?: string;
  private model?: string;
  private lastUsage?: TurnUsage;

  private activeProc: GeminiSpawnedProcess | null = null;
  private activeTurnPromise: Promise<HeadlessTurnResult> | null = null;
  private cancelReason: string | null = null;

  constructor(options: GeminiHeadlessExecutorOptions) {
    this.threadId = options.threadId;
    this.launch = options.launch;
    this.sessionId = options.sessionId?.trim() || undefined;
    this.spawnFn = options.spawn ?? defaultGeminiSpawn;
    this.cancelGraceMs = options.cancelGraceMs ?? DEFAULT_CANCEL_GRACE_MS;
  }

  getSessionId(): string | undefined {
    return this.sessionId;
  }

  isRunning(): boolean {
    return this.activeProc !== null;
  }

  async sendTurn(
    prompt: string,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    if (this.activeProc !== null) {
      throw new GeminiHeadlessError(
        "busy",
        "Gemini headless turn is already running",
        false,
      );
    }

    this.cancelReason = null;
    const argv = buildGeminiHeadlessArgv(
      this.launch.argv,
      prompt,
      this.sessionId,
    );
    const env = mergeLaunchEnv(this.launch);
    const proc = this.spawnFn(argv, { cwd: this.launch.cwd, env });
    this.activeProc = proc;

    const turnPromise = this.runTurn(proc, onEvent);
    this.activeTurnPromise = turnPromise;

    try {
      return await turnPromise;
    } finally {
      if (this.activeProc === proc) {
        this.activeProc = null;
      }
      if (this.activeTurnPromise === turnPromise) {
        this.activeTurnPromise = null;
      }
    }
  }

  async cancel(reason = "cancelled"): Promise<void> {
    this.cancelReason = reason;
    const proc = this.activeProc;
    if (!proc) {
      return;
    }

    try {
      proc.kill("SIGTERM");
    } catch {
      // Ignore already-exited process.
    }

    let timer: ReturnType<typeof setTimeout> | undefined;
    const exited = await Promise.race([
      proc.exited.then(() => true).catch(() => true),
      new Promise<boolean>((resolve) => {
        timer = setTimeout(() => resolve(false), this.cancelGraceMs);
      }),
    ]);
    if (timer !== undefined) {
      clearTimeout(timer);
    }

    if (!exited) {
      try {
        proc.kill("SIGKILL");
      } catch {
        // Ignore already-exited process.
      }
      await proc.exited.catch(() => {});
    }
  }

  async stopAndFlush(): Promise<{ sessionId?: string; usage?: TurnUsage }> {
    if (this.activeProc) {
      await this.cancel("stopped");
    }
    if (this.activeTurnPromise) {
      await this.activeTurnPromise.catch(() => {});
    }
    return {
      ...(this.sessionId ? { sessionId: this.sessionId } : {}),
      ...(this.lastUsage ? { usage: this.lastUsage } : {}),
    };
  }

  private async runTurn(
    proc: GeminiSpawnedProcess,
    onEvent?: (event: HeadlessTurnEvent) => void,
  ): Promise<HeadlessTurnResult> {
    const stderrPromise = readStreamTextTail(proc.stderr, STDERR_TAIL_MAX_CHARS);

    let replyText = "";
    let turnUsage: TurnUsage | undefined;
    let turnModel = this.model;
    let failedError: GeminiHeadlessError | null = null;

    const handleLine = (rawLine: string): void => {
      const trimmed = rawLine.trim();
      if (!trimmed) return;

      let parsed: unknown;
      try {
        parsed = JSON.parse(trimmed);
      } catch {
        return;
      }
      if (!isRecord(parsed)) return;

      const typ = strField(parsed, "type").toLowerCase();

      if (typ === "init") {
        const sid = strField(parsed, "session_id", "sessionId");
        const model = strField(parsed, "model");
        if (sid) this.sessionId = sid;
        if (model) {
          this.model = model;
          turnModel = model;
        }
        if (sid) {
          onEvent?.({
            type: "session_init",
            sessionId: sid,
            ...(model ? { model } : {}),
          });
        }
        return;
      }

      if (typ === "message") {
        const role = strField(parsed, "role").toLowerCase();
        if (
          role === "assistant" ||
          role === "model" ||
          (role === "" && parsed.delta === true)
        ) {
          const text = extractText(parsed.content ?? parsed.text);
          if (text) {
            replyText += text;
            onEvent?.({ type: "text_delta", text });
          }
        }
        return;
      }

      if (typ === "tool_use") {
        const name = strField(parsed, "tool_name", "name") || "tool";
        const id = strField(parsed, "tool_id", "id") || undefined;
        onEvent?.({
          type: "tool_use",
          ...(id ? { id } : {}),
          name,
          input: parsed.parameters ?? parsed.input,
        });
        return;
      }

      if (typ === "tool_result") {
        const id = strField(parsed, "tool_id", "id") || undefined;
        const output =
          typeof parsed.output === "string"
            ? parsed.output
            : typeof parsed.content === "string"
              ? parsed.content
              : undefined;
        const isError =
          parsed.status === "error" || parsed.is_error === true;
        onEvent?.({
          type: "tool_result",
          ...(id ? { id } : {}),
          ...(output !== undefined ? { output } : {}),
          ...(isError ? { isError: true } : {}),
        });
        return;
      }

      if (typ === "result") {
        const sid = strField(parsed, "session_id", "sessionId");
        if (sid) this.sessionId = sid;

        const model = strField(parsed, "model");
        if (model) {
          this.model = model;
          turnModel = model;
        }

        const usage = parseGeminiUsage(parsed.stats ?? parsed.usage ?? parsed);
        if (usage) {
          turnUsage = usage;
          this.lastUsage = usage;
        }

        const status = strField(parsed, "status").toLowerCase();
        if (status === "error" || parsed.is_error === true) {
          const errObj = isRecord(parsed.error) ? parsed.error : undefined;
          const code = strField(errObj, "code", "type") || "result_error";
          const message =
            strField(errObj, "message") ||
            strField(parsed, "error", "message") ||
            "Gemini turn failed";
          failedError = new GeminiHeadlessError(code, message, false);
          onEvent?.({
            type: "failed",
            code: failedError.code,
            message: failedError.message,
            retryable: failedError.retryable,
          });
          return;
        }

        if (!replyText) {
          const finalText = extractText(parsed.response ?? parsed.result);
          if (finalText) {
            replyText = finalText;
          }
        }
        return;
      }

      if (typ === "error") {
        const errObj = isRecord(parsed.error) ? parsed.error : parsed;
        const code = strField(errObj, "code", "type") || "stream_error";
        const message =
          strField(errObj, "message") || "Gemini stream error";
        const retryable = errObj.retryable === true;
        failedError = new GeminiHeadlessError(code, message, retryable);
        onEvent?.({
          type: "failed",
          code: failedError.code,
          message: failedError.message,
          retryable: failedError.retryable,
        });
      }
    };

    const reader = proc.stdout.getReader();
    const decoder = new TextDecoder();
    let partial = "";
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        if (!value) continue;
        partial += decoder.decode(value, { stream: true });
        let nl = partial.indexOf("\n");
        while (nl >= 0) {
          const line = partial.slice(0, nl);
          partial = partial.slice(nl + 1);
          handleLine(line);
          nl = partial.indexOf("\n");
        }
      }
      partial += decoder.decode();
      if (partial.trim()) {
        handleLine(partial);
      }
    } finally {
      reader.releaseLock();
    }

    const exitCode = await proc.exited;
    const stderrTail = await stderrPromise;

    if (this.cancelReason !== null) {
      const err = new GeminiHeadlessError(
        "cancelled",
        `Gemini turn cancelled: ${this.cancelReason}`,
        false,
      );
      onEvent?.({
        type: "failed",
        code: err.code,
        message: err.message,
        retryable: false,
      });
      throw err;
    }

    if (failedError) {
      throw failedError;
    }

    if (exitCode !== 0) {
      const detail = stderrTail.trim();
      const message = detail
        ? `gemini exited with code ${exitCode}: ${detail}`
        : `gemini exited with code ${exitCode}`;
      const err = new GeminiHeadlessError("process_exit", message, false);
      onEvent?.({
        type: "failed",
        code: err.code,
        message: err.message,
        retryable: false,
      });
      throw err;
    }

    const replyPreview = replyText.trim() || undefined;
    const result: HeadlessTurnResult = {
      ...(this.sessionId ? { sessionId: this.sessionId } : {}),
      ...(replyPreview ? { replyPreview } : {}),
      ...(turnModel ? { model: turnModel } : {}),
      ...(turnUsage ? { usage: turnUsage } : {}),
    };

    onEvent?.({
      type: "completed",
      ...(result.sessionId ? { sessionId: result.sessionId } : {}),
      ...(result.replyPreview ? { replyPreview: result.replyPreview } : {}),
      ...(result.model ? { model: result.model } : {}),
      ...(result.usage ? { usage: result.usage } : {}),
    });

    return result;
  }
}

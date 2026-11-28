import {
  closeSync,
  existsSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  statSync,
} from "node:fs";
import * as path from "node:path";
import { SPACES_CONFIG_DIR_NAME, type TurnUsage } from "@spaces/types";
import { ClaudeHeadlessExecutor } from "../executors/claude";
import type { VendorSessionLookup } from "../resurrect/capture";
import {
  defaultSpacesDir,
  loadProfileConfig,
  loadRouterConfig,
  realHomeDir,
} from "./profiles";
import type {
  AccessLevel,
  DeltaCursor,
  DeltaReadResult,
  HarnessPlugin,
  HarnessPluginSpec,
  HarnessProfileConfig,
  HeadlessSession,
  HeadlessStartRequest,
  InteractiveLaunchRequest,
  InteractiveLaunchSpec,
  ParsedDiskTurn,
  StopAndFlushOptions,
} from "./types";

const MAX_DERIVED_TITLE_LEN = 120;

const BOILERPLATE_USER_PREFIXES = [
  "<local-command-caveat>",
  "<command-name>",
  "<command-message>",
  "<system-reminder>",
  "<ide_opened_file>",
  "<ide_selection>",
  "[request interrupted",
] as const;

const CLAUDE_ACCESS_ARGS: Record<AccessLevel, readonly string[]> = {
  "read-only": ["--permission-mode", "plan"],
  default: [],
  auto: ["--permission-mode", "acceptEdits"],
  full: ["--dangerously-skip-permissions"],
};

export interface ClaudePluginOptions {
  homeDir?: string;
  spacesDir?: string;
  headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;
}

/**
 * Encodes a working directory into Claude Code's project directory slug under
 * `~/.claude/projects/<encoded-cwd>/` by replacing path separators and any
 * non-alphanumeric characters with `-`.
 */
export function encodeClaudeProjectSlug(cwd: string): string {
  const trimmed = cwd.trim();
  if (!trimmed) {
    return "";
  }
  const normalized = trimmed.length > 1 ? trimmed.replace(/[\\/]+$/, "") : trimmed;
  return normalized.replace(/[^a-zA-Z0-9]/g, "-");
}

function isRecord(val: unknown): val is Record<string, unknown> {
  return typeof val === "object" && val !== null && !Array.isArray(val);
}

function strField(obj: Record<string, unknown> | undefined, key: string): string {
  if (!obj) {
    return "";
  }
  const val = obj[key];
  return typeof val === "string" ? val.trim() : "";
}

function firstNonEmpty(...vals: Array<string | undefined>): string {
  for (const v of vals) {
    if (v && v.trim() !== "") {
      return v.trim();
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

function normalizeNumericTimestamp(n: number): number {
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  if (n >= 1e17) return Math.floor(n / 1e6);
  if (n >= 1e14) return Math.floor(n / 1e3);
  if (n >= 1e11) return Math.floor(n);
  if (n >= 1e9) return Math.floor(n * 1000);
  return Math.floor(n);
}

function parseTimestampMs(val: unknown): number | undefined {
  if (typeof val === "number") {
    const ms = normalizeNumericTimestamp(val);
    return ms > 0 ? ms : undefined;
  }
  if (typeof val === "string") {
    const s = val.trim();
    if (!s) {
      return undefined;
    }
    const num = Number(s);
    if (Number.isFinite(num) && num > 0) {
      const ms = normalizeNumericTimestamp(num);
      return ms > 0 ? ms : undefined;
    }
    const parsed = Date.parse(s);
    if (Number.isFinite(parsed) && parsed > 0) {
      return parsed;
    }
  }
  return undefined;
}

function firstTimestampMs(obj: Record<string, unknown>, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const ms = parseTimestampMs(obj[key]);
    if (ms !== undefined) {
      return ms;
    }
  }
  return undefined;
}

function extractTextFromContent(content: unknown): string {
  if (typeof content === "string") {
    return content.trim();
  }
  if (Array.isArray(content)) {
    const parts: string[] = [];
    for (const item of content) {
      if (typeof item === "string") {
        const s = item.trim();
        if (s) parts.push(s);
        continue;
      }
      if (!isRecord(item)) {
        continue;
      }
      const blockType = strField(item, "type").toLowerCase();
      if (
        blockType === "tool_use" ||
        blockType === "tool_result" ||
        blockType === "tool_call" ||
        blockType === "function_call" ||
        blockType === "function_call_output" ||
        blockType === "thinking" ||
        blockType === "reasoning"
      ) {
        continue;
      }
      if (
        blockType === "" ||
        blockType === "text" ||
        blockType === "output_text" ||
        blockType === "input_text"
      ) {
        const txt = firstNonEmpty(
          typeof item.text === "string" ? item.text : undefined,
          typeof item.content === "string" ? item.content : undefined,
          typeof item.value === "string" ? item.value : undefined,
        );
        if (txt) {
          parts.push(txt);
        }
      }
    }
    return parts.join("\n").trim();
  }
  if (isRecord(content)) {
    return firstNonEmpty(
      typeof content.text === "string" ? content.text : undefined,
      typeof content.content === "string" ? content.content : undefined,
      typeof content.message === "string" ? content.message : undefined,
    );
  }
  return "";
}

function cleanSingleLine(s: string, maxLen = MAX_DERIVED_TITLE_LEN): string {
  const collapsed = s.trim().replace(/\s+/g, " ");
  if (maxLen <= 0 || collapsed.length <= maxLen) {
    return collapsed;
  }
  return collapsed.slice(0, maxLen);
}

function isBoilerplateUserMessage(text: string): boolean {
  const lower = text.trim().toLowerCase();
  if (!lower) {
    return true;
  }
  return BOILERPLATE_USER_PREFIXES.some((prefix) => lower.startsWith(prefix));
}

function parseUsageObject(
  raw: unknown,
  fallbackObj?: Record<string, unknown>,
): TurnUsage | undefined {
  const u = isRecord(raw) ? raw : undefined;
  const fb = fallbackObj;
  if (!u && !fb) {
    return undefined;
  }

  const inputTokens =
    finiteNumber(u?.input_tokens) ??
    finiteNumber(u?.inputTokens) ??
    finiteNumber(fb?.input_tokens) ??
    finiteNumber(fb?.inputTokens) ??
    0;
  const outputTokens =
    finiteNumber(u?.output_tokens) ??
    finiteNumber(u?.outputTokens) ??
    finiteNumber(fb?.output_tokens) ??
    finiteNumber(fb?.outputTokens) ??
    0;
  const reasoningTokens =
    finiteNumber(u?.reasoning_tokens) ??
    finiteNumber(u?.reasoningTokens) ??
    finiteNumber(fb?.reasoning_tokens) ??
    finiteNumber(fb?.reasoningTokens) ??
    0;
  const cacheReadTokens =
    finiteNumber(u?.cache_read_tokens) ??
    finiteNumber(u?.cache_read_input_tokens) ??
    finiteNumber(u?.cacheReadTokens) ??
    finiteNumber(u?.cacheReadInputTokens) ??
    finiteNumber(fb?.cache_read_tokens) ??
    finiteNumber(fb?.cache_read_input_tokens) ??
    finiteNumber(fb?.cacheReadTokens) ??
    0;
  const cacheWriteTokens =
    finiteNumber(u?.cache_write_tokens) ??
    finiteNumber(u?.cache_creation_input_tokens) ??
    finiteNumber(u?.cacheWriteTokens) ??
    finiteNumber(u?.cacheCreationInputTokens) ??
    finiteNumber(fb?.cache_write_tokens) ??
    finiteNumber(fb?.cache_creation_input_tokens) ??
    finiteNumber(fb?.cacheWriteTokens) ??
    0;
  const costUsd =
    finiteNumber(u?.cost_usd) ??
    finiteNumber(u?.costUSD) ??
    finiteNumber(u?.costUsd) ??
    finiteNumber(u?.total_cost_usd) ??
    finiteNumber(u?.totalCostUSD) ??
    finiteNumber(fb?.cost_usd) ??
    finiteNumber(fb?.costUSD) ??
    finiteNumber(fb?.costUsd) ??
    finiteNumber(fb?.total_cost_usd) ??
    finiteNumber(fb?.totalCostUSD) ??
    0;
  const explicitPriced =
    typeof u?.cost_priced === "boolean"
      ? u.cost_priced
      : typeof fb?.cost_priced === "boolean"
        ? fb.cost_priced
        : undefined;
  const costPriced = explicitPriced ?? costUsd > 0;

  if (
    !u &&
    inputTokens === 0 &&
    outputTokens === 0 &&
    reasoningTokens === 0 &&
    cacheReadTokens === 0 &&
    cacheWriteTokens === 0 &&
    costUsd === 0 &&
    !costPriced
  ) {
    return undefined;
  }

  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    reasoning_tokens: reasoningTokens,
    cache_read_tokens: cacheReadTokens,
    cache_write_tokens: cacheWriteTokens,
    cost_usd: costUsd,
    cost_priced: costPriced,
  };
}

function emptyUsage(): TurnUsage {
  return {
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
    cost_priced: false,
  };
}

function addUsage(acc: TurnUsage, next: TurnUsage): TurnUsage {
  const costUsd = acc.cost_usd + next.cost_usd;
  return {
    input_tokens: acc.input_tokens + next.input_tokens,
    output_tokens: acc.output_tokens + next.output_tokens,
    reasoning_tokens: acc.reasoning_tokens + next.reasoning_tokens,
    cache_read_tokens: acc.cache_read_tokens + next.cache_read_tokens,
    cache_write_tokens: acc.cache_write_tokens + next.cache_write_tokens,
    cost_usd: costUsd,
    cost_priced: acc.cost_priced || next.cost_priced || costUsd > 0,
  };
}

function mergeTurnAndCostStateUsage(
  turnTotals: TurnUsage | undefined,
  costState: TurnUsage | undefined,
): TurnUsage | undefined {
  if (!turnTotals && !costState) {
    return undefined;
  }
  if (!turnTotals) {
    return costState;
  }
  if (!costState) {
    return turnTotals;
  }
  const costUsd = Math.max(turnTotals.cost_usd, costState.cost_usd);
  return {
    input_tokens: Math.max(turnTotals.input_tokens, costState.input_tokens),
    output_tokens: Math.max(turnTotals.output_tokens, costState.output_tokens),
    reasoning_tokens: Math.max(turnTotals.reasoning_tokens, costState.reasoning_tokens),
    cache_read_tokens: Math.max(turnTotals.cache_read_tokens, costState.cache_read_tokens),
    cache_write_tokens: Math.max(turnTotals.cache_write_tokens, costState.cache_write_tokens),
    cost_usd: costUsd,
    cost_priced: turnTotals.cost_priced || costState.cost_priced || costUsd > 0,
  };
}

function normalizeCwd(cwd: string): string {
  const trimmed = cwd.trim();
  if (!trimmed) {
    return "";
  }
  return path.resolve(trimmed);
}

function dedupeStrings(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const val of values) {
    if (!val || seen.has(val)) {
      continue;
    }
    seen.add(val);
    out.push(val);
  }
  return out;
}

function isRegularFile(targetPath: string): boolean {
  try {
    return statSync(targetPath).isFile();
  } catch {
    return false;
  }
}

function isDirectory(targetPath: string): boolean {
  try {
    return statSync(targetPath).isDirectory();
  } catch {
    return false;
  }
}

function readBytesSlice(filePath: string, offset: number, length: number): Buffer {
  const buf = Buffer.allocUnsafe(length);
  const fd = openSync(filePath, "r");
  try {
    let totalRead = 0;
    while (totalRead < length) {
      const n = readSync(fd, buf, totalRead, length - totalRead, offset + totalRead);
      if (n <= 0) {
        break;
      }
      totalRead += n;
    }
    return totalRead === length ? buf : buf.subarray(0, totalRead);
  } finally {
    closeSync(fd);
  }
}

interface SessionFileMetadata {
  sessionId: string;
  cwd: string;
  title: string;
  archived: boolean;
  latestTimestampMs: number;
}

function inspectClaudeSessionFile(filePath: string): SessionFileMetadata {
  const fallbackId = path.basename(filePath, ".jsonl");
  let sessionId = "";
  let cwd = "";
  let customTitle = "";
  let summaryTitle = "";
  let firstUserTitle = "";
  let archived = false;
  let latestTimestampMs = 0;

  let raw = "";
  try {
    raw = readFileSync(filePath, "utf8");
  } catch {
    return {
      sessionId: fallbackId,
      cwd: "",
      title: "",
      archived: false,
      latestTimestampMs: 0,
    };
  }

  for (const rawLine of raw.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      continue;
    }
    if (!isRecord(parsed)) {
      continue;
    }
    if (parsed.isSidechain === true) {
      continue;
    }

    if (!sessionId) {
      sessionId = firstNonEmpty(strField(parsed, "sessionId"), strField(parsed, "session_id"));
    }
    if (!cwd) {
      cwd = firstNonEmpty(strField(parsed, "cwd"), strField(parsed, "project_dir"));
    }
    if (parsed.archived === true || parsed.isArchived === true || parsed.is_archived === true) {
      archived = true;
    }
    const ts = firstTimestampMs(parsed, "timestamp", "ts", "created_at", "createdAt");
    if (ts !== undefined && ts > latestTimestampMs) {
      latestTimestampMs = ts;
    }

    const typ = strField(parsed, "type").toLowerCase();
    if (typ === "custom-title") {
      const ct = firstNonEmpty(
        strField(parsed, "customTitle"),
        strField(parsed, "custom_title"),
        strField(parsed, "title"),
        strField(parsed, "text"),
      );
      if (ct) {
        customTitle = cleanSingleLine(ct);
      }
      continue;
    }
    if (typ === "summary") {
      const sm = firstNonEmpty(strField(parsed, "summary"), strField(parsed, "text"));
      if (sm) {
        summaryTitle = cleanSingleLine(sm);
      }
      continue;
    }

    const inlineCustom = firstNonEmpty(
      strField(parsed, "customTitle"),
      strField(parsed, "custom_title"),
    );
    if (inlineCustom) {
      customTitle = cleanSingleLine(inlineCustom);
    }

    const msg = isRecord(parsed.message) ? parsed.message : undefined;
    const role = firstNonEmpty(
      strField(msg, "role"),
      strField(parsed, "role"),
      typ === "user" || typ === "assistant" ? typ : "",
    ).toLowerCase();
    if (role === "user" && !firstUserTitle) {
      const text = extractTextFromContent(msg?.content ?? parsed.content) || strField(parsed, "text");
      if (text && !isBoilerplateUserMessage(text)) {
        firstUserTitle = cleanSingleLine(text);
      }
    }
  }

  return {
    sessionId: sessionId || fallbackId,
    cwd,
    title: customTitle || summaryTitle || firstUserTitle,
    archived,
    latestTimestampMs,
  };
}

/**
 * Unified `HarnessPlugin` implementation for Claude Code (`claude`).
 */
export class ClaudePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = {
    id: "claude",
    aliases: ["claude-code", "claude_code", "claudecode"],
    displayApp: "claude",
    cliDisplayApp: "claude-code",
    binary: "claude",
    screenManifestId: "claude",
    oscAuthority: true,
    resumeFlag: "--resume",
    positionalResume: false,
    uniqueExplicitSessions: false,
    titlePrefixes: ["Claude Code - ", "Claude Code: ", "Claude - ", "Claude: "],
    mcpTargets: [".claude.json", ".claude/mcp.json", ".claude/settings.json"],
  };

  private readonly homeDir?: string;
  private readonly spacesDir?: string;
  private readonly headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;

  constructor(options: ClaudePluginOptions = {}) {
    this.homeDir = options.homeDir;
    this.spacesDir = options.spacesDir;
    this.headlessFactory = options.headlessFactory;
  }

  resumeArgs(sessionId: string): string[] {
    const sid = sessionId.trim();
    return sid ? ["--resume", sid] : [];
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    const baseEnv = req.env ?? process.env;
    const explicitHome = req.homeDir ?? this.homeDir;
    const { realHome, spacesDir } = this.resolvePaths(
      req.homeDir,
      req.spacesDir,
      baseEnv,
    );

    const profileName = (req.profile ?? "").trim();
    let profile: HarnessProfileConfig | null = null;
    if (profileName) {
      profile = loadProfileConfig("claude", profileName, {
        homeDir: realHome,
        spacesDir,
      });
      if (profile && !profile.enabled) {
        throw new Error(`profile disabled: claude/${profile.name}`);
      }
    }

    const shadowHome = profile?.shadowHomePath?.trim() || undefined;
    const routerEnabled = !req.noRouter && (profile ? profile.router : true);

    const env: Record<string, string> = {};
    if (routerEnabled) {
      const routerEnvInput =
        explicitHome && !req.env?.HOME ? { ...baseEnv, HOME: realHome } : baseEnv;
      const xdgConfigHome =
        explicitHome && !req.env?.XDG_CONFIG_HOME
          ? path.join(realHome, ".config")
          : undefined;
      const router = loadRouterConfig({
        env: routerEnvInput,
        xdgConfigHome,
      });
      env.ANTHROPIC_BASE_URL = router.url;
      env.ANTHROPIC_AUTH_TOKEN = router.apiKey;
    }

    if (shadowHome) {
      env.CLAUDE_CONFIG_DIR = shadowHome;
    } else if (profile?.homePath?.trim()) {
      env.CLAUDE_CONFIG_DIR = profile.homePath.trim();
    }

    if (profile) {
      env.HARNESS_PROFILE = profile.name;
    }

    if (profile?.env) {
      for (const [k, v] of Object.entries(profile.env)) {
        if (k === "HOME") {
          continue;
        }
        env[k] = v;
      }
    }

    const unsetEnvList: string[] = ["HARNESS_AGENT"];
    if (!profile) {
      unsetEnvList.push("HARNESS_PROFILE");
    }
    if (!routerEnabled) {
      unsetEnvList.push(
        "ANTHROPIC_BASE_URL",
        "ANTHROPIC_AUTH_TOKEN",
        "ANTHROPIC_API_KEY",
      );
    }
    const unsetEnv = dedupeStrings(unsetEnvList).filter((key) => !Object.hasOwn(env, key));

    const binary = profile?.binaryPath?.trim() || this.spec.binary;
    const accessArgs = req.accessLevel ? CLAUDE_ACCESS_ARGS[req.accessLevel] ?? [] : [];
    const modelArgs = req.model?.trim() ? ["--model", req.model.trim()] : [];
    const effortArgs = req.effort?.trim() ? ["--effort", req.effort.trim()] : [];
    const resumeArgs = req.sessionId?.trim() ? this.resumeArgs(req.sessionId) : [];

    const argv = [
      binary,
      ...(profile?.prependArgs ?? []),
      ...accessArgs,
      ...modelArgs,
      ...effortArgs,
      ...resumeArgs,
      ...(req.extraArgs ?? []),
    ];

    const configDir =
      env.CLAUDE_CONFIG_DIR ||
      (explicitHome ? req.env?.CLAUDE_CONFIG_DIR : baseEnv.CLAUDE_CONFIG_DIR)?.trim() ||
      path.join(realHome, ".claude");

    return {
      argv,
      env,
      unsetEnv,
      cwd: req.cwd,
      ...(shadowHome ? { shadowHome } : {}),
      configDir,
    };
  }

  startHeadless(req: HeadlessStartRequest): HeadlessSession {
    const launch = this.buildInteractiveLaunch(req);
    if (this.headlessFactory) {
      return this.headlessFactory(req, launch);
    }
    return new ClaudeHeadlessExecutor({
      threadId: req.threadId,
      launch,
      sessionId: req.sessionId,
    });
  }

  readDelta(
    sessionId: string,
    cwd: string,
    cursor?: DeltaCursor,
    configDir?: string,
  ): DeltaReadResult {
    const effectiveConfigDir = this.resolveConfigDir(configDir);
    const resolvedPath = this.locateSessionFile(
      sessionId,
      cwd,
      effectiveConfigDir,
      cursor?.sessionPath,
    );

    if (!resolvedPath || !isRegularFile(resolvedPath)) {
      return {
        nextCursor: {
          sessionPath: resolvedPath,
          byteOffset: 0,
        },
        turns: [],
      };
    }

    const fileSize = statSync(resolvedPath).size;
    let offset = cursor?.byteOffset ?? 0;
    let partial = cursor?.partialLine ?? "";

    if (offset < 0 || offset > fileSize) {
      offset = 0;
      partial = "";
    }

    if (offset === fileSize) {
      return {
        nextCursor: {
          sessionPath: resolvedPath,
          byteOffset: offset,
          ...(partial ? { partialLine: partial } : {}),
        },
        turns: [],
      };
    }

    const chunk = readBytesSlice(resolvedPath, offset, fileSize - offset);
    const nextOffset = offset + chunk.length;
    const combined = partial + chunk.toString("utf8");
    const endsWithNewline = combined.endsWith("\n");
    const rawLines = combined.split("\n");

    let nextPartial = "";
    if (!endsWithNewline) {
      nextPartial = rawLines.pop() ?? "";
    } else if (rawLines.length > 0 && rawLines[rawLines.length - 1] === "") {
      rawLines.pop();
    }

    const turns: ParsedDiskTurn[] = [];
    let customTitle = "";
    let summaryTitle = "";
    let lastModel = "";
    let turnUsageSum: TurnUsage | undefined;
    let costStateUsage: TurnUsage | undefined;

    for (const rawLine of rawLines) {
      const line = rawLine.replace(/\r$/, "").trim();
      if (!line) {
        continue;
      }

      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch {
        continue;
      }
      if (!isRecord(parsed) || parsed.isSidechain === true) {
        continue;
      }

      const typ = strField(parsed, "type").toLowerCase();
      const tsMs = firstTimestampMs(parsed, "timestamp", "ts", "created_at", "createdAt");

      if (typ === "custom-title") {
        const titleVal = firstNonEmpty(
          strField(parsed, "customTitle"),
          strField(parsed, "custom_title"),
          strField(parsed, "title"),
          strField(parsed, "text"),
        );
        if (titleVal) {
          customTitle = cleanSingleLine(titleVal);
        }
        continue;
      }

      if (typ === "summary") {
        const sumVal = firstNonEmpty(strField(parsed, "summary"), strField(parsed, "text"));
        if (sumVal) {
          summaryTitle = cleanSingleLine(sumVal);
        }
        continue;
      }

      if (typ === "cost-state" || typ === "cost_state") {
        const costContainer =
          (isRecord(parsed.usage) ? parsed.usage : undefined) ??
          (isRecord(parsed.state) ? parsed.state : undefined) ??
          (isRecord(parsed.cost) ? parsed.cost : undefined) ??
          parsed;
        const parsedCost = parseUsageObject(costContainer, parsed);
        if (parsedCost) {
          costStateUsage = parsedCost;
        }
        continue;
      }

      const inlineCustom = firstNonEmpty(
        strField(parsed, "customTitle"),
        strField(parsed, "custom_title"),
      );
      if (inlineCustom) {
        customTitle = cleanSingleLine(inlineCustom);
      }

      const msg = isRecord(parsed.message) ? parsed.message : undefined;
      const role = firstNonEmpty(
        strField(msg, "role"),
        strField(parsed, "role"),
        typ === "user" || typ === "assistant" ? typ : "",
      ).toLowerCase();

      if (role !== "user" && role !== "assistant") {
        continue;
      }

      const model = firstNonEmpty(strField(msg, "model"), strField(parsed, "model"));
      if (model) {
        lastModel = model;
      }

      const stopReason = firstNonEmpty(
        strField(msg, "stop_reason"),
        strField(msg, "stopReason"),
        strField(parsed, "stop_reason"),
        strField(parsed, "stopReason"),
      );

      const lineUsage = parseUsageObject(msg?.usage ?? parsed.usage, parsed);
      if (lineUsage) {
        turnUsageSum = addUsage(turnUsageSum ?? emptyUsage(), lineUsage);
      }

      const text =
        extractTextFromContent(msg?.content ?? parsed.content) || strField(parsed, "text");
      if (!text) {
        continue;
      }

      const turn: ParsedDiskTurn = {
        role,
        text,
      };
      if (tsMs !== undefined) turn.timestampMs = tsMs;
      if (model) turn.model = model;
      if (stopReason) turn.stopReason = stopReason;
      if (lineUsage) turn.usage = lineUsage;
      turns.push(turn);
    }

    const finalTitle = customTitle || summaryTitle || undefined;
    const finalUsage = mergeTurnAndCostStateUsage(turnUsageSum, costStateUsage);

    const result: DeltaReadResult = {
      nextCursor: {
        sessionPath: resolvedPath,
        byteOffset: nextOffset,
        ...(nextPartial ? { partialLine: nextPartial } : {}),
      },
      turns,
    };
    if (finalTitle) result.title = finalTitle;
    if (lastModel) result.model = lastModel;
    if (finalUsage) result.usage = finalUsage;
    return result;
  }

  resolveVendorSession: VendorSessionLookup = (
    _agent: string,
    cwd: string,
    profile: string,
    title: string,
    exclude: ReadonlySet<string>,
  ) => {
    const configDirs = this.resolveCandidateConfigDirs(profile);
    const slug = encodeClaudeProjectSlug(cwd);
    const wantCwd = normalizeCwd(cwd);
    const wantTitle = title.trim().toLowerCase();

    interface Candidate {
      sessionId: string;
      title: string;
      mtimeMs: number;
      latestTimestampMs: number;
    }

    const seenPaths = new Set<string>();
    const candidates: Candidate[] = [];

    const collectFile = (filePath: string, requireMatchingCwd: boolean) => {
      const absPath = path.resolve(filePath);
      if (seenPaths.has(absPath) || !isRegularFile(absPath)) {
        return;
      }
      seenPaths.add(absPath);

      let mtimeMs = 0;
      try {
        mtimeMs = statSync(absPath).mtimeMs;
      } catch {
        return;
      }

      const meta = inspectClaudeSessionFile(absPath);
      if (meta.archived || !meta.sessionId) {
        return;
      }
      if (requireMatchingCwd && wantCwd) {
        const metaCwd = normalizeCwd(meta.cwd);
        if (!metaCwd || metaCwd !== wantCwd) {
          return;
        }
      }

      candidates.push({
        sessionId: meta.sessionId,
        title: meta.title,
        mtimeMs,
        latestTimestampMs: meta.latestTimestampMs,
      });
    };

    for (const cfgDir of configDirs) {
      const projectsRoot = path.join(cfgDir, "projects");
      if (!isDirectory(projectsRoot)) {
        continue;
      }

      const exactProjDir = slug ? path.join(projectsRoot, slug) : "";
      if (exactProjDir && isDirectory(exactProjDir)) {
        for (const entry of readdirSync(exactProjDir, { withFileTypes: true })) {
          if (entry.name.toLowerCase().endsWith(".jsonl")) {
            collectFile(path.join(exactProjDir, entry.name), false);
          }
        }
        continue;
      }

      // Fallback when exact slug dir does not exist (e.g. non-standard dir name):
      // scan sibling project directories and match by recorded JSONL `cwd`.
      for (const projEntry of readdirSync(projectsRoot, { withFileTypes: true })) {
        const projPath = path.join(projectsRoot, projEntry.name);
        if (!isDirectory(projPath)) {
          continue;
        }
        for (const fileEntry of readdirSync(projPath, { withFileTypes: true })) {
          if (fileEntry.name.toLowerCase().endsWith(".jsonl")) {
            collectFile(path.join(projPath, fileEntry.name), wantCwd !== "");
          }
        }
      }
    }

    candidates.sort((a, b) => {
      if (a.mtimeMs !== b.mtimeMs) {
        return b.mtimeMs - a.mtimeMs;
      }
      if (a.latestTimestampMs !== b.latestTimestampMs) {
        return b.latestTimestampMs - a.latestTimestampMs;
      }
      return a.sessionId < b.sessionId ? -1 : a.sessionId > b.sessionId ? 1 : 0;
    });

    if (wantTitle) {
      for (const cand of candidates) {
        if (exclude.has(cand.sessionId)) {
          continue;
        }
        if (cand.title.trim().toLowerCase() === wantTitle) {
          return { sessionId: cand.sessionId, storeAvailable: true };
        }
      }
    }

    for (const cand of candidates) {
      if (exclude.has(cand.sessionId)) {
        continue;
      }
      return { sessionId: cand.sessionId, storeAvailable: true };
    }

    return { sessionId: "", storeAvailable: true };
  };

  async stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    let sessionId = opts.sessionId?.trim() ?? "";

    if (opts.headlessSession) {
      const stopResult = await opts.headlessSession.stopAndFlush();
      if (!sessionId && stopResult.sessionId?.trim()) {
        sessionId = stopResult.sessionId.trim();
      }
      if (!sessionId) {
        sessionId = opts.headlessSession.getSessionId()?.trim() ?? "";
      }
    }

    if (opts.stopInteractive) {
      await opts.stopInteractive();
    }

    if (!sessionId && !opts.cursor?.sessionPath) {
      return null;
    }

    const effectiveSessionId =
      sessionId || path.basename(opts.cursor!.sessionPath, ".jsonl");

    let effectiveConfigDir = opts.configDir?.trim() || undefined;
    if (!effectiveConfigDir && opts.profile?.trim()) {
      const { realHome, spacesDir } = this.resolvePaths();
      const profileCfg = loadProfileConfig("claude", opts.profile.trim(), {
        homeDir: realHome,
        spacesDir,
      });
      if (profileCfg?.shadowHomePath) {
        effectiveConfigDir = profileCfg.shadowHomePath;
      } else if (profileCfg?.homePath) {
        effectiveConfigDir = profileCfg.homePath;
      }
    }

    return this.readDelta(effectiveSessionId, opts.cwd, opts.cursor, effectiveConfigDir);
  }

  private resolvePaths(
    reqHomeDir?: string,
    reqSpacesDir?: string,
    env: Record<string, string | undefined> = process.env,
  ): { realHome: string; spacesDir: string } {
    const explicitHome = reqHomeDir ?? this.homeDir;
    const realHome = realHomeDir(explicitHome ?? env.HOME);
    const explicitSpaces = reqSpacesDir?.trim() || this.spacesDir?.trim();
    const spacesDir =
      explicitSpaces ||
      (explicitHome ? path.join(realHome, SPACES_CONFIG_DIR_NAME) : defaultSpacesDir(realHome, env));
    return { realHome, spacesDir };
  }

  private resolveConfigDir(explicitConfigDir?: string): string {
    const trimmed = explicitConfigDir?.trim();
    if (trimmed) {
      return trimmed;
    }
    if (!this.homeDir) {
      const envDir = (process.env.CLAUDE_CONFIG_DIR ?? "").trim();
      if (envDir) {
        return envDir;
      }
    }
    return path.join(realHomeDir(this.homeDir), ".claude");
  }

  private resolveCandidateConfigDirs(profile: string): string[] {
    const { realHome, spacesDir } = this.resolvePaths();
    const wantProfile = profile.trim();
    const dirs: string[] = [];

    const pushShadowVariants = (shadowHome: string) => {
      if (!shadowHome) return;
      dirs.push(shadowHome);
      dirs.push(path.join(shadowHome, ".claude"));
    };

    if (wantProfile && wantProfile.toLowerCase() !== "default") {
      const cfg = loadProfileConfig("claude", wantProfile, {
        homeDir: realHome,
        spacesDir,
      });
      if (cfg?.shadowHomePath) {
        pushShadowVariants(cfg.shadowHomePath);
      } else if (cfg?.homePath) {
        pushShadowVariants(cfg.homePath);
      }
      const spacesShadowHome = path.join(spacesDir, "profiles", "claude", wantProfile, "home");
      if (isDirectory(spacesShadowHome)) {
        pushShadowVariants(spacesShadowHome);
      }
      const harnessShadowHome = path.join(
        realHome,
        ".harness",
        "profiles",
        "claude",
        wantProfile,
        "home",
      );
      if (isDirectory(harnessShadowHome)) {
        pushShadowVariants(harnessShadowHome);
      }
      if (dirs.length > 0) {
        return dedupeStrings(dirs);
      }
    }

    if (!this.homeDir) {
      const envDir = (process.env.CLAUDE_CONFIG_DIR ?? "").trim();
      if (envDir) {
        dirs.push(envDir);
      }
    }
    dirs.push(path.join(realHome, ".claude"));

    return dedupeStrings(dirs);
  }

  private locateSessionFile(
    sessionId: string,
    cwd: string,
    configDir: string,
    cursorSessionPath?: string,
  ): string {
    if (cursorSessionPath && isRegularFile(cursorSessionPath)) {
      return cursorSessionPath;
    }

    const sid = sessionId.trim();
    if (!sid) {
      return cursorSessionPath ?? "";
    }

    const slug = encodeClaudeProjectSlug(cwd);
    const projectsRoot = path.join(configDir, "projects");
    const exactPath = slug ? path.join(projectsRoot, slug, `${sid}.jsonl`) : "";
    if (exactPath && isRegularFile(exactPath)) {
      return exactPath;
    }

    if (isDirectory(projectsRoot)) {
      const targetName = `${sid}.jsonl`;
      for (const projEntry of readdirSync(projectsRoot, { withFileTypes: true })) {
        const projPath = path.join(projectsRoot, projEntry.name);
        if (!isDirectory(projPath)) {
          continue;
        }
        const candidate = path.join(projPath, targetName);
        if (isRegularFile(candidate)) {
          return candidate;
        }
      }
    }

    return exactPath || cursorSessionPath || "";
  }
}

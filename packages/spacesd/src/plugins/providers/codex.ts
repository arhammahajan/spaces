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
import { CodexHeadlessExecutor } from "../../executors/codex";
import type { VendorSessionLookup } from "../../resurrect/capture";
import {
  defaultSpacesDir,
  loadProfileConfig,
  loadRouterConfig,
  realHomeDir,
} from "../profiles";
import type {
  AccessLevel,
  ConversationListOptions,
  ConversationRecord,
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
  ParsedLogMessage,
  StopAndFlushOptions,
} from "../types";

const MAX_DERIVED_TITLE_LEN = 120;
const ROLLOUT_FILE_PREFIX = "rollout-";
const UUID_SUFFIX_RE =
  /([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/i;

const BOILERPLATE_USER_PREFIXES = [
  "<environment_context>",
  "<instructions>",
  "<user_instructions>",
  "# agents.md",
  "<turn_aborted>",
  "<system-reminder>",
] as const;

const CODEX_ACCESS_ARGS: Record<AccessLevel, readonly string[]> = {
  "read-only": ["--sandbox", "read-only", "--ask-for-approval", "on-request"],
  default: [],
  auto: ["--full-auto"],
  full: ["--dangerously-bypass-approvals-and-sandbox"],
};

export interface CodexPluginOptions {
  homeDir?: string;
  spacesDir?: string;
  headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;
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

function normalizeCwd(cwd: string): string {
  const trimmed = cwd.trim();
  if (!trimmed) {
    return "";
  }
  return path.resolve(trimmed);
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

function walkRolloutFiles(dir: string, out: string[], maxDepth = 6, depth = 0): void {
  if (depth > maxDepth || !isDirectory(dir)) {
    return;
  }
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    if (entry.name.startsWith(".")) {
      continue;
    }
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      walkRolloutFiles(full, out, maxDepth, depth + 1);
    } else if (
      entry.isFile() &&
      entry.name.startsWith(ROLLOUT_FILE_PREFIX) &&
      entry.name.endsWith(".jsonl")
    ) {
      out.push(full);
    }
  }
}

function extractSessionIdFromFilename(filePath: string): string {
  const stem = path.basename(filePath, ".jsonl");
  const uuidMatch = UUID_SUFFIX_RE.exec(stem);
  if (uuidMatch && uuidMatch[1]) {
    return uuidMatch[1];
  }
  if (stem.startsWith(ROLLOUT_FILE_PREFIX)) {
    return stem.slice(ROLLOUT_FILE_PREFIX.length);
  }
  return stem;
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

function parseTokenUsageObject(raw: unknown): TurnUsage | undefined {
  if (!isRecord(raw)) {
    return undefined;
  }
  const inputTokens = finiteNumber(raw.input_tokens) ?? finiteNumber(raw.inputTokens) ?? 0;
  const outputTokens = finiteNumber(raw.output_tokens) ?? finiteNumber(raw.outputTokens) ?? 0;
  const reasoningTokens =
    finiteNumber(raw.reasoning_output_tokens) ??
    finiteNumber(raw.reasoning_tokens) ??
    finiteNumber(raw.reasoningTokens) ??
    0;
  const cacheReadTokens =
    finiteNumber(raw.cached_input_tokens) ??
    finiteNumber(raw.cache_read_tokens) ??
    finiteNumber(raw.cache_read_input_tokens) ??
    finiteNumber(raw.cacheReadTokens) ??
    0;
  const cacheWriteTokens =
    finiteNumber(raw.cache_write_tokens) ??
    finiteNumber(raw.cache_creation_input_tokens) ??
    finiteNumber(raw.cacheWriteTokens) ??
    0;
  const costUsd =
    finiteNumber(raw.cost_usd) ?? finiteNumber(raw.total_cost_usd) ?? finiteNumber(raw.costUsd) ?? 0;
  const costPriced =
    typeof raw.cost_priced === "boolean" ? raw.cost_priced : costUsd > 0;

  if (
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

interface SessionIndexEntry {
  title?: string;
  cwd?: string;
  updatedAtMs?: number;
  deleted?: boolean;
}

function loadCodexSessionIndex(codexDir: string): Map<string, SessionIndexEntry> {
  const map = new Map<string, SessionIndexEntry>();
  const indexPath = path.join(codexDir, "session_index.jsonl");
  if (!isRegularFile(indexPath)) {
    return map;
  }
  let raw: string;
  try {
    raw = readFileSync(indexPath, "utf8");
  } catch {
    return map;
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
    const id = firstNonEmpty(
      strField(parsed, "id"),
      strField(parsed, "session_id"),
      strField(parsed, "sessionId"),
    );
    if (!id) {
      continue;
    }
    const existing = map.get(id) ?? {};
    const title = firstNonEmpty(
      strField(parsed, "thread_name"),
      strField(parsed, "title"),
      existing.title,
    );
    const cwd = firstNonEmpty(strField(parsed, "cwd"), existing.cwd);
    const updatedAtMs =
      firstTimestampMs(parsed, "updated_at", "updatedAt", "timestamp") ?? existing.updatedAtMs;
    const deleted = parsed.deleted === true ? true : existing.deleted;
    map.set(id, {
      ...(title ? { title: cleanSingleLine(title) } : {}),
      ...(cwd ? { cwd } : {}),
      ...(updatedAtMs !== undefined ? { updatedAtMs } : {}),
      ...(deleted ? { deleted: true } : {}),
    });
  }
  return map;
}

interface ParsedRolloutLine {
  sessionId?: string;
  cwd?: string;
  model?: string;
  timestampMs?: number;
  turn?: ParsedDiskTurn;
  usage?: TurnUsage;
  isTotalUsage?: boolean;
}

function parseRolloutRecord(obj: Record<string, unknown>): ParsedRolloutLine {
  const out: ParsedRolloutLine = {};
  const typ = strField(obj, "type").toLowerCase();
  const payload = isRecord(obj.payload) ? obj.payload : undefined;
  const tsMs =
    firstTimestampMs(obj, "timestamp", "ts", "created_at", "createdAt") ??
    (payload ? firstTimestampMs(payload, "timestamp", "ts", "created_at", "createdAt") : undefined);
  if (tsMs !== undefined) {
    out.timestampMs = tsMs;
  }

  if (typ === "session_meta") {
    const sid = firstNonEmpty(
      strField(payload, "id"),
      strField(payload, "session_id"),
      strField(obj, "id"),
      strField(obj, "session_id"),
    );
    const cwd = firstNonEmpty(strField(payload, "cwd"), strField(obj, "cwd"));
    const model = firstNonEmpty(strField(payload, "model"), strField(obj, "model"));
    if (sid) out.sessionId = sid;
    if (cwd) out.cwd = cwd;
    if (model) out.model = model;
    return out;
  }

  if (typ === "turn_context") {
    const cwd = firstNonEmpty(strField(payload, "cwd"), strField(obj, "cwd"));
    const model = firstNonEmpty(strField(payload, "model"), strField(obj, "model"));
    if (cwd) out.cwd = cwd;
    if (model) out.model = model;
    return out;
  }

  if (typ === "event_msg" && payload) {
    const evType = strField(payload, "type").toLowerCase();
    if (evType === "user_message") {
      const text =
        firstNonEmpty(
          typeof payload.message === "string" ? payload.message : undefined,
          typeof payload.text === "string" ? payload.text : undefined,
        ) || extractTextFromContent(payload.content);
      if (text) {
        out.turn = {
          role: "user",
          text,
          ...(tsMs !== undefined ? { timestampMs: tsMs } : {}),
        };
      }
      return out;
    }
    if (evType === "agent_message") {
      const text =
        firstNonEmpty(
          typeof payload.message === "string" ? payload.message : undefined,
          typeof payload.text === "string" ? payload.text : undefined,
        ) || extractTextFromContent(payload.content);
      const model = strField(payload, "model");
      if (model) out.model = model;
      if (text) {
        out.turn = {
          role: "assistant",
          text,
          ...(tsMs !== undefined ? { timestampMs: tsMs } : {}),
          ...(model ? { model } : {}),
        };
      }
      return out;
    }
    if (evType === "token_count") {
      const info = isRecord(payload.info) ? payload.info : undefined;
      const totalUsage = parseTokenUsageObject(info?.total_token_usage ?? payload.total_token_usage);
      if (totalUsage) {
        out.usage = totalUsage;
        out.isTotalUsage = true;
      } else {
        const lastUsage = parseTokenUsageObject(info?.last_token_usage ?? payload.usage);
        if (lastUsage) {
          out.usage = lastUsage;
        }
      }
      return out;
    }
    return out;
  }

  if (typ === "response_item" && payload) {
    const itemType = strField(payload, "type").toLowerCase();
    if (itemType === "message") {
      const role = strField(payload, "role").toLowerCase();
      if (role === "user" || role === "assistant") {
        const text =
          extractTextFromContent(payload.content) ||
          firstNonEmpty(
            typeof payload.text === "string" ? payload.text : undefined,
            typeof payload.message === "string" ? payload.message : undefined,
          );
        const model = strField(payload, "model");
        if (model) out.model = model;
        if (text) {
          out.turn = {
            role,
            text,
            ...(tsMs !== undefined ? { timestampMs: tsMs } : {}),
            ...(model ? { model } : {}),
          };
        }
      }
    }
    return out;
  }

  return out;
}

interface RolloutInspection {
  sessionId: string;
  cwd: string;
  model: string;
  firstUserMessage: string;
  lastMessage: string;
  createdAtMs: number;
  latestTimestampMs: number;
  messageCount: number;
}

function inspectRolloutFile(filePath: string): RolloutInspection {
  const fallbackId = extractSessionIdFromFilename(filePath);
  let sessionId = "";
  let cwd = "";
  let model = "";
  let firstUserMessage = "";
  let lastMessage = "";
  let createdAtMs = 0;
  let latestTimestampMs = 0;
  let messageCount = 0;
  let prevRole = "";
  let prevText = "";

  let raw = "";
  try {
    raw = readFileSync(filePath, "utf8");
  } catch {
    return {
      sessionId: fallbackId,
      cwd: "",
      model: "",
      firstUserMessage: "",
      lastMessage: "",
      createdAtMs: 0,
      latestTimestampMs: 0,
      messageCount: 0,
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

    const rec = parseRolloutRecord(parsed);
    if (rec.sessionId && !sessionId) {
      sessionId = rec.sessionId;
    }
    if (rec.cwd && !cwd) {
      cwd = rec.cwd;
    }
    if (rec.model) {
      model = rec.model;
    }
    if (rec.timestampMs !== undefined) {
      if (createdAtMs === 0 || rec.timestampMs < createdAtMs) {
        createdAtMs = rec.timestampMs;
      }
      if (rec.timestampMs > latestTimestampMs) {
        latestTimestampMs = rec.timestampMs;
      }
    }
    if (rec.turn) {
      if (rec.turn.role === prevRole && rec.turn.text === prevText) {
        continue;
      }
      prevRole = rec.turn.role;
      prevText = rec.turn.text;
      messageCount += 1;
      lastMessage = rec.turn.text;
      if (rec.turn.role === "user" && !firstUserMessage && !isBoilerplateUserMessage(rec.turn.text)) {
        firstUserMessage = rec.turn.text;
      }
    }
  }

  return {
    sessionId: sessionId || fallbackId,
    cwd,
    model,
    firstUserMessage,
    lastMessage,
    createdAtMs,
    latestTimestampMs,
    messageCount,
  };
}

/**
 * Unified `HarnessPlugin` implementation for OpenAI Codex CLI (`codex`).
 */
export class CodexPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = {
    id: "codex",
    aliases: ["openai-codex"],
    displayApp: "codex",
    cliDisplayApp: "codex",
    binary: "codex",
    screenManifestId: "codex",
    oscAuthority: true,
    resumeSubcommand: "resume",
    positionalResume: true,
    uniqueExplicitSessions: true,
    titlePrefixes: ["Codex - ", "Codex: "],
    mcpTargets: [".codex/config.toml"],
  };

  private readonly homeDir?: string;
  private readonly spacesDir?: string;
  private readonly headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;

  constructor(options: CodexPluginOptions = {}) {
    this.homeDir = options.homeDir;
    this.spacesDir = options.spacesDir;
    this.headlessFactory = options.headlessFactory;
  }

  resumeArgs(sessionId: string): string[] {
    const sid = sessionId.trim();
    return sid ? ["resume", sid] : [];
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
      profile = loadProfileConfig("codex", profileName, {
        homeDir: realHome,
        spacesDir,
      });
      if (profile && !profile.enabled) {
        throw new Error(`profile disabled: codex/${profile.name}`);
      }
    }

    const shadowHome = profile?.shadowHomePath?.trim() || undefined;
    const routerEnabled = !req.noRouter && (profile ? profile.router : true);

    const env: Record<string, string> = {};
    const routerArgs: string[] = [];

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
      const baseUrl = `${router.url}/v1`;
      routerArgs.push(
        "-c",
        'model_provider="model_router"',
        "-c",
        'model="gpt-6-luna"',
        "-c",
        'service_tier="default"',
        "-c",
        'model_providers.model_router.name="model_router"',
        "-c",
        `model_providers.model_router.base_url="${baseUrl}"`,
        "-c",
        'model_providers.model_router.env_key="MODEL_ROUTER_API_KEY"',
        "-c",
        'model_providers.model_router.wire_api="responses"',
        "-c",
        "model_providers.model_router.requires_openai_auth=false",
        "-c",
        "model_providers.model_router.supports_websockets=false",
      );
      env.MODEL_ROUTER_API_KEY = router.apiKey;
    }

    if (shadowHome) {
      env.CODEX_HOME = shadowHome;
    } else if (profile?.homePath?.trim()) {
      env.CODEX_HOME = profile.homePath.trim();
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
      unsetEnvList.push("OPENAI_API_KEY", "OPENAI_BASE_URL");
    }
    const unsetEnv = dedupeStrings(unsetEnvList).filter((key) => !Object.hasOwn(env, key));

    const defaultBinary = path.join(realHome, ".local", "bin", "codex");
    const binary = profile?.binaryPath?.trim() || defaultBinary;
    const accessArgs = req.accessLevel ? CODEX_ACCESS_ARGS[req.accessLevel] ?? [] : [];
    const modelArgs = req.model?.trim() ? ["--model", req.model.trim()] : [];
    const effortArgs = req.effort?.trim()
      ? ["-c", `model_reasoning_effort="${req.effort.trim()}"`]
      : [];
    const resumeArgs = req.sessionId?.trim() ? this.resumeArgs(req.sessionId) : [];

    const argv = [
      binary,
      ...(profile?.prependArgs ?? []),
      ...routerArgs,
      ...accessArgs,
      ...modelArgs,
      ...effortArgs,
      ...resumeArgs,
      ...(req.extraArgs ?? []),
    ];

    const configDir =
      env.CODEX_HOME ||
      (explicitHome ? req.env?.CODEX_HOME : baseEnv.CODEX_HOME)?.trim() ||
      path.join(realHome, ".codex");

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
    return new CodexHeadlessExecutor({
      threadId: req.threadId,
      launch,
      sessionId: req.sessionId,
    });
  }

  storePaths(homeDir: string): string[] {
    return [
      path.join(homeDir, ".codex", "sessions"),
      path.join(homeDir, ".codex", "archived_sessions"),
    ];
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    const codexDirs = this.resolveListCodexDirs(opts.homeDir);
    const seenPaths = new Set<string>();
    const records: ConversationRecord[] = [];

    for (const codexDir of codexDirs) {
      const indexMap = loadCodexSessionIndex(codexDir);
      const sources: Array<{ dir: string; archived: boolean }> = [
        { dir: path.join(codexDir, "sessions"), archived: false },
      ];
      if (opts.includeArchived) {
        sources.push({ dir: path.join(codexDir, "archived_sessions"), archived: true });
      }

      for (const source of sources) {
        const files: string[] = [];
        walkRolloutFiles(source.dir, files);
        for (const filePath of files) {
          const absPath = path.resolve(filePath);
          if (seenPaths.has(absPath)) {
            continue;
          }
          seenPaths.add(absPath);

          let st;
          try {
            st = statSync(absPath);
          } catch {
            continue;
          }

          const meta = inspectRolloutFile(absPath);
          const idx = indexMap.get(meta.sessionId);
          if (idx?.deleted) {
            continue;
          }

          const cwd = meta.cwd || idx?.cwd || "";
          const title = idx?.title || "";
          const createdAtMs = meta.createdAtMs || Math.trunc(st.birthtimeMs || st.mtimeMs);
          const updatedAtMs = Math.max(
            Math.trunc(st.mtimeMs),
            meta.latestTimestampMs,
            idx?.updatedAtMs ?? 0,
          );

          records.push({
            id: meta.sessionId,
            agent: this.spec.id,
            app: this.spec.cliDisplayApp,
            ...(opts.profile ? { profile: opts.profile } : {}),
            ...(cwd ? { cwd } : {}),
            ...(title ? { title } : {}),
            ...(meta.firstUserMessage ? { firstMessage: meta.firstUserMessage } : {}),
            ...(meta.lastMessage ? { lastMessage: meta.lastMessage } : {}),
            ...(meta.model ? { model: meta.model } : {}),
            sourcePath: absPath,
            resumeTarget: meta.sessionId,
            createdAtMs,
            updatedAtMs,
            messageCount: meta.messageCount,
            ...(source.archived ? { archived: true } : {}),
          });
        }
      }
    }

    records.sort((a, b) => b.updatedAtMs - a.updatedAtMs);
    return records;
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    if (!existsSync(sourcePath)) {
      return [];
    }
    let raw: string;
    try {
      raw = readFileSync(sourcePath, "utf8");
    } catch {
      return [];
    }

    const messages: ParsedLogMessage[] = [];
    let currentModel = "";
    let prevRole = "";
    let prevText = "";

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

      const rec = parseRolloutRecord(parsed);
      if (rec.model) {
        currentModel = rec.model;
      }
      if (!rec.turn) {
        continue;
      }
      if (rec.turn.role === prevRole && rec.turn.text === prevText) {
        continue;
      }
      prevRole = rec.turn.role;
      prevText = rec.turn.text;

      const model = rec.turn.model || (rec.turn.role === "assistant" ? currentModel : "");
      messages.push({
        role: rec.turn.role,
        text: rec.turn.text,
        ...(rec.turn.timestampMs !== undefined ? { timestampMs: rec.turn.timestampMs } : {}),
        ...(model ? { model } : {}),
      });
    }

    return messages;
  }

  readDelta(
    sessionId: string,
    _cwd: string,
    cursor?: DeltaCursor,
    configDir?: string,
  ): DeltaReadResult {
    const effectiveConfigDir = this.resolveConfigDir(configDir);
    const resolvedPath = this.locateSessionFile(
      sessionId,
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
    let lastModel = "";
    let lastUsage: TurnUsage | undefined;
    let prevRole = "";
    let prevText = "";

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
      if (!isRecord(parsed)) {
        continue;
      }

      const rec = parseRolloutRecord(parsed);
      if (rec.model) {
        lastModel = rec.model;
      }
      if (rec.usage) {
        lastUsage = rec.usage;
      }
      if (rec.turn) {
        if (rec.turn.role === prevRole && rec.turn.text === prevText) {
          continue;
        }
        prevRole = rec.turn.role;
        prevText = rec.turn.text;
        const turn: ParsedDiskTurn = {
          role: rec.turn.role,
          text: rec.turn.text,
        };
        if (rec.turn.timestampMs !== undefined) turn.timestampMs = rec.turn.timestampMs;
        const turnModel = rec.turn.model || (rec.turn.role === "assistant" ? lastModel : "");
        if (turnModel) turn.model = turnModel;
        turns.push(turn);
      }
    }

    const indexMap = loadCodexSessionIndex(effectiveConfigDir);
    const effectiveSid = sessionId.trim() || extractSessionIdFromFilename(resolvedPath);
    const indexTitle = indexMap.get(effectiveSid)?.title;

    const result: DeltaReadResult = {
      nextCursor: {
        sessionPath: resolvedPath,
        byteOffset: nextOffset,
        ...(nextPartial ? { partialLine: nextPartial } : {}),
      },
      turns,
    };
    if (indexTitle) result.title = indexTitle;
    if (lastModel) result.model = lastModel;
    if (lastUsage) result.usage = lastUsage;
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
    const wantCwd = normalizeCwd(cwd);
    const wantTitle = title.trim().toLowerCase();

    interface Candidate {
      sessionId: string;
      title: string;
      firstMessage: string;
      mtimeMs: number;
      latestTimestampMs: number;
    }

    let storeAvailable = false;
    const seenPaths = new Set<string>();
    const candidates: Candidate[] = [];

    for (const cfgDir of configDirs) {
      const sessionsRoot = path.join(cfgDir, "sessions");
      if (!isDirectory(sessionsRoot)) {
        continue;
      }
      storeAvailable = true;

      const indexMap = loadCodexSessionIndex(cfgDir);
      const files: string[] = [];
      walkRolloutFiles(sessionsRoot, files);

      for (const filePath of files) {
        const absPath = path.resolve(filePath);
        if (seenPaths.has(absPath)) {
          continue;
        }
        seenPaths.add(absPath);

        let mtimeMs = 0;
        try {
          mtimeMs = statSync(absPath).mtimeMs;
        } catch {
          continue;
        }

        const meta = inspectRolloutFile(absPath);
        if (!meta.sessionId) {
          continue;
        }
        const idx = indexMap.get(meta.sessionId);
        if (idx?.deleted) {
          continue;
        }

        const effectiveCwd = meta.cwd || idx?.cwd || "";
        if (wantCwd) {
          const normEffectiveCwd = normalizeCwd(effectiveCwd);
          if (!normEffectiveCwd || normEffectiveCwd !== wantCwd) {
            continue;
          }
        }

        candidates.push({
          sessionId: meta.sessionId,
          title: idx?.title || "",
          firstMessage: cleanSingleLine(meta.firstUserMessage),
          mtimeMs,
          latestTimestampMs: meta.latestTimestampMs,
        });
      }
    }

    if (!storeAvailable) {
      return { sessionId: "", storeAvailable: false };
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
        if (
          (cand.title && cand.title.trim().toLowerCase() === wantTitle) ||
          (cand.firstMessage && cand.firstMessage.trim().toLowerCase() === wantTitle)
        ) {
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
      sessionId || extractSessionIdFromFilename(opts.cursor!.sessionPath);

    let effectiveConfigDir = opts.configDir?.trim() || undefined;
    if (!effectiveConfigDir && opts.profile?.trim()) {
      const { realHome, spacesDir } = this.resolvePaths();
      const profileCfg = loadProfileConfig("codex", opts.profile.trim(), {
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
      const envDir = (process.env.CODEX_HOME ?? "").trim();
      if (envDir) {
        return envDir;
      }
    }
    return path.join(realHomeDir(this.homeDir), ".codex");
  }

  private resolveListCodexDirs(homeDir: string): string[] {
    const dotCodex = path.join(homeDir, ".codex");
    const dirs: string[] = [];
    if (
      isDirectory(path.join(dotCodex, "sessions")) ||
      isDirectory(path.join(dotCodex, "archived_sessions"))
    ) {
      dirs.push(dotCodex);
    }
    if (
      isDirectory(path.join(homeDir, "sessions")) ||
      isDirectory(path.join(homeDir, "archived_sessions"))
    ) {
      dirs.push(homeDir);
    }
    if (dirs.length === 0) {
      dirs.push(dotCodex);
    }
    return dedupeStrings(dirs);
  }

  private resolveCandidateConfigDirs(profile: string): string[] {
    const { realHome, spacesDir } = this.resolvePaths();
    const wantProfile = profile.trim();
    const dirs: string[] = [];

    const pushShadowVariants = (shadowHome: string) => {
      if (!shadowHome) return;
      dirs.push(shadowHome);
      dirs.push(path.join(shadowHome, ".codex"));
    };

    if (wantProfile && wantProfile.toLowerCase() !== "default") {
      const cfg = loadProfileConfig("codex", wantProfile, {
        homeDir: realHome,
        spacesDir,
      });
      if (cfg?.shadowHomePath) {
        pushShadowVariants(cfg.shadowHomePath);
      } else if (cfg?.homePath) {
        pushShadowVariants(cfg.homePath);
      }
      const spacesShadowHome = path.join(spacesDir, "profiles", "codex", wantProfile, "home");
      if (isDirectory(spacesShadowHome)) {
        pushShadowVariants(spacesShadowHome);
      }
      const harnessShadowHome = path.join(
        realHome,
        ".harness",
        "profiles",
        "codex",
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
      const envDir = (process.env.CODEX_HOME ?? "").trim();
      if (envDir) {
        dirs.push(envDir);
      }
    }
    dirs.push(path.join(realHome, ".codex"));

    return dedupeStrings(dirs);
  }

  private locateSessionFile(
    sessionId: string,
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

    const roots = [
      path.join(configDir, "sessions"),
      path.join(configDir, "archived_sessions"),
    ];
    const allFiles: string[] = [];
    for (const root of roots) {
      walkRolloutFiles(root, allFiles);
    }

    // Fast path: filename contains sessionId (e.g. rollout-2026-...-<uuid>.jsonl)
    for (const filePath of allFiles) {
      const base = path.basename(filePath);
      if (base.includes(sid)) {
        return filePath;
      }
    }

    // Fallback: inspect session_meta header in each rollout file
    for (const filePath of allFiles) {
      const meta = inspectRolloutFile(filePath);
      if (meta.sessionId === sid) {
        return filePath;
      }
    }

    return cursorSessionPath ?? "";
  }
}

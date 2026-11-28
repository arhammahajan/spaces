import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, readlinkSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import type { VendorSessionLookup } from "../../resurrect/capture";
import {
  buildStandardResumeArgs,
  createEmptyDelta,
  expandTilde,
  runStandardStopAndFlush,
} from "../helpers/launch";
import {
  normalizeEpochMs,
  sqliteTableExists,
  withReadOnlySqlite,
} from "../helpers/sqlite-store";
import {
  defaultSpacesDir,
  loadProfileConfig,
  loadRouterConfig,
  realHomeDir,
} from "../profiles";
import {
  HeadlessUnsupportedError,
  type AccessLevel,
  type ConversationListOptions,
  type ConversationRecord,
  type DeltaCursor,
  type DeltaReadResult,
  type HarnessPlugin,
  type HarnessPluginSpec,
  type HeadlessSession,
  type HeadlessStartRequest,
  type InteractiveLaunchRequest,
  type InteractiveLaunchSpec,
  type StopAndFlushOptions,
} from "../types";

const MAX_PROC_BFS_DEPTH = 6;
const DEFAULT_ANTIGRAVITY_BINARY = "~/.local/libexec/antigravity/agy";
const DEFAULT_ROUTER_MODEL = "gemini-4-argon";
const DEFAULT_ROUTER_SETTINGS_JSON = '{"modelProvider":"gemini"}';

const CONVERSATION_DB_RE =
  /antigravity-cli\/conversations\/([^/\s]+?)\.db(?:-(?:wal|shm))?$/;
const PRESENCE_LOCK_RE = /antigravity-cli\/presence\/([^/\s]+?)\.lock$/;

const ANTIGRAVITY_ACCESS_ARGS: Record<AccessLevel, readonly string[]> = {
  "read-only": ["--sandbox"],
  default: [],
  auto: ["--mode", "accept-edits"],
  full: ["--yolo"],
};

const SHADOW_UNSET_ENV = [
  "ANTIGRAVITY_APP_DATA_DIR",
  "ANTIGRAVITY_CONFIG_REL_DIR",
  "GOOGLE_API_KEY",
  "GOOGLE_APPLICATION_CREDENTIALS",
  "GOOGLE_CLOUD_PROJECT",
  "GEMINI_API_KEY",
] as const;

export const ANTIGRAVITY_PLUGIN_SPEC: HarnessPluginSpec = {
  id: "antigravity",
  aliases: ["agy", "antigravity-cli"],
  displayApp: "antigravity",
  cliDisplayApp: "antigravity",
  binary: "antigravity",
  screenManifestId: "agy",
  oscAuthority: false,
  resumeFlag: "--conversation",
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [
    ".gemini/config/mcp_config.json",
    ".gemini/antigravity-cli/settings.json",
  ],
};

/**
 * Injectable `/proc` reader abstraction so Linux `/proc/<pid>/fd` live session
 * discovery can be tested deterministically without real processes.
 */
export interface AntigravityProcReader {
  /** Direct child PIDs of `pid` (e.g. from `/proc/<pid>/task/*\/children`). */
  listChildren(pid: number): number[];
  /** Resolved symlink targets of `/proc/<pid>/fd/*`. */
  listFdTargets(pid: number): string[];
}

export interface AntigravityPluginOptions {
  homeDir?: string;
  spacesDir?: string;
  procReader?: AntigravityProcReader;
}

/**
 * Default Linux `/proc` reader inspecting `/proc/<pid>/task/*\/children` and
 * `/proc/<pid>/fd/*` symlinks.
 */
export function createLinuxProcReader(procRoot = "/proc"): AntigravityProcReader {
  return {
    listChildren(pid: number): number[] {
      const taskDir = path.join(procRoot, String(pid), "task");
      let tids: string[];
      try {
        tids = readdirSync(taskDir).sort();
      } catch {
        return [];
      }
      const children: number[] = [];
      const seen = new Set<number>();
      for (const tid of tids) {
        let raw: string;
        try {
          raw = readFileSync(path.join(taskDir, tid, "children"), "utf8");
        } catch {
          continue;
        }
        for (const tok of raw.trim().split(/\s+/)) {
          if (!tok) continue;
          const child = Number.parseInt(tok, 10);
          if (Number.isInteger(child) && child > 0 && !seen.has(child)) {
            seen.add(child);
            children.push(child);
          }
        }
      }
      return children;
    },

    listFdTargets(pid: number): string[] {
      const fdDir = path.join(procRoot, String(pid), "fd");
      let entries: string[];
      try {
        entries = readdirSync(fdDir).sort();
      } catch {
        return [];
      }
      const targets: string[] = [];
      for (const ent of entries) {
        try {
          targets.push(readlinkSync(path.join(fdDir, ent)));
        } catch {
          // FD may have closed concurrently or be unreadable.
        }
      }
      return targets;
    },
  };
}

function stripDeletedSuffix(target: string): string {
  return target.replace(/ \(deleted\)$/u, "").trim();
}

function matchAntigravityFdTarget(
  rawTarget: string,
): { kind: "db" | "lock"; sessionId: string } | null {
  const normalized = stripDeletedSuffix(rawTarget).replaceAll("\\", "/");
  const dbMatch = CONVERSATION_DB_RE.exec(normalized);
  if (dbMatch?.[1]) {
    return { kind: "db", sessionId: dbMatch[1] };
  }
  const lockMatch = PRESENCE_LOCK_RE.exec(normalized);
  if (lockMatch?.[1]) {
    return { kind: "lock", sessionId: lockMatch[1] };
  }
  return null;
}

function hasFlag(argv: readonly string[], flags: readonly string[]): boolean {
  for (const tok of argv) {
    for (const flag of flags) {
      if (tok === flag || tok.startsWith(`${flag}=`)) {
        return true;
      }
    }
  }
  return false;
}

function readAntigravitySettingsModel(
  shadowHome: string | undefined,
  baseHome: string,
): string | undefined {
  const candidates = [
    ...(shadowHome
      ? [path.join(shadowHome, ".gemini", "antigravity-cli", "settings.json")]
      : []),
    path.join(baseHome, ".gemini", "antigravity-cli", "settings.json"),
  ];
  for (const filePath of candidates) {
    if (!existsSync(filePath)) continue;
    try {
      const parsed = JSON.parse(readFileSync(filePath, "utf8")) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const model = (parsed as Record<string, unknown>).model;
        if (typeof model === "string" && model.trim() !== "") {
          return model.trim();
        }
      }
    } catch {
      // Ignore malformed settings.json.
    }
  }
  return undefined;
}

function parseWorkspaceUriToPath(uri: string): string | undefined {
  const trimmed = uri.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith("file://")) {
    try {
      return fileURLToPath(trimmed);
    } catch {
      const stripped = trimmed.slice("file://".length);
      try {
        return decodeURIComponent(stripped);
      } catch {
        return stripped;
      }
    }
  }
  if (path.isAbsolute(trimmed)) {
    return trimmed;
  }
  return undefined;
}

function parseWorkspaceUrisCwd(raw: unknown): string | undefined {
  if (typeof raw !== "string") {
    return undefined;
  }
  const trimmed = raw.trim();
  if (!trimmed) return undefined;

  if (trimmed.startsWith("[")) {
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      if (Array.isArray(parsed)) {
        for (const item of parsed) {
          if (typeof item === "string") {
            const resolved = parseWorkspaceUriToPath(item);
            if (resolved) return resolved;
          }
        }
      }
    } catch {
      // Fall through to plain-string parsing.
    }
  }

  for (const part of trimmed.split(/[\n,]/)) {
    const resolved = parseWorkspaceUriToPath(part);
    if (resolved) return resolved;
  }
  return undefined;
}

function nonEmptyString(val: unknown): string {
  return typeof val === "string" ? val.trim() : "";
}

function nonNegativeInt(val: unknown): number {
  if (typeof val === "number" && Number.isFinite(val) && val >= 0) {
    return Math.trunc(val);
  }
  if (typeof val === "string" && val.trim()) {
    const n = Number(val);
    if (Number.isFinite(n) && n >= 0) {
      return Math.trunc(n);
    }
  }
  return 0;
}

function isTruthyFlag(val: unknown): boolean {
  if (typeof val === "boolean") return val;
  if (typeof val === "number") return val !== 0;
  if (typeof val === "string") {
    const lower = val.trim().toLowerCase();
    return lower === "1" || lower === "true" || lower === "yes";
  }
  return false;
}

export function antigravitySummaryDbPath(homeDir: string): string {
  return path.join(homeDir, ".gemini", "antigravity-cli", "conversation_summaries.db");
}

/**
 * Unified `HarnessPlugin` implementation for Google Antigravity CLI (`antigravity` / `agy`).
 */
export class AntigravityPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = ANTIGRAVITY_PLUGIN_SPEC;

  private readonly homeDir?: string;
  private readonly spacesDir?: string;
  private readonly procReader: AntigravityProcReader;

  constructor(options: AntigravityPluginOptions = {}) {
    this.homeDir = options.homeDir;
    this.spacesDir = options.spacesDir;
    this.procReader = options.procReader ?? createLinuxProcReader();
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  /**
   * BFS over descendant PIDs (depth <= 6) via `/proc/<pid>/task/*\/children`,
   * reading `/proc/<pid>/fd/*` symlinks for:
   * 1. `antigravity-cli/conversations/<id>.db{,-wal,-shm}` (preferred)
   * 2. `antigravity-cli/presence/<id>.lock` (fallback)
   */
  discoverLiveSessionId(pid: number): string {
    if (!Number.isInteger(pid) || pid <= 0) {
      return "";
    }

    const visited = new Set<number>([pid]);
    const queue: Array<{ pid: number; depth: number }> = [{ pid, depth: 0 }];
    let fallbackLockId = "";

    while (queue.length > 0) {
      const current = queue.shift()!;

      for (const target of this.procReader.listFdTargets(current.pid)) {
        const match = matchAntigravityFdTarget(target);
        if (!match) continue;
        if (match.kind === "db") {
          return match.sessionId;
        }
        if (!fallbackLockId) {
          fallbackLockId = match.sessionId;
        }
      }

      if (current.depth >= MAX_PROC_BFS_DEPTH) {
        continue;
      }

      for (const child of this.procReader.listChildren(current.pid)) {
        if (Number.isInteger(child) && child > 0 && !visited.has(child)) {
          visited.add(child);
          queue.push({ pid: child, depth: current.depth + 1 });
        }
      }
    }

    return fallbackLockId;
  }

  storePaths(homeDir: string): string[] {
    return [antigravitySummaryDbPath(homeDir)];
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    const dbPath = antigravitySummaryDbPath(opts.homeDir);
    const rows = withReadOnlySqlite(dbPath, (db) => {
      if (!sqliteTableExists(db, "conversation_summaries")) {
        return [] as Record<string, unknown>[];
      }
      return db
        .query<Record<string, unknown>, []>("SELECT * FROM conversation_summaries")
        .all();
    });

    if (!rows || rows.length === 0) {
      return [];
    }

    const records: ConversationRecord[] = [];
    for (const row of rows) {
      const parentId = nonEmptyString(row.parent_conversation_id);
      if (parentId !== "") {
        continue;
      }

      const archived = isTruthyFlag(row.archived ?? row.is_archived);
      if (archived && !opts.includeArchived) {
        continue;
      }

      const id =
        nonEmptyString(row.conversation_id) ||
        nonEmptyString(row.id);
      if (!id) {
        continue;
      }

      const title =
        nonEmptyString(row.title) ||
        nonEmptyString(row.summary) ||
        undefined;
      const cwd =
        parseWorkspaceUrisCwd(row.workspace_uris) ||
        nonEmptyString(row.cwd) ||
        undefined;
      const model = nonEmptyString(row.model) || undefined;

      const createdAtMs = normalizeEpochMs(
        row.created_at_ms ?? row.created_at ?? row.created_time ?? row.createdAt,
      );
      const updatedAtMs =
        normalizeEpochMs(
          row.updated_at_ms ??
            row.updated_at ??
            row.last_modified_time ??
            row.updatedAt,
        ) || createdAtMs;
      const messageCount = nonNegativeInt(
        row.message_count ?? row.step_count ?? row.turn_count,
      );

      records.push({
        id,
        agent: this.spec.id,
        app: this.spec.displayApp,
        ...(opts.profile ? { profile: opts.profile } : {}),
        ...(cwd ? { cwd } : {}),
        ...(title ? { title } : {}),
        ...(model ? { model } : {}),
        sourcePath: dbPath,
        resumeTarget: id,
        createdAtMs,
        updatedAtMs,
        messageCount,
        ...(archived ? { archived: true } : {}),
      });
    }

    records.sort((a, b) => b.updatedAtMs - a.updatedAtMs);
    return records;
  }

  resolveVendorSession: VendorSessionLookup = (
    _agent: string,
    cwd: string,
    profile: string,
    title: string,
    exclude: ReadonlySet<string>,
  ) => {
    const baseHome = realHomeDir(this.homeDir ?? os.homedir());
    const spacesDir = this.spacesDir ?? defaultSpacesDir(baseHome);

    let targetHome = baseHome;
    if (profile.trim()) {
      const loaded = loadProfileConfig(this.spec.id, profile, {
        homeDir: baseHome,
        spacesDir,
      });
      if (loaded?.shadowHomePath) {
        targetHome = loaded.shadowHomePath;
      } else if (loaded?.homePath) {
        targetHome = loaded.homePath;
      }
    }

    const dbPath = antigravitySummaryDbPath(targetHome);
    if (!existsSync(dbPath)) {
      return { sessionId: "", storeAvailable: false };
    }

    const records = this.listConversations({
      homeDir: targetHome,
      profile: profile.trim() || undefined,
      includeArchived: false,
    });

    const normCwd = cwd.trim() ? path.resolve(cwd) : "";
    const normTitle = title.trim().toLowerCase();

    const matching = records
      .filter((r) => !exclude.has(r.id))
      .filter((r) => !normCwd || !r.cwd || path.resolve(r.cwd) === normCwd)
      .sort((a, b) => b.updatedAtMs - a.updatedAtMs);

    if (normTitle) {
      const exact = matching.find(
        (r) => r.title && r.title.trim().toLowerCase() === normTitle,
      );
      if (exact) {
        return { sessionId: exact.id, storeAvailable: true };
      }
    }

    return {
      sessionId: matching[0]?.id ?? "",
      storeAvailable: true,
    };
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    const reqEnv = req.env ?? process.env;
    const baseHome = realHomeDir(req.homeDir ?? this.homeDir ?? os.homedir());
    const spacesDir =
      req.spacesDir ?? this.spacesDir ?? defaultSpacesDir(baseHome, reqEnv);

    const profile = req.profile
      ? loadProfileConfig(this.spec.id, req.profile, {
          homeDir: baseHome,
          spacesDir,
        })
      : null;
    if (profile && !profile.enabled) {
      throw new Error(`profile disabled for ${this.spec.id}: ${req.profile}`);
    }

    const routerEnabled =
      req.noRouter === true ? false : profile ? profile.router : true;

    let shadowHome: string | undefined;
    if (profile?.shadowHomePath) {
      shadowHome = profile.shadowHomePath;
    } else if (profile?.homePath) {
      shadowHome = profile.homePath;
    } else if (routerEnabled) {
      shadowHome = path.join(spacesDir, "profiles", "antigravity", "router", "home");
    }

    const binaryRaw = profile?.binaryPath || DEFAULT_ANTIGRAVITY_BINARY;
    const binary = expandTilde(binaryRaw, baseHome);

    const argv: string[] = [binary];
    if (profile?.prependArgs?.length) {
      argv.push(...profile.prependArgs);
    }

    if (req.accessLevel && ANTIGRAVITY_ACCESS_ARGS[req.accessLevel].length > 0) {
      argv.push(...ANTIGRAVITY_ACCESS_ARGS[req.accessLevel]);
    }

    if (req.model?.trim()) {
      argv.push("--model", req.model.trim());
    }

    if (req.sessionId?.trim()) {
      argv.push(...this.resumeArgs(req.sessionId));
    }

    if (req.extraArgs?.length) {
      argv.push(...req.extraArgs);
    }

    const env: Record<string, string> = {};
    const unsetEnv: string[] = ["HARNESS_AGENT", "HARNESS_PROFILE"];

    if (shadowHome) {
      const geminiDir = path.join(shadowHome, ".gemini");
      const scopeHash = createHash("sha256")
        .update(shadowHome)
        .digest("hex")
        .slice(0, 6);

      env.HOME = shadowHome;
      env.XDG_CONFIG_HOME = path.join(shadowHome, ".config");
      env.XDG_DATA_HOME = path.join(shadowHome, ".local", "share");
      env.XDG_STATE_HOME = path.join(shadowHome, ".local", "state");
      env.XDG_CACHE_HOME = path.join(shadowHome, ".cache");
      env.ANTIGRAVITY_GEMINI_DIR = geminiDir;
      env.GEMINI_HOME = geminiDir;
      env.DBUS_SESSION_BUS_ADDRESS = "disabled:";
      env.ANTIGRAVITY_CREDENTIAL_SCOPE = `harness-profile-${scopeHash}`;
      env.HARNESS_REAL_HOME = baseHome;
      env.BASH_ENV = path.join(spacesDir, "harness-restore-env.sh");

      unsetEnv.push(...SHADOW_UNSET_ENV);
    }

    if (routerEnabled) {
      const explicitHome = req.homeDir ?? this.homeDir;
      const routerEnvInput =
        explicitHome && !req.env?.HOME ? { ...reqEnv, HOME: baseHome } : reqEnv;
      const xdgConfigHome =
        explicitHome && !req.env?.XDG_CONFIG_HOME
          ? path.join(baseHome, ".config")
          : undefined;
      const router = loadRouterConfig({
        env: routerEnvInput,
        xdgConfigHome,
      });

      env.GOOGLE_GEMINI_BASE_URL = router.url;
      env.GEMINI_API_KEY = router.apiKey;

      if (!hasFlag(argv.slice(1), ["--model"])) {
        const configuredModel = readAntigravitySettingsModel(shadowHome, baseHome);
        argv.push("--model", configuredModel ?? DEFAULT_ROUTER_MODEL);
      }

      if (!hasFlag(argv.slice(1), ["--settings", "-settings"])) {
        argv.push("--settings", DEFAULT_ROUTER_SETTINGS_JSON);
      }
    } else {
      unsetEnv.push("GOOGLE_GEMINI_BASE_URL");
    }

    if (profile?.env) {
      Object.assign(env, profile.env);
    }

    return {
      argv,
      env,
      unsetEnv: Array.from(new Set(unsetEnv)),
      cwd: req.cwd,
      ...(shadowHome ? { shadowHome } : {}),
    };
  }

  startHeadless(_req: HeadlessStartRequest): HeadlessSession {
    throw new HeadlessUnsupportedError("antigravity");
  }

  readDelta(
    sessionId: string,
    _cwd: string,
    cursor?: DeltaCursor,
    _configDir?: string,
  ): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }
}

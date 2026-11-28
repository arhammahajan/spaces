import type { Database } from "bun:sqlite";
import { existsSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { VendorSessionLookup } from "../../resurrect/capture";
import {
  listJsonStoreConversations,
  parseJsonOrJsonlLog,
  type JsonStoreScanSpec,
} from "../helpers/json-store";
import {
  buildStandardInteractiveLaunch,
  buildStandardResumeArgs,
  createEmptyDelta,
  runStandardStopAndFlush,
  unsupportedHeadless,
} from "../helpers/launch";
import {
  normalizeEpochMs,
  sqliteTableExists,
  withReadOnlySqlite,
} from "../helpers/sqlite-store";
import { loadProfileConfig, realHomeDir } from "../profiles";
import type {
  ConversationListOptions,
  ConversationRecord,
  DeltaCursor,
  DeltaReadResult,
  HarnessPlugin,
  HarnessPluginSpec,
  HeadlessSession,
  HeadlessStartRequest,
  InteractiveLaunchRequest,
  InteractiveLaunchSpec,
  ParsedLogMessage,
  StopAndFlushOptions,
} from "../types";

const MAX_DERIVED_TITLE_LEN = 120;

const MUSE_DB_REL = path.join(".local", "share", "muse", "session-index.db");
const MUSE_JSON_SPEC: JsonStoreScanSpec = {
  agent: "muse",
  app: "muse",
  relDirs: [
    path.join(".config", "muse", "sessions"),
    path.join(".share", "muse", "sessions"),
    path.join(".local", "share", "muse", "sessions"),
  ],
};

export interface MusePluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

function cleanSingleLine(raw: string, maxRunes: number): string {
  const collapsed = raw.trim().replace(/\s+/g, " ");
  if (!collapsed) return "";
  const runes = Array.from(collapsed);
  if (runes.length <= maxRunes) return collapsed;
  if (maxRunes <= 1) return runes.slice(0, maxRunes).join("");
  return `${runes.slice(0, maxRunes - 1).join("")}…`;
}

function isPlaceholderSessionTitle(s: string): boolean {
  const low = s.trim().toLowerCase();
  if (!low || low === "new session" || low === "untitled") {
    return true;
  }
  return low.startsWith("new session - ");
}

function normalizeMuseTimestampMs(value: unknown): number {
  if (typeof value === "number" && Number.isFinite(value) && value > 0) {
    if (value > 1e14) return Math.trunc(value / 1000);
    return normalizeEpochMs(value);
  }
  if (typeof value === "string" && value.trim()) {
    const num = Number(value);
    if (Number.isFinite(num) && num > 0) {
      if (num > 1e14) return Math.trunc(num / 1000);
      return normalizeEpochMs(num);
    }
    return normalizeEpochMs(value);
  }
  return 0;
}

function sqliteTableColumns(db: Database, tableName: string): Set<string> {
  const cols = new Set<string>();
  try {
    const rows = db
      .query<{ name: string }, []>(`PRAGMA table_info(${tableName})`)
      .all();
    for (const row of rows) {
      if (row.name) cols.add(row.name.trim().toLowerCase());
    }
  } catch {
    // Ignore PRAGMA errors.
  }
  return cols;
}

function loadMuseSqliteConversations(
  dbPath: string,
  opts: ConversationListOptions,
): ConversationRecord[] {
  const result = withReadOnlySqlite(dbPath, (db) => {
    if (!sqliteTableExists(db, "sessions")) return [];

    const cols = sqliteTableColumns(db, "sessions");
    const colExpr = (name: string): string =>
      cols.has(name.toLowerCase()) ? name : "NULL";

    const query = `SELECT ${colExpr("session_id")} AS session_id,
      ${colExpr("workspace_root")} AS workspace_root,
      ${colExpr("title")} AS title,
      ${colExpr("effective_long_title")} AS effective_long_title,
      ${colExpr("first_user_prompt")} AS first_user_prompt,
      ${colExpr("model_id")} AS model_id,
      ${colExpr("created_at_us")} AS created_at_us,
      ${colExpr("updated_at_us")} AS updated_at_us,
      ${colExpr("prompt_count")} AS prompt_count,
      ${colExpr("session_log_path")} AS session_log_path,
      ${colExpr("msp_parent_session_id")} AS msp_parent_session_id
      FROM sessions`;

    const rows = db
      .query<
        {
          session_id: string | null;
          workspace_root: string | null;
          title: string | null;
          effective_long_title: string | null;
          first_user_prompt: string | null;
          model_id: string | null;
          created_at_us: unknown;
          updated_at_us: unknown;
          prompt_count: number | null;
          session_log_path: string | null;
          msp_parent_session_id: string | null;
        },
        []
      >(query)
      .all();

    const records: ConversationRecord[] = [];
    for (const row of rows) {
      if ((row.msp_parent_session_id ?? "").trim() !== "") continue;
      const id = (row.session_id ?? "").trim();
      if (!id) continue;

      const firstPrompt = (row.first_user_prompt ?? "").trim();
      const rawTitle =
        (row.title ?? "").trim() ||
        (row.effective_long_title ?? "").trim() ||
        firstPrompt;
      const title = cleanSingleLine(rawTitle, MAX_DERIVED_TITLE_LEN);
      const msgCount =
        typeof row.prompt_count === "number" && row.prompt_count > 0
          ? Math.trunc(row.prompt_count)
          : 0;

      if (isPlaceholderSessionTitle(title) && msgCount === 0 && !firstPrompt) {
        continue;
      }

      let createdAtMs = normalizeMuseTimestampMs(row.created_at_us);
      let updatedAtMs = normalizeMuseTimestampMs(row.updated_at_us);
      if (updatedAtMs === 0) updatedAtMs = createdAtMs;
      if (createdAtMs === 0) createdAtMs = updatedAtMs;

      const cwd = (row.workspace_root ?? "").trim();
      const model = (row.model_id ?? "").trim();
      const sourcePath = (row.session_log_path ?? "").trim() || `${dbPath}#${id}`;

      records.push({
        id,
        agent: "muse",
        app: "muse",
        ...(opts.profile ? { profile: opts.profile } : {}),
        ...(cwd ? { cwd } : {}),
        ...(title ? { title } : {}),
        ...(firstPrompt ? { firstMessage: firstPrompt, lastMessage: firstPrompt } : {}),
        ...(model ? { model } : {}),
        sourcePath,
        resumeTarget: id,
        createdAtMs,
        updatedAtMs,
        messageCount: msgCount,
      });
    }

    return records;
  });

  return result ?? [];
}

function parseMuseSqliteLog(dbPath: string, sessionId: string): ParsedLogMessage[] {
  if (!sessionId) return [];
  const result = withReadOnlySqlite(dbPath, (db) => {
    if (!sqliteTableExists(db, "sessions")) return [];
    const cols = sqliteTableColumns(db, "sessions");
    const colExpr = (name: string): string =>
      cols.has(name.toLowerCase()) ? name : "NULL";

    const row = db
      .query<
        {
          first_user_prompt: string | null;
          model_id: string | null;
          created_at_us: unknown;
          session_log_path: string | null;
        },
        [string]
      >(
        `SELECT ${colExpr("first_user_prompt")} AS first_user_prompt,
                ${colExpr("model_id")} AS model_id,
                ${colExpr("created_at_us")} AS created_at_us,
                ${colExpr("session_log_path")} AS session_log_path
         FROM sessions
         WHERE session_id = ?`,
      )
      .get(sessionId);

    if (!row) return [];
    const logPath = (row.session_log_path ?? "").trim();
    if (logPath && existsSync(logPath)) {
      const fromFile = parseJsonOrJsonlLog(logPath);
      if (fromFile.length > 0) return fromFile;
    }

    const prompt = (row.first_user_prompt ?? "").trim();
    if (!prompt) return [];
    const ts = normalizeMuseTimestampMs(row.created_at_us);
    const model = (row.model_id ?? "").trim();
    return [
      {
        role: "user" as const,
        text: prompt,
        ...(ts > 0 ? { timestampMs: ts } : {}),
        ...(model ? { model } : {}),
      },
    ];
  });
  return result ?? [];
}

export class MusePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = {
    id: "muse",
    aliases: ["muse-code", "muse-cli"],
    displayApp: "Muse",
    cliDisplayApp: "muse",
    binary: "muse",
    screenManifestId: "muse",
    oscAuthority: true,
    resumeSubcommand: "resume",
    positionalResume: true,
    uniqueExplicitSessions: false,
    titlePrefixes: ["Muse - ", "Muse: "],
    mcpTargets: [".config/muse/settings.json"],
  };

  private readonly opts: MusePluginOptions;

  constructor(opts: MusePluginOptions = {}) {
    this.opts = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return [
      path.join(homeDir, MUSE_DB_REL),
      ...MUSE_JSON_SPEC.relDirs.map((rel) => path.join(homeDir, rel)),
    ];
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    const dbPath = path.join(opts.homeDir, MUSE_DB_REL);
    const sqliteRecords = loadMuseSqliteConversations(dbPath, opts);
    const jsonRecords = listJsonStoreConversations(MUSE_JSON_SPEC, opts);

    if (sqliteRecords.length === 0) return jsonRecords;
    if (jsonRecords.length === 0) return sqliteRecords;

    const seen = new Set(sqliteRecords.map((r) => r.id));
    const merged = [...sqliteRecords];
    for (const rec of jsonRecords) {
      if (!seen.has(rec.id)) {
        seen.add(rec.id);
        merged.push(rec);
      }
    }
    return merged;
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    const hashIdx = sourcePath.indexOf("#");
    if (hashIdx >= 0) {
      const dbPath = sourcePath.slice(0, hashIdx);
      const sessionId = sourcePath.slice(hashIdx + 1).trim();
      return parseMuseSqliteLog(dbPath, sessionId);
    }
    return parseJsonOrJsonlLog(sourcePath);
  }

  resolveVendorSession: VendorSessionLookup = (
    _agent,
    cwd,
    profile,
    title,
    exclude,
  ) => {
    const baseHome = realHomeDir(this.opts.homeDir ?? os.homedir());
    let homeDir = baseHome;
    if (profile && profile !== "default") {
      const cfg = loadProfileConfig(this.spec.id, profile, {
        homeDir: baseHome,
        spacesDir: this.opts.spacesDir,
      });
      if (cfg?.shadowHomePath) {
        homeDir = cfg.shadowHomePath;
      } else if (cfg?.homePath) {
        homeDir = cfg.homePath;
      }
    }

    const available = this.storePaths(homeDir).some((p) => existsSync(p));
    if (!available) {
      return { sessionId: "", storeAvailable: false };
    }

    const records = this.listConversations({
      homeDir,
      profile: profile || undefined,
      includeArchived: false,
    });
    const normCwd = cwd ? path.resolve(cwd) : "";
    const normTitle = title.trim().toLowerCase();

    const matching = records
      .filter((r) => !exclude.has(r.id))
      .filter((r) => !normCwd || !r.cwd || path.resolve(r.cwd) === normCwd)
      .sort((a, b) => b.updatedAtMs - a.updatedAtMs);

    if (normTitle) {
      const exact = matching.find(
        (r) =>
          (r.title && r.title.trim().toLowerCase() === normTitle) ||
          (r.firstMessage && r.firstMessage.trim().toLowerCase() === normTitle),
      );
      if (exact) return { sessionId: exact.id, storeAvailable: true };
    }

    return {
      sessionId: matching[0]?.id ?? "",
      storeAvailable: true,
    };
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(
      this.spec,
      req,
      {
        defaultBinary: "~/.local/bin/muse",
        shadowVar: "MUSE_AUTH_PATH",
        shadowSubpath: "auth.json",
        routerEnv: (router) => ({
          META_API_KEY: router.apiKey,
        }),
      },
      this.opts,
    );
  }

  startHeadless: (req: HeadlessStartRequest) => HeadlessSession =
    unsupportedHeadless("muse");

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

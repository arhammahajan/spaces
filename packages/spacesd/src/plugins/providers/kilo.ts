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

const KILO_DB_REL = path.join(".local", "share", "kilo", "kilo.db");
const KILO_JSON_SPEC: JsonStoreScanSpec = {
  agent: "kilo",
  app: "kilo",
  relDirs: [
    path.join(".local", "share", "kilo", "storage", "session"),
    path.join(".config", "kilo", "sessions"),
  ],
};

const BOILERPLATE_USER_PREFIXES = [
  "<local-command-caveat>",
  "<command-name>",
  "<command-message>",
  "<system-reminder>",
  "<ide_opened_file>",
  "<ide_selection>",
  "[request interrupted",
] as const;

export interface KiloPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

interface SqliteSessionSummary {
  firstUser: string;
  lastText: string;
  msgCount: number;
  model: string;
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

function isBoilerplateUserMessage(text: string): boolean {
  const low = text.trim().toLowerCase();
  return BOILERPLATE_USER_PREFIXES.some((prefix) => low.startsWith(prefix));
}

function extractTextFromContent(content: unknown): string {
  if (typeof content === "string") return content.trim();
  if (Array.isArray(content)) {
    const parts: string[] = [];
    for (const item of content) {
      if (typeof item === "string") {
        if (item.trim()) parts.push(item.trim());
      } else if (item && typeof item === "object") {
        const rec = item as Record<string, unknown>;
        if (typeof rec.text === "string" && rec.text.trim()) {
          parts.push(rec.text.trim());
        } else if (typeof rec.content === "string" && rec.content.trim()) {
          parts.push(rec.content.trim());
        }
      }
    }
    return parts.join("\n").trim();
  }
  return "";
}

function extractSqliteMessageText(raw: string | null | undefined): string {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) return "";
  try {
    const doc = JSON.parse(trimmed) as unknown;
    if (!doc || typeof doc !== "object" || Array.isArray(doc)) return "";
    const rec = doc as Record<string, unknown>;
    if (typeof rec.text === "string" && rec.text.trim()) {
      return rec.text.trim();
    }
    const content = rec.content ?? rec.parts;
    if (content !== undefined) {
      return extractTextFromContent(content);
    }
  } catch {
    // Ignore malformed JSON.
  }
  return "";
}

function extractSqliteRoleAndModel(raw: string | null | undefined): {
  role: ParsedLogMessage["role"];
  model: string;
} {
  let role: ParsedLogMessage["role"] = "assistant";
  let model = "";
  const trimmed = (raw ?? "").trim();
  if (!trimmed) return { role, model };
  try {
    const doc = JSON.parse(trimmed) as unknown;
    if (!doc || typeof doc !== "object" || Array.isArray(doc)) {
      return { role, model };
    }
    const rec = doc as Record<string, unknown>;
    if (typeof rec.role === "string") {
      const r = rec.role.trim().toLowerCase();
      if (r === "user" || r === "human") role = "user";
      else if (r === "system") role = "system";
      else if (r === "tool") role = "tool";
      else role = "assistant";
    }
    if (rec.model && typeof rec.model === "object" && !Array.isArray(rec.model)) {
      const m = rec.model as Record<string, unknown>;
      model = String(m.modelID ?? m.id ?? m.model_id ?? m.name ?? "").trim();
    } else if (typeof rec.model === "string") {
      model = rec.model.trim();
    }
  } catch {
    // Ignore malformed JSON.
  }
  return { role, model };
}

function parseSqliteModel(raw: unknown): string {
  if (typeof raw !== "string") return "";
  const s = raw.trim();
  if (!s) return "";
  if (s.startsWith("{")) {
    try {
      const obj = JSON.parse(s) as Record<string, unknown>;
      const val = obj.id ?? obj.modelID ?? obj.model_id ?? obj.name;
      return typeof val === "string" ? val.trim() : "";
    } catch {
      return s;
    }
  }
  return s;
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

function loadSqliteSessionSummaries(db: Database): Map<string, SqliteSessionSummary> {
  const out = new Map<string, SqliteSessionSummary>();

  if (sqliteTableExists(db, "message") && sqliteTableExists(db, "part")) {
    try {
      const rows = db
        .query<
          { session_id: string | null; msg_data: string | null; part_data: string | null },
          []
        >(
          `SELECT m.session_id AS session_id, m.data AS msg_data, p.data AS part_data
           FROM message m
           LEFT JOIN part p ON p.message_id = m.id
           ORDER BY m.session_id ASC, m.time_created ASC, p.time_created ASC`,
        )
        .all();
      for (const row of rows) {
        const sid = (row.session_id ?? "").trim();
        if (!sid) continue;
        const { role, model } = extractSqliteRoleAndModel(row.msg_data);
        const text =
          extractSqliteMessageText(row.part_data) ||
          extractSqliteMessageText(row.msg_data);
        const sum = out.get(sid) ?? {
          firstUser: "",
          lastText: "",
          msgCount: 0,
          model: "",
        };
        if (!sum.model && model) sum.model = model;
        if (text) {
          sum.msgCount += 1;
          if (role === "user" && !sum.firstUser && !isBoilerplateUserMessage(text)) {
            sum.firstUser = text;
          }
          sum.lastText = text;
        }
        out.set(sid, sum);
      }
      if (out.size > 0) return out;
    } catch {
      // Fall through to session_message.
    }
  }

  if (sqliteTableExists(db, "session_message")) {
    try {
      const countRows = db
        .query<{ session_id: string | null; cnt: number }, []>(
          `SELECT session_id, COUNT(*) AS cnt
           FROM session_message
           WHERE lower(type) IN ('user', 'assistant')
           GROUP BY session_id`,
        )
        .all();
      for (const row of countRows) {
        const sid = (row.session_id ?? "").trim();
        if (!sid) continue;
        const sum = out.get(sid) ?? {
          firstUser: "",
          lastText: "",
          msgCount: 0,
          model: "",
        };
        sum.msgCount = row.cnt ?? 0;
        out.set(sid, sum);
      }
    } catch {
      // Ignore count failure.
    }

    try {
      const msgRows = db
        .query<{ session_id: string | null; data: string | null }, []>(
          `SELECT session_id, data
           FROM session_message
           WHERE lower(type) = 'user'
           ORDER BY session_id ASC, seq ASC, time_created ASC`,
        )
        .all();
      for (const row of msgRows) {
        const sid = (row.session_id ?? "").trim();
        if (!sid) continue;
        const text = extractSqliteMessageText(row.data);
        if (!text) continue;
        const sum = out.get(sid) ?? {
          firstUser: "",
          lastText: "",
          msgCount: 0,
          model: "",
        };
        if (sum.msgCount === 0) sum.msgCount = 1;
        if (!sum.firstUser && !isBoilerplateUserMessage(text)) {
          sum.firstUser = text;
        }
        sum.lastText = text;
        out.set(sid, sum);
      }
    } catch {
      // Ignore message read failure.
    }
  }

  return out;
}

function loadKiloSqliteConversations(
  dbPath: string,
  opts: ConversationListOptions,
): ConversationRecord[] {
  const result = withReadOnlySqlite(dbPath, (db) => {
    if (!sqliteTableExists(db, "session")) return [];

    const summaries = loadSqliteSessionSummaries(db);
    const cols = sqliteTableColumns(db, "session");
    const colExpr = (name: string): string =>
      cols.has(name.toLowerCase()) ? name : "NULL";

    const query = `SELECT ${colExpr("id")} AS id,
      ${colExpr("parent_id")} AS parent_id,
      ${colExpr("directory")} AS directory,
      ${colExpr("title")} AS title,
      ${colExpr("model")} AS model,
      ${colExpr("time_created")} AS time_created,
      ${colExpr("time_updated")} AS time_updated,
      ${colExpr("time_archived")} AS time_archived
      FROM session`;

    const rows = db
      .query<
        {
          id: string | null;
          parent_id: string | null;
          directory: string | null;
          title: string | null;
          model: string | null;
          time_created: unknown;
          time_updated: unknown;
          time_archived: unknown;
        },
        []
      >(query)
      .all();

    const records: ConversationRecord[] = [];
    for (const row of rows) {
      if ((row.parent_id ?? "").trim() !== "") continue;
      const id = (row.id ?? "").trim();
      if (!id) continue;

      const sum = summaries.get(id) ?? {
        firstUser: "",
        lastText: "",
        msgCount: 0,
        model: "",
      };

      let title = cleanSingleLine(row.title ?? "", MAX_DERIVED_TITLE_LEN);
      if (isPlaceholderSessionTitle(title)) {
        if (sum.firstUser) {
          title = cleanSingleLine(sum.firstUser, MAX_DERIVED_TITLE_LEN);
        } else if (sum.lastText) {
          title = cleanSingleLine(sum.lastText, MAX_DERIVED_TITLE_LEN);
        } else if (sum.msgCount === 0) {
          continue;
        } else {
          title = "";
        }
      }

      const model = parseSqliteModel(row.model) || sum.model;
      let createdAtMs = normalizeEpochMs(row.time_created);
      let updatedAtMs = normalizeEpochMs(row.time_updated);
      const archivedMs = normalizeEpochMs(row.time_archived);
      if (updatedAtMs === 0) updatedAtMs = createdAtMs;
      if (createdAtMs === 0) createdAtMs = updatedAtMs;

      const archived = archivedMs > 0;
      if (opts.includeArchived === false && archived) {
        continue;
      }

      const cwd = (row.directory ?? "").trim();
      records.push({
        id,
        agent: "kilo",
        app: "kilo",
        ...(opts.profile ? { profile: opts.profile } : {}),
        ...(cwd ? { cwd } : {}),
        ...(title ? { title } : {}),
        ...(sum.firstUser ? { firstMessage: sum.firstUser } : {}),
        ...(sum.lastText ? { lastMessage: sum.lastText } : {}),
        ...(model ? { model } : {}),
        sourcePath: `${dbPath}#${id}`,
        resumeTarget: id,
        createdAtMs,
        updatedAtMs,
        messageCount: sum.msgCount,
        archived,
      });
    }

    return records;
  });

  return result ?? [];
}

function parseKiloSqliteLog(dbPath: string, sessionId: string): ParsedLogMessage[] {
  if (!sessionId) return [];
  const result = withReadOnlySqlite(dbPath, (db) => {
    if (sqliteTableExists(db, "message") && sqliteTableExists(db, "part")) {
      try {
        const rows = db
          .query<
            { time_created: unknown; msg_data: string | null; part_data: string | null },
            [string]
          >(
            `SELECT m.time_created AS time_created, m.data AS msg_data, p.data AS part_data
             FROM message m
             LEFT JOIN part p ON p.message_id = m.id
             WHERE m.session_id = ?
             ORDER BY m.time_created ASC, p.time_created ASC`,
          )
          .all(sessionId);
        const entries: ParsedLogMessage[] = [];
        for (const row of rows) {
          const { role, model } = extractSqliteRoleAndModel(row.msg_data);
          const text =
            extractSqliteMessageText(row.part_data) ||
            extractSqliteMessageText(row.msg_data);
          if (!text) continue;
          const ts = normalizeEpochMs(row.time_created);
          entries.push({
            role,
            text,
            ...(ts > 0 ? { timestampMs: ts } : {}),
            ...(model ? { model } : {}),
          });
        }
        if (entries.length > 0) return entries;
      } catch {
        // Fall through to session_message.
      }
    }

    if (sqliteTableExists(db, "session_message")) {
      try {
        const rows = db
          .query<{ type: string | null; time_created: unknown; data: string | null }, [string]>(
            `SELECT type, time_created, data
             FROM session_message
             WHERE session_id = ?
             ORDER BY seq ASC, time_created ASC`,
          )
          .all(sessionId);
        const entries: ParsedLogMessage[] = [];
        for (const row of rows) {
          const text = extractSqliteMessageText(row.data);
          if (!text) continue;
          const role: ParsedLogMessage["role"] =
            (row.type ?? "").trim().toLowerCase() === "user" ? "user" : "assistant";
          const ts = normalizeEpochMs(row.time_created);
          entries.push({
            role,
            text,
            ...(ts > 0 ? { timestampMs: ts } : {}),
          });
        }
        return entries;
      } catch {
        return [];
      }
    }

    return [];
  });
  return result ?? [];
}

export class KiloPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = {
    id: "kilo",
    aliases: ["kilo-code", "kilo code", "herdr:kilo"],
    displayApp: "Kilo",
    cliDisplayApp: "kilo",
    binary: "kilo",
    screenManifestId: "kilo",
    oscAuthority: false,
    resumeFlag: "--session",
    positionalResume: false,
    uniqueExplicitSessions: false,
    titlePrefixes: ["Kilo - ", "Kilo: ", "Kilo Code - "],
    mcpTargets: [".config/kilo/kilo.json"],
  };

  private readonly opts: KiloPluginOptions;

  constructor(opts: KiloPluginOptions = {}) {
    this.opts = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return [
      path.join(homeDir, KILO_DB_REL),
      ...KILO_JSON_SPEC.relDirs.map((rel) => path.join(homeDir, rel)),
    ];
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    const dbPath = path.join(opts.homeDir, KILO_DB_REL);
    const sqliteRecords = loadKiloSqliteConversations(dbPath, opts);
    const jsonRecords = listJsonStoreConversations(KILO_JSON_SPEC, opts);

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
      return parseKiloSqliteLog(dbPath, sessionId);
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
        defaultBinary: "~/.kilo/bin/kilo",
        shadowVar: "XDG_DATA_HOME",
      },
      this.opts,
    );
  }

  startHeadless: (req: HeadlessStartRequest) => HeadlessSession =
    unsupportedHeadless("kilo");

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

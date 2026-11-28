import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import * as path from "node:path";
import type { VendorSessionLookup } from "../../resurrect/capture";
import type {
  ConversationListOptions,
  ConversationRecord,
  ParsedLogMessage,
} from "../types";

export interface JsonStoreScanSpec {
  agent: string;
  app?: string;
  /** Relative directories under `homeDir` to scan recursively for `.json` / `.jsonl` files. */
  relDirs: string[];
  /** Optional relative path to `session_index.jsonl` (e.g. `.kimi-code/session_index.jsonl`). */
  indexRelPath?: string;
  /** Optional relative path to `kimi.json` mapping `work_dirs[].path` via MD5 hash. */
  kimiJsonRelPath?: string;
  /** Optional filename prefix filter (e.g. `"rollout-"`). */
  filePrefix?: string;
}

function walkSessionFiles(dir: string, out: string[], maxDepth = 4, depth = 0): void {
  if (depth > maxDepth || !existsSync(dir)) return;
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const ent of entries) {
    if (ent.name.startsWith(".")) continue;
    const full = path.join(dir, ent.name);
    if (ent.isDirectory()) {
      walkSessionFiles(full, out, maxDepth, depth + 1);
    } else if (ent.isFile() && (ent.name.endsWith(".jsonl") || ent.name.endsWith(".json"))) {
      out.push(full);
    }
  }
}

function extractTextFromContent(content: unknown): string {
  if (typeof content === "string") return content.trim();
  if (Array.isArray(content)) {
    const parts: string[] = [];
    for (const block of content) {
      if (typeof block === "string") {
        parts.push(block);
      } else if (block && typeof block === "object") {
        const rec = block as Record<string, unknown>;
        if (typeof rec.text === "string") parts.push(rec.text);
        else if (typeof rec.content === "string") parts.push(rec.content);
      }
    }
    return parts.join("\n").trim();
  }
  return "";
}

function parseTimestampMs(raw: unknown): number | undefined {
  if (typeof raw === "number" && Number.isFinite(raw)) {
    return raw > 1e12 ? Math.trunc(raw) : Math.trunc(raw * 1000);
  }
  if (typeof raw === "string" && raw.trim()) {
    const parsed = Date.parse(raw);
    if (!Number.isNaN(parsed)) return parsed;
  }
  return undefined;
}

function normalizeRole(raw: unknown): ParsedLogMessage["role"] | null {
  if (typeof raw !== "string") return null;
  const r = raw.trim().toLowerCase();
  if (r === "user" || r === "human") return "user";
  if (r === "assistant" || r === "model" || r === "ai") return "assistant";
  if (r === "system") return "system";
  if (r === "tool") return "tool";
  return null;
}

export function parseJsonOrJsonlLog(sourcePath: string): ParsedLogMessage[] {
  if (!existsSync(sourcePath)) return [];
  let raw: string;
  try {
    raw = readFileSync(sourcePath, "utf8");
  } catch {
    return [];
  }

  const messages: ParsedLogMessage[] = [];
  const pushObj = (obj: Record<string, unknown>): void => {
    const msgObj =
      obj.message && typeof obj.message === "object" && !Array.isArray(obj.message)
        ? (obj.message as Record<string, unknown>)
        : obj;
    const role = normalizeRole(msgObj.role ?? obj.role ?? obj.type);
    if (!role) return;
    const text = extractTextFromContent(msgObj.content ?? obj.content ?? obj.text);
    if (!text) return;
    const ts = parseTimestampMs(obj.timestamp ?? msgObj.timestamp ?? obj.created_at ?? obj.ts);
    const model =
      typeof msgObj.model === "string"
        ? msgObj.model
        : typeof obj.model === "string"
          ? obj.model
          : undefined;
    messages.push({
      role,
      text,
      ...(ts !== undefined ? { timestampMs: ts } : {}),
      ...(model ? { model } : {}),
    });
  };

  if (sourcePath.endsWith(".jsonl")) {
    for (const line of raw.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed) as unknown;
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          pushObj(parsed as Record<string, unknown>);
        }
      } catch {
        // Skip malformed line.
      }
    }
    return messages;
  }

  try {
    const parsed = JSON.parse(raw) as unknown;
    if (Array.isArray(parsed)) {
      for (const item of parsed) {
        if (item && typeof item === "object" && !Array.isArray(item)) {
          pushObj(item as Record<string, unknown>);
        }
      }
    } else if (parsed && typeof parsed === "object") {
      const rec = parsed as Record<string, unknown>;
      const list = Array.isArray(rec.messages)
        ? rec.messages
        : Array.isArray(rec.history)
          ? rec.history
          : Array.isArray(rec.turns)
            ? rec.turns
            : null;
      if (list) {
        for (const item of list) {
          if (item && typeof item === "object" && !Array.isArray(item)) {
            pushObj(item as Record<string, unknown>);
          }
        }
      }
    }
  } catch {
    // Ignore malformed JSON.
  }
  return messages;
}

interface IndexEntry {
  cwd?: string;
  title?: string;
  deleted?: boolean;
}

function loadSessionIndex(indexPath: string): Map<string, IndexEntry> {
  const map = new Map<string, IndexEntry>();
  if (!existsSync(indexPath)) return map;
  let raw: string;
  try {
    raw = readFileSync(indexPath, "utf8");
  } catch {
    return map;
  }
  for (const line of raw.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    try {
      const rec = JSON.parse(trimmed) as Record<string, unknown>;
      const id = String(rec.sessionId ?? rec.session_id ?? rec.id ?? "").trim();
      if (!id) continue;
      const existing = map.get(id) ?? {};
      const cwd = typeof rec.workDir === "string"
        ? rec.workDir
        : typeof rec.cwd === "string"
          ? rec.cwd
          : existing.cwd;
      const title = typeof rec.title === "string"
        ? rec.title
        : typeof rec.thread_name === "string"
          ? rec.thread_name
          : existing.title;
      const deleted = rec.deleted === true ? true : existing.deleted;
      map.set(id, { cwd, title, deleted });
    } catch {
      // Skip malformed line.
    }
  }
  return map;
}

function loadKimiWorkDirMap(kimiJsonPath: string): Map<string, string> {
  const map = new Map<string, string>();
  if (!existsSync(kimiJsonPath)) return map;
  try {
    const parsed = JSON.parse(readFileSync(kimiJsonPath, "utf8")) as {
      work_dirs?: Array<{ path?: string }>;
    };
    for (const wd of parsed.work_dirs ?? []) {
      if (typeof wd?.path === "string" && wd.path.trim()) {
        const hash = createHash("md5").update(wd.path).digest("hex");
        map.set(hash, wd.path);
      }
    }
  } catch {
    // Ignore malformed kimi.json.
  }
  return map;
}

export function listJsonStoreConversations(
  spec: JsonStoreScanSpec,
  opts: ConversationListOptions,
): ConversationRecord[] {
  const indexMap = spec.indexRelPath
    ? loadSessionIndex(path.join(opts.homeDir, spec.indexRelPath))
    : new Map<string, IndexEntry>();
  const kimiHashMap = spec.kimiJsonRelPath
    ? loadKimiWorkDirMap(path.join(opts.homeDir, spec.kimiJsonRelPath))
    : new Map<string, string>();

  const files: string[] = [];
  for (const rel of spec.relDirs) {
    walkSessionFiles(path.join(opts.homeDir, rel), files);
  }

  const records: ConversationRecord[] = [];
  for (const filePath of files) {
    const base = path.basename(filePath);
    if (base === "session_index.jsonl" || base === "kimi.json") continue;
    if (spec.filePrefix && !base.startsWith(spec.filePrefix)) continue;

    let st;
    try {
      st = statSync(filePath);
    } catch {
      continue;
    }

    const stem = base.replace(/\.(jsonl|json)$/u, "");
    let id = stem;
    let title: string | undefined;
    let cwd: string | undefined;
    let model: string | undefined;
    let createdAtMs = Math.trunc(st.birthtimeMs || st.mtimeMs);
    let updatedAtMs = Math.trunc(st.mtimeMs);

    // Infer CWD from Kimi MD5 parent directory name if present.
    const parentName = path.basename(path.dirname(filePath));
    if (kimiHashMap.has(parentName)) {
      cwd = kimiHashMap.get(parentName);
    }

    // Parse top-level metadata from JSON objects if present.
    if (filePath.endsWith(".json")) {
      try {
        const parsed = JSON.parse(readFileSync(filePath, "utf8")) as Record<string, unknown>;
        if (typeof parsed.id === "string" && parsed.id.trim()) id = parsed.id.trim();
        else if (typeof parsed.session_id === "string" && parsed.session_id.trim()) id = parsed.session_id.trim();
        if (typeof parsed.title === "string" && parsed.title.trim()) title = parsed.title.trim();
        if (typeof parsed.cwd === "string" && parsed.cwd.trim()) cwd = parsed.cwd.trim();
        if (typeof parsed.model === "string" && parsed.model.trim()) model = parsed.model.trim();
        const cMs = parseTimestampMs(parsed.created_at ?? parsed.createdAt);
        const uMs = parseTimestampMs(parsed.updated_at ?? parsed.updatedAt);
        if (cMs !== undefined) createdAtMs = cMs;
        if (uMs !== undefined) updatedAtMs = uMs;
      } catch {
        // Fallback to parsed messages below.
      }
    }

    const idx = indexMap.get(id) ?? indexMap.get(stem);
    if (idx?.deleted) continue;
    if (idx?.cwd && !cwd) cwd = idx.cwd;
    if (idx?.title && !title) title = idx.title;

    const msgs = parseJsonOrJsonlLog(filePath);
    const firstUser = msgs.find((m) => m.role === "user")?.text;
    const lastMsg = msgs.length > 0 ? msgs[msgs.length - 1]!.text : undefined;
    if (!model) {
      model = [...msgs].reverse().find((m) => m.model)?.model;
    }

    records.push({
      id,
      agent: spec.agent,
      app: spec.app ?? spec.agent,
      ...(opts.profile ? { profile: opts.profile } : {}),
      ...(cwd ? { cwd } : {}),
      ...(title ? { title } : {}),
      ...(firstUser ? { firstMessage: firstUser } : {}),
      ...(lastMsg ? { lastMessage: lastMsg } : {}),
      ...(model ? { model } : {}),
      sourcePath: filePath,
      resumeTarget: id,
      createdAtMs,
      updatedAtMs,
      messageCount: msgs.length,
    });
  }

  return records;
}

export function resolveJsonStoreVendorSession(
  spec: JsonStoreScanSpec,
  homeDir: string,
  cwd: string,
  profile: string,
  title: string,
  exclude: ReadonlySet<string>,
): ReturnType<VendorSessionLookup> {
  const hasDir = spec.relDirs.some((rel) => existsSync(path.join(homeDir, rel)));
  if (!hasDir) {
    return { sessionId: "", storeAvailable: false };
  }

  const records = listJsonStoreConversations(spec, {
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
}

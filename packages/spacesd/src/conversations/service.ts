import { randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  renameSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type {
  ConversationEntry,
  ConversationSearchHit,
  ConversationSearchRequest,
  ConversationSnapshot,
  SnippetSpan,
} from "@spaces/types";
import { defaultSpacesDir, loadProfileConfig, realHomeDir } from "../plugins/profiles";
import type { HarnessPluginRegistry } from "../plugins/registry";
import type { ConversationRecord, HarnessPlugin, ParsedLogMessage } from "../plugins/types";
import {
  canonicalConversationAgent,
  type ConversationOverlayRecord,
  type SpacesStore,
} from "../store/db";

const DEFAULT_MAX_WORKERS = 8;
const MAX_CONV_TITLE_RUNES = 120;
const MAX_CONV_PREVIEW_RUNES = 200;
const MAX_CONV_SNIPPET_RUNES = 140;
const CURRENT_CONV_CACHE_VERSION = 3;

export interface ConversationServiceOptions {
  registry: HarnessPluginRegistry;
  store?: SpacesStore;
  homeDir?: string;
  spacesDir?: string;
  cachePath?: string;
  maxWorkers?: number;
  now?: () => number;
}

export interface ConversationListParams {
  refresh?: boolean;
  includeArchived?: boolean;
}

interface ProfileRoot {
  agent: string;
  profile: string;
  homeDir: string;
}

interface ScanJob {
  plugin: HarnessPlugin;
  homeDir: string;
  resolvedHomeDir: string;
  profile: string;
}

interface CachedConvSnapshot {
  version: number;
  synced_ms: number;
  indexed_messages: number;
  conversations: ConversationEntry[];
}

export function effectiveTimeMs(entry: ConversationEntry): number {
  const latest = Math.max(entry.updated_at_ms || 0, entry.last_opened_ms || 0);
  return latest > 0 ? latest : entry.created_at_ms || 0;
}

function collapseWhitespace(s: string): string {
  return s.trim().replace(/\s+/g, " ");
}

function truncateSingleLine(s: string, maxRunes: number): string {
  const norm = collapseWhitespace(s);
  if (!norm || maxRunes <= 0) return norm;
  const runes = Array.from(norm);
  if (runes.length <= maxRunes) return norm;
  if (maxRunes <= 1) return runes.slice(0, maxRunes).join("");
  return `${runes.slice(0, maxRunes - 1).join("")}…`;
}

function resolveRealPath(targetPath: string): string {
  const clean = targetPath.trim();
  if (!clean) return "";
  try {
    return path.resolve(realpathSync(clean));
  } catch {
    return path.resolve(clean);
  }
}

function isPathWithin(targetPath: string, dir: string): boolean {
  if (!targetPath || !dir) return false;
  const rel = path.relative(dir, targetPath);
  return (
    rel === "" ||
    (rel !== ".." && !rel.startsWith(`..${path.sep}`) && !path.isAbsolute(rel))
  );
}

/**
 * Reports whether `targetPath` (which may carry a `#id` SQLite fragment suffix)
 * resolves via symlinks into `resolvedRealHome` and outside `resolvedProfileHome`.
 */
export function pathPointsIntoRealHome(
  targetPath: string,
  resolvedProfileHome: string,
  resolvedRealHome: string,
): boolean {
  if (!resolvedRealHome || !resolvedProfileHome) return false;
  let clean = targetPath.trim();
  if (!clean) return false;
  const hashIdx = clean.indexOf("#");
  if (hashIdx >= 0) {
    clean = clean.slice(0, hashIdx);
  }
  if (!clean) return false;
  let resolved: string;
  try {
    resolved = path.resolve(realpathSync(clean));
  } catch {
    return false;
  }
  return (
    isPathWithin(resolved, resolvedRealHome) &&
    !isPathWithin(resolved, resolvedProfileHome)
  );
}

/**
 * Reports whether `profileHome` itself resolves to `resolvedRealHome`, or all
 * existing `plugin.storePaths(profileHome)` resolve via symlinks into
 * `resolvedRealHome` outside `profileHome`.
 */
function profileStorePointsToRealHome(
  plugin: HarnessPlugin,
  profileHome: string,
  resolvedRealHome: string,
): boolean {
  if (!resolvedRealHome || !profileHome.trim()) return false;
  let resolvedProfile: string;
  try {
    resolvedProfile = path.resolve(realpathSync(profileHome));
  } catch {
    return false;
  }
  if (resolvedProfile === resolvedRealHome) {
    return true;
  }
  if (!plugin.storePaths) {
    return false;
  }
  const paths = plugin.storePaths(profileHome);
  if (paths.length === 0) {
    return false;
  }
  let existing = 0;
  for (const p of paths) {
    if (!existsSync(p)) continue;
    existing += 1;
    if (!pathPointsIntoRealHome(p, resolvedProfile, resolvedRealHome)) {
      return false;
    }
  }
  return existing > 0;
}

function overlayKey(canonAgent: string, sessionId: string): string {
  return `${canonAgent}:${sessionId}`;
}

function cloneEntry(entry: ConversationEntry): ConversationEntry {
  return {
    ...entry,
    launch_cmd: entry.launch_cmd ? [...entry.launch_cmd] : [],
  };
}

function applyOverlayToEntry(
  entry: ConversationEntry,
  ov: ConversationOverlayRecord,
): void {
  if (ov.space) {
    entry.space = ov.space;
  }
  if (ov.last_opened_ms > entry.last_opened_ms) {
    entry.last_opened_ms = ov.last_opened_ms;
  }
  if (ov.last_outcome) {
    entry.last_outcome = ov.last_outcome;
  }
  if (!entry.profile.trim() && ov.profile) {
    entry.profile = ov.profile;
  }
  if (!entry.cwd.trim() && ov.cwd) {
    entry.cwd = ov.cwd;
  }
  if (!entry.title.trim() && ov.title) {
    entry.title = ov.title;
  }
  if (ov.launch_cmd && ov.launch_cmd.length > 0) {
    entry.launch_cmd = [...ov.launch_cmd];
  }
}

function mergeConversationEntries(
  a: ConversationEntry,
  b: ConversationEntry,
): ConversationEntry {
  let winner = cloneEntry(a);
  let loser = b;
  if (
    b.updated_at_ms > a.updated_at_ms ||
    (b.updated_at_ms === a.updated_at_ms && b.message_count > a.message_count)
  ) {
    winner = cloneEntry(b);
    loser = a;
  }
  if (!winner.profile) winner.profile = loser.profile;
  if (!winner.title) winner.title = loser.title;
  if (!winner.preview) winner.preview = loser.preview;
  if (!winner.cwd) winner.cwd = loser.cwd;
  if (!winner.branch) winner.branch = loser.branch;
  if (!winner.model) winner.model = loser.model;
  if (!winner.app) winner.app = loser.app;
  if (!winner.source_path) winner.source_path = loser.source_path;
  if (!winner.resume_target) winner.resume_target = loser.resume_target;
  if (winner.launch_cmd.length === 0 && loser.launch_cmd.length > 0) {
    winner.launch_cmd = [...loser.launch_cmd];
  }
  if (
    loser.created_at_ms > 0 &&
    (winner.created_at_ms === 0 || loser.created_at_ms < winner.created_at_ms)
  ) {
    winner.created_at_ms = loser.created_at_ms;
  }
  if (loser.message_count > winner.message_count) {
    winner.message_count = loser.message_count;
  }
  return winner;
}

function sortConversationEntries(entries: ConversationEntry[]): void {
  entries.sort((a, b) => {
    const ea = effectiveTimeMs(a);
    const eb = effectiveTimeMs(b);
    if (ea !== eb) return eb - ea;
    if (a.updated_at_ms !== b.updated_at_ms) {
      return b.updated_at_ms - a.updated_at_ms;
    }
    if (a.created_at_ms !== b.created_at_ms) {
      return b.created_at_ms - a.created_at_ms;
    }
    if (a.agent !== b.agent) return a.agent < b.agent ? -1 : 1;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

function filterArchivedEntries(
  entries: ConversationEntry[],
  includeArchived: boolean,
): ConversationEntry[] {
  if (includeArchived) return entries.map(cloneEntry);
  return entries.filter((e) => !e.archived).map(cloneEntry);
}

function mapRecordToEntry(
  plugin: HarnessPlugin,
  fallbackProfile: string,
  rec: ConversationRecord,
): ConversationEntry {
  const rawAgent = (rec.agent ?? "").trim() || plugin.spec.id;
  const canonAgent = canonicalConversationAgent(rawAgent) || plugin.spec.id;
  const app = plugin.spec.cliDisplayApp || (rec.app ?? "").trim() || canonAgent;
  const profile = (rec.profile ?? "").trim() || fallbackProfile.trim();
  const id = (rec.id ?? "").trim();
  const title =
    (rec.title ?? "").trim() ||
    truncateSingleLine(rec.firstMessage ?? "", MAX_CONV_TITLE_RUNES);
  const preview =
    truncateSingleLine(rec.lastMessage ?? "", MAX_CONV_PREVIEW_RUNES) ||
    truncateSingleLine(rec.firstMessage ?? "", MAX_CONV_PREVIEW_RUNES);

  return {
    agent: canonAgent,
    id,
    app,
    profile,
    title,
    preview,
    cwd: (rec.cwd ?? "").trim(),
    branch: "",
    model: (rec.model ?? "").trim(),
    created_at_ms: rec.createdAtMs || 0,
    updated_at_ms: rec.updatedAtMs || 0,
    message_count: rec.messageCount || 0,
    archived: Boolean(rec.archived),
    source_path: (rec.sourcePath ?? "").trim(),
    resume_target: (rec.resumeTarget ?? "").trim() || id,
    space: "",
    last_outcome: "",
    last_opened_ms: 0,
    launch_cmd: [],
  };
}

async function runBounded<T, R>(
  items: readonly T[],
  maxWorkers: number,
  worker: (item: T, index: number) => Promise<R>,
): Promise<R[]> {
  const results = new Array<R>(items.length);
  if (items.length === 0) return results;

  let nextIdx = 0;
  const concurrency = Math.min(Math.max(1, maxWorkers), items.length);

  const runners = Array.from({ length: concurrency }, async () => {
    while (true) {
      const idx = nextIdx;
      nextIdx += 1;
      if (idx >= items.length) break;
      results[idx] = await worker(items[idx]!, idx);
    }
  });

  await Promise.all(runners);
  return results;
}

// -----------------------------------------------------------------------------
// Full-text search & Unicode code-point (rune) snippet helpers
// -----------------------------------------------------------------------------

function normalizeSearchScope(scope: string | undefined): "all" | "user" | "titles" {
  switch ((scope ?? "").trim().toLowerCase()) {
    case "titles":
      return "titles";
    case "user":
      return "user";
    default:
      return "all";
  }
}

function recencyBonus(tsMs: number): number {
  if (tsMs <= 0) return 0;
  const b = tsMs / 2e12;
  return b >= 0.99 ? 0.99 : b;
}

function toLowerRunes(runes: string[]): string[] {
  return runes.map((r) => r.toLowerCase());
}

function indexRunesFrom(
  haystack: readonly string[],
  needle: readonly string[],
  from: number,
): number {
  const nLen = needle.length;
  if (nLen === 0 || from < 0 || from + nLen > haystack.length) {
    return -1;
  }
  const limit = haystack.length - nLen;
  const first = needle[0];
  for (let i = from; i <= limit; i += 1) {
    if (haystack[i] !== first) continue;
    let matched = true;
    for (let j = 1; j < nLen; j += 1) {
      if (haystack[i + j] !== needle[j]) {
        matched = false;
        break;
      }
    }
    if (matched) return i;
  }
  return -1;
}

function mergeSnippetSpans(spans: SnippetSpan[]): SnippetSpan[] {
  if (spans.length <= 1) return spans;
  const sorted = [...spans].sort((a, b) =>
    a.start !== b.start ? a.start - b.start : a.end - b.end,
  );
  const merged: SnippetSpan[] = [];
  let cur: SnippetSpan = { start: sorted[0]!.start, end: sorted[0]!.end };
  for (let i = 1; i < sorted.length; i += 1) {
    const s = sorted[i]!;
    if (s.start <= cur.end) {
      if (s.end > cur.end) cur.end = s.end;
    } else {
      merged.push(cur);
      cur = { start: s.start, end: s.end };
    }
  }
  merged.push(cur);
  return merged;
}

function buildCenteredSnippet(
  textRunes: string[],
  lowerText: string[],
  needles: string[][],
  matchStart: number,
  matchLen: number,
  maxRunes: number,
): { snippet: string; spans: SnippetSpan[] } {
  const total = textRunes.length;
  const effectiveMax = maxRunes > 0 ? maxRunes : MAX_CONV_SNIPPET_RUNES;

  let winStart = 0;
  let winEnd = total;
  if (total > effectiveMax) {
    const center = matchStart + Math.floor(matchLen / 2);
    winStart = Math.max(0, center - Math.floor(effectiveMax / 2));
    winEnd = winStart + effectiveMax;
    if (winEnd > total) {
      winEnd = total;
      winStart = Math.max(0, total - effectiveMax);
    }
  }

  const hasLeading = winStart > 0;
  const hasTrailing = winEnd < total;
  const offset = hasLeading ? 1 : 0;

  const windowRunes = textRunes.slice(winStart, winEnd);
  const windowLower = lowerText.slice(winStart, winEnd);

  const snippet =
    (hasLeading ? "…" : "") +
    windowRunes.join("") +
    (hasTrailing ? "…" : "");

  const spans: SnippetSpan[] = [];
  for (const needle of needles) {
    if (needle.length === 0) continue;
    let from = 0;
    while (true) {
      const pos = indexRunesFrom(windowLower, needle, from);
      if (pos < 0) break;
      spans.push({
        start: offset + pos,
        end: offset + pos + needle.length,
      });
      from = pos + needle.length;
    }
  }

  return {
    snippet,
    spans: mergeSnippetSpans(spans),
  };
}

export function matchAndBuildSnippet(
  text: string,
  query: string,
  maxRunes = MAX_CONV_SNIPPET_RUNES,
): { matched: boolean; exact: boolean; snippet: string; spans: SnippetSpan[] } {
  const normText = collapseWhitespace(text);
  const normQuery = collapseWhitespace(query);
  if (!normText || !normQuery) {
    return { matched: false, exact: false, snippet: "", spans: [] };
  }

  const textRunes = Array.from(normText);
  const lowerText = toLowerRunes(textRunes);
  const queryRunes = toLowerRunes(Array.from(normQuery));

  const exactPos = indexRunesFrom(lowerText, queryRunes, 0);
  if (exactPos >= 0) {
    const { snippet, spans } = buildCenteredSnippet(
      textRunes,
      lowerText,
      [queryRunes],
      exactPos,
      queryRunes.length,
      maxRunes,
    );
    return { matched: true, exact: true, snippet, spans };
  }

  const rawTokens = normQuery.split(" ").filter(Boolean);
  if (rawTokens.length < 2) {
    return { matched: false, exact: false, snippet: "", spans: [] };
  }

  const tokens: string[][] = [];
  let firstPos = -1;
  let firstLen = 0;
  for (const tok of rawTokens) {
    const tr = toLowerRunes(Array.from(tok));
    if (tr.length === 0) continue;
    const pos = indexRunesFrom(lowerText, tr, 0);
    if (pos < 0) {
      return { matched: false, exact: false, snippet: "", spans: [] };
    }
    if (firstPos < 0 || pos < firstPos) {
      firstPos = pos;
      firstLen = tr.length;
    }
    tokens.push(tr);
  }
  if (firstPos < 0) {
    return { matched: false, exact: false, snippet: "", spans: [] };
  }

  const { snippet, spans } = buildCenteredSnippet(
    textRunes,
    lowerText,
    tokens,
    firstPos,
    firstLen,
    maxRunes,
  );
  return { matched: true, exact: false, snippet, spans };
}

function matchConversationMetadata(
  entry: ConversationEntry,
  query: string,
): ConversationSearchHit | null {
  const fields: Array<{ text: string; baseScore: number }> = [
    { text: entry.title, baseScore: 100.0 },
    { text: entry.id, baseScore: 75.0 },
    { text: entry.cwd, baseScore: 70.0 },
    { text: entry.model, baseScore: 65.0 },
    { text: entry.profile, baseScore: 65.0 },
  ];

  let best: ConversationSearchHit | null = null;
  for (const f of fields) {
    if (!f.text.trim()) continue;
    const res = matchAndBuildSnippet(f.text, query, MAX_CONV_SNIPPET_RUNES);
    if (!res.matched) continue;
    let score = f.baseScore + recencyBonus(effectiveTimeMs(entry));
    if (res.exact) score += 10.0;
    if (!best || score > best.score) {
      best = {
        conversation: entry,
        match_kind: "metadata",
        match_role: "",
        snippet: res.snippet,
        snippet_highlights: res.spans,
        score,
      };
    }
  }
  return best;
}

async function matchConversationMessages(
  registry: HarnessPluginRegistry,
  entry: ConversationEntry,
  query: string,
  scope: "all" | "user",
): Promise<ConversationSearchHit | null> {
  if (!entry.source_path.trim()) return null;
  const plugin = registry.get(entry.agent);
  if (!plugin || plugin.spec.worktreeOnly || !plugin.parseLog) return null;

  let messages: ParsedLogMessage[];
  try {
    messages = await plugin.parseLog(entry.source_path);
  } catch {
    return null;
  }
  if (!messages.length) return null;

  let best: ConversationSearchHit | null = null;
  for (const msg of messages) {
    const role = (msg.role ?? "").trim().toLowerCase();
    if (scope === "user" && role !== "user") continue;
    if (!msg.text.trim()) continue;

    const res = matchAndBuildSnippet(msg.text, query, MAX_CONV_SNIPPET_RUNES);
    if (!res.matched) continue;

    const base = role === "user" ? 80.0 : 50.0;
    let score = base + recencyBonus(effectiveTimeMs(entry));
    if (res.exact) score += 10.0;

    if (!best || score > best.score) {
      best = {
        conversation: entry,
        match_kind: "message",
        match_role: role,
        snippet: res.snippet,
        snippet_highlights: res.spans,
        score,
      };
    }
  }

  return best;
}

export class ConversationService {
  private readonly registry: HarnessPluginRegistry;
  private readonly store?: SpacesStore;
  private readonly homeDir: string;
  private readonly spacesDir: string;
  private readonly cachePath: string;
  private readonly maxWorkers: number;
  private readonly now: () => number;

  private cacheLoaded = false;
  private cachedEntries: ConversationEntry[] = [];
  private cachedSyncedMs = 0;
  private cachedIndexedMessages = 0;

  constructor(opts: ConversationServiceOptions) {
    this.registry = opts.registry;
    this.store = opts.store;
    this.homeDir = realHomeDir(opts.homeDir ?? os.homedir());
    this.spacesDir = opts.spacesDir ?? defaultSpacesDir(this.homeDir);
    this.cachePath =
      opts.cachePath ?? path.join(this.spacesDir, "conversations-cache.json");
    this.maxWorkers = Math.max(1, opts.maxWorkers ?? DEFAULT_MAX_WORKERS);
    this.now = opts.now ?? (() => Date.now());
  }

  resetCache(): void {
    this.cacheLoaded = false;
    this.cachedEntries = [];
    this.cachedSyncedMs = 0;
    this.cachedIndexedMessages = 0;
  }

  async list(params: ConversationListParams = {}): Promise<ConversationSnapshot> {
    const refresh = Boolean(params.refresh);
    const includeArchived = Boolean(params.includeArchived);

    if (!refresh) {
      if (this.cacheLoaded) {
        const merged = this.applyOverlays(this.cachedEntries);
        return {
          ok: true,
          conversations: filterArchivedEntries(merged, includeArchived),
          synced_ms: this.cachedSyncedMs,
          indexed_messages: this.cachedIndexedMessages,
          pending_sources: 0,
        };
      }

      const diskSnap = this.loadDiskCache();
      if (diskSnap) {
        const merged = this.applyOverlays(diskSnap.conversations);
        this.cacheLoaded = true;
        this.cachedEntries = merged.map(cloneEntry);
        this.cachedSyncedMs = diskSnap.synced_ms;
        this.cachedIndexedMessages = diskSnap.indexed_messages;
        return {
          ok: true,
          conversations: filterArchivedEntries(merged, includeArchived),
          synced_ms: diskSnap.synced_ms,
          indexed_messages: diskSnap.indexed_messages,
          pending_sources: 0,
        };
      }
    }

    const { entries, indexedMessages } = await this.scanAll();
    const syncedMs = this.now();

    this.cacheLoaded = true;
    this.cachedEntries = entries.map(cloneEntry);
    this.cachedSyncedMs = syncedMs;
    this.cachedIndexedMessages = indexedMessages;

    this.saveDiskCache({
      version: CURRENT_CONV_CACHE_VERSION,
      synced_ms: syncedMs,
      indexed_messages: indexedMessages,
      conversations: entries,
    });

    return {
      ok: true,
      conversations: filterArchivedEntries(entries, includeArchived),
      synced_ms: syncedMs,
      indexed_messages: indexedMessages,
      pending_sources: 0,
    };
  }

  async search(
    req: Partial<ConversationSearchRequest> & { query: string },
  ): Promise<ConversationSearchHit[]> {
    const scope = normalizeSearchScope(req.scope);
    const query = (req.query ?? "").trim();
    if (!query) return [];

    const snap = await this.list({
      refresh: false,
      includeArchived: Boolean(req.include_archived),
    });

    const wantAgent = (req.agent ?? "").trim()
      ? canonicalConversationAgent(req.agent!)
      : "";
    const wantCwd = (req.cwd ?? "").trim() ? path.resolve(req.cwd!.trim()) : "";

    const candidates = snap.conversations.filter((entry) => {
      if (wantAgent && canonicalConversationAgent(entry.agent) !== wantAgent) {
        return false;
      }
      if (wantCwd) {
        if (!entry.cwd.trim() || path.resolve(entry.cwd.trim()) !== wantCwd) {
          return false;
        }
      }
      return true;
    });

    let hits: Array<ConversationSearchHit | null>;
    if (scope === "titles") {
      hits = candidates.map((entry) => matchConversationMetadata(entry, query));
    } else {
      hits = await runBounded(candidates, this.maxWorkers, async (entry) => {
        let best = matchConversationMetadata(entry, query);
        const msgHit = await matchConversationMessages(
          this.registry,
          entry,
          query,
          scope,
        );
        if (msgHit && (!best || msgHit.score > best.score)) {
          best = msgHit;
        }
        return best;
      });
    }

    const validHits = hits.filter((h): h is ConversationSearchHit => h !== null);
    validHits.sort((a, b) => {
      if (a.score !== b.score) return b.score - a.score;
      const ta = effectiveTimeMs(a.conversation);
      const tb = effectiveTimeMs(b.conversation);
      if (ta !== tb) return tb - ta;
      return a.conversation.id < b.conversation.id
        ? -1
        : a.conversation.id > b.conversation.id
          ? 1
          : 0;
    });

    const limit = req.limit ?? 0;
    if (limit > 0 && validHits.length > limit) {
      return validHits.slice(0, limit);
    }
    return validHits;
  }

  private discoverProfileRoots(): ProfileRoot[] {
    const out: ProfileRoot[] = [];
    const seen = new Set<string>();
    const bases = [
      path.join(this.spacesDir, "profiles"),
      path.join(this.homeDir, ".harness", "profiles"),
    ];

    for (const profilesDir of bases) {
      if (!existsSync(profilesDir)) continue;
      let agentEntries;
      try {
        agentEntries = readdirSync(profilesDir, { withFileTypes: true });
      } catch {
        continue;
      }

      for (const ae of agentEntries) {
        if (!ae.isDirectory()) continue;
        const plugin = this.registry.get(ae.name);
        const agentId = plugin?.spec.id ?? ae.name.trim().toLowerCase();
        if (!agentId) continue;

        let profEntries;
        try {
          profEntries = readdirSync(path.join(profilesDir, ae.name), {
            withFileTypes: true,
          });
        } catch {
          continue;
        }

        for (const pe of profEntries) {
          if (!pe.isDirectory() || pe.name === "default") continue;
          const key = `${agentId}/${pe.name}`;
          if (seen.has(key)) continue;

          const cfg = loadProfileConfig(agentId, pe.name, {
            homeDir: this.homeDir,
            spacesDir: this.spacesDir,
          });
          if (cfg && !cfg.enabled) {
            seen.add(key);
            continue;
          }

          const homeCandidate =
            cfg?.shadowHomePath ||
            path.join(profilesDir, ae.name, pe.name, "home");

          try {
            if (statSync(homeCandidate).isDirectory()) {
              seen.add(key);
              out.push({
                agent: agentId,
                profile: pe.name,
                homeDir: homeCandidate,
              });
            }
          } catch {
            // Directory does not exist; skip.
          }
        }
      }
    }

    out.sort((a, b) =>
      a.agent !== b.agent
        ? a.agent < b.agent
          ? -1
          : 1
        : a.profile < b.profile
          ? -1
          : a.profile > b.profile
            ? 1
            : 0,
    );
    return out;
  }

  private async scanAll(): Promise<{
    entries: ConversationEntry[];
    indexedMessages: number;
  }> {
    const resolvedRealHome = resolveRealPath(this.homeDir);
    const jobs: ScanJob[] = [];

    for (const plugin of this.registry.runtimePlugins()) {
      if (!plugin.listConversations) continue;
      if (this.homeDir) {
        jobs.push({
          plugin,
          homeDir: this.homeDir,
          resolvedHomeDir: resolvedRealHome,
          profile: "",
        });
      }
    }

    if (this.homeDir) {
      for (const pr of this.discoverProfileRoots()) {
        const plugin = this.registry.get(pr.agent);
        if (!plugin || plugin.spec.worktreeOnly || !plugin.listConversations) {
          continue;
        }
        if (profileStorePointsToRealHome(plugin, pr.homeDir, resolvedRealHome)) {
          continue;
        }
        jobs.push({
          plugin,
          homeDir: pr.homeDir,
          resolvedHomeDir: resolveRealPath(pr.homeDir),
          profile: pr.profile,
        });
      }
    }

    const batches = await runBounded(jobs, this.maxWorkers, async (job) => {
      let recs: ConversationRecord[];
      try {
        recs = await job.plugin.listConversations!({
          homeDir: job.homeDir,
          profile: job.profile || undefined,
          includeArchived: true,
        });
      } catch {
        return [];
      }
      if (!recs || recs.length === 0) return [];

      const mapped: ConversationEntry[] = [];
      for (const rawRec of recs) {
        let fallbackProfile = job.profile;
        let rec = rawRec;
        if (
          fallbackProfile &&
          pathPointsIntoRealHome(
            rawRec.sourcePath,
            job.resolvedHomeDir,
            resolvedRealHome,
          )
        ) {
          fallbackProfile = "";
          if (
            (rawRec.profile ?? "").trim().toLowerCase() ===
            job.profile.toLowerCase()
          ) {
            rec = { ...rawRec, profile: undefined };
          }
        }
        mapped.push(mapRecordToEntry(job.plugin, fallbackProfile, rec));
      }
      return mapped;
    });

    const byKey = new Map<string, ConversationEntry>();
    for (const batch of batches) {
      for (const entry of batch) {
        const idKey = entry.id.trim() || entry.source_path.trim();
        if (!idKey) continue;
        const canonAgent = canonicalConversationAgent(entry.agent) || entry.agent;
        const key = `${canonAgent}\0${idKey}`;
        const existing = byKey.get(key);
        if (existing) {
          byKey.set(key, mergeConversationEntries(existing, entry));
        } else {
          byKey.set(key, entry);
        }
      }
    }

    const merged = this.applyOverlays(Array.from(byKey.values()));
    let indexedMessages = 0;
    for (const entry of merged) {
      indexedMessages += entry.message_count;
    }
    return { entries: merged, indexedMessages };
  }

  private loadOverlayMap(): Map<string, ConversationOverlayRecord> {
    const map = new Map<string, ConversationOverlayRecord>();
    if (!this.store) return map;
    let overlays: ConversationOverlayRecord[];
    try {
      overlays = this.store.listConversationOverlays();
    } catch {
      return map;
    }
    for (const ov of overlays) {
      const canon = canonicalConversationAgent(ov.agent);
      const sid = ov.session_id.trim();
      if (!canon || !sid) continue;
      map.set(overlayKey(canon, sid), ov);
    }
    return map;
  }

  private applyOverlays(entries: ConversationEntry[]): ConversationEntry[] {
    const overlays = this.loadOverlayMap();
    const out = entries.map(cloneEntry);
    if (overlays.size > 0) {
      for (const entry of out) {
        const canon = canonicalConversationAgent(entry.agent) || entry.agent;
        const ov = overlays.get(overlayKey(canon, entry.id));
        if (ov) {
          applyOverlayToEntry(entry, ov);
        }
      }
    }
    sortConversationEntries(out);
    return out;
  }

  private loadDiskCache(): CachedConvSnapshot | null {
    if (!this.cachePath || !existsSync(this.cachePath)) return null;
    try {
      const raw = readFileSync(this.cachePath, "utf8").trim();
      if (!raw) return null;
      const parsed = JSON.parse(raw) as Partial<CachedConvSnapshot>;
      if (parsed.version !== CURRENT_CONV_CACHE_VERSION) return null;
      if (!Array.isArray(parsed.conversations)) return null;
      const syncedMs = typeof parsed.synced_ms === "number" ? parsed.synced_ms : 0;
      if (parsed.conversations.length === 0 && syncedMs <= 0) return null;
      return {
        version: CURRENT_CONV_CACHE_VERSION,
        synced_ms: syncedMs,
        indexed_messages:
          typeof parsed.indexed_messages === "number"
            ? parsed.indexed_messages
            : 0,
        conversations: parsed.conversations,
      };
    } catch {
      return null;
    }
  }

  private saveDiskCache(snap: CachedConvSnapshot): void {
    if (!this.cachePath) return;
    const dir = path.dirname(this.cachePath);
    const tmpPath = path.join(
      dir,
      `.conversations-cache-${process.pid}-${randomBytes(6).toString("hex")}.tmp`,
    );
    try {
      mkdirSync(dir, { recursive: true });
      writeFileSync(tmpPath, JSON.stringify(snap), {
        encoding: "utf8",
        mode: 0o600,
      });
      chmodSync(tmpPath, 0o600);
      renameSync(tmpPath, this.cachePath);
    } catch {
      try {
        if (existsSync(tmpPath)) unlinkSync(tmpPath);
      } catch {
        // Ignore cleanup error.
      }
    }
  }
}

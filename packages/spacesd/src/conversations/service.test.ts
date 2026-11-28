import { Database } from "bun:sqlite";
import { afterEach, describe, expect, test } from "bun:test";
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { KiloPlugin } from "../plugins/providers/kilo";
import { MusePlugin } from "../plugins/providers/muse";
import { OpenCodePlugin } from "../plugins/providers/opencode";
import { HarnessPluginRegistry } from "../plugins/registry";
import { SpacesStore } from "../store/db";
import { ConversationService, matchAndBuildSnippet } from "./service";

const tempDirs: string[] = [];

function makeTempDir(prefix: string): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop();
    if (dir) {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

function seedOpenCodeDb(
  homeDir: string,
  rows: Array<{
    id: string;
    directory: string;
    title: string;
    model?: string;
    createdMs: number;
    updatedMs: number;
    archivedMs?: number;
    messages?: Array<{ role: "user" | "assistant"; text: string }>;
  }>,
): string {
  const dbDir = path.join(homeDir, ".local", "share", "opencode");
  mkdirSync(dbDir, { recursive: true });
  const dbPath = path.join(dbDir, "opencode.db");
  const db = new Database(dbPath);
  db.exec(`
    CREATE TABLE IF NOT EXISTS session_v2 (
      id TEXT PRIMARY KEY,
      parent_id TEXT,
      directory TEXT,
      title TEXT,
      model TEXT,
      time_created INTEGER,
      time_updated INTEGER,
      time_archived INTEGER
    );
    CREATE TABLE IF NOT EXISTS session_message (
      id TEXT PRIMARY KEY,
      session_id TEXT,
      type TEXT,
      seq INTEGER,
      time_created INTEGER,
      data TEXT
    );
  `);
  for (const r of rows) {
    db.run(
      `INSERT OR REPLACE INTO session_v2 VALUES (?, '', ?, ?, ?, ?, ?, ?)`,
      [
        r.id,
        r.directory,
        r.title,
        r.model ?? "",
        r.createdMs,
        r.updatedMs,
        r.archivedMs ?? 0,
      ],
    );
    let seq = 1;
    for (const m of r.messages ?? []) {
      db.run(
        `INSERT OR REPLACE INTO session_message VALUES (?, ?, ?, ?, ?, ?)`,
        [
          `${r.id}-m${seq}`,
          r.id,
          m.role,
          seq,
          r.createdMs + seq * 1000,
          JSON.stringify({ text: m.text }),
        ],
      );
      seq += 1;
    }
  }
  db.close();
  return dbPath;
}

function seedKiloDb(
  homeDir: string,
  rows: Array<{
    id: string;
    directory: string;
    title: string;
    updatedMs: number;
    userText: string;
  }>,
): void {
  const dbDir = path.join(homeDir, ".local", "share", "kilo");
  mkdirSync(dbDir, { recursive: true });
  const db = new Database(path.join(dbDir, "kilo.db"));
  db.exec(`
    CREATE TABLE IF NOT EXISTS session (
      id TEXT PRIMARY KEY,
      directory TEXT,
      title TEXT,
      time_created INTEGER,
      time_updated INTEGER
    );
    CREATE TABLE IF NOT EXISTS session_message (
      id TEXT PRIMARY KEY,
      session_id TEXT,
      type TEXT,
      seq INTEGER,
      time_created INTEGER,
      data TEXT
    );
  `);
  for (const r of rows) {
    db.run(`INSERT OR REPLACE INTO session VALUES (?, ?, ?, ?, ?)`, [
      r.id,
      r.directory,
      r.title,
      r.updatedMs - 5000,
      r.updatedMs,
    ]);
    db.run(`INSERT OR REPLACE INTO session_message VALUES (?, ?, 'user', 1, ?, ?)`, [
      `${r.id}-u1`,
      r.id,
      r.updatedMs - 4000,
      JSON.stringify({ text: r.userText }),
    ]);
  }
  db.close();
}

function seedMuseDb(
  homeDir: string,
  rows: Array<{
    id: string;
    cwd: string;
    title: string;
    prompt: string;
    updatedMs: number;
  }>,
): void {
  const dbDir = path.join(homeDir, ".local", "share", "muse");
  mkdirSync(dbDir, { recursive: true });
  const db = new Database(path.join(dbDir, "session-index.db"));
  db.exec(`
    CREATE TABLE IF NOT EXISTS sessions (
      session_id TEXT PRIMARY KEY,
      workspace_root TEXT,
      title TEXT,
      first_user_prompt TEXT,
      model_id TEXT,
      created_at_us INTEGER,
      updated_at_us INTEGER,
      prompt_count INTEGER,
      session_log_path TEXT,
      msp_parent_session_id TEXT
    );
  `);
  for (const r of rows) {
    db.run(`INSERT OR REPLACE INTO sessions VALUES (?, ?, ?, ?, ?, ?, ?, 2, '', '')`, [
      r.id,
      r.cwd,
      r.title,
      r.prompt,
      "llama-4",
      (r.updatedMs - 2000) * 1000,
      r.updatedMs * 1000,
    ]);
  }
  db.close();
}

describe("ConversationService", () => {
  test("multi-plugin discovery, profile shadow homes, symlink dedup, disk cache (0600), and overlay merge", async () => {
    const homeDir = makeTempDir("conv-svc-home-");
    const spacesDir = path.join(homeDir, ".spaces");
    mkdirSync(spacesDir, { recursive: true });

    // Real home sessions across all 3 SQLite providers
    seedOpenCodeDb(homeDir, [
      {
        id: "oc-main",
        directory: "/work/alpha",
        title: "Alpha OpenCode session",
        model: "claude-sonnet-4",
        createdMs: 1700000100000,
        updatedMs: 1700000200000,
        messages: [
          { role: "user", text: "Fix the auth middleware" },
          { role: "assistant", text: "Middleware updated." },
        ],
      },
      {
        id: "oc-archived",
        directory: "/work/alpha",
        title: "Old archived session",
        createdMs: 1700000050000,
        updatedMs: 1700000060000,
        archivedMs: 1700000070000,
        messages: [{ role: "user", text: "Archived prompt" }],
      },
    ]);
    seedKiloDb(homeDir, [
      {
        id: "kilo-main",
        directory: "/work/beta",
        title: "",
        updatedMs: 1700000150000,
        userText: "Investigate flaky webhook retry queue",
      },
    ]);
    seedMuseDb(homeDir, [
      {
        id: "muse-main",
        cwd: "/work/gamma",
        title: "Muse architecture review",
        prompt: "Review our daemon IPC protocol",
        updatedMs: 1700000180000,
      },
    ]);

    // Isolated profile shadow home under <spacesDir>/profiles/opencode/work/home
    const workProfileHome = path.join(
      spacesDir,
      "profiles",
      "opencode",
      "work",
      "home",
    );
    seedOpenCodeDb(workProfileHome, [
      {
        id: "oc-work-1",
        directory: "/work/corp",
        title: "Corp isolated session",
        createdMs: 1700000250000,
        updatedMs: 1700000260000,
        messages: [{ role: "user", text: "Deploy corp service" }],
      },
    ]);

    // Symlinked profile shadow home under <homeDir>/.harness/profiles/opencode/linked/home
    // whose opencode.db symlinks back into real home -> must be deduplicated!
    const linkedProfileHome = path.join(
      homeDir,
      ".harness",
      "profiles",
      "opencode",
      "linked",
      "home",
    );
    mkdirSync(path.join(linkedProfileHome, ".local", "share", "opencode"), {
      recursive: true,
    });
    symlinkSync(
      path.join(homeDir, ".local", "share", "opencode", "opencode.db"),
      path.join(linkedProfileHome, ".local", "share", "opencode", "opencode.db"),
    );

    const registry = new HarnessPluginRegistry();
    registry.register(new OpenCodePlugin({ homeDir, spacesDir }));
    registry.register(new KiloPlugin({ homeDir, spacesDir }));
    registry.register(new MusePlugin({ homeDir, spacesDir }));

    const store = new SpacesStore(path.join(spacesDir, "spaces.db"));
    // Overlay: wins for space, last_opened_ms, last_outcome, launch_cmd; fallback-only for profile/cwd/title
    store.upsertConversationOverlay({
      agent: "kilo",
      session_id: "kilo-main",
      space: "beta-space",
      last_opened_ms: 1700000999000, // bumps kilo-main to top by effectiveTimeMs!
      last_outcome: "done",
      profile: "fallback-profile",
      cwd: "/should-not-overwrite-cwd",
      title: "Should not overwrite derived title",
      launch_cmd: ["kilo", "--session", "kilo-main"],
    });
    store.upsertConversationOverlay({
      agent: "opencode",
      session_id: "oc-main",
      space: "alpha-space",
      title: "Should not overwrite existing title",
    });

    const service = new ConversationService({
      registry,
      store,
      homeDir,
      spacesDir,
      now: () => 1700001000000,
    });

    const snap = await service.list({ includeArchived: false });
    expect(snap.ok).toBe(true);
    expect(snap.synced_ms).toBe(1700001000000);
    expect(snap.conversations.map((c) => c.id)).toEqual([
      "kilo-main", // last_opened_ms = 1700000999000
      "oc-work-1", // updated_at_ms = 1700000260000
      "oc-main", // updated_at_ms = 1700000200000
      "muse-main", // updated_at_ms = 1700000180000
    ]);

    // Verify symlinked profile did NOT tag real home's oc-main with "linked"
    const ocMain = snap.conversations.find((c) => c.id === "oc-main")!;
    expect(ocMain.profile).toBe("");
    expect(ocMain.space).toBe("alpha-space");
    expect(ocMain.title).toBe("Alpha OpenCode session");

    // Verify isolated profile WAS tagged with "work"
    const ocWork = snap.conversations.find((c) => c.id === "oc-work-1")!;
    expect(ocWork.profile).toBe("work");

    // Verify overlay semantics on kilo-main
    const kiloMain = snap.conversations.find((c) => c.id === "kilo-main")!;
    expect(kiloMain.space).toBe("beta-space");
    expect(kiloMain.last_opened_ms).toBe(1700000999000);
    expect(kiloMain.last_outcome).toBe("done");
    expect(kiloMain.launch_cmd).toEqual(["kilo", "--session", "kilo-main"]);
    expect(kiloMain.profile).toBe("fallback-profile"); // fallback applied
    expect(kiloMain.cwd).toBe("/work/beta"); // existing cwd preserved
    expect(kiloMain.title).toBe("Investigate flaky webhook retry queue"); // existing title preserved

    // Verify includeArchived: true returns archived conversation
    const snapWithArchived = await service.list({ includeArchived: true });
    expect(snapWithArchived.conversations.some((c) => c.id === "oc-archived")).toBe(
      true,
    );

    // Verify atomic 0600 disk cache exists and reloads in a fresh service instance
    const cachePath = path.join(spacesDir, "conversations-cache.json");
    expect(statSync(cachePath).mode & 0o777).toBe(0o600);

    const freshService = new ConversationService({
      registry,
      store,
      homeDir,
      spacesDir,
      now: () => 1700002000000,
    });
    const fromDisk = await freshService.list({ refresh: false });
    expect(fromDisk.synced_ms).toBe(1700001000000); // loaded from disk cache
    expect(fromDisk.conversations).toHaveLength(4);

    // refresh: true rescans and updates synced_ms
    const refreshed = await freshService.list({ refresh: true });
    expect(refreshed.synced_ms).toBe(1700002000000);

    store.close();
  });

  test("full-text search scopes, scoring, filters, and 140-rune Unicode SnippetSpan", async () => {
    const homeDir = makeTempDir("conv-search-home-");
    const spacesDir = path.join(homeDir, ".spaces");
    mkdirSync(spacesDir, { recursive: true });

    seedOpenCodeDb(homeDir, [
      {
        id: "s-title",
        directory: "/work/repo-a",
        title: "Quantum router tuning",
        model: "claude-opus-4",
        createdMs: 1700000100000,
        updatedMs: 1700000100000,
        messages: [
          { role: "user", text: "Unrelated body text" },
        ],
      },
      {
        id: "s-user",
        directory: "/work/repo-a",
        title: "General debugging",
        createdMs: 1700000200000,
        updatedMs: 1700000200000,
        messages: [
          {
            role: "user",
            text: "Please inspect the quantum router tuning logs for latency spikes",
          },
          {
            role: "assistant",
            text: "Checking latency now.",
          },
        ],
      },
      {
        id: "s-assistant",
        directory: "/work/repo-b",
        title: "Other session",
        createdMs: 1700000300000,
        updatedMs: 1700000300000,
        messages: [
          { role: "user", text: "Why is it slow?" },
          {
            role: "assistant",
            text: "The quantum router tuning config needs adjustment.",
          },
        ],
      },
    ]);

    const registry = new HarnessPluginRegistry();
    registry.register(new OpenCodePlugin({ homeDir, spacesDir }));
    const service = new ConversationService({ registry, homeDir, spacesDir });

    // scope: "all" -> title (110+) > user (90+) > assistant (60+)
    const allHits = await service.search({
      query: "quantum router tuning",
      scope: "all",
    });
    expect(allHits.map((h) => h.conversation.id)).toEqual([
      "s-title",
      "s-user",
      "s-assistant",
    ]);
    expect(allHits[0]?.match_kind).toBe("metadata");
    expect(allHits[1]?.match_kind).toBe("message");
    expect(allHits[1]?.match_role).toBe("user");
    expect(allHits[2]?.match_kind).toBe("message");
    expect(allHits[2]?.match_role).toBe("assistant");
    expect(allHits[0]!.score).toBeGreaterThan(allHits[1]!.score);
    expect(allHits[1]!.score).toBeGreaterThan(allHits[2]!.score);

    // scope: "titles" -> only metadata match
    const titleHits = await service.search({
      query: "quantum router tuning",
      scope: "titles",
    });
    expect(titleHits.map((h) => h.conversation.id)).toEqual(["s-title"]);

    // scope: "user" -> title metadata + user message, excludes assistant-only match
    const userHits = await service.search({
      query: "quantum router tuning",
      scope: "user",
    });
    expect(userHits.map((h) => h.conversation.id)).toEqual(["s-title", "s-user"]);

    // CWD + limit filtering
    const cwdHits = await service.search({
      query: "quantum router tuning",
      cwd: "/work/repo-b",
      limit: 5,
    });
    expect(cwdHits.map((h) => h.conversation.id)).toEqual(["s-assistant"]);

    // 140-rune centered Unicode snippet with emoji (surrogate pairs) & merged SnippetSpan
    const prefix = "🚀".repeat(90);
    const suffix = "✨".repeat(90);
    const text = `${prefix} café quantum router ${suffix}`;
    const res = matchAndBuildSnippet(text, "café quantum", 140);
    expect(res.matched).toBe(true);
    expect(res.exact).toBe(true);
    expect(res.snippet.startsWith("…")).toBe(true);
    expect(res.snippet.endsWith("…")).toBe(true);
    const snippetRunes = Array.from(res.snippet);
    expect(snippetRunes.length).toBe(142); // 140 window runes + leading "…" + trailing "…"
    expect(res.spans).toHaveLength(1);
    const span = res.spans[0]!;
    expect(snippetRunes.slice(span.start, span.end).join("")).toBe(
      "café quantum",
    );
  });
});

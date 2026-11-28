import { Database } from "bun:sqlite";
import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { KiloPlugin } from "./kilo";
import { MusePlugin } from "./muse";
import { OpenCodePlugin } from "./opencode";

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

describe("OpenCodePlugin", () => {
  test("spec, resumeArgs, and interactive launch rules", () => {
    const homeDir = makeTempDir("oc-home-");
    const spacesDir = path.join(homeDir, ".spaces");
    mkdirSync(path.join(spacesDir, "profiles", "opencode", "work"), {
      recursive: true,
    });
    writeFileSync(
      path.join(spacesDir, "profiles", "opencode", "work", "profile.json"),
      JSON.stringify({ enabled: true, router: false }),
      "utf8",
    );

    const plugin = new OpenCodePlugin({ homeDir, spacesDir });
    expect(plugin.spec.id).toBe("opencode");
    expect(plugin.spec.aliases).toEqual(["open-code", "herdr:opencode", "oc"]);
    expect(plugin.spec.oscAuthority).toBe(true);
    expect(plugin.spec.resumeFlag).toBe("--session");
    expect(plugin.spec.mcpTargets).toEqual([".config/opencode/opencode.json"]);
    expect(plugin.resumeArgs("ses_123")).toEqual(["--session", "ses_123"]);

    // Empty args -> auto-prepend --standalone
    const bare = plugin.buildInteractiveLaunch({ cwd: "/repo" });
    expect(bare.argv).toEqual(["opencode", "--standalone"]);

    // Flag-like first arg + auto access -> auto-prepend --standalone + --auto-approve
    const autoFlag = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      accessLevel: "auto",
      sessionId: "ses_123",
    });
    expect(autoFlag.argv).toEqual([
      "opencode",
      "--standalone",
      "--auto-approve",
      "--session",
      "ses_123",
    ]);

    // First arg "mini" -> auto-prepend --standalone
    const mini = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      extraArgs: ["mini"],
    });
    expect(mini.argv).toEqual(["opencode", "--standalone", "mini"]);

    // Already has --server -> do NOT prepend --standalone
    const server = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      extraArgs: ["--server=http://127.0.0.1:4096"],
    });
    expect(server.argv).toEqual([
      "opencode",
      "--server=http://127.0.0.1:4096",
    ]);

    // Subcommand -> do NOT prepend --standalone
    const subcmd = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      extraArgs: ["serve"],
    });
    expect(subcmd.argv).toEqual(["opencode", "serve"]);

    // Shadow profile sets XDG_DATA_HOME
    const prof = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      profile: "work",
    });
    expect(prof.env.XDG_DATA_HOME).toBe(
      path.join(spacesDir, "profiles", "opencode", "work", "home"),
    );
  });

  test("reads SQLite session_v2 + session_message, skips subagents/placeholders, parses logs, and falls back to JSON", () => {
    const homeDir = makeTempDir("oc-store-");
    const dbDir = path.join(homeDir, ".local", "share", "opencode");
    mkdirSync(dbDir, { recursive: true });
    const dbPath = path.join(dbDir, "opencode.db");

    const db = new Database(dbPath);
    db.exec(`
      CREATE TABLE session_v2 (
        id TEXT PRIMARY KEY,
        parent_id TEXT,
        directory TEXT,
        title TEXT,
        model TEXT,
        time_created INTEGER,
        time_updated INTEGER,
        time_archived INTEGER
      );
      CREATE TABLE session_message (
        id TEXT PRIMARY KEY,
        session_id TEXT,
        type TEXT,
        seq INTEGER,
        time_created INTEGER,
        data TEXT
      );
    `);
    db.run(
      `INSERT INTO session_v2 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "oc-1",
        "",
        "/work/app",
        "New session - 2026",
        JSON.stringify({ id: "claude-sonnet-4" }),
        1700000001000,
        1700000005000,
        0,
      ],
    );
    db.run(
      `INSERT INTO session_v2 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "oc-sub",
        "oc-1",
        "/work/app",
        "Subagent child",
        "",
        1700000002000,
        1700000002000,
        0,
      ],
    );
    db.run(
      `INSERT INTO session_v2 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "oc-empty",
        "",
        "/work/app",
        "New session - empty",
        "",
        1700000003000,
        1700000003000,
        0,
      ],
    );
    db.run(
      `INSERT INTO session_v2 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "oc-arch",
        "",
        "/work/app",
        "Archived thread",
        "gpt-5",
        1700000000000,
        1700000004000,
        1700000004500,
      ],
    );
    db.run(
      `INSERT INTO session_message VALUES (?, ?, ?, ?, ?, ?)`,
      [
        "m1",
        "oc-1",
        "user",
        1,
        1700000001000,
        JSON.stringify({ text: "Refactor the SQLite query layer" }),
      ],
    );
    db.run(
      `INSERT INTO session_message VALUES (?, ?, ?, ?, ?, ?)`,
      [
        "m2",
        "oc-1",
        "assistant",
        2,
        1700000002000,
        JSON.stringify({ text: "Done refactoring the query layer." }),
      ],
    );
    db.close();

    // Also add a JSON fallback session
    const jsonDir = path.join(homeDir, ".opencode", "sessions");
    mkdirSync(jsonDir, { recursive: true });
    writeFileSync(
      path.join(jsonDir, "oc-json.json"),
      JSON.stringify({
        id: "oc-json",
        title: "JSON fallback session",
        cwd: "/work/json",
        messages: [{ role: "user", content: "Hello from JSON fallback" }],
      }),
      "utf8",
    );

    const plugin = new OpenCodePlugin({ homeDir });
    const active = plugin.listConversations({ homeDir, includeArchived: false });
    expect(active.map((r) => r.id).sort()).toEqual(["oc-1", "oc-json"]);

    const oc1 = active.find((r) => r.id === "oc-1")!;
    expect(oc1.title).toBe("Refactor the SQLite query layer");
    expect(oc1.model).toBe("claude-sonnet-4");
    expect(oc1.messageCount).toBe(2);
    expect(oc1.sourcePath).toBe(`${dbPath}#oc-1`);

    const all = plugin.listConversations({ homeDir, includeArchived: true });
    expect(all.map((r) => r.id).sort()).toEqual(["oc-1", "oc-arch", "oc-json"]);
    expect(all.find((r) => r.id === "oc-arch")?.archived).toBe(true);

    const log = plugin.parseLog(oc1.sourcePath);
    expect(log).toHaveLength(2);
    expect(log[0]?.role).toBe("user");
    expect(log[0]?.text).toContain("Refactor the SQLite query layer");
    expect(log[1]?.role).toBe("assistant");

    const resolved = plugin.resolveVendorSession(
      "opencode",
      "/work/app",
      "",
      "",
      new Set(),
    );
    expect(resolved).toEqual({ sessionId: "oc-1", storeAvailable: true });
  });
});

describe("KiloPlugin", () => {
  test("spec, defaultBinary, XDG_DATA_HOME, SQLite session+message+part, and JSON fallback", () => {
    const homeDir = makeTempDir("kilo-home-");
    const spacesDir = path.join(homeDir, ".spaces");
    mkdirSync(path.join(spacesDir, "profiles", "kilo", "alt"), {
      recursive: true,
    });
    writeFileSync(
      path.join(spacesDir, "profiles", "kilo", "alt", "profile.json"),
      JSON.stringify({ enabled: true }),
      "utf8",
    );

    const plugin = new KiloPlugin({ homeDir, spacesDir });
    expect(plugin.spec.id).toBe("kilo");
    expect(plugin.spec.oscAuthority).toBe(false);
    expect(plugin.spec.mcpTargets).toEqual([".config/kilo/kilo.json"]);
    expect(plugin.resumeArgs("kilo-1")).toEqual(["--session", "kilo-1"]);

    const launch = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      profile: "alt",
    });
    expect(launch.argv[0]).toBe(path.join(homeDir, ".kilo", "bin", "kilo"));
    expect(launch.env.XDG_DATA_HOME).toBe(
      path.join(spacesDir, "profiles", "kilo", "alt", "home"),
    );

    const dbDir = path.join(homeDir, ".local", "share", "kilo");
    mkdirSync(dbDir, { recursive: true });
    const dbPath = path.join(dbDir, "kilo.db");
    const db = new Database(dbPath);
    db.exec(`
      CREATE TABLE session (
        id TEXT PRIMARY KEY,
        directory TEXT,
        title TEXT,
        time_created INTEGER,
        time_updated INTEGER
      );
      CREATE TABLE message (
        id TEXT PRIMARY KEY,
        session_id TEXT,
        time_created INTEGER,
        data TEXT
      );
      CREATE TABLE part (
        id TEXT PRIMARY KEY,
        message_id TEXT,
        time_created INTEGER,
        data TEXT
      );
    `);
    db.run(`INSERT INTO session VALUES (?, ?, ?, ?, ?)`, [
      "kilo-1",
      "/work/kilo",
      "Fix Kilo parser",
      1700000100000,
      1700000200000,
    ]);
    db.run(`INSERT INTO message VALUES (?, ?, ?, ?)`, [
      "km1",
      "kilo-1",
      1700000101000,
      JSON.stringify({ role: "user", model: { modelID: "kilo-pro" } }),
    ]);
    db.run(`INSERT INTO part VALUES (?, ?, ?, ?)`, [
      "kp1",
      "km1",
      1700000101000,
      JSON.stringify({ text: "Please fix the Kilo SQLite parser" }),
    ]);
    db.close();

    const records = plugin.listConversations({ homeDir, includeArchived: true });
    expect(records).toHaveLength(1);
    expect(records[0]?.id).toBe("kilo-1");
    expect(records[0]?.title).toBe("Fix Kilo parser");
    expect(records[0]?.model).toBe("kilo-pro");
    expect(records[0]?.firstMessage).toBe("Please fix the Kilo SQLite parser");

    const parsed = plugin.parseLog(records[0]!.sourcePath);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.role).toBe("user");
    expect(parsed[0]?.model).toBe("kilo-pro");
  });
});

describe("MusePlugin", () => {
  test("spec, positional resume, MUSE_AUTH_PATH, META_API_KEY, and session-index.db", () => {
    const homeDir = makeTempDir("muse-home-");
    const spacesDir = path.join(homeDir, ".spaces");
    mkdirSync(path.join(spacesDir, "profiles", "muse", "team"), {
      recursive: true,
    });
    writeFileSync(
      path.join(spacesDir, "profiles", "muse", "team", "profile.json"),
      JSON.stringify({ enabled: true, router: true }),
      "utf8",
    );

    const plugin = new MusePlugin({ homeDir, spacesDir });
    expect(plugin.spec.id).toBe("muse");
    expect(plugin.spec.oscAuthority).toBe(true);
    expect(plugin.spec.positionalResume).toBe(true);
    expect(plugin.spec.resumeSubcommand).toBe("resume");
    expect(plugin.spec.mcpTargets).toEqual([".config/muse/settings.json"]);
    expect(plugin.resumeArgs("muse-42")).toEqual(["resume", "muse-42"]);

    const launch = plugin.buildInteractiveLaunch({
      cwd: "/repo",
      profile: "team",
      env: { MODEL_ROUTER_API_KEY: "secret-router-key" },
    });
    expect(launch.argv[0]).toBe(path.join(homeDir, ".local", "bin", "muse"));
    expect(launch.env.MUSE_AUTH_PATH).toBe(
      path.join(spacesDir, "profiles", "muse", "team", "home", "auth.json"),
    );
    expect(launch.env.META_API_KEY).toBe("secret-router-key");

    const dbDir = path.join(homeDir, ".local", "share", "muse");
    mkdirSync(dbDir, { recursive: true });
    const dbPath = path.join(dbDir, "session-index.db");
    const db = new Database(dbPath);
    db.exec(`
      CREATE TABLE sessions (
        session_id TEXT PRIMARY KEY,
        workspace_root TEXT,
        title TEXT,
        effective_long_title TEXT,
        first_user_prompt TEXT,
        model_id TEXT,
        created_at_us INTEGER,
        updated_at_us INTEGER,
        prompt_count INTEGER,
        session_log_path TEXT,
        msp_parent_session_id TEXT
      );
    `);
    db.run(
      `INSERT INTO sessions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "muse-main",
        "/work/muse",
        "Build Muse index",
        "",
        "Implement Muse SQLite session reader",
        "llama-4-maverick",
        1700000500000000, // microseconds
        1700000600000000,
        4,
        "",
        "",
      ],
    );
    db.run(
      `INSERT INTO sessions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        "muse-child",
        "/work/muse",
        "Child subagent",
        "",
        "Subagent task",
        "llama-4-maverick",
        1700000550000000,
        1700000550000000,
        1,
        "",
        "muse-main",
      ],
    );
    db.close();

    const records = plugin.listConversations({ homeDir, includeArchived: true });
    expect(records).toHaveLength(1);
    expect(records[0]?.id).toBe("muse-main");
    expect(records[0]?.title).toBe("Build Muse index");
    expect(records[0]?.createdAtMs).toBe(1700000500000);
    expect(records[0]?.updatedAtMs).toBe(1700000600000);
    expect(records[0]?.messageCount).toBe(4);
    expect(records[0]?.sourcePath).toBe(`${dbPath}#muse-main`);

    const parsed = plugin.parseLog(records[0]!.sourcePath);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.role).toBe("user");
    expect(parsed[0]?.text).toBe("Implement Muse SQLite session reader");

    const resolved = plugin.resolveVendorSession(
      "muse",
      "/work/muse",
      "",
      "build muse index",
      new Set(),
    );
    expect(resolved).toEqual({ sessionId: "muse-main", storeAvailable: true });
  });
});

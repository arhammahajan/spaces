import { Database } from "bun:sqlite";
import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  buildGeminiHeadlessArgv,
  GeminiHeadlessError,
  GeminiHeadlessExecutor,
  type GeminiSpawnedProcess,
} from "../../executors/gemini";
import {
  HeadlessUnsupportedError,
  type HeadlessTurnEvent,
  type InteractiveLaunchSpec,
} from "../types";
import {
  AntigravityPlugin,
  antigravitySummaryDbPath,
  type AntigravityProcReader,
} from "./antigravity";
import { GeminiPlugin } from "./gemini";

function makeStream(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
}

function makeMockProc(opts: {
  stdoutChunks?: string[];
  stderrChunks?: string[];
  exitCode?: number;
  autoExit?: boolean;
}): {
  proc: GeminiSpawnedProcess;
  signals: Array<NodeJS.Signals | number | undefined>;
  resolveExit: (code: number) => void;
} {
  const signals: Array<NodeJS.Signals | number | undefined> = [];
  let resolveExit!: (code: number) => void;
  const exited = new Promise<number>((resolve) => {
    resolveExit = resolve;
  });

  if (opts.autoExit !== false) {
    queueMicrotask(() => resolveExit(opts.exitCode ?? 0));
  }

  const proc: GeminiSpawnedProcess = {
    stdout: makeStream(opts.stdoutChunks ?? []),
    stderr: makeStream(opts.stderrChunks ?? []),
    exited,
    kill(signal) {
      signals.push(signal);
    },
  };

  return { proc, signals, resolveExit };
}

describe("AntigravityPlugin", () => {
  let tmpRoot: string;
  let homeDir: string;
  let spacesDir: string;

  beforeEach(() => {
    tmpRoot = mkdtempSync(path.join(os.tmpdir(), "spaces-agygem-test-"));
    homeDir = path.join(tmpRoot, "home");
    spacesDir = path.join(tmpRoot, "spaces");
    mkdirSync(homeDir, { recursive: true });
    mkdirSync(spacesDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(tmpRoot, { recursive: true, force: true });
  });

  test("spec metadata, resumeArgs, and unsupported headless", () => {
    const plugin = new AntigravityPlugin({ homeDir, spacesDir });
    expect(plugin.spec).toEqual({
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
    });

    expect(plugin.resumeArgs("conv-123")).toEqual(["--conversation", "conv-123"]);
    expect(plugin.resumeArgs("   ")).toEqual([]);
    expect(() =>
      plugin.startHeadless({ threadId: "t1", cwd: "/tmp" }),
    ).toThrow(HeadlessUnsupportedError);
  });

  test("discoverLiveSessionId BFS prefers .db{,-wal,-shm} over .lock up to depth 6", () => {
    const tree: Record<number, { children: number[]; fds: string[] }> = {
      100: {
        children: [101],
        fds: ["/home/u/.gemini/antigravity-cli/presence/lock-root.lock"],
      },
      101: {
        children: [102, 100], // includes cycle back to 100
        fds: ["/dev/pts/1"],
      },
      102: {
        children: [],
        fds: [
          "/home/u/.gemini/antigravity-cli/conversations/conv-db-wal.db-wal (deleted)",
        ],
      },
    };

    const procReader: AntigravityProcReader = {
      listChildren: (pid) => tree[pid]?.children ?? [],
      listFdTargets: (pid) => tree[pid]?.fds ?? [],
    };

    const plugin = new AntigravityPlugin({ homeDir, spacesDir, procReader });
    expect(plugin.discoverLiveSessionId(100)).toBe("conv-db-wal");
    expect(plugin.discoverLiveSessionId(0)).toBe("");
    expect(plugin.discoverLiveSessionId(-5)).toBe("");
  });

  test("discoverLiveSessionId falls back to .lock and respects max BFS depth 6", () => {
    const tree: Record<number, { children: number[]; fds: string[] }> = {
      1: {
        children: [2],
        fds: ["/home/u/.gemini/antigravity-cli/presence/fallback-lock.lock"],
      },
      2: { children: [3], fds: [] },
      3: { children: [4], fds: [] },
      4: { children: [5], fds: [] },
      5: { children: [6], fds: [] },
      6: { children: [7], fds: [] },
      7: { children: [8], fds: [] }, // depth 6
      8: {
        children: [],
        // depth 7: beyond max BFS depth, should not be visited
        fds: ["/home/u/.gemini/antigravity-cli/conversations/too-deep.db"],
      },
    };

    const procReader: AntigravityProcReader = {
      listChildren: (pid) => tree[pid]?.children ?? [],
      listFdTargets: (pid) => tree[pid]?.fds ?? [],
    };

    const plugin = new AntigravityPlugin({ homeDir, spacesDir, procReader });
    expect(plugin.discoverLiveSessionId(1)).toBe("fallback-lock");

    // Put .db at depth 6 (pid 7) -> should be discovered!
    tree[7]!.fds = ["/home/u/.gemini/antigravity-cli/conversations/at-depth-6.db-shm"];
    expect(plugin.discoverLiveSessionId(1)).toBe("at-depth-6");
  });

  test("buildInteractiveLaunch shadow-HOME isolation, D-Bus, credential scope, and router args", () => {
    const profileDir = path.join(spacesDir, "profiles", "antigravity", "work");
    const customShadow = path.join(tmpRoot, "custom-agy-shadow");
    mkdirSync(profileDir, { recursive: true });
    writeFileSync(
      path.join(profileDir, "profile.json"),
      JSON.stringify({
        agent: "antigravity",
        name: "work",
        enabled: true,
        shadow_home_path: customShadow,
        router: true,
      }),
    );

    // Put a settings.json in shadow home with a custom model
    const settingsDir = path.join(customShadow, ".gemini", "antigravity-cli");
    mkdirSync(settingsDir, { recursive: true });
    writeFileSync(
      path.join(settingsDir, "settings.json"),
      JSON.stringify({ model: "gemini-4-custom" }),
    );

    const plugin = new AntigravityPlugin({ homeDir, spacesDir });
    const launch = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "work",
      accessLevel: "auto",
      sessionId: "sess-99",
      env: {
        HOME: homeDir,
        MODEL_ROUTER_URL: "http://127.0.0.1:4123/",
        MODEL_ROUTER_API_KEY: "router-secret",
      },
    });

    const expectedHash = createHash("sha256")
      .update(customShadow)
      .digest("hex")
      .slice(0, 6);

    expect(launch.argv).toEqual([
      path.join(homeDir, ".local", "libexec", "antigravity", "agy"),
      "--mode",
      "accept-edits",
      "--conversation",
      "sess-99",
      "--model",
      "gemini-4-custom",
      "--settings",
      '{"modelProvider":"gemini"}',
    ]);
    expect(launch.shadowHome).toBe(customShadow);
    expect(launch.env).toEqual({
      HOME: customShadow,
      XDG_CONFIG_HOME: path.join(customShadow, ".config"),
      XDG_DATA_HOME: path.join(customShadow, ".local", "share"),
      XDG_STATE_HOME: path.join(customShadow, ".local", "state"),
      XDG_CACHE_HOME: path.join(customShadow, ".cache"),
      ANTIGRAVITY_GEMINI_DIR: path.join(customShadow, ".gemini"),
      GEMINI_HOME: path.join(customShadow, ".gemini"),
      DBUS_SESSION_BUS_ADDRESS: "disabled:",
      ANTIGRAVITY_CREDENTIAL_SCOPE: `harness-profile-${expectedHash}`,
      HARNESS_REAL_HOME: homeDir,
      BASH_ENV: path.join(spacesDir, "harness-restore-env.sh"),
      GOOGLE_GEMINI_BASE_URL: "http://127.0.0.1:4123",
      GEMINI_API_KEY: "router-secret",
    });
    expect(launch.unsetEnv).toEqual(
      expect.arrayContaining([
        "ANTIGRAVITY_APP_DATA_DIR",
        "ANTIGRAVITY_CONFIG_REL_DIR",
        "GOOGLE_API_KEY",
        "GOOGLE_APPLICATION_CREDENTIALS",
        "GOOGLE_CLOUD_PROJECT",
        "GEMINI_API_KEY",
      ]),
    );
  });

  test("buildInteractiveLaunch router default model, flag deduplication, access levels, and noRouter", () => {
    const plugin = new AntigravityPlugin({ homeDir, spacesDir });

    // Router enabled with no profile uses router shadow home and default model gemini-4-argon
    const routed = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      accessLevel: "read-only",
      env: { HOME: homeDir },
    });
    expect(routed.shadowHome).toBe(
      path.join(spacesDir, "profiles", "antigravity", "router", "home"),
    );
    expect(routed.argv).toContain("--sandbox");
    expect(routed.argv).toContain("--model");
    expect(routed.argv[routed.argv.indexOf("--model") + 1]).toBe("gemini-4-argon");
    expect(routed.argv).toContain("--settings");
    expect(routed.env.DBUS_SESSION_BUS_ADDRESS).toBe("disabled:");

    // Explicit --model and -settings prevent router duplicates
    const deduped = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      accessLevel: "full",
      model: "explicit-model",
      extraArgs: ["-settings", '{"custom":true}'],
      env: { HOME: homeDir },
    });
    expect(deduped.argv.filter((a) => a === "--model")).toHaveLength(1);
    expect(deduped.argv).not.toContain("--settings");
    expect(deduped.argv).toContain("--yolo");

    // noRouter without profile disables shadow-HOME and router env
    const direct = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      noRouter: true,
      env: { HOME: homeDir },
    });
    expect(direct.shadowHome).toBeUndefined();
    expect(direct.env.GOOGLE_GEMINI_BASE_URL).toBeUndefined();
    expect(direct.env.HOME).toBeUndefined();
    expect(direct.unsetEnv).toContain("GOOGLE_GEMINI_BASE_URL");
    expect(direct.argv).not.toContain("--settings");
  });

  test("SQLite conversation_summaries.db listing and resolveVendorSession", () => {
    const plugin = new AntigravityPlugin({ homeDir, spacesDir });
    const dbPath = antigravitySummaryDbPath(homeDir);
    expect(plugin.storePaths(homeDir)).toEqual([dbPath]);

    // Missing DB -> storeAvailable: false
    expect(
      plugin.resolveVendorSession("antigravity", "/repo/a", "", "", new Set()),
    ).toEqual({ sessionId: "", storeAvailable: false });

    mkdirSync(path.dirname(dbPath), { recursive: true });
    const db = new Database(dbPath);
    db.exec(`
      CREATE TABLE conversation_summaries (
        conversation_id TEXT PRIMARY KEY,
        parent_conversation_id TEXT,
        title TEXT,
        workspace_uris TEXT,
        model TEXT,
        created_at INTEGER,
        updated_at INTEGER,
        message_count INTEGER
      );
    `);

    const insert = db.query(`
      INSERT INTO conversation_summaries
      (conversation_id, parent_conversation_id, title, workspace_uris, model, created_at, updated_at, message_count)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `);

    insert.run(
      "conv-older",
      "",
      "Fix login bug",
      JSON.stringify(["file:///repo/a"]),
      "gemini-4-argon",
      1700000000,
      1700001000,
      4,
    );
    insert.run(
      "conv-newer",
      null,
      "Refactor plugins",
      JSON.stringify(["file:///repo/a"]),
      "gemini-4-argon",
      1700002000,
      1700003000,
      8,
    );
    insert.run(
      "conv-subagent",
      "conv-newer",
      "Child subagent task",
      JSON.stringify(["file:///repo/a"]),
      "gemini-4-argon",
      1700004000,
      1700005000,
      2,
    );
    insert.run(
      "conv-other-repo",
      "",
      "Other workspace",
      JSON.stringify(["file:///repo/b"]),
      "gemini-4-argon",
      1700006000,
      1700007000,
      3,
    );
    db.close();

    const list = plugin.listConversations({ homeDir });
    expect(list.map((r) => r.id)).toEqual([
      "conv-other-repo",
      "conv-newer",
      "conv-older",
    ]);
    expect(list[1]).toMatchObject({
      id: "conv-newer",
      agent: "antigravity",
      app: "antigravity",
      cwd: "/repo/a",
      title: "Refactor plugins",
      model: "gemini-4-argon",
      sourcePath: dbPath,
      resumeTarget: "conv-newer",
      messageCount: 8,
    });

    // Most recent for /repo/a
    expect(
      plugin.resolveVendorSession("antigravity", "/repo/a", "", "", new Set()),
    ).toEqual({ sessionId: "conv-newer", storeAvailable: true });

    // Title match prefers conv-older
    expect(
      plugin.resolveVendorSession(
        "antigravity",
        "/repo/a",
        "",
        "fix login bug",
        new Set(),
      ),
    ).toEqual({ sessionId: "conv-older", storeAvailable: true });

    // Exclude conv-newer -> falls back to conv-older
    expect(
      plugin.resolveVendorSession(
        "antigravity",
        "/repo/a",
        "",
        "",
        new Set(["conv-newer"]),
      ),
    ).toEqual({ sessionId: "conv-older", storeAvailable: true });
  });
});

describe("GeminiPlugin & GeminiHeadlessExecutor", () => {
  let tmpRoot: string;
  let homeDir: string;
  let spacesDir: string;

  beforeEach(() => {
    tmpRoot = mkdtempSync(path.join(os.tmpdir(), "spaces-gemini-test-"));
    homeDir = path.join(tmpRoot, "home");
    spacesDir = path.join(tmpRoot, "spaces");
    mkdirSync(homeDir, { recursive: true });
    mkdirSync(spacesDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(tmpRoot, { recursive: true, force: true });
  });

  test("GeminiPlugin spec, shadow var, router env, local dist resolution, and 4 access levels", () => {
    const plugin = new GeminiPlugin({ homeDir, spacesDir });
    expect(plugin.spec).toEqual({
      id: "gemini",
      aliases: ["gemini-cli"],
      displayApp: "gemini",
      cliDisplayApp: "gemini",
      binary: "gemini",
      screenManifestId: "gemini",
      oscAuthority: false,
      positionalResume: false,
      uniqueExplicitSessions: false,
      titlePrefixes: [],
      mcpTargets: [".gemini/settings.json"],
    });

    // Create a profile with shadowHomePath
    const profileDir = path.join(spacesDir, "profiles", "gemini", "alt");
    const shadowHome = path.join(tmpRoot, "gemini-shadow");
    mkdirSync(profileDir, { recursive: true });
    writeFileSync(
      path.join(profileDir, "profile.json"),
      JSON.stringify({
        agent: "gemini",
        name: "alt",
        enabled: true,
        shadow_home_path: shadowHome,
        router: true,
      }),
    );

    // Without local dist -> falls back to `gemini`
    const fallbackLaunch = plugin.buildInteractiveLaunch({
      cwd: "/repo/x",
      profile: "alt",
      accessLevel: "read-only",
      env: {
        HOME: homeDir,
        MODEL_ROUTER_URL: "http://127.0.0.1:4555",
        MODEL_ROUTER_API_KEY: "gem-key",
      },
    });
    expect(fallbackLaunch.argv).toEqual(["gemini", "--approval-mode", "plan"]);
    expect(fallbackLaunch.env).toEqual({
      GEMINI_CLI_HOME: shadowHome,
      GOOGLE_GEMINI_BASE_URL: "http://127.0.0.1:4555",
      GEMINI_MODEL_ROUTER: "1",
      GEMINI_API_KEY: "gem-key",
    });

    // With GEMINI_CLI_REPO dist/index.js present -> resolves `node <dist>`
    const customRepo = path.join(tmpRoot, "my-gemini-cli");
    const distIndex = path.join(customRepo, "packages", "cli", "dist", "index.js");
    mkdirSync(path.dirname(distIndex), { recursive: true });
    writeFileSync(distIndex, "// entry");

    const localLaunch = plugin.buildInteractiveLaunch({
      cwd: "/repo/x",
      accessLevel: "auto",
      env: { HOME: homeDir, GEMINI_CLI_REPO: customRepo },
    });
    expect(localLaunch.argv).toEqual([
      "node",
      distIndex,
      "--approval-mode",
      "auto_edit",
    ]);

    const fullLaunch = plugin.buildInteractiveLaunch({
      cwd: "/repo/x",
      accessLevel: "full",
      noRouter: true,
      env: { HOME: homeDir },
    });
    expect(fullLaunch.argv).toEqual(["gemini", "--yolo"]);
    expect(fullLaunch.env.GEMINI_MODEL_ROUTER).toBeUndefined();
    expect(fullLaunch.unsetEnv).toEqual(
      expect.arrayContaining([
        "GOOGLE_GEMINI_BASE_URL",
        "GEMINI_MODEL_ROUTER",
        "GEMINI_API_KEY",
      ]),
    );

    // Default startHeadless returns GeminiHeadlessExecutor
    const session = plugin.startHeadless({
      threadId: "thr-1",
      cwd: "/repo/x",
      env: { HOME: homeDir },
    });
    expect(session).toBeInstanceOf(GeminiHeadlessExecutor);
    expect(session.agent).toBe("gemini");
    expect(session.threadId).toBe("thr-1");
  });

  test("GeminiHeadlessExecutor parses stream-json events, usage, resume, and stopAndFlush", async () => {
    const spawnedArgvs: string[][] = [];
    let callCount = 0;

    const launch: InteractiveLaunchSpec = {
      argv: ["node", "/opt/gemini-cli/packages/cli/dist/index.js", "--yolo"],
      env: { GEMINI_MODEL_ROUTER: "1" },
      unsetEnv: ["UNUSED_VAR"],
      cwd: "/repo/x",
    };

    const exec = new GeminiHeadlessExecutor({
      threadId: "thr-gem",
      launch,
      spawn: (argv) => {
        spawnedArgvs.push(argv);
        callCount += 1;
        if (callCount === 1) {
          return makeMockProc({
            stdoutChunks: [
              '{"type":"init","session_id":"gem-sess-1","model":"gemini-2.5-pro"}\n{"type":"mess',
              'age","role":"assistant","content":"Hello "}\n',
              '{"type":"message","role":"model","content":"world!"}\n',
              '{"type":"tool_use","tool_id":"t1","tool_name":"read_file","parameters":{"path":"a.ts"}}\n',
              '{"type":"tool_result","tool_id":"t1","output":"ok"}\n',
              '{"type":"result","status":"success","stats":{"input_tokens":120,"output_tokens":45,"cached":30,"cost_usd":0.012}}\n',
            ],
          }).proc;
        }
        return makeMockProc({
          stdoutChunks: [
            '{"type":"message","role":"assistant","content":"Follow up"}\n',
            '{"type":"result","status":"success","usage":{"input_tokens":50,"output_tokens":10}}\n',
          ],
        }).proc;
      },
    });

    const events: HeadlessTurnEvent[] = [];
    const res1 = await exec.sendTurn("Hi Gemini", (e) => events.push(e));

    expect(spawnedArgvs[0]).toEqual([
      "node",
      "/opt/gemini-cli/packages/cli/dist/index.js",
      "--yolo",
      "-p",
      "Hi Gemini",
      "--output-format",
      "stream-json",
    ]);
    expect(exec.getSessionId()).toBe("gem-sess-1");
    expect(res1).toEqual({
      sessionId: "gem-sess-1",
      replyPreview: "Hello world!",
      model: "gemini-2.5-pro",
      usage: {
        input_tokens: 120,
        output_tokens: 45,
        reasoning_tokens: 0,
        cache_read_tokens: 30,
        cache_write_tokens: 0,
        cost_usd: 0.012,
        cost_priced: true,
      },
    });
    expect(events.map((e) => e.type)).toEqual([
      "session_init",
      "text_delta",
      "text_delta",
      "tool_use",
      "tool_result",
      "completed",
    ]);

    // Second turn automatically passes --resume gem-sess-1
    const res2 = await exec.sendTurn("Next");
    expect(spawnedArgvs[1]).toEqual([
      "node",
      "/opt/gemini-cli/packages/cli/dist/index.js",
      "--yolo",
      "--resume",
      "gem-sess-1",
      "-p",
      "Next",
      "--output-format",
      "stream-json",
    ]);
    expect(res2.replyPreview).toBe("Follow up");

    const flushed = await exec.stopAndFlush();
    expect(flushed).toEqual({
      sessionId: "gem-sess-1",
      usage: {
        input_tokens: 50,
        output_tokens: 10,
        reasoning_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd: 0,
        cost_priced: false,
      },
    });
  });

  test("GeminiHeadlessExecutor handles error result, non-zero exit, and cancel escalation", async () => {
    const launch: InteractiveLaunchSpec = {
      argv: ["gemini", "-p", "old", "--output-format", "text"],
      env: {},
      unsetEnv: [],
      cwd: "/repo/x",
    };

    // Argv deduplication check
    expect(buildGeminiHeadlessArgv(launch.argv, "fresh", "s-1")).toEqual([
      "gemini",
      "--resume",
      "s-1",
      "-p",
      "fresh",
      "--output-format",
      "stream-json",
    ]);

    // Result error event
    const errExec = new GeminiHeadlessExecutor({
      threadId: "thr-err",
      launch,
      spawn: () =>
        makeMockProc({
          stdoutChunks: [
            '{"type":"result","status":"error","error":{"code":"quota","message":"Rate limit"}}\n',
          ],
        }).proc,
    });
    await expect(errExec.sendTurn("go")).rejects.toThrow(GeminiHeadlessError);

    // Non-zero exit with stderr
    const exitExec = new GeminiHeadlessExecutor({
      threadId: "thr-exit",
      launch,
      spawn: () =>
        makeMockProc({
          stdoutChunks: [],
          stderrChunks: ["fatal auth error"],
          exitCode: 2,
        }).proc,
    });
    await expect(exitExec.sendTurn("go")).rejects.toThrow("fatal auth error");

    // Cancel escalates SIGTERM -> SIGKILL when process ignores SIGTERM
    const signals: Array<NodeJS.Signals | number | undefined> = [];
    let closeStdout!: () => void;
    let resolveExit!: (code: number) => void;
    const stdout = new ReadableStream<Uint8Array>({
      start(controller) {
        closeStdout = () => controller.close();
      },
    });
    const exited = new Promise<number>((resolve) => {
      resolveExit = resolve;
    });

    const cancelExec = new GeminiHeadlessExecutor({
      threadId: "thr-cancel",
      launch,
      cancelGraceMs: 20,
      spawn: () => ({
        stdout,
        exited,
        kill(sig) {
          signals.push(sig);
          if (sig === "SIGKILL") {
            closeStdout();
            resolveExit(137);
          }
        },
      }),
    });

    const turnPromise = cancelExec.sendTurn("hang");
    expect(cancelExec.isRunning()).toBe(true);
    await cancelExec.cancel("user_abort");
    await expect(turnPromise).rejects.toThrow("user_abort");
    expect(signals).toEqual(["SIGTERM", "SIGKILL"]);
    expect(cancelExec.isRunning()).toBe(false);
  });
});

import { afterEach, describe, expect, test } from "bun:test";
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  CodexHeadlessError,
  CodexHeadlessExecutor,
  buildCodexHeadlessArgv,
  type CodexSpawnFn,
  type SpawnedProcess,
} from "../../executors/codex";
import { HarnessPluginRegistry } from "../registry";
import type { HeadlessSession, HeadlessTurnEvent, InteractiveLaunchSpec } from "../types";
import { CodexPlugin } from "./codex";

const tempDirs: string[] = [];

function makeTempDir(prefix = "codex-plugin-test-"): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

function writeFile(filePath: string, content: string): void {
  mkdirSync(path.dirname(filePath), { recursive: true });
  writeFileSync(filePath, content, "utf8");
}

afterEach(() => {
  while (tempDirs.length > 0) {
    rmSync(tempDirs.pop()!, { recursive: true, force: true });
  }
});

interface FakeProcessController {
  proc: SpawnedProcess;
  stdinEnded: boolean;
  signals: Array<NodeJS.Signals | number | undefined>;
  emitStdout(text: string): void;
  emitStdoutJson(obj: unknown): void;
  emitStderr(text: string): void;
  closeStdout(): void;
  exit(code: number): void;
}

interface FakeProcessOptions {
  ignoreSigint?: boolean;
  ignoreSigterm?: boolean;
  closeStdoutOnExit?: boolean;
}

function createFakeProcess(opts: FakeProcessOptions = {}): FakeProcessController {
  const encoder = new TextEncoder();
  let stdinEnded = false;
  const signals: Array<NodeJS.Signals | number | undefined> = [];

  let stdoutController!: ReadableStreamDefaultController<Uint8Array>;
  let stdoutClosed = false;
  const stdout = new ReadableStream<Uint8Array>({
    start(controller) {
      stdoutController = controller;
    },
    cancel() {
      stdoutClosed = true;
    },
  });

  let stderrController!: ReadableStreamDefaultController<Uint8Array>;
  let stderrClosed = false;
  const stderr = new ReadableStream<Uint8Array>({
    start(controller) {
      stderrController = controller;
    },
    cancel() {
      stderrClosed = true;
    },
  });

  let resolveExited!: (code: number) => void;
  let exitedResolved = false;
  const exited = new Promise<number>((resolve) => {
    resolveExited = resolve;
  });

  const ctrl: FakeProcessController = {
    get stdinEnded() {
      return stdinEnded;
    },
    signals,
    emitStdout(text: string) {
      if (!stdoutClosed) {
        stdoutController.enqueue(encoder.encode(text));
      }
    },
    emitStdoutJson(obj: unknown) {
      ctrl.emitStdout(`${JSON.stringify(obj)}\n`);
    },
    emitStderr(text: string) {
      if (!stderrClosed) {
        stderrController.enqueue(encoder.encode(text));
      }
    },
    closeStdout() {
      if (!stdoutClosed) {
        stdoutClosed = true;
        try {
          stdoutController.close();
        } catch {
          // Already closed.
        }
      }
    },
    exit(code: number) {
      if (opts.closeStdoutOnExit ?? true) {
        ctrl.closeStdout();
      }
      if (!stderrClosed) {
        stderrClosed = true;
        try {
          stderrController.close();
        } catch {
          // Already closed.
        }
      }
      if (!exitedResolved) {
        exitedResolved = true;
        resolveExited(code);
      }
    },
    proc: {
      stdin: {
        write(): number {
          return 0;
        },
        end(): void {
          stdinEnded = true;
        },
      },
      stdout,
      stderr,
      exited,
      kill(signal?: NodeJS.Signals | number): void {
        signals.push(signal);
        if (signal === "SIGINT") {
          if (!opts.ignoreSigint) {
            queueMicrotask(() => ctrl.exit(130));
          }
          return;
        }
        if (signal === "SIGTERM") {
          if (!opts.ignoreSigterm) {
            queueMicrotask(() => ctrl.exit(143));
          }
          return;
        }
        queueMicrotask(() => ctrl.exit(1));
      },
    },
  };

  return ctrl;
}

function baseLaunch(overrides: Partial<InteractiveLaunchSpec> = {}): InteractiveLaunchSpec {
  return {
    argv: ["/home/dev/.local/bin/codex"],
    env: {},
    unsetEnv: [],
    cwd: "/tmp/repo",
    ...overrides,
  };
}

describe("CodexPlugin spec & registry integration", () => {
  test("exposes canonical Codex spec, resumeArgs, storePaths, and uniqueExplicitSessions policy", () => {
    const home = makeTempDir("codex-spec-");
    const plugin = new CodexPlugin({ homeDir: home });

    expect(plugin.spec).toEqual({
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
    });

    expect(plugin.resumeArgs("019a-1234")).toEqual(["resume", "019a-1234"]);
    expect(plugin.resumeArgs("   ")).toEqual([]);
    expect(plugin.storePaths(home)).toEqual([
      path.join(home, ".codex", "sessions"),
      path.join(home, ".codex", "archived_sessions"),
    ]);

    const registry = new HarnessPluginRegistry();
    registry.register(plugin);
    expect(registry.get("codex")).toBe(plugin);
    expect(registry.get("openai-codex")).toBe(plugin);

    const policy = registry.asResumePolicy();
    expect(policy.binaryName?.("openai-codex")).toBe("codex");
    expect(policy.resumeArgs?.("codex", "sid-1")).toEqual(["resume", "sid-1"]);
    expect(policy.positionalResume?.("codex")).toBe(true);
    expect(policy.uniqueExplicitSessions?.("codex")).toBe(true);
  });
});

describe("CodexPlugin buildInteractiveLaunch", () => {
  test("injects 9 router -c TOML overrides, CODEX_HOME shadow home, access levels, and --no-router", () => {
    const home = makeTempDir("codex-launch-");
    const spacesDir = path.join(home, ".spaces");

    writeFile(
      path.join(home, ".config", "ai", "model-router.env"),
      "MODEL_ROUTER_URL=http://127.0.0.1:4321/\nMODEL_ROUTER_API_KEY=tok-codex\n",
    );

    writeFile(
      path.join(spacesDir, "profiles", "codex", "work", "profile.json"),
      JSON.stringify({
        agent: "codex",
        displayName: "Work Codex",
        binaryPath: "/opt/bin/codex-work",
        prependArgs: ["--profile-arg"],
        env: { EXTRA_CODEX: "1", HOME: "/should-be-ignored" },
        router: true,
      }),
    );

    writeFile(
      path.join(spacesDir, "profiles", "codex", "direct", "profile.json"),
      JSON.stringify({
        agent: "codex",
        displayName: "Direct",
        router: false,
      }),
    );

    writeFile(
      path.join(spacesDir, "profiles", "codex", "disabled", "profile.json"),
      JSON.stringify({ agent: "codex", enabled: false }),
    );

    const plugin = new CodexPlugin({ homeDir: home, spacesDir });

    // Default bare launch with router enabled + read-only access level
    const bare = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      accessLevel: "read-only",
      model: "gpt-5-codex",
      effort: "high",
      sessionId: "sess-123",
      extraArgs: ["--search"],
      env: {},
    });

    expect(bare.argv).toEqual([
      path.join(home, ".local", "bin", "codex"),
      "-c",
      'model_provider="model_router"',
      "-c",
      'model="gpt-6-luna"',
      "-c",
      'service_tier="default"',
      "-c",
      'model_providers.model_router.name="model_router"',
      "-c",
      'model_providers.model_router.base_url="http://127.0.0.1:4321/v1"',
      "-c",
      'model_providers.model_router.env_key="MODEL_ROUTER_API_KEY"',
      "-c",
      'model_providers.model_router.wire_api="responses"',
      "-c",
      "model_providers.model_router.requires_openai_auth=false",
      "-c",
      "model_providers.model_router.supports_websockets=false",
      "--sandbox",
      "read-only",
      "--ask-for-approval",
      "on-request",
      "--model",
      "gpt-5-codex",
      "-c",
      'model_reasoning_effort="high"',
      "resume",
      "sess-123",
      "--search",
    ]);
    expect(bare.env).toEqual({
      MODEL_ROUTER_API_KEY: "tok-codex",
    });
    expect(bare.env.HOME).toBeUndefined();
    expect(bare.unsetEnv).toEqual(["HARNESS_AGENT", "HARNESS_PROFILE"]);
    expect(bare.configDir).toBe(path.join(home, ".codex"));

    // Profiled launch with auto access level and CODEX_HOME shadow home
    const expectedShadow = path.join(spacesDir, "profiles", "codex", "work", "home");
    const profiled = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "work",
      accessLevel: "auto",
      env: {},
    });
    expect(profiled.argv[0]).toBe("/opt/bin/codex-work");
    expect(profiled.argv[1]).toBe("--profile-arg");
    expect(profiled.argv).toContain("--full-auto");
    expect(profiled.shadowHome).toBe(expectedShadow);
    expect(profiled.configDir).toBe(expectedShadow);
    expect(profiled.env).toEqual({
      MODEL_ROUTER_API_KEY: "tok-codex",
      CODEX_HOME: expectedShadow,
      HARNESS_PROFILE: "work",
      EXTRA_CODEX: "1",
    });
    expect(profiled.env.HOME).toBeUndefined();
    expect(profiled.unsetEnv).toEqual(["HARNESS_AGENT"]);

    // noRouter=true with full access level unsets OPENAI_API_KEY and OPENAI_BASE_URL
    const noRouter = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "work",
      accessLevel: "full",
      noRouter: true,
      env: {},
    });
    expect(noRouter.argv).toEqual([
      "/opt/bin/codex-work",
      "--profile-arg",
      "--dangerously-bypass-approvals-and-sandbox",
    ]);
    expect(noRouter.env.MODEL_ROUTER_API_KEY).toBeUndefined();
    expect(noRouter.env.CODEX_HOME).toBe(expectedShadow);
    expect(noRouter.unsetEnv).toEqual([
      "HARNESS_AGENT",
      "OPENAI_API_KEY",
      "OPENAI_BASE_URL",
    ]);

    // profile.router=false with default access level
    const direct = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "direct",
      accessLevel: "default",
      env: {},
    });
    expect(direct.argv).toEqual([path.join(home, ".local", "bin", "codex")]);
    expect(direct.unsetEnv).toEqual([
      "HARNESS_AGENT",
      "OPENAI_API_KEY",
      "OPENAI_BASE_URL",
    ]);

    // Disabled profile throws
    expect(() =>
      plugin.buildInteractiveLaunch({ cwd: "/repo/app", profile: "disabled" }),
    ).toThrow(/profile disabled: codex\/disabled/);
  });
});

describe("CodexPlugin rollout JSONL store (listConversations, resolveVendorSession, parseLog, readDelta)", () => {
  test("listConversations includes active & archived sessions and merges session_index.jsonl titles", () => {
    const home = makeTempDir("codex-list-");
    const codexDir = path.join(home, ".codex");

    const activePath = path.join(
      codexDir,
      "sessions",
      "2026",
      "11",
      "28",
      "rollout-2026-11-28T10-00-00-019a0000-0000-7000-8000-000000000001.jsonl",
    );
    const archivedPath = path.join(
      codexDir,
      "archived_sessions",
      "2026",
      "11",
      "20",
      "rollout-2026-11-20T09-00-00-019a0000-0000-7000-8000-000000000002.jsonl",
    );
    const deletedPath = path.join(
      codexDir,
      "sessions",
      "2026",
      "11",
      "21",
      "rollout-2026-11-21T09-00-00-019a0000-0000-7000-8000-000000000003.jsonl",
    );

    writeFile(
      activePath,
      [
        JSON.stringify({
          timestamp: "2026-11-28T10:00:00Z",
          type: "session_meta",
          payload: {
            id: "019a0000-0000-7000-8000-000000000001",
            cwd: "/repo/spaces",
          },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T10:00:01Z",
          type: "turn_context",
          payload: { cwd: "/repo/spaces", model: "gpt-5-codex" },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T10:00:02Z",
          type: "event_msg",
          payload: { type: "user_message", message: "Implement Codex plugin" },
        }),
        // Duplicate response_item for same user message should be deduplicated
        JSON.stringify({
          timestamp: "2026-11-28T10:00:02Z",
          type: "response_item",
          payload: {
            type: "message",
            role: "user",
            content: [{ type: "input_text", text: "Implement Codex plugin" }],
          },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T10:00:05Z",
          type: "event_msg",
          payload: { type: "agent_message", message: "Codex plugin implemented." },
        }),
      ].join("\n") + "\n",
    );

    writeFile(
      archivedPath,
      [
        JSON.stringify({
          timestamp: "2026-11-20T09:00:00Z",
          type: "session_meta",
          payload: {
            id: "019a0000-0000-7000-8000-000000000002",
            cwd: "/repo/legacy",
          },
        }),
        JSON.stringify({
          timestamp: "2026-11-20T09:00:05Z",
          type: "response_item",
          payload: {
            type: "message",
            role: "user",
            content: [{ type: "input_text", text: "Archive me" }],
          },
        }),
      ].join("\n") + "\n",
    );

    writeFile(
      deletedPath,
      JSON.stringify({
        timestamp: "2026-11-21T09:00:00Z",
        type: "session_meta",
        payload: { id: "019a0000-0000-7000-8000-000000000003", cwd: "/repo/spaces" },
      }) + "\n",
    );

    writeFile(
      path.join(codexDir, "session_index.jsonl"),
      [
        JSON.stringify({
          id: "019a0000-0000-7000-8000-000000000001",
          thread_name: "Codex Plugin Track B",
          updated_at: "2026-11-28T10:00:06Z",
        }),
        JSON.stringify({
          id: "019a0000-0000-7000-8000-000000000002",
          thread_name: "Old Archived Thread",
        }),
        JSON.stringify({
          id: "019a0000-0000-7000-8000-000000000003",
          deleted: true,
        }),
      ].join("\n") + "\n",
    );

    const plugin = new CodexPlugin({ homeDir: home });

    const activeOnly = plugin.listConversations({ homeDir: home });
    expect(activeOnly).toHaveLength(1);
    expect(activeOnly[0]).toMatchObject({
      id: "019a0000-0000-7000-8000-000000000001",
      agent: "codex",
      app: "codex",
      cwd: "/repo/spaces",
      title: "Codex Plugin Track B",
      firstMessage: "Implement Codex plugin",
      lastMessage: "Codex plugin implemented.",
      model: "gpt-5-codex",
      resumeTarget: "019a0000-0000-7000-8000-000000000001",
      messageCount: 2,
    });
    expect(activeOnly[0]!.archived).toBeUndefined();

    const withArchived = plugin.listConversations({ homeDir: home, includeArchived: true });
    expect(withArchived).toHaveLength(2);
    const archivedRec = withArchived.find(
      (r) => r.id === "019a0000-0000-7000-8000-000000000002",
    );
    expect(archivedRec).toMatchObject({
      title: "Old Archived Thread",
      cwd: "/repo/legacy",
      firstMessage: "Archive me",
      archived: true,
      messageCount: 1,
    });
  });

  test("parseLog extracts user and assistant messages from event_msg and response_item records", () => {
    const home = makeTempDir("codex-parse-");
    const rolloutPath = path.join(home, "rollout-sample.jsonl");

    writeFile(
      rolloutPath,
      [
        JSON.stringify({
          timestamp: "2026-11-28T11:00:00Z",
          type: "turn_context",
          payload: { model: "gpt-5-codex" },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T11:00:01Z",
          type: "response_item",
          payload: {
            type: "message",
            role: "developer",
            content: [{ type: "input_text", text: "System instructions" }],
          },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T11:00:02Z",
          type: "event_msg",
          payload: { type: "user_message", message: "Hello Codex" },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T11:00:03Z",
          type: "response_item",
          payload: {
            type: "message",
            role: "assistant",
            content: [
              { type: "reasoning", text: "hidden thought" },
              { type: "output_text", text: "Hello back!" },
            ],
          },
        }),
        JSON.stringify({
          timestamp: "2026-11-28T11:00:04Z",
          type: "event_msg",
          payload: { type: "agent_message", message: "Hello back!" },
        }),
        "not-json-line",
      ].join("\n") + "\n",
    );

    const plugin = new CodexPlugin({ homeDir: home });
    const parsed = plugin.parseLog(rolloutPath);
    expect(parsed).toEqual([
      {
        role: "user",
        text: "Hello Codex",
        timestampMs: Date.parse("2026-11-28T11:00:02Z"),
      },
      {
        role: "assistant",
        text: "Hello back!",
        timestampMs: Date.parse("2026-11-28T11:00:03Z"),
        model: "gpt-5-codex",
      },
    ]);
  });

  test("resolveVendorSession matches CWD + title/first-message, honors exclude & profile isolation", () => {
    const home = makeTempDir("codex-vendor-");
    const spacesDir = path.join(home, ".spaces");
    const cwd = "/work/spaces";
    const codexDir = path.join(home, ".codex");

    const sOlder = path.join(codexDir, "sessions", "2026", "11", "24", "rollout-older.jsonl");
    const sNewest = path.join(codexDir, "sessions", "2026", "11", "25", "rollout-newest.jsonl");
    const sOtherCwd = path.join(codexDir, "sessions", "2026", "11", "26", "rollout-other.jsonl");

    writeFile(
      sOlder,
      [
        JSON.stringify({ type: "session_meta", payload: { id: "sid-older", cwd } }),
        JSON.stringify({
          type: "event_msg",
          payload: { type: "user_message", message: "Fix auth middleware" },
        }),
      ].join("\n") + "\n",
    );
    writeFile(
      sNewest,
      [
        JSON.stringify({ type: "session_meta", payload: { id: "sid-newest", cwd } }),
        JSON.stringify({
          type: "event_msg",
          payload: { type: "user_message", message: "Refactor daemon" },
        }),
      ].join("\n") + "\n",
    );
    writeFile(
      sOtherCwd,
      [
        JSON.stringify({ type: "session_meta", payload: { id: "sid-other", cwd: "/work/other" } }),
        JSON.stringify({
          type: "event_msg",
          payload: { type: "user_message", message: "Fix auth middleware" },
        }),
      ].join("\n") + "\n",
    );
    writeFile(
      path.join(codexDir, "session_index.jsonl"),
      JSON.stringify({ id: "sid-older", thread_name: "Auth Middleware Title" }) + "\n",
    );

    const t1 = new Date("2026-11-24T10:00:00Z");
    const t2 = new Date("2026-11-25T10:00:00Z");
    const t3 = new Date("2026-11-26T10:00:00Z");
    utimesSync(sOlder, t1, t1);
    utimesSync(sNewest, t2, t2);
    utimesSync(sOtherCwd, t3, t3);

    // Profiled isolated store
    writeFile(
      path.join(spacesDir, "profiles", "codex", "work", "profile.json"),
      JSON.stringify({ agent: "codex", displayName: "Work" }),
    );
    const profRollout = path.join(
      spacesDir,
      "profiles",
      "codex",
      "work",
      "home",
      "sessions",
      "rollout-prof-1.jsonl",
    );
    writeFile(
      profRollout,
      JSON.stringify({ type: "session_meta", payload: { id: "sid-prof-1", cwd } }) + "\n",
    );

    const plugin = new CodexPlugin({ homeDir: home, spacesDir });

    // Match by session_index title beats newer session
    expect(
      plugin.resolveVendorSession("codex", cwd, "", "auth middleware title", new Set()),
    ).toEqual({ sessionId: "sid-older", storeAvailable: true });

    // Match by first user message
    expect(
      plugin.resolveVendorSession("codex", cwd, "", "fix auth middleware", new Set()),
    ).toEqual({ sessionId: "sid-older", storeAvailable: true });

    // Empty title returns newest unclaimed matching CWD (ignoring other CWD)
    expect(plugin.resolveVendorSession("codex", cwd, "", "", new Set())).toEqual({
      sessionId: "sid-newest",
      storeAvailable: true,
    });

    // Exclude skips sid-newest and returns sid-older
    expect(
      plugin.resolveVendorSession("codex", cwd, "", "", new Set(["sid-newest"])),
    ).toEqual({ sessionId: "sid-older", storeAvailable: true });

    // Profiled lookup resolves inside shadow home
    expect(plugin.resolveVendorSession("codex", cwd, "work", "", new Set())).toEqual({
      sessionId: "sid-prof-1",
      storeAvailable: true,
    });

    // Missing store returns storeAvailable: false
    const emptyHome = makeTempDir("codex-empty-");
    const emptyPlugin = new CodexPlugin({ homeDir: emptyHome });
    expect(emptyPlugin.resolveVendorSession("codex", cwd, "", "", new Set())).toEqual({
      sessionId: "",
      storeAvailable: false,
    });
  });

  test("readDelta tails rollout JSONL incrementally with partial lines, usage, and truncation reset", () => {
    const home = makeTempDir("codex-delta-");
    const codexDir = path.join(home, ".codex");
    const sid = "019a0000-0000-7000-8000-000000000099";
    const rolloutPath = path.join(
      codexDir,
      "sessions",
      "2026",
      "11",
      "28",
      `rollout-2026-11-28T12-00-00-${sid}.jsonl`,
    );

    writeFile(
      path.join(codexDir, "session_index.jsonl"),
      JSON.stringify({ id: sid, thread_name: "Incremental Tail Test" }) + "\n",
    );

    const line1 =
      JSON.stringify({
        timestamp: "2026-11-28T12:00:00Z",
        type: "session_meta",
        payload: { id: sid, cwd: "/repo/app" },
      }) + "\n";
    const line2 =
      JSON.stringify({
        timestamp: "2026-11-28T12:00:01Z",
        type: "turn_context",
        payload: { model: "gpt-5-codex" },
      }) + "\n";
    const line3 =
      JSON.stringify({
        timestamp: "2026-11-28T12:00:02Z",
        type: "event_msg",
        payload: { type: "user_message", message: "Stream UTF-8 — 日本語" },
      }) + "\n";
    const line4Full =
      JSON.stringify({
        timestamp: "2026-11-28T12:00:05Z",
        type: "event_msg",
        payload: { type: "agent_message", message: "Completed response." },
      }) + "\n";
    const line5 =
      JSON.stringify({
        timestamp: "2026-11-28T12:00:06Z",
        type: "event_msg",
        payload: {
          type: "token_count",
          info: {
            total_token_usage: {
              input_tokens: 200,
              cached_input_tokens: 50,
              output_tokens: 40,
              reasoning_output_tokens: 10,
            },
          },
        },
      }) + "\n";

    const splitAt = Math.floor(line4Full.length / 2);
    const partA = line4Full.slice(0, splitAt);
    writeFile(rolloutPath, line1 + line2 + line3 + partA);

    const plugin = new CodexPlugin({ homeDir: home });
    const d1 = plugin.readDelta(sid, "/repo/app", undefined, codexDir);
    expect(d1.title).toBe("Incremental Tail Test");
    expect(d1.model).toBe("gpt-5-codex");
    expect(d1.turns).toEqual([
      {
        role: "user",
        text: "Stream UTF-8 — 日本語",
        timestampMs: Date.parse("2026-11-28T12:00:02Z"),
      },
    ]);
    expect(d1.nextCursor.partialLine).toBe(partA);
    expect(d1.nextCursor.byteOffset).toBe(Buffer.byteLength(line1 + line2 + line3 + partA, "utf8"));

    // Append remainder of line 4 and token_count line 5
    writeFile(rolloutPath, line1 + line2 + line3 + line4Full + line5);
    const d2 = plugin.readDelta(sid, "/repo/app", d1.nextCursor, codexDir);
    expect(d2.nextCursor.partialLine).toBeUndefined();
    expect(d2.turns).toEqual([
      {
        role: "assistant",
        text: "Completed response.",
        timestampMs: Date.parse("2026-11-28T12:00:05Z"),
      },
    ]);
    expect(d2.usage).toEqual({
      input_tokens: 200,
      output_tokens: 40,
      reasoning_tokens: 10,
      cache_read_tokens: 50,
      cache_write_tokens: 0,
      cost_usd: 0,
      cost_priced: false,
    });

    // No new bytes -> empty delta
    const d3 = plugin.readDelta(sid, "/repo/app", d2.nextCursor, codexDir);
    expect(d3.turns).toEqual([]);
    expect(d3.nextCursor.byteOffset).toBe(d2.nextCursor.byteOffset);

    // Truncation resets offset to 0
    const truncated =
      JSON.stringify({
        type: "event_msg",
        payload: { type: "user_message", message: "After truncation" },
      }) + "\n";
    writeFile(rolloutPath, truncated);
    const d4 = plugin.readDelta(sid, "/repo/app", d3.nextCursor, codexDir);
    expect(d4.turns).toEqual([{ role: "user", text: "After truncation" }]);
    expect(d4.nextCursor.byteOffset).toBe(Buffer.byteLength(truncated, "utf8"));
  });

  test("startHeadless and stopAndFlush wire CodexHeadlessExecutor and flush final delta", async () => {
    const home = makeTempDir("codex-stop-");
    const codexDir = path.join(home, ".codex");
    const sid = "sess-codex-stop-1";
    const rolloutPath = path.join(codexDir, "sessions", `rollout-${sid}.jsonl`);

    const line1 =
      JSON.stringify({
        type: "event_msg",
        payload: { type: "user_message", message: "Initial question" },
      }) + "\n";
    writeFile(rolloutPath, line1);

    const plugin = new CodexPlugin({ homeDir: home });
    const defaultSession = plugin.startHeadless({
      threadId: "t1",
      cwd: "/repo",
      sessionId: sid,
    });
    expect(defaultSession.agent).toBe("codex");
    expect(defaultSession.threadId).toBe("t1");
    expect(defaultSession.getSessionId()).toBe(sid);
    expect(defaultSession.isRunning()).toBe(false);

    const first = plugin.readDelta(sid, "/repo", undefined, codexDir);
    expect(first.turns).toHaveLength(1);

    let headlessStopped = false;
    let interactiveStopped = false;
    const fakeHeadless: HeadlessSession = {
      agent: "codex",
      threadId: "t1",
      getSessionId: () => sid,
      isRunning: () => true,
      async sendTurn() {
        return {};
      },
      async cancel() {},
      async stopAndFlush() {
        headlessStopped = true;
        writeFile(
          rolloutPath,
          line1 +
            JSON.stringify({
              type: "event_msg",
              payload: { type: "agent_message", message: "Flushed answer." },
            }) +
            "\n",
        );
        return { sessionId: sid };
      },
    };

    const flushed = await plugin.stopAndFlush({
      cwd: "/repo",
      configDir: codexDir,
      cursor: first.nextCursor,
      headlessSession: fakeHeadless,
      stopInteractive: () => {
        interactiveStopped = true;
      },
    });
    expect(headlessStopped).toBe(true);
    expect(interactiveStopped).toBe(true);
    expect(flushed?.turns).toEqual([{ role: "assistant", text: "Flushed answer." }]);
    expect(await plugin.stopAndFlush({ cwd: "/repo" })).toBeNull();
  });
});

describe("CodexHeadlessExecutor", () => {
  test("buildCodexHeadlessArgv constructs codex exec --json and codex exec resume <id> --json", () => {
    expect(
      buildCodexHeadlessArgv(
        ["/usr/local/bin/codex", "-c", 'model="gpt-6-luna"', "--full-auto"],
        { prompt: "Hello world" },
      ),
    ).toEqual([
      "/usr/local/bin/codex",
      "exec",
      "--json",
      "-c",
      'model="gpt-6-luna"',
      "--full-auto",
      "Hello world",
    ]);

    expect(
      buildCodexHeadlessArgv(
        ["codex", "exec", "--json", "--full-auto", "resume", "old-id"],
        { sessionId: "new-thread-id", prompt: "Continue task" },
      ),
    ).toEqual([
      "codex",
      "exec",
      "resume",
      "new-thread-id",
      "--json",
      "--full-auto",
      "Continue task",
    ]);
  });

  test("streams thread.started, item.completed (agent_message), and turn.completed usage, then resumes on turn 2", async () => {
    const spawnedArgvs: string[][] = [];
    const controllers: FakeProcessController[] = [];

    const spawn: CodexSpawnFn = (argv) => {
      spawnedArgvs.push(argv);
      const ctrl = createFakeProcess();
      controllers.push(ctrl);

      queueMicrotask(() => {
        if (controllers.length === 1) {
          ctrl.emitStdoutJson({ type: "thread.started", thread_id: "thread-abc-123" });
          ctrl.emitStdoutJson({
            type: "item.completed",
            item: { id: "item_1", type: "agent_message", text: "First reply." },
          });
          ctrl.emitStdoutJson({
            type: "turn.completed",
            usage: {
              input_tokens: 100,
              cached_input_tokens: 25,
              output_tokens: 30,
            },
          });
          ctrl.exit(0);
        } else {
          ctrl.emitStdoutJson({
            type: "item.completed",
            item: { id: "item_2", type: "agent_message", text: "Second reply." },
          });
          ctrl.emitStdoutJson({
            type: "turn.completed",
            usage: {
              input_tokens: 50,
              cached_input_tokens: 15,
              output_tokens: 20,
            },
          });
          ctrl.exit(0);
        }
      });

      return ctrl.proc;
    };

    const exec = new CodexHeadlessExecutor({
      threadId: "t-codex",
      launch: baseLaunch({ argv: ["/bin/codex", "--full-auto"] }),
      spawn,
    });

    const events1: HeadlessTurnEvent[] = [];
    const events2: HeadlessTurnEvent[] = [];

    // Queue two turns concurrently; executor must serialize them and resume turn 2 with thread-abc-123
    const [res1, res2] = await Promise.all([
      exec.sendTurn("Prompt one", (ev) => events1.push(ev)),
      exec.sendTurn("Prompt two", (ev) => events2.push(ev)),
    ]);

    expect(spawnedArgvs).toEqual([
      ["/bin/codex", "exec", "--json", "--full-auto", "Prompt one"],
      ["/bin/codex", "exec", "resume", "thread-abc-123", "--json", "--full-auto", "Prompt two"],
    ]);

    expect(exec.getSessionId()).toBe("thread-abc-123");
    expect(res1).toEqual({
      sessionId: "thread-abc-123",
      replyPreview: "First reply.",
      usage: {
        input_tokens: 100,
        output_tokens: 30,
        reasoning_tokens: 0,
        cache_read_tokens: 25,
        cache_write_tokens: 0,
        cost_usd: 0,
        cost_priced: false,
      },
    });
    expect(events1).toEqual([
      { type: "session_init", sessionId: "thread-abc-123" },
      { type: "text_delta", text: "First reply." },
      {
        type: "completed",
        sessionId: "thread-abc-123",
        replyPreview: "First reply.",
        usage: res1.usage,
      },
    ]);

    expect(res2.replyPreview).toBe("Second reply.");

    const flushed = await exec.stopAndFlush();
    expect(flushed).toEqual({
      sessionId: "thread-abc-123",
      usage: {
        input_tokens: 150,
        output_tokens: 50,
        reasoning_tokens: 0,
        cache_read_tokens: 40,
        cache_write_tokens: 0,
        cost_usd: 0,
        cost_priced: false,
      },
    });

    await expect(exec.sendTurn("After stop")).rejects.toMatchObject({
      name: "CodexHeadlessError",
      code: "session_stopped",
    });
  });

  test("handles turn.failed and error events as structured CodexHeadlessError failures", async () => {
    let call = 0;
    const spawn: CodexSpawnFn = () => {
      call += 1;
      const ctrl = createFakeProcess();
      queueMicrotask(() => {
        if (call === 1) {
          ctrl.emitStdoutJson({
            type: "turn.failed",
            error: { code: "rate_limit", message: "Too many requests" },
          });
        } else {
          ctrl.emitStdoutJson({
            type: "error",
            message: "Authentication failed",
          });
        }
        ctrl.exit(1);
      });
      return ctrl.proc;
    };

    const exec = new CodexHeadlessExecutor({
      threadId: "t-fail",
      launch: baseLaunch(),
      spawn,
    });

    const events: HeadlessTurnEvent[] = [];
    await expect(exec.sendTurn("Fail 1", (e) => events.push(e))).rejects.toThrow(
      CodexHeadlessError,
    );
    expect(events).toEqual([
      {
        type: "failed",
        code: "rate_limit",
        message: "Too many requests",
        retryable: true,
      },
    ]);

    await expect(exec.sendTurn("Fail 2")).rejects.toMatchObject({
      code: "codex_error",
      message: "Authentication failed",
      retryable: false,
    });
  });

  test("cancel interrupts running process with SIGINT -> SIGTERM fallback and rejects queued turns", async () => {
    let activeCtrl!: FakeProcessController;
    const spawn: CodexSpawnFn = () => {
      activeCtrl = createFakeProcess({ ignoreSigint: true });
      return activeCtrl.proc;
    };

    const exec = new CodexHeadlessExecutor({
      threadId: "t-cancel",
      launch: baseLaunch(),
      spawn,
      cancelGraceMs: 20,
    });

    const p1 = exec.sendTurn("Long running turn");
    const p2 = exec.sendTurn("Queued turn");

    // Wait a tick for p1 to spawn
    await new Promise((r) => setTimeout(r, 10));
    expect(exec.isRunning()).toBe(true);

    await exec.cancel("user requested");

    await expect(p1).rejects.toMatchObject({
      code: "cancelled",
      message: "Turn cancelled: user requested",
    });
    await expect(p2).rejects.toMatchObject({
      code: "cancelled",
      message: "Turn cancelled: user requested",
    });
    expect(activeCtrl.signals).toEqual(["SIGINT", "SIGTERM"]);
    expect(exec.isRunning()).toBe(false);
  });
});

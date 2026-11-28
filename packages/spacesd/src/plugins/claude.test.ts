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
import type { DriverPane, DriverSession, DriverSnapshot, DriverWindow } from "../drivers/types";
import { captureSpaces } from "../resurrect/capture";
import { resolveAgentSessions, restoreSpace } from "../resurrect/restore";
import { SpacesStore, type ResurrectSpaceSnapshot } from "../store/db";
import { ClaudePlugin, encodeClaudeProjectSlug } from "./claude";
import { loadProfileConfig, loadRouterConfig, realHomeDir } from "./profiles";
import { HarnessPluginRegistry } from "./registry";
import type { HeadlessSession, HeadlessTurnEvent, InteractiveLaunchSpec } from "./types";

const tempDirs: string[] = [];

function makeTempDir(prefix = "claude-plugin-test-"): string {
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

describe("profiles & router config helpers", () => {
  test("realHomeDir unwraps both /.spaces/ and /.harness/ shadow prefixes", () => {
    expect(realHomeDir("/Users/alice")).toBe("/Users/alice");
    expect(realHomeDir("/Users/alice/.spaces/profiles/claude/work/home")).toBe("/Users/alice");
    expect(realHomeDir("/Users/alice/.harness/profiles/claude/work/home")).toBe("/Users/alice");
    expect(realHomeDir("/Users/alice/.harness/profiles/claude/work/.spaces/nested")).toBe(
      "/Users/alice",
    );

    const prevHome = process.env.HOME;
    try {
      process.env.HOME = "/home/bob/.spaces/profiles/codex/ops/home";
      expect(realHomeDir()).toBe("/home/bob");
    } finally {
      if (prevHome === undefined) {
        delete process.env.HOME;
      } else {
        process.env.HOME = prevHome;
      }
    }
  });

  test("loadRouterConfig follows env -> XDG_CONFIG_HOME/ai/model-router.env -> defaults", () => {
    const xdgEmpty = makeTempDir("xdg-empty-");
    expect(loadRouterConfig({ env: {}, xdgConfigHome: xdgEmpty })).toEqual({
      url: "http://127.0.0.1:4000",
      apiKey: "unused",
    });

    const xdgWithFile = makeTempDir("xdg-file-");
    writeFile(
      path.join(xdgWithFile, "ai", "model-router.env"),
      [
        "# router config",
        `export MODEL_ROUTER_URL="\${MODEL_ROUTER_URL:-http://127.0.0.1:4100/}"`,
        `export MODEL_ROUTER_API_KEY="\${MODEL_ROUTER_API_KEY:-router-secret}"`,
      ].join("\n"),
    );

    expect(loadRouterConfig({ env: {}, xdgConfigHome: xdgWithFile })).toEqual({
      url: "http://127.0.0.1:4100",
      apiKey: "router-secret",
    });

    expect(
      loadRouterConfig({
        env: {
          MODEL_ROUTER_URL: "http://127.0.0.1:5000///",
          MODEL_ROUTER_API_KEY: "env-key",
        },
        xdgConfigHome: xdgWithFile,
      }),
    ).toEqual({
      url: "http://127.0.0.1:5000",
      apiKey: "env-key",
    });
  });

  test("loadProfileConfig prioritizes ~/.spaces/profiles, falls back to ~/.harness, and normalizes keys", () => {
    const home = makeTempDir("home-");
    const spacesDir = path.join(home, ".spaces");

    // 1. Legacy ~/.harness/profiles.json fallback (snake_case & camelCase)
    writeFile(
      path.join(home, ".harness", "profiles.json"),
      JSON.stringify({
        version: 1,
        profiles: {
          legacy: {
            agent: "claude",
            display_name: "Legacy Profile",
            binary_path: "~/bin/claude-legacy",
            prepend_args: ["--verbose"],
            router: false,
            env: { CUSTOM_LEGACY: "yes" },
          },
        },
      }),
    );

    const fromLegacyJson = loadProfileConfig("claude", "legacy", { homeDir: home, spacesDir });
    expect(fromLegacyJson).toEqual({
      agent: "claude",
      name: "legacy",
      displayName: "Legacy Profile",
      enabled: true,
      binaryPath: path.join(home, "bin", "claude-legacy"),
      shadowHomePath: path.join(home, ".harness", "profiles", "claude", "legacy", "home"),
      env: { CUSTOM_LEGACY: "yes" },
      prependArgs: ["--verbose"],
      router: false,
    });

    // 2. ~/.harness/profiles/<agent>/<name>/profile.json overrides profiles.json
    writeFile(
      path.join(home, ".harness", "profiles", "claude", "work", "profile.json"),
      JSON.stringify({
        agent: "claude",
        displayName: "Harness Work",
        prependArgs: ["--model", "sonnet"],
        env: { FROM_HARNESS: "1" },
      }),
    );

    const fromHarness = loadProfileConfig("claude", "work", { homeDir: home, spacesDir });
    expect(fromHarness).toEqual({
      agent: "claude",
      name: "work",
      displayName: "Harness Work",
      enabled: true,
      shadowHomePath: path.join(home, ".harness", "profiles", "claude", "work", "home"),
      env: { FROM_HARNESS: "1" },
      prependArgs: ["--model", "sonnet"],
      router: true,
    });

    // 3. ~/.spaces/profiles/<agent>/<name>/profile.json wins over .harness
    writeFile(
      path.join(spacesDir, "profiles", "claude", "work", "profile.json"),
      JSON.stringify({
        agent: "claude",
        display_name: "Spaces Work",
        binary_path: "$HOME/.local/bin/claude-custom",
        prepend_args: ["--debug"],
        router: true,
        env: { FROM_SPACES: "1" },
      }),
    );

    // Works even when homeDir is passed as a shadow home inside .harness!
    const shadowCallerHome = path.join(home, ".harness", "profiles", "claude", "other", "home");
    const fromSpaces = loadProfileConfig("claude", "work", {
      homeDir: shadowCallerHome,
      spacesDir,
    });
    expect(fromSpaces).toEqual({
      agent: "claude",
      name: "work",
      displayName: "Spaces Work",
      enabled: true,
      binaryPath: path.join(home, ".local", "bin", "claude-custom"),
      shadowHomePath: path.join(spacesDir, "profiles", "claude", "work", "home"),
      env: { FROM_SPACES: "1" },
      prependArgs: ["--debug"],
      router: true,
    });

    expect(loadProfileConfig("claude", "nonexistent", { homeDir: home, spacesDir })).toBeNull();
  });
});

describe("ClaudePlugin launch & headless/stopAndFlush", () => {
  test("buildInteractiveLaunch handles shadow home (preserving HOME), router on/off, access levels, and flags", () => {
    const home = makeTempDir("claude-launch-");
    const spacesDir = path.join(home, ".spaces");

    writeFile(
      path.join(home, ".config", "ai", "model-router.env"),
      "MODEL_ROUTER_URL=http://127.0.0.1:4321\nMODEL_ROUTER_API_KEY=tok-4321\n",
    );

    writeFile(
      path.join(spacesDir, "profiles", "claude", "work", "profile.json"),
      JSON.stringify({
        agent: "claude",
        displayName: "Work",
        binaryPath: "/opt/bin/claude-work",
        prependArgs: ["--verbose"],
        env: { EXTRA_VAR: "abc" },
        router: true,
      }),
    );

    writeFile(
      path.join(spacesDir, "profiles", "claude", "direct", "profile.json"),
      JSON.stringify({
        agent: "claude",
        displayName: "Direct Auth",
        router: false,
      }),
    );

    const plugin = new ClaudePlugin({ homeDir: home, spacesDir });

    // Unprofiled default launch
    const bare = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      accessLevel: "read-only",
      model: "claude-sonnet-4-5",
      effort: "high",
      sessionId: "sess-123",
      extraArgs: ["--print"],
      env: {},
    });
    expect(bare.argv).toEqual([
      "claude",
      "--permission-mode",
      "plan",
      "--model",
      "claude-sonnet-4-5",
      "--effort",
      "high",
      "--resume",
      "sess-123",
      "--print",
    ]);
    expect(bare.env).toEqual({
      ANTHROPIC_BASE_URL: "http://127.0.0.1:4321",
      ANTHROPIC_AUTH_TOKEN: "tok-4321",
    });
    expect(bare.env.HOME).toBeUndefined();
    expect(bare.env.CLAUDE_CONFIG_DIR).toBeUndefined();
    expect(bare.unsetEnv).toEqual(["HARNESS_AGENT", "HARNESS_PROFILE"]);
    expect(bare.configDir).toBe(path.join(home, ".claude"));

    // Profiled launch with router enabled
    const expectedShadow = path.join(spacesDir, "profiles", "claude", "work", "home");
    const profiled = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "work",
      accessLevel: "auto",
      env: {},
    });
    expect(profiled.argv).toEqual([
      "/opt/bin/claude-work",
      "--verbose",
      "--permission-mode",
      "acceptEdits",
    ]);
    expect(profiled.shadowHome).toBe(expectedShadow);
    expect(profiled.configDir).toBe(expectedShadow);
    expect(profiled.env).toEqual({
      ANTHROPIC_BASE_URL: "http://127.0.0.1:4321",
      ANTHROPIC_AUTH_TOKEN: "tok-4321",
      CLAUDE_CONFIG_DIR: expectedShadow,
      HARNESS_PROFILE: "work",
      EXTRA_VAR: "abc",
    });
    expect(profiled.env.HOME).toBeUndefined();
    expect(profiled.unsetEnv).toEqual(["HARNESS_AGENT"]);

    // noRouter=true or profile.router=false scrubs router/API credentials
    const noRouter = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "work",
      accessLevel: "full",
      noRouter: true,
      env: {},
    });
    expect(noRouter.argv).toContain("--dangerously-skip-permissions");
    expect(noRouter.env.ANTHROPIC_BASE_URL).toBeUndefined();
    expect(noRouter.env.ANTHROPIC_AUTH_TOKEN).toBeUndefined();
    expect(noRouter.env.CLAUDE_CONFIG_DIR).toBe(expectedShadow);
    expect(noRouter.unsetEnv).toEqual([
      "HARNESS_AGENT",
      "ANTHROPIC_BASE_URL",
      "ANTHROPIC_AUTH_TOKEN",
      "ANTHROPIC_API_KEY",
    ]);

    const profileRouterOff = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      profile: "direct",
      accessLevel: "default",
      env: {},
    });
    expect(profileRouterOff.argv).toEqual(["claude"]);
    expect(profileRouterOff.unsetEnv).toEqual([
      "HARNESS_AGENT",
      "ANTHROPIC_BASE_URL",
      "ANTHROPIC_AUTH_TOKEN",
      "ANTHROPIC_API_KEY",
    ]);

    const envOverride = plugin.buildInteractiveLaunch({
      cwd: "/repo/app",
      env: {
        MODEL_ROUTER_URL: "http://127.0.0.1:4999/",
        MODEL_ROUTER_API_KEY: "override-tok",
      },
    });
    expect(envOverride.env).toEqual({
      ANTHROPIC_BASE_URL: "http://127.0.0.1:4999",
      ANTHROPIC_AUTH_TOKEN: "override-tok",
    });

    writeFile(
      path.join(spacesDir, "profiles", "claude", "disabled", "profile.json"),
      JSON.stringify({ agent: "claude", enabled: false }),
    );
    expect(() =>
      plugin.buildInteractiveLaunch({ cwd: "/repo/app", profile: "disabled" }),
    ).toThrow(/profile disabled/);
  });

  test("startHeadless defaults to ClaudeHeadlessExecutor and delegates when headlessFactory is provided", () => {
    const home = makeTempDir("claude-headless-");
    const barePlugin = new ClaudePlugin({ homeDir: home });
    const defaultSession = barePlugin.startHeadless({
      threadId: "t1",
      cwd: "/repo",
      sessionId: "sess-default",
    });
    expect(defaultSession.agent).toBe("claude");
    expect(defaultSession.threadId).toBe("t1");
    expect(defaultSession.getSessionId()).toBe("sess-default");
    expect(defaultSession.isRunning()).toBe(false);

    const captured: { launch?: InteractiveLaunchSpec } = {};
    const fakeSession: HeadlessSession = {
      agent: "claude",
      threadId: "t1",
      getSessionId: () => "sess-h1",
      isRunning: () => false,
      async sendTurn(_prompt: string, _onEvent?: (ev: HeadlessTurnEvent) => void) {
        return { sessionId: "sess-h1", replyPreview: "ok" };
      },
      async cancel() {},
      async stopAndFlush() {
        return { sessionId: "sess-h1" };
      },
    };

    const wiredPlugin = new ClaudePlugin({
      homeDir: home,
      headlessFactory: (_req, launch) => {
        captured.launch = launch;
        return fakeSession;
      },
    });
    const session = wiredPlugin.startHeadless({
      threadId: "t1",
      cwd: "/repo",
      accessLevel: "full",
      model: "claude-opus-4-1",
    });
    expect(session).toBe(fakeSession);
    expect(captured.launch?.argv).toEqual([
      "claude",
      "--dangerously-skip-permissions",
      "--model",
      "claude-opus-4-1",
    ]);
  });

  test("stopAndFlush stops headless + interactive handles and runs final readDelta", async () => {
    const home = makeTempDir("claude-stop-");
    const cwd = "/work/proj";
    const slug = encodeClaudeProjectSlug(cwd);
    const configDir = path.join(home, ".claude");
    const jsonlPath = path.join(configDir, "projects", slug, "sess-stop-1.jsonl");

    const line1 =
      JSON.stringify({
        type: "user",
        sessionId: "sess-stop-1",
        timestamp: "2026-11-25T10:00:00Z",
        message: { role: "user", content: "Initial prompt" },
      }) + "\n";
    writeFile(jsonlPath, line1);

    const plugin = new ClaudePlugin({ homeDir: home });
    const first = plugin.readDelta("sess-stop-1", cwd, undefined, configDir);
    expect(first.turns).toHaveLength(1);

    let headlessStopped = false;
    let interactiveStopped = false;
    const fakeHeadless: HeadlessSession = {
      agent: "claude",
      threadId: "t-stop",
      getSessionId: () => "sess-stop-1",
      isRunning: () => true,
      async sendTurn() {
        return {};
      },
      async cancel() {},
      async stopAndFlush() {
        headlessStopped = true;
        // Flush final assistant turn before returning
        writeFile(
          jsonlPath,
          line1 +
            JSON.stringify({
              type: "assistant",
              sessionId: "sess-stop-1",
              timestamp: "2026-11-25T10:00:05Z",
              message: {
                role: "assistant",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
                content: [{ type: "text", text: "Final flushed answer." }],
                usage: { input_tokens: 40, output_tokens: 12 },
              },
            }) +
            "\n",
        );
        return { sessionId: "sess-stop-1" };
      },
    };

    const flushed = await plugin.stopAndFlush({
      cwd,
      configDir,
      cursor: first.nextCursor,
      headlessSession: fakeHeadless,
      stopInteractive: () => {
        interactiveStopped = true;
      },
    });

    expect(headlessStopped).toBe(true);
    expect(interactiveStopped).toBe(true);
    expect(flushed).not.toBeNull();
    expect(flushed!.turns).toEqual([
      {
        role: "assistant",
        text: "Final flushed answer.",
        timestampMs: Date.parse("2026-11-25T10:00:05Z"),
        model: "claude-sonnet-4-5",
        stopReason: "end_turn",
        usage: {
          input_tokens: 40,
          output_tokens: 12,
          reasoning_tokens: 0,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
          cost_usd: 0,
          cost_priced: false,
        },
      },
    ]);

    expect(await plugin.stopAndFlush({ cwd })).toBeNull();
  });
});

describe("ClaudePlugin readDelta incremental JSONL parser", () => {
  test("encodes project slugs and reads incremental byte offsets with partial line buffering and truncation reset", () => {
    expect(encodeClaudeProjectSlug("/home/dev/my_app.v2/")).toBe("-home-dev-my-app-v2");
    expect(encodeClaudeProjectSlug("/")).toBe("-");

    const home = makeTempDir("claude-delta-");
    const cwd = "/home/dev/app";
    const configDir = path.join(home, ".claude");
    const jsonlPath = path.join(
      configDir,
      "projects",
      encodeClaudeProjectSlug(cwd),
      "sess-inc.jsonl",
    );

    const line1 =
      JSON.stringify({
        type: "summary",
        summary: "Fallback summary title",
        sessionId: "sess-inc",
        timestamp: "2026-11-25T09:00:00Z",
      }) + "\n";
    const line2 =
      JSON.stringify({
        type: "user",
        isSidechain: true,
        sessionId: "sess-inc",
        timestamp: "2026-11-25T09:00:01Z",
        message: { role: "user", content: "Sidechain task must be ignored" },
      }) + "\n";
    const line3 =
      JSON.stringify({
        type: "user",
        sessionId: "sess-inc",
        timestamp: "2026-11-25T09:00:02Z",
        message: { role: "user", content: "Fix streaming UTF-8 — 日本語" },
      }) + "\n";
    const line4Full =
      JSON.stringify({
        type: "assistant",
        sessionId: "sess-inc",
        timestamp: "2026-11-25T09:00:05Z",
        message: {
          role: "assistant",
          model: "claude-sonnet-4-5",
          stop_reason: "end_turn",
          content: [
            { type: "thinking", thinking: "internal" },
            { type: "text", text: "Part one." },
            { type: "tool_use", name: "Read", input: { path: "a.ts" } },
            { type: "text", text: "Part two." },
          ],
          usage: {
            input_tokens: 120,
            output_tokens: 35,
            cache_read_input_tokens: 500,
            cache_creation_input_tokens: 80,
          },
        },
      }) + "\n";

    // Write lines 1-3 plus first half of line 4 (incomplete trailing line)
    const splitAt = Math.floor(line4Full.length / 2);
    const partA = line4Full.slice(0, splitAt);
    const partB = line4Full.slice(splitAt);
    writeFile(jsonlPath, line1 + line2 + line3 + partA);

    const plugin = new ClaudePlugin({ homeDir: home });
    const d1 = plugin.readDelta("sess-inc", cwd, undefined, configDir);
    expect(d1.title).toBe("Fallback summary title");
    expect(d1.turns).toHaveLength(1);
    expect(d1.turns[0]).toEqual({
      role: "user",
      text: "Fix streaming UTF-8 — 日本語",
      timestampMs: Date.parse("2026-11-25T09:00:02Z"),
    });
    expect(d1.nextCursor.partialLine).toBe(partA);
    expect(d1.nextCursor.byteOffset).toBe(Buffer.byteLength(line1 + line2 + line3 + partA, "utf8"));

    // Append remainder of line 4, custom-title (beats summary), and cost-state
    const line5 =
      JSON.stringify({
        type: "custom-title",
        customTitle: "Authoritative Custom Title",
        sessionId: "sess-inc",
        timestamp: "2026-11-25T09:00:06Z",
      }) + "\n";
    const line6 =
      JSON.stringify({
        type: "summary",
        summary: "Later summary should not override custom-title",
      }) + "\n";
    const line7 =
      JSON.stringify({
        type: "cost-state",
        total_cost_usd: 0.0145,
        usage: {
          input_tokens: 120,
          output_tokens: 35,
          cache_read_tokens: 500,
          cache_write_tokens: 80,
        },
      }) + "\n";

    writeFile(jsonlPath, line1 + line2 + line3 + line4Full + line5 + line6 + line7);

    const d2 = plugin.readDelta("sess-inc", cwd, d1.nextCursor, configDir);
    expect(d2.nextCursor.partialLine).toBeUndefined();
    expect(d2.title).toBe("Authoritative Custom Title");
    expect(d2.model).toBe("claude-sonnet-4-5");
    expect(d2.turns).toEqual([
      {
        role: "assistant",
        text: "Part one.\nPart two.",
        timestampMs: Date.parse("2026-11-25T09:00:05Z"),
        model: "claude-sonnet-4-5",
        stopReason: "end_turn",
        usage: {
          input_tokens: 120,
          output_tokens: 35,
          reasoning_tokens: 0,
          cache_read_tokens: 500,
          cache_write_tokens: 80,
          cost_usd: 0,
          cost_priced: false,
        },
      },
    ]);
    expect(d2.usage).toEqual({
      input_tokens: 120,
      output_tokens: 35,
      reasoning_tokens: 0,
      cache_read_tokens: 500,
      cache_write_tokens: 80,
      cost_usd: 0.0145,
      cost_priced: true,
    });

    // No new bytes -> empty delta
    const d3 = plugin.readDelta("sess-inc", cwd, d2.nextCursor, configDir);
    expect(d3.turns).toEqual([]);
    expect(d3.nextCursor.byteOffset).toBe(d2.nextCursor.byteOffset);

    // File truncation resets offset to 0
    const truncated =
      JSON.stringify({
        type: "user",
        message: { role: "user", content: "Reset after truncation" },
      }) + "\n";
    writeFile(jsonlPath, truncated);
    const d4 = plugin.readDelta("sess-inc", cwd, d3.nextCursor, configDir);
    expect(d4.turns).toEqual([{ role: "user", text: "Reset after truncation" }]);
    expect(d4.nextCursor.byteOffset).toBe(Buffer.byteLength(truncated, "utf8"));
  });

  test("readDelta falls back to scanning projects/*/<sessionId>.jsonl when exact slug dir is missing", () => {
    const home = makeTempDir("claude-scan-");
    const configDir = path.join(home, ".claude");
    const fallbackPath = path.join(configDir, "projects", "custom-slug-dir", "sess-fallback.jsonl");
    writeFile(
      fallbackPath,
      JSON.stringify({
        type: "user",
        sessionId: "sess-fallback",
        message: { role: "user", content: "Found via fallback scan" },
      }) + "\n",
    );

    const plugin = new ClaudePlugin({ homeDir: home });
    const res = plugin.readDelta("sess-fallback", "/unrelated/cwd", undefined, configDir);
    expect(res.nextCursor.sessionPath).toBe(fallbackPath);
    expect(res.turns).toEqual([{ role: "user", text: "Found via fallback scan" }]);
  });
});

describe("ClaudePlugin resolveVendorSession & HarnessPluginRegistry integration", () => {
  test("resolveVendorSession matches exact case-insensitive title first, falls back to newest unclaimed, and honors exclude & profile", () => {
    const home = makeTempDir("claude-vendor-");
    const spacesDir = path.join(home, ".spaces");
    const cwd = "/work/spaces";
    const slug = encodeClaudeProjectSlug(cwd);

    const defaultProjectsDir = path.join(home, ".claude", "projects", slug);
    const sOlder = path.join(defaultProjectsDir, "sess-older.jsonl");
    const sNewest = path.join(defaultProjectsDir, "sess-newest.jsonl");
    const sOtherCwd = path.join(
      home,
      ".claude",
      "projects",
      encodeClaudeProjectSlug("/work/other"),
      "sess-other.jsonl",
    );

    writeFile(
      sOlder,
      JSON.stringify({
        type: "custom-title",
        customTitle: "Fix Auth Middleware",
        sessionId: "sess-older",
        cwd,
      }) + "\n",
    );
    writeFile(
      sNewest,
      JSON.stringify({
        type: "summary",
        summary: "Refactor PTY Broker",
        sessionId: "sess-newest",
        cwd,
      }) + "\n",
    );
    writeFile(
      sOtherCwd,
      JSON.stringify({
        type: "summary",
        summary: "Fix Auth Middleware",
        sessionId: "sess-other",
        cwd: "/work/other",
      }) + "\n",
    );

    const olderTime = new Date("2026-11-24T10:00:00Z");
    const newerTime = new Date("2026-11-25T10:00:00Z");
    utimesSync(sOlder, olderTime, olderTime);
    utimesSync(sNewest, newerTime, newerTime);
    utimesSync(sOtherCwd, newerTime, newerTime);

    // Profile "work" isolated store
    writeFile(
      path.join(spacesDir, "profiles", "claude", "work", "profile.json"),
      JSON.stringify({ agent: "claude", displayName: "Work" }),
    );
    const profSession = path.join(
      spacesDir,
      "profiles",
      "claude",
      "work",
      "home",
      "projects",
      slug,
      "sess-prof-1.jsonl",
    );
    writeFile(
      profSession,
      JSON.stringify({
        type: "user",
        sessionId: "sess-prof-1",
        cwd,
        message: { role: "user", content: "Profile isolated task" },
      }) + "\n",
    );

    const plugin = new ClaudePlugin({ homeDir: home, spacesDir });

    // Exact case-insensitive title match beats newer session
    expect(
      plugin.resolveVendorSession("claude", cwd, "", "fix auth middleware", new Set()),
    ).toEqual({ sessionId: "sess-older", storeAvailable: true });

    // Empty/non-matching title selects newest unclaimed in CWD (ignoring other CWD)
    expect(plugin.resolveVendorSession("claude", cwd, "", "", new Set())).toEqual({
      sessionId: "sess-newest",
      storeAvailable: true,
    });

    // Exclude deduplication moves to next unclaimed session in CWD
    expect(
      plugin.resolveVendorSession("claude", cwd, "", "", new Set(["sess-newest"])),
    ).toEqual({ sessionId: "sess-older", storeAvailable: true });

    // Profiled lookup resolves inside profile shadowHome without leaking default store
    expect(
      plugin.resolveVendorSession("claude", cwd, "work", "", new Set()),
    ).toEqual({ sessionId: "sess-prof-1", storeAvailable: true });
    expect(
      plugin.resolveVendorSession("claude", cwd, "work", "", new Set(["sess-prof-1"])),
    ).toEqual({ sessionId: "", storeAvailable: true });
  });

  test("HarnessPluginRegistry.asResumePolicy() integrates with resurrect captureSpaces & restoreSpace", async () => {
    const home = makeTempDir("claude-resurrect-");
    const cwd = "/repo/unified";
    const slug = encodeClaudeProjectSlug(cwd);
    const projDir = path.join(home, ".claude", "projects", slug);

    const sAuth = path.join(projDir, "sess-auth.jsonl");
    const sLatest = path.join(projDir, "sess-latest.jsonl");
    writeFile(
      sAuth,
      JSON.stringify({
        type: "custom-title",
        customTitle: "Fix OAuth",
        sessionId: "sess-auth",
        cwd,
      }) + "\n",
    );
    writeFile(
      sLatest,
      JSON.stringify({
        type: "summary",
        summary: "Unrelated latest",
        sessionId: "sess-latest",
        cwd,
      }) + "\n",
    );
    utimesSync(sAuth, new Date("2026-11-24T08:00:00Z"), new Date("2026-11-24T08:00:00Z"));
    utimesSync(sLatest, new Date("2026-11-25T08:00:00Z"), new Date("2026-11-25T08:00:00Z"));

    const claude = new ClaudePlugin({ homeDir: home });
    const registry = new HarnessPluginRegistry();
    registry.register(claude);

    expect(registry.get("claude")).toBe(claude);
    expect(registry.get("CLAUDE-CODE")).toBe(claude);
    expect(registry.get("claude_code")).toBe(claude);
    expect(registry.get("claudecode")).toBe(claude);
    expect(registry.list()).toEqual([claude]);

    const policy = registry.asResumePolicy();
    expect(policy.binaryName?.("claude-code")).toBe("claude");
    expect(policy.resumeArgs?.("claude-code", "s-1")).toEqual(["--resume", "s-1"]);
    expect(policy.positionalResume?.("claude")).toBe(false);
    expect(policy.uniqueExplicitSessions?.("claude")).toBe(false);

    // 1. Capture integration via captureSpaces(..., { resumePolicy: policy })
    const store = new SpacesStore(":memory:");
    const snap: DriverSnapshot = {
      currentSession: "main",
      currentPaneId: "%1",
      sessions: [
        {
          id: "$0",
          name: "main",
          attached: 1,
          createdAtSec: 1_700_000_000,
          windowCount: 1,
          worktreePath: "",
          worktreeName: "",
          worktreeBase: "",
          worktreeManaged: false,
        } satisfies DriverSession,
      ],
      windows: [
        {
          sessionName: "main",
          id: "@1",
          index: 1,
          name: "claude",
          active: true,
          last: false,
          zoomed: false,
          bell: false,
          activity: false,
          silence: false,
          width: 120,
          height: 40,
          activityAtSec: 0,
          layout: "",
          worktreePath: "",
          worktreeName: "",
          worktreeBase: "",
          worktreeManaged: false,
        } satisfies DriverWindow,
      ],
      panes: [
        {
          sessionName: "main",
          windowId: "@1",
          windowIndex: 1,
          windowName: "claude",
          paneIndex: 1,
          id: "%1",
          numericId: 1,
          title: "",
          currentCommand: "claude",
          startCommand: "",
          currentPath: cwd,
          pid: 101,
          width: 120,
          height: 40,
          active: true,
          windowActive: true,
          inMode: false,
          synchronized: false,
          dead: false,
          deadStatus: 0,
          bell: false,
          activity: false,
          silence: false,
          spacesState: "idle",
          spacesApp: "claude-code",
          spacesKind: "",
          spacesMsg: "",
          spacesTsMs: 0,
          spacesSource: "hook",
          spacesTitle: "Fix OAuth",
          resumeSession: "",
          profile: "",
          launchCommand: [],
        } satisfies DriverPane,
      ],
    };

    const captured = captureSpaces(snap, null, store, {
      readChildCmdline: () => ["claude", "--resume", "sess-latest"],
      resumePolicy: policy,
    });
    expect(captured[0]!.windows[0]!.panes[0]!.session_id).toBe("sess-latest");

    // 2. Restore integration: Tier 6 vendor resolution + deduplication + skipResume
    const spaceSnap: ResurrectSpaceSnapshot = {
      name: "main",
      cwd,
      windows: [
        {
          index: 1,
          name: "claude",
          active: true,
          panes: [
            // Pane 1 explicitly has sess-latest
            { index: 1, active: true, agent: "claude", cwd, session_id: "sess-latest" },
            // Pane 2 matches "Fix OAuth" via Tier 6 resolveVendorSession
            { index: 2, active: false, agent: "claude", cwd, title: "Fix OAuth" },
            // Pane 3 finds all store sessions already claimed -> skipResume (no --resume)
            { index: 3, active: false, agent: "claude", cwd, title: "Brand New" },
          ],
        },
      ],
    };

    const resolution = resolveAgentSessions(spaceSnap, undefined, policy);
    expect(resolution.sessionIds).toEqual({ "0:1": "sess-auth" });
    expect(resolution.skipResume).toEqual({ "0:2": true });

    const launchedCmds: string[][] = [];
    const fakeDriver = {
      kind: "tmux" as const,
      async serverRunning() { return true; },
      async currentSession() { return "main"; },
      async hasClients() { return false; },
      async listSessions() { return []; },
      async listWindows() { return []; },
      async listPanes() { return []; },
      async snapshot(): Promise<DriverSnapshot> {
        return { sessions: [], windows: [], panes: [], currentSession: "", currentPaneId: "" };
      },
      async capturePane() { return ""; },
      async capturePaneStyled() { return ""; },
      async switchToSpace() {},
      async switchClient() {},
      async focusPane() {},
      async newSession() {},
      async renameSession() {},
      async killSession() {},
      async newWindow() {},
      async newWindowReturningId() { return "@1"; },
      async renameWindow() {},
      async killWindow() {},
      async setPaneOption() {},
      async unsetPaneOption() {},
      async setSpacesStatus() {},
      async setWindowOption() {},
      async setSessionOption() {},
      async getGlobalOption() { return ""; },
      async setGlobalOption() {},
      async newSessionDetached() {},
      async newSessionDetachedWithId(
        _name: string,
        _cwd: string,
        _winName: string,
        _w: number,
        _h: number,
        command?: string[],
      ) {
        if (command) launchedCmds.push(command);
        return { windowId: "@1", paneId: "%1" };
      },
      async newWindowDetachedWithId() { return { windowId: "@2", paneId: "%2" }; },
      async splitWindow() {},
      async splitWindowWithId(_target: string, _cwd: string, command?: string[]) {
        if (command) launchedCmds.push(command);
        return "%2";
      },
      async selectLayout() {},
      async selectWindow() {},
      async selectPane() {},
      async resizePaneZoom() {},
    };

    await restoreSpace(fakeDriver, spaceSnap, {
      selfPath: "/bin/tmux-spaces",
      resumePolicy: policy,
      isDir: () => true,
    });

    expect(launchedCmds).toEqual([
      ["/bin/tmux-spaces", "__shim", "claude", "claude", "--resume", "sess-latest"],
      ["/bin/tmux-spaces", "__shim", "claude", "claude", "--resume", "sess-auth"],
      ["/bin/tmux-spaces", "__shim", "claude", "claude"],
    ]);

    store.close();
  });
});

import { describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { HarnessPluginRegistry } from "../registry";
import { HeadlessUnsupportedError, type HarnessPlugin } from "../types";
import { AiderPlugin } from "./aider";
import { AmpPlugin } from "./amp";
import { ClinePlugin } from "./cline";
import { CopilotPlugin } from "./copilot";
import { CrushPlugin } from "./crush";
import { CursorAgentPlugin } from "./cursor-agent";
import { DeepSeekPlugin } from "./deepseek";
import { DevinPlugin } from "./devin";
import { DroidPlugin } from "./droid";
import { FxPlugin } from "./fx";
import { GoosePlugin } from "./goose";
import { GrokPlugin } from "./grok";
import { HermesPlugin } from "./hermes";
import { KimiPlugin } from "./kimi";
import { KimiCliPlugin } from "./kimi-cli";
import { KiroPlugin } from "./kiro";
import { LettaPlugin } from "./letta";
import { MakiPlugin } from "./maki";
import { McodePlugin } from "./mcode";
import { OmpPlugin } from "./omp";
import { PiPlugin } from "./pi";
import { QoderCliPlugin } from "./qodercli";
import { QwenPlugin } from "./qwen";
import { RooPlugin } from "./roo";
import { VibePlugin } from "./vibe";
import { WindsurfPlugin } from "./windsurf";

function makeTempDir(prefix: string): string {
  return mkdtempSync(path.join(os.tmpdir(), prefix));
}

function writeProfile(
  spacesDir: string,
  agent: string,
  name: string,
  doc: Record<string, unknown>,
): void {
  const dir = path.join(spacesDir, "profiles", agent, name);
  mkdirSync(dir, { recursive: true });
  writeFileSync(path.join(dir, "profile.json"), JSON.stringify(doc, null, 2));
}

function createAll26Plugins(opts?: { homeDir?: string; spacesDir?: string }): HarnessPlugin[] {
  return [
    // Group 1 — 10 Harness-managed / OSC providers
    new ClinePlugin(opts),
    new CopilotPlugin(opts),
    new FxPlugin(opts),
    new GrokPlugin(opts),
    new McodePlugin(opts),
    new PiPlugin(opts),
    new QwenPlugin(opts),
    new VibePlugin(opts),
    new CrushPlugin(opts),
    new OmpPlugin(opts),
    // Group 2 — 4 JSON/JSONL-store providers
    new CursorAgentPlugin(opts),
    new DeepSeekPlugin(opts),
    new KimiPlugin(opts),
    new KimiCliPlugin(opts),
    // Group 3 — 8 Manifest-only runtime providers
    new AmpPlugin(opts),
    new DevinPlugin(opts),
    new DroidPlugin(opts),
    new HermesPlugin(opts),
    new KiroPlugin(opts),
    new LettaPlugin(opts),
    new MakiPlugin(opts),
    new QoderCliPlugin(opts),
    // Group 4 — 4 Worktree-only providers
    new AiderPlugin(opts),
    new GoosePlugin(opts),
    new WindsurfPlugin(opts),
    new RooPlugin(opts),
  ];
}

describe("26 provider plugins", () => {
  test("spec parity across all 26 providers", () => {
    const plugins = createAll26Plugins();
    expect(plugins).toHaveLength(26);

    const byId = new Map(plugins.map((p) => [p.spec.id, p]));
    expect(byId.size).toBe(26);

    const expected: Array<{
      id: string;
      aliases: string[];
      screenManifestId: string;
      oscAuthority: boolean;
      mcpTargets: string[];
      resumeFlag?: string;
      titlePrefixes?: string[];
      worktreeOnly?: boolean;
    }> = [
      // Group 1
      { id: "cline", aliases: [], screenManifestId: "cline", oscAuthority: false, mcpTargets: [".cline/cline_mcp_settings.json"] },
      { id: "copilot", aliases: ["github-copilot", "ghcs"], screenManifestId: "copilot", oscAuthority: true, mcpTargets: [".copilot/mcp-config.json"] },
      { id: "fx", aliases: ["fx-router"], screenManifestId: "", oscAuthority: true, mcpTargets: [".fx/mcp.json"] },
      { id: "grok", aliases: ["grok-build"], screenManifestId: "grok", oscAuthority: true, mcpTargets: [".grok/config.toml"] },
      { id: "mcode", aliases: [], screenManifestId: "", oscAuthority: true, mcpTargets: [".minimax/mcp.json"] },
      { id: "pi", aliases: ["herdr:pi"], screenManifestId: "pi", oscAuthority: true, mcpTargets: [".pi/agent/mcp.json"] },
      { id: "qwen", aliases: ["qwen-code", "qwen code"], screenManifestId: "qwen", oscAuthority: false, mcpTargets: [".qwen/settings.json"] },
      { id: "vibe", aliases: ["vibe-acp"], screenManifestId: "", oscAuthority: true, mcpTargets: [".vibe/config.toml"] },
      { id: "crush", aliases: [], screenManifestId: "", oscAuthority: true, mcpTargets: [] },
      { id: "omp", aliases: [], screenManifestId: "", oscAuthority: true, mcpTargets: [] },
      // Group 2
      {
        id: "cursor-agent",
        aliases: ["cursor", "cursor_agent", "cursor-cli"],
        screenManifestId: "cursor",
        oscAuthority: false,
        resumeFlag: "--resume",
        titlePrefixes: ["Cursor - ", "Cursor: ", "Cursor Agent - "],
        mcpTargets: [".cursor/mcp.json"],
      },
      {
        id: "deepseek",
        aliases: ["dsh", "dsh-tui"],
        screenManifestId: "deepseek",
        oscAuthority: true,
        resumeFlag: "--session",
        titlePrefixes: ["DeepSeek - ", "DeepSeek: "],
        mcpTargets: [".deepseek/mcp.json", ".config/deepseek/mcp.json"],
      },
      {
        id: "kimi",
        aliases: ["kimi-code", "kimi code"],
        screenManifestId: "kimi",
        oscAuthority: true,
        resumeFlag: "--session",
        titlePrefixes: ["Kimi - ", "Kimi: ", "Kimi Code - "],
        mcpTargets: [".kimi-code/mcp.json", ".kimi-code/config.toml"],
      },
      {
        id: "kimi-cli",
        aliases: [],
        screenManifestId: "kimi",
        oscAuthority: true,
        resumeFlag: "--session",
        titlePrefixes: [],
        mcpTargets: [".kimi/mcp.json", ".kimi/config.toml"],
      },
      // Group 3
      { id: "amp", aliases: ["amp-local"], screenManifestId: "amp", oscAuthority: false, mcpTargets: [] },
      { id: "devin", aliases: ["devin-cli", "devin cli"], screenManifestId: "devin", oscAuthority: false, mcpTargets: [] },
      { id: "droid", aliases: [], screenManifestId: "droid", oscAuthority: false, mcpTargets: [] },
      { id: "hermes", aliases: ["hermes-agent"], screenManifestId: "hermes", oscAuthority: false, mcpTargets: [] },
      { id: "kiro", aliases: ["kiro-cli"], screenManifestId: "kiro", oscAuthority: false, mcpTargets: [] },
      { id: "letta", aliases: ["letta-code", "letta code"], screenManifestId: "letta", oscAuthority: false, mcpTargets: [] },
      { id: "maki", aliases: [], screenManifestId: "maki", oscAuthority: false, mcpTargets: [] },
      { id: "qodercli", aliases: ["qoderclicn", "qoder", "qodercn"], screenManifestId: "qodercli", oscAuthority: false, mcpTargets: [] },
      // Group 4
      { id: "aider", aliases: [], screenManifestId: "", oscAuthority: false, mcpTargets: [], worktreeOnly: true },
      { id: "goose", aliases: [], screenManifestId: "", oscAuthority: false, mcpTargets: [], worktreeOnly: true },
      { id: "windsurf", aliases: [], screenManifestId: "", oscAuthority: false, mcpTargets: [], worktreeOnly: true },
      { id: "roo", aliases: ["roo-code"], screenManifestId: "", oscAuthority: false, mcpTargets: [], worktreeOnly: true },
    ];

    for (const exp of expected) {
      const plugin = byId.get(exp.id);
      expect(plugin).toBeDefined();
      expect(plugin!.spec.aliases).toEqual(exp.aliases);
      expect(plugin!.spec.screenManifestId).toBe(exp.screenManifestId);
      expect(plugin!.spec.oscAuthority).toBe(exp.oscAuthority);
      expect(plugin!.spec.mcpTargets).toEqual(exp.mcpTargets);
      expect(plugin!.spec.resumeFlag).toBe(exp.resumeFlag);
      if (exp.titlePrefixes) {
        expect(plugin!.spec.titlePrefixes).toEqual(exp.titlePrefixes);
      }
      expect(Boolean(plugin!.spec.worktreeOnly)).toBe(Boolean(exp.worktreeOnly));
    }
  });

  test("registry resolves all 26 IDs, aliases, runtimePlugins, and mcpTargets", () => {
    const registry = new HarnessPluginRegistry();
    const plugins = createAll26Plugins();
    for (const p of plugins) {
      registry.register(p);
    }

    expect(registry.list()).toHaveLength(26);
    expect(registry.runtimePlugins()).toHaveLength(22);

    for (const p of plugins) {
      expect(registry.get(p.spec.id)).toBe(p);
      for (const alias of p.spec.aliases) {
        expect(registry.get(alias)).toBe(p);
      }
    }

    const targets = registry.allMcpTargets();
    expect(targets).toContain(".cline/cline_mcp_settings.json");
    expect(targets).toContain(".copilot/mcp-config.json");
    expect(targets).toContain(".cursor/mcp.json");
    expect(targets).toContain(".kimi-code/config.toml");
    expect(targets).toContain(".kimi/config.toml");
  });

  test("shadow vars are injected from profile shadowHome for all shadow-capable providers", () => {
    const tmp = makeTempDir("spaces-p26-shadow-");
    try {
      const spacesDir = path.join(tmp, ".spaces");
      const cases: Array<{ plugin: HarnessPlugin; shadowVar: string }> = [
        { plugin: new ClinePlugin(), shadowVar: "CLINE_DATA_DIR" },
        { plugin: new CopilotPlugin(), shadowVar: "COPILOT_HOME" },
        { plugin: new FxPlugin(), shadowVar: "FX_HOME" },
        { plugin: new GrokPlugin(), shadowVar: "GROK_HOME" },
        { plugin: new McodePlugin(), shadowVar: "MINIMAX_DATA_DIR" },
        { plugin: new PiPlugin(), shadowVar: "PI_CODING_AGENT_DIR" },
        { plugin: new QwenPlugin(), shadowVar: "QWEN_HOME" },
        { plugin: new VibePlugin(), shadowVar: "VIBE_HOME" },
        { plugin: new DeepSeekPlugin(), shadowVar: "DSH_HOME" },
        { plugin: new KimiPlugin(), shadowVar: "KIMI_CODE_HOME" },
        { plugin: new KimiCliPlugin(), shadowVar: "KIMI_SHARE_DIR" },
      ];

      for (const { plugin, shadowVar } of cases) {
        const expectedShadow = path.join(tmp, "shadows", plugin.spec.id);
        writeProfile(spacesDir, plugin.spec.id, "work", {
          agent: plugin.spec.id,
          name: "work",
          enabled: true,
          shadow_home_path: expectedShadow,
          router: true,
        });

        const launch = plugin.buildInteractiveLaunch({
          cwd: "/repo",
          profile: "work",
          homeDir: tmp,
          spacesDir,
          env: { MODEL_ROUTER_URL: "http://127.0.0.1:4123", MODEL_ROUTER_API_KEY: "tok-1" },
        });
        expect(launch.shadowHome).toBe(expectedShadow);
        expect(launch.env[shadowVar]).toBe(expectedShadow);
      }
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  test("router env, router args, default binaries, and noRouter scrubbing", () => {
    const homeDir = "/tmp/fake-home";
    const routerEnv = {
      MODEL_ROUTER_URL: "http://127.0.0.1:4555/",
      MODEL_ROUTER_API_KEY: "sk-router-99",
    };

    // Cline: --key <apiKey>
    const clineOn = new ClinePlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(clineOn.argv).toEqual(["cline", "--key", "sk-router-99"]);
    const clineOff = new ClinePlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, noRouter: true });
    expect(clineOff.argv).toEqual(["cline"]);

    // Copilot: router env + default & custom COPILOT_MODEL
    const copilotDefault = new CopilotPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(copilotDefault.env).toEqual({
      COPILOT_PROVIDER_BASE_URL: "http://127.0.0.1:4555/v1",
      COPILOT_PROVIDER_API_KEY: "sk-router-99",
      COPILOT_PROVIDER_TYPE: "openai",
      COPILOT_PROVIDER_WIRE_API: "responses",
      COPILOT_PROVIDER_TRANSPORT: "http",
      COPILOT_MODEL: "gpt-6-luna",
    });
    const copilotCustom = new CopilotPlugin().buildInteractiveLaunch({
      cwd: "/repo",
      homeDir,
      env: { ...routerEnv, COPILOT_MODEL: "claude-sonnet-4" },
    });
    expect(copilotCustom.env.COPILOT_MODEL).toBe("claude-sonnet-4");
    const copilotOff = new CopilotPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, noRouter: true });
    expect(copilotOff.env.COPILOT_PROVIDER_BASE_URL).toBeUndefined();
    expect(copilotOff.unsetEnv).toContain("COPILOT_PROVIDER_BASE_URL");

    // Fx: defaultBinary + FX_MODEL_ROUTER_SESSION
    const fxOn = new FxPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(fxOn.argv[0]).toBe(path.join(homeDir, ".local/libexec/fx/fx-router"));
    expect(fxOn.env.FX_MODEL_ROUTER_SESSION).toBe("sk-router-99");
    const fxOff = new FxPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, noRouter: true });
    expect(fxOff.unsetEnv).toContain("FX_MODEL_ROUTER_SESSION");

    // Grok: defaultBinary + router env including GROK_HOME
    const grokOn = new GrokPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(grokOn.argv[0]).toBe(path.join(homeDir, ".local/bin/grok"));
    expect(grokOn.env).toEqual({
      GROK_HOME: path.join(homeDir, ".harness/grok/model-router"),
      GROK_MODELS_BASE_URL: "http://127.0.0.1:4555/v1",
      GROK_MODELS_LIST_URL: "http://127.0.0.1:4555/v1/models",
      XAI_API_KEY: "sk-router-99",
      GROK_MODEL_ROUTER_SESSION: "sk-router-99",
    });
    const grokOff = new GrokPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, noRouter: true });
    expect(grokOff.unsetEnv).toContain("XAI_API_KEY");

    // Vibe: defaultBinary + VIBE_ROUTER_SESSION
    const vibeOn = new VibePlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(vibeOn.argv[0]).toBe(path.join(homeDir, ".local/share/uv/tools/mistral-vibe/bin/vibe"));
    expect(vibeOn.env.VIBE_ROUTER_SESSION).toBe("sk-router-99");

    // DeepSeek: defaultBinary + defaultArgs ["web"] only when no extraArgs/sessionId
    const dshDefault = new DeepSeekPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(dshDefault.argv).toEqual([path.join(homeDir, ".local/share/deepseek-harness/bin/dsh"), "web"]);
    const dshSession = new DeepSeekPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, sessionId: "s-1" });
    expect(dshSession.argv).toEqual([path.join(homeDir, ".local/share/deepseek-harness/bin/dsh"), "--session", "s-1"]);
    const dshExtra = new DeepSeekPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, extraArgs: ["tui"] });
    expect(dshExtra.argv).toEqual([path.join(homeDir, ".local/share/deepseek-harness/bin/dsh"), "tui"]);

    // Kimi-CLI: defaultBinary + OPENAI_BASE_URL / OPENAI_API_KEY
    const kimiCliOn = new KimiCliPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv });
    expect(kimiCliOn.argv[0]).toBe(path.join(homeDir, ".local/share/kimi-cli/bin/kimi-cli"));
    expect(kimiCliOn.env).toEqual({
      OPENAI_BASE_URL: "http://127.0.0.1:4555/v1",
      OPENAI_API_KEY: "sk-router-99",
    });
    const kimiCliOff = new KimiCliPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, env: routerEnv, noRouter: true });
    expect(kimiCliOff.unsetEnv).toContain("OPENAI_BASE_URL");
    expect(kimiCliOff.unsetEnv).toContain("OPENAI_API_KEY");
  });

  test("access level flags and resumeArgs", () => {
    const homeDir = "/tmp/fake-home";

    expect(
      new CopilotPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "full", noRouter: true }).argv,
    ).toEqual(["copilot", "--allow-all-tools"]);

    expect(
      new GrokPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "full", noRouter: true }).argv,
    ).toEqual([path.join(homeDir, ".local/bin/grok"), "--yolo"]);

    const qwen = new QwenPlugin();
    expect(qwen.buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "read-only" }).argv).toEqual([
      "qwen",
      "--approval-mode",
      "plan",
    ]);
    expect(qwen.buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "auto" }).argv).toEqual([
      "qwen",
      "--approval-mode",
      "auto-edit",
    ]);
    expect(qwen.buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "full" }).argv).toEqual([
      "qwen",
      "--yolo",
    ]);

    const kimi = new KimiPlugin();
    expect(kimi.buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "auto" }).argv).toEqual(["kimi", "--yolo"]);
    expect(kimi.buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "full" }).argv).toEqual(["kimi", "--auto"]);

    expect(
      new KimiCliPlugin().buildInteractiveLaunch({ cwd: "/repo", homeDir, accessLevel: "full", noRouter: true }).argv,
    ).toEqual([path.join(homeDir, ".local/share/kimi-cli/bin/kimi-cli"), "--yolo"]);

    // Resume args
    expect(new CursorAgentPlugin().resumeArgs("cur-123")).toEqual(["--resume", "cur-123"]);
    expect(new DeepSeekPlugin().resumeArgs("dsh-456")).toEqual(["--session", "dsh-456"]);
    expect(new KimiPlugin().resumeArgs("km-789")).toEqual(["--session", "km-789"]);
    expect(new KimiCliPlugin().resumeArgs("kc-012")).toEqual(["--session", "kc-012"]);
    expect(new CursorAgentPlugin().resumeArgs("   ")).toEqual([]);
    expect(new AmpPlugin().resumeArgs("any")).toEqual([]);
  });

  test("all 26 providers throw HeadlessUnsupportedError and implement stopAndFlush/readDelta", async () => {
    for (const plugin of createAll26Plugins()) {
      expect(() =>
        plugin.startHeadless({ threadId: "t-1", cwd: "/repo" }),
      ).toThrow(HeadlessUnsupportedError);

      try {
        plugin.startHeadless({ threadId: "t-1", cwd: "/repo" });
      } catch (err) {
        expect(err).toBeInstanceOf(HeadlessUnsupportedError);
        expect((err as HeadlessUnsupportedError).agent).toBe(plugin.spec.id);
      }

      const empty = await plugin.stopAndFlush({ cwd: "/repo" });
      expect(empty).toBeNull();

      const flushed = await plugin.stopAndFlush({ cwd: "/repo", sessionId: "s-1" });
      expect(flushed).toEqual({
        nextCursor: { sessionPath: "s-1", byteOffset: 0 },
        turns: [],
      });
    }
  });

  test("Group 2 JSON/JSONL store discovery: cursor-agent and deepseek", () => {
    const tmp = makeTempDir("spaces-p26-stores-");
    try {
      // Cursor Agent store
      const cursorChatDir = path.join(tmp, ".cursor", "chats");
      mkdirSync(cursorChatDir, { recursive: true });
      const cursorFile = path.join(cursorChatDir, "chat-1.json");
      writeFileSync(
        cursorFile,
        JSON.stringify({
          id: "cur-sess-1",
          title: "Refactor auth flow",
          cwd: "/workspace/app",
          model: "claude-4-sonnet",
          messages: [
            { role: "user", content: "Please refactor auth" },
            { role: "assistant", content: "Done refactoring auth" },
          ],
        }),
      );

      const cursor = new CursorAgentPlugin({ homeDir: tmp });
      expect(cursor.storePaths(tmp)).toEqual([
        path.join(tmp, ".cursor/chats"),
        path.join(tmp, ".cursor/sessions"),
      ]);
      const cursorConvos = cursor.listConversations({ homeDir: tmp });
      expect(cursorConvos).toHaveLength(1);
      expect(cursorConvos[0]!.id).toBe("cur-sess-1");
      expect(cursorConvos[0]!.title).toBe("Refactor auth flow");
      expect(cursorConvos[0]!.cwd).toBe("/workspace/app");
      expect(cursorConvos[0]!.model).toBe("claude-4-sonnet");
      expect(cursorConvos[0]!.messageCount).toBe(2);

      const parsed = cursor.parseLog(cursorFile);
      expect(parsed).toHaveLength(2);
      expect(parsed[0]!.role).toBe("user");
      expect(parsed[1]!.role).toBe("assistant");

      expect(
        cursor.resolveVendorSession("cursor-agent", "/workspace/app", "", "Refactor auth flow", new Set()),
      ).toEqual({ sessionId: "cur-sess-1", storeAvailable: true });
      expect(
        cursor.resolveVendorSession("cursor-agent", "/workspace/app", "", "", new Set(["cur-sess-1"])),
      ).toEqual({ sessionId: "", storeAvailable: true });

      // DeepSeek store
      const dshDir = path.join(tmp, ".local", "share", "deepseek", "sessions");
      mkdirSync(dshDir, { recursive: true });
      const dshFile = path.join(dshDir, "dsh-sess-9.jsonl");
      writeFileSync(
        dshFile,
        [
          JSON.stringify({ role: "user", content: "Explain WAL checkpoints", timestamp: 1700000000 }),
          JSON.stringify({ role: "assistant", content: "WAL checkpoints flush frames", model: "deepseek-r1" }),
        ].join("\n"),
      );

      const dsh = new DeepSeekPlugin({ homeDir: tmp });
      expect(dsh.storePaths(tmp)).toEqual([
        path.join(tmp, ".deepseek/sessions"),
        path.join(tmp, ".local/share/deepseek/sessions"),
      ]);
      const dshConvos = dsh.listConversations({ homeDir: tmp });
      expect(dshConvos).toHaveLength(1);
      expect(dshConvos[0]!.id).toBe("dsh-sess-9");
      expect(dshConvos[0]!.firstMessage).toBe("Explain WAL checkpoints");
      expect(dshConvos[0]!.model).toBe("deepseek-r1");
      expect(
        dsh.resolveVendorSession("deepseek", "", "", "Explain WAL checkpoints", new Set()),
      ).toEqual({ sessionId: "dsh-sess-9", storeAvailable: true });
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  test("Group 2 Kimi session_index.jsonl enrichment and tombstones", () => {
    const tmp = makeTempDir("spaces-p26-kimi-");
    try {
      const sessionsDir = path.join(tmp, ".kimi-code", "sessions");
      mkdirSync(sessionsDir, { recursive: true });
      writeFileSync(
        path.join(sessionsDir, "keep-1.jsonl"),
        JSON.stringify({ role: "user", content: "Build API" }) + "\n",
      );
      writeFileSync(
        path.join(sessionsDir, "gone-2.jsonl"),
        JSON.stringify({ role: "user", content: "Old session" }) + "\n",
      );
      writeFileSync(
        path.join(tmp, ".kimi-code", "session_index.jsonl"),
        [
          JSON.stringify({ sessionId: "keep-1", workDir: "/proj/api", title: "API Work" }),
          JSON.stringify({ sessionId: "gone-2", workDir: "/proj/old", title: "Deleted One" }),
          JSON.stringify({ sessionId: "gone-2", deleted: true }),
        ].join("\n"),
      );

      const kimi = new KimiPlugin({ homeDir: tmp });
      const convos = kimi.listConversations({ homeDir: tmp });
      expect(convos).toHaveLength(1);
      expect(convos[0]!.id).toBe("keep-1");
      expect(convos[0]!.cwd).toBe("/proj/api");
      expect(convos[0]!.title).toBe("API Work");

      expect(
        kimi.resolveVendorSession("kimi", "/proj/api", "", "API Work", new Set()),
      ).toEqual({ sessionId: "keep-1", storeAvailable: true });
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  test("Group 2 Kimi-CLI MD5 work_dirs mapping via .kimi/kimi.json", () => {
    const tmp = makeTempDir("spaces-p26-kimicli-");
    try {
      const workDir = "/workspace/kimi-repo";
      const md5Dir = createHash("md5").update(workDir).digest("hex");
      const sessDir = path.join(tmp, ".kimi", "sessions", md5Dir);
      mkdirSync(sessDir, { recursive: true });

      writeFileSync(
        path.join(tmp, ".kimi", "kimi.json"),
        JSON.stringify({ work_dirs: [{ path: workDir }] }),
      );
      writeFileSync(
        path.join(sessDir, "kc-sess-42.jsonl"),
        [
          JSON.stringify({ role: "user", content: "Write migration tests" }),
          JSON.stringify({ role: "assistant", content: "Added migration tests", model: "kimi-k2" }),
        ].join("\n"),
      );

      const kimiCli = new KimiCliPlugin({ homeDir: tmp });
      expect(kimiCli.storePaths(tmp)).toEqual([path.join(tmp, ".kimi/sessions")]);

      const convos = kimiCli.listConversations({ homeDir: tmp });
      expect(convos).toHaveLength(1);
      expect(convos[0]!.id).toBe("kc-sess-42");
      expect(convos[0]!.cwd).toBe(workDir);
      expect(convos[0]!.model).toBe("kimi-k2");
      expect(convos[0]!.messageCount).toBe(2);

      expect(
        kimiCli.resolveVendorSession("kimi-cli", workDir, "", "", new Set()),
      ).toEqual({ sessionId: "kc-sess-42", storeAvailable: true });
      expect(
        kimiCli.resolveVendorSession("kimi-cli", "/other/dir", "", "", new Set()),
      ).toEqual({ sessionId: "", storeAvailable: true });
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });
});

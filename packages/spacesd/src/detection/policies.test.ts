import { describe, expect, test } from "bun:test";
import {
  ANTIGRAVITY_HOOK_IDLE_GRACE_MS,
  AntigravityHookPolicy,
  BaseHookPolicy,
  DEFAULT_STALE_LOCAL_CMD_MS,
  DetectionRegistry,
  commandName,
  defaultDetectionRegistry,
  isShellApp,
  isShellCommand,
  normalizeCommand,
} from "./policies";

describe("detection/policies", () => {
  test("normalizeCommand matches Go TestNormalizeCommand", () => {
    const cases: Record<string, string> = {
      claude: "claude",
      "/usr/bin/claude-code": "claude-code",
      "herdr:opencode": "opencode",
      "muse-bin-1.4.24-R9131.1": "muse",
      "claude-2.1.76": "claude",
      "codex-0.42.0.exe": "codex",
      "  Kimi-CLI.JS  ": "kimi-cli",
      "wrapper.cmd": "wrapper",
      "script.bat": "script",
      "tool.ps1": "tool",
      "agent.mjs": "agent",
      "runner.cjs": "runner",
      "   ": "",
      ".exe": "",
    };

    for (const [input, want] of Object.entries(cases)) {
      expect(normalizeCommand(input)).toBe(want);
    }
  });

  test("shell and command helpers match Go util", () => {
    for (const sh of ["sh", "bash", "zsh", "-zsh", "fish", "nu", "nushell", "dash", "ksh", "tcsh", "csh"]) {
      expect(isShellCommand(sh)).toBe(true);
      expect(isShellApp(sh)).toBe(true);
    }
    expect(isShellApp("shell")).toBe(true);
    expect(isShellApp("  SHELL ")).toBe(true);
    expect(isShellCommand("shell")).toBe(false);
    expect(isShellCommand("claude")).toBe(false);
    expect(isShellApp("claude")).toBe(false);

    expect(commandName("opencode --auto")).toBe("opencode");
    expect(commandName("/usr/bin/pi")).toBe("pi");
    expect(commandName("/usr/bin/pi/")).toBe("pi");
    expect(commandName("   ")).toBe("");
  });

  test("built-in registry counts, canonical lookups, manifests, and OSCAuthority parity", () => {
    const reg = new DetectionRegistry();
    expect(reg).not.toBe(defaultDetectionRegistry());

    const all = reg.all();
    const runtime = reg.runtimeSpecs();
    const worktreeOnly = all.filter((s) => s.worktreeOnly);
    const oscAuthTrue = runtime.filter((s) => s.oscAuthority);
    const oscAuthFalse = runtime.filter((s) => !s.oscAuthority);

    expect(all.length).toBe(33);
    expect(runtime.length).toBe(29);
    expect(worktreeOnly.length).toBe(4);
    expect(oscAuthTrue.length).toBe(15);
    expect(oscAuthFalse.length).toBe(14);
    expect(worktreeOnly.every((s) => !s.oscAuthority)).toBe(true);

    const expectedCanonical: Record<string, string> = {
      claude: "claude",
      "claude-code": "claude",
      agy: "antigravity",
      antigravity: "antigravity",
      "antigravity-cli": "antigravity",
      cursor: "cursor-agent",
      "cursor-agent": "cursor-agent",
      dsh: "deepseek",
      "dsh-tui": "deepseek",
      deepseek: "deepseek",
      kimi: "kimi",
      "kimi-cli": "kimi-cli",
      "kimi-code": "kimi",
      "open-code": "opencode",
      "herdr:opencode": "opencode",
      "muse-bin-1.4.2": "muse",
      "grok-build": "grok",
      "github-copilot": "copilot",
      ghcs: "copilot",
      "fx-router": "fx",
      "vibe-acp": "vibe",
      qoder: "qodercli",
    };

    for (const [alias, want] of Object.entries(expectedCanonical)) {
      expect(reg.canonicalId(alias)).toBe(want);
      expect(reg.canonicalOrNormalized(alias)).toBe(want);
      expect(reg.isKnownAgent(alias)).toBe(true);
      expect(reg.resolveRuntime(alias)?.id).toBe(want);
    }

    // Worktree-only agents must NOT resolve as runtime agents.
    for (const wt of ["aider", "goose", "windsurf", "roo", "roo-code"]) {
      expect(reg.isKnownAgent(wt)).toBe(false);
      expect(reg.resolveRuntime(wt)).toBeUndefined();
      expect(reg.resolve(wt)).toBeDefined();
      expect(reg.isOscAuthority(wt)).toBe(false);
      expect(reg.screenManifestId(wt)).toBe("");
    }

    // Exact 15 OSCAuthority=true runtime agents
    const expectedOscTrue = [
      "claude",
      "claude-code",
      "codex",
      "openai-codex",
      "deepseek",
      "dsh",
      "kimi",
      "kimi-code",
      "kimi-cli",
      "muse",
      "opencode",
      "oc",
      "copilot",
      "ghcs",
      "fx",
      "fx-router",
      "grok",
      "mcode",
      "pi",
      "herdr:pi",
      "vibe",
      "vibe-acp",
      "crush",
      "omp",
    ];
    for (const name of expectedOscTrue) {
      expect(reg.isOscAuthority(name)).toBe(true);
    }

    // Exact 14 OSCAuthority=false runtime agents
    const expectedOscFalse = [
      "antigravity",
      "agy",
      "cursor-agent",
      "cursor",
      "kilo",
      "cline",
      "gemini",
      "gemini-cli",
      "qwen",
      "amp",
      "devin",
      "droid",
      "hermes",
      "kiro",
      "letta",
      "maki",
      "qodercli",
      "qoder",
      "unknown-tool",
    ];
    for (const name of expectedOscFalse) {
      expect(reg.isOscAuthority(name)).toBe(false);
    }

    // Screen manifest ID mappings
    expect(reg.screenManifestId("antigravity")).toBe("agy");
    expect(reg.screenManifestId("agy")).toBe("agy");
    expect(reg.screenManifestId("cursor-agent")).toBe("cursor");
    expect(reg.screenManifestId("cursor")).toBe("cursor");
    expect(reg.screenManifestId("claude-code")).toBe("claude");
    expect(reg.screenManifestId("kimi-cli")).toBe("kimi");
    expect(reg.screenManifestId("fx")).toBe("");
    expect(reg.screenManifestId("unknown")).toBe("");

    // Registry method delegation for shell/command & canonicalOrNormalized fallback
    expect(reg.canonicalOrNormalized("/usr/bin/custom-tool-1.0.0.js")).toBe("custom-tool-1.0.0");
    expect(reg.canonicalOrNormalized("/usr/bin/custom-1.0.0.js")).toBe("custom");
    expect(reg.isShellApp("shell")).toBe(true);
    expect(reg.isShellCommand("-bash")).toBe(true);
    expect(reg.commandName("/usr/local/bin/codex --model gpt-5")).toBe("codex");
  });

  test("custom registry respects primary-ID protection and worktree-only precedence", () => {
    const reg = DetectionRegistry.empty();
    reg.register({ id: "alpha", aliases: ["shared-alias"], oscAuthority: true });
    reg.register({ id: "beta", aliases: ["alpha"], screenManifestId: "alpha" });
    reg.register({ id: "wt", aliases: ["shared-alias"], worktreeOnly: true });

    expect(reg.canonicalId("alpha")).toBe("alpha");
    expect(reg.canonicalId("shared-alias")).toBe("alpha");
    expect(reg.canonicalId("beta")).toBe("beta");
  });

  test("AntigravityHookPolicy and BaseHookPolicy match Go TestAntigravityPluginHookPolicy", () => {
    expect(DEFAULT_STALE_LOCAL_CMD_MS).toBe(120_000);
    expect(ANTIGRAVITY_HOOK_IDLE_GRACE_MS).toBe(10_000);

    const reg = defaultDetectionRegistry();
    const hp = reg.hookPolicyFor("antigravity");
    expect(hp).toBeInstanceOf(AntigravityHookPolicy);
    expect(hp.oscAuthority()).toBe(false);

    // 10s idle grace when visibly idle without finite work
    expect(
      hp.shouldYieldWorkingHookToIdle({
        hookState: "working",
        hookSilenceMs: 10_000,
        hasActiveFiniteWork: false,
        hasOnlyLocalCmdBackground: false,
        visibleIdle: true,
        visibleWorking: false,
        visibleBlocker: false,
        screenState: "idle",
      }),
    ).toBe(true);

    // Below 10s grace: does NOT yield
    expect(
      hp.shouldYieldWorkingHookToIdle({
        hookState: "working",
        hookSilenceMs: 9_999,
        hasActiveFiniteWork: false,
        hasOnlyLocalCmdBackground: false,
        visibleIdle: true,
        visibleWorking: false,
        visibleBlocker: false,
        screenState: "idle",
      }),
    ).toBe(false);

    // Active finite work (e.g. subagent): does NOT yield
    expect(
      hp.shouldYieldWorkingHookToIdle({
        hookState: "working",
        hookSilenceMs: 15_000,
        hasActiveFiniteWork: true,
        hasOnlyLocalCmdBackground: false,
        visibleIdle: true,
        visibleWorking: false,
        visibleBlocker: false,
        screenState: "idle",
      }),
    ).toBe(false);

    // BaseHookPolicy yields stale (>120s) local_bash-only working hooks when visibly idle
    const basePolicy = new BaseHookPolicy({ authority: true });
    expect(basePolicy.oscAuthority()).toBe(true);
    expect(
      basePolicy.shouldYieldWorkingHookToIdle({
        hookState: "working",
        hookSilenceMs: 120_000,
        hasActiveFiniteWork: true,
        hasOnlyLocalCmdBackground: true,
        visibleIdle: true,
        visibleWorking: false,
        visibleBlocker: false,
        screenState: "idle",
      }),
    ).toBe(false);
    expect(
      basePolicy.shouldYieldWorkingHookToIdle({
        hookState: "working",
        hookSilenceMs: 120_001,
        hasActiveFiniteWork: true,
        hasOnlyLocalCmdBackground: true,
        visibleIdle: true,
        visibleWorking: false,
        visibleBlocker: false,
        screenState: "idle",
      }),
    ).toBe(true);
  });
});

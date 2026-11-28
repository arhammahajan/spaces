import { HarnessPluginRegistry } from "../registry";
import { AiderPlugin } from "./aider";
import { AmpPlugin } from "./amp";
import { AntigravityPlugin } from "./antigravity";
import { ClaudePlugin } from "./claude";
import { ClinePlugin } from "./cline";
import { CodexPlugin } from "./codex";
import { CopilotPlugin } from "./copilot";
import { CrushPlugin } from "./crush";
import { CursorAgentPlugin } from "./cursor-agent";
import { DeepSeekPlugin } from "./deepseek";
import { DevinPlugin } from "./devin";
import { DroidPlugin } from "./droid";
import { FxPlugin } from "./fx";
import { GeminiPlugin } from "./gemini";
import { GoosePlugin } from "./goose";
import { GrokPlugin } from "./grok";
import { HermesPlugin } from "./hermes";
import { KiloPlugin } from "./kilo";
import { KimiPlugin } from "./kimi";
import { KimiCliPlugin } from "./kimi-cli";
import { KiroPlugin } from "./kiro";
import { LettaPlugin } from "./letta";
import { MakiPlugin } from "./maki";
import { McodePlugin } from "./mcode";
import { MusePlugin } from "./muse";
import { OmpPlugin } from "./omp";
import { OpenCodePlugin } from "./opencode";
import { PiPlugin } from "./pi";
import { QoderCliPlugin } from "./qodercli";
import { QwenPlugin } from "./qwen";
import { RooPlugin } from "./roo";
import { VibePlugin } from "./vibe";
import { WindsurfPlugin } from "./windsurf";

export interface DefaultPluginRegistryOptions {
  homeDir?: string;
  spacesDir?: string;
}

/**
 * Creates a `HarnessPluginRegistry` pre-populated with all 33 provider plugins
 * (each self-contained in `plugins/providers/<id>.ts` and normalized to `HarnessPlugin`).
 */
export function createDefaultPluginRegistry(
  opts: DefaultPluginRegistryOptions = {},
): HarnessPluginRegistry {
  const registry = new HarnessPluginRegistry();
  const baseOpts = {
    ...(opts.homeDir ? { homeDir: opts.homeDir } : {}),
    ...(opts.spacesDir ? { spacesDir: opts.spacesDir } : {}),
  };

  // Dedicated & store-backed providers
  registry.register(new ClaudePlugin(baseOpts));
  registry.register(new CodexPlugin(baseOpts));
  registry.register(new AntigravityPlugin(baseOpts));
  registry.register(new GeminiPlugin(baseOpts));
  registry.register(new OpenCodePlugin(baseOpts));
  registry.register(new KiloPlugin(baseOpts));
  registry.register(new MusePlugin(baseOpts));
  registry.register(new CursorAgentPlugin(baseOpts));
  registry.register(new DeepSeekPlugin(baseOpts));
  registry.register(new KimiPlugin(baseOpts));
  registry.register(new KimiCliPlugin(baseOpts));

  // Harness-managed & OSC providers
  registry.register(new ClinePlugin(baseOpts));
  registry.register(new CopilotPlugin(baseOpts));
  registry.register(new FxPlugin(baseOpts));
  registry.register(new GrokPlugin(baseOpts));
  registry.register(new McodePlugin(baseOpts));
  registry.register(new PiPlugin(baseOpts));
  registry.register(new QwenPlugin(baseOpts));
  registry.register(new VibePlugin(baseOpts));
  registry.register(new CrushPlugin(baseOpts));
  registry.register(new OmpPlugin(baseOpts));

  // Manifest-backed runtime providers
  registry.register(new AmpPlugin(baseOpts));
  registry.register(new DevinPlugin(baseOpts));
  registry.register(new DroidPlugin(baseOpts));
  registry.register(new HermesPlugin(baseOpts));
  registry.register(new KiroPlugin(baseOpts));
  registry.register(new LettaPlugin(baseOpts));
  registry.register(new MakiPlugin(baseOpts));
  registry.register(new QoderCliPlugin(baseOpts));

  // Worktree-naming-only providers
  registry.register(new AiderPlugin(baseOpts));
  registry.register(new GoosePlugin(baseOpts));
  registry.register(new WindsurfPlugin(baseOpts));
  registry.register(new RooPlugin(baseOpts));

  return registry;
}

export {
  AiderPlugin,
  AmpPlugin,
  AntigravityPlugin,
  ClaudePlugin,
  ClinePlugin,
  CodexPlugin,
  CopilotPlugin,
  CrushPlugin,
  CursorAgentPlugin,
  DeepSeekPlugin,
  DevinPlugin,
  DroidPlugin,
  FxPlugin,
  GeminiPlugin,
  GoosePlugin,
  GrokPlugin,
  HermesPlugin,
  KiloPlugin,
  KimiCliPlugin,
  KimiPlugin,
  KiroPlugin,
  LettaPlugin,
  MakiPlugin,
  McodePlugin,
  MusePlugin,
  OmpPlugin,
  OpenCodePlugin,
  PiPlugin,
  QoderCliPlugin,
  QwenPlugin,
  RooPlugin,
  VibePlugin,
  WindsurfPlugin,
};

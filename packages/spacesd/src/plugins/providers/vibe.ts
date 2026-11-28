import {
  buildStandardInteractiveLaunch,
  buildStandardResumeArgs,
  createEmptyDelta,
  runStandardStopAndFlush,
  unsupportedHeadless,
  type StandardLaunchConfig,
} from "../helpers/launch";
import type {
  DeltaCursor,
  DeltaReadResult,
  HarnessPlugin,
  HarnessPluginSpec,
  InteractiveLaunchRequest,
  InteractiveLaunchSpec,
  StopAndFlushOptions,
} from "../types";

export const VIBE_SPEC: HarnessPluginSpec = {
  id: "vibe",
  aliases: ["vibe-acp"],
  displayApp: "vibe",
  cliDisplayApp: "vibe",
  binary: "vibe",
  screenManifestId: "",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".vibe/config.toml"],
};

const VIBE_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "VIBE_HOME",
  defaultBinary: "~/.local/share/uv/tools/mistral-vibe/bin/vibe",
  routerEnv: (router) => ({
    VIBE_ROUTER_SESSION: router.apiKey,
  }),
  noRouterUnsets: ["VIBE_ROUTER_SESSION"],
};

export interface VibePluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class VibePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = VIBE_SPEC;
  private readonly defaults: VibePluginOptions;

  constructor(opts: VibePluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, VIBE_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(VIBE_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

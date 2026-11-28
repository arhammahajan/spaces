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

export const FX_SPEC: HarnessPluginSpec = {
  id: "fx",
  aliases: ["fx-router"],
  displayApp: "fx",
  cliDisplayApp: "fx",
  binary: "fx",
  screenManifestId: "",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".fx/mcp.json"],
};

const FX_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "FX_HOME",
  defaultBinary: "~/.local/libexec/fx/fx-router",
  routerEnv: (router) => ({
    FX_MODEL_ROUTER_SESSION: router.apiKey,
  }),
  noRouterUnsets: ["FX_MODEL_ROUTER_SESSION"],
};

export interface FxPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class FxPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = FX_SPEC;
  private readonly defaults: FxPluginOptions;

  constructor(opts: FxPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, FX_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(FX_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

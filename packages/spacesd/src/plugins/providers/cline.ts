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

export const CLINE_SPEC: HarnessPluginSpec = {
  id: "cline",
  aliases: [],
  displayApp: "cline",
  cliDisplayApp: "cline",
  binary: "cline",
  screenManifestId: "cline",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".cline/cline_mcp_settings.json"],
};

const CLINE_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "CLINE_DATA_DIR",
  routerArgs: (router) => ["--key", router.apiKey],
};

export interface ClinePluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class ClinePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = CLINE_SPEC;
  private readonly defaults: ClinePluginOptions;

  constructor(opts: ClinePluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, CLINE_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(CLINE_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

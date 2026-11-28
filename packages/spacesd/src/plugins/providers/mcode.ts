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

export const MCODE_SPEC: HarnessPluginSpec = {
  id: "mcode",
  aliases: [],
  displayApp: "mcode",
  cliDisplayApp: "mcode",
  binary: "mcode",
  screenManifestId: "",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".minimax/mcp.json"],
};

const MCODE_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "MINIMAX_DATA_DIR",
};

export interface McodePluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class McodePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = MCODE_SPEC;
  private readonly defaults: McodePluginOptions;

  constructor(opts: McodePluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, MCODE_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(MCODE_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

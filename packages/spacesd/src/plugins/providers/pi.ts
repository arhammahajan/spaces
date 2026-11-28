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

export const PI_SPEC: HarnessPluginSpec = {
  id: "pi",
  aliases: ["herdr:pi"],
  displayApp: "pi",
  cliDisplayApp: "pi",
  binary: "pi",
  screenManifestId: "pi",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".pi/agent/mcp.json"],
};

const PI_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "PI_CODING_AGENT_DIR",
};

export interface PiPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class PiPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = PI_SPEC;
  private readonly defaults: PiPluginOptions;

  constructor(opts: PiPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, PI_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(PI_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

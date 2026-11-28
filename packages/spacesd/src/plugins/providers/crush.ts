import {
  buildStandardInteractiveLaunch,
  buildStandardResumeArgs,
  createEmptyDelta,
  runStandardStopAndFlush,
  unsupportedHeadless,
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

export const CRUSH_SPEC: HarnessPluginSpec = {
  id: "crush",
  aliases: [],
  displayApp: "crush",
  cliDisplayApp: "crush",
  binary: "crush",
  screenManifestId: "",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface CrushPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class CrushPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = CRUSH_SPEC;
  private readonly defaults: CrushPluginOptions;

  constructor(opts: CrushPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(CRUSH_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

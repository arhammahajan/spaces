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

export const OMP_SPEC: HarnessPluginSpec = {
  id: "omp",
  aliases: [],
  displayApp: "omp",
  cliDisplayApp: "omp",
  binary: "omp",
  screenManifestId: "",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface OmpPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class OmpPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = OMP_SPEC;
  private readonly defaults: OmpPluginOptions;

  constructor(opts: OmpPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(OMP_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

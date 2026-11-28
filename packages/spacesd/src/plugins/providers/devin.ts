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

export const DEVIN_SPEC: HarnessPluginSpec = {
  id: "devin",
  aliases: ["devin-cli", "devin cli"],
  displayApp: "devin",
  cliDisplayApp: "devin",
  binary: "devin",
  screenManifestId: "devin",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface DevinPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class DevinPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = DEVIN_SPEC;
  private readonly defaults: DevinPluginOptions;

  constructor(opts: DevinPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(DEVIN_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

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

export const ROO_SPEC: HarnessPluginSpec = {
  id: "roo",
  aliases: ["roo-code"],
  displayApp: "roo",
  cliDisplayApp: "roo",
  binary: "roo",
  screenManifestId: "",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  worktreeOnly: true,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface RooPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class RooPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = ROO_SPEC;
  private readonly defaults: RooPluginOptions;

  constructor(opts: RooPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(ROO_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

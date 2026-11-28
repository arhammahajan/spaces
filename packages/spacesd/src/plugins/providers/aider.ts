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

export const AIDER_SPEC: HarnessPluginSpec = {
  id: "aider",
  aliases: [],
  displayApp: "aider",
  cliDisplayApp: "aider",
  binary: "aider",
  screenManifestId: "",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  worktreeOnly: true,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface AiderPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class AiderPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = AIDER_SPEC;
  private readonly defaults: AiderPluginOptions;

  constructor(opts: AiderPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(AIDER_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

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

export const WINDSURF_SPEC: HarnessPluginSpec = {
  id: "windsurf",
  aliases: [],
  displayApp: "windsurf",
  cliDisplayApp: "windsurf",
  binary: "windsurf",
  screenManifestId: "",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  worktreeOnly: true,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface WindsurfPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class WindsurfPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = WINDSURF_SPEC;
  private readonly defaults: WindsurfPluginOptions;

  constructor(opts: WindsurfPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(WINDSURF_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

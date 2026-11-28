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

export const GOOSE_SPEC: HarnessPluginSpec = {
  id: "goose",
  aliases: [],
  displayApp: "goose",
  cliDisplayApp: "goose",
  binary: "goose",
  screenManifestId: "",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  worktreeOnly: true,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface GoosePluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class GoosePlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = GOOSE_SPEC;
  private readonly defaults: GoosePluginOptions;

  constructor(opts: GoosePluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(GOOSE_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

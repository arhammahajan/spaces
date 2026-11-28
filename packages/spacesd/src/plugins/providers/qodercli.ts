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

export const QODERCLI_SPEC: HarnessPluginSpec = {
  id: "qodercli",
  aliases: ["qoderclicn", "qoder", "qodercn"],
  displayApp: "qodercli",
  cliDisplayApp: "qodercli",
  binary: "qodercli",
  screenManifestId: "qodercli",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [],
};

export interface QoderCliPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class QoderCliPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = QODERCLI_SPEC;
  private readonly defaults: QoderCliPluginOptions;

  constructor(opts: QoderCliPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(QODERCLI_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

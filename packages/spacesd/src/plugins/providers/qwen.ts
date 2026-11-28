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

export const QWEN_SPEC: HarnessPluginSpec = {
  id: "qwen",
  aliases: ["qwen-code", "qwen code"],
  displayApp: "qwen",
  cliDisplayApp: "qwen",
  binary: "qwen",
  screenManifestId: "qwen",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".qwen/settings.json"],
};

const QWEN_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "QWEN_HOME",
  accessArgs: {
    "read-only": ["--approval-mode", "plan"],
    auto: ["--approval-mode", "auto-edit"],
    full: ["--yolo"],
  },
};

export interface QwenPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class QwenPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = QWEN_SPEC;
  private readonly defaults: QwenPluginOptions;

  constructor(opts: QwenPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, QWEN_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(QWEN_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

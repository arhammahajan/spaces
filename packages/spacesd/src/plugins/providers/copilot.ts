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

export const COPILOT_SPEC: HarnessPluginSpec = {
  id: "copilot",
  aliases: ["github-copilot", "ghcs"],
  displayApp: "copilot",
  cliDisplayApp: "copilot",
  binary: "copilot",
  screenManifestId: "copilot",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".copilot/mcp-config.json"],
};

const COPILOT_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "COPILOT_HOME",
  routerEnv: (router, _homeDir, env) => ({
    COPILOT_PROVIDER_BASE_URL: `${router.url}/v1`,
    COPILOT_PROVIDER_API_KEY: router.apiKey,
    COPILOT_PROVIDER_TYPE: "openai",
    COPILOT_PROVIDER_WIRE_API: "responses",
    COPILOT_PROVIDER_TRANSPORT: "http",
    COPILOT_MODEL: env.COPILOT_MODEL || "gpt-6-luna",
  }),
  noRouterUnsets: [
    "COPILOT_PROVIDER_BASE_URL",
    "COPILOT_PROVIDER_API_KEY",
    "COPILOT_PROVIDER_TYPE",
    "COPILOT_PROVIDER_WIRE_API",
    "COPILOT_PROVIDER_TRANSPORT",
  ],
  accessArgs: {
    full: ["--allow-all-tools"],
  },
};

export interface CopilotPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class CopilotPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = COPILOT_SPEC;
  private readonly defaults: CopilotPluginOptions;

  constructor(opts: CopilotPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, COPILOT_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(COPILOT_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

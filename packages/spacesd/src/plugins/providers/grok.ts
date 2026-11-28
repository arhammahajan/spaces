import * as path from "node:path";
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

export const GROK_SPEC: HarnessPluginSpec = {
  id: "grok",
  aliases: ["grok-build"],
  displayApp: "grok",
  cliDisplayApp: "grok",
  binary: "grok",
  screenManifestId: "grok",
  oscAuthority: true,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".grok/config.toml"],
};

const GROK_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "GROK_HOME",
  defaultBinary: "~/.local/bin/grok",
  routerEnv: (router, homeDir) => ({
    GROK_HOME: path.join(homeDir, ".harness/grok/model-router"),
    GROK_MODELS_BASE_URL: `${router.url}/v1`,
    GROK_MODELS_LIST_URL: `${router.url}/v1/models`,
    XAI_API_KEY: router.apiKey,
    GROK_MODEL_ROUTER_SESSION: router.apiKey,
  }),
  noRouterUnsets: [
    "XAI_API_KEY",
    "GROK_MODELS_BASE_URL",
    "GROK_MODELS_LIST_URL",
    "GROK_MODEL_ROUTER_SESSION",
  ],
  accessArgs: {
    full: ["--yolo"],
  },
  customize: (spec) => {
    if (spec.shadowHome) {
      spec.env.GROK_HOME = spec.shadowHome;
    }
    return spec;
  },
};

export interface GrokPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class GrokPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = GROK_SPEC;
  private readonly defaults: GrokPluginOptions;

  constructor(opts: GrokPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, GROK_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(GROK_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

import * as path from "node:path";
import type { VendorSessionLookup } from "../../resurrect/capture";
import {
  listJsonStoreConversations,
  parseJsonOrJsonlLog,
  resolveJsonStoreVendorSession,
  type JsonStoreScanSpec,
} from "../helpers/json-store";
import {
  buildStandardInteractiveLaunch,
  buildStandardResumeArgs,
  createEmptyDelta,
  runStandardStopAndFlush,
  unsupportedHeadless,
  type StandardLaunchConfig,
} from "../helpers/launch";
import { realHomeDir } from "../profiles";
import type {
  ConversationListOptions,
  ConversationRecord,
  DeltaCursor,
  DeltaReadResult,
  HarnessPlugin,
  HarnessPluginSpec,
  InteractiveLaunchRequest,
  InteractiveLaunchSpec,
  ParsedLogMessage,
  StopAndFlushOptions,
} from "../types";

export const KIMI_CLI_SPEC: HarnessPluginSpec = {
  id: "kimi-cli",
  aliases: [],
  displayApp: "kimi-cli",
  cliDisplayApp: "kimi-cli",
  binary: "kimi-cli",
  screenManifestId: "kimi",
  oscAuthority: true,
  resumeFlag: "--session",
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".kimi/mcp.json", ".kimi/config.toml"],
};

const KIMI_CLI_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "KIMI_SHARE_DIR",
  defaultBinary: "~/.local/share/kimi-cli/bin/kimi-cli",
  routerEnv: (router) => ({
    OPENAI_BASE_URL: `${router.url}/v1`,
    OPENAI_API_KEY: router.apiKey,
  }),
  noRouterUnsets: ["OPENAI_BASE_URL", "OPENAI_API_KEY"],
  accessArgs: {
    full: ["--yolo"],
  },
};

const KIMI_CLI_SCAN_SPEC: JsonStoreScanSpec = {
  agent: KIMI_CLI_SPEC.id,
  app: KIMI_CLI_SPEC.displayApp,
  relDirs: [".kimi/sessions"],
  kimiJsonRelPath: ".kimi/kimi.json",
};

export interface KimiCliPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class KimiCliPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = KIMI_CLI_SPEC;
  private readonly defaults: KimiCliPluginOptions;

  constructor(opts: KimiCliPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return KIMI_CLI_SCAN_SPEC.relDirs.map((rel) => path.join(homeDir, rel));
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    return listJsonStoreConversations(KIMI_CLI_SCAN_SPEC, opts);
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    return parseJsonOrJsonlLog(sourcePath);
  }

  resolveVendorSession: VendorSessionLookup = (_agent, cwd, profile, title, exclude) => {
    const home = realHomeDir(this.defaults.homeDir);
    return resolveJsonStoreVendorSession(KIMI_CLI_SCAN_SPEC, home, cwd, profile, title, exclude);
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, KIMI_CLI_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(KIMI_CLI_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

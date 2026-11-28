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

export const KIMI_SPEC: HarnessPluginSpec = {
  id: "kimi",
  aliases: ["kimi-code", "kimi code"],
  displayApp: "kimi",
  cliDisplayApp: "kimi",
  binary: "kimi",
  screenManifestId: "kimi",
  oscAuthority: true,
  resumeFlag: "--session",
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: ["Kimi - ", "Kimi: ", "Kimi Code - "],
  mcpTargets: [".kimi-code/mcp.json", ".kimi-code/config.toml"],
};

const KIMI_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "KIMI_CODE_HOME",
  accessArgs: {
    auto: ["--yolo"],
    full: ["--auto"],
  },
};

const KIMI_SCAN_SPEC: JsonStoreScanSpec = {
  agent: KIMI_SPEC.id,
  app: KIMI_SPEC.displayApp,
  relDirs: [".kimi-code/sessions"],
  indexRelPath: ".kimi-code/session_index.jsonl",
};

export interface KimiPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class KimiPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = KIMI_SPEC;
  private readonly defaults: KimiPluginOptions;

  constructor(opts: KimiPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return KIMI_SCAN_SPEC.relDirs.map((rel) => path.join(homeDir, rel));
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    return listJsonStoreConversations(KIMI_SCAN_SPEC, opts);
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    return parseJsonOrJsonlLog(sourcePath);
  }

  resolveVendorSession: VendorSessionLookup = (_agent, cwd, profile, title, exclude) => {
    const home = realHomeDir(this.defaults.homeDir);
    return resolveJsonStoreVendorSession(KIMI_SCAN_SPEC, home, cwd, profile, title, exclude);
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, KIMI_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(KIMI_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

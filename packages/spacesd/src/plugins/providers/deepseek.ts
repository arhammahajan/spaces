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

export const DEEPSEEK_SPEC: HarnessPluginSpec = {
  id: "deepseek",
  aliases: ["dsh", "dsh-tui"],
  displayApp: "deepseek",
  cliDisplayApp: "deepseek",
  binary: "deepseek",
  screenManifestId: "deepseek",
  oscAuthority: true,
  resumeFlag: "--session",
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: ["DeepSeek - ", "DeepSeek: "],
  mcpTargets: [".deepseek/mcp.json", ".config/deepseek/mcp.json"],
};

const DEEPSEEK_LAUNCH_CONFIG: StandardLaunchConfig = {
  shadowVar: "DSH_HOME",
  defaultBinary: "~/.local/share/deepseek-harness/bin/dsh",
  defaultArgs: ["web"],
};

const DEEPSEEK_SCAN_SPEC: JsonStoreScanSpec = {
  agent: DEEPSEEK_SPEC.id,
  app: DEEPSEEK_SPEC.displayApp,
  relDirs: [".deepseek/sessions", ".local/share/deepseek/sessions"],
};

export interface DeepSeekPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class DeepSeekPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = DEEPSEEK_SPEC;
  private readonly defaults: DeepSeekPluginOptions;

  constructor(opts: DeepSeekPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return DEEPSEEK_SCAN_SPEC.relDirs.map((rel) => path.join(homeDir, rel));
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    return listJsonStoreConversations(DEEPSEEK_SCAN_SPEC, opts);
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    return parseJsonOrJsonlLog(sourcePath);
  }

  resolveVendorSession: VendorSessionLookup = (_agent, cwd, profile, title, exclude) => {
    const home = realHomeDir(this.defaults.homeDir);
    return resolveJsonStoreVendorSession(DEEPSEEK_SCAN_SPEC, home, cwd, profile, title, exclude);
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, DEEPSEEK_LAUNCH_CONFIG, this.defaults);
  }

  startHeadless = unsupportedHeadless(DEEPSEEK_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

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

export const CURSOR_AGENT_SPEC: HarnessPluginSpec = {
  id: "cursor-agent",
  aliases: ["cursor", "cursor_agent", "cursor-cli"],
  displayApp: "cursor-agent",
  cliDisplayApp: "cursor-agent",
  binary: "cursor-agent",
  screenManifestId: "cursor",
  oscAuthority: false,
  resumeFlag: "--resume",
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: ["Cursor - ", "Cursor: ", "Cursor Agent - "],
  mcpTargets: [".cursor/mcp.json"],
};

const CURSOR_AGENT_SCAN_SPEC: JsonStoreScanSpec = {
  agent: CURSOR_AGENT_SPEC.id,
  app: CURSOR_AGENT_SPEC.displayApp,
  relDirs: [".cursor/chats", ".cursor/sessions"],
};

export interface CursorAgentPluginOptions {
  homeDir?: string;
  spacesDir?: string;
}

export class CursorAgentPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = CURSOR_AGENT_SPEC;
  private readonly defaults: CursorAgentPluginOptions;

  constructor(opts: CursorAgentPluginOptions = {}) {
    this.defaults = opts;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  storePaths(homeDir: string): string[] {
    return CURSOR_AGENT_SCAN_SPEC.relDirs.map((rel) => path.join(homeDir, rel));
  }

  listConversations(opts: ConversationListOptions): ConversationRecord[] {
    return listJsonStoreConversations(CURSOR_AGENT_SCAN_SPEC, opts);
  }

  parseLog(sourcePath: string): ParsedLogMessage[] {
    return parseJsonOrJsonlLog(sourcePath);
  }

  resolveVendorSession: VendorSessionLookup = (_agent, cwd, profile, title, exclude) => {
    const home = realHomeDir(this.defaults.homeDir);
    return resolveJsonStoreVendorSession(CURSOR_AGENT_SCAN_SPEC, home, cwd, profile, title, exclude);
  };

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    return buildStandardInteractiveLaunch(this.spec, req, {}, this.defaults);
  }

  startHeadless = unsupportedHeadless(CURSOR_AGENT_SPEC.id);

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }

  readDelta(sessionId: string, _cwd: string, cursor?: DeltaCursor): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }
}

import { existsSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { GeminiHeadlessExecutor } from "../../executors/gemini";
import {
  buildStandardInteractiveLaunch,
  buildStandardResumeArgs,
  createEmptyDelta,
  expandTilde,
  runStandardStopAndFlush,
} from "../helpers/launch";
import { defaultSpacesDir, loadProfileConfig, realHomeDir } from "../profiles";
import type {
  AccessLevel,
  DeltaCursor,
  DeltaReadResult,
  HarnessPlugin,
  HarnessPluginSpec,
  HeadlessSession,
  HeadlessStartRequest,
  InteractiveLaunchRequest,
  InteractiveLaunchSpec,
  StopAndFlushOptions,
} from "../types";

const GEMINI_ACCESS_ARGS: Partial<Record<AccessLevel, string[]>> = {
  "read-only": ["--approval-mode", "plan"],
  default: [],
  auto: ["--approval-mode", "auto_edit"],
  full: ["--yolo"],
};

export const GEMINI_PLUGIN_SPEC: HarnessPluginSpec = {
  id: "gemini",
  aliases: ["gemini-cli"],
  displayApp: "gemini",
  cliDisplayApp: "gemini",
  binary: "gemini",
  screenManifestId: "gemini",
  oscAuthority: false,
  positionalResume: false,
  uniqueExplicitSessions: false,
  titlePrefixes: [],
  mcpTargets: [".gemini/settings.json"],
};

export interface GeminiPluginOptions {
  homeDir?: string;
  spacesDir?: string;
  headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;
}

function resolveGeminiLocalDistEntry(
  baseHome: string,
  env: Record<string, string | undefined>,
): string | null {
  const repoEnv = (env.GEMINI_CLI_REPO ?? "").trim();
  const repoDir = repoEnv
    ? expandTilde(repoEnv, baseHome)
    : path.join(baseHome, "dev", "gemini-cli");
  const distIndex = path.join(repoDir, "packages", "cli", "dist", "index.js");
  return existsSync(distIndex) ? distIndex : null;
}

/**
 * Unified `HarnessPlugin` implementation for Google Gemini CLI (`gemini`).
 */
export class GeminiPlugin implements HarnessPlugin {
  readonly spec: HarnessPluginSpec = GEMINI_PLUGIN_SPEC;

  private readonly homeDir?: string;
  private readonly spacesDir?: string;
  private readonly headlessFactory?: (
    req: HeadlessStartRequest,
    launch: InteractiveLaunchSpec,
  ) => HeadlessSession;

  constructor(options: GeminiPluginOptions = {}) {
    this.homeDir = options.homeDir;
    this.spacesDir = options.spacesDir;
    this.headlessFactory = options.headlessFactory;
  }

  resumeArgs(sessionId: string): string[] {
    return buildStandardResumeArgs(this.spec, sessionId);
  }

  buildInteractiveLaunch(req: InteractiveLaunchRequest): InteractiveLaunchSpec {
    const reqEnv = req.env ?? process.env;
    const baseHome = realHomeDir(req.homeDir ?? this.homeDir ?? os.homedir());
    const spacesDir =
      req.spacesDir ?? this.spacesDir ?? defaultSpacesDir(baseHome, reqEnv);

    const profile = req.profile
      ? loadProfileConfig(this.spec.id, req.profile, {
          homeDir: baseHome,
          spacesDir,
        })
      : null;

    return buildStandardInteractiveLaunch(
      this.spec,
      req,
      {
        shadowVar: "GEMINI_CLI_HOME",
        accessArgs: GEMINI_ACCESS_ARGS,
        routerEnv: (router) => ({
          GOOGLE_GEMINI_BASE_URL: router.url,
          GEMINI_MODEL_ROUTER: "1",
          GEMINI_API_KEY: router.apiKey,
        }),
        noRouterUnsets: [
          "GOOGLE_GEMINI_BASE_URL",
          "GEMINI_MODEL_ROUTER",
          "GEMINI_API_KEY",
        ],
        customize: (spec) => {
          if (profile?.binaryPath) {
            return spec;
          }
          const localDist = resolveGeminiLocalDistEntry(baseHome, reqEnv);
          if (!localDist) {
            return spec;
          }
          return {
            ...spec,
            argv: ["node", localDist, ...spec.argv.slice(1)],
          };
        },
      },
      { homeDir: this.homeDir, spacesDir: this.spacesDir },
    );
  }

  startHeadless(req: HeadlessStartRequest): HeadlessSession {
    const launch = this.buildInteractiveLaunch(req);
    if (this.headlessFactory) {
      return this.headlessFactory(req, launch);
    }
    return new GeminiHeadlessExecutor({
      threadId: req.threadId,
      launch,
      sessionId: req.sessionId,
    });
  }

  readDelta(
    sessionId: string,
    _cwd: string,
    cursor?: DeltaCursor,
    _configDir?: string,
  ): DeltaReadResult {
    return createEmptyDelta(sessionId, cursor);
  }

  stopAndFlush(opts: StopAndFlushOptions): Promise<DeltaReadResult | null> {
    return runStandardStopAndFlush(this, opts);
  }
}

import * as os from "node:os";
import * as path from "node:path";
import {
  defaultSpacesDir,
  loadProfileConfig,
  loadRouterConfig,
  realHomeDir,
  type RouterConfig,
} from "../profiles";
import {
  HeadlessUnsupportedError,
  type AccessLevel,
  type DeltaCursor,
  type DeltaReadResult,
  type HarnessPlugin,
  type HarnessPluginSpec,
  type HeadlessSession,
  type HeadlessStartRequest,
  type InteractiveLaunchRequest,
  type InteractiveLaunchSpec,
  type StopAndFlushOptions,
} from "../types";

export interface StandardLaunchConfig {
  /** Environment variable holding the shadow home dir (e.g. `GROK_HOME`, `CODEX_HOME`). */
  shadowVar?: string;
  /** Optional relative filename appended to shadowHome (e.g. `muse` uses `auth.json`). */
  shadowSubpath?: string;
  /** Default binary override when profile doesn't specify one (may start with `~/`). */
  defaultBinary?: string;
  /** Default args when caller provides no extraArgs/sessionId (e.g. deepseek `["web"]`). */
  defaultArgs?: string[];
  /** Access-level CLI flags per `AccessLevel`. */
  accessArgs?: Partial<Record<AccessLevel, string[]>>;
  /** Router env vars to inject when router is enabled (`!noRouter`). */
  routerEnv?: (router: RouterConfig, homeDir: string, env: Record<string, string | undefined>) => Record<string, string>;
  /** Extra CLI args to inject when router is enabled. */
  routerArgs?: (router: RouterConfig, req: InteractiveLaunchRequest) => string[];
  /** Env vars to unset when `--no-router` is set. */
  noRouterUnsets?: string[];
  /** Optional post-hook to transform the built spec. */
  customize?: (spec: InteractiveLaunchSpec, req: InteractiveLaunchRequest, routerEnabled: boolean) => InteractiveLaunchSpec;
}

export function expandTilde(p: string, homeDir: string): string {
  if (p === "~") return homeDir;
  if (p.startsWith("~/")) return path.join(homeDir, p.slice(2));
  return p;
}

export function buildStandardResumeArgs(spec: HarnessPluginSpec, sessionId: string): string[] {
  const trimmed = sessionId.trim();
  if (!trimmed) return [];
  if (spec.resumeSubcommand) {
    return [spec.resumeSubcommand, trimmed];
  }
  if (spec.resumeFlag) {
    return [spec.resumeFlag, trimmed];
  }
  return [];
}

export function buildStandardInteractiveLaunch(
  spec: HarnessPluginSpec,
  req: InteractiveLaunchRequest,
  cfg: StandardLaunchConfig = {},
  pluginDefaults?: { homeDir?: string; spacesDir?: string },
): InteractiveLaunchSpec {
  const baseHome = realHomeDir(req.homeDir ?? pluginDefaults?.homeDir ?? os.homedir());
  const spacesDir = req.spacesDir ?? pluginDefaults?.spacesDir ?? defaultSpacesDir(baseHome);
  const reqEnv = req.env ?? process.env;

  const profile = req.profile
    ? loadProfileConfig(spec.id, req.profile, { homeDir: baseHome, spacesDir })
    : null;
  if (profile && !profile.enabled) {
    throw new Error(`profile disabled for ${spec.id}: ${req.profile}`);
  }

  const binaryRaw = profile?.binaryPath || cfg.defaultBinary || spec.binary;
  const binary = expandTilde(binaryRaw, baseHome);

  const argv: string[] = [binary];
  if (profile?.prependArgs?.length) {
    argv.push(...profile.prependArgs);
  }

  const env: Record<string, string> = {};
  const unsetEnv: string[] = ["HARNESS_AGENT", "HARNESS_PROFILE"];

  let shadowHome: string | undefined;
  if (profile?.shadowHomePath) {
    shadowHome = profile.shadowHomePath;
  } else if (profile?.homePath) {
    shadowHome = profile.homePath;
  }

  if (shadowHome && cfg.shadowVar) {
    env[cfg.shadowVar] = cfg.shadowSubpath
      ? path.join(shadowHome, cfg.shadowSubpath)
      : shadowHome;
  }

  const routerEnabled = req.noRouter === true ? false : (profile ? profile.router : true);
  if (routerEnabled) {
    const router = loadRouterConfig({ env: reqEnv });
    if (cfg.routerEnv) {
      Object.assign(env, cfg.routerEnv(router, baseHome, reqEnv));
    }
    if (cfg.routerArgs) {
      argv.push(...cfg.routerArgs(router, req));
    }
  } else if (cfg.noRouterUnsets?.length) {
    unsetEnv.push(...cfg.noRouterUnsets);
  }

  if (req.accessLevel && cfg.accessArgs?.[req.accessLevel]) {
    argv.push(...cfg.accessArgs[req.accessLevel]!);
  }

  if (req.model) {
    argv.push("--model", req.model);
  }

  if (req.sessionId) {
    argv.push(...buildStandardResumeArgs(spec, req.sessionId));
  }

  if (req.extraArgs?.length) {
    argv.push(...req.extraArgs);
  } else if (!req.sessionId && cfg.defaultArgs?.length) {
    argv.push(...cfg.defaultArgs);
  }

  if (profile?.env) {
    Object.assign(env, profile.env);
  }

  const out: InteractiveLaunchSpec = {
    argv,
    env,
    unsetEnv: Array.from(new Set(unsetEnv)),
    cwd: req.cwd,
    ...(shadowHome ? { shadowHome } : {}),
  };

  return cfg.customize ? cfg.customize(out, req, routerEnabled) : out;
}

export function createEmptyDelta(sessionId: string, cursor?: DeltaCursor): DeltaReadResult {
  return {
    nextCursor: cursor ?? { sessionPath: sessionId, byteOffset: 0 },
    turns: [],
  };
}

export async function runStandardStopAndFlush(
  plugin: HarnessPlugin,
  opts: StopAndFlushOptions,
): Promise<DeltaReadResult | null> {
  if (opts.headlessSession) {
    await opts.headlessSession.stopAndFlush().catch(() => {});
  }
  if (opts.stopInteractive) {
    await opts.stopInteractive();
  }
  if (!opts.sessionId) {
    return null;
  }
  return plugin.readDelta(opts.sessionId, opts.cwd, opts.cursor, opts.configDir);
}

export function unsupportedHeadless(agent: string): (_req: HeadlessStartRequest) => HeadlessSession {
  return () => {
    throw new HeadlessUnsupportedError(agent);
  };
}

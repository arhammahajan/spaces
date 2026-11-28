import { existsSync, readFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { SPACES_CONFIG_DIR_NAME } from "@spaces/types";
import type { HarnessProfileConfig } from "./types";

const SHADOW_MARKERS = ["/.spaces/", "/.harness/"] as const;
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const ENV_SUBST_RE = /\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}/g;

export const DEFAULT_MODEL_ROUTER_URL = "http://127.0.0.1:4000";
export const DEFAULT_MODEL_ROUTER_API_KEY = "unused";

export interface RouterConfig {
  url: string;
  apiKey: string;
}

export interface LoadRouterConfigOptions {
  env?: Record<string, string | undefined>;
  xdgConfigHome?: string;
}

export interface LoadProfileConfigOptions {
  homeDir?: string;
  spacesDir?: string;
}

function isRecord(val: unknown): val is Record<string, unknown> {
  return typeof val === "object" && val !== null && !Array.isArray(val);
}

function nonEmptyString(val: unknown): string | undefined {
  if (typeof val !== "string") {
    return undefined;
  }
  const trimmed = val.trim();
  return trimmed !== "" ? trimmed : undefined;
}

function containsShadowMarker(targetPath: string): boolean {
  const normalized = targetPath.replaceAll("\\", "/");
  return SHADOW_MARKERS.some((marker) => normalized.includes(marker));
}

/**
 * Unwraps `/.spaces/` and `/.harness/` shadow-profile home prefixes back to the
 * user's real home directory. Falls back to `$HOME` then `os.homedir()` when
 * `homeDir` is empty.
 */
export function realHomeDir(homeDir?: string): string {
  let home = (homeDir ?? "").trim();
  if (!home) {
    home = (process.env.HOME ?? "").trim();
  }
  if (!home) {
    home = os.homedir();
  }
  if (!home) {
    return "";
  }

  const normalized = home.replaceAll("\\", "/");
  let earliest = -1;
  for (const marker of SHADOW_MARKERS) {
    const idx = normalized.indexOf(marker);
    if (idx > 0 && (earliest < 0 || idx < earliest)) {
      earliest = idx;
    }
  }
  if (earliest > 0) {
    return home.slice(0, earliest);
  }
  return home;
}

/**
 * Resolves the default spaces configuration directory (`$SPACES_DIR` or
 * `<realHome>/.spaces`).
 */
export function defaultSpacesDir(
  homeDir?: string,
  env: Record<string, string | undefined> = process.env,
): string {
  const explicit = (env.SPACES_DIR ?? "").trim();
  if (explicit !== "") {
    return explicit;
  }
  return path.join(realHomeDir(homeDir ?? env.HOME), SPACES_CONFIG_DIR_NAME);
}

/**
 * Expands leading `~`, `~/`, `$HOME`, `$HOME/`, and `${HOME}` references
 * using `home`. Returns `value` unchanged when `home` is empty.
 */
export function expandHomePath(value: string, home: string): string {
  if (!value || !home) {
    return value;
  }
  let out = value;
  if (out.includes("${HOME}")) {
    out = out.replaceAll("${HOME}", home);
  }
  if (out === "$HOME") {
    return home;
  }
  if (out.startsWith("$HOME/") || out.startsWith(`$HOME${path.sep}`)) {
    out = path.join(home, out.slice("$HOME".length + 1));
  }
  if (out === "~") {
    return home;
  }
  if (out.startsWith("~/") || out.startsWith(`~${path.sep}`)) {
    out = path.join(home, out.slice(2));
  }
  return out;
}

function expandSubstitutions(
  value: string,
  lookup: (name: string) => string | undefined,
): string {
  return value.replace(ENV_SUBST_RE, (_match, name: string, fallback?: string) => {
    const resolved = lookup(name);
    if (resolved !== undefined && resolved !== "") {
      return resolved;
    }
    return fallback ?? "";
  });
}

function evaluateEnvValue(
  rawValue: string,
  lookup: (name: string) => string | undefined,
): string {
  if (!rawValue) {
    return "";
  }
  if (rawValue.startsWith("'") && rawValue.endsWith("'") && rawValue.length >= 2) {
    return rawValue.slice(1, -1);
  }
  let body = rawValue;
  if (body.startsWith('"') && body.endsWith('"') && body.length >= 2) {
    body = body.slice(1, -1);
  }
  return expandSubstitutions(body, lookup);
}

/**
 * Parses `ai/model-router.env` lines (`export NAME=VAL`, single/double-quoted or
 * bare values, `${VAR}` / `${VAR:-default}` expansions, `#` comments).
 */
export function parseEnvFile(
  data: string,
  lookup?: (name: string) => string | undefined,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const rawLine of data.split(/\r?\n/)) {
    let line = rawLine.trim();
    if (!line || line.startsWith("#")) {
      continue;
    }
    if (line.startsWith("export ")) {
      line = line.slice("export ".length).trimStart();
    }
    const eq = line.indexOf("=");
    if (eq <= 0) {
      continue;
    }
    const name = line.slice(0, eq).trim();
    if (!ENV_NAME_RE.test(name)) {
      continue;
    }
    const rawVal = line.slice(eq + 1).trim();
    out[name] = evaluateEnvValue(rawVal, (varName) => {
      if (Object.hasOwn(out, varName)) {
        return out[varName];
      }
      return lookup?.(varName);
    });
  }
  return out;
}

function resolveXdgConfigHome(
  opts: LoadRouterConfigOptions | undefined,
  env: Record<string, string | undefined>,
  realHome: string,
): string {
  const explicit = (opts?.xdgConfigHome ?? "").trim();
  if (explicit !== "") {
    return explicit;
  }
  const envXdg = (env.XDG_CONFIG_HOME ?? "").trim();
  if (envXdg !== "" && path.isAbsolute(envXdg) && !containsShadowMarker(envXdg)) {
    return envXdg;
  }
  return path.join(realHome, ".config");
}

/**
 * Loads model-router endpoint settings using precedence:
 * process/provided `env` -> `$XDG_CONFIG_HOME/ai/model-router.env` -> defaults.
 */
export function loadRouterConfig(opts?: LoadRouterConfigOptions): RouterConfig {
  const env = opts?.env ?? process.env;
  const realHome = realHomeDir(env.HOME);
  const configHome = resolveXdgConfigHome(opts, env, realHome);
  const envFilePath = path.join(configHome, "ai", "model-router.env");

  let fileEnv: Record<string, string> = {};
  if (existsSync(envFilePath)) {
    try {
      const raw = readFileSync(envFilePath, "utf8");
      fileEnv = parseEnvFile(raw, (name) => env[name]);
    } catch {
      fileEnv = {};
    }
  }

  let url = (env.MODEL_ROUTER_URL ?? "").trim();
  if (!url) {
    url = (fileEnv.MODEL_ROUTER_URL ?? "").trim();
  }
  url = url.replace(/\/+$/, "");
  if (!url) {
    url = DEFAULT_MODEL_ROUTER_URL;
  }

  let apiKey = (env.MODEL_ROUTER_API_KEY ?? "").trim();
  if (!apiKey) {
    apiKey = (fileEnv.MODEL_ROUTER_API_KEY ?? "").trim();
  }
  if (!apiKey) {
    apiKey = DEFAULT_MODEL_ROUTER_API_KEY;
  }

  return { url, apiKey };
}

function normalizeStringMap(raw: unknown): Record<string, string> {
  if (!isRecord(raw)) {
    return {};
  }
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw)) {
    if (typeof v === "string") {
      out[k] = v;
    }
  }
  return out;
}

function normalizeStringArray(raw: unknown): string[] {
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.filter((item): item is string => typeof item === "string");
}

function expandShadowTemplate(
  template: string,
  agent: string,
  name: string,
  realHome: string,
): string {
  const replaced = template
    .replaceAll("{home}", realHome)
    .replaceAll("{data_home}", path.join(realHome, ".local", "share"))
    .replaceAll("{config_home}", path.join(realHome, ".config"))
    .replaceAll("{state_home}", path.join(realHome, ".local", "state"))
    .replaceAll("{cache_home}", path.join(realHome, ".cache"))
    .replaceAll("{agent}", agent)
    .replaceAll("{name}", name);
  return path.resolve(expandHomePath(replaced, realHome));
}

function normalizeProfileObject(
  raw: Record<string, unknown>,
  agent: string,
  name: string,
  realHome: string,
  defaultShadowBase: string,
  shadowHomeTemplate?: string,
): HarnessProfileConfig {
  const resolvedAgent = nonEmptyString(raw.agent)?.toLowerCase() ?? agent;
  const resolvedName = nonEmptyString(raw.name) ?? name;
  const displayName = nonEmptyString(raw.displayName) ?? nonEmptyString(raw.display_name);
  const enabled = typeof raw.enabled === "boolean" ? raw.enabled : true;

  const rawBinary = nonEmptyString(raw.binaryPath) ?? nonEmptyString(raw.binary_path);
  const binaryPath = rawBinary ? expandHomePath(rawBinary, realHome) : undefined;

  const rawHome = nonEmptyString(raw.homePath) ?? nonEmptyString(raw.home_path);
  const homePath = rawHome ? expandHomePath(rawHome, realHome) : undefined;

  const rawShadow = nonEmptyString(raw.shadowHomePath) ?? nonEmptyString(raw.shadow_home_path);
  let shadowHomePath = rawShadow ? expandHomePath(rawShadow, realHome) : undefined;
  if (!shadowHomePath && resolvedName !== "default") {
    if (shadowHomeTemplate) {
      shadowHomePath = expandShadowTemplate(
        shadowHomeTemplate,
        resolvedAgent,
        resolvedName,
        realHome,
      );
    } else {
      shadowHomePath = path.join(
        defaultShadowBase,
        "profiles",
        resolvedAgent,
        resolvedName,
        "home",
      );
    }
  }

  const env = normalizeStringMap(raw.env);
  const prependArgs = normalizeStringArray(raw.prependArgs ?? raw.prepend_args);
  const router = typeof raw.router === "boolean" ? raw.router : true;
  const overlay = typeof raw.overlay === "boolean" ? raw.overlay : undefined;

  const config: HarnessProfileConfig = {
    agent: resolvedAgent,
    name: resolvedName,
    enabled,
    env,
    prependArgs,
    router,
  };
  if (displayName !== undefined) config.displayName = displayName;
  if (binaryPath !== undefined) config.binaryPath = binaryPath;
  if (homePath !== undefined) config.homePath = homePath;
  if (shadowHomePath !== undefined) config.shadowHomePath = shadowHomePath;
  if (overlay !== undefined) config.overlay = overlay;
  return config;
}

function readJsonRecord(filePath: string): Record<string, unknown> | null {
  if (!existsSync(filePath)) {
    return null;
  }
  try {
    const raw = readFileSync(filePath, "utf8").trim();
    if (!raw) {
      return null;
    }
    const parsed: unknown = JSON.parse(raw);
    return isRecord(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function parseQualifiedProfileRef(
  agent: string,
  profileName: string,
): { agent: string; name: string } | null {
  const normAgent = agent.trim().toLowerCase();
  const rawName = profileName.trim();
  if (!normAgent || !rawName) {
    return null;
  }
  const slashIdx = rawName.indexOf("/");
  if (slashIdx >= 0) {
    const prefix = rawName.slice(0, slashIdx).trim().toLowerCase();
    const rest = rawName.slice(slashIdx + 1).trim();
    if (prefix !== normAgent || !rest) {
      return null;
    }
    return { agent: normAgent, name: rest };
  }
  return { agent: normAgent, name: rawName };
}

/**
 * Loads a profile configuration for `(agent, profileName)`:
 * 1. `<spacesDir>/profiles/<agent>/<profileName>/profile.json`
 * 2. `<realHome>/.harness/profiles/<agent>/<profileName>/profile.json`
 * 3. `<realHome>/.harness/profiles.json`
 *
 * Normalizes both `snake_case` and legacy `camelCase` fields and resolves the
 * default `shadowHomePath` under `<spacesDir>` (or `.harness` when loaded from
 * `.harness`).
 */
export function loadProfileConfig(
  agent: string,
  profileName: string,
  opts?: LoadProfileConfigOptions,
): HarnessProfileConfig | null {
  const ref = parseQualifiedProfileRef(agent, profileName);
  if (!ref) {
    return null;
  }

  const realHome = realHomeDir(opts?.homeDir);
  const explicitSpacesDir = (opts?.spacesDir ?? "").trim();
  const spacesDir =
    explicitSpacesDir ||
    (opts?.homeDir ? path.join(realHome, SPACES_CONFIG_DIR_NAME) : defaultSpacesDir(realHome));
  const harnessDir = path.join(realHome, ".harness");

  const spacesProfilePath = path.join(
    spacesDir,
    "profiles",
    ref.agent,
    ref.name,
    "profile.json",
  );
  const spacesDoc = readJsonRecord(spacesProfilePath);
  if (spacesDoc) {
    return normalizeProfileObject(spacesDoc, ref.agent, ref.name, realHome, spacesDir);
  }

  const harnessProfilePath = path.join(
    harnessDir,
    "profiles",
    ref.agent,
    ref.name,
    "profile.json",
  );
  const harnessDoc = readJsonRecord(harnessProfilePath);
  if (harnessDoc) {
    return normalizeProfileObject(harnessDoc, ref.agent, ref.name, realHome, harnessDir);
  }

  const legacyStorePath = path.join(harnessDir, "profiles.json");
  const storeDoc = readJsonRecord(legacyStorePath);
  if (!storeDoc) {
    return null;
  }

  const shadowHomeTemplate =
    nonEmptyString(storeDoc.shadowHomeTemplate) ??
    nonEmptyString(storeDoc.shadow_home_template);

  if (ref.name === "default") {
    const defaults = isRecord(storeDoc.defaults) ? storeDoc.defaults : null;
    if (!defaults) {
      return null;
    }
    const directDefault = defaults[ref.agent];
    if (isRecord(directDefault)) {
      return normalizeProfileObject(
        directDefault,
        ref.agent,
        ref.name,
        realHome,
        harnessDir,
        shadowHomeTemplate,
      );
    }
    for (const [k, v] of Object.entries(defaults)) {
      if (k.trim().toLowerCase() === ref.agent && isRecord(v)) {
        return normalizeProfileObject(
          v,
          ref.agent,
          ref.name,
          realHome,
          harnessDir,
          shadowHomeTemplate,
        );
      }
    }
    return null;
  }

  const profilesMap = isRecord(storeDoc.profiles) ? storeDoc.profiles : null;
  if (!profilesMap) {
    return null;
  }

  const qualifiedKey = `${ref.agent}/${ref.name}`;
  const candidates = [profilesMap[qualifiedKey], profilesMap[ref.name]];
  for (const cand of candidates) {
    if (!isRecord(cand)) {
      continue;
    }
    const candAgent = nonEmptyString(cand.agent)?.toLowerCase();
    if (candAgent && candAgent !== ref.agent) {
      continue;
    }
    return normalizeProfileObject(
      cand,
      ref.agent,
      ref.name,
      realHome,
      harnessDir,
      shadowHomeTemplate,
    );
  }

  return null;
}

import * as fs from "node:fs";
import * as path from "node:path";
import { compilePattern } from "./regex";
import { extractRegion, validateRegionSpec } from "./regions";
import { extractTitle } from "./titles";

export type ScreenState = "idle" | "working" | "blocked" | "unknown";

export interface DetectedScreen {
  state: ScreenState;
  visibleIdle: boolean;
  visibleBlocker: boolean;
  visibleWorking: boolean;
  skipStateUpdate: boolean;
  /** Winning rule id, or `""` when no rule matched. */
  matchedRule: string;
  /** Conversation/session title parsed from the viewport, or `""`. */
  title: string;
}

export interface Gate {
  all?: Gate[];
  any?: Gate[];
  not?: Gate[];
  contains?: string[];
  regex?: string[];
  line_regex?: string[];
}

export interface ManifestRule extends Gate {
  id: string;
  state?: string | null;
  priority?: number;
  region?: string;
  visible_idle?: boolean;
  visible_blocker?: boolean;
  visible_working?: boolean;
  skip_state_update?: boolean;
}

export interface AgentManifest {
  id: string;
  version?: string | null;
  min_engine_version?: number | null;
  updated_at?: string | null;
  aliases?: string[];
  rules: ManifestRule[];
}

export interface ManifestFile {
  source?: string;
  engine_version?: number;
  manifests: AgentManifest[];
}

interface CompiledGate {
  all: CompiledGate[];
  any: CompiledGate[];
  not: CompiledGate[];
  contains: string[];
  regex: RegExp[];
  lineRegex: RegExp[];
}

interface CompiledRule {
  id: string;
  state: ScreenState;
  priority: number;
  region: string;
  visibleIdle: boolean;
  visibleBlocker: boolean;
  visibleWorking: boolean;
  skip: boolean;
  gate: CompiledGate;
}

export interface CompiledManifest {
  readonly id: string;
  readonly rules: readonly CompiledRule[];
}

const VALID_STATES = new Set<ScreenState>(["idle", "working", "blocked", "unknown"]);
const EXECUTABLE_SUFFIXES = [".exe", ".cmd", ".bat", ".ps1", ".js", ".mjs", ".cjs"];

const MANIFEST_FILE_KEYS = new Set(["source", "engine_version", "manifests"]);
const AGENT_MANIFEST_KEYS = new Set([
  "id",
  "version",
  "min_engine_version",
  "updated_at",
  "aliases",
  "rules",
]);
const GATE_KEYS = new Set(["all", "any", "not", "contains", "regex", "line_regex"]);
const RULE_KEYS = new Set([
  ...GATE_KEYS,
  "id",
  "state",
  "priority",
  "region",
  "visible_idle",
  "visible_blocker",
  "visible_working",
  "skip_state_update",
]);

/**
 * Apps whose OSC lifecycle is complete enough that a live non-idle OSC state
 * takes precedence over screen working/idle detection (blockers still win).
 */
const OSC_AUTHORITY_APPS: ReadonlySet<string> = new Set([
  "claude",
  "codex",
  "opencode",
  "pi",
  "crush",
  "grok",
  "omp",
  "copilot",
  "kimi",
  "muse",
  "mcode",
  "fx",
  "fx-router",
  "vibe",
  "vibe-acp",
  "dsh-tui",
  "deepseek",
]);

const DEFAULT_MANIFESTS_PATH = path.resolve(
  import.meta.dir,
  "../../../../internal/screen/manifests.json",
);

/**
 * Builds a no-evidence `unknown` detection result.
 */
export function unknownDetected(matchedRule = ""): DetectedScreen {
  return {
    state: "unknown",
    visibleIdle: false,
    visibleBlocker: false,
    visibleWorking: false,
    skipStateUpdate: false,
    matchedRule,
    title: "",
  };
}

/**
 * Reports whether the detection carries a visible signal.
 */
export function hasEvidence(d: DetectedScreen): boolean {
  return d.visibleIdle || d.visibleBlocker || d.visibleWorking;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function assertAllowedKeys(
  obj: Record<string, unknown>,
  allowed: ReadonlySet<string>,
  context: string,
): void {
  for (const key of Object.keys(obj)) {
    if (!allowed.has(key)) {
      throw new Error(`parse manifests: unknown field "${key}" in ${context}`);
    }
  }
}

function parseOptionalString(value: unknown, field: string): string | undefined {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value !== "string") {
    throw new Error(`parse manifests: expected string for ${field}`);
  }
  return value;
}

function parseOptionalBoolean(value: unknown, field: string): boolean {
  if (value === undefined || value === null) {
    return false;
  }
  if (typeof value !== "boolean") {
    throw new Error(`parse manifests: expected boolean for ${field}`);
  }
  return value;
}

function parseOptionalNumber(value: unknown, field: string): number | undefined {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`parse manifests: expected number for ${field}`);
  }
  return value;
}

function parseStringArray(value: unknown, field: string): string[] {
  if (value === undefined || value === null) {
    return [];
  }
  if (!Array.isArray(value)) {
    throw new Error(`parse manifests: expected array for ${field}`);
  }
  const out: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") {
      throw new Error(`parse manifests: expected string element in ${field}`);
    }
    out.push(item);
  }
  return out;
}

function ruleState(raw: unknown): ScreenState {
  if (raw === undefined || raw === null || raw === "") {
    return "unknown";
  }
  if (typeof raw !== "string" || !VALID_STATES.has(raw as ScreenState)) {
    throw new Error(`unknown state ${JSON.stringify(raw)}`);
  }
  return raw as ScreenState;
}

function compileGate(raw: Record<string, unknown>, checkKeys: boolean): CompiledGate {
  if (checkKeys) {
    assertAllowedKeys(raw, GATE_KEYS, "gate");
  }

  const compileGateList = (field: string): CompiledGate[] => {
    const val = raw[field];
    if (val === undefined || val === null) {
      return [];
    }
    if (!Array.isArray(val)) {
      throw new Error(`parse manifests: expected array for gate.${field}`);
    }
    return val.map((item) => {
      if (!isPlainObject(item)) {
        throw new Error(`parse manifests: expected object element in gate.${field}`);
      }
      return compileGate(item, true);
    });
  };

  const all = compileGateList("all");
  const any = compileGateList("any");
  const not = compileGateList("not");
  const contains = parseStringArray(raw.contains, "contains").map((s) => s.toLowerCase());

  const regex: RegExp[] = [];
  for (const pattern of parseStringArray(raw.regex, "regex")) {
    try {
      regex.push(compilePattern(pattern));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`invalid regex ${JSON.stringify(pattern)}: ${message}`);
    }
  }

  const lineRegex: RegExp[] = [];
  for (const pattern of parseStringArray(raw.line_regex, "line_regex")) {
    try {
      lineRegex.push(compilePattern(pattern));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`invalid line_regex ${JSON.stringify(pattern)}: ${message}`);
    }
  }

  return { all, any, not, contains, regex, lineRegex };
}

function compileManifestRules(manifest: Record<string, unknown>): CompiledRule[] {
  assertAllowedKeys(manifest, AGENT_MANIFEST_KEYS, "manifest");
  const manifestId = parseOptionalString(manifest.id, "manifest.id") ?? "";
  const minEngineVersion = parseOptionalNumber(
    manifest.min_engine_version,
    "manifest.min_engine_version",
  );

  const rawRules = manifest.rules;
  if (!Array.isArray(rawRules) || rawRules.length === 0) {
    throw new Error(`manifest ${manifestId} has no rules`);
  }

  const compiled: CompiledRule[] = [];
  for (const rawRule of rawRules) {
    if (!isPlainObject(rawRule)) {
      throw new Error(`parse manifests: expected object in manifest ${manifestId} rules`);
    }
    assertAllowedKeys(rawRule, RULE_KEYS, "rule");

    const ruleId = parseOptionalString(rawRule.id, "rule.id") ?? "";
    const rawRegion = parseOptionalString(rawRule.region, "rule.region") ?? "";
    const regionCheck = validateRegionSpec(rawRegion, minEngineVersion);
    if (!regionCheck.ok) {
      throw new Error(`rule ${ruleId} uses ${regionCheck.error}`);
    }

    let state: ScreenState;
    try {
      state = ruleState(rawRule.state);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`rule ${ruleId}: ${message}`);
    }

    let gate: CompiledGate;
    try {
      gate = compileGate(rawRule, false);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`rule ${ruleId}: ${message}`);
    }

    const priority = parseOptionalNumber(rawRule.priority, "rule.priority") ?? 0;
    const visibleIdle = parseOptionalBoolean(rawRule.visible_idle, "rule.visible_idle");
    const visibleBlocker = parseOptionalBoolean(rawRule.visible_blocker, "rule.visible_blocker");
    const visibleWorking = parseOptionalBoolean(rawRule.visible_working, "rule.visible_working");
    const skip = parseOptionalBoolean(rawRule.skip_state_update, "rule.skip_state_update");

    compiled.push({
      id: ruleId,
      state,
      priority,
      region: regionCheck.region,
      visibleIdle,
      visibleBlocker,
      visibleWorking,
      skip,
      gate,
    });
  }

  return compiled;
}

function gateMatches(gate: CompiledGate, text: string): boolean {
  const lower = text.toLowerCase();
  return gateMatchesLower(gate, text, lower);
}

function gateMatchesLower(gate: CompiledGate, text: string, lower: string): boolean {
  for (const needle of gate.contains) {
    if (!lower.includes(needle)) {
      return false;
    }
  }
  for (const re of gate.regex) {
    if (!re.test(text)) {
      return false;
    }
  }
  if (gate.lineRegex.length > 0) {
    const lines = text.split("\n");
    for (const re of gate.lineRegex) {
      let matched = false;
      for (const line of lines) {
        if (re.test(line)) {
          matched = true;
          break;
        }
      }
      if (!matched) {
        return false;
      }
    }
  }
  for (const nested of gate.all) {
    if (!gateMatchesLower(nested, text, lower)) {
      return false;
    }
  }
  if (gate.any.length > 0) {
    let anyMatched = false;
    for (const nested of gate.any) {
      if (gateMatchesLower(nested, text, lower)) {
        anyMatched = true;
        break;
      }
    }
    if (!anyMatched) {
      return false;
    }
  }
  for (const nested of gate.not) {
    if (gateMatchesLower(nested, text, lower)) {
      return false;
    }
  }
  return true;
}

/**
 * Compiled manifest evaluator mirroring `internal/screen/manifest.go`.
 */
export class ScreenEngine {
  readonly manifests: readonly CompiledManifest[];
  private readonly lookup: ReadonlyMap<string, CompiledManifest>;

  private constructor(
    manifests: readonly CompiledManifest[],
    lookup: ReadonlyMap<string, CompiledManifest>,
  ) {
    this.manifests = manifests;
    this.lookup = lookup;
  }

  /**
   * Creates an empty `ScreenEngine` with no manifests.
   */
  static empty(): ScreenEngine {
    return new ScreenEngine([], new Map());
  }

  /**
   * Parses, validates, and compiles a manifest bundle JSON string.
   */
  static fromJson(raw: string): ScreenEngine {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`parse manifests: ${message}`);
    }

    if (!isPlainObject(parsed)) {
      throw new Error("parse manifests: root must be a JSON object");
    }
    assertAllowedKeys(parsed, MANIFEST_FILE_KEYS, "manifest file");
    if (parsed.source !== undefined && parsed.source !== null && typeof parsed.source !== "string") {
      throw new Error("parse manifests: expected string for source");
    }
    parseOptionalNumber(parsed.engine_version, "engine_version");

    const rawManifests = parsed.manifests;
    if (!Array.isArray(rawManifests) || rawManifests.length === 0) {
      throw new Error("manifest bundle is empty");
    }

    const manifests: CompiledManifest[] = [];
    const lookup = new Map<string, CompiledManifest>();

    for (const rawManifest of rawManifests) {
      if (!isPlainObject(rawManifest)) {
        throw new Error("parse manifests: manifest entry must be an object");
      }
      const rules = compileManifestRules(rawManifest);
      const rawId = parseOptionalString(rawManifest.id, "manifest.id") ?? "";
      const id = rawId.toLowerCase();
      if (lookup.has(id)) {
        throw new Error(`duplicate manifest id ${id}`);
      }

      const cm: CompiledManifest = { id, rules };
      manifests.push(cm);
      lookup.set(id, cm);

      const aliases = parseStringArray(rawManifest.aliases, "manifest.aliases");
      for (const alias of aliases) {
        const key = alias.toLowerCase();
        if (!lookup.has(key)) {
          lookup.set(key, cm);
        }
      }
    }

    return new ScreenEngine(manifests, lookup);
  }

  /**
   * Returns the compiled manifest covering an already-normalized app label.
   */
  manifest(app: string): CompiledManifest | undefined {
    return this.lookup.get(app);
  }

  /**
   * Returns the canonical manifest id covering `app`, or `""` when unknown.
   */
  manifestID(app: string): string {
    return this.lookup.get(app)?.id ?? "";
  }

  /**
   * Evaluates screen and OSC inputs for an already-normalized app label.
   * Does not run title extraction.
   */
  detect(app: string, screen: string, oscTitle = "", oscProgress = ""): DetectedScreen {
    const manifest = this.manifest(app);
    if (!manifest) {
      return unknownDetected("");
    }

    const input = { screen, oscTitle, oscProgress };
    let matched: CompiledRule | undefined;

    for (const rule of manifest.rules) {
      if (!gateMatches(rule.gate, extractRegion(rule.region, input))) {
        continue;
      }
      if (matched !== undefined && matched.priority >= rule.priority) {
        continue;
      }
      matched = rule;
    }

    if (!matched) {
      return unknownDetected("");
    }

    return {
      state: matched.state,
      visibleIdle: matched.visibleIdle && matched.state === "idle",
      visibleBlocker: matched.visibleBlocker && matched.state === "blocked",
      visibleWorking: matched.visibleWorking && matched.state === "working",
      skipStateUpdate: matched.skip,
      matchedRule: matched.id,
      title: "",
    };
  }
}

/**
 * Loads and compiles `internal/screen/manifests.json` (or a custom file path).
 */
export function loadBundledScreenEngine(manifestsPath = DEFAULT_MANIFESTS_PATH): ScreenEngine {
  const raw = fs.readFileSync(manifestsPath, "utf8");
  return ScreenEngine.fromJson(raw);
}

let cachedDefaultEngine: ScreenEngine | undefined;

function getDefaultEngine(): ScreenEngine {
  if (!cachedDefaultEngine) {
    try {
      cachedDefaultEngine = loadBundledScreenEngine();
    } catch (err) {
      console.error("spaces: screen manifests unavailable:", err);
      cachedDefaultEngine = ScreenEngine.empty();
    }
  }
  return cachedDefaultEngine;
}

/**
 * Lowercases, takes the basename, strips common executable suffixes and
 * versioned binary suffixes (e.g. `"muse-bin-1.4.24-R9131.1"` -> `"muse"`).
 */
export function normalizeApp(app: string): string {
  const trimmed = app.trim();
  let base = trimmed.endsWith("/") ? trimmed.slice(0, -1) : trimmed;
  const slashIdx = base.lastIndexOf("/");
  if (slashIdx >= 0) {
    base = base.slice(slashIdx + 1);
  }
  if (base === "") {
    base = trimmed;
  }
  let lowered = base.toLowerCase();
  for (const suffix of EXECUTABLE_SUFFIXES) {
    if (lowered.endsWith(suffix)) {
      lowered = lowered.slice(0, -suffix.length);
      break;
    }
  }
  const binIdx = lowered.indexOf("-bin-");
  if (binIdx > 0) {
    return lowered.slice(0, binIdx);
  }
  return lowered;
}

/**
 * Returns the canonical manifest id where one exists, else the normalized app
 * label. Maps harness aliases without manifests onto their canonical agent.
 */
export function canonicalApp(app: string): string {
  const normalized = normalizeApp(app);
  const id = getDefaultEngine().manifestID(normalized);
  if (id !== "") {
    return id;
  }
  switch (normalized) {
    case "dsh":
    case "dsh-tui":
      return "deepseek";
    case "fx-router":
      return "fx";
    case "vibe-acp":
      return "vibe";
    case "kimi-cli":
    case "kimi-code":
      return "kimi";
    case "claude":
    case "claude-code":
      return "claude";
    default:
      return normalized;
  }
}

/**
 * Reports whether a bundled manifest can evaluate `app`.
 */
export function agentIsKnown(app: string): boolean {
  const eng = getDefaultEngine();
  if (eng.manifest(normalizeApp(app)) !== undefined) {
    return true;
  }
  return eng.manifest(canonicalApp(app)) !== undefined;
}

/**
 * Reports whether a live non-idle OSC state should win over screen working/idle
 * detection for `app`.
 */
export function oscAuthority(app: string): boolean {
  const canonical = canonicalApp(app);
  if (OSC_AUTHORITY_APPS.has(canonical)) {
    return true;
  }
  return OSC_AUTHORITY_APPS.has(normalizeApp(app));
}

/**
 * Reports whether a bundled manifest exists for `id` (or alias).
 */
export function hasManifest(id: string): boolean {
  return getDefaultEngine().manifest(normalizeApp(id)) !== undefined;
}

/**
 * Evaluates only the manifest rules for `manifestId` without running title
 * extraction.
 */
export function detectManifest(
  manifestId: string,
  viewport: string,
  oscTitle = "",
): DetectedScreen {
  if (viewport.trim() === "") {
    return unknownDetected("");
  }
  return getDefaultEngine().detect(normalizeApp(manifestId), viewport, oscTitle, "");
}

/**
 * Evaluates the captured viewport and OSC title for `app`, including viewport
 * title extraction.
 */
export function detect(app: string, screen: string, oscTitle = ""): DetectedScreen {
  if (screen.trim() === "") {
    return unknownDetected("");
  }
  const eng = getDefaultEngine();
  let norm = normalizeApp(app);
  if (eng.manifest(norm) === undefined) {
    norm = canonicalApp(app);
  }
  const det = eng.detect(norm, screen, oscTitle, "");
  det.title = extractTitle(norm, screen);
  return det;
}

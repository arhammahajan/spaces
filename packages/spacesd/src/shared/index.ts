import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { SharedKind, SharedMCP, SharedSkill, SharedSnapshot } from "@spaces/types";
import { expandHomePath, realHomeDir } from "../plugins/profiles";

export type { SharedKind, SharedMCP, SharedSkill, SharedSnapshot };

export const ALL_SHARED_FILTERS: readonly SharedKind[] = ["all", "mcp", "skills"];

export interface MCPConfigTarget {
  readonly harnessLabel: string;
  readonly relPath: string;
}

export const DEFAULT_MCP_CONFIG_TARGETS: readonly MCPConfigTarget[] = [
  { harnessLabel: "antigravity", relPath: ".gemini/config/mcp_config.json" },
  { harnessLabel: "antigravity", relPath: ".gemini/antigravity-cli/settings.json" },
  { harnessLabel: "claude", relPath: ".claude.json" },
  { harnessLabel: "claude", relPath: ".claude/mcp.json" },
  { harnessLabel: "claude", relPath: ".claude/settings.json" },
  { harnessLabel: "cline", relPath: ".cline/cline_mcp_settings.json" },
  { harnessLabel: "codex", relPath: ".codex/config.toml" },
  { harnessLabel: "copilot", relPath: ".copilot/mcp-config.json" },
  { harnessLabel: "cursor", relPath: ".cursor/mcp.json" },
  { harnessLabel: "deepseek", relPath: ".deepseek/mcp.json" },
  { harnessLabel: "deepseek", relPath: ".config/deepseek/mcp.json" },
  { harnessLabel: "fx", relPath: ".fx/mcp.json" },
  { harnessLabel: "gemini", relPath: ".gemini/settings.json" },
  { harnessLabel: "grok", relPath: ".grok/config.toml" },
  { harnessLabel: "kilo", relPath: ".config/kilo/kilo.json" },
  { harnessLabel: "kimi-code", relPath: ".kimi-code/mcp.json" },
  { harnessLabel: "kimi-code", relPath: ".kimi-code/config.toml" },
  { harnessLabel: "kimi-cli", relPath: ".kimi/mcp.json" },
  { harnessLabel: "kimi-cli", relPath: ".kimi/config.toml" },
  { harnessLabel: "mcode", relPath: ".minimax/mcp.json" },
  { harnessLabel: "muse", relPath: ".config/muse/settings.json" },
  { harnessLabel: "opencode", relPath: ".config/opencode/opencode.json" },
  { harnessLabel: "pi", relPath: ".pi/agent/mcp.json" },
  { harnessLabel: "qwen", relPath: ".qwen/settings.json" },
  { harnessLabel: "vibe", relPath: ".vibe/config.toml" },
];

const SIBLING_MCP_CANDIDATES: readonly string[] = [
  "mcp.json",
  "mcp_config.json",
  "mcp-config.json",
  "cline_mcp_settings.json",
  "opencode.json",
  "kilo.json",
  "settings.json",
  "config.toml",
  "linux-config.toml",
];

export interface LoadSharedOptions {
  dir?: string;
  homeDir?: string;
  mcpTargets?: readonly string[];
  env?: Record<string, string | undefined>;
}

/**
 * Returns a single-line human-readable command or URL representation for a `SharedMCP`.
 */
export function sharedMCPCommandLine(mcp: SharedMCP): string {
  const cmd = mcp.command.trim();
  if (cmd !== "") {
    if (mcp.args.length === 0) {
      return cmd;
    }
    return `${cmd} ${mcp.args.join(" ")}`;
  }
  return mcp.url.trim();
}

function isDir(p: string): boolean {
  if (!p) {
    return false;
  }
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

function isFile(p: string): boolean {
  if (!p) {
    return false;
  }
  try {
    return statSync(p).isFile();
  } catch {
    return false;
  }
}

function resolveEffectiveHome(
  homeDir?: string,
  env: Record<string, string | undefined> = process.env,
): string {
  const raw = (homeDir ?? "").trim();
  if (raw) {
    return raw;
  }
  const envHome = (env.HOME ?? "").trim();
  if (envHome) {
    return envHome;
  }
  return os.homedir();
}

function candidateHomes(homeDir: string): string[] {
  if (!homeDir) {
    return [];
  }
  const real = realHomeDir(homeDir);
  if (real && real !== homeDir) {
    return [homeDir, real];
  }
  return [homeDir];
}

/**
 * Resolves the shared skills/MCP root directory:
 * 1. `explicit` argument
 * 2. `$TMUX_SPACES_SHARED_DIR`
 * 3. `<home>/.agents` then `<realHome>/.agents` (if directory exists)
 * 4. `<home>/dev/dots/harnesses/shared` then `<realHome>/dev/dots/harnesses/shared`
 * 5. Fallback `<realHome>/.agents`
 */
export function defaultSharedDir(
  explicit?: string,
  homeDir?: string,
  env: Record<string, string | undefined> = process.env,
): string {
  const effectiveHome = resolveEffectiveHome(homeDir, env);
  let raw = (explicit ?? "").trim();
  if (!raw) {
    raw = (env.TMUX_SPACES_SHARED_DIR ?? "").trim();
  }
  if (raw) {
    return path.normalize(expandHomePath(raw, effectiveHome));
  }

  const homes = candidateHomes(effectiveHome);
  for (const h of homes) {
    const cand = path.join(h, ".agents");
    if (isDir(cand)) {
      return cand;
    }
  }
  for (const h of homes) {
    const cand = path.join(h, "dev", "dots", "harnesses", "shared");
    if (isDir(cand)) {
      return cand;
    }
  }
  return path.join(realHomeDir(effectiveHome), ".agents");
}

/**
 * Scans `opts.dir` (or `defaultSharedDir`) for shared skills and MCP server definitions.
 */
export function loadShared(opts: LoadSharedOptions = {}): SharedSnapshot {
  const env = opts.env ?? process.env;
  const effectiveHome = resolveEffectiveHome(opts.homeDir, env);
  const root = defaultSharedDir(opts.dir, effectiveHome, env);
  const skillsDir = path.join(root, "skills");
  const rootExists = isDir(root);
  const loadedAtMs = Date.now();

  if (!rootExists) {
    return {
      root_dir: root,
      skills_dir: skillsDir,
      mcps: [],
      skills: [],
      loaded_at_ms: loadedAtMs,
      root_exists: false,
    };
  }

  return {
    root_dir: root,
    skills_dir: skillsDir,
    mcps: loadSharedMCPs(root, effectiveHome, opts.mcpTargets),
    skills: loadSharedSkills(skillsDir),
    loaded_at_ms: loadedAtMs,
    root_exists: true,
  };
}

// ---------------------------------------------------------------------------
// Skills loader
// ---------------------------------------------------------------------------

interface SkillFrontmatter {
  name: string;
  description: string;
  disableModelInvocation: boolean;
  paths: string[];
  icon: string;
  color: string;
}

function normalizeInline(s: string): string {
  return s.trim().split(/\s+/).filter(Boolean).join(" ");
}

function findSkillDoc(dirPath: string): string {
  for (const name of ["SKILL.md", "skill.md"]) {
    const p = path.join(dirPath, name);
    if (isFile(p)) {
      return p;
    }
  }
  return "";
}

function parseYAMLScalar(raw: string): string {
  const s = raw.trim();
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) {
    try {
      const parsed: unknown = JSON.parse(s);
      if (typeof parsed === "string") {
        return parsed;
      }
    } catch {
      // Fallback below matches Go implementation.
    }
    return s.slice(1, -1).replaceAll('\\"', '"');
  }
  if (s.length >= 2 && s.startsWith("'") && s.endsWith("'")) {
    return s.slice(1, -1).replaceAll("''", "'");
  }
  return s;
}

function parseYAMLInlineList(raw: string): string[] {
  let inner = raw.trim();
  if (inner.startsWith("[")) {
    inner = inner.slice(1);
  }
  if (inner.endsWith("]")) {
    inner = inner.slice(0, -1);
  }
  inner = inner.trim();
  if (!inner) {
    return [];
  }

  const out: string[] = [];
  let cur = "";
  let inSingle = false;
  let inDouble = false;
  let escaped = false;

  for (let i = 0; i < inner.length; i += 1) {
    const c = inner[i]!;
    if (escaped) {
      cur += c;
      escaped = false;
      continue;
    }
    if (c === "\\" && inDouble) {
      cur += c;
      escaped = true;
      continue;
    }
    if (c === '"' && !inSingle) {
      inDouble = !inDouble;
      cur += c;
      continue;
    }
    if (c === "'" && !inDouble) {
      inSingle = !inSingle;
      cur += c;
      continue;
    }
    if (c === "," && !inSingle && !inDouble) {
      const item = parseYAMLScalar(cur);
      if (item !== "") {
        out.push(item);
      }
      cur = "";
      continue;
    }
    cur += c;
  }
  const tail = parseYAMLScalar(cur);
  if (tail !== "") {
    out.push(tail);
  }
  return out;
}

function splitFrontmatter(rawInput: string): { fm: SkillFrontmatter; body: string } {
  const fm: SkillFrontmatter = {
    name: "",
    description: "",
    disableModelInvocation: false,
    paths: [],
    icon: "",
    color: "",
  };
  let raw = rawInput.replaceAll("\r\n", "\n");
  if (raw.startsWith("\uFEFF")) {
    raw = raw.slice(1);
  }
  const lines = raw.split("\n");
  if (lines.length < 3 || lines[0]!.trim() !== "---") {
    return { fm, body: raw };
  }

  let endIdx = -1;
  for (let i = 1; i < lines.length; i += 1) {
    const t = lines[i]!.trim();
    if (t === "---" || t === "...") {
      endIdx = i;
      break;
    }
  }
  if (endIdx < 0) {
    return { fm, body: raw };
  }

  const fmLines = lines.slice(1, endIdx);
  const body = lines.slice(endIdx + 1).join("\n");

  for (let i = 0; i < fmLines.length; i += 1) {
    const ln = fmLines[i]!;
    const trimmed = ln.trim();
    if (!trimmed || trimmed.startsWith("#")) {
      continue;
    }
    if (ln[0] === " " || ln[0] === "\t") {
      continue;
    }
    const colon = ln.indexOf(":");
    if (colon < 0) {
      continue;
    }
    const key = ln.slice(0, colon).trim();
    const val = ln.slice(colon + 1).trim();

    switch (key) {
      case "name":
        fm.name = parseYAMLScalar(val);
        break;
      case "icon":
        fm.icon = parseYAMLScalar(val);
        break;
      case "color":
        fm.color = parseYAMLScalar(val);
        break;
      case "disable-model-invocation":
        fm.disableModelInvocation = parseYAMLScalar(val).toLowerCase() === "true";
        break;
      case "description":
        if (val === ">" || val === ">-" || val === "|" || val === "|-") {
          const block: string[] = [];
          while (i + 1 < fmLines.length) {
            const next = fmLines[i + 1]!;
            if (next === "") {
              block.push("");
              i += 1;
              continue;
            }
            if (next[0] === " " || next[0] === "\t") {
              block.push(next.trim());
              i += 1;
              continue;
            }
            break;
          }
          fm.description = block.join(" ");
        } else {
          fm.description = parseYAMLScalar(val);
        }
        break;
      case "paths":
        if (val.startsWith("[") && val.endsWith("]")) {
          fm.paths = parseYAMLInlineList(val);
        } else if (val === "") {
          while (i + 1 < fmLines.length) {
            const next = fmLines[i + 1]!.trim();
            if (next.startsWith("- ")) {
              const item = parseYAMLScalar(next.slice(2).trim());
              if (item !== "") {
                fm.paths.push(item);
              }
              i += 1;
              continue;
            }
            break;
          }
        } else {
          const item = parseYAMLScalar(val);
          if (item !== "") {
            fm.paths.push(item);
          }
        }
        break;
    }
  }

  return { fm, body };
}

function firstMarkdownParagraph(body: string): string {
  const para: string[] = [];
  let inFence = false;
  for (const ln of body.split("\n")) {
    const t = ln.trim();
    if (t.startsWith("```")) {
      if (para.length > 0) {
        break;
      }
      inFence = !inFence;
      continue;
    }
    if (inFence) {
      continue;
    }
    if (!t) {
      if (para.length > 0) {
        break;
      }
      continue;
    }
    if (t.startsWith("#")) {
      if (para.length > 0) {
        break;
      }
      continue;
    }
    para.push(t);
  }
  return normalizeInline(para.join(" "));
}

function countNonHiddenFilesRecursive(dirPath: string): number {
  let count = 0;
  let entries;
  try {
    entries = readdirSync(dirPath, { withFileTypes: true });
  } catch {
    return 0;
  }
  for (const de of entries) {
    const name = de.name;
    if (name.startsWith(".")) {
      continue;
    }
    const full = path.join(dirPath, name);
    if (de.isDirectory()) {
      count += countNonHiddenFilesRecursive(full);
    } else if (de.isFile() || (de.isSymbolicLink() && isFile(full))) {
      count += 1;
    } else if (de.isSymbolicLink() && isDir(full)) {
      count += countNonHiddenFilesRecursive(full);
    }
  }
  return count;
}

function inspectSkillDir(
  dirPath: string,
  mainDoc: string,
): { extraFiles: number; hasReferences: boolean; hasScripts: boolean } {
  let entries;
  try {
    entries = readdirSync(dirPath, { withFileTypes: true });
  } catch {
    return { extraFiles: 0, hasReferences: false, hasScripts: false };
  }

  let extraFiles = 0;
  let hasReferences = false;
  let hasScripts = false;

  for (const de of entries) {
    const name = de.name;
    if (name.startsWith(".") || name === mainDoc) {
      continue;
    }
    const full = path.join(dirPath, name);
    const entryIsDir = de.isDirectory() || (de.isSymbolicLink() && isDir(full));
    if (entryIsDir) {
      const lower = name.toLowerCase();
      if (lower === "references" || lower === "reference") {
        hasReferences = true;
      } else if (lower === "scripts" || lower === "script") {
        hasScripts = true;
      }
      extraFiles += countNonHiddenFilesRecursive(full);
      continue;
    }
    extraFiles += 1;
  }

  return { extraFiles, hasReferences, hasScripts };
}

function parseSkillFile(dirName: string, dirPath: string, skillPath: string): SharedSkill | null {
  let data: string;
  try {
    data = readFileSync(skillPath, "utf8");
  } catch {
    return null;
  }

  let modTimeMs = 0;
  try {
    modTimeMs = Math.trunc(statSync(skillPath).mtimeMs);
  } catch {
    modTimeMs = 0;
  }

  const { fm, body } = splitFrontmatter(data);
  const trimmedBody = body.trim();
  const description = fm.description
    ? normalizeInline(fm.description)
    : firstMarkdownParagraph(trimmedBody);
  const { extraFiles, hasReferences, hasScripts } = inspectSkillDir(
    dirPath,
    path.basename(skillPath),
  );

  return {
    name: fm.name || dirName,
    dir_name: dirName,
    description,
    path: skillPath,
    dir_path: dirPath,
    disable_model_invocation: fm.disableModelInvocation,
    paths: fm.paths,
    icon: fm.icon,
    color: fm.color,
    extra_files: extraFiles,
    has_references: hasReferences,
    has_scripts: hasScripts,
    body: trimmedBody,
    mod_time_ms: modTimeMs,
  };
}

/**
 * Scans immediate non-hidden subdirectories of `skillsDir` for `SKILL.md` / `skill.md`,
 * parses YAML frontmatter, inspects extra files, and sorts case-insensitively by `name`
 * then `dir_name`.
 */
export function loadSharedSkills(skillsDir: string): SharedSkill[] {
  let entries;
  try {
    entries = readdirSync(skillsDir, { withFileTypes: true });
  } catch {
    return [];
  }

  const skills: SharedSkill[] = [];
  for (const de of entries) {
    const name = de.name;
    if (name.startsWith(".")) {
      continue;
    }
    const dirPath = path.join(skillsDir, name);
    if (!isDir(dirPath)) {
      continue;
    }
    const skillPath = findSkillDoc(dirPath);
    if (!skillPath) {
      continue;
    }
    const sk = parseSkillFile(name, dirPath, skillPath);
    if (sk) {
      skills.push(sk);
    }
  }

  skills.sort((a, b) => {
    const la = a.name.toLowerCase();
    const lb = b.name.toLowerCase();
    if (la !== lb) {
      return la < lb ? -1 : 1;
    }
    if (a.dir_name === b.dir_name) {
      return 0;
    }
    return a.dir_name < b.dir_name ? -1 : 1;
  });

  return skills;
}

// ---------------------------------------------------------------------------
// MCP loader
// ---------------------------------------------------------------------------

function isRecord(val: unknown): val is Record<string, unknown> {
  return typeof val === "object" && val !== null && !Array.isArray(val);
}

function isShellWrapper(cmd: string): boolean {
  const base = path.basename(cmd.trim());
  return base === "sh" || base === "bash" || base === "zsh";
}

function isDedicatedMCPFile(filePath: string): boolean {
  const lower = path.basename(filePath).toLowerCase();
  return lower.includes("mcp") && lower.endsWith(".json");
}

function mergeMCP(byName: Map<string, SharedMCP>, item: SharedMCP): void {
  const key = item.name.trim().toLowerCase();
  if (!key) {
    return;
  }
  const existing = byName.get(key);
  if (!existing) {
    byName.set(key, {
      ...item,
      args: [...item.args],
      harnesses: [...item.harnesses],
      env: { ...item.env },
    });
    return;
  }

  // Merge harness provenance.
  for (const h of item.harnesses) {
    if (!h) {
      continue;
    }
    if (!existing.harnesses.includes(h)) {
      existing.harnesses.push(h);
    }
  }

  // Prefer a direct non-shell command over a `sh -c` wrapper.
  if (
    existing.command === "" ||
    (isShellWrapper(existing.command) && item.command !== "" && !isShellWrapper(item.command))
  ) {
    existing.command = item.command;
    existing.args = [...item.args];
    if (item.source_path !== "") {
      existing.source_path = item.source_path;
    }
    if (item.raw_json !== "") {
      existing.raw_json = item.raw_json;
    }
  }

  if (existing.url === "" && item.url !== "") {
    existing.url = item.url;
  }
  if (existing.description === "" && item.description !== "") {
    existing.description = item.description;
  }
  if (existing.exposure === "" && item.exposure !== "") {
    existing.exposure = item.exposure;
  }
  if (existing.transport === "" && item.transport !== "") {
    existing.transport = item.transport;
  }
  for (const [k, v] of Object.entries(item.env)) {
    if (v !== undefined && !Object.hasOwn(existing.env, k)) {
      existing.env[k] = v;
    }
  }

  // Prefer a dedicated *mcp*.json source path when available.
  if (
    !isDedicatedMCPFile(existing.source_path) &&
    isDedicatedMCPFile(item.source_path) &&
    !isShellWrapper(item.command)
  ) {
    existing.source_path = item.source_path;
    if (item.raw_json !== "") {
      existing.raw_json = item.raw_json;
    }
  }
}

function finalizeMCPMap(byName: Map<string, SharedMCP>): SharedMCP[] {
  const out: SharedMCP[] = [];
  for (const m of byName.values()) {
    m.harnesses.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
    if (m.transport === "") {
      if (m.url !== "") {
        m.transport = "http";
      } else if (m.command !== "") {
        m.transport = "stdio";
      }
    }
    out.push(m);
  }
  out.sort((a, b) => {
    const la = a.name.toLowerCase();
    const lb = b.name.toLowerCase();
    if (la === lb) {
      return 0;
    }
    return la < lb ? -1 : 1;
  });
  return out;
}

function parseMCPServerObject(
  name: string,
  rawObj: unknown,
  filePath: string,
  sourceKind: string,
  harness: string,
): SharedMCP | null {
  if (!isRecord(rawObj)) {
    return null;
  }

  let command = "";
  let args: string[] = [];
  if (typeof rawObj.command === "string") {
    command = rawObj.command.trim();
  } else if (Array.isArray(rawObj.command)) {
    const strSlice = rawObj.command.filter((x): x is string => typeof x === "string");
    if (strSlice.length > 0) {
      command = strSlice[0]!.trim();
      if (strSlice.length > 1) {
        args = strSlice.slice(1);
      }
    }
  }

  if (args.length === 0 && Array.isArray(rawObj.args)) {
    args = rawObj.args.filter((x): x is string => typeof x === "string");
  }

  let url = "";
  for (const urlKey of ["url", "serverUrl", "httpUrl"]) {
    if (!url && typeof rawObj[urlKey] === "string") {
      url = rawObj[urlKey].trim();
    }
  }

  if (!command && !url) {
    return null;
  }

  let transport = "";
  for (const tKey of ["transport", "type"]) {
    if (!transport && typeof rawObj[tKey] === "string") {
      transport = rawObj[tKey].trim();
    }
  }
  if (!transport || transport === "local") {
    transport = url ? "http" : "stdio";
  }

  const description =
    typeof rawObj.description === "string" ? normalizeInline(rawObj.description) : "";
  const exposure = typeof rawObj.exposure === "string" ? rawObj.exposure.trim() : "";

  let disabled = false;
  if (typeof rawObj.disabled === "boolean") {
    disabled = rawObj.disabled;
  }
  if (typeof rawObj.enabled === "boolean" && !rawObj.enabled) {
    disabled = true;
  }

  const env: Record<string, string> = {};
  if (isRecord(rawObj.env)) {
    for (const [k, v] of Object.entries(rawObj.env)) {
      if (typeof v === "string") {
        env[k] = v;
      }
    }
  }

  let rawJson = "";
  try {
    rawJson = JSON.stringify(rawObj, null, 2);
  } catch {
    rawJson = "";
  }

  return {
    name: name.trim(),
    transport,
    command,
    args,
    url,
    description,
    exposure,
    disabled,
    env,
    source_path: filePath,
    source_kind: sourceKind,
    harnesses: harness ? [harness] : [],
    raw_json: rawJson,
  };
}

function parseMCPJSON(
  text: string,
  filePath: string,
  sourceKind: string,
  harness: string,
): SharedMCP[] {
  let root: unknown;
  try {
    root = JSON.parse(text);
  } catch {
    return [];
  }
  if (!isRecord(root)) {
    return [];
  }

  const out: SharedMCP[] = [];
  const collectMap = (raw: unknown): void => {
    if (!isRecord(raw)) {
      return;
    }
    const names = Object.keys(raw).sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
    for (const name of names) {
      const item = parseMCPServerObject(name, raw[name], filePath, sourceKind, harness);
      if (item) {
        out.push(item);
      }
    }
  };

  if (Object.hasOwn(root, "mcpServers")) {
    collectMap(root.mcpServers);
  }
  if (Object.hasOwn(root, "mcp_servers")) {
    collectMap(root.mcp_servers);
  }
  if (Object.hasOwn(root, "servers")) {
    collectMap(root.servers);
  }
  if (Object.hasOwn(root, "mcp") && isRecord(root.mcp)) {
    if (Object.hasOwn(root.mcp, "servers")) {
      collectMap(root.mcp.servers);
    } else {
      // Kilo-style: "mcp": { "<server>": { "command": [...], ... } }
      collectMap(root.mcp);
    }
  }

  // Single standalone server definition file (only for direct shared configs).
  if (out.length === 0 && sourceKind === "shared") {
    if (Object.hasOwn(root, "command") || Object.hasOwn(root, "url")) {
      const ext = path.extname(filePath);
      let name = path.basename(filePath, ext);
      if (typeof root.name === "string" && root.name.trim() !== "") {
        name = root.name.trim();
      }
      const item = parseMCPServerObject(name, root, filePath, sourceKind, harness);
      if (item) {
        out.push(item);
      }
    }
  }

  return out;
}

function stripTOMLComment(line: string): string {
  let inSingle = false;
  let inDouble = false;
  let escaped = false;
  for (let i = 0; i < line.length; i += 1) {
    const c = line[i]!;
    if (escaped) {
      escaped = false;
      continue;
    }
    if (c === "\\" && inDouble) {
      escaped = true;
      continue;
    }
    if (c === '"' && !inSingle) {
      inDouble = !inDouble;
    } else if (c === "'" && !inDouble) {
      inSingle = !inSingle;
    } else if (c === "#" && !inSingle && !inDouble) {
      return line.slice(0, i);
    }
  }
  return line;
}

function parseMCPTOML(
  text: string,
  filePath: string,
  sourceKind: string,
  harness: string,
): SharedMCP[] {
  const lines = text.replaceAll("\r\n", "\n").split("\n");
  const out: SharedMCP[] = [];
  let cur: SharedMCP | null = null;

  const newEntry = (name: string): SharedMCP => ({
    name,
    transport: "",
    command: "",
    args: [],
    url: "",
    description: "",
    exposure: "",
    disabled: false,
    env: {},
    source_path: filePath,
    source_kind: sourceKind,
    harnesses: harness ? [harness] : [],
    raw_json: "",
  });

  const flush = (): void => {
    if (!cur) {
      return;
    }
    if (cur.name !== "" && (cur.command !== "" || cur.url !== "")) {
      if (cur.transport === "") {
        cur.transport = cur.url !== "" ? "http" : "stdio";
      }
      if (cur.raw_json === "") {
        cur.raw_json = JSON.stringify(
          {
            command: cur.command,
            args: cur.args,
            url: cur.url,
            transport: cur.transport,
          },
          null,
          2,
        );
      }
      out.push(cur);
    }
    cur = null;
  };

  for (let i = 0; i < lines.length; i += 1) {
    const trimmed = stripTOMLComment(lines[i]!).trim();
    if (!trimmed) {
      continue;
    }
    if (trimmed.startsWith("[[") && trimmed.endsWith("]]")) {
      flush();
      const sec = trimmed.slice(2, -2).trim();
      if (sec === "mcp_servers") {
        cur = newEntry("");
      }
      continue;
    }
    if (trimmed.startsWith("[") && trimmed.endsWith("]")) {
      flush();
      const sec = trimmed.slice(1, -1).trim();
      if (sec.startsWith("mcp_servers.")) {
        const rest = sec.slice("mcp_servers.".length);
        // Ignore sub-tables like [mcp_servers.foo.env].
        if (!rest.includes(".")) {
          cur = newEntry(parseYAMLScalar(rest));
        }
      }
      continue;
    }
    if (!cur) {
      continue;
    }
    const eq = trimmed.indexOf("=");
    if (eq < 0) {
      continue;
    }
    const key = trimmed.slice(0, eq).trim();
    let val = trimmed.slice(eq + 1).trim();

    // Join multiline arrays.
    if (val.startsWith("[") && !val.includes("]")) {
      const parts = [val];
      while (i + 1 < lines.length) {
        i += 1;
        const part = stripTOMLComment(lines[i]!).trim();
        parts.push(part);
        if (part.includes("]")) {
          break;
        }
      }
      val = parts.join(" ");
    }

    switch (key) {
      case "name":
        cur.name = parseYAMLScalar(val);
        break;
      case "command":
        cur.command = parseYAMLScalar(val);
        break;
      case "url":
        cur.url = parseYAMLScalar(val);
        break;
      case "transport":
      case "type":
        cur.transport = parseYAMLScalar(val);
        break;
      case "description":
        cur.description = normalizeInline(parseYAMLScalar(val));
        break;
      case "args":
        if (val.startsWith("[") && val.endsWith("]")) {
          cur.args = parseYAMLInlineList(val);
        }
        break;
      case "disabled":
        cur.disabled = val.toLowerCase() === "true";
        break;
      case "enabled":
        if (val.toLowerCase() === "false") {
          cur.disabled = true;
        }
        break;
    }
  }

  flush();
  return out;
}

function parseMCPFile(filePath: string, sourceKind: string, harness: string): SharedMCP[] {
  let data: string;
  try {
    data = readFileSync(filePath, "utf8");
  } catch {
    return [];
  }
  if (filePath.toLowerCase().endsWith(".toml")) {
    return parseMCPTOML(data, filePath, sourceKind, harness);
  }
  return parseMCPJSON(data, filePath, sourceKind, harness);
}

function loadDirectMCPs(root: string): SharedMCP[] {
  let entries;
  try {
    entries = readdirSync(root, { withFileTypes: true });
  } catch {
    return [];
  }

  const files: string[] = [];
  for (const de of entries) {
    const name = de.name;
    if (name.startsWith(".")) {
      continue;
    }
    const lower = name.toLowerCase();
    const full = path.join(root, name);
    const entryIsDir = de.isDirectory() || (de.isSymbolicLink() && isDir(full));
    if (!entryIsDir) {
      if (lower.includes("mcp") && (lower.endsWith(".json") || lower.endsWith(".toml"))) {
        files.push(full);
      }
      continue;
    }
    if (lower === "mcp" || lower === "mcps") {
      let subEntries;
      try {
        subEntries = readdirSync(full, { withFileTypes: true });
      } catch {
        continue;
      }
      for (const sub of subEntries) {
        if (sub.name.startsWith(".")) {
          continue;
        }
        const subFull = path.join(full, sub.name);
        const subLower = sub.name.toLowerCase();
        const subIsDir = sub.isDirectory() || (sub.isSymbolicLink() && isDir(subFull));
        if (!subIsDir) {
          if (subLower.endsWith(".json") || subLower.endsWith(".toml")) {
            files.push(subFull);
          }
          continue;
        }
        for (const cand of ["mcp.json", "mcp_config.json", "config.json", "config.toml"]) {
          const cp = path.join(subFull, cand);
          if (isFile(cp)) {
            files.push(cp);
          }
        }
      }
    }
  }

  files.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));

  const byName = new Map<string, SharedMCP>();
  for (const f of files) {
    for (const item of parseMCPFile(f, "shared", "")) {
      mergeMCP(byName, item);
    }
  }
  return finalizeMCPMap(byName);
}

function loadSiblingHarnessMCPs(harnessesRoot: string): SharedMCP[] {
  let entries;
  try {
    entries = readdirSync(harnessesRoot, { withFileTypes: true });
  } catch {
    return [];
  }

  const byName = new Map<string, SharedMCP>();
  for (const de of entries) {
    const name = de.name;
    if (name.startsWith(".") || name === "shared") {
      continue;
    }
    const dir = path.join(harnessesRoot, name);
    if (!isDir(dir)) {
      continue;
    }
    for (const cand of SIBLING_MCP_CANDIDATES) {
      const p = path.join(dir, cand);
      if (!isFile(p)) {
        continue;
      }
      for (const item of parseMCPFile(p, "harnesses", name)) {
        mergeMCP(byName, item);
      }
    }
  }
  return finalizeMCPMap(byName);
}

const DEFAULT_TARGET_LABEL_BY_PATH = new Map<string, string>(
  DEFAULT_MCP_CONFIG_TARGETS.map((t) => [t.relPath, t.harnessLabel]),
);

function inferHarnessLabel(relPath: string): string {
  const normalized = relPath.replaceAll("\\", "/").replace(/^\/+/, "");
  const known = DEFAULT_TARGET_LABEL_BY_PATH.get(normalized);
  if (known) {
    return known;
  }
  const segments = normalized.split("/").filter(Boolean);
  if (segments.length === 0) {
    return "";
  }
  if (segments[0] === ".config" && segments.length >= 2) {
    return segments[1]!.replace(/^\.+/, "");
  }
  const first = segments[0]!.replace(/^\.+/, "");
  if (segments.length === 1) {
    return path.basename(first, path.extname(first));
  }
  return first;
}

function resolveMCPTargets(mcpTargets?: readonly string[]): MCPConfigTarget[] {
  if (!mcpTargets) {
    return [...DEFAULT_MCP_CONFIG_TARGETS];
  }
  const out: MCPConfigTarget[] = [];
  for (const raw of mcpTargets) {
    const trimmed = raw.trim();
    if (!trimmed) {
      continue;
    }
    const colon = trimmed.indexOf(":");
    if (colon > 0 && !path.isAbsolute(trimmed)) {
      const label = trimmed.slice(0, colon).trim();
      const rel = trimmed.slice(colon + 1).trim();
      if (rel) {
        out.push({ harnessLabel: label || inferHarnessLabel(rel), relPath: rel });
        continue;
      }
    }
    out.push({ harnessLabel: inferHarnessLabel(trimmed), relPath: trimmed });
  }
  return out;
}

function loadHomeHarnessMCPs(homeRoot: string, mcpTargets?: readonly string[]): SharedMCP[] {
  if (!isDir(homeRoot)) {
    return [];
  }
  const targets = resolveMCPTargets(mcpTargets);
  const byName = new Map<string, SharedMCP>();
  for (const target of targets) {
    const p = path.isAbsolute(target.relPath)
      ? target.relPath
      : path.join(homeRoot, ...target.relPath.split("/"));
    if (!existsSync(p) || !isFile(p)) {
      continue;
    }
    for (const item of parseMCPFile(p, "harnesses", target.harnessLabel)) {
      mergeMCP(byName, item);
    }
  }
  return finalizeMCPMap(byName);
}

/**
 * Discovers shared MCP server definitions using a 3-pass fallback:
 * 1. Direct MCP files/dirs under `root`
 * 2. Sibling harness directories under `dirname(root)` when `basename(root) === "shared"`
 * 3. Stowed home harness configs using `mcpTargets` when `root` is `<home>/.agents` or `<home>`
 */
export function loadSharedMCPs(
  root: string,
  homeDir: string,
  mcpTargets?: readonly string[],
): SharedMCP[] {
  const direct = loadDirectMCPs(root);
  if (direct.length > 0) {
    return direct;
  }

  const base = path.basename(root);
  if (base === "shared") {
    return loadSiblingHarnessMCPs(path.dirname(root));
  }

  if (base === ".agents") {
    return loadHomeHarnessMCPs(path.dirname(root), mcpTargets);
  }

  if (homeDir) {
    const cleanRoot = path.resolve(root);
    const cleanHome = path.resolve(homeDir);
    const cleanRealHome = path.resolve(realHomeDir(homeDir));
    if (cleanRoot === cleanHome || cleanRoot === cleanRealHome) {
      return loadHomeHarnessMCPs(root, mcpTargets);
    }
  }

  return [];
}

// ---------------------------------------------------------------------------
// Fuzzy matching & scoring (exact port of internal/model/fuzzy.go + shared.go)
// ---------------------------------------------------------------------------

const FUZZY_BASE_MATCH = 16;
const FUZZY_PREFIX_BONUS = 24;
const FUZZY_WORD_BOUNDARY = 14;
const FUZZY_CAMEL_BOUNDARY = 12;
const FUZZY_CONSECUTIVE_BONUS = 18;
const FUZZY_EXACT_CASE_BONUS = 1;
const FUZZY_GAP_START_PENALTY = 3;
const FUZZY_GAP_EXTEND_PENALTY = 1;
const FUZZY_MAX_LEAD_PENALTY = 12;
const FUZZY_DP_MAX_PAT = 64;
const FUZZY_DP_MAX_TEXT = 512;
const NEG_INF = -536_870_912; // Math.trunc(MinInt32 / 4)

const WORD_SEPARATORS = new Set([
  "/",
  "\\",
  "-",
  "_",
  ".",
  ":",
  " ",
  "\t",
  "(",
  ")",
  "[",
  "]",
  "{",
  "}",
  ",",
  ";",
]);

function isWordSeparator(ch: string): boolean {
  return WORD_SEPARATORS.has(ch);
}

function isLowerChar(ch: string): boolean {
  return ch !== ch.toUpperCase() && ch === ch.toLowerCase();
}

function isUpperChar(ch: string): boolean {
  return ch !== ch.toLowerCase() && ch === ch.toUpperCase();
}

function boundaryBonusAt(txt: readonly string[], idx: number): number {
  if (idx === 0) {
    return FUZZY_PREFIX_BONUS;
  }
  const prev = txt[idx - 1]!;
  const cur = txt[idx]!;
  if (isWordSeparator(prev) && !isWordSeparator(cur)) {
    return FUZZY_WORD_BOUNDARY;
  }
  if (isLowerChar(prev) && isUpperChar(cur)) {
    return FUZZY_CAMEL_BOUNDARY;
  }
  return 0;
}

export interface FuzzyMatchResult {
  score: number;
  positions: number[];
  matched: boolean;
}

function fuzzyMatchDP(
  pat: readonly string[],
  lowPat: readonly string[],
  txt: readonly string[],
  lowTxt: readonly string[],
): FuzzyMatchResult {
  const m = pat.length;
  const n = txt.length;

  const bonuses = new Int32Array(n);
  for (let j = 0; j < n; j += 1) {
    bonuses[j] = boundaryBonusAt(txt, j);
  }

  let prevRow = new Int32Array(n);
  let currRow = new Int32Array(n);
  const parent = new Int32Array(m * n).fill(-1);

  for (let j = 0; j < n; j += 1) {
    if (lowTxt[j] === lowPat[0]) {
      const leadPen = Math.min(j, FUZZY_MAX_LEAD_PENALTY);
      let s = FUZZY_BASE_MATCH + bonuses[j]! - leadPen;
      if (txt[j] === pat[0]) {
        s += FUZZY_EXACT_CASE_BONUS;
      }
      prevRow[j] = s;
    } else {
      prevRow[j] = NEG_INF;
    }
  }

  for (let i = 1; i < m; i += 1) {
    currRow.fill(NEG_INF);
    let bestGap = NEG_INF;
    let bestGapIdx = -1;
    for (let j = i; j < n; j += 1) {
      if (bestGapIdx >= 0) {
        bestGap -= FUZZY_GAP_EXTEND_PENALTY;
      }
      if (j >= 2 && prevRow[j - 2]! > NEG_INF) {
        const cand = prevRow[j - 2]! - FUZZY_GAP_START_PENALTY;
        if (cand > bestGap) {
          bestGap = cand;
          bestGapIdx = j - 2;
        }
      }
      if (lowTxt[j] !== lowPat[i]) {
        continue;
      }
      let bestScore = NEG_INF;
      let bestPrev = -1;

      if (prevRow[j - 1]! > NEG_INF) {
        let s =
          prevRow[j - 1]! +
          FUZZY_BASE_MATCH +
          FUZZY_CONSECUTIVE_BONUS +
          Math.trunc(bonuses[j]! / 2);
        if (txt[j] === pat[i]) {
          s += FUZZY_EXACT_CASE_BONUS;
        }
        if (s > bestScore) {
          bestScore = s;
          bestPrev = j - 1;
        }
      }
      if (bestGapIdx >= 0 && bestGap > NEG_INF) {
        let s = bestGap + FUZZY_BASE_MATCH + bonuses[j]!;
        if (txt[j] === pat[i]) {
          s += FUZZY_EXACT_CASE_BONUS;
        }
        if (s > bestScore) {
          bestScore = s;
          bestPrev = bestGapIdx;
        }
      }
      currRow[j] = bestScore;
      parent[i * n + j] = bestPrev;
    }
    const tmp = prevRow;
    prevRow = currRow;
    currRow = tmp;
  }

  let bestEnd = -1;
  let bestScore = NEG_INF;
  for (let j = m - 1; j < n; j += 1) {
    if (prevRow[j]! > bestScore) {
      bestScore = prevRow[j]!;
      bestEnd = j;
    }
  }
  if (bestEnd < 0 || bestScore <= NEG_INF) {
    return { score: 0, positions: [], matched: false };
  }

  const positions = new Array<number>(m);
  let idx = bestEnd;
  for (let i = m - 1; i >= 0; i -= 1) {
    positions[i] = idx;
    idx = parent[i * n + idx]!;
  }

  const trailing = n - 1 - bestEnd;
  if (trailing > 0) {
    const pen = Math.min(Math.trunc(trailing / 4), 10);
    bestScore -= pen;
  }
  if (m === n && bestEnd === n - 1) {
    bestScore += 12;
  }
  return { score: bestScore, positions, matched: true };
}

function fuzzyMatchTight(
  pat: readonly string[],
  lowPat: readonly string[],
  txt: readonly string[],
  lowTxt: readonly string[],
  end: number,
): FuzzyMatchResult {
  const m = pat.length;
  const n = txt.length;
  const positions = new Array<number>(m);
  let pi = m - 1;
  for (let ti = end; ti >= 0 && pi >= 0; ti -= 1) {
    if (lowTxt[ti] === lowPat[pi]) {
      positions[pi] = ti;
      pi -= 1;
    }
  }
  if (pi >= 0) {
    return { score: 0, positions: [], matched: false };
  }

  let score = 0;
  for (let i = 0; i < m; i += 1) {
    const pos = positions[i]!;
    const b = boundaryBonusAt(txt, pos);
    if (i === 0) {
      const leadPen = Math.min(pos, FUZZY_MAX_LEAD_PENALTY);
      score += FUZZY_BASE_MATCH + b - leadPen;
    } else {
      const gap = pos - positions[i - 1]! - 1;
      if (gap === 0) {
        score += FUZZY_BASE_MATCH + FUZZY_CONSECUTIVE_BONUS + Math.trunc(b / 2);
      } else {
        score +=
          FUZZY_BASE_MATCH +
          b -
          FUZZY_GAP_START_PENALTY -
          (gap - 1) * FUZZY_GAP_EXTEND_PENALTY;
      }
    }
    if (txt[pos] === pat[i]) {
      score += FUZZY_EXACT_CASE_BONUS;
    }
  }

  const trailing = n - 1 - positions[m - 1]!;
  if (trailing > 0) {
    const pen = Math.min(Math.trunc(trailing / 4), 10);
    score -= pen;
  }
  return { score, positions, matched: true };
}

/**
 * Unicode-safe, case-insensitive fzf-style subsequence fuzzy matcher.
 */
export function fuzzyMatch(pattern: string, text: string): FuzzyMatchResult {
  if (pattern === "") {
    return { score: 0, positions: [], matched: true };
  }
  const pat = Array.from(pattern);
  const txt = Array.from(text);
  const m = pat.length;
  const n = txt.length;
  if (m === 0) {
    return { score: 0, positions: [], matched: true };
  }
  if (m > n) {
    return { score: 0, positions: [], matched: false };
  }

  const lowPat = pat.map((r) => r.toLowerCase());
  const lowTxt = txt.map((r) => r.toLowerCase());

  let firstEnd = -1;
  let pi = 0;
  for (let ti = 0; ti < n && pi < m; ti += 1) {
    if (lowTxt[ti] === lowPat[pi]) {
      pi += 1;
      if (pi === m) {
        firstEnd = ti;
      }
    }
  }
  if (pi < m) {
    return { score: 0, positions: [], matched: false };
  }

  if (m <= FUZZY_DP_MAX_PAT && n <= FUZZY_DP_MAX_TEXT) {
    return fuzzyMatchDP(pat, lowPat, txt, lowTxt);
  }
  return fuzzyMatchTight(pat, lowPat, txt, lowTxt, firstEnd);
}

interface WeightedField {
  text: string;
  mult: number;
  base: number;
  isName?: boolean;
}

export interface SharedFuzzyResult {
  score: number;
  highlights: number[];
  matched: boolean;
}

function tokenizeQuery(query: string): string[] {
  return query.trim().split(/\s+/).filter(Boolean);
}

function scoreSharedFields(
  tokens: readonly string[],
  fields: readonly WeightedField[],
): SharedFuzzyResult {
  if (tokens.length === 0) {
    return { score: 0, highlights: [], matched: true };
  }

  let total = 0;
  let namePosSet: Set<number> | null = null;

  for (const rawTok of tokens) {
    const tok = rawTok.trim();
    if (!tok) {
      continue;
    }
    let best = Number.NEGATIVE_INFINITY;
    let matched = false;
    for (const f of fields) {
      if (!f.text) {
        continue;
      }
      const res = fuzzyMatch(tok, f.text);
      if (!res.matched) {
        continue;
      }
      matched = true;
      if (f.isName && res.positions.length > 0) {
        if (!namePosSet) {
          namePosSet = new Set<number>();
        }
        for (const idx of res.positions) {
          namePosSet.add(idx);
        }
      }
      const w = f.base + res.score * f.mult;
      if (w > best) {
        best = w;
      }
    }
    if (!matched) {
      return { score: -1, highlights: [], matched: false };
    }
    total += best;
  }

  const highlights = namePosSet ? Array.from(namePosSet).sort((a, b) => a - b) : [];
  return { score: total, highlights, matched: true };
}

/**
 * Evaluates multi-token AND fuzzy matching across a skill's name, directory slug,
 * description, and paths with exact Go weights.
 */
export function fuzzyMatchSharedSkill(skill: SharedSkill, query: string): SharedFuzzyResult {
  const tokens = tokenizeQuery(query);
  return scoreSharedFields(tokens, [
    { text: skill.name, mult: 4, base: 260, isName: true },
    { text: skill.dir_name, mult: 3, base: 200 },
    { text: skill.description, mult: 2, base: 100 },
    { text: skill.paths.join(" "), mult: 1, base: 60 },
  ]);
}

/**
 * Evaluates multi-token AND fuzzy matching across an MCP server's name, description,
 * command/args/URL, transport, harnesses, and source kind with exact Go weights.
 */
export function fuzzyMatchSharedMCP(mcp: SharedMCP, query: string): SharedFuzzyResult {
  const tokens = tokenizeQuery(query);
  return scoreSharedFields(tokens, [
    { text: mcp.name, mult: 4, base: 260, isName: true },
    { text: mcp.description, mult: 2, base: 120 },
    { text: sharedMCPCommandLine(mcp), mult: 2, base: 100 },
    { text: mcp.transport, mult: 2, base: 80 },
    { text: mcp.harnesses.join(" "), mult: 1, base: 60 },
    { text: mcp.source_kind, mult: 1, base: 50 },
  ]);
}

/**
 * Returns the multi-token AND fuzzy score for a `SharedSkill` (`0` for empty query,
 * `-1` when any token fails to match).
 */
export function fuzzyScoreSharedSkill(skill: SharedSkill, query: string): number {
  const res = fuzzyMatchSharedSkill(skill, query);
  return res.matched ? res.score : -1;
}

/**
 * Returns the multi-token AND fuzzy score for a `SharedMCP` (`0` for empty query,
 * `-1` when any token fails to match).
 */
export function fuzzyScoreSharedMCP(mcp: SharedMCP, query: string): number {
  const res = fuzzyMatchSharedMCP(mcp, query);
  return res.matched ? res.score : -1;
}

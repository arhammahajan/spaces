import { randomBytes } from "node:crypto";
import type {
  WorktreeNameSuggestion,
  WorktreeNamingInput,
} from "@spaces/types";

const MAX_SLUG_LENGTH = 36;
const MAX_DIR_NAME_LENGTH = 64;
const MAX_SLUG_WORDS = 5;

export const NAMING_ADJECTIVES: readonly string[] = [
  "amber", "avid", "bold", "bright", "brisk", "calm", "clear", "cobalt",
  "cool", "coral", "crisp", "daring", "deft", "eager", "fair", "firm",
  "fleet", "fresh", "gentle", "glad", "golden", "grand", "green", "hale",
  "happy", "hardy", "ivory", "jade", "keen", "kind", "light", "lively",
  "lucid", "lunar", "mellow", "mint", "misty", "neat", "noble", "nordic",
  "ochre", "olive", "opal", "plain", "plucky", "prime", "pure", "quick",
  "quiet", "rapid", "ready", "rosy", "royal", "ruby", "sage", "sharp",
  "silver", "sleek", "smart", "smooth", "solar", "solid", "steady", "swift",
];

export const NAMING_NOUNS: readonly string[] = [
  "acorn", "alder", "anchor", "anvil", "arch", "aspen", "atlas", "badger",
  "beacon", "birch", "bison", "brook", "cairn", "canyon", "cedar", "cliff",
  "clover", "comet", "condor", "crane", "creek", "crest", "delta", "drift",
  "eagle", "ember", "falcon", "fern", "finch", "fjord", "flint", "forge",
  "fox", "glade", "grove", "harbor", "haven", "hawk", "heron", "island",
  "jasper", "kestrel", "larch", "lark", "laurel", "ledge", "linden", "lynx",
  "maple", "meadow", "mesa", "otter", "owl", "peak", "pebble", "pine",
  "plover", "quartz", "raven", "reef", "ridge", "river", "robin", "spruce",
];

const GENERIC_SHELL_NAMES = new Set([
  "zsh",
  "bash",
  "sh",
  "fish",
  "nu",
  "nushell",
  "tcsh",
  "csh",
  "ksh",
  "dash",
  "shell",
  "terminal",
  "term",
  "tmux",
  "login",
  "console",
]);

const GENERIC_TARGET_NAMES = new Set([
  "space",
  "tab",
  "window",
  "pane",
  "default",
  "untitled",
  "new",
]);

const STOP_WORDS = new Set([
  "a", "an", "the", "and", "or", "but",
  "of", "in", "on", "at", "to", "for",
  "from", "by", "with", "into", "onto",
  "about", "as", "is", "are", "was", "were",
  "be", "been", "being", "it", "its", "this",
  "that", "these", "those", "i", "me", "my",
  "we", "our", "you", "your", "please", "can",
  "could", "would", "should", "will", "just",
  "want", "need", "help", "let", "lets", "make",
  "do", "does", "did", "have", "has", "had",
  "all", "some", "any", "so", "if", "then",
  "than", "there", "here", "when", "where",
  "how", "what", "which", "who", "why",
  "hey", "hi", "hello", "thanks", "thank",
]);

/**
 * Canonical short names for all known coding agents (parity with internal/agent/plugins).
 */
const KNOWN_AGENT_SHORT_NAMES: Record<string, string> = {
  // Primary agents
  antigravity: "agy",
  agy: "agy",
  "antigravity-cli": "agy",
  claude: "claude",
  "claude-code": "claude",
  claude_code: "claude",
  claudecode: "claude",
  codex: "codex",
  "openai-codex": "codex",
  "cursor-agent": "cursor",
  cursor: "cursor",
  cursor_agent: "cursor",
  "cursor-cli": "cursor",
  deepseek: "deepseek",
  dsh: "deepseek",
  "dsh-tui": "deepseek",
  kimi: "kimi",
  "kimi-cli": "kimi",
  "kimi-code": "kimi",
  "kimi code": "kimi",
  kilo: "kilo",
  "kilo-code": "kilo",
  "kilo code": "kilo",
  "herdr:kilo": "kilo",
  muse: "muse",
  "muse-code": "muse",
  "muse-cli": "muse",
  opencode: "opencode",
  "open-code": "opencode",
  "herdr:opencode": "opencode",
  oc: "opencode",
  // Harness-managed agents
  cline: "cline",
  copilot: "copilot",
  "github-copilot": "copilot",
  ghcs: "copilot",
  fx: "fx",
  "fx-router": "fx",
  gemini: "gemini",
  "gemini-cli": "gemini",
  grok: "grok",
  "grok-build": "grok",
  mcode: "mcode",
  pi: "pi",
  "herdr:pi": "pi",
  qwen: "qwen",
  "qwen-code": "qwen",
  "qwen code": "qwen",
  vibe: "vibe",
  "vibe-acp": "vibe",
  crush: "crush",
  omp: "omp",
  // Other + worktree-only agents
  amp: "amp",
  "amp-local": "amp",
  devin: "devin",
  "devin-cli": "devin",
  "devin cli": "devin",
  droid: "droid",
  hermes: "hermes",
  "hermes-agent": "hermes",
  kiro: "kiro",
  "kiro-cli": "kiro",
  letta: "letta",
  "letta-code": "letta",
  "letta code": "letta",
  maki: "maki",
  qodercli: "qodercli",
  qoderclicn: "qodercli",
  qoder: "qodercli",
  qodercn: "qodercli",
  aider: "aider",
  goose: "goose",
  windsurf: "windsurf",
  roo: "roo",
  "roo-code": "roo",
};

const WORKTREE_TITLE_PREFIXES: readonly string[] = [
  "claude code - ",
  "claude code: ",
  "claude code | ",
  "claude - ",
  "claude: ",
  "claude | ",
  "antigravity - ",
  "antigravity: ",
  "antigravity | ",
  "codex - ",
  "codex: ",
  "codex | ",
  "cursor - ",
  "cursor: ",
  "cursor | ",
  "deepseek harness - ",
  "deepseek harness: ",
  "deepseek - ",
  "deepseek: ",
  "deepseek | ",
  "gemini - ",
  "gemini: ",
  "gemini | ",
  "kimi code - ",
  "kimi code: ",
  "kimi cli - ",
  "kimi cli: ",
  "kimi - ",
  "kimi: ",
  "kimi | ",
  "kilo code - ",
  "kilo code: ",
  "kilo - ",
  "kilo: ",
  "kilo | ",
  "muse - ",
  "muse: ",
  "muse | ",
  "open code - ",
  "open code: ",
  "opencode - ",
  "opencode: ",
  "opencode | ",
];

const BOILERPLATE_TITLES = new Set([
  "claude code",
  "claude",
  "codex",
  "openai codex",
  "cursor",
  "cursor agent",
  "deepseek",
  "deepseek harness",
  "kimi",
  "kimi cli",
  "kimi code",
  "kilo",
  "kilo code",
  "muse",
  "muse cli",
  "opencode",
  "open code",
  "new chat",
  "new session",
]);

const URL_RE = /\b(?:https?:\/\/|www\.)\S+/gi;
const MD_LINK_RE = /\[([^\]]+)\]\([^)]+\)/g;
const VERSION_BANNER_RE =
  /^(?:[a-z0-9._-]+\s+)?v?\d+\.\d+(?:\.\d+)?(?:[-+][a-z0-9._-]+)?$/i;
const NUMBERED_GEN_RE =
  /^(?:tab|space|window|pane|zsh|bash|sh|fish|shell|terminal)[-_\s#]*\d+$/i;

const STATUS_GLYPHS = new Set([
  "✳", "●", "◐", "◑", "◒", "◓", "✓", "✔", "✗", "✘", "○", "◌", "•", "·",
  "✦", "✧", "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠐", "⠂",
  "⠁", "⠈", "⠠", "⠄",
]);

export interface NamingContext extends Partial<WorktreeNamingInput> {
  repo_name?: string;
  current_branch?: string;
  harness_profile?: string;
  prefix?: string;
}

export type TakenNameSet = ReadonlySet<string> | Readonly<Record<string, boolean>>;

/**
 * 64-bit SplitMix64 mixing function (exact uint64 parity with Go).
 */
export function splitMix64(x: bigint): bigint {
  let z = BigInt.asUintN(64, x + 0x9e3779b97f4a7c15n);
  z = BigInt.asUintN(64, (z ^ (z >> 30n)) * 0xbf58476d1ce4e5b9n);
  z = BigInt.asUintN(64, (z ^ (z >> 27n)) * 0x94d049bb133111ebn);
  return BigInt.asUintN(64, z ^ (z >> 31n));
}

function normalizeTaken(taken?: TakenNameSet): Set<string> {
  const out = new Set<string>();
  if (!taken) {
    return out;
  }
  if (taken instanceof Set) {
    for (const v of taken) {
      const low = v.trim().toLowerCase();
      if (low) {
        out.add(low);
      }
    }
    return out;
  }
  for (const [k, v] of Object.entries(taken)) {
    if (v) {
      const low = k.trim().toLowerCase();
      if (low) {
        out.add(low);
      }
    }
  }
  return out;
}

function collapseDashes(s: string): string {
  let out = "";
  let prevDash = false;
  for (let i = 0; i < s.length; i += 1) {
    const ch = s[i]!;
    if (ch === "-") {
      if (prevDash) {
        continue;
      }
      prevDash = true;
    } else {
      prevDash = false;
    }
    out += ch;
  }
  return out;
}

function trimChars(s: string, chars: string): string {
  let start = 0;
  let end = s.length;
  while (start < end && chars.includes(s[start]!)) {
    start += 1;
  }
  while (end > start && chars.includes(s[end - 1]!)) {
    end -= 1;
  }
  return s.slice(start, end);
}

function trimRightChars(s: string, chars: string): string {
  let end = s.length;
  while (end > 0 && chars.includes(s[end - 1]!)) {
    end -= 1;
  }
  return s.slice(0, end);
}

function sanitizeSegment(seg: string): string {
  const replaced = seg.replaceAll("@{", "-");
  let built = "";
  for (const ch of replaced) {
    const code = ch.charCodeAt(0);
    const isLower = code >= 0x61 && code <= 0x7a;
    const isDigit = code >= 0x30 && code <= 0x39;
    if (isLower || isDigit || ch === "." || ch === "_" || ch === "-") {
      built += ch;
    } else {
      built += "-";
    }
  }
  let s = built;
  while (s.includes("..")) {
    s = s.replaceAll("..", "-");
  }
  s = collapseDashes(s);
  while (true) {
    const prev = s;
    s = trimChars(s, "-.");
    if (s.endsWith(".lock")) {
      s = s.slice(0, -".lock".length);
    }
    if (s === prev) {
      break;
    }
  }
  return s;
}

/**
 * Converts an arbitrary string into a safe, lowercase Git branch name while
 * preserving '/' path segments.
 */
export function sanitizeBranchName(raw: string): string {
  const s = raw.trim().toLowerCase();
  if (!s) {
    return "";
  }
  const rawSegs = s.replaceAll("@{", "-").split("/");
  const outSegs: string[] = [];
  for (const seg of rawSegs) {
    const cleaned = sanitizeSegment(seg);
    if (cleaned) {
      outSegs.push(cleaned);
    }
  }
  const res = outSegs.join("/");
  if (res === "@") {
    return "";
  }
  return res;
}

/**
 * Converts a Git branch name into a filesystem-safe directory basename by
 * replacing '/' with '-', stripping invalid characters, and capping length.
 */
export function worktreeDirName(branch: string): string {
  const trimmed = branch.trim();
  if (!trimmed) {
    return "worktree";
  }
  const slashed = trimmed.replaceAll("/", "-");
  let built = "";
  for (const ch of slashed) {
    const code = ch.charCodeAt(0);
    const isLower = code >= 0x61 && code <= 0x7a;
    const isUpper = code >= 0x41 && code <= 0x5a;
    const isDigit = code >= 0x30 && code <= 0x39;
    if (isLower || isUpper || isDigit || ch === "." || ch === "_" || ch === "-") {
      built += ch;
    } else {
      built += "-";
    }
  }
  let out = built;
  while (out.includes("..")) {
    out = out.replaceAll("..", "-");
  }
  out = collapseDashes(out);
  out = trimChars(out, "-.");
  if (out.length > MAX_DIR_NAME_LENGTH) {
    out = trimRightChars(out.slice(0, MAX_DIR_NAME_LENGTH), "-.");
  }
  return out || "worktree";
}

/**
 * Verifies that `branch` is a safe, valid Git branch ref name.
 * Returns `null` when valid, or a descriptive error string when invalid.
 */
export function validateBranchName(branch: string): string | null {
  if (!branch) {
    return "branch name cannot be empty";
  }
  if (branch === "@") {
    return "branch name cannot be '@'";
  }
  if (branch.startsWith("-")) {
    return `branch name "${branch}" cannot start with '-'`;
  }
  if (branch.startsWith(".") || branch.startsWith("/")) {
    return `branch name "${branch}" cannot start with '.' or '/'`;
  }
  if (
    branch.endsWith(".") ||
    branch.endsWith("/") ||
    branch.endsWith(".lock")
  ) {
    return `branch name "${branch}" has invalid suffix`;
  }
  if (branch.includes("..")) {
    return `branch name "${branch}" cannot contain '..'`;
  }
  if (branch.includes("@{")) {
    return `branch name "${branch}" cannot contain '@{'`;
  }
  if (branch.includes("//")) {
    return `branch name "${branch}" cannot contain '//'`;
  }
  for (const ch of branch) {
    const code = ch.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f || /\s/.test(ch)) {
      return `branch name "${branch}" contains invalid whitespace or control character`;
    }
    if (
      ch === "~" ||
      ch === "^" ||
      ch === ":" ||
      ch === "?" ||
      ch === "*" ||
      ch === "[" ||
      ch === "\\"
    ) {
      return `branch name "${branch}" contains invalid character '${ch}'`;
    }
  }
  for (const seg of branch.split("/")) {
    if (!seg) {
      return `branch name "${branch}" contains empty path segment`;
    }
    if (seg.startsWith(".")) {
      return `branch name "${branch}" has segment starting with '.'`;
    }
    if (seg.endsWith(".lock")) {
      return `branch name "${branch}" has segment ending with '.lock'`;
    }
  }
  return null;
}

function resolvePrefix(ctx: NamingContext): string {
  let raw = (ctx.prefix ?? "").trim();
  if (!raw) {
    raw = (process.env.TMUX_SPACES_WORKTREE_PREFIX ?? "").trim();
  }
  if (!raw) {
    return "";
  }
  let sep = "/";
  if (raw.endsWith("-")) {
    sep = "-";
  } else if (raw.endsWith("_")) {
    sep = "_";
  } else if (raw.endsWith("/")) {
    sep = "/";
  }
  const clean = sanitizeBranchName(raw);
  if (!clean) {
    return "";
  }
  return clean + sep;
}

function applyPrefix(prefix: string, slug: string): string {
  if (!prefix || !slug) {
    return slug;
  }
  if (slug.toLowerCase().startsWith(prefix.toLowerCase())) {
    return slug;
  }
  return prefix + slug;
}

function isTaken(candidate: string, taken: Set<string>): boolean {
  if (taken.size === 0) {
    return false;
  }
  const low = candidate.toLowerCase();
  if (taken.has(low)) {
    return true;
  }
  return taken.has(worktreeDirName(candidate).toLowerCase());
}

function disambiguate(base: string, taken: Set<string>): string {
  if (!isTaken(base, taken)) {
    return base;
  }
  for (let i = 2; i <= 9999; i += 1) {
    const cand = `${base}-${i}`;
    if (!isTaken(cand, taken)) {
      return cand;
    }
  }
  return `${base}-${Math.floor(Date.now() / 1000) % 100000}`;
}

function pickRandomPair(
  seed: number,
  prefix: string,
  agentPrefix: string,
  taken: Set<string>,
): string {
  let state: bigint;
  if (seed === 0) {
    try {
      state = randomBytes(8).readBigUInt64LE(0);
    } catch {
      state = BigInt.asUintN(64, BigInt(Date.now()) * 1_000_000n);
    }
  } else {
    state = splitMix64(BigInt.asUintN(64, BigInt(Math.trunc(seed))));
  }

  const total = NAMING_ADJECTIVES.length * NAMING_NOUNS.length; // 4096
  const startIdx = Number(state % BigInt(total));
  // 137 is coprime with 4096, so stepping covers distinct adjective/noun pairs.
  const stride = 137;
  const maxProbes = 128;

  let firstBase = "";
  for (let i = 0; i < maxProbes; i += 1) {
    const idx = (startIdx + i * stride) % total;
    const adj = NAMING_ADJECTIVES[Math.floor(idx / NAMING_NOUNS.length)]!;
    const noun = NAMING_NOUNS[idx % NAMING_NOUNS.length]!;
    const base = applyPrefix(prefix, `${agentPrefix}${adj}-${noun}`);
    if (i === 0) {
      firstBase = base;
    }
    if (!isTaken(base, taken)) {
      return base;
    }
  }
  return disambiguate(firstBase, taken);
}

function knownCodingAgentShort(app: string | undefined): string | null {
  const low = (app ?? "").trim().toLowerCase();
  if (!low || GENERIC_SHELL_NAMES.has(low)) {
    return null;
  }
  return KNOWN_AGENT_SHORT_NAMES[low] ?? null;
}

function targetSourceLabel(kind: string | undefined): string {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "tab":
      return "from tab name";
    case "space":
      return "from space name";
    default:
      return "from name";
  }
}

function harnessSourceLabel(app: string | undefined, kind: string): string {
  const short = knownCodingAgentShort(app);
  if (short) {
    return `from ${short} ${kind}`;
  }
  const clean = (app ?? "").trim().toLowerCase();
  if (clean && !GENERIC_SHELL_NAMES.has(clean)) {
    const slug = sanitizeBranchName(clean);
    if (slug) {
      return `from ${slug} ${kind}`;
    }
  }
  return `from harness ${kind}`;
}

function stripLeadingStatusGlyphs(s: string): string {
  const chars = Array.from(s);
  let i = 0;
  while (i < chars.length) {
    const ch = chars[i]!;
    const code = ch.codePointAt(0) ?? 0;
    if (/\s/.test(ch)) {
      i += 1;
      continue;
    }
    if (code >= 0x2800 && code <= 0x28ff) {
      i += 1;
      continue;
    }
    if (STATUS_GLYPHS.has(ch)) {
      i += 1;
      continue;
    }
    break;
  }
  return chars.slice(i).join("");
}

function stripHarnessPrefixes(s: string): string {
  const low = s.toLowerCase();
  for (const p of WORKTREE_TITLE_PREFIXES) {
    if (low.startsWith(p)) {
      return s.slice(p.length).trim();
    }
  }
  return s;
}

function looksLikePath(s: string): boolean {
  if (
    s.startsWith("/") ||
    s.startsWith("~/") ||
    s.startsWith("./") ||
    s.startsWith("../") ||
    s === "~"
  ) {
    return true;
  }
  if (!/[ \t]/.test(s) && s.includes("/")) {
    return true;
  }
  return false;
}

function firstNonEmptyLine(s: string): string {
  for (const line of s.split("\n")) {
    const t = line.trim();
    if (!t || t.startsWith("```")) {
      continue;
    }
    return t;
  }
  return "";
}

function stripMarkdownPunctuation(s: string): string {
  let out = "";
  for (const ch of s) {
    if ("`*_#>~[](){}\"'|\\".includes(ch)) {
      out += " ";
    } else {
      out += ch;
    }
  }
  return out;
}

function firstSentence(s: string): string {
  const trimmed = s.trim();
  for (let i = 0; i < trimmed.length; i += 1) {
    const ch = trimmed[i]!;
    if (ch === "." || ch === "!" || ch === "?") {
      if (i + 1 === trimmed.length || /\s/.test(trimmed[i + 1]!)) {
        return trimmed.slice(0, i).trim();
      }
    }
  }
  return trimmed;
}

function tokenizeWords(s: string): string[] {
  const words: string[] = [];
  let cur = "";
  const flush = () => {
    if (cur.length > 0) {
      words.push(cur);
      cur = "";
    }
  };
  for (const ch of s.toLowerCase()) {
    const code = ch.charCodeAt(0);
    if ((code >= 0x61 && code <= 0x7a) || (code >= 0x30 && code <= 0x39)) {
      cur += ch;
    } else {
      flush();
    }
  }
  flush();
  return words;
}

function filterStopWords(words: readonly string[]): string[] {
  const out: string[] = [];
  for (const w of words) {
    if (w.length <= 1 || STOP_WORDS.has(w)) {
      continue;
    }
    out.push(w);
  }
  return out;
}

function joinSlugWords(
  words: readonly string[],
  maxWords: number,
  maxLen: number,
): string {
  const parts: string[] = [];
  let curLen = 0;
  for (const w of words) {
    if (parts.length >= maxWords) {
      break;
    }
    const addLen = w.length + (parts.length > 0 ? 1 : 0);
    if (curLen + addLen > maxLen) {
      if (parts.length === 0) {
        parts.push(w.slice(0, maxLen));
      }
      break;
    }
    parts.push(w);
    curLen += addLen;
  }
  return sanitizeBranchName(parts.join("-"));
}

function extractTargetSlug(
  target: string | undefined,
  repoNorm: string,
  curNorm: string,
  harnessApp: string | undefined,
): string {
  let raw = (target ?? "").trim();
  if (!raw) {
    return "";
  }
  raw = stripLeadingStatusGlyphs(raw);
  if (!raw) {
    return "";
  }
  const low = raw.toLowerCase();
  if (
    GENERIC_SHELL_NAMES.has(low) ||
    GENERIC_TARGET_NAMES.has(low) ||
    NUMBERED_GEN_RE.test(low)
  ) {
    return "";
  }
  if (looksLikePath(raw) || VERSION_BANNER_RE.test(raw)) {
    return "";
  }

  const appLow = (harnessApp ?? "").trim().toLowerCase();
  if (appLow) {
    if (low === appLow) {
      return "";
    }
    const short = knownCodingAgentShort(appLow);
    if (short && low === short) {
      return "";
    }
  }

  const rawNorm = sanitizeBranchName(raw);
  if (
    !rawNorm ||
    (repoNorm && rawNorm === repoNorm) ||
    (curNorm && rawNorm === curNorm)
  ) {
    return "";
  }

  let words = tokenizeWords(raw);
  if (words.length === 0) {
    return "";
  }
  if (words.length > 4) {
    const filtered = filterStopWords(words);
    if (filtered.length > 0) {
      words = filtered;
    }
  }
  const slug = joinSlugWords(words, MAX_SLUG_WORDS, MAX_SLUG_LENGTH);
  if (slug.length < 2) {
    return "";
  }
  if ((repoNorm && slug === repoNorm) || (curNorm && slug === curNorm)) {
    return "";
  }
  return slug;
}

function extractHarnessTitleSlug(
  title: string | undefined,
  repoNorm: string,
  curNorm: string,
): string {
  let s = (title ?? "").trim();
  if (!s) {
    return "";
  }
  while (true) {
    const prev = s;
    s = stripLeadingStatusGlyphs(s);
    s = stripHarnessPrefixes(s);
    s = s.trim();
    if (s === prev) {
      break;
    }
  }
  const pipeIdx = s.lastIndexOf(" | ");
  if (pipeIdx > 0) {
    const left = s.slice(0, pipeIdx).trim();
    if (left) {
      s = left;
    }
  }
  s = s.trim();
  if (!s) {
    return "";
  }
  const low = s.toLowerCase();
  if (
    GENERIC_SHELL_NAMES.has(low) ||
    GENERIC_TARGET_NAMES.has(low) ||
    NUMBERED_GEN_RE.test(low)
  ) {
    return "";
  }
  if (knownCodingAgentShort(low)) {
    return "";
  }
  if (BOILERPLATE_TITLES.has(low)) {
    return "";
  }
  if (looksLikePath(s) || VERSION_BANNER_RE.test(s)) {
    return "";
  }

  let words = tokenizeWords(s);
  if (words.length === 0) {
    return "";
  }
  const filtered = filterStopWords(words);
  if (filtered.length > 0) {
    words = filtered;
  }
  const slug = joinSlugWords(words, MAX_SLUG_WORDS, MAX_SLUG_LENGTH);
  if (slug.length < 2) {
    return "";
  }
  if (curNorm && (slug === curNorm || slug === worktreeDirName(curNorm))) {
    return "";
  }
  if (repoNorm && slug === repoNorm) {
    return "";
  }
  return slug;
}

function extractHarnessPromptSlug(
  prompt: string | undefined,
  curNorm: string,
): string {
  const line = firstNonEmptyLine(prompt ?? "");
  if (!line) {
    return "";
  }
  let cleaned = line.replace(URL_RE, " ");
  cleaned = cleaned.replace(MD_LINK_RE, "$1");
  cleaned = stripMarkdownPunctuation(cleaned);

  const sentence = firstSentence(cleaned);
  let words = filterStopWords(tokenizeWords(sentence));
  if (words.length < 2) {
    const allWords = filterStopWords(tokenizeWords(cleaned));
    if (allWords.length > words.length) {
      words = allWords;
    }
  }
  if (words.length === 0) {
    words = tokenizeWords(sentence);
  }
  const slug = joinSlugWords(words, MAX_SLUG_WORDS, MAX_SLUG_LENGTH);
  if (slug.length < 2) {
    return "";
  }
  if (curNorm && (slug === curNorm || slug === worktreeDirName(curNorm))) {
    return "";
  }
  return slug;
}

/**
 * Produces a worktree branch and directory name from `NamingContext`
 * following the 6-tier priority ruleset while avoiding collisions in `taken`.
 */
export function suggestName(
  ctx: NamingContext,
  taken?: TakenNameSet,
): WorktreeNameSuggestion {
  const takenSet = normalizeTaken(taken);

  // Tier 1: Explicit user-provided name. Never auto-suffixed or prefixed.
  const explicit = (ctx.explicit_name ?? "").trim();
  if (explicit) {
    const branch = sanitizeBranchName(explicit);
    if (branch) {
      return {
        branch,
        dir_name: worktreeDirName(branch),
        source: "custom",
        rule: "explicit",
      };
    }
  }

  const prefix = resolvePrefix(ctx);
  const curNorm = sanitizeBranchName(ctx.current_branch ?? "");
  const repoNorm = sanitizeBranchName(ctx.repo_name ?? "");
  const seed = ctx.seed ?? 0;

  // Tier 2: Meaningful target (tab/space) name (skipped on seed > 0 regeneration).
  if (seed === 0) {
    const slug = extractTargetSlug(
      ctx.target_name,
      repoNorm,
      curNorm,
      ctx.harness_app,
    );
    if (slug) {
      const branch = disambiguate(applyPrefix(prefix, slug), takenSet);
      return {
        branch,
        dir_name: worktreeDirName(branch),
        source: targetSourceLabel(ctx.target_kind),
        rule: "target_name",
      };
    }
  }

  // Tier 3: Harness title (only when seed === 0 so regeneration cycles to random names).
  if (seed === 0) {
    const slug = extractHarnessTitleSlug(ctx.harness_title, repoNorm, curNorm);
    if (slug) {
      const branch = disambiguate(applyPrefix(prefix, slug), takenSet);
      return {
        branch,
        dir_name: worktreeDirName(branch),
        source: harnessSourceLabel(ctx.harness_app, "title"),
        rule: "harness_title",
      };
    }
  }

  // Tier 4: Harness prompt (only when seed === 0).
  if (seed === 0) {
    const slug = extractHarnessPromptSlug(ctx.harness_prompt, curNorm);
    if (slug) {
      const branch = disambiguate(applyPrefix(prefix, slug), takenSet);
      return {
        branch,
        dir_name: worktreeDirName(branch),
        source: harnessSourceLabel(ctx.harness_app, "prompt"),
        rule: "harness_prompt",
      };
    }
  }

  // Tier 5: Known coding agent fallback (<shortAgent>-<adj>-<noun>).
  const shortAgent = knownCodingAgentShort(ctx.harness_app);
  if (shortAgent) {
    const branch = pickRandomPair(seed, prefix, `${shortAgent}-`, takenSet);
    return {
      branch,
      dir_name: worktreeDirName(branch),
      source: `from ${shortAgent}`,
      rule: "harness_agent",
    };
  }

  // Tier 6: Curated random fallback (<adj>-<noun>).
  const branch = pickRandomPair(seed, prefix, "", takenSet);
  return {
    branch,
    dir_name: worktreeDirName(branch),
    source: "auto",
    rule: "fallback",
  };
}

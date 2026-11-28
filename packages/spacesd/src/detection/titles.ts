import {
  bottomNonEmptyLines as regionBottomNonEmptyLines,
  isHorizontalRule,
  splitTextLines,
  topNonEmptyLines as regionTopNonEmptyLines,
} from "./regions";
import { canonicalApp, normalizeApp } from "./screen";

export type TitleExtractorFn = (viewport: string) => string;

const PLACEHOLDER_TITLES = new Set([
  "",
  "new session",
  "untitled",
  "launch overrides",
  "ask codex to do anything",
]);

const IGNORED_SLASH_COMMANDS = new Set([
  "/model",
  "/login",
  "/logout",
  "/clear",
  "/compact",
  "/help",
  "/status",
  "/config",
  "/setup",
]);

const EFFORT_SEGMENTS = new Set([
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "ultra",
  "auto",
  "plan",
  "default",
]);

const MUSE_TOKEN_METER_RE =
  /\s+\d+(?:\.\d+)?[kKmM]?\/\d+(?:\.\d+)?[kKmM]?(?:\s*\(\d+(?:\.\d+)?%\))?\s*$/;
const OPENCODE_TAB_LEAD_RE = /^[‹<]\d+\s+/;
const MULTI_SPACE_RE = /\s{2,}/;

const CURSOR_PREFIXES = ["cursor agent · ", "cursor · ", "session: "] as const;
const CURSOR_MARKERS = ["›", "❯", ">"] as const;

const DEEPSEEK_PREFIXES = ["deepseek · ", "dsh · ", "dsh-tui · ", "session: "] as const;
const DEEPSEEK_MARKERS = ["❯", ">"] as const;

const KIMI_PREFIXES = ["kimi code · ", "kimi · ", "session: "] as const;
const KIMI_MARKERS = ["❯", ">"] as const;

/**
 * Normalizes a candidate title extracted from a viewport, filtering out generic
 * placeholders and bare slash commands.
 */
export function cleanViewportTitle(s: string): string {
  const trimmed = s.trim();
  if (trimmed === "") {
    return "";
  }
  let normalized = trimmed.split(/\s+/).join(" ");
  const lower = normalized.toLowerCase();
  if (PLACEHOLDER_TITLES.has(lower) || lower.startsWith("new session - ")) {
    return "";
  }
  if (normalized.startsWith("/")) {
    const fields = normalized.split(/\s+/);
    if (fields.length <= 1) {
      return "";
    }
    if (IGNORED_SLASH_COMMANDS.has(fields[0]!.toLowerCase())) {
      return "";
    }
    normalized = fields.slice(1).join(" ");
  }
  const runes = Array.from(normalized);
  if (runes.length > 160) {
    return `${runes.slice(0, 159).join("")}…`;
  }
  return normalized;
}

/**
 * Reports whether `s` looks like a filesystem path segment.
 */
export function isPathSegment(s: string): boolean {
  const trimmed = s.trim();
  return (
    trimmed === "~" ||
    trimmed.startsWith("~/") ||
    trimmed.startsWith("/") ||
    trimmed.startsWith("./")
  );
}

/**
 * Reports whether `s` is a reasoning-effort token in status footers.
 */
export function isEffortSegment(s: string): boolean {
  return EFFORT_SEGMENTS.has(s.trim().toLowerCase());
}

/**
 * Returns the slice of `viewport` spanning the last `n` non-empty lines.
 */
export function bottomNonEmptyLines(viewport: string, n: number): string {
  return regionBottomNonEmptyLines(viewport, n);
}

/**
 * Returns the slice of `viewport` spanning the first `n` non-empty lines.
 */
export function topNonEmptyLines(viewport: string, n: number): string {
  return regionTopNonEmptyLines(viewport, n);
}

/**
 * Splits `s` into individual lines while preserving leading whitespace.
 */
export function splitLines(s: string): string[] {
  return splitTextLines(s).map((tl) => tl.text);
}

/**
 * Extracts the latest non-composer prompt line starting with `marker`.
 */
export function extractPromptMarkerLine(viewport: string, marker: string): string {
  const lines = splitLines(viewport);
  let found = "";
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]!.trim();
    if (!line.startsWith(marker)) {
      continue;
    }
    // Ignore composer box prompt lines (immediately bordered by horizontal rules).
    if (
      i > 0 &&
      i + 1 < lines.length &&
      isHorizontalRule(lines[i - 1]!) &&
      isHorizontalRule(lines[i + 1]!)
    ) {
      continue;
    }
    const rest = line.slice(marker.length).trim();
    if (rest === "") {
      continue;
    }
    if (rest.startsWith("Auto mode:") || rest.startsWith("You are in ")) {
      continue;
    }
    const cleaned = cleanViewportTitle(rest);
    if (cleaned !== "") {
      found = cleaned;
    }
  }
  return found;
}

/**
 * Extracts a prompt starting with `marker` plus up to 3 indented continuation lines.
 */
export function extractMultilinePrompt(viewport: string, marker: string): string {
  const lines = splitLines(viewport);
  let found = "";
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]!.trim();
    if (!line.startsWith(marker)) {
      continue;
    }
    if (
      i > 0 &&
      i + 1 < lines.length &&
      isHorizontalRule(lines[i - 1]!) &&
      isHorizontalRule(lines[i + 1]!)
    ) {
      continue;
    }
    const rest = line.slice(marker.length).trim();
    if (rest === "" || rest.startsWith("Auto mode:") || rest.includes(" Default ")) {
      continue;
    }
    const parts = [rest];
    for (let j = i + 1; j < lines.length && j <= i + 3; j += 1) {
      const nextRaw = lines[j]!;
      const nextTrim = nextRaw.trim();
      if (
        nextTrim === "" ||
        isHorizontalRule(nextTrim) ||
        nextTrim.startsWith("●") ||
        nextTrim.startsWith("▸") ||
        nextTrim.startsWith("◆") ||
        nextTrim.startsWith("•") ||
        nextTrim.startsWith("⎿") ||
        nextTrim.startsWith(marker)
      ) {
        break;
      }
      if (nextRaw.startsWith("  ")) {
        parts.push(nextTrim);
      } else {
        break;
      }
    }
    const cleaned = cleanViewportTitle(parts.join(" "));
    if (cleaned !== "") {
      found = cleaned;
    }
  }
  return found;
}

/**
 * Tries common prompt markers (`❯`, `›`, `>`) to extract a title.
 */
export function extractGenericTitle(viewport: string): string {
  for (const marker of ["❯", "›", ">"]) {
    const t = extractPromptMarkerLine(viewport, marker);
    if (t !== "") {
      return t;
    }
  }
  return "";
}

/**
 * Parses Codex footer lines (`<model> <effort> · <cwd> · <Title>`) or the
 * latest user prompt line (`› <prompt>`).
 */
export function extractCodexTitle(viewport: string): string {
  const bottom = bottomNonEmptyLines(viewport, 6);
  for (const raw of splitLines(bottom)) {
    const line = raw.trim();
    if (!line.includes(" · ")) {
      continue;
    }
    if (line.includes("limit:") || line.includes("resets at") || line.includes("warnings")) {
      continue;
    }
    const parts = line.split(" · ");
    if (parts.length >= 3 && isPathSegment(parts[1]!)) {
      const t = cleanViewportTitle(parts.slice(2).join(" · "));
      if (t !== "") {
        return t;
      }
    }
  }
  return extractPromptMarkerLine(viewport, "›");
}

/**
 * Parses Muse footer lines (`<model> · <effort> · <cwd> · <Title> [tokens]`)
 * or user prompt lines (`❯ <prompt>`).
 */
export function extractMuseTitle(viewport: string): string {
  const bottom = bottomNonEmptyLines(viewport, 5);
  for (const raw of splitLines(bottom)) {
    const line = raw.trim();
    const parts = line.split(" · ");
    if (parts.length >= 4 && isEffortSegment(parts[1]!) && isPathSegment(parts[2]!)) {
      const rawTitle = parts.slice(3).join(" · ").replace(MUSE_TOKEN_METER_RE, "");
      const t = cleanViewportTitle(rawTitle);
      if (t !== "") {
        return t;
      }
    }
  }
  return extractPromptMarkerLine(viewport, "❯");
}

/**
 * Parses OpenCode/Kilo top tab bars (`‹3  Tab1  ActiveTitle + ...`) or boxed
 * user prompts (`┃  <prompt>`).
 */
export function extractOpencodeTitle(viewport: string): string {
  const top = topNonEmptyLines(viewport, 4);
  for (const raw of splitLines(top)) {
    const line = raw.trim();
    if (OPENCODE_TAB_LEAD_RE.test(line) && line.includes("+")) {
      const rest = line.replace(OPENCODE_TAB_LEAD_RE, "");
      const segs = rest.split(MULTI_SPACE_RE);
      let plusIdx = -1;
      for (let i = 0; i < segs.length; i += 1) {
        const s = segs[i]!.trim();
        if (s === "+" || s.endsWith(" +")) {
          plusIdx = i;
          break;
        }
      }
      if (plusIdx >= 0) {
        const plusSeg = segs[plusIdx]!.trim();
        if (plusSeg.endsWith(" +")) {
          const t = cleanViewportTitle(plusSeg.slice(0, -2));
          if (t !== "") {
            return t;
          }
        }
        for (let i = plusIdx - 1; i >= 0; i -= 1) {
          const t = cleanViewportTitle(segs[i]!);
          if (t !== "") {
            return t;
          }
        }
      }
    }
  }

  // Fallback: boxed user prompt block (`┃  <prompt>`)
  for (const raw of splitLines(viewport)) {
    const trimmed = raw.trim();
    if (trimmed.startsWith("┃")) {
      const rest = trimmed.slice(1).trim();
      if (rest === "" || rest.includes(" · ")) {
        continue;
      }
      const t = cleanViewportTitle(rest);
      if (t !== "") {
        return t;
      }
    }
  }
  return "";
}

/**
 * Parses Antigravity CLI viewports (`> <prompt>` with up to 3 indented
 * continuation lines).
 */
export function extractAntigravityTitle(viewport: string): string {
  return extractMultilinePrompt(viewport, ">");
}

/**
 * Parses Claude Code viewports (`❯ <prompt>`).
 */
export function extractClaudeTitle(viewport: string): string {
  return extractPromptMarkerLine(viewport, "❯");
}

function extractHeaderPrefixedTitle(
  viewport: string,
  prefixes: readonly string[],
  markers: readonly string[],
): string {
  const top = topNonEmptyLines(viewport, 4);
  for (const raw of splitLines(top)) {
    const line = raw.trim();
    const lower = line.toLowerCase();
    for (const prefix of prefixes) {
      if (lower.startsWith(prefix)) {
        const t = cleanViewportTitle(line.slice(prefix.length));
        if (t !== "") {
          return t;
        }
      }
    }
  }
  for (const marker of markers) {
    const t = extractPromptMarkerLine(viewport, marker);
    if (t !== "") {
      return t;
    }
  }
  return extractGenericTitle(viewport);
}

/**
 * Parses Cursor Agent CLI session headers or prompt lines.
 */
export function extractCursorTitle(viewport: string): string {
  return extractHeaderPrefixedTitle(viewport, CURSOR_PREFIXES, CURSOR_MARKERS);
}

/**
 * Parses DeepSeek Harness (`dsh` / `dsh-tui`) header lines or prompt lines.
 */
export function extractDeepSeekTitle(viewport: string): string {
  return extractHeaderPrefixedTitle(viewport, DEEPSEEK_PREFIXES, DEEPSEEK_MARKERS);
}

/**
 * Parses Kimi Code / Kimi CLI status headers or prompt lines.
 */
export function extractKimiTitle(viewport: string): string {
  return extractHeaderPrefixedTitle(viewport, KIMI_PREFIXES, KIMI_MARKERS);
}

/**
 * Map of all 9 custom plugin title extractors (plus canonical manifest aliases).
 */
export const CUSTOM_TITLE_EXTRACTORS: Record<string, TitleExtractorFn> = {
  agy: extractAntigravityTitle,
  antigravity: extractAntigravityTitle,
  "antigravity-cli": extractAntigravityTitle,
  claude: extractClaudeTitle,
  codex: extractCodexTitle,
  cursor: extractCursorTitle,
  "cursor-agent": extractCursorTitle,
  deepseek: extractDeepSeekTitle,
  kilo: extractOpencodeTitle,
  kimi: extractKimiTitle,
  "kimi-cli": extractKimiTitle,
  muse: extractMuseTitle,
  opencode: extractOpencodeTitle,
};

/**
 * Parses a captured pane viewport for agent-specific header, tab-bar, status-line,
 * or prompt lines showing the conversation/session title, falling back to generic
 * prompt extraction.
 */
export function extractTitle(app: string, viewport: string): string {
  if (viewport.trim() === "") {
    return "";
  }
  const canonical = canonicalApp(app);
  const extractor =
    CUSTOM_TITLE_EXTRACTORS[canonical] ?? CUSTOM_TITLE_EXTRACTORS[normalizeApp(app)];
  if (extractor) {
    const t = extractor(viewport);
    if (t !== "") {
      return t;
    }
  }
  return extractGenericTitle(viewport);
}

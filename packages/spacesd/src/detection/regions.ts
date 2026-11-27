/**
 * Default region used when a manifest rule omits `region`.
 */
export const DEFAULT_REGION = "whole_recent";

/**
 * Minimum manifest `min_engine_version` required to use `top_non_empty_lines(N)`.
 */
export const TOP_NON_EMPTY_LINES_ENGINE_VERSION = 3;

const U16_MAX = 65535;
const CODEX_BLOCK_MARKER_PREFIXES = ["•", "■", "✗", "✓"] as const;

const STATIC_REGIONS = new Set([
  "whole_recent",
  "after_last_prompt_marker",
  "before_current_prompt_marker",
  "whole_recent_without_current_prompt_marker",
  "current_prompt_block_marker",
  "after_current_prompt_block_marker",
  "prompt_box_body",
  "above_prompt_box",
  "last_non_empty_above_prompt_box",
  "after_last_horizontal_rule",
  "osc_title",
  "osc_progress",
]);

/**
 * One captured screen line with offsets into the original viewport string.
 */
export interface TextLine {
  /** Line content without trailing `\n` or `\r`. */
  text: string;
  /** Start offset of the line inside the source string. */
  start: number;
  /** End offset just past the `\n`, or `content.length` for the final line. */
  end: number;
}

/**
 * Viewport and out-of-band OSC inputs supplied to region extraction.
 */
export interface DetectionInput {
  screen: string;
  oscTitle?: string;
  oscProgress?: string;
}

/**
 * Result of validating a rule's region specifier against manifest constraints.
 */
export type RegionValidationResult =
  | { ok: true; region: string }
  | { ok: false; reason: "invalid_region" | "engine_version"; error: string };

/**
 * Splits `content` into lines matching Rust `str::lines` / Go `splitTextLines`:
 * splits on `\n`, drops a single trailing empty segment, and strips trailing `\r`.
 */
export function splitTextLines(content: string): TextLine[] {
  const lines: TextLine[] = [];
  let start = 0;
  for (let i = 0; i < content.length; i += 1) {
    if (content[i] === "\n") {
      let text = content.slice(start, i);
      if (text.endsWith("\r")) {
        text = text.slice(0, -1);
      }
      lines.push({ text, start, end: i + 1 });
      start = i + 1;
    }
  }
  if (start < content.length) {
    let text = content.slice(start);
    if (text.endsWith("\r")) {
      text = text.slice(0, -1);
    }
    lines.push({ text, start, end: content.length });
  }
  return lines;
}

/**
 * Reports whether `line` is a box-drawing horizontal rule (`─` repeated to end,
 * or at least 3 leading `─` characters with any suffix).
 */
export function isHorizontalRule(line: string): boolean {
  const trimmed = line.trim();
  if (trimmed === "") {
    return false;
  }
  let count = 0;
  while (count < trimmed.length && trimmed[count] === "─") {
    count += 1;
  }
  if (count === 0) {
    return false;
  }
  const suffix = trimmed.slice(count).trimStart();
  return suffix === "" || count >= 3;
}

/**
 * Finds the index of the second horizontal rule scanning from the bottom, or `-1`
 * when fewer than two horizontal rules exist.
 */
export function promptBoxTopBorderIndex(lines: readonly TextLine[]): number {
  let borderCount = 0;
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    if (isHorizontalRule(lines[i]!.text)) {
      borderCount += 1;
      if (borderCount === 2) {
        return i;
      }
    }
  }
  return -1;
}

/**
 * Reports whether `line` is a Codex prompt marker line (`›` or `› ...`).
 */
export function codexPromptLine(line: string): boolean {
  return line === "›" || line.startsWith("› ");
}

/**
 * Reports whether `line` begins with a Codex block marker (`•`, `■`, `✗`, `✓`).
 */
export function codexBlockMarkerLine(line: string): boolean {
  for (const prefix of CODEX_BLOCK_MARKER_PREFIXES) {
    if (line.startsWith(prefix)) {
      return true;
    }
  }
  return false;
}

/**
 * Returns the index of the last Codex prompt line that has no block-marker line
 * after it, or `-1` when none exists.
 */
export function currentCodexPromptIndex(lines: readonly string[]): number {
  let prompt = -1;
  for (let i = 0; i < lines.length; i += 1) {
    if (codexPromptLine(lines[i]!)) {
      prompt = i;
    }
  }
  if (prompt < 0) {
    return -1;
  }
  for (let i = prompt + 1; i < lines.length; i += 1) {
    if (codexBlockMarkerLine(lines[i]!)) {
      return -1;
    }
  }
  return prompt;
}

/**
 * Parses `name(N)` counts for `bottom_lines` / `bottom_non_empty_lines`.
 * `N = 0` is valid.
 */
export function regionCount(spec: string, name: string): number | null {
  if (!spec.startsWith(name)) {
    return null;
  }
  let rest = spec.slice(name.length);
  if (!rest.startsWith("(") || !rest.endsWith(")")) {
    return null;
  }
  rest = rest.slice(1, -1);
  if (!/^\+?\d+$/.test(rest)) {
    return null;
  }
  const n = Number(rest);
  if (!Number.isSafeInteger(n) || n < 0) {
    return null;
  }
  return n;
}

/**
 * Parses `top_non_empty_lines(N)` with strict validation: no leading zeros,
 * ASCII digits only, and at most `65535` (`u16::MAX`).
 */
export function topRegionCount(spec: string): number | null {
  const prefix = "top_non_empty_lines";
  if (!spec.startsWith(prefix)) {
    return null;
  }
  let rest = spec.slice(prefix.length);
  if (!rest.startsWith("(") || !rest.endsWith(")")) {
    return null;
  }
  rest = rest.slice(1, -1);
  if (rest === "" || rest.startsWith("0")) {
    return null;
  }
  for (let i = 0; i < rest.length; i += 1) {
    const code = rest.charCodeAt(i);
    if (code < 48 || code > 57) {
      return null;
    }
  }
  const n = Number(rest);
  if (!Number.isSafeInteger(n) || n > U16_MAX) {
    return null;
  }
  return n;
}

/**
 * Reports whether `spec` names one of the 15 supported viewport/OSC regions.
 */
export function validRegion(spec: string): boolean {
  const trimmed = spec.trim();
  if (STATIC_REGIONS.has(trimmed)) {
    return true;
  }
  if (regionCount(trimmed, "bottom_lines") !== null) {
    return true;
  }
  if (regionCount(trimmed, "bottom_non_empty_lines") !== null) {
    return true;
  }
  return topRegionCount(trimmed) !== null;
}

/**
 * Validates and normalizes a rule `region` string against the manifest's
 * `min_engine_version` constraint.
 */
export function validateRegionSpec(
  spec: string,
  minEngineVersion?: number | null,
): RegionValidationResult {
  const trimmed = spec.trim();
  const region = trimmed === "" ? DEFAULT_REGION : trimmed;
  if (!validRegion(region)) {
    return {
      ok: false,
      reason: "invalid_region",
      error: `invalid region ${spec}`,
    };
  }
  if (
    region.startsWith("top_non_empty_lines(") &&
    minEngineVersion !== undefined &&
    minEngineVersion !== null &&
    minEngineVersion < TOP_NON_EMPTY_LINES_ENGINE_VERSION
  ) {
    return {
      ok: false,
      reason: "engine_version",
      error: `top_non_empty_lines below engine ${TOP_NON_EMPTY_LINES_ENGINE_VERSION}`,
    };
  }
  return { ok: true, region };
}

export function bottomLines(content: string, count: number): string {
  const lines = splitTextLines(content);
  const start = Math.max(0, lines.length - count);
  if (start >= lines.length) {
    return "";
  }
  return content.slice(lines[start]!.start);
}

export function bottomNonEmptyLines(content: string, count: number): string {
  const lines = splitTextLines(content);
  let start = -1;
  let seen = 0;
  for (let i = lines.length - 1; i >= 0 && seen < count; i -= 1) {
    if (lines[i]!.text.trim() !== "") {
      seen += 1;
      start = i;
    }
  }
  if (start < 0) {
    return "";
  }
  return content.slice(lines[start]!.start);
}

export function topNonEmptyLines(content: string, count: number): string {
  const lines = splitTextLines(content);
  let end = -1;
  let seen = 0;
  for (const line of lines) {
    if (seen >= count) {
      break;
    }
    if (line.text.trim() !== "") {
      seen += 1;
      end = line.end;
    }
  }
  if (end < 0) {
    return "";
  }
  return content.slice(0, end);
}

export function afterLastPromptMarker(content: string): string {
  const lines = splitTextLines(content);
  let index = -1;
  for (let i = 0; i < lines.length; i += 1) {
    if (codexPromptLine(lines[i]!.text)) {
      index = i;
    }
  }
  if (index < 0) {
    return content;
  }
  if (index + 1 >= lines.length) {
    return "";
  }
  return content.slice(lines[index + 1]!.start);
}

export function beforeCurrentPromptMarker(content: string): string {
  const lines = splitTextLines(content);
  const index = currentCodexPromptIndex(lines.map((line) => line.text));
  if (index < 0) {
    return content;
  }
  const end = Math.min(lines[index]!.start, content.length);
  return content.slice(0, end);
}

export function wholeRecentWithoutCurrentPromptMarker(content: string): string {
  const lines = splitTextLines(content);
  if (currentCodexPromptIndex(lines.map((line) => line.text)) >= 0) {
    return "";
  }
  return content;
}

function findCurrentPromptBlockLineIndex(lines: readonly TextLine[]): number {
  const prompt = currentCodexPromptIndex(lines.map((line) => line.text));
  if (prompt < 0) {
    return -1;
  }
  for (let i = prompt - 1; i >= 0; i -= 1) {
    if (codexBlockMarkerLine(lines[i]!.text)) {
      return i;
    }
  }
  return -1;
}

export function currentPromptBlockMarker(content: string): string | null {
  const lines = splitTextLines(content);
  const block = findCurrentPromptBlockLineIndex(lines);
  if (block < 0) {
    return null;
  }
  return lines[block]!.text;
}

export function afterCurrentPromptBlockMarker(content: string): string | null {
  const lines = splitTextLines(content);
  const block = findCurrentPromptBlockLineIndex(lines);
  if (block < 0) {
    return null;
  }
  return content.slice(lines[block]!.start);
}

export function promptBoxBody(content: string): string | null {
  const lines = splitTextLines(content);
  const top = promptBoxTopBorderIndex(lines);
  if (top < 0) {
    return null;
  }
  let start = top + 1 < lines.length ? lines[top + 1]!.start : content.length;
  let end = content.length;
  for (let i = top + 1; i < lines.length; i += 1) {
    if (isHorizontalRule(lines[i]!.text)) {
      end = lines[i]!.start;
      break;
    }
  }
  if (start > end) {
    start = end;
  }
  return content.slice(start, end);
}

export function abovePromptBox(content: string): string {
  const lines = splitTextLines(content);
  const top = promptBoxTopBorderIndex(lines);
  if (top < 0) {
    return content;
  }
  const end = Math.min(lines[top]!.start, content.length);
  return content.slice(0, end);
}

export function afterLastHorizontalRule(content: string): string {
  let lastEnd = 0;
  for (const line of splitTextLines(content)) {
    if (isHorizontalRule(line.text)) {
      lastEnd = line.end;
    }
  }
  return content.slice(Math.min(lastEnd, content.length));
}

export function lastNonEmptyLine(content: string): string {
  const lines = splitTextLines(content);
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    if (lines[i]!.text.trim() !== "") {
      return lines[i]!.text;
    }
  }
  return "";
}

/**
 * Extracts the viewport or OSC section named by `spec`.
 */
export function extractRegion(spec: string, input: DetectionInput): string {
  const trimmed = spec.trim();
  if (trimmed === "osc_title") {
    return input.oscTitle ?? "";
  }
  if (trimmed === "osc_progress") {
    return input.oscProgress ?? "";
  }

  const content = input.screen;
  switch (trimmed) {
    case "whole_recent":
      return content;
    case "after_last_prompt_marker":
      return afterLastPromptMarker(content);
    case "before_current_prompt_marker":
      return beforeCurrentPromptMarker(content);
    case "whole_recent_without_current_prompt_marker":
      return wholeRecentWithoutCurrentPromptMarker(content);
    case "current_prompt_block_marker":
      return currentPromptBlockMarker(content) ?? "";
    case "after_current_prompt_block_marker":
      return afterCurrentPromptBlockMarker(content) ?? "";
    case "prompt_box_body":
      return promptBoxBody(content) ?? "";
    case "above_prompt_box":
      return abovePromptBox(content);
    case "last_non_empty_above_prompt_box":
      return lastNonEmptyLine(abovePromptBox(content));
    case "after_last_horizontal_rule":
      return afterLastHorizontalRule(content);
  }

  const bottomCount = regionCount(trimmed, "bottom_lines");
  if (bottomCount !== null) {
    return bottomLines(content, bottomCount);
  }
  const bottomNonEmptyCount = regionCount(trimmed, "bottom_non_empty_lines");
  if (bottomNonEmptyCount !== null) {
    return bottomNonEmptyLines(content, bottomNonEmptyCount);
  }
  const topCount = topRegionCount(trimmed);
  if (topCount !== null) {
    return topNonEmptyLines(content, topCount);
  }
  return "";
}

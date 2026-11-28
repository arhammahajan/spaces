/**
 * Result of translating a Rust/RE2 manifest regex pattern into a JavaScript
 * `RegExp` source string and flag set.
 */
export interface TranslatedPattern {
  pattern: string;
  flags: string;
}

const LEADING_INLINE_FLAGS_RE = /^\(\?([ims]+)\)/;
const HEX_DIGITS_RE = /^[0-9a-fA-F]+$/;
const FLAG_ORDER = ["i", "m", "s"] as const;

/**
 * Translates a Rust/RE2 manifest regex pattern into a JavaScript `RegExp`
 * pattern and flags with Unicode (`u`) mode enabled.
 *
 * - Extracts leading inline flags (`(?i)`, `(?m)`, `(?s)`, and combinations)
 *   into JS flags.
 * - Translates `\A` to `(?<![\s\S])` and `\z` to `(?![\s\S])` so absolute
 *   start/end anchors remain strict even when `m` is active.
 * - Translates `\x{HHHH}` and `\UHHHHHHHH` into `\u{...}` for JS `/u` mode.
 * - Preserves escaped backslashes (`\\A`, `\\z`, `\\x{...}`) verbatim.
 */
export function translatePattern(raw: string): TranslatedPattern {
  const flagSet = new Set<string>();
  let rest = raw;

  while (true) {
    const match = LEADING_INLINE_FLAGS_RE.exec(rest);
    if (!match) {
      break;
    }
    for (const flagChar of match[1]!) {
      flagSet.add(flagChar);
    }
    rest = rest.slice(match[0].length);
  }

  let flags = "";
  for (const flag of FLAG_ORDER) {
    if (flagSet.has(flag)) {
      flags += flag;
    }
  }
  flags += "u";

  let out = "";
  let i = 0;
  while (i < rest.length) {
    if (rest[i] === "\\" && i + 1 < rest.length) {
      const next = rest[i + 1]!;
      if (next === "\\") {
        out += "\\\\";
        i += 2;
        continue;
      }
      if (next === "A") {
        out += "(?<![\\s\\S])";
        i += 2;
        continue;
      }
      if (next === "z") {
        out += "(?![\\s\\S])";
        i += 2;
        continue;
      }
      if (next === "x" && rest[i + 2] === "{") {
        const closeIdx = rest.indexOf("}", i + 3);
        if (closeIdx > i + 3) {
          const digits = rest.slice(i + 3, closeIdx);
          if (digits.length >= 1 && digits.length <= 6 && HEX_DIGITS_RE.test(digits)) {
            out += `\\u{${digits}}`;
            i = closeIdx + 1;
            continue;
          }
        }
      }
      if (next === "U" && i + 10 <= rest.length) {
        const digits = rest.slice(i + 2, i + 10);
        if (HEX_DIGITS_RE.test(digits)) {
          out += `\\u{${digits}}`;
          i += 10;
          continue;
        }
      }
      out += `\\${next}`;
      i += 2;
      continue;
    }

    out += rest[i]!;
    i += 1;
  }

  return { pattern: out, flags };
}

/**
 * Translates and compiles a Rust/RE2 manifest regex pattern into a JS `RegExp`.
 */
export function compilePattern(raw: string): RegExp {
  const { pattern, flags } = translatePattern(raw);
  return new RegExp(pattern, flags);
}

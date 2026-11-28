import type {
  DiffFile,
  DiffFileStatus,
  DiffHunk,
  DiffLine,
  DiffSnapshot,
  GitDiffScope,
} from "@spaces/types";

const DIFF_GIT_PREFIX = "diff --git ";
const DIFF_CC_PREFIX = "diff --cc ";
const DIFF_COMBINED_PREFIX = "diff --combined ";
const QUOTE = '"';

const CHARACTER_BY_ESCAPE: Record<string, number> = {
  '"': 0x22,
  "\\": 0x5c,
  a: 0x07,
  b: 0x08,
  f: 0x0c,
  n: 0x0a,
  r: 0x0d,
  t: 0x09,
  v: 0x0b,
};

const utf8Encoder = new TextEncoder();
const utf8Decoder = new TextDecoder("utf-8", { ignoreBOM: true });

const HUNK_HEADER_RE =
  /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:[ \t]?(.*))?$/;
const COMBINED_HUNK_HEADER_RE =
  /^@@@ -(\d+)(?:,(\d+))? -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@@(?:[ \t]?(.*))?$/;

const GENERATED_FILE_NAMES = new Set([
  "pnpm-lock.yaml",
  "yarn.lock",
  "package-lock.json",
  "bun.lock",
  "bun.lockb",
  "Cargo.lock",
  "go.sum",
  "composer.lock",
  "Gemfile.lock",
]);

const GENERATED_DIRECTORIES = new Set([
  "__snapshots__",
  "__generated__",
  "dist",
  "build",
  "vendor",
]);

const TEST_DIRECTORIES = new Set(["__tests__", "tests", "test"]);

type DiffFileTier = "source" | "test" | "generated";

export interface ParseUnifiedDiffOptions {
  scope: GitDiffScope;
  baseRef?: string;
  headRef?: string;
  maxBytes?: number;
}

function unescapeGitPathBody(body: string): string {
  if (!body.includes("\\")) {
    return body;
  }
  const bytes: number[] = [];
  let literal = "";
  const flush = () => {
    if (literal.length === 0) {
      return;
    }
    bytes.push(...utf8Encoder.encode(literal));
    literal = "";
  };

  let at = 0;
  while (at < body.length) {
    const ch = body.charAt(at);
    if (ch !== "\\") {
      literal += ch;
      at += 1;
      continue;
    }
    const escaped = body.charAt(at + 1);
    if (escaped === "") {
      flush();
      bytes.push(0x5c);
      break;
    }
    const named = CHARACTER_BY_ESCAPE[escaped];
    if (named !== undefined) {
      flush();
      bytes.push(named);
      at += 2;
      continue;
    }
    const octal = body.slice(at + 1, at + 4);
    if (/^[0-7]{3}$/.test(octal)) {
      flush();
      bytes.push(Number.parseInt(octal, 8));
      at += 4;
      continue;
    }
    literal += escaped;
    at += 2;
  }
  flush();
  return utf8Decoder.decode(new Uint8Array(bytes));
}

export function unquoteGitPatchPath(token: string): string {
  if (token.length >= 2 && token.startsWith(QUOTE) && token.endsWith(QUOTE)) {
    return unescapeGitPathBody(token.slice(1, -1));
  }
  return unescapeGitPathBody(token);
}

function quotedEnd(rest: string): number {
  for (let at = 1; at < rest.length; at += 1) {
    const ch = rest.charAt(at);
    if (ch === "\\") {
      at += 1;
      continue;
    }
    if (ch === QUOTE) {
      return at;
    }
  }
  return -1;
}

function stripSidePrefix(token: string, prefix: string): string | null {
  const unquoted = unquoteGitPatchPath(token);
  return unquoted.startsWith(prefix) ? unquoted.slice(prefix.length) : null;
}

function parseSidePath(rest: string, prefix: string): string | null {
  const tab = rest.indexOf("\t");
  const token = tab === -1 ? rest : rest.slice(0, tab);
  if (token === "/dev/null") {
    return null;
  }
  const unquoted = unquoteGitPatchPath(token);
  return unquoted.startsWith(prefix) ? unquoted.slice(prefix.length) : unquoted;
}

function parseGitHeaderPaths(
  rest: string,
): readonly [string | null, string | null] {
  if (rest.startsWith(QUOTE)) {
    const end = quotedEnd(rest);
    if (end === -1 || rest.charAt(end + 1) !== " ") {
      return [null, null];
    }
    return [
      stripSidePrefix(rest.slice(0, end + 1), "a/"),
      stripSidePrefix(rest.slice(end + 2), "b/"),
    ];
  }
  if (rest.endsWith(QUOTE)) {
    const opens = rest.indexOf(QUOTE);
    if (opens < 1 || rest.charAt(opens - 1) !== " ") {
      return [null, null];
    }
    return [
      stripSidePrefix(rest.slice(0, opens - 1), "a/"),
      stripSidePrefix(rest.slice(opens), "b/"),
    ];
  }
  if (!rest.startsWith("a/")) {
    return [null, null];
  }
  const splits: number[] = [];
  for (
    let at = rest.indexOf(" b/");
    at !== -1;
    at = rest.indexOf(" b/", at + 1)
  ) {
    splits.push(at);
  }
  const chosen =
    splits.find((at) => rest.slice(2, at) === rest.slice(at + 3)) ??
    (splits.length === 1 ? splits[0] : undefined);
  return chosen === undefined
    ? [null, null]
    : [rest.slice(2, chosen), rest.slice(chosen + 3)];
}

function diffFileTier(filePath: string): DiffFileTier {
  const segments = filePath.split("/");
  const name = segments[segments.length - 1] ?? "";
  if (
    GENERATED_FILE_NAMES.has(name) ||
    name.endsWith(".snap") ||
    name.endsWith(".min.js") ||
    name.endsWith(".min.css") ||
    /\.generated\./.test(name) ||
    segments.slice(0, -1).some((seg) => GENERATED_DIRECTORIES.has(seg))
  ) {
    return "generated";
  }
  if (
    /\.(?:test|spec)\./.test(name) ||
    segments.slice(0, -1).some((seg) => TEST_DIRECTORIES.has(seg))
  ) {
    return "test";
  }
  return "source";
}

function tierRank(tier: DiffFileTier): number {
  switch (tier) {
    case "source":
      return 0;
    case "test":
      return 1;
    case "generated":
      return 2;
  }
}

/**
 * Deterministic review ordering inspired by t3code's diffFileOrder:
 * source files first, then tests, then generated/lock files; within each tier
 * sorted alphabetically by path, then regrouped so sibling files in the same
 * directory remain contiguous.
 */
export function orderDiffFiles(files: readonly DiffFile[]): DiffFile[] {
  if (files.length <= 1) {
    return [...files];
  }

  const tierSorted = [...files].sort((a, b) => {
    const rankDiff = tierRank(diffFileTier(a.path)) - tierRank(diffFileTier(b.path));
    if (rankDiff !== 0) {
      return rankDiff;
    }
    const pathDiff = a.path.localeCompare(b.path);
    if (pathDiff !== 0) {
      return pathDiff;
    }
    return (a.old_path ?? "").localeCompare(b.old_path ?? "");
  });

  // Compute tree-first-appearance positions so directories stay contiguous.
  const positions = new Map<string, number>();
  tierSorted.forEach((file, index) => {
    if (!positions.has(file.path)) {
      positions.set(file.path, index);
    }
    let dir = "";
    const parts = file.path.split("/");
    for (let i = 0; i < parts.length - 1; i += 1) {
      dir += `${parts[i]}/`;
      if (!positions.has(dir)) {
        positions.set(dir, index);
      }
    }
  });

  const keyed = tierSorted.map((file, index) => {
    const key: number[] = [];
    let dir = "";
    const parts = file.path.split("/");
    for (let i = 0; i < parts.length - 1; i += 1) {
      dir += `${parts[i]}/`;
      key.push(positions.get(dir)!);
    }
    key.push(positions.get(file.path)!);
    key.push(index);
    return { file, key };
  });

  keyed.sort((left, right) => {
    const len = Math.min(left.key.length, right.key.length);
    for (let i = 0; i < len; i += 1) {
      const diff = left.key[i]! - right.key[i]!;
      if (diff !== 0) {
        return diff;
      }
    }
    return left.key.length - right.key.length;
  });

  return keyed.map((entry) => entry.file);
}

interface WorkingFile {
  oldPath: string | null;
  newPath: string | null;
  status: DiffFileStatus;
  explicitStatus: boolean;
  isBinary: boolean;
  isCombined: boolean;
  additions: number;
  deletions: number;
  hunks: DiffHunk[];
  truncated: boolean;
  inBody: boolean;
}

function finalizeFile(wf: WorkingFile): DiffFile | null {
  const targetPath =
    wf.status === "deleted"
      ? (wf.oldPath ?? wf.newPath)
      : (wf.newPath ?? wf.oldPath);
  if (!targetPath) {
    return null;
  }

  const file: DiffFile = {
    path: targetPath,
    status: wf.status,
    is_binary: wf.isBinary,
    additions: wf.additions,
    deletions: wf.deletions,
    hunks: wf.hunks,
    truncated: wf.truncated,
  };
  if (
    wf.oldPath &&
    wf.oldPath !== targetPath &&
    (wf.status === "renamed" || wf.status === "copied")
  ) {
    file.old_path = wf.oldPath;
  }
  return file;
}

function isFileHeaderStart(line: string): boolean {
  return (
    line.startsWith(DIFF_GIT_PREFIX) ||
    line.startsWith(DIFF_CC_PREFIX) ||
    line.startsWith(DIFF_COMBINED_PREFIX)
  );
}

/**
 * Parses unified git diff output into a structured `DiffSnapshot`.
 */
export function parseUnifiedDiff(
  raw: string,
  opts: ParseUnifiedDiffOptions,
): DiffSnapshot {
  const maxBytes =
    opts.maxBytes !== undefined && opts.maxBytes > 0
      ? opts.maxBytes
      : undefined;

  let truncated = false;
  let source = raw.replace(/\r\n/g, "\n");

  if (maxBytes !== undefined) {
    const rawBytes = utf8Encoder.encode(source);
    if (rawBytes.byteLength > maxBytes) {
      truncated = true;
      // Decode prefix safely up to maxBytes and trim trailing partial line.
      const sliced = utf8Decoder.decode(rawBytes.subarray(0, maxBytes));
      const lastNewline = sliced.lastIndexOf("\n");
      source = lastNewline >= 0 ? sliced.slice(0, lastNewline) : sliced;
    }
  }

  const lines = source.split("\n");
  const parsedFiles: DiffFile[] = [];

  let currentFile: WorkingFile | null = null;
  let currentHunk: DiffHunk | null = null;
  let oldCursor = 0;
  let newCursor = 0;

  const flushFile = () => {
    if (!currentFile) {
      return;
    }
    if (truncated && currentFile.inBody) {
      // Mark the last file if raw input was truncated mid-stream.
      currentFile.truncated = true;
    }
    const finalized = finalizeFile(currentFile);
    if (finalized) {
      parsedFiles.push(finalized);
    }
    currentFile = null;
    currentHunk = null;
  };

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i]!;

    if (isFileHeaderStart(line)) {
      if (currentFile) {
        const finalized = finalizeFile(currentFile);
        if (finalized) {
          parsedFiles.push(finalized);
        }
        currentFile = null;
        currentHunk = null;
      }

      if (line.startsWith(DIFF_GIT_PREFIX)) {
        const [oldPath, newPath] = parseGitHeaderPaths(
          line.slice(DIFF_GIT_PREFIX.length),
        );
        currentFile = {
          oldPath,
          newPath,
          status: "modified",
          explicitStatus: false,
          isBinary: false,
          isCombined: false,
          additions: 0,
          deletions: 0,
          hunks: [],
          truncated: false,
          inBody: false,
        };
      } else {
        const prefixLen = line.startsWith(DIFF_CC_PREFIX)
          ? DIFF_CC_PREFIX.length
          : DIFF_COMBINED_PREFIX.length;
        const p = unquoteGitPatchPath(line.slice(prefixLen).trim());
        currentFile = {
          oldPath: p || null,
          newPath: p || null,
          status: "conflicted",
          explicitStatus: true,
          isBinary: false,
          isCombined: true,
          additions: 0,
          deletions: 0,
          hunks: [],
          truncated: false,
          inBody: false,
        };
      }
      continue;
    }

    if (!currentFile) {
      continue;
    }

    // Header metadata lines before the first hunk.
    if (!currentFile.inBody) {
      if (line.startsWith("@@")) {
        currentFile.inBody = true;
        // Fall through to hunk header parsing below.
      } else {
        if (line.startsWith("new file mode ")) {
          currentFile.status = "added";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("deleted file mode ")) {
          currentFile.status = "deleted";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("rename from ")) {
          currentFile.oldPath = unquoteGitPatchPath(
            line.slice("rename from ".length),
          );
          currentFile.status = "renamed";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("rename to ")) {
          currentFile.newPath = unquoteGitPatchPath(
            line.slice("rename to ".length),
          );
          currentFile.status = "renamed";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("copy from ")) {
          currentFile.oldPath = unquoteGitPatchPath(
            line.slice("copy from ".length),
          );
          currentFile.status = "copied";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("copy to ")) {
          currentFile.newPath = unquoteGitPatchPath(
            line.slice("copy to ".length),
          );
          currentFile.status = "copied";
          currentFile.explicitStatus = true;
        } else if (line.startsWith("--- ")) {
          const oldSide = parseSidePath(line.slice(4), "a/");
          currentFile.oldPath = oldSide;
          if (oldSide === null && !currentFile.explicitStatus) {
            currentFile.status = "added";
            currentFile.explicitStatus = true;
          }
        } else if (line.startsWith("+++ ")) {
          const newSide = parseSidePath(line.slice(4), "b/");
          currentFile.newPath = newSide;
          if (newSide === null && !currentFile.explicitStatus) {
            currentFile.status = "deleted";
            currentFile.explicitStatus = true;
          }
        } else if (
          (line.startsWith("Binary files ") && line.endsWith(" differ")) ||
          line === "GIT binary patch"
        ) {
          currentFile.isBinary = true;
        }
        continue;
      }
    }

    // Hunk headers.
    if (line.startsWith("@@")) {
      const m = HUNK_HEADER_RE.exec(line);
      if (m) {
        const oldStart = Number.parseInt(m[1]!, 10);
        const oldLines = m[2] !== undefined ? Number.parseInt(m[2], 10) : 1;
        const newStart = Number.parseInt(m[3]!, 10);
        const newLines = m[4] !== undefined ? Number.parseInt(m[4], 10) : 1;
        const heading = (m[5] ?? "").trim();

        const hunk: DiffHunk = {
          header: line,
          old_start: oldStart,
          old_lines: oldLines,
          new_start: newStart,
          new_lines: newLines,
          lines: [],
        };
        if (heading) {
          hunk.section_heading = heading;
        }
        currentFile.hunks.push(hunk);
        currentHunk = hunk;
        oldCursor = oldStart;
        newCursor = newStart;
        continue;
      }

      const cm = COMBINED_HUNK_HEADER_RE.exec(line);
      if (cm) {
        const oldStart = Number.parseInt(cm[1]!, 10);
        const oldLines = cm[2] !== undefined ? Number.parseInt(cm[2], 10) : 1;
        const newStart = Number.parseInt(cm[5]!, 10);
        const newLines = cm[6] !== undefined ? Number.parseInt(cm[6], 10) : 1;
        const heading = (cm[7] ?? "").trim();

        const hunk: DiffHunk = {
          header: line,
          old_start: oldStart,
          old_lines: oldLines,
          new_start: newStart,
          new_lines: newLines,
          lines: [],
        };
        if (heading) {
          hunk.section_heading = heading;
        }
        currentFile.hunks.push(hunk);
        currentHunk = hunk;
        oldCursor = oldStart;
        newCursor = newStart;
        continue;
      }
    }

    if (!currentHunk) {
      continue;
    }

    if (line === "\\ No newline at end of file") {
      const prev = currentHunk.lines[currentHunk.lines.length - 1];
      if (prev) {
        prev.no_newline_at_eof = true;
      }
      continue;
    }

    // Combined diff lines have a 2-character prefix ("  ", "+ ", " +", "- ", " -", etc.).
    if (currentFile.isCombined) {
      if (line.length < 2) {
        continue;
      }
      const p0 = line[0]!;
      const p1 = line[1]!;
      const content = line.slice(2);
      if (p0 === "+" || p1 === "+") {
        const diffLine: DiffLine = {
          kind: "add",
          content,
          new_lineno: newCursor,
        };
        newCursor += 1;
        currentFile.additions += 1;
        currentHunk.lines.push(diffLine);
      } else if (p0 === "-" || p1 === "-") {
        const diffLine: DiffLine = {
          kind: "del",
          content,
          old_lineno: oldCursor,
        };
        oldCursor += 1;
        currentFile.deletions += 1;
        currentHunk.lines.push(diffLine);
      } else {
        const diffLine: DiffLine = {
          kind: "context",
          content,
          old_lineno: oldCursor,
          new_lineno: newCursor,
        };
        oldCursor += 1;
        newCursor += 1;
        currentHunk.lines.push(diffLine);
      }
      continue;
    }

    const prefix = line[0];
    if (prefix === "+") {
      const diffLine: DiffLine = {
        kind: "add",
        content: line.slice(1),
        new_lineno: newCursor,
      };
      newCursor += 1;
      currentFile.additions += 1;
      currentHunk.lines.push(diffLine);
    } else if (prefix === "-") {
      const diffLine: DiffLine = {
        kind: "del",
        content: line.slice(1),
        old_lineno: oldCursor,
      };
      oldCursor += 1;
      currentFile.deletions += 1;
      currentHunk.lines.push(diffLine);
    } else if (prefix === " ") {
      const diffLine: DiffLine = {
        kind: "context",
        content: line.slice(1),
        old_lineno: oldCursor,
        new_lineno: newCursor,
      };
      oldCursor += 1;
      newCursor += 1;
      currentHunk.lines.push(diffLine);
    } else if (line === "" && i < lines.length - 1) {
      // Bare empty line inside a hunk (some editors strip trailing space on blank context lines).
      const remainingOld =
        currentHunk.old_start + currentHunk.old_lines - oldCursor;
      const remainingNew =
        currentHunk.new_start + currentHunk.new_lines - newCursor;
      if (remainingOld > 0 && remainingNew > 0) {
        currentHunk.lines.push({
          kind: "context",
          content: "",
          old_lineno: oldCursor,
          new_lineno: newCursor,
        });
        oldCursor += 1;
        newCursor += 1;
      }
    }
  }

  flushFile();

  const orderedFiles = orderDiffFiles(parsedFiles);
  let totalAdditions = 0;
  let totalDeletions = 0;
  for (const f of orderedFiles) {
    totalAdditions += f.additions;
    totalDeletions += f.deletions;
  }

  const snapshot: DiffSnapshot = {
    scope: opts.scope,
    total_additions: totalAdditions,
    total_deletions: totalDeletions,
    files: orderedFiles,
    truncated,
  };
  if (opts.baseRef) {
    snapshot.base_ref = opts.baseRef;
  }
  if (opts.headRef) {
    snapshot.head_ref = opts.headRef;
  }
  return snapshot;
}

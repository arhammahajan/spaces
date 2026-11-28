import {
  closeSync,
  existsSync,
  lstatSync,
  openSync,
  readSync,
  realpathSync,
  statSync,
} from "node:fs";
import path from "node:path";
import type {
  GitDiffScope,
  GitInfo,
  RepoInfo,
} from "@spaces/types";

export const DEFAULT_GIT_TIMEOUT_MS = 3_000;
export const MAX_UNTRACKED_FILE_SIZE = 2 * 1024 * 1024; // 2 MiB

const textDecoder = new TextDecoder("utf-8");
const textEncoder = new TextEncoder();

export interface GitCommandOptions {
  cwd?: string;
  stdin?: Uint8Array | string;
  timeoutMs?: number;
  signal?: AbortSignal;
}

export interface GitCommandResult {
  stdout: Uint8Array;
  stderr: Uint8Array;
  exitCode: number;
  timedOut?: boolean;
  error?: string;
}

export type GitCommandRunner = (
  args: string[],
  opts?: GitCommandOptions,
) => Promise<GitCommandResult>;

export interface GitDiffRawOptions {
  scope: GitDiffScope;
  baseRef?: string;
  headRef?: string;
  paths?: string[];
  contextLines?: number;
}

export interface GitDiffRawResult {
  raw: string;
  baseRef?: string;
  headRef?: string;
}

export interface LocalGitDriver {
  execGit(
    cwd: string,
    args: string[],
    opts?: Omit<GitCommandOptions, "cwd">,
  ): Promise<GitCommandResult>;
  discoverRepo(dir: string): Promise<RepoInfo | null>;
  getStatus(cwd: string): Promise<GitInfo | null>;
  getRemoteUrl(cwd: string, remote?: string): Promise<string | null>;
  listBranches(cwd: string): Promise<string[]>;
  getDiffRaw(cwd: string, opts: GitDiffRawOptions): Promise<GitDiffRawResult>;
}

export interface PorcelainObservation {
  branch: string;
  ahead: number;
  behind: number;
  dirty: boolean;
  upstream: boolean;
  filesAdded: number;
  filesModified: number;
}

export function decodeUtf8(bytes: Uint8Array): string {
  return textDecoder.decode(bytes);
}

export function encodeUtf8(str: string): Uint8Array {
  return textEncoder.encode(str);
}

/**
 * Resolves symlinks when possible (including when only ancestor directories
 * exist) and returns a cleaned absolute path.
 */
export function canonicalPath(p: string): string {
  const trimmed = p.trim();
  if (!trimmed) {
    return "";
  }
  const abs = path.resolve(trimmed);
  try {
    return path.normalize(realpathSync(abs));
  } catch {
    // Walk ancestors when the leaf does not exist yet.
  }

  let cur = abs;
  const tail: string[] = [];
  while (true) {
    const parent = path.dirname(cur);
    if (parent === cur) {
      break;
    }
    tail.push(path.basename(cur));
    cur = parent;
    try {
      const resolved = realpathSync(cur);
      return path.normalize(path.join(resolved, ...tail.reverse()));
    } catch {
      // Keep walking up.
    }
  }
  return path.normalize(abs);
}

export function resolveAbsPath(base: string, p: string): string {
  const trimmed = p.trim();
  if (!trimmed) {
    return "";
  }
  if (path.isAbsolute(trimmed)) {
    return path.normalize(trimmed);
  }
  return path.resolve(base, trimmed);
}

export function splitNonEmptyLines(raw: string): string[] {
  const out: string[] = [];
  for (const line of raw.replace(/\r\n/g, "\n").split("\n")) {
    const trimmed = line.trim();
    if (trimmed) {
      out.push(trimmed);
    }
  }
  return out;
}

function isDirSync(p: string): boolean {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

export function isRebasingDir(gitDir: string): boolean {
  if (!gitDir) {
    return false;
  }
  return (
    isDirSync(path.join(gitDir, "rebase-merge")) ||
    isDirSync(path.join(gitDir, "rebase-apply"))
  );
}

/**
 * Parses `git status --porcelain=v2 --branch --untracked-files=all -z` output.
 */
export function parsePorcelainV2(data: Uint8Array): PorcelainObservation {
  const obs: PorcelainObservation = {
    branch: "",
    ahead: 0,
    behind: 0,
    dirty: false,
    upstream: false,
    filesAdded: 0,
    filesModified: 0,
  };

  // Split raw bytes on NUL (0x00) to avoid corrupting non-UTF8 paths.
  const records: Uint8Array[] = [];
  let start = 0;
  for (let i = 0; i < data.length; i += 1) {
    if (data[i] === 0) {
      records.push(data.subarray(start, i));
      start = i + 1;
    }
  }
  if (start < data.length) {
    records.push(data.subarray(start));
  }

  for (let i = 0; i < records.length; i += 1) {
    const rec = records[i]!;
    if (rec.length === 0) {
      continue;
    }
    // Header line: "# ..."
    if (rec.length >= 2 && rec[0] === 0x23 && rec[1] === 0x20) {
      const header = decodeUtf8(rec.subarray(2));
      if (header.startsWith("branch.head ")) {
        const name = header.slice("branch.head ".length).trim();
        if (name && name !== "(detached)") {
          obs.branch = name;
        }
      } else if (header.startsWith("branch.upstream ")) {
        obs.upstream = true;
      } else if (header.startsWith("branch.ab ")) {
        const fields = header
          .slice("branch.ab ".length)
          .trim()
          .split(/\s+/);
        if (fields.length >= 2) {
          const a = Number.parseInt(fields[0]!.replace(/^\+/, ""), 10);
          const b = Number.parseInt(fields[1]!.replace(/^-/, ""), 10);
          if (Number.isFinite(a) && a > 0) {
            obs.ahead = a;
          }
          if (Number.isFinite(b) && b > 0) {
            obs.behind = b;
          }
        }
      }
      continue;
    }

    const tag = String.fromCharCode(rec[0]!);
    switch (tag) {
      case "?":
        obs.dirty = true;
        obs.filesAdded += 1;
        break;
      case "1": {
        obs.dirty = true;
        // Format: "1 XY ..."
        const x = rec.length > 2 ? String.fromCharCode(rec[2]!) : ".";
        const y = rec.length > 3 ? String.fromCharCode(rec[3]!) : ".";
        if (x === "A" || y === "A") {
          obs.filesAdded += 1;
        } else {
          obs.filesModified += 1;
        }
        break;
      }
      case "u":
        obs.dirty = true;
        obs.filesModified += 1;
        break;
      case "2":
        // Renamed/copied entries are followed by a second NUL-terminated original path record.
        obs.dirty = true;
        obs.filesModified += 1;
        i += 1;
        break;
      default:
        break;
    }
  }

  return obs;
}

/**
 * Sums added/removed lines from `git diff --numstat`.
 * Binary files report "-\t-\t<path>" and are skipped.
 */
export function parseNumstat(raw: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of raw.split("\n")) {
    if (!line) {
      continue;
    }
    const tab1 = line.indexOf("\t");
    if (tab1 < 0) {
      continue;
    }
    const tab2 = line.indexOf("\t", tab1 + 1);
    if (tab2 < 0) {
      continue;
    }
    const a = Number.parseInt(line.slice(0, tab1), 10);
    const r = Number.parseInt(line.slice(tab1 + 1, tab2), 10);
    if (Number.isFinite(a) && a > 0) {
      added += a;
    }
    if (Number.isFinite(r) && r > 0) {
      removed += r;
    }
  }
  return { added, removed };
}

/**
 * Counts lines in a text file like git, skipping binary files that contain
 * a NUL byte in the first 8 KiB.
 */
export function countTextLines(filePath: string): number {
  let fd: number | null = null;
  try {
    fd = openSync(filePath, "r");
    const probe = Buffer.allocUnsafe(8192);
    const n = readSync(fd, probe, 0, probe.length, null);
    if (n === 0) {
      return 0;
    }
    if (probe.subarray(0, n).includes(0)) {
      return 0; // binary
    }

    let count = 0;
    for (let i = 0; i < n; i += 1) {
      if (probe[i] === 0x0a) {
        count += 1;
      }
    }
    let last = probe[n - 1]!;

    const buf = Buffer.allocUnsafe(32 * 1024);
    while (true) {
      const m = readSync(fd, buf, 0, buf.length, null);
      if (m === 0) {
        break;
      }
      for (let i = 0; i < m; i += 1) {
        if (buf[i] === 0x0a) {
          count += 1;
        }
      }
      last = buf[m - 1]!;
    }
    if (last !== 0x0a) {
      count += 1;
    }
    return count;
  } catch {
    return 0;
  } finally {
    if (fd !== null) {
      try {
        closeSync(fd);
      } catch {
        // ignore
      }
    }
  }
}

async function readStreamBytes(
  stream: ReadableStream<Uint8Array> | number | null | undefined,
): Promise<Uint8Array> {
  if (!stream || typeof stream === "number") {
    return new Uint8Array(0);
  }
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}

/**
 * Default git command runner backed by `Bun.spawn` with a 3s timeout.
 */
export const defaultGitCommandRunner: GitCommandRunner = async (
  args,
  opts = {},
) => {
  const timeoutMs = opts.timeoutMs ?? DEFAULT_GIT_TIMEOUT_MS;
  let stdinInit: Uint8Array | undefined;
  if (typeof opts.stdin === "string") {
    stdinInit = encodeUtf8(opts.stdin);
  } else if (opts.stdin) {
    stdinInit = opts.stdin;
  }

  let proc: ReturnType<typeof Bun.spawn>;
  try {
    proc = Bun.spawn(["git", ...args], {
      cwd: opts.cwd || undefined,
      stdout: "pipe",
      stderr: "pipe",
      stdin: stdinInit,
    });
  } catch (err) {
    return {
      stdout: new Uint8Array(0),
      stderr: new Uint8Array(0),
      exitCode: -1,
      error: err instanceof Error ? err.message : String(err),
    };
  }

  let timedOut = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const onAbort = () => {
    timedOut = true;
    try {
      proc.kill();
    } catch {
      // ignore
    }
  };

  if (timeoutMs > 0) {
    timer = setTimeout(() => {
      timedOut = true;
      try {
        proc.kill();
      } catch {
        // ignore
      }
    }, timeoutMs);
  }
  if (opts.signal) {
    if (opts.signal.aborted) {
      onAbort();
    } else {
      opts.signal.addEventListener("abort", onAbort, { once: true });
    }
  }

  try {
    const [stdout, stderr, exitCode] = await Promise.all([
      readStreamBytes(proc.stdout),
      readStreamBytes(proc.stderr),
      proc.exited,
    ]);
    if (timedOut) {
      return {
        stdout,
        stderr,
        exitCode: -1,
        timedOut: true,
        error: "git command timed out",
      };
    }
    return { stdout, stderr, exitCode };
  } catch (err) {
    return {
      stdout: new Uint8Array(0),
      stderr: new Uint8Array(0),
      exitCode: -1,
      timedOut,
      error: err instanceof Error ? err.message : String(err),
    };
  } finally {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
    if (opts.signal) {
      opts.signal.removeEventListener("abort", onAbort);
    }
  }
};

export interface CliGitDriverOptions {
  runner?: GitCommandRunner;
  timeoutMs?: number;
}

export class CliGitDriver implements LocalGitDriver {
  private readonly runner: GitCommandRunner;
  private readonly timeoutMs: number;

  constructor(opts: CliGitDriverOptions = {}) {
    this.runner = opts.runner ?? defaultGitCommandRunner;
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_GIT_TIMEOUT_MS;
  }

  async execGit(
    cwd: string,
    args: string[],
    opts: Omit<GitCommandOptions, "cwd"> = {},
  ): Promise<GitCommandResult> {
    return this.runner(["--no-optional-locks", ...args], {
      ...opts,
      cwd,
      timeoutMs: opts.timeoutMs ?? this.timeoutMs,
    });
  }

  async discoverRepo(dir: string): Promise<RepoInfo | null> {
    const trimmed = dir.trim();
    if (!trimmed) {
      return null;
    }

    let targetDir = trimmed;
    try {
      const st = statSync(trimmed);
      if (!st.isDirectory()) {
        targetDir = path.dirname(trimmed);
      }
    } catch {
      return null;
    }

    const canonDir = canonicalPath(targetDir);
    let topLevel = "";
    let commonDir = "";

    const rev = await this.execGit(canonDir, [
      "rev-parse",
      "--show-toplevel",
      "--git-common-dir",
      "--git-dir",
    ]);
    if (rev.exitCode === 0) {
      const lines = splitNonEmptyLines(decodeUtf8(rev.stdout));
      if (lines.length < 3) {
        return null;
      }
      topLevel = canonicalPath(resolveAbsPath(canonDir, lines[0]!));
      commonDir = canonicalPath(resolveAbsPath(canonDir, lines[1]!));
    } else {
      // Bare repository fallback.
      const bare = await this.execGit(canonDir, [
        "rev-parse",
        "--git-common-dir",
        "--is-bare-repository",
      ]);
      if (bare.exitCode !== 0) {
        return null;
      }
      const bLines = splitNonEmptyLines(decodeUtf8(bare.stdout));
      if (bLines.length < 2 || bLines[1] !== "true") {
        return null;
      }
      commonDir = canonicalPath(resolveAbsPath(canonDir, bLines[0]!));
    }

    const mainRoot =
      path.basename(commonDir) === ".git"
        ? path.dirname(commonDir)
        : commonDir;
    let repoName = path.basename(mainRoot);
    if (!repoName || repoName === "." || repoName === "/") {
      repoName = path.basename(topLevel);
    }

    const workDir = topLevel || mainRoot;

    // Current branch (empty when detached). Works even before initial commit.
    let currentBranch = "";
    const brRes = await this.execGit(workDir, [
      "symbolic-ref",
      "--quiet",
      "--short",
      "HEAD",
    ]);
    if (brRes.exitCode === 0) {
      currentBranch = decodeUtf8(brRes.stdout).trim();
    }

    // Check whether HEAD resolves to a commit (unborn repo detection).
    const headRes = await this.execGit(workDir, [
      "rev-parse",
      "--verify",
      "--quiet",
      "HEAD",
    ]);
    const hasCommits = headRes.exitCode === 0;

    const defaultBase = await this.discoverDefaultBase(mainRoot, currentBranch);

    const info: RepoInfo = {
      repo_key: commonDir,
      main_root: mainRoot,
      repo_name: repoName,
      current_worktree_path: topLevel,
      has_commits: hasCommits,
    };
    if (currentBranch) {
      info.current_branch = currentBranch;
    }
    if (defaultBase) {
      info.default_base = defaultBase;
    }
    return info;
  }

  async getStatus(cwd: string): Promise<GitInfo | null> {
    const trimmed = cwd.trim();
    if (!trimmed || !isDirSync(trimmed)) {
      return null;
    }

    const rev = await this.execGit(trimmed, [
      "rev-parse",
      "--show-toplevel",
      "--git-common-dir",
      "--git-dir",
    ]);
    if (rev.exitCode !== 0) {
      return null;
    }
    const lines = decodeUtf8(rev.stdout).replace(/[\r\n]+$/, "").split("\n");
    if (lines.length < 3) {
      return null;
    }

    const repoRoot = resolveAbsPath(trimmed, lines[0]!);
    const commonDir = resolveAbsPath(trimmed, lines[1]!);
    const gitDir = resolveAbsPath(trimmed, lines[2]!);
    if (!repoRoot) {
      return null;
    }

    const canonCommon = canonicalPath(commonDir);
    const canonGit = canonicalPath(gitDir);
    const repoKey = canonCommon || canonicalPath(repoRoot);
    const isWorktree = Boolean(
      canonCommon && canonGit && canonGit !== canonCommon,
    );

    let mainRoot = repoRoot;
    if (isWorktree) {
      mainRoot =
        path.basename(canonCommon) === ".git"
          ? path.dirname(canonCommon)
          : canonCommon;
    }

    let repoName = path.basename(mainRoot);
    if (!repoName || repoName === "." || repoName === "/") {
      repoName = path.basename(repoRoot);
    }

    let wtName = "";
    if (isWorktree) {
      wtName = path.basename(canonGit);
      if (!wtName || wtName === "." || wtName === ".git") {
        wtName = path.basename(repoRoot);
      }
    }

    const info: GitInfo = {
      repo_key: repoKey,
      repo_root: repoRoot,
      main_repo_root: mainRoot,
      repo_name: repoName,
      is_worktree: isWorktree,
      worktree_name: wtName,
      managed: false,
      branch: "",
      base_branch: "",
      ahead: 0,
      behind: 0,
      is_dirty: false,
      is_rebasing: isRebasingDir(gitDir),
      has_conflict: false,
      lines_added: 0,
      lines_removed: 0,
      uncommitted_added: 0,
      uncommitted_removed: 0,
      files_added: 0,
      files_modified: 0,
      pr: null,
      checks: null,
    };

    const stRes = await this.execGit(repoRoot, [
      "status",
      "--porcelain=v2",
      "--branch",
      "--untracked-files=all",
      "-z",
    ]);
    if (stRes.exitCode !== 0) {
      return info;
    }

    const obs = parsePorcelainV2(stRes.stdout);
    info.branch = obs.branch;
    info.ahead = obs.ahead;
    info.behind = obs.behind;
    info.is_dirty = obs.dirty;
    info.files_added = obs.filesAdded;
    info.files_modified = obs.filesModified;

    // Discover base branch and compute committed branch diff stats when on a feature branch.
    if (info.branch) {
      const base = await this.discoverBaseBranch(repoRoot, info.branch);
      info.base_branch = base;
      if (base && info.branch !== base) {
        const diffRes = await this.execGit(repoRoot, [
          "diff",
          "--no-ext-diff",
          "--no-textconv",
          "--numstat",
          `${base}...HEAD`,
        ]);
        if (diffRes.exitCode === 0) {
          const { added, removed } = parseNumstat(decodeUtf8(diffRes.stdout));
          info.lines_added = added;
          info.lines_removed = removed;
        }
        // Fast merge-conflict prediction (git >= 2.38). Ignore exit codes other than 0/1.
        const mergeRes = await this.execGit(repoRoot, [
          "merge-tree",
          "--write-tree",
          base,
          "HEAD",
        ]);
        if (mergeRes.exitCode === 1) {
          info.has_conflict = true;
        }
      }
    }

    // Compute uncommitted line stats (working tree diff vs HEAD + untracked text files).
    if (info.is_dirty) {
      const uRes = await this.execGit(repoRoot, [
        "diff",
        "--no-ext-diff",
        "--no-textconv",
        "--numstat",
        "HEAD",
      ]);
      if (uRes.exitCode === 0) {
        const { added, removed } = parseNumstat(decodeUtf8(uRes.stdout));
        info.uncommitted_added = added;
        info.uncommitted_removed = removed;
      } else {
        // Initial commit repo with no HEAD yet: fall back to cached diff.
        const cachedRes = await this.execGit(repoRoot, [
          "diff",
          "--no-ext-diff",
          "--no-textconv",
          "--numstat",
          "--cached",
        ]);
        if (cachedRes.exitCode === 0) {
          const { added, removed } = parseNumstat(decodeUtf8(cachedRes.stdout));
          info.uncommitted_added = added;
          info.uncommitted_removed = removed;
        }
      }

      const lsRes = await this.execGit(repoRoot, [
        "ls-files",
        "--others",
        "--exclude-standard",
        "-z",
      ]);
      if (lsRes.exitCode === 0) {
        let start = 0;
        for (let i = 0; i <= lsRes.stdout.length; i += 1) {
          if (i < lsRes.stdout.length && lsRes.stdout[i] !== 0) {
            continue;
          }
          if (i > start) {
            const relPath = decodeUtf8(lsRes.stdout.subarray(start, i));
            const full = path.join(repoRoot, relPath);
            try {
              const fi = lstatSync(full);
              if (fi.isSymbolicLink()) {
                info.uncommitted_added += 1;
              } else if (fi.isFile() && fi.size <= MAX_UNTRACKED_FILE_SIZE) {
                info.uncommitted_added += countTextLines(full);
              }
            } catch {
              // ignore disappearing files
            }
          }
          start = i + 1;
        }
      }
    }

    return info;
  }

  async getRemoteUrl(cwd: string, remote = "origin"): Promise<string | null> {
    const trimmed = cwd.trim();
    if (!trimmed) {
      return null;
    }
    const res = await this.execGit(trimmed, ["remote", "get-url", remote]);
    if (res.exitCode !== 0) {
      return null;
    }
    const url = decodeUtf8(res.stdout).trim();
    return url || null;
  }

  async listBranches(cwd: string): Promise<string[]> {
    const res = await this.execGit(cwd, [
      "for-each-ref",
      "--format=%(refname:short)",
      "refs/heads",
    ]);
    if (res.exitCode !== 0) {
      const detail =
        decodeUtf8(res.stderr).trim() || res.error || "unknown error";
      throw new Error(`git: list branches failed: ${detail}`);
    }
    return splitNonEmptyLines(decodeUtf8(res.stdout));
  }

  async getDiffRaw(
    cwd: string,
    opts: GitDiffRawOptions,
  ): Promise<GitDiffRawResult> {
    const baseArgs = ["diff", "--no-ext-diff", "--no-textconv"];
    if (opts.contextLines !== undefined && opts.contextLines >= 0) {
      baseArgs.push(`-U${Math.floor(opts.contextLines)}`);
    }
    const pathArgs =
      opts.paths && opts.paths.length > 0 ? ["--", ...opts.paths] : [];

    let resolvedBase = opts.baseRef?.trim() || undefined;
    let resolvedHead = opts.headRef?.trim() || undefined;

    switch (opts.scope) {
      case "staged": {
        const res = await this.execGit(cwd, [
          ...baseArgs,
          "--cached",
          ...pathArgs,
        ]);
        if (res.exitCode !== 0) {
          throw new Error(
            `git diff (staged) failed: ${decodeUtf8(res.stderr).trim() || res.error || "unknown error"}`,
          );
        }
        return {
          raw: decodeUtf8(res.stdout),
          baseRef: resolvedBase,
          headRef: resolvedHead,
        };
      }
      case "unstaged": {
        const res = await this.execGit(cwd, [...baseArgs, ...pathArgs]);
        if (res.exitCode !== 0) {
          throw new Error(
            `git diff (unstaged) failed: ${decodeUtf8(res.stderr).trim() || res.error || "unknown error"}`,
          );
        }
        return {
          raw: decodeUtf8(res.stdout),
          baseRef: resolvedBase,
          headRef: resolvedHead,
        };
      }
      case "working_tree": {
        const res = await this.execGit(cwd, [
          ...baseArgs,
          "HEAD",
          ...pathArgs,
        ]);
        if (res.exitCode === 0) {
          return {
            raw: decodeUtf8(res.stdout),
            baseRef: resolvedBase ?? "HEAD",
            headRef: resolvedHead,
          };
        }
        // Fallback for unborn repo without HEAD.
        const cached = await this.execGit(cwd, [
          ...baseArgs,
          "--cached",
          ...pathArgs,
        ]);
        if (cached.exitCode !== 0) {
          throw new Error(
            `git diff (working_tree) failed: ${decodeUtf8(res.stderr).trim() || res.error || "unknown error"}`,
          );
        }
        return {
          raw: decodeUtf8(cached.stdout),
          baseRef: resolvedBase,
          headRef: resolvedHead,
        };
      }
      case "branch_base": {
        if (!resolvedBase) {
          const brRes = await this.execGit(cwd, [
            "symbolic-ref",
            "--quiet",
            "--short",
            "HEAD",
          ]);
          const curBranch =
            brRes.exitCode === 0 ? decodeUtf8(brRes.stdout).trim() : "";
          resolvedBase =
            (await this.discoverBaseBranch(cwd, curBranch)) || undefined;
          if (!resolvedBase) {
            const repo = await this.discoverRepo(cwd);
            resolvedBase = repo?.default_base || undefined;
          }
        }
        if (!resolvedBase) {
          throw new Error("git diff (branch_base): could not resolve base ref");
        }
        resolvedHead = resolvedHead ?? "HEAD";
        const res = await this.execGit(cwd, [
          ...baseArgs,
          `${resolvedBase}...${resolvedHead}`,
          ...pathArgs,
        ]);
        if (res.exitCode !== 0) {
          throw new Error(
            `git diff (branch_base) failed: ${decodeUtf8(res.stderr).trim() || res.error || "unknown error"}`,
          );
        }
        return {
          raw: decodeUtf8(res.stdout),
          baseRef: resolvedBase,
          headRef: resolvedHead,
        };
      }
      case "commit_range": {
        if (!resolvedBase) {
          throw new Error("git diff (commit_range): baseRef is required");
        }
        const rangeArg = resolvedHead
          ? `${resolvedBase}..${resolvedHead}`
          : resolvedBase;
        const res = await this.execGit(cwd, [
          ...baseArgs,
          rangeArg,
          ...pathArgs,
        ]);
        if (res.exitCode !== 0) {
          throw new Error(
            `git diff (commit_range) failed: ${decodeUtf8(res.stderr).trim() || res.error || "unknown error"}`,
          );
        }
        return {
          raw: decodeUtf8(res.stdout),
          baseRef: resolvedBase,
          headRef: resolvedHead,
        };
      }
    }
  }

  private async refExists(repoRoot: string, ref: string): Promise<boolean> {
    const res = await this.execGit(repoRoot, [
      "show-ref",
      "--verify",
      "--quiet",
      ref,
    ]);
    return res.exitCode === 0;
  }

  private async discoverDefaultBase(
    repoRoot: string,
    currentBranch: string,
  ): Promise<string> {
    // 1. origin/HEAD symbolic ref.
    const sym = await this.execGit(repoRoot, [
      "symbolic-ref",
      "--quiet",
      "refs/remotes/origin/HEAD",
    ]);
    if (sym.exitCode === 0) {
      const ref = decodeUtf8(sym.stdout).trim();
      if (ref.startsWith("refs/remotes/origin/")) {
        const b = ref.slice("refs/remotes/origin/".length);
        if (b) {
          if (await this.refExists(repoRoot, `refs/heads/${b}`)) {
            return b;
          }
          if (await this.refExists(repoRoot, `refs/remotes/origin/${b}`)) {
            return `origin/${b}`;
          }
          return b;
        }
      }
    }
    // 2. Local main or master.
    for (const cand of ["main", "master"]) {
      if (await this.refExists(repoRoot, `refs/heads/${cand}`)) {
        return cand;
      }
    }
    // 3. Fallback to current branch.
    return currentBranch;
  }

  private async discoverBaseBranch(
    repoRoot: string,
    branch: string,
  ): Promise<string> {
    if (branch) {
      for (const key of [
        `branch.${branch}.spaces-base`,
        `branch.${branch}.workmux-base`,
      ]) {
        const cfg = await this.execGit(repoRoot, ["config", "--local", key]);
        if (cfg.exitCode === 0) {
          const val = decodeUtf8(cfg.stdout).trim();
          if (val) {
            return val;
          }
        }
      }
    }

    const sym = await this.execGit(repoRoot, [
      "symbolic-ref",
      "refs/remotes/origin/HEAD",
    ]);
    if (sym.exitCode === 0) {
      const ref = decodeUtf8(sym.stdout).trim();
      if (ref.startsWith("refs/remotes/origin/")) {
        const b = ref.slice("refs/remotes/origin/".length);
        if (b) {
          return b;
        }
      }
    }

    for (const cand of ["main", "master"]) {
      const res = await this.execGit(repoRoot, [
        "rev-parse",
        "--verify",
        "--quiet",
        cand,
      ]);
      if (res.exitCode === 0) {
        return cand;
      }
    }

    return "";
  }
}

export function pathExists(p: string): boolean {
  return existsSync(p);
}

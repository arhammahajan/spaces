import { createHash } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  readdirSync,
  rmdirSync,
  statSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import type {
  RepoInfo,
  WorktreeContext,
  WorktreeCreateResult,
  WorktreeEntry,
  WorktreeNameSuggestion,
  WorktreePruneResult,
  WorktreeRecord,
  WorktreeRemoveSafety,
} from "@spaces/types";
import {
  CliGitDriver,
  canonicalPath,
  decodeUtf8,
  resolveAbsPath,
  type LocalGitDriver,
} from "../git/driver";
import type { SpacesStore } from "../store/db";
import {
  suggestName,
  validateBranchName,
  worktreeDirName,
  type NamingContext,
} from "./naming";

export interface WorktreeManagerOptions {
  store: SpacesStore;
  driver?: LocalGitDriver;
  worktreeRoot?: string;
  now?: () => number;
}

export interface CreateWorktreeInput {
  repo_dir: string;
  branch?: string;
  base_ref?: string;
  naming?: NamingContext;
  origin_space?: string;
  origin_window_id?: string;
  origin_pane_id?: string;
  worktree_root?: string;
}

export interface RemoveWorktreeOptions {
  force?: boolean;
  delete_branch?: boolean;
  force_branch?: boolean;
}

export interface PruneWorktreesOptions {
  repo_dirs?: readonly string[];
  remove_orphaned_merged?: boolean;
  active_paths?: Iterable<string>;
}

export interface ParsedWorktreePorcelain {
  path: string;
  head_sha: string;
  branch: string;
  is_bare: boolean;
  is_detached: boolean;
  is_locked: boolean;
  is_prunable: boolean;
}

/**
 * Resolves the default worktree storage directory:
 * $TMUX_SPACES_WORKTREE_DIR, else $TMUX_SPACES_DATA_DIR/worktrees,
 * else ${XDG_DATA_HOME:-~/.local/share}/tmux-spaces/worktrees.
 */
export function defaultWorktreeRoot(): string {
  const explicit = (process.env.TMUX_SPACES_WORKTREE_DIR ?? "").trim();
  if (explicit) {
    return path.normalize(explicit);
  }
  const dataDir = (process.env.TMUX_SPACES_DATA_DIR ?? "").trim();
  if (dataDir) {
    return path.join(path.normalize(dataDir), "worktrees");
  }
  let base = (process.env.XDG_DATA_HOME ?? "").trim();
  if (!base) {
    const home = os.homedir() || os.tmpdir();
    base = path.join(home, ".local", "share");
  }
  return path.join(base, "tmux-spaces", "worktrees");
}

/**
 * Reports whether `candidate` equals `root` or is strictly inside `root`.
 * Both arguments should be cleaned canonical paths.
 */
export function pathWithin(candidate: string, root: string): boolean {
  if (!candidate || !root) {
    return false;
  }
  if (candidate === root) {
    return true;
  }
  const prefix = root.endsWith(path.sep) ? root : `${root}${path.sep}`;
  return candidate.startsWith(prefix);
}

/**
 * Parses `git worktree list --porcelain` output.
 */
export function parseWorktreePorcelain(raw: string): ParsedWorktreePorcelain[] {
  const out: ParsedWorktreePorcelain[] = [];
  let cur: ParsedWorktreePorcelain | null = null;

  const flush = () => {
    if (cur && cur.path) {
      out.push(cur);
    }
    cur = null;
  };

  for (const rawLine of raw.replace(/\r\n/g, "\n").split("\n")) {
    const line = rawLine.trim();
    if (!line) {
      flush();
      continue;
    }
    if (line.startsWith("worktree ")) {
      flush();
      cur = {
        path: line.slice("worktree ".length).trim(),
        head_sha: "",
        branch: "",
        is_bare: false,
        is_detached: false,
        is_locked: false,
        is_prunable: false,
      };
    } else if (!cur) {
      continue;
    } else if (line.startsWith("HEAD ")) {
      cur.head_sha = line.slice("HEAD ".length).trim();
    } else if (line.startsWith("branch ")) {
      const ref = line.slice("branch ".length).trim();
      cur.branch = ref.startsWith("refs/heads/")
        ? ref.slice("refs/heads/".length)
        : ref;
    } else if (line === "bare") {
      cur.is_bare = true;
    } else if (line === "detached") {
      cur.is_detached = true;
    } else if (line === "locked" || line.startsWith("locked ")) {
      cur.is_locked = true;
    } else if (line === "prunable" || line.startsWith("prunable ")) {
      cur.is_prunable = true;
    }
  }
  flush();
  return out;
}

function errDetail(stderr: Uint8Array, error?: string): string {
  const msg = decodeUtf8(stderr).trim();
  if (msg) {
    return msg;
  }
  return error || "unknown error";
}

function isDirSync(p: string): boolean {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}

function uniqueDestinationPath(
  bucketDir: string,
  baseName: string,
): { dstPath: string; finalDirName: string } {
  let cand = path.join(bucketDir, baseName);
  if (!existsSync(cand)) {
    return { dstPath: cand, finalDirName: baseName };
  }
  for (let i = 2; i <= 9999; i += 1) {
    const name = `${baseName}-${i}`;
    cand = path.join(bucketDir, name);
    if (!existsSync(cand)) {
      return { dstPath: cand, finalDirName: name };
    }
  }
  const fallback = `${baseName}-${Math.floor(Date.now() / 1000) % 100000}`;
  return { dstPath: path.join(bucketDir, fallback), finalDirName: fallback };
}

/**
 * Orchestrates Git worktree discovery, creation, safety inspection, removal,
 * and pruning alongside persistent metadata in `SpacesStore`.
 */
export class WorktreeManager {
  private readonly store: SpacesStore;
  private readonly driver: LocalGitDriver;
  private readonly rootDir: string;
  private readonly now: () => number;

  constructor(opts: WorktreeManagerOptions) {
    this.store = opts.store;
    this.driver = opts.driver ?? new CliGitDriver();
    this.rootDir = opts.worktreeRoot?.trim()
      ? path.normalize(opts.worktreeRoot.trim())
      : defaultWorktreeRoot();
    this.now = opts.now ?? Date.now;
  }

  get worktreeRoot(): string {
    return this.rootDir;
  }

  async discoverRepo(dir: string): Promise<RepoInfo> {
    const trimmed = dir.trim();
    if (!trimmed) {
      throw new Error("worktree: repo directory is empty");
    }
    const repo = await this.driver.discoverRepo(trimmed);
    if (!repo) {
      throw new Error(`worktree: not a git repository: ${trimmed}`);
    }
    return repo;
  }

  async list(
    dir: string,
  ): Promise<{ repo: RepoInfo; worktrees: WorktreeEntry[] }> {
    const repo = await this.discoverRepo(dir);
    const res = await this.driver.execGit(repo.main_root, [
      "worktree",
      "list",
      "--porcelain",
    ]);
    if (res.exitCode !== 0) {
      throw new Error(
        `worktree: list failed: ${errDetail(res.stderr, res.error)}`,
      );
    }

    const rawList = parseWorktreePorcelain(decodeUtf8(res.stdout));
    const recByPath = new Map<string, WorktreeRecord>();
    for (const rec of this.store.listWorktreesByRepo(repo.repo_key)) {
      recByPath.set(canonicalPath(rec.path), rec);
    }

    const canonMain = canonicalPath(repo.main_root);
    const canonCurrent = canonicalPath(repo.current_worktree_path);
    const result: WorktreeEntry[] = [];

    for (const raw of rawList) {
      const wtPath = canonicalPath(raw.path);
      const isMain = !raw.is_bare && wtPath === canonMain;
      const rec = recByPath.get(wtPath);

      let name = rec?.name || "";
      if (!name) {
        name = isMain ? repo.repo_name : path.basename(wtPath);
      }

      let isDirty = false;
      let unmergedCommits = 0;
      const baseBranch = await this.resolveWorktreeBase(
        repo,
        raw.branch,
        rec?.base_branch,
      );

      if (!raw.is_bare && !raw.is_prunable && isDirSync(wtPath)) {
        const stRes = await this.driver.execGit(wtPath, [
          "status",
          "--porcelain",
        ]);
        if (stRes.exitCode === 0) {
          isDirty = decodeUtf8(stRes.stdout).trim().length > 0;
        }
        if (baseBranch && raw.branch !== baseBranch) {
          const cntRes = await this.driver.execGit(wtPath, [
            "rev-list",
            "--count",
            `${baseBranch}..HEAD`,
          ]);
          if (cntRes.exitCode === 0) {
            const n = Number.parseInt(decodeUtf8(cntRes.stdout).trim(), 10);
            if (Number.isFinite(n) && n > 0) {
              unmergedCommits = n;
            }
          }
        }
      }

      result.push({
        path: wtPath,
        name,
        branch: raw.branch,
        head_sha: raw.head_sha,
        base_branch: baseBranch,
        repo_key: repo.repo_key,
        main_repo_root: repo.main_root,
        repo_name: repo.repo_name,
        is_main: isMain,
        is_current: Boolean(canonCurrent && wtPath === canonCurrent),
        is_detached: raw.is_detached,
        is_locked: raw.is_locked,
        is_prunable: raw.is_prunable,
        is_dirty: isDirty,
        managed: rec?.managed ?? false,
        name_source: rec?.name_source ?? "",
        created_at_ms: rec?.created_at_ms ?? 0,
        unmerged_commits: unmergedCommits,
        open_spaces: [],
        open_window_ids: [],
        open_window_count: 0,
        git: null,
      });
    }

    return { repo, worktrees: result };
  }

  async listBranches(dir: string): Promise<string[]> {
    return this.driver.listBranches(dir);
  }

  async takenNames(dir: string): Promise<Set<string>> {
    const taken = new Set<string>();
    const add = (v: string | undefined) => {
      const low = (v ?? "").trim().toLowerCase();
      if (low) {
        taken.add(low);
      }
    };

    let repo: RepoInfo;
    let wts: WorktreeEntry[];
    try {
      const listed = await this.list(dir);
      repo = listed.repo;
      wts = listed.worktrees;
    } catch {
      return taken;
    }

    for (const wt of wts) {
      add(wt.branch);
      if (wt.branch) {
        add(worktreeDirName(wt.branch));
      }
      add(wt.name);
      add(path.basename(wt.path));
    }

    try {
      const branches = await this.listBranches(repo.main_root);
      for (const br of branches) {
        add(br);
        add(worktreeDirName(br));
      }
    } catch {
      // ignore
    }

    for (const rec of this.store.listWorktreesByRepo(repo.repo_key)) {
      add(rec.branch);
      add(rec.name);
      add(path.basename(rec.path));
    }

    const bucket = await this.repoBucketDir(this.rootDir, repo);
    try {
      for (const entry of readdirSync(bucket)) {
        add(entry);
      }
    } catch {
      // bucket may not exist yet
    }

    return taken;
  }

  async suggestForDir(
    dir: string,
    naming: NamingContext = {},
  ): Promise<{ repo: RepoInfo | null; suggestion: WorktreeNameSuggestion }> {
    let repo: RepoInfo | null = null;
    let taken: Set<string> | undefined;
    const nctx: NamingContext = { ...naming };

    if (dir.trim()) {
      const discovered = await this.driver.discoverRepo(dir);
      if (discovered) {
        repo = discovered;
        if (!nctx.repo_name) {
          nctx.repo_name = discovered.repo_name;
        }
        if (!nctx.current_branch && discovered.current_branch) {
          nctx.current_branch = discovered.current_branch;
        }
        taken = await this.takenNames(dir);
      }
    }

    return { repo, suggestion: suggestName(nctx, taken) };
  }

  async create(req: CreateWorktreeInput): Promise<WorktreeCreateResult> {
    const repo = await this.discoverRepo(req.repo_dir);
    if (!repo.has_commits) {
      throw new Error(
        `worktree: repository "${repo.repo_name}" has no commits yet; create an initial commit first`,
      );
    }

    const { worktrees: existingWTs } = await this.list(repo.main_root);
    const root = req.worktree_root?.trim()
      ? path.normalize(req.worktree_root.trim())
      : this.rootDir;

    const taken = await this.takenNames(repo.main_root);
    const bucketDir = await this.repoBucketDir(root, repo);
    try {
      for (const entry of readdirSync(bucketDir)) {
        taken.add(entry.toLowerCase());
      }
    } catch {
      // bucket may not exist yet
    }

    let namingRes: WorktreeNameSuggestion;
    const explicitBranch = (req.branch ?? "").trim();
    if (explicitBranch) {
      const errMsg = validateBranchName(explicitBranch);
      if (errMsg) {
        throw new Error(`worktree: invalid branch name: ${errMsg}`);
      }
      namingRes = {
        branch: explicitBranch,
        dir_name: worktreeDirName(explicitBranch),
        source: "custom",
        rule: "explicit",
      };
    } else {
      const nctx: NamingContext = { ...(req.naming ?? {}) };
      if (!nctx.repo_name) {
        nctx.repo_name = repo.repo_name;
      }
      if (!nctx.current_branch && repo.current_branch) {
        nctx.current_branch = repo.current_branch;
      }
      namingRes = suggestName(nctx, taken);
      const errMsg = validateBranchName(namingRes.branch);
      if (errMsg) {
        throw new Error(`worktree: suggested branch invalid: ${errMsg}`);
      }
    }

    const branch = namingRes.branch;

    // Refuse if branch is already checked out in an existing worktree.
    for (const wt of existingWTs) {
      if (wt.branch === branch) {
        throw new Error(
          `worktree: branch "${branch}" is already checked out at ${wt.path}`,
        );
      }
    }

    const baseRef =
      (req.base_ref ?? "").trim() || repo.default_base || "HEAD";

    mkdirSync(bucketDir, { recursive: true, mode: 0o755 });

    const dirName = namingRes.dir_name || worktreeDirName(branch);
    const { dstPath, finalDirName } = uniqueDestinationPath(bucketDir, dirName);
    namingRes = { ...namingRes, dir_name: finalDirName };

    const branchExists = await this.refExists(
      repo.main_root,
      `refs/heads/${branch}`,
    );
    let createdBranch = false;
    let addRes;
    if (branchExists) {
      addRes = await this.driver.execGit(repo.main_root, [
        "worktree",
        "add",
        dstPath,
        branch,
      ]);
    } else {
      createdBranch = true;
      addRes = await this.driver.execGit(repo.main_root, [
        "worktree",
        "add",
        "-b",
        branch,
        dstPath,
        baseRef,
      ]);
    }
    if (addRes.exitCode !== 0) {
      throw new Error(
        `worktree: git worktree add failed: ${errDetail(addRes.stderr, addRes.error)}`,
      );
    }

    const canonDst = canonicalPath(dstPath);
    let headSHA = "";
    const headRes = await this.driver.execGit(canonDst, ["rev-parse", "HEAD"]);
    if (headRes.exitCode === 0) {
      headSHA = decodeUtf8(headRes.stdout).trim();
    }

    const nowMs = this.now();
    const rec: WorktreeRecord = {
      path: canonDst,
      repo_key: repo.repo_key,
      main_repo_root: repo.main_root,
      repo_name: repo.repo_name,
      name: finalDirName,
      branch,
      base_branch: baseRef,
      managed: true,
      name_source: namingRes.source,
      created_at_ms: nowMs,
      last_used_at_ms: nowMs,
    };
    if (req.origin_space) rec.origin_space = req.origin_space;
    if (req.origin_window_id) rec.origin_window_id = req.origin_window_id;
    if (req.origin_pane_id) rec.origin_pane_id = req.origin_pane_id;

    try {
      this.store.upsertWorktree(rec);
    } catch (persistErr) {
      const rbErrors = await this.rollbackCreate(
        repo.main_root,
        canonDst,
        branch,
        createdBranch,
      );
      const baseMsg =
        persistErr instanceof Error ? persistErr.message : String(persistErr);
      if (rbErrors.length > 0) {
        throw new Error(
          `worktree: persist metadata: ${baseMsg} (rollback failed: ${rbErrors.join("; ")})`,
        );
      }
      throw new Error(`worktree: persist metadata: ${baseMsg}`);
    }

    // Record base branch in local git config (best-effort) after persistence succeeds.
    await this.driver.execGit(repo.main_root, [
      "config",
      "--local",
      `branch.${branch}.spaces-base`,
      baseRef,
    ]);

    const wt: WorktreeEntry = {
      path: canonDst,
      name: finalDirName,
      branch,
      head_sha: headSHA,
      base_branch: baseRef,
      repo_key: repo.repo_key,
      main_repo_root: repo.main_root,
      repo_name: repo.repo_name,
      is_main: false,
      is_current: false,
      is_detached: false,
      is_locked: false,
      is_prunable: false,
      is_dirty: false,
      managed: true,
      name_source: namingRes.source,
      created_at_ms: nowMs,
      unmerged_commits: 0,
      open_spaces: [],
      open_window_ids: [],
      open_window_count: 0,
      git: null,
    };

    return {
      worktree: wt,
      created_branch: createdBranch,
      naming: namingRes,
    };
  }

  async inspectSafety(wtPath: string): Promise<WorktreeRemoveSafety> {
    const { wt } = await this.lookupWorktree(wtPath);

    const warnings: string[] = [];
    if (wt.is_main) {
      warnings.push("cannot remove main repository worktree");
    }
    if (wt.is_dirty) {
      warnings.push("worktree has uncommitted changes");
    }
    if (wt.unmerged_commits > 0) {
      const base = wt.base_branch || "base branch";
      warnings.push(
        `branch has ${wt.unmerged_commits} unmerged commit(s) relative to ${base}`,
      );
    }
    if (wt.is_locked) {
      warnings.push("worktree is locked");
    }

    return {
      can_remove_safely:
        !wt.is_main &&
        !wt.is_dirty &&
        wt.unmerged_commits === 0 &&
        !wt.is_locked,
      is_main: wt.is_main,
      is_dirty: wt.is_dirty,
      unmerged_commits: wt.unmerged_commits,
      branch: wt.branch,
      base_branch: wt.base_branch,
      warnings,
    };
  }

  async remove(
    wtPath: string,
    opts: RemoveWorktreeOptions = {},
  ): Promise<void> {
    const { repo, wt } = await this.lookupWorktree(wtPath);
    if (wt.is_main) {
      throw new Error(
        `worktree: refusing to remove main repository worktree ${wt.path}`,
      );
    }

    const args = ["worktree", "remove"];
    if (opts.force) {
      args.push("--force");
    }
    args.push(wt.path);

    const res = await this.driver.execGit(repo.main_root, args);
    if (res.exitCode !== 0) {
      throw new Error(
        `worktree: git worktree remove failed: ${errDetail(res.stderr, res.error)}`,
      );
    }

    // Clean up store record and empty parent repo bucket directory best-effort.
    try {
      this.store.deleteWorktreeByPath(wt.path);
    } catch {
      // ignore
    }
    this.cleanupEmptyParent(wt.path, repo.main_root);

    if (
      opts.delete_branch &&
      wt.branch &&
      wt.branch !== wt.base_branch &&
      wt.branch !== (repo.default_base ?? "")
    ) {
      const flag = opts.force_branch ? "-D" : "-d";
      const brRes = await this.driver.execGit(repo.main_root, [
        "branch",
        flag,
        wt.branch,
      ]);
      if (brRes.exitCode !== 0) {
        throw new Error(
          `worktree: removed worktree but failed to delete branch "${wt.branch}": ${errDetail(brRes.stderr, brRes.error)}`,
        );
      }
    }
  }

  async prune(opts: PruneWorktreesOptions = {}): Promise<WorktreePruneResult> {
    const res: WorktreePruneResult = {
      pruned_git: 0,
      pruned_records: 0,
      removed_paths: [],
    };

    const canonActiveSet = new Set<string>();
    const canonActiveList: string[] = [];
    if (opts.active_paths) {
      for (const p of opts.active_paths) {
        const cp = canonicalPath(p);
        if (cp && !canonActiveSet.has(cp)) {
          canonActiveSet.add(cp);
          canonActiveList.push(cp);
        }
      }
    }

    const isActiveWorktree = (wtPath: string): boolean => {
      const canonWT = canonicalPath(wtPath);
      if (!canonWT) {
        return false;
      }
      if (canonActiveSet.has(canonWT)) {
        return true;
      }
      for (const ap of canonActiveList) {
        if (pathWithin(ap, canonWT)) {
          return true;
        }
      }
      return false;
    };

    const reposByKey = new Map<string, RepoInfo>();
    const repoOrder: RepoInfo[] = [];
    const addRepo = async (dir: string) => {
      if (!dir.trim()) {
        return;
      }
      const repo = await this.driver.discoverRepo(dir);
      if (!repo || reposByKey.has(repo.repo_key)) {
        return;
      }
      reposByKey.set(repo.repo_key, repo);
      repoOrder.push(repo);
    };

    for (const dir of opts.repo_dirs ?? []) {
      await addRepo(dir);
    }
    if (repoOrder.length === 0) {
      for (const rec of this.store.listWorktrees()) {
        await addRepo(rec.main_repo_root);
      }
    }

    // 1. Run `git worktree prune -v` per unique repository.
    for (const repo of repoOrder) {
      const pRes = await this.driver.execGit(repo.main_root, [
        "worktree",
        "prune",
        "-v",
      ]);
      if (pRes.exitCode === 0) {
        const combined = `${decodeUtf8(pRes.stdout)}\n${decodeUtf8(pRes.stderr)}`;
        for (const line of combined.split("\n")) {
          if (line.trim().startsWith("Removing ")) {
            res.pruned_git += 1;
          }
        }
      }
    }

    // 2. Prune store records whose paths no longer exist on disk.
    const prunedPaths = this.store.pruneMissingWorktrees();
    res.pruned_records = prunedPaths.length;

    // 3. Optionally remove orphaned, clean, merged managed worktrees.
    if (opts.remove_orphaned_merged) {
      for (const repo of repoOrder) {
        let wts: WorktreeEntry[];
        try {
          ({ worktrees: wts } = await this.list(repo.main_root));
        } catch {
          continue;
        }
        for (const wt of wts) {
          if (
            !wt.managed ||
            wt.is_main ||
            wt.is_locked ||
            wt.is_prunable
          ) {
            continue;
          }
          if (isActiveWorktree(wt.path)) {
            continue;
          }
          if (wt.is_dirty || wt.unmerged_commits > 0) {
            continue;
          }
          if (!wt.base_branch) {
            continue;
          }
          try {
            await this.remove(wt.path, {
              force: false,
              delete_branch: true,
              force_branch: false,
            });
            res.removed_paths.push(wt.path);
          } catch {
            // Best-effort cleanup for orphaned worktrees.
          }
        }
      }
    }

    return res;
  }

  async resolveWorktreePath(
    repoDir: string,
    nameOrPath: string,
  ): Promise<string> {
    const query = nameOrPath.trim();
    if (!query) {
      throw new Error("worktree: name or path is empty");
    }
    const canonQuery = canonicalPath(query);
    const lowQuery = query.toLowerCase();

    let repo: RepoInfo | null = null;
    if (repoDir.trim()) {
      try {
        const listed = await this.list(repoDir);
        repo = listed.repo;
        const wts = listed.worktrees;
        for (const wt of wts) {
          if (canonicalPath(wt.path) === canonQuery) {
            return wt.path;
          }
        }
        for (const wt of wts) {
          if (
            wt.name === query ||
            wt.branch === query ||
            path.basename(wt.path) === query
          ) {
            return wt.path;
          }
        }
        for (const wt of wts) {
          if (
            wt.name.toLowerCase() === lowQuery ||
            wt.branch.toLowerCase() === lowQuery ||
            path.basename(wt.path).toLowerCase() === lowQuery
          ) {
            return wt.path;
          }
        }
      } catch {
        // Fall through to store lookup.
      }
    }

    const recs = repo
      ? this.store.listWorktreesByRepo(repo.repo_key)
      : this.store.listWorktrees();
    for (const rec of recs) {
      if (canonicalPath(rec.path) === canonQuery) {
        return rec.path;
      }
    }
    for (const rec of recs) {
      if (
        rec.name === query ||
        rec.branch === query ||
        path.basename(rec.path) === query
      ) {
        return rec.path;
      }
    }
    for (const rec of recs) {
      if (
        rec.name.toLowerCase() === lowQuery ||
        rec.branch.toLowerCase() === lowQuery ||
        path.basename(rec.path).toLowerCase() === lowQuery
      ) {
        return rec.path;
      }
    }

    if (isDirSync(canonQuery)) {
      const discovered = await this.driver.discoverRepo(canonQuery);
      if (
        discovered?.current_worktree_path &&
        canonicalPath(discovered.current_worktree_path) === canonQuery
      ) {
        return canonQuery;
      }
    }

    throw new Error(`worktree: could not resolve "${query}"`);
  }

  async getContext(
    dir: string,
    naming: NamingContext = {},
  ): Promise<WorktreeContext> {
    const empty: WorktreeContext = {
      in_repo: false,
      repo_key: "",
      main_repo_root: "",
      repo_name: "",
      worktree_root: this.rootDir,
      default_base: "",
      current_branch: "",
      current_path: "",
      has_commits: false,
      worktrees: [],
      branches: [],
      auto_branch: "",
      auto_dir_name: "",
      auto_source: "",
      auto_rule: "",
    };

    if (!dir.trim()) {
      return empty;
    }

    let repo: RepoInfo;
    let worktrees: WorktreeEntry[];
    try {
      const listed = await this.list(dir);
      repo = listed.repo;
      worktrees = listed.worktrees;
    } catch {
      return empty;
    }

    let branches: string[] = [];
    try {
      branches = await this.listBranches(repo.main_root);
    } catch {
      branches = [];
    }

    const { suggestion } = await this.suggestForDir(dir, naming);

    return {
      in_repo: true,
      repo_key: repo.repo_key,
      main_repo_root: repo.main_root,
      repo_name: repo.repo_name,
      worktree_root: this.rootDir,
      default_base: repo.default_base ?? "",
      current_branch: repo.current_branch ?? "",
      current_path: repo.current_worktree_path,
      has_commits: repo.has_commits,
      worktrees,
      branches,
      auto_branch: suggestion.branch,
      auto_dir_name: suggestion.dir_name,
      auto_source: suggestion.source,
      auto_rule: suggestion.rule,
    };
  }

  private async refExists(repoRoot: string, ref: string): Promise<boolean> {
    const res = await this.driver.execGit(repoRoot, [
      "show-ref",
      "--verify",
      "--quiet",
      ref,
    ]);
    return res.exitCode === 0;
  }

  private async resolveWorktreeBase(
    repo: RepoInfo,
    branch: string,
    recordBase?: string,
  ): Promise<string> {
    if (branch) {
      for (const key of [
        `branch.${branch}.spaces-base`,
        `branch.${branch}.workmux-base`,
      ]) {
        const res = await this.driver.execGit(repo.main_root, [
          "config",
          "--local",
          key,
        ]);
        if (res.exitCode === 0) {
          const v = decodeUtf8(res.stdout).trim();
          if (v) {
            return v;
          }
        }
      }
    }
    if (recordBase?.trim()) {
      return recordBase.trim();
    }
    return repo.default_base ?? "";
  }

  private async repoBucketDir(root: string, repo: RepoInfo): Promise<string> {
    let slug = worktreeDirName(repo.repo_name.toLowerCase());
    if (!slug) {
      slug = "repo";
    }
    const candidate = path.join(root, slug);
    if (!(await this.bucketOwnedByOtherRepo(candidate, repo.repo_key))) {
      return candidate;
    }
    const hash = createHash("sha256")
      .update(repo.repo_key)
      .digest("hex")
      .slice(0, 6);
    return path.join(root, `${slug}-${hash}`);
  }

  private async bucketOwnedByOtherRepo(
    bucketDir: string,
    repoKey: string,
  ): Promise<boolean> {
    const canonBucket = canonicalPath(bucketDir);
    const canonRepoKey = canonicalPath(repoKey);

    for (const rec of this.store.listWorktrees()) {
      if (!rec.repo_key || canonicalPath(rec.repo_key) === canonRepoKey) {
        continue;
      }
      if (canonicalPath(path.dirname(rec.path)) === canonBucket) {
        return true;
      }
    }

    let entries: string[];
    try {
      entries = readdirSync(bucketDir);
    } catch {
      return false;
    }

    for (const name of entries) {
      const sub = path.join(bucketDir, name);
      if (!isDirSync(sub)) {
        continue;
      }
      const res = await this.driver.execGit(sub, [
        "rev-parse",
        "--git-common-dir",
      ]);
      if (res.exitCode === 0) {
        const common = canonicalPath(
          resolveAbsPath(sub, decodeUtf8(res.stdout).trim()),
        );
        if (common && common !== canonRepoKey) {
          return true;
        }
      }
    }
    return false;
  }

  private async rollbackCreate(
    mainRoot: string,
    dstPath: string,
    branch: string,
    deleteBranch: boolean,
  ): Promise<string[]> {
    const errors: string[] = [];
    const rmRes = await this.driver.execGit(mainRoot, [
      "worktree",
      "remove",
      "--force",
      dstPath,
    ]);
    if (rmRes.exitCode !== 0) {
      errors.push(
        `remove worktree ${dstPath}: ${errDetail(rmRes.stderr, rmRes.error)}`,
      );
      const pRes = await this.driver.execGit(mainRoot, ["worktree", "prune"]);
      if (pRes.exitCode !== 0) {
        errors.push(`prune worktrees: ${errDetail(pRes.stderr, pRes.error)}`);
      }
    } else {
      this.cleanupEmptyParent(dstPath, mainRoot);
    }

    if (deleteBranch && branch) {
      const brRes = await this.driver.execGit(mainRoot, [
        "branch",
        "-D",
        branch,
      ]);
      if (brRes.exitCode !== 0) {
        errors.push(
          `delete branch "${branch}": ${errDetail(brRes.stderr, brRes.error)}`,
        );
      }
    }

    return errors;
  }

  private cleanupEmptyParent(wtPath: string, mainRoot: string): void {
    const parent = canonicalPath(path.dirname(wtPath));
    if (!parent || parent === "/" || parent === ".") {
      return;
    }
    const canonMain = canonicalPath(mainRoot);
    if (
      parent === canonMain ||
      parent === canonicalPath(path.dirname(canonMain))
    ) {
      return;
    }
    try {
      rmdirSync(parent);
    } catch {
      // Only removes if empty.
    }
  }

  private async lookupWorktree(
    wtPath: string,
  ): Promise<{ repo: RepoInfo; wt: WorktreeEntry }> {
    const trimmed = wtPath.trim();
    if (!trimmed) {
      throw new Error("worktree: path is empty");
    }
    const canonTarget = canonicalPath(trimmed);

    let discoverDir = "";
    if (isDirSync(canonTarget)) {
      discoverDir = canonTarget;
    } else {
      const rec = this.store.getWorktreeByPath(canonTarget);
      if (rec?.main_repo_root) {
        discoverDir = rec.main_repo_root;
      } else {
        throw new Error(
          `worktree: path ${wtPath} does not exist and is not tracked`,
        );
      }
    }

    const { repo, worktrees } = await this.list(discoverDir);
    for (const wt of worktrees) {
      if (canonicalPath(wt.path) === canonTarget) {
        return { repo, wt };
      }
    }
    throw new Error(
      `worktree: ${wtPath} is not a registered worktree of ${repo.main_root}`,
    );
  }
}

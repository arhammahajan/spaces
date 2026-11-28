import { afterEach, describe, expect, test } from "bun:test";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import { CliGitDriver, canonicalPath, decodeUtf8 } from "../git/driver";
import { SpacesStore } from "../store/db";
import { WorktreeManager, pathWithin } from "./manager";
import {
  NAMING_ADJECTIVES,
  NAMING_NOUNS,
  sanitizeBranchName,
  splitMix64,
  suggestName,
  validateBranchName,
  worktreeDirName,
} from "./naming";

const tempDirs: string[] = [];

function makeTempDir(prefix = "spaces-wt-test-"): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

function runGitSync(cwd: string, args: string[]): string {
  const proc = Bun.spawnSync(["git", ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "Test User",
      GIT_AUTHOR_EMAIL: "test@example.com",
      GIT_COMMITTER_NAME: "Test User",
      GIT_COMMITTER_EMAIL: "test@example.com",
    },
  });
  if (proc.exitCode !== 0) {
    throw new Error(
      `git ${args.join(" ")} failed (${proc.exitCode}): ${new TextDecoder().decode(proc.stderr)}`,
    );
  }
  return new TextDecoder().decode(proc.stdout);
}

function initTempRepo(parentDir: string, name: string, branch = "main"): string {
  const dir = path.join(parentDir, name);
  mkdirSync(dir, { recursive: true });
  runGitSync(dir, ["init", "-b", branch]);
  runGitSync(dir, ["config", "user.name", "Test User"]);
  runGitSync(dir, ["config", "user.email", "test@example.com"]);
  writeFileSync(path.join(dir, "README.md"), `# ${name}\n`);
  runGitSync(dir, ["add", "."]);
  runGitSync(dir, ["commit", "-m", "initial commit"]);
  return dir;
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop()!;
    rmSync(dir, { recursive: true, force: true });
  }
});

describe("worktree naming parity", () => {
  test("wordlists have 64 adjectives and 64 nouns", () => {
    expect(NAMING_ADJECTIVES.length).toBe(64);
    expect(NAMING_NOUNS.length).toBe(64);
  });

  test("sanitizeBranchName, worktreeDirName, and validateBranchName", () => {
    expect(sanitizeBranchName("  Feat/My Branch!!  ")).toBe("feat/my-branch");
    expect(sanitizeBranchName("foo..bar.lock")).toBe("foo-bar");
    expect(sanitizeBranchName("@")).toBe("");

    expect(worktreeDirName("feat/auth-fix")).toBe("feat-auth-fix");
    expect(worktreeDirName("")).toBe("worktree");
    expect(worktreeDirName("a".repeat(80)).length).toBe(64);

    expect(validateBranchName("feat/good-branch_1.0")).toBeNull();
    expect(validateBranchName("")).not.toBeNull();
    expect(validateBranchName("@")).not.toBeNull();
    expect(validateBranchName("-leading")).not.toBeNull();
    expect(validateBranchName("a..b")).not.toBeNull();
    expect(validateBranchName("a@{b")).not.toBeNull();
    expect(validateBranchName("foo/.hidden")).not.toBeNull();
    expect(validateBranchName("foo/bar.lock")).not.toBeNull();
    expect(validateBranchName("has space")).not.toBeNull();
  });

  test("6-tier suggestName priority, prefix preservation, and collision disambiguation", () => {
    // Tier 1: Explicit (never prefixed or auto-suffixed)
    const t1 = suggestName(
      {
        explicit_name: "My/Custom Branch",
        target_name: "ignored",
        prefix: "feat/",
      },
      new Set(["my/custom-branch"]),
    );
    expect(t1).toEqual({
      branch: "my/custom-branch",
      dir_name: "my-custom-branch",
      source: "custom",
      rule: "explicit",
    });

    // Tier 2: Meaningful target name with prefix & collision disambiguation
    const t2 = suggestName(
      {
        target_name: "✳ Auth Refactor",
        target_kind: "tab",
        repo_name: "spaces",
        current_branch: "main",
        prefix: "feat/",
      },
      new Set(["feat/auth-refactor"]),
    );
    expect(t2.branch).toBe("feat/auth-refactor-2");
    expect(t2.dir_name).toBe("feat-auth-refactor-2");
    expect(t2.source).toBe("from tab name");
    expect(t2.rule).toBe("target_name");

    // Tier 3: Harness title (generic target "zsh" skipped; harness prefix & trailing cwd stripped)
    const t3 = suggestName({
      target_name: "zsh",
      harness_app: "claude-code",
      harness_title: "⠋ Claude Code - Fix SQLite WAL checkpoint | ~/dev/spaces",
      repo_name: "spaces",
      current_branch: "main",
    });
    expect(t3.branch).toBe("fix-sqlite-wal-checkpoint");
    expect(t3.source).toBe("from claude title");
    expect(t3.rule).toBe("harness_title");

    // Tier 4: Harness prompt (target_name matching harness_app short name and boilerplate title skipped)
    const t4 = suggestName({
      target_name: "agy",
      harness_app: "antigravity",
      harness_title: "Claude Code",
      harness_prompt:
        "Please help me implement [OAuth token refresh](https://example.com/docs) for the CLI.",
      repo_name: "spaces",
      current_branch: "main",
      prefix: "dev-",
    });
    expect(t4.branch).toBe("dev-implement-oauth-token-refresh-cli");
    expect(t4.source).toBe("from agy prompt");
    expect(t4.rule).toBe("harness_prompt");

    // Tier 5: Known coding agent fallback when seed > 0 (or no title/prompt)
    const t5a = suggestName({
      target_name: "Auth Refactor",
      harness_app: "antigravity",
      seed: 1,
    });
    expect(t5a.rule).toBe("harness_agent");
    expect(t5a.source).toBe("from agy");
    expect(t5a.branch.startsWith("agy-")).toBe(true);

    // Deterministic for same seed, advances on collision via stride 137
    const t5b = suggestName(
      { harness_app: "antigravity", seed: 1 },
      new Set([t5a.branch]),
    );
    expect(t5b.branch).not.toBe(t5a.branch);
    expect(t5b.branch.startsWith("agy-")).toBe(true);

    // Tier 6: Curated random fallback
    const t6 = suggestName({ target_name: "zsh", seed: 42 });
    expect(t6.rule).toBe("fallback");
    expect(t6.source).toBe("auto");
    expect(t6.branch).toMatch(/^[a-z]+-[a-z]+$/);

    // Verify splitMix64 deterministic output matches 64-bit unsigned spec
    expect(splitMix64(1n)).toBe(0x910a2dec89025cc1n);
  });
});

describe("WorktreeManager lifecycle (real git repo + SQLite SpacesStore)", () => {
  test("create, list, inspectSafety, resolveWorktreePath, getContext, remove, and bucket collision", async () => {
    const tmp = makeTempDir("spaces-wt-mgr-");
    const wtRoot = path.join(tmp, "worktrees");
    const dbPath = path.join(tmp, "spaces.db");
    const store = new SpacesStore(dbPath);

    try {
      const repoA = initTempRepo(path.join(tmp, "a"), "my-app", "main");
      const repoB = initTempRepo(path.join(tmp, "b"), "my-app", "main");

      const mgr = new WorktreeManager({ store, worktreeRoot: wtRoot });

      // Unborn repo guard
      const unborn = path.join(tmp, "unborn");
      mkdirSync(unborn, { recursive: true });
      runGitSync(unborn, ["init", "-b", "main"]);
      await expect(
        mgr.create({ repo_dir: unborn, branch: "feat/init" }),
      ).rejects.toThrow("no commits yet");

      // Create worktree in repoA
      const createdA = await mgr.create({
        repo_dir: repoA,
        branch: "feat/login",
        base_ref: "main",
        origin_space: "sp-1",
      });
      expect(createdA.created_branch).toBe(true);
      expect(createdA.worktree.branch).toBe("feat/login");
      expect(createdA.worktree.managed).toBe(true);
      expect(existsSync(createdA.worktree.path)).toBe(true);

      // Verify branch.<branch>.spaces-base was written to local git config
      const driver = new CliGitDriver();
      const cfgRes = await driver.execGit(repoA, [
        "config",
        "--local",
        "branch.feat/login.spaces-base",
      ]);
      expect(cfgRes.exitCode).toBe(0);
      expect(decodeUtf8(cfgRes.stdout).trim()).toBe("main");

      // Checked-out branch guard
      await expect(
        mgr.create({ repo_dir: repoA, branch: "feat/login" }),
      ).rejects.toThrow("already checked out");

      // Repo bucket collision: repoB has same repo_name ("my-app") -> gets -<6-hex sha256> bucket
      const createdB = await mgr.create({
        repo_dir: repoB,
        branch: "feat/login",
      });
      const bucketA = path.basename(path.dirname(createdA.worktree.path));
      const bucketB = path.basename(path.dirname(createdB.worktree.path));
      expect(bucketA).toBe("my-app");
      expect(bucketB).toMatch(/^my-app-[0-9a-f]{6}$/);

      // getContext in repo vs non-repo
      const ctx = await mgr.getContext(repoA);
      expect(ctx.in_repo).toBe(true);
      expect(ctx.worktrees.length).toBe(2);
      expect(ctx.branches).toContain("feat/login");
      expect(ctx.auto_branch.length).toBeGreaterThan(0);

      const nonRepoCtx = await mgr.getContext(tmp);
      expect(nonRepoCtx.in_repo).toBe(false);

      // resolveWorktreePath by branch, name, and case-insensitive
      expect(await mgr.resolveWorktreePath(repoA, "feat/login")).toBe(
        createdA.worktree.path,
      );
      expect(await mgr.resolveWorktreePath(repoA, "FEAT/LOGIN")).toBe(
        createdA.worktree.path,
      );

      // inspectSafety: clean linked worktree vs main vs dirty + unmerged
      const cleanSafety = await mgr.inspectSafety(createdA.worktree.path);
      expect(cleanSafety.can_remove_safely).toBe(true);
      expect(cleanSafety.warnings).toEqual([]);

      const mainSafety = await mgr.inspectSafety(repoA);
      expect(mainSafety.can_remove_safely).toBe(false);
      expect(mainSafety.is_main).toBe(true);

      writeFileSync(path.join(createdA.worktree.path, "dirty.txt"), "change\n");
      const dirtySafety = await mgr.inspectSafety(createdA.worktree.path);
      expect(dirtySafety.can_remove_safely).toBe(false);
      expect(dirtySafety.is_dirty).toBe(true);

      // Refuse removing main worktree
      await expect(mgr.remove(repoA, { force: true })).rejects.toThrow(
        "refusing to remove main",
      );

      // Remove linked worktree with force + delete_branch, verify empty parent bucket cleaned up
      const parentBucketA = path.dirname(createdA.worktree.path);
      await mgr.remove(createdA.worktree.path, {
        force: true,
        delete_branch: true,
        force_branch: true,
      });
      expect(existsSync(createdA.worktree.path)).toBe(false);
      expect(existsSync(parentBucketA)).toBe(false);
      expect(store.getWorktreeByPath(createdA.worktree.path)).toBeNull();
      expect(await mgr.listBranches(repoA)).not.toContain("feat/login");
    } finally {
      store.close();
    }
  });

  test("create rolls back worktree and newly created branch when store persistence fails", async () => {
    const tmp = makeTempDir("spaces-wt-rb-");
    const wtRoot = path.join(tmp, "worktrees");
    const repo = initTempRepo(tmp, "rb-repo", "main");
    const store = new SpacesStore(path.join(tmp, "spaces.db"));

    try {
      // Simulate persistence failure by closing the SQLite DB before create
      const failingStore = Object.create(store) as SpacesStore;
      failingStore.upsertWorktree = () => {
        throw new Error("disk full");
      };

      const mgr = new WorktreeManager({
        store: failingStore,
        worktreeRoot: wtRoot,
      });

      await expect(
        mgr.create({ repo_dir: repo, branch: "feat/rollback-me" }),
      ).rejects.toThrow("persist metadata: disk full");

      // Worktree directory and newly created branch must both be rolled back
      expect(existsSync(path.join(wtRoot, "rb-repo", "feat-rollback-me"))).toBe(
        false,
      );
      expect(await mgr.listBranches(repo)).not.toContain("feat/rollback-me");
    } finally {
      store.close();
    }
  });

  test("prune removes orphaned clean merged worktrees while protecting active paths via pathWithin", async () => {
    const tmp = makeTempDir("spaces-wt-prune-");
    const wtRoot = path.join(tmp, "worktrees");
    const repo = initTempRepo(tmp, "prune-repo", "main");
    const store = new SpacesStore(path.join(tmp, "spaces.db"));

    try {
      const mgr = new WorktreeManager({ store, worktreeRoot: wtRoot });

      const activeWt = await mgr.create({
        repo_dir: repo,
        branch: "feat/active",
      });
      const orphanWt = await mgr.create({
        repo_dir: repo,
        branch: "feat/orphan",
      });
      const goneWt = await mgr.create({
        repo_dir: repo,
        branch: "feat/gone",
      });

      // Manually delete goneWt directory to simulate a stale/missing worktree
      rmSync(goneWt.worktree.path, { recursive: true, force: true });

      // Active path is a subdirectory inside activeWt -> pathWithin must protect it
      const activeSubdir = path.join(activeWt.worktree.path, "sub", "dir");
      mkdirSync(activeSubdir, { recursive: true });
      expect(
        pathWithin(canonicalPath(activeSubdir), canonicalPath(activeWt.worktree.path)),
      ).toBe(true);

      const pruneRes = await mgr.prune({
        repo_dirs: [repo],
        remove_orphaned_merged: true,
        active_paths: [activeSubdir],
      });

      expect(pruneRes.pruned_git).toBeGreaterThanOrEqual(1);
      expect(pruneRes.pruned_records).toBe(1);
      expect(pruneRes.removed_paths).toEqual([orphanWt.worktree.path]);
      expect(existsSync(activeWt.worktree.path)).toBe(true);
      expect(existsSync(orphanWt.worktree.path)).toBe(false);
    } finally {
      store.close();
    }
  });
});

import { afterEach, describe, expect, test } from "bun:test";
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import type { GitInfo, Space } from "@spaces/types";
import { GitCollector } from "./collector";
import { orderDiffFiles, parseUnifiedDiff, unquoteGitPatchPath } from "./diff-parser";
import {
  CliGitDriver,
  canonicalPath,
  countTextLines,
  encodeUtf8,
  parseNumstat,
  parsePorcelainV2,
  type GitCommandRunner,
} from "./driver";

const tempDirs: string[] = [];

function makeTempDir(prefix = "spaces-git-test-"): string {
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

function initTempRepo(initialBranch = "main"): string {
  const dir = makeTempDir("spaces-repo-");
  runGitSync(dir, ["init", "-b", initialBranch]);
  runGitSync(dir, ["config", "user.name", "Test User"]);
  runGitSync(dir, ["config", "user.email", "test@example.com"]);
  writeFileSync(path.join(dir, "README.md"), "# Hello\nline 2\n");
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

describe("git driver helpers", () => {
  test("canonicalPath resolves symlinks even when leaf does not exist yet", () => {
    const root = makeTempDir();
    const realTarget = path.join(root, "real-dir");
    const linkPath = path.join(root, "link-dir");
    mkdirSync(realTarget, { recursive: true });
    symlinkSync(realTarget, linkPath);

    const resolvedExisting = canonicalPath(linkPath);
    expect(resolvedExisting).toBe(canonicalPath(realTarget));

    const futureLeaf = path.join(linkPath, "nested", "new-wt");
    expect(canonicalPath(futureLeaf)).toBe(
      path.join(canonicalPath(realTarget), "nested", "new-wt"),
    );
  });

  test("parsePorcelainV2 handles branch headers, ahead/behind, renames (2 extra NUL field), and untracked", () => {
    const raw = [
      "# branch.oid abc123",
      "# branch.head feat/demo",
      "# branch.upstream origin/feat/demo",
      "# branch.ab +3 -2",
      "1 .M N... 100644 100644 100644 aaa bbb src/a.ts",
      "2 R. N... 100644 100644 100644 aaa bbb R100 src/new.ts",
      "src/old.ts", // extra NUL record belonging to rename '2'
      "? untracked.txt",
      "",
    ].join("\0");

    const obs = parsePorcelainV2(encodeUtf8(raw));
    expect(obs.branch).toBe("feat/demo");
    expect(obs.upstream).toBe(true);
    expect(obs.ahead).toBe(3);
    expect(obs.behind).toBe(2);
    expect(obs.dirty).toBe(true);
    expect(obs.filesAdded).toBe(1);
    expect(obs.filesModified).toBe(2);
  });

  test("parseNumstat skips binary entries and sums additions/removals", () => {
    const out = "12\t4\tsrc/a.ts\n-\t-\tassets/logo.png\n3\t1\tsrc/b.ts\n";
    expect(parseNumstat(out)).toEqual({ added: 15, removed: 5 });
  });

  test("countTextLines counts text lines and skips NUL binaries", () => {
    const dir = makeTempDir();
    const textWithTrail = path.join(dir, "a.txt");
    const textNoTrail = path.join(dir, "b.txt");
    const binaryFile = path.join(dir, "c.bin");
    writeFileSync(textWithTrail, "one\ntwo\nthree\n");
    writeFileSync(textNoTrail, "one\ntwo");
    writeFileSync(binaryFile, Buffer.from([0x41, 0x00, 0x42, 0x0a]));

    expect(countTextLines(textWithTrail)).toBe(3);
    expect(countTextLines(textNoTrail)).toBe(2);
    expect(countTextLines(binaryFile)).toBe(0);
  });
});

describe("CliGitDriver (mock runner)", () => {
  test("getStatus parses worktree, rebase, base config, conflict, and numstat via mock runner", async () => {
    const root = makeTempDir();
    const repoDir = path.join(root, "wt-feat");
    const gitDir = path.join(root, "main-repo", ".git", "worktrees", "wt-feat");
    const commonDir = path.join(root, "main-repo", ".git");
    mkdirSync(path.join(gitDir, "rebase-merge"), { recursive: true });
    mkdirSync(repoDir, { recursive: true });
    mkdirSync(commonDir, { recursive: true });

    const calls: string[][] = [];
    const runner: GitCommandRunner = async (args) => {
      calls.push(args);
      const sub = args.slice(1); // strip --no-optional-locks
      if (sub[0] === "rev-parse" && sub[1] === "--show-toplevel") {
        return {
          stdout: encodeUtf8(`${repoDir}\n${commonDir}\n${gitDir}\n`),
          stderr: new Uint8Array(0),
          exitCode: 0,
        };
      }
      if (sub[0] === "status") {
        const out = [
          "# branch.head feat/x",
          "# branch.upstream origin/feat/x",
          "# branch.ab +2 -1",
          "1 M. N... 100644 100644 100644 a b foo.ts",
          "",
        ].join("\0");
        return { stdout: encodeUtf8(out), stderr: new Uint8Array(0), exitCode: 0 };
      }
      if (sub[0] === "config" && sub[2] === "branch.feat/x.spaces-base") {
        return { stdout: encodeUtf8("develop\n"), stderr: new Uint8Array(0), exitCode: 0 };
      }
      if (sub[0] === "diff" && sub.includes("develop...HEAD")) {
        return {
          stdout: encodeUtf8("10\t2\tfoo.ts\n"),
          stderr: new Uint8Array(0),
          exitCode: 0,
        };
      }
      if (sub[0] === "merge-tree") {
        return { stdout: new Uint8Array(0), stderr: new Uint8Array(0), exitCode: 1 };
      }
      if (sub[0] === "diff" && sub.includes("HEAD")) {
        return {
          stdout: encodeUtf8("4\t1\tfoo.ts\n"),
          stderr: new Uint8Array(0),
          exitCode: 0,
        };
      }
      if (sub[0] === "ls-files") {
        return { stdout: new Uint8Array(0), stderr: new Uint8Array(0), exitCode: 0 };
      }
      return { stdout: new Uint8Array(0), stderr: new Uint8Array(0), exitCode: 1 };
    };

    const driver = new CliGitDriver({ runner });
    const status = await driver.getStatus(repoDir);

    expect(status).not.toBeNull();
    expect(status!.is_worktree).toBe(true);
    expect(status!.worktree_name).toBe("wt-feat");
    expect(status!.repo_name).toBe("main-repo");
    expect(status!.is_rebasing).toBe(true);
    expect(status!.branch).toBe("feat/x");
    expect(status!.base_branch).toBe("develop");
    expect(status!.ahead).toBe(2);
    expect(status!.behind).toBe(1);
    expect(status!.lines_added).toBe(10);
    expect(status!.lines_removed).toBe(2);
    expect(status!.has_conflict).toBe(true);
    expect(status!.is_dirty).toBe(true);
    expect(status!.uncommitted_added).toBe(4);
    expect(status!.uncommitted_removed).toBe(1);
    expect(calls.every((c) => c[0] === "--no-optional-locks")).toBe(true);
  });
});

describe("CliGitDriver (real temp git repo)", () => {
  test("discoverRepo handles normal, unborn, and bare repos", async () => {
    const driver = new CliGitDriver();

    // Unborn repo
    const unborn = makeTempDir("spaces-unborn-");
    runGitSync(unborn, ["init", "-b", "main"]);
    const unbornInfo = await driver.discoverRepo(unborn);
    expect(unbornInfo).not.toBeNull();
    expect(unbornInfo!.has_commits).toBe(false);
    expect(unbornInfo!.current_branch).toBe("main");
    expect(unbornInfo!.default_base).toBe("main");

    // Normal repo
    const normal = initTempRepo("main");
    const normalInfo = await driver.discoverRepo(normal);
    expect(normalInfo).not.toBeNull();
    expect(normalInfo!.has_commits).toBe(true);
    expect(normalInfo!.current_branch).toBe("main");
    expect(normalInfo!.default_base).toBe("main");
    expect(normalInfo!.main_root).toBe(canonicalPath(normal));

    // Bare repo
    const bare = makeTempDir("spaces-bare-");
    runGitSync(bare, ["init", "--bare", "-b", "main"]);
    const bareInfo = await driver.discoverRepo(bare);
    expect(bareInfo).not.toBeNull();
    expect(bareInfo!.current_worktree_path).toBe("");
    expect(bareInfo!.main_root).toBe(canonicalPath(bare));
  });

  test("getStatus, getRemoteUrl, listBranches, and getDiffRaw across scopes", async () => {
    const repo = initTempRepo("main");
    runGitSync(repo, ["remote", "add", "origin", "https://github.com/acme/demo.git"]);

    // Create feature branch with committed change
    runGitSync(repo, ["checkout", "-b", "feat/demo"]);
    writeFileSync(path.join(repo, "app.ts"), "export const x = 1;\nexport const y = 2;\n");
    runGitSync(repo, ["add", "app.ts"]);
    runGitSync(repo, ["commit", "-m", "add app.ts"]);

    // Add staged + unstaged + untracked + symlink changes
    writeFileSync(path.join(repo, "app.ts"), "export const x = 10;\nexport const y = 2;\n");
    runGitSync(repo, ["add", "app.ts"]);
    writeFileSync(path.join(repo, "app.ts"), "export const x = 10;\nexport const y = 20;\n");
    writeFileSync(path.join(repo, "notes.txt"), "hello\nworld\n");
    symlinkSync(path.join(repo, "README.md"), path.join(repo, "readme-link"));

    const driver = new CliGitDriver();
    expect(await driver.getRemoteUrl(repo)).toBe("https://github.com/acme/demo.git");
    expect((await driver.listBranches(repo)).sort()).toEqual(["feat/demo", "main"]);

    const status = await driver.getStatus(repo);
    expect(status).not.toBeNull();
    expect(status!.branch).toBe("feat/demo");
    expect(status!.base_branch).toBe("main");
    expect(status!.lines_added).toBe(2);
    expect(status!.lines_removed).toBe(0);
    expect(status!.is_dirty).toBe(true);
    // 2 modified lines in app.ts + 2 lines in notes.txt + 1 for symlink = 5
    expect(status!.uncommitted_added).toBe(5);
    expect(status!.uncommitted_removed).toBe(2);

    const staged = await driver.getDiffRaw(repo, { scope: "staged", contextLines: 1 });
    expect(staged.raw).toContain("-export const x = 1;");
    expect(staged.raw).toContain("+export const x = 10;");

    const unstaged = await driver.getDiffRaw(repo, { scope: "unstaged" });
    expect(unstaged.raw).toContain("-export const y = 2;");
    expect(unstaged.raw).toContain("+export const y = 20;");

    const wt = await driver.getDiffRaw(repo, { scope: "working_tree", paths: ["app.ts"] });
    expect(wt.raw).toContain("+export const x = 10;");
    expect(wt.raw).toContain("+export const y = 20;");

    const branchBase = await driver.getDiffRaw(repo, { scope: "branch_base" });
    expect(branchBase.baseRef).toBe("main");
    expect(branchBase.raw).toContain("a/app.ts");

    const range = await driver.getDiffRaw(repo, {
      scope: "commit_range",
      baseRef: "main",
      headRef: "HEAD",
    });
    expect(range.raw).toContain("new file mode");
  });
});

describe("parseUnifiedDiff", () => {
  test("parses modified, added, deleted, renamed, binary, section headings, and no-newline-at-EOF", () => {
    const raw = [
      "diff --git a/package-lock.json b/package-lock.json",
      "index 111..222 100644",
      "--- a/package-lock.json",
      "+++ b/package-lock.json",
      "@@ -1,2 +1,2 @@",
      "-old",
      "+new",
      "diff --git a/src/foo.test.ts b/src/foo.test.ts",
      "new file mode 100644",
      "--- /dev/null",
      "+++ b/src/foo.test.ts",
      "@@ -0,0 +1,2 @@",
      "+import { foo } from './foo';",
      "+expect(foo()).toBe(1);",
      "\\ No newline at end of file",
      "diff --git a/src/foo.ts b/src/foo.ts",
      "index 111..222 100644",
      "--- a/src/foo.ts",
      "+++ b/src/foo.ts",
      "@@ -10,3 +10,4 @@ export function foo()",
      " const a = 1;",
      "-const b = 2;",
      "+const b = 3;",
      "+const c = 4;",
      " return a + b;",
      "diff --git a/src/old.ts b/src/renamed.ts",
      "similarity index 90%",
      "rename from src/old.ts",
      "rename to src/renamed.ts",
      "diff --git a/assets/icon.png b/assets/icon.png",
      "Binary files a/assets/icon.png and b/assets/icon.png differ",
      "diff --git a/obsolete.ts b/obsolete.ts",
      "deleted file mode 100644",
      "--- a/obsolete.ts",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-bye",
      "",
    ].join("\n");

    const snap = parseUnifiedDiff(raw, {
      scope: "branch_base",
      baseRef: "main",
      headRef: "HEAD",
    });

    expect(snap.scope).toBe("branch_base");
    expect(snap.base_ref).toBe("main");
    expect(snap.head_ref).toBe("HEAD");
    expect(snap.truncated).toBe(false);
    expect(snap.total_additions).toBe(5);
    expect(snap.total_deletions).toBe(3);

    // Deterministic review ordering: source before test before generated (lockfile last)
    expect(snap.files[snap.files.length - 1]!.path).toBe("package-lock.json");
    const fooIdx = snap.files.findIndex((f) => f.path === "src/foo.ts");
    const testIdx = snap.files.findIndex((f) => f.path === "src/foo.test.ts");
    expect(fooIdx).toBeLessThan(testIdx);

    const foo = snap.files[fooIdx]!;
    expect(foo.status).toBe("modified");
    expect(foo.hunks[0]!.section_heading).toBe("export function foo()");
    expect(foo.hunks[0]!.lines[0]).toEqual({
      kind: "context",
      content: "const a = 1;",
      old_lineno: 10,
      new_lineno: 10,
    });
    expect(foo.hunks[0]!.lines[1]).toEqual({
      kind: "del",
      content: "const b = 2;",
      old_lineno: 11,
    });
    expect(foo.hunks[0]!.lines[2]).toEqual({
      kind: "add",
      content: "const b = 3;",
      new_lineno: 11,
    });

    const testFile = snap.files[testIdx]!;
    expect(testFile.status).toBe("added");
    expect(testFile.hunks[0]!.lines[1]!.no_newline_at_eof).toBe(true);

    const renamed = snap.files.find((f) => f.path === "src/renamed.ts")!;
    expect(renamed.status).toBe("renamed");
    expect(renamed.old_path).toBe("src/old.ts");

    const binary = snap.files.find((f) => f.path === "assets/icon.png")!;
    expect(binary.is_binary).toBe(true);

    const deleted = snap.files.find((f) => f.path === "obsolete.ts")!;
    expect(deleted.status).toBe("deleted");
    expect(deleted.deletions).toBe(1);
  });

  test("unquotes octal UTF-8 git patch paths and respects maxBytes truncation guard", () => {
    expect(unquoteGitPatchPath('"caf\\303\\251.txt"')).toBe("café.txt");

    const bigDiff = [
      "diff --git a/a.ts b/a.ts",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1,1 +1,10 @@",
      "+line 1",
      "+line 2",
      "+line 3",
      "+line 4",
      "+line 5",
      "+line 6",
    ].join("\n");

    const snap = parseUnifiedDiff(bigDiff, {
      scope: "working_tree",
      maxBytes: 90,
    });
    expect(snap.truncated).toBe(true);
    expect(snap.files.length).toBe(1);
    expect(snap.files[0]!.truncated).toBe(true);
  });

  test("orderDiffFiles keeps sibling files in the same directory contiguous", () => {
    const files = orderDiffFiles([
      { path: "z/one.ts", status: "modified", is_binary: false, additions: 1, deletions: 0, hunks: [], truncated: false },
      { path: "a/alpha.ts", status: "modified", is_binary: false, additions: 1, deletions: 0, hunks: [], truncated: false },
      { path: "a/alpha.test.ts", status: "modified", is_binary: false, additions: 1, deletions: 0, hunks: [], truncated: false },
      { path: "package-lock.json", status: "modified", is_binary: false, additions: 1, deletions: 0, hunks: [], truncated: false },
    ]);
    expect(files.map((f) => f.path)).toEqual([
      "a/alpha.ts",
      "a/alpha.test.ts",
      "z/one.ts",
      "package-lock.json",
    ]);
  });
});

describe("GitCollector", () => {
  function makeGitInfo(repoKey: string, root: string, branch: string): GitInfo {
    return {
      repo_key: repoKey,
      repo_root: root,
      main_repo_root: root,
      repo_name: path.basename(root),
      is_worktree: false,
      worktree_name: "",
      managed: false,
      branch,
      base_branch: "main",
      ahead: 0,
      behind: 0,
      is_dirty: false,
      is_rebasing: false,
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
  }

  test("caches git status by TTL, bounds worker concurrency, and attaches cached info to spaces/panes", async () => {
    let nowMs = 1_000;
    let activeWorkers = 0;
    let maxObserved = 0;
    let calls = 0;

    const fakeDriver = {
      execGit: async () => ({ stdout: new Uint8Array(0), stderr: new Uint8Array(0), exitCode: 0 }),
      discoverRepo: async () => null,
      getRemoteUrl: async () => null,
      listBranches: async () => [],
      getDiffRaw: async () => ({ raw: "" }),
      getStatus: async (cwd: string) => {
        calls += 1;
        activeWorkers += 1;
        maxObserved = Math.max(maxObserved, activeWorkers);
        await Bun.sleep(10);
        activeWorkers -= 1;
        return makeGitInfo(`${cwd}/.git`, cwd, "feat/x");
      },
    };

    const collector = new GitCollector({
      driver: fakeDriver,
      now: () => nowMs,
      gitTtlMs: 5_000,
      maxWorkers: 2,
    });

    const cwds = ["/repo/1", "/repo/2", "/repo/3", "/repo/4", "/repo/1"];
    const first = await collector.fetchGit(cwds, false);
    expect(first.size).toBe(4);
    expect(calls).toBe(4);
    expect(maxObserved).toBeLessThanOrEqual(2);

    // Within TTL: zero extra calls
    nowMs += 2_000;
    const second = await collector.fetchGit(cwds, false);
    expect(second.size).toBe(4);
    expect(calls).toBe(4);

    // Force bypasses TTL
    await collector.fetchGit(["/repo/1"], true);
    expect(calls).toBe(5);

    // attachCached populates space & pane git without I/O
    const mockSpaces = [
      {
        name: "sp1",
        cwd: "/repo/1",
        git: null,
        panes: [{ cwd: "/repo/2", git: null }],
      },
    ] as unknown as Space[];
    collector.attachCached(mockSpaces);
    expect(mockSpaces[0]!.git?.repo_root).toBe("/repo/1");
    expect(mockSpaces[0]!.panes[0]!.git?.repo_root).toBe("/repo/2");
  });

  test("fetchForge caches PR/checks by TTL and backs off on repo errors", async () => {
    let nowMs = 10_000;
    let lookupCalls = 0;
    let shouldFail = true;

    const collector = new GitCollector({
      now: () => nowMs,
      prTtlMs: 30_000,
      repoErrTtlMs: 60_000,
      forgeLookup: async (_cwd, branch) => {
        lookupCalls += 1;
        if (shouldFail) {
          throw new Error("gh not authenticated");
        }
        return {
          pr: {
            number: 42,
            title: `PR for ${branch}`,
            state: "OPEN",
            is_draft: false,
            url: "https://github.com/acme/demo/pull/42",
          },
          checks: {
            state: "success",
            passed: 3,
            total: 3,
            failing_name: "",
          },
        };
      },
    });

    const gitByCwd = new Map<string, GitInfo>([
      ["/repo/a", makeGitInfo("/repo/a/.git", "/repo/a", "feat/a")],
    ]);

    // First call fails and caches error backoff
    await collector.fetchForge(gitByCwd, false);
    expect(lookupCalls).toBe(1);

    // Within repoErrTtlMs: skipped
    nowMs += 10_000;
    await collector.fetchForge(gitByCwd, false);
    expect(lookupCalls).toBe(1);

    // Force retries and succeeds
    shouldFail = false;
    const enriched = await collector.fetchForge(gitByCwd, true);
    expect(lookupCalls).toBe(2);
    expect(enriched.get("/repo/a")?.pr?.number).toBe(42);
    expect(enriched.get("/repo/a")?.checks?.state).toBe("success");

    // Within prTtlMs: cached
    nowMs += 5_000;
    const cached = await collector.fetchForge(gitByCwd, false);
    expect(lookupCalls).toBe(2);
    expect(cached.get("/repo/a")?.pr?.number).toBe(42);
  });
});

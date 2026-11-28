import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  type BackendHookDetailResult,
  type BackendSharedListResult,
  type BackendStatusBarResult,
  type BackendWorktreesListResult,
  type GitDiffResult,
  type GitStatusResult,
  type HookEmitResult,
  type RpcResponse,
  type WorktreeCreateResult,
  type WorktreeInspectSafetyResult,
  type WorktreePruneResult,
} from "@spaces/types";
import type { DriverPane, DriverSnapshot, MultiplexerDriver } from "../drivers/types";
import { SpacesDaemon } from "./daemon";

interface UdsClient {
  request<T = unknown>(method: string, params?: unknown): Promise<RpcResponse & { result?: T }>;
  close(): void;
}

async function connectUds(socketPath: string): Promise<UdsClient> {
  let nextId = 1;
  let buffer = "";
  const pending = new Map<number, (resp: RpcResponse) => void>();

  const socket = await Bun.connect({
    unix: socketPath,
    socket: {
      data(_socket, chunk) {
        buffer += Buffer.from(chunk).toString("utf8");
        let idx = buffer.indexOf("\n");
        while (idx !== -1) {
          const line = buffer.slice(0, idx).trim();
          buffer = buffer.slice(idx + 1);
          if (line.length > 0) {
            const msg = JSON.parse(line) as RpcResponse;
            if (msg.id !== undefined) {
              const resolver = pending.get(Number(msg.id));
              if (resolver) {
                pending.delete(Number(msg.id));
                resolver(msg);
              }
            }
          }
          idx = buffer.indexOf("\n");
        }
      },
      open() {},
      close() {},
      error() {},
    },
  });

  return {
    request<T = unknown>(method: string, params?: unknown) {
      const id = nextId++;
      return new Promise<RpcResponse & { result?: T }>((resolve) => {
        pending.set(id, resolve as (resp: RpcResponse) => void);
        socket.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
      });
    },
    close() {
      socket.end();
    },
  };
}

function runGit(cwd: string, args: string[]): void {
  const proc = Bun.spawnSync(["git", ...args], {
    cwd,
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "Spaces Test",
      GIT_AUTHOR_EMAIL: "test@spaces.local",
      GIT_COMMITTER_NAME: "Spaces Test",
      GIT_COMMITTER_EMAIL: "test@spaces.local",
    },
    stdout: "pipe",
    stderr: "pipe",
  });
  if (proc.exitCode !== 0) {
    throw new Error(`git ${args.join(" ")} failed: ${proc.stderr.toString()}`);
  }
}

function makeFakeDriver(panes: DriverPane[]): MultiplexerDriver {
  const snapshot: DriverSnapshot = {
    sessions: [
      {
        id: "$1",
        name: "dev",
        createdAtSec: 1_700_000_000,
        attached: 1,
        windowCount: 1,
        worktreePath: "",
        worktreeName: "",
        worktreeBase: "",
        worktreeManaged: false,
      },
    ],
    windows: [
      {
        sessionName: "dev",
        id: "@1",
        index: 0,
        name: "main",
        active: true,
        last: false,
        zoomed: false,
        bell: false,
        activity: false,
        silence: false,
        width: 120,
        height: 40,
        activityAtSec: 1_700_000_000,
        layout: "",
        worktreePath: "",
        worktreeName: "",
        worktreeBase: "",
        worktreeManaged: false,
      },
    ],
    panes,
    currentSession: "dev",
    currentPaneId: "%1",
  };
  return {
    kind: "tmux",
    serverRunning: async () => true,
    currentSession: async () => "dev",
    hasClients: async () => true,
    listSessions: async () => snapshot.sessions,
    listWindows: async () => snapshot.windows,
    listPanes: async () => snapshot.panes,
    snapshot: async () => snapshot,
    capturePane: async () => "",
    capturePaneStyled: async () => "",
    switchToSpace: async () => {},
    focusPane: async () => {},
    newSession: async () => {},
    renameSession: async () => {},
    killSession: async () => {},
    newWindow: async () => {},
    renameWindow: async () => {},
    killWindow: async () => {},
    splitWindow: async () => {},
    selectLayout: async () => {},
    selectWindow: async () => {},
    selectPane: async () => {},
    resizePaneZoom: async () => {},
    getOption: async () => "",
    setOption: async () => {},
    getPaneOption: async () => "",
    setPaneOption: async () => {},
    setWindowOption: async () => {},
    setSessionOption: async () => {},
  } as unknown as MultiplexerDriver;
}

describe("Step 7 E2E: worktree, git/diff, shared assets, hook.emit, and status-bar RPC", () => {
  const tempDirs: string[] = [];
  const daemons: SpacesDaemon[] = [];

  afterEach(async () => {
    while (daemons.length > 0) {
      await daemons.pop()!.stop();
    }
    while (tempDirs.length > 0) {
      rmSync(tempDirs.pop()!, { recursive: true, force: true });
    }
  });

  test("worktree lifecycle + git.status + structured git.diff + shared.list + hook.emit + status-bar over UDS", async () => {
    const root = mkdtempSync(path.join(os.tmpdir(), "spaces-step7-e2e-"));
    tempDirs.push(root);
    const spacesDir = path.join(root, "spaces");
    const worktreeRoot = path.join(root, "worktrees");
    const repoDir = path.join(root, "demo-repo");
    const sharedDir = path.join(root, ".agents");

    // 1. Set up real temp git repo with an initial commit
    mkdirSync(repoDir, { recursive: true });
    runGit(repoDir, ["init", "-b", "main"]);
    writeFileSync(path.join(repoDir, "README.md"), "# Demo\nline 2\n");
    runGit(repoDir, ["add", "."]);
    runGit(repoDir, ["commit", "-m", "initial"]);

    // 2. Set up shared assets (.agents/skills + mcp.json)
    mkdirSync(path.join(sharedDir, "skills", "git-review"), { recursive: true });
    writeFileSync(
      path.join(sharedDir, "skills", "git-review", "SKILL.md"),
      "---\nname: git-review\ndescription: Review diffs cleanly\n---\nBody\n",
    );
    writeFileSync(
      path.join(sharedDir, "mcp.json"),
      JSON.stringify({
        mcpServers: {
          github: { command: "npx", args: ["-y", "@modelcontextprotocol/server-github"] },
        },
      }),
    );

    const fakeDriver = makeFakeDriver([
      {
        sessionName: "dev",
        windowId: "@1",
        windowIndex: 0,
        windowName: "main",
        paneIndex: 0,
        id: "%1",
        numericId: 1,
        title: "claude",
        currentCommand: "claude",
        startCommand: "claude",
        currentPath: repoDir,
        pid: process.pid,
        width: 120,
        height: 40,
        active: true,
        windowActive: true,
        inMode: false,
        synchronized: false,
        dead: false,
        deadStatus: 0,
        bell: false,
        activity: false,
        silence: false,
        spacesState: "working",
        spacesApp: "claude-code",
        spacesKind: "",
        spacesMsg: "running tests",
        spacesTsMs: Date.now(),
        spacesSource: "hook",
        spacesTitle: "",
        resumeSession: "",
        profile: "",
        launchCommand: ["claude"],
      },
    ]);

    const daemon = new SpacesDaemon({
      spacesDir,
      worktreeRoot,
      driver: fakeDriver,
    });
    daemons.push(daemon);
    const endpoint = daemon.start();

    const client = await connectUds(endpoint.socket);
    try {
      const auth = await client.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "step7-e2e",
        protocol_version: PROTOCOL_VERSION,
      });
      expect(auth.error).toBeUndefined();

      // 3. Worktree create -> list -> git.status -> edit -> git.diff -> inspect_safety -> remove -> prune
      const created = await client.request<WorktreeCreateResult>(RPC_METHODS.WORKTREE_CREATE, {
        request: {
          session: "dev",
          space_name: "dev",
          tab_name: "",
          folder: repoDir,
          use_worktree: true,
          create_new: true,
          existing_path: "",
          branch_name: "feat/step7-diff",
          base_branch: "main",
          auto_seed: 0,
          harness_app: "",
          harness_title: "",
          harness_prompt: "",
        },
      });
      expect(created.error).toBeUndefined();
      const wtPath = created.result!.worktree.path;
      expect(created.result!.created_branch).toBe(true);
      expect(created.result!.worktree.branch).toBe("feat/step7-diff");

      const listed = await client.request<BackendWorktreesListResult>(RPC_METHODS.WORKTREE_LIST, {
        dir: repoDir,
      });
      expect(listed.error).toBeUndefined();
      expect(listed.result!.worktrees.some((w) => w.path === wtPath)).toBe(true);

      // Modify README.md inside the new worktree and inspect git.status + git.diff
      writeFileSync(path.join(wtPath, "README.md"), "# Demo\nline 2 updated\nline 3 added\n");

      const status = await client.request<GitStatusResult>(RPC_METHODS.GIT_STATUS, {
        cwd: wtPath,
        force: true,
      });
      expect(status.error).toBeUndefined();
      expect(status.result?.git?.branch).toBe("feat/step7-diff");
      expect(status.result?.git?.is_dirty).toBe(true);

      const diff = await client.request<GitDiffResult>(RPC_METHODS.GIT_DIFF, {
        cwd: wtPath,
        scope: "working_tree",
      });
      expect(diff.error).toBeUndefined();
      expect(diff.result?.diff.files.length).toBe(1);
      expect(diff.result?.diff.files[0]?.path).toBe("README.md");
      expect(diff.result?.diff.total_additions).toBe(2);
      expect(diff.result?.diff.total_deletions).toBe(1);
      expect(diff.result?.diff.files[0]?.hunks.length).toBe(1);

      const safety = await client.request<WorktreeInspectSafetyResult>(
        RPC_METHODS.WORKTREE_INSPECT_SAFETY,
        { path: wtPath },
      );
      expect(safety.error).toBeUndefined();
      expect(safety.result?.safety.is_dirty).toBe(true);
      expect(safety.result?.safety.can_remove_safely).toBe(false);

      const removed = await client.request(RPC_METHODS.WORKTREE_REMOVE, {
        path: wtPath,
        force: true,
        delete_branch: true,
        force_branch: true,
      });
      expect(removed.error).toBeUndefined();

      const pruned = await client.request<WorktreePruneResult>(RPC_METHODS.WORKTREE_PRUNE, {
        repo_dirs: [repoDir],
      });
      expect(pruned.error).toBeUndefined();

      // 4. Shared assets list
      const shared = await client.request<BackendSharedListResult>(RPC_METHODS.BACKEND_SHARED_LIST, {
        dir: sharedDir,
      });
      expect(shared.error).toBeUndefined();
      expect(shared.result?.snapshot.skills.map((s) => s.name)).toEqual(["git-review"]);
      expect(shared.result?.snapshot.mcps.map((m) => m.name)).toEqual(["github"]);

      // 5. Hook ingestion -> hook_detail.get
      const emitted = await client.request<HookEmitResult>(RPC_METHODS.HOOK_EMIT, {
        agent: "claude",
        event: "PreToolUse",
        session: "dev",
        pane: "0",
        cwd: repoDir,
        session_id: "sess_hook_e2e",
        payload: { tool_name: "Bash", tool_input: { command: "bun test" } },
      });
      expect(emitted.error).toBeUndefined();
      expect(emitted.result?.ok).toBe(true);
      expect(emitted.result?.state).toBe("working");

      const detail = await client.request<BackendHookDetailResult>(
        RPC_METHODS.BACKEND_HOOK_DETAIL_GET,
        { session: "dev", pane_num: 0 },
      );
      expect(detail.error).toBeUndefined();
      expect(detail.result?.detail?.session_id).toBe("sess_hook_e2e");
      expect(detail.result?.detail?.tool?.name).toBe("Bash");

      // 6. Status bar RPC
      const sb = await client.request<BackendStatusBarResult>(RPC_METHODS.BACKEND_STATUS_BAR_GET, {
        skip_autosave: true,
      });
      expect(sb.error).toBeUndefined();
      expect(sb.result?.counts.working).toBe(1);
      expect(sb.result?.formatted_plain).toBe("◐1");
      expect(sb.result?.formatted_tmux).toBe("#[fg=#F9E2AF]◐1#[default]");
    } finally {
      client.close();
    }
  });
});

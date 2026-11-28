import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { TmuxDriver, type TmuxRunResult, type TmuxRunner } from "../drivers/tmux";
import { SpacesStore } from "../store/db";
import { BackendService } from "./service";

class RecordingTmuxRunner implements TmuxRunner {
  readonly calls: string[][] = [];
  private readonly responder: (args: string[]) => TmuxRunResult;

  constructor(responder?: (args: string[]) => TmuxRunResult) {
    this.responder =
      responder ??
      ((args) => {
        if (args[0] === "list-clients") {
          return { stdout: "/dev/pts/1\n", stderr: "", exitCode: 0 };
        }
        if (args[0] === "display-message" && args[2] === "#{session_name}") {
          return { stdout: "main\n", stderr: "", exitCode: 0 };
        }
        if (args[0] === "capture-pane") {
          return { stdout: `preview for ${args[args.length - 1]}\n`, stderr: "", exitCode: 0 };
        }
        if (args[0] === "new-session" && args.includes("-P")) {
          return { stdout: "@10\x1f%10\n", stderr: "", exitCode: 0 };
        }
        return { stdout: "", stderr: "", exitCode: 0 };
      });
  }

  async run(args: string[]): Promise<TmuxRunResult> {
    this.calls.push([...args]);
    return this.responder(args);
  }
}

describe("BackendService", () => {
  const tempDirs: string[] = [];

  afterEach(() => {
    while (tempDirs.length > 0) {
      rmSync(tempDirs.pop()!, { recursive: true, force: true });
    }
  });

  function setup(responder?: (args: string[]) => TmuxRunResult) {
    const dir = mkdtempSync(path.join(os.tmpdir(), "spacesd-backend-"));
    tempDirs.push(dir);
    const runner = new RecordingTmuxRunner(responder);
    const driver = new TmuxDriver({ runner, env: {} });
    const store = new SpacesStore(":memory:");
    const service = new BackendService(driver, store, {
      selfPath: "/usr/local/bin/spaces",
      spacesDir: dir,
      now: () => 1_700_000_000_000,
    });
    return { dir, runner, driver, store, service };
  }

  test("executes OpenSpace, FocusPane, NewWindow, KillWindow, CreateSpace, RenameSpace, KillSpace", async () => {
    const { runner, store, service } = setup();

    expect(await service.execVerb({ verb: "open_space", name: "work" })).toEqual({ kind: "ack" });
    expect(runner.calls.some((c) => c[0] === "switch-client" && c[2] === "work")).toBe(true);

    expect(
      await service.execVerb({
        verb: "focus_pane",
        session: "work",
        window_id: "@2",
        pane_id: "%7",
      }),
    ).toEqual({ kind: "ack" });
    expect(runner.calls.some((c) => c[0] === "select-window" && c[2] === "@2")).toBe(true);
    expect(runner.calls.some((c) => c[0] === "select-pane" && c[2] === "%7")).toBe(true);

    expect(
      await service.execVerb({ verb: "create_space", name: "demo", cwd: "/tmp" }),
    ).toEqual({ kind: "ack" });
    expect(store.getOpenState()?.spaces).toContain("demo");
    expect(store.getOpenState()?.active_session).toBe("demo");

    expect(
      await service.execVerb({ verb: "new_window", session: "demo", cwd: "/tmp", name: "dev" }),
    ).toEqual({ kind: "ack" });
    expect(
      runner.calls.some(
        (c) =>
          c[0] === "new-window" &&
          c.includes("demo:") &&
          c.includes("-n") &&
          c.includes("dev"),
      ),
    ).toBe(true);

    expect(await service.execVerb({ verb: "kill_window", window_id: "@9" })).toEqual({
      kind: "ack",
    });
    expect(runner.calls.some((c) => c[0] === "kill-window" && c[2] === "@9")).toBe(true);

    expect(
      await service.execVerb({ verb: "rename_space", old_name: "demo", new_name: "demo2" }),
    ).toEqual({ kind: "ack" });
    expect(store.getOpenState()?.spaces).toContain("demo2");

    expect(await service.execVerb({ verb: "kill_space", name: "demo2" })).toEqual({
      kind: "ack",
    });
    expect(store.getOpenState()?.spaces).not.toContain("demo2");
  });

  test("LaunchAgent builds __shim argv with and without profile", async () => {
    const { runner, service } = setup();

    // Without profile: uses agent binary name (`cursor-agent` for `cursor`)
    await service.execVerb({
      verb: "launch_agent",
      session: "main",
      cwd: "/repo",
      agent: "cursor",
      profile: "",
    });
    const noProfileCall = runner.calls.find(
      (c) => c[0] === "new-window" && c.includes("cursor-agent"),
    );
    expect(noProfileCall).toBeDefined();
    expect(noProfileCall?.slice(-4)).toEqual([
      "/usr/local/bin/spaces",
      "__shim",
      "cursor",
      "cursor-agent",
    ]);

    // With profile: uses `--profile <P> <agent>` and labels window with profile
    await service.execVerb({
      verb: "launch_agent",
      session: "main",
      cwd: "/repo",
      agent: "claude",
      profile: "work",
    });
    const profileCall = runner.calls.find((c) => c[0] === "new-window" && c.includes("--profile"));
    expect(profileCall).toBeDefined();
    expect(profileCall).toContain("-n");
    expect(profileCall).toContain("work");
    expect(profileCall?.slice(-5)).toEqual([
      "/usr/local/bin/spaces",
      "__shim",
      "--profile",
      "work",
      "claude",
    ]);
  });

  test("CapturePreviews, RestoreSpace, and LoadHarnesses work via SpacesStore", async () => {
    const { dir, store, service } = setup();

    const previews = await service.execVerb({
      verb: "capture_previews",
      pane_ids: ["%1", "%2"],
      lines: 20,
    });
    expect(previews).toEqual({
      kind: "previews",
      previews: {
        "%1": "preview for %1\n",
        "%2": "preview for %2\n",
      },
    });

    store.saveResurrectSpace({
      version: 1,
      name: "restored",
      saved_at_ms: 1_700_000_000_000,
      cwd: "/tmp",
      worktree_path: "",
      worktree_name: "",
      worktree_base: "",
      main_repo_root: "",
      active_window_index: 0,
      windows: [
        {
          index: 0,
          name: "shell",
          layout: "",
          active: true,
          zoomed: false,
          width: 120,
          height: 40,
          worktree_path: "",
          worktree_name: "",
          worktree_base: "",
          panes: [
            {
              index: 0,
              id: "%1",
              cwd: "/tmp",
              command: "zsh",
              app: "",
              title: "",
              active: true,
              agent: "",
              session_id: "",
              profile: "",
              full_command: [],
            },
          ],
        },
      ],
    });

    expect(await service.execVerb({ verb: "restore_space", name: "restored" })).toEqual({
      kind: "ack",
    });
    expect(store.getOpenState()?.spaces).toContain("restored");

    writeFileSync(
      path.join(dir, "harnesses.json"),
      JSON.stringify({
        version: 1,
        agents: [
          { name: "claude", binary_state: "ok", profiles: [] },
          { name: "missing", binary_state: "missing", profiles: [] },
          { name: "codex", binary_state: "missing", profiles: [{ name: "work" }] },
        ],
      }),
    );
    const harnesses = service.loadHarnesses();
    expect(harnesses.map((h) => h.name)).toEqual(["claude", "codex"]);
  });
});

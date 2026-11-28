import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { Subprocess } from "bun";
import { RPC_METHODS, type PtyAttachResult } from "@spaces/types";
import { SpacesRpcClient } from "../client/rpc-client";
import { SpacesDaemon } from "./daemon";

const REPO_ROOT = path.resolve(import.meta.dir, "../../../..");
const SPACES_BIN = path.join(REPO_ROOT, "target", "debug", "spaces");

async function waitFor(predicate: () => boolean, timeoutMs = 4000, stepMs = 25): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) {
      return;
    }
    await Bun.sleep(stepMs);
  }
  throw new Error(`Timed out waiting for condition after ${timeoutMs}ms`);
}

describe("Cross-language E2E: Rust __pty-host + spaces CLI <-> TS spacesd", () => {
  const tempDirs: string[] = [];
  const daemons: SpacesDaemon[] = [];
  const procs: Subprocess[] = [];

  afterEach(async () => {
    while (procs.length > 0) {
      const p = procs.pop()!;
      try {
        p.kill("SIGTERM");
        await p.exited;
      } catch {
        // Ignore already-exited subprocesses.
      }
    }
    while (daemons.length > 0) {
      await daemons.pop()!.stop();
    }
    while (tempDirs.length > 0) {
      rmSync(tempDirs.pop()!, { recursive: true, force: true });
    }
  });

  test("spawns PTY via Rust CLI + __pty-host, verifies HMAC OSC 7577, replays ring, runs handoff & ls", async () => {
    if (!existsSync(SPACES_BIN)) {
      // Skip gracefully if `cargo build -p spaces-cli` has not been built yet (e.g. TS-only runs).
      return;
    }

    const dir = mkdtempSync(path.join(os.tmpdir(), "spaces-e2e-rust-"));
    tempDirs.push(dir);

    const daemon = new SpacesDaemon({ spacesDir: dir });
    daemons.push(daemon);
    daemon.start();

    // Start real Rust `__pty-host` connected to `spacesd` over UDS
    const hostProc = Bun.spawn([SPACES_BIN, "--spaces-dir", dir, "__pty-host"], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, SPACES_DIR: dir },
    });
    procs.push(hostProc);

    // Wait until `__pty-host` authenticates with `spacesd`
    const rpc = new SpacesRpcClient({ spacesDir: dir });
    await waitFor(() => {
      // Poll until daemon sees the pty_host client connected.
      return daemon.getStore() !== null && existsSync(path.join(dir, "daemon.endpoint.json"));
    });
    await Bun.sleep(150);

    // Child script inside the PTY:
    // 1. Verifies SPACES_PTY_ID & SPACES_PTY_HMAC_TOKEN are injected
    // 2. Emits OSC 7501 status
    // 3. Computes real HMAC-SHA256 using Bun and emits OSC 7577 resume hint
    // 4. Prints marker output and stays alive briefly so attach/handoff/ls can inspect it
    const bunBin = process.execPath;
    const childJs = [
      `const crypto = require("node:crypto");`,
      `const ptyId = process.env.SPACES_PTY_ID || "";`,
      `const secret = process.env.SPACES_PTY_HMAC_TOKEN || "";`,
      `const agent = "claude";`,
      `const sid = "sess_e2e_7577";`,
      `const cwd = "/tmp";`,
      `const target = "claude --resume sess_e2e_7577";`,
      `const payload = "v1\\0" + ptyId + "\\0" + agent + "\\0" + sid + "\\0\\0" + cwd + "\\0" + target;`,
      `const sig = crypto.createHmac("sha256", Buffer.from(secret, "hex")).update(payload, "utf8").digest("hex");`,
      `process.stdout.write("\\x1b]7501;state=working:app=claude\\x1b\\\\");`,
      `process.stdout.write("\\x1b]7577;agent=" + agent + ":session_id=" + sid + ":cwd=" + Buffer.from(cwd).toString("base64") + ":resume_target=" + Buffer.from(target).toString("base64") + ":sig=" + sig + "\\x1b\\\\");`,
      `process.stdout.write("HELLO_REPLAY_RING\\n");`,
      `setTimeout(() => process.exit(0), 2500);`,
    ].join(" ");

    const spawnProc = Bun.spawn(
      [
        SPACES_BIN,
        "--spaces-dir",
        dir,
        "--json",
        "spawn",
        bunBin,
        "--cwd",
        "/tmp",
        "--",
        "-e",
        childJs,
      ],
      { stdout: "pipe", stderr: "pipe", env: { ...process.env, SPACES_DIR: dir } },
    );
    const spawnExit = await spawnProc.exited;
    const spawnStdout = await new Response(spawnProc.stdout).text();
    const spawnStderr = await new Response(spawnProc.stderr).text();
    expect(spawnExit, `spaces spawn stderr: ${spawnStderr}`).toBe(0);

    const spawned = JSON.parse(spawnStdout) as { thread_id: string; pty_id: string };
    expect(spawned.thread_id).toStartWith("thr_");
    expect(spawned.pty_id).toStartWith("pty_");

    // Verify Rust OscScanner -> spacesd HMAC verification -> SQLite verified_resume_hints!
    await waitFor(() => daemon.getStore().getVerifiedResumeHint(spawned.pty_id) !== null, 4000);
    const hint = daemon.getStore().getVerifiedResumeHint(spawned.pty_id);
    expect(hint?.agent).toBe("claude");
    expect(hint?.session_id).toBe("sess_e2e_7577");
    expect(hint?.resume_cmd).toEqual(["claude", "--resume", "sess_e2e_7577"]);

    // Verify 1 MiB replay ring via pty.attach
    const attached = await rpc.call<PtyAttachResult>(RPC_METHODS.PTY_ATTACH, {
      pty_id: spawned.pty_id,
      request_replay: true,
    });
    expect(attached.pty_id).toBe(spawned.pty_id);
    expect(attached.current_offset).toBeGreaterThan(0);
    expect(attached.replay_truncated).toBe(false);

    // Verify `spaces ls --json`
    const lsProc = Bun.spawn([SPACES_BIN, "--spaces-dir", dir, "--json", "ls"], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, SPACES_DIR: dir },
    });
    expect(await lsProc.exited).toBe(0);
    const lsOut = JSON.parse(await new Response(lsProc.stdout).text()) as {
      threads: Array<{ id?: string; thread?: { id: string } }>;
    };
    expect(lsOut.threads.some((t) => (t.thread?.id ?? t.id) === spawned.thread_id)).toBe(true);

    // Verify `spaces handoff` advances lease epoch
    const handoffProc = Bun.spawn(
      [
        SPACES_BIN,
        "--spaces-dir",
        dir,
        "--json",
        "handoff",
        spawned.thread_id,
        "--to",
        "agent",
        "--id",
        "coordinator",
      ],
      { stdout: "pipe", stderr: "pipe", env: { ...process.env, SPACES_DIR: dir } },
    );
    const handoffExit = await handoffProc.exited;
    const handoffOut = await new Response(handoffProc.stdout).text();
    const handoffErr = await new Response(handoffProc.stderr).text();
    expect(handoffExit, `spaces handoff stderr: ${handoffErr}`).toBe(0);
    const handoffRes = JSON.parse(handoffOut) as { lease: { state: string; epoch: number } };
    expect(handoffRes.lease.state).toBe("owned");
    expect(handoffRes.lease.epoch).toBe(1);
  });
});

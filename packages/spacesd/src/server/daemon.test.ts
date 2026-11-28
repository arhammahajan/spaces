import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  type EventPushNotification,
  type EventSubscribeResult,
  type LeaseResult,
  type MailboxSendResult,
  type OscResumeHint,
  type RpcNotification,
  type RpcResponse,
  type ThreadSpawnResult,
  type TurnSendResult,
} from "@spaces/types";
import { signOscResumeHint } from "../core/hmac";
import { SpacesRpcClient } from "../client/rpc-client";
import { TmuxDriver, type TmuxRunResult, type TmuxRunner } from "../drivers/tmux";
import { SpacesDaemon, type DaemonEndpointMetadata } from "./daemon";

interface RpcClient {
  request<T = unknown>(method: string, params?: unknown): Promise<RpcResponse & { result?: T }>;
  nextNotification(timeoutMs?: number): Promise<RpcNotification>;
  close(): void;
}

async function connectUdsClient(socketPath: string): Promise<RpcClient> {
  let nextId = 1;
  let buffer = "";
  const pending = new Map<number, (resp: RpcResponse) => void>();
  const notifQueue: RpcNotification[] = [];
  const notifWaiters: Array<(n: RpcNotification) => void> = [];

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
            const msg = JSON.parse(line) as RpcResponse & RpcNotification;
            if ("id" in msg && msg.id !== undefined) {
              const resolver = pending.get(Number(msg.id));
              if (resolver) {
                pending.delete(Number(msg.id));
                resolver(msg);
              }
            } else {
              const waiter = notifWaiters.shift();
              if (waiter) {
                waiter(msg);
              } else {
                notifQueue.push(msg);
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
    nextNotification(timeoutMs = 2000) {
      const queued = notifQueue.shift();
      if (queued) {
        return Promise.resolve(queued);
      }
      return new Promise<RpcNotification>((resolve, reject) => {
        const timer = setTimeout(() => {
          reject(new Error(`Timed out waiting for RPC notification after ${timeoutMs}ms`));
        }, timeoutMs);
        notifWaiters.push((n) => {
          clearTimeout(timer);
          resolve(n);
        });
      });
    },
    close() {
      socket.end();
    },
  };
}

describe("SpacesDaemon UDS NDJSON-RPC 2.0", () => {
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

  function createTempDaemon(): { dir: string; daemon: SpacesDaemon; endpoint: DaemonEndpointMetadata } {
    const dir = mkdtempSync(path.join(os.tmpdir(), "spacesd-test-"));
    tempDirs.push(dir);
    const daemon = new SpacesDaemon({ spacesDir: dir });
    daemons.push(daemon);
    const endpoint = daemon.start();
    return { dir, daemon, endpoint };
  }

  test("enforces 0600 permissions, auth gate, and full thread/lease/turn/mailbox/event flow", async () => {
    const { dir, endpoint } = createTempDaemon();

    expect(existsSync(endpoint.socket)).toBe(true);
    expect(existsSync(path.join(dir, "daemon.endpoint.json"))).toBe(true);
    expect(statSync(path.join(dir, "daemon.endpoint.json")).mode & 0o777).toBe(0o600);
    expect(statSync(path.join(dir, "spaces.db")).mode & 0o777).toBe(0o600);

    const diskEndpoint = JSON.parse(
      readFileSync(path.join(dir, "daemon.endpoint.json"), "utf8"),
    ) as DaemonEndpointMetadata;
    expect(diskEndpoint.boot_id).toBe(endpoint.boot_id);

    const client = await connectUdsClient(endpoint.socket);
    try {
      // Unauthenticated ping succeeds
      const pingResp = await client.request<{ ok: boolean; boot_id: string }>(
        RPC_METHODS.DAEMON_PING,
      );
      expect(pingResp.error).toBeUndefined();
      expect(pingResp.result?.ok).toBe(true);

      // Unauthenticated status rejected with -32001
      const unauthResp = await client.request(RPC_METHODS.DAEMON_STATUS);
      expect(unauthResp.error?.code).toBe(-32001);

      // Bad token rejected
      const badAuth = await client.request(RPC_METHODS.AUTH, {
        token: "wrong-token",
        client_kind: "cli",
        client_id: "cli-1",
        protocol_version: PROTOCOL_VERSION,
      });
      expect(badAuth.error?.code).toBe(-32001);

      // Valid auth succeeds
      const authResp = await client.request<{ ok: boolean; boot_id: string }>(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-1",
        protocol_version: PROTOCOL_VERSION,
      });
      expect(authResp.error).toBeUndefined();
      expect(authResp.result?.ok).toBe(true);

      // Subscribe to live events
      const subResp = await client.request<EventSubscribeResult>(RPC_METHODS.EVENT_SUBSCRIBE, {
        filter: { thread_ids: [], types: [] },
      });
      expect(subResp.error).toBeUndefined();
      const subId = subResp.result!.subscription_id;

      // Spawn thread
      const spawnResp = await client.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "claude",
        cwd: "/tmp",
        title: "migrate core",
      });
      expect(spawnResp.error).toBeUndefined();
      const threadId = spawnResp.result!.thread.id;

      const spawnedNotif = await client.nextNotification();
      expect(spawnedNotif.method).toBe(RPC_METHODS.EVENT_PUSH);
      expect((spawnedNotif.params as EventPushNotification).subscription_id).toBe(subId);
      expect((spawnedNotif.params as EventPushNotification).event.payload.type).toBe(
        "thread_updated",
      );

      // Claim lease: UNOWNED -> OWNED(1)
      const claimResp = await client.request<LeaseResult>(RPC_METHODS.LEASE_CLAIM, {
        thread_id: threadId,
        owner: { kind: "human", id: "tty-1" },
      });
      expect(claimResp.error).toBeUndefined();
      expect(claimResp.result?.lease.state).toBe("owned");
      expect(claimResp.result?.lease.epoch).toBe(1);

      // Stale epoch on turn.send fails with -32002
      const staleTurn = await client.request(RPC_METHODS.TURN_SEND, {
        thread_id: threadId,
        epoch: 0,
        origin: "human",
        prompt: "should fail",
      });
      expect(staleTurn.error?.code).toBe(-32002);

      // Valid epoch on turn.send succeeds
      const turnResp = await client.request<TurnSendResult>(RPC_METHODS.TURN_SEND, {
        thread_id: threadId,
        epoch: 1,
        origin: "human",
        prompt: "hello spacesd",
      });
      expect(turnResp.error).toBeUndefined();
      expect(turnResp.result?.turn.state).toBe("running");

      // Graceful handoff: OWNED(1) -> DRAIN -> STOP_AND_FLUSH -> OWNED(2)
      const handoffResp = await client.request<LeaseResult>(RPC_METHODS.LEASE_HANDOFF, {
        thread_id: threadId,
        from_epoch: 1,
        to_owner: { kind: "agent", id: "coordinator" },
        reason: "agent_claim",
      });
      expect(handoffResp.error).toBeUndefined();
      expect(handoffResp.result?.lease.epoch).toBe(2);
      expect(handoffResp.result?.lease.owner?.id).toBe("coordinator");

      // Mailbox send + dedup + ack
      const mb1 = await client.request<MailboxSendResult>(RPC_METHODS.MAILBOX_SEND, {
        thread_id: threadId,
        from: { role: "coordinator" },
        kind: "prompt",
        priority: "normal",
        dedup_key: "k1",
        payload: { text: "continue" },
      });
      const mb2 = await client.request<MailboxSendResult>(RPC_METHODS.MAILBOX_SEND, {
        thread_id: threadId,
        from: { role: "coordinator" },
        kind: "prompt",
        priority: "normal",
        dedup_key: "k1",
        payload: { text: "continue" },
      });
      expect(mb1.result?.deduplicated).toBe(false);
      expect(mb2.result?.deduplicated).toBe(true);
      expect(mb2.result?.message.id).toBe(mb1.result?.message.id);

      const ackResp = await client.request(RPC_METHODS.MAILBOX_ACK, {
        thread_id: threadId,
        message_id: mb1.result!.message.id,
      });
      expect(ackResp.error).toBeUndefined();
    } finally {
      client.close();
    }
  });

  test("verifies per-pane HMAC-SHA256 on OSC 7577 resume hints and rejects forged or unsafe hints", async () => {
    const { daemon, endpoint } = createTempDaemon();
    const secret = daemon.issuePaneHmacToken("pty-42");

    const client = await connectUdsClient(endpoint.socket);
    try {
      await client.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "pty_host",
        client_id: "pty-host-1",
        protocol_version: PROTOCOL_VERSION,
      });

      const baseHint: OscResumeHint = {
        agent: "claude",
        session_id: "sess-xyz",
        cwd: "/repo",
        resume_target: "claude --resume sess-xyz",
      };

      // Forged signature rejected
      const forgedResp = await client.request(RPC_METHODS.PTY_OSC, {
        pty_id: "pty-42",
        resume_hint: { ...baseHint, signature: "0".repeat(64) },
        ts_ms: Date.now(),
      });
      expect(forgedResp.error?.code).toBe(-32001);
      expect(daemon.getStore().getVerifiedResumeHint("pty-42")).toBeNull();

      // Unsafe shell command rejected even with valid HMAC
      const unsafeHint: OscResumeHint = {
        agent: "",
        session_id: "sess-bad",
        resume_target: "sh -c evil",
      };
      unsafeHint.signature = signOscResumeHint(secret, "pty-42", unsafeHint);
      const unsafeResp = await client.request(RPC_METHODS.PTY_OSC, {
        pty_id: "pty-42",
        resume_hint: unsafeHint,
        ts_ms: Date.now(),
      });
      expect(unsafeResp.error?.code).toBe(-32001);
      expect(daemon.getStore().getVerifiedResumeHint("pty-42")).toBeNull();

      // Valid HMAC + safe agent resume command persists
      const signedHint: OscResumeHint = {
        ...baseHint,
        signature: signOscResumeHint(secret, "pty-42", baseHint),
      };
      const okResp = await client.request<{ ok: boolean; verified: boolean }>(RPC_METHODS.PTY_OSC, {
        pty_id: "pty-42",
        resume_hint: signedHint,
        ts_ms: Date.now(),
      });
      expect(okResp.error).toBeUndefined();
      expect(okResp.result?.verified).toBe(true);

      const stored = daemon.getStore().getVerifiedResumeHint("pty-42");
      expect(stored).not.toBeNull();
      expect(stored?.agent).toBe("claude");
      expect(stored?.session_id).toBe("sess-xyz");
      expect(stored?.resume_cmd).toEqual(["claude", "--resume", "sess-xyz"]);
    } finally {
      client.close();
    }
  });

  test("emits GapReloadRequired on stale boot_id and cleans up socket on graceful stop", async () => {
    const { daemon, endpoint } = createTempDaemon();
    const client = await connectUdsClient(endpoint.socket);
    try {
      await client.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-2",
        protocol_version: PROTOCOL_VERSION,
      });

      await client.request(RPC_METHODS.EVENT_SUBSCRIBE, {
        filter: { boot_id: "boot_old_server", after_seq: 5, thread_ids: [], types: [] },
      });

      const gapNotif = await client.nextNotification();
      expect(gapNotif.method).toBe(RPC_METHODS.EVENT_PUSH);
      expect((gapNotif.params as EventPushNotification).event.payload.type).toBe(
        "gap_reload_required",
      );
    } finally {
      client.close();
    }

    await daemon.stop();
    expect(existsSync(endpoint.socket)).toBe(false);
    expect(existsSync(path.join(daemon.spacesDir, "daemon.endpoint.json"))).toBe(false);
  });

  test("executes backend verbs (OpenSpace, FocusPane, NewWindow, KillWindow, CreateSpace, LaunchAgent) over UDS RPC", async () => {
    const calls: string[][] = [];
    const runner: TmuxRunner = {
      async run(args: string[]): Promise<TmuxRunResult> {
        calls.push([...args]);
        if (args[0] === "list-clients") {
          return { stdout: "/dev/pts/1\n", stderr: "", exitCode: 0 };
        }
        if (args[0] === "display-message" && args[2] === "#{session_name}") {
          return { stdout: "main\n", stderr: "", exitCode: 0 };
        }
        return { stdout: "", stderr: "", exitCode: 0 };
      },
    };

    const dir = mkdtempSync(path.join(os.tmpdir(), "spacesd-backend-rpc-"));
    tempDirs.push(dir);
    const daemon = new SpacesDaemon({
      spacesDir: dir,
      driver: new TmuxDriver({ runner, env: {} }),
    });
    daemons.push(daemon);
    daemon.start();

    const rpc = new SpacesRpcClient({ spacesDir: dir });

    expect(await rpc.execVerb({ verb: "open_space", name: "work" })).toEqual({
      result: { kind: "ack" },
    });
    expect(
      await rpc.execVerb({
        verb: "focus_pane",
        session: "work",
        window_id: "@1",
        pane_id: "%3",
      }),
    ).toEqual({ result: { kind: "ack" } });
    expect(
      await rpc.execVerb({ verb: "create_space", name: "feat", cwd: "/tmp" }),
    ).toEqual({ result: { kind: "ack" } });
    expect(
      await rpc.execVerb({ verb: "new_window", session: "feat", cwd: "/tmp", name: "code" }),
    ).toEqual({ result: { kind: "ack" } });
    expect(await rpc.execVerb({ verb: "kill_window", window_id: "@5" })).toEqual({
      result: { kind: "ack" },
    });
    expect(
      await rpc.execVerb({
        verb: "launch_agent",
        session: "feat",
        cwd: "/tmp",
        agent: "claude",
        profile: "work",
      }),
    ).toEqual({ result: { kind: "ack" } });

    expect(calls.some((c) => c[0] === "switch-client" && c[2] === "work")).toBe(true);
    expect(calls.some((c) => c[0] === "select-pane" && c[2] === "%3")).toBe(true);
    expect(calls.some((c) => c[0] === "new-session" && c.includes("feat"))).toBe(true);
    expect(calls.some((c) => c[0] === "kill-window" && c[2] === "@5")).toBe(true);
    expect(calls.some((c) => c[0] === "new-window" && c.includes("--profile"))).toBe(true);
    expect(daemon.getStore().getOpenState()?.spaces).toContain("feat");
  });
});

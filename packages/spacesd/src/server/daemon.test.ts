import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  type DaemonStatusResult,
  type EventPushNotification,
  type EventSubscribeResult,
  type LeaseResult,
  type MailboxSendResult,
  type OscResumeHint,
  type PtyAttachResult,
  type PtyExitNotification,
  type PtyOutputNotification,
  type PtySpawnParams,
  type PtySpawnResult,
  type RpcNotification,
  type RpcRequest,
  type RpcResponse,
  type Thread,
  type ThreadSpawnResult,
  type TurnSendResult,
} from "@spaces/types";
import { signOscResumeHint } from "../core/hmac";
import { SpacesRpcClient } from "../client/rpc-client";
import { TmuxDriver, type TmuxRunResult, type TmuxRunner } from "../drivers/tmux";
import { SpacesDaemon, type DaemonEndpointMetadata } from "./daemon";

interface RpcClient {
  request<T = unknown>(method: string, params?: unknown): Promise<RpcResponse & { result?: T }>;
  notify(method: string, params?: unknown): void;
  nextNotification(timeoutMs?: number): Promise<RpcNotification>;
  close(): void;
}

interface ConnectClientOptions {
  onServerRequest?: (req: RpcRequest) => Promise<unknown> | unknown;
}

async function connectUdsClient(
  socketPath: string,
  options: ConnectClientOptions = {},
): Promise<RpcClient> {
  let nextId = 1;
  let buffer = "";
  const pending = new Map<number, (resp: RpcResponse) => void>();
  const notifQueue: RpcNotification[] = [];
  const notifWaiters: Array<(n: RpcNotification) => void> = [];

  const socket = await Bun.connect({
    unix: socketPath,
    socket: {
      data(sock, chunk) {
        buffer += Buffer.from(chunk).toString("utf8");
        let idx = buffer.indexOf("\n");
        while (idx !== -1) {
          const line = buffer.slice(0, idx).trim();
          buffer = buffer.slice(idx + 1);
          if (line.length > 0) {
            const msg = JSON.parse(line) as Partial<RpcResponse & RpcNotification & RpcRequest>;
            if (typeof msg.method === "string" && "id" in msg && msg.id !== undefined) {
              const req = msg as RpcRequest;
              void Promise.resolve(options.onServerRequest?.(req)).then(
                (result) => {
                  sock.write(
                    `${JSON.stringify({ jsonrpc: "2.0", id: req.id, result: result ?? { ok: true } })}\n`,
                  );
                },
                (err) => {
                  const message = err instanceof Error ? err.message : String(err);
                  sock.write(
                    `${JSON.stringify({
                      jsonrpc: "2.0",
                      id: req.id,
                      error: { code: -32603, message },
                    })}\n`,
                  );
                },
              );
            } else if ("id" in msg && msg.id !== undefined) {
              const resolver = pending.get(Number(msg.id));
              if (resolver) {
                pending.delete(Number(msg.id));
                resolver(msg as RpcResponse);
              }
            } else {
              const waiter = notifWaiters.shift();
              if (waiter) {
                waiter(msg as RpcNotification);
              } else {
                notifQueue.push(msg as RpcNotification);
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
    notify(method: string, params?: unknown) {
      socket.write(`${JSON.stringify({ jsonrpc: "2.0", method, params })}\n`);
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

  test("brokers PTY spawn/attach/output/input/resize/osc/exit between cli and pty_host over UDS", async () => {
    const { daemon, endpoint } = createTempDaemon();

    const hostRequests: RpcRequest[] = [];
    let lastSpawnedEnv: Record<string, string | undefined> | undefined;

    const ptyHost = await connectUdsClient(endpoint.socket, {
      onServerRequest(req) {
        hostRequests.push(req);
        if (req.method === RPC_METHODS.PTY_SPAWN) {
          const p = req.params as PtySpawnParams;
          lastSpawnedEnv = p.env;
          return {
            pty_id: p.env.SPACES_PTY_ID!,
            pid: 4242,
            pgid: 4242,
          } satisfies PtySpawnResult;
        }
        if (req.method === RPC_METHODS.PTY_ATTACH) {
          const p = req.params as { pty_id: string };
          return {
            pty_id: p.pty_id,
            current_offset: 128,
            replay_truncated: false,
          } satisfies PtyAttachResult;
        }
        return { ok: true };
      },
    });

    const cli = await connectUdsClient(endpoint.socket);
    const cli2 = await connectUdsClient(endpoint.socket);

    try {
      await cli.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-pty-1",
        protocol_version: PROTOCOL_VERSION,
      });
      await cli2.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-pty-2",
        protocol_version: PROTOCOL_VERSION,
      });

      const threadResp = await cli.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "claude",
        cwd: "/tmp",
      });
      expect(threadResp.error).toBeUndefined();
      const threadId = threadResp.result!.thread.id;

      // Without pty_host connected, pty.spawn fails with -32603
      const noHostResp = await cli.request(RPC_METHODS.PTY_SPAWN, {
        thread_id: threadId,
        argv: ["claude"],
        cwd: "/tmp",
        env: {},
        size: { cols: 80, rows: 24 },
      });
      expect(noHostResp.error?.code).toBe(-32603);
      expect(noHostResp.error?.message).toContain("no __pty-host connected");

      // Authenticate mock pty_host
      const hostAuth = await ptyHost.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "pty_host",
        client_id: "pty-host-main",
        protocol_version: PROTOCOL_VERSION,
      });
      expect(hostAuth.error).toBeUndefined();

      // Subscribe cli2 to live events on this thread
      await cli2.request(RPC_METHODS.EVENT_SUBSCRIBE, {
        filter: { thread_ids: [threadId], types: [] },
      });

      // Spawn PTY (ensure daemon-issued env overrides any caller-supplied SPACES_* keys)
      const spawnResp = await cli.request<PtySpawnResult>(RPC_METHODS.PTY_SPAWN, {
        thread_id: threadId,
        argv: ["claude", "--model", "opus"],
        cwd: "/tmp",
        env: { CUSTOM_VAR: "yes", SPACES_PTY_ID: "spoofed" },
        size: { cols: 120, rows: 40 },
      });
      expect(spawnResp.error).toBeUndefined();
      const ptyId = spawnResp.result!.pty_id;
      expect(ptyId.startsWith("pty_")).toBe(true);
      expect(spawnResp.result!.pid).toBe(4242);
      expect(spawnResp.result!.pgid).toBe(4242);

      // Verify injected env & persisted HMAC secret
      expect(lastSpawnedEnv?.CUSTOM_VAR).toBe("yes");
      expect(lastSpawnedEnv?.SPACES_PTY_ID).toBe(ptyId);
      expect(lastSpawnedEnv?.SPACES_THREAD_ID).toBe(threadId);
      const hmacSecret = lastSpawnedEnv?.SPACES_PTY_HMAC_TOKEN;
      expect(typeof hmacSecret).toBe("string");
      expect(hmacSecret?.length).toBe(64);
      expect(daemon.getStore().getPaneToken(ptyId)?.secret_hex).toBe(hmacSecret);

      // Verify daemon.status.active_ptys === 1 & thread updated
      const statusResp = await cli.request<DaemonStatusResult>(RPC_METHODS.DAEMON_STATUS);
      expect(statusResp.error).toBeUndefined();
      expect(statusResp.result?.active_ptys).toBe(1);

      const getThreadResp = await cli.request<{ thread: Thread }>(RPC_METHODS.THREAD_GET, {
        thread_id: threadId,
      });
      expect(getThreadResp.result?.thread.pty_id).toBe(ptyId);
      expect(getThreadResp.result?.thread.state).toBe("active");

      const spawnEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(spawnEvt.event.payload.type).toBe("thread_updated");

      // Attaching to unknown PTY returns -32003
      const missingAttach = await cli.request(RPC_METHODS.PTY_ATTACH, {
        pty_id: "pty_missing",
        request_replay: false,
      });
      expect(missingAttach.error?.code).toBe(-32003);

      // Attach both clients and test pty.output fan-out
      const attach1 = await cli.request<PtyAttachResult>(RPC_METHODS.PTY_ATTACH, {
        pty_id: ptyId,
        request_replay: true,
      });
      expect(attach1.error).toBeUndefined();
      expect(attach1.result?.current_offset).toBe(128);

      const attach2 = await cli2.request<PtyAttachResult>(RPC_METHODS.PTY_ATTACH, {
        pty_id: ptyId,
        request_replay: false,
      });
      expect(attach2.error).toBeUndefined();

      const out1: PtyOutputNotification = {
        pty_id: ptyId,
        offset: 133,
        encoding: "base64",
        data_b64: Buffer.from("hello").toString("base64"),
        ts_ms: Date.now(),
      };
      ptyHost.notify(RPC_METHODS.PTY_OUTPUT, out1);

      const cliOut1 = await cli.nextNotification();
      expect(cliOut1.method).toBe(RPC_METHODS.PTY_OUTPUT);
      expect(cliOut1.params).toEqual(out1);

      const cli2Out1 = await cli2.nextNotification();
      expect(cli2Out1.method).toBe(RPC_METHODS.PTY_OUTPUT);
      expect(cli2Out1.params).toEqual(out1);

      // Detach cli2: cli is still attached so pty_host should NOT receive pty.detach yet
      const detachCountBefore = hostRequests.filter((r) => r.method === RPC_METHODS.PTY_DETACH).length;
      const detach2 = await cli2.request<{ ok: boolean }>(RPC_METHODS.PTY_DETACH, { pty_id: ptyId });
      expect(detach2.error).toBeUndefined();
      expect(detach2.result?.ok).toBe(true);
      expect(hostRequests.filter((r) => r.method === RPC_METHODS.PTY_DETACH).length).toBe(
        detachCountBefore,
      );

      // Epoch fencing on pty.input
      const claim = await cli.request<LeaseResult>(RPC_METHODS.LEASE_CLAIM, {
        thread_id: threadId,
        owner: { kind: "human", id: "tty-cli-1" },
      });
      expect(claim.error).toBeUndefined();
      expect(claim.result?.lease.epoch).toBe(1);
      // Drain lease_changed event on cli2
      const leaseEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(leaseEvt.event.payload.type).toBe("lease_changed");

      const inputCountBefore = hostRequests.filter((r) => r.method === RPC_METHODS.PTY_INPUT).length;
      const staleInput = await cli.request(RPC_METHODS.PTY_INPUT, {
        pty_id: ptyId,
        epoch: 0,
        encoding: "base64",
        data_b64: Buffer.from("stale").toString("base64"),
      });
      expect(staleInput.error?.code).toBe(-32002);
      expect(hostRequests.filter((r) => r.method === RPC_METHODS.PTY_INPUT).length).toBe(
        inputCountBefore,
      );

      const validInput = await cli.request<{ ok: boolean }>(RPC_METHODS.PTY_INPUT, {
        pty_id: ptyId,
        epoch: 1,
        encoding: "base64",
        data_b64: Buffer.from("ls\n").toString("base64"),
      });
      expect(validInput.error).toBeUndefined();
      expect(validInput.result?.ok).toBe(true);
      const forwardedInput = hostRequests.filter((r) => r.method === RPC_METHODS.PTY_INPUT).at(-1);
      expect(forwardedInput?.params).toEqual({
        pty_id: ptyId,
        epoch: 1,
        encoding: "base64",
        data_b64: Buffer.from("ls\n").toString("base64"),
      });

      // pty.resize
      const resizeResp = await cli.request<{ ok: boolean }>(RPC_METHODS.PTY_RESIZE, {
        pty_id: ptyId,
        size: { cols: 160, rows: 50 },
      });
      expect(resizeResp.error).toBeUndefined();
      expect(resizeResp.result?.ok).toBe(true);
      const forwardedResize = hostRequests.filter((r) => r.method === RPC_METHODS.PTY_RESIZE).at(-1);
      expect(forwardedResize?.params).toEqual({
        pty_id: ptyId,
        size: { cols: 160, rows: 50 },
      });

      // pty.osc attaches threadId & pty_id to emitted events
      const hint: OscResumeHint = {
        agent: "claude",
        session_id: "sess-broker-1",
        cwd: "/tmp",
        resume_target: "claude --resume sess-broker-1",
      };
      hint.signature = signOscResumeHint(hmacSecret!, ptyId, hint);
      const oscResp = await ptyHost.request<{ ok: boolean; verified?: boolean }>(
        RPC_METHODS.PTY_OSC,
        {
          pty_id: ptyId,
          status: { state: "working", app: "claude", msg: "thinking" },
          resume_hint: hint,
          ts_ms: Date.now(),
        },
      );
      expect(oscResp.error).toBeUndefined();
      expect(oscResp.result?.verified).toBe(true);

      const oscStatusEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(oscStatusEvt.event.thread_id).toBe(threadId);
      expect(oscStatusEvt.event.payload).toEqual({
        type: "osc_status_received",
        pty_id: ptyId,
        status: { state: "working", app: "claude", msg: "thinking" },
      });

      const oscHintEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(oscHintEvt.event.thread_id).toBe(threadId);
      expect(oscHintEvt.event.payload.type).toBe("osc_resume_hint_received");
      if (oscHintEvt.event.payload.type === "osc_resume_hint_received") {
        expect(oscHintEvt.event.payload.pty_id).toBe(ptyId);
      }

      const threadSessionEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(threadSessionEvt.event.thread_id).toBe(threadId);
      expect(threadSessionEvt.event.payload.type).toBe("thread_updated");
      if (threadSessionEvt.event.payload.type === "thread_updated") {
        expect(threadSessionEvt.event.payload.thread.vendor_session_id).toBe("sess-broker-1");
      }

      // pty.exit notification fans out to attached clients, updates thread & active_ptys, and emits events
      const exitTs = Date.now();
      const exitPayload: PtyExitNotification = {
        pty_id: ptyId,
        exit_code: 0,
        ts_ms: exitTs,
      };
      ptyHost.notify(RPC_METHODS.PTY_EXIT, exitPayload);

      const exitNotif = await cli.nextNotification();
      expect(exitNotif.method).toBe(RPC_METHODS.PTY_EXIT);
      expect(exitNotif.params).toEqual(exitPayload);

      const ptyExitedEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(ptyExitedEvt.event.thread_id).toBe(threadId);
      expect(ptyExitedEvt.event.payload).toEqual({
        type: "pty_exited",
        pty_id: ptyId,
        exit_code: 0,
      });

      const threadIdleEvt = (await cli2.nextNotification()).params as EventPushNotification;
      expect(threadIdleEvt.event.payload.type).toBe("thread_updated");
      if (threadIdleEvt.event.payload.type === "thread_updated") {
        expect(threadIdleEvt.event.payload.thread.state).toBe("idle");
        expect(threadIdleEvt.event.payload.thread.pty_id).toBeUndefined();
        expect(threadIdleEvt.event.payload.thread.updated_at_ms).toBe(exitTs);
      }

      const statusAfterExit = await cli.request<DaemonStatusResult>(RPC_METHODS.DAEMON_STATUS);
      expect(statusAfterExit.result?.active_ptys).toBe(0);

      const threadAfterExit = await cli.request<{ thread: Thread }>(RPC_METHODS.THREAD_GET, {
        thread_id: threadId,
      });
      expect(threadAfterExit.result?.thread.state).toBe("idle");
      expect(threadAfterExit.result?.thread.pty_id).toBeUndefined();
    } finally {
      cli.close();
      cli2.close();
      ptyHost.close();
    }
  });

  test("handles non-zero pty.exit, socket-close detach, thread.close pty.kill, and pty_host disconnect rejection", async () => {
    const { daemon, endpoint } = createTempDaemon();

    const hostRequests: RpcRequest[] = [];
    let stallNextSpawn = false;
    const ptyHost = await connectUdsClient(endpoint.socket, {
      onServerRequest(req) {
        hostRequests.push(req);
        if (req.method === RPC_METHODS.PTY_SPAWN) {
          if (stallNextSpawn) {
            return new Promise(() => {
              // Never settles; pty_host will disconnect to trigger rejection.
            });
          }
          const p = req.params as PtySpawnParams;
          return { pty_id: p.env.SPACES_PTY_ID!, pid: 5150, pgid: 5150 } satisfies PtySpawnResult;
        }
        if (req.method === RPC_METHODS.PTY_ATTACH) {
          const p = req.params as { pty_id: string };
          return {
            pty_id: p.pty_id,
            current_offset: 0,
            replay_truncated: false,
          } satisfies PtyAttachResult;
        }
        return { ok: true };
      },
    });
    const cli = await connectUdsClient(endpoint.socket);
    const watcher = await connectUdsClient(endpoint.socket);

    try {
      await ptyHost.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "pty_host",
        client_id: "pty-host-2",
        protocol_version: PROTOCOL_VERSION,
      });
      await cli.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-a",
        protocol_version: PROTOCOL_VERSION,
      });
      await watcher.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "cli-watcher",
        protocol_version: PROTOCOL_VERSION,
      });

      const thr = await cli.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "codex",
        cwd: "/tmp",
      });
      const threadId = thr.result!.thread.id;

      const spawned = await cli.request<PtySpawnResult>(RPC_METHODS.PTY_SPAWN, {
        thread_id: threadId,
        argv: ["codex"],
        cwd: "/tmp",
        env: {},
        size: { cols: 80, rows: 24 },
      });
      const ptyId = spawned.result!.pty_id;

      // Attach watcher, then close watcher socket -> should forward pty.detach to pty_host
      await watcher.request(RPC_METHODS.PTY_ATTACH, { pty_id: ptyId, request_replay: false });
      watcher.close();
      await Bun.sleep(50);
      expect(
        hostRequests.some(
          (r) =>
            r.method === RPC_METHODS.PTY_DETACH &&
            (r.params as { pty_id: string }).pty_id === ptyId,
        ),
      ).toBe(true);

      // Non-zero pty.exit marks thread failed
      ptyHost.notify(RPC_METHODS.PTY_EXIT, {
        pty_id: ptyId,
        exit_code: 137,
        signal: "SIGKILL",
        ts_ms: Date.now(),
      });
      await Bun.sleep(50);
      expect(daemon.getStore().getThread(threadId)?.state).toBe("failed");

      // Spawn second PTY on a second thread and verify thread.close sends best-effort pty.kill
      const thr2 = await cli.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "claude",
        cwd: "/tmp",
      });
      const thread2Id = thr2.result!.thread.id;
      const spawned2 = await cli.request<PtySpawnResult>(RPC_METHODS.PTY_SPAWN, {
        thread_id: thread2Id,
        argv: ["claude"],
        cwd: "/tmp",
        env: {},
        size: { cols: 80, rows: 24 },
      });
      const pty2Id = spawned2.result!.pty_id;

      const closeResp = await cli.request(RPC_METHODS.THREAD_CLOSE, { thread_id: thread2Id });
      expect(closeResp.error).toBeUndefined();
      await Bun.sleep(50);
      expect(
        hostRequests.some(
          (r) =>
            r.method === RPC_METHODS.PTY_KILL &&
            (r.params as { pty_id: string }).pty_id === pty2Id,
        ),
      ).toBe(true);

      // Disconnecting pty_host rejects in-flight pendingHostCalls cleanly
      const thr3 = await cli.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "claude",
        cwd: "/tmp",
      });
      stallNextSpawn = true;
      const inFlightSpawnPromise = cli.request(RPC_METHODS.PTY_SPAWN, {
        thread_id: thr3.result!.thread.id,
        argv: ["claude"],
        cwd: "/tmp",
        env: {},
        size: { cols: 80, rows: 24 },
      });
      await Bun.sleep(30);
      ptyHost.close();

      const failedSpawn = await inFlightSpawnPromise;
      expect(failedSpawn.error?.code).toBe(-32603);
      expect(failedSpawn.error?.message).toContain("pty_host disconnected");
    } finally {
      cli.close();
      watcher.close();
      ptyHost.close();
    }
  });
});

import { afterEach, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  type LeaseResult,
  type OscResumeHint,
  type RpcResponse,
  type Thread,
  type ThreadSpawnResult,
  type Turn,
  type TurnSendResult,
} from "@spaces/types";
import { signOscResumeHint } from "../core/hmac";
import {
  ClaudeHeadlessExecutor,
  type ClaudeSpawnFn,
  type SpawnedProcess,
} from "../executors/claude";
import { SpacesDaemon } from "../server/daemon";
import { ClaudePlugin, encodeClaudeProjectSlug } from "./claude";
import { HarnessPluginRegistry } from "./registry";

interface MockClient {
  request<T = unknown>(method: string, params?: unknown): Promise<RpcResponse & { result?: T }>;
  close(): void;
}

async function connectUds(socketPath: string): Promise<MockClient> {
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

function createMockClaudeSpawn(
  seenArgvs: string[][],
  onTurnWritten?: (jsonlPath: string) => void,
  jsonlPath = "",
): ClaudeSpawnFn {
  return (argv) => {
    seenArgvs.push([...argv]);
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    let resolveExit!: (code: number) => void;
    const exited = new Promise<number>((resolve) => {
      resolveExit = resolve;
    });
    const encoder = new TextEncoder();
    const stdout = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c;
      },
    });
    let closed = false;
    const finish = (code = 0) => {
      if (closed) return;
      closed = true;
      try {
        controller.close();
      } catch {
        // Already closed.
      }
      resolveExit(code);
    };

    const proc: SpawnedProcess = {
      stdout,
      exited,
      stdin: {
        write(data) {
          const raw = typeof data === "string" ? data : Buffer.from(data).toString("utf8");
          if (raw.includes('"type":"user"')) {
            const sidIdx = argv.indexOf("--resume");
            const sid = sidIdx >= 0 && argv[sidIdx + 1] ? argv[sidIdx + 1]! : "sess_claude_e2e";
            const events = [
              {
                type: "system",
                subtype: "init",
                session_id: sid,
                model: "claude-sonnet-4-5",
              },
              {
                type: "stream_event",
                event: {
                  type: "content_block_delta",
                  delta: { type: "text_delta", text: "AI headless reply" },
                },
              },
              {
                type: "result",
                subtype: "success",
                is_error: false,
                session_id: sid,
                result: "AI headless reply",
                total_cost_usd: 0.04,
                usage: {
                  input_tokens: 120,
                  output_tokens: 45,
                  cache_read_input_tokens: 10,
                  cache_creation_input_tokens: 5,
                },
              },
            ];
            for (const ev of events) {
              controller.enqueue(encoder.encode(`${JSON.stringify(ev)}\n`));
            }
            if (onTurnWritten && jsonlPath) {
              onTurnWritten(jsonlPath);
            }
          }
          return raw.length;
        },
        end() {
          finish(0);
        },
      },
      kill() {
        finish(0);
      },
    };
    return proc;
  };
}

async function waitFor(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await Bun.sleep(15);
  }
  throw new Error(`Timed out after ${timeoutMs}ms`);
}

describe("Step 6 E2E: ClaudePlugin + ClaudeHeadlessExecutor + LeaseMachine stopAndFlush handoff", () => {
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

  test("full loop: interactive launch -> OSC 7501/7577 -> human->AI handoff & readDelta -> headless turn -> AI->human handoff -> resume", async () => {
    const root = mkdtempSync(path.join(os.tmpdir(), "spaces-step6-e2e-"));
    tempDirs.push(root);
    const homeDir = path.join(root, "home");
    const spacesDir = path.join(root, "spaces");
    const repoCwd = path.join(root, "repo");
    const sessionId = "sess_claude_e2e_1";

    const slug = encodeClaudeProjectSlug(repoCwd);
    const projectDir = path.join(homeDir, ".claude", "projects", slug);
    mkdirSync(projectDir, { recursive: true });
    mkdirSync(repoCwd, { recursive: true });
    const jsonlPath = path.join(projectDir, `${sessionId}.jsonl`);

    const seenHeadlessArgvs: string[][] = [];
    const mockSpawn = createMockClaudeSpawn(seenHeadlessArgvs, (p) => {
      // Simulate Claude CLI appending the AI turn to disk JSONL
      writeFileSync(
        p,
        JSON.stringify({
          type: "assistant",
          sessionId,
          timestamp: "2026-11-28T10:00:05.000Z",
          message: { role: "assistant", model: "claude-sonnet-4-5", content: "AI headless reply" },
        }) + "\n",
        { flag: "a" },
      );
    }, jsonlPath);

    const claudePlugin = new ClaudePlugin({
      homeDir,
      spacesDir,
      headlessFactory: (req, launch) =>
        new ClaudeHeadlessExecutor({
          threadId: req.threadId,
          launch,
          sessionId: req.sessionId,
          spawn: mockSpawn,
        }),
    });

    const registry = new HarnessPluginRegistry();
    registry.register(claudePlugin);

    const daemon = new SpacesDaemon({ spacesDir, plugins: registry });
    daemons.push(daemon);
    const endpoint = daemon.start();

    const client = await connectUds(endpoint.socket);
    try {
      await client.request(RPC_METHODS.AUTH, {
        token: endpoint.token,
        client_kind: "cli",
        client_id: "e2e-cli",
        protocol_version: PROTOCOL_VERSION,
      });

      // 1. Spawn hybrid Claude thread & claim human lease (epoch = 1)
      const spawned = await client.request<ThreadSpawnResult>(RPC_METHODS.THREAD_SPAWN, {
        agent: "claude",
        driver: "claude_sdk",
        kind: "hybrid",
        cwd: repoCwd,
      });
      const threadId = spawned.result!.thread.id;

      const claimed = await client.request<LeaseResult>(RPC_METHODS.LEASE_CLAIM, {
        thread_id: threadId,
        owner: { kind: "human", id: "tty-1" },
      });
      expect(claimed.result?.lease.epoch).toBe(1);
      expect(claimed.result?.lease.state).toBe("owned");

      // Verify interactive launch spec for initial human session
      const initialLaunch = claudePlugin.buildInteractiveLaunch({
        cwd: repoCwd,
        homeDir,
        spacesDir,
      });
      expect(initialLaunch.argv[0]).toBe("claude");

      // 2. Emit OSC 7501 status + HMAC-signed OSC 7577 resume hint
      const paneId = "pty_e2e_1";
      const secret = daemon.issuePaneHmacToken(paneId, threadId);
      const hint: OscResumeHint = {
        agent: "claude",
        session_id: sessionId,
        cwd: repoCwd,
        resume_target: `claude --resume ${sessionId}`,
        thread_id: threadId,
      };
      hint.signature = signOscResumeHint(secret, paneId, hint);

      const oscResp = await client.request<{ ok: boolean; verified: boolean }>(
        RPC_METHODS.PTY_OSC,
        {
          pty_id: paneId,
          status: { state: "idle", app: "claude" },
          resume_hint: hint,
          ts_ms: Date.now(),
        },
      );
      expect(oscResp.error).toBeUndefined();
      expect(oscResp.result?.verified).toBe(true);
      expect(daemon.getStore().getThread(threadId)?.vendor_session_id).toBe(sessionId);

      // 3. Simulate human turn written to Claude's disk JSONL before handoff
      writeFileSync(
        jsonlPath,
        [
          JSON.stringify({ type: "custom-title", customTitle: "Migrate to Unified Spaces" }),
          JSON.stringify({
            type: "user",
            sessionId,
            timestamp: "2026-11-28T10:00:01.000Z",
            message: { role: "user", content: "Explain the lease FSM" },
          }),
          JSON.stringify({
            type: "assistant",
            sessionId,
            timestamp: "2026-11-28T10:00:02.000Z",
            message: {
              role: "assistant",
              model: "claude-sonnet-4-5",
              content: "The lease FSM uses monotonic epochs.",
            },
          }),
        ].join("\n") + "\n",
      );

      // 4. Human -> AI handoff (epoch 1 -> DRAIN -> STOP_AND_FLUSH + readDelta -> epoch 2)
      const handoffToAi = await client.request<LeaseResult>(RPC_METHODS.LEASE_HANDOFF, {
        thread_id: threadId,
        from_epoch: 1,
        to_owner: { kind: "agent", id: "coordinator" },
        reason: "agent_claim",
      });
      expect(handoffToAi.error).toBeUndefined();
      expect(handoffToAi.result?.lease.epoch).toBe(2);
      expect(handoffToAi.result?.lease.owner?.kind).toBe("agent");

      // Verify stopAndFlush ran readDelta and synced title + model onto the Thread
      const afterHandoffThread: Thread | null = daemon.getStore().getThread(threadId);
      expect(afterHandoffThread?.title).toBe("Migrate to Unified Spaces");
      expect(afterHandoffThread?.model).toBe("claude-sonnet-4-5");

      // 5. Send AI headless turn at epoch 2 -> runs ClaudeHeadlessExecutor (--resume <sessionId>)
      const turnResp = await client.request<TurnSendResult>(RPC_METHODS.TURN_SEND, {
        thread_id: threadId,
        epoch: 2,
        origin: "coordinator",
        prompt: "Summarize next steps",
      });
      expect(turnResp.error).toBeUndefined();
      const turnId = turnResp.result!.turn.id;

      await waitFor(() => daemon.getStore().getTurn(turnId)?.state === "completed");
      const completedTurn: Turn | null = daemon.getStore().getTurn(turnId);
      expect(completedTurn?.reply_preview).toBe("AI headless reply");
      expect(completedTurn?.usage?.input_tokens).toBe(120);
      expect(completedTurn?.usage?.output_tokens).toBe(45);
      expect(seenHeadlessArgvs.length).toBe(1);
      expect(seenHeadlessArgvs[0]).toContain("--resume");
      expect(seenHeadlessArgvs[0]).toContain(sessionId);

      // 6. AI -> Human handoff (epoch 2 -> DRAIN -> STOP_AND_FLUSH -> epoch 3)
      const handoffToHuman = await client.request<LeaseResult>(RPC_METHODS.LEASE_HANDOFF, {
        thread_id: threadId,
        from_epoch: 2,
        to_owner: { kind: "human", id: "tty-1" },
        reason: "user_attach",
      });
      expect(handoffToHuman.error).toBeUndefined();
      expect(handoffToHuman.result?.lease.epoch).toBe(3);
      expect(handoffToHuman.result?.lease.owner?.kind).toBe("human");

      // 7. Interactive re-launch carries `--resume <sessionId>`
      const resumeLaunch = claudePlugin.buildInteractiveLaunch({
        cwd: repoCwd,
        sessionId: daemon.getStore().getThread(threadId)?.vendor_session_id,
        homeDir,
        spacesDir,
      });
      expect(resumeLaunch.argv).toEqual(["claude", "--resume", sessionId]);
    } finally {
      client.close();
    }
  });
});

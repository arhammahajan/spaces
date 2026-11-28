import { describe, expect, test } from "bun:test";
import type { InteractiveLaunchSpec, HeadlessTurnEvent } from "../plugins/types";
import {
  ClaudeHeadlessError,
  ClaudeHeadlessExecutor,
  buildClaudeHeadlessArgv,
  type ClaudeSpawnFn,
  type SpawnedProcess,
} from "./claude";

interface FakeProcessController {
  proc: SpawnedProcess;
  stdinChunks: string[];
  stdinEnded: boolean;
  signals: Array<NodeJS.Signals | number | undefined>;
  emitStdout(text: string): void;
  emitStdoutJson(obj: unknown): void;
  emitStderr(text: string): void;
  closeStdout(): void;
  exit(code: number): void;
}

interface FakeProcessOptions {
  ignoreSigint?: boolean;
  ignoreSigterm?: boolean;
  closeStdoutOnExit?: boolean;
  exitOnStdinEnd?: number | null;
  onStdinLine?: (line: string, ctrl: FakeProcessController) => void;
}

function createFakeProcess(opts: FakeProcessOptions = {}): FakeProcessController {
  const encoder = new TextEncoder();
  const stdinChunks: string[] = [];
  let stdinLineBuf = "";
  let stdinEnded = false;
  const signals: Array<NodeJS.Signals | number | undefined> = [];

  let stdoutController!: ReadableStreamDefaultController<Uint8Array>;
  let stdoutClosed = false;
  const stdout = new ReadableStream<Uint8Array>({
    start(controller) {
      stdoutController = controller;
    },
    cancel() {
      stdoutClosed = true;
    },
  });

  let stderrController!: ReadableStreamDefaultController<Uint8Array>;
  let stderrClosed = false;
  const stderr = new ReadableStream<Uint8Array>({
    start(controller) {
      stderrController = controller;
    },
    cancel() {
      stderrClosed = true;
    },
  });

  let resolveExited!: (code: number) => void;
  let exitedResolved = false;
  const exited = new Promise<number>((resolve) => {
    resolveExited = resolve;
  });

  const ctrl: FakeProcessController = {
    stdinChunks,
    get stdinEnded() {
      return stdinEnded;
    },
    signals,
    emitStdout(text: string) {
      if (!stdoutClosed) {
        stdoutController.enqueue(encoder.encode(text));
      }
    },
    emitStdoutJson(obj: unknown) {
      ctrl.emitStdout(`${JSON.stringify(obj)}\n`);
    },
    emitStderr(text: string) {
      if (!stderrClosed) {
        stderrController.enqueue(encoder.encode(text));
      }
    },
    closeStdout() {
      if (!stdoutClosed) {
        stdoutClosed = true;
        try {
          stdoutController.close();
        } catch {
          // Already closed/cancelled.
        }
      }
    },
    exit(code: number) {
      if (opts.closeStdoutOnExit ?? true) {
        ctrl.closeStdout();
      }
      if (!stderrClosed) {
        stderrClosed = true;
        try {
          stderrController.close();
        } catch {
          // Already closed/cancelled.
        }
      }
      if (!exitedResolved) {
        exitedResolved = true;
        resolveExited(code);
      }
    },
    proc: {
      stdin: {
        write(data: string | Uint8Array): number {
          const text = typeof data === "string" ? data : new TextDecoder().decode(data);
          stdinChunks.push(text);
          stdinLineBuf += text;
          const lines = stdinLineBuf.split("\n");
          stdinLineBuf = lines.pop() ?? "";
          for (const line of lines) {
            if (line.length > 0) {
              opts.onStdinLine?.(line, ctrl);
            }
          }
          return text.length;
        },
        end(): void {
          stdinEnded = true;
          if (opts.exitOnStdinEnd !== undefined && opts.exitOnStdinEnd !== null) {
            queueMicrotask(() => ctrl.exit(opts.exitOnStdinEnd!));
          }
        },
      },
      stdout,
      stderr,
      exited,
      kill(signal?: NodeJS.Signals | number): void {
        signals.push(signal);
        if (signal === "SIGINT") {
          if (!opts.ignoreSigint) {
            queueMicrotask(() => ctrl.exit(130));
          }
          return;
        }
        if (signal === "SIGTERM") {
          if (!opts.ignoreSigterm) {
            queueMicrotask(() => ctrl.exit(143));
          }
          return;
        }
        queueMicrotask(() => ctrl.exit(1));
      },
    },
  };

  return ctrl;
}

function baseLaunch(overrides: Partial<InteractiveLaunchSpec> = {}): InteractiveLaunchSpec {
  return {
    argv: ["claude"],
    env: {},
    unsetEnv: [],
    cwd: "/tmp/repo",
    ...overrides,
  };
}

describe("buildClaudeHeadlessArgv", () => {
  test("prepends stream-json headless flags and forwards remaining flags", () => {
    const argv = buildClaudeHeadlessArgv([
      "/usr/local/bin/claude",
      "--model",
      "sonnet",
      "--effort",
      "high",
      "--dangerously-skip-permissions",
    ]);

    expect(argv).toEqual([
      "/usr/local/bin/claude",
      "-p",
      "--input-format",
      "stream-json",
      "--output-format",
      "stream-json",
      "--verbose",
      "--include-partial-messages",
      "--replay-user-messages",
      "--model",
      "sonnet",
      "--effort",
      "high",
      "--dangerously-skip-permissions",
    ]);
  });

  test("deduplicates -p, format, verbose, partial-messages, replay, and session flags", () => {
    const argv = buildClaudeHeadlessArgv(
      [
        "claude",
        "-p",
        "--print",
        "--input-format",
        "text",
        "--output-format=json",
        "--verbose",
        "--include-partial-messages",
        "--replay-user-messages",
        "--resume",
        "old-sess",
        "-r=other-sess",
        "--session-id",
        "11111111-1111-1111-1111-111111111111",
        "--model",
        "opus",
      ],
      { sessionId: "sess-target", resume: true, permissionPromptsHost: true },
    );

    expect(argv).toEqual([
      "claude",
      "-p",
      "--input-format",
      "stream-json",
      "--output-format",
      "stream-json",
      "--verbose",
      "--include-partial-messages",
      "--replay-user-messages",
      "--model",
      "opus",
      "--resume",
      "sess-target",
      "--permission-prompts",
      "host",
    ]);
  });

  test("uses --session-id when sessionId is passed with resume=false and deduplicates existing flags when omitted", () => {
    const withNewSession = buildClaudeHeadlessArgv(
      ["claude", "-c", "--resume", "stale", "--permission-prompts", "none"],
      { sessionId: "22222222-2222-2222-2222-222222222222", resume: false },
    );
    expect(withNewSession).toEqual([
      "claude",
      "-p",
      "--input-format",
      "stream-json",
      "--output-format",
      "stream-json",
      "--verbose",
      "--include-partial-messages",
      "--replay-user-messages",
      "--permission-prompts",
      "none",
      "--session-id",
      "22222222-2222-2222-2222-222222222222",
    ]);

    const dedupLaunchOnly = buildClaudeHeadlessArgv([
      "claude",
      "--resume",
      "first",
      "-r",
      "second",
      "--session-id=uuid-1",
      "--session-id",
      "uuid-2",
    ]);
    expect(dedupLaunchOnly).toEqual([
      "claude",
      "-p",
      "--input-format",
      "stream-json",
      "--output-format",
      "stream-json",
      "--verbose",
      "--include-partial-messages",
      "--replay-user-messages",
      "--resume",
      "second",
      "--session-id",
      "uuid-2",
    ]);
  });
});

describe("ClaudeHeadlessExecutor", () => {
  test("merges process.env + launch.env, deletes unsetEnv, and passes resume argv", async () => {
    const prevKeep = process.env.SPACES_TEST_KEEP;
    const prevUnset = process.env.SPACES_TEST_UNSET;
    process.env.SPACES_TEST_KEEP = "from-process";
    process.env.SPACES_TEST_UNSET = "should-be-removed";

    try {
      let capturedArgv: string[] = [];
      let capturedOpts: { cwd: string; env: Record<string, string | undefined> } | undefined;

      const fake = createFakeProcess({
        exitOnStdinEnd: 0,
        onStdinLine(_line, ctrl) {
          ctrl.emitStdoutJson({
            type: "result",
            subtype: "success",
            is_error: false,
            result: "ok",
            session_id: "sess-resumed",
          });
        },
      });

      const spawn: ClaudeSpawnFn = (argv, opts) => {
        capturedArgv = argv;
        capturedOpts = opts;
        return fake.proc;
      };

      const executor = new ClaudeHeadlessExecutor({
        threadId: "thr_1",
        sessionId: "sess-resumed",
        launch: baseLaunch({
          argv: ["claude", "--model", "opus"],
          cwd: "/workspace/proj",
          env: { SPACES_TEST_ADDED: "from-launch", SPACES_TEST_KEEP: "overridden" },
          unsetEnv: ["SPACES_TEST_UNSET"],
        }),
        spawn,
      });

      expect(executor.agent).toBe("claude");
      expect(executor.threadId).toBe("thr_1");
      expect(executor.getSessionId()).toBe("sess-resumed");

      const res = await executor.sendTurn("ping");
      expect(res.sessionId).toBe("sess-resumed");
      expect(capturedArgv).toContain("--resume");
      expect(capturedArgv).toContain("sess-resumed");
      expect(capturedArgv).toContain("--model");
      expect(capturedOpts?.cwd).toBe("/workspace/proj");
      expect(capturedOpts?.env.SPACES_TEST_ADDED).toBe("from-launch");
      expect(capturedOpts?.env.SPACES_TEST_KEEP).toBe("overridden");
      expect("SPACES_TEST_UNSET" in (capturedOpts?.env ?? {})).toBe(false);

      await executor.stopAndFlush();
    } finally {
      if (prevKeep === undefined) delete process.env.SPACES_TEST_KEEP;
      else process.env.SPACES_TEST_KEEP = prevKeep;
      if (prevUnset === undefined) delete process.env.SPACES_TEST_UNSET;
      else process.env.SPACES_TEST_UNSET = prevUnset;
    }
  });

  test("handles multi-turn bidirectional sendTurn streaming init, deltas, tool_use, tool_result, permission_request, and completed TurnUsage", async () => {
    let spawnCount = 0;
    let turnNum = 0;

    const fake = createFakeProcess({
      exitOnStdinEnd: 0,
      onStdinLine(line, ctrl) {
        turnNum += 1;
        const parsed = JSON.parse(line) as {
          type: string;
          message: { role: string; content: Array<{ type: string; text: string }> };
        };
        expect(parsed.type).toBe("user");
        expect(parsed.message.role).toBe("user");

        if (turnNum === 1) {
          // Echoed user replay (--replay-user-messages) must not emit text_delta.
          ctrl.emitStdoutJson({
            type: "user",
            message: { role: "user", content: [{ type: "text", text: "Turn 1 prompt" }] },
          });
          ctrl.emitStdoutJson({
            type: "system",
            subtype: "init",
            session_id: "sess-live-1",
            model: "claude-sonnet-4-20250514",
          });
          // Split a UTF-8 + NDJSON chunk across two writes to verify partial line & UTF-8 handling.
          const ev1 = JSON.stringify({
            type: "stream_event",
            event: {
              type: "content_block_delta",
              index: 0,
              delta: { type: "text_delta", text: "Hello 🚀 " },
            },
          });
          const splitAt = Math.floor(ev1.length / 2);
          ctrl.emitStdout(ev1.slice(0, splitAt));
          ctrl.emitStdout(ev1.slice(splitAt) + "\n");

          ctrl.emitStdoutJson({
            type: "stream_event",
            event: {
              type: "content_block_start",
              index: 1,
              content_block: { type: "tool_use", id: "tu_1", name: "Read", input: {} },
            },
          });
          ctrl.emitStdoutJson({
            type: "stream_event",
            event: {
              type: "content_block_delta",
              index: 1,
              delta: { type: "input_json_delta", partial_json: "{\"path\":\"src/" },
            },
          });
          ctrl.emitStdoutJson({
            type: "stream_event",
            event: {
              type: "content_block_delta",
              index: 1,
              delta: { type: "input_json_delta", partial_json: "app.ts\"}" },
            },
          });
          ctrl.emitStdoutJson({
            type: "stream_event",
            event: { type: "content_block_stop", index: 1 },
          });
          // Complete assistant snapshot should NOT duplicate streamed text_delta or tu_1.
          ctrl.emitStdoutJson({
            type: "assistant",
            message: {
              id: "msg_1",
              model: "claude-sonnet-4-20250514",
              role: "assistant",
              content: [
                { type: "text", text: "Hello 🚀 " },
                { type: "tool_use", id: "tu_1", name: "Read", input: { path: "src/app.ts" } },
              ],
            },
          });
          ctrl.emitStdoutJson({
            type: "user",
            message: {
              role: "user",
              content: [
                { type: "tool_result", tool_use_id: "tu_1", content: "export const x = 1;", is_error: false },
              ],
            },
          });
          ctrl.emitStdoutJson({
            type: "control_request",
            request_id: "perm_1",
            request: {
              subtype: "can_use_tool",
              tool_name: "Bash",
              decision_reason: "Run unit tests",
            },
          });
          ctrl.emitStdoutJson({
            type: "result",
            subtype: "success",
            is_error: false,
            session_id: "sess-live-1",
            result: "Hello 🚀 done",
            total_cost_usd: 0.01,
            usage: {
              input_tokens: 120,
              output_tokens: 45,
              reasoning_tokens: 15,
              cache_read_input_tokens: 800,
              cache_creation_input_tokens: 50,
            },
          });
        } else {
          // Turn 2 uses assistant-only message (no stream_event).
          ctrl.emitStdoutJson({
            type: "assistant",
            message: {
              id: "msg_2",
              model: "claude-sonnet-4-20250514",
              role: "assistant",
              content: [
                { type: "text", text: "Second reply" },
                { type: "tool_use", id: "tu_2", name: "Edit", input: { file: "a.ts" } },
              ],
            },
          });
          ctrl.emitStdoutJson({
            type: "result",
            subtype: "success",
            is_error: false,
            session_id: "sess-live-1",
            total_cost_usd: 0.02,
            usage: {
              input_tokens: 80,
              output_tokens: 25,
              cache_read_input_tokens: 200,
              cache_creation_input_tokens: 0,
            },
          });
        }
      },
    });

    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_multi",
      launch: baseLaunch(),
      spawn: () => {
        spawnCount += 1;
        return fake.proc;
      },
    });

    const turn1Events: HeadlessTurnEvent[] = [];
    const turn1 = await executor.sendTurn("First prompt", (ev) => turn1Events.push(ev));

    expect(spawnCount).toBe(1);
    expect(executor.isRunning()).toBe(true);
    expect(executor.getSessionId()).toBe("sess-live-1");
    expect(turn1).toEqual({
      sessionId: "sess-live-1",
      replyPreview: "Hello 🚀 done",
      model: "claude-sonnet-4-20250514",
      usage: {
        input_tokens: 120,
        output_tokens: 45,
        reasoning_tokens: 15,
        cache_read_tokens: 800,
        cache_write_tokens: 50,
        cost_usd: 0.01,
        cost_priced: true,
      },
    });

    expect(turn1Events).toEqual([
      { type: "session_init", sessionId: "sess-live-1", model: "claude-sonnet-4-20250514" },
      { type: "text_delta", text: "Hello 🚀 " },
      { type: "tool_use", id: "tu_1", name: "Read", input: { path: "src/app.ts" } },
      { type: "tool_result", id: "tu_1", output: "export const x = 1;", isError: false },
      { type: "permission_request", id: "perm_1", tool: "Bash", description: "Run unit tests" },
      {
        type: "completed",
        sessionId: "sess-live-1",
        replyPreview: "Hello 🚀 done",
        model: "claude-sonnet-4-20250514",
        usage: {
          input_tokens: 120,
          output_tokens: 45,
          reasoning_tokens: 15,
          cache_read_tokens: 800,
          cache_write_tokens: 50,
          cost_usd: 0.01,
          cost_priced: true,
        },
      },
    ]);

    const turn2Events: HeadlessTurnEvent[] = [];
    const turn2 = await executor.sendTurn("Second prompt", (ev) => turn2Events.push(ev));
    expect(spawnCount).toBe(1);
    expect(turn2.replyPreview).toBe("Second reply");
    expect(turn2Events).toEqual([
      { type: "text_delta", text: "Second reply" },
      { type: "tool_use", id: "tu_2", name: "Edit", input: { file: "a.ts" } },
      {
        type: "completed",
        sessionId: "sess-live-1",
        replyPreview: "Second reply",
        model: "claude-sonnet-4-20250514",
        usage: {
          input_tokens: 80,
          output_tokens: 25,
          reasoning_tokens: 0,
          cache_read_tokens: 200,
          cache_write_tokens: 0,
          cost_usd: 0.02,
          cost_priced: true,
        },
      },
    ]);

    expect(fake.stdinChunks).toEqual([
      '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"First prompt"}]}}\n',
      '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Second prompt"}]}}\n',
    ]);

    const flushed = await executor.stopAndFlush();
    expect(fake.stdinEnded).toBe(true);
    expect(executor.isRunning()).toBe(false);
    expect(flushed).toEqual({
      sessionId: "sess-live-1",
      usage: {
        input_tokens: 200,
        output_tokens: 70,
        reasoning_tokens: 15,
        cache_read_tokens: 1000,
        cache_write_tokens: 50,
        cost_usd: 0.03,
        cost_priced: true,
      },
    });
  });

  test("serializes concurrent sendTurn calls without interleaving", async () => {
    const order: string[] = [];
    const fake = createFakeProcess({
      exitOnStdinEnd: 0,
      onStdinLine(line, ctrl) {
        const msg = JSON.parse(line) as {
          message: { content: Array<{ text: string }> };
        };
        const prompt = msg.message.content[0]!.text;
        order.push(`write:${prompt}`);
        setTimeout(() => {
          ctrl.emitStdoutJson({
            type: "result",
            is_error: false,
            session_id: "sess-serial",
            result: `ack:${prompt}`,
          });
        }, 10);
      },
    });

    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_serial",
      launch: baseLaunch(),
      spawn: () => fake.proc,
    });

    const [r1, r2] = await Promise.all([
      executor.sendTurn("one"),
      executor.sendTurn("two"),
    ]);

    expect(order).toEqual(["write:one", "write:two"]);
    expect(r1.replyPreview).toBe("ack:one");
    expect(r2.replyPreview).toBe("ack:two");
    await executor.stopAndFlush();
  });

  test("result with is_error=true emits failed event and rejects cleanly while keeping session usable", async () => {
    let count = 0;
    const fake = createFakeProcess({
      exitOnStdinEnd: 0,
      onStdinLine(_line, ctrl) {
        count += 1;
        if (count === 1) {
          ctrl.emitStdoutJson({
            type: "result",
            subtype: "error_during_execution",
            is_error: true,
            session_id: "sess-err",
            errors: ["Rate limit exceeded"],
            total_cost_usd: 0.002,
            usage: { input_tokens: 10, output_tokens: 2 },
          });
        } else {
          ctrl.emitStdoutJson({
            type: "result",
            subtype: "success",
            is_error: false,
            session_id: "sess-err",
            result: "Recovered",
          });
        }
      },
    });

    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_err",
      launch: baseLaunch(),
      spawn: () => fake.proc,
    });

    const events: HeadlessTurnEvent[] = [];
    let caught: unknown;
    try {
      await executor.sendTurn("trigger error", (ev) => events.push(ev));
    } catch (err) {
      caught = err;
    }

    expect(caught).toBeInstanceOf(ClaudeHeadlessError);
    expect((caught as ClaudeHeadlessError).code).toBe("error_during_execution");
    expect((caught as ClaudeHeadlessError).message).toBe("Rate limit exceeded");
    expect(events).toEqual([
      {
        type: "failed",
        code: "error_during_execution",
        message: "Rate limit exceeded",
        retryable: false,
      },
    ]);

    const recovered = await executor.sendTurn("retry");
    expect(recovered.replyPreview).toBe("Recovered");

    const flushed = await executor.stopAndFlush();
    expect(flushed.sessionId).toBe("sess-err");
    expect(flushed.usage?.input_tokens).toBe(10);
  });

  test("cancel() sends SIGINT and rejects in-flight turn cleanly", async () => {
    const fake = createFakeProcess();
    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_cancel_int",
      launch: baseLaunch(),
      spawn: () => fake.proc,
      cancelGraceMs: 30,
    });

    const events: HeadlessTurnEvent[] = [];
    const turnPromise = executor.sendTurn("long task", (ev) => events.push(ev));
    await new Promise((r) => setTimeout(r, 5));

    await executor.cancel("user aborted");

    await expect(turnPromise).rejects.toThrow("Turn cancelled: user aborted");
    expect(fake.signals).toEqual(["SIGINT"]);
    expect(executor.isRunning()).toBe(false);
    expect(events).toEqual([
      {
        type: "failed",
        code: "cancelled",
        message: "Turn cancelled: user aborted",
        retryable: false,
      },
    ]);
  });

  test("cancel() falls back to SIGTERM after cancelGraceMs when process ignores SIGINT", async () => {
    const fake = createFakeProcess({ ignoreSigint: true });
    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_cancel_term",
      launch: baseLaunch(),
      spawn: () => fake.proc,
      cancelGraceMs: 20,
    });

    const turnPromise = executor.sendTurn("stuck task");
    await new Promise((r) => setTimeout(r, 5));

    await executor.cancel();

    await expect(turnPromise).rejects.toThrow("Turn cancelled");
    expect(fake.signals).toEqual(["SIGINT", "SIGTERM"]);
    expect(executor.isRunning()).toBe(false);
  });

  test("stopAndFlush() escalates to SIGINT/SIGTERM when child ignores stdin EOF", async () => {
    const fake = createFakeProcess({ ignoreSigint: true });
    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_stop_escalate",
      launch: baseLaunch(),
      spawn: () => fake.proc,
      cancelGraceMs: 15,
    });

    // Start a turn that completes cleanly, leaving child alive.
    const turnPromise = executor.sendTurn("hello");
    await new Promise((r) => setTimeout(r, 5));
    fake.emitStdoutJson({
      type: "result",
      is_error: false,
      session_id: "sess-flush",
      result: "hi",
      total_cost_usd: 0.005,
      usage: { input_tokens: 50, output_tokens: 10 },
    });
    await turnPromise;

    const flushed = await executor.stopAndFlush();
    expect(fake.stdinEnded).toBe(true);
    expect(fake.signals).toEqual(["SIGINT", "SIGTERM"]);
    expect(flushed).toEqual({
      sessionId: "sess-flush",
      usage: {
        input_tokens: 50,
        output_tokens: 10,
        reasoning_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd: 0.005,
        cost_priced: true,
      },
    });

    await expect(executor.sendTurn("after stop")).rejects.toThrow(
      "Claude headless session has been stopped",
    );
  });

  test("non-zero unexpected process exit rejects an in-flight turn with stderr context", async () => {
    const fake = createFakeProcess({ closeStdoutOnExit: false });
    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_crash",
      launch: baseLaunch(),
      spawn: () => fake.proc,
    });

    const events: HeadlessTurnEvent[] = [];
    const turnPromise = executor.sendTurn("will crash", (ev) => events.push(ev));
    await new Promise((r) => setTimeout(r, 5));

    fake.emitStderr("fatal: invalid api key");
    fake.exit(2);

    let caught: unknown;
    try {
      await turnPromise;
    } catch (err) {
      caught = err;
    }

    expect(caught).toBeInstanceOf(ClaudeHeadlessError);
    expect((caught as ClaudeHeadlessError).code).toBe("process_exit");
    expect((caught as ClaudeHeadlessError).message).toContain("code 2");
    expect((caught as ClaudeHeadlessError).message).toContain("invalid api key");
    expect(events).toEqual([
      {
        type: "failed",
        code: "process_exit",
        message: (caught as ClaudeHeadlessError).message,
        retryable: false,
      },
    ]);
    expect(executor.isRunning()).toBe(false);
  });

  test("handles modelUsage fallback, direct permission_request/tool_result events, and idle cancel/stop", async () => {
    const fake = createFakeProcess({
      exitOnStdinEnd: 0,
      onStdinLine(_line, ctrl) {
        ctrl.emitStdoutJson({
          type: "stream_event",
          event: {
            type: "content_block_start",
            index: 0,
            content_block: {
              type: "tool_use",
              id: "tu_fallback",
              name: "Glob",
              input: { pattern: "*.ts" },
            },
          },
        });
        ctrl.emitStdoutJson({
          type: "permission_request",
          id: "pr_1",
          tool: "Write",
          description: "Write file",
        });
        ctrl.emitStdoutJson({
          type: "tool_result",
          id: "tu_fallback",
          output: "a.ts\nb.ts",
          is_error: false,
        });
        ctrl.emitStdoutJson({
          type: "result",
          subtype: "success",
          is_error: false,
          session_id: "sess-model-usage",
          result: "Found 2 files",
          modelUsage: {
            "claude-opus-4-1": {
              inputTokens: 300,
              outputTokens: 90,
              cacheReadInputTokens: 400,
              cacheCreationInputTokens: 100,
              costUSD: 0.045,
            },
          },
        });
      },
    });

    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_misc",
      launch: baseLaunch(),
      spawn: () => fake.proc,
    });

    // Idle cancel before any process is spawned is a safe no-op.
    await executor.cancel("nothing running");
    expect(executor.isRunning()).toBe(false);

    const events: HeadlessTurnEvent[] = [];
    const res = await executor.sendTurn("find files", (ev) => events.push(ev));

    expect(res).toEqual({
      sessionId: "sess-model-usage",
      replyPreview: "Found 2 files",
      model: "claude-opus-4-1",
      usage: {
        input_tokens: 300,
        output_tokens: 90,
        reasoning_tokens: 0,
        cache_read_tokens: 400,
        cache_write_tokens: 100,
        cost_usd: 0.045,
        cost_priced: true,
      },
    });

    expect(events).toEqual([
      { type: "permission_request", id: "pr_1", tool: "Write", description: "Write file" },
      { type: "tool_result", id: "tu_fallback", output: "a.ts\nb.ts", isError: false },
      { type: "tool_use", id: "tu_fallback", name: "Glob", input: { pattern: "*.ts" } },
      {
        type: "completed",
        sessionId: "sess-model-usage",
        replyPreview: "Found 2 files",
        model: "claude-opus-4-1",
        usage: {
          input_tokens: 300,
          output_tokens: 90,
          reasoning_tokens: 0,
          cache_read_tokens: 400,
          cache_write_tokens: 100,
          cost_usd: 0.045,
          cost_priced: true,
        },
      },
    ]);

    const flushed = await executor.stopAndFlush();
    expect(flushed.sessionId).toBe("sess-model-usage");
    expect(flushed.usage?.cost_usd).toBe(0.045);

    // Calling stopAndFlush again is idempotent.
    const flushedAgain = await executor.stopAndFlush();
    expect(flushedAgain).toEqual(flushed);
  });

  test("delivers result buffered just before zero/non-zero process exit", async () => {
    const fake = createFakeProcess({
      onStdinLine(_line, ctrl) {
        ctrl.emitStdoutJson({
          type: "result",
          subtype: "success",
          is_error: false,
          session_id: "sess-race",
          result: "final answer",
        });
        ctrl.exit(0);
      },
    });

    const executor = new ClaudeHeadlessExecutor({
      threadId: "thr_race",
      launch: baseLaunch(),
      spawn: () => fake.proc,
    });

    const res = await executor.sendTurn("quick");
    expect(res.sessionId).toBe("sess-race");
    expect(res.replyPreview).toBe("final answer");
    expect(executor.isRunning()).toBe(false);
  });
});

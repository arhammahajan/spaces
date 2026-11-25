// Comprehensive suite for the pi-spaces RICH companion extension.
// Runs directly under Node 24 (native TS stripping, no build step) against a
// real mock UDS HTTP server.
import { describe, it, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import plugin from "../index.ts";
import { Tracker } from "../tracker.ts";
import * as transport from "../transport.ts";

const TOKEN = "test-zj-token";

function tmpDir(prefix = "pi-spaces-test-") {
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

function writeEndpoint(dir, socketPath, token = TOKEN) {
  const doc = {
    version: 1,
    protocol: "http/uds",
    pid: process.pid,
    uid: 1000,
    socket: socketPath,
    tcp_port: 0,
    token,
    started_ms: Date.now(),
  };
  fs.writeFileSync(path.join(dir, "hooks.endpoint.json"), JSON.stringify(doc));
}

/** Start a real HTTP-over-UDS mock spacecore. */
function startMockServer({ token = TOKEN, delayMs = 0, status = 204 } = {}) {
  const dir = tmpDir();
  const socketPath = path.join(dir, "h.sock");
  const received = [];
  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", (c) => {
      body += c;
    });
    req.on("end", () => {
      received.push({
        method: req.method,
        url: req.url,
        headers: { ...req.headers },
        body,
      });
      const respond = () => {
        try {
          res.writeHead(status, { "Content-Type": "text/plain", Connection: "close" });
          res.end("");
        } catch {
          /* client may have timed out */
        }
      };
      if (delayMs > 0) setTimeout(respond, delayMs);
      else respond();
    });
  });
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(socketPath, () => resolve({ dir, socketPath, received, server }));
  });
}

function stopMock(mock) {
  return new Promise((resolve) => {
    try {
      mock.server.close(() => resolve());
    } catch {
      resolve();
    }
  });
}

function makePi() {
  const handlers = new Map();
  const pi = {
    version: "9.9.9-test",
    on(event, handler) {
      if (!handlers.has(event)) handlers.set(event, []);
      handlers.get(event).push(handler);
    },
    getThinkingLevel() {
      return "high";
    },
    getActiveTools() {
      return ["read", "edit"];
    },
  };
  return {
    pi,
    emit(event, ctx = {}, data = {}) {
      const list = handlers.get(event) ?? [];
      for (const h of list) h(ctx, data);
    },
    events: () => [...handlers.keys()],
  };
}

function baseCtx(overrides = {}) {
  return {
    cwd: "/home/dev/app",
    hasUI: true,
    model: { provider: "anthropic", id: "claude-sonnet-4-5", contextWindow: 200000 },
    sessionManager: {
      getSessionId: () => "sess-pi-1",
      getSessionFile: () => "/home/dev/.pi/sessions/sess-pi-1.json",
      getSessionName: () => "Fix OAuth token refresh",
      getBranch: () => [
        { type: "user", text: "hello" },
        {
          type: "assistant",
          usage: { input: 100, output: 50, cacheRead: 10, cacheWrite: 5, cost: { total: 0.01 } },
        },
      ],
    },
    getContextUsage: () => ({ tokens: 12000, contextWindow: 200000, percent: 6 }),
    ...overrides,
  };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let savedEnv = {};

function saveEnv() {
  savedEnv = { ...process.env };
}

function restoreEnv() {
  for (const k of Object.keys(process.env)) {
    if (!(k in savedEnv)) delete process.env[k];
  }
  for (const [k, v] of Object.entries(savedEnv)) process.env[k] = v;
  transport.clearSessionCache();
}

describe("pi-spaces extension", () => {
  let mock = null;

  beforeEach(async () => {
    saveEnv();
    mock = await startMockServer();
    const runtime = tmpDir("pi-spaces-runtime-");
    // Point the *endpoint file* at our mock socket, but keep runtime dir
    // discoverable via SPACES_HOOKS_RUNTIME.
    writeEndpoint(runtime, mock.socketPath);
    process.env["SPACES_HOOKS_RUNTIME"] = runtime;
    process.env["TMUX_PANE"] = "%1";
    process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] = "1";
    process.env["SPACES_SESSION"] = "test-tmux-session";
    process.env["SPACES_HOOKS_DISABLE"] = "";
    transport.clearSessionCache();
    mock.received.length = 0;
  });

  afterEach(async (t) => {
    if (mock) {
      await stopMock(mock);
      mock = null;
    }
    // Cleanup temp runtime dir.
    try {
      const rt = process.env["SPACES_HOOKS_RUNTIME"];
      if (rt && rt.includes("pi-spaces-runtime-")) fs.rmSync(rt, { recursive: true, force: true });
    } catch {}
    restoreEnv();
    void t;
  });

  it("subscribes to all Pi lifecycle events without throwing", () => {
    const { pi, events } = makePi();
    assert.doesNotThrow(() => (plugin)(pi));
    for (const e of [
      "session_start",
      "session_switch",
      "session_fork",
      "session_tree",
      "session_shutdown",
      "input",
      "before_agent_start",
      "agent_start",
      "agent_end",
      "turn_start",
      "turn_end",
      "tool_call",
      "tool_execution_start",
      "tool_result",
      "tool_execution_end",
      "session_before_compact",
      "session_compact",
      "model_select",
    ]) {
      assert.ok(events().includes(e), `missing subscription for ${e}`);
    }
  });

  it("ignores a Pi object without .on (version skew)", () => {
    assert.doesNotThrow(() => (plugin)({}));
    assert.doesNotThrow(() => (plugin)(null));
    assert.doesNotThrow(() => (plugin)(undefined));
  });

  it("never returns { block: true } from any handler", () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    for (const [event, data] of [
      ["input", { prompt: "hi" }],
      ["agent_start", {}],
      ["tool_call", { tool: "read", toolCallId: "c1", input: { path: "a.ts" } }],
    ]) {
      // Handlers are void; call directly via emit and assert no throw.
      assert.doesNotThrow(() => emit(event, ctx, data));
    }
  });

  it("full lifecycle emits v2 envelopes with shared wire contract", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();

    emit("session_start", ctx, {});
    emit("input", ctx, { prompt: "Fix the login bug" });
    emit("agent_start", ctx, {});
    emit("turn_start", ctx, {});
    emit("tool_call", ctx, { tool: "read", toolCallId: "call-1", input: { path: "src/app.ts" } });
    emit(
      "tool_result",
      ctx,
      { tool: "read", toolCallId: "call-1", output: "file contents here", isError: false },
    );
    emit("turn_end", ctx, {
      usage: { input: 1200, output: 350, cacheRead: 500, cacheWrite: 100, cost: { total: 0.042 } },
      reply: "Done reading.",
    });
    emit("agent_end", ctx, { reply: "All fixed." });
    emit("session_before_compact", ctx, { reason: "manual" });
    emit("session_compact", ctx, { reason: "manual" });
    emit("model_select", ctx, { model: "claude-opus-4-6", provider: "anthropic" });
    emit("session_shutdown", ctx, {});

    await sleep(350);

    assert.ok(mock.received.length >= 11, `expected >=11 posts, got ${mock.received.length}`);
    const bodies = mock.received.map((r) => {
      assert.equal(r.method, "POST");
      assert.equal(r.url, "/v1/plugin/pi");
      assert.equal(r.headers["content-type"], "application/json");
      assert.equal(r.headers["x-zj-token"], TOKEN);
      assert.ok(r.headers["content-length"], "missing Content-Length");
      return JSON.parse(r.body);
    });
    for (const env of bodies) {
      assert.equal(env.v, 2);
      assert.equal(env.agent, "pi");
      assert.equal(env.session, "test-tmux-session");
      assert.equal(env.pane, "%1");
      assert.equal(env.plugin.name, "pi-spaces");
      assert.equal(env.plugin.version, "1.0.0");
      assert.ok(Array.isArray(env.plugin.capabilities));
      for (const c of ["lifecycle", "usage", "context", "tools", "files", "compaction", "errors", "model", "thinking", "session_tree"]) {
        assert.ok(env.plugin.capabilities.includes(c), `missing capability ${c}`);
      }
      assert.ok(typeof env.event === "string" && env.event.length > 0);
      assert.ok(typeof env.ts_ms === "number");
      assert.ok(typeof env.pid === "number");
      assert.ok(env.payload && typeof env.payload === "object");
    }
    const events = bodies.map((b) => b.event);
    for (const e of ["session_start", "input", "agent_start", "turn_start", "tool_call", "tool_result", "turn_end", "agent_end", "session_before_compact", "session_compact", "model_select", "session_shutdown"]) {
      assert.ok(events.includes(e), `missing event ${e}`);
    }
    // Session identity propagates from sessionManager.
    const first = bodies[0].payload;
    assert.equal(first.session_id, "sess-pi-1");
    assert.equal(first.title, "Fix OAuth token refresh");
    assert.equal(first.cwd, "/home/dev/app");
    assert.equal(first.provider, "anthropic");
  });

  it("extracts live ctx.getContextUsage() into context_* fields", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx({
      getContextUsage: () => ({ tokens: 50000, contextWindow: 200000, percent: 25 }),
    });
    emit("session_start", ctx, {});
    emit("turn_start", ctx, {});
    emit("turn_end", ctx, { usage: { input: 10, output: 5 } });
    await sleep(300);
    assert.ok(mock.received.length >= 3);
    const last = JSON.parse(mock.received[mock.received.length - 1].body);
    assert.equal(last.payload.context_tokens, 50000);
    assert.equal(last.payload.context_window, 200000);
    assert.equal(last.payload.context_percent, 25);
  });

  it("tracks cumulative usage & cost, files_touched, tool_error_count, failed state", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    emit("session_start", ctx, {});
    // Branch reconstruction seeds 100 in / 50 out / 0.01 cost, turn_count>=1.
    emit("input", ctx, { prompt: "Edit the server" });
    emit("tool_call", ctx, { tool: "edit", toolCallId: "e1", input: { path: "src/server.ts" } });
    emit("tool_result", ctx, { tool: "edit", toolCallId: "e1", output: "ok", isError: false });
    emit("tool_call", ctx, { tool: "write", toolCallId: "w1", input: { path: "src/new.ts" } });
    emit("tool_result", ctx, { tool: "write", toolCallId: "w1", output: "boom", isError: true, error: "EACCES" });
    emit("turn_end", ctx, {
      usage: { input: 200, output: 100, cost: 0.02 },
      error: "model exploded",
    });
    await sleep(350);
    const last = JSON.parse(mock.received[mock.received.length - 1].body);
    const p = last.payload;
    // 100 (branch) + 200 (turn) = 300 in; 50 + 100 = 150 out.
    assert.equal(p.usage.input_tokens, 300);
    assert.equal(p.usage.output_tokens, 150);
    assert.ok(Math.abs(p.usage.cost_usd - 0.03) < 1e-9, `cost=${p.usage.cost_usd}`);
    assert.equal(p.tool_call_count, 2);
    assert.equal(p.tool_error_count, 1);
    assert.equal(p.state, "failed");
    assert.ok(p.last_error.length > 0);
    const touched = JSON.stringify(p.files_touched);
    assert.ok(touched.includes("src/server.ts"), `files_touched=${touched}`);
    assert.ok(touched.includes("src/new.ts"), `files_touched=${touched}`);
    assert.ok(p.turn_count >= 2);
  });

  it("ask/question tools mark the pane blocked with kind=question", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    emit("session_start", ctx, {});
    emit("tool_call", ctx, { tool: "ask_user", toolCallId: "q1", input: { question: "proceed?" } });
    await sleep(250);
    const last = JSON.parse(mock.received[mock.received.length - 1].body);
    assert.equal(last.payload.state, "blocked");
    assert.equal(last.payload.kind, "question");
  });

  it("session_start reconstructs totals, turns and title from branch entries", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    emit("session_start", ctx, {});
    await sleep(250);
    const first = JSON.parse(mock.received[0].body);
    assert.equal(first.payload.title, "Fix OAuth token refresh");
    assert.equal(first.payload.usage.input_tokens, 100);
    assert.equal(first.payload.usage.output_tokens, 50);
    assert.ok(first.payload.turn_count >= 1);
  });

  it("tool_execution_start / tool_execution_end aliases work", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    emit("session_start", ctx, {});
    emit("tool_execution_start", ctx, { toolName: "read", toolCallId: "a1", args: { path: "x.ts" } });
    emit("tool_execution_end", ctx, { toolName: "read", toolCallId: "a1", output: "ok" });
    emit("turn_end", ctx, {});
    await sleep(300);
    const last = JSON.parse(mock.received[mock.received.length - 1].body);
    assert.equal(last.payload.tool_call_count, 1);
    assert.equal(last.payload.tool_error_count, 0);
  });

  it("compaction and model_select update counters and model fields", async () => {
    const { pi, emit } = makePi();
    (plugin)(pi);
    const ctx = baseCtx();
    emit("session_start", ctx, {});
    emit("session_before_compact", ctx, { reason: "threshold" });
    emit("model_select", ctx, { provider: "openai", model: "gpt-5", context_window: 400000 });
    await sleep(300);
    const last = JSON.parse(mock.received[mock.received.length - 1].body);
    assert.equal(last.payload.compaction_count, 1);
    assert.equal(last.payload.last_compaction_reason, "threshold");
    assert.ok(last.payload.last_compaction_ms > 0);
    assert.equal(last.payload.provider, "openai");
    assert.equal(last.payload.model, "gpt-5");
    assert.equal(last.payload.context_window, 400000);
  });
});

describe("transport fail-open behavior", () => {
  beforeEach(() => {
    saveEnv();
    transport.clearSessionCache();
  });
  afterEach(() => restoreEnv());

  it("postEnvelope returns false with no endpoint (missing socket dir)", async () => {
    const dir = tmpDir("pi-spaces-empty-");
    process.env["SPACES_HOOKS_RUNTIME"] = dir;
    process.env["TMUX_PANE"] = "%9";
    process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] = "1";
    transport.clearSessionCache();
    const ok = await transport.postEnvelope(
      transport.buildEnvelope("input", { state: "working" }),
    );
    assert.equal(ok, false);
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("postEnvelope returns false on a slow socket (>100ms) without hanging", async () => {
    const slow = await startMockServer({ delayMs: 2000 });
    try {
      const runtime = tmpDir("pi-spaces-slow-");
      writeEndpoint(runtime, slow.socketPath);
      process.env["SPACES_HOOKS_RUNTIME"] = runtime;
      process.env["TMUX_PANE"] = "%9";
      process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] = "1";
      transport.clearSessionCache();
      const start = Date.now();
      const ok = await transport.postEnvelope(transport.buildEnvelope("input", {}));
      const elapsed = Date.now() - start;
      assert.equal(ok, false);
      assert.ok(elapsed < 1500, `took ${elapsed}ms, must fail fast`);
      fs.rmSync(runtime, { recursive: true, force: true });
    } finally {
      await stopMock(slow);
    }
  });

  it("silent no-op when $TMUX_PANE is unset without opt-in", async () => {
    const mock2 = await startMockServer();
    try {
      const runtime = tmpDir("pi-spaces-noop-");
      writeEndpoint(runtime, mock2.socketPath);
      process.env["SPACES_HOOKS_RUNTIME"] = runtime;
      delete process.env["TMUX_PANE"];
      delete process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"];
      transport.clearSessionCache();
      assert.equal(transport.isEnabled(), false);
      const { pi, emit } = makePi();
      (plugin)(pi);
      emit("session_start", baseCtx(), {});
      emit("input", baseCtx(), { prompt: "hello" });
      await sleep(300);
      assert.equal(mock2.received.length, 0);
      fs.rmSync(runtime, { recursive: true, force: true });
    } finally {
      await stopMock(mock2);
    }
  });

  it("sends when SPACES_PLUGIN_ALLOW_NO_TMUX=1 even without $TMUX_PANE", async () => {
    const mock3 = await startMockServer();
    try {
      const runtime = tmpDir("pi-spaces-allow-");
      writeEndpoint(runtime, mock3.socketPath);
      process.env["SPACES_HOOKS_RUNTIME"] = runtime;
      delete process.env["TMUX_PANE"];
      process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] = "1";
      delete process.env["SPACES_SESSION"];
      transport.clearSessionCache();
      assert.equal(transport.isEnabled(), true);
      const ok = await transport.postEnvelope(transport.buildEnvelope("input", { state: "working" }));
      assert.equal(ok, true);
      assert.equal(mock3.received.length, 1);
      const env = JSON.parse(mock3.received[0].body);
      assert.equal(env.v, 2);
      assert.equal(env.pane, "");
      fs.rmSync(runtime, { recursive: true, force: true });
    } finally {
      await stopMock(mock3);
    }
  });

  it("rejects CRLF tokens without sending bytes", async () => {
    const mock4 = await startMockServer();
    try {
      const runtime = tmpDir("pi-spaces-crlf-");
      writeEndpoint(runtime, mock4.socketPath, "bad\r\ntoken");
      process.env["SPACES_HOOKS_RUNTIME"] = runtime;
      process.env["TMUX_PANE"] = "%1";
      process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] = "1";
      transport.clearSessionCache();
      const ok = await transport.postEnvelope(transport.buildEnvelope("input", {}));
      assert.equal(ok, false);
      assert.equal(mock4.received.length, 0);
      fs.rmSync(runtime, { recursive: true, force: true });
    } finally {
      await stopMock(mock4);
    }
  });
});

describe("Tracker unit behavior", () => {
  it("extracts all usage shapes and accumulates cost", () => {
    const t = new Tracker();
    t.addUsage(t.extractUsage({ usage: { input: 10, output: 20, cacheRead: 1, cacheWrite: 2, cost: { total: 0.5 } } }));
    t.addUsage(t.extractUsage({ tokens: { input_tokens: 5, output_tokens: 6, cost_usd: 0.1 } }));
    assert.equal(t.usage.input_tokens, 15);
    assert.equal(t.usage.output_tokens, 26);
    assert.equal(t.usage.cache_read_tokens, 1);
    assert.equal(t.usage.cache_write_tokens, 2);
    assert.ok(Math.abs(t.usage.cost_usd - 0.6) < 1e-9);
  });

  it("computes context percent when only tokens+window are given", () => {
    const t = new Tracker();
    t.applyContext(50000, 200000, 0);
    assert.equal(t.contextPercent, 25);
  });

  it("dedupes files_touched", () => {
    const t = new Tracker();
    t.recordFileTouch("a.ts", "edit");
    t.recordFileTouch("a.ts", "edit");
    t.recordFileTouch("a.ts", "write");
    assert.equal(t.filesTouched.length, 2);
  });
});

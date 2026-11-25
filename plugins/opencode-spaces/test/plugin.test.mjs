// test/plugin.test.mjs — comprehensive node:test suite for opencode-spaces.
//
// Uses a real mock Unix domain socket HTTP server and the real transport, so
// the v2 envelopes, headers, and fail-open behavior are verified end to end.

import { test } from "node:test";
import assert from "node:assert/strict";
import net from "node:net";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { createHooks } from "../index.mjs";
import { Tracker, PLUGIN_META } from "../tracker.mjs";
import {
  postEnvelope,
  resolveSocketPath,
  resolveToken,
  getTmuxSession,
  resetTransportCache,
} from "../transport.mjs";

// ---------------------------------------------------------------------------
// Mock UDS HTTP server
// ---------------------------------------------------------------------------

async function startMockServer({ hang = false } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "spaces-test-"));
  const sock = path.join(dir, "hooks.sock");
  const received = [];
  const server = net.createServer((conn) => {
    let buf = Buffer.alloc(0);
    let headerEnd = -1;
    let headers = {};
    let contentLength = 0;
    let requestLine = "";
    conn.on("data", (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      if (headerEnd === -1) {
        const idx = buf.indexOf("\r\n\r\n");
        if (idx !== -1) {
          headerEnd = idx + 4;
          const head = buf.slice(0, idx).toString();
          const lines = head.split("\r\n");
          requestLine = lines[0] || "";
          for (const line of lines.slice(1)) {
            const c = line.indexOf(":");
            if (c > -1) {
              headers[line.slice(0, c).trim().toLowerCase()] =
                line.slice(c + 1).trim();
            }
          }
          contentLength = parseInt(headers["content-length"] || "0", 10) || 0;
        }
      }
      if (headerEnd !== -1 && buf.length >= headerEnd + contentLength) {
        const raw = buf.slice(headerEnd, headerEnd + contentLength).toString();
        let body = null;
        try {
          body = JSON.parse(raw);
        } catch {
          body = null;
        }
        received.push({ requestLine, headers: { ...headers }, body, raw });
        headers = {};
        buf = Buffer.alloc(0);
        headerEnd = -1;
        if (!hang) {
          conn.end(
            "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok",
          );
        }
      }
    });
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(sock, resolve);
  });
  return {
    dir,
    sock,
    server,
    received,
    async close() {
      await new Promise((resolve) => server.close(resolve));
      fs.rmSync(dir, { recursive: true, force: true });
    },
  };
}

// ---------------------------------------------------------------------------
// Env helpers (tests share process.env; save/restore around each case)
// ---------------------------------------------------------------------------

const MANAGED_KEYS = [
  "TMUX_PANE",
  "SPACES_TMUX_SESSION",
  "SPACES_HOOKS_RUNTIME",
  "SPACES_HOOKS_SOCKET",
  "SPACES_HOOKS_TOKEN",
  "SPACES_PLUGIN_ALLOW_NO_TMUX",
  "SPACES_HOOKS_TIMEOUT_MS",
  "XDG_RUNTIME_DIR",
];

function setEnv(vars) {
  const saved = new Map();
  for (const k of MANAGED_KEYS) {
    saved.set(k, process.env[k]);
    delete process.env[k];
  }
  for (const [k, v] of Object.entries(vars || {})) {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  resetTransportCache();
  return () => {
    for (const k of MANAGED_KEYS) {
      const v = saved.get(k);
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    resetTransportCache();
  };
}

function mockEnv(server, extra = {}) {
  return setEnv({
    TMUX_PANE: "%test-pane",
    SPACES_TMUX_SESSION: "test-tmux-session",
    SPACES_HOOKS_RUNTIME: server.dir,
    SPACES_HOOKS_TOKEN: "test-token-123",
    ...extra,
  });
}

function makePlugin() {
  const tracker = new Tracker();
  const hooks = createHooks({ tracker });
  return { tracker, hooks };
}

const lastEnvelope = (server) => server.received[server.received.length - 1];

function checkBaseEnvelope(entry, eventName) {
  assert.equal(entry.requestLine, "POST /v1/plugin/opencode HTTP/1.1");
  assert.equal(entry.headers["content-type"], "application/json");
  assert.ok(entry.headers["content-length"], "Content-Length header present");
  assert.equal(
    Number(entry.headers["content-length"]),
    Buffer.byteLength(entry.raw),
  );
  assert.equal(entry.headers["x-zj-token"], "test-token-123");
  const env = entry.body;
  assert.equal(env.v, 2);
  assert.equal(env.agent, "opencode");
  assert.equal(env.session, "test-tmux-session");
  assert.equal(env.pane, "%test-pane");
  assert.equal(env.plugin.name, "opencode-spaces");
  assert.equal(env.plugin.version, PLUGIN_META.version);
  assert.deepEqual(
    [...env.plugin.capabilities].sort(),
    [...PLUGIN_META.capabilities].sort(),
  );
  assert.equal(env.event, eventName);
  assert.ok(typeof env.ts_ms === "number" && env.ts_ms > 0);
  assert.ok(typeof env.pid === "number");
  assert.ok(env.payload && typeof env.payload === "object");
  return env;
}

// ---------------------------------------------------------------------------
// Wire contract
// ---------------------------------------------------------------------------

test("mock server receives exact v2 envelope with X-ZJ-Token header", async (t) => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: {
          info: { id: "ses-root", title: "hello", directory: "/tmp/work" },
        },
      },
    });
    assert.equal(server.received.length, 1);
    const env = checkBaseEnvelope(lastEnvelope(server), "session.created");
    assert.equal(env.payload.session_id, "ses-root");
    assert.equal(env.payload.title, "hello");
    assert.equal(env.payload.cwd, "/tmp/work");
    assert.equal(env.payload.is_subagent, false);
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

test("session lifecycle: created -> status busy -> idle", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-1", title: "t" } },
      },
    });
    await hooks.event({
      event: {
        type: "session.status",
        properties: { sessionID: "ses-1", status: { type: "busy" } },
      },
    });
    await hooks.event({
      event: {
        type: "session.status",
        properties: { sessionID: "ses-1", status: { type: "idle" } },
      },
    });
    assert.equal(server.received.length, 3);
    assert.equal(server.received[1].body.payload.state, "working");
    assert.equal(server.received[2].body.payload.state, "idle");
    assert.equal(server.received[2].body.event, "session.status");
  } finally {
    restore();
    await server.close();
  }
});

test("session.status retry with rate-limit message sets rate_limited", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-r" } },
      },
    });
    await hooks.event({
      event: {
        type: "session.status",
        properties: {
          sessionID: "ses-r",
          status: {
            type: "retry",
            attempt: 3,
            message: "Rate limit exceeded, retrying (429)",
          },
        },
      },
    });
    const payload = lastEnvelope(server).body.payload;
    assert.equal(payload.state, "blocked");
    assert.equal(payload.retry_count, 3);
    assert.equal(payload.rate_limited, true);
    assert.match(payload.rate_limit_message, /Rate limit/);
  } finally {
    restore();
    await server.close();
  }
});

test("session.error records last_error and failed state", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-e" } },
      },
    });
    await hooks.event({
      event: {
        type: "session.error",
        properties: { sessionID: "ses-e", error: { message: "boom" } },
      },
    });
    const payload = lastEnvelope(server).body.payload;
    assert.equal(payload.last_error, "boom");
    assert.equal(payload.state, "failed");
    assert.ok(payload.raw_event, "raw_event present");
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Messages: tokens, cost, context
// ---------------------------------------------------------------------------

test("message/token/cost/context extraction via chat.params + message.updated", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-m" } },
      },
    });
    await hooks["chat.params"]({
      sessionID: "ses-m",
      model: {
        providerID: "anthropic",
        modelID: "claude-x",
        variant: "opus",
        limit: { context: 200000 },
      },
      agent: "build",
    });
    await hooks.event({
      event: {
        type: "message.updated",
        properties: {
          info: {
            sessionID: "ses-m",
            role: "assistant",
            model: { providerID: "anthropic", modelID: "claude-x" },
            tokens: {
              input: 50000,
              output: 1000,
              reasoning: 200,
              cache: { read: 40000, write: 5000 },
            },
            cost: 1.25,
            parts: [{ type: "text", text: "done thing" }],
          },
        },
      },
    });
    const payload = lastEnvelope(server).body.payload;
    assert.equal(payload.provider, "anthropic");
    assert.equal(payload.model, "claude-x");
    assert.equal(payload.effort, "opus");
    assert.equal(payload.mode, "build");
    assert.deepEqual(payload.usage, {
      input_tokens: 50000,
      output_tokens: 1000,
      reasoning_tokens: 200,
      cache_read_tokens: 40000,
      cache_write_tokens: 5000,
      cost_usd: 1.25,
    });
    assert.equal(payload.context_tokens, 50000);
    assert.equal(payload.context_window, 200000);
    assert.equal(payload.context_percent, 25);
    assert.equal(payload.turn_count, 1);
    assert.equal(payload.reply, "done thing");
    // User prompt extraction
    await hooks.event({
      event: {
        type: "message.updated",
        properties: {
          info: {
            sessionID: "ses-m",
            role: "user",
            parts: [{ type: "text", text: "please fix it" }],
          },
        },
      },
    });
    assert.equal(lastEnvelope(server).body.payload.prompt, "please fix it");
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Tools: before/after timing + part updates
// ---------------------------------------------------------------------------

test("tool.execute.before/after timing, counts, and recent_tools", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { tracker, hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-t" } },
      },
    });
    await hooks["tool.execute.before"]({
      sessionID: "ses-t",
      callID: "call-1",
      tool: "read",
      input: { path: "/tmp/a.txt" },
    });
    let payload = lastEnvelope(server).body.payload;
    assert.equal(payload.active_tools.length, 1);
    assert.equal(payload.active_tools[0].tool, "read");
    assert.equal(payload.tool_call_id, "call-1");
    assert.match(payload.tool_input, /a\.txt/);

    await new Promise((r) => setTimeout(r, 5));
    await hooks["tool.execute.after"](
      { sessionID: "ses-t", callID: "call-1" },
      { output: "file contents here" },
    );
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.active_tools.length, 0);
    assert.equal(payload.tool_call_count, 1);
    assert.equal(payload.tool_error_count, 0);
    assert.equal(payload.recent_tools.length, 1);
    assert.equal(payload.recent_tools[0].tool, "read");
    assert.ok(payload.recent_tools[0].duration_ms >= 0);
    assert.match(payload.tool_output, /file contents/);
    assert.equal(tracker.stateFor("ses-t").tool_call_count, 1);

    // Error output path
    await hooks["tool.execute.before"]({
      sessionID: "ses-t",
      callID: "call-2",
      tool: "bash",
    });
    await hooks["tool.execute.after"](
      { sessionID: "ses-t", callID: "call-2" },
      { error: "exit 1" },
    );
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.tool_call_count, 2);
    assert.equal(payload.tool_error_count, 1);
    assert.equal(payload.recent_tools[0].state, "error");
  } finally {
    restore();
    await server.close();
  }
});

test("message.part.updated tool lifecycle + text delta throttling", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-p" } },
      },
    });
    const base = server.received.length;
    await hooks.event({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses-p",
          part: { type: "tool", callID: "c1", tool: "edit", state: "running" },
        },
      },
    });
    let payload = lastEnvelope(server).body.payload;
    assert.equal(payload.active_tools.length, 1);
    await hooks.event({
      event: {
        type: "message.part.updated",
        properties: {
          sessionID: "ses-p",
          part: {
            type: "tool",
            callID: "c1",
            tool: "edit",
            state: "completed",
            output: "ok",
          },
        },
      },
    });
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.tool_call_count, 1);
    assert.equal(payload.recent_tools[0].tool, "edit");

    // Rapid text deltas are coalesced (max 1 push per 250ms).
    const beforeText = server.received.length;
    for (let i = 0; i < 10; i++) {
      await hooks.event({
        event: {
          type: "message.part.updated",
          properties: {
            sessionID: "ses-p",
            part: { type: "text", text: `chunk ${i}` },
          },
        },
      });
    }
    assert.ok(
      server.received.length - beforeText <= 2,
      `expected throttled text pushes, got ${server.received.length - beforeText}`,
    );
    assert.equal(base >= 0, true);
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Permissions & questions
// ---------------------------------------------------------------------------

test("permission.asked/replied round-trip", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-perm" } },
      },
    });
    await hooks.event({
      event: {
        type: "permission.asked",
        properties: {
          sessionID: "ses-perm",
          id: "perm-1",
          permission: {
            tool: "bash",
            permission: "ask",
            title: "Run command?",
            patterns: ["rm *"],
            metadata: { cwd: "/tmp" },
          },
        },
      },
    });
    let payload = lastEnvelope(server).body.payload;
    assert.equal(payload.state, "blocked");
    assert.equal(payload.kind, "permission");
    assert.deepEqual(payload.pending_permission, {
      id: "perm-1",
      tool: "bash",
      permission: "ask",
      title: "Run command?",
      patterns: ["rm *"],
      metadata: { cwd: "/tmp" },
    });
    await hooks.event({
      event: {
        type: "permission.replied",
        properties: { sessionID: "ses-perm", id: "perm-1", reply: "yes" },
      },
    });
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.pending_permission, null);
    assert.equal(payload.state, "working");
    assert.equal(payload.reply, "yes");
  } finally {
    restore();
    await server.close();
  }
});

test("question.asked/replied round-trip", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-q" } },
      },
    });
    await hooks.event({
      event: {
        type: "question.asked",
        properties: {
          sessionID: "ses-q",
          id: "q-1",
          question: {
            text: "Which approach?",
            questions: [
              {
                text: "Which approach?",
                options: [{ label: "A" }, { label: "B" }],
              },
            ],
          },
        },
      },
    });
    let payload = lastEnvelope(server).body.payload;
    assert.equal(payload.state, "blocked");
    assert.equal(payload.kind, "question");
    assert.equal(payload.text, "Which approach?");
    assert.equal(payload.pending_question.questions[0].options.length, 2);
    await hooks.event({
      event: {
        type: "question.replied",
        properties: { sessionID: "ses-q", id: "q-1", reply: "A" },
      },
    });
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.pending_question, null);
    assert.equal(payload.state, "working");
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Files, todos, lsp, vcs, compaction
// ---------------------------------------------------------------------------

test("session.diff and file.edited accumulate files_touched", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-f" } },
      },
    });
    await hooks.event({
      event: {
        type: "session.diff",
        properties: {
          sessionID: "ses-f",
          diff: "diff --git\n+++ b/src/a.ts\n+++ b/src/b.ts\n",
        },
      },
    });
    await hooks.event({
      event: {
        type: "file.edited",
        properties: { sessionID: "ses-f", file: "/tmp/work/c.ts" },
      },
    });
    const payload = lastEnvelope(server).body.payload;
    const paths = payload.files_touched.map((f) => f.path);
    assert.ok(paths.includes("src/a.ts"));
    assert.ok(paths.includes("src/b.ts"));
    assert.ok(paths.includes("/tmp/work/c.ts"));
  } finally {
    restore();
    await server.close();
  }
});

test("todo.updated stores full todo list", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-todo" } },
      },
    });
    await hooks.event({
      event: {
        type: "todo.updated",
        properties: {
          sessionID: "ses-todo",
          todos: [
            { id: "1", content: "write code", status: "in_progress", priority: "high" },
            { id: "2", content: "write tests", status: "pending", priority: "medium" },
          ],
        },
      },
    });
    assert.deepEqual(lastEnvelope(server).body.payload.todos, [
      { id: "1", content: "write code", status: "in_progress", priority: "high" },
      { id: "2", content: "write tests", status: "pending", priority: "medium" },
    ]);
  } finally {
    restore();
    await server.close();
  }
});

test("lsp diagnostics aggregate error/warning/file counts", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-lsp" } },
      },
    });
    await hooks.event({
      event: {
        type: "lsp.client.diagnostics",
        properties: {
          sessionID: "ses-lsp",
          uri: "file:///tmp/a.ts",
          diagnostics: [
            { severity: 1, message: "boom" },
            { severity: 1, message: "boom2" },
            { severity: 2, message: "hmm" },
          ],
        },
      },
    });
    assert.deepEqual(lastEnvelope(server).body.payload.lsp_diagnostics, {
      errors: 2,
      warnings: 1,
      files: 1,
    });
  } finally {
    restore();
    await server.close();
  }
});

test("vcs.branch.updated tracks git branch; mcp events tracked", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-v" } },
      },
    });
    await hooks.event({
      event: {
        type: "vcs.branch.updated",
        properties: { sessionID: "ses-v", branch: "feature/x" },
      },
    });
    assert.equal(lastEnvelope(server).body.payload.git_branch, "feature/x");
    await hooks.event({
      event: {
        type: "mcp.tools.changed",
        properties: { sessionID: "ses-v", server: "gh", status: "ok" },
      },
    });
    assert.deepEqual(lastEnvelope(server).body.payload.mcp_servers, [
      { name: "gh", status: "ok", last_error: "" },
    ]);
    await hooks.event({
      event: {
        type: "mcp.browser.open.failed",
        properties: { sessionID: "ses-v", server: "gh", error: "no browser" },
      },
    });
    const payload = lastEnvelope(server).body.payload;
    assert.equal(payload.mcp_servers[0].status, "error");
  } finally {
    restore();
    await server.close();
  }
});

test("session.compacted + experimental.session.compacting track compaction", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-c" } },
      },
    });
    await hooks["experimental.session.compacting"]({
      sessionID: "ses-c",
      reason: "context pressure",
    });
    let payload = lastEnvelope(server).body.payload;
    assert.equal(payload.last_compaction_reason, "context pressure");
    await hooks.event({
      event: {
        type: "session.compacted",
        properties: { sessionID: "ses-c", reason: "auto" },
      },
    });
    payload = lastEnvelope(server).body.payload;
    assert.equal(payload.compaction_count, 1);
    assert.ok(payload.last_compaction_ms > 0);
    assert.equal(payload.last_compaction_reason, "auto");
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Subagent isolation
// ---------------------------------------------------------------------------

test("subagent isolation: child never overwrites root model/usage", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { tracker, hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-root", title: "root" } },
      },
    });
    await hooks["chat.params"]({
      sessionID: "ses-root",
      model: {
        providerID: "anthropic",
        modelID: "root-model",
        limit: { context: 200000 },
      },
    });
    await hooks.event({
      event: {
        type: "message.updated",
        properties: {
          info: {
            sessionID: "ses-root",
            role: "assistant",
            model: { providerID: "anthropic", modelID: "root-model" },
            tokens: { input: 1000, output: 50 },
            cost: 0.5,
            parts: [{ type: "text", text: "root reply" }],
          },
        },
      },
    });
    // Child session appears with a parentID.
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-child", parentID: "ses-root" } },
      },
    });
    const childEnv = lastEnvelope(server).body;
    assert.equal(childEnv.payload.is_subagent, true);
    assert.equal(childEnv.payload.parent_session_id, "ses-root");
    assert.equal(childEnv.payload.session_id, "ses-child");

    // Child activity (different model/usage) must not touch the root.
    await hooks["chat.params"]({
      sessionID: "ses-child",
      model: { providerID: "other", modelID: "child-model" },
    });
    await hooks.event({
      event: {
        type: "message.updated",
        properties: {
          info: {
            sessionID: "ses-child",
            role: "assistant",
            model: { providerID: "other", modelID: "child-model" },
            tokens: { input: 5, output: 5 },
            cost: 0.01,
            parts: [{ type: "text", text: "child reply" }],
          },
        },
      },
    });
    const root = tracker.stateFor("ses-root");
    assert.equal(root.model, "root-model");
    assert.equal(root.provider, "anthropic");
    assert.equal(root.usage.input_tokens, 1000);
    assert.equal(root.usage.cost_usd, 0.5);
    const child = tracker.stateFor("ses-child");
    assert.equal(child.model, "child-model");
    assert.equal(child.is_subagent, true);

    // Root snapshots carry the subagents summary; child snapshots do not.
    const rootPayload = tracker.snapshot("ses-root");
    assert.equal(rootPayload.subagents.length, 1);
    assert.equal(rootPayload.subagents[0].session_id, "ses-child");
    const childPayload = tracker.snapshot("ses-child");
    assert.deepEqual(childPayload.subagents, []);
  } finally {
    restore();
    await server.close();
  }
});

// ---------------------------------------------------------------------------
// Resilience / fail-open
// ---------------------------------------------------------------------------

test("hooks never throw on garbage input", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const { hooks } = makePlugin();
    await hooks.event({});
    await hooks.event({ event: null });
    await hooks.event({ event: { type: "bogus.future.event", properties: null } });
    await hooks.event({ event: { properties: { sessionID: "x" } } });
    await hooks["tool.execute.before"](undefined, undefined);
    await hooks["tool.execute.after"](null, null);
    await hooks["chat.params"]({ model: "not-an-object" });
    await hooks["experimental.session.compacting"]();
    // Every hook above resolved without rejecting; pushes are fail-open
    // (bogus typed event + 4 interception hooks = at most 5 envelopes).
    assert.ok(server.received.length <= 5);
  } finally {
    restore();
    await server.close();
  }
});

test("fail-open when socket is missing", async () => {
  const restore = setEnv({
    TMUX_PANE: "%pane",
    SPACES_TMUX_SESSION: "s",
    SPACES_HOOKS_RUNTIME: path.join(
      os.tmpdir(),
      `spaces-missing-${Date.now()}-${process.pid}`,
    ),
    SPACES_HOOKS_TOKEN: "t",
  });
  try {
    assert.equal(resolveSocketPath(), "");
    const res = await postEnvelope({ v: 2 });
    assert.equal(res.delivered, false);
  } finally {
    restore();
  }
});

test("fail-open on slow/hanging socket (100ms timeout)", async () => {
  const server = await startMockServer({ hang: true });
  const restore = mockEnv(server);
  try {
    const started = Date.now();
    const res = await postEnvelope(
      { v: 2 },
      { socketPath: server.sock, timeoutMs: 100 },
    );
    const elapsed = Date.now() - started;
    assert.equal(res.delivered, false);
    assert.ok(elapsed < 1500, `took too long: ${elapsed}ms`);
    assert.equal(server.received.length, 1, "server got the bytes");
  } finally {
    restore();
    await server.close();
  }
});

test("silent no-op when $TMUX_PANE is unset; opt-in via ALLOW_NO_TMUX", async () => {
  const server = await startMockServer();
  const restore = setEnv({
    SPACES_TMUX_SESSION: "s",
    SPACES_HOOKS_RUNTIME: server.dir,
    SPACES_HOOKS_TOKEN: "t",
  });
  try {
    delete process.env.TMUX_PANE;
    resetTransportCache();
    const { hooks } = makePlugin();
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-no-tmux" } },
      },
    });
    assert.equal(server.received.length, 0);

    process.env.SPACES_PLUGIN_ALLOW_NO_TMUX = "1";
    await hooks.event({
      event: {
        type: "session.idle",
        properties: { sessionID: "ses-no-tmux" },
      },
    });
    assert.equal(server.received.length, 1);
  } finally {
    restore();
    await server.close();
  }
});

test("transport helpers: token resolution and tmux session override", async () => {
  const restore = setEnv({
    TMUX_PANE: "%pane",
    SPACES_TMUX_SESSION: "override-session",
    SPACES_HOOKS_TOKEN: "tok",
  });
  try {
    assert.equal(resolveToken(), "tok");
    delete process.env.SPACES_HOOKS_TOKEN;
    process.env.ZJ_TOKEN = "zj-tok";
    assert.equal(resolveToken(), "zj-tok");
    delete process.env.ZJ_TOKEN;
    assert.equal(resolveToken(), "");
    assert.equal(await getTmuxSession(), "override-session");
  } finally {
    restore();
  }
});

test("default plugin export returns hooks and enriches client fail-open", async () => {
  const server = await startMockServer();
  const restore = mockEnv(server);
  try {
    const mod = await import("../index.mjs");
    const hooks = await mod.default({
      client: {
        provider: {
          list: async () => [
            {
              id: "anthropic",
              models: [{ id: "m1", limit: { context: 100000 } }],
            },
          ],
        },
        mcp: {
          status: async () => {
            throw new Error("nope");
          },
        },
      },
    });
    assert.equal(typeof hooks.event, "function");
    assert.equal(typeof hooks["tool.execute.before"], "function");
    assert.equal(typeof hooks["tool.execute.after"], "function");
    assert.equal(typeof hooks["chat.params"], "function");
    assert.equal(
      typeof hooks["experimental.session.compacting"],
      "function",
    );
    await hooks.event({
      event: {
        type: "session.created",
        properties: { info: { id: "ses-def" } },
      },
    });
    assert.equal(server.received.length, 1);
  } finally {
    restore();
    await server.close();
  }
});

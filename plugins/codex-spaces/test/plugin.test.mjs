// plugin.test.mjs — comprehensive node:test suite for codex-spaces.
//
// Spins up a real mock Unix-domain-socket HTTP server per test, points the
// bridges at it via $SPACES_HOOKS_RUNTIME, and asserts on the v2 envelopes
// received at POST /v1/plugin/codex.

import { describe, it, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import net from "node:net";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const ROOT = path.dirname(
  path.dirname(fileURLToPath(import.meta.url)),
);
const HOOK_BRIDGE = path.join(ROOT, "scripts", "hook-bridge.mjs");
const OTEL_BRIDGE = path.join(ROOT, "scripts", "otel-bridge.mjs");
const TOKEN = "test-token-123";

// ---------------------------------------------------------------------------
// Mock UDS HTTP server
// ---------------------------------------------------------------------------

async function startMock({ delayMs = 0, token = TOKEN, expectToken = null, status = 204 } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "codex-spaces-test-"));
  const socket = path.join(dir, "h.sock");
  const captures = [];
  const server = net.createServer((conn) => {
    conn.on("error", () => {});
    let buf = Buffer.alloc(0);
    let responded = false;
    conn.on("data", (chunk) => {
      if (responded) return;
      buf = Buffer.concat([buf, chunk]);
      const hi = buf.indexOf("\r\n\r\n");
      if (hi < 0) return;
      const head = buf.slice(0, hi).toString("utf8");
      const m = /Content-Length:\s*(\d+)/i.exec(head);
      const len = m ? parseInt(m[1], 10) : 0;
      if (buf.length < hi + 4 + len) return;
      responded = true;
      const bodyStr = buf.slice(hi + 4, hi + 4 + len).toString("utf8");
      const lines = head.split("\r\n");
      const [method, p] = lines[0].split(" ");
      const headers = {};
      for (const l of lines.slice(1)) {
        const i = l.indexOf(":");
        if (i > 0) headers[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
      }
      captures.push({ method, path: p, headers, body: bodyStr });
      const respond = () => {
        try {
          if (headers["x-zj-token"] !== (expectToken ?? token)) {
            conn.end(
              "HTTP/1.1 401 Unauthorized\r\nContent-Type: application/json\r\nContent-Length: 13\r\nConnection: close\r\n\r\n{\"ok\":false}",
            );
          } else if (status === 204) {
            conn.end(
              "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
            );
          } else {
            conn.end(
              `HTTP/1.1 ${status} X\r\nContent-Type: application/json\r\nContent-Length: 13\r\nConnection: close\r\n\r\n{"ok":false}`,
            );
          }
        } catch {
          // client already gone (slow-socket test); ignore
        }
      };
      if (delayMs > 0) setTimeout(respond, delayMs);
      else respond();
    });
  });
  server.listen(socket);
  await new Promise((resolve, reject) => {
    server.once("listening", resolve);
    server.once("error", reject);
  });
  fs.writeFileSync(
    path.join(dir, "hooks.endpoint.json"),
    JSON.stringify({ version: 1, socket, tcp_port: 0, token }),
  );
  return {
    dir,
    socket,
    captures,
    async close() {
      await new Promise((r) => server.close(r));
      fs.rmSync(dir, { recursive: true, force: true });
    },
  };
}

function waitFor(captures, n, timeoutMs = 5000) {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const poll = () => {
      if (captures.length >= n) resolve();
      else if (Date.now() - start > timeoutMs) {
        reject(new Error(`timed out waiting for ${n} captures`));
      } else setTimeout(poll, 25);
    };
    poll();
  });
}

function baseEnv(mock, extra = {}) {
  const env = { ...process.env };
  delete env.TMUX_PANE;
  delete env.TMUX;
  delete env.SPACES_PLUGIN_ALLOW_NO_TMUX;
  delete env.SPACES_TMUX_SESSION;
  return {
    ...env,
    TMUX_PANE: "%1",
    SPACES_TMUX_SESSION: "testsession",
    SPACES_HOOKS_RUNTIME: mock.dir,
    ...extra,
  };
}

function runBridge(script, args, { stdin = "", env }) {
  const start = Date.now();
  let code = -1;
  let stdout = "";
  let stderr = "";
  try {
    stdout = execFileSync(process.execPath, [script, ...args], {
      input: stdin,
      encoding: "utf8",
      timeout: 15000,
      env,
    });
    code = 0;
  } catch (e) {
    code = typeof e.status === "number" ? e.status : -1;
    stdout = e.stdout ? String(e.stdout) : "";
    stderr = e.stderr ? String(e.stderr) : "";
  }
  return { code, stdout, stderr, elapsed: Date.now() - start };
}

function lastEnvelope(mock) {
  assert.ok(mock.captures.length > 0, "expected at least one captured request");
  const c = mock.captures[mock.captures.length - 1];
  assert.equal(c.method, "POST");
  assert.equal(c.path, "/v1/plugin/codex");
  assert.equal(c.headers["content-type"], "application/json");
  assert.ok(c.headers["content-length"], "Content-Length required");
  assert.equal(c.headers["x-zj-token"], TOKEN);
  return JSON.parse(c.body);
}

function checkBaseEnvelope(env, event) {
  assert.equal(env.v, 2);
  assert.equal(env.agent, "codex");
  assert.equal(env.session, "testsession");
  assert.equal(env.pane, "%1");
  assert.equal(env.event, event);
  assert.equal(env.plugin.name, "codex-spaces");
  assert.equal(env.plugin.version, "1.0.0");
  for (const cap of [
    "lifecycle",
    "usage",
    "context",
    "tools",
    "files",
    "subagents",
    "permissions",
    "rate_limits",
    "otel",
    "sandbox",
    "errors",
  ]) {
    assert.ok(env.plugin.capabilities.includes(cap), `missing capability ${cap}`);
  }
  assert.equal(typeof env.ts_ms, "number");
}

let mock = null;
async function teardown() {
  if (mock) {
    await mock.close();
    mock = null;
  }
}

// ---------------------------------------------------------------------------
// hooks.json template
// ---------------------------------------------------------------------------

describe("hooks.json template", () => {
  it("wires all seven Codex events to hook-bridge.mjs", () => {
    const doc = JSON.parse(
      fs.readFileSync(path.join(ROOT, "hooks.json"), "utf8"),
    );
    for (const ev of [
      "SessionStart",
      "UserPromptSubmit",
      "PreToolUse",
      "PostToolUse",
      "PermissionRequest",
      "Stop",
      "SessionEnd",
    ]) {
      const hooks = doc.hooks[ev];
      assert.ok(Array.isArray(hooks) && hooks.length > 0, `${ev} missing`);
      assert.ok(
        hooks.some((h) => String(h.command || "").includes("hook-bridge.mjs") &&
          String(h.command || "").includes(ev)),
        `${ev} not wired to hook-bridge.mjs`,
      );
    }
  });
});

// ---------------------------------------------------------------------------
// Modern hooks.json stdin payloads
// ---------------------------------------------------------------------------

describe("hook-bridge modern stdin payloads", () => {
  beforeEach(async () => {
    mock = await startMock();
  });
  afterEach(teardown);

  it("SessionStart reports idle with session id", async () => {
    const r = runBridge(HOOK_BRIDGE, ["SessionStart"], {
      stdin: JSON.stringify({
        thread_id: "thr-1",
        cwd: "/repo",
        model: "gpt-5.2",
        codex_version: "1.2.3",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "SessionStart");
    assert.equal(env.payload.session_id, "thr-1");
    assert.equal(env.payload.state, "idle");
    assert.equal(env.payload.cwd, "/repo");
    assert.equal(env.payload.model, "gpt-5.2");
    assert.equal(env.harness_version, "1.2.3");
  });

  it("UserPromptSubmit reports working with prompt", async () => {
    const r = runBridge(HOOK_BRIDGE, ["UserPromptSubmit"], {
      stdin: JSON.stringify({ thread_id: "thr-1", prompt: "fix the bug" }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "UserPromptSubmit");
    assert.equal(env.payload.state, "working");
    assert.equal(env.payload.prompt, "fix the bug");
  });

  it("PreToolUse extracts files_touched from apply_patch", async () => {
    const patch = [
      "*** Begin Patch",
      "*** Update File: src/a.ts",
      "@@",
      "-old",
      "+new",
      "*** Add File: src/b.ts",
      "@@",
      "+x",
      "*** Delete File: src/old.ts",
      "*** End Patch",
    ].join("\n");
    const r = runBridge(HOOK_BRIDGE, ["PreToolUse"], {
      stdin: JSON.stringify({
        thread_id: "thr-2",
        tool_name: "apply_patch",
        tool_input: patch,
        model: "gpt-5.2",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "PreToolUse");
    assert.equal(env.payload.state, "working");
    assert.equal(env.payload.tool, "apply_patch");
    assert.deepEqual(env.payload.files_touched, [
      "src/a.ts",
      "src/b.ts",
      "src/old.ts",
    ]);
  });

  it("PreToolUse extracts files from standard file args", async () => {
    const r = runBridge(HOOK_BRIDGE, ["PreToolUse"], {
      stdin: JSON.stringify({
        thread_id: "thr-2",
        tool_name: "write",
        tool_input: { file_path: "src/c.ts", content: "x" },
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.deepEqual(env.payload.files_touched, ["src/c.ts"]);
  });

  it("PostToolUse reports working with tool output", async () => {
    const r = runBridge(HOOK_BRIDGE, ["PostToolUse"], {
      stdin: JSON.stringify({
        thread_id: "thr-2",
        tool_name: "shell",
        tool_input: { command: "ls" },
        tool_output: "a.ts\nb.ts",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "PostToolUse");
    assert.equal(env.payload.state, "working");
    assert.match(env.payload.tool_output, /a\.ts/);
  });

  it("PermissionRequest reports blocked/permission", async () => {
    const r = runBridge(HOOK_BRIDGE, ["PermissionRequest"], {
      stdin: JSON.stringify({
        thread_id: "thr-3",
        tool_name: "shell",
        tool_input: { command: "rm -rf /" },
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "PermissionRequest");
    assert.equal(env.payload.state, "blocked");
    assert.equal(env.payload.kind, "permission");
    assert.ok(env.payload.pending_permission, "pending_permission set");
  });

  it("Stop reports done with reply", async () => {
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({
        thread_id: "thr-1",
        last_assistant_message: "All done!",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "Stop");
    assert.equal(env.payload.state, "done");
    assert.equal(env.payload.reply, "All done!");
  });

  it("Stop with error reports failed", async () => {
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "thr-1", error: "boom" }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.equal(env.payload.state, "failed");
    assert.equal(env.payload.last_error, "boom");
  });

  it("SessionEnd reports idle", async () => {
    const r = runBridge(HOOK_BRIDGE, ["SessionEnd"], {
      stdin: JSON.stringify({ thread_id: "thr-1" }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "SessionEnd");
    assert.equal(env.payload.state, "idle");
  });
});

// ---------------------------------------------------------------------------
// Rollout JSONL enrichment
// ---------------------------------------------------------------------------

describe("hook-bridge rollout JSONL enrichment", () => {
  beforeEach(async () => {
    mock = await startMock();
  });
  afterEach(teardown);

  it("extracts token usage, rate limits and compactions", async () => {
    const rollout = path.join(mock.dir, "rollout.jsonl");
    fs.writeFileSync(
      rollout,
      [
        JSON.stringify({
          timestamp: 1,
          type: "event_msg",
          payload: {
            type: "token_count",
            info: {
              total_token_usage: {
                input_tokens: 1200,
                output_tokens: 340,
                reasoning_tokens: 50,
              },
              last_token_usage: { input_tokens: 1200, output_tokens: 340 },
              model_context_window: 200000,
            },
          },
        }),
        JSON.stringify({
          timestamp: 2,
          type: "event_msg",
          payload: {
            type: "rate_limits",
            rate_limits: {
              primary: {
                limited: true,
                message: "rate limited, retry later",
                resets_in_seconds: 30,
              },
              secondary: {},
            },
          },
        }),
        JSON.stringify({
          timestamp: 3,
          type: "event_msg",
          payload: { type: "context_compacted" },
        }),
      ].join("\n") + "\n",
    );
    const before = Date.now();
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({
        thread_id: "thr-9",
        rollout_path: rollout,
        last_assistant_message: "done",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.deepEqual(env.payload.usage, {
      input_tokens: 1200,
      output_tokens: 340,
      reasoning_tokens: 50,
      cache_read_tokens: 0,
      cost_usd: 0,
    });
    assert.equal(env.payload.context_tokens, 1540);
    assert.equal(env.payload.context_window, 200000);
    assert.equal(env.payload.context_percent, 0.8);
    assert.equal(env.payload.compaction_count, 1);
    assert.equal(env.payload.turn_count, 1);
    assert.equal(env.payload.rate_limited, true);
    assert.equal(env.payload.rate_limit_message, "rate limited, retry later");
    assert.ok(
      env.payload.rate_limit_reset_ms >= before + 29 * 1000,
      "reset window ~30s out",
    );
  });
});

// ---------------------------------------------------------------------------
// Legacy notify argv JSON
// ---------------------------------------------------------------------------

describe("hook-bridge legacy notify argv", () => {
  beforeEach(async () => {
    mock = await startMock();
  });
  afterEach(teardown);

  it("maps agent-turn-complete argv JSON to Stop/done", async () => {
    const r = runBridge(
      HOOK_BRIDGE,
      ["notify", JSON.stringify({ type: "agent-turn-complete", thread_id: "thr-legacy" })],
      { stdin: "", env: baseEnv(mock) },
    );
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    checkBaseEnvelope(env, "Stop");
    assert.equal(env.payload.state, "done");
    assert.equal(env.payload.session_id, "thr-legacy");
    assert.equal(env.payload.extras.notify_type, "agent-turn-complete");
  });
});

// ---------------------------------------------------------------------------
// Subagent isolation
// ---------------------------------------------------------------------------

describe("hook-bridge subagent isolation", () => {
  beforeEach(async () => {
    mock = await startMock();
  });
  afterEach(teardown);

  it("marks agent payloads as subagents with child ids", async () => {
    const r = runBridge(HOOK_BRIDGE, ["UserPromptSubmit"], {
      stdin: JSON.stringify({
        thread_id: "child-thr",
        parent_thread_id: "parent-thr",
        agent_id: "agent-7",
        model: "gpt-5.2-mini",
        prompt: "subtask",
      }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.equal(env.payload.is_subagent, true);
    assert.equal(env.payload.agent_id, "agent-7");
    assert.equal(env.payload.session_id, "child-thr");
    assert.equal(env.payload.parent_session_id, "parent-thr");
  });
});

// ---------------------------------------------------------------------------
// Fail-open + pane gating
// ---------------------------------------------------------------------------

describe("hook-bridge fail-open behavior", () => {
  afterEach(teardown);

  it("exits 0 with no socket (missing endpoint)", async () => {
    mock = await startMock();
    const empty = fs.mkdtempSync(path.join(os.tmpdir(), "codex-spaces-empty-"));
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "x" }),
      env: baseEnv(mock, { SPACES_HOOKS_RUNTIME: empty }),
    });
    fs.rmSync(empty, { recursive: true, force: true });
    assert.equal(r.code, 0);
    assert.equal(mock.captures.length, 0);
  });

  it("exits 0 fast on a slow socket", async () => {
    if (mock) await mock.close();
    mock = await startMock({ delayMs: 500 });
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "x" }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    assert.ok(r.elapsed < 5000, `fail-open budget blown: ${r.elapsed}ms`);
  });

  it("silent no-op when $TMUX_PANE is unset", async () => {
    mock = await startMock();
    const env = baseEnv(mock);
    delete env.TMUX_PANE;
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "x" }),
      env,
    });
    assert.equal(r.code, 0);
    assert.equal(mock.captures.length, 0);
  });

  it("delivers without $TMUX_PANE when SPACES_PLUGIN_ALLOW_NO_TMUX=1", async () => {
    mock = await startMock();
    const env = baseEnv(mock, { SPACES_PLUGIN_ALLOW_NO_TMUX: "1" });
    delete env.TMUX_PANE;
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "x" }),
      env,
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const envOut = lastEnvelope(mock);
    assert.equal(envOut.pane, "");
  });

  it("exits 0 on 401 without crashing", async () => {
    mock = await startMock({ expectToken: "different-token" });
    const r = runBridge(HOOK_BRIDGE, ["Stop"], {
      stdin: JSON.stringify({ thread_id: "x" }),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    // The mock can only accept the connection once execFileSync unblocks
    // this process's loop, so poll rather than asserting synchronously.
    await waitFor(mock.captures, 1);
  });
});

// ---------------------------------------------------------------------------
// otel-bridge
// ---------------------------------------------------------------------------

describe("otel-bridge OTLP parsing", () => {
  beforeEach(async () => {
    mock = await startMock();
  });
  afterEach(teardown);

  it("parses /v1/logs with usage, 429 and tool result", async () => {
    const otlp = {
      resourceLogs: [
        {
          resource: {
            attributes: [
              { key: "session.id", value: { stringValue: "sess-1" } },
              { key: "codex.model", value: { stringValue: "gpt-5.2" } },
              { key: "gen_ai.usage.input_tokens", value: { intValue: "120" } },
              { key: "gen_ai.usage.output_tokens", value: { intValue: "45" } },
              { key: "http.status_code", value: { intValue: "429" } },
            ],
          },
          scopeLogs: [
            {
              scope: { name: "codex" },
              logRecords: [
                {
                  body: {
                    stringValue:
                      "codex.tool_result status=error rate limit hit 429",
                  },
                  attributes: [
                    { key: "codex.tool", value: { stringValue: "shell" } },
                    {
                      key: "codex.rate_limit_message",
                      value: { stringValue: "slow down" },
                    },
                  ],
                  severityText: "ERROR",
                },
              ],
            },
          ],
        },
      ],
    };
    const r = runBridge(OTEL_BRIDGE, ["/v1/logs"], {
      stdin: JSON.stringify(otlp),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.equal(env.v, 2);
    assert.equal(env.agent, "codex");
    assert.equal(env.payload.session_id, "sess-1");
    assert.equal(env.payload.model, "gpt-5.2");
    assert.equal(env.payload.tool, "shell");
    assert.equal(env.payload.usage.input_tokens, 120);
    assert.equal(env.payload.usage.output_tokens, 45);
    assert.equal(env.payload.rate_limited, true);
    assert.equal(env.payload.rate_limit_message, "slow down");
    assert.ok(env.payload.extras.status_codes.includes(429));
    assert.ok(
      env.payload.extras.otel_events.some((e) => /tool_result/.test(e)),
    );
  });

  it("parses /v1/metrics with token sums and tool decisions/results", async () => {
    const otlp = {
      resourceMetrics: [
        {
          resource: {
            attributes: [
              { key: "service.name", value: { stringValue: "codex" } },
              { key: "codex.session_id", value: { stringValue: "sess-9" } },
            ],
          },
          scopeMetrics: [
            {
              scope: { name: "codex" },
              metrics: [
                {
                  name: "codex.api_request",
                  sum: {
                    dataPoints: [
                      {
                        attributes: [
                          {
                            key: "gen_ai.usage.input_tokens",
                            value: { stringValue: "x" },
                          },
                        ],
                        asInt: "150",
                      },
                    ],
                  },
                },
                {
                  name: "codex.tool_decision",
                  sum: {
                    dataPoints: [
                      {
                        attributes: [
                          { key: "codex.tool", value: { stringValue: "apply_patch" } },
                        ],
                        asInt: "1",
                      },
                    ],
                  },
                },
                {
                  name: "codex.tool_result",
                  sum: {
                    dataPoints: [
                      {
                        attributes: [
                          { key: "codex.tool", value: { stringValue: "apply_patch" } },
                        ],
                        asInt: "1",
                      },
                    ],
                  },
                },
              ],
            },
          ],
        },
      ],
    };
    const r = runBridge(OTEL_BRIDGE, ["/v1/metrics"], {
      stdin: JSON.stringify(otlp),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.equal(env.event, "post_tool");
    assert.equal(env.payload.session_id, "sess-9");
    assert.equal(env.payload.usage.input_tokens, 150);
    assert.ok(env.payload.tool_call_count >= 2, "decision + result counted");
    assert.equal(env.payload.rate_limited, false);
    for (const n of ["codex.api_request", "codex.tool_decision", "codex.tool_result"]) {
      assert.ok(env.payload.extras.otel_events.includes(n), `missing ${n}`);
    }
  });

  it("parses /v1/traces tool spans, durations and errors", async () => {
    const otlp = {
      resourceSpans: [
        {
          resource: {
            attributes: [{ key: "session.id", value: { stringValue: "sess-t" } }],
          },
          scopeSpans: [
            {
              scope: { name: "codex" },
              spans: [
                {
                  name: "codex.tool.exec",
                  startTimeUnixNano: "1000000000",
                  endTimeUnixNano: "1500000000",
                  attributes: [
                    { key: "codex.tool", value: { stringValue: "shell" } },
                  ],
                  status: { code: 2, message: "boom" },
                },
              ],
            },
          ],
        },
      ],
    };
    const r = runBridge(OTEL_BRIDGE, ["/v1/traces"], {
      stdin: JSON.stringify(otlp),
      env: baseEnv(mock),
    });
    assert.equal(r.code, 0);
    await waitFor(mock.captures, 1);
    const env = lastEnvelope(mock);
    assert.equal(env.payload.session_id, "sess-t");
    assert.equal(env.payload.tool, "shell");
    assert.equal(env.payload.tool_call_count, 1);
    assert.equal(env.payload.tool_error_count, 1);
    assert.equal(env.payload.extras.duration_ms, 500);
  });

  it("otel-bridge is fail-open and pane-gated", async () => {
    const env = baseEnv(mock);
    delete env.TMUX_PANE;
    const r = runBridge(OTEL_BRIDGE, ["/v1/logs"], {
      stdin: JSON.stringify({ resourceLogs: [] }),
      env,
    });
    assert.equal(r.code, 0);
    assert.equal(mock.captures.length, 0);
  });
});

// ---------------------------------------------------------------------------
// transport.mjs unit tests (in-process: real 2xx round-trips)
// ---------------------------------------------------------------------------

describe("transport UDS client", () => {
  let transport = null;
  const savedEnv = {};

  beforeEach(async () => {
    mock = await startMock();
    if (!transport) {
      transport = await import("../scripts/transport.mjs");
    }
    transport.resetSessionCache();
    for (const k of [
      "SPACES_HOOKS_RUNTIME",
      "XDG_RUNTIME_DIR",
      "SPACES_TMUX_SESSION",
      "TMUX_PANE",
      "SPACES_PLUGIN_ALLOW_NO_TMUX",
    ]) {
      savedEnv[k] = process.env[k];
    }
    process.env.SPACES_HOOKS_RUNTIME = mock.dir;
  });

  afterEach(async () => {
    for (const [k, v] of Object.entries(savedEnv)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    transport.resetSessionCache();
    await teardown();
  });

  it("resolves the endpoint and POSTs a 2xx envelope", async () => {
    process.env.SPACES_TMUX_SESSION = "unitsess";
    process.env.TMUX_PANE = "%9";
    const ep = transport.readEndpoint();
    assert.equal(ep.token, TOKEN);
    assert.ok(ep.socket.endsWith("h.sock"));
    const env = transport.buildEnvelope("Stop", { session_id: "s" });
    assert.equal(env.session, "unitsess");
    assert.equal(env.pane, "%9");
    assert.ok(await transport.postEnvelope(env));
    await waitFor(mock.captures, 1);
    const c = mock.captures[0];
    assert.equal(c.method, "POST");
    assert.equal(c.path, "/v1/plugin/codex");
    assert.equal(c.headers["x-zj-token"], TOKEN);
  });

  it("returns false on non-2xx without throwing", async () => {
    process.env.SPACES_TMUX_SESSION = "unitsess";
    process.env.TMUX_PANE = "%9";
    if (mock) await mock.close();
    mock = await startMock({ status: 500 });
    process.env.SPACES_HOOKS_RUNTIME = mock.dir;
    assert.equal(
      await transport.postEnvelope(
        transport.buildEnvelope("Stop", { session_id: "s" }),
      ),
      false,
    );
  });

  it("returns false fast on a slow socket (100ms budget)", async () => {
    process.env.SPACES_TMUX_SESSION = "unitsess";
    process.env.TMUX_PANE = "%9";
    if (mock) await mock.close();
    mock = await startMock({ delayMs: 2000 });
    process.env.SPACES_HOOKS_RUNTIME = mock.dir;
    const start = Date.now();
    assert.equal(
      await transport.postEnvelope(
        transport.buildEnvelope("Stop", { session_id: "s" }),
      ),
      false,
    );
    assert.ok(Date.now() - start < 2000, "fail-open budget exceeded");
  });

  it("returns false with a missing endpoint", async () => {
    const empty = fs.mkdtempSync(path.join(os.tmpdir(), "codex-spaces-empty-"));
    process.env.SPACES_HOOKS_RUNTIME = empty;
    assert.equal(
      await transport.postEnvelope(
        transport.buildEnvelope("Stop", { session_id: "s" }),
      ),
      false,
    );
    fs.rmSync(empty, { recursive: true, force: true });
  });

  it("gates delivery on $TMUX_PANE", () => {
    delete process.env.TMUX_PANE;
    delete process.env.SPACES_PLUGIN_ALLOW_NO_TMUX;
    assert.equal(transport.shouldDeliver(), false);
    process.env.SPACES_PLUGIN_ALLOW_NO_TMUX = "1";
    assert.equal(transport.shouldDeliver(), true);
    process.env.TMUX_PANE = "%3";
    delete process.env.SPACES_PLUGIN_ALLOW_NO_TMUX;
    assert.equal(transport.shouldDeliver(), true);
  });
});

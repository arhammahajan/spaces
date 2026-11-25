// Comprehensive tests for the claude-spaces plugin.
// Run with: node --test  (from plugins/claude-spaces/)
//
// Spins up a real mock Unix-domain-socket HTTP server, points the bridges
// at it via $SPACES_HOOKS_RUNTIME/hooks.endpoint.json, and asserts the
// shared v2 wire contract plus every safety guarantee.

import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PLUGIN_ROOT = path.resolve(HERE, '..');
const HOOK_BRIDGE = path.join(PLUGIN_ROOT, 'scripts', 'hook-bridge.mjs');
const STATUSLINE_BRIDGE = path.join(PLUGIN_ROOT, 'scripts', 'statusline-bridge.mjs');

const TOKEN = 'test-token-abc123';
const TMUX_PANE = '%42';

const ALL_EVENTS = [
  'SessionStart',
  'UserPromptSubmit',
  'PreToolUse',
  'PostToolUse',
  'PostToolUseFailure',
  'PermissionRequest',
  'Notification',
  'Elicitation',
  'ElicitationResult',
  'PreCompact',
  'PostCompact',
  'SubagentStart',
  'SubagentStop',
  'TaskCreated',
  'TaskCompleted',
  'Stop',
  'StopFailure',
  'SessionEnd',
];

const CAPABILITIES = [
  'lifecycle',
  'usage',
  'context',
  'cost',
  'tools',
  'files',
  'todos',
  'subagents',
  'background_tasks',
  'permissions',
  'questions',
  'compaction',
  'statusline',
  'errors',
];

// ---------------------------------------------------------------------------
// harness helpers
// ---------------------------------------------------------------------------

function makeTempDir(prefix) {
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix));
}

/** Write a fake `tmux` that reports a fixed session name; returns bin dir. */
function makeFakeTmuxBin(sessionName) {
  const dir = makeTempDir('claude-spaces-bin-');
  const tmux = path.join(dir, 'tmux');
  fs.writeFileSync(tmux, `#!/bin/sh\necho "${sessionName}"\n`);
  fs.chmodSync(tmux, 0o755);
  return dir;
}

/**
 * Start a mock UDS HTTP server that records requests and answers `status`
 * (optionally after `delayMs`). Resolves {server, requests, socketPath}.
 */
function startMockServer(dir, { delayMs = 0, status = 204 } = {}) {
  const socketPath = path.join(dir, 'mock.sock');
  try {
    fs.unlinkSync(socketPath);
  } catch {
    // ignore
  }
  const requests = [];
  const server = net.createServer((conn) => {
    let buf = Buffer.alloc(0);
    let responded = false;
    conn.on('data', (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      const text = buf.toString('latin1');
      const headerEnd = text.indexOf('\r\n\r\n');
      if (headerEnd === -1) return;
      const lines = text.slice(0, headerEnd).split('\r\n');
      const [method, reqPath] = lines[0].split(' ');
      const headers = {};
      for (const line of lines.slice(1)) {
        const idx = line.indexOf(':');
        if (idx > 0) headers[line.slice(0, idx).trim().toLowerCase()] = line.slice(idx + 1).trim();
      }
      const len = parseInt(headers['content-length'] || '0', 10);
      const bodyStart = headerEnd + 4;
      if (buf.length < bodyStart + len) return; // wait for full body
      requests.push({
        method,
        path: reqPath,
        headers,
        body: buf.slice(bodyStart, bodyStart + len).toString('utf8'),
      });
      const respond = () => {
        if (responded) return;
        responded = true;
        const reason = status === 204 ? 'No Content' : status === 200 ? 'OK' : 'Error';
        const respBody = status === 204 ? '' : '{}';
        try {
          conn.write(
            `HTTP/1.1 ${status} ${reason}\r\nContent-Length: ${respBody.length}\r\nConnection: close\r\n\r\n${respBody}`,
          );
        } catch {
          // ignore
        }
        conn.end();
      };
      if (delayMs > 0) setTimeout(respond, delayMs);
      else respond();
    });
    conn.on('error', () => {});
  });
  return new Promise((resolve, reject) => {
    server.on('error', reject);
    server.listen(socketPath, () => resolve({ server, requests, socketPath }));
  });
}

function closeServer(server) {
  return new Promise((resolve) => server.close(() => resolve()));
}

/** Write hooks.endpoint.json for a runtime dir pointing at socketPath. */
function writeEndpoint(dir, socketPath, token = TOKEN) {
  fs.writeFileSync(
    path.join(dir, 'hooks.endpoint.json'),
    JSON.stringify({ version: 1, protocol: 'http/uds', pid: 99999, socket: socketPath, tcp_port: null, token }),
  );
}

function baseEnv(runtimeDir, binDir, extra = {}) {
  return {
    ...process.env,
    SPACES_HOOKS_RUNTIME: runtimeDir,
    TMUX_PANE,
    PATH: `${binDir}${path.delimiter}${process.env.PATH}`,
    ...extra,
  };
}

/** Run a bridge script with JSON on stdin; resolves {code, stdout, stderr, elapsedMs}. */
function runBridge(script, args, stdinObj, env, stdinRaw = null) {
  return new Promise((resolve) => {
    const started = Date.now();
    const child = spawn(process.execPath, [script, ...args], { env, stdio: ['pipe', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (c) => {
      stdout += c;
    });
    child.stderr.on('data', (c) => {
      stderr += c;
    });
    const killTimer = setTimeout(() => {
      try {
        child.kill('SIGKILL');
      } catch {
        // ignore
      }
    }, 15000);
    child.on('close', (code) => {
      clearTimeout(killTimer);
      resolve({ code, stdout, stderr, elapsedMs: Date.now() - started });
    });
    child.on('error', () => {
      clearTimeout(killTimer);
      resolve({ code: -1, stdout, stderr, elapsedMs: Date.now() - started });
    });
    try {
      child.stdin.write(stdinRaw !== null ? stdinRaw : JSON.stringify(stdinObj ?? {}));
      child.stdin.end();
    } catch {
      // ignore
    }
  });
}

/** Set up runtime dir + mock server + fake tmux; returns context. */
async function setupEnv(serverOpts = {}) {
  const runtimeDir = makeTempDir('claude-spaces-rt-');
  const binDir = makeFakeTmuxBin('faketmuxsession');
  const { server, requests, socketPath } = await startMockServer(runtimeDir, serverOpts);
  writeEndpoint(runtimeDir, socketPath);
  return { runtimeDir, binDir, server, requests, socketPath };
}

async function teardownEnv(ctx) {
  await closeServer(ctx.server);
  fs.rmSync(ctx.runtimeDir, { recursive: true, force: true });
}

function lastRequestBody(requests) {
  assert.ok(requests.length > 0, 'expected at least one request to the mock server');
  return requests[requests.length - 1];
}

function assertWireHeaders(req) {
  assert.equal(req.method, 'POST');
  assert.equal(req.path, '/v1/plugin/claude');
  assert.equal(req.headers['content-type'], 'application/json');
  assert.equal(req.headers['x-zj-token'], TOKEN);
  assert.equal(Number(req.headers['content-length']), Buffer.byteLength(req.body));
}

function assertEnvelopeShape(env, event) {
  assert.equal(env.v, 2);
  assert.equal(env.agent, 'claude');
  assert.equal(env.event, event);
  assert.equal(typeof env.session, 'string');
  assert.equal(typeof env.pane, 'string');
  assert.equal(env.plugin.name, 'claude-spaces');
  assert.equal(env.plugin.version, '1.0.0');
  assert.deepEqual(env.plugin.capabilities, CAPABILITIES);
  assert.equal(typeof env.ts_ms, 'number');
  assert.equal(typeof env.pid, 'number');
  assert.ok(env.payload && typeof env.payload === 'object');
}

// ---------------------------------------------------------------------------
// manifest schema
// ---------------------------------------------------------------------------

test('plugin.json is valid and contains no hooks key', () => {
  const doc = JSON.parse(fs.readFileSync(path.join(PLUGIN_ROOT, '.claude-plugin', 'plugin.json'), 'utf8'));
  assert.equal(doc.name, 'claude-spaces');
  assert.match(doc.version, /^\d+\.\d+\.\d+$/);
  assert.ok(doc.description && doc.description.length > 0);
  assert.ok(!('hooks' in doc), 'hooks must live in hooks/hooks.json, not plugin.json');
});

test('hooks/hooks.json wires all lifecycle hooks to hook-bridge.mjs', () => {
  const doc = JSON.parse(fs.readFileSync(path.join(PLUGIN_ROOT, 'hooks', 'hooks.json'), 'utf8'));
  assert.ok(doc.hooks && typeof doc.hooks === 'object');
  for (const event of ALL_EVENTS) {
    const entries = doc.hooks[event];
    assert.ok(Array.isArray(entries) && entries.length > 0, `missing hook wiring for ${event}`);
    const commands = entries.flatMap((e) => (e.hooks || []).map((h) => h.command));
    assert.ok(commands.length > 0, `no command hooks for ${event}`);
    for (const cmd of commands) {
      assert.match(cmd, /hook-bridge\.mjs/, `${event}: command must invoke hook-bridge.mjs`);
      assert.ok(cmd.includes(event), `${event}: command must pass the event name`);
    }
  }
});

// ---------------------------------------------------------------------------
// hook-bridge: core lifecycle events
// ---------------------------------------------------------------------------

test('hook-bridge SessionStart pushes idle v2 envelope', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir, { SPACES_PLUGIN_SESSION: 'testsession' });
    const res = await runBridge(
      HOOK_BRIDGE,
      ['SessionStart'],
      { session_id: 'sess-1', cwd: '/repo', transcript_path: '/tmp/t.jsonl', source: 'startup' },
      env,
    );
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
    const req = lastRequestBody(ctx.requests);
    assertWireHeaders(req);
    const envelope = JSON.parse(req.body);
    assertEnvelopeShape(envelope, 'SessionStart');
    assert.equal(envelope.session, 'testsession');
    assert.equal(envelope.pane, TMUX_PANE);
    assert.equal(envelope.payload.session_id, 'sess-1');
    assert.equal(envelope.payload.state, 'idle');
    assert.equal(envelope.payload.cwd, '/repo');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge resolves tmux session via tmux binary and caches it', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir); // no SPACES_PLUGIN_SESSION override
    const res = await runBridge(HOOK_BRIDGE, ['SessionStart'], { session_id: 's' }, env);
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.session, 'faketmuxsession');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge UserPromptSubmit extracts prompt and working state', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(
      HOOK_BRIDGE,
      ['UserPromptSubmit'],
      { session_id: 'sess-1', cwd: '/repo', prompt: 'Implement the thing', permission_mode: 'default' },
      env,
    );
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assertEnvelopeShape(envelope, 'UserPromptSubmit');
    assert.equal(envelope.payload.state, 'working');
    assert.equal(envelope.payload.prompt, 'Implement the thing');
    assert.equal(envelope.payload.mode, 'default');
    assert.ok(envelope.payload.title);
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PreToolUse Edit extracts files_touched', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(
      HOOK_BRIDGE,
      ['PreToolUse'],
      {
        session_id: 'sess-1',
        cwd: '/repo',
        tool_name: 'Edit',
        tool_use_id: 'tu-1',
        tool_input: { file_path: '/repo/a.ts', old_string: 'x', new_string: 'y' },
      },
      env,
    );
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assertEnvelopeShape(envelope, 'PreToolUse');
    assert.equal(envelope.payload.tool, 'Edit');
    assert.equal(envelope.payload.tool_call_id, 'tu-1');
    assert.deepEqual(envelope.payload.files_touched, ['/repo/a.ts']);
    assert.equal(envelope.payload.state, 'working');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PreToolUse AskUserQuestion maps to blocked/question', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(
      HOOK_BRIDGE,
      ['PreToolUse'],
      { session_id: 's', tool_name: 'AskUserQuestion', tool_input: { questions: [] } },
      env,
    );
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'blocked');
    assert.equal(envelope.payload.kind, 'question');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PostToolUse Write + TodoWrite extract files and todos', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(
      HOOK_BRIDGE,
      ['PostToolUse'],
      {
        session_id: 's',
        tool_name: 'Write',
        tool_use_id: 'tu-9',
        tool_input: { file_path: '/repo/new.ts', content: 'hi' },
        tool_response: { success: true },
      },
      env,
    );
    let envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.deepEqual(envelope.payload.files_touched, ['/repo/new.ts']);
    assert.equal(envelope.payload.state, 'working');
    assert.ok(envelope.payload.tool_output);

    await runBridge(
      HOOK_BRIDGE,
      ['PostToolUse'],
      {
        session_id: 's',
        tool_name: 'TodoWrite',
        tool_input: {
          todos: [
            { content: 'first task', status: 'in_progress', priority: 'high' },
            { content: 'second task', status: 'pending', priority: 'medium' },
          ],
        },
      },
      env,
    );
    envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.todos.length, 2);
    assert.equal(envelope.payload.todos[0].content, 'first task');
    assert.equal(envelope.payload.todos[0].status, 'in_progress');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PostToolUseFailure reports failed state and last_error', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(
      HOOK_BRIDGE,
      ['PostToolUseFailure'],
      { session_id: 's', tool_name: 'Bash', tool_input: { command: 'exit 1' }, error: 'command failed' },
      env,
    );
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'failed');
    assert.equal(envelope.payload.last_error, 'command failed');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PermissionRequest reports blocked/permission with pending_permission', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(
      HOOK_BRIDGE,
      ['PermissionRequest'],
      { session_id: 's', tool_name: 'Bash', tool_use_id: 'tu-2', tool_input: { command: 'rm -rf /' } },
      env,
    );
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assertEnvelopeShape(envelope, 'PermissionRequest');
    assert.equal(envelope.payload.state, 'blocked');
    assert.equal(envelope.payload.kind, 'permission');
    assert.equal(envelope.payload.pending_permission.tool, 'Bash');
    assert.equal(envelope.payload.pending_permission.tool_use_id, 'tu-2');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge Notification maps idle_prompt to done and permission to blocked', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(HOOK_BRIDGE, ['Notification'], { session_id: 's', notification_type: 'idle_prompt' }, env);
    let envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'done');

    await runBridge(
      HOOK_BRIDGE,
      ['Notification'],
      { session_id: 's', notification_type: 'permission_prompt', message: 'needs approval' },
      env,
    );
    envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'blocked');
    assert.equal(envelope.payload.kind, 'permission');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge Elicitation/ElicitationResult map to blocked question then working', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(HOOK_BRIDGE, ['Elicitation'], { session_id: 's', prompt: 'Pick one' }, env);
    let envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'blocked');
    assert.equal(envelope.payload.kind, 'question');

    await runBridge(HOOK_BRIDGE, ['ElicitationResult'], { session_id: 's', decision: 'accept' }, env);
    envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'working');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge PreCompact/PostCompact/TaskCreated/TaskCompleted push working envelopes', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    for (const event of ['PreCompact', 'PostCompact', 'TaskCreated', 'TaskCompleted']) {
      const res = await runBridge(HOOK_BRIDGE, [event], { session_id: 's', trigger: 'auto' }, env);
      assert.equal(res.code, 0, event);
      const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
      assert.equal(envelope.event, event);
      assert.equal(envelope.payload.state, 'working');
    }
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge SubagentStart/SubagentStop isolate subagent identity', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(
      HOOK_BRIDGE,
      ['SubagentStart'],
      { session_id: 'parent-sess', agent_id: 'agent-123', agent_type: 'Explore', prompt: 'go look' },
      env,
    );
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assertEnvelopeShape(envelope, 'SubagentStart');
    assert.equal(envelope.payload.is_subagent, true);
    assert.equal(envelope.payload.parent_session_id, 'parent-sess');
    assert.equal(envelope.payload.agent_id, 'agent-123');
    assert.equal(envelope.payload.agent_type, 'Explore');
    // Parent model/context must not be clobbered by subagent traffic.
    assert.equal(envelope.payload.session_id, 'parent-sess');
    assert.equal(envelope.payload.model, null);
    assert.equal(envelope.payload.context_tokens, null);

    await runBridge(
      HOOK_BRIDGE,
      ['SubagentStop'],
      { session_id: 'parent-sess', agent_id: 'agent-123', agent_type: 'Explore' },
      env,
    );
    const stopEnv = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(stopEnv.payload.is_subagent, true);
    assert.equal(stopEnv.payload.agent_id, 'agent-123');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge Stop parses transcript_path JSONL usage on Stop', async () => {
  const ctx = await setupEnv();
  const tmp = makeTempDir('claude-spaces-transcript-');
  try {
    const transcript = path.join(tmp, 't.jsonl');
    const lines = [
      { type: 'user', message: { role: 'user', content: 'hi' } },
      {
        type: 'assistant',
        message: {
          model: 'claude-opus-4-6',
          usage: { input_tokens: 100, output_tokens: 50, cache_read_input_tokens: 1000, cache_creation_input_tokens: 200 },
        },
      },
      {
        type: 'assistant',
        message: {
          model: 'claude-opus-4-6',
          usage: { input_tokens: 150, output_tokens: 75, cache_read_input_tokens: 500, cache_creation_input_tokens: 0 },
        },
      },
      { type: 'summary', summary: 'compacted once' },
    ];
    fs.writeFileSync(transcript, `${lines.map((l) => JSON.stringify(l)).join('\n')}\n`);
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(HOOK_BRIDGE, ['Stop'], { session_id: 's', transcript_path: transcript }, env);
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assertEnvelopeShape(envelope, 'Stop');
    assert.equal(envelope.payload.state, 'done');
    assert.deepEqual(envelope.payload.usage, {
      input_tokens: 250,
      output_tokens: 125,
      cache_read_tokens: 1500,
      cache_write_tokens: 200,
    });
    assert.equal(envelope.payload.model, 'claude-opus-4-6');
    assert.equal(envelope.payload.extras.turn_count, 2);
    assert.equal(envelope.payload.compaction_count, 1);
  } finally {
    await teardownEnv(ctx);
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test('hook-bridge Stop with error payload reports failed', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(HOOK_BRIDGE, ['Stop'], { session_id: 's', stop_reason: 'error', error: 'boom' }, env);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'failed');
    assert.equal(envelope.payload.last_error, 'boom');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge StopFailure and SessionEnd exit 0 with envelopes', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    let res = await runBridge(HOOK_BRIDGE, ['StopFailure'], { session_id: 's', error: 'nope' }, env);
    assert.equal(res.code, 0);
    let envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'failed');

    res = await runBridge(HOOK_BRIDGE, ['SessionEnd'], { session_id: 's', reason: 'exit' }, env);
    assert.equal(res.code, 0);
    envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.state, 'idle');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge passes background_tasks/session_crons through', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    await runBridge(
      HOOK_BRIDGE,
      ['Stop'],
      {
        session_id: 's',
        background_tasks: [{ id: 'bt-1', state: 'running', description: 'build' }],
        session_crons: [{ id: 'cron-1', schedule: '*/5 * * * *' }],
      },
      env,
    );
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.payload.background_tasks.length, 2);
  } finally {
    await teardownEnv(ctx);
  }
});

// ---------------------------------------------------------------------------
// statusline-bridge
// ---------------------------------------------------------------------------

function fullStatuslineSnapshot() {
  return {
    session_id: 'sess-status-1',
    transcript_path: '/tmp/t.jsonl',
    cwd: '/repo',
    model: { id: 'claude-opus-4-6', display_name: 'Opus' },
    workspace: { current_dir: '/repo/src', project_dir: '/repo' },
    version: '2.1.76',
    cost: {
      total_cost_usd: 1.234,
      total_duration_ms: 60000,
      total_api_duration_ms: 30000,
      total_lines_added: 42,
      total_lines_removed: 7,
    },
    context_window: {
      total_input_tokens: 9000,
      total_output_tokens: 1500,
      context_window_size: 200000,
      current_usage: {
        input_tokens: 5000,
        output_tokens: 800,
        cache_creation_input_tokens: 1200,
        cache_read_input_tokens: 30000,
      },
      used_percentage: 18.5,
      remaining_percentage: 81.5,
    },
    exceeds_200k_tokens: false,
    output_style: { name: 'default' },
  };
}

test('statusline-bridge parses full statusLine JSON and pushes rich metrics', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    // Exactly one clean status line on stdout, nothing else.
    const lines = res.stdout.split('\n').filter((l) => l !== '');
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('Opus'), `status line should name the model, got: ${lines[0]}`);
    assert.ok(lines[0].includes('src'), `status line should name the directory, got: ${lines[0]}`);

    const req = lastRequestBody(ctx.requests);
    assertWireHeaders(req);
    const envelope = JSON.parse(req.body);
    assertEnvelopeShape(envelope, 'statusline');
    assert.equal(envelope.harness_version, '2.1.76');
    const p = envelope.payload;
    assert.equal(p.session_id, 'sess-status-1');
    assert.equal(p.cwd, '/repo/src');
    assert.equal(p.model, 'Opus');
    assert.deepEqual(p.usage, {
      input_tokens: 5000,
      output_tokens: 800,
      cache_read_tokens: 30000,
      cache_write_tokens: 1200,
      cost_usd: 1.234,
    });
    assert.equal(p.context_tokens, 5000 + 800 + 30000 + 1200);
    assert.equal(p.context_window, 200000);
    assert.equal(p.context_percent, 18.5);
    assert.equal(p.state, 'working');
    assert.equal(p.extras.project_dir, '/repo');
    assert.equal(p.extras.output_style, 'default');
    assert.equal(p.extras.total_lines_added, 42);
  } finally {
    await teardownEnv(ctx);
  }
});

test('statusline-bridge delegates to SPACES_CLAUDE_STATUSLINE_PASSTHROUGH', async () => {
  const ctx = await setupEnv();
  try {
    const passthrough = `${process.execPath} -e "let d='';process.stdin.on('data',c=>d+=c).on('end',()=>{const s=JSON.parse(d);process.stdout.write('CUSTOM:'+s.session_id)})"`;
    const env = baseEnv(ctx.runtimeDir, ctx.binDir, { SPACES_CLAUDE_STATUSLINE_PASSTHROUGH: passthrough });
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    assert.ok(res.stdout.includes('CUSTOM:sess-status-1'), `passthrough output expected, got: ${res.stdout}`);
    // Telemetry still flows.
    assert.ok(ctx.requests.length > 0);
  } finally {
    await teardownEnv(ctx);
  }
});

test('statusline-bridge falls back to compact line when passthrough fails', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir, {
      SPACES_CLAUDE_STATUSLINE_PASSTHROUGH: 'definitely-not-a-real-command-xyz',
    });
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    assert.ok(res.stdout.trim().length > 0);
    assert.ok(res.stdout.includes('Opus'));
  } finally {
    await teardownEnv(ctx);
  }
});

test('statusline-bridge handles empty/garbage stdin with a default line', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(STATUSLINE_BRIDGE, [], null, env, 'not json{{{');
    assert.equal(res.code, 0);
    assert.ok(res.stdout.trim().length > 0);
  } finally {
    await teardownEnv(ctx);
  }
});

// ---------------------------------------------------------------------------
// safety: fail-open, timeouts, tmux gating
// ---------------------------------------------------------------------------

test('hook-bridge is fail-open when the socket/endpoint is missing', async () => {
  const runtimeDir = makeTempDir('claude-spaces-empty-');
  const binDir = makeFakeTmuxBin('s');
  try {
    const env = baseEnv(runtimeDir, binDir);
    const res = await runBridge(HOOK_BRIDGE, ['PostToolUse'], { session_id: 's', tool_name: 'Bash' }, env);
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
  } finally {
    fs.rmSync(runtimeDir, { recursive: true, force: true });
  }
});

test('statusline-bridge is fail-open when the socket/endpoint is missing', async () => {
  const runtimeDir = makeTempDir('claude-spaces-empty-');
  const binDir = makeFakeTmuxBin('s');
  try {
    const env = baseEnv(runtimeDir, binDir);
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    assert.ok(res.stdout.trim().length > 0, 'status line must still print without a listener');
  } finally {
    fs.rmSync(runtimeDir, { recursive: true, force: true });
  }
});

test('hook-bridge is fail-open on a slow socket (100ms budget)', async () => {
  const ctx = await setupEnv({ delayMs: 500 });
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(HOOK_BRIDGE, ['Stop'], { session_id: 's' }, env);
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
    assert.ok(res.elapsedMs < 5000, `bridge must not wait for the slow socket (took ${res.elapsedMs}ms)`);
  } finally {
    await teardownEnv(ctx);
  }
});

test('statusline-bridge is fail-open on a slow socket', async () => {
  const ctx = await setupEnv({ delayMs: 500 });
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    assert.ok(res.stdout.trim().length > 0);
    assert.ok(res.elapsedMs < 5000, `statusline must not wait for the slow socket (took ${res.elapsedMs}ms)`);
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge is a silent no-op when $TMUX_PANE is unset', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    delete env.TMUX_PANE;
    const res = await runBridge(HOOK_BRIDGE, ['UserPromptSubmit'], { session_id: 's', prompt: 'hi' }, env);
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
    assert.equal(ctx.requests.length, 0, 'no telemetry without $TMUX_PANE');
  } finally {
    await teardownEnv(ctx);
  }
});

test('statusline-bridge sends no telemetry when $TMUX_PANE is unset but still prints a line', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    delete env.TMUX_PANE;
    const res = await runBridge(STATUSLINE_BRIDGE, [], fullStatuslineSnapshot(), env);
    assert.equal(res.code, 0);
    assert.ok(res.stdout.trim().length > 0);
    assert.equal(ctx.requests.length, 0, 'no telemetry without $TMUX_PANE');
  } finally {
    await teardownEnv(ctx);
  }
});

test('bridges send with pane no-tmux when SPACES_PLUGIN_ALLOW_NO_TMUX=1', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir, { SPACES_PLUGIN_ALLOW_NO_TMUX: '1' });
    delete env.TMUX_PANE;
    const res = await runBridge(HOOK_BRIDGE, ['SessionStart'], { session_id: 's' }, env);
    assert.equal(res.code, 0);
    const envelope = JSON.parse(lastRequestBody(ctx.requests).body);
    assert.equal(envelope.pane, 'no-tmux');
  } finally {
    await teardownEnv(ctx);
  }
});

test('hook-bridge exits 0 on garbage stdin without emitting stdout JSON', async () => {
  const ctx = await setupEnv();
  try {
    const env = baseEnv(ctx.runtimeDir, ctx.binDir);
    const res = await runBridge(HOOK_BRIDGE, ['PreToolUse'], null, env, '{{{not json');
    assert.equal(res.code, 0);
    assert.equal(res.stdout, '');
  } finally {
    await teardownEnv(ctx);
  }
});

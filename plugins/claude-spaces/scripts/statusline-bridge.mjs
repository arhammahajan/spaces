#!/usr/bin/env node
// statusline-bridge.mjs — Claude Code statusLine bridge for tmux-spaces.
//
// Invoked as the `statusLine` command (see README) with Claude Code's
// statusLine JSON snapshot on stdin. Pushes a rich v2 metrics envelope
// (model / usage / cost / context window / workspace) to
// POST /v1/plugin/claude on the spacecore UDS listener, then prints a
// concise status line to stdout.
//
// Optional passthrough: when $SPACES_CLAUDE_STATUSLINE_PASSTHROUGH names a
// command, the snapshot is piped to it and its stdout is printed instead
// (falls back to the built-in compact line on any failure).
//
// Safety contract:
//   - NEVER emits non-statusline debug logs to stdout (only the status
//     line itself). Diagnostics go to stderr.
//   - Telemetry is fire-and-forget with a <=100ms budget; the status line
//     is always printed regardless of listener state.
//   - ALWAYS exits 0.

import { spawn } from 'node:child_process';
import path from 'node:path';
import { buildEnvelope, postEnvelope, readStdinJson, shouldSend } from './transport.mjs';

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

function str(v) {
  return typeof v === 'string' ? v : '';
}

function oneLine(value, limit) {
  let s;
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') s = value;
  else {
    try {
      s = JSON.stringify(value);
    } catch {
      s = String(value);
    }
  }
  // eslint-disable-next-line no-control-regex
  s = s.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, ' ');
  s = s.split(/\s+/).join(' ').trim();
  const runes = [...s];
  if (runes.length <= limit) return s;
  return `${runes.slice(0, limit - 1).join('')}…`;
}

/** Build the v2 payload from a Claude Code statusLine snapshot. Exported for tests. */
export function buildStatuslinePayload(snapshot) {
  const input = snapshot && typeof snapshot === 'object' ? snapshot : {};

  const modelObj = input.model && typeof input.model === 'object' ? input.model : null;
  const modelId = str(modelObj?.id) || str(input.model);
  const modelDisplay = str(modelObj?.display_name) || modelId;

  const workspace = input.workspace && typeof input.workspace === 'object' ? input.workspace : {};
  const currentDir = str(workspace.current_dir) || str(input.cwd);
  const projectDir = str(workspace.project_dir);

  const version = str(input.version);

  const cost = input.cost && typeof input.cost === 'object' ? input.cost : {};
  const costUsd = Number(cost.total_cost_usd);
  const durationMs = num(cost.total_duration_ms);
  const apiDurationMs = num(cost.total_api_duration_ms);
  const linesAdded = num(cost.total_lines_added);
  const linesRemoved = num(cost.total_lines_removed);

  const contextWindow = input.context_window && typeof input.context_window === 'object' ? input.context_window : {};
  const currentUsage =
    contextWindow.current_usage && typeof contextWindow.current_usage === 'object' ? contextWindow.current_usage : {};
  const inputTokens = num(currentUsage.input_tokens);
  const outputTokens = num(currentUsage.output_tokens);
  const cacheWriteTokens = num(currentUsage.cache_creation_input_tokens);
  const cacheReadTokens = num(currentUsage.cache_read_input_tokens);
  const windowSize = num(contextWindow.context_window_size);
  const usedPercent = Number(contextWindow.used_percentage);
  const remainingPercent = Number(contextWindow.remaining_percentage);

  const usage = {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    cache_read_tokens: cacheReadTokens,
    cache_write_tokens: cacheWriteTokens,
  };
  if (Number.isFinite(costUsd) && costUsd > 0) usage.cost_usd = costUsd;

  const sessionId = str(input.session_id);
  const exceeds200k = input.exceeds_200k_tokens === true;
  const outputStyle =
    input.output_style && typeof input.output_style === 'object' ? str(input.output_style.name) : str(input.output_style);
  const vim = input.vim && typeof input.vim === 'object' ? input.vim : null;

  const extras = {};
  if (version) extras.version = version;
  if (projectDir) extras.project_dir = projectDir;
  if (Number.isFinite(usedPercent)) extras.used_percentage = usedPercent;
  if (Number.isFinite(remainingPercent)) extras.remaining_percentage = remainingPercent;
  if (exceeds200k) extras.exceeds_200k_tokens = true;
  if (outputStyle) extras.output_style = outputStyle;
  if (vim && Object.keys(vim).length > 0) extras.vim = vim;
  if (durationMs) extras.total_duration_ms = durationMs;
  if (apiDurationMs) extras.total_api_duration_ms = apiDurationMs;
  if (linesAdded) extras.total_lines_added = linesAdded;
  if (linesRemoved) extras.total_lines_removed = linesRemoved;
  const totalInput = num(contextWindow.total_input_tokens);
  const totalOutput = num(contextWindow.total_output_tokens);
  if (totalInput) extras.total_input_tokens = totalInput;
  if (totalOutput) extras.total_output_tokens = totalOutput;

  return {
    payload: {
      session_id: sessionId || null,
      parent_session_id: null,
      is_subagent: false,
      agent_id: null,
      agent_type: null,
      title: null,
      cwd: currentDir || null,
      model: modelDisplay || modelId || null,
      mode: outputStyle || null,
      state: 'working',
      kind: null,
      text: oneLine(`${modelDisplay || modelId || 'claude'} · ${currentDir || ''}`.trim(), 240) || 'statusline',
      prompt: null,
      reply: null,
      tool: null,
      tool_call_id: null,
      tool_input: null,
      tool_output: null,
      usage,
      context_tokens: inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens,
      context_window: windowSize || null,
      context_percent: Number.isFinite(usedPercent) ? usedPercent : null,
      compaction_count: null,
      files_touched: [],
      todos: [],
      background_tasks: [],
      pending_permission: null,
      last_error: null,
      extras,
      raw_event: input,
    },
    version,
    modelId,
    modelDisplay,
    currentDir,
    costUsd: Number.isFinite(costUsd) && costUsd > 0 ? costUsd : 0,
    usedPercent: Number.isFinite(usedPercent) ? usedPercent : null,
  };
}

/** Render the built-in compact single-line status. Exported for tests. */
export function renderStatusLine({ modelDisplay, modelId, currentDir, costUsd, usedPercent }) {
  const model = modelDisplay || modelId || 'claude';
  const base = currentDir ? path.basename(currentDir.replace(/\/+$/, '')) || currentDir : '';
  const parts = [model];
  if (base) parts.push(base);
  if (usedPercent !== null && usedPercent !== undefined) parts.push(`ctx ${usedPercent}%`);
  if (costUsd > 0) parts.push(`$${costUsd.toFixed(costUsd < 10 ? 2 : 1)}`);
  return oneLine(parts.join(' · '), 200) || 'claude';
}

/**
 * Pipe the snapshot to the user's passthrough command and capture its
 * stdout. Resolves the trimmed first-line output, or null on any failure.
 */
export function runPassthrough(command, snapshotJson) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => {
      if (!done) {
        done = true;
        resolve(value);
      }
    };
    let child;
    try {
      child = spawn(command, { shell: true, stdio: ['pipe', 'pipe', 'ignore'] });
    } catch {
      finish(null);
      return;
    }
    let out = '';
    const timer = setTimeout(() => {
      try {
        child.kill('SIGKILL');
      } catch {
        // ignore
      }
      finish(null);
    }, 2000);
    if (typeof timer.unref === 'function') timer.unref();
    child.stdout.setEncoding('utf8');
    child.stdout.on('data', (chunk) => {
      out += chunk;
      if (out.length > 4096) out = out.slice(0, 4096);
    });
    child.on('error', () => {
      clearTimeout(timer);
      finish(null);
    });
    child.on('close', () => {
      clearTimeout(timer);
      const line = out.trim();
      finish(line === '' ? null : line.split('\n')[0]);
    });
    try {
      child.stdin.write(snapshotJson);
      child.stdin.end();
    } catch {
      clearTimeout(timer);
      finish(null);
    }
  });
}

async function main() {
  let snapshot = null;
  try {
    snapshot = await readStdinJson({ timeoutMs: 2000, maxBytes: 1024 * 1024 });
  } catch {
    snapshot = null;
  }
  const built = buildStatuslinePayload(snapshot ?? {});

  // Telemetry is best-effort and bounded: never delay the status line.
  try {
    if (shouldSend()) {
      const envelope = buildEnvelope('statusline', built.payload, built.version);
      await postEnvelope(envelope);
    }
  } catch {
    // fail-open
  }

  // Render: user passthrough wins, built-in compact line is the fallback.
  let line = null;
  try {
    const passthrough = process.env.SPACES_CLAUDE_STATUSLINE_PASSTHROUGH;
    if (passthrough) {
      line = await runPassthrough(passthrough, JSON.stringify(snapshot ?? {}));
    }
  } catch {
    line = null;
  }
  if (!line) {
    try {
      line = renderStatusLine(built);
    } catch {
      line = 'claude';
    }
  }
  process.stdout.write(`${line}\n`);
}

try {
  await main();
} catch {
  try {
    process.stdout.write('claude\n');
  } catch {
    // ignore
  }
}
process.exit(0);

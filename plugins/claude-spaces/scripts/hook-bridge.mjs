#!/usr/bin/env node
// hook-bridge.mjs — Claude Code lifecycle hook bridge for tmux-spaces.
//
// Invoked by hooks/hooks.json as:
//   node "${CLAUDE_PLUGIN_ROOT}/scripts/hook-bridge.mjs" <EventName>
// with the Claude Code hook JSON payload on stdin.
//
// Reads the payload, extracts session/tool/file/todo/subagent/permission/
// error/usage signals, optionally tail-reads transcript_path for cumulative
// usage on Stop/PostCompact/SessionEnd, and pushes a v2 envelope to
// POST /v1/plugin/claude on the spacecore UDS listener.
//
// Safety contract:
//   - ALWAYS exits 0 (never emits blocking stdout JSON that would
//     interfere with Claude Code tool execution).
//   - Fail-open: missing/slow socket, missing tmux, bad stdin all degrade
//     to a silent no-op.
//   - Subagent isolation: when agent_id is present (or the event is
//     SubagentStart/SubagentStop), the payload is marked is_subagent and
//     carries parent_session_id/agent_id/agent_type; model and context
//     fields are left unset so subagent traffic never clobbers the parent
//     session's stored model/context_tokens.

import fs from 'node:fs';
import {
  buildEnvelope,
  postEnvelope,
  readStdinJson,
  shouldSend,
} from './transport.mjs';

const FILE_EDIT_TOOLS = new Set(['Edit', 'Write', 'MultiEdit', 'NotebookEdit']);
const ASK_TOOLS = new Set([
  'askuserquestion',
  'ask_user_question',
  'request_user_input',
  'requestuserinput',
  'ask_user',
  'askuser',
  'ask_question',
  'askquestion',
]);

const MAX_TEXT = 240;
const MAX_FIELD = 4096;
const MAX_OUTPUT = 2048;

function str(v) {
  return typeof v === 'string' ? v : '';
}

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : 0;
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
  // Strip control chars (keep tab/LF/CR for the whitespace squash), squash.
  // eslint-disable-next-line no-control-regex
  s = s.replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, ' ');
  s = s.split(/\s+/).join(' ').trim();
  const runes = [...s];
  if (runes.length <= limit) return s;
  return `${runes.slice(0, limit - 1).join('')}…`;
}

function trunc(value, limit) {
  const s = oneLine(value, limit);
  return s === '' ? null : s;
}

function firstString(obj, keys) {
  if (!obj || typeof obj !== 'object') return '';
  for (const k of keys) {
    const v = obj[k];
    if (typeof v === 'string' && v.trim() !== '') return v.trim();
  }
  return '';
}

function toolTextFragment(input) {
  if (input === null || input === undefined) return '';
  if (typeof input === 'string') return input;
  if (Array.isArray(input)) {
    return input.map(toolTextFragment).filter(Boolean).join(' ');
  }
  if (typeof input === 'object') {
    for (const key of [
      'command',
      'CommandLine',
      'Command',
      'toolSummary',
      'file_path',
      'path',
      'Path',
      'TargetFile',
      'SearchPath',
      'pattern',
      'url',
      'Url',
      'query',
      'Query',
      'Role',
      'description',
      'Description',
      'prompt',
      'Prompt',
      'content',
      'new_string',
      'old_string',
    ]) {
      const v = input[key];
      if (typeof v === 'string' && v.trim() !== '') return v.trim();
    }
    for (const v of Object.values(input)) {
      if (typeof v === 'string' && v.trim() !== '') return v.trim();
    }
  }
  return '';
}

function buildPreview(tool, toolInput) {
  const fragment = oneLine(toolTextFragment(toolInput), MAX_TEXT);
  if (tool && fragment) {
    const runes = [...fragment];
    const short = runes.length > 120 ? `${runes.slice(0, 119).join('')}…` : fragment;
    return `${tool} · ${short}`;
  }
  return tool || fragment;
}

/** Extract touched file paths for Edit/Write/MultiEdit/NotebookEdit. */
export function extractFilesTouched(tool, input) {
  if (!FILE_EDIT_TOOLS.has(tool) || !input || typeof input !== 'object') return [];
  const out = [];
  const push = (v) => {
    if (typeof v === 'string' && v.trim() !== '') out.push(v.trim());
  };
  push(input.file_path ?? input.filePath ?? input.path);
  if (Array.isArray(input.edits)) {
    for (const edit of input.edits) {
      if (edit && typeof edit === 'object') push(edit.file_path ?? edit.filePath ?? edit.path);
    }
  }
  return [...new Set(out)];
}

/** Extract todo list for TodoWrite (also honours a top-level todos array). */
export function extractTodos(tool, input, raw) {
  let list = null;
  if (tool === 'TodoWrite' && input && typeof input === 'object' && Array.isArray(input.todos)) {
    list = input.todos;
  } else if (raw && Array.isArray(raw.todos)) {
    list = raw.todos;
  }
  if (!list) return [];
  return list
    .slice(0, 50)
    .map((t) => {
      if (!t || typeof t !== 'object') return null;
      const content = oneLine(t.content ?? t.text ?? t.title ?? '', MAX_TEXT);
      if (!content) return null;
      return {
        content,
        status: str(t.status),
        priority: str(t.priority ?? t.activeForm ?? ''),
      };
    })
    .filter(Boolean);
}

/** Pass through background task / cron snapshots with truncation + caps. */
function extractList(raw, keys) {
  for (const k of keys) {
    if (Array.isArray(raw?.[k])) return raw[k];
  }
  return [];
}

function normalizeEntries(list) {
  return list.slice(0, 32).map((item) => {
    if (!item || typeof item !== 'object') return { text: oneLine(item, MAX_TEXT) };
    const out = {};
    for (const [k, v] of Object.entries(item)) {
      out[k] = typeof v === 'string' ? oneLine(v, MAX_FIELD) : v;
    }
    return out;
  });
}

/**
 * Fast bounded tail-read of a Claude Code transcript JSONL file.
 * Sums per-message usage, captures the last model, counts usage-bearing
 * messages as turns and summary markers as compactions. Never throws.
 */
export function readTranscriptUsage(transcriptPath) {
  const result = { usage: null, model: '', turns: 0, compactions: 0 };
  if (typeof transcriptPath !== 'string' || transcriptPath === '') return result;
  try {
    const stat = fs.statSync(transcriptPath);
    if (!stat.isFile() || stat.size === 0) return result;
    const TAIL_BYTES = 256 * 1024;
    const start = Math.max(0, stat.size - TAIL_BYTES);
    const length = stat.size - start;
    const fd = fs.openSync(transcriptPath, 'r');
    try {
      const buf = Buffer.alloc(length);
      fs.readSync(fd, buf, 0, length, start);
      const text = buf.toString('utf8');
      let input = 0;
      let output = 0;
      let cacheRead = 0;
      let cacheWrite = 0;
      let model = '';
      let turns = 0;
      let compactions = 0;
      for (const line of text.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        let entry;
        try {
          entry = JSON.parse(trimmed);
        } catch {
          continue;
        }
        if (!entry || typeof entry !== 'object') continue;
        if (entry.type === 'summary' || entry.isCompactSummary === true) compactions += 1;
        const message = entry.message && typeof entry.message === 'object' ? entry.message : null;
        const usage =
          message && message.usage && typeof message.usage === 'object'
            ? message.usage
            : entry.usage && typeof entry.usage === 'object'
              ? entry.usage
              : null;
        if (usage) {
          input += num(usage.input_tokens);
          output += num(usage.output_tokens);
          cacheRead += num(usage.cache_read_input_tokens);
          cacheWrite += num(usage.cache_creation_input_tokens);
          turns += 1;
        }
        const m = (message && message.model) ?? entry.model;
        if (typeof m === 'string' && m !== '') model = m;
      }
      if (input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0) {
        result.usage = {
          input_tokens: input,
          output_tokens: output,
          cache_read_tokens: cacheRead,
          cache_write_tokens: cacheWrite,
        };
      }
      result.model = model;
      result.turns = turns;
      result.compactions = compactions;
    } finally {
      try {
        fs.closeSync(fd);
      } catch {
        // ignore
      }
    }
  } catch {
    // fail-open: unreadable transcript contributes nothing
  }
  return result;
}

function mapState(event, raw, tool) {
  const errorText = firstString(raw, ['error', 'error_message', 'errorMessage', 'reason', 'message']);
  switch (event) {
    case 'SessionStart':
    case 'SessionEnd':
      return { state: 'idle', kind: '' };
    case 'UserPromptSubmit':
      return { state: 'working', kind: '' };
    case 'PreToolUse':
      if (ASK_TOOLS.has(String(tool).toLowerCase().replace(/[^a-z]/g, ''))) {
        return { state: 'blocked', kind: 'question' };
      }
      return { state: 'working', kind: '' };
    case 'PostToolUse':
      return { state: 'working', kind: '' };
    case 'PostToolUseFailure':
      return { state: 'failed', kind: '' };
    case 'PermissionRequest': {
      const blob = `${str(raw.message)} ${str(raw.reason)} ${str(raw.notification_type)}`.toLowerCase();
      if (/sign in|sign-in|log ?in|login|authenticate|auth required|api key/.test(blob)) {
        return { state: 'blocked', kind: 'auth' };
      }
      return { state: 'blocked', kind: 'permission' };
    }
    case 'Notification': {
      const ntype = firstString(raw, ['notification_type', 'notificationType', 'type', 'notification']);
      const message = firstString(raw, ['message', 'body', 'text', 'title']);
      const lowered = ntype.toLowerCase();
      const messageLower = message.toLowerCase();
      if (
        lowered.includes('permission') ||
        lowered.includes('toolpermission') ||
        lowered.includes('approval') ||
        messageLower.includes('permission') ||
        messageLower.includes('approve')
      ) {
        return { state: 'blocked', kind: 'permission' };
      }
      if (lowered.includes('elicitation') || lowered.includes('question')) {
        return { state: 'blocked', kind: 'question' };
      }
      if (lowered.includes('idle')) return { state: 'done', kind: '' };
      if (lowered.includes('complete')) return { state: 'done', kind: '' };
      if (/sign in|sign-in|log ?in|login|authenticate|auth required|api key/.test(`${lowered} ${messageLower}`)) {
        return { state: 'blocked', kind: 'auth' };
      }
      return { state: 'working', kind: '' };
    }
    case 'Elicitation':
      return { state: 'blocked', kind: 'question' };
    case 'ElicitationResult':
      return { state: 'working', kind: '' };
    case 'PreCompact':
    case 'PostCompact':
      return { state: 'working', kind: '' };
    case 'SubagentStart':
    case 'SubagentStop':
    case 'TaskCreated':
    case 'TaskCompleted':
      return { state: 'working', kind: '' };
    case 'Stop':
      if (errorText !== '' || String(raw.stop_reason ?? raw.stopReason ?? '').toLowerCase() === 'error') {
        return { state: 'failed', kind: '' };
      }
      return { state: 'done', kind: '' };
    case 'StopFailure':
      return { state: 'failed', kind: '' };
    default:
      return { state: 'working', kind: '' };
  }
}

function buildText(event, raw, tool, toolInput, prompt, reply, errorText) {
  const preview = buildPreview(tool, toolInput);
  switch (event) {
    case 'PermissionRequest':
      if (preview) return `approve ${preview}`;
      if (errorText) return oneLine(errorText, MAX_TEXT);
      return 'needs approval';
    case 'Elicitation': {
      const q = prompt || errorText;
      return q ? `answer: ${oneLine(q, MAX_TEXT)}` : 'answer: needs an answer';
    }
    case 'UserPromptSubmit':
      return prompt ? `prompt: ${oneLine(prompt, MAX_TEXT)}` : 'prompt: working';
    case 'PostToolUseFailure':
      if (errorText) return `tool failed: ${oneLine(errorText, MAX_TEXT)}`;
      return preview ? `tool failed: ${preview}` : 'tool failed: failed';
    case 'Stop':
      if (reply) return oneLine(reply, MAX_TEXT);
      return 'turn complete';
    case 'StopFailure':
      return errorText ? `error: ${oneLine(errorText, MAX_TEXT)}` : 'error: turn failed';
    case 'SessionStart':
      return 'session started';
    case 'SessionEnd':
      return 'session ended';
    case 'PreCompact':
    case 'PostCompact':
      return 'compacting context';
    case 'Notification': {
      const m = firstString(raw, ['message', 'body', 'text', 'title']);
      return oneLine(m || 'notification', MAX_TEXT);
    }
    case 'SubagentStart':
      return `subagent: ${oneLine(firstString(raw, ['description', 'prompt']) || tool || 'started', MAX_TEXT)}`;
    case 'SubagentStop':
      return `subagent done: ${oneLine(firstString(raw, ['description', 'prompt']) || tool || 'finished', MAX_TEXT)}`;
    case 'TaskCreated':
      return `background task: ${oneLine(firstString(raw, ['description', 'command', 'title', 'name']) || 'started', MAX_TEXT)}`;
    case 'TaskCompleted':
      return `background task done: ${oneLine(firstString(raw, ['description', 'command', 'title', 'name']) || 'finished', MAX_TEXT)}`;
    default:
      return preview || oneLine(prompt || reply || errorText || event, MAX_TEXT);
  }
}

/** Build the v2 payload for one hook event. Exported for tests. */
export function buildPayload(event, raw) {
  const input = raw && typeof raw === 'object' ? raw : {};
  const tool = firstString(input, ['tool_name', 'toolName', 'name', 'tool']);
  const toolInput = input.tool_input ?? input.toolInput ?? input.toolArgs ?? input.input ?? input.arguments ?? null;
  const toolOutput =
    input.tool_response ?? input.toolResponse ?? input.tool_output ?? input.toolOutput ?? input.tool_result ?? input.toolResult ?? input.output ?? input.result ?? null;
  const toolCallId = firstString(input, ['tool_use_id', 'toolUseId', 'tool_use_ID', 'toolCallId', 'id']);
  const prompt = firstString(input, [
    'prompt',
    'user_prompt',
    'userPrompt',
    'initial_prompt',
    'initialPrompt',
    'user_message',
    'message',
    'description',
  ]);
  const reply = firstString(input, [
    'last_assistant_message',
    'lastAssistantMessage',
    'response',
    'finalModelOutput',
    'final_model_output',
    'assistant_response',
    'text',
  ]);
  const errorText = firstString(input, ['error', 'error_details', 'errorDetails', 'error_message', 'errorMessage', 'reason']);

  const agentId = firstString(input, ['agent_id', 'agentId', 'parent_agent_id', 'parentAgentId']);
  const agentType = firstString(input, ['agentName', 'agent_name', 'agent_type', 'agentType', 'subagent_type', 'subagentType']);
  const isSubagent = agentId !== '' || agentType !== '' || event === 'SubagentStart' || event === 'SubagentStop';

  const sessionId = firstString(input, [
    'session_id',
    'sessionId',
    'sessionID',
    'thread_id',
    'threadId',
    'conversation_id',
    'conversationId',
  ]);
  const cwd = firstString(input, ['cwd', 'working_directory', 'workingDirectory', 'workspaceRoot', 'project_dir']);
  const transcriptPath = firstString(input, ['transcript_path', 'transcriptPath']);

  let { state, kind } = mapState(event, input, tool);

  // Transcript fallback: cumulative usage/model/turns when the statusline
  // snapshot has not fired yet (Stop / PostCompact / SessionEnd).
  let transcriptUsage = null;
  let transcriptModel = '';
  let transcriptTurns = 0;
  let transcriptCompactions = 0;
  if ((event === 'Stop' || event === 'PostCompact' || event === 'SessionEnd') && transcriptPath) {
    const t = readTranscriptUsage(transcriptPath);
    transcriptUsage = t.usage;
    transcriptModel = t.model;
    transcriptTurns = t.turns;
    transcriptCompactions = t.compactions;
  }

  const rawUsage = input.usage && typeof input.usage === 'object' ? input.usage : null;
  let usage = null;
  if (rawUsage) {
    const u = {
      input_tokens: num(rawUsage.input_tokens ?? rawUsage.inputTokens),
      output_tokens: num(rawUsage.output_tokens ?? rawUsage.outputTokens),
      cache_read_tokens: num(rawUsage.cache_read_tokens ?? rawUsage.cacheReadTokens ?? rawUsage.cache_read_input_tokens),
      cache_write_tokens: num(rawUsage.cache_write_tokens ?? rawUsage.cacheWriteTokens ?? rawUsage.cache_creation_input_tokens),
    };
    const cost = Number(rawUsage.cost_usd ?? rawUsage.costUsd ?? rawUsage.costUSD);
    if (Number.isFinite(cost) && cost > 0) u.cost_usd = cost;
    if (u.input_tokens || u.output_tokens || u.cache_read_tokens || u.cache_write_tokens || u.cost_usd) {
      usage = u;
    }
  } else if (transcriptUsage) {
    usage = { ...transcriptUsage };
  }

  const model = isSubagent ? '' : firstString(input, ['model', 'model_name', 'modelName']) || transcriptModel;

  const text = buildText(event, input, tool, toolInput, prompt, reply, errorText);

  let title = firstString(input, [
    'conversation_title',
    'conversationTitle',
    'session_title',
    'sessionTitle',
    'custom_title',
    'customTitle',
    'title',
  ]);
  if (!title && event === 'UserPromptSubmit' && prompt) title = oneLine(prompt, MAX_TEXT);

  let toolInputStr = null;
  if (toolInput !== null && toolInput !== undefined) {
    toolInputStr = typeof toolInput === 'string' ? oneLine(toolInput, MAX_FIELD) : trunc(toolInput, MAX_FIELD);
  }
  let toolOutputStr = null;
  if (toolOutput !== null && toolOutput !== undefined) {
    toolOutputStr =
      typeof toolOutput === 'string' ? oneLine(toolOutput, MAX_OUTPUT) : trunc(toolOutput, MAX_OUTPUT);
  }

  let pendingPermission = null;
  if (event === 'PermissionRequest') {
    pendingPermission = {
      tool,
      tool_use_id: toolCallId || null,
      input: toolInputStr,
    };
  }

  const backgroundTasks = normalizeEntries(extractList(input, ['background_tasks', 'backgroundTasks']));
  const crons = normalizeEntries(extractList(input, ['session_crons', 'sessionCrons']));
  if (crons.length > 0) backgroundTasks.push(...crons.map((c) => ({ ...c, kind: 'cron' })));

  const extras = {};
  const permissionMode = firstString(input, ['permission_mode', 'permissionMode', 'mode']);
  if (permissionMode) extras.permission_mode = permissionMode;
  const source = firstString(input, ['source']);
  if (source) extras.source = oneLine(source, MAX_TEXT);
  const trigger = firstString(input, ['trigger']);
  if (trigger) extras.trigger = oneLine(trigger, MAX_TEXT);
  const stopReason = firstString(input, ['stop_reason', 'stopReason']);
  if (stopReason) extras.stop_reason = oneLine(stopReason, MAX_TEXT);
  const sessionReason = firstString(input, ['reason']);
  if (sessionReason && event !== 'PermissionRequest') extras.reason = oneLine(sessionReason, MAX_TEXT);
  const notificationType = firstString(input, ['notification_type', 'notificationType']);
  if (notificationType) extras.notification_type = notificationType;
  if (transcriptTurns > 0) extras.turn_count = transcriptTurns;

  return {
    session_id: sessionId || null,
    parent_session_id: isSubagent ? sessionId || null : null,
    is_subagent: isSubagent,
    agent_id: agentId || null,
    agent_type: agentType || null,
    title: title || null,
    cwd: cwd || null,
    model: model || null,
    mode: permissionMode || null,
    state,
    kind: kind || null,
    text,
    prompt: prompt ? oneLine(prompt, MAX_FIELD) : null,
    reply: reply ? oneLine(reply, MAX_FIELD) : null,
    tool: tool || null,
    tool_call_id: toolCallId || null,
    tool_input: toolInputStr,
    tool_output: toolOutputStr,
    usage,
    context_tokens: null,
    context_window: null,
    context_percent: null,
    compaction_count: transcriptCompactions > 0 ? transcriptCompactions : null,
    files_touched: extractFilesTouched(tool, toolInput),
    todos: extractTodos(tool, toolInput, input),
    background_tasks: backgroundTasks,
    pending_permission: pendingPermission,
    last_error: errorText ? oneLine(errorText, MAX_FIELD) : null,
    extras,
    raw_event: input,
  };
}

async function main() {
  const event = process.argv[2] || process.env.CLAUDE_HOOK_EVENT || 'unknown';
  let raw = null;
  try {
    raw = await readStdinJson({ timeoutMs: 2000, maxBytes: 4 * 1024 * 1024 });
  } catch {
    raw = null;
  }
  if (!shouldSend()) return;
  let payload;
  try {
    payload = buildPayload(event, raw ?? {});
  } catch {
    return;
  }
  try {
    const envelope = buildEnvelope(event, payload);
    await postEnvelope(envelope);
  } catch {
    // fail-open: never let telemetry break the hook chain
  }
}

// Always exit 0 — never emit blocking stdout JSON that would interfere
// with Claude Code tool execution.
try {
  await main();
} catch {
  // ignore
}
process.exit(0);

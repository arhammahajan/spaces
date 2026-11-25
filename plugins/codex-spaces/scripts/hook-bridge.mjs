// hook-bridge.mjs — Codex lifecycle hook bridge for tmux-spaces.
//
// Invoked by Codex hooks.json as:
//   node "<plugin_dir>/scripts/hook-bridge.mjs" <EventName>
// with the hook payload on stdin (modern Codex hooks.json JSON), or by the
// legacy config.toml `notify = [...]` path with a JSON payload in argv
// (e.g. {"type":"agent-turn-complete",...}).
//
// Builds a RICH v2 envelope and POSTs it to /v1/plugin/codex via
// transport.mjs. ALWAYS exits 0 so a hook can never block a Codex tool
// call or turn.
//
// Subagent isolation: when agent_id / parent_thread_id is present the
// payload is marked is_subagent so child activity never overwrites the
// parent thread's session_id, model or context tokens server-side.

import fs from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve as resolvePath } from "node:path";
import {
  shouldDeliver,
  buildEnvelope,
  postEnvelope,
} from "./transport.mjs";

const NESTED = ["data", "session", "thread", "conversation", "_meta"];

function first(obj, ...keys) {
  if (!obj || typeof obj !== "object") return undefined;
  for (const k of keys) {
    const v = obj[k];
    if (v !== undefined && v !== null && v !== "") return v;
  }
  for (const c of NESTED) {
    const n = obj[c];
    if (n && typeof n === "object") {
      const v = first(n, ...keys);
      if (v !== undefined && v !== null && v !== "") return v;
    }
  }
  return undefined;
}

function firstStr(obj, ...keys) {
  const v = first(obj, ...keys);
  if (typeof v === "string") {
    const t = v.trim();
    return t ? t : "";
  }
  if (typeof v === "number" && Number.isFinite(v)) return String(v);
  return "";
}

function asText(v, limit = 4096) {
  if (v === undefined || v === null) return "";
  let s;
  if (typeof v === "string") s = v;
  else {
    try {
      s = JSON.stringify(v);
    } catch {
      s = String(v);
    }
  }
  s = s.replace(/\s+/g, " ").trim();
  if (s.length > limit) s = s.slice(0, limit - 1) + "…";
  return s;
}

function oneLine(s, limit = 240) {
  s = String(s || "").replace(/\s+/g, " ").trim();
  if (s.length > limit) s = s.slice(0, limit - 1) + "…";
  return s;
}

// Read stdin with a deadline. Resolves the parsed JSON object, or null.
// Never blocks forever: TTY means no payload, and an open-but-quiet pipe
// (hooks that keep stdin open) resolves at the deadline with whatever
// complete JSON object arrived first.
export function readStdinJson(deadlineMs = 750) {
  return new Promise((resolve) => {
    if (process.stdin.isTTY) {
      resolve(null);
      return;
    }
    let data = "";
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      try {
        process.stdin.removeAllListeners("data");
        process.stdin.removeAllListeners("end");
        process.stdin.pause();
      } catch {
        // ignore
      }
      resolve(parseJsonObject(data));
    };
    const timer = setTimeout(finish, deadlineMs);
    try {
      process.stdin.setEncoding("utf8");
      process.stdin.on("data", (chunk) => {
        data += chunk;
        if (data.length > 4 * 1024 * 1024) {
          finish();
          return;
        }
        // Early exit: a complete JSON object already arrived.
        if (parseJsonObject(data) !== null) finish();
      });
      process.stdin.on("end", finish);
      process.stdin.resume();
    } catch {
      finish();
    }
  });
}

function parseJsonObject(text) {
  const t = String(text || "").trim();
  if (!t) return null;
  try {
    const v = JSON.parse(t);
    if (v && typeof v === "object" && !Array.isArray(v)) return v;
    return null;
  } catch {
    return null;
  }
}

// Legacy notify argv scan: find the first argv element (from index 2) that
// parses as a JSON object.
export function findArgvJson(argv) {
  for (let i = 2; i < argv.length; i++) {
    const v = parseJsonObject(argv[i]);
    if (v) return v;
  }
  // Also handle a single JSON blob split across argv words.
  if (argv.length > 3) {
    const v = parseJsonObject(argv.slice(2).join(" "));
    if (v) return v;
  }
  return null;
}

// Extract touched file paths from apply_patch patch text:
//   *** Update File: <path> / *** Add File: <path> / *** Delete File: <path>
function filesFromPatch(patchText) {
  const out = [];
  const re = /^\*\*\*\s+(?:Update File|Add File|Delete File):\s*(.+?)\s*$/gm;
  let m;
  const text = String(patchText || "");
  while ((m = re.exec(text)) !== null) {
    let p = m[1].trim().replace(/^["']|["']$/g, "");
    if (p) out.push(p);
  }
  return out;
}

export function extractFilesTouched(toolName, toolInput) {
  const files = [];
  const push = (p) => {
    if (typeof p !== "string") return;
    const t = p.trim().replace(/^["']|["']$/g, "");
    if (t && !files.includes(t)) files.push(t);
  };
  const inputText =
    typeof toolInput === "string" ? toolInput : asText(toolInput, 200000);
  // apply_patch / edit patch bodies (string or embedded in object fields).
  for (const p of filesFromPatch(inputText)) push(p);
  if (toolInput && typeof toolInput === "object") {
    for (const k of [
      "file_path",
      "filePath",
      "path",
      "filename",
      "file",
      "target_file",
      "targetFile",
    ]) {
      const v = toolInput[k];
      if (typeof v === "string") push(v);
      else if (Array.isArray(v)) v.forEach(push);
    }
    // Common patch-bearing fields when the harness passes an object.
    for (const k of ["patch", "diff", "content", "code", "input"]) {
      const v = toolInput[k];
      if (typeof v === "string") {
        for (const p of filesFromPatch(v)) push(p);
      }
    }
  }
  return files.slice(0, 100);
}

// Fast tail-read of a Codex rollout JSONL file (last 128KB only).
// Extracts token usage, context window, rate limits and compactions.
export function readRolloutTail(rolloutPath) {
  const result = {
    usage: null,
    context_tokens: 0,
    context_window: 0,
    rate_limits: null,
    rate_limited: false,
    rate_limit_reset_ms: 0,
    rate_limit_message: "",
    compaction_count: 0,
    turn_count: 0,
  };
  if (!rolloutPath || typeof rolloutPath !== "string") return result;
  let stat;
  try {
    stat = fs.statSync(rolloutPath);
  } catch {
    return result;
  }
  if (!stat.isFile()) return result;
  const TAIL = 128 * 1024;
  const start = Math.max(0, stat.size - TAIL);
  let text = "";
  try {
    const fd = fs.openSync(rolloutPath, "r");
    try {
      const len = Math.min(stat.size - start, TAIL);
      const buf = Buffer.alloc(len);
      fs.readSync(fd, buf, 0, len, start);
      text = buf.toString("utf8");
    } finally {
      fs.closeSync(fd);
    }
  } catch {
    return result;
  }
  const num = (v) => {
    const n = Number(v);
    return Number.isFinite(n) ? n : 0;
  };
  const pick = (o, ...keys) => {
    if (!o || typeof o !== "object") return 0;
    for (const k of keys) {
      if (o[k] !== undefined && o[k] !== null && o[k] !== "") {
        const n = num(o[k]);
        if (n) return n;
      }
    }
    return 0;
  };
  let totalIn = 0;
  let totalOut = 0;
  let totalReason = 0;
  let totalCache = 0;
  let lastCtx = 0;
  let lastWin = 0;
  let turns = 0;
  let compactions = 0;
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t || t[0] !== "{") continue;
    let ev;
    try {
      ev = JSON.parse(t);
    } catch {
      continue;
    }
    if (!ev || typeof ev !== "object") continue;
    const payload = ev.payload && typeof ev.payload === "object" ? ev.payload : ev;
    const ptype = typeof payload.type === "string" ? payload.type : "";
    const etype = typeof ev.type === "string" ? ev.type : "";
    if (/compact/i.test(ptype) || /compact/i.test(etype)) compactions++;
    const info =
      payload.info && typeof payload.info === "object" ? payload.info : null;
    const total = info
      ? info.total_token_usage || info.totalTokenUsage
      : payload.total_token_usage || payload.totalTokenUsage;
    const last = info
      ? info.last_token_usage || info.lastTokenUsage
      : payload.last_token_usage || payload.lastTokenUsage;
    if (total && typeof total === "object") {
      totalIn = pick(total, "input_tokens", "inputTokens", "input");
      totalOut = pick(total, "output_tokens", "outputTokens", "output");
      totalReason = pick(
        total,
        "reasoning_tokens",
        "reasoningTokens",
        "reasoning",
      );
      totalCache = pick(
        total,
        "cache_read_tokens",
        "cacheReadTokens",
        "cached_tokens",
        "cachedTokens",
      );
      turns++;
    } else if (last && typeof last === "object") {
      turns++;
    }
    if (last && typeof last === "object") {
      lastCtx =
        pick(last, "total_tokens", "totalTokens") ||
        pick(last, "input_tokens", "inputTokens", "input") +
          pick(last, "output_tokens", "outputTokens", "output") ||
        lastCtx;
    }
    const win = info
      ? pick(info, "model_context_window", "modelContextWindow")
      : 0;
    if (win) lastWin = win;
    const rl =
      payload.rate_limits ||
      payload.rateLimits ||
      ev.rate_limits ||
      ev.rateLimits;
    if (rl && typeof rl === "object") {
      result.rate_limits = rl;
      for (const scope of [rl.primary, rl.secondary]) {
        if (!scope || typeof scope !== "object") continue;
        const resetsIn = pick(
          scope,
          "resets_in_seconds",
          "resetsInSeconds",
          "reset_in_seconds",
          "retry_after_seconds",
        );
        const resetAt = pick(
          scope,
          "reset_at",
          "resetAt",
          "resets_at",
          "reset_at_ms",
        );
        const limited =
          scope.limited === true ||
          scope.rate_limited === true ||
          /rate.?limit|429/.test(
            String(scope.message || scope.error || ""),
          );
        if (limited) result.rate_limited = true;
        if (resetsIn > 0) {
          result.rate_limit_reset_ms = Date.now() + resetsIn * 1000;
        } else if (resetAt > 0) {
          result.rate_limit_reset_ms =
            resetAt < 1e12 ? resetAt * 1000 : resetAt;
        }
        const msg = scope.message || scope.error;
        if (typeof msg === "string" && msg.trim()) {
          result.rate_limit_message = msg.trim().slice(0, 500);
        }
      }
    }
  }
  if (totalIn || totalOut || totalReason || totalCache) {
    result.usage = {
      input_tokens: totalIn,
      output_tokens: totalOut,
      reasoning_tokens: totalReason,
      cache_read_tokens: totalCache,
      cost_usd: 0,
    };
  }
  result.context_tokens = lastCtx;
  result.context_window = lastWin;
  result.compaction_count = compactions;
  result.turn_count = turns;
  return result;
}

function stateForEvent(event) {
  switch (event) {
    case "SessionStart":
      return { state: "idle", kind: "" };
    case "SessionEnd":
      return { state: "idle", kind: "" };
    case "UserPromptSubmit":
      return { state: "working", kind: "" };
    case "PreToolUse":
      return { state: "working", kind: "" };
    case "PostToolUse":
      return { state: "working", kind: "" };
    case "PermissionRequest":
      return { state: "blocked", kind: "permission" };
    case "Stop":
      return { state: "done", kind: "" };
    default:
      return { state: "working", kind: "" };
  }
}

// Build the v2 payload from a hook payload object + native event name.
export function buildPayload(eventName, hook, argvJson) {
  const src = hook && typeof hook === "object" ? hook : {};
  const legacy =
    argvJson && typeof argvJson === "object" ? argvJson : null;

  const sessionId = firstStr(
    src,
    "session_id",
    "sessionId",
    "sessionID",
    "thread_id",
    "threadId",
    "conversation_id",
    "conversationId",
    "id",
  );
  const parentSessionId = firstStr(
    src,
    "parent_thread_id",
    "parentThreadId",
    "parent_conversation_id",
    "parentConversationId",
    "parent_session_id",
    "parentSessionId",
    "root_conversation_id",
    "rootConversationId",
  );
  const agentId = firstStr(src, "agent_id", "agentId");
  const isSubagent = Boolean(agentId || parentSessionId);

  const cwd = firstStr(
    src,
    "cwd",
    "working_directory",
    "workingDirectory",
    "workspaceRoot",
    "workspace_root",
  );
  const model = firstStr(src, "model", "model_name", "modelName");
  const effort = firstStr(
    src,
    "reasoning_effort",
    "reasoningEffort",
    "model_reasoning_effort",
    "effort",
  );
  const mode = firstStr(src, "mode", "execution_mode", "executionMode");
  const sandboxMode = firstStr(
    src,
    "sandbox_mode",
    "sandboxMode",
    "sandbox",
  );
  const approvalPolicy = firstStr(
    src,
    "approval_policy",
    "approvalPolicy",
    "approval",
  );
  const title = firstStr(
    src,
    "title",
    "conversation_title",
    "conversationTitle",
    "session_title",
    "sessionTitle",
    "thread_title",
    "threadTitle",
  );
  const prompt = asText(
    first(
      src,
      "prompt",
      "user_prompt",
      "userPrompt",
      "lastUserInput",
      "last_user_input",
      "initial_prompt",
      "initialPrompt",
      "user_message",
      "message",
    ),
  );
  const reply = asText(
    first(
      src,
      "last_assistant_message",
      "lastAssistantMessage",
      "finalModelOutput",
      "final_model_output",
      "modelOutput",
      "model_output",
      "assistant_message",
      "assistant_response",
      "last_message",
      "response",
      "text",
    ),
  );
  const tool = firstStr(src, "tool_name", "toolName", "name", "tool");
  const toolCallId = firstStr(
    src,
    "tool_call_id",
    "toolCallId",
    "call_id",
    "callId",
  );
  const toolInput = first(
    src,
    "tool_input",
    "toolInput",
    "toolArgs",
    "tool_args",
    "input",
    "arguments",
    "args",
  );
  const toolOutput = first(
    src,
    "tool_response",
    "toolResponse",
    "tool_output",
    "toolOutput",
    "tool_result",
    "toolResult",
    "output",
    "result",
  );
  const lastError = asText(
    first(
      src,
      "error",
      "error_details",
      "errorDetails",
      "error_message",
      "errorMessage",
      "reason",
    ),
    2000,
  );

  const filesTouched = extractFilesTouched(tool, toolInput);

  const pendingPermission =
    eventName === "PermissionRequest"
      ? asText(
          first(src, "permission", "request", "proposal", "tool_input") ??
            toolInput,
          2000,
        ) || tool
      : null;

  const turnCount = Number(first(src, "turn_count", "turnCount", "turn")) || 0;
  const toolCallCount =
    Number(first(src, "tool_call_count", "toolCallCount")) || 0;
  const toolErrorCount =
    Number(first(src, "tool_error_count", "toolErrorCount")) || 0;

  // Optional rollout JSONL enrichment.
  const rolloutPath = firstStr(
    src,
    "transcript_path",
    "transcriptPath",
    "rollout_path",
    "rolloutPath",
    "transcript_file",
    "transcriptFile",
  );
  const tail = readRolloutTail(rolloutPath);

  let usage = null;
  const inlineUsage = first(src, "usage");
  if (inlineUsage && typeof inlineUsage === "object") {
    const n = (v) => (Number.isFinite(Number(v)) ? Number(v) : 0);
    usage = {
      input_tokens: n(inlineUsage.input_tokens ?? inlineUsage.inputTokens),
      output_tokens: n(inlineUsage.output_tokens ?? inlineUsage.outputTokens),
      reasoning_tokens: n(
        inlineUsage.reasoning_tokens ?? inlineUsage.reasoningTokens,
      ),
      cache_read_tokens: n(
        inlineUsage.cache_read_tokens ?? inlineUsage.cacheReadTokens,
      ),
      cost_usd: Number(inlineUsage.cost_usd ?? inlineUsage.costUsd) || 0,
    };
    if (
      !usage.input_tokens &&
      !usage.output_tokens &&
      !usage.reasoning_tokens &&
      !usage.cache_read_tokens &&
      !usage.cost_usd
    ) {
      usage = null;
    }
  }
  if (!usage && tail.usage) usage = tail.usage;

  let contextTokens =
    Number(first(src, "context_tokens", "contextTokens")) || tail.context_tokens;
  let contextWindow =
    Number(first(src, "context_window", "contextWindow")) || tail.context_window;
  let contextPercent = 0;
  if (contextTokens > 0 && contextWindow > 0) {
    contextPercent = Math.round((contextTokens / contextWindow) * 1000) / 10;
  }

  let compactionCount =
    Number(first(src, "compaction_count", "compactionCount")) ||
    tail.compaction_count;
  let turns = turnCount || tail.turn_count;

  let rateLimited = tail.rate_limited;
  let rateLimitResetMs = tail.rate_limit_reset_ms;
  let rateLimitMessage = tail.rate_limit_message;
  const rlInline = first(src, "rate_limited", "rateLimited");
  if (rlInline === true) rateLimited = true;

  let { state, kind } = stateForEvent(eventName);
  if (eventName === "Stop" && lastError) {
    state = "failed";
  }
  if (eventName === "PostToolUse") {
    const status = firstStr(src, "status", "tool_status", "result_status");
    if (/fail|error|denied/i.test(status) || /fail|error/i.test(lastError)) {
      // keep working (tool failure, turn continues); surface error text
    }
  }

  let text;
  switch (eventName) {
    case "SessionStart":
      text = "session started";
      break;
    case "SessionEnd":
      text = "session ended";
      break;
    case "UserPromptSubmit":
      text = prompt ? `prompt: ${oneLine(prompt)}` : "prompt: working";
      break;
    case "PreToolUse":
      text = tool
        ? `${tool}${toolInput ? ` · ${oneLine(asText(toolInput, 240))}` : ""}`
        : "tool running";
      break;
    case "PostToolUse":
      text = tool ? `${tool} done` : "tool done";
      break;
    case "PermissionRequest":
      text = tool ? `approve ${tool}` : "needs approval";
      break;
    case "Stop":
      if (state === "failed") text = `error: ${oneLine(lastError) || "turn failed"}`;
      else text = reply ? oneLine(reply, 240) : "turn complete";
      break;
    default:
      text = oneLine(reply || prompt || tool || eventName, 240) || eventName;
  }

  const extras = {};
  if (legacy) {
    extras.notify_type = legacy.type || "notify";
    if (legacy.type === "agent-turn-complete") {
      if (legacy.turn) extras.turn = legacy.turn;
      if (legacy.duration_ms !== undefined)
        extras.duration_ms = legacy.duration_ms;
    }
  }
  if (approvalPolicy) extras.approval_policy = approvalPolicy;
  if (tail.rate_limits) extras.rate_limits = tail.rate_limits;

  const payload = {
    session_id: sessionId || null,
    parent_session_id: parentSessionId || null,
    is_subagent: isSubagent,
    agent_id: agentId || null,
    title: title || null,
    cwd: cwd || null,
    model: model || null,
    effort: effort || null,
    mode: mode || null,
    sandbox_mode: sandboxMode || null,
    state,
    kind: kind || null,
    text,
    prompt: prompt || null,
    reply: reply || null,
    tool: tool || null,
    tool_call_id: toolCallId || null,
    tool_input: toolInput !== undefined ? asText(toolInput) || null : null,
    tool_output: toolOutput !== undefined ? asText(toolOutput, 2048) || null : null,
    usage,
    context_tokens: contextTokens || 0,
    context_window: contextWindow || 0,
    context_percent: contextPercent,
    turn_count: turns || 0,
    compaction_count: compactionCount || 0,
    tool_call_count: toolCallCount,
    tool_error_count: toolErrorCount,
    rate_limited: rateLimited,
    rate_limit_reset_ms: rateLimitResetMs || 0,
    rate_limit_message: rateLimitMessage || "",
    files_touched: filesTouched,
    pending_permission: pendingPermission,
    last_error: lastError || "",
    extras,
    raw_event: src,
  };
  return payload;
}

async function main() {
  try {
    const argvJson = findArgvJson(process.argv);
    let eventName = process.argv[2] || "";
    // Legacy notify: the "event" is a JSON blob like
    // {"type":"agent-turn-complete",...} in argv.
    let legacy = null;
    if (argvJson && (argvJson.type || argvJson.event)) {
      legacy = argvJson;
      const t = String(argvJson.type || argvJson.event || "");
      if (/agent-turn-complete|turn-complete|turn_complete/i.test(t)) {
        eventName = "Stop";
      } else if (!eventName || eventName.startsWith("{")) {
        eventName = "Stop";
      }
    }
    const stdinJson = await readStdinJson(750);
    // A modern hooks.json payload may also carry the event name.
    if ((!eventName || eventName.startsWith("{")) && stdinJson) {
      const t = firstStr(
        stdinJson,
        "hook_event_name",
        "hookEventName",
        "event",
        "type",
      );
      if (t) eventName = t;
    }
    if (!eventName || eventName.startsWith("{")) eventName = "Stop";
    const hook = stdinJson || legacy || {};
    // Legacy notify payloads arrive via argv; merge them in.
    const merged =
      stdinJson && legacy ? { ...legacy, ...stdinJson } : hook;

    if (!shouldDeliver()) process.exit(0);

    const payload = buildPayload(eventName, merged, legacy);
    const envelope = buildEnvelope(eventName, payload);
    const hookVersion = firstStr(
      merged,
      "codex_version",
      "codexVersion",
      "version",
      "cli_version",
    );
    if (hookVersion) envelope.harness_version = hookVersion.slice(0, 100);

    await postEnvelope(envelope);
    process.exit(0);
  } catch {
    process.exit(0);
  }
}

const isMain =
  process.argv[1] &&
  resolvePath(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  main();
}

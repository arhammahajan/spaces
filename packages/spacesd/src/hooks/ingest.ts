import * as path from "node:path";
import type {
  HookChild,
  HookDetail,
  HookEmitParams,
  HookEmitResult,
  HookRecordOutcomeParams,
  HookRemoveOutcomeParams,
  HookTool,
  HookUsage,
  TimestampMs,
} from "@spaces/types";
import type { EventBus } from "../core/event-bus";
import type {
  HookPaneInput,
  HookPaneRecord,
  RecordOutcomeResult,
  SpacesStore,
} from "../store/db";

const MAX_TEXT = 240;
const MAX_FIELD = 4096;
const MAX_OUTPUT = 2048;
const MAX_MODEL = 200;
const MAX_SESSION = 200;
const MAX_CWD = 1000;

const AGENT_ALIASES: Record<string, string> = {
  agy: "antigravity",
  dsh: "deepseek",
  "kimi-code": "kimi",
  cursor: "cursor-agent",
};

const APP_NAMES: Record<string, string> = {
  claude: "claude-code",
  "kimi-cli": "kimi",
  dsh: "deepseek",
  "dsh-tui": "deepseek",
  cursor: "cursor-agent",
};

const EXT_SUFFIX_RE = /\.(js|mjs|cjs|py|exe|cmd|bat)$/;
const NON_ALNUM_RE = /[^a-z0-9]/g;
const CONTROL_CHARS_RE = /[\u0000-\u001f\u007f]/g;

const NESTED_CONTAINERS = ["data", "session", "thread", "conversation", "_meta"] as const;

const ASK_TOOLS = new Set([
  "askuserquestion",
  "ask_user_question",
  "request_user_input",
  "requestuserinput",
  "ask_user",
  "askuser",
  "ask_question",
  "askquestion",
]);

const ACTIVE_BG_STATES = new Set(["working", "running", "pending", "active", "starting"]);

const EVENT_ALIASES: Record<string, string> = (() => {
  const map: Record<string, string> = {};
  const add = (canonical: string, ...names: string[]): void => {
    for (const name of names) {
      map[name.toLowerCase().replace(NON_ALNUM_RE, "")] = canonical;
    }
  };
  add("session_start", "SessionStart", "session_start", "start");
  add("session_end", "SessionEnd", "session_end", "end");
  add(
    "prompt",
    "UserPromptSubmit",
    "userPromptSubmitted",
    "user_prompt_submit",
    "beforeSubmitPrompt",
    "BeforeAgent",
    "prompt",
    "busy",
    "sessionBusy",
    "session.busy",
    "session.execution.started",
  );
  add("pre_invocation", "PreInvocation", "preInvocation", "pre_invocation");
  add(
    "pre_tool",
    "PreToolUse",
    "preToolUse",
    "pre_tool_use",
    "BeforeTool",
    "beforeShellExecution",
    "beforeMCPExecution",
    "toolCall",
    "tool.call",
    "tool.execute.before",
  );
  add(
    "post_tool",
    "PostToolUse",
    "postToolUse",
    "post_tool_use",
    "AfterTool",
    "PostToolBatch",
    "afterAgentThought",
    "toolResult",
    "tool.result",
    "tool.execute.after",
  );
  add("tool_failure", "PostToolUseFailure", "postToolUseFailure", "tool_failure", "ToolError");
  add(
    "permission",
    "PermissionRequest",
    "permissionRequest",
    "permissionRequested",
    "PermissionDenied",
    "permission.asked",
    "permission.updated",
  );
  add(
    "stop",
    "Stop",
    "stop",
    "agentStop",
    "AfterAgent",
    "Interrupt",
    "StopCancelled",
    "SessionIdle",
    "turnEnd",
    "session.execution.succeeded",
    "session.execution.interrupted",
    "session.idle",
  );
  add(
    "response",
    "afterAgentResponse",
    "AgentResponse",
    "agentResponse",
    "PostInvocation",
    "postInvocation",
    "post_invocation",
  );
  add(
    "stop_failure",
    "StopFailure",
    "stopFailure",
    "ErrorOccurred",
    "session.execution.failed",
    "session.error",
  );
  add("subagent_start", "SubagentStart", "subagentStart");
  add("subagent_stop", "SubagentStop", "subagentStop", "SubagentEnd", "TeammateIdle");
  add("task_created", "TaskCreated", "taskCreated");
  add(
    "task_completed",
    "TaskCompleted",
    "taskCompleted",
    "BackgroundTaskCompleted",
    "backgroundTaskCompleted",
  );
  add("compact", "PreCompact", "PostCompact", "PreCompress", "compact");
  add("notification", "Notification", "notification");
  add("elicitation", "Elicitation", "question.asked");
  add("elicitation_result", "ElicitationResult", "question.replied", "question.rejected");
  add("working", "working", "agent.start", "permission.replied");
  add("done", "done", "agent.end");
  add("error", "error", "failed");
  add("blocked", "blocked", "needs_you", "needsYou");
  add("idle", "idle");
  return map;
})();

export interface NormalizedHookEvent {
  agent: string;
  app: string;
  source: string;
  event: string;
  rawEvent: string;
  state: string;
  kind: string | null;
  text: string;
  title: string | null;
  prompt: string | null;
  reply: string | null;
  model: string | null;
  profile: string | null;
  sessionId: string | null;
  cwd: string | null;
  tool: HookTool | null;
  usage: HookUsage | null;
}

function isRecord(val: unknown): val is Record<string, unknown> {
  return typeof val === "object" && val !== null && !Array.isArray(val);
}

function truncateRunes(s: string, limit: number): string {
  const chars = Array.from(s);
  if (chars.length <= limit) {
    return s;
  }
  return chars.slice(0, limit).join("");
}

function oneLine(value: unknown, limit: number, keepTail: boolean): string {
  if (value === null || value === undefined) {
    return "";
  }
  let s: string;
  if (typeof value === "string") {
    s = value;
  } else {
    try {
      s = JSON.stringify(value);
    } catch {
      s = String(value);
    }
  }
  s = s.replace(CONTROL_CHARS_RE, " ").trim().split(/\s+/).filter(Boolean).join(" ");
  const chars = Array.from(s);
  if (chars.length <= limit) {
    return s;
  }
  if (keepTail) {
    return `…${chars.slice(chars.length - (limit - 1)).join("")}`;
  }
  const n = Math.max(1, limit - 1);
  return `${chars.slice(0, n).join("")}…`;
}

function fieldStr(value: unknown, limit: number, keepTail: boolean): string | null {
  const text = oneLine(value, limit, keepTail);
  return text !== "" ? text : null;
}

function first(payload: Record<string, unknown>, ...keys: string[]): unknown {
  for (const key of keys) {
    if (Object.hasOwn(payload, key)) {
      const v = payload[key];
      if (v !== null && v !== undefined && v !== "") {
        return v;
      }
    }
  }
  for (const container of NESTED_CONTAINERS) {
    const nested = payload[container];
    if (isRecord(nested)) {
      const v = first(nested, ...keys);
      if (v !== undefined) {
        return v;
      }
    }
  }
  return undefined;
}

function firstStr(payload: Record<string, unknown>, ...keys: string[]): string {
  const v = first(payload, ...keys);
  if (typeof v === "string") {
    const t = v.trim();
    if (t !== "") {
      return t;
    }
  }
  return "";
}

function toolText(value: unknown): string {
  if (value === null || value === undefined) {
    return "";
  }
  if (typeof value === "string") {
    return value;
  }
  if (Array.isArray(value)) {
    const parts: string[] = [];
    for (const item of value) {
      if (item !== null && item !== undefined && item !== false && item !== 0 && item !== "") {
        const t = toolText(item);
        if (t) {
          parts.push(t);
        }
      }
    }
    return parts.join(" ");
  }
  if (isRecord(value)) {
    const preferredKeys = [
      "command",
      "CommandLine",
      "Command",
      "toolSummary",
      "file_path",
      "path",
      "Path",
      "TargetFile",
      "SearchPath",
      "pattern",
      "url",
      "Url",
      "query",
      "Query",
      "Role",
      "description",
      "Description",
      "prompt",
      "Prompt",
      "content",
      "new_string",
      "old_string",
    ];
    for (const key of preferredKeys) {
      const v = value[key];
      if (typeof v === "string" && v.trim() !== "") {
        return v.trim();
      }
    }
    for (const candidate of Object.values(value)) {
      if (typeof candidate === "string" && candidate.trim() !== "") {
        return candidate.trim();
      }
    }
  }
  return "";
}

function buildPreview(tool: string, toolInput: unknown): string {
  const t = tool.trim();
  let preview = oneLine(toolText(toolInput), MAX_TEXT, false);
  if (t && preview) {
    const chars = Array.from(preview);
    if (chars.length > 120) {
      preview = `${chars.slice(0, 119).join("")}…`;
    }
    return `${t} · ${preview}`;
  }
  return t || preview;
}

function looksAuth(payload: Record<string, unknown>): boolean {
  const parts: string[] = [];
  for (const key of ["message", "reason", "notification_type", "notificationType", "text"]) {
    const v = payload[key];
    if (typeof v === "string") {
      parts.push(v);
    }
  }
  const text = parts.join(" ").toLowerCase();
  const needles = [
    "sign in",
    "sign-in",
    "log in",
    "login",
    "authenticate",
    "auth required",
    "api key",
  ];
  return needles.some((needle) => text.includes(needle));
}

function toolIsAsk(tool: string): boolean {
  return ASK_TOOLS.has(tool.trim().toLowerCase());
}

function conversationTitle(payload: Record<string, unknown>): string | null {
  const v = first(
    payload,
    "conversation_title",
    "conversationTitle",
    "session_title",
    "sessionTitle",
    "session_summary",
    "sessionSummary",
    "custom_title",
    "customTitle",
    "ai_title",
    "aiTitle",
    "thread_title",
    "threadTitle",
    "chat_title",
    "chatTitle",
    "summary",
  );
  if (typeof v === "string" && v.trim() !== "") {
    return fieldStr(v, MAX_TEXT, false);
  }
  for (const container of ["session", "thread", "conversation", "info"]) {
    const nested = payload[container];
    if (isRecord(nested)) {
      for (const key of [
        "title",
        "name",
        "summary",
        "session_summary",
        "custom_title",
        "ai_title",
      ]) {
        const s = nested[key];
        if (typeof s === "string" && s.trim() !== "") {
          return fieldStr(s, MAX_TEXT, false);
        }
      }
    }
  }
  return null;
}

function extractSessionId(payload: Record<string, unknown>): string | null {
  const v = first(
    payload,
    "session_id",
    "sessionId",
    "sessionID",
    "thread_id",
    "threadId",
    "conversation_id",
    "conversationId",
    "id",
  );
  if (typeof v === "string" && v.trim() !== "") {
    return truncateRunes(v.trim(), MAX_SESSION);
  }
  return null;
}

function toNumber(v: unknown): number {
  if (typeof v === "number" && Number.isFinite(v)) {
    return v;
  }
  if (typeof v === "string" && v.trim() !== "") {
    const n = Number(v);
    if (Number.isFinite(n)) {
      return n;
    }
  }
  return 0;
}

function normalizeUsage(raw: unknown): HookUsage | null {
  if (!isRecord(raw)) {
    return null;
  }
  const input_tokens = Math.max(
    0,
    Math.trunc(toNumber(first(raw, "input_tokens", "inputTokens"))),
  );
  const output_tokens = Math.max(
    0,
    Math.trunc(toNumber(first(raw, "output_tokens", "outputTokens"))),
  );
  const reasoning_tokens = Math.max(
    0,
    Math.trunc(toNumber(first(raw, "reasoning_tokens", "reasoningTokens"))),
  );
  const cache_read_tokens = Math.max(
    0,
    Math.trunc(toNumber(first(raw, "cache_read_tokens", "cacheReadTokens"))),
  );
  const cache_write_tokens = Math.max(
    0,
    Math.trunc(toNumber(first(raw, "cache_write_tokens", "cacheWriteTokens"))),
  );
  const cost_usd = Math.max(0, toNumber(first(raw, "cost_usd", "costUsd", "costUSD")));

  if (
    input_tokens === 0 &&
    output_tokens === 0 &&
    reasoning_tokens === 0 &&
    cache_read_tokens === 0 &&
    cache_write_tokens === 0 &&
    cost_usd === 0
  ) {
    return null;
  }
  return {
    input_tokens,
    output_tokens,
    reasoning_tokens,
    cache_read_tokens,
    cache_write_tokens,
    cost_usd,
  };
}

/**
 * Normalizes a hook argv agent hint, mirroring `canonical.go:CanonicalAgent`.
 */
export function canonicalAgent(
  agent: string,
  env: Record<string, string | undefined> = process.env,
): string {
  const override = (env.ZJ_HOOK_AGENT ?? "").trim();
  const rawAgent = override || agent;
  if ((env.GROK_SESSION_ID ?? "") !== "" || (env.GROK_HOOK_EVENT ?? "") !== "") {
    return "grok";
  }
  let name = rawAgent.replace(/\/+$/, "");
  if (!name) {
    return "unknown";
  }
  name = path.posix.basename(name.replaceAll("\\", "/")).toLowerCase().replace(EXT_SUFFIX_RE, "");
  if (Object.hasOwn(AGENT_ALIASES, name)) {
    name = AGENT_ALIASES[name]!;
  }
  return name || "unknown";
}

/**
 * Maps an agent name to its display app label.
 */
export function displayApp(agent: string): string {
  const canon = canonicalAgent(agent, {});
  return APP_NAMES[canon] ?? canon;
}

/**
 * Maps a raw provider event name to its canonical hook event name, or `""` if unknown.
 */
export function canonicalEvent(raw: string): string {
  if (!raw) {
    return "";
  }
  const key = raw.toLowerCase().replace(NON_ALNUM_RE, "");
  return EVENT_ALIASES[key] ?? "";
}

/**
 * Maps a canonical hook event + payload to a pane state and optional kind,
 * mirroring `internal/hooks/core/events.go:EventState`.
 */
export function eventState(
  event: string,
  payload: Record<string, unknown>,
): { state: string; kind: string | null } | null {
  switch (event) {
    case "session_start":
    case "session_end":
      return { state: "idle", kind: null };
    case "prompt":
    case "pre_invocation":
      return { state: "working", kind: null };
    case "pre_tool": {
      let toolName = firstStr(payload, "tool_name", "toolName", "name", "tool");
      if (!toolName) {
        const tc = isRecord(payload.toolCall)
          ? payload.toolCall
          : isRecord(payload.tool_call)
            ? payload.tool_call
            : null;
        if (tc) {
          toolName = firstStr(tc, "name");
        }
      }
      if (toolIsAsk(toolName)) {
        return { state: "blocked", kind: "question" };
      }
      return { state: "working", kind: null };
    }
    case "post_tool":
    case "tool_failure":
    case "compact":
    case "response":
      return { state: "working", kind: null };
    case "permission":
      if (looksAuth(payload)) {
        return { state: "blocked", kind: "auth" };
      }
      return { state: "blocked", kind: "permission" };
    case "elicitation":
      return { state: "blocked", kind: "question" };
    case "elicitation_result":
      return { state: "working", kind: null };
    case "stop": {
      const term = firstStr(payload, "terminationReason", "termination_reason");
      const errMsg = firstStr(payload, "error", "error_message", "errorMessage");
      if (term.toLowerCase() === "error" || errMsg !== "") {
        return { state: "failed", kind: null };
      }
      return { state: "done", kind: null };
    }
    case "stop_failure":
      return { state: "failed", kind: null };
    case "subagent_start":
    case "subagent_stop":
    case "task_created":
    case "task_completed":
      return { state: "working", kind: null };
    case "notification": {
      const notification = firstStr(
        payload,
        "notification_type",
        "notificationType",
        "type",
        "notification",
      );
      const lowered = notification.toLowerCase();
      const message = firstStr(payload, "message", "body", "text", "title");
      const messageLower = message.toLowerCase();
      if (
        lowered.includes("permission") ||
        lowered.includes("toolpermission") ||
        lowered.includes("approval") ||
        messageLower.includes("permission") ||
        messageLower.includes("approve")
      ) {
        return { state: "blocked", kind: "permission" };
      }
      if (lowered.includes("elicitation") || lowered.includes("question")) {
        return { state: "blocked", kind: "question" };
      }
      if (lowered.includes("idle") || lowered.includes("complete")) {
        return { state: "done", kind: null };
      }
      if (looksAuth(payload) || messageLower.includes("auth")) {
        return { state: "blocked", kind: "auth" };
      }
      return null;
    }
    case "working":
      return { state: "working", kind: null };
    case "blocked":
      return { state: "blocked", kind: "permission" };
    case "done":
      return { state: "done", kind: null };
    case "error":
      return { state: "failed", kind: null };
    case "idle":
      return { state: "idle", kind: null };
    default:
      return null;
  }
}

/**
 * Canonicalizes a provider hook event into a normalized record, mirroring
 * `internal/hooks/core/events.go:NormalizeEvent`. Returns `null` for unknown or
 * stateless events.
 */
export function normalizeHookEvent(
  agent: string,
  event?: string,
  payload?: unknown,
  env: Record<string, string | undefined> = process.env,
): NormalizedHookEvent | null {
  const payloadObj: Record<string, unknown> = isRecord(payload) ? payload : {};
  const canonAgent = canonicalAgent(agent, env);
  const app = APP_NAMES[canonAgent] ?? canonAgent;

  const rawEvent =
    (event ?? "").trim() ||
    firstStr(
      payloadObj,
      "hook_event_name",
      "hookEventName",
      "hook_type",
      "hookType",
      "event",
      "type",
    );
  const canonEvent = canonicalEvent(rawEvent);
  if (!canonEvent) {
    return null;
  }
  if (
    canonAgent === "codex" &&
    (canonEvent === "subagent_start" || canonEvent === "subagent_stop") &&
    firstStr(payloadObj, "agent_id", "agentId") === ""
  ) {
    return null;
  }

  const stateInfo = eventState(canonEvent, payloadObj);
  if (!stateInfo) {
    return null;
  }
  const { state, kind } = stateInfo;

  let toolName = firstStr(payloadObj, "tool_name", "toolName", "name", "tool");
  let toolInput = first(
    payloadObj,
    "tool_input",
    "toolInput",
    "toolArgs",
    "input",
    "arguments",
    "args",
  );
  const tc = isRecord(payloadObj.toolCall)
    ? payloadObj.toolCall
    : isRecord(payloadObj.tool_call)
      ? payloadObj.tool_call
      : null;
  if (tc) {
    if (!toolName) {
      toolName = firstStr(tc, "name");
    }
    if (toolInput === undefined) {
      toolInput = first(tc, "args", "arguments", "input");
    }
  }

  const toolOutput = first(
    payloadObj,
    "tool_response",
    "toolResponse",
    "tool_output",
    "toolOutput",
    "tool_result",
    "toolResult",
    "output",
    "result",
  );
  const prompt = firstStr(
    payloadObj,
    "prompt",
    "user_prompt",
    "userPrompt",
    "lastUserInput",
    "last_user_input",
    "initial_prompt",
    "initialPrompt",
    "user_message",
    "message",
  );
  const reply = firstStr(
    payloadObj,
    "last_assistant_message",
    "lastAssistantMessage",
    "finalModelOutput",
    "final_model_output",
    "modelOutput",
    "model_output",
    "prompt_response",
    "last_message",
    "assistant_message",
    "assistant_response",
    "text",
  );
  const errText = firstStr(
    payloadObj,
    "error",
    "error_details",
    "errorDetails",
    "error_message",
    "errorMessage",
    "reason",
    "message",
  );

  const preview = buildPreview(toolName, toolInput);
  let text = preview;

  switch (canonEvent) {
    case "permission":
      if (preview) {
        text = `approve ${preview}`;
      } else {
        const e = oneLine(errText, MAX_TEXT, false);
        text = e || "needs approval";
      }
      break;
    case "elicitation": {
      const pe = prompt || errText;
      const t = oneLine(pe, MAX_TEXT, false);
      text = t ? `answer: ${t}` : "answer: needs an answer";
      break;
    }
    case "prompt":
    case "pre_invocation": {
      const t = oneLine(prompt, MAX_TEXT, false);
      text = t ? `prompt: ${t}` : "prompt: working";
      break;
    }
    case "tool_failure": {
      const ep = errText || preview;
      const t = oneLine(ep, MAX_TEXT, false);
      text = t ? `tool failed: ${t}` : "tool failed: failed";
      break;
    }
    case "response": {
      const t = oneLine(reply, MAX_TEXT, true);
      text = t || "responding";
      break;
    }
    case "stop":
      if (state === "failed") {
        const t = oneLine(errText, MAX_TEXT, false);
        text = t ? `error: ${t}` : "error: turn failed";
      } else {
        const t = oneLine(reply, MAX_TEXT, true);
        text = t || "turn complete";
      }
      break;
    case "stop_failure": {
      const t = oneLine(errText, MAX_TEXT, false);
      text = t ? `error: ${t}` : "error: turn failed";
      break;
    }
    case "session_start":
      text = "session started";
      break;
    case "session_end":
      text = "session ended";
      break;
    case "compact":
      text = "compacting context";
      break;
    case "notification": {
      const m = firstStr(payloadObj, "message", "body", "text", "title") || "notification";
      text = oneLine(m, MAX_TEXT, false);
      break;
    }
    case "subagent_start": {
      const d = firstStr(payloadObj, "description", "prompt") || toolName || "started";
      text = `subagent: ${oneLine(d, MAX_TEXT, false)}`;
      break;
    }
    case "subagent_stop": {
      const d = firstStr(payloadObj, "description", "prompt") || toolName || "finished";
      text = `subagent done: ${oneLine(d, MAX_TEXT, false)}`;
      break;
    }
    case "task_created": {
      const d = firstStr(payloadObj, "description", "command", "title", "name") || "started";
      text = `background task: ${oneLine(d, MAX_TEXT, false)}`;
      break;
    }
    case "task_completed": {
      const d = firstStr(payloadObj, "description", "command", "title", "name") || "finished";
      text = `background task done: ${oneLine(d, MAX_TEXT, false)}`;
      break;
    }
    case "idle":
      text = "idle";
      break;
  }

  if (!text) {
    text = canonEvent.replaceAll("_", " ");
  }

  let title = conversationTitle(payloadObj);
  if (!title && (canonEvent === "prompt" || canonEvent === "pre_invocation") && prompt) {
    title = fieldStr(prompt, MAX_TEXT, false);
  }

  const model = fieldStr(firstStr(payloadObj, "model", "model_name", "modelName"), MAX_MODEL, false);
  const cwd = fieldStr(
    firstStr(payloadObj, "cwd", "working_directory", "workingDirectory", "workspaceRoot"),
    MAX_CWD,
    false,
  );
  const rawProfile = firstStr(payloadObj, "profile") || (env.HARNESS_PROFILE ?? "").trim();
  const profile = rawProfile ? truncateRunes(rawProfile, MAX_MODEL) : null;
  const source = truncateRunes(firstStr(payloadObj, "source") || "hook", MAX_MODEL);
  const usage = normalizeUsage(first(payloadObj, "usage"));

  const normToolName = truncateRunes(toolName, MAX_MODEL);
  const normToolInput = fieldStr(toolText(toolInput), MAX_FIELD, false);
  const normToolOutput = fieldStr(toolOutput, MAX_OUTPUT, true);
  const tool: HookTool | null =
    normToolName || normToolInput || normToolOutput
      ? {
          name: normToolName,
          input: normToolInput ?? "",
          output: normToolOutput ?? "",
        }
      : null;

  return {
    agent: canonAgent,
    app,
    source,
    event: canonEvent,
    rawEvent: oneLine(rawEvent, MAX_MODEL, false),
    state,
    kind,
    text: oneLine(text, MAX_TEXT, false),
    title,
    prompt: fieldStr(prompt, MAX_FIELD, false),
    reply: fieldStr(reply, MAX_FIELD, true),
    model,
    profile,
    sessionId: extractSessionId(payloadObj),
    cwd,
    tool,
    usage,
  };
}

// ---------------------------------------------------------------------------
// HookIngestor
// ---------------------------------------------------------------------------

export interface HookIngestorOptions {
  now?: () => TimestampMs;
  env?: Record<string, string | undefined>;
}

function normalizePaneKey(pane?: string, paneRef?: string): string {
  const raw = (pane ?? "").trim() || (paneRef ?? "").trim();
  return raw.replace(/^%/, "");
}

function mergeUsage(existing: HookUsage | null, incoming: HookUsage | null): HookUsage | null {
  if (!incoming) {
    return existing;
  }
  if (!existing) {
    return incoming;
  }
  return {
    input_tokens: incoming.input_tokens > 0 ? incoming.input_tokens : existing.input_tokens,
    output_tokens: incoming.output_tokens > 0 ? incoming.output_tokens : existing.output_tokens,
    reasoning_tokens:
      incoming.reasoning_tokens > 0 ? incoming.reasoning_tokens : existing.reasoning_tokens,
    cache_read_tokens:
      incoming.cache_read_tokens > 0 ? incoming.cache_read_tokens : existing.cache_read_tokens,
    cache_write_tokens:
      incoming.cache_write_tokens > 0 ? incoming.cache_write_tokens : existing.cache_write_tokens,
    cost_usd: incoming.cost_usd > 0 ? incoming.cost_usd : existing.cost_usd,
  };
}

function countActiveBackground(
  children: readonly HookChild[],
  backgroundTasks: readonly HookChild[],
): { finiteActive: number; serviceActive: number; background: number } {
  const seen = new Set<string>();
  let finiteActive = 0;
  let serviceActive = 0;

  const consider = (item: HookChild, defaultCategory: string): void => {
    const id = item.id.trim();
    if (id) {
      if (seen.has(id)) {
        return;
      }
      seen.add(id);
    }
    if (!ACTIVE_BG_STATES.has(item.state.trim().toLowerCase())) {
      return;
    }
    const category = item.category.trim() || defaultCategory;
    if (category === "finite") {
      finiteActive += 1;
    } else {
      serviceActive += 1;
    }
  };

  for (const task of backgroundTasks) {
    consider(task, "finite");
  }
  for (const child of children) {
    consider(child, "finite");
  }

  let activeBg = 0;
  for (const task of backgroundTasks) {
    if (ACTIVE_BG_STATES.has(task.state.trim().toLowerCase())) {
      activeBg += 1;
    }
  }

  return { finiteActive, serviceActive, background: activeBg };
}

function hookPaneRecordToDetail(rec: HookPaneRecord): HookDetail {
  const { finiteActive, serviceActive, background } = countActiveBackground(
    rec.children,
    rec.background_tasks,
  );
  const childCount = rec.children.length;
  const hasDetail = Boolean(
    rec.prompt ||
      rec.tool?.input ||
      rec.tool?.output ||
      rec.reply ||
      rec.usage ||
      childCount > 0 ||
      rec.background_tasks.length > 0 ||
      background > 0,
  );

  return {
    prompt: rec.prompt ?? "",
    tool: rec.tool,
    reply: rec.reply ?? "",
    agent: rec.agent,
    app: rec.app,
    state: rec.state,
    kind: rec.kind ?? "",
    event: rec.event,
    source: rec.source,
    ts_ms: rec.ts_ms,
    state_since_ms: rec.state_since_ms,
    turn_started_ms: rec.turn_started_ms,
    text: rec.text,
    title: rec.title || (rec.prompt ? oneLine(rec.prompt, MAX_TEXT, false) : ""),
    model: rec.model ?? "",
    session_id: rec.session_id ?? "",
    cwd: rec.cwd ?? "",
    usage: rec.usage,
    child_count: childCount,
    background,
    finite_active: finiteActive,
    service_active: serviceActive,
    waiting_on: rec.waiting_on ?? "",
    children: rec.children,
    background_tasks: rec.background_tasks,
    has_detail: hasDetail,
  };
}

/**
 * Ingests normalized provider hook events into `SpacesStore` (`hook_panes` and
 * `outcomes`) and publishes live updates onto `EventBus`.
 */
export class HookIngestor {
  private readonly store: SpacesStore;
  private readonly eventBus: EventBus;
  private readonly now: () => TimestampMs;
  private readonly env?: Record<string, string | undefined>;

  constructor(
    store: SpacesStore,
    eventBus: EventBus,
    optionsOrNow?: HookIngestorOptions | (() => TimestampMs),
  ) {
    this.store = store;
    this.eventBus = eventBus;
    if (typeof optionsOrNow === "function") {
      this.now = optionsOrNow;
    } else {
      this.now = optionsOrNow?.now ?? (() => Date.now());
      this.env = optionsOrNow?.env;
    }
  }

  /**
   * Normalizes a hook event, upserts `hook_panes` (preserving `state_since_ms`,
   * `turn_started_ms`, and omitted metadata), records terminal outcomes (`done` /
   * `failed`), and publishes `hook_pane_updated` (and `outcome_recorded`) on `EventBus`.
   */
  emit(params: HookEmitParams): HookEmitResult {
    const tsMs = params.ts_ms ?? this.now();
    const normalized = normalizeHookEvent(
      params.agent,
      params.event,
      params.payload,
      this.env ?? {},
    );
    if (!normalized) {
      return { ok: false };
    }

    const session = (params.session ?? "").trim();
    const pane = normalizePaneKey(params.pane, params.pane_ref);
    if (!session || !pane) {
      return {
        ok: true,
        state: normalized.state,
        ...(normalized.kind ? { kind: normalized.kind } : {}),
      };
    }

    const existing = this.store.getHookPane(session, pane);
    let state = normalized.state;
    let kind = normalized.kind;
    const ev = normalized.event;

    // Auth-block stickiness: survives ordinary events until a fresh turn or session boundary.
    if (
      existing?.kind === "auth" &&
      state !== "idle" &&
      ev !== "session_start" &&
      ev !== "session_end" &&
      ev !== "prompt"
    ) {
      state = "blocked";
      kind = "auth";
    }
    if (state === "working") {
      kind = null;
    }

    const stateSinceMs =
      existing && existing.state === state && ev !== "session_start"
        ? existing.state_since_ms
        : tsMs;

    const turnStartedMs =
      ev === "prompt" ||
      ev === "session_start" ||
      (ev === "pre_invocation" && existing?.state !== "working")
        ? tsMs
        : (existing?.turn_started_ms ?? 0);

    const incomingSessionId = (params.session_id ?? "").trim() || normalized.sessionId;
    const isSessionReset =
      ev === "session_start" &&
      Boolean(existing?.session_id) &&
      Boolean(incomingSessionId) &&
      existing!.session_id !== incomingSessionId;

    const clearReply =
      ev === "prompt" ||
      ev === "session_start" ||
      ev === "session_end" ||
      (ev === "pre_invocation" && existing?.state !== "working");

    const title =
      (params.title ?? "").trim() ||
      normalized.title ||
      (isSessionReset ? "" : (existing?.title ?? ""));
    const profile =
      (params.profile ?? "").trim() || normalized.profile || existing?.profile || null;
    const sessionId = incomingSessionId || existing?.session_id || null;
    const cwd = (params.cwd ?? "").trim() || normalized.cwd || existing?.cwd || null;
    const model = normalized.model || existing?.model || null;
    const prompt = normalized.prompt ?? (isSessionReset ? null : (existing?.prompt ?? null));
    const reply = normalized.reply ?? (clearReply ? null : (existing?.reply ?? null));
    const tool = normalized.tool ?? existing?.tool ?? null;
    const usage = isSessionReset
      ? normalized.usage
      : mergeUsage(existing?.usage ?? null, normalized.usage);

    const input: HookPaneInput = {
      session,
      pane,
      agent: normalized.agent,
      app: normalized.app,
      source: normalized.source,
      state,
      kind,
      event: ev,
      ts_ms: tsMs,
      state_since_ms: stateSinceMs,
      turn_started_ms: turnStartedMs,
      text: normalized.text,
      title,
      prompt,
      tool,
      reply,
      model,
      profile,
      session_id: sessionId,
      cwd,
      usage,
      children:
        ev === "session_start" || ev === "session_end" ? [] : (existing?.children ?? []),
      background_tasks:
        ev === "session_start" || ev === "session_end" ? [] : (existing?.background_tasks ?? []),
      waiting_on:
        ev === "session_start" || ev === "session_end" || clearReply
          ? null
          : (existing?.waiting_on ?? null),
      last_event: {
        event: ev,
        agent: normalized.agent,
        ts_ms: tsMs,
      },
    };

    const saved = this.store.upsertHookPane(input);

    if (saved.state === "done" || saved.state === "failed") {
      const outcomeRes = this.store.recordOutcome({
        session: saved.session,
        pane: saved.pane,
        agent: saved.agent,
        app: saved.app,
        profile: saved.profile ?? "",
        outcome: saved.state,
        text: saved.text,
        title: saved.title,
        model: saved.model ?? "",
        session_id: saved.session_id ?? "",
        cwd: saved.cwd ?? "",
        ts_ms: tsMs,
      });
      if (outcomeRes.recorded && outcomeRes.entry) {
        this.eventBus.publish({
          payload: {
            type: "outcome_recorded",
            outcome: outcomeRes.entry,
          },
          tsMs,
        });
      }
    }

    this.eventBus.publish({
      payload: {
        type: "hook_pane_updated",
        session: saved.session,
        pane: saved.pane,
        agent: saved.agent,
        state: saved.state,
        ...(saved.kind ? { kind: saved.kind } : {}),
      },
      tsMs,
    });

    return {
      ok: true,
      state: saved.state,
      ...(saved.kind ? { kind: saved.kind } : {}),
    };
  }

  /**
   * Reads `SpacesStore.getHookPane(session, String(paneNum))` and maps it to `HookDetail`.
   */
  getDetail(session: string, paneNum: number): HookDetail | null {
    const rec = this.store.getHookPane(session, String(paneNum));
    if (!rec) {
      return null;
    }
    return hookPaneRecordToDetail(rec);
  }

  /**
   * Persists a terminal outcome via `SpacesStore` and publishes `outcome_recorded`
   * on `EventBus` when recorded.
   */
  recordOutcome(params: HookRecordOutcomeParams): RecordOutcomeResult {
    const tsMs = params.ts_ms ?? this.now();
    const res = this.store.recordOutcome({
      session: params.session,
      pane: params.pane,
      agent: params.agent,
      app: params.app,
      profile: params.profile,
      outcome: params.outcome,
      text: params.text,
      title: params.title,
      model: params.model,
      session_id: params.session_id,
      cwd: params.cwd,
      ts_ms: tsMs,
    });
    if (res.recorded && res.entry) {
      this.eventBus.publish({
        payload: {
          type: "outcome_recorded",
          outcome: res.entry,
        },
        tsMs,
      });
    }
    return res;
  }

  /**
   * Removes matching outcomes from `SpacesStore`.
   */
  removeOutcome(params: HookRemoveOutcomeParams): number {
    return this.store.removeOutcome({
      session: params.session,
      pane: params.pane,
    });
  }
}

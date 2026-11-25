// tracker.mjs — rich in-process session/subagent/tool/usage/context/file/todo/
// mcp/lsp/permission/question/compaction/error/VCS state tracker.
//
// The Tracker is pure in-memory state: it never touches the network, never
// throws on malformed input, and never mutates OpenCode objects. All OpenCode
// bus-event / hook parsing lives here so index.mjs stays a thin wiring layer.

export const PLUGIN_NAME = "opencode-spaces";
export const PLUGIN_VERSION = "1.0.0";
export const PLUGIN_CAPABILITIES = [
  "lifecycle",
  "usage",
  "context",
  "tools",
  "files",
  "todos",
  "subagents",
  "permissions",
  "questions",
  "mcp",
  "lsp",
  "compaction",
  "errors",
  "vcs",
];

export const PLUGIN_META = {
  name: PLUGIN_NAME,
  version: PLUGIN_VERSION,
  capabilities: [...PLUGIN_CAPABILITIES],
};

export const MAX_RECENT_TOOLS = 25;
export const MAX_FILES_TOUCHED = 100;
export const MAX_PREVIEW_CHARS = 2000;

export function num(v, dflt = 0) {
  const n = Number(v);
  return Number.isFinite(n) ? n : dflt;
}

export function str(v, dflt = "") {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return dflt;
  try {
    return String(v);
  } catch {
    return dflt;
  }
}

// Truncated JSON-safe preview of arbitrary tool input/output.
export function preview(value, limit = MAX_PREVIEW_CHARS) {
  let s;
  if (typeof value === "string") {
    s = value;
  } else {
    try {
      s = JSON.stringify(value) ?? "";
    } catch {
      s = "";
    }
  }
  if (s.length > limit) return s.slice(0, limit) + "…[truncated]";
  return s;
}

function emptyUsage() {
  return {
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
  };
}

function newSessionState(id) {
  return {
    session_id: id || "",
    parent_session_id: "",
    is_subagent: false,
    title: "",
    cwd: "",
    git_branch: "",
    provider: "",
    model: "",
    effort: "",
    mode: "",
    state: "working",
    kind: "",
    text: "",
    prompt: "",
    reply: "",
    tool: "",
    tool_call_id: "",
    tool_input: "",
    tool_output: "",
    usage: emptyUsage(),
    context_tokens: 0,
    context_window: 0,
    context_percent: 0,
    turn_count: 0,
    compaction_count: 0,
    last_compaction_ms: 0,
    last_compaction_reason: "",
    tool_call_count: 0,
    tool_error_count: 0,
    retry_count: 0,
    rate_limited: false,
    rate_limit_message: "",
    active_tools: {}, // callID -> { call_id, tool, state, input, start_ms, ... }
    recent_tools: [], // newest-first, capped
    files_touched: [], // [{ path, ms }], deduped, capped
    todos: [],
    mcp_servers: [], // [{ name, status, last_error }]
    lsp_diagnostics: { errors: 0, warnings: 0, files: 0 },
    pending_permission: null,
    pending_question: null,
    last_error: "",
  };
}

function pickSessionID(props, info) {
  return (
    str(props.sessionID) ||
    str(props.session_id) ||
    str(info && info.sessionID) ||
    str(info && info.session_id) ||
    str(props.messageID && undefined) ||
    ""
  );
}

function extractTextParts(message) {
  // OpenCode messages carry `parts` (or `content`) with { type: "text", text }.
  const parts = (message && (message.parts || message.content)) || [];
  if (typeof parts === "string") return parts;
  if (!Array.isArray(parts)) return "";
  const out = [];
  for (const p of parts) {
    if (!p || typeof p !== "object") continue;
    if (p.type === "text" && typeof p.text === "string") out.push(p.text);
    else if (typeof p.text === "string" && !p.type) out.push(p.text);
  }
  return out.join("\n");
}

function rateLimitLike(message) {
  return /rate.?limit|429|quota|too many requests|resource.?exhausted/i.test(
    str(message),
  );
}

function diffToFiles(diff) {
  // Accept common diff shapes: array of {file,path}, {files:[...]},
  // or unified-diff text with "+++ b/path" markers.
  if (!diff) return [];
  if (Array.isArray(diff)) {
    return diff
      .map((d) => str(d && (d.file || d.path || d.filename)))
      .filter(Boolean);
  }
  if (typeof diff === "object") {
    if (Array.isArray(diff.files)) return diffToFiles(diff.files);
    if (diff.file || diff.path) return [str(diff.file || diff.path)];
    return [];
  }
  if (typeof diff === "string") {
    const files = [];
    for (const line of diff.split("\n")) {
      const m = /^\+\+\+\s+b\/(.+?)\s*$/.exec(line.trim());
      if (m) files.push(m[1]);
    }
    return files;
  }
  return [];
}

export class Tracker {
  constructor() {
    this.sessions = new Map(); // sessionID -> state
    this.rootID = "";
    this.modelContextCache = new Map(); // "provider/model" -> context window
  }

  reset() {
    this.sessions.clear();
    this.rootID = "";
    this.modelContextCache.clear();
  }

  // Ensure a state object exists; wire up root/subagent relationships.
  // Returns the state.
  ensure(id, parentID = "") {
    if (!id) return null;
    let st = this.sessions.get(id);
    if (!st) {
      st = newSessionState(id);
      this.sessions.set(id, st);
    }
    if (parentID && parentID !== id) {
      st.parent_session_id = parentID;
      st.is_subagent = true;
      if (!this.rootID) {
        this.rootID = parentID;
        if (!this.sessions.has(parentID)) {
          this.sessions.set(parentID, newSessionState(parentID));
        }
      }
    } else if (!parentID) {
      if (!this.rootID) this.rootID = id;
    }
    return st;
  }

  stateFor(id) {
    if (id && this.sessions.has(id)) return this.sessions.get(id);
    if (this.rootID && this.sessions.has(this.rootID)) {
      return this.sessions.get(this.rootID);
    }
    return null;
  }

  // Id used when an event carries no session id (global events attach to root).
  fallbackID() {
    return this.rootID || "";
  }

  applySessionInfo(id, info = {}) {
    const st = this.ensure(id, str(info.parentID || info.parent_id));
    if (!st) return null;
    if (info.title) st.title = str(info.title);
    if (info.directory || info.cwd) st.cwd = str(info.directory || info.cwd);
    if (info.mode || info.agent) st.mode = str(info.mode || info.agent, st.mode);
    return st;
  }

  touchFile(st, filePath, ms) {
    const p = str(filePath);
    if (!p) return;
    st.files_touched = st.files_touched.filter((f) => f.path !== p);
    st.files_touched.push({ path: p, ms: ms || Date.now() });
    if (st.files_touched.length > MAX_FILES_TOUCHED) {
      st.files_touched = st.files_touched.slice(-MAX_FILES_TOUCHED);
    }
  }

  upsertMcp(st, name, status, lastError = "") {
    const n = str(name);
    if (!n) return;
    const existing = st.mcp_servers.find((m) => m.name === n);
    if (existing) {
      existing.status = status;
      if (lastError) existing.last_error = str(lastError);
    } else {
      st.mcp_servers.push({ name: n, status, last_error: str(lastError) });
    }
  }

  finishActiveTool(st, callID, outputPreview, isError, endMs) {
    const key = str(callID);
    const active = (key && st.active_tools[key]) || null;
    const base = active || {
      call_id: key,
      tool: st.tool || "",
      state: "running",
      input: "",
      start_ms: endMs,
    };
    const durationMs = Math.max(0, endMs - num(base.start_ms, endMs));
    const record = {
      call_id: base.call_id || key,
      tool: base.tool || st.tool || "",
      state: isError ? "error" : "completed",
      input: base.input || "",
      output: outputPreview || "",
      start_ms: num(base.start_ms, endMs),
      end_ms: endMs,
      duration_ms: durationMs,
    };
    if (key) delete st.active_tools[key];
    st.recent_tools.unshift(record);
    if (st.recent_tools.length > MAX_RECENT_TOOLS) {
      st.recent_tools.length = MAX_RECENT_TOOLS;
    }
    st.tool_call_count += 1;
    if (isError) st.tool_error_count += 1;
    st.tool = record.tool;
    st.tool_call_id = record.call_id;
    st.tool_output = record.output;
    if (record.input) st.tool_input = record.input;
    return record;
  }

  subagentSummary() {
    const out = [];
    for (const [id, st] of this.sessions) {
      if (id !== this.rootID && st.is_subagent) {
        out.push({
          session_id: id,
          parent_session_id: st.parent_session_id,
          title: st.title,
          state: st.state,
          tool_call_count: st.tool_call_count,
          last_error: st.last_error,
        });
      }
    }
    return out;
  }

  snapshot(id) {
    const st = this.stateFor(id);
    const base = st || newSessionState(str(id));
    const contextWindow = num(base.context_window);
    const contextPercent =
      contextWindow > 0
        ? Math.min(
            100,
            Math.round((num(base.context_tokens) / contextWindow) * 100),
          )
        : 0;
    const payload = {
      session_id: base.session_id,
      parent_session_id: base.parent_session_id,
      is_subagent: base.is_subagent,
      title: base.title,
      cwd: base.cwd,
      git_branch: base.git_branch,
      provider: base.provider,
      model: base.model,
      effort: base.effort,
      mode: base.mode,
      state: base.state,
      kind: base.kind,
      text: base.text,
      prompt: base.prompt,
      reply: base.reply,
      tool: base.tool,
      tool_call_id: base.tool_call_id,
      tool_input: base.tool_input,
      tool_output: base.tool_output,
      usage: { ...base.usage },
      context_tokens: num(base.context_tokens),
      context_window: contextWindow,
      context_percent: contextPercent,
      turn_count: num(base.turn_count),
      compaction_count: num(base.compaction_count),
      last_compaction_ms: num(base.last_compaction_ms),
      last_compaction_reason: str(base.last_compaction_reason),
      tool_call_count: num(base.tool_call_count),
      tool_error_count: num(base.tool_error_count),
      retry_count: num(base.retry_count),
      rate_limited: Boolean(base.rate_limited),
      rate_limit_message: str(base.rate_limit_message),
      active_tools: Object.values(base.active_tools || {}),
      recent_tools: [...(base.recent_tools || [])],
      files_touched: [...(base.files_touched || [])],
      todos: [...(base.todos || [])],
      mcp_servers: [...(base.mcp_servers || [])],
      lsp_diagnostics: { ...base.lsp_diagnostics },
      pending_permission: base.pending_permission
        ? { ...base.pending_permission }
        : null,
      pending_question: base.pending_question
        ? { ...base.pending_question }
        : null,
      subagents:
        base.is_subagent || (id && id !== this.rootID)
          ? []
          : this.subagentSummary(),
      last_error: str(base.last_error),
      raw_event: {},
    };
    return payload;
  }

  // Cache a model context window, e.g. from client.provider.list().
  noteModelContext(providerID, modelID, contextWindow) {
    const key = `${str(providerID)}/${str(modelID)}`;
    const n = num(contextWindow, 0);
    if (key !== "/" && n > 0) this.modelContextCache.set(key, n);
  }

  lookupContextWindow(providerID, modelID) {
    return (
      this.modelContextCache.get(`${str(providerID)}/${str(modelID)}`) || 0
    );
  }

  applyContextWindow(st) {
    if (!st.context_window) {
      const cached = this.lookupContextWindow(st.provider, st.model);
      if (cached > 0) st.context_window = cached;
    }
  }

  // ---- OpenCode bus events -------------------------------------------------
  // Returns { sessionID, throttleText }. Never throws.
  applyBusEvent(type, props = {}) {
    try {
      const t = str(type);
      if (!t) return { sessionID: "", throttleText: false };
      const p = props && typeof props === "object" ? props : {};
      switch (t) {
        case "session.created":
        case "session.updated": {
          const info = p.info || p.session || p;
          const id = str(info.id || info.sessionID || info.session_id) || pickSessionID(p, info);
          const st = this.applySessionInfo(
            id || this.fallbackID() || "unknown",
            info,
          );
          return { sessionID: st ? st.session_id : "", throttleText: false };
        }
        case "session.deleted": {
          const id = pickSessionID(p, p.info) || this.fallbackID();
          const st = this.stateFor(id);
          if (st && st.state !== "failed") st.state = "done";
          return { sessionID: id, throttleText: false };
        }
        case "session.status": {
          const id = pickSessionID(p, p.status) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const status = p.status && typeof p.status === "object" ? p.status : p;
          const kind = str(status.type || status.status).toLowerCase();
          const message = str(status.message || status.text);
          if (kind === "retry") {
            st.state = "blocked";
            const attempt = num(status.attempt, NaN);
            st.retry_count = Number.isFinite(attempt)
              ? Math.max(st.retry_count, attempt)
              : st.retry_count + 1;
            if (rateLimitLike(message) || rateLimitLike(status.error)) {
              st.rate_limited = true;
              st.rate_limit_message = message || str(status.error);
            } else if (message) {
              st.last_error = message;
            }
          } else if (kind === "busy") {
            st.state = "working";
            if (!st.pending_permission && !st.pending_question) {
              st.rate_limited = false;
            }
          } else if (kind === "idle") {
            st.state =
              st.pending_permission || st.pending_question ? "blocked" : "idle";
            st.rate_limited = false;
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "session.idle": {
          const id = pickSessionID(p, p.info) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          st.state =
            st.pending_permission || st.pending_question ? "blocked" : "idle";
          st.rate_limited = false;
          return { sessionID: st.session_id, throttleText: false };
        }
        case "session.error": {
          const id = pickSessionID(p, p.info) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const err = p.error ?? p.message ?? p.detail;
          st.last_error =
            err && typeof err === "object"
              ? str(err.message || err.text || err.name)
              : str(err);
          if (!st.last_error) st.last_error = "session error";
          st.state = "failed";
          return { sessionID: st.session_id, throttleText: false };
        }
        case "session.compacted": {
          const id = pickSessionID(p, p.info) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          st.compaction_count += 1;
          st.last_compaction_ms = num(p.timestamp || p.ms, Date.now());
          st.last_compaction_reason = str(p.reason || p.cause);
          return { sessionID: st.session_id, throttleText: false };
        }
        case "session.diff": {
          const id = pickSessionID(p, p.info) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const ms = Date.now();
          const files = [
            ...diffToFiles(p.diff),
            ...diffToFiles(p.files),
            ...diffToFiles(p.paths),
            ...diffToFiles(p.filepaths),
          ];
          if (p.file || p.path || p.filename) {
            files.push(str(p.file || p.path || p.filename));
          }
          for (const f of files) this.touchFile(st, f, ms);
          return { sessionID: st.session_id, throttleText: false };
        }
        case "message.updated": {
          const info = p.info || p.message || p;
          const id =
            str(info.sessionID || info.session_id) ||
            pickSessionID(p, info) ||
            this.fallbackID();
          const st = this.ensure(id || "unknown");
          const role = str(info.role).toLowerCase();
          const text = extractTextParts(info) || str(info.text);
          if (role === "user") {
            if (text) st.prompt = text;
            st.state = "working";
          } else {
            // Assistant (or system/tool-role) message: model, tokens, cost.
            const model = info.model && typeof info.model === "object" ? info.model : {};
            const providerID = str(
              model.providerID || model.provider || info.providerID || info.provider,
            );
            const modelID = str(
              model.modelID || model.model || model.id || info.modelID || info.model,
            );
            if (providerID) st.provider = providerID;
            if (modelID) st.model = modelID;
            const variant = str(
              model.variant || model.effort || info.variant || info.effort,
            );
            if (variant) st.effort = variant;
            const tokens =
              info.tokens && typeof info.tokens === "object" ? info.tokens : {};
            const cache =
              tokens.cache && typeof tokens.cache === "object" ? tokens.cache : {};
            const hasTokens =
              tokens.input !== undefined ||
              tokens.output !== undefined ||
              info.cost !== undefined;
            if (hasTokens) {
              st.usage = {
                input_tokens: num(tokens.input, st.usage.input_tokens),
                output_tokens: num(tokens.output, st.usage.output_tokens),
                reasoning_tokens: num(tokens.reasoning, st.usage.reasoning_tokens),
                cache_read_tokens: num(
                  cache.read ?? cache.readTokens,
                  st.usage.cache_read_tokens,
                ),
                cache_write_tokens: num(
                  cache.write ?? cache.writeTokens,
                  st.usage.cache_write_tokens,
                ),
                cost_usd: num(info.cost, st.usage.cost_usd),
              };
              if (tokens.input !== undefined) {
                st.context_tokens = num(tokens.input);
              } else if (info.contextTokens !== undefined) {
                st.context_tokens = num(info.contextTokens);
              }
              this.applyContextWindow(st);
            }
            const finish = str(info.finish || info.stopReason);
            const err = info.error;
            if (err) {
              st.last_error =
                typeof err === "object"
                  ? str(err.message || err.text)
                  : str(err);
              st.state = "failed";
            } else if (finish && /error|abort/i.test(finish)) {
              st.last_error = finish;
              st.state = "failed";
            } else if (!st.pending_permission && !st.pending_question) {
              st.state = "working";
            }
            if (text) {
              st.reply = text;
              st.text = text;
            }
            st.turn_count += 1;
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "message.part.updated": {
          const part = p.part || p.info || p;
          const id =
            str(p.sessionID || p.session_id || part.sessionID) ||
            this.fallbackID();
          const st = this.ensure(id || "unknown");
          const ptype = str(part.type).toLowerCase();
          if (ptype === "tool") {
            const callID = str(
              part.callID || part.call_id || part.id || p.callID,
            );
            const toolName = str(part.tool || part.name);
            const pstate = str(part.state || part.status).toLowerCase();
            const detail =
              part.state && typeof part.state === "object" ? part.state : {};
            const inputPreview = preview(
              part.input ?? detail.input ?? part.props ?? part.args,
            );
            const outputPreview = preview(
              part.output ?? detail.output ?? part.result,
            );
            if (pstate === "completed" || pstate === "error" || part.end || part.time?.end) {
              const key = callID || `${toolName}-${Date.now()}`;
              st.active_tools[key] = st.active_tools[key] || {
                call_id: key,
                tool: toolName,
                state: "running",
                input: inputPreview,
                start_ms: num(part.start || part.time?.start, Date.now()),
              };
              this.finishActiveTool(
                st,
                key,
                outputPreview,
                pstate === "error" || Boolean(part.isError || part.error),
                Date.now(),
              );
            } else {
              const key = callID || `${toolName}-${Date.now()}`;
              st.active_tools[key] = {
                call_id: key,
                tool: toolName || st.tool,
                state: pstate === "pending" ? "pending" : "running",
                input: inputPreview,
                start_ms: num(part.start || part.time?.start, Date.now()),
              };
              if (toolName) st.tool = toolName;
              if (key) st.tool_call_id = key;
              if (inputPreview) st.tool_input = inputPreview;
            }
            return { sessionID: st.session_id, throttleText: false };
          }
          if (ptype === "step-finish" || ptype === "step_finish" || ptype === "finish") {
            const tokens = part.tokens || {};
            const cache = tokens.cache || {};
            const merge = (a, b) => Math.max(num(a), num(b));
            st.usage = {
              input_tokens: merge(st.usage.input_tokens, tokens.input),
              output_tokens: merge(st.usage.output_tokens, tokens.output),
              reasoning_tokens: merge(st.usage.reasoning_tokens, tokens.reasoning),
              cache_read_tokens: merge(st.usage.cache_read_tokens, cache.read),
              cache_write_tokens: merge(st.usage.cache_write_tokens, cache.write),
              cost_usd: merge(st.usage.cost_usd, part.cost),
            };
            this.applyContextWindow(st);
            return { sessionID: st.session_id, throttleText: false };
          }
          if (ptype === "compaction") {
            st.compaction_count += 1;
            st.last_compaction_ms = Date.now();
            if (part.reason) st.last_compaction_reason = str(part.reason);
            return { sessionID: st.session_id, throttleText: false };
          }
          if (ptype === "subtask") {
            const childID = str(part.sessionID || part.session_id || part.id);
            if (childID && childID !== st.session_id) {
              this.ensure(childID, st.session_id);
            }
            return { sessionID: st.session_id, throttleText: false };
          }
          if (ptype === "text" || ptype === "reasoning") {
            const text = str(part.text);
            if (text) {
              st.text = text;
              if (ptype === "text") st.reply = text;
            }
            // High-frequency streaming deltas: caller should coalesce these.
            return { sessionID: st.session_id, throttleText: true };
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "permission.asked": {
          const id = pickSessionID(p, p.permission) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const perm = (p.permission && typeof p.permission === "object" ? p.permission : p) || {};
          st.pending_permission = {
            id: str(p.id || p.permissionID || perm.id),
            tool: str(perm.tool || perm.name || p.tool),
            permission: str(perm.permission || perm.type),
            title: str(perm.title),
            patterns: Array.isArray(perm.patterns) ? perm.patterns.map(String) : [],
            metadata: perm.metadata && typeof perm.metadata === "object" ? perm.metadata : {},
          };
          st.state = "blocked";
          st.kind = "permission";
          return { sessionID: st.session_id, throttleText: false };
        }
        case "permission.updated": {
          const id = pickSessionID(p, p.permission) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          if (st.pending_permission) {
            const perm = p.permission || {};
            if (perm.title) st.pending_permission.title = str(perm.title);
            if (perm.patterns) st.pending_permission.patterns = perm.patterns.map(String);
            if (p.status) st.pending_permission.status = str(p.status);
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "permission.replied": {
          const id = pickSessionID(p, p.permission) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const reply = p.reply ?? p.approved ?? p.decision;
          st.reply =
            typeof reply === "string"
              ? reply
              : preview(reply);
          st.pending_permission = null;
          st.kind = "";
          st.state = "working";
          return { sessionID: st.session_id, throttleText: false };
        }
        case "question.asked": {
          const id = pickSessionID(p, p.question) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const q = (p.question && typeof p.question === "object" ? p.question : p) || {};
          const questions = Array.isArray(q.questions)
            ? q.questions
            : q.text
              ? [q]
              : [];
          st.pending_question = {
            id: str(p.id || q.id),
            text: str(q.text || q.question || q.prompt),
            questions: questions.map((item) =>
              typeof item === "string"
                ? { text: item, options: [] }
                : {
                    text: str(item.text || item.question),
                    header: str(item.header),
                    options: Array.isArray(item.options)
                      ? item.options.map((o) =>
                          typeof o === "string" ? { label: o } : o,
                        )
                      : [],
                  },
            ),
          };
          st.text = st.pending_question.text;
          st.state = "blocked";
          st.kind = "question";
          return { sessionID: st.session_id, throttleText: false };
        }
        case "question.replied":
        case "question.rejected": {
          const id = pickSessionID(p, p.question) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const reply = p.reply ?? p.answers ?? p.decision;
          st.reply =
            typeof reply === "string" ? reply : preview(reply);
          st.pending_question = null;
          st.kind = "";
          st.state = "working";
          return { sessionID: st.session_id, throttleText: false };
        }
        case "todo.updated": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const todos = p.todos || p.items || [];
          if (Array.isArray(todos)) {
            st.todos = todos.map((t, i) =>
              typeof t === "string"
                ? { id: String(i), content: t, status: "", priority: "" }
                : {
                    id: str(t.id ?? i),
                    content: str(t.content || t.text || t.title),
                    status: str(t.status),
                    priority: str(t.priority),
                  },
            );
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "file.edited": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          this.touchFile(
            st,
            p.file || p.path || p.filename || p.filenamePath,
            num(p.timestamp || p.ms, Date.now()),
          );
          return { sessionID: st.session_id, throttleText: false };
        }
        case "lsp.client.diagnostics":
        case "lsp.updated":
        case "lsp.diagnostics": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const diags = p.diagnostics;
          if (Array.isArray(diags)) {
            let errors = 0;
            let warnings = 0;
            const files = new Set(st.lsp_diagnostics.files > 0 ? ["*"] : []);
            for (const d of diags) {
              const sev = num(d && d.severity, 0);
              if (sev === 1) errors += 1;
              else if (sev === 2) warnings += 1;
            }
            if (p.uri || p.file) files.add(str(p.uri || p.file));
            st.lsp_diagnostics = {
              errors,
              warnings,
              files: files.size,
            };
          } else {
            st.lsp_diagnostics = {
              errors: num(p.errors, st.lsp_diagnostics.errors),
              warnings: num(p.warnings, st.lsp_diagnostics.warnings),
              files: num(p.files ?? p.fileCount, st.lsp_diagnostics.files),
            };
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "mcp.tools.changed": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const servers = p.servers || p.tools;
          if (Array.isArray(servers)) {
            for (const s of servers) {
              this.upsertMcp(
                st,
                typeof s === "string" ? s : s.server || s.name || s.id,
                str((s && s.status) || "ok", "ok"),
              );
            }
          } else {
            this.upsertMcp(st, p.server || p.serverID || p.name, "ok");
          }
          return { sessionID: st.session_id, throttleText: false };
        }
        case "mcp.browser.open.failed":
        case "mcp.failed":
        case "mcp.error": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          this.upsertMcp(
            st,
            p.server || p.serverID || p.name || "unknown",
            "error",
            p.error || p.message,
          );
          st.last_error = str(p.error || p.message || st.last_error);
          return { sessionID: st.session_id, throttleText: false };
        }
        case "vcs.branch.updated":
        case "vcs.branch":
        case "branch.updated": {
          const id = pickSessionID(p, null) || this.fallbackID();
          const st = this.ensure(id || "unknown");
          const branch = str(p.branch || p.name);
          if (branch) st.git_branch = branch;
          return { sessionID: st.session_id, throttleText: false };
        }
        default: {
          // Unknown/future event: attach to its session (or root) so the raw
          // event still reaches spacecore without disturbing tracked state.
          const id = pickSessionID(p, p.info) || this.fallbackID();
          if (id) this.ensure(id);
          return { sessionID: id, throttleText: false };
        }
      }
    } catch {
      return { sessionID: "", throttleText: false };
    }
  }

  // ---- Direct interception hooks (observe-only) -----------------------------

  // "tool.execute.before": returns affected sessionID.
  toolBefore(input = {}) {
    try {
      const src = input && typeof input === "object" ? input : {};
      const id = str(src.sessionID || src.session_id) || this.fallbackID();
      const st = this.ensure(id || "unknown");
      const toolName = str(src.tool || src.name);
      const callID = str(src.callID || src.call_id || src.id || src.toolCallId);
      const inputPreview = preview(
        src.input ?? src.props ?? src.args ?? src.arguments,
      );
      const key = callID || `${toolName || "tool"}-${Date.now()}`;
      st.active_tools[key] = {
        call_id: key,
        tool: toolName,
        state: "pending",
        input: inputPreview,
        start_ms: Date.now(),
      };
      if (toolName) st.tool = toolName;
      st.tool_call_id = key;
      if (inputPreview) st.tool_input = inputPreview;
      if (st.state === "idle") st.state = "working";
      return st.session_id;
    } catch {
      return "";
    }
  }

  // "tool.execute.after": returns affected sessionID.
  toolAfter(input = {}, output = {}) {
    try {
      const src = input && typeof input === "object" ? input : {};
      const out = output && typeof output === "object" ? output : {};
      const id = str(src.sessionID || src.session_id) || this.fallbackID();
      const st = this.ensure(id || "unknown");
      const endMs = Date.now();
      const callID = str(
        src.callID || src.call_id || src.id || src.toolCallId || out.callID,
      );
      const keys = Object.keys(st.active_tools);
      const key =
        (callID && st.active_tools[callID] ? callID : "") ||
        (st.tool_call_id && st.active_tools[st.tool_call_id]
          ? st.tool_call_id
          : "") ||
        keys[keys.length - 1] ||
        callID ||
        `tool-${endMs}`;
      const isError = Boolean(
        out.error || out.isError || out.is_error || out.failed,
      );
      const outputPreview = preview(
        out.output ?? out.result ?? out.data ?? (isError ? out.error : ""),
      );
      this.finishActiveTool(st, key, outputPreview, isError, endMs);
      if (!st.pending_permission && !st.pending_question && st.state !== "failed") {
        st.state = "working";
      }
      return st.session_id;
    } catch {
      return "";
    }
  }

  // "chat.params": capture exact model/provider/variant/context limit.
  chatParams(input = {}) {
    try {
      const src = input && typeof input === "object" ? input : {};
      const id = str(src.sessionID || src.session_id) || this.fallbackID();
      const st = this.ensure(id || "unknown");
      const model =
        src.model && typeof src.model === "object" ? src.model : {};
      const providerID = str(
        model.providerID || model.provider || src.providerID || src.provider,
      );
      const modelID = str(
        model.modelID || model.model || model.id || src.modelID || src.model,
      );
      if (providerID) st.provider = providerID;
      if (modelID) st.model = modelID;
      const variant = str(
        model.variant || model.effort || src.variant || src.effort,
      );
      if (variant) st.effort = variant;
      const agent = str(src.agent || src.mode);
      if (agent) st.mode = agent;
      const limit =
        (model.limit && typeof model.limit === "object" ? model.limit : null) ||
        (model.limits && typeof model.limits === "object" ? model.limits : null);
      const ctxWindow = num(
        (limit && (limit.context ?? limit.contextWindow)) ??
          model.context ??
          model.contextWindow ??
          src.contextWindow ??
          0,
      );
      if (ctxWindow > 0) st.context_window = ctxWindow;
      this.applyContextWindow(st);
      return st.session_id;
    } catch {
      return "";
    }
  }

  // "experimental.session.compacting": track compaction start/reason.
  compacting(input = {}) {
    try {
      const src = input && typeof input === "object" ? input : {};
      const id = str(src.sessionID || src.session_id) || this.fallbackID();
      const st = this.ensure(id || "unknown");
      const reason = str(src.reason || src.cause || src.trigger);
      if (reason) st.last_compaction_reason = reason;
      if (!st.last_compaction_ms) st.last_compaction_ms = Date.now();
      return st.session_id;
    } catch {
      return "";
    }
  }
}

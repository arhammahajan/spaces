// tracker.ts — rich in-process state tracker for Pi sessions.
//
// Accumulates everything the Pi ExtensionAPI exposes across the process
// lifetime: session identity, turns, token/cost usage, live context window,
// compaction history, thinking level, branch/tree, files touched, tool
// activity and errors. Pure state machine: no I/O, never throws.

export interface UsageTotals {
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
}

export interface ActiveTool {
  tool: string;
  tool_call_id: string;
  tool_input: string;
  started_ms: number;
}

export interface RecentTool {
  tool: string;
  tool_call_id: string;
  duration_ms: number;
  ok: boolean;
  output_preview: string;
}

export interface FileTouch {
  path: string;
  action: string;
}

export type AgentState = "working" | "blocked" | "done" | "failed" | "idle";

function toNum(value: unknown): number {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() !== "") {
    const n = Number(value);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function toStr(value: unknown, max = 4000): string {
  if (value === null || value === undefined) return "";
  let s: string;
  if (typeof value === "string") s = value;
  else {
    try {
      s = JSON.stringify(value) ?? "";
    } catch {
      s = String(value);
    }
  }
  if (s.length > max) s = s.slice(0, max);
  return s;
}

function pickFirst(obj: Record<string, unknown>, keys: string[]): unknown {
  for (const k of keys) {
    const v = obj[k];
    if (v !== undefined && v !== null && v !== "") return v;
  }
  return undefined;
}

const ASK_TOOL_HINTS = ["ask_user", "ask_question", "question", "ask", "confirm", "prompt_user"];
const PERMISSION_HINTS = ["permission", "approve", "consent"];

function toolKind(tool: string): string {
  const t = tool.toLowerCase();
  if (ASK_TOOL_HINTS.some((h) => t.includes(h))) return "question";
  if (PERMISSION_HINTS.some((h) => t.includes(h))) return "permission";
  return "";
}

const FILE_PATH_KEYS = ["path", "file", "filePath", "filepath", "filename", "file_name", "target", "absolute_path"];

function extractFilePath(tool: string, input: unknown): string {
  if (input === null || input === undefined) return "";
  if (typeof input === "string") {
    const s = input.trim();
    // Bare path string for read/write/edit tools.
    if (s !== "" && s.length < 1024 && !s.startsWith("{")) return s;
    try {
      const parsed: unknown = JSON.parse(s);
      return extractFilePath(tool, parsed);
    } catch {
      return "";
    }
  }
  if (typeof input === "object") {
    const obj = input as Record<string, unknown>;
    for (const k of FILE_PATH_KEYS) {
      const v = obj[k];
      if (typeof v === "string" && v.trim() !== "") return v.trim();
    }
    // Nested shapes: { params: { path } }, { args: {...} }
    for (const k of ["params", "args", "input", "arguments"]) {
      const nested = obj[k];
      if (nested && typeof nested === "object") {
        const found = extractFilePath(tool, nested);
        if (found) return found;
      }
      if (typeof nested === "string" && nested.trim() !== "") {
        const found = extractFilePath(tool, nested);
        if (found) return found;
      }
    }
  }
  return "";
}

function isWriteTool(tool: string): boolean {
  const t = tool.toLowerCase();
  return (
    t.includes("write") ||
    t.includes("edit") ||
    t.includes("apply") ||
    t.includes("create") ||
    t.includes("save") ||
    t === "w"
  );
}

export class Tracker {
  sessionId = "";
  title = "";
  cwd = "";
  provider = "";
  model = "";
  effort = "";
  mode = "";
  state: AgentState = "idle";
  kind = "";
  text = "";
  prompt = "";
  reply = "";
  tool = "";
  toolCallId = "";
  toolInput = "";
  toolOutput = "";
  usage: UsageTotals = {
    input_tokens: 0,
    output_tokens: 0,
    reasoning_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
  };
  contextTokens = 0;
  contextWindow = 0;
  contextPercent = 0;
  turnCount = 0;
  compactionCount = 0;
  lastCompactionMs = 0;
  lastCompactionReason = "";
  toolCallCount = 0;
  toolErrorCount = 0;
  thinkingLevel = "";
  activeTools: Map<string, ActiveTool> = new Map();
  recentTools: RecentTool[] = [];
  filesTouched: FileTouch[] = [];
  lastError = "";
  extras: Record<string, unknown> = {};

  private seenFiles = new Set<string>();

  reset(): void {
    const fresh = new Tracker();
    Object.assign(this, fresh);
    this.activeTools = new Map();
    this.seenFiles = new Set<string>();
  }

  // -- session -----------------------------------------------------------

  setSession(info: {
    sessionId?: unknown;
    title?: unknown;
    cwd?: unknown;
    provider?: unknown;
    model?: unknown;
    effort?: unknown;
    mode?: unknown;
  }): void {
    if (info.sessionId !== undefined) {
      const v = toStr(info.sessionId, 200);
      if (v) this.sessionId = v;
    }
    if (info.title !== undefined) {
      const v = toStr(info.title, 400);
      if (v) this.title = v;
    }
    if (info.cwd !== undefined) {
      const v = toStr(info.cwd, 1000);
      if (v) this.cwd = v;
    }
    if (info.provider !== undefined) {
      const v = toStr(info.provider, 200);
      if (v) this.provider = v;
    }
    if (info.model !== undefined) {
      const v = toStr(info.model, 200);
      if (v) this.model = v;
    }
    if (info.effort !== undefined) {
      const v = toStr(info.effort, 100);
      if (v) this.effort = v;
    }
    if (info.mode !== undefined) {
      const v = toStr(info.mode, 100);
      if (v) this.mode = v;
    }
  }

  seedTitleFromPrompt(prompt: string): void {
    const p = prompt.trim().replace(/\s+/g, " ");
    if (!p) return;
    if (this.title) return;
    this.title = p.length > 80 ? p.slice(0, 80) : p;
  }

  /** Reconstruct cumulative totals from a session branch entry list. */
  reconstructFromBranch(entries: unknown): void {
    if (!Array.isArray(entries)) return;
    let turns = 0;
    let compactions = 0;
    for (const entry of entries) {
      if (!entry || typeof entry !== "object") continue;
      const e = entry as Record<string, unknown>;
      const type = String(e["type"] ?? e["role"] ?? "").toLowerCase();
      if (type.includes("compact")) {
        compactions += 1;
        continue;
      }
      const usage = (e["usage"] ?? e["tokens"] ?? null) as unknown;
      if (usage && typeof usage === "object") {
        const u = usage as Record<string, unknown>;
        this.usage.input_tokens += toNum(pickFirst(u, ["input", "input_tokens", "prompt_tokens"]));
        this.usage.output_tokens += toNum(pickFirst(u, ["output", "output_tokens", "completion_tokens"]));
        this.usage.reasoning_tokens += toNum(pickFirst(u, ["reasoning", "reasoning_tokens"]));
        this.usage.cache_read_tokens += toNum(pickFirst(u, ["cacheRead", "cache_read", "cache_read_tokens"]));
        this.usage.cache_write_tokens += toNum(pickFirst(u, ["cacheWrite", "cache_write", "cache_write_tokens"]));
        const cost = pickFirst(u, ["cost", "total", "cost_usd"]);
        if (cost !== undefined) {
          if (typeof cost === "object" && cost !== null) {
            this.usage.cost_usd += toNum((cost as Record<string, unknown>)["total"] ?? (cost as Record<string, unknown>)["cost"]);
          } else {
            this.usage.cost_usd += toNum(cost);
          }
        }
      }
      if (type.includes("assistant") || type.includes("turn") || e["assistant"] !== undefined) {
        turns += 1;
      }
      const title = e["title"] ?? e["summary"];
      if (!this.title && typeof title === "string" && title.trim() !== "") {
        this.title = title.trim().slice(0, 400);
      }
    }
    if (turns > this.turnCount) this.turnCount = turns;
    if (compactions > this.compactionCount) this.compactionCount = compactions;
  }

  // -- lifecycle ---------------------------------------------------------

  onInput(prompt: string): void {
    this.prompt = toStr(prompt, 4000);
    this.seedTitleFromPrompt(this.prompt);
    this.state = "working";
    this.kind = "";
    this.text = this.prompt.slice(0, 240);
  }

  onAgentStart(): void {
    this.state = "working";
    this.kind = "";
  }

  onAgentEnd(data?: Record<string, unknown>): void {
    const d = data ?? {};
    const err = pickFirst(d, ["error", "err", "last_error", "failure"]);
    const reply = pickFirst(d, ["reply", "response", "text", "output", "final_message"]);
    const stopReason = toStr(pickFirst(d, ["stop_reason", "stopReason", "finish_reason"]), 100).toLowerCase();
    if (reply !== undefined) this.reply = toStr(reply, 4000);
    if (err !== undefined && toStr(err) !== "") {
      this.lastError = toStr(err, 2000);
      this.state = "failed";
      this.text = this.lastError.slice(0, 240);
      return;
    }
    if (stopReason.includes("error") || stopReason.includes("fail") || stopReason.includes("abort")) {
      this.lastError = toStr(pickFirst(d, ["stop_reason", "message"]) ?? stopReason, 2000);
      this.state = "failed";
      this.text = this.lastError.slice(0, 240);
      return;
    }
    this.state = "done";
  }

  onTurnEnd(ctxUsage?: { tokens?: unknown; window?: unknown; percent?: unknown }): void {
    this.turnCount += 1;
    if (ctxUsage) this.applyContext(ctxUsage.tokens, ctxUsage.window, ctxUsage.percent);
    if (this.state !== "failed" && this.state !== "blocked") {
      // Keep working until agent_end; a finished turn with no active tools
      // stays working so the pane doesn't flicker to idle mid-run.
      if (this.state === "idle") this.state = "working";
    }
  }

  applyContext(tokens: unknown, window: unknown, percent: unknown): void {
    const t = toNum(tokens);
    const w = toNum(window);
    let p = toNum(percent);
    if (t) this.contextTokens = t;
    if (w) this.contextWindow = w;
    if (t && w && !p) p = Math.min(100, (t / w) * 100);
    if (p) this.contextPercent = p;
  }

  addUsage(partial: Partial<UsageTotals>): void {
    this.usage.input_tokens += partial.input_tokens ?? 0;
    this.usage.output_tokens += partial.output_tokens ?? 0;
    this.usage.reasoning_tokens += partial.reasoning_tokens ?? 0;
    this.usage.cache_read_tokens += partial.cache_read_tokens ?? 0;
    this.usage.cache_write_tokens += partial.cache_write_tokens ?? 0;
    this.usage.cost_usd += partial.cost_usd ?? 0;
  }

  /** Extract cumulative-or-delta usage from the many shapes Pi/LLMs emit. */
  extractUsage(data: Record<string, unknown>): Partial<UsageTotals> {
    const out: Partial<UsageTotals> = {};
    const root = data["usage"] ?? data["tokens"] ?? data;
    if (!root || typeof root !== "object") return out;
    const u = root as Record<string, unknown>;
    const input = toNum(pickFirst(u, ["input", "input_tokens", "prompt_tokens", "promptTokens"]));
    const output = toNum(pickFirst(u, ["output", "output_tokens", "completion_tokens", "completionTokens"]));
    const reasoning = toNum(pickFirst(u, ["reasoning", "reasoning_tokens", "reasoningTokens"]));
    const cacheRead = toNum(pickFirst(u, ["cacheRead", "cache_read", "cache_read_tokens", "cached_tokens"]));
    const cacheWrite = toNum(pickFirst(u, ["cacheWrite", "cache_write", "cache_write_tokens"]));
    let cost = 0;
    const costRaw = pickFirst(u, ["cost", "cost_usd", "total_cost"]);
    if (costRaw !== undefined) {
      if (typeof costRaw === "object" && costRaw !== null) {
        const c = costRaw as Record<string, unknown>;
        cost = toNum(pickFirst(c, ["total", "cost", "usd", "amount"]));
      } else {
        cost = toNum(costRaw);
      }
    }
    if (input) out.input_tokens = input;
    if (output) out.output_tokens = output;
    if (reasoning) out.reasoning_tokens = reasoning;
    if (cacheRead) out.cache_read_tokens = cacheRead;
    if (cacheWrite) out.cache_write_tokens = cacheWrite;
    if (cost) out.cost_usd = cost;
    return out;
  }

  // -- tools -------------------------------------------------------------

  onToolCall(data: Record<string, unknown>): ActiveTool {
    const tool = toStr(
      pickFirst(data, ["tool", "toolName", "tool_name", "name", "function"]) ?? "",
      200,
    );
    const toolCallId = toStr(
      pickFirst(data, ["toolCallId", "tool_call_id", "id", "callId", "call_id"]) ?? "",
      200,
    );
    const rawInput = data["input"] ?? data["args"] ?? data["arguments"] ?? data["parameters"];
    const toolInput = toStr(rawInput, 4000);
    const startedMs = Date.now();
    const id = toolCallId || `${tool}:${startedMs}:${this.toolCallCount}`;
    const active: ActiveTool = { tool, tool_call_id: id, tool_input: toolInput, started_ms: startedMs };
    this.activeTools.set(id, active);
    this.tool = tool;
    this.toolCallId = id;
    this.toolInput = toolInput;
    this.toolCallCount += 1;

    const kind = toolKind(tool);
    if (kind) {
      this.kind = kind;
      this.state = "blocked";
      this.text = `${tool} waiting`.slice(0, 240);
    } else if (this.state !== "failed") {
      this.state = "working";
      this.text = tool.slice(0, 240);
    }

    const filePath = extractFilePath(tool, rawInput);
    if (filePath) {
      this.text = `${tool} · ${filePath}`.slice(0, 240);
    }
    return active;
  }

  onToolResult(data: Record<string, unknown>): RecentTool | null {
    const tool = toStr(pickFirst(data, ["tool", "toolName", "tool_name", "name"]) ?? this.tool, 200);
    const toolCallId = toStr(
      pickFirst(data, ["toolCallId", "tool_call_id", "id", "callId", "call_id"]) ?? this.toolCallId,
      200,
    );
    const rawOutput = data["output"] ?? data["result"] ?? data["tool_output"] ?? data["content"];
    const toolOutput = toStr(rawOutput, 4000);
    const isError =
      data["isError"] === true ||
      data["is_error"] === true ||
      toStr(data["status"] ?? "", 50).toLowerCase().includes("error") ||
      toStr(data["error"] ?? "", 50) !== "";
    this.toolOutput = toolOutput;

    let startedMs = Date.now();
    let input = this.toolInput;
    // Complete the matching active tool (or any oldest one as fallback).
    let key = toolCallId;
    let active = key ? this.activeTools.get(key) : undefined;
    if (!active && this.activeTools.size > 0) {
      const first = this.activeTools.keys().next();
      if (!first.done) {
        key = first.value as string;
        active = this.activeTools.get(key);
      }
    }
    if (active) {
      startedMs = active.started_ms;
      input = active.tool_input;
      this.activeTools.delete(key);
    }
    const durationMs = Math.max(0, Date.now() - startedMs);
    const recent: RecentTool = {
      tool,
      tool_call_id: toolCallId || key || tool,
      duration_ms: durationMs,
      ok: !isError,
      output_preview: toolOutput.slice(0, 500),
    };
    this.recentTools.push(recent);
    if (this.recentTools.length > 20) this.recentTools = this.recentTools.slice(-20);

    if (isError) {
      this.toolErrorCount += 1;
      const errText = toStr(data["error"] ?? toolOutput, 2000);
      if (errText) this.lastError = errText;
      this.state = "failed";
      this.text = (errText || `${tool} failed`).slice(0, 240);
    } else if (this.state !== "blocked") {
      this.state = "working";
    }

    // Record write/edit file touches.
    const rawInput = data["input"] ?? data["args"] ?? input;
    const filePath = extractFilePath(tool, typeof rawInput === "string" && rawInput ? tryParse(rawInput) ?? rawInput : (rawInput ?? input));
    if (filePath && isWriteTool(tool)) {
      this.recordFileTouch(filePath, tool.toLowerCase().includes("edit") ? "edit" : "write");
    }
    return recent;
  }

  recordFileTouch(filePath: string, action: string): void {
    const p = filePath.trim();
    if (!p) return;
    const key = `${action}:${p}`;
    if (this.seenFiles.has(key)) return;
    this.seenFiles.add(key);
    this.filesTouched.push({ path: p, action });
    if (this.filesTouched.length > 100) this.filesTouched = this.filesTouched.slice(-100);
  }

  // -- compaction & model -------------------------------------------------

  onCompaction(reason: string): void {
    this.compactionCount += 1;
    this.lastCompactionMs = Date.now();
    this.lastCompactionReason = reason || "manual";
  }

  onModelSelect(info: { provider?: unknown; model?: unknown; contextWindow?: unknown; thinkingLevel?: unknown }): void {
    if (info.provider !== undefined) {
      const v = toStr(info.provider, 200);
      if (v) this.provider = v;
    }
    if (info.model !== undefined) {
      const v = toStr(info.model, 200);
      if (v) this.model = v;
    }
    if (info.contextWindow !== undefined) {
      const w = toNum(info.contextWindow);
      if (w) this.contextWindow = w;
    }
    if (info.thinkingLevel !== undefined) {
      const v = toStr(info.thinkingLevel, 100);
      if (v) this.thinkingLevel = v;
    }
  }

  // -- payload ------------------------------------------------------------

  buildPayload(event: string, rawEvent?: unknown): Record<string, unknown> {
    return {
      session_id: this.sessionId,
      title: this.title,
      cwd: this.cwd,
      provider: this.provider,
      model: this.model,
      effort: this.effort,
      mode: this.mode,
      state: this.state,
      kind: this.kind,
      text: this.text,
      prompt: this.prompt,
      reply: this.reply,
      tool: this.tool,
      tool_call_id: this.toolCallId,
      tool_input: this.toolInput,
      tool_output: this.toolOutput,
      usage: { ...this.usage },
      context_tokens: this.contextTokens,
      context_window: this.contextWindow,
      context_percent: this.contextPercent,
      turn_count: this.turnCount,
      compaction_count: this.compactionCount,
      last_compaction_ms: this.lastCompactionMs,
      last_compaction_reason: this.lastCompactionReason,
      tool_call_count: this.toolCallCount,
      tool_error_count: this.toolErrorCount,
      active_tools: [...this.activeTools.values()].map((t) => ({
        tool: t.tool,
        tool_call_id: t.tool_call_id,
        input: t.tool_input,
      })),
      recent_tools: this.recentTools.map((t) => ({ ...t })),
      files_touched: this.filesTouched.map((f) => ({ ...f })),
      last_error: this.lastError,
      thinking_level: this.thinkingLevel,
      extras: { ...this.extras },
      raw_event: rawEvent === undefined ? {} : rawEvent,
      _event: event,
    };
  }
}

function tryParse(s: string): unknown {
  try {
    return JSON.parse(s);
  } catch {
    return null;
  }
}

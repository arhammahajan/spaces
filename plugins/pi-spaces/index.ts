// index.ts — RICH Pi Coding Agent companion extension for tmux-spaces.
//
// Loaded natively by Pi from ~/.pi/agent/extensions/pi-spaces/ or
// .pi/extensions/. Observes EVERYTHING Pi's ExtensionAPI exposes and pushes
// structured v2 envelopes to spacecore over UDS HTTP (POST /v1/plugin/pi).
//
// Safety contract:
// - Observe-only: never returns { block: true }, never mutates prompts/args.
// - Every handler is wrapped in try/catch so this extension can NEVER crash Pi.
// - Fire-and-forget UDS POST with 100ms timeout; silent no-op without $TMUX_PANE.

import { Tracker } from "./tracker.ts";
import { resolveSession, resolvePane, send } from "./transport.ts";

type AnyRecord = Record<string, unknown>;

// Minimal structural view of Pi's ExtensionAPI. Everything is optional so
// this keeps working across Pi versions (jiti/tsx/esbuild + Node 24
// type-stripping compatible: no enums, no parameter properties).
export interface PiEventContext {
  sessionManager?: {
    getSessionId?: () => unknown;
    getSessionFile?: () => unknown;
    getSessionName?: () => unknown;
    getBranch?: () => unknown;
    getEntries?: () => unknown;
    getTitle?: () => unknown;
  } | null;
  sessionId?: unknown;
  sessionFile?: unknown;
  cwd?: unknown;
  model?: {
    provider?: unknown;
    id?: unknown;
    name?: unknown;
    contextWindow?: unknown;
    contextLength?: unknown;
  } | null;
  provider?: unknown;
  hasUI?: unknown;
  getContextUsage?: () => unknown;
  thinkingLevel?: unknown;
  mode?: unknown;
  effort?: unknown;
  [key: string]: unknown;
}

export interface ExtensionAPI {
  on?: (event: string, handler: (ctx: PiEventContext, data?: AnyRecord) => unknown) => void;
  getThinkingLevel?: () => unknown;
  getActiveTools?: () => unknown;
  getModel?: () => unknown;
  version?: unknown;
  harnessVersion?: unknown;
  [key: string]: unknown;
}

function toStr(value: unknown, max = 4000): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value.length > max ? value.slice(0, max) : value;
  try {
    const s = JSON.stringify(value) ?? "";
    return s.length > max ? s.slice(0, max) : s;
  } catch {
    return String(value).slice(0, max);
  }
}

function toNum(value: unknown): number {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() !== "") {
    const n = Number(value);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function pick(data: AnyRecord | undefined, keys: string[]): unknown {
  if (!data) return undefined;
  for (const k of keys) {
    const v = data[k];
    if (v !== undefined && v !== null && v !== "") return v;
  }
  return undefined;
}

function harnessVersion(pi: ExtensionAPI): string {
  const v = pi.version ?? pi.harnessVersion ?? (process.env["SPACES_HARNESS_VERSION"] as unknown);
  return toStr(v as unknown, 100);
}

function readThinkingLevel(pi: ExtensionAPI, ctx: PiEventContext): string {
  try {
    if (typeof pi.getThinkingLevel === "function") {
      const v = (pi.getThinkingLevel as () => unknown)();
      const s = toStr(v, 100);
      if (s) return s;
    }
  } catch {
    /* ignore */
  }
  const fromCtx = ctx.thinkingLevel ?? ctx["thinking_level"] ?? ctx["effort"];
  return toStr(fromCtx as unknown, 100);
}

function readActiveTools(pi: ExtensionAPI): unknown[] {
  try {
    if (typeof pi.getActiveTools === "function") {
      const v = (pi.getActiveTools as () => unknown)();
      if (Array.isArray(v)) return v;
    }
  } catch {
    /* ignore */
  }
  return [];
}

function readModel(ctx: PiEventContext, pi: ExtensionAPI): { provider: string; model: string; contextWindow: number; effort: string } {
  let provider = "";
  let model = "";
  let contextWindow = 0;
  let effort = "";
  try {
    const m = (ctx.model ?? null) as PiEventContext["model"];
    if (m && typeof m === "object") {
      provider = toStr(m.provider, 200);
      model = toStr((m.id ?? m.name) as unknown, 200);
      contextWindow = toNum((m.contextWindow ?? m.contextLength) as unknown);
    }
  } catch {
    /* ignore */
  }
  try {
    if (!model && typeof pi.getModel === "function") {
      const m = (pi.getModel as () => unknown)() as AnyRecord | null;
      if (m && typeof m === "object") {
        if (!provider) provider = toStr(m["provider"], 200);
        if (!model) model = toStr((m["id"] ?? m["name"] ?? m["model"]) as unknown, 200);
        if (!contextWindow) contextWindow = toNum(m["contextWindow"] ?? m["context_window"]);
      }
    }
  } catch {
    /* ignore */
  }
  if (!provider) provider = toStr(ctx.provider, 200);
  if (!effort) effort = toStr((ctx.effort ?? ctx["thinkingLevel"]) as unknown, 100);
  return { provider, model, contextWindow, effort };
}

function readSessionInfo(ctx: PiEventContext): { sessionId: string; title: string; cwd: string; branch: unknown; entries: unknown } {
  let sessionId = toStr((ctx.sessionId ?? ctx["session_id"]) as unknown, 200);
  let title = "";
  let cwd = toStr((ctx.cwd ?? ctx["workingDirectory"] ?? ctx["working_directory"]) as unknown, 1000);
  let branch: unknown = null;
  let entries: unknown = null;
  try {
    const sm = ctx.sessionManager;
    if (sm && typeof sm === "object") {
      if (!sessionId && typeof sm.getSessionId === "function") {
        try {
          sessionId = toStr(sm.getSessionId() as unknown, 200);
        } catch {
          /* ignore */
        }
      }
      if (typeof sm.getSessionFile === "function") {
        try {
          const f = toStr(sm.getSessionFile() as unknown, 1000);
          if (f && !sessionId) sessionId = f;
        } catch {
          /* ignore */
        }
      }
      if (typeof sm.getSessionName === "function") {
        try {
          title = toStr(sm.getSessionName() as unknown, 400);
        } catch {
          /* ignore */
        }
      }
      if (!title && typeof sm.getTitle === "function") {
        try {
          title = toStr(sm.getTitle() as unknown, 400);
        } catch {
          /* ignore */
        }
      }
      if (typeof sm.getBranch === "function") {
        try {
          branch = sm.getBranch() as unknown;
        } catch {
          /* ignore */
        }
      }
      if (typeof sm.getEntries === "function") {
        try {
          entries = sm.getEntries() as unknown;
        } catch {
          /* ignore */
        }
      }
    }
  } catch {
    /* ignore */
  }
  if (!title) title = toStr((ctx as AnyRecord)["title"], 400);
  return { sessionId, title, cwd, branch, entries };
}

function readContextUsage(ctx: PiEventContext): { tokens: number; window: number; percent: number } {
  try {
    if (typeof ctx.getContextUsage === "function") {
      const u = (ctx.getContextUsage as () => unknown)() as AnyRecord | null;
      if (u && typeof u === "object") {
        const tokens = toNum(u["tokens"] ?? u["used"] ?? u["total_tokens"]);
        const window = toNum(u["contextWindow"] ?? u["windowSize"] ?? u["window"] ?? u["context_window"] ?? u["size"]);
        let percent = toNum(u["percent"] ?? u["percentage"] ?? u["utilization"]);
        if (tokens && window && !percent) percent = Math.min(100, (tokens / window) * 100);
        return { tokens, window, percent };
      }
    }
  } catch {
    /* ignore */
  }
  return { tokens: 0, window: 0, percent: 0 };
}

function emit(tracker: Tracker, pi: ExtensionAPI, event: string, rawEvent: unknown): void {
  try {
    const payload = tracker.buildPayload(event, (rawEvent ?? {}) as AnyRecord);
    // Force-resolve tmux attribution helpers so failures surface as
    // "unknown" rather than throwing inside buildEnvelope.
    void resolveSession();
    void resolvePane();
    send(event, payload, harnessVersion(pi));
  } catch {
    /* fail-open */
  }
}

/**
 * Pi extension entrypoint. Pi calls this with its ExtensionAPI; we subscribe
 * to every known lifecycle event and forward rich state to spacecore.
 */
export default function (pi: ExtensionAPI): void {
  try {
    if (!pi || typeof pi.on !== "function") return;
    const on = pi.on.bind(pi);
    const tracker = new Tracker();

    const safe = (
      fn: (ctx: PiEventContext, data?: AnyRecord) => void,
    ): ((ctx: PiEventContext, data?: AnyRecord) => void) => {
      return (ctx: PiEventContext, data?: AnyRecord) => {
        try {
          fn(ctx ?? {}, data ?? {});
        } catch {
          /* this extension must never crash Pi */
        }
        // Observe-only: never return { block: true }.
        return undefined;
      };
    };

    const syncSession = (ctx: PiEventContext): void => {
      try {
        const info = readSessionInfo(ctx);
        if (info.sessionId) tracker.sessionId = info.sessionId;
        if (info.title && !tracker.title) tracker.title = info.title;
        if (info.cwd) tracker.cwd = info.cwd;
      } catch {
        /* ignore */
      }
    };

    const syncModel = (ctx: PiEventContext, piRef: ExtensionAPI): void => {
      try {
        const m = readModel(ctx, piRef);
        if (m.provider) tracker.provider = m.provider;
        if (m.model) tracker.model = m.model;
        if (m.contextWindow) tracker.contextWindow = m.contextWindow;
        if (m.effort && !tracker.effort) tracker.effort = m.effort;
        const mode = toStr((ctx.mode ?? (ctx as AnyRecord)["agentMode"]) as unknown, 100);
        if (mode) tracker.mode = mode;
        const thinking = readThinkingLevel(piRef, ctx);
        if (thinking) {
          tracker.thinkingLevel = thinking;
          if (!tracker.effort) tracker.effort = thinking;
        }
        const active = readActiveTools(piRef);
        if (active.length > 0) {
          tracker.extras["pi_active_tools"] = active.slice(0, 20);
        }
        if (ctx.hasUI !== undefined) tracker.extras["has_ui"] = ctx.hasUI;
      } catch {
        /* ignore */
      }
    };

    const syncContext = (ctx: PiEventContext): void => {
      try {
        const u = readContextUsage(ctx);
        if (u.tokens || u.window || u.percent) tracker.applyContext(u.tokens, u.window, u.percent);
      } catch {
        /* ignore */
      }
    };

    const branchEntries = (branch: unknown): unknown[] | null => {
      if (Array.isArray(branch)) return branch as unknown[];
      if (branch && typeof branch === "object") {
        const b = branch as AnyRecord;
        for (const k of ["entries", "messages", "turns", "history"]) {
          if (Array.isArray(b[k])) return b[k] as unknown[];
        }
      }
      return null;
    };

    const handleSessionStart = safe((ctx) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      try {
        const info = readSessionInfo(ctx);
        const entries = branchEntries(info.branch) ?? (Array.isArray(info.entries) ? (info.entries as unknown[]) : null);
        if (entries) tracker.reconstructFromBranch(entries);
      } catch {
        /* ignore */
      }
      syncContext(ctx);
      if (!tracker.state || tracker.state === "idle") tracker.state = "working";
      emit(tracker, pi, "session_start", { ctx: snapshotCtx(ctx) });
    });

    const handleSessionSwitch = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      try {
        const info = readSessionInfo(ctx);
        const entries = branchEntries(info.branch) ?? (Array.isArray(info.entries) ? (info.entries as unknown[]) : null);
        if (entries) tracker.reconstructFromBranch(entries);
      } catch {
        /* ignore */
      }
      syncContext(ctx);
      emit(tracker, pi, "session_switch", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleSessionFork = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      emit(tracker, pi, "session_fork", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleSessionTree = safe((ctx, data) => {
      syncSession(ctx);
      try {
        const info = readSessionInfo(ctx);
        tracker.extras["session_tree"] = info.branch ?? info.entries ?? (data?.["tree"] as unknown) ?? null;
      } catch {
        /* ignore */
      }
      emit(tracker, pi, "session_tree", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleSessionShutdown = safe((ctx, data) => {
      syncSession(ctx);
      if (tracker.state === "working" || tracker.state === "idle") tracker.state = "done";
      emit(tracker, pi, "session_shutdown", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleInput = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      const prompt = toStr(
        pick(data, ["prompt", "message", "text", "input", "content"]) ?? (ctx as AnyRecord)["prompt"],
        4000,
      );
      tracker.onInput(prompt);
      syncContext(ctx);
      emit(tracker, pi, "input", { ...(data ?? {}), prompt });
    });

    const handleAgentStart = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      tracker.onAgentStart();
      syncContext(ctx);
      emit(tracker, pi, "agent_start", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleAgentEnd = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      syncContext(ctx);
      tracker.onAgentEnd(data ?? {});
      emit(tracker, pi, "agent_end", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleTurnStart = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      syncContext(ctx);
      if (tracker.state === "idle" || tracker.state === "done") tracker.state = "working";
      emit(tracker, pi, "turn_start", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleTurnEnd = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      const d = data ?? {};
      const usage = tracker.extractUsage(d);
      if (Object.keys(usage).length > 0) tracker.addUsage(usage);
      const reply = pick(d, ["reply", "response", "text", "output", "message", "assistant_message"]);
      if (reply !== undefined) tracker.reply = toStr(reply, 4000);
      const err = pick(d, ["error", "err", "last_error", "failure"]);
      if (err !== undefined && toStr(err) !== "") {
        tracker.lastError = toStr(err, 2000);
        tracker.state = "failed";
      }
      const u = readContextUsage(ctx);
      tracker.onTurnEnd({ tokens: u.tokens || undefined, window: u.window || undefined, percent: u.percent || undefined });
      const ctxUsageRaw = pick(d, ["context_usage", "contextUsage", "context"]);
      if (ctxUsageRaw && typeof ctxUsageRaw === "object") {
        const c = ctxUsageRaw as AnyRecord;
        tracker.applyContext(c["tokens"] ?? c["used"], c["contextWindow"] ?? c["windowSize"] ?? c["window"], c["percent"]);
      }
      emit(tracker, pi, "turn_end", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleToolCall = (piEvent: string) =>
      safe((ctx, data) => {
        syncSession(ctx);
        const d = { ...(data ?? {}) };
        // Normalize the many Pi tool-call shapes into one.
        const nested = (d["toolCall"] ?? d["tool_call"] ?? d["call"]) as unknown;
        if (nested && typeof nested === "object") Object.assign(d, nested as AnyRecord);
        tracker.onToolCall(d);
        emit(tracker, pi, piEvent, { ...d, ctx: snapshotCtx(ctx) });
      });

    const handleToolResult = (piEvent: string) =>
      safe((ctx, data) => {
        syncSession(ctx);
        const d = { ...(data ?? {}) };
        const nested = (d["toolResult"] ?? d["tool_result"] ?? d["result"]) as unknown;
        void nested;
        tracker.onToolResult(d);
        emit(tracker, pi, piEvent, { ...d, ctx: snapshotCtx(ctx) });
      });

    const handleBeforeCompact = safe((ctx, data) => {
      syncSession(ctx);
      const reason = toStr(pick(data, ["reason", "trigger", "mode"]) ?? "manual", 100);
      tracker.onCompaction(reason || "manual");
      emit(tracker, pi, "session_before_compact", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleCompact = safe((ctx, data) => {
      syncSession(ctx);
      const reason = toStr(
        pick(data, ["reason", "trigger", "mode"]) ?? tracker.lastCompactionReason ?? "manual",
        100,
      );
      // session_before_compact already counted this compaction in the normal
      // flow; only count here when it fired standalone.
      void reason;
      syncContext(ctx);
      emit(tracker, pi, "session_compact", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    const handleModelSelect = safe((ctx, data) => {
      syncSession(ctx);
      syncModel(ctx, pi);
      const modelObj = ((ctx.model ?? {}) as object) as AnyRecord;
      const dd = data ?? {};
      const firstPresent = (keys: string[]): unknown => {
        const a = pick(dd, keys);
        if (a !== undefined) return a;
        return pick(modelObj, keys);
      };
      tracker.onModelSelect({
        provider: firstPresent(["provider"]),
        model: firstPresent(["model", "modelId", "model_id", "id", "name"]),
        contextWindow: firstPresent(["contextWindow", "context_window", "contextWindowSize", "windowSize"]),
        thinkingLevel: firstPresent(["thinkingLevel", "thinking_level", "effort", "thinking"]),
      });
      emit(tracker, pi, "model_select", { ...(data ?? {}), ctx: snapshotCtx(ctx) });
    });

    // Session lifecycle.
    on("session_start", handleSessionStart);
    on("session_switch", handleSessionSwitch);
    on("session_fork", handleSessionFork);
    on("session_tree", handleSessionTree);
    on("session_shutdown", handleSessionShutdown);
    // Agent & turn lifecycle (register common aliases; Pi versions vary).
    on("input", handleInput);
    on("before_agent_start", handleInput);
    on("agent_start", handleAgentStart);
    on("agent_end", handleAgentEnd);
    on("turn_start", handleTurnStart);
    on("turn_end", handleTurnEnd);
    // Tools (both naming families).
    on("tool_call", handleToolCall("tool_call"));
    on("tool_execution_start", handleToolCall("tool_execution_start"));
    on("tool_result", handleToolResult("tool_result"));
    on("tool_execution_end", handleToolResult("tool_execution_end"));
    // Compaction & model.
    on("session_before_compact", handleBeforeCompact);
    on("session_compact", handleCompact);
    on("model_select", handleModelSelect);
  } catch {
    /* never break Pi during setup either */
  }
}

function snapshotCtx(ctx: PiEventContext): AnyRecord {
  try {
    return {
      cwd: toStr((ctx.cwd ?? "") as unknown, 1000),
      sessionId: toStr(((ctx.sessionId ?? "") as unknown) as string, 200),
      hasUI: ctx.hasUI ?? null,
    };
  } catch {
    return {};
  }
}

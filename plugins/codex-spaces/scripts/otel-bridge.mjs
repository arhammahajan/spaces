// otel-bridge.mjs — Codex OpenTelemetry bridge for tmux-spaces.
//
// Codex can export OTLP HTTP/JSON telemetry ([otel] config in
// ~/.codex/config.toml) to /v1/logs, /v1/metrics and /v1/traces. This
// script parses one OTLP JSON export payload from stdin, translates the
// Codex signals it carries — codex.conversation_starts, codex.api_request,
// codex.sse_event, codex.tool_decision, codex.tool_result, codex.user_prompt,
// token usage, durations, status codes, 429 rate limits, model, reasoning
// effort and sandbox policy — into a v2 envelope, and POSTs it to
// /v1/plugin/codex via transport.mjs.
//
// Usage:
//   <otlp_json_on_stdin> | node otel-bridge.mjs [/v1/logs|/v1/metrics|/v1/traces]
//
// ALWAYS exits 0 (fail-open telemetry must never disturb the agent).

import { fileURLToPath } from "node:url";
import { resolve as resolvePath } from "node:path";
import {
  shouldDeliver,
  buildEnvelope,
  postEnvelope,
} from "./transport.mjs";
import { readStdinJson } from "./hook-bridge.mjs";

function asAttrs(list) {
  const out = {};
  if (!Array.isArray(list)) return out;
  for (const a of list) {
    if (!a || typeof a !== "object") continue;
    const k = a.key;
    if (typeof k !== "string" || !k) continue;
    out[k] = anyValue(a.value);
  }
  return out;
}

function anyValue(v) {
  if (!v || typeof v !== "object") return v;
  if (typeof v.stringValue === "string") return v.stringValue;
  if (typeof v.intValue === "string" || typeof v.intValue === "number") {
    const n = Number(v.intValue);
    return Number.isFinite(n) ? n : v.intValue;
  }
  if (typeof v.doubleValue === "number") return v.doubleValue;
  if (typeof v.boolValue === "boolean") return v.boolValue;
  if (v.arrayValue && Array.isArray(v.arrayValue.values)) {
    return v.arrayValue.values.map(anyValue);
  }
  if (v.kvlistValue && Array.isArray(v.kvlistValue.values)) {
    const o = {};
    for (const kv of v.kvlistValue.values) {
      if (kv && typeof kv.key === "string") o[kv.key] = anyValue(kv.value);
    }
    return o;
  }
  if (typeof v.bytesValue === "string") return v.bytesValue;
  return v;
}

function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}

// Collect every attribute map in an OTLP export body plus event names,
// metric names/values and span names/durations.
export function collectOtelSignals(body) {
  const attrMaps = [];
  const metricNames = [];
  const metricPoints = []; // {name, attrs, value}
  const logBodies = [];
  const logAttrs = [];
  const spans = []; // {name, attrs, durationMs, statusCode, statusMessage}
  const b = body && typeof body === "object" ? body : {};

  const pushResource = (r) => {
    if (r && typeof r === "object") {
      const a = asAttrs(r.attributes);
      if (Object.keys(a).length) attrMaps.push(a);
    }
  };

  // Logs: {resourceLogs:[{resource, scopeLogs:[{scope, logRecords:[...]}]}]}
  for (const rl of b.resourceLogs || []) {
    pushResource(rl.resource);
    for (const sl of rl.scopeLogs || rl.scope_logs || []) {
      for (const rec of sl.logRecords || sl.log_records || []) {
        const a = asAttrs(rec.attributes);
        if (Object.keys(a).length) {
          attrMaps.push(a);
          logAttrs.push(a);
        }
        const bv = rec.body ? anyValue(rec.body) : "";
        const text = typeof bv === "string" ? bv : JSON.stringify(bv ?? "");
        if (text) logBodies.push(text.slice(0, 2000));
        if (typeof rec.severityText === "string" && rec.severityText) {
          logBodies.push(rec.severityText.slice(0, 200));
        }
      }
    }
  }

  // Metrics: {resourceMetrics:[{resource, scopeMetrics:[{metrics:[...]}]}]}
  for (const rm of b.resourceMetrics || b.resource_metrics || []) {
    pushResource(rm.resource);
    for (const sm of rm.scopeMetrics || rm.scope_metrics || []) {
      for (const m of sm.metrics || []) {
        const name = typeof m.name === "string" ? m.name : "";
        if (name) metricNames.push(name);
        const datas = [
          m.sum && m.sum.dataPoints,
          m.gauge && m.gauge.dataPoints,
          m.histogram && m.histogram.dataPoints,
          m.summary && m.summary.dataPoints,
          m.dataPoints,
          m.data_points,
        ];
        for (const dp of datas) {
          if (!Array.isArray(dp)) continue;
          for (const p of dp) {
            if (!p || typeof p !== "object") continue;
            const a = asAttrs(p.attributes);
            let value = 0;
            if (p.asInt !== undefined) value = num(p.asInt);
            else if (p.asDouble !== undefined) value = num(p.asDouble);
            else if (p.sum !== undefined) value = num(p.sum);
            else if (p.count !== undefined) value = num(p.count);
            else if (p.value !== undefined) value = num(p.value);
            metricPoints.push({ name, attrs: a, value });
            if (Object.keys(a).length) attrMaps.push(a);
          }
        }
      }
    }
  }

  // Traces: {resourceSpans:[{resource, scopeSpans:[{spans:[...]}]}]}
  for (const rs of b.resourceSpans || b.resource_spans || []) {
    pushResource(rs.resource);
    for (const ss of rs.scopeSpans || rs.scope_spans || []) {
      for (const sp of ss.spans || []) {
        const a = asAttrs(sp.attributes);
        if (Object.keys(a).length) attrMaps.push(a);
        let durationMs = 0;
        try {
          const s = BigInt(sp.startTimeUnixNano ?? 0);
          const e = BigInt(sp.endTimeUnixNano ?? 0);
          if (e > s) durationMs = Number(e - s) / 1e6;
        } catch {
          // ignore malformed nanos
        }
        spans.push({
          name: typeof sp.name === "string" ? sp.name : "",
          attrs: a,
          durationMs,
          statusCode: sp.status ? sp.status.code : undefined,
          statusMessage:
            sp.status && typeof sp.status.message === "string"
              ? sp.status.message
              : "",
        });
      }
    }
  }

  return { attrMaps, metricNames, metricPoints, logBodies, logAttrs, spans };
}

const TOKEN_KEYS = [
  [/input/, ["input_tokens", "inputtokens"]],
  [/output/, ["output_tokens", "outputtokens"]],
  [/reason/, ["reasoning_tokens", "reasoningtokens"]],
  [/cach/, ["cache_read_tokens", "cachereadtokens", "cached_tokens"]],
];

// Translate collected OTLP signals into a v2 payload.
export function buildOtelPayload(signals, signalPath) {
  const { attrMaps, metricNames, metricPoints, logBodies, spans } = signals;
  const merged = {};
  for (const m of attrMaps) {
    for (const [k, v] of Object.entries(m)) {
      if (merged[k] === undefined) merged[k] = v;
    }
  }
  const lower = {};
  for (const [k, v] of Object.entries(merged)) lower[k.toLowerCase()] = v;

  const get = (...keys) => {
    for (const k of keys) {
      if (merged[k] !== undefined && merged[k] !== null && merged[k] !== "") {
        return merged[k];
      }
    }
    for (const k of keys) {
      const lk = k.toLowerCase();
      if (lower[lk] !== undefined && lower[lk] !== null && lower[lk] !== "") {
        return lower[lk];
      }
    }
    return "";
  };

  const getNum = (...keys) => num(get(...keys));

  const sessionId = String(
    get(
      "session.id",
      "codex.session_id",
      "codex.thread_id",
      "thread_id",
      "threadId",
      "conversation_id",
      "conversationId",
      "session_id",
      "sessionId",
    ) || "",
  ).trim();
  const model = String(
    get(
      "gen_ai.request.model",
      "gen_ai.response.model",
      "codex.model",
      "model",
      "response.model",
      "llm.model",
    ) || "",
  ).trim();
  const effort = String(
    get("reasoning_effort", "codex.reasoning_effort", "gen_ai.reasoning_effort") ||
      "",
  ).trim();
  const sandboxMode = String(
    get("sandbox", "codex.sandbox_mode", "sandbox_mode", "codex.sandbox") || "",
  ).trim();
  const cwd = String(
    get("cwd", "codex.cwd", "working_directory", "service.instance.id") || "",
  ).trim();

  // Token usage: explicit usage attributes first, then metric datapoints.
  let inTok = getNum(
    "gen_ai.usage.input_tokens",
    "llm.usage.input_tokens",
    "codex.usage.input_tokens",
    "input_tokens",
  );
  let outTok = getNum(
    "gen_ai.usage.output_tokens",
    "llm.usage.output_tokens",
    "codex.usage.output_tokens",
    "output_tokens",
  );
  let reasonTok = getNum(
    "gen_ai.usage.reasoning_tokens",
    "codex.usage.reasoning_tokens",
    "reasoning_tokens",
  );
  let cacheTok = getNum(
    "gen_ai.usage.cache_read_tokens",
    "codex.usage.cache_read_tokens",
    "cache_read_tokens",
    "cached_tokens",
  );
  for (const p of metricPoints) {
    const hay = `${p.name} ${Object.keys(p.attrs).join(" ")}`.toLowerCase();
    for (const [re] of TOKEN_KEYS) {
      if (!re.test(hay)) continue;
      if (/input/.test(hay) && !/output/.test(hay)) inTok += p.value;
      else if (/output/.test(hay)) outTok += p.value;
      else if (/reason/.test(hay)) reasonTok += p.value;
      else if (/cach/.test(hay)) cacheTok += p.value;
    }
  }

  // Event census: metric names, span names and log bodies mentioning codex.*.
  const seen = new Set(metricNames.filter(Boolean));
  for (const s of spans) if (s.name) seen.add(s.name);
  const bodyText = logBodies.join("\n");
  const codexEvents = [];
  const pushEvent = (n) => {
    if (n && !codexEvents.includes(n)) codexEvents.push(n);
  };
  for (const n of seen) {
    if (/^codex\./i.test(n)) pushEvent(n);
  }
  for (const m of bodyText.match(/codex\.[a-z_]+/gi) || []) pushEvent(m);
  const has = (re) =>
    codexEvents.some((e) => re.test(e)) ||
    [...seen].some((e) => re.test(e)) ||
    re.test(bodyText);

  let toolCalls = 0;
  let toolErrors = 0;
  for (const p of metricPoints) {
    if (/tool_?decision|tool_?result/i.test(p.name)) toolCalls += 1;
  }
  // Span-based tool accounting (each tool span = one call).
  for (const s of spans) {
    if (/tool/i.test(s.name)) {
      toolCalls += 1;
      if (
        (typeof s.statusCode === "number" && s.statusCode === 2) ||
        /error|fail/i.test(s.statusMessage || "")
      ) {
        toolErrors += 1;
      }
    }
  }
  // Status codes + 429 detection across attrs, datapoints and bodies.
  const statusCodes = [];
  const pushCode = (c) => {
    const n = Math.floor(num(c));
    if (n > 0 && !statusCodes.includes(n)) statusCodes.push(n);
  };
  for (const k of [
    "http.status_code",
    "http.response.status_code",
    "httpStatusCode",
    "status_code",
    "codex.status_code",
  ]) {
    const v = get(k);
    if (v !== "") pushCode(v);
  }
  for (const p of metricPoints) {
    for (const [ak, av] of Object.entries(p.attrs)) {
      if (/status.?code/i.test(ak)) pushCode(av);
    }
    if (/status.?code|429/.test(p.name)) pushCode(p.value);
  }
  if (/\b429\b/.test(bodyText)) pushCode(429);

  let rateLimited =
    statusCodes.includes(429) ||
    /rate.?limit|too many requests/i.test(bodyText) ||
    has(/rate.?limit/) ||
    get("codex.rate_limited", "rate_limited") === true;
  let rateLimitMessage = "";
  const rlMsg = get(
    "codex.rate_limit_message",
    "rate_limit_message",
    "retry.message",
    "error.message",
  );
  if (typeof rlMsg === "string" && rlMsg.trim()) {
    rateLimitMessage = rlMsg.trim().slice(0, 500);
  } else {
    const m = bodyText.match(/.{0,80}rate.?limit.{0,120}/i);
    if (m) rateLimitMessage = m[0].trim().slice(0, 500);
    else if (statusCodes.includes(429)) rateLimitMessage = "HTTP 429";
  }
  let rateLimitResetMs = getNum(
    "codex.rate_limit_reset_ms",
    "rate_limit_reset_ms",
    "retry_after_ms",
  );
  const retryAfterS = getNum("retry_after", "retry-after", "x-ratelimit-reset");
  if (!rateLimitResetMs && retryAfterS > 0 && retryAfterS < 1e9) {
    rateLimitResetMs = Date.now() + retryAfterS * 1000;
  }

  // Duration: max span duration wins; else duration-ish datapoints.
  let durationMs = 0;
  for (const s of spans) {
    if (s.durationMs > durationMs) durationMs = s.durationMs;
  }
  if (!durationMs) {
    for (const p of metricPoints) {
      if (/duration|latency/i.test(p.name) && p.value > durationMs) {
        durationMs = p.value;
      }
    }
  }

  // Event selection (all map to `working` server-side; nothing here should
  // flip a pane to done/failed on telemetry alone).
  let event = "response";
  if (
    has(/tool_?result/) ||
    has(/tool_?decision/) ||
    spans.some((s) => /tool/i.test(s.name))
  ) {
    event = "post_tool";
  } else if (has(/user_?prompt/)) {
    event = "prompt";
  }
  if (
    has(/tool_?failure|tool_?error/) ||
    toolErrors > 0 ||
    (rateLimited && event === "response")
  ) {
    event = event === "prompt" ? event : "tool_failure";
  }

  const tool = String(
    get("codex.tool", "tool_name", "toolName", "tool") || "",
  ).trim();

  const usage =
    inTok || outTok || reasonTok || cacheTok
      ? {
          input_tokens: inTok,
          output_tokens: outTok,
          reasoning_tokens: reasonTok,
          cache_read_tokens: cacheTok,
          cost_usd: 0,
        }
      : null;

  const contextTokens = getNum(
    "codex.context_tokens",
    "context_tokens",
    "llm.context_tokens",
  );
  const contextWindow = getNum(
    "codex.context_window",
    "context_window",
    "llm.context_window",
    "model_context_window",
  );
  const contextPercent =
    contextTokens > 0 && contextWindow > 0
      ? Math.round((contextTokens / contextWindow) * 1000) / 10
      : 0;

  const lastAssistant = logBodies.length
    ? logBodies[logBodies.length - 1].slice(0, 4000)
    : "";
  const lastError = toolErrors > 0 || rateLimited ? rateLimitMessage : "";

  return {
    event,
    payload: {
      session_id: sessionId || null,
      parent_session_id: null,
      is_subagent: false,
      agent_id: null,
      title: null,
      cwd: cwd || null,
      model: model || null,
      effort: effort || null,
      mode: null,
      sandbox_mode: sandboxMode || null,
      state: event === "tool_failure" ? "working" : "working",
      kind: null,
      text:
        event === "post_tool"
          ? tool
            ? `${tool} done`
            : "tool done"
          : event === "prompt"
            ? "prompt: working"
            : lastAssistant
              ? lastAssistant.replace(/\s+/g, " ").trim().slice(0, 240)
              : "telemetry",
      prompt: has(/user_?prompt/) ? lastAssistant.slice(0, 4096) || null : null,
      reply: event !== "prompt" ? lastAssistant.slice(0, 4096) || null : null,
      tool: tool || null,
      tool_call_id: null,
      tool_input: null,
      tool_output: null,
      usage,
      context_tokens: contextTokens,
      context_window: contextWindow,
      context_percent: contextPercent,
      turn_count: 0,
      compaction_count: 0,
      tool_call_count: toolCalls,
      tool_error_count: toolErrors,
      rate_limited: rateLimited,
      rate_limit_reset_ms: rateLimitResetMs,
      rate_limit_message: rateLimitMessage,
      files_touched: [],
      pending_permission: null,
      last_error: lastError,
      extras: {
        otel_signal: signalPath,
        otel_events: codexEvents,
        metric_names: [...new Set(metricNames)],
        status_codes: statusCodes,
        duration_ms: Math.round(durationMs * 100) / 100,
      },
      raw_event: { signal: signalPath, attributes: merged },
    },
  };
}

async function main() {
  try {
    const signalPath = process.argv[2] || "/v1/logs";
    const body = await readStdinJson(750);
    if (!shouldDeliver()) process.exit(0);
    const signals = collectOtelSignals(body || {});
    const { event, payload } = buildOtelPayload(signals, signalPath);
    const envelope = buildEnvelope(event, payload);
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

// index.mjs — opencode-spaces main plugin export.
//
// Drop-in compatible with `~/.config/opencode/plugins/` and with npm package
// imports. The default export is the OpenCode plugin function:
//
//   export default async ({ project, client, $, directory, worktree }) => ({ ... })
//
// Every hook handler is wrapped in try/catch and every UDS POST is
// fire-and-forget with a 100ms timeout, so this plugin can neither crash nor
// slow down OpenCode. All handlers are observe-only: they never mutate inputs
// and never return values that alter OpenCode's behavior.

import { Tracker, PLUGIN_META, num, str } from "./tracker.mjs";
import { postEnvelope, getTmuxSession } from "./transport.mjs";

export { Tracker, PLUGIN_META };
export { postEnvelope, getTmuxSession } from "./transport.mjs";

const TEXT_THROTTLE_MS = 250;
const RAW_EVENT_CHARS = 8192;

// JSON-safe copy of the raw event for the `raw_event` payload field.
export function safeRaw(value) {
  try {
    const s = JSON.stringify(value ?? {});
    if (s.length <= RAW_EVENT_CHARS) return JSON.parse(s);
    return { _truncated: true, preview: s.slice(0, RAW_EVENT_CHARS) };
  } catch {
    return {};
  }
}

export function buildEnvelope({
  nativeEvent,
  payload,
  tmuxSession,
  pane,
  harnessVersion,
  nowMs,
  pid,
}) {
  return {
    v: 2,
    agent: "opencode",
    session: str(tmuxSession),
    pane: str(pane),
    plugin: {
      name: PLUGIN_META.name,
      version: PLUGIN_META.version,
      capabilities: [...PLUGIN_META.capabilities],
    },
    event: str(nativeEvent),
    ts_ms: num(nowMs, Date.now()),
    pid: num(pid, 0),
    harness_version: str(harnessVersion),
    payload,
  };
}

// Best-effort, non-blocking enrichment of cached model context windows and
// MCP server status from the OpenCode client. All errors swallowed.
function enrichFromClient(client, tracker) {
  if (!client || typeof client !== "object") return;
  try {
    const maybeList = client?.provider?.list;
    if (typeof maybeList === "function") {
      Promise.resolve()
        .then(() => maybeList.call(client.provider))
        .then((listed) => {
          try {
            const providers = Array.isArray(listed)
              ? listed
              : Array.isArray(listed?.providers)
                ? listed.providers
                : Array.isArray(listed?.data)
                  ? listed.data
                  : [];
            for (const p of providers) {
              if (!p || typeof p !== "object") continue;
              const pid = p.id || p.providerID || p.provider;
              const models = Array.isArray(p.models)
                ? p.models
                : Array.isArray(p.data)
                  ? p.data
                  : [];
              for (const m of models) {
                if (!m || typeof m !== "object") continue;
                const mid = m.id || m.modelID || m.model;
                const limit =
                  m.limit && typeof m.limit === "object" ? m.limit : null;
                const ctx = num(
                  (limit && (limit.context ?? limit.contextWindow)) ??
                    m.context ??
                    m.contextWindow ??
                    0,
                );
                if (ctx > 0) tracker.noteModelContext(pid, mid, ctx);
              }
            }
          } catch {
            /* ignore malformed provider list */
          }
        })
        .catch(() => {});
    }
  } catch {
    /* ignore */
  }
  try {
    const maybeStatus = client?.mcp?.status;
    if (typeof maybeStatus === "function") {
      Promise.resolve()
        .then(() => maybeStatus.call(client.mcp))
        .then(() => {})
        .catch(() => {});
    }
  } catch {
    /* ignore */
  }
}

// Create the hooks object. Seams (`tracker`, `sender`, `resolveSession`,
// `clock`) exist for tests; production uses the real transport.
export function createHooks({
  tracker = new Tracker(),
  sender = postEnvelope,
  resolveSession = getTmuxSession,
  clock = Date.now,
  harnessVersion = "",
} = {}) {
  let version = str(harnessVersion);
  let lastTextPushMs = 0;

  async function emit(nativeEvent, sessionID, opts = {}) {
    try {
      const nowMs = clock();
      if (opts.throttleText) {
        if (nowMs - lastTextPushMs < TEXT_THROTTLE_MS) return;
        lastTextPushMs = nowMs;
      }
      const payload = tracker.snapshot(sessionID);
      payload.raw_event = opts.rawEvent !== undefined ? opts.rawEvent : {};
      let tmuxSession = "";
      try {
        tmuxSession = str(await resolveSession());
      } catch {
        tmuxSession = "";
      }
      const pane = str(process.env.TMUX_PANE);
      const envelope = buildEnvelope({
        nativeEvent,
        payload,
        tmuxSession,
        pane,
        harnessVersion: version,
        nowMs,
        pid: process.pid,
      });
      try {
        await sender(envelope);
      } catch {
        /* fail-open: never let transport break OpenCode */
      }
    } catch {
      /* never throw out of a hook path */
    }
  }

  function setVersion(v) {
    const s = str(v);
    if (s) version = s;
  }

  return {
    // Test seam: allow pinning the harness version.
    __setHarnessVersion: setVersion,

    event: async ({ event } = {}) => {
      try {
        if (!event || typeof event !== "object") return;
        const type = str(event.type ?? event.name ?? event.event);
        if (!type) return;
        const props =
          event.properties && typeof event.properties === "object"
            ? event.properties
            : event.payload && typeof event.payload === "object"
              ? event.payload
              : event;
        const res = tracker.applyBusEvent(type, props);
        await emit(type, res.sessionID, {
          throttleText: Boolean(res.throttleText),
          rawEvent: safeRaw(props),
        });
      } catch {
        /* ignore */
      }
    },

    "tool.execute.before": async (input, output) => {
      try {
        const sessionID = tracker.toolBefore(input ?? {});
        await emit("tool.execute.before", sessionID, { rawEvent: {} });
      } catch {
        /* ignore */
      }
      // Observe-only: return nothing so OpenCode's execution is untouched.
    },

    "tool.execute.after": async (input, output) => {
      try {
        const sessionID = tracker.toolAfter(input ?? {}, output ?? {});
        await emit("tool.execute.after", sessionID, { rawEvent: {} });
      } catch {
        /* ignore */
      }
    },

    "chat.params": async (input, output) => {
      try {
        const sessionID = tracker.chatParams(input ?? {});
        await emit("chat.params", sessionID, { rawEvent: {} });
      } catch {
        /* ignore */
      }
    },

    "experimental.session.compacting": async (input, output) => {
      try {
        const sessionID = tracker.compacting(input ?? {});
        await emit("experimental.session.compacting", sessionID, {
          rawEvent: {},
        });
      } catch {
        /* ignore */
      }
    },
  };
}

// OpenCode plugin entry point.
export default async function opencodeSpacesPlugin(context = {}) {
  const tracker = new Tracker();
  let harnessVersion = "";
  try {
    const ctx = context && typeof context === "object" ? context : {};
    harnessVersion = str(
      ctx.version ?? ctx.opencodeVersion ?? process.env.OPENCODE_VERSION,
    );
    enrichFromClient(ctx.client, tracker);
  } catch {
    /* ignore */
  }
  return createHooks({ tracker, harnessVersion });
}

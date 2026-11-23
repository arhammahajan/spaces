// transport.mjs — fail-open UDS HTTP/1.1 client for the codex-spaces plugin.
//
// Discovers the spacecore listener via:
//   $SPACES_HOOKS_RUNTIME/hooks.endpoint.json
//   else $XDG_RUNTIME_DIR/spaces/hooks.endpoint.json
//   else /tmp/spaces-<uid>/hooks.endpoint.json
// (mirrors core.RuntimeDir in internal/hooks/core/spacecore.go)
//
// The endpoint document carries {socket, tcp_port, token}. Envelopes are
// POSTed to /v1/plugin/codex with Content-Type, Content-Length and the
// X-ZJ-Token header. Everything is fail-open: any error resolves false.
//
// Silent no-op policy: when $TMUX_PANE is unset the hook is not running in
// a tmux pane, so shouldDeliver() returns false unless
// SPACES_PLUGIN_ALLOW_NO_TMUX=1 is set.

import net from "node:net";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const POST_PATH = "/v1/plugin/codex";
const TIMEOUT_MS = 100;

let pluginVersion = "1.0.0";
try {
  const pkg = JSON.parse(
    readFileSync(new URL("../package.json", import.meta.url), "utf8"),
  );
  if (pkg && typeof pkg.version === "string" && pkg.version) {
    pluginVersion = pkg.version;
  }
} catch {
  // keep default
}

export function runtimeDir() {
  const configured = process.env.SPACES_HOOKS_RUNTIME;
  if (configured) return configured;
  const xdg = process.env.XDG_RUNTIME_DIR;
  if (xdg) return path.join(xdg, "spaces");
  const uid =
    typeof process.getuid === "function" ? process.getuid() : 0;
  return path.join(os.tmpdir(), `spaces-${uid}`);
}

export function endpointPath() {
  return path.join(runtimeDir(), "hooks.endpoint.json");
}

export function readEndpoint() {
  let raw;
  try {
    raw = fs.readFileSync(endpointPath(), "utf8");
  } catch {
    return null;
  }
  let doc;
  try {
    doc = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!doc || typeof doc !== "object") return null;
  const socket = typeof doc.socket === "string" ? doc.socket : "";
  const token = typeof doc.token === "string" ? doc.token : "";
  if (!socket || !token) return null;
  let tcpPort = 0;
  const tp = doc.tcp_port ?? doc.tcpPort;
  if (typeof tp === "number" && Number.isFinite(tp) && tp > 0) {
    tcpPort = Math.floor(tp);
  }
  return { socket, token, tcpPort };
}

let sessionCache = undefined;

export function resetSessionCache() {
  sessionCache = undefined;
}

// Test/embedding override. Otherwise resolved once per process via
// `tmux display-message -p '#S'` (100ms budget) and cached.
export function resolveSession() {
  if (sessionCache !== undefined) return sessionCache;
  const override = process.env.SPACES_TMUX_SESSION;
  if (override) {
    sessionCache = override;
    return sessionCache;
  }
  sessionCache = "";
  try {
    const out = execFileSync("tmux", ["display-message", "-p", "#S"], {
      encoding: "utf8",
      timeout: TIMEOUT_MS,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const name = String(out || "").trim();
    if (name && !/[\r\n]/.test(name)) sessionCache = name;
  } catch {
    // fail-open: leave empty
  }
  return sessionCache;
}

export function resolvePane() {
  return process.env.TMUX_PANE || "";
}

export function shouldDeliver() {
  if (resolvePane()) return true;
  return process.env.SPACES_PLUGIN_ALLOW_NO_TMUX === "1";
}

export function buildEnvelope(event, payload) {
  return {
    v: 2,
    agent: "codex",
    session: resolveSession(),
    pane: resolvePane(),
    plugin: {
      name: "codex-spaces",
      version: pluginVersion,
      capabilities: [
        "lifecycle",
        "usage",
        "context",
        "tools",
        "files",
        "subagents",
        "permissions",
        "rate_limits",
        "otel",
        "sandbox",
        "errors",
      ],
    },
    event: String(event || ""),
    ts_ms: Date.now(),
    pid: process.pid,
    harness_version: "",
    payload: payload && typeof payload === "object" ? payload : {},
  };
}

function frameRequest(token, body) {
  const head =
    `POST ${POST_PATH} HTTP/1.1\r\n` +
    `Host: localhost\r\n` +
    `Content-Type: application/json\r\n` +
    `X-ZJ-Token: ${token}\r\n` +
    `Content-Length: ${Buffer.byteLength(body)}\r\n` +
    `Connection: close\r\n\r\n`;
  return Buffer.concat([Buffer.from(head, "utf8"), Buffer.from(body, "utf8")]);
}

function postOnce(socketPath, isTcp, token, body) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(guard);
      try {
        sock.destroy();
      } catch {
        // ignore
      }
      resolve(ok);
    };
    const guard = setTimeout(() => finish(false), TIMEOUT_MS);
    let sock;
    try {
      sock =
        isTcp === true
          ? net.createConnection({ host: "127.0.0.1", port: socketPath })
          : net.createConnection(socketPath);
    } catch {
      finish(false);
      return;
    }
    sock.setTimeout(TIMEOUT_MS, () => finish(false));
    sock.on("error", () => finish(false));
    let acc = "";
    sock.on("data", (chunk) => {
      acc += chunk.toString("utf8");
      const m = /^HTTP\/\d(?:\.\d)?\s+(\d{3})/m.exec(acc);
      if (m) {
        const code = parseInt(m[1], 10);
        if (code >= 200 && code < 300) finish(true);
        else finish(false);
      }
      if (acc.length > 65536) finish(false);
    });
    sock.on("end", () => {
      const m = /^HTTP\/\d(?:\.\d)?\s+(\d{3})/m.exec(acc);
      if (m) {
        const code = parseInt(m[1], 10);
        finish(code >= 200 && code < 300);
      } else {
        finish(false);
      }
    });
    sock.on("close", () => finish(false));
    const req = frameRequest(token, body);
    sock.on("connect", () => {
      sock.write(req);
    });
  });
}

// POST one envelope to the listener. Resolves true on any 2xx, false
// otherwise (missing/slow socket, bad token, non-2xx). Never throws.
export async function postEnvelope(envelope) {
  let body;
  try {
    body = JSON.stringify(envelope);
  } catch {
    return false;
  }
  const ep = readEndpoint();
  if (!ep) return false;
  if (/[\r\n]/.test(ep.token)) return false;
  try {
    if (await postOnce(ep.socket, false, ep.token, body)) return true;
  } catch {
    // fall through to TCP fallback
  }
  if (ep.tcpPort > 0) {
    try {
      return await postOnce(ep.tcpPort, true, ep.token, body);
    } catch {
      return false;
    }
  }
  return false;
}

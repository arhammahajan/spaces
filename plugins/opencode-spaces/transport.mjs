// transport.mjs — fail-open UDS HTTP/1.1 client for the spaces hook endpoint.
//
// Design goals:
//   - NEVER throw: every failure mode (no socket, refused connection, slow or
//     hanging server) resolves quietly with { delivered: false, reason }.
//   - Fire-and-forget with a tight default timeout (100ms) so the plugin can
//     never slow down OpenCode.
//   - Endpoint discovery across $SPACES_HOOKS_RUNTIME / $XDG_RUNTIME_DIR/spaces
//     / /tmp/spaces-<uid>, with $SPACES_HOOKS_SOCKET as an explicit override.
//   - Silent no-op when $TMUX_PANE is unset unless SPACES_PLUGIN_ALLOW_NO_TMUX=1.

import net from "node:net";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFile } from "node:child_process";

export const HOOK_PATH = "/v1/plugin/opencode";
export const SOCKET_BASENAMES = ["hooks.sock", "spacecore.sock", "spaces.sock"];
export const DEFAULT_TIMEOUT_MS = 100;

function existsSync(p) {
  try {
    fs.statSync(p);
    return true;
  } catch {
    return false;
  }
}

export function num(v, dflt) {
  const n = Number(v);
  return Number.isFinite(n) ? n : dflt;
}

// Ordered list of directories that may contain the spacecore hook socket.
export function discoveryRoots(env = process.env) {
  const roots = [];
  if (env.SPACES_HOOKS_RUNTIME) roots.push(env.SPACES_HOOKS_RUNTIME);
  if (env.XDG_RUNTIME_DIR) roots.push(path.join(env.XDG_RUNTIME_DIR, "spaces"));
  let uid = 0;
  try {
    if (typeof process.getuid === "function") uid = process.getuid();
  } catch {
    uid = 0;
  }
  roots.push(`/tmp/spaces-${uid}`);
  return roots;
}

// Resolve the Unix socket path for the hook endpoint.
// Returns "" when nothing is found (fail-open: the caller must no-op).
export function resolveSocketPath(env = process.env) {
  if (env.SPACES_HOOKS_SOCKET) return env.SPACES_HOOKS_SOCKET;
  const runtime = env.SPACES_HOOKS_RUNTIME;
  // $SPACES_HOOKS_RUNTIME may point directly at the socket file.
  if (runtime && runtime.endsWith(".sock")) return runtime;
  for (const root of discoveryRoots(env)) {
    if (!root) continue;
    if (root.endsWith(".sock")) {
      if (existsSync(root)) return root;
      continue;
    }
    for (const base of SOCKET_BASENAMES) {
      const candidate = path.join(root, base);
      if (existsSync(candidate)) return candidate;
    }
  }
  return "";
}

export function resolveToken(env = process.env) {
  return env.SPACES_HOOKS_TOKEN || env.SPACES_TOKEN || env.ZJ_TOKEN || "";
}

// True when the plugin is allowed to emit (inside tmux, or explicitly allowed
// to run without tmux for tests / headless harnesses).
export function shouldEmit(env = process.env) {
  return Boolean(env.TMUX_PANE || env.SPACES_PLUGIN_ALLOW_NO_TMUX === "1");
}

// Cached tmux session name resolution via `tmux display-message`.
// $SPACES_TMUX_SESSION overrides discovery (useful for tests).
const tmuxCache = new Map();

export function resetTransportCache() {
  tmuxCache.clear();
}

export function getTmuxSession(env = process.env) {
  if (env.SPACES_TMUX_SESSION) return Promise.resolve(env.SPACES_TMUX_SESSION);
  const pane = env.TMUX_PANE || "";
  if (!pane) return Promise.resolve("");
  if (tmuxCache.has(pane)) return Promise.resolve(tmuxCache.get(pane));
  return new Promise((resolve) => {
    execFile(
      "tmux",
      ["display-message", "-p", "-t", pane, "#{session_name}"],
      { timeout: 500 },
      (err, stdout) => {
        let name = "";
        try {
          name = err ? "" : String(stdout || "").trim();
        } catch {
          name = "";
        }
        tmuxCache.set(pane, name);
        resolve(name);
      },
    );
  });
}

function sendOverUds(socketPath, body, token, timeoutMs) {
  return new Promise((resolve) => {
    let sock = null;
    let settled = false;
    const timer = setTimeout(() => {
      finish({ delivered: false, reason: "timeout" });
    }, Math.max(1, timeoutMs));
    // Never hold the event loop open on our own account; the socket does that.
    if (typeof timer.unref === "function") timer.unref();

    function finish(result) {
      if (settled) return;
      settled = true;
      try {
        clearTimeout(timer);
      } catch {
        /* ignore */
      }
      try {
        if (sock) sock.destroy();
      } catch {
        /* ignore */
      }
      resolve(result);
    }

    try {
      sock = net.createConnection(socketPath);
    } catch {
      finish({ delivered: false, reason: "connect-throw" });
      return;
    }
    sock.on("connect", () => {
      try {
        const len = Buffer.byteLength(body);
        let head =
          `POST ${HOOK_PATH} HTTP/1.1\r\n` +
          `Host: localhost\r\n` +
          `Content-Type: application/json\r\n` +
          `Content-Length: ${len}\r\n` +
          `Connection: close\r\n`;
        if (token) head += `X-ZJ-Token: ${token}\r\n`;
        sock.write(head + `\r\n` + body);
      } catch {
        finish({ delivered: false, reason: "write-error" });
      }
    });
    // Any response bytes mean the harness accepted the envelope.
    sock.on("data", () => finish({ delivered: true }));
    sock.on("error", () => finish({ delivered: false, reason: "socket-error" }));
    sock.on("close", () => finish({ delivered: false, reason: "closed" }));
  });
}

// POST a v2 envelope to spacecore. Never throws, never blocks the caller for
// longer than the timeout. Options: { env, pane, socketPath, token, timeoutMs }.
export async function postEnvelope(envelope, opts = {}) {
  const env = opts.env ?? process.env;
  try {
    const pane = opts.pane ?? env.TMUX_PANE ?? "";
    if (!pane && env.SPACES_PLUGIN_ALLOW_NO_TMUX !== "1") {
      return { delivered: false, reason: "no-tmux-pane" };
    }
    const socketPath = opts.socketPath ?? resolveSocketPath(env);
    if (!socketPath) return { delivered: false, reason: "no-socket" };
    const token = opts.token ?? resolveToken(env);
    const timeoutMs =
      opts.timeoutMs ?? num(env.SPACES_HOOKS_TIMEOUT_MS, DEFAULT_TIMEOUT_MS);
    let body;
    try {
      body = JSON.stringify(envelope);
    } catch {
      return { delivered: false, reason: "encode-error" };
    }
    return await sendOverUds(socketPath, body, token, timeoutMs);
  } catch {
    return { delivered: false, reason: "exception" };
  }
}

// Discover the OS tmp dir helper for tests (mkdtemp prefix).
export function defaultTmpPrefix() {
  return path.join(os.tmpdir(), "spaces-test-");
}

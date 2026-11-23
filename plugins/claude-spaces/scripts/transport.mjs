// Shared transport for the claude-spaces bridges.
//
// Fail-open UDS HTTP/1.1 client for pushing v2 envelopes to spacecore.
// Zero runtime dependencies (node builtins only).
//
// Endpoint discovery mirrors the Go spacecore client:
//   1. $SPACES_HOOKS_RUNTIME (env expansion supported)
//   2. $XDG_RUNTIME_DIR/spaces
//   3. <tmpdir>/spaces-<uid>
// and reads hooks.endpoint.json ({version: 1, socket, token}) from it.

import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

export const PLUGIN_NAME = 'claude-spaces';
export const PLUGIN_VERSION = '1.0.0';
export const CAPABILITIES = [
  'lifecycle',
  'usage',
  'context',
  'cost',
  'tools',
  'files',
  'todos',
  'subagents',
  'background_tasks',
  'permissions',
  'questions',
  'compaction',
  'statusline',
  'errors',
];

// Wire path spacecore listens on for Claude Code plugin envelopes.
export const PLUGIN_PATH = '/v1/plugin/claude';

// Total budget (dial + write + first response byte) per push.
export const POST_TIMEOUT_MS = 100;

/** Expand $VAR / ${VAR} references, mirroring Go os.ExpandEnv. */
export function expandEnv(s) {
  return String(s).replace(/\$([A-Za-z_][A-Za-z0-9_]*)|\$\{([^}]+)\}/g, (_, a, b) => {
    const key = a ?? b;
    return process.env[key] ?? '';
  });
}

/** Resolve the hooks runtime directory (never throws). */
export function runtimeDir() {
  try {
    const configured = process.env.SPACES_HOOKS_RUNTIME;
    if (configured) return expandEnv(configured);
    const xdg = process.env.XDG_RUNTIME_DIR;
    if (xdg) return path.join(xdg, 'spaces');
    const uid = typeof process.getuid === 'function' ? process.getuid() : 0;
    return path.join(os.tmpdir(), `spaces-${uid}`);
  } catch {
    return path.join(os.tmpdir(), 'spaces-0');
  }
}

/**
 * Read and validate the listener endpoint document.
 * Returns {socket, token} or null when missing/malformed/version-mismatched.
 */
export function readEndpoint() {
  try {
    const raw = fs.readFileSync(path.join(runtimeDir(), 'hooks.endpoint.json'), 'utf8');
    const doc = JSON.parse(raw);
    if (!doc || typeof doc !== 'object' || Array.isArray(doc)) return null;
    if (doc.version !== 1) return null;
    if (typeof doc.socket !== 'string' || !doc.socket) return null;
    if (typeof doc.token !== 'string' || !doc.token || /[\r\n]/.test(doc.token)) return null;
    return { socket: doc.socket, token: doc.token };
  } catch {
    return null;
  }
}

let sessionCache = null; // null = unresolved; string (maybe '') once resolved

/** Resolve the tmux session name (cached per process). Never throws. */
export function resolveSession() {
  try {
    const override = process.env.SPACES_PLUGIN_SESSION;
    if (override) return override;
  } catch {
    // fall through
  }
  if (sessionCache !== null) return sessionCache;
  let session = '';
  try {
    session = execFileSync('tmux', ['display-message', '-p', '#S'], {
      encoding: 'utf8',
      timeout: 500,
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
  } catch {
    session = '';
  }
  sessionCache = session;
  return session;
}

/** Clear the cached tmux session (used by tests). */
export function clearSessionCache() {
  sessionCache = null;
}

/** Raw $TMUX_PANE value ('' when unset). */
export function resolvePane() {
  try {
    return process.env.TMUX_PANE || '';
  } catch {
    return '';
  }
}

/**
 * Whether telemetry may be sent. Silent no-op when $TMUX_PANE is unset
 * unless SPACES_PLUGIN_ALLOW_NO_TMUX=1 (useful for tests / non-tmux runs).
 */
export function shouldSend() {
  if (resolvePane()) return true;
  try {
    return process.env.SPACES_PLUGIN_ALLOW_NO_TMUX === '1';
  } catch {
    return false;
  }
}

/** Pane value to put in the envelope (never empty). */
export function effectivePane() {
  return resolvePane() || 'no-tmux';
}

/**
 * Build the shared v2 envelope shell around an event payload.
 * harnessVersion is the Claude Code version string when known (statusline).
 */
export function buildEnvelope(event, payload, harnessVersion = '') {
  const envelope = {
    v: 2,
    agent: 'claude',
    session: resolveSession(),
    pane: effectivePane(),
    plugin: {
      name: PLUGIN_NAME,
      version: PLUGIN_VERSION,
      capabilities: [...CAPABILITIES],
    },
    event,
    ts_ms: Date.now(),
    pid: process.pid,
    harness_version: harnessVersion || '',
    payload: payload && typeof payload === 'object' ? payload : {},
  };
  return envelope;
}

/**
 * POST one envelope to spacecore over the unix socket.
 * Resolves true on any 2xx status, false otherwise. Never rejects —
 * every failure mode (missing endpoint, dial error, timeout, bad status)
 * is a silent fail-open false.
 */
export function postEnvelope(envelope, { timeoutMs = POST_TIMEOUT_MS, endpointPath = PLUGIN_PATH } = {}) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => {
      if (!done) {
        done = true;
        resolve(value);
      }
    };

    let endpoint = null;
    try {
      endpoint = readEndpoint();
    } catch {
      endpoint = null;
    }
    if (!endpoint) {
      finish(false);
      return;
    }

    let body;
    try {
      body = Buffer.from(JSON.stringify(envelope));
    } catch {
      finish(false);
      return;
    }

    let socket;
    try {
      socket = net.createConnection({ path: endpoint.socket });
    } catch {
      finish(false);
      return;
    }

    const timer = setTimeout(() => {
      try {
        socket.destroy();
      } catch {
        // ignore
      }
      finish(false);
    }, timeoutMs);
    if (typeof timer.unref === 'function') timer.unref();

    let head = Buffer.alloc(0);
    const cleanup = () => clearTimeout(timer);

    socket.on('error', () => {
      cleanup();
      finish(false);
    });
    socket.on('connect', () => {
      try {
        socket.write(
          `POST ${endpointPath} HTTP/1.1\r\n` +
            'Host: localhost\r\n' +
            'Content-Type: application/json\r\n' +
            `Content-Length: ${body.length}\r\n` +
            `X-ZJ-Token: ${endpoint.token}\r\n` +
            'Connection: close\r\n\r\n',
        );
        socket.write(body);
      } catch {
        cleanup();
        finish(false);
      }
    });
    socket.on('data', (chunk) => {
      head = Buffer.concat([head, chunk]);
      const text = head.toString('latin1');
      const m = text.match(/^HTTP\/\d(?:\.\d)?\s+(\d{3})/);
      if (m) {
        const code = parseInt(m[1], 10);
        cleanup();
        try {
          socket.destroy();
        } catch {
          // ignore
        }
        finish(code >= 200 && code < 300);
      } else if (head.length > 8192) {
        // Not an HTTP status line — give up rather than buffer forever.
        cleanup();
        try {
          socket.destroy();
        } catch {
          // ignore
        }
        finish(false);
      }
    });
    socket.on('close', () => {
      cleanup();
      finish(false);
    });
  });
}

/**
 * Read all of stdin and parse it as JSON.
 * Resolves null on timeout, oversize input, or parse failure. Never rejects.
 */
export function readStdinJson({ timeoutMs = 2000, maxBytes = 1024 * 1024 } = {}) {
  return new Promise((resolve) => {
    let done = false;
    let data = '';
    const finish = (value) => {
      if (!done) {
        done = true;
        resolve(value);
      }
    };
    const cleanup = () => {
      clearTimeout(timer);
      try {
        process.stdin.removeAllListeners('data');
        process.stdin.removeAllListeners('end');
        process.stdin.removeAllListeners('error');
        process.stdin.pause();
      } catch {
        // ignore
      }
    };
    const timer = setTimeout(() => {
      cleanup();
      finish(null);
    }, timeoutMs);
    if (typeof timer.unref === 'function') timer.unref();

    try {
      process.stdin.setEncoding('utf8');
      process.stdin.on('data', (chunk) => {
        data += chunk;
        if (data.length > maxBytes) {
          cleanup();
          finish(null);
        }
      });
      process.stdin.on('end', () => {
        cleanup();
        const trimmed = data.trim();
        if (!trimmed) {
          finish(null);
          return;
        }
        try {
          finish(JSON.parse(trimmed));
        } catch {
          finish(null);
        }
      });
      process.stdin.on('error', () => {
        cleanup();
        finish(null);
      });
      process.stdin.resume();
    } catch {
      cleanup();
      finish(null);
    }
  });
}

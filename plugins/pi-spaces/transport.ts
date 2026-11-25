// transport.ts — fail-open UDS HTTP/1.1 client for the pi-spaces Pi extension.
//
// Sends structured v2 envelopes to spacecore over the hooks runtime Unix
// socket. Never throws, never blocks Pi: 100ms timeout, silent no-op when
// $TMUX_PANE is unset (unless SPACES_PLUGIN_ALLOW_NO_TMUX=1).
//
// Endpoint discovery mirrors the Go/Python hooks core:
//   $SPACES_HOOKS_RUNTIME (with ${VAR} expansion)
//   -> $XDG_RUNTIME_DIR/spaces
//   -> /tmp/spaces-<uid>
// The runtime dir holds hooks.endpoint.json: { socket, token, ... }.
//
// Uses only node:http / node:fs / node:child_process (+ path/os builtins).

import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import { execFileSync } from "node:child_process";

export const PLUGIN_NAME = "pi-spaces";
export const PLUGIN_VERSION = "1.0.0";
export const ENDPOINT_PATH = "/v1/plugin/pi";
export const TIMEOUT_MS = 100;

export const CAPABILITIES = [
  "lifecycle",
  "usage",
  "context",
  "tools",
  "files",
  "compaction",
  "errors",
  "model",
  "thinking",
  "session_tree",
] as const;

export interface Endpoint {
  socket: string;
  token: string;
}

export interface Envelope {
  v: number;
  agent: string;
  session: string;
  pane: string;
  plugin: { name: string; version: string; capabilities: string[] };
  event: string;
  ts_ms: number;
  pid: number;
  harness_version: string;
  payload: Record<string, unknown>;
}

function expandEnv(value: string): string {
  return value.replace(/\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)/g, (_m, braced, plain) => {
    const name: string = braced ?? plain ?? "";
    return process.env[name] ?? "";
  });
}

export function runtimeDir(): string {
  const configured = process.env["SPACES_HOOKS_RUNTIME"];
  if (configured && configured.trim() !== "") {
    return expandEnv(configured.trim());
  }
  const xdg = process.env["XDG_RUNTIME_DIR"];
  if (xdg && xdg.trim() !== "") {
    return path.join(xdg.trim(), "spaces");
  }
  const uid = typeof os.userInfo === "function" ? safeUid() : -1;
  return path.join(os.tmpdir(), `spaces-${uid}`);
}

function safeUid(): number {
  try {
    return os.userInfo().uid;
  } catch {
    return 0;
  }
}

export function endpointFile(): string {
  return path.join(runtimeDir(), "hooks.endpoint.json");
}

function readJsonFile(filePath: string): Record<string, unknown> | null {
  try {
    const raw = fs.readFileSync(filePath, "utf8");
    const parsed: unknown = JSON.parse(raw);
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
    return null;
  } catch {
    return null;
  }
}

export function readEndpoint(): Endpoint | null {
  try {
    const doc = readJsonFile(endpointFile());
    if (!doc) return null;
    const socket = typeof doc["socket"] === "string" ? (doc["socket"] as string) : "";
    const token = typeof doc["token"] === "string" ? (doc["token"] as string) : "";
    if (!socket) return null;
    return { socket, token };
  } catch {
    return null;
  }
}

/** True unless hooks are globally disabled. */
export function isDisabled(): boolean {
  const v = (process.env["SPACES_HOOKS_DISABLE"] ?? "").toLowerCase().trim();
  return v === "1" || v === "true" || v === "yes";
}

/**
 * Silent no-op gate: without a tmux pane there is nothing to attribute the
 * event to, so stay quiet unless tests explicitly opt in.
 */
export function isEnabled(): boolean {
  if (isDisabled()) return false;
  const pane = process.env["TMUX_PANE"] ?? "";
  if (pane !== "") return true;
  return process.env["SPACES_PLUGIN_ALLOW_NO_TMUX"] === "1";
}

export function resolvePane(): string {
  return process.env["TMUX_PANE"] ?? "";
}

let sessionCache: string | null = null;
let sessionCacheKey = "";

export function clearSessionCache(): void {
  sessionCache = null;
  sessionCacheKey = "";
}

function tmuxBinary(): string {
  const v = process.env["TMUX_BIN"];
  return v && v.trim() !== "" ? v : "tmux";
}

/** Resolve the tmux session name. Cached; never throws. */
export function resolveSession(): string {
  try {
    const pane = process.env["TMUX_PANE"] ?? "";
    const override =
      process.env["SPACES_SESSION"] ?? process.env["TMUX_SESSION_NAME"] ?? "";
    const cacheKey = `${pane}\0${override}\0${process.env["TMUX"] ?? ""}`;
    if (sessionCache !== null && cacheKey === sessionCacheKey) return sessionCache;
    sessionCacheKey = cacheKey;

    if (override.trim() !== "") {
      sessionCache = override.trim();
      return sessionCache;
    }
    if (!pane) {
      // No pane: still attribute to *something* when explicitly allowed.
      sessionCache = "unknown";
      return sessionCache;
    }
    try {
      const out = execFileSync(tmuxBinary(), ["display-message", "-p", "-t", pane, "#{session_name}"], {
        timeout: TIMEOUT_MS,
        stdio: ["ignore", "pipe", "ignore"],
        encoding: "utf8",
      } as unknown as { timeout: number });
      const session = String(out ?? "").trim();
      sessionCache = session !== "" ? session : "unknown";
      return sessionCache;
    } catch {
      sessionCache = "unknown";
      return sessionCache;
    }
  } catch {
    return "unknown";
  }
}

/** Build the full v2 envelope (pure; never touches the network). */
export function buildEnvelope(
  event: string,
  payload: Record<string, unknown>,
  opts?: { harnessVersion?: string },
): Envelope {
  return {
    v: 2,
    agent: "pi",
    session: resolveSession(),
    pane: resolvePane(),
    plugin: {
      name: PLUGIN_NAME,
      version: PLUGIN_VERSION,
      capabilities: [...CAPABILITIES],
    },
    event,
    ts_ms: Date.now(),
    pid: process.pid,
    harness_version: opts?.harnessVersion ?? process.env["SPACES_HARNESS_VERSION"] ?? "",
    payload,
  };
}

/**
 * POST one envelope over the UDS. Resolves true on any 2xx, false otherwise.
 * Never rejects.
 */
export function postEnvelope(envelope: Envelope): Promise<boolean> {
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok: boolean): void => {
      if (done) return;
      done = true;
      resolve(ok);
    };
    try {
      if (!isEnabled()) {
        finish(false);
        return;
      }
      const endpoint = readEndpoint();
      if (!endpoint) {
        finish(false);
        return;
      }
      if (/[\r\n]/.test(endpoint.token)) {
        finish(false);
        return;
      }
      let body: string;
      try {
        body = JSON.stringify(envelope);
      } catch {
        finish(false);
        return;
      }
      let req: http.ClientRequest;
      try {
        req = http.request({
          socketPath: endpoint.socket,
          path: ENDPOINT_PATH,
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "Content-Length": Buffer.byteLength(body),
            "X-ZJ-Token": endpoint.token,
            Connection: "close",
            Host: "localhost",
          },
          timeout: TIMEOUT_MS,
        });
      } catch {
        finish(false);
        return;
      }
      const timer = setTimeout(() => {
        try {
          req.destroy(new Error("timeout"));
        } catch {
          /* ignore */
        }
        finish(false);
      }, TIMEOUT_MS);
      // Unref so a slow socket can never hold Pi open.
      try {
        (timer as unknown as { unref?: () => void }).unref?.();
      } catch {
        /* ignore */
      }
      req.on("timeout", () => {
        try {
          req.destroy(new Error("timeout"));
        } catch {
          /* ignore */
        }
        finish(false);
      });
      req.on("error", () => {
        clearTimeout(timer);
        finish(false);
      });
      req.on("response", (res) => {
        const status = res.statusCode ?? 0;
        res.resume();
        res.on("end", () => {
          clearTimeout(timer);
          finish(status >= 200 && status < 300);
        });
        res.on("error", () => {
          clearTimeout(timer);
          finish(false);
        });
      });
      try {
        req.end(body);
      } catch {
        clearTimeout(timer);
        finish(false);
      }
    } catch {
      finish(false);
    }
  });
}

/** Fire-and-forget send. Never throws, never returns a useful value. */
export function send(event: string, payload: Record<string, unknown>, harnessVersion?: string): void {
  try {
    if (!isEnabled()) return;
    const envelope = buildEnvelope(event, payload, { harnessVersion });
    void postEnvelope(envelope).catch(() => undefined);
  } catch {
    /* fail-open: never break the host agent */
  }
}

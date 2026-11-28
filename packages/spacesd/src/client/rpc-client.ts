import { existsSync, readFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  SPACES_CONFIG_DIR_NAME,
  type BackendExecResult,
  type BackendVerb,
  type RpcClientKind,
  type RpcResponse,
} from "@spaces/types";
import type { DaemonEndpointMetadata } from "../server/daemon";

export interface RpcClientOptions {
  spacesDir?: string;
  socketPath?: string;
  token?: string;
  clientKind?: RpcClientKind;
  clientId?: string;
  timeoutMs?: number;
}

function defaultSpacesDir(): string {
  const env = process.env.SPACES_DIR?.trim();
  if (env) {
    return env;
  }
  return path.join(os.homedir(), SPACES_CONFIG_DIR_NAME);
}

/**
 * Reads `~/.spaces/daemon.endpoint.json` if present and valid.
 */
export function readDaemonEndpoint(spacesDir = defaultSpacesDir()): DaemonEndpointMetadata | null {
  const endpointPath = path.join(spacesDir, "daemon.endpoint.json");
  if (!existsSync(endpointPath)) {
    return null;
  }
  try {
    const parsed = JSON.parse(readFileSync(endpointPath, "utf8")) as Partial<DaemonEndpointMetadata>;
    if (typeof parsed.socket !== "string" || typeof parsed.token !== "string") {
      return null;
    }
    return parsed as DaemonEndpointMetadata;
  } catch {
    return null;
  }
}

/**
 * Thin UDS NDJSON-RPC 2.0 client for calling `spacesd` daemon verbs from scripts,
 * `tmux-spaces.tmux`, and tests.
 */
export class SpacesRpcClient {
  private readonly socketPath: string;
  private readonly token: string;
  private readonly clientKind: RpcClientKind;
  private readonly clientId: string;
  private readonly timeoutMs: number;
  private nextId = 1;

  constructor(options: RpcClientOptions = {}) {
    const dir = options.spacesDir ?? defaultSpacesDir();
    const endpoint = readDaemonEndpoint(dir);
    this.socketPath = options.socketPath ?? endpoint?.socket ?? path.join(dir, "daemon.sock");
    this.token = options.token ?? endpoint?.token ?? "";
    this.clientKind = options.clientKind ?? "tmux_frontend";
    this.clientId = options.clientId ?? `tmux-frontend-${process.pid}`;
    this.timeoutMs = options.timeoutMs ?? 5000;
  }

  /**
   * Connects to `daemon.sock`, authenticates via `rpc.auth`, executes one RPC
   * method, and closes the connection.
   */
  async call<T = unknown>(method: string, params?: unknown): Promise<T> {
    if (!existsSync(this.socketPath)) {
      throw new Error(`spacesd socket not found: ${this.socketPath}`);
    }

    const authId = this.nextId++;
    const callId = this.nextId++;
    const token = this.token;
    const clientKind = this.clientKind;
    const clientId = this.clientId;

    return new Promise<T>((resolve, reject) => {
      let buffer = "";
      let settled = false;
      const timer = setTimeout(() => {
        if (!settled) {
          settled = true;
          reject(new Error(`spacesd RPC timeout (${this.timeoutMs}ms) calling ${method}`));
        }
      }, this.timeoutMs);

      const finish = (err?: Error, value?: T) => {
        if (settled) {
          return;
        }
        settled = true;
        clearTimeout(timer);
        if (err) {
          reject(err);
        } else {
          resolve(value as T);
        }
      };

      void Bun.connect({
        unix: this.socketPath,
        socket: {
          open(socket) {
            const authReq = {
              jsonrpc: "2.0",
              id: authId,
              method: RPC_METHODS.AUTH,
              params: {
                token,
                client_kind: clientKind,
                client_id: clientId,
                protocol_version: PROTOCOL_VERSION,
              },
            };
            const verbReq = {
              jsonrpc: "2.0",
              id: callId,
              method,
              params,
            };
            socket.write(`${JSON.stringify(authReq)}\n${JSON.stringify(verbReq)}\n`);
          },
          data(socket, chunk) {
            buffer += Buffer.from(chunk).toString("utf8");
            let idx = buffer.indexOf("\n");
            while (idx !== -1) {
              const line = buffer.slice(0, idx).trim();
              buffer = buffer.slice(idx + 1);
              if (line.length > 0) {
                let msg: RpcResponse;
                try {
                  msg = JSON.parse(line) as RpcResponse;
                } catch {
                  continue;
                }
                if (msg.id === authId && msg.error) {
                  finish(new Error(`spacesd auth failed (${msg.error.code}): ${msg.error.message}`));
                  socket.end();
                  return;
                }
                if (msg.id === callId) {
                  if (msg.error) {
                    finish(
                      new Error(`spacesd RPC error (${msg.error.code}): ${msg.error.message}`),
                    );
                  } else {
                    finish(undefined, msg.result as T);
                  }
                  socket.end();
                  return;
                }
              }
              idx = buffer.indexOf("\n");
            }
          },
          error(_socket, err) {
            finish(err instanceof Error ? err : new Error(String(err)));
          },
          close() {
            finish(new Error(`spacesd closed connection before replying to ${method}`));
          },
        },
      }).catch((err) => {
        finish(err instanceof Error ? err : new Error(String(err)));
      });
    });
  }

  async execVerb(verb: BackendVerb): Promise<BackendExecResult> {
    return this.call<BackendExecResult>(RPC_METHODS.BACKEND_EXEC, { verb });
  }
}

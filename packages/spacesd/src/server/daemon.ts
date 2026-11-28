import { randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import type { Socket, UnixSocketListener } from "bun";
import {
  PROTOCOL_VERSION,
  RPC_METHODS,
  SPACES_CONFIG_DIR_NAME,
  type BackendExecParams,
  type BackendExecResult,
  type BackendHarnessesListResult,
  type BackendSpacesListResult,
  type BootId,
  type DaemonPingResult,
  type DaemonStatusResult,
  type EventPushNotification,
  type EventSubscribeParams,
  type EventSubscribeResult,
  type EventTailParams,
  type EventTailResult,
  type EventUnsubscribeParams,
  type Lease,
  type LeaseClaimParams,
  type LeaseHandoffParams,
  type LeaseResult,
  type LeaseStatusParams,
  type LeaseTransition,
  type LeaseYieldParams,
  type MailboxAckParams,
  type MailboxListParams,
  type MailboxListResult,
  type MailboxMessage,
  type MailboxSendParams,
  type MailboxSendResult,
  type OscResumeHint,
  type PtyOscNotification,
  type RpcAuthParams,
  type RpcAuthResult,
  type RpcError,
  type RpcErrorCode,
  type RpcId,
  type RpcNotification,
  type RpcRequest,
  type RpcResponse,
  type Thread,
  type ThreadCloseParams,
  type ThreadGetParams,
  type ThreadListParams,
  type ThreadListResult,
  type ThreadSpawnParams,
  type ThreadSpawnResult,
  type Turn,
  type TurnCancelParams,
  type TurnSendParams,
  type TurnSendResult,
} from "@spaces/types";
import { BackendService } from "../backend/service";
import { ThreadActorRegistry } from "../core/actor";
import { EventBus, type EventStoreAdapter } from "../core/event-bus";
import {
  generatePaneSecret,
  validateResumeCommand,
  verifyOscResumeHint,
} from "../core/hmac";
import { LeaseError, LeaseMachine, type LeaseHooks, type LeaseStoreAdapter } from "../core/lease-fsm";
import { TmuxDriver } from "../drivers/tmux";
import type { MultiplexerDriver } from "../drivers/types";
import { SpacesStore, type VerifiedResumeHintRecord } from "../store/db";

export const SPACESD_VERSION = "0.1.0" as const;
export const MAX_RPC_FRAME_BYTES = 1024 * 1024; // 1 MiB

const RPC_PARSE_ERROR: RpcErrorCode = -32700;
const RPC_INVALID_REQUEST: RpcErrorCode = -32600;
const RPC_METHOD_NOT_FOUND: RpcErrorCode = -32601;
const RPC_INVALID_PARAMS: RpcErrorCode = -32602;
const RPC_INTERNAL_ERROR: RpcErrorCode = -32603;
const RPC_UNAUTHORIZED: RpcErrorCode = -32001;
const RPC_NOT_FOUND: RpcErrorCode = -32003;

export interface DaemonEndpointMetadata {
  version: number;
  protocol: "ndjson-rpc/uds";
  pid: number;
  uid: number;
  socket: string;
  token: string;
  boot_id: BootId;
  started_ms: number;
}

export interface SpacesDaemonOptions {
  /** Override `~/.spaces` runtime/config directory (useful for tests). */
  spacesDir?: string;
  /** Override SQLite DB path (defaults to `<spacesDir>/spaces.db`). */
  dbPath?: string;
  /** Override UDS path (defaults to `<spacesDir>/daemon.sock`). */
  socketPath?: string;
  /** Override daemon auth token (defaults to 32 random hex chars). */
  token?: string;
  /** Override boot ID (defaults to `boot_<uuid>`). */
  bootId?: BootId;
  /** Bounded per-boot event log retention size (defaults to 10_000). */
  maxEvents?: number;
  /** Optional hooks for lease DRAIN / CANCEL / STOP_AND_FLUSH transitions. */
  leaseHooks?: LeaseHooks;
  /** Optional swappable multiplexer driver (defaults to `TmuxDriver`). */
  driver?: MultiplexerDriver;
  /** Optional custom BackendService instance. */
  backend?: BackendService;
  /** Injectable clock for deterministic tests. */
  now?: () => number;
}

class RpcFailure extends Error {
  readonly code: RpcErrorCode;
  readonly data?: unknown;

  constructor(code: RpcErrorCode, message: string, data?: unknown) {
    super(message);
    this.name = "RpcFailure";
    this.code = code;
    this.data = data;
  }
}

interface ConnectionState {
  id: string;
  authenticated: boolean;
  clientId?: string;
  buffer: string;
  subscriptions: Set<string>;
}

function isProcessAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) {
    return false;
  }
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    // EPERM means the process exists even if owned by another user.
    return code === "EPERM";
  }
}

function constantTimeEquals(a: string, b: string): boolean {
  const bufA = Buffer.from(a, "utf8");
  const bufB = Buffer.from(b, "utf8");
  if (bufA.length !== bufB.length) {
    return false;
  }
  return timingSafeEqual(bufA, bufB);
}

function atomicWriteFile(filePath: string, content: string, mode: number): void {
  const tmpPath = `${filePath}.tmp.${process.pid}.${randomBytes(4).toString("hex")}`;
  writeFileSync(tmpPath, content, { mode });
  chmodSync(tmpPath, mode);
  renameSync(tmpPath, filePath);
}

function defaultSpacesDir(): string {
  const envDir = process.env.SPACES_DIR?.trim();
  if (envDir) {
    return envDir;
  }
  return path.join(os.homedir(), SPACES_CONFIG_DIR_NAME);
}

function splitResumeTargetArgv(target: string): string[] {
  return target
    .trim()
    .split(/\s+/)
    .filter((part) => part.length > 0);
}

/**
 * Minimal TypeScript `spacesd` control-plane daemon:
 * - Unified SQLite WAL store (`~/.spaces/spaces.db`, mode `0600`)
 * - Per-thread serial actors (`ThreadActorRegistry`)
 * - Fenced ownership lease FSM (`LeaseMachine`)
 * - Verified OSC 7577 resume hints + per-pane HMAC-SHA256 tokens
 * - UDS NDJSON-RPC 2.0 server (`~/.spaces/daemon.sock`, mode `0600`)
 */
export class SpacesDaemon {
  readonly spacesDir: string;
  readonly dbPath: string;
  readonly socketPath: string;
  readonly endpointPath: string;
  readonly bootId: BootId;
  readonly token: string;
  readonly startedAtMs: number;

  private readonly now: () => number;
  private readonly store: SpacesStore;
  private readonly backend: BackendService;
  private readonly actors: ThreadActorRegistry;
  private readonly eventBus: EventBus;
  private readonly leaseMachine: LeaseMachine;
  private readonly connections = new Map<Socket<ConnectionState>, ConnectionState>();
  private listener: UnixSocketListener<ConnectionState> | null = null;
  private stopped = false;

  constructor(options: SpacesDaemonOptions = {}) {
    this.now = options.now ?? (() => Date.now());
    this.spacesDir = options.spacesDir ?? defaultSpacesDir();
    this.dbPath = options.dbPath ?? path.join(this.spacesDir, "spaces.db");
    this.socketPath = options.socketPath ?? path.join(this.spacesDir, "daemon.sock");
    this.endpointPath = path.join(this.spacesDir, "daemon.endpoint.json");
    this.bootId = options.bootId ?? `boot_${randomUUID()}`;
    this.token = options.token ?? randomBytes(16).toString("hex");
    this.startedAtMs = this.now();

    if (this.token.includes("\r") || this.token.includes("\n")) {
      throw new Error("Daemon auth token must not contain newlines");
    }

    mkdirSync(this.spacesDir, { recursive: true, mode: 0o700 });
    try {
      chmodSync(this.spacesDir, 0o700);
    } catch {
      // Best-effort on filesystems that ignore chmod.
    }

    this.store = new SpacesStore(this.dbPath, { now: this.now });
    this.backend =
      options.backend ??
      new BackendService(options.driver ?? new TmuxDriver(), this.store, {
        spacesDir: this.spacesDir,
        now: this.now,
      });
    this.actors = new ThreadActorRegistry();

    const eventAdapter: EventStoreAdapter = {
      nextSeq: (bootId) => this.store.nextSeq(bootId),
      appendEvent: (envelope) => this.store.appendEvent(envelope),
      getEventBounds: (bootId) => {
        const bounds = this.store.getEventBounds(bootId);
        if (bounds.earliestSeq === null || bounds.latestSeq === null) {
          return null;
        }
        return { earliestSeq: bounds.earliestSeq, latestSeq: bounds.latestSeq };
      },
      listEventsAfter: (bootId, afterSeq, limit, filter) =>
        this.store.listEventsAfter(bootId, afterSeq, limit, filter),
      trimEvents: (bootId, maxRows) => {
        this.store.trimEvents(bootId, maxRows);
      },
    };

    this.eventBus = new EventBus(eventAdapter, {
      bootId: this.bootId,
      maxEvents: options.maxEvents,
      now: this.now,
    });

    const leaseAdapter: LeaseStoreAdapter = {
      getLease: (threadId) => this.store.getLease(threadId),
      commitLeaseTransition: (lease: Lease, transition: LeaseTransition) => {
        this.store.applyLeaseTransition(lease, transition);
        this.eventBus.publish({
          threadId: lease.thread_id,
          epoch: lease.epoch,
          tsMs: transition.ts_ms,
          payload: {
            type: "lease_changed",
            lease,
            transition,
          },
        });
      },
      refreshLease: (lease: Lease) => {
        this.store.upsertLease(lease);
      },
    };

    this.leaseMachine = new LeaseMachine(leaseAdapter, {
      hooks: options.leaseHooks,
      now: this.now,
    });
  }

  getStore(): SpacesStore {
    return this.store;
  }

  getBackend(): BackendService {
    return this.backend;
  }

  getActors(): ThreadActorRegistry {
    return this.actors;
  }

  getEventBus(): EventBus {
    return this.eventBus;
  }

  getLeaseMachine(): LeaseMachine {
    return this.leaseMachine;
  }

  /**
   * Issues and persists a fresh 256-bit HMAC-SHA256 secret for a pane/PTY.
   */
  issuePaneHmacToken(paneId: string, threadId?: string): string {
    const secret = generatePaneSecret();
    this.store.upsertPaneToken(paneId, secret, threadId, this.now());
    return secret;
  }

  /**
   * Verifies an OSC 7577 resume hint's per-pane HMAC-SHA256 signature and
   * validates its resume command before persisting it into `verified_resume_hints`.
   */
  verifyAndStoreOscResumeHint(
    paneId: string,
    hint: OscResumeHint,
  ): { ok: true; record: VerifiedResumeHintRecord } | { ok: false; reason: string } {
    const tokenRecord = this.store.getPaneToken(paneId);
    if (!tokenRecord || tokenRecord.revoked_at_ms !== undefined) {
      return { ok: false, reason: "missing or revoked pane HMAC token" };
    }
    if (!verifyOscResumeHint(tokenRecord.secret_hex, paneId, hint)) {
      return { ok: false, reason: "invalid OSC 7577 HMAC signature" };
    }

    let validatedArgv: string[] | undefined;
    if (hint.resume_target && hint.resume_target.trim().length > 0) {
      const argv = splitResumeTargetArgv(hint.resume_target);
      const checked = validateResumeCommand({
        agent: hint.agent || undefined,
        argv,
      });
      if (!checked.ok) {
        return { ok: false, reason: `unsafe resume command: ${checked.reason}` };
      }
      validatedArgv = checked.argv;
    }

    const nowMs = this.now();
    const record = this.store.upsertVerifiedResumeHint({
      pane_id: paneId,
      thread_id: hint.thread_id ?? tokenRecord.thread_id,
      agent: hint.agent,
      session_id: hint.session_id,
      profile: hint.profile,
      cwd: hint.cwd,
      resume_target: hint.resume_target,
      resume_cmd: validatedArgv,
      signature_hex: hint.signature!,
      verified_at_ms: nowMs,
      updated_at_ms: nowMs,
    });

    this.eventBus.publish({
      threadId: record.thread_id,
      tsMs: nowMs,
      payload: {
        type: "osc_resume_hint_received",
        pane_id: paneId,
        hint,
      },
    });

    return { ok: true, record };
  }

  /**
   * Starts listening on the Unix domain socket (`daemon.sock`, mode `0600`) and
   * atomically publishes `daemon.endpoint.json` (`0600`).
   *
   * Refuses to unlink an existing socket if the recorded endpoint PID is still alive.
   */
  start(): DaemonEndpointMetadata {
    if (this.listener) {
      return this.endpointMetadata();
    }
    this.stopped = false;
    this.checkAndCleanStaleSocket();

    const daemon = this;
    this.listener = Bun.listen<ConnectionState>({
      unix: this.socketPath,
      socket: {
        open(socket) {
          const state: ConnectionState = {
            id: `conn_${randomUUID()}`,
            authenticated: false,
            buffer: "",
            subscriptions: new Set(),
          };
          socket.data = state;
          daemon.connections.set(socket, state);
        },
        data(socket, chunk) {
          daemon.onSocketData(socket, chunk);
        },
        close(socket) {
          daemon.onSocketClose(socket);
        },
        error(socket) {
          daemon.onSocketClose(socket);
        },
      },
    });

    try {
      chmodSync(this.socketPath, 0o600);
    } catch {
      // Best-effort on filesystems that ignore socket chmod.
    }

    const endpoint = this.endpointMetadata();
    atomicWriteFile(this.endpointPath, JSON.stringify(endpoint, null, 2) + "\n", 0o600);
    return endpoint;
  }

  /**
   * Gracefully shuts down the UDS listener, drains all per-thread actors,
   * checkpoints SQLite WAL (`TRUNCATE`), closes the DB, and removes socket/endpoint files.
   */
  async stop(): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.stopped = true;

    for (const [socket, state] of this.connections.entries()) {
      for (const subId of state.subscriptions) {
        this.eventBus.unsubscribe(subId);
      }
      state.subscriptions.clear();
      try {
        socket.end();
      } catch {
        // Ignore already-closed sockets.
      }
    }
    this.connections.clear();

    if (this.listener) {
      this.listener.stop(true);
      this.listener = null;
    }

    await this.actors.drainAll();
    this.store.close(true);

    safeUnlink(this.socketPath);
    safeUnlink(this.endpointPath);
  }

  private endpointMetadata(): DaemonEndpointMetadata {
    return {
      version: PROTOCOL_VERSION,
      protocol: "ndjson-rpc/uds",
      pid: process.pid,
      uid: typeof process.getuid === "function" ? process.getuid() : 0,
      socket: this.socketPath,
      token: this.token,
      boot_id: this.bootId,
      started_ms: this.startedAtMs,
    };
  }

  private checkAndCleanStaleSocket(): void {
    if (existsSync(this.endpointPath)) {
      try {
        const raw = readFileSync(this.endpointPath, "utf8");
        const parsed = JSON.parse(raw) as Partial<DaemonEndpointMetadata>;
        if (typeof parsed.pid === "number" && parsed.pid !== process.pid && isProcessAlive(parsed.pid)) {
          throw new Error(
            `spacesd is already running with pid ${parsed.pid} on ${parsed.socket ?? this.socketPath}`,
          );
        }
      } catch (err) {
        if (err instanceof Error && err.message.startsWith("spacesd is already running")) {
          throw err;
        }
        // Malformed stale endpoint file; safe to replace.
      }
    }

    safeUnlink(this.socketPath);
    safeUnlink(this.endpointPath);
  }

  private onSocketClose(socket: Socket<ConnectionState>): void {
    const state = this.connections.get(socket);
    if (!state) {
      return;
    }
    for (const subId of state.subscriptions) {
      this.eventBus.unsubscribe(subId);
    }
    state.subscriptions.clear();
    this.connections.delete(socket);
  }

  private onSocketData(socket: Socket<ConnectionState>, chunk: Buffer | Uint8Array): void {
    const state = this.connections.get(socket);
    if (!state) {
      return;
    }
    state.buffer += Buffer.from(chunk).toString("utf8");

    if (Buffer.byteLength(state.buffer, "utf8") > MAX_RPC_FRAME_BYTES) {
      this.sendError(socket, 0, RPC_INVALID_REQUEST, "RPC frame exceeds 1 MiB limit");
      state.buffer = "";
      socket.end();
      return;
    }

    let newlineIdx = state.buffer.indexOf("\n");
    while (newlineIdx !== -1) {
      const line = state.buffer.slice(0, newlineIdx).trim();
      state.buffer = state.buffer.slice(newlineIdx + 1);
      if (line.length > 0) {
        void this.handleLine(socket, state, line);
      }
      newlineIdx = state.buffer.indexOf("\n");
    }
  }

  private async handleLine(
    socket: Socket<ConnectionState>,
    state: ConnectionState,
    line: string,
  ): Promise<void> {
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      this.sendError(socket, 0, RPC_PARSE_ERROR, "Invalid JSON");
      return;
    }

    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      this.sendError(socket, 0, RPC_INVALID_REQUEST, "JSON-RPC frame must be an object");
      return;
    }

    const frame = parsed as Partial<RpcRequest & RpcNotification>;
    if (frame.jsonrpc !== "2.0" || typeof frame.method !== "string") {
      this.sendError(
        socket,
        frame.id ?? 0,
        RPC_INVALID_REQUEST,
        "Expected JSON-RPC 2.0 request or notification",
      );
      return;
    }

    const isNotification = frame.id === undefined;

    try {
      const result = await this.dispatch(socket, state, frame.method, frame.params);
      if (!isNotification) {
        this.sendResult(socket, frame.id as RpcId, result);
      }
    } catch (err) {
      if (isNotification) {
        return;
      }
      if (err instanceof RpcFailure || err instanceof LeaseError) {
        this.sendError(socket, frame.id as RpcId, err.code, err.message, err.data);
        return;
      }
      const message = err instanceof Error ? err.message : String(err);
      this.sendError(socket, frame.id as RpcId, RPC_INTERNAL_ERROR, message);
    }
  }

  private async dispatch(
    socket: Socket<ConnectionState>,
    state: ConnectionState,
    method: string,
    params: unknown,
  ): Promise<unknown> {
    if (method === RPC_METHODS.DAEMON_PING) {
      const res: DaemonPingResult = {
        ok: true,
        boot_id: this.bootId,
        ts_ms: this.now(),
      };
      return res;
    }

    if (method === RPC_METHODS.AUTH) {
      const p = requireObject<RpcAuthParams>(params, "rpc.auth params required");
      if (
        typeof p.token !== "string" ||
        p.token.includes("\r") ||
        p.token.includes("\n") ||
        !constantTimeEquals(p.token, this.token)
      ) {
        throw new RpcFailure(RPC_UNAUTHORIZED, "Invalid authentication token");
      }
      if (p.protocol_version !== PROTOCOL_VERSION) {
        throw new RpcFailure(
          RPC_INVALID_PARAMS,
          `Unsupported protocol_version ${String(p.protocol_version)}; expected ${PROTOCOL_VERSION}`,
        );
      }
      const clientId = p.client_id ?? state.id;
      state.authenticated = true;
      state.clientId = clientId;
      const bounds = this.store.getEventBounds(this.bootId);
      const res: RpcAuthResult = {
        ok: true,
        boot_id: this.bootId,
        protocol_version: PROTOCOL_VERSION,
        client_id: clientId,
        current_seq: bounds.latestSeq ?? 0,
      };
      return res;
    }

    if (!state.authenticated) {
      throw new RpcFailure(RPC_UNAUTHORIZED, "Connection is not authenticated; call rpc.auth first");
    }

    switch (method) {
      case RPC_METHODS.DAEMON_STATUS: {
        const nowMs = this.now();
        const bounds = this.store.getEventBounds(this.bootId);
        const activeThreads = this.store
          .listThreads()
          .filter((t) => t.state !== "closed" && t.state !== "failed").length;
        const res: DaemonStatusResult = {
          boot_id: this.bootId,
          protocol_version: PROTOCOL_VERSION,
          pid: process.pid,
          started_at_ms: this.startedAtMs,
          uptime_ms: Math.max(0, nowMs - this.startedAtMs),
          current_seq: bounds.latestSeq ?? 0,
          active_threads: activeThreads,
          active_ptys: 0,
          connected_clients: this.connections.size,
        };
        return res;
      }

      case RPC_METHODS.THREAD_SPAWN: {
        const p = requireObject<ThreadSpawnParams>(params, "thread.spawn params required");
        if (!p.agent || !p.cwd) {
          throw new RpcFailure(RPC_INVALID_PARAMS, "thread.spawn requires agent and cwd");
        }
        const nowMs = this.now();
        const threadId = `thr_${randomUUID()}`;
        const thread: Thread = {
          id: threadId,
          ...(p.space ? { space: p.space } : {}),
          ...(p.window_id ? { window_id: p.window_id } : {}),
          ...(p.pane_id ? { pane_id: p.pane_id } : {}),
          agent: p.agent,
          ...(p.profile ? { profile: p.profile } : {}),
          driver: p.driver ?? "native_pty",
          kind: p.kind ?? "interactive",
          state: "created",
          cwd: p.cwd,
          canonical_cwd: path.resolve(p.cwd),
          ...(p.worktree_path ? { worktree_path: p.worktree_path } : {}),
          ...(p.vendor_session_id ? { vendor_session_id: p.vendor_session_id } : {}),
          ...(p.title ? { title: p.title } : {}),
          ...(p.model ? { model: p.model } : {}),
          epoch: 0,
          created_at_ms: nowMs,
          updated_at_ms: nowMs,
        };
        const initialLease: Lease = {
          id: `lease_${randomUUID()}`,
          thread_id: threadId,
          epoch: 0,
          state: "unowned",
          updated_at_ms: nowMs,
        };
        this.store.transaction(() => {
          this.store.createThread(thread);
          this.store.upsertLease(initialLease);
        });
        this.eventBus.publish({
          threadId,
          epoch: 0,
          tsMs: nowMs,
          payload: { type: "thread_updated", thread },
        });
        const res: ThreadSpawnResult = { thread };
        return res;
      }

      case RPC_METHODS.THREAD_LIST: {
        const p = (params ?? {}) as ThreadListParams;
        const threads = this.store.listThreads(p);
        const res: ThreadListResult = { threads };
        return res;
      }

      case RPC_METHODS.THREAD_GET: {
        const p = requireObject<ThreadGetParams>(params, "thread.get params required");
        const thread = this.store.getThread(p.thread_id);
        if (!thread) {
          throw new RpcFailure(RPC_NOT_FOUND, `Thread not found: ${p.thread_id}`);
        }
        return { thread };
      }

      case RPC_METHODS.THREAD_CLOSE: {
        const p = requireObject<ThreadCloseParams>(params, "thread.close params required");
        return this.actors.enqueue(p.thread_id, async () => {
          const thread = this.store.getThread(p.thread_id);
          if (!thread) {
            throw new RpcFailure(RPC_NOT_FOUND, `Thread not found: ${p.thread_id}`);
          }
          const lease = this.store.getLease(p.thread_id);
          if (lease && lease.state !== "unowned") {
            await this.leaseMachine.cancel({
              threadId: p.thread_id,
              expectedEpoch: p.epoch,
              reason: "cancel",
            });
          }
          const updated = this.store.updateThread(p.thread_id, {
            state: "closed",
            active_turn_id: null,
            updated_at_ms: this.now(),
          });
          if (updated) {
            this.eventBus.publish({
              threadId: updated.id,
              epoch: updated.epoch,
              tsMs: updated.updated_at_ms,
              payload: { type: "thread_updated", thread: updated },
            });
          }
          return { ok: true, thread: updated };
        });
      }

      case RPC_METHODS.TURN_SEND: {
        const p = requireObject<TurnSendParams>(params, "turn.send params required");
        if (!p.thread_id || typeof p.epoch !== "number" || typeof p.prompt !== "string") {
          throw new RpcFailure(RPC_INVALID_PARAMS, "turn.send requires thread_id, epoch, and prompt");
        }
        return this.actors.enqueue(p.thread_id, () => {
          const thread = this.store.getThread(p.thread_id);
          if (!thread) {
            throw new RpcFailure(RPC_NOT_FOUND, `Thread not found: ${p.thread_id}`);
          }
          this.leaseMachine.assertFenced(p.thread_id, p.epoch);
          const nowMs = this.now();
          const turn: Turn = {
            id: `turn_${randomUUID()}`,
            thread_id: p.thread_id,
            epoch: p.epoch,
            origin: p.origin,
            state: "running",
            prompt: p.prompt,
            ...(p.model ? { model: p.model } : {}),
            started_at_ms: nowMs,
          };
          this.store.transaction(() => {
            this.store.createTurn(turn);
            this.store.updateThread(p.thread_id, {
              state: "active",
              active_turn_id: turn.id,
              updated_at_ms: nowMs,
            });
          });
          this.eventBus.publish({
            threadId: p.thread_id,
            epoch: p.epoch,
            tsMs: nowMs,
            payload: { type: "turn_updated", turn },
          });
          const res: TurnSendResult = { turn };
          return res;
        });
      }

      case RPC_METHODS.TURN_CANCEL: {
        const p = requireObject<TurnCancelParams>(params, "turn.cancel params required");
        return this.actors.enqueue(p.thread_id, () => {
          const turn = this.store.getTurn(p.turn_id);
          if (!turn || turn.thread_id !== p.thread_id) {
            throw new RpcFailure(RPC_NOT_FOUND, `Turn not found: ${p.turn_id}`);
          }
          if (p.epoch !== undefined) {
            this.leaseMachine.assertFenced(p.thread_id, p.epoch);
          }
          const nowMs = this.now();
          const updated = this.store.updateTurn(p.turn_id, {
            state: "cancelled",
            ended_at_ms: nowMs,
          });
          this.store.updateThread(p.thread_id, {
            active_turn_id: null,
            state: "idle",
            updated_at_ms: nowMs,
          });
          if (updated) {
            this.eventBus.publish({
              threadId: p.thread_id,
              epoch: updated.epoch,
              tsMs: nowMs,
              payload: { type: "turn_updated", turn: updated },
            });
          }
          return { turn: updated };
        });
      }

      case RPC_METHODS.LEASE_CLAIM: {
        const p = requireObject<LeaseClaimParams>(params, "lease.claim params required");
        return this.actors.enqueue(p.thread_id, async () => {
          this.requireThread(p.thread_id);
          const result = await this.leaseMachine.claim({
            threadId: p.thread_id,
            owner: p.owner,
            expectedEpoch: p.expected_epoch,
            ttlMs: p.ttl_ms,
            reason: p.reason,
          });
          const res: LeaseResult = {
            lease: result.lease,
            ...(result.transitions.length > 0
              ? { transition: result.transitions[result.transitions.length - 1] }
              : {}),
          };
          return res;
        });
      }

      case RPC_METHODS.LEASE_YIELD: {
        const p = requireObject<LeaseYieldParams>(params, "lease.yield params required");
        return this.actors.enqueue(p.thread_id, async () => {
          this.requireThread(p.thread_id);
          const result = await this.leaseMachine.yield({
            threadId: p.thread_id,
            owner: p.owner,
            epoch: p.epoch,
            reason: p.reason,
          });
          const res: LeaseResult = {
            lease: result.lease,
            ...(result.transitions.length > 0
              ? { transition: result.transitions[result.transitions.length - 1] }
              : {}),
          };
          return res;
        });
      }

      case RPC_METHODS.LEASE_HANDOFF: {
        const p = requireObject<LeaseHandoffParams>(params, "lease.handoff params required");
        return this.actors.enqueue(p.thread_id, async () => {
          this.requireThread(p.thread_id);
          const result = await this.leaseMachine.handoff({
            threadId: p.thread_id,
            targetOwner: p.to_owner,
            expectedEpoch: p.from_epoch,
            force: p.cancel_active_turn ?? false,
            reason: p.reason,
            ttlMs: p.ttl_ms,
          });
          const res: LeaseResult = {
            lease: result.lease,
            ...(result.transitions.length > 0
              ? { transition: result.transitions[result.transitions.length - 1] }
              : {}),
          };
          return res;
        });
      }

      case RPC_METHODS.LEASE_STATUS: {
        const p = requireObject<LeaseStatusParams>(params, "lease.status params required");
        const lease = this.store.getLease(p.thread_id);
        if (!lease) {
          throw new RpcFailure(RPC_NOT_FOUND, `Lease not found for thread: ${p.thread_id}`);
        }
        const res: LeaseResult = { lease };
        return res;
      }

      case RPC_METHODS.MAILBOX_SEND: {
        const p = requireObject<MailboxSendParams>(params, "mailbox.send params required");
        return this.actors.enqueue(p.thread_id, () => {
          this.requireThread(p.thread_id);
          const nowMs = this.now();
          const msg: MailboxMessage = {
            id: `msg_${randomUUID()}`,
            thread_id: p.thread_id,
            from: p.from,
            kind: p.kind,
            priority: p.priority,
            status: "pending",
            ...(p.dedup_key ? { dedup_key: p.dedup_key } : {}),
            ...(p.correlation_id ? { correlation_id: p.correlation_id } : {}),
            payload: p.payload,
            created_at_ms: nowMs,
            ...(p.expires_at_ms !== undefined ? { expires_at_ms: p.expires_at_ms } : {}),
          };
          const stored = this.store.enqueueMailbox(msg);
          const deduplicated = stored.id !== msg.id;
          if (!deduplicated) {
            this.eventBus.publish({
              threadId: stored.thread_id,
              tsMs: nowMs,
              payload: { type: "mailbox_enqueued", message: stored },
            });
          }
          const res: MailboxSendResult = { message: stored, deduplicated };
          return res;
        });
      }

      case RPC_METHODS.MAILBOX_LIST: {
        const p = requireObject<MailboxListParams>(params, "mailbox.list params required");
        const messages = this.store.listMailbox(p.thread_id, p.status, p.limit);
        const res: MailboxListResult = { messages };
        return res;
      }

      case RPC_METHODS.MAILBOX_ACK: {
        const p = requireObject<MailboxAckParams>(params, "mailbox.ack params required");
        return this.actors.enqueue(p.thread_id, () => {
          const status = p.status ?? "acknowledged";
          const updated = this.store.ackMailbox(p.message_id, status, this.now());
          if (!updated) {
            throw new RpcFailure(RPC_NOT_FOUND, `Mailbox message not found: ${p.message_id}`);
          }
          this.eventBus.publish({
            threadId: updated.thread_id,
            tsMs: this.now(),
            payload: {
              type: "mailbox_updated",
              message_id: updated.id,
              status: updated.status,
            },
          });
          return { message: updated };
        });
      }

      case RPC_METHODS.EVENT_SUBSCRIBE: {
        const p = requireObject<EventSubscribeParams>(params, "event.subscribe params required");
        const sub = this.eventBus.subscribe(p.filter, (notification: EventPushNotification) => {
          this.sendNotification(socket, RPC_METHODS.EVENT_PUSH, notification);
        });
        state.subscriptions.add(sub.subscriptionId);

        // Schedule replay / gap-reload notifications right after the subscribe response.
        queueMicrotask(() => {
          if (!this.connections.has(socket) || !state.subscriptions.has(sub.subscriptionId)) {
            return;
          }
          if (sub.gapReload) {
            this.sendNotification(socket, RPC_METHODS.EVENT_PUSH, {
              subscription_id: sub.subscriptionId,
              event: sub.gapReload,
            } satisfies EventPushNotification);
          }
          for (const ev of sub.replay) {
            this.sendNotification(socket, RPC_METHODS.EVENT_PUSH, {
              subscription_id: sub.subscriptionId,
              event: ev,
            } satisfies EventPushNotification);
          }
        });

        const res: EventSubscribeResult = {
          subscription_id: sub.subscriptionId,
          cursor: sub.cursor,
        };
        return res;
      }

      case RPC_METHODS.EVENT_UNSUBSCRIBE: {
        const p = requireObject<EventUnsubscribeParams>(
          params,
          "event.unsubscribe params required",
        );
        state.subscriptions.delete(p.subscription_id);
        const ok = this.eventBus.unsubscribe(p.subscription_id);
        return { ok };
      }

      case RPC_METHODS.EVENT_TAIL: {
        const p = requireObject<EventTailParams>(params, "event.tail params required");
        const tailed = this.eventBus.tail(p.filter, p.limit);
        const res: EventTailResult = {
          events: tailed.events,
          cursor: tailed.cursor,
          has_more: tailed.hasMore,
        };
        return res;
      }

      case RPC_METHODS.BACKEND_EXEC: {
        const p = requireObject<BackendExecParams>(params, "backend.exec params required");
        if (!p.verb || typeof p.verb !== "object") {
          throw new RpcFailure(RPC_INVALID_PARAMS, "backend.exec requires verb");
        }
        const result = await this.backend.execVerb(p.verb);
        const res: BackendExecResult = { result };
        return res;
      }

      case RPC_METHODS.BACKEND_SPACES_LIST: {
        const spaces = await this.backend.loadSpaces();
        const res: BackendSpacesListResult = { spaces };
        return res;
      }

      case RPC_METHODS.BACKEND_HARNESSES_LIST: {
        const harnesses = this.backend.loadHarnesses();
        const res: BackendHarnessesListResult = { harnesses };
        return res;
      }

      case "resurrect.autosave": {
        const force = Boolean(
          params && typeof params === "object" && (params as { force?: boolean }).force,
        );
        return this.backend.autosaveSpaces(force);
      }

      case "resurrect.auto_restore": {
        return this.backend.autoRestoreSpaces();
      }

      case RPC_METHODS.PTY_OSC: {
        const p = requireObject<PtyOscNotification>(params, "pty.osc params required");
        if (p.status) {
          this.eventBus.publish({
            tsMs: p.ts_ms,
            payload: {
              type: "osc_status_received",
              pty_id: p.pty_id,
              status: p.status,
            },
          });
        }
        if (p.resume_hint) {
          const verified = this.verifyAndStoreOscResumeHint(p.pty_id, p.resume_hint);
          if (!verified.ok) {
            throw new RpcFailure(RPC_UNAUTHORIZED, verified.reason);
          }
          return { ok: true, verified: true };
        }
        return { ok: true };
      }

      default:
        throw new RpcFailure(RPC_METHOD_NOT_FOUND, `Method not found: ${method}`);
    }
  }

  private requireThread(threadId: string): Thread {
    const thread = this.store.getThread(threadId);
    if (!thread) {
      throw new RpcFailure(RPC_NOT_FOUND, `Thread not found: ${threadId}`);
    }
    return thread;
  }

  private sendResult(socket: Socket<ConnectionState>, id: RpcId, result: unknown): void {
    const resp: RpcResponse = { jsonrpc: "2.0", id, result };
    this.writeJsonLine(socket, resp);
  }

  private sendError(
    socket: Socket<ConnectionState>,
    id: RpcId,
    code: RpcErrorCode,
    message: string,
    data?: unknown,
  ): void {
    const error: RpcError = data !== undefined ? { code, message, data } : { code, message };
    const resp: RpcResponse = { jsonrpc: "2.0", id, error };
    this.writeJsonLine(socket, resp);
  }

  private sendNotification(
    socket: Socket<ConnectionState>,
    method: string,
    params: unknown,
  ): void {
    const notif: RpcNotification = { jsonrpc: "2.0", method, params };
    this.writeJsonLine(socket, notif);
  }

  private writeJsonLine(socket: Socket<ConnectionState>, payload: unknown): void {
    try {
      socket.write(`${JSON.stringify(payload)}\n`);
    } catch {
      // Socket closed concurrently.
    }
  }
}

function requireObject<T>(value: unknown, message: string): T {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new RpcFailure(RPC_INVALID_PARAMS, message);
  }
  return value as T;
}

function safeUnlink(filePath: string): void {
  try {
    unlinkSync(filePath);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "ENOENT") {
      // Ignore cleanup errors during shutdown.
    }
  }
}

import { describe, expect, test } from "bun:test";
import {
  OSC_RESUME_HINT_CODE,
  OSC_STATUS_CODE,
  PROTOCOL_VERSION,
  PTY_REPLAY_RING_BYTES,
  RPC_METHODS,
  SPACES_CONFIG_DIR_NAME,
  type BackendVerb,
  type EventEnvelope,
  type Lease,
  type MailboxMessage,
  type OscResumeHint,
  type OscStatus,
  type PtyOutputNotification,
  type PtyResizeParams,
  type RpcRequest,
  type Space,
  type TelemetrySnapshot,
  type Thread,
  type Turn,
} from "./index";

describe("@spaces/types contracts", () => {
  test("protocol constants match Rust crate", () => {
    expect(PROTOCOL_VERSION).toBe(1);
    expect(OSC_STATUS_CODE).toBe(7501);
    expect(OSC_RESUME_HINT_CODE).toBe(7577);
    expect(PTY_REPLAY_RING_BYTES).toBe(1_048_576);
    expect(SPACES_CONFIG_DIR_NAME).toBe(".spaces");
    expect(RPC_METHODS.PTY_OUTPUT).toBe("pty.output");
    expect(RPC_METHODS.PTY_RESIZE).toBe("pty.resize");
    expect(RPC_METHODS.BACKEND_EXEC).toBe("backend.exec");
  });

  test("core unified, OSC, PTY, and domain types compile with snake_case JSON", () => {
    const oscStatus: OscStatus = {
      state: "working",
      app: "claude",
      msg: "running tests",
    };

    const resumeHint: OscResumeHint = {
      agent: "claude",
      session_id: "sess-123",
      cwd: "/repo",
    };

    const ptyOutput: PtyOutputNotification = {
      pty_id: "pty-1",
      offset: 4096,
      encoding: "base64",
      data_b64: "aGVsbG8K",
      ts_ms: 1_700_000_000_000,
    };

    const ptyResize: PtyResizeParams = {
      pty_id: "pty-1",
      size: { cols: 120, rows: 40 },
    };

    const verb: BackendVerb = {
      verb: "open_space",
      name: "spaces",
    };

    const lease: Lease = {
      id: "lease-1",
      thread_id: "thr-1",
      epoch: 2,
      state: "owned",
      owner: { kind: "human", id: "tty-1" },
      updated_at_ms: 1_700_000_000_000,
    };

    const thread: Thread = {
      id: "thr-1",
      agent: "claude",
      driver: "claude_sdk",
      kind: "hybrid",
      state: "active",
      cwd: "/repo",
      canonical_cwd: "/repo",
      epoch: 2,
      created_at_ms: 1_700_000_000_000,
      updated_at_ms: 1_700_000_000_000,
    };

    const turn: Turn = {
      id: "turn-1",
      thread_id: "thr-1",
      epoch: 2,
      origin: "human",
      state: "running",
      started_at_ms: 1_700_000_000_000,
    };

    const mailbox: MailboxMessage = {
      id: "msg-1",
      thread_id: "thr-1",
      from: { role: "coordinator" },
      kind: "prompt",
      priority: "normal",
      status: "pending",
      payload: { text: "hello" },
      created_at_ms: 1_700_000_000_000,
    };

    const event: EventEnvelope = {
      id: "evt-1",
      boot_id: "boot-1",
      seq: 1,
      ts_ms: 1_700_000_000_000,
      thread_id: "thr-1",
      epoch: 2,
      payload: {
        type: "osc_status_received",
        pty_id: "pty-1",
        status: oscStatus,
      },
    };

    const req: RpcRequest = {
      jsonrpc: "2.0",
      id: 1,
      method: RPC_METHODS.BACKEND_EXEC,
      params: { verb },
    };

    const space: Space = {
      name: "spaces",
      id: "$0",
      is_current: true,
      windows: [],
      panes: [],
      cwd: "/repo",
      git: null,
      worktree_path: "",
      worktree_name: "",
      worktree_base: "",
      worktree_managed: false,
      counts: {
        needs: 0,
        failed: 0,
        working: 1,
        done: 0,
        quiet: 0,
        new_output: 0,
        idle: 0,
      },
      status: "working",
      connected_clients: 1,
      created_at_ms: 1_700_000_000_000,
    };

    const telemetry: TelemetrySnapshot = {
      context: {
        tokens_per_min: 100,
        rate_history: [],
        total_tokens: 1000,
        total_cost_usd: 0.12,
        total_mem_kb: 4096,
        avg_context_pct: 12.5,
        active_sessions: 1,
        sessions: [],
      },
      quotas: [],
      projects: [],
      ports: [],
      orphan_ports: [],
      sessions: [],
      mcp_servers: [],
      updated_at: 1_700_000_000,
    };

    expect(resumeHint.agent).toBe("claude");
    expect(ptyOutput.offset).toBe(4096);
    expect(ptyResize.size.cols).toBe(120);
    expect(lease.epoch).toBe(2);
    expect(thread.driver).toBe("claude_sdk");
    expect(turn.state).toBe("running");
    expect(mailbox.kind).toBe("prompt");
    expect(event.payload.type).toBe("osc_status_received");
    expect(req.method).toBe("backend.exec");
    expect(space.status).toBe("working");
    expect(telemetry.context.active_sessions).toBe(1);
  });
});

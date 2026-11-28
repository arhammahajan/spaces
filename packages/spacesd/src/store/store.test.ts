import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type {
  EventEnvelope,
  Lease,
  LeaseTransition,
  MailboxMessage,
  MailboxSubscription,
  Thread,
  Turn,
} from "@spaces/types";
import {
  AUTH_TTL_MS,
  BLOCK_TTL_MS,
  DONE_TTL_MS,
  FAILED_TTL_MS,
  MAX_OUTCOMES,
  OUTCOME_DEDUPE_MS,
  OUTCOME_TTL_MS,
  SpacesStore,
  WORKING_STALE_MS,
} from "./db";
import { SCHEMA_VERSION, openSpacesDatabase } from "./schema";

const tempDirs: string[] = [];

function makeTempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "spacesd-store-test-"));
  tempDirs.push(dir);
  return dir;
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop()!;
    rmSync(dir, { recursive: true, force: true });
  }
});

function makeThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thr-1",
    agent: "claude",
    driver: "claude_sdk",
    kind: "hybrid",
    state: "active",
    cwd: "/repo/app",
    canonical_cwd: "/repo/app",
    epoch: 0,
    created_at_ms: 1_700_000_000_000,
    updated_at_ms: 1_700_000_000_000,
    ...overrides,
  };
}

describe("schema & openSpacesDatabase", () => {
  test("creates private parent dir (0700), DB file (0600), WAL mode, pragmas, and all 15 v1 tables", () => {
    const root = makeTempDir();
    const parentDir = join(root, "nested", "state");
    const dbPath = join(parentDir, "spaces.db");

    const db = openSpacesDatabase(dbPath);
    try {
      const parentMode = statSync(parentDir).mode & 0o777;
      const dbMode = statSync(dbPath).mode & 0o777;
      expect(parentMode).toBe(0o700);
      expect(dbMode).toBe(0o600);

      const journal = db.query<{ journal_mode: string }, []>("PRAGMA journal_mode;").get();
      expect(journal?.journal_mode.toLowerCase()).toBe("wal");

      const busy = db.query<{ timeout: number }, []>("PRAGMA busy_timeout;").get();
      expect(busy?.timeout).toBe(5000);

      const sync = db.query<{ synchronous: number }, []>("PRAGMA synchronous;").get();
      expect(sync?.synchronous).toBe(1); // NORMAL = 1

      const fk = db.query<{ foreign_keys: number }, []>("PRAGMA foreign_keys;").get();
      expect(fk?.foreign_keys).toBe(1);

      const migrations = db
        .query<{ version: string }, []>("SELECT version FROM schema_migrations")
        .all();
      expect(migrations.map((m) => m.version)).toEqual(["v1", SCHEMA_VERSION]);

      const tables = db
        .query<{ name: string }, []>(
          "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
        )
        .all()
        .map((r) => r.name);

      for (const expectedTable of [
        "schema_migrations",
        "threads",
        "leases",
        "lease_transitions",
        "turns",
        "events",
        "seen_events",
        "mailbox",
        "subscriptions",
        "outcomes",
        "hook_panes",
        "conversation_overlay",
        "resurrect_spaces",
        "resurrect_open_state",
        "pane_hmac_tokens",
        "verified_resume_hints",
        "worktrees",
      ]) {
        expect(tables).toContain(expectedTable);
      }

      const indexes = db
        .query<{ name: string }, []>(
          "SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_mailbox_thread_dedup'",
        )
        .all();
      expect(indexes).toHaveLength(1);
    } finally {
      db.close();
    }

    // Re-opening is idempotent and checkpoints cleanly on close(true).
    const store = new SpacesStore(dbPath);
    const migrations = store.db
      .query<{ version: string }, []>("SELECT version FROM schema_migrations")
      .all();
    expect(migrations).toHaveLength(2);
    store.close(true);
  });

  test("supports :memory: databases without creating filesystem dirs", () => {
    const store = new SpacesStore(":memory:");
    expect(store.getThread("missing")).toBeNull();
    store.close();
  });
});

describe("threads, turns & cascading deletes", () => {
  test("CRUD on threads and turns, filters, nullable updates, and FK cascade", () => {
    const store = new SpacesStore(":memory:");
    const t1 = store.createThread(
      makeThread({
        id: "thr-1",
        space: "alpha",
        pane_id: "%1",
        agent: "claude",
        driver: "claude_sdk",
        state: "active",
        title: "Fix bug",
        updated_at_ms: 100,
      }),
    );
    expect(t1.space).toBe("alpha");
    expect(t1.title).toBe("Fix bug");
    expect(t1.worktree_path).toBeUndefined();

    store.createThread(
      makeThread({
        id: "thr-2",
        space: "beta",
        agent: "codex",
        driver: "codex_sdk",
        state: "closed",
        updated_at_ms: 200,
      }),
    );

    expect(store.listThreads().map((t) => t.id)).toEqual(["thr-1"]);
    expect(store.listThreads({ include_closed: true }).map((t) => t.id)).toEqual(["thr-2", "thr-1"]);
    expect(store.listThreads({ state: "closed" }).map((t) => t.id)).toEqual(["thr-2"]);
    expect(store.listThreads({ space: "alpha", agent: "claude", driver: "claude_sdk" })).toHaveLength(1);

    const updated = store.updateThread("thr-1", {
      state: "idle",
      title: null,
      active_turn_id: "turn-1",
      updated_at_ms: 300,
    });
    expect(updated?.state).toBe("idle");
    expect(updated?.title).toBeUndefined();
    expect(updated?.active_turn_id).toBe("turn-1");

    const turn: Turn = {
      id: "turn-1",
      thread_id: "thr-1",
      epoch: 0,
      origin: "human",
      state: "running",
      prompt: "Run tests",
      started_at_ms: 310,
    };
    expect(store.createTurn(turn)).toEqual(turn);

    const updatedTurn = store.updateTurn("turn-1", {
      state: "failed",
      reply_preview: "Test failed",
      usage: {
        input_tokens: 100,
        output_tokens: 50,
        reasoning_tokens: 10,
        cache_read_tokens: 20,
        cache_write_tokens: 0,
        cost_usd: 0.02,
        cost_priced: true,
      },
      error: { code: "exit_1", message: "boom", retryable: false },
      ended_at_ms: 350,
    });
    expect(updatedTurn?.state).toBe("failed");
    expect(updatedTurn?.usage?.input_tokens).toBe(100);
    expect(updatedTurn?.error?.code).toBe("exit_1");
    expect(store.listTurns("thr-1")).toHaveLength(1);

    // Deleting the thread cascades to turns.
    expect(store.deleteThread("thr-1")).toBe(true);
    expect(store.getThread("thr-1")).toBeNull();
    expect(store.getTurn("turn-1")).toBeNull();
    expect(store.listTurns("thr-1")).toEqual([]);
    store.close();
  });
});

describe("leases & transactional lease updates", () => {
  test("applyLeaseTransition atomically updates leases, threads.epoch, lease_transitions, and events", () => {
    const store = new SpacesStore(":memory:");
    store.createThread(makeThread({ id: "thr-1", epoch: 0, updated_at_ms: 1000 }));

    const lease1: Lease = {
      id: "lease-1",
      thread_id: "thr-1",
      epoch: 1,
      state: "owned",
      owner: { kind: "human", id: "client-1", label: "tty1" },
      acquired_at_ms: 2000,
      expires_at_ms: 12000,
      updated_at_ms: 2000,
    };
    const transition1: LeaseTransition = {
      thread_id: "thr-1",
      from_state: "unowned",
      to_state: "owned",
      from_epoch: 0,
      to_epoch: 1,
      reason: "user_attach",
      actor: { kind: "human", id: "client-1" },
      ts_ms: 2000,
    };
    const event1: EventEnvelope = {
      id: "evt-lease-1",
      boot_id: "boot-1",
      seq: store.nextSeq("boot-1"),
      ts_ms: 2000,
      thread_id: "thr-1",
      epoch: 1,
      payload: {
        type: "lease_changed",
        lease: lease1,
        transition: transition1,
      },
    };

    const res = store.applyLeaseTransition(lease1, transition1, event1);
    expect(res.lease).toEqual(lease1);
    expect(res.transition).toEqual(transition1);
    expect(store.getLease("thr-1")).toEqual(lease1);
    expect(store.getThread("thr-1")?.epoch).toBe(1);
    expect(store.listLeaseTransitions("thr-1")).toEqual([transition1]);
    expect(store.listEventsAfter("boot-1", 0, 10)).toEqual([event1]);

    // Rollback check: colliding (boot_id, seq) on event rolls back lease + thread.epoch + transition!
    const badLease: Lease = { ...lease1, epoch: 2, state: "draining", updated_at_ms: 3000 };
    const badTransition: LeaseTransition = {
      thread_id: "thr-1",
      from_state: "owned",
      to_state: "draining",
      from_epoch: 1,
      to_epoch: 2,
      reason: "agent_claim",
      ts_ms: 3000,
    };
    const collidingEvent: EventEnvelope = {
      id: "evt-colliding",
      boot_id: "boot-1",
      seq: 1, // already taken by event1
      ts_ms: 3000,
      thread_id: "thr-1",
      epoch: 2,
      payload: {
        type: "lease_changed",
        lease: badLease,
        transition: badTransition,
      },
    };

    expect(() => store.applyLeaseTransition(badLease, badTransition, collidingEvent)).toThrow();
    expect(store.getLease("thr-1")?.epoch).toBe(1);
    expect(store.getLease("thr-1")?.state).toBe("owned");
    expect(store.getThread("thr-1")?.epoch).toBe(1);
    expect(store.listLeaseTransitions("thr-1")).toHaveLength(1);
    expect(
      store.db
        .query<{ cnt: number }, [string]>("SELECT COUNT(*) AS cnt FROM seen_events WHERE event_id = ?")
        .get("evt-colliding")?.cnt,
    ).toBe(0);

    // Nested transaction rollback leaves state intact.
    expect(() =>
      store.transaction(() => {
        store.upsertLease({ ...lease1, epoch: 99, updated_at_ms: 4000 });
        throw new Error("abort inner");
      }),
    ).toThrow("abort inner");
    expect(store.getLease("thr-1")?.epoch).toBe(1);

    store.close();
  });
});

describe("events, seen_events dedup, bounds & trimming", () => {
  test("dedups by seen_events even across seq bumps and post-trimming", () => {
    const store = new SpacesStore(":memory:");
    expect(store.getEventBounds("b1")).toEqual({ earliestSeq: null, latestSeq: null });
    expect(store.nextSeq("b1")).toBe(1);

    const makeEv = (id: string, seq: number, threadId?: string, type: "thread_updated" | "pty_exited" = "pty_exited"): EventEnvelope =>
      type === "pty_exited"
        ? {
            id,
            boot_id: "b1",
            seq,
            ts_ms: 1000 + seq,
            thread_id: threadId,
            payload: { type: "pty_exited", pty_id: "pty-1", exit_code: 0 },
          }
        : {
            id,
            boot_id: "b1",
            seq,
            ts_ms: 1000 + seq,
            thread_id: threadId,
            payload: {
              type: "thread_updated",
              thread: makeThread({ id: threadId ?? "thr-x" }),
            },
          };

    expect(store.appendEvent(makeEv("e1", 1, "thr-1", "thread_updated"))).toBe(true);
    // Retry with same id and different seq is idempotently dropped via seen_events.
    expect(store.appendEvent(makeEv("e1", 2, "thr-1", "thread_updated"))).toBe(false);
    expect(store.nextSeq("b1")).toBe(2);

    expect(store.appendEvent(makeEv("e2", 2, "thr-1", "pty_exited"))).toBe(true);
    expect(store.appendEvent(makeEv("e3", 3, "thr-2", "thread_updated"))).toBe(true);
    expect(store.appendEvent(makeEv("e4", 4, "thr-2", "pty_exited"))).toBe(true);

    expect(store.getEventBounds("b1")).toEqual({ earliestSeq: 1, latestSeq: 4 });
    expect(
      store.listEventsAfter("b1", 1, 10, { thread_ids: ["thr-2"], types: ["thread_updated"] }).map((e) => e.id),
    ).toEqual(["e3"]);

    // Trim to newest 2 events; e1 and e2 are removed from events, but e1 stays in seen_events.
    expect(store.trimEvents("b1", 2)).toBe(2);
    expect(store.getEventBounds("b1")).toEqual({ earliestSeq: 3, latestSeq: 4 });
    expect(store.appendEvent(makeEv("e1", 5, "thr-1", "thread_updated"))).toBe(false);
    expect(store.getEventBounds("b1")).toEqual({ earliestSeq: 3, latestSeq: 4 });

    store.close();
  });
});

describe("mailbox & subscriptions", () => {
  test("mailbox dedup_key returns existing message, respects priority ordering, ack updates delivered_at_ms", () => {
    const store = new SpacesStore(":memory:");
    store.createThread(makeThread({ id: "thr-1" }));
    store.createThread(makeThread({ id: "thr-2" }));

    const msg1: MailboxMessage = {
      id: "m-1",
      thread_id: "thr-1",
      from: { role: "coordinator", thread_id: "thr-coord" },
      kind: "prompt",
      priority: "normal",
      status: "pending",
      dedup_key: "key-1",
      payload: { text: "first" },
      created_at_ms: 1000,
    };
    const first = store.enqueueMailbox(msg1);
    expect(first).toEqual(msg1);

    // Duplicate (thread_id, dedup_key) with new id & payload returns original message cleanly.
    const dup = store.enqueueMailbox({
      ...msg1,
      id: "m-dup",
      payload: { text: "second" },
      created_at_ms: 2000,
    });
    expect(dup).toEqual(msg1);

    // Same dedup_key on a different thread is distinct.
    const otherThread = store.enqueueMailbox({
      ...msg1,
      id: "m-other",
      thread_id: "thr-2",
    });
    expect(otherThread.id).toBe("m-other");

    // Messages without dedup_key do not collide and sort by priority then created_at_ms.
    store.enqueueMailbox({
      id: "m-low",
      thread_id: "thr-1",
      from: { agent: "watcher" },
      kind: "system_notice",
      priority: "low",
      status: "pending",
      payload: null,
      created_at_ms: 1100,
    });
    store.enqueueMailbox({
      id: "m-urgent",
      thread_id: "thr-1",
      from: { role: "human" },
      kind: "steer",
      priority: "urgent",
      status: "pending",
      payload: { stop: true },
      created_at_ms: 1200,
    });

    expect(store.listMailbox("thr-1", "pending").map((m) => m.id)).toEqual([
      "m-urgent",
      "m-1",
      "m-low",
    ]);

    const delivered = store.ackMailbox("m-urgent", "delivered", 5000);
    expect(delivered?.status).toBe("delivered");
    expect(delivered?.delivered_at_ms).toBe(5000);

    const acked = store.ackMailbox("m-urgent", "acknowledged", 6000);
    expect(acked?.status).toBe("acknowledged");
    expect(acked?.delivered_at_ms).toBe(5000);

    expect(store.listMailbox("thr-1", "pending", 1).map((m) => m.id)).toEqual(["m-1"]);

    const sub: MailboxSubscription = {
      id: "sub-1",
      thread_id: "thr-1",
      source: "github",
      filter: { repo: "spaces", events: ["pr"] },
      created_at_ms: 1000,
    };
    expect(store.upsertSubscription(sub)).toEqual(sub);
    expect(store.listSubscriptions("thr-1")).toEqual([sub]);
    expect(store.deleteSubscription("sub-1")).toBe(true);
    expect(store.listSubscriptions()).toEqual([]);

    store.close();
  });
});

describe("outcomes invariants", () => {
  test("15s failed protection against generic done/finished and metadata richness merge", () => {
    const store = new SpacesStore(":memory:");
    const base = 1_700_000_000_000;

    const failed = store.recordOutcome(
      {
        session: "model-router",
        pane: "%41",
        agent: "antigravity",
        profile: "pratham",
        outcome: "failed",
        text: "build failed",
        title: "Deploy release",
        model: "gemini-4-neon-high",
        session_id: "conv-fail",
        cwd: "/repo",
        ts_ms: base,
      },
      base,
    );
    expect(failed.recorded).toBe(true);
    expect(failed.notify).toBe(true);

    // Trailing generic done/"finished" within 15s must NOT overwrite failed outcome.
    const genericExit = store.recordOutcome(
      {
        session: "model-router",
        pane: "41",
        agent: "antigravity",
        outcome: "done",
        text: "finished",
        session_id: "conv-fail",
        ts_ms: base + 100,
      },
      base + 100,
    );
    expect(genericExit.recorded).toBe(false);
    expect(genericExit.notify).toBe(false);

    // Also protects unattributed generic exit on same (session, pane) within 15s.
    const unattributedExit = store.recordOutcome(
      {
        session: "model-router",
        pane: "%41",
        agent: "antigravity",
        outcome: "done",
        text: "finished",
        ts_ms: base + OUTCOME_DEDUPE_MS,
      },
      base + OUTCOME_DEDUPE_MS,
    );
    expect(unattributedExit.recorded).toBe(false);

    let list = store.listOutcomes(base + 500);
    expect(list).toHaveLength(1);
    expect(list[0]?.outcome).toBe("failed");
    expect(list[0]?.model).toBe("gemini-4-neon-high");
    expect(list[0]?.profile).toBe("pratham");

    // Richness merge: unattributed OSC outcome followed within 15s by rich hook Stop merges into 1 entry.
    store.recordOutcome(
      {
        session: "dev",
        pane: "20",
        agent: "codex",
        outcome: "done",
        text: "",
        title: "hello who are you",
        cwd: "/repo",
        ts_ms: base + 20_000,
      },
      base + 20_000,
    );
    const rich = store.recordOutcome(
      {
        session: "dev",
        pane: "20",
        agent: "codex",
        outcome: "done",
        text: "I am Codex.",
        title: "hello who are you",
        cwd: "/repo",
        model: "gpt-6-luna",
        session_id: "sid-20",
        ts_ms: base + 20_800,
      },
      base + 20_800,
    );
    expect(rich.recorded).toBe(true);
    expect(rich.entry?.session_id).toBe("sid-20");

    // A less-rich duplicate on same (session, pane) within 15s is suppressed.
    const poorer = store.recordOutcome(
      {
        session: "dev",
        pane: "20",
        agent: "codex",
        outcome: "done",
        text: "finished",
        ts_ms: base + 21_000,
      },
      base + 21_000,
    );
    expect(poorer.recorded).toBe(false);

    // Same (agent, session_id) reopening and completing again keeps 1 entry and preserves metadata.
    const reopen = store.recordOutcome(
      {
        session: "dev",
        pane: "21",
        agent: "codex",
        outcome: "done",
        text: "turn complete",
        session_id: "sid-20",
        ts_ms: base + 60_000,
      },
      base + 60_000,
    );
    expect(reopen.recorded).toBe(true);
    expect(reopen.entry?.model).toBe("gpt-6-luna");
    expect(reopen.entry?.text).toBe("I am Codex.");

    list = store.listOutcomes(base + 60_000);
    expect(list).toHaveLength(2);

    // removeOutcome removes by (agent, session_id).
    expect(store.removeOutcome({ agent: "codex", session_id: "sid-20" })).toBe(1);
    expect(store.listOutcomes(base + 60_000)).toHaveLength(1);

    // 24h TTL expires outcomes cleanly.
    expect(store.listOutcomes(base + OUTCOME_TTL_MS + 1)).toEqual([]);

    store.close();
  });

  test("caps stored outcomes at MAX_OUTCOMES (20), newest first", () => {
    const store = new SpacesStore(":memory:");
    const base = 1_700_000_000_000;

    for (let i = 0; i < MAX_OUTCOMES + 5; i++) {
      const ts = base + i * 20_000;
      store.recordOutcome(
        {
          session: "work",
          pane: String(i),
          agent: "codex",
          outcome: "done",
          text: `done ${i}`,
          session_id: `sid-${i}`,
          ts_ms: ts,
        },
        ts,
      );
    }

    const latestTs = base + (MAX_OUTCOMES + 4) * 20_000;
    const list = store.listOutcomes(latestTs);
    expect(list).toHaveLength(MAX_OUTCOMES);
    expect(list[0]?.session_id).toBe(`sid-${MAX_OUTCOMES + 4}`);
    expect(list[MAX_OUTCOMES - 1]?.session_id).toBe("sid-5");

    store.close();
  });
});

describe("hook_panes ATT-5 & TTL pruning", () => {
  test("ATT-5 holds working on stop with active finite work, while services allow done", () => {
    const store = new SpacesStore(":memory:");
    const t0 = 1_700_000_000_000;

    const held = store.upsertHookPane({
      session: "dev",
      pane: "%1",
      agent: "claude",
      state: "done",
      event: "stop",
      ts_ms: t0,
      children: [
        {
          id: "sub-1",
          type: "Explore",
          kind: "subagent",
          category: "finite",
          state: "working",
          text: "scan repo",
        },
      ],
    });
    expect(held.state).toBe("working");
    expect(held.kind).toBeNull();
    expect(held.waiting_on).toBe("waiting on subagent: scan repo");

    // Held working pane survives well beyond 120s.
    expect(store.pruneHookPanes(t0 + 10 * 60_000)).toBe(0);
    expect(store.listHookPanes(t0 + 10 * 60_000)).toHaveLength(1);

    // Service-only background task does NOT hold Stop from becoming done.
    const finished = store.upsertHookPane({
      session: "dev",
      pane: "%2",
      agent: "claude",
      state: "done",
      event: "stop",
      ts_ms: t0,
      background_tasks: [
        {
          id: "srv-1",
          kind: "service",
          category: "service",
          state: "running",
          command: "bun run dev",
        },
      ],
    });
    expect(finished.state).toBe("done");
    expect(finished.waiting_on).toBeNull();

    // clearHookPane removes only the pane record and leaves outcomes intact.
    store.recordOutcome(
      {
        session: "dev",
        pane: "1",
        agent: "claude",
        outcome: "done",
        text: "completed",
        session_id: "s-keep",
        ts_ms: t0,
      },
      t0,
    );
    expect(store.clearHookPane("dev", "%1")).toBe(true);
    expect(store.getHookPane("dev", "%1")).toBeNull();
    expect(store.listOutcomes(t0)).toHaveLength(1);

    store.close();
  });

  test("TTL pruning matrix: 120s working/idle, 30m done/failed/permission/question, 60m sticky auth", () => {
    const store = new SpacesStore(":memory:");
    const now = 1_700_000_000_000;

    store.upsertHookPane({
      session: "s",
      pane: "work-fresh",
      agent: "claude",
      state: "working",
      event: "pre_tool",
      ts_ms: now - 60_000,
    });
    store.upsertHookPane({
      session: "s",
      pane: "work-stale",
      agent: "claude",
      state: "working",
      event: "pre_tool",
      ts_ms: now - (WORKING_STALE_MS + 1),
    });
    store.upsertHookPane({
      session: "s",
      pane: "work-waiting-on",
      agent: "claude",
      state: "working",
      event: "stop",
      ts_ms: now - (WORKING_STALE_MS + 60_000),
      waiting_on: "waiting on: cargo test",
    });
    store.upsertHookPane({
      session: "s",
      pane: "work-service-only",
      agent: "claude",
      state: "working",
      event: "pre_tool",
      ts_ms: now - (WORKING_STALE_MS + 1),
      background_tasks: [
        { id: "dev-srv", kind: "service", category: "service", state: "running" },
      ],
    });
    store.upsertHookPane({
      session: "s",
      pane: "idle-stale",
      agent: "claude",
      state: "idle",
      event: "session_start",
      ts_ms: now - (WORKING_STALE_MS + 1),
    });
    store.upsertHookPane({
      session: "s",
      pane: "done-fresh",
      agent: "claude",
      state: "done",
      event: "stop",
      ts_ms: now - 1000,
      state_since_ms: now - 60_000,
    });
    store.upsertHookPane({
      session: "s",
      pane: "done-stale",
      agent: "claude",
      state: "done",
      event: "stop",
      ts_ms: now - 1000,
      state_since_ms: now - (DONE_TTL_MS + 1),
    });
    store.upsertHookPane({
      session: "s",
      pane: "failed-stale",
      agent: "claude",
      state: "failed",
      event: "stop_failure",
      ts_ms: now - 1000,
      state_since_ms: now - (FAILED_TTL_MS + 1),
    });
    store.upsertHookPane({
      session: "s",
      pane: "perm-stale",
      agent: "claude",
      state: "blocked",
      kind: "permission",
      event: "permission",
      ts_ms: now - 1000,
      state_since_ms: now - (BLOCK_TTL_MS + 1),
    });
    store.upsertHookPane({
      session: "s",
      pane: "question-fresh",
      agent: "claude",
      state: "blocked",
      kind: "question",
      event: "elicitation",
      ts_ms: now - 1000,
      state_since_ms: now - 60_000,
    });
    store.upsertHookPane({
      session: "s",
      pane: "auth-sticky",
      agent: "claude",
      state: "blocked",
      kind: "auth",
      event: "permission",
      ts_ms: now - 1000,
      state_since_ms: now - (BLOCK_TTL_MS + 1),
    });

    const livePanes = store.listHookPanes(now).map((p) => p.pane);
    expect(livePanes.sort()).toEqual([
      "auth-sticky",
      "done-fresh",
      "question-fresh",
      "work-fresh",
      "work-waiting-on",
    ]);

    expect(store.pruneHookPanes(now)).toBe(6);

    // At +28m: work-fresh (>120s) expires, while done-fresh (29m), question-fresh (29m),
    // auth-sticky (58m), and work-waiting-on (ATT-5) stay alive.
    expect(store.pruneHookPanes(now + 28 * 60_000)).toBe(1);
    expect(store.getHookPane("s", "auth-sticky")).not.toBeNull();

    // At +30m: done-fresh (31m), question-fresh (31m), and auth-sticky (60m+1ms) expire;
    // work-waiting-on remains alive.
    expect(store.pruneHookPanes(now + (AUTH_TTL_MS - BLOCK_TTL_MS))).toBe(3);
    expect(store.getHookPane("s", "auth-sticky")).toBeNull();
    expect(store.getHookPane("s", "work-waiting-on")).not.toBeNull();

    // Codex turn-scoped child filtering drops stale children ended before turn_started_ms.
    store.upsertHookPane({
      session: "codex-sess",
      pane: "%9",
      agent: "codex",
      state: "working",
      event: "prompt",
      ts_ms: now,
      turn_started_ms: now - 10_000,
      children: [
        { id: "old-sub", kind: "subagent", category: "finite", state: "done", ended_ms: now - 20_000 },
        { id: "cur-sub", kind: "subagent", category: "finite", state: "working", ended_ms: 0 },
      ],
    });
    const codexPane = store.listHookPanes(now).find((p) => p.session === "codex-sess");
    expect(codexPane?.children.map((c) => c.id)).toEqual(["cur-sub"]);
    expect(store.pruneHookPanes(now)).toBe(0);
    expect(store.getHookPane("codex-sess", "%9")?.children.map((c) => c.id)).toEqual(["cur-sub"]);

    store.close();
  });
});

describe("conversation_overlay non-empty merge", () => {
  test("canonicalizes agents, preserves non-empty fields, takes MAX timestamps, returns launch_cmd", () => {
    const store = new SpacesStore(":memory:");

    const initial = store.upsertConversationOverlay({
      agent: "agy",
      session_id: "conv-1",
      space: "alpha",
      last_opened_ms: 2000,
      last_outcome: "done",
      profile: "work",
      cwd: "/repo/a",
      title: "Fix SQLite store",
      launch_cmd: ["harness", "agy", "profile", "work", "--conversation", "conv-1"],
      updated_at_ms: 2500,
    });
    expect(initial?.agent).toBe("antigravity");

    // Empty fields & older timestamps must not clobber existing metadata.
    const merged = store.upsertConversationOverlay({
      agent: "antigravity",
      session_id: "conv-1",
      space: "",
      last_opened_ms: 1000,
      last_outcome: "",
      profile: "",
      cwd: "",
      title: "Updated title",
      launch_cmd: [],
      updated_at_ms: 1500,
    });
    expect(merged).toEqual({
      agent: "antigravity",
      session_id: "conv-1",
      space: "alpha",
      last_opened_ms: 2000,
      last_outcome: "done",
      profile: "work",
      cwd: "/repo/a",
      title: "Updated title",
      launch_cmd: ["harness", "agy", "profile", "work", "--conversation", "conv-1"],
      updated_at_ms: 2500,
    });

    expect(store.getConversationLaunchCmd("agy", "conv-1")).toEqual([
      "harness",
      "agy",
      "profile",
      "work",
      "--conversation",
      "conv-1",
    ]);
    expect(store.getConversationLaunchCmd("claude-code", "missing")).toBeNull();

    store.upsertConversationOverlay({
      agent: "claude-code",
      session_id: "conv-2",
      space: "beta",
      last_opened_ms: 3000,
    });
    const list = store.listConversationOverlays();
    expect(list.map((o) => `${o.agent}:${o.session_id}`)).toEqual([
      "claude:conv-2",
      "antigravity:conv-1",
    ]);

    store.close();
  });
});

describe("resurrect spaces & open state", () => {
  test("distinguishes explicit close from crash recovery and deletes from both tables", () => {
    let clock = 1_700_000_000_000;
    const store = new SpacesStore(":memory:", { now: () => clock });

    store.saveResurrectSpace({
      name: "alpha",
      saved_at_ms: clock - 20_000,
      cwd: "/repo/alpha",
      windows: [
        {
          index: 0,
          name: "code",
          active: true,
          panes: [{ index: 0, cwd: "/repo/alpha", active: true, agent: "claude", session_id: "s-1" }],
        },
      ],
    });
    store.saveResurrectSpace({
      name: "beta",
      saved_at_ms: clock - 10_000,
      cwd: "/repo/beta",
      windows: [
        {
          index: 0,
          name: "codex",
          active: true,
          panes: [
            { index: 0, cwd: "/repo/beta", active: true, agent: "codex" },
            { index: 1, cwd: "/repo/beta", active: false, agent: "codex" },
          ],
        },
      ],
    });
    store.saveResurrectSpace({
      name: "exited-prior",
      saved_at_ms: clock - 30_000,
      cwd: "/repo/exited",
      windows: [{ index: 0, name: "shell", active: true, panes: [{ index: 0, active: true }] }],
    });

    // Open spaces during normal work: alpha & beta are open; exited-prior was already closed.
    store.addOpenSpace("alpha", true);
    store.addOpenSpace("beta", true);
    expect(store.getOpenState()).toEqual({
      version: 1,
      saved_at_ms: clock,
      active_session: "beta",
      spaces: ["alpha", "beta"],
    });

    // Crash recovery scenario: daemon/tmux died with alpha + beta open; neither is running now.
    const crashExited = store.listExitedSpaces([]);
    expect(crashExited.map((s) => s.name)).toEqual(["beta", "alpha", "exited-prior"]);
    expect(store.getOpenState()?.spaces).toEqual(["alpha", "beta"]);

    // Explicit close of beta: removeOpenSpace removes beta from auto-restore open_state
    // while keeping its snapshot so it remains available in listExitedSpaces!
    clock += 5_000;
    expect(store.removeOpenSpace("beta")).toBe(true);
    expect(store.getOpenState()).toEqual({
      version: 1,
      saved_at_ms: clock,
      active_session: "alpha",
      spaces: ["alpha"],
    });
    const afterExplicitClose = store.listExitedSpaces({ alpha: true });
    expect(afterExplicitClose.map((s) => s.name)).toEqual(["beta", "exited-prior"]);
    expect(afterExplicitClose[0]).toEqual({
      name: "beta",
      saved_at_ms: 1_700_000_000_000 - 10_000,
      cwd: "/repo/beta",
      window_count: 1,
      pane_count: 2,
      agents: ["codex"],
    });

    // Rename alpha -> alpha2 updates both snapshot and open_state.
    expect(store.renameOpenSpace("alpha", "alpha2")).toBe(true);
    expect(store.getResurrectSpace("alpha")).toBeNull();
    expect(store.getResurrectSpace("alpha2")?.cwd).toBe("/repo/alpha");
    expect(store.getOpenState()?.spaces).toEqual(["alpha2"]);
    expect(store.getOpenState()?.active_session).toBe("alpha2");

    // deleteResurrectSpace removes from BOTH resurrect_spaces and resurrect_open_state.
    expect(store.deleteResurrectSpace("alpha2")).toBe(true);
    expect(store.getResurrectSpace("alpha2")).toBeNull();
    expect(store.getOpenState()?.spaces).toEqual([]);
    expect(store.getOpenState()?.active_session).toBe("");

    store.close();
  });
});

describe("pane HMAC tokens & verified OSC 7577 resume hints", () => {
  test("creates, rotates, revokes pane tokens and persists verified resume hints", () => {
    const store = new SpacesStore(":memory:");

    const created = store.upsertPaneToken("%1", "aa".repeat(32), "thr-1", 1000);
    expect(created).toEqual({
      pane_id: "%1",
      thread_id: "thr-1",
      secret_hex: "aa".repeat(32),
      created_at_ms: 1000,
    });

    // Rotating token keeps created_at_ms, sets rotated_at_ms, and clears revoked_at_ms.
    expect(store.revokePaneToken("%1", 1500)).toBe(true);
    expect(store.getPaneToken("%1")?.revoked_at_ms).toBe(1500);

    const rotated = store.upsertPaneToken("%1", "bb".repeat(32), undefined, 2000);
    expect(rotated).toEqual({
      pane_id: "%1",
      thread_id: "thr-1",
      secret_hex: "bb".repeat(32),
      created_at_ms: 1000,
      rotated_at_ms: 2000,
    });
    expect(store.revokePaneToken("%missing", 2500)).toBe(false);

    const hint = store.upsertVerifiedResumeHint({
      pane_id: "%1",
      thread_id: "thr-1",
      agent: "claude",
      session_id: "sess-xyz",
      profile: "work",
      cwd: "/repo/app",
      resume_target: "sess-xyz",
      resume_cmd: ["claude", "--resume", "sess-xyz"],
      signature_hex: "cc".repeat(32),
      verified_at_ms: 3000,
    });
    expect(hint).toEqual({
      pane_id: "%1",
      thread_id: "thr-1",
      agent: "claude",
      session_id: "sess-xyz",
      profile: "work",
      cwd: "/repo/app",
      resume_target: "sess-xyz",
      resume_cmd: ["claude", "--resume", "sess-xyz"],
      signature_hex: "cc".repeat(32),
      verified_at_ms: 3000,
      updated_at_ms: 3000,
    });

    const updatedHint = store.upsertVerifiedResumeHint({
      pane_id: "%1",
      agent: "claude",
      session_id: "sess-xyz-2",
      signature_hex: "dd".repeat(32),
      verified_at_ms: 3500,
      updated_at_ms: 4000,
    });
    expect(updatedHint.session_id).toBe("sess-xyz-2");
    expect(updatedHint.resume_cmd).toBeUndefined();
    expect(store.getVerifiedResumeHint("%1")).toEqual(updatedHint);
    expect(store.getVerifiedResumeHint("%999")).toBeNull();

    store.close();
  });
});

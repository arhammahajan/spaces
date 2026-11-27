import { describe, expect, test } from "bun:test";
import type { Lease, LeaseOwner, LeaseTransition, ThreadId } from "@spaces/types";
import {
  LeaseError,
  LeaseMachine,
  type LeaseStoreAdapter,
} from "./lease-fsm";

class MemoryLeaseStore implements LeaseStoreAdapter {
  readonly leases = new Map<ThreadId, Lease>();
  readonly transitions: LeaseTransition[] = [];

  getLease(threadId: ThreadId): Lease | null {
    const lease = this.leases.get(threadId);
    return lease ? { ...lease, owner: lease.owner ? { ...lease.owner } : undefined } : null;
  }

  commitLeaseTransition(lease: Lease, transition: LeaseTransition): void {
    this.leases.set(lease.thread_id, {
      ...lease,
      owner: lease.owner ? { ...lease.owner } : undefined,
    });
    this.transitions.push(transition);
  }
}

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const humanOwner: LeaseOwner = {
  kind: "human",
  id: "tty-1",
  client_id: "cli-1",
};

const agentOwner: LeaseOwner = {
  kind: "agent",
  id: "coordinator",
  client_id: "mcp-1",
};

describe("LeaseMachine", () => {
  test("claim transitions unowned -> owned (epoch + 1), rejects conflict, and is idempotent for same owner", async () => {
    let clock = 1_000;
    const store = new MemoryLeaseStore();
    const fsm = new LeaseMachine(store, {
      now: () => clock,
      idGenerator: () => "lease_fixed",
    });

    const first = await fsm.claim({
      threadId: "thr-1",
      owner: humanOwner,
      ttlMs: 5_000,
    });
    expect(first.lease).toEqual({
      id: "lease_fixed",
      thread_id: "thr-1",
      epoch: 1,
      state: "owned",
      owner: humanOwner,
      acquired_at_ms: 1_000,
      expires_at_ms: 6_000,
      updated_at_ms: 1_000,
    });
    expect(first.transitions).toHaveLength(1);
    expect(first.transitions[0]).toMatchObject({
      from_state: "unowned",
      to_state: "owned",
      from_epoch: 0,
      to_epoch: 1,
      reason: "user_attach",
    });

    // Different owner (or different client_id) is rejected with -32004
    await expect(
      fsm.claim({ threadId: "thr-1", owner: agentOwner }),
    ).rejects.toMatchObject({ name: "LeaseError", code: -32004 });

    await expect(
      fsm.claim({
        threadId: "thr-1",
        owner: { ...humanOwner, client_id: "cli-2" },
      }),
    ).rejects.toMatchObject({ name: "LeaseError", code: -32004 });

    // Idempotent re-claim by exact same owner without ttlMs keeps state unchanged
    clock = 2_000;
    const idempotentNoTtl = await fsm.claim({
      threadId: "thr-1",
      owner: humanOwner,
    });
    expect(idempotentNoTtl.lease.epoch).toBe(1);
    expect(idempotentNoTtl.lease.expires_at_ms).toBe(6_000);
    expect(idempotentNoTtl.transitions).toEqual([]);

    // Idempotent re-claim with ttlMs refreshes expires_at_ms without bumping epoch
    clock = 3_000;
    const refreshed = await fsm.claim({
      threadId: "thr-1",
      owner: humanOwner,
      ttlMs: 10_000,
    });
    expect(refreshed.lease.epoch).toBe(1);
    expect(refreshed.lease.acquired_at_ms).toBe(1_000);
    expect(refreshed.lease.expires_at_ms).toBe(13_000);
    expect(refreshed.transitions).toEqual([]);
    expect(store.getLease("thr-1")?.expires_at_ms).toBe(13_000);
  });

  test("yield transitions owned -> unowned (epoch + 1) and enforces epoch/owner/not-found", async () => {
    let clock = 5_000;
    const store = new MemoryLeaseStore();
    const fsm = new LeaseMachine(store, { now: () => clock });

    await expect(
      fsm.yield({ threadId: "missing", owner: humanOwner, epoch: 1 }),
    ).rejects.toMatchObject({ code: -32003 });

    await fsm.claim({ threadId: "thr-1", owner: humanOwner });

    // Wrong epoch -> -32002
    await expect(
      fsm.yield({ threadId: "thr-1", owner: humanOwner, epoch: 99 }),
    ).rejects.toMatchObject({ code: -32002 });

    // Wrong owner at current epoch -> -32004
    await expect(
      fsm.yield({ threadId: "thr-1", owner: agentOwner, epoch: 1 }),
    ).rejects.toMatchObject({ code: -32004 });

    clock = 6_000;
    const yielded = await fsm.yield({
      threadId: "thr-1",
      owner: humanOwner,
      epoch: 1,
    });
    expect(yielded.lease).toMatchObject({
      thread_id: "thr-1",
      epoch: 2,
      state: "unowned",
      updated_at_ms: 6_000,
    });
    expect(yielded.lease.owner).toBeUndefined();
    expect(yielded.transitions[0]).toMatchObject({
      from_state: "owned",
      to_state: "unowned",
      from_epoch: 1,
      to_epoch: 2,
      reason: "user_yield",
    });

    // Yielding again at epoch 2 -> -32004 conflict (already unowned)
    await expect(
      fsm.yield({ threadId: "thr-1", owner: humanOwner, epoch: 2 }),
    ).rejects.toMatchObject({ code: -32004 });

    // Subsequent claim increments epoch to 3
    const reclaimed = await fsm.claim({ threadId: "thr-1", owner: agentOwner });
    expect(reclaimed.lease.epoch).toBe(3);
    expect(reclaimed.lease.owner).toEqual(agentOwner);
  });

  test("graceful handoff runs owned -> draining -> stopping_and_flushing -> owned(epoch + 1) with hooks", async () => {
    const store = new MemoryLeaseStore();
    const hookOrder: string[] = [];

    const fsm = new LeaseMachine(store, {
      hooks: {
        onDrain: async (lease, reason) => {
          hookOrder.push(`drain:${lease.state}:${lease.epoch}:${reason}`);
          expect(store.getLease("thr-1")?.state).toBe("draining");
          // Conflicting claim during transition must fail with -32004
          await expect(
            fsm.claim({ threadId: "thr-1", owner: agentOwner }),
          ).rejects.toMatchObject({ code: -32004 });
        },
        onStopAndFlush: async (lease, reason) => {
          hookOrder.push(`flush:${lease.state}:${lease.epoch}:${reason}`);
          expect(store.getLease("thr-1")?.state).toBe("stopping_and_flushing");
        },
      },
    });

    await fsm.claim({ threadId: "thr-1", owner: humanOwner });
    const res = await fsm.handoff({
      threadId: "thr-1",
      targetOwner: agentOwner,
      expectedEpoch: 1,
      reason: "agent_claim",
      ttlMs: 30_000,
    });

    expect(hookOrder).toEqual([
      "drain:draining:1:agent_claim",
      "flush:stopping_and_flushing:1:agent_claim",
    ]);
    expect(res.lease).toMatchObject({
      thread_id: "thr-1",
      epoch: 2,
      state: "owned",
      owner: agentOwner,
    });
    expect(res.lease.expires_at_ms).toBeDefined();
    expect(res.transitions.map((t) => [t.from_state, t.to_state, t.from_epoch, t.to_epoch])).toEqual([
      ["owned", "draining", 1, 1],
      ["draining", "stopping_and_flushing", 1, 1],
      ["stopping_and_flushing", "owned", 1, 2],
    ]);
  });

  test("handoff delegates to claim when unowned with targetOwner, and rejects stale expectedEpoch", async () => {
    const store = new MemoryLeaseStore();
    const fsm = new LeaseMachine(store);

    await expect(
      fsm.handoff({ threadId: "missing", targetOwner: agentOwner }),
    ).rejects.toMatchObject({ code: -32003 });

    await fsm.claim({ threadId: "thr-1", owner: humanOwner });
    await fsm.yield({ threadId: "thr-1", owner: humanOwner, epoch: 1 });

    // Stale expectedEpoch when unowned -> -32002
    await expect(
      fsm.handoff({
        threadId: "thr-1",
        targetOwner: agentOwner,
        expectedEpoch: 1,
      }),
    ).rejects.toMatchObject({ code: -32002 });

    // Unowned without targetOwner -> -32004
    await expect(
      fsm.handoff({ threadId: "thr-1", expectedEpoch: 2 }),
    ).rejects.toMatchObject({ code: -32004 });

    const claimed = await fsm.handoff({
      threadId: "thr-1",
      targetOwner: agentOwner,
      expectedEpoch: 2,
    });
    expect(claimed.lease.state).toBe("owned");
    expect(claimed.lease.epoch).toBe(3);
    expect(claimed.lease.owner).toEqual(agentOwner);
  });

  test("forced handoff / cancel transitions owned/draining -> cancelling -> stopping_and_flushing -> unowned(epoch+1)", async () => {
    const store = new MemoryLeaseStore();
    const hookOrder: string[] = [];
    const drainGate = deferred<void>();

    const fsm = new LeaseMachine(store, {
      onDrain: async () => {
        hookOrder.push("drain:enter");
        await drainGate.promise;
        hookOrder.push("drain:exit");
      },
      onCancel: async (lease, reason) => {
        hookOrder.push(`cancel:${lease.state}:${lease.epoch}:${reason}`);
        expect(store.getLease("thr-1")?.state).toBe("cancelling");
      },
      onStopAndFlush: async (lease, reason) => {
        hookOrder.push(`flush:${lease.state}:${lease.epoch}:${reason}`);
        expect(store.getLease("thr-1")?.state).toBe("stopping_and_flushing");
      },
    });

    await fsm.claim({ threadId: "thr-1", owner: humanOwner });

    // Start graceful handoff and pause inside onDrain
    const gracefulPromise = fsm.handoff({
      threadId: "thr-1",
      targetOwner: agentOwner,
      expectedEpoch: 1,
    });
    await Promise.resolve();
    expect(store.getLease("thr-1")?.state).toBe("draining");

    // Preempt draining lease via cancel()
    const cancelRes = await fsm.cancel({
      threadId: "thr-1",
      expectedEpoch: 1,
      reason: "timeout",
    });
    expect(cancelRes.lease).toMatchObject({
      thread_id: "thr-1",
      state: "unowned",
      epoch: 2,
    });
    expect(
      cancelRes.transitions.map((t) => [t.from_state, t.to_state, t.from_epoch, t.to_epoch]),
    ).toEqual([
      ["draining", "cancelling", 1, 1],
      ["cancelling", "stopping_and_flushing", 1, 1],
      ["stopping_and_flushing", "unowned", 1, 2],
    ]);

    // Unblock superseded graceful drain; it must abort with LeaseConflict (-32004)
    drainGate.resolve();
    await expect(gracefulPromise).rejects.toMatchObject({
      name: "LeaseError",
      code: -32004,
    });
    expect(store.getLease("thr-1")?.epoch).toBe(2);
    expect(store.getLease("thr-1")?.state).toBe("unowned");
    expect(hookOrder).toEqual([
      "drain:enter",
      "cancel:cancelling:1:timeout",
      "flush:stopping_and_flushing:1:timeout",
      "drain:exit",
    ]);
  });

  test("assertFenced validates owned state, epoch, and optional ownerId", async () => {
    const store = new MemoryLeaseStore();
    const drainGate = deferred<void>();
    const fsm = new LeaseMachine(store, {
      onDrain: async () => {
        await drainGate.promise;
      },
    });

    expect(() => fsm.assertFenced("missing", 1)).toThrow(LeaseError);
    try {
      fsm.assertFenced("missing", 1);
    } catch (err) {
      expect((err as LeaseError).code).toBe(-32003);
    }

    await fsm.claim({ threadId: "thr-1", owner: humanOwner });

    const ok = fsm.assertFenced("thr-1", 1, "tty-1");
    expect(ok.epoch).toBe(1);
    expect(fsm.assertFenced("thr-1", 1).owner).toEqual(humanOwner);

    // Wrong epoch -> -32002
    try {
      fsm.assertFenced("thr-1", 2);
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(LeaseError);
      expect((err as LeaseError).code).toBe(-32002);
    }

    // Wrong ownerId -> -32004
    try {
      fsm.assertFenced("thr-1", 1, "coordinator");
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(LeaseError);
      expect((err as LeaseError).code).toBe(-32004);
    }

    // Draining at epoch 1 is not owned -> -32002
    const handoffPromise = fsm.handoff({ threadId: "thr-1", targetOwner: agentOwner });
    await Promise.resolve();
    try {
      fsm.assertFenced("thr-1", 1);
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(LeaseError);
      expect((err as LeaseError).code).toBe(-32002);
    }

    drainGate.resolve();
    await handoffPromise;
    // Old epoch 1 still fails, new epoch 2 succeeds
    expect(() => fsm.assertFenced("thr-1", 1)).toThrow();
    expect(fsm.assertFenced("thr-1", 2, "coordinator").epoch).toBe(2);
  });
});

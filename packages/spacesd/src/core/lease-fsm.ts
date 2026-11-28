import { randomUUID } from "node:crypto";
import type {
  Epoch,
  HandoffReason,
  Lease,
  LeaseId,
  LeaseOwner,
  LeaseState,
  LeaseTransition,
  RpcErrorCode,
  ThreadId,
  TimestampMs,
} from "@spaces/types";

const RPC_STALE_EPOCH: RpcErrorCode = -32002;
const RPC_NOT_FOUND: RpcErrorCode = -32003;
const RPC_LEASE_CONFLICT: RpcErrorCode = -32004;

/**
 * Typed error raised by `LeaseMachine` for fencing, missing lease, or ownership conflicts.
 */
export class LeaseError extends Error {
  readonly code: RpcErrorCode;
  readonly data?: unknown;

  constructor(code: RpcErrorCode, message: string, data?: unknown) {
    super(message);
    this.name = "LeaseError";
    this.code = code;
    this.data = data;
  }
}

export interface LeaseStoreAdapter {
  getLease(threadId: ThreadId): Lease | null;
  commitLeaseTransition(lease: Lease, transition: LeaseTransition): void;
  refreshLease?(lease: Lease): void;
}

export interface LeaseHooks {
  onDrain?: (lease: Lease, reason: HandoffReason) => Promise<void> | void;
  onCancel?: (lease: Lease, reason: HandoffReason) => Promise<void> | void;
  onStopAndFlush?: (lease: Lease, reason: HandoffReason) => Promise<void> | void;
}

export interface LeaseMachineOptions {
  hooks?: LeaseHooks;
  now?: () => TimestampMs;
  idGenerator?: () => LeaseId;
}

export interface LeaseTransitionResult {
  lease: Lease;
  transitions: LeaseTransition[];
}

export interface LeaseClaimInput {
  threadId: ThreadId;
  owner: LeaseOwner;
  ttlMs?: number;
  reason?: HandoffReason;
  expectedEpoch?: Epoch;
}

export interface LeaseYieldInput {
  threadId: ThreadId;
  owner: LeaseOwner;
  epoch: Epoch;
  reason?: HandoffReason;
}

export interface LeaseHandoffInput {
  threadId: ThreadId;
  targetOwner?: LeaseOwner;
  expectedEpoch?: Epoch;
  force?: boolean;
  reason?: HandoffReason;
  ttlMs?: number;
}

export interface LeaseCancelInput {
  threadId: ThreadId;
  reason?: HandoffReason;
  expectedEpoch?: Epoch;
}

function isLeaseHooks(value: LeaseHooks | LeaseMachineOptions): value is LeaseHooks {
  return "onDrain" in value || "onCancel" in value || "onStopAndFlush" in value;
}

function isSameOwner(a: LeaseOwner, b: LeaseOwner): boolean {
  return (
    a.kind === b.kind &&
    a.id === b.id &&
    (a.client_id ?? undefined) === (b.client_id ?? undefined)
  );
}

function defaultClaimReason(owner: LeaseOwner): HandoffReason {
  return owner.kind === "agent" ? "agent_claim" : "user_attach";
}

function defaultYieldReason(owner: LeaseOwner): HandoffReason {
  return owner.kind === "agent" ? "agent_yield" : "user_yield";
}

function defaultHandoffReason(
  targetOwner: LeaseOwner | undefined,
  currentOwner: LeaseOwner | undefined,
  force: boolean,
): HandoffReason {
  if (force) {
    return "cancel";
  }
  if (targetOwner) {
    return defaultClaimReason(targetOwner);
  }
  if (currentOwner) {
    return defaultYieldReason(currentOwner);
  }
  return "user_yield";
}

/**
 * Fenced ownership lease FSM (`UNOWNED -> OWNED(epoch)`,
 * `DRAIN / CANCEL -> STOP_AND_FLUSH -> +1`).
 */
export class LeaseMachine {
  private readonly store: LeaseStoreAdapter;
  private readonly hooks: LeaseHooks;
  private readonly now: () => TimestampMs;
  private readonly idGenerator: () => LeaseId;

  constructor(
    store: LeaseStoreAdapter,
    hooksOrOptions?: LeaseHooks | LeaseMachineOptions,
  ) {
    this.store = store;
    if (!hooksOrOptions) {
      this.hooks = {};
      this.now = () => Date.now();
      this.idGenerator = () => `lease_${randomUUID()}`;
    } else if (isLeaseHooks(hooksOrOptions)) {
      this.hooks = hooksOrOptions;
      this.now = () => Date.now();
      this.idGenerator = () => `lease_${randomUUID()}`;
    } else {
      this.hooks = hooksOrOptions.hooks ?? {};
      this.now = hooksOrOptions.now ?? (() => Date.now());
      this.idGenerator = hooksOrOptions.idGenerator ?? (() => `lease_${randomUUID()}`);
    }
  }

  getLease(threadId: ThreadId): Lease | null {
    return this.store.getLease(threadId);
  }

  /**
   * Claims an `unowned` lease (`unowned -> owned`, `epoch + 1`), or refreshes
   * `expires_at_ms` idempotently when already owned by the exact same `owner`.
   */
  async claim(params: LeaseClaimInput): Promise<LeaseTransitionResult> {
    const now = this.now();
    const existing = this.store.getLease(params.threadId);

    if (!existing) {
      if (params.expectedEpoch !== undefined && params.expectedEpoch !== 0) {
        throw new LeaseError(
          RPC_STALE_EPOCH,
          `Epoch mismatch for thread ${params.threadId}: expected ${params.expectedEpoch}, current 0`,
          { expected: params.expectedEpoch, current: 0 },
        );
      }

      const reason = params.reason ?? defaultClaimReason(params.owner);
      const lease: Lease = {
        id: this.idGenerator(),
        thread_id: params.threadId,
        epoch: 1,
        state: "owned",
        owner: params.owner,
        acquired_at_ms: now,
        ...(params.ttlMs !== undefined ? { expires_at_ms: now + params.ttlMs } : {}),
        updated_at_ms: now,
      };
      const transition: LeaseTransition = {
        thread_id: params.threadId,
        from_state: "unowned",
        to_state: "owned",
        from_epoch: 0,
        to_epoch: 1,
        reason,
        actor: params.owner,
        ts_ms: now,
      };
      this.store.commitLeaseTransition(lease, transition);
      return { lease, transitions: [transition] };
    }

    if (params.expectedEpoch !== undefined && existing.epoch !== params.expectedEpoch) {
      throw new LeaseError(
        RPC_STALE_EPOCH,
        `Epoch mismatch for thread ${params.threadId}: expected ${params.expectedEpoch}, current ${existing.epoch}`,
        { expected: params.expectedEpoch, current: existing.epoch },
      );
    }

    if (existing.state === "owned") {
      if (existing.owner && isSameOwner(existing.owner, params.owner)) {
        if (params.ttlMs === undefined) {
          return { lease: existing, transitions: [] };
        }
        const refreshed: Lease = {
          ...existing,
          expires_at_ms: now + params.ttlMs,
          updated_at_ms: now,
        };
        if (this.store.refreshLease) {
          this.store.refreshLease(refreshed);
        } else {
          // Persist refreshed expiry through the 2-method adapter without bumping epoch.
          this.store.commitLeaseTransition(refreshed, {
            thread_id: params.threadId,
            from_state: "owned",
            to_state: "owned",
            from_epoch: existing.epoch,
            to_epoch: existing.epoch,
            reason: params.reason ?? defaultClaimReason(params.owner),
            actor: params.owner,
            ts_ms: now,
          });
        }
        return { lease: refreshed, transitions: [] };
      }

      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Thread ${params.threadId} is already owned by ${existing.owner?.kind ?? "unknown"}:${existing.owner?.id ?? "unknown"}`,
        { state: existing.state, owner: existing.owner, epoch: existing.epoch },
      );
    }

    if (existing.state !== "unowned") {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Cannot claim thread ${params.threadId} while lease is in state '${existing.state}'`,
        { state: existing.state, epoch: existing.epoch },
      );
    }

    const nextEpoch = existing.epoch + 1;
    const reason = params.reason ?? defaultClaimReason(params.owner);
    const lease: Lease = {
      id: existing.id,
      thread_id: params.threadId,
      epoch: nextEpoch,
      state: "owned",
      owner: params.owner,
      acquired_at_ms: now,
      ...(params.ttlMs !== undefined ? { expires_at_ms: now + params.ttlMs } : {}),
      updated_at_ms: now,
    };
    const transition: LeaseTransition = {
      thread_id: params.threadId,
      from_state: "unowned",
      to_state: "owned",
      from_epoch: existing.epoch,
      to_epoch: nextEpoch,
      reason,
      actor: params.owner,
      ts_ms: now,
    };
    this.store.commitLeaseTransition(lease, transition);
    return { lease, transitions: [transition] };
  }

  /**
   * Voluntarily releases an `owned` lease (`owned -> unowned`, `epoch + 1`).
   */
  async yield(params: LeaseYieldInput): Promise<LeaseTransitionResult> {
    const existing = this.store.getLease(params.threadId);
    if (!existing) {
      throw new LeaseError(
        RPC_NOT_FOUND,
        `No lease found for thread ${params.threadId}`,
      );
    }

    if (existing.epoch !== params.epoch) {
      throw new LeaseError(
        RPC_STALE_EPOCH,
        `Stale epoch on yield for thread ${params.threadId}: got ${params.epoch}, current ${existing.epoch}`,
        { expected: existing.epoch, got: params.epoch },
      );
    }

    if (existing.state !== "owned") {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Cannot yield thread ${params.threadId} in state '${existing.state}'`,
        { state: existing.state, epoch: existing.epoch },
      );
    }

    if (!existing.owner || !isSameOwner(existing.owner, params.owner)) {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Yield owner mismatch for thread ${params.threadId}`,
        { currentOwner: existing.owner, requestedOwner: params.owner },
      );
    }

    const now = this.now();
    const nextEpoch = existing.epoch + 1;
    const reason = params.reason ?? defaultYieldReason(params.owner);
    const lease: Lease = {
      id: existing.id,
      thread_id: params.threadId,
      epoch: nextEpoch,
      state: "unowned",
      updated_at_ms: now,
    };
    const transition: LeaseTransition = {
      thread_id: params.threadId,
      from_state: "owned",
      to_state: "unowned",
      from_epoch: existing.epoch,
      to_epoch: nextEpoch,
      reason,
      actor: params.owner,
      ts_ms: now,
    };
    this.store.commitLeaseTransition(lease, transition);
    return { lease, transitions: [transition] };
  }

  /**
   * Transfers or releases ownership through the fenced `draining`/`cancelling`
   * -> `stopping_and_flushing` -> `owned`/`unowned` (`epoch + 1`) pipeline.
   */
  async handoff(params: LeaseHandoffInput): Promise<LeaseTransitionResult> {
    const existing = this.store.getLease(params.threadId);
    if (!existing) {
      throw new LeaseError(
        RPC_NOT_FOUND,
        `No lease found for thread ${params.threadId}`,
      );
    }

    if (params.expectedEpoch !== undefined && existing.epoch !== params.expectedEpoch) {
      throw new LeaseError(
        RPC_STALE_EPOCH,
        `Stale epoch on handoff for thread ${params.threadId}: expected ${params.expectedEpoch}, current ${existing.epoch}`,
        { expected: params.expectedEpoch, current: existing.epoch },
      );
    }

    if (existing.state === "unowned") {
      if (params.targetOwner) {
        return this.claim({
          threadId: params.threadId,
          owner: params.targetOwner,
          ttlMs: params.ttlMs,
          reason: params.reason,
          expectedEpoch: params.expectedEpoch,
        });
      }
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Thread ${params.threadId} lease is already unowned`,
        { state: existing.state, epoch: existing.epoch },
      );
    }

    const force = params.force === true;
    const reason =
      params.reason ??
      defaultHandoffReason(params.targetOwner, existing.owner, force);
    const actor = params.targetOwner ?? existing.owner;
    const transitions: LeaseTransition[] = [];

    if (force) {
      if (existing.state !== "owned" && existing.state !== "draining") {
        throw new LeaseError(
          RPC_LEASE_CONFLICT,
          `Cannot force handoff while lease is in state '${existing.state}'`,
          { state: existing.state, epoch: existing.epoch },
        );
      }
      const cancellingLease = this.commitPhase(
        existing,
        "cancelling",
        existing.epoch,
        reason,
        actor,
        transitions,
      );
      await this.hooks.onCancel?.(cancellingLease, reason);
      return this.finishStopAndFlush(
        cancellingLease,
        "cancelling",
        params.targetOwner,
        params.ttlMs,
        reason,
        actor,
        transitions,
      );
    }

    if (existing.state !== "owned") {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Cannot start graceful handoff while lease is in state '${existing.state}'`,
        { state: existing.state, epoch: existing.epoch },
      );
    }

    const drainingLease = this.commitPhase(
      existing,
      "draining",
      existing.epoch,
      reason,
      actor,
      transitions,
    );
    await this.hooks.onDrain?.(drainingLease, reason);
    return this.finishStopAndFlush(
      drainingLease,
      "draining",
      params.targetOwner,
      params.ttlMs,
      reason,
      actor,
      transitions,
    );
  }

  /**
   * Shorthand for forced release to `unowned` (`epoch + 1`).
   */
  async cancel(params: LeaseCancelInput): Promise<LeaseTransitionResult> {
    return this.handoff({
      threadId: params.threadId,
      expectedEpoch: params.expectedEpoch,
      force: true,
      reason: params.reason ?? "cancel",
    });
  }

  /**
   * Asserts that `threadId` currently has an `owned` lease at `epoch` (and
   * optionally matches `ownerId`). Throws `LeaseError` otherwise.
   */
  assertFenced(threadId: ThreadId, epoch: Epoch, ownerId?: string): Lease {
    const lease = this.store.getLease(threadId);
    if (!lease) {
      throw new LeaseError(
        RPC_NOT_FOUND,
        `No lease found for thread ${threadId}`,
      );
    }
    if (lease.state !== "owned" || lease.epoch !== epoch) {
      throw new LeaseError(
        RPC_STALE_EPOCH,
        `Fencing check failed for thread ${threadId}: expected owned@${epoch}, got ${lease.state}@${lease.epoch}`,
        { expectedEpoch: epoch, currentEpoch: lease.epoch, state: lease.state },
      );
    }
    if (ownerId !== undefined && lease.owner?.id !== ownerId) {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Lease owner mismatch for thread ${threadId}: expected owner '${ownerId}', got '${lease.owner?.id ?? "none"}'`,
        { expectedOwnerId: ownerId, currentOwner: lease.owner },
      );
    }
    return lease;
  }

  private async finishStopAndFlush(
    priorLease: Lease,
    expectedPhase: LeaseState,
    targetOwner: LeaseOwner | undefined,
    ttlMs: number | undefined,
    reason: HandoffReason,
    actor: LeaseOwner | undefined,
    transitions: LeaseTransition[],
  ): Promise<LeaseTransitionResult> {
    // Re-read after async drain/cancel hook in case a forced preemption superseded us.
    const current = this.store.getLease(priorLease.thread_id);
    if (
      !current ||
      current.epoch !== priorLease.epoch ||
      current.state !== expectedPhase
    ) {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Lease transition for thread ${priorLease.thread_id} was preempted during '${expectedPhase}'`,
        { expectedState: expectedPhase, currentState: current?.state, currentEpoch: current?.epoch },
      );
    }

    const flushingLease = this.commitPhase(
      current,
      "stopping_and_flushing",
      current.epoch,
      reason,
      actor,
      transitions,
    );
    await this.hooks.onStopAndFlush?.(flushingLease, reason);

    const afterFlush = this.store.getLease(priorLease.thread_id);
    if (
      !afterFlush ||
      afterFlush.epoch !== priorLease.epoch ||
      afterFlush.state !== "stopping_and_flushing"
    ) {
      throw new LeaseError(
        RPC_LEASE_CONFLICT,
        `Lease transition for thread ${priorLease.thread_id} was preempted during 'stopping_and_flushing'`,
        { currentState: afterFlush?.state, currentEpoch: afterFlush?.epoch },
      );
    }

    const now = this.now();
    const nextEpoch = afterFlush.epoch + 1;
    const finalLease: Lease = targetOwner
      ? {
          id: afterFlush.id,
          thread_id: afterFlush.thread_id,
          epoch: nextEpoch,
          state: "owned",
          owner: targetOwner,
          acquired_at_ms: now,
          ...(ttlMs !== undefined ? { expires_at_ms: now + ttlMs } : {}),
          updated_at_ms: now,
        }
      : {
          id: afterFlush.id,
          thread_id: afterFlush.thread_id,
          epoch: nextEpoch,
          state: "unowned",
          updated_at_ms: now,
        };

    const finalTransition: LeaseTransition = {
      thread_id: afterFlush.thread_id,
      from_state: "stopping_and_flushing",
      to_state: finalLease.state,
      from_epoch: afterFlush.epoch,
      to_epoch: nextEpoch,
      reason,
      ...(actor ? { actor } : {}),
      ts_ms: now,
    };
    this.store.commitLeaseTransition(finalLease, finalTransition);
    transitions.push(finalTransition);
    return { lease: finalLease, transitions };
  }

  private commitPhase(
    current: Lease,
    toState: LeaseState,
    toEpoch: Epoch,
    reason: HandoffReason,
    actor: LeaseOwner | undefined,
    transitions: LeaseTransition[],
  ): Lease {
    const now = this.now();
    const nextLease: Lease = {
      ...current,
      state: toState,
      epoch: toEpoch,
      updated_at_ms: now,
    };
    const transition: LeaseTransition = {
      thread_id: current.thread_id,
      from_state: current.state,
      to_state: toState,
      from_epoch: current.epoch,
      to_epoch: toEpoch,
      reason,
      ...(actor ? { actor } : {}),
      ts_ms: now,
    };
    this.store.commitLeaseTransition(nextLease, transition);
    transitions.push(transition);
    return nextLease;
  }
}

import { randomUUID } from "node:crypto";
import type {
  BootId,
  Epoch,
  EventCursor,
  EventEnvelope,
  EventId,
  EventPayload,
  EventPushNotification,
  EventSubscriptionFilter,
  Seq,
  ThreadId,
  TimestampMs,
} from "@spaces/types";

const DEFAULT_MAX_EVENTS = 10_000;
const DEFAULT_TAIL_LIMIT = 100;

export interface EventStoreAdapter {
  nextSeq(bootId: BootId): Seq;
  appendEvent(envelope: EventEnvelope): boolean;
  getEventBounds(bootId: BootId): { earliestSeq: Seq; latestSeq: Seq } | null;
  listEventsAfter(
    bootId: BootId,
    afterSeq: Seq,
    limit: number,
    filter?: EventSubscriptionFilter,
  ): EventEnvelope[];
  trimEvents(bootId: BootId, maxRows: number): void;
}

export interface EventBusOptions {
  bootId?: BootId;
  maxEvents?: number;
  now?: () => TimestampMs;
  idGenerator?: () => EventId;
}

export interface PublishEventOptions {
  id?: EventId;
  threadId?: ThreadId;
  epoch?: Epoch;
  payload: EventPayload;
  tsMs?: TimestampMs;
}

export interface SubscribeResult {
  subscriptionId: string;
  cursor: EventCursor;
  replay: EventEnvelope[];
  gapReload?: EventEnvelope;
}

export interface TailResult {
  events: EventEnvelope[];
  cursor: EventCursor;
  hasMore: boolean;
}

interface Subscription {
  id: string;
  filter: EventSubscriptionFilter;
  onPush: (notification: EventPushNotification) => void;
}

/**
 * Evaluates whether `envelope` matches a subscription/tail filter's
 * `thread_ids` and `types` predicates.
 */
export function matchesEventFilter(
  envelope: EventEnvelope,
  filter?: EventSubscriptionFilter,
): boolean {
  if (!filter) {
    return true;
  }
  const threadIds = filter.thread_ids;
  if (Array.isArray(threadIds) && threadIds.length > 0) {
    if (!envelope.thread_id || !threadIds.includes(envelope.thread_id)) {
      return false;
    }
  }
  const types = filter.types;
  if (Array.isArray(types) && types.length > 0) {
    if (!types.includes(envelope.payload.type)) {
      return false;
    }
  }
  return true;
}

/**
 * Unified monotonic event bus backed by `EventStoreAdapter` with idempotent
 * deduplication, bounded replay, live subscriber fan-out, and gap detection.
 */
export class EventBus {
  readonly bootId: BootId;

  private readonly store: EventStoreAdapter;
  private readonly maxEvents: number;
  private readonly now: () => TimestampMs;
  private readonly idGenerator: () => EventId;
  private readonly subscribers = new Map<string, Subscription>();
  private subSeq = 0;

  constructor(store: EventStoreAdapter, options?: EventBusOptions);
  constructor(store: EventStoreAdapter, bootId: BootId, options?: Omit<EventBusOptions, "bootId">);
  constructor(
    store: EventStoreAdapter,
    bootIdOrOptions?: BootId | EventBusOptions,
    maybeOptions?: EventBusOptions,
  ) {
    this.store = store;
    const opts: EventBusOptions =
      typeof bootIdOrOptions === "string"
        ? { ...maybeOptions, bootId: bootIdOrOptions }
        : (bootIdOrOptions ?? {});

    this.bootId = opts.bootId ?? `boot_${randomUUID()}`;
    this.maxEvents = opts.maxEvents ?? DEFAULT_MAX_EVENTS;
    this.now = opts.now ?? (() => Date.now());
    this.idGenerator = opts.idGenerator ?? (() => `evt_${randomUUID()}`);
  }

  /**
   * Publishes an event onto the monotonic per-boot stream.
   *
   * Returns `null` if `appendEvent` deduplicates the event via `seen_events`.
   */
  publish(opts: PublishEventOptions): EventEnvelope | null {
    const seq = this.store.nextSeq(this.bootId);
    const envelope: EventEnvelope = {
      id: opts.id ?? this.idGenerator(),
      boot_id: this.bootId,
      seq,
      ts_ms: opts.tsMs ?? this.now(),
      ...(opts.threadId !== undefined ? { thread_id: opts.threadId } : {}),
      ...(opts.epoch !== undefined ? { epoch: opts.epoch } : {}),
      payload: opts.payload,
    };

    const inserted = this.store.appendEvent(envelope);
    if (!inserted) {
      return null;
    }

    if (this.maxEvents > 0 && Number.isFinite(this.maxEvents)) {
      this.store.trimEvents(this.bootId, this.maxEvents);
    }

    // Snapshot subscriber list so unsubscribe calls during delivery stay safe.
    for (const sub of Array.from(this.subscribers.values())) {
      if (!this.subscribers.has(sub.id)) {
        continue;
      }
      if (!matchesEventFilter(envelope, sub.filter)) {
        continue;
      }
      sub.onPush({
        subscription_id: sub.id,
        event: envelope,
      });
    }

    return envelope;
  }

  /**
   * Registers a live event subscription and replays missed events after `after_seq`,
   * or synthesizes a `GapReloadRequired` event if the client's cursor is stale.
   */
  subscribe(
    filter: EventSubscriptionFilter,
    onPush: (notification: EventPushNotification) => void,
  ): SubscribeResult {
    this.subSeq += 1;
    const subscriptionId = `sub_${this.subSeq}`;
    const normalizedFilter: EventSubscriptionFilter = {
      ...filter,
      thread_ids: [...(filter.thread_ids ?? [])],
      types: [...(filter.types ?? [])],
    };

    this.subscribers.set(subscriptionId, {
      id: subscriptionId,
      filter: normalizedFilter,
      onPush,
    });

    const bounds = this.store.getEventBounds(this.bootId);
    const latestSeq = bounds?.latestSeq ?? 0;
    const cursor: EventCursor = { boot_id: this.bootId, seq: latestSeq };

    const gapReason = this.detectGapReason(normalizedFilter, bounds);
    if (gapReason !== null) {
      return {
        subscriptionId,
        cursor,
        replay: [],
        gapReload: this.buildGapReloadEvent(bounds, gapReason),
      };
    }

    if (normalizedFilter.after_seq === undefined || bounds === null) {
      return {
        subscriptionId,
        cursor,
        replay: [],
      };
    }

    const replayLimit = Math.max(1, bounds.latestSeq - normalizedFilter.after_seq);
    const replay = this.store
      .listEventsAfter(this.bootId, normalizedFilter.after_seq, replayLimit, normalizedFilter)
      .filter((event) => matchesEventFilter(event, normalizedFilter));

    return {
      subscriptionId,
      cursor,
      replay,
    };
  }

  /**
   * Removes an active subscription by ID.
   */
  unsubscribe(subscriptionId: string): boolean {
    return this.subscribers.delete(subscriptionId);
  }

  /**
   * Reads a bounded page of retained events matching `filter` without creating a subscription.
   */
  tail(filter: EventSubscriptionFilter, limit = DEFAULT_TAIL_LIMIT): TailResult {
    const bounds = this.store.getEventBounds(this.bootId);
    const latestSeq = bounds?.latestSeq ?? 0;
    const normalizedFilter: EventSubscriptionFilter = {
      ...filter,
      thread_ids: [...(filter.thread_ids ?? [])],
      types: [...(filter.types ?? [])],
    };

    const gapReason = this.detectGapReason(normalizedFilter, bounds);
    if (gapReason !== null) {
      return {
        events: [this.buildGapReloadEvent(bounds, gapReason)],
        cursor: { boot_id: this.bootId, seq: latestSeq },
        hasMore: false,
      };
    }

    if (limit <= 0 || bounds === null) {
      const emptySeq =
        bounds === null
          ? (normalizedFilter.after_seq ?? 0)
          : (normalizedFilter.after_seq ?? Math.max(0, bounds.earliestSeq - 1));
      return {
        events: [],
        cursor: { boot_id: this.bootId, seq: emptySeq },
        hasMore: false,
      };
    }

    const afterSeq =
      normalizedFilter.after_seq ?? Math.max(0, bounds.earliestSeq - 1);
    const fetched = this.store
      .listEventsAfter(this.bootId, afterSeq, limit + 1, normalizedFilter)
      .filter((event) => matchesEventFilter(event, normalizedFilter));

    const hasMore = fetched.length > limit;
    const events = hasMore ? fetched.slice(0, limit) : fetched;
    const cursorSeq = hasMore
      ? events[events.length - 1]!.seq
      : latestSeq;

    return {
      events,
      cursor: { boot_id: this.bootId, seq: cursorSeq },
      hasMore,
    };
  }

  /**
   * Returns the number of active subscribers.
   */
  subscriberCount(): number {
    return this.subscribers.size;
  }

  private detectGapReason(
    filter: EventSubscriptionFilter,
    bounds: { earliestSeq: Seq; latestSeq: Seq } | null,
  ): string | null {
    if (filter.boot_id !== undefined && filter.boot_id !== this.bootId) {
      return `stale boot_id '${filter.boot_id}' does not match current '${this.bootId}'`;
    }
    if (filter.after_seq === undefined) {
      return null;
    }
    if (bounds === null) {
      if (filter.after_seq > 0) {
        return `after_seq ${filter.after_seq} exceeds empty replay log`;
      }
      return null;
    }
    if (filter.after_seq + 1 < bounds.earliestSeq) {
      return `after_seq ${filter.after_seq} fell behind earliest retained seq ${bounds.earliestSeq}`;
    }
    if (filter.after_seq > bounds.latestSeq) {
      return `after_seq ${filter.after_seq} exceeds latest seq ${bounds.latestSeq}`;
    }
    return null;
  }

  private buildGapReloadEvent(
    bounds: { earliestSeq: Seq; latestSeq: Seq } | null,
    reason: string,
  ): EventEnvelope {
    return {
      id: this.idGenerator(),
      boot_id: this.bootId,
      seq: bounds?.latestSeq ?? 0,
      ts_ms: this.now(),
      payload: {
        type: "gap_reload_required",
        earliest_seq: bounds?.earliestSeq ?? 0,
        latest_seq: bounds?.latestSeq ?? 0,
        reason,
      },
    };
  }
}

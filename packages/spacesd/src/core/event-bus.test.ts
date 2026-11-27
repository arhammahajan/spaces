import { describe, expect, test } from "bun:test";
import type {
  BootId,
  EventEnvelope,
  EventId,
  EventPayload,
  EventPushNotification,
  EventSubscriptionFilter,
  Seq,
} from "@spaces/types";
import {
  EventBus,
  matchesEventFilter,
  type EventStoreAdapter,
} from "./event-bus";

class MemoryEventStore implements EventStoreAdapter {
  private readonly seqByBoot = new Map<BootId, Seq>();
  private readonly eventsByBoot = new Map<BootId, EventEnvelope[]>();
  private readonly seenIds = new Set<EventId>();

  nextSeq(bootId: BootId): Seq {
    const next = (this.seqByBoot.get(bootId) ?? 0) + 1;
    this.seqByBoot.set(bootId, next);
    return next;
  }

  appendEvent(envelope: EventEnvelope): boolean {
    if (this.seenIds.has(envelope.id)) {
      return false;
    }
    this.seenIds.add(envelope.id);
    const list = this.eventsByBoot.get(envelope.boot_id) ?? [];
    list.push(envelope);
    this.eventsByBoot.set(envelope.boot_id, list);
    return true;
  }

  getEventBounds(bootId: BootId): { earliestSeq: Seq; latestSeq: Seq } | null {
    const list = this.eventsByBoot.get(bootId);
    if (!list || list.length === 0) {
      return null;
    }
    return {
      earliestSeq: list[0]!.seq,
      latestSeq: list[list.length - 1]!.seq,
    };
  }

  listEventsAfter(
    bootId: BootId,
    afterSeq: Seq,
    limit: number,
    filter?: EventSubscriptionFilter,
  ): EventEnvelope[] {
    const list = this.eventsByBoot.get(bootId) ?? [];
    return list
      .filter((e) => e.seq > afterSeq && matchesEventFilter(e, filter))
      .slice(0, limit);
  }

  trimEvents(bootId: BootId, maxRows: number): void {
    const list = this.eventsByBoot.get(bootId);
    if (!list || list.length <= maxRows) {
      return;
    }
    const trimmed = list.slice(list.length - maxRows);
    this.eventsByBoot.set(bootId, trimmed);
  }
}

function outcomePayload(summary: string): EventPayload {
  return {
    type: "outcome_recorded",
    outcome: {
      session: "main",
      pane: "%1",
      agent: "claude",
      app: "claude",
      profile: "default",
      outcome: "done",
      text: summary,
      title: "Task",
      model: "sonnet",
      session_id: "sess-1",
      cwd: "/repo",
      ts_ms: 1_700_000_000_000,
    },
  };
}

function oscPayload(state: "working" | "done"): EventPayload {
  return {
    type: "osc_status_received",
    pane_id: "%1",
    status: { state, app: "claude" },
  };
}

describe("EventBus", () => {
  test("publishes monotonic envelopes, deduplicates seen event IDs, and fans out with filters", () => {
    const store = new MemoryEventStore();
    let clock = 10_000;
    const bus = new EventBus(store, "boot-1", { now: () => clock });

    const allEvents: EventPushNotification[] = [];
    const thr1OutcomeOnly: EventPushNotification[] = [];

    const subAll = bus.subscribe({ thread_ids: [], types: [] }, (n) => {
      allEvents.push(n);
    });
    const subFiltered = bus.subscribe(
      { thread_ids: ["thr-1"], types: ["outcome_recorded"] },
      (n) => {
        thr1OutcomeOnly.push(n);
      },
    );

    expect(bus.subscriberCount()).toBe(2);
    expect(subAll.cursor).toEqual({ boot_id: "boot-1", seq: 0 });
    expect(subAll.replay).toEqual([]);
    expect(subAll.gapReload).toBeUndefined();

    const e1 = bus.publish({
      id: "evt-dup",
      threadId: "thr-1",
      epoch: 1,
      payload: outcomePayload("first"),
    });
    expect(e1).not.toBeNull();
    expect(e1?.seq).toBe(1);
    expect(e1?.boot_id).toBe("boot-1");
    expect(e1?.ts_ms).toBe(10_000);

    // Duplicate event ID is ignored
    const dup = bus.publish({
      id: "evt-dup",
      threadId: "thr-1",
      payload: outcomePayload("duplicate"),
    });
    expect(dup).toBeNull();

    clock = 10_500;
    const e2 = bus.publish({
      id: "evt-2",
      threadId: "thr-1",
      payload: oscPayload("working"),
    });
    expect(e2?.seq).toBe(3);

    const e3 = bus.publish({
      id: "evt-3",
      threadId: "thr-2",
      payload: outcomePayload("other thread"),
    });
    expect(e3?.seq).toBe(4);

    expect(allEvents.map((n) => n.event.id)).toEqual([
      "evt-dup",
      "evt-2",
      "evt-3",
    ]);
    expect(allEvents.every((n) => n.subscription_id === subAll.subscriptionId)).toBe(
      true,
    );
    expect(thr1OutcomeOnly.map((n) => n.event.id)).toEqual(["evt-dup"]);

    expect(bus.unsubscribe(subFiltered.subscriptionId)).toBe(true);
    expect(bus.unsubscribe(subFiltered.subscriptionId)).toBe(false);
    expect(bus.subscriberCount()).toBe(1);
  });

  test("replays missed events when within retained window and synthesizes GapReloadRequired on stale cursor or boot_id", () => {
    const store = new MemoryEventStore();
    const bus = new EventBus(store, "boot-2", { maxEvents: 3, now: () => 50_000 });

    // Publish 2 events before trim -> [1, 2]
    bus.publish({ id: "e1", threadId: "thr-1", payload: oscPayload("working") });
    bus.publish({ id: "e2", threadId: "thr-2", payload: oscPayload("done") });

    const replayFromZero = bus.subscribe(
      { boot_id: "boot-2", after_seq: 0, thread_ids: [], types: [] },
      () => {},
    );
    expect(replayFromZero.gapReload).toBeUndefined();
    expect(replayFromZero.replay.map((e) => e.seq)).toEqual([1, 2]);
    expect(replayFromZero.cursor).toEqual({ boot_id: "boot-2", seq: 2 });

    // Publish 3 more -> retained buffer trims to [3, 4, 5]
    bus.publish({ id: "e3", threadId: "thr-1", payload: outcomePayload("3") });
    bus.publish({ id: "e4", threadId: "thr-1", payload: outcomePayload("4") });
    bus.publish({ id: "e5", threadId: "thr-2", payload: outcomePayload("5") });

    // Cursor right at earliestSeq - 1 (seq 2) has missed nothing -> replays [3, 4, 5]
    const exactBoundary = bus.subscribe(
      { boot_id: "boot-2", after_seq: 2, thread_ids: ["thr-1"], types: [] },
      () => {},
    );
    expect(exactBoundary.gapReload).toBeUndefined();
    expect(exactBoundary.replay.map((e) => e.seq)).toEqual([3, 4]);

    // Cursor fell behind (after_seq: 1 < earliestSeq - 1) -> GapReloadRequired
    const fellBehind = bus.subscribe(
      { boot_id: "boot-2", after_seq: 1, thread_ids: [], types: [] },
      () => {},
    );
    expect(fellBehind.replay).toEqual([]);
    expect(fellBehind.gapReload).toBeDefined();
    expect(fellBehind.gapReload?.payload).toEqual({
      type: "gap_reload_required",
      earliest_seq: 3,
      latest_seq: 5,
      reason: expect.stringContaining("fell behind"),
    });

    // Stale boot_id (daemon restarted) -> GapReloadRequired
    const staleBoot = bus.subscribe(
      { boot_id: "boot-1", after_seq: 4, thread_ids: [], types: [] },
      () => {},
    );
    expect(staleBoot.replay).toEqual([]);
    expect(staleBoot.gapReload?.payload).toMatchObject({
      type: "gap_reload_required",
      earliest_seq: 3,
      latest_seq: 5,
    });
    expect(staleBoot.cursor).toEqual({ boot_id: "boot-2", seq: 5 });
  });

  test("tail paginates with cursor and hasMore and reports gap reload on stale boot_id", () => {
    const store = new MemoryEventStore();
    const bus = new EventBus(store, "boot-tail", { maxEvents: 10 });

    const emptyTail = bus.tail({ thread_ids: [], types: [] }, 5);
    expect(emptyTail).toEqual({
      events: [],
      cursor: { boot_id: "boot-tail", seq: 0 },
      hasMore: false,
    });

    for (let i = 1; i <= 5; i += 1) {
      bus.publish({
        id: `t-${i}`,
        threadId: i % 2 === 1 ? "thr-odd" : "thr-even",
        payload: outcomePayload(`m-${i}`),
      });
    }

    const page1 = bus.tail(
      { after_seq: 0, thread_ids: ["thr-odd"], types: [] },
      2,
    );
    expect(page1.events.map((e) => e.seq)).toEqual([1, 3]);
    expect(page1.hasMore).toBe(true);
    expect(page1.cursor).toEqual({ boot_id: "boot-tail", seq: 3 });

    const page2 = bus.tail(
      { after_seq: page1.cursor.seq, thread_ids: ["thr-odd"], types: [] },
      2,
    );
    expect(page2.events.map((e) => e.seq)).toEqual([5]);
    expect(page2.hasMore).toBe(false);
    expect(page2.cursor).toEqual({ boot_id: "boot-tail", seq: 5 });

    const stale = bus.tail(
      { boot_id: "boot-old", after_seq: 1, thread_ids: [], types: [] },
      10,
    );
    expect(stale.hasMore).toBe(false);
    expect(stale.events).toHaveLength(1);
    expect(stale.events[0]?.payload.type).toBe("gap_reload_required");
  });

  test("handles subscriber self-unsubscription during publish safely", () => {
    const store = new MemoryEventStore();
    const bus = new EventBus(store, "boot-safe");
    const seenSecond: string[] = [];

    let sub1Id = "";
    const sub1 = bus.subscribe({ thread_ids: [], types: [] }, () => {
      bus.unsubscribe(sub1Id);
    });
    sub1Id = sub1.subscriptionId;

    bus.subscribe({ thread_ids: [], types: [] }, (n) => {
      seenSecond.push(n.event.id);
    });

    bus.publish({ id: "first", payload: oscPayload("working") });
    bus.publish({ id: "second", payload: oscPayload("done") });

    expect(seenSecond).toEqual(["first", "second"]);
    expect(bus.subscriberCount()).toBe(1);
  });
});

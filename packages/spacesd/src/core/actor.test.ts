import { describe, expect, test } from "bun:test";
import { ThreadActor, ThreadActorRegistry } from "./actor";

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("ThreadActor", () => {
  test("executes concurrent tasks strictly sequentially in FIFO order", async () => {
    const actor = new ThreadActor("thr-1");
    const trace: string[] = [];
    let active = 0;
    let maxActive = 0;

    const firstGate = deferred<void>();
    const tasks = [
      actor.enqueue(async () => {
        active += 1;
        maxActive = Math.max(maxActive, active);
        trace.push("start:0");
        await firstGate.promise;
        trace.push("end:0");
        active -= 1;
        return "r0";
      }),
      actor.enqueue(async () => {
        active += 1;
        maxActive = Math.max(maxActive, active);
        trace.push("start:1");
        await Bun.sleep(5);
        trace.push("end:1");
        active -= 1;
        return "r1";
      }),
      actor.enqueue(() => {
        active += 1;
        maxActive = Math.max(maxActive, active);
        trace.push("sync:2");
        active -= 1;
        return "r2";
      }),
    ];

    expect(actor.pendingCount()).toBe(3);
    expect(actor.isIdle()).toBe(false);

    firstGate.resolve();
    const results = await Promise.all(tasks);

    expect(results).toEqual(["r0", "r1", "r2"]);
    expect(maxActive).toBe(1);
    expect(trace).toEqual([
      "start:0",
      "end:0",
      "start:1",
      "end:1",
      "sync:2",
    ]);
    expect(actor.pendingCount()).toBe(0);
    expect(actor.isIdle()).toBe(true);
  });

  test("isolates sync and async errors without wedging subsequent queued tasks", async () => {
    const actor = new ThreadActor("thr-err");
    const order: string[] = [];

    const p1 = actor.enqueue(async () => {
      order.push("t1");
      throw new Error("async boom");
    });
    const p2 = actor.enqueue(() => {
      order.push("t2");
      throw new Error("sync boom");
    });
    const p3 = actor.enqueue(async () => {
      order.push("t3");
      return 42;
    });

    const [r1, r2, r3] = await Promise.allSettled([p1, p2, p3]);
    expect(r1.status).toBe("rejected");
    if (r1.status === "rejected") {
      expect((r1.reason as Error).message).toBe("async boom");
    }
    expect(r2.status).toBe("rejected");
    if (r2.status === "rejected") {
      expect((r2.reason as Error).message).toBe("sync boom");
    }
    expect(r3).toEqual({ status: "fulfilled", value: 42 });
    expect(order).toEqual(["t1", "t2", "t3"]);
    expect(actor.pendingCount()).toBe(0);
    expect(actor.isIdle()).toBe(true);
  });

  test("drain waits for queued and dynamically appended tasks", async () => {
    const actor = new ThreadActor("thr-drain");
    const gate = deferred<void>();
    let finishedFollowUp = false;

    const p1 = actor.enqueue(async () => {
      await gate.promise;
      void actor.enqueue(async () => {
        await Bun.sleep(5);
        finishedFollowUp = true;
      });
    });

    const drainPromise = actor.drain();
    gate.resolve();
    await p1;
    await drainPromise;

    expect(finishedFollowUp).toBe(true);
    expect(actor.isIdle()).toBe(true);
  });
});

describe("ThreadActorRegistry", () => {
  test("serializes per thread while running distinct threads concurrently", async () => {
    const registry = new ThreadActorRegistry();
    const gateA = deferred<void>();
    const gateB = deferred<void>();
    const events: string[] = [];

    const a1 = registry.enqueue("thr-a", async () => {
      events.push("a1:start");
      await gateA.promise;
      events.push("a1:end");
      return "a1";
    });
    const a2 = registry.enqueue("thr-a", async () => {
      events.push("a2:run");
      return "a2";
    });
    const b1 = registry.enqueue("thr-b", async () => {
      events.push("b1:start");
      await gateB.promise;
      events.push("b1:end");
      return "b1";
    });

    // Wait a microtask so a1 and b1 both enter execution.
    await Promise.resolve();
    await Promise.resolve();

    expect(events).toEqual(["a1:start", "b1:start"]);
    expect(registry.size()).toBe(2);
    expect(registry.pendingCount("thr-a")).toBe(2);
    expect(registry.pendingCount("thr-b")).toBe(1);
    expect(registry.pendingCount("thr-missing")).toBe(0);
    expect(registry.pendingCount()).toBe(3);

    // b1 completes independently while thr-a is still blocked on gateA.
    gateB.resolve();
    expect(await b1).toBe("b1");
    expect(events).toEqual(["a1:start", "b1:start", "b1:end"]);
    expect(registry.size()).toBe(1);
    expect(registry.pendingCount()).toBe(2);

    gateA.resolve();
    expect(await Promise.all([a1, a2])).toEqual(["a1", "a2"]);
    expect(events).toEqual([
      "a1:start",
      "b1:start",
      "b1:end",
      "a1:end",
      "a2:run",
    ]);
    expect(registry.size()).toBe(0);
    expect(registry.pendingCount()).toBe(0);
  });

  test("drainAll settles all in-flight, queued, and spawned tasks and cleans up idle actors", async () => {
    const registry = new ThreadActorRegistry();
    const settled: string[] = [];
    const gate = deferred<void>();

    const p1 = registry.enqueue("thr-1", async () => {
      await gate.promise;
      settled.push("thr-1:first");
      void registry.enqueue("thr-3", async () => {
        await Bun.sleep(5);
        settled.push("thr-3:spawned");
      });
    });

    const p2 = registry.enqueue("thr-2", async () => {
      await Bun.sleep(5);
      settled.push("thr-2:done");
      throw new Error("worker failure");
    });

    expect(registry.size()).toBe(2);
    const drainPromise = registry.drainAll();
    gate.resolve();

    await expect(p2).rejects.toThrow("worker failure");
    await p1;
    await drainPromise;

    expect(settled).toContain("thr-1:first");
    expect(settled).toContain("thr-2:done");
    expect(settled).toContain("thr-3:spawned");
    expect(registry.size()).toBe(0);
    expect(registry.pendingCount()).toBe(0);
  });

  test("drainAll on empty registry resolves immediately", async () => {
    const registry = new ThreadActorRegistry();
    await registry.drainAll();
    expect(registry.size()).toBe(0);
    expect(registry.pendingCount()).toBe(0);
  });
});

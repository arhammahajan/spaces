import type { ThreadId } from "@spaces/types";

interface QueueItem<T> {
  task: () => Promise<T> | T;
  resolve: (value: T | PromiseLike<T>) => void;
  reject: (reason?: unknown) => void;
}

/**
 * FIFO serial async execution queue for a single managed thread.
 *
 * Concurrent callers share serialized execution in exact submission order;
 * errors reject only the failing caller's promise without wedging subsequent tasks.
 */
export class ThreadActor {
  readonly threadId: ThreadId;

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private readonly queue: QueueItem<any>[] = [];
  private running = false;
  private inFlight = false;
  private readonly drainWaiters: Array<() => void> = [];
  private readonly onIdle?: () => void;

  constructor(threadId: ThreadId = "", onIdle?: () => void) {
    this.threadId = threadId;
    this.onIdle = onIdle;
  }

  /**
   * Submits a task to execute after all previously queued tasks on this actor.
   */
  enqueue<T>(task: () => Promise<T> | T): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      this.queue.push({ task, resolve, reject });
      if (!this.running) {
        this.running = true;
        queueMicrotask(() => {
          void this.pump();
        });
      }
    });
  }

  /**
   * Returns the count of tasks currently in-flight or waiting in the queue.
   */
  pendingCount(): number {
    return this.queue.length + (this.inFlight ? 1 : 0);
  }

  /**
   * Returns true when no task is executing, scheduled, or queued.
   */
  isIdle(): boolean {
    return !this.running && !this.inFlight && this.queue.length === 0;
  }

  /**
   * Resolves once all currently in-flight and queued tasks have settled.
   */
  async drain(): Promise<void> {
    while (!this.isIdle()) {
      await new Promise<void>((resolve) => {
        this.drainWaiters.push(resolve);
      });
    }
  }

  private async pump(): Promise<void> {
    while (this.queue.length > 0) {
      const item = this.queue.shift()!;
      this.inFlight = true;
      try {
        const result = await item.task();
        this.inFlight = false;
        item.resolve(result);
      } catch (error) {
        this.inFlight = false;
        item.reject(error);
      }
    }

    this.running = false;
    const waiters = this.drainWaiters.splice(0);
    if (this.isIdle()) {
      this.onIdle?.();
    }
    for (const resolveDrain of waiters) {
      resolveDrain();
    }
  }
}

/**
 * Registry of per-thread `ThreadActor` queues enabling parallel execution
 * across distinct threads while strictly serializing operations within a thread.
 */
export class ThreadActorRegistry {
  private readonly actors = new Map<ThreadId, ThreadActor>();

  /**
   * Enqueues a task on the actor for `threadId`, creating it lazily if needed.
   */
  enqueue<T>(threadId: ThreadId, task: () => Promise<T> | T): Promise<T> {
    let actor = this.actors.get(threadId);
    if (!actor) {
      const created = new ThreadActor(threadId, () => {
        if (this.actors.get(threadId) === created && created.isIdle()) {
          this.actors.delete(threadId);
        }
      });
      actor = created;
      this.actors.set(threadId, actor);
    }
    return actor.enqueue(task);
  }

  /**
   * Returns the number of currently active actors in the registry.
   */
  size(): number {
    return this.actors.size;
  }

  /**
   * Returns the pending (in-flight + queued) task count for a specific thread,
   * or the aggregate count across all threads when `threadId` is omitted.
   */
  pendingCount(threadId?: ThreadId): number {
    if (threadId !== undefined) {
      return this.actors.get(threadId)?.pendingCount() ?? 0;
    }
    let total = 0;
    for (const actor of this.actors.values()) {
      total += actor.pendingCount();
    }
    return total;
  }

  /**
   * Graceful shutdown: waits for all in-flight and queued tasks across all
   * actors to settle and cleans up idle actors.
   */
  async drainAll(): Promise<void> {
    while (true) {
      const actors = Array.from(this.actors.values());
      if (actors.length === 0) {
        return;
      }

      const activeDrains = actors
        .filter((actor) => !actor.isIdle())
        .map((actor) => actor.drain());
      if (activeDrains.length > 0) {
        await Promise.all(activeDrains);
      }

      for (const [threadId, actor] of this.actors.entries()) {
        if (actor.isIdle()) {
          this.actors.delete(threadId);
        }
      }

      let hasActive = false;
      for (const actor of this.actors.values()) {
        if (!actor.isIdle()) {
          hasActive = true;
          break;
        }
      }
      if (!hasActive) {
        return;
      }
    }
  }
}

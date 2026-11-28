import { describe, expect, test } from "bun:test";
import type { EventEnvelope, Pane, Space } from "@spaces/types";
import { EventBus, type EventStoreAdapter } from "../core/event-bus";
import { SpacesStore } from "../store/db";
import { HookIngestor, normalizeHookEvent } from "./ingest";
import {
  MOCHA_BLUE,
  MOCHA_GREEN,
  MOCHA_RED,
  MOCHA_YELLOW,
  bucketOf,
  computeStatusBar,
} from "./statusbar";

function asEventStoreAdapter(store: SpacesStore): EventStoreAdapter {
  return {
    nextSeq: (bootId) => store.nextSeq(bootId),
    appendEvent: (env) => store.appendEvent(env),
    getEventBounds: (bootId) => {
      const b = store.getEventBounds(bootId);
      if (b.earliestSeq === null || b.latestSeq === null) {
        return null;
      }
      return { earliestSeq: b.earliestSeq, latestSeq: b.latestSeq };
    },
    listEventsAfter: (bootId, afterSeq, limit, filter) =>
      store.listEventsAfter(bootId, afterSeq, limit, filter),
    trimEvents: (bootId, maxRows) => store.trimEvents(bootId, maxRows),
  };
}

function makePane(status: Pane["status"], id = "%1"): Pane {
  return {
    id,
    numeric_id: 1,
    pane_index: 0,
    window_id: "@1",
    window_index: 0,
    window_name: "main",
    title: "",
    conversation_title: "",
    base_conv_title: "",
    screen_title: "",
    resume_session: "",
    profile: "",
    command: "zsh",
    start_command: "zsh",
    foreground_cmd: "zsh",
    launch_command: [],
    child_process_count: 0,
    app: "",
    status,
    base_status: status,
    text: "",
    base_text: "",
    kind: "",
    source: "",
    hook: null,
    git: null,
    cwd: "/tmp",
    age_ms: 0,
    base_age_ms: 0,
    width: 80,
    height: 24,
    is_active: true,
    window_active: true,
    in_mode: false,
    synchronized: false,
    is_dead: false,
    dead_status: 0,
    has_spaces_meta: false,
    returned_to_shell: false,
    pid: 123,
  };
}

function makeSpace(name: string, panes: Pane[]): Space {
  return {
    name,
    id: `$${name}`,
    is_current: false,
    windows: [],
    panes,
    cwd: "/tmp",
    git: null,
    worktree_path: "",
    worktree_name: "",
    worktree_base: "",
    worktree_managed: false,
    counts: { needs: 0, failed: 0, working: 0, done: 0, quiet: 0, new_output: 0, idle: 0 },
    status: "idle",
    connected_clients: 0,
    created_at_ms: 0,
  };
}

describe("normalizeHookEvent", () => {
  test("normalizes Claude events (prompt, pre_tool, ask tool, permission, auth, stop)", () => {
    const promptEv = normalizeHookEvent("claude", "UserPromptSubmit", {
      prompt: "Fix the failing unit test",
      session_id: "sess-123",
      cwd: "/repo/app",
      model: "claude-opus-4-6",
    });
    expect(promptEv).not.toBeNull();
    expect(promptEv!.agent).toBe("claude");
    expect(promptEv!.app).toBe("claude-code");
    expect(promptEv!.event).toBe("prompt");
    expect(promptEv!.state).toBe("working");
    expect(promptEv!.kind).toBeNull();
    expect(promptEv!.text).toBe("prompt: Fix the failing unit test");
    expect(promptEv!.title).toBe("Fix the failing unit test");
    expect(promptEv!.sessionId).toBe("sess-123");
    expect(promptEv!.cwd).toBe("/repo/app");
    expect(promptEv!.model).toBe("claude-opus-4-6");

    const preTool = normalizeHookEvent("claude", "PreToolUse", {
      tool_name: "Bash",
      tool_input: { command: "bun test" },
    });
    expect(preTool!.state).toBe("working");
    expect(preTool!.text).toBe("Bash · bun test");
    expect(preTool!.tool).toEqual({ name: "Bash", input: "bun test", output: "" });

    const askTool = normalizeHookEvent("claude", "PreToolUse", {
      tool_name: "AskUserQuestion",
      tool_input: { prompt: "Proceed with migration?" },
    });
    expect(askTool!.state).toBe("blocked");
    expect(askTool!.kind).toBe("question");

    const perm = normalizeHookEvent("claude", "PermissionRequest", {
      tool_name: "Bash",
      tool_input: { command: "rm -rf build" },
    });
    expect(perm!.state).toBe("blocked");
    expect(perm!.kind).toBe("permission");
    expect(perm!.text).toBe("approve Bash · rm -rf build");

    const auth = normalizeHookEvent("claude", "PermissionRequest", {
      message: "Please sign in to continue",
    });
    expect(auth!.state).toBe("blocked");
    expect(auth!.kind).toBe("auth");

    const stopDone = normalizeHookEvent("claude", "Stop", {
      last_assistant_message: "All tests pass now.",
      usage: { input_tokens: 1000, output_tokens: 250, cost_usd: 0.02 },
    });
    expect(stopDone!.state).toBe("done");
    expect(stopDone!.text).toBe("All tests pass now.");
    expect(stopDone!.reply).toBe("All tests pass now.");
    expect(stopDone!.usage).toEqual({
      input_tokens: 1000,
      output_tokens: 250,
      reasoning_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: 0.02,
    });

    const stopErr = normalizeHookEvent("claude", "Stop", {
      terminationReason: "ERROR",
      error_message: "Rate limit exceeded",
    });
    expect(stopErr!.state).toBe("failed");
    expect(stopErr!.text).toBe("error: Rate limit exceeded");

    // Stateless notification returns null
    expect(
      normalizeHookEvent("claude", "Notification", {
        notification_type: "info",
        message: "FYI",
      }),
    ).toBeNull();
  });

  test("normalizes Codex events including subagent agent_id guard", () => {
    expect(normalizeHookEvent("codex", "SubagentStart", {})).toBeNull();

    const sub = normalizeHookEvent("codex", "SubagentStart", {
      agent_id: "worker-1",
      description: "Run typecheck",
    });
    expect(sub).not.toBeNull();
    expect(sub!.state).toBe("working");
    expect(sub!.text).toBe("subagent: Run typecheck");

    const turnEnd = normalizeHookEvent("codex", "turnEnd", {
      finalModelOutput: "Implemented feature",
      threadId: "thr-9",
    });
    expect(turnEnd!.event).toBe("stop");
    expect(turnEnd!.state).toBe("done");
    expect(turnEnd!.sessionId).toBe("thr-9");
  });

  test("normalizes generic events, aliases, and unknown events", () => {
    const agy = normalizeHookEvent("agy", "working", {
      conversationTitle: "Refactor daemon",
    });
    expect(agy!.agent).toBe("antigravity");
    expect(agy!.state).toBe("working");
    expect(agy!.title).toBe("Refactor daemon");

    expect(normalizeHookEvent("generic", "error", {})!.state).toBe("failed");
    expect(normalizeHookEvent("generic", "blocked", {})).toMatchObject({
      state: "blocked",
      kind: "permission",
    });
    expect(normalizeHookEvent("generic", "idle", {})!.state).toBe("idle");
    expect(normalizeHookEvent("generic", "totally_unknown_event", {})).toBeNull();
  });
});

describe("HookIngestor", () => {
  test("emit upserts hook_panes, preserves state_since_ms & metadata, records outcomes, and publishes events", () => {
    let clock = 1_000_000;
    const store = new SpacesStore(":memory:", { now: () => clock });
    const bus = new EventBus(asEventStoreAdapter(store), { bootId: "boot_test", now: () => clock });
    const ingestor = new HookIngestor(store, bus, { now: () => clock });

    const pushed: EventEnvelope[] = [];
    bus.subscribe({ thread_ids: [], types: [] }, (n) => pushed.push(n.event));

    // 1. Prompt starts a turn
    const r1 = ingestor.emit({
      agent: "claude",
      event: "UserPromptSubmit",
      session: "work",
      pane_ref: "%42",
      profile: "default",
      session_id: "sid-42",
      cwd: "/repo/spaces",
      payload: { prompt: "Ship Track C", model: "claude-opus-4-6" },
    });
    expect(r1).toEqual({ ok: true, state: "working" });

    // 2. PreToolUse stays working -> state_since_ms & turn_started_ms preserved, metadata preserved
    clock += 5_000;
    const r2 = ingestor.emit({
      agent: "claude",
      event: "PreToolUse",
      session: "work",
      pane: "42",
      payload: { tool_name: "Bash", tool_input: { command: "bun test" } },
    });
    expect(r2).toEqual({ ok: true, state: "working" });

    const detailWorking = ingestor.getDetail("work", 42);
    expect(detailWorking).not.toBeNull();
    expect(detailWorking!.state).toBe("working");
    expect(detailWorking!.state_since_ms).toBe(1_000_000);
    expect(detailWorking!.turn_started_ms).toBe(1_000_000);
    expect(detailWorking!.ts_ms).toBe(1_005_000);
    expect(detailWorking!.title).toBe("Ship Track C");
    expect(detailWorking!.session_id).toBe("sid-42");
    expect(detailWorking!.cwd).toBe("/repo/spaces");
    expect(detailWorking!.model).toBe("claude-opus-4-6");
    expect(detailWorking!.tool).toEqual({ name: "Bash", input: "bun test", output: "" });
    expect(detailWorking!.has_detail).toBe(true);

    // 3. Stop -> done updates state_since_ms, records outcome, publishes outcome_recorded + hook_pane_updated
    clock += 10_000;
    const r3 = ingestor.emit({
      agent: "claude",
      event: "Stop",
      session: "work",
      pane: "42",
      payload: { last_assistant_message: "All green!" },
    });
    expect(r3).toEqual({ ok: true, state: "done" });

    const detailDone = ingestor.getDetail("work", 42);
    expect(detailDone!.state).toBe("done");
    expect(detailDone!.state_since_ms).toBe(1_015_000);
    expect(detailDone!.turn_started_ms).toBe(1_000_000);
    expect(detailDone!.reply).toBe("All green!");

    const outcomes = store.listOutcomes(clock);
    expect(outcomes).toHaveLength(1);
    expect(outcomes[0]!.outcome).toBe("done");
    expect(outcomes[0]!.session_id).toBe("sid-42");
    expect(outcomes[0]!.profile).toBe("default");

    const types = pushed.map((e) => e.payload.type);
    expect(types).toEqual([
      "hook_pane_updated",
      "hook_pane_updated",
      "outcome_recorded",
      "hook_pane_updated",
    ]);

    // Missing pane returns null
    expect(ingestor.getDetail("work", 999)).toBeNull();

    // Unknown event returns ok: false
    expect(ingestor.emit({ agent: "claude", event: "unknown_xyz" })).toEqual({ ok: false });

    store.close();
  });

  test("recordOutcome and removeOutcome persist and publish outcome_recorded", () => {
    const now = 2_000_000;
    const store = new SpacesStore(":memory:", { now: () => now });
    const bus = new EventBus(asEventStoreAdapter(store), { bootId: "boot_out", now: () => now });
    const ingestor = new HookIngestor(store, bus, () => now);

    const events: EventEnvelope[] = [];
    bus.subscribe({ thread_ids: [], types: ["outcome_recorded"] }, (n) => events.push(n.event));

    const res = ingestor.recordOutcome({
      session: "alpha",
      pane: "7",
      agent: "codex",
      app: "codex",
      profile: "work",
      outcome: "failed",
      text: "error: build failed",
      title: "Build release",
      model: "gpt-5",
      session_id: "thr-7",
      cwd: "/repo",
    });

    expect(res.recorded).toBe(true);
    expect(events).toHaveLength(1);
    expect(events[0]!.payload).toMatchObject({
      type: "outcome_recorded",
      outcome: { session: "alpha", pane: "7", outcome: "failed" },
    });

    const removed = ingestor.removeOutcome({ session: "alpha", pane: "7" });
    expect(removed).toBe(1);
    expect(store.listOutcomes(now)).toHaveLength(0);

    store.close();
  });
});

describe("computeStatusBar", () => {
  test("maps all raw state aliases to exact buckets", () => {
    expect(bucketOf("blocked")).toBe("needs");
    expect(bucketOf("needs_you")).toBe("needs");
    expect(bucketOf("needs-you")).toBe("needs");
    expect(bucketOf("needsyou")).toBe("needs");
    expect(bucketOf("failed")).toBe("failed");
    expect(bucketOf("error")).toBe("failed");
    expect(bucketOf("working")).toBe("working");
    expect(bucketOf("done")).toBe("done");
    expect(bucketOf("idle")).toBe("idle");
    expect(bucketOf("quiet")).toBe("idle");
    expect(bucketOf("new_output")).toBe("idle");
    expect(bucketOf("")).toBe("idle");
  });

  test("returns empty strings when all panes are idle or no spaces", () => {
    const empty = computeStatusBar([]);
    expect(empty).toEqual({
      counts: { needs: 0, failed: 0, working: 0, done: 0, idle: 0 },
      formatted_tmux: "",
      formatted_plain: "",
    });

    const idleOnly = computeStatusBar([
      makeSpace("s1", [makePane("idle", "%1"), makePane("quiet", "%2"), makePane("new_output", "%3")]),
    ]);
    expect(idleOnly.counts).toEqual({ needs: 0, failed: 0, working: 0, done: 0, idle: 3 });
    expect(idleOnly.formatted_tmux).toBe("");
    expect(idleOnly.formatted_plain).toBe("");
  });

  test("formats active buckets with exact Catppuccin Mocha colors and icons", () => {
    const result = computeStatusBar([
      makeSpace("s1", [
        makePane("needs_you", "%1"),
        makePane("needs_you", "%2"),
        makePane("failed", "%3"),
      ]),
      makeSpace("s2", [
        makePane("working", "%4"),
        makePane("done", "%5"),
        makePane("done", "%6"),
        makePane("done", "%7"),
        makePane("idle", "%8"),
      ]),
    ]);

    expect(result.counts).toEqual({
      needs: 2,
      failed: 1,
      working: 1,
      done: 3,
      idle: 1,
    });
    expect(result.formatted_plain).toBe("●2 ✗1 ◐1 ✓3");
    expect(result.formatted_tmux).toBe(
      [
        `#[fg=${MOCHA_BLUE}]●2#[default]`,
        `#[fg=${MOCHA_RED}]✗1#[default]`,
        `#[fg=${MOCHA_YELLOW}]◐1#[default]`,
        `#[fg=${MOCHA_GREEN}]✓3#[default]`,
      ].join(" "),
    );
  });
});

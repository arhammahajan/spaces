import { describe, expect, test } from "bun:test";
import type {
  Counts,
  HookChild,
  HookSnapshot,
  HookSummary,
  HookUsage,
  Pane,
  Space,
  Status,
} from "@spaces/types";
import {
  Arbitrator,
  DONE_TTL_MS,
  HOOK_WORKING_STALE_MS,
  type ScreenReading,
  addStatusCount,
  allBackgroundWork,
  emptyCounts,
  hasActiveFiniteWork,
  hasOnlyLocalCommandBackgroundWork,
  hookUsageHasData,
  hookUsageTotalTokens,
  idleLike,
  isAgentPane,
  isAttentionStatus,
  isAuthBlock,
  rollupCounts,
  seedAppsFromHooks,
  sortSpaces,
  statusPriority,
} from "./arbitrate";
import { ANTIGRAVITY_HOOK_IDLE_GRACE_MS } from "./policies";

function makePane(overrides: Partial<Pane> = {}): Pane {
  return {
    id: "%1",
    numeric_id: 1,
    pane_index: 0,
    window_id: "@1",
    window_index: 0,
    window_name: "editor",
    title: "",
    conversation_title: "",
    base_conv_title: "",
    screen_title: "",
    resume_session: "",
    profile: "",
    command: "",
    start_command: "",
    foreground_cmd: "",
    launch_command: [],
    child_process_count: 0,
    app: "",
    status: "idle",
    base_status: "idle",
    text: "",
    base_text: "",
    kind: "",
    source: "",
    hook: null,
    git: null,
    cwd: "",
    age_ms: 0,
    base_age_ms: 0,
    width: 80,
    height: 24,
    is_active: false,
    window_active: false,
    in_mode: false,
    synchronized: false,
    is_dead: false,
    dead_status: 0,
    has_spaces_meta: false,
    returned_to_shell: false,
    pid: 0,
    ...overrides,
  };
}

function makeSpace(overrides: Partial<Space> = {}): Space {
  return {
    name: "dots",
    id: "$0",
    is_current: false,
    windows: [],
    panes: [],
    cwd: "",
    git: null,
    worktree_path: "",
    worktree_name: "",
    worktree_base: "",
    worktree_managed: false,
    counts: emptyCounts(),
    status: "idle",
    connected_clients: 0,
    created_at_ms: 0,
    ...overrides,
  };
}

function makeHookSummary(overrides: Partial<HookSummary> = {}): HookSummary {
  return {
    agent: "",
    app: "",
    state: "",
    kind: "",
    event: "",
    source: "",
    ts_ms: 0,
    state_since_ms: 0,
    turn_started_ms: 0,
    text: "",
    title: "",
    model: "",
    session_id: "",
    cwd: "",
    usage: null,
    child_count: 0,
    background: 0,
    finite_active: 0,
    service_active: 0,
    waiting_on: "",
    children: [],
    background_tasks: [],
    has_detail: false,
    ...overrides,
  };
}

function makeHookChild(overrides: Partial<HookChild> = {}): HookChild {
  return {
    id: "",
    type: "",
    kind: "",
    category: "",
    state: "",
    outcome: "",
    command: "",
    schedule: "",
    since_ms: 0,
    ts_ms: 0,
    ended_ms: 0,
    text: "",
    ...overrides,
  };
}

function arbSpace(name: string, app: string, status: Status, active: boolean): Space {
  const p = makePane({
    id: "%1",
    numeric_id: 1,
    window_index: 0,
    window_name: "editor",
    app,
    status,
    base_status: status,
    text: "base",
    base_text: "base",
    is_active: active,
  });
  const counts: Counts = emptyCounts();
  addStatusCount(counts, status);
  return makeSpace({
    name,
    id: "$0",
    is_current: name === "dots",
    panes: [p],
    counts,
    status: rollupCounts(counts),
  });
}

function hookSnap(
  session: string,
  id: string,
  state: string,
  text: string,
  ts: number,
  since: number,
): HookSnapshot {
  return {
    ok: true,
    version: 1,
    now_ms: ts,
    spool_pending: 0,
    panes: {
      [session]: {
        [id]: makeHookSummary({
          agent: "claude",
          app: "claude-code",
          state,
          event: "pre_tool",
          ts_ms: ts,
          state_since_ms: since,
          text,
        }),
      },
    },
    outcomes: [],
  };
}

function applyOne(
  arb: Arbitrator,
  space: Space,
  hooks: HookSnapshot | null | undefined,
  readings: Record<string, ScreenReading> | null | undefined,
  nowMs: number,
): Space {
  const spaces = [space];
  arb.apply(spaces, hooks, readings, nowMs);
  return spaces[0];
}

describe("detection/arbitrate", () => {
  test("1. TestFreshHookWorkingWinsOverIdleBase", () => {
    const arb = new Arbitrator();
    const got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "idle", false),
      hookSnap("dots", "1", "working", "Bash x test", 9_000, 9_000),
      null,
      10_000,
    );
    expect(got.panes[0].status).toBe("working");
    expect(got.panes[0].text).toBe("Bash x test");
    expect(got.panes[0].age_ms).toBe(1_000);
    expect(got.status).toBe("working");
    expect(got.panes[0].hook).not.toBeNull();
    expect(got.panes[0].hook?.state).toBe("working");
  });

  test("2. TestStaleHookFallsBackToBase", () => {
    const arb = new Arbitrator();
    // working hook silent for >120s.
    let got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "idle", false),
      hookSnap("dots", "1", "working", "old", 0, 0),
      null,
      200_000,
    );
    expect(got.panes[0].status).toBe("idle");

    // done hook older than 30m.
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hookSnap("dots", "1", "done", "old", 0, 0),
      null,
      31 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");

    // failed hook older than 30m.
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hookSnap("dots", "1", "failed", "old", 0, 0),
      null,
      31 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");

    // non-auth blocked hook older than 30m.
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hookSnap("dots", "1", "blocked", "old", 0, 0),
      null,
      31 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");

    // auth block survives 30m but not 60m.
    const auth = hookSnap("dots", "1", "blocked", "auth?", 0, 0);
    auth.panes.dots!["1"] = makeHookSummary({
      state: "blocked",
      kind: "auth",
      ts_ms: 0,
      state_since_ms: 0,
      text: "auth?",
    });
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      auth,
      null,
      31 * 60_000,
    );
    expect(got.panes[0].status).toBe("needs_you");

    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      auth,
      null,
      61 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");
  });

  test("3. TestHookDonePersistsThirtyMinutes", () => {
    const arb = new Arbitrator();
    const hooks = hookSnap("dots", "1", "done", "turn complete", 9_000, 9_000);
    let got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hooks,
      null,
      10_000,
    );
    expect(got.panes[0].status).toBe("done");

    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hooks,
      null,
      29 * 60_000,
    );
    expect(got.panes[0].status).toBe("done");

    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hooks,
      null,
      9_000 + 31 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");
  });

  test("4. TestHookIdleNeverOverrides", () => {
    const arb = new Arbitrator();
    const got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      hookSnap("dots", "1", "idle", "idle", 9_000, 9_000),
      null,
      10_000,
    );
    expect(got.panes[0].status).toBe("working");
  });

  test("5. TestNewerBaseBeatsOldHook", () => {
    const arb = new Arbitrator();
    const space = arbSpace("dots", "claude-code", "done", false);
    space.panes[0].base_age_ms = 500;
    space.panes[0].age_ms = 500;
    const got = applyOne(
      arb,
      space,
      hookSnap("dots", "1", "working", "Bash old", 9_000, 9_000),
      null,
      10_000,
    );
    expect(got.panes[0].status).toBe("done");
  });

  test("6. TestVisibleBlockerOverridesHookWorking", () => {
    const arb = new Arbitrator();
    const readings: Record<string, ScreenReading> = {
      "%1": { app: "claude-code", state: "blocked", visible_blocker: true },
    };
    const got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "idle", false),
      hookSnap("dots", "1", "working", "Bash x test", 9_000, 9_000),
      readings,
      10_000,
    );
    expect(got.panes[0].status).toBe("needs_you");
  });

  test("7. TestAuthorityKeepsLiveBaseOverScreen", () => {
    const arb = new Arbitrator();
    // claude-code is an authority app: idle viewport must not downgrade working.
    const readings: Record<string, ScreenReading> = {
      "%1": { app: "claude-code", state: "idle", visible_idle: true },
    };
    let got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("working");

    // ... but a strong blocker still wins.
    readings["%1"] = { app: "claude-code", state: "blocked", visible_blocker: true };
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "working", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("needs_you");

    // Non-authority apps follow screen working evidence.
    const plain: Record<string, ScreenReading> = {
      "%1": { app: "gemini", state: "working", visible_working: true },
    };
    got = applyOne(arb, arbSpace("dots", "gemini", "idle", false), null, plain, 1_000);
    expect(got.panes[0].status).toBe("working");

    // Unknown screens leave the base alone.
    const unknown: Record<string, ScreenReading> = {
      "%1": { app: "gemini", state: "unknown" },
    };
    got = applyOne(arb, arbSpace("dots", "gemini", "idle", false), null, unknown, 1_000);
    expect(got.panes[0].status).toBe("idle");

    // skip_state_update leaves the base alone.
    const skip: Record<string, ScreenReading> = {
      "%1": { app: "gemini", state: "working", visible_working: true, skip_state_update: true },
    };
    got = applyOne(arb, arbSpace("dots", "gemini", "idle", false), null, skip, 1_000);
    expect(got.panes[0].status).toBe("idle");
  });

  test("8. TestNativeDoneSurvivesIdleViewport", () => {
    const arb = new Arbitrator();
    const readings: Record<string, ScreenReading> = {
      "%1": { app: "claude-code", state: "idle", visible_idle: true },
    };
    let got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "done", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("done");

    // ... but a new visible working turn promotes it.
    readings["%1"] = { app: "claude-code", state: "working", visible_working: true };
    got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "done", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("working");
  });

  test("9. TestScreenFailedBaseSticks", () => {
    const arb = new Arbitrator();
    const readings: Record<string, ScreenReading> = {
      "%1": { app: "gemini", state: "working", visible_working: true },
    };
    const got = applyOne(
      arb,
      arbSpace("dots", "gemini", "failed", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("failed");
  });

  test("10. TestWorkingToIdleSynthesizesDoneUntilFocused", () => {
    const arb = new Arbitrator();
    // Tick 1: screen working observed (non-authority app).
    const working: Record<string, ScreenReading> = {
      "%1": { app: "qwen", state: "working", visible_working: true },
    };
    const got = applyOne(
      arb,
      arbSpace("dots", "qwen", "idle", false),
      null,
      working,
      1_000,
    );
    expect(got.panes[0].status).toBe("working");

    // Tick 2: idle viewport after working -> Done (pane not focused).
    const idle: Record<string, ScreenReading> = {
      "%1": { app: "qwen", state: "idle", visible_idle: true },
    };
    let spaces = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(spaces, null, idle, 2_000);
    expect(spaces[0].panes[0].status).toBe("done");

    // Tick 3: repeat is stable (idempotent recompute from base).
    spaces = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(spaces, null, idle, 3_000);
    expect(spaces[0].panes[0].status).toBe("done");

    // Done memory stays Done even when the pane is active (clears on new working turn or 10m TTL).
    spaces = [arbSpace("dots", "qwen", "idle", true)];
    arb.apply(spaces, null, idle, 4_000);
    expect(spaces[0].panes[0].status).toBe("done");

    // Starting a new working turn clears Done memory -> Working.
    spaces = [arbSpace("dots", "qwen", "idle", true)];
    arb.apply(spaces, null, working, 5_000);
    expect(spaces[0].panes[0].status).toBe("working");
  });

  test("11. TestDoneMemoryExpiresAfterTenMinutes", () => {
    const arb = new Arbitrator();
    const working: Record<string, ScreenReading> = {
      "%1": { app: "qwen", state: "working", visible_working: true },
    };
    const idle: Record<string, ScreenReading> = {
      "%1": { app: "qwen", state: "idle", visible_idle: true },
    };

    let spaces = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(spaces, null, working, 1_000);

    spaces = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(spaces, null, idle, 2_000);
    expect(spaces[0].panes[0].status).toBe("done");

    spaces = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(spaces, null, idle, 2_000 + DONE_TTL_MS + 1);
    expect(spaces[0].panes[0].status).toBe("idle");
  });

  test("12. TestStaleAppReadingIgnored", () => {
    const arb = new Arbitrator();
    const readings: Record<string, ScreenReading> = {
      "%1": { app: "gemini", state: "working", visible_working: true },
    };
    const got = applyOne(
      arb,
      arbSpace("dots", "claude-code", "idle", false),
      null,
      readings,
      1_000,
    );
    expect(got.panes[0].status).toBe("idle");
  });

  test("13. TestApplyRecomputesSortAndCounts", () => {
    const arb = new Arbitrator();
    const space = makeSpace({
      name: "dots",
      id: "$0",
      is_current: true,
      panes: [
        makePane({
          id: "%2",
          numeric_id: 2,
          window_index: 1,
          app: "shell",
          status: "idle",
          base_status: "idle",
        }),
        makePane({
          id: "%1",
          numeric_id: 1,
          window_index: 0,
          app: "shell",
          status: "idle",
          base_status: "idle",
        }),
      ],
    });
    const hooks = hookSnap("dots", "2", "blocked", "approve?", 9_000, 9_000);
    const spaces = [arbSpace("zzz", "shell", "idle", false), space];
    arb.apply(spaces, hooks, null, 10_000);
    expect(spaces[0].name).toBe("dots");
    expect(spaces[0].status).toBe("needs_you");
    expect(spaces[0].counts.needs).toBe(1);
    expect(spaces[0].panes[0].id).toBe("%2");
  });

  test("14. TestApplyWithRealBuildSnapshot", () => {
    const makeBuilt = () => [
      makeSpace({
        name: "dots",
        id: "$0",
        is_current: true,
        panes: [
          makePane({
            id: "%5",
            numeric_id: 5,
            window_id: "@1",
            window_index: 0,
            window_name: "w",
            command: "bash",
            app: "shell",
            status: "working",
            base_status: "working",
            has_spaces_meta: true,
          }),
        ],
      }),
    ];

    const spaces = makeBuilt();
    const arb = new Arbitrator();
    const hooks = hookSnap("dots", "5", "done", "all set", 9_500, 9_500);
    arb.apply(spaces, hooks, null, 10_000);
    expect(spaces[0].panes[0].status).toBe("done");

    // Idempotent: second apply with same inputs is stable.
    const spaces2 = makeBuilt();
    arb.apply(spaces2, hooks, null, 10_000);
    expect(spaces2[0].panes[0].status).toBe(spaces[0].panes[0].status);
    expect(spaces2[0].panes[0].text).toBe(spaces[0].panes[0].text);

    // Nil hooks/readings keep the base.
    const spaces3 = makeBuilt();
    new Arbitrator().apply(spaces3, null, null, 10_000);
    expect(spaces3[0].panes[0].status).toBe("working");
    expect(spaces3[0].panes[0].app).toContain("shell");
  });

  test("15. TestNewerOSCWorkingBeatsOlderHookDone", () => {
    const arb = new Arbitrator();
    const space = arbSpace("dots", "claude-code", "working", false);
    space.panes[0].base_age_ms = 500;
    space.panes[0].age_ms = 500;
    // Hook said "done" 5s ago, OSC 7501 said "working" 500ms ago.
    const got = applyOne(
      arb,
      space,
      hookSnap("dots", "1", "done", "old turn", 5_000, 5_000),
      null,
      10_000,
    );
    expect(got.panes[0].status).toBe("working");
  });

  test("16. TestExitedAgentReturnedToShellDoesNotStayInInboxViaHookStore", () => {
    const spaces = [
      makeSpace({
        name: "dots",
        id: "$0",
        is_current: true,
        panes: [
          makePane({
            id: "%5",
            numeric_id: 5,
            window_id: "@1",
            window_index: 0,
            window_name: "w",
            command: "zsh",
            pid: 100,
            app: "shell",
            status: "idle",
            base_status: "idle",
            returned_to_shell: true,
          }),
        ],
      }),
    ];
    const arb = new Arbitrator({
      detectDescendantAgent: () => "",
    });
    arb.apply(
      spaces,
      hookSnap("dots", "5", "working", "Bash · cargo test", 9_500, 9_500),
      null,
      10_000,
    );
    const p = spaces[0].panes[0];
    expect(p.app).toBe("shell");
    expect(p.hook).toBeNull();
    expect(isAgentPane(p)).toBe(false);
  });

  test("17. TestSeedAppsFromHooksAndApplyDoNotReviveShellPaneAfterCtrlD", () => {
    const hooks: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: 10_000,
      spool_pending: 0,
      panes: {
        "model-router": {
          "41": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "done",
            event: "stop",
            ts_ms: 9_000,
            state_since_ms: 9_000,
            text: "turn complete",
            title: "Rewrite Readme",
            session_id: "conv-41",
          }),
        },
      },
      outcomes: [],
    };

    // Case 1: Immediately after Ctrl+D back to zsh with stale @spaces_app/@spaces_state.
    const spaces1 = [
      makeSpace({
        name: "model-router",
        id: "$0",
        is_current: true,
        panes: [
          makePane({
            id: "%41",
            numeric_id: 41,
            window_id: "@1",
            window_index: 0,
            window_name: "zsh",
            command: "zsh",
            pid: 438039,
            app: "shell",
            status: "idle",
            base_status: "idle",
            resume_session: "conv-41",
            returned_to_shell: true,
          }),
        ],
      }),
    ];
    seedAppsFromHooks(spaces1, hooks);
    new Arbitrator({
      procIsLive: (pid) => pid === 438039,
      detectDescendantAgent: () => "",
    }).apply(spaces1, hooks, null, 10_000);
    const p1 = spaces1[0].panes[0];
    expect(p1.returned_to_shell).toBe(true);
    expect(p1.app).toBe("shell");
    expect(p1.hook).toBeNull();
    expect(isAgentPane(p1)).toBe(false);

    // Case 2: Subsequent tick after @spaces_* has been cleared while the 30m hook
    // record is still present in hooks.store.json.
    const spaces2 = [
      makeSpace({
        name: "model-router",
        id: "$0",
        is_current: true,
        panes: [
          makePane({
            id: "%41",
            numeric_id: 41,
            window_id: "@1",
            window_index: 0,
            window_name: "zsh",
            command: "zsh",
            pid: 438039,
            app: "shell",
            status: "idle",
            base_status: "idle",
          }),
        ],
      }),
    ];
    seedAppsFromHooks(spaces2, hooks);
    new Arbitrator({
      procIsLive: (pid) => pid === 438039,
      detectDescendantAgent: () => "",
    }).apply(spaces2, hooks, null, 10_000);
    const p2 = spaces2[0].panes[0];
    expect(p2.app).toBe("shell");
    expect(p2.hook).toBeNull();
    expect(isAgentPane(p2)).toBe(false);
  });

  test("18. TestActiveBackgroundWorkStaysWorkingPastThirtyMinutes", () => {
    const arb = new Arbitrator();
    const startMs = 1_700_000_000_000;
    const twoHoursLater = startMs + 2 * 60 * 60_000;

    // Pane has an active finite subagent running for 2 hours without new hook events.
    const hooks: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: twoHoursLater,
      spool_pending: 0,
      panes: {
        proj: {
          "1": makeHookSummary({
            agent: "claude",
            app: "claude-code",
            state: "working",
            event: "stop",
            ts_ms: startMs,
            state_since_ms: startMs,
            text: "waiting on subagent: Worker · Migrate backend",
            waiting_on: "waiting on subagent: Worker · Migrate backend",
            finite_active: 1,
            child_count: 1,
            children: [
              makeHookChild({
                id: "sub-1",
                type: "Worker",
                kind: "subagent",
                category: "finite",
                state: "working",
                since_ms: startMs,
                ts_ms: startMs,
                text: "Migrate backend",
              }),
            ],
          }),
        },
      },
      outcomes: [],
    };

    let got = applyOne(
      arb,
      arbSpace("proj", "claude-code", "idle", false),
      hooks,
      null,
      twoHoursLater,
    );
    expect(got.panes[0].status).toBe("working");

    // Even if only Children has an active finite subagent (finite_active/waiting_on omitted),
    // it stays working indefinitely until the child is no longer active.
    hooks.panes.proj!["1"] = makeHookSummary({
      agent: "claude",
      app: "claude-code",
      state: "working",
      event: "post_tool",
      ts_ms: startMs,
      state_since_ms: startMs,
      text: "running subagent",
      children: [
        makeHookChild({
          id: "sub-1",
          kind: "subagent",
          category: "finite",
          state: "working",
          since_ms: startMs,
          ts_ms: startMs,
        }),
      ],
    });
    got = applyOne(
      arb,
      arbSpace("proj", "claude-code", "idle", false),
      hooks,
      null,
      startMs + 6 * 60 * 60_000,
    );
    expect(got.panes[0].status).toBe("working");

    // Once the subagent finishes, normal working staleness applies.
    hooks.panes.proj!["1"] = makeHookSummary({
      agent: "claude",
      app: "claude-code",
      state: "working",
      event: "subagent_stop",
      ts_ms: startMs,
      state_since_ms: startMs,
      text: "subagent done",
      finite_active: 0,
      children: [
        makeHookChild({
          id: "sub-1",
          kind: "subagent",
          category: "finite",
          state: "done",
          since_ms: startMs,
          ts_ms: startMs + 1_000,
          ended_ms: startMs + 1_000,
        }),
      ],
    });
    got = applyOne(
      arb,
      arbSpace("proj", "claude-code", "idle", false),
      hooks,
      null,
      startMs + HOOK_WORKING_STALE_MS + 1,
    );
    expect(got.panes[0].status).toBe("idle");

    // Long-running services alone do NOT keep a silent working hook alive past WorkingStaleMs.
    hooks.panes.proj!["1"] = makeHookSummary({
      agent: "claude",
      app: "claude-code",
      state: "working",
      event: "post_tool",
      ts_ms: startMs,
      state_since_ms: startMs,
      service_active: 1,
      background_tasks: [
        makeHookChild({
          id: "svc-1",
          kind: "service",
          category: "service",
          state: "working",
          since_ms: startMs,
          ts_ms: startMs,
        }),
      ],
    });
    got = applyOne(
      arb,
      arbSpace("proj", "claude-code", "idle", false),
      hooks,
      null,
      startMs + HOOK_WORKING_STALE_MS + 1,
    );
    expect(got.panes[0].status).toBe("idle");
  });

  test("19. TestExpiredBaseWorkingWithNewerTmuxTsDoesNotOverrideActiveBackgroundHook", () => {
    const startMs = 1_700_000_000_000;
    const laterMs = startMs + 45 * 60_000;

    // When expired base resets to idle with base_age_ms = 0, hookView does not
    // mistake the expired fallback idle for a newer idle event.
    const spaces = [
      makeSpace({
        name: "proj",
        id: "$0",
        is_current: true,
        panes: [
          makePane({
            id: "%5",
            numeric_id: 5,
            window_id: "@1",
            window_index: 0,
            window_name: "agent",
            command: "antigravity",
            app: "antigravity",
            status: "idle",
            base_status: "idle",
            base_age_ms: 0,
            age_ms: 0,
          }),
        ],
      }),
    ];

    const hooks: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: laterMs,
      spool_pending: 0,
      panes: {
        proj: {
          "5": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "stop",
            ts_ms: startMs,
            state_since_ms: startMs,
            text: "waiting on subagent: Codebase Researcher",
            waiting_on: "waiting on subagent: Codebase Researcher",
            finite_active: 1,
          }),
        },
      },
      outcomes: [],
    };
    const arb = new Arbitrator();
    arb.apply(spaces, hooks, null, laterMs);
    expect(spaces[0].panes[0].status).toBe("working");
  });

  test("20. TestFreshWorkingHookSuppressesSpuriousScreenNewlyDone", () => {
    const arb = new Arbitrator();
    const t0 = 1_700_000_000_000;

    // Tick 1: Antigravity viewport shows working.
    const workingReading: Record<string, ScreenReading> = {
      "%1": { app: "antigravity", state: "working", visible_working: true },
    };
    const hooks: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0,
      spool_pending: 0,
      panes: {
        proj: {
          "1": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "stop",
            ts_ms: t0,
            state_since_ms: t0,
            waiting_on: "waiting on subagent: Docs",
            finite_active: 1,
          }),
        },
      },
      outcomes: [],
    };
    const s1 = [arbSpace("proj", "antigravity", "working", false)];
    s1[0].panes[0].source = "screen";
    arb.apply(s1, hooks, workingReading, t0);

    // Tick 2: Parent viewport clears to unknown/idle while subagent still runs.
    const idleReading: Record<string, ScreenReading> = {
      "%1": { app: "antigravity", state: "unknown" },
    };
    const s2 = [arbSpace("proj", "antigravity", "working", false)];
    s2[0].panes[0].source = "screen";
    arb.apply(s2, hooks, idleReading, t0 + 5_000);
    expect(s2[0].panes[0].status).toBe("working");
    expect(arb.takeNewlyDone()).toEqual([]);
  });

  test("21. TestAntigravityClearsStaleWorkingHooksWhenVisiblyIdle", () => {
    const t0 = 1_700_000_000_000;
    const idleReading: Record<string, ScreenReading> = {
      "%1": { app: "antigravity", state: "idle", visible_idle: true },
    };

    // Case 1: Stale local_bash background task while viewport is visibly idle.
    const arb1 = new Arbitrator();
    const hooksLocalCmd: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0 + HOOK_WORKING_STALE_MS + 60_000,
      spool_pending: 0,
      panes: {
        dots: {
          "1": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "post_tool",
            ts_ms: t0,
            state_since_ms: t0,
            finite_active: 1,
            waiting_on: "waiting on: for l in ...",
            background_tasks: [
              makeHookChild({
                id: "conv/task-115",
                kind: "command",
                type: "local_bash",
                category: "finite",
                state: "working",
                since_ms: t0,
                ts_ms: t0,
              }),
            ],
          }),
        },
      },
      outcomes: [],
    };
    const got1 = applyOne(
      arb1,
      arbSpace("dots", "antigravity", "idle", false),
      hooksLocalCmd,
      idleReading,
      t0 + HOOK_WORKING_STALE_MS + 60_000,
    );
    expect(got1.panes[0].status).toBe("idle");

    // Case 2: Newer screen turn completes after an older working hook -> Done & preserves newlyDone.
    const arb2 = new Arbitrator();
    const sp2 = arbSpace("dots", "antigravity", "working", false);
    sp2.panes[0].source = "screen";
    sp2.panes[0].base_age_ms = 4_000;
    const got2 = applyOne(arb2, sp2, hooksLocalCmd, idleReading, t0 + 60_000);
    expect(got2.panes[0].status).toBe("done");
    expect(arb2.takeNewlyDone()).toEqual(["%1"]);

    // Case 3: Antigravity missed Stop hook (working, no finite work, >10s silent) while visibly idle.
    const arb3 = new Arbitrator();
    const hooksMissedStop: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0 + ANTIGRAVITY_HOOK_IDLE_GRACE_MS + 1_000,
      spool_pending: 0,
      panes: {
        dots: {
          "1": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "post_tool",
            ts_ms: t0,
            state_since_ms: t0,
          }),
        },
      },
      outcomes: [],
    };
    const sp3 = arbSpace("dots", "antigravity", "working", false);
    sp3.panes[0].source = "osc7501";
    const got3 = applyOne(
      arb3,
      sp3,
      hooksMissedStop,
      idleReading,
      t0 + ANTIGRAVITY_HOOK_IDLE_GRACE_MS + 1_000,
    );
    expect(got3.panes[0].status).toBe("done");

    // Case 4: Active subagent (NOT local_bash) still stays working even with VisibleIdle.
    const arb4 = new Arbitrator();
    const hooksSubagent: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0 + HOOK_WORKING_STALE_MS + 60_000,
      spool_pending: 0,
      panes: {
        dots: {
          "1": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "stop",
            ts_ms: t0,
            state_since_ms: t0,
            finite_active: 1,
            waiting_on: "waiting on subagent: Codebase Researcher",
          }),
        },
      },
      outcomes: [],
    };
    const got4 = applyOne(
      arb4,
      arbSpace("dots", "antigravity", "idle", false),
      hooksSubagent,
      idleReading,
      t0 + HOOK_WORKING_STALE_MS + 60_000,
    );
    expect(got4.panes[0].status).toBe("working");
  });

  test("22. TestHookUsageHelpers", () => {
    expect(hookUsageHasData(null)).toBe(false);
    expect(hookUsageTotalTokens(null)).toBe(0);

    const empty: HookUsage = {
      input_tokens: 0,
      output_tokens: 0,
      reasoning_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: 0,
    };
    expect(hookUsageHasData(empty)).toBe(false);
    expect(hookUsageTotalTokens(empty)).toBe(0);

    const full: HookUsage = {
      input_tokens: 12_400,
      output_tokens: 3_100,
      reasoning_tokens: 500,
      cache_read_tokens: 8_200,
      cache_write_tokens: 1_000,
      cost_usd: 0.04,
    };
    expect(hookUsageHasData(full)).toBe(true);
    expect(hookUsageTotalTokens(full)).toBe(16_000);

    const costOnly: HookUsage = { ...empty, cost_usd: 0.01 };
    expect(hookUsageHasData(costOnly)).toBe(true);

    const cacheOnly: HookUsage = { ...empty, cache_read_tokens: 2048 };
    expect(hookUsageHasData(cacheOnly)).toBe(true);
    expect(hookUsageTotalTokens(cacheOnly)).toBe(0);
  });

  test("23. TestDaemonSSESourceAttributionAndArbitration", () => {
    const t0 = 1_700_000_000_000;

    // 1. Fresh daemon_sse working hook sets pane.source = "daemon_sse" and working.
    const arb1 = new Arbitrator();
    const daemonWorking: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0 + 1_000,
      spool_pending: 0,
      panes: {
        dots: {
          "1": makeHookSummary({
            agent: "opencode",
            app: "opencode",
            state: "working",
            event: "session.busy",
            source: "daemon_sse",
            ts_ms: t0,
            state_since_ms: t0,
            text: "Running tool: bash",
            usage: {
              input_tokens: 12_400,
              output_tokens: 3_100,
              reasoning_tokens: 0,
              cache_read_tokens: 8_200,
              cache_write_tokens: 0,
              cost_usd: 0.04,
            },
          }),
        },
      },
      outcomes: [],
    };
    const got1 = applyOne(
      arb1,
      arbSpace("dots", "opencode", "idle", false),
      daemonWorking,
      null,
      t0 + 1_000,
    );
    expect(got1.panes[0].status).toBe("working");
    expect(got1.panes[0].source).toBe("daemon_sse");
    expect(got1.panes[0].hook).not.toBeNull();
    expect(hookUsageTotalTokens(got1.panes[0].hook?.usage)).toBe(15_500);

    // 2. daemon_sse working suppresses screen-inferred idle/done transitions
    // (both screenFinishedNewerTurn and yieldWorkingToIdle).
    const arb2 = new Arbitrator();
    const idleReading: Record<string, ScreenReading> = {
      "%1": { app: "antigravity", state: "idle", visible_idle: true },
    };
    const daemonAGY: HookSnapshot = {
      ok: true,
      version: 1,
      now_ms: t0 + ANTIGRAVITY_HOOK_IDLE_GRACE_MS + 5_000,
      spool_pending: 0,
      panes: {
        dots: {
          "1": makeHookSummary({
            agent: "antigravity",
            app: "antigravity",
            state: "working",
            event: "session.busy",
            source: "daemon_sse",
            ts_ms: t0,
            state_since_ms: t0,
            text: "Streaming response",
          }),
        },
      },
      outcomes: [],
    };
    const sp2 = arbSpace("dots", "antigravity", "working", false);
    sp2.panes[0].source = "screen";
    sp2.panes[0].base_age_ms = 2_000;
    const got2 = applyOne(
      arb2,
      sp2,
      daemonAGY,
      idleReading,
      t0 + ANTIGRAVITY_HOOK_IDLE_GRACE_MS + 5_000,
    );
    expect(got2.panes[0].status).toBe("working");
    expect(got2.panes[0].source).toBe("daemon_sse");
    expect(arb2.takeNewlyDone()).toEqual([]);

    // 3. daemon_sse permission and question blockers surface as needs_you.
    for (const kind of ["permission", "question"]) {
      const arb3 = new Arbitrator();
      const daemonBlocked: HookSnapshot = {
        ok: true,
        version: 1,
        now_ms: t0 + 1_000,
        spool_pending: 0,
        panes: {
          dots: {
            "1": makeHookSummary({
              agent: "opencode",
              app: "opencode",
              state: "blocked",
              kind,
              event: "permission.asked",
              source: "daemon_sse",
              ts_ms: t0,
              state_since_ms: t0,
              text: "Allow bash command?",
            }),
          },
        },
        outcomes: [],
      };
      const got3 = applyOne(
        arb3,
        arbSpace("dots", "opencode", "working", false),
        daemonBlocked,
        null,
        t0 + 1_000,
      );
      expect(got3.panes[0].status).toBe("needs_you");
      expect(got3.panes[0].source).toBe("daemon_sse");
    }

    // 4. A visible screen blocker still overrides a fresh daemon_sse working state.
    const arb4 = new Arbitrator();
    const blockerReading: Record<string, ScreenReading> = {
      "%1": { app: "opencode", state: "blocked", visible_blocker: true },
    };
    const got4 = applyOne(
      arb4,
      arbSpace("dots", "opencode", "idle", false),
      daemonWorking,
      blockerReading,
      t0 + 1_000,
    );
    expect(got4.panes[0].status).toBe("needs_you");
  });

  describe("24. TestCodexIdleScreenCompletesMissedStop", () => {
    const now = 1_700_000_000_000;
    const reading: Record<string, ScreenReading> = {
      "%1": { app: "codex", state: "idle", visible_idle: true, osc_authority: true },
    };
    const cases: Array<{ name: string; age: number; child: boolean; want: Status }> = [
      { name: "missed Stop", age: 15_000, child: false, want: "done" },
      { name: "recent work", age: 1_000, child: false, want: "working" },
      { name: "active subagent", age: 15_000, child: true, want: "working" },
    ];

    for (const tc of cases) {
      test(tc.name, () => {
        const sp = arbSpace("work", "codex", "working", false);
        sp.panes[0].base_age_ms = tc.age;
        const hook = makeHookSummary({
          app: "codex",
          agent: "codex",
          state: "working",
          ts_ms: now - tc.age,
          children: tc.child ? [makeHookChild({ id: "child", state: "working" })] : [],
        });
        const hooks: HookSnapshot = {
          ok: true,
          version: 1,
          now_ms: now,
          spool_pending: 0,
          panes: { work: { "1": hook } },
          outcomes: [],
        };
        const arb = new Arbitrator();
        const got = applyOne(arb, sp, hooks, reading, now);
        expect(got.panes[0].status).toBe(tc.want);
        const done = arb.takeNewlyDone();
        expect(done.length === 1).toBe(tc.want === "done");
      });
    }
  });

  test("prunes vanished pane IDs from done and prev memory", () => {
    const arb = new Arbitrator();
    const working = new Map<string, ScreenReading>([
      ["%1", { app: "qwen", state: "working", visible_working: true }],
    ]);
    const idle = new Map<string, ScreenReading>([
      ["%1", { app: "qwen", state: "idle", visible_idle: true }],
    ]);

    arb.apply([arbSpace("dots", "qwen", "idle", false)], null, working, 1_000);
    const s2 = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(s2, null, idle, 2_000);
    expect(s2[0].panes[0].status).toBe("done");

    // Apply with a space where %1 has vanished.
    arb.apply([makeSpace({ name: "dots", panes: [] })], null, null, 3_000);

    // Reappearing %1 with idle viewport must not resurrect pruned done/prev state.
    const s4 = [arbSpace("dots", "qwen", "idle", false)];
    arb.apply(s4, null, idle, 4_000);
    expect(s4[0].panes[0].status).toBe("idle");
  });

  test("status, counts, sortSpaces, and background work helpers match Go", () => {
    expect(statusPriority("needs_you")).toBe(0);
    expect(statusPriority("failed")).toBe(1);
    expect(statusPriority("done")).toBe(2);
    expect(statusPriority("working")).toBe(3);
    expect(statusPriority("quiet")).toBe(4);
    expect(statusPriority("new_output")).toBe(5);
    expect(statusPriority("idle")).toBe(6);
    expect(isAttentionStatus("needs_you")).toBe(true);
    expect(isAttentionStatus("failed")).toBe(true);
    expect(isAttentionStatus("working")).toBe(false);
    expect(idleLike("idle")).toBe(true);
    expect(idleLike("quiet")).toBe(true);
    expect(idleLike("new_output")).toBe(true);
    expect(idleLike("working")).toBe(false);

    const mk = (name: string, is_current: boolean, status: Status): Space => {
      const counts = emptyCounts();
      addStatusCount(counts, status);
      return makeSpace({ name, is_current, status, counts });
    };
    const spaces = [
      mk("zeta", false, "idle"),
      mk("alpha", false, "needs_you"),
      mk("beta", false, "working"),
      mk("current", true, "idle"),
      mk("Alpha2", false, "needs_you"),
    ];
    sortSpaces(spaces);
    expect(spaces.map((s) => s.name)).toEqual(["current", "alpha", "Alpha2", "beta", "zeta"]);

    const authHook = makeHookSummary({ state: "blocked", kind: "auth" });
    expect(isAuthBlock(authHook)).toBe(true);
    expect(isAuthBlock(makeHookSummary({ state: "blocked", kind: "permission" }))).toBe(false);

    const bgHook = makeHookSummary({
      finite_active: 1,
      children: [
        makeHookChild({ id: "done-sub", state: "done", since_ms: 10 }),
      ],
      background_tasks: [
        makeHookChild({
          id: "cmd-1",
          kind: "command",
          type: "local_bash",
          category: "finite",
          state: "working",
          since_ms: 20,
        }),
        makeHookChild({
          id: "svc-1",
          kind: "service",
          category: "service",
          state: "working",
          since_ms: 5,
        }),
      ],
    });
    expect(hasActiveFiniteWork(bgHook)).toBe(true);
    expect(hasOnlyLocalCommandBackgroundWork(bgHook)).toBe(true);
    expect(allBackgroundWork(bgHook).map((item) => item.id)).toEqual([
      "cmd-1",
      "svc-1",
      "done-sub",
    ]);
  });
});

/**
 * Three-layer pane status arbitrator (OSC 7501 baseline -> Hooks -> Screen)
 * ported 1:1 from `internal/model/arbitrate.go` and `internal/model/types.go`.
 */

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
  DetectionRegistry,
  defaultDetectionRegistry,
} from "./policies";

export const HOOK_WORKING_STALE_MS = 120_000;
export const HOOK_DONE_TTL_MS = 1_800_000;
export const HOOK_FAILED_TTL_MS = 1_800_000;
export const HOOK_BLOCK_TTL_MS = 1_800_000;
export const HOOK_AUTH_TTL_MS = 3_600_000;
export const DONE_TTL_MS = 600_000;
export const CODEX_IDLE_GRACE_MS = 10_000;

/**
 * One pane's screen-detector result keyed by pane ID (e.g. `"%12"`).
 */
export interface ScreenReading {
  app?: string;
  state: string;
  visible_idle?: boolean;
  visible_blocker?: boolean;
  visible_working?: boolean;
  skip_state_update?: boolean;
  osc_authority?: boolean;
  title?: string;
}

export interface ArbitratorOptions {
  registry?: DetectionRegistry;
  procIsLive?: (pid: number) => boolean;
  detectDescendantAgent?: (pid: number) => string;
}

export interface HookViewResult {
  status: Status;
  ageMs: number;
  text: string;
  fresh: boolean;
}

const STATUS_PRIORITY: Record<Status, number> = {
  needs_you: 0,
  failed: 1,
  done: 2,
  working: 3,
  quiet: 4,
  new_output: 5,
  idle: 6,
};

const ACTIVE_CHILD_STATES = new Set([
  "",
  "working",
  "running",
  "pending",
  "in_progress",
  "started",
  "active",
  "scheduled",
  "waiting",
]);

function saturatingSub(a: number, b: number): number {
  return a < b ? 0 : a - b;
}

// --- Status & Counts helpers ---

/**
 * Returns the sort priority for a `Status` (lower = more urgent).
 */
export function statusPriority(s: Status): number {
  return STATUS_PRIORITY[s] ?? 6;
}

/**
 * Reports whether the status requires immediate user attention.
 */
export function isAttentionStatus(s: Status): boolean {
  return s === "needs_you" || s === "failed";
}

/**
 * Reports whether `s` is an idle-family status that screen working evidence may promote.
 */
export function idleLike(s: Status): boolean {
  return s === "idle" || s === "quiet" || s === "new_output";
}

/**
 * Creates a zeroed `Counts` tally object.
 */
export function emptyCounts(): Counts {
  return {
    needs: 0,
    failed: 0,
    working: 0,
    done: 0,
    quiet: 0,
    new_output: 0,
    idle: 0,
  };
}

/**
 * Increments the counter in `counts` corresponding to `s`.
 */
export function addStatusCount(counts: Counts, s: Status): void {
  switch (s) {
    case "needs_you":
      counts.needs++;
      break;
    case "failed":
      counts.failed++;
      break;
    case "done":
      counts.done++;
      break;
    case "working":
      counts.working++;
      break;
    case "quiet":
      counts.quiet++;
      break;
    case "new_output":
      counts.new_output++;
      break;
    default:
      counts.idle++;
      break;
  }
}

/**
 * Returns the highest-priority status represented in `counts`.
 */
export function rollupCounts(counts: Counts): Status {
  if (counts.needs > 0) return "needs_you";
  if (counts.failed > 0) return "failed";
  if (counts.done > 0) return "done";
  if (counts.working > 0) return "working";
  if (counts.quiet > 0) return "quiet";
  if (counts.new_output > 0) return "new_output";
  return "idle";
}

/**
 * Orders panes in-place by status urgency, window index, then numeric ID.
 */
export function sortPanes(panes: Pane[]): void {
  panes.sort((a, b) => {
    const pa = statusPriority(a.status);
    const pb = statusPriority(b.status);
    if (pa !== pb) return pa - pb;
    if (a.window_index !== b.window_index) return a.window_index - b.window_index;
    return a.numeric_id - b.numeric_id;
  });
}

/**
 * Orders spaces in-place: current first, then attention urgency, then
 * case-insensitive name.
 */
export function sortSpaces(spaces: Space[]): void {
  spaces.sort((a, b) => {
    if (a.is_current !== b.is_current) {
      return a.is_current ? -1 : 1;
    }
    const pa = statusPriority(a.status);
    const pb = statusPriority(b.status);
    if (pa !== pb) return pa - pb;
    const ni = a.name.toLowerCase();
    const nj = b.name.toLowerCase();
    if (ni !== nj) return ni < nj ? -1 : 1;
    if (a.name !== b.name) return a.name < b.name ? -1 : 1;
    return 0;
  });
}

// --- HookUsage & HookSummary helpers ---

/**
 * Returns the sum of input, output, and reasoning tokens.
 */
export function hookUsageTotalTokens(u: HookUsage | null | undefined): number {
  if (!u) return 0;
  return (u.input_tokens ?? 0) + (u.output_tokens ?? 0) + (u.reasoning_tokens ?? 0);
}

/**
 * Reports whether any token count or cost value is populated.
 */
export function hookUsageHasData(u: HookUsage | null | undefined): boolean {
  if (!u) return false;
  return (
    u.input_tokens > 0 ||
    u.output_tokens > 0 ||
    u.reasoning_tokens > 0 ||
    u.cache_read_tokens > 0 ||
    u.cache_write_tokens > 0 ||
    u.cost_usd > 0
  );
}

function childEffectiveCategory(c: HookChild): string {
  const cat = (c.category ?? "").trim().toLowerCase();
  if (cat === "service" || cat === "watcher" || cat === "schedule" || cat === "finite") {
    return cat;
  }
  return "finite";
}

function childIsActive(c: HookChild): boolean {
  return ACTIVE_CHILD_STATES.has((c.state ?? "").trim().toLowerCase());
}

function childIsFinite(c: HookChild): boolean {
  return childEffectiveCategory(c) === "finite";
}

function mergeHookBackgroundItems(
  children: readonly HookChild[] | undefined,
  bgTasks: readonly HookChild[] | undefined,
): HookChild[] {
  const chList = children ?? [];
  const bgList = bgTasks ?? [];
  if (chList.length === 0 && bgList.length === 0) {
    return [];
  }
  const out: HookChild[] = [];
  const seen = new Map<string, number>();

  const add = (rawItem: HookChild, defaultRole: string) => {
    const item: HookChild = { ...rawItem };
    if ((item.kind ?? "").trim() === "") {
      const cat = childEffectiveCategory(item);
      item.kind = cat === "finite" ? defaultRole : cat;
    }
    if ((item.category ?? "").trim() === "") {
      item.category = "finite";
    }
    const id = (item.id ?? "").trim();
    if (id !== "") {
      const existingIdx = seen.get(id);
      if (existingIdx !== undefined) {
        const existing = out[existingIdx];
        if (item.ts_ms >= existing.ts_ms) {
          if (
            existing.since_ms > 0 &&
            (item.since_ms <= 0 || existing.since_ms < item.since_ms)
          ) {
            item.since_ms = existing.since_ms;
          }
          out[existingIdx] = item;
        }
        return;
      }
      seen.set(id, out.length);
    }
    out.push(item);
  };

  for (const c of chList) {
    add(c, "subagent");
  }
  for (const b of bgList) {
    add(b, "command");
  }

  const groupOf = (c: HookChild): number => {
    if (!childIsActive(c)) return 2;
    if (childIsFinite(c)) return 0;
    return 1;
  };

  out.sort((a, b) => {
    const ga = groupOf(a);
    const gb = groupOf(b);
    if (ga !== gb) return ga - gb;
    return a.since_ms - b.since_ms;
  });

  return out;
}

/**
 * Reports whether this hook represents an auth-required block.
 */
export function isAuthBlock(h: HookSummary | null | undefined): boolean {
  if (!h) return false;
  return h.state === "blocked" && h.kind === "auth";
}

/**
 * Returns deduplicated subagents and background tasks ordered with active
 * finite jobs first, then active services/watchers/schedules, then finished
 * outcomes.
 */
export function allBackgroundWork(h: HookSummary | null | undefined): HookChild[] {
  if (!h) return [];
  return mergeHookBackgroundItems(h.children, h.background_tasks);
}

/**
 * Reports whether the pane has actively running finite background work (subagents
 * or finite background commands) or is explicitly waiting on background tasks to finish.
 */
export function hasActiveFiniteWork(h: HookSummary | null | undefined): boolean {
  if (!h) return false;
  if (h.finite_active > 0 || (h.waiting_on ?? "").trim() !== "") {
    return true;
  }
  for (const item of allBackgroundWork(h)) {
    if (childIsActive(item) && childIsFinite(item)) {
      return true;
    }
  }
  return false;
}

/**
 * Reports whether the pane's active finite background work consists solely of
 * local shell/bash commands (with no active subagents).
 *
 * Note: In `@spaces/types` (`HookChild`), JSON `"kind"` maps to Go `Role` and
 * JSON `"type"` maps to Go `Kind` (e.g. `kind: "command"`, `type: "local_bash"`).
 */
export function hasOnlyLocalCommandBackgroundWork(
  h: HookSummary | null | undefined,
): boolean {
  if (!h || !hasActiveFiniteWork(h)) {
    return false;
  }
  if ((h.waiting_on ?? "").toLowerCase().includes("subagent")) {
    return false;
  }
  for (const ch of h.children ?? []) {
    if (childIsActive(ch) && childIsFinite(ch)) {
      return false;
    }
  }
  let hasLocalCmd = false;
  for (const item of h.background_tasks ?? []) {
    if (!childIsActive(item) || !childIsFinite(item)) {
      continue;
    }
    const role = (item.kind ?? "").trim().toLowerCase();
    const typ = (item.type ?? "").trim().toLowerCase();
    if (role === "command" || typ === "local_bash") {
      hasLocalCmd = true;
    } else {
      return false;
    }
  }
  return hasLocalCmd;
}

/**
 * Reports whether `pane` represents a coding agent (as opposed to a plain
 * shell or non-agent terminal), matching `Pane.IsAgent()` in Go.
 */
export function isAgentPane(
  pane: Pane,
  registry: DetectionRegistry = defaultDetectionRegistry(),
): boolean {
  if (pane.is_dead && pane.status !== "failed") {
    return false;
  }
  if (pane.hook !== null) {
    return true;
  }
  if (
    pane.has_spaces_meta &&
    !registry.isShellApp(pane.app) &&
    pane.app !== "" &&
    pane.app !== "terminal"
  ) {
    return true;
  }
  if (registry.isKnownAgent(pane.app)) {
    return true;
  }
  const cmd = registry.commandName(pane.command);
  if (cmd !== "" && registry.isKnownAgent(cmd)) {
    return true;
  }
  return false;
}

// --- Pre-pass & layer evaluation functions ---

/**
 * Fills in unresolved pane `app` labels from matching hook records before
 * screen-reader capture runs, so panes whose process command is generic can
 * still be evaluated by Layer 3.
 */
export function seedAppsFromHooks(
  spaces: Space[],
  hooks: HookSnapshot | null | undefined,
  registry: DetectionRegistry = defaultDetectionRegistry(),
): void {
  if (!hooks) {
    return;
  }
  for (const space of spaces) {
    const sessionHooks = hooks.panes?.[space.name];
    if (!sessionHooks) {
      continue;
    }
    for (const pane of space.panes) {
      if (
        pane.returned_to_shell ||
        registry.isKnownAgent(pane.app) ||
        registry.isShellApp(pane.app) ||
        registry.isShellCommand(registry.commandName(pane.command))
      ) {
        continue;
      }
      const h = sessionHooks[String(pane.numeric_id)];
      if (!h) {
        continue;
      }
      const app = (h.app ?? "").trim();
      const agent = (h.agent ?? "").trim();
      if (app !== "" && !registry.isShellApp(app)) {
        pane.app = app;
      } else if (agent !== "" && !registry.isShellApp(agent)) {
        pane.app = agent;
      }
    }
  }
}

/**
 * Decides whether a hook summary owns a pane's state: hooks win while fresh,
 * hook idle never overrides, and a newer live base beats an older hook record.
 */
export function hookView(
  record: HookSummary,
  base: Status,
  baseAgeMs: number,
  nowMs: number,
): HookViewResult {
  const ts = record.ts_ms;
  let since = record.state_since_ms;
  if (since <= 0) {
    since = ts;
  }
  const stateAge = saturatingSub(nowMs, since);
  const silence = saturatingSub(nowMs, ts);

  switch (record.state) {
    case "working":
      if (!hasActiveFiniteWork(record) && silence > HOOK_WORKING_STALE_MS) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      break;
    case "idle":
      if (silence > HOOK_WORKING_STALE_MS) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      break;
    case "done":
      if (stateAge > HOOK_DONE_TTL_MS) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      break;
    case "failed":
      if (stateAge > HOOK_FAILED_TTL_MS) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      break;
    case "blocked":
    case "waiting":
      if (isAuthBlock(record)) {
        if (stateAge > HOOK_AUTH_TTL_MS) {
          return { status: "idle", ageMs: 0, text: "", fresh: false };
        }
      } else if (stateAge > HOOK_BLOCK_TTL_MS) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      break;
  }

  const baseIsNewer = baseAgeMs > 0 && baseAgeMs < silence;
  switch (record.state) {
    case "working":
      if ((base === "done" || base === "failed" || base === "idle") && baseIsNewer) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      return { status: "working", ageMs: stateAge, text: record.text, fresh: true };
    case "blocked":
    case "waiting":
      if (!isAuthBlock(record) && (base === "done" || base === "idle") && baseIsNewer) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      return { status: "needs_you", ageMs: stateAge, text: record.text, fresh: true };
    case "done":
      if ((base === "working" || base === "needs_you") && baseIsNewer) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      return { status: "done", ageMs: stateAge, text: record.text, fresh: true };
    case "failed":
      if ((base === "working" || base === "needs_you") && baseIsNewer) {
        return { status: "idle", ageMs: 0, text: "", fresh: false };
      }
      return { status: "failed", ageMs: stateAge, text: record.text, fresh: true };
    default:
      return { status: "idle", ageMs: 0, text: "", fresh: false };
  }
}

/**
 * Reports whether an idle Codex viewport indicates a completed turn after a
 * missed Stop hook (past the 10s idle grace window with no active finite work).
 */
export function codexIdleCompletesTurn(
  pane: Pane,
  reading: ScreenReading,
  nowMs: number,
  registry: DetectionRegistry = defaultDetectionRegistry(),
): boolean {
  if (
    registry.canonicalOrNormalized(pane.app) !== "codex" ||
    reading.state !== "idle" ||
    !reading.visible_idle ||
    reading.visible_working ||
    reading.visible_blocker
  ) {
    return false;
  }
  if (pane.base_status === "working" && pane.base_age_ms <= CODEX_IDLE_GRACE_MS) {
    return false;
  }
  const hook = pane.hook;
  if (
    hook !== null &&
    (hasActiveFiniteWork(hook) ||
      (hook.state === "working" && saturatingSub(nowMs, hook.ts_ms) <= CODEX_IDLE_GRACE_MS))
  ) {
    return false;
  }
  return (
    pane.base_status === "working" ||
    (pane.base_status === "idle" && hook !== null && hook.state === "working")
  );
}

function lookupReading(
  readings: Record<string, ScreenReading> | Map<string, ScreenReading> | null | undefined,
  paneId: string,
): ScreenReading | undefined {
  if (!readings) {
    return undefined;
  }
  if (readings instanceof Map) {
    return readings.get(paneId);
  }
  return Object.prototype.hasOwnProperty.call(readings, paneId) ? readings[paneId] : undefined;
}

/**
 * Applies hook summaries and screen readings over freshly built spaces.
 * Tracks per-pane screen-synthesized `done` memory (working -> idle transitions)
 * with a 10-minute TTL.
 */
export class Arbitrator {
  private readonly done = new Map<string, number>();
  private readonly prev = new Map<string, string>();
  private newlyDone: string[] = [];
  private readonly registry: DetectionRegistry;
  private readonly procIsLive?: (pid: number) => boolean;
  private readonly detectDescendantAgent?: (pid: number) => string;

  constructor(options: ArbitratorOptions = {}) {
    this.registry = options.registry ?? defaultDetectionRegistry();
    this.procIsLive = options.procIsLive;
    this.detectDescendantAgent = options.detectDescendantAgent;
  }

  /**
   * Returns and clears the pane IDs whose screen state just transitioned from
   * working to done during the most recent `apply` call.
   */
  takeNewlyDone(): string[] {
    if (this.newlyDone.length === 0) {
      return [];
    }
    const out = this.newlyDone.slice();
    this.newlyDone = [];
    return out;
  }

  /**
   * Recomputes pane and space state idempotently from `base_status` / `base_text` /
   * `base_age_ms` / `base_conv_title`, hooks, and screen readings, then updates
   * `counts` / `status`, sorts panes and spaces, and prunes vanished pane IDs.
   */
  apply(
    spaces: Space[],
    hooks: HookSnapshot | null | undefined,
    readings: Record<string, ScreenReading> | Map<string, ScreenReading> | null | undefined,
    nowMs: number,
  ): void {
    this.newlyDone = [];
    const seen = new Set<string>();

    for (const space of spaces) {
      for (const pane of space.panes) {
        seen.add(pane.id);

        // 1. Reset to baseline so ticks are idempotent.
        pane.status = pane.base_status;
        pane.text = pane.base_text;
        pane.age_ms = pane.base_age_ms;
        pane.conversation_title = pane.base_conv_title;
        pane.screen_title = "";
        pane.hook = null;

        // Attach hook summary unless the pane returned to a shell or belongs
        // to a different live agent.
        let hook: HookSummary | null = null;
        if (hooks && !pane.returned_to_shell) {
          const sessionHooks = hooks.panes?.[space.name];
          const candidate = sessionHooks?.[String(pane.numeric_id)];
          if (candidate) {
            let skip = false;
            if (
              (this.registry.isShellApp(pane.app) ||
                this.registry.isShellCommand(this.registry.commandName(pane.command))) &&
              pane.pid > 0 &&
              this.procIsLive &&
              this.procIsLive(pane.pid)
            ) {
              if (!this.detectDescendantAgent || this.detectDescendantAgent(pane.pid) === "") {
                skip = true;
              }
            }
            if (!skip && this.registry.isKnownAgent(pane.app)) {
              let hookApp = (candidate.app ?? "").trim();
              if (hookApp === "") {
                hookApp = (candidate.agent ?? "").trim();
              }
              if (
                hookApp !== "" &&
                this.registry.canonicalOrNormalized(hookApp) !==
                  this.registry.canonicalOrNormalized(pane.app)
              ) {
                skip = true;
              }
            }
            if (
              !skip &&
              this.registry.canonicalOrNormalized(pane.app) === "codex" &&
              pane.resume_session !== "" &&
              candidate.session_id !== "" &&
              pane.resume_session !== candidate.session_id
            ) {
              skip = true;
            }
            if (!skip) {
              hook = { ...candidate };
              pane.hook = hook;
              const ht = (candidate.title ?? "").trim();
              if (ht !== "") {
                pane.conversation_title = ht;
              }
              if (!this.registry.isKnownAgent(pane.app) && !this.registry.isShellApp(pane.app)) {
                const app = (candidate.app ?? "").trim();
                const agent = (candidate.agent ?? "").trim();
                if (app !== "" && !this.registry.isShellApp(app)) {
                  pane.app = app;
                } else if (agent !== "" && !this.registry.isShellApp(agent)) {
                  pane.app = agent;
                }
              }
            }
          }
        }

        // 2. Screen pass over the base.
        const base = pane.base_status;
        let status: Status = base;
        let clearDone = false;
        const reading = lookupReading(readings, pane.id);
        let hasReading = reading !== undefined;
        let visibleBlocker = false;
        let codexTurnEnded = false;
        const prevNewlyDoneLen = this.newlyDone.length;

        if (hasReading && reading !== undefined) {
          const readingApp = reading.app ?? "";
          if (
            readingApp !== "" &&
            pane.app !== "" &&
            this.registry.canonicalOrNormalized(readingApp) !==
              this.registry.canonicalOrNormalized(pane.app)
          ) {
            hasReading = false;
          } else {
            visibleBlocker = Boolean(reading.visible_blocker);
            codexTurnEnded = codexIdleCompletesTurn(pane, reading, nowMs, this.registry);
            const screenRes = this.screenStatus(pane, reading, base, nowMs, codexTurnEnded);
            status = screenRes.status;
            clearDone = screenRes.clearDone;
            const st = (reading.title ?? "").trim();
            if (st !== "") {
              pane.screen_title = st;
            }
          }
        }
        if (hasReading && reading !== undefined) {
          if (
            reading.state === "working" ||
            reading.state === "idle" ||
            reading.state === "blocked"
          ) {
            this.prev.set(pane.id, reading.state);
          }
        }

        // 3. Done-memory pass (screen-synthesized working->idle, 10m TTL).
        // Stays done even when `pane.is_active === true`; clears only on `clearDone || expired`.
        const doneAt = this.done.get(pane.id);
        if (doneAt !== undefined) {
          const expired = nowMs >= doneAt && nowMs - doneAt > DONE_TTL_MS;
          if (clearDone || expired) {
            this.done.delete(pane.id);
            if (status === "done") {
              status = "idle";
            }
          } else if (status === "idle") {
            status = "done";
          }
          if (status === "done") {
            pane.age_ms = saturatingSub(nowMs, doneAt);
          }
        }

        // 4. Fresh hooks win over screen evidence, except a visible blocker on screen.
        if (hook !== null) {
          const hookSource = (hook.source ?? "").trim();
          const isDaemonSSE = hookSource.toLowerCase() === "daemon_sse";
          const hookSilence = saturatingSub(nowMs, hook.ts_ms);
          const screenFinishedNewerTurn =
            !isDaemonSSE &&
            this.newlyDone.length > prevNewlyDoneLen &&
            status === "done" &&
            (codexTurnEnded || (pane.base_age_ms > 0 && pane.base_age_ms < hookSilence));
          const policy = this.registry.hookPolicyFor(pane.app);
          const screenState = hasReading && reading !== undefined ? reading.state : "";
          const yieldWorkingToIdle =
            !isDaemonSSE &&
            policy.shouldYieldWorkingHookToIdle({
              hookState: hook.state,
              hookSilenceMs: hookSilence,
              hasActiveFiniteWork: hasActiveFiniteWork(hook),
              hasOnlyLocalCmdBackground: hasOnlyLocalCommandBackgroundWork(hook),
              visibleIdle: hasReading && Boolean(reading?.visible_idle),
              visibleWorking: hasReading && Boolean(reading?.visible_working),
              visibleBlocker: hasReading && Boolean(reading?.visible_blocker),
              screenState,
            });

          if (!(hook.state === "working" && screenFinishedNewerTurn) && !yieldWorkingToIdle) {
            const hv = hookView(hook, base, pane.base_age_ms, nowMs);
            if (hv.fresh) {
              status = hv.status;
              pane.age_ms = hv.ageMs;
              if (hv.text.trim() !== "") {
                pane.text = hv.text;
              }
              if (hookSource !== "") {
                pane.source = hookSource;
              }
              if (visibleBlocker && status !== "needs_you") {
                status = "needs_you";
              }
              if (this.newlyDone.length > prevNewlyDoneLen) {
                this.newlyDone.length = prevNewlyDoneLen;
              }
              if (status !== "done") {
                this.done.delete(pane.id);
              }
            }
          }
        }

        pane.status = status;
      }

      const counts = emptyCounts();
      for (const pane of space.panes) {
        addStatusCount(counts, pane.status);
      }
      space.counts = counts;
      space.status = rollupCounts(counts);
      sortPanes(space.panes);
    }

    // Prune memory for panes that no longer exist.
    for (const id of this.done.keys()) {
      if (!seen.has(id)) {
        this.done.delete(id);
      }
    }
    for (const id of this.prev.keys()) {
      if (!seen.has(id)) {
        this.prev.delete(id);
      }
    }
    sortSpaces(spaces);
  }

  private screenStatus(
    pane: Pane,
    reading: ScreenReading,
    base: Status,
    nowMs: number,
    codexTurnEnded: boolean,
  ): { status: Status; clearDone: boolean } {
    if (reading.skip_state_update) {
      return { status: base, clearDone: false };
    }
    if (base === "failed") {
      return { status: "failed", clearDone: true };
    }
    const authorityActive =
      (Boolean(reading.osc_authority) || this.registry.isOscAuthority(pane.app)) &&
      !idleLike(base);
    if (reading.visible_blocker || (reading.state === "blocked" && !authorityActive)) {
      return { status: "needs_you", clearDone: true };
    }
    if (base === "done") {
      if (reading.state === "working" && reading.visible_working) {
        return { status: "working", clearDone: true };
      }
      return { status: "done", clearDone: true };
    }
    if (authorityActive && !codexTurnEnded) {
      return { status: base, clearDone: false };
    }
    switch (reading.state) {
      case "working":
        if (reading.visible_working) {
          return { status: "working", clearDone: true };
        }
        return { status: base, clearDone: false };
      case "idle":
      case "unknown": {
        const persistedScreenWorking =
          base === "working" && (pane.source ?? "").trim().toLowerCase() === "screen";
        if (reading.state === "unknown") {
          if (!persistedScreenWorking) {
            return { status: base, clearDone: false };
          }
        } else if (!reading.visible_idle && !persistedScreenWorking) {
          return { status: base, clearDone: false };
        }
        const wasWorking =
          base === "working" ||
          base === "needs_you" ||
          this.prev.get(pane.id) === "working" ||
          codexTurnEnded;
        const hasDone = this.done.has(pane.id);
        if (wasWorking) {
          this.done.set(pane.id, nowMs);
          this.newlyDone.push(pane.id);
          return { status: "done", clearDone: false };
        }
        if (hasDone) {
          return { status: "done", clearDone: false };
        }
        return { status: "idle", clearDone: false };
      }
      default:
        return { status: base, clearDone: false };
    }
  }
}

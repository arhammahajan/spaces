import { existsSync, readFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  SPACES_CONFIG_DIR_NAME,
  type BackendVerb,
  type BackendVerbResult,
  type HarnessEntry,
  type HarnessFile,
  type HookSnapshot,
  type HookSummary,
  type Pane,
  type Space,
  type Status,
  type Window,
} from "@spaces/types";
import {
  Arbitrator,
  commandName,
  defaultDetectionRegistry,
  emptyCounts,
  isShellCommand,
  type DetectionRegistry,
  type ScreenReading,
} from "../detection/index";
import type { DriverPane, DriverSnapshot, MultiplexerDriver } from "../drivers/types";
import {
  captureOpenState,
  captureSpaces,
  type CaptureOptions,
} from "../resurrect/capture";
import {
  agentBinaryName,
  autoRestore,
  restoreSpace,
  type AutoRestoreResult,
  type VendorSessionLookup,
} from "../resurrect/restore";
import type { SpacesStore } from "../store/db";

export interface BackendServiceOptions {
  /** Path to the launcher binary (`tmux-spaces` / `spaces`) used by `__shim` and restore. */
  selfPath?: string;
  /** Config root directory (defaults to `$SPACES_DIR` or `~/.spaces`). */
  spacesDir?: string;
  /** Optional override path for `harnesses.json`. */
  harnessesPath?: string;
  /** Optional detection registry (defaults to `defaultDetectionRegistry()`). */
  registry?: DetectionRegistry;
  /** Optional vendor store lookup for Tier 6 restore resolution. */
  vendorLookup?: VendorSessionLookup;
  /** Optional capture overrides (for deterministic tests). */
  captureOptions?: CaptureOptions;
  /** Injectable clock for deterministic tests. */
  now?: () => number;
}

const DEFAULT_AUTOSAVE_INTERVAL_MS = 30_000;

function normalizeBaseStatus(raw: string): Status {
  switch (raw.trim().toLowerCase()) {
    case "blocked":
    case "needs_you":
    case "waiting":
      return "needs_you";
    case "failed":
    case "error":
      return "failed";
    case "done":
      return "done";
    case "working":
    case "busy":
    case "running":
      return "working";
    case "quiet":
      return "quiet";
    case "new_output":
      return "new_output";
    default:
      return "idle";
  }
}

function defaultSpacesDir(): string {
  const env = process.env.SPACES_DIR?.trim();
  if (env) {
    return env;
  }
  return path.join(os.homedir(), SPACES_CONFIG_DIR_NAME);
}

/**
 * Executes multiplexer-agnostic `BackendVerb`s and space/harness/resurrect operations
 * over any `MultiplexerDriver` (`tmux` now, `kitty` / `zellij` later) backed by `SpacesStore`.
 */
export class BackendService {
  readonly driver: MultiplexerDriver;
  readonly store: SpacesStore;

  private readonly selfPath: string;
  private readonly spacesDir: string;
  private readonly harnessesPath?: string;
  private readonly registry: DetectionRegistry;
  private readonly arbitrator: Arbitrator;
  private readonly vendorLookup?: VendorSessionLookup;
  private readonly captureOptions?: CaptureOptions;
  private readonly now: () => number;
  private lastAutosaveMs = 0;

  constructor(
    driver: MultiplexerDriver,
    store: SpacesStore,
    options: BackendServiceOptions = {},
  ) {
    this.driver = driver;
    this.store = store;
    this.selfPath = options.selfPath ?? "tmux-spaces";
    this.spacesDir = options.spacesDir ?? defaultSpacesDir();
    this.harnessesPath = options.harnessesPath;
    this.registry = options.registry ?? defaultDetectionRegistry();
    this.arbitrator = new Arbitrator({ registry: this.registry });
    this.vendorLookup = options.vendorLookup;
    this.captureOptions = options.captureOptions;
    this.now = options.now ?? (() => Date.now());
  }

  /**
   * Executes one `BackendVerb` from `backend.exec` RPC.
   */
  async execVerb(verb: BackendVerb): Promise<BackendVerbResult> {
    switch (verb.verb) {
      case "open_space": {
        await this.driver.switchToSpace(verb.name);
        return { kind: "ack" };
      }

      case "focus_pane": {
        await this.driver.focusPane(verb.session, verb.window_id, verb.pane_id);
        return { kind: "ack" };
      }

      case "create_space": {
        await this.driver.newSession(verb.name, verb.cwd);
        try {
          this.store.addOpenSpace(verb.name, true);
        } catch {
          // Best-effort persistence matching cmd/tmux-spaces/main.go.
        }
        return { kind: "ack" };
      }

      case "rename_space": {
        await this.driver.renameSession(verb.old_name, verb.new_name);
        try {
          const existing = this.store.getResurrectSpace(verb.old_name);
          if (existing) {
            this.store.saveResurrectSpace({ ...existing, name: verb.new_name });
            this.store.deleteResurrectSpace(verb.old_name);
          }
          this.store.renameOpenSpace(verb.old_name, verb.new_name);
        } catch {
          // Best-effort persistence.
        }
        return { kind: "ack" };
      }

      case "kill_space": {
        await this.driver.killSession(verb.name);
        try {
          // Explicit close deletes both snapshot and open-space entry atomically.
          this.store.deleteResurrectSpace(verb.name);
        } catch {
          // Best-effort persistence.
        }
        return { kind: "ack" };
      }

      case "new_window": {
        await this.driver.newWindow(verb.session, verb.cwd, verb.name ?? "");
        return { kind: "ack" };
      }

      case "rename_window": {
        await this.driver.renameWindow(verb.window_id, verb.new_name);
        return { kind: "ack" };
      }

      case "kill_window": {
        await this.driver.killWindow(verb.window_id);
        return { kind: "ack" };
      }

      case "launch_agent": {
        let session = verb.session.trim();
        if (session === "") {
          session = await this.driver.currentSession();
          if (!session) {
            throw new Error("could not determine current space");
          }
        }
        const profile = (verb.profile ?? "").trim();
        const label = profile !== "" && profile !== "default" ? profile : verb.agent;
        const cmd =
          profile !== ""
            ? [this.selfPath, "__shim", "--profile", profile, verb.agent]
            : [this.selfPath, "__shim", verb.agent, agentBinaryName(verb.agent)];

        await this.driver.newWindow(session, verb.cwd, label, cmd);
        return { kind: "ack" };
      }

      case "restore_space": {
        const snap = this.store.getResurrectSpace(verb.name);
        if (!snap) {
          throw new Error(`resurrect snapshot not found: ${verb.name}`);
        }
        await restoreSpace(this.driver, snap, {
          selfPath: this.selfPath,
          switch: true,
          resolveSession: this.vendorLookup,
        });
        try {
          this.store.addOpenSpace(verb.name, true);
        } catch {
          // Best-effort persistence.
        }
        return { kind: "ack" };
      }

      case "delete_exited_space": {
        this.store.deleteResurrectSpace(verb.name);
        return { kind: "ack" };
      }

      case "capture_previews": {
        const previews: Record<string, string> = {};
        for (const paneId of verb.pane_ids) {
          if (!paneId) {
            continue;
          }
          try {
            previews[paneId] = await this.driver.capturePaneStyled(paneId);
          } catch {
            // Ignore per-pane capture errors matching cmd/tmux-spaces/main.go.
          }
        }
        return { kind: "previews", previews };
      }

      default:
        throw new Error(`backend verb not yet implemented in spacesd: ${verb.verb}`);
    }
  }

  /**
   * Builds live `Space[]` from the multiplexer snapshot, attaches hook state from
   * `SpacesStore`, and runs the Step 3 three-layer `Arbitrator`.
   */
  async loadSpaces(readings?: Map<string, ScreenReading>): Promise<Space[]> {
    const snap = await this.driver.snapshot();
    const nowMs = this.now();
    const spaces = this.buildSpacesFromSnapshot(snap, nowMs);
    const hookSnap = this.buildHookSnapshot(nowMs);
    this.arbitrator.apply(spaces, hookSnap, readings ?? null, nowMs);
    return spaces;
  }

  /**
   * Loads launchable harnesses from `$SPACES_HARNESSES_PATH`, `$TMUX_SPACES_HARNESSES_FILE`,
   * or `~/.spaces/harnesses.json`.
   */
  loadHarnesses(): HarnessEntry[] {
    const candidates = [
      this.harnessesPath,
      process.env.SPACES_HARNESSES_PATH?.trim(),
      process.env.TMUX_SPACES_HARNESSES_FILE?.trim(),
      path.join(this.spacesDir, "harnesses.json"),
    ].filter((p): p is string => Boolean(p && p.length > 0));

    for (const filePath of candidates) {
      if (!existsSync(filePath)) {
        continue;
      }
      try {
        const raw = readFileSync(filePath, "utf8");
        const parsed = JSON.parse(raw) as Partial<HarnessFile>;
        if (!Array.isArray(parsed.agents)) {
          continue;
        }
        return parsed.agents.filter((entry) => {
          if (entry.profiles && entry.profiles.length > 0) {
            return true;
          }
          const state = (entry.binary_state ?? "").trim().toLowerCase();
          return state === "" || state === "ok";
        });
      } catch {
        continue;
      }
    }
    return [];
  }

  /**
   * Captures all running spaces and open state into `SpacesStore`, honoring the
   * `@spaces_restored == "pending"` startup guard and 30s autosave throttle.
   */
  async autosaveSpaces(force = false): Promise<{ saved: number; skipped: boolean }> {
    const nowMs = this.now();
    if (!force && this.lastAutosaveMs > 0 && nowMs - this.lastAutosaveMs < DEFAULT_AUTOSAVE_INTERVAL_MS) {
      return { saved: 0, skipped: true };
    }

    const restoredState = await this.driver.getGlobalOption("@spaces_restored").catch(() => "");
    if (restoredState.trim() === "pending") {
      return { saved: 0, skipped: true };
    }

    const snap = await this.driver.snapshot();
    if (snap.sessions.length === 0) {
      return { saved: 0, skipped: false };
    }

    const hookSnap = this.buildHookSnapshot(nowMs);
    const spaceSnaps = captureSpaces(snap, hookSnap, this.store, {
      ...this.captureOptions,
      nowMs,
    });
    const openState = captureOpenState(snap, nowMs);

    this.store.transaction(() => {
      for (const sp of spaceSnaps) {
        this.store.saveResurrectSpace(sp);
      }
      this.store.saveOpenState(openState);
    });

    this.lastAutosaveMs = nowMs;
    return { saved: spaceSnaps.length, skipped: false };
  }

  /**
   * Runs startup `autoRestore` using `MultiplexerDriver` and `SpacesStore`.
   */
  async autoRestoreSpaces(): Promise<AutoRestoreResult> {
    return autoRestore(this.driver, this.store, {
      selfPath: this.selfPath,
      resolveSession: this.vendorLookup,
    });
  }

  private buildHookSnapshot(nowMs: number): HookSnapshot {
    const records = this.store.listHookPanes(nowMs);
    const panes: Record<string, Record<string, HookSummary>> = {};
    for (const rec of records) {
      let sessionMap = panes[rec.session];
      if (!sessionMap) {
        sessionMap = {};
        panes[rec.session] = sessionMap;
      }
      sessionMap[rec.pane] = {
        agent: rec.agent,
        app: rec.app,
        state: rec.state,
        kind: rec.kind ?? "",
        event: rec.event,
        source: rec.source,
        ts_ms: rec.ts_ms,
        state_since_ms: rec.state_since_ms,
        turn_started_ms: rec.turn_started_ms,
        text: rec.text,
        title: rec.title ?? "",
        model: rec.model ?? "",
        session_id: rec.session_id ?? "",
        cwd: rec.cwd ?? "",
        usage: rec.usage ?? null,
        child_count: rec.children.length,
        background: rec.background_tasks.length,
        finite_active: 0,
        service_active: 0,
        waiting_on: rec.waiting_on ?? "",
        children: rec.children,
        background_tasks: rec.background_tasks,
        has_detail: Boolean(rec.prompt || rec.tool || rec.reply || rec.usage),
      };
    }

    return {
      ok: true,
      version: 1,
      now_ms: nowMs,
      spool_pending: 0,
      panes,
      outcomes: this.store.listOutcomes(nowMs),
    };
  }

  private buildSpacesFromSnapshot(snap: DriverSnapshot, nowMs: number): Space[] {
    const windowsBySession = new Map<string, Window[]>();
    for (const w of snap.windows) {
      const list = windowsBySession.get(w.sessionName) ?? [];
      list.push({
        id: w.id,
        index: w.index,
        name: w.name,
        active: w.active,
        last: w.last,
        zoomed: w.zoomed,
        bell: w.bell,
        activity: w.activity,
        silence: w.silence,
        width: w.width,
        height: w.height,
        activity_at_ms: w.activityAtSec > 0 ? w.activityAtSec * 1000 : 0,
        layout: w.layout,
        worktree_path: w.worktreePath,
        worktree_name: w.worktreeName,
        worktree_base: w.worktreeBase,
        worktree_managed: w.worktreeManaged,
      });
      windowsBySession.set(w.sessionName, list);
    }

    const panesBySession = new Map<string, Pane[]>();
    for (const p of snap.panes) {
      const list = panesBySession.get(p.sessionName) ?? [];
      list.push(this.toModelPane(p, nowMs));
      panesBySession.set(p.sessionName, list);
    }

    return snap.sessions.map((s) => {
      const windows = windowsBySession.get(s.name) ?? [];
      const panes = panesBySession.get(s.name) ?? [];
      const activePane = panes.find((p) => p.is_active && p.window_active) ?? panes[0];
      return {
        name: s.name,
        id: s.id,
        is_current: s.name === snap.currentSession,
        windows,
        panes,
        cwd: activePane?.cwd ?? "",
        git: null,
        worktree_path: s.worktreePath,
        worktree_name: s.worktreeName,
        worktree_base: s.worktreeBase,
        worktree_managed: s.worktreeManaged,
        counts: emptyCounts(),
        status: "idle",
        connected_clients: s.attached,
        created_at_ms: s.createdAtSec > 0 ? s.createdAtSec * 1000 : 0,
      };
    });
  }

  private toModelPane(p: DriverPane, nowMs: number): Pane {
    const baseStatus = normalizeBaseStatus(p.spacesState);
    const baseAgeMs = p.spacesTsMs > 0 && nowMs >= p.spacesTsMs ? nowMs - p.spacesTsMs : 0;
    const hasSpacesMeta = Boolean(p.spacesApp || p.spacesState || p.spacesSource);
    const cmd = commandName(p.currentCommand);
    const returnedToShell = hasSpacesMeta && isShellCommand(cmd) && !this.registry.isKnownAgent(cmd);
    const app = p.spacesApp || (cmd || "terminal");

    return {
      id: p.id,
      numeric_id: p.numericId,
      pane_index: p.paneIndex,
      window_id: p.windowId,
      window_index: p.windowIndex,
      window_name: p.windowName,
      title: p.title,
      conversation_title: p.spacesTitle,
      base_conv_title: p.spacesTitle,
      screen_title: "",
      resume_session: p.resumeSession,
      profile: p.profile,
      command: p.currentCommand,
      start_command: p.startCommand,
      foreground_cmd: "",
      launch_command: p.launchCommand,
      child_process_count: 0,
      app,
      status: baseStatus,
      base_status: baseStatus,
      text: p.spacesMsg,
      base_text: p.spacesMsg,
      kind: p.spacesKind,
      source: p.spacesSource,
      hook: null,
      git: null,
      cwd: p.currentPath,
      age_ms: baseAgeMs,
      base_age_ms: baseAgeMs,
      width: p.width,
      height: p.height,
      is_active: p.active,
      window_active: p.windowActive,
      in_mode: p.inMode,
      synchronized: p.synchronized,
      is_dead: p.dead,
      dead_status: p.deadStatus,
      has_spaces_meta: hasSpacesMeta,
      returned_to_shell: returnedToShell,
      pid: p.pid,
    };
  }
}

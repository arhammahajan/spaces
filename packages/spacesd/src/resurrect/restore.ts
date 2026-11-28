import { statSync } from "node:fs";
import * as path from "node:path";
import { SAFE_RESTORE_COMMANDS, validateResumeCommand } from "../core/hmac";
import { defaultDetectionRegistry } from "../detection/policies";
import type { DriverSnapshot, MultiplexerDriver } from "../drivers/types";
import type {
  ResurrectPaneSnapshot,
  ResurrectSpaceSnapshot,
  ResurrectWindowSnapshot,
  SpacesStore,
} from "../store/db";
import {
  canonicalAgent,
  extractSessionIdFromArgs,
  harnessLaunchArgs,
  harnessProfileCommand,
  harnessProfileFromArgv,
  harnessResumeCommand,
  type HarnessResumePolicy,
  type VendorSessionLookup,
} from "./capture";

export type { HarnessResumePolicy, VendorSessionLookup };

/** Global multiplexer option guarding auto-restore against concurrent autosaves and re-runs. */
export const SPACES_RESTORED_OPTION = "@spaces_restored";

const BOOTSTRAP_SHELLS = new Set([
  "",
  "sh",
  "dash",
  "bash",
  "zsh",
  "fish",
  "nu",
  "ksh",
  "csh",
  "tcsh",
  "login",
]);

export interface SessionResolution {
  sessionIds: Record<string, string>;
  skipResume: Record<string, boolean>;
  duplicatePanes: Record<string, boolean>;
}

export interface BuildPaneCommandOptions {
  selfPath?: string;
  skipResume?: boolean;
  duplicatePane?: boolean;
  verifiedResumeCmd?: string[];
  resumePolicy?: HarnessResumePolicy;
}

export interface RestoreOptions {
  selfPath?: string;
  switch?: boolean;
  resolveSession?: VendorSessionLookup;
  resumePolicy?: HarnessResumePolicy;
  isDir?: (path: string) => boolean;
}

export interface AutoRestoreOptions extends RestoreOptions {
  pid?: number;
  force?: boolean;
}

export interface AutoRestoreResult {
  restored: string[];
  skipped: string[];
  activeSession: string;
  removedPlaceholder: boolean;
  errors: Error[];
}

interface SessionPaneRef {
  key: string;
  agent: string;
  sessionId: string;
  profile: string;
  cwd: string;
  title: string;
  fullCommand: string[];
}

/**
 * Returns the executable binary name for direct agent launches, consulting
 * `policy?.binaryName` first and falling back to the detection registry spec.
 */
export function agentBinaryName(agent: string, policy?: HarnessResumePolicy): string {
  const fromPolicy = policy?.binaryName?.(agent)?.trim();
  if (fromPolicy) {
    return fromPolicy;
  }
  const spec = defaultDetectionRegistry().resolveRuntime(agent);
  if (spec) {
    return spec.binary || spec.id;
  }
  return agent.trim();
}

/**
 * Returns the native CLI resume arguments for `agent` and `sessionId` via the
 * injected `HarnessResumePolicy`, or `[]` when no plugin policy is registered.
 */
export function agentResumeArgs(
  agent: string,
  sessionId: string,
  policy?: HarnessResumePolicy,
): string[] {
  const sid = sessionId.trim();
  if (sid === "" || !policy?.resumeArgs) {
    return [];
  }
  return policy.resumeArgs(agent, sid);
}

/**
 * Resolves and deduplicates agent conversation/session IDs across all panes in
 * `snap` so two panes never resume the same conversation and exhausted vendor
 * stores suppress duplicate resume flags.
 */
export function resolveAgentSessions(
  snap: ResurrectSpaceSnapshot,
  lookup?: VendorSessionLookup,
  policy?: HarnessResumePolicy,
): SessionResolution {
  const resolution: SessionResolution = {
    sessionIds: {},
    skipResume: {},
    duplicatePanes: {},
  };

  const effectiveLookup = lookup ?? policy?.resolveVendorSession;
  const byAgent = new Map<string, SessionPaneRef[]>();
  const agentOrder: string[] = [];

  for (let wi = 0; wi < snap.windows.length; wi += 1) {
    const win = snap.windows[wi]!;
    for (let pi = 0; pi < win.panes.length; pi += 1) {
      const p = win.panes[pi]!;
      const rawAgent = (p.agent ?? "").trim();
      if (rawAgent === "") {
        continue;
      }
      const agentId = canonicalAgent(rawAgent) || rawAgent;
      if (!byAgent.has(agentId)) {
        byAgent.set(agentId, []);
        agentOrder.push(agentId);
      }
      const fullCommand = p.full_command ?? [];
      byAgent.get(agentId)!.push({
        key: `${wi}:${pi}`,
        agent: agentId,
        sessionId: (p.session_id ?? "").trim(),
        profile: (p.profile ?? "").trim() || harnessProfileFromArgv(fullCommand),
        cwd: p.cwd || snap.cwd || "",
        title: p.title ?? "",
        fullCommand,
      });
    }
  }

  for (const agentId of agentOrder) {
    const spec = defaultDetectionRegistry().resolveRuntime(agentId);
    if (!spec || spec.worktreeOnly) {
      continue;
    }

    const panes = byAgent.get(agentId) ?? [];
    if (panes.length === 0) {
      continue;
    }

    const uniqueExplicit = policy?.uniqueExplicitSessions?.(spec.id) ?? false;
    const positionalResume = policy?.positionalResume?.(agentId) ?? false;
    const claimed = new Set<string>();
    const pending: SessionPaneRef[] = [];

    for (const p of panes) {
      let sid = p.sessionId.trim();
      if (sid === "") {
        sid = extractSessionIdFromArgs(agentId, p.fullCommand, positionalResume);
      }
      if (sid !== "") {
        if (uniqueExplicit && claimed.has(sid)) {
          resolution.duplicatePanes[p.key] = true;
        }
        claimed.add(sid);
        continue;
      }
      pending.push(p);
    }

    if (pending.length === 0 || !effectiveLookup) {
      continue;
    }

    for (const p of pending) {
      const res = effectiveLookup(agentId, p.cwd, p.profile, p.title, claimed);
      const resolvedSid = res.sessionId.trim();
      if (resolvedSid !== "") {
        resolution.sessionIds[p.key] = resolvedSid;
        claimed.add(resolvedSid);
      } else if (res.storeAvailable && claimed.size > 0) {
        resolution.skipResume[p.key] = true;
      }
    }
  }

  return resolution;
}

function appendResumeArgs(
  cmd: readonly string[],
  agentName: string,
  sessionId: string,
  skipResume: boolean,
  policy?: HarnessResumePolicy,
): string[] {
  const out = cmd.slice();
  const sid = sessionId.trim();
  if (skipResume || sid === "") {
    return out;
  }
  const positional = policy?.positionalResume?.(agentName) ?? false;
  if (extractSessionIdFromArgs(agentName, out, positional) !== "") {
    return out;
  }
  const resumeArgs = agentResumeArgs(agentName, sid, policy);
  if (resumeArgs.length === 0) {
    return out;
  }
  return out.concat(resumeArgs);
}

/**
 * Decides the command vector (if any) to run when recreating `pane`:
 * 1. `duplicatePane` -> `null` (plain shell)
 * 2. Valid `verifiedResumeCmd` (Tier 1 MAC-verified OSC 7577 argv)
 * 3. `harnessLaunchArgs` -> `profile` -> direct agent command -> plugin binary fallback
 * 4. Allowlisted `SAFE_RESTORE_COMMANDS` interactive TUI -> command argv
 * 5. Anything else -> `null` (clean login shell)
 */
export function buildPaneCommand(
  pane: ResurrectPaneSnapshot,
  opts: BuildPaneCommandOptions = {},
): string[] | null {
  if (opts.duplicatePane) {
    return null;
  }

  const self = opts.selfPath && opts.selfPath !== "" ? opts.selfPath : "tmux-spaces";
  const skipResume = Boolean(opts.skipResume);
  const rawAgent = (pane.agent ?? "").trim();
  const agent = rawAgent !== "" ? canonicalAgent(rawAgent) || rawAgent : "";
  const profile = (pane.profile ?? "").trim();

  if (opts.verifiedResumeCmd && opts.verifiedResumeCmd.length > 0) {
    const checked = validateResumeCommand({
      agent: agent || undefined,
      argv: opts.verifiedResumeCmd,
    });
    if (checked.ok) {
      if (agent !== "") {
        const cmd = [self, "__shim"];
        if (profile !== "") {
          cmd.push("--profile", profile);
        }
        cmd.push(agent, ...checked.argv);
        return cmd;
      }
      return checked.argv;
    }
  }

  const fullCmd = pane.full_command ?? [];
  if (agent !== "") {
    const sid = (pane.session_id ?? "").trim();
    const resumeArgs =
      !skipResume && sid !== "" ? agentResumeArgs(agent, sid, opts.resumePolicy) : [];

    // Case 1: Harness launcher (e.g. `spaces agy profile saucerf166`).
    const launch = harnessLaunchArgs(fullCmd);
    if (launch.ok) {
      const cmdWithResume = appendResumeArgs(fullCmd, agent, sid, skipResume, opts.resumePolicy);
      const parsed = harnessLaunchArgs(cmdWithResume);
      const cmd = [self, "__shim"];
      if (profile !== "") {
        cmd.push("--profile", profile);
      }
      cmd.push(agent);
      return cmd.concat(harnessResumeCommand(self, parsed.level, ...parsed.args));
    }

    // Case 2: Profiled agent pane.
    if (profile !== "") {
      const cmd = [self, "__shim", "--profile", profile, agent];
      const args = harnessProfileCommand(agent, profile, ...resumeArgs);
      return cmd.concat(harnessResumeCommand(self, "", ...args));
    }

    // Case 3: Direct agent invocation captured in full_command.
    if (fullCmd.length > 0 && canonicalAgent(fullCmd[0]!) === agent) {
      const cmdWithResume = appendResumeArgs(fullCmd, agent, sid, skipResume, opts.resumePolicy);
      return [self, "__shim", agent, ...cmdWithResume];
    }

    // Case 4: Fallback to plugin binary name.
    const bin = agentBinaryName(agent, opts.resumePolicy) || agent;
    return [self, "__shim", agent, bin, ...resumeArgs];
  }

  // Case 5: Non-agent panes only restart allowlisted interactive TUIs.
  if (fullCmd.length > 0) {
    const base = path.basename(fullCmd[0]!).toLowerCase();
    if (SAFE_RESTORE_COMMANDS.has(base)) {
      const checked = validateResumeCommand({ argv: fullCmd });
      if (checked.ok) {
        return checked.argv;
      }
    }
  }

  return null;
}

function defaultIsDir(targetPath: string): boolean {
  try {
    return statSync(targetPath).isDirectory();
  } catch {
    return false;
  }
}

/**
 * Resolves a pane or space CWD for resurrection (`cwd -> worktreePath -> mainRepoRoot -> cwd`).
 */
export function resolveRestoreCwd(
  cwd: string,
  worktreePath: string,
  mainRepoRoot: string,
  isDir: (p: string) => boolean = defaultIsDir,
): string {
  if (cwd === "") {
    return cwd;
  }
  if (isDir(cwd)) {
    return cwd;
  }
  if (worktreePath !== "" && isDir(worktreePath)) {
    return worktreePath;
  }
  if (mainRepoRoot !== "" && isDir(mainRepoRoot)) {
    return mainRepoRoot;
  }
  return cwd;
}

function restoredPaneCommand(
  pane: ResurrectPaneSnapshot,
  opts: RestoreOptions,
  key: string,
  resolution: SessionResolution,
): string[] | null {
  if (resolution.duplicatePanes[key]) {
    return null;
  }
  const resolvedSid = resolution.sessionIds[key];
  const effectivePane =
    resolvedSid && resolvedSid !== "" ? { ...pane, session_id: resolvedSid } : pane;
  return buildPaneCommand(effectivePane, {
    selfPath: opts.selfPath,
    skipResume: Boolean(resolution.skipResume[key]),
    resumePolicy: opts.resumePolicy,
  });
}

async function ignoreOptionalError(fn: () => Promise<unknown>): Promise<void> {
  try {
    await fn();
  } catch {
    // Non-fatal layout/option/focus restoration error matching Go `_ = ...`.
  }
}

async function restoreSessionWorktreeOptions(
  driver: MultiplexerDriver,
  snap: ResurrectSpaceSnapshot,
): Promise<void> {
  if (snap.worktree_path) {
    await ignoreOptionalError(() =>
      driver.setSessionOption(snap.name, "@spaces_worktree_path", snap.worktree_path!),
    );
  }
  if (snap.worktree_name) {
    await ignoreOptionalError(() =>
      driver.setSessionOption(snap.name, "@spaces_worktree_name", snap.worktree_name!),
    );
  }
  if (snap.worktree_base) {
    await ignoreOptionalError(() =>
      driver.setSessionOption(snap.name, "@spaces_worktree_base", snap.worktree_base!),
    );
  }
}

async function restoreWindowWorktreeOptions(
  driver: MultiplexerDriver,
  winTarget: string,
  win: ResurrectWindowSnapshot,
): Promise<void> {
  if (win.worktree_path) {
    await ignoreOptionalError(() =>
      driver.setWindowOption(winTarget, "@spaces_worktree_path", win.worktree_path!),
    );
  }
  if (win.worktree_name) {
    await ignoreOptionalError(() =>
      driver.setWindowOption(winTarget, "@spaces_worktree_name", win.worktree_name!),
    );
  }
  if (win.worktree_base) {
    await ignoreOptionalError(() =>
      driver.setWindowOption(winTarget, "@spaces_worktree_base", win.worktree_base!),
    );
  }
}

/**
 * Recreates a saved `ResurrectSpaceSnapshot` through `MultiplexerDriver`,
 * restoring windows, pane splits, intermediate `tiled` rebalances (>2 panes),
 * final saved layouts, active pane/window selection, zoom state, worktree
 * options, and AI agent conversation resume.
 */
export async function restoreSpace(
  driver: MultiplexerDriver,
  snap: ResurrectSpaceSnapshot,
  opts: RestoreOptions = {},
): Promise<void> {
  if (!snap.name || snap.name.trim() === "") {
    throw new Error("resurrect: snapshot has empty name");
  }
  const isDir = opts.isDir ?? defaultIsDir;

  if (snap.windows.length === 0) {
    const cwd = resolveRestoreCwd(
      snap.cwd ?? "",
      snap.worktree_path ?? "",
      snap.main_repo_root ?? "",
      isDir,
    );
    await driver.newSessionDetached(snap.name, cwd, "");
    await restoreSessionWorktreeOptions(driver, snap);
    if (opts.switch) {
      await driver.switchToSpace(snap.name);
    }
    return;
  }

  const resolution = resolveAgentSessions(snap, opts.resolveSession, opts.resumePolicy);
  let activeWinTarget = "";

  for (let wi = 0; wi < snap.windows.length; wi += 1) {
    const win = snap.windows[wi]!;
    let winTarget = win.index > 0 ? `${snap.name}:${win.index}` : `${snap.name}:${wi + 1}`;
    const isActiveWin =
      win.active ||
      ((snap.active_window_index ?? 0) > 0 && win.index === snap.active_window_index);

    const firstPane: ResurrectPaneSnapshot =
      win.panes.length > 0 ? win.panes[0]! : { index: 0, active: false, cwd: snap.cwd };
    const firstKey = `${wi}:0`;
    const winWt = win.worktree_path || snap.worktree_path || "";
    const firstCwd = resolveRestoreCwd(
      firstPane.cwd || snap.cwd || "",
      winWt,
      snap.main_repo_root ?? "",
      isDir,
    );
    const firstCmd = restoredPaneCommand(firstPane, opts, firstKey, resolution);

    const created =
      wi === 0
        ? await driver.newSessionDetachedWithId(
            snap.name,
            firstCwd,
            win.name,
            win.width ?? 0,
            win.height ?? 0,
            firstCmd ?? undefined,
          )
        : await driver.newWindowDetachedWithId(
            snap.name,
            firstCwd,
            win.name,
            firstCmd ?? undefined,
          );

    if (wi === 0) {
      await restoreSessionWorktreeOptions(driver, snap);
    }
    if (created.windowId !== "") {
      winTarget = created.windowId;
    }
    await restoreWindowWorktreeOptions(driver, winTarget, win);

    if (isActiveWin) {
      activeWinTarget = winTarget;
    }

    const createdPaneIds: string[] = [created.paneId];
    let lastPaneId = created.paneId;

    for (let pi = 1; pi < win.panes.length; pi += 1) {
      const p = win.panes[pi]!;
      const pKey = `${wi}:${pi}`;
      const pCwd = resolveRestoreCwd(
        p.cwd || firstCwd,
        winWt,
        snap.main_repo_root ?? "",
        isDir,
      );
      const pCmd = restoredPaneCommand(p, opts, pKey, resolution);
      const splitTarget = lastPaneId !== "" ? lastPaneId : winTarget;
      const paneId = await driver.splitWindowWithId(splitTarget, pCwd, pCmd ?? undefined);
      createdPaneIds.push(paneId);
      if (paneId !== "") {
        lastPaneId = paneId;
      }
      // Rebalance intermediate splits when recreating 3+ panes so subsequent
      // splits never fail with "no space for new pane" before final layout.
      if (win.panes.length > 2 && pi < win.panes.length - 1) {
        await ignoreOptionalError(() => driver.selectLayout(winTarget, "tiled"));
      }
    }

    if (win.layout) {
      await ignoreOptionalError(() => driver.selectLayout(winTarget, win.layout!));
    }

    let activePaneTarget = "";
    for (let pi = 0; pi < win.panes.length; pi += 1) {
      const p = win.panes[pi]!;
      if (p.active) {
        const createdId = createdPaneIds[pi];
        if (createdId) {
          activePaneTarget = createdId;
        } else {
          const paneIdx = p.index > 0 ? p.index : pi + 1;
          activePaneTarget = `${winTarget}.${paneIdx}`;
        }
        await ignoreOptionalError(() => driver.selectPane(activePaneTarget));
        break;
      }
    }

    if (win.zoomed) {
      const zoomTarget = activePaneTarget !== "" ? activePaneTarget : winTarget;
      await ignoreOptionalError(() => driver.resizePaneZoom(zoomTarget));
    }
  }

  if (activeWinTarget !== "") {
    await ignoreOptionalError(() => driver.selectWindow(activeWinTarget));
  }
  if (opts.switch) {
    await driver.switchToSpace(snap.name);
  }
}

/**
 * Reports whether `snap` represents a fresh multiplexer server containing only
 * a single untouched empty shell session (1 window, 1 plain shell pane, no agent).
 */
export function isSingleEmptyShellSession(snap: DriverSnapshot): {
  isEmpty: boolean;
  sessionName: string;
} {
  if (snap.sessions.length !== 1) {
    return { isEmpty: false, sessionName: "" };
  }
  const sess = snap.sessions[0]!;
  const wins = snap.windows.filter((w) => w.sessionName === sess.name);
  if (wins.length !== 1) {
    return { isEmpty: false, sessionName: "" };
  }
  const panes = snap.panes.filter((p) => p.sessionName === sess.name);
  if (panes.length !== 1) {
    return { isEmpty: false, sessionName: "" };
  }
  const p = panes[0]!;
  if (p.spacesApp.trim() !== "" || canonicalAgent(p.currentCommand) !== "") {
    return { isEmpty: false, sessionName: "" };
  }
  const base = path.basename(p.currentCommand.trim()).replace(/^-/, "").toLowerCase();
  if (!BOOTSTRAP_SHELLS.has(base)) {
    return { isEmpty: false, sessionName: "" };
  }
  return { isEmpty: true, sessionName: sess.name };
}

async function readRestoredGuard(driver: MultiplexerDriver): Promise<string> {
  try {
    return (await driver.getGlobalOption(SPACES_RESTORED_OPTION)).trim();
  } catch {
    return "";
  }
}

/**
 * Reopens only the spaces recorded in `openState.spaces`, guarding concurrent
 * saves via `@spaces_restored = "pending" -> "done"`, cleaning up matching or
 * `"0"` bootstrap placeholders while leaving custom user sessions untouched, and
 * switching attached clients to `active_session` (or the first restored space).
 */
export async function autoRestore(
  driver: MultiplexerDriver,
  store: SpacesStore,
  opts: AutoRestoreOptions = {},
): Promise<AutoRestoreResult> {
  const res: AutoRestoreResult = {
    restored: [],
    skipped: [],
    activeSession: "",
    removedPlaceholder: false,
    errors: [],
  };

  if (!opts.force && (await readRestoredGuard(driver)) === "done") {
    return res;
  }

  await driver.setGlobalOption(SPACES_RESTORED_OPTION, "pending");
  try {
    const state = store.getOpenState();
    if (!state || state.spaces.length === 0) {
      return res;
    }

    const snap = await driver.snapshot();
    const wantSet = new Set(state.spaces);

    let placeholderName = "";
    let placeholderOrigName = "";
    const singleEmpty = isSingleEmptyShellSession(snap);
    if (singleEmpty.isEmpty) {
      if (wantSet.has(singleEmpty.sessionName)) {
        const tempName = `__spaces_bootstrap_${opts.pid ?? process.pid}`;
        try {
          await driver.renameSession(singleEmpty.sessionName, tempName);
          placeholderName = tempName;
          placeholderOrigName = singleEmpty.sessionName;
        } catch {
          // Leave existing session intact if rename fails.
        }
      } else if (singleEmpty.sessionName === "0") {
        placeholderName = "0";
        placeholderOrigName = "0";
      }
    }

    const running = new Set<string>();
    for (const s of snap.sessions) {
      if (placeholderName !== "" && s.name === placeholderOrigName) {
        continue;
      }
      running.add(s.name);
    }

    const restoreOpts: RestoreOptions = { ...opts, switch: false };
    const liveAfter = new Set<string>(running);

    for (const name of state.spaces) {
      if (running.has(name)) {
        res.skipped.push(name);
        continue;
      }
      const sp = store.getResurrectSpace(name);
      if (!sp) {
        res.errors.push(new Error(`load ${name}: snapshot not found`));
        continue;
      }
      try {
        await restoreSpace(driver, sp, restoreOpts);
        res.restored.push(name);
        liveAfter.add(name);
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        res.errors.push(new Error(`restore ${name}: ${message}`));
      }
    }

    if (res.restored.length === 0) {
      if (placeholderName !== "" && placeholderName !== placeholderOrigName) {
        await ignoreOptionalError(() =>
          driver.renameSession(placeholderName, placeholderOrigName),
        );
      }
      return res;
    }

    const target =
      state.active_session && liveAfter.has(state.active_session)
        ? state.active_session
        : res.restored[0]!;
    res.activeSession = target;

    const shouldSwitch = placeholderName !== "" || running.size === 0;
    let switched = false;
    const hasClients = await driver.hasClients();
    if (shouldSwitch && target !== "" && hasClients) {
      try {
        await driver.switchClient(target);
        switched = true;
      } catch {
        // Keep placeholder alive if switching attached client fails.
      }
    }

    if (placeholderName !== "" && (switched || !hasClients)) {
      try {
        await driver.killSession(placeholderName);
        res.removedPlaceholder = true;
      } catch {
        // Non-fatal placeholder cleanup failure.
      }
    }

    return res;
  } finally {
    await ignoreOptionalError(() => driver.setGlobalOption(SPACES_RESTORED_OPTION, "done"));
  }
}

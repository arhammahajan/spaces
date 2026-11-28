import { readdirSync, readFileSync } from "node:fs";
import * as path from "node:path";
import type { HookSnapshot, HookSummary } from "@spaces/types";
import { validateResumeCommand } from "../core/hmac";
import {
  commandName,
  defaultDetectionRegistry,
  isShellApp,
  isShellCommand,
} from "../detection/policies";
import type { DriverPane, DriverSession, DriverSnapshot, DriverWindow } from "../drivers/types";
import type {
  ResurrectOpenState,
  ResurrectPaneSnapshot,
  ResurrectSpaceSnapshot,
  ResurrectWindowSnapshot,
  SpacesStore,
  VerifiedResumeHintRecord,
} from "../store/db";

/** On-disk / SQLite schema version for resurrect snapshots. */
export const SNAPSHOT_VERSION = 1;

/** Subcommand used by harness resume launchers to carry preselected access level. */
export const RESUME_ACCESS_COMMAND = "__resume-access";

const SESSION_ID_FLAGS = ["--conversation", "--resume", "--session", "--id"] as const;
const HARNESS_BINARIES = new Set(["harness", "spaces", "tmux-spaces"]);
const MAX_PROC_WALK_DEPTH = 6;

/**
 * Tier 6 vendor conversation store lookup seam for resolving an unclaimed
 * session ID during restore.
 */
export type VendorSessionLookup = (
  agent: string,
  cwd: string,
  profile: string,
  title: string,
  exclude: ReadonlySet<string>,
) => { sessionId: string; storeAvailable: boolean };

/**
 * Agent-agnostic plugin policy seam injected into `resurrect` capture and restore.
 *
 * Agent-specific launch flags, positional resume syntax, duplicate-session
 * policies, live `/proc` discovery, and vendor store lookups live in each
 * agent's `HarnessPlugin` (not in shared `resurrect/`).
 */
export interface HarnessResumePolicy {
  binaryName?(agent: string): string;
  resumeArgs?(agent: string, sessionId: string): string[];
  positionalResume?(agent: string): boolean;
  uniqueExplicitSessions?(agent: string): boolean;
  discoverLiveSessionId?(agent: string, pid: number): string;
  resolveVendorSession?: VendorSessionLookup;
}

export interface HarnessLaunchParseResult {
  ok: boolean;
  level: string;
  args: string[];
}

export interface CaptureOptions {
  nowMs?: number;
  readChildCmdline?: (pid: number) => string[];
  discoverLiveSessionId?: (agent: string, pid: number) => string;
  discoverMainRepoRoot?: (worktreePath: string) => string;
  resumePolicy?: HarnessResumePolicy;
}

/**
 * Returns the canonical runtime agent ID if `name` identifies a known coding
 * harness, or `""` otherwise.
 */
export function canonicalAgent(name: string): string {
  return defaultDetectionRegistry().canonicalId(name);
}

/**
 * Scans `argv` for standard session ID flags (`--conversation`, `--resume`,
 * `--session`, `--id` in `--flag val` or `--flag=val` forms, skipping `"latest"`
 * and `"-*"`) and optional positional `resume <id>` (skipping `"--last"` and `"-*"`)
 * when enabled by the agent's `HarnessResumePolicy`.
 */
export function extractSessionIdFromArgs(
  agent: string,
  argv: string[],
  positionalResume = false,
): string {
  if (!Array.isArray(argv) || argv.length === 0) {
    return "";
  }
  void agent;

  for (let i = 0; i < argv.length; i += 1) {
    const tok = argv[i]!;
    for (const flag of SESSION_ID_FLAGS) {
      if (tok === flag && i + 1 < argv.length) {
        const val = argv[i + 1]!.trim();
        if (val !== "" && !val.startsWith("-") && val !== "latest") {
          return val;
        }
      }
      const prefix = `${flag}=`;
      if (tok.startsWith(prefix)) {
        const val = tok.slice(prefix.length).trim();
        if (val !== "" && !val.startsWith("-") && val !== "latest") {
          return val;
        }
      }
    }
    if (positionalResume && tok === "resume" && i + 1 < argv.length) {
      const val = argv[i + 1]!.trim();
      if (val !== "" && !val.startsWith("-") && val !== "--last") {
        return val;
      }
    }
  }
  return "";
}

/**
 * Recognizes `spaces`, `tmux-spaces`, `harness`, and `__resume-access` launcher
 * commands while leaving direct native agent commands alone.
 */
export function harnessLaunchArgs(argv: string[]): HarnessLaunchParseResult {
  if (!Array.isArray(argv) || argv.length < 2) {
    return { ok: false, level: "", args: [] };
  }
  if (argv[1] === RESUME_ACCESS_COMMAND && argv.length >= 4) {
    return { ok: true, level: argv[2]!, args: argv.slice(3) };
  }
  const base = path.basename(argv[0]!).toLowerCase();
  if (HARNESS_BINARIES.has(base)) {
    const sub = argv[1]!;
    if (sub === "profile" || sub === "launch" || canonicalAgent(sub) !== "") {
      return { ok: true, level: "", args: argv.slice(1) };
    }
  }
  return { ok: false, level: "", args: [] };
}

/**
 * Builds the `__resume-access` launcher command prefix for resurrect replay.
 */
export function harnessResumeCommand(
  selfPath: string,
  level: string,
  ...args: string[]
): string[] {
  return [selfPath, RESUME_ACCESS_COMMAND, level, ...args];
}

/**
 * Builds `<canonicalAgent> profile <profile> [...args]` for profiled launches.
 */
export function harnessProfileCommand(
  name: string,
  profile: string,
  ...args: string[]
): string[] {
  const canonical = canonicalAgent(name) || name;
  const resolvedProfile = profile.trim() === "" ? "default" : profile;
  return [canonical, "profile", resolvedProfile, ...args];
}

/**
 * Extracts the profile name from a `spaces`, `tmux-spaces`, `harness`, or
 * `__resume-access` launcher command line.
 */
export function harnessProfileFromArgv(argv: string[]): string {
  if (!Array.isArray(argv) || argv.length < 2) {
    return "";
  }
  const base = path.basename(argv[0]!).toLowerCase();
  if (!HARNESS_BINARIES.has(base) && argv[1] !== RESUME_ACCESS_COMMAND) {
    return "";
  }
  for (let i = 1; i < argv.length; i += 1) {
    const tok = argv[i]!;
    if ((tok === "profile" || tok === "--profile") && i + 1 < argv.length) {
      const prof = argv[i + 1]!.trim();
      if (prof !== "" && !prof.startsWith("-")) {
        return prof;
      }
    }
    if (tok.startsWith("--profile=")) {
      const prof = tok.slice("--profile=".length).trim();
      if (prof !== "") {
        return prof;
      }
    }
    if (tok === "__shim-profile" && i + 1 < argv.length) {
      const raw = argv[i + 1]!;
      const slashIdx = raw.indexOf("/");
      if (slashIdx >= 0) {
        return raw.slice(slashIdx + 1).trim();
      }
    }
  }
  return "";
}

function readTaskChildren(pid: number, procRoot: string): number[] {
  const taskDir = path.join(procRoot, String(pid), "task");
  let taskEntries: string[];
  try {
    taskEntries = readdirSync(taskDir).sort();
  } catch {
    return [];
  }
  const children: number[] = [];
  for (const tid of taskEntries) {
    let raw: string;
    try {
      raw = readFileSync(path.join(taskDir, tid, "children"), "utf8");
    } catch {
      continue;
    }
    for (const token of raw.trim().split(/\s+/)) {
      if (!token) {
        continue;
      }
      const parsed = Number.parseInt(token, 10);
      if (Number.isInteger(parsed) && parsed > 0) {
        children.push(parsed);
      }
    }
  }
  return children;
}

function firstChildPid(pid: number, procRoot: string): number {
  const children = readTaskChildren(pid, procRoot);
  return children.length > 0 ? children[0]! : 0;
}

function readProcCmdline(pid: number, procRoot: string): string[] {
  let buf: Buffer;
  try {
    buf = readFileSync(path.join(procRoot, String(pid), "cmdline"));
  } catch {
    return [];
  }
  if (buf.length === 0) {
    return [];
  }
  let end = buf.length;
  while (end > 0 && buf[end - 1] === 0) {
    end -= 1;
  }
  if (end === 0) {
    return [];
  }
  return buf
    .subarray(0, end)
    .toString("utf8")
    .split("\0")
    .filter((part) => part !== "");
}

/**
 * Walks `/proc/<pid>/task/*\/children` and `/proc/<pid>/cmdline` up to depth 6,
 * stopping early on `harnessLaunchArgs` or a known agent binary while keeping
 * the deepest non-empty command line otherwise.
 */
export function readChildCmdline(pid: number, procRoot = "/proc"): string[] {
  if (!Number.isInteger(pid) || pid <= 0) {
    return [];
  }
  let cur = pid;
  let best: string[] = [];
  for (let depth = 0; depth < MAX_PROC_WALK_DEPTH; depth += 1) {
    const child = firstChildPid(cur, procRoot);
    if (child <= 0 || child === cur) {
      break;
    }
    const argv = readProcCmdline(child, procRoot);
    if (argv.length > 0) {
      best = argv;
      if (harnessLaunchArgs(argv).ok) {
        return argv;
      }
      const base = path.basename(argv[0]!);
      if (canonicalAgent(base) !== "") {
        return argv;
      }
    }
    cur = child;
  }
  return best;
}

function shimTargetAgent(argv: string[]): string {
  if (argv.length < 3) {
    return "";
  }
  const base = path.basename(argv[0]!).toLowerCase();
  if (!HARNESS_BINARIES.has(base) || argv[1] !== "__shim") {
    return "";
  }
  for (let i = 2; i < argv.length; i += 1) {
    const tok = argv[i]!;
    if (tok === "--") {
      return i + 1 < argv.length ? canonicalAgent(argv[i + 1]!) : "";
    }
    if (tok === "--hold") {
      continue;
    }
    if (tok === "--profile") {
      i += 1;
      continue;
    }
    if (tok.startsWith("--profile=") || tok.startsWith("-")) {
      continue;
    }
    return canonicalAgent(tok);
  }
  return "";
}

function isHarnessOrAgentArgv(argv: string[]): boolean {
  if (argv.length === 0) {
    return false;
  }
  if (harnessLaunchArgs(argv).ok) {
    return true;
  }
  if (shimTargetAgent(argv) !== "") {
    return true;
  }
  return canonicalAgent(argv[0] ?? "") !== "";
}

function agentFromArgv(argv: string[]): string {
  if (argv.length === 0) {
    return "";
  }
  const direct = canonicalAgent(argv[0]!);
  if (direct !== "") {
    return direct;
  }
  const launch = harnessLaunchArgs(argv);
  if (launch.ok && launch.args.length > 0) {
    return canonicalAgent(launch.args[0]!);
  }
  return shimTargetAgent(argv);
}

function lookupHookSummary(
  hooks: HookSnapshot | null,
  sessionName: string,
  pane: DriverPane,
): HookSummary | null {
  const sessionPanes = hooks?.panes?.[sessionName];
  if (!sessionPanes) {
    return null;
  }
  return sessionPanes[String(pane.numericId)] ?? (pane.id ? sessionPanes[pane.id] : undefined) ?? null;
}

function capturePane(
  pane: DriverPane,
  hook: HookSummary | null,
  store: SpacesStore | null,
  opts: CaptureOptions,
): ResurrectPaneSnapshot {
  const readCmd = opts.readChildCmdline ?? readChildCmdline;
  const discoverLive =
    opts.discoverLiveSessionId ??
    opts.resumePolicy?.discoverLiveSessionId ??
    (() => "");

  const procCmdline = pane.pid > 0 ? readCmd(pane.pid) : [];
  const cmdBase = commandName(pane.currentCommand);
  const procHasAgent = isHarnessOrAgentArgv(procCmdline);
  const returnedToShell =
    isShellApp(pane.spacesApp) || (isShellCommand(cmdBase) && !procHasAgent);
  const spacesSourceLower = pane.spacesSource.trim().toLowerCase();
  const staleAgentReturnedToShell =
    isShellApp(pane.spacesApp) ||
    (isShellCommand(cmdBase) &&
      !procHasAgent &&
      (spacesSourceLower === "osc7501" ||
        spacesSourceLower === "hook" ||
        (pane.pid > 0 && defaultDetectionRegistry().isKnownAgent(pane.spacesApp))));

  // Tier 1: MAC-verified OSC 7577 resume hint (skipped if pane returned to an interactive shell).
  const hint: VerifiedResumeHintRecord | null =
    !returnedToShell && store && pane.id ? store.getVerifiedResumeHint(pane.id) : null;

  // Determine canonical AI agent from highest-confidence source first.
  const agentCandidates: string[] = [
    hint?.agent ?? "",
    staleAgentReturnedToShell ? "" : pane.spacesApp,
    !returnedToShell && hook ? hook.agent || hook.app : "",
    hint?.resume_cmd?.length ? agentFromArgv(hint.resume_cmd) : "",
    procCmdline.length > 0 ? agentFromArgv(procCmdline) : "",
    pane.currentCommand,
  ];
  let agent = "";
  for (const cand of agentCandidates) {
    const canon = canonicalAgent(cand);
    if (canon !== "") {
      agent = canon;
      break;
    }
  }

  // Validate Tier 1 OSC 7577 resume_cmd before allowing it to override /proc or @spaces_launch_cmd.
  let verifiedFullCommand: string[] | null = null;
  if (hint?.resume_cmd && hint.resume_cmd.length > 0) {
    const checked = validateResumeCommand({
      agent: agent || undefined,
      argv: hint.resume_cmd,
    });
    if (checked.ok) {
      verifiedFullCommand = checked.argv;
    }
  }

  let fullCommand: string[] =
    verifiedFullCommand !== null ? verifiedFullCommand : procCmdline.slice();

  // Tiers 1–5 session ID resolution.
  let sessionId = "";
  const hintSid = hint?.session_id?.trim() ?? "";
  const paneResumeSid = staleAgentReturnedToShell ? "" : pane.resumeSession.trim();
  const hookSid = !returnedToShell && hook?.session_id ? hook.session_id.trim() : "";

  if (hintSid !== "") {
    sessionId = hintSid;
  } else if (paneResumeSid !== "") {
    sessionId = paneResumeSid;
  } else if (hookSid !== "") {
    sessionId = hookSid;
  } else if (agent !== "") {
    const positional = opts.resumePolicy?.positionalResume?.(agent) ?? false;
    const flagSid = extractSessionIdFromArgs(agent, fullCommand, positional);
    if (flagSid !== "") {
      sessionId = flagSid;
    } else {
      const liveSid = discoverLive(agent, pane.pid).trim();
      if (liveSid !== "") {
        sessionId = liveSid;
      }
    }
  }

  // @spaces_launch_cmd enriches full_command when no verified OSC 7577 resume_cmd won.
  let launchOverrideProfile = "";
  if (verifiedFullCommand === null && pane.launchCommand.length > 0) {
    const parsedLaunch = harnessLaunchArgs(pane.launchCommand);
    if (
      parsedLaunch.ok &&
      parsedLaunch.level !== "" &&
      agent !== "" &&
      canonicalAgent(parsedLaunch.args[0] ?? "") === agent
    ) {
      fullCommand = pane.launchCommand.slice();
      launchOverrideProfile = harnessProfileFromArgv(pane.launchCommand);
    }
  }

  const hintProfile = hint?.profile?.trim() ?? "";
  const paneProfile = staleAgentReturnedToShell ? "" : pane.profile.trim();
  const argvProfile = harnessProfileFromArgv(fullCommand);
  const profile = hintProfile || paneProfile || launchOverrideProfile || argvProfile;

  const cwd = pane.currentPath || hint?.cwd?.trim() || "";
  const title = pane.spacesTitle.trim() || pane.title;
  let app = staleAgentReturnedToShell ? "" : pane.spacesApp;
  if (app === "" && agent !== "") {
    app = agent;
  }

  const out: ResurrectPaneSnapshot = {
    index: pane.paneIndex,
    active: pane.active,
  };
  if (pane.id) out.id = pane.id;
  if (cwd) out.cwd = cwd;
  if (pane.currentCommand) out.command = pane.currentCommand;
  if (app) out.app = app;
  if (title) out.title = title;
  if (agent) out.agent = agent;
  if (sessionId) out.session_id = sessionId;
  if (profile) out.profile = profile;
  if (fullCommand.length > 0) out.full_command = fullCommand;
  return out;
}

/**
 * Converts a live `DriverSnapshot` plus optional hook store and `SpacesStore`
 * (for MAC-verified OSC 7577 resume hints) into per-space snapshots ready for persistence.
 */
export function captureSpaces(
  snap: DriverSnapshot,
  hooks: HookSnapshot | null,
  store: SpacesStore | null,
  opts: CaptureOptions = {},
): ResurrectSpaceSnapshot[] {
  const winBySession = new Map<string, DriverWindow[]>();
  for (const w of snap.windows) {
    const list = winBySession.get(w.sessionName);
    if (list) {
      list.push(w);
    } else {
      winBySession.set(w.sessionName, [w]);
    }
  }

  const panesByWin = new Map<string, DriverPane[]>();
  for (const p of snap.panes) {
    const key = `${p.sessionName}\0${p.windowId}`;
    const list = panesByWin.get(key);
    if (list) {
      list.push(p);
    } else {
      panesByWin.set(key, [p]);
    }
  }

  const nowMs = opts.nowMs ?? Date.now();
  const out: ResurrectSpaceSnapshot[] = [];

  for (const sess of snap.sessions) {
    const wins = (winBySession.get(sess.name) ?? []).slice().sort((a, b) => a.index - b.index);
    const spaceSnap: ResurrectSpaceSnapshot = {
      version: SNAPSHOT_VERSION,
      name: sess.name,
      saved_at_ms: nowMs,
      active_window_index: 0,
      windows: [],
    };
    if (sess.worktreePath) spaceSnap.worktree_path = sess.worktreePath;
    if (sess.worktreeName) spaceSnap.worktree_name = sess.worktreeName;
    if (sess.worktreeBase) spaceSnap.worktree_base = sess.worktreeBase;
    if (sess.worktreePath && opts.discoverMainRepoRoot) {
      const mainRoot = opts.discoverMainRepoRoot(sess.worktreePath).trim();
      if (mainRoot) {
        spaceSnap.main_repo_root = mainRoot;
      }
    }

    let firstCwd = "";
    for (const w of wins) {
      if (w.active) {
        spaceSnap.active_window_index = w.index;
      }
      const rawPanes = (panesByWin.get(`${sess.name}\0${w.id}`) ?? []).slice().sort((a, b) => {
        if (a.paneIndex !== b.paneIndex) {
          return a.paneIndex - b.paneIndex;
        }
        return a.numericId - b.numericId;
      });

      const ws: ResurrectWindowSnapshot = {
        index: w.index,
        name: w.name,
        active: w.active,
        panes: [],
      };
      if (w.layout) ws.layout = w.layout;
      if (w.zoomed) ws.zoomed = true;
      if (w.width > 0) ws.width = w.width;
      if (w.height > 0) ws.height = w.height;
      if (w.worktreePath) ws.worktree_path = w.worktreePath;
      if (w.worktreeName) ws.worktree_name = w.worktreeName;
      if (w.worktreeBase) ws.worktree_base = w.worktreeBase;

      for (const p of rawPanes) {
        const hookRec = lookupHookSummary(hooks, sess.name, p);
        const ps = capturePane(p, hookRec, store, opts);
        if (ps.cwd) {
          if (firstCwd === "") {
            firstCwd = ps.cwd;
          }
          if (p.active && w.active) {
            spaceSnap.cwd = ps.cwd;
          }
        }
        ws.panes.push(ps);
      }

      spaceSnap.windows.push(ws);
    }

    if (!spaceSnap.cwd && firstCwd !== "") {
      spaceSnap.cwd = firstCwd;
    }

    out.push(spaceSnap);
  }

  return out;
}

/**
 * Records the ordered list of currently running spaces and the last active
 * space from a live `DriverSnapshot`.
 */
export function captureOpenState(
  snap: DriverSnapshot,
  nowMs: number = Date.now(),
): ResurrectOpenState {
  const sessions: DriverSession[] = snap.sessions.slice().sort((a, b) => {
    if (a.createdAtSec !== b.createdAtSec) {
      return a.createdAtSec - b.createdAtSec;
    }
    if (a.id !== b.id) {
      return a.id < b.id ? -1 : 1;
    }
    if (a.name !== b.name) {
      return a.name < b.name ? -1 : 1;
    }
    return 0;
  });

  const spaces: string[] = [];
  const inSet = new Set<string>();
  let firstAttached = "";

  for (const s of sessions) {
    if (s.name.trim() === "") {
      continue;
    }
    spaces.push(s.name);
    inSet.add(s.name);
    if (firstAttached === "" && s.attached > 0) {
      firstAttached = s.name;
    }
  }

  let active = snap.currentSession.trim();
  if (active === "" || !inSet.has(active)) {
    active = firstAttached;
  }
  if (active === "" && spaces.length > 0) {
    active = spaces[0]!;
  }

  return {
    version: SNAPSHOT_VERSION,
    saved_at_ms: nowMs,
    active_session: active,
    spaces,
  };
}

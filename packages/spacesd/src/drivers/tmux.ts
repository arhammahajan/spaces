import {
  MultiplexerNoServerError,
  type DriverPane,
  type DriverSession,
  type DriverSnapshot,
  type DriverWindow,
  type MultiplexerDriver,
} from "./types";

/**
 * ASCII unit separator used in tmux `-F` format strings so spaces and tabs
 * inside titles, paths, and messages never break field parsing.
 */
export const UNIT_SEP = "\x1f";

/**
 * Exact 9-field `\x1f`-delimited format string for `tmux list-sessions -F`.
 */
export const SESSION_LIST_FORMAT =
  "#{session_id}\x1f#{session_name}\x1f" +
  "#{session_attached}\x1f#{session_created}\x1f#{session_windows}\x1f" +
  "#{@spaces_worktree_path}\x1f#{@spaces_worktree_name}\x1f" +
  "#{@spaces_worktree_base}\x1f#{@spaces_worktree_managed}";

/**
 * Exact 18-field `\x1f`-delimited format string for `tmux list-windows -a -F`.
 */
export const WINDOW_LIST_FORMAT =
  "#{session_name}\x1f#{window_id}\x1f#{window_index}\x1f" +
  "#{window_name}\x1f#{window_active}\x1f#{window_bell_flag}\x1f" +
  "#{window_activity_flag}\x1f#{window_silence_flag}\x1f#{window_layout}\x1f" +
  "#{window_last_flag}\x1f#{window_zoomed_flag}\x1f#{window_width}\x1f" +
  "#{window_height}\x1f#{window_activity}\x1f" +
  "#{@spaces_worktree_path}\x1f#{@spaces_worktree_name}\x1f" +
  "#{@spaces_worktree_base}\x1f#{@spaces_worktree_managed}";

/**
 * Exact 32-field `\x1f`-delimited format string for `tmux list-panes -a -F`.
 */
export const PANE_LIST_FORMAT =
  "#{session_name}\x1f#{window_id}\x1f#{window_index}\x1f" +
  "#{window_name}\x1f#{pane_id}\x1f#{pane_title}\x1f#{pane_current_command}\x1f" +
  "#{pane_current_path}\x1f#{pane_pid}\x1f#{pane_active}\x1f#{window_active}\x1f" +
  "#{pane_dead}\x1f#{window_bell_flag}\x1f#{window_activity_flag}\x1f" +
  "#{window_silence_flag}\x1f#{@spaces_state}\x1f#{@spaces_app}\x1f" +
  "#{@spaces_kind}\x1f#{@spaces_msg}\x1f#{@spaces_ts}\x1f" +
  "#{pane_index}\x1f#{@spaces_resume_session}\x1f#{@spaces_profile}\x1f#{@spaces_source}\x1f#{@spaces_title}\x1f" +
  "#{pane_width}\x1f#{pane_height}\x1f#{pane_in_mode}\x1f#{pane_synchronized}\x1f#{pane_start_command}\x1f#{pane_dead_status}\x1f#{@spaces_launch_cmd}";

/**
 * Native per-pane tmux user options managed by `setSpacesStatus`.
 */
export const SPACES_STATUS_OPTIONS = [
  "@spaces_state",
  "@spaces_app",
  "@spaces_kind",
  "@spaces_msg",
  "@spaces_ts",
] as const;

const SESSION_MIN_FIELDS = 5;
const WINDOW_MIN_FIELDS = 8;
const PANE_MIN_FIELDS = 20;

const NO_SERVER_NEEDLES = [
  "no server running",
  "no server found",
  "failed to connect to server",
  "can't find session",
  "couldn't find session",
  "no current session",
  "no sessions",
] as const;

/**
 * Result returned by a `TmuxRunner` invocation.
 */
export interface TmuxRunResult {
  stdout: string;
  stderr: string;
  exitCode: number;
  error?: Error;
}

/**
 * Seam for executing tmux CLI invocations (real `BunTmuxRunner` or test fake).
 */
export interface TmuxRunner {
  run(args: string[]): Promise<TmuxRunResult>;
}

/**
 * Options for configuring `BunTmuxRunner`.
 */
export interface BunTmuxRunnerOptions {
  bin?: string;
  env?: Record<string, string | undefined>;
}

/**
 * `TmuxRunner` backed by `Bun.spawn`, resolving `$TMUX_BIN || "tmux"`.
 */
export class BunTmuxRunner implements TmuxRunner {
  private readonly explicitBin?: string;
  private readonly env?: Record<string, string | undefined>;

  constructor(binOrOptions?: string | BunTmuxRunnerOptions) {
    if (typeof binOrOptions === "string") {
      this.explicitBin = binOrOptions;
    } else if (binOrOptions) {
      this.explicitBin = binOrOptions.bin;
      this.env = binOrOptions.env;
    }
  }

  /**
   * Resolved tmux binary path (`explicitBin || $TMUX_BIN || "tmux"`).
   */
  get bin(): string {
    if (this.explicitBin && this.explicitBin.length > 0) {
      return this.explicitBin;
    }
    const fromEnv = (this.env ?? process.env).TMUX_BIN;
    return fromEnv && fromEnv.length > 0 ? fromEnv : "tmux";
  }

  async run(args: string[]): Promise<TmuxRunResult> {
    try {
      const proc = Bun.spawn([this.bin, ...args], {
        stdin: "ignore",
        stdout: "pipe",
        stderr: "pipe",
      });
      const [stdout, stderr, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
      ]);
      if (exitCode !== 0) {
        return {
          stdout,
          stderr,
          exitCode,
          error: new Error(`exit status ${exitCode}`),
        };
      }
      return { stdout, stderr, exitCode };
    } catch (err) {
      return {
        stdout: "",
        stderr: "",
        exitCode: -1,
        error: err instanceof Error ? err : new Error(String(err)),
      };
    }
  }
}

/**
 * Reports whether tmux stderr/error text indicates no tmux server (or target
 * session) is present.
 */
export function isNoServerError(stderr: string, errorMessage?: string): boolean {
  const hay = `${stderr}\x00${errorMessage ?? ""}`.toLowerCase();
  return NO_SERVER_NEEDLES.some((needle) => hay.includes(needle));
}

/**
 * Decorates a failed tmux command invocation as an `Error` (or
 * `MultiplexerNoServerError` when no tmux server/session exists).
 */
export function commandError(
  args: string[],
  stderr: string,
  exitCode: number,
  error?: Error,
): Error {
  const detail = stderr.trim();
  const base =
    detail !== ""
      ? `tmux ${args.join(" ")} (exit ${exitCode}): ${detail}`
      : `tmux ${args.join(" ")} (exit ${exitCode})`;
  const message = error?.message ? `${base}: ${error.message}` : base;

  if (error instanceof MultiplexerNoServerError || isNoServerError(stderr, error?.message)) {
    return new MultiplexerNoServerError(message);
  }
  if (error) {
    return new Error(message, { cause: error });
  }
  return new Error(base);
}

/**
 * Converts a tmux pane id like `"%12"` to `12`, returning `-1` for malformed or
 * negative values.
 */
export function parseNumericPaneId(raw: string): number {
  let s = raw.trim();
  if (s.startsWith("%")) {
    s = s.slice(1);
  }
  if (!/^\d+$/.test(s)) {
    return -1;
  }
  const n = Number(s);
  return Number.isSafeInteger(n) && n >= 0 ? n : -1;
}

function splitOutputLines(stdout: string): string[] {
  const lines: string[] = [];
  for (const rawLine of stdout.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line !== "") {
      lines.push(line);
    }
  }
  return lines;
}

function parseTmuxBool(raw: string): boolean {
  return raw.trim() === "1";
}

function parseTmuxInt(raw: string): number {
  const trimmed = raw.trim();
  if (!/^[+-]?\d+$/.test(trimmed)) {
    return 0;
  }
  const n = Number(trimmed);
  if (!Number.isSafeInteger(n) || n < 0) {
    return 0;
  }
  return n;
}

function spacesValue(raw: string): string {
  return raw === "" || raw === "-" ? "" : raw;
}

function parseLaunchCommand(raw: string): string[] {
  if (raw === "") {
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    if (Array.isArray(parsed) && parsed.every((item): item is string => typeof item === "string")) {
      return parsed;
    }
  } catch {
    // Ignore invalid @spaces_launch_cmd JSON payloads.
  }
  return [];
}

function parseSessionRecord(line: string): DriverSession | null {
  const fields = line.split(UNIT_SEP);
  if (fields.length < SESSION_MIN_FIELDS) {
    return null;
  }
  return {
    id: fields[0]!,
    name: fields[1]!,
    attached: parseTmuxInt(fields[2]!),
    createdAtSec: parseTmuxInt(fields[3]!),
    windowCount: parseTmuxInt(fields[4]!),
    worktreePath: fields.length > 5 ? spacesValue(fields[5]!) : "",
    worktreeName: fields.length > 6 ? spacesValue(fields[6]!) : "",
    worktreeBase: fields.length > 7 ? spacesValue(fields[7]!) : "",
    worktreeManaged: fields.length > 8 ? parseTmuxBool(fields[8]!) : false,
  };
}

function parseWindowRecord(line: string): DriverWindow | null {
  const fields = line.split(UNIT_SEP);
  if (fields.length < WINDOW_MIN_FIELDS) {
    return null;
  }
  return {
    sessionName: fields[0]!,
    id: fields[1]!,
    index: parseTmuxInt(fields[2]!),
    name: fields[3]!,
    active: parseTmuxBool(fields[4]!),
    bell: parseTmuxBool(fields[5]!),
    activity: parseTmuxBool(fields[6]!),
    silence: parseTmuxBool(fields[7]!),
    layout: fields.length > 8 ? fields[8]! : "",
    last: fields.length > 9 ? parseTmuxBool(fields[9]!) : false,
    zoomed: fields.length > 10 ? parseTmuxBool(fields[10]!) : false,
    width: fields.length > 11 ? parseTmuxInt(fields[11]!) : 0,
    height: fields.length > 12 ? parseTmuxInt(fields[12]!) : 0,
    activityAtSec: fields.length > 13 ? parseTmuxInt(fields[13]!) : 0,
    worktreePath: fields.length > 14 ? spacesValue(fields[14]!) : "",
    worktreeName: fields.length > 15 ? spacesValue(fields[15]!) : "",
    worktreeBase: fields.length > 16 ? spacesValue(fields[16]!) : "",
    worktreeManaged: fields.length > 17 ? parseTmuxBool(fields[17]!) : false,
  };
}

function parsePaneRecord(line: string): DriverPane | null {
  const fields = line.split(UNIT_SEP);
  if (fields.length < PANE_MIN_FIELDS) {
    return null;
  }
  const id = fields[4]!;
  return {
    sessionName: fields[0]!,
    windowId: fields[1]!,
    windowIndex: parseTmuxInt(fields[2]!),
    windowName: fields[3]!,
    id,
    numericId: parseNumericPaneId(id),
    title: fields[5]!,
    currentCommand: fields[6]!,
    currentPath: fields[7]!,
    pid: parseTmuxInt(fields[8]!),
    active: parseTmuxBool(fields[9]!),
    windowActive: parseTmuxBool(fields[10]!),
    dead: parseTmuxBool(fields[11]!),
    bell: parseTmuxBool(fields[12]!),
    activity: parseTmuxBool(fields[13]!),
    silence: parseTmuxBool(fields[14]!),
    spacesState: spacesValue(fields[15]!),
    spacesApp: spacesValue(fields[16]!),
    spacesKind: spacesValue(fields[17]!),
    spacesMsg: spacesValue(fields[18]!),
    spacesTsMs: parseTmuxInt(fields[19]!),
    paneIndex: fields.length > 20 ? parseTmuxInt(fields[20]!) : 0,
    resumeSession: fields.length > 21 ? spacesValue(fields[21]!) : "",
    profile: fields.length > 22 ? spacesValue(fields[22]!) : "",
    spacesSource: fields.length > 23 ? spacesValue(fields[23]!) : "",
    spacesTitle: fields.length > 24 ? spacesValue(fields[24]!) : "",
    width: fields.length > 25 ? parseTmuxInt(fields[25]!) : 0,
    height: fields.length > 26 ? parseTmuxInt(fields[26]!) : 0,
    inMode: fields.length > 27 ? parseTmuxBool(fields[27]!) : false,
    synchronized: fields.length > 28 ? parseTmuxBool(fields[28]!) : false,
    startCommand: fields.length > 29 ? fields[29]!.trim() : "",
    deadStatus: fields.length > 30 ? parseTmuxInt(fields[30]!) : 0,
    launchCommand: fields.length > 31 ? parseLaunchCommand(fields[31]!) : [],
  };
}

/**
 * Parses `tmux list-sessions -F` output into `DriverSession` entries.
 */
export function parseSessionsOutput(stdout: string): DriverSession[] {
  const sessions: DriverSession[] = [];
  for (const line of splitOutputLines(stdout)) {
    const parsed = parseSessionRecord(line);
    if (parsed) {
      sessions.push(parsed);
    }
  }
  return sessions;
}

/**
 * Parses `tmux list-windows -a -F` output into `DriverWindow` entries.
 */
export function parseWindowsOutput(stdout: string): DriverWindow[] {
  const windows: DriverWindow[] = [];
  for (const line of splitOutputLines(stdout)) {
    const parsed = parseWindowRecord(line);
    if (parsed) {
      windows.push(parsed);
    }
  }
  return windows;
}

/**
 * Parses `tmux list-panes -a -F` output into `DriverPane` entries.
 */
export function parsePanesOutput(stdout: string): DriverPane[] {
  const panes: DriverPane[] = [];
  for (const line of splitOutputLines(stdout)) {
    const parsed = parsePaneRecord(line);
    if (parsed) {
      panes.push(parsed);
    }
  }
  return panes;
}

function normalizeOption(option: string): string {
  return option.startsWith("@") ? option : `@${option}`;
}

function buildNewWindowArgs(
  prefixArgs: string[],
  session: string,
  cwd: string,
  name: string,
  command: string[],
): string[] {
  const args = [...prefixArgs];
  if (session !== "") {
    args.push("-t", `${session}:`);
  }
  if (cwd !== "") {
    args.push("-c", cwd);
  }
  if (name !== "") {
    args.push("-n", name);
  }
  if (command.length > 0) {
    args.push("--", ...command);
  }
  return args;
}

function buildNewSessionDetachedArgs(
  name: string,
  cwd: string,
  windowName: string,
  width = 0,
  height = 0,
  formatSuffix: string[] = [],
  command: string[] = [],
): string[] {
  const args = ["new-session", "-d", "-s", name];
  if (cwd !== "") {
    args.push("-c", cwd);
  }
  if (windowName !== "") {
    args.push("-n", windowName);
  }
  if (width > 0) {
    args.push("-x", String(width));
  }
  if (height > 0) {
    args.push("-y", String(height));
  }
  if (formatSuffix.length > 0) {
    args.push(...formatSuffix);
  }
  if (command.length > 0) {
    args.push("--", ...command);
  }
  return args;
}

function buildSplitWindowArgs(
  prefixArgs: string[],
  cwd: string,
  command: string[],
): string[] {
  const args = [...prefixArgs];
  if (cwd !== "") {
    args.push("-c", cwd);
  }
  if (command.length > 0) {
    args.push("--", ...command);
  }
  return args;
}

function parseWindowPaneIdPair(stdout: string): { windowId: string; paneId: string } {
  const trimmed = stdout.trim();
  const parts = trimmed.split(UNIT_SEP);
  if (parts.length === 2) {
    return { windowId: parts[0]!, paneId: parts[1]! };
  }
  return { windowId: trimmed, paneId: "" };
}

/**
 * Configuration options for `TmuxDriver`.
 */
export interface TmuxDriverOptions {
  runner?: TmuxRunner;
  env?: Record<string, string | undefined>;
}

/**
 * Options for `TmuxDriver.displayPopup`.
 */
export interface TmuxPopupOptions {
  width?: string;
  height?: string;
  title?: string;
  border?: string;
  close?: boolean;
  cwd?: string;
  command?: string[];
}

/**
 * `MultiplexerDriver` implementation backed by the tmux CLI.
 */
export class TmuxDriver implements MultiplexerDriver {
  readonly kind = "tmux" as const;
  private readonly runner: TmuxRunner;
  private readonly env?: Record<string, string | undefined>;

  constructor(options: TmuxDriverOptions = {}) {
    this.env = options.env;
    this.runner = options.runner ?? new BunTmuxRunner({ env: options.env });
  }

  private getenv(key: string): string {
    return (this.env ?? process.env)[key] ?? "";
  }

  private async run(args: string[]): Promise<string> {
    const res = await this.runner.run(args);
    if (res.error || res.exitCode !== 0) {
      throw commandError(args, res.stderr, res.exitCode, res.error);
    }
    return res.stdout;
  }

  private async setScopedOption(
    scopeFlags: string[],
    target: string,
    option: string,
    value: string,
  ): Promise<void> {
    const args = ["set-option", ...scopeFlags];
    if (target !== "") {
      args.push("-t", target);
    }
    args.push(normalizeOption(option), value);
    await this.run(args);
  }

  /**
   * Reports whether this process runs inside a tmux client (`$TMUX` set).
   */
  insideTmux(): boolean {
    return this.getenv("TMUX") !== "";
  }

  /**
   * Returns `$TMUX_PANE` or `""` when not running inside a tmux pane.
   */
  currentPaneId(): string {
    return this.getenv("TMUX_PANE");
  }

  /**
   * Parses a tmux pane id like `"%12"` to its numeric form (`-1` if invalid).
   */
  parseNumericPaneId(id: string): number {
    return parseNumericPaneId(id);
  }

  async serverRunning(): Promise<boolean> {
    const res = await this.runner.run(["has-session"]);
    return !res.error && res.exitCode === 0;
  }

  async currentSession(): Promise<string> {
    const out = await this.run(["display-message", "-p", "#{session_name}"]);
    const name = out.trim();
    if (name === "") {
      throw new MultiplexerNoServerError("tmux display-message: no current session");
    }
    return name;
  }

  async hasClients(): Promise<boolean> {
    try {
      const out = await this.run(["list-clients", "-F", "#{client_name}"]);
      return out.trim() !== "";
    } catch {
      return false;
    }
  }

  async listSessions(): Promise<DriverSession[]> {
    const out = await this.run(["list-sessions", "-F", SESSION_LIST_FORMAT]);
    return parseSessionsOutput(out);
  }

  async listWindows(): Promise<DriverWindow[]> {
    const out = await this.run(["list-windows", "-a", "-F", WINDOW_LIST_FORMAT]);
    return parseWindowsOutput(out);
  }

  async listPanes(): Promise<DriverPane[]> {
    const out = await this.run(["list-panes", "-a", "-F", PANE_LIST_FORMAT]);
    return parsePanesOutput(out);
  }

  async snapshot(): Promise<DriverSnapshot> {
    const [sessionsRes, windowsRes, panesRes, currentRes] = await Promise.allSettled([
      this.listSessions(),
      this.listWindows(),
      this.listPanes(),
      this.runner.run(["display-message", "-p", "-F", `#{session_name}${UNIT_SEP}#{pane_id}`]),
    ]);

    const listResults = [sessionsRes, windowsRes, panesRes];
    for (const res of listResults) {
      if (res.status === "rejected" && res.reason instanceof MultiplexerNoServerError) {
        return {
          sessions: [],
          windows: [],
          panes: [],
          currentSession: "",
          currentPaneId: "",
        };
      }
    }
    for (const res of listResults) {
      if (res.status === "rejected") {
        throw res.reason;
      }
    }

    let currentSession = "";
    let currentPaneId = "";
    if (currentRes.status === "fulfilled") {
      const res = currentRes.value;
      if (!res.error && res.exitCode === 0) {
        const fields = res.stdout.trim().split(UNIT_SEP);
        if (fields.length === 2) {
          currentSession = fields[0]!;
          currentPaneId = fields[1]!;
        }
      }
    }
    if (currentPaneId === "") {
      currentPaneId = this.currentPaneId();
    }

    return {
      sessions: (sessionsRes as PromiseFulfilledResult<DriverSession[]>).value,
      windows: (windowsRes as PromiseFulfilledResult<DriverWindow[]>).value,
      panes: (panesRes as PromiseFulfilledResult<DriverPane[]>).value,
      currentSession,
      currentPaneId,
    };
  }

  async capturePane(paneId: string, lines = 0): Promise<string> {
    const scrollback = lines <= 0 ? 200 : lines;
    return this.run(["capture-pane", "-p", "-t", paneId, "-S", `-${scrollback}`]);
  }

  async capturePaneStyled(paneId: string): Promise<string> {
    return this.run(["capture-pane", "-p", "-e", "-N", "-t", paneId]);
  }

  async switchToSpace(name: string): Promise<void> {
    if (this.insideTmux() || (await this.hasClients())) {
      await this.run(["switch-client", "-t", name]);
      return;
    }
    await this.run(["attach-session", "-t", name]);
  }

  async switchClient(name: string): Promise<void> {
    await this.run(["switch-client", "-t", name]);
  }

  async focusPane(session: string, windowId: string, paneId: string): Promise<void> {
    if (session !== "") {
      let current = "";
      try {
        current = await this.currentSession();
      } catch {
        current = "";
      }
      if (current !== session) {
        await this.switchToSpace(session);
      }
    }
    if (windowId !== "") {
      await this.run(["select-window", "-t", windowId]);
    }
    if (paneId !== "") {
      await this.run(["select-pane", "-t", paneId]);
    }
  }

  async newSession(name: string, cwd: string): Promise<void> {
    const args = ["new-session", "-d", "-s", name];
    if (cwd !== "") {
      args.push("-c", cwd);
    }
    await this.run(args);
    await this.switchToSpace(name);
  }

  async renameSession(oldName: string, newName: string): Promise<void> {
    await this.run(["rename-session", "-t", oldName, newName]);
  }

  async killSession(name: string): Promise<void> {
    await this.run(["kill-session", "-t", name]);
  }

  async newWindow(
    session: string,
    cwd: string,
    name: string,
    command: string[] = [],
  ): Promise<void> {
    await this.run(buildNewWindowArgs(["new-window"], session, cwd, name, command));
  }

  async newWindowReturningId(
    session: string,
    cwd: string,
    name: string,
    command: string[] = [],
  ): Promise<string> {
    const args = buildNewWindowArgs(
      ["new-window", "-P", "-F", "#{window_id}"],
      session,
      cwd,
      name,
      command,
    );
    const out = await this.run(args);
    return out.trim();
  }

  async renameWindow(target: string, newName: string): Promise<void> {
    const args = ["rename-window"];
    if (target !== "") {
      args.push("-t", target);
    }
    args.push("--", newName);
    await this.run(args);
  }

  async killWindow(target: string): Promise<void> {
    await this.run(["kill-window", "-t", target]);
  }

  async setPaneOption(paneId: string, option: string, value: string): Promise<void> {
    await this.setScopedOption(["-p"], paneId, option, value);
  }

  async unsetPaneOption(paneId: string, option: string): Promise<void> {
    const args = ["set-option", "-p"];
    if (paneId !== "") {
      args.push("-t", paneId);
    }
    args.push("-u", normalizeOption(option));
    await this.run(args);
  }

  async setSpacesStatus(
    paneId: string,
    state: string,
    app: string,
    kind: string,
    msg: string,
    tsMs: number,
  ): Promise<void> {
    if (state === "" || state === "clear") {
      for (const option of SPACES_STATUS_OPTIONS) {
        await this.unsetPaneOption(paneId, option);
      }
      return;
    }
    const values: Record<(typeof SPACES_STATUS_OPTIONS)[number], string> = {
      "@spaces_state": state,
      "@spaces_app": app,
      "@spaces_kind": kind,
      "@spaces_msg": msg,
      "@spaces_ts": String(tsMs),
    };
    for (const option of SPACES_STATUS_OPTIONS) {
      await this.setPaneOption(paneId, option, values[option]);
    }
  }

  async setWindowOption(windowId: string, option: string, value: string): Promise<void> {
    await this.setScopedOption(["-w"], windowId, option, value);
  }

  async setSessionOption(session: string, option: string, value: string): Promise<void> {
    await this.setScopedOption([], session, option, value);
  }

  async getGlobalOption(option: string): Promise<string> {
    const out = await this.run(["show-option", "-gqv", option]);
    return out.trim();
  }

  async setGlobalOption(option: string, value: string): Promise<void> {
    await this.run(["set-option", "-gq", option, value]);
  }

  async sendKeys(target: string, text: string, enter = false): Promise<void> {
    await this.run(["send-keys", "-t", target, "-l", text]);
    if (enter) {
      await this.run(["send-keys", "-t", target, "Enter"]);
    }
  }

  async displayPopup(opts: TmuxPopupOptions): Promise<void> {
    const args = ["display-popup"];
    if (opts.close) {
      args.push("-E");
    }
    if (opts.width) {
      args.push("-w", opts.width);
    }
    if (opts.height) {
      args.push("-h", opts.height);
    }
    if (opts.title) {
      args.push("-T", opts.title);
    }
    if (opts.border) {
      args.push("-b", opts.border);
    }
    if (opts.cwd) {
      args.push("-d", opts.cwd);
    }
    if (opts.command && opts.command.length > 0) {
      args.push(...opts.command);
    }
    await this.run(args);
  }

  async newSessionDetached(
    name: string,
    cwd: string,
    windowName: string,
    command: string[] = [],
  ): Promise<void> {
    await this.run(buildNewSessionDetachedArgs(name, cwd, windowName, 0, 0, [], command));
  }

  async newSessionDetachedWithId(
    name: string,
    cwd: string,
    windowName: string,
    width: number,
    height: number,
    command: string[] = [],
  ): Promise<{ windowId: string; paneId: string }> {
    const out = await this.run(
      buildNewSessionDetachedArgs(
        name,
        cwd,
        windowName,
        width,
        height,
        ["-P", "-F", `#{window_id}${UNIT_SEP}#{pane_id}`],
        command,
      ),
    );
    return parseWindowPaneIdPair(out);
  }

  async newWindowDetachedWithId(
    session: string,
    cwd: string,
    name: string,
    command: string[] = [],
  ): Promise<{ windowId: string; paneId: string }> {
    const args = buildNewWindowArgs(
      ["new-window", "-d", "-P", "-F", `#{window_id}${UNIT_SEP}#{pane_id}`],
      session,
      cwd,
      name,
      command,
    );
    const out = await this.run(args);
    return parseWindowPaneIdPair(out);
  }

  async splitWindow(target: string, cwd: string, command: string[] = []): Promise<void> {
    await this.run(buildSplitWindowArgs(["split-window", "-t", target], cwd, command));
  }

  async splitWindowWithId(
    target: string,
    cwd: string,
    command: string[] = [],
  ): Promise<string> {
    const out = await this.run(
      buildSplitWindowArgs(
        ["split-window", "-d", "-t", target, "-P", "-F", "#{pane_id}"],
        cwd,
        command,
      ),
    );
    return out.trim();
  }

  async selectLayout(target: string, layout: string): Promise<void> {
    if (layout.trim() === "") {
      return;
    }
    await this.run(["select-layout", "-t", target, layout]);
  }

  async selectWindow(target: string): Promise<void> {
    await this.run(["select-window", "-t", target]);
  }

  async selectPane(target: string): Promise<void> {
    await this.run(["select-pane", "-t", target]);
  }

  async resizePaneZoom(target: string): Promise<void> {
    await this.run(["resize-pane", "-Z", "-t", target]);
  }
}

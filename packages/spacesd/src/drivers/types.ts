import type { BackendDriverKind } from "@spaces/types";

/**
 * Error raised by a multiplexer driver when no server/session is running.
 */
export class MultiplexerNoServerError extends Error {
  constructor(message = "no multiplexer server running") {
    super(message);
    this.name = "MultiplexerNoServerError";
  }
}

/**
 * Multiplexer-agnostic session (space) metadata from a live server snapshot.
 */
export interface DriverSession {
  id: string;
  name: string;
  attached: number;
  createdAtSec: number;
  windowCount: number;
  worktreePath: string;
  worktreeName: string;
  worktreeBase: string;
  worktreeManaged: boolean;
}

/**
 * Multiplexer-agnostic window (tab) metadata from a live server snapshot.
 */
export interface DriverWindow {
  sessionName: string;
  id: string;
  index: number;
  name: string;
  active: boolean;
  last: boolean;
  zoomed: boolean;
  bell: boolean;
  activity: boolean;
  silence: boolean;
  width: number;
  height: number;
  activityAtSec: number;
  layout: string;
  worktreePath: string;
  worktreeName: string;
  worktreeBase: string;
  worktreeManaged: boolean;
}

/**
 * Multiplexer-agnostic pane metadata from a live server snapshot.
 */
export interface DriverPane {
  sessionName: string;
  windowId: string;
  windowIndex: number;
  windowName: string;
  paneIndex: number;
  id: string;
  numericId: number;
  title: string;
  currentCommand: string;
  startCommand: string;
  currentPath: string;
  pid: number;
  width: number;
  height: number;
  active: boolean;
  windowActive: boolean;
  inMode: boolean;
  synchronized: boolean;
  dead: boolean;
  deadStatus: number;
  bell: boolean;
  activity: boolean;
  silence: boolean;
  spacesState: string;
  spacesApp: string;
  spacesKind: string;
  spacesMsg: string;
  spacesTsMs: number;
  spacesSource: string;
  spacesTitle: string;
  resumeSession: string;
  profile: string;
  launchCommand: string[];
}

/**
 * Point-in-time snapshot of all sessions, windows, and panes from a multiplexer.
 */
export interface DriverSnapshot {
  sessions: DriverSession[];
  windows: DriverWindow[];
  panes: DriverPane[];
  currentSession: string;
  currentPaneId: string;
}

/**
 * Swappable multiplexer driver contract (`tmux` primary now, `kitty` + `zellij` later).
 *
 * `BackendService`, `resurrect`, and `spacesd` depend only on this interface.
 */
export interface MultiplexerDriver {
  readonly kind: BackendDriverKind;

  // Inspection
  serverRunning(): Promise<boolean>;
  currentSession(): Promise<string>;
  hasClients(): Promise<boolean>;
  listSessions(): Promise<DriverSession[]>;
  listWindows(): Promise<DriverWindow[]>;
  listPanes(): Promise<DriverPane[]>;
  snapshot(): Promise<DriverSnapshot>;
  capturePane(paneId: string, lines?: number): Promise<string>;
  capturePaneStyled(paneId: string): Promise<string>;

  // Core space/window/pane verbs
  switchToSpace(name: string): Promise<void>;
  switchClient(name: string): Promise<void>;
  focusPane(session: string, windowId: string, paneId: string): Promise<void>;
  newSession(name: string, cwd: string): Promise<void>;
  renameSession(oldName: string, newName: string): Promise<void>;
  killSession(name: string): Promise<void>;
  newWindow(session: string, cwd: string, name: string, command?: string[]): Promise<void>;
  newWindowReturningId(
    session: string,
    cwd: string,
    name: string,
    command?: string[],
  ): Promise<string>;
  renameWindow(target: string, newName: string): Promise<void>;
  killWindow(target: string): Promise<void>;

  // Metadata options
  setPaneOption(paneId: string, option: string, value: string): Promise<void>;
  unsetPaneOption(paneId: string, option: string): Promise<void>;
  setSpacesStatus(
    paneId: string,
    state: string,
    app: string,
    kind: string,
    msg: string,
    tsMs: number,
  ): Promise<void>;
  setWindowOption(windowId: string, option: string, value: string): Promise<void>;
  setSessionOption(session: string, option: string, value: string): Promise<void>;
  getGlobalOption(option: string): Promise<string>;
  setGlobalOption(option: string, value: string): Promise<void>;

  // Resurrect layout & session reconstruction primitives
  newSessionDetached(
    name: string,
    cwd: string,
    windowName: string,
    command?: string[],
  ): Promise<void>;
  newSessionDetachedWithId(
    name: string,
    cwd: string,
    windowName: string,
    width: number,
    height: number,
    command?: string[],
  ): Promise<{ windowId: string; paneId: string }>;
  newWindowDetachedWithId(
    session: string,
    cwd: string,
    name: string,
    command?: string[],
  ): Promise<{ windowId: string; paneId: string }>;
  splitWindow(target: string, cwd: string, command?: string[]): Promise<void>;
  splitWindowWithId(target: string, cwd: string, command?: string[]): Promise<string>;
  selectLayout(target: string, layout: string): Promise<void>;
  selectWindow(target: string): Promise<void>;
  selectPane(target: string): Promise<void>;
  resizePaneZoom(target: string): Promise<void>;
}

import { existsSync } from "node:fs";
import type { Database, SQLQueryBindings } from "bun:sqlite";
import type {
  BootId,
  Epoch,
  EventEnvelope,
  EventId,
  EventPayload,
  EventSubscriptionFilter,
  ExitedSpace,
  HandoffReason,
  HookChild,
  HookTool,
  HookUsage,
  Lease,
  LeaseId,
  LeaseOwner,
  LeaseResult,
  LeaseState,
  LeaseTransition,
  MailboxAddress,
  MailboxMessage,
  MailboxMessageId,
  MailboxMessageKind,
  MailboxPriority,
  MailboxStatus,
  MailboxSubscription,
  OutcomeEntry,
  PtyId,
  Seq,
  Thread,
  ThreadDriver,
  ThreadId,
  ThreadKind,
  ThreadListParams,
  ThreadState,
  TimestampMs,
  Turn,
  TurnError,
  TurnId,
  TurnOrigin,
  TurnState,
  TurnUsage,
  WorktreeRecord,
} from "@spaces/types";
import { openSpacesDatabase } from "./schema";

export const WORKING_STALE_MS = 120_000;
export const DONE_TTL_MS = 30 * 60_000;
export const FAILED_TTL_MS = 30 * 60_000;
export const BLOCK_TTL_MS = 30 * 60_000;
export const AUTH_TTL_MS = 60 * 60_000;
export const OUTCOME_TTL_MS = 24 * 60 * 60_000;
export const OUTCOME_DEDUPE_MS = 15_000;
export const MAX_OUTCOMES = 20;

const MAX_TEXT = 240;
const MAX_MODEL = 200;
const MAX_SESSION = 200;
const MAX_CWD = 1000;

export type ThreadPatch = {
  space?: string | null;
  window_id?: string | null;
  pane_id?: string | null;
  pty_id?: PtyId | null;
  agent?: string;
  profile?: string | null;
  driver?: ThreadDriver;
  kind?: ThreadKind;
  state?: ThreadState;
  cwd?: string;
  canonical_cwd?: string;
  worktree_path?: string | null;
  vendor_session_id?: string | null;
  title?: string | null;
  model?: string | null;
  epoch?: Epoch;
  active_turn_id?: TurnId | null;
  updated_at_ms?: TimestampMs;
};

export type TurnPatch = {
  epoch?: Epoch;
  origin?: TurnOrigin;
  state?: TurnState;
  prompt?: string | null;
  reply_preview?: string | null;
  model?: string | null;
  usage?: TurnUsage | null;
  error?: TurnError | null;
  ended_at_ms?: TimestampMs | null;
};

export interface EventBounds {
  earliestSeq: Seq | null;
  latestSeq: Seq | null;
}

export interface OutcomeInput {
  session: string;
  pane?: string;
  agent: string;
  app?: string;
  profile?: string;
  outcome: string;
  text?: string;
  title?: string;
  model?: string;
  session_id?: string;
  cwd?: string;
  ts_ms?: TimestampMs;
}

export interface RecordOutcomeResult {
  recorded: boolean;
  notify: boolean;
  entry: OutcomeEntry | null;
}

export interface RemoveOutcomeOptions {
  agent?: string;
  app?: string;
  session_id?: string;
  session?: string;
  pane?: string;
  ts_ms?: TimestampMs;
}

export type HookChildInput = Partial<HookChild> & { id?: string };

export interface HookPaneInput {
  session: string;
  pane: string;
  agent: string;
  app?: string;
  source?: string;
  state: string;
  kind?: string | null;
  event: string;
  ts_ms: TimestampMs;
  state_since_ms?: TimestampMs;
  turn_started_ms?: TimestampMs;
  text?: string;
  title?: string;
  prompt?: string | null;
  tool?: HookTool | null;
  reply?: string | null;
  model?: string | null;
  profile?: string | null;
  session_id?: string | null;
  cwd?: string | null;
  usage?: HookUsage | null;
  children?: HookChildInput[];
  background_tasks?: HookChildInput[];
  waiting_on?: string | null;
  last_event?: Record<string, unknown> | null;
}

export interface HookPaneRecord {
  session: string;
  pane: string;
  agent: string;
  app: string;
  source: string;
  state: string;
  kind: string | null;
  event: string;
  ts_ms: TimestampMs;
  state_since_ms: TimestampMs;
  turn_started_ms: TimestampMs;
  text: string;
  title: string;
  prompt: string | null;
  tool: HookTool | null;
  reply: string | null;
  model: string | null;
  profile: string | null;
  session_id: string | null;
  cwd: string | null;
  usage: HookUsage | null;
  children: HookChild[];
  background_tasks: HookChild[];
  waiting_on: string | null;
  last_event: Record<string, unknown> | null;
}

export interface ConversationOverlayInput {
  agent: string;
  session_id: string;
  space?: string;
  last_opened_ms?: TimestampMs;
  last_outcome?: string;
  profile?: string;
  cwd?: string;
  title?: string;
  launch_cmd?: string[];
  updated_at_ms?: TimestampMs;
}

export interface ConversationOverlayRecord {
  agent: string;
  session_id: string;
  space: string;
  last_opened_ms: TimestampMs;
  last_outcome: string;
  profile: string;
  cwd: string;
  title: string;
  launch_cmd?: string[];
  updated_at_ms: TimestampMs;
}

export interface ResurrectPaneSnapshot {
  index: number;
  id?: string;
  cwd?: string;
  command?: string;
  app?: string;
  title?: string;
  active: boolean;
  agent?: string;
  session_id?: string;
  profile?: string;
  full_command?: string[];
}

export interface ResurrectWindowSnapshot {
  index: number;
  name: string;
  layout?: string;
  active: boolean;
  zoomed?: boolean;
  width?: number;
  height?: number;
  worktree_path?: string;
  worktree_name?: string;
  worktree_base?: string;
  panes: ResurrectPaneSnapshot[];
}

export interface ResurrectSpaceSnapshot {
  version?: number;
  name: string;
  saved_at_ms?: TimestampMs;
  cwd?: string;
  worktree_path?: string;
  worktree_name?: string;
  worktree_base?: string;
  main_repo_root?: string;
  active_window_index?: number;
  windows: ResurrectWindowSnapshot[];
}

export interface ResurrectOpenState {
  version: number;
  saved_at_ms: TimestampMs;
  active_session: string;
  spaces: string[];
}

export interface PaneHmacTokenRecord {
  pane_id: string;
  thread_id?: ThreadId;
  secret_hex: string;
  created_at_ms: TimestampMs;
  rotated_at_ms?: TimestampMs;
  revoked_at_ms?: TimestampMs;
}

export interface VerifiedResumeHintRecord {
  pane_id: string;
  thread_id?: ThreadId;
  agent: string;
  session_id: string;
  profile?: string;
  cwd?: string;
  resume_target?: string;
  resume_cmd?: string[];
  signature_hex: string;
  verified_at_ms: TimestampMs;
  updated_at_ms: TimestampMs;
}

export type VerifiedResumeHintInput = Omit<VerifiedResumeHintRecord, "updated_at_ms"> & {
  updated_at_ms?: TimestampMs;
};

interface ThreadRow {
  id: string;
  space: string | null;
  window_id: string | null;
  pane_id: string | null;
  pty_id: string | null;
  agent: string;
  profile: string | null;
  driver: string;
  kind: string;
  state: string;
  cwd: string;
  canonical_cwd: string;
  worktree_path: string | null;
  vendor_session_id: string | null;
  title: string | null;
  model: string | null;
  epoch: number;
  active_turn_id: string | null;
  created_at_ms: number;
  updated_at_ms: number;
}

interface LeaseRow {
  thread_id: string;
  id: string;
  epoch: number;
  state: string;
  owner_json: string | null;
  acquired_at_ms: number | null;
  expires_at_ms: number | null;
  updated_at_ms: number;
}

interface LeaseTransitionRow {
  id: number;
  thread_id: string;
  from_state: string;
  to_state: string;
  from_epoch: number;
  to_epoch: number;
  reason: string;
  actor_json: string | null;
  ts_ms: number;
}

interface TurnRow {
  id: string;
  thread_id: string;
  epoch: number;
  origin: string;
  state: string;
  prompt: string | null;
  reply_preview: string | null;
  model: string | null;
  usage_json: string | null;
  error_json: string | null;
  started_at_ms: number;
  ended_at_ms: number | null;
}

interface EventRow {
  boot_id: string;
  seq: number;
  id: string;
  ts_ms: number;
  thread_id: string | null;
  epoch: number | null;
  event_type: string;
  payload_json: string;
}

interface MailboxRow {
  id: string;
  thread_id: string;
  from_json: string;
  kind: string;
  priority: string;
  status: string;
  dedup_key: string | null;
  correlation_id: string | null;
  payload_json: string;
  created_at_ms: number;
  delivered_at_ms: number | null;
  expires_at_ms: number | null;
}

interface SubscriptionRow {
  id: string;
  thread_id: string;
  source: string;
  filter_json: string | null;
  created_at_ms: number;
}

interface OutcomeRow {
  id: number;
  session: string;
  pane: string;
  agent: string;
  app: string;
  profile: string;
  outcome: string;
  text: string;
  title: string;
  model: string;
  session_id: string;
  cwd: string;
  ts_ms: number;
}

interface HookPaneRow {
  session: string;
  pane: string;
  agent: string;
  app: string;
  source: string;
  state: string;
  kind: string | null;
  event: string;
  ts_ms: number;
  state_since_ms: number;
  turn_started_ms: number;
  text: string;
  title: string;
  prompt: string | null;
  tool_json: string | null;
  reply: string | null;
  model: string | null;
  profile: string | null;
  session_id: string | null;
  cwd: string | null;
  usage_json: string | null;
  children_json: string;
  background_tasks_json: string;
  waiting_on: string | null;
  last_event_json: string | null;
}

interface ConversationOverlayRow {
  agent: string;
  session_id: string;
  space: string;
  last_opened_ms: number;
  last_outcome: string;
  profile: string;
  cwd: string;
  title: string;
  launch_cmd_json: string;
  updated_at_ms: number;
}

interface ResurrectSpaceRow {
  name: string;
  version: number;
  saved_at_ms: number;
  cwd: string;
  worktree_path: string;
  worktree_name: string;
  worktree_base: string;
  main_repo_root: string;
  active_window_index: number;
  windows_json: string;
}

interface ResurrectOpenStateRow {
  id: number;
  version: number;
  saved_at_ms: number;
  active_session: string;
  spaces_json: string;
}

interface PaneHmacTokenRow {
  pane_id: string;
  thread_id: string | null;
  secret_hex: string;
  created_at_ms: number;
  rotated_at_ms: number | null;
  revoked_at_ms: number | null;
}

interface VerifiedResumeHintRow {
  pane_id: string;
  thread_id: string | null;
  agent: string;
  session_id: string;
  profile: string | null;
  cwd: string | null;
  resume_target: string | null;
  resume_cmd_json: string | null;
  signature_hex: string;
  verified_at_ms: number;
  updated_at_ms: number;
}

const HOOK_AGENT_ALIASES: Record<string, string> = {
  agy: "antigravity",
  dsh: "deepseek",
  "kimi-code": "kimi",
  cursor: "cursor-agent",
};

const DISPLAY_APP_NAMES: Record<string, string> = {
  claude: "claude-code",
  "kimi-cli": "kimi",
  dsh: "deepseek",
  "dsh-tui": "deepseek",
  cursor: "cursor-agent",
};

const CONVERSATION_AGENT_ALIASES: Record<string, string> = {
  "claude-code": "claude",
  claude_code: "claude",
  claudecode: "claude",
  "openai-codex": "codex",
  "open-code": "opencode",
  "herdr:opencode": "opencode",
  oc: "opencode",
  agy: "antigravity",
  "antigravity-cli": "antigravity",
  "kimi-cli": "kimi",
  "kimi-code": "kimi",
  "kimi code": "kimi",
  dsh: "deepseek",
  "dsh-tui": "deepseek",
  cursor: "cursor-agent",
  cursor_agent: "cursor-agent",
  "cursor-cli": "cursor-agent",
  "gemini-cli": "gemini",
  "github-copilot": "copilot",
  ghcs: "copilot",
  "grok-build": "grok",
  "qwen-code": "qwen",
  "qwen code": "qwen",
  "herdr:pi": "pi",
  "vibe-acp": "vibe",
  "fx-router": "fx",
  "kilo-code": "kilo",
  "kilo code": "kilo",
  "herdr:kilo": "kilo",
  "muse-code": "muse",
  "muse-cli": "muse",
  "amp-local": "amp",
  "devin-cli": "devin",
  "devin cli": "devin",
  "hermes-agent": "hermes",
  "kiro-cli": "kiro",
  "letta-code": "letta",
  "letta code": "letta",
  qoderclicn: "qodercli",
  qoder: "qodercli",
  qodercn: "qodercli",
  "roo-code": "roo",
};

const ACTIVE_BG_STATES = new Set([
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

function parseJson<T>(raw: string): T {
  return JSON.parse(raw) as T;
}

function parseOptionalJson<T>(raw: string | null): T | undefined {
  if (raw === null || raw === "") {
    return undefined;
  }
  return JSON.parse(raw) as T;
}

function assignOptional<T extends object, K extends keyof T>(
  target: T,
  key: K,
  value: T[K] | null | undefined,
): void {
  if (value !== null && value !== undefined) {
    target[key] = value;
  }
}

function stripControls(s: string): string {
  let out = "";
  for (let i = 0; i < s.length; i++) {
    const code = s.charCodeAt(i);
    if (
      code <= 0x08 ||
      code === 0x0b ||
      code === 0x0c ||
      (code >= 0x0e && code <= 0x1f) ||
      code === 0x7f
    ) {
      out += " ";
    } else {
      out += s[i];
    }
  }
  return out;
}

function truncateCodePoints(s: string, limit: number): string {
  const chars = Array.from(s);
  if (chars.length <= limit) {
    return s;
  }
  const n = Math.max(1, limit - 1);
  return `${chars.slice(0, n).join("")}…`;
}

function oneLine(raw: string | undefined, limit: number): string {
  if (!raw) {
    return "";
  }
  const collapsed = stripControls(raw).trim().replace(/\s+/g, " ");
  if (!collapsed) {
    return "";
  }
  return truncateCodePoints(collapsed, limit);
}

function canonicalHookAgent(agent: string): string {
  const trimmed = agent.trim().replace(/\/+$/, "");
  if (!trimmed) {
    return "unknown";
  }
  const slashIdx = trimmed.lastIndexOf("/");
  const base = (slashIdx >= 0 ? trimmed.slice(slashIdx + 1) : trimmed)
    .toLowerCase()
    .replace(/\.(js|mjs|cjs|py|exe|cmd|bat)$/, "");
  if (!base) {
    return "unknown";
  }
  return HOOK_AGENT_ALIASES[base] ?? base;
}

function displayApp(agent: string): string {
  const canon = canonicalHookAgent(agent);
  return DISPLAY_APP_NAMES[canon] ?? canon;
}

export function canonicalConversationAgent(name: string): string {
  const low = name.trim().toLowerCase();
  if (!low) {
    return "";
  }
  return CONVERSATION_AGENT_ALIASES[low] ?? low;
}

function normalizeOutcomeValue(raw: string): "done" | "failed" | null {
  switch (raw.trim().toLowerCase()) {
    case "done":
    case "complete":
    case "completed":
    case "success":
      return "done";
    case "failed":
    case "failure":
    case "error":
      return "failed";
    default:
      return null;
  }
}

function outcomeTextQuality(text: string): number {
  const lowered = text.trim().toLowerCase();
  if (!lowered) {
    return 0;
  }
  if (lowered === "finished" || lowered === "turn complete") {
    return 1;
  }
  return 2;
}

function outcomeRichness(rec: OutcomeEntry): number {
  let score = outcomeTextQuality(rec.text);
  if (rec.session_id.trim()) score += 4;
  if (rec.title.trim()) score += 2;
  if (rec.model.trim()) score += 1;
  if (rec.profile.trim()) score += 1;
  return score;
}

function mergeOutcomeMetadata(dst: OutcomeEntry, src: OutcomeEntry): void {
  if (!dst.session_id && src.session_id) dst.session_id = src.session_id;
  if (!dst.title && src.title) dst.title = src.title;
  if (!dst.profile && src.profile) dst.profile = src.profile;
  if (!dst.model && src.model) dst.model = src.model;
  if (!dst.cwd && src.cwd) dst.cwd = src.cwd;
  if (outcomeTextQuality(dst.text) < outcomeTextQuality(src.text)) {
    dst.text = src.text;
  }
}

function outcomeAgentKey(rec: { agent?: string; app?: string }): string {
  const a = (rec.agent ?? "").trim().toLowerCase();
  if (a) {
    return a;
  }
  return (rec.app ?? "").trim().toLowerCase();
}

function outcomeConversationKey(rec: {
  agent?: string;
  app?: string;
  session_id?: string;
}): string {
  const sid = (rec.session_id ?? "").trim();
  if (!sid) {
    return "";
  }
  return `${outcomeAgentKey(rec)}\0${sid}`;
}

function pruneOutcomesList(items: OutcomeEntry[], nowMs: TimestampMs): OutcomeEntry[] {
  const kept = items
    .filter((rec) => {
      if (rec.outcome !== "done" && rec.outcome !== "failed") {
        return false;
      }
      if (rec.ts_ms > 0 && nowMs > rec.ts_ms && nowMs - rec.ts_ms > OUTCOME_TTL_MS) {
        return false;
      }
      return true;
    })
    .map((rec) => ({ ...rec }))
    .sort((a, b) => b.ts_ms - a.ts_ms);

  const attributedTasks = new Set<string>();
  for (const m of kept) {
    if (!outcomeConversationKey(m)) {
      continue;
    }
    const a = outcomeAgentKey(m);
    const t = m.title.trim().toLowerCase();
    const c = m.cwd.trim();
    if (a && t) {
      attributedTasks.add(`${a}\0${c}\0${t}`);
    }
  }

  const seenConv = new Set<string>();
  const seenUnattributedPane = new Set<string>();
  const deduped: OutcomeEntry[] = [];

  for (const m of kept) {
    const key = outcomeConversationKey(m);
    if (key) {
      if (seenConv.has(key)) {
        continue;
      }
      seenConv.add(key);
    } else {
      const a = outcomeAgentKey(m);
      const t = m.title.trim().toLowerCase();
      const c = m.cwd.trim();
      if (a && t && attributedTasks.has(`${a}\0${c}\0${t}`)) {
        continue;
      }
      const s = m.session.trim();
      const p = m.pane.trim().replace(/^%/, "");
      if (a && s && p) {
        const pk = `${a}\0${s}\0${p}`;
        if (seenUnattributedPane.has(pk)) {
          continue;
        }
        seenUnattributedPane.add(pk);
      }
    }
    deduped.push(m);
    if (deduped.length >= MAX_OUTCOMES) {
      break;
    }
  }

  return deduped;
}

function isActiveBgState(state: string | undefined): boolean {
  return ACTIVE_BG_STATES.has((state ?? "").trim().toLowerCase());
}

function normalizeHookChildren(
  items: HookChildInput[] | undefined,
  defaultCategory: string,
  defaultKind: string,
  fallbackTs: TimestampMs,
): HookChild[] {
  if (!items || items.length === 0) {
    return [];
  }
  return items.map((c) => ({
    id: c.id ?? "",
    type: c.type ?? "",
    kind: c.kind ?? defaultKind,
    category: c.category ?? defaultCategory,
    state: c.state ?? "working",
    outcome: c.outcome ?? "",
    command: c.command ?? "",
    schedule: c.schedule ?? "",
    since_ms: c.since_ms ?? c.ts_ms ?? fallbackTs,
    ts_ms: c.ts_ms ?? fallbackTs,
    ended_ms: c.ended_ms ?? 0,
    text: c.text ?? "",
  }));
}

function collectBackgroundWork(
  children: readonly HookChild[],
  backgroundTasks: readonly HookChild[],
): { activeFinite: HookChild[]; activeServices: HookChild[] } {
  const activeFinite: HookChild[] = [];
  const activeServices: HookChild[] = [];
  const seen = new Set<string>();

  const consider = (item: HookChild, defaultCategory: string): void => {
    const id = item.id.trim();
    if (id) {
      if (seen.has(id)) {
        return;
      }
      seen.add(id);
    }
    if (!isActiveBgState(item.state)) {
      return;
    }
    const category = item.category.trim() || defaultCategory;
    if (category === "finite") {
      activeFinite.push(item);
    } else {
      activeServices.push(item);
    }
  };

  for (const task of backgroundTasks) {
    consider(task, "finite");
  }
  for (const child of children) {
    consider(child, "finite");
  }

  return { activeFinite, activeServices };
}

function bgItemSummaryLabel(item: HookChild): string {
  for (const candidate of [item.text, item.command, item.type, item.id]) {
    if (candidate && candidate.trim()) {
      return candidate.trim();
    }
  }
  return "background task";
}

function formatWaitingText(finiteItems: readonly HookChild[]): string {
  if (finiteItems.length === 0) {
    return "";
  }
  if (finiteItems.length === 1) {
    const item = finiteItems[0]!;
    const label = bgItemSummaryLabel(item);
    if (item.kind.trim() === "subagent") {
      return oneLine(`waiting on subagent: ${label}`, MAX_TEXT);
    }
    return oneLine(`waiting on: ${label}`, MAX_TEXT);
  }
  const labels = finiteItems
    .map((m) => oneLine(bgItemSummaryLabel(m), 48))
    .sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  return oneLine(`waiting on ${finiteItems.length} tasks: ${labels.join(", ")}`, MAX_TEXT);
}

function isHookPaneStale(rec: HookPaneRecord, nowMs: TimestampMs): boolean {
  const since = rec.state_since_ms ?? rec.ts_ms;
  switch (rec.state) {
    case "working": {
      const { activeFinite } = collectBackgroundWork(rec.children, rec.background_tasks);
      if (activeFinite.length > 0 || (rec.waiting_on !== null && rec.waiting_on.trim() !== "")) {
        return false;
      }
      return nowMs - rec.ts_ms > WORKING_STALE_MS;
    }
    case "idle":
      return nowMs - rec.ts_ms > WORKING_STALE_MS;
    case "done":
      return nowMs - since > DONE_TTL_MS;
    case "failed":
      return nowMs - since > FAILED_TTL_MS;
    case "blocked":
      if (rec.kind === "auth") {
        return nowMs - since > AUTH_TTL_MS;
      }
      return nowMs - since > BLOCK_TTL_MS;
    default:
      return nowMs - rec.ts_ms > WORKING_STALE_MS;
  }
}

function filterCodexTurnChildren(rec: HookPaneRecord): HookChild[] {
  if (rec.agent !== "codex" || rec.turn_started_ms <= 0) {
    return rec.children;
  }
  return rec.children.filter(
    (child) => isActiveBgState(child.state) || child.ended_ms >= rec.turn_started_ms,
  );
}

function rowToThread(row: ThreadRow): Thread {
  const out: Thread = {
    id: row.id,
    agent: row.agent,
    driver: row.driver as ThreadDriver,
    kind: row.kind as ThreadKind,
    state: row.state as ThreadState,
    cwd: row.cwd,
    canonical_cwd: row.canonical_cwd,
    epoch: row.epoch,
    created_at_ms: row.created_at_ms,
    updated_at_ms: row.updated_at_ms,
  };
  assignOptional(out, "space", row.space);
  assignOptional(out, "window_id", row.window_id);
  assignOptional(out, "pane_id", row.pane_id);
  assignOptional(out, "pty_id", row.pty_id);
  assignOptional(out, "profile", row.profile);
  assignOptional(out, "worktree_path", row.worktree_path);
  assignOptional(out, "vendor_session_id", row.vendor_session_id);
  assignOptional(out, "title", row.title);
  assignOptional(out, "model", row.model);
  assignOptional(out, "active_turn_id", row.active_turn_id);
  return out;
}

function rowToLease(row: LeaseRow): Lease {
  const out: Lease = {
    id: row.id,
    thread_id: row.thread_id,
    epoch: row.epoch,
    state: row.state as LeaseState,
    updated_at_ms: row.updated_at_ms,
  };
  assignOptional(out, "owner", parseOptionalJson<LeaseOwner>(row.owner_json));
  assignOptional(out, "acquired_at_ms", row.acquired_at_ms);
  assignOptional(out, "expires_at_ms", row.expires_at_ms);
  return out;
}

function rowToLeaseTransition(row: LeaseTransitionRow): LeaseTransition {
  const out: LeaseTransition = {
    thread_id: row.thread_id,
    from_state: row.from_state as LeaseState,
    to_state: row.to_state as LeaseState,
    from_epoch: row.from_epoch,
    to_epoch: row.to_epoch,
    reason: row.reason as HandoffReason,
    ts_ms: row.ts_ms,
  };
  assignOptional(out, "actor", parseOptionalJson<LeaseOwner>(row.actor_json));
  return out;
}

function rowToTurn(row: TurnRow): Turn {
  const out: Turn = {
    id: row.id,
    thread_id: row.thread_id,
    epoch: row.epoch,
    origin: row.origin as TurnOrigin,
    state: row.state as TurnState,
    started_at_ms: row.started_at_ms,
  };
  assignOptional(out, "prompt", row.prompt);
  assignOptional(out, "reply_preview", row.reply_preview);
  assignOptional(out, "model", row.model);
  assignOptional(out, "usage", parseOptionalJson<TurnUsage>(row.usage_json));
  assignOptional(out, "error", parseOptionalJson<TurnError>(row.error_json));
  assignOptional(out, "ended_at_ms", row.ended_at_ms);
  return out;
}

function rowToEventEnvelope(row: EventRow): EventEnvelope {
  const out: EventEnvelope = {
    id: row.id,
    boot_id: row.boot_id,
    seq: row.seq,
    ts_ms: row.ts_ms,
    payload: parseJson<EventPayload>(row.payload_json),
  };
  assignOptional(out, "thread_id", row.thread_id);
  assignOptional(out, "epoch", row.epoch);
  return out;
}

function rowToMailboxMessage(row: MailboxRow): MailboxMessage {
  const out: MailboxMessage = {
    id: row.id,
    thread_id: row.thread_id,
    from: parseJson<MailboxAddress>(row.from_json),
    kind: row.kind as MailboxMessageKind,
    priority: row.priority as MailboxPriority,
    status: row.status as MailboxStatus,
    payload: parseJson<unknown>(row.payload_json),
    created_at_ms: row.created_at_ms,
  };
  assignOptional(out, "dedup_key", row.dedup_key);
  assignOptional(out, "correlation_id", row.correlation_id);
  assignOptional(out, "delivered_at_ms", row.delivered_at_ms);
  assignOptional(out, "expires_at_ms", row.expires_at_ms);
  return out;
}

function rowToSubscription(row: SubscriptionRow): MailboxSubscription {
  const out: MailboxSubscription = {
    id: row.id,
    thread_id: row.thread_id,
    source: row.source,
    created_at_ms: row.created_at_ms,
  };
  if (row.filter_json !== null) {
    out.filter = parseJson<unknown>(row.filter_json);
  }
  return out;
}

function rowToOutcome(row: OutcomeRow): OutcomeEntry {
  return {
    session: row.session,
    pane: row.pane,
    agent: row.agent,
    app: row.app,
    profile: row.profile,
    outcome: row.outcome,
    text: row.text,
    title: row.title,
    model: row.model,
    session_id: row.session_id,
    cwd: row.cwd,
    ts_ms: row.ts_ms,
  };
}

function rowToHookPane(row: HookPaneRow): HookPaneRecord {
  return {
    session: row.session,
    pane: row.pane,
    agent: row.agent,
    app: row.app,
    source: row.source,
    state: row.state,
    kind: row.kind,
    event: row.event,
    ts_ms: row.ts_ms,
    state_since_ms: row.state_since_ms,
    turn_started_ms: row.turn_started_ms,
    text: row.text,
    title: row.title,
    prompt: row.prompt,
    tool: parseOptionalJson<HookTool>(row.tool_json) ?? null,
    reply: row.reply,
    model: row.model,
    profile: row.profile,
    session_id: row.session_id,
    cwd: row.cwd,
    usage: parseOptionalJson<HookUsage>(row.usage_json) ?? null,
    children: parseJson<HookChild[]>(row.children_json || "[]"),
    background_tasks: parseJson<HookChild[]>(row.background_tasks_json || "[]"),
    waiting_on: row.waiting_on,
    last_event: parseOptionalJson<Record<string, unknown>>(row.last_event_json) ?? null,
  };
}

function decodeLaunchCmdJson(raw: string): string[] | null {
  const trimmed = raw.trim();
  if (!trimmed) {
    return null;
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!Array.isArray(parsed) || parsed.length === 0) {
      return null;
    }
    return parsed.map((item) => String(item));
  } catch {
    return null;
  }
}

function rowToOverlay(row: ConversationOverlayRow): ConversationOverlayRecord {
  const launchCmd = decodeLaunchCmdJson(row.launch_cmd_json);
  const out: ConversationOverlayRecord = {
    agent: canonicalConversationAgent(row.agent),
    session_id: row.session_id.trim(),
    space: row.space,
    last_opened_ms: row.last_opened_ms,
    last_outcome: row.last_outcome,
    profile: row.profile,
    cwd: row.cwd,
    title: row.title,
    updated_at_ms: row.updated_at_ms,
  };
  if (launchCmd) {
    out.launch_cmd = launchCmd;
  }
  return out;
}

function rowToResurrectSpace(row: ResurrectSpaceRow): ResurrectSpaceSnapshot {
  const out: ResurrectSpaceSnapshot = {
    version: row.version,
    name: row.name,
    saved_at_ms: row.saved_at_ms,
    active_window_index: row.active_window_index,
    windows: parseJson<ResurrectWindowSnapshot[]>(row.windows_json || "[]"),
  };
  if (row.cwd) out.cwd = row.cwd;
  if (row.worktree_path) out.worktree_path = row.worktree_path;
  if (row.worktree_name) out.worktree_name = row.worktree_name;
  if (row.worktree_base) out.worktree_base = row.worktree_base;
  if (row.main_repo_root) out.main_repo_root = row.main_repo_root;
  return out;
}

function rowToPaneToken(row: PaneHmacTokenRow): PaneHmacTokenRecord {
  const out: PaneHmacTokenRecord = {
    pane_id: row.pane_id,
    secret_hex: row.secret_hex,
    created_at_ms: row.created_at_ms,
  };
  assignOptional(out, "thread_id", row.thread_id);
  assignOptional(out, "rotated_at_ms", row.rotated_at_ms);
  assignOptional(out, "revoked_at_ms", row.revoked_at_ms);
  return out;
}

function rowToVerifiedHint(row: VerifiedResumeHintRow): VerifiedResumeHintRecord {
  const out: VerifiedResumeHintRecord = {
    pane_id: row.pane_id,
    agent: row.agent,
    session_id: row.session_id,
    signature_hex: row.signature_hex,
    verified_at_ms: row.verified_at_ms,
    updated_at_ms: row.updated_at_ms,
  };
  assignOptional(out, "thread_id", row.thread_id);
  assignOptional(out, "profile", row.profile);
  assignOptional(out, "cwd", row.cwd);
  assignOptional(out, "resume_target", row.resume_target);
  assignOptional(out, "resume_cmd", parseOptionalJson<string[]>(row.resume_cmd_json));
  return out;
}

function dedupeSpaceNames(spaces: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of spaces) {
    const trimmed = raw.trim();
    if (!trimmed || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    out.push(trimmed);
  }
  return out;
}

function normalizeRunningNames(
  runningNames: Iterable<string> | Record<string, boolean>,
): Set<string> {
  if (Symbol.iterator in runningNames) {
    return new Set(Array.from(runningNames as Iterable<string>));
  }
  const set = new Set<string>();
  for (const [name, running] of Object.entries(runningNames)) {
    if (running) {
      set.add(name);
    }
  }
  return set;
}

function buildPatchSets(
  fields: ReadonlyArray<readonly [column: string, value: SQLQueryBindings | undefined]>,
): { sets: string[]; params: SQLQueryBindings[] } {
  const sets: string[] = [];
  const params: SQLQueryBindings[] = [];
  for (const [column, value] of fields) {
    if (value !== undefined) {
      sets.push(`${column} = ?`);
      params.push(value);
    }
  }
  return { sets, params };
}

export class SpacesStore {
  readonly db: Database;
  private readonly now: () => TimestampMs;
  private savepointSeq = 0;

  constructor(dbOrPath: Database | string, options?: { now?: () => TimestampMs }) {
    this.db = typeof dbOrPath === "string" ? openSpacesDatabase(dbOrPath) : dbOrPath;
    this.now = options?.now ?? (() => Date.now());
  }

  private execSql(sql: string, ...params: SQLQueryBindings[]) {
    return params.length > 0 ? this.db.run(sql, params) : this.db.run(sql);
  }

  close(checkpoint = false): void {
    if (checkpoint) {
      this.execSql("PRAGMA wal_checkpoint(TRUNCATE);");
    }
    this.db.close();
  }

  transaction<T>(fn: () => T): T {
    if (!this.db.inTransaction) {
      return this.db.transaction(fn)();
    }
    // Nested transactions use SQLite savepoints so inner callers remain atomic.
    const sp = `sp_${++this.savepointSeq}`;
    this.execSql(`SAVEPOINT ${sp};`);
    try {
      const result = fn();
      this.execSql(`RELEASE SAVEPOINT ${sp};`);
      return result;
    } catch (err) {
      this.execSql(`ROLLBACK TO SAVEPOINT ${sp};`);
      this.execSql(`RELEASE SAVEPOINT ${sp};`);
      throw err;
    }
  }

  // ---------------------------------------------------------------------------
  // Threads
  // ---------------------------------------------------------------------------

  createThread(thread: Thread): Thread {
    this.execSql(
      `INSERT INTO threads (
        id, space, window_id, pane_id, pty_id, agent, profile, driver, kind,
        state, cwd, canonical_cwd, worktree_path, vendor_session_id, title,
        model, epoch, active_turn_id, created_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      thread.id,
      thread.space ?? null,
      thread.window_id ?? null,
      thread.pane_id ?? null,
      thread.pty_id ?? null,
      thread.agent,
      thread.profile ?? null,
      thread.driver,
      thread.kind,
      thread.state,
      thread.cwd,
      thread.canonical_cwd,
      thread.worktree_path ?? null,
      thread.vendor_session_id ?? null,
      thread.title ?? null,
      thread.model ?? null,
      thread.epoch,
      thread.active_turn_id ?? null,
      thread.created_at_ms,
      thread.updated_at_ms,
    );
    return this.getThread(thread.id)!;
  }

  getThread(id: ThreadId): Thread | null {
    const row = this.db
      .query<ThreadRow, [string]>("SELECT * FROM threads WHERE id = ?")
      .get(id);
    return row ? rowToThread(row) : null;
  }

  listThreads(filter?: ThreadListParams): Thread[] {
    const clauses: string[] = [];
    const params: SQLQueryBindings[] = [];

    if (filter?.space !== undefined) {
      clauses.push("space = ?");
      params.push(filter.space);
    }
    if (filter?.state !== undefined) {
      clauses.push("state = ?");
      params.push(filter.state);
    } else if (!filter?.include_closed) {
      clauses.push("state != 'closed'");
    }
    if (filter?.agent !== undefined) {
      clauses.push("agent = ?");
      params.push(filter.agent);
    }
    if (filter?.driver !== undefined) {
      clauses.push("driver = ?");
      params.push(filter.driver);
    }

    const where = clauses.length > 0 ? `WHERE ${clauses.join(" AND ")}` : "";
    const rows = this.db
      .query<ThreadRow, SQLQueryBindings[]>(
        `SELECT * FROM threads ${where} ORDER BY updated_at_ms DESC, created_at_ms DESC, id ASC`,
      )
      .all(...params);
    return rows.map(rowToThread);
  }

  updateThread(id: ThreadId, patch: ThreadPatch): Thread | null {
    const { sets, params } = buildPatchSets([
      ["space", patch.space],
      ["window_id", patch.window_id],
      ["pane_id", patch.pane_id],
      ["pty_id", patch.pty_id],
      ["agent", patch.agent],
      ["profile", patch.profile],
      ["driver", patch.driver],
      ["kind", patch.kind],
      ["state", patch.state],
      ["cwd", patch.cwd],
      ["canonical_cwd", patch.canonical_cwd],
      ["worktree_path", patch.worktree_path],
      ["vendor_session_id", patch.vendor_session_id],
      ["title", patch.title],
      ["model", patch.model],
      ["epoch", patch.epoch],
      ["active_turn_id", patch.active_turn_id],
    ]);

    if (sets.length === 0 && patch.updated_at_ms === undefined) {
      return this.getThread(id);
    }

    sets.push("updated_at_ms = ?");
    params.push(patch.updated_at_ms ?? this.now());
    params.push(id);

    const res = this.execSql(`UPDATE threads SET ${sets.join(", ")} WHERE id = ?`, ...params);
    if (res.changes === 0) {
      return null;
    }
    return this.getThread(id);
  }

  deleteThread(id: ThreadId): boolean {
    const res = this.execSql("DELETE FROM threads WHERE id = ?", id);
    return res.changes > 0;
  }

  // ---------------------------------------------------------------------------
  // Leases
  // ---------------------------------------------------------------------------

  getLease(threadId: ThreadId): Lease | null {
    const row = this.db
      .query<LeaseRow, [string]>("SELECT * FROM leases WHERE thread_id = ?")
      .get(threadId);
    return row ? rowToLease(row) : null;
  }

  upsertLease(lease: Lease): Lease {
    this.execSql(
      `INSERT INTO leases (
        thread_id, id, epoch, state, owner_json, acquired_at_ms, expires_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(thread_id) DO UPDATE SET
        id = excluded.id,
        epoch = excluded.epoch,
        state = excluded.state,
        owner_json = excluded.owner_json,
        acquired_at_ms = excluded.acquired_at_ms,
        expires_at_ms = excluded.expires_at_ms,
        updated_at_ms = excluded.updated_at_ms`,
      lease.thread_id,
      lease.id,
      lease.epoch,
      lease.state,
      lease.owner ? JSON.stringify(lease.owner) : null,
      lease.acquired_at_ms ?? null,
      lease.expires_at_ms ?? null,
      lease.updated_at_ms,
    );
    return this.getLease(lease.thread_id)!;
  }

  recordLeaseTransition(transition: LeaseTransition): LeaseTransition {
    this.execSql(
      `INSERT INTO lease_transitions (
        thread_id, from_state, to_state, from_epoch, to_epoch, reason, actor_json, ts_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
      transition.thread_id,
      transition.from_state,
      transition.to_state,
      transition.from_epoch,
      transition.to_epoch,
      transition.reason,
      transition.actor ? JSON.stringify(transition.actor) : null,
      transition.ts_ms,
    );
    return { ...transition };
  }

  listLeaseTransitions(threadId: ThreadId): LeaseTransition[] {
    const rows = this.db
      .query<LeaseTransitionRow, [string]>(
        "SELECT * FROM lease_transitions WHERE thread_id = ? ORDER BY id ASC",
      )
      .all(threadId);
    return rows.map(rowToLeaseTransition);
  }

  applyLeaseTransition(
    lease: Lease,
    transition: LeaseTransition,
    event?: EventEnvelope,
  ): LeaseResult {
    return this.transaction(() => {
      const updated = this.execSql(
        "UPDATE threads SET epoch = ?, updated_at_ms = ? WHERE id = ?",
        lease.epoch,
        lease.updated_at_ms,
        lease.thread_id,
      );
      if (updated.changes === 0) {
        throw new Error(`Cannot apply lease transition: thread not found (${lease.thread_id})`);
      }

      const savedLease = this.upsertLease(lease);
      const savedTransition = this.recordLeaseTransition(transition);
      if (event) {
        this.appendEvent(event);
      }
      return {
        lease: savedLease,
        transition: savedTransition,
      };
    });
  }

  // ---------------------------------------------------------------------------
  // Turns
  // ---------------------------------------------------------------------------

  createTurn(turn: Turn): Turn {
    this.execSql(
      `INSERT INTO turns (
        id, thread_id, epoch, origin, state, prompt, reply_preview,
        model, usage_json, error_json, started_at_ms, ended_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      turn.id,
      turn.thread_id,
      turn.epoch,
      turn.origin,
      turn.state,
      turn.prompt ?? null,
      turn.reply_preview ?? null,
      turn.model ?? null,
      turn.usage ? JSON.stringify(turn.usage) : null,
      turn.error ? JSON.stringify(turn.error) : null,
      turn.started_at_ms,
      turn.ended_at_ms ?? null,
    );
    return this.getTurn(turn.id)!;
  }

  getTurn(id: TurnId): Turn | null {
    const row = this.db.query<TurnRow, [string]>("SELECT * FROM turns WHERE id = ?").get(id);
    return row ? rowToTurn(row) : null;
  }

  updateTurn(id: TurnId, patch: TurnPatch): Turn | null {
    const { sets, params } = buildPatchSets([
      ["epoch", patch.epoch],
      ["origin", patch.origin],
      ["state", patch.state],
      ["prompt", patch.prompt],
      ["reply_preview", patch.reply_preview],
      ["model", patch.model],
      [
        "usage_json",
        patch.usage === undefined ? undefined : patch.usage ? JSON.stringify(patch.usage) : null,
      ],
      [
        "error_json",
        patch.error === undefined ? undefined : patch.error ? JSON.stringify(patch.error) : null,
      ],
      ["ended_at_ms", patch.ended_at_ms],
    ]);

    if (sets.length === 0) {
      return this.getTurn(id);
    }

    params.push(id);
    const res = this.execSql(`UPDATE turns SET ${sets.join(", ")} WHERE id = ?`, ...params);
    if (res.changes === 0) {
      return null;
    }
    return this.getTurn(id);
  }

  listTurns(threadId: ThreadId): Turn[] {
    const rows = this.db
      .query<TurnRow, [string]>(
        "SELECT * FROM turns WHERE thread_id = ? ORDER BY started_at_ms ASC, rowid ASC",
      )
      .all(threadId);
    return rows.map(rowToTurn);
  }

  // ---------------------------------------------------------------------------
  // Events & Idempotency
  // ---------------------------------------------------------------------------

  appendEvent(envelope: EventEnvelope): boolean {
    return this.transaction(() => {
      const seenRes = this.execSql(
        "INSERT OR IGNORE INTO seen_events (event_id, seen_at_ms) VALUES (?, ?)",
        envelope.id,
        envelope.ts_ms,
      );
      if (seenRes.changes === 0) {
        return false;
      }

      this.execSql(
        `INSERT INTO events (
          boot_id, seq, id, ts_ms, thread_id, epoch, event_type, payload_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        envelope.boot_id,
        envelope.seq,
        envelope.id,
        envelope.ts_ms,
        envelope.thread_id ?? null,
        envelope.epoch ?? null,
        envelope.payload.type,
        JSON.stringify(envelope.payload),
      );
      return true;
    });
  }

  nextSeq(bootId: BootId): Seq {
    const row = this.db
      .query<{ next_seq: number }, [string]>(
        "SELECT COALESCE(MAX(seq), 0) + 1 AS next_seq FROM events WHERE boot_id = ?",
      )
      .get(bootId);
    return row ? row.next_seq : 1;
  }

  getEventBounds(bootId: BootId): EventBounds {
    const row = this.db
      .query<{ earliest_seq: number | null; latest_seq: number | null }, [string]>(
        "SELECT MIN(seq) AS earliest_seq, MAX(seq) AS latest_seq FROM events WHERE boot_id = ?",
      )
      .get(bootId);
    return {
      earliestSeq: row?.earliest_seq ?? null,
      latestSeq: row?.latest_seq ?? null,
    };
  }

  listEventsAfter(
    bootId: BootId,
    afterSeq: Seq,
    limit: number,
    filter?: Partial<EventSubscriptionFilter>,
  ): EventEnvelope[] {
    if (limit <= 0) {
      return [];
    }
    const clauses: string[] = ["boot_id = ?", "seq > ?"];
    const params: SQLQueryBindings[] = [bootId, afterSeq];

    if (filter?.thread_ids && filter.thread_ids.length > 0) {
      const placeholders = filter.thread_ids.map(() => "?").join(", ");
      clauses.push(`thread_id IN (${placeholders})`);
      params.push(...filter.thread_ids);
    }

    if (filter?.types && filter.types.length > 0) {
      const placeholders = filter.types.map(() => "?").join(", ");
      clauses.push(`event_type IN (${placeholders})`);
      params.push(...filter.types);
    }

    params.push(limit);

    const rows = this.db
      .query<EventRow, SQLQueryBindings[]>(
        `SELECT * FROM events WHERE ${clauses.join(" AND ")} ORDER BY seq ASC LIMIT ?`,
      )
      .all(...params);
    return rows.map(rowToEventEnvelope);
  }

  trimEvents(bootId: BootId, maxRows: number): number {
    if (maxRows <= 0) {
      const res = this.execSql("DELETE FROM events WHERE boot_id = ?", bootId);
      return res.changes;
    }
    const res = this.execSql(
      `DELETE FROM events
       WHERE boot_id = ?
         AND seq NOT IN (
           SELECT seq FROM events WHERE boot_id = ? ORDER BY seq DESC LIMIT ?
         )`,
      bootId,
      bootId,
      maxRows,
    );
    return res.changes;
  }

  // ---------------------------------------------------------------------------
  // Mailbox
  // ---------------------------------------------------------------------------

  enqueueMailbox(msg: MailboxMessage): MailboxMessage {
    return this.transaction(() => {
      if (msg.dedup_key !== undefined) {
        const existing = this.db
          .query<MailboxRow, [string, string]>(
            "SELECT * FROM mailbox WHERE thread_id = ? AND dedup_key = ?",
          )
          .get(msg.thread_id, msg.dedup_key);
        if (existing) {
          return rowToMailboxMessage(existing);
        }
      }

      this.execSql(
        `INSERT INTO mailbox (
          id, thread_id, from_json, kind, priority, status, dedup_key,
          correlation_id, payload_json, created_at_ms, delivered_at_ms, expires_at_ms
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        msg.id,
        msg.thread_id,
        JSON.stringify(msg.from),
        msg.kind,
        msg.priority,
        msg.status,
        msg.dedup_key ?? null,
        msg.correlation_id ?? null,
        JSON.stringify(msg.payload === undefined ? null : msg.payload),
        msg.created_at_ms,
        msg.delivered_at_ms ?? null,
        msg.expires_at_ms ?? null,
      );

      const inserted = this.db
        .query<MailboxRow, [string]>("SELECT * FROM mailbox WHERE id = ?")
        .get(msg.id)!;
      return rowToMailboxMessage(inserted);
    });
  }

  listMailbox(
    threadId: ThreadId,
    status?: MailboxStatus,
    limit?: number,
  ): MailboxMessage[] {
    if (limit !== undefined && limit <= 0) {
      return [];
    }
    const clauses: string[] = ["thread_id = ?"];
    const params: SQLQueryBindings[] = [threadId];

    if (status !== undefined) {
      clauses.push("status = ?");
      params.push(status);
    }

    const limitSql = limit !== undefined ? " LIMIT ?" : "";
    if (limit !== undefined) {
      params.push(limit);
    }

    const rows = this.db
      .query<MailboxRow, SQLQueryBindings[]>(
        `SELECT * FROM mailbox
         WHERE ${clauses.join(" AND ")}
         ORDER BY
           CASE priority
             WHEN 'urgent' THEN 0
             WHEN 'high' THEN 1
             WHEN 'normal' THEN 2
             WHEN 'low' THEN 3
             ELSE 4
           END ASC,
           created_at_ms ASC,
           rowid ASC${limitSql}`,
      )
      .all(...params);
    return rows.map(rowToMailboxMessage);
  }

  ackMailbox(
    id: MailboxMessageId,
    status: MailboxStatus,
    nowMs: TimestampMs = this.now(),
  ): MailboxMessage | null {
    const updateDelivered =
      status === "delivered"
        ? ", delivered_at_ms = ?"
        : status === "acknowledged"
          ? ", delivered_at_ms = COALESCE(delivered_at_ms, ?)"
          : "";
    const params: SQLQueryBindings[] =
      updateDelivered !== "" ? [status, nowMs, id] : [status, id];

    const res = this.execSql(
      `UPDATE mailbox SET status = ?${updateDelivered} WHERE id = ?`,
      ...params,
    );
    if (res.changes === 0) {
      return null;
    }
    const row = this.db
      .query<MailboxRow, [string]>("SELECT * FROM mailbox WHERE id = ?")
      .get(id);
    return row ? rowToMailboxMessage(row) : null;
  }

  // ---------------------------------------------------------------------------
  // Subscriptions
  // ---------------------------------------------------------------------------

  upsertSubscription(sub: MailboxSubscription): MailboxSubscription {
    this.execSql(
      `INSERT INTO subscriptions (id, thread_id, source, filter_json, created_at_ms)
       VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET
         thread_id = excluded.thread_id,
         source = excluded.source,
         filter_json = excluded.filter_json,
         created_at_ms = excluded.created_at_ms`,
      sub.id,
      sub.thread_id,
      sub.source,
      sub.filter !== undefined ? JSON.stringify(sub.filter) : null,
      sub.created_at_ms,
    );
    const row = this.db
      .query<SubscriptionRow, [string]>("SELECT * FROM subscriptions WHERE id = ?")
      .get(sub.id)!;
    return rowToSubscription(row);
  }

  listSubscriptions(threadId?: ThreadId): MailboxSubscription[] {
    if (threadId !== undefined) {
      const rows = this.db
        .query<SubscriptionRow, [string]>(
          "SELECT * FROM subscriptions WHERE thread_id = ? ORDER BY created_at_ms ASC, id ASC",
        )
        .all(threadId);
      return rows.map(rowToSubscription);
    }
    const rows = this.db
      .query<SubscriptionRow, []>(
        "SELECT * FROM subscriptions ORDER BY created_at_ms ASC, id ASC",
      )
      .all();
    return rows.map(rowToSubscription);
  }

  deleteSubscription(id: string): boolean {
    const res = this.execSql("DELETE FROM subscriptions WHERE id = ?", id);
    return res.changes > 0;
  }

  // ---------------------------------------------------------------------------
  // Outcomes (internal/hooks/core invariants)
  // ---------------------------------------------------------------------------

  recordOutcome(
    entry: OutcomeInput,
    nowMs: TimestampMs = this.now(),
  ): RecordOutcomeResult {
    const outcome = normalizeOutcomeValue(entry.outcome);
    if (!outcome) {
      return { recorded: false, notify: false, entry: null };
    }
    const session = entry.session.trim();
    if (!session) {
      return { recorded: false, notify: false, entry: null };
    }
    let agent = entry.agent.trim();
    let app = (entry.app ?? "").trim();
    if (!agent && !app) {
      return { recorded: false, notify: false, entry: null };
    }
    if (!agent) {
      const fromConv = canonicalConversationAgent(app);
      agent = fromConv || canonicalHookAgent(app);
    }
    if (!app) {
      app = displayApp(agent);
    }

    const tsMs = entry.ts_ms && entry.ts_ms > 0 ? entry.ts_ms : nowMs;
    const candidate: OutcomeEntry = {
      session,
      pane: (entry.pane ?? "").trim().replace(/^%/, ""),
      agent,
      app,
      profile: oneLine(entry.profile, MAX_MODEL),
      outcome,
      text: oneLine(entry.text, MAX_TEXT),
      title: oneLine(entry.title, MAX_TEXT),
      model: oneLine(entry.model, MAX_MODEL),
      session_id: (entry.session_id ?? "").trim().slice(0, MAX_SESSION),
      cwd: oneLine(entry.cwd, MAX_CWD),
      ts_ms: tsMs,
    };

    return this.transaction(() => {
      const existingRows = this.db
        .query<OutcomeRow, []>("SELECT * FROM outcomes ORDER BY ts_ms DESC, id DESC")
        .all();
      const existing = existingRows.map(rowToOutcome);

      const convKey = outcomeConversationKey(candidate);
      const entryAgent = outcomeAgentKey(candidate);
      const entryCWD = candidate.cwd.trim();
      const entryTitle = candidate.title.trim().toLowerCase();

      const kept: OutcomeEntry[] = [];
      let mergedRecentSameOutcome = false;

      for (const rec of existing) {
        const recConvKey = outcomeConversationKey(rec);
        const recOutcome = rec.outcome;
        const delta = Math.abs(candidate.ts_ms - rec.ts_ms);

        // One entry per (agent, session_id) conversation, preserving failure
        // against a trailing generic exit "done"/"finished" within 15s.
        if (convKey && recConvKey === convKey) {
          if (
            recOutcome === "failed" &&
            candidate.outcome === "done" &&
            candidate.text.trim().toLowerCase() === "finished" &&
            delta <= OUTCOME_DEDUPE_MS
          ) {
            return { recorded: false, notify: false, entry: null };
          }
          if (recOutcome === candidate.outcome && delta <= OUTCOME_DEDUPE_MS) {
            mergedRecentSameOutcome = true;
          }
          mergeOutcomeMetadata(candidate, rec);
          continue;
        }

        const recAgent = outcomeAgentKey(rec);
        if (rec.session === candidate.session && rec.pane === candidate.pane) {
          if (delta <= OUTCOME_DEDUPE_MS) {
            if (
              recOutcome === "failed" &&
              candidate.outcome === "done" &&
              candidate.text.trim().toLowerCase() === "finished"
            ) {
              return { recorded: false, notify: false, entry: null };
            }
            if (recOutcome === candidate.outcome) {
              if (outcomeRichness(candidate) > outcomeRichness(rec)) {
                mergeOutcomeMetadata(candidate, rec);
                mergedRecentSameOutcome = true;
                continue;
              }
              return { recorded: false, notify: false, entry: null };
            }
          }
          // Supersede unattributed outcome on the same (session, pane, agent).
          if (!recConvKey && entryAgent && recAgent === entryAgent) {
            mergeOutcomeMetadata(candidate, rec);
            continue;
          }
        }

        // Collapse an unattributed OSC outcome and an attributed hook outcome
        // for the same (agent, cwd, title) within 15s.
        if (
          delta <= OUTCOME_DEDUPE_MS &&
          recOutcome === candidate.outcome &&
          entryAgent &&
          recAgent === entryAgent &&
          entryTitle &&
          rec.title.trim().toLowerCase() === entryTitle &&
          (!entryCWD || !rec.cwd.trim() || rec.cwd.trim() === entryCWD)
        ) {
          if (convKey && !recConvKey) {
            mergeOutcomeMetadata(candidate, rec);
            mergedRecentSameOutcome = true;
            continue;
          }
          if (!convKey && recConvKey) {
            return { recorded: false, notify: false, entry: null };
          }
        }

        kept.push(rec);
      }

      const nextList = pruneOutcomesList([candidate, ...kept], nowMs);
      this.execSql("DELETE FROM outcomes");
      const insertStmt = this.db.prepare<
        unknown,
        [string, string, string, string, string, string, string, string, string, string, string, number]
      >(
        `INSERT INTO outcomes (
          session, pane, agent, app, profile, outcome, text, title, model, session_id, cwd, ts_ms
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      );
      for (const item of nextList) {
        insertStmt.run(
          item.session,
          item.pane,
          item.agent,
          item.app,
          item.profile,
          item.outcome,
          item.text,
          item.title,
          item.model,
          item.session_id,
          item.cwd,
          item.ts_ms,
        );
      }

      const persisted = nextList.some(
        (item) =>
          item.ts_ms === candidate.ts_ms &&
          item.session === candidate.session &&
          item.pane === candidate.pane &&
          item.agent === candidate.agent &&
          item.session_id === candidate.session_id,
      );
      const isGenericFinished =
        candidate.outcome === "done" && candidate.text.trim().toLowerCase() === "finished";
      return {
        recorded: persisted,
        notify: persisted && !mergedRecentSameOutcome && !isGenericFinished,
        entry: persisted ? candidate : null,
      };
    });
  }

  listOutcomes(nowMs: TimestampMs = this.now()): OutcomeEntry[] {
    const rows = this.db
      .query<OutcomeRow, []>("SELECT * FROM outcomes ORDER BY ts_ms DESC, id DESC")
      .all();
    return pruneOutcomesList(rows.map(rowToOutcome), nowMs);
  }

  removeOutcome(opts: RemoveOutcomeOptions): number {
    const convKey = outcomeConversationKey(opts);
    const session = (opts.session ?? "").trim();
    const pane = (opts.pane ?? "").trim().replace(/^%/, "");
    const tsMs = opts.ts_ms ?? 0;

    if (!convKey && !session) {
      return 0;
    }

    return this.transaction(() => {
      const rows = this.db.query<OutcomeRow, []>("SELECT * FROM outcomes").all();
      const deleteIds: number[] = [];

      for (const row of rows) {
        const rec = rowToOutcome(row);
        if (convKey) {
          if (outcomeConversationKey(rec) === convKey) {
            deleteIds.push(row.id);
          }
          continue;
        }
        if (
          rec.session === session &&
          (!pane || rec.pane === pane) &&
          (tsMs <= 0 || rec.ts_ms === tsMs)
        ) {
          deleteIds.push(row.id);
        }
      }

      if (deleteIds.length === 0) {
        return 0;
      }
      const placeholders = deleteIds.map(() => "?").join(", ");
      const res = this.execSql(
        `DELETE FROM outcomes WHERE id IN (${placeholders})`,
        ...deleteIds,
      );
      return res.changes;
    });
  }

  // ---------------------------------------------------------------------------
  // Hook Panes (TTL & ATT-5 invariants)
  // ---------------------------------------------------------------------------

  upsertHookPane(pane: HookPaneInput): HookPaneRecord {
    const session = pane.session.trim();
    const paneKey = pane.pane.trim();
    if (!session || !paneKey) {
      throw new Error("hook_panes: session and pane are required");
    }

    const agent = pane.agent.trim();
    const app = (pane.app ?? "").trim() || displayApp(agent);
    const source = (pane.source ?? "").trim() || "hook";
    const tsMs = pane.ts_ms;
    const stateSinceMs = pane.state_since_ms ?? tsMs;
    const turnStartedMs = pane.turn_started_ms ?? 0;

    const children = normalizeHookChildren(pane.children, "finite", "subagent", tsMs);
    const backgroundTasks = normalizeHookChildren(
      pane.background_tasks,
      "finite",
      "command",
      tsMs,
    );

    let state = pane.state;
    let kind = pane.kind ?? null;
    let text = pane.text ?? "";
    let waitingOn = pane.waiting_on ?? null;

    // ATT-5: hold working while active finite background work remains on Stop / idle_prompt.
    if (
      ((pane.event === "stop" && state !== "failed") ||
        (pane.event === "notification" && state === "done")) &&
      !(kind === "auth" && state === "blocked")
    ) {
      const { activeFinite } = collectBackgroundWork(children, backgroundTasks);
      if (activeFinite.length > 0) {
        state = "working";
        kind = null;
        const waiting =
          waitingOn && waitingOn.trim() ? waitingOn.trim() : formatWaitingText(activeFinite);
        waitingOn = waiting;
        text = waiting;
      } else if (state === "done") {
        waitingOn = null;
      }
    }

    this.execSql(
      `INSERT INTO hook_panes (
        session, pane, agent, app, source, state, kind, event,
        ts_ms, state_since_ms, turn_started_ms, text, title, prompt,
        tool_json, reply, model, profile, session_id, cwd, usage_json,
        children_json, background_tasks_json, waiting_on, last_event_json
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(session, pane) DO UPDATE SET
        agent = excluded.agent,
        app = excluded.app,
        source = excluded.source,
        state = excluded.state,
        kind = excluded.kind,
        event = excluded.event,
        ts_ms = excluded.ts_ms,
        state_since_ms = excluded.state_since_ms,
        turn_started_ms = excluded.turn_started_ms,
        text = excluded.text,
        title = excluded.title,
        prompt = excluded.prompt,
        tool_json = excluded.tool_json,
        reply = excluded.reply,
        model = excluded.model,
        profile = excluded.profile,
        session_id = excluded.session_id,
        cwd = excluded.cwd,
        usage_json = excluded.usage_json,
        children_json = excluded.children_json,
        background_tasks_json = excluded.background_tasks_json,
        waiting_on = excluded.waiting_on,
        last_event_json = excluded.last_event_json`,
      session,
      paneKey,
      agent,
      app,
      source,
      state,
      kind,
      pane.event,
      tsMs,
      stateSinceMs,
      turnStartedMs,
      text,
      pane.title ?? "",
      pane.prompt ?? null,
      pane.tool ? JSON.stringify(pane.tool) : null,
      pane.reply ?? null,
      pane.model ?? null,
      pane.profile ?? null,
      pane.session_id ?? null,
      pane.cwd ?? null,
      pane.usage ? JSON.stringify(pane.usage) : null,
      JSON.stringify(children),
      JSON.stringify(backgroundTasks),
      waitingOn,
      pane.last_event ? JSON.stringify(pane.last_event) : null,
    );

    return this.getHookPane(session, paneKey)!;
  }

  getHookPane(session: string, pane: string): HookPaneRecord | null {
    const row = this.db
      .query<HookPaneRow, [string, string]>(
        "SELECT * FROM hook_panes WHERE session = ? AND pane = ?",
      )
      .get(session.trim(), pane.trim());
    return row ? rowToHookPane(row) : null;
  }

  clearHookPane(session: string, pane: string): boolean {
    const res = this.execSql(
      "DELETE FROM hook_panes WHERE session = ? AND pane = ?",
      session.trim(),
      pane.trim(),
    );
    return res.changes > 0;
  }

  listHookPanes(nowMs: TimestampMs = this.now()): HookPaneRecord[] {
    const rows = this.db
      .query<HookPaneRow, []>("SELECT * FROM hook_panes ORDER BY ts_ms DESC, session ASC, pane ASC")
      .all();
    const out: HookPaneRecord[] = [];
    for (const row of rows) {
      const rec = rowToHookPane(row);
      if (isHookPaneStale(rec, nowMs)) {
        continue;
      }
      rec.children = filterCodexTurnChildren(rec);
      out.push(rec);
    }
    return out;
  }

  pruneHookPanes(nowMs: TimestampMs = this.now()): number {
    return this.transaction(() => {
      const rows = this.db.query<HookPaneRow, []>("SELECT * FROM hook_panes").all();
      let pruned = 0;

      for (const row of rows) {
        const rec = rowToHookPane(row);
        if (isHookPaneStale(rec, nowMs)) {
          const res = this.execSql(
            "DELETE FROM hook_panes WHERE session = ? AND pane = ?",
            rec.session,
            rec.pane,
          );
          pruned += res.changes;
          continue;
        }
        const filteredChildren = filterCodexTurnChildren(rec);
        if (filteredChildren.length !== rec.children.length) {
          this.execSql(
            "UPDATE hook_panes SET children_json = ? WHERE session = ? AND pane = ?",
            JSON.stringify(filteredChildren),
            rec.session,
            rec.pane,
          );
        }
      }

      return pruned;
    });
  }

  // ---------------------------------------------------------------------------
  // Conversation Overlay
  // ---------------------------------------------------------------------------

  upsertConversationOverlay(
    entry: ConversationOverlayInput,
  ): ConversationOverlayRecord | null {
    const agent = canonicalConversationAgent(entry.agent);
    const sessionId = entry.session_id.trim();
    if (!agent || !sessionId) {
      return null;
    }

    const lastOpenedMs = entry.last_opened_ms ?? 0;
    const updatedAtMs =
      entry.updated_at_ms && entry.updated_at_ms > 0
        ? entry.updated_at_ms
        : lastOpenedMs > 0
          ? lastOpenedMs
          : this.now();
    const launchCmdJson =
      entry.launch_cmd && entry.launch_cmd.length > 0
        ? JSON.stringify(entry.launch_cmd)
        : "";

    this.execSql(
      `INSERT INTO conversation_overlay (
        agent, session_id, space, last_opened_ms, last_outcome,
        profile, cwd, title, launch_cmd_json, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(agent, session_id) DO UPDATE SET
        space = CASE WHEN excluded.space != '' THEN excluded.space ELSE conversation_overlay.space END,
        last_opened_ms = MAX(conversation_overlay.last_opened_ms, excluded.last_opened_ms),
        last_outcome = CASE WHEN excluded.last_outcome != '' THEN excluded.last_outcome ELSE conversation_overlay.last_outcome END,
        profile = CASE WHEN excluded.profile != '' THEN excluded.profile ELSE conversation_overlay.profile END,
        cwd = CASE WHEN excluded.cwd != '' THEN excluded.cwd ELSE conversation_overlay.cwd END,
        title = CASE WHEN excluded.title != '' THEN excluded.title ELSE conversation_overlay.title END,
        launch_cmd_json = CASE WHEN excluded.launch_cmd_json != '' THEN excluded.launch_cmd_json ELSE conversation_overlay.launch_cmd_json END,
        updated_at_ms = MAX(conversation_overlay.updated_at_ms, excluded.updated_at_ms)`,
      agent,
      sessionId,
      (entry.space ?? "").trim(),
      lastOpenedMs,
      (entry.last_outcome ?? "").trim(),
      (entry.profile ?? "").trim(),
      (entry.cwd ?? "").trim(),
      (entry.title ?? "").trim(),
      launchCmdJson,
      updatedAtMs,
    );

    const row = this.db
      .query<ConversationOverlayRow, [string, string]>(
        "SELECT * FROM conversation_overlay WHERE agent = ? AND session_id = ?",
      )
      .get(agent, sessionId);
    return row ? rowToOverlay(row) : null;
  }

  getConversationLaunchCmd(agent: string, sessionId: string): string[] | null {
    const canon = canonicalConversationAgent(agent);
    const sid = sessionId.trim();
    if (!canon || !sid) {
      return null;
    }
    const row = this.db
      .query<{ launch_cmd_json: string }, [string, string]>(
        "SELECT launch_cmd_json FROM conversation_overlay WHERE agent = ? AND session_id = ?",
      )
      .get(canon, sid);
    return row ? decodeLaunchCmdJson(row.launch_cmd_json) : null;
  }

  listConversationOverlays(): ConversationOverlayRecord[] {
    const rows = this.db
      .query<ConversationOverlayRow, []>(
        `SELECT * FROM conversation_overlay
         ORDER BY updated_at_ms DESC, last_opened_ms DESC, agent ASC, session_id ASC`,
      )
      .all();
    return rows.map(rowToOverlay);
  }

  // ---------------------------------------------------------------------------
  // Resurrect (internal/resurrect semantics)
  // ---------------------------------------------------------------------------

  saveResurrectSpace(snap: ResurrectSpaceSnapshot): ResurrectSpaceSnapshot {
    const name = snap.name.trim();
    if (!name) {
      throw new Error("resurrect: space name is empty");
    }
    const version = snap.version && snap.version > 0 ? snap.version : 1;
    const savedAtMs = snap.saved_at_ms && snap.saved_at_ms > 0 ? snap.saved_at_ms : this.now();

    this.execSql(
      `INSERT INTO resurrect_spaces (
        name, version, saved_at_ms, cwd, worktree_path, worktree_name,
        worktree_base, main_repo_root, active_window_index, windows_json
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(name) DO UPDATE SET
        version = excluded.version,
        saved_at_ms = excluded.saved_at_ms,
        cwd = excluded.cwd,
        worktree_path = excluded.worktree_path,
        worktree_name = excluded.worktree_name,
        worktree_base = excluded.worktree_base,
        main_repo_root = excluded.main_repo_root,
        active_window_index = excluded.active_window_index,
        windows_json = excluded.windows_json`,
      name,
      version,
      savedAtMs,
      snap.cwd ?? "",
      snap.worktree_path ?? "",
      snap.worktree_name ?? "",
      snap.worktree_base ?? "",
      snap.main_repo_root ?? "",
      snap.active_window_index ?? 0,
      JSON.stringify(snap.windows ?? []),
    );

    return this.getResurrectSpace(name)!;
  }

  getResurrectSpace(name: string): ResurrectSpaceSnapshot | null {
    const row = this.db
      .query<ResurrectSpaceRow, [string]>("SELECT * FROM resurrect_spaces WHERE name = ?")
      .get(name.trim());
    return row ? rowToResurrectSpace(row) : null;
  }

  deleteResurrectSpace(name: string): boolean {
    const trimmed = name.trim();
    if (!trimmed) {
      return false;
    }
    return this.transaction(() => {
      const spaceRes = this.execSql("DELETE FROM resurrect_spaces WHERE name = ?", trimmed);
      const removedOpen = this.removeOpenSpace(trimmed);
      return spaceRes.changes > 0 || removedOpen;
    });
  }

  listResurrectSpaces(): ResurrectSpaceSnapshot[] {
    const rows = this.db
      .query<ResurrectSpaceRow, []>(
        "SELECT * FROM resurrect_spaces ORDER BY saved_at_ms DESC, LOWER(name) ASC",
      )
      .all();
    return rows.map(rowToResurrectSpace);
  }

  listExitedSpaces(
    runningNames: Iterable<string> | Record<string, boolean>,
  ): ExitedSpace[] {
    const running = normalizeRunningNames(runningNames);
    const all = this.listResurrectSpaces();
    const out: ExitedSpace[] = [];

    for (const snap of all) {
      if (running.has(snap.name)) {
        continue;
      }
      let paneCount = 0;
      const agentSeen = new Set<string>();
      const agents: string[] = [];
      for (const win of snap.windows) {
        paneCount += win.panes.length;
        for (const pane of win.panes) {
          if (pane.agent && !agentSeen.has(pane.agent)) {
            agentSeen.add(pane.agent);
            agents.push(pane.agent);
          }
        }
      }
      out.push({
        name: snap.name,
        saved_at_ms: snap.saved_at_ms ?? 0,
        cwd: snap.cwd ?? "",
        window_count: snap.windows.length,
        pane_count: paneCount,
        agents,
      });
    }

    return out;
  }

  getOpenState(): ResurrectOpenState | null {
    const row = this.db
      .query<ResurrectOpenStateRow, []>("SELECT * FROM resurrect_open_state WHERE id = 1")
      .get();
    if (!row) {
      return null;
    }
    return {
      version: row.version,
      saved_at_ms: row.saved_at_ms,
      active_session: row.active_session,
      spaces: parseJson<string[]>(row.spaces_json || "[]"),
    };
  }

  saveOpenState(
    state: Omit<ResurrectOpenState, "version" | "saved_at_ms" | "active_session"> & {
      version?: number;
      saved_at_ms?: TimestampMs;
      active_session?: string;
    },
  ): ResurrectOpenState {
    const version = state.version && state.version > 0 ? state.version : 1;
    const savedAtMs =
      state.saved_at_ms && state.saved_at_ms > 0 ? state.saved_at_ms : this.now();
    const activeSession = (state.active_session ?? "").trim();
    const spaces = dedupeSpaceNames(state.spaces);

    this.execSql(
      `INSERT INTO resurrect_open_state (id, version, saved_at_ms, active_session, spaces_json)
       VALUES (1, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET
         version = excluded.version,
         saved_at_ms = excluded.saved_at_ms,
         active_session = excluded.active_session,
         spaces_json = excluded.spaces_json`,
      version,
      savedAtMs,
      activeSession,
      JSON.stringify(spaces),
    );

    return this.getOpenState()!;
  }

  addOpenSpace(name: string, activeSession?: boolean | string): ResurrectOpenState | null {
    const trimmed = name.trim();
    if (!trimmed) {
      return this.getOpenState();
    }
    return this.transaction(() => {
      const current = this.getOpenState() ?? {
        version: 1,
        saved_at_ms: 0,
        active_session: "",
        spaces: [],
      };
      const spaces = current.spaces.includes(trimmed)
        ? [...current.spaces]
        : [...current.spaces, trimmed];

      let nextActive = current.active_session;
      if (typeof activeSession === "string") {
        if (activeSession.trim()) {
          nextActive = activeSession.trim();
        }
      } else if (activeSession === true || !nextActive) {
        nextActive = trimmed;
      }

      return this.saveOpenState({
        version: current.version,
        saved_at_ms: this.now(),
        active_session: nextActive,
        spaces,
      });
    });
  }

  removeOpenSpace(name: string): boolean {
    const trimmed = name.trim();
    if (!trimmed) {
      return false;
    }
    return this.transaction(() => {
      const current = this.getOpenState();
      if (!current) {
        return false;
      }
      const kept = current.spaces.filter((sp) => sp !== trimmed);
      let changed = kept.length !== current.spaces.length;
      let activeSession = current.active_session;
      if (activeSession === trimmed) {
        activeSession = kept[0] ?? "";
        changed = true;
      }
      if (!changed) {
        return false;
      }
      this.saveOpenState({
        version: current.version,
        saved_at_ms: this.now(),
        active_session: activeSession,
        spaces: kept,
      });
      return true;
    });
  }

  renameOpenSpace(oldName: string, newName: string): boolean {
    const prev = oldName.trim();
    const next = newName.trim();
    if (!prev || !next || prev === next) {
      return false;
    }

    return this.transaction(() => {
      let changed = false;

      const snap = this.getResurrectSpace(prev);
      if (snap) {
        this.saveResurrectSpace({ ...snap, name: next });
        this.execSql("DELETE FROM resurrect_spaces WHERE name = ?", prev);
        changed = true;
      }

      const open = this.getOpenState();
      if (open) {
        let openChanged = false;
        const mappedSpaces = open.spaces.map((sp) => {
          if (sp === prev) {
            openChanged = true;
            return next;
          }
          return sp;
        });
        let activeSession = open.active_session;
        if (activeSession === prev) {
          activeSession = next;
          openChanged = true;
        }
        if (openChanged) {
          this.saveOpenState({
            version: open.version,
            saved_at_ms: this.now(),
            active_session: activeSession,
            spaces: mappedSpaces,
          });
          changed = true;
        }
      }

      return changed;
    });
  }

  // ---------------------------------------------------------------------------
  // Pane HMAC Tokens & Verified OSC 7577 Hints
  // ---------------------------------------------------------------------------

  upsertPaneToken(
    paneId: string,
    secretHex: string,
    threadId?: ThreadId | null,
    nowMs: TimestampMs = this.now(),
  ): PaneHmacTokenRecord {
    return this.transaction(() => {
      const existing = this.getPaneToken(paneId);
      if (!existing) {
        this.execSql(
          `INSERT INTO pane_hmac_tokens (
            pane_id, thread_id, secret_hex, created_at_ms, rotated_at_ms, revoked_at_ms
          ) VALUES (?, ?, ?, ?, NULL, NULL)`,
          paneId,
          threadId ?? null,
          secretHex,
          nowMs,
        );
        return this.getPaneToken(paneId)!;
      }

      const nextThreadId =
        threadId === undefined ? (existing.thread_id ?? null) : threadId;
      const rotatedAtMs =
        existing.secret_hex !== secretHex ? nowMs : (existing.rotated_at_ms ?? null);

      this.execSql(
        `UPDATE pane_hmac_tokens
         SET thread_id = ?, secret_hex = ?, rotated_at_ms = ?, revoked_at_ms = NULL
         WHERE pane_id = ?`,
        nextThreadId,
        secretHex,
        rotatedAtMs,
        paneId,
      );
      return this.getPaneToken(paneId)!;
    });
  }

  getPaneToken(paneId: string): PaneHmacTokenRecord | null {
    const row = this.db
      .query<PaneHmacTokenRow, [string]>("SELECT * FROM pane_hmac_tokens WHERE pane_id = ?")
      .get(paneId);
    return row ? rowToPaneToken(row) : null;
  }

  revokePaneToken(paneId: string, nowMs: TimestampMs = this.now()): boolean {
    const res = this.execSql(
      "UPDATE pane_hmac_tokens SET revoked_at_ms = ? WHERE pane_id = ?",
      nowMs,
      paneId,
    );
    return res.changes > 0;
  }

  upsertVerifiedResumeHint(record: VerifiedResumeHintInput): VerifiedResumeHintRecord {
    const updatedAtMs = record.updated_at_ms ?? record.verified_at_ms;
    this.execSql(
      `INSERT INTO verified_resume_hints (
        pane_id, thread_id, agent, session_id, profile, cwd, resume_target,
        resume_cmd_json, signature_hex, verified_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(pane_id) DO UPDATE SET
        thread_id = excluded.thread_id,
        agent = excluded.agent,
        session_id = excluded.session_id,
        profile = excluded.profile,
        cwd = excluded.cwd,
        resume_target = excluded.resume_target,
        resume_cmd_json = excluded.resume_cmd_json,
        signature_hex = excluded.signature_hex,
        verified_at_ms = excluded.verified_at_ms,
        updated_at_ms = excluded.updated_at_ms`,
      record.pane_id,
      record.thread_id ?? null,
      record.agent,
      record.session_id,
      record.profile ?? null,
      record.cwd ?? null,
      record.resume_target ?? null,
      record.resume_cmd ? JSON.stringify(record.resume_cmd) : null,
      record.signature_hex,
      record.verified_at_ms,
      updatedAtMs,
    );
    return this.getVerifiedResumeHint(record.pane_id)!;
  }

  getVerifiedResumeHint(paneId: string): VerifiedResumeHintRecord | null {
    const row = this.db
      .query<VerifiedResumeHintRow, [string]>(
        "SELECT * FROM verified_resume_hints WHERE pane_id = ?",
      )
      .get(paneId);
    return row ? rowToVerifiedHint(row) : null;
  }

  upsertWorktree(rec: WorktreeRecord): WorktreeRecord {
    this.execSql(
      `INSERT INTO worktrees (
        path, repo_key, main_repo_root, repo_name, name, branch, base_branch,
        managed, name_source, created_at_ms, last_used_at_ms,
        origin_space, origin_window_id, origin_pane_id
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(path) DO UPDATE SET
        repo_key = excluded.repo_key,
        main_repo_root = excluded.main_repo_root,
        repo_name = excluded.repo_name,
        name = excluded.name,
        branch = excluded.branch,
        base_branch = excluded.base_branch,
        managed = excluded.managed,
        name_source = excluded.name_source,
        created_at_ms = excluded.created_at_ms,
        last_used_at_ms = excluded.last_used_at_ms,
        origin_space = excluded.origin_space,
        origin_window_id = excluded.origin_window_id,
        origin_pane_id = excluded.origin_pane_id`,
      rec.path,
      rec.repo_key,
      rec.main_repo_root,
      rec.repo_name,
      rec.name,
      rec.branch,
      rec.base_branch ?? null,
      rec.managed ? 1 : 0,
      rec.name_source ?? null,
      rec.created_at_ms,
      rec.last_used_at_ms ?? null,
      rec.origin_space ?? null,
      rec.origin_window_id ?? null,
      rec.origin_pane_id ?? null,
    );
    return this.getWorktreeByPath(rec.path)!;
  }

  getWorktreeByPath(wtPath: string): WorktreeRecord | null {
    const row = this.db
      .query<WorktreeRow, [string]>("SELECT * FROM worktrees WHERE path = ?")
      .get(wtPath);
    return row ? rowToWorktree(row) : null;
  }

  deleteWorktreeByPath(wtPath: string): boolean {
    const res = this.execSql("DELETE FROM worktrees WHERE path = ?", wtPath);
    return res.changes > 0;
  }

  listWorktrees(): WorktreeRecord[] {
    const rows = this.db
      .query<WorktreeRow, []>(
        "SELECT * FROM worktrees ORDER BY created_at_ms DESC, path ASC",
      )
      .all();
    return rows.map(rowToWorktree);
  }

  listWorktreesByRepo(repoKey: string): WorktreeRecord[] {
    const rows = this.db
      .query<WorktreeRow, [string]>(
        "SELECT * FROM worktrees WHERE repo_key = ? ORDER BY created_at_ms DESC, path ASC",
      )
      .all(repoKey);
    return rows.map(rowToWorktree);
  }

  pruneMissingWorktrees(): string[] {
    return this.transaction(() => {
      const all = this.listWorktrees();
      const removed: string[] = [];
      for (const rec of all) {
        if (!existsSync(rec.path)) {
          this.execSql("DELETE FROM worktrees WHERE path = ?", rec.path);
          removed.push(rec.path);
        }
      }
      return removed;
    });
  }
}

interface WorktreeRow {
  path: string;
  repo_key: string;
  main_repo_root: string;
  repo_name: string;
  name: string;
  branch: string;
  base_branch: string | null;
  managed: number;
  name_source: string | null;
  created_at_ms: number;
  last_used_at_ms: number | null;
  origin_space: string | null;
  origin_window_id: string | null;
  origin_pane_id: string | null;
}

function rowToWorktree(row: WorktreeRow): WorktreeRecord {
  const out: WorktreeRecord = {
    path: row.path,
    repo_key: row.repo_key,
    main_repo_root: row.main_repo_root,
    repo_name: row.repo_name,
    name: row.name,
    branch: row.branch,
    managed: row.managed !== 0,
    created_at_ms: row.created_at_ms,
  };
  assignOptional(out, "base_branch", row.base_branch);
  assignOptional(out, "name_source", row.name_source);
  assignOptional(out, "last_used_at_ms", row.last_used_at_ms);
  assignOptional(out, "origin_space", row.origin_space);
  assignOptional(out, "origin_window_id", row.origin_window_id);
  assignOptional(out, "origin_pane_id", row.origin_pane_id);
  return out;
}

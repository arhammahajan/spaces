import { Database } from "bun:sqlite";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname, resolve, sep } from "node:path";

export const SCHEMA_VERSION = "v1" as const;

interface Migration {
  readonly version: string;
  readonly statements: readonly string[];
}

const V1_STATEMENTS: readonly string[] = [
  // 1. Managed threads
  `CREATE TABLE IF NOT EXISTS threads (
    id TEXT PRIMARY KEY,
    space TEXT,
    window_id TEXT,
    pane_id TEXT,
    pty_id TEXT,
    agent TEXT NOT NULL,
    profile TEXT,
    driver TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    cwd TEXT NOT NULL,
    canonical_cwd TEXT NOT NULL,
    worktree_path TEXT,
    vendor_session_id TEXT,
    title TEXT,
    model TEXT,
    epoch INTEGER NOT NULL DEFAULT 0,
    active_turn_id TEXT,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
  );`,
  `CREATE INDEX IF NOT EXISTS idx_threads_space ON threads(space);`,
  `CREATE INDEX IF NOT EXISTS idx_threads_state ON threads(state);`,
  `CREATE INDEX IF NOT EXISTS idx_threads_pane_id ON threads(pane_id);`,
  `CREATE INDEX IF NOT EXISTS idx_threads_pty_id ON threads(pty_id);`,
  `CREATE INDEX IF NOT EXISTS idx_threads_vendor_session ON threads(agent, vendor_session_id);`,

  // 2. Ownership leases
  `CREATE TABLE IF NOT EXISTS leases (
    thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    owner_json TEXT,
    acquired_at_ms INTEGER,
    expires_at_ms INTEGER,
    updated_at_ms INTEGER NOT NULL
  );`,

  // 3. Lease FSM transition audit log
  `CREATE TABLE IF NOT EXISTS lease_transitions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    from_epoch INTEGER NOT NULL,
    to_epoch INTEGER NOT NULL,
    reason TEXT NOT NULL,
    actor_json TEXT,
    ts_ms INTEGER NOT NULL
  );`,
  `CREATE INDEX IF NOT EXISTS idx_lease_transitions_thread ON lease_transitions(thread_id, id);`,

  // 4. Execution turns
  `CREATE TABLE IF NOT EXISTS turns (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    epoch INTEGER NOT NULL,
    origin TEXT NOT NULL,
    state TEXT NOT NULL,
    prompt TEXT,
    reply_preview TEXT,
    model TEXT,
    usage_json TEXT,
    error_json TEXT,
    started_at_ms INTEGER NOT NULL,
    ended_at_ms INTEGER
  );`,
  `CREATE INDEX IF NOT EXISTS idx_turns_thread ON turns(thread_id, started_at_ms);`,

  // 5. Monotonic per-boot event log
  `CREATE TABLE IF NOT EXISTS events (
    boot_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    id TEXT NOT NULL UNIQUE,
    ts_ms INTEGER NOT NULL,
    thread_id TEXT,
    epoch INTEGER,
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (boot_id, seq)
  );`,
  `CREATE INDEX IF NOT EXISTS idx_events_thread ON events(thread_id, boot_id, seq);`,
  `CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type, boot_id, seq);`,

  // 6. Idempotent event deduplication ledger
  `CREATE TABLE IF NOT EXISTS seen_events (
    event_id TEXT PRIMARY KEY,
    seen_at_ms INTEGER NOT NULL
  );`,

  // 7. Durable thread mailbox
  `CREATE TABLE IF NOT EXISTS mailbox (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    from_json TEXT NOT NULL,
    kind TEXT NOT NULL,
    priority TEXT NOT NULL,
    status TEXT NOT NULL,
    dedup_key TEXT,
    correlation_id TEXT,
    payload_json TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    delivered_at_ms INTEGER,
    expires_at_ms INTEGER
  );`,
  `CREATE UNIQUE INDEX IF NOT EXISTS idx_mailbox_thread_dedup ON mailbox(thread_id, dedup_key) WHERE dedup_key IS NOT NULL;`,
  `CREATE INDEX IF NOT EXISTS idx_mailbox_thread_status ON mailbox(thread_id, status, created_at_ms);`,

  // 8. Mailbox subscriptions
  `CREATE TABLE IF NOT EXISTS subscriptions (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    filter_json TEXT,
    created_at_ms INTEGER NOT NULL
  );`,
  `CREATE INDEX IF NOT EXISTS idx_subscriptions_thread ON subscriptions(thread_id);`,

  // 9. Recent completion / failure outcomes
  `CREATE TABLE IF NOT EXISTS outcomes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session TEXT NOT NULL,
    pane TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL,
    app TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    ts_ms INTEGER NOT NULL
  );`,
  `CREATE INDEX IF NOT EXISTS idx_outcomes_ts ON outcomes(ts_ms DESC);`,
  `CREATE INDEX IF NOT EXISTS idx_outcomes_agent_sid ON outcomes(agent, session_id);`,

  // 10. Live hook pane telemetry records
  `CREATE TABLE IF NOT EXISTS hook_panes (
    session TEXT NOT NULL,
    pane TEXT NOT NULL,
    agent TEXT NOT NULL,
    app TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'hook',
    state TEXT NOT NULL,
    kind TEXT,
    event TEXT NOT NULL,
    ts_ms INTEGER NOT NULL,
    state_since_ms INTEGER NOT NULL,
    turn_started_ms INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    prompt TEXT,
    tool_json TEXT,
    reply TEXT,
    model TEXT,
    profile TEXT,
    session_id TEXT,
    cwd TEXT,
    usage_json TEXT,
    children_json TEXT NOT NULL DEFAULT '[]',
    background_tasks_json TEXT NOT NULL DEFAULT '[]',
    waiting_on TEXT,
    last_event_json TEXT,
    PRIMARY KEY (session, pane)
  );`,

  // 11. Conversation overlay metadata
  `CREATE TABLE IF NOT EXISTS conversation_overlay (
    agent TEXT NOT NULL,
    session_id TEXT NOT NULL,
    space TEXT NOT NULL DEFAULT '',
    last_opened_ms INTEGER NOT NULL DEFAULT 0,
    last_outcome TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    launch_cmd_json TEXT NOT NULL DEFAULT '',
    updated_at_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (agent, session_id)
  );`,

  // 12. Space snapshots for resurrect
  `CREATE TABLE IF NOT EXISTS resurrect_spaces (
    name TEXT PRIMARY KEY,
    version INTEGER NOT NULL DEFAULT 1,
    saved_at_ms INTEGER NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    worktree_path TEXT NOT NULL DEFAULT '',
    worktree_name TEXT NOT NULL DEFAULT '',
    worktree_base TEXT NOT NULL DEFAULT '',
    main_repo_root TEXT NOT NULL DEFAULT '',
    active_window_index INTEGER NOT NULL DEFAULT 0,
    windows_json TEXT NOT NULL
  );`,

  // 13. Singleton open-spaces tracking for crash-recovery auto-restore
  `CREATE TABLE IF NOT EXISTS resurrect_open_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL DEFAULT 1,
    saved_at_ms INTEGER NOT NULL,
    active_session TEXT NOT NULL DEFAULT '',
    spaces_json TEXT NOT NULL
  );`,

  // 14. Per-pane HMAC secrets for authenticating OSC 7577 resume hints
  `CREATE TABLE IF NOT EXISTS pane_hmac_tokens (
    pane_id TEXT PRIMARY KEY,
    thread_id TEXT,
    secret_hex TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL,
    rotated_at_ms INTEGER,
    revoked_at_ms INTEGER
  );`,

  // 15. Verified OSC 7577 resume hints
  `CREATE TABLE IF NOT EXISTS verified_resume_hints (
    pane_id TEXT PRIMARY KEY,
    thread_id TEXT,
    agent TEXT NOT NULL,
    session_id TEXT NOT NULL,
    profile TEXT,
    cwd TEXT,
    resume_target TEXT,
    resume_cmd_json TEXT,
    signature_hex TEXT NOT NULL,
    verified_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
  );`,
];

const MIGRATIONS: readonly Migration[] = [
  {
    version: SCHEMA_VERSION,
    statements: V1_STATEMENTS,
  },
];

function isMemoryPath(dbPath: string): boolean {
  return dbPath === "" || dbPath === ":memory:" || dbPath.startsWith("file::memory:");
}

/**
 * Creates parent directories with mode 0o700, bypassing process umask for
 * every newly created directory while leaving pre-existing ancestors untouched.
 */
function ensurePrivateParentDir(filePath: string): void {
  const parentDir = resolve(dirname(filePath));
  if (existsSync(parentDir)) {
    return;
  }
  const firstCreated = mkdirSync(parentDir, { recursive: true, mode: 0o700 });
  if (!firstCreated) {
    return;
  }
  const createdRoot = resolve(firstCreated);
  let cur = parentDir;
  while (true) {
    chmodSync(cur, 0o700);
    if (cur === createdRoot) {
      break;
    }
    const next = dirname(cur);
    if (next === cur || !cur.startsWith(createdRoot + sep)) {
      break;
    }
    cur = next;
  }
}

function enforceDbFilePermissions(dbPath: string): void {
  if (existsSync(dbPath)) {
    chmodSync(dbPath, 0o600);
  }
  for (const suffix of ["-wal", "-shm"]) {
    const sidecar = `${dbPath}${suffix}`;
    if (existsSync(sidecar)) {
      chmodSync(sidecar, 0o600);
    }
  }
}

function applyMigrations(db: Database): void {
  db.run(`
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version TEXT PRIMARY KEY,
      applied_at_ms INTEGER NOT NULL
    );
  `);

  const appliedRows = db
    .query<{ version: string }, []>("SELECT version FROM schema_migrations")
    .all();
  const applied = new Set(appliedRows.map((row) => row.version));

  const insertMigration = db.prepare<unknown, [string, number]>(
    "INSERT INTO schema_migrations (version, applied_at_ms) VALUES (?, ?)",
  );

  for (const migration of MIGRATIONS) {
    if (applied.has(migration.version)) {
      continue;
    }
    const runMigration = db.transaction(() => {
      for (const stmt of migration.statements) {
        db.run(stmt);
      }
      insertMigration.run(migration.version, Date.now());
    });
    runMigration();
  }
}

/**
 * Opens (or creates) the unified spacesd SQLite database in WAL mode with
 * strict private permissions (0o700 dir, 0o600 file) and applies schema_migrations.
 */
export function openSpacesDatabase(dbPath: string): Database {
  const inMemory = isMemoryPath(dbPath);
  if (!inMemory) {
    ensurePrivateParentDir(dbPath);
  }

  const db = new Database(dbPath, { create: true });

  if (!inMemory) {
    enforceDbFilePermissions(dbPath);
  }

  db.run("PRAGMA journal_mode = WAL;");
  db.run("PRAGMA busy_timeout = 5000;");
  db.run("PRAGMA synchronous = NORMAL;");
  db.run("PRAGMA foreign_keys = ON;");

  applyMigrations(db);

  if (!inMemory) {
    enforceDbFilePermissions(dbPath);
  }

  return db;
}

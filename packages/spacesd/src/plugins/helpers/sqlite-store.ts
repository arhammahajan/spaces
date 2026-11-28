import { Database } from "bun:sqlite";
import { existsSync } from "node:fs";

/**
 * Opens a vendor SQLite database in read-only mode (no creation, no schema mutation),
 * runs `fn`, and guarantees clean closure. Returns `null` if the DB doesn't exist or
 * cannot be queried.
 */
export function withReadOnlySqlite<T>(dbPath: string, fn: (db: Database) => T): T | null {
  if (!existsSync(dbPath)) {
    return null;
  }
  let db: Database | null = null;
  try {
    db = new Database(dbPath, { readonly: true, create: false });
    return fn(db);
  } catch {
    return null;
  } finally {
    try {
      db?.close();
    } catch {
      // Ignore close errors.
    }
  }
}

export function sqliteTableExists(db: Database, tableName: string): boolean {
  const row = db
    .query<{ name: string }, [string]>(
      "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
    )
    .get(tableName);
  return Boolean(row);
}

export function normalizeEpochMs(value: unknown): number {
  if (typeof value === "number" && Number.isFinite(value) && value > 0) {
    return value > 1e12 ? Math.trunc(value) : Math.trunc(value * 1000);
  }
  if (typeof value === "string" && value.trim()) {
    const num = Number(value);
    if (Number.isFinite(num) && num > 0) {
      return num > 1e12 ? Math.trunc(num) : Math.trunc(num * 1000);
    }
    const parsed = Date.parse(value);
    if (!Number.isNaN(parsed)) return parsed;
  }
  return 0;
}

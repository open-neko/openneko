/**
 * Metadata DB client.
 * Drizzle ORM bound to the Neko metadata Postgres.
 */

import { drizzle, type NodePgDatabase } from "drizzle-orm/node-postgres";
import pg from "pg";
import { buildConnectionString } from "./connection";
import * as schema from "./schema";

export { schema };
export * from "./schema";
export { getOrgId, _resetOrgIdCacheForTesting } from "./org";
export {
  readLocalConfig,
  writeLocalConfig,
  hasCustomPassword,
  localConfigPath,
  type LocalConfig,
  type LocalPgConfig,
} from "./local-config";
export {
  and,
  asc,
  desc,
  eq,
  gt,
  gte,
  inArray,
  isNull,
  isNotNull,
  lt,
  lte,
  ne,
  or,
  sql,
} from "drizzle-orm";

let _pool: pg.Pool | null = null;
let _db: NodePgDatabase<typeof schema> | null = null;

export function pool(): pg.Pool {
  if (_pool) return _pool;
  _pool = new pg.Pool({ connectionString: buildConnectionString(), max: 10 });
  return _pool;
}

export function db(): NodePgDatabase<typeof schema> {
  if (_db) return _db;
  _db = drizzle(pool(), { schema });
  return _db;
}

/**
 * Drain and reset the connection pool so the next `db()` / `pool()` call
 * builds a fresh one against the current config (e.g. after the admin
 * changes the DB password via /setup).
 *
 * Safe to await even if the pool was never created.
 */
export async function reconnectPool(): Promise<void> {
  if (_pool) {
    try {
      await _pool.end();
    } catch {
      // Pool already closing or closed — fine.
    }
  }
  _pool = null;
  _db = null;
}

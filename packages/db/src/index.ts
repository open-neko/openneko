/**
 * Metadata DB client.
 * Drizzle ORM bound to the Neko metadata Postgres.
 */

import { drizzle, type NodePgDatabase } from "drizzle-orm/node-postgres";
import pg from "pg";
import { buildPoolConfig } from "./connection";
import * as schema from "./schema";

export { schema };
export * from "./schema";
export { buildPoolConfig } from "./connection";
export { createNotifyClient, type NotifyClient } from "./notify";
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
  DEFAULT_INSTALL_POLICY,
  INSTALL_POLICY_SCOPE,
  OFFICIAL_MARKETPLACE_URL,
  getInstallPolicyForOrg,
  isInstallSourceAllowed,
  policyFromConfig,
  type InstallPolicy,
} from "./install-policy";
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

// Self-heal after .end() — vitest singleFork shares this singleton across
// every test file in a workspace; once one file's afterAll calls
// `pool().end()`, every later file gets a dead pool unless we recreate.
function isPoolUsable(p: pg.Pool | null): p is pg.Pool {
  if (!p) return false;
  const flags = p as pg.Pool & { ended?: boolean; ending?: boolean };
  return !flags.ended && !flags.ending;
}

export function pool(): pg.Pool {
  if (isPoolUsable(_pool)) return _pool;
  _pool = new pg.Pool(buildPoolConfig());
  _db = null;
  return _pool;
}

export function db(): NodePgDatabase<typeof schema> {
  // Re-resolve through pool() so a closed pool gets rebuilt + the drizzle
  // wrapper rebound. Otherwise _db could hold a reference to a dead pool.
  const live = pool();
  if (_db && (_db as { client?: unknown }).client === live) return _db;
  _db = drizzle(live, { schema });
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

/**
 * Single-tenant admin-org resolver.
 *
 * Returns the id of the (currently one) organization row, creating it on
 * first call when the table is empty. Result is cached per-process.
 *
 * Both web and worker import this so the "current org" is consistent
 * regardless of which side of the app is running. When SSO/multi-tenant
 * lands the web side switches to session-derived org ids; the worker stays
 * single-tenant per process or gets refactored to per-job.
 */

import { randomUUID } from "node:crypto";
import { db } from "./index";
import { organization } from "./schema";

let _cachedOrgId: string | null = null;

export async function getOrgId(): Promise<string> {
  if (_cachedOrgId) return _cachedOrgId;
  const rows = await db()
    .select({ id: organization.id })
    .from(organization)
    .limit(1);
  if (rows[0]?.id) {
    _cachedOrgId = rows[0].id;
    return _cachedOrgId;
  }
  const newId = randomUUID();
  await db().insert(organization).values({
    id: newId,
    name: "My Workspace",
  });
  _cachedOrgId = newId;
  return _cachedOrgId;
}

/** Test-only: drop the cache so different tests can use different orgs. */
export function _resetOrgIdCacheForTesting(): void {
  _cachedOrgId = null;
}

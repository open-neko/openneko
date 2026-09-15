import { randomUUID } from "node:crypto";
import { readFile, rename, stat, writeFile } from "node:fs/promises";
import { and, data_source, db, desc, eq, getGroupGrantsEnabled, loadGroupGrantInputs } from "@neko/db";
import {
  acquireGraphjinConfigLock,
  applyGroupGrantsToConfig,
  buildGroupGrantsModel,
  listConfigApiOperations,
} from "@neko/llm/graphjin";
import { requestGraphjinRestart } from "../packs/graphjin-config.js";

export type GroupGrantsApplyResult = { enabled: boolean; changed: boolean; roles: string[] };

export type GroupGrantsDeps = {
  configFile: string | null;
  restart: (configFile: string) => Promise<void>;
};

async function defaultEndpoint(orgId: string): Promise<string | null> {
  const [source] = await db()
    .select({ graphqlUrl: data_source.graphql_url })
    .from(data_source)
    .where(and(eq(data_source.org_id, orgId), eq(data_source.enabled, true)))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  if (!source?.graphqlUrl) return null;
  const clean = source.graphqlUrl.replace(/\/+$/, "");
  return clean.endsWith("/api/v1/graphql") ? clean : `${clean}/api/v1/graphql`;
}

export function defaultGroupGrantsDeps(orgId: string): GroupGrantsDeps {
  return {
    configFile: process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim() || null,
    restart: async (configFile) => {
      const endpoint = await defaultEndpoint(orgId);
      if (endpoint) await requestGraphjinRestart(configFile, endpoint);
    },
  };
}

/**
 * Writes the org's group roles and grants into the GraphJin config file and
 * restarts GraphJin when the file changed. Does nothing while group grants
 * are off.
 */
export async function applyGroupGrants(orgId: string, deps: GroupGrantsDeps = defaultGroupGrantsDeps(orgId)): Promise<GroupGrantsApplyResult> {
  if (!(await getGroupGrantsEnabled(orgId))) return { enabled: false, changed: false, roles: [] };
  if (!deps.configFile) throw new Error("GraphJin configuration is unavailable");
  const configFile = deps.configFile;
  const release = await acquireGraphjinConfigLock({ configFile });
  let result: GroupGrantsApplyResult;
  try {
    const raw = await readFile(configFile, "utf8");
    const inputs = await loadGroupGrantInputs(orgId, listConfigApiOperations(raw));
    const model = buildGroupGrantsModel(inputs);
    const patched = applyGroupGrantsToConfig(raw, model);
    if (patched.changed) {
      const mode = (await stat(configFile)).mode & 0o777;
      const temporary = `${configFile}.${randomUUID()}.group-grants`;
      await writeFile(temporary, patched.content, { mode });
      await rename(temporary, configFile);
    }
    result = { enabled: true, changed: patched.changed, roles: model.roles.map((r) => r.role) };
  } finally {
    await release();
  }
  if (result.changed) await deps.restart(configFile);
  return result;
}

const timers = new Map<string, NodeJS.Timeout>();

/** Batches changes: one apply runs 30 seconds after the last change. */
export function scheduleGroupGrantsApply(orgId: string, delayMs = 30_000, run: (orgId: string) => Promise<unknown> = applyGroupGrants): void {
  const existing = timers.get(orgId);
  if (existing) clearTimeout(existing);
  timers.set(
    orgId,
    setTimeout(() => {
      timers.delete(orgId);
      run(orgId).catch((error) =>
        console.warn(`[group-grants] apply failed for ${orgId}: ${error instanceof Error ? error.message : error}`),
      );
    }, delayMs),
  );
}

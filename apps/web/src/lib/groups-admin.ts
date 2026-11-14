import "server-only";

import {
  and,
  data_source,
  db,
  desc,
  eq,
  type ItemType,
} from "@neko/db";
import { graphjinQuery, mintGraphjinToken } from "@neko/llm/graphjin";

export type ItemOption = { id: string; label: string; detail?: string };

export const ITEM_TYPE_LABELS: Record<ItemType, { label: string; plural: string }> = {
  skill: { label: "Skill", plural: "Skills" },
  workflow: { label: "Workflow", plural: "Workflows" },
  metric: { label: "Metric", plural: "Metrics" },
  dashboard: { label: "Dashboard", plural: "Dashboards" },
  watcher: { label: "Watcher", plural: "Watchers" },
  team_memory: { label: "Team memory", plural: "Team memory" },
  data_source: { label: "Data source", plural: "Data sources" },
  saved_query: { label: "Saved query", plural: "Saved queries" },
  api_operation: { label: "API operation", plural: "API operations" },
  action: { label: "Action", plural: "Actions" },
  integration: { label: "Integration", plural: "Integrations" },
  channel: { label: "Channel", plural: "Channels" },
  pack: { label: "Pack", plural: "Packs" },
};

async function graphjinAdmin(orgId: string) {
  const [source] = await db()
    .select({ graphqlUrl: data_source.graphql_url })
    .from(data_source)
    .where(and(eq(data_source.org_id, orgId), eq(data_source.enabled, true)))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  if (!source?.graphqlUrl) return null;
  const clean = source.graphqlUrl.replace(/\/+$/, "");
  return {
    endpoint: clean.endsWith("/api/v1/graphql") ? clean : `${clean}/api/v1/graphql`,
    headers: { authorization: `Bearer ${mintGraphjinToken({ orgId, userId: null, role: "admin", ttlSeconds: 120 })}` },
  };
}

type CatalogRow = { id: string; name: string | null; summary: string | null; database_name: string | null; schema_name: string | null; table_name: string | null; column_name: string | null };

/** GraphJin rejects an `and` with one expression, so a single condition stays bare. */
export function catalogWhere(kind: string, databaseName?: string): string {
  const byKind = `{ kind: { eq: ${JSON.stringify(kind)} } }`;
  return databaseName === undefined ? byKind : `{ and: [${byKind}, { database_name: { eq: ${JSON.stringify(databaseName)} } }] }`;
}

export async function graphjinCatalog(orgId: string, kind: string, databaseName?: string): Promise<CatalogRow[]> {
  const admin = await graphjinAdmin(orgId);
  if (!admin) return [];
  const pageSize = 1000;
  const rows: CatalogRow[] = [];
  for (let offset = 0; offset < 200_000; offset += pageSize) {
    const result = await graphjinQuery<{ gj_catalog?: CatalogRow[] }>({
      baseUrl: admin.endpoint,
      headers: admin.headers,
      query: `query AdminCatalog { gj_catalog(where: ${catalogWhere(kind, databaseName)}, limit: ${pageSize}, offset: ${offset}, order_by: { id: asc }) { id name summary database_name schema_name table_name column_name } }`,
      signal: AbortSignal.timeout(30_000),
    }).catch(() => ({ data: undefined, errors: [{ message: "GraphJin is unavailable" }] }));
    if (result.errors?.length) return rows;
    const page = result.data?.gj_catalog ?? [];
    rows.push(...page);
    if (page.length < pageSize) break;
  }
  return rows;
}

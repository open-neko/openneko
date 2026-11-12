import "server-only";

import { data_source, db, desc, eq } from "@neko/db";

export type DataSourceRow = {
  id: string;
  org_id: string;
  kind: string;
  graphql_url: string;
  mcp_url: string | null;
  label: string | null;
};

export type PublicDataSource = {
  source: "org" | "unset";
  kind: string;
  graphqlUrl: string;
  mcpUrl: string;
  label: string;
};

export type DataSourceDraft = {
  graphqlUrl: string;
  mcpUrl?: string | null;
  label?: string | null;
};

const DEFAULT_KIND = "graphjin";

async function loadRow(orgId: string): Promise<DataSourceRow | null> {
  const rows = await db()
    .select({
      id: data_source.id,
      org_id: data_source.org_id,
      kind: data_source.kind,
      graphql_url: data_source.graphql_url,
      mcp_url: data_source.mcp_url,
      label: data_source.label,
    })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  return (rows[0] as DataSourceRow | undefined) ?? null;
}

function publicFromRow(row: DataSourceRow | null): PublicDataSource {
  if (!row) {
    return {
      source: "unset",
      kind: DEFAULT_KIND,
      graphqlUrl: "",
      mcpUrl: "",
      label: "primary",
    };
  }
  return {
    source: "org",
    kind: row.kind,
    graphqlUrl: row.graphql_url,
    mcpUrl: row.mcp_url ?? "",
    label: row.label ?? "primary",
  };
}

export async function getDataSourceSettings(orgId: string): Promise<PublicDataSource> {
  const row = await loadRow(orgId);
  return publicFromRow(row);
}

export function validateDraft(draft: DataSourceDraft): string[] {
  const errors: string[] = [];
  const graphqlUrl = draft.graphqlUrl?.trim() ?? "";
  if (!graphqlUrl) {
    errors.push("GraphQL URL is required.");
  } else {
    try {
      const url = new URL(graphqlUrl);
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        errors.push("GraphQL URL must use http or https.");
      }
    } catch {
      errors.push("GraphQL URL is not a valid URL.");
    }
  }

  const mcpUrl = draft.mcpUrl?.trim();
  if (mcpUrl) {
    try {
      const url = new URL(mcpUrl);
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        errors.push("MCP URL must use http or https.");
      }
    } catch {
      errors.push("MCP URL is not a valid URL.");
    }
  }
  return errors;
}

export async function hasDataSourceSetup(orgId: string): Promise<boolean> {
  const row = await loadRow(orgId);
  if (!row) return false;
  return validateDraft({
    graphqlUrl: row.graphql_url,
    mcpUrl: row.mcp_url,
    label: row.label,
  }).length === 0;
}

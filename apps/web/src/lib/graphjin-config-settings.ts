import "server-only";

import {
  and,
  db,
  eq,
  GRAPHJIN_CONFIG_SCOPE,
  graphjinConfigSettingsFromRow,
  llm_provider_config,
  type GraphjinConfigSettings,
} from "@neko/db";

export type GraphjinConfigSettingsPayload = {
  settings: GraphjinConfigSettings;
  source: "org" | "default";
};

async function loadRow(orgId: string): Promise<{
  enabled: boolean;
  config: Record<string, unknown> | null;
} | null> {
  const rows = await db()
    .select({
      enabled: llm_provider_config.enabled,
      config: llm_provider_config.config,
    })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, GRAPHJIN_CONFIG_SCOPE),
      ),
    )
    .limit(1);
  return (
    (rows[0] as
      | {
          enabled: boolean;
          config: Record<string, unknown> | null;
        }
      | undefined) ?? null
  );
}

export async function getGraphjinConfigSettingsPayload(
  orgId: string,
): Promise<GraphjinConfigSettingsPayload> {
  const row = await loadRow(orgId);
  return {
    settings: graphjinConfigSettingsFromRow(row ?? undefined),
    source: row ? "org" : "default",
  };
}

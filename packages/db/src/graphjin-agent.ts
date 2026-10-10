/**
 * Org-wide opt-in for GraphJin's server-side agent. Persisted in
 * llm_provider_config with scope="graphjin-agent". Off by default.
 */

import { and, db, eq } from "./index";
import { llm_provider_config } from "./schema";

export const GRAPHJIN_AGENT_SCOPE = "graphjin-agent";
/** Provider value that reuses the primary provider and model. */
export const GRAPHJIN_AGENT_REUSE_PRIMARY = "primary";

export type GraphjinAgentRow = {
  enabled: boolean;
  provider: string;
  model: string | null;
  config: Record<string, unknown> | null;
  secrets: Record<string, unknown> | null;
};

export async function getGraphjinAgentRow(orgId: string): Promise<GraphjinAgentRow | null> {
  const rows = await db()
    .select({
      enabled: llm_provider_config.enabled,
      provider: llm_provider_config.provider,
      model: llm_provider_config.model,
      config: llm_provider_config.config,
      secrets: llm_provider_config.secrets,
    })
    .from(llm_provider_config)
    .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, GRAPHJIN_AGENT_SCOPE)))
    .limit(1);
  return (rows[0] as GraphjinAgentRow | undefined) ?? null;
}

/** True when every agent run gets data only through GraphJin's agent. */
export async function graphjinAgentEnabledForOrg(orgId: string): Promise<boolean> {
  return (await getGraphjinAgentRow(orgId))?.enabled === true;
}

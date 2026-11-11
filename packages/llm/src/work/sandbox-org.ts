import { and, db, eq, llm_provider_config } from "@neko/db";
import { sandboxConfig, type SandboxConfig } from "./sandbox-service";

/** The org's Terminal setting at Admin, Settings, Agent. On unless an admin turned it off. */
export async function orgTerminalSetting(orgId: string): Promise<boolean> {
  const [row] = await db()
    .select({ config: llm_provider_config.config })
    .from(llm_provider_config)
    .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, "agent")))
    .limit(1);
  return (row?.config as { terminal?: unknown } | null | undefined)?.terminal !== false;
}

/** The sandbox a run of this org may use: none without an install URL or with the org's Terminal off. */
export async function sandboxForOrg(
  orgId: string,
  env: NodeJS.ProcessEnv = process.env,
): Promise<SandboxConfig | undefined> {
  const config = sandboxConfig(env);
  if (!config) return undefined;
  return (await orgTerminalSetting(orgId)) ? config : undefined;
}

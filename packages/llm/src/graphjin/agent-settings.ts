// Org-wide GraphJin agent opt-in: OpenNeko owns GraphJin's agent provider,
// model and key while the opt-in is on. When it is off, GraphJin's agent
// block stays as the operator configured it.

import { readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import {
  GRAPHJIN_AGENT_REUSE_PRIMARY,
  and,
  data_source,
  db,
  desc,
  eq,
  getGraphjinAgentRow,
  llm_provider_config,
} from "@neko/db";
import { isMap, parseDocument } from "yaml";
import { isPrimaryProvider } from "../config";
import { resolveAxModelRoute, type AxModelRoute } from "../provider-runtime";
import { maybeDecryptSecret } from "../secrets";
import { acquireGraphjinConfigLock } from "./persist-source-config";
import { requestGraphjinRestart } from "./restart";

/** GraphJin reads the key from this variable; the supervisor fills it from the key file. */
export const GRAPHJIN_AGENT_KEY_ENV = "OPENNEKO_GRAPHJIN_AGENT_API_KEY";
export const GRAPHJIN_AGENT_KEY_FILE = ".openneko-graphjin-agent-key";

export type GraphjinAgentModel = {
  provider: string;
  model: string;
  baseUrl?: string;
  apiKey: string;
};

type ProviderRow = {
  provider: string;
  model: string | null;
  config: Record<string, unknown> | null;
  secrets: Record<string, unknown> | null;
};

/** GraphJin's agent is built on Ax, so it takes Ax provider profiles. */
export function graphjinAgentModelFromRow(row: ProviderRow): GraphjinAgentModel {
  const model = row.model?.trim();
  if (!model || !isPrimaryProvider(row.provider)) {
    throw new Error("The GraphJin agent needs a supported provider and a model.");
  }
  const route: AxModelRoute = resolveAxModelRoute({ provider: row.provider, model, config: row.config });
  // GraphJin has no Azure resource fields; Ax derives the endpoint from base_url.
  const baseUrl = route.options?.resource_name
    ? `https://${route.options.resource_name}.openai.azure.com/openai/deployments/${encodeURIComponent(route.model)}`
    : route.url;
  const apiKey = maybeDecryptSecret(row.secrets?.apiKey) || "none";
  return { provider: route.provider, model: route.model, ...(baseUrl ? { baseUrl } : {}), apiKey };
}

async function primaryRow(orgId: string): Promise<ProviderRow | null> {
  const rows = await db()
    .select({
      provider: llm_provider_config.provider,
      model: llm_provider_config.model,
      config: llm_provider_config.config,
      secrets: llm_provider_config.secrets,
      enabled: llm_provider_config.enabled,
    })
    .from(llm_provider_config)
    .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, "primary")))
    .limit(1);
  const row = rows[0] as (ProviderRow & { enabled: boolean }) | undefined;
  return row?.enabled ? row : null;
}

/** The primary provider as GraphJin agent settings; throws when it is not configured. */
export async function primaryGraphjinAgentModel(orgId: string): Promise<GraphjinAgentModel> {
  const primary = await primaryRow(orgId);
  if (!primary) throw new Error("The GraphJin agent reuses the primary provider, which is not configured.");
  return graphjinAgentModelFromRow(primary);
}

/** The model GraphJin's agent should use, or null when the opt-in is off. */
export async function resolveGraphjinAgentModel(orgId: string): Promise<GraphjinAgentModel | null> {
  const row = await getGraphjinAgentRow(orgId);
  if (!row?.enabled) return null;
  if (row.provider === GRAPHJIN_AGENT_REUSE_PRIMARY) return primaryGraphjinAgentModel(orgId);
  return graphjinAgentModelFromRow(row);
}

/**
 * Write the agent block. Returns the new text and whether GraphJin must
 * restart: GraphJin reads its config file only at start.
 */
export function patchGraphjinAgentBlock(
  yamlText: string,
  model: Omit<GraphjinAgentModel, "apiKey">,
): { content: string; changed: boolean; restart: boolean } {
  const document = parseDocument(yamlText);
  const before = document.get("agent");
  const current = isMap(before) ? (before.toJSON() as Record<string, unknown>) : {};
  const next: Record<string, unknown> = {
    ...current,
    enabled: true,
    provider: model.provider,
    model: model.model,
    api_key_env: GRAPHJIN_AGENT_KEY_ENV,
    read_only: true,
  };
  if (model.baseUrl) next.base_url = model.baseUrl;
  else delete next.base_url;
  const changed = ["enabled", "provider", "model", "base_url", "api_key_env", "read_only"].some((key) => current[key] !== next[key]);
  if (!changed) return { content: yamlText, changed: false, restart: false };
  document.set("agent", document.createNode(next));
  return { content: document.toString(), changed: true, restart: true };
}

async function writePrivate(path: string, content: string, mode: number): Promise<void> {
  const temporary = `${path}.${process.pid}.${Date.now()}.tmp`;
  await writeFile(temporary, content, { mode });
  try {
    await rename(temporary, path);
  } catch (error) {
    await rm(temporary, { force: true });
    throw error;
  }
}

async function graphjinEndpoint(orgId: string): Promise<string | null> {
  const [row] = await db()
    .select({ graphql_url: data_source.graphql_url })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  return row?.graphql_url ?? null;
}

/**
 * Apply the opt-in to GraphJin's config and key file. Any change restarts
 * GraphJin, which reads both only at start.
 * TODO(dosco/graphjin#655): use agent.api_key_file and gj_config so no change needs a restart.
 */
export async function provisionGraphjinAgent(orgId: string, configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim()): Promise<void> {
  if (!configFile) return;
  const model = await resolveGraphjinAgentModel(orgId);
  if (!model) return;
  const release = await acquireGraphjinConfigLock({ configFile });
  let restart = false;
  try {
    const patch = patchGraphjinAgentBlock(await readFile(configFile, "utf8"), model);
    if (patch.changed) await writePrivate(configFile, patch.content, 0o664);
    const keyFile = join(dirname(configFile), GRAPHJIN_AGENT_KEY_FILE);
    const currentKey = await readFile(keyFile, "utf8").catch(() => null);
    if (currentKey !== model.apiKey) await writePrivate(keyFile, model.apiKey, 0o600);
    restart = patch.restart || currentKey !== model.apiKey;
  } finally {
    await release();
  }
  if (!restart) return;
  const endpoint = await graphjinEndpoint(orgId);
  if (endpoint) await requestGraphjinRestart(configFile, endpoint);
}

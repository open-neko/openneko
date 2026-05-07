import { and, db, eq, llm_provider_config } from "@neko/db";
import { AgentBackendConfigError, isAgentBackendId, type AgentBackendId } from "../agent-backend";
import { maybeDecryptSecret } from "../secrets";
import { ClaudeWorkBackend } from "./claude";
import { HermesWorkBackend } from "./hermes";
import type { WorkAgentBackend } from "./types";

type StoredRow = {
  provider: string;
  model: string | null;
  enabled: boolean;
  config: Record<string, unknown> | null;
  secrets: Record<string, unknown> | null;
};

async function loadRow(orgId: string, scope: string): Promise<StoredRow | null> {
  try {
    const rows = await db()
      .select({
        provider: llm_provider_config.provider,
        model: llm_provider_config.model,
        enabled: llm_provider_config.enabled,
        config: llm_provider_config.config,
        secrets: llm_provider_config.secrets,
      })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, scope),
        ),
      )
      .limit(1);
    return (rows[0] as StoredRow | undefined) ?? null;
  } catch {
    return null;
  }
}

function decryptSecrets(secrets: Record<string, unknown> | null | undefined): Record<string, string> {
  if (!secrets) return {};
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(secrets)) {
    const decrypted = maybeDecryptSecret(value);
    if (decrypted) out[key] = decrypted;
  }
  return out;
}

export async function resolveWorkBackendId(orgId: string): Promise<AgentBackendId> {
  const row = await loadRow(orgId, "agent");
  const cfg = (row?.config ?? {}) as { backend?: unknown };
  if (typeof cfg.backend === "string" && isAgentBackendId(cfg.backend)) {
    return cfg.backend;
  }
  return "hermes";
}

export async function resolveWorkAgentBackend(orgId: string): Promise<WorkAgentBackend> {
  const id = await resolveWorkBackendId(orgId);
  if (id === "hermes") return new HermesWorkBackend();

  const primary = await loadRow(orgId, "primary");
  if (!primary) {
    throw new AgentBackendConfigError(
      "claude-agent backend selected but no primary provider is configured. Open /settings/agent and add an Anthropic API key.",
    );
  }
  if (primary.provider !== "anthropic") {
    throw new AgentBackendConfigError(
      `claude-agent backend requires primary provider 'anthropic' (current: '${primary.provider}'). Open /settings/agent to switch.`,
    );
  }
  if (!primary.enabled) {
    throw new AgentBackendConfigError(
      "claude-agent backend selected but primary provider is disabled. Open /settings/agent to re-enable.",
    );
  }

  const secrets = decryptSecrets(primary.secrets);
  return new ClaudeWorkBackend({
    apiKey: secrets.apiKey || "",
    model: primary.model || "",
  });
}

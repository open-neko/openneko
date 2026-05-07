/**
 * Per-org agent backend resolution.
 *
 * Read order: llm_provider_config (scope='agent').config.backend
 *   → env AGENT_BACKEND
 *   → default 'hermes'
 *
 * For backend='claude-agent', also reads the primary scope row to extract the
 * Anthropic API key + model. Throws AgentBackendConfigError if the primary
 * provider isn't Anthropic or the key is missing — the worker job catches
 * that and writes it to processing_job.error so the user sees a "go fix
 * /settings" message instead of a stack trace.
 */

import { and, db, eq, llm_provider_config } from "@neko/db";
import {
  AGENT_DEFAULT_GLOBAL_CAP,
  AgentBackendConfigError,
  isAgentBackendId,
  type AgentBackend,
  type AgentBackendId,
} from "./agent-backend";
import { HermesBackend } from "./agent-backends/hermes";
import { ClaudeAgentBackend } from "./agent-backends/claude-agent";
import { maybeDecryptSecret } from "./secrets";

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
  for (const [k, v] of Object.entries(secrets)) {
    const decrypted = maybeDecryptSecret(v);
    if (decrypted) out[k] = decrypted;
  }
  return out;
}

export async function resolveAgentBackendId(orgId: string): Promise<AgentBackendId> {
  const row = await loadRow(orgId, "agent");
  const cfg = (row?.config ?? {}) as { backend?: unknown };
  if (typeof cfg.backend === "string" && isAgentBackendId(cfg.backend)) {
    return cfg.backend;
  }
  return "hermes";
}

export type AgentConcurrency = {
  globalCap: number;
};

function readPositiveInt(
  raw: unknown,
  fallback: number,
  { min = 1, max = 1000 }: { min?: number; max?: number } = {},
): number {
  const n = Number(raw);
  if (!Number.isFinite(n)) return fallback;
  if (n < min || n > max) return fallback;
  return Math.floor(n);
}

/**
 * Resolves the agent's concurrency caps for this worker boot.
 *
 * Read order: DB (scope='agent') → default. The /settings/agent UI is
 * the only source of truth.
 *
 * Note: pg-boss `batchSize` is fixed at `b.work()` time, so changes to
 * `globalCap` only take effect after the next worker restart.
 */
export async function resolveAgentConcurrency(orgId: string): Promise<AgentConcurrency> {
  const row = await loadRow(orgId, "agent");
  const cfg = (row?.config ?? {}) as { globalCap?: unknown };
  return {
    globalCap: readPositiveInt(cfg.globalCap, AGENT_DEFAULT_GLOBAL_CAP),
  };
}

export async function resolveAgentBackend(orgId: string): Promise<AgentBackend> {
  const id = await resolveAgentBackendId(orgId);

  if (id === "hermes") return new HermesBackend();

  // claude-agent: pull Anthropic key + model from the primary provider row.
  // We deliberately couple to the primary scope here so users have a single
  // place to manage their Anthropic credentials. /settings/agent enforces
  // primary=anthropic when backend=claude-agent; this is the runtime check.
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
  const apiKey = secrets.apiKey;
  const model = primary.model || "";

  return new ClaudeAgentBackend({ apiKey, model });
}

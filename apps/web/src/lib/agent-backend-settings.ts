import "server-only";

import { and, db, eq, llm_provider_config } from "@neko/db";
import {
  AGENT_BACKEND_OPTIONS,
  AGENT_DEFAULT_GLOBAL_CAP,
  getDefaultPrimaryModel,
  isAgentBackendId,
  type AgentBackendId,
} from "@neko/llm";

const AGENT_SCOPE = "agent";
const CLAUDE_SDK_REQUIRED_PROVIDER = "anthropic";

export type AgentBackendSettings = {
  source: "org" | "default";
  backend: AgentBackendId;
  globalCap: number;
};

export type AgentSettingsPayload = {
  agent: AgentBackendSettings;
  options: typeof AGENT_BACKEND_OPTIONS;
  defaults: {
    globalCap: number;
  };
};

async function loadAgentRow(orgId: string): Promise<{
  id: string;
  config: Record<string, unknown> | null;
} | null> {
  const rows = await db()
    .select({
      id: llm_provider_config.id,
      config: llm_provider_config.config,
    })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, AGENT_SCOPE),
      ),
    )
    .limit(1);
  return (
    (rows[0] as
      | { id: string; config: Record<string, unknown> | null }
      | undefined) ?? null
  );
}

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

export async function getAgentBackendSettings(
  orgId: string,
): Promise<AgentBackendSettings> {
  const row = await loadAgentRow(orgId);
  const cfg = (row?.config ?? {}) as {
    backend?: unknown;
    globalCap?: unknown;
  };
  const backend =
    typeof cfg.backend === "string" && isAgentBackendId(cfg.backend)
      ? cfg.backend
      : "hermes";
  const globalCap = readPositiveInt(cfg.globalCap, AGENT_DEFAULT_GLOBAL_CAP);
  return {
    source: row ? "org" : "default",
    backend,
    globalCap,
  };
}

export async function getAgentSettingsPayload(
  orgId: string,
): Promise<AgentSettingsPayload> {
  const agent = await getAgentBackendSettings(orgId);
  return {
    agent,
    options: AGENT_BACKEND_OPTIONS,
    defaults: {
      globalCap: AGENT_DEFAULT_GLOBAL_CAP,
    },
  };
}

export type AgentSaveDraft = {
  backend: string;
  globalCap?: number | string;
};

export async function saveAgentBackendDraft(
  orgId: string,
  draft: AgentSaveDraft,
): Promise<AgentBackendSettings> {
  if (!isAgentBackendId(draft.backend)) {
    throw new Error(`Unsupported agent backend: ${draft.backend}`);
  }

  if (draft.backend === "claude-agent") {
    await ensurePrimaryIsAnthropic(orgId);
  }

  const existing = await loadAgentRow(orgId);
  const existingCfg = (existing?.config ?? {}) as {
    globalCap?: unknown;
  };
  const globalCap = readPositiveInt(
    draft.globalCap ?? existingCfg.globalCap,
    AGENT_DEFAULT_GLOBAL_CAP,
  );
  const config = { backend: draft.backend, globalCap };

  if (existing) {
    await db()
      .update(llm_provider_config)
      .set({
        provider: draft.backend,
        config,
        updated_at: new Date(),
      })
      .where(eq(llm_provider_config.id, existing.id));
  } else {
    await db().insert(llm_provider_config).values({
      org_id: orgId,
      scope: AGENT_SCOPE,
      provider: draft.backend,
      enabled: true,
      config,
      secrets: {},
    });
  }

  return { source: "org", backend: draft.backend, globalCap };
}

async function ensurePrimaryIsAnthropic(orgId: string): Promise<void> {
  const rows = await db()
    .select({
      id: llm_provider_config.id,
      provider: llm_provider_config.provider,
    })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, "primary"),
      ),
    )
    .limit(1);
  const existing = rows[0];

  if (existing && existing.provider === CLAUDE_SDK_REQUIRED_PROVIDER) return;

  if (existing) {
    await db()
      .update(llm_provider_config)
      .set({
        provider: CLAUDE_SDK_REQUIRED_PROVIDER,
        model: getDefaultPrimaryModel(CLAUDE_SDK_REQUIRED_PROVIDER),
        config: {},
        secrets: {},
        enabled: true,
        updated_at: new Date(),
      })
      .where(eq(llm_provider_config.id, existing.id));
    return;
  }

  await db().insert(llm_provider_config).values({
    org_id: orgId,
    scope: "primary",
    provider: CLAUDE_SDK_REQUIRED_PROVIDER,
    model: getDefaultPrimaryModel(CLAUDE_SDK_REQUIRED_PROVIDER),
    enabled: true,
    config: {},
    secrets: {},
  });
}

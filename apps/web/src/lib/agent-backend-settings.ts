import "server-only";

import { and, db, eq, llm_provider_config } from "@neko/db";
import {
  AGENT_BACKEND_OPTIONS,
  AGENT_DEFAULT_GLOBAL_CAP,
  resolveAgentBackendId,
  sandboxConfig,
  type AgentBackendId,
} from "@neko/llm";

const AGENT_SCOPE = "agent";
export type AgentSettings = {
  source: "org" | "default";
  /** Org-wide agent backend. */
  backend: AgentBackendId;
  globalCap: number;
  terminal: boolean;
};

export type AgentSettingsPayload = {
  agent: AgentSettings;
  options: typeof AGENT_BACKEND_OPTIONS;
  /** False when the install has no sandbox URL; the Terminal setting then has no effect. */
  terminalAvailable: boolean;
  defaults: {
    globalCap: number;
    terminal: boolean;
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

export async function getAgentSettings(
  orgId: string,
): Promise<AgentSettings> {
  const row = await loadAgentRow(orgId);
  const cfg = (row?.config ?? {}) as {
    backend?: unknown;
    globalCap?: unknown;
    terminal?: unknown;
  };
  const globalCap = readPositiveInt(cfg.globalCap, AGENT_DEFAULT_GLOBAL_CAP);
  return {
    source: row ? "org" : "default",
    backend: await resolveAgentBackendId(orgId),
    globalCap,
    terminal: cfg.terminal !== false,
  };
}

export async function getAgentSettingsPayload(
  orgId: string,
): Promise<AgentSettingsPayload> {
  const agent = await getAgentSettings(orgId);
  return {
    agent,
    options: AGENT_BACKEND_OPTIONS,
    terminalAvailable: sandboxConfig() !== undefined,
    defaults: {
      globalCap: AGENT_DEFAULT_GLOBAL_CAP,
      terminal: true,
    },
  };
}

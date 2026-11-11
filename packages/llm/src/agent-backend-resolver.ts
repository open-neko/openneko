/**
 * Per-org agent backend resolution.
 *
 * The agent row holds the org-wide backend choice and the concurrency
 * settings. Provider credentials are provisioned independently.
 * The non-secret primary provider/model selection is retained so callers can
 * attest it against the identity the live backend reports.
 */

import { and, db, eq, llm_provider_config } from "@neko/db";
import {
  AGENT_DEFAULT_GLOBAL_CAP,
  type AgentBackend,
  type AgentBackendId,
  DEFAULT_AGENT_BACKEND,
  isAgentBackendId,
} from "./agent-backend";
import type { AxBackendConfig } from "./agent-backends/ax";
import { isReasoningEffort } from "./agent-limits";
import { makeAgentBackend } from "./agent-runtime";
import { isPrimaryProvider } from "./config";
import { recordPrice, taskRouteRecords, type ModelRecord } from "./model-routes";
import { resolveAxModelRoute, type AxModelRoute } from "./provider-runtime";

type StoredRow = {
  provider: string;
  model: string | null;
  enabled: boolean;
  config: Record<string, unknown> | null;
};

async function loadRow(orgId: string, scope: string): Promise<StoredRow | null> {
  try {
    const rows = await db()
      .select({
        provider: llm_provider_config.provider,
        model: llm_provider_config.model,
        enabled: llm_provider_config.enabled,
        config: llm_provider_config.config,
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

export async function resolveAgentBackendId(orgId: string): Promise<AgentBackendId> {
  const row = await loadRow(orgId, "agent");
  const backend = (row?.config ?? {}).backend;
  if (typeof backend === "string" && isAgentBackendId(backend)) return backend;
  return DEFAULT_AGENT_BACKEND;
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
 * Read order: DB (scope='agent') → default. The /admin/settings/agent UI is
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

function positiveInt(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value > 0 ? value : undefined;
}

/** Ax needs the route itself, not only the identity; the launcher loads the key per run. */
export function axBackendConfig(row: StoredRow | null): AxBackendConfig | undefined {
  const model = row?.model?.trim();
  if (!row?.enabled || !model || !isPrimaryProvider(row.provider)) return undefined;
  const config = row.config ?? {};
  return {
    route: resolveAxModelRoute({ provider: row.provider, model, config }),
    ...(positiveInt(config.context_window_tokens) ? { contextWindowTokens: positiveInt(config.context_window_tokens) } : {}),
    ...(positiveInt(config.max_output_tokens) ? { maxOutputTokens: positiveInt(config.max_output_tokens) } : {}),
    // Medium answered q08 in 63s against 153s at high, with the same answer.
    reasoningEffort: isReasoningEffort(config.reasoning_effort) ? config.reasoning_effort : "medium",
  };
}

function recordRow(record: ModelRecord): StoredRow {
  return { provider: record.provider, model: record.model, enabled: record.enabled, config: record.config };
}

function recordRoute(record: ModelRecord): AxModelRoute | undefined {
  if (!isPrimaryProvider(record.provider)) return undefined;
  const price = recordPrice(record);
  try {
    return { ...resolveAxModelRoute({ provider: record.provider, model: record.model, config: record.config ?? {} }), ...(price ? { price } : {}) };
  } catch {
    return undefined;
  }
}

async function turnRecords(orgId: string): Promise<Awaited<ReturnType<typeof taskRouteRecords>> | undefined> {
  try {
    return await taskRouteRecords(orgId, "turn");
  } catch {
    return undefined;
  }
}

/**
 * Resolve the org's backend and the non-secret route of the "turn" task: its
 * model, and the fallback model when one is set. Keys never travel with it.
 * `model` overrides the model name for this run.
 */
export async function resolveAgentBackend(orgId: string, backendId?: AgentBackendId, model?: string): Promise<AgentBackend> {
  const [id, primary, records] = await Promise.all([
    backendId ?? resolveAgentBackendId(orgId),
    loadRow(orgId, "primary"),
    turnRecords(orgId),
  ]);
  const base = records?.primary ? recordRow(records.primary) : primary;
  const ax = axBackendConfig(model && base ? { ...base, model } : base);
  const price = records?.primary ? recordPrice(records.primary) : undefined;
  const fallback = records?.fallback ? recordRoute(records.fallback) : undefined;
  return makeAgentBackend({
    id,
    ax: ax && { ...ax, route: { ...ax.route, ...(price ? { price } : {}) }, ...(fallback ? { fallback } : {}) },
  });
}

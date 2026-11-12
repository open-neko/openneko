import { pool } from "@neko/db";
import {
  PRIMARY_PROVIDER_OPTIONS,
  getDefaultPrimaryModel,
  getPrimaryProviderFields,
  isPrimaryProvider,
  maskSecret,
  type PrimaryProviderId,
  type SettingsField,
} from "./config";
import { decryptSecrets, resolvePrimaryProviderConfig, validateResolvedConfig, type ResolvedPrimaryProviderConfig } from "./llm";
import { MODEL_TASKS, isModelTask, type ModelTask, type RouteTask } from "./model-tasks";

export { MODEL_TASKS, isModelTask, type ModelTask, type RouteTask };

export type ModelPrice = { inputUsdPer1M: number; outputUsdPer1M: number };

export type RoutedModel = {
  /** "default" for the org's primary provider, else the llm_model id. */
  ref: string;
  name: string;
  config: ResolvedPrimaryProviderConfig;
  price?: ModelPrice;
};

export type TaskRoute = { task: RouteTask; primary: RoutedModel; fallback?: RoutedModel };

type ModelRow = {
  id: string;
  name: string;
  provider: string;
  model: string;
  config: Record<string, unknown> | null;
  secrets: Record<string, unknown> | null;
  enabled: boolean;
  input_usd_per_1m: string | null;
  output_usd_per_1m: string | null;
};

type RouteRow = { task: RouteTask; model_id: string | null; fallback_model_id: string | null };

const MODEL_COLUMNS = "id, name, provider, model, config, secrets, enabled, input_usd_per_1m, output_usd_per_1m";

function rowPrice(row: ModelRow): ModelPrice | undefined {
  if (row.input_usd_per_1m === null || row.output_usd_per_1m === null) return undefined;
  return { inputUsdPer1M: Number(row.input_usd_per_1m), outputUsdPer1M: Number(row.output_usd_per_1m) };
}

function routedRecord(row: ModelRow): RoutedModel | undefined {
  if (!row.enabled || !isPrimaryProvider(row.provider)) return undefined;
  const config: ResolvedPrimaryProviderConfig = {
    source: "org",
    provider: row.provider,
    model: row.model,
    enabled: true,
    label: row.name,
    config: row.config ?? {},
    secrets: decryptSecrets(row),
  };
  validateResolvedConfig(config);
  const price = rowPrice(row);
  return { ref: row.id, name: row.name, config, ...(price ? { price } : {}) };
}

async function defaultModel(orgId: string): Promise<RoutedModel> {
  return { ref: "default", name: "default", config: await resolvePrimaryProviderConfig(orgId) };
}

export type ModelRecord = ModelRow;

/**
 * The task's own route wins, then the org's "default" route. A disabled or
 * missing model counts as unset; an unset model means the default model.
 */
export async function taskRouteRecords(
  orgId: string,
  task: ModelTask,
): Promise<{ task: RouteTask; primary?: ModelRecord; fallback?: ModelRecord }> {
  const { rows } = await pool().query<RouteRow>(
    "select task, model_id, fallback_model_id from llm_task_route where org_id = $1 and task in ($2, 'default')",
    [orgId, task],
  );
  const route = rows.find((row) => row.task === task) ?? rows.find((row) => row.task === "default");
  const ids = [route?.model_id, route?.fallback_model_id].filter((id): id is string => Boolean(id));
  const models = ids.length
    ? (await pool().query<ModelRow>(`select ${MODEL_COLUMNS} from llm_model where org_id = $1 and id = any($2::uuid[]) and enabled`, [orgId, ids])).rows
    : [];
  const byId = (id: string | null | undefined) => (id ? models.find((model) => model.id === id) : undefined);
  const primary = byId(route?.model_id);
  const fallback = byId(route?.fallback_model_id);
  return { task: route?.task ?? "default", ...(primary ? { primary } : {}), ...(fallback && fallback.id !== primary?.id ? { fallback } : {}) };
}

export function recordPrice(row: ModelRecord): ModelPrice | undefined {
  return rowPrice(row);
}

export async function resolveTaskRoute(orgId: string, task: ModelTask): Promise<TaskRoute> {
  const records = await taskRouteRecords(orgId, task);
  const primary = (records.primary && routedRecord(records.primary)) || (await defaultModel(orgId));
  let fallback: RoutedModel | undefined;
  try {
    fallback = records.fallback && routedRecord(records.fallback);
  } catch (error) {
    console.warn(`[models] fallback for ${task} skipped: ${error instanceof Error ? error.message : error}`);
  }
  return { task: records.task, primary, ...(fallback ? { fallback } : {}) };
}

export type PublicModel = {
  id: string;
  name: string;
  provider: PrimaryProviderId;
  model: string;
  enabled: boolean;
  config: Record<string, unknown>;
  secretStatus: Record<string, string>;
  price: ModelPrice | null;
};

export type PublicRoute = {
  task: RouteTask;
  label: string;
  description: string;
  modelId: string | null;
  fallbackModelId: string | null;
};

export type ModelSettings = {
  defaultModel: { provider: string; model: string; source: string };
  models: PublicModel[];
  routes: PublicRoute[];
  catalog: {
    providers: ReadonlyArray<{ value: string; label: string; description: string }>;
    defaults: Record<string, string>;
    fields: Record<string, SettingsField[]>;
  };
};

function publicModel(row: ModelRow): PublicModel {
  const secrets = decryptSecrets(row);
  return {
    id: row.id,
    name: row.name,
    provider: row.provider as PrimaryProviderId,
    model: row.model,
    enabled: row.enabled,
    config: row.config ?? {},
    secretStatus: Object.fromEntries(Object.entries(secrets).map(([key, value]) => [key, maskSecret(value)])),
    price: rowPrice(row) ?? null,
  };
}

const DEFAULT_ROUTE = { task: "default", label: "Default", description: "Every task with no route of its own." } as const;

export async function getModelSettings(orgId: string): Promise<ModelSettings> {
  const [models, routes, primary] = await Promise.all([
    pool().query<ModelRow>(`select ${MODEL_COLUMNS} from llm_model where org_id = $1 order by name`, [orgId]),
    pool().query<RouteRow>("select task, model_id, fallback_model_id from llm_task_route where org_id = $1", [orgId]),
    resolvePrimaryProviderConfig(orgId).catch(() => undefined),
  ]);
  const routeFor = (task: RouteTask) => routes.rows.find((row) => row.task === task);
  return {
    defaultModel: { provider: primary?.provider ?? "", model: primary?.model ?? "", source: primary?.source ?? "default" },
    models: models.rows.map(publicModel),
    routes: [DEFAULT_ROUTE, ...MODEL_TASKS].map((entry) => ({
      task: entry.task,
      label: entry.label,
      description: entry.description,
      modelId: routeFor(entry.task)?.model_id ?? null,
      fallbackModelId: routeFor(entry.task)?.fallback_model_id ?? null,
    })),
    catalog: {
      providers: PRIMARY_PROVIDER_OPTIONS,
      defaults: Object.fromEntries(PRIMARY_PROVIDER_OPTIONS.map((option) => [option.value, getDefaultPrimaryModel(option.value)])),
      fields: Object.fromEntries(PRIMARY_PROVIDER_OPTIONS.map((option) => [option.value, getPrimaryProviderFields(option.value)])),
    },
  };
}

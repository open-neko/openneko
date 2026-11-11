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
import { resolveProviderRuntime } from "./provider-runtime";
import { MODEL_TASKS, isModelTask, type ModelTask, type RouteTask } from "./model-tasks";
import { maybeEncryptSecret } from "./secrets";
import { recordAuditEvent } from "./workflows/audit-chain";

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

export class ModelSettingsError extends Error {
  constructor(
    message: string,
    readonly status: 400 | 404 | 409 = 400,
  ) {
    super(message);
    this.name = "ModelSettingsError";
  }
}

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

export type ModelDraft = {
  name?: unknown;
  provider?: unknown;
  model?: unknown;
  enabled?: unknown;
  config?: unknown;
  secrets?: unknown;
  price?: unknown;
};

const NAME = /^[a-z][a-z0-9-]{0,39}$/;
const MAX_PRICE_USD_PER_1M = 1000;

function record(value: unknown, what: string): Record<string, unknown> {
  if (value === undefined || value === null) return {};
  if (typeof value !== "object" || Array.isArray(value)) throw new ModelSettingsError(`${what} must be an object.`);
  return value as Record<string, unknown>;
}

function readPrice(value: unknown): ModelPrice | null {
  if (value === undefined || value === null) return null;
  const raw = record(value, "price");
  const input = raw.inputUsdPer1M;
  const output = raw.outputUsdPer1M;
  const valid = (n: unknown): n is number => typeof n === "number" && Number.isFinite(n) && n >= 0 && n <= MAX_PRICE_USD_PER_1M;
  if (!valid(input) || !valid(output)) {
    throw new ModelSettingsError(`Each price must be a number from 0 to ${MAX_PRICE_USD_PER_1M} USD per million tokens.`);
  }
  return { inputUsdPer1M: input, outputUsdPer1M: output };
}

/** Creates a model when id is absent. A secret left out keeps its stored value; null removes it. */
export async function saveModel(orgId: string, actorUserId: string | null, id: string | undefined, draft: ModelDraft): Promise<PublicModel> {
  const existing = id
    ? (await pool().query<ModelRow>(`select ${MODEL_COLUMNS} from llm_model where org_id = $1 and id = $2`, [orgId, id])).rows[0]
    : undefined;
  if (id && !existing) throw new ModelSettingsError("No such model.", 404);
  const name = draft.name === undefined && existing ? existing.name : String(draft.name ?? "").trim();
  if (!NAME.test(name) || name === "default") {
    throw new ModelSettingsError("The name must start with a letter and use only a to z, 0 to 9 and hyphens, at most 40 characters. \"default\" is taken.");
  }
  const provider = draft.provider === undefined && existing ? existing.provider : String(draft.provider ?? "");
  if (!isPrimaryProvider(provider)) throw new ModelSettingsError("Choose a model provider from the list.");
  const providerChanged = !existing || existing.provider !== provider;
  const model = (draft.model === undefined && existing && !providerChanged ? existing.model : String(draft.model ?? "").trim()) || getDefaultPrimaryModel(provider);
  if (!model || model.length > 128) throw new ModelSettingsError("The model name is required, at most 128 characters.");
  const fields = getPrimaryProviderFields(provider);
  const configInput = record(draft.config, "config");
  const config: Record<string, unknown> = {};
  for (const field of fields.filter((entry) => entry.kind !== "secret")) {
    const value = configInput[field.key] ?? (providerChanged ? undefined : existing?.config?.[field.key]);
    if (typeof value === "string" && value.trim()) config[field.key] = value.trim();
  }
  const secrets: Record<string, string> = providerChanged ? {} : decryptSecrets(existing ?? null);
  const secretInput = record(draft.secrets, "secrets");
  for (const field of fields.filter((entry) => entry.kind === "secret")) {
    const value = secretInput[field.key];
    if (value === null) delete secrets[field.key];
    else if (typeof value === "string" && value.trim()) secrets[field.key] = value.trim();
  }
  for (const field of fields) {
    const value = field.kind === "secret" ? secrets[field.key] : config[field.key];
    if (field.required && !value) throw new ModelSettingsError(`${field.label} is required.`);
  }
  const enabled = draft.enabled === undefined ? (existing?.enabled ?? true) : draft.enabled === true;
  const price = draft.price === undefined && existing ? (rowPrice(existing) ?? null) : readPrice(draft.price);
  try {
    resolveProviderRuntime({ provider, model, config });
    validateResolvedConfig({ source: "org", provider, model, enabled, config, secrets });
  } catch (error) {
    throw new ModelSettingsError(error instanceof Error ? error.message : "The model settings are not valid.");
  }
  const sealed = Object.fromEntries(Object.entries(secrets).map(([key, value]) => [key, maybeEncryptSecret(value)]));
  const values = [orgId, name, provider, model, JSON.stringify(config), JSON.stringify(sealed), enabled, price?.inputUsdPer1M ?? null, price?.outputUsdPer1M ?? null];
  try {
    const { rows } = existing
      ? await pool().query<ModelRow>(
          `update llm_model set name = $2, provider = $3, model = $4, config = $5::jsonb, secrets = $6::jsonb, enabled = $7,
                  input_usd_per_1m = $8, output_usd_per_1m = $9, updated_at = now()
            where org_id = $1 and id = $10 returning ${MODEL_COLUMNS}`,
          [...values, existing.id],
        )
      : await pool().query<ModelRow>(
          `insert into llm_model (org_id, name, provider, model, config, secrets, enabled, input_usd_per_1m, output_usd_per_1m)
           values ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9) returning ${MODEL_COLUMNS}`,
          values,
        );
    const saved = publicModel(rows[0]!);
    await audit(orgId, saved.id, existing ? "model:changed" : "model:added", {
      actorUserId,
      name,
      provider,
      model,
      enabled,
      price,
      secretsChanged: Object.keys(secretInput),
    });
    return saved;
  } catch (error) {
    if ((error as { code?: string }).code === "23505") throw new ModelSettingsError(`A model named "${name}" exists.`, 409);
    throw error;
  }
}

export async function deleteModel(orgId: string, actorUserId: string | null, id: string): Promise<void> {
  try {
    const { rows } = await pool().query<{ name: string }>("delete from llm_model where org_id = $1 and id = $2 returning name", [orgId, id]);
    if (!rows[0]) throw new ModelSettingsError("No such model.", 404);
    await audit(orgId, id, "model:removed", { actorUserId, name: rows[0].name });
  } catch (error) {
    if ((error as { code?: string }).code === "23503") {
      throw new ModelSettingsError("A task route uses this model. Change the route first.", 409);
    }
    throw error;
  }
}

export type RouteDraft = { task?: unknown; modelId?: unknown; fallbackModelId?: unknown };

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function readModelId(value: unknown, what: string): string | null {
  if (value === undefined || value === null || value === "" || value === "default") return null;
  if (typeof value !== "string" || !UUID.test(value)) throw new ModelSettingsError(`${what} must be a model id.`);
  return value;
}

/** A route with no model and no fallback is removed, so the task follows the default route again. */
export async function saveRoute(orgId: string, actorUserId: string | null, draft: RouteDraft): Promise<PublicRoute> {
  const task = draft.task;
  if (task !== "default" && !isModelTask(task)) throw new ModelSettingsError("Choose a task from the list.");
  const modelId = readModelId(draft.modelId, "modelId");
  const fallbackModelId = readModelId(draft.fallbackModelId, "fallbackModelId");
  if (task === "default" && modelId) {
    throw new ModelSettingsError("The default route always uses the default model. Set only its fallback.");
  }
  if (modelId && modelId === fallbackModelId) throw new ModelSettingsError("The fallback must be another model.");
  const ids = [modelId, fallbackModelId].filter((id): id is string => Boolean(id));
  if (ids.length) {
    const { rows } = await pool().query<{ id: string }>("select id from llm_model where org_id = $1 and id = any($2::uuid[])", [orgId, ids]);
    if (rows.length !== new Set(ids).size) throw new ModelSettingsError("No such model.", 404);
  }
  if (!modelId && !fallbackModelId) {
    await pool().query("delete from llm_task_route where org_id = $1 and task = $2", [orgId, task]);
  } else {
    await pool().query(
      `insert into llm_task_route (org_id, task, model_id, fallback_model_id) values ($1, $2, $3, $4)
       on conflict (org_id, task) do update set model_id = excluded.model_id, fallback_model_id = excluded.fallback_model_id, updated_at = now()`,
      [orgId, task, modelId, fallbackModelId],
    );
  }
  await audit(orgId, task, "model:route_changed", { actorUserId, task, modelId, fallbackModelId });
  const entry = task === "default" ? DEFAULT_ROUTE : MODEL_TASKS.find((item) => item.task === task)!;
  return { task, label: entry.label, description: entry.description, modelId, fallbackModelId };
}

function audit(orgId: string, entityId: string, event: string, payload: Record<string, unknown>): Promise<void> {
  return recordAuditEvent({ orgId, entityKind: "llm_model", entityId, event, payload });
}

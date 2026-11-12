import { afterAll, afterEach, beforeEach, describe, expect, it } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, seedProvider, uniqueOrgId } from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import { getModelSettings, resolveTaskRoute } from "../src/model-routes";
import { maybeEncryptSecret } from "../src/secrets";
import { generate, hostProgram, setGatewayTransport, type GatewayRequest, type GatewayResult } from "../src/inference";
import { resolveAgentBackend } from "../src/agent-backend-resolver";
import { loadAgentModelKeys } from "../src/host-provision";
import { getSpendSettings } from "../src/spend";
import type { AxBackend } from "../src/agent-backends/ax";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const PROGRAM = hostProgram(`question:string "a question" -> answer:string "the answer"`, "Answer it.");
const PLANTED = "sk-planted-4f1e9c2a";
const FALLBACK_PLANTED = "sk-fallback-planted-77b0";

function fakeGateway(fallback = false) {
  const calls: Array<{ request: GatewayRequest; key: string; fallbackKey?: string }> = [];
  setGatewayTransport(async (request, key, fallbackKey) => {
    calls.push({ request, key, fallbackKey });
    const served = fallback && request.fallback ? request.fallback : request.route;
    return {
      outputs: { answer: "42" },
      usage: { requests: fallback ? 2 : 1, reported: 1, input_tokens: 1_000_000, output_tokens: 500_000, total_tokens: 1_500_000, coverage: "complete" },
      provider: served.provider ?? "openai-compatible",
      model: served.model,
      ...(fallback ? { fallback: true } : {}),
    } satisfies GatewayResult;
  });
  return calls;
}

type ModelSeed = {
  name: string;
  provider: string;
  model: string;
  config?: Record<string, unknown>;
  secrets?: Record<string, string>;
  price?: { inputUsdPer1M: number; outputUsdPer1M: number };
};

async function addModel(orgId: string, seed: ModelSeed): Promise<{ id: string }> {
  const sealed = Object.fromEntries(Object.entries(seed.secrets ?? {}).map(([key, value]) => [key, maybeEncryptSecret(value)]));
  const { rows } = await pool().query<{ id: string }>(
    `insert into llm_model (org_id, name, provider, model, config, secrets, input_usd_per_1m, output_usd_per_1m)
     values ($1, $2, $3, $4, $5, $6, $7, $8) returning id`,
    [orgId, seed.name, seed.provider, seed.model, seed.config ?? {}, sealed, seed.price?.inputUsdPer1M ?? null, seed.price?.outputUsdPer1M ?? null],
  );
  return rows[0]!;
}

async function setRoute(orgId: string, task: string, modelId: string | null, fallbackModelId: string | null = null): Promise<void> {
  if (!modelId && !fallbackModelId) {
    await pool().query("delete from llm_task_route where org_id = $1 and task = $2", [orgId, task]);
    return;
  }
  await pool().query(
    `insert into llm_task_route (org_id, task, model_id, fallback_model_id) values ($1, $2, $3, $4)
     on conflict (org_id, task) do update set model_id = excluded.model_id, fallback_model_id = excluded.fallback_model_id`,
    [orgId, task, modelId, fallbackModelId],
  );
}

describeIfDb("model records and task routes", () => {
  let orgId: string;

  beforeEach(async () => {
    orgId = uniqueOrgId("models");
    await createTestOrg(orgId);
    await seedProvider(orgId, { scope: "primary", provider: "anthropic", model: "claude-sonnet-5-5", secrets: { apiKey: "sk-default" } });
  });

  afterEach(async () => {
    setGatewayTransport(undefined);
    await deleteTestOrg(orgId);
  });

  afterAll(async () => {
    await pool().end();
  });

  const addCheap = () =>
    addModel(orgId, {
      name: "cheap",
      provider: "openai-compatible",
      model: "qwen3",
      config: { url: "https://models.example.test/v1" },
      price: { inputUsdPer1M: 0.5, outputUsdPer1M: 2 },
    });
  const addSpare = () => addModel(orgId, { name: "spare", provider: "openai", model: "gpt-5", secrets: { apiKey: FALLBACK_PLANTED } });

  it("keeps a model's key out of the settings", async () => {
    await addModel(orgId, { name: "openai-main", provider: "openai", model: "gpt-5", secrets: { apiKey: PLANTED } });
    const settings = await getModelSettings(orgId);
    expect(JSON.stringify(settings)).not.toContain(PLANTED);
    expect(settings.models[0]?.secretStatus.apiKey).toBeTruthy();
  });

  it("sends a task to its own model, others to the default route, and a disabled model to the default model", async () => {
    const cheap = await addCheap();
    const spare = await addSpare();
    await setRoute(orgId, "classify", cheap.id);
    await setRoute(orgId, "default", null, spare.id);

    const classify = await resolveTaskRoute(orgId, "classify");
    expect(classify.primary.config).toMatchObject({ provider: "openai-compatible", model: "qwen3" });
    expect(classify.fallback).toBeUndefined();

    const compact = await resolveTaskRoute(orgId, "compact");
    expect(compact.task).toBe("default");
    expect(compact.primary.ref).toBe("default");
    expect(compact.primary.config).toMatchObject({ provider: "anthropic", model: "claude-sonnet-5-5" });
    expect(compact.fallback?.config).toMatchObject({ provider: "openai", model: "gpt-5" });

    await pool().query("update llm_model set enabled = false where id = $1", [cheap.id]);
    expect((await resolveTaskRoute(orgId, "classify")).primary.ref).toBe("default");

    await setRoute(orgId, "classify", null);
    expect((await resolveTaskRoute(orgId, "classify")).task).toBe("default");
  });

  it("sends the task's model and fallback to the gateway and meters the call on the model that answered", async () => {
    const cheap = await addCheap();
    const spare = await addSpare();
    await setRoute(orgId, "classify", cheap.id, spare.id);

    const calls = fakeGateway();
    const first = await generate(orgId, PROGRAM, { question: "q" }, { task: "classify" });
    expect(calls[0].request.route).toMatchObject({ model: "qwen3", url: "https://models.example.test/v1" });
    expect(calls[0].request.fallback).toMatchObject({ provider: "openai", model: "gpt-5" });
    expect(calls[0].fallbackKey).toBe(FALLBACK_PLANTED);
    expect(first).toMatchObject({ model: "qwen3", fallback: false });
    expect(first.usage).toMatchObject({ costStatus: "estimated", estimatedCostUsd: 1.5, costSource: "model_record" });

    fakeGateway(true);
    const second = await generate(orgId, PROGRAM, { question: "q" }, { task: "classify" });
    expect(second).toMatchObject({ provider: "openai", model: "gpt-5", fallback: true });

    const ledger = await pool().query("select task, provider, model, priced, cost_micros from spend_ledger where org_id = $1 order by created_at", [orgId]);
    expect(ledger.rows).toEqual([
      { task: "classify", provider: "openai-compatible", model: "qwen3", priced: "estimated", cost_micros: "1500000" },
      expect.objectContaining({ task: "classify", provider: "openai", model: "gpt-5" }),
    ]);
  });

  it("records a call on a model with no price at no cost, and the meter groups by task and model", async () => {
    await pool().query("update llm_provider_config set model = 'house-model', provider = 'openai-compatible', config = $2 where org_id = $1", [
      orgId,
      { url: "https://house.example.test/v1" },
    ]);
    fakeGateway();
    await generate(orgId, PROGRAM, { question: "q" }, { task: "compact" });
    await generate(orgId, PROGRAM, { question: "q" }, { task: "compact" });
    const settings = await getSpendSettings(orgId);
    expect(settings.byModel).toEqual([
      { task: "compact", provider: "openai-compatible", model: "house-model", calls: 2, tokens: 3_000_000, spentUsd: 0, unpricedCalls: 2 },
    ]);
    const alerts = await pool().query("select 1 from behavior_alert where org_id = $1 and kind = 'spend.price_unknown'", [orgId]);
    expect(alerts.rowCount).toBe(0);
  });

  it("gives an agent turn the turn route's model and fallback, and loads the fallback key apart", async () => {
    const cheap = await addCheap();
    const spare = await addSpare();
    await setRoute(orgId, "turn", cheap.id, spare.id);

    const backend = (await resolveAgentBackend(orgId)) as AxBackend;
    const ax = (backend as unknown as { ax: { route: Record<string, unknown>; fallback?: Record<string, unknown> } }).ax;
    expect(ax.route).toMatchObject({ model: "qwen3", price: { inputUsdPer1M: 0.5, outputUsdPer1M: 2 } });
    expect(ax.fallback).toMatchObject({ provider: "openai", model: "gpt-5" });
    expect(JSON.stringify(ax)).not.toContain(FALLBACK_PLANTED);

    const keys = await loadAgentModelKeys(orgId);
    expect(keys.FALLBACK_MODEL_API_KEY).toBe(FALLBACK_PLANTED);
    expect(Object.entries(keys).filter(([name]) => name !== "FALLBACK_MODEL_API_KEY").map(([, value]) => value)).not.toContain(FALLBACK_PLANTED);
  });
});

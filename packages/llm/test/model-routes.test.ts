import { afterAll, afterEach, beforeEach, describe, expect, it } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, seedProvider, uniqueOrgId } from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import {
  deleteModel,
  getModelSettings,
  ModelSettingsError,
  resolveTaskRoute,
  saveModel,
  saveRoute,
} from "../src/model-routes";
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

async function expectError(promise: Promise<unknown>, status: number): Promise<void> {
  const error = await promise.catch((e: unknown) => e);
  expect(error).toBeInstanceOf(ModelSettingsError);
  expect((error as ModelSettingsError).status).toBe(status);
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
    saveModel(orgId, null, undefined, {
      name: "cheap",
      provider: "openai-compatible",
      model: "qwen3",
      config: { url: "https://models.example.test/v1" },
      price: { inputUsdPer1M: 0.5, outputUsdPer1M: 2 },
    });
  const addSpare = () =>
    saveModel(orgId, null, undefined, { name: "spare", provider: "openai", model: "gpt-5", secrets: { apiKey: FALLBACK_PLANTED } });

  it("keeps a model's key sealed in the table and masked in the settings", async () => {
    const saved = await saveModel(orgId, null, undefined, { name: "openai-main", provider: "openai", model: "gpt-5", secrets: { apiKey: PLANTED } });
    const raw = await pool().query("select secrets::text as secrets from llm_model where id = $1", [saved.id]);
    expect(raw.rows[0].secrets).not.toContain(PLANTED);
    expect(JSON.stringify(await getModelSettings(orgId))).not.toContain(PLANTED);
    const kept = await saveModel(orgId, null, saved.id, { model: "gpt-5-mini" });
    expect(kept.secretStatus.apiKey).toBeTruthy();
  });

  it("refuses a bad name, a provider not in the list and a missing key", async () => {
    await expectError(saveModel(orgId, null, undefined, { name: "default", provider: "openai", secrets: { apiKey: "k" } }), 400);
    await expectError(saveModel(orgId, null, undefined, { name: "Big Model", provider: "openai", secrets: { apiKey: "k" } }), 400);
    await expectError(saveModel(orgId, null, undefined, { name: "x", provider: "no-such" }), 400);
    await expectError(saveModel(orgId, null, undefined, { name: "x", provider: "openai" }), 400);
    await addCheap();
    await expectError(addCheap(), 409);
  });

  it("sends a task to its own model, others to the default route, and a disabled model to the default model", async () => {
    const cheap = await addCheap();
    const spare = await addSpare();
    await saveRoute(orgId, null, { task: "classify", modelId: cheap.id });
    await saveRoute(orgId, null, { task: "default", fallbackModelId: spare.id });

    const classify = await resolveTaskRoute(orgId, "classify");
    expect(classify.primary.config).toMatchObject({ provider: "openai-compatible", model: "qwen3" });
    expect(classify.fallback).toBeUndefined();

    const compact = await resolveTaskRoute(orgId, "compact");
    expect(compact.task).toBe("default");
    expect(compact.primary.ref).toBe("default");
    expect(compact.primary.config).toMatchObject({ provider: "anthropic", model: "claude-sonnet-5-5" });
    expect(compact.fallback?.config).toMatchObject({ provider: "openai", model: "gpt-5" });

    await saveModel(orgId, null, cheap.id, { enabled: false });
    expect((await resolveTaskRoute(orgId, "classify")).primary.ref).toBe("default");

    await saveRoute(orgId, null, { task: "classify" });
    expect((await resolveTaskRoute(orgId, "classify")).task).toBe("default");
  });

  it("refuses a route that names the default's model, the same model twice, or an unknown model", async () => {
    const cheap = await addCheap();
    await expectError(saveRoute(orgId, null, { task: "default", modelId: cheap.id }), 400);
    await expectError(saveRoute(orgId, null, { task: "turn", modelId: cheap.id, fallbackModelId: cheap.id }), 400);
    await expectError(saveRoute(orgId, null, { task: "turn", modelId: "00000000-0000-4000-8000-000000000000" }), 404);
    await expectError(saveRoute(orgId, null, { task: "no-such-task", modelId: cheap.id }), 400);
  });

  it("refuses to delete a model a route uses, and an org delete still removes both", async () => {
    const cheap = await addCheap();
    await saveRoute(orgId, null, { task: "turn", modelId: cheap.id });
    await expectError(deleteModel(orgId, null, cheap.id), 409);
    await saveRoute(orgId, null, { task: "turn" });
    await deleteModel(orgId, null, cheap.id);
    await expectError(deleteModel(orgId, null, cheap.id), 404);

    const again = await addCheap();
    await saveRoute(orgId, null, { task: "turn", modelId: again.id });
    await deleteTestOrg(orgId);
    const left = await pool().query("select 1 from llm_model where org_id = $1 union all select 1 from llm_task_route where org_id = $1", [orgId]);
    expect(left.rowCount).toBe(0);
    await createTestOrg(orgId);
  });

  it("sends the task's model and fallback to the gateway and meters the call on the model that answered", async () => {
    const cheap = await addCheap();
    const spare = await addSpare();
    await saveRoute(orgId, null, { task: "classify", modelId: cheap.id, fallbackModelId: spare.id });

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
    await saveRoute(orgId, null, { task: "turn", modelId: cheap.id, fallbackModelId: spare.id });

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

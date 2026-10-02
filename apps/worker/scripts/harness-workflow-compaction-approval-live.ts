// Connected M6: a pending governed action and committed finding survive Ax compaction.
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { action_policy, db, eq, getOrgId, llm_provider_config, pack_action_definition, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, createActionRequest, enableWorkflowApiAccess, getWorkflowApiRunStatus,
  registerActionAdapter, updateWorkflowApiLimits } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";
import { createAdminHandler } from "../src/admin-server.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 environment required");
const orgId = await getOrgId();
const queue = await boss();
const control = "http://127.0.0.1:18118/control";
const model = "harness-compaction-approval-fixture";
const actionKind = "harness_workflow_effect_fixture";
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
let workflowId: string | undefined;
let policyId: string | undefined;
let admin: ReturnType<typeof createServer> | undefined;
let effectCount = 0;
try {
  assert.equal((await fetch(control, { method: "POST", body: "{}" })).status, 204);
  await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [workflow] = await db().insert(workflow_definition).values({
    org_id: orgId, name: `Compacted approval ${randomUUID()}`,
    goal: "Verify REF-42, propose a governed synthetic update, publish a finding, and report its pending approval. Never execute a change without approval.",
  }).returning({ id: workflow_definition.id });
  workflowId = workflow.id;
  await db().insert(pack_action_definition).values({ org_id: orgId, kind: actionKind,
    readiness: "ready", definition_hash: "compaction-fixture", definition: { kind: actionKind,
      description: "Update the synthetic reference", inputSchema: { type: "object",
        properties: { value: { type: "integer" } }, required: ["value"], additionalProperties: false } } });
  registerActionAdapter(actionKind, async ({ idempotencyKey }) => {
    effectCount++;
    return { result: { value: 42 }, externalRef: idempotencyKey };
  }, "pack");
  const [policy] = await db().insert(action_policy).values({ org_id: orgId, name: "Compaction approval fixture",
    mode: "approval_required", applies_to_kinds: [actionKind], applies_to_scopes: ["external"] }).returning({ id: action_policy.id });
  policyId = policy.id;
  admin = createServer(createAdminHandler({ actionRequests: { create: async input => {
    const request = await createActionRequest(input as Parameters<typeof createActionRequest>[0]);
    return { id: request.id, status: request.status };
  } } }));
  await new Promise<void>(resolve => admin!.listen(18122, "127.0.0.1", resolve));
  const [org] = (await pool().query<{ solo_admin_user_id: string | null }>(
    "select solo_admin_user_id from organization where id=$1", [orgId])).rows;
  assert.ok(org);
  const actor = { userId: org.solo_admin_user_id, role: "admin" as const };
  const { token } = await enableWorkflowApiAccess({ orgId, workflowId, actor });
  await updateWorkflowApiLimits({ orgId, workflowId, actor,
    limits: { maxModelCalls: 48, maxTokensPerRun: 100_000, maxCostMicrosPerRun: 1_000_000 } });
  const clientFingerprint = `harness-compaction-${orgId}`;
  const workflowStartedAt = Date.now();
  const admitted = await admitWorkflowApiRun({ workflowId, token,
    idempotencyKey: `compaction-approval-${randomUUID()}`, mode: "single",
    value: { reference: "REF-42", constraint: "Never execute a change without approval" }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let apiStatus: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 180; n++) {
    apiStatus = await getWorkflowApiRunStatus({ workflowId, runId: admitted.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(apiStatus.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  const [run] = (await pool().query<{ work_run_id: string; status: string; error: string | null }>(
    "select work_run_id,status,error from workflow_run where id=$1", [admitted.runId])).rows;
  assert.ok(run);
  const [telemetryRow] = (await pool().query<{ telemetry_summary: {provider?: string; requestedModel?: string; resolvedModel?: string} | null }>(
    "select telemetry_summary from workflow_run where id=$1", [admitted.runId])).rows;
  assert.equal(telemetryRow?.telemetry_summary?.provider, "fixture");
  assert.equal(telemetryRow?.telemetry_summary?.requestedModel, model);
  assert.equal(telemetryRow?.telemetry_summary?.resolvedModel, model);
  const counts = await (await fetch(control)).json() as Record<string, number>;
  const checkpointRoot = join(getOrgAgentRoot(orgId), "runs", run.work_run_id, ".harness");
  const checkpointBytes = await readFile(join(checkpointRoot,
    `${createHash("sha256").update(run.work_run_id).digest("hex")}.json`));
  const checkpoint = JSON.parse(checkpointBytes.toString("utf8")) as {
      spec?: { host_budget_mode?: string };
      result: { status: string; answer?: string; code?: string; cost?: { charged_micros: number } };
      events: Array<{ type: string; stage?: string; origin?: string; name?: string; operation_id?: number; call_id?: number; cost_micros?: number;
        data?: { version?: string; choice?: string; suggested_profile?: string; probabilities?: Record<string, number>;
          profile?: string; from?: string; to?: string; operation_id?: number; call_id?: number; reason?: string;
          limits?: {max_model_calls: number; max_model_tokens: number; max_cost_micros: number} };
        terminal?: { accepted: boolean } }>;
    };
  const operations = (await pool().query<{ operation_id: number; request: { tool?: string }; result: unknown }>(
    "select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
    [orgId, run.work_run_id])).rows;
  const [request] = (await pool().query<{ id: string; status: string; harness_operation_id: number }>(
    "select id,status,harness_operation_id from action_request where org_id=$1 and workflow_run_id=$2", [orgId, admitted.runId])).rows;
  const outputs = (await pool().query<{ id: string }>(
    "select id from workflow_output where workflow_run_id=$1", [admitted.runId])).rows;
  const executions = (await pool().query<{ n: number }>(
    "select count(*)::int as n from action_execution where org_id=$1 and action_request_id=$2", [orgId, request?.id ?? randomUUID()])).rows[0].n;
  console.log("M6_CONNECTED_APPROVAL_COMPACTION_DIAGNOSTIC", JSON.stringify({ status: apiStatus?.status,
    run, result: { status: checkpoint.result.status, kind: (checkpoint.result as { kind?: string }).kind, code: checkpoint.result.code, answer: checkpoint.result.answer }, calls: counts[model], ordinary: counts[`ordinary:${model}`],
    summaries: counts[`summary:${model}`], summaryAt: counts[`summary-at:${model}`],
    largestRequest: counts[`max-request:${model}`], operations: operations.map(op => op.request.tool),
    request, outputs: outputs.length, executions, effectCount }));
  assert.equal(apiStatus?.status, "completed");
  assert.equal(run.status, "completed");
  assert.equal(checkpoint.result.status, "completed");
  assert.equal(checkpoint.spec?.host_budget_mode ?? "", process.env.OPENNEKO_HARNESS_BUDGET_CANARY === "1" ? "canary" : "");
  assert.match(checkpoint.result.answer ?? "", /pending approval/);
  assert.equal(counts[`ordinary:${model}`], 10);
  assert.ok((counts[`summary:${model}`] ?? 0) >= 1, "Ax did not compact the approval trajectory");
  assert.ok((counts[`summary-at:${model}`] ?? Infinity) <= 9, "summary occurred after the responder");
  assert.ok((counts[`max-request:${model}`] ?? Infinity) < 100_000);
  assert.equal(operations.length, 7);
  assert.ok(request);
  assert.equal(request.status, "pending_approval");
  assert.equal(request.harness_operation_id, 2);
  assert.equal((operations[1].result as { id?: string }).id, request.id);
  assert.equal(outputs.length, 1);
  assert.equal((operations[2].result as { outputId?: string }).outputId, outputs[0].id);
  assert.equal(executions, 0);
  assert.equal(effectCount, 0);
  assert.equal(checkpoint.events.find(event => event.type === "terminal.checked")?.terminal?.accepted, true);
  if (process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW === "1") {
    const started = checkpoint.events.filter(event => event.type === "model.request.started" && event.stage === "budget_triage");
    const finished = checkpoint.events.filter(event => event.type === "model.request.finished" && event.stage === "budget_triage");
    const proposals = checkpoint.events.filter(event => event.type === "budget.profile.proposed");
    const extensions = checkpoint.events.filter(event => event.type === "budget.profile.extended");
    assert.equal(counts["jev-fixture"], 1, "Typesafe route did not traverse the connected OpenShell gateway");
    assert.equal(started.length, 1);
    assert.equal(finished.length, 1);
    assert.equal(started[0].origin, "triage");
    assert.equal(started[0].name, "jev-fixture");
    assert.equal(started[0].cost_micros, 512);
    assert.equal(finished[0].data?.version, "budget-triage-v1");
    assert.equal(finished[0].data?.choice, "multi_step");
    assert.equal(finished[0].data?.suggested_profile, "multi_step");
    assert.equal(finished[0].data?.probabilities?.multi_step, 0.8);
    assert.equal(proposals.length, 1);
    assert.equal(proposals[0].data?.version, "m6-shadow-v1");
    assert.equal(proposals[0].data?.profile, "multi_step");
    assert.deepEqual(proposals[0].data?.limits, {max_model_calls: 3, max_model_tokens: 40_000, max_cost_micros: 20_000});
    assert.equal(extensions.length, 1, "a durable model result did not extend before GraphJin preflight");
    assert.equal(extensions[0].data?.from, "multi_step");
    assert.equal(extensions[0].data?.to, "artifact");
    assert.equal(extensions[0].data?.reason, "remote_lookup_preflight");
    assert.equal(extensions[0].data?.call_id, extensions[0].call_id);
    assert.ok((extensions[0].call_id ?? 0) > 1);
    const extensionAt = checkpoint.events.indexOf(extensions[0]);
    const lookupProposalAt = checkpoint.events.findIndex(event => event.type === "tool.proposed" && event.name === "lookup");
    const firstLookupAt = checkpoint.events.findIndex(event => event.type === "tool.started" && event.name === "lookup");
    assert.ok(lookupProposalAt >= 0 && lookupProposalAt < extensionAt && extensionAt < firstLookupAt,
      "GraphJin lookup started before its intent and shadow preflight extension were journaled");
    assert.ok((checkpoint.result.cost?.charged_micros ?? 0) >= 512);
  }
  const [admission] = (await pool().query<{ id: string; attempts: number }>(
    "select id,attempts from workflow_api_admission where workflow_run_id=$1", [admitted.runId])).rows;
  await runWorkflowRunFire({ orgId, workflowId, triggerKind: "api", apiAdmissionId: admission.id,
    workflowRunId: admitted.runId, workRunId: run.work_run_id, queueAttempt: admission.attempts });
  assert.deepEqual(await (await fetch(control)).json(), counts, "API queue redelivery repeated model work");
  assert.equal((await pool().query("select count(*)::int as n from action_request where workflow_run_id=$1", [admitted.runId])).rows[0].n, 1);
  assert.equal((await pool().query("select count(*)::int as n from workflow_output where workflow_run_id=$1", [admitted.runId])).rows[0].n, 1);
  assert.equal(effectCount, 0);
  if (process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW === "1" && process.env.OPENNEKO_HARNESS_BUDGET_CANARY !== "1") {
    assert.ok(process.env.HARNESS_BUDGET_EVAL_MANIFEST);
    await writeFile(process.env.HARNESS_BUDGET_EVAL_MANIFEST, JSON.stringify({version: 1, cases: [{
      id: "connected-approval-compaction", root: checkpointRoot, run_id: run.work_run_id,
      checkpoint_sha256: createHash("sha256").update(checkpointBytes).digest("hex"),
      split: "calibration", source: "synthetic",
      label: {task_class: "investigation", outcome: "verified_success", wall_ms: 0},
    }]}));
  }
  if (process.env.HARNESS_BUDGET_COMPARISON_REPORT) {
    await writeFile(process.env.HARNESS_BUDGET_COMPARISON_REPORT, JSON.stringify({
      mode: checkpoint.spec?.host_budget_mode === "canary" ? "canary" : "fixed",
      verified: apiStatus?.status === "completed" && checkpoint.result.status === "completed" &&
        request.status === "pending_approval" && outputs.length === 1 && executions === 0 && effectCount === 0,
      checkpointRoot,
      runId: run.work_run_id,
      checkpointSha256: createHash("sha256").update(checkpointBytes).digest("hex"),
      modelCalls: checkpoint.events.filter(event => event.type === "model.request.started").length,
      ordinaryCalls: counts[`ordinary:${model}`] ?? 0,
      triageCalls: counts["jev-fixture"] ?? 0,
      chargedMicros: checkpoint.result.cost?.charged_micros ?? null,
      wallMS: Date.now() - workflowStartedAt,
    }));
  }
  console.log("M6_CONNECTED_WORKFLOW_APPROVAL_COMPACTION_PASS", run.work_run_id);
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  if (admin) await new Promise<void>(resolve => admin!.close(() => resolve()));
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  if (policyId) await db().delete(action_policy).where(eq(action_policy.id, policyId));
  await db().delete(pack_action_definition).where(eq(pack_action_definition.kind, actionKind));
  if (workflowId) await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  await pool().end();
}

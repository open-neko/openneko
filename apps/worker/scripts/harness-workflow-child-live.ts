// Isolated acceptance: a queued workflow owns two Ax child investigations and one durable output.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { action_policy, db, pool, getOrgId, llm_provider_config, pack_action_definition, workflow_definition, workflow_run, eq } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, approveActionRequest, createActionRequest, enableWorkflowApiAccess, executeApprovedActionRequest, getWorkflowApiRunStatus, registerActionAdapter, updateWorkflowApiLimits } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";
import { createAdminHandler } from "../src/admin-server.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}
const orgId = await getOrgId();
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
if (!prior) throw Error("isolated model configuration missing");
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
const queue = await boss();
const [workflow] = await db().insert(workflow_definition).values({
  org_id: orgId, name: `Child output ${randomUUID()}`, description: "Synthetic reference checks",
  goal: "Investigate the seeded reference twice and record one finding",
  steps: [{ id: "read", description: "Run two independent read-only investigations" },
    { id: "output", description: "Emit one finding from the evidence" }],
}).returning({ id: workflow_definition.id });
const actionKind = "harness_workflow_effect_fixture";
let admin: ReturnType<typeof createServer> | undefined;
try {
  await db().update(llm_provider_config).set({ model: "harness-workflow-child-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-workflow-child-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const jobId = await enqueue(QUEUE.WORKFLOW_RUN_FIRE, { orgId, workflowId: workflow.id, triggerKind: "manual",
    userMessage: "Run the reference checks", queuedAt: Date.now() }, { retryLimit: 0 });
  assert.ok(jobId);
  let run: { id: string; work_run_id: string; status: string; error: string | null } | undefined;
  for (let n = 0; n < 180; n++) {
    [run] = (await pool().query("SELECT id,work_run_id,status,error FROM workflow_run WHERE org_id=$1 AND workflow_id=$2 ORDER BY created_at DESC LIMIT 1",
      [orgId, workflow.id])).rows;
    const job = await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, jobId);
    if (run && ["completed", "failed", "cancelled"].includes(run.status) && ["completed", "failed"].includes(job?.state ?? "")) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  if (run?.status !== "completed") {
    const operations = run ? (await pool().query("SELECT operation_id,request->>'tool' AS tool,result->'response'->>'status' AS response_status,result->>'error' AS error FROM harness_operation WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id",[orgId,run.work_run_id])).rows : [];
    const events = run ? (await pool().query("SELECT kind,payload->>'name' AS name,payload->>'error' AS error FROM work_run_event WHERE org_id=$1 AND run_id=$2 ORDER BY id DESC LIMIT 12",[orgId,run.work_run_id])).rows : [];
    console.error("WORKFLOW_CHILD_DIAGNOSTIC",JSON.stringify({run,operations,events,model:await (await fetch("http://127.0.0.1:18118/control")).json()}));
  }
  assert.equal(run?.status, "completed", JSON.stringify(run));
  assert.equal((await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, jobId))?.state, "completed");
  const outputs = (await pool().query("SELECT id,kind,body FROM workflow_output WHERE org_id=$1 AND workflow_run_id=$2", [orgId,run.id])).rows;
  assert.equal(outputs.length, 1);
  assert.equal(outputs[0].kind, "finding");
  assert.match(outputs[0].body, /REF-42/);
  const operations = (await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id", [orgId,run.work_run_id])).rows;
  assert.equal(operations.length, 3);
  assert.deepEqual(operations.map(op => op.request.tool ?? "lookup"), ["lookup", "lookup", "workflow_output"]);
  assert.equal(operations[2].result.outputId, outputs[0].id);
  const outputEvents = (await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='output_emit' AND payload->>'output_id'=$3",
    [orgId,run.work_run_id,outputs[0].id])).rows[0].n;
  assert.equal(outputEvents, 1);
  const children = (await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='tool_start' AND payload->>'name'='ax_child_agent'",
    [orgId,run.work_run_id])).rows[0].n;
  assert.equal(children, 2);
  console.log("M5_QUEUE_WORKFLOW_CHILD_PASS", run.id);

  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  const adminId = (await pool().query("SELECT solo_admin_user_id FROM organization WHERE id=$1", [orgId])).rows[0].solo_admin_user_id;
  const actor = { userId: adminId, role: "admin" as const };
  const { token } = await enableWorkflowApiAccess({ orgId, workflowId: workflow.id, actor });
  await updateWorkflowApiLimits({ orgId, workflowId: workflow.id, actor,
    limits: { maxModelCalls: 12, maxTokensPerRun: 10_000, maxCostMicrosPerRun: 1_000_000 } });
  const clientFingerprint = `harness-workflow-${orgId}`;
  const admitted = await admitWorkflowApiRun({ workflowId: workflow.id, token,
    idempotencyKey: "child-output-fixture", mode: "single", value: { reference: "REF-42" }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let apiStatus: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 180; n++) {
    apiStatus = await getWorkflowApiRunStatus({ workflowId: workflow.id, runId: admitted.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(apiStatus.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.equal(apiStatus?.status, "completed", JSON.stringify(apiStatus));
  assert.ok(apiStatus.result);
  const apiRun = (await pool().query("SELECT work_run_id FROM workflow_run WHERE id=$1", [admitted.runId])).rows[0];
  const apiReceipt = (await pool().query("SELECT result FROM harness_operation WHERE run_id=$1 AND request->>'tool'='workflow_output'", [apiRun.work_run_id])).rows[0]?.result;
  assert.equal(apiReceipt?.outputId, outputs[0].id); // Same finding is deliberately deduplicated across runs.
  const seenBeforeReplay = (await pool().query("SELECT seen_count FROM workflow_output WHERE id=$1", [apiReceipt.outputId])).rows[0].seen_count;
  assert.equal(seenBeforeReplay, 2);
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE run_id=$1 AND kind='output_emit' AND payload->>'output_id'=$2", [apiRun.work_run_id,apiReceipt.outputId])).rows[0].n, 1);
  assert.equal((await (await fetch("http://127.0.0.1:18118/control")).json())["harness-workflow-child-fixture"], 9);
  const admission = (await pool().query("SELECT id,attempts FROM workflow_api_admission WHERE workflow_run_id=$1", [admitted.runId])).rows[0];
  await runWorkflowRunFire({ orgId, workflowId: workflow.id, triggerKind: "api", apiAdmissionId: admission.id,
    workflowRunId: admitted.runId, workRunId: apiRun.work_run_id, queueAttempt: admission.attempts });
  assert.equal((await pool().query("SELECT seen_count FROM workflow_output WHERE id=$1", [apiReceipt.outputId])).rows[0].seen_count, seenBeforeReplay);
  console.log("M5_API_WORKFLOW_CHILD_PASS", admitted.runId);

  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  await updateWorkflowApiLimits({ orgId, workflowId: workflow.id, actor, limits: { maxModelCalls: 4 } });
  const capped = await admitWorkflowApiRun({ workflowId: workflow.id, token,
    idempotencyKey: "child-output-model-cap", mode: "single", value: { reference: "REF-42" }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let cappedStatus: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 180; n++) {
    cappedStatus = await getWorkflowApiRunStatus({ workflowId: workflow.id, runId: capped.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(cappedStatus.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.equal(cappedStatus?.status, "failed", JSON.stringify(cappedStatus));
  assert.equal((await (await fetch("http://127.0.0.1:18118/control")).json())["harness-workflow-child-fixture"], 4);
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM workflow_output WHERE workflow_run_id=$1", [capped.runId])).rows[0].n, 0);
  console.log("M5_API_WORKFLOW_MODEL_CAP_PASS", capped.runId);

  await db().insert(pack_action_definition).values({ org_id: orgId, kind: actionKind,
    readiness: "ready", definition_hash: "fixture", definition: { kind: actionKind,
      description: "Update the synthetic reference", inputSchema: { type: "object",
        properties: { value: { type: "integer" } }, required: ["value"], additionalProperties: false } } });
  const [policy] = await db().insert(action_policy).values({ org_id: orgId, name: "Harness workflow action fixture",
    mode: "approval_required", applies_to_kinds: [actionKind], applies_to_scopes: ["external"] }).returning({ id: action_policy.id });
  admin = createServer(createAdminHandler({ actionRequests: { create: async input => {
    const request = await createActionRequest(input as Parameters<typeof createActionRequest>[0]);
    return { id: request.id, status: request.status };
  } } }));
  await new Promise<void>(resolve => admin!.listen(18122, "127.0.0.1", resolve));
  await db().update(llm_provider_config).set({ model: "harness-workflow-action-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-workflow-action-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  await updateWorkflowApiLimits({ orgId, workflowId: workflow.id, actor, limits: { maxModelCalls: 12 } });
  const actionRun = await admitWorkflowApiRun({ workflowId: workflow.id, token,
    idempotencyKey: "workflow-pack-action-fixture", mode: "single", value: { reference: "REF-42" }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let actionStatus: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 180; n++) {
    actionStatus = await getWorkflowApiRunStatus({ workflowId: workflow.id, runId: actionRun.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(actionStatus.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.equal(actionStatus?.status, "completed", JSON.stringify(actionStatus));
  const [actionRow] = (await pool().query("SELECT work_run_id FROM workflow_run WHERE id=$1", [actionRun.runId])).rows;
  const [request] = (await pool().query("SELECT id,status,workflow_run_id,work_run_id,requested_by_run_id,harness_operation_id,harness_prepared,actor_backend FROM action_request WHERE org_id=$1 AND workflow_run_id=$2", [orgId,actionRun.runId])).rows;
  assert.ok(request);
  assert.equal(request.status, "pending_approval");
  assert.equal(request.workflow_run_id, actionRun.runId);
  assert.equal(request.work_run_id, actionRow.work_run_id);
  assert.equal(request.requested_by_run_id, actionRun.runId);
  assert.equal(request.actor_backend, "harness");
  assert.ok(request.harness_operation_id > 0 && request.harness_prepared);
  const [actionOutput] = (await pool().query("SELECT id,kind FROM workflow_output WHERE workflow_run_id=$1", [actionRun.runId])).rows;
  assert.equal(actionOutput?.kind, "finding");
  const actionOps = (await pool().query("SELECT request,result FROM harness_operation WHERE run_id=$1 ORDER BY operation_id", [actionRow.work_run_id])).rows;
  assert.deepEqual(actionOps.map(op => op.request.tool), ["propose", "workflow_output"]);
  assert.equal(actionOps[0].result.id, request.id);
  assert.equal(actionOps[1].result.outputId, actionOutput.id);
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1 AND action_request_id=$2", [orgId,request.id])).rows[0].n, 0);
  const actionAdmission = (await pool().query("SELECT id,attempts FROM workflow_api_admission WHERE workflow_run_id=$1", [actionRun.runId])).rows[0];
  await runWorkflowRunFire({ orgId, workflowId: workflow.id, triggerKind: "api", apiAdmissionId: actionAdmission.id,
    workflowRunId: actionRun.runId, workRunId: actionRow.work_run_id, queueAttempt: actionAdmission.attempts });
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1 AND workflow_run_id=$2", [orgId,actionRun.runId])).rows[0].n, 1);
  let effects = 0;
  registerActionAdapter(actionKind, async ({ idempotencyKey }) => { effects++; return { result: { value: 42 }, externalRef: idempotencyKey }; });
  await approveActionRequest({ orgId, id: request.id, approverUserId: null, approver: { userId: null, role: "admin" } });
  const executed = await executeApprovedActionRequest(orgId, request.id);
  assert.equal(executed.ok, true);
  assert.equal(effects, 1);
  const repeated = await executeApprovedActionRequest(orgId, request.id);
  assert.equal(repeated.ok, true);
  assert.equal(effects, 1);
  console.log("M5_API_WORKFLOW_ACTION_PASS", actionRun.runId);
  await db().delete(action_policy).where(eq(action_policy.id, policy.id));
  await db().delete(pack_action_definition).where(eq(pack_action_definition.kind, actionKind));
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  if (admin) await new Promise<void>(resolve => admin!.close(() => resolve()));
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  await db().delete(workflow_definition).where(eq(workflow_definition.id, workflow.id));
  await pool().end();
}

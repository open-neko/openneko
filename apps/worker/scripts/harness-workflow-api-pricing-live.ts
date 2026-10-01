// An unpriced Harness API run must fail before creating a sandbox or calling a model.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, enableWorkflowApiAccess, getWorkflowApiRunStatus } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119" || process.env.OPENNEKO_HARNESS_ROUTING) {
  throw Error("isolated unpriced M3 environment required");
}
const orgId = await getOrgId();
const queue = await boss();
const control = "http://127.0.0.1:18118/control";
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
let workflowId: string | undefined;
try {
  assert.equal((await fetch(control, { method: "POST", body: "{}" })).status, 204);
  await db().update(llm_provider_config).set({ model: "harness-job-model-only-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-job-model-only-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [workflow] = await db().insert(workflow_definition).values({
    org_id: orgId, name: `Unpriced API preflight ${randomUUID()}`, goal: "Answer the fixture request",
  }).returning({ id: workflow_definition.id });
  workflowId = workflow.id;
  const [org] = (await pool().query<{ solo_admin_user_id: string | null }>(
    "select solo_admin_user_id from organization where id=$1", [orgId])).rows;
  assert.ok(org);
  const actor = { userId: org.solo_admin_user_id, role: "admin" as const };
  const { token } = await enableWorkflowApiAccess({ orgId, workflowId, actor });
  const clientFingerprint = `harness-pricing-${orgId}`;
  const admitted = await admitWorkflowApiRun({ workflowId, token,
    idempotencyKey: `unpriced-${randomUUID()}`, mode: "single", value: { fixture: true }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let status: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 90; n++) {
    status = await getWorkflowApiRunStatus({ workflowId, runId: admitted.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(status.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.equal(status?.status, "failed", JSON.stringify(status));
  assert.equal(status.error?.code, "harness_pricing_required", JSON.stringify(status));
  const [run] = (await pool().query<{ work_run_id: string }>(
    "select work_run_id from workflow_run where id=$1", [admitted.runId])).rows;
  assert.ok(run);
  assert.equal((await pool().query("select count(*)::int as n from harness_operation where run_id=$1", [run.work_run_id])).rows[0].n, 0);
  const counts = await (await fetch(control)).json() as Record<string, number>;
  assert.equal(counts["harness-job-model-only-fixture"] ?? 0, 0);
  console.log("M6_CONNECTED_API_PRICING_PREFLIGHT_PASS", run.work_run_id);
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  if (workflowId) await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  await pool().end();
}

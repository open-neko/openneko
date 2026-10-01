// Connected actor-step exhaustion through a queued source-change workflow.
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { data_source, db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { createSubscription, dispatchPendingSourceChangeDeliveries, recordSourceChangeDelivery } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 environment required");
const orgId = await getOrgId();
const queue = await boss();
const control = "http://127.0.0.1:18118/control";
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
const workflowIds: string[] = [];
try {
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [source] = await db().select({ id: data_source.id }).from(data_source).where(eq(data_source.org_id, orgId)).limit(1);
  assert.ok(source);
  for (const hasOutput of [true, false]) {
    const model = hasOutput ? "harness-finalizer-output-fixture" : "harness-finalizer-empty-fixture";
    assert.equal((await fetch(control, { method: "POST", body: "{}" })).status, 204);
    await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, prior.id));
    await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);
    const [workflow] = await db().insert(workflow_definition).values({
      org_id: orgId, name: `Finalizer ${hasOutput ? "output" : "empty"} ${randomUUID()}`,
      goal: "Report the seeded source-change reference through a committed workflow finding",
    }).returning({ id: workflow_definition.id });
    workflowIds.push(workflow.id);
    const subscription = await createSubscription({ orgId, workflowId: workflow.id,
      sourceKind: "source_change", filter: { table: "references", primary_key: ["id"] } });
    const delivery = await recordSourceChangeDelivery({ orgId, workflowId: workflow.id,
      subscriptionId: subscription.id, subscriptionUpdatedAt: subscription.updatedAt,
      sourceId: source.id, deliveryKey: `finalizer-${randomUUID()}`,
      match: { table: "references", primary_key: { id: "REF-42" }, snapshot: { id: "REF-42" },
        version_token: `finalizer-${randomUUID()}` } });
    assert.equal(await dispatchPendingSourceChangeDeliveries(), 1);
    const [queued] = (await pool().query<{ queue_job_id: string }>(
      "select queue_job_id from source_change_delivery where id=$1", [delivery.id])).rows;
    assert.ok(queued.queue_job_id);
    let run: { id: string; work_run_id: string; status: string } | undefined;
    let jobState: string | undefined;
    for (let n = 0; n < 180; n++) {
      [run] = (await pool().query<{ id: string; work_run_id: string; status: string }>(
        "select id,work_run_id,status from workflow_run where org_id=$1 and workflow_id=$2 order by created_at desc limit 1",
        [orgId, workflow.id])).rows;
      jobState = (await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, queued.queue_job_id))?.state;
      if (run && ["completed", "failed"].includes(jobState ?? "")) break;
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    assert.ok(run, `${model} did not create a workflow run`);
    const [work] = (await pool().query<{ status: string; error: string | null }>(
      "select status,error from work_run where id=$1", [run.work_run_id])).rows;
    const checkpoint = JSON.parse(await readFile(join(getOrgAgentRoot(orgId), "runs", run.work_run_id,
      ".harness", `${createHash("sha256").update(run.work_run_id).digest("hex")}.json`), "utf8")) as {
        result: { status: string; code?: string }; events: Array<{ type: string; stage?: string; terminal?: { accepted: boolean } }>;
      };
    const operations = (await pool().query<{ operation_id: number; result: unknown }>(
      "select operation_id,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
      [orgId, run.work_run_id])).rows;
    const outputs = (await pool().query<{ n: number }>(
      "select count(*)::int as n from workflow_output where workflow_run_id=$1", [run.id])).rows[0].n;
    const calls = (await (await fetch(control)).json() as Record<string, number>)[model];
    console.log("M6_FINALIZER_CASE", JSON.stringify({ hasOutput, jobState, run, work, result: checkpoint.result,
      calls, operations: operations.length, outputs,
      finalizerEvents: checkpoint.events.filter(e => e.type.startsWith("finalizer.")) }));
    assert.equal(jobState, "completed");
    assert.equal(calls, hasOutput ? 10 : 9);
    assert.equal(operations.length, hasOutput ? 1 : 0);
    assert.equal(outputs, hasOutput ? 1 : 0);
    assert.equal(checkpoint.result.status, hasOutput ? "completed" : "failed");
    assert.equal(run.status, hasOutput ? "completed" : "failed");
    assert.equal(checkpoint.events.filter(e => e.type === "finalizer.admitted").length, hasOutput ? 1 : 0);
    assert.equal(checkpoint.events.filter(e => e.type === "finalizer.denied").length, hasOutput ? 0 : 1);
    assert.equal(checkpoint.events.filter(e => e.type === "model.request.started" && e.stage === "terminal_finalizer").length,
      hasOutput ? 1 : 0);
    assert.equal(checkpoint.events.find(e => e.type === "terminal.checked")?.terminal?.accepted, hasOutput ? true : undefined);
    if (!hasOutput) assert.equal(checkpoint.result.code, "actor_steps_exhausted");
    const replay = await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, queued.queue_job_id);
    assert.ok(replay);
    await runWorkflowRunFire(replay.data as WorkflowRunFirePayload);
    assert.equal((await (await fetch(control)).json() as Record<string, number>)[model], calls,
      "queue redelivery repeated model work");
  }
  console.log("M6_CONNECTED_WORKFLOW_FINALIZER_PASS");
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  for (const id of workflowIds) await db().delete(workflow_definition).where(eq(workflow_definition.id, id));
  await pool().end();
}

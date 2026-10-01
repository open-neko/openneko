// Connected M6 compaction: a file artifact, broker receipt and source constraint survive an Ax summary.
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
const model = "harness-compaction-output-fixture";
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
let workflowId: string | undefined;
try {
  assert.equal((await fetch(control, { method: "POST", body: "{}" })).status, 204);
  await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [workflow] = await db().insert(workflow_definition).values({
    org_id: orgId, name: `Compacted evidence ${randomUUID()}`,
    goal: "Read the seeded reference, create a CSV artifact, emit a file output, then verify the saved output receipt. Never execute a change without approval.",
  }).returning({ id: workflow_definition.id });
  workflowId = workflow.id;
  const [source] = await db().select({ id: data_source.id }).from(data_source).where(eq(data_source.org_id, orgId)).limit(1);
  assert.ok(source);
  const subscription = await createSubscription({ orgId, workflowId, sourceKind: "source_change",
    filter: { table: "references", primary_key: ["id"] } });
  const delivery = await recordSourceChangeDelivery({ orgId, workflowId,
    subscriptionId: subscription.id, subscriptionUpdatedAt: subscription.updatedAt,
    sourceId: source.id, deliveryKey: `compaction-${randomUUID()}`,
    match: { table: "references", primary_key: { id: "REF-42" }, snapshot: { id: "REF-42" },
      version_token: `compaction-${randomUUID()}` } });
  assert.equal(await dispatchPendingSourceChangeDeliveries(), 1);
  const [queued] = (await pool().query<{ queue_job_id: string }>(
    "select queue_job_id from source_change_delivery where id=$1", [delivery.id])).rows;
  const jobId = queued.queue_job_id;
  assert.ok(jobId);
  let run: { id: string; work_run_id: string; status: string; error: string | null } | undefined;
  let jobState: string | undefined;
  for (let n = 0; n < 180; n++) {
    [run] = (await pool().query<{ id: string; work_run_id: string; status: string; error: string | null }>(
      "select id,work_run_id,status,error from workflow_run where org_id=$1 and workflow_id=$2 order by created_at desc limit 1",
      [orgId, workflowId])).rows;
    jobState = (await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, jobId))?.state;
    if (run && ["completed", "failed"].includes(jobState ?? "")) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.ok(run);
  const counts = await (await fetch(control)).json() as Record<string, number>;
  const checkpoint = JSON.parse(await readFile(join(getOrgAgentRoot(orgId), "runs", run.work_run_id,
    ".harness", `${createHash("sha256").update(run.work_run_id).digest("hex")}.json`), "utf8")) as {
      result: { status: string; answer?: string; code?: string };
      operations: Array<{ tool: string; result: unknown }>;
      events: Array<{ type: string; stage?: string; terminal?: { accepted: boolean } }>;
    };
  const operations = (await pool().query<{ operation_id: number; request: { tool?: string }; result: unknown }>(
    "select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
    [orgId, run.work_run_id])).rows;
  const outputs = (await pool().query<{ id: string; kind: string; artifact_path: string | null }>(
    "select id,kind,artifact_path from workflow_output where workflow_run_id=$1", [run.id])).rows;
  const artifact = await readFile(join(getOrgAgentRoot(orgId), "runs", run.work_run_id, "artifacts", "result.csv"));
  const expectedArtifact = Buffer.from(`lead_id\n${"LEAD-42\n".repeat(6500)}`);
  console.log("M6_CONNECTED_COMPACTION_DIAGNOSTIC", JSON.stringify({ run, jobState, result: { status: checkpoint.result.status, code: checkpoint.result.code, answer: checkpoint.result.answer },
    calls: counts[model], ordinary: counts[`ordinary:${model}`], summaries: counts[`summary:${model}`], summaryAt: counts[`summary-at:${model}`],
    largestRequest: counts[`max-request:${model}`], operations: operations.map(op => op.request.tool), outputs: outputs.length }));
  assert.equal(jobState, "completed");
  assert.equal(run.status, "completed");
  assert.equal(checkpoint.result.status, "completed");
  assert.match(checkpoint.result.answer ?? "", /REF-42/);
  assert.equal(counts[`summary:${model}`], 1, "Ax did not compact the mixed-tool trajectory exactly once");
  assert.equal(counts[`ordinary:${model}`], 9);
  assert.ok((counts[`summary-at:${model}`] ?? Infinity) <= 8, "summary occurred after the responder");
  assert.ok((counts[`max-request:${model}`] ?? Infinity) < 50_000, "file content entered model context");
  assert.equal(operations.length, 5);
  assert.equal(checkpoint.operations.length, 7);
  assert.equal(checkpoint.operations[1]?.tool, "file_write");
  assert.equal(checkpoint.operations[2]?.tool, "file_read");
  assert.equal(checkpoint.operations[3]?.tool, "workflow_output_emit");
  assert.equal(artifact.length, expectedArtifact.length);
  assert.equal(createHash("sha256").update(artifact).digest("hex"),
    createHash("sha256").update(expectedArtifact).digest("hex"));
  assert.equal(outputs.length, 1);
  assert.equal(outputs[0].kind, "file");
  assert.equal(outputs[0].artifact_path, "result.csv");
  const [published] = (await pool().query<{ result_artifact_path: string | null; artifact_events: number }>(
    `select r.result_artifact_path,
       (select count(*)::int from work_run_event e where e.org_id=r.org_id and e.run_id=r.work_run_id and e.kind='artifact') as artifact_events
     from workflow_run r where r.id=$1`, [run.id])).rows;
  console.log("M6_CONNECTED_ARTIFACT_PUBLICATION", JSON.stringify(published));
  assert.equal(published.result_artifact_path, `runs/${run.work_run_id}/artifacts/result.csv`);
  assert.equal(published.artifact_events, 1);
  assert.equal((operations[1].result as { outputId?: string }).outputId, outputs[0].id);
  assert.equal(checkpoint.events.find(event => event.type === "terminal.checked")?.terminal?.accepted, true);
  const before = JSON.stringify({ counts, operations, outputs });
  const replay = await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE, jobId);
  assert.ok(replay);
  await runWorkflowRunFire(replay.data as WorkflowRunFirePayload);
  const after = JSON.stringify({ counts: await (await fetch(control)).json(),
    operations: (await pool().query("select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id", [orgId, run.work_run_id])).rows,
    outputs: (await pool().query("select id,kind,artifact_path from workflow_output where workflow_run_id=$1", [run.id])).rows });
  assert.equal(after, before, "completed queue redelivery repeated model or broker work");
  assert.equal(createHash("sha256").update(await readFile(join(getOrgAgentRoot(orgId), "runs", run.work_run_id,
    "artifacts", "result.csv"))).digest("hex"), createHash("sha256").update(expectedArtifact).digest("hex"));
  if (process.env.HARNESS_M6_COMPACTION_WEB === "1") {
    assert.ok(process.env.HARNESS_STATE);
    await writeFile(join(process.env.HARNESS_STATE, "m6-compaction-work-run"), run.work_run_id);
    await writeFile(join(process.env.HARNESS_STATE, "m6-compaction-workflow-run"), run.id);
  }
  console.log("M6_CONNECTED_WORKFLOW_COMPACTION_PASS", run.work_run_id);
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  // The optional web gate needs the workflow/run rows after this process exits.
  // Its isolated Postgres volume is removed when the gate completes.
  if (workflowId && process.env.HARNESS_M6_COMPACTION_WEB !== "1") {
    await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  }
  await pool().end();
}

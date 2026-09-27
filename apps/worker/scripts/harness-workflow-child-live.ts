// Isolated acceptance: a queued workflow owns two Ax child investigations and one durable output.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, pool, getOrgId, llm_provider_config, workflow_definition, workflow_run, eq } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { shutdownAgentBroker } from "@neko/llm/work";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";

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
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  await db().delete(workflow_definition).where(eq(workflow_definition.id, workflow.id));
  await pool().end();
}

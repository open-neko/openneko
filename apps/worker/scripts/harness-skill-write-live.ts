// Connected skill-write gate through the production Work queue and OpenShell.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, getOrCreateSoloAdmin, llm_provider_config, pool, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { createWorkRun, createWorkThread, ensureWorkWorkspace, shutdownAgentBroker } from "@neko/llm/work";
import { runWorkRun } from "../src/jobs/work-run.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}

const orgId = await getOrgId();
const actor = await getOrCreateSoloAdmin(orgId);
assert.ok(actor);
const [provider] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(provider);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const originalConfig = await readFile(configPath, "utf8");
const queue = await boss();

type FinishedRun = { id: string; threadId: string; workspace: Awaited<ReturnType<typeof ensureWorkWorkspace>> };

async function setModel(model: string): Promise<void> {
  await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, provider.id));
  await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);
  const response = await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  assert.equal(response.status, 204);
}

async function waitForJob(jobId: string): Promise<void> {
  for (let attempt = 0; attempt < 120; attempt++) {
    const job = await queue.getJobById(QUEUE.WORK_RUN, jobId);
    if (job?.state === "failed") throw Error(`Skill Work job failed: ${JSON.stringify(job.output)}`);
    if (job?.state === "completed") return;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw Error("Skill Work job timed out");
}

async function deliver(runId: string, threadId: string, message: string, trigger: string): Promise<void> {
  const [job] = await db().insert(processing_job).values({ org_id: orgId, kind: QUEUE.WORK_RUN, trigger })
    .returning({ id: processing_job.id });
  const jobId = await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId, runId, threadId, message },
    { retryLimit: 0 });
  assert.ok(jobId);
  await waitForJob(jobId);
  const [run] = (await pool().query<{status: string}>(
    "select status from work_run where org_id=$1 and id=$2", [orgId, runId])).rows;
  assert.equal(run?.status, "completed");
}

async function startRun(model: string, message: string, title: string): Promise<FinishedRun> {
  await setModel(model);
  const thread = await createWorkThread(orgId, title, "web", actor.id);
  const run = await createWorkRun(orgId, thread.id, "harness", { userId: actor.id, role: "admin" });
  const workspace = await ensureWorkWorkspace(orgId, thread.id, run.id);
  await deliver(run.id, thread.id, message, `test-skill-${model}`);
  return { id: run.id, threadId: thread.id, workspace };
}

async function operations(runId: string): Promise<Array<{operation_id: number; request: {tool: string}; result: Record<string, unknown>}>> {
  return (await pool().query(
    "select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
    [orgId, runId])).rows;
}

async function snapshotTools(run: FinishedRun): Promise<string[]> {
  const path = join(run.workspace.runRoot, ".harness", `${createHash("sha256").update(run.id).digest("hex")}.json`);
  const snapshot = JSON.parse(await readFile(path, "utf8")) as {operations: Array<{tool: string}>};
  return snapshot.operations.map(operation => operation.tool);
}

async function assertNoRedeliveryEffect(run: FinishedRun, message: string): Promise<void> {
  const priorOperations = await operations(run.id);
  const priorCounts = await (await fetch("http://127.0.0.1:18118/control")).json();
  const [priorEvents] = (await pool().query<{n: number}>(
    "select count(*)::int as n from work_run_event where org_id=$1 and run_id=$2 and kind in ('message','artifact','surface','needs_input')",
    [orgId, run.id])).rows;
  await deliver(run.id, run.threadId, message, "test-skill-redelivery");
  assert.deepEqual(await operations(run.id), priorOperations, "redelivery must reuse the host receipt");
  assert.deepEqual(await (await fetch("http://127.0.0.1:18118/control")).json(), priorCounts,
    "redelivery must not call the model again");
  const [afterEvents] = (await pool().query<{n: number}>(
    "select count(*)::int as n from work_run_event where org_id=$1 and run_id=$2 and kind in ('message','artifact','surface','needs_input')",
    [orgId, run.id])).rows;
  assert.equal(afterEvents.n, priorEvents.n, "redelivery must not duplicate user-visible results");
}

try {
  await queue.createQueue(QUEUE.WORK_RUN);
  await queue.work<WorkRunPayload>(QUEUE.WORK_RUN, async jobs => {
    for (const job of jobs) {
      const { processingJobId, orgId: jobOrg, ...payload } = job.data;
      await db().update(processing_job).set({ status: "running" }).where(eq(processing_job.id, processingJobId));
      try {
        await runWorkRun(processingJobId, jobOrg, { ...payload, channel: "web" });
        await db().update(processing_job).set({ status: "succeeded" }).where(eq(processing_job.id, processingJobId));
      } catch (error) {
        await db().update(processing_job).set({ status: "failed" }).where(eq(processing_job.id, processingJobId));
        throw error;
      }
    }
  });

  const createMessage = "Create a lead CSV review skill for this organization.";
  const created = await startRun("harness-skill-create-fixture", createMessage, "M5 queued skill creation");
  const skill = join(created.workspace.skillsRoot, "fixture-lead-review");
  assert.match(await readFile(join(skill, "SKILL.md"), "utf8"), /Read the selected CSV/);
  assert.match(await readFile(join(skill, "scripts/check.py"), "utf8"), /skill fixture/);
  assert.deepEqual(await snapshotTools(created), ["skill_create"]);
  const createdOperations = await operations(created.id);
  assert.equal(createdOperations.length, 1);
  assert.equal(createdOperations[0].request.tool, "skill_create");
  assert.equal(createdOperations[0].result.ok, true);
  await assertNoRedeliveryEffect(created, createMessage);
  await writeFile(join(process.env.HARNESS_STATE!, "m5-skill-create-thread"), created.threadId);
  console.log("M5_QUEUE_SKILL_CREATE_PASS", created.id);

  const read = await startRun("harness-skill-read-fixture", "Read the installed skill.", "M5 queued skill read");
  assert.deepEqual(await snapshotTools(read), ["skill_read"]);
  assert.deepEqual(await operations(read.id), []);

  const updateMessage = "Inspect and update the lead review skill to verify owners.";
  const updated = await startRun("harness-skill-update-fixture", updateMessage, "M5 queued skill update");
  assert.deepEqual(await snapshotTools(updated), ["skill_inspect", "skill_update"]);
  const updatedOperations = await operations(updated.id);
  assert.equal(updatedOperations.length, 1);
  assert.equal(updatedOperations[0].request.tool, "skill_update");
  assert.equal(updatedOperations[0].result.ok, true);
  assert.match(await readFile(join(skill, "SKILL.md"), "utf8"), /Verify the lead owner/);
  assert.deepEqual(await readdir(join(skill, "scripts")), ["new_check.py"]);
  await assertNoRedeliveryEffect(updated, updateMessage);
  await writeFile(join(process.env.HARNESS_STATE!, "m5-skill-update-thread"), updated.threadId);
  console.log("M5_QUEUE_SKILL_UPDATE_PASS", updated.id);

  const final = await startRun("harness-skill-read-updated-fixture", "Read the updated skill.", "M5 queued updated skill read");
  assert.deepEqual(await snapshotTools(final), ["skill_read"]);
  assert.deepEqual(await operations(final.id), []);
  console.log("M5_QUEUE_SKILL_VISIBILITY_PASS", final.id);
} finally {
  await db().update(llm_provider_config).set({ model: provider.model }).where(eq(llm_provider_config.id, provider.id));
  await writeFile(configPath, originalConfig);
  await queue.stop({ graceful: true, timeout: 5000 });
  await shutdownAgentBroker();
  await pool().end();
}

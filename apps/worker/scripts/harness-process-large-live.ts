// Connected M6 gate: publish a file created inside the isolated process
// compartment, then let the public Work route verify its exact bytes.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, getOrCreateSoloAdmin, pool, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { createWorkRun, createWorkThread, ensureWorkWorkspace, shutdownAgentBroker } from "@neko/llm/work";
import { runWorkRun } from "../src/jobs/work-run.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}

const orgId = await getOrgId();
const actor = await getOrCreateSoloAdmin(orgId);
assert.ok(actor);
const queue = await boss();

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

  assert.equal((await fetch("http://127.0.0.1:18118/control", {
    method: "POST", body: JSON.stringify({ process_large: true }),
  })).status, 204);
  const thread = await createWorkThread(orgId, "M6 large isolated artifact", "web", actor.id);
  const run = await createWorkRun(orgId, thread.id, "harness", { userId: actor.id, role: "admin" });
  const workspace = await ensureWorkWorkspace(orgId, thread.id, run.id);
  const [job] = await db().insert(processing_job).values({ org_id: orgId,
    kind: QUEUE.WORK_RUN, trigger: "test-large-isolated-process" }).returning({ id: processing_job.id });
  const jobId = await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId,
    runId: run.id, threadId: thread.id, message: "Create the large isolated artifact." },
  { retryLimit: 0 });
  assert.ok(jobId);
  let finished = false;
  for (let attempt = 0; attempt < 120; attempt++) {
    const state = await queue.getJobById(QUEUE.WORK_RUN, jobId);
    if (state?.state === "failed") throw Error(`Large process Work job failed: ${JSON.stringify(state.output)}`);
    if (state?.state === "completed") { finished = true; break; }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.ok(finished, "large process Work job timed out");
  const [status] = (await pool().query<{status: string}>(
    "select status from work_run where org_id=$1 and id=$2", [orgId, run.id])).rows;
  assert.equal(status.status, "completed");
  const bytes = await readFile(join(workspace.artifactRoot, "process-1", "large.bin"));
  assert.equal(bytes.length, 8 << 20);
  assert.ok(bytes.every(byte => byte === 65));
  const [operation] = (await pool().query<{result: {ok: boolean; files: Array<{sha256: string; bytes: number}>}}>(
    "select result from harness_operation where org_id=$1 and run_id=$2 and request->>'tool'='process_run'",
    [orgId, run.id])).rows;
  assert.equal(operation.result.ok, true);
  assert.equal(operation.result.files[0]?.bytes, bytes.length);
  assert.equal(operation.result.files[0]?.sha256, createHash("sha256").update(bytes).digest("hex"));
  const events = (await pool().query<{path: string}>(
    "select payload->'artifact'->>'path' as path from work_run_event where org_id=$1 and run_id=$2 and kind='artifact'",
    [orgId, run.id])).rows;
  assert.deepEqual(events.map(event => event.path), [`runs/${run.id}/artifacts/process-1/large.bin`]);
  await writeFile(join(process.env.HARNESS_STATE!, "m6-large-process-run"), run.id);
  console.log("M6_QUEUE_LARGE_ARTIFACT_PASS", run.id);
} finally {
  await queue.stop({ graceful: true, timeout: 5000 });
  await shutdownAgentBroker();
  await pool().end();
}

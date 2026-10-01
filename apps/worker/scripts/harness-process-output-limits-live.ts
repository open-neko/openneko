// Connected Work resource gate: oversized files never publish, while noisy
// command output is bounded in the model-visible receipt and durable journal.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, getOrCreateSoloAdmin, pool, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { createWorkRun, createWorkThread, ensureWorkWorkspace, shutdownAgentBroker } from "@neko/llm/work";
import { runWorkRun } from "../src/jobs/work-run.js";
import { sandboxExists } from "./harness-openshell-inventory.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}

const orgId = await getOrgId();
const actor = await getOrCreateSoloAdmin(orgId);
assert.ok(actor);
const queue = await boss();
const cli = process.env.HARNESS_OPENSHELL_BIN;
assert.ok(cli);
const control = "http://127.0.0.1:18118/control";

async function waitFor(condition: () => Promise<boolean>, label: string): Promise<void> {
  for (let attempt = 0; attempt < 120; attempt++) {
    if (await condition()) return;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  throw Error(`Timed out waiting for ${label}`);
}

async function sandboxGone(name: string): Promise<boolean> {
  return !await sandboxExists(cli!, name);
}

async function runCase(flag: "process_oversize" | "process_flood") {
  assert.equal((await fetch(control, { method: "POST", body: JSON.stringify({ [flag]: true }) })).status, 204);
  const thread = await createWorkThread(orgId, `M5 ${flag}`, "web", actor.id);
  const run = await createWorkRun(orgId, thread.id, "harness", { userId: actor.id, role: "admin" });
  const workspace = await ensureWorkWorkspace(orgId, thread.id, run.id);
  const [job] = await db().insert(processing_job).values({ org_id: orgId,
    kind: QUEUE.WORK_RUN, trigger: `test-${flag}` }).returning({ id: processing_job.id });
  const jobId = await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId,
    runId: run.id, threadId: thread.id, message: `Run the ${flag} fixture.` }, { retryLimit: 0 });
  assert.ok(jobId);
  await waitFor(async () => {
    const current = await queue.getJobById(QUEUE.WORK_RUN, jobId);
    return current?.state === "completed" || current?.state === "failed";
  }, `${flag} queue completion`);
  const name = `hp-${createHash("sha256").update(`${run.id}:1`).digest("hex").slice(0, 16)}`;
  await waitFor(() => sandboxGone(name), `${flag} sandbox deletion`);
  const [state] = (await pool().query<{ status: string }>(
    "select status from work_run where org_id=$1 and id=$2", [orgId, run.id])).rows;
  const [operation] = (await pool().query<{ result: Record<string, unknown> | null }>(
    "select result from harness_operation where org_id=$1 and run_id=$2 and request->>'tool'='process_run'",
    [orgId, run.id])).rows;
  assert.ok(operation, `${flag} must have a journaled dispatch`);
  const [artifacts] = (await pool().query<{ count: number }>(
    "select count(*)::int as count from work_run_event where org_id=$1 and run_id=$2 and kind='artifact'",
    [orgId, run.id])).rows;
  return { run, workspace, state, operation, artifacts };
}

try {
  await queue.createQueue(QUEUE.WORK_RUN);
  await queue.work<WorkRunPayload>(QUEUE.WORK_RUN, async jobs => {
    for (const job of jobs) {
      const { processingJobId, orgId: jobOrg, ...payload } = job.data;
      await db().update(processing_job).set({ status: "running" })
        .where(eq(processing_job.id, processingJobId));
      try {
        await runWorkRun(processingJobId, jobOrg, { ...payload, channel: "web" });
        await db().update(processing_job).set({ status: "succeeded" })
          .where(eq(processing_job.id, processingJobId));
      } catch (error) {
        await db().update(processing_job).set({ status: "failed" })
          .where(eq(processing_job.id, processingJobId));
        throw error;
      }
    }
  });

  const oversized = await runCase("process_oversize");
  assert.equal(oversized.state.status, "failed");
  assert.equal(oversized.operation.result, null);
  assert.equal(oversized.artifacts.count, 0);
  await assert.rejects(readFile(join(oversized.workspace.artifactRoot, "process-1", "oversize.bin")),
    { code: "ENOENT" });
  console.log("M5_QUEUE_PROCESS_OVERSIZE_PASS", oversized.run.id);

  const flooded = await runCase("process_flood");
  assert.equal(flooded.state.status, "completed");
  assert.equal(flooded.artifacts.count, 1);
  assert.equal(flooded.operation.result?.outputTruncated, true);
  assert.ok(typeof flooded.operation.result?.output === "string" &&
    flooded.operation.result.output.length <= 8192);
  assert.equal(await readFile(join(flooded.workspace.artifactRoot, "process-1", "result.csv"), "utf8"),
    "lead_id\nLEAD-42\n");
  console.log("M5_QUEUE_PROCESS_FLOOD_PASS", flooded.run.id);
} finally {
  await queue.stop({ graceful: true, timeout: 5000 });
  await shutdownAgentBroker();
  await pool().end();
}

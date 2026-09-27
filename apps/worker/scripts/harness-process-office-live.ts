// Connected Office artifact gate: one queued Work turn transforms a selected
// upload inside the credential-free process compartment, then the public route
// returns both exact validated packages to the authorized actor.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
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
const runCommand = promisify(execFile);

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
    method: "POST", body: JSON.stringify({ process_office: true }),
  })).status, 204);
  const thread = await createWorkThread(orgId, "M5 isolated Office artifacts", "web", actor.id);
  const run = await createWorkRun(orgId, thread.id, "harness", { userId: actor.id, role: "admin" });
  const workspace = await ensureWorkWorkspace(orgId, thread.id, run.id);
  await mkdir(join(workspace.skillsRoot, "office-fixture"), {recursive: true});
  await writeFile(join(workspace.skillsRoot, "office-fixture", "SKILL.md"),
    "---\nname: office-fixture\ndescription: Create Office files from an uploaded lead CSV\n---\nOFFICE-SKILL-MARKER: Read the selected CSV with a deterministic script. Skills do not call models directly.\n");
  await writeFile(join(workspace.threadUploadsRoot, "lead.csv"), "lead_id\nLEAD-42\n");
  const [job] = await db().insert(processing_job).values({ org_id: orgId,
    kind: QUEUE.WORK_RUN, trigger: "test-office-artifacts" }).returning({ id: processing_job.id });
  const jobId = await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId,
    runId: run.id, threadId: thread.id, message: "Generate a spreadsheet and document from the selected lead upload." },
  { retryLimit: 0 });
  assert.ok(jobId);
  let finished = false;
  for (let attempt = 0; attempt < 120; attempt++) {
    const state = await queue.getJobById(QUEUE.WORK_RUN, jobId);
    if (state?.state === "failed") throw Error(`Office Work job failed: ${JSON.stringify(state.output)}`);
    if (state?.state === "completed") { finished = true; break; }
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  assert.ok(finished, "Office Work job timed out");
  const [status] = (await pool().query<{status: string}>(
    "select status from work_run where org_id=$1 and id=$2", [orgId, run.id])).rows;
  assert.equal(status.status, "completed");
  const snapshot = JSON.parse(await readFile(join(workspace.runRoot, ".harness",
    `${createHash("sha256").update(run.id).digest("hex")}.json`), "utf8")) as
    {operations: Array<{tool: string}>};
  assert.deepEqual(snapshot.operations.map(operation => operation.tool), ["skill_read", "process_run"]);
  const [operation] = (await pool().query<{operation_id: number; result: {ok: boolean; files: Array<{path: string; sha256: string; bytes: number}>}}>(
    "select operation_id,result from harness_operation where org_id=$1 and run_id=$2 and request->>'tool'='process_run'",
    [orgId, run.id])).rows;
  assert.equal(operation.operation_id, 2);
  assert.equal(operation.result.ok, true);
  assert.equal(operation.result.files.length, 2);
  const events = (await pool().query<{path: string}>(
    "select payload->'artifact'->>'path' as path from work_run_event where org_id=$1 and run_id=$2 and kind='artifact' order by id",
    [orgId, run.id])).rows;
  assert.deepEqual(events.map(event => event.path).sort(), ["leads.xlsx", "summary.docx"].map(name =>
    `runs/${run.id}/artifacts/process-2/${name}`).sort());

  const validate = `import sys,zipfile,xml.etree.ElementTree as ET\nfor path in sys.argv[1:]:\n with zipfile.ZipFile(path) as pkg:\n  assert pkg.testzip() is None\n  assert '[Content_Types].xml' in pkg.namelist() and '_rels/.rels' in pkg.namelist()\n  name='xl/worksheets/sheet1.xml' if path.endswith('.xlsx') else 'word/document.xml'\n  root=ET.fromstring(pkg.read(name))\n  assert 'LEAD-42' in ''.join(root.itertext())\n`;
  for (const [name, mime] of [
    ["leads.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"],
    ["summary.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"],
  ] as const) {
    const path = join(workspace.artifactRoot, "process-2", name);
    const bytes = await readFile(path);
    const receipt = operation.result.files.find(file => file.path.endsWith(`/process-2/${name}`));
    assert.ok(receipt);
    assert.equal(receipt.bytes, bytes.length);
    assert.equal(receipt.sha256, createHash("sha256").update(bytes).digest("hex"));
    await runCommand("python3", ["-c", validate, path], {timeout: 10_000});
    const download = await fetch(`http://127.0.0.1:18121/api/work/files/runs/${run.id}/artifacts/process-2/${name}`);
    assert.equal(download.status, 200, name);
    assert.equal(download.headers.get("content-type"), mime);
    assert.match(download.headers.get("content-disposition") ?? "", new RegExp(`filename="${name.replace(".", "\\.")}"`));
    assert.deepEqual(Buffer.from(await download.arrayBuffer()), bytes);
  }
  assert.equal((await fetch(`http://127.0.0.1:18121/api/work/files/runs/${run.id}/artifacts/process-2/unissued.docx`)).status, 404);
  await writeFile(join(process.env.HARNESS_STATE!, "m5-office-thread"), thread.id);
  console.log("M5_QUEUE_OFFICE_ARTIFACTS_PASS", run.id);
  console.log("M5_WEB_OFFICE_DOWNLOAD_PASS", run.id);
} finally {
  await queue.stop({ graceful: true, timeout: 5000 });
  await shutdownAgentBroker();
  await pool().end();
}

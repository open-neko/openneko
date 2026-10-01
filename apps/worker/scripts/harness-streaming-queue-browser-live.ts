import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { rm } from "node:fs/promises";
import { join } from "node:path";
import { db, getOrgId, getOrCreateSoloAdmin, pool, processing_job, work_run_event, and, eq } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { createWorkRun, createWorkThread, getWorkRun, shutdownAgentBroker } from "@neko/llm/work";
import { runWorkRun } from "../src/jobs/work-run.js";

if (process.env.HARNESS_M6_QUEUE_BROWSER_STREAM !== "1" || process.env.NEKO_PG_PORT !== "18119")
  throw Error("isolated M6 queue/browser environment required");
const root = process.env.HARNESS_STATE;
if (!root) throw Error("missing isolated state root");
const orgId = await getOrgId();
const soloAdmin = await getOrCreateSoloAdmin(orgId);
const thread = await createWorkThread(orgId,"M6 queued streaming","web",soloAdmin.id);
const run = await createWorkRun(orgId,thread.id,"harness",{userId:soloAdmin.id,role:"admin"});
const readyPath = join(root,"m6-queue-browser-stream-ready");
await rm(readyPath,{force:true});
const browser = spawn("pnpm",["--filter","@neko/web","exec","node","scripts/harness-streaming-browser.mjs",thread.id,readyPath],
  {cwd:join(process.cwd(),"../.."),env:process.env,stdio:["ignore","pipe","pipe"]});
let browserOutput="";
browser.stdout.on("data",chunk=>{browserOutput+=String(chunk);});
browser.stderr.on("data",chunk=>{browserOutput+=String(chunk);});
const browserExit = new Promise<number>((resolve,reject)=>{
  browser.on("error",reject);
  browser.on("exit",code=>resolve(code??-1));
});
const queue = await boss();
try {
  for (let n=0;n<600 && !existsSync(readyPath);n++) {
    if (browser.exitCode !== null) throw Error(`browser exited before SSE ready: ${browserOutput}`);
    await new Promise(resolve=>setTimeout(resolve,100));
  }
  assert.ok(existsSync(readyPath),`browser did not connect to SSE: ${browserOutput}`);
  await queue.createQueue(QUEUE.WORK_RUN);
  await queue.work<WorkRunPayload>(QUEUE.WORK_RUN,async jobs=>{
    for (const job of jobs) {
      const {processingJobId,orgId,...payload}=job.data;
      await runWorkRun(processingJobId,orgId,{...payload,channel:"web"});
    }
  });
  const [job] = await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:"m6-stream-browser"}).returning();
  await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,runId:run.id,threadId:thread.id,
    message:"Answer the streaming check."},{retryLimit:0});
  for (let n=0;n<900;n++) {
    const current = await getWorkRun(orgId,run.id);
    if (current?.status === "completed") break;
    if (current?.status === "failed" || current?.status === "cancelled")
      throw Error(`queued run ${current.status}: ${current.error}`);
    await new Promise(resolve=>setTimeout(resolve,100));
  }
  assert.equal((await getWorkRun(orgId,run.id))?.status,"completed");
  assert.equal(await browserExit,0,browserOutput);
  assert.match(browserOutput,/M6_RENDERED_BROWSER_STREAMING_PASS/);
  const drafts=await db().select({id:work_run_event.id}).from(work_run_event)
    .where(and(eq(work_run_event.run_id,run.id),eq(work_run_event.kind,"provisional_answer")));
  assert.equal(drafts.length,0,"draft leaked into durable run events");
  console.log("M6_QUEUED_BROWSER_STREAMING_PASS",run.id);
} finally {
  browser.kill("SIGTERM");
  await queue.stop({graceful:true,timeout:5000});
  await shutdownAgentBroker();
  await pool().end();
}

// Connected Work cancellation: a long-running child process writes a partial
// output, then the durable Stop transition must end its OpenShell compartment.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, pool, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { cancelWorkRunIfActive, createWorkRun, createWorkThread,
  ensureWorkWorkspace, markWorkRunRunning, shutdownAgentBroker } from "@neko/llm/work";
import { runWorkRun } from "../src/jobs/work-run.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}
const runCommand=promisify(execFile);
const orgId=await getOrgId();
const queue=await boss();
const cli=process.env.HARNESS_OPENSHELL_BIN;
assert.ok(cli,"pinned OpenShell CLI required");
const control="http://127.0.0.1:18118/control";

async function sandboxExists(name:string):Promise<boolean> {
  const {stdout}=await runCommand(cli!,["--gateway","harness-m2","sandbox","list","-o","json","--limit","500"],
    {timeout:5000});
  const boxes=JSON.parse(stdout) as Array<{name:string}>;
  return boxes.some(box=>box.name===name);
}

async function sandboxContainer(name:string):Promise<string|null> {
  const {stdout}=await runCommand("docker",["ps","--format","{{.Names}}"],{timeout:5000});
  return stdout.split("\n").find(row=>row.startsWith(`openshell-default--${name}-`)) ?? null;
}

async function waitFor(condition:()=>Promise<boolean>,label:string):Promise<void> {
  for(let n=0;n<120;n++){
    if(await condition())return;
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  throw Error(`Timed out waiting for ${label}`);
}

try {
  await queue.createQueue(QUEUE.WORK_RUN);
  await queue.work<WorkRunPayload>(QUEUE.WORK_RUN,async jobs=>{
    for(const job of jobs){
      const {processingJobId,orgId:jobOrg,...payload}=job.data;
      await db().update(processing_job).set({status:"running"})
        .where(eq(processing_job.id,processingJobId));
      try {
        await runWorkRun(processingJobId,jobOrg,{...payload,channel:"web"});
        await db().update(processing_job).set({status:"succeeded"})
          .where(eq(processing_job.id,processingJobId));
      } catch(error) {
        await db().update(processing_job).set({status:"failed"})
          .where(eq(processing_job.id,processingJobId));
        throw error;
      }
    }
  });
  assert.equal((await fetch(control,{method:"POST",body:JSON.stringify({process_cancel:true})})).status,204);
  const thread=await createWorkThread(orgId,"M5 cancellable isolated process");
  const run=await createWorkRun(orgId,thread.id,"harness",{userId:null,role:"service"});
  const workspace=await ensureWorkWorkspace(orgId,thread.id,run.id);
  const [job]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,
    trigger:"test-process-cancel"}).returning({id:processing_job.id});
  const jobId=await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,
    runId:run.id,threadId:thread.id,message:"Run the cancellable process fixture."},{retryLimit:0});
  assert.ok(jobId);
  const name=`hp-${createHash("sha256").update(`${run.id}:1`).digest("hex").slice(0,16)}`;
  await waitFor(async()=>{
    const container=await sandboxContainer(name);
    if(!container)return false;
    try {
      // OpenShell serializes exec in this version; observe the test-owned
      // container directly while its production exec remains occupied.
      await runCommand("docker",["exec",container,"test","-f","/sandbox/task/result.csv"],
        {timeout:5000});
      return true;
    } catch{return false;}
  },"partial process output inside OpenShell");
  assert.equal(await cancelWorkRunIfActive(run.id,"Stopped by connected cancellation fixture"),true);
  await waitFor(async()=>!await sandboxExists(name) && !await sandboxContainer(name),
    "process sandbox teardown");
  await waitFor(async()=>{
    const current=await queue.getJobById(QUEUE.WORK_RUN,jobId);
    return current?.state==="completed" || current?.state==="failed";
  },"cancelled queue handler");
  const [finished]=(await pool().query<{status:string}>(
    "select status from work_run where org_id=$1 and id=$2",[orgId,run.id])).rows;
  assert.equal(finished.status,"cancelled");
  assert.equal((await pool().query(
    "select count(*)::int as n from work_run_event where org_id=$1 and run_id=$2 and kind='artifact'",
    [orgId,run.id])).rows[0].n,0);
  await assert.rejects(readFile(join(workspace.artifactRoot,"process-1","result.csv")),{code:"ENOENT"});
  const [operation]=(await pool().query<{result:unknown}>(
    "select result from harness_operation where org_id=$1 and run_id=$2 and request->>'tool'='process_run'",
    [orgId,run.id])).rows;
  assert.ok(operation,"process dispatch must be journaled");
  assert.equal(operation.result,null,"cancelled effect must not record a successful result");
  await assert.rejects(markWorkRunRunning(run.id),/cancelled before execution/);
  const calls=await (await fetch(control)).json();
  await runWorkRun(job.id,orgId,{runId:run.id,threadId:thread.id,
    message:"Run the cancellable process fixture.",channel:"web"});
  assert.deepEqual(await (await fetch(control)).json(),calls,
    "late queue delivery must not restart model work");
  assert.equal((await pool().query(
    "select status from work_run where org_id=$1 and id=$2",[orgId,run.id])).rows[0].status,
    "cancelled");
  console.log("M5_QUEUE_PROCESS_CANCEL_PASS",run.id);
} finally {
  await queue.stop({graceful:true,timeout:5_000});
  await shutdownAgentBroker();
  await pool().end();
}

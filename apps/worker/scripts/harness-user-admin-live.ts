// A queued Work proposal must use the existing administrator approval and
// action-execution path, with no user mutation before approval or on replay.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { app_user, db, eq, getOrgId, getOrCreateSoloAdmin, llm_provider_config, pool, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type ActionExecutePayload, type WorkRunPayload } from "@neko/db/jobs";
import { createWorkRun, createWorkThread, shutdownAgentBroker } from "@neko/llm/work";
import { approveActionRequest, createActionRequest, executeApprovedActionRequest, seedDefaultActionPolicies } from "@neko/llm/workflows";
import { createAdminHandler } from "../src/admin-server.js";
import { registerUserAdminAdapter } from "../src/plugins/manage-adapters.js";
import { runActionExecute } from "../src/jobs/action-execute.js";
import { runWorkRun } from "../src/jobs/work-run.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}
const orgId=await getOrgId();
const actor=await getOrCreateSoloAdmin(orgId);
assert.ok(actor);
const [provider]=await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id,orgId));
assert.ok(provider);
const configPath=join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "","config.yaml");
const priorConfig=await readFile(configPath,"utf8");
const queue=await boss();
const admin=createServer(createAdminHandler({actionRequests:{create:async input=>{
  const request=await createActionRequest(input as Parameters<typeof createActionRequest>[0]);
  return {id:request.id,status:request.status};
}}}));
const inviteEmail="harness-invite@example.invalid";

async function waitFor(queueName:string,jobId:string):Promise<void> {
  for(let n=0;n<120;n++) {
    const job=await queue.getJobById(queueName,jobId);
    if(job?.state==="failed") throw Error(`${queueName} failed: ${JSON.stringify(job.output)}`);
    if(job?.state==="completed") return;
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  throw Error(`${queueName} timed out`);
}

async function submitWork(runId:string,threadId:string,trigger:string):Promise<void> {
  const [job]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger})
    .returning({id:processing_job.id});
  const queued=await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,runId,threadId,
    message:"Invite the synthetic team member as a member; request approval first."},{retryLimit:0});
  assert.ok(queued);
  await waitFor(QUEUE.WORK_RUN,queued);
  const [run]=(await pool().query<{status:string;error:string|null}>(
    "select status,error from work_run where org_id=$1 and id=$2",[orgId,runId])).rows;
  assert.equal(run?.status,"completed",JSON.stringify(run));
}

try {
  await new Promise<void>(resolve=>admin.listen(18122,"127.0.0.1",resolve));
  await seedDefaultActionPolicies(orgId);
  registerUserAdminAdapter();
  await db().update(llm_provider_config).set({model:"harness-user-admin-fixture"})
    .where(eq(llm_provider_config.id,provider.id));
  await writeFile(configPath,"model:\n  provider: custom\n  default: harness-user-admin-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  assert.equal((await fetch("http://127.0.0.1:18118/control",{method:"POST",body:"{}"})).status,204);
  await queue.createQueue(QUEUE.WORK_RUN);
  await queue.createQueue(QUEUE.ACTION_EXECUTE);
  await queue.work<WorkRunPayload>(QUEUE.WORK_RUN,async jobs=>{
    for(const job of jobs) {
      const {processingJobId,orgId:jobOrg,...payload}=job.data;
      await db().update(processing_job).set({status:"running"}).where(eq(processing_job.id,processingJobId));
      try {
        await runWorkRun(processingJobId,jobOrg,{...payload,channel:"web"});
        await db().update(processing_job).set({status:"succeeded"}).where(eq(processing_job.id,processingJobId));
      } catch(error) {
        await db().update(processing_job).set({status:"failed"}).where(eq(processing_job.id,processingJobId));
        throw error;
      }
    }
  });
  await queue.work<ActionExecutePayload>(QUEUE.ACTION_EXECUTE,async jobs=>{
    for(const job of jobs) await runActionExecute(job.data);
  });

  const thread=await createWorkThread(orgId,"M5 governed user invitation","web",actor.id);
  const run=await createWorkRun(orgId,thread.id,"harness",{userId:actor.id,role:"admin"});
  await submitWork(run.id,thread.id,"test-user-admin-proposal");
  const [request]=(await pool().query<{id:string;status:string;scope:string;kind:string;target:string;harness_prepared:unknown}>(
    "select id,status,scope,kind,target,harness_prepared from action_request where org_id=$1 and work_run_id=$2",
    [orgId,run.id])).rows;
  if (!request) {
    const diagnostic=(await pool().query(
      "select request,result,finished_at from harness_operation where org_id=$1 and run_id=$2",
      [orgId,run.id])).rows;
    const [status]=(await pool().query("select status,error from work_run where org_id=$1 and id=$2",
      [orgId,run.id])).rows;
    console.error("USER_ADMIN_PROPOSAL_DIAGNOSTIC",JSON.stringify({diagnostic,status}));
  }
  assert.ok(request);
  assert.equal(request.status,"pending_approval");
  assert.equal(request.scope,"internal");
  assert.equal(request.kind,"user_admin");
  assert.equal(request.target,inviteEmail);
  assert.ok(request.harness_prepared);
  assert.equal((await pool().query("select count(*)::int as n from app_user where org_id=$1 and email=$2",
    [orgId,inviteEmail])).rows[0].n,0,"proposal must not invite before approval");
  const operation=(await pool().query("select request,result from harness_operation where org_id=$1 and run_id=$2",
    [orgId,run.id])).rows;
  assert.equal(operation.length,1);
  assert.equal(operation[0].request.tool,"propose");
  assert.equal(operation[0].result.id,request.id);
  const beforeCalls=await (await fetch("http://127.0.0.1:18118/control")).json();
  await submitWork(run.id,thread.id,"test-user-admin-redelivery");
  assert.deepEqual(await (await fetch("http://127.0.0.1:18118/control")).json(),beforeCalls);
  assert.equal((await pool().query("select count(*)::int as n from action_request where org_id=$1 and work_run_id=$2",
    [orgId,run.id])).rows[0].n,1);
  console.log("M5_QUEUE_USER_ADMIN_PROPOSAL_PASS",run.id);

  await approveActionRequest({orgId,id:request.id,approverUserId:actor.id,
    approver:{userId:actor.id,role:"admin"}});
  await db().update(app_user).set({disabled_at:new Date()}).where(eq(app_user.id,actor.id));
  try {
    await assert.rejects(executeApprovedActionRequest(orgId,request.id),
      /Requesting actor is no longer active/);
    assert.equal((await pool().query("select count(*)::int as n from action_execution where org_id=$1 and action_request_id=$2",
      [orgId,request.id])).rows[0].n,0,"revocation must stop before the effect claim");
  } finally {
    await db().update(app_user).set({disabled_at:null}).where(eq(app_user.id,actor.id));
  }
  const effectJob=await enqueue(QUEUE.ACTION_EXECUTE,{orgId,actionRequestId:request.id},{retryLimit:0});
  assert.ok(effectJob);
  await waitFor(QUEUE.ACTION_EXECUTE,effectJob);
  const [invited]=(await pool().query<{id:string}>(
    "select id from app_user where org_id=$1 and email=$2",[orgId,inviteEmail])).rows;
  assert.ok(invited);
  const [execution]=(await pool().query<{status:string}>(
    "select status from action_execution where org_id=$1 and action_request_id=$2",[orgId,request.id])).rows;
  assert.equal(execution.status,"succeeded");
  const replayJob=await enqueue(QUEUE.ACTION_EXECUTE,{orgId,actionRequestId:request.id},{retryLimit:0});
  assert.ok(replayJob);
  await waitFor(QUEUE.ACTION_EXECUTE,replayJob);
  assert.equal((await pool().query("select count(*)::int as n from action_execution where org_id=$1 and action_request_id=$2",
    [orgId,request.id])).rows[0].n,1);
  assert.equal((await pool().query("select count(*)::int as n from app_user where org_id=$1 and email=$2",
    [orgId,inviteEmail])).rows[0].n,1);
  await writeFile(join(process.env.HARNESS_STATE!,"m5-user-admin-thread"),thread.id);
  console.log("M5_QUEUE_USER_ADMIN_EFFECT_PASS",request.id);
} finally {
  await queue.stop({graceful:true,timeout:5000});
  await new Promise<void>(resolve=>admin.close(()=>resolve()));
  await shutdownAgentBroker();
  await db().update(llm_provider_config).set({model:provider.model}).where(eq(llm_provider_config.id,provider.id));
  await writeFile(configPath,priorConfig);
  await pool().end();
}

// Connected acceptance for cron and source-change triggers through pg-boss,
// the production workflow handler, Ax, the host broker and OpenShell.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import { data_source, db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { shutdownAgentBroker } from "@neko/llm/work";
import { createSubscription, handleSourceChangeMatch, startSubscriptionManager } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runDurableWorkflowSchedulerTick } from "../src/workflow-scheduler.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}

const orgId = await getOrgId();
const queue = await boss();
const runCommand=promisify(execFile);
const workflows: string[] = [];
let subscriptionManager:ReturnType<typeof startSubscriptionManager>|undefined;
const control = "http://127.0.0.1:18118/control";
const [priorProvider]=await db().select().from(llm_provider_config)
  .where(eq(llm_provider_config.org_id,orgId));
assert.ok(priorProvider,"isolated model configuration must be seeded");
const configPath=join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "","config.yaml");
const priorConfig=await readFile(configPath,"utf8");

async function waitForWorkflow(workflowId: string, jobId: string) {
  for (let n = 0; n < 180; n++) {
    const [run] = (await pool().query<{
      id:string;work_run_id:string;status:string;error:string|null;
    }>(`select id,work_run_id,status,error from workflow_run
       where org_id=$1 and workflow_id=$2 order by created_at desc limit 1`,
      [orgId,workflowId])).rows;
    const job = await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,jobId);
    if (run && ["completed","failed","cancelled"].includes(run.status) &&
        ["completed","failed"].includes(job?.state ?? "")) {
      assert.equal(run.status,"completed",JSON.stringify({run,job:job?.state}));
      assert.equal(job?.state,"completed");
      return run;
    }
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  throw Error(`Timed out waiting for workflow ${workflowId} job ${jobId}`);
}

async function modelCalls():Promise<Record<string,number>> {
  const response=await fetch(control);
  assert.equal(response.status,200);
  return response.json() as Promise<Record<string,number>>;
}

try {
  await db().update(llm_provider_config).set({model:"harness-trigger-fixture"})
    .where(eq(llm_provider_config.id,priorProvider.id));
  await writeFile(configPath,
    "model:\n  provider: custom\n  default: harness-trigger-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE,async jobs=>{
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [source]=(await db().select({id:data_source.id}).from(data_source)
    .where(eq(data_source.org_id,orgId)).limit(1));
  assert.ok(source,"isolated GraphJin source must be seeded");
  const [sourceWorkflow]=await db().insert(workflow_definition).values({
    org_id:orgId,name:`Trigger replay source ${randomUUID()}`,
    goal:"Find the seeded reference and report it once",
  }).returning({id:workflow_definition.id});
  workflows.push(sourceWorkflow.id);
  const subscription=await createSubscription({orgId,workflowId:sourceWorkflow.id,
    sourceKind:"source_change",filter:{table:"references",primary_key:["id"]}});
  assert.equal((await fetch(control,{method:"POST",body:"{}"})).status,204);
  const decisions:Array<Awaited<ReturnType<typeof handleSourceChangeMatch>>>=[];
  const subscriptionErrors:string[]=[];
  subscriptionManager=startSubscriptionManager({
    resolveTransport:async()=>({baseUrl:"http://127.0.0.1:18117/api/v1/graphql"}),
    refreshIntervalMs:60_000,
    onMatch:async event=>{
      if(event.kind!=="source_change" || event.subscription.id!==subscription.id)return;
      decisions.push(await handleSourceChangeMatch({subscription:event.subscription,
        match:event.match,dataSourceId:source.id}));
    },
    onError:error=>{subscriptionErrors.push(error.message);},
  });
  await subscriptionManager.ready;
  const waitForDecision=async(count:number)=>{
    for(let attempt=0;attempt<120;attempt++){
      if(decisions.length>=count)return decisions[count-1];
      await new Promise(resolve=>setTimeout(resolve,500));
    }
    throw Error(`GraphJin websocket delivered ${decisions.length}/${count} matches: ${subscriptionErrors.join("; ")}`);
  };
  const first=await waitForDecision(1);
  assert.equal(first.action,"enqueued",JSON.stringify(first));
  if(first.action!=="enqueued" || !first.jobId)throw Error("source event not queued");
  const sourceRun=await waitForWorkflow(sourceWorkflow.id,first.jobId);
  const [delivery]=(await pool().query<{id:string;status:string;workflow_run_id:string}>(
    "select id,status,workflow_run_id from source_change_delivery where org_id=$1 and subscription_id=$2",
    [orgId,subscription.id])).rows;
  assert.deepEqual({status:delivery.status,run:delivery.workflow_run_id},
    {status:"completed",run:sourceRun.id});
  assert.equal((await pool().query(
    "select count(*)::int as n from observation where org_id=$1 and subscription_id=$2",
    [orgId,subscription.id])).rows[0].n,1);
  const sourceCalls=await modelCalls();
  await runCommand("docker",["restart","harness-m3-graphjin-1"],{timeout:60_000});
  const replay=await waitForDecision(2);
  assert.equal(replay.action,"dropped",JSON.stringify(replay));
  assert.deepEqual(await modelCalls(),sourceCalls,"websocket replay called the model again");
  const sourceJob=await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,first.jobId);
  assert.ok(sourceJob);
  await runWorkflowRunFire(sourceJob.data as WorkflowRunFirePayload);
  assert.deepEqual(await modelCalls(),sourceCalls,"source redelivery called the model again");
  assert.equal((await pool().query(
    "select count(*)::int as n from workflow_run where org_id=$1 and workflow_id=$2",
    [orgId,sourceWorkflow.id])).rows[0].n,1);
  console.log("M5_QUEUE_SOURCE_TRIGGER_REPLAY_PASS",sourceRun.id);
  console.log("M5_GRAPHJIN_WEBSOCKET_LEDGER_PASS",subscription.id);

  const [cronWorkflow]=await db().insert(workflow_definition).values({
    org_id:orgId,name:`Trigger replay cron ${randomUUID()}`,
    goal:"Find the seeded reference and report it once",cron:"* * * * *",
    cron_timezone:"UTC",cron_enabled:true,
    updated_at:new Date(Date.now()-120_000),
  }).returning({id:workflow_definition.id});
  workflows.push(cronWorkflow.id);
  assert.equal((await fetch(control,{method:"POST",body:"{}"})).status,204);
  const health=await runDurableWorkflowSchedulerTick();
  assert.equal(health.status,"ok",JSON.stringify(health));
  assert.equal(health.dispatched,1,JSON.stringify(health));
  const [firing]=(await pool().query<{id:string;queue_job_id:string;status:string}>(
    "select id,queue_job_id,status from workflow_schedule_firing where org_id=$1 and workflow_id=$2",
    [orgId,cronWorkflow.id])).rows;
  assert.ok(firing?.queue_job_id);
  const cronRun=await waitForWorkflow(cronWorkflow.id,firing.queue_job_id);
  assert.equal((await pool().query(
    "select status from workflow_schedule_firing where id=$1",[firing.id])).rows[0].status,"completed");
  const cronCalls=await modelCalls();
  const cronJob=await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,firing.queue_job_id);
  assert.ok(cronJob);
  await runWorkflowRunFire(cronJob.data as WorkflowRunFirePayload);
  assert.deepEqual(await modelCalls(),cronCalls,"cron redelivery called the model again");
  assert.equal((await pool().query(
    "select count(*)::int as n from workflow_run where org_id=$1 and workflow_id=$2",
    [orgId,cronWorkflow.id])).rows[0].n,1);
  console.log("M5_QUEUE_CRON_TRIGGER_REPLAY_PASS",cronRun.id);
} finally {
  await subscriptionManager?.stop();
  await queue.stop({graceful:true,timeout:5_000});
  await shutdownAgentBroker();
  await writeFile(configPath,priorConfig);
  await db().update(llm_provider_config).set({model:priorProvider.model})
    .where(eq(llm_provider_config.id,priorProvider.id));
  for(const workflowId of workflows){
    await db().delete(workflow_definition).where(eq(workflow_definition.id,workflowId));
  }
  await pool().end();
}

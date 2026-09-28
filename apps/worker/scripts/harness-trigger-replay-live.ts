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
import { claimSourceChangeDelivery, claimWorkflowScheduleFiring, createSubscription, dispatchPendingSourceChangeDeliveries, handleSourceChangeMatch, prepareWorkflowRunForDelivery, reclaimQueuedSourceChangeDelivery, reclaimQueuedWorkflowScheduleFiring, recordSourceChangeDelivery, startSubscriptionManager } from "@neko/llm/workflows";
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
  }).returning({id:workflow_definition.id,name:workflow_definition.name});
  workflows.push(sourceWorkflow.id);
  const orphanCount=async(workflowId:string,threadTitle:string)=>{
    const [row]=(await pool().query<{threads:number;workRuns:number;workflowRuns:number;reservations:number}>(
      `select
        (select count(*)::int from work_thread where org_id=$1 and title=$3) as threads,
        (select count(*)::int from work_run run join work_thread thread on thread.id=run.thread_id
          where run.org_id=$1 and thread.title=$3) as "workRuns",
        (select count(*)::int from workflow_run where org_id=$1 and workflow_id=$2) as "workflowRuns",
        (select count(*)::int from spend_reservation where org_id=$1 and workflow_id=$2) as reservations`,
      [orgId,workflowId,threadTitle])).rows;
    return row;
  };
  const sourceBefore=await orphanCount(sourceWorkflow.id,sourceWorkflow.name);
  await assert.rejects(prepareWorkflowRunForDelivery({orgId,workflowId:sourceWorkflow.id,
    triggerKind:"subscription"},{kind:"source_change",id:randomUUID()}),
    /Claimed workflow delivery can no longer be linked/);
  assert.deepEqual(await orphanCount(sourceWorkflow.id,sourceWorkflow.name),sourceBefore,
    "failed source-change link must roll back the thread, runs and spend reservation");
  console.log("M5_SOURCE_TRIGGER_PREPARE_ROLLBACK_PASS",sourceWorkflow.id);
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
  }).returning({id:workflow_definition.id,name:workflow_definition.name});
  workflows.push(cronWorkflow.id);
  const cronBefore=await orphanCount(cronWorkflow.id,cronWorkflow.name);
  await assert.rejects(prepareWorkflowRunForDelivery({orgId,workflowId:cronWorkflow.id,
    triggerKind:"cron"},{kind:"schedule",id:randomUUID()}),
    /Claimed workflow delivery can no longer be linked/);
  assert.deepEqual(await orphanCount(cronWorkflow.id,cronWorkflow.name),cronBefore,
    "failed cron link must roll back the thread, runs and spend reservation");
  console.log("M5_CRON_TRIGGER_PREPARE_ROLLBACK_PASS",cronWorkflow.id);
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

  // Simulate the worker dying after the atomic delivery/run commit but before
  // run.mark_running. The restarted handler must reuse the linked queued run.
  const recoverySource=await recordSourceChangeDelivery({orgId,workflowId:sourceWorkflow.id,
    subscriptionId:subscription.id,subscriptionUpdatedAt:subscription.updatedAt,
    sourceId:source.id,deliveryKey:`crash-${randomUUID()}`,
    match:{table:"references",primary_key:{id:"REF-42"},
      snapshot:{id:"REF-42"},version_token:`crash-${randomUUID()}`}});
  assert.equal(await claimSourceChangeDelivery({id:recoverySource.id,orgId,
    workflowId:sourceWorkflow.id}),true);
  const preparedSource=await prepareWorkflowRunForDelivery({orgId,
    workflowId:sourceWorkflow.id,triggerKind:"subscription",
    triggeredBySubscriptionId:subscription.id,
    triggeredByObservationId:recoverySource.observationId},
    {kind:"source_change",id:recoverySource.id});
  assert.equal((await pool().query("select status from work_run where id=$1",
    [preparedSource.workRunId])).rows[0].status,"queued");
  await pool().query("update source_change_delivery set lease_until=now()-interval '1 second' where id=$1",
    [recoverySource.id]);
  assert.equal((await fetch(control,{method:"POST",body:"{}"})).status,204);
  const sourceRecoveryPayload:WorkflowRunFirePayload={orgId,workflowId:sourceWorkflow.id,
    triggerKind:"subscription",sourceChangeDeliveryId:recoverySource.id,
    triggeredBySubscriptionId:subscription.id,
    triggeredByObservationId:recoverySource.observationId};
  assert.equal(await dispatchPendingSourceChangeDeliveries(),1);
  const [requeuedSource]=(await pool().query<{queue_job_id:string}>(
    "select queue_job_id from source_change_delivery where id=$1",
    [recoverySource.id])).rows;
  assert.ok(requeuedSource.queue_job_id);
  await waitForWorkflow(sourceWorkflow.id,requeuedSource.queue_job_id);
  assert.equal((await pool().query("select status from workflow_run where id=$1",
    [preparedSource.workflowRun.id])).rows[0].status,"completed");
  assert.equal((await pool().query("select status from source_change_delivery where id=$1",
    [recoverySource.id])).rows[0].status,"completed");
  const recoveredSourceCalls=await modelCalls();
  await runWorkflowRunFire(sourceRecoveryPayload);
  assert.deepEqual(await modelCalls(),recoveredSourceCalls,
    "source-change recovery redelivery called the model again");
  assert.equal((await pool().query("select count(*)::int as n from workflow_run where org_id=$1 and workflow_id=$2",
    [orgId,sourceWorkflow.id])).rows[0].n,2);
  console.log("M5_SOURCE_TRIGGER_QUEUED_RECOVERY_PASS",preparedSource.workflowRun.id);

  const recoveryFiringId=randomUUID();
  await pool().query(
    `insert into workflow_schedule_firing
       (id,org_id,workflow_id,scheduled_for,status)
     values ($1,$2,$3,$4,'enqueued')`,
    [recoveryFiringId,orgId,cronWorkflow.id,new Date(Date.now()+60_000)]);
  assert.equal(await claimWorkflowScheduleFiring({firingId:recoveryFiringId,
    orgId,workflowId:cronWorkflow.id}),true);
  const preparedCron=await prepareWorkflowRunForDelivery({orgId,
    workflowId:cronWorkflow.id,triggerKind:"cron"},
    {kind:"schedule",id:recoveryFiringId});
  assert.equal((await pool().query("select status from work_run where id=$1",
    [preparedCron.workRunId])).rows[0].status,"queued");
  await pool().query("update workflow_schedule_firing set lease_until=now()-interval '1 second' where id=$1",
    [recoveryFiringId]);
  assert.equal((await fetch(control,{method:"POST",body:"{}"})).status,204);
  const cronRecoveryPayload:WorkflowRunFirePayload={orgId,workflowId:cronWorkflow.id,
    triggerKind:"cron",scheduleFiringId:recoveryFiringId};
  await pool().query("update workflow_schedule_state set next_fire_at=now()+interval '1 day' where workflow_id=$1",
    [cronWorkflow.id]);
  const recoveryHealth=await runDurableWorkflowSchedulerTick();
  assert.equal(recoveryHealth.status,"ok",JSON.stringify(recoveryHealth));
  const [requeuedCron]=(await pool().query<{queue_job_id:string}>(
    "select queue_job_id from workflow_schedule_firing where id=$1",
    [recoveryFiringId])).rows;
  assert.ok(requeuedCron.queue_job_id);
  await waitForWorkflow(cronWorkflow.id,requeuedCron.queue_job_id);
  assert.equal((await pool().query("select status from workflow_run where id=$1",
    [preparedCron.workflowRun.id])).rows[0].status,"completed");
  assert.equal((await pool().query("select status from workflow_schedule_firing where id=$1",
    [recoveryFiringId])).rows[0].status,"completed");
  const recoveredCronCalls=await modelCalls();
  await runWorkflowRunFire(cronRecoveryPayload);
  assert.deepEqual(await modelCalls(),recoveredCronCalls,
    "cron recovery redelivery called the model again");
  assert.equal((await pool().query("select count(*)::int as n from workflow_run where org_id=$1 and workflow_id=$2",
    [orgId,cronWorkflow.id])).rows[0].n,2);
  console.log("M5_CRON_TRIGGER_QUEUED_RECOVERY_PASS",preparedCron.workflowRun.id);

  // pg-boss may deliver before the dispatcher persists its queue acknowledgement.
  // A linked queued run in dispatching state must still be reclaimed once.
  const ackRaceSource=await recordSourceChangeDelivery({orgId,workflowId:sourceWorkflow.id,
    subscriptionId:subscription.id,subscriptionUpdatedAt:subscription.updatedAt,
    sourceId:source.id,deliveryKey:`ack-race-${randomUUID()}`,
    match:{table:"references",primary_key:{id:"REF-42"},
      snapshot:{id:"REF-42"},version_token:`ack-race-${randomUUID()}`}});
  assert.equal(await claimSourceChangeDelivery({id:ackRaceSource.id,orgId,
    workflowId:sourceWorkflow.id}),true);
  const ackRaceSourceRun=await prepareWorkflowRunForDelivery({orgId,
    workflowId:sourceWorkflow.id,triggerKind:"subscription"},
    {kind:"source_change",id:ackRaceSource.id});
  await pool().query("update source_change_delivery set status='dispatching',lease_until=now()+interval '2 minutes' where id=$1",
    [ackRaceSource.id]);
  assert.equal(await reclaimQueuedSourceChangeDelivery({id:ackRaceSource.id,orgId,
    workflowId:sourceWorkflow.id}),ackRaceSourceRun.workflowRun.id);
  assert.equal(await reclaimQueuedSourceChangeDelivery({id:ackRaceSource.id,orgId,
    workflowId:sourceWorkflow.id}),null);
  await pool().query("update work_run set status='running' where id=$1",
    [ackRaceSourceRun.workRunId]);
  await pool().query("update source_change_delivery set lease_until=now()-interval '1 second' where id=$1",
    [ackRaceSource.id]);
  assert.equal(await reclaimQueuedSourceChangeDelivery({id:ackRaceSource.id,orgId,
    workflowId:sourceWorkflow.id}),null,"already-started source run must stay fenced");
  console.log("M5_SOURCE_TRIGGER_ACK_RACE_PASS",ackRaceSourceRun.workflowRun.id);

  const ackRaceFiringId=randomUUID();
  await pool().query(
    `insert into workflow_schedule_firing
       (id,org_id,workflow_id,scheduled_for,status)
     values ($1,$2,$3,$4,'enqueued')`,
    [ackRaceFiringId,orgId,cronWorkflow.id,new Date(Date.now()+120_000)]);
  assert.equal(await claimWorkflowScheduleFiring({firingId:ackRaceFiringId,
    orgId,workflowId:cronWorkflow.id}),true);
  const ackRaceCronRun=await prepareWorkflowRunForDelivery({orgId,
    workflowId:cronWorkflow.id,triggerKind:"cron"},
    {kind:"schedule",id:ackRaceFiringId});
  await pool().query("update workflow_schedule_firing set status='dispatching',lease_until=now()+interval '2 minutes' where id=$1",
    [ackRaceFiringId]);
  assert.equal(await reclaimQueuedWorkflowScheduleFiring({firingId:ackRaceFiringId,
    orgId,workflowId:cronWorkflow.id}),ackRaceCronRun.workflowRun.id);
  assert.equal(await reclaimQueuedWorkflowScheduleFiring({firingId:ackRaceFiringId,
    orgId,workflowId:cronWorkflow.id}),null);
  await pool().query("update work_run set status='running' where id=$1",
    [ackRaceCronRun.workRunId]);
  await pool().query("update workflow_schedule_firing set lease_until=now()-interval '1 second' where id=$1",
    [ackRaceFiringId]);
  assert.equal(await reclaimQueuedWorkflowScheduleFiring({firingId:ackRaceFiringId,
    orgId,workflowId:cronWorkflow.id}),null,"already-started cron run must stay fenced");
  console.log("M5_CRON_TRIGGER_ACK_RACE_PASS",ackRaceCronRun.workflowRun.id);
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

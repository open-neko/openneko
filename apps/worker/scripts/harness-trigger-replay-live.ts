// Connected acceptance for cron and source-change triggers through pg-boss,
// the production workflow handler, Ax, the host broker and OpenShell.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { join } from "node:path";
import { promisify } from "node:util";
import { data_source, db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { claimSourceChangeDelivery, claimWorkflowScheduleFiring, createSubscription, dispatchPendingSourceChangeDeliveries, handleSourceChangeMatch, materializeDueWorkflowFirings, prepareWorkflowRunForDelivery, reclaimQueuedSourceChangeDelivery, reclaimQueuedWorkflowScheduleFiring, recordSourceChangeDelivery, runWorkflowTurn, startSubscriptionManager } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runDurableWorkflowSchedulerTick } from "../src/workflow-scheduler.js";
import { initializeWorkerTelemetry, shutdownWorkerTelemetry } from "../src/telemetry.js";

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
const exportedTraces:Buffer[]=[];
const collector=createServer((request,response)=>{
  const chunks:Buffer[]=[];
  request.on("data",(chunk:Buffer)=>chunks.push(chunk));
  request.on("end",()=>{
    exportedTraces.push(Buffer.concat(chunks));
    response.writeHead(200,{"content-type":"application/x-protobuf"});
    response.end();
  });
});
await new Promise<void>(resolve=>collector.listen(0,"127.0.0.1",resolve));
const collectorAddress=collector.address();
assert.ok(collectorAddress && typeof collectorAddress!=="string");
process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=`http://127.0.0.1:${collectorAddress.port}/v1/traces`;
assert.equal(initializeWorkerTelemetry(),true);

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

async function verifiedOutputCheckpoint(workRunId:string):Promise<string> {
  const file=join(getOrgAgentRoot(orgId),"runs",workRunId,".harness",
    `${createHash("sha256").update(workRunId).digest("hex")}.json`);
  const contents=await readFile(file,"utf8");
  const checkpoint=JSON.parse(contents) as {
    result?:{status:string};
    events:Array<{type:string;origin?:string;operation_id?:number;
      state_update?:{target:string;state:{operation_id:number;output_id:string;kind:string}};
      terminal?:{accepted:boolean;evidence_ids?:number[]}}>;
    operations:Array<{id:number;tool?:string;binding?:string;finished:boolean;error?:string;result?:{ok?:boolean;outputId?:string;kind?:string}}>;
  };
  assert.equal(checkpoint.result?.status,"completed");
  const terminal=checkpoint.events.find(event=>event.type==="terminal.checked");
  assert.equal(terminal?.origin,"openneko-workflow-output-v1");
  assert.equal(terminal.terminal?.accepted,true);
  const ids=terminal.terminal.evidence_ids;
  assert.ok(ids?.length,"terminal gate must cite a broker-confirmed output");
  assert.ok(checkpoint.events.findIndex(event=>event.type==="terminal.checked") <
    checkpoint.events.findIndex(event=>event.type==="run.finished"));
  for(const id of ids){
    const operation=checkpoint.operations.find(item=>item.id===id);
    assert.equal(operation?.tool,"workflow_output_emit");
    assert.equal(operation?.finished,true);
    assert.equal(operation?.error,undefined);
    assert.equal(operation?.result?.ok,true);
    assert.ok(operation.result.outputId);
    assert.ok(operation.result.kind);
    const [receipt]=(await pool().query<{result:{ok:boolean;outputId:string;kind:string}}>(
      `select result from harness_operation where org_id=$1 and run_id=$2 and operation_id=$3
         and request->>'tool'='workflow_output'`,[orgId,workRunId,id])).rows;
    assert.deepEqual(receipt?.result,operation.result);
    const updates=checkpoint.events.filter(event=>event.type==="runtime.state.updated" && event.operation_id===id);
    assert.equal(updates.length,1,"committed output must update responder state exactly once");
    assert.equal(updates[0].state_update?.target,"root/responder");
    assert.deepEqual(updates[0].state_update?.state,
      {operation_id:id,output_id:receipt.result.outputId,kind:receipt.result.kind});
    const applied=checkpoint.events.filter(event=>event.type==="runtime.state.applied" && event.operation_id===id);
    assert.equal(applied.length,1,"Ax must acknowledge application at the responder boundary");
    assert.equal(applied[0].origin,"next-response");
    assert.ok(checkpoint.events.indexOf(applied[0])>checkpoint.events.indexOf(updates[0]));
  }
  return contents;
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
  const sourceCheckpoint=await verifiedOutputCheckpoint(sourceRun.work_run_id);
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
  assert.equal(await verifiedOutputCheckpoint(sourceRun.work_run_id),sourceCheckpoint,
    "source redelivery changed the terminal checkpoint");
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
  const cronCheckpoint=await verifiedOutputCheckpoint(cronRun.work_run_id);
  assert.equal((await pool().query(
    "select status from workflow_schedule_firing where id=$1",[firing.id])).rows[0].status,"completed");
  const cronCalls=await modelCalls();
  const cronJob=await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,firing.queue_job_id);
  assert.ok(cronJob);
  await runWorkflowRunFire(cronJob.data as WorkflowRunFirePayload);
  assert.deepEqual(await modelCalls(),cronCalls,"cron redelivery called the model again");
  assert.equal(await verifiedOutputCheckpoint(cronRun.work_run_id),cronCheckpoint,
    "cron redelivery changed the terminal checkpoint");
  console.log("M6_CONNECTED_WORKFLOW_TERMINAL_REPLAY_PASS",sourceRun.work_run_id,cronRun.work_run_id);
  console.log("M6_CONNECTED_WORKFLOW_STATE_HOOK_PASS",sourceRun.work_run_id,cronRun.work_run_id);
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
       (id,org_id,workflow_id,scheduled_for,definition_updated_at,status)
     select $1,$2,$3,$4,workflow.updated_at,'enqueued'
     from workflow_definition workflow where workflow.id=$3`,
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
       (id,org_id,workflow_id,scheduled_for,definition_updated_at,status)
     select $1,$2,$3,$4,workflow.updated_at,'enqueued'
     from workflow_definition workflow where workflow.id=$3`,
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

  // An edit after the atomic link but before the model starts invalidates the
  // admitted revision. Sweeps must terminalize the exact queued run and
  // release its spend reservation, not leave an invisible running workflow.
  const staleSource=await recordSourceChangeDelivery({orgId,workflowId:sourceWorkflow.id,
    subscriptionId:subscription.id,subscriptionUpdatedAt:subscription.updatedAt,
    sourceId:source.id,deliveryKey:`stale-${randomUUID()}`,
    match:{table:"references",primary_key:{id:"REF-42"},
      snapshot:{id:"REF-42"},version_token:`stale-${randomUUID()}`}});
  assert.equal(await claimSourceChangeDelivery({id:staleSource.id,orgId,
    workflowId:sourceWorkflow.id}),true);
  const staleSourceRun=await prepareWorkflowRunForDelivery({orgId,
    workflowId:sourceWorkflow.id,triggerKind:"subscription"},
    {kind:"source_change",id:staleSource.id});
  const staleFiringId=randomUUID();
  await pool().query(
    `insert into workflow_schedule_firing
       (id,org_id,workflow_id,scheduled_for,definition_updated_at,status)
     select $1,$2,$3,$4,workflow.updated_at,'enqueued'
     from workflow_definition workflow where workflow.id=$3`,
    [staleFiringId,orgId,cronWorkflow.id,new Date(Date.now()+180_000)]);
  assert.equal(await claimWorkflowScheduleFiring({firingId:staleFiringId,
    orgId,workflowId:cronWorkflow.id}),true);
  const staleCronRun=await prepareWorkflowRunForDelivery({orgId,
    workflowId:cronWorkflow.id,triggerKind:"cron"},
    {kind:"schedule",id:staleFiringId});
  const beforeStaleCalls=await modelCalls();
  await pool().query("update workflow_definition set updated_at=now()+interval '1 second' where id=any($1::uuid[])",
    [[sourceWorkflow.id,cronWorkflow.id]]);
  await assert.rejects(runWorkflowTurn({prepared:staleSourceRun,
    queuedDelivery:{kind:"source_change",id:staleSource.id},mode:"headless",emit:async()=>{}}),
    /Workflow delivery changed or work run already started/);
  await assert.rejects(runWorkflowTurn({prepared:staleCronRun,
    queuedDelivery:{kind:"schedule",id:staleFiringId},mode:"headless",emit:async()=>{}}),
    /Workflow delivery changed or work run already started/);
  assert.equal(await dispatchPendingSourceChangeDeliveries(),0);
  const staleHealth=await runDurableWorkflowSchedulerTick();
  assert.equal(staleHealth.status,"ok",JSON.stringify(staleHealth));
  for(const [deliveryTable,deliveryId,preparedRun] of [
    ["source_change_delivery",staleSource.id,staleSourceRun],
    ["workflow_schedule_firing",staleFiringId,staleCronRun],
  ] as const){
    assert.equal((await pool().query(`select status from ${deliveryTable} where id=$1`,
      [deliveryId])).rows[0].status,"cancelled");
    assert.equal((await pool().query("select status from workflow_run where id=$1",
      [preparedRun.workflowRun.id])).rows[0].status,"cancelled");
    assert.equal((await pool().query("select status from work_run where id=$1",
      [preparedRun.workRunId])).rows[0].status,"cancelled");
    assert.ok((await pool().query("select released_at from spend_reservation where work_run_id=$1",
      [preparedRun.workRunId])).rows[0].released_at);
  }
  await runWorkflowRunFire({orgId,workflowId:sourceWorkflow.id,triggerKind:"subscription",
    sourceChangeDeliveryId:staleSource.id});
  await runWorkflowRunFire({orgId,workflowId:cronWorkflow.id,triggerKind:"cron",
    scheduleFiringId:staleFiringId});
  assert.deepEqual(await modelCalls(),beforeStaleCalls,
    "a changed trigger definition must not call the model");
  console.log("M5_SOURCE_TRIGGER_STALE_PRESTART_PASS",staleSourceRun.workflowRun.id);
  console.log("M5_CRON_TRIGGER_STALE_PRESTART_PASS",staleCronRun.workflowRun.id);

  const [precisionWorkflow]=await db().insert(workflow_definition).values({
    org_id:orgId,name:`Trigger revision precision ${randomUUID()}`,
    cron:"* * * * *",cron_timezone:"UTC",cron_enabled:true,
  }).returning({id:workflow_definition.id});
  workflows.push(precisionWorkflow.id);
  await pool().query(
    "update workflow_definition set updated_at=date_trunc('milliseconds',now())+interval '321 microseconds' where id=$1",
    [precisionWorkflow.id]);
  const exactRevision=(await pool().query<{revision:string}>(
    "select updated_at::text as revision from workflow_definition where id=$1",
    [precisionWorkflow.id])).rows[0].revision;
  await materializeDueWorkflowFirings(new Date(Date.now()+120_000),{orgId});
  const [precisionFiring]=(await pool().query<{id:string;revision:string;state_revision:string}>(
    `select firing.id,firing.definition_updated_at::text as revision,
       state.definition_updated_at::text as state_revision
     from workflow_schedule_firing firing
     join workflow_schedule_state state on state.workflow_id=firing.workflow_id
     where firing.workflow_id=$1 order by firing.created_at desc limit 1`,
    [precisionWorkflow.id])).rows;
  assert.ok(precisionFiring,"sub-millisecond cron definition produced no firing");
  assert.equal(precisionFiring.revision,exactRevision);
  assert.equal(precisionFiring.state_revision,exactRevision);
  assert.equal(await claimWorkflowScheduleFiring({firingId:precisionFiring.id,
    orgId,workflowId:precisionWorkflow.id}),true);
  console.log("M5_CRON_TRIGGER_EXACT_REVISION_PASS",precisionWorkflow.id);
  await shutdownWorkerTelemetry();
  const traceBody=Buffer.concat(exportedTraces).toString("utf8");
  assert.ok(traceBody.includes("model.stage_usage"),"OTLP must export stage attribution");
  assert.ok(traceBody.includes("openneko.agent.stage"));
  assert.ok(traceBody.includes("openneko.model.requests"));
  assert.ok(traceBody.includes("tool.catalog"),"OTLP must export Harness catalog size");
  assert.ok(traceBody.includes("openneko.tool.catalog.schema_bytes"));
  assert.ok(traceBody.includes("openneko.tool.catalog.descriptor_bytes"));
  assert.ok(traceBody.includes("executor"));
  assert.ok(traceBody.includes(sourceRun.work_run_id));
  assert.ok(!traceBody.includes("Find the seeded reference and report it once"),
    "OTLP must not export workflow prompt content");
  console.log("M6_CONNECTED_WORKER_OTLP_STAGE_USAGE_PASS",sourceRun.work_run_id);
} finally {
  await subscriptionManager?.stop();
  await queue.stop({graceful:true,timeout:5_000});
  await shutdownAgentBroker();
  await shutdownWorkerTelemetry();
  await new Promise<void>(resolve=>collector.close(()=>resolve()));
  await writeFile(configPath,priorConfig);
  await db().update(llm_provider_config).set({model:priorProvider.model})
    .where(eq(llm_provider_config.id,priorProvider.id));
  for(const workflowId of workflows){
    await db().delete(workflow_definition).where(eq(workflow_definition.id,workflowId));
  }
  await pool().end();
}

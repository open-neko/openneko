// Connected crash window: the output and host state are durable before the
// responder returns; the same queued workflow must resume without a new effect.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { data_source, db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { createSubscription, dispatchPendingSourceChangeDeliveries, recordSourceChangeDelivery } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}
const orgId=await getOrgId();
const queue=await boss();
const control="http://127.0.0.1:18118/control";
const [prior]=await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id,orgId));
assert.ok(prior);
const configPath=join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "","config.yaml");
const priorConfig=await readFile(configPath,"utf8");
let workflowId:string|undefined;
try {
  await db().update(llm_provider_config).set({model:"harness-trigger-crash-fixture"})
    .where(eq(llm_provider_config.id,prior.id));
  await writeFile(configPath,
    "model:\n  provider: custom\n  default: harness-trigger-crash-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  assert.equal((await fetch(control,{method:"POST",body:JSON.stringify({pause_responder:true})})).status,204);
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE,async jobs=>{
    for(const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [source]=await db().select({id:data_source.id}).from(data_source)
    .where(eq(data_source.org_id,orgId)).limit(1);
  assert.ok(source);
  const [workflow]=await db().insert(workflow_definition).values({
    org_id:orgId,name:`State crash recovery ${randomUUID()}`,
    goal:"Find the seeded reference, emit one finding, and report the receipt",
  }).returning({id:workflow_definition.id});
  workflowId=workflow.id;
  const subscription=await createSubscription({orgId,workflowId:workflow.id,
    sourceKind:"source_change",filter:{table:"references",primary_key:["id"]}});
  const delivery=await recordSourceChangeDelivery({orgId,workflowId:workflow.id,
    subscriptionId:subscription.id,subscriptionUpdatedAt:subscription.updatedAt,
    sourceId:source.id,deliveryKey:`state-crash-${randomUUID()}`,
    match:{table:"references",primary_key:{id:"REF-42"},snapshot:{id:"REF-42"},
      version_token:`state-crash-${randomUUID()}`}});
  assert.equal(await dispatchPendingSourceChangeDeliveries(),1);
  const [queued]=(await pool().query<{queue_job_id:string}>(
    "select queue_job_id from source_change_delivery where id=$1",[delivery.id])).rows;
  assert.ok(queued.queue_job_id);
  let run:{id:string;work_run_id:string;status:string}|undefined;
  for(let n=0;n<120;n++){
    [run]=(await pool().query<{id:string;work_run_id:string;status:string}>(
      "select id,work_run_id,status from workflow_run where org_id=$1 and workflow_id=$2 order by created_at desc limit 1",
      [orgId,workflow.id])).rows;
    const calls=await (await fetch(control)).json() as Record<string,number>;
    if(run && calls["harness-trigger-crash-fixture"]===4)break;
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  assert.ok(run,"workflow did not reach its paused responder");
  const before=(await pool().query<{operation_id:number;request:unknown;result:unknown}>(
    "select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
    [orgId,run.work_run_id])).rows;
  assert.deepEqual(before.map(row=>row.operation_id),[1,4]);
  assert.ok(before[1].result,"broker output receipt must commit before the crash");
  const outputId=(before[1].result as {outputId:string}).outputId;
  assert.ok(outputId);
  const callsBefore=await (await fetch(control)).json() as Record<string,number>;
  const sandboxName=`neko-h-${createHash("sha256").update(run.work_run_id).digest("hex").slice(0,12)}`;
  const openshell=process.env.HARNESS_M3_CLI!;
  const sandbox=(...args:string[])=>execFileSync(openshell,["--gateway","harness-m2","sandbox",...args],
    {encoding:"utf8",timeout:30_000});
  const beforeStop=JSON.parse(sandbox("get",sandboxName,"-o","json")) as {name:string;phase:string;labels:Record<string,string>};
  assert.equal(beforeStop.name,sandboxName);
  assert.equal(beforeStop.phase,"Ready");
  assert.equal(beforeStop.labels["openneko.recovery"],"retain");
  // 0.1.2 denies in-sandbox kill and a disconnected CLI does not stop its
  // remote exec. Stop/start is the supported process crash with workspace
  // preservation; the checkpoint remains available for reconciliation.
  sandbox("stop",sandboxName);
  sandbox("start",sandboxName);
  assert.equal((await fetch(control,{method:"POST",body:JSON.stringify({continue:true})})).status,204);
  let jobState:string|undefined;
  for(let n=0;n<180;n++){
    [run]=(await pool().query<{id:string;work_run_id:string;status:string}>(
      "select id,work_run_id,status from workflow_run where id=$1",[run.id])).rows;
    jobState=(await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,queued.queue_job_id))?.state;
    if(["completed","failed"].includes(jobState??""))break;
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  if(jobState!=="completed" || run?.status!=="completed"){
    const [work]=(await pool().query<{status:string;error:string|null}>(
      "select status,error from work_run where id=$1",[run?.work_run_id])).rows;
    const [workflow]=(await pool().query<{status:string;error:string|null}>(
      "select status,error from workflow_run where id=$1",[run?.id])).rows;
    let checkpointDiagnostic:unknown;
    try {
      const raw=JSON.parse(await readFile(join(getOrgAgentRoot(orgId),"runs",run!.work_run_id,".harness",
        `${createHash("sha256").update(run!.work_run_id).digest("hex")}.json`),"utf8")) as {
          result?:{status?:string;code?:string};operations?:Array<{tool?:string;error?:string;finished?:boolean}>;
          events?:Array<{type?:string;error?:string;name?:string;operation_id?:number;data?:unknown}>;
        };
      checkpointDiagnostic={result:raw.result&&{status:raw.result.status,code:raw.result.code},operations:raw.operations?.map(op=>({tool:op.tool,error:op.error,finished:op.finished})),
        executionEvents:raw.events?.filter(event=>["executor.step.failed","tool.failed","tool.started","tool.finished","run.finished"].includes(event.type??""))
          .map(event=>({type:event.type,error:event.error,name:event.name,operationId:event.operation_id,
            detail:event.type==="tool.failed"?event.data:undefined})),
        recentEvents:raw.events?.slice(-8).map(event=>({type:event.type,error:event.error,name:event.name}))};
    } catch { checkpointDiagnostic="checkpoint unavailable"; }
    console.error("M6_STATE_CRASH_DIAGNOSTIC",JSON.stringify({jobState,work,workflow,modelCounts:await (await fetch(control)).json(),checkpointDiagnostic}));
  }
  assert.equal(jobState,"completed");
  assert.equal(run?.status,"completed");
  assert.deepEqual((await pool().query("select operation_id,request,result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
    [orgId,run.work_run_id])).rows,before,"crash recovery repeated a broker operation");
  assert.equal((await pool().query("select count(*)::int as n from workflow_output where id=$1",[outputId])).rows[0].n,1);
  const calls=await (await fetch(control)).json() as Record<string,number>;
  assert.equal(calls["harness-trigger-crash-fixture"],7);
  assert.ok((calls["max-request:harness-trigger-crash-fixture"]??Infinity)<50_000,
    "large saved read inflated a provider request after restart");
  assert.ok((calls["max-csv-markers:harness-trigger-crash-fixture"]??Infinity)<=150,
    "large saved read body entered provider context after restart");
  assert.equal(calls["graphjin-fixture"],callsBefore["graphjin-fixture"],
    "crash recovery re-ran GraphJin");
  const checkpoint=JSON.parse(await readFile(join(getOrgAgentRoot(orgId),"runs",run.work_run_id,".harness",
    `${createHash("sha256").update(run.work_run_id).digest("hex")}.json`),"utf8")) as {
      result:{status:string};operations:Array<{tool:string;result?:{content?:string}}>;
      events:Array<{type:string;operation_id?:number;observation_read?:{result_bytes?:number};terminal?:{accepted:boolean}}>;
    };
  assert.equal(checkpoint.result.status,"completed");
  assert.equal(checkpoint.operations[2]?.tool,"file_read");
  const expectedLargeRead=Buffer.from(`lead_id\n${"LEAD-42\n".repeat(6500)}`);
  assert.equal(checkpoint.operations[2]?.result?.content?.length,expectedLargeRead.length);
  assert.equal(createHash("sha256").update(checkpoint.operations[2].result!.content!).digest("hex"),
    createHash("sha256").update(expectedLargeRead).digest("hex"));
  assert.equal(checkpoint.events.filter(event=>event.type==="observation.retrieved" && event.operation_id===3 &&
    (event.observation_read?.result_bytes??0)>=expectedLargeRead.length).length,1,
    "resumed actor did not retrieve the full saved read by ID");
  const artifact=await readFile(join(getOrgAgentRoot(orgId),"runs",run.work_run_id,"artifacts","large.csv"));
  assert.equal(createHash("sha256").update(artifact).digest("hex"),
    createHash("sha256").update(expectedLargeRead).digest("hex"));
  assert.equal(checkpoint.events.filter(event=>event.type==="run.resumed").length,1);
  assert.equal(checkpoint.events.filter(event=>event.type==="runtime.state.updated" && event.operation_id===4).length,1);
  assert.equal(checkpoint.events.filter(event=>event.type==="runtime.state.applied" && event.operation_id===4).length,1);
  assert.equal(checkpoint.events.find(event=>event.type==="terminal.checked")?.terminal?.accepted,true);
  const replay=await queue.getJobById(QUEUE.WORKFLOW_RUN_FIRE,queued.queue_job_id);
  assert.ok(replay);
  await runWorkflowRunFire(replay.data as WorkflowRunFirePayload);
  assert.deepEqual(await (await fetch(control)).json(),calls,"queue redelivery called a model again");
  console.log("M6_CONNECTED_WORKFLOW_STATE_CRASH_RECOVERY_PASS",run.work_run_id);
} finally {
  await queue.stop({graceful:true,timeout:5_000});
  await shutdownAgentBroker();
  await writeFile(configPath,priorConfig);
  await db().update(llm_provider_config).set({model:prior.model}).where(eq(llm_provider_config.id,prior.id));
  if(workflowId)await db().delete(workflow_definition).where(eq(workflow_definition.id,workflowId));
  await pool().end();
}

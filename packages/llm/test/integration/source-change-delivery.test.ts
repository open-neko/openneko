import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, expect, it } from "vitest";
import { data_source, db, pool, subscription, workflow_definition } from "@neko/db";
import { boss, QUEUE, stopBoss } from "@neko/db/jobs";
import { createTestOrg, deleteTestOrg } from "@neko/db/test-helpers";
import { handleSourceChangeMatch } from "../../src/workflows/match-handler";
import {
  claimSourceChangeDelivery, dispatchPendingSourceChangeDeliveries,
  linkSourceChangeDeliveryRun, recordSourceChangeDelivery,
  settleSourceChangeDelivery,
} from "../../src/workflows/source-change-delivery";

const live=process.env.HARNESS_M5_SOURCE_CHANGE === "1" ? it : it.skip;
const orgId=`source-change-${randomUUID()}`;
let workflowId:string,subscriptionId:string,sourceId:string,subscriptionUpdatedAt:Date;

beforeAll(async()=>{
  if(process.env.HARNESS_M5_SOURCE_CHANGE !== "1")return;
  await createTestOrg(orgId);
  const [workflow]=await db().insert(workflow_definition).values({org_id:orgId,name:"Stream replay fixture"}).returning();
  workflowId=workflow.id;
  const [sub]=await db().insert(subscription).values({org_id:orgId,workflow_id:workflowId,
    source_kind:"source_change",filter:{table:"leads",primary_key:["id"]}}).returning();
  subscriptionId=sub.id;
  subscriptionUpdatedAt=sub.updated_at;
  const [source]=await db().insert(data_source).values({org_id:orgId,kind:"graphjin",
    graphql_url:"http://127.0.0.1:18117/api/v1/graphql"}).returning();
  sourceId=source.id;
  await (await boss()).createQueue(QUEUE.WORKFLOW_RUN_FIRE);
});

afterAll(async()=>{
  if(process.env.HARNESS_M5_SOURCE_CHANGE !== "1")return;
  await stopBoss();
  await deleteTestOrg(orgId);
  await pool().end();
});

live("deduplicates stream replay before observation and fences duplicate consumer delivery",async()=>{
  const sub={id:subscriptionId,orgId,workflowId,sourceKind:"source_change" as const,
    filter:{table:"leads",primary_key:["id"]},enabled:true,debounceMs:0,
    maxConcurrentRuns:5,maxChainDepthOverride:null,idempotencyKeyTemplate:null,
    createdAt:new Date(),updatedAt:subscriptionUpdatedAt};
  const match={table:"leads",primary_key:{id:42},snapshot:{id:42,label:"Lead 42"},
    version_token:"v1"};
  const first=await handleSourceChangeMatch({subscription:sub,match,dataSourceId:sourceId});
  expect(first.action,JSON.stringify(first)).toBe("enqueued");
  if(first.action!=="enqueued" || !first.jobId)throw Error("stream event was not queued");
  const replay=await handleSourceChangeMatch({subscription:sub,
    match:{...match,snapshot:{label:"Lead 42",id:42}},dataSourceId:sourceId});
  expect(replay).toMatchObject({action:"dropped",reason:expect.stringMatching(/already enqueued/)});
  const rows=await pool().query<{id:string;observation_id:string;queue_job_id:string;status:string}>(
    "select id,observation_id,queue_job_id,status from source_change_delivery where org_id=$1",[orgId]);
  expect(rows.rows).toHaveLength(1);
  expect(rows.rows[0]).toMatchObject({observation_id:first.observationId,queue_job_id:first.jobId,status:"enqueued"});
  const deliveryId=rows.rows[0]!.id;
  expect((await pool().query("select id from observation where org_id=$1",[orgId])).rowCount).toBe(1);
  expect((await pool().query("select id from source_change_log where org_id=$1 and change_kind='subscription_match'",[orgId])).rowCount).toBe(1);
  const queued=await (await boss()).getJobById(QUEUE.WORKFLOW_RUN_FIRE,first.jobId);
  expect(queued?.data).toMatchObject({sourceChangeDeliveryId:deliveryId,triggeredByObservationId:first.observationId});
  expect(await claimSourceChangeDelivery({id:deliveryId,orgId,workflowId})).toBe(true);
  expect(await claimSourceChangeDelivery({id:deliveryId,orgId,workflowId})).toBe(false);

  const threadId=randomUUID(),workRunId=randomUUID(),workflowRunId=randomUUID();
  await pool().query("insert into work_thread(id,org_id,title) values($1,$2,'Stream replay fixture')",[threadId,orgId]);
  await pool().query("insert into work_run(id,org_id,thread_id,backend,status) values($1,$2,$3,'harness','running')",[workRunId,orgId,threadId]);
  await pool().query(`insert into workflow_run(id,org_id,workflow_id,thread_id,work_run_id,trigger_kind,status)
    values($1,$2,$3,$4,$5,'subscription','running')`,[workflowRunId,orgId,workflowId,threadId,workRunId]);
  await linkSourceChangeDeliveryRun(deliveryId,workflowRunId);
  expect(await settleSourceChangeDelivery(deliveryId,workflowRunId)).toBe(false);
  await pool().query("update workflow_run set status='failed' where id=$1",[workflowRunId]);
  expect(await settleSourceChangeDelivery(deliveryId,workflowRunId)).toBe(true);
  expect(await settleSourceChangeDelivery(deliveryId,workflowRunId)).toBe(false);
  expect(await claimSourceChangeDelivery({id:deliveryId,orgId,workflowId})).toBe(false);
  expect((await pool().query("select status from source_change_delivery where id=$1",[deliveryId])).rows[0].status).toBe("completed");

  // A crash after the transaction but before enqueue leaves a pending outbox
  // row. The worker sweep dispatches it without needing websocket replay.
  const stranded=await recordSourceChangeDelivery({orgId,workflowId,subscriptionId,
    subscriptionUpdatedAt,sourceId,
    deliveryKey:`${subscriptionId}:lead-42:v2`,match:{...match,version_token:"v2"}});
  expect(stranded.status).toBe("pending");
  expect(await dispatchPendingSourceChangeDeliveries()).toBe(1);
  expect((await pool().query("select status,queue_job_id from source_change_delivery where id=$1",[stranded.id])).rows[0])
    .toMatchObject({status:"enqueued",queue_job_id:expect.any(String)});
  await pool().query("update subscription set updated_at=updated_at + interval '1 second', filter=$2::jsonb where id=$1",
    [subscriptionId,JSON.stringify({table:"leads",primary_key:["id"],where:{id:{eq:43}}})]);
  expect(await claimSourceChangeDelivery({id:stranded.id,orgId,workflowId})).toBe(false);
  await dispatchPendingSourceChangeDeliveries();
  expect((await pool().query("select status from source_change_delivery where id=$1",[stranded.id])).rows[0].status)
    .toBe("cancelled");

  // If the host work run dies after the delivery is linked, recovery must
  // retain the prior effect identity rather than schedule a second attempt.
  const current=(await pool().query<{updated_at:Date}>(
    "select updated_at from subscription where id=$1",[subscriptionId])).rows[0]!.updated_at;
  const linked=await recordSourceChangeDelivery({orgId,workflowId,subscriptionId,
    subscriptionUpdatedAt:current,sourceId,
    deliveryKey:`${subscriptionId}:lead-42:v3`,match:{...match,version_token:"v3"}});
  expect(await claimSourceChangeDelivery({id:linked.id,orgId,workflowId})).toBe(true);
  const failedWorkRunId=randomUUID(),failedWorkflowRunId=randomUUID();
  await pool().query("insert into work_run(id,org_id,thread_id,backend,status) values($1,$2,$3,'harness','running')",
    [failedWorkRunId,orgId,threadId]);
  await pool().query(`insert into workflow_run(id,org_id,workflow_id,thread_id,work_run_id,trigger_kind,status)
    values($1,$2,$3,$4,$5,'subscription','running')`,
    [failedWorkflowRunId,orgId,workflowId,threadId,failedWorkRunId]);
  await linkSourceChangeDeliveryRun(linked.id,failedWorkflowRunId);
  await pool().query("update work_run set status='failed' where id=$1",[failedWorkRunId]);
  await pool().query("update source_change_delivery set lease_until=now()-interval '1 second' where id=$1",[linked.id]);
  expect(await dispatchPendingSourceChangeDeliveries()).toBe(0);
  expect((await pool().query("select status,workflow_run_id from source_change_delivery where id=$1",[linked.id])).rows[0])
    .toMatchObject({status:"cancelled",workflow_run_id:failedWorkflowRunId});
},30_000);

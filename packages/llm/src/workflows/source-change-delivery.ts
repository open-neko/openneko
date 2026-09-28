import { pool } from "@neko/db";
import { enqueue as defaultEnqueue, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import type { SourceChangeMatch } from "./subscription-query";
import { cancelStaleQueuedTriggerRuns } from "./queued-trigger-recovery";

export type SourceChangeDelivery = {
  id: string;
  observationId: string;
  status: string;
  queueJobId: string | null;
};

/** The observation, audit entry and outbox identity commit together. A stream
 * replay gets the same observation instead of writing a second audit event. */
export async function recordSourceChangeDelivery(input: {
  orgId: string;
  workflowId: string;
  subscriptionId: string;
  subscriptionUpdatedAt: Date;
  sourceId: string;
  deliveryKey: string;
  match: SourceChangeMatch;
}): Promise<SourceChangeDelivery> {
  const client = await pool().connect();
  try {
    await client.query("BEGIN");
    const triggerPayload = {
      subscription_id: input.subscriptionId,
      table: input.match.table,
      primary_key: input.match.primary_key,
      snapshot: input.match.snapshot,
      version_token: input.match.version_token,
    };
    const inserted = await client.query<{id:string}>(
       `insert into source_change_delivery
         (org_id, subscription_id, subscription_updated_at, definition_updated_at, workflow_id,
          source_id, delivery_key, trigger_payload)
       select $1, sub.id, sub.updated_at, workflow.updated_at, $3, $4, $5, $6::jsonb
       from subscription sub
       join workflow_definition workflow on workflow.id=$3 and workflow.org_id=$1 and workflow.enabled=true
       join data_source source on source.id=$4 and source.org_id=$1 and source.enabled=true
       where sub.id=$2 and sub.org_id=$1 and sub.workflow_id=$3
         and sub.enabled=true and date_trunc('milliseconds',sub.updated_at)=$7
       on conflict (org_id, subscription_id, subscription_updated_at, delivery_key) do nothing
       returning id`,
      [input.orgId, input.subscriptionId, input.workflowId, input.sourceId,
        input.deliveryKey, JSON.stringify(triggerPayload),input.subscriptionUpdatedAt],
    );
    if (!inserted.rows[0]) {
      const existing = await client.query<{
        id:string; observation_id:string; status:string; queue_job_id:string|null;
      }>(
        `select id, observation_id, status, queue_job_id
         from source_change_delivery
         where org_id=$1 and subscription_id=$2
           and date_trunc('milliseconds',subscription_updated_at)=$4
           and delivery_key=$3`,
        [input.orgId, input.subscriptionId, input.deliveryKey,input.subscriptionUpdatedAt],
      );
      await client.query("COMMIT");
      const row = existing.rows[0];
      if (!row?.observation_id) throw new Error("Source-change subscription changed or delivery has no committed observation");
      return {id:row.id,observationId:row.observation_id,status:row.status,queueJobId:row.queue_job_id};
    }
    const id = inserted.rows[0].id;
    const pkSummary = Object.entries(input.match.primary_key)
      .map(([key,value])=>`${key}=${value === null ? "" : String(value)}`).join(", ");
    const observation = await client.query<{id:string}>(
      `insert into observation
         (org_id, source_output_id, consumer_kind, consumer_workflow_id,
          subscription_id, title, body)
       values ($1, null, 'workflow', $2, $3, $4, $5)
       returning id`,
      [input.orgId, input.workflowId, input.subscriptionId,
        `${input.match.table} (${pkSummary})`, JSON.stringify(input.match.snapshot).slice(0,4000)],
    );
    const observationId = observation.rows[0]!.id;
    await client.query(
      `insert into source_change_log
         (org_id, source_id, table_name, change_kind, payload)
       values ($1, $2, $3, 'subscription_match', $4::jsonb)`,
      [input.orgId, input.sourceId, input.match.table, JSON.stringify({
        delivery_id:id, subscription_id:input.subscriptionId,
        observation_id:observationId, primary_key:input.match.primary_key,
        snapshot:input.match.snapshot, version_token:input.match.version_token,
      })],
    );
    await client.query(
      `update source_change_delivery
       set observation_id=$2,
           trigger_payload=jsonb_set(trigger_payload, '{observation_id}', to_jsonb($2::uuid::text))
       where id=$1`, [id,observationId],
    );
    await client.query("COMMIT");
    return {id,observationId,status:"pending",queueJobId:null};
  } catch (error) {
    await client.query("ROLLBACK").catch(()=>undefined);
    throw error;
  } finally {
    client.release();
  }
}

type DispatchRow = {
  id:string; org_id:string; workflow_id:string; subscription_id:string;
  observation_id:string; delivery_key:string; subscription_updated_at:Date;
  trigger_payload:Record<string,unknown>;
};

export async function dispatchSourceChangeDelivery(input: {
  id: string;
  enqueue?: typeof defaultEnqueue;
  now?: Date;
}): Promise<string | null> {
  const now = input.now ?? new Date();
  const leased = await pool().query<DispatchRow>(
    `update source_change_delivery
     set status='dispatching', lease_until=$2::timestamptz + interval '2 minutes',
         attempts=attempts+1, updated_at=$2
     where id=$1 and status='pending' and available_at <= now()
     returning id,org_id,workflow_id,subscription_id,observation_id,
               delivery_key,subscription_updated_at,trigger_payload`, [input.id,now],
  );
  const row = leased.rows[0];
  if (!row) return null;
  try {
    const payload:WorkflowRunFirePayload = {
      orgId:row.org_id, workflowId:row.workflow_id, triggerKind:"subscription",
      sourceChangeDeliveryId:row.id, triggerPayload:row.trigger_payload,
      triggeredBySubscriptionId:row.subscription_id,
      triggeredByObservationId:row.observation_id,
    };
    const jobId = await (input.enqueue ?? defaultEnqueue)(QUEUE.WORKFLOW_RUN_FIRE,payload,{
      singletonKey:`${row.delivery_key}:${row.subscription_updated_at.toISOString()}`,
      singletonHours:1,retryLimit:2,
    });
    if (!jobId) throw new Error("pg-boss did not accept the source-change delivery");
    await pool().query(
      `update source_change_delivery
       set status='enqueued', queue_job_id=$2, lease_until=null, updated_at=$3
       where id=$1 and status='dispatching'`, [row.id,jobId,now],
    );
    return jobId;
  } catch(error) {
    await pool().query(
      `update source_change_delivery
       set status='pending',lease_until=null,available_at=$2::timestamptz + interval '15 seconds',
           last_error=$3,updated_at=$2
       where id=$1 and status='dispatching'`,
      [row.id,now,error instanceof Error?error.message:String(error)],
    );
    throw error;
  }
}

/** Reconcile lost queue acknowledgements and dispatch pending stream events
 * even when GraphJin does not replay the original websocket message. */
export async function dispatchPendingSourceChangeDeliveries(
  at?: Date, limit = 50,
): Promise<number> {
  const now = at ?? (await pool().query<{now:Date}>("select now() as now")).rows[0]!.now;
  await cancelStaleQueuedTriggerRuns("source_change",now);
  await pool().query(
    `update source_change_delivery delivery
     set status='pending',lease_until=null,available_at=$1,updated_at=$1,
         last_error='linked queued run awaiting redelivery'
     from workflow_run run join work_run work on work.id=run.work_run_id
     where delivery.status='running' and delivery.lease_until < $1
       and delivery.workflow_run_id=run.id and run.status='running'
       and work.status='queued'
       and exists (select 1 from subscription sub join workflow_definition workflow
         on workflow.id=sub.workflow_id where sub.id=delivery.subscription_id
           and sub.org_id=delivery.org_id and sub.enabled=true and workflow.enabled=true
           and sub.updated_at=delivery.subscription_updated_at
           and workflow.updated_at=delivery.definition_updated_at)`,[now],
  );
  await pool().query(
    `update source_change_delivery delivery
     set status='cancelled',lease_until=null,completed_at=$1,updated_at=$1,
         last_error='subscription or workflow changed before dispatch'
     from subscription sub join workflow_definition workflow on workflow.id=sub.workflow_id
     where delivery.subscription_id=sub.id and delivery.workflow_id=workflow.id
       and delivery.status in ('pending','dispatching','enqueued')
       and (sub.enabled=false or workflow.enabled=false
            or sub.updated_at is distinct from delivery.subscription_updated_at
            or workflow.updated_at is distinct from delivery.definition_updated_at)`,[now],
  );
  await pool().query(
    `update source_change_delivery d
     set status=case when run.status='cancelled' then 'cancelled' else 'completed' end,
         lease_until=null,completed_at=$1,updated_at=$1
     from workflow_run run
     where d.status='running' and d.workflow_run_id=run.id
       and run.status in ('completed','failed','needs_input','cancelled')`, [now],
  );
  // A restart can cancel the work_run after the worker linked it, while the
  // workflow_run row still says running. Preserve the evidence and stop
  // redispatch; the prior model/effect outcome is not safe to repeat.
  await pool().query(
    `update source_change_delivery delivery
     set status='cancelled',lease_until=null,completed_at=$1,updated_at=$1,
         last_error='linked work run ended before workflow result reconciliation'
     from workflow_run run join work_run work on work.id=run.work_run_id
     where delivery.status='running' and delivery.workflow_run_id=run.id
       and delivery.lease_until < $1 and run.status='running'
       and work.status in ('cancelled','failed')`,[now],
  );
  await pool().query(
    `update source_change_delivery
     set status='pending',lease_until=null,available_at=$1,updated_at=$1,
         last_error=coalesce(last_error,'recovered stale source-change delivery')
     where (status='dispatching' and lease_until < $1)
        or (status='enqueued' and workflow_run_id is null
            and updated_at < $1::timestamptz - interval '30 minutes')
        or (status='running' and workflow_run_id is null and lease_until < $1)`, [now],
  );
  const rows = await pool().query<{id:string}>(
    `select id from source_change_delivery
     where status='pending' and available_at <= $1
     order by available_at,created_at limit $2`, [now,Math.max(1,Math.min(limit,200))],
  );
  let dispatched = 0;
  for (const row of rows.rows) {
    try { if (await dispatchSourceChangeDelivery({id:row.id,now})) dispatched++; }
    catch(error) {
      console.warn(`[source-change-delivery] dispatch failed id=${row.id}: ${error instanceof Error?error.message:String(error)}`);
    }
  }
  return dispatched;
}

export async function claimSourceChangeDelivery(input:{id:string;orgId:string;workflowId:string}):Promise<boolean> {
  const claimed = await pool().query(
    `update source_change_delivery delivery
     set status='running',lease_until=now()+interval '30 minutes',updated_at=now()
     where delivery.id=$1 and delivery.org_id=$2 and delivery.workflow_id=$3
       and delivery.status in ('pending','dispatching','enqueued')
       and delivery.workflow_run_id is null
       and exists (select 1 from subscription sub join workflow_definition workflow
         on workflow.id=sub.workflow_id where sub.id=delivery.subscription_id
         and sub.org_id=delivery.org_id and sub.enabled=true and workflow.enabled=true
         and sub.updated_at=delivery.subscription_updated_at
         and workflow.updated_at=delivery.definition_updated_at)
     returning delivery.id`,[input.id,input.orgId,input.workflowId],
  );
  return claimed.rowCount===1;
}

/** Recover only a linked run that never left queued. Once it is running the
 * prior model/effect outcome may be ambiguous and must not be redispatched. */
export async function reclaimQueuedSourceChangeDelivery(input:{
  id:string;orgId:string;workflowId:string;now?:Date;
}):Promise<string|null> {
  const now=input.now??new Date();
  const reclaimed=await pool().query<{workflow_run_id:string}>(
    `update source_change_delivery delivery
     set status='running',lease_until=$4::timestamptz + interval '30 minutes',updated_at=$4
     where delivery.id=$1 and delivery.org_id=$2 and delivery.workflow_id=$3
       and delivery.status in ('pending','dispatching','enqueued','running')
       and (delivery.status <> 'running' or delivery.lease_until < $4)
       and delivery.workflow_run_id is not null
       and exists (select 1 from workflow_run run join work_run work on work.id=run.work_run_id
         where run.id=delivery.workflow_run_id and run.org_id=delivery.org_id
           and run.status='running' and work.status='queued')
       and exists (select 1 from subscription sub join workflow_definition workflow
         on workflow.id=sub.workflow_id where sub.id=delivery.subscription_id
           and sub.org_id=delivery.org_id and sub.enabled=true and workflow.enabled=true
           and sub.updated_at=delivery.subscription_updated_at
           and workflow.updated_at=delivery.definition_updated_at)
     returning delivery.workflow_run_id`,[input.id,input.orgId,input.workflowId,now]);
  return reclaimed.rows[0]?.workflow_run_id??null;
}

export async function linkSourceChangeDeliveryRun(id:string,workflowRunId:string):Promise<void> {
  const linked=await pool().query(
    `update source_change_delivery set workflow_run_id=$2,updated_at=now()
     where id=$1 and status='running' and (workflow_run_id is null or workflow_run_id=$2)
     returning id`,[id,workflowRunId],
  );
  if(linked.rowCount!==1)throw Error("Source-change delivery can no longer be linked to this run");
}

export async function settleSourceChangeDelivery(id:string,workflowRunId:string):Promise<boolean> {
  const settled=await pool().query(
    `update source_change_delivery delivery
     set status=case when run.status='cancelled' then 'cancelled' else 'completed' end,
         lease_until=null,completed_at=now(),updated_at=now()
     from workflow_run run
     where delivery.id=$1 and delivery.workflow_run_id=$2 and delivery.status='running'
       and run.id=$2 and run.status in ('completed','failed','needs_input','cancelled')
     returning delivery.id`,[id,workflowRunId],
  );
  return settled.rowCount===1;
}

export async function releaseUnlinkedSourceChangeDelivery(id:string,error:unknown):Promise<void> {
  await pool().query(
    `update source_change_delivery
     set status='pending',lease_until=null,available_at=now()+interval '15 seconds',
         last_error=$2,updated_at=now()
     where id=$1 and status='running' and workflow_run_id is null`,
    [id,error instanceof Error?error.message:String(error)],
  );
}

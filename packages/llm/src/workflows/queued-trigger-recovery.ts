import { pool } from "@neko/db";
import { recordAuditEvent } from "./audit-chain";

type TriggerKind = "schedule" | "source_change";

/** A changed definition invalidates a prepared trigger only while its work run
 * is still queued. The work_run update races the worker's start CAS: exactly
 * one wins, so a run that may have reached the model is never recycled. */
export async function cancelStaleQueuedTriggerRuns(
  kind: TriggerKind,
  now = new Date(),
  orgId?: string,
): Promise<number> {
  const table = kind === "schedule" ? "workflow_schedule_firing" : "source_change_delivery";
  const current = kind === "schedule"
    ? `exists (select 1 from workflow_definition workflow
         join workflow_schedule_state state on state.workflow_id=workflow.id
         where workflow.id=delivery.workflow_id and workflow.org_id=delivery.org_id
           and workflow.enabled=true and workflow.cron_enabled=true
           and workflow.cron=state.cron and workflow.cron_timezone=state.cron_timezone
           and workflow.updated_at=state.definition_updated_at
           and delivery.definition_updated_at=state.definition_updated_at)`
    : `exists (select 1 from subscription sub
         join workflow_definition workflow on workflow.id=sub.workflow_id
         where sub.id=delivery.subscription_id and sub.org_id=delivery.org_id
           and sub.enabled=true and workflow.enabled=true
           and sub.updated_at=delivery.subscription_updated_at
           and workflow.updated_at=delivery.definition_updated_at)`;
  const cancelled = await pool().query<{work_run_id:string;org_id:string}>(
    `with stopped as (
       update work_run work
       set status='cancelled',error='trigger definition changed before model start',
           finished_at=$1,updated_at=$1
       from workflow_run run join ${table} delivery on delivery.workflow_run_id=run.id
       where work.id=run.work_run_id and work.status='queued'
         and run.status='running' and delivery.status in ('pending','dispatching','enqueued','running')
         and ($2::text is null or delivery.org_id=$2)
         and not ${current}
       returning work.id as work_run_id,work.org_id,run.id as workflow_run_id,delivery.id as delivery_id
     ), finished as (
       update workflow_run run
       set status='cancelled',error='trigger definition changed before model start',
           finished_at=$1,updated_at=$1
       from stopped where run.id=stopped.workflow_run_id and run.status='running'
       returning stopped.work_run_id,stopped.org_id,stopped.delivery_id
     ), released as (
       update spend_reservation reservation set released_at=$1
       from finished where reservation.work_run_id=finished.work_run_id
         and reservation.released_at is null
       returning reservation.id
     )
     update ${table} delivery
     set status='cancelled',lease_until=null,completed_at=$1,updated_at=$1,
         last_error='trigger definition changed before model start'
     from finished where delivery.id=finished.delivery_id
     returning finished.work_run_id,finished.org_id`,
    [now,orgId??null],
  );
  for (const row of cancelled.rows) {
    await recordAuditEvent({orgId:row.org_id,entityKind:"work_run",entityId:row.work_run_id,
      event:"run:cancelled",payload:{status:"cancelled",error:"trigger definition changed before model start"}});
  }
  return cancelled.rowCount??0;
}

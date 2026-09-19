import { pool, resolveUserGroups } from "@neko/db";
import { isDeepStrictEqual } from "node:util";
import { startupEvent } from "@neko/telemetry/startup";
import { validateHarnessAction } from "../work/harness-proposal";
import { resolveDeploymentProfile } from "../work/deployment-profile";
import { assertMayDecide, hasHumanActionApproval, listEnabledPolicies, type ActionRequestRecord } from "./action-store";
import { evaluateActionPolicy } from "./policy-engine";
import type { ActionAdapter, ActionExecutionOutcome } from "./action-executor";

/** One claim, one dispatch. Without a provider receipt, interrupted effects stay unknown. */
export async function executeHarnessAction(request:ActionRequestRecord,resolve:()=>Promise<ActionAdapter|undefined>) {
  const owner=await pool().connect();
  const key=`harness.effect:${request.orgId}:${request.id}`;
  const abort=new AbortController();
  const lost=()=>abort.abort();
  owner.on("error",lost);owner.on("end",lost);
  let locked=false;
  try {
    locked=(await owner.query("SELECT pg_try_advisory_lock(hashtextextended($1,0)) AS locked",[key])).rows[0].locked;
    if (!locked) return {ok:false,error:"Effect is still executing; redispatch disabled"};
    return await executeOwned(request,resolve,abort.signal);
  } finally {
    let destroy=abort.signal.aborted;
    if (locked && !destroy) {
      try {await owner.query("SELECT pg_advisory_unlock(hashtextextended($1,0))",[key]);}
      catch {destroy=true;}
    }
    owner.removeListener("error",lost);owner.removeListener("end",lost);owner.release(destroy);
  }
}

async function markUnknown(request:ActionRequestRecord) {
  await pool().query(`WITH unknown AS (
    UPDATE action_execution SET status='failed',error='Effect outcome unknown; automatic redispatch disabled',finished_at=now()
    WHERE org_id=$1 AND action_request_id=$2 AND executor='harness' AND status<>'succeeded' RETURNING id)
    UPDATE action_request SET status=CASE WHEN status IN ('approved','failed') THEN 'failed' ELSE status END,rejection_reason='Effect outcome unknown; automatic redispatch disabled',updated_at=now()
    WHERE org_id=$1 AND id=$2 AND EXISTS(SELECT 1 FROM unknown)`,[request.orgId,request.id]);
}

async function executeOwned(request:ActionRequestRecord,resolve:()=>Promise<ActionAdapter|undefined>,signal:AbortSignal) {
  const scope={orgId:request.orgId,runId:request.workRunId!};
  const args=[request.orgId,request.id];
  const saved=(await pool().query("SELECT id,status,result FROM action_execution WHERE org_id=$1 AND action_request_id=$2 AND executor='harness'",args)).rows[0];
  if (saved?.status === "succeeded") {
    startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"restored"});
    return {ok:true,outcome:saved.result as ActionExecutionOutcome};
  }
  // Ownership is exclusive: a surviving unfinished claim belongs to a dead attempt.
  if (saved) await markUnknown(request);
  if ((!saved && request.status !== "approved") || !request.harnessPrepared || !request.workRunId || !(await hasHumanActionApproval(request))) throw Error("Harness action requires a prepared human approval");
  const row=(await pool().query("SELECT harness_proposal FROM action_request WHERE org_id=$1 AND id=$2",args)).rows[0];
  const proposal=row?.harness_proposal;
  if (!proposal?.definition) throw Error("Harness proposal has no bound action definition");
  const actor=(await pool().query("SELECT actor_user_id,actor_role,backend FROM work_run WHERE org_id=$1 AND id=$2",[request.orgId,request.workRunId])).rows[0];
  if (!actor || !isDeepStrictEqual(proposal.actor,{userId:actor.actor_user_id,role:actor.actor_role,backend:actor.backend})) throw Error("Requesting actor changed; request approval again");
  const definition=await validateHarnessAction(scope,request.kind,proposal.payload);
  if (!isDeepStrictEqual(definition,proposal.definition)) throw Error("Action definition changed; request approval again");
  if (request.approvedByUserId) {
    const groups=await resolveUserGroups(request.orgId,request.approvedByUserId);
    if (!groups.groupIds.length) throw Error("Approver is no longer active");
    await assertMayDecide(request.orgId,request,{userId:request.approvedByUserId,role:groups.administrator ? "admin" : "member"});
  } else if (resolveDeploymentProfile() !== "solo") throw Error("Current deployment requires a named approver");
  const decision=evaluateActionPolicy({scope:request.scope,kind:request.kind,target:request.target,riskLevel:request.riskLevel},await listEnabledPolicies(request.orgId));
  if (decision.decision === "deny" || decision.decision === "no_policy" || decision.policy.id !== request.policyId) throw Error("Action policy changed; request approval again");
  const adapter=await resolve();
  if (!adapter) throw Error("No adapter available for approved Harness action");
  const frozen={scope:request.scope,kind:request.kind,target:request.target,payload:request.payload,policyId:request.policyId,riskLevel:request.riskLevel,actorUserId:request.actorUserId,actorRole:request.actorRole,actorBackend:request.actorBackend};
  if (!isDeepStrictEqual(frozen,request.harnessPrepared)) throw Error("Approved action arguments changed");
  signal.throwIfAborted();
  const idempotencyKey=`harness:${request.orgId}:${request.id}`;
  if (saved) {
    try {
      const outcome=await adapter.reconcile?.({request,idempotencyKey});
      signal.throwIfAborted();
      if (outcome) {
        await saveOutcome(request,saved.id,outcome,signal);
        startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"reconciled"});
        return {ok:true,outcome};
      }
    } catch { /* Status failure is uncertainty, never a retry of the effect. */ }
    await markUnknown(request);
    startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"outcome_unknown"});
    return {ok:false,error:"Effect outcome unknown; automatic redispatch disabled"};
  }
  const claim=await pool().query(`INSERT INTO action_execution(org_id,action_request_id,executor,payload,status,started_at)
    SELECT org_id,id,'harness',payload,'running',now() FROM action_request
    WHERE org_id=$1 AND id=$2 AND status='approved' AND harness_prepared=$3::jsonb
    AND jsonb_build_object('scope',scope,'kind',kind,'target',target,'payload',payload,'policyId',policy_id,'riskLevel',risk_level,
      'actorUserId',actor_user_id,'actorRole',actor_role,'actorBackend',actor_backend)=harness_prepared
    ON CONFLICT DO NOTHING RETURNING id`,[...args,JSON.stringify(frozen)]);
  if (!claim.rowCount) return {ok:false,error:"Effect already claimed or approval changed; automatic redispatch disabled"};
  startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"claimed"});
  try {
    signal.throwIfAborted();
    const outcome=await adapter({request,idempotencyKey});
    signal.throwIfAborted();
    await saveOutcome(request,claim.rows[0].id,outcome,signal);
    startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"recorded"});
    return {ok:true,outcome};
  } catch {
    // The adapter may have committed remotely. Even a RetryableActionAdapterError
    // cannot authorize another attempt at this boundary.
    await markUnknown(request);
    startupEvent("harness.effect",{runId:request.workRunId,actionRequestId:request.id,outcome:"outcome_unknown"});
    return {ok:false,error:"Effect outcome unknown; automatic redispatch disabled"};
  }
}

async function saveOutcome(request:ActionRequestRecord,executionId:string,outcome:ActionExecutionOutcome,signal:AbortSignal) {
    const encoded=JSON.stringify(outcome);
    if (Buffer.byteLength(encoded)>262144) throw Error("Effect receipt exceeds limit");
    const client=await pool().connect();
    try {
      await client.query("BEGIN");
      const receipt=await client.query("UPDATE action_execution SET status='succeeded',result=$2::jsonb,finished_at=now() WHERE id=$1",[executionId,encoded]);
      const terminal=await client.query("UPDATE action_request SET status=CASE WHEN status='approved' OR (status='failed' AND rejection_reason='Effect outcome unknown; automatic redispatch disabled') THEN 'executed' ELSE status END,updated_at=now() WHERE org_id=$1 AND id=$2",[request.orgId,request.id]);
      if (receipt.rowCount !== 1 || terminal.rowCount !== 1) throw Error("Effect journal disappeared");
      signal.throwIfAborted();
      await client.query("COMMIT");
    } catch(error) {await client.query("ROLLBACK");throw error;} finally {client.release();}
}

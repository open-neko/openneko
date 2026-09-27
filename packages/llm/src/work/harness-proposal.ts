import type {AgentEvent,AgentRunResult} from "../agent-backend";
import { and, db, eq, holds, pack_action_definition, pool } from "@neko/db";
import { z } from "zod";
import type { AgentControlPlane } from "./control-plane";
import { entitlementActorForRun } from "./entitlement-scope";
import { recordHarnessOperation } from "./harness-operation";

const proposalSchema=z.object({
  action:z.string().trim().min(1).max(128),
  arguments:z.record(z.string(),z.unknown()),
  summary:z.string().trim().min(1).max(1000),
}).strict();

/** Frozen by the host at run admission; never supplied by the model. */
export type HarnessActionGrant = {
  kind: string;
  source: "pack" | "plugin";
  scope: "external" | "internal";
  /** Installed plugin entitlement, when the descriptor belongs to one. */
  pluginName?: string;
};

/** Resolve the installed contract and current actor on the trusted side. */
export async function validateHarnessAction(scope:{orgId:string;runId:string},kind:string,payload:Record<string,unknown>) {
  const [row]=await db().select().from(pack_action_definition).where(and(
    eq(pack_action_definition.org_id,scope.orgId),eq(pack_action_definition.kind,kind),
    eq(pack_action_definition.enabled,true),eq(pack_action_definition.readiness,"ready"),
  )).limit(1);
  if (!row) throw Error("Action is not installed and ready");
  const actor=await entitlementActorForRun(scope.orgId,scope.runId);
  if (!actor || !(await holds(actor,"action",kind)).allowed) throw Error("Action is not available to this actor");
  const definition=row.definition as {inputSchema?:Record<string,unknown>};
  if (!definition.inputSchema) throw Error("Action has no input schema");
  // Conversion errors reject unsupported contracts. Never narrow or coerce model arguments.
  const schema=z.fromJSONSchema(definition.inputSchema);
  if (!schema.safeParse(payload).success) throw Error("Action arguments do not match the installed schema");
  return row.definition;
}

export async function validateHarnessPluginAction(scope:{orgId:string;runId:string},grant:HarnessActionGrant) {
  if (grant.source !== "plugin") throw Error("Expected a plugin action grant");
  const actor=await entitlementActorForRun(scope.orgId,scope.runId);
  if (!actor || !(await holds(actor,"action",grant.kind)).allowed ||
      (grant.pluginName && !(await holds(actor,"integration",grant.pluginName)).allowed)) {
    throw Error("Action is no longer available to this actor");
  }
  return {harnessSource:"plugin",kind:grant.kind,scope:grant.scope,
    ...(grant.pluginName ? {pluginName:grant.pluginName} : {})};
}

export async function proposeHarnessAction(
  scope:{orgId:string;runId:string;operationLimit?:number;workflowRunId?:string;actionGrants?:readonly HarnessActionGrant[]},body:Record<string,unknown>,cp:AgentControlPlane,
):Promise<unknown> {
  const instruction=typeof body.instruction === "string" ? body.instruction : "";
  if (Buffer.byteLength(instruction)>65536) return {error:"Proposal exceeds limit"};
  let proposal:z.infer<typeof proposalSchema>;
  try {proposal=proposalSchema.parse(JSON.parse(instruction));}
  catch {return {error:"Invalid proposal"};}
  return recordHarnessOperation(scope,body.operationId,{tool:"propose",instruction},async()=>{
    const grant=scope.actionGrants?.find(item=>item.kind===proposal.action);
    if (!grant) return {status:"denied",reason:"Action was not admitted for this run"};
    if (scope.workflowRunId) {
      const owned=await pool().query("SELECT 1 FROM workflow_run WHERE org_id=$1 AND id=$2 AND work_run_id=$3 AND status='running'",
        [scope.orgId,scope.workflowRunId,scope.runId]);
      if (!owned.rowCount) throw Error("Workflow action run binding changed");
    }
    let definition:unknown;
    if (grant.source === "pack") {
      try {definition={harnessSource:"pack",snapshot:await validateHarnessAction(scope,proposal.action,proposal.arguments)};}
      catch {return {status:"denied",reason:"Action unavailable or arguments invalid"};}
    } else {
      try {definition=await validateHarnessPluginAction(scope,grant);}
      catch {return {status:"denied",reason:"Action is no longer available to this actor"};}
    }
    const decision=await cp.evaluateActionPolicy({orgId:scope.orgId,scope:grant.scope,kind:proposal.action,riskLevel:"critical"});
    if (decision.decision === "deny" || decision.decision === "no_policy") return {status:"denied",reason:"Action policy does not permit this proposal"};
    const request=await cp.createActionRequest({orgId:scope.orgId,workRunId:scope.runId,
      ...(scope.workflowRunId ? {workflowRunId:scope.workflowRunId,requestedByRunId:scope.workflowRunId} : {}),
      harnessOperationId:Number(body.operationId),harnessDefinition:definition as Record<string,unknown>,scope:grant.scope,kind:proposal.action,
      payload:proposal.arguments,status:"pending_approval",policyId:decision.policy.id,
      riskLevel:"critical",summary:proposal.summary,intent:proposal.summary});
    if (request.status !== "pending_approval") throw Error("Proposal was not prepared for approval");
    return {id:request.id,status:"pending_approval"};
  });
}

/** Cards are reconstructed from authoritative rows, including after terminal adoption. */
export async function emitHarnessApprovals(result:AgentRunResult,scope:{orgId:string;runId:string},emit?: (event:AgentEvent)=>Promise<void>|void) {
    const state=result.backendState?.harness as {proposals?: {id?:string;status:string}[]} | undefined;
    if (!emit || !state?.proposals?.length) return;
    const {pool}=await import("@neko/db");
    const rows=(await pool().query("SELECT id,kind,scope,summary,intent,status FROM action_request WHERE org_id=$1 AND work_run_id=$2 AND actor_backend='harness' AND harness_prepared IS NOT NULL",[scope.orgId,scope.runId])).rows;
    for (const proposal of state.proposals) {
        const row=rows.find(row=>row.id===proposal.id);
        if (!row || row.status!=="pending_approval") continue;
        await emit({type:"action_request_emit",action_request_id:row.id,kind:row.kind,scope:row.scope,decision:"pending_approval",summary:row.summary,intent:row.intent});
    }
}

/** Harness emits one final assistant message; recovery must not append it again. */
export async function emitHarnessRestoredAnswer(result:AgentRunResult,scope:{orgId:string;runId:string},emit:(event:AgentEvent)=>Promise<void>) {
    if (!result.finalText || (result.backendState?.harness as {kind?:string}|undefined)?.kind === "clarification") return;
    const {pool}=await import("@neko/db");
    const existing=await pool().query(`SELECT 1 FROM work_run_event WHERE org_id=$1 AND run_id=$2
      AND kind='message' AND payload->>'role'='assistant' LIMIT 1`,[scope.orgId,scope.runId]);
    if (!existing.rowCount) await emit({type:"message",role:"assistant",content:result.finalText});
}

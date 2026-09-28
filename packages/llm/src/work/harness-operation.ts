import { pool } from "@neko/db";
import { startupEvent } from "@neko/telemetry/startup";

/** Admit before dispatch; publish before delivery. Never replay an ambiguous call. */
export async function recordHarnessLookup(
  scope: {orgId:string; runId:string; operationLimit?:number}, operationId:unknown,
  request: {instruction:string; dataSourceId?:string; maxSteps:number},
  execute: () => Promise<unknown>, signal?: AbortSignal,
): Promise<unknown> {
  return recordHarnessOperation(scope, operationId, request, execute, signal);
}

/** Tool identity lives in the same primary-key space; proposals cannot reset the budget. */
export async function recordHarnessOperation(
  scope: {orgId:string; runId:string; operationLimit?:number}, operationId:unknown,
  request: {instruction:string; tool?:"propose"|"memory_save"|"skill_create"|"skill_update"|"workflow_save"|"workflow_delete"|"rule_save"|"workflow_output"|"process_run"; binding?:string; dataSourceId?:string; maxSteps?:number},
  execute: () => Promise<unknown>, signal?: AbortSignal,
): Promise<unknown> {
  const limit=scope.operationLimit ?? 4;
  if (!Number.isInteger(limit) || limit<1 || limit>32 ||
      !Number.isInteger(operationId) || Number(operationId)<1 || Number(operationId)>limit ||
      !request.instruction.trim() || Buffer.byteLength(request.instruction)>(request.tool === "propose" || request.tool === "workflow_save" || request.tool === "rule_save" ? 65536 : request.tool === "workflow_output" || request.tool === "process_run" || request.tool === "skill_create" || request.tool === "skill_update" ? 131072 : request.tool === "memory_save" || request.tool === "workflow_delete" ? 4096 : 8000) ||
      ((request.tool === "memory_save" || request.tool === "skill_create" || request.tool === "skill_update" || request.tool === "workflow_save" || request.tool === "workflow_delete" || request.tool === "rule_save" || request.tool === "workflow_output" || request.tool === "process_run") && !/^[a-f0-9]{64}$/.test(request.binding ?? ""))) {
    return {error:"Invalid Harness lookup operation"};
  }
  if (signal?.aborted) return {error:"Harness lookup cancelled before dispatch"};
  const args=[scope.orgId,scope.runId,operationId,JSON.stringify(request)];
  const admitted=await pool().query(`INSERT INTO harness_operation (org_id,run_id,operation_id,request)
    SELECT org_id,run_id,$3,$4::jsonb FROM harness_run_journal
    WHERE org_id=$1 AND run_id=$2 AND result IS NULL AND operation_limit >= $3
    ON CONFLICT DO NOTHING RETURNING operation_id`,args);
  if (!admitted.rowCount) {
    const row=(await pool().query(`SELECT request=$4::jsonb AS matches, result IS NOT NULL AS finished
      FROM harness_operation WHERE org_id=$1 AND run_id=$2 AND operation_id=$3`,args)).rows[0];
    const pending=!row && (await pool().query(`SELECT 1 FROM harness_operation
      WHERE org_id=$1 AND run_id=$2 AND result IS NULL LIMIT 1`,[scope.orgId,scope.runId])).rowCount;
    const outcome=!row ? pending ? "outcome_unknown" : "run_not_admitted" : !row.matches ? "conflict" : row.finished ? "recorded" : "outcome_unknown";
    startupEvent("harness.operation",{runId:scope.runId,operationId:Number(operationId),outcome});
    // Receipt recovery is host-authorized. A repeated bearer request gets no saved data.
    return {error:`Harness operation ${outcome}; automatic dispatch disabled`};
  }
  startupEvent("harness.operation",{runId:scope.runId,operationId:Number(operationId),outcome:"admitted"});
  try {
    signal?.throwIfAborted();
    const result=await execute();
    signal?.throwIfAborted();
    const encoded=JSON.stringify(result);
    if (!encoded || encoded==="null" || Buffer.byteLength(encoded)>262144) throw Error("Harness operation result exceeds limit or is invalid");
    const saved=await pool().query(`UPDATE harness_operation SET result=$4::jsonb,finished_at=now()
      WHERE org_id=$1 AND run_id=$2 AND operation_id=$3`,[scope.orgId,scope.runId,operationId,encoded]);
    if (saved.rowCount !== 1) throw Error("Harness operation journal disappeared");
    startupEvent("harness.operation",{runId:scope.runId,operationId:Number(operationId),outcome:"recorded"});
    return result;
  } catch {
    startupEvent("harness.operation",{runId:scope.runId,operationId:Number(operationId),outcome:"outcome_unknown"});
    return {error:"Harness operation outcome unknown; automatic dispatch disabled"};
  }
}

/** Called only after the launcher validates current input/authorization and owns the run. */
export async function loadHarnessOperations(scope:{orgId:string;runId:string}) {
  const rows=(await pool().query(`SELECT operation_id,request,result FROM harness_operation
    WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id LIMIT 32`,[scope.orgId,scope.runId])).rows;
  return rows.map(row=>({id:row.operation_id,...(row.request.tool ? {tool:row.request.tool} : {}),...(row.request.binding ? {binding:row.request.binding} : {}),instruction:row.request.instruction,result:row.result}));
}

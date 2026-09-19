import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { createRequire } from "node:module";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { db,pool,organization,work_thread,work_run,pack_action_definition,action_policy,eq } from "@neko/db";
import { expect,it } from "vitest";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { approveActionRequest,getActionRequest } from "../src/workflows/action-store";
import { executeApprovedActionRequest,registerActionAdapter } from "../src/workflows/action-executor";
import { loadHarnessOperations,recordHarnessLookup } from "../src/work/harness-operation";
const live=process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live.each(["before_dispatch","after_commit","after_commit_reconcile","after_receipt"])("broker approval and effect recovery at %s",async phase=>{
 if(process.env.NEKO_PG_PORT!=="18119") throw Error("isolated database required");
 const orgId=`effect-${randomUUID()}`,runId=randomUUID(),threadId=randomUUID();
 const kind="harness_effect_fixture";
 const recoverable=phase === "after_receipt" || phase === "after_commit_reconcile";
 await db().insert(organization).values({id:orgId,name:"Effect acceptance"});
 await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Effect"});
 await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
 await pool().query("INSERT INTO harness_run_journal(org_id,run_id,fingerprint) VALUES($1,$2,$3)",[orgId,runId,"c".repeat(64)]);
 await db().insert(pack_action_definition).values({org_id:orgId,kind,readiness:"ready",definition_hash:"fixture",definition:{kind,inputSchema:{type:"object",properties:{value:{type:"integer"}},required:["value"],additionalProperties:false}}});
 await db().insert(action_policy).values({org_id:orgId,name:"Fixture approval",mode:"approval_required",applies_to_kinds:[kind],applies_to_scopes:["external"]});
 const broker=await startAgentBroker({port:0,hostAlias:"127.0.0.1",controlPlane:inProcessControlPlane});
 let effects=0;
 const receipts=new Map<string,unknown>();
 const service=createServer((req,res)=>{
  req.resume();res.setHeader("content-type","application/json");
  const key=String(req.headers["idempotency-key"] ?? "");
  if (req.method === "POST" && !receipts.has(key)) {effects++;receipts.set(key,{result:{changed:true},externalRef:"fixture-effect"});}
  res.end(JSON.stringify(receipts.get(key) ?? null));
 });
 await new Promise<void>(resolve=>service.listen(0,"127.0.0.1",resolve));
 let child:ReturnType<typeof spawn>|undefined;
 try {
  const token=broker.tokenFor({orgId,runId,threadId,kind:"work",profile:"harness-governed"});
  const send=async(operationId:number,args:unknown)=>{
   const res=await fetch(`http://127.0.0.1:${broker.port}/v1/harness/propose`,{method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({operationId,instruction:JSON.stringify({action:kind,arguments:args,summary:"Update the synthetic value"})})});
   expect(res.status).toBe(200);return res.json();
  };
  expect(await send(1,{value:"invalid"})).toMatchObject({status:"denied"});
  const receipt=await send(2,{value:42});expect(receipt.status).toBe("pending_approval");
  expect(await send(2,{value:42})).toMatchObject({error:expect.stringContaining("recorded")});
  expect(await recordHarnessLookup({orgId,runId},2,{instruction:"collision",maxSteps:12},async()=>{throw Error("must not dispatch");})).toMatchObject({error:expect.stringContaining("conflict")});
  const operations=await loadHarnessOperations({orgId,runId});expect(operations[1].tool).toBe("propose");expect(operations[1].result).toEqual(receipt);
  await approveActionRequest({orgId,id:receipt.id,approverUserId:null,approver:{userId:null,role:"admin"}});
  const tsx=createRequire(import.meta.url).resolve("tsx",{paths:[join(process.cwd(),"../../apps/worker")]});
  const module=pathToFileURL(join(process.cwd(),"src/workflows/action-executor.ts")).href;
  const url=`http://127.0.0.1:${(service.address() as {port:number}).port}`;
  child=spawn(process.execPath,["--import",tsx,"--input-type=module","-e",`
   import {registerActionAdapter,executeApprovedActionRequest} from ${JSON.stringify(module)};
   registerActionAdapter(${JSON.stringify(kind)},async({idempotencyKey})=>{
    ${phase === "before_dispatch" ? 'process.send("ready");await new Promise(()=>{});' : ''}
    const outcome=await (await fetch(${JSON.stringify(url)},{method:"POST",headers:{"idempotency-key":idempotencyKey}})).json();
    ${phase.startsWith("after_commit") ? 'process.send("ready");await new Promise(()=>{});' : ''}
    return outcome;
   });
   await executeApprovedActionRequest(${JSON.stringify(orgId)},${JSON.stringify(receipt.id)});
   process.send("ready");await new Promise(()=>{});
  `],{stdio:["ignore","ignore","pipe","ipc"]});
  let stderr="";child.stderr?.on("data",chunk=>{stderr=(stderr+chunk).slice(-4096);});
  await Promise.race([once(child,"message",{signal:AbortSignal.timeout(15000)}),once(child,"exit").then(()=>{throw Error(stderr);})]);
  const adapter=Object.assign(async()=>{effects++;return {result:{duplicate:true}};},phase === "after_commit_reconcile" ? {
   reconcile:async({idempotencyKey}:{idempotencyKey:string})=> (await fetch(url,{headers:{"idempotency-key":idempotencyKey}})).json(),
  } : {});
  registerActionAdapter(kind,adapter);
  const overlap=await executeApprovedActionRequest(orgId,receipt.id);
  expect(overlap.ok).toBe(phase === "after_receipt");
  const exited=once(child,"exit");child.kill("SIGKILL");await exited;
  const recovered=await executeApprovedActionRequest(orgId,receipt.id);
  expect(recovered.ok).toBe(recoverable);
  if(recoverable) expect(recovered.outcome).toEqual({result:{changed:true},externalRef:"fixture-effect"});
  else expect(recovered.error).toContain("outcome unknown");
  expect(effects).toBe(phase === "before_dispatch" ? 0 : 1);
  expect((await getActionRequest(orgId,receipt.id))?.status).toBe(recoverable ? "executed" : "failed");
  expect((await pool().query("SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1",[orgId])).rows[0].n).toBe(1);
  if (phase === "after_receipt") {
   const next=await send(3,{value:43});
   await approveActionRequest({orgId,id:next.id,approverUserId:null,approver:{userId:null,role:"admin"}});
   await pool().query("UPDATE action_policy SET mode='never' WHERE org_id=$1",[orgId]);
   await expect(executeApprovedActionRequest(orgId,next.id)).rejects.toThrow("policy changed");
   await pool().query("UPDATE action_policy SET mode='approval_required' WHERE org_id=$1",[orgId]);
   await pool().query("UPDATE pack_action_definition SET definition=definition || '{\"revision\":2}'::jsonb WHERE org_id=$1",[orgId]);
   await expect(executeApprovedActionRequest(orgId,next.id)).rejects.toThrow("definition changed");
   await pool().query("UPDATE pack_action_definition SET definition=definition - 'revision' WHERE org_id=$1",[orgId]);
   await pool().query("UPDATE work_run SET actor_role='admin' WHERE org_id=$1 AND id=$2",[orgId,runId]);
   await expect(executeApprovedActionRequest(orgId,next.id)).rejects.toThrow("actor changed");
   expect(effects).toBe(1);
   expect((await pool().query("SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1",[orgId])).rows[0].n).toBe(1);
  }

 } finally {
  child?.kill("SIGKILL");await broker.close();await new Promise<void>(resolve=>service.close(()=>resolve()));
  await db().delete(organization).where(eq(organization.id,orgId));await pool().end();
 }
},30000);

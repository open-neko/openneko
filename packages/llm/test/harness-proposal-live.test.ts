import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { createRequire } from "node:module";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { db, pool, organization, work_thread, work_run, eq } from "@neko/db";
import { expect, it } from "vitest";
import { approveActionRequest, autoApprovePreparedActionRequest, createActionRequest, getActionRequest, registerActionRequestCreatedHook, rejectActionRequest, updateActionRequestPayload } from "../src/workflows/action-store";
import { executeApprovedActionRequest } from "../src/workflows/action-executor";
const live=process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("Harness proposal identity and frozen approval survive retries and preparation death",async()=>{
  if(process.env.NEKO_PG_PORT!=="18119") throw Error("isolated M3 database required");
  const orgId=`proposal-${randomUUID()}`,threadId=randomUUID(),runId=randomUUID();
  await db().insert(organization).values({id:orgId,name:"Proposal acceptance"});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Proposal"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  await pool().query("INSERT INTO harness_run_journal(org_id,run_id,fingerprint) VALUES($1,$2,$3)",[orgId,runId,"a".repeat(64)]);
  const input={orgId,workRunId:runId,harnessOperationId:1,scope:"external" as const,kind:"harness_fixture",payload:{target:"reference",value:42},status:"approved" as const,actorRole:"admin",actorBackend:"hermes",summary:"Update synthetic reference"};
  const approver={userId:null,role:"admin" as const};
  let preparations=0;
  const unregister=registerActionRequestCreatedHook(async request=>{
    if(request.orgId!==orgId) return;
    preparations++;
    expect(request.status).toBe("draft");
    expect(request.actorRole).toBe("service");
    expect(request.actorBackend).toBe("harness");
    await expect(approveActionRequest({orgId,id:request.id,approverUserId:null,approver})).rejects.toThrow("preparation is not complete");
    const prepared=await updateActionRequestPayload({orgId,id:request.id,payload:{...request.payload,preparedVersion:1}});
    if(request.harnessOperationId===3) await updateActionRequestPayload({orgId,id:request.id,payload:{changedAfterPreflight:true}});
    return prepared;
  });
  let child:ReturnType<typeof spawn>|undefined;
  try {
    const request=await createActionRequest(input);
    expect(request.status).toBe("pending_approval");
    expect(request.harnessPrepared?.payload).toEqual({...input.payload,preparedVersion:1});
    expect((await createActionRequest({...input,payload:{value:42,target:"reference"}})).id).toBe(request.id);
    expect(preparations).toBe(1);
    await expect(createActionRequest({...input,payload:{value:43,target:"reference"}})).rejects.toThrow("conflicts");
    await expect(createActionRequest({...input,harnessOperationId:5})).rejects.toThrow("Invalid Harness");
    await expect(createActionRequest({...input,orgId:"wrong-org"})).rejects.toThrow("admitted run");
    await expect(updateActionRequestPayload({orgId,id:request.id,payload:{value:43}})).rejects.toThrow("frozen");
    await expect(approveActionRequest({orgId,id:request.id,approverUserId:null})).rejects.toThrow("current approver identity");
    await expect(approveActionRequest({orgId,id:request.id,approverUserId:null,approver:{userId:null,role:"service"}})).rejects.toThrow();
    await expect(autoApprovePreparedActionRequest({orgId,id:request.id,reason:"bypass"})).rejects.toThrow("explicit approval");
    const decisions=await Promise.allSettled([
      approveActionRequest({orgId,id:request.id,approverUserId:null,approver}),
      rejectActionRequest({orgId,id:request.id,approverUserId:null,approver}),
    ]);
    expect(decisions.filter(result=>result.status==="fulfilled")).toHaveLength(1);
    const decided=await getActionRequest(orgId,request.id);
    expect(["approved","rejected"]).toContain(decided?.status);
    expect((await createActionRequest(input)).status).toBe(decided!.status);
    expect(preparations).toBe(1);
    await expect(updateActionRequestPayload({orgId,id:request.id,payload:{value:44}})).rejects.toThrow("frozen");
    if(decided?.status==="approved") await expect(executeApprovedActionRequest(orgId,request.id)).rejects.toThrow("no bound action definition");
    await pool().query("UPDATE harness_run_journal SET fingerprint=$3 WHERE org_id=$1 AND run_id=$2",[orgId,runId,"b".repeat(64)]);
    await expect(createActionRequest(input)).rejects.toThrow("conflicts");
    await pool().query("UPDATE harness_run_journal SET fingerprint=$3 WHERE org_id=$1 AND run_id=$2",[orgId,runId,"a".repeat(64)]);
    await expect(createActionRequest({...input,harnessOperationId:3})).rejects.toThrow("changed during preparation");
    await expect(createActionRequest({...input,harnessOperationId:3})).rejects.toThrow("outcome unknown");
    expect(preparations).toBe(2);
    // Kill after the request row exists but before preparation is published.
    const tsx=createRequire(import.meta.url).resolve("tsx",{paths:[join(process.cwd(),"../../apps/worker")]});
    const module=pathToFileURL(join(process.cwd(),"src/workflows/action-store.ts")).href;
    const crashInput={...input,harnessOperationId:2};
    child=spawn(process.execPath,["--import",tsx,"--input-type=module","-e",`
      import {createActionRequest,registerActionRequestCreatedHook} from ${JSON.stringify(module)};
      registerActionRequestCreatedHook(async request=>{process.send(request.id);await new Promise(()=>{});});
      await createActionRequest(${JSON.stringify(crashInput)});
    `],{stdio:["ignore","ignore","pipe","ipc"]});
    let error="";child.stderr?.on("data",chunk=>{error=(error+chunk).slice(-4096);});
    const [crashId]=await Promise.race([
      once(child,"message",{signal:AbortSignal.timeout(10_000)}),
      once(child,"exit").then(()=>{throw Error(`Proposal child exited: ${error}`);}),
    ]);
    await expect(createActionRequest(crashInput)).rejects.toThrow("outcome unknown");
    const exited=once(child,"exit");child.kill("SIGKILL");await exited;
    await expect(createActionRequest(crashInput)).rejects.toThrow("outcome unknown");
    await expect(approveActionRequest({orgId,id:crashId,approverUserId:null,approver})).rejects.toThrow("preparation is not complete");
    expect(preparations).toBe(2);
    expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1 AND work_run_id=$2",[orgId,runId])).rows[0].n).toBe(3);
  } finally {
    child?.kill("SIGKILL");unregister();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},30_000);

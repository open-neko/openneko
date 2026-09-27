import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { db, pool, organization, data_source, work_thread, work_run, eq } from "@neko/db";
import { expect, it } from "vitest";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { recordHarnessLookup } from "../src/work/harness-operation";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
live("queued Harness workflow output binds the run and does not redispatch", async () => {
  if (process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 database required");
  const orgId = `workflow-output-${randomUUID()}`;
  const threadId = randomUUID(), runId = randomUUID(), workflowId = randomUUID(), workflowRunId = randomUUID();
  await db().insert(organization).values({ id: orgId, name: "Workflow output test" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Output" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
  await pool().query("INSERT INTO workflow_definition (id,org_id,name) VALUES ($1,$2,'Test output')", [workflowId,orgId]);
  await pool().query("INSERT INTO workflow_run (id,org_id,workflow_id,thread_id,work_run_id,trigger_kind,status) VALUES ($1,$2,$3,$4,$5,'manual','running')",
    [workflowRunId,orgId,workflowId,threadId,runId]);
  await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint) VALUES ($1,$2,$3)", [orgId,runId,"0".repeat(64)]);
  const events: unknown[] = [];
  const broker = await startAgentBroker({port:0,controlPlane:inProcessControlPlane,onEvents:async (_,emitted)=>{events.push(...emitted);}});
  const token = broker.tokenFor({orgId,runId,threadId,kind:"workflow",profile:"harness-read-only",workflowRunId,workflowOutput:true});
  const call = () => fetch(`http://127.0.0.1:${broker.port}/v1/harness/workflow-output/emit`, {
    method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},
    body:JSON.stringify({operationId:1,binding:"a".repeat(64),instruction:JSON.stringify({kind:"finding",title:"Reference",body:"REF-42"}),workflowRunId:randomUUID(),orgId:"forged"}),
  });
  try {
    const first = await (await call()).json() as {ok:boolean;outputId:string;kind:string};
    expect(first).toMatchObject({ok:true,kind:"finding"});
    expect(first.outputId).toBeTruthy();
    expect(await (await call()).json()).toEqual({error:"Harness operation recorded; automatic dispatch disabled"});
    const rows = (await pool().query("SELECT id,org_id,workflow_run_id,work_run_id FROM workflow_output WHERE org_id=$1",[orgId])).rows;
    expect(rows).toEqual([{id:first.outputId,org_id:orgId,workflow_run_id:workflowRunId,work_run_id:runId}]);
    expect(events).toEqual([{type:"output_emit",output_id:first.outputId,kind:"finding"}]);
  } finally {
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},20_000);

live.each([false,true])("operation dispatch is not repeated after SIGKILL (result saved: %s)",async saved=>{
  if (process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 database required");
  const scope={orgId:`operation-${randomUUID()}`,runId:randomUUID()};
  const request={instruction:"Read the reference",maxSteps:12};
  await db().insert(organization).values({id:scope.orgId,name:"Operation crash test"});
  await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint) VALUES ($1,$2,$3)",[scope.orgId,scope.runId,"0".repeat(64)]);
  let calls=0;
  const server=createServer((_,res)=>{calls++;res.setHeader("content-type","application/json");res.end('{"response":{"answer":"saved reference"}}');});
  await new Promise<void>(resolve=>server.listen(0,"127.0.0.1",resolve));
  const url=`http://127.0.0.1:${(server.address() as {port:number}).port}`;
  const tsx=createRequire(import.meta.url).resolve("tsx",{paths:[join(process.cwd(),"../../apps/worker")]});
  const module=pathToFileURL(join(process.cwd(),"src/work/harness-operation.ts")).href;
  const child=spawn(process.execPath,["--import",tsx,"--input-type=module","-e",`
    import {recordHarnessLookup} from ${JSON.stringify(module)};
    await recordHarnessLookup(${JSON.stringify(scope)},1,${JSON.stringify(request)},async()=>{
      const response=await (await fetch(${JSON.stringify(url)})).json();
      ${saved ? '' : 'process.send("ready");await new Promise(()=>{});'}
      return response;
    });
    ${saved ? 'process.send("ready");' : ''}
    await new Promise(()=>{});
  `],{stdio:["ignore","ignore","pipe","ipc"]});
  let stderr="";
  child.stderr?.on("data",chunk=>{stderr=(stderr+chunk).slice(-4096);});
  try {
    await Promise.race([once(child,"message",{signal:AbortSignal.timeout(10_000)}),once(child,"exit").then(()=>{throw Error(`child exited: ${stderr}`);})]);
    const duplicate=()=>recordHarnessLookup(scope,1,request,async()=>{calls++;return {response:{answer:"duplicate"}};});
    expect(await duplicate()).toEqual({error:`Harness operation ${saved ? "recorded" : "outcome_unknown"}; automatic dispatch disabled`});
    const exited=once(child,"exit");child.kill("SIGKILL");await exited;
    expect(await duplicate()).toEqual({error:`Harness operation ${saved ? "recorded" : "outcome_unknown"}; automatic dispatch disabled`});
    expect(await recordHarnessLookup(scope,1,{...request,instruction:"changed"},async()=>{calls++;})).toEqual({error:"Harness operation conflict; automatic dispatch disabled"});
    expect(calls).toBe(1);
    const row=(await pool().query("SELECT request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2",[scope.orgId,scope.runId])).rows[0];
    expect(row.request).toEqual(request);
    expect(row.result).toEqual(saved ? {response:{answer:"saved reference"}} : null);
    expect(Boolean(row.finished_at)).toBe(saved);
    // Missing admission and invalid IDs cannot reach the external callback.
    expect(await recordHarnessLookup({...scope,runId:randomUUID()},1,request,async()=>{calls++;})).toEqual({error:"Harness operation run_not_admitted; automatic dispatch disabled"});
    expect(await recordHarnessLookup(scope,5,request,async()=>{calls++;})).toEqual({error:"Invalid Harness lookup operation"});
    if (!saved) expect(await recordHarnessLookup(scope,2,request,async()=>{calls++;})).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    expect(calls).toBe(1);
    const admit=async(limit=4)=>{const runId=randomUUID();await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint,operation_limit) VALUES ($1,$2,$3,$4)",[scope.orgId,runId,"0".repeat(64),limit]);return {...scope,runId};};
    const cancellationScope=await admit();
    expect(await recordHarnessLookup(cancellationScope,3,request,async()=>{calls++;},AbortSignal.abort())).toEqual({error:"Harness lookup cancelled before dispatch"});
    expect(calls).toBe(1);
    const cancelled=new AbortController();
    expect(await recordHarnessLookup(cancellationScope,3,request,async()=>{
      cancelled.abort();return {response:{answer:"late result"}};
    },cancelled.signal)).toEqual({error:"Harness operation outcome unknown; automatic dispatch disabled"});
    expect((await pool().query("SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2 AND operation_id=3",[scope.orgId,cancellationScope.runId])).rows[0].result).toBeNull();
    expect(await recordHarnessLookup(cancellationScope,4,request,async()=>{calls++;})).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    expect(calls).toBe(1);
    // A lost journal after execution must never be acknowledged as durable success.
    const lostScope=await admit();
    expect(await recordHarnessLookup(lostScope,2,request,async()=>{
      await pool().query("DELETE FROM harness_operation WHERE org_id=$1 AND run_id=$2 AND operation_id=2",[scope.orgId,lostScope.runId]);
      return {response:{answer:"unrecorded"}};
    })).toEqual({error:"Harness operation outcome unknown; automatic dispatch disabled"});
    expect(await recordHarnessLookup({...lostScope,operationLimit:8},5,request,async()=>{calls++;})).toEqual({error:"Harness operation run_not_admitted; automatic dispatch disabled"});
    const budgeted={...await admit(8),operationLimit:8};
    expect(await recordHarnessLookup(budgeted,5,request,async()=>{calls++;return {response:{answer:"fifth"}};})).toEqual({response:{answer:"fifth"}});
    expect(await recordHarnessLookup(budgeted,5,request,async()=>{calls++;})).toEqual({error:"Harness operation recorded; automatic dispatch disabled"});
    expect(await recordHarnessLookup(budgeted,9,request,async()=>{calls++;})).toEqual({error:"Invalid Harness lookup operation"});
    expect(calls).toBe(2);
  } finally {
    child.kill("SIGKILL");
    await new Promise<void>(resolve=>server.close(()=>resolve()));
    await db().delete(organization).where(eq(organization.id,scope.orgId));
    await pool().end();
  }
},20_000);

live.each(["status","lookup"])("broker disconnect cancels GraphJin %s request without replay",async phase=>{
  if (process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 database required");
  const orgId=`cancel-${randomUUID()}`,runId=randomUUID(),threadId=randomUUID();
  let lookups=0;
  const server=createServer((req,res)=>{
    req.resume();
    if (req.url==="/api/v1/agent/status" && phase==="lookup") {
      res.setHeader("content-type","application/json");
      res.end(JSON.stringify({ready:true,read_only:true}));
      return;
    }
    if (req.url==="/api/v1/agent") lookups++;
    res.on("close",()=>server.emit("upstreamClosed"));
    server.emit("pending");
  });
  await new Promise<void>(resolve=>server.listen(0,"127.0.0.1",resolve));
  const source=`http://127.0.0.1:${(server.address() as {port:number}).port}/api/v1/graphql`;
  await db().insert(organization).values({id:orgId,name:"Broker cancellation test"});
  await db().insert(data_source).values({org_id:orgId,graphql_url:source,kind:"graphjin",auth_mode:"none",is_default:true});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Cancellation"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint) VALUES ($1,$2,$3)",[orgId,runId,"0".repeat(64)]);
  const broker=await startAgentBroker({port:0,controlPlane:inProcessControlPlane});
  const token=broker.tokenFor({orgId,runId,threadId,kind:"work"});
  const url=`http://127.0.0.1:${broker.port}/v1/harness/lookup`;
  const options={method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({operationId:1,instruction:"Read the reference"})};
  const abort=new AbortController();
  try {
    const pending=once(server,"pending",{signal:AbortSignal.timeout(10_000)});
    const response=fetch(url,{...options,signal:AbortSignal.any([abort.signal,AbortSignal.timeout(10_000)])}).catch(error=>error);
    await Promise.race([pending,response.then(()=>{throw Error("broker completed before reaching downstream");})]);
    const closed=once(server,"upstreamClosed",{signal:AbortSignal.timeout(3000)});
    abort.abort();
    await response;
    await closed;
    const retry=await (await fetch(url,options)).json();
    expect(retry).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    expect(lookups).toBe(phase==="lookup" ? 1 : 0);
    const row=(await pool().query("SELECT result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,runId])).rows[0];
    expect(row).toEqual({result:null,finished_at:null});
  } finally {
    abort.abort();
    server.closeAllConnections();
    await new Promise<void>(resolve=>server.close(()=>resolve()));
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},20_000);

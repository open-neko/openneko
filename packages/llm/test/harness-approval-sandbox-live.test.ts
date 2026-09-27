import { randomUUID } from "node:crypto";
import { mkdir,writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db,pool,organization,work_thread,work_run,pack_action_definition,action_policy,eq } from "@neko/db";
import { expect,it } from "vitest";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import { approveActionRequest,getActionRequest } from "../src/workflows/action-store";
import { executeApprovedActionRequest,registerActionAdapter } from "../src/workflows/action-executor";
import type { AgentEvent,AgentWorkspace } from "../src/agent-backend";
const live=process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("OpenShell Go/Ax proposal returns a durable approval and terminal recovery reuses it",async()=>{
 const root=process.env.HARNESS_STATE!;
 if(!root || process.env.NEKO_PG_PORT!=="18119") throw Error("isolated M3 environment required");
 const orgId=`approval-${randomUUID()}`,threadId=randomUUID(),runId=randomUUID(),kind="harness_effect_fixture";
 const orgRoot=join(root,"approval-workspace");
 const workspace:AgentWorkspace={orgRoot,skillsRoot:join(orgRoot,"skills"),memoryRoot:join(orgRoot,"memory"),knowledgeRoot:join(orgRoot,"knowledge"),uploadsRoot:join(orgRoot,"uploads"),runsRoot:join(orgRoot,"runs"),threadUploadsRoot:join(orgRoot,"uploads",threadId),runRoot:join(orgRoot,"runs",runId),artifactRoot:join(orgRoot,"runs",runId,"artifacts"),binRoot:join(orgRoot,"runs",runId,"bin")};
 for(const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
 const hermesHome=join(root,"provider-config");await mkdir(hermesHome,{recursive:true});
 await writeFile(join(hermesHome,"config.yaml"),'model:\n  provider: custom\n  default: harness-fixture\n  base_url: http://host.docker.internal:18118/v1\n');
 await db().insert(organization).values({id:orgId,name:"Approval sandbox"});
 await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Approval"});
 await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
 await db().insert(pack_action_definition).values({org_id:orgId,kind,readiness:"ready",definition_hash:"fixture",definition:{kind,inputSchema:{type:"object",properties:{value:{type:"integer"}},required:["value"],additionalProperties:false}}});
 await db().insert(action_policy).values({org_id:orgId,name:"Fixture approval",mode:"approval_required",applies_to_kinds:[kind],applies_to_scopes:["external"]});
 const broker=await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
 try {
  await fetch("http://127.0.0.1:18118/control",{method:"POST",body:JSON.stringify({proposal:true})});
  const core=makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,brokerRelease:broker.release,onLog:()=>{}});
  const events:AgentEvent[]=[];
  const input={backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId,workspace,prompt:"Request approval to set the fixture value to 42. Do not execute it.",userMessage:"Prepare the change",pluginActions:[],packActions:[{kind,description:"Set the fixture value",scope:"external" as const,default_mode:"ask" as const}],emit:async(event:AgentEvent)=>{events.push(event);}};
  const result=await core(input);expect(result.status,JSON.stringify(result)).toBe("completed");
  expect(JSON.stringify(result.backendState)).toContain('"kind":"approval"');
  const card=events.find(event=>event.type==="action_request_emit");expect(card).toBeDefined();
  if(card?.type!=="action_request_emit") throw Error("missing approval");
  expect((await getActionRequest(orgId,card.action_request_id))?.status).toBe("pending_approval");
  const counts=await (await fetch("http://127.0.0.1:18118/control")).json();
  events.length=0;await core(input);
  expect(events.some(event=>event.type==="action_request_emit" && event.action_request_id===card.action_request_id)).toBe(true);
  expect(await (await fetch("http://127.0.0.1:18118/control")).json()).toEqual(counts);
  expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1",[orgId])).rows[0].n).toBe(1);
  await approveActionRequest({orgId,id:card.action_request_id,approverUserId:null,approver:{userId:null,role:"admin"}});
  let effects=0;registerActionAdapter(kind,async()=>{effects++;return {result:{value:42}};});
  expect((await executeApprovedActionRequest(orgId,card.action_request_id)).ok).toBe(true);
  expect((await executeApprovedActionRequest(orgId,card.action_request_id)).ok).toBe(true);expect(effects).toBe(1);
  // The same Go/Ax/OpenShell proposal path must admit a host-bound plugin
  // action at its internal policy scope, without a pack action descriptor.
  const pluginRun=randomUUID();
  await db().insert(work_run).values({id:pluginRun,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  await db().insert(action_policy).values({org_id:orgId,name:"Fixture internal approval",mode:"approval_required",applies_to_kinds:[kind],applies_to_scopes:["internal"]});
  const pluginWorkspace=Object.fromEntries(Object.entries(workspace).map(([key,value])=>[key,value.replaceAll(runId,pluginRun)])) as AgentWorkspace;
  for(const dir of Object.values(pluginWorkspace)) await mkdir(dir,{recursive:true});
  await mkdir(join(pluginWorkspace.skillsRoot,"records"),{recursive:true});
  await writeFile(join(pluginWorkspace.skillsRoot,"records","SKILL.md"),"# Records\nUse the admitted governed action for this synthetic record change.\n");
  await fetch("http://127.0.0.1:18118/control",{method:"POST",body:JSON.stringify({proposal:true})});
  const pluginEvents:AgentEvent[]=[];
  const pluginResult=await core({...input,runId:pluginRun,workspace:pluginWorkspace,dataSurface:"records" as const,
    pluginActions:[{kind,description:"Set the fixture value through a plugin",scope:"internal",default_mode:"ask"}],
    packActions:[],emit:async(event:AgentEvent)=>{pluginEvents.push(event);}});
  expect(pluginResult.status,JSON.stringify(pluginResult)).toBe("completed");
  const pluginCard=pluginEvents.find(event=>event.type==="action_request_emit");
  if(pluginCard?.type!=="action_request_emit") throw Error("missing plugin approval");
  const pluginRequest=await getActionRequest(orgId,pluginCard.action_request_id);
  expect(pluginRequest?.status).toBe("pending_approval");
  expect(pluginRequest?.scope).toBe("internal");
  await approveActionRequest({orgId,id:pluginCard.action_request_id,approverUserId:null,approver:{userId:null,role:"admin"}});
  expect((await executeApprovedActionRequest(orgId,pluginCard.action_request_id)).ok).toBe(true);
  expect(effects).toBe(2);
  // Cancellation must cross the sandbox boundary even when the proxy's idle
  // stream does not react to the Go client's socket close.
  const cancelledRun=randomUUID();
  await db().insert(work_run).values({id:cancelledRun,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  const cancelWorkspace=Object.fromEntries(Object.entries(workspace).map(([key,value])=>[key,value.replaceAll(runId,cancelledRun)])) as AgentWorkspace;
  for(const dir of Object.values(cancelWorkspace)) await mkdir(dir,{recursive:true});
  await fetch("http://127.0.0.1:18118/control",{method:"POST",body:JSON.stringify({delay:30})});
  const abort=new AbortController();
  const pending=core({...input,runId:cancelledRun,workspace:cancelWorkspace,signal:abort.signal}).catch(error=>({status:"failed",error:String(error)}));
  let started=false;
  for(let n=0;n<200;n++) {
   const count=await (await fetch("http://127.0.0.1:18118/control")).json();
   if(count["harness-fixture"]>0) {started=true;break;}
   await new Promise(resolve=>setTimeout(resolve,100));
  }
  abort.abort();expect(started).toBe(true);
  const cancelled=await pending;expect(cancelled.status).not.toBe("completed");
  let closed=false;
  for(let n=0;n<100;n++) {
   const count=await (await fetch("http://127.0.0.1:18118/control")).json();
   if(count["cancelled:harness-fixture"]>0) {closed=true;break;}
   await new Promise(resolve=>setTimeout(resolve,100));
  }
  expect(closed,"sandbox teardown must close the upstream model request").toBe(true);

 } finally {
  await fetch("http://127.0.0.1:18118/control",{method:"POST",body:"{}"});
  await broker.close();await db().delete(organization).where(eq(organization.id,orgId));await pool().end();
 }
},180000);

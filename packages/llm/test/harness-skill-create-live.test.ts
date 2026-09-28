import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, organization, pool, work_run, work_thread } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { inProcessControlPlane } from "../src/work/control-plane";
import { startAgentBroker } from "../src/work/broker";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import { harnessSkillVersion } from "../src/work/harness-skill-update";

const live=process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("creates one host-owned org skill through connected Ax/OpenShell dispatch",async()=>{
  if(process.env.NEKO_PG_PORT!=="18119" || !process.env.HARNESS_STATE) throw Error("isolated M3 environment required");
  const orgId=`harness-skill-${randomUUID()}`, threadId=randomUUID(), runId=randomUUID();
  const root=join(process.env.HARNESS_STATE,"skills",runId);
  const workspace:AgentWorkspace={orgRoot:root,skillsRoot:join(root,"skills"),memoryRoot:join(root,"memory"),
    knowledgeRoot:join(root,"knowledge"),uploadsRoot:join(root,"uploads"),runsRoot:join(root,"runs"),
    threadUploadsRoot:join(root,"uploads",threadId),runRoot:join(root,"runs",runId),
    artifactRoot:join(root,"runs",runId,"artifacts"),binRoot:join(root,"runs",runId,"bin")};
  for(const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
  const hermesHome=join(root,"provider-config");
  await mkdir(hermesHome,{recursive:true});
  await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-skill-create-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(organization).values({id:orgId,name:"Harness skill fixture"});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Skill"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"admin"});
  const broker=await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
  try{
    const noGrant=broker.tokenFor({profile:"harness-read-only",kind:"work",orgId,runId:randomUUID(),threadId});
    const denied=await fetch(`http://127.0.0.1:${broker.port}/v1/harness/skill/create`,{method:"POST",
      headers:{authorization:`Bearer ${noGrant}`,"content-type":"application/json"},
      body:JSON.stringify({operationId:1,binding:"a".repeat(64),instruction:JSON.stringify({name:"forged",description:"forged",body:"forged"})})});
    expect(denied.status).toBe(403);
    const runCore=makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",
      agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],
      hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,
      brokerTokenFor:broker.tokenFor,brokerRelease:broker.release,onLog:()=>{}});
    const events:Array<{type:string;name?:string}>=[];
    const result=await runCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId,workspace,
      prompt:"Create the requested reusable skill through the host tool.",userMessage:"Create a lead CSV review skill.",
      dataSurface:"customer",pluginActions:[],emit:async event=>{events.push(event);}});
    expect(result.status,JSON.stringify(result)).toBe("completed");
    expect(events).toContainEqual(expect.objectContaining({type:"tool_start",name:"skill_create"}));
    const path=join(workspace.skillsRoot,"fixture-lead-review");
    expect(await readFile(join(path,"SKILL.md"),"utf8")).toContain("Read the selected CSV");
    expect(await readFile(join(path,"scripts/check.py"),"utf8")).toContain("skill fixture");
    expect(await readdir(workspace.skillsRoot)).toEqual(["fixture-lead-review"]);
    const [receipt]=(await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,runId])).rows;
    expect(receipt.request).toMatchObject({tool:"skill_create",binding:expect.stringMatching(/^[a-f0-9]{64}$/)});
    expect(receipt.result).toEqual({ok:true,name:"fixture-lead-review",skillFile:"fixture-lead-review/SKILL.md"});
    const snapshot=JSON.parse(await readFile(join(workspace.runRoot,".harness",`${createHash("sha256").update(runId).digest("hex")}.json`),"utf8"));
    expect(snapshot.operations).toHaveLength(1);
    expect(snapshot.operations[0]).toMatchObject({tool:"skill_create",binding:receipt.request.binding,finished:true,result:receipt.result});
    const readRunId=randomUUID();
    const readWorkspace:AgentWorkspace={...workspace,runRoot:join(workspace.runsRoot,readRunId),
      artifactRoot:join(workspace.runsRoot,readRunId,"artifacts"),binRoot:join(workspace.runsRoot,readRunId,"bin")};
    for(const dir of [readWorkspace.runRoot,readWorkspace.artifactRoot,readWorkspace.binRoot]) await mkdir(dir,{recursive:true});
    await db().insert(work_run).values({id:readRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"admin"});
    await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-skill-read-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const readEvents:Array<{type:string;name?:string}>=[];
    const readResult=await runCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:readRunId,
      workspace:readWorkspace,prompt:"Read the installed skill before answering.",userMessage:"What does the skill say?",
      dataSurface:"customer",pluginActions:[],emit:async event=>{readEvents.push(event);}});
    expect(readResult.status,JSON.stringify(readResult)).toBe("completed");
    expect(readEvents).toContainEqual(expect.objectContaining({type:"tool_start",name:"skill_read"}));
    expect((await pool().query("SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,readRunId])).rows[0].n).toBe(0);
    const oldVersion=await harnessSkillVersion(workspace.skillsRoot,"fixture-lead-review");
    const updateRunId=randomUUID();
    const updateWorkspace:AgentWorkspace={...workspace,runRoot:join(workspace.runsRoot,updateRunId),
      artifactRoot:join(workspace.runsRoot,updateRunId,"artifacts"),binRoot:join(workspace.runsRoot,updateRunId,"bin")};
    for(const dir of [updateWorkspace.runRoot,updateWorkspace.artifactRoot,updateWorkspace.binRoot]) await mkdir(dir,{recursive:true});
    await db().insert(work_run).values({id:updateRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"admin"});
    await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-skill-update-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const updateEvents:Array<{type:string;name?:string}>=[];
    const updateResult=await runCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:updateRunId,
      workspace:updateWorkspace,prompt:"Update the installed skill after inspecting its version.",
      userMessage:"Update the lead review skill to verify owners.",dataSurface:"customer",pluginActions:[],
      emit:async event=>{updateEvents.push(event);}});
    expect(updateResult.status,JSON.stringify(updateResult)).toBe("completed");
    expect(updateEvents).toContainEqual(expect.objectContaining({type:"tool_start",name:"skill_inspect"}));
    expect(updateEvents).toContainEqual(expect.objectContaining({type:"tool_start",name:"skill_update"}));
    expect(await harnessSkillVersion(workspace.skillsRoot,"fixture-lead-review")).not.toBe(oldVersion);
    expect(await readFile(join(path,"SKILL.md"),"utf8")).toContain("Verify the lead owner");
    expect(await readdir(join(path,"scripts"))).toEqual(["new_check.py"]);
    const [updateReceipt]=(await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,updateRunId])).rows;
    expect(updateReceipt.request).toMatchObject({tool:"skill_update"});
    expect(updateReceipt.result).toMatchObject({ok:true,name:"fixture-lead-review",version:expect.stringMatching(/^[a-f0-9]{64}$/)});
    const updateSnapshot=JSON.parse(await readFile(join(updateWorkspace.runRoot,".harness",`${createHash("sha256").update(updateRunId).digest("hex")}.json`),"utf8"));
    expect(updateSnapshot.operations.map((operation:{tool:string})=>operation.tool)).toEqual(["skill_inspect","skill_update"]);
    const finalRunId=randomUUID();
    const finalWorkspace:AgentWorkspace={...workspace,runRoot:join(workspace.runsRoot,finalRunId),
      artifactRoot:join(workspace.runsRoot,finalRunId,"artifacts"),binRoot:join(workspace.runsRoot,finalRunId,"bin")};
    for(const dir of [finalWorkspace.runRoot,finalWorkspace.artifactRoot,finalWorkspace.binRoot]) await mkdir(dir,{recursive:true});
    await db().insert(work_run).values({id:finalRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"admin"});
    await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-skill-read-updated-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const finalResult=await runCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:finalRunId,
      workspace:finalWorkspace,prompt:"Read the updated installed skill.",userMessage:"What does it say now?",
      dataSurface:"customer",pluginActions:[],emit:async()=>{}});
    expect(finalResult.status,JSON.stringify(finalResult)).toBe("completed");
  } finally { await broker.close(); await deleteTestOrg(orgId); }
},120_000);

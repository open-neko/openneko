import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, it } from "vitest";
import { db, organization, data_source, work_thread, work_run, eq, pool } from "@neko/db";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { parseHarnessRouting } from "../src/work/harness-routing";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import type { AgentWorkspace } from "../src/agent-backend";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("launches trusted Ax routes through distinct OpenShell providers", async () => {
  const root = process.env.HARNESS_STATE!;
  if (!root || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 environment required");
  const orgId=`harness-m6-${randomUUID()}`, threadId=randomUUID(), runId=randomUUID();
  const orgRoot=join(root,"m6-worker-workspace");
  const workspace:AgentWorkspace={orgRoot,skillsRoot:join(orgRoot,"skills"),memoryRoot:join(orgRoot,"memory"),
    knowledgeRoot:join(orgRoot,"knowledge"),uploadsRoot:join(orgRoot,"uploads"),runsRoot:join(orgRoot,"runs"),
    threadUploadsRoot:join(orgRoot,"uploads",threadId),runRoot:join(orgRoot,"runs",runId),
    artifactRoot:join(orgRoot,"runs",runId,"artifacts"),binRoot:join(orgRoot,"runs",runId,"bin")};
  for(const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
  const hermesHome=join(root,"provider-config");
  await mkdir(hermesHome,{recursive:true});
  await writeFile(join(hermesHome,"config.yaml"),
    "model:\n  provider: custom\n  default: harness-route-context\n  base_url: http://host.docker.internal:18118/route/context/v1\n");
  await db().insert(organization).values({id:orgId,name:"Harness M6 routing acceptance"});
  await db().insert(data_source).values({org_id:orgId,graphql_url:"http://127.0.0.1:18117/api/v1/graphql",kind:"graphjin",auth_mode:"none",is_default:true});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"M6 routes"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  const broker=await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
  try {
    const routes=parseHarnessRouting(JSON.stringify({context:"context",executor:"executor",responder:"responder",routes:
      ["context","executor","responder"].map(stage=>({key:stage,model:`harness-route-${stage}`,
        url:`http://host.docker.internal:18118/route/${stage}/v1`,provider:`harness-m6-${stage}`,
        credential_env:`HARNESS_${stage.toUpperCase()}_KEY`,api_key_env:`HARNESS_MODEL_${stage.toUpperCase()}_KEY`}))}));
    const core=makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",
      agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],
      hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,
      brokerRelease:broker.release,harnessRouting:routes,onLog:()=>{}});
    const result=await core({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId,workspace,
      prompt:"Answer the routing check.",userMessage:"Answer the routing check.",pluginActions:[],emit:async()=>{}});
    expect(result.status,JSON.stringify(result)).toBe("completed");
    expect(result.finalText).toContain("ROUTED-OK");
    const counts=await (await fetch("http://127.0.0.1:18118/control")).json() as Record<string,number>;
    for(const stage of ["context","executor","responder"]) expect(counts[`harness-route-${stage}`]).toBe(1);

    await fetch("http://127.0.0.1:18118/control",{method:"POST",body:"{}"});
    const skillDir=join(workspace.skillsRoot,"reference-check");
    await mkdir(skillDir,{recursive:true});
    await writeFile(join(skillDir,"SKILL.md"),
      "---\nname: reference-check\ndescription: Verify a seeded reference using GraphJin\n---\nUse the governed lookup capability and report its evidence.\n");
    const lookupRunId=randomUUID();
    const lookupWorkspace={...workspace,runRoot:join(workspace.runsRoot,lookupRunId),
      artifactRoot:join(workspace.runsRoot,lookupRunId,"artifacts"),binRoot:join(workspace.runsRoot,lookupRunId,"bin")};
    for(const dir of [lookupWorkspace.runRoot,lookupWorkspace.artifactRoot,lookupWorkspace.binRoot]) await mkdir(dir,{recursive:true});
    await db().insert(work_run).values({id:lookupRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
    const routeStages=["skill","context-lookup","executor-lookup","responder-lookup"];
    const lookupRoutes=parseHarnessRouting(JSON.stringify({skill:"skill",context:"context",executor:"executor",responder:"responder",
      routes:routeStages.map((stage,index)=>({key:["skill","context","executor","responder"][index],
        model:`harness-route-${stage}`,url:`http://host.docker.internal:18118/route/${stage}/v1`,
        provider:`harness-m6-${stage}`,credential_env:`HARNESS_${stage.toUpperCase().replaceAll("-","_")}_KEY`,
        api_key_env:`HARNESS_MODEL_${stage.toUpperCase().replaceAll("-","_")}_KEY`}))}));
    const lookupCore=makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",
      agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],
      hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,
      brokerRelease:broker.release,harnessRouting:lookupRoutes,onLog:()=>{}});
    const lookupResult=await lookupCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:lookupRunId,
      workspace:lookupWorkspace,prompt:"Find the seeded reference using the appropriate staged skill and GraphJin.",
      userMessage:"Verify the seeded reference and report it with evidence.",allowedSkills:["reference-check"],
      pluginActions:[],emit:async()=>{}});
    expect(lookupResult.status,JSON.stringify(lookupResult)).toBe("completed");
    expect(lookupResult.finalText).toContain("REF-42");
    const routedCalls=await (await fetch("http://127.0.0.1:18118/control")).json() as Record<string,number>;
    for(const stage of routeStages) expect(routedCalls[`harness-route-${stage}`],stage).toBeGreaterThan(0);
    expect(routedCalls["graphjin-fixture"]).toBeGreaterThan(0);
    const operations=(await pool().query("select result from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
      [orgId,lookupRunId])).rows;
    expect(operations).toHaveLength(1);
    expect(operations[0].result.agentStatus.model).toBe("graphjin-fixture");
    const snapshot=JSON.parse(await readFile(join(lookupWorkspace.runRoot,".harness",
      `${createHash("sha256").update(lookupRunId).digest("hex")}.json`),"utf8"));
    const skill=snapshot.events.filter((event:{type:string})=>event.type==="skill.selected");
    expect(skill).toMatchObject([{name:"reference-check",origin:"semantic"}]);
    const calls=snapshot.events.filter((event:{type:string})=>event.type==="model.request.started");
    expect(calls.map((event:{name:string})=>event.name)).toContain("harness-route-skill");
    expect(calls.map((event:{name:string})=>event.name)).toContain("harness-route-context-lookup");
    expect(calls.map((event:{name:string})=>event.name)).toContain("harness-route-executor-lookup");
    expect(calls.map((event:{name:string})=>event.name)).toContain("harness-route-responder-lookup");
    const remote=snapshot.events.filter((event:{type:string;name?:string})=>event.type==="tool.finished"&&event.name==="lookup");
    expect(remote).toHaveLength(1);
    expect(remote[0].remote_usage).toMatchObject({reported:true});
    expect(remote[0].remote_usage.total_tokens).toBeGreaterThan(0);
    const outerTokens=snapshot.events.filter((event:{type:string})=>event.type==="model.request.finished")
      .reduce((sum:number,event:{usage?:{total_tokens?:number}})=>sum+(event.usage?.total_tokens??0),0);
    expect(snapshot.result.usage.total_tokens).toBe(outerTokens);
    broker.release(lookupRunId);
  } finally {
    broker.release(runId);
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},120000);

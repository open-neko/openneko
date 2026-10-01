import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
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
  } finally {
    broker.release(runId);
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},120000);

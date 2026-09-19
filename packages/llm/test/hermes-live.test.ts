import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, pool, organization, work_thread, work_run, eq } from "@neko/db";
import { expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore, closeSandboxPools } from "../src/work/sandbox-launcher";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
live("preserves real Hermes cold and reused warm runs on the qualified gateway", async () => {
  if (!process.env.HARNESS_STATE || process.env.NEKO_PG_PORT !== "18119") throw new Error("isolated M3 environment required");
  const orgId=`hermes-regression-${randomUUID()}`;
  await db().insert(organization).values({id:orgId,name:"Hermes regression"});
  const orgRoot=join(process.env.HARNESS_STATE,"hermes-workspace");
  const home=join(process.env.HARNESS_STATE,"hermes-regression-config");
  await mkdir(home,{recursive:true});
  await writeFile(join(home,"config.yaml"),'model:\n  provider: custom\n  default: hermes-fixture\n  base_url: http://host.docker.internal:18118/v1\n');
  const broker=await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
  const logs:string[]=[];
  try {
    for (const warmPoolSize of [0,1]) {
      const core=makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",agentImage:"harness-openneko:m3",
        modelProvider:"harness-hermes",modelHosts:[{host:"host.docker.internal",port:18118}],hermesHomeHostPath:home,
        keyAliases:[{from:"api_key",to:"OPENAI_API_KEY"}],warmPoolSize,
        brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,brokerRelease:broker.release,onLog:line=>logs.push(line)});
      for(let turn=0;turn<(warmPoolSize?2:1);turn++) {
        const runId=randomUUID(),threadId=randomUUID();
        await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Hermes regression"});
        await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"hermes",actor_role:"service"});
        const runRoot=join(orgRoot,"runs",runId);
        const workspace:AgentWorkspace={orgRoot,skillsRoot:join(orgRoot,"skills"),memoryRoot:join(orgRoot,"memory"),knowledgeRoot:join(orgRoot,"knowledge"),uploadsRoot:join(orgRoot,"uploads"),runsRoot:join(orgRoot,"runs"),threadUploadsRoot:join(orgRoot,"uploads",threadId),runRoot,artifactRoot:join(runRoot,"artifacts"),binRoot:join(runRoot,"bin")};
        for(const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
        const result=await core({backend:makeAgentBackend({id:"hermes"}),orgId,runId,threadId,workspace,prompt:"Reply HERMES-OK. No tools are needed.",sandboxUser:{principalId:"service:hermes-regression",authorizationRevision:"fixture-v1"},pluginActions:[],emit:async()=>{}});
        expect(result.status,JSON.stringify(result)).toBe("completed");
        expect(result.finalText).toContain("HERMES-OK");
      }
    }
    const names=logs.filter(line=>line.startsWith("agent sandbox ready:")).map(line=>line.split(" ")[3]);
    expect(names).toHaveLength(3);
    expect(names[1]).toBe(names[2]);
    expect(names[0]).not.toBe(names[1]);
    expect((await pool().query("SELECT run_id FROM harness_run_journal WHERE org_id=$1",[orgId])).rowCount).toBe(0);
  } finally {
    await closeSandboxPools();
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},180_000);

import { randomUUID } from "node:crypto";
import { db, organization, pool, work_run, work_thread } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { expect, it } from "vitest";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane, type AgentControlPlane } from "../src/work/control-plane";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("memory save binds the actor and never repeats an ambiguous write", async () => {
  if (process.env.NEKO_PG_PORT !== "18119") throw Error("isolated database required");
  const orgId=`memory-write-${randomUUID()}`, threadId=randomUUID(), runId=randomUUID();
  await db().insert(organization).values({id:orgId,name:"Memory write fixture"});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Rules"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint) VALUES ($1,$2,$3)",[orgId,runId,"0".repeat(64)]);
  let saves=0;
  const controlPlane={
    async rememberWorkMemory(input: Parameters<AgentControlPlane["rememberWorkMemory"]>[0]) {
      saves++;
      await inProcessControlPlane.rememberWorkMemory(input);
      throw Error("lost receipt after database commit");
    },
  } as AgentControlPlane;
  const broker=await startAgentBroker({port:0,hostAlias:"127.0.0.1",controlPlane});
  const binding={profile:"harness-read-only" as const,memoryWrite:true,lookupRead:false,kind:"work" as const,orgId,runId,threadId};
  const token=broker.tokenFor(binding);
  const call=async(operationId:number,instruction:Record<string,unknown>,auth=token)=>{
    const response=await fetch(`${broker.url}/v1/harness/memory/save`,{method:"POST",headers:{authorization:`Bearer ${auth}`,"content-type":"application/json"},body:JSON.stringify({operationId,instruction:JSON.stringify(instruction),binding:"a".repeat(64)})});
    return {status:response.status,body:await response.json() as Record<string,unknown>};
  };
  try {
    const text="Never close a lead without a verified owner";
    const input={text,scope:"thread",orgId:"forged-org",runId:"forged-run"};
    expect((await call(1,input)).status).toBe(400);
    const denied=broker.tokenFor({...binding,runId:randomUUID(),memoryWrite:false});
    expect((await call(1,{text},denied)).status).toBe(403);
    expect((await call(1,{text,scope:"thread"})).body).toEqual({error:"Harness operation outcome unknown; automatic dispatch disabled"});
    expect((await call(1,{text,scope:"thread"})).body).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    expect((await call(2,{text:"A second rule must be fenced"})).body).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    expect(saves).toBe(1);
    const memories=(await pool().query("SELECT text,source_run_id,source_thread_id FROM work_memory WHERE org_id=$1",[orgId])).rows;
    expect(memories).toEqual([{text,source_run_id:runId,source_thread_id:threadId}]);
    const pending=(await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,runId])).rows;
    expect(pending).toHaveLength(1);
    expect(pending[0].request).toMatchObject({tool:"memory_save",binding:"a".repeat(64)});
    expect(pending[0].result).toBeNull();
  } finally {
    await broker.close();
    await deleteTestOrg(orgId);
  }
},20_000);

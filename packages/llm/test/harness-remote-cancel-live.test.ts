import { randomUUID } from "node:crypto";
import { db, pool, organization, data_source, work_thread, work_run, eq } from "@neko/db";
import { expect, it } from "vitest";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";

const live=process.env.HARNESS_M3_LIVE==="1" ? it : it.skip;
// Run sequentially: this deliberately delays and resets the shared model fixture.
live("broker cancellation stops the real GraphJin agent's model request",async()=>{
  if (process.env.NEKO_PG_PORT!=="18119") throw Error("isolated M3 database required");
  const orgId=`remote-cancel-${randomUUID()}`,runId=randomUUID(),threadId=randomUUID();
  const control="http://127.0.0.1:18118/control";
  await fetch(control,{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({delay:30})});
  await db().insert(organization).values({id:orgId,name:"Remote cancellation test"});
  await db().insert(data_source).values({org_id:orgId,graphql_url:"http://127.0.0.1:18117/api/v1/graphql",kind:"graphjin",auth_mode:"none",is_default:true});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"Remote cancellation"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  await pool().query("INSERT INTO harness_run_journal (org_id,run_id,fingerprint) VALUES ($1,$2,$3)",[orgId,runId,"0".repeat(64)]);
  const broker=await startAgentBroker({port:0,controlPlane:inProcessControlPlane});
  const token=broker.tokenFor({orgId,runId,threadId,kind:"work"});
  const url=`http://127.0.0.1:${broker.port}/v1/harness/lookup`;
  const options={method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({operationId:1,instruction:"Find the seeded reference"})};
  const abort=new AbortController();
  try {
    const response=fetch(url,{...options,signal:AbortSignal.any([abort.signal,AbortSignal.timeout(15_000)])}).catch(error=>error);
    await expect.poll(async()=>((await (await fetch(control)).json())["graphjin-fixture"] ?? 0),{timeout:8000}).toBe(1);
    abort.abort();
    expect((await response).name).toBe("AbortError");
    await expect.poll(async()=>((await (await fetch(control)).json())["cancelled:graphjin-fixture"] ?? 0),{timeout:5000}).toBe(1);
    expect(await (await fetch(url,options)).json()).toEqual({error:"Harness operation outcome_unknown; automatic dispatch disabled"});
    const row=(await pool().query("SELECT result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,runId])).rows[0];
    expect(row).toEqual({result:null,finished_at:null});
    expect((await (await fetch(control)).json())["graphjin-fixture"]).toBe(1);
  } finally {
    abort.abort();
    await broker.close();
    await fetch(control,{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({delay:0})});
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},25_000);

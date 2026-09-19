import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { db, pool, organization, eq } from "@neko/db";
import { expect, it } from "vitest";
import { admitHarnessLaunch } from "../src/work/harness-launch-journal";
import { withHarnessRunJournal } from "../src/work/harness-run-journal";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
live.each([false, true])("database ownership survives SIGKILL and moves hosts (saved: %s)", async saved => {
  if (process.env.NEKO_PG_PORT !== "18119") throw new Error("isolated M3 database required");
  const orgId = `journal-${randomUUID()}`, runId = randomUUID();
  const root = await mkdtemp(join(tmpdir(), "harness-db-journal-"));
  const scope = {orgId, runId};
  await db().insert(organization).values({id: orgId, name: "Journal crash test"});
  const tsx = createRequire(import.meta.url).resolve("tsx", {paths:[join(process.cwd(),"../../apps/worker")]});
  const module = pathToFileURL(join(process.cwd(),"src/work/harness-run-journal.ts")).href;
  const child = spawn(process.execPath,["--import",tsx,"--input-type=module","-e",`
    import { withHarnessRunJournal } from ${JSON.stringify(module)};
    await withHarnessRunJournal(${JSON.stringify(scope)}, async (signal, journal) => {
      const admission = await journal(${JSON.stringify(join(root,"host-a"))}, {input:"same"}, async()=>{throw Error("unexpected recovery")});
      ${saved ? 'await admission.complete({status:"completed",finalText:"saved evidence"});' : ''}
      process.send("admitted"); await new Promise(()=>{});
    });
  `], {stdio:["ignore","ignore","pipe","ipc"]});
  let stderr = "";
  child.stderr?.on("data", chunk => {stderr=(stderr+chunk).slice(-4096);});
  try {
    await Promise.race([
      once(child,"message",{signal:AbortSignal.timeout(10_000)}),
      once(child,"exit").then(()=>{throw Error(`Journal child exited before admission: ${stderr}`);}),
    ]);
    await expect(withHarnessRunJournal(scope, async()=>"bad")).rejects.toThrow("another host");
    const exit = once(child,"exit"); child.kill("SIGKILL"); await exit;
    await expect.poll(async()=>{
      try {return await withHarnessRunJournal(scope,async()=>"available");} catch {return "locked";}
    }).toBe("available");
    // A rejected legacy fingerprint must not poison a new database admission.
    const legacyRoot = join(root,"legacy");
    const legacyScope = {orgId, runId: randomUUID()};
    await admitHarnessLaunch(legacyRoot,{input:"old-scope"});
    for (let attempt=0; attempt<2; attempt++) {
      await expect(withHarnessRunJournal(legacyScope,(_,journal)=>journal(legacyRoot,{input:"new-scope"},async()=>{
        throw Error("must not adopt evidence under changed scope");
      }))).rejects.toThrow("conflicts");
    }
    expect((await pool().query("SELECT run_id FROM harness_run_journal WHERE org_id=$1 AND run_id=$2",[orgId,legacyScope.runId])).rowCount).toBe(0);
    let reconciled = 0;
    const read = () => withHarnessRunJournal(scope, (_,journal)=>journal(join(root,"host-b"),{input:"same"},async()=>{
      reconciled++; throw Error("outcome unknown; do not replay");
    }));
    if (saved) {
      expect((await read()).result?.finalText).toBe("saved evidence");
      expect(reconciled).toBe(0);
    } else {
      await expect(read()).rejects.toThrow("outcome unknown");
      await expect(read()).rejects.toThrow("outcome unknown");
      expect(reconciled).toBe(2);
      await withHarnessRunJournal(scope, async (_,journal) => {
        const resumed = await journal(join(root,"host-b"),{input:"same"},async()=>"resume");
        expect(resumed.resume).toBe(true);
        expect(resumed.result).toBeUndefined();
        await resumed.complete!({status:"completed",finalText:"continued evidence"});
      });
      expect((await read()).result?.finalText).toBe("continued evidence");
      expect(reconciled).toBe(2);
    }
    await expect(withHarnessRunJournal(scope,(_,journal)=>journal(join(root,"host-c"),{input:"changed"},async()=>{
      throw Error("must not reconcile conflicting input");
    }))).rejects.toThrow("conflicts");
  } finally {
    child.kill("SIGKILL");
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
    await rm(root,{recursive:true,force:true});
  }
}, 20_000);

live("accepted context restores only under unchanged request and authorization", async () => {
  if (process.env.NEKO_PG_PORT !== "18119") throw new Error("isolated M3 database required");
  const orgId = `context-${randomUUID()}`, runId = randomUUID();
  const root = await mkdtemp(join(tmpdir(), "harness-context-"));
  const scope = {orgId, runId};
  const original = {prompt:"accepted context", userMessage:"original request", principal:"user-a", policy:"read-only"};
  const result = {status:"completed" as const, finalText:"saved evidence"};
  let restored = "";
  const read = (identity: typeof original) => withHarnessRunJournal(scope, (_,journal) =>
    journal(join(root,"host-b"), identity, async()=>{throw Error("must not execute");}, prompt=>{restored=prompt;}));
  await db().insert(organization).values({id:orgId,name:"Accepted context test"});
  try {
    await withHarnessRunJournal(scope, async (_,journal) => {
      const admission = await journal(join(root,"host-a"),original,async()=>{throw Error("unexpected recovery");},()=>{});
      await admission.complete!(result);
    });
    expect((await read({...original,prompt:"new dynamic context"})).result).toEqual(result);
    expect(restored).toBe(original.prompt);
    for (const identity of [
      {...original,userMessage:"different request"}, {...original,userMessage:""},
      {...original,principal:"user-b"}, {...original,policy:"revoked"},
    ]) {
      restored="";
      await expect(read(identity)).rejects.toThrow("conflicts");
      expect(restored).toBe("");
    }
    // Tampering with persisted context cannot bypass its original fingerprint.
    await pool().query("UPDATE harness_run_journal SET accepted_context=jsonb_set(accepted_context,'{prompt}',to_jsonb('tampered'::text)) WHERE org_id=$1 AND run_id=$2",[orgId,runId]);
    await expect(read(original)).rejects.toThrow("conflicts");
    expect(restored).toBe("");
    // Without a separate user message, the whole prompt remains immutable.
    const strictScope={orgId,runId:randomUUID()};
    const strict = (prompt:string) => withHarnessRunJournal(strictScope,(_,journal)=>journal(join(root,"strict"),{prompt},async()=>result,()=>{}));
    await strict("accepted");
    await expect(strict("changed")).rejects.toThrow("conflicts");
  } finally {
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
    await rm(root,{recursive:true,force:true});
  }
},20_000);

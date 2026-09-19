import { spawn, execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile, chmod } from "node:fs/promises";
import { join } from "node:path";
import { once } from "node:events";
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
import { withHarnessRunJournal } from "../src/work/harness-run-journal";
import { expect, it } from "vitest";
import { db, pool, organization, data_source, work_thread, work_run, eq } from "@neko/db";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
// Explicit opt-in, isolated DB/gateway/model/GraphJin deployment required.
const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
live("runs Harness through the real launcher, broker and GraphJin and replays its checkpoint", async () => {
    const root = process.env.HARNESS_STATE!;
    if (!root || process.env.NEKO_PG_PORT !== "18119")
        throw new Error("isolated M3 environment required");
    const orgId = `harness-m3-${randomUUID()}`, threadId = randomUUID(), runId = randomUUID();
    const orgRoot = join(root, "workspace");
    const workspace: AgentWorkspace = { orgRoot, skillsRoot: join(orgRoot, "skills"), memoryRoot: join(orgRoot, "memory"), knowledgeRoot: join(orgRoot, "knowledge"), uploadsRoot: join(orgRoot, "uploads"), runsRoot: join(orgRoot, "runs"), threadUploadsRoot: join(orgRoot, "uploads", threadId), runRoot: join(orgRoot, "runs", runId), artifactRoot: join(orgRoot, "runs", runId, "artifacts"), binRoot: join(orgRoot, "runs", runId, "bin") };
    for (const dir of Object.values(workspace))
        await mkdir(dir, { recursive: true });
    const hermesHome = join(root, "provider-config");
    await mkdir(hermesHome, { recursive: true });
    await writeFile(join(hermesHome, "config.yaml"), 'model:\n  provider: custom\n  default: harness-fixture\n  base_url: http://host.docker.internal:18118/v1\n');
    await db().insert(organization).values({ id: orgId, name: "Harness M3 acceptance" });
    await db().insert(data_source).values({ org_id: orgId, graphql_url: "http://127.0.0.1:18117/api/v1/graphql", kind: "graphjin", auth_mode: "none", is_default: true });
    await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "M3" });
    await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    const broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane: inProcessControlPlane });
    try {
        const url = `http://127.0.0.1:${broker.port}/v1/graphjin/agent`;
        const unauthenticated = await fetch(url, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ instruction: "Find reference" }) });
        expect(unauthenticated.status).toBe(401);
        const token = broker.tokenFor({ orgId, runId, threadId, kind: "work" });
        const forged = await fetch(url, { method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${token}` }, body: JSON.stringify({ instruction: "Find reference", dataSourceId: randomUUID(), orgId: "forged-org", actor: { role: "admin" } }) });
        const denied = await forged.json();
        expect(denied.denied || denied.error, JSON.stringify(denied)).toBeTruthy();
        broker.release(runId);
        const restricted=broker.tokenFor({orgId,runId,threadId,kind:"work",profile:"harness-read-only"});
        for(const route of ["/v1/action/request","/v1/action/enqueue","/v1/memory/remember","/v1/events","/v1/graphjin/agent","/v1/graphjin/tools/call"]) {
          const response=await fetch(`http://127.0.0.1:${broker.port}${route}`,{method:"POST",headers:{authorization:`Bearer ${restricted}`,"content-type":"application/json"},body:JSON.stringify({profile:"legacy",status:"approved",scope:"external",kind:"fixture",summary:"must not execute",events:[]})});
          expect(response.status,route).toBe(403);
        }
        expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1",[orgId])).rows[0].n).toBe(0);
        broker.release(runId);
        // Lose the first checkpoint transfer after the real model/tool execution.
        // This leaves only the retained sandbox, with no host receipt/checkpoint.
        const cli = join(root, "recovery-cli");
        const fault = join(root, "recovery-download-fault");
        await writeFile(fault, "1");
        await writeFile(cli, `#!/bin/sh
if [ -f '${fault}' ]; then
  for arg in "$@"; do
    if [ "$arg" = download ]; then rm '${fault}'; exit 71; fi
  done
fi
exec '${process.env.HARNESS_M3_CLI!}' "$@"
`);
        await chmod(cli, 0o700);
        const runCore = makeSandboxRunCore({ cli, gatewayName: "harness-m2", agentImage: "harness-openneko:m3", modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }], hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url, brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => { } });
        const events: AgentEvent[] = [];
        const input = { backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace, prompt: `Find the seeded reference using lookup. Report the returned reference. Workspace: ${orgRoot}`, userMessage: "Return the seeded reference.", pluginActions: [], emit: async (e: AgentEvent) => { events.push(e); } };
        await expect(runCore(input)).rejects.toThrow();
        await expect(readFile(join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`))).rejects.toThrow();
        const recoveryRoot = join(root, "recovery-host", "workspace");
        const recoveredWorkspace = Object.fromEntries(Object.entries(workspace).map(([key,value]) => [key,value.replace(orgRoot,recoveryRoot)])) as AgentWorkspace;
        const recoveredInput = {...input,workspace:recoveredWorkspace,prompt:input.prompt.replaceAll(orgRoot,recoveryRoot)+"\nRefreshed context on retry."};
        const modelCalls = await (await fetch("http://127.0.0.1:18118/control")).json();
        expect(modelCalls["harness-fixture"]).toBe(3);
        const firstLookup = (await pool().query("SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id",[orgId,runId])).rows;
        expect(modelCalls["graphjin-fixture"] ?? 0, JSON.stringify(firstLookup)).toBeGreaterThan(0);
        const result = await runCore(recoveredInput);
        expect(result.status, JSON.stringify(result)).toBe("completed");
        expect(result.finalText).toContain("REF-42");
        expect(events.some(e => e.type === "tool_start")).toBe(true);
        expect(JSON.stringify(result.backendState)).toContain("REF-42");
        const inspected = JSON.parse(execFileSync(process.env.HARNESS_INSPECT_BIN!, [], {
            env: { HARNESS_STATE_DIR: join(recoveredWorkspace.runRoot, ".harness") },
            input: JSON.stringify({ version: 1, run_id: runId, input_id: runId, prompt: `${input.prompt.replaceAll(orgRoot,"/sandbox/workspace")}\n\nUser request:\n${input.userMessage}` }),
            encoding: "utf8",
        }));
        expect(inspected.outcome).toBe("terminal");
        expect(inspected.operations).toHaveLength(1);
        expect(inspected.result.answer).toContain("REF-42");
        const receipt = (await pool().query("SELECT result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2", [orgId,runId])).rows[0];
        expect(receipt.result.status).toBe("completed");
        const replay = await runCore(recoveredInput);
        expect(replay.status, JSON.stringify(replay)).toBe("completed");
        expect(replay.finalText).toBe(result.finalText);
        await expect(runCore({...recoveredInput,userMessage:"changed accepted input"})).rejects.toThrow("conflicts");
        await expect(runCore({...recoveredInput,allowedSkills:["new-skill"]})).rejects.toThrow("conflicts");
        await expect(runCore({...recoveredInput,sandboxUser:{principalId:"different-user",authorizationRevision:"revoked"}})).rejects.toThrow("conflicts");
        // Recover again from the downloaded checkpoint after loss of the host receipt.
        await pool().query("UPDATE harness_run_journal SET result=NULL WHERE org_id=$1 AND run_id=$2",[orgId,runId]);
        const recovery = await Promise.allSettled([runCore(recoveredInput), runCore(recoveredInput)]);
        expect(recovery.filter(r => r.status === "fulfilled")).toHaveLength(1);
        expect((recovery.find(r => r.status === "fulfilled") as PromiseFulfilledResult<typeof result>).value).toEqual(result);
        // An unresolved operation must never be reissued, even on repeated delivery.
        const snapshot = join(recoveredWorkspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`);
        const original = await readFile(snapshot, "utf8");
        const unknown = JSON.parse(original);
        delete unknown.result;
        unknown.events = unknown.events.slice(0, unknown.events.findIndex((e: {type: string}) => e.type === "tool.started") + 1);
        unknown.operations = [{id: 1, instruction: unknown.operations[0].instruction, finished: false}];
        await pool().query("UPDATE harness_run_journal SET result=NULL WHERE org_id=$1 AND run_id=$2",[orgId,runId]);
        const operation = (await pool().query("SELECT request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2 AND operation_id=1",[orgId,runId])).rows[0];
        await pool().query("UPDATE harness_operation SET result=NULL,finished_at=NULL WHERE org_id=$1 AND run_id=$2",[orgId,runId]);
        await writeFile(snapshot, JSON.stringify(unknown));
        await expect(runCore(recoveredInput)).rejects.toThrow("outcome unknown");
        await expect(runCore(recoveredInput)).rejects.toThrow("outcome unknown");
        await pool().query("UPDATE harness_operation SET result=$3::jsonb,finished_at=$4,request=jsonb_set(request,'{instruction}',to_jsonb('conflicting instruction'::text)) WHERE org_id=$1 AND run_id=$2",[orgId,runId,JSON.stringify(operation.result),operation.finished_at]);
        await expect(runCore(recoveredInput)).rejects.toThrow();
        expect(JSON.parse(await readFile(snapshot,"utf8")).operations[0].finished).toBe(false);
        await pool().query("UPDATE harness_operation SET request=$3::jsonb WHERE org_id=$1 AND run_id=$2",[orgId,runId,JSON.stringify(operation.request)]);
        // A fresh Ax attempt uses the recovered observation without another lookup.
        await fetch("http://127.0.0.1:18118/control", {method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({continue:true})});
        const continued = await runCore(recoveredInput);
        expect(continued.status).toBe("completed");
        expect(continued.finalText).toContain("REF-42");
        const repaired=JSON.parse(await readFile(snapshot,"utf8"));
        expect(repaired.operations[0].finished).toBe(true);
        expect(repaired.operations[0].result).toEqual(operation.result);
        expect(repaired.operations).toHaveLength(1);
        expect(repaired.events.filter((event:{type:string})=>event.type==="tool.finished")).toHaveLength(1);
        expect(repaired.events.filter((event:{type:string})=>event.type==="run.resumed")).toHaveLength(1);
        expect(repaired.events.filter((event:{type:string})=>event.type==="tool.reused")).toHaveLength(1);
        expect(repaired.result.status).toBe("completed");
        const continuedCalls = await (await fetch("http://127.0.0.1:18118/control")).json();
        expect(continuedCalls["harness-fixture"]).toBe(modelCalls["harness-fixture"]+3);
        expect(continuedCalls["graphjin-fixture"]).toBe(modelCalls["graphjin-fixture"]);
        expect(await runCore(recoveredInput)).toEqual(continued);
        // A second host has no original admission, receipt or checkpoint files.
        const otherRoot = join(root, "other-host", "workspace");
        const otherWorkspace = Object.fromEntries(Object.entries(workspace).map(([key,value]) => [key,value.replace(orgRoot,otherRoot)])) as AgentWorkspace;
        expect(await runCore({...input,workspace:otherWorkspace,prompt:input.prompt.replaceAll(orgRoot,otherRoot)})).toEqual(continued);
        await expect(readFile(join(otherWorkspace.runRoot,".harness",`${createHash("sha256").update(runId).digest("hex")}.json`))).rejects.toThrow();
        expect(await (await fetch("http://127.0.0.1:18118/control")).json()).toEqual(continuedCalls);
        // Kill the actual Go process after GraphJin completed, while the responder
        // is waiting. The next delivery must recover solely from the retained box.
        const crashRunId = randomUUID();
        await db().insert(work_run).values({id:crashRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
        const crashWorkspace = Object.fromEntries(Object.entries(workspace).map(([key,value])=>[key,value.replaceAll(runId,crashRunId)])) as AgentWorkspace;
        for (const dir of Object.values(crashWorkspace)) await mkdir(dir,{recursive:true});
        const crashInput = {...input,runId:crashRunId,workspace:crashWorkspace};
        await fetch("http://127.0.0.1:18118/control",{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({pause_responder:true})});
        const interrupted = runCore(crashInput).then(value=>value,error=>error);
        await expect.poll(async()=> (await (await fetch("http://127.0.0.1:18118/control")).json())["harness-fixture"],{timeout:20_000}).toBe(3);
        const crashOperations = (await pool().query("SELECT operation_id,request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id",[orgId,crashRunId])).rows;
        expect(crashOperations).toHaveLength(1);
        expect(crashOperations[0].result).not.toBeNull();
        const beforeKill = await (await fetch("http://127.0.0.1:18118/control")).json();
        const crashName = "h-"+createHash("sha256").update(crashRunId).digest("hex").slice(0,16);
        // Inject the fault from Docker, outside the sandbox process restrictions.
        // Both the exact random run name and isolated network must match.
        const containers = execFileSync("docker",["ps","--filter",`label=openshell.ai/sandbox-name=${crashName}`,"--filter","network=harness-m2","--format","{{.ID}}"],{encoding:"utf8"}).trim().split("\n").filter(Boolean);
        expect(containers).toHaveLength(1);
        execFileSync("docker",["exec","--user","0",containers[0],"/bin/sh","-c",
          'killed=0; for comm in /proc/[0-9]*/comm; do read -r name < "$comm" || continue; case "$name" in harness-opennek|harness-openneko) pid=${comm#/proc/}; pid=${pid%/comm}; kill -KILL "$pid" || exit 1; killed=1;; esac; done; test "$killed" = 1'],{timeout:15_000});
        expect(await interrupted).toBeInstanceOf(Error);
        const crashSnapshot = join(crashWorkspace.runRoot,".harness",createHash("sha256").update(crashRunId).digest("hex")+".json");
        await expect(readFile(crashSnapshot)).rejects.toThrow();
        await fetch("http://127.0.0.1:18118/control",{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({continue:true})});
        const afterCrash = await runCore(crashInput);
        expect(afterCrash.status).toBe("completed");
        expect(afterCrash.finalText).toContain("REF-42");
        const crashCheckpoint = JSON.parse(await readFile(crashSnapshot,"utf8"));
        expect(crashCheckpoint.events.filter((event:{type:string})=>event.type==="run.resumed")).toMatchObject([{attempt:2}]);
        expect(crashCheckpoint.events.filter((event:{type:string})=>event.type==="tool.reused")).toHaveLength(1);
        expect(crashCheckpoint.operations).toHaveLength(1);
        expect((await pool().query("SELECT operation_id,request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2 ORDER BY operation_id",[orgId,crashRunId])).rows).toEqual(crashOperations);
        const afterCrashCalls = await (await fetch("http://127.0.0.1:18118/control")).json();
        expect(afterCrashCalls["harness-fixture"]).toBe(6);
        expect(afterCrashCalls["graphjin-fixture"]).toBe(beforeKill["graphjin-fixture"]);
        expect(await runCore(crashInput)).toEqual(afterCrash);
        const replayCalls = await (await fetch("http://127.0.0.1:18118/control")).json();
        for (const model of ["harness-fixture","graphjin-fixture"]) expect(replayCalls[model]).toBe(afterCrashCalls[model]);


        // Lose the host launcher while the remote Go process still owns its lock.
        // The broker stays alive to isolate host ownership from broker failure.
        const hostRunId = randomUUID();
        await db().insert(work_run).values({id:hostRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
        const hostWorkspace = Object.fromEntries(Object.entries(workspace).map(([key,value])=>[key,value.replaceAll(runId,hostRunId)])) as AgentWorkspace;
        for (const dir of Object.values(hostWorkspace)) await mkdir(dir,{recursive:true});
        const hostInput = {...input,runId:hostRunId,workspace:hostWorkspace};
        const tsx = createRequire(import.meta.url).resolve("tsx",{paths:[join(process.cwd(),"../../apps/worker")]});
        const launcherModule = pathToFileURL(join(process.cwd(),"src/work/sandbox-launcher.ts")).href;
        const backendModule = pathToFileURL(join(process.cwd(),"src/agent-runtime.ts")).href;
        await fetch("http://127.0.0.1:18118/control",{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({pause_responder:true})});
        const child = spawn(process.execPath,["--import",tsx,"--input-type=module","-e",`
          import { makeSandboxRunCore } from ${JSON.stringify(launcherModule)};
          import { makeAgentBackend } from ${JSON.stringify(backendModule)};
          let raw=""; for await (const chunk of process.stdin) raw+=chunk;
          const {options,input,token}=JSON.parse(raw);
          const core=makeSandboxRunCore({...options,brokerTokenFor:()=>token,brokerRelease:()=>{},onLog:()=>{}});
          await core({...input,backend:makeAgentBackend({id:"harness"}),emit:async()=>{}});
          process.exit(0);
        `],{detached:true,stdio:["pipe","ignore","pipe"]});
        let childError="";
        child.stderr.on("data",chunk=>{childError=(childError+chunk).slice(-4096);});
        child.stdin.end(JSON.stringify({
          options:{cli,gatewayName:"harness-m2",agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url},
          input:{...hostInput,backend:undefined,emit:undefined},
          token:broker.tokenFor({orgId,runId:hostRunId,threadId,kind:"work",profile:"harness-read-only"}),
        }));
        try {
          await expect.poll(async()=>{
            if(child.exitCode!==null) throw Error(`Launcher child exited: ${childError}`);
            return (await (await fetch("http://127.0.0.1:18118/control")).json())["harness-fixture"];
          },{timeout:20_000}).toBe(3);
          const callsBeforeHostDeath=await (await fetch("http://127.0.0.1:18118/control")).json();
          const exit=once(child,"exit"); child.kill("SIGKILL"); await exit;
          await expect.poll(async()=>{
            try {return await withHarnessRunJournal({orgId,runId:hostRunId},async()=>"released");}
            catch {return "owned";}
          }).toBe("released");
          const hostName="h-"+createHash("sha256").update(hostRunId).digest("hex").slice(0,16);
          const hostContainers=execFileSync("docker",["ps","--filter",`label=openshell.ai/sandbox-name=${hostName}`,"--filter","network=harness-m2","--format","{{.ID}}"],{encoding:"utf8"}).trim().split("\n").filter(Boolean);
          expect(hostContainers).toHaveLength(1);
          const assertRemoteRunning=()=>execFileSync("docker",["exec","--user","0",hostContainers[0],"/bin/sh","-c",
            'for comm in /proc/[0-9]*/comm; do read -r name < "$comm" || continue; case "$name" in harness-opennek|harness-openneko) exit 0;; esac; done; exit 1'],{timeout:5000});
          assertRemoteRunning();
          // Database ownership is free, but remote execution is not. This must
          // reject without deleting the sandbox or dispatching a second attempt.
          await expect(runCore(hostInput)).rejects.toThrow("outcome unknown");
          assertRemoteRunning();
          const afterRefusal=await (await fetch("http://127.0.0.1:18118/control")).json();
          for(const model of ["harness-fixture","graphjin-fixture"]) expect(afterRefusal[model]).toBe(callsBeforeHostDeath[model]);
          // The original remote responder finishes after its controlled pause.
          // Retry inspection only; admission cannot start another live attempt.
          let adopted: Awaited<ReturnType<typeof runCore>> | undefined;
          await expect.poll(async()=>{
            try {adopted=await runCore(hostInput); return adopted.status;}
            catch(error) {if(!String(error).includes("outcome unknown")) throw error; return "running";}
          },{timeout:45_000,interval:1000}).toBe("completed");
          expect(adopted!.finalText).toContain("REF-42");
          const hostSnapshot=JSON.parse(await readFile(join(hostWorkspace.runRoot,".harness",createHash("sha256").update(hostRunId).digest("hex")+".json"),"utf8"));
          expect(hostSnapshot.events.filter((event:{type:string})=>event.type==="run.resumed")).toHaveLength(0);
          const afterAdoption=await (await fetch("http://127.0.0.1:18118/control")).json();
          for(const model of ["harness-fixture","graphjin-fixture"]) expect(afterAdoption[model]).toBe(callsBeforeHostDeath[model]);
          expect((await pool().query("SELECT operation_id,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,hostRunId])).rows).toMatchObject([{operation_id:1,result:expect.anything()}]);
        } finally {
          // Only this detached test-owned process group, including orphaned CLI.
          try {process.kill(-child.pid!,"SIGKILL");} catch(error) {if((error as NodeJS.ErrnoException).code!=="ESRCH") throw error;}
          broker.release(hostRunId);
        }

    }
    finally {
        await broker.close();
        await db().delete(organization).where(eq(organization.id, orgId));
        await pool().end();
    }
}, 240000);

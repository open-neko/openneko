import {spawn} from "node:child_process";
import {createHash, randomUUID} from "node:crypto";
import {existsSync} from "node:fs";
import {mkdir, readFile, rm, writeFile} from "node:fs/promises";
import {join} from "node:path";
import {expect, it} from "vitest";
import {db, eq, getOrCreateSoloAdmin, getOrgId, pool, work_thread} from "@neko/db";
import {makeAgentBackend} from "../src/agent-runtime";
import {startAgentBroker} from "../src/work/broker";
import {inProcessControlPlane} from "../src/work/control-plane";
import {makeSandboxRunCore} from "../src/work/sandbox-launcher";
import {publishProvisionalRunAnswer} from "../src/work/provisional-events";
import {appendWorkRunEvent, createWorkRun, finishWorkRun, saveAssistantWorkMessage} from "../src/work/store";
import type {AgentWorkspace} from "../src/agent-backend";

const live = process.env.HARNESS_M6_BROWSER_STREAM === "1" ? it : it.skip;

live("renders an OpenShell/Ax draft before the verified answer and omits it on reload", async () => {
  const root = process.env.HARNESS_STATE;
  if (!root || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M6 environment required");
  const orgId = await getOrgId(), threadId = randomUUID();
  const soloAdmin = await getOrCreateSoloAdmin(orgId);
  const orgRoot = join(root,"m6-browser-stream-workspace"), readyPath = join(root,"m6-browser-stream-ready");
  await rm(readyPath,{force:true});
  const workspace: AgentWorkspace = {orgRoot,skillsRoot:join(orgRoot,"skills"),memoryRoot:join(orgRoot,"memory"),
    knowledgeRoot:join(orgRoot,"knowledge"),uploadsRoot:join(orgRoot,"uploads"),runsRoot:join(orgRoot,"runs"),
    threadUploadsRoot:join(orgRoot,"uploads",threadId),runRoot:join(orgRoot,"runs",threadId),
    artifactRoot:join(orgRoot,"runs",threadId,"artifacts"),binRoot:join(orgRoot,"runs",threadId,"bin")};
  for (const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
  const hermesHome = join(root,"browser-stream-provider-config");
  await mkdir(hermesHome,{recursive:true});
  await writeFile(join(hermesHome,"config.yaml"),
    "model:\n  provider: custom\n  default: harness-stream-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"M6 browser streaming",created_by_user_id:soloAdmin.id});
  const run = await createWorkRun(orgId,threadId,"harness",{userId:soloAdmin.id,role:"admin"});
  const runId = run.id;
  const broker = await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
  const browser = spawn("pnpm",["--filter","@neko/web","exec","node","scripts/harness-streaming-browser.mjs",threadId,readyPath],
    {cwd:join(process.cwd(),"../.."),env:process.env,stdio:["ignore","pipe","pipe"]});
  let browserOutput = "";
  browser.stdout.on("data",chunk=>{browserOutput+=String(chunk);});
  browser.stderr.on("data",chunk=>{browserOutput+=String(chunk);});
  const browserExit = new Promise<number>((resolve,reject)=>{
    browser.on("error",reject);
    browser.on("exit",code=>resolve(code??-1));
  });
  try {
    for (let n=0;n<600 && !existsSync(readyPath);n++) {
      if (browser.exitCode !== null) throw Error(`browser exited before SSE ready: ${browserOutput}`);
      await new Promise(resolve=>setTimeout(resolve,100));
    }
    if (!existsSync(readyPath)) throw Error(`browser did not connect to SSE: ${browserOutput}`);
    const core = makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",
      agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],
      hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,
      brokerRelease:broker.release,onLog:()=>{}});
    const result = await core({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId,workspace,
      prompt:"Answer the streaming check.",userMessage:"Answer the streaming check.",pluginActions:[],
      emit:async(event)=>{
        if (event.type === "provisional_answer") await publishProvisionalRunAnswer(orgId,runId,event);
        else if (event.type === "message" && event.role === "assistant") await appendWorkRunEvent({orgId,threadId,runId,event});
      }});
    expect(result.status,JSON.stringify(result)).toBe("completed");
    expect(result.finalText).toBe("STREAM-OK");
    await saveAssistantWorkMessage({orgId,threadId,runId,content:result.finalText!});
    await appendWorkRunEvent({orgId,threadId,runId,event:{type:"done",result:{status:"completed"}}});
    await finishWorkRun(runId,"completed",null);
    expect(await browserExit,browserOutput).toBe(0);
    expect(browserOutput).toContain("M6_RENDERED_BROWSER_STREAMING_PASS");
    const snapshot = JSON.parse(await readFile(join(workspace.runRoot,".harness",
      `${createHash("sha256").update(runId).digest("hex")}.json`),"utf8"));
    expect(snapshot.events.some((event:{type:string})=>event.type==="answer.delta")).toBe(false);
  } finally {
    browser.kill("SIGTERM");
    broker.release(runId);
    await broker.close();
    await db().delete(work_thread).where(eq(work_thread.id,threadId));
    await pool().end();
  }
},180000);

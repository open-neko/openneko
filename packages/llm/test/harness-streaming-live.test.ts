import {createHash, randomUUID} from "node:crypto";
import {mkdir, readFile, writeFile} from "node:fs/promises";
import {join} from "node:path";
import {expect, it} from "vitest";
import {createNotifyClient, db, data_source, eq, organization, pool, work_run, work_thread} from "@neko/db";
import {makeAgentBackend} from "../src/agent-runtime";
import {startAgentBroker} from "../src/work/broker";
import {inProcessControlPlane} from "../src/work/control-plane";
import {makeSandboxRunCore} from "../src/work/sandbox-launcher";
import {PROVISIONAL_RUN_CHANNEL, parseProvisionalNotification, publishProvisionalRunAnswer} from "../src/work/provisional-events";
import type {AgentEvent, AgentWorkspace} from "../src/agent-backend";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("streams a provisional Ax responder through OpenShell before the verified answer", async () => {
  const root = process.env.HARNESS_STATE;
  if (!root || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 environment required");
  const orgId = `harness-stream-${randomUUID()}`, threadId = randomUUID(), runId = randomUUID();
  const orgRoot = join(root, "m6-stream-workspace");
  const workspace: AgentWorkspace = {orgRoot, skillsRoot: join(orgRoot,"skills"), memoryRoot: join(orgRoot,"memory"),
    knowledgeRoot: join(orgRoot,"knowledge"), uploadsRoot: join(orgRoot,"uploads"), runsRoot: join(orgRoot,"runs"),
    threadUploadsRoot: join(orgRoot,"uploads",threadId), runRoot: join(orgRoot,"runs",runId),
    artifactRoot: join(orgRoot,"runs",runId,"artifacts"), binRoot: join(orgRoot,"runs",runId,"bin")};
  for (const dir of Object.values(workspace)) await mkdir(dir,{recursive:true});
  const hermesHome = join(root,"stream-provider-config");
  await mkdir(hermesHome,{recursive:true});
  await writeFile(join(hermesHome,"config.yaml"),
    "model:\n  provider: custom\n  default: harness-stream-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(organization).values({id:orgId,name:"Harness streaming acceptance"});
  await db().insert(data_source).values({org_id:orgId,graphql_url:"http://127.0.0.1:18117/api/v1/graphql",kind:"graphjin",auth_mode:"none",is_default:true});
  await db().insert(work_thread).values({id:threadId,org_id:orgId,title:"M6 streaming"});
  await db().insert(work_run).values({id:runId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
  const broker = await startAgentBroker({port:0,hostAlias:"host.docker.internal",controlPlane:inProcessControlPlane});
  const listener = await createNotifyClient(PROVISIONAL_RUN_CHANNEL);
  const notifications: Array<{text:string; at:number}> = [];
  listener.on((channel,payload)=>{
    const parsed = channel === PROVISIONAL_RUN_CHANNEL ? parseProvisionalNotification(payload) : undefined;
    if (parsed?.orgId === orgId && parsed.runId === runId) notifications.push({text:parsed.event.text,at:Date.now()});
  });
  const priorStream = process.env.OPENNEKO_HARNESS_STREAM_RESPONSES;
  process.env.OPENNEKO_HARNESS_STREAM_RESPONSES = "1";
  try {
    const core = makeSandboxRunCore({cli:process.env.HARNESS_M3_CLI!,gatewayName:"harness-m2",
      agentImage:"harness-openneko:m3",modelProvider:"harness-m3",modelHosts:[{host:"host.docker.internal",port:18118}],
      hermesHomeHostPath:hermesHome,warmPoolSize:0,brokerUrl:broker.url,brokerTokenFor:broker.tokenFor,
      brokerRelease:broker.release,onLog:()=>{}});
    const events: Array<{event:AgentEvent; at:number}> = [];
    const result = await core({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId,workspace,
      prompt:"Answer the streaming check.",userMessage:"Answer the streaming check.",pluginActions:[],
      emit:async(event)=>{
        events.push({event,at:Date.now()});
        if (event.type === "provisional_answer") await publishProvisionalRunAnswer(orgId,runId,event);
      }});
    expect(result.status,JSON.stringify(result)).toBe("completed");
    expect(result.finalText).toBe("STREAM-OK");
    const previews = events.filter(({event})=>event.type==="provisional_answer");
    expect(previews.length).toBeGreaterThan(0);
    expect(previews.map(({event})=>event.type==="provisional_answer"?event.text:"").join("")).toContain("STREAM-");
    const canonical = events.find(({event})=>event.type==="message" && event.role==="assistant");
    expect(canonical?.event).toMatchObject({type:"message",content:"STREAM-OK"});
    expect(canonical!.at-previews[0].at).toBeGreaterThanOrEqual(500);
    expect(notifications.map((item)=>item.text).join("")).toContain("STREAM-");
    expect(canonical!.at-notifications[0].at).toBeGreaterThanOrEqual(500);
    const snapshot = JSON.parse(await readFile(join(workspace.runRoot,".harness",
      `${createHash("sha256").update(runId).digest("hex")}.json`),"utf8"));
    expect(snapshot.events.some((event:{type:string})=>event.type==="answer.delta")).toBe(false);
  } finally {
    if (priorStream === undefined) delete process.env.OPENNEKO_HARNESS_STREAM_RESPONSES;
    else process.env.OPENNEKO_HARNESS_STREAM_RESPONSES = priorStream;
    broker.release(runId);
    await listener.close();
    await broker.close();
    await db().delete(organization).where(eq(organization.id,orgId));
    await pool().end();
  }
},120000);

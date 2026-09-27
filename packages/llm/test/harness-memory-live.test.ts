import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, library_concept, organization, pool, sql, work_thread, work_run, workflow_definition } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { expect, it } from "vitest";
import { inProcessControlPlane, type AgentControlPlane } from "../src/work/control-plane";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { searchLibraryForRun } from "../src/work/library";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("runs journaled Harness memory and library reads through OpenShell, MCP and the broker", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || !process.env.HARNESS_STATE) {
    throw new Error("isolated M3 environment required");
  }
  const orgId = `harness-memory-${randomUUID()}`;
  const threadId = randomUUID();
  const runId = randomUUID();
  const libraryRunId = randomUUID();
  const saveRunId = randomUUID();
  const workflowRunId = randomUUID();
  const childRunId = randomUUID();
  const priorEmbeddingURL = process.env.NEKO_EMBEDDING_URL;
  const orgRoot = join(process.env.HARNESS_STATE, "memory", runId);
  const workspace: AgentWorkspace = {
    orgRoot,
    skillsRoot: join(orgRoot, "skills"),
    memoryRoot: join(orgRoot, "memory"),
    knowledgeRoot: join(orgRoot, "knowledge"),
    uploadsRoot: join(orgRoot, "uploads"),
    runsRoot: join(orgRoot, "runs"),
    threadUploadsRoot: join(orgRoot, "uploads", threadId),
    runRoot: join(orgRoot, "runs", runId),
    artifactRoot: join(orgRoot, "runs", runId, "artifacts"),
    binRoot: join(orgRoot, "runs", runId, "bin"),
  };
  for (const dir of Object.values(workspace)) await mkdir(dir, { recursive: true });
  const hermesHome = join(orgRoot, "provider-config");
  await mkdir(hermesHome, { recursive: true });
  await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-memory-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(organization).values({ id: orgId, name: "Harness memory acceptance" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Memory" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
  await db().insert(workflow_definition).values({org_id:orgId,name:"Fixture workflow",description:"A saved fixture workflow",steps:[{id:"verify",description:"Verify the result"}]});
  const vector = sql`${JSON.stringify([1, ...Array(383).fill(0)])}::vector`;
  await db().insert(library_concept).values({
    org_id: orgId, user_id: null, path: "contracts/example", type: "contract",
    title: "Fixture contract", body: "TERMS-42", status: "stable", embedding: vector,
  });
  let searches = 0;
  let librarySearches = 0;
  let childSearches = 0;
  const controlPlane = {
    async searchWorkMemoryByContext(args: { orgId: string; runId: string; query: string }) {
      expect(args.orgId).toBe(orgId);
      if (args.runId === childRunId) {
        expect(["find policy", "find exception"]).toContain(args.query);
        childSearches++;
        return [{id:args.query === "find policy" ? "memory-1" : "memory-2",text:"Fixture " + args.query}];
      }
      expect(args).toMatchObject({ runId, query: "find policy" });
      searches++;
      return [{ id: "memory-1", text: "Fixture policy" }];
    },
    async searchLibraryForRun(args: { orgId: string; runId: string; query: string }) {
      expect(args).toMatchObject({ orgId, runId: libraryRunId, query: "find contract" });
      librarySearches++;
      return searchLibraryForRun(args);
    },
    rememberWorkMemory: inProcessControlPlane.rememberWorkMemory,
    listWorkflowsWithTriggers: inProcessControlPlane.listWorkflowsWithTriggers,
  } as unknown as AgentControlPlane;
  const broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane });
  try {
    const missingRunToken=broker.tokenFor({profile:"harness-read-only",workflowRead:true,lookupRead:false,kind:"work",orgId,runId:randomUUID(),threadId});
    const missingRun=await fetch(`http://127.0.0.1:${broker.port}/v1/workflow/list`,{method:"POST",headers:{authorization:`Bearer ${missingRunToken}`,"content-type":"application/json"},body:JSON.stringify({limit:5,orgId,runId:workflowRunId})});
    expect(missingRun.status).toBe(200);
    expect((await missingRun.json()).workflows).toEqual([]);
    const noGrantToken=broker.tokenFor({profile:"harness-read-only",lookupRead:false,kind:"work",orgId,runId:randomUUID(),threadId});
    expect((await fetch(`http://127.0.0.1:${broker.port}/v1/workflow/list`,{method:"POST",headers:{authorization:`Bearer ${noGrantToken}`,"content-type":"application/json"},body:"{}"})).status).toBe(403);
    const runCore = makeSandboxRunCore({
      cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3",
      modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }],
      hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url,
      brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => {},
    });
    const events: AgentEvent[] = [];
    const result = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace,
      prompt: "Search saved memory for the policy and report it.", userMessage: "Find the policy.",
      dataSurface: "customer", pluginActions: [], emit: async (event) => { events.push(event); },
    });
    expect(result.status).toBe("completed");
    expect(result.finalText).toContain("memory-1");
    expect(searches).toBe(1);
    expect(events).toContainEqual(expect.objectContaining({ type: "tool_start", name: "mcp_neko_memory_search" }));
    const snapshotPath = join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`);
    const snapshot = JSON.parse(await readFile(snapshotPath, "utf8"));
    expect(snapshot.operations).toHaveLength(1);
    expect(snapshot.operations[0]).toMatchObject({ tool: "mcp_memory_search", finished: true });
    expect(snapshot.operations[0].binding).toMatch(/^[a-f0-9]{64}$/);
    expect(snapshot.events).toContainEqual(expect.objectContaining({ type: "tool.finished", name: "mcp_memory_search", effect: "read" }));

    const libraryWorkspace: AgentWorkspace = {
      ...workspace,
      runRoot: join(workspace.runsRoot, libraryRunId),
      artifactRoot: join(workspace.runsRoot, libraryRunId, "artifacts"),
      binRoot: join(workspace.runsRoot, libraryRunId, "bin"),
    };
    for (const dir of [libraryWorkspace.runRoot, libraryWorkspace.artifactRoot, libraryWorkspace.binRoot]) await mkdir(dir, { recursive: true });
    await db().insert(work_run).values({ id: libraryRunId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    process.env.NEKO_EMBEDDING_URL = "http://127.0.0.1:18118";
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-library-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const libraryEvents: AgentEvent[] = [];
    const libraryResult = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId: libraryRunId, workspace: libraryWorkspace,
      prompt: "Search the document library for the contract and report it.", userMessage: "Find the contract.",
      dataSurface: "customer", pluginActions: [], emit: async (event) => { libraryEvents.push(event); },
    });
    expect(libraryResult.status).toBe("completed");
    expect(libraryResult.finalText).toContain("TERMS-42");
    expect(librarySearches).toBe(1);
    expect(libraryEvents).toContainEqual(expect.objectContaining({ type: "tool_start", name: "mcp_library_search" }));
    const librarySnapshot = JSON.parse(await readFile(join(libraryWorkspace.runRoot, ".harness", `${createHash("sha256").update(libraryRunId).digest("hex")}.json`), "utf8"));
    expect(librarySnapshot.operations).toHaveLength(1);
    expect(librarySnapshot.operations[0]).toMatchObject({ tool: "mcp_library_search", finished: true });
    expect(librarySnapshot.events).toContainEqual(expect.objectContaining({ type: "tool.finished", name: "mcp_library_search", effect: "read" }));

    const saveWorkspace: AgentWorkspace = {
      ...workspace,
      runRoot: join(workspace.runsRoot, saveRunId),
      artifactRoot: join(workspace.runsRoot, saveRunId, "artifacts"),
      binRoot: join(workspace.runsRoot, saveRunId, "bin"),
    };
    for (const dir of [saveWorkspace.runRoot, saveWorkspace.artifactRoot, saveWorkspace.binRoot]) await mkdir(dir, { recursive: true });
    await db().insert(work_run).values({ id: saveRunId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-memory-save-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const saveEvents: AgentEvent[] = [];
    const saveResult = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId: saveRunId, workspace: saveWorkspace,
      prompt: "The operator explicitly asked you to remember this business rule.",
      userMessage: "Remember: Never close a lead without a verified owner.",
      dataSurface: "customer", pluginActions: [], emit: async event => { saveEvents.push(event); },
    });
    expect(saveResult.status).toBe("completed");
    expect(saveEvents).toContainEqual(expect.objectContaining({ type: "tool_start", name: "memory_save" }));
    const saved = (await pool().query("SELECT id,text,source_run_id,source_thread_id,scope FROM work_memory WHERE org_id=$1 AND source_run_id=$2",[orgId,saveRunId])).rows;
    expect(saved).toHaveLength(1);
    expect(saved[0]).toMatchObject({text:"Never close a lead without a verified owner",source_run_id:saveRunId,source_thread_id:threadId,scope:"thread"});
    const hostReceipt=(await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",[orgId,saveRunId])).rows;
    expect(hostReceipt).toHaveLength(1);
    expect(hostReceipt[0].request).toMatchObject({tool:"memory_save",binding:expect.stringMatching(/^[a-f0-9]{64}$/)});
    expect(hostReceipt[0].result).toEqual({ok:true,memoryId:saved[0].id});
    const saveSnapshot = JSON.parse(await readFile(join(saveWorkspace.runRoot, ".harness", `${createHash("sha256").update(saveRunId).digest("hex")}.json`), "utf8"));
    expect(saveSnapshot.operations).toHaveLength(1);
    expect(saveSnapshot.operations[0]).toMatchObject({tool:"memory_save",binding:hostReceipt[0].request.binding,finished:true,result:hostReceipt[0].result});

    const workflowWorkspace: AgentWorkspace = {
      ...workspace,
      runRoot: join(workspace.runsRoot, workflowRunId),
      artifactRoot: join(workspace.runsRoot, workflowRunId, "artifacts"),
      binRoot: join(workspace.runsRoot, workflowRunId, "bin"),
    };
    for (const dir of [workflowWorkspace.runRoot, workflowWorkspace.artifactRoot, workflowWorkspace.binRoot]) await mkdir(dir, { recursive: true });
    await db().insert(work_run).values({id:workflowRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-workflow-list-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const workflowEvents:AgentEvent[]=[];
    const workflowResult=await runCore({
      backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:workflowRunId,workspace:workflowWorkspace,
      prompt:"List the saved workflows.",userMessage:"What workflow did we save?",dataSurface:"customer",pluginActions:[],
      emit:async event=>{workflowEvents.push(event);},
    });
    expect(workflowResult.status).toBe("completed");
    expect(workflowResult.finalText).toContain("Fixture workflow");
    expect(workflowEvents).toContainEqual(expect.objectContaining({type:"tool_start",name:"mcp_neko_workflow_builder_list_workflows"}));
    const workflowSnapshot=JSON.parse(await readFile(join(workflowWorkspace.runRoot,".harness",`${createHash("sha256").update(workflowRunId).digest("hex")}.json`),"utf8"));
    expect(workflowSnapshot.operations).toHaveLength(1);
    expect(workflowSnapshot.operations[0]).toMatchObject({tool:"mcp_neko_workflow_builder_list_workflows",finished:true});

    const childWorkspace: AgentWorkspace = {
      ...workspace,
      runRoot: join(workspace.runsRoot, childRunId),
      artifactRoot: join(workspace.runsRoot, childRunId, "artifacts"),
      binRoot: join(workspace.runsRoot, childRunId, "bin"),
    };
    for (const dir of [childWorkspace.runRoot, childWorkspace.artifactRoot, childWorkspace.binRoot]) await mkdir(dir,{recursive:true});
    await db().insert(work_run).values({id:childRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"service"});
    await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-child-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const childEvents:AgentEvent[]=[];
    const childResult=await runCore({
      backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:childRunId,workspace:childWorkspace,
      prompt:"Investigate two saved memory topics independently.",userMessage:"Find policy and exception.",
      nativeDelegation:"enabled",dataSurface:"customer",pluginActions:[],emit:async event=>{childEvents.push(event);},
    });
    const childSnapshot=JSON.parse(await readFile(join(childWorkspace.runRoot,".harness",`${createHash("sha256").update(childRunId).digest("hex")}.json`),"utf8"));
    expect(childResult).toMatchObject({status:"completed"});
    expect(childResult.finalText).toContain("memory-1");
    expect(childResult.finalText).toContain("memory-2");
    expect(childSearches).toBe(2);
    expect(childEvents.filter(event=>event.type==="tool_start" && event.name==="mcp_neko_memory_search")).toHaveLength(2);
    expect(childSnapshot.operations).toHaveLength(2);
    expect(childSnapshot.operations.map((operation:{tool:string})=>operation.tool)).toEqual(["mcp_memory_search","mcp_memory_search"]);
    expect(childSnapshot.events.filter((event:{type:string})=>event.type==="child.started")).toHaveLength(2);
    expect(childSnapshot.events.filter((event:{type:string})=>event.type==="child.finished")).toHaveLength(2);
    expect(childSnapshot.events.filter((event:{type:string})=>event.type==="model.request.finished")).toHaveLength(9);
  } finally {
    if (priorEmbeddingURL === undefined) delete process.env.NEKO_EMBEDDING_URL;
    else process.env.NEKO_EMBEDDING_URL = priorEmbeddingURL;
    try { await broker.close(); }
    finally { await deleteTestOrg(orgId); }
  }
}, 120_000);

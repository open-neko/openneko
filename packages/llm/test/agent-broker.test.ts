import { describe, expect, it, vi } from "vitest";
import type { AgentControlPlane } from "../src/work/control-plane";
import {
  ensureAgentBroker,
  registerAgentBrokerEventSink,
  routeBrokerEvents,
  shutdownAgentBroker,
  startAgentBroker,
  type RunBinding,
} from "../src/work/broker";

// /v1/events is the only path exercised here and it never touches the control
// plane (it routes to deps.onEvents), so a stub that satisfies the interface
// is enough — the registry/auth behaviour is what's under test.
function stubControlPlane(): AgentControlPlane {
  const unused = () => {
    throw new Error("not exercised by these tests");
  };
  return {
    evaluateActionPolicy: unused as AgentControlPlane["evaluateActionPolicy"],
    createActionRequest: unused as AgentControlPlane["createActionRequest"],
    enqueueActionExecute: unused as AgentControlPlane["enqueueActionExecute"],
    waitForActionExecution:
      unused as AgentControlPlane["waitForActionExecution"],
    rememberWorkMemory: unused as AgentControlPlane["rememberWorkMemory"],
    searchWorkMemoryByContext:
      unused as AgentControlPlane["searchWorkMemoryByContext"],
    searchLibraryForRun: unused as AgentControlPlane["searchLibraryForRun"],
    queryGraphjinRead: unused as AgentControlPlane["queryGraphjinRead"],
    listGraphjinTools: unused as AgentControlPlane["listGraphjinTools"],
    callGraphjinTool: unused as AgentControlPlane["callGraphjinTool"],
    askGraphjinDataAgent:
      unused as AgentControlPlane["askGraphjinDataAgent"],
    listRecordCatalog: unused as AgentControlPlane["listRecordCatalog"],
    findRecords: unused as AgentControlPlane["findRecords"],
    getRecord: unused as AgentControlPlane["getRecord"],
    findRecycledRecords:
      unused as AgentControlPlane["findRecycledRecords"],
    getRecycledRecord: unused as AgentControlPlane["getRecycledRecord"],
    listRecordBlueprints: unused as AgentControlPlane["listRecordBlueprints"],
    saveWorkflowWithTrigger:
      unused as AgentControlPlane["saveWorkflowWithTrigger"],
    emitWorkflowOutput: unused as AgentControlPlane["emitWorkflowOutput"],
    listWorkflowsWithTriggers:
      unused as AgentControlPlane["listWorkflowsWithTriggers"],
    deleteWorkflow: unused as AgentControlPlane["deleteWorkflow"],
    upsertActionPolicyByName:
      unused as AgentControlPlane["upsertActionPolicyByName"],
    listActionPolicies: unused as AgentControlPlane["listActionPolicies"],
    listPlugins: unused as AgentControlPlane["listPlugins"],
    listUsers: unused as AgentControlPlane["listUsers"],
    listChannels: unused as AgentControlPlane["listChannels"],
    listDataSources: unused as AgentControlPlane["listDataSources"],
    listAuditTrail: unused as AgentControlPlane["listAuditTrail"],
    describeSourceGraph: unused as AgentControlPlane["describeSourceGraph"],
    listSourceSecretNames:
      unused as AgentControlPlane["listSourceSecretNames"],
    importOpenApiSpec: unused as AgentControlPlane["importOpenApiSpec"],
    listOpenApiSpecs: unused as AgentControlPlane["listOpenApiSpecs"],
    askSourceConfigAgent:
      unused as AgentControlPlane["askSourceConfigAgent"],
    previewSourceConfigChange:
      unused as AgentControlPlane["previewSourceConfigChange"],
  };
}

function postEvents(
  port: number,
  token: string,
  events: unknown[] = [],
): Promise<Response> {
  return fetch(new URL("/v1/events", `http://127.0.0.1:${port}`), {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify({ events }),
  });
}

describe("startAgentBroker token registry", () => {
  it("binds workflow saves to a Work run without opening legacy save or delete routes", async () => {
    const handle = await startAgentBroker({controlPlane:stubControlPlane(),port:0});
    try {
      const binding:RunBinding={runId:"workflow-save",orgId:"org",threadId:"thread",kind:"work",
        profile:"harness-read-only",workflowWrite:true};
      const token=handle.tokenFor(binding);
      expect(()=>handle.tokenFor({...binding,workflowWrite:false})).toThrow("conflicts");
      expect(()=>handle.tokenFor({...binding,runId:"job",kind:"agent-job"})).toThrow("Invalid broker workflow write grant");
      const post=(path:string,bearer=token,instruction="{}")=>fetch(`http://127.0.0.1:${handle.port}${path}`,{
        method:"POST",headers:{authorization:`Bearer ${bearer}`,"content-type":"application/json"},
        body:JSON.stringify({operationId:1,binding:"b".repeat(64),instruction}),
      });
      expect((await post("/v1/harness/workflow/save")).status).toBe(400);
      const dataTrigger=JSON.stringify({name:"Needs separate trigger recovery",steps:[{id:"s",description:"Check"}],
        expectedVersion:"absent",triggers:{when:{table:"lead",primary_key:["id"]}}});
      expect((await post("/v1/harness/workflow/save",token,dataTrigger)).status).toBe(400);
      expect((await post("/v1/workflow/save")).status).toBe(403);
      expect((await post("/v1/workflow/delete")).status).toBe(403);
      const ungranted=handle.tokenFor({runId:"other",orgId:"org",threadId:"thread",kind:"work",profile:"harness-read-only"});
      expect((await post("/v1/harness/workflow/save",ungranted)).status).toBe(403);
    } finally {await handle.close();}
  });

  it("limits source configuration to admin-read routes on one bound Work run", async () => {
    const describeSourceGraph = vi.fn(async ({orgId, runId}: {orgId: string; runId: string}) => ({orgId, runId, reachable: true}));
    const listSourceSecretNames = vi.fn(async () => ({names: [{name: "SYNTHETIC_DB"}]}));
    const listOpenApiSpecs = vi.fn(async () => ({assets: []}));
    const handle = await startAgentBroker({controlPlane: {...stubControlPlane(), describeSourceGraph,
      listSourceSecretNames, listOpenApiSpecs} as AgentControlPlane, port: 0});
    try {
      const binding: RunBinding = {runId: "source", orgId: "bound-org", threadId: "thread",
        kind: "work", profile: "harness-read-only", sourceConfigRead: true};
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({...binding, sourceConfigRead: false})).toThrow("conflicts");
      expect(() => handle.tokenFor({...binding, runId: "job", kind: "agent-job"})).toThrow("Invalid broker source config read grant");
      const call = (path: string) => fetch(`http://127.0.0.1:${handle.port}${path}`, {
        method: "POST", headers: {authorization: `Bearer ${token}`, "content-type": "application/json"},
        body: JSON.stringify({orgId: "forged-org", runId: "forged-run"}),
      });
      expect(await (await call("/v1/source-graph/describe")).json()).toEqual({orgId: "bound-org", runId: "source", reachable: true});
      expect(await (await call("/v1/source-secrets/names")).json()).toEqual({names: [{name: "SYNTHETIC_DB"}]});
      expect(await (await call("/v1/openapi/list")).json()).toEqual({assets: []});
      expect(describeSourceGraph).toHaveBeenCalledWith({orgId: "bound-org", runId: "source"});
      for (const path of ["/v1/openapi/import", "/v1/source-config/agent", "/v1/source-config/preview"]) {
        expect((await call(path)).status).toBe(403);
      }
    } finally { await handle.close(); }
  });

  it("binds audit reads to one Work run and keeps the actor decision on the host", async () => {
    const listAuditTrail = vi.fn(async ({orgId, runId}: {orgId: string; runId: string}) =>
      ({requests: [{orgId, runId}]}));
    const handle = await startAgentBroker({
      controlPlane: {...stubControlPlane(), listAuditTrail} as AgentControlPlane,
      port: 0,
    });
    try {
      const binding: RunBinding = {runId: "audit", orgId: "bound-org", threadId: "thread",
        kind: "work", profile: "harness-read-only", auditRead: true};
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({...binding, auditRead: false})).toThrow("conflicts");
      expect(() => handle.tokenFor({...binding, runId: "job", kind: "agent-job"})).toThrow("Invalid broker audit read grant");
      const call = (path: string) => fetch(`http://127.0.0.1:${handle.port}${path}`, {
        method: "POST", headers: {authorization: `Bearer ${token}`, "content-type": "application/json"},
        body: JSON.stringify({orgId: "forged-org", runId: "forged-run"}),
      });
      expect(await (await call("/v1/audit/list")).json()).toEqual({requests: [{orgId: "bound-org", runId: "audit"}]});
      expect(listAuditTrail).toHaveBeenCalledWith({orgId: "bound-org", runId: "audit", limit: undefined});
      expect((await call("/v1/source-config/preview")).status).toBe(403);
    } finally { await handle.close(); }
  });

  it("binds management catalogs to a Work read grant without opening writes", async () => {
    const listUsers = vi.fn(async ({orgId}: {orgId: string}) => ({users: [{id: orgId}], groups: []}));
    const controlPlane = {...stubControlPlane(), listUsers} as AgentControlPlane;
    const handle = await startAgentBroker({controlPlane, port: 0});
    try {
      const binding: RunBinding = {runId: "management", orgId: "bound-org", threadId: "thread",
        kind: "work", profile: "harness-read-only", managementRead: true};
      const allowed = handle.tokenFor(binding);
      expect(() => handle.tokenFor({...binding, managementRead: false})).toThrow("conflicts");
      expect(() => handle.tokenFor({...binding, runId: "job", kind: "agent-job"})).toThrow("Invalid broker management read grant");
      const denied = handle.tokenFor({runId: "other", orgId: "bound-org", threadId: "thread",
        kind: "work", profile: "harness-read-only"});
      const call = (token: string, path: string) => fetch(`http://127.0.0.1:${handle.port}${path}`, {
        method: "POST", headers: {authorization: `Bearer ${token}`, "content-type": "application/json"},
        body: JSON.stringify({orgId: "forged-org"}),
      });
      expect((await call(denied, "/v1/users/list")).status).toBe(403);
      expect(await (await call(allowed, "/v1/users/list")).json()).toEqual({users: [{id: "bound-org"}], groups: []});
      expect(listUsers).toHaveBeenCalledWith({orgId: "bound-org"});
      expect((await call(allowed, "/v1/rule/save")).status).toBe(403);
    } finally { await handle.close(); }
  });

  it("admits an isolated process only for its exact Work-run grant", async () => {
    const handle = await startAgentBroker({controlPlane: stubControlPlane(), port: 0});
    try {
      const processRun = {binary: "/tmp/harness-process", binarySha256: "a".repeat(64),
        openshell: "/tmp/openshell", gateway: "fixture", image: "fixture:local",
        orgRoot: "/tmp/org", runRoot: "/tmp/org/runs/work-1",
        artifactRoot: "/tmp/org/runs/work-1/artifacts", uploadsRoot: "/tmp/org/uploads/thread-1"};
      const binding: RunBinding = {runId: "work-1", orgId: "org", threadId: "thread-1",
        kind: "work", profile: "harness-read-only", processRun};
      const allowed = handle.tokenFor(binding);
      expect(() => handle.tokenFor({...binding, processRun: {...processRun, image: "changed"}})).toThrow("conflicts");
      expect(() => handle.tokenFor({...binding, runId: "work-2", kind: "workflow"})).toThrow("Invalid broker isolated process grant");
      expect(() => handle.tokenFor({...binding, runId: "work-3", processRun: {...processRun, artifactRoot: "/tmp/foreign"}})).toThrow("Invalid broker isolated process grant");
      const denied = handle.tokenFor({runId: "work-4", orgId: "org", threadId: "thread-1",
        kind: "work", profile: "harness-read-only"});
      const post = (token: string) => fetch(`http://127.0.0.1:${handle.port}/v1/harness/process/run`, {
        method: "POST", headers: {authorization: `Bearer ${token}`, "content-type": "application/json"},
        body: JSON.stringify({operationId: 1, binding: "b".repeat(64), instruction: '{}'}),
      });
      expect((await post(denied)).status).toBe(403);
      expect((await post(allowed)).status).toBe(400);
    } finally { await handle.close(); }
  });

  it("denies Harness lookup to agent jobs without the server-agent grant", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const binding: RunBinding = { runId: "job-1", orgId: "org", kind: "agent-job", profile: "harness-read-only", lookupRead: false };
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, lookupRead: true })).toThrow("conflicts");
      const response = await fetch(`http://127.0.0.1:${handle.port}/v1/harness/lookup`, {
        method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify({ operationId: 1, instruction: "Find a reference" }),
      });
      expect(response.status).toBe(403);
    } finally { await handle.close(); }
  });

  it("admits workflow output only for an exact workflow-bound Harness token", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const binding: RunBinding = { runId: "work-1", orgId: "org", kind: "workflow", profile: "harness-read-only",
        workflowRunId: "workflow-1", workflowOutput: true };
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, workflowRunId: "workflow-2" })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, workflowOutput: false })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, runId: "work-2", workflowRunId: undefined })).toThrow("Invalid broker workflow output grant");
      expect(() => handle.tokenFor({ ...binding, runId: "work-3", kind: "work" })).toThrow("Invalid broker workflow identity");
      const denied = handle.tokenFor({ runId: "work-4", orgId: "org", kind: "workflow", profile: "harness-read-only" });
      const post = (bearer: string) => fetch(`http://127.0.0.1:${handle.port}/v1/harness/workflow-output/emit`, {
        method: "POST", headers: { authorization: `Bearer ${bearer}`, "content-type": "application/json" },
        body: JSON.stringify({ operationId: 1, instruction: '{}' }),
      });
      expect((await post(denied)).status).toBe(403);
      expect((await post(token)).status).toBe(400);
    } finally { await handle.close(); }
  });

  it("routes run events through the active reducer and restores the worker sink", async () => {
    const outer = vi.fn(async () => {});
    const inner = vi.fn(async () => {});
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), onEvents: routeBrokerEvents, port: 0 });
    const token = handle.tokenFor({ runId: "nested-sink", orgId: "org", kind: "work", profile: "harness-read-only", interactionEvents: true });
    const question = { type: "needs_input", question: "Which day?", questions: [{ id: "q1", question: "Which day?" }] };
    const unregisterOuter = registerAgentBrokerEventSink("nested-sink", outer);
    const unregisterInner = registerAgentBrokerEventSink("nested-sink", inner);
    try {
      expect((await postEvents(handle.port, token, [question])).status).toBe(200);
      expect(inner).toHaveBeenCalledOnce();
      expect(outer).not.toHaveBeenCalled();
      unregisterInner();
      expect((await postEvents(handle.port, token, [question])).status).toBe(200);
      expect(outer).toHaveBeenCalledOnce();
    } finally {
      unregisterInner();
      unregisterOuter();
      await handle.close();
    }
  });

  it("limits Harness interaction events to the bound question and card surface", async () => {
    const onEvents = vi.fn(async () => {});
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), onEvents, port: 0 });
    try {
      const denied = handle.tokenFor({ runId: "no-interaction", orgId: "org", kind: "work", profile: "harness-read-only" });
      expect((await postEvents(handle.port, denied, [{ type: "needs_input", question: "Which day?", questions: [{ id: "q1", question: "Which day?" }] }])).status).toBe(403);
      const binding: RunBinding = { runId: "ask", orgId: "org", kind: "work", profile: "harness-read-only", interactionEvents: true, cardEvents: true };
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, cardEvents: false })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, kind: "workflow" })).toThrow("Invalid broker interaction grant");
      const question = { type: "needs_input", question: "Which day?", questions: [{ id: "q1", question: "Which day?" }] };
      const surface = { type: "surface", messages: [{ version: "v1.0", createSurface: { surfaceId: "clarification-ask", catalogId: "urn:openneko:catalog:work:v2", components: [{ id: "root", component: "Text" }] } }] };
      expect((await postEvents(handle.port, token, [question])).status).toBe(200);
      expect((await postEvents(handle.port, token, [surface])).status).toBe(200);
      for (const events of [[{ type: "done", result: { status: "completed" } }], [{ ...question, questions: [{ id: "q2", question: "Which day?" }] }], [{ type: "surface", messages: [{ version: "v1.0", createSurface: { surfaceId: "x", catalogId: "wrong" } }] }], [question, question]]) {
        expect((await postEvents(handle.port, token, events)).status).toBe(403);
      }
      expect(onEvents).toHaveBeenCalledTimes(2);
      expect(onEvents).toHaveBeenCalledWith(expect.objectContaining({ runId: "ask", orgId: "org" }), [question]);
    } finally { await handle.close(); }
  });

  it("admits only bound Harness library search and strips caller identity", async () => {
    const cp = stubControlPlane();
    cp.searchLibraryForRun = vi.fn(async () => []);
    const handle = await startAgentBroker({ controlPlane: cp, port: 0 });
    const request = (token: string, body: object, path = "/v1/library/search") => fetch(`http://127.0.0.1:${handle.port}${path}`, {
      method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" }, body: JSON.stringify(body),
    });
    try {
      const denied = handle.tokenFor({ runId: "no-library", orgId: "org", kind: "work", profile: "harness-read-only" });
      expect((await request(denied, { query: "find contract" })).status).toBe(403);
      const binding: RunBinding = { runId: "library", orgId: "org", kind: "work", profile: "harness-read-only", libraryRead: true };
      const allowed = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, libraryRead: false })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, kind: "workflow" })).toThrow("Invalid broker library grant");
      expect((await request(allowed, { query: "x" })).status).toBe(400);
      expect((await request(allowed, { query: "find contract", limit: 21 })).status).toBe(400);
      expect((await request(allowed, { query: "find contract", userId: "forged", orgId: "forged", runId: "forged", limit: 2 })).status).toBe(200);
      expect(cp.searchLibraryForRun).toHaveBeenCalledWith({ query: "find contract", limit: 2, orgId: "org", runId: "library" });
      expect((await request(allowed, { query: "find contract" }, "/v1/memory/search")).status).toBe(403);
    } finally { await handle.close(); }
  });

  it("admits only an explicitly bound Harness batch read to GraphJin", async () => {
    const cp = stubControlPlane();
    cp.queryGraphjinRead = vi.fn(async () => ({ data: { rows: [{ id: 1 }] } }));
    const handle = await startAgentBroker({ controlPlane: cp, port: 0 });
    const query = async (token: string) => fetch(`http://127.0.0.1:${handle.port}/v1/graphjin/query`, {
      method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify({ query: "query { rows { id } }", orgId: "forged", runId: "forged" }),
    });
    try {
      const denied = handle.tokenFor({ runId: "no-batch", orgId: "org", kind: "work", profile: "harness-read-only" });
      expect((await query(denied)).status).toBe(403);
      const binding: RunBinding = { runId: "batch", orgId: "org", kind: "work", profile: "harness-read-only", batchRead: true };
      const allowed = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, batchRead: false })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, kind: "workflow" })).toThrow("Invalid broker batch grant");
      expect((await query(allowed)).status).toBe(200);
      expect(cp.queryGraphjinRead).toHaveBeenCalledWith({ query: "query { rows { id } }", orgId: "org", runId: "batch" });
      const invalid = await fetch(`http://127.0.0.1:${handle.port}/v1/graphjin/query`, {
        method: "POST", headers: { authorization: `Bearer ${allowed}`, "content-type": "application/json" },
        body: JSON.stringify({ query: "query { rows { id } }", variables: { bypass: true } }),
      });
      expect(invalid.status).toBe(400);
      expect(cp.queryGraphjinRead).toHaveBeenCalledTimes(1);
    } finally { await handle.close(); }
  });

  it("admits registry-backed records and blueprint reads only for the bound work run", async () => {
    const cp = stubControlPlane();
    cp.listRecordCatalog = vi.fn(async () => ({ apps: [] })) as AgentControlPlane["listRecordCatalog"];
    cp.findRecords = vi.fn(async () => ({ records: [] })) as AgentControlPlane["findRecords"];
    cp.getRecord = vi.fn(async () => null) as AgentControlPlane["getRecord"];
    cp.listRecordBlueprints = vi.fn(async () => ({ blueprints: [] })) as AgentControlPlane["listRecordBlueprints"];
    cp.findRecycledRecords = vi.fn(async () => ({ records: [] })) as AgentControlPlane["findRecycledRecords"];
    cp.getRecycledRecord = vi.fn(async () => null) as AgentControlPlane["getRecycledRecord"];
    const handle = await startAgentBroker({ controlPlane: cp, port: 0 });
    const request = (token: string, path: string, body: object) => fetch(`http://127.0.0.1:${handle.port}${path}`, {
      method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" }, body: JSON.stringify(body),
    });
    try {
      const denied = handle.tokenFor({ runId: "no-records", orgId: "org", kind: "work", profile: "harness-read-only" });
      for (const path of ["/v1/records/catalog", "/v1/records/blueprints", "/v1/records/recycle/find", "/v1/records/recycle/get"]) {
        expect((await request(denied, path, {})).status).toBe(403);
      }
      const binding: RunBinding = { runId: "records", orgId: "org", kind: "work", profile: "harness-read-only", recordsRead: true, lookupRead: false };
      const token = handle.tokenFor(binding);
      expect(() => handle.tokenFor({ ...binding, recordsRead: false })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, lookupRead: true })).toThrow("conflicts");
      expect(() => handle.tokenFor({ ...binding, kind: "workflow" })).toThrow("Invalid broker lookup grant");
      expect(() => handle.tokenFor({ ...binding, kind: "workflow", lookupRead: undefined })).toThrow("Invalid broker records grant");
      expect((await request(token, "/v1/records/catalog", { appId: "crm", orgId: "forged", runId: "forged" })).status).toBe(200);
      expect((await request(token, "/v1/records/find", { appId: "crm", objectApiName: "lead", first: 5, orgId: "forged", runId: "forged" })).status).toBe(200);
      expect((await request(token, "/v1/records/get", { appId: "crm", objectApiName: "lead", recordId: "lead-42", orgId: "forged", runId: "forged" })).status).toBe(200);
      expect((await request(token, "/v1/records/blueprints", { blueprintId: "crm", orgId: "forged" })).status).toBe(200);
      expect((await request(token, "/v1/records/recycle/find", { appId: "crm", objectApiName: "lead", orgId: "forged", runId: "forged" })).status).toBe(200);
      expect((await request(token, "/v1/records/recycle/get", { appId: "crm", objectApiName: "lead", recordId: "lead-deleted", orgId: "forged", runId: "forged" })).status).toBe(200);
      expect(cp.listRecordCatalog).toHaveBeenCalledWith({ orgId: "org", runId: "records", appId: "crm" });
      expect(cp.findRecords).toHaveBeenCalledWith({ orgId: "org", runId: "records", appId: "crm", objectApiName: "lead", first: 5 });
      expect(cp.getRecord).toHaveBeenCalledWith({ orgId: "org", runId: "records", appId: "crm", objectApiName: "lead", recordId: "lead-42" });
      expect(cp.listRecordBlueprints).toHaveBeenCalledWith({ orgId: "org", blueprintId: "crm" });
      expect(cp.findRecycledRecords).toHaveBeenCalledWith({ orgId: "org", runId: "records", appId: "crm", objectApiName: "lead" });
      expect(cp.getRecycledRecord).toHaveBeenCalledWith({ orgId: "org", runId: "records", appId: "crm", objectApiName: "lead", recordId: "lead-deleted" });
      for (const path of ["/v1/harness/lookup", "/v1/memory/search", "/v1/action/request"]) {
        expect((await request(token, path, {})).status).toBe(403);
      }
    } finally { await handle.close(); }
  });

  it("bounds Harness tokens to journaled lookup without widening on reuse", async () => {
    const cp=stubControlPlane();
    cp.createActionRequest=vi.fn(async()=>({id:"unexpected",status:"approved"}));
    cp.enqueueActionExecute=vi.fn(async()=>{});
    cp.searchWorkMemoryByContext=vi.fn(async()=>[]);
    const onEvents=vi.fn(async()=>{});
    const handle=await startAgentBroker({controlPlane:cp,onEvents,port:0});
    try {
      const binding:RunBinding={runId:"restricted",orgId:"org",kind:"work",profile:"harness-read-only",memoryRead:true};
      const token=handle.tokenFor(binding);
      expect(handle.tokenFor({...binding})).toBe(token);
      for(const change of [{orgId:"other"},{threadId:"other"},{memoryRead:false},{operationLimit:12}]) {
        expect(()=>handle.tokenFor({...binding,...change})).toThrow("conflicts");
      }
      expect(()=>handle.tokenFor({runId:"invalid-limit",orgId:"org",kind:"work",profile:"harness-read-only",operationLimit:33})).toThrow("Invalid broker operation limit");
      for(const change of [{profile:undefined},{kind:"workflow" as const}]) {
        expect(()=>handle.tokenFor({...binding,...change})).toThrow("Invalid broker memory grant");
      }
      // Mutating the caller's object cannot alter the saved capability.
      binding.profile=undefined;
      for(const path of ["/v1/action/request","/v1/action/enqueue","/v1/memory/remember","/v1/events","/v1/graphjin/agent","/v1/graphjin/tools/call","/v1/future/route"]) {
        const response=await fetch(`http://127.0.0.1:${handle.port}${path}`,{method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({profile:"legacy",status:"approved",events:[]})});
        expect(response.status,path).toBe(403);
        expect(await response.json()).toEqual({error:"Harness broker capability denied"});
      }
      expect(cp.createActionRequest).not.toHaveBeenCalled();
      expect(cp.enqueueActionExecute).not.toHaveBeenCalled();
      expect(onEvents).not.toHaveBeenCalled();
      const memory=await fetch(`http://127.0.0.1:${handle.port}/v1/memory/search`,{method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({query:"test policy",limit:2,orgId:"forged",runId:"forged",userId:"forged"})});
      expect(memory.status).toBe(200);
      expect(await memory.json()).toEqual([]);
      expect(cp.searchWorkMemoryByContext).toHaveBeenCalledWith({orgId:"org",runId:"restricted",query:"test policy",limit:2});
      const invalidMemory=await fetch(`http://127.0.0.1:${handle.port}/v1/memory/search`,{method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:JSON.stringify({query:"x"})});
      expect(invalidMemory.status).toBe(400);
      const recordsToken=handle.tokenFor({runId:"records",orgId:"org",kind:"work",profile:"harness-read-only"});
      const recordsMemory=await fetch(`http://127.0.0.1:${handle.port}/v1/memory/search`,{method:"POST",headers:{authorization:`Bearer ${recordsToken}`,"content-type":"application/json"},body:JSON.stringify({query:"test policy"})});
      expect(recordsMemory.status).toBe(403);
      // The permitted route reaches its own validation, with no database needed.
      const allowed=await fetch(`http://127.0.0.1:${handle.port}/v1/harness/lookup`,{method:"POST",headers:{authorization:`Bearer ${token}`,"content-type":"application/json"},body:"{}"});
      expect(allowed.status).toBe(200);
      expect(await allowed.json()).toEqual({error:"Invalid Harness lookup operation"});
      const legacy=handle.tokenFor({runId:"legacy",orgId:"org",kind:"work"});
      expect((await postEvents(handle.port,legacy)).status).toBe(200);
      expect(()=>handle.tokenFor({runId:"legacy",orgId:"org",kind:"work",profile:"harness-read-only"})).toThrow("conflicts");
    } finally {await handle.close();}
  });

  it("mints one stable token per run and resolves it over HTTP", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const a = handle.tokenFor({ runId: "r1", orgId: "o1", kind: "work" });
      expect(handle.tokenFor({ runId: "r1", orgId: "o1", kind: "work" })).toBe(a); // reused
      expect(handle.tokenFor({ runId: "r2", orgId: "o1", kind: "work" })).not.toBe(a); // per-run

      expect(handle.url).toBe(`http://host.openshell.internal:${handle.port}`);

      expect((await postEvents(handle.port, a)).status).toBe(200);
      expect((await postEvents(handle.port, "unknown")).status).toBe(401);
    } finally {
      await handle.close();
    }
  });

  it("releases a run's token so it stops resolving", async () => {
    const handle = await startAgentBroker({ controlPlane: stubControlPlane(), port: 0 });
    try {
      const tok = handle.tokenFor({ runId: "r1", orgId: "o1", kind: "work" });
      handle.release("r1");
      expect((await postEvents(handle.port, tok)).status).toBe(401);
      // a token minted after release is a fresh one, not the revoked value:
      expect(handle.tokenFor({ runId: "r1", orgId: "o1", kind: "work" })).not.toBe(tok);
    } finally {
      await handle.close();
    }
  });

  it("advertises a custom host alias in the url", async () => {
    const handle = await startAgentBroker({
      controlPlane: stubControlPlane(),
      port: 0,
      hostAlias: "10.200.0.1",
    });
    try {
      expect(handle.url).toBe(`http://10.200.0.1:${handle.port}`);
    } finally {
      await handle.close();
    }
  });

  it("binds native records reads to the token's org and run", async () => {
    const cp = stubControlPlane();
    cp.listRecordCatalog = vi.fn(async () => ({ apps: [] }));
    const handle = await startAgentBroker({ controlPlane: cp, port: 0 });
    try {
      const token = handle.tokenFor({
        runId: "trusted-run",
        orgId: "trusted-org",
        kind: "work",
      });
      const response = await fetch(
        new URL("/v1/records/catalog", `http://127.0.0.1:${handle.port}`),
        {
          method: "POST",
          headers: {
            authorization: `Bearer ${token}`,
            "content-type": "application/json",
          },
          body: JSON.stringify({
            orgId: "other-org",
            runId: "other-run",
            appId: "equipment",
          }),
        },
      );
      expect(response.status).toBe(200);
      expect(cp.listRecordCatalog).toHaveBeenCalledWith({
        orgId: "trusted-org",
        runId: "trusted-run",
        appId: "equipment",
      });
    } finally {
      await handle.close();
    }
  });
});

describe("ensureAgentBroker (SEC9: always on)", () => {
  it("starts the per-process broker and is idempotent", async () => {
    const prevPort = process.env.OPENNEKO_BROKER_PORT;
    process.env.OPENNEKO_BROKER_PORT = "0";
    try {
      const a = await ensureAgentBroker();
      const b = await ensureAgentBroker();
      expect(a).toBeDefined();
      expect(b).toBe(a);
      const received: unknown[] = [];
      const unregister = registerAgentBrokerEventSink("r-live", async (event) => {
        received.push(event);
      });
      const token = a!.tokenFor({
        runId: "r-live",
        orgId: "o1",
        threadId: "t1",
        kind: "work",
      });
      const event = { type: "status", message: "Approval pending" };
      expect((await postEvents(a!.port, token, [event])).status).toBe(200);
      expect(received).toEqual([event]);
      unregister();
      await shutdownAgentBroker();
    } finally {
      if (prevPort === undefined) delete process.env.OPENNEKO_BROKER_PORT;
      else process.env.OPENNEKO_BROKER_PORT = prevPort;
    }
  });
});

import { proposeHarnessAction } from "./harness-proposal";
import { validateRenderCardsInput } from "./a2ui-contract";
import { startupEvent } from "@neko/telemetry/startup";
import { recordHarnessLookup } from "./harness-operation";
import { recordHarnessOperation } from "./harness-operation";
import { WORK_MEMORY_KINDS } from "./memory-types";
import { WORKFLOW_OUTPUT_SCHEMA } from "../workflows/fence-schemas";
import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import { randomUUID } from "node:crypto";
import type { AgentEvent } from "../agent-backend";
import { inProcessControlPlane, type AgentControlPlane } from "./control-plane";
import { sandboxBrokerHost } from "./sandbox-net";
import {
  traceActionPolicy,
  traceGraphjinQuery,
  traceGraphjinToolCall,
  traceGraphjinToolsList,
  traceLibrarySearch,
  traceMemorySearch,
  traceRecordBlueprints,
  traceWorkflowList,
  unwrapWorkSemanticTraceControlPlane,
} from "./semantic-trace";

/** What a per-run bearer token resolves to — the trust binding. */
export interface RunBinding {
  /** Trusted launcher capability profile; never read from request JSON. */
  profile?: "harness-read-only" | "harness-governed";
  /** Host-selected run-wide operation limit; omitted bindings retain four. */
  operationLimit?: number;
  /** Records-only turns cannot delegate customer-source GraphJin lookups. */
  lookupRead?: boolean;
  /** Explicit customer-surface read grant; records-only runs omit it. */
  memoryRead?: boolean;
  /** Explicit Work-run grant for a journaled memory save. */
  memoryWrite?: boolean;
  /** Explicit customer-surface library search grant. */
  libraryRead?: boolean;
  /** Actor-filtered saved workflow definitions, without mutation authority. */
  workflowRead?: boolean;
  /** Registry-backed reads and shipped blueprints; actor grants remain authoritative. */
  recordsRead?: boolean;
  /** Run-scoped clarification and validated card events from the MCP bridge. */
  interactionEvents?: boolean;
  cardEvents?: boolean;
  /** Controlled file-backed batch reads; never grants general GraphJin MCP. */
  batchRead?: boolean;
  /** Exact queued workflow identity and its journaled output grant. */
  workflowRunId?: string;
  workflowOutput?: boolean;
  workflowAction?: boolean;
  runId: string;
  orgId: string;
  /** Agent jobs have no work_run actor and intentionally use service reads. */
  kind: "work" | "workflow" | "agent-job";
  /** Present for chat/workflow runs so broker-emitted events can be persisted. */
  threadId?: string;
}

export type AgentBrokerEventSink = (event: AgentEvent) => Promise<void>;

const runEventSinks = new Map<string, AgentBrokerEventSink>();
const harnessRecordsReadPaths = new Set([
  "/v1/records/catalog",
  "/v1/records/find",
  "/v1/records/get",
  "/v1/records/blueprints",
  "/v1/records/recycle/find",
  "/v1/records/recycle/get",
]);

function validHarnessEvents(binding: RunBinding, value: unknown): boolean {
  if (!Array.isArray(value) || value.length !== 1) return false;
  const event = value[0];
  if (!event || typeof event !== "object") return false;
  if (event.type === "surface") {
    return binding.cardEvents === true && validateRenderCardsInput({ messages: event.messages }).success;
  }
  if (event.type !== "needs_input" || binding.interactionEvents !== true ||
      typeof event.question !== "string" || !event.question.trim() || event.question.length > 500 ||
      !Array.isArray(event.questions) || event.questions.length < 1 || event.questions.length > 3) return false;
  return event.questions.every((question: unknown, index: number) => {
    if (!question || typeof question !== "object") return false;
    const item = question as Record<string, unknown>;
    return item.id === `q${index + 1}` && typeof item.question === "string" &&
      item.question.trim().length > 0 && item.question.length <= 500;
  });
}

/**
 * Attach the host run's normal event sink to MCP bridge emissions. This keeps
 * approval cards, builder confirmations, and other tool-authored events on the
 * same scrubbed/persisted/live-SSE path as backend events.
 */
export function registerAgentBrokerEventSink(
  runId: string,
  sink: AgentBrokerEventSink,
): () => void {
  const previous = runEventSinks.get(runId);
  runEventSinks.set(runId, sink);
  return () => {
    if (runEventSinks.get(runId) === sink) {
      if (previous) runEventSinks.set(runId, previous);
      else runEventSinks.delete(runId);
    }
  };
}

export interface AgentBrokerDeps {
  /** Host-side control plane (real DB/pg-boss access). */
  controlPlane: AgentControlPlane;
  /** Validate a bearer token → its run binding (undefined = reject). */
  resolveRun(token: string): RunBinding | undefined;
  /** Host-side event sink: scrub + persist. Scrubbing stays here so a
   *  sandboxed agent can't leak a secret it was never given. */
  onEvents(binding: RunBinding, events: AgentEvent[]): Promise<void>;
}

/**
 * Localhost HTTP/JSON broker — the ONLY channel a sandboxed agent turn has
 * back to the trusted control plane. orgId and workRunId are always taken
 * from the token binding, never from the request body, so a compromised
 * sandbox can't act cross-run or cross-org.
 */
export function createAgentBroker(deps: AgentBrokerDeps): Server {
  return createServer((req, res) => {
    void handle(deps, req, res).catch((err) => {
      const message = err instanceof Error ? err.message : String(err);
      // The bridge runs outside the worker process and Hermes captures its
      // stderr inside an ephemeral sandbox. Keep the trusted-side failure in
      // worker logs as well; never include the request body or bearer token.
      console.error(
        `[agent-broker] ${req.method ?? "UNKNOWN"} ${req.url ?? "/"} failed: ${message}`,
      );
      send(res, 500, { error: message });
    });
  });
}

async function handle(
  deps: AgentBrokerDeps,
  req: IncomingMessage,
  res: ServerResponse,
): Promise<void> {
  const auth = req.headers.authorization ?? "";
  const token = auth.startsWith("Bearer ") ? auth.slice(7) : "";
  const binding = deps.resolveRun(token);
  if (!binding) return send(res, 401, { error: "unauthorized" });
  if (req.method !== "POST") return send(res, 405, { error: "method_not_allowed" });

  const path = (req.url ?? "").split("?")[0];
  // SEC5: every authenticated gateway call is audited with the dual
  // identity (human principal + agent backend). Best-effort — auditing
  // must never fail the call itself.
  void auditControlPlaneCall(binding, path);

  if (binding.profile && !(binding.lookupRead !== false && path === "/v1/harness/lookup") && !(binding.profile === "harness-governed" && (binding.kind === "work" || binding.kind === "workflow" && binding.workflowAction === true) && path === "/v1/harness/propose") && !(binding.memoryRead === true && path === "/v1/memory/search") && !(binding.memoryWrite === true && binding.kind === "work" && path === "/v1/harness/memory/save") && !(binding.libraryRead === true && path === "/v1/library/search") && !(binding.workflowRead === true && binding.kind === "work" && path === "/v1/workflow/list") && !(binding.recordsRead === true && binding.kind === "work" && harnessRecordsReadPaths.has(path)) && !(binding.batchRead === true && binding.kind === "work" && path === "/v1/graphjin/query") && !(binding.workflowOutput === true && binding.kind === "workflow" && path === "/v1/harness/workflow-output/emit") && !((binding.interactionEvents || binding.cardEvents) && path === "/v1/events")) {
    startupEvent("harness.broker_capability", {
      runId: binding.runId, outcome: "denied", profile: binding.profile,
    });
    return send(res, 403, { error: "Harness broker capability denied" });
  }
  const body = (await readJson(req)) as Record<string, unknown>;
  // A trusted in-process eval may pass an already-decorated control plane.
  // The broker is the authoritative outer boundary here, so unwrap it before
  // applying the broker trace below and avoid recording every call twice.
  const cp = unwrapWorkSemanticTraceControlPlane(deps.controlPlane);

  switch (path) {
    case "/v1/policy/evaluate":
      return send(
        res,
        200,
        await traceActionPolicy({
          binding,
          request: {
            scope: body.scope === "internal" ? "internal" : "external",
            kind: String(body.kind ?? ""),
            ...(typeof body.target === "string" || body.target === null
              ? { target: body.target }
              : {}),
            ...(typeof body.riskLevel === "string" || body.riskLevel === null
              ? { riskLevel: body.riskLevel }
              : {}),
          },
          execute: () =>
            cp.evaluateActionPolicy({
              ...body,
              orgId: binding.orgId,
            } as Parameters<AgentControlPlane["evaluateActionPolicy"]>[0]),
        }),
      );
    case "/v1/action/request": {
      // The sandbox's status and policyId are claims. The broker decides both
      // from the rules, so code in the box cannot create an approved request.
      const input = {
        ...body,
        orgId: binding.orgId,
        workRunId: binding.runId,
      } as Parameters<AgentControlPlane["createActionRequest"]>[0];
      const decision = await cp.evaluateActionPolicy({
        orgId: binding.orgId,
        scope: input.scope,
        kind: input.kind,
        target: input.target ?? null,
        payload: input.payload ?? null,
        riskLevel: input.riskLevel ?? null,
      });
      if (decision.decision === "deny" || decision.decision === "no_policy") {
        return send(res, 403, { error: `action ${input.kind} denied: ${decision.reason}` });
      }
      const status =
        input.status === "draft"
          ? "draft"
          : input.status === "approved" && decision.decision === "allow"
            ? "approved"
            : "pending_approval";
      return send(
        res,
        200,
        await cp.createActionRequest({ ...input, policyId: decision.policy.id, status }),
      );
    }
    case "/v1/action/enqueue":
      await cp.enqueueActionExecute({
        orgId: binding.orgId,
        actionRequestId: String(body.actionRequestId),
      });
      return send(res, 200, { ok: true });
    case "/v1/action/wait":
      return send(
        res,
        200,
        await cp.waitForActionExecution({
          orgId: binding.orgId,
          actionRequestId: String(body.actionRequestId),
          ...(typeof body.timeoutMs === "number"
            ? { timeoutMs: body.timeoutMs }
            : {}),
        }),
      );
    case "/v1/memory/remember":
      // CV2: the memory layer is derived server-side from the bound run's
      // actor — an agent-supplied userId is never trusted.
      delete body.userId;
      return send(
        res,
        200,
        await cp.rememberWorkMemory({
          ...body,
          orgId: binding.orgId,
          runId: binding.runId,
        } as Parameters<AgentControlPlane["rememberWorkMemory"]>[0]),
      );
    case "/v1/memory/search": {
      delete body.userId;
      const query = String(body.query ?? "");
      const limit = typeof body.limit === "number" ? body.limit : undefined;
      if (binding.profile && (typeof body.query !== "string" || query.trim().length < 2 || query.length > 800 || (body.limit !== undefined && (limit === undefined || !Number.isInteger(limit) || limit < 1 || limit > 20)))) {
        return send(res, 400, { error: "Invalid Harness memory search" });
      }
      return send(
        res,
        200,
        await traceMemorySearch({
          binding,
          request: { query, ...(limit !== undefined ? { limit } : {}) },
          execute: () => cp.searchWorkMemoryByContext(binding.profile
            ? { orgId: binding.orgId, runId: binding.runId, query, ...(limit !== undefined ? { limit } : {}) }
            : { ...body, orgId: binding.orgId, runId: binding.runId } as Parameters<AgentControlPlane["searchWorkMemoryByContext"]>[0]),
        }),
      );
    }
    case "/v1/library/search": {
      // Same rule as memory: the personal layer comes from the bound
      // run's owner, never from agent-supplied identity.
      if (binding.libraryRead && (typeof body.query !== "string" || body.query.length < 2 || body.query.length > 800 || (body.limit !== undefined && (typeof body.limit !== "number" || !Number.isInteger(body.limit) || body.limit < 1 || body.limit > 20)))) {
        return send(res, 400, { error: "Invalid Harness library search" });
      }
      delete body.userId;
      const query = String(body.query ?? "");
      const limit = typeof body.limit === "number" ? body.limit : undefined;
      return send(
        res,
        200,
        await traceLibrarySearch({
          binding,
          request: { query, ...(limit !== undefined ? { limit } : {}) },
          execute: () =>
            cp.searchLibraryForRun({
              ...body,
              orgId: binding.orgId,
              runId: binding.runId,
            } as Parameters<AgentControlPlane["searchLibraryForRun"]>[0]),
        }),
      );
    }
    case "/v1/graphjin/query": {
      if (binding.batchRead && (typeof body.query !== "string" || !body.query.trim() || body.query.length > 60_000 || body.variables !== undefined || body.operationName !== undefined)) {
        return send(res, 400, { error: "Invalid Harness batch query" });
      }
      const query = String(body.query ?? "");
      const variables =
        body.variables && typeof body.variables === "object"
          ? (body.variables as Record<string, unknown>)
          : undefined;
      const operationName =
        typeof body.operationName === "string" ? body.operationName : undefined;
      return send(
        res,
        200,
        await traceGraphjinQuery({
          binding,
          query,
          ...(variables !== undefined ? { variables } : {}),
          ...(operationName !== undefined ? { operationName } : {}),
          execute: () =>
            cp.queryGraphjinRead({
              query,
              ...(variables !== undefined ? { variables } : {}),
              ...(operationName !== undefined ? { operationName } : {}),
              orgId: binding.orgId,
              ...(binding.kind === "agent-job" ? {} : { runId: binding.runId }),
            }),
        }),
      );
    }
    case "/v1/graphjin/tools/list":
      return send(
        res,
        200,
        await traceGraphjinToolsList({
          binding,
          execute: () =>
            cp.listGraphjinTools({
              orgId: binding.orgId,
              ...(binding.kind === "agent-job" ? {} : { runId: binding.runId }),
            }),
        }),
      );
    case "/v1/graphjin/tools/call": {
      const name = String(body.name ?? "");
      const args =
        body.arguments && typeof body.arguments === "object"
          ? (body.arguments as Record<string, unknown>)
          : undefined;
      return send(
        res,
        200,
        await traceGraphjinToolCall({
          binding,
          toolName: name,
          ...(args !== undefined ? { arguments: args } : {}),
          execute: () =>
            cp.callGraphjinTool({
              orgId: binding.orgId,
              ...(binding.kind === "agent-job" ? {} : { runId: binding.runId }),
              name,
              ...(args !== undefined ? { arguments: args } : {}),
            }),
        }),
      );
    }
    case "/v1/harness/propose": {
      if (binding.profile !== "harness-governed" || (binding.kind !== "work" && !(binding.kind === "workflow" && binding.workflowAction && binding.workflowRunId))) return send(res,403,{error:"Harness proposal capability denied"});
      return send(res,200,await proposeHarnessAction(binding,body,cp));
    }
    case "/v1/harness/memory/save": {
      if (binding.profile === undefined || binding.memoryWrite !== true || binding.kind !== "work" || !binding.threadId ||
          typeof body.instruction !== "string" || typeof body.binding !== "string" || !/^[a-f0-9]{64}$/.test(body.binding)) {
        return send(res,403,{error:"Harness memory save denied"});
      }
      let input: Record<string, unknown>;
      try { input = JSON.parse(body.instruction) as Record<string, unknown>; }
      catch { return send(res,400,{error:"Invalid Harness memory save"}); }
      if (!input || Array.isArray(input) || typeof input !== "object" ||
          Object.keys(input).some(key=>!["text","kind","scope","pinned"].includes(key)) ||
          typeof input.text !== "string" || input.text.trim().length < 5 || input.text.length > 2000 ||
          (input.kind !== undefined && !WORK_MEMORY_KINDS.includes(input.kind as typeof WORK_MEMORY_KINDS[number])) ||
          (input.scope !== undefined && input.scope !== "global" && input.scope !== "thread") ||
          (input.pinned !== undefined && typeof input.pinned !== "boolean")) {
        return send(res,400,{error:"Invalid Harness memory save"});
      }
      const request = {tool:"memory_save" as const,binding:body.binding,instruction:body.instruction};
      return send(res,200,await recordHarnessOperation(binding,body.operationId,request,async()=>{
        const memory=await cp.rememberWorkMemory({orgId:binding.orgId,runId:binding.runId,threadId:binding.threadId,
          text:input.text as string,kind:(input.kind ?? "business_rule") as typeof WORK_MEMORY_KINDS[number],
          scope:(input.scope ?? "global") as "global"|"thread",pinned:(input.pinned ?? true) as boolean});
        return {ok:true,memoryId:memory.id};
      }));
    }
    case "/v1/harness/lookup": {
      const request = {
        instruction: typeof body.instruction === "string" ? body.instruction : "",
        ...(typeof body.dataSourceId === "string" ? {dataSourceId:body.dataSourceId} : {}),
        maxSteps:12,
      };
      const abort = new AbortController();
      const disconnected = () => { if (!res.writableFinished) abort.abort(); };
      res.once("close",disconnected);
      if (req.aborted || res.destroyed) abort.abort();
      try {
        return send(res,200,await recordHarnessLookup(binding,body.operationId,request,()=>
          cp.askGraphjinDataAgent({orgId:binding.orgId,...(binding.kind === "agent-job" ? {} : {runId:binding.runId}),...request,signal:abort.signal}),abort.signal));
      } finally { res.removeListener("close",disconnected); }
    }
    case "/v1/harness/workflow-output/emit": {
      if (!binding.workflowRunId || binding.kind !== "workflow") return send(res,403,{error:"Workflow output denied"});
      const instruction = typeof body.instruction === "string" ? body.instruction : "";
      let output: ReturnType<typeof WORKFLOW_OUTPUT_SCHEMA.parse>;
      try { output = WORKFLOW_OUTPUT_SCHEMA.strict().parse(JSON.parse(instruction)); }
      catch { return send(res,400,{error:"Invalid workflow output"}); }
      const {pool} = await import("@neko/db");
      const owned = await pool().query("SELECT 1 FROM workflow_run WHERE org_id=$1 AND id=$2 AND work_run_id=$3 LIMIT 1",
        [binding.orgId,binding.workflowRunId,binding.runId]);
      if (!owned.rowCount) return send(res,403,{error:"Workflow run binding denied"});
      const result = await recordHarnessOperation(binding,body.operationId,
        {tool:"workflow_output",instruction,binding:body.binding as string},async()=>{
          const saved = await cp.emitWorkflowOutput({
            orgId:binding.orgId,workflowRunId:binding.workflowRunId!,workRunId:binding.runId,
            ...output,
            timeWindowStart:output.timeWindowStart ? new Date(output.timeWindowStart) : null,
            timeWindowEnd:output.timeWindowEnd ? new Date(output.timeWindowEnd) : null,
          });
          return {ok:true,outputId:saved.id,kind:saved.kind};
        });
      if (result && typeof result === "object" && "outputId" in result && "kind" in result && typeof result.outputId === "string" && typeof result.kind === "string") {
        await deps.onEvents(binding,[{type:"output_emit",output_id:result.outputId,kind:result.kind}]).catch(()=>{
          console.warn(`[agent-broker] workflow output event delivery deferred to run reconciliation: ${binding.runId}`);
        });
      }
      return send(res,200,result);
    }
    case "/v1/graphjin/agent":
      return send(
        res,
        200,
        await cp.askGraphjinDataAgent({
          orgId: binding.orgId,
          runId: binding.runId,
          instruction: String(body.instruction ?? ""),
          ...(typeof body.dataSourceId === "string"
            ? { dataSourceId: body.dataSourceId }
            : {}),
          ...(typeof body.maxSteps === "number"
            ? { maxSteps: body.maxSteps }
            : {}),
        }),
      );
    case "/v1/records/catalog":
      return send(
        res,
        200,
        await cp.listRecordCatalog({
          orgId: binding.orgId,
          runId: binding.runId,
          ...(typeof body.appId === "string" ? { appId: body.appId } : {}),
        }),
      );
    case "/v1/records/find":
      return send(
        res,
        200,
        await cp.findRecords({
          orgId: binding.orgId,
          runId: binding.runId,
          appId: String(body.appId ?? ""),
          objectApiName: String(body.objectApiName ?? ""),
          ...(typeof body.first === "number" ? { first: body.first } : {}),
          ...(typeof body.after === "string" ? { after: body.after } : {}),
          ...(typeof body.search === "string" ? { search: body.search } : {}),
          ...(Array.isArray(body.filters)
            ? {
                filters: body.filters as Parameters<
                  AgentControlPlane["findRecords"]
                >[0]["filters"],
              }
            : {}),
          ...(body.sort && typeof body.sort === "object"
            ? {
                sort: body.sort as NonNullable<
                  Parameters<AgentControlPlane["findRecords"]>[0]["sort"]
                >,
              }
            : {}),
          ...(typeof body.myRecords === "boolean"
            ? { myRecords: body.myRecords }
            : {}),
        }),
      );
    case "/v1/records/get":
      return send(
        res,
        200,
        await cp.getRecord({
          orgId: binding.orgId,
          runId: binding.runId,
          appId: String(body.appId ?? ""),
          objectApiName: String(body.objectApiName ?? ""),
          recordId: String(body.recordId ?? ""),
          ...(typeof body.allFields === "boolean"
            ? { allFields: body.allFields }
            : {}),
        }),
      );
    case "/v1/records/recycle/find":
      return send(
        res,
        200,
        await cp.findRecycledRecords({
          orgId: binding.orgId,
          runId: binding.runId,
          appId: String(body.appId ?? ""),
          objectApiName: String(body.objectApiName ?? ""),
          ...(typeof body.first === "number" ? { first: body.first } : {}),
          ...(typeof body.after === "string" ? { after: body.after } : {}),
          ...(typeof body.search === "string" ? { search: body.search } : {}),
        }),
      );
    case "/v1/records/recycle/get":
      return send(
        res,
        200,
        await cp.getRecycledRecord({
          orgId: binding.orgId,
          runId: binding.runId,
          appId: String(body.appId ?? ""),
          objectApiName: String(body.objectApiName ?? ""),
          recordId: String(body.recordId ?? ""),
        }),
      );
    case "/v1/records/blueprints": {
      const blueprintId =
        typeof body.blueprintId === "string" ? body.blueprintId : undefined;
      return send(
        res,
        200,
        await traceRecordBlueprints({
          binding,
          request: {
            ...(blueprintId !== undefined ? { blueprintId } : {}),
          },
          execute: () =>
            cp.listRecordBlueprints({
              orgId: binding.orgId,
              ...(blueprintId !== undefined ? { blueprintId } : {}),
            }),
        }),
      );
    }
    case "/v1/workflow/save":
      return send(
        res,
        200,
        await cp.saveWorkflowWithTrigger({
          ...body,
          orgId: binding.orgId,
          createdByRunId: binding.runId,
        } as Parameters<AgentControlPlane["saveWorkflowWithTrigger"]>[0]),
      );
    case "/v1/workflow-output/emit":
      return send(
        res,
        200,
        await cp.emitWorkflowOutput(workflowOutputInputFromBody(body, binding)),
      );
    case "/v1/workflow/list": {
      const limit = typeof body.limit === "number" ? body.limit : undefined;
      if (binding.profile && (body.limit !== undefined && (limit === undefined || !Number.isInteger(limit) || limit < 1 || limit > 200))) {
        return send(res,400,{error:"Invalid Harness workflow list"});
      }
      return send(
        res,
        200,
        await traceWorkflowList({
          binding,
          request: { ...(limit !== undefined ? { limit } : {}) },
          execute: () =>
            cp.listWorkflowsWithTriggers({
              orgId: binding.orgId,
              limit,
              runId: binding.runId,
            }),
        }),
      );
    }
    case "/v1/workflow/delete":
      // orgId comes from the token binding, never the body — a sandbox
      // can't delete another org's workflow by passing its id.
      return send(
        res,
        200,
        await cp.deleteWorkflow({
          orgId: binding.orgId,
          workflowId: String(body.workflowId),
          runId: binding.runId,
        }),
      );
    case "/v1/rule/save":
      return send(
        res,
        200,
        await cp.upsertActionPolicyByName({
          ...body,
          orgId: binding.orgId,
          createdByRunId: binding.runId,
        } as Parameters<AgentControlPlane["upsertActionPolicyByName"]>[0]),
      );
    case "/v1/rule/list":
      return send(
        res,
        200,
        await cp.listActionPolicies({
          orgId: binding.orgId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/plugins/list":
      return send(res, 200, await cp.listPlugins({ orgId: binding.orgId }));
    case "/v1/users/list":
      return send(res, 200, await cp.listUsers({ orgId: binding.orgId }));
    case "/v1/groups/list":
      return send(res, 200, await cp.listGroups({ orgId: binding.orgId }));
    case "/v1/channels/list":
      return send(res, 200, await cp.listChannels({ orgId: binding.orgId }));
    case "/v1/datasources/list":
      return send(res, 200, await cp.listDataSources({ orgId: binding.orgId }));
    case "/v1/source-graph/describe":
      return send(
        res,
        200,
        await cp.describeSourceGraph({
          orgId: binding.orgId,
          runId: binding.runId,
        }),
      );
    case "/v1/source-secrets/names":
      return send(
        res,
        200,
        await cp.listSourceSecretNames({
          orgId: binding.orgId,
          runId: binding.runId,
        }),
      );
    case "/v1/openapi/import":
      return send(
        res,
        200,
        await cp.importOpenApiSpec({
          orgId: binding.orgId,
          runId: binding.runId,
          url: String(body.url ?? ""),
          baseUrl: typeof body.baseUrl === "string" ? body.baseUrl : null,
        }),
      );
    case "/v1/openapi/list":
      return send(
        res,
        200,
        await cp.listOpenApiSpecs({
          orgId: binding.orgId,
          runId: binding.runId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/source-config/agent":
      return send(
        res,
        200,
        await cp.askSourceConfigAgent({
          orgId: binding.orgId,
          runId: binding.runId,
          dataSourceId:
            typeof body.dataSourceId === "string"
              ? body.dataSourceId
              : undefined,
          instruction: String(body.instruction ?? ""),
          maxSteps:
            typeof body.maxSteps === "number" ? body.maxSteps : undefined,
        }),
      );
    case "/v1/source-config/preview":
      return send(
        res,
        200,
        await cp.previewSourceConfigChange({
          orgId: binding.orgId,
          runId: binding.runId,
          dataSourceId:
            typeof body.dataSourceId === "string"
              ? body.dataSourceId
              : undefined,
          payload:
            body.payload && typeof body.payload === "object"
              ? (body.payload as Record<string, unknown>)
              : {},
        }),
      );
    case "/v1/audit/list":
      // ADM4: the admin gate runs on the BOUND run's actor — the
      // sandbox can't claim someone else's run.
      return send(
        res,
        200,
        await cp.listAuditTrail({
          orgId: binding.orgId,
          runId: binding.runId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/events":
      if (binding.profile && !validHarnessEvents(binding, body.events)) {
        return send(res, 403, { error: "Harness event capability denied" });
      }
      await deps.onEvents(binding, (body.events as AgentEvent[]) ?? []);
      return send(res, 200, { ok: true });
    default:
      return send(res, 404, { error: "not_found" });
  }
}

// Per-run dual-identity cache so auditing costs one DB lookup per run,
// not per call. Bounded; runs are short-lived.
const runIdentityCache = new Map<
  string,
  { userId: string | null; role: string | null; backend: string | null }
>();
const RUN_IDENTITY_CACHE_MAX = 500;

async function auditControlPlaneCall(
  binding: RunBinding,
  path: string,
): Promise<void> {
  try {
    const { control_plane_audit, db, eq, work_run } = await import("@neko/db");
    let identity = runIdentityCache.get(binding.runId);
    if (!identity) {
      const [run] = await db()
        .select({
          userId: work_run.actor_user_id,
          role: work_run.actor_role,
          backend: work_run.backend,
        })
        .from(work_run)
        .where(eq(work_run.id, binding.runId))
        .limit(1);
      identity = run ?? { userId: null, role: null, backend: null };
      if (runIdentityCache.size >= RUN_IDENTITY_CACHE_MAX) {
        runIdentityCache.clear();
      }
      runIdentityCache.set(binding.runId, identity);
    }
    await db().insert(control_plane_audit).values({
      org_id: binding.orgId,
      run_id: binding.runId,
      path,
      actor_user_id: identity.userId,
      actor_role: identity.role,
      backend: identity.backend,
    });
  } catch (err) {
    console.warn(
      `[agent-broker] audit insert failed (call proceeded): ${err instanceof Error ? err.message : err}`,
    );
  }
}

function readJson(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    let data = "";
    req.on("data", (c: Buffer) => {
      data += c.toString("utf8");
      if (data.length > 8_000_000) req.destroy();
    });
    req.on("end", () => {
      try {
        resolve(data ? JSON.parse(data) : {});
      } catch (err) {
        reject(err);
      }
    });
    req.on("error", reject);
  });
}

function send(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

function workflowOutputInputFromBody(
  body: Record<string, unknown>,
  binding: RunBinding,
): Parameters<AgentControlPlane["emitWorkflowOutput"]>[0] {
  return {
    ...body,
    orgId: binding.orgId,
    workRunId: binding.runId,
    timeWindowStart: optionalDate(body.timeWindowStart, "timeWindowStart"),
    timeWindowEnd: optionalDate(body.timeWindowEnd, "timeWindowEnd"),
  } as Parameters<AgentControlPlane["emitWorkflowOutput"]>[0];
}

function optionalDate(value: unknown, field: string): Date | null | undefined {
  if (value === undefined) return undefined;
  if (value === null) return null;
  if (value instanceof Date) return value;
  if (typeof value !== "string" && typeof value !== "number") {
    throw new Error(`${field} must be an ISO date string or null`);
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    throw new Error(`${field} must be a valid date`);
  }
  return date;
}

/** A running broker + its per-run token registry. */
export interface AgentBrokerHandle {
  /** URL the sandboxed agent reaches the broker at. */
  readonly url: string;
  /** Actual listening port (resolved when port 0 / ephemeral is requested). */
  readonly port: number;
  /** Mint-or-reuse a per-run bearer token; pass the return into the sandbox. */
  tokenFor(binding: RunBinding): string;
  /** Drop a finished run's token. */
  release(runId: string): void;
  close(): Promise<void>;
}

export interface StartAgentBrokerOptions {
  controlPlane: AgentControlPlane;
  /** Host-side scrub + persist of events posted to /v1/events. Default no-op
   *  (agent events normally stream over the launcher's stdout channel). */
  onEvents?: (binding: RunBinding, events: AgentEvent[]) => Promise<void>;
  /** Host or address the sandbox uses to reach this broker. Packaged Compose
   *  uses its private container IP; host mode defaults to the gateway alias. */
  hostAlias?: string;
  /** Port to listen on. It is exposed only within the packaged Compose
   *  network; host-mode setups may publish it explicitly. */
  port: number;
}

/**
 * Start a long-lived broker bound to a host control plane, with an in-memory
 * per-run token registry. The worker/web start ONE per process; each run mints
 * a token via {@link AgentBrokerHandle.tokenFor} and releases it on completion.
 */
export async function startAgentBroker(
  opts: StartAgentBrokerOptions,
): Promise<AgentBrokerHandle> {
  const tokens = new Map<string, RunBinding>(); // token -> binding
  const byRun = new Map<string, string>(); // runId -> token

  const server = createAgentBroker({
    controlPlane: opts.controlPlane,
    resolveRun: (token) => tokens.get(token),
    onEvents: opts.onEvents ?? (async () => {}),
  });

  await new Promise<void>((resolve, reject) => {
    const onErr = (e: Error) => reject(e);
    server.once("error", onErr);
    server.listen(opts.port, "0.0.0.0", () => {
      server.removeListener("error", onErr);
      resolve();
    });
  });

  const addr = server.address();
  const port = typeof addr === "object" && addr ? addr.port : opts.port;
  const host = opts.hostAlias ?? "host.openshell.internal";

  return {
    url: `http://${host}:${port}`,
    port,
    tokenFor(binding) {
      const existing = byRun.get(binding.runId);
      if (binding.profile !== undefined && binding.profile !== "harness-read-only" && binding.profile !== "harness-governed") {
        throw new Error("Unknown broker capability profile");
      }
      if (binding.operationLimit !== undefined && (!binding.profile || !Number.isInteger(binding.operationLimit) || binding.operationLimit < 1 || binding.operationLimit > 32)) {
        throw new Error("Invalid broker operation limit");
      }
      if (binding.lookupRead === false && (!binding.profile || (binding.kind !== "work" && binding.kind !== "agent-job"))) {
        throw new Error("Invalid broker lookup grant");
      }
      if (binding.memoryRead && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker memory grant");
      }
      if (binding.memoryWrite && (!binding.profile || binding.kind !== "work" || !binding.threadId)) {
        throw new Error("Invalid broker memory write grant");
      }
      if (binding.libraryRead && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker library grant");
      }
      if (binding.workflowRead && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker workflow read grant");
      }
      if (binding.recordsRead && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker records grant");
      }
      if ((binding.interactionEvents || binding.cardEvents) && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker interaction grant");
      }
      if (binding.batchRead && (!binding.profile || binding.kind !== "work")) {
        throw new Error("Invalid broker batch grant");
      }
      if (binding.workflowRunId && binding.kind !== "workflow") {
        throw new Error("Invalid broker workflow identity");
      }
      if (binding.workflowOutput && (!binding.profile || binding.kind !== "workflow" || !binding.workflowRunId)) {
        throw new Error("Invalid broker workflow output grant");
      }
      if (binding.workflowAction && (binding.profile !== "harness-governed" || binding.kind !== "workflow" || !binding.workflowRunId)) {
        throw new Error("Invalid broker workflow action grant");
      }
      if (existing) {
        const saved = tokens.get(existing)!;
        if (
          (saved.profile || binding.profile) &&
          (saved.profile !== binding.profile || saved.orgId !== binding.orgId ||
            saved.kind !== binding.kind || saved.threadId !== binding.threadId || saved.operationLimit !== binding.operationLimit || saved.lookupRead !== binding.lookupRead || saved.memoryRead !== binding.memoryRead || saved.memoryWrite !== binding.memoryWrite || saved.libraryRead !== binding.libraryRead || saved.workflowRead !== binding.workflowRead || saved.recordsRead !== binding.recordsRead || saved.batchRead !== binding.batchRead || saved.workflowRunId !== binding.workflowRunId || saved.workflowOutput !== binding.workflowOutput || saved.workflowAction !== binding.workflowAction || saved.interactionEvents !== binding.interactionEvents || saved.cardEvents !== binding.cardEvents)
        ) {
          throw new Error("Broker capability binding conflicts with existing run");
        }
        return existing;
      }
      const token = randomUUID();
      tokens.set(token, { ...binding });
      byRun.set(binding.runId, token);
      return token;
    },
    release(runId) {
      const token = byRun.get(runId);
      if (token) {
        tokens.delete(token);
        byRun.delete(runId);
      }
    },
    async close() {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

let brokerSingleton: AgentBrokerHandle | undefined;
let brokerStarting: Promise<AgentBrokerHandle | undefined> | undefined;

/**
 * Lazily start the per-process agent broker, bound to the in-process control
 * plane. SEC9: OpenShell is the only agent runtime, so every control-plane
 * host runs a broker. Packaged sandboxes dial the container's private Compose
 * IP; host-mode setups use host.openshell.internal.
 * Idempotent — one broker per process, shared by runWorkRun (channel runs)
 * and the web chat route.
 */
export function ensureAgentBroker(): Promise<AgentBrokerHandle | undefined> {
  if (brokerSingleton) return Promise.resolve(brokerSingleton);
  if (!brokerStarting) {
    const pinned = Number(process.env.OPENNEKO_BROKER_PORT) || 0;
    brokerStarting = startAgentBroker({
      controlPlane: inProcessControlPlane,
      hostAlias: sandboxBrokerHost(),
      port: pinned || 4199,
      onEvents: routeBrokerEvents,
    })
      .catch((e: NodeJS.ErrnoException) => {
        // Unpinned default only: web + worker on one host (dev) both reach
        // for 4199 — fall back to an ephemeral port; sandboxes get the real
        // port via the handle URL. A pinned port must fail loudly because the
        // sandbox policy and Compose exposure both expect that exact port.
        if (pinned || e.code !== "EADDRINUSE") throw e;
        return startAgentBroker({
          controlPlane: inProcessControlPlane,
          hostAlias: sandboxBrokerHost(),
          port: 0,
          onEvents: routeBrokerEvents,
        });
      })
      .then((h) => {
        brokerSingleton = h;
        return h;
      })
      .catch((e) => {
        brokerStarting = undefined; // allow a retry on the next run
        throw e;
      });
  }
  return brokerStarting;
}

/**
 * Stop and forget the process-wide broker.
 *
 * Long-lived web/worker hosts normally keep it for their full lifetime. Short-
 * lived hosts such as the eval CLI must call this during teardown so the
 * broker's listening socket does not keep the Node process alive.
 */
export async function shutdownAgentBroker(): Promise<void> {
  const starting = brokerStarting;
  const active =
    brokerSingleton ??
    (starting ? await starting.catch(() => undefined) : undefined);
  brokerSingleton = undefined;
  brokerStarting = undefined;
  await active?.close();
}

export async function routeBrokerEvents(
  binding: RunBinding,
  events: AgentEvent[],
): Promise<void> {
  const sink = runEventSinks.get(binding.runId);
  if (sink) {
    for (const event of events) await sink(event);
    return;
  }

  // Job callers should register their scrubbed sink. Keep a durable fallback
  // for short-lived hosts or a process race so a successfully-created action
  // request never loses its inline approval event.
  if (!binding.threadId) {
    console.warn(
      `[agent-broker] dropped ${events.length} event(s) for ${binding.runId}: no event sink or thread binding`,
    );
    return;
  }
  const { appendWorkRunEvent } = await import("./store");
  for (const event of events) {
    await appendWorkRunEvent({
      orgId: binding.orgId,
      threadId: binding.threadId,
      runId: binding.runId,
      event,
    });
  }
}

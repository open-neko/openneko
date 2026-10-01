import { startupEvent, startupPhase, withStartupTrace } from "@neko/telemetry/startup";
import { heldItems, pool, resolveUserGroups } from "@neko/db";
import { filterHeldActions, runAllowedLibrary, runEntitlementActor, runHeldItemIds } from "../work/entitlement-scope";
import { listPackActionDescriptors } from "../work/pack-action-descriptors";
import { getWorkRunActor } from "../work/personas";
import { workflowTurnBudget, type AgentEvent } from "../agent-backend";
import type { HarnessObserver } from "@neko/telemetry";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import {
  knowledgePackPaths,
  readKnowledgePack,
} from "../knowledge-pack";
import { formatGlobalMemoryPromptContext as defaultFormatGlobalMemoryPromptContext } from "../work/memory";
import {
  createWorkRun,
  createWorkThread,
  finishWorkRun,
  markWorkRunRunning,
  saveAssistantWorkMessage,
  setWorkRunValue,
} from "../work/store";
import { ensureWorkWorkspace } from "../work/workspace";
import { inProcessControlPlane } from "../work/control-plane";
import { createAgentEventTelemetry } from "../work/agent-event-telemetry";
import {
  runWorkflowAgentBackend as defaultRunWorkflowAgentBackend,
} from "./agent-core";
import { extractValueFence } from "./fence-parsers";
import { clampAnalysisMinutes } from "./value";
import {
  buildWorkflowRunnerPrompt,
  type PluginActionPromptDescriptor,
} from "./runner-prompt";
import {
  createWorkflowRun,
  finishWorkflowRun,
  getWorkflow,
  getWorkflowRun,
  type WorkflowRecord,
  type WorkflowRunRecord,
} from "./store";
import { spendCapFromSignal } from "../spend/run-guard";
import { admitRunSpend, recordBudgetBlocked } from "../spend/admission";
import { recordAuditEvent } from "./audit-chain";

export class WorkflowNeedsInputError extends Error {
  constructor(message = "Workflow paused awaiting operator input") {
    super(message);
    this.name = "WorkflowNeedsInputError";
  }
}

export type WorkflowTriggerKind =
  | "manual"
  | "cron"
  | "subscription"
  | "watcher"
  | "api";

export type PrepareWorkflowRunOptions = {
  orgId: string;
  workflowId: string;
  triggerKind: WorkflowTriggerKind;
  triggerPayload?: Record<string, unknown>;
  threadId?: string;
  parentChainDepth?: number;
  triggeredBySubscriptionId?: string | null;
  triggeredByOutputId?: string | null;
  triggeredByObservationId?: string | null;
};

export type PreparedWorkflowRun = {
  workflow: WorkflowRecord;
  workflowRun: WorkflowRunRecord;
  threadId: string;
  workRunId: string;
};

type ClaimedDelivery =
  | { kind: "schedule"; id: string }
  | { kind: "source_change"; id: string };

async function resolveWorkflowPreparation(
  opts: PrepareWorkflowRunOptions,
  deps: Pick<Partial<RunWorkflowTurnDeps>, "resolveAgentBackend">,
) {
  const resolveAgentBackend = deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const workflow = await startupPhase("workflow.load", async () => getWorkflow(opts.orgId, opts.workflowId));
  if (!workflow) throw new Error(`Workflow ${opts.workflowId} not found for org ${opts.orgId}.`);
  if (!workflow.enabled) throw new Error(`Workflow ${workflow.name} is disabled.`);
  const backend = await startupPhase("config.backend", async () => resolveAgentBackend(opts.orgId));
  let actor: { userId: string | null; role: "admin" | "member" | "service" } = { userId: null, role: "service" };
  if (workflow.ownerUserId) {
    const owner = await pool().query<{ id: string }>("select id from app_user where org_id=$1 and id=$2 and disabled_at is null", [opts.orgId, workflow.ownerUserId]);
    if (!owner.rows[0]) throw new Error("The workflow owner is no longer active");
    const groups = await resolveUserGroups(opts.orgId, workflow.ownerUserId);
    actor = { userId: workflow.ownerUserId, role: groups.administrator ? "admin" : "member" };
  }
  return {workflow,backend,actor};
}

export async function prepareWorkflowRun(
  opts: PrepareWorkflowRunOptions,
  deps: Pick<Partial<RunWorkflowTurnDeps>, "resolveAgentBackend"> = {},
): Promise<PreparedWorkflowRun> {
  const {workflow,backend,actor}=await resolveWorkflowPreparation(opts,deps);
  // Trigger threads live on the "workflow" channel, never "web", so they can't
  // surface in the human Ask sidebar — even as an orphan whose work_run never
  // persisted (the sidebar lists only "web" threads).
  const threadId =
    opts.threadId ??
    (await startupPhase("workflow.create_thread", async () => createWorkThread(opts.orgId, workflow.name, "workflow"))).id;
  const created = await startupPhase("workflow.create_work_run", async () => createWorkRun(opts.orgId, threadId, backend.id, actor, {
    source: opts.triggerKind === "cron" ? "cron" : opts.triggerKind === "api" ? "api" : "trigger",
    workflowId: opts.workflowId,
  }));
  const workflowRun = await startupPhase("workflow.create_run", async () => createWorkflowRun({
    orgId: opts.orgId,
    workflowId: opts.workflowId,
    threadId,
    workRunId: created.id,
    triggerKind: opts.triggerKind,
    triggerPayload: opts.triggerPayload,
    chainDepth:
      (opts.parentChainDepth ?? 0) +
      (opts.triggerKind === "subscription" ? 1 : 0),
    triggeredBySubscriptionId: opts.triggeredBySubscriptionId,
    triggeredByOutputId: opts.triggeredByOutputId,
    triggeredByObservationId: opts.triggeredByObservationId,
  }));
  return {
    workflow,
    workflowRun,
    threadId,
    workRunId: created.id,
  };
}

/** A claimed cron or source-change delivery must never commit a run without
 * its delivery link. Otherwise a crash before the separate link write leaves
 * an orphan and a retry may prepare another run for the same occurrence. */
export async function prepareWorkflowRunForDelivery(
  opts: PrepareWorkflowRunOptions,
  delivery: ClaimedDelivery,
  deps: Pick<Partial<RunWorkflowTurnDeps>, "resolveAgentBackend"> = {},
): Promise<PreparedWorkflowRun> {
  if ((delivery.kind === "schedule" && opts.triggerKind !== "cron") ||
      (delivery.kind === "source_change" && opts.triggerKind !== "subscription")) {
    throw new Error("Claimed delivery does not match the workflow trigger");
  }
  const {workflow,backend,actor}=await resolveWorkflowPreparation(opts,deps);
  const client=await pool().connect();
  let released=false;
  let workRunId:string;
  let threadId:string;
  let workflowRunId:string;
  try {
    await client.query("BEGIN");
    if (opts.threadId) {
      const existing=await client.query<{id:string}>(
        "select id from work_thread where id=$1 and org_id=$2",[opts.threadId,opts.orgId]);
      if (!existing.rows[0]) throw new Error("Workflow thread is no longer available");
      threadId=opts.threadId;
    } else {
      const created=await client.query<{id:string}>(
        "insert into work_thread (org_id,title,channel) values ($1,$2,'workflow') returning id",
        [opts.orgId,workflow.name]);
      threadId=created.rows[0]!.id;
    }
    const work=await client.query<{id:string}>(
      `insert into work_run (org_id,thread_id,backend,status,actor_user_id,actor_role)
       values ($1,$2,$3,'queued',$4,$5) returning id`,
      [opts.orgId,threadId,backend.id,actor.userId,actor.role]);
    workRunId=work.rows[0]!.id;
    await admitRunSpend(client,{orgId:opts.orgId,workflowId:opts.workflowId,
      workRunId,source:delivery.kind==="schedule"?"cron":"trigger"});
    const created=await client.query<{id:string}>(
      `insert into workflow_run
         (org_id,workflow_id,thread_id,work_run_id,trigger_kind,trigger_payload,
          chain_depth,triggered_by_subscription_id,triggered_by_output_id,
          triggered_by_observation_id,status,started_at)
       values ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,'running',now()) returning id`,
      [opts.orgId,opts.workflowId,threadId,workRunId,opts.triggerKind,
        JSON.stringify(opts.triggerPayload??{}),
        (opts.parentChainDepth??0)+(opts.triggerKind==="subscription"?1:0),
        opts.triggeredBySubscriptionId??null,opts.triggeredByOutputId??null,
        opts.triggeredByObservationId??null]);
    workflowRunId=created.rows[0]!.id;
    const table=delivery.kind==="schedule"?"workflow_schedule_firing":"source_change_delivery";
    const currentRevision=delivery.kind==="schedule"
      ? `exists (select 1 from workflow_schedule_state state
          where state.workflow_id=$4
            and ${table}.definition_updated_at=state.definition_updated_at
            and date_trunc('milliseconds',state.definition_updated_at)=$5::timestamptz)`
      : `exists (select 1 from subscription sub
          where sub.id=${table}.subscription_id and sub.enabled=true
            and sub.updated_at=${table}.subscription_updated_at
            and date_trunc('milliseconds',${table}.definition_updated_at)=$5::timestamptz)`;
    const linked=await client.query(
      `update ${table} set workflow_run_id=$2,updated_at=now()
       where id=$1 and org_id=$3 and workflow_id=$4 and status='running'
         and workflow_run_id is null and ${currentRevision}
         and exists (select 1 from workflow_definition workflow
           where workflow.id=$4 and workflow.org_id=$3 and workflow.enabled=true
             and date_trunc('milliseconds',workflow.updated_at)=$5::timestamptz)
       returning id`,
      [delivery.id,workflowRunId,opts.orgId,opts.workflowId,workflow.updatedAt]);
    if (linked.rowCount!==1) throw new Error("Claimed workflow delivery can no longer be linked");
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK").catch(()=>undefined);
    client.release();
    released=true;
    await recordBudgetBlocked(error);
    throw error;
  } finally {
    if (!released) client.release();
  }
  await recordAuditEvent({orgId:opts.orgId,entityKind:"work_run",entityId:workRunId,
    event:"run:created",payload:{backend:backend.id,actorUserId:actor.userId,actorRole:actor.role}});
  const workflowRun=await getWorkflowRun(opts.orgId,workflowRunId);
  if (!workflowRun) throw new Error("Committed workflow run could not be loaded");
  return {workflow,workflowRun,threadId,workRunId};
}

/** Load the linked rows created transactionally by external API admission. */
export async function loadPreparedWorkflowRun(input: {
  orgId: string;
  workflowId: string;
  workflowRunId: string;
}): Promise<PreparedWorkflowRun> {
  const [workflow, workflowRun] = await Promise.all([
    getWorkflow(input.orgId, input.workflowId),
    getWorkflowRun(input.orgId, input.workflowRunId),
  ]);
  if (
    !workflow ||
    !workflowRun ||
    workflowRun.workflowId !== input.workflowId ||
    workflowRun.triggerKind !== "api"
  ) {
    throw new Error("Admitted workflow API run could not be loaded.");
  }
  return {
    workflow,
    workflowRun,
    threadId: workflowRun.threadId,
    workRunId: workflowRun.workRunId,
  };
}

/** Reload the exact linked run after a pre-model worker crash. The caller has
 * already reclaimed an expired delivery lease; a running work_run is never
 * eligible because it may have invoked the model. */
export async function loadQueuedPreparedWorkflowRun(input: {
  orgId:string;workflowId:string;workflowRunId:string;
  triggerKind:"cron"|"subscription";
}):Promise<PreparedWorkflowRun> {
  const {workflow}=await resolveWorkflowPreparation({
    orgId:input.orgId,workflowId:input.workflowId,triggerKind:input.triggerKind,
  },{});
  const workflowRun=await getWorkflowRun(input.orgId,input.workflowRunId);
  if(!workflowRun || workflowRun.workflowId!==input.workflowId ||
      workflowRun.triggerKind!==input.triggerKind || workflowRun.status!=="running") {
    throw new Error("Linked queued workflow run could not be loaded");
  }
  const [work]=(await pool().query<{status:string}>(
    "select status from work_run where id=$1 and org_id=$2 and thread_id=$3",
    [workflowRun.workRunId,input.orgId,workflowRun.threadId])).rows;
  if(work?.status!=="queued")throw new Error("Linked workflow work run is no longer queued");
  return {workflow,workflowRun,threadId:workflowRun.threadId,workRunId:workflowRun.workRunId};
}

export type RunWorkflowTurnOptions = {
  prepared: PreparedWorkflowRun;
  /** Queue-delivered triggers must still match their admitted revision and
   * win one queued-to-running transition before any model call. */
  queuedDelivery?: ClaimedDelivery;
  /** Efficacy evals disable time-saved metadata. Defaults to true. */
  includeUxMetadata?: boolean;
  userMessage?: string;
  mode: "live" | "headless";
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
  /** A hard runtime ceiling for the whole run (API runs); disables continuations. */
  timeoutMs?: number;
  /** Tool-call ceiling for the run (API runs). */
  maxToolIterations?: number;
  /** Trusted API admission ceiling; the sandbox applies the lower Harness cap. */
  maxModelCalls?: number;
  maxModelTokens?: number;
  maxCostMicros?: number;
  /** Metadata-only observation stream shared with Ask. */
  observer?: HarnessObserver;
  /**
   * Installed plugin action kinds, so the runner agent proposes real kinds
   * (e.g. send_slack_dm) that policy rules + adapters match — not a generic
   * send_message that stalls at pending_approval. The fire job passes its
   * registry snapshot; tests may omit it.
   */
  pluginActions?: readonly PluginActionPromptDescriptor[];
};

export type RunWorkflowTurnDeps = {
  resolveAgentBackend: typeof defaultResolveAgentBackend;
  formatGlobalMemoryPromptContext: typeof defaultFormatGlobalMemoryPromptContext;
  runCore: typeof defaultRunWorkflowAgentBackend;
};

export type RunWorkflowTurnResult = {
  status:
    | "completed"
    | "failed"
    | "cancelled"
    | "needs_input";
  workflowRunId: string;
  workRunId: string;
  threadId: string;
  finalText: string;
  error?: string;
};

function synthesizeSeedMessage(
  workflow: WorkflowRecord,
  triggerKind: WorkflowTriggerKind,
  userMessage: string | undefined,
): string {
  if (userMessage?.trim()) return userMessage;
  if (triggerKind === "cron") {
    return `[scheduled run started at ${new Date().toISOString()}] Begin executing the "${workflow.name}" workflow.`;
  }
  if (triggerKind === "subscription") {
    return `[subscription-triggered run started at ${new Date().toISOString()}] Begin executing the "${workflow.name}" workflow.`;
  }
  if (triggerKind === "api") {
    return `Begin executing the externally admitted "${workflow.name}" workflow with the supplied API input.`;
  }
  return `Begin executing the "${workflow.name}" workflow.`;
}

export function runWorkflowTurn(opts: RunWorkflowTurnOptions, deps: Partial<RunWorkflowTurnDeps> = {}): Promise<RunWorkflowTurnResult> {
  return withStartupTrace({ runId: opts.prepared.workRunId, threadId: opts.prepared.threadId, workflowRunId: opts.prepared.workflowRun.id, rootOperationId: `workflow:${opts.prepared.workRunId}`, observer: opts.observer }, () => runWorkflowTurnTraced(opts, deps));
}

async function runWorkflowTurnTraced(
  opts: RunWorkflowTurnOptions,
  deps: Partial<RunWorkflowTurnDeps> = {},
): Promise<RunWorkflowTurnResult> {
  const { prepared, userMessage, mode, emit, signal } = opts;
  const { workflow, workflowRun, threadId, workRunId } = prepared;
  const orgId = workflow.orgId;
  const triggerKind = workflowRun.triggerKind;

  const resolveAgentBackend =
    deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const formatGlobalMemoryPromptContext =
    deps.formatGlobalMemoryPromptContext ?? defaultFormatGlobalMemoryPromptContext;
  const runCore = deps.runCore ?? defaultRunWorkflowAgentBackend;

  const backend = await startupPhase("config.backend", async () => resolveAgentBackend(orgId));
  await startupPhase("run.bind_backend", async () => {
    const bound = await pool().query("UPDATE work_run SET backend=$3,updated_at=now() WHERE org_id=$1 AND id=$2 AND status='queued' RETURNING id",
      [orgId,workRunId,backend.id]);
    if (bound.rowCount) return;
    const current = await pool().query("SELECT backend,status FROM work_run WHERE org_id=$1 AND id=$2",[orgId,workRunId]);
    if (current.rows[0]?.status !== "running" || current.rows[0]?.backend !== backend.id) {
      throw new Error("Workflow backend binding changed");
    }
  });
  await startupPhase("run.mark_running", async () => {
    if (!opts.queuedDelivery) return markWorkRunRunning(workRunId);
    const delivery=opts.queuedDelivery;
    const revisionGuard=delivery.kind==="schedule"
      ? `exists (select 1 from workflow_schedule_firing firing
          join workflow_schedule_state state on state.workflow_id=firing.workflow_id
          join workflow_definition workflow on workflow.id=firing.workflow_id
          where firing.id=$3 and firing.workflow_run_id=$4 and firing.status='running'
            and firing.definition_updated_at=state.definition_updated_at
            and workflow.updated_at=state.definition_updated_at
            and workflow.enabled=true and workflow.cron_enabled=true
            and workflow.cron=state.cron and workflow.cron_timezone=state.cron_timezone)`
      : `exists (select 1 from source_change_delivery delivery
          join subscription sub on sub.id=delivery.subscription_id
          join workflow_definition workflow on workflow.id=delivery.workflow_id
          where delivery.id=$3 and delivery.workflow_run_id=$4 and delivery.status='running'
            and sub.enabled=true and workflow.enabled=true
            and sub.updated_at=delivery.subscription_updated_at
            and workflow.updated_at=delivery.definition_updated_at)`;
    const claimed=await pool().query(
      `update work_run set status='running',updated_at=now()
       where id=$1 and org_id=$2 and status='queued' and ${revisionGuard}
       returning id`,
      [workRunId,orgId,delivery.id,workflowRun.id]);
    if(claimed.rowCount!==1)throw new Error("Workflow delivery changed or work run already started");
  });

  let assistantText = "";
  let needsInput = false;
  const eventTelemetry = createAgentEventTelemetry({
    observer: opts.observer,
    operationId: `workflow:${workRunId}`,
  });
  const wrappedEmit = async (event: AgentEvent): Promise<void> => {
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
    }
    if (event.type === "needs_input") {
      needsInput = true;
    }
    await eventTelemetry.observeEvent(event);
    await emit(event);
  };

  const workspace = await startupPhase("workspace.prepare", async () => ensureWorkWorkspace(orgId, threadId, workRunId));

  try {
    if (!backend.capabilities.mcpTools && backend.id !== "harness") {
      throw new Error(
        "Workflow data access requires the native GraphJin broker tool; this backend does not support MCP tools.",
      );
    }
    await wrappedEmit({
      type: "status",
      message: `Starting workflow "${workflow.name}" (${triggerKind})…`,
    });

    const runActor = await runEntitlementActor(orgId, await getWorkRunActor(workRunId), { workflowId: workflow.id });
    const memoryContext = await startupPhase("context.memory", async () =>
      formatGlobalMemoryPromptContext(orgId, 5, await heldItems(runActor, "team_memory")));

    const knowledge = await startupPhase("knowledge.read_pack", async () => readKnowledgePack(
      knowledgePackPaths(workspace.knowledgeRoot),
    ));
    const packActions = backend.id === "harness"
      ? (await filterHeldActions(runActor, await listPackActionDescriptors(orgId, { forHarness: true })))
          .sort((a, b) => a.kind.localeCompare(b.kind)).slice(0, 64)
      : [];

    const prompt = buildWorkflowRunnerPrompt({
      workflow,
      includeUxMetadata: opts.includeUxMetadata,
      mode,
      memoryContext,
      mcpTools: backend.capabilities.mcpTools,
      backend: backend.id,
      workspace,
      knowledge,
      pluginActions: await filterHeldActions(runActor, opts.pluginActions ?? []),
      packActions,
    });

    const seedMessage = synthesizeSeedMessage(
      workflow,
      triggerKind,
      userMessage,
    );

    await eventTelemetry.startAgent({
      backend: backend.id,
      ...(backend.model ? { model: backend.model } : {}),
      inputBytes: Buffer.byteLength(`${prompt}\n\n${seedMessage}`, "utf8"),
    });
    const [allowedSkills, allowedLibrary] = await startupPhase("identity.entitlements", () =>
      Promise.all([runHeldItemIds(runActor, "skill"), runAllowedLibrary(runActor)]));
    const budget = workflowTurnBudget();
    const coreInput = {
      ...(allowedSkills ? { allowedSkills } : {}),
      ...(allowedLibrary ? { allowedLibrary } : {}),
      backend,
      prompt,
      userMessage: seedMessage,
      orgId,
      threadId,
      runId: workRunId,
      workflowRunId: workflowRun.id,
      packActions,
      mode,
      networkHosts: workflow.networkHosts,
      triggeredByObservationId:
        workflowRun.triggeredByObservationId ?? null,
      workspace,
      controlPlane: inProcessControlPlane,
      emit: wrappedEmit,
      tag: `workflow ${workflow.name} ${workflowRun.id}`,
      signal,
      steps: workflow.steps,
      input: workflowRun.triggerPayload,
      timeoutMs: opts.timeoutMs ?? budget.timeoutMs,
      reasoningEffort: budget.reasoningEffort,
      maxToolIterations: opts.maxToolIterations ?? budget.maxToolIterations,
      maxContinuations: opts.timeoutMs ? 0 : budget.maxContinuations,
      maxModelCalls: opts.maxModelCalls,
      maxModelTokens: opts.maxModelTokens,
      maxCostMicros: opts.maxCostMicros,
    };
    let coreResult;
    try {
      coreResult = await runCore(coreInput);
    } catch (error) {
      if (backend.id !== "harness" || signal?.aborted || spendCapFromSignal(signal)) throw error;
      // Re-enter only the same run's launcher. Its persisted admission and
      // checkpoint inspector either adopt a terminal receipt, resume a fully
      // reconciled attempt, or deny dispatch when the prior outcome is unknown.
      // The workflow remains running across this one bounded recovery attempt.
      startupEvent("workflow.harness_reconcile_retry", {runId:workRunId});
      coreResult = await runCore(coreInput);
    }
    const spendStop = spendCapFromSignal(signal);
    let result = spendStop
      ? { ...coreResult, status: "failed" as const, error: spendStop.message }
      : coreResult;
    if (backend.id === "harness") {
      const outputs = await pool().query<{ output_id: string; kind: string; emitted: boolean }>(`SELECT
          h.result->>'outputId' AS output_id, h.result->>'kind' AS kind,
          EXISTS (SELECT 1 FROM work_run_event e WHERE e.org_id=h.org_id AND e.run_id::text=h.run_id
            AND e.kind='output_emit' AND e.payload->>'output_id'=h.result->>'outputId') AS emitted
        FROM harness_operation h WHERE h.org_id=$1 AND h.run_id=$2
          AND h.request->>'tool'='workflow_output' AND h.result->>'ok'='true'
        ORDER BY h.operation_id`, [orgId, workRunId]);
      for (const output of outputs.rows) {
        if (!output.emitted) await wrappedEmit({ type: "output_emit", output_id: output.output_id, kind: output.kind });
      }
      if (result.status === "completed" && outputs.rowCount === 0) {
        result = { ...result, status: "failed", error: "Harness workflow completed without a recorded output" };
      }
    }
    await eventTelemetry.finishAgent({
      status: result.status === "completed" ? "ok" : "error",
      ...(result.error ? { errorType: "agent_backend_error" } : {}),
      outputBytes: Buffer.byteLength(result.finalText, "utf8"),
      ...(result.backendState?.harness && typeof result.backendState.harness === "object" &&
        "cost" in result.backendState.harness && result.backendState.harness.cost &&
        typeof result.backendState.harness.cost === "object"
        ? {cost: {type: "cost" as const, source: "harness" as const,
          ...(result.backendState.harness.cost as {chargedMicros:number;budgetMicros:number;pricingVersion:string})}} : {}),
    });

    if (needsInput) {
      throw new WorkflowNeedsInputError();
    }

    let persistedText = result.finalText.trim() || assistantText.trim();

    // Per-run analysis value estimate (works for both backends — the
    // `neko_value` fence rides in the agent's final text). Parse, clamp,
    // strip from the persisted text so it never shows in the summary.
    const valueFence = extractValueFence(persistedText);
    persistedText = valueFence.text;
    const analysisMinutes = clampAnalysisMinutes(valueFence.payload?.minutes_saved);

    await finishWorkRun(workRunId, result.status, result.error ?? null);
    if (valueFence.payload) {
      try {
        await setWorkRunValue(workRunId, {
          minutes: analysisMinutes,
          basis: valueFence.payload.basis ?? null,
        });
      } catch (err) {
        console.warn(
          `[workflow-run] setWorkRunValue failed: ${err instanceof Error ? err.message : err}`,
        );
      }
    }
    const summary =
      persistedText.slice(0, 4000) ||
      (result.status === "completed"
        ? "Looked at the data; nothing to flag."
        : null);
    await startupPhase("workflow.persist_result", async () => finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status: result.status,
      summary,
      error: result.error ?? null,
    }));

    if (persistedText) {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId: workRunId,
        content: persistedText,
      });
    }

    await wrappedEmit({
      type: "done",
      result: { status: result.status, minutesSaved: analysisMinutes ?? 0 },
    });

    return {
      status: result.status,
      workflowRunId: workflowRun.id,
      workRunId: workRunId,
      threadId,
      finalText: persistedText,
      error: result.error,
    };
  } catch (error) {
    if (error instanceof WorkflowNeedsInputError || needsInput) {
      await eventTelemetry.closeOpen({
        status: "ok",
        outcome: "needs_input",
        usageMissingReason: "workflow paused for operator input",
      });
      await finishWorkRun(workRunId, "failed", null);
      await startupPhase("workflow.persist_result", async () => finishWorkflowRun({
        workflowRunId: workflowRun.id,
        status: "needs_input",
        summary: assistantText.slice(0, 4000) || null,
        error: null,
      }));
      await wrappedEmit({ type: "done", result: { status: "needs_input" } });
      return {
        status: "needs_input",
        workflowRunId: workflowRun.id,
        workRunId: workRunId,
        threadId,
        finalText: assistantText,
      };
    }

    const spendStop = spendCapFromSignal(signal);
    const aborted =
      !spendStop &&
      (signal?.aborted ||
        (error instanceof Error &&
          (error.name === "AbortError" || error.message.includes("aborted"))));
    const status: "failed" | "cancelled" = aborted ? "cancelled" : "failed";
    const errMsg = spendStop
      ? spendStop.message
      : aborted
        ? "Cancelled by user."
        : error instanceof Error
          ? error.message
          : "Workflow run failed unexpectedly.";

    await eventTelemetry.closeOpen({
      status: "error",
      outcome: status,
      errorType: aborted ? "cancelled" : "agent_backend_error",
      usageMissingReason: "workflow model call did not complete with normalized usage",
    });

    await wrappedEmit({ type: "error", message: errMsg });
    await finishWorkRun(workRunId, status, aborted ? null : errMsg);
    await startupPhase("workflow.persist_result", async () => finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status,
      summary: assistantText.slice(0, 4000) || null,
      error: aborted ? null : errMsg,
    }));
    await wrappedEmit({ type: "done", result: { status } });
    if (!aborted && !spendStop) throw error;
    return {
      status,
      ...(spendStop ? { error: errMsg } : {}),
      workflowRunId: workflowRun.id,
      workRunId: workRunId,
      threadId,
      finalText: assistantText,
    };
  }
}

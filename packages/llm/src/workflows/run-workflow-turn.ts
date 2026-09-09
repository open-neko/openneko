import type { AgentEvent } from "../agent-backend";
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

export async function prepareWorkflowRun(
  opts: PrepareWorkflowRunOptions,
  deps: Pick<Partial<RunWorkflowTurnDeps>, "resolveAgentBackend"> = {},
): Promise<PreparedWorkflowRun> {
  const resolveAgentBackend =
    deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const workflow = await getWorkflow(opts.orgId, opts.workflowId);
  if (!workflow) {
    throw new Error(`Workflow ${opts.workflowId} not found for org ${opts.orgId}.`);
  }
  if (!workflow.enabled) {
    throw new Error(`Workflow ${workflow.name} is disabled.`);
  }
  const backend = await resolveAgentBackend(opts.orgId);
  // Trigger threads live on the "workflow" channel, never "web", so they can't
  // surface in the human Ask sidebar — even as an orphan whose work_run never
  // persisted (the sidebar lists only "web" threads).
  const threadId =
    opts.threadId ??
    (await createWorkThread(opts.orgId, workflow.name, "workflow")).id;
  const created = await createWorkRun(opts.orgId, threadId, backend.id, {
    userId: null,
    role: "service",
  });
  const workflowRun = await createWorkflowRun({
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
  });
  return {
    workflow,
    workflowRun,
    threadId,
    workRunId: created.id,
  };
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

export type RunWorkflowTurnOptions = {
  prepared: PreparedWorkflowRun;
  userMessage?: string;
  mode: "live" | "headless";
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
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

export async function runWorkflowTurn(
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

  const backend = await resolveAgentBackend(orgId);
  await markWorkRunRunning(workRunId);

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

  const workspace = await ensureWorkWorkspace(orgId, threadId, workRunId);

  try {
    if (!backend.capabilities.mcpTools) {
      throw new Error(
        "Workflow data access requires the native GraphJin broker tool; this backend does not support MCP tools.",
      );
    }
    await wrappedEmit({
      type: "status",
      message: `Starting workflow "${workflow.name}" (${triggerKind})…`,
    });

    const memoryContext = await formatGlobalMemoryPromptContext(orgId);

    const knowledge = await readKnowledgePack(
      knowledgePackPaths(workspace.knowledgeRoot),
    );

    const prompt = buildWorkflowRunnerPrompt({
      workflow,
      mode,
      memoryContext,
      mcpTools: backend.capabilities.mcpTools,
      backend: backend.id,
      workspace,
      knowledge,
      pluginActions: opts.pluginActions ?? [],
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
    const result = await runCore({
      backend,
      prompt,
      userMessage: seedMessage,
      orgId,
      threadId,
      runId: workRunId,
      workflowRunId: workflowRun.id,
      mode,
      triggeredByObservationId:
        workflowRun.triggeredByObservationId ?? null,
      workspace,
      controlPlane: inProcessControlPlane,
      emit: wrappedEmit,
      tag: `workflow ${workflow.name} ${workflowRun.id}`,
      signal,
    });
    await eventTelemetry.finishAgent({
      status: result.status === "completed" ? "ok" : "error",
      ...(result.error ? { errorType: "agent_backend_error" } : {}),
      outputBytes: Buffer.byteLength(result.finalText, "utf8"),
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
    await finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status: result.status,
      summary,
      error: result.error ?? null,
    });

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
      await finishWorkflowRun({
        workflowRunId: workflowRun.id,
        status: "needs_input",
        summary: assistantText.slice(0, 4000) || null,
        error: null,
      });
      await wrappedEmit({ type: "done", result: { status: "needs_input" } });
      return {
        status: "needs_input",
        workflowRunId: workflowRun.id,
        workRunId: workRunId,
        threadId,
        finalText: assistantText,
      };
    }

    const aborted =
      signal?.aborted ||
      (error instanceof Error &&
        (error.name === "AbortError" || error.message.includes("aborted")));
    const status: "failed" | "cancelled" = aborted ? "cancelled" : "failed";
    const errMsg = aborted
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
    await finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status,
      summary: assistantText.slice(0, 4000) || null,
      error: aborted ? null : errMsg,
    });
    await wrappedEmit({ type: "done", result: { status } });
    if (!aborted) throw error;
    return {
      status,
      workflowRunId: workflowRun.id,
      workRunId: workRunId,
      threadId,
      finalText: assistantText,
    };
  }
}

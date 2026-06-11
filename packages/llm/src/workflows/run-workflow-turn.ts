import type { AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import {
  knowledgePackPaths,
  readKnowledgePack,
} from "../knowledge-pack";
import {
  ensureGraphjinGuard,
  resolveBinaryOnPath,
} from "../work/graphjin-guard";
import { ensureGraphjinGuardWithActorAuth } from "../work/graphjin-actor-guard";
import { formatGlobalMemoryPromptContext as defaultFormatGlobalMemoryPromptContext } from "../work/memory";
import {
  createWorkRun,
  createWorkThread,
  finishWorkRun,
  markWorkRunRunning,
  saveAssistantWorkMessage,
  setWorkRunValue,
} from "../work/store";
import { buildWorkMemoryServer } from "../work/tools";
import { ensureWorkWorkspace } from "../work/workspace";
import {
  buildWorkflowActionServer,
  handleActionRequest,
} from "./action-server";
import {
  extractActionRequestFences,
  extractValueFence,
  extractWorkflowOutputFences,
} from "./fence-parsers";
import { clampAnalysisMinutes } from "./value";
import {
  buildWorkflowOutputServer,
  handleWorkflowOutput,
} from "./output-server";
import {
  buildWorkflowRunnerPrompt,
  type PluginActionPromptDescriptor,
} from "./runner-prompt";
import {
  createWorkflowRun,
  finishWorkflowRun,
  getWorkflow,
  type WorkflowRecord,
  type WorkflowRunRecord,
} from "./store";
import {
  WORKFLOW_FIXED_DENY,
  WORKFLOW_RUNNER_DEFAULT_ALLOWED_TOOLS,
  buildAllowDenyGate,
} from "./tool-defaults";

export class WorkflowNeedsInputError extends Error {
  constructor(message = "Workflow paused awaiting operator input") {
    super(message);
    this.name = "WorkflowNeedsInputError";
  }
}

export type WorkflowTriggerKind = "manual" | "cron" | "subscription" | "watcher";

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

export type RunWorkflowTurnOptions = {
  prepared: PreparedWorkflowRun;
  userMessage?: string;
  mode: "live" | "headless";
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
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

  const backend = await resolveAgentBackend(orgId);
  await markWorkRunRunning(workRunId);

  let assistantText = "";
  const wrappedEmit = async (event: AgentEvent): Promise<void> => {
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
    }
    await emit(event);
  };

  let needsInput = false;
  const headlessElicitation = async (): Promise<Record<string, unknown>> => {
    needsInput = true;
    await wrappedEmit({
      type: "needs_input",
      question: "Workflow paused awaiting operator input.",
    });
    throw new WorkflowNeedsInputError();
  };

  const workspace = await ensureWorkWorkspace(orgId, threadId, workRunId);
  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    const errMsg = "graphjin CLI is not installed on PATH.";
    await emit({ type: "error", message: errMsg });
    await finishWorkRun(workRunId, "failed", errMsg);
    await finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status: "failed",
      error: errMsg,
    });
    await emit({ type: "done", result: { status: "failed" } });
    throw new Error(errMsg);
  }
  await ensureGraphjinGuardWithActorAuth({
    orgId,
    graphjinBinary,
    binRoot: workspace.binRoot,
    runRoot: workspace.runRoot,
    actor: { userId: null, role: "service" },
  });

  try {
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

    const mcpServers = backend.capabilities.mcpTools
      ? {
          neko_workflow_output: buildWorkflowOutputServer({
            orgId,
            workflowRunId: workflowRun.id,
            workRunId: workRunId,
            emit: wrappedEmit,
          }),
          neko_action: buildWorkflowActionServer({
            orgId,
            workflowRunId: workflowRun.id,
            workRunId: workRunId,
            triggeredByObservationId:
              workflowRun.triggeredByObservationId ?? null,
            emit: wrappedEmit,
          }),
          neko_memory: buildWorkMemoryServer({
            orgId,
            threadId,
            runId: workRunId,
          }),
        }
      : undefined;

    const canUseTool = backend.capabilities.canUseToolGate
      ? buildAllowDenyGate(
          WORKFLOW_RUNNER_DEFAULT_ALLOWED_TOOLS,
          WORKFLOW_FIXED_DENY,
        )
      : undefined;

    const result = await backend.run({
      prompt,
      userMessage: seedMessage,
      orgId,
      workspace,
      onEvent: wrappedEmit,
      mcpServers,
      canUseTool,
      allowedTools: backend.capabilities.canUseToolGate
        ? WORKFLOW_RUNNER_DEFAULT_ALLOWED_TOOLS
        : undefined,
      onElicitation: mode === "headless" ? headlessElicitation : undefined,
      tag: `workflow ${workflow.name} ${workflowRun.id}`,
      signal,
    });

    let persistedText = result.finalText.trim() || assistantText.trim();

    if (!backend.capabilities.mcpTools && persistedText) {
      persistedText = await processWorkflowFences({
        text: persistedText,
        outputCtx: {
          orgId,
          workflowRunId: workflowRun.id,
          workRunId,
          emit: wrappedEmit,
        },
        actionCtx: {
          orgId,
          workflowRunId: workflowRun.id,
          workRunId,
          triggeredByObservationId:
            workflowRun.triggeredByObservationId ?? null,
          emit: wrappedEmit,
        },
        onParseError: (msg) => wrappedEmit({ type: "error", message: msg }),
      });
    }

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

type ProcessWorkflowFencesInput = {
  text: string;
  outputCtx: Parameters<typeof handleWorkflowOutput>[0];
  actionCtx: Parameters<typeof handleActionRequest>[0];
  onParseError: (message: string) => Promise<void> | void;
};

async function processWorkflowFences(
  input: ProcessWorkflowFencesInput,
): Promise<string> {
  const { outputCtx, actionCtx, onParseError } = input;
  let text = input.text;

  const outputs = extractWorkflowOutputFences(text);
  text = outputs.text;
  for (const payload of outputs.payloads) {
    await handleWorkflowOutput(outputCtx, payload);
  }
  if (outputs.errors.length > 0) {
    await onParseError(
      `workflow output fence(s) invalid: ${outputs.errors
        .map((e) => e.reason)
        .join("; ")}`,
    );
  }

  const actions = extractActionRequestFences(text);
  text = actions.text;
  for (const payload of actions.payloads) {
    await handleActionRequest(actionCtx, payload);
  }
  if (actions.errors.length > 0) {
    await onParseError(
      `action request fence(s) invalid: ${actions.errors
        .map((e) => e.reason)
        .join("; ")}`,
    );
  }

  return text;
}

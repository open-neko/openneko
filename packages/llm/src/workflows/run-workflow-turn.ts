import type { AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import { formatWorkMemoryPromptContext as defaultFormatWorkMemoryPromptContext } from "../work/memory";
import {
  createWorkRun,
  createWorkThread,
  finishWorkRun,
  markWorkRunRunning,
  saveAssistantWorkMessage,
} from "../work/store";
import { buildWorkMemoryServer } from "../work/tools";
import {
  buildWorkflowActionServer,
  handleActionRequest,
} from "./action-server";
import {
  extractActionRequestFences,
  extractWorkflowOutputFences,
} from "./fence-parsers";
import {
  buildWorkflowOutputServer,
  handleWorkflowOutput,
} from "./output-server";
import { buildWorkflowRunnerPrompt } from "./runner-prompt";
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

export type WorkflowTriggerKind = "manual" | "cron" | "subscription";

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
  const threadId =
    opts.threadId ??
    (await createWorkThread(opts.orgId, workflow.name)).id;
  const created = await createWorkRun(opts.orgId, threadId, backend.id);
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
};

export type RunWorkflowTurnDeps = {
  resolveAgentBackend: typeof defaultResolveAgentBackend;
  formatWorkMemoryPromptContext: typeof defaultFormatWorkMemoryPromptContext;
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
  const formatWorkMemoryPromptContext =
    deps.formatWorkMemoryPromptContext ?? defaultFormatWorkMemoryPromptContext;

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

  try {
    await wrappedEmit({
      type: "status",
      message: `Starting workflow "${workflow.name}" (${triggerKind})…`,
    });

    const memoryContext = await formatWorkMemoryPromptContext({
      orgId,
      threadId,
      runId: workRunId,
    });

    const prompt = buildWorkflowRunnerPrompt({
      workflow,
      mode,
      memoryContext,
      mcpTools: backend.capabilities.mcpTools,
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
      onEvent: wrappedEmit,
      mcpServers,
      canUseTool,
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

    await finishWorkRun(workRunId, result.status, result.error ?? null);
    await finishWorkflowRun({
      workflowRunId: workflowRun.id,
      status: result.status,
      summary: persistedText.slice(0, 4000) || null,
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

    await wrappedEmit({ type: "done", result: { status: result.status } });

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

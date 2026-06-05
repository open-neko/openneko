import type { WorkflowRunFirePayload } from "@neko/db/jobs";
import { type AgentEvent, registerAgentCanceller } from "@neko/llm";
import { appendWorkRunEvent, scrubAgentEvent } from "@neko/llm/work";
import { prepareWorkflowRun, runWorkflowTurn } from "@neko/llm/workflows";
import {
  getCurrentScrubber,
  getPluginRegistryInstance,
} from "../plugins/registry-instance.js";

// Thrown when worker shutdown cuts a headless run short, so the pg-boss handler
// fails the job and a later worker retries it.
export class WorkflowRunInterrupted extends Error {
  constructor() {
    super("Workflow run interrupted by worker shutdown");
    this.name = "WorkflowRunInterrupted";
  }
}

export async function runWorkflowRunFire(
  payload: WorkflowRunFirePayload,
): Promise<void> {
  const prepared = await prepareWorkflowRun({
    orgId: payload.orgId,
    workflowId: payload.workflowId,
    triggerKind: payload.triggerKind,
    triggerPayload: payload.triggerPayload,
    threadId: payload.threadId,
    parentChainDepth: payload.parentChainDepth,
    triggeredBySubscriptionId: payload.triggeredBySubscriptionId,
    triggeredByOutputId: payload.triggeredByOutputId,
    triggeredByObservationId: payload.triggeredByObservationId,
  });

  // Scrubber snapshot per fire — see work-run.ts for the
  // mid-run-rotation caveat.
  const scrubber = getCurrentScrubber();
  const emit = async (event: AgentEvent): Promise<void> => {
    await appendWorkRunEvent({
      orgId: payload.orgId,
      threadId: prepared.threadId,
      runId: prepared.workRunId,
      event: scrubAgentEvent(scrubber, event),
    });
  };

  const pluginActions =
    getPluginRegistryInstance()?.getRegisteredActionDescriptors() ?? [];

  // SIGTERM aborts this signal, so an interrupted run finalizes as "cancelled"
  // (not a hard failure with an opaque "ACP client disposed" error).
  const abort = new AbortController();
  const unregister = registerAgentCanceller(() => abort.abort());
  let result;
  try {
    result = await runWorkflowTurn({
      prepared,
      userMessage: payload.userMessage,
      mode: "headless",
      emit,
      signal: abort.signal,
      pluginActions,
    });
  } finally {
    unregister();
  }

  if (result.status === "cancelled") throw new WorkflowRunInterrupted();
}

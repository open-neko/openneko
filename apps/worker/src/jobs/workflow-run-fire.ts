import type { WorkflowRunFirePayload } from "@neko/db/jobs";
import type { AgentEvent } from "@neko/llm";
import { appendWorkRunEvent, scrubAgentEvent } from "@neko/llm/work";
import { prepareWorkflowRun, runWorkflowTurn } from "@neko/llm/workflows";
import {
  getCurrentScrubber,
  getPluginRegistryInstance,
} from "../plugins/registry-instance.js";

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

  await runWorkflowTurn({
    prepared,
    userMessage: payload.userMessage,
    mode: "headless",
    emit,
    pluginActions,
  });
}

import type { WorkflowRunFirePayload } from "@neko/db/jobs";
import type { AgentEvent } from "@neko/llm";
import { appendWorkRunEvent, scrubAgentEvent } from "@neko/llm/work";
import { prepareWorkflowRun, runWorkflowTurn } from "@neko/llm/workflows";
import { getCurrentScrubber } from "../plugins/registry-instance.js";

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

  let seq = 0;
  // Scrubber snapshot per fire — see work-run.ts for the
  // mid-run-rotation caveat.
  const scrubber = getCurrentScrubber();
  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({
      orgId: payload.orgId,
      threadId: prepared.threadId,
      runId: prepared.workRunId,
      seq,
      event: scrubAgentEvent(scrubber, event),
    });
  };

  await runWorkflowTurn({
    prepared,
    userMessage: payload.userMessage,
    mode: "headless",
    emit,
  });
}

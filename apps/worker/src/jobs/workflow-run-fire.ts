import type { WorkflowRunFirePayload } from "@neko/db/jobs";
import type { AgentEvent } from "@neko/llm";
import { appendWorkRunEvent } from "@neko/llm/work";
import { prepareWorkflowRun, runWorkflowTurn } from "@neko/llm/workflows";

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
  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({
      orgId: payload.orgId,
      threadId: prepared.threadId,
      runId: prepared.workRunId,
      seq,
      event,
    });
  };

  await runWorkflowTurn({
    prepared,
    userMessage: payload.userMessage,
    mode: "headless",
    emit,
  });
}

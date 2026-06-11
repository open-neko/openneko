import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  computeDueWorkflows,
  reEnablePausedWorkflows,
  singletonKeyForFiring,
} from "@neko/llm/workflows";

const SWEEP_WINDOW_MS = 90_000;

export async function runWorkflowCronSweep(): Promise<void> {
  const resumed = await reEnablePausedWorkflows().catch((e) => {
    console.warn(
      `[workflow-cron-sweep] pause-timer re-enable failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return 0;
  });
  if (resumed > 0) {
    console.log(`[workflow-cron-sweep] re-enabled ${resumed} paused workflow(s)`);
  }

  const now = new Date();
  const windowStart = new Date(now.getTime() - SWEEP_WINDOW_MS);
  const due = await computeDueWorkflows({ windowStart, windowEnd: now });

  if (due.length === 0) {
    return;
  }

  let enqueued = 0;
  for (const { workflow, firingTime } of due) {
    try {
      const key = singletonKeyForFiring(workflow.id, firingTime);
      const id = await enqueue(
        QUEUE.WORKFLOW_RUN_FIRE,
        {
          orgId: workflow.orgId,
          workflowId: workflow.id,
          triggerKind: "cron" as const,
          triggerPayload: { firingTime: firingTime.toISOString() },
        },
        {
          singletonKey: key,
          // Hold the slot for an hour so a late sweep can't re-fire the same
          // (workflow, firingTime) pair after the first one completes.
          singletonHours: 1,
        },
      );
      if (id) enqueued++;
    } catch (e) {
      console.warn(
        `[workflow-cron-sweep] failed to enqueue workflow=${workflow.id} firingTime=${firingTime.toISOString()}: ${
          e instanceof Error ? e.message : String(e)
        }`,
      );
    }
  }

  if (enqueued > 0) {
    console.log(
      `[workflow-cron-sweep] enqueued ${enqueued}/${due.length} workflow run(s) for window ending ${now.toISOString()}`,
    );
  }
}

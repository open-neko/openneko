import cronParser from "cron-parser";
import {
  countWorkflowRunsSince as defaultCountSince,
  listCronWorkflows,
  startOfTodayUtc,
  type WorkflowRecord,
} from "./store";

export type DueWorkflow = {
  workflow: WorkflowRecord;
  firingTime: Date;
};

export type ComputeDueWorkflowsInput = {
  windowStart: Date;
  windowEnd: Date;
  workflows?: WorkflowRecord[];
  /** DI for tests. */
  countWorkflowRunsSince?: typeof defaultCountSince;
};

export async function computeDueWorkflows(
  input: ComputeDueWorkflowsInput,
): Promise<DueWorkflow[]> {
  const all = input.workflows ?? (await listCronWorkflows());
  const countSince = input.countWorkflowRunsSince ?? defaultCountSince;
  const dayStart = startOfTodayUtc(input.windowEnd);
  const due: DueWorkflow[] = [];

  for (const workflow of all) {
    if (!workflow.cron || !workflow.cronEnabled || !workflow.enabled) continue;

    if (workflow.dailyRunBudget !== null) {
      const ran = await countSince(workflow.orgId, workflow.id, dayStart);
      if (ran >= workflow.dailyRunBudget) {
        console.log(
          `[workflow-cron-sweep] skipping ${workflow.id} (${workflow.name}): daily_run_budget=${workflow.dailyRunBudget} reached (${ran} today)`,
        );
        continue;
      }
    }

    try {
      const interval = cronParser.parseExpression(workflow.cron, {
        tz: workflow.cronTimezone,
        currentDate: input.windowStart,
        endDate: input.windowEnd,
      });
      while (interval.hasNext()) {
        const firingTime = interval.next().toDate();
        if (firingTime <= input.windowEnd) {
          due.push({ workflow, firingTime });
        }
      }
    } catch (error) {
      console.warn(
        `[workflow-cron-sweep] invalid cron "${workflow.cron}" on workflow ${workflow.id} (${workflow.name}): ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  }
  return due;
}

export function singletonKeyForFiring(workflowId: string, firingTime: Date): string {
  return `${workflowId}:${firingTime.toISOString()}`;
}

import cronParser from "cron-parser";
import { listCronWorkflows, type WorkflowRecord } from "./store";

export type DueWorkflow = {
  workflow: WorkflowRecord;
  firingTime: Date;
};

export type ComputeDueWorkflowsInput = {
  windowStart: Date;
  windowEnd: Date;
  workflows?: WorkflowRecord[];
};

export async function computeDueWorkflows(
  input: ComputeDueWorkflowsInput,
): Promise<DueWorkflow[]> {
  const all = input.workflows ?? (await listCronWorkflows());
  const due: DueWorkflow[] = [];
  for (const workflow of all) {
    if (!workflow.cron || !workflow.cronEnabled || !workflow.enabled) continue;
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

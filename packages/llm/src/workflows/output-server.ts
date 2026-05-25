import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import type { AgentEvent, OutputMood } from "../agent-backend";
import {
  WORKFLOW_OUTPUT_SCHEMA,
  type WorkflowOutputPayload,
} from "./fence-schemas";
import { emitWorkflowOutput, type WorkflowOutputRecord } from "./store";

export type WorkflowOutputContext = {
  orgId: string;
  workflowRunId: string;
  workRunId: string;
  emit: (event: AgentEvent) => Promise<void> | void;
};

/**
 * Optional delivery hook — the worker registers one at startup to fan a
 * newly-emitted output out to bound channels (Slack, Telegram, …). A
 * registered seam (like registerActionAdapter) so packages/llm never depends
 * on the worker's channel registry. Fire-and-forget; never fails the run.
 */
export type WorkflowOutputDeliveryHook = (
  orgId: string,
  output: WorkflowOutputRecord,
) => Promise<void> | void;

let outputDeliveryHook: WorkflowOutputDeliveryHook | null = null;

export function setWorkflowOutputDeliveryHook(
  hook: WorkflowOutputDeliveryHook | null,
): void {
  outputDeliveryHook = hook;
}

/**
 * Shared handler. The MCP tool and the fence-fallback path both route
 * here so persistence + the emit event happen in one place.
 */
export async function handleWorkflowOutput(
  ctx: WorkflowOutputContext,
  args: WorkflowOutputPayload,
): Promise<WorkflowOutputRecord> {
  const output = await emitWorkflowOutput({
    orgId: ctx.orgId,
    workflowRunId: ctx.workflowRunId,
    workRunId: ctx.workRunId,
    kind: args.kind,
    title: args.title,
    body: args.body,
    payload: args.payload,
    artifactPath: args.artifactPath ?? null,
    scope: args.scope ?? null,
    topic: args.topic ?? null,
    mood: (args.mood ?? null) as OutputMood | null,
    timeWindowStart: args.timeWindowStart
      ? new Date(args.timeWindowStart)
      : null,
    timeWindowEnd: args.timeWindowEnd
      ? new Date(args.timeWindowEnd)
      : null,
    freshnessTtlSeconds: args.freshnessTtlSeconds ?? null,
  });
  await ctx.emit({
    type: "output_emit",
    output_id: output.id,
    kind: output.kind,
  });
  if (outputDeliveryHook) {
    void Promise.resolve(outputDeliveryHook(ctx.orgId, output)).catch((err) => {
      console.warn(
        `[workflow-output] delivery hook failed: ${err instanceof Error ? err.message : err}`,
      );
    });
  }
  return output;
}

export function buildWorkflowOutputServer(ctx: WorkflowOutputContext) {
  const emitOutput = tool(
    "emit",
    [
      "Persist a workflow output — the thing this run produced. Most",
      "workflow value is non-mutating, so emit outputs liberally rather",
      "than reaching for state-changing actions.",
      "",
      "Use `kind` to describe the shape (`report`, `finding`,",
      "`observation`, `recommendation`, `briefing_card_proposal`, ...).",
      "Tag every output with a `scope` (e.g. 'apac_churn', 'inventory_risk'),",
      "optionally a more specific `topic`, and a `mood` ('good', 'watch',",
      "or 'act'). Other workflows subscribe by scope/mood and humans browse",
      "by them, so honest tagging is what makes the output discoverable.",
      "",
      "Example for an observe-and-report run:",
      "  kind: 'observation', scope: 'apac_churn', mood: 'watch',",
      "  title: 'APAC churn rose 18% WoW', body: '...'",
    ].join(" "),
    WORKFLOW_OUTPUT_SCHEMA.shape,
    async (args) => {
      const output = await handleWorkflowOutput(ctx, args);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              outputId: output.id,
              kind: output.kind,
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_workflow_output",
    version: "1.0.0",
    tools: [emitOutput],
  });
}

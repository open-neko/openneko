import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import type { AgentEvent, OutputMood } from "../agent-backend";
import { emitWorkflowOutput } from "./store";

export type WorkflowOutputContext = {
  orgId: string;
  workflowRunId: string;
  workRunId: string;
  emit: (event: AgentEvent) => Promise<void> | void;
};

const OUTPUT_KINDS = [
  "report",
  "summary",
  "briefing_card_proposal",
  "chart",
  "table",
  "file",
  "message_draft",
  "finding",
  "recommendation",
] as const;

const MOODS = ["good", "watch", "act"] as const;

export function buildWorkflowOutputServer(ctx: WorkflowOutputContext) {
  const emitOutput = tool(
    "emit",
    [
      "Persist a workflow output (report, finding, recommendation, briefing",
      "card proposal, etc.). Most workflow value is non-mutating — produce",
      "outputs liberally rather than reaching for state-changing actions.",
      "",
      "Tag every output with `scope` (e.g. 'apac_churn', 'inventory_risk'),",
      "optionally a more specific `topic`, and a `mood` ('good', 'watch',",
      "or 'act') so other workflows and humans can find it. Use `kind` to",
      "describe what shape this output takes.",
    ].join(" "),
    {
      kind: z.enum(OUTPUT_KINDS),
      title: z.string().max(240).optional(),
      body: z.string().max(64_000).optional(),
      payload: z.record(z.string(), z.unknown()).optional(),
      artifactPath: z.string().max(1024).optional(),
      scope: z.string().max(120).optional(),
      topic: z.string().max(120).optional(),
      mood: z.enum(MOODS).optional(),
      timeWindowStart: z.string().datetime().optional(),
      timeWindowEnd: z.string().datetime().optional(),
      freshnessTtlSeconds: z.number().int().positive().max(31_536_000).optional(),
    },
    async (args) => {
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

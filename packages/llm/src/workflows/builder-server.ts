import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import { saveWorkflow } from "./store";

export type WorkflowBuilderContext = {
  orgId: string;
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
};

export function buildWorkflowBuilderServer(ctx: WorkflowBuilderContext) {
  const createWorkflowTool = tool(
    "create_workflow",
    "Save (create or update) a workflow. Upserts by name within the org — if a workflow with the same name exists, it is updated in place.",
    {
      name: z.string().trim().min(1).max(120),
      description: z.string().max(2000).optional(),
      goal: z.string().max(2000).optional(),
      systemPromptOverlay: z.string().max(16000).optional(),
      steps: z
        .array(
          z.object({
            id: z.string().trim().min(1).max(60),
            description: z.string().trim().min(1).max(2000),
          }),
        )
        .min(1)
        .max(40),
      triggers: z
        .object({
          cron: z.string().trim().min(1).max(120).optional(),
          timezone: z.string().trim().min(1).max(80).optional(),
          enabled: z.boolean().optional(),
        })
        .optional(),
    },
    async (args) => {
      const result = await saveWorkflow({
        orgId: ctx.orgId,
        name: args.name,
        description: args.description,
        goal: args.goal,
        systemPromptOverlay: args.systemPromptOverlay,
        steps: args.steps,
        triggers: args.triggers,
        createdByThreadId: ctx.createdByThreadId ?? null,
        createdByRunId: ctx.createdByRunId ?? null,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              action: result.action,
              workflowId: result.workflow.id,
              name: result.workflow.name,
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_workflow_builder",
    version: "1.0.0",
    tools: [createWorkflowTool],
  });
}

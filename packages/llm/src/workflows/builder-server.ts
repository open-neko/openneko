import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { WORKFLOW_SAVE_SCHEMA } from "./fence-schemas";
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
    WORKFLOW_SAVE_SCHEMA.shape,
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

import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import type { AgentEvent } from "../agent-backend";
import { workflowSavedCard } from "./builder-cards";
import { WORKFLOW_SAVE_SCHEMA } from "./fence-schemas";
import { listWorkflows, saveWorkflow } from "./store";

export type WorkflowBuilderContext = {
  orgId: string;
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
  emit?: (event: AgentEvent) => Promise<void> | void;
};

export function buildWorkflowBuilderServer(ctx: WorkflowBuilderContext) {
  const createWorkflowTool = tool(
    "create_workflow",
    [
      "Create or update a workflow. Upserts by name within the org — if a",
      "workflow with the same name already exists, it is updated in place.",
      "Use this when the operator asks to set up, modify, or rename a recurring",
      "task or pipeline. After saving, narrate the change in a sentence; the",
      "tool also emits a confirmation card the operator can click to open the",
      "detail page.",
    ].join(" "),
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
      if (ctx.emit) {
        await ctx.emit({
          type: "surface",
          messages: workflowSavedCard({
            workflow: result.workflow,
            action: result.action,
          }),
        });
      }
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

  const listWorkflowsTool = tool(
    "list_workflows",
    [
      "List the workflows defined in this org so you can answer questions",
      "like 'what was the workflow we set up last week to summarize sales' or",
      "look up the exact name/steps of a workflow before updating it via",
      "`create_workflow`. Returns full bodies (steps, cron, description,",
      "system prompt overlay), ordered by most recently updated.",
    ].join(" "),
    {
      limit: z.number().int().min(1).max(200).optional(),
    },
    async (args) => {
      const all = await listWorkflows(ctx.orgId);
      const limit = args.limit ?? 50;
      const slice = all.slice(0, limit);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              total: all.length,
              returned: slice.length,
              workflows: slice.map((w) => ({
                id: w.id,
                name: w.name,
                description: w.description,
                enabled: w.enabled,
                status: w.status,
                goal: w.goal,
                systemPromptOverlay: w.systemPromptOverlay,
                steps: w.steps,
                cron: w.cron,
                cronTimezone: w.cronTimezone,
                cronEnabled: w.cronEnabled,
                updatedAt: w.updatedAt.toISOString(),
                createdAt: w.createdAt.toISOString(),
              })),
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_workflow_builder",
    version: "1.0.0",
    tools: [createWorkflowTool, listWorkflowsTool],
  });
}

import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import { writeWorkSkill } from "./skills";
import type { AgentEvent, AgentSurfaceMessage } from "../agent-backend";

const a2uiMessageSchema = z.object({ version: z.literal("v0.9") }).passthrough();

export function buildRenderCardsServer(
  emit: (event: AgentEvent) => Promise<void> | void,
) {
  const renderCards = tool(
    "render_cards",
    [
      "Render structured Neko cards inline in the chat.",
      "Use this for KPIs, tables, charts, and dashboard-style summaries.",
      "Pass a JSON array of A2UI v0.9 messages.",
    ].join(" "),
    {
      messages: z.array(a2uiMessageSchema).min(1),
    },
    async (args) => {
      const messages = args.messages as AgentSurfaceMessage[];
      await emit({ type: "surface", messages });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: true, accepted: messages.length }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_ui",
    version: "1.0.0",
    tools: [renderCards],
  });
}

export function buildSkillBuilderServer(skillsRoot: string) {
  const createSkill = tool(
    "create_skill",
    "Create or update an agentskills.io-style skill in Neko's shared skills directory.",
    {
      name: z.string().trim().min(1).max(64),
      description: z.string().trim().min(1).max(1024),
      body: z.string().min(1),
      license: z.string().trim().min(1).max(200).optional(),
      compatibility: z.string().trim().min(1).max(500).optional(),
      metadata: z.record(z.string().trim().min(1).max(128), z.string().max(2048)).optional(),
      allowedTools: z.string().trim().min(1).max(1000).optional(),
      files: z.array(
        z.object({
          path: z.string().trim().min(1).max(240),
          content: z.string().max(128 * 1024),
        }),
      ).max(30).optional(),
    },
    async (args) => {
      const result = await writeWorkSkill(skillsRoot, args);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              name: result.name,
              skillFile: `${result.skillPath}/SKILL.md`,
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_skills",
    version: "1.0.0",
    tools: [createSkill],
  });
}

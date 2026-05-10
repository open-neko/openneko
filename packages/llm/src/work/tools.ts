import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import { writeWorkSkill } from "./skills";
import type { AgentEvent, AgentSurfaceMessage } from "../agent-backend";
import {
  WORK_MEMORY_KINDS,
  archiveWorkMemory,
  rememberWorkMemory,
  searchWorkMemory,
  type WorkMemoryContext,
} from "./memory";

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

export function buildWorkMemoryServer(ctx: WorkMemoryContext) {
  const search = tool(
    "search",
    [
      "Search Neko Work's long-term memory. Returns saved memories plus",
      "matching historical Work thread messages. Use this before answering",
      "when a question may depend on durable preferences, business rules,",
      "metric definitions, company context, named entities, or older context.",
    ].join(" "),
    {
      query: z.string().max(800),
      limit: z.number().int().min(1).max(20).optional(),
      includeArchives: z.boolean().optional(),
    },
    async (args) => {
      const results = await searchWorkMemory({
        orgId: ctx.orgId,
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        query: args.query,
        limit: args.limit,
        includeArchives: args.includeArchives,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: true, ...results }),
          },
        ],
      };
    },
  );

  const remember = tool(
    "remember",
    [
      "Save a durable memory for future Neko Work sessions. Use only when the",
      "operator explicitly asks you to remember something, corrects a recurring",
      "assumption, defines a metric/business rule, or gives a stable preference",
      "that should affect future answers.",
    ].join(" "),
    {
      text: z.string().min(5).max(2000),
      kind: z.enum(WORK_MEMORY_KINDS),
      scope: z.enum(["global", "thread"]).optional(),
      scopeId: z.string().min(1).max(200).optional(),
      pinned: z.boolean().optional(),
      confidence: z.number().min(0).max(1).optional(),
      metadata: z.record(z.string(), z.unknown()).optional(),
    },
    async (args) => {
      const memory = await rememberWorkMemory({
        orgId: ctx.orgId,
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        text: args.text,
        kind: args.kind,
        scope: args.scope,
        scopeId: args.scopeId,
        pinned: args.pinned,
        confidence: args.confidence,
        metadata: args.metadata,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: true, memory }),
          },
        ],
      };
    },
  );

  const forget = tool(
    "forget",
    "Archive a saved memory by id. Use only when the operator asks you to forget, remove, replace, or correct that memory.",
    {
      id: z.string().min(1),
      reason: z.string().max(500).optional(),
    },
    async (args) => {
      const forgotten = await archiveWorkMemory(ctx.orgId, args.id, {
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        reason: args.reason,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: forgotten, id: args.id }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_memory",
    version: "1.0.0",
    tools: [search, remember, forget],
  });
}

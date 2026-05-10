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

function isValidA2UIMessage(m: unknown): m is AgentSurfaceMessage {
  if (!m || typeof m !== "object") return false;
  const o = m as Record<string, unknown>;
  if (o.version !== "v0.9") return false;
  return (
    "createSurface" in o ||
    "updateComponents" in o ||
    "updateDataModel" in o ||
    "deleteSurface" in o
  );
}

const RENDER_CARDS_DESCRIPTION = [
  "Render structured Neko cards inline in the chat using the A2UI v0.9 protocol.",
  "Prefer this over markdown when you have numeric findings, KPIs, or trends.",
  "",
  "Pass an ARRAY of A2UI v0.9 protocol messages — each must have",
  '`version: "v0.9"` plus exactly one of: `createSurface`, `updateComponents`,',
  "`updateDataModel`, `deleteSurface`. Bare component objects (e.g.",
  '`{ "type": "kpi_group", ... }`) are rejected — they MUST be wrapped in',
  "an `updateComponents` envelope.",
  "",
  "Catalog (catalogId `urn:app:catalog:briefing:v1`):",
  "  - `Markdown` — prose block. Use for ANY narrative text — your response",
  "    prose lives here, never outside the tool call. Props: text (markdown",
  "    string; supports headings, lists, tables, code blocks).",
  "  - `Briefing` — root container. Props: greeting, subtitle, role, children[].",
  "  - `BriefingCard` — KPI card with optional chart. Required props:",
  "    metricId (any string for ad-hoc cards, e.g. 'chat-1'),",
  '    source ("chat" for ad-hoc rendering),',
  '    mood ("good" | "watch" | "act"),',
  "    text (1-sentence headline), metric (e.g. '$498,376'),",
  "    label (e.g. 'Total Profit'), detail (1-3 sentences),",
  '    chartType ("kpi" | "line" | "bar" | "area" | "donut"),',
  "    chartData (array of `{ d: string, v: number, t?: number }`,",
  '    or `[]` when chartType="kpi").',
  "",
  "Typical message sequence:",
  "  1. createSurface (once)",
  "  2. updateComponents with a `Briefing` root + 1-N `BriefingCard` children",
  "",
  "Example (one KPI card, no chart):",
  "[",
  '  { "version":"v0.9", "createSurface":{ "surfaceId":"s1",',
  '    "catalogId":"urn:app:catalog:briefing:v1" } },',
  '  { "version":"v0.9", "updateComponents":{ "surfaceId":"s1", "components":[',
  '    { "id":"root", "component":"Briefing", "greeting":"Top product",',
  '      "subtitle":"All stores", "role":"CEO", "children":["c1"] },',
  '    { "id":"c1", "component":"BriefingCard", "metricId":"chat-1",',
  '      "source":"chat", "mood":"good", "text":"Mountain-200 Silver leads",',
  '      "metric":"$498,376", "label":"Total Profit",',
  '      "detail":"$3.19M revenue − $2.70M cost on 2,130 units sold.",',
  '      "chartType":"kpi", "chartData":[] }',
  "  ]}}",
  "]",
].join("\n");

export function buildRenderCardsServer(
  emit: (event: AgentEvent) => Promise<void> | void,
) {
  const renderCards = tool(
    "render_cards",
    RENDER_CARDS_DESCRIPTION,
    {
      messages: z.array(a2uiMessageSchema).min(1),
    },
    async (args) => {
      const valid = (args.messages as unknown[]).filter(isValidA2UIMessage);
      const rejected = args.messages.length - valid.length;
      if (valid.length === 0) {
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({
                ok: false,
                error:
                  "All messages rejected — each must have version: 'v0.9' and one of createSurface/updateComponents/updateDataModel/deleteSurface. Bare component objects are not accepted; wrap them in updateComponents.",
                rejected,
              }),
            },
          ],
        };
      }
      await emit({ type: "surface", messages: valid });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              accepted: valid.length,
              rejected,
            }),
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

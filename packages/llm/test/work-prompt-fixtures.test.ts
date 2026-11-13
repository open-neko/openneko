import { createHash } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import type { KnowledgePackContents } from "../src/knowledge-pack";
import { buildWorkPrompt } from "../src/work/prompt";

// The Go turn (apps/openneko/internal/webfront/turnprompt.go) must build the
// same prompt. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/workprompt.json");

type Args = Parameters<typeof buildWorkPrompt>[0];
type Case = { name: string; filesEnabled: boolean; input: Required<Omit<Args, "bot" | "messages">> & { bot: Args["bot"] | null; messages: Array<{ role: "user" | "assistant"; content: string }> } };

const workspace: AgentWorkspace = {
  orgRoot: "/home/neko/.config/openneko/agents/orgs/org_1",
  skillsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/skills",
  memoryRoot: "/home/neko/.config/openneko/agents/orgs/org_1/memory",
  knowledgeRoot: "/home/neko/.config/openneko/agents/orgs/org_1/knowledge",
  uploadsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/uploads",
  runsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs",
  threadUploadsRoot: "/home/neko/.config/openneko/agents/orgs/org_1/uploads/t-1",
  runRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1",
  artifactRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1/artifacts",
  binRoot: "/home/neko/.config/openneko/agents/orgs/org_1/runs/r-1/bin",
};

const longSummary = `Sales orders with lines, ${"🚚 shipping ".repeat(12)}and returns`;
const agentic: KnowledgePackContents = {
  mode: "agentic",
  tables: JSON.stringify({
    tables: [
      ...Array.from({ length: 140 }, (_, i) => ({ schema: "sales", name: `order_part_${i}`, column_count: i })),
      { name: "customer", summary: longSummary },
      { id: "api:orders" },
      { schema: "", name: 7, summary: 0, column_count: null },
      { summary: "line one\nline two" },
    ],
  }),
  namespaces: "{}",
  insights: JSON.stringify({
    hub_tables: [
      {
        name: "salesorderheader",
        summary: longSummary,
        join_paths: ["salesorderheader -> salesorderdetail", "salesorderheader -> customer", 3, null, "a", "b", "c"],
        examples: [
          "query {\n  salesorderheader(limit: 5) {\n  id\t totaldue }\n}",
          { query: "query { customer { id } }" },
        ],
      },
      { summary: 12, join_paths: "abcdefghij", examples: [{ name: "x", b: [1, 2.5, true, null] }, 4] },
      ...Array.from({ length: 40 }, (_, i) => ({
        name: `hub_${i}`,
        summary: "s".repeat(200),
        examples: ["q".repeat(400)],
      })),
    ],
    api_operations: [
      { name: "listOrders", summary: "List orders ".repeat(20) },
      { summary: "no name" },
      ...Array.from({ length: 80 }, (_, i) => ({ name: `operation_${i}`, summary: "Ä".repeat(i) })),
    ],
    help_cards: [
      { id: "help:filters", summary: "Filters" },
      { id: "", summary: "skipped" },
      ...Array.from({ length: 60 }, (_, i) => ({ id: `help:topic_${i}` })),
    ],
  }),
  syntax: '{"operators":["eq","gt"]}',
};

const legacy: KnowledgePackContents = {
  mode: "legacy",
  tables: "{\"tables\":[]}",
  namespaces: "{\"namespaces\":[\"public\"]}",
  insights: "{}",
  syntax: "{}",
};

const textTables: KnowledgePackContents = {
  mode: "agentic",
  tables: "customer: id, name\n\n   \norders: id, total\n",
  namespaces: "{}",
  insights: "not json",
  syntax: "",
};

const all = {
  backend: "ax" as const,
  workspace,
  knowledge: agentic,
  messages: [] as Case["input"]["messages"],
  currentUserMessage: "What was revenue last week?",
  priorSummary: "",
  memoryContext: "",
  operatorProfile: "",
  installedSkills: [] as Array<{ name: string; description: string }>,
  wantsCards: true,
  includeUxMetadata: true,
  supportsCardTool: true,
  richCards: true,
  supportsSkillTool: true,
  supportsMemoryTool: true,
  supportsWorkflowTool: true,
  supportsPolicyTool: true,
  supportsSourceConfigTool: true,
  supportsClarificationTool: true,
  supportsNativeDelegation: true,
  inlineTranscript: true,
  bot: null as Case["input"]["bot"],
  scriptData: true,
};

const cases: Case[] = [
  {
    name: "web admin with every tool",
    filesEnabled: true,
    input: {
      ...all,
      messages: [
        { role: "user", content: "Revenue by month?" },
        { role: "assistant", content: "Here it is:\n\n| Month | Revenue |" },
        { role: "user", content: "What was revenue last week?" },
      ],
      priorSummary: "The operator studies Q2 revenue.",
      memoryContext: "Memories the operator has saved — treat as durable context:\n- [m1] business_rule (global): Fiscal year starts in April.",
      operatorProfile: "<operator-profile>\nYou are working for Asha.\n</operator-profile>",
      installedSkills: [{ name: "graphjin-config", description: "Edit config" }],
      bot: { name: "Stock \"watcher\"", instructions: "  Watch stock levels daily.\n" },
    },
  },
  {
    name: "telegram member",
    filesEnabled: false,
    input: { ...all, knowledge: legacy, richCards: false, supportsSourceConfigTool: false },
  },
  {
    name: "slack eval without cards",
    filesEnabled: true,
    input: {
      ...all,
      knowledge: textTables,
      wantsCards: false,
      richCards: false,
      includeUxMetadata: false,
      supportsSourceConfigTool: false,
      supportsNativeDelegation: false,
      memoryContext: "   \n",
      bot: { name: "Helper", instructions: " \n " },
    },
  },
  {
    name: "no tools",
    filesEnabled: false,
    input: {
      ...all,
      supportsCardTool: false,
      supportsSkillTool: false,
      supportsMemoryTool: false,
      supportsWorkflowTool: false,
      supportsPolicyTool: false,
      supportsSourceConfigTool: false,
      supportsClarificationTool: false,
      inlineTranscript: false,
    },
  },
  {
    name: "skill list catalog",
    filesEnabled: false,
    input: {
      ...all,
      backend: "other" as "ax",
      knowledge: legacy,
      supportsSkillTool: false,
      installedSkills: [
        { name: "pdf", description: "Read and write PDF files" },
        { name: "notes", description: "" },
      ],
    },
  },
  {
    name: "skill list catalog empty",
    filesEnabled: false,
    input: { ...all, backend: "other" as "ax", knowledge: legacy },
  },
  {
    name: "no sandbox, so no script data",
    filesEnabled: true,
    input: { ...all, scriptData: false },
  },
];

function prompt(c: Case): string {
  const before = process.env.OPENNEKO_SERVE_URL;
  if (c.filesEnabled) process.env.OPENNEKO_SERVE_URL = "http://serve.test";
  else delete process.env.OPENNEKO_SERVE_URL;
  try {
    return buildWorkPrompt({ ...c.input, bot: c.input.bot ?? undefined });
  } finally {
    if (before === undefined) delete process.env.OPENNEKO_SERVE_URL;
    else process.env.OPENNEKO_SERVE_URL = before;
  }
}

const sha256 = (text: string) => createHash("sha256").update(text, "utf8").digest("hex");

describe("work prompt fixtures for the Go turn", () => {
  const before = process.env.OPENNEKO_SERVE_URL;
  afterEach(() => {
    if (before === undefined) delete process.env.OPENNEKO_SERVE_URL;
    else process.env.OPENNEKO_SERVE_URL = before;
  });

  it("matches the fixture the Go test reads", () => {
    const built = cases.map((c) => {
      const text = prompt(c);
      return { ...c, bytes: Buffer.byteLength(text, "utf8"), sha256: sha256(text) };
    });
    const json = `${JSON.stringify(built, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, json);
    expect(existsSync(FIXTURE)).toBe(true);
    expect(readFileSync(FIXTURE, "utf8")).toBe(json);
  });
});

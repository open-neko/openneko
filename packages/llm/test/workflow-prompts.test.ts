import { describe, expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { buildWorkflowRunnerPrompt } from "../src/workflows/runner-prompt";
import type { WorkflowRecord } from "../src/workflows/store";

const sampleWorkspace: AgentWorkspace = {
  orgRoot: "/tmp/org",
  skillsRoot: "/tmp/org/skills",
  memoryRoot: "/tmp/org/memory",
  knowledgeRoot: "/tmp/org/knowledge",
  uploadsRoot: "/tmp/org/uploads",
  runsRoot: "/tmp/org/runs",
  threadUploadsRoot: "/tmp/org/uploads/t1",
  runRoot: "/tmp/org/runs/r1",
  artifactRoot: "/tmp/org/runs/r1/artifacts",
  binRoot: "/tmp/org/runs/r1/bin",
};

const sampleWorkflow: WorkflowRecord = {
  id: "wf1",
  orgId: "org1",
  name: "APAC revenue dip check",
  description: "Daily APAC revenue check.",
  enabled: true,
  status: "active",
  goal: "Surface revenue dips.",
  systemPromptOverlay: "Show INR in lakhs.",
  steps: [{ id: "pull", description: "Pull last 7 days of revenue" }],
  cron: null,
  cronTimezone: "UTC",
  cronEnabled: false,
  dailyRunBudget: null,
  outputContract: null,
  createdByThreadId: null,
  createdByRunId: null,
  createdAt: new Date(),
  updatedAt: new Date(),
};

describe("buildWorkflowRunnerPrompt", () => {
  const sampleKnowledge = {
    tables: '{"tables":[{"name":"salesorderdetail"}]}',
    namespaces: '{"namespaces":["public"]}',
    insights: '{"hub_tables":["salesorderdetail"]}',
    syntax: '{"query":{"aggregations":{"functions":["sum_<column>"]},"examples":{"aggregations":[{"description":"sum(expr)","query":"{ orders { revenue: sum(expr: { mul: [price, qty] }) } }"}]}}}',
  };
  const base = {
    workflow: sampleWorkflow,
    mode: "headless" as const,
    memoryContext: "",
    backend: "hermes" as const,
    workspace: sampleWorkspace,
    knowledge: sampleKnowledge,
  };

  it("uses MCP tool names when mcpTools=true", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("mcp_neko_workflow_output_emit");
    expect(prompt).toContain("mcp_neko_action_request");
    expect(prompt).not.toContain("```neko_workflow_output");
    expect(prompt).not.toContain("```neko_action_request");
  });

  it("fails closed when the native GraphJin broker tool is unavailable", () => {
    expect(() =>
      buildWorkflowRunnerPrompt({ ...base, mcpTools: false }),
    ).toThrow(/native GraphJin broker tool/);
  });

  it("surfaces installed plugin action kinds to the native tool path", () => {
      const prompt = buildWorkflowRunnerPrompt({
        ...base,
        mcpTools: true,
        pluginActions: [
          {
            kind: "send_slack_dm",
            description: "Send a DM to a Slack user.",
            example: { user: "amit", text: "Stock is low." },
          },
          {
            kind: "lookup_slack_entity",
            description: "Look up a Slack user.",
            default_mode: "deny",
          },
        ],
      });
      expect(prompt).toContain("send_slack_dm");
      // the example payload is surfaced verbatim so the agent copies the shape
      expect(prompt).toContain('example payload: {"user":"amit","text":"Stock is low."}');
      // denied-everywhere kinds are not advertised
      expect(prompt).not.toContain("lookup_slack_entity");
  });

  it("surfaces an internal record action's scope to the native action tool", () => {
    const prompt = buildWorkflowRunnerPrompt({
      ...base,
      mcpTools: true,
      pluginActions: [
        {
          kind: "record_create",
          scope: "internal",
          description: "Create a generated-app record.",
          default_mode: "ask",
        },
      ],
    });

    expect(prompt).toContain("`record_create` (scope: internal)");
    expect(prompt).toContain("Always use the exact scope listed");
  });

  it("includes the workflow's overlay and steps", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("Show INR in lakhs.");
    expect(prompt).toContain("Pull last 7 days of revenue");
  });

  it("uses native brokered GraphQL for data access", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("<data_access>");
    expect(prompt).toContain("mcp_neko_graphjin_execute_graphql");
    expect(prompt).toContain("mcp_neko_graphjin_query_catalog");
    expect(prompt).not.toContain("graphjin cli");
  });

  it("inlines syntax.json so cat-truncation can't hide the aggregation patterns", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain(sampleKnowledge.syntax);
  });

  it("exposes search + save MCP tools in the long_term_memory block when mcpTools=true", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("<long_term_memory>");
    expect(prompt).toContain("mcp_neko_memory_search");
    expect(prompt).toContain("mcp_neko_memory_save");
  });

  it("uses native dynamic delegation guidance for Hermes workflow runs", () => {
    const prompt = buildWorkflowRunnerPrompt({
      ...base,
      backend: "hermes",
      mcpTools: true,
    });
    expect(prompt).toContain("delegate_task");
    expect(prompt).toContain("OpenNeko does not provide named subagent profiles");
    expect(prompt).toContain("toolsets");
  });

});

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
  claudeProjectRoot: "/tmp/org",
  claudeConfigRoot: "/tmp/org/claude/config",
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
    expect(prompt).toContain("mcp__neko_workflow_output__emit");
    expect(prompt).toContain("mcp__neko_action__request");
    expect(prompt).not.toContain("```neko_workflow_output");
    expect(prompt).not.toContain("```neko_action_request");
  });

  it("uses fence instructions when mcpTools=false", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("```neko_workflow_output");
    expect(prompt).toContain("```neko_action_request");
    expect(prompt).not.toContain("mcp__neko_workflow_output__emit");
    expect(prompt).not.toContain("mcp__neko_action__request");
  });

  it.each([true, false])(
    "surfaces installed plugin action kinds (mcpTools=%s) and uses one as the fence example",
    (mcpTools) => {
      const prompt = buildWorkflowRunnerPrompt({
        ...base,
        mcpTools,
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
      if (!mcpTools) {
        // the fence example uses a real installed kind, not the placeholder
        expect(prompt).toContain('"kind": "send_slack_dm"');
        expect(prompt).not.toContain('"kind": "send_message"');
      }
    },
  );

  it("includes the workflow's overlay and steps in both branches", () => {
    for (const mcpTools of [true, false]) {
      const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools });
      expect(prompt).toContain("Show INR in lakhs.");
      expect(prompt).toContain("Pull last 7 days of revenue");
    }
  });

  it("includes data_access guidance pointing at the knowledge pack", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("<data_access>");
    expect(prompt).toContain("graphjin cli execute_graphql");
    expect(prompt).toContain(sampleWorkspace.knowledgeRoot);
  });

  it("inlines syntax.json so cat-truncation can't hide the aggregation patterns", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain(sampleKnowledge.syntax);
  });

  it("exposes search + save MCP tools in the long_term_memory block when mcpTools=true", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("<long_term_memory>");
    expect(prompt).toContain("mcp__neko_memory__search");
    expect(prompt).toContain("mcp__neko_memory__save");
  });

  it("omits MCP memory tools and never falls back to a save fence when mcpTools=false", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("<long_term_memory>");
    expect(prompt).not.toContain("mcp__neko_memory__search");
    expect(prompt).not.toContain("mcp__neko_memory__save");
    // No fence-save path for workflow runner (runtime doesn't parse one).
    expect(prompt).not.toContain("```neko_memory");
  });
});

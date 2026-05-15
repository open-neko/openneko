import { describe, expect, it } from "vitest";
import { buildMetricPrompt } from "../src/metric-prompt";
import type { AgentWorkspace } from "../src/agent-backend";
import type { KnowledgePackContents } from "../src/knowledge-pack";

const fakeWorkspace: AgentWorkspace = {
  orgRoot: "/tmp/wsp/org",
  skillsRoot: "/tmp/wsp/skills",
  memoryRoot: "/tmp/wsp/memory",
  knowledgeRoot: "/tmp/wsp/knowledge",
  uploadsRoot: "/tmp/wsp/uploads",
  runsRoot: "/tmp/wsp/runs",
  threadUploadsRoot: "/tmp/wsp/thread-uploads",
  runRoot: "/tmp/wsp/run",
  artifactRoot: "/tmp/wsp/artifacts",
  binRoot: "/tmp/wsp/bin",
  claudeProjectRoot: "/tmp/wsp/claude-project",
  claudeConfigRoot: "/tmp/wsp/claude-config",
};

const fakeKnowledge: KnowledgePackContents = {
  tables: '{"tables":["sales","products"]}',
  namespaces: '{"ns":["public"]}',
  insights: '{"hubs":["sales"]}',
  syntax: '{"operators":["sum","count","ratio"]}',
};

const fakeInput = {
  title: "Top Product",
  why: "CEO wants top revenue product",
  role: "CEO",
  slug: "top-product",
  chartHint: "bar" as const,
};

describe("buildMetricPrompt", () => {
  it("returns a single string composed from required sections", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(typeof prompt).toBe("string");
    // Sentinel anchors from each section.
    expect(prompt).toContain("<role>");
    expect(prompt).toContain("<long_term_memory>");
    expect(prompt).toContain("<data_access>");
    expect(prompt).toContain("<time_window>");
    expect(prompt).toContain("<mood_and_chart>");
    expect(prompt).toContain("<hard_constraints>");
    expect(prompt).toContain("<output_contract>");
    expect(prompt).toContain("<input>");
  });

  it("inlines the full knowledge pack (the metric agent is one-shot)", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).toContain(fakeKnowledge.tables);
    expect(prompt).toContain(fakeKnowledge.namespaces);
    expect(prompt).toContain(fakeKnowledge.insights);
    expect(prompt).toContain(fakeKnowledge.syntax);
  });

  it("uses the configured shell tool name throughout", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "Bash",
    });
    expect(prompt).toContain("`Bash`");
    expect(prompt).not.toContain("`terminal`");
  });

  it("declares the JSON output contract with every required field name", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    for (const key of [
      "reasoning",
      "headlineMetric",
      "headlineLabel",
      "insightText",
      "detailText",
      "mood",
      "chartType",
      "chartData",
      "timeWindow",
      "grain",
      "start",
      "end",
      "label",
    ]) {
      expect(prompt).toContain(`"${key}"`);
    }
  });

  it("inlines the card input as JSON the model can read deterministically", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).toContain('"cardTitle": "Top Product"');
    expect(prompt).toContain('"cardRole": "CEO"');
    expect(prompt).toContain('"cardSlug": "top-product"');
    expect(prompt).toContain('"chartHint": "bar"');
  });

  it("forwards memoryContext into the long_term_memory block", () => {
    const ctx = "- [id-1] business_rule (global): Always cite tables";
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
      memoryContext: ctx,
    });
    expect(prompt).toContain(ctx);
  });

  it("doesn't expose the MCP memory tool surface (one-shot agent shouldn't be writing memories)", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).not.toContain("mcp__neko_memory__save");
    expect(prompt).not.toContain("mcp__neko_memory__search");
  });

  it("includes the anti-fanout + date-filter rules (regression: must keep them across refactors)", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).toContain("flattened");
    expect(prompt).toContain("distinct: [parent_id]");
    expect(prompt).toContain("multiple operators under");
  });

  it("includes the live-max(date) anchor rule under hard_constraints (TTM correctness)", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).toContain("max(<date_col>)");
  });

  it("includes time-window grain rules (TTM, snapshot, etc.)", () => {
    const prompt = buildMetricPrompt({
      input: fakeInput,
      knowledge: fakeKnowledge,
      workspace: fakeWorkspace,
      shellTool: "terminal",
    });
    expect(prompt).toContain("TTM");
    expect(prompt).toContain("snapshot");
    expect(prompt).toContain("trailing twelve months");
  });
});

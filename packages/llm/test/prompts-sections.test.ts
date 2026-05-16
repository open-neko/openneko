import { describe, expect, it } from "vitest";
import {
  GRAPHJIN_DATE_RULE,
  GRAPHJIN_FANOUT_RULE,
  buildDataAccessSection,
  buildMemorySection,
} from "../src/prompts/sections";
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
  tables: '{"tables":["t1","t2"]}',
  namespaces: '{"ns":["public"]}',
  insights: '{"hubs":["users"]}',
  syntax: '{"operators":["eq","gt"]}',
};

describe("buildMemorySection", () => {
  it("renders the operator-saved-context section even when no memories are loaded", () => {
    const section = buildMemorySection(false, undefined);
    expect(section).toContain("<long_term_memory>");
    expect(section).toContain("No memories are currently saved");
    expect(section).toContain("</long_term_memory>");
  });

  it("inlines the loaded memory context when provided", () => {
    const ctx = "- [id-1] business_rule (global): Always cite tables";
    const section = buildMemorySection(false, ctx);
    expect(section).toContain(ctx);
  });

  it("describes the MCP tool path when supportsMemoryTool is true", () => {
    const section = buildMemorySection(true, "loaded memories here");
    expect(section).toContain("mcp__neko_memory__save");
    expect(section).toContain("mcp__neko_memory__search");
    expect(section).not.toContain("```neko_memory");
  });

  it("describes the neko_memory fence path when supportsMemoryTool is false", () => {
    const section = buildMemorySection(false, "loaded memories here");
    expect(section).toContain("```neko_memory");
    expect(section).toContain('"save"');
    expect(section).not.toContain("mcp__neko_memory__save");
  });

  it("includes precedence + cite-back framing in every variant", () => {
    for (const supports of [true, false]) {
      const section = buildMemorySection(supports, "anything");
      expect(section).toMatch(/take precedence/i);
      expect(section).toMatch(/cite/i);
    }
  });
});

describe("buildDataAccessSection", () => {
  it("inlines the GraphJin date rule and the anti-fanout rule", () => {
    const section = buildDataAccessSection({
      shellTool: "Bash",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "syntax",
    });
    // Date rule body (without the leading "- " bullet).
    expect(section).toContain("multiple operators under");
    // Fanout rule body.
    expect(section).toContain("flattened");
    expect(section).toContain("distinct: [parent_id]");
  });

  it("uses the configured shell tool name", () => {
    const bash = buildDataAccessSection({
      shellTool: "Bash",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "syntax",
    });
    const term = buildDataAccessSection({
      shellTool: "terminal",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "syntax",
    });
    expect(bash).toContain("`Bash`");
    expect(bash).not.toContain("`terminal`");
    expect(term).toContain("`terminal`");
    expect(term).not.toContain("`Bash`");
  });

  it("`syntax` mode inlines syntax + points at file paths for the rest", () => {
    const section = buildDataAccessSection({
      shellTool: "Bash",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "syntax",
    });
    expect(section).toContain(fakeKnowledge.syntax);
    // File-path guidance for tables/namespaces/insights only appears
    // in `syntax` mode (not `all` mode).
    expect(section).toContain("/tmp/wsp/knowledge");
    // Tables/namespaces/insights bodies should NOT be inlined.
    expect(section).not.toContain(fakeKnowledge.tables);
    expect(section).not.toContain(fakeKnowledge.namespaces);
    expect(section).not.toContain(fakeKnowledge.insights);
  });

  it("`all` mode inlines tables + namespaces + insights + syntax (no file-path pointer)", () => {
    const section = buildDataAccessSection({
      shellTool: "terminal",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "all",
    });
    expect(section).toContain(fakeKnowledge.tables);
    expect(section).toContain(fakeKnowledge.namespaces);
    expect(section).toContain(fakeKnowledge.insights);
    expect(section).toContain(fakeKnowledge.syntax);
  });

  it("blocks discovery commands the agent shouldn't be running", () => {
    const section = buildDataAccessSection({
      shellTool: "Bash",
      workspace: fakeWorkspace,
      knowledge: fakeKnowledge,
      inlineKnowledge: "syntax",
    });
    expect(section).toContain("Do NOT call");
    expect(section).toContain("list_tables");
    expect(section).toContain("describe_table");
  });
});

describe("constants stay shaped right (consumed verbatim by other prompts)", () => {
  it("GRAPHJIN_DATE_RULE starts as a bullet so callers can splice consistently", () => {
    expect(GRAPHJIN_DATE_RULE.startsWith("- ")).toBe(true);
    expect(GRAPHJIN_DATE_RULE).toContain("and: [");
  });

  it("GRAPHJIN_FANOUT_RULE starts as a bullet and names the three remediations", () => {
    expect(GRAPHJIN_FANOUT_RULE.startsWith("- ")).toBe(true);
    expect(GRAPHJIN_FANOUT_RULE).toContain("(a)");
    expect(GRAPHJIN_FANOUT_RULE).toContain("(b)");
    expect(GRAPHJIN_FANOUT_RULE).toContain("(c)");
  });
});

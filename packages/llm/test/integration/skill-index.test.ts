
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { and, db, eq, GRAPHJIN_CONFIG_SCOPE, llm_provider_config, pool, skill_embedding } from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";

// Each topic is one axis, so texts that share a topic sit close together.
const TOPICS = [/spreadsheet|excel|xlsx|workbook/i, /word|document|docx|letter/i, /customer|reply|complaint/i];
vi.mock("../../src/embedding", async () => ({
  EMBEDDING_DIM: 384,
  embedText: vi.fn(async (text: string) => {
    const v = new Array<number>(384).fill(0.001);
    TOPICS.forEach((topic, i) => {
      if (topic.test(text)) v[i] = 1;
    });
    return v;
  }),
  vectorLiteral: (vec: number[]) => `[${vec.join(",")}]`,
  embeddingModel: vi.fn(() => "test-model"),
}));

import { embeddingModel } from "../../src/embedding";
import { runEmbeddingIndexJob } from "../../src/embedding-jobs";
import { searchSkillsForRun, syncSkillIndex } from "../../src/work/skill-index";
import { createWorkRun, createWorkThread } from "../../src/work/store";
import { inProcessControlPlane } from "../../src/work/control-plane";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

describeIfDb("skill index", () => {
  const orgId = uniqueOrgId("skills");
  let runId: string;

  beforeAll(async () => {
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "unix:///run/openneko/sandbox.sock");
    await createTestOrg(orgId);
    const thread = await createWorkThread(orgId, "skills");
    const run = await createWorkRun(orgId, thread.id, "ax", { userId: null, role: "admin" });
    runId = run.id;
  });

  afterAll(async () => {
    vi.unstubAllEnvs();
    await deleteTestOrg(orgId);
    await pool().end();
  });

  it("finds the guide closest in meaning and follows installs and removals", async () => {
    await syncSkillIndex(orgId, [
      { name: "xlsx", description: "Use this skill any time a spreadsheet file is the primary input or output" },
      { name: "skill-creator", description: "Create and edit Word documents" },
      { name: "customer-reply", description: "Answer a customer complaint in the house voice" },
    ]);
    const rows = await db().select().from(skill_embedding).where(eq(skill_embedding.org_id, orgId));
    expect(rows).toHaveLength(3);
    expect(rows.every((row) => row.embedding === null)).toBe(true);
    for (const row of rows) await runEmbeddingIndexJob({ kind: "skill", orgId, id: row.id });
    const indexed = await db().select().from(skill_embedding).where(eq(skill_embedding.org_id, orgId));
    expect(indexed.every((row) => row.embedding !== null && row.embedding_model === "test-model")).toBe(true);

    const excel = await searchSkillsForRun({ orgId, runId, query: "Make me an Excel file of stock", limit: 1 });
    expect(excel.map((s) => s.name)).toEqual(["xlsx"]);
    const reply = await searchSkillsForRun({ orgId, runId, query: "draft a reply to an upset customer", limit: 1 });
    expect(reply.map((s) => s.name)).toEqual(["customer-reply"]);

    await syncSkillIndex(orgId, [
      { name: "xlsx", description: "Use this skill any time a spreadsheet file is the primary input or output" },
      { name: "skill-creator", description: "Draft letters as Word documents" },
    ]);
    const after = await db().select().from(skill_embedding).where(eq(skill_embedding.org_id, orgId));
    expect(after.map((row) => row.skill_name).sort()).toEqual(["skill-creator", "xlsx"]);
    expect(after.find((row) => row.skill_name === "skill-creator")?.document).toBe("skill-creator\nDraft letters as Word documents");
  });

  it("searches by keyword when embedding is off", async () => {
    vi.mocked(embeddingModel).mockReturnValue(null);
    try {
      const results = await searchSkillsForRun({ orgId, runId, query: "spreadsheet output", limit: 1 });
      expect(results.map((s) => s.name)).toEqual(["xlsx"]);
      expect(await searchSkillsForRun({ orgId, runId, query: "the and of" })).toEqual([]);
    } finally {
      vi.mocked(embeddingModel).mockReturnValue("test-model");
    }
  });

  it("hides the script skills when the install has no sandbox", async () => {
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "");
    try {
      const results = await searchSkillsForRun({ orgId, runId, query: "Make me an Excel file of stock", limit: 3 });
      expect(results.map((s) => s.name)).not.toContain("xlsx");
      expect(results.map((s) => s.name)).not.toContain("skill-creator");
    } finally {
      vi.stubEnv("OPENNEKO_SANDBOX_URL", "unix:///run/openneko/sandbox.sock");
    }
  });

  it("hides the script skills when an admin turned the org's Terminal off", async () => {
    await db().insert(llm_provider_config).values({
      org_id: orgId, scope: "agent", provider: "ax", enabled: true, config: { terminal: false }, secrets: {},
    });
    try {
      const results = await searchSkillsForRun({ orgId, runId, query: "Make me an Excel file of stock", limit: 3 });
      expect(results.map((s) => s.name)).not.toContain("xlsx");
    } finally {
      await db().delete(llm_provider_config)
        .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, "agent")));
    }
    const on = await searchSkillsForRun({ orgId, runId, query: "Make me an Excel file of stock", limit: 1 });
    expect(on.map((s) => s.name)).toEqual(["xlsx"]);
  });

  it("finds the admin-only skill for an admin run with the config tools, and for no other run", async () => {
    vi.mocked(embeddingModel).mockReturnValue(null);
    const thread = await createWorkThread(orgId, "service");
    const service = await createWorkRun(orgId, thread.id, "ax", { userId: null, role: "service" });
    try {
      await syncSkillIndex(orgId, [
        { name: "xlsx", description: "Use this skill any time a spreadsheet file is the primary input or output" },
        { name: "graphjin-config", description: "Use when viewing or proposing changes to GraphJin sources and roles" },
      ]);
      const search = (run: string) =>
        inProcessControlPlane
          .searchSkillsForRun({ orgId, runId: run, query: "GraphJin sources roles", limit: 3 })
          .then((results) => results.map((s) => s.name));

      await db().insert(llm_provider_config).values({
        org_id: orgId,
        scope: GRAPHJIN_CONFIG_SCOPE,
        provider: "graphjin-config",
        enabled: false,
        config: { sourceConfigEnabled: false },
        secrets: {},
      });
      expect(await search(runId)).not.toContain("graphjin-config");
      await db()
        .update(llm_provider_config)
        .set({ enabled: true, config: { sourceConfigEnabled: true } })
        .where(and(eq(llm_provider_config.org_id, orgId), eq(llm_provider_config.scope, GRAPHJIN_CONFIG_SCOPE)));
      expect(await search(runId)).toContain("graphjin-config");
      expect(await search(service.id)).not.toContain("graphjin-config");
      expect(await searchSkillsForRun({ orgId, runId: service.id, query: "GraphJin sources roles" }))
        .toEqual([expect.objectContaining({ name: "graphjin-config" })]);
    } finally {
      vi.mocked(embeddingModel).mockReturnValue("test-model");
    }
  });

  it("finds nothing for an unknown run", async () => {
    const results = await searchSkillsForRun({ orgId, runId: "00000000-0000-0000-0000-000000000000", query: "excel" });
    expect(results).toEqual([]);
  });
});

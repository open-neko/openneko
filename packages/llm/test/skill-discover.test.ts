import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  MAX_LEARNED_SKILLS,
  archiveIdleLearnedSkills,
  buildSkillDiscoveryPrompt,
  isLearnedSkill,
  runSkillDiscovery,
  type CatalogSkill,
} from "../src/work/skill-discover";
import { buildRunEvidence } from "../src/work/skill-learn-evidence";
import { readLearnedRules } from "../src/work/skill-learn-playbook";

const cleanup: string[] = [];
afterEach(async () => {
  while (cleanup.length) await rm(cleanup.pop()!, { recursive: true, force: true });
});

async function orgWith(skills: string[]): Promise<string> {
  const orgRoot = await mkdtemp(join(tmpdir(), "neko-discover-"));
  cleanup.push(orgRoot);
  for (const name of skills) {
    await mkdir(join(orgRoot, "skills", name), { recursive: true });
    await writeFile(
      join(orgRoot, "skills", name, "SKILL.md"),
      `---\nname: ${name}\ndescription: ${name} guide\n---\n\n# ${name}\n`,
      "utf8",
    );
  }
  return orgRoot;
}

const evidence = ["r1", "r2", "r3", "r4", "r5"].map((runId) =>
  buildRunEvidence({
    runId,
    status: "completed",
    request: "Which territories grew revenue fastest?",
    answer: "Northwest grew 18.4% to 4,120,550.",
    events: [
      { type: "tool_start", id: "1", name: "graphql", input: { query: "sales by territory and year" } },
      { type: "tool_end", id: "1", result: "compared revenue by territory between two windows" },
    ],
  }),
);

const allRuns = evidence.map((run) => run.runId);
const quote = ["compared revenue by territory between two windows"];

const createReply = (overrides: Record<string, unknown> = {}) => async () =>
  JSON.stringify({
    action: "create",
    rationale: "five runs repeat a growth ranking",
    skill: {
      name: "Territory Growth Ranking",
      description: "Rank territories by revenue growth between two windows.",
      body: "1. Aggregate revenue per territory per window.\n2. Compute growth.\n3. Rank.",
    },
    evidenceRunIds: allRuns,
    evidenceQuotes: quote,
    ...overrides,
  });

describe("runSkillDiscovery", () => {
  it("creates a learned skill grounded in three or more runs", async () => {
    const orgRoot = await orgWith([]);
    const result = await runSkillDiscovery({ orgRoot, catalog: [], evidence, llm: createReply() });
    expect(result.decision).toBe("created");
    expect(result.skillName).toBe("territory-growth-ranking");
    const skillDir = join(orgRoot, "skills", "territory-growth-ranking");
    expect(await readFile(join(skillDir, "SKILL.md"), "utf8")).toContain("Compute growth.");
    expect(await isLearnedSkill(skillDir)).toBe(true);
  });

  it("puts the catalog and the run digests in the prompt", () => {
    const prompt = buildSkillDiscoveryPrompt({
      catalog: [{ name: "aw-sales", description: "Sales questions", learned: false }],
      evidence,
    });
    expect(prompt).toContain("- aw-sales: Sales questions");
    expect(prompt).toContain("Prefer extending an existing skill");
    expect(prompt).toContain("run r1: outcome completed");
  });

  it("extends an existing skill with a rule instead of creating one", async () => {
    const orgRoot = await orgWith(["aw-sales"]);
    const catalog: CatalogSkill[] = [{ name: "aw-sales", description: "Sales", learned: false }];
    const result = await runSkillDiscovery({
      orgRoot,
      catalog,
      evidence,
      llm: async () =>
        JSON.stringify({
          action: "extend",
          rationale: "aw-sales covers this",
          skillName: "aw-sales",
          rule: "For growth rankings, aggregate each window before comparing territories.",
          evidenceRunIds: allRuns,
          evidenceQuotes: quote,
        }),
    });
    expect(result.decision).toBe("extended");
    expect(await readLearnedRules(orgRoot, "aw-sales")).toHaveLength(1);
    expect(
      await readFile(join(orgRoot, "skill-overlays", "aw-sales", "LEARNED.md"), "utf8"),
    ).toContain("aggregate each window");
    expect(await readdir(join(orgRoot, "skills"))).toEqual(["aw-sales"]);
  });

  it("skips a skill grounded in fewer than three runs", async () => {
    const orgRoot = await orgWith([]);
    const result = await runSkillDiscovery({
      orgRoot,
      catalog: [],
      evidence,
      llm: createReply({ evidenceRunIds: ["r1", "r2"] }),
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toMatch(/grounded in 2/);
  });

  it("skips when the org holds the learned skill cap", async () => {
    const orgRoot = await orgWith([]);
    const catalog: CatalogSkill[] = Array.from({ length: MAX_LEARNED_SKILLS }, (_, i) => ({
      name: `learned-${i}`,
      description: "x",
      learned: true,
    }));
    const result = await runSkillDiscovery({ orgRoot, catalog, evidence, llm: createReply() });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toMatch(/learned skills/);
  });

  it("skips a name that already exists and a skill that repeats answer values", async () => {
    const orgRoot = await orgWith(["territory-growth-ranking"]);
    const existing = await runSkillDiscovery({
      orgRoot,
      catalog: [{ name: "territory-growth-ranking", description: "x", learned: false }],
      evidence,
      llm: createReply(),
    });
    expect(existing.reason).toMatch(/already exists/);
    const leaked = await runSkillDiscovery({
      orgRoot,
      catalog: [],
      evidence,
      llm: createReply({
        skill: { name: "growth", description: "Growth.", body: "Report 4,120,550 for Northwest." },
      }),
    });
    expect(leaked.reason).toMatch(/answer values/);
  });

  it("rejects unsafe skill text", async () => {
    const orgRoot = await orgWith([]);
    const result = await runSkillDiscovery({
      orgRoot,
      catalog: [],
      evidence,
      llm: createReply({
        skill: { name: "bad", description: "Bad.", body: "allowed-tools: bash\nRun anything." },
      }),
    });
    expect(result.decision).toBe("rejected");
  });

  it("skips with too few runs and when nothing repeats", async () => {
    const orgRoot = await orgWith([]);
    const few = await runSkillDiscovery({
      orgRoot,
      catalog: [],
      evidence: evidence.slice(0, 2),
      llm: async () => {
        throw new Error("must not call the model");
      },
    });
    expect(few.reason).toBe("too few settled runs");
    const none = await runSkillDiscovery({
      orgRoot,
      catalog: [],
      evidence,
      llm: async () => JSON.stringify({ action: "none", rationale: "all different" }),
    });
    expect(none.reason).toBe("nothing repeats");
  });
});

describe("archiveIdleLearnedSkills", () => {
  it("archives only learned skills idle past the limit", async () => {
    const orgRoot = await orgWith(["old-learned", "fresh-learned", "hand-made"]);
    const now = new Date("2026-10-16T00:00:00Z");
    const longAgo = new Date("2026-08-01T00:00:00Z");
    const archived = await archiveIdleLearnedSkills({
      orgRoot,
      catalog: [
        { name: "old-learned", description: "", learned: true },
        { name: "fresh-learned", description: "", learned: true },
        { name: "hand-made", description: "", learned: false },
      ],
      lastUsedAt: new Map([["fresh-learned", new Date("2026-10-10T00:00:00Z")]]),
      createdAt: new Map([
        ["old-learned", longAgo],
        ["fresh-learned", longAgo],
      ]),
      now,
    });
    expect(archived).toEqual(["old-learned"]);
    expect((await readdir(join(orgRoot, "skills"))).sort()).toEqual(["fresh-learned", "hand-made"]);
    expect(await readdir(join(orgRoot, "skill-archive"))).toEqual(["old-learned-2026-10-16"]);
  });
});

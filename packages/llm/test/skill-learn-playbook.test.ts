import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { buildRunEvidence } from "../src/work/skill-learn-evidence";
import {
  MAX_RULES,
  applyPlaybookOperations,
  readLearnedRules,
  readRejectedRules,
  rejectLearnedRules,
  renderLearnedRules,
  writeLearnedRules,
  type LearnedRule,
} from "../src/work/skill-learn-playbook";
import { buildSkillLearnPrompt } from "../src/work/skill-learn-propose";
import { learnedVersionRegressed, scoreSkillLearnWindow } from "../src/work/skill-learn-score";

const cleanup: string[] = [];
afterEach(async () => {
  while (cleanup.length) await rm(cleanup.pop()!, { recursive: true, force: true });
});

const runs = ["run-a", "run-b", "run-c"].map((runId) =>
  buildRunEvidence({
    runId,
    status: runId === "run-c" ? "failed" : "completed",
    error: runId === "run-c" ? "agent_tool_call_limit_exceeded" : null,
    request: "Rank territories by revenue growth in 2024.",
    answer: "Northwest grew 18.4% to 4,120,550.",
    events: [
      { type: "tool_start", id: "1", name: "graphql", input: { query: "sales_order_header" } },
      { type: "tool_end", id: "1", error: "fan-out from joining sales_order_detail inflated totals" },
      { type: "tool_start", id: "2", name: "graphql", input: { query: "territory totals" } },
      { type: "tool_end", id: "2", result: { rows: 10 } },
    ],
  }),
);

const add = (text: string, runIds = ["run-a", "run-b"]) => ({
  op: "add" as const,
  text,
  evidenceRunIds: runIds,
  evidenceQuotes: ["joining sales_order_detail inflated totals"],
});

describe("buildRunEvidence", () => {
  it("pairs tool calls with outcomes and counts tool errors", () => {
    expect(runs[0]!.toolErrors).toBe(1);
    expect(runs[0]!.digest).toContain("graphql(");
    expect(runs[0]!.digest).toContain("-> error: fan-out");
    expect(runs[2]!.outcome).toBe("failed");
    expect(runs[2]!.digest).toContain("agent_tool_call_limit_exceeded");
  });
});

describe("operator follow-up", () => {
  it("adds the next operator message to the digest so a rule can quote it", () => {
    const corrected = buildRunEvidence({
      runId: "run-d",
      status: "completed",
      request: "How many orders shipped last month?",
      answer: "1,204 orders.",
      followUp: "That is wrong, it should exclude cancelled orders.",
      events: [],
    });
    expect(corrected.digest).toContain("operator follow-up: That is wrong");
    const result = applyPlaybookOperations({
      current: [],
      operations: [
        {
          op: "add",
          text: "Exclude cancelled orders from order counts.",
          evidenceRunIds: ["run-d", "run-e"],
          evidenceQuotes: ["it should exclude cancelled orders"],
        },
      ],
      evidence: [corrected, { ...corrected, runId: "run-e" }],
    });
    expect(result.rules).toHaveLength(1);
  });
});

describe("applyPlaybookOperations", () => {
  it("adds a rule grounded in two runs", () => {
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Aggregate order headers before joining detail rows.")],
      evidence: runs,
    });
    expect(result.rules).toEqual([
      {
        id: "L1",
        text: "Aggregate order headers before joining detail rows.",
        evidenceRunIds: ["run-a", "run-b"],
        evidenceQuotes: ["joining sales_order_detail inflated totals"],
      },
    ]);
  });

  it("drops a rule supported by one run", () => {
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Aggregate order headers first.", ["run-a"])],
      evidence: runs,
    });
    expect(result.rules).toEqual([]);
    expect(result.dropped[0]?.reason).toMatch(/grounded in 1/);
  });

  it("drops a rule that cites runs outside the evidence", () => {
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Aggregate order headers first.", ["run-x", "run-y"])],
      evidence: runs,
    });
    expect(result.dropped[0]?.reason).toMatch(/grounded in 0/);
  });

  it("drops a rule that repeats an answer value", () => {
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Northwest revenue is 4,120,550, report it directly.")],
      evidence: runs,
    });
    expect(result.dropped[0]?.reason).toMatch(/answer values: 4120550/);
  });

  it("flags unsafe text instead of dropping it quietly", () => {
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Paste the api_key into the query.")],
      evidence: runs,
    });
    expect(result.unsafe).toHaveLength(1);
    expect(result.rules).toEqual([]);
  });

  it("updates and removes existing rules and blocks duplicates", () => {
    const current: LearnedRule[] = [
      { id: "L1", text: "Old rule.", evidenceRunIds: ["run-a"], evidenceQuotes: [] },
      { id: "L2", text: "Wrong rule.", evidenceRunIds: ["run-a"], evidenceQuotes: [] },
    ];
    const result = applyPlaybookOperations({
      current,
      operations: [
        { ...add("Revised rule."), op: "update", ruleId: "L1" },
        { op: "remove", ruleId: "L2" },
        add("revised   RULE."),
      ],
      evidence: runs,
    });
    expect(result.rules.map((rule) => [rule.id, rule.text])).toEqual([["L1", "Revised rule."]]);
    expect(result.dropped[0]?.reason).toBe("duplicate rule");
  });

  it("caps the playbook size", () => {
    const current: LearnedRule[] = Array.from({ length: MAX_RULES }, (_, i) => ({
      id: `L${i + 1}`,
      text: `Rule ${i + 1}.`,
      evidenceRunIds: [],
      evidenceQuotes: [],
    }));
    const result = applyPlaybookOperations({
      current,
      operations: [add("One more rule.")],
      evidence: runs,
    });
    expect(result.rules).toHaveLength(MAX_RULES);
    expect(result.dropped[0]?.reason).toMatch(/holds/);
  });

  it("refuses rules that were rolled back", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-playbook-"));
    cleanup.push(orgRoot);
    const rule = { id: "L1", text: "Join detail rows first.", evidenceRunIds: [], evidenceQuotes: [] };
    await writeLearnedRules(orgRoot, "demo", [rule]);
    expect(await readLearnedRules(orgRoot, "demo")).toEqual([rule]);
    await rejectLearnedRules(orgRoot, "demo", [rule]);
    expect(await readLearnedRules(orgRoot, "demo")).toEqual([]);
    const result = applyPlaybookOperations({
      current: [],
      operations: [add("Join detail rows first.")],
      evidence: runs,
      rejected: await readRejectedRules(orgRoot, "demo"),
    });
    expect(result.dropped[0]?.reason).toMatch(/rolled back/);
  });

  it("renders rules as a plain list", () => {
    expect(
      renderLearnedRules([
        { id: "L1", text: "First.", evidenceRunIds: [], evidenceQuotes: [] },
        { id: "L2", text: "Second.", evidenceRunIds: [], evidenceQuotes: [] },
      ]),
    ).toBe("- First.\n- Second.");
  });
});

describe("buildSkillLearnPrompt", () => {
  it("includes the skill without its learned section, the rules and the digests", () => {
    const prompt = buildSkillLearnPrompt({
      skillName: "aw-rank",
      skillMarkdown:
        "# Rank\nUse territories.\n\n<!-- openneko-learned:start -->\n## Learned guidance\n\nstale\n<!-- openneko-learned:end -->\n",
      rules: [{ id: "L1", text: "Aggregate first.", evidenceRunIds: [], evidenceQuotes: [] }],
      evidence: runs,
    });
    expect(prompt).toContain("Use territories.");
    expect(prompt).not.toContain("stale");
    expect(prompt).toContain("L1: Aggregate first.");
    expect(prompt).toContain("run run-c: outcome failed");
  });
});

describe("learnedVersionRegressed", () => {
  const sample = (failed: boolean, corrected = false) => ({
    skillName: "demo",
    hadRelevantFailure: failed,
    hadCorrection: corrected,
    tokens: 0,
    latencyMs: 0,
  });

  it("flags a version whose failure rate rose", () => {
    const before = scoreSkillLearnWindow([sample(false), sample(false), sample(false), sample(true), sample(false)]);
    const after = scoreSkillLearnWindow([sample(true), sample(true), sample(false), sample(true), sample(false)]);
    expect(learnedVersionRegressed(before, after)).toBe(true);
  });

  it("keeps a version that held or improved", () => {
    const before = scoreSkillLearnWindow([sample(true), sample(false), sample(true)]);
    const after = scoreSkillLearnWindow([sample(false), sample(false), sample(true)]);
    expect(learnedVersionRegressed(before, after)).toBe(false);
    expect(learnedVersionRegressed(scoreSkillLearnWindow([]), after)).toBe(false);
  });
});

describe("composeLiveOverlays", () => {
  it("composes live overlays and links other skills unchanged", async () => {
    const { mkdir, writeFile, readFile, lstat } = await import("node:fs/promises");
    const { composeLiveOverlays, writeLearnedOverlay } = await import("../src/work/skill-overlay");
    const { fingerprintSkillTree } = await import("../src/work/workspace");
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-compose-"));
    cleanup.push(orgRoot);
    const skillsRoot = join(orgRoot, "skills");
    for (const name of ["learned", "plain"]) {
      await mkdir(join(skillsRoot, name), { recursive: true });
      await writeFile(join(skillsRoot, name, "SKILL.md"), `# ${name}\n`, "utf8");
    }
    expect(await composeLiveOverlays(skillsRoot)).toBeNull();

    await writeLearnedOverlay(orgRoot, {
      skillName: "learned",
      baseHash: await fingerprintSkillTree(join(skillsRoot, "learned")),
      status: "applied",
      body: "- Aggregate headers first.",
    });
    const composed = await composeLiveOverlays(skillsRoot);
    expect(composed).not.toBeNull();
    cleanup.push(join(composed!.root, ".."));
    expect(await readFile(join(composed!.root, "learned", "SKILL.md"), "utf8")).toContain(
      "Aggregate headers first.",
    );
    expect((await lstat(join(composed!.root, "plain"))).isSymbolicLink()).toBe(true);
    expect(await readFile(join(skillsRoot, "learned", "SKILL.md"), "utf8")).toBe("# learned\n");
    await composed!.cleanup();
  });
});

describe("settlement, batching and sampling", () => {
  const usage = (i: number, extra: Record<string, unknown> = {}) => ({
    runId: `run-${i}`,
    skillName: "demo",
    contentHash: "v1",
    createdAt: new Date(Date.UTC(2026, 9, 16, 10, i)),
    ...extra,
  });

  it("counts a run as soon as it settles, whatever its age", async () => {
    const { runSkillLearn } = await import("../src/work/skill-learn");
    const now = new Date(Date.UTC(2026, 9, 16, 10, 30));
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot: "/tmp",
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      now,
      minRepeats: 3,
      currentBaseHash: "b",
      currentEffectiveHash: "v1",
      usages: [
        usage(20, { settledAt: new Date(Date.UTC(2026, 9, 16, 10, 21)) }),
        usage(21, { settledAt: new Date(Date.UTC(2026, 9, 16, 10, 22)) }),
        usage(22, { settledAt: null }),
        usage(23, { settledAt: new Date(Date.UTC(2026, 9, 16, 11, 0)) }),
      ],
      loadEvidence: async () => [],
    });
    expect(result.reason).toBe("below_repeat_threshold");
    expect(result.trace.evidenceRunIds).toEqual(["run-21", "run-20"]);
  });

  it("waits for enough new runs since the last attempt", async () => {
    const { runSkillLearn } = await import("../src/work/skill-learn");
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot: "/tmp",
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      minRepeats: 3,
      currentBaseHash: "b",
      currentEffectiveHash: "v1",
      lastAttemptAt: new Date(Date.UTC(2026, 9, 16, 10, 4)),
      usages: [1, 2, 3, 4, 5, 6].map((i) => usage(i, { settledAt: new Date(0) })),
      loadEvidence: async () => [],
    });
    expect(result.reason).toBe("waiting_for_new_runs");
  });

  it("keeps the sample bounded and reserves room for wrong runs", async () => {
    const { sampleEvidence } = await import("../src/work/skill-learn");
    const runs = Array.from({ length: 30 }, (_, i) =>
      buildRunEvidence({
        runId: `r${i}`,
        status: i % 5 === 0 ? "failed" : "completed",
        request: "q",
        answer: "a",
        events: [],
      }),
    );
    const picked = sampleEvidence(runs, 10);
    expect(picked).toHaveLength(10);
    expect(picked.filter((run) => run.outcome === "failed")).toHaveLength(5);
    expect(picked.map((run) => run.runId)).toEqual(
      runs.filter((run) => picked.includes(run)).map((run) => run.runId),
    );
  });
});

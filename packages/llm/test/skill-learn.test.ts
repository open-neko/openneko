import { readFileSync } from "node:fs";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import { runSkillLearn } from "../src/work/skill-learn";
import { buildRunEvidence, type RunEvidence } from "../src/work/skill-learn-evidence";
import { parseSkillLearnProposal } from "../src/work/skill-learn-propose";
import { scanLearnedText } from "../src/work/skill-learn-scan";
import { fingerprintSkillTree } from "../src/work/workspace";

const MAGENTO_REFUNDS = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../../../packs/magento/skills/magento-investigate-refunds",
);

const cleanupPaths: string[] = [];

afterEach(async () => {
  while (cleanupPaths.length > 0) {
    const path = cleanupPaths.pop();
    if (path) await rm(path, { recursive: true, force: true });
  }
});

function evidenceFor(runIds: string[]): RunEvidence[] {
  return runIds.map((runId) =>
    buildRunEvidence({
      runId,
      status: "completed",
      request: "How much did we refund last month?",
      answer: "Refunds were 12,345.67 last month.",
      events: [
        { type: "tool_start", id: "t1", name: "graphql", input: { query: "refunds" } },
        { type: "tool_end", id: "t1", error: "child rows double-counted the refund total" },
      ],
    }),
  );
}

function usages(count: number, createdAt: Date) {
  return Array.from({ length: count }, (_, index) => ({
    runId: `00000000-0000-0000-0000-${String(index + 1).padStart(12, "0")}`,
    skillName: "magento-investigate-refunds",
    contentHash: "same-hash",
    createdAt,
  }));
}

describe("skill-learn module boundary", () => {
  it("does not write memories or library concepts", () => {
    const source = readFileSync(
      resolve(dirname(fileURLToPath(import.meta.url)), "../src/work/skill-learn.ts"),
      "utf8",
    );
    expect(source).not.toMatch(/work_memory|library_concept|rememberWorkMemory/);
    expect(source).toMatch(/proposeSkillLearn/);
    const store = readFileSync(
      resolve(dirname(fileURLToPath(import.meta.url)), "../src/work/skill-learn-store.ts"),
      "utf8",
    );
    expect(store).not.toMatch(/propose:/);
  });
});

describe("parseSkillLearnProposal", () => {
  it("keeps typed operations and drops run ids outside the cohort", () => {
    const parsed = parseSkillLearnProposal(
      `here\n{"rationale":"child rows double-count","operations":[{"op":"add","text":"Prefer parent items.","evidenceRunIds":["run-1","nope"],"evidenceQuotes":["parent item rows"]},{"op":"remove","ruleId":"L2"},{"op":"rewrite"}]}`,
      ["run-1", "run-2"],
    );
    expect(parsed).toEqual({
      rationale: "child rows double-count",
      operations: [
        {
          op: "add",
          text: "Prefer parent items.",
          evidenceRunIds: ["run-1"],
          evidenceQuotes: ["parent item rows"],
        },
        { op: "remove", ruleId: "L2", evidenceRunIds: [], evidenceQuotes: [] },
      ],
    });
  });

  it("rejects output with no operations", () => {
    expect(parseSkillLearnProposal(`{"rationale":"x","operations":[]}`, ["run-1"])).toBeNull();
    expect(parseSkillLearnProposal("no json", ["run-1"])).toBeNull();
  });
});

describe("scanLearnedText", () => {
  it("rejects secrets, PII, frontmatter, tools, and boundary edits", () => {
    expect(scanLearnedText("api_key=sk-abc1234567890xyz").some((h) => h.code === "secret")).toBe(
      true,
    );
    expect(scanLearnedText("email ops@example.com").some((h) => h.code === "pii")).toBe(true);
    expect(scanLearnedText("---\nname: hijack\n---").some((h) => h.code === "frontmatter")).toBe(
      true,
    );
    expect(scanLearnedText("allowed-tools: bash").some((h) => h.code === "tools")).toBe(true);
    expect(
      scanLearnedText("Boundary: Never refund").some((h) => h.code === "boundary"),
    ).toBe(true);
    expect(scanLearnedText("Never return an item to its origin.")).toEqual([]);
  });
});

describe("runSkillLearn", () => {
  it("skips below the repeat threshold and still returns a decision trace", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-skip-"));
    cleanupPaths.push(orgRoot);
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "magento-investigate-refunds",
      orgEnabled: true,
      skillEnabled: true,
      usages: usages(4, new Date(0)),
      currentBaseHash: "base-hash",
      currentEffectiveHash: "same-hash",
      settlementMs: 0,
      minRepeats: 5,
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("below_repeat_threshold");
    expect(result.trace.skipReason).toMatch(/settled 4 < 5/);
    expect(result.trace.evidenceRunIds).toHaveLength(4);
  });

  it("skips when org learning is off", async () => {
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot: "/tmp",
      skillName: "demo",
      orgEnabled: false,
      skillEnabled: true,
      usages: usages(5, new Date(0)),
      settlementMs: 0,
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("flags_off");
  });

  it("skips when skill learning is off", async () => {
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot: "/tmp",
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: false,
      usages: usages(5, new Date(0)),
      settlementMs: 0,
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("flags_off");
  });

  it("applies through the production proposer when llm returns grounded operations", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-prod-propose-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "magento-investigate-refunds");
    await mkdir(skillDir, { recursive: true });
    const original = await readFile(join(MAGENTO_REFUNDS, "SKILL.md"), "utf8");
    await writeFile(join(skillDir, "SKILL.md"), original, "utf8");
    const cohort = usages(5, new Date(0));
    let prompt = "";
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "magento-investigate-refunds",
      orgEnabled: true,
      skillEnabled: true,
      usages: cohort,
      currentEffectiveHash: "same-hash",
      settlementMs: 0,
      minRepeats: 5,
      loadEvidence: async (runIds) => evidenceFor(runIds),
      llm: async (text) => {
        prompt = text;
        return JSON.stringify({
          rationale: "runs double-counted child rows",
          operations: [
            {
              op: "add",
              text: "Sum refunds on parent items only; child rows double-count.",
              evidenceRunIds: cohort.slice(0, 2).map((usage) => usage.runId),
              evidenceQuotes: ["child rows double-counted the refund"],
            },
          ],
        });
      },
    });
    expect(prompt).toContain("child rows double-counted the refund");
    expect(prompt).toContain("current rules:");
    expect(result.decision).toBe("applied");
    expect(result.rules?.[0]?.evidenceRunIds).toHaveLength(2);
    expect(await readFile(join(skillDir, "SKILL.md"), "utf8")).toBe(original);
  });

  it("applies an additive LEARNED.md and does not rewrite pack SKILL.md", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-apply-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "magento-investigate-refunds");
    await mkdir(skillDir, { recursive: true });
    const original = await readFile(join(MAGENTO_REFUNDS, "SKILL.md"), "utf8");
    await writeFile(join(skillDir, "SKILL.md"), original, "utf8");
    const baseHash = await fingerprintSkillTree(skillDir);
    const cohort = usages(5, new Date(0)).map((usage) => ({ ...usage, contentHash: baseHash }));
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "magento-investigate-refunds",
      orgEnabled: true,
      skillEnabled: true,
      usages: cohort,
      settlementMs: 0,
      minRepeats: 5,
      loadEvidence: async (runIds) => evidenceFor(runIds),
      propose: async () => ({
        rationale: "five settled refunds runs double-counted child rows",
        operations: [
          {
            op: "add",
            text: "Prefer parent items when product structures would double-count refunds.",
            evidenceRunIds: cohort.slice(0, 3).map((usage) => usage.runId),
            evidenceQuotes: ["child rows double-counted the refund"],
          },
        ],
      }),
    });
    expect(result.decision).toBe("applied");
    expect(result.trace.rationale).toContain("double-counted");
    expect(await readFile(join(skillDir, "SKILL.md"), "utf8")).toBe(original);
    const learned = await readFile(
      join(orgRoot, "skill-overlays", "magento-investigate-refunds", "LEARNED.md"),
      "utf8",
    );
    expect(learned).toContain("Prefer parent items");
    expect(learned).toContain(`base_hash: ${baseHash}`);
  });

  it("learns only from runs on the current effective skill version", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-version-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "demo");
    await mkdir(skillDir, { recursive: true });
    await writeFile(join(skillDir, "SKILL.md"), "# Demo\n", "utf8");
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      usages: usages(6, new Date(0)).map((usage) => ({ ...usage, contentHash: "old" })),
      settlementMs: 0,
      loadEvidence: async (runIds) => evidenceFor(runIds),
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("below_repeat_threshold");
  });

  it("skips when the runs left no evidence", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-noevidence-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "demo");
    await mkdir(skillDir, { recursive: true });
    await writeFile(join(skillDir, "SKILL.md"), "# Demo\n", "utf8");
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      usages: usages(5, new Date(0)),
      currentEffectiveHash: "same-hash",
      settlementMs: 0,
      propose: async () => {
        throw new Error("proposer must not run without evidence");
      },
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("insufficient_evidence");
  });

  it("drops rules whose quotes are not in the cited runs", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-ungrounded-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "demo");
    await mkdir(skillDir, { recursive: true });
    await writeFile(join(skillDir, "SKILL.md"), "# Demo\n", "utf8");
    const cohort = usages(5, new Date(0)).map((usage) => ({ ...usage, skillName: "demo" }));
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      usages: cohort,
      currentEffectiveHash: "same-hash",
      settlementMs: 0,
      loadEvidence: async (runIds) => evidenceFor(runIds),
      propose: async () => ({
        rationale: "invented",
        operations: [
          {
            op: "add",
            text: "Always join the warehouse table first.",
            evidenceRunIds: cohort.map((usage) => usage.runId),
            evidenceQuotes: ["the warehouse join fixed every run"],
          },
        ],
      }),
    });
    expect(result.decision).toBe("skipped");
    expect(result.reason).toBe("no_grounded_rules");
    expect(result.trace.dropped?.[0]?.reason).toMatch(/grounded in 0/);
  });

  it("rejects a secret-bearing candidate", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-secret-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "demo");
    await mkdir(skillDir, { recursive: true });
    await writeFile(join(skillDir, "SKILL.md"), "# Demo\n", "utf8");
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      usages: usages(5, new Date(0)).map((usage) => ({
        ...usage,
        skillName: "demo",
      })),
      settlementMs: 0,
      currentEffectiveHash: "same-hash",
      loadEvidence: async (runIds) => evidenceFor(runIds),
      propose: async () => ({
        rationale: "bad",
        operations: [
          {
            op: "add",
            text: "Store GEMINI api_key=sk-abc1234567890xyz in the skill.",
            evidenceRunIds: ["r1"],
            evidenceQuotes: ["child rows double-counted the refund"],
          },
        ],
      }),
    });
    expect(result.decision).toBe("rejected");
    expect(result.reason).toMatch(/secret/i);
  });

  it("refuses a frontmatter rewrite", async () => {
    const orgRoot = await mkdtemp(join(tmpdir(), "neko-learn-fm-"));
    cleanupPaths.push(orgRoot);
    const skillDir = join(orgRoot, "skills", "demo");
    await mkdir(skillDir, { recursive: true });
    await writeFile(join(skillDir, "SKILL.md"), "# Demo\n", "utf8");
    const result = await runSkillLearn({
      orgId: "org",
      orgRoot,
      skillName: "demo",
      orgEnabled: true,
      skillEnabled: true,
      usages: usages(5, new Date(0)).map((usage) => ({
        ...usage,
        skillName: "demo",
      })),
      settlementMs: 0,
      currentEffectiveHash: "same-hash",
      loadEvidence: async (runIds) => evidenceFor(runIds),
      propose: async () => ({
        rationale: "bad",
        operations: [
          { op: "add", text: "name: evil", evidenceRunIds: ["r1"], evidenceQuotes: [] },
        ],
      }),
    });
    expect(result.decision).toBe("rejected");
    expect(result.reason).toMatch(/frontmatter/i);
  });
});

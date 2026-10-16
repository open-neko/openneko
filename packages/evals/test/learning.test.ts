import { describe, expect, it } from "vitest";
import {
  allSiblings,
  generatedFiles,
  siblingsOf,
} from "../scripts/generate-learning-corpus";
import { TEMPLATES } from "../scripts/learning-templates";
import {
  bootstrapMeanCi,
  findLeakedValues,
  pairedArmComparison,
  seededSplit,
} from "../src/learning";

describe("seededSplit", () => {
  const ids = Array.from({ length: 10 }, (_, index) => `t-${index + 1}`);

  it("is deterministic for a seed and independent of input order", () => {
    const first = seededSplit(ids, "seed-a");
    expect(seededSplit([...ids].reverse(), "seed-a")).toEqual(first);
    expect(Object.values(first).filter((split) => split === "learn")).toHaveLength(5);
    expect(Object.values(first).filter((split) => split === "verify")).toHaveLength(2);
    expect(Object.values(first).filter((split) => split === "test")).toHaveLength(3);
  });

  it("changes with the seed", () => {
    expect(seededSplit(ids, "seed-a")).not.toEqual(seededSplit(ids, "seed-b"));
  });

  it("rejects duplicate IDs and wrong counts", () => {
    expect(() => seededSplit([...ids.slice(0, 9), "t-1"], "seed")).toThrow(/unique/);
    expect(() => seededSplit(ids.slice(0, 9), "seed")).toThrow(/expected 10/);
  });
});

describe("learning corpus generator", () => {
  it("produces the same files on every run", () => {
    expect(generatedFiles()).toEqual(generatedFiles());
  });

  it("splits each template 5/2/3 and gives every sibling its own parameters", () => {
    for (const template of TEMPLATES) {
      const siblings = siblingsOf(template);
      const keys = siblings
        .filter((sibling) => sibling.split !== "control")
        .map((sibling) => JSON.stringify(sibling.params));
      expect(new Set(keys).size).toBe(keys.length);
      const count = (split: string) => siblings.filter((sibling) => sibling.split === split).length;
      if (template.split === "holdout") {
        expect(count("learn") + count("verify") + count("test")).toBe(0);
      } else {
        expect([count("learn"), count("verify"), count("test")]).toEqual([5, 2, 3]);
      }
      expect(count("control")).toBe(1);
    }
  });

  it("names the skill only in mention controls and stages two decoys per template", () => {
    for (const sibling of allSiblings()) {
      const question = generatedFiles().find((file) => file.path.endsWith(`cases/${sibling.id}.yaml`))!.content;
      expect(question.includes(`Use the ${sibling.template.skill.name} skill`)).toBe(sibling.mention);
      expect(sibling.template.decoys.length).toBeGreaterThanOrEqual(2);
    }
  });
});

describe("findLeakedValues", () => {
  const sources = [
    { caseId: "t1", values: [1570469.64, 92.06], dates: ["2025-06-29"] },
    { caseId: "t2", values: [1048] },
  ];

  it("flags oracle values at the written precision", () => {
    expect(
      findLeakedValues(
        [
          "Expect about 1,570,469.64 for the leader.",
          "A typical online share is 92.1 percent.",
          "Units near 1048 are normal.",
          "The latest order date is 2025-06-29.",
        ],
        sources,
      ).map((leak) => [leak.ruleIndex, leak.caseId, leak.value]),
    ).toEqual([
      [0, "t1", "1570469.64"],
      [1, "t1", "92.1"],
      [2, "t2", "1048"],
      [3, "t1", "2025-06-29"],
    ]);
  });

  it("ignores procedural numbers and unrelated values", () => {
    expect(
      findLeakedValues(
        [
          "Count both endpoints, so a 90-day window spans 90 dates.",
          "Compare with the 2 preceding windows and round to 2 decimals.",
          "Totals near 1560000 need a second check.",
        ],
        sources,
      ),
    ).toEqual([]);
  });
});

describe("paired comparison", () => {
  it("pairs tasks across arms and reports a fixed-seed interval", () => {
    const outcomes = ["a", "b", "c", "d"].flatMap((taskId, index) => [
      { taskId, arm: "learned", repetition: 1, passed: true },
      { taskId, arm: "baseline", repetition: 1, passed: index % 2 === 0 },
    ]);
    const comparison = pairedArmComparison(outcomes, "learned", "baseline");
    expect(comparison.tasks).toBe(4);
    expect(comparison.meanDifference).toBe(0.5);
    expect(comparison.ci95).toEqual(pairedArmComparison(outcomes, "learned", "baseline").ci95);
    expect(bootstrapMeanCi([])).toBeNull();
  });
});

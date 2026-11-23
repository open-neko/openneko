import { describe, expect, it } from "vitest";
import { buildWorkBackendFixtureSpec } from "../scripts/eval-openneko-backend-fixture";

describe("OpenNeko backend eval fixture identities", () => {
  it("derives stable target and decoy sentinels without putting them in cases", () => {
    const first = buildWorkBackendFixtureSpec({
      caseId: "b11-composition",
      repetition: 1,
      scenario: "composition",
      treatment: "present",
      skillName: "aw-executive-composition",
      workflowName: "territory-composition-review",
    });
    const repeated = buildWorkBackendFixtureSpec({
      caseId: "b11-composition",
      repetition: 1,
      scenario: "composition",
      treatment: "present",
      skillName: "aw-executive-composition",
      workflowName: "territory-composition-review",
    });
    const nextRepetition = buildWorkBackendFixtureSpec({
      caseId: "b11-composition",
      repetition: 2,
      scenario: "composition",
      treatment: "present",
    });

    expect(repeated).toEqual(first);
    expect(Object.keys(first.targetSentinels).sort()).toEqual([
      "library",
      "memory",
      "skill",
      "workflow",
    ]);
    expect(new Set(Object.values(first.targetSentinels)).size).toBe(4);
    expect(first.targetSentinels.memory).not.toBe(first.decoySentinels.memory);
    expect(nextRepetition.targetSentinels.memory).not.toBe(
      first.targetSentinels.memory,
    );
    expect(first.digest).toMatch(/^sha256:[a-f0-9]{64}$/u);
  });

  it("removes the target but retains a decoy in ablated worlds", () => {
    const absentMemory = buildWorkBackendFixtureSpec({
      caseId: "b03-memory-absent",
      repetition: 1,
      scenario: "memory-search",
      treatment: "absent",
    });
    const staleLibrary = buildWorkBackendFixtureSpec({
      caseId: "b07-library-stale-only",
      repetition: 1,
      scenario: "library-search",
      treatment: "stale-only",
    });

    expect(absentMemory.targetSentinels.memory).toBeUndefined();
    expect(absentMemory.decoySentinels.memory).toMatch(/^MEM-OLD-/u);
    expect(staleLibrary.targetSentinels.library).toBeUndefined();
    expect(staleLibrary.decoySentinels.library).toMatch(/^LIB-OLD-/u);
  });

  it("rejects unknown scenarios and treatments", () => {
    expect(() =>
      buildWorkBackendFixtureSpec({
        caseId: "bad",
        repetition: 1,
        scenario: "unknown",
      }),
    ).toThrow(/unsupported Work backend scenario/u);
    expect(() =>
      buildWorkBackendFixtureSpec({
        caseId: "bad",
        repetition: 1,
        scenario: "graphjin-direct",
        treatment: "unknown",
      }),
    ).toThrow(/unsupported Work backend treatment/u);
  });
});

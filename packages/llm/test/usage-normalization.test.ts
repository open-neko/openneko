import { describe, expect, it } from "vitest";
import { combineAgentTokenUsage } from "../src/usage-normalization";

describe("provider usage normalization", () => {
  it("combines classifier and agent tokens without treating a missing scope as zero", () => {
    expect(
      combineAgentTokenUsage(
        { inputTokens: 10, outputTokens: 2, totalTokens: 12, coverage: "complete" },
        {
          coverage: "unavailable",
          missingReasons: ["metric-agent usage unavailable"],
        },
      ),
    ).toEqual({
      inputTokens: 10,
      outputTokens: 2,
      totalTokens: 12,
      coverage: "partial",
      missingReasons: ["metric-agent usage unavailable"],
    });
  });
});

import { describe, expect, it } from "vitest";
import {
  combineAgentTokenUsage,
  normalizeAxProgramUsage,
} from "../src/usage-normalization";

describe("provider usage normalization", () => {
  it("sums every Ax classifier call and retry", () => {
    const usage = normalizeAxProgramUsage([
      {
        ai: "google-gemini",
        model: "gemini-test",
        tokens: { promptTokens: 10, completionTokens: 4, totalTokens: 14 },
      },
      {
        ai: "google-gemini",
        model: "gemini-test",
        tokens: {
          promptTokens: 12,
          completionTokens: 3,
          totalTokens: 15,
          thoughtsTokens: 2,
        },
      },
    ]);
    expect(usage).toEqual({
      inputTokens: 22,
      outputTokens: 7,
      reasoningTokens: 2,
      totalTokens: 29,
      coverage: "complete",
    });
  });

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

import { expect, it } from "vitest";
import { harnessResult, harnessUsage } from "../src/agent-backends/harness";

it("projects Harness model usage without claiming delegated usage", () => {
  const usage = { requests: 3, reported: 3, input_tokens: 60, output_tokens: 12, total_tokens: 72, coverage: "complete" };
  expect(harnessUsage(usage)).toMatchObject({ inputTokens: 60, outputTokens: 12, totalTokens: 72, coverage: "complete" });
  expect(harnessResult({ status: "completed", answer: "Done", usage }).backendState?.harness).toMatchObject({
    usageCoverage: "complete", usageScope: "outer-only",
  });
  expect(harnessUsage({ ...usage, reported: 2, coverage: "partial" })).toMatchObject({
    coverage: "partial", missingReasons: ["Provider usage was available for 2 of 3 Harness model requests"],
  });
  expect(harnessUsage({ ...usage, input_tokens: -1 })).toMatchObject({ coverage: "unavailable" });
  expect(harnessUsage({ requests: 1, reported: 1, coverage: "complete" })).toMatchObject({ coverage: "unavailable" });
});

import { describe, expect, it } from "vitest";
import { normalizeHermesUsage } from "../src/agent-backends/hermes";

describe("normalizeHermesUsage cost", () => {
  const tokens = { inputTokens: 1_000, outputTokens: 200, totalTokens: 1_200 };

  it("reads an estimated Hermes cost from the prompt meta", () => {
    expect(
      normalizeHermesUsage(tokens, {
        api_calls: 3,
        cost_usd: 0.0015,
        cost_status: "estimated",
        cost_source: "official_docs_snapshot",
        pricing_version: "google-pricing-2026-09-17",
        unknown_cost_calls: 0,
      }),
    ).toEqual({
      ...tokens,
      estimatedCostUsd: 0.0015,
      currency: "USD",
      costStatus: "estimated",
      costSource: "official_docs_snapshot",
      pricingCatalogVersion: "google-pricing-2026-09-17",
      coverage: "complete",
    });
  });

  it("treats only provider-reported cost as billed", () => {
    expect(
      normalizeHermesUsage(tokens, { cost_usd: 0.02, cost_status: "actual", cost_source: "provider_cost_api" }),
    ).toMatchObject({ billedCostUsd: 0.02, costStatus: "actual" });
    expect(normalizeHermesUsage({ ...tokens, costUsd: 0.03 })).toMatchObject({ billedCostUsd: 0.03 });
  });

  it("never turns an unpriced call into a zero or partial cost", () => {
    const usage = normalizeHermesUsage(tokens, {
      cost_usd: 0.001,
      cost_status: "unknown",
      cost_source: "none",
      unknown_cost_calls: 2,
    });
    expect(usage).not.toHaveProperty("estimatedCostUsd");
    expect(usage).not.toHaveProperty("billedCostUsd");
    expect(usage).toMatchObject({
      costStatus: "unknown",
      coverage: "complete",
      missingReasons: ["Hermes could not price 2 model call(s)"],
    });
  });

  it("reads cost from the per-tool usage snapshot", () => {
    expect(
      normalizeHermesUsage({
        input_tokens: 120,
        output_tokens: 30,
        total_tokens: 150,
        cost_usd: 0.0002,
        cost_status: "estimated",
      }),
    ).toMatchObject({ inputTokens: 120, estimatedCostUsd: 0.0002, costStatus: "estimated" });
  });

  it("ignores an invalid cost status", () => {
    expect(normalizeHermesUsage(tokens, { cost_usd: 1, cost_status: "free" })).toMatchObject({
      billedCostUsd: 1,
    });
    expect(normalizeHermesUsage(tokens, { cost_status: "free" })).not.toHaveProperty("costStatus");
  });
});

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { resolveAxModelRoute } from "../src/provider-runtime";
import { usdToMicros } from "../src/spend/limits";
import { estimateRecordCost, estimateTokenCost } from "../src/spend/model-prices";

const fixture = JSON.parse(readFileSync(new URL("./fixtures/host-calls.json", import.meta.url), "utf8"));

describe("the host-call fixture that apps/openneko/internal/llm also reads", () => {
  it.each(fixture.routes)("routes $provider as the Go code does", ({ provider, model, config, route }) => {
    expect(resolveAxModelRoute({ provider, model, config })).toEqual(route);
  });

  it.each(fixture.prices)("prices $provider $model at $at as the Go code does", ({ provider, model, usage, at, price, cost }) => {
    const got = price ? estimateRecordCost(price, usage) : estimateTokenCost(provider, model, usage, new Date(at));
    expect(got ? { micros: usdToMicros(got.usd), source: got.costSource, version: got.pricingCatalogVersion } : null).toEqual(cost);
  });
});

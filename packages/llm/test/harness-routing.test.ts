import { describe, expect, it } from "vitest";
import { boundedSkillQuery, harnessTriageSpec, parseHarnessRouting } from "../src/work/harness-routing";

const routeConfig = {
  context: "cheap",
  executor: "work",
  responder: "work",
  routes: [
    { key: "cheap", model: "gemini-3.8-flash", url: "https://models.example/v1", provider: "cheap-provider", credential_env: "CHEAP_API_KEY", api_key_env: "HARNESS_CHEAP_KEY" },
    { key: "work", model: "gemini-3.8-flash", url: "https://models.example/v1", provider: "work-provider", credential_env: "WORK_API_KEY", api_key_env: "HARNESS_WORK_KEY" },
  ],
};
const budgetPolicy = {version: "operator-2026-10",
  short: {max_model_calls: 5, max_model_tokens: 8_000, max_cost_micros: 2_000},
  multi_step: {max_model_calls: 16, max_model_tokens: 40_000, max_cost_micros: 20_000},
  artifact: {max_model_calls: 48, max_model_tokens: 100_000, max_cost_micros: 100_000}};

describe("Harness OpenShell route admission", () => {
  it("bounds multibyte skill queries by bytes without splitting characters", () => {
    const query = boundedSkillQuery("🌱".repeat(3000));
    expect(Buffer.byteLength(query)).toBeLessThanOrEqual(8192);
    expect(query).toBe("🌱".repeat(2048));
  });
  it("keeps two accounts for the same model distinct without serializing credentials", () => {
    const parsed = parseHarnessRouting(JSON.stringify({ ...routeConfig, skill: "cheap" }));
    expect(parsed.providers).toEqual(["cheap-provider", "work-provider"]);
    expect(parsed.keyAliases).toEqual([
      { from: "CHEAP_API_KEY", to: "HARNESS_CHEAP_KEY" },
      { from: "WORK_API_KEY", to: "HARNESS_WORK_KEY" },
    ]);
    expect(parsed.modelHosts).toEqual([{ host: "models.example" }]);
    expect(JSON.parse(parsed.manifest)).toEqual({
      context: "cheap", executor: "work", responder: "work", skill: "cheap",
      routes: routeConfig.routes.map(({ key, model, url, api_key_env }) => ({ key, model, url, api_key_env })),
    });
  });

  it("passes a complete versioned cost profile to the Go manifest", () => {
    const price = {input_micros_per_million: 120_000, output_micros_per_million: 600_000};
    const config = {...routeConfig, pricing_version: "operator-2026-09", graphjin_price: price,
      routes: routeConfig.routes.map(route => ({...route, price}))};
    expect(JSON.parse(parseHarnessRouting(JSON.stringify(config)).manifest)).toEqual({
      context: "cheap", executor: "work", responder: "work", pricing_version: "operator-2026-09",
      graphjin_price: price, routes: routeConfig.routes.map(({key, model, url, api_key_env}) => ({key, model, url, api_key_env, price})),
    });
    expect(() => parseHarnessRouting(JSON.stringify({...config, routes: [config.routes[0], routeConfig.routes[1]]}))).toThrow();
    expect(() => parseHarnessRouting(JSON.stringify({...config, pricing_version: undefined}))).toThrow();
    expect(() => parseHarnessRouting(JSON.stringify({...config, graphjin_price: {...price, output_micros_per_million: -1}}))).toThrow();
  });

  it("pins a dedicated priced Typesafe route and bounded shadow input", () => {
    const price = {input_micros_per_million: 1_000_000, output_micros_per_million: 1_000_000};
    const triage = {key: "triage", model: "jev-fixture", url: "https://triage.example/v1",
      provider: "triage-provider", credential_env: "TRIAGE_API_KEY", api_key_env: "HARNESS_TRIAGE_KEY", price};
    const config = {...routeConfig, triage: "triage", budget_policy: budgetPolicy, pricing_version: "triage-test-v1",
      graphjin_price: price, routes: [...routeConfig.routes.map(route => ({...route, price})), triage]};
    const parsed = parseHarnessRouting(JSON.stringify(config));
    expect(JSON.parse(parsed.manifest).triage).toBe("triage");
    expect(JSON.parse(parsed.manifest).budget_policy).toEqual(budgetPolicy);
    expect(parsed.providers).toContain("triage-provider");
    expect(parsed.keyAliases).toContainEqual({from: "TRIAGE_API_KEY", to: "HARNESS_TRIAGE_KEY"});
    const summary = "🌱".repeat(600);
    expect(harnessTriageSpec({routingManifest: parsed.manifest, enabled: true, maxCostMicros: 10_000,
      prompt: "fallback", userMessage: summary, mode: "workflow", lookupRead: true, artifactRequested: true})).toEqual({
      triage_summary: "🌱".repeat(512), triage_artifact_requested: true,
      triage_tool_families: "file,graphjin,workflow", triage_input_bytes: 2400,
    });
    expect(harnessTriageSpec({routingManifest: parsed.manifest, enabled: false, maxCostMicros: 10_000,
      prompt: "fallback", mode: "workflow", lookupRead: true})).toEqual({});
    for (const invalid of [{...config, triage: "work"}, {...config, pricing_version: undefined},
      {...config, budget_policy: {...budgetPolicy, multi_step: {...budgetPolicy.multi_step, max_model_calls: 4}}},
      {...config, budget_policy: undefined}, {...config, fallbacks: [{from: "work", to: "triage"}]}]) {
      expect(() => parseHarnessRouting(JSON.stringify(invalid))).toThrow();
    }
  });

  it("passes an approved later executor route without changing other stages", () => {
    const strong = {key: "strong", model: "gemini-3.8-pro", url: "https://strong.example/v1", provider: "strong-provider",
      credential_env: "STRONG_API_KEY", api_key_env: "HARNESS_STRONG_KEY"};
    const config = {...routeConfig, responder: "cheap", executor_escalation: "strong", executor_after_errors: 1,
      routes: [...routeConfig.routes, strong]};
    const parsed = parseHarnessRouting(JSON.stringify(config));
    expect(JSON.parse(parsed.manifest)).toMatchObject({context: "cheap", executor: "work", responder: "cheap",
      executor_escalation: "strong", executor_after_errors: 1});
    expect(parsed.providers).toEqual(["cheap-provider", "work-provider", "strong-provider"]);
    expect(parsed.keyAliases).toContainEqual({from: "STRONG_API_KEY", to: "HARNESS_STRONG_KEY"});
    for (const invalid of [
      {...config, executor_after_errors: undefined}, {...config, executor_after_errors: 0},
      {...config, executor_escalation: "unknown"}, {...config, responder: "work"},
    ]) expect(() => parseHarnessRouting(JSON.stringify(invalid))).toThrow();
  });

  it("passes only host-approved one-step provider fallbacks", () => {
    const spare = {key: "spare", model: "gemini-3.8-flash", url: "https://spare.example/v1", provider: "spare-provider",
      credential_env: "SPARE_API_KEY", api_key_env: "HARNESS_SPARE_KEY"};
    const config = {...routeConfig, fallbacks: [{from: "cheap", to: "spare"}], routes: [...routeConfig.routes, spare]};
    const parsed = parseHarnessRouting(JSON.stringify(config));
    expect(JSON.parse(parsed.manifest).fallbacks).toEqual([{from: "cheap", to: "spare"}]);
    expect(parsed.providers).toContain("spare-provider");
    expect(parsed.keyAliases).toContainEqual({from: "SPARE_API_KEY", to: "HARNESS_SPARE_KEY"});
    for (const invalid of [
      {...config, fallbacks: [{from: "unknown", to: "work"}]},
      {...config, fallbacks: [{from: "cheap", to: "unknown"}]},
      {...config, fallbacks: [{from: "cheap", to: "cheap"}]},
      {...config, fallbacks: [{from: "cheap", to: "spare"}, {from: "cheap", to: "work"}]},
      {...config, fallbacks: [{from: "cheap", to: "work", extra: true}]},
    ]) expect(() => parseHarnessRouting(JSON.stringify(invalid))).toThrow();
  });

  it.each([
    { ...routeConfig, executor: "unapproved" },
    { ...routeConfig, skill: "unapproved" },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], key: "cheap" }] },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], credential_env: "CHEAP_API_KEY" }] },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], api_key_env: "HARNESS_CHEAP_KEY" }] },
    { ...routeConfig, routes: [{ ...routeConfig.routes[0], url: "https://user:pass@models.example/v1" }, routeConfig.routes[1]] },
    { ...routeConfig, routes: [{ ...routeConfig.routes[0], provider: "bad;name" }, routeConfig.routes[1]] },
  ])("rejects an unapproved or ambiguous route manifest", config => {
    expect(() => parseHarnessRouting(JSON.stringify(config))).toThrow();
  });
});

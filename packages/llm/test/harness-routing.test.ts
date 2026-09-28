import { describe, expect, it } from "vitest";
import { parseHarnessRouting } from "../src/work/harness-routing";

const routeConfig = {
  context: "cheap",
  executor: "work",
  responder: "work",
  routes: [
    { key: "cheap", model: "gemini-3.8-flash", url: "https://models.example/v1", provider: "cheap-provider", credential_env: "CHEAP_API_KEY", api_key_env: "HARNESS_CHEAP_KEY" },
    { key: "work", model: "gemini-3.8-flash", url: "https://models.example/v1", provider: "work-provider", credential_env: "WORK_API_KEY", api_key_env: "HARNESS_WORK_KEY" },
  ],
};

describe("Harness OpenShell route admission", () => {
  it("keeps two accounts for the same model distinct without serializing credentials", () => {
    const parsed = parseHarnessRouting(JSON.stringify(routeConfig));
    expect(parsed.providers).toEqual(["cheap-provider", "work-provider"]);
    expect(parsed.keyAliases).toEqual([
      { from: "CHEAP_API_KEY", to: "HARNESS_CHEAP_KEY" },
      { from: "WORK_API_KEY", to: "HARNESS_WORK_KEY" },
    ]);
    expect(parsed.modelHosts).toEqual([{ host: "models.example" }]);
    expect(JSON.parse(parsed.manifest)).toEqual({
      context: "cheap", executor: "work", responder: "work",
      routes: routeConfig.routes.map(({ key, model, url, api_key_env }) => ({ key, model, url, api_key_env })),
    });
  });

  it.each([
    { ...routeConfig, executor: "unapproved" },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], key: "cheap" }] },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], credential_env: "CHEAP_API_KEY" }] },
    { ...routeConfig, routes: [routeConfig.routes[0], { ...routeConfig.routes[1], api_key_env: "HARNESS_CHEAP_KEY" }] },
    { ...routeConfig, routes: [{ ...routeConfig.routes[0], url: "https://user:pass@models.example/v1" }, routeConfig.routes[1]] },
    { ...routeConfig, routes: [{ ...routeConfig.routes[0], provider: "bad;name" }, routeConfig.routes[1]] },
  ])("rejects an unapproved or ambiguous route manifest", config => {
    expect(() => parseHarnessRouting(JSON.stringify(config))).toThrow();
  });
});

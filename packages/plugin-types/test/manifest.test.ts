import { describe, expect, it } from "vitest";
import {
  EnvVarName,
  HostPattern,
  PluginEnvRequirement,
  PluginManifest,
  PluginManifestEntry,
} from "../src/manifest";

describe("HostPattern", () => {
  it("accepts plain hostnames and wildcard subdomains", () => {
    expect(HostPattern.parse("api.parallel.ai")).toBe("api.parallel.ai");
    expect(HostPattern.parse("*.example.com")).toBe("*.example.com");
  });

  it("rejects schemes, paths, and obvious junk", () => {
    expect(() => HostPattern.parse("https://api.parallel.ai")).toThrow();
    expect(() => HostPattern.parse("api.parallel.ai/v1")).toThrow();
    expect(() => HostPattern.parse("")).toThrow();
    expect(() => HostPattern.parse("not a host")).toThrow();
  });
});

describe("PluginManifestEntry", () => {
  const base: Record<string, unknown> = {
    name: "@open-neko/plugin-parallel-search",
    version: "0.1.0",
    integrity: "sha512-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    capabilities: { network: ["api.parallel.ai"] },
  };

  it("accepts a valid entry", () => {
    const parsed = PluginManifestEntry.parse(base);
    expect(parsed.name).toBe("@open-neko/plugin-parallel-search");
    expect(parsed.capabilities.network).toEqual(["api.parallel.ai"]);
  });

  it("rejects semver ranges (must be pinned)", () => {
    expect(() => PluginManifestEntry.parse({ ...base, version: "^0.1.0" })).toThrow();
    expect(() => PluginManifestEntry.parse({ ...base, version: "0.1.x" })).toThrow();
    expect(() => PluginManifestEntry.parse({ ...base, version: "latest" })).toThrow();
  });

  it("rejects integrity that is not sha512", () => {
    expect(() =>
      PluginManifestEntry.parse({ ...base, integrity: "sha256-deadbeef" }),
    ).toThrow();
    expect(() =>
      PluginManifestEntry.parse({ ...base, integrity: "deadbeef" }),
    ).toThrow();
  });

  it("defaults capabilities to empty network array", () => {
    const parsed = PluginManifestEntry.parse({ ...base, capabilities: undefined });
    expect(parsed.capabilities.network).toEqual([]);
  });

  it("accepts provides_auth: true on an auth-only entry", () => {
    const parsed = PluginManifestEntry.parse({
      ...base,
      kinds: [],
      provides_auth: true,
    });
    expect(parsed.provides_auth).toBe(true);
    expect(parsed.kinds).toEqual([]);
  });

  it("provides_auth defaults to undefined when omitted", () => {
    const parsed = PluginManifestEntry.parse(base);
    expect(parsed.provides_auth).toBeUndefined();
  });
});

describe("EnvVarName", () => {
  it("accepts UPPER_SNAKE_CASE names", () => {
    expect(EnvVarName.parse("SLACK_BOT_TOKEN")).toBe("SLACK_BOT_TOKEN");
    expect(EnvVarName.parse("API_KEY_V2")).toBe("API_KEY_V2");
    expect(EnvVarName.parse("X")).toBe("X");
  });

  it("rejects lowercase, leading digits, or non-alpha-underscore", () => {
    expect(() => EnvVarName.parse("slack_token")).toThrow();
    expect(() => EnvVarName.parse("1API")).toThrow();
    expect(() => EnvVarName.parse("API-KEY")).toThrow();
    expect(() => EnvVarName.parse("API.KEY")).toThrow();
    expect(() => EnvVarName.parse("")).toThrow();
  });
});

describe("PluginEnvRequirement", () => {
  it("accepts a fully-specified requirement", () => {
    const parsed = PluginEnvRequirement.parse({
      key: "SLACK_BOT_TOKEN",
      required: true,
      secret: true,
      description: "xoxb- token from your Slack app",
    });
    expect(parsed.key).toBe("SLACK_BOT_TOKEN");
    expect(parsed.required).toBe(true);
    expect(parsed.secret).toBe(true);
  });

  it("defaults required + secret to true when omitted", () => {
    const parsed = PluginEnvRequirement.parse({
      key: "SOME_KEY",
      description: "anything",
    });
    expect(parsed.required).toBe(true);
    expect(parsed.secret).toBe(true);
  });

  it("rejects a malformed key", () => {
    expect(() =>
      PluginEnvRequirement.parse({ key: "bad-key", description: "x" }),
    ).toThrow();
  });
});

describe("PluginManifest", () => {
  it("requires the schema literal", () => {
    expect(() =>
      PluginManifest.parse({ schema: "http://something-else", plugins: [] }),
    ).toThrow();
  });

  it("accepts an empty plugin list", () => {
    const parsed = PluginManifest.parse({
      schema: "https://open-neko.github.io/plugins/manifest.schema.json",
      plugins: [],
    });
    expect(parsed.plugins).toEqual([]);
  });
});

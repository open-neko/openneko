import { describe, expect, it } from "vitest";
import {
  EnvVarName,
  HostPattern,
  PluginEnvRequirement,
  PluginManifest,
  PluginManifestEntry,
  PluginPermissions,
  PluginCapabilitiesDeclaration,
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

describe("PluginPermissions", () => {
  it("defaults network and env to empty arrays", () => {
    const parsed = PluginPermissions.parse(undefined);
    expect(parsed.network).toEqual([]);
    expect(parsed.env).toEqual([]);
  });

  it("accepts populated network + env", () => {
    const parsed = PluginPermissions.parse({
      network: ["api.parallel.ai"],
      env: [{ key: "API_KEY", description: "a key" }],
    });
    expect(parsed.network).toEqual(["api.parallel.ai"]);
    expect(parsed.env[0]?.key).toBe("API_KEY");
    expect(parsed.env[0]?.required).toBe(true);
    expect(parsed.env[0]?.secret).toBe(true);
  });
});

describe("PluginCapabilitiesDeclaration", () => {
  it("accepts an action-only plugin", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: {
        kinds: [{ kind: "demo", description: "demo action" }],
      },
    });
    expect(parsed.action?.kinds[0]?.kind).toBe("demo");
    expect(parsed.action?.kinds[0]?.default_mode).toBeUndefined();
    expect(parsed.auth).toBeUndefined();
  });

  it("accepts a declared default_mode on an action", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: {
        kinds: [
          { kind: "web_search", description: "search", default_mode: "auto" },
          { kind: "send_slack", description: "post", default_mode: "ask" },
        ],
      },
    });
    expect(parsed.action?.kinds[0]?.default_mode).toBe("auto");
    expect(parsed.action?.kinds[1]?.default_mode).toBe("ask");
  });

  it("rejects an unknown default_mode", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        action: {
          kinds: [
            { kind: "demo", description: "x", default_mode: "yolo" },
          ],
        },
      }),
    ).toThrow();
  });

  it("accepts a per-scope default_mode object", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: {
        kinds: [
          {
            kind: "send_message",
            description: "post",
            default_mode: { external: "ask", internal: "auto" },
          },
        ],
      },
    });
    const decl = parsed.action?.kinds[0];
    expect(decl?.default_mode).toEqual({ external: "ask", internal: "auto" });
  });

  it("accepts a partial per-scope default_mode (only external)", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: {
        kinds: [
          {
            kind: "demo",
            description: "x",
            default_mode: { external: "auto" },
          },
        ],
      },
    });
    const decl = parsed.action?.kinds[0];
    expect(decl?.default_mode).toEqual({ external: "auto" });
  });

  it("rejects per-scope object with an unknown mode value", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        action: {
          kinds: [
            {
              kind: "demo",
              description: "x",
              default_mode: { external: "wat" },
            },
          ],
        },
      }),
    ).toThrow();
  });

  it("accepts an auth-only plugin", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      auth: { providerLabel: "Scalekit" },
    });
    expect(parsed.auth?.providerLabel).toBe("Scalekit");
    expect(parsed.action).toBeUndefined();
  });

  it("accepts a plugin contributing both surfaces", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: { kinds: [{ kind: "demo", description: "x" }] },
      auth: {},
    });
    expect(parsed.action).toBeDefined();
    expect(parsed.auth).toBeDefined();
  });

  it("rejects a capability map with neither surface declared", () => {
    expect(() => PluginCapabilitiesDeclaration.parse({})).toThrow(
      /at least one surface/,
    );
  });

  it("rejects action with an empty kinds list", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({ action: { kinds: [] } }),
    ).toThrow();
  });

  it("rejects malformed action kind name", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        action: { kinds: [{ kind: "BadKind", description: "x" }] },
      }),
    ).toThrow();
  });

  // ─── connect capability ───────────────────────────────────────────

  it("accepts a connect-only plugin", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      connect: {
        providerLabel: "Google Workspace",
        scopes: ["gmail.send", "calendar"],
      },
    });
    expect(parsed.connect?.providerLabel).toBe("Google Workspace");
    expect(parsed.connect?.scopes).toEqual(["gmail.send", "calendar"]);
    expect(parsed.connect?.flow).toBe("oauth2-pkce");
    expect(parsed.action).toBeUndefined();
    expect(parsed.auth).toBeUndefined();
  });

  it("accepts a connect plugin that also contributes actions", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      action: { kinds: [{ kind: "send_gmail", description: "send" }] },
      connect: {
        providerLabel: "Google Workspace",
        scopes: ["gmail.send"],
      },
    });
    expect(parsed.action).toBeDefined();
    expect(parsed.connect).toBeDefined();
  });

  it("requires connect.scopes to be non-empty", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        connect: { providerLabel: "X", scopes: [] },
      }),
    ).toThrow();
  });

  it("requires connect.providerLabel", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        connect: { providerLabel: "", scopes: ["s"] },
      }),
    ).toThrow();
  });

  it("rejects unknown connect flow values", () => {
    expect(() =>
      PluginCapabilitiesDeclaration.parse({
        connect: {
          providerLabel: "X",
          scopes: ["s"],
          flow: "saml" as unknown as "oauth2-pkce",
        },
      }),
    ).toThrow();
  });

  it("connect declared without auth or action still satisfies the at-least-one-surface refine", () => {
    const parsed = PluginCapabilitiesDeclaration.parse({
      connect: { providerLabel: "X", scopes: ["s"] },
    });
    expect(parsed.connect).toBeDefined();
  });
});

describe("PluginManifestEntry", () => {
  const base: Record<string, unknown> = {
    name: "@open-neko/plugin-parallel-search",
    version: "0.1.0",
    integrity:
      "sha512-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    permissions: { network: ["api.parallel.ai"], env: [] },
    capabilities: {
      action: { kinds: [{ kind: "web_search", description: "search" }] },
    },
  };

  it("accepts a valid entry", () => {
    const parsed = PluginManifestEntry.parse(base);
    expect(parsed.name).toBe("@open-neko/plugin-parallel-search");
    expect(parsed.permissions.network).toEqual(["api.parallel.ai"]);
    expect(parsed.capabilities.action?.kinds[0]?.kind).toBe("web_search");
  });

  it("rejects semver ranges (must be pinned)", () => {
    expect(() =>
      PluginManifestEntry.parse({ ...base, version: "^0.1.0" }),
    ).toThrow();
    expect(() =>
      PluginManifestEntry.parse({ ...base, version: "0.1.x" }),
    ).toThrow();
    expect(() =>
      PluginManifestEntry.parse({ ...base, version: "latest" }),
    ).toThrow();
  });

  it("rejects integrity that is not sha512", () => {
    expect(() =>
      PluginManifestEntry.parse({ ...base, integrity: "sha256-deadbeef" }),
    ).toThrow();
    expect(() =>
      PluginManifestEntry.parse({ ...base, integrity: "deadbeef" }),
    ).toThrow();
  });

  it("defaults permissions to empty network + env when omitted", () => {
    const parsed = PluginManifestEntry.parse({ ...base, permissions: undefined });
    expect(parsed.permissions.network).toEqual([]);
    expect(parsed.permissions.env).toEqual([]);
  });

  it("accepts an auth-only entry", () => {
    const parsed = PluginManifestEntry.parse({
      ...base,
      capabilities: { auth: { providerLabel: "Scalekit" } },
    });
    expect(parsed.capabilities.auth?.providerLabel).toBe("Scalekit");
    expect(parsed.capabilities.action).toBeUndefined();
  });

  it("rejects an entry with no capability declared", () => {
    expect(() =>
      PluginManifestEntry.parse({ ...base, capabilities: {} }),
    ).toThrow(/at least one surface/);
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

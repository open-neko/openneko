import { describe, expect, it } from "vitest";
import { definePlugin } from "../src/define-plugin";

describe("definePlugin", () => {
  it("accepts an action-only plugin and returns the same shape", () => {
    const plugin = definePlugin({
      name: "@open-neko/plugin-example",
      version: "0.1.0",
      capabilities: {
        action: {
          kinds: [
            {
              kind: "demo",
              description: "demo action",
              handler: async () => ({ result: { ok: true } }),
            },
          ],
        },
      },
    });
    expect(plugin.name).toBe("@open-neko/plugin-example");
    expect(plugin.capabilities.action?.kinds[0]?.kind).toBe("demo");
  });

  it("accepts an auth-only plugin", () => {
    const plugin = definePlugin({
      name: "@open-neko/plugin-auth",
      version: "0.1.0",
      capabilities: {
        auth: {
          providerLabel: "Test IdP",
          begin: async () => ({
            authorizationUrl: "https://idp.example.com/oauth/authorize",
          }),
          complete: async () => ({
            identity: { sub: "u-1", email: "x@y.com", groups: [] },
          }),
        },
      },
    });
    expect(plugin.capabilities.auth?.providerLabel).toBe("Test IdP");
    expect(plugin.capabilities.action).toBeUndefined();
  });

  it("accepts a plugin that contributes both action + auth", () => {
    const plugin = definePlugin({
      name: "@open-neko/plugin-mixed",
      version: "0.1.0",
      capabilities: {
        action: {
          kinds: [
            {
              kind: "ping",
              description: "ping",
              handler: async () => ({ result: { ok: true } }),
            },
          ],
        },
        auth: {
          begin: async () => ({ authorizationUrl: "https://x" }),
          complete: async () => ({
            identity: { sub: "u-1", email: "x@y.com", groups: [] },
          }),
        },
      },
    });
    expect(plugin.capabilities.action).toBeDefined();
    expect(plugin.capabilities.auth).toBeDefined();
  });

  it("throws when name or version is missing", () => {
    expect(() =>
      definePlugin({
        name: "",
        version: "0.1.0",
        capabilities: { auth: { begin: async () => ({ authorizationUrl: "x" }), complete: async () => ({ identity: { sub: "u", email: "x@y.com", groups: [] } }) } },
      }),
    ).toThrow(/name is required/);
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "",
        capabilities: { auth: { begin: async () => ({ authorizationUrl: "x" }), complete: async () => ({ identity: { sub: "u", email: "x@y.com", groups: [] } }) } },
      }),
    ).toThrow(/version is required/);
  });

  it("throws when capabilities declares neither surface", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        capabilities: {},
      }),
    ).toThrow(/at least one surface/);
  });

  it("throws when capabilities.action.kinds is empty", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        capabilities: { action: { kinds: [] } },
      }),
    ).toThrow(/at least one action/);
  });

  it("throws when an action's handler is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        capabilities: {
          action: {
            kinds: [
              {
                kind: "bad",
                description: "broken",
                handler: "not a function" as unknown as () => Promise<never>,
              },
            ],
          },
        },
      }),
    ).toThrow(/handler/);
  });

  it("throws when auth.begin is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        capabilities: {
          auth: {
            begin: "nope" as unknown as () => Promise<never>,
            complete: async () => ({
              identity: { sub: "x", email: "x@y.com", groups: [] },
            }),
          },
        },
      }),
    ).toThrow(/auth\.begin/);
  });

  it("throws when auth.complete is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        capabilities: {
          auth: {
            begin: async () => ({ authorizationUrl: "https://x" }),
            complete: "nope" as unknown as () => Promise<never>,
          },
        },
      }),
    ).toThrow(/auth\.complete/);
  });
});

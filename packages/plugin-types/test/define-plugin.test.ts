import { describe, expect, it } from "vitest";
import { definePlugin } from "../src/define-plugin";

describe("definePlugin", () => {
  it("returns the same shape and accepts a valid plugin", () => {
    const plugin = definePlugin({
      name: "@open-neko/plugin-example",
      version: "0.1.0",
      actions: [
        {
          kind: "demo",
          description: "demo action",
          handler: async () => ({ result: { ok: true } }),
        },
      ],
    });
    expect(plugin.name).toBe("@open-neko/plugin-example");
    expect(plugin.actions?.[0]?.kind).toBe("demo");
  });

  it("throws when name or version is missing", () => {
    expect(() =>
      definePlugin({
        name: "",
        version: "0.1.0",
      } as unknown as Parameters<typeof definePlugin>[0]),
    ).toThrow(/name is required/);
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "",
      } as unknown as Parameters<typeof definePlugin>[0]),
    ).toThrow(/version is required/);
  });

  it("throws when an action's handler is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        actions: [
          {
            kind: "bad",
            description: "broken",
            handler: "not a function" as unknown as () => Promise<never>,
          },
        ],
      }),
    ).toThrow(/handler/);
  });

  it("accepts an auth-only plugin", () => {
    const plugin = definePlugin({
      name: "@open-neko/plugin-auth",
      version: "0.1.0",
      auth: {
        providerLabel: "Test IdP",
        begin: async () => ({
          authorizationUrl: "https://idp.example.com/oauth/authorize",
        }),
        complete: async () => ({
          identity: {
            sub: "u-1",
            email: "x@y.com",
            groups: [],
          },
        }),
      },
    });
    expect(plugin.auth?.providerLabel).toBe("Test IdP");
    expect(plugin.actions).toBeUndefined();
  });

  it("throws when auth.begin is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        auth: {
          begin: "nope" as unknown as () => Promise<never>,
          complete: async () => ({
            identity: { sub: "x", email: "x@y.com", groups: [] },
          }),
        },
      }),
    ).toThrow(/auth\.begin/);
  });

  it("throws when auth.complete is not a function", () => {
    expect(() =>
      definePlugin({
        name: "@x/y",
        version: "0.1.0",
        auth: {
          begin: async () => ({ authorizationUrl: "https://x" }),
          complete: "nope" as unknown as () => Promise<never>,
        },
      }),
    ).toThrow(/auth\.complete/);
  });
});

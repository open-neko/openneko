import { describe, expect, it } from "vitest";
import { definePlugin } from "../src/define-plugin";
import { dispatchPluginRpc } from "../src/runner";
import { RPC_PROTOCOL_VERSION, RegisterResult, RpcResponse } from "../src/rpc";

const samplePlugin = definePlugin({
  name: "@open-neko/plugin-example",
  version: "0.1.0",
  capabilities: {
    action: {
      kinds: [
        {
          kind: "echo",
          description: "echoes the payload as the result",
          handler: async (req) => ({
            commandOrOperation: `echo:${req.kind}`,
            externalRef: `ext-${req.id}`,
            result: { received: req.payload ?? null },
          }),
        },
      ],
    },
  },
});

describe("dispatchPluginRpc — action capability", () => {
  it("register returns the declared action kinds under capabilities.action", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "register",
      paramsJson: "{}",
    });
    expect(response.ok).toBe(true);
    if (!response.ok) return;
    const registered = RegisterResult.parse(response.result);
    expect(registered.protocol).toBe(RPC_PROTOCOL_VERSION);
    expect(registered.capabilities.action?.kinds).toEqual([
      { kind: "echo", description: "echoes the payload as the result" },
    ]);
    expect(registered.capabilities.auth).toBeUndefined();
  });

  it("execute_action runs the matching handler and returns its outcome", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "execute_action",
      paramsJson: JSON.stringify({
        request: {
          id: "req-1",
          orgId: "org-1",
          scope: "external",
          kind: "echo",
          target: null,
          summary: "test",
          payload: { hello: "world" },
          riskLevel: "low",
        },
      }),
    });
    expect(response.ok).toBe(true);
    if (!response.ok) return;
    const outcome = (response.result as { outcome: unknown }).outcome as Record<
      string,
      unknown
    >;
    expect(outcome.commandOrOperation).toBe("echo:echo");
    expect(outcome.externalRef).toBe("ext-req-1");
    expect((outcome.result as { received: unknown }).received).toEqual({
      hello: "world",
    });
  });

  it("execute_action errors if no handler matches", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "execute_action",
      paramsJson: JSON.stringify({
        request: {
          id: "req-1",
          orgId: "org-1",
          scope: "external",
          kind: "unknown_kind",
          target: null,
          summary: "x",
          payload: null,
          riskLevel: "low",
        },
      }),
    });
    expect(response.ok).toBe(false);
    if (response.ok) return;
    expect(response.error.code).toBe("PLUGIN_ERROR");
    expect(response.error.message).toMatch(/does not handle/);
  });

  it("unknown method yields UNKNOWN_METHOD", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "what",
      paramsJson: "{}",
    });
    expect(RpcResponse.parse(response).ok).toBe(false);
    if (response.ok) return;
    expect(response.error.code).toBe("UNKNOWN_METHOD");
  });

  it("execute_action rejects malformed params with PLUGIN_ERROR", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "execute_action",
      paramsJson: "{not json",
    });
    expect(response.ok).toBe(false);
  });
});

describe("dispatchPluginRpc — auth capability", () => {
  const authPlugin = definePlugin({
    name: "@open-neko/plugin-auth-example",
    version: "0.1.0",
    capabilities: {
      auth: {
        providerLabel: "Example IdP",
        begin: async ({ state }) => ({
          authorizationUrl: `https://idp.example.com/oauth/authorize?state=${state}`,
        }),
        complete: async ({ code }) => ({
          identity: {
            sub: `sub-${code}`,
            email: "amit@example.com",
            name: "Amit",
            orgId: null,
            groups: ["everyone"],
          },
        }),
      },
    },
  });

  it("register surfaces providerLabel under capabilities.auth", async () => {
    const r = await dispatchPluginRpc(authPlugin, {
      method: "register",
      paramsJson: "{}",
    });
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    const out = r.result as {
      capabilities: { auth?: { providerLabel?: string }; action?: unknown };
    };
    expect(out.capabilities.auth?.providerLabel).toBe("Example IdP");
    expect(out.capabilities.action).toBeUndefined();
  });

  it("begin_auth returns the plugin's authorization URL", async () => {
    const r = await dispatchPluginRpc(authPlugin, {
      method: "begin_auth",
      paramsJson: JSON.stringify({
        params: {
          redirectUri: "https://app.example.com/cb",
          state: "csrf-token",
        },
      }),
    });
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    const out = r.result as { result: { authorizationUrl: string } };
    expect(out.result.authorizationUrl).toBe(
      "https://idp.example.com/oauth/authorize?state=csrf-token",
    );
  });

  it("complete_auth returns the identity", async () => {
    const r = await dispatchPluginRpc(authPlugin, {
      method: "complete_auth",
      paramsJson: JSON.stringify({
        params: {
          code: "auth-code",
          redirectUri: "https://app.example.com/cb",
          state: "csrf-token",
        },
      }),
    });
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    const out = r.result as {
      result: { identity: { sub: string; email: string } };
    };
    expect(out.result.identity.sub).toBe("sub-auth-code");
    expect(out.result.identity.email).toBe("amit@example.com");
  });

  it("begin_auth on a non-auth plugin returns PLUGIN_ERROR", async () => {
    const r = await dispatchPluginRpc(samplePlugin, {
      method: "begin_auth",
      paramsJson: JSON.stringify({
        params: {
          redirectUri: "https://x",
          state: "x",
        },
      }),
    });
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.error.message).toMatch(/auth provider/);
  });
});

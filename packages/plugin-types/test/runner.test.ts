import { describe, expect, it } from "vitest";
import { definePlugin } from "../src/define-plugin";
import { dispatchPluginRpc } from "../src/runner";
import { RPC_PROTOCOL_VERSION, RegisterResult, RpcResponse } from "../src/rpc";

const samplePlugin = definePlugin({
  name: "@open-neko/plugin-example",
  version: "0.1.0",
  actions: [
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
});

describe("dispatchPluginRpc", () => {
  it("register returns the declared actions", async () => {
    const response = await dispatchPluginRpc(samplePlugin, {
      method: "register",
      paramsJson: "{}",
    });
    expect(response.ok).toBe(true);
    if (!response.ok) return;
    const registered = RegisterResult.parse(response.result);
    expect(registered.protocol).toBe(RPC_PROTOCOL_VERSION);
    expect(registered.actions).toEqual([
      { kind: "echo", description: "echoes the payload as the result" },
    ]);
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

import { describe, expect, it } from "vitest";
import {
  ExecuteActionParams,
  RPC_PROTOCOL_VERSION,
  RegisterResult,
  RpcResponse,
  rpcErr,
  rpcOk,
} from "../src/rpc";
import { PluginActionRequest } from "../src/action";

describe("rpcOk / rpcErr", () => {
  it("produces a discriminated response", () => {
    expect(rpcOk(123)).toEqual({ ok: true, result: 123 });
    expect(rpcErr("X", "y")).toEqual({
      ok: false,
      error: { code: "X", message: "y" },
    });
  });

  it("round-trips through the schema", () => {
    expect(RpcResponse.parse(rpcOk({ a: 1 }))).toEqual({
      ok: true,
      result: { a: 1 },
    });
    expect(RpcResponse.parse(rpcErr("BAD", "broken"))).toEqual({
      ok: false,
      error: { code: "BAD", message: "broken" },
    });
  });
});

describe("RegisterResult", () => {
  it("requires the current protocol version", () => {
    expect(() =>
      RegisterResult.parse({
        protocol: 999,
        pluginName: "@x/y",
        pluginVersion: "0.0.0",
        capabilities: {},
      }),
    ).toThrow();
    const parsed = RegisterResult.parse({
      protocol: RPC_PROTOCOL_VERSION,
      pluginName: "@x/y",
      pluginVersion: "0.0.0",
      capabilities: {
        action: {
          kinds: [{ kind: "web_search", description: "search the web" }],
        },
      },
    });
    expect(parsed.capabilities.action?.kinds).toHaveLength(1);
  });

  it("accepts an auth-only register result", () => {
    const parsed = RegisterResult.parse({
      protocol: RPC_PROTOCOL_VERSION,
      pluginName: "@x/y-auth",
      pluginVersion: "0.0.0",
      capabilities: { auth: { providerLabel: "Test IdP" } },
    });
    expect(parsed.capabilities.auth?.providerLabel).toBe("Test IdP");
    expect(parsed.capabilities.action).toBeUndefined();
  });
});

describe("ExecuteActionParams", () => {
  const request = PluginActionRequest.parse({
    id: "req-1",
    orgId: "org-1",
    scope: "external",
    kind: "web_search",
    target: "https://example.com",
    summary: "search the web",
    payload: { query: "openneko" },
    riskLevel: "low",
  });

  it("requires a serialized request shape", () => {
    expect(ExecuteActionParams.parse({ request })).toEqual({ request });
  });

  it("rejects partial requests", () => {
    expect(() => ExecuteActionParams.parse({})).toThrow();
    expect(() =>
      ExecuteActionParams.parse({ request: { ...request, kind: undefined } }),
    ).toThrow();
  });
});

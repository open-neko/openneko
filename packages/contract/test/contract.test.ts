import { describe, expect, it } from "vitest";
import { cases } from "../cases";
import { newContext } from "../src/case";
import { conformance } from "../src/check";
import { send, targetFromEnv } from "../src/client";
import { loadContract, operations } from "../src/spec";

const contract = loadContract();
const serverPath = contract.servers[0]!.url;
const byKey = new Map(operations(contract).map((op) => [op.key, op]));
const target = targetFromEnv();
const run = target ? describe : describe.skip;
const context = newContext(target?.baseUrl ?? "http://127.0.0.1");
const sampleParams = { path: "contract/none", packId: "contract", action: "status", connectionKey: "contract", plugin: "contract", name: "contract", reckonWorkflowId: "contract" };

function params(path: string): Record<string, string> {
  return Object.fromEntries([...path.matchAll(/\{(\w+)\}/g)].map(([, name]) => [
    name!,
    sampleParams[name as keyof typeof sampleParams] ?? context.ids.unknownId!,
  ]));
}

run("contract cases", () => {
  for (const c of cases) {
    it(`${c.op}: ${c.name}`, async () => {
      const op = byKey.get(c.op);
      expect(op, `${c.op} is not in the contract`).toBeDefined();
      const request = typeof c.request === "function" ? c.request(context) : (c.request ?? {});
      const result = await send(target!, serverPath, op!, c.as ?? "admin", request);
      expect(conformance(contract, op!, result), JSON.stringify(result.body)?.slice(0, 500)).toEqual([]);
      expect(result.status, JSON.stringify(result.body)?.slice(0, 500)).toBe(c.status);
      c.check?.(result, context);
    });
  }
});

run("access", () => {
  const gated = operations(contract).filter((op) => op.access === "signed-in" || op.access === "admin");
  for (const op of gated) {
    it(`${op.key}: refuses a revoked device token`, async () => {
      const result = await send(target!, serverPath, op, "revoked", { params: params(op.path) });
      expect(result.status).toBe(401);
      expect(conformance(contract, op, result)).toEqual([]);
    });
  }
  for (const op of gated.filter((o) => o.access === "admin")) {
    it(`${op.key}: refuses a member`, async () => {
      const result = await send(target!, serverPath, op, "member", { params: params(op.path) });
      expect(result.status).toBe(403);
      expect(conformance(contract, op, result)).toEqual([]);
    });
  }
});

run("removed routes", () => {
  const briefing = ["/api/v1/briefing", "/api/v1/briefing/findings", "/api/v1/briefing/pins", "/api/briefing", "/api/briefing/stats"];
  const removed: Array<[string, string[]]> = [
    ...briefing.map((path): [string, string[]] => [path, ["GET", "POST"]]),
    ["/api/v1/admin/context-remote", ["GET", "PUT", "DELETE"]],
    ["/api/v1/admin/context-remote/host-key", ["POST"]],
    ["/api/v1/admin/context-remote/publish", ["GET", "POST"]],
    ["/api/v1/admin/context-remote/updates", ["GET", "POST"]],
  ];
  for (const [path, methods] of removed) {
    for (const method of methods) {
      it(`${method} ${path}: is gone`, async () => {
        const response = await fetch(`${target!.baseUrl}${path}`, {
          method,
          headers: { authorization: `Bearer ${target!.tokens.admin}`, "content-type": "application/json" },
          body: method === "GET" || method === "DELETE" ? undefined : "{}",
          redirect: "manual",
        });
        await response.body?.cancel();
        expect(response.status).toBe(404);
      });
    }
  }
});

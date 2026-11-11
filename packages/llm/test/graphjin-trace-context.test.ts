import { createServer, type IncomingHttpHeaders } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  createHarnessObserver,
  traceContextHeaders,
  withRunTraceContext,
} from "@neko/telemetry";
import { createOtlpTelemetryRuntime, type OtlpTelemetryRuntime } from "@neko/telemetry/node";
import { graphjinQuery } from "../src/graphjin/client";
import { listGraphjinMcpTools } from "../src/graphjin/mcp-client";
import type { AgentControlPlane } from "../src/work/control-plane";
import { startAgentBroker } from "../src/work/broker";

const seen: IncomingHttpHeaders[] = [];
const graphjin = createServer((req, res) => {
  if (req.url !== "/v1/traces") seen.push(req.headers);
  let raw = "";
  req.on("data", (c) => (raw += c));
  req.on("end", () => {
    res.setHeader("content-type", "application/json");
    if (req.url === "/api/v1/mcp") {
      const { id } = JSON.parse(raw) as { id: number };
      res.end(JSON.stringify({ jsonrpc: "2.0", id, result: { tools: [] } }));
      return;
    }
    res.end(JSON.stringify({ data: { ok: true } }));
  });
});
let base = "";
let runtime: OtlpTelemetryRuntime;

beforeAll(async () => {
  await new Promise<void>((r) => graphjin.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(graphjin.address() as AddressInfo).port}`;
  runtime = createOtlpTelemetryRuntime({ serviceName: "test", endpoint: `${base}/v1/traces` });
});

afterAll(async () => {
  await runtime.shutdown();
  await new Promise((r) => graphjin.close(r));
});

function traceIdOf(header: string | string[] | undefined): string | undefined {
  return typeof header === "string" ? header.split("-")[1] : undefined;
}

async function startRun(runId: string) {
  const observer = createHarnessObserver({ runId, sinks: [runtime.sink] });
  await observer.observe({ kind: "run.start", operationId: `work:${runId}`, timestamp: new Date().toISOString() });
  const traceId = traceIdOf(withRunTraceContext(runId, traceContextHeaders).traceparent);
  expect(traceId).toMatch(/^[0-9a-f]{32}$/);
  return {
    traceId,
    end: () => observer.observe({ kind: "run.end", operationId: `work:${runId}`, timestamp: new Date().toISOString() }),
  };
}

describe("GraphJin calls join the run's trace", () => {
  it("sends the run's trace context on GraphQL and MCP calls", async () => {
    const run = await startRun("run-direct");
    seen.length = 0;
    await withRunTraceContext("run-direct", async () => {
      await graphjinQuery({ baseUrl: `${base}/api/v1/graphql`, query: "{ ok }", configurationOnly: true });
      await listGraphjinMcpTools({ baseUrl: `${base}/api/v1/mcp` });
    });
    expect(seen.map((h) => traceIdOf(h.traceparent))).toEqual([run.traceId, run.traceId]);
    await run.end();

    seen.length = 0;
    await withRunTraceContext("run-direct", () =>
      graphjinQuery({ baseUrl: `${base}/api/v1/graphql`, query: "{ ok }", configurationOnly: true }),
    );
    expect(seen[0]?.traceparent).toBeUndefined();
  });

  it("puts each broker request for a run in that run's trace", async () => {
    const controlPlane = {
      queryGraphjinRead: (input: { query: string }) =>
        graphjinQuery({ baseUrl: `${base}/api/v1/graphql`, query: input.query, configurationOnly: true }),
    } as unknown as AgentControlPlane;
    const broker = await startAgentBroker({ controlPlane, port: 0 });
    const post = (runId: string) =>
      fetch(`${broker.url}/v1/graphjin/query`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${broker.tokenFor({ runId, orgId: "o1", kind: "work" })}`,
          "content-type": "application/json",
        },
        body: JSON.stringify({ query: "{ ok }" }),
      });
    try {
      const a = await startRun("run-a");
      const b = await startRun("run-b");
      seen.length = 0;
      expect((await post("run-a")).status).toBe(200);
      expect((await post("run-b")).status).toBe(200);
      expect((await post("run-none")).status).toBe(200);
      expect(seen.map((h) => traceIdOf(h.traceparent))).toEqual([a.traceId, b.traceId, undefined]);
      expect(a.traceId).not.toBe(b.traceId);
      await a.end();
      await b.end();
    } finally {
      await broker.close();
    }
  });
});

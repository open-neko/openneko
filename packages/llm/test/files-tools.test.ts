import { createHash, createHmac } from "node:crypto";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mintServeToken, SERVE_TOKEN_LABEL } from "../src/files/client";
import { startAgentBroker } from "../src/work/broker";
import type { AgentControlPlane } from "../src/work/control-plane";
import { buildFilesServer } from "../src/work/tools";

function decode(part: string): Record<string, unknown> {
  return JSON.parse(Buffer.from(part, "base64url").toString("utf8"));
}

describe("openneko serve tokens", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("derives the key the Go service derives from the secret-key", async () => {
    const dir = mkdtempSync(join(tmpdir(), "serve-key-"));
    const keyFile = join(dir, "openneko", "secret-key");
    mkdirSync(join(dir, "openneko"));
    writeFileSync(keyFile, "fixture-secret\n");
    vi.stubEnv("XDG_CONFIG_HOME", dir);
    vi.resetModules();
    const { deriveSigningSecret } = await import("@neko/secret-crypt");
    expect(deriveSigningSecret(SERVE_TOKEN_LABEL).toString("hex")).toBe(
      "4fc869c2fcfaf11034f04f0ea823c141e82f489797c6da24a1571bc1fb052113",
    );
  });

  it("signs the org, the user and a short expiry", () => {
    const key = createHash("sha256").update("k").digest();
    const token = mintServeToken({ orgId: "org1", userId: "alice" }, { key, nowMs: 1_000_000 });
    const [head, body, sig] = token.split(".");
    expect(decode(head!)).toEqual({ alg: "HS256", typ: "JWT" });
    expect(decode(body!)).toEqual({ org: "org1", sub: "alice", exp: 1060 });
    expect(sig).toBe(createHmac("sha256", key).update(`${head}.${body}`).digest("base64url"));
  });
});

function controlPlane(overrides: Partial<AgentControlPlane>): AgentControlPlane {
  return overrides as AgentControlPlane;
}

async function connect(cp: AgentControlPlane) {
  const server = buildFilesServer({ orgId: "org1", threadId: "t1", runId: "run1" }, { controlPlane: cp });
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.instance.connect(serverTransport);
  const client = new Client({ name: "files-test", version: "1.0.0" });
  await client.connect(clientTransport);
  return client;
}

describe("neko_files tools", () => {
  it("offers search, read_text and query_tables", async () => {
    const client = await connect(controlPlane({}));
    const { tools } = await client.listTools();
    expect(tools.map((t) => t.name).sort()).toEqual(["query_tables", "read_text", "search"]);
  });

  it("passes the run binding and returns the service's answer", async () => {
    const searchFilesForRun = vi.fn().mockResolvedValue({ matches: [{ path: "a.md", score: 1, passages: [] }] });
    const client = await connect(controlPlane({ searchFilesForRun }));
    const result = await client.callTool({ name: "search", arguments: { query: "per diem" } });
    expect(result.isError).not.toBe(true);
    expect(searchFilesForRun).toHaveBeenCalledWith({ orgId: "org1", runId: "run1", query: "per diem" });
    expect(JSON.parse((result.content as Array<{ text: string }>)[0]!.text)).toEqual({
      matches: [{ path: "a.md", score: 1, passages: [] }],
    });
  });

  it("returns a service error as a tool error", async () => {
    const queryFileTablesForRun = vi.fn().mockRejectedValue(new Error("sql must be a single statement"));
    const client = await connect(controlPlane({ queryFileTablesForRun }));
    const result = await client.callTool({ name: "query_tables", arguments: { sql: "select 1; select 2" } });
    expect(result.isError).toBe(true);
    expect((result.content as Array<{ text: string }>)[0]!.text).toContain("single statement");
  });
});

describe("broker file routes", () => {
  it("take the org and run from the binding, never from the body", async () => {
    const searchFilesForRun = vi.fn().mockResolvedValue({ matches: [] });
    const readFileForRun = vi.fn().mockResolvedValue({ text: "x" });
    const queryFileTablesForRun = vi.fn().mockResolvedValue({ columns: [], rows: [] });
    const handle = await startAgentBroker({
      controlPlane: controlPlane({ searchFilesForRun, readFileForRun, queryFileTablesForRun }),
      port: 0,
    });
    try {
      const token = handle.tokenFor({ runId: "run1", orgId: "org1", kind: "work" });
      const post = (path: string, body: unknown) =>
        fetch(new URL(path, `http://127.0.0.1:${handle.port}`), {
          method: "POST",
          headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
          body: JSON.stringify(body),
        });
      expect((await post("/v1/files/search", { query: "q", limit: 3, orgId: "evil", runId: "evil", userId: "evil" })).status).toBe(200);
      expect(searchFilesForRun).toHaveBeenCalledWith({ orgId: "org1", runId: "run1", query: "q", limit: 3 });
      expect((await post("/v1/files/read", { path: "a.md", offset: 10, userId: "evil" })).status).toBe(200);
      expect(readFileForRun).toHaveBeenCalledWith({ orgId: "org1", runId: "run1", path: "a.md", offset: 10 });
      expect((await post("/v1/files/sql", { sql: "select 1", orgId: "evil" })).status).toBe(200);
      expect(queryFileTablesForRun).toHaveBeenCalledWith({ orgId: "org1", runId: "run1", sql: "select 1" });
    } finally {
      await handle.close();
    }
  });
});

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const provider = vi.hoisted(() => ({
  config: { source: "default", provider: "google-gemini", model: "m", enabled: true, config: {}, secrets: {} } as Record<string, unknown>,
  test: vi.fn(async () => ({})),
}));

vi.mock("../src/llm", () => ({ resolvePrimaryProviderConfig: vi.fn(async () => provider.config) }));
vi.mock("../src/inference", () => ({ testPrimaryProvider: provider.test }));

const { clearExternalServiceCache, externalServiceStatus, serviceWhere } = await import("../src/external-services");
const { setEmbeddingTransport } = await import("../src/embedding");
import type { ExternalService } from "../src/external-services";

const servers: Server[] = [];

async function listen(handler: (req: IncomingMessage, res: ServerResponse) => void): Promise<string> {
  const server = createServer(handler);
  servers.push(server);
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

function json(res: ServerResponse, status: number, body: unknown) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

const EMBED_KEY = "emb-planted-7731";
const SANDBOX_KEY = "sbx-planted-9902";
const MODEL_KEY = "sk-planted-4410";

beforeEach(() => {
  clearExternalServiceCache();
  provider.config = { source: "default", provider: "google-gemini", model: "m", enabled: true, config: {}, secrets: {} };
  provider.test.mockReset().mockResolvedValue({});
  for (const name of ["NEKO_EMBEDDING_URL", "NEKO_EMBEDDING_MODEL", "NEKO_EMBEDDING_API_KEY", "OPENNEKO_SANDBOX_URL", "OPENNEKO_SANDBOX_KEY", "OPENNEKO_SERVE_URL"]) {
    vi.stubEnv(name, "");
  }
});

afterEach(async () => {
  vi.unstubAllEnvs();
  setEmbeddingTransport(undefined);
  await Promise.all(servers.splice(0).map((s) => new Promise<void>((done) => s.close(() => done()))));
});

async function turnOn(options: { sandboxStatus?: number; health?: Record<string, unknown> } = {}) {
  provider.config = {
    source: "org",
    provider: "openai-compatible",
    model: "qwen3",
    enabled: true,
    config: { url: "https://user:pw@models.example.com/v1?tenant=a" },
    secrets: { apiKey: MODEL_KEY },
  };
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedder:5003/v1");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "google/embeddinggemma-2");
  vi.stubEnv("NEKO_EMBEDDING_API_KEY", EMBED_KEY);
  const sandbox = await listen((req, res) => {
    if (req.url !== "/v2/check") return json(res, 404, {});
    if (req.headers.authorization !== `Bearer ${SANDBOX_KEY}`) return json(res, 401, {});
    json(res, options.sandboxStatus ?? 200, { status: "ok" });
  });
  vi.stubEnv("OPENNEKO_SANDBOX_URL", sandbox);
  vi.stubEnv("OPENNEKO_SANDBOX_KEY", SANDBOX_KEY);
  const serve = await listen((req, res) =>
    req.url === "/healthz"
      ? json(res, 200, options.health ?? { ok: true, file_store: "s3", file_store_ok: true, file_store_where: "s3://files/team/" })
      : json(res, 404, {}),
  );
  vi.stubEnv("OPENNEKO_SERVE_URL", serve);
  return { sandbox };
}

describe("external services", () => {
  it("says what still works when every service is off", async () => {
    const statuses = await externalServiceStatus("org1");
    expect(statuses.map((s) => [s.id, s.state])).toEqual([
      ["inference", "off"],
      ["embedding", "off"],
      ["sandbox", "off"],
      ["file-store", "off"],
    ]);
    expect(statuses.find((s) => s.id === "embedding")!.detail).toBe("Search uses Postgres full-text search.");
    expect(statuses.find((s) => s.id === "sandbox")!.detail).toBe("The terminal and the script skills are off.");
    expect(statuses.every((s) => s.where === null)).toBe(true);
    expect(provider.test).not.toHaveBeenCalled();
  });

  it("checks each service that is on", async () => {
    const { sandbox } = await turnOn();
    const seen: Array<{ key: string; input: string[] }> = [];
    setEmbeddingTransport(async (request, key) => {
      seen.push({ key, input: request.input });
      return { vectors: [[0.1, 0.2, 0.3]], model: request.model };
    });
    const statuses = await externalServiceStatus("org1");
    expect(statuses.map((s) => [s.id, s.state, s.where])).toEqual([
      ["inference", "ok", "openai-compatible · qwen3 · https://models.example.com"],
      ["embedding", "ok", "http://embedder:5003 · google/embeddinggemma-2"],
      ["sandbox", "ok", sandbox],
      ["file-store", "ok", "s3://files/team/"],
    ]);
    expect(provider.test).toHaveBeenCalledWith("org1");
    expect(seen).toEqual([{ key: EMBED_KEY, input: ["OpenNeko status check"] }]);
    expect(statuses.find((s) => s.id === "file-store")!.detail).toBe("Files are in the bucket.");
  });

  it("reports a failing service and keeps every key out of the result", async () => {
    await turnOn({ health: { ok: true, file_store: "s3", file_store_ok: false, file_store_error: "s3://files/team/: Forbidden", file_store_where: "s3://files/team/" } });
    vi.stubEnv("OPENNEKO_SANDBOX_KEY", `${SANDBOX_KEY}-wrong`);
    provider.test.mockRejectedValue(new Error(`provider said 401 for key ${MODEL_KEY}`));
    setEmbeddingTransport(async (_request, key) => {
      throw new Error(`refused ${key}`);
    });
    const statuses = await externalServiceStatus("org1");
    expect(statuses.map((s) => [s.id, s.state])).toEqual([
      ["inference", "failing"],
      ["embedding", "failing"],
      ["sandbox", "failing"],
      ["file-store", "failing"],
    ]);
    const byId = Object.fromEntries(statuses.map((s) => [s.id, s]));
    expect(byId.inference!.detail).toBe("provider said 401 for key [redacted]");
    expect(byId.embedding!.detail).toBe("refused [redacted]");
    expect(byId.sandbox!.detail).toBe("the sandbox refused the key");
    expect(byId["file-store"]).toMatchObject({ detail: "s3://files/team/: Forbidden", where: "s3://files/team/" });
    const text = JSON.stringify(statuses);
    for (const secret of [MODEL_KEY, EMBED_KEY, SANDBOX_KEY, "user:pw", "tenant=a"]) expect(text).not.toContain(secret);
  });

  it("names an old sandbox and a sandbox that does not answer", async () => {
    await turnOn({ sandboxStatus: 404 });
    setEmbeddingTransport(async () => ({ vectors: [[1]], model: "m" }));
    let sandbox = (await externalServiceStatus("org1")).find((s) => s.id === "sandbox")!;
    expect(sandbox.detail).toMatch(/older than this release/);
    vi.stubEnv("OPENNEKO_SANDBOX_URL", "http://127.0.0.1:1");
    sandbox = (await externalServiceStatus("org1", { refresh: true })).find((s) => s.id === "sandbox")!;
    expect(sandbox).toMatchObject({ state: "failing", detail: "the sandbox did not answer" });
  });

  it("gives up on a check that does not answer in time", async () => {
    const slow: ExternalService = {
      id: "sandbox",
      label: "Slow",
      whenOff: "off",
      timeoutMs: 50,
      settings: async () => ({ url: "http://x", where: "x", secrets: [] }),
      check: () => new Promise(() => {}),
    };
    const [status] = await externalServiceStatus("org1", { services: [slow] });
    expect(status).toMatchObject({ state: "failing", detail: "No answer within 0.05 seconds." });
  });

  it("keeps results for a minute unless asked to check again", async () => {
    let calls = 0;
    const counted: ExternalService = {
      id: "embedding",
      label: "Counted",
      whenOff: "off",
      timeoutMs: 1000,
      settings: async () => ({ url: "http://x", where: "x", secrets: [] }),
      check: async () => ({ detail: `call ${++calls}` }),
    };
    expect((await externalServiceStatus("org1", { services: [counted] }))[0]!.detail).toBe("call 1");
    expect((await externalServiceStatus("org1", { services: [counted] }))[0]!.detail).toBe("call 1");
    expect((await externalServiceStatus("org2", { services: [counted] }))[0]!.detail).toBe("call 2");
    expect((await externalServiceStatus("org1", { services: [counted], refresh: true }))[0]!.detail).toBe("call 3");
  });

  it("shows only the scheme and host of a URL", () => {
    expect(serviceWhere("https://u:p@host.example:8443/v1/x?key=1#f")).toBe("https://host.example:8443");
    expect(serviceWhere("unix:///run/sandbox.sock")).toBe("unix:/run/sandbox.sock");
    expect(serviceWhere("not a url")).toBe("an address that is not a URL");
  });
});

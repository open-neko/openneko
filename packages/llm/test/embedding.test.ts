import { createServer, type Server } from "node:net";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { afterEach, expect, it, vi } from "vitest";
import { EmbeddingOffError, embedText, embedTexts, embeddingConfig, embeddingModel, setEmbeddingTransport } from "../src/embedding";

let server: Server | undefined;
let dir: string | undefined;

afterEach(async () => {
  vi.unstubAllEnvs();
  setEmbeddingTransport(undefined);
  await new Promise<void>((done) => (server ? server.close(() => done()) : done()));
  if (dir) await rm(dir, { recursive: true, force: true });
  server = undefined;
  dir = undefined;
});

async function fakeServe(reply: (frame: Record<string, unknown>) => unknown[]): Promise<Array<Record<string, unknown>>> {
  dir = await mkdtemp(join(tmpdir(), "embed-"));
  const socket = join(dir, "serve.sock");
  const seen: Array<Record<string, unknown>> = [];
  server = createServer((conn) => {
    createInterface({ input: conn }).once("line", (line) => {
      const frame = JSON.parse(line) as Record<string, unknown>;
      seen.push(frame);
      for (const out of reply(frame)) conn.write(`${JSON.stringify(out)}\n`);
      conn.end();
    });
  });
  await new Promise<void>((done) => server!.listen(socket, done));
  vi.stubEnv("OPENNEKO_SERVE_SOCKET", socket);
  return seen;
}

function on(key = "secret", dimensions = "256") {
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedder:5003/v1/");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "google/embeddinggemma-2");
  vi.stubEnv("NEKO_EMBEDDING_API_KEY", key);
  vi.stubEnv("NEKO_EMBEDDING_DIMENSIONS", dimensions);
}

it("sends one embed frame to serve with the key in its own field", async () => {
  on();
  const vector = Array.from({ length: 256 }, (_, i) => (i === 0 ? 1 : 0));
  const seen = await fakeServe(() => [{ type: "result", result: { vectors: [vector], model: "google/embeddinggemma-2" } }, { type: "exit", code: 0 }]);
  expect(embeddingModel()).toBe("google/embeddinggemma-2@256");
  expect(await embedText(" hello ")).toEqual(vector);
  expect(seen).toEqual([{
    type: "embed",
    model_key: "secret",
    embed: { url: "http://embedder:5003/v1", model: "google/embeddinggemma-2", dimensions: 256, input: ["hello"] },
  }]);
});

it("sends no key or dimensions when none are set", async () => {
  on("", "");
  const seen = await fakeServe(() => [{ type: "result", result: { vectors: [[0.5, 0.5]], model: "m" } }, { type: "exit", code: 0 }]);
  expect(await embedText("hello")).toEqual([0.5, 0.5]);
  expect(seen[0]).toEqual({ type: "embed", embed: { url: "http://embedder:5003/v1", model: "google/embeddinggemma-2", input: ["hello"] } });
  expect(embeddingModel()).toBe("google/embeddinggemma-2");
});

it("embeds several texts in one call, in order", async () => {
  on("k", "");
  const transport = vi.fn(async (request: { input: string[] }) => ({ vectors: request.input.map((_, i) => [i, 1]), model: "m" }));
  setEmbeddingTransport(transport);
  expect(await embedTexts(["a", " b "])).toEqual([[0, 1], [1, 1]]);
  expect(transport).toHaveBeenCalledWith(expect.objectContaining({ input: ["a", "b"] }), "k", 15_000);
  await expect(embedTexts(Array(65).fill("a"))).rejects.toThrow(/1 to 64/);
  await expect(embedTexts([])).rejects.toThrow(/1 to 64/);
});

it("refuses a reply that does not fit, and passes serve's error on", async () => {
  on();
  const replies: unknown[] = [{ vectors: [] }, { vectors: [[1]] }, { vectors: [Array(256).fill("1")] }, { vectors: [Array(256).fill(Number.NaN)] }];
  setEmbeddingTransport(async () => replies.shift() as never);
  for (let i = 0; i < 4; i++) await expect(embedText("hello")).rejects.toThrow(/incompatible vector/);
  setEmbeddingTransport(undefined);
  await fakeServe(() => [{ type: "exit", code: 1, error: "embedding service returned HTTP 503" }]);
  await expect(embedText("hello")).rejects.toThrow(/HTTP 503/);
});

it("is off until both the URL and the model are set, and never calls out while off", async () => {
  const transport = vi.fn();
  setEmbeddingTransport(transport);
  expect(embeddingConfig({})).toBeNull();
  expect(embeddingConfig({ NEKO_EMBEDDING_URL: "http://embedder/v1" })).toBeNull();
  expect(embeddingConfig({ NEKO_EMBEDDING_MODEL: "m" })).toBeNull();
  vi.stubEnv("NEKO_EMBEDDING_URL", "");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "");
  expect(embeddingModel()).toBeNull();
  await expect(embedText("hello")).rejects.toBeInstanceOf(EmbeddingOffError);
  expect(transport).not.toHaveBeenCalled();
});

it("rejects invalid input before it calls out", async () => {
  on();
  const transport = vi.fn();
  setEmbeddingTransport(transport);
  await expect(embedText(" ")).rejects.toThrow(/empty/);
  await expect(embedText("x".repeat(32001))).rejects.toThrow(/32000/);
  expect(transport).not.toHaveBeenCalled();
});

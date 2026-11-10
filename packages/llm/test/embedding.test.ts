import { afterEach, expect, it, vi } from "vitest";
import { EmbeddingOffError, embedText, embeddingConfig, embeddingModel } from "../src/embedding";

afterEach(() => { vi.unstubAllEnvs(); vi.unstubAllGlobals(); });

it("calls the OpenAI-compatible embeddings route with the key, model and dimensions", async () => {
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedder:5003/v1/");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "google/embeddinggemma-2");
  vi.stubEnv("NEKO_EMBEDDING_API_KEY", "secret");
  vi.stubEnv("NEKO_EMBEDDING_DIMENSIONS", "256");
  const vector = Array.from({ length: 256 }, (_, i) => (i === 0 ? 1 : 0));
  const fetcher = vi.fn(async () => Response.json({ data: [{ embedding: vector }] }));
  vi.stubGlobal("fetch", fetcher);
  expect(embeddingModel()).toBe("google/embeddinggemma-2@256");
  expect(await embedText(" hello ")).toEqual(vector);
  expect(fetcher).toHaveBeenCalledWith("http://embedder:5003/v1/embeddings", expect.objectContaining({
    headers: { "content-type": "application/json", authorization: "Bearer secret" },
    body: JSON.stringify({ model: "google/embeddinggemma-2", input: "hello", encoding_format: "float", dimensions: 256 }),
  }));
  for (const bad of [{ data: [] }, { data: [{ embedding: [1] }] }, { data: [{ embedding: Array(256).fill("1") }] }]) {
    fetcher.mockResolvedValueOnce(Response.json(bad));
    await expect(embedText("hello")).rejects.toThrow(/incompatible vector/);
  }
  fetcher.mockResolvedValueOnce(new Response("busy", { status: 503 }));
  await expect(embedText("hello")).rejects.toThrow(/HTTP 503/);
});

it("sends no key or dimensions when none are set", async () => {
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedder/v1");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "m");
  vi.stubEnv("NEKO_EMBEDDING_API_KEY", "");
  vi.stubEnv("NEKO_EMBEDDING_DIMENSIONS", "");
  const fetcher = vi.fn(async () => Response.json({ data: [{ embedding: [0.5, 0.5] }] }));
  vi.stubGlobal("fetch", fetcher);
  expect(await embedText("hello")).toEqual([0.5, 0.5]);
  expect(fetcher).toHaveBeenCalledWith("http://embedder/v1/embeddings", expect.objectContaining({
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ model: "m", input: "hello", encoding_format: "float" }),
  }));
  expect(embeddingModel()).toBe("m");
});

it("is off until both the URL and the model are set, and never calls out while off", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  expect(embeddingConfig({})).toBeNull();
  expect(embeddingConfig({ NEKO_EMBEDDING_URL: "http://embedder/v1" })).toBeNull();
  expect(embeddingConfig({ NEKO_EMBEDDING_MODEL: "m" })).toBeNull();
  vi.stubEnv("NEKO_EMBEDDING_URL", "");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "");
  expect(embeddingModel()).toBeNull();
  await expect(embedText("hello")).rejects.toBeInstanceOf(EmbeddingOffError);
  expect(fetcher).not.toHaveBeenCalled();
});

it("rejects invalid input", async () => {
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedder/v1");
  vi.stubEnv("NEKO_EMBEDDING_MODEL", "m");
  await expect(embedText(" ")).rejects.toThrow(/empty/);
  await expect(embedText("x".repeat(32001))).rejects.toThrow(/32000/);
});

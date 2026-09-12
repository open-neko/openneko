import { afterEach, expect, it, vi } from "vitest";
import { embedText } from "../src/embedding";

afterEach(() => { vi.unstubAllEnvs(); vi.unstubAllGlobals(); });

it("uses only the embedding service and checks model/vector compatibility", async () => {
  vi.stubEnv("NEKO_EMBEDDING_URL", "http://embedding:5003/");
  const vector = Array.from({length:384}, (_, i) => i === 0 ? 1 : 0);
  const payload = { model: "Xenova/all-MiniLM-L6-v2", dimensions:384, vector };
  const fetcher = vi.fn(async () => Response.json(payload));
  vi.stubGlobal("fetch",fetcher);
  expect(await embedText(" hello ")).toEqual(vector);
  expect(fetcher).toHaveBeenCalledWith("http://embedding:5003/v1/embeddings",expect.objectContaining({body:JSON.stringify({text:"hello"})}));
  for (const bad of [{...payload, model:"another-model"}, {...payload, vector:[1]}, {...payload, vector:Array(384).fill("1")}]) {
    fetcher.mockResolvedValueOnce(Response.json(bad));
    await expect(embedText("hello")).rejects.toThrow(/incompatible vector/);
  }
  fetcher.mockResolvedValueOnce(new Response("busy",{status:503}));
  await expect(embedText("hello")).rejects.toThrow(/HTTP 503/);
});

it("rejects invalid input and missing configuration without loading a local model", async () => {
  vi.stubEnv("NEKO_EMBEDDING_URL", "");
  await expect(embedText("hello")).rejects.toThrow(/not configured/);
  await expect(embedText(" ")).rejects.toThrow(/empty/);
  await expect(embedText("x".repeat(32001))).rejects.toThrow(/32000/);
});

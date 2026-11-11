import { createServer, type Server } from "node:net";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("google-auth-library", () => ({
  GoogleAuth: class {
    async getClient() {
      return { getAccessToken: async () => ({ token: "adc-token" }) };
    }
  },
}));

const { generate, hostProgram, setGatewayTransport, testPrimaryProvider } = await import("../src/inference");
import type { GatewayRequest, GatewayResult } from "../src/inference";
import type { EditableProviderConfig } from "../src/config";

const PROGRAM = hostProgram(`question:string "a question" -> answer:string "the answer"`, "Answer it.");

function draft(provider: string, model: string, config: Record<string, unknown> = {}, apiKey?: string): EditableProviderConfig {
  return { scope: "primary", provider, model, enabled: true, config, secrets: apiKey ? { apiKey } : {} } as EditableProviderConfig;
}

function capture(outputs: Record<string, unknown> = { answer: "42" }) {
  const calls: Array<{ request: GatewayRequest; key: string }> = [];
  setGatewayTransport(async (request, key) => {
    calls.push({ request, key });
    return {
      outputs,
      usage: { requests: 1, reported: 1, input_tokens: 7, output_tokens: 2, total_tokens: 9, coverage: "complete" },
      provider: request.route.provider,
      model: request.route.model,
    } satisfies GatewayResult;
  });
  return calls;
}

afterEach(() => {
  setGatewayTransport(undefined);
  delete process.env.OPENNEKO_SERVE_SOCKET;
});

describe("host model calls through the inference gateway", () => {
  it.each([
    ["anthropic", draft("anthropic", "claude-sonnet-5-5", {}, "sk-ant"), { provider: "anthropic", model: "claude-sonnet-5-5" }, "sk-ant"],
    ["groq", draft("groq", "llama", {}, "gk"), { provider: "groq", model: "llama", url: "https://api.groq.com/openai/v1" }, "gk"],
    ["ollama", draft("ollama", "llama3", { url: "http://ollama.lan:11434" }), { provider: "openai-compatible", model: "llama3", url: "http://ollama.lan:11434/v1" }, "none"],
    [
      "azure",
      draft("azure-openai", "gpt", { resourceName: "acme", deploymentName: "prod-gpt" }, "az"),
      { provider: "azure-openai", model: "prod-gpt", options: { resource_name: "acme", deployment_name: "prod-gpt", api_version: "2024-10-21" } },
      "az",
    ],
    [
      "openai-compatible",
      draft("openai-compatible", "qwen3", { url: "https://models.example.com/v1" }),
      { provider: "openai-compatible", model: "qwen3", url: "https://models.example.com/v1" },
      "none",
    ],
    ["vertex", draft("vertex", "gemini-3", { projectId: "p1", region: "global" }), { provider: "vertex-ai", model: "gemini-3" }, "adc-token"],
  ])("sends the %s route and key, and the program", async (_name, config, route, key) => {
    const calls = capture();
    const result = await generate(undefined, PROGRAM, { question: "q" }, { draft: config });
    expect(calls).toHaveLength(1);
    expect(calls[0].request.route).toMatchObject(route);
    expect(calls[0].key).toBe(key);
    expect(calls[0].request).toMatchObject({ signature: PROGRAM.signature, instruction: "Answer it.", values: { question: "q" } });
    expect(result.outputs).toEqual({ answer: "42" });
    expect(result.usage).toEqual({ inputTokens: 7, outputTokens: 2, totalTokens: 9, coverage: "complete" });
  });

  it("tests a keyless provider without asking for a key", async () => {
    const calls = capture({ reply: "READY" });
    await expect(testPrimaryProvider(undefined, draft("ollama", "llama3", { url: "http://localhost:11434" }))).resolves.toMatchObject({
      provider: "ollama",
      model: "llama3",
      source: "draft",
    });
    expect(calls[0].key).toBe("none");
  });

  it("refuses an OpenAI-compatible provider without a model name", async () => {
    capture();
    await expect(generate(undefined, PROGRAM, { question: "q" }, { draft: draft("openai-compatible", "", { url: "https://m.example.com/v1" }) })).rejects.toThrow(/model name/);
  });

  it("fails the provider test on an empty reply", async () => {
    capture({ reply: " " });
    await expect(testPrimaryProvider(undefined, draft("openai", "gpt", {}, "k"))).rejects.toThrow(/empty response/);
  });
});

describe("the serve socket transport", () => {
  let server: Server | undefined;
  let dir: string | undefined;

  afterEach(async () => {
    await new Promise<void>((resolve) => (server ? server.close(() => resolve()) : resolve()));
    if (dir) await rm(dir, { recursive: true, force: true });
    server = undefined;
    dir = undefined;
  });

  async function fakeServe(reply: (frame: Record<string, unknown>) => unknown[]): Promise<Array<Record<string, unknown>>> {
    dir = await mkdtemp(join(tmpdir(), "gateway-"));
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
    await new Promise<void>((resolve) => server!.listen(socket, resolve));
    process.env.OPENNEKO_SERVE_SOCKET = socket;
    return seen;
  }

  it("sends one generate frame with the key in its own field", async () => {
    const seen = await fakeServe(() => [
      { type: "result", result: { outputs: { answer: "42" }, usage: { requests: 1, reported: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, coverage: "unavailable" } } },
      { type: "exit", code: 0 },
    ]);
    const result = await generate(undefined, PROGRAM, { question: "q" }, { draft: draft("openai", "gpt", {}, "sk-openai") });
    expect(result.outputs).toEqual({ answer: "42" });
    expect(result.usage.coverage).toBe("unavailable");
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatchObject({ type: "generate", model_key: "sk-openai", generate: { route: { provider: "openai", model: "gpt" } } });
    expect(JSON.stringify(seen[0].generate)).not.toContain("sk-openai");
  });

  it("reports serve's error", async () => {
    await fakeServe(() => [{ type: "exit", code: 1, error: "provider said no" }]);
    await expect(generate(undefined, PROGRAM, { question: "q" }, { draft: draft("openai", "gpt", {}, "k") })).rejects.toThrow("provider said no");
  });
});

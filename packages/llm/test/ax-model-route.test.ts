import { describe, expect, it } from "vitest";
import { PRIMARY_PROVIDER_OPTIONS } from "../src/config";
import { resolveAxModelRoute, resolveProviderRuntime } from "../src/provider-runtime";

describe("resolveAxModelRoute", () => {
  it("uses native clients for OpenAI, Anthropic and Gemini", () => {
    expect(resolveAxModelRoute({ provider: "anthropic", model: "claude-sonnet-5-5" })).toEqual({
      provider: "anthropic",
      model: "claude-sonnet-5-5",
      keyEnv: "ANTHROPIC_API_KEY",
    });
    expect(resolveAxModelRoute({ provider: "google-gemini", model: "gemini-3.6-flash" }).provider).toBe("google-gemini");
    expect(resolveAxModelRoute({ provider: "openai", model: "gpt-5.5" }).provider).toBe("openai");
  });

  it("builds Azure from its resource and deployment", () => {
    expect(
      resolveAxModelRoute({ provider: "azure-openai", model: "ignored", config: { resourceName: "acme", deploymentName: "sales-gpt" } }),
    ).toEqual({
      provider: "azure-openai",
      model: "sales-gpt",
      keyEnv: "AZURE_FOUNDRY_API_KEY",
      options: { resource_name: "acme", deployment_name: "sales-gpt", api_version: "2024-10-21" },
    });
  });

  it("uses each provider's Ax profile on the runtime host", () => {
    expect(resolveAxModelRoute({ provider: "groq", model: "llama-5" })).toEqual({
      provider: "groq",
      model: "llama-5",
      url: "https://api.groq.com/openai/v1",
      keyEnv: "GROQ_API_KEY",
    });
    expect(resolveAxModelRoute({ provider: "x-grok", model: "grok-5" }).provider).toBe("grok");
    expect(resolveAxModelRoute({ provider: "huggingface", model: "m" }).provider).toBe("huggingface-router");
    expect(resolveAxModelRoute({ provider: "ollama", model: "qwen", config: { url: "http://localhost:11434" } })).toEqual({
      provider: "openai-compatible",
      model: "qwen",
      url: "http://host.docker.internal:11434/v1",
    });
  });

  it("reaches the runtime model host for every admin provider", () => {
    const config = { resourceName: "acme", deploymentName: "d", projectId: "p", region: "us-central1", url: "http://localhost:11434" };
    for (const { value } of PRIMARY_PROVIDER_OPTIONS) {
      let runtime: ReturnType<typeof resolveProviderRuntime>;
      try {
        runtime = resolveProviderRuntime({ provider: value, model: "m", config });
      } catch {
        continue;
      }
      const ax = resolveAxModelRoute({ provider: value, model: "m", config });
      const host = ax.url
        ? new URL(ax.url).hostname
        : ax.provider === "azure-openai"
          ? `${ax.options?.resource_name}.openai.azure.com`
          : new URL(runtime.baseUrl).hostname;
      expect(host, value).toBe(runtime.endpoint.host);
    }
  });
});

describe("fallbackRouteProfile", () => {
  it("builds the profile that apps/ax-harness TestHostFallbackProfileLoads parses", async () => {
    const { fallbackRouteProfile } = await import("../src/agent-backends/ax");
    expect(
      fallbackRouteProfile(
        { provider: "openai-compatible", model: "qwen3", url: "https://models.example.test/v1" },
        { provider: "openai", model: "gpt-5", keyEnv: "OPENAI_API_KEY", price: { inputUsdPer1M: 1, outputUsdPer1M: 2 } },
      ),
    ).toBe(
      `{"context":"primary","executor":"primary","responder":"primary","fallbacks":[{"from":"primary","to":"fallback"}],"routes":[{"key":"primary","provider":"openai-compatible","model":"qwen3","url":"https://models.example.test/v1","api_key_env":"PRIMARY_MODEL_API_KEY"},{"key":"fallback","provider":"openai","model":"gpt-5","api_key_env":"FALLBACK_MODEL_API_KEY"}]}`,
    );
  });
});

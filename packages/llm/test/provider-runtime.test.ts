import { describe, expect, it } from "vitest";
import { PRIMARY_PROVIDER_OPTIONS, type PrimaryProviderId } from "../src/config";
import { resolveAxModelRoute, resolveProviderRuntime } from "../src/provider-runtime";

const configByProvider: Partial<Record<PrimaryProviderId, Record<string, unknown>>> = {
  "azure-openai": {
    resourceName: "customer-openai",
    deploymentName: "production-gpt",
  },
  ollama: { url: "http://localhost:11434" },
  "openai-compatible": { url: "https://models.example.com:8443/serving/v1" },
  vertex: { projectId: "customer-ai", region: "us-central1" },
};

const expected = {
  openai: ["OPENAI_API_KEY", "api.openai.com", undefined],
  anthropic: ["ANTHROPIC_API_KEY", "api.anthropic.com", undefined],
  "google-gemini": ["GEMINI_API_KEY", "generativelanguage.googleapis.com", undefined],
  "azure-openai": ["AZURE_FOUNDRY_API_KEY", "customer-openai.openai.azure.com", undefined],
  mistral: ["MISTRAL_API_KEY", "api.mistral.ai", undefined],
  groq: ["GROQ_API_KEY", "api.groq.com", undefined],
  cohere: ["COHERE_API_KEY", "api.cohere.ai", undefined],
  together: ["TOGETHER_API_KEY", "api.together.ai", undefined],
  deepseek: ["DEEPSEEK_API_KEY", "api.deepseek.com", undefined],
  ollama: [undefined, "host.docker.internal", 11434],
  "openai-compatible": ["OPENAI_COMPATIBLE_API_KEY", "models.example.com", 8443],
  huggingface: ["HF_TOKEN", "router.huggingface.co", undefined],
  openrouter: ["OPENROUTER_API_KEY", "openrouter.ai", undefined],
  reka: ["REKA_API_KEY", "api.reka.ai", undefined],
  "x-grok": ["XAI_API_KEY", "api.x.ai", undefined],
  vertex: ["VERTEX_ACCESS_TOKEN", "us-central1-aiplatform.googleapis.com", undefined],
} satisfies Record<PrimaryProviderId, [string | undefined, string, number | undefined]>;

describe("resolveProviderRuntime", () => {
  it("defines the complete credential and egress contract for every Admin provider", () => {
    expect(Object.keys(expected).sort()).toEqual(
      PRIMARY_PROVIDER_OPTIONS.map(({ value }) => value).sort(),
    );

    for (const { value: provider } of PRIMARY_PROVIDER_OPTIONS) {
      const runtime = resolveProviderRuntime({
        provider,
        model: "test-model",
        config: configByProvider[provider],
      });
      const [keyEnv, host, port] = expected[provider];
      expect(runtime.keyEnv, provider).toBe(keyEnv);
      expect(runtime.endpoint, provider).toEqual({
        host,
        ...(port ? { port } : {}),
      });
      expect(runtime.baseUrl, provider).toMatch(/^https?:\/\//);
    }
  });

  it("uses the Azure deployment name as the wire model", () => {
    const runtime = resolveProviderRuntime({
      provider: "azure-openai",
      model: "friendly-model-label",
      config: configByProvider["azure-openai"],
    });
    expect(runtime.model).toBe("production-gpt");
    expect(runtime.baseUrl).toBe(
      "https://customer-openai.openai.azure.com/openai/v1",
    );
  });

  it("normalizes host-local Ollama and preserves its explicit port", () => {
    const runtime = resolveProviderRuntime({
      provider: "ollama",
      model: "llama3.2",
      config: { url: "http://127.0.0.1:11434" },
    });
    expect(runtime.baseUrl).toBe("http://host.docker.internal:11434/v1");
    expect(runtime.endpoint).toEqual({ host: "host.docker.internal", port: 11434 });
    expect(runtime.credentialSource).toBe("none");
  });

  it("keeps an OpenAI-compatible URL's path and refuses one that carries credentials", () => {
    const runtime = resolveProviderRuntime({
      provider: "openai-compatible",
      model: "qwen3",
      config: { url: "http://localhost:8000/v1/" },
    });
    expect(runtime.baseUrl).toBe("http://host.docker.internal:8000/v1");
    expect(runtime.credentialSource).toBe("stored-api-key");
    expect(resolveAxModelRoute({ provider: "openai-compatible", model: "qwen3", config: { url: "https://m.example.com/v1" } })).toEqual({
      provider: "openai-compatible",
      model: "qwen3",
      url: "https://m.example.com/v1",
      keyEnv: "OPENAI_COMPATIBLE_API_KEY",
    });
    for (const url of ["https://user:pw@m.example.com/v1", "https://m.example.com/v1?key=x", "ftp://m.example.com"]) {
      expect(() => resolveProviderRuntime({ provider: "openai-compatible", model: "m", config: { url } }), url).toThrow();
    }
  });

  it("routes global Vertex through the global endpoint", () => {
    const runtime = resolveProviderRuntime({
      provider: "vertex",
      model: "publisher/model",
      config: { projectId: "customer-ai", region: "global" },
    });
    expect(runtime.baseUrl).toBe(
      "https://aiplatform.googleapis.com/v1beta1/projects/customer-ai/locations/global/endpoints/openapi",
    );
    expect(runtime.endpoint).toEqual({ host: "aiplatform.googleapis.com" });
    expect(runtime.credentialSource).toBe("google-adc");
  });

  it("rejects endpoint-bearing providers whose endpoint fields are unsafe", () => {
    expect(() =>
      resolveProviderRuntime({
        provider: "azure-openai",
        model: "gpt",
        config: { resourceName: "customer.example.com/path", deploymentName: "gpt" },
      }),
    ).toThrow("invalid characters");
    expect(() =>
      resolveProviderRuntime({
        provider: "ollama",
        model: "llama",
        config: { url: "file:///tmp/ollama.sock" },
      }),
    ).toThrow("must use http or https");
  });
});

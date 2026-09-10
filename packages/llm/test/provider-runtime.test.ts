import { describe, expect, it } from "vitest";
import { PRIMARY_PROVIDER_OPTIONS, type PrimaryProviderId } from "../src/config";
import { resolveHermesProviderRuntime } from "../src/provider-runtime";
import {
  hermesModelBudgetConfigLines,
  hermesModelConfigLines,
} from "../src/host-provision";

const configByProvider: Partial<Record<PrimaryProviderId, Record<string, unknown>>> = {
  "azure-openai": {
    resourceName: "customer-openai",
    deploymentName: "production-gpt",
  },
  ollama: { url: "http://localhost:11434" },
  vertex: { projectId: "customer-ai", region: "us-central1" },
};

const expected = {
  openai: ["openai-api", "OPENAI_API_KEY", "api.openai.com", undefined],
  anthropic: ["anthropic", "ANTHROPIC_API_KEY", "api.anthropic.com", undefined],
  "google-gemini": ["gemini", "GEMINI_API_KEY", "generativelanguage.googleapis.com", undefined],
  "azure-openai": ["azure-foundry", "AZURE_FOUNDRY_API_KEY", "customer-openai.openai.azure.com", undefined],
  mistral: ["custom", "MISTRAL_API_KEY", "api.mistral.ai", undefined],
  groq: ["custom", "GROQ_API_KEY", "api.groq.com", undefined],
  cohere: ["custom", "COHERE_API_KEY", "api.cohere.ai", undefined],
  together: ["custom", "TOGETHER_API_KEY", "api.together.ai", undefined],
  deepseek: ["deepseek", "DEEPSEEK_API_KEY", "api.deepseek.com", undefined],
  ollama: ["custom", undefined, "host.docker.internal", 11434],
  huggingface: ["huggingface", "HF_TOKEN", "router.huggingface.co", undefined],
  openrouter: ["openrouter", "OPENROUTER_API_KEY", "openrouter.ai", undefined],
  reka: ["custom", "REKA_API_KEY", "api.reka.ai", undefined],
  "x-grok": ["xai", "XAI_API_KEY", "api.x.ai", undefined],
  vertex: ["custom", "VERTEX_ACCESS_TOKEN", "us-central1-aiplatform.googleapis.com", undefined],
} satisfies Record<PrimaryProviderId, [string, string | undefined, string, number | undefined]>;

describe("resolveHermesProviderRuntime", () => {
  it("defines the complete Hermes, credential and egress contract for every Admin provider", () => {
    expect(Object.keys(expected).sort()).toEqual(
      PRIMARY_PROVIDER_OPTIONS.map(({ value }) => value).sort(),
    );

    for (const { value: provider } of PRIMARY_PROVIDER_OPTIONS) {
      const runtime = resolveHermesProviderRuntime({
        provider,
        model: "test-model",
        config: configByProvider[provider],
      });
      const [hermesProvider, keyEnv, host, port] = expected[provider];
      expect(runtime.provider, provider).toBe(hermesProvider);
      expect(runtime.keyEnv, provider).toBe(keyEnv);
      expect(runtime.endpoint, provider).toEqual({
        host,
        ...(port ? { port } : {}),
      });
      expect(runtime.baseUrl, provider).toMatch(/^https?:\/\//);
    }
  });

  it("renders a complete Hermes model block for every Admin provider", () => {
    for (const { value: provider } of PRIMARY_PROVIDER_OPTIONS) {
      const runtime = resolveHermesProviderRuntime({
        provider,
        model: "test-model",
        config: configByProvider[provider],
      });
      const yaml = hermesModelConfigLines(runtime).join("\n");
      expect(yaml, provider).toContain(`provider: "${runtime.provider}"`);
      expect(yaml, provider).toContain(`base_url: "${runtime.baseUrl}"`);
      if (runtime.provider === "custom" && runtime.keyEnv) {
        expect(yaml, provider).toContain(`api_key: "\${${runtime.keyEnv}}"`);
      } else {
        expect(yaml, provider).not.toContain("api_key:");
      }
      expect(yaml, provider).not.toContain("REAL_SECRET");
    }
  });

  it("maps explicit eval runtime budgets to the pinned Hermes config keys", () => {
    expect(
      hermesModelBudgetConfigLines({
        context_window_tokens: 1_048_576,
        max_output_tokens: 65_536,
      }),
    ).toEqual([
      "  context_length: 1048576",
      "  max_tokens: 65536",
    ]);
    expect(() =>
      hermesModelBudgetConfigLines({
        context_window_tokens: 8_192,
        max_output_tokens: 8_192,
      }),
    ).toThrow("max_output_tokens must be smaller than context_window_tokens");
    expect(() =>
      hermesModelBudgetConfigLines({ max_output_tokens: "65536" }),
    ).toThrow("max_output_tokens must be a positive integer");
  });

  it("uses the Azure deployment name as the wire model", () => {
    const runtime = resolveHermesProviderRuntime({
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
    const runtime = resolveHermesProviderRuntime({
      provider: "ollama",
      model: "llama3.2",
      config: { url: "http://127.0.0.1:11434" },
    });
    expect(runtime.baseUrl).toBe("http://host.docker.internal:11434/v1");
    expect(runtime.endpoint).toEqual({ host: "host.docker.internal", port: 11434 });
    expect(runtime.credentialSource).toBe("none");
  });

  it("routes global Vertex through the global endpoint", () => {
    const runtime = resolveHermesProviderRuntime({
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
      resolveHermesProviderRuntime({
        provider: "azure-openai",
        model: "gpt",
        config: { resourceName: "customer.example.com/path", deploymentName: "gpt" },
      }),
    ).toThrow("invalid characters");
    expect(() =>
      resolveHermesProviderRuntime({
        provider: "ollama",
        model: "llama",
        config: { url: "file:///tmp/ollama.sock" },
      }),
    ).toThrow("must use http or https");
  });
});

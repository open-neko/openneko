import { describe, expect, it } from "vitest";
import { resolveAxModelRoute } from "../src/provider-runtime";

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

  it("sends other providers to their OpenAI-compatible endpoint", () => {
    expect(resolveAxModelRoute({ provider: "groq", model: "llama-5" })).toEqual({
      provider: "openai-compatible",
      model: "llama-5",
      url: "https://api.groq.com/openai/v1",
      keyEnv: "GROQ_API_KEY",
    });
    expect(resolveAxModelRoute({ provider: "ollama", model: "qwen", config: { url: "http://localhost:11434" } })).toEqual({
      provider: "openai-compatible",
      model: "qwen",
      url: "http://host.docker.internal:11434/v1",
    });
  });

  it("refuses Azure OpenAI", () => {
    expect(() => resolveAxModelRoute({ provider: "azure-openai", model: "x", config: { resourceName: "r", deploymentName: "d" } })).toThrow(/Azure/);
  });
});

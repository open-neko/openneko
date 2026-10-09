import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import { graphjinAgentModelFromRow, patchGraphjinAgentBlock } from "../src/graphjin/agent-settings";

const config = `mcp:
  include_tools_with_agent: true
agent:
  enabled: true
  provider: openai
  model: gpt-4.1-mini
  api_key_env: GRAPHJIN_AGENT_API_KEY
  max_steps: 8
  read_only: true
`;

describe("GraphJin agent settings", () => {
  it("maps admin providers onto GraphJin's Ax profiles", () => {
    expect(graphjinAgentModelFromRow({ provider: "anthropic", model: "claude-sonnet-5-5", config: {}, secrets: { apiKey: "k" } })).toEqual({
      provider: "anthropic",
      model: "claude-sonnet-5-5",
      apiKey: "k",
    });
    expect(graphjinAgentModelFromRow({ provider: "azure-openai", model: "x", config: { resourceName: "acme", deploymentName: "sales" }, secrets: { apiKey: "k" } })).toEqual({
      provider: "azure-openai",
      model: "sales",
      baseUrl: "https://acme.openai.azure.com/openai/deployments/sales",
      apiKey: "k",
    });
    expect(graphjinAgentModelFromRow({ provider: "groq", model: "llama", config: {}, secrets: { apiKey: "k" } }).baseUrl).toBe(
      "https://api.groq.com/openai/v1",
    );
  });

  it("writes the agent block and asks for a restart only when GraphJin reads the change at start", () => {
    const provider = patchGraphjinAgentBlock(config, { provider: "anthropic", model: "claude-sonnet-5-5" });
    expect(provider.restart).toBe(true);
    const agent = parse(provider.content).agent;
    expect(agent).toMatchObject({
      enabled: true,
      provider: "anthropic",
      model: "claude-sonnet-5-5",
      api_key_env: "OPENNEKO_GRAPHJIN_AGENT_API_KEY",
      read_only: true,
      max_steps: 8,
    });
    expect(agent.base_url).toBeUndefined();
    expect(parse(provider.content).mcp).toEqual({ include_tools_with_agent: true });

    const model = patchGraphjinAgentBlock(provider.content, { provider: "anthropic", model: "claude-haiku-5-5" });
    expect(model).toMatchObject({ changed: true, restart: false });
    expect(patchGraphjinAgentBlock(model.content, { provider: "anthropic", model: "claude-haiku-5-5" }).changed).toBe(false);
  });
});

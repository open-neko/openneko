import { describe, expect, it } from "vitest";
import {
  AGENT_BACKEND_IDS,
  AGENT_BACKEND_OPTIONS,
  AGENT_DEFAULT_CLAUDE_AGENT_CAP,
  AGENT_DEFAULT_GLOBAL_CAP,
  AgentBackendConfigError,
  isAgentBackendId,
} from "../src/agent-backend";

describe("isAgentBackendId", () => {
  it("accepts hermes", () => {
    expect(isAgentBackendId("hermes")).toBe(true);
  });
  it("accepts claude-agent", () => {
    expect(isAgentBackendId("claude-agent")).toBe(true);
  });
  it("rejects unknown values", () => {
    expect(isAgentBackendId("openai")).toBe(false);
    expect(isAgentBackendId("")).toBe(false);
    expect(isAgentBackendId("HERMES")).toBe(false); // case-sensitive
  });
});

describe("AGENT_BACKEND_OPTIONS / AGENT_BACKEND_IDS integrity", () => {
  it("options and ids have the same length", () => {
    expect(AGENT_BACKEND_OPTIONS.length).toBe(AGENT_BACKEND_IDS.length);
  });
  it("every option value is an id and vice versa", () => {
    const optionValues = AGENT_BACKEND_OPTIONS.map((o) => o.value);
    expect(new Set(optionValues)).toEqual(new Set(AGENT_BACKEND_IDS));
  });
  it("every option has a non-empty label and description", () => {
    for (const o of AGENT_BACKEND_OPTIONS) {
      expect(o.label.length).toBeGreaterThan(0);
      expect(o.description.length).toBeGreaterThan(0);
    }
  });
});

describe("default concurrency caps", () => {
  it("globalCap default is positive", () => {
    expect(AGENT_DEFAULT_GLOBAL_CAP).toBeGreaterThan(0);
  });
  it("claudeAgentCap default is positive", () => {
    expect(AGENT_DEFAULT_CLAUDE_AGENT_CAP).toBeGreaterThan(0);
  });
  it("claudeAgentCap is <= globalCap (sane defaults; SDK is in-process)", () => {
    expect(AGENT_DEFAULT_CLAUDE_AGENT_CAP).toBeLessThanOrEqual(AGENT_DEFAULT_GLOBAL_CAP);
  });
});

describe("AgentBackendConfigError", () => {
  it("preserves the message and is named", () => {
    const e = new AgentBackendConfigError("missing key");
    expect(e.message).toBe("missing key");
    expect(e.name).toBe("AgentBackendConfigError");
    expect(e).toBeInstanceOf(Error);
  });
});

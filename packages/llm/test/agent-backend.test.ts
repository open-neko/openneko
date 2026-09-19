import { describe, expect, it, vi } from "vitest";
import {
  AGENT_BACKEND_IDS,
  AGENT_BACKEND_OPTIONS,
  AGENT_DEFAULT_GLOBAL_CAP,
  isAgentBackendId,
} from "../src/agent-backend";

describe("isAgentBackendId", () => {
  it("accepts hermes", () => {
    expect(isAgentBackendId("hermes")).toBe(true);
  });
  it("rejects removed backend ids", () => {
    expect(isAgentBackendId("removed-runtime")).toBe(false);
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

describe("default concurrency cap", () => {
  it("starts three jobs concurrently", () => {
    expect(AGENT_DEFAULT_GLOBAL_CAP).toBe(3);
  });
});

describe("Harness opt-in", () => {
  it("keeps Hermes first and exposes only read-only broker lookup", async () => {
    const { makeAgentBackend } = await import("../src/agent-runtime");
    expect(AGENT_BACKEND_OPTIONS[0].value).toBe("hermes");
    const backend = makeAgentBackend({ id: "harness" });
    expect(backend.capabilities).toMatchObject({ mcpTools: false, sessionResume: false, brokerLookup: true });
  });
});


it("requires explicit Harness selection and rejects unknown selectors", async () => {
  const { resolveAgentBackendId } = await import("../src/agent-backend-resolver");
  try {
    vi.stubEnv("OPENNEKO_AGENT_BACKEND", "");
    expect(await resolveAgentBackendId("test")).toBe("hermes");
    vi.stubEnv("OPENNEKO_AGENT_BACKEND", "harness");
    expect(await resolveAgentBackendId("test")).toBe("harness");
    vi.stubEnv("OPENNEKO_AGENT_BACKEND", "unknown");
    await expect(resolveAgentBackendId("test")).rejects.toThrow("Unsupported");
  } finally { vi.unstubAllEnvs(); }
});

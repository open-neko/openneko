/**
 * agent-backend-resolver integration tests against a real Postgres.
 *
 * Validates the read-priority chain (DB → env → default) for both the
 * backend ID and the concurrency caps, plus the typed-error path when
 * claude-agent is selected without the necessary primary provider config.
 */

import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import {
  resolveAgentBackend,
  resolveAgentBackendId,
  resolveAgentConcurrency,
} from "../src/agent-backend-resolver";
import {
  AGENT_DEFAULT_GLOBAL_CAP,
  AgentBackendConfigError,
} from "../src/agent-backend";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[agent-backend-resolver] skipping: metadata Postgres unreachable. Run `docker compose up -d`.",
  );
}

const ORIGINAL_ENV = { ...process.env };

describeIfDb("resolveAgentBackendId", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("resolver");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    process.env = { ...ORIGINAL_ENV };
  });

  it("returns DB value when scope='agent' row exists", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    expect(await resolveAgentBackendId(orgId)).toBe("claude-agent");
  });

  it("returns 'hermes' default when no DB row exists", async () => {
    expect(await resolveAgentBackendId(orgId)).toBe("hermes");
  });

  it("returns 'hermes' default when DB row has invalid backend", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "openai",
      config: { backend: "openai" },
    });
    expect(await resolveAgentBackendId(orgId)).toBe("hermes");
  });
});

describeIfDb("resolveAgentBackend (instantiation + error paths)", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("resolver-instantiate");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    await clearProvider(orgId, "primary");
    process.env = { ...ORIGINAL_ENV };
  });

  it("hermes backend has no precondition (returns even with no primary row)", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes" },
    });
    const backend = await resolveAgentBackend(orgId);
    expect(backend.id).toBe("hermes");
  });

  it("claude-agent throws AgentBackendConfigError when no primary row", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await expect(resolveAgentBackend(orgId)).rejects.toBeInstanceOf(
      AgentBackendConfigError,
    );
  });

  it("claude-agent throws when primary provider != anthropic", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "google-gemini",
      model: "gemini-pro-latest",
    });
    await expect(resolveAgentBackend(orgId)).rejects.toThrow(
      /requires primary provider 'anthropic'/,
    );
  });

  it("claude-agent throws when primary is anthropic but disabled", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      enabled: false,
      secrets: { apiKey: "sk-test" },
    });
    await expect(resolveAgentBackend(orgId)).rejects.toThrow(/disabled/);
  });

  it("claude-agent throws when primary is anthropic with empty key", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: {},
    });
    await expect(resolveAgentBackend(orgId)).rejects.toBeInstanceOf(
      AgentBackendConfigError,
    );
  });

  it("claude-agent succeeds with anthropic + valid model + non-empty key", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: { apiKey: "sk-test-key" },
    });
    const backend = await resolveAgentBackend(orgId);
    expect(backend.id).toBe("claude-agent");
  });

  it("claude-agent rejects non-claude model on anthropic", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "gpt-4",
      secrets: { apiKey: "sk-test-key" },
    });
    await expect(resolveAgentBackend(orgId)).rejects.toBeInstanceOf(
      AgentBackendConfigError,
    );
  });
});

describeIfDb("resolveAgentConcurrency", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("resolver-concurrency");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    process.env = { ...ORIGINAL_ENV };
  });

  it("returns defaults with no row", async () => {
    const c = await resolveAgentConcurrency(orgId);
    expect(c.globalCap).toBe(AGENT_DEFAULT_GLOBAL_CAP);
  });

  it("returns DB value when row exists", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes", globalCap: 50 },
    });
    const c = await resolveAgentConcurrency(orgId);
    expect(c.globalCap).toBe(50);
  });

  it("malformed value falls back to default", async () => {
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes", globalCap: "not-a-number" },
    });
    const c = await resolveAgentConcurrency(orgId);
    expect(c.globalCap).toBe(AGENT_DEFAULT_GLOBAL_CAP);
  });
});

if (reachable) {
  afterAll(async () => {
    await pool().end();
  });
}

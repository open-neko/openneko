import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { and, db, eq, llm_provider_config, pool } from "@neko/db";
import {
  getAgentBackendSettings,
  getAgentSettingsPayload,
  saveAgentBackendDraft,
} from "@/lib/agent-backend-settings";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[agent-backend-settings] skipping: metadata Postgres unreachable.",
  );
}

async function readPrimary(orgId: string) {
  const rows = await db()
    .select({
      provider: llm_provider_config.provider,
      model: llm_provider_config.model,
      enabled: llm_provider_config.enabled,
      secrets: llm_provider_config.secrets,
    })
    .from(llm_provider_config)
    .where(
      and(
        eq(llm_provider_config.org_id, orgId),
        eq(llm_provider_config.scope, "primary"),
      ),
    );
  return rows[0] ?? null;
}

describeIfDb("agent-backend-settings", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("agent-settings");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    await clearProvider(orgId, "primary");
  });

  describe("getAgentBackendSettings", () => {
    it("returns default { hermes, 20 } with no row", async () => {
      const got = await getAgentBackendSettings(orgId);
      expect(got).toEqual({
        source: "default",
        backend: "hermes",
        globalCap: 20,
      });
    });

    it("returns DB values when row exists", async () => {
      await seedProvider(orgId, {
        scope: "agent",
        provider: "claude-agent",
        config: { backend: "claude-agent", globalCap: 30 },
      });
      const got = await getAgentBackendSettings(orgId);
      expect(got).toMatchObject({
        source: "org",
        backend: "claude-agent",
        globalCap: 30,
      });
    });
  });

  describe("getAgentSettingsPayload", () => {
    it("includes options + defaults", async () => {
      const payload = await getAgentSettingsPayload(orgId);
      expect(payload.agent.backend).toBe("hermes");
      expect(payload.options.length).toBeGreaterThanOrEqual(2);
      expect(payload.defaults).toEqual({ globalCap: 20 });
    });
  });

  describe("saveAgentBackendDraft — claude-agent auto-coerces primary", () => {
    it("with no primary row, creates anthropic primary row with empty secrets", async () => {
      await saveAgentBackendDraft(orgId, { backend: "claude-agent" });
      const primary = await readPrimary(orgId);
      expect(primary).not.toBeNull();
      expect(primary!.provider).toBe("anthropic");
      expect(primary!.enabled).toBe(true);
      expect(primary!.secrets).toEqual({});
    });

    it("with primary=google-gemini, rewrites primary to anthropic and clears secrets", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "google-gemini",
        model: "gemini-pro-latest",
        secrets: { apiKey: "old-gemini-key" },
      });
      await saveAgentBackendDraft(orgId, { backend: "claude-agent" });
      const primary = await readPrimary(orgId);
      expect(primary!.provider).toBe("anthropic");
      expect(primary!.secrets).toEqual({});
    });

    it("with primary=anthropic + key, preserves existing secrets", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "anthropic",
        model: "claude-opus-4-7",
        secrets: { apiKey: "existing-anthropic-key" },
      });
      await saveAgentBackendDraft(orgId, { backend: "claude-agent" });
      const primary = await readPrimary(orgId);
      expect(primary!.provider).toBe("anthropic");
      expect(primary!.secrets).toMatchObject({ apiKey: "existing-anthropic-key" });
    });
  });

  describe("saveAgentBackendDraft — hermes does not touch primary", () => {
    it("preserves existing non-anthropic primary", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "google-gemini",
        model: "gemini-pro-latest",
        secrets: { apiKey: "gemini-key" },
      });
      await saveAgentBackendDraft(orgId, { backend: "hermes" });
      const primary = await readPrimary(orgId);
      expect(primary!.provider).toBe("google-gemini");
      expect(primary!.secrets).toMatchObject({ apiKey: "gemini-key" });
    });
  });

  describe("saveAgentBackendDraft — concurrency cap", () => {
    it("persists valid numeric cap", async () => {
      await saveAgentBackendDraft(orgId, {
        backend: "hermes",
        globalCap: 50,
      });
      const got = await getAgentBackendSettings(orgId);
      expect(got.globalCap).toBe(50);
    });

    it("falls back to default for invalid value", async () => {
      await saveAgentBackendDraft(orgId, {
        backend: "hermes",
        globalCap: "not-a-number" as never,
      });
      const got = await getAgentBackendSettings(orgId);
      expect(got.globalCap).toBe(20);
    });
  });

  describe("saveAgentBackendDraft — invalid backend", () => {
    it("rejects unknown backend ids", async () => {
      await expect(
        saveAgentBackendDraft(orgId, { backend: "openai-agents" }),
      ).rejects.toThrow(/Unsupported agent backend/);
    });
  });
});

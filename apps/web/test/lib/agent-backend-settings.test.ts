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
import { AGENT_BACKEND_OPTIONS, AGENT_DEFAULT_GLOBAL_CAP, sandboxForOrg } from "@neko/llm";
import {
  getAgentSettings,
  getAgentSettingsPayload,
  saveAgentSettingsDraft,
} from "@/lib/agent-backend-settings";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[agent-settings] skipping: metadata Postgres unreachable.");
}

describeIfDb("agent-settings", () => {
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

  it("returns Ax concurrency defaults with no row", async () => {
    await expect(getAgentSettings(orgId)).resolves.toEqual({
      source: "default",
      backend: "ax",
      globalCap: AGENT_DEFAULT_GLOBAL_CAP,
      terminal: true,
    });
  });

  it("returns the backend and its only option", async () => {
    await expect(getAgentSettingsPayload(orgId)).resolves.toEqual({
      agent: {
        source: "default",
        backend: "ax",
        globalCap: AGENT_DEFAULT_GLOBAL_CAP,
        terminal: true,
      },
      options: AGENT_BACKEND_OPTIONS,
      terminalAvailable: false,
      defaults: { globalCap: AGENT_DEFAULT_GLOBAL_CAP, terminal: true },
    });
    expect(AGENT_BACKEND_OPTIONS.map((o) => o.value)).toEqual(["ax"]);
  });

  it("persists the backend and concurrency", async () => {
    await saveAgentSettingsDraft(orgId, { globalCap: 50 });
    expect((await getAgentSettings(orgId)).globalCap).toBe(50);

    const [row] = await db()
      .select({
        provider: llm_provider_config.provider,
        config: llm_provider_config.config,
      })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, "agent"),
        ),
      );
    expect(row.provider).toBe("ax");
    expect(row.config).toEqual({ backend: "ax", globalCap: 50, terminal: true });
  });

  it("falls back to the default for an invalid cap", async () => {
    await saveAgentSettingsDraft(orgId, { globalCap: "not-a-number" });
    expect((await getAgentSettings(orgId)).globalCap).toBe(
      AGENT_DEFAULT_GLOBAL_CAP,
    );
  });

  it("accepts Ax and rejects the removed Hermes backend", async () => {
    await expect(
      saveAgentSettingsDraft(orgId, { backend: "ax", globalCap: 8 }),
    ).resolves.toEqual({ source: "org", backend: "ax", globalCap: 8, terminal: true });
    await expect(
      saveAgentSettingsDraft(orgId, { backend: "hermes", globalCap: 8 }),
    ).rejects.toThrow("Unsupported agent backend: hermes");
  });

  it("saves concurrency before a primary provider exists", async () => {
    await expect(saveAgentSettingsDraft(orgId, { globalCap: 9 })).resolves.toEqual({
      source: "org",
      backend: "ax",
      globalCap: 9,
      terminal: true,
    });
  });

  it("turns the terminal off for the org's runs and back on", async () => {
    await saveAgentSettingsDraft(orgId, { terminal: false });
    expect((await getAgentSettings(orgId)).terminal).toBe(false);
    await saveAgentSettingsDraft(orgId, { globalCap: 4 });
    await expect(getAgentSettings(orgId)).resolves.toMatchObject({ globalCap: 4, terminal: false });
    await expect(saveAgentSettingsDraft(orgId, { terminal: "no" })).rejects.toThrow("terminal must be true or false");
    const env = { ...process.env, OPENNEKO_SANDBOX_URL: "unix:///run/openneko/sandbox.sock" };
    await expect(sandboxForOrg(orgId, env)).resolves.toBeUndefined();
    await saveAgentSettingsDraft(orgId, { terminal: true });
    await expect(sandboxForOrg(orgId, env)).resolves.toMatchObject({ url: env.OPENNEKO_SANDBOX_URL });
  });

  it("does not alter the primary provider", async () => {
    await seedProvider(orgId, {
      scope: "primary",
      provider: "google-gemini",
      model: "gemini-pro-latest",
      secrets: { apiKey: "gemini-key" },
    });
    await saveAgentSettingsDraft(orgId, { globalCap: 10 });
    const [primary] = await db()
      .select({ provider: llm_provider_config.provider })
      .from(llm_provider_config)
      .where(
        and(
          eq(llm_provider_config.org_id, orgId),
          eq(llm_provider_config.scope, "primary"),
        ),
      );
    expect(primary.provider).toBe("google-gemini");
  });
});

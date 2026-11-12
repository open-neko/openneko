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
  getProviderSettingsPayload,
  hasPrimaryProviderSetup,
  resolveResearchStatus,
} from "@/lib/provider-settings";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[provider-settings] skipping: metadata Postgres unreachable.",
  );
}

const ORIGINAL_ENV = { ...process.env };

describeIfDb("provider-settings", () => {
  let orgId: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("provider-settings");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  afterEach(async () => {
    await clearProvider(orgId, "agent");
    await clearProvider(orgId, "primary");
    await clearProvider(orgId, "research");
    process.env = { ...ORIGINAL_ENV };
  });

  describe("getProviderSettingsPayload", () => {
    it("returns default config when no rows", async () => {
      const payload = await getProviderSettingsPayload(orgId);
      expect(payload.primary.source).toBe("default");
      expect(payload.research.source).toBe("default");
    });

    it("returns the stored row when set", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "anthropic",
        model: "claude-opus-4-7",
        secrets: { apiKey: "sk-ant-stored" },
      });
      const payload = await getProviderSettingsPayload(orgId);
      expect(payload.primary.source).toBe("org");
      expect(payload.primary.provider).toBe("anthropic");
      // secretStatus is masked, never plaintext.
      expect(payload.primary.secretStatus.apiKey).not.toBe("sk-ant-stored");
      expect(payload.primary.secretStatus.apiKey).toMatch(/[•*]/);
    });
  });

  describe("hasPrimaryProviderSetup", () => {
    it("returns true with valid org row", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "anthropic",
        model: "claude-opus-4-7",
        secrets: { apiKey: "sk-ant" },
      });
      expect(await hasPrimaryProviderSetup(orgId)).toBe(true);
    });

    it("returns false with no row", async () => {
      expect(await hasPrimaryProviderSetup(orgId)).toBe(false);
    });

    it("returns false when row is disabled", async () => {
      await seedProvider(orgId, {
        scope: "primary",
        provider: "anthropic",
        model: "claude-opus-4-7",
        enabled: false,
        secrets: { apiKey: "sk-ant" },
      });
      expect(await hasPrimaryProviderSetup(orgId)).toBe(false);
    });
  });

  describe("resolveResearchStatus", () => {
    it("returns 'disabled' for explicit disabled provider", async () => {
      await seedProvider(orgId, {
        scope: "research",
        provider: "disabled",
        enabled: false,
      });
      expect(await resolveResearchStatus(orgId)).toBe("disabled");
    });

    it("returns 'enabled' for valid perplexity row", async () => {
      await seedProvider(orgId, {
        scope: "research",
        provider: "perplexity",
        model: "sonar-deep-research",
        enabled: true,
        secrets: { apiKey: "pplx-test" },
      });
      expect(await resolveResearchStatus(orgId)).toBe("enabled");
    });
  });
});

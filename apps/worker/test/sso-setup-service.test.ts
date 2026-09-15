import { describe, expect, it, vi } from "vitest";
import { db, eq, organization, pool, sso_setup } from "@neko/db";
import { SsoSetupService } from "../src/sso/sso-setup-service";
import type { PluginRegistry } from "../src/plugins/plugin-registry";

const reachable = await pool().query("select 1").then(() => true, () => false);

(reachable ? describe : describe.skip)("SsoSetupService.setScalekitIds", () => {
  it("stores the organization for sign-in setup and for directory sync", async () => {
    const orgId = `sso-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
    await db().insert(organization).values({ id: orgId, name: "SSO" });
    const setPluginSecret = vi.fn(async () => undefined);
    const registry = {
      getAuthProvider: () => ({ pluginId: "open-neko-plugin-scalekit", pluginName: "@open-neko/plugin-scalekit" }),
      setPluginSecret,
    } as unknown as PluginRegistry;
    try {
      await new SsoSetupService(() => registry).setScalekitIds(orgId, { environmentId: "env_1", organizationId: "org_9", tier: "dev" });
      const [row] = await db().select().from(sso_setup).where(eq(sso_setup.org_id, orgId));
      expect(row).toMatchObject({ scalekit_environment_id: "env_1", scalekit_organization_id: "org_9" });
      expect(setPluginSecret).toHaveBeenCalledWith("@open-neko/plugin-scalekit", "SCALEKIT_ORGANIZATION_ID", "org_9");
    } finally {
      await db().delete(sso_setup).where(eq(sso_setup.org_id, orgId));
      await db().delete(organization).where(eq(organization.id, orgId));
    }
  });
});

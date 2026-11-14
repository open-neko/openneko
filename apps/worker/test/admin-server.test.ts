/**
 * Tests for the worker's admin HTTP handler.
 */

import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createAdminHandler } from "../src/admin-server";
import { PackPreflightError } from "../src/packs/preflight.js";

async function startServer(handler: ReturnType<typeof createAdminHandler>) {
  const server = createServer(handler);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = (server.address() as AddressInfo).port;
  return {
    port,
    close: () => new Promise<void>((resolve, reject) =>
      server.close((err) => (err ? reject(err) : resolve())),
    ),
  };
}

describe("worker admin HTTP handler", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("moved health, reconnect, source-check, and install-policy routes return 404", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const [method, path] of [
        ["GET", "/health"],
        ["GET", "/health/scheduler"],
        ["GET", "/health/security"],
        ["POST", "/admin/reconnect"],
        ["POST", "/admin/sources/check"],
        ["GET", "/admin/install-policy"],
      ] as const) {
        const res = await fetch(`http://127.0.0.1:${srv.port}${path}`, {
          method,
        });
        expect(res.status).toBe(404);
      }
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/action-requests/create delegates to worker preflight", async () => {
    const create = vi.fn(async () => ({ id: "action-prepared", status: "approved" }));
    const srv = await startServer(
      createAdminHandler({ actionRequests: { create } }),
    );
    try {
      const input = {
        orgId: "org-test",
        kind: "user_admin",
        scope: "internal",
        status: "pending_approval",
        payload: { email: "new@example.com" },
      };
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/action-requests/create`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(input),
        },
      );
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({ id: "action-prepared", status: "approved" });
      expect(create).toHaveBeenCalledWith(input);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/action-requests/create fails closed before preflight is ready", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/action-requests/create`,
        { method: "POST", body: "{}" },
      );
      expect(res.status).toBe(503);
      expect(await res.json()).toEqual({
        error: "action request preflight is not ready",
      });
    } finally {
      await srv.close();
    }
  });

  it("unknown path returns 404", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/whatever`);
      expect(res.status).toBe(404);
    } finally {
      await srv.close();
    }
  });
});

describe("worker solution-pack admin routes", () => {
  it("routes the supported pack lifecycle without filtering artifacts", async () => {
    const surface = {
      list: vi.fn(async () => [{ id: "magento", installed: false }]),
      inspect: vi.fn(async (packId: string) => ({ packId })),
      plan: vi.fn(async (packId: string) => ({ packId, entries: [{ key: "action.add_internal_order_comment" }] })),
      status: vi.fn(async (packId: string) => ({ packId, status: "installed" })),
      doctor: vi.fn(async (packId: string) => ({ packId, status: "degraded" })),
      install: vi.fn(async (packId: string, input: Record<string, unknown>) => ({
        packId,
        status: "installed",
        input,
      })),
      configure: vi.fn(async (packId: string, input: Record<string, unknown>) => ({
        packId,
        status: "installed",
        input,
      })),
      upgrade: vi.fn(async (packId: string, input: Record<string, unknown>) => ({
        packId,
        status: "installed",
        input,
      })),
      uninstall: vi.fn(async (packId: string, input: Record<string, unknown>) => ({
        packId,
        status: "removed",
        input,
      })),
      upload: vi.fn(async () => ({ packId: "service-health", version: "0.1.0" })),
      review: vi.fn(async () => ({ reviewHash: "reviewed" })),
      magentoStoreManagement: vi.fn(async () => ({ controls: [{ domain: "catalog" }] })),
      updateMagentoStoreManagement: vi.fn(async (input: Record<string, unknown>) => ({ input })),
    };
    const srv = await startServer(createAdminHandler({ packs: surface }));
    try {
      const gone = await fetch(`http://127.0.0.1:${srv.port}/admin/context-remote/updates`);
      expect(gone.status).toBe(404);
      await gone.body?.cancel();

      const list = await fetch(`http://127.0.0.1:${srv.port}/admin/packs`);
      expect(list.status).toBe(200);
      expect(await list.json()).toEqual({ packs: [{ id: "magento", installed: false }] });

      for (const [suffix, method] of [
        ["", "inspect"],
        ["/plan", "plan"],
        ["/status", "status"],
        ["/doctor", "doctor"],
      ] as const) {
        const response = await fetch(
          `http://127.0.0.1:${srv.port}/admin/packs/magento${suffix}`,
        );
        expect(response.status).toBe(200);
        expect(surface[method]).toHaveBeenCalledWith("magento");
      }

      const input = {
        inputs: { "magento.base_url": "http://magento.test" },
        secrets: { "database.analytics_username": "analytics" },
      };
      const install = await fetch(
        `http://127.0.0.1:${srv.port}/admin/packs/magento/install`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(input),
        },
      );
      expect(install.status).toBe(200);
      expect(surface.install).toHaveBeenCalledWith("magento", input);

      surface.install.mockRejectedValueOnce(new PackPreflightError(
        "account_profile",
        "graphjin/saved-queries/account_profile.gql",
        ["table not found: customer_api_get_profile"],
      ));
      const failedInstall = await fetch(
        `http://127.0.0.1:${srv.port}/admin/packs/google-workspace/install`,
        { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(input) },
      );
      expect(failedInstall.status).toBe(400);
      expect(await failedInstall.json()).toMatchObject({
        error: expect.stringContaining("graphjin/saved-queries/account_profile.gql"),
        diagnostic: {
          code: "pack_query_preflight_failed",
          phase: "query_preflight",
          artifact: { kind: "saved_query", name: "account_profile", path: "graphjin/saved-queries/account_profile.gql" },
          causes: ["table not found: customer_api_get_profile"],
        },
      });

      for (const action of ["configure", "upgrade", "uninstall"] as const) {
        const response = await fetch(
          `http://127.0.0.1:${srv.port}/admin/packs/magento/${action}`,
          {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify(input),
          },
        );
        expect(response.status).toBe(200);
        expect(surface[action]).toHaveBeenCalledWith("magento", input);
      }

      const upload = await fetch(`http://127.0.0.1:${srv.port}/admin/packs/upload`, {
        method: "POST", headers: { "content-type": "application/zip", "x-openneko-actor": "admin-1" }, body: Buffer.from("fixture ZIP"),
      });
      expect(upload.status).toBe(200);
      expect(surface.upload).toHaveBeenCalledWith(Buffer.from("fixture ZIP"), expect.objectContaining({ actorUserId: "admin-1" }));
      const tooLarge = await fetch(`http://127.0.0.1:${srv.port}/admin/packs/upload`, {
        method: "POST", headers: { "content-type": "application/zip" }, body: Buffer.alloc(16 * 1024 * 1024 + 1),
      });
      expect(tooLarge.status).toBe(413);
      expect(surface.upload).toHaveBeenCalledTimes(1);
      const review = await fetch(`http://127.0.0.1:${srv.port}/admin/packs/service-health/review`, {
        method: "POST", body: JSON.stringify({ operation: "upgrade", version: "0.2.0" }),
      });
      expect(review.status).toBe(200);
      expect(surface.review).toHaveBeenCalledWith("service-health", { operation: "upgrade", version: "0.2.0" }, "upgrade");
      surface.review.mockRejectedValueOnce(new Error("manifest input service.base_url is missing"));
      const failedReview = await fetch(`http://127.0.0.1:${srv.port}/admin/packs/service-health/review`, {
        method: "POST", body: JSON.stringify({ operation: "install" }),
      });
      expect(await failedReview.json()).toMatchObject({
        error: "manifest input service.base_url is missing",
        diagnostic: {
          code: "pack_operation_failed",
          phase: "review",
          causes: ["manifest input service.base_url is missing"],
          nextStep: expect.any(String),
        },
      });
      const management = await fetch(
        `http://127.0.0.1:${srv.port}/admin/packs/magento/store-management`,
      );
      expect(management.status).toBe(200);
      expect(await management.json()).toEqual({ controls: [{ domain: "catalog" }] });

      const managementInput = { action: "update_domain", domain: "catalog", enabled: true };
      const updateManagement = await fetch(
        `http://127.0.0.1:${srv.port}/admin/packs/magento/store-management`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(managementInput),
        },
      );
      expect(updateManagement.status).toBe(200);
      expect(surface.updateMagentoStoreManagement).toHaveBeenCalledWith(managementInput);

    } finally {
      await srv.close();
    }
  });

  it("fails closed when no pack surface is wired", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const response = await fetch(`http://127.0.0.1:${srv.port}/admin/packs`);
      expect(response.status).toBe(503);
    } finally {
      await srv.close();
    }
  });

  it("does not accept trailing junk as a pack lifecycle route", async () => {
    const inspect = vi.fn(async () => ({ packId: "magento" }));
    const srv = await startServer(createAdminHandler({
      packs: {
        list: async () => [],
        inspect,
        plan: async () => ({}),
        status: async () => ({}),
        doctor: async () => ({}),
        install: async () => ({}),
        configure: async () => ({}),
        upgrade: async () => ({}),
        uninstall: async () => ({}),
        upload: async () => ({}),
        review: async () => ({}),
        magentoStoreManagement: async () => ({}),
        updateMagentoStoreManagement: async () => ({}),
      },
    }));
    try {
      const response = await fetch(
        `http://127.0.0.1:${srv.port}/admin/packs/magento/status-extra`,
      );
      expect(response.status).toBe(404);
      expect(inspect).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });
});

describe("worker admin: removed plugin and action-descriptor routes", () => {
  it("answers 404", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const path of ["/admin/action-descriptors", "/admin/plugins/status", "/admin/plugins/settings", "/admin/plugins/action-descriptors"]) {
        expect((await fetch(`http://127.0.0.1:${srv.port}${path}`)).status).toBe(404);
      }
    } finally {
      await srv.close();
    }
  });
});

describe("worker admin /admin/actions/execute", () => {
  it("runs an approved action once the adapters are ready", async () => {
    let ready = false;
    const execute = vi.fn(async (input: { orgId: string; actionRequestId: string }) => {
      if (input.actionRequestId === "ar-boom") throw new Error("adapter failed");
    });
    const srv = await startServer(createAdminHandler({ actions: { ready: () => ready, execute } }));
    const post = async (body: unknown) => {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/actions/execute`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body),
      });
      return [res.status, await res.json()];
    };
    try {
      expect(await post({ orgId: "org", actionRequestId: "ar-1" })).toEqual([503, { error: "action adapters are not ready" }]);
      ready = true;
      expect(await post({ orgId: "org" })).toEqual([400, { error: "orgId and actionRequestId are required" }]);
      expect(await post({ orgId: "org", actionRequestId: "ar-1" })).toEqual([200, { ok: true }]);
      expect(await post({ orgId: "org", actionRequestId: "ar-boom" })).toEqual([500, { error: "adapter failed" }]);
      expect(execute.mock.calls).toEqual([[{ orgId: "org", actionRequestId: "ar-1" }], [{ orgId: "org", actionRequestId: "ar-boom" }]]);
    } finally {
      await srv.close();
    }
  });

  it("answers 503 with no surface", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/actions/execute`, { method: "POST", body: "{}" });
      expect(res.status).toBe(503);
    } finally {
      await srv.close();
    }
  });
});

describe("worker admin /admin/connect/*", () => {
  const sampleCredential = () => ({
    tokens: { access_token: "at-1", refresh_token: "rt-1" },
    scopes: ["gmail.send"],
    providerLabel: "Google Workspace",
    connectedAt: "2026-05-21T10:00:00Z",
  });

  function fakeConnect(overrides: Partial<{
    getConnectProviders: () => ReturnType<NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["getConnectProviders"]>;
    getOperatorConnectStatus: (operatorId: string) => ReturnType<NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["getOperatorConnectStatus"]>;
    beginConnect: NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["beginConnect"];
    completeConnect: NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["completeConnect"];
    disconnect: NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["disconnect"];
  }> = {}) {
    return {
      getConnectProviders: overrides.getConnectProviders ?? (() => []),
      getOperatorConnectStatus: overrides.getOperatorConnectStatus ?? (() => []),
      beginConnect:
        overrides.beginConnect ??
        (async () => ({ authorizationUrl: "https://x" })),
      completeConnect:
        overrides.completeConnect ?? (async () => sampleCredential()),
      disconnect: overrides.disconnect ?? (async () => true),
    };
  }

  it("GET /admin/connect/providers returns the registry's list", async () => {
    const srv = await startServer(
      createAdminHandler({
        connect: fakeConnect({
          getConnectProviders: () => [
            {
              pluginId: "open-neko-connector-google-workspace",
              pluginName: "@open-neko/connector-google-workspace",
              providerLabel: "Google Workspace",
              scopes: ["gmail.readonly"],
            },
          ],
        }),
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/providers`);
      expect(res.status).toBe(200);
      const body = (await res.json()) as { providers: Array<{ providerLabel: string }> };
      expect(body.providers[0]?.providerLabel).toBe("Google Workspace");
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/connect/providers returns [] when connect surface absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/providers`);
      const body = (await res.json()) as { providers: unknown[] };
      expect(body.providers).toEqual([]);
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/connect/status/:operatorId returns per-operator status", async () => {
    const srv = await startServer(
      createAdminHandler({
        connect: fakeConnect({
          getOperatorConnectStatus: (operatorId) => [
            {
              pluginName: `${operatorId}-plugin`,
              connectedAt: "2026-05-21T10:00:00Z",
            },
          ],
        }),
      }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/connect/status/op-1`,
      );
      expect(res.status).toBe(200);
      const body = (await res.json()) as {
        connected: Array<{ pluginName: string }>;
      };
      expect(body.connected[0]?.pluginName).toBe("op-1-plugin");
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/begin proxies to the plugin", async () => {
    let captured: { plugin?: string; params?: unknown } = {};
    const srv = await startServer(
      createAdminHandler({
        connect: fakeConnect({
          beginConnect: async (plugin, params) => {
            captured = { plugin, params };
            return { authorizationUrl: "https://provider/auth?x" };
          },
        }),
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          pluginName: "@x/y",
          params: {
            operatorId: "op-1",
            redirectUri: "https://app/cb",
            state: "csrf",
            scopes: ["s"],
          },
        }),
      });
      expect(res.status).toBe(200);
      const body = (await res.json()) as { authorizationUrl: string };
      expect(body.authorizationUrl).toBe("https://provider/auth?x");
      expect(captured.plugin).toBe("@x/y");
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/begin returns 400 on missing pluginName", async () => {
    const srv = await startServer(
      createAdminHandler({ connect: fakeConnect() }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ params: {} }),
      });
      expect(res.status).toBe(400);
      expect(((await res.json()) as { error: string }).error).toMatch(/pluginName/);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/begin returns 503 when connect surface disabled", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pluginName: "@x/y", params: {} }),
      });
      expect(res.status).toBe(503);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/complete returns the credential", async () => {
    const srv = await startServer(
      createAdminHandler({ connect: fakeConnect() }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/connect/complete`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            pluginName: "@x/y",
            params: {
              operatorId: "op-1",
              code: "auth-code",
              redirectUri: "https://app/cb",
              state: "csrf",
              scopes: [],
            },
          }),
        },
      );
      expect(res.status).toBe(200);
      const body = (await res.json()) as {
        credential: { tokens: Record<string, string> };
      };
      expect(body.credential.tokens.access_token).toBe("at-1");
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/disconnect returns { removed }", async () => {
    const srv = await startServer(
      createAdminHandler({
        connect: fakeConnect({ disconnect: async () => true }),
      }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/connect/disconnect`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ pluginName: "@x/y", operatorId: "op-1" }),
        },
      );
      expect(res.status).toBe(200);
      const body = (await res.json()) as { removed: boolean };
      expect(body.removed).toBe(true);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/begin propagates provider errors as 500", async () => {
    const srv = await startServer(
      createAdminHandler({
        connect: fakeConnect({
          beginConnect: async () => {
            throw new Error("auth_url_build_failed");
          },
        }),
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/connect/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ pluginName: "@x/y", params: {} }),
      });
      expect(res.status).toBe(500);
      expect(((await res.json()) as { error: string }).error).toMatch(/auth_url_build_failed/);
    } finally {
      await srv.close();
    }
  });
});

describe("worker channel routes", () => {
  it("POST /admin/channels/:p/deliver is gone: openneko web sends", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/channels/x/deliver`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ recipient: { kind: "x" }, events: [] }),
      });
      expect(res.status).toBe(404);
    } finally {
      await srv.close();
    }
  });

  it("the inbound webhook, the channel settings and the provider list are gone: openneko web owns them", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const [method, path] of [["POST", "/channels/telegram/inbound"], ["GET", "/admin/channels/settings"], ["POST", "/admin/channels/settings"], ["GET", "/admin/channels/providers"]]) {
        const res = await fetch(`http://127.0.0.1:${srv.port}${path}`, { method, headers: { "Content-Type": "application/json" }, ...(method === "POST" ? { body: "{}" } : {}) });
        expect(res.status).toBe(404);
      }
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/sso/setup/status returns the setup state", async () => {
    const srv = await startServer(
      createAdminHandler({
        ssoSetup: {
          getStatus: async () => ({
            status: "awaiting_portal",
            portalLink: "https://scalekit.test/portal",
            portalLinkExpiresAt: null,
            provider: "OKTA",
            connection: { status: "in_progress", provider: "OKTA", enabled: false },
            lastError: null,
            setupCompletedAt: null,
            environmentId: "env_1",
            organizationId: "org_1",
            environmentTier: "dev",
            signInConfigured: false,
          }),
          setScalekitIds: async () => {},
          setSignInCredentials: async () => {},
          listEnvironments: async () => [
            { id: "env_1", name: "Dev", tier: "DEV", domain: "acme.scalekit.dev" },
          ],
          listOrganizations: async () => [{ id: "org_1", name: "Acme" }],
          getEnvironmentCredentials: async () => ({
            environmentUrl: "https://acme.scalekit.dev",
            clientId: "skc_1",
          }),
          generatePortalLink: async () => ({
            portalLink: "https://scalekit.test/portal",
            expiresAt: null,
          }),
          checkConnection: async () => ({
            status: "connected",
            connection: { status: "connected", provider: "OKTA", enabled: true },
            lastError: null,
            setupCompletedAt: "2026-08-13T12:00:00Z",
          }),
        },
      }),
    );
    try {
      const status = await fetch(
        `http://127.0.0.1:${srv.port}/admin/sso/setup/status?orgId=org-1`,
      );
      expect(status.status).toBe(200);
      const body = (await status.json()) as { status: string; environmentTier: string };
      expect(body.status).toBe("awaiting_portal");
      expect(body.environmentTier).toBe("dev");

      const envs = await fetch(
        `http://127.0.0.1:${srv.port}/admin/sso/setup/environments?orgId=org-1`,
      );
      expect(envs.status).toBe(200);
      const envBody = (await envs.json()) as {
        environments: Array<{ tier: string }>;
      };
      expect(envBody.environments).toHaveLength(1);

      const check = await fetch(`http://127.0.0.1:${srv.port}/admin/sso/setup/check`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ orgId: "org-1" }),
      });
      expect(check.status).toBe(200);
      const checkBody = (await check.json()) as { status: string };
      expect(checkBody.status).toBe("connected");
    } finally {
      await srv.close();
    }
  });

  it("sso setup endpoints return 503 when surface absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/sso/setup/status?orgId=org-1`,
      );
      expect(res.status).toBe(503);
    } finally {
      await srv.close();
    }
  });
});

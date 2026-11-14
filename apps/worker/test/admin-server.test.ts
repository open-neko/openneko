/**
 * Tests for the worker's admin HTTP handler.
 */

import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createAdminHandler } from "../src/admin-server";

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

  it("moved health, reconnect, source-check, install-policy, graphjin, and external-event routes return 404", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const [method, path] of [
        ["GET", "/health"],
        ["GET", "/health/scheduler"],
        ["GET", "/health/security"],
        ["POST", "/admin/reconnect"],
        ["POST", "/admin/sources/check"],
        ["GET", "/admin/install-policy"],
        ["GET", "/admin/graphjin/api-operations"],
        ["POST", "/admin/graphjin/group-grants"],
        ["POST", "/admin/graphjin/group-grants/enable"],
        ["POST", "/admin/graphjin/group-grants/disable"],
        ["POST", "/admin/events/external"],
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

describe("worker admin: removed plugin, action-descriptor and pack routes", () => {
  it("answers 404", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const path of ["/admin/action-descriptors", "/admin/plugins/status", "/admin/plugins/settings", "/admin/plugins/action-descriptors", "/admin/packs", "/admin/packs/magento/inspect", "/admin/packs/magento/store-management"]) {
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

  it("the /admin/sso/setup/* routes are gone: openneko web owns them", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      for (const [method, path] of [
        ["GET", "/admin/sso/setup/status?orgId=org-1"],
        ["GET", "/admin/sso/setup/environments?orgId=org-1"],
        ["GET", "/admin/sso/setup/organizations?orgId=org-1&environmentId=env-1"],
        ["GET", "/admin/sso/setup/credentials-info?orgId=org-1&environmentId=env-1"],
        ["POST", "/admin/sso/setup/ids"],
        ["POST", "/admin/sso/setup/credentials"],
        ["POST", "/admin/sso/setup/generate-portal-link"],
        ["POST", "/admin/sso/setup/check"],
      ]) {
        const res = await fetch(`http://127.0.0.1:${srv.port}${path}`, {
          method,
          headers: { "Content-Type": "application/json" },
          ...(method === "POST" ? { body: "{}" } : {}),
        });
        expect(res.status).toBe(404);
      }
    } finally {
      await srv.close();
    }
  });
});

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

  it("moved health, reconnect, source-check, install-policy, graphjin, external-event, directory and connect routes return 404", async () => {
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
        ["GET", "/admin/directory/status"],
        ["POST", "/admin/directory/sync"],
        ["POST", "/admin/directory/users"],
        ["GET", "/admin/connect/providers"],
        ["GET", "/admin/connect/deployment/status"],
        ["GET", "/admin/connect/status/op-1"],
        ["POST", "/admin/connect/begin"],
        ["POST", "/admin/connect/complete"],
        ["POST", "/admin/connect/disconnect"],
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

  it("POST /admin/action-requests/create is gone: openneko web creates action requests", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/action-requests/create`,
        { method: "POST", body: "{}" },
      );
      expect(res.status).toBe(404);
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
      for (const path of ["/admin/action-descriptors", "/admin/actions/execute", "/admin/plugins/status", "/admin/plugins/settings", "/admin/plugins/action-descriptors", "/admin/packs", "/admin/packs/magento/inspect", "/admin/packs/magento/store-management"]) {
        expect((await fetch(`http://127.0.0.1:${srv.port}${path}`)).status).toBe(404);
      }
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

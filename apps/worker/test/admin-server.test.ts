/**
 * Tests for the worker's /health + /admin/reconnect HTTP handler. The
 * /admin/reconnect signal is fired by the web app's change-password
 * handler after rotating the Postgres password; the worker must respond
 * 202 and exit cleanly so the supervisor (`tsx watch` / Cloud Run)
 * restarts it with fresh credentials.
 */

import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
  let exit: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    exit = vi.fn();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("GET /health returns 200 ok", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/health`);
      expect(res.status).toBe(200);
      expect(await res.text()).toBe("ok");
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/reconnect returns 202 and triggers process.exit(0)", async () => {
    const srv = await startServer(
      createAdminHandler({ exit, exitDelayMs: 0 }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/reconnect`, {
        method: "POST",
      });
      expect(res.status).toBe(202);
      expect(await res.text()).toBe("restarting");
      // setTimeout(0) microtask — flush.
      await new Promise((r) => setTimeout(r, 5));
      expect(exit).toHaveBeenCalledTimes(1);
      expect(exit).toHaveBeenCalledWith(0);
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/reconnect (wrong method) returns 404 and does not exit", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/reconnect`);
      expect(res.status).toBe(404);
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });

  it("unknown path returns 404", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/whatever`);
      expect(res.status).toBe(404);
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });
});

describe("worker admin /admin/auth/*", () => {
  it("GET /admin/auth/status returns { provider: null } when no auth surface is wired", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/status`);
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({ provider: null });
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/auth/status returns provider info when one is registered", async () => {
    const srv = await startServer(
      createAdminHandler({
        auth: {
          getAuthProvider: () => ({
            pluginName: "@open-neko/plugin-scalekit",
            providerLabel: "Scalekit",
          }),
          beginAuth: async () => ({ authorizationUrl: "https://x" }),
          completeAuth: async () => ({
            sub: "u",
            email: "u@e.com",
            groups: [],
          }),
        },
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/status`);
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({
        provider: {
          pluginName: "@open-neko/plugin-scalekit",
          providerLabel: "Scalekit",
        },
      });
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/auth/begin proxies to the auth surface", async () => {
    const calls: Array<{
      redirectUri: string;
      state: string;
      loginHint?: string | null;
    }> = [];
    const srv = await startServer(
      createAdminHandler({
        auth: {
          getAuthProvider: () => null,
          beginAuth: async (p) => {
            calls.push(p);
            return { authorizationUrl: `https://idp/oauth?state=${p.state}` };
          },
          completeAuth: async () => ({
            sub: "u",
            email: "u@e.com",
            groups: [],
          }),
        },
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          redirectUri: "https://app.example.com/cb",
          state: "csrf-1",
          loginHint: "amit@example.com",
        }),
      });
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({
        authorizationUrl: "https://idp/oauth?state=csrf-1",
      });
      expect(calls).toEqual([
        {
          redirectUri: "https://app.example.com/cb",
          state: "csrf-1",
          loginHint: "amit@example.com",
        },
      ]);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/auth/begin returns 400 when required fields are missing", async () => {
    const srv = await startServer(
      createAdminHandler({
        auth: {
          getAuthProvider: () => null,
          beginAuth: async () => ({ authorizationUrl: "x" }),
          completeAuth: async () => ({
            sub: "u",
            email: "u@e.com",
            groups: [],
          }),
        },
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ redirectUri: "https://x" }),
      });
      expect(res.status).toBe(400);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/auth/complete returns the identity", async () => {
    const srv = await startServer(
      createAdminHandler({
        auth: {
          getAuthProvider: () => null,
          beginAuth: async () => ({ authorizationUrl: "x" }),
          completeAuth: async ({ code }) => ({
            sub: `sub-${code}`,
            email: "amit@example.com",
            name: "Amit",
            orgId: "org-1",
            groups: ["everyone"],
          }),
        },
      }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/auth/complete`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            code: "auth-code",
            redirectUri: "https://app.example.com/cb",
            state: "csrf-1",
          }),
        },
      );
      expect(res.status).toBe(200);
      const body = (await res.json()) as { identity: { sub: string } };
      expect(body.identity.sub).toBe("sub-auth-code");
    } finally {
      await srv.close();
    }
  });

  it("auth endpoints return 503 when no auth surface is wired", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const begin = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          redirectUri: "https://x",
          state: "x",
        }),
      });
      expect(begin.status).toBe(503);
    } finally {
      await srv.close();
    }
  });

  it("propagates plugin errors as 500", async () => {
    const srv = await startServer(
      createAdminHandler({
        auth: {
          getAuthProvider: () => null,
          beginAuth: async () => {
            throw new Error("SCALEKIT_CLIENT_SECRET not set");
          },
          completeAuth: async () => ({
            sub: "u",
            email: "u@e.com",
            groups: [],
          }),
        },
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/auth/begin`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          redirectUri: "https://x",
          state: "x",
        }),
      });
      expect(res.status).toBe(500);
      expect(((await res.json()) as { error: string }).error).toMatch(
        /SCALEKIT_CLIENT_SECRET/,
      );
    } finally {
      await srv.close();
    }
  });

  // ─── /admin/connect/* (per-operator OAuth) ─────────────────────────

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
    refreshConnect: NonNullable<Parameters<typeof createAdminHandler>[0]["connect"]>["refreshConnect"];
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
      refreshConnect:
        overrides.refreshConnect ?? (async () => sampleCredential()),
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

  it("POST /admin/connect/refresh requires operatorId + pluginName", async () => {
    const srv = await startServer(
      createAdminHandler({ connect: fakeConnect() }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/connect/refresh`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ pluginName: "@x/y" }),
        },
      );
      expect(res.status).toBe(400);
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

  it("GET /admin/install-policy returns the configured policy", async () => {
    const srv = await startServer(
      createAdminHandler({
        installPolicy: {
          getInstallPolicy: async () => ({
            allowUnverified: true,
            allowGitUrlInstalls: false,
            allowedMarketplaces: ["https://x"],
            allowSandboxedSkillEscape: false,
          }),
        },
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/install-policy`);
      expect(res.status).toBe(200);
      const body = (await res.json()) as {
        policy: { allowUnverified: boolean };
        source: string;
      };
      expect(body.source).toBe("org");
      expect(body.policy.allowUnverified).toBe(true);
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/install-policy returns default policy when surface absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/install-policy`);
      expect(res.status).toBe(200);
      const body = (await res.json()) as {
        policy: { allowUnverified: boolean };
        source: string;
      };
      expect(body.source).toBe("default");
      expect(body.policy.allowUnverified).toBe(false);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/connect/begin propagates plugin errors as 500", async () => {
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
  function fakeChannel(
    overrides: Partial<NonNullable<Parameters<typeof createAdminHandler>[0]["channels"]>> = {},
  ): NonNullable<Parameters<typeof createAdminHandler>[0]["channels"]> {
    return {
      getChannelProviders: overrides.getChannelProviders ?? (() => []),
      deliver: overrides.deliver ?? (async () => ({ delivered: true, ref: "1" })),
      ingestInbound: overrides.ingestInbound ?? (async () => ({ ok: true, dispatched: 0 })),
    };
  }

  it("GET /admin/channels/providers returns the registry's channels", async () => {
    const srv = await startServer(
      createAdminHandler({
        channels: fakeChannel({
          getChannelProviders: () => [
            {
              pluginId: "open-neko-channel-telegram",
              pluginName: "@open-neko/channel-telegram",
              providerLabel: "Telegram",
              directions: ["outbound", "inbound"],
              ingress: "webhook",
            },
          ],
        }),
      }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/channels/providers`);
      expect(res.status).toBe(200);
      const body = (await res.json()) as { providers: Array<{ providerLabel: string }> };
      expect(body.providers[0]?.providerLabel).toBe("Telegram");
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/channels/providers returns [] when the channel surface is absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/channels/providers`);
      expect(((await res.json()) as { providers: unknown[] }).providers).toEqual([]);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/channels/:p/deliver proxies recipient + events to the surface", async () => {
    let captured: { plugin?: string; recipient?: unknown; events?: unknown[] } = {};
    const srv = await startServer(
      createAdminHandler({
        channels: fakeChannel({
          deliver: async (plugin, recipient, events) => {
            captured = { plugin, recipient, events };
            return { delivered: true, ref: "278" };
          },
        }),
      }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/admin/channels/@open-neko%2Fchannel-telegram/deliver`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ recipient: { kind: "telegram", chatId: 5 }, events: [{ kind: "inform" }] }),
        },
      );
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({ delivered: true, ref: "278" });
      expect(captured.plugin).toBe("@open-neko/channel-telegram");
      expect((captured.recipient as { chatId: number }).chatId).toBe(5);
      expect(captured.events).toHaveLength(1);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/channels/:p/deliver returns 400 on missing events", async () => {
    const srv = await startServer(createAdminHandler({ channels: fakeChannel() }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/channels/x/deliver`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ recipient: { kind: "x" } }),
      });
      expect(res.status).toBe(400);
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/channels/:p/deliver returns 503 when surface absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/channels/x/deliver`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ recipient: { kind: "x" }, events: [] }),
      });
      expect(res.status).toBe(503);
    } finally {
      await srv.close();
    }
  });

  it("POST /channels/:p/inbound runs the surface's verify->parse->dispatch", async () => {
    let captured: { plugin?: string; headers?: Record<string, string>; body?: string } = {};
    const srv = await startServer(
      createAdminHandler({
        channels: fakeChannel({
          ingestInbound: async (plugin, headers, body) => {
            captured = { plugin, headers, body };
            return { ok: true, dispatched: 1 };
          },
        }),
      }),
    );
    try {
      const res = await fetch(
        `http://127.0.0.1:${srv.port}/channels/@open-neko%2Fchannel-telegram/inbound`,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-Telegram-Bot-Api-Secret-Token": "s3cret",
          },
          body: JSON.stringify({ callback_query: { data: "approve:ar-1" } }),
        },
      );
      expect(res.status).toBe(200);
      expect(await res.json()).toEqual({ ok: true, dispatched: 1 });
      expect(captured.plugin).toBe("@open-neko/channel-telegram");
      expect(captured.headers?.["x-telegram-bot-api-secret-token"]).toBe("s3cret");
      expect(captured.body).toContain("approve:ar-1");
    } finally {
      await srv.close();
    }
  });

  it("POST /channels/:p/inbound returns 503 when surface absent", async () => {
    const srv = await startServer(createAdminHandler());
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/channels/x/inbound`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      expect(res.status).toBe(503);
    } finally {
      await srv.close();
    }
  });
});

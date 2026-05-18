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
});

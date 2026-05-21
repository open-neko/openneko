/**
 * /api/integrations/* contract tests. The web routes proxy to the
 * worker's admin port; we stub the fetch calls and confirm the
 * routes thread auth + state-cookie semantics correctly.
 */

import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type MockInstance,
} from "vitest";
import { callRoute } from "../_helpers/route";

const { mockGetCurrentUser } = vi.hoisted(() => ({
  mockGetCurrentUser: vi.fn(),
}));

vi.mock("@/lib/auth", async () => {
  const actual = await vi.importActual<typeof import("@/lib/auth")>("@/lib/auth");
  return { ...actual, getCurrentUser: mockGetCurrentUser };
});

// `MockInstance` (no generic args) keeps the type loose enough to
// hold the spy on `globalThis.fetch` (overloaded signature) without
// CI's stricter TS resolution rejecting the assignment.
let fetchMock: MockInstance;

beforeAll(() => {
  process.env.OPENNEKO_SESSION_SECRET = process.env.OPENNEKO_SESSION_SECRET ?? "a".repeat(48);
});

beforeEach(() => {
  fetchMock = vi.spyOn(globalThis, "fetch");
});

afterEach(() => {
  fetchMock.mockRestore();
  vi.clearAllMocks();
});

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("/api/integrations/list", () => {
  it("401 when signed out", async () => {
    mockGetCurrentUser.mockResolvedValue(null);
    const { GET } = await import("@/app/api/integrations/list/route");
    const res = await callRoute(GET);
    expect(res.status).toBe(401);
  });

  it("combines providers + per-operator status into one payload", async () => {
    mockGetCurrentUser.mockResolvedValue({ id: "op-1", email: "x@y.com", name: null });
    fetchMock.mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : (input as Request).url;
      if (url.endsWith("/admin/connect/providers")) {
        return jsonResponse(200, {
          providers: [
            {
              pluginId: "open-neko-connector-google-workspace",
              pluginName: "@open-neko/connector-google-workspace",
              providerLabel: "Google Workspace",
              scopes: ["gmail.send"],
            },
            {
              pluginId: "open-neko-connector-github",
              pluginName: "@open-neko/connector-github",
              providerLabel: "GitHub",
              scopes: ["repo:read"],
            },
          ],
        });
      }
      if (url.includes("/admin/connect/status/op-1")) {
        return jsonResponse(200, {
          connected: [
            {
              pluginName: "@open-neko/connector-google-workspace",
              connectedAt: "2026-05-21T10:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    const { GET } = await import("@/app/api/integrations/list/route");
    const res = await callRoute(GET);
    expect(res.status).toBe(200);
    const body = res.body as {
      providers: Array<{ pluginName: string; connected: boolean; connectedAt: string | null }>;
    };
    expect(body.providers).toHaveLength(2);
    expect(body.providers.find((r) => r.pluginName === "@open-neko/connector-google-workspace")?.connected).toBe(
      true,
    );
    expect(body.providers.find((r) => r.pluginName === "@open-neko/connector-github")?.connected).toBe(
      false,
    );
  });
});

describe("/api/integrations/disconnect/[plugin]", () => {
  it("401 when signed out", async () => {
    mockGetCurrentUser.mockResolvedValue(null);
    const { POST } = await import(
      "@/app/api/integrations/disconnect/[plugin]/route"
    );
    const res = await callRoute(
      (req) =>
        POST(req, { params: Promise.resolve({ plugin: "%40open-neko%2Fx" }) }) as
          | Promise<Response>
          | Response,
      { method: "POST" },
    );
    expect(res.status).toBe(401);
  });

  it("proxies to worker and returns { removed }", async () => {
    mockGetCurrentUser.mockResolvedValue({ id: "op-1", email: "x@y.com", name: null });
    fetchMock.mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : (input as Request).url;
      if (url.endsWith("/admin/connect/disconnect")) {
        return jsonResponse(200, { removed: true });
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    const { POST } = await import(
      "@/app/api/integrations/disconnect/[plugin]/route"
    );
    const res = await callRoute(
      (req) =>
        POST(req, {
          params: Promise.resolve({ plugin: encodeURIComponent("@open-neko/x") }),
        }) as Promise<Response> | Response,
      { method: "POST" },
    );
    expect(res.status).toBe(200);
    expect((res.body as { removed: boolean }).removed).toBe(true);
  });
});

describe("/api/integrations/connect/[plugin]/start", () => {
  it("401 when signed out", async () => {
    mockGetCurrentUser.mockResolvedValue(null);
    const { GET } = await import(
      "@/app/api/integrations/connect/[plugin]/start/route"
    );
    const res = await callRoute(
      (req) =>
        GET(req, { params: Promise.resolve({ plugin: "%40x%2Fy" }) }) as
          | Promise<Response>
          | Response,
    );
    expect(res.status).toBe(401);
  });

  it("404 when plugin not installed", async () => {
    mockGetCurrentUser.mockResolvedValue({ id: "op-1", email: "x@y.com", name: null });
    fetchMock.mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : (input as Request).url;
      if (url.endsWith("/admin/connect/providers")) {
        return jsonResponse(200, { providers: [] });
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    const { GET } = await import(
      "@/app/api/integrations/connect/[plugin]/start/route"
    );
    const res = await callRoute(
      (req) =>
        GET(req, {
          params: Promise.resolve({ plugin: encodeURIComponent("@open-neko/missing") }),
        }) as Promise<Response> | Response,
    );
    expect(res.status).toBe(404);
  });

  // The 302-with-cookie path requires Next.js's request scope to set
  // cookies, which the synthetic callRoute() helper doesn't provide.
  // End-to-end coverage of the full /start → IdP → /callback dance
  // is verified manually against the real Google Workspace connector
  // (M6); these unit-level tests cover the pre-flight gates.
});

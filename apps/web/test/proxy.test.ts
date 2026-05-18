import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
import { createHmac } from "node:crypto";
import {
  _resetProviderCache,
  isAuthPluginInstalled,
  proxy,
  verifySessionCookie,
} from "../src/proxy";

const SECRET = "a".repeat(64);

function freshFetch(
  impl: (url: string) => Response | Promise<Response>,
): typeof fetch {
  return ((url: string) => Promise.resolve(impl(url))) as unknown as typeof fetch;
}

function mintCookie(opts: {
  userId?: string;
  expiresAt?: number;
  secret?: string;
} = {}): string {
  const userId = opts.userId ?? "usr_abc";
  const expiresAt =
    opts.expiresAt ?? Math.floor(Date.now() / 1000) + 60 * 60;
  const secret = opts.secret ?? SECRET;
  const body = `${userId}.${expiresAt}`;
  const mac = createHmac("sha256", secret).update(body).digest("base64url");
  return `${body}.${mac}`;
}

function request(url: string, cookie?: string): NextRequest {
  const headers = new Headers();
  if (cookie) headers.set("cookie", `openneko_session=${cookie}`);
  return new NextRequest(url, { headers });
}

beforeEach(() => {
  process.env.OPENNEKO_SESSION_SECRET = SECRET;
  _resetProviderCache();
});

afterEach(() => {
  delete process.env.OPENNEKO_SESSION_SECRET;
  _resetProviderCache();
  vi.restoreAllMocks();
});

describe("verifySessionCookie", () => {
  it("accepts a freshly minted cookie", () => {
    expect(verifySessionCookie(mintCookie())).toBe(true);
  });

  it("rejects an empty / undefined cookie", () => {
    expect(verifySessionCookie(undefined)).toBe(false);
    expect(verifySessionCookie("")).toBe(false);
  });

  it("rejects a cookie signed with a different secret", () => {
    const cookie = mintCookie({ secret: "b".repeat(64) });
    expect(verifySessionCookie(cookie)).toBe(false);
  });

  it("rejects an expired cookie", () => {
    const cookie = mintCookie({
      expiresAt: Math.floor(Date.now() / 1000) - 60,
    });
    expect(verifySessionCookie(cookie)).toBe(false);
  });

  it("rejects a malformed cookie", () => {
    expect(verifySessionCookie("only.two")).toBe(false);
    expect(verifySessionCookie("a.b.c.d")).toBe(false);
  });

  it("rejects when the session secret is missing or too short", () => {
    delete process.env.OPENNEKO_SESSION_SECRET;
    expect(verifySessionCookie(mintCookie())).toBe(false);
    process.env.OPENNEKO_SESSION_SECRET = "tooshort";
    expect(verifySessionCookie(mintCookie())).toBe(false);
  });
});

describe("isAuthPluginInstalled", () => {
  it("returns true when the worker reports a provider", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() =>
        Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } }),
      ),
    );
    expect(await isAuthPluginInstalled()).toBe(true);
  });

  it("returns false when the worker reports no provider", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() => Response.json({ provider: null })),
    );
    expect(await isAuthPluginInstalled()).toBe(false);
  });

  it("fails open (false) when the worker is unreachable", async () => {
    vi.stubGlobal(
      "fetch",
      (() => Promise.reject(new Error("ECONNREFUSED"))) as unknown as typeof fetch,
    );
    expect(await isAuthPluginInstalled()).toBe(false);
  });

  it("caches the result for the TTL window", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      freshFetch(() => {
        calls++;
        return Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } });
      }),
    );
    expect(await isAuthPluginInstalled()).toBe(true);
    expect(await isAuthPluginInstalled()).toBe(true);
    expect(await isAuthPluginInstalled()).toBe(true);
    expect(calls).toBe(1);
  });
});

describe("proxy", () => {
  it("lets every request through when no SSO plugin is installed", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() => Response.json({ provider: null })),
    );
    const res = await proxy(request("https://app.example.com/dashboard"));
    expect(res.status).toBe(200);
    // NextResponse.next() carries the x-middleware-next sentinel header.
    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  it("lets a signed-in user through when the SSO plugin is installed", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() =>
        Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } }),
      ),
    );
    const res = await proxy(
      request("https://app.example.com/dashboard", mintCookie()),
    );
    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  it("redirects to /signin with returnTo when no session cookie is present", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() =>
        Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } }),
      ),
    );
    const res = await proxy(
      request("https://app.example.com/dashboard?foo=bar"),
    );
    expect(res.status).toBe(302);
    const loc = res.headers.get("location") ?? "";
    expect(loc).toContain("/signin");
    expect(loc).toContain(
      `returnTo=${encodeURIComponent("/dashboard?foo=bar")}`,
    );
  });

  it("redirects when the session cookie has been tampered with", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() =>
        Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } }),
      ),
    );
    const tampered = mintCookie({ secret: "b".repeat(64) });
    const res = await proxy(
      request("https://app.example.com/dashboard", tampered),
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location") ?? "").toContain("/signin");
  });

  it("redirects when the session cookie has expired", async () => {
    vi.stubGlobal(
      "fetch",
      freshFetch(() =>
        Response.json({ provider: { pluginName: "@x/y", providerLabel: "X" } }),
      ),
    );
    const expired = mintCookie({
      expiresAt: Math.floor(Date.now() / 1000) - 60,
    });
    const res = await proxy(
      request("https://app.example.com/dashboard", expired),
    );
    expect(res.status).toBe(302);
  });
});

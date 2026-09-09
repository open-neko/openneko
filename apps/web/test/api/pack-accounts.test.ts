import { beforeEach, expect, it, vi } from "vitest";
import { NextRequest, NextResponse } from "next/server";
const mocks = vi.hoisted(() => ({ user: vi.fn(), admin: vi.fn(), worker: vi.fn() }));
vi.mock("@/lib/auth", () => ({ getCurrentUser: mocks.user }));
vi.mock("@/lib/admin-auth", () => ({ requireAdminActor: mocks.admin, isDenied: (value: unknown) => value instanceof Response }));
vi.mock("@/lib/solution-packs", async load => ({ ...await load<object>(), requestPackWorker: mocks.worker }));
import { GET, POST } from "@/app/api/pack-accounts/[packId]/[connectorId]/[action]/route";
const origin = "https://openneko.example";
const ctx = (action: string) => ({ params: Promise.resolve({ packId: "fixture", connectorId: "accounts", action }) });
function request(action: string, body: unknown = {}, headers: Record<string, string> = {}) {
  return new NextRequest(`${origin}/api/pack-accounts/fixture/accounts/${action}`, { method: "POST", headers: { origin, "Content-Type": "application/json", ...headers }, body: JSON.stringify(body) });
}
beforeEach(() => { vi.clearAllMocks(); mocks.user.mockResolvedValue({ id: "one" }); mocks.admin.mockResolvedValue({ userId: "one", role: "admin" }); mocks.worker.mockResolvedValue({ status: 200, body: {} }); });
it("takes account ownership from the session and binds start to a private browser cookie", async () => {
  mocks.worker.mockResolvedValue({ status: 200, body: { authorizationUrl: "https://provider.example/authorize", state: "private-state" } });
  const response = await POST(request("start", { owner: "other", redirectUri: "https://evil.example" }), ctx("start"));
  const input = JSON.parse(mocks.worker.mock.calls[0][1].body);
  expect(input.owner).toBe("user:one");
  expect(input.input.redirectUri).toBe(`${origin}/api/pack-accounts/fixture/accounts/callback`);
  expect(await response.json()).not.toHaveProperty("state");
  expect(response.headers.get("set-cookie")).toMatch(/HttpOnly/);
  expect(response.headers.get("set-cookie")).toMatch(/Secure/);
  expect(response.headers.get("set-cookie")).toMatch(/SameSite=lax/);
});
it("rejects cross-origin requests and client settings from non-admin users", async () => {
  expect((await POST(request("start", {}, { origin: "https://evil.example" }), ctx("start"))).status).toBe(403);
  mocks.admin.mockResolvedValue(NextResponse.json({}, { status: 403 }));
  expect((await POST(request("configure"), ctx("configure"))).status).toBe(403);
  expect(mocks.worker).not.toHaveBeenCalled();
});
it("rejects signed-out SSO users but keeps the solo owner separate", async () => {
  mocks.user.mockResolvedValue(null); mocks.admin.mockResolvedValue(NextResponse.json({}, { status: 403 }));
  expect((await GET(new NextRequest(`${origin}/api/pack-accounts/fixture/accounts/list`), ctx("list"))).status).toBe(403);
  mocks.admin.mockResolvedValue({ userId: null, role: "admin" });
  await GET(new NextRequest(`${origin}/api/pack-accounts/fixture/accounts/list`), ctx("list"));
  expect(JSON.parse(mocks.worker.mock.calls[0][1].body).owner).toBe("solo");
});
it("requires the browser state on callback and clears it after completion", async () => {
  const url = `${origin}/api/pack-accounts/fixture/accounts/callback?state=state&code=private-code`;
  const failed = await GET(new NextRequest(url), ctx("callback"));
  expect(failed.headers.get("location")).toContain("connectionError=1"); expect(mocks.worker).not.toHaveBeenCalled();
  const response = await GET(new NextRequest(url, { headers: { cookie: "pack-connect-fixture-accounts=state" } }), ctx("callback"));
  expect(response.headers.get("location")).toBe(`${origin}/integrations/packs?connected=1`);
  expect(response.headers.get("set-cookie")).toContain("Max-Age=0");
  expect(JSON.parse(mocks.worker.mock.calls[0][1].body).input.code).toBe("private-code");
});
it("does not expose credential resolution or raw provider errors", async () => {
  expect((await POST(request("credential"), ctx("credential"))).status).toBe(404);
  mocks.worker.mockRejectedValue(new Error("private-token"));
  const response = await POST(request("disconnect", { accountId: "id" }), ctx("disconnect"));
  expect(await response.text()).not.toContain("private-token");
});

import { beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
const mocks = vi.hoisted(() => ({ holds: vi.fn(async () => true), actor: vi.fn(), begin: vi.fn(), complete: vi.fn(), disconnect: vi.fn(), write: vi.fn(), read: vi.fn(), admin: vi.fn(), worker: vi.fn() }));
vi.mock("server-only", () => ({}));
vi.mock("@/lib/personal-pack-accounts", () => ({ personalPackActor: mocks.actor }));
vi.mock("@neko/llm/graphjin/pack-user-connections", () => ({ beginPackUserConnection: mocks.begin, completePackUserConnection: mocks.complete, disconnectPackUserConnection: mocks.disconnect }));
vi.mock("@/lib/pack-oauth", () => ({ writePackOAuthState: mocks.write, readAndClearPackOAuthState: mocks.read, packOAuthCallbackUri: () => "https://app.test/api/pack-accounts/fixture/account/callback" }));
vi.mock("@/lib/integrations", () => ({ newStateToken: () => "state", newPkceVerifier: () => "verifier", pkceChallenge: () => "challenge" }));
vi.mock("@/lib/admin-auth", () => ({ requireAdminActor: mocks.admin, isDenied: (value: unknown) => value instanceof Response }));
vi.mock("@/lib/solution-packs", () => ({ validPackId: () => true, requestPackWorker: mocks.worker }));
vi.mock("@neko/db", () => ({ getOrgId: async () => "org" }));
vi.mock("@/lib/entitlements", () => ({ holdsItem: mocks.holds }));
import { GET } from "../src/app/api/v1/pack-accounts/[packId]/[connectionKey]/callback/route";
const params = { params: Promise.resolve({ packId: "fixture", connectionKey: "account" }) };
const actor = { userId: "alice", orgId: "org" };
beforeEach(() => { vi.clearAllMocks(); mocks.actor.mockResolvedValue(actor); mocks.begin.mockResolvedValue({ authorizationUrl: "https://provider.test" }); mocks.complete.mockResolvedValue(undefined); mocks.read.mockResolvedValue({ packId: "fixture", connectionKey: "account", personal: true, userId: "alice", orgId: "org", state: "state", codeVerifier: "verifier", returnPath: "/integrations" }); });
describe("personal account callback", () => {
  it("rejects callback session switching without exchanging the code", async () => {
    mocks.actor.mockResolvedValue({ ...actor, userId: "bob" });
    const response = await GET(new NextRequest("https://app.test/api/pack-accounts/fixture/account/callback?state=state&code=code"), params);
    expect(response.headers.get("location")).toContain("error=");
    expect(mocks.complete).not.toHaveBeenCalled();
  });
  it("completes only matching personal state and never uses the admin flow", async () => {
    const response = await GET(new NextRequest("https://app.test/api/pack-accounts/fixture/account/callback?state=state&code=code"), params);
    expect(response.headers.get("location")).toBe("https://app.test/integrations?connected=account");
    expect(mocks.complete).toHaveBeenCalledWith(actor, "fixture", "account", expect.objectContaining({ state: "state", code: "code", codeVerifier: "verifier" }));
    expect(mocks.worker).not.toHaveBeenCalled(); expect(mocks.admin).not.toHaveBeenCalled();
  });
});

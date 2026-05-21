/**
 * /api/settings/install-policy contract tests.
 *
 * Covers: signed-out → 401, non-admin → 403, admin → 200 read + write,
 * partial PATCH semantics, validation error on http: marketplace URL,
 * official marketplace is always preserved.
 */

import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { and, app_user, db, eq, pool } from "@neko/db";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId, mockGetCurrentUser } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
  mockGetCurrentUser: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

vi.mock("@/lib/auth", async () => {
  const actual = await vi.importActual<typeof import("@/lib/auth")>("@/lib/auth");
  return { ...actual, getCurrentUser: mockGetCurrentUser };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/settings/install-policy] skipping: Postgres unreachable.");
}

const OFFICIAL = "https://open-neko.github.io/plugins/marketplace.json";

describeIfDb("/api/settings/install-policy", () => {
  let orgId: string;
  let GET: typeof import("@/app/api/settings/install-policy/route").GET;
  let PATCH: typeof import("@/app/api/settings/install-policy/route").PATCH;

  beforeAll(async () => {
    const mod = await import("@/app/api/settings/install-policy/route");
    GET = mod.GET;
    PATCH = mod.PATCH;
  });

  async function seedUser(role: "admin" | "member"): Promise<string> {
    const id = `user-${role}-${Math.random().toString(36).slice(2, 8)}`;
    await db().insert(app_user).values({
      id,
      email: `${id}@example.com`,
      name: `${role}`,
      org_id: orgId,
      role,
    });
    return id;
  }

  beforeEach(async () => {
    orgId = uniqueOrgId("api-install-policy");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("GET returns 401 when signed out", async () => {
    mockGetCurrentUser.mockResolvedValue(null);
    const res = await callRoute(GET);
    expect(res.status).toBe(401);
  });

  it("GET returns DEFAULT_POLICY when signed in but no row exists", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    const res = await callRoute(GET);
    expect(res.status).toBe(200);
    const body = res.body as {
      source: "default" | "org";
      policy: {
        allowUnverified: boolean;
        allowGitUrlInstalls: boolean;
        allowedMarketplaces: string[];
        allowSandboxedSkillEscape: boolean;
      };
    };
    expect(body.source).toBe("default");
    expect(body.policy.allowUnverified).toBe(false);
    expect(body.policy.allowGitUrlInstalls).toBe(false);
    expect(body.policy.allowSandboxedSkillEscape).toBe(false);
    expect(body.policy.allowedMarketplaces).toEqual([OFFICIAL]);
  });

  it("PATCH returns 401 when signed out", async () => {
    mockGetCurrentUser.mockResolvedValue(null);
    const res = await callRoute(PATCH, {
      method: "PATCH",
      body: { allowUnverified: true },
    });
    expect(res.status).toBe(401);
  });

  // OpenNeko has no admin/member role separation today — any signed-
  // in operator can change install policy, matching the rest of
  // /settings. When a real role gate ships, restore an "only admin
  // PATCHes" test here.
  it("PATCH as any signed-in operator persists the policy", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    const res = await callRoute(PATCH, {
      method: "PATCH",
      body: { allowUnverified: true, allowGitUrlInstalls: true },
    });
    expect(res.status).toBe(200);
    const body = res.body as { policy: { allowUnverified: boolean; allowGitUrlInstalls: boolean } };
    expect(body.policy.allowUnverified).toBe(true);
    expect(body.policy.allowGitUrlInstalls).toBe(true);

    // GET back should reflect the new state.
    const readBack = await callRoute(GET);
    const readBody = readBack.body as { policy: { allowUnverified: boolean } };
    expect(readBody.policy.allowUnverified).toBe(true);
  });

  it("PATCH is partial — omitted keys don't clobber existing values", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    await callRoute(PATCH, { method: "PATCH", body: { allowUnverified: true } });
    await callRoute(PATCH, { method: "PATCH", body: { allowSandboxedSkillEscape: true } });
    const res = await callRoute(GET);
    const body = res.body as {
      policy: { allowUnverified: boolean; allowSandboxedSkillEscape: boolean };
    };
    expect(body.policy.allowUnverified).toBe(true);
    expect(body.policy.allowSandboxedSkillEscape).toBe(true);
  });

  it("PATCH rejects http: marketplace URLs with 400", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    const res = await callRoute(PATCH, {
      method: "PATCH",
      body: { allowedMarketplaces: ["http://example.com/m.json"] },
    });
    expect(res.status).toBe(400);
    expect((res.body as { error: string }).error).toMatch(/https/);
  });

  it("PATCH adding a community marketplace preserves the official one", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    const community = "https://example.com/marketplace.json";
    const res = await callRoute(PATCH, {
      method: "PATCH",
      body: { allowedMarketplaces: [community] },
    });
    expect(res.status).toBe(200);
    const body = res.body as { policy: { allowedMarketplaces: string[] } };
    expect(body.policy.allowedMarketplaces).toContain(OFFICIAL);
    expect(body.policy.allowedMarketplaces).toContain(community);
  });

  it("PATCH ignores non-boolean values for boolean fields", async () => {
    const userId = await seedUser("member");
    mockGetCurrentUser.mockResolvedValue({ id: userId, email: "x@y.com", name: null });
    await callRoute(PATCH, {
      method: "PATCH",
      body: { allowUnverified: "yes" as unknown as boolean },
    });
    const res = await callRoute(GET);
    const body = res.body as { policy: { allowUnverified: boolean } };
    // Stays at default false because string was rejected at the API layer.
    expect(body.policy.allowUnverified).toBe(false);
  });
});

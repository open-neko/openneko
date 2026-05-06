/**
 * /settings/finish contract tests. Asserts the endpoint refuses to flip
 * setup_complete_at when prerequisites aren't met.
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
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedDataSource,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { db, eq, organization, pool } from "@neko/db";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[api/setup-finish] skipping: Postgres unreachable.");
}

async function readSetupCompleteAt(orgId: string): Promise<Date | null> {
  const rows = await db()
    .select({ ts: organization.setup_complete_at })
    .from(organization)
    .where(eq(organization.id, orgId));
  return rows[0]?.ts ?? null;
}

describeIfDb("/settings/finish", () => {
  let orgId: string;
  let POST: typeof import("@/app/settings/finish/route").POST;

  beforeAll(async () => {
    const mod = await import("@/app/settings/finish/route");
    POST = mod.POST;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("api-finish");
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

  it("rejects when no data source is configured", async () => {
    const res = await callRoute(POST, { method: "POST" });
    expect(res.status).toBe(400);
    expect((res.body as { error: string }).error).toMatch(/Data source/);
    expect(await readSetupCompleteAt(orgId)).toBeNull();
  });

  it("rejects when data source exists but no primary provider", async () => {
    await seedDataSource(orgId);
    const res = await callRoute(POST, { method: "POST" });
    expect(res.status).toBe(400);
    expect((res.body as { error: string }).error).toMatch(/provider/i);
    expect(await readSetupCompleteAt(orgId)).toBeNull();
  });

  it("flips setup_complete_at when both prerequisites are met", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: { apiKey: "sk-ant" },
    });
    const res = await callRoute(POST, { method: "POST" });
    expect(res.status).toBe(200);
    expect((res.body as { ok: boolean }).ok).toBe(true);

    const ts = await readSetupCompleteAt(orgId);
    expect(ts).toBeInstanceOf(Date);
  });
});

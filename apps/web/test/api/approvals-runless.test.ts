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
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { action_request, db, pool } from "@neko/db";
import { callRoute } from "../_helpers/route";

const { mockGetOrgId, mockGetCurrentActor } = vi.hoisted(() => ({
  mockGetOrgId: vi.fn(),
  mockGetCurrentActor: vi.fn(),
}));

vi.mock("@/lib/db", async () => {
  const actual = await vi.importActual<typeof import("@/lib/db")>("@/lib/db");
  return { ...actual, getOrgId: mockGetOrgId };
});

vi.mock("@/lib/actor", () => ({
  getCurrentActor: mockGetCurrentActor,
}));

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[api/approvals-runless] skipping: Postgres unreachable.",
  );
}

describeIfDb("run-less approvals", () => {
  let orgId: string;
  let approvalsGET: typeof import("@/app/api/v1/approvals/route").GET;

  beforeAll(async () => {
    approvalsGET = (await import("@/app/api/v1/approvals/route")).GET;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("runless-approval");
    await createTestOrg(orgId);
    mockGetOrgId.mockResolvedValue(orgId);
    mockGetCurrentActor.mockResolvedValue({ userId: "admin-1", role: "admin" });
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  it("appears in the approvals endpoint with a nullable workflow contract", async () => {
    const [inserted] = await db()
      .insert(action_request)
      .values({
        org_id: orgId,
        scope: "admin.apps",
        kind: "user_admin",
        payload: { email: "new@example.com" },
        risk_level: "medium",
        status: "pending_approval",
        summary: null,
      })
      .returning({ id: action_request.id });

    const approvals = await callRoute(approvalsGET, {
      url: "http://localhost/api/approvals?filter=awaiting",
    });
    expect(approvals.status).toBe(200);
    expect(approvals.body).toMatchObject({
      count: 1,
      actions: [
        {
          id: inserted!.id,
          workflowRunId: null,
          workflow: null,
          summary: null,
        },
      ],
    });
  });

  it("applies the source-config visibility rule", async () => {
    mockGetCurrentActor.mockResolvedValue({ userId: "member-1", role: "member" });
    await db().insert(action_request).values([
      {
        org_id: orgId,
        scope: "admin.sources",
        kind: "source_config_admin",
        status: "pending_approval",
      },
      {
        org_id: orgId,
        scope: "admin.apps",
        kind: "user_admin",
        status: "pending_approval",
      },
    ]);

    const approvals = await callRoute(approvalsGET, {
      url: "http://localhost/api/approvals?filter=awaiting",
    });

    expect(
      (approvals.body as { actions: Array<{ kind: string }> }).actions.map(
        (action) => action.kind,
      ),
    ).toEqual(["user_admin"]);
  });
});

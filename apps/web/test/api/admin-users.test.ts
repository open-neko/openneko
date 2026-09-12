import { beforeEach, describe, expect, it, vi } from "vitest";

type Row = Record<string, unknown>;

const mocks = vi.hoisted(() => ({
  selectResults: [] as Row[][],
  inserted: [] as Row[],
  updates: [] as Row[],
  actorId: "admin-1" as string | null,
}));

vi.mock("@/lib/admin-auth", () => ({
  requireAdminActor: async () => ({ userId: mocks.actorId, role: "admin" }),
  isDenied: () => false,
}));

vi.mock("@/lib/db", () => ({
  getOrgId: async () => "org-1",
}));

vi.mock("@neko/db", () => {
  const chain = () => ({
    from: () => ({
      where: () => ({
        limit: async () => mocks.selectResults.shift() ?? [],
      }),
    }),
  });
  return {
    organization: { solo_admin_user_id: "owner" },
    isUnclaimedSoloEmail: (email: string) => email.endsWith("@solo.openneko.invalid"),
    app_user: {
      id: "id",
      org_id: "org_id",
      email: "email",
      role: "role",
      disabled_at: "disabled_at",
      $inferInsert: {},
    },
    db: () => ({
      transaction: async (fn: (tx: unknown) => Promise<unknown>) => fn({
        execute: async () => {}, select: chain,
        update: () => ({ set: (value: Row) => ({ where: async () => { mocks.updates.push(value); } }) }),
        insert: () => ({ values: async (values: Row) => { mocks.inserted.push(values); } }),
      }),
      select: chain,
      insert: () => ({
        values: async (values: Row) => {
          mocks.inserted.push(values);
        },
      }),
      update: () => ({
        set: (patch: Row) => ({
          where: async () => {
            mocks.updates.push(patch);
          },
        }),
      }),
    }),
    and: (...args: unknown[]) => args,
    eq: (...args: unknown[]) => args,
    ne: (...args: unknown[]) => args,
    isNull: (...args: unknown[]) => args,
    sql: () => "sql",
  };
});

import { POST } from "@/app/api/admin/users/route";
import { PATCH } from "@/app/api/admin/users/[userId]/route";

function postRequest(body: unknown): Request {
  return new Request("http://localhost/api/admin/users", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

function patchRequest(body: unknown): Request {
  return new Request("http://localhost/api/admin/users/usr_target", {
    method: "PATCH",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

const targetParams = { params: Promise.resolve({ userId: "usr_target" }) };

describe("POST /api/admin/users", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.selectResults = [];
    mocks.inserted = [];
    mocks.updates = [];
    mocks.actorId = "admin-1";
  });

  it("adds the solo owner's email in place without creating another account", async () => {
    mocks.selectResults = [[], [{ owner: "admin-1" }], [{ email: "local@solo.openneko.invalid", sub: null }]];
    const res = await POST(postRequest({ email: "owner@example.com", role: "admin", updateSoloAccount: true }) as never);
    expect(res.status).toBe(200);
    expect((await res.json()).user.id).toBe("admin-1");
    expect(mocks.updates[0]).toMatchObject({ email: "owner@example.com" });
    expect(mocks.inserted).toEqual([]);
    mocks.selectResults = [[], [{ owner: "someone-else" }], [{ email: "local@solo.openneko.invalid", sub: null }]];
    expect((await POST(postRequest({ email: "another@example.com", role: "admin", updateSoloAccount: true }) as never)).status).toBe(409);
  });

  it("provisions a user with a lowercased email and no sub", async () => {
    mocks.selectResults = [[]];
    const res = await POST(
      postRequest({ email: "New.Person@Company.COM", role: "member" }) as never,
    );
    expect(res.status).toBe(201);
    expect(mocks.inserted).toHaveLength(1);
    expect(mocks.inserted[0]).toMatchObject({
      email: "new.person@company.com",
      role: "member",
      sub: null,
      org_id: "org-1",
    });
    expect(String(mocks.inserted[0].id)).toMatch(/^usr_/);
  });

  it("rejects duplicates with 409", async () => {
    mocks.selectResults = [[{ id: "usr_existing" }]];
    const res = await POST(
      postRequest({ email: "dupe@company.com", role: "member" }) as never,
    );
    expect(res.status).toBe(409);
    expect(mocks.inserted).toHaveLength(0);
  });

  it("rejects invalid emails and roles", async () => {
    expect(
      (await POST(postRequest({ email: "not-an-email", role: "member" }) as never))
        .status,
    ).toBe(400);
    expect(
      (await POST(postRequest({ email: "a@b.co", role: "owner" }) as never))
        .status,
    ).toBe(400);
    expect(mocks.inserted).toHaveLength(0);
  });
});

describe("PATCH /api/admin/users/[userId]", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.selectResults = [];
    mocks.inserted = [];
    mocks.updates = [];
    mocks.actorId = "admin-1";
  });

  it("refuses to demote the last active admin", async () => {
    mocks.selectResults = [
      [{ id: "usr_target", role: "admin", disabledAt: null }],
      [], // no other active admin
    ];
    const res = await PATCH(
      patchRequest({ role: "member" }) as never,
      targetParams as never,
    );
    expect(res.status).toBe(409);
    expect(mocks.updates).toHaveLength(0);
  });

  it("refuses to disable the last active admin", async () => {
    mocks.selectResults = [
      [{ id: "usr_target", role: "admin", disabledAt: null }],
      [],
    ];
    const res = await PATCH(
      patchRequest({ disabled: true }) as never,
      targetParams as never,
    );
    expect(res.status).toBe(409);
    expect(mocks.updates).toHaveLength(0);
  });

  it("demotes an admin when another active admin remains", async () => {
    mocks.selectResults = [
      [{ id: "usr_target", role: "admin", disabledAt: null }],
      [{ id: "usr_other_admin" }],
    ];
    const res = await PATCH(
      patchRequest({ role: "member" }) as never,
      targetParams as never,
    );
    expect(res.status).toBe(200);
    expect(mocks.updates).toHaveLength(1);
    expect(mocks.updates[0]).toMatchObject({ role: "member" });
  });

  it("disables and re-enables a member without consulting the admin count", async () => {
    mocks.selectResults = [
      [{ id: "usr_target", role: "member", disabledAt: null }],
    ];
    const res = await PATCH(
      patchRequest({ disabled: true }) as never,
      targetParams as never,
    );
    expect(res.status).toBe(200);
    expect(mocks.updates[0].disabled_at).toBeInstanceOf(Date);

    mocks.selectResults = [
      [{ id: "usr_target", role: "member", disabledAt: new Date() }],
    ];
    const res2 = await PATCH(
      patchRequest({ disabled: false }) as never,
      targetParams as never,
    );
    expect(res2.status).toBe(200);
    expect(mocks.updates[1].disabled_at).toBeNull();
  });

  it("404s on an unknown user and 400s on an empty patch", async () => {
    mocks.selectResults = [[]];
    const res = await PATCH(
      patchRequest({ role: "member" }) as never,
      targetParams as never,
    );
    expect(res.status).toBe(404);

    const res2 = await PATCH(
      patchRequest({}) as never,
      targetParams as never,
    );
    expect(res2.status).toBe(400);
  });
});

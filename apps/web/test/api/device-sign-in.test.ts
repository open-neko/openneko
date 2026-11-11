import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";
import { and, app_user, audit_chain, db, eq } from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";

const state = vi.hoisted(() => ({
  orgId: "",
  cookie: undefined as string | undefined,
  authorization: undefined as string | undefined,
  admin: null as { userId: string; role: "admin" } | null,
}));

vi.mock("@/lib/db", () => ({ getOrgId: async () => state.orgId }));
vi.mock("next/headers", () => ({
  cookies: async () => ({
    get: (name: string) => (name === "openneko_session" && state.cookie ? { value: state.cookie } : undefined),
    set: () => undefined,
    delete: () => {
      state.cookie = undefined;
    },
  }),
  headers: async () => new Headers(state.authorization ? { authorization: state.authorization } : {}),
}));
vi.mock("@/lib/admin-auth", () => ({
  requireAdminActor: async () =>
    state.admin ?? Response.json({ error: "admin only" }, { status: 403 }),
  isDenied: (value: unknown) => value instanceof Response,
}));

const { encodeSession, getCurrentUser, _resetAuthProviderCache } = await import("@/lib/auth");
const code = await import("@/app/api/v1/auth/device/code/route");
const token = await import("@/app/api/v1/auth/device/token/route");
const approve = await import("@/app/api/v1/auth/device/approve/route");
const logout = await import("@/app/api/v1/auth/logout/route");
const adminList = await import("@/app/api/v1/admin/devices/route");
const adminRevoke = await import("@/app/api/v1/admin/devices/[deviceId]/route");

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

function post(path: string, body: unknown): NextRequest {
  return new NextRequest(`https://openneko.example.com/api/v1${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

function signedInAs(userId: string) {
  state.authorization = undefined;
  state.cookie = encodeSession({ userId, expiresAt: Math.floor(Date.now() / 1000) + 600, email: "", name: null });
}

function asDevice(value: string) {
  state.cookie = undefined;
  state.authorization = `Bearer ${value}`;
}

async function signIn(name: string, userId: string): Promise<{ token: string; deviceId: string }> {
  state.cookie = undefined;
  state.authorization = undefined;
  const started = (await (await code.POST(post("/auth/device/code", { name }))).json()) as {
    deviceCode: string;
    userCode: string;
    verificationUriComplete: string;
    interval: number;
  };
  expect(started.verificationUriComplete).toBe(`https://openneko.example.com/device?code=${started.userCode}`);
  const pending = await token.POST(post("/auth/device/token", { deviceCode: started.deviceCode }));
  expect(pending.status).toBe(400);
  expect(await pending.json()).toEqual({ error: "authorization_pending" });

  signedInAs(userId);
  const approved = await approve.POST(post("/auth/device/approve", { userCode: started.userCode, approve: true }));
  expect(approved.status).toBe(200);
  state.cookie = undefined;

  await db().execute(
    (await import("@neko/db")).sql`update app_device_request set last_polled_at = null where org_id = ${state.orgId}`,
  );
  const issued = await token.POST(post("/auth/device/token", { deviceCode: started.deviceCode }));
  expect(issued.status).toBe(200);
  expect(issued.headers.get("cache-control")).toBe("no-store");
  return (await issued.json()) as { token: string; deviceId: string };
}

let worker: Server;
let member = "";
let admin = "";

describeIfDb("device sign-in", () => {
  beforeAll(async () => {
    worker = createServer((_req, res) => {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ provider: { pluginName: "magic-link", providerLabel: "Email link" }, pending: null }));
    });
    await new Promise<void>((resolve) => worker.listen(0, "127.0.0.1", resolve));
    process.env.WORKER_ADMIN_URL = `http://127.0.0.1:${(worker.address() as AddressInfo).port}`;
    process.env.OPENNEKO_SESSION_SECRET = "s".repeat(64);
    state.orgId = uniqueOrgId("device-web");
    await createTestOrg(state.orgId);
    member = `${state.orgId}-member`;
    admin = `${state.orgId}-admin`;
    await db().insert(app_user).values([
      { id: member, org_id: state.orgId, email: "member@example.test" },
      { id: admin, org_id: state.orgId, email: "admin@example.test" },
    ]);
  });

  afterAll(async () => {
    await deleteTestOrg(state.orgId);
    await new Promise((resolve) => worker.close(resolve));
    delete process.env.WORKER_ADMIN_URL;
    delete process.env.OPENNEKO_SESSION_SECRET;
  });

  beforeEach(() => {
    _resetAuthProviderCache();
    state.cookie = undefined;
    state.authorization = undefined;
    state.admin = null;
  });

  it("signs the installed app in through a browser approval and lets the token act as the user", async () => {
    const issued = await signIn("Pixel 9", member);
    asDevice(issued.token);
    expect(await getCurrentUser()).toMatchObject({ id: member, email: "member@example.test" });

    asDevice(`${issued.token.slice(0, 20)}${"x".repeat(issued.token.length - 20)}`);
    expect(await getCurrentUser()).toBeNull();

    const events = await db()
      .select({ event: audit_chain.event, payload: audit_chain.payload })
      .from(audit_chain)
      .where(and(eq(audit_chain.org_id, state.orgId), eq(audit_chain.entity_kind, "device_request")));
    expect(events).toEqual([{ event: "approved", payload: { userId: member, deviceName: "Pixel 9" } }]);
  });

  it("refuses an approval without a browser session or from a device token", async () => {
    const issued = await signIn("Tablet", member);
    const started = (await (await code.POST(post("/auth/device/code", { name: "Other" }))).json()) as {
      userCode: string;
    };
    state.cookie = undefined;
    expect((await approve.POST(post("/auth/device/approve", { userCode: started.userCode, approve: true }))).status).toBe(401);
    asDevice(issued.token);
    expect((await approve.POST(post("/auth/device/approve", { userCode: started.userCode, approve: true }))).status).toBe(403);
    signedInAs(member);
    expect((await approve.POST(post("/auth/device/approve", { userCode: started.userCode }))).status).toBe(400);
    expect((await approve.POST(post("/auth/device/approve", { userCode: "BBBB-BBBB", approve: true }))).status).toBe(404);
  });

  it("lets an admin list and revoke a device, and the token stops at once", async () => {
    const issued = await signIn("Work phone", member);
    expect((await adminList.GET()).status).toBe(403);

    state.admin = { userId: admin, role: "admin" };
    const listed = (await (await adminList.GET()).json()) as { devices: Array<{ id: string; name: string; status: string; tokenPrefix: string }> };
    const device = listed.devices.find((d) => d.id === issued.deviceId);
    expect(device).toMatchObject({ name: "Work phone", status: "active" });
    expect(JSON.stringify(listed)).not.toContain(issued.token);

    const context = { params: Promise.resolve({ deviceId: issued.deviceId }) };
    expect((await adminRevoke.DELETE(new Request("https://x"), context)).status).toBe(200);
    expect((await adminRevoke.DELETE(new Request("https://x"), context)).status).toBe(404);
    asDevice(issued.token);
    expect(await getCurrentUser()).toBeNull();

    const [revoked] = await db()
      .select({ event: audit_chain.event, payload: audit_chain.payload })
      .from(audit_chain)
      .where(and(eq(audit_chain.org_id, state.orgId), eq(audit_chain.entity_id, issued.deviceId)));
    expect(revoked).toEqual({
      event: "revoked",
      payload: { byUserId: admin, userId: member, deviceName: "Work phone" },
    });
  });

  it("revokes the calling device on sign-out and keeps other devices", async () => {
    const phone = await signIn("Phone", member);
    const laptop = await signIn("Laptop", member);
    asDevice(phone.token);
    expect((await logout.POST()).status).toBe(200);
    expect(await getCurrentUser()).toBeNull();
    asDevice(laptop.token);
    expect(await getCurrentUser()).toMatchObject({ id: member });
  });

  it("ends every device of a disabled user", async () => {
    const issued = await signIn("Old phone", admin);
    await db().update(app_user).set({ disabled_at: new Date() }).where(eq(app_user.id, admin));
    asDevice(issued.token);
    expect(await getCurrentUser()).toBeNull();
    await db().update(app_user).set({ disabled_at: null }).where(eq(app_user.id, admin));
  });
});

import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { app_user, db, eq } from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";

const state = vi.hoisted(() => ({
  orgId: "",
  cookie: undefined as string | undefined,
  authorization: undefined as string | undefined,
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

const { getCurrentUser, _resetAuthProviderCache } = await import("@/lib/auth");
const { decideDeviceRequest, redeemDeviceRequest, sql, startDeviceRequest } = await import("@neko/db");

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

function asDevice(value: string) {
  state.cookie = undefined;
  state.authorization = `Bearer ${value}`;
}

async function signIn(name: string, userId: string): Promise<{ token: string; deviceId: string }> {
  const started = await startDeviceRequest(state.orgId, name);
  expect(await decideDeviceRequest({ orgId: state.orgId, userCode: started.userCode, userId, approve: true })).not.toBeNull();
  await db().execute(sql`update app_device_request set last_polled_at = null where org_id = ${state.orgId}`);
  const issued = await redeemDeviceRequest(state.orgId, started.deviceCode);
  if (issued.status !== "issued") throw new Error(`device request ${issued.status}`);
  return { token: issued.token, deviceId: issued.deviceId };
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
  });

  it("lets a device token act as its user until a forged token fails", async () => {
    const issued = await signIn("Pixel 9", member);
    asDevice(issued.token);
    expect(await getCurrentUser()).toMatchObject({ id: member, email: "member@example.test" });

    asDevice(`${issued.token.slice(0, 20)}${"x".repeat(issued.token.length - 20)}`);
    expect(await getCurrentUser()).toBeNull();
  });

  it("stops a revoked device token at once", async () => {
    const issued = await signIn("Work phone", member);
    await db().execute(sql`update app_device set revoked_at = now(), revoked_by_user_id = ${admin} where id = ${issued.deviceId}`);
    asDevice(issued.token);
    expect(await getCurrentUser()).toBeNull();
  });

  it("ends every device of a disabled user", async () => {
    const issued = await signIn("Old phone", admin);
    await db().update(app_user).set({ disabled_at: new Date() }).where(eq(app_user.id, admin));
    asDevice(issued.token);
    expect(await getCurrentUser()).toBeNull();
    await db().update(app_user).set({ disabled_at: null }).where(eq(app_user.id, admin));
  });
});

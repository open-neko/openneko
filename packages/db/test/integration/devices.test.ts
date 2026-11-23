import { expect, it } from "vitest";
import {
  app_device,
  app_device_request,
  app_user,
  db,
  decideDeviceRequest,
  eq,
  findDeviceRequest,
  formatUserCode,
  listDevices,
  redeemDeviceRequest,
  sql,
  startDeviceRequest,
  verifyDeviceToken,
} from "../../src";
import { dbReachable, withTestOrg } from "./_helpers";

const reachable = await dbReachable();

async function user(orgId: string, label: string): Promise<string> {
  const id = `${orgId}-${label}`;
  await db().insert(app_user).values({ id, org_id: orgId, email: `${label}@example.test` });
  return id;
}

async function backdatePoll(orgId: string) {
  await db().execute(sql`update app_device_request set last_polled_at = now() - interval '1 minute' where org_id = ${orgId}`);
}

it.skipIf(!reachable)("issues a token once after approval and stores only digests", async () => {
  await withTestOrg(async (orgId) => {
    const owner = await user(orgId, "owner");
    const started = await startDeviceRequest(orgId, "  Amit's   phone ");
    expect(started.userCode).toMatch(/^[BCDFGHJKLMNPQRSTVWXZ]{8}$/);
    expect(await redeemDeviceRequest(orgId, started.deviceCode)).toEqual({ status: "pending" });
    expect(await redeemDeviceRequest(orgId, started.deviceCode)).toEqual({ status: "slow_down" });

    const shown = await findDeviceRequest(orgId, formatUserCode(started.userCode).toLowerCase());
    expect(shown?.name).toBe("Amit's phone");
    expect(await decideDeviceRequest({ orgId, userCode: started.userCode, userId: owner, approve: true })).not.toBeNull();
    expect(await decideDeviceRequest({ orgId, userCode: started.userCode, userId: owner, approve: false })).toBeNull();

    await backdatePoll(orgId);
    const issued = await redeemDeviceRequest(orgId, started.deviceCode);
    if (issued.status !== "issued") throw new Error(`expected a token, got ${issued.status}`);
    expect(issued.token).toMatch(/^onk_dev_[0-9a-f]{12}_[A-Za-z0-9_-]{43}$/);
    expect(issued.userId).toBe(owner);
    expect(await redeemDeviceRequest(orgId, started.deviceCode)).toEqual({ status: "expired" });

    const [stored] = await db().select().from(app_device).where(eq(app_device.id, issued.deviceId));
    expect(JSON.stringify(stored)).not.toContain(issued.token.slice(21));
    expect(await db().select().from(app_device_request).where(eq(app_device_request.org_id, orgId))).toEqual([]);

    expect(await verifyDeviceToken(orgId, issued.token)).toEqual({ deviceId: issued.deviceId, userId: owner });
    expect(await verifyDeviceToken(orgId, `${issued.token.slice(0, -1)}${issued.token.endsWith("A") ? "B" : "A"}`)).toBeNull();
    expect(await verifyDeviceToken("another-org", issued.token)).toBeNull();
  }, "device-issue");
});

it.skipIf(!reachable)("refuses a denied, expired or unknown request", async () => {
  await withTestOrg(async (orgId) => {
    const owner = await user(orgId, "owner");
    const denied = await startDeviceRequest(orgId, "Laptop");
    await decideDeviceRequest({ orgId, userCode: denied.userCode, userId: owner, approve: false });
    expect(await findDeviceRequest(orgId, denied.userCode)).toBeNull();
    expect(await redeemDeviceRequest(orgId, denied.deviceCode)).toEqual({ status: "denied" });

    const late = await startDeviceRequest(orgId, "");
    await db().execute(sql`update app_device_request set expires_at = now() - interval '1 second' where org_id = ${orgId}`);
    expect(await findDeviceRequest(orgId, late.userCode)).toBeNull();
    expect(await decideDeviceRequest({ orgId, userCode: late.userCode, userId: owner, approve: true })).toBeNull();
    expect(await redeemDeviceRequest(orgId, late.deviceCode)).toEqual({ status: "expired" });
    expect(await redeemDeviceRequest(orgId, "not-a-code")).toEqual({ status: "expired" });
    expect(await findDeviceRequest(orgId, "AAAA-AAAA")).toBeNull();
  }, "device-refuse");
});

it.skipIf(!reachable)("stops a token at once on revoke, user disable or 30 idle days", async () => {
  await withTestOrg(async (orgId) => {
    const owner = await user(orgId, "owner");
    const admin = await user(orgId, "admin");
    async function issue(name: string) {
      const started = await startDeviceRequest(orgId, name);
      await decideDeviceRequest({ orgId, userCode: started.userCode, userId: owner, approve: true });
      const issued = await redeemDeviceRequest(orgId, started.deviceCode);
      if (issued.status !== "issued") throw new Error(issued.status);
      return issued;
    }
    const phone = await issue("Phone");
    const tablet = await issue("Tablet");
    const laptop = await issue("Laptop");

    await db().execute(sql`update app_device set revoked_at = now(), revoked_by_user_id = ${admin} where id = ${phone.deviceId}`);
    expect(await verifyDeviceToken(orgId, phone.token)).toBeNull();

    await db().execute(sql`update app_device set last_used_at = now() - interval '31 days' where id = ${tablet.deviceId}`);
    expect(await verifyDeviceToken(orgId, tablet.token)).toBeNull();

    await db().execute(sql`update app_device set last_used_at = now() - interval '2 minutes' where id = ${laptop.deviceId}`);
    expect(await verifyDeviceToken(orgId, laptop.token)).not.toBeNull();
    const [touched] = await db().select().from(app_device).where(eq(app_device.id, laptop.deviceId));
    expect(Date.now() - touched!.last_used_at.getTime()).toBeLessThan(30_000);

    const listed = await listDevices(orgId);
    expect(listed.map((d) => [d.name, d.status, d.userEmail])).toEqual([
      ["Laptop", "active", "owner@example.test"],
      ["Tablet", "idle", "owner@example.test"],
      ["Phone", "revoked", "owner@example.test"],
    ]);

    await db().update(app_user).set({ disabled_at: new Date() }).where(eq(app_user.id, owner));
    expect(await verifyDeviceToken(orgId, laptop.token)).toBeNull();
  }, "device-revoke");
});

it.skipIf(!reachable)("caps the requests that wait for approval", async () => {
  await withTestOrg(async (orgId) => {
    for (let i = 0; i < 20; i++) await startDeviceRequest(orgId, `Device ${i}`);
    await expect(startDeviceRequest(orgId, "One more")).rejects.toMatchObject({ code: "too_many" });
    await db().execute(sql`update app_device_request set expires_at = now() - interval '1 second' where org_id = ${orgId}`);
    await expect(startDeviceRequest(orgId, "After expiry")).resolves.toMatchObject({ userCode: expect.any(String) });
  }, "device-cap");
});

import { expect } from "vitest";
import { body, type Case } from "../src/case";

export const authCases: Case[] = [
  { op: "GET /version", name: "reports the version", as: "anonymous", status: 200 },
  { op: "GET /auth/status", name: "reports the sign-in provider", as: "anonymous", status: 200 },
  {
    op: "GET /auth/session", name: "names the device token's user", status: 200,
    check: (r) => expect(body(r)).toMatchObject({ user: { id: "contract-admin" }, role: "admin", signedIn: true }),
  },
  {
    op: "GET /auth/session", name: "reports no user without a credential", as: "anonymous", status: 200,
    check: (r) => expect(body(r).signedIn).toBe(false),
  },
  { op: "GET /auth/begin", name: "needs the worker to start a sign-in", as: "anonymous", status: 502 },
  { op: "GET /auth/callback", name: "sends a callback without state back to sign-in", as: "anonymous", status: 302 },
  {
    op: "POST /auth/device/code", name: "starts a device sign-in", as: "anonymous", status: 200,
    request: { body: { name: "Contract phone" } },
    check: (r, c) => {
      c.ids.deviceCode = String(body(r).deviceCode);
      c.ids.userCode = String(body(r).userCode);
    },
  },
  {
    op: "POST /auth/device/token", name: "waits for the approval", as: "anonymous", status: 400,
    request: (c) => ({ body: { deviceCode: c.ids.deviceCode } }),
    check: (r) => expect(body(r).error).toBe("authorization_pending"),
  },
  {
    op: "POST /auth/device/approve", name: "refuses an approval from a device token", status: 403,
    request: (c) => ({ body: { userCode: c.ids.userCode, approve: true } }),
  },
  {
    op: "POST /auth/device/token", name: "refuses an unknown device code", as: "anonymous", status: 400,
    request: { body: { deviceCode: "not-a-real-code" } },
  },
  { op: "POST /auth/logout", name: "ends a session that does not exist", as: "anonymous", status: 200 },
];

import { beforeEach, expect, it, vi } from "vitest";
const state = vi.hoisted(() => ({ user: null as { id: string } | null, provider: false, rows: [] as unknown[] }));
vi.mock("@/lib/auth", () => ({ SESSION_COOKIE_NAME: "openneko_session", getCurrentUser: async () => state.user, currentDevice: async () => null, getAuthProvider: async () => state.provider ? {} : null }));
vi.mock("@neko/db", () => ({ app_user: {}, eq: vi.fn(), getOrgId: async () => "org", resolveUserGroups: async () => ({ administrator: (state.rows[0] as { role?: string } | undefined)?.role === "admin", groupIds: [], slugs: [], ssoGroupIds: [] }), db: () => ({ select: () => ({ from: () => ({ where: () => ({ limit: async () => state.rows }) }) }) }) }));
import { getCurrentActor } from "@/lib/actor";
beforeEach(() => { state.user = null; state.provider = false; state.rows = []; });
it("uses the persisted owner immediately and grants no anonymous admin fallback", async () => {
  expect(await getCurrentActor()).toEqual({ userId: null, role: "member" });
  state.user = { id: "usr_owner" }; state.rows = [{ role: "admin" }];
  expect(await getCurrentActor()).toEqual({ userId: "usr_owner", role: "admin" });
});

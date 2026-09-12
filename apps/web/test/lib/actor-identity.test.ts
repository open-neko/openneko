import { beforeEach, expect, it, vi } from "vitest";
const state = vi.hoisted(() => ({ user: null as { id: string } | null, provider: false, rows: [] as unknown[] }));
vi.mock("@/lib/auth", () => ({ getCurrentUser: async () => state.user, getAuthProvider: async () => state.provider ? {} : null }));
vi.mock("@neko/db", () => ({ app_user: {}, eq: vi.fn(), getOrgId: async () => "org", db: () => ({ select: () => ({ from: () => ({ where: () => ({ limit: async () => state.rows }) }) }) }) }));
import { getCurrentActor } from "@/lib/actor";
import { GET as session } from "@/app/api/auth/session/route";
beforeEach(() => { state.user = null; state.provider = false; state.rows = []; });
it("uses the persisted owner immediately and grants no anonymous admin fallback", async () => {
  expect(await getCurrentActor()).toEqual({ userId: null, role: "member" });
  state.user = { id: "usr_owner" }; state.rows = [{ role: "admin" }];
  expect(await getCurrentActor()).toEqual({ userId: "usr_owner", role: "admin" });
});

it("reports solo identity separately from sign-in state for the navigation", async () => {
  state.user = { id: "usr_owner" }; state.rows = [{ role: "admin" }];
  expect(await (await session()).json()).toMatchObject({ user: { id: "usr_owner" }, authEnabled: false });
  state.provider = true;
  expect(await (await session()).json()).toMatchObject({ user: { id: "usr_owner" }, authEnabled: true });
});

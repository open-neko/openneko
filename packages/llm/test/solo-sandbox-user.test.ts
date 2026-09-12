import { afterEach, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  owner: null as string | null,
  users: [] as Array<{ id: string; role: string; disabledAt: Date | null; sub?: string | null }>,
}));
vi.mock("@neko/db", () => ({
  organization: { id: "org", solo_admin_user_id: "owner" },
  app_user: { id: "id", org_id: "org_id", role: "role", disabled_at: "disabled_at", sub: "sub" },
  operator_profile: {}, work_run: {}, and: vi.fn(), eq: vi.fn(),
  db: () => ({ select: () => ({ from: (table: { solo_admin_user_id?: string }) => ({ where: () => ({
    limit: async () => table.solo_admin_user_id ? [{ owner: state.owner }] : state.users.filter(u => u.id === state.owner),
  }) }) }) }),
}));

import { getSoloSandboxUser } from "../src/work/personas";

afterEach(() => { state.users = []; state.owner = null; vi.unstubAllEnvs(); });

it("retains the solo admin identity and excludes detached and disabled actors while retaining the owner as users are added", async () => {
  vi.stubEnv("OPENNEKO_PROFILE", "solo");
  const admin = { userId: null, role: "admin" };
  expect(await getSoloSandboxUser("org-a", admin)).toBeNull();
  expect(await getSoloSandboxUser("org-a", { ...admin, role: "member" })).toBeNull();
  state.owner = "alice";
  state.users = [{ id: "alice", role: "admin", disabledAt: null }];
  expect(await getSoloSandboxUser("org-a", admin)).toMatchObject({ principalId: "alice" });
  state.users[0].sub = "sso-alice";
  expect(await getSoloSandboxUser("org-a", admin)).toBeNull();
  state.users[0].sub = null;
  const alice = { userId: "alice", role: "admin" };
  expect(await getSoloSandboxUser("org-a", alice)).toMatchObject({ principalId: "alice" });
  state.users[0].disabledAt = new Date();
  expect(await getSoloSandboxUser("org-a", alice)).toBeNull();
  state.users[0].disabledAt = null;
  state.users.push({ id: "bob", role: "member", disabledAt: null });
  expect(await getSoloSandboxUser("org-a", alice)).toMatchObject({ principalId: "alice" });
  state.users = [];
  vi.stubEnv("OPENNEKO_PROFILE", "team");
  expect(await getSoloSandboxUser("org-a", admin)).toBeNull();
});

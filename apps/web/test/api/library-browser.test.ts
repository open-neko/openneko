import { NextRequest } from "next/server";
import { beforeEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ browse: vi.fn(), parse: vi.fn(), edit: vi.fn(), read: vi.fn(), materialize: vi.fn(), actor: vi.fn() }));
vi.mock("@/lib/actor", () => ({ getCurrentActor: mocks.actor }));
vi.mock("@/lib/db", () => ({ getOrgId: async () => "trusted-org" }));
vi.mock("@neko/llm", () => ({ materializeTeamLibrary: mocks.materialize }));
vi.mock("@neko/llm/work", () => ({ browseLibrary: mocks.browse, parseLibraryBrowseOptions: mocks.parse, editLibraryConcept: mocks.edit, readLibraryConcept: mocks.read, archiveLibraryConcept: vi.fn() }));
import { GET as browse } from "@/app/api/library/route";
import { GET, PATCH } from "@/app/api/library/concepts/[id]/route";
const id = "11111111-1111-4111-8111-111111111111";
const context = { params: Promise.resolve({ id }) };
const input = { title: "Corrected title", description: "", type: "policy", body: "Corrected text", updatedAt: "2026-09-01T00:00:00.000Z" };
const request = (body: unknown) => new NextRequest(`http://localhost/api/library/concepts/${id}`, { method: "PATCH", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
beforeEach(() => {
  vi.clearAllMocks();
  mocks.actor.mockResolvedValue({ userId: "trusted-user", role: "member" });
  mocks.parse.mockReturnValue({ view: "concepts" });
  mocks.browse.mockResolvedValue({ total: 0 });
});
it("takes org, ownership and admin rights only from the authenticated server context", async () => {
  await browse(new Request("http://localhost/api/library?userId=another-user&orgId=another-org&isAdmin=true"));
  expect(mocks.browse).toHaveBeenCalledWith({ orgId: "trusted-org", userId: "trusted-user", isAdmin: false }, { view: "concepts" });
  mocks.parse.mockReturnValue({ view: "review" });
  expect((await browse(new Request("http://localhost/api/library?view=review"))).status).toBe(403);
  expect((await PATCH(request({ ...input, userId: "another-user" }), context)).status).toBe(400);
  expect(mocks.edit).not.toHaveBeenCalled();
});
it("returns missing, invalid and stale edit failures without false success", async () => {
  mocks.read.mockResolvedValue(null);
  expect((await GET(new NextRequest("http://localhost"), context)).status).toBe(404);
  expect((await PATCH(request({ ...input, title: " " }), context)).status).toBe(400);
  mocks.edit.mockResolvedValue({ status: "not_found" });
  expect((await PATCH(request(input), context)).status).toBe(404);
  mocks.edit.mockResolvedValue({ status: "conflict" });
  expect((await PATCH(request(input), context)).status).toBe(409);
  expect(mocks.materialize).not.toHaveBeenCalled();
});
it("refreshes the existing team bundle after an authorized save, never for personal edits", async () => {
  mocks.edit.mockResolvedValue({ status: "saved", concept: { userId: "trusted-user" }, searchIndexed: true });
  expect((await PATCH(request(input), context)).status).toBe(200);
  expect(mocks.materialize).not.toHaveBeenCalled();
  mocks.actor.mockResolvedValue({ userId: "trusted-user", role: "admin" });
  mocks.edit.mockResolvedValue({ status: "saved", concept: { userId: null }, searchIndexed: true });
  expect((await PATCH(request(input), context)).status).toBe(200);
  expect(mocks.materialize).toHaveBeenCalledWith("trusted-org");
});

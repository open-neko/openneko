import { mkdtemp, readFile, readdir, rm, writeFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it, vi } from "vitest";
import {
  addLocalGroupMember,
  app_user,
  builtinGroupId,
  createUserGroup,
  db,
  grantItem,
  library_concept,
  revokeItem,
  sql,
} from "@neko/db";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";

vi.mock("../../src/embedding", () => ({
  embedText: async () => [1, ...Array(383).fill(0)],
  vectorLiteral: (vector: number[]) => `[${vector.join(",")}]`,
}));
import { copyAllowedTeamLibrary } from "../../src/library/staging";
import { rebuildIndexes } from "../../src/library/tree";
import { browseLibrary, parseLibraryBrowseOptions, readLibraryConcept, searchLibraryByContext } from "../../src/work/library";
import { libraryAccessFor, runAllowedLibrary } from "../../src/work/entitlement-scope";

describe("copyAllowedTeamLibrary", () => {
  it("stages only held concepts and rebuilds indexes without hidden titles", async () => {
    const root = await mkdtemp(join(tmpdir(), "okf-src-"));
    const dest = await mkdtemp(join(tmpdir(), "okf-dest-"));
    try {
      for (const [path, title] of [["revenue/net.md", "Net revenue"], ["revenue/eu/vat.md", "EU VAT"], ["policies/refunds.md", "Refund policy"], ["hr/pay.md", "Pay bands"]]) {
        await mkdir(join(root, path, ".."), { recursive: true });
        await writeFile(join(root, path), `---\ntype: note\ntitle: ${title}\n---\nbody\n`);
      }
      await rebuildIndexes(root);
      const copied = await copyAllowedTeamLibrary(root, dest, { prefixes: ["revenue/"], paths: ["policies/refunds.md"] });
      expect(copied).toEqual(["policies/refunds.md", "revenue/eu/vat.md", "revenue/net.md"]);
      expect((await readdir(dest)).sort()).toEqual(["index.md", "policies", "revenue"]);
      expect(await readFile(join(dest, "index.md"), "utf8")).not.toContain("hr");
      expect(await copyAllowedTeamLibrary(root, join(dest, "none"), { prefixes: [], paths: [] })).toEqual([]);
    } finally {
      await rm(root, { recursive: true, force: true });
      await rm(dest, { recursive: true, force: true });
    }
  });
});

const reachable = await dbReachable();
(reachable ? describe : describe.skip)("library entitlements", () => {
  it("limits team concepts to held collections and concepts, and keeps personal concepts", async () => {
    const orgId = uniqueOrgId("lib-ent");
    await createTestOrg(orgId);
    try {
      const ann = `${orgId}-ann`;
      await db().insert(app_user).values({ id: ann, org_id: orgId, email: "ann@example.test" });
      const everyone = await builtinGroupId(orgId, "everyone");
      await revokeItem(orgId, { groupId: everyone, itemType: "library_collection", itemId: "*" });
      await revokeItem(orgId, { groupId: everyone, itemType: "library_concept", itemId: "*" });
      const finance = await createUserGroup(orgId, { name: "Finance" });
      await addLocalGroupMember(orgId, finance.id, ann);

      const vector = sql`${JSON.stringify([1, ...Array(383).fill(0)])}::vector`;
      const insert = async (path: string, title: string, userId: string | null = null) => {
        const [row] = await db().insert(library_concept).values({
          org_id: orgId, user_id: userId, path, type: "note", title, body: `${title} body`, status: "stable", embedding: vector,
        }).returning({ id: library_concept.id });
        return row!.id;
      };
      const net = await insert("revenue/net.md", "Net revenue");
      const refunds = await insert("policies/refunds.md", "Refund policy");
      const pay = await insert("hr/pay.md", "Pay bands");
      const mine = await insert("notes/mine.md", "My note", ann);
      await grantItem(orgId, { groupId: finance.id, itemType: "library_collection", itemId: "revenue/" });
      await grantItem(orgId, { groupId: finance.id, itemType: "library_concept", itemId: refunds });

      const actor = { orgId, kind: "user" as const, userId: ann };
      const reader = { orgId, userId: ann, isAdmin: false, access: await libraryAccessFor(actor) };
      const browsed = await browseLibrary(reader, parseLibraryBrowseOptions(new URLSearchParams("view=concepts")));
      expect(browsed.concepts.map((c) => c.title).sort()).toEqual(["My note", "Net revenue", "Refund policy"]);
      expect(await readLibraryConcept(reader, pay)).toBeNull();
      expect((await readLibraryConcept(reader, net))?.title).toBe("Net revenue");

      const found = await searchLibraryByContext({ orgId, userId: ann, query: "anything", limit: 20, access: reader.access });
      expect(found.map((r) => r.concept.id).sort()).toEqual([mine, net, refunds].sort());
      expect(await runAllowedLibrary(actor)).toEqual({ prefixes: ["revenue/"], paths: ["policies/refunds.md"] });

      const admin = { orgId, userId: null, isAdmin: true, access: reader.access };
      expect((await browseLibrary(admin, parseLibraryBrowseOptions(new URLSearchParams("view=concepts&layer=team")))).total).toBe(3);
    } finally {
      await deleteTestOrg(orgId);
    }
  });
});

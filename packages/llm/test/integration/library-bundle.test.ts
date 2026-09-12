// Round-trip tests for OKF bundle export/import and starter packs,
// against real Postgres with mocked embeddings.

import { afterAll, beforeEach, describe, expect, it, vi } from "vitest";
import { pool } from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";

vi.mock("../../src/embedding", async () => {
  const EMBEDDING_DIM = 384;
  return {
    EMBEDDING_DIM,
    embedText: vi.fn(async (text: string) => {
      const seed = text
        .split("")
        .reduce((acc, c) => (acc * 31 + c.charCodeAt(0)) >>> 0, 7);
      const v = new Array<number>(EMBEDDING_DIM);
      for (let i = 0; i < EMBEDDING_DIM; i++) {
        v[i] = Math.sin(seed + i) * 0.1;
      }
      return v;
    }),
    vectorLiteral: (vec: number[]) => `[${vec.join(",")}]`,
  };
});

import {
  collectLibraryBundleFiles,
  importLibraryBundleFiles,
} from "../../src/library/bundle";
import { installLibraryPack, listLibraryPacks } from "../../src/library/packs";
import { runEmbeddingIndexJob } from "../../src/embedding-jobs";
import {
  listLibraryConcepts,
  searchLibraryByContext,
  upsertLibraryConcept,
} from "../../src/work/library";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[library-bundle] skipping: Postgres unreachable.");
}

describeIfDb("library OKF bundle round trip", () => {
  afterAll(async () => {
    await pool().end();
  });

  let orgA: string;
  let orgB: string;

  beforeEach(async () => {
    orgA = uniqueOrgId("lib-exp");
    orgB = uniqueOrgId("lib-imp");
    await createTestOrg(orgA, "Export Org");
    await createTestOrg(orgB, "Import Org");
  });

  it("exports team concepts and imports them with trust state intact", async () => {
    try {
      await upsertLibraryConcept({
        orgId: orgA,
        userId: null,
        path: "policies/refund-policy.md",
        type: "Policy",
        title: "Refund policy",
        description: "Approval limits for refunds.",
        tags: ["finance"],
        body: "Refunds over $500 need CFO approval.",
        status: "stable",
        verified: [{ by: "human:cfo", at: "2026-08-01T00:00:00.000Z" }],
        staleAfter: "2027-01-01",
        sources: [{ resource: "/uploads/t/refund.pdf" }],
      });
      await upsertLibraryConcept({
        orgId: orgA,
        userId: null,
        path: "notes/pending-idea.md",
        type: "Notes",
        title: "Pending idea",
        body: "Awaiting review.",
        status: "draft",
      });

      const files = await collectLibraryBundleFiles(orgA);
      expect(files.some((f) => f.path === "index.md")).toBe(true);
      expect(files.some((f) => f.path === "policies/refund-policy.md")).toBe(true);
      const rootIndex = files.find((f) => f.path === "index.md");
      expect(rootIndex?.content).toContain('okf_version: "0.2"');

      const result = await importLibraryBundleFiles({ orgId: orgB, files });
      expect(result.imported).toBe(2);
      expect(result.skipped).toEqual([]);

      const imported = await listLibraryConcepts({ orgId: orgB, userId: null });
      const policy = imported.find((c) => c.path === "policies/refund-policy.md");
      expect(policy?.status).toBe("stable");
      expect(policy?.verified).toEqual([
        { by: "human:cfo", at: "2026-08-01T00:00:00.000Z" },
      ]);
      expect(policy?.staleAfter).toBe("2027-01-01");
      expect(policy?.sources).toEqual([{ resource: "/uploads/t/refund.pdf" }]);
      const draft = imported.find((c) => c.path === "notes/pending-idea.md");
      expect(draft?.status).toBe("draft");

      // Import leaves durable indexing work; process it before semantic search.
      for (const concept of imported) {
        await runEmbeddingIndexJob({ kind: "concept", orgId: orgB, id: concept.id });
      }
      const found = await searchLibraryByContext({
        orgId: orgB,
        userId: null,
        query: "refund approval limits",
      });
      expect(found.some((r) => r.concept.path === "policies/refund-policy.md")).toBe(
        true,
      );
    } finally {
      await deleteTestOrg(orgA);
      await deleteTestOrg(orgB);
    }
  });

  it("skips malformed files without failing the import", async () => {
    try {
      const result = await importLibraryBundleFiles({
        orgId: orgB,
        files: [
          { path: "../escape.md", content: "---\ntype: X\n---\nbody" },
          { path: "no-frontmatter.md", content: "just text" },
          { path: "index.md", content: "---\ntitle: idx\n---\n# idx" },
          {
            path: "ok.md",
            content: "---\ntype: Note\ntitle: Ok\n---\n\nA valid concept.",
          },
        ],
      });
      expect(result.imported).toBe(1);
      expect(result.skipped.map((s) => s.path).sort()).toEqual([
        "../escape.md",
        "no-frontmatter.md",
      ]);
    } finally {
      await deleteTestOrg(orgA);
      await deleteTestOrg(orgB);
    }
  });
});

describeIfDb("starter packs", () => {
  let orgId: string;

  beforeEach(async () => {
    orgId = uniqueOrgId("lib-pack");
    await createTestOrg(orgId, "Pack Org");
  });

  it("lists bundled packs with metadata", async () => {
    const packs = await listLibraryPacks();
    const retail = packs.find((p) => p.id === "retail-ops");
    expect(retail).toBeDefined();
    expect(retail?.title).toBe("Retail operations starter");
    expect(retail?.concepts).toBeGreaterThanOrEqual(3);
  });

  it("installs a pack as stable concepts vouched for by the installer", async () => {
    try {
      const result = await installLibraryPack({
        orgId,
        packId: "retail-ops",
        installedBy: "admin-1",
      });
      expect(result.imported).toBeGreaterThanOrEqual(3);

      const team = await listLibraryConcepts({ orgId, userId: null });
      const metric = team.find((c) => c.path === "metrics/net-revenue.md");
      expect(metric?.status).toBe("stable");
      expect(metric?.verified.at(-1)?.by).toBe("human:admin-1");
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("rejects unknown pack ids", async () => {
    await expect(
      installLibraryPack({ orgId, packId: "nope", installedBy: null }),
    ).rejects.toThrow(/Unknown library pack/);
    await deleteTestOrg(orgId);
  });
});

// Portable OKF bundles: export the team library as plain files and
// import a bundle back — the round trip behind the data-ownership
// promise. Exports carry the full trust state (status, verified,
// generated, sources, stale_after); imports restore it faithfully,
// recompute embeddings (vectors never travel), and finish with a team
// bundle re-materialization. Personal layers never enter a bundle.

import { mkdtemp, readFile, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, relative, sep } from "node:path";
import { listLibraryConcepts, upsertLibraryConcept } from "../work/library";
import { isValidConceptPath } from "./fence";
import { conceptToOkfDoc, materializeTeamLibrary } from "./materialize";
import { parseConceptDoc } from "./okf";
import { rebuildIndexes, writeConceptDoc } from "./tree";

export type BundleFile = { path: string; content: string };

/**
 * Collect the team library as OKF bundle files (concepts + indexes),
 * in-memory. Draft and stable concepts are included so a restore
 * preserves the approval queue; archived rows stay behind.
 */
export async function collectLibraryBundleFiles(
  orgId: string,
): Promise<BundleFile[]> {
  const concepts = await listLibraryConcepts({ orgId, userId: null, limit: null });
  const dir = await mkdtemp(join(tmpdir(), "neko-okf-export-"));
  try {
    for (const concept of concepts) {
      await writeConceptDoc(dir, {
        path: concept.path,
        doc: conceptToOkfDoc(concept),
      });
    }
    await rebuildIndexes(dir);
    return await collectFiles(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

export type BundleImportResult = {
  imported: number;
  skipped: Array<{ path: string; reason: string }>;
};

/**
 * Import OKF bundle files into the team layer. Non-concept files
 * (index.md, log.md, anything without parseable frontmatter + type) are
 * skipped per OKF conformance rules rather than rejected. Status
 * defaults to draft when the bundle doesn't say — unlabeled knowledge
 * still goes through approval.
 */
export async function importLibraryBundleFiles(input: {
  orgId: string;
  files: BundleFile[];
  importedBy?: string | null;
}): Promise<BundleImportResult> {
  const skipped: Array<{ path: string; reason: string }> = [];
  let imported = 0;
  for (const file of input.files) {
    const path = file.path.replace(/\\/g, "/").replace(/^\/+/, "");
    const base = path.split("/").at(-1) ?? "";
    if (!path.endsWith(".md") || base === "index.md" || base === "log.md") {
      continue;
    }
    if (!isValidConceptPath(path)) {
      skipped.push({ path, reason: "invalid concept path" });
      continue;
    }
    const doc = parseConceptDoc(file.content);
    if (!doc) {
      skipped.push({ path, reason: "not an OKF concept (missing frontmatter/type)" });
      continue;
    }
    await upsertLibraryConcept({
      orgId: input.orgId,
      userId: null,
      path,
      type: doc.type,
      title: doc.title ?? base.replace(/\.md$/, ""),
      description: doc.description ?? null,
      tags: doc.tags ?? [],
      body: doc.body,
      sources: doc.sources ?? [],
      generatedBy: doc.generated?.by ?? null,
      status: doc.status ?? "draft",
      staleAfter: doc.stale_after ?? null,
      verified: doc.verified ?? [],
    });
    imported += 1;
  }
  if (imported > 0) await materializeTeamLibrary(input.orgId);
  return { imported, skipped };
}

async function collectFiles(root: string): Promise<BundleFile[]> {
  const out: BundleFile[] = [];
  const walk = async (dir: string): Promise<void> => {
    for (const entry of await readdir(dir, { withFileTypes: true })) {
      const absolute = join(dir, entry.name);
      if (entry.isDirectory()) {
        await walk(absolute);
      } else if (entry.name.endsWith(".md")) {
        out.push({
          path: relative(root, absolute).split(sep).join("/"),
          content: await readFile(absolute, "utf8"),
        });
      }
    }
  };
  await walk(root);
  return out.sort((a, b) => a.path.localeCompare(b.path));
}

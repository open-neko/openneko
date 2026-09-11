// Materialize the team library as an OKF bundle at
// <orgRoot>/library/okf — the wiki-walk surface for agents and the tree
// config-vcs versions. Team layer only: personal concepts never touch
// this tree (docs/OKF.md layering; same hard rule as memory snapshots).
// Wholesale rewrite like snapshotDurableMemories so archives disappear.

import { mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { listLibraryConcepts, type LibraryConcept } from "../work/library";
import { getOrgAgentRoot } from "../work/workspace";
import type { OkfConceptDoc } from "./okf";
import { rebuildIndexes, writeConceptDoc } from "./tree";

export function teamLibraryBundleRoot(orgId: string): string {
  return join(getOrgAgentRoot(orgId), "library", "okf");
}

export function conceptToOkfDoc(concept: LibraryConcept): OkfConceptDoc {
  return {
    type: concept.type,
    title: concept.title,
    description: concept.description ?? undefined,
    tags: concept.tags.length > 0 ? concept.tags : undefined,
    sources: concept.sources.length > 0 ? concept.sources : undefined,
    generated:
      concept.generatedBy && concept.generatedAt
        ? { by: concept.generatedBy, at: concept.generatedAt }
        : undefined,
    verified: concept.verified.length > 0 ? concept.verified : undefined,
    status: concept.status,
    stale_after: concept.staleAfter ?? undefined,
    extra: { openneko_concept_id: concept.id },
    body: concept.body,
  };
}

/**
 * Best-effort, like the memory snapshotter — a materialization failure
 * must never fail the DB write that triggered it.
 */
export async function materializeTeamLibrary(orgId: string): Promise<void> {
  try {
    // Approved knowledge only: drafts wait for approval, deprecated
    // concepts stay out of the agent-visible tree.
    const concepts = await listLibraryConcepts({
      orgId,
      userId: null,
      status: "stable",
      limit: null,
    });
    const bundleRoot = teamLibraryBundleRoot(orgId);
    await rm(bundleRoot, { recursive: true, force: true });
    if (concepts.length === 0) return;
    await mkdir(bundleRoot, { recursive: true });
    for (const concept of concepts) {
      await writeConceptDoc(bundleRoot, {
        path: concept.path,
        doc: conceptToOkfDoc(concept),
      });
    }
    await rebuildIndexes(bundleRoot);

    const { recordConfigChange } = await import("../config-vcs");
    await recordConfigChange({
      workspaceRoot: getOrgAgentRoot(orgId),
      orgId,
      paths: ["library"],
      message: "Library checkpoint",
      artifactKind: "library",
      artifactRef: "team",
    });
  } catch (err) {
    console.warn(
      `[library] team materialization failed: ${err instanceof Error ? err.message : err}`,
    );
  }
}

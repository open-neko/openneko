import { readFile, rm } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import {
  getOrgAgentRoot,
  listLibraryDocumentsOutsideStore,
  markLibraryDocumentStored,
  markLibraryDocumentFailed,
} from "@neko/llm";
import { storeLibraryFile } from "@neko/llm/library/store";

function workspaceUploadPath(orgId: string, relativePath: string): string {
  const orgRoot = resolve(getOrgAgentRoot(orgId));
  const absolute = resolve(join(orgRoot, relativePath.replace(/\\/g, "/").replace(/^\/+/, "")));
  if (!absolute.startsWith(orgRoot + sep)) throw new Error("library upload path leaves the org workspace");
  return absolute;
}

type MoveDependencies = {
  list: typeof listLibraryDocumentsOutsideStore;
  read: (orgId: string, relativePath: string) => Promise<Buffer>;
  save: typeof storeLibraryFile;
  moved: typeof markLibraryDocumentStored;
  failed: typeof markLibraryDocumentFailed;
  removeOriginal: (orgId: string, relativePath: string) => Promise<void>;
};

const defaultDependencies: MoveDependencies = {
  list: listLibraryDocumentsOutsideStore,
  read: (orgId, relativePath) => readFile(workspaceUploadPath(orgId, relativePath)),
  save: storeLibraryFile,
  moved: markLibraryDocumentStored,
  failed: markLibraryDocumentFailed,
  removeOriginal: (orgId, relativePath) => rm(workspaceUploadPath(orgId, relativePath), { force: true }),
};

// Thread attachments keep their thread copy; Library-only uploads leave the org workspace.
export async function moveLibraryDocumentsToStore(
  overrides: Partial<MoveDependencies> = {},
): Promise<{ moved: number; missing: number }> {
  const deps = { ...defaultDependencies, ...overrides };
  let moved = 0;
  let missing = 0;
  for (const document of await deps.list()) {
    if (!document.userId) {
      missing += 1;
      await deps.failed({
        orgId: document.orgId,
        id: document.documentId,
        error: "This document has no owner. Remove it and upload it again.",
      });
      continue;
    }
    let data: Buffer;
    try {
      data = await deps.read(document.orgId, document.relativePath);
    } catch {
      missing += 1;
      await deps.failed({
        orgId: document.orgId,
        id: document.documentId,
        error: "The original file is missing. Remove this document and upload it again.",
      });
      continue;
    }
    const stored = await deps.save({ orgId: document.orgId, userId: document.userId, filename: document.filename, data });
    await deps.moved({ orgId: document.orgId, id: document.documentId, relativePath: stored.relativePath });
    if (document.relativePath.startsWith("library/uploads/")) {
      await deps.removeOriginal(document.orgId, document.relativePath).catch(() => {});
    }
    moved += 1;
  }
  return { moved, missing };
}

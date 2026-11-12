import { and, db, eq, library_document, sql } from "@neko/db";

export const LIBRARY_DOCUMENT_STATUSES = ["ready", "failed"] as const;
export type LibraryDocumentStatus = (typeof LIBRARY_DOCUMENT_STATUSES)[number];

export type LibraryDocument = {
  id: string;
  orgId: string;
  userId: string | null;
  sourceThreadId: string | null;
  filename: string;
  relativePath: string;
  contentHash: string;
  sizeBytes: number;
  status: LibraryDocumentStatus;
  error: string | null;
  createdAt: string;
  updatedAt: string;
};

export type LibraryPage = {
  total: number;
  page: number;
  pageSize: number;
  documents: Array<Pick<LibraryDocument, "id" | "filename" | "relativePath" | "sizeBytes" | "status" | "error" | "createdAt">>;
};

export async function listLibraryDocumentsOutsideStore(): Promise<
  Array<{ orgId: string; documentId: string; userId: string | null; filename: string; relativePath: string }>
> {
  return db()
    .select({
      orgId: library_document.org_id,
      documentId: library_document.id,
      userId: library_document.user_id,
      filename: library_document.filename,
      relativePath: library_document.relative_path,
    })
    .from(library_document)
    .where(and(
      sql`${library_document.relative_path} not like 'files/%'`,
      eq(library_document.status, "ready"),
    ));
}

export async function markLibraryDocumentStored(input: {
  orgId: string;
  id: string;
  relativePath: string;
}): Promise<void> {
  await db()
    .update(library_document)
    .set({ relative_path: input.relativePath, status: "ready", error: null, updated_at: new Date() })
    .where(and(eq(library_document.org_id, input.orgId), eq(library_document.id, input.id)));
}

export async function markLibraryDocumentFailed(input: {
  orgId: string;
  id: string;
  error: string;
}): Promise<void> {
  await db()
    .update(library_document)
    .set({ status: "failed", error: input.error, updated_at: new Date() })
    .where(and(eq(library_document.org_id, input.orgId), eq(library_document.id, input.id)));
}

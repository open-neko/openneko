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


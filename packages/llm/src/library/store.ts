import { createHash } from "node:crypto";
import { deleteFile, putFile } from "../files/client";

export const LIBRARY_STORE_PREFIX = "files/";

export function isLibraryStorePath(relativePath: string): boolean {
  return relativePath.startsWith(LIBRARY_STORE_PREFIX);
}

export function libraryStoreFilePath(relativePath: string): string {
  if (!isLibraryStorePath(relativePath)) throw new Error("Not a Library file");
  return relativePath.slice(LIBRARY_STORE_PREFIX.length);
}

// The folder is content-addressed, so two uploads with the same cleaned name never overwrite each other.
export async function storeLibraryFile(input: {
  orgId: string;
  userId: string;
  filename: string;
  data: Buffer;
  contentType?: string;
}): Promise<{ relativePath: string; contentHash: string; size: number }> {
  const contentHash = createHash("sha256").update(input.data).digest("hex");
  const path = `library/${contentHash}/${input.filename}`;
  await putFile(
    { orgId: input.orgId, userId: input.userId },
    path,
    new Uint8Array(input.data),
    input.contentType,
  );
  return { relativePath: LIBRARY_STORE_PREFIX + path, contentHash, size: input.data.byteLength };
}

export async function removeLibraryFile(input: {
  orgId: string;
  userId: string;
  relativePath: string;
}): Promise<void> {
  await deleteFile({ orgId: input.orgId, userId: input.userId }, libraryStoreFilePath(input.relativePath));
}

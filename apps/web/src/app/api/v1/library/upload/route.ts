import { NextResponse } from "next/server";
import { filesEnabled, FilesServiceError } from "@neko/llm/files";
import { storeLibraryFile } from "@neko/llm/library/store";
import { createLibraryDocument } from "@neko/llm/work";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";
import {
  MAX_LIBRARY_UPLOAD_REQUEST_BYTES,
  validateLibraryUploadBatch,
} from "@/lib/library-upload-contract";
import { ALLOWED_UPLOAD_EXTENSIONS, safeFileName } from "@/lib/work-files";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const contentLength = Number.parseInt(
    request.headers.get("content-length") ?? "0",
    10,
  );
  if (
    Number.isFinite(contentLength) &&
    contentLength > MAX_LIBRARY_UPLOAD_REQUEST_BYTES
  ) {
    return NextResponse.json(
      {
        error:
          "The selected files exceed the 100 MB import limit. Choose fewer or smaller files.",
      },
      { status: 413 },
    );
  }
  const form = await request.formData().catch(() => null);
  if (!form) {
    return NextResponse.json({ error: "Invalid multipart body" }, { status: 400 });
  }
  // `files` is the batch contract. Keep accepting the original singular
  // field so existing API clients continue to work after the UI switches.
  const files = [...form.getAll("files"), ...form.getAll("file")].filter(
    (value): value is File => value instanceof File,
  );
  const validation = validateLibraryUploadBatch(files);
  if (!validation.ok) {
    return NextResponse.json(
      { error: validation.error },
      { status: validation.status },
    );
  }

  const names = new Set<string>();
  for (const file of files) {
    const safeName = safeFileName(file.name);
    if (names.has(safeName)) {
      return NextResponse.json(
        {
          error: `More than one selected file becomes "${safeName}" after filename cleanup. Rename one and try again.`,
        },
        { status: 400 },
      );
    }
    names.add(safeName);
    const idx = file.name.lastIndexOf(".");
    const ext = idx > 0 ? file.name.slice(idx).toLowerCase() : "";
    if (!ALLOWED_UPLOAD_EXTENSIONS.has(ext)) {
      return NextResponse.json(
        { error: `Unsupported file type "${ext || "(none)"}".` },
        { status: 415 },
      );
    }
  }

  if (!filesEnabled()) {
    return NextResponse.json(
      { error: "File storage is off: openneko serve is not configured." },
      { status: 503 },
    );
  }
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  if (!actor.userId) {
    return NextResponse.json({ error: "Sign in to add files to your library." }, { status: 401 });
  }
  const documents: Array<{
    document: Awaited<ReturnType<typeof createLibraryDocument>>["document"];
    created: boolean;
  }> = [];
  for (const file of files) {
    let saved: Awaited<ReturnType<typeof storeLibraryFile>>;
    try {
      saved = await storeLibraryFile({
        orgId,
        userId: actor.userId,
        filename: safeFileName(file.name || "upload.bin"),
        data: Buffer.from(await file.arrayBuffer()),
        ...(file.type ? { contentType: file.type } : {}),
      });
    } catch (error) {
      if (!(error instanceof FilesServiceError)) throw error;
      return NextResponse.json(
        { error: `"${file.name}" was not stored: ${error.message}`, documents },
        { status: error.status >= 500 ? 502 : error.status },
      );
    }
    const { document, created } = await createLibraryDocument({
      orgId,
      userId: actor.userId,
      filename: safeFileName(file.name || "upload.bin"),
      relativePath: saved.relativePath,
      contentHash: saved.contentHash,
      sizeBytes: saved.size,
    });
    documents.push({ document, created });
  }

  // Keep the singular response members for clients that still post `file`.
  return NextResponse.json({
    ok: true,
    documents,
    document: documents[0]?.document,
    created: documents[0]?.created,
  });
}

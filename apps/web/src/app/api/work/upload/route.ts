import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { getWorkThread } from "@/lib/work-store";
import { ALLOWED_UPLOAD_EXTENSIONS, MAX_UPLOAD_SIZE, saveWorkUpload } from "@/lib/work-files";

export const runtime = "nodejs";

export async function POST(request: Request) {
  const contentLength = Number(request.headers.get("content-length") ?? 0);
  if (contentLength && contentLength > MAX_UPLOAD_SIZE + 4096) {
    return NextResponse.json(
      { error: `File is over ${Math.round(MAX_UPLOAD_SIZE / (1024 * 1024))} MB.` },
      { status: 413 },
    );
  }

  const form = await request.formData().catch(() => null);
  if (!form) {
    return NextResponse.json({ error: "Invalid multipart body" }, { status: 400 });
  }

  const threadId = String(form.get("threadId") ?? "").trim();
  const file = form.get("file");
  if (!threadId || !(file instanceof File)) {
    return NextResponse.json({ error: "threadId and file are required" }, { status: 400 });
  }
  if (file.type.startsWith("image/")) {
    return NextResponse.json({ error: "Image uploads are not supported yet." }, { status: 415 });
  }
  if (file.size > MAX_UPLOAD_SIZE) {
    return NextResponse.json(
      { error: `"${file.name}" is over ${Math.round(MAX_UPLOAD_SIZE / (1024 * 1024))} MB.` },
      { status: 413 },
    );
  }
  const ext = extractExtension(file.name);
  if (!ALLOWED_UPLOAD_EXTENSIONS.has(ext)) {
    return NextResponse.json(
      { error: `Unsupported file type "${ext || "(none)"}".` },
      { status: 415 },
    );
  }

  const orgId = await getOrgId();
  const thread = await getWorkThread(orgId, threadId);
  if (!thread) {
    return NextResponse.json({ error: "Thread not found" }, { status: 404 });
  }

  const saved = await saveWorkUpload(orgId, threadId, file);
  return NextResponse.json({
    file: {
      name: saved.name,
      size: saved.size,
      relativePath: saved.relativePath.replace(/\\/g, "/"),
      absolutePath: saved.absolutePath,
    },
  });
}

function extractExtension(name: string): string {
  const idx = name.lastIndexOf(".");
  if (idx <= 0) return "";
  return name.slice(idx).toLowerCase();
}

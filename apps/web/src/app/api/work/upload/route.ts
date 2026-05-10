import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { getWorkThread } from "@/lib/work-store";
import { saveWorkUpload } from "@/lib/work-files";

export const runtime = "nodejs";

export async function POST(request: Request) {
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
    return NextResponse.json({ error: "image uploads are not supported yet" }, { status: 415 });
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
      relativePath: saved.relativePath.replace(/\\/g, "/"),
      absolutePath: saved.absolutePath,
    },
  });
}

import { NextResponse } from "next/server";
import { extname } from "node:path";
import { getOrgId } from "@/lib/db";
import { FORCE_DOWNLOAD_EXTENSIONS, readWorkFile } from "@/lib/work-files";

type RouteContext = {
  params: Promise<{ path: string[] }>;
};

export const runtime = "nodejs";

export async function GET(_: Request, context: RouteContext) {
  try {
    const { path } = await context.params;
    const relativePath = (path ?? []).join("/");
    const file = await readWorkFile(await getOrgId(), relativePath);
    const body = new Uint8Array(file.data);
    const disposition = FORCE_DOWNLOAD_EXTENSIONS.has(extname(file.filename).toLowerCase())
      ? "attachment"
      : "inline";
    const safeName = file.filename.replace(/"/g, "");
    return new NextResponse(body, {
      status: 200,
      headers: {
        "content-type": file.mimeType,
        "content-disposition": `${disposition}; filename="${safeName}"`,
      },
    });
  } catch (error) {
    return NextResponse.json(
      { error: error instanceof Error ? error.message : String(error) },
      { status: 404 },
    );
  }
}

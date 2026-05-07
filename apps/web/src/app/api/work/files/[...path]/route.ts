import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { readWorkFile } from "@/lib/work-files";

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
    return new NextResponse(body, {
      status: 200,
      headers: {
        "content-type": file.mimeType,
        "content-disposition": `inline; filename="${file.filename}"`,
      },
    });
  } catch (error) {
    return NextResponse.json(
      { error: error instanceof Error ? error.message : String(error) },
      { status: 404 },
    );
  }
}

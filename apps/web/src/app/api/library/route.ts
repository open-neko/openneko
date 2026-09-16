import { NextResponse } from "next/server";
import { browseLibrary, parseLibraryBrowseOptions } from "@neko/llm/work";
import { getCurrentActor } from "@/lib/actor";
import { currentLibraryReader } from "@/lib/entitlements";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

/**
 * Library overview for the current actor: their uploaded documents,
 * their personal concepts, the approved team layer, and — for admins —
 * the team drafts awaiting a decision.
 */
export async function GET(request: Request) {
  let options;
  try {
    options = parseLibraryBrowseOptions(new URL(request.url).searchParams);
  } catch {
    return NextResponse.json({ error: "Invalid library filters." }, { status: 400 });
  }
  const actor = await getCurrentActor();

  if (options.view === "review" && actor.role !== "admin") {
    return NextResponse.json({ error: "Admin access required." }, { status: 403 });
  }
  try {
    return NextResponse.json(await browseLibrary(await currentLibraryReader(), options));
  } catch {
    return NextResponse.json({ error: "Library could not be loaded. Try again." }, { status: 503 });
  }
}

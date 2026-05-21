import { NextRequest, NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import { disconnectConnector } from "@/lib/integrations";

/** POST /api/integrations/disconnect/[plugin] — drop the current operator's credential. */
export async function POST(
  _request: NextRequest,
  { params }: { params: Promise<{ plugin: string }> },
) {
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  const { plugin } = await params;
  const pluginName = decodeURIComponent(plugin);
  if (!pluginName) {
    return NextResponse.json({ error: "plugin param required" }, { status: 400 });
  }
  try {
    const removed = await disconnectConnector(pluginName, user.id);
    return NextResponse.json({ removed });
  } catch (err) {
    return NextResponse.json(
      { error: err instanceof Error ? err.message : String(err) },
      { status: 502 },
    );
  }
}

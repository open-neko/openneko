import { NextRequest, NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { requestPackWorker, readPackRequest, validPackId } from "@/lib/solution-packs";

type Context = { params: Promise<{ packId: string; connectorId: string; action: string }> };
async function handle(request: NextRequest, context: Context) {
  const { packId, connectorId, action } = await context.params;
  if (!validPackId(packId) || !validPackId(connectorId) || !["list", "start", "callback", "disconnect", "configure"].includes(action)) return NextResponse.json({ error: "Unknown account operation" }, { status: 404 });
  if ((request.method === "GET") !== ["list", "callback"].includes(action)) return NextResponse.json({ error: "Method not allowed" }, { status: 405 });
  if (request.method === "POST" && request.headers.get("origin") !== request.nextUrl.origin) return NextResponse.json({ error: "Invalid request origin" }, { status: 403 });
  const user = await getCurrentUser();
  if (!user || action === "configure") {
    const actor = await requireAdminActor();
    if (isDenied(actor)) return actor;
  }
  const owner = user ? `user:${user.id}` : "solo";
  const callback = new URL(`/api/pack-accounts/${packId}/${connectorId}/callback`, request.url).toString();
  const cookieName = `pack-connect-${packId}-${connectorId}`;
  const cookieOptions = { httpOnly: true, secure: request.nextUrl.protocol === "https:", sameSite: "lax" as const, path: "/", maxAge: 600 };
  try {
    let input: Record<string, unknown> = {};
    if (request.method === "POST") {
      input = JSON.parse((await readPackRequest(request, 16 * 1024)).toString("utf8") || "{}");
      if (!input || typeof input !== "object" || Array.isArray(input)) throw new Error("Invalid account request");
    }
    if (action === "start") input.redirectUri = callback;
    if (action === "callback") {
      const state = request.nextUrl.searchParams.get("state");
      if (!state || state !== request.cookies.get(cookieName)?.value) throw new Error("Connection state does not match this browser");
      input = { state, code: request.nextUrl.searchParams.get("code"), error: request.nextUrl.searchParams.get("error"), redirectUri: callback };
    }
    const result = await requestPackWorker(`/admin/packs/${packId}/connections/${connectorId}/${action}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ owner, input }),
    });
    if (result.status !== 200) throw new Error("Account connection failed. Check the settings and permissions, then try again.");
    const body = result.body as { authorizationUrl?: string; state?: string };
    if (action === "start") {
      if (!body.authorizationUrl || !body.state) throw new Error("Invalid connection response");
      const response = NextResponse.json({ authorizationUrl: body.authorizationUrl });
      response.cookies.set(cookieName, body.state, cookieOptions);
      return response;
    }
    if (action === "callback") {
      const response = NextResponse.redirect(new URL("/integrations/packs?connected=1", request.url), 303);
      response.cookies.set(cookieName, "", { ...cookieOptions, maxAge: 0 });
      return response;
    }
    return NextResponse.json(result.body);
  } catch {
    if (action === "callback") {
      const response = NextResponse.redirect(new URL("/integrations/packs?connectionError=1", request.url), 303);
      response.cookies.set(cookieName, "", { ...cookieOptions, maxAge: 0 });
      return response;
    }
    return NextResponse.json({ error: "Account request failed. Check the settings and permissions, then try again." }, { status: 400 });
  }
}
export const GET = handle;
export const POST = handle;

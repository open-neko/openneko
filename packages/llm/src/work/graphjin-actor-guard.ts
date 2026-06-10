import { data_source, db, eq } from "@neko/db";
import { provisionGraphjinClientAuth } from "../graphjin/client-auth";
import { ensureGraphjinGuard } from "./graphjin-guard";

/**
 * GJ4 — the one call every agent path makes before shelling to the
 * GraphJin CLI: install the guard wrapper and, when the org's data
 * source runs source mode (auth_mode='jwt'), provision a per-run
 * client.json with the actor's token and pin the wrapper at it. Legacy
 * sources get the plain guard, byte-for-byte today's behavior.
 */
export async function ensureGraphjinGuardWithActorAuth(opts: {
  orgId: string;
  graphjinBinary: string;
  binRoot: string;
  /** Per-run dir for the client.json (the workspace runRoot). */
  runRoot: string;
  actor: { userId: string | null; role: "admin" | "member" | "service" };
}): Promise<string> {
  const [src] = await db()
    .select({ authMode: data_source.auth_mode, mcpUrl: data_source.mcp_url })
    .from(data_source)
    .where(eq(data_source.org_id, opts.orgId))
    .limit(1);
  let xdgConfigHome: string | undefined;
  if (src?.authMode === "jwt" && src.mcpUrl) {
    const auth = await provisionGraphjinClientAuth({
      runRoot: opts.runRoot,
      serverUrl: src.mcpUrl,
      orgId: opts.orgId,
      userId: opts.actor.userId,
      role: opts.actor.role,
    });
    xdgConfigHome = auth.xdgConfigHome;
  }
  return ensureGraphjinGuard(opts.binRoot, opts.graphjinBinary, {
    ...(xdgConfigHome ? { xdgConfigHome } : {}),
  });
}

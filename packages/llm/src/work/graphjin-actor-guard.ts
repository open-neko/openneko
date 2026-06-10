import { action_policy, and, data_source, db, eq } from "@neko/db";
import { provisionGraphjinClientAuth } from "../graphjin/client-auth";
import {
  ensureGraphjinGuard,
  GRAPHJIN_WRITE_SUBCOMMANDS,
} from "./graphjin-guard";

/**
 * GJ5 — the per-run write grants for the guard. Only an ADMIN actor can
 * hold grants, and only when the org carries an enabled auto_approve
 * policy for kind "graphjin_write" whose allowedTargets.patterns name
 * the write subcommands to open up. Default (no policy): read-only,
 * byte-for-byte today's guard.
 */
export async function resolveGraphjinWriteGrants(
  orgId: string,
  actor: { userId: string | null; role: string | null },
): Promise<string[]> {
  if (actor.role !== "admin") return [];
  const rows = await db()
    .select({
      kinds: action_policy.applies_to_kinds,
      mode: action_policy.mode,
      allowedTargets: action_policy.allowed_targets,
    })
    .from(action_policy)
    .where(and(eq(action_policy.org_id, orgId), eq(action_policy.enabled, true)));
  const grants = new Set<string>();
  for (const row of rows) {
    if (row.mode !== "auto_approve") continue;
    if (!row.kinds.includes("graphjin_write")) continue;
    const patterns = (row.allowedTargets as { patterns?: unknown } | null)
      ?.patterns;
    if (!Array.isArray(patterns)) continue;
    for (const p of patterns) {
      if (
        typeof p === "string" &&
        (GRAPHJIN_WRITE_SUBCOMMANDS as readonly string[]).includes(p)
      ) {
        grants.add(p);
      }
    }
  }
  return Array.from(grants);
}

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
  const allowSubcommands = await resolveGraphjinWriteGrants(
    opts.orgId,
    opts.actor,
  );
  return ensureGraphjinGuard(opts.binRoot, opts.graphjinBinary, {
    ...(xdgConfigHome ? { xdgConfigHome } : {}),
    ...(allowSubcommands.length > 0 ? { allowSubcommands } : {}),
  });
}

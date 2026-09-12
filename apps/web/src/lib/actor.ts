import { app_user, db, eq } from "@neko/db";
import type { RunActor } from "@neko/llm/work";
import { getCurrentUser } from "@/lib/auth";

/**
 * getCurrentUser without a request-scope requirement: outside a Next
 * request (tests, jobs) `cookies()` throws — treat that as "no session"
 * without granting anonymous admin access.
 */
export async function getCurrentUserSafe(): Promise<Awaited<
  ReturnType<typeof getCurrentUser>
> | null> {
  try {
    return await getCurrentUser();
  } catch {
    return null;
  }
}

/** Solo and SSO work both carry the persisted acting user's ID. */
export async function getCurrentActor(): Promise<RunActor> {
  const user = await getCurrentUserSafe();
  if (!user) return { userId: null, role: "member" };
  const [row] = await db()
    .select({ role: app_user.role })
    .from(app_user)
    .where(eq(app_user.id, user.id))
    .limit(1);
  return { userId: user.id, role: row?.role === "admin" ? "admin" : "member" };
}

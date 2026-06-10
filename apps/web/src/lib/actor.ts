import { app_user, db, eq } from "@neko/db";
import type { RunActor } from "@neko/llm/work";
import { getCurrentUser } from "@/lib/auth";

/**
 * getCurrentUser without a request-scope requirement: outside a Next
 * request (tests, jobs) `cookies()` throws — treat that as "no session"
 * (solo profile), exactly like a missing cookie.
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

/**
 * K1: resolve the acting principal for a web-initiated run. With the auth
 * plugin off (solo profile) there is no session user — the operator IS
 * the admin. With auth on, the session user's role is snapshotted here at
 * run start.
 */
export async function getCurrentActor(): Promise<RunActor> {
  const user = await getCurrentUserSafe();
  if (!user) return { userId: null, role: "admin" };
  const [row] = await db()
    .select({ role: app_user.role })
    .from(app_user)
    .where(eq(app_user.id, user.id))
    .limit(1);
  return { userId: user.id, role: row?.role === "member" ? "member" : "admin" };
}

import "server-only";

import { db, eq, organization } from "@neko/db";

export async function getSetupCompleteAt(orgId: string): Promise<Date | null> {
  // Intentionally NOT wrapped in try/catch. A DB error here used to
  // collapse to `null`, which downstream callers treat the same as
  // "setup not complete yet" — bouncing the user back into a wizard
  // they've already filled out. Let the error propagate so Next.js
  // shows an error page instead of an empty wizard.
  const rows = await db()
    .select({ setup_complete_at: organization.setup_complete_at })
    .from(organization)
    .where(eq(organization.id, orgId))
    .limit(1);
  return rows[0]?.setup_complete_at ?? null;
}

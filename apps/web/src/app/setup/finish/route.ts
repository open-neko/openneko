import { NextResponse } from "next/server";
import { revalidatePath } from "next/cache";
import { db, eq, organization } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { hasDataSourceSetup } from "@/lib/data-source-settings";
import { hasPrimaryProviderSetup } from "@/lib/provider-settings";

/**
 * Marks admin setup complete on the org. Gated on the same prerequisites
 * the wizard's "Finish" button enforces in the UI — re-checked server-side
 * so a direct API call can't bypass them. Research is intentionally NOT
 * required: it's optional and admins can skip it.
 */
export async function POST() {
  const [dataReady, primaryReady] = await Promise.all([
    hasDataSourceSetup((await getOrgId())),
    hasPrimaryProviderSetup((await getOrgId())),
  ]);
  if (!dataReady) {
    return NextResponse.json(
      { error: "Data source not configured." },
      { status: 400 },
    );
  }
  if (!primaryReady) {
    return NextResponse.json(
      { error: "Primary model provider not configured." },
      { status: 400 },
    );
  }

  await db()
    .update(organization)
    .set({ setup_complete_at: new Date(), updated_at: new Date() })
    .where(eq(organization.id, (await getOrgId())));

  // Invalidate cached RSC payloads on the gates so the next navigation
  // sees the fresh setup_complete_at value. Wrapped because tests call
  // this handler outside Next's request context, where revalidatePath
  // throws "static generation store missing".
  try {
    revalidatePath("/onboarding");
    revalidatePath("/setup");
  } catch {}

  return NextResponse.json({ ok: true });
}

import { NextRequest, NextResponse } from "next/server";
import { and, app_user, db, eq } from "@neko/db";
import { getCurrentUser } from "@/lib/auth";
import { getOrgId } from "@/lib/db";
import {
  getInstallPolicyPayload,
  saveInstallPolicyDraft,
  type InstallPolicyDraft,
} from "@/lib/install-policy-settings";

async function requireAdmin(): Promise<NextResponse | null> {
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  const orgId = await getOrgId();
  const rows = await db()
    .select({ role: app_user.role })
    .from(app_user)
    .where(and(eq(app_user.id, user.id), eq(app_user.org_id, orgId)))
    .limit(1);
  if (!rows[0] || rows[0].role !== "admin") {
    return NextResponse.json(
      { error: "admin role required to change install policy" },
      { status: 403 },
    );
  }
  return null;
}

export async function GET() {
  // Anyone signed in can read the policy — operators benefit from seeing the
  // trust floor in effect (and the /integrations page renders helper hints
  // based on these flags).
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  const orgId = await getOrgId();
  const payload = await getInstallPolicyPayload(orgId);
  return NextResponse.json(payload);
}

export async function PATCH(request: NextRequest) {
  const denied = await requireAdmin();
  if (denied) return denied;
  try {
    const body = (await request.json()) as InstallPolicyDraft;
    const orgId = await getOrgId();
    const saved = await saveInstallPolicyDraft(orgId, {
      allowUnverified: typeof body.allowUnverified === "boolean" ? body.allowUnverified : undefined,
      allowGitUrlInstalls:
        typeof body.allowGitUrlInstalls === "boolean" ? body.allowGitUrlInstalls : undefined,
      allowedMarketplaces: Array.isArray(body.allowedMarketplaces)
        ? body.allowedMarketplaces
        : undefined,
      allowSandboxedSkillEscape:
        typeof body.allowSandboxedSkillEscape === "boolean"
          ? body.allowSandboxedSkillEscape
          : undefined,
    });
    return NextResponse.json({ policy: saved, source: "org" });
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: message }, { status: 400 });
  }
}

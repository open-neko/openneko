import { NextRequest, NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import { getOrgId } from "@/lib/db";
import {
  getInstallPolicyPayload,
  saveInstallPolicyDraft,
  type InstallPolicyDraft,
} from "@/lib/install-policy-settings";

// OpenNeko has no admin/member role separation today — `app_user.role`
// only gets populated when an SSO plugin maps IdP groups. For the
// common deployment (no SSO, one or a small team of operators), the
// concept of "admin-only" would lock everyone out. Treating any signed-
// in operator as eligible to change install policy is consistent with
// every other /settings route. When a true role model lands, gate at
// the same shared helper used by /settings/agent, /settings/data-source,
// etc. — don't reintroduce a one-off role check just here.
async function requireSignedIn(): Promise<NextResponse | null> {
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  return null;
}

export async function GET() {
  const denied = await requireSignedIn();
  if (denied) return denied;
  const orgId = await getOrgId();
  const payload = await getInstallPolicyPayload(orgId);
  return NextResponse.json(payload);
}

export async function PATCH(request: NextRequest) {
  const denied = await requireSignedIn();
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

import { and, app_user, organization, db, eq, operator_profile, work_run } from "@neko/db";
import { resolveDeploymentProfile } from "./deployment-profile";

/**
 * CV3 — personas. The agent reads a compiled, persona-shaped brief as an
 * <operator-profile> prompt block: who it's working for, their role
 * shape, and what they care about. Raw onboarding answers never enter
 * the prompt (per CONTEXT_VERSIONING §13 — don't inject raw answers);
 * the brief is the curated compilation.
 */
export type OperatorProfile = {
  id: string;
  orgId: string;
  /** '' = the org-default persona (solo profile / unlinked channels). */
  userId: string;
  displayName: string | null;
  /** Free-text role shape ("CFO", "Head of Ops", …) — not a closed enum. */
  roleTemplate: string;
  focusAreas: string[];
  answers: Record<string, unknown>;
  briefMd: string;
};

function toProfile(row: typeof operator_profile.$inferSelect): OperatorProfile {
  return {
    id: row.id,
    orgId: row.org_id,
    userId: row.user_id,
    displayName: row.display_name,
    roleTemplate: row.role_template,
    focusAreas: row.focus_areas,
    answers: row.answers as Record<string, unknown>,
    briefMd: row.brief_md,
  };
}

/**
 * The actor's persona, falling back to the org-default ('') row. Null
 * when the org has configured no personas at all.
 */
export async function getOperatorProfile(
  orgId: string,
  userId: string | null,
): Promise<OperatorProfile | null> {
  if (userId) {
    const [own] = await db()
      .select()
      .from(operator_profile)
      .where(
        and(
          eq(operator_profile.org_id, orgId),
          eq(operator_profile.user_id, userId),
        ),
      )
      .limit(1);
    if (own) return toProfile(own);
  }
  const [fallback] = await db()
    .select()
    .from(operator_profile)
    .where(
      and(eq(operator_profile.org_id, orgId), eq(operator_profile.user_id, "")),
    )
    .limit(1);
  return fallback ? toProfile(fallback) : null;
}

export async function upsertOperatorProfile(input: {
  orgId: string;
  userId?: string | null;
  displayName?: string | null;
  roleTemplate?: string;
  focusAreas?: string[];
  answers?: Record<string, unknown>;
  briefMd?: string;
}): Promise<OperatorProfile> {
  const userId = input.userId ?? "";
  const [row] = await db()
    .insert(operator_profile)
    .values({
      org_id: input.orgId,
      user_id: userId,
      display_name: input.displayName ?? null,
      role_template: input.roleTemplate ?? "",
      focus_areas: input.focusAreas ?? [],
      answers: input.answers ?? {},
      brief_md: input.briefMd ?? "",
    })
    .onConflictDoUpdate({
      target: [operator_profile.org_id, operator_profile.user_id],
      set: {
        ...(input.displayName !== undefined
          ? { display_name: input.displayName }
          : {}),
        ...(input.roleTemplate !== undefined
          ? { role_template: input.roleTemplate }
          : {}),
        ...(input.focusAreas !== undefined
          ? { focus_areas: input.focusAreas }
          : {}),
        ...(input.answers !== undefined ? { answers: input.answers } : {}),
        ...(input.briefMd !== undefined ? { brief_md: input.briefMd } : {}),
        updated_at: new Date(),
      },
    })
    .returning();
  return toProfile(row);
}

/** The run's acting principal (K1 columns), for persona resolution. */
export async function getWorkRunActor(
  runId: string,
  orgId?: string,
): Promise<{ userId: string | null; role: string | null }> {
  const [row] = await db()
    .select({
      userId: work_run.actor_user_id,
      role: work_run.actor_role,
    })
    .from(work_run)
    .where(
      orgId
        ? and(eq(work_run.id, runId), eq(work_run.org_id, orgId))
        : eq(work_run.id, runId),
    )
    .limit(1);
  return row ?? { userId: null, role: null };
}

/** Reuse is keyed to the persisted solo admin account. */
export async function getSoloSandboxUser(
  orgId: string,
  actor: { userId: string | null; role: string | null },
): Promise<{ principalId: string; authorizationRevision: string } | null> {
  if (resolveDeploymentProfile() !== "solo" || actor.role !== "admin") return null;
  const [org] = await db().select({ owner: organization.solo_admin_user_id })
    .from(organization).where(eq(organization.id, orgId)).limit(1);
  if (!org?.owner || (actor.userId && actor.userId !== org.owner)) return null;
  const users = await db().select({ id: app_user.id, role: app_user.role, disabledAt: app_user.disabled_at, sub: app_user.sub })
    .from(app_user).where(and(eq(app_user.org_id, orgId), eq(app_user.id, org.owner))).limit(1);
  if (!users[0] || users[0].role !== "admin" || users[0].disabledAt || users[0].sub) return null;
  return {
    principalId: users[0].id,
    // Solo admin has unrestricted org access; fine-grained grants still use
    // the authorization-revision hook. Launch configuration is hashed separately.
    authorizationRevision: "solo-admin-v1",
  };
}

/** Compiled prompt block. Empty string when there's nothing to say. */
export function buildOperatorProfileSection(
  profile: OperatorProfile | null,
): string {
  if (!profile) return "";
  const lines: string[] = [];
  if (profile.displayName) lines.push(`You are working for ${profile.displayName}.`);
  if (profile.roleTemplate) lines.push(`Their role: ${profile.roleTemplate}.`);
  if (profile.focusAreas.length > 0) {
    lines.push(
      "They care most about:",
      ...profile.focusAreas.map((f) => `- ${f}`),
    );
  }
  if (profile.briefMd.trim()) lines.push("", profile.briefMd.trim());
  if (lines.length === 0) return "";
  return `<operator-profile>\n${lines.join("\n")}\n</operator-profile>`;
}

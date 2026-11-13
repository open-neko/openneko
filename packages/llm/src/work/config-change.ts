import { db, config_change } from "@neko/db";

/**
 * Adds one config_change audit row. A failure is logged only: the write
 * that called it stands.
 */
export async function insertConfigChangeRow(input: {
  orgId: string;
  artifactKind: string;
  artifactRef: string;
  actorUserId?: string | null;
  summary?: string;
  scope?: "team" | "user";
  userId?: string;
  status?: string;
}): Promise<void> {
  try {
    await db().insert(config_change).values({
      org_id: input.orgId,
      artifact_kind: input.artifactKind,
      artifact_ref: input.artifactRef,
      scope: input.scope ?? "team",
      user_id: input.userId ?? "",
      actor_user_id: input.actorUserId ?? null,
      summary: input.summary ?? "",
      status: input.status ?? "recorded",
    });
  } catch (err) {
    console.warn(
      `[config-change] config_change insert failed: ${err instanceof Error ? err.message : err}`,
    );
  }
}

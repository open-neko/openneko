import { eq, heldItems, packsContaining, type EntitlementActor, type HeldItems, type ItemType } from "@neko/db";
import type { AllowedLibrary } from "../library/staging";

/**
 * The entitlement actor for an agent run. A user run resolves through the
 * user's groups. A run without a user is a service run; a pack workflow is
 * limited to its pack's items, and any other service run is a system job.
 */
export async function runEntitlementActor(
  orgId: string,
  actor: { userId: string | null },
  opts: { workflowId?: string | null } = {},
): Promise<EntitlementActor> {
  if (actor.userId) return { orgId, kind: "user", userId: actor.userId };
  if (opts.workflowId) {
    const [packId] = await packsContaining(orgId, { type: "workflow", id: opts.workflowId });
    if (packId) return { orgId, kind: "service", packId };
  }
  return { orgId, kind: "service" };
}

/** Held item ids for a run, or undefined when the actor holds every item. */
export async function runHeldItemIds(actor: EntitlementActor, type: ItemType): Promise<string[] | undefined> {
  const held = await heldItems(actor, type);
  return held === "*" ? undefined : [...held].sort();
}

/** Entitlement actor for a persisted work run; unknown runs hold nothing. */
export async function entitlementActorForRun(orgId: string, runId: string): Promise<EntitlementActor | null> {
  const { and, db, eq, work_run, workflow_run } = await import("@neko/db");
  const [run] = await db()
    .select({ userId: work_run.actor_user_id })
    .from(work_run)
    .where(and(eq(work_run.id, runId), eq(work_run.org_id, orgId)))
    .limit(1);
  if (!run) return null;
  const [workflowRun] = run.userId
    ? []
    : await db()
        .select({ workflowId: workflow_run.workflow_id })
        .from(workflow_run)
        .where(and(eq(workflow_run.work_run_id, runId), eq(workflow_run.org_id, orgId)))
        .limit(1);
  return runEntitlementActor(orgId, { userId: run.userId }, { workflowId: workflowRun?.workflowId ?? null });
}

/**
 * A workflow filter for a run: the run's user sees org workflows they hold
 * and their own personal workflows. Without a run id nothing is filtered.
 */
export async function runWorkflowFilter(
  orgId: string,
  runId: string | null | undefined,
): Promise<(workflow: { id: string; ownerUserId?: string | null }) => boolean> {
  if (!runId) return () => true;
  const actor = await entitlementActorForRun(orgId, runId);
  if (!actor) return () => false;
  const held = await heldItems(actor, "workflow");
  const admin = actor.kind === "service" && !actor.packId;
  return (workflow) => {
    const owner = workflow.ownerUserId ?? "";
    if (actor.kind === "user" && owner === actor.userId) return true;
    if (owner) return admin || held === "*";
    return held === "*" || held.has(workflow.id);
  };
}

export async function libraryAccessFor(actor: EntitlementActor): Promise<{ concepts: HeldItems; collections: HeldItems }> {
  const [concepts, collections] = await Promise.all([heldItems(actor, "library_concept"), heldItems(actor, "library_collection")]);
  return { concepts, collections };
}

/** Team library files a run may stage, or undefined for the whole team library. */
export async function runAllowedLibrary(actor: EntitlementActor): Promise<AllowedLibrary | undefined> {
  const access = await libraryAccessFor(actor);
  if (access.concepts === "*" || access.collections === "*") return undefined;
  const ids = [...access.concepts];
  const { and, db, inArray, isNull, library_concept } = await import("@neko/db");
  const paths = ids.length
    ? (await db()
        .select({ path: library_concept.path })
        .from(library_concept)
        .where(and(eq(library_concept.org_id, actor.orgId), isNull(library_concept.user_id), inArray(library_concept.id, ids)))).map((r) => r.path)
    : [];
  return { prefixes: [...access.collections].sort(), paths: paths.sort() };
}

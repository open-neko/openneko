import { heldItems, packsContaining, type EntitlementActor, type ItemType } from "@neko/db";

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

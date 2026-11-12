import { NextResponse } from "next/server";
import {
  and,
  db,
  eq,
  inArray,
  workflow_definition,
  workflow_run,
  filterHeld,
  heldItems,
  holds,
  type EntitlementActor,
  type HeldItems,
  type ItemRef,
  type ItemType,
} from "@neko/db";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";

/** The signed-in user as an entitlement actor, or null without a session. */
export async function currentEntitlementActor(): Promise<EntitlementActor | null> {
  const actor = await getCurrentActor();
  if (!actor.userId) return null;
  return { orgId: await getOrgId(), kind: "user", userId: actor.userId };
}

export async function heldItemIds(type: ItemType): Promise<HeldItems> {
  if ((await getCurrentActor()).role === "admin") return "*";
  const actor = await currentEntitlementActor();
  return actor ? heldItems(actor, type) : new Set();
}

export async function filterToHeld<T>(type: ItemType, items: T[], idOf: (item: T) => string): Promise<T[]> {
  return filterHeld(await heldItemIds(type), items, idOf);
}

export async function holdsItem(type: ItemType, id: string, parents: ItemRef[] = []): Promise<boolean> {
  if ((await getCurrentActor()).role === "admin") return true;
  const actor = await currentEntitlementActor();
  return actor ? (await holds(actor, type, id, { parents })).allowed : false;
}

export function itemNotFound(label = "Not found"): NextResponse {
  return NextResponse.json({ error: label }, { status: 404 });
}

/**
 * Returns a 404 response when the user does not hold the item, so a detail
 * route never reveals that the item exists. Returns null when allowed.
 */
export async function requireItem(
  type: ItemType,
  id: string,
  opts: { parents?: ItemRef[]; notFound?: string } = {},
): Promise<NextResponse | null> {
  return (await holdsItem(type, id, opts.parents)) ? null : itemNotFound(opts.notFound);
}

/**
 * A workflow is visible when the user holds it and it is an org workflow,
 * or when it is the user's own personal workflow.
 */
export async function workflowVisibility(): Promise<(workflow: { id: string; ownerUserId?: string | null }) => boolean> {
  const actor = await currentEntitlementActor();
  const admin = (await getCurrentActor()).role === "admin";
  if (admin) return () => true;
  if (!actor || actor.kind !== "user") return () => false;
  const held = await heldItems(actor, "workflow");
  return (workflow) => {
    const owner = workflow.ownerUserId ?? "";
    if (owner === actor.userId) return true;
    if (owner) return admin;
    return held === "*" || held.has(workflow.id);
  };
}

export async function requireWorkflow(workflowId: string): Promise<NextResponse | null> {
  const [row] = await db()
    .select({ id: workflow_definition.id, ownerUserId: workflow_definition.owner_user_id })
    .from(workflow_definition)
    .where(and(eq(workflow_definition.org_id, await getOrgId()), eq(workflow_definition.id, workflowId)))
    .limit(1);
  if (!row) return null;
  return (await workflowVisibility())(row) ? null : itemNotFound("Workflow not found");
}

/** Hides outputs of watcher-triggered runs when the user does not hold the watcher. */
export async function watcherRunVisibility(runIds: Array<string | null | undefined>): Promise<(runId: string | null | undefined) => boolean> {
  const ids = [...new Set(runIds.filter((id): id is string => Boolean(id)))];
  if (ids.length === 0) return () => true;
  const held = await heldItemIds("watcher");
  if (held === "*") return () => true;
  const runs = await db()
    .select({ id: workflow_run.id, kind: workflow_run.trigger_kind, payload: workflow_run.trigger_payload })
    .from(workflow_run)
    .where(and(eq(workflow_run.org_id, await getOrgId()), inArray(workflow_run.id, ids)));
  const hidden = new Set(
    runs
      .filter((r) => r.kind === "watcher")
      .filter((r) => !held.has(String((r.payload as { watcherId?: unknown } | null)?.watcherId ?? "")))
      .map((r) => r.id),
  );
  return (runId) => !runId || !hidden.has(runId);
}

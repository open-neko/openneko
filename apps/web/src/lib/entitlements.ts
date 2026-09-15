import { NextResponse } from "next/server";
import {
  and,
  db,
  eq,
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
  const actor = await currentEntitlementActor();
  return actor ? heldItems(actor, type) : new Set();
}

export async function filterToHeld<T>(type: ItemType, items: T[], idOf: (item: T) => string): Promise<T[]> {
  return filterHeld(await heldItemIds(type), items, idOf);
}

export async function holdsItem(type: ItemType, id: string, parents: ItemRef[] = []): Promise<boolean> {
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

/** Library concepts inherit access from every collection prefix above them. */
export function libraryCollectionParents(path: string): ItemRef[] {
  const parts = path.split("/").filter(Boolean);
  const parents: ItemRef[] = [];
  for (let i = 1; i < parts.length; i++) {
    parents.push({ type: "library_collection", id: `${parts.slice(0, i).join("/")}/` });
  }
  return parents;
}

/**
 * A workflow is visible when the user holds it and it is an org workflow,
 * or when it is the user's own personal workflow.
 */
export async function workflowVisibility(): Promise<(workflow: { id: string; ownerUserId?: string | null }) => boolean> {
  const actor = await currentEntitlementActor();
  if (!actor || actor.kind !== "user") return () => false;
  const held = await heldItems(actor, "workflow");
  const admin = (await getCurrentActor()).role === "admin";
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

export async function requireWorkflowRun(workflowRunId: string): Promise<NextResponse | null> {
  const [row] = await db()
    .select({ workflowId: workflow_run.workflow_id })
    .from(workflow_run)
    .where(and(eq(workflow_run.org_id, await getOrgId()), eq(workflow_run.id, workflowRunId)))
    .limit(1);
  if (!row) return null;
  const denied = await requireWorkflow(row.workflowId);
  return denied ? itemNotFound("Run not found") : null;
}

/** Library reader for the current user, limited to held team concepts. */
export async function currentLibraryReader() {
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const entitlementActor = await currentEntitlementActor();
  const access = entitlementActor
    ? {
        concepts: await heldItems(entitlementActor, "library_concept"),
        collections: await heldItems(entitlementActor, "library_collection"),
      }
    : { concepts: new Set<string>(), collections: new Set<string>() };
  return { orgId, userId: actor.userId, isAdmin: actor.role === "admin", access };
}

/** Visibility by workflow id; items without a workflow stay visible. */
export async function workflowIdVisibility(): Promise<(workflowId: string | null | undefined) => boolean> {
  const visible = await workflowVisibility();
  const owners = new Map(
    (await db()
      .select({ id: workflow_definition.id, ownerUserId: workflow_definition.owner_user_id })
      .from(workflow_definition)
      .where(eq(workflow_definition.org_id, await getOrgId()))).map((w) => [w.id, w.ownerUserId]),
  );
  return (workflowId) => !workflowId || (owners.has(workflowId) && visible({ id: workflowId, ownerUserId: owners.get(workflowId) }));
}

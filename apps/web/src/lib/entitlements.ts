import { NextResponse } from "next/server";
import {
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
